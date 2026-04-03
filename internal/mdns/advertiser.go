package mdns

import (
	"context"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"

	"github.com/grandcat/zeroconf"
)

// AdvertiserOptions configures the mDNS advertiser.
type AdvertiserOptions struct {
	ServiceName string
	ServiceType string
	Domain      string
	Port        int
	TXT         map[string]string
}

// Advertiser registers a Tuner service on the local network via mDNS.
// Only used in broadcast mode.
type Advertiser struct {
	opts   AdvertiserOptions
	server *zeroconf.Server
}

// NewAdvertiser creates a new mDNS advertiser.
func NewAdvertiser(opts AdvertiserOptions) *Advertiser {
	if opts.Domain == "" {
		opts.Domain = "local."
	} else if !strings.HasSuffix(opts.Domain, ".") {
		// Ensure domain is fully qualified with trailing dot
		opts.Domain = opts.Domain + "."
	}
	return &Advertiser{opts: opts}
}

// Start begins mDNS advertisement. The service is discoverable until Shutdown is called.
func (a *Advertiser) Start(ctx context.Context) error {
	txtRecords := formatTXT(a.opts.TXT)

	// Use nil for interfaces - let zeroconf auto-select.
	// Passing specific interfaces can cause "dns: domain must be fully qualified" errors.
	server, err := zeroconf.Register(
		a.opts.ServiceName,
		a.opts.ServiceType,
		a.opts.Domain,
		a.opts.Port,
		txtRecords,
		nil,
	)
	if err != nil {
		return fmt.Errorf("mdns register: %w", err)
	}
	a.server = server

	log.Printf("mdns advertiser: advertising %s.%s on port %d (txt: %v)",
		a.opts.ServiceType, a.opts.Domain, a.opts.Port, txtRecords)

	go func() {
		<-ctx.Done()
		a.Shutdown()
	}()

	return nil
}

// Shutdown stops the mDNS advertisement.
func (a *Advertiser) Shutdown() {
	if a.server != nil {
		a.server.Shutdown()
		a.server = nil
	}
	log.Println("mdns advertiser: stopped")
}

// ParsePort extracts port from "host:port".
func ParsePort(addr string) int {
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return 0
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 0
	}
	return port
}

// ServiceID returns a formatted identifier.
func (a *Advertiser) ServiceID() string {
	return fmt.Sprintf("%s.%s:%d", a.opts.ServiceType, a.opts.Domain, a.opts.Port)
}

func formatTXT(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, strings.Join([]string{k, v}, "="))
	}
	return out
}
