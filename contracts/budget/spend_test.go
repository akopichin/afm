package budget

import "testing"

func TestMoneyNanoUSD(t *testing.T) {
	cases := map[float64]int64{0.1: 100_000_000, 1: 1_000_000_000, 0.0000000004: 0, 0.0000000006: 1, -1: 0}
	for in, want := range cases {
		if got := MoneyNanoUSD(in); got != want {
			t.Errorf("MoneyNanoUSD(%v) = %d, want %d", in, got, want)
		}
	}
	if MoneyNanoUSD(1e30) <= 0 {
		t.Error("huge amounts must saturate, not wrap")
	}
}

func TestSpendAdd(t *testing.T) {
	a := Spend{Tokens: 1, MoneyNanoUSD: 2, Records: 1, Unmetered: 1}
	b := Spend{Tokens: 10, MoneyNanoUSD: 20, Records: 2, Unpriced: 1, NotExplicit: 2}
	got := a.Add(b)
	want := Spend{Tokens: 11, MoneyNanoUSD: 22, Records: 3, Unmetered: 1, Unpriced: 1, NotExplicit: 2}
	if got != want || got != b.Add(a) {
		t.Fatalf("Add = %+v, want %+v", got, want)
	}
	if got.TokensExact() || got.MoneyExact() || !(Spend{Tokens: 5}).MoneyExact() || !(Spend{Unpriced: 1}).TokensExact() {
		t.Error("exactness flags wrong")
	}
	if (Spend{MoneyNanoUSD: 1_500_000_000}).MoneyUSD() != 1.5 {
		t.Error("MoneyUSD conversion wrong")
	}
}

func TestTenTimesTenCentsReachOneDollar(t *testing.T) {
	var spent Spend
	for range 10 {
		spent = spent.Add(Spend{MoneyNanoUSD: MoneyNanoUSD(0.10), Records: 1})
	}
	if got := Evaluate(Spec{MoneyUSD: 1}, spent).Money; got != LevelReached {
		t.Fatalf("10 x $0.10 vs $1 limit = %q, want reached", got)
	}
}

func TestEvaluate(t *testing.T) {
	cases := []struct {
		name   string
		limit  Spec
		spent  Spend
		tokens Level
		money  Level
		worst  Level
	}{
		{"nothing limited", Spec{}, Spend{Tokens: 5}, LevelNone, LevelNone, LevelNone},
		{"below 80%", Spec{Tokens: 100}, Spend{Tokens: 79}, LevelOK, LevelNone, LevelOK},
		{"exactly 80%", Spec{Tokens: 100}, Spend{Tokens: 80}, LevelWarning, LevelNone, LevelWarning},
		{"just below limit", Spec{Tokens: 100}, Spend{Tokens: 99}, LevelWarning, LevelNone, LevelWarning},
		{"exactly limit", Spec{Tokens: 100}, Spend{Tokens: 100}, LevelReached, LevelNone, LevelReached},
		{"over limit", Spec{Tokens: 100}, Spend{Tokens: 150}, LevelReached, LevelNone, LevelReached},
		{"odd limit threshold", Spec{Tokens: 7}, Spend{Tokens: 6}, LevelWarning, LevelNone, LevelWarning},
		{"money 80%", Spec{MoneyUSD: 10}, Spend{MoneyNanoUSD: 8_000_000_000}, LevelNone, LevelWarning, LevelWarning},
		{"money reached", Spec{MoneyUSD: 10}, Spend{MoneyNanoUSD: 10_000_000_000}, LevelNone, LevelReached, LevelReached},
		{"either metric wins", Spec{Tokens: 100, MoneyUSD: 1}, Spend{Tokens: 10, MoneyNanoUSD: 1_000_000_000}, LevelOK, LevelReached, LevelReached},
		{"tiny money limit", Spec{MoneyUSD: 1e-12}, Spend{MoneyNanoUSD: 1}, LevelNone, LevelReached, LevelReached},
	}
	for _, c := range cases {
		got := Evaluate(c.limit, c.spent)
		if got.Tokens != c.tokens || got.Money != c.money || got.Worst() != c.worst {
			t.Errorf("%s: got %+v worst %q", c.name, got, got.Worst())
		}
	}
}
