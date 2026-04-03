package llms

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseFeatures(t *testing.T) {
	dir := t.TempDir()
	feature := `Feature: Test Broadcasting
  Background:
    Given a tuner

  @health
  Scenario: Health check
    When GET /healthz
    Then status is 200

  @channels
  Scenario: List channels
    When GET /channels
    Then channels are listed

  Scenario Outline: Channel view
    When viewing <channel>
    Then data appears
`
	if err := os.WriteFile(filepath.Join(dir, "test.feature"), []byte(feature), 0644); err != nil {
		t.Fatal(err)
	}

	scenarios, err := ParseFeatures(dir)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(scenarios) != 3 {
		t.Fatalf("expected 3 scenarios, got %d", len(scenarios))
	}
	if scenarios[0].Feature != "Test Broadcasting" {
		t.Fatalf("expected feature name 'Test Broadcasting', got %q", scenarios[0].Feature)
	}
	if scenarios[0].Name != "Health check" {
		t.Fatalf("expected scenario name 'Health check', got %q", scenarios[0].Name)
	}
	if len(scenarios[0].Tags) != 1 || scenarios[0].Tags[0] != "@health" {
		t.Fatalf("expected tag @health, got %v", scenarios[0].Tags)
	}
	if scenarios[2].Name != "Channel view" {
		t.Fatalf("expected scenario outline 'Channel view', got %q", scenarios[2].Name)
	}
}

func TestParseFeaturesEmptyDir(t *testing.T) {
	dir := t.TempDir()
	scenarios, err := ParseFeatures(dir)
	if err != nil {
		t.Fatalf("parse empty: %v", err)
	}
	if len(scenarios) != 0 {
		t.Fatalf("expected 0 scenarios, got %d", len(scenarios))
	}
}

func TestParseFeaturesMissingDir(t *testing.T) {
	_, err := ParseFeatures("/nonexistent/dir")
	if err == nil {
		t.Fatal("expected error for missing dir")
	}
}

func TestGenerateLLMSTxt(t *testing.T) {
	scenarios := []GherkinScenario{
		{Feature: "Broadcasting", Name: "Health check"},
		{Feature: "Broadcasting", Name: "List channels"},
		{Feature: "Receiving", Name: "Discover services"},
	}
	info := RuntimeInfo{
		Mode:          "broadcast",
		HealthAddr:    "127.0.0.1:8092",
		BroadcastAddr: "127.0.0.1:8093",
		Capabilities:  []string{"mDNS discovery", "Signal visualization"},
		Channels: []ChannelInfo{
			{Name: "ntp", Description: "NTP monitoring"},
			{Name: "dns", Description: "DNS health"},
		},
	}

	output := GenerateLLMSTxt(scenarios, info)

	checks := []string{
		"# Tuner",
		"> Passive observer",
		"## Capabilities",
		"- mDNS discovery",
		"- Signal visualization",
		"## Connection",
		"Mode: broadcast",
		"127.0.0.1:8092",
		"127.0.0.1:8093",
		"## Channels",
		"**ntp**",
		"**dns**",
		"## Features",
		"### Broadcasting",
		"- Health check",
		"- List channels",
		"### Receiving",
		"- Discover services",
		"## See Also",
		"tuner-broadcasting.feature",
	}
	for _, check := range checks {
		if !strings.Contains(output, check) {
			t.Errorf("missing in llms.txt output: %q", check)
		}
	}
}

func TestGenerateLLMSTxtNoScenarios(t *testing.T) {
	info := RuntimeInfo{
		Mode:          "receive",
		HealthAddr:    "127.0.0.1:8092",
		BroadcastAddr: "127.0.0.1:8093",
		Capabilities:  []string{"Discovery"},
		Channels:      []ChannelInfo{{Name: "ntp", Description: "NTP"}},
	}

	output := GenerateLLMSTxt(nil, info)

	if strings.Contains(output, "## Features") {
		t.Fatal("should not have Features section with no scenarios")
	}
	if !strings.Contains(output, "## Channels") {
		t.Fatal("should have Channels section")
	}
}
