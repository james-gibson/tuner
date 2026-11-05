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
	"github.com/james-gibson/tuner/internal/featurebrowser"
	"github.com/james-gibson/tuner/internal/llms"
	"github.com/james-gibson/tuner/internal/mdns"
	"github.com/james-gibson/tuner/internal/notes"
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

	notes *notes.Store

	// isotopes returns the currently advertised adhd endpoints; nil when the
	// process has no browse loop (receive mode, tests).
	isotopes func() []mdns.Endpoint

	// features is the adhd certification browser behind the adhd-features
	// channel (tuner#37). It is always present: with no discovered instance
	// and no configured endpoint it answers "no sources" rather than 404, so a
	// listed channel always answers (tuner#30).
	features *featurebrowser.Browser
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
	// Isotopes returns the isotope endpoints currently advertised on the
	// network — the adhd instances the certification browser calls. It is a
	// function because discovery keeps running after the server starts. The
	// channel registry cannot supply this set: it holds only _smoke-alarm._tcp
	// records, because only those back a /status document.
	Isotopes func() []mdns.Endpoint
	Version  string
}

// New creates a new Server.
func New(opts Options) *Server {
	s := &Server{
		cfg:      opts.Config,
		version:  opts.Version,
		startAt:  time.Now(),
		sources:  opts.Channels,
		isotopes: opts.Isotopes,
		custom:   custom.New(opts.Config.Channels.Custom),
		audience: make(map[string]AudienceMetric),
		notes:    notes.NewStore(30 * time.Second),
	}
	// The browser reads the live source set on every fetch, so an adhd
	// instance discovered after the server started is browsable without a
	// restart — the same rule continuous discovery applies to channels.
	s.features = featurebrowser.New(featurebrowser.Options{
		Version:  opts.Version,
		Refresh:  opts.Config.FeatureRefreshDuration(),
		Identity: opts.Config.FeatureIdentity,
		Sources:  s.featureSources,
	})
	return s
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
	notesCount := len(s.cfg.Channels.Notes)
	discoveredCount := len(s.discoveredSources())
	// +1 for the generated certification browser: it is always listed by
	// HandleListChannels, so the count has to include it or /status disagrees
	// with /channels (tuner#37).
	browserCount := 1

	writeJSON(w, map[string]any{
		"service":  "tuner",
		"version":  s.version,
		"mode":     s.cfg.Mode,
		"uptime":   time.Since(s.startAt).String(),
		"channels": presetCount + customCount + notesCount + browserCount + discoveredCount,
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
	for _, n := range s.cfg.Channels.Notes {
		channels = append(channels, map[string]any{
			"name":   n.Name,
			"type":   "notes",
			"source": n.Source,
		})
	}
	// The certification browser is a channel like any other: it is listed here
	// and answers on GET /channels/adhd-features, including the honest
	// "no sources" answer (tuner#37).
	channels = append(channels, map[string]any{
		"name":   featurebrowser.ChannelName,
		"type":   "adhd-features",
		"source": "adhd-mcp",
	})
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

	// The adhd-features channel is generated from the browser's merged view
	// rather than stored: it owns its default and sse routes, and every other
	// action falls through to the shared ones (status, caller, audience).
	if name == featurebrowser.ChannelName {
		switch action {
		case "":
			writeJSON(w, featureViewResponse(name, s.features.View(r.Context())))
			return
		case "sse":
			s.handleFeatureSSE(w, r)
			return
		}
	}

	// A notes channel owns its own default and "sse" routes: the generic
	// handlers below only know presets.
	if src, ok := s.notesSource(name); ok && (action == "" || action == "sse") {
		if action == "sse" {
			s.handleNotesSSE(w, r, name, src)
			return
		}
		feed, err := s.notes.Get(name, src)
		if err != nil {
			writeJSON(w, map[string]any{"channel": name, "type": "notes", "error": err.Error()})
			return
		}
		writeJSON(w, map[string]any{
			"channel": name, "type": "notes", "title": feed.Title,
			"items": feed.Items, "count": len(feed.Items), "fetched": feed.Fetched,
		})
		return
	}

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

// featureSources is the browser's source set: the adhd instances the mDNS
// browser has discovered, with the configured endpoints as the fallback for
// when mDNS is unavailable. Discovery wins when it found something — the rule
// the feature file pins for "falls back to configured ADHD endpoint".
//
// The channel registry is deliberately not consulted: it keeps only
// _smoke-alarm._tcp records, and an adhd instance advertises
// _adhd-isotope._tcp, which backs no /status and is not a channel source.
func (s *Server) featureSources() []featurebrowser.Source {
	var discovered []featurebrowser.Source
	if s.isotopes != nil {
		discovered = featurebrowser.DiscoveredSources(s.isotopes())
	}
	configured := featurebrowser.ConfigSources(s.cfg.FeatureBrowser.Endpoints)
	return featurebrowser.SourceSet(discovered, configured)
}

// featureChannelResponse is the adhd-features channel body: the channel's own
// identity plus the merged view at the top level, so a consumer reads
// view.status without unwrapping a second envelope.
type featureChannelResponse struct {
	Channel string `json:"channel"`
	Type    string `json:"type"`
	featurebrowser.View
}

func featureViewResponse(name string, view featurebrowser.View) featureChannelResponse {
	return featureChannelResponse{Channel: name, Type: "adhd-features", View: view}
}

// handleFeatureSSE streams the merged certification view: the current view
// once, then a view event whenever the merged state changes.
//
// The stream is poll-based, not push. adhd does not publish coverage updates —
// its certification tools are declared absent (adhd#24 closed @red, adhd#39
// open) — so tuner re-reads within the refresh window and announces a change
// only when the merged view actually differs, which is what "refreshes
// automatically when new certifications arrive" can honestly mean here.
func (s *Server) handleFeatureSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	last := ""
	emitIfChanged := func(view featurebrowser.View) {
		key := featureViewKey(view)
		if key == last {
			return
		}
		last = key
		payload, err := json.Marshal(featureViewResponse(featurebrowser.ChannelName, view))
		if err != nil {
			return
		}
		_, _ = fmt.Fprintf(w, "event: view\ndata: %s\n\n", payload) //nolint:gosec // G705: payload is json.Marshal output, served as text/event-stream
		flusher.Flush()
	}

	emitIfChanged(s.features.View(r.Context()))

	ticker := time.NewTicker(s.featurePollInterval())
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			emitIfChanged(s.features.View(r.Context()))
		case <-r.Context().Done():
			return
		}
	}
}

