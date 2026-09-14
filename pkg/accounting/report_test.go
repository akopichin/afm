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

	got := RenderMarkdown("myflow-20260914-100000", stages, led)

	for _, want := range []string{
		"# Cost report: myflow-20260914-100000",
		"## Stages",
		"backend",
		"implementation×1",
		"review×1",
		"glm-5.3",
		"done (12m34s)",
		"## Run overhead",
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
			got := RenderMarkdown("myflow-20260914-100000", nil, led)
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

	got := RenderMarkdown("myflow-20260914-100000", nil, led)

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
