package mdns

import (
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
