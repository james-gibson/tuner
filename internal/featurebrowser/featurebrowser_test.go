package featurebrowser

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/james-gibson/tuner/internal/config"
	"github.com/james-gibson/tuner/internal/mdns"
)

// fakeADHD is an MCP endpoint serving a chosen tool list and tool results. The
// certification tools are declared absent in the lab (adhd#24 @red, adhd#39
// open), so a served contract exists only as a fixture: these tests pin tuner's
// side of a contract whose server side is declared, not served.
type fakeADHD struct {
	tools   []string
	results map[string]json.RawMessage
	refuse  map[string]string // tool -> refusal message
}

type fakeState struct {
	mu       sync.Mutex
	calls    map[string]int
	instance string
	inits    int
}

func (s *fakeState) callCount(tool string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[tool]
}

func (s *fakeState) initCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inits
}

func (s *fakeState) sentIdentity() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.instance
}

func newFakeADHD(t *testing.T, fake fakeADHD) (*httptest.Server, *fakeState) {
	t.Helper()
	state := &fakeState{calls: map[string]int{}}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			JSONRPC string `json:"jsonrpc"`
			ID      int64  `json:"id"`
			Method  string `json:"method"`
			Params  struct {
				Name     string          `json:"name"`
				Instance string          `json:"instance"`
				Args     json.RawMessage `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}

		state.mu.Lock()
		defer state.mu.Unlock()

		switch req.Method {
		case "initialize":
			state.inits++
			state.instance = req.Params.Instance
			writeRPCResult(w, req.ID, map[string]any{
				"protocolVersion": "2024-11-05",
				"serverInfo":      map[string]any{"name": "adhd", "version": "test"},
			})
		case "notifications/initialized":
			// A notification expects no answer; 202 is a legal one.
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			tools := make([]map[string]any, 0, len(fake.tools))
			for _, name := range fake.tools {
				tools = append(tools, map[string]any{"name": name})
			}
			writeRPCResult(w, req.ID, map[string]any{"tools": tools})
		case "tools/call":
			state.calls[req.Params.Name]++
			if message, ok := fake.refuse[req.Params.Name]; ok {
				writeRPCError(w, req.ID, -32003, message)
				return
			}
			raw, ok := fake.results[req.Params.Name]
			if !ok {
				writeRPCError(w, req.ID, -32601, "method not found: "+req.Params.Name)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":%s}`, req.ID, raw)
		default:
			writeRPCError(w, req.ID, -32601, "method not found: "+req.Method)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, state
}

func writeRPCResult(w http.ResponseWriter, id int64, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func writeRPCError(w http.ResponseWriter, id int64, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0", "id": id,
		"error": map[string]any{"code": code, "message": message},
	})
}

// endpointOf turns a fake server URL into the MCP URL a source carries.
func endpointOf(srv *httptest.Server) string {
	return srv.URL + "/mcp"
}

// newBrowser builds a browser over a fixed source set with a short cache so
// tests do not share state across cases.
func newBrowser(sources []Source, opts ...func(*Options)) *Browser {
	options := Options{
		Version:  "test",
		Refresh:  time.Minute,
		Identity: func() string { return "tuner:test" },
		Sources:  func() []Source { return sources },
	}
	for _, opt := range opts {
		opt(&options)
	}
	return New(options)
}

