package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/james-gibson/tuner/internal/config"
)

// The caller line is the rendezvous: a viewer posts once, is told the address
// the broadcaster observed for it, and any subscriber of that channel hears the
// message. These tests pin those three claims separately.

func callerServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := newTestServer()
	mux := http.NewServeMux()
	mux.HandleFunc("/channels/", srv.HandleChannel)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func postCaller(t *testing.T, base, channel, body string) map[string]any {
	t.Helper()
	response, err := http.Post(
		base+"/channels/"+channel+"/caller",
		"application/json",
		strings.NewReader(body),
	)
	if err != nil {
		t.Fatalf("post caller: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.StatusCode)
	}
	var payload map[string]any
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode ack: %v", err)
	}
	return payload
}

// sseReader reads one SSE connection and reports events by name as they arrive.
// One scanner per connection: a scanner reads ahead, so creating a second one
// for each event would drop whatever the first had already buffered.
type sseReader struct {
	cancel  context.CancelFunc
	body    io.ReadCloser
	scanner *bufio.Scanner
	current string
}

func openSSE(t *testing.T, base, channel string) *sseReader {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		base+"/channels/"+channel+"/sse",
		http.NoBody,
	)
	if err != nil {
		cancel()
		t.Fatalf("build sse request: %v", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		cancel()
		t.Fatalf("open sse: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		_ = response.Body.Close()
	})
	return &sseReader{cancel: cancel, body: response.Body, scanner: bufio.NewScanner(response.Body)}
}

func (r *sseReader) await(t *testing.T, event string, timeout time.Duration) string {
	t.Helper()

	// Closing the body is the only way to unblock a blocked Scan, so the
	// timeout is enforced by the read failing.
	timer := time.AfterFunc(timeout, func() { _ = r.body.Close() })
	defer timer.Stop()

	for r.scanner.Scan() {
		line := r.scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			r.current = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: ") && r.current == event:
			return strings.TrimPrefix(line, "data: ")
		}
	}
	t.Fatalf("timed out waiting for %q: %v", event, r.scanner.Err())
	return ""
}

func TestCallerAckNamesTheObservedAddress(t *testing.T) {
	server := callerServer(t)

	response, err := http.Post(
		server.URL+"/channels/ntp/caller",
		"application/json",
		strings.NewReader(`{"message":"who am i"}`),
	)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer func() { _ = response.Body.Close() }()

	var ack map[string]any
	if err := json.NewDecoder(response.Body).Decode(&ack); err != nil {
		t.Fatalf("decode: %v", err)
	}
	seen, _ := ack["seen"].(string)
	if seen == "" {
		t.Fatal("the acknowledgement did not name the observed address")
	}
	if !strings.HasPrefix(seen, "127.0.0.1:") {
		t.Fatalf("expected a loopback address for a local test, got %q", seen)
	}
}

func TestCallerReachesASubscriberThatWasAlreadyListening(t *testing.T) {
	server := callerServer(t)
	stream := openSSE(t, server.URL, "ntp")

	// The connected event proves the subscription exists before the post.
	stream.await(t, "connected", 5*time.Second)
	postCaller(t, server.URL, "ntp", `{"message":"relay me","from":"viewer1"}`)

	payload := stream.await(t, "caller", 5*time.Second)
	var message CallerMessage
	if err := json.Unmarshal([]byte(payload), &message); err != nil {
		t.Fatalf("decode caller event: %v", err)
	}
	if message.Message != "relay me" || message.From != "viewer1" {
		t.Fatalf("unexpected message: %#v", message)
	}
	if message.Seen == "" {
		t.Fatal("the relayed message did not carry the observed address")
	}
}

func TestCallerReachesASubscriberThatConnectsLater(t *testing.T) {
	server := callerServer(t)

	// Post first: a viewer may not know how to subscribe yet, and the point of
	// the line is that the broadcaster can tell it where it was seen.
	postCaller(t, server.URL, "ntp", `{"message":"late join"}`)

	stream := openSSE(t, server.URL, "ntp")
	stream.await(t, "connected", 5*time.Second)

	payload := stream.await(t, "caller", 5*time.Second)
	var message CallerMessage
	if err := json.Unmarshal([]byte(payload), &message); err != nil {
		t.Fatalf("decode caller event: %v", err)
	}
	if message.Message != "late join" {
		t.Fatalf("history did not replay the message: %#v", message)
	}
	if message.Seen == "" {
		t.Fatal("the replayed message did not carry the observed address")
	}
}

func TestCallerMessagesDoNotCrossChannels(t *testing.T) {
	server := callerServer(t)
	stream := openSSE(t, server.URL, "dns")
	stream.await(t, "connected", 5*time.Second)

	postCaller(t, server.URL, "ntp", `{"message":"for ntp only"}`)

	// Posting on another channel must not reach this subscriber. Prove the
	// stream is alive by posting on its own channel afterwards.
	postCaller(t, server.URL, "dns", `{"message":"for dns"}`)
	payload := stream.await(t, "caller", 5*time.Second)

	var message CallerMessage
	if err := json.Unmarshal([]byte(payload), &message); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if message.Message != "for dns" {
		t.Fatalf("expected the dns message, got %#v", message)
	}
}

func TestCallerRefusesWhenDisabled(t *testing.T) {
	srv := newTestServer()
	srv.cfg = config.Defaults()
	srv.cfg.Broadcast.CallerEnabled = false

	req := httptest.NewRequest(
		"POST",
		"/channels/ntp/caller",
		strings.NewReader(`{"message":"should not pass"}`),
	)
	w := httptest.NewRecorder()
	srv.HandleChannel(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when the caller line is disabled, got %d", w.Code)
	}
}

func TestCallerHistoryIsBounded(t *testing.T) {
	srv := newTestServer()
	for i := 0; i < callerHistory+5; i++ {
		srv.publish(CallerMessage{Channel: "ntp", Message: "m", Seen: "127.0.0.1:1"})
	}
	if got := len(srv.historyFor("ntp")); got != callerHistory {
		t.Fatalf("history kept %d messages, want %d", got, callerHistory)
	}
}
