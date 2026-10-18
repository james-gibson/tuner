package mdns

import (
	"context"
	"sync"
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
// within one scan deadline. The old serial scan let the first type consume
// the whole budget, so the broadcast mode pre-scan (5s) never reached
// _smoke-alarm._tcp at all.
func TestScanBrowsesEveryTypeUnderOneDeadline(t *testing.T) {
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
	b.scan(ctx)

	mu.Lock()
	defer mu.Unlock()
	for _, svcType := range b.serviceTypes {
		if browsed[svcType] != 1 {
			t.Errorf("service type %s browsed %d time(s), want exactly 1 — the scan budget is shared, not serial",
				svcType, browsed[svcType])
		}
	}
}

// A scan must not outlive its context even when types are browsed concurrently.
func TestScanReturnsWhenContextCancelled(t *testing.T) {
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
	b.scan(ctx)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("scan took %s, want roughly the 50ms context", elapsed)
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
