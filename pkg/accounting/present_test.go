package accounting

import "testing"

func sum(metered, unpriced, unmetered int, cost float64) Summary {
	return Summary{Metered: metered, Unpriced: unpriced, Unmetered: unmetered, CostUSD: cost}
}

func TestCoverageOf(t *testing.T) {
	cases := []struct{ name string; s Summary; want Coverage }{
		{"all priced", sum(2, 0, 0, 0.1), CoverageFull},
		{"single unpriced", sum(1, 1, 0, 0), CoverageNone},           // metered=1,unpriced=1 → priced=0
		{"priced+unpriced", sum(2, 1, 0, 0.1), CoveragePartial},
		{"priced+unmetered", sum(1, 0, 1, 0.1), CoveragePartial},
		{"zero-rate full", sum(1, 0, 0, 0), CoverageFull},            // priced, $0
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
	cases := []struct{ name string; s Summary; want string }{
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
		{Metered: true, Priced: false, StageID: "run_overhead", Phase: "planning", Model: "m"},   // a STAGE named run_overhead
		{Metered: true, Priced: false, Scope: ScopeRunOverhead, Phase: "planning", Model: "m"},    // real overhead
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
