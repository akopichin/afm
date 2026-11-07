package accounting

import (
	"encoding/json"
	"testing"
)

func sum(metered, unpriced, unmetered int, cost float64) Summary {
	return Summary{Metered: metered, Unpriced: unpriced, Unmetered: unmetered, CostUSD: cost}
}

func TestCoverageOf(t *testing.T) {
	cases := []struct {
		name string
		s    Summary
		want Coverage
	}{
		{"all priced", sum(2, 0, 0, 0.1), CoverageFull},
		{"single unpriced", sum(1, 1, 0, 0), CoverageNone}, // metered=1,unpriced=1 → priced=0
		{"priced+unpriced", sum(2, 1, 0, 0.1), CoveragePartial},
		{"priced+unmetered", sum(1, 0, 1, 0.1), CoveragePartial},
		{"zero-rate full", sum(1, 0, 0, 0), CoverageFull}, // priced, $0
		{"zero-rate partial", sum(2, 1, 0, 0), CoveragePartial},
		{"nothing", sum(0, 0, 0, 0), CoverageNone},
	}
	for _, c := range cases {
		if got := coverageOf(c.s); got != c.want {
			t.Errorf("%s: coverageOf = %q want %q", c.name, got, c.want)
		}
	}
}

func TestDisplayCost(t *testing.T) {
	cases := []struct {
		name string
		s    Summary
		want string
	}{
		{"full", sum(1, 0, 0, 0.3995), "$0.3995"},
		{"full zero", sum(1, 0, 0, 0), "$0.0000"},
		{"full tiny", sum(1, 0, 0, 0.00004), "<$0.0001"},
		{"partial", sum(2, 1, 0, 0.3995), "$0.3995+"},
		{"partial zero", sum(2, 1, 0, 0), "$0.0000+"},
		{"partial tiny", sum(2, 1, 0, 0.00004), "<$0.0001+"},
		{"none", sum(1, 1, 0, 0), "—"},
	}
	for _, c := range cases {
		if got := DisplayCost(c.s); got != c.want {
			t.Errorf("%s: DisplayCost = %q want %q", c.name, got, c.want)
		}
	}
}

func TestAttributionIdentityDistinct(t *testing.T) {
	recs := []UsageRecord{
		{Metered: true, Priced: false, StageID: "run_overhead", Phase: "planning", Model: "m"}, // a STAGE named run_overhead
		{Metered: true, Priced: false, Scope: ScopeRunOverhead, Phase: "planning", Model: "m"}, // real overhead
	}
	got := aggregateIssues(recs)
	if len(got) != 2 {
		t.Fatalf("want 2 distinct groups, got %d: %+v", len(got), got)
	}
}

func TestAggregateCountsAndStableOrder(t *testing.T) {
	r := UsageRecord{Metered: true, Priced: false, StageID: "s", Phase: "impl", Model: "codex"}
	a := aggregateIssues([]UsageRecord{r, r, r})
	if len(a) != 1 || a[0].Count != 3 {
		t.Fatalf("want 1 issue count=3, got %+v", a)
	}
	// stable order: same slice regardless of input order
	x := aggregateIssues([]UsageRecord{{Metered: true, StageID: "b", Phase: "p"}, {Priced: false, Metered: true, StageID: "a", Phase: "p", Model: "m"}})
	y := aggregateIssues([]UsageRecord{{Priced: false, Metered: true, StageID: "a", Phase: "p", Model: "m"}, {Metered: true, StageID: "b", Phase: "p"}})
	// only unpriced/unmetered are issues; assert equal ordering of whatever groups exist
	if len(x) != len(y) {
		t.Fatalf("len differ %d vs %d", len(x), len(y))
	}
	for i := range x {
		if x[i] != y[i] {
			t.Fatalf("order not stable at %d: %+v vs %+v", i, x[i], y[i])
		}
	}
}

