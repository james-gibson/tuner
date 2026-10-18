// Package mdns provides mDNS discovery for Tuner.
// In receive mode, Tuner browses for _smoke-alarm._tcp and _tuner._tcp services.
// Tuner never registers itself -- it is a passive observer.
package mdns

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/grandcat/zeroconf"
)

// SmokeAlarmServiceType is the bare tool type smoke-alarm advertises:
// tuner's data source, but not an identity. Trust is carried by the registered
// isotope types, whose TXT records hold role and trust_rung (tuner#3).
const SmokeAlarmServiceType = "_smoke-alarm._tcp"

// defaultBrowseWindow is how long one service type's browse runs. Every type
// in the array is browsed concurrently, so the window is the budget for the
// whole scan, not a serial slice of it per type. Browsing the types one after
// another is what hid _smoke-alarm._tcp: the pre-advertising peer scan in
// broadcast mode allows one 5s deadline, the first type consumed it all, and
// the remaining types were never queried (tuner#3).
const defaultBrowseWindow = 5 * time.Second

// Service represents a discovered mDNS service record: one instance of one
// service type at one address. Several records may share an address -- see
// Endpoint for the address-keyed view.
type Service struct {
	Name         string            `json:"name"`
	ServiceType  string            `json:"service_type"`
	Host         string            `json:"host"`
	Port         int               `json:"port"`
	TXT          map[string]string `json:"txt"`
	DiscoveredAt time.Time         `json:"discovered_at"`
	LastSeen     time.Time         `json:"last_seen"`
}

// Endpoint returns the HTTP address for this service.
func (s Service) Endpoint() string {
	return fmt.Sprintf("http://%s:%d", s.Host, s.Port)
}

// Instance returns the DNS-SD instance label ("adhd" from
// "adhd._adhd-isotope._tcp.local."). Records that never carried a
// fully-qualified instance name (lezz demo seeds) return the name verbatim.
func (s Service) Instance() string {
	if s.ServiceType != "" {
		suffix := "." + strings.TrimSuffix(s.ServiceType, ".") + "."
		if i := strings.Index(strings.ToLower(s.Name), strings.ToLower(suffix)); i > 0 {
			return s.Name[:i]
		}
	}
	return s.Name
}

// Identity is the trust-bearing identity an isotope record carries in its TXT
// record: isotope.Advertise stamps role= and trust_rung= alongside the isotope
// service type. A bare tool advertisement (_smoke-alarm._tcp, _tuner._tcp)
// carries no identity -- browsing it finds a thing on the network, not a
// trusted peer.
type Identity struct {
	ServiceType string `json:"service_type"`
	Instance    string `json:"instance"`
	Role        string `json:"role,omitempty"`
	TrustRung   int    `json:"trust_rung,omitempty"`
}

// Identity reports the isotope identity stamped into the record's TXT record.
// ok is false when the record carries neither role nor a parseable trust_rung.
func (s Service) Identity() (Identity, bool) {
	role, hasRole := s.TXT["role"]

	rung, hasRung := 0, false
	if raw, ok := s.TXT["trust_rung"]; ok {
		if v, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil {
			rung, hasRung = v, true
		}
	}

	if !hasRole && !hasRung {
		return Identity{}, false
	}
	return Identity{
		ServiceType: s.ServiceType,
		Instance:    s.Instance(),
		Role:        role,
		TrustRung:   rung,
	}, true
}

// Endpoint is one host:port plus every service record and isotope identity
// observed there. The endpoint, not the record, is the address of a thing on
// the network: several agents may supply the same host:port (several per-kb
// adhds, a repeated demo cluster, a restarted process before its old record
// expires), so the endpoint set dedupes by address and disambiguates by
// isotope identity and rung instead of counting one record as one endpoint.
type Endpoint struct {
	Host         string     `json:"host"`
	Port         int        `json:"port"`
	ServiceTypes []string   `json:"service_types"`
	Identities   []Identity `json:"identities,omitempty"`
	Services     []Service  `json:"services"`
	DiscoveredAt time.Time  `json:"discovered_at"`
	LastSeen     time.Time  `json:"last_seen"`
}

