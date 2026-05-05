package tv

import (
	"os/exec"
)

// Preset defines a built-in channel template.
type Preset struct {
	Name        string
	Description string
	Command     string
	Watch       float64 // refresh interval in seconds
	Preview     string
}

// HasTunerViz checks if the tuner-viz Rust binary is on PATH.
func HasTunerViz() bool {
	_, err := exec.LookPath("tuner-viz")
	return err == nil
}

// BuiltinPresets returns the default filler channels.
// These provide live network data until real smoke-alarm channels come online.
// If tuner-viz is available, signal-based presets use it for rich rendering.
func BuiltinPresets() map[string]Preset {
	return map[string]Preset{
		"mdns": {
			Name:        "mdns",
			Description: "Real-time mDNS Network Services",
			Command:     `tuner mdns --duration=5s --services=_smoke-alarm._tcp,_tuner._tcp,_http._tcp,_https._tcp,_ssh._tcp,_smb._tcp,_afpovertcp._tcp,_airplay._tcp,_raop._tcp,_homekit._tcp,_googlecast._tcp,_spotify-connect._tcp`,
			Watch:       8.0,
			Preview:     `tuner mdns --duration=2s --format=json`,
		},
		"ntp": {
			Name:        "ntp",
			Description: "NTP Time Sync - Latency & Drift",
			Command:     `date -u "+%Y-%m-%d %H:%M:%S UTC" && (sntp -t 2 time.apple.com 2>&1 || ntpdate -q time.apple.com 2>&1 || echo "ntp: checking system time only")`,
			Watch:       10.0,
			Preview:     `date -u "+%Y-%m-%d %H:%M:%S UTC"`,
		},
		"dns": {
			Name:        "dns",
			Description: "DNS Resolution Health",
			Command:     `echo "DNS Resolution Test - $(date +%H:%M:%S)" && dig +short +time=2 +tries=1 example.com A 2>&1 && dig +short +time=2 +tries=1 google.com A 2>&1 && dig +short +time=2 +tries=1 cloudflare.com A 2>&1 || echo "dns: dig not available, trying host" && host example.com 2>&1`,
			Watch:       15.0,
			Preview:     `dig +stats +time=2 example.com 2>&1 | grep -E "Query time|SERVER|status" || host example.com 2>&1`,
		},
		"ping": {
			Name:        "ping",
			Description: "ICMP Latency Monitor",
			Command:     `echo "ICMP Latency - $(date +%H:%M:%S)" && ping -c 3 -W 2 1.1.1.1 2>&1 | tail -5`,
			Watch:       10.0,
			Preview:     `ping -c 1 -W 2 8.8.8.8 2>&1 | tail -3`,
		},
		"ocd-signal": SignalPreset("http://localhost:17091"),
		"ocd-topology": SignalPreset("http://localhost:17091"),
	}
}

// SignalPreset returns a TV channel preset for alert signal visualization.
// Uses tuner-viz if available, falls back to Go ASCII rendering via the
// tuner binary's built-in signal command.
func SignalPreset(endpoint string) Preset {
	if HasTunerViz() {
		return Preset{
			Name:        "signal",
			Description: "Alert Signal Strength (tuner-viz)",
			Command:     `curl -s ` + endpoint + `/status | tuner-viz signal --stdin --preview`,
			Watch:       5.0,
			Preview:     `curl -s ` + endpoint + `/status | tuner-viz signal --stdin --width 24 --preview`,
		}
	}
	// ASCII fallback via Go.
	return Preset{
		Name:        "signal",
		Description: "Alert Signal Strength",
		Command:     `curl -s ` + endpoint + `/status`,
		Watch:       5.0,
		Preview:     `curl -s ` + endpoint + `/status`,
	}
}

// TopologyPreset returns a TV channel for federation topology visualization.
func TopologyPreset(endpoint string) Preset {
	if HasTunerViz() {
		return Preset{
			Name:        "federation",
			Description: "Federation Topology (tuner-viz)",
			Command:     `curl -s ` + endpoint + `/hosted/status | tuner-viz topology --stdin`,
			Watch:       10.0,
			Preview:     `curl -s ` + endpoint + `/hosted/status | tuner-viz topology --stdin`,
		}
	}
	return Preset{
		Name:        "federation",
		Description: "Federation Topology",
		Command:     `curl -s ` + endpoint + `/hosted/status`,
		Watch:       10.0,
		Preview:     `curl -s ` + endpoint + `/hosted/status`,
	}
}
