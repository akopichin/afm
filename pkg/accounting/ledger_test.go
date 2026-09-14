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