func TestCacheHitRatioPtr(t *testing.T) {
	cases := []struct {
		name string
		t    Tokens
		want *float64
	}{
		{
			"zero input returns nil",
			Tokens{UncachedInput: 0, CacheRead: 0, CacheWrite5m: 0, CacheWrite1h: 0, CacheWriteOther: 0},
			nil,
		},
		{
			"uncached input only returns pointer to 0",
			Tokens{UncachedInput: 100, CacheRead: 0},
			ptrFloat64(0),
		},
		{
			"cached input returns non-zero pointer",
			Tokens{UncachedInput: 100, CacheRead: 50},
			ptrFloat64(0.3333333333333333), // 50 / (100 + 50)
		},
		{
			"cache write counts as input",
			Tokens{UncachedInput: 100, CacheWrite5m: 50},
			ptrFloat64(0), // 0 / (100 + 50) = 0
		},
		{
			"cache hit on cache-write input",
			Tokens{UncachedInput: 100, CacheRead: 50, CacheWrite5m: 50},
			ptrFloat64(0.25), // 50 / (100 + 50 + 50) = 0.25
		},
	}

	for _, c := range cases {
		got := cacheHitRatioPtr(c.t)
		if c.want == nil {
			if got != nil {
				t.Errorf("%s: cacheHitRatioPtr = %v, want nil", c.name, got)
			}
		} else {
			if got == nil {
				t.Errorf("%s: cacheHitRatioPtr = nil, want %v", c.name, *c.want)
			} else if *got != *c.want {
				t.Errorf("%s: cacheHitRatioPtr = %v, want %v", c.name, *got, *c.want)
			}
		}
	}
}

