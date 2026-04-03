package tv

import (
	"strings"
	"testing"
)

func TestBuiltinPresetsCount(t *testing.T) {
	presets := BuiltinPresets()
	if len(presets) != 4 {
		t.Fatalf("expected 4 presets, got %d", len(presets))
	}
}

func TestBuiltinPresetsHaveRequiredFields(t *testing.T) {
	for name, p := range BuiltinPresets() {
		if p.Name == "" {
			t.Fatalf("preset %q has empty Name", name)
		}
		if p.Description == "" {
			t.Fatalf("preset %q has empty Description", name)
		}
		if p.Command == "" {
			t.Fatalf("preset %q has empty Command", name)
		}
		if p.Watch <= 0 {
			t.Fatalf("preset %q has non-positive Watch: %f", name, p.Watch)
		}
		if p.Preview == "" {
			t.Fatalf("preset %q has empty Preview", name)
		}
	}
}

func TestBuiltinPresetsContainExpected(t *testing.T) {
	presets := BuiltinPresets()
	for _, expected := range []string{"ntp", "dns", "ping"} {
		if _, ok := presets[expected]; !ok {
			t.Fatalf("missing expected preset %q", expected)
		}
	}
}

func TestHasTunerViz(t *testing.T) {
	// In CI / dev without Rust, this should return false.
	// We just verify it doesn't panic.
	_ = HasTunerViz()
}

func TestSignalPresetFallback(t *testing.T) {
	// Without tuner-viz on PATH, should produce a fallback preset.
	p := SignalPreset("http://127.0.0.1:18088")
	if p.Name != "signal" {
		t.Fatalf("expected name signal, got %q", p.Name)
	}
	if p.Command == "" {
		t.Fatal("expected non-empty command")
	}
	if p.Watch <= 0 {
		t.Fatalf("expected positive watch, got %f", p.Watch)
	}
	// If tuner-viz not on PATH, command should NOT contain "tuner-viz".
	if !HasTunerViz() && strings.Contains(p.Command, "tuner-viz") {
		t.Fatal("fallback command should not reference tuner-viz")
	}
}

func TestTopologyPresetFallback(t *testing.T) {
	p := TopologyPreset("http://127.0.0.1:18088")
	if p.Name != "federation" {
		t.Fatalf("expected name federation, got %q", p.Name)
	}
	if p.Command == "" {
		t.Fatal("expected non-empty command")
	}
	if !HasTunerViz() && strings.Contains(p.Command, "tuner-viz") {
		t.Fatal("fallback command should not reference tuner-viz")
	}
}

func TestSignalPresetWithViz(t *testing.T) {
	// If tuner-viz IS available, verify it's used.
	if !HasTunerViz() {
		t.Skip("tuner-viz not on PATH")
	}
	p := SignalPreset("http://127.0.0.1:18088")
	if !strings.Contains(p.Command, "tuner-viz") {
		t.Fatal("with tuner-viz on PATH, command should use tuner-viz")
	}
	if !strings.Contains(p.Description, "tuner-viz") {
		t.Fatal("with tuner-viz on PATH, description should mention tuner-viz")
	}
}

func TestSignalPresetContainsEndpoint(t *testing.T) {
	endpoint := "http://192.168.1.50:18088"
	p := SignalPreset(endpoint)
	if !strings.Contains(p.Command, endpoint) {
		t.Fatalf("expected endpoint %q in command, got %q", endpoint, p.Command)
	}
}

func TestTopologyPresetContainsEndpoint(t *testing.T) {
	endpoint := "http://192.168.1.50:18088"
	p := TopologyPreset(endpoint)
	if !strings.Contains(p.Command, endpoint) {
		t.Fatalf("expected endpoint %q in command, got %q", endpoint, p.Command)
	}
}
