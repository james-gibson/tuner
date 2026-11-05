package config

import (
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/james-gibson/tuner/internal/mdns"
	"gopkg.in/yaml.v3"
)

// Modes
const (
	ModeBroadcast = "broadcast"
	ModeReceive   = "receive"
)

// Source types for custom channels.
const (
	SourceCommand = "command"
	SourceHTTP    = "http"
	SourceSSE     = "sse"
	SourceWebhook = "webhook"
)

// Config is the root configuration.
type Config struct {
	Version        string               `yaml:"version"`
	Mode           string               `yaml:"mode"` // broadcast | receive
	Health         HealthConfig         `yaml:"health"`
	Broadcast      BroadcastConfig      `yaml:"broadcast"`
	Receive        ReceiveConfig        `yaml:"receive"`
	MDNS           MDNSConfig           `yaml:"mdns"`
	Channels       ChannelsConfig       `yaml:"channels"`
	FeatureBrowser FeatureBrowserConfig `yaml:"feature_browser"`
	Signal         SignalConfig         `yaml:"signal"`
	TV             TVConfig             `yaml:"tv"`
	Logging        LoggingConfig        `yaml:"logging"`
	Extra          map[string]any       `yaml:",inline"`
}

// HealthConfig configures the local health/status HTTP server.
type HealthConfig struct {
	ListenAddr string `yaml:"listen_addr"`
}

// BroadcastConfig configures broadcast mode.
type BroadcastConfig struct {
	Listen        string         `yaml:"listen"`
	Advertise     string         `yaml:"advertise"`
	CallerEnabled bool           `yaml:"caller_enabled"`
	Audience      AudienceConfig `yaml:"audience"`
}

// AudienceConfig configures audience metric ingestion.
type AudienceConfig struct {
	Enabled   bool   `yaml:"enabled"`
	AllowMDNS string `yaml:"allow_mdns"`
}

// ReceiveConfig configures receive mode.
type ReceiveConfig struct {
	MDNSService          string `yaml:"mdns_service"`
	PersistSubscriptions string `yaml:"persist_subscriptions"`
}

// MDNSConfig configures mDNS advertisement and discovery.
type MDNSConfig struct {
	ServiceType     string            `yaml:"service_type"`
	IsotopeTypes    []string          `yaml:"isotope_types"`
	Port            int               `yaml:"port"`
	TXTRecord       map[string]string `yaml:"txt_record"`
	RefreshInterval string            `yaml:"refresh_interval"`
	Domains         []string          `yaml:"domains"`
}

// ServiceTypes returns the service type as a slice for browsing.
func (m MDNSConfig) ServiceTypes() []string {
	if m.ServiceType == "" {
		return nil
	}
	return []string{m.ServiceType}
}

// BrowseTypes returns every service type the browser subscribes to: tuner's
// own type (peers), smoke-alarm (the data source), and the registered isotope
// types whose TXT records carry role and trust_rung.
//
// This is the single source for the browse array. Assembling that array ad hoc
// at each call site is what let one type silently consume another's scan
// budget (tuner#3), and it left "what tuner trusts" unrepresentable: the
// trust-bearing set is the registered isotope types, not the bare tool names.
func (m MDNSConfig) BrowseTypes() []string {
	var out []string
	for _, candidates := range [][]string{
		m.ServiceTypes(),
		{mdns.SmokeAlarmServiceType},
		m.IsotopeTypes,
	} {
		for _, svcType := range candidates {
			if svcType == "" || slices.Contains(out, svcType) {
				continue
			}
			out = append(out, svcType)
		}
	}
	return out
}

// ChannelsConfig lists the built-in and custom channels.
type ChannelsConfig struct {
	Preset []PresetChannel `yaml:"preset"`
	Custom []CustomChannel `yaml:"custom"`
	Notes  []NotesChannel  `yaml:"notes"`
}

// NotesChannel is an RSS-shaped feed of prep notes — questions worth asking and
// gaps worth expecting. The content stays where it is authored; tuner surfaces
// the latest items to whoever is watching.
type NotesChannel struct {
	Name    string `yaml:"name"`
	Source  string `yaml:"source"`  // path, file://, http(s)://
	Refresh string `yaml:"refresh"` // e.g. "30s"; the cache lifetime
}

// PresetChannel selects a built-in channel by name.
type PresetChannel struct {
	Name    string `yaml:"name"`
	Enabled *bool  `yaml:"enabled,omitempty"`
}

// CustomChannel defines a user-supplied channel and its data source.
type CustomChannel struct {
	Name    string       `yaml:"name"`
	Source  SourceConfig `yaml:"source"`
	Auth    *AuthConfig  `yaml:"auth,omitempty"`
	Refresh string       `yaml:"refresh"`
	Theme   *ThemeConfig `yaml:"theme,omitempty"`
}

// SourceConfig describes where a custom channel reads its data from.
type SourceConfig struct {
	Type     string `yaml:"type"` // command | http | sse | webhook
	Command  string `yaml:"command,omitempty"`
	URL      string `yaml:"url,omitempty"`
	Endpoint string `yaml:"endpoint,omitempty"`
}

// AuthConfig holds credentials for a custom channel source.
type AuthConfig struct {
	Type  string `yaml:"type"`
	Token string `yaml:"token,omitempty"`
}

// ThemeConfig customizes a channel's appearance.
type ThemeConfig struct {
	Color string `yaml:"color"`
	Icon  string `yaml:"icon,omitempty"`
}

