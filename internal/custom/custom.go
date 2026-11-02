// Package custom serves the channels declared under channels.custom in the
// tuner config: a `command`, an HTTP poll, an SSE event stream, or a webhook
// ingest. One cache and one shape, so a custom channel answers like any other
// channel instead of being listed and left unserved (tuner#30).
//
// The cache is deliberately a feed cache: the data stays where it is authored,
// and tuner holds only the latest snapshot for a refresh window. That keeps a
// channel ephemeral, which is what its signal decay already assumes.
package custom

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/james-gibson/tuner/internal/config"
)

// ErrNoData is returned by a push channel that nothing has pushed to yet.
var ErrNoData = errors.New("custom: no data yet")

// ErrNotWebhook is returned when an ingest is attempted on a pull channel.
var ErrNotWebhook = errors.New("custom: channel does not accept ingest")

const (
	// MaxBody caps one snapshot, whichever direction it travels.
	MaxBody = 1 << 20 // 1 MiB

	// DefaultRefresh is the cache lifetime of a pull channel that does not set
	// refresh, and the tick a subscribed client is offered.
	DefaultRefresh = 10 * time.Second

	// fetchTimeout bounds one command execution or one HTTP round trip.
	fetchTimeout = 5 * time.Second
)

// Snapshot is the current value of a custom channel.
type Snapshot struct {
	Channel string          `json:"channel"`
	Type    string          `json:"type"`
	Data    json.RawMessage `json:"data,omitempty"`
	Lines   []string        `json:"lines,omitempty"`
	Fetched time.Time       `json:"fetched"`
}

// Info is the part of a channel's configuration the HTTP layer needs.
type Info struct {
	Name    string
	Type    string
	Refresh time.Duration
	// Token is the shared secret for this channel: sent as a bearer token to
	// an http/sse source, and required on an ingest when set.
	Token string
}

// Set is the configured custom channels and their cached snapshots.
type Set struct {
	mu       sync.Mutex
	channels map[string]*channel
}

type channel struct {
	info    Info
	command string
	url     string
	cfgErr  error

	client *http.Client

	mu   sync.Mutex
	snap *Snapshot
	at   time.Time
}

// New builds a Set from the configured custom channels. A channel with a
// configuration problem is still addressable: every request for it reports the
// problem instead of pretending the channel exists (tuner#30: a listed channel
// must answer).
func New(chs []config.CustomChannel) *Set {
	set := &Set{channels: make(map[string]*channel, len(chs))}
	for _, c := range chs {
		if c.Name == "" {
			continue
		}
		ch := &channel{
			info: Info{
				Name:    c.Name,
				Type:    c.Source.Type,
				Refresh: refreshOf(c),
			},
			command: c.Source.Command,
			url:     c.Source.URL,
			client:  &http.Client{Timeout: fetchTimeout},
		}
		if c.Auth != nil {
			ch.info.Token = c.Auth.Token
		}
		if err := check(c); err != nil {
			ch.cfgErr = err
		}
		set.channels[c.Name] = ch
	}
	return set
}

// refreshOf is the cache lifetime for a channel: its refresh, else the default.
func refreshOf(c config.CustomChannel) time.Duration {
	if c.Refresh == "" {
		return DefaultRefresh
	}
	d, err := time.ParseDuration(c.Refresh)
	if err != nil || d <= 0 {
		return DefaultRefresh
	}
	return d
}

// check reports what is wrong with one channel's configuration. It is the
// single definition of "well configured", used both here and by Validate.
func check(c config.CustomChannel) error {
	switch c.Source.Type {
	case config.SourceCommand:
		if strings.TrimSpace(c.Source.Command) == "" {
			return fmt.Errorf("custom channel %q: command source has no command", c.Name)
		}
	case config.SourceHTTP, config.SourceSSE:
		if !isHTTPURL(c.Source.URL) {
			return fmt.Errorf("custom channel %q: %s source needs an http(s) url, got %q",
				c.Name, c.Source.Type, c.Source.URL)
		}
	case config.SourceWebhook:
		// Nothing to check: a push channel is defined by its route.
	case "":
		return fmt.Errorf("custom channel %q: no source type (want %s, %s, %s or %s)",
			c.Name, config.SourceCommand, config.SourceHTTP, config.SourceSSE, config.SourceWebhook)
	default:
		return fmt.Errorf("custom channel %q: unknown source type %q (want %s, %s, %s or %s)",
			c.Name, c.Source.Type, config.SourceCommand, config.SourceHTTP, config.SourceSSE, config.SourceWebhook)
	}
	if c.Refresh != "" {
		if d, err := time.ParseDuration(c.Refresh); err != nil || d <= 0 {
			return fmt.Errorf("custom channel %q: refresh %q is not a positive duration", c.Name, c.Refresh)
		}
	}
	return nil
}

