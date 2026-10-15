package analysis_test

import (
	"strings"
	"testing"

	"github.com/SanishKumar/go-microservices/backdraft"
	"github.com/SanishKumar/go-microservices/backdraft/analysis"
	"github.com/SanishKumar/go-microservices/internal/reference"
)

func byID(r analysis.Report) map[string]analysis.Finding {
	m := map[string]analysis.Finding{}
	for _, f := range r.Findings {
		m[f.ID] = f
	}
	return m
}

func TestReferenceSystemFindings(t *testing.T) {
	r := analysis.Analyze(analysis.Input{System: reference.System()})
	got := byID(r)

	amp, ok := got["amplification:account,catalog"]
	if !ok {
		t.Fatalf("missing amplification behind order; got %v", keys(got))
	}
	// users x2, gateway x3, order x3
	if amp.Metrics["sustained"] != 18 || amp.Severity != analysis.Critical {
		t.Errorf("amplification = %v (%s), want 18 critical", amp.Metrics["sustained"], amp.Severity)
	}
	if amp.Metrics["budgeted"] >= 3 {
		t.Errorf("budgeted amplification %v should collapse toward 1", amp.Metrics["budgeted"])
	}

	inv, ok := got["inversion:gateway>order"]
	if !ok {
		t.Fatalf("missing gateway>order timeout inversion")
	}
	// Statically, one dependency of order alone: 3 x 600ms + 25ms + 50ms.
	if inv.Metrics["worstMs"] != 1875 || inv.Severity != analysis.High {
		t.Errorf("inversion worst = %vms (%s), want 1875ms high (order orphans work)", inv.Metrics["worstMs"], inv.Severity)
	}

	for _, id := range []string{"orphaned-work", "unbudgeted-retries", "no-shedding", "unbounded-queue",
		"unsafe-retry:account.PostAccount", "unsafe-retry:catalog.PostProduct", "amplification:order"} {
		if _, ok := got[id]; !ok {
			t.Errorf("missing finding %s", id)
		}
	}
	// order.PostOrder dedupes on an idempotency key, so retrying it is fine.
	if _, ok := got["unsafe-retry:order.PostOrder"]; ok {
		t.Errorf("flagged PostOrder although it dedupes")
	}
	if f := got["unsafe-retry:account.PostAccount"]; f.Severity != analysis.Critical || len(f.Edges) != 2 {
		t.Errorf("unsafe retry finding = %+v", f)
	}
	for i := 1; i < len(r.Findings); i++ {
		if rank(r.Findings[i-1].Severity) > rank(r.Findings[i].Severity) {
			t.Fatalf("findings not ordered by severity")
		}
	}
}

func TestPatchesFixWhatTheyClaim(t *testing.T) {
	sys := reference.System()
	r := analysis.Analyze(analysis.Input{System: sys})
	for _, f := range r.Findings {
		if f.Patch == nil {
			continue
		}
		patched := apply(sys, f.Patch)
		after := byID(analysis.Analyze(analysis.Input{System: patched}))
		if _, still := after[f.ID]; still {
			t.Errorf("patch for %s did not resolve it", f.ID)
		}
	}
}

func TestMitigateAllLeavesOnlyCodeLevelFindings(t *testing.T) {
	sys := analysis.Mitigate(reference.System(), analysis.All())
	if m := analysis.Detect(sys); m != analysis.All() {
		t.Fatalf("Detect after Mitigate(All) = %+v", m)
	}
	// Users still refresh; that is not ours to change.
	if sys.Services[reference.Users].Client(reference.Gateway).MaxAttempts != 2 {
		t.Fatalf("Mitigate changed the users model")
	}
	for _, f := range analysis.Analyze(analysis.Input{System: sys}).Findings {
		switch f.Rule {
		case "unsafe-retry", "timeout-inversion":
			// Fixing these needs code or timeout changes, not mitigations.
		default:
			if f.Severity == analysis.Critical || f.Severity == analysis.High {
				t.Errorf("after all mitigations: %s %s: %s", f.Severity, f.ID, f.Title)
			}
		}
	}
}

