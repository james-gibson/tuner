package mdns

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewBrowserDefaults(t *testing.T) {
	b := NewBrowser(BrowserOptions{
		ServiceTypes: []string{"_tuner._tcp"},
	})
	if len(b.domains) != 1 || b.domains[0] != "local" {
		t.Fatalf("expected default domain [local], got %v", b.domains)
	}
	if b.interval != 30*time.Second {
		t.Fatalf("expected default interval 30s, got %v", b.interval)
	}
}

func TestServicesEmpty(t *testing.T) {
	b := NewBrowser(BrowserOptions{
		ServiceTypes: []string{"_tuner._tcp"},
	})
	services := b.Services()
	if len(services) != 0 {
		t.Fatalf("expected 0 services, got %d", len(services))
	}
}

func TestAddService(t *testing.T) {
	var discovered Service
	b := NewBrowser(BrowserOptions{
		ServiceTypes: []string{"_tuner._tcp"},
		OnChange: func(svc Service) {
			discovered = svc
		},
	})

	svc := Service{
		Name:        "test-alarm",
		ServiceType: "_smoke-alarm._tcp",
		Host:        "192.168.1.50",
		Port:        18088,
		TXT:         map[string]string{"version": "1.0"},
	}
	b.addService(svc)

	services := b.Services()
	if len(services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(services))
	}
	if services[0].Name != "test-alarm" {
		t.Fatalf("expected name test-alarm, got %q", services[0].Name)
	}
	if discovered.Name != "test-alarm" {
		t.Fatalf("expected onChange to fire with test-alarm, got %q", discovered.Name)
	}
}

func TestAddServiceDuplicate(t *testing.T) {
	callCount := 0
	b := NewBrowser(BrowserOptions{
		ServiceTypes: []string{"_tuner._tcp"},
		OnChange: func(svc Service) {
			callCount++
		},
	})

	svc := Service{
		Name:        "test-alarm",
		ServiceType: "_smoke-alarm._tcp",
		Host:        "192.168.1.50",
		Port:        18088,
	}
	b.addService(svc)
	b.addService(svc) // duplicate

	if callCount != 1 {
		t.Fatalf("expected onChange called once (not for duplicate), got %d", callCount)
	}
	if len(b.Services()) != 1 {
		t.Fatalf("expected 1 service after duplicate add, got %d", len(b.Services()))
	}
}

func TestServicesByType(t *testing.T) {
	b := NewBrowser(BrowserOptions{
		ServiceTypes: []string{"_tuner._tcp", "_smoke-alarm._tcp"},
	})
	b.addService(Service{Name: "tuner1", ServiceType: "_tuner._tcp", Host: "1.1.1.1", Port: 8093})
	b.addService(Service{Name: "alarm1", ServiceType: "_smoke-alarm._tcp", Host: "2.2.2.2", Port: 18088})
	b.addService(Service{Name: "alarm2", ServiceType: "_smoke-alarm._tcp", Host: "3.3.3.3", Port: 18089})

	tuners := b.ServicesByType("_tuner._tcp")
	if len(tuners) != 1 {
		t.Fatalf("expected 1 tuner, got %d", len(tuners))
	}

	alarms := b.ServicesByType("_smoke-alarm._tcp")
	if len(alarms) != 2 {
		t.Fatalf("expected 2 alarms, got %d", len(alarms))
	}
}

func TestServiceEndpoint(t *testing.T) {
	svc := Service{Host: "192.168.1.50", Port: 18088}
	if svc.Endpoint() != "http://192.168.1.50:18088" {
		t.Fatalf("expected http://192.168.1.50:18088, got %q", svc.Endpoint())
	}

	// An IPv6 literal has to be bracketed or the endpoint is not a URL, and a
	// discovered source is fetched from this address.
	v6 := Service{Host: "fe80::1", Port: 18088}
	if v6.Endpoint() != "http://[fe80::1]:18088" {
		t.Fatalf("expected a bracketed IPv6 endpoint, got %q", v6.Endpoint())
	}
	ep := Endpoint{Host: "2001:db8::2", Port: 9090}
	if ep.URL() != "http://[2001:db8::2]:9090" {
		t.Fatalf("expected a bracketed IPv6 endpoint URL, got %q", ep.URL())
	}
}

