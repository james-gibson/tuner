package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/james-gibson/tuner/internal/channelsource"
	"github.com/james-gibson/tuner/internal/config"
	"github.com/james-gibson/tuner/internal/featurebrowser"
	"github.com/james-gibson/tuner/internal/mdns"
)

// fakeMCP serves the JSON-RPC methods the feature browser calls. The
// certification tools are declared absent in the lab, so a served contract
// exists only as a fixture here.
type fakeMCP struct {
	tools   []string
	results map[string]json.RawMessage
}

func newFakeMCP(t *testing.T, fake fakeMCP) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{"protocolVersion": "2024-11-05", "serverInfo": map[string]any{"name": "adhd"}}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			tools := make([]map[string]any, 0, len(fake.tools))
			for _, name := range fake.tools {
				tools = append(tools, map[string]any{"name": name})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{"tools": tools}})
		case "tools/call":
			raw, ok := fake.results[req.Params.Name]
			if !ok {
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID,
					"error": map[string]any{"code": -32601, "message": "method not found"}})
				return
			}
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":%s}`, req.ID, raw)
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID,
				"error": map[string]any{"code": -32601, "message": "method not found"}})
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// servedFeatureServer is a server whose fallback endpoint answers the
// certification contract with one certified and one uncertified feature.
func servedFeatureServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	fake := newFakeMCP(t, fakeMCP{
		tools: []string{featurebrowser.ToolFeaturesCertified},
		results: map[string]json.RawMessage{
			featurebrowser.ToolFeaturesCertified: json.RawMessage(`{"caller_rung":3,"features":[
				{"feature_file":"adhd/mdns-discovery","certified":true,"cluster_member":"smoke:alarm-a/t1"},
				{"feature_file":"adhd/headless-mode","certified":false}]}`),
		},
	})
	cfg := config.Defaults()
	cfg.FeatureBrowser.Endpoints = []config.FeatureEndpoint{{Name: "adhd", URL: fake.URL}}
	return New(Options{Config: cfg, Version: "test"}), fake
}

func TestAdhdFeaturesChannelServesMergedView(t *testing.T) {
	srv, _ := servedFeatureServer(t)

	req := httptest.NewRequest("GET", "/channels/"+featurebrowser.ChannelName, http.NoBody)
	w := httptest.NewRecorder()
	srv.HandleChannel(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body)
	}
	var resp featureChannelResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	if resp.Channel != featurebrowser.ChannelName || resp.Type != "adhd-features" {
		t.Fatalf("unexpected channel identity: %+v", resp)
	}
	if resp.Status != featurebrowser.StatusServed {
		t.Fatalf("status = %q, want served", resp.Status)
	}
	if resp.Rung != 3 {
		t.Fatalf("rung = %d, want 3 (adhd's assignment, read back)", resp.Rung)
	}
	if len(resp.Features) != 2 || resp.Summary.FeaturesCertified != 1 {
		t.Fatalf("unexpected view: %+v", resp)
	}
}

func TestAdhdFeaturesChannelPublishesDeclaredAbsent(t *testing.T) {
	fake := newFakeMCP(t, fakeMCP{tools: []string{"adhd.ping"}})
	cfg := config.Defaults()
	cfg.FeatureBrowser.Endpoints = []config.FeatureEndpoint{{Name: "adhd", URL: fake.URL}}
	srv := New(Options{Config: cfg, Version: "test"})

	w := httptest.NewRecorder()
	srv.HandleChannel(w, httptest.NewRequest("GET", "/channels/"+featurebrowser.ChannelName, http.NoBody))

	if w.Code != http.StatusOK {
		t.Fatalf("a listed channel that cannot serve must still answer, got %d", w.Code)
	}
	var resp featureChannelResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	if resp.Status != featurebrowser.StatusDeclaredAbsent {
		t.Fatalf("status = %q, want declared-absent", resp.Status)
	}
	if !strings.Contains(resp.Note, "declared but not served") {
		t.Fatalf("note does not state the absence: %q", resp.Note)
	}
}

func TestAdhdFeaturesChannelWithNoSources(t *testing.T) {
	srv := New(Options{Config: config.Defaults(), Version: "test"})

	w := httptest.NewRecorder()
	srv.HandleChannel(w, httptest.NewRequest("GET", "/channels/"+featurebrowser.ChannelName, http.NoBody))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp featureChannelResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	if resp.Status != featurebrowser.StatusNoSources {
		t.Fatalf("status = %q, want no-sources", resp.Status)
	}
}

