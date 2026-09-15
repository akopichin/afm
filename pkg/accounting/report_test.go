package accounting

import (
	"strings"
	"testing"
)

// reportObsA is a realistic metered claude-cli invocation of a known
// built-in model, used to build a couple of stage-attributed records for the
// golden report test.
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

// TestRenderMarkdown_Golden builds a ledger with two stage-attributed
// records (implementation + review of the same stage) plus one
// run_overhead-scoped record, and asserts the rendered markdown contains the
// stage row, its per-phase invocation counts and token breakdown, the Run
// overhead section, the Total section, and the Coverage and pricing
// appendix — the shape described in the task brief.
func TestRenderMarkdown_Golden(t *testing.T) {
	r := NewResolver(PricingConfig{})
	led := &Ledger{}
	led.Add(BuildRecord(r, reportObsA(), "backend", "implementation", ""))
	led.Add(BuildRecord(r, reportObsA(), "backend", "review", ""))
	led.Add(BuildRecord(r, reportObsOverhead(), "", "memory_update", ScopeRunOverhead))

	stages := map[string]StageInfo{
		"backend": {Status: "done", Duration: "12m34s"},
	}

	got := RenderMarkdown("myflow-20260914-100000", stages, led, RenderNormal)

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

// TestRenderMarkdown_OverheadPhaseBreakdownShowsActualCounts is a targeted
// regression for a bug found in review: renderOverhead called
// phaseCounts(overhead, "") on a slice ALREADY filtered to
// Scope==ScopeRunOverhead, but phaseCounts itself ALSO unconditionally
// skipped any record with that scope (a guard meant to keep overhead out of
// the per-stage table) — so the Run overhead section's phase/invocation
// breakdown always rendered "—" instead of the real counts. This test would
// have failed against the buggy code (which printed "—" here) and must pass
// against the fix (phaseCounts no longer re-excludes what the caller
// already scoped).
func TestRenderMarkdown_OverheadPhaseBreakdownShowsActualCounts(t *testing.T) {
	r := NewResolver(PricingConfig{})
	led := &Ledger{}
	led.Add(BuildRecord(r, reportObsOverhead(), "", "memory_update", ScopeRunOverhead))

	got := RenderMarkdown("myflow-20260914-100000", nil, led, RenderNormal)

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

// TestRenderMarkdown_NoUsageData covers both shapes a run without recorded
// usage can take (a nil ledger, and a non-nil ledger with zero records —
// what accounting.Load returns for a run predating this feature) and
// asserts both render a clean, honest "no usage" report instead of crashing
// or showing a bogus $0.00.
func TestRenderMarkdown_NoUsageData(t *testing.T) {
	for name, led := range map[string]*Ledger{
		"nil ledger":   nil,
		"empty ledger": {},
	} {
		t.Run(name, func(t *testing.T) {
			got := RenderMarkdown("myflow-20260914-100000", nil, led, RenderNormal)
			if !strings.Contains(got, "No usage data") {
				t.Errorf("expected a clean \"No usage data\" report; got:\n%s", got)
			}
			if strings.Contains(got, "$0.00") {
				t.Errorf("must never render a bogus $0.00; got:\n%s", got)
			}
		})
	}
}

// TestRenderMarkdown_CoverageAndPricing exercises the coverage appendix's
// three call-outs: an unmetered invocation (with its reason), a metered but
// unpriced invocation (unknown model, no rate resolved), and a
// reported-vs-estimated cost mismatch surfaced via the record's own
// warnings — none of which are computed by the renderer itself, only read
// off the already-built UsageRecords.
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

	got := RenderMarkdown("myflow-20260914-100000", nil, led, RenderNormal)

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

// TestRenderMarkdown_CoverageGroupsIdenticalGapsWithCount verifies the
// coverage appendix now groups identical unmetered/unpriced records via
// aggregateIssues instead of listing one line per record — two identical
// unpriced records for the same stage/phase/channel/model must render as a
// single grouped line carrying "×2", not two separate lines each silently
// implying a count of 1.
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

	got := RenderMarkdown("myflow-20260914-100000", nil, led, RenderNormal)

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

// TestRenderMarkdown_DegradedCorrupt verifies the self-contained degraded
// shape for a corrupt ledger: no bare "No usage data", but a title, the
// caller-supplied FSM stage rows, and a note that plainly distinguishes
// "corrupt" from "unreadable" or "missing" — all of it on stdout (the return
// value here), since a caller redirecting `afm report > out.md` never sees
// stderr.
func TestRenderMarkdown_DegradedCorrupt(t *testing.T) {
	stages := map[string]StageInfo{"backend": {Status: "done", Duration: "12m34s"}}

	got := RenderMarkdown("myflow-20260914-100000", stages, nil, RenderCorrupt)

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

// TestRenderMarkdown_DegradedUnreadable mirrors
// TestRenderMarkdown_DegradedCorrupt for the "other read error" case — the
// note must never say "corrupt" for a cause that isn't a parse failure.
func TestRenderMarkdown_DegradedUnreadable(t *testing.T) {
	stages := map[string]StageInfo{"backend": {Status: "running"}}

	got := RenderMarkdown("myflow-20260914-100000", stages, nil, RenderUnreadable)

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

// TestLedger_CoverageIssues_SumsCountAcrossIdenticalRecords is the
// pkg/accounting-level companion to the CLI's coverageNote test: two
// identical unpriced records must aggregate into ONE CoverageIssue with
// Count==2, never two separate issues (which would make a naive len(issues)
// undercount).
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