func TestParsePort(t *testing.T) {
	if p := ParsePort("127.0.0.1:8093"); p != 8093 {
		t.Fatalf("expected 8093, got %d", p)
	}
	if p := ParsePort("invalid"); p != 0 {
		t.Fatalf("expected 0 for invalid, got %d", p)
	}
	if p := ParsePort(""); p != 0 {
		t.Fatalf("expected 0 for empty, got %d", p)
	}
}

// Regression for tuner#3: every type in the browse array must be queried
// within one refresh deadline. A serial scan let the first type consume the
// whole budget, so a bounded scan never reached _smoke-alarm._tcp at all.
func TestRefreshBrowsesEveryTypeUnderOneDeadline(t *testing.T) {
	b := NewBrowser(BrowserOptions{
		ServiceTypes: []string{"_tuner._tcp", "_smoke-alarm._tcp", "_adhd-isotope._tcp"},
		Quiet:        true,
	})
	b.browseWindow = time.Second // each type would happily burn a full window

	var mu sync.Mutex
	browsed := make(map[string]int)
	b.browse = func(ctx context.Context, domain, svcType string) {
		mu.Lock()
		browsed[svcType]++
		mu.Unlock()
		<-ctx.Done() // like the real resolver: runs until the window closes
	}

	// One deadline, shorter than a single browse window.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	b.refresh(ctx)

	mu.Lock()
	defer mu.Unlock()
	for _, svcType := range b.serviceTypes {
		if browsed[svcType] != 1 {
			t.Errorf("service type %s browsed %d time(s), want exactly 1 — the scan budget is shared, not serial",
				svcType, browsed[svcType])
		}
	}
}

// A refresh must not outlive its context even when types are browsed
// concurrently.
func TestRefreshReturnsWhenContextCancelled(t *testing.T) {
	b := NewBrowser(BrowserOptions{
		ServiceTypes: []string{"_tuner._tcp", "_smoke-alarm._tcp"},
		Quiet:        true,
	})
	b.browse = func(ctx context.Context, domain, svcType string) {
		<-ctx.Done()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	b.refresh(ctx)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("refresh took %s, want roughly the 50ms context", elapsed)
	}
}

// tuner#3: several agents may supply one host:port, so the network is counted
// in endpoints — deduped by address and disambiguated by isotope identity —
// not in service records.
func TestEndpointsDedupeByEndpoint(t *testing.T) {
	b := NewBrowser(BrowserOptions{ServiceTypes: []string{"_tuner._tcp"}, Quiet: true})

	adhdA := Service{
		Name:        "adhd._adhd-isotope._tcp.local.",
		ServiceType: "_adhd-isotope._tcp",
		Host:        "192.168.1.10", Port: 9090,
		TXT: map[string]string{"v": "1", "role": "prime", "trust_rung": "4"},
	}
	adhdB := Service{
		Name:        "adhd-lab._adhd-isotope._tcp.local.",
		ServiceType: "_adhd-isotope._tcp",
		Host:        "192.168.1.10", Port: 9090,
		TXT: map[string]string{"v": "1", "role": "peer", "trust_rung": "2"},
	}
	peer := Service{
		Name:        "tuner._tuner._tcp.local.",
		ServiceType: "_tuner._tcp",
		Host:        "192.168.1.10", Port: 9090,
	}
	alarm := Service{
		Name:        "alarm1",
		ServiceType: SmokeAlarmServiceType,
		Host:        "192.168.1.11", Port: 18088,
	}

	b.Add(adhdA)
	b.Add(adhdB)
	b.Add(peer)
	b.Add(alarm)
	b.Add(adhdA) // re-add: a repeat must not add a record or fire OnChange

	if got := len(b.Services()); got != 4 {
		t.Fatalf("expected 4 records, got %d", got)
	}

	endpoints := b.Endpoints()
	if len(endpoints) != 2 {
		t.Fatalf("expected 2 endpoints, got %d (%v)", len(endpoints), endpoints)
	}

	shared := endpoints[0]
	if shared.Host != "192.168.1.10" || shared.Port != 9090 {
		t.Fatalf("expected the shared 192.168.1.10:9090 endpoint first, got %s", shared.URL())
	}
	if len(shared.Services) != 3 {
		t.Errorf("expected 3 records at the shared endpoint, got %d", len(shared.Services))
	}
	if len(shared.ServiceTypes) != 2 {
		t.Errorf("expected 2 service types at the shared endpoint, got %v", shared.ServiceTypes)
	}
	if len(shared.Identities) != 2 {
		t.Fatalf("expected 2 isotope identities at the shared endpoint, got %v", shared.Identities)
	}
	// Both identities survive on one endpoint, rungs intact; the order among
	// them is an implementation detail of record sorting.
	for _, id := range shared.Identities {
		switch id.Role {
		case "prime":
			if id.TrustRung != 4 || id.Instance != "adhd" {
				t.Errorf("unexpected prime identity %+v", id)
			}
		case "peer":
			if id.TrustRung != 2 || id.Instance != "adhd-lab" {
				t.Errorf("unexpected peer identity %+v", id)
			}
		default:
			t.Errorf("unexpected identity %+v", id)
		}
	}
	if shared.URL() != "http://192.168.1.10:9090" {
		t.Errorf("unexpected URL %q", shared.URL())
	}

	// The bare records at the other address carry no identity.
	if len(endpoints[1].Identities) != 0 {
		t.Errorf("expected no identities on the alarm endpoint, got %v", endpoints[1].Identities)
	}

	byType := b.EndpointsByType("_adhd-isotope._tcp")
	if len(byType) != 1 || len(byType[0].Identities) != 2 {
		t.Fatalf("expected 1 isotope endpoint with 2 identities, got %d (%+v)", len(byType), byType)
	}
	if got := len(b.EndpointsByType(SmokeAlarmServiceType)); got != 1 {
		t.Fatalf("expected 1 smoke-alarm endpoint, got %d", got)
	}
}

