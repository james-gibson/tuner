// Package featurebrowser publishes the cluster's certification state: it
// connects to every adhd instance tuner can see, calls the declared
// certification tools over MCP, and merges the answers into one view that the
// adhd-features channel, the CLI and the Television preset render
// (tuner#37, features/tuner-feature-browser.feature).
//
// The roles: smoke-alarm certifies rungs, adhd exposes the certified surface,
// tuner publishes it. Two rules keep the publication honest:
//
//   - Tuner never declares its own rung. It sends its instance identity with
//     the handshake; adhd looks the identity up in its registry and filters the
//     response. What the renderer shows is what adhd released — the effective
//     rung in a View is read back from the responses, never configured.
//   - "Declared but not served" is an answer, not an error. adhd.features.*
//     and adhd.skills.certified are declared absent by policy (adhd#24 closed
//     @red, adhd#39 open, lab-safety sheets/adhd.md §16). A source whose
//     tools/list omits them is reported declared-absent, and the channel says
//     so plainly — the same fail-closed honesty with which /channels marks a
//     misconfigured channel (tuner#30).
package featurebrowser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/james-gibson/tuner/internal/config"
	"github.com/james-gibson/tuner/internal/mcpclient"
	"github.com/james-gibson/tuner/internal/mdns"
)

// ChannelName is the broadcast channel and Television preset this browser
// feeds. One name, three surfaces: GET /channels/adhd-features, the TV TOML,
// and `tuner features`.
const ChannelName = "adhd-features"

// The declared certification contract (adhd/features/adhd/certified-features.feature,
// adhd/features/adhd/skill-certification.feature). Declared absent today; the
// browser handles their absence as a first-class state.
const (
	ToolFeaturesCertified = "adhd.features.certified"
	ToolFeaturesVerify    = "adhd.features.verify"
	ToolFeaturesCluster   = "adhd.features.cluster"
	ToolSkillsCertified   = "adhd.skills.certified"
)

// contractTools are the read tools the merged view is built from, in the order
// they are called and reported.
var contractTools = []string{ToolFeaturesCertified, ToolSkillsCertified, ToolFeaturesCluster}

// DefaultRefresh is the cache lifetime of the merged view.
const DefaultRefresh = 30 * time.Second

// SourceState is how one adhd instance answered.
type SourceState string

// Source states.
const (
	// Served means the handshake succeeded and at least one contract tool
	// answered.
	Served SourceState = "served"
	// DeclaredAbsent means the endpoint speaks MCP but serves none of the
	// contract tools: declared, absent by policy — a normal answer.
	DeclaredAbsent SourceState = "declared-absent"
	// Unreachable means the endpoint did not answer the handshake at all.
	Unreachable SourceState = "unreachable"
)

// View statuses: the roll-up of the source states.
const (
	StatusServed         = "served"
	StatusDeclaredAbsent = "declared-absent"
	StatusUnreachable    = "unreachable"
	StatusNoSources      = "no-sources"
)

// Source is one adhd instance the browser can call.
type Source struct {
	// Instance is the DNS-SD label (discovered) or the configured name.
	Instance string `json:"instance"`
	// Endpoint is the MCP URL, e.g. http://127.0.0.1:9090/mcp.
	Endpoint string `json:"endpoint"`
	// Origin is where the source came from: "mdns", "config" or "flag".
	Origin string `json:"origin"`
	// Role and TrustRung are the instance's own advertised isotope identity
	// (TXT role=/trust_rung=). They describe adhd, never tuner.
	Role      string `json:"role,omitempty"`
	TrustRung int    `json:"trust_rung,omitempty"`

	// State is how the instance answered the last fetch.
	State SourceState `json:"state"`
	// Tools is how many tools its tools/list served.
	Tools int `json:"tools,omitempty"`
	// ToolsServed lists which contract tools were in tools/list.
	ToolsServed []string `json:"tools_served,omitempty"`
	// Rung is the caller rung this instance's responses evidenced for tuner:
	// reported explicitly by adhd, or inferred from the gated fields it
	// released. 0 when nothing evidenced more. Never configured.
	Rung int `json:"rung,omitempty"`
	// Demo is set when the instance evidenced a demo cluster (mode=demo,
	// plaintext-isotope notes, stale demo certs).
	Demo bool `json:"demo,omitempty"`
	// Error carries the failure detail when unreachable, or a refusal.
	Error string `json:"error,omitempty"`
	// Fetched is when this source was last called.
	Fetched time.Time `json:"fetched,omitempty"`
}