// Validate returns every configuration problem across the custom channels.
// `tuner validate` calls it; the server calls New, which keeps the same verdict
// per channel.
func Validate(chs []config.CustomChannel) []error {
	var errs []error
	seen := make(map[string]bool, len(chs))
	for _, c := range chs {
		if c.Name == "" {
			errs = append(errs, errors.New("custom channel without a name"))
			continue
		}
		if seen[c.Name] {
			errs = append(errs, fmt.Errorf("custom channel %q is declared twice", c.Name))
		}
		seen[c.Name] = true
		if err := check(c); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

func isHTTPURL(raw string) bool {
	return strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://")
}

// Info returns a channel's configuration facts.
func (s *Set) Info(name string) (Info, bool) {
	s.mu.Lock()
	ch, ok := s.channels[name]
	s.mu.Unlock()
	if !ok {
		return Info{}, false
	}
	return ch.info, true
}

// ConfigError returns the configuration problem that keeps a channel from
// serving, or nil. The discovery surface uses it so a channel that cannot answer
// is not listed as if it could.
func (s *Set) ConfigError(name string) error {
	s.mu.Lock()
	ch, ok := s.channels[name]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("custom: unknown channel %q", name)
	}
	return ch.cfgErr
}

// Snapshot returns the channel's current value, fetching it if the cached one
// is older than the channel's refresh. A push channel returns ErrNoData until
// something is pushed to it.
func (s *Set) Snapshot(ctx context.Context, name string) (Snapshot, error) {
	s.mu.Lock()
	ch, ok := s.channels[name]
	s.mu.Unlock()
	if !ok {
		return Snapshot{}, fmt.Errorf("custom: unknown channel %q", name)
	}
	return ch.snapshot(ctx)
}

func (c *channel) snapshot(ctx context.Context) (Snapshot, error) {
	if c.cfgErr != nil {
		return Snapshot{}, c.cfgErr
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.info.Type == config.SourceWebhook {
		if c.snap == nil {
			return Snapshot{}, ErrNoData
		}
		return *c.snap, nil
	}

	if c.snap != nil && time.Since(c.at) < c.info.Refresh {
		return *c.snap, nil
	}

	var (
		body []byte
		err  error
	)
	if c.info.Type == config.SourceCommand {
		body, err = c.fetchCommand(ctx)
	} else {
		body, err = c.fetchHTTP(ctx, c.info.Type == config.SourceSSE)
	}
	if err != nil {
		return Snapshot{}, err
	}

	snap := parse(c.info.Name, c.info.Type, body)
	c.snap = &snap
	c.at = time.Now()
	return snap, nil
}

func (c *channel) fetchCommand(ctx context.Context) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	// The command is operator-supplied configuration, the same trust as the
	// shell: a config file that names it can run it.
	cmd := exec.CommandContext(ctx, "sh", "-c", c.command) //nolint:gosec // G204: operator-supplied config, by design
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			return nil, fmt.Errorf("custom channel %q: command: %w", c.info.Name, err)
		}
		return nil, fmt.Errorf("custom channel %q: command: %w: %s", c.info.Name, err, detail)
	}
	if len(out) > MaxBody {
		return nil, fmt.Errorf("custom channel %q: command output exceeds %d bytes", c.info.Name, MaxBody)
	}
	return out, nil
}

