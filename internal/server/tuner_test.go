package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/james-gibson/tuner/internal/config"
)

func newTestServer() *Server {
	cfg := config.Defaults()
	return New(Options{Config: cfg, Version: "test"})
}

func TestHandleHealthz(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/healthz", nil)
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
	req := httptest.NewRequest("GET", "/readyz", nil)
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
	req := httptest.NewRequest("GET", "/status", nil)
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
	req := httptest.NewRequest("GET", "/channels", nil)
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

func TestHandleChannelInfo(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/channels/ntp", nil)
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
	req := httptest.NewRequest("GET", "/channels/ntp/audience", nil)
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
	req := httptest.NewRequest("GET", "/channels/ntp/caller", nil)
	w := httptest.NewRecorder()
	srv.HandleChannel(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}

func TestHandleLLMSTxt(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/llms.txt", nil)
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
