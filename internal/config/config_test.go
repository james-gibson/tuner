package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestDefaults(t *testing.T) {
	cfg := Defaults()

	if cfg.Version != "1.0" {
		t.Fatalf("expected version 1.0, got %q", cfg.Version)
	}
	if cfg.Mode != ModeBroadcast {
		t.Fatalf("expected default mode broadcast, got %q", cfg.Mode)
	}
	if cfg.Health.ListenAddr != "127.0.0.1:58092" {
		t.Fatalf("expected health addr 127.0.0.1:58092, got %q", cfg.Health.ListenAddr)
	}
	if len(cfg.Channels.Preset) != 3 {
		t.Fatalf("expected 3 preset channels, got %d", len(cfg.Channels.Preset))
	}
	names := map[string]bool{}
	for _, p := range cfg.Channels.Preset {
		names[p.Name] = true
	}
	for _, expected := range []string{"ntp", "dns", "ping"} {
		if !names[expected] {
			t.Fatalf("missing preset channel %q", expected)
		}
	}
}

func TestLoadFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.yaml")
	content := `
version: "2.0"
mode: receive
health:
  listen_addr: "0.0.0.0:9999"
channels:
  preset:
    - name: custom1
signal:
  max_age: "15m"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Version != "2.0" {
		t.Fatalf("expected version 2.0, got %q", cfg.Version)
	}
	if cfg.Mode != ModeReceive {
		t.Fatalf("expected mode receive, got %q", cfg.Mode)
	}
	if cfg.Health.ListenAddr != "0.0.0.0:9999" {
		t.Fatalf("expected listen addr 0.0.0.0:9999, got %q", cfg.Health.ListenAddr)
	}
	if len(cfg.Channels.Preset) != 1 {
		t.Fatalf("expected 1 preset, got %d", len(cfg.Channels.Preset))
	}
	if cfg.Channels.Preset[0].Name != "custom1" {
		t.Fatalf("expected preset custom1, got %q", cfg.Channels.Preset[0].Name)
	}
}

func TestLoadFeatureBrowser(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "feature.yaml")
	content := `
mode: broadcast
feature_browser:
  refresh: "10s"
  identity: "tuner:58093"
  endpoints:
    - name: adhd-tui
      url: "http://127.0.0.1:9090"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := cfg.FeatureRefreshDuration(); got != 10*time.Second {
		t.Fatalf("refresh = %v, want 10s", got)
	}
	if got := cfg.FeatureIdentity(); got != "tuner:58093" {
		t.Fatalf("identity = %q, want the configured pin", got)
	}
	if len(cfg.FeatureBrowser.Endpoints) != 1 || cfg.FeatureBrowser.Endpoints[0].URL != "http://127.0.0.1:9090" {
		t.Fatalf("endpoints = %+v", cfg.FeatureBrowser.Endpoints)
	}
	// A config file that omits the block keeps the built-in default.
	defaults, err := Load("")
	if err != nil {
		t.Fatalf("load defaults: %v", err)
	}
	if got := defaults.FeatureRefreshDuration(); got != 30*time.Second {
		t.Fatalf("default refresh = %v, want 30s", got)
	}
}