// fetchHTTP reads one snapshot over HTTP. For an sse source it opens the stream
// and takes the first event: reconnecting per refresh keeps the last value in
// the cache without a goroutine per channel, and an event source that greets a
// new subscriber with its current state answers exactly like a poll.
func (c *channel) fetchHTTP(ctx context.Context, stream bool) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("custom channel %q: %w", c.info.Name, err)
	}
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	if c.info.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.info.Token)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("custom channel %q: %w", c.info.Name, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("custom channel %q: %s returned %d", c.info.Name, c.url, resp.StatusCode)
	}

	body := io.LimitReader(resp.Body, MaxBody)
	if !stream {
		data, readErr := io.ReadAll(body)
		if readErr != nil {
			return nil, fmt.Errorf("custom channel %q: read: %w", c.info.Name, readErr)
		}
		return data, nil
	}
	event, err := firstEvent(body)
	if err != nil {
		return nil, fmt.Errorf("custom channel %q: %w", c.info.Name, err)
	}
	return event, nil
}

// firstEvent returns the data of the first SSE event on r, per the stream
// grammar: "data:" lines up to the blank line, joined with newlines.
func firstEvent(r io.Reader) ([]byte, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 8<<10), MaxBody)
	var data []string
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if len(data) > 0 {
				return []byte(strings.Join(data, "\n")), nil
			}
			continue // comment keep-alive or an event without data
		}
		if strings.HasPrefix(line, ":") {
			continue // comment / heartbeat
		}
		if payload, ok := strings.CutPrefix(line, "data:"); ok {
			data = append(data, strings.TrimPrefix(payload, " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read event stream: %w", err)
	}
	if len(data) > 0 {
		return []byte(strings.Join(data, "\n")), nil
	}
	return nil, errors.New("event stream closed without an event")
}

// parse turns a body into a snapshot: JSON when it is JSON, otherwise lines.
// Both shapes are the same ones internal/notes accepts, so a feed moved from a
// notes channel to a custom channel does not change shape.
func parse(name, kind string, body []byte) Snapshot {
	snap := Snapshot{Channel: name, Type: kind, Fetched: time.Now().UTC()}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && json.Valid(trimmed) {
		snap.Data = append(json.RawMessage(nil), trimmed...)
		return snap
	}
	if len(trimmed) > 0 {
		snap.Lines = append(snap.Lines, strings.Split(string(trimmed), "\n")...)
	}
	return snap
}

// Ingest stores a pushed value. Only a webhook channel accepts one; the caller
// has already decided that the push was allowed (see the server's ingest guard).
func (s *Set) Ingest(name string, body []byte) (Snapshot, error) {
	s.mu.Lock()
	ch, ok := s.channels[name]
	s.mu.Unlock()
	if !ok {
		return Snapshot{}, fmt.Errorf("custom: unknown channel %q", name)
	}
	if ch.cfgErr != nil {
		return Snapshot{}, ch.cfgErr
	}
	if ch.info.Type != config.SourceWebhook {
		return Snapshot{}, ErrNotWebhook
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return Snapshot{}, fmt.Errorf("custom channel %q: empty push", name)
	}
	if len(body) > MaxBody {
		return Snapshot{}, fmt.Errorf("custom channel %q: push exceeds %d bytes", name, MaxBody)
	}

	snap := parse(name, ch.info.Type, body)
	ch.mu.Lock()
	ch.snap = &snap
	ch.at = time.Now()
	ch.mu.Unlock()
	return snap, nil
}

// ExposureProblems reports webhook channels that will refuse a push from
// anywhere but this machine, when the broadcast listener is reachable beyond
// loopback. The ingest route already refuses those pushes at request time (see
// the unshippable-offense guard in internal/server); this exists so an operator
// is told at startup rather than by a 403.
func ExposureProblems(chs []config.CustomChannel, listen string) []string {
	if isLoopbackListen(listen) {
		return nil
	}
	var out []string
	for _, c := range chs {
		if c.Source.Type != config.SourceWebhook || c.Name == "" {
			continue
		}
		if c.Auth != nil && c.Auth.Token != "" {
			continue
		}
		out = append(out, fmt.Sprintf(
			"custom channel %q accepts webhook ingest from loopback only on a non-loopback listener (%s); "+
				"set channels.custom[].auth.token to accept pushes over the network", c.Name, listen))
	}
	return out
}

// isLoopbackListen reports whether a listen address is confined to this host.
// An empty host (":58093") is every interface, which is not loopback.
func isLoopbackListen(listen string) bool {
	host := listen
	if h, _, err := net.SplitHostPort(listen); err == nil {
		host = h
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
