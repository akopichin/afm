package accounting

import (
	"strings"
	"testing"
)

// reportObsA is a realistic metered claude-cli invocation of a known built-in model, used to build a
// couple of stage-attributed records for the golden report test.
func reportObsA() Observation {
	return Observation{
		Metered: true,
		Schema:  SchemaAnthropic,
		Channel: "claude-cli",
		Model:   "glm-5.3",
		Tokens: Tokens{
			UncachedInput: 10000,
			CacheRead:     5000,
			CacheWrite5m:  2000,
			Output:        3000,
		},
	}
}

// reportObsOverhead stands in for a run-overhead memory-pipeline invocation.
func reportObsOverhead() Observation {
	return Observation{
		Metered: true,
		Schema:  SchemaAnthropic,
		Channel: "claude-cli",
		Model:   "glm-5.3",
		Tokens: Tokens{
			UncachedInput: 1000,
			Output:        500,
		},
	}
}

func TestRenderMarkdown_Golden(t *testing.T) {
	r := NewResolver(PricingConfig{})
	led := &Ledger{}
	led.Add(BuildRecord(r, reportObsA(), "backend", "implementation", ""))
	led.Add(BuildRecord(r, reportObsA(), "backend", "review", ""))
	led.Add(BuildRecord(r, reportObsOverhead(), "", "memory_update", ScopeRunOverhead))

	stages := map[string]StageInfo{
		"backend": {Status: "done", Duration: "12m34s"},
	}

	got := RenderMarkdown("myflow-20260914-100000", stages, led, RenderNormal, true)

	for _, want := range []string{
		"# Cost report: myflow-20260914-100000",
		"## Stages",
		"backend",
		"implementation×1",
		"review×1",
		"glm-5.3",
		"done (12m34s)",
		"## Run overhead",
		"memory_update×1",
		"## Total",
		"## Coverage and pricing",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report missing %q; got:\n%s", want, got)
		}
	}

	// Token/cost math must come straight from the ledger's own summaries —
	// two identical reportObsA invocations fold to double the tokens.
	wantUncached := reportObsA().Tokens.UncachedInput * 2
	if !strings.Contains(got, "20000") {
		t.Errorf("expected doubled uncached-input tokens (%d) in report; got:\n%s", wantUncached, got)
	}
}

func TestRenderMarkdown_ShowMoneyFalse(t *testing.T) {
	r := NewResolver(PricingConfig{})
	led := &Ledger{}
	led.Add(BuildRecord(r, reportObsA(), "backend", "implementation", ""))
	led.Add(BuildRecord(r, reportObsOverhead(), "", "memory_update", ScopeRunOverhead))

	// A mismatch record so the coverage appendix's dollar figures are exercised.
	obs := reportObsA()
	rr, ok := r.Resolve(obs.Channel, obs.Model)
	if !ok {
		t.Fatal("expected glm-5.3 to resolve for this test's fixture")
	}
	reported := rr.cost(obs.Tokens) + costToleranceUSD + 0.05
	obs.ReportedCostUSD = &reported
	led.Add(BuildRecord(r, obs, "backend", "review", ""))

	stages := map[string]StageInfo{"backend": {Status: "done", Duration: "12m34s"}}

	got := RenderMarkdown("myflow-20260914-100000", stages, led, RenderNormal, false)

	// Tokens must still be present.
	for _, want := range []string{
		"Total Tokens",
		"10000", // uncached-input tokens of reportObsA
		"reported_cost_differs_from_estimate",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("show_money=false report missing token/coverage content %q; got:\n%s", want, got)
		}
	}

	// No money whatsoever.
	for _, bad := range []string{"$", "Est. Cost", "Estimated cost"} {
		if strings.Contains(got, bad) {
			t.Errorf("show_money=false report must not contain %q; got:\n%s", bad, got)
		}
	}
}

func TestRenderMarkdown_OverheadPhaseBreakdownShowsActualCounts(t *testing.T) {
	r := NewResolver(PricingConfig{})
	led := &Ledger{}
	led.Add(BuildRecord(r, reportObsOverhead(), "", "memory_update", ScopeRunOverhead))

	got := RenderMarkdown("myflow-20260914-100000", nil, led, RenderNormal, true)

	idx := strings.Index(got, "## Run overhead")
	if idx == -1 {
		t.Fatalf("report missing \"## Run overhead\" section; got:\n%s", got)
	}
	overheadSection := got[idx:]

	if !strings.Contains(overheadSection, "memory_update×1") {
		t.Errorf("expected the overhead section to show the real phase/invocation count \"memory_update×1\"; got:\n%s", overheadSection)
	}
	if strings.Contains(overheadSection, "(—)") {
		t.Errorf("overhead section must not fall back to \"(—)\" when it has real records; got:\n%s", overheadSection)
	}
}

func TestRenderMarkdown_NoUsageData(t *testing.T) {
	for name, led := range map[string]*Ledger{
		"nil ledger":   nil,
		"empty ledger": {},
	} {
		t.Run(name, func(t *testing.T) {
			got := RenderMarkdown("myflow-20260914-100000", nil, led, RenderNormal, true)
			if !strings.Contains(got, "No usage data") {
				t.Errorf("expected a clean \"No usage data\" report; got:\n%s", got)
			}
			if strings.Contains(got, "$0.00") {
				t.Errorf("must never render a bogus $0.00; got:\n%s", got)
			}
		})
	}
}

