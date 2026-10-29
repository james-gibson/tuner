// Package channelsource keeps the set of live channel sources tuner has
// discovered on the network.
//
// A _smoke-alarm._tcp announcement is a data source: its health endpoint
// answers /status with the instance's current state. Before this registry
// existed the announcement was only a log line -- the browse loop stayed open
// (tuner#19) but nothing turned a record into a channel, so a post-startup
// instance never appeared in /channels and never got a TV TOML (tuner#20). The
// mDNS browser writes into the registry (OnChange adds or updates, OnRemove
// retires), and the HTTP server and the TV channel writer both read it.
package channelsource

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/james-gibson/tuner/internal/mdns"
)

const (
	// statusLimit bounds the upstream /status document. A discovered peer is
	// not trusted to size its response.
	statusLimit = 1 << 20
	// statusTimeout bounds one poll of a discovered endpoint so an unreachable
	// instance cannot hold an HTTP handler open indefinitely.
	statusTimeout = 3 * time.Second
)

// statusClient is the HTTP client used to poll discovered sources.
var statusClient = &http.Client{
	Timeout: statusTimeout,
	// Follow a discovered endpoint only where it advertised itself: a redirect
	// would take the poll somewhere the mDNS record never named.
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// EventKind describes how the registry changed.
type EventKind int

// Registry events.
const (
	// Added means the instance was not known and now has a source.
	Added EventKind = iota
	// Updated means a known instance now points at a different endpoint (or
	// carries a new identity), e.g. because it rebound its health port.
	Updated
	// Retired means the instance's record departed and its source is gone.
	Retired
)

// String renders the event kind for logs and JSON.
func (k EventKind) String() string {
	switch k {
	case Added:
		return "added"
	case Updated:
		return "updated"
	case Retired:
		return "retired"
	default:
		return "unknown"
	}
}

// Event is one change to the registry.
type Event struct {
	Kind   EventKind `json:"kind"`
	Source Source    `json:"source"`
}

// Source is one live channel source: a discovered smoke-alarm instance and the
// health endpoint that backs its channel.
//
// Instance, not Endpoint, is the identity. A member that rebinds its health
// port is the same source at a new address, so it is updated in place rather
// than duplicated; host:port would make every rebind a second channel.
type Source struct {
	Instance     string    `json:"instance"`
	ServiceType  string    `json:"service_type"`
	Endpoint     string    `json:"endpoint"`
	Role         string    `json:"role,omitempty"`
	TrustRung    int       `json:"trust_rung,omitempty"`
	DiscoveredAt time.Time `json:"discovered_at"`
	LastSeen     time.Time `json:"last_seen"`
}

// StatusURL is the endpoint a discovered source is polled at.
func (s Source) StatusURL() string {
	return s.Endpoint + "/status"
}

// Status polls the instance's /status document.
//
// The endpoint is the address the instance advertised via mDNS; there is no
// operator-supplied allowlist to check it against, which is the point of a
// discovered source.
func (s Source) Status(ctx context.Context) (json.RawMessage, error) {
	if s.Endpoint == "" {
		return nil, fmt.Errorf("channel source %q has no endpoint", s.Instance)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.StatusURL(), http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("status request for %q: %w", s.Instance, err)
	}
	resp, err := statusClient.Do(req) //nolint:gosec // G107/G704: the discovered endpoint is the data source by design
	if err != nil {
		return nil, fmt.Errorf("poll %s: %w", s.StatusURL(), err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, statusLimit))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", s.StatusURL(), err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("poll %s: unexpected status %s", s.StatusURL(), resp.Status)
	}
	if !json.Valid(body) {
		return nil, fmt.Errorf("poll %s: response is not JSON", s.StatusURL())
	}
	return json.RawMessage(body), nil
}

// Registry is the set of live channel sources, keyed by instance name.
//
// It is safe for concurrent use. Subscribers are notified after the state has
// changed, so a callback always sees the change it is told about.
type Registry struct {
	mu       sync.RWMutex
	sources  map[string]entry
	subs     map[uint64]func(Event)
	nextSub  uint64
	notifyMu sync.Mutex
}

// entry is a source plus the discovery time of the record currently backing
// its endpoint. The record time, not the browser's arrival order, decides
// whether a later announcement is stale: a rebind keeps the instance's
// original DiscoveredAt so the channel still reports when the instance first
// appeared.
type entry struct {
	src        Source
	recordSeen time.Time
}

// NewRegistry creates an empty source registry.
func NewRegistry() *Registry {
	return &Registry{
		sources: make(map[string]entry),
		subs:    make(map[uint64]func(Event)),
	}
}

