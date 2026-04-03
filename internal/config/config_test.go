package config

import (
	"os"
	"path/filepath"
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
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
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
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
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
