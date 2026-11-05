package server

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/james-gibson/tuner/internal/channelsource"
	"github.com/james-gibson/tuner/internal/config"
	"github.com/james-gibson/tuner/internal/mdns"
	"github.com/james-gibson/tuner/internal/notes"
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
	if !ok || channels != 4 {
		t.Fatalf("expected 4 channels (3 presets + the browser), got %v", resp["channels"])
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
	// 3 presets + the generated certification browser channel.
	if len(channels) != 4 {
		t.Fatalf("expected 4 channels, got %d", len(channels))
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
	if len(resp.Channels) != 5 {
		t.Fatalf("expected 3 presets + 1 browser + 1 discovered channel, got %d", len(resp.Channels))
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
	if channels, ok := resp["channels"].(float64); !ok || channels != 5 {
		t.Fatalf("expected 5 channels (3 presets + 1 browser + 1 discovered), got %v", resp["channels"])
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

// newTestServerWithCustom returns a server whose config declares the given
// channels.custom entries.
func newTestServerWithCustom(chs ...config.CustomChannel) *Server {
	cfg := config.Defaults()
	cfg.Channels.Custom = chs
	return New(Options{Config: cfg, Version: "test"})
}

func customChannel(name, kind string) config.CustomChannel {
	return config.CustomChannel{Name: name, Source: config.SourceConfig{Type: kind}}
}

// TestCustomChannelServesItsSource is the tuner#30 regression: a listed custom
// channel used to answer "status: available" without ever reading its source.
func TestCustomChannelServesItsSource(t *testing.T) {
	c := customChannel("state", config.SourceCommand)
	c.Source.Command = `echo '{"state":"ok"}'`
	srv := newTestServerWithCustom(c)

	w := httptest.NewRecorder()
	srv.HandleChannel(w, httptest.NewRequest("GET", "/channels/state", http.NoBody))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body)
	}
	var resp struct {
		Channel string          `json:"channel"`
		Type    string          `json:"type"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not a snapshot: %v (%s)", err, w.Body)
	}
	if resp.Channel != "state" || resp.Type != config.SourceCommand {
		t.Fatalf("unexpected snapshot header: %+v", resp)
	}
	if !strings.Contains(string(resp.Data), `"ok"`) {
		t.Fatalf("snapshot does not carry the source's data: %s", resp.Data)
	}
}

// TestCustomChannelSourceErrorIsBadGateway keeps a dead source from reading as
// an empty channel: the failure is the answer.
func TestCustomChannelSourceErrorIsBadGateway(t *testing.T) {
	// Port 1 on loopback refuses connections.
	srv := newTestServerWithCustom(config.CustomChannel{
		Name:   "dead",
		Source: config.SourceConfig{Type: config.SourceHTTP, URL: "http://127.0.0.1:1/"},
	})

	w := httptest.NewRecorder()
	srv.HandleChannel(w, httptest.NewRequest("GET", "/channels/dead", http.NoBody))

	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), "error") {
		t.Fatalf("502 does not say what failed: %s", w.Body)
	}
}

func TestCustomChannelMisconfiguredReportsProblem(t *testing.T) {
	srv := newTestServerWithCustom(customChannel("typo", "htp"))

	w := httptest.NewRecorder()
	srv.HandleChannel(w, httptest.NewRequest("GET", "/channels/typo", http.NoBody))

	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), "unknown source type") {
		t.Fatalf("502 does not name the problem: %s", w.Body)
	}
}

// TestCustomWebhookIngestAndServe covers the push direction: before the first
// push the channel says it is waiting, after one it serves the value.
func TestCustomWebhookIngestAndServe(t *testing.T) {
	srv := newTestServerWithCustom(customChannel("push", config.SourceWebhook))

	w := httptest.NewRecorder()
	srv.HandleChannel(w, httptest.NewRequest("GET", "/channels/push", http.NoBody))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "waiting") {
		t.Fatalf("a channel nothing has pushed to should be waiting, got %d: %s", w.Code, w.Body)
	}

	req := httptest.NewRequest("POST", "/channels/push/ingest", strings.NewReader(`{"alarm":"ringing"}`))
	req.RemoteAddr = "127.0.0.1:54321"
	w = httptest.NewRecorder()
	srv.HandleChannel(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("loopback ingest: expected 200, got %d: %s", w.Code, w.Body)
	}

	w = httptest.NewRecorder()
	srv.HandleChannel(w, httptest.NewRequest("GET", "/channels/push", http.NoBody))
	if !strings.Contains(w.Body.String(), "ringing") {
		t.Fatalf("pushed value is not served: %s", w.Body)
	}
}

// TestCustomIngestRefusesRemoteWithoutToken is the unshippable-offense guard: no
// token, not this machine, no write. The refusal happens before the body is
// read, and the channel keeps whatever it had.
func TestCustomIngestRefusesRemoteWithoutToken(t *testing.T) {
	srv := newTestServerWithCustom(customChannel("push", config.SourceWebhook))

	req := httptest.NewRequest("POST", "/channels/push/ingest", strings.NewReader(`{"alarm":"ringing"}`))
	req.RemoteAddr = "203.0.113.5:41234" // a LAN peer, not loopback
	w := httptest.NewRecorder()
	srv.HandleChannel(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for a remote tokenless ingest, got %d: %s", w.Code, w.Body)
	}

	w = httptest.NewRecorder()
	srv.HandleChannel(w, httptest.NewRequest("GET", "/channels/push", http.NoBody))
	if strings.Contains(w.Body.String(), "ringing") {
		t.Fatalf("a refused push was stored anyway: %s", w.Body)
	}
}

func TestCustomIngestRequiresConfiguredToken(t *testing.T) {
	c := customChannel("push", config.SourceWebhook)
	c.Auth = &config.AuthConfig{Type: "bearer", Token: "s3cret"}
	srv := newTestServerWithCustom(c)

	send := func(header string) int {
		req := httptest.NewRequest("POST", "/channels/push/ingest", strings.NewReader(`{"alarm":"ringing"}`))
		req.RemoteAddr = "203.0.113.5:41234" // a token admits a remote peer
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		w := httptest.NewRecorder()
		srv.HandleChannel(w, req)
		return w.Code
	}

	if code := send(""); code != http.StatusUnauthorized {
		t.Fatalf("no token: expected 401, got %d", code)
	}
	if code := send("Bearer wrong"); code != http.StatusUnauthorized {
		t.Fatalf("wrong token: expected 401, got %d", code)
	}
	if code := send("Bearer s3cret"); code != http.StatusOK {
		t.Fatalf("right token: expected 200, got %d", code)
	}
}

func TestCustomIngestOnlyForWebhookChannels(t *testing.T) {
	c := customChannel("poll", config.SourceCommand)
	c.Source.Command = "echo hi"
	srv := newTestServerWithCustom(c)

	req := httptest.NewRequest("POST", "/channels/poll/ingest", strings.NewReader(`{}`))
	req.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	srv.HandleChannel(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for a pull channel ingest, got %d: %s", w.Code, w.Body)
	}
}

func TestCustomIngestRejectsGet(t *testing.T) {
	srv := newTestServerWithCustom(customChannel("push", config.SourceWebhook))

	req := httptest.NewRequest("GET", "/channels/push/ingest", http.NoBody)
	req.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	srv.HandleChannel(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
	if w.Header().Get("Allow") != http.MethodPost {
		t.Fatalf("expected Allow: POST, got %q", w.Header().Get("Allow"))
	}
}

// TestCustomChannelSSEStreamsSnapshot covers the pull-a-push-channel shape a
// watching client uses: the current value arrives as an event.
func TestCustomChannelSSEStreamsSnapshot(t *testing.T) {
	srv := newTestServerWithCustom(customChannel("push", config.SourceWebhook))
	if _, err := srv.custom.Ingest("push", []byte(`{"alarm":"ringing"}`)); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	ts := httptest.NewServer(http.HandlerFunc(srv.HandleChannel))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/channels/push/sse")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("expected an event stream, got %q", got)
	}

	reader := bufio.NewReader(resp.Body)
	var event, data string
	for i := 0; i < 10; i++ {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read event: %v", err)
		}
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "event: "); ok {
			event = v
		}
		if v, ok := strings.CutPrefix(line, "data: "); ok {
			data = v
		}
		if event != "" && data != "" {
			break
		}
	}
	if event != "snapshot" {
		t.Fatalf("expected a snapshot event, got %q", event)
	}
	if !strings.Contains(data, "ringing") {
		t.Fatalf("snapshot event does not carry the value: %s", data)
	}
}

// TestCustomChannelKeepsBroadcastActions: owning the data routes must not take
// the channel off the broadcast surface (audience, caller).
func TestCustomChannelKeepsBroadcastActions(t *testing.T) {
	srv := newTestServerWithCustom(customChannel("push", config.SourceWebhook))

	body := `{"count":3,"signal":0.5}`
	req := httptest.NewRequest("POST", "/channels/push/audience", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.HandleChannel(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body)
	}
}

// TestIngestOnPresetIsNotFound: only a configured webhook channel has an ingest
// route. A preset must not answer a push with the generic "available" reply.
func TestIngestOnPresetIsNotFound(t *testing.T) {
	srv := newTestServer()

	req := httptest.NewRequest("POST", "/channels/ntp/ingest", strings.NewReader(`{}`))
	req.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	srv.HandleChannel(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body)
	}
}

// TestListChannelsMarksMisconfiguredCustom: the discovery surface must not
// advertise a channel that cannot answer without saying so (tuner#30).
func TestListChannelsMarksMisconfiguredCustom(t *testing.T) {
	srv := newTestServerWithCustom(customChannel("typo", "htp"))

	w := httptest.NewRecorder()
	srv.HandleListChannels(w, httptest.NewRequest("GET", "/channels", http.NoBody))

	var resp struct {
		Channels []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
			Error  string `json:"error"`
		} `json:"channels"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	for _, c := range resp.Channels {
		if c.Name != "typo" {
			continue
		}
		if c.Status != "misconfigured" || !strings.Contains(c.Error, "unknown source type") {
			t.Fatalf("misconfigured channel listed as healthy: %+v", c)
		}
		return
	}
	t.Fatal("the custom channel is missing from /channels")
}

func TestLLMSTxtMarksMisconfiguredCustom(t *testing.T) {
	srv := newTestServerWithCustom(customChannel("typo", "htp"))

	w := httptest.NewRecorder()
	srv.HandleLLMSTxt(w, httptest.NewRequest("GET", "/llms.txt", http.NoBody))

	if !strings.Contains(w.Body.String(), "misconfigured") {
		t.Fatalf("llms.txt advertises an unservable channel as if it could answer:\n%s", w.Body)
	}
}

func TestNotesChannelItems(t *testing.T) {
	dir := t.TempDir()
	feedPath := filepath.Join(dir, "feed.json")
	body := `{"title":"prep","items":[{"id":"n-1","title":"is it frontmatter?","kind":"question"}]}`
	if err := os.WriteFile(feedPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write feed: %v", err)
	}

	cfg := config.Defaults()
	cfg.Channels.Notes = []config.NotesChannel{{Name: "prep", Source: feedPath, Refresh: "30s"}}
	srv := New(Options{Config: cfg, Version: "test"})

	req := httptest.NewRequest("GET", "/channels/prep", http.NoBody)
	w := httptest.NewRecorder()
	srv.HandleChannel(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp struct {
		Channel string       `json:"channel"`
		Type    string       `json:"type"`
		Items   []notes.Item `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	if resp.Type != "notes" || len(resp.Items) != 1 || resp.Items[0].ID != "n-1" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestNotesChannelListed(t *testing.T) {
	cfg := config.Defaults()
	cfg.Channels.Notes = []config.NotesChannel{{Name: "prep", Source: "x"}}
	srv := New(Options{Config: cfg, Version: "test"})

	req := httptest.NewRequest("GET", "/channels", http.NoBody)
	w := httptest.NewRecorder()
	srv.HandleListChannels(w, req)

	var resp struct {
		Channels []map[string]any `json:"channels"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	for _, c := range resp.Channels {
		if c["name"] == "prep" && c["type"] == "notes" {
			return
		}
	}
	t.Fatalf("notes channel not listed: %+v", resp.Channels)
}