func TestBuildCostView(t *testing.T) {
	t.Run("partial summary", func(t *testing.T) {
		s := Summary{
			Tokens: Tokens{
				UncachedInput: 100,
				CacheRead:     50,
				CacheWrite5m:  25,
				Output:        30,
			},
			CostUSD:   0.15,
			Priced:    false,
			Models:    []string{"claude-3-sonnet"},
			Metered:   5,
			Unmetered: 0,
			Unpriced:  2,
		}
		phases := map[string]int{"planning": 2, "implementation": 3}

		view := BuildCostView(s, phases)

		// Check display_cost ends with +
		if !endsWith(view.DisplayCost, "+") {
			t.Errorf("DisplayCost = %q, want to end with +", view.DisplayCost)
		}

		// Check coverage is partial
		if view.Coverage != string(CoveragePartial) {
			t.Errorf("Coverage = %q, want %q", view.Coverage, CoveragePartial)
		}

		// Check priced_invocations = metered - unpriced
		expectedPriced := s.Metered - s.Unpriced
		if view.PricedInvocations != expectedPriced {
			t.Errorf("PricedInvocations = %d, want %d", view.PricedInvocations, expectedPriced)
		}

		// Check estimated_cost_usd
		if view.EstimatedCostUSD != s.CostUSD {
			t.Errorf("EstimatedCostUSD = %v, want %v", view.EstimatedCostUSD, s.CostUSD)
		}

		// Check metered, unpriced, unmetered
		if view.Metered != s.Metered {
			t.Errorf("Metered = %d, want %d", view.Metered, s.Metered)
		}
		if view.Unpriced != s.Unpriced {
			t.Errorf("Unpriced = %d, want %d", view.Unpriced, s.Unpriced)
		}
		if view.Unmetered != s.Unmetered {
			t.Errorf("Unmetered = %d, want %d", view.Unmetered, s.Unmetered)
		}

		// Check token fields
		if view.UncachedInput != s.Tokens.UncachedInput {
			t.Errorf("UncachedInput = %d, want %d", view.UncachedInput, s.Tokens.UncachedInput)
		}
		if view.CacheRead != s.Tokens.CacheRead {
			t.Errorf("CacheRead = %d, want %d", view.CacheRead, s.Tokens.CacheRead)
		}
		if view.Output != s.Tokens.Output {
			t.Errorf("Output = %d, want %d", view.Output, s.Tokens.Output)
		}

		// Check derived token fields
		expectedTotal := s.Tokens.Total()
		if view.TotalTokens != expectedTotal {
			t.Errorf("TotalTokens = %d, want %d", view.TotalTokens, expectedTotal)
		}

		expectedCacheWriteTotal := s.Tokens.CacheWriteTotal()
		if view.CacheWriteTotal != expectedCacheWriteTotal {
			t.Errorf("CacheWriteTotal = %d, want %d", view.CacheWriteTotal, expectedCacheWriteTotal)
		}

		// Check phases
		if len(view.Phases) != len(phases) {
			t.Errorf("Phases len = %d, want %d", len(view.Phases), len(phases))
		}
		for k, v := range phases {
			if view.Phases[k] != v {
				t.Errorf("Phases[%q] = %d, want %d", k, view.Phases[k], v)
			}
		}

		// Check models
		if len(view.Models) != len(s.Models) {
			t.Errorf("Models len = %d, want %d", len(view.Models), len(s.Models))
		}
		if len(view.Models) > 0 && view.Models[0] != s.Models[0] {
			t.Errorf("Models[0] = %q, want %q", view.Models[0], s.Models[0])
		}
	})

	t.Run("cache_hit_ratio nil when InputTotal==0", func(t *testing.T) {
		s := Summary{
			Tokens:    Tokens{UncachedInput: 0, CacheRead: 0},
			CostUSD:   0,
			Metered:   0,
			Unmetered: 1,
		}
		view := BuildCostView(s, nil)
		if view.CacheHitRatio != nil {
			t.Errorf("CacheHitRatio = %v, want nil", view.CacheHitRatio)
		}
	})

	t.Run("cache_hit_ratio zero pointer for uncached input only", func(t *testing.T) {
		s := Summary{
			Tokens:  Tokens{UncachedInput: 100, CacheRead: 0},
			CostUSD: 0,
			Metered: 1,
		}
		view := BuildCostView(s, nil)
		if view.CacheHitRatio == nil {
			t.Error("CacheHitRatio = nil, want non-nil")
		} else if *view.CacheHitRatio != 0 {
			t.Errorf("CacheHitRatio = %v, want 0", *view.CacheHitRatio)
		}
	})

	t.Run("cache_hit_ratio non-zero for cached input", func(t *testing.T) {
		s := Summary{
			Tokens:  Tokens{UncachedInput: 100, CacheRead: 50},
			CostUSD: 0,
			Metered: 1,
		}
		view := BuildCostView(s, nil)
		if view.CacheHitRatio == nil {
			t.Error("CacheHitRatio = nil, want non-nil")
		} else if *view.CacheHitRatio == 0 {
			t.Error("CacheHitRatio = 0, want non-zero")
		}
	})

	t.Run("JSON marshaling with snake_case and null cache_hit_ratio", func(t *testing.T) {
		// Test with nil cache_hit_ratio
		s1 := Summary{
			Tokens:   Tokens{UncachedInput: 0},
			CostUSD:  0.5,
			Metered:  1,
			Unpriced: 0,
		}
		view1 := BuildCostView(s1, map[string]int{"planning": 1})
		data1, err := json.Marshal(view1)
		if err != nil {
			t.Fatalf("json.Marshal failed: %v", err)
		}
		str1 := string(data1)
		if !contains(str1, `"cache_hit_ratio":null`) {
			t.Errorf("JSON missing null cache_hit_ratio: %s", str1)
		}
		if !contains(str1, `"display_cost"`) {
			t.Errorf("JSON missing display_cost key: %s", str1)
		}
		if !contains(str1, `"priced_invocations"`) {
			t.Errorf("JSON missing priced_invocations key: %s", str1)
		}

		// Test with non-nil cache_hit_ratio
		s2 := Summary{
			Tokens:  Tokens{UncachedInput: 100, CacheRead: 50},
			CostUSD: 0.5,
			Metered: 1,
		}
		view2 := BuildCostView(s2, nil)
		data2, err := json.Marshal(view2)
		if err != nil {
			t.Fatalf("json.Marshal failed: %v", err)
		}
		str2 := string(data2)
		if contains(str2, `"cache_hit_ratio":null`) {
			t.Errorf("JSON should not have null cache_hit_ratio: %s", str2)
		}
		// Should have a numeric value
		if !contains(str2, `"cache_hit_ratio":`) {
			t.Errorf("JSON missing cache_hit_ratio key: %s", str2)
		}
	})
}

func ptrFloat64(f float64) *float64 {
	return &f
}

func endsWith(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}

func contains(s, substr string) bool {
	for i := 0; i < len(s)-len(substr)+1; i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