func TestViewServedMergesFederation(t *testing.T) {
	a, stateA := newFakeADHD(t, fakeADHD{
		tools: []string{ToolFeaturesCertified, ToolSkillsCertified, ToolFeaturesCluster},
		results: map[string]json.RawMessage{
			ToolFeaturesCertified: json.RawMessage(`{"caller_rung":3,"features":[
				{"feature_file":"adhd/mdns-discovery","certified":true,"isotope_id":"iso-a-1","cluster_member":"smoke:alarm-a/t1"},
				{"feature_file":"adhd/headless-mode","certified":false}]}`),
			ToolSkillsCertified: json.RawMessage(`{"skills":[
				{"skill_name":"open-the-pickle-jar","certified_rung":3,"isotope_binding":"adhd/skill-system"}]}`),
			ToolFeaturesCluster: json.RawMessage(`{"members":[
				{"name":"alarm-a","mode":"real","certified_feature_count":6},
				{"name":"alarm-b","mode":"real","certified_feature_count":2}]}`),
		},
	})
	b, _ := newFakeADHD(t, fakeADHD{
		tools: []string{ToolFeaturesCertified},
		results: map[string]json.RawMessage{
			// No rung field, no gated fields: adhd released names and booleans.
			ToolFeaturesCertified: json.RawMessage(`[{"feature_file":"adhd/headless-mode","certified":true}]`),
		},
	})

	browser := newBrowser([]Source{
		{Instance: "adhd-tui", Endpoint: endpointOf(a), Origin: "mdns"},
		{Instance: "adhd-headless", Endpoint: endpointOf(b), Origin: "mdns"},
	})

	view := browser.View(context.Background())
	if view.Status != StatusServed {
		t.Fatalf("status = %q, want served (sources: %+v)", view.Status, view.Sources)
	}
	if view.Rung != 3 {
		t.Fatalf("rung = %d, want 3 (from adhd's caller_rung)", view.Rung)
	}

	byName := map[string]Feature{}
	for _, f := range view.Features {
		byName[f.FeatureFile] = f
	}
	if len(byName) != 2 {
		t.Fatalf("expected 2 merged features, got %+v", view.Features)
	}
	// Federation: a feature certified by either instance is certified.
	headless, ok := byName["adhd/headless-mode"]
	if !ok || !headless.Certified {
		t.Fatalf("a feature certified by the second instance was not merged: %+v", headless)
	}
	if headless.ClusterMember != "" || headless.IsotopeID != "" {
		t.Fatalf("gated fields appeared without a source that released them: %+v", headless)
	}
	mdnsFeature := byName["adhd/mdns-discovery"]
	if mdnsFeature.ClusterMember != "smoke:alarm-a/t1" || mdnsFeature.IsotopeID != "iso-a-1" {
		t.Fatalf("gated fields from the serving source were dropped: %+v", mdnsFeature)
	}
	if view.Summary.FeaturesCertified != 2 || view.Summary.FeaturesTotal != 2 {
		t.Fatalf("summary = %+v, want 2/2", view.Summary)
	}
	if len(view.Members) != 2 || view.Members[0].Name != "alarm-a" {
		t.Fatalf("members not ranked by certified count: %+v", view.Members)
	}
	if len(view.Skills) != 1 || view.Skills[0].CertifiedRung != 3 {
		t.Fatalf("skills not merged: %+v", view.Skills)
	}
	if stateA.initCount() != 1 {
		t.Fatalf("initialize called %d times, want once per source", stateA.initCount())
	}
	if got := stateA.sentIdentity(); got != "tuner:test" {
		t.Fatalf("handshake identity = %q, want tuner:test (identity, never a rung)", got)
	}
}

func TestViewDeclaredAbsentIsAnAnswer(t *testing.T) {
	srv, _ := newFakeADHD(t, fakeADHD{tools: []string{"adhd.ping"}})
	browser := newBrowser([]Source{{Instance: "adhd", Endpoint: endpointOf(srv), Origin: "mdns"}})

	view := browser.View(context.Background())
	if view.Status != StatusDeclaredAbsent {
		t.Fatalf("status = %q, want declared-absent", view.Status)
	}
	if !strings.Contains(view.Note, "declared but not served") {
		t.Fatalf("note does not state the absence: %q", view.Note)
	}
	if len(view.Sources) != 1 || view.Sources[0].State != DeclaredAbsent {
		t.Fatalf("source not reported declared-absent: %+v", view.Sources)
	}
	if view.Sources[0].Error == "" {
		t.Fatal("declared-absent source carries no explanation")
	}

	rendered := strings.Join(Render(view), "\n")
	if !strings.Contains(rendered, "declared but not served") {
		t.Fatalf("channel does not publish the absence:\n%s", rendered)
	}
	if !strings.Contains(rendered, "0 / 0 certified") {
		t.Fatalf("summary row missing:\n%s", rendered)
	}
}