// FeatureBrowserConfig configures the adhd certification browser (tuner#37):
// the merged feature + skill certification view published as the adhd-features
// channel. Discovery over mDNS is the primary source set; endpoints are the
// fallback for when mDNS is unavailable (features/tuner-feature-browser.feature).
type FeatureBrowserConfig struct {
	// Endpoints are direct adhd MCP endpoints, e.g. http://127.0.0.1:9090/mcp.
	// A bare origin gets the conventional /mcp path.
	Endpoints []FeatureEndpoint `yaml:"endpoints"`
	// Refresh is the merged-view cache lifetime, e.g. "30s".
	Refresh string `yaml:"refresh"`
	// Identity pins the tuner instance id sent with each MCP handshake, for
	// an operator whose adhd registry knows this tuner under a specific id.
	// Never a rung — adhd assigns rungs; tuner only names itself.
	Identity string `yaml:"identity"`
}

// FeatureEndpoint is one configured adhd MCP endpoint.
type FeatureEndpoint struct {
	Name string `yaml:"name"`
	URL  string `yaml:"url"`
}

// SignalConfig configures alert signal strength and decay.
type SignalConfig struct {
	MaxAge      string `yaml:"max_age"`
	DecayLinear bool   `yaml:"decay_linear"`
	VizBars     int    `yaml:"viz_bars"`
}

// TVConfig configures integration with Television.
type TVConfig struct {
	CableDir   string `yaml:"cable_dir"` // default TV cable dir (for reference)
	TunerDir   string `yaml:"tuner_dir"` // isolated directory for tuner-managed channels
	AutoLaunch bool   `yaml:"auto_launch"`
	Isolate    bool   `yaml:"isolate"` // if true, use tuner_dir exclusively via --cable-dir
}

// LoggingConfig configures log level and output.
type LoggingConfig struct {
	Level  string `yaml:"level"`
	Output string `yaml:"output"`
}

// Load reads a YAML config file. Returns defaults if path is empty.
func Load(path string) (*Config, error) {
	cfg := Defaults()
	if path == "" {
		return cfg, nil
	}
	// The config path is supplied by the operator on the command line; reading it
	// from an arbitrary location is the documented behavior of Load.
	data, err := os.ReadFile(path) //nolint:gosec // G304: operator-supplied config path by design
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return cfg, nil
}

// Defaults returns a Config with sensible defaults.
func Defaults() *Config {
	return &Config{
		Version: "1.0",
		Mode:    ModeBroadcast,
		Health: HealthConfig{
			ListenAddr: "127.0.0.1:58092",
		},
		Broadcast: BroadcastConfig{
			Listen:        "127.0.0.1:58093",
			Advertise:     "_tuner._tcp",
			CallerEnabled: true,
			Audience: AudienceConfig{
				Enabled:   true,
				AllowMDNS: mdns.SmokeAlarmServiceType,
			},
		},
		Receive: ReceiveConfig{
			MDNSService:          "_tuner._tcp",
			PersistSubscriptions: "~/.config/tuner/subscriptions.json",
		},
		MDNS: MDNSConfig{
			ServiceType: "_tuner._tcp",
			// Registered isotope types to browse. Each carries role and
			// trust_rung in its TXT record (isotope.Advertise); today only
			// adhd registers one. A config file overrides this list; an
			// explicit "isotope_types: []" opts out, an absent key keeps it.
			IsotopeTypes:    []string{"_adhd-isotope._tcp"},
			Port:            58093,
			RefreshInterval: "30s",
			Domains:         []string{"local"},
			TXTRecord:       map[string]string{"version": "1.0"},
		},
		Channels: ChannelsConfig{
			Preset: []PresetChannel{
				{Name: "ntp"},
				{Name: "dns"},
				{Name: "ping"},
			},
		},
		FeatureBrowser: FeatureBrowserConfig{
			Refresh: "30s",
		},
		Signal: SignalConfig{
			MaxAge:      "30m",
			DecayLinear: true,
			VizBars:     12,
		},
		TV: TVConfig{
			CableDir:   "~/.config/television/cable",
			TunerDir:   "~/.config/tuner/cable",
			AutoLaunch: true,
			Isolate:    true, // keep tuner channels separate from TV defaults
		},
		Logging: LoggingConfig{
			Level:  "info",
			Output: "console",
		},
	}
}

// MaxAgeDuration parses the signal max_age as a duration.
func (c *Config) MaxAgeDuration() time.Duration {
	d, err := time.ParseDuration(c.Signal.MaxAge)
	if err != nil {
		return 30 * time.Minute
	}
	return d
}

// RefreshDuration parses the mDNS refresh interval.
func (c *Config) RefreshDuration() time.Duration {
	d, err := time.ParseDuration(c.MDNS.RefreshInterval)
	if err != nil {
		return 30 * time.Second
	}
	return d
}

// FeatureRefreshDuration parses the feature browser's merged-view cache
// lifetime. An absent or unparseable value falls back to the built-in 30s, so
// the channel answers even when the config file omits feature_browser.
func (c *Config) FeatureRefreshDuration() time.Duration {
	d, err := time.ParseDuration(c.FeatureBrowser.Refresh)
	if err != nil || d <= 0 {
		return 30 * time.Second
	}
	return d
}

// FeatureIdentity returns the instance id tuner presents in the MCP handshake.
// The handshake carries an identity, never a rung: adhd looks the identity up
// in its registry and decides what the caller may see. The configured pin
// exists for an operator whose registry knows this tuner by a specific id.
func (c *Config) FeatureIdentity() string {
	if id := c.FeatureBrowser.Identity; id != "" {
		return id
	}
	return "tuner"
}