// featureViewKey is the change-detection key: the view without the fetch
// timestamps — the view's own and each source's — so a poll that returns the
// same state is not re-announced. Everything else in the view is adhd's
// answer, and a change in any of it is worth an event.
func featureViewKey(view featurebrowser.View) string {
	view.Fetched = time.Time{}
	// The sources slice is shared with the browser's cache, so copying it
	// before clearing the per-source timestamps keeps the key from mutating a
	// view another request is reading.
	sources := make([]featurebrowser.Source, len(view.Sources))
	copy(sources, view.Sources)
	for i := range sources {
		sources[i].Fetched = time.Time{}
	}
	view.Sources = sources
	payload, err := json.Marshal(view)
	if err != nil {
		return ""
	}
	return string(payload)
}

// featurePollInterval bounds how often the stream re-reads the browser. The
// browser's own cache shortens the effective rate; the floor keeps a
// misconfigured sub-second refresh from spinning.
func (s *Server) featurePollInterval() time.Duration {
	interval := s.cfg.FeatureRefreshDuration()
	if interval < 2*time.Second {
		return 2 * time.Second
	}
	return interval
}

func (s *Server) notesSource(name string) (string, bool) {
	for _, n := range s.cfg.Channels.Notes {
		if n.Name == name {
			return n.Source, true
		}
	}
	return "", false
}

// handleNotesSSE streams a notes feed: the current items once, then any new item as
// it appears. A note is ephemeral on the wire — the feed is the record, the channel
// is the announcement.
func (s *Server) handleNotesSSE(w http.ResponseWriter, r *http.Request, name, source string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	seen := map[string]bool{}
	emit := func(feed notes.Feed) {
		for _, item := range feed.Items {
			if item.ID == "" || seen[item.ID] {
				continue
			}
			seen[item.ID] = true
			payload, err := json.Marshal(item)
			if err != nil {
				continue
			}
			_, _ = fmt.Fprintf(w, "event: item\ndata: %s\n\n", payload)
		}
		flusher.Flush()
	}

	if feed, err := s.notes.Get(name, source); err == nil {
		emit(feed)
	} else {
		_, _ = fmt.Fprintf(w, "event: warning\ndata: {\"channel\":%q,\"error\":%q}\n\n", name, err.Error()) //nolint:gosec // G705: %q escapes before it reaches the body
		flusher.Flush()
	}

	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if feed, err := s.notes.Get(name, source); err == nil {
				emit(feed)
			}
		case <-r.Context().Done():
			return
		}
	}
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
	// Notes channels are deliberately absent from llms.txt. They carry a lab's
	// working questions and must not become a discovery surface -- see
	// kb/.opencode/skills/unshippable-offense.
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
	// The certification browser is generated from the adhd sources, so it is
	// advertised like a preset channel rather than a per-instance one.
	channels = append(channels, llms.ChannelInfo{
		Name:        featurebrowser.ChannelName,
		Description: "adhd feature and skill certification, merged over the discovered instances",
	})

	info := llms.RuntimeInfo{
		Mode:          s.cfg.Mode,
		HealthAddr:    s.cfg.Health.ListenAddr,
		BroadcastAddr: s.cfg.Broadcast.Listen,
		Capabilities: []string{
			"mDNS discovery of smoke-alarm services",
			"Real-time alert signal with 30-minute decay",
			"Channel visualization via Television",
			"Interactive caller line",
			"Feature/skill certification browsing over adhd's MCP endpoint",
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