func TestViewUnreachableEndpoints(t *testing.T) {
	browser := newBrowser([]Source{{Instance: "gone", Endpoint: "http://127.0.0.1:1/mcp", Origin: "mdns"}})

	view := browser.View(context.Background())
	if view.Status != StatusUnreachable {
		t.Fatalf("status = %q, want unreachable", view.Status)
	}
	if !strings.Contains(view.Note, "failed the MCP handshake") {
		t.Fatalf("note = %q", view.Note)
	}
	if len(view.Sources) != 1 || view.Sources[0].Error == "" {
		t.Fatalf("unreachable source not explained: %+v", view.Sources)
	}
}

func TestViewNoSources(t *testing.T) {
	browser := newBrowser(nil)
	view := browser.View(context.Background())
	if view.Status != StatusNoSources {
		t.Fatalf("status = %q, want no-sources", view.Status)
	}
	if !strings.Contains(view.Note, "feature_browser.endpoints") {
		t.Fatalf("no-source note does not name the fallback: %q", view.Note)
	}
	rendered := strings.Join(Render(view), "\n")
	if !strings.Contains(rendered, "no sources") || !strings.Contains(rendered, "declared but not served") {
		t.Fatalf("no-source view is not honest:\n%s", rendered)
	}
}

func TestSourceSetFallsBackOnlyWhenDiscoveryIsEmpty(t *testing.T) {
	discovered := []Source{{Instance: "adhd-tui", Endpoint: "http://10.0.0.2:9090/mcp", Origin: "mdns"}}
	configured := []Source{{Instance: "fallback", Endpoint: "http://127.0.0.1:9090/mcp", Origin: "config"}}

	if got := SourceSet(discovered, configured); len(got) != 1 || got[0].Instance != "adhd-tui" {
		t.Fatalf("discovery did not win: %+v", got)
	}
	if got := SourceSet(nil, configured); len(got) != 1 || got[0].Instance != "fallback" {
		t.Fatalf("configured fallback was not used: %+v", got)
	}
	// A duplicate address is one source, not two channels.
	dup := append([]Source{}, configured...)
	dup = append(dup, configured[0])
	if got := SourceSet(nil, dup); len(got) != 1 {
		t.Fatalf("duplicate endpoint not deduped: %+v", got)
	}
}

func TestNormalizeEndpoint(t *testing.T) {
	cases := map[string]string{
		"http://127.0.0.1:9090":        "http://127.0.0.1:9090/mcp",
		"http://127.0.0.1:9090/":       "http://127.0.0.1:9090/mcp",
		"http://127.0.0.1:9090/mcp":    "http://127.0.0.1:9090/mcp",
		"https://adhd.local/some/path": "https://adhd.local/some/path",
		"  http://127.0.0.1:9090  ":    "http://127.0.0.1:9090/mcp",
		"127.0.0.1:9090":               "127.0.0.1:9090",
	}
	for in, want := range cases {
		if got := NormalizeEndpoint(in); got != want {
			t.Fatalf("NormalizeEndpoint(%q) = %q, want %q", in, got, want)
		}
	}
	if got := NormalizeEndpoint("   "); got != "" {
		t.Fatalf("NormalizeEndpoint(empty) = %q, want empty", got)
	}
}

