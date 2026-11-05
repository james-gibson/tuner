// Rendering of the merged certification view. The renderer is deliberately
// data-driven: it shows what adhd released and nothing more. A gated field
// that is absent is not drawn from anywhere else — the visibility tier adhd
// chose is the visibility tier the operator gets, which is what makes
// "tuner never declares its own rung" true on the render side too.
package featurebrowser

import (
	"fmt"
	"strings"

	"github.com/james-gibson/tuner/internal/signal"
)

// rungLabels is the certification ladder the feature file pins.
var rungLabels = map[int]string{
	0: "(unverified)",
	1: "exists",
	2: "deterministic",
	3: "scenario-linked",
}

// nextSteps is the advancement path shown for a skill below rung 3.
var nextSteps = map[int]string{
	0: "probe call required to verify existence",
	1: "equality probe required for deterministic output",
	2: "isotope transit required to bind a scenario",
}

// RungLabel names a certification rung.
func RungLabel(rung int) string {
	if label, ok := rungLabels[rung]; ok {
		return label
	}
	return fmt.Sprintf("rung %d", rung)
}

// Render returns the view as terminal lines for `tuner features` and the
// adhd-features Television channel.
func Render(v View) []string {
	if v.Status == StatusNoSources {
		return renderNoSources(v)
	}

	lines := []string{summaryLine(v.Summary)}

	health := fmt.Sprintf("cluster health: %d/%d features certified, %d/%d skills deterministic",
		v.Summary.FeaturesCertified, v.Summary.FeaturesTotal,
		v.Summary.SkillsDeterministic, v.Summary.SkillsTotal)
	if v.Summary.Distance42i != nil {
		// The cluster-level 42i figure is the worst distance any skill
		// reported — a measurement, not a formula.
		health += fmt.Sprintf(" · 42i: %d", *v.Summary.Distance42i)
	}
	lines = append(lines, health)

	lines = append(lines, fmt.Sprintf("rung %d assigned by adhd · %s", v.Rung, sourceTally(v.Sources)))
	if v.Demo {
		lines = append(lines, "demo cluster: plaintext isotopes, not production evidence")
	}
	if v.Note != "" {
		lines = append(lines, v.Note)
	}

	lines = append(lines, "", "Gherkin Features")
	if len(v.Features) == 0 {
		lines = append(lines, "  (no feature entries released)")
	}
	for _, f := range v.Features {
		lines = append(lines, renderFeature(f))
	}
	lines = append(lines, verifyHint(v)...)

	if len(v.Skills) > 0 || v.UnverifiedSkillCount > 0 {
		lines = append(lines, "", "Cluster Skills")
		for _, s := range v.Skills {
			lines = append(lines, renderSkill(s))
		}
		if v.UnverifiedSkillCount > 0 {
			lines = append(lines, fmt.Sprintf("  %d unverified skill(s) at rung 0 excluded (existence never confirmed)",
				v.UnverifiedSkillCount))
		}
	}

	if len(v.Members) > 0 {
		lines = append(lines, "", "Cluster Members")
		for _, m := range v.Members {
			lines = append(lines, renderMember(m))
		}
	}

	lines = append(lines, "", "Sources")
	for _, src := range v.Sources {
		lines = append(lines, renderSource(src))
	}
	return lines
}

// summaryLine is the channel's first row: "4 / 10 certified (40%)" with a
// progress indicator reflecting the ratio.
func summaryLine(s Summary) string {
	if s.FeaturesTotal == 0 {
		return "0 / 0 certified"
	}
	ratio := float64(s.FeaturesCertified) / float64(s.FeaturesTotal)
	return fmt.Sprintf("%d / %d certified (%.0f%%) [%s]",
		s.FeaturesCertified, s.FeaturesTotal, ratio*100, signal.VizBar(ratio, 10))
}

func sourceTally(sources []Source) string {
	counts := map[SourceState]int{}
	for _, src := range sources {
		counts[src.State]++
	}
	parts := make([]string, 0, len(counts))
	for _, state := range []SourceState{Served, DeclaredAbsent, Unreachable} {
		if counts[state] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[state], state))
		}
	}
	return fmt.Sprintf("%d source(s): %s", len(sources), strings.Join(parts, ", "))
}

func renderFeature(f Feature) string {
	marker := "·"
	if f.Certified {
		marker = "✔"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "  %s %s", marker, f.FeatureFile)
	if f.ScenarioName != "" {
		fmt.Fprintf(&b, " — %s", f.ScenarioName)
	}
	if !f.Certified {
		b.WriteString("   uncertified")
	}
	// The certifying member renders as "certified by alarm-a/t1": the
	// "smoke:" service prefix is the registry's, not the operator's.
	if f.ClusterMember != "" {
		fmt.Fprintf(&b, "   certified by %s", strings.TrimPrefix(f.ClusterMember, "smoke:"))
	}
	if f.IsotopeID != "" {
		fmt.Fprintf(&b, "   isotope %s", f.IsotopeID)
	}
	if f.LastTransit != "" {
		fmt.Fprintf(&b, "   transit %s", f.LastTransit)
	}
	if f.StaleDemoCert {
		b.WriteString("   [stale demo cert]")
	}
	if f.Demo {
		// A demo certification is labelled, never solid: plaintext isotopes
		// are stand-ins, not production evidence.
		b.WriteString("   [demo]")
	}
	if len(f.Sources) > 1 {
		fmt.Fprintf(&b, "   via %s", strings.Join(f.Sources, ", "))
	}
	return b.String()
}

