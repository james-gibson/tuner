package config

import (
	"fmt"
	"os"
	"time"

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
	Version string         `yaml:"version"`
	Mode    string         `yaml:"mode"` // broadcast | receive
	Health  HealthConfig   `yaml:"health"`
	Broadcast BroadcastConfig `yaml:"broadcast"`
	Receive ReceiveConfig  `yaml:"receive"`
	MDNS    MDNSConfig     `yaml:"mdns"`
	Channels ChannelsConfig `yaml:"channels"`
	Signal  SignalConfig    `yaml:"signal"`
	TV      TVConfig       `yaml:"tv"`
	Logging LoggingConfig  `yaml:"logging"`
	Extra   map[string]any `yaml:",inline"`
}

type HealthConfig struct {
	ListenAddr string `yaml:"listen_addr"`
}

type BroadcastConfig struct {
	Listen        string          `yaml:"listen"`
	Advertise     string          `yaml:"advertise"`
	CallerEnabled bool            `yaml:"caller_enabled"`
	Audience      AudienceConfig  `yaml:"audience"`
}

type AudienceConfig struct {
	Enabled   bool   `yaml:"enabled"`
	AllowMDNS string `yaml:"allow_mdns"`
}

type ReceiveConfig struct {
	MDNSService          string `yaml:"mdns_service"`
	PersistSubscriptions string `yaml:"persist_subscriptions"`
}

type MDNSConfig struct {
	ServiceType     string            `yaml:"service_type"`
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

type ChannelsConfig struct {
	Preset []PresetChannel `yaml:"preset"`
	Custom []CustomChannel `yaml:"custom"`
}

type PresetChannel struct {
	Name    string `yaml:"name"`
	Enabled *bool  `yaml:"enabled,omitempty"`
}

type CustomChannel struct {
	Name    string       `yaml:"name"`
	Source  SourceConfig `yaml:"source"`
	Auth    *AuthConfig  `yaml:"auth,omitempty"`
	Refresh string       `yaml:"refresh"`
	Theme   *ThemeConfig `yaml:"theme,omitempty"`
}

type SourceConfig struct {
	Type     string `yaml:"type"` // command | http | sse | webhook
	Command  string `yaml:"command,omitempty"`
	URL      string `yaml:"url,omitempty"`
	Endpoint string `yaml:"endpoint,omitempty"`
}

type AuthConfig struct {
	Type  string `yaml:"type"`
	Token string `yaml:"token,omitempty"`
}

type ThemeConfig struct {
	Color string `yaml:"color"`
	Icon  string `yaml:"icon,omitempty"`
}

type SignalConfig struct {
	MaxAge      string `yaml:"max_age"`
	DecayLinear bool   `yaml:"decay_linear"`
	VizBars     int    `yaml:"viz_bars"`
}

type TVConfig struct {
	CableDir   string `yaml:"cable_dir"`   // default TV cable dir (for reference)
	TunerDir   string `yaml:"tuner_dir"`   // isolated directory for tuner-managed channels
	AutoLaunch bool   `yaml:"auto_launch"`
	Isolate    bool   `yaml:"isolate"`     // if true, use tuner_dir exclusively via --cable-dir
}

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
	data, err := os.ReadFile(path)
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
				AllowMDNS: "_smoke-alarm._tcp",
			},
		},
		Receive: ReceiveConfig{
			MDNSService:          "_tuner._tcp",
			PersistSubscriptions: "~/.config/tuner/subscriptions.json",
		},
		MDNS: MDNSConfig{
			ServiceType:     "_tuner._tcp",
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