func TestRenderRungGating(t *testing.T) {
	rungZero := View{
		Status:  StatusServed,
		Sources: []Source{{Instance: "adhd", Endpoint: "http://x/mcp", State: Served}},
		Features: []Feature{
			{FeatureFile: "adhd/mdns-discovery", Certified: true},
			{FeatureFile: "adhd/headless-mode", Certified: false},
		},
		Summary: Summary{FeaturesCertified: 1, FeaturesTotal: 2},
	}
	rendered := strings.Join(Render(rungZero), "\n")
	for _, forbidden := range []string{"certified by", "isotope "} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("rung-0 render leaked %q:\n%s", forbidden, rendered)
		}
	}
	if !strings.Contains(rendered, "1 / 2 certified (50%)") {
		t.Fatalf("summary row not in the specified shape:\n%s", rendered)
	}
	if !strings.Contains(rendered, "advance to rung 3") {
		t.Fatalf("rung-0 view offers no verify hint:\n%s", rendered)
	}
	if strings.Contains(rendered, "tuner features --verify") {
		t.Fatalf("rung-0 view offers a verify action:\n%s", rendered)
	}

	rungThree := View{
		Status: StatusServed,
		Rung:   3,
		Sources: []Source{{
			Instance: "adhd", Endpoint: "http://x/mcp", State: Served,
			ToolsServed: []string{ToolFeaturesCertified, ToolFeaturesVerify},
		}},
		Features: []Feature{{
			FeatureFile: "adhd/mdns-discovery", Certified: true,
			ClusterMember: "smoke:alarm-a/t1", IsotopeID: "iso-1",
		}},
		Summary: Summary{FeaturesCertified: 1, FeaturesTotal: 1},
	}
	rendered = strings.Join(Render(rungThree), "\n")
	if !strings.Contains(rendered, "certified by alarm-a/t1") {
		t.Fatalf("rung-3 render dropped the certifying member:\n%s", rendered)
	}
	if !strings.Contains(rendered, "verify: tuner features --verify=") {
		t.Fatalf("rung-3 render offers no verify action:\n%s", rendered)
	}
}

func TestRenderDemoLabelling(t *testing.T) {
	srv, _ := newFakeADHD(t, fakeADHD{
		tools: []string{ToolFeaturesCertified, ToolFeaturesCluster},
		results: map[string]json.RawMessage{
			ToolFeaturesCertified: json.RawMessage(`{"features":[{"feature_file":"adhd/mdns-discovery","certified":true}]}`),
			ToolFeaturesCluster:   json.RawMessage(`{"mode":"demo","members":[{"name":"alarm-a","mode":"demo"}]}`),
		},
	})
	browser := newBrowser([]Source{{Instance: "adhd", Endpoint: endpointOf(srv), Origin: "mdns"}})
	view := browser.View(context.Background())

	if !view.Demo {
		t.Fatal("demo cluster not detected from the cluster response")
	}
	if len(view.Features) != 1 || !view.Features[0].Demo {
		t.Fatalf("demo-only certification not labeled: %+v", view.Features)
	}
	rendered := strings.Join(Render(view), "\n")
	if !strings.Contains(rendered, "◇ adhd/mdns-discovery") {
		t.Fatalf("demo certification drawn as a solid indicator:\n%s", rendered)
	}
	if !strings.Contains(rendered, "[demo]") {
		t.Fatalf("demo certification not labeled [demo]:\n%s", rendered)
	}
	if !strings.Contains(rendered, "demo cluster: plaintext isotopes, not production evidence") {
		t.Fatalf("demo caveat missing:\n%s", rendered)
	}
}

func TestRenderSkillAdvancementAnd42i(t *testing.T) {
	failures := 2
	distance := 16
	view := View{
		Status:  StatusServed,
		Sources: []Source{{Instance: "adhd", Endpoint: "http://x/mcp", State: Served}},
		Skills: []Skill{
			{SkillName: "start-here", CertifiedRung: 2, Distance: &distance, VariationFailures: &failures},
			{SkillName: "ghost-skill", CertifiedRung: 0},
		},
		Summary: Summary{SkillsDeterministic: 1, SkillsTotal: 2},
	}
	rendered := strings.Join(Render(view), "\n")
	for _, want := range []string{
		"2 variation failures",
		"42i: 16",
		"probe call required to verify existence", // rung-0 next step
		"rung 0 (unverified)",
		"1/2 skills deterministic",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("skill render missing %q:\n%s", want, rendered)
		}
	}
}