// URL returns the HTTP address for this endpoint.
func (e Endpoint) URL() string {
	return fmt.Sprintf("http://%s:%d", e.Host, e.Port)
}

// browseFunc performs one browse for serviceType under ctx, handing every
// record it sees to the browser. It is a seam so the scan budget and the
// browse array can be tested without touching the network.
type browseFunc func(ctx context.Context, domain, serviceType string)

// Browser passively discovers services on the local network.
type Browser struct {
	serviceTypes []string
	domains      []string
	interval     time.Duration
	browseWindow time.Duration
	quiet        bool
	browse       browseFunc

	mu       sync.RWMutex
	services map[string]Service // records, keyed by type|instance|host:port
	onChange func(Service)      // optional callback, once per new record
}

// BrowserOptions configures the mDNS browser.
type BrowserOptions struct {
	ServiceTypes []string      // e.g. cfg.MDNS.BrowseTypes()
	Domains      []string      // e.g. ["local"]
	Interval     time.Duration // refresh interval
	OnChange     func(Service) // called when a new record is found
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
	b := &Browser{
		serviceTypes: opts.ServiceTypes,
		domains:      opts.Domains,
		interval:     opts.Interval,
		browseWindow: defaultBrowseWindow,
		quiet:        opts.Quiet,
		services:     make(map[string]Service),
		onChange:     opts.OnChange,
	}
	b.browse = b.zeroconfBrowse
	return b
}

// Start begins periodic browsing until ctx is canceled.
func (b *Browser) Start(ctx context.Context) {
	if !b.quiet {
		log.Printf("mdns browser: watching for %v on domains %v (interval: %s, window: %s, types browsed concurrently)",
			b.serviceTypes, b.domains, b.interval, b.browseWindow)
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

// Services returns a snapshot of every discovered record.
func (b *Browser) Services() []Service {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]Service, 0, len(b.services))
	for _, s := range b.services {
		out = append(out, s)
	}
	return out
}

// ServicesByType returns records matching a specific type.
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

// Endpoints returns the discovered addresses, deduped by host:port, each
// carrying every record and isotope identity seen there. This is the set that
// answers "what is on the network"; Services answers "what advertised it".
func (b *Browser) Endpoints() []Endpoint {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return foldEndpoints(b.services)
}

// EndpointsByType returns the endpoints at which a given service type was
// observed (an endpoint may carry several types).
func (b *Browser) EndpointsByType(serviceType string) []Endpoint {
	b.mu.RLock()
	defer b.mu.RUnlock()

	records := make(map[string]Service, len(b.services))
	for key, svc := range b.services {
		if svc.ServiceType == serviceType {
			records[key] = svc
		}
	}
	return foldEndpoints(records)
}

// foldEndpoints folds records into host:port endpoints. Records are sorted
// first so the result is deterministic rather than map-iteration order.
func foldEndpoints(records map[string]Service) []Endpoint {
	sorted := make([]Service, 0, len(records))
	for _, svc := range records {
		sorted = append(sorted, svc)
	}
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Host != sorted[j].Host {
			return sorted[i].Host < sorted[j].Host
		}
		if sorted[i].Port != sorted[j].Port {
			return sorted[i].Port < sorted[j].Port
		}
		if sorted[i].ServiceType != sorted[j].ServiceType {
			return sorted[i].ServiceType < sorted[j].ServiceType
		}
		return sorted[i].Name < sorted[j].Name
	})

	byAddr := make(map[string]*Endpoint, len(sorted))
	order := make([]*Endpoint, 0, len(sorted))

	for _, svc := range sorted {
		addr := fmt.Sprintf("%s:%d", svc.Host, svc.Port)
		ep, ok := byAddr[addr]
		if !ok {
			ep = &Endpoint{
				Host:         svc.Host,
				Port:         svc.Port,
				DiscoveredAt: svc.DiscoveredAt,
			}
			byAddr[addr] = ep
			order = append(order, ep)
		}
		if svc.DiscoveredAt.Before(ep.DiscoveredAt) {
			ep.DiscoveredAt = svc.DiscoveredAt
		}
		if svc.LastSeen.After(ep.LastSeen) {
			ep.LastSeen = svc.LastSeen
		}
		if !containsString(ep.ServiceTypes, svc.ServiceType) {
			ep.ServiceTypes = append(ep.ServiceTypes, svc.ServiceType)
		}
		ep.Services = append(ep.Services, svc)
		if id, ok := svc.Identity(); ok && !containsIdentity(ep.Identities, id) {
			ep.Identities = append(ep.Identities, id)
		}
	}

	out := make([]Endpoint, len(order))
	for i, ep := range order {
		out[i] = *ep
	}
	return out
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func containsIdentity(list []Identity, id Identity) bool {
	for _, v := range list {
		if v == id {
			return true
		}
	}
	return false
}

