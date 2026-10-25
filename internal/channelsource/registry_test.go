package channelsource

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/james-gibson/tuner/internal/mdns"
)

func smokeAlarm(instance string, port int) mdns.Service {
	return mdns.Service{
		Name:        instance + "._smoke-alarm._tcp.local.",
		ServiceType: mdns.SmokeAlarmServiceType,
		Host:        "192.168.1.50",
		Port:        port,
	}
}

func TestObserveAddsSmokeAlarmSource(t *testing.T) {
	reg := NewRegistry()

	event, ok := reg.Observe(smokeAlarm("smoke-alarm-late", 18088))
	if !ok {
		t.Fatal("expected a smoke-alarm record to become a channel source")
	}
	if event.Kind != Added {
		t.Fatalf("event kind = %s, want added", event.Kind)
	}
	if event.Source.Instance != "smoke-alarm-late" {
		t.Fatalf("instance = %q, want smoke-alarm-late", event.Source.Instance)
	}
	if event.Source.Endpoint != "http://192.168.1.50:18088" {
		t.Fatalf("endpoint = %q", event.Source.Endpoint)
	}
	if got := reg.Len(); got != 1 {
		t.Fatalf("registry holds %d source(s), want 1", got)
	}
}

func TestObserveIgnoresNonChannelServices(t *testing.T) {
	reg := NewRegistry()

	for _, serviceType := range []string{"_tuner._tcp", "_adhd-isotope._tcp"} {
		svc := smokeAlarm("adhd", 9090)
		svc.ServiceType = serviceType
		if _, ok := reg.Observe(svc); ok {
			t.Errorf("%s record must not become a channel source", serviceType)
		}
	}
	if got := reg.Len(); got != 0 {
		t.Fatalf("registry holds %d source(s), want 0", got)
	}
}

// A member that rebinds its health port is the same source at a new address.
// Keying on host:port would make every rebind a second channel.
func TestObserveRebindUpdatesInPlace(t *testing.T) {
	reg := NewRegistry()
	now := time.Now()

	first := smokeAlarm("smoke-alarm-primary", 18088)
	first.DiscoveredAt = now.Add(-time.Minute)
	reg.Observe(first)

	rebound := smokeAlarm("smoke-alarm-primary", 18089)
	rebound.DiscoveredAt = now
	event, ok := reg.Observe(rebound)
	if !ok {
		t.Fatal("expected the rebound record to be accepted")
	}
	if event.Kind != Updated {
		t.Fatalf("event kind = %s, want updated", event.Kind)
	}

	if got := reg.Len(); got != 1 {
		t.Fatalf("registry holds %d source(s) after rebind, want 1", got)
	}
	src, found := reg.Get("smoke-alarm-primary")
	if !found {
		t.Fatal("source disappeared after rebind")
	}
	if src.Endpoint != "http://192.168.1.50:18089" {
		t.Fatalf("endpoint = %q, want the rebound port", src.Endpoint)
	}
}

// The old record of a rebind stays in the browser until it expires. Its
// refresh (or its late arrival) must not roll the source back to the dead
// address.
func TestObserveKeepsNewestEndpoint(t *testing.T) {
	reg := NewRegistry()
	now := time.Now()

	first := smokeAlarm("smoke-alarm-primary", 18088)
	first.DiscoveredAt = now.Add(-time.Minute)
	reg.Observe(first)

	rebound := smokeAlarm("smoke-alarm-primary", 18089)
	rebound.DiscoveredAt = now
	reg.Observe(rebound)

	stale := smokeAlarm("smoke-alarm-primary", 18088)
	stale.DiscoveredAt = now.Add(-time.Minute)
	stale.LastSeen = now.Add(time.Second)
	reg.Observe(stale)

	src, _ := reg.Get("smoke-alarm-primary")
	if src.Endpoint != "http://192.168.1.50:18089" {
		t.Fatalf("stale record rolled the source back to %q", src.Endpoint)
	}
}