func TestRenderMarkdown_CoverageAndPricing(t *testing.T) {
	r := NewResolver(PricingConfig{})

	unmeteredRec := BuildRecord(r, Observation{
		Metered: false,
		Reason:  "subscription plan, no per-token billing",
	}, "docs", "planning", "")

	unpricedRec := BuildRecord(r, Observation{
		Metered: true,
		Schema:  SchemaAnthropic,
		Channel: "claude-cli",
		Model:   "some-unknown-model",
		Tokens:  Tokens{Output: 10},
	}, "docs", "implementation", "")

	obs := reportObsA()
	rr, ok := r.Resolve(obs.Channel, obs.Model)
	if !ok {
		t.Fatal("expected glm-5.3 to resolve for this test's fixture")
	}
	estimate := rr.cost(obs.Tokens)
	reported := estimate + costToleranceUSD + 0.05
	obs.ReportedCostUSD = &reported
	mismatchRec := BuildRecord(r, obs, "backend", "review", "")

	led := &Ledger{}
	led.Add(unmeteredRec)
	led.Add(unpricedRec)
	led.Add(mismatchRec)

	got := RenderMarkdown("myflow-20260914-100000", nil, led, RenderNormal, true)

	for _, want := range []string{
		"## Coverage and pricing",
		"subscription plan, no per-token billing",
		"some-unknown-model",
		"docs",
		"backend",
		"reported_cost_differs_from_estimate",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("coverage appendix missing %q; got:\n%s", want, got)
		}
	}
}

func TestRenderMarkdown_CoverageGroupsIdenticalGapsWithCount(t *testing.T) {
	r := NewResolver(PricingConfig{})
	unpricedRec := BuildRecord(r, Observation{
		Metered: true,
		Schema:  SchemaAnthropic,
		Channel: "claude-cli",
		Model:   "some-unknown-model",
		Tokens:  Tokens{Output: 10},
	}, "docs", "implementation", "")

	led := &Ledger{}
	led.Add(unpricedRec)
	led.Add(unpricedRec)

	got := RenderMarkdown("myflow-20260914-100000", nil, led, RenderNormal, true)

	idx := strings.Index(got, "## Coverage and pricing")
	if idx == -1 {
		t.Fatalf("report missing \"## Coverage and pricing\" section; got:\n%s", got)
	}
	coverage := got[idx:]

	if !strings.Contains(coverage, "×2") {
		t.Errorf("expected the grouped unpriced line to show ×2 for two identical records; got:\n%s", coverage)
	}
	if strings.Count(coverage, "some-unknown-model") != 1 {
		t.Errorf("expected exactly one grouped line for the two identical unpriced records in the coverage appendix; got:\n%s", coverage)
	}
}

func TestRenderMarkdown_DegradedCorrupt(t *testing.T) {
	stages := map[string]StageInfo{"backend": {Status: "done", Duration: "12m34s"}}

	got := RenderMarkdown("myflow-20260914-100000", stages, nil, RenderCorrupt, true)

	for _, want := range []string{
		"# Cost report: myflow-20260914-100000",
		"backend",
		"done (12m34s)",
		"Cost unavailable",
		"corrupt",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("degraded corrupt report missing %q; got:\n%s", want, got)
		}
	}
	if strings.Contains(got, "No usage data") {
		t.Errorf("corrupt report must not read as plain \"No usage data\"; got:\n%s", got)
	}
	if strings.Contains(got, "$0.00") || strings.Contains(got, "## Total") {
		t.Errorf("corrupt report must not invent any totals; got:\n%s", got)
	}
}

func TestRenderMarkdown_DegradedUnreadable(t *testing.T) {
	stages := map[string]StageInfo{"backend": {Status: "running"}}

	got := RenderMarkdown("myflow-20260914-100000", stages, nil, RenderUnreadable, true)

	for _, want := range []string{
		"# Cost report: myflow-20260914-100000",
		"backend",
		"Cost unavailable",
		"cannot read",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("degraded unreadable report missing %q; got:\n%s", want, got)
		}
	}
	if strings.Contains(got, "corrupt") {
		t.Errorf("an \"other read error\" report must never claim \"corrupt\"; got:\n%s", got)
	}
	if strings.Contains(got, "No usage data") || strings.Contains(got, "## Total") {
		t.Errorf("unreadable report must not read as missing, and must not invent totals; got:\n%s", got)
	}
}

func TestLedger_CoverageIssues_SumsCountAcrossIdenticalRecords(t *testing.T) {
	r := NewResolver(PricingConfig{})
	rec := BuildRecord(r, Observation{
		Metered: true,
		Schema:  SchemaAnthropic,
		Channel: "claude-cli",
		Model:   "some-unknown-model",
		Tokens:  Tokens{Output: 10},
	}, "docs", "implementation", "")

	led := &Ledger{}
	led.Add(rec)
	led.Add(rec)

	issues := led.CoverageIssues()
	if len(issues) != 1 {
		t.Fatalf("expected exactly one grouped issue, got %d: %+v", len(issues), issues)
	}
	if issues[0].Count != 2 {
		t.Errorf("expected Count==2 for two identical unpriced records, got %d", issues[0].Count)
	}
}