func TestServiceIdentity(t *testing.T) {
	isotope := Service{
		Name:        "adhd._adhd-isotope._tcp.local.",
		ServiceType: "_adhd-isotope._tcp",
		TXT:         map[string]string{"v": "1", "role": "prime", "trust_rung": "4"},
	}
	id, ok := isotope.Identity()
	if !ok {
		t.Fatal("expected an isotope record to carry an identity")
	}
	if id.Instance != "adhd" {
		t.Errorf("expected instance adhd, got %q", id.Instance)
	}
	if id.ServiceType != "_adhd-isotope._tcp" || id.Role != "prime" || id.TrustRung != 4 {
		t.Errorf("unexpected identity %+v", id)
	}

	// A bare tool advertisement finds a thing on the network, not a peer.
	bare := Service{
		Name:        "tuner._tuner._tcp.local.",
		ServiceType: "_tuner._tcp",
		TXT:         map[string]string{"version": "1.0"},
	}
	if _, ok := bare.Identity(); ok {
		t.Error("a bare tool record must carry no identity")
	}

	// A role alone is an identity; a rung that is not a number is not.
	roleOnly := Service{ServiceType: "_adhd-isotope._tcp", TXT: map[string]string{"role": "peer"}}
	if id, ok := roleOnly.Identity(); !ok || id.TrustRung != 0 {
		t.Errorf("expected role-only identity with rung 0, got %+v (ok=%v)", id, ok)
	}
	garbageRung := Service{ServiceType: "_adhd-isotope._tcp", TXT: map[string]string{"trust_rung": "high"}}
	if _, ok := garbageRung.Identity(); ok {
		t.Error("an unparseable trust_rung alone must not produce an identity")
	}
}

// Records seeded from a non-mDNS registry keep their synthetic names.
func TestInstanceForNonDNSSDNames(t *testing.T) {
	seed := Service{Name: "demo/alarm-a", ServiceType: SmokeAlarmServiceType}
	if got := seed.Instance(); got != "demo/alarm-a" {
		t.Fatalf("expected the name verbatim, got %q", got)
	}
}

