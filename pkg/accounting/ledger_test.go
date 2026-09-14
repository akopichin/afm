package accounting

import "testing"

// obsGLM is a realistic metered claude-cli invocation of glm-5.3 (the same
// token shape as the spec's usage-record example).
func obsGLM() Observation {
	return Observation{
		Metered: true,
		Schema:  SchemaAnthropic,
		Channel: "claude-cli",
		Model:   "glm-5.3",
		Tokens: Tokens{
			UncachedInput: 48583,
			CacheRead:     47616,
			Output:        1130,
		},
	}
}

// obsMemory is a small, distinct metered invocation standing in for a
// run-overhead memory-pipeline step (reflect/aggregate/prioritize/update).
func obsMemory() Observation {
	return Observation{
		Metered: true,
		Schema:  SchemaAnthropic,
		Channel: "claude-cli",
		Model:   "glm-5.3",
		Tokens: Tokens{
			UncachedInput: 2000,
			Output:        212,
		},
	}
}

func TestLedgerAggregation(t *testing.T) {
	r := NewResolver(PricingConfig{})
	l := &Ledger{}
	l.Add(BuildRecord(r, obsGLM(), "backend", "implementation", ""))
	l.Add(BuildRecord(r, obsGLM(), "backend", "review", ""))
	l.Add(BuildRecord(r, obsMemory(), "", "memory_update", "run_overhead"))

	byStage := l.SummaryByStage()
	if _, ok := byStage[""]; ok {
		t.Fatal("run_overhead must not appear as a stage")
	}
	if byStage["backend"].Tokens.Output != 1130*2 {
		t.Fatalf("stage output = %d", byStage["backend"].Tokens.Output)
	}
	run := l.RunSummary()
	// run total includes the two backend invocations + the run_overhead one
	if run.Tokens.Output != 1130*2+obsMemory().Tokens.Output {
		t.Fatalf("run output = %d", run.Tokens.Output)
	}
}

func TestBuildRecordUnknownModelUnpriced(t *testing.T) {
	r := NewResolver(PricingConfig{})
	rec := BuildRecord(r, Observation{Metered: true, Schema: SchemaAnthropic, Model: "mystery", Tokens: Tokens{Output: 10}}, "s", "planning", "")
	if rec.Priced {
		t.Fatal("unknown model must be unpriced")
	}
}

// TestBuildRecordPartialOverrideUnknownModelMissingCategoryUnpriced verifies
// FINDING 1's completeness rule end-to-end: a user config overrides a
// category on an UNKNOWN model (no builtin to fill the gap), but the
// observation actually uses a category with no configured rate. BuildRecord
// must mark the record unpriced (Priced=false, cost 0) rather than compute a
// misleadingly partial total that silently treats the uncovered category as
// free.
func TestBuildRecordPartialOverrideUnknownModelMissingCategoryUnpriced(t *testing.T) {
	four := Rate(4.0)
	twenty := Rate(20.0)
	// Only input/output configured — cache_read left unset, and there is no
	// builtin for "my-custom-model" to fall back to.
	cfg := PricingConfig{Models: map[string]RateCard{
		"my-custom-model": {Input: &four, Output: &twenty},
	}}
	r := NewResolver(cfg)
	obs := Observation{
		Metered: true,
		Schema:  SchemaAnthropic,
		Model:   "my-custom-model",
		Tokens:  Tokens{UncachedInput: 1000, CacheRead: 500, Output: 200},
	}
	rec := BuildRecord(r, obs, "s", "implementation", "")
	if rec.Priced {
		t.Fatal("expected unpriced record: cache_read tokens present but no cache_read rate and no builtin to fall back to")
	}
	if rec.EstimatedCostUSD != 0 {
		t.Fatalf("expected zero estimated cost for unpriced record, got %v", rec.EstimatedCostUSD)
	}
}

// TestBuildRecordBuiltinCacheWriteOtherIsPriced is a regression test for a
// bug introduced by the FINDING-1 fix: the builtin card() constructor never
// set the generic CacheWrite rate, only CacheWrite5m/CacheWrite1h. When an
// Anthropic usage line has no 5m/1h breakdown, normalize.go puts ALL
// cache-creation tokens into CacheWriteOther, which is priced ONLY by the
// generic CacheWrite rate (no TTL-specific fallback of its own, see
// ResolvedRate.missingCategory/cost). With CacheWrite left nil, a fully
// mainstream builtin model observation using only CacheWriteOther got
// silently flipped to unpriced. card() now sets CacheWrite = the 1h rate, so
// this must resolve priced with the CacheWriteOther bucket costed at that
// fallback rate.
func TestBuildRecordBuiltinCacheWriteOtherIsPriced(t *testing.T) {
	r := NewResolver(PricingConfig{})
	obs := Observation{
		Metered: true,
		Schema:  SchemaAnthropic,
		Model:   "claude-opus-4-8",
		Tokens: Tokens{
			UncachedInput:   100,
			CacheWriteOther: 1000, // no 5m/1h breakdown at all
			Output:          50,
		},
	}
	rec := BuildRecord(r, obs, "s", "implementation", "")
	if !rec.Priced {
		t.Fatal("expected priced record: CacheWriteOther must fall back to the builtin's generic CacheWrite rate")
	}
	// opus: input 5.00, cache_write (fallback for Other) 10.00, output 25.00.
	want := 100.0/1_000_000*5.00 + 1000.0/1_000_000*10.00 + 50.0/1_000_000*25.00
	if diff := rec.EstimatedCostUSD - want; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("cost = %.9f want %.9f", rec.EstimatedCostUSD, want)
	}
}

func TestBuildRecordReportedCostDiffersBeyondTolerance(t *testing.T) {
	r := NewResolver(PricingConfig{})
	obs := obsGLM()
	rr, ok := r.Resolve(obs.Channel, obs.Model)
	if !ok {
		t.Fatal("expected glm-5.3 to resolve")
	}
	estimate := rr.cost(obs.Tokens)
	reported := estimate + costToleranceUSD + 0.01 // well beyond tolerance
	obs.ReportedCostUSD = &reported

	rec := BuildRecord(r, obs, "backend", "implementation", "")
	if !rec.Priced {
		t.Fatal("expected priced record")
	}
	if !hasWarning(rec.Warnings, "reported_cost_differs_from_estimate") {
		t.Fatalf("expected reported_cost_differs_from_estimate warning, got %v", rec.Warnings)
	}
}

func TestBuildRecordReportedCostWithinTolerance(t *testing.T) {
	r := NewResolver(PricingConfig{})
	obs := obsGLM()
	rr, ok := r.Resolve(obs.Channel, obs.Model)
	if !ok {
		t.Fatal("expected glm-5.3 to resolve")
	}
	estimate := rr.cost(obs.Tokens)
	reported := estimate + costToleranceUSD/2 // well within tolerance
	obs.ReportedCostUSD = &reported

	rec := BuildRecord(r, obs, "backend", "implementation", "")
	if !rec.Priced {
		t.Fatal("expected priced record")
	}
	if hasWarning(rec.Warnings, "reported_cost_differs_from_estimate") {
		t.Fatalf("did not expect reported_cost_differs_from_estimate warning, got %v", rec.Warnings)
	}
}

func hasWarning(warnings []string, target string) bool {
	for _, w := range warnings {
		if w == target {
			return true
		}
	}
	return false
}