// Feature is one Gherkin feature/scenario entry of adhd.features.certified,
// merged across sources. The gated fields are exactly what adhd released for
// tuner's rung — the renderer shows them, and never invents them.
type Feature struct {
	FeatureFile   string `json:"feature_file"`
	ScenarioName  string `json:"scenario_name,omitempty"`
	Certified     bool   `json:"certified"`
	IsotopeID     string `json:"isotope_id,omitempty"`     // rung 2+
	LastTransit   string `json:"last_transit,omitempty"`   // rung 2+
	ClusterMember string `json:"cluster_member,omitempty"` // rung 3
	StaleDemoCert bool   `json:"stale_demo_cert,omitempty"`

	// Demo labels a certification whose every certifying source evidenced a
	// demo cluster: plaintext isotopes, not production evidence.
	Demo bool `json:"demo,omitempty"`
	// Sources is the federation trail: every instance that reported the
	// entry, certifying sources first.
	Sources []string `json:"sources,omitempty"`
}

// Skill is one entry of adhd.skills.certified, merged across sources.
type Skill struct {
	SkillName      string `json:"skill_name"`
	CertifiedRung  int    `json:"certified_rung"`
	Distance       *int   `json:"distance,omitempty"`        // rung 2+, 42i distance
	HostInstance   string `json:"host_instance,omitempty"`   // rung 2+
	IsotopeBinding string `json:"isotope_binding,omitempty"` // rung 3
	// VariationFailures is the worst count any source reported.
	VariationFailures *int `json:"variation_failures,omitempty"`

	Sources []string `json:"sources,omitempty"`
}

// Member is one cluster member of adhd.features.cluster, merged across
// sources.
type Member struct {
	Name                  string `json:"name"`
	Mode                  string `json:"mode,omitempty"` // demo | real
	TrustRung             *int   `json:"trust_rung,omitempty"`
	CertifiedFeatureCount *int   `json:"certified_feature_count,omitempty"` // rung 2+
	Note                  string `json:"note,omitempty"`

	Sources []string `json:"sources,omitempty"`
}

// Summary is the "4 / 10 certified" roll-up over the merged view.
type Summary struct {
	FeaturesCertified int `json:"features_certified"`
	FeaturesTotal     int `json:"features_total"`
	// SkillsDeterministic counts skills at rung >= 2 (equality verified).
	SkillsDeterministic int `json:"skills_deterministic"`
	SkillsTotal         int `json:"skills_total"`
	// Distance42i is the worst 42i distance any skill reported, when any did.
	Distance42i *int `json:"distance_42i,omitempty"`
}

// View is the merged certification state of every source.
type View struct {
	// Status rolls up the source states: served when any source served a
	// contract tool, else declared-absent, else unreachable, else no-sources.
	Status string `json:"status"`
	// Rung is the effective caller rung evidenced across the sources. It is
	// adhd's assignment read back from its answers — tuner never declares it.
	Rung int `json:"rung"`
	// Demo is set when any source evidenced a demo cluster.
	Demo bool `json:"demo,omitempty"`
	// Note explains a non-served status, so the channel states the absence
	// rather than showing an empty list.
	Note string `json:"note,omitempty"`

	Summary  Summary   `json:"summary"`
	Features []Feature `json:"features"`
	Skills   []Skill   `json:"skills"`
	Members  []Member  `json:"members,omitempty"`
	// UnverifiedSkillCount is the rung-0 diagnostic adhd reports at high
	// rungs: skills excluded from the response because existence was never
	// confirmed.
	UnverifiedSkillCount int `json:"unverified_skill_count,omitempty"`

	Sources []Source  `json:"sources"`
	Fetched time.Time `json:"fetched"`
}

// VerifyResult is the outcome of one adhd.features.verify call.
type VerifyResult struct {
	Feature        string `json:"feature"`
	StillCertified bool   `json:"still_certified"`
	// State renders the verdict: "still certified", "isotope rotated" or
	// "certification lapsed".
	State          string `json:"state"`
	CurrentIsotope string `json:"current_isotope_id,omitempty"`
	ProbeTime      string `json:"probe_time,omitempty"`
	Note           string `json:"note,omitempty"`
	// Source is the adhd instance that ran the probe.
	Source string `json:"source"`
}

