// Package lezzdemo discovers smoke-alarm services from a running lezz demo
// cluster registry. It is a fallback for when mDNS is unavailable or slow.
package lezzdemo

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/james-gibson/tuner/internal/mdns"
)

// RegistryPort is the well-known port where lezz demo hosts its /cluster registry.
const RegistryPort = 19100

type clusterInfo struct {
	Name   string `json:"name"`
	AlarmA string `json:"alarm_a"`
	AlarmB string `json:"alarm_b"`
}

// SmokeAlarms fetches localhost:19100/cluster and returns mdns.Service entries
// for every alarm_a and alarm_b endpoint found. Returns nil if the registry is
// unreachable or empty — callers should treat this as "no demo running".
func SmokeAlarms() []mdns.Service {
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/cluster", RegistryPort))
	if err != nil {
		return nil
	}
	defer func() { _ = resp.Body.Close() }()

	var registry map[string]clusterInfo
	if err := json.NewDecoder(resp.Body).Decode(&registry); err != nil {
		return nil
	}

	now := time.Now()
	var services []mdns.Service
	for _, cluster := range registry {
		for _, entry := range []struct{ name, rawURL string }{
			{cluster.Name + "/alarm-a", cluster.AlarmA},
			{cluster.Name + "/alarm-b", cluster.AlarmB},
		} {
			if entry.rawURL == "" {
				continue
			}
			u, err := url.Parse(entry.rawURL)
			if err != nil {
				continue
			}
			port, err := strconv.Atoi(u.Port())
			if err != nil {
				continue
			}
			services = append(services, mdns.Service{
				Name:         entry.name,
				ServiceType:  "_smoke-alarm._tcp",
				Host:         u.Hostname(),
				Port:         port,
				TXT:          map[string]string{"source": "lezz-demo"},
				DiscoveredAt: now,
				LastSeen:     now,
			})
		}
	}
	return services
}

// Seed injects any smoke alarms from the local lezz demo registry into b.
// It is a no-op if the registry is unreachable.
func Seed(b *mdns.Browser) []mdns.Service {
	svcs := SmokeAlarms()
	for _, svc := range svcs {
		b.Add(svc)
	}
	return svcs
}