func TestLoadEmptyPath(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("expected no error for empty path, got %v", err)
	}
	if cfg.Mode != ModeBroadcast {
		t.Fatalf("expected default mode, got %q", cfg.Mode)
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load("/nonexistent/path.yaml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestMaxAgeDuration(t *testing.T) {
	cfg := Defaults()
	d := cfg.MaxAgeDuration()
	if d != 30*time.Minute {
		t.Fatalf("expected 30m, got %v", d)
	}

	cfg.Signal.MaxAge = "15m"
	d = cfg.MaxAgeDuration()
	if d != 15*time.Minute {
		t.Fatalf("expected 15m, got %v", d)
	}

	cfg.Signal.MaxAge = "invalid"
	d = cfg.MaxAgeDuration()
	if d != 30*time.Minute {
		t.Fatalf("expected fallback 30m for invalid, got %v", d)
	}
}

func TestRefreshDuration(t *testing.T) {
	cfg := Defaults()
	d := cfg.RefreshDuration()
	if d != 30*time.Second {
		t.Fatalf("expected 30s, got %v", d)
	}
}

func TestFeatureBrowserDefaults(t *testing.T) {
	cfg := Defaults()
	if got := cfg.FeatureRefreshDuration(); got != 30*time.Second {
		t.Fatalf("default feature refresh = %v, want 30s", got)
	}
	if got := cfg.FeatureIdentity(); got != "tuner" {
		t.Fatalf("default feature identity = %q, want tuner", got)
	}
	if len(cfg.FeatureBrowser.Endpoints) != 0 {
		t.Fatalf("defaults carry endpoints: %+v", cfg.FeatureBrowser.Endpoints)
	}
}

func TestFeatureRefreshDurationFallsBack(t *testing.T) {
	for _, refresh := range []string{"", "soon", "0s", "-5s"} {
		cfg := Defaults()
		cfg.FeatureBrowser.Refresh = refresh
		if got := cfg.FeatureRefreshDuration(); got != 30*time.Second {
			t.Fatalf("refresh %q parsed to %v, want the 30s fallback", refresh, got)
		}
	}
	cfg := Defaults()
	cfg.FeatureBrowser.Refresh = "5s"
	if got := cfg.FeatureRefreshDuration(); got != 5*time.Second {
		t.Fatalf("configured refresh = %v, want 5s", got)
	}
}

func TestFeatureIdentityFromConfig(t *testing.T) {
	cfg := Defaults()
	cfg.FeatureBrowser.Identity = "tuner:58093"
	if got := cfg.FeatureIdentity(); got != "tuner:58093" {
		t.Fatalf("feature identity = %q, want the configured pin", got)
	}
}

func TestServiceTypes(t *testing.T) {
	m := MDNSConfig{ServiceType: "_tuner._tcp"}
	types := m.ServiceTypes()
	if len(types) != 1 || types[0] != "_tuner._tcp" {
		t.Fatalf("expected [_tuner._tcp], got %v", types)
	}

	m2 := MDNSConfig{}
	if types := m2.ServiceTypes(); types != nil {
		t.Fatalf("expected nil for empty service type, got %v", types)
	}
}

// tuner#3: the browse array has one source — own type, smoke-alarm, isotopes —
// instead of being appended together at each call site.
func TestBrowseTypes(t *testing.T) {
	m := MDNSConfig{ServiceType: "_tuner._tcp", IsotopeTypes: []string{"_adhd-isotope._tcp"}}
	want := []string{"_tuner._tcp", "_smoke-alarm._tcp", "_adhd-isotope._tcp"}
	if got := m.BrowseTypes(); !slices.Equal(got, want) {
		t.Fatalf("expected %v, got %v", want, got)
	}

	// No own service type: still the data source plus the isotopes.
	m2 := MDNSConfig{IsotopeTypes: []string{"_adhd-isotope._tcp"}}
	want2 := []string{"_smoke-alarm._tcp", "_adhd-isotope._tcp"}
	if got := m2.BrowseTypes(); !slices.Equal(got, want2) {
		t.Fatalf("expected %v, got %v", want2, got)
	}

	// Declaring a type twice must not browse it twice.
	m3 := MDNSConfig{ServiceType: "_tuner._tcp", IsotopeTypes: []string{"_smoke-alarm._tcp", "_adhd-isotope._tcp"}}
	want3 := []string{"_tuner._tcp", "_smoke-alarm._tcp", "_adhd-isotope._tcp"}
	if got := m3.BrowseTypes(); !slices.Equal(got, want3) {
		t.Fatalf("expected deduped %v, got %v", want3, got)
	}

	// The defaults carry the registered isotope type so an unconfigured
	// tuner still subscribes to the trust-bearing array.
	if got := Defaults().MDNS.BrowseTypes(); !slices.Equal(got, want) {
		t.Fatalf("expected default browse types %v, got %v", want, got)
	}
}

func TestLoadWithCustomChannels(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.yaml")
	content := `
version: "1.0"
mode: broadcast
channels:
  preset:
    - name: ntp
  custom:
    - name: github
      source:
        type: http
        url: "https://api.github.com"
      refresh: "30s"
      theme:
        color: "#FF0000"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.Channels.Custom) != 1 {
		t.Fatalf("expected 1 custom channel, got %d", len(cfg.Channels.Custom))
	}
	c := cfg.Channels.Custom[0]
	if c.Name != "github" {
		t.Fatalf("expected name github, got %q", c.Name)
	}
	if c.Source.Type != "http" {
		t.Fatalf("expected source type http, got %q", c.Source.Type)
	}
	if c.Theme == nil || c.Theme.Color != "#FF0000" {
		t.Fatalf("expected theme color #FF0000, got %v", c.Theme)
	}
}
