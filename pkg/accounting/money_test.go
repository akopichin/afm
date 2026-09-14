package accounting

import "testing"

func TestCostUSD(t *testing.T) {
	// 48583 uncached @ $1.40/M + 47616 cache read @ $0.26/M + 1130 output @ $4.40/M
	got := costUSD(48583, 1.40) + costUSD(47616, 0.26) + costUSD(1130, 4.40)
	want := 0.08536836
	if diff := got - want; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("cost = %.9f want %.9f", got, want)
	}
}

func TestFormatUSD(t *testing.T) {
	cases := []struct {
		v      float64
		priced bool
		want   string
	}{
		{0.08536836, true, "$0.0854"},
		{0.00004, true, "<$0.0001"},
		{0, true, "$0.0000"},
		{0.5, false, "—"},
	}
	for _, c := range cases {
		if got := FormatUSD(c.v, c.priced); got != c.want {
			t.Errorf("FormatUSD(%v,%v) = %q want %q", c.v, c.priced, got, c.want)
		}
	}
}
