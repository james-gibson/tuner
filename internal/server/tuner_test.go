package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/james-gibson/tuner/internal/channelsource"
	"github.com/james-gibson/tuner/internal/config"
	"github.com/james-gibson/tuner/internal/mdns"
)

func newTestServer() *Server {
	cfg := config.Defaults()
	return New(Options{Config: cfg, Version: "test"})
}

// newTestServerWithSource returns a server and a registry holding one
// discovered smoke-alarm source pointing at endpoint.
func newTestServerWithSource(t *testing.T, instance, endpoint string) (*Server, *channelsource.Registry) {
	t.Helper()

	u, err := url.Parse(endpoint)
	if err != nil {
		t.Fatalf("parse endpoint: %v", err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}

	registry := channelsource.NewRegistry()
	registry.Observe(mdns.Service{
		Name:        instance + "._smoke-alarm._tcp.local.",
		ServiceType: mdns.SmokeAlarmServiceType,
		Host:        u.Hostname(),
		Port:        port,
	})

	return New(Options{Config: config.Defaults(), Version: "test", Channels: registry}), registry
}

func TestHandleHealthz(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/healthz", http.NoBody)
	w := httptest.NewRecorder()
	srv.HandleHealthz(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	if resp["status"] != "ok" {
		t.Fatalf("expected status ok, got %v", resp["status"])
	}
}

func TestHandleReadyz(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/readyz", http.NoBody)
	w := httptest.NewRecorder()
	srv.HandleReadyz(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["mode"] != "broadcast" {
		t.Fatalf("expected mode broadcast, got %v", resp["mode"])
	}
}

func TestHandleStatus(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/status", http.NoBody)
	w := httptest.NewRecorder()
	srv.HandleStatus(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["service"] != "tuner" {
		t.Fatalf("expected service tuner, got %v", resp["service"])
	}
	if resp["version"] != "test" {
		t.Fatalf("expected version test, got %v", resp["version"])
	}
	channels, ok := resp["channels"].(float64)
	if !ok || channels != 3 {
		t.Fatalf("expected 3 channels, got %v", resp["channels"])
	}
}

func TestHandleListChannels(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/channels", http.NoBody)
	w := httptest.NewRecorder()
	srv.HandleListChannels(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	channels, ok := resp["channels"].([]any)
	if !ok {
		t.Fatalf("expected channels array, got %T", resp["channels"])
	}
	if len(channels) != 3 {
		t.Fatalf("expected 3 channels, got %d", len(channels))
	}
}

func TestHandleListChannelsIncludesDiscovered(t *testing.T) {
	srv, _ := newTestServerWithSource(t, "smoke-alarm-late", "http://192.168.1.50:18088")

	w := httptest.NewRecorder()
	srv.HandleListChannels(w, httptest.NewRequest("GET", "/channels", http.NoBody))

	var resp struct {
		Channels []map[string]any `json:"channels"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	if len(resp.Channels) != 4 {
		t.Fatalf("expected 3 presets + 1 discovered channel, got %d", len(resp.Channels))
	}

	last := resp.Channels[len(resp.Channels)-1]
	if last["name"] != "smoke-alarm-late" || last["type"] != "discovered" {
		t.Fatalf("unexpected discovered channel entry: %v", last)
	}
	if last["endpoint"] != "http://192.168.1.50:18088" {
		t.Fatalf("unexpected endpoint: %v", last["endpoint"])
	}
	if last["status_url"] != "http://192.168.1.50:18088/status" {
		t.Fatalf("unexpected status_url: %v", last["status_url"])
	}
}

func TestHandleStatusCountsDiscovered(t *testing.T) {
	srv, _ := newTestServerWithSource(t, "smoke-alarm-late", "http://192.168.1.50:18088")

	w := httptest.NewRecorder()
	srv.HandleStatus(w, httptest.NewRequest("GET", "/status", http.NoBody))

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	if channels, ok := resp["channels"].(float64); !ok || channels != 4 {
		t.Fatalf("expected 4 channels (3 presets + 1 discovered), got %v", resp["channels"])
	}
}

func TestHandleChannelDiscoveredSource(t *testing.T) {
	srv, _ := newTestServerWithSource(t, "smoke-alarm-late", "http://192.168.1.50:18088")

	w := httptest.NewRecorder()
	srv.HandleChannel(w, httptest.NewRequest("GET", "/channels/smoke-alarm-late", http.NoBody))

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	if resp["type"] != "discovered" || resp["instance"] != "smoke-alarm-late" {
		t.Fatalf("expected a discovered channel, got %v", resp)
	}
	if resp["status_url"] != "http://192.168.1.50:18088/status" {
		t.Fatalf("unexpected status_url: %v", resp["status_url"])
	}
}

// Polling a discovered channel returns the instance's current health; the
// channel is not just a directory listing.
func TestHandleChannelDiscoveredStatusProxies(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"service":"ocd-smoke-alarm","ready":true}`))
	}))
	defer upstream.Close()

	srv, _ := newTestServerWithSource(t, "smoke-alarm-late", upstream.URL)

	w := httptest.NewRecorder()
	srv.HandleChannel(w, httptest.NewRequest("GET", "/channels/smoke-alarm-late/status", http.NoBody))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected JSON content type, got %q", ct)
	}
	var status map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatalf("json: %v", err)
	}
	if status["service"] != "ocd-smoke-alarm" {
		t.Fatalf("unexpected proxied status: %v", status)
	}
}