// Options configures a Browser.
type Options struct {
	// Identity returns tuner's instance id, sent with each handshake. It is
	// a function because the serve-mode identity (tuner:<port>) is only known
	// after the broadcast listener binds. An empty result sends "tuner".
	Identity func() string
	// Version is tuner's version, sent with each handshake.
	Version string
	// Refresh is the merged-view cache lifetime. Defaults to DefaultRefresh.
	Refresh time.Duration
	// Sources returns the live source set, evaluated on every fetch: the
	// discovered instances with the configured fallback merged in.
	Sources func() []Source
}

// Browser fetches and merges the certification state of tuner's adhd sources.
// It is safe for concurrent use.
type Browser struct {
	opts    Options
	refresh time.Duration

	mu     sync.Mutex
	cached *View
}

// New creates a feature browser.
func New(opts Options) *Browser {
	refresh := opts.Refresh
	if refresh <= 0 {
		refresh = DefaultRefresh
	}
	return &Browser{opts: opts, refresh: refresh}
}

// View returns the merged certification view, refetching when the cache is
// older than the refresh window. It never fails: a source set that cannot
// answer is itself the answer, published in the view's status, note and
// per-source state — a listed channel has to respond (tuner#30).
func (b *Browser) View(ctx context.Context) View {
	b.mu.Lock()
	if b.cached != nil && time.Since(b.cached.Fetched) < b.refresh {
		view := *b.cached
		b.mu.Unlock()
		return view
	}
	b.mu.Unlock()

	view := b.fetch(ctx)

	b.mu.Lock()
	b.cached = &view
	b.mu.Unlock()
	return view
}

