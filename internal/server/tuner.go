package server

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/james-gibson/tuner/internal/channelsource"
	"github.com/james-gibson/tuner/internal/config"
	"github.com/james-gibson/tuner/internal/custom"
	"github.com/james-gibson/tuner/internal/llms"
)

// Server handles HTTP endpoints for Tuner.
type Server struct {
	cfg     *config.Config
	version string
	startAt time.Time

	// sources is the set of channel sources discovered via mDNS. It may be
	// nil, in which case only configured presets and custom channels exist.
	sources *channelsource.Registry

	// custom is the configured channels.custom set: the channels whose data
	// comes from a command, an HTTP poll, an SSE subscription, or an ingest.
	custom *custom.Set

	mu       sync.RWMutex
	audience map[string]AudienceMetric
}

// AudienceMetric holds broadcaster-reported audience data.
type AudienceMetric struct {
	Channel   string         `json:"channel"`
	Count     int            `json:"count"`
	Signal    float64        `json:"signal"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// Options configures a new Server.
type Options struct {
	Config *config.Config
	// Channels is the live registry of discovered channel sources. Optional:
	// without it the server only knows the configured channels.
	Channels *channelsource.Registry
	Version  string
}

// New creates a new Server.
func New(opts Options) *Server {
	return &Server{
		cfg:      opts.Config,
		version:  opts.Version,
		startAt:  time.Now(),
		sources:  opts.Channels,
		custom:   custom.New(opts.Config.Channels.Custom),
		audience: make(map[string]AudienceMetric),
	}
}

// HandleHealthz returns 200 if alive.
func (s *Server) HandleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"status": "ok",
		"time":   time.Now().UTC().Format(time.RFC3339Nano),
	})
}

// HandleReadyz returns 200 if ready to serve.
func (s *Server) HandleReadyz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"status": "ready",
		"mode":   s.cfg.Mode,
	})
}

// HandleStatus returns service status summary.
func (s *Server) HandleStatus(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	audienceCount := len(s.audience)
	s.mu.RUnlock()

	presetCount := len(s.cfg.Channels.Preset)
	customCount := len(s.cfg.Channels.Custom)
	discoveredCount := len(s.discoveredSources())

	writeJSON(w, map[string]any{
		"service":  "tuner",
		"version":  s.version,
		"mode":     s.cfg.Mode,
		"uptime":   time.Since(s.startAt).String(),
		"channels": presetCount + customCount + discoveredCount,
		"audience": audienceCount,
	})
}

// HandleListChannels lists available channels: configured presets and custom
// channels, plus every smoke-alarm source discovered via mDNS. A discovered
// instance is a channel as soon as its announcement arrives, without a restart
// (features/tuner-mdns-continuous.feature).
func (s *Server) HandleListChannels(w http.ResponseWriter, _ *http.Request) {
	channels := []map[string]any{}
	for _, p := range s.cfg.Channels.Preset {
		channels = append(channels, map[string]any{
			"name": p.Name,
			"type": "preset",
		})
	}
	for _, c := range s.cfg.Channels.Custom {
		entry := map[string]any{
			"name":   c.Name,
			"type":   "custom",
			"source": c.Source.Type,
		}
		// A channel that cannot serve says so where it is advertised, rather
		// than being listed as one that can (tuner#30).
		if err := s.custom.ConfigError(c.Name); err != nil {
			entry["status"] = "misconfigured"
			entry["error"] = err.Error()
		}
		channels = append(channels, entry)
	}
	for _, src := range s.discoveredSources() {
		channels = append(channels, map[string]any{
			"name":       src.Instance,
			"type":       "discovered",
			"endpoint":   src.Endpoint,
			"status_url": src.StatusURL(),
		})
	}
	writeJSON(w, map[string]any{"channels": channels})
}

// HandleChannel routes channel-specific requests.
func (s *Server) HandleChannel(w http.ResponseWriter, r *http.Request) {
	// /channels/{name}[/{action}]
	name, action := splitChannelPath(r.URL.Path)

	// A configured custom channel owns its data routes: a listed channel has to
	// answer with its source, not the generic "available" reply (tuner#30). The
	// broadcast-wide actions (status, caller, audience) still fall through.
	if info, ok := s.custom.Info(name); ok {
		switch action {
		case "":
			s.handleCustomSnapshot(w, r, info)
			return
		case "sse":
			s.handleCustomSSE(w, r, info)
			return
		case "ingest":
			s.handleCustomIngest(w, r, info)
			return
		}
	}

	switch action {
	case "sse":
		s.handleChannelSSE(w, r, name)
		return
	case "ingest":
		// Only a configured webhook channel has an ingest route; reaching here
		// means the name is not one (a preset or a discovered instance).
		http.Error(w, "no webhook channel named "+name, http.StatusNotFound)
		return
	case "status":
		s.handleChannelStatus(w, r, name)
		return
	case "caller":
		s.handleCaller(w, r, name)
		return
	case "audience":
		s.handleAudience(w, r, name)
		return
	}

	// A discovered source is a live channel: report where it is and where its
	// data comes from, so a consumer can poll it without another discovery.
	if src, ok := s.channelSource(name); ok {
		writeJSON(w, map[string]any{
			"channel":       name,
			"type":          "discovered",
			"status":        "available",
			"instance":      src.Instance,
			"endpoint":      src.Endpoint,
			"status_url":    src.StatusURL(),
			"role":          src.Role,
			"trust_rung":    src.TrustRung,
			"discovered_at": src.DiscoveredAt,
			"last_seen":     src.LastSeen,
		})
		return
	}

	// Default: return channel info.
	writeJSON(w, map[string]any{
		"channel": name,
		"status":  "available",
	})
}

// splitChannelPath returns the channel name and trailing action from a request
// path. The action is matched as a suffix, not at the first slash: a discovered
// instance keeps its DNS-SD label, and a lezz-demo seed is named "demo/alarm-a"
// -- splitting at the first slash would make that channel unaddressable.
func splitChannelPath(path string) (name, action string) {
	path = strings.TrimPrefix(path, "/channels/")
	for _, suffix := range []string{"/sse", "/status", "/caller", "/audience", "/ingest"} {
		if strings.HasSuffix(path, suffix) {
			return strings.TrimSuffix(path, suffix), strings.TrimPrefix(suffix, "/")
		}
	}
	return path, ""
}

// handleChannelStatus proxies the discovered instance's /status document. This
// is what makes a discovered source a channel rather than a directory entry:
// polling it returns the instance's current health.
func (s *Server) handleChannelStatus(w http.ResponseWriter, r *http.Request, name string) {
	src, ok := s.channelSource(name)
	if !ok {
		http.Error(w, "unknown discovered channel: "+name, http.StatusNotFound)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	body, err := src.Status(ctx)
	if err != nil {
		log.Printf("channel %q: %v", name, err) //nolint:gosec // G706: %q escapes the channel name from the request path
		http.Error(w, "channel source unavailable", http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	// The body is the upstream /status document, already validated as JSON by
	// Source.Status and served with a JSON content type, not rendered as HTML.
	_, _ = w.Write(body) //nolint:gosec // G705: JSON body from the discovered source, not markup
}

// channelSource returns a discovered source by instance name.
func (s *Server) channelSource(name string) (channelsource.Source, bool) {
	if s.sources == nil {
		return channelsource.Source{}, false
	}
	return s.sources.Get(name)
}

// discoveredSources is every live source, or nil when discovery is disabled.
func (s *Server) discoveredSources() []channelsource.Source {
	if s.sources == nil {
		return nil
	}
	return s.sources.List()
}

// handleCustomSnapshot serves the current value of a custom channel, fetching it
// from the configured source if the cached one has expired.
func (s *Server) handleCustomSnapshot(w http.ResponseWriter, r *http.Request, info custom.Info) {
	snap, err := s.custom.Snapshot(r.Context(), info.Name)
	switch {
	case errors.Is(err, custom.ErrNoData):
		// Nothing has been pushed to a webhook channel yet. That is a normal
		// state for a push channel, not a failure.
		writeJSON(w, map[string]any{
			"channel": info.Name,
			"type":    info.Type,
			"status":  "waiting",
		})
	case err != nil:
		log.Printf("custom channel %q: %v", info.Name, err) //nolint:gosec // G706: %q escapes the channel name
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"channel": info.Name,
			"type":    info.Type,
			"status":  "error",
			"error":   err.Error(),
		})
	default:
		writeJSON(w, snap)
	}
}

// handleCustomSSE streams a custom channel: the current snapshot, then each new
// one as the source changes. A watching client polls tuner, never the source.
func (s *Server) handleCustomSSE(w http.ResponseWriter, r *http.Request, info custom.Info) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	var last []byte
	emit := func() {
		snap, err := s.custom.Snapshot(r.Context(), info.Name)
		if err != nil {
			if errors.Is(err, custom.ErrNoData) {
				return // a push channel with nothing pushed yet; wait
			}
			// %q Go-quotes the name and error, so neither can break the frame.
			_, _ = fmt.Fprintf(w, "event: warning\ndata: {\"channel\":%q,\"error\":%q}\n\n", info.Name, err.Error()) //nolint:gosec // G705: %q escapes before it reaches the body
			flusher.Flush()
			return
		}
		payload, err := json.Marshal(snap)
		if err != nil || bytes.Equal(payload, last) {
			return
		}
		last = payload
		_, _ = fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", payload)
		flusher.Flush()
	}

	emit()

	// The tick is the channel's own refresh: a client is never told to look
	// faster than tuner is willing to fetch.
	ticker := time.NewTicker(info.Refresh)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			emit()
		case <-r.Context().Done():
			return
		}
	}
}

// handleCustomIngest accepts one pushed value for a webhook channel.
//
// This is the only write route on the broadcast surface, so it is guarded twice.
// A channel with an auth token requires that token (a bearer secret, the same
// one used to fetch a pull source). A channel without one accepts a push only
// from this machine: ingest is local by construction, and a listener reachable
// from the LAN must not turn a missing token into an open write endpoint. That
// makes the offense -- an unauthenticated public ingest -- unrepresentable
// rather than merely logged; see kb/.opencode/skills/unshippable-offense.
func (s *Server) handleCustomIngest(w http.ResponseWriter, r *http.Request, info custom.Info) {
	if info.Type != config.SourceWebhook {
		http.Error(w, "channel "+info.Name+" is a "+info.Type+" channel, not webhook", http.StatusMethodNotAllowed)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if info.Token != "" {
		if !tokenMatches(r, info.Token) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="tuner"`)
			http.Error(w, "ingest refused: a bearer token is required for this channel", http.StatusUnauthorized)
			return
		}
	} else if !isLoopbackPeer(r) {
		http.Error(w, "ingest refused: channel "+info.Name+
			" has no auth token and accepts pushes from loopback only", http.StatusForbidden)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, custom.MaxBody))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusRequestEntityTooLarge)
		return
	}

	snap, err := s.custom.Ingest(info.Name, body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, snap)
}

