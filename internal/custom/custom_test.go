package custom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/james-gibson/tuner/internal/config"
)

func declared(name, kind string) config.CustomChannel {
	return config.CustomChannel{Name: name, Source: config.SourceConfig{Type: kind}}
}

func commandChannel(name, command string) config.CustomChannel {
	c := declared(name, config.SourceCommand)
	c.Source.Command = command
	return c
}

func urlChannel(name, kind, url string) config.CustomChannel {
	c := declared(name, kind)
	c.Source.URL = url
	return c
}

// TestCommandSnapshotParsesJSON guards the pull shape: a command channel serves
// what the command wrote, as data when it is JSON.
func TestCommandSnapshotParsesJSON(t *testing.T) {
	set := New([]config.CustomChannel{
		commandChannel("state", `echo '{"state":"ok","count":2}'`),
	})

	snap, err := set.Snapshot(context.Background(), "state")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snap.Channel != "state" || snap.Type != config.SourceCommand {
		t.Fatalf("unexpected snapshot header: %+v", snap)
	}
	var got struct {
		State string `json:"state"`
		Count int    `json:"count"`
	}
	if err := json.Unmarshal(snap.Data, &got); err != nil {
		t.Fatalf("data is not JSON: %v (%s)", err, snap.Data)
	}
	if got.State != "ok" || got.Count != 2 {
		t.Fatalf("unexpected data: %+v", got)
	}
	if snap.Fetched.IsZero() {
		t.Fatal("snapshot has no fetch time")
	}
}

func TestCommandSnapshotKeepsPlainTextAsLines(t *testing.T) {
	set := New([]config.CustomChannel{
		commandChannel("text", `printf 'alpha\nbeta\n'`),
	})

	snap, err := set.Snapshot(context.Background(), "text")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(snap.Lines) != 2 || snap.Lines[0] != "alpha" || snap.Lines[1] != "beta" {
		t.Fatalf("unexpected lines: %q", snap.Lines)
	}
	if snap.Data != nil {
		t.Fatalf("non-JSON output should not become data: %s", snap.Data)
	}
}

// TestCommandSnapshotReportsStderr keeps a failing command's own message with
// the error: "exit status 1" alone is not a diagnosis.
func TestCommandSnapshotReportsStderr(t *testing.T) {
	set := New([]config.CustomChannel{
		commandChannel("broken", `echo 'disk on fire' >&2; exit 3`),
	})

	_, err := set.Snapshot(context.Background(), "broken")
	if err == nil {
		t.Fatal("a failing command must not produce a snapshot")
	}
	if !strings.Contains(err.Error(), "disk on fire") {
		t.Fatalf("error lost the command's stderr: %v", err)
	}
}