func TestRetryCliffUsesObservedTraffic(t *testing.T) {
	sys := reference.System()
	obs := &analysis.Observed{
		EdgeRPS: map[analysis.Edge]float64{
			{From: "gateway", To: "catalog"}: 75,
			{From: "order", To: "catalog"}:   30,
		},
		HoldMs: map[string]float64{"catalog": 12},
	}
	got := byID(analysis.Analyze(analysis.Input{System: sys, Observed: obs}))
	f, ok := got["retry-cliff:catalog"]
	if !ok {
		t.Fatalf("missing retry cliff; got %v", keys(got))
	}
	// capacity 8 workers / 12ms = 667/s; worst = 75*3*2 + 30*3*6 = 990/s
	if c := f.Metrics["capacity"]; c < 666 || c > 667 {
		t.Errorf("capacity = %v", c)
	}
	if w := f.Metrics["worst"]; w != 990 {
		t.Errorf("worst = %v, want 990", w)
	}
	if f.Patch == nil {
		t.Errorf("retry cliff has no patch")
	}

	// Light traffic does not trip it.
	obs.EdgeRPS = map[analysis.Edge]float64{{From: "gateway", To: "catalog"}: 10}
	if _, ok := byID(analysis.Analyze(analysis.Input{System: sys, Observed: obs}))["retry-cliff:catalog"]; ok {
		t.Errorf("retry cliff reported at 10 req/s")
	}
}

func TestObservationsSharpenStaticFindings(t *testing.T) {
	sys := reference.System()
	obs := &analysis.Observed{
		Sequences: map[string][][]string{"order": {{"account", "catalog"}, {}}},
		Methods: map[analysis.Edge][]string{
			{From: "gateway", To: "account"}: {"GetAccount", "PostAccount"},
			{From: "order", To: "account"}:   {"GetAccount"},
			{From: "gateway", To: "catalog"}: {"GetProducts"},
			{From: "order", To: "catalog"}:   {"GetProducts"},
		},
	}
	got := byID(analysis.Analyze(analysis.Input{System: sys, Observed: obs}))
	// PostOrder calls account then catalog: 2 x 1875ms.
	if w := got["inversion:gateway>order"].Metrics["worstMs"]; w != 3750 {
		t.Errorf("observed inversion worst = %vms, want 3750", w)
	}
	// Only the gateway actually calls PostAccount, and nobody calls PostProduct.
	if f, ok := got["unsafe-retry:account.PostAccount"]; !ok || len(f.Edges) != 1 || f.Edges[0].From != "gateway" {
		t.Errorf("observed unsafe retry = %+v", f)
	}
	if _, ok := got["unsafe-retry:catalog.PostProduct"]; ok {
		t.Errorf("flagged PostProduct, which is never called")
	}
}

func TestNoTimeoutIsUnboundedInversion(t *testing.T) {
	sys := backdraft.SystemConfig{Entry: "a", Services: map[string]backdraft.ServiceConfig{
		"a": {Clients: map[string]backdraft.ClientPolicy{"b": {TimeoutMs: 100, MaxAttempts: 1, PropagateDeadline: true}}},
		"b": {Clients: map[string]backdraft.ClientPolicy{"c": {MaxAttempts: 1}}},
		"c": {},
	}}
	f, ok := byID(analysis.Analyze(analysis.Input{System: sys}))["inversion:a>b"]
	if !ok || !strings.Contains(f.Detail, "without bound") {
		t.Fatalf("want unbounded inversion, got %+v", f)
	}
}

func apply(sys backdraft.SystemConfig, p analysis.Patch) backdraft.SystemConfig {
	out := backdraft.SystemConfig{Entry: sys.Entry, Services: map[string]backdraft.ServiceConfig{}}
	for k, v := range sys.Services {
		out.Services[k] = v
	}
	for k, v := range p {
		out.Services[k] = v
	}
	return out
}

func rank(s analysis.Severity) int {
	return map[analysis.Severity]int{analysis.Critical: 0, analysis.High: 1, analysis.Medium: 2, analysis.Low: 3}[s]
}

func keys(m map[string]analysis.Finding) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