// tokenMatches compares the request's bearer token with the configured one in
// constant time, so the comparison leaks nothing about the secret.
func tokenMatches(r *http.Request, token string) bool {
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(strings.TrimSpace(got)), []byte(token)) == 1
}

// isLoopbackPeer reports whether the request was made from this machine. The
// remote address is the peer of this connection, not a forwarding header, so a
// client cannot claim loopback.
func isLoopbackPeer(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// HandleLLMSTxt generates llms.txt on demand using Gherkin features.
func (s *Server) HandleLLMSTxt(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")

	// Parse Gherkin features on demand.
	scenarios, err := llms.ParseFeatures("features")
	if err != nil {
		log.Printf("llms.txt: parse features: %v (continuing without)", err)
	}

	var channels []llms.ChannelInfo
	for _, p := range s.cfg.Channels.Preset {
		channels = append(channels, llms.ChannelInfo{
			Name:        p.Name,
			Description: fmt.Sprintf("%s monitoring", p.Name),
		})
	}
	for _, c := range s.cfg.Channels.Custom {
		description := fmt.Sprintf("custom %s channel", c.Source.Type)
		if err := s.custom.ConfigError(c.Name); err != nil {
			// Advertised, so say what it cannot do rather than describing it as
			// a channel an agent can consume.
			description = fmt.Sprintf("custom %s channel (misconfigured: %v)", c.Source.Type, err)
		}
		channels = append(channels, llms.ChannelInfo{
			Name:        c.Name,
			Description: description,
		})
	}

	info := llms.RuntimeInfo{
		Mode:          s.cfg.Mode,
		HealthAddr:    s.cfg.Health.ListenAddr,
		BroadcastAddr: s.cfg.Broadcast.Listen,
		Capabilities: []string{
			"mDNS discovery of smoke-alarm services",
			"Real-time alert signal with 30-minute decay",
			"Channel visualization via Television",
			"Interactive caller line",
		},
		Channels: channels,
	}

	body := llms.GenerateLLMSTxt(scenarios, info)
	_, _ = w.Write([]byte(body))
}

func (s *Server) handleChannelSSE(w http.ResponseWriter, _ *http.Request, channel string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Send initial event. %q Go-quotes the channel name, escaping quotes,
	// backslashes and control characters, so it is safe inside the JSON string.
	_, _ = fmt.Fprintf(w, "event: connected\ndata: {\"channel\":%q}\n\n", channel) //nolint:gosec // G705: %q escapes the value before it reaches the body
	flusher.Flush()

	// TODO: stream real channel data from presets/sources.
	// For now, send a heartbeat until client disconnects.
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for t := range ticker.C {
		_, _ = fmt.Fprintf(w, "event: heartbeat\ndata: {\"time\":%q}\n\n", t.UTC().Format(time.RFC3339Nano))
		flusher.Flush()
	}
}

func (s *Server) handleCaller(w http.ResponseWriter, r *http.Request, channel string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var msg struct {
		Message  string `json:"message"`
		From     string `json:"from"`
		Priority string `json:"priority"`
	}
	if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// TODO: fan out to SSE subscribers.
	writeJSON(w, map[string]any{
		"status":  "received",
		"channel": channel,
	})
}

func (s *Server) handleAudience(w http.ResponseWriter, r *http.Request, channel string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var metric AudienceMetric
	if err := json.NewDecoder(r.Body).Decode(&metric); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	metric.Channel = channel
	metric.UpdatedAt = time.Now()

	s.mu.Lock()
	s.audience[channel] = metric
	s.mu.Unlock()

	writeJSON(w, map[string]string{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(v)
}