func TestVerifyRefusedBelowRung3(t *testing.T) {
	srv, state := newFakeADHD(t, fakeADHD{
		tools: []string{ToolFeaturesCertified, ToolFeaturesVerify},
		results: map[string]json.RawMessage{
			ToolFeaturesCertified: json.RawMessage(`{"caller_rung":1,"features":[{"feature_file":"adhd/mdns-discovery","certified":true}]}`),
		},
		refuse: map[string]string{ToolFeaturesVerify: "insufficient trust rung: need 3, have 1"},
	})
	browser := newBrowser([]Source{{Instance: "adhd", Endpoint: endpointOf(srv), Origin: "mdns"}})

	_, err := browser.Verify(context.Background(), "adhd/mdns-discovery")
	if err == nil {
		t.Fatal("verify succeeded below rung 3")
	}
	if !strings.Contains(err.Error(), "advance to rung 3") {
		t.Fatalf("refusal does not name the rung boundary: %v", err)
	}
	if got := state.callCount(ToolFeaturesVerify); got != 0 {
		t.Fatalf("verify called %d times below rung 3; tuner must not offer an action adhd would refuse", got)
	}
}

func TestVerifyAtRung3ReportsIsotopeRotation(t *testing.T) {
	srv, state := newFakeADHD(t, fakeADHD{
		tools: []string{ToolFeaturesCertified, ToolFeaturesVerify},
		results: map[string]json.RawMessage{
			ToolFeaturesCertified: json.RawMessage(`{"caller_rung":3,"features":[{"feature_file":"adhd/mdns-discovery","certified":true}]}`),
			ToolFeaturesVerify:    json.RawMessage(`{"still_certified":false,"note":"isotope rotated","current_isotope_id":"iso-2","probe_time":"2026-11-04T10:00:00Z"}`),
		},
	})
	browser := newBrowser([]Source{{Instance: "adhd-tui", Endpoint: endpointOf(srv), Origin: "mdns"}})

	result, err := browser.Verify(context.Background(), "adhd/mdns-discovery")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if result.State != "isotope rotated" || result.CurrentIsotope != "iso-2" {
		t.Fatalf("rotation not reported as a distinct state: %+v", result)
	}
	if result.Source != "adhd-tui" {
		t.Fatalf("probe source = %q, want adhd-tui", result.Source)
	}
	if got := state.callCount(ToolFeaturesVerify); got != 1 {
		t.Fatalf("verify called %d times, want 1", got)
	}
	rendered := strings.Join(RenderVerify(result), "\n")
	if !strings.Contains(rendered, "isotope rotated") || !strings.Contains(rendered, "iso-2") {
		t.Fatalf("verify render incomplete:\n%s", rendered)
	}
}

func TestVerifyNotServed(t *testing.T) {
	srv, _ := newFakeADHD(t, fakeADHD{
		tools: []string{ToolFeaturesCertified},
		results: map[string]json.RawMessage{
			ToolFeaturesCertified: json.RawMessage(`{"caller_rung":3,"features":[]}`),
		},
	})
	browser := newBrowser([]Source{{Instance: "adhd", Endpoint: endpointOf(srv), Origin: "mdns"}})

	_, err := browser.Verify(context.Background(), "adhd/mdns-discovery")
	if err == nil || !strings.Contains(err.Error(), "not served by any source") {
		t.Fatalf("verify on a declared-absent tool: %v", err)
	}
}