func TestAdhdFeaturesChannelUsesDiscoveredIsotopes(t *testing.T) {
	fake := newFakeMCP(t, fakeMCP{
		tools: []string{featurebrowser.ToolFeaturesCertified},
		results: map[string]json.RawMessage{
			featurebrowser.ToolFeaturesCertified: json.RawMessage(`{"features":[{"feature_file":"adhd/mdns-discovery","certified":true}]}`),
		},
	})
	u, err := url.Parse(fake.URL)
	if err != nil {
		t.Fatalf("parse fake url: %v", err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}

	cfg := config.Defaults() // no configured fallback: discovery must supply it
	srv := New(Options{
		Config:   cfg,
		Version:  "test",
		Channels: channelsource.NewRegistry(),
		Isotopes: func() []mdns.Endpoint {
			return []mdns.Endpoint{{
				Host:       u.Hostname(),
				Port:       port,
				Identities: []mdns.Identity{{Role: "adhd", TrustRung: 3}},
				Services: []mdns.Service{{
					Name:        "adhd-tui._adhd-isotope._tcp.local.",
					ServiceType: "_adhd-isotope._tcp",
					Host:        u.Hostname(),
					Port:        port,
				}},
			}}
		},
	})

	w := httptest.NewRecorder()
	srv.HandleChannel(w, httptest.NewRequest("GET", "/channels/"+featurebrowser.ChannelName, http.NoBody))

	var resp featureChannelResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	if resp.Status != featurebrowser.StatusServed {
		t.Fatalf("status = %q, want served from the discovered isotope endpoint", resp.Status)
	}
	if len(resp.Sources) != 1 || resp.Sources[0].Instance != "adhd-tui" || resp.Sources[0].Origin != "mdns" {
		t.Fatalf("discovered source not used: %+v", resp.Sources)
	}
	// The isotope identity is adhd's, carried through to the view.
	if resp.Sources[0].Role != "adhd" || resp.Sources[0].TrustRung != 3 {
		t.Fatalf("advertised identity lost: %+v", resp.Sources[0])
	}
}

func TestAdhdFeaturesChannelListed(t *testing.T) {
	srv := New(Options{Config: config.Defaults(), Version: "test"})

	w := httptest.NewRecorder()
	srv.HandleListChannels(w, httptest.NewRequest("GET", "/channels", http.NoBody))

	var resp struct {
		Channels []map[string]any `json:"channels"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	for _, c := range resp.Channels {
		if c["name"] == featurebrowser.ChannelName && c["type"] == "adhd-features" {
			return
		}
	}
	t.Fatalf("adhd-features not listed: %+v", resp.Channels)
}

func TestLLMSTxtAdvertisesAdhdFeatures(t *testing.T) {
	srv := New(Options{Config: config.Defaults(), Version: "test"})

	w := httptest.NewRecorder()
	srv.HandleLLMSTxt(w, httptest.NewRequest("GET", "/llms.txt", http.NoBody))

	if !strings.Contains(w.Body.String(), featurebrowser.ChannelName) {
		t.Fatalf("llms.txt does not advertise the certification channel:\n%s", w.Body)
	}
}

// sseRecorder is a concurrency-safe ResponseWriter for the streaming handler:
// httptest.ResponseRecorder is not safe to read while the handler writes.
type sseRecorder struct {
	mu      sync.Mutex
	body    bytes.Buffer
	header  http.Header
	written chan struct{}
}

func newSSERecorder() *sseRecorder {
	return &sseRecorder{header: http.Header{}, written: make(chan struct{}, 1)}
}

func (r *sseRecorder) Header() http.Header { return r.header }

func (r *sseRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, err := r.body.Write(p)
	select {
	case r.written <- struct{}{}:
	default:
	}
	return n, err
}

func (r *sseRecorder) WriteHeader(int) {}

func (r *sseRecorder) Flush() {}

func (r *sseRecorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.body.String()
}

func TestAdhdFeaturesSSEEmitsFirstView(t *testing.T) {
	srv, _ := servedFeatureServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest("GET", "/channels/"+featurebrowser.ChannelName+"/sse", http.NoBody).WithContext(ctx)
	w := newSSERecorder()

	done := make(chan struct{})
	go func() {
		srv.HandleChannel(w, req)
		close(done)
	}()

	select {
	case <-w.written:
	case <-time.After(3 * time.Second):
		t.Fatal("stream produced no first event")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not stop when the request context was canceled")
	}

	body := w.String()
	if !strings.Contains(body, "event: view") {
		t.Fatalf("stream does not label the event:\n%s", body)
	}
	if !strings.Contains(body, `"status":"served"`) {
		t.Fatalf("first event does not carry the merged view:\n%s", body)
	}
	if !strings.Contains(body, `"channel":"adhd-features"`) {
		t.Fatalf("first event does not carry the channel identity:\n%s", body)
	}
}

func TestFeatureViewKeyIgnoresFetched(t *testing.T) {
	a := featurebrowser.View{
		Status:  featurebrowser.StatusServed,
		Fetched: time.Now(),
		Sources: []featurebrowser.Source{{Instance: "adhd", Endpoint: "http://x/mcp", Fetched: time.Now()}},
	}
	b := featurebrowser.View{
		Status:  featurebrowser.StatusServed,
		Fetched: time.Now().Add(time.Minute),
		Sources: []featurebrowser.Source{{Instance: "adhd", Endpoint: "http://x/mcp", Fetched: time.Now().Add(time.Minute)}},
	}
	if featureViewKey(a) != featureViewKey(b) {
		t.Fatal("a re-poll with unchanged state counts as an update")
	}
	c := featurebrowser.View{
		Status:  featurebrowser.StatusDeclaredAbsent,
		Fetched: a.Fetched,
		Sources: a.Sources,
	}
	if featureViewKey(a) == featureViewKey(c) {
		t.Fatal("a status change does not count as an update")
	}
	d := a
	d.Sources = []featurebrowser.Source{{Instance: "adhd", Endpoint: "http://x/mcp", State: featurebrowser.Unreachable}}
	if featureViewKey(a) == featureViewKey(d) {
		t.Fatal("a source state change does not count as an update")
	}

	// The key must not mutate its input: the sources slice is shared with the
	// browser's cache, which another request may be serializing.
	stamp := time.Now()
	input := featurebrowser.View{Sources: []featurebrowser.Source{{Instance: "adhd", Fetched: stamp}}}
	_ = featureViewKey(input)
	if !input.Sources[0].Fetched.Equal(stamp) {
		t.Fatal("featureViewKey cleared the caller's per-source fetch time")
	}
}