func renderSkill(s Skill) string {
	var b strings.Builder
	fmt.Fprintf(&b, "  %-24s rung %d %s", s.SkillName, s.CertifiedRung, RungLabel(s.CertifiedRung))
	if s.VariationFailures != nil && *s.VariationFailures > 0 {
		fmt.Fprintf(&b, "   %d variation failures", *s.VariationFailures)
	}
	if s.Distance != nil {
		fmt.Fprintf(&b, "   42i: %d", *s.Distance)
	}
	if s.IsotopeBinding != "" {
		fmt.Fprintf(&b, "   binding %s", s.IsotopeBinding)
	}
	if s.HostInstance != "" {
		fmt.Fprintf(&b, "   host %s", s.HostInstance)
	}
	if next, ok := nextSteps[s.CertifiedRung]; ok {
		fmt.Fprintf(&b, "   next: %s", next)
	}
	return b.String()
}

func renderMember(m Member) string {
	var b strings.Builder
	fmt.Fprintf(&b, "  %-16s", m.Name)
	if m.Mode != "" {
		fmt.Fprintf(&b, " %s", m.Mode)
	}
	// The count is a rung 2+ field: it renders when it was released, and its
	// absence below rung 2 is the gate working.
	if m.CertifiedFeatureCount != nil {
		fmt.Fprintf(&b, "   %d features certified", *m.CertifiedFeatureCount)
	}
	if m.TrustRung != nil {
		fmt.Fprintf(&b, "   trust_rung %d", *m.TrustRung)
	}
	if m.Note != "" {
		fmt.Fprintf(&b, "   %s", m.Note)
	}
	return b.String()
}

func renderSource(src Source) string {
	var b strings.Builder
	fmt.Fprintf(&b, "  %-16s %-32s %s", src.Instance, src.Endpoint, src.State)
	switch src.State {
	case Served:
		fmt.Fprintf(&b, " · %d tools", src.Tools)
		if len(src.ToolsServed) > 0 {
			fmt.Fprintf(&b, " · contract: %s", strings.Join(src.ToolsServed, ", "))
		}
		if src.Rung > 0 {
			fmt.Fprintf(&b, " · rung %d assigned", src.Rung)
		}
	default:
		if src.Error != "" {
			fmt.Fprintf(&b, " · %s", src.Error)
		}
	}
	if src.Role != "" {
		fmt.Fprintf(&b, " · role=%s trust_rung=%d", src.Role, src.TrustRung)
	}
	return b.String()
}

// verifyHint is the rung-3 boundary of the verify action: below rung 3 the
// action is not offered at all, and the hint says what is missing; at rung 3
// it names the command. (The action is CLI-only by design — the broadcast
// surface serves the merged read-only view, so a LAN peer cannot trigger
// probes through tuner.)
func verifyHint(v View) []string {
	served := false
	for _, src := range v.Sources {
		if contains(src.ToolsServed, ToolFeaturesVerify) {
			served = true
			break
		}
	}
	if v.Rung < 3 {
		return []string{"  hint: advance to rung 3 to enable live verification"}
	}
	if !served {
		return []string{fmt.Sprintf("  hint: %s is not served by any source (declared absent)", ToolFeaturesVerify)}
	}
	return []string{"  verify: tuner features --verify=<feature>"}
}

func renderNoSources(v View) []string {
	lines := []string{
		"0 / 0 certified",
		ChannelName + ": no sources",
	}
	if v.Note != "" {
		lines = append(lines, v.Note)
	}
	lines = append(lines, DeclaredAbsentNote)
	return lines
}

// RenderVerify renders one verification verdict: "still certified",
// "isotope rotated" (with the previous and current isotope ids, which the
// note carries when the rung permitted them) or "certification lapsed".
func RenderVerify(result VerifyResult) []string {
	lines := []string{fmt.Sprintf("%s: %s (probed via %s)", result.Feature, result.State, result.Source)}
	if result.CurrentIsotope != "" {
		lines = append(lines, fmt.Sprintf("  current isotope: %s", result.CurrentIsotope))
	}
	if result.ProbeTime != "" {
		lines = append(lines, fmt.Sprintf("  probe time: %s", result.ProbeTime))
	}
	if result.Note != "" {
		lines = append(lines, fmt.Sprintf("  note: %s", result.Note))
	}
	return lines
}
