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
