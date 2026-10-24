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

	mu          sync.RWMutex
	audience    map[string]AudienceMetric
	subscribers map[string]map[chan CallerMessage]struct{}
	history     map[string][]CallerMessage
}

// CallerMessage is one caller-line message as a subscriber receives it.
//
// Seen is the point of the line. A viewer does not have to know how the
// broadcaster reaches it: it posts once, and the acknowledgement (and every
// subscriber event) carries the address the broadcaster observed for it. That
// is the same question STUN asks — what do I look like from over there — with
// the broadcast channel as the rendezvous, and no extra protocol.
type CallerMessage struct {
	Channel  string    `json:"channel"`
	Message  string    `json:"message"`
	From     string    `json:"from,omitempty"`
	Priority string    `json:"priority,omitempty"`
	Seen     string    `json:"seen"`
	At       time.Time `json:"at"`
}

// callerHistory is how many caller messages a channel replays to a subscriber
// that connects after the message was posted. The caller line is a rendezvous,
// not a log: enough to cover a join racing a post, not enough to be an archive.
const callerHistory = 16

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
	Config  *config.Config
	Version string
}

// New creates a new Server.
func New(opts Options) *Server {
	return &Server{
		cfg:         opts.Config,
		version:     opts.Version,
		startAt:     time.Now(),
		audience:    make(map[string]AudienceMetric),
		subscribers: make(map[string]map[chan CallerMessage]struct{}),
		history:     make(map[string][]CallerMessage),
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

	writeJSON(w, map[string]any{
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
	writeJSON(w, map[string]any{"channels": channels})
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
	writeJSON(w, map[string]any{
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
	_, _ = w.Write([]byte(body))
}

func (s *Server) handleChannelSSE(w http.ResponseWriter, r *http.Request, channel string) {
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

	// A subscriber that arrives after a post still needs the address: replay
	// the channel's short history, then stream new messages live.
	events, unsubscribe := s.subscribe(channel)
	defer unsubscribe()

	for _, message := range s.historyFor(channel) {
		writeCallerEvent(w, flusher, message)
	}

	// Preset and custom channel data is not streamed yet; the heartbeat keeps
	// the connection observably alive until it is.
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case message := <-events:
			writeCallerEvent(w, flusher, message)
		case t := <-ticker.C:
			_, _ = fmt.Fprintf(w, "event: heartbeat\ndata: {\"time\":%q}\n\n", t.UTC().Format(time.RFC3339Nano))
			flusher.Flush()
		}
	}
}

func writeCallerEvent(w http.ResponseWriter, flusher http.Flusher, message CallerMessage) {
	data, err := json.Marshal(message)
	if err != nil {
		return
	}
	// json.Marshal escapes control characters, so the payload cannot break the
	// SSE framing.
	_, _ = fmt.Fprintf(w, "event: caller\ndata: %s\n\n", data)
	flusher.Flush()
}

// subscribe registers a channel subscriber. The returned cancel removes it.
func (s *Server) subscribe(channel string) (<-chan CallerMessage, func()) {
	events := make(chan CallerMessage, callerHistory)

	s.mu.Lock()
	if s.subscribers[channel] == nil {
		s.subscribers[channel] = make(map[chan CallerMessage]struct{})
	}
	s.subscribers[channel][events] = struct{}{}
	s.mu.Unlock()

	return events, func() {
		s.mu.Lock()
		if subscribers, ok := s.subscribers[channel]; ok {
			delete(subscribers, events)
			if len(subscribers) == 0 {
				delete(s.subscribers, channel)
			}
		}
		s.mu.Unlock()
		// publish holds the write lock while it sends, so closing under the
		// same lock cannot race a send. Sends are non-blocking, so holding the
		// lock through them cannot stall a caller.
		close(events)
	}
}

// publish fans a caller message out to current subscribers and remembers it
// briefly for subscribers that connect next.
func (s *Server) publish(message CallerMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()

	history := s.history[message.Channel]
	history = append(history, message)
	if len(history) > callerHistory {
		history = history[len(history)-callerHistory:]
	}
	s.history[message.Channel] = history

	for events := range s.subscribers[message.Channel] {
		select {
		case events <- message:
		default:
			// A subscriber that cannot keep up loses this message rather than
			// stalling the caller line for everybody else.
		}
	}
}

func (s *Server) historyFor(channel string) []CallerMessage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	history := s.history[channel]
	out := make([]CallerMessage, len(history))
	copy(out, history)
	return out
}

func (s *Server) handleCaller(w http.ResponseWriter, r *http.Request, channel string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.cfg.Broadcast.CallerEnabled {
		http.Error(w, "caller line disabled", http.StatusForbidden)
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

	message := CallerMessage{
		Channel:  channel,
		Message:  msg.Message,
		From:     msg.From,
		Priority: msg.Priority,
		Seen:     observedAddress(r),
		At:       time.Now().UTC(),
	}
	s.publish(message)

	writeJSON(w, map[string]any{
		"status":  "received",
		"channel": channel,
		// The viewer learns how the broadcaster reaches it. A viewer that
		// subscribed before posting already knows this; one that asked
		// without subscribing gets the same answer here.
		"seen": message.Seen,
	})
}

// observedAddress is the address the broadcaster sees the poster coming from.
// It is the socket's own view and nothing else: X-Forwarded-For is a claim by
// an intermediary and is not what a peer would have to dial, so it is ignored
// on purpose.
func observedAddress(r *http.Request) string {
	if r == nil {
		return ""
	}
	return r.RemoteAddr
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