// scan performs one round of mDNS browsing for the configured service types.
// Every type is browsed concurrently under the same context, so a scan costs
// one browse window in total however long the array is; each browse runs for
// up to the browse window or until ctx is canceled.
func (b *Browser) scan(ctx context.Context) {
	domain := b.domains[0]

	var wg sync.WaitGroup
	for _, svcType := range b.serviceTypes {
		// Skip types that can no longer be queried at all. A type is never
		// skipped because an earlier type used up its window: they all start
		// together (tuner#3).
		select {
		case <-ctx.Done():
			if !b.quiet {
				log.Printf("mdns browser: scan canceled before browsing the full array %v", b.serviceTypes)
			}
			wg.Wait()
			return
		default:
		}

		wg.Add(1)
		go func(st string) {
			defer wg.Done()
			b.browseType(ctx, domain, st)
		}(svcType)
	}
	wg.Wait()

	b.mu.RLock()
	count := len(b.services)
	b.mu.RUnlock()
	if !b.quiet {
		log.Printf("mdns browser: scan complete (%d services tracked)", count)
	}
}

// browseType browses one service type for at most the browse window, bounded
// by ctx.
func (b *Browser) browseType(ctx context.Context, domain, svcType string) {
	window := b.browseWindow
	if window <= 0 {
		window = defaultBrowseWindow
	}
	browseCtx, cancel := context.WithTimeout(ctx, window)
	defer cancel()
	b.browse(browseCtx, domain, svcType)
}

// zeroconfBrowse is the real browse: it resolves one service type on the LAN
// and feeds every record it sees to the browser until browseCtx ends.
func (b *Browser) zeroconfBrowse(browseCtx context.Context, domain, svcType string) {
	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		if !b.quiet {
			log.Printf("mdns browser: create resolver for %s: %v", svcType, err)
		}
		return
	}

	entries := make(chan *zeroconf.ServiceEntry)

	// Start browse in background - Browse closes the entries channel when done
	go func() {
		if err := resolver.Browse(browseCtx, svcType, domain, entries); err != nil && !b.quiet {
			log.Printf("mdns browser: browse %s: %v", svcType, err)
		}
	}()

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

	if discovered > 0 && !b.quiet {
		log.Printf("mdns browser: found %d %s services", discovered, svcType)
	}
}

// Add inserts a service discovered via a non-mDNS mechanism (e.g. an HTTP
// registry). Follows the same record and endpoint deduplication as mDNS
// entries: a seed for a host:port that is already known does not create a
// second endpoint.
func (b *Browser) Add(svc Service) {
	b.addService(svc)
}

// recordKey identifies one advertised record: the same instance of the same
// type at the same address. Distinct instances at one address are distinct
// records (they may carry different identities) but a single Endpoint.
func recordKey(svc Service) string {
	return fmt.Sprintf("%s|%s|%s:%d", svc.ServiceType, svc.Name, svc.Host, svc.Port)
}

func (b *Browser) addService(svc Service) {
	key := recordKey(svc)

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