// A discovered instance keeps its DNS-SD label, which may contain a slash
// (lezz-demo seeds are named "cluster/alarm-a"). The channel must still be
// addressable at the name /channels listed.
func TestHandleChannelInstanceWithSlash(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"service":"ocd-smoke-alarm"}`))
	}))
	defer upstream.Close()

	srv, _ := newTestServerWithSource(t, "demo/alarm-a", upstream.URL)

	w := httptest.NewRecorder()
	srv.HandleChannel(w, httptest.NewRequest("GET", "/channels/demo/alarm-a", http.NoBody))
	var info map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil {
		t.Fatalf("json: %v", err)
	}
	if info["type"] != "discovered" || info["instance"] != "demo/alarm-a" {
		t.Fatalf("slash-named channel is unaddressable: %v", info)
	}

	w = httptest.NewRecorder()
	srv.HandleChannel(w, httptest.NewRequest("GET", "/channels/demo/alarm-a/status", http.NoBody))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 from the slash-named source, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleChannelStatusUnknown(t *testing.T) {
	srv := newTestServer()

	w := httptest.NewRecorder()
	srv.HandleChannel(w, httptest.NewRequest("GET", "/channels/nobody/status", http.NoBody))

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for an unknown discovered channel, got %d", w.Code)
	}
}

func TestHandleChannelInfo(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/channels/ntp", http.NoBody)
	w := httptest.NewRecorder()
	srv.HandleChannel(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["channel"] != "ntp" {
		t.Fatalf("expected channel ntp, got %v", resp["channel"])
	}
}

func TestHandleAudiencePost(t *testing.T) {
	srv := newTestServer()
	body := `{"channel":"ntp","count":5,"signal":0.75}`
	req := httptest.NewRequest("POST", "/channels/ntp/audience", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.HandleChannel(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	srv.mu.RLock()
	m, ok := srv.audience["ntp"]
	srv.mu.RUnlock()
	if !ok {
		t.Fatal("audience metric not stored")
	}
	if m.Count != 5 {
		t.Fatalf("expected count 5, got %d", m.Count)
	}
}

func TestHandleAudienceMethodNotAllowed(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/channels/ntp/audience", http.NoBody)
	w := httptest.NewRecorder()
	srv.HandleChannel(w, req)

	// GET on audience is not POST, should be 405
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}

func TestHandleCallerPost(t *testing.T) {
	srv := newTestServer()
	body := `{"message":"hello","from":"viewer1","priority":"normal"}`
	req := httptest.NewRequest("POST", "/channels/ntp/caller", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.HandleChannel(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["status"] != "received" {
		t.Fatalf("expected status received, got %v", resp["status"])
	}
}

func TestHandleCallerMethodNotAllowed(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/channels/ntp/caller", http.NoBody)
	w := httptest.NewRecorder()
	srv.HandleChannel(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}

func TestHandleLLMSTxt(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/llms.txt", http.NoBody)
	w := httptest.NewRecorder()
	srv.HandleLLMSTxt(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body, _ := io.ReadAll(w.Body)
	content := string(body)

	if !strings.Contains(content, "# Tuner") {
		t.Fatal("missing title")
	}
	if !strings.Contains(content, "## Capabilities") {
		t.Fatal("missing capabilities")
	}
	if !strings.Contains(content, "## Channels") {
		t.Fatal("missing channels")
	}
	if !strings.Contains(content, "**ntp**") {
		t.Fatal("missing ntp channel")
	}
	if w.Header().Get("Content-Type") != "text/markdown; charset=utf-8" {
		t.Fatalf("expected markdown content type, got %q", w.Header().Get("Content-Type"))
	}
}