// Observe records a service announcement. It reports the event applied and
// whether the service is a channel source at all: only _smoke-alarm._tcp
// records back a /status, so tuner peers and isotope records are ignored here.
//
// An announcement from an older record than the one already registered is
// ignored for the endpoint. That happens when an instance rebinds its health
// port: the new record becomes a second record key in the browser, so after
// the browser expires the old one its retirement must not roll the source back
// to the dead address.
func (r *Registry) Observe(svc mdns.Service) (Event, bool) {
	if svc.ServiceType != mdns.SmokeAlarmServiceType {
		return Event{}, false
	}
	next := sourceFrom(svc)
	if next.Instance == "" {
		return Event{}, false
	}

	now := time.Now()
	recordSeen := next.DiscoveredAt
	if recordSeen.IsZero() {
		// A source added without a discovery time (a hand-built seed) still
		// needs an ordering mark so a later announcement can be compared.
		recordSeen = now
		next.DiscoveredAt = now
	}

	r.mu.Lock()
	existing, found := r.sources[next.Instance]
	changed := false
	switch {
	case found && recordSeen.Before(existing.recordSeen):
		// A stale record for an instance that has already moved. Keep the
		// newer endpoint.
		next = existing.src
	case found:
		next.DiscoveredAt = existing.src.DiscoveredAt
		changed = next.Endpoint != existing.src.Endpoint ||
			next.Role != existing.src.Role ||
			next.TrustRung != existing.src.TrustRung
		r.sources[next.Instance] = entry{src: next, recordSeen: recordSeen}
	default:
		changed = true
		r.sources[next.Instance] = entry{src: next, recordSeen: recordSeen}
	}
	r.mu.Unlock()

	if !changed {
		return Event{Kind: Updated, Source: next}, true
	}

	kind := Added
	if found {
		kind = Updated
	}
	event := Event{Kind: kind, Source: next}
	r.publish(event)
	return event, true
}

// Retire removes the source for a departed record.
//
// A retirement only applies to the endpoint currently registered. When an
// instance rebinds its health port, the old record expires later; that stale
// retirement must not take the live source with it.
func (r *Registry) Retire(svc mdns.Service) (Event, bool) {
	if svc.ServiceType != mdns.SmokeAlarmServiceType {
		return Event{}, false
	}
	instance, endpoint := svc.Instance(), svc.Endpoint()

	r.mu.Lock()
	existing, found := r.sources[instance]
	if !found || existing.src.Endpoint != endpoint {
		r.mu.Unlock()
		return Event{}, false
	}
	delete(r.sources, instance)
	r.mu.Unlock()

	event := Event{Kind: Retired, Source: existing.src}
	r.publish(event)
	return event, true
}

// List returns every live source, ordered by instance so responses are stable.
func (r *Registry) List() []Source {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]Source, 0, len(r.sources))
	for instance := range r.sources {
		out = append(out, r.sources[instance].src)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Instance < out[j].Instance })
	return out
}

// Get returns one source by instance name.
func (r *Registry) Get(instance string) (Source, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	state, ok := r.sources[instance]
	return state.src, ok
}

// Len is the number of live sources.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return len(r.sources)
}

// Subscribe registers fn for registry changes and replays every source already
// known as an Added event, so a subscriber attached after discovery does not
// miss it. The returned function unsubscribes.
//
// fn is called with delivery serialized and must not call Observe, Retire, or
// Subscribe; reading (Get, List, Len) is safe.
func (r *Registry) Subscribe(fn func(Event)) func() {
	if fn == nil {
		return func() {}
	}

	// Hold delivery for the whole registration: a live event cannot be
	// delivered between the snapshot and its replay.
	r.notifyMu.Lock()
	r.mu.Lock()
	r.nextSub++
	id := r.nextSub
	r.subs[id] = fn
	snapshot := make([]Source, 0, len(r.sources))
	for instance := range r.sources {
		snapshot = append(snapshot, r.sources[instance].src)
	}
	r.mu.Unlock()

	sort.Slice(snapshot, func(i, j int) bool { return snapshot[i].Instance < snapshot[j].Instance })
	for _, src := range snapshot {
		fn(Event{Kind: Added, Source: src})
	}
	r.notifyMu.Unlock()

	return func() {
		r.mu.Lock()
		delete(r.subs, id)
		r.mu.Unlock()
	}
}

// publish delivers one event to every subscriber, outside the state lock.
func (r *Registry) publish(event Event) {
	r.notifyMu.Lock()
	defer r.notifyMu.Unlock()

	r.mu.RLock()
	subs := make([]func(Event), 0, len(r.subs))
	for _, fn := range r.subs {
		subs = append(subs, fn)
	}
	r.mu.RUnlock()

	for _, fn := range subs {
		fn(event)
	}
}

// sourceFrom builds the registry entry for a discovered record.
func sourceFrom(svc mdns.Service) Source {
	src := Source{
		Instance:     svc.Instance(),
		ServiceType:  svc.ServiceType,
		Endpoint:     svc.Endpoint(),
		DiscoveredAt: svc.DiscoveredAt,
		LastSeen:     svc.LastSeen,
	}
	if identity, ok := svc.Identity(); ok {
		src.Role = identity.Role
		src.TrustRung = identity.TrustRung
	}
	return src
}