// The browse loop is not a scan: it stays open for the lifetime of the process,
// so an instance that announces after startup is still seen
// (features/tuner-mdns-continuous.feature).
func TestStartKeepsOneBrowseOpenPerType(t *testing.T) {
	b := NewBrowser(BrowserOptions{
		ServiceTypes: []string{"_smoke-alarm._tcp", "_adhd-isotope._tcp"},
		Quiet:        true,
	})

	var mu sync.Mutex
	starts := make(map[string]int)
	var active atomic.Int32

	b.browse = func(ctx context.Context, domain, svcType string) {
		mu.Lock()
		starts[svcType]++
		mu.Unlock()
		active.Add(1)
		defer active.Add(-1)
		<-ctx.Done()
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		b.Start(ctx)
		close(done)
	}()

	// Well past the old 5s window's proportional delay: nothing closes it.
	deadline := time.Now().Add(2 * time.Second)
	for active.Load() != int32(len(b.serviceTypes)) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	// An announcement that arrives after startup is tracked like any other.
	b.addService(Service{
		Name:        "smoke-alarm-late",
		ServiceType: "_smoke-alarm._tcp",
		Host:        "192.168.1.77",
		Port:        18090,
	})
	if got := len(b.ServicesByType("_smoke-alarm._tcp")); got != 1 {
		t.Fatalf("late announcement tracked as %d service(s), want 1", got)
	}

	// Still browsing: the loop did not close after the first batch.
	if got := active.Load(); got != int32(len(b.serviceTypes)) {
		t.Fatalf("%d browse(s) open, want %d", got, len(b.serviceTypes))
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after cancellation")
	}

	mu.Lock()
	defer mu.Unlock()
	for _, svcType := range b.serviceTypes {
		if starts[svcType] != 1 {
			t.Errorf("%s browsed %d time(s), want exactly one long-lived browse", svcType, starts[svcType])
		}
	}
}

func TestExpireRemovesDepartedRecords(t *testing.T) {
	var removed []Service
	b := NewBrowser(BrowserOptions{
		ServiceTypes: []string{"_smoke-alarm._tcp"},
		Quiet:        true,
		OnRemove: func(svc Service) {
			removed = append(removed, svc)
		},
	})
	b.expiry = 10 * time.Second

	b.addService(Service{Name: "alive", ServiceType: "_smoke-alarm._tcp", Host: "192.0.2.1", Port: 18088})
	b.addService(Service{Name: "gone", ServiceType: "_smoke-alarm._tcp", Host: "192.0.2.2", Port: 18088})

	now := time.Now()
	b.mu.Lock()
	gone := b.services[recordKey(Service{Name: "gone", ServiceType: "_smoke-alarm._tcp", Host: "192.0.2.2", Port: 18088})]
	gone.LastSeen = now.Add(-time.Minute)
	b.services[recordKey(gone)] = gone
	b.mu.Unlock()

	b.expire(now)

	if len(removed) != 1 || removed[0].Name != "gone" {
		t.Fatalf("removed = %v, want only the departed record", removed)
	}
	if names := len(b.Services()); names != 1 {
		t.Fatalf("tracked %d service(s) after expiry, want 1", names)
	}
}

func TestRefreshKeepsLiveRecordsFromExpiring(t *testing.T) {
	b := NewBrowser(BrowserOptions{
		ServiceTypes: []string{"_smoke-alarm._tcp"},
		Quiet:        true,
	})
	b.expiry = 10 * time.Second

	live := Service{Name: "live", ServiceType: "_smoke-alarm._tcp", Host: "192.0.2.9", Port: 18088}
	b.addService(live)

	// A refresh re-resolves and re-delivers the record: the browse seam does
	// the same here.
	b.browse = func(ctx context.Context, domain, svcType string) {
		b.addService(live)
	}

	b.mu.Lock()
	record := b.services[recordKey(live)]
	record.LastSeen = time.Now().Add(-time.Hour)
	b.services[recordKey(live)] = record
	b.mu.Unlock()

	b.refresh(context.Background())
	b.expire(time.Now())

	if got := len(b.Services()); got != 1 {
		t.Fatalf("a refreshed record was expired: %d service(s) left", got)
	}
}

// A cancellation while browsing must not leave Start waiting on a refresh.
func TestStartReturnsPromptlyOnCancel(t *testing.T) {
	b := NewBrowser(BrowserOptions{
		ServiceTypes: []string{"_tuner._tcp"},
		Quiet:        true,
	})
	b.interval = time.Hour
	b.browse = func(ctx context.Context, domain, svcType string) {
		<-ctx.Done()
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		b.Start(ctx)
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Start did not return after cancellation")
	}
}
