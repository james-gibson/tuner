package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/james-gibson/tuner/internal/config"
	"github.com/james-gibson/tuner/internal/llms"
)

// Server handles HTTP endpoints for Tuner.
type Server struct {
	cfg     *config.Config
	version string
	startAt time.Time

	mu       sync.RWMutex
	audience map[string]AudienceMetric
}

// AudienceMetric holds broadcaster-reported audience data.
type AudienceMetric struct {
	Channel  string         `json:"channel"`
	Count    int            `json:"count"`
	Signal   float64        `json:"signal"`
	Metadata map[string]any `json:"metadata,omitempty"`
	UpdatedAt time.Time     `json:"updated_at"`
}

// Options configures a new Server.
type Options struct {
	Config  *config.Config
	Version string
}

// New creates a new Server.
func New(opts Options) *Server {
	return &Server{
		cfg:      opts.Config,
		version:  opts.Version,
		startAt:  time.Now(),
		audience: make(map[string]AudienceMetric),
	}
}

// HandleHealthz returns 200 if alive.
func (s *Server) HandleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"time":   time.Now().UTC().Format(time.RFC3339Nano),
	})
}

// HandleReadyz returns 200 if ready to serve.
func (s *Server) HandleReadyz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
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

	writeJSON(w, http.StatusOK, map[string]any{
		"service":  "tuner",
		"version":  s.version,
		"mode":     s.cfg.Mode,
		"uptime":   time.Since(s.startAt).String(),
		"channels": presetCount + customCount,
		"audience": audienceCount,
	})
}

// HandleListChannels lists available channels.
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
	writeJSON(w, http.StatusOK, map[string]any{"channels": channels})
}

// HandleChannel routes channel-specific requests.
func (s *Server) HandleChannel(w http.ResponseWriter, r *http.Request) {
	// /channels/{name}/...
	path := strings.TrimPrefix(r.URL.Path, "/channels/")
	parts := strings.SplitN(path, "/", 2)
	name := parts[0]

	if len(parts) == 2 {
		switch parts[1] {
		case "sse":
			s.handleChannelSSE(w, r, name)
			return
		case "caller":
			s.handleCaller(w, r, name)
			return
		case "audience":
			s.handleAudience(w, r, name)
			return
		}
	}

	// Default: return channel info.
	writeJSON(w, http.StatusOK, map[string]any{
		"channel": name,
		"status":  "available",
	})
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
	w.Write([]byte(body))
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

	// Send initial event.
	fmt.Fprintf(w, "event: connected\ndata: {\"channel\":%q}\n\n", channel)
	flusher.Flush()

	// TODO: stream real channel data from presets/sources.
	// For now, send a heartbeat until client disconnects.
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case t := <-ticker.C:
			fmt.Fprintf(w, "event: heartbeat\ndata: {\"time\":%q}\n\n", t.UTC().Format(time.RFC3339Nano))
			flusher.Flush()
		}
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
	writeJSON(w, http.StatusOK, map[string]any{
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

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