func TestRetireRemovesCurrentEndpoint(t *testing.T) {
	reg := NewRegistry()
	svc := smokeAlarm("smoke-alarm-primary", 18088)
	reg.Observe(svc)

	event, ok := reg.Retire(svc)
	if !ok {
		t.Fatal("expected the departure to remove the source")
	}
	if event.Kind != Retired {
		t.Fatalf("event kind = %s, want retired", event.Kind)
	}
	if got := reg.Len(); got != 0 {
		t.Fatalf("registry holds %d source(s) after retirement, want 0", got)
	}
}

// A stale retirement names the old endpoint; the live source must survive it.
func TestRetireIgnoresStaleEndpoint(t *testing.T) {
	reg := NewRegistry()
	now := time.Now()

	first := smokeAlarm("smoke-alarm-primary", 18088)
	first.DiscoveredAt = now.Add(-time.Minute)
	reg.Observe(first)

	rebound := smokeAlarm("smoke-alarm-primary", 18089)
	rebound.DiscoveredAt = now
	reg.Observe(rebound)

	if _, ok := reg.Retire(first); ok {
		t.Fatal("a retirement for the old endpoint must not remove the rebound source")
	}
	if _, found := reg.Get("smoke-alarm-primary"); !found {
		t.Fatal("the live source was removed by a stale retirement")
	}

	if _, ok := reg.Retire(rebound); !ok {
		t.Fatal("the current endpoint's retirement should remove the source")
	}
}

func TestEachInstanceIsIndependent(t *testing.T) {
	reg := NewRegistry()

	a := smokeAlarm("smoke-alarm-a", 18088)
	b := smokeAlarm("smoke-alarm-b", 18089)
	reg.Observe(a)
	reg.Observe(b)

	reg.Retire(a)

	if _, found := reg.Get("smoke-alarm-a"); found {
		t.Error("smoke-alarm-a should be gone")
	}
	if _, found := reg.Get("smoke-alarm-b"); !found {
		t.Error("smoke-alarm-b should be unaffected by another instance's departure")
	}
}

func TestSubscribeReplaysAndReceivesChanges(t *testing.T) {
	reg := NewRegistry()
	reg.Observe(smokeAlarm("smoke-alarm-a", 18088))

	var events []Event
	unsubscribe := reg.Subscribe(func(event Event) {
		events = append(events, event)
	})

	if len(events) != 1 || events[0].Kind != Added || events[0].Source.Instance != "smoke-alarm-a" {
		t.Fatalf("expected the existing source replayed as added, got %+v", events)
	}

	reg.Observe(smokeAlarm("smoke-alarm-b", 18089))
	reg.Retire(smokeAlarm("smoke-alarm-b", 18089))

	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d: %+v", len(events), events)
	}
	if events[1].Kind != Added || events[2].Kind != Retired {
		t.Fatalf("unexpected event sequence: %+v", events)
	}

	unsubscribe()
	reg.Observe(smokeAlarm("smoke-alarm-c", 18090))
	if len(events) != 3 {
		t.Fatalf("events arrived after unsubscribe: %+v", events)
	}
}

func TestSourceStatusPollsEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"service":"ocd-smoke-alarm","ready":true}`))
	}))
	defer server.Close()

	src := Source{Instance: "smoke-alarm-late", Endpoint: server.URL}
	body, err := src.Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}

	var status map[string]any
	if err := json.Unmarshal(body, &status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if status["service"] != "ocd-smoke-alarm" {
		t.Fatalf("unexpected status body: %s", body)
	}
}

func TestSourceStatusRejectsBadResponses(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		wantErr bool
	}{
		{name: "unreachable status", status: http.StatusServiceUnavailable, body: `{}`, wantErr: true},
		{name: "not JSON", status: http.StatusOK, body: "not json", wantErr: true},
		{name: "ok", status: http.StatusOK, body: `{"ready":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			src := Source{Instance: "smoke-alarm-late", Endpoint: server.URL}
			_, err := src.Status(context.Background())
			if tc.wantErr && err == nil {
				t.Fatal("expected an error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
