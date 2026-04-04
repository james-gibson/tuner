// Package mdns provides mDNS discovery for Tuner.
// In receive mode, Tuner browses for _smoke-alarm._tcp and _tuner._tcp services.
// Tuner never registers itself -- it is a passive observer.
package mdns

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/grandcat/zeroconf"
)

// Service represents a discovered mDNS service.
type Service struct {
	Name        string            `json:"name"`
	ServiceType string            `json:"service_type"`
	Host        string            `json:"host"`
	Port        int               `json:"port"`
	TXT         map[string]string `json:"txt"`
	DiscoveredAt time.Time        `json:"discovered_at"`
	LastSeen     time.Time        `json:"last_seen"`
}

// Endpoint returns the HTTP address for this service.
func (s Service) Endpoint() string {
	return fmt.Sprintf("http://%s:%d", s.Host, s.Port)
}

// Browser passively discovers services on the local network.
type Browser struct {
	serviceTypes []string
	domains      []string
	interval     time.Duration
	quiet        bool

	mu       sync.RWMutex
	services map[string]Service // key: "type:host:port"
	onChange func(Service)      // optional callback
}

// BrowserOptions configures the mDNS browser.
type BrowserOptions struct {
	ServiceTypes []string      // e.g. ["_smoke-alarm._tcp", "_tuner._tcp"]
	Domains      []string      // e.g. ["local"]
	Interval     time.Duration // refresh interval
	OnChange     func(Service) // called when a new service is found
	Quiet        bool          // suppress log output (for TV channel use)
}

// NewBrowser creates a passive mDNS browser.
func NewBrowser(opts BrowserOptions) *Browser {
	if len(opts.Domains) == 0 {
		opts.Domains = []string{"local"}
	}
	if opts.Interval <= 0 {
		opts.Interval = 30 * time.Second
	}
	return &Browser{
		serviceTypes: opts.ServiceTypes,
		domains:      opts.Domains,
		interval:     opts.Interval,
		quiet:        opts.Quiet,
		services:     make(map[string]Service),
		onChange:     opts.OnChange,
	}
}

// Start begins periodic browsing until ctx is cancelled.
func (b *Browser) Start(ctx context.Context) {
	if !b.quiet {
		log.Printf("mdns browser: watching for %v on domains %v (interval: %s)",
			b.serviceTypes, b.domains, b.interval)
	}

	// Do an initial scan.
	b.scan(ctx)

	ticker := time.NewTicker(b.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			b.scan(ctx)
		case <-ctx.Done():
			if !b.quiet {
				log.Println("mdns browser: stopped")
			}
			return
		}
	}
}

// Services returns a snapshot of all discovered services.
func (b *Browser) Services() []Service {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]Service, 0, len(b.services))
	for _, s := range b.services {
		out = append(out, s)
	}
	return out
}

// ServicesByType returns services matching a specific type.
func (b *Browser) ServicesByType(serviceType string) []Service {
	b.mu.RLock()
	defer b.mu.RUnlock()
	var out []Service
	for _, s := range b.services {
		if s.ServiceType == serviceType {
			out = append(out, s)
		}
	}
	return out
}

// scan performs one round of mDNS browsing for all configured service types.
// Each browse runs for up to 5 seconds (or until ctx is cancelled).
func (b *Browser) scan(ctx context.Context) {
	domain := b.domains[0]

	for _, svcType := range b.serviceTypes {
		// Bail out early if the parent context is already done.
		select {
		case <-ctx.Done():
			return
		default:
		}

		resolver, err := zeroconf.NewResolver(nil)
		if err != nil {
			log.Printf("mdns browser: create resolver for %s: %v", svcType, err)
			continue
		}

		entries := make(chan *zeroconf.ServiceEntry)
		browseCtx, browseCancel := context.WithTimeout(ctx, 5*time.Second)

		// Start browse in background - Browse closes the entries channel when done
		go func(st string, quiet bool) {
			err := resolver.Browse(browseCtx, st, domain, entries)
			if err != nil && !quiet {
				log.Printf("mdns browser: browse %s: %v", st, err)
			}
		}(svcType, b.quiet)

		// Collect entries until the channel is closed (when browseCtx times out)
		discovered := 0
		for entry := range entries {
			host := entry.HostName
			// Prefer the first IPv4 address if available.
			if len(entry.AddrIPv4) > 0 {
				host = entry.AddrIPv4[0].String()
			} else if len(entry.AddrIPv6) > 0 {
				host = entry.AddrIPv6[0].String()
			}

			svc := Service{
				Name:        entry.ServiceInstanceName(),
				ServiceType: svcType,
				Host:        host,
				Port:        entry.Port,
				TXT:         parseTXT(entry.Text),
			}
			b.addService(svc)
			discovered++
			if !b.quiet {
				log.Printf("mdns browser: discovered %s at %s:%d", svc.Name, svc.Host, svc.Port)
			}
		}

		// Cancel after processing all entries to clean up resources
		browseCancel()

		if discovered > 0 && !b.quiet {
			log.Printf("mdns browser: found %d %s services", discovered, svcType)
		}
	}

	b.mu.RLock()
	count := len(b.services)
	b.mu.RUnlock()
	if !b.quiet {
		log.Printf("mdns browser: scan complete (%d services tracked)", count)
	}
}

// Add inserts a service discovered via a non-mDNS mechanism (e.g. an HTTP
// registry). Follows the same deduplication logic as internal mDNS entries.
func (b *Browser) Add(svc Service) {
	b.addService(svc)
}

func (b *Browser) addService(svc Service) {
	key := fmt.Sprintf("%s:%s:%d", svc.ServiceType, svc.Host, svc.Port)

	b.mu.Lock()
	existing, found := b.services[key]
	svc.LastSeen = time.Now()
	if found {
		svc.DiscoveredAt = existing.DiscoveredAt
	} else {
		svc.DiscoveredAt = time.Now()
	}
	b.services[key] = svc
	b.mu.Unlock()

	if !found && b.onChange != nil {
		b.onChange(svc)
	}
}

// parseTXT converts zeroconf TXT records ("key=value") into a map.
func parseTXT(records []string) map[string]string {
	m := make(map[string]string, len(records))
	for _, r := range records {
		parts := strings.SplitN(r, "=", 2)
		if len(parts) == 2 {
			m[parts[0]] = parts[1]
		} else if len(parts) == 1 && parts[0] != "" {
			m[parts[0]] = ""
		}
	}
	return m
}
