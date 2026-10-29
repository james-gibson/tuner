package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/james-gibson/tuner/internal/channelsource"
	"github.com/james-gibson/tuner/internal/config"
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
		channels = append(channels, map[string]any{
			"name":   c.Name,
			"type":   "custom",
			"source": c.Source.Type,
		})
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

	switch action {
	case "sse":
		s.handleChannelSSE(w, r, name)
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
	for _, suffix := range []string{"/sse", "/status", "/caller", "/audience"} {
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
		channels = append(channels, llms.ChannelInfo{
			Name:        c.Name,
			Description: fmt.Sprintf("custom %s channel", c.Source.Type),
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
