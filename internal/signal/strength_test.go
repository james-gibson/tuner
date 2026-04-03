package signal

import (
	"math"
	"testing"
	"time"
)

func TestStrengthCriticalFresh(t *testing.T) {
	alert := Alert{
		Severity:    "critical",
		TriggeredAt: time.Now(),
	}
	s := Strength(alert, 30*time.Minute)
	if s < 0.99 {
		t.Fatalf("expected ~1.0 for fresh critical, got %f", s)
	}
}

func TestStrengthCriticalHalfway(t *testing.T) {
	alert := Alert{
		Severity:    "critical",
		TriggeredAt: time.Now().Add(-15 * time.Minute),
	}
	s := Strength(alert, 30*time.Minute)
	if math.Abs(s-0.5) > 0.02 {
		t.Fatalf("expected ~0.5 for 15m old critical with 30m decay, got %f", s)
	}
}

func TestStrengthExpired(t *testing.T) {
	alert := Alert{
		Severity:    "critical",
		TriggeredAt: time.Now().Add(-31 * time.Minute),
	}
	s := Strength(alert, 30*time.Minute)
	if s != 0 {
		t.Fatalf("expected 0 for expired alert, got %f", s)
	}
}

func TestStrengthWarn(t *testing.T) {
	alert := Alert{
		Severity:    "warn",
		TriggeredAt: time.Now(),
	}
	s := Strength(alert, 30*time.Minute)
	if math.Abs(s-0.6) > 0.02 {
		t.Fatalf("expected ~0.6 for fresh warn, got %f", s)
	}
}

func TestStrengthInfo(t *testing.T) {
	alert := Alert{
		Severity:    "info",
		TriggeredAt: time.Now(),
	}
	s := Strength(alert, 30*time.Minute)
	if math.Abs(s-0.3) > 0.02 {
		t.Fatalf("expected ~0.3 for fresh info, got %f", s)
	}
}

func TestStrengthUnknownSeverity(t *testing.T) {
	alert := Alert{
		Severity:    "unknown",
		TriggeredAt: time.Now(),
	}
	s := Strength(alert, 30*time.Minute)
	// Defaults to info weight (0.3)
	if math.Abs(s-0.3) > 0.02 {
		t.Fatalf("expected ~0.3 for unknown severity, got %f", s)
	}
}

func TestVizBarFull(t *testing.T) {
	bar := VizBar(1.0, 12)
	if bar != "████████████" {
		t.Fatalf("expected full bar, got %q", bar)
	}
}

func TestVizBarHalf(t *testing.T) {
	bar := VizBar(0.5, 12)
	if bar != "██████░░░░░░" {
		t.Fatalf("expected half bar, got %q", bar)
	}
}

func TestVizBarEmpty(t *testing.T) {
	bar := VizBar(0.0, 12)
	if bar != "░░░░░░░░░░░░" {
		t.Fatalf("expected empty bar, got %q", bar)
	}
}

func TestVizBarOverflow(t *testing.T) {
	bar := VizBar(1.5, 12)
	if bar != "████████████" {
		t.Fatalf("expected clamped full bar, got %q", bar)
	}
}

func TestVizBarNegative(t *testing.T) {
	bar := VizBar(-0.5, 12)
	if bar != "░░░░░░░░░░░░" {
		t.Fatalf("expected empty bar for negative, got %q", bar)
	}
}

func TestStrengthWarning(t *testing.T) {
	// Test "warning" alias
	alert := Alert{
		Severity:    "warning",
		TriggeredAt: time.Now(),
	}
	s := Strength(alert, 30*time.Minute)
	if math.Abs(s-0.6) > 0.02 {
		t.Fatalf("expected ~0.6 for fresh warning, got %f", s)
	}
}