// TestHTTPSnapshotIsCachedForRefresh is the reason the cache exists: a watching
// client must not be able to hammer a source through tuner.
func TestHTTPSnapshotIsCachedForRefresh(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"up":true}`))
	}))
	defer srv.Close()

	c := urlChannel("feed", config.SourceHTTP, srv.URL)
	c.Refresh = "1h"
	set := New([]config.CustomChannel{c})

	for i := 0; i < 3; i++ {
		if _, err := set.Snapshot(context.Background(), "feed"); err != nil {
			t.Fatalf("snapshot %d: %v", i, err)
		}
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("source was fetched %d times inside its refresh window, want 1", got)
	}
}

func TestHTTPSnapshotSendsBearerToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer s3cret" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := urlChannel("private", config.SourceHTTP, srv.URL)
	c.Auth = &config.AuthConfig{Type: "bearer", Token: "s3cret"}

	if _, err := New([]config.CustomChannel{c}).Snapshot(context.Background(), "private"); err != nil {
		t.Fatalf("snapshot with bearer token: %v", err)
	}
}

func TestHTTPSnapshotRejectsBadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := New([]config.CustomChannel{urlChannel("down", config.SourceHTTP, srv.URL)}).
		Snapshot(context.Background(), "down")
	if err == nil {
		t.Fatal("a 500 from the source must not serve a snapshot")
	}
}

// TestSSESnapshotTakesFirstEvent covers the sse type: tuner reconnects per
// refresh and keeps the first event, skipping comment keep-alives.
func TestSSESnapshotTakesFirstEvent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept"); got != "text/event-stream" {
			http.Error(w, "want an event stream", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		_, _ = fmt.Fprint(w, ": keep-alive\n\n")
		_, _ = fmt.Fprint(w, "event: snapshot\ndata: {\"level\":\ndata: 7}\n\n")
		flusher.Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()

	snap, err := New([]config.CustomChannel{urlChannel("stream", config.SourceSSE, srv.URL)}).
		Snapshot(context.Background(), "stream")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	var got map[string]int
	if err := json.Unmarshal(snap.Data, &got); err != nil {
		t.Fatalf("joined data lines are not JSON: %v (%s)", err, snap.Data)
	}
	if got["level"] != 7 {
		t.Fatalf("unexpected data: %v", got)
	}
}

func TestSSESnapshotWithoutEventIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, ": keep-alive\n\n")
	}))
	defer srv.Close()

	_, err := New([]config.CustomChannel{urlChannel("quiet", config.SourceSSE, srv.URL)}).
		Snapshot(context.Background(), "quiet")
	if err == nil {
		t.Fatal("a stream with no event must not serve a snapshot")
	}
}

// TestWebhookWaitsThenServes covers the push direction end to end: before an
// ingest the channel says so, after one it serves the pushed value.
func TestWebhookWaitsThenServes(t *testing.T) {
	set := New([]config.CustomChannel{declared("push", config.SourceWebhook)})

	if _, err := set.Snapshot(context.Background(), "push"); !errors.Is(err, ErrNoData) {
		t.Fatalf("want ErrNoData before the first push, got %v", err)
	}

	snap, err := set.Ingest("push", []byte(`{"alarm":"ringing"}`))
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if !strings.Contains(string(snap.Data), "ringing") {
		t.Fatalf("ingest returned the wrong value: %s", snap.Data)
	}

	again, err := set.Snapshot(context.Background(), "push")
	if err != nil {
		t.Fatalf("snapshot after ingest: %v", err)
	}
	if !strings.Contains(string(again.Data), "ringing") {
		t.Fatalf("pushed value was not kept: %s", again.Data)
	}
}

func TestIngestOnPullChannelIsRefused(t *testing.T) {
	c := urlChannel("poll", config.SourceHTTP, "http://127.0.0.1:1/")
	_, err := New([]config.CustomChannel{c}).Ingest("poll", []byte(`{}`))
	if !errors.Is(err, ErrNotWebhook) {
		t.Fatalf("want ErrNotWebhook, got %v", err)
	}
}

func TestIngestOverLimitIsRefused(t *testing.T) {
	set := New([]config.CustomChannel{declared("push", config.SourceWebhook)})
	_, err := set.Ingest("push", []byte(strings.Repeat("x", MaxBody+1)))
	if err == nil {
		t.Fatal("an over-size push must be refused")
	}
}

func TestSnapshotUnknownChannel(t *testing.T) {
	if _, err := New(nil).Snapshot(context.Background(), "ghost"); err == nil {
		t.Fatal("an unknown channel must not snapshot")
	}
}

// TestMisconfiguredChannelReportsItsProblem is the tuner#30 invariant at the
// package level: a declared channel answers, and when it cannot it says why
// instead of looking merely "available".
func TestMisconfiguredChannelReportsItsProblem(t *testing.T) {
	set := New([]config.CustomChannel{declared("typo", "htp")})

	_, err := set.Snapshot(context.Background(), "typo")
	if err == nil {
		t.Fatal("a channel with an unknown source type must not serve")
	}
	if !strings.Contains(err.Error(), "unknown source type") {
		t.Fatalf("error does not name the problem: %v", err)
	}
	if _, ok := set.Info("typo"); !ok {
		t.Fatal("a misconfigured channel is still a listed channel")
	}
}

func TestValidate(t *testing.T) {
	bad := []config.CustomChannel{
		{Name: "", Source: config.SourceConfig{Type: config.SourceWebhook}},
		commandChannel("no-command", ""),
		urlChannel("not-http", config.SourceHTTP, "file:///etc/passwd"),
		{Name: "no-type"},
		{Name: "two", Source: config.SourceConfig{Type: config.SourceWebhook}},
		{Name: "two", Source: config.SourceConfig{Type: config.SourceWebhook}},
	}

	problems := Validate(bad)
	if len(problems) != 5 {
		t.Fatalf("want 5 problems, got %d: %v", len(problems), problems)
	}
	if len(Validate(nil)) != 0 {
		t.Fatal("no channels is not a problem")
	}
}

func TestValidateRejectsBadRefresh(t *testing.T) {
	c := declared("slow", config.SourceWebhook)
	c.Refresh = "soon"
	if problems := Validate([]config.CustomChannel{c}); len(problems) != 1 {
		t.Fatalf("want 1 problem for refresh %q, got %v", c.Refresh, problems)
	}
}

// TestExposureProblems covers the notice behind the ingest guard: a tokenless
// webhook channel on a listener the LAN can reach will refuse remote pushes, and
// the operator is told at startup.
func TestExposureProblems(t *testing.T) {
	webhook := declared("push", config.SourceWebhook)
	poll := urlChannel("poll", config.SourceHTTP, "http://127.0.0.1:1/")

	cases := []struct {
		name    string
		chans   []config.CustomChannel
		listen  string
		want    int
		because string
	}{
		{"loopback listener", []config.CustomChannel{webhook}, "127.0.0.1:58093", 0,
			"ingest is loopback-only anyway"},
		{"all interfaces", []config.CustomChannel{webhook}, "0.0.0.0:58093", 1,
			"a LAN-reachable listener with a tokenless ingest"},
		{"empty host", []config.CustomChannel{webhook}, ":58093", 1,
			"an empty host binds every interface"},
		{"tokenless pull channel", []config.CustomChannel{poll}, "0.0.0.0:58093", 0,
			"only webhook channels take pushes"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExposureProblems(tc.chans, tc.listen)
			if len(got) != tc.want {
				t.Fatalf("%d notices, want %d (%s): %v", len(got), tc.want, tc.because, got)
			}
		})
	}

	withToken := declared("push", config.SourceWebhook)
	withToken.Auth = &config.AuthConfig{Type: "bearer", Token: "s3cret"}
	if got := ExposureProblems([]config.CustomChannel{withToken}, "0.0.0.0:58093"); len(got) != 0 {
		t.Fatalf("a token makes a reachable ingest legitimate, got %v", got)
	}
}

func TestRefreshFallsBackToDefault(t *testing.T) {
	c := urlChannel("poll", config.SourceHTTP, "http://127.0.0.1:1/")
	for _, refresh := range []string{"", "nonsense", "-5s"} {
		c.Refresh = refresh
		if got := New([]config.CustomChannel{c}).channels["poll"].info.Refresh; got != DefaultRefresh {
			t.Fatalf("refresh %q gave %v, want %v", refresh, got, DefaultRefresh)
		}
	}
}

func TestSnapshotChangeIsVisibleAfterRefresh(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := hits.Add(1)
		_, _ = fmt.Fprintf(w, `{"hit":%d}`, n)
	}))
	defer srv.Close()

	c := urlChannel("feed", config.SourceHTTP, srv.URL)
	c.Refresh = "1ns"
	set := New([]config.CustomChannel{c})

	first, err := set.Snapshot(context.Background(), "feed")
	if err != nil {
		t.Fatalf("first snapshot: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	second, err := set.Snapshot(context.Background(), "feed")
	if err != nil {
		t.Fatalf("second snapshot: %v", err)
	}
	if string(first.Data) == string(second.Data) {
		t.Fatalf("a cached snapshot outlived its refresh: %s", second.Data)
	}
}

func TestIngestEmptyIsRefused(t *testing.T) {
	set := New([]config.CustomChannel{declared("push", config.SourceWebhook)})
	if _, err := set.Ingest("push", []byte("  \n")); err == nil {
		t.Fatal("an empty push must be refused, not stored as an empty snapshot")
	}
}