func TestVerifyNoSources(t *testing.T) {
	browser := newBrowser(nil)
	if _, err := browser.Verify(context.Background(), "adhd/mdns-discovery"); err == nil {
		t.Fatal("verify with no sources succeeded")
	}
}

func TestViewCachesWithinRefresh(t *testing.T) {
	srv, state := newFakeADHD(t, fakeADHD{
		tools: []string{ToolFeaturesCertified},
		results: map[string]json.RawMessage{
			ToolFeaturesCertified: json.RawMessage(`{"features":[]}`),
		},
	})
	browser := newBrowser([]Source{{Instance: "adhd", Endpoint: endpointOf(srv), Origin: "mdns"}},
		func(o *Options) { o.Refresh = time.Hour })

	browser.View(context.Background())
	browser.View(context.Background())

	if got := state.initCount(); got != 1 {
		t.Fatalf("initialize called %d times inside the cache window, want 1", got)
	}
}

func TestConfigSourcesAndValidation(t *testing.T) {
	sources := ConfigSources([]config.FeatureEndpoint{
		{Name: "tui", URL: "http://127.0.0.1:9090"},
		{URL: "https://adhd.example/mcp"},
		{Name: "", URL: "  "},
	})
	if len(sources) != 2 {
		t.Fatalf("expected 2 usable configured sources, got %+v", sources)
	}
	if sources[0].Instance != "tui" || sources[0].Endpoint != "http://127.0.0.1:9090/mcp" {
		t.Fatalf("named endpoint not normalized: %+v", sources[0])
	}
	if sources[1].Instance != "adhd.example" {
		t.Fatalf("unnamed endpoint labeled %q, want host", sources[1].Instance)
	}

	problems := ValidateConfig(config.FeatureBrowserConfig{
		Endpoints: []config.FeatureEndpoint{
			{Name: "bad-scheme", URL: "ftp://adhd/mcp"},
			{Name: "no-host", URL: "http:///mcp"},
			{Name: "empty", URL: ""},
			{Name: "fine", URL: "http://127.0.0.1:9090/mcp"},
		},
		Refresh: "soon",
	})
	if len(problems) != 4 {
		t.Fatalf("expected 4 config problems, got %d: %v", len(problems), problems)
	}
	if got := ValidateConfig(config.FeatureBrowserConfig{Refresh: "30s"}); len(got) != 0 {
		t.Fatalf("valid config reported problems: %v", got)
	}
	if got := ValidateConfig(config.FeatureBrowserConfig{}); len(got) != 0 {
		t.Fatalf("absent refresh should defer to the default: %v", got)
	}
}

// TestDiscoveredSourcesCarriesIsotopeIdentity pins that the discovered set is
// adhd's own advertised identity: role and trust_rung describe the peer, never
// the caller.
func TestDiscoveredSourcesCarriesIsotopeIdentity(t *testing.T) {
	sources := DiscoveredSources([]mdns.Endpoint{{
		Host:       "10.0.0.2",
		Port:       9090,
		Identities: []mdns.Identity{{Role: "adhd", TrustRung: 3}},
		Services: []mdns.Service{{
			Name:        "adhd-tui._adhd-isotope._tcp.local.",
			ServiceType: "_adhd-isotope._tcp",
			Host:        "10.0.0.2",
			Port:        9090,
		}},
	}})
	if len(sources) != 1 {
		t.Fatalf("expected 1 source, got %+v", sources)
	}
	if sources[0].Instance != "adhd-tui" || sources[0].Role != "adhd" || sources[0].TrustRung != 3 {
		t.Fatalf("advertised identity not carried: %+v", sources[0])
	}
	if sources[0].Origin != "mdns" {
		t.Fatalf("origin = %q, want mdns", sources[0].Origin)
	}
	if sources[0].Endpoint != "http://10.0.0.2:9090/mcp" {
		t.Fatalf("endpoint = %q, want the conventional /mcp route", sources[0].Endpoint)
	}
}