// Verify triggers a live verification of one feature (adhd.features.verify).
//
// The action exists only at rung 3: below it, tuner does not call — offering
// an action adhd would refuse is offering a lie — and returns the hint the
// feature file specifies instead. When adhd refuses with -32003 anyway (its
// registry moved), the refusal's own rung figure is reported: adhd said it,
// so it is evidence, not a claim.
func (b *Browser) Verify(ctx context.Context, feature string) (VerifyResult, error) {
	view := b.View(ctx)
	if view.Status == StatusNoSources {
		return VerifyResult{}, fmt.Errorf("no adhd instances discovered or configured")
	}

	var best *Source
	for i := range view.Sources {
		src := &view.Sources[i]
		if !contains(src.ToolsServed, ToolFeaturesVerify) {
			continue
		}
		if best == nil || src.Rung > best.Rung {
			best = src
		}
	}
	if best == nil {
		return VerifyResult{}, fmt.Errorf("%s is not served by any source (declared absent: adhd#24 @red, adhd#39 open)", ToolFeaturesVerify)
	}
	if view.Rung < 3 {
		return VerifyResult{}, fmt.Errorf("advance to rung 3 to enable live verification (adhd assigned rung %d)", view.Rung)
	}

	client := b.clientFor(*best)
	raw, err := client.CallTool(ctx, ToolFeaturesVerify, map[string]interface{}{"feature": feature})
	if err != nil {
		var rpcErr *mcpclient.RPCError
		if errors.As(err, &rpcErr) && rpcErr.Code == -32003 {
			if rung, ok := rungFromRefusal(rpcErr.Message); ok {
				return VerifyResult{}, fmt.Errorf("adhd assigned tuner rung %d: advance to rung 3 to enable live verification", rung)
			}
			return VerifyResult{}, fmt.Errorf("adhd refused verification: %s", rpcErr.Message)
		}
		return VerifyResult{}, fmt.Errorf("verify %q: %w", feature, err)
	}

	var decoded struct {
		StillCertified   bool   `json:"still_certified"`
		CurrentIsotopeID string `json:"current_isotope_id"`
		ProbeTime        string `json:"probe_time"`
		Note             string `json:"note"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return VerifyResult{}, fmt.Errorf("decode verify result: %w", err)
	}

	state := "certification lapsed"
	switch {
	case decoded.StillCertified:
		state = "still certified"
	case strings.Contains(strings.ToLower(decoded.Note), "rotated"):
		state = "isotope rotated"
	}
	return VerifyResult{
		Feature:        feature,
		StillCertified: decoded.StillCertified,
		State:          state,
		CurrentIsotope: decoded.CurrentIsotopeID,
		ProbeTime:      decoded.ProbeTime,
		Note:           decoded.Note,
		Source:         best.Instance,
	}, nil
}

// refusalRung matches the "have N" figure of adhd's declared refusal message
// ("insufficient trust rung: need 3, have 1").
var refusalRung = regexp.MustCompile(`have (\d+)`)

func rungFromRefusal(message string) (int, bool) {
	m := refusalRung.FindStringSubmatch(message)
	if m == nil {
		return 0, false
	}
	rung, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	return rung, true
}

// sourceResult is one source's fetch: the source with its state, plus the
// decoded entries it released.
type sourceResult struct {
	src      Source
	features []Feature
	skills   []Skill
	members  []Member
	// unverified is the rung-0 skill diagnostic, when reported.
	unverified int
}

// fetch calls every source concurrently and merges the answers.
func (b *Browser) fetch(ctx context.Context) View {
	sources := b.sources()
	now := time.Now().UTC()
	if len(sources) == 0 {
		return View{
			Status:   StatusNoSources,
			Note:     "no adhd instances: mDNS discovery found none and no fallback endpoint is configured (feature_browser.endpoints)",
			Sources:  []Source{},
			Features: []Feature{},
			Skills:   []Skill{},
			Fetched:  now,
		}
	}

	results := make([]sourceResult, len(sources))
	var wg sync.WaitGroup
	for i := range sources {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = b.fetchSource(ctx, sources[i])
		}(i)
	}
	wg.Wait()

	return merge(results, now)
}

func (b *Browser) sources() []Source {
	if b.opts.Sources == nil {
		return nil
	}
	return b.opts.Sources()
}

func (b *Browser) identity() string {
	if b.opts.Identity != nil {
		if id := strings.TrimSpace(b.opts.Identity()); id != "" {
			return id
		}
	}
	return "tuner"
}

func (b *Browser) clientFor(src Source) *mcpclient.Client {
	return mcpclient.New(src.Endpoint, mcpclient.Options{
		Version:  b.opts.Version,
		Instance: b.identity(),
	})
}

// fetchSource runs the contract against one instance: handshake, tools/list,
// then each contract tool the list actually serves.
func (b *Browser) fetchSource(ctx context.Context, src Source) sourceResult {
	src.Fetched = time.Now().UTC()
	client := b.clientFor(src)

	if _, err := client.Initialize(ctx); err != nil {
		src.State = Unreachable
		src.Error = err.Error()
		return sourceResult{src: src}
	}
	tools, err := client.ListTools(ctx)
	if err != nil {
		src.State = Unreachable
		src.Error = err.Error()
		return sourceResult{src: src}
	}
	src.Tools = len(tools)
	for _, tool := range contractTools {
		if contains(tools, tool) {
			src.ToolsServed = append(src.ToolsServed, tool)
		}
	}
	if contains(tools, ToolFeaturesVerify) {
		src.ToolsServed = append(src.ToolsServed, ToolFeaturesVerify)
	}
	if len(src.ToolsServed) == 0 {
		// The endpoint speaks MCP but serves none of the contract: declared,
		// absent by policy. Not an error — the published answer.
		src.State = DeclaredAbsent
		src.Error = fmt.Sprintf("%s not in tools/list (%d tools served)", strings.Join(contractTools, ", "), len(tools))
		return sourceResult{src: src}
	}
	src.State = Served

	result := sourceResult{src: src}
	rung := 0

	if contains(src.ToolsServed, ToolFeaturesCertified) {
		raw, err := client.CallTool(ctx, ToolFeaturesCertified, nil)
		if err != nil {
			result.src.recordCallFailure(ToolFeaturesCertified, err)
		} else {
			envelope := decodeEnvelope(raw)
			result.features = decodeFeatures(raw)
			rung = max(rung, envelope.rungEvidence(featureRungEvidence(result.features)))
			result.src.Demo = result.src.Demo || envelope.demo
		}
	}
	if contains(src.ToolsServed, ToolSkillsCertified) {
		raw, err := client.CallTool(ctx, ToolSkillsCertified, nil)
		if err != nil {
			result.src.recordCallFailure(ToolSkillsCertified, err)
		} else {
			envelope := decodeEnvelope(raw)
			result.skills = decodeSkills(raw)
			result.unverified = envelope.UnverifiedSkillCount
			rung = max(rung, envelope.rungEvidence(skillRungEvidence(result.skills)))
			result.src.Demo = result.src.Demo || envelope.demo
		}
	}
	if contains(src.ToolsServed, ToolFeaturesCluster) {
		raw, err := client.CallTool(ctx, ToolFeaturesCluster, nil)
		if err != nil {
			result.src.recordCallFailure(ToolFeaturesCluster, err)
		} else {
			envelope := decodeEnvelope(raw)
			result.members = decodeMembers(raw)
			rung = max(rung, envelope.rungEvidence(memberRungEvidence(result.members)))
			if strings.EqualFold(envelope.Mode, "demo") {
				result.src.Demo = true
			}
			result.src.Demo = result.src.Demo || envelope.demo
			for _, m := range result.members {
				if strings.EqualFold(m.Mode, "demo") {
					result.src.Demo = true
				}
			}
		}
	}
	for _, f := range result.features {
		if f.StaleDemoCert {
			result.src.Demo = true
		}
	}
	result.src.Rung = rung
	return result
}

// recordCallFailure notes a refused or failed contract call on the source. A
// refusal is evidence too: adhd's -32003 names the rung it assigned.
func (s *Source) recordCallFailure(tool string, err error) {
	var rpcErr *mcpclient.RPCError
	if errors.As(err, &rpcErr) {
		if rung, ok := rungFromRefusal(rpcErr.Message); ok && rung > s.Rung {
			s.Rung = rung
		}
		detail := fmt.Sprintf("%s refused: %s", tool, rpcErr.Message)
		if s.Error == "" {
			s.Error = detail
		} else {
			s.Error += "; " + detail
		}
		return
	}
	detail := fmt.Sprintf("%s: %v", tool, err)
	if s.Error == "" {
		s.Error = detail
	} else {
		s.Error += "; " + detail
	}
}

// envelope is the top level of a contract response: the fields that describe
// the answer itself rather than its entries.
type envelope struct {
	CallerRung           *int   `json:"caller_rung"`
	Rung                 *int   `json:"rung"`
	Mode                 string `json:"mode"`
	Note                 string `json:"note"`
	UnverifiedSkillCount int    `json:"unverified_skill_count"`
	// demo is set when the envelope itself evidences a demo cluster.
	demo bool
}

func decodeEnvelope(raw json.RawMessage) envelope {
	var env envelope
	if err := json.Unmarshal(raw, &env); err == nil {
		if strings.Contains(strings.ToLower(env.Note), "plaintext") || strings.EqualFold(env.Mode, "demo") {
			env.demo = true
		}
	}
	return env
}

// rungEvidence prefers the explicit rung adhd reported; without one it takes
// the inference from the released fields. Both are adhd's statement — the
// first in words, the second in what it chose to send.
func (e envelope) rungEvidence(inferred int) int {
	rung := inferred
	if e.CallerRung != nil {
		rung = max(rung, *e.CallerRung)
	}
	if e.Rung != nil {
		rung = max(rung, *e.Rung)
	}
	return rung
}

// entriesOf extracts the entry array from a contract response: the top-level
// array itself, or the first of the conventional container keys holding one.
// The tools are declared absent today, so the container key is not pinned by a
// served example; the declared contract names the entries, and this accepts
// the shapes a JSON-RPC handler naturally returns.
func entriesOf(raw json.RawMessage, keys ...string) []json.RawMessage {
	trimmed := []byte(strings.TrimSpace(string(raw)))
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var arr []json.RawMessage
		if err := json.Unmarshal(trimmed, &arr); err == nil {
			return arr
		}
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &obj); err != nil {
		return nil
	}
	for _, key := range keys {
		candidate, ok := obj[key]
		if !ok {
			continue
		}
		var arr []json.RawMessage
		if err := json.Unmarshal(candidate, &arr); err == nil {
			return arr
		}
	}
	return nil
}

func decodeFeatures(raw json.RawMessage) []Feature {
	entries := entriesOf(raw, "features", "entries", "items")
	out := make([]Feature, 0, len(entries))
	for _, entry := range entries {
		var f Feature
		if err := json.Unmarshal(entry, &f); err == nil && (f.FeatureFile != "" || f.ScenarioName != "") {
			out = append(out, f)
		}
	}
	return out
}

func decodeSkills(raw json.RawMessage) []Skill {
	entries := entriesOf(raw, "skills", "entries", "items")
	out := make([]Skill, 0, len(entries))
	for _, entry := range entries {
		var s Skill
		if err := json.Unmarshal(entry, &s); err == nil && s.SkillName != "" {
			out = append(out, s)
		}
	}
	return out
}

func decodeMembers(raw json.RawMessage) []Member {
	entries := entriesOf(raw, "members", "cluster", "entries", "items")
	out := make([]Member, 0, len(entries))
	for _, entry := range entries {
		var m Member
		if err := json.Unmarshal(entry, &m); err == nil && m.Name != "" {
			out = append(out, m)
		}
	}
	return out
}

// The rung evidence of released fields: a gated field adhd sent is a statement
// of what tuner may see, so the visibility tier is read from the data, never
// assumed.

func featureRungEvidence(features []Feature) int {
	rung := 0
	for _, f := range features {
		if f.ClusterMember != "" {
			rung = max(rung, 3)
		}
		if f.IsotopeID != "" || f.LastTransit != "" {
			rung = max(rung, 2)
		}
	}
	return rung
}

func skillRungEvidence(skills []Skill) int {
	rung := 0
	for _, s := range skills {
		if s.IsotopeBinding != "" {
			rung = max(rung, 3)
		}
		if s.Distance != nil || s.HostInstance != "" {
			rung = max(rung, 2)
		}
	}
	return rung
}

func memberRungEvidence(members []Member) int {
	rung := 0
	for _, m := range members {
		if m.TrustRung != nil || m.CertifiedFeatureCount != nil {
			rung = max(rung, 2)
		}
	}
	return rung
}

// merge folds every source's answers into one view. Federation rule from the
// feature file: a feature certified by either instance is certified, and the
// source of each certification is kept.
func merge(results []sourceResult, now time.Time) View {
	view := View{
		Status:   StatusUnreachable,
		Sources:  make([]Source, 0, len(results)),
		Features: []Feature{},
		Skills:   []Skill{},
		Fetched:  now,
	}

	features := map[string]*Feature{}
	featureOrder := []string{}
	skills := map[string]*Skill{}
	members := map[string]*Member{}
	// demoVotes tracks, per feature, whether any certifying source was real:
	// a real certification is not laundered into demo, and a demo-only one is
	// never presented as production evidence.
	certifiedByReal := map[string]bool{}
	certifiedByDemo := map[string]bool{}

	served, declared, unreachable := 0, 0, 0
	for i := range results {
		result := &results[i]
		view.Sources = append(view.Sources, result.src)
		switch result.src.State {
		case Served:
			served++
		case DeclaredAbsent:
			declared++
		default:
			unreachable++
		}
		view.Rung = max(view.Rung, result.src.Rung)
		view.Demo = view.Demo || result.src.Demo
		view.UnverifiedSkillCount = max(view.UnverifiedSkillCount, result.unverified)

		for _, f := range result.features {
			key := f.FeatureFile + "\x00" + f.ScenarioName
			merged, ok := features[key]
			if !ok {
				entry := f
				entry.Sources = []string{result.src.Instance}
				features[key] = &entry
				featureOrder = append(featureOrder, key)
			} else {
				merged.Certified = merged.Certified || f.Certified
				merged.StaleDemoCert = merged.StaleDemoCert || f.StaleDemoCert
				merged.Sources = appendUnique(merged.Sources, result.src.Instance)
				// Keep the most adhd released: the entry from the higher-rung
				// answer carries the gated fields.
				if featureTier(f) > featureTier(*merged) {
					merged.IsotopeID, merged.LastTransit, merged.ClusterMember = f.IsotopeID, f.LastTransit, f.ClusterMember
				}
			}
			if f.Certified {
				if result.src.Demo {
					certifiedByDemo[key] = true
				} else {
					certifiedByReal[key] = true
				}
			}
		}

		for _, s := range result.skills {
			merged, ok := skills[s.SkillName]
			if !ok {
				entry := s
				entry.Sources = []string{result.src.Instance}
				skills[s.SkillName] = &entry
				continue
			}
			merged.Sources = appendUnique(merged.Sources, result.src.Instance)
			if s.VariationFailures != nil {
				if merged.VariationFailures == nil || *s.VariationFailures > *merged.VariationFailures {
					merged.VariationFailures = s.VariationFailures
				}
			}
			if s.CertifiedRung > merged.CertifiedRung {
				keep := merged.Sources
				*merged = s
				merged.Sources = keep
			}
		}

		for _, m := range result.members {
			merged, ok := members[m.Name]
			if !ok {
				entry := m
				entry.Sources = []string{result.src.Instance}
				members[m.Name] = &entry
				continue
			}
			merged.Sources = appendUnique(merged.Sources, result.src.Instance)
			// A demo report is not laundered into a real one: demo wins.
			if strings.EqualFold(m.Mode, "demo") {
				merged.Mode = "demo"
			} else if merged.Mode == "" {
				merged.Mode = m.Mode
			}
			if m.TrustRung != nil && (merged.TrustRung == nil || *m.TrustRung > *merged.TrustRung) {
				merged.TrustRung = m.TrustRung
			}
			if m.CertifiedFeatureCount != nil && (merged.CertifiedFeatureCount == nil || *m.CertifiedFeatureCount > *merged.CertifiedFeatureCount) {
				merged.CertifiedFeatureCount = m.CertifiedFeatureCount
			}
			if merged.Note == "" {
				merged.Note = m.Note
			}
		}
	}

	for _, key := range featureOrder {
		f := *features[key]
		f.Demo = f.Certified && certifiedByDemo[key] && !certifiedByReal[key]
		if f.Certified {
			// Certifying sources first in the federation trail.
			f.Sources = orderSources(f.Sources, results, func(r sourceResult) bool {
				for _, entry := range r.features {
					if entry.FeatureFile+"\x00"+entry.ScenarioName == key && entry.Certified {
						return true
					}
				}
				return false
			})
		}
		view.Features = append(view.Features, f)
	}
	sort.SliceStable(view.Features, func(i, j int) bool {
		a, b := view.Features[i], view.Features[j]
		if a.Certified != b.Certified {
			return a.Certified // certified first
		}
		if a.FeatureFile != b.FeatureFile {
			return a.FeatureFile < b.FeatureFile
		}
		return a.ScenarioName < b.ScenarioName
	})

	for _, s := range skills {
		view.Skills = append(view.Skills, *s)
	}
	sort.SliceStable(view.Skills, func(i, j int) bool {
		a, b := view.Skills[i], view.Skills[j]
		if a.CertifiedRung != b.CertifiedRung {
			return a.CertifiedRung > b.CertifiedRung
		}
		return a.SkillName < b.SkillName
	})

	for _, m := range members {
		view.Members = append(view.Members, *m)
	}
	// The member that certifies the most features ranks highest.
	sort.SliceStable(view.Members, func(i, j int) bool {
		a, b := view.Members[i], view.Members[j]
		ac, bc := -1, -1
		if a.CertifiedFeatureCount != nil {
			ac = *a.CertifiedFeatureCount
		}
		if b.CertifiedFeatureCount != nil {
			bc = *b.CertifiedFeatureCount
		}
		if ac != bc {
			return ac > bc
		}
		return a.Name < b.Name
	})

	for _, f := range view.Features {
		view.Summary.FeaturesTotal++
		if f.Certified {
			view.Summary.FeaturesCertified++
		}
	}
	for _, s := range view.Skills {
		view.Summary.SkillsTotal++
		if s.CertifiedRung >= 2 {
			view.Summary.SkillsDeterministic++
		}
		if s.Distance != nil && (view.Summary.Distance42i == nil || *s.Distance > *view.Summary.Distance42i) {
			d := *s.Distance
			view.Summary.Distance42i = &d
		}
	}

	switch {
	case served > 0:
		view.Status = StatusServed
		if declared+unreachable > 0 {
			view.Note = partialNote(declared, unreachable)
		}
	case declared > 0:
		view.Status = StatusDeclaredAbsent
		view.Note = DeclaredAbsentNote
	default:
		view.Status = StatusUnreachable
		view.Note = "every adhd source failed the MCP handshake"
	}
	return view
}

// DeclaredAbsentNote is the honest answer when the contract tools are not
// served: the surface is declared absent by policy, which is a state to
// publish, not an error to hide.
const DeclaredAbsentNote = "adhd.features.certified / adhd.skills.certified / adhd.features.cluster are declared but not served " +
	"(declared absent by policy: adhd#24 closed @red, adhd#39 open, lab-safety sheets/adhd.md §16)"

func partialNote(declared, unreachable int) string {
	parts := []string{}
	if declared > 0 {
		parts = append(parts, fmt.Sprintf("%d source(s) declared-absent", declared))
	}
	if unreachable > 0 {
		parts = append(parts, fmt.Sprintf("%d source(s) unreachable", unreachable))
	}
	return "partial federation: " + strings.Join(parts, ", ")
}

// featureTier ranks an entry by how much adhd released in it, so a federation
// merge keeps the richest answer rather than the first.
func featureTier(f Feature) int {
	switch {
	case f.ClusterMember != "":
		return 3
	case f.IsotopeID != "" || f.LastTransit != "":
		return 2
	case f.Certified:
		return 1
	default:
		return 0
	}
}

func orderSources(sources []string, results []sourceResult, certifiedBy func(sourceResult) bool) []string {
	out := make([]string, 0, len(sources))
	for i := range results {
		if certifiedBy(results[i]) && contains(sources, results[i].src.Instance) {
			out = append(out, results[i].src.Instance)
		}
	}
	for _, s := range sources {
		if !contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

func appendUnique(list []string, s string) []string {
	if contains(list, s) {
		return list
	}
	return append(list, s)
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// NormalizeEndpoint turns a configured or discovered address into the MCP
// URL: adhd's isotope TXT record carries no path, so a bare origin gets the
// conventional /mcp route.
func NormalizeEndpoint(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	u, err := url.Parse(trimmed)
	if err != nil || u.Host == "" {
		return trimmed
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = mcpclient.DefaultMCPPath
	}
	return u.String()
}

// ValidateConfig reports feature_browser problems that would leave the channel
// advertising a source it cannot reach by construction. A malformed endpoint is
// an error the operator can see at startup; an absent refresh is not — the
// built-in default applies.
func ValidateConfig(cfg config.FeatureBrowserConfig) []error {
	var problems []error
	for i, ep := range cfg.Endpoints {
		raw := strings.TrimSpace(ep.URL)
		if raw == "" {
			problems = append(problems, fmt.Errorf("feature_browser.endpoints[%d]: empty url", i))
			continue
		}
		u, err := url.Parse(raw)
		if err != nil {
			problems = append(problems, fmt.Errorf("feature_browser.endpoints[%d]: %q: %w", i, ep.URL, err))
			continue
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			problems = append(problems, fmt.Errorf("feature_browser.endpoints[%d]: %q is not an http(s) URL", i, ep.URL))
			continue
		}
		if u.Host == "" {
			problems = append(problems, fmt.Errorf("feature_browser.endpoints[%d]: %q has no host", i, ep.URL))
		}
	}
	if refresh := strings.TrimSpace(cfg.Refresh); refresh != "" {
		if d, err := time.ParseDuration(refresh); err != nil || d <= 0 {
			problems = append(problems, fmt.Errorf("feature_browser.refresh: %q is not a positive duration", cfg.Refresh))
		}
	}
	return problems
}

// ConfigSources turns feature_browser.endpoints — the fallback for when mDNS
// is unavailable — into sources.
func ConfigSources(endpoints []config.FeatureEndpoint) []Source {
	out := make([]Source, 0, len(endpoints))
	for _, ep := range endpoints {
		normalized := NormalizeEndpoint(ep.URL)
		if normalized == "" {
			continue
		}
		name := strings.TrimSpace(ep.Name)
		if name == "" {
			name = endpointLabel(normalized)
		}
		out = append(out, Source{
			Instance: name,
			Endpoint: normalized,
			Origin:   "config",
		})
	}
	return out
}

// DiscoveredSources turns the mDNS browser's isotope endpoints into sources.
// Each endpoint carries the identity its TXT record advertised (role,
// trust_rung) — adhd's own, which is what makes the instance addressable as a
// peer; tuner's rung is never in this data.
func DiscoveredSources(endpoints []mdns.Endpoint) []Source {
	out := make([]Source, 0, len(endpoints))
	for i := range endpoints {
		ep := endpoints[i]
		src := Source{
			Instance: ep.Host + ":" + strconv.Itoa(ep.Port),
			Endpoint: NormalizeEndpoint(ep.URL()),
			Origin:   "mdns",
		}
		for _, svc := range ep.Services {
			if label := svc.Instance(); label != "" {
				src.Instance = label
				break
			}
		}
		if len(ep.Identities) > 0 {
			src.Role = ep.Identities[0].Role
			src.TrustRung = ep.Identities[0].TrustRung
		}
		out = append(out, src)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Instance < out[j].Instance })
	return out
}

// SourceSet applies the discovery contract: the discovered instances are the
// source set, and the configured endpoints are the fallback for when mDNS is
// unavailable — used only when discovery found nothing.
func SourceSet(discovered, configured []Source) []Source {
	if len(discovered) > 0 {
		return dedupeByEndpoint(discovered)
	}
	return dedupeByEndpoint(configured)
}

func dedupeByEndpoint(sources []Source) []Source {
	seen := map[string]bool{}
	out := make([]Source, 0, len(sources))
	for i := range sources {
		if seen[sources[i].Endpoint] {
			continue
		}
		seen[sources[i].Endpoint] = true
		out = append(out, sources[i])
	}
	return out
}

// endpointLabel names a configured endpoint without a name: the host:port is
// the address the operator wrote.
func endpointLabel(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	return u.Host
}
