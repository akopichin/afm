package usageview

import (
	"encoding/json"
	"testing"

	"github.com/akopichin/afm/contracts/accounting"
)

func fullSpend() Spend {
	cost := 1.5
	return Spend{
		Tokens: &Tokens{Counts: &TokenCounts{Total: 10}, MeteredJobs: 1},
		Money:  &Money{EstimatedCostUSD: &cost, PricedJobs: 1},
	}
}

func fullMetrics() Metrics {
	return Metrics{Jobs: 1, Spend: fullSpend()}
}

// hasKey reports whether the key occurs as a JSON object key at any depth.
func hasKey(t *testing.T, v any, key string) bool {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		t.Fatal(err)
	}
	var walk func(any) bool
	walk = func(n any) bool {
		switch x := n.(type) {
		case map[string]any:
			for k, child := range x {
				if k == key || walk(child) {
					return true
				}
			}
		case []any:
			for _, child := range x {
				if walk(child) {
					return true
				}
			}
		default: // scalars have no keys
		}
		return false
	}
	return walk(tree)
}

func TestForModeHidesKeysAtAnyDepth(t *testing.T) {
	cov := Coverage{Status: CoverageFull}
	pieces := map[string]func(Mode) any{
		"metrics":      func(m Mode) any { return fullMetrics().ForMode(m) },
		"job":          func(m Mode) any { return Job{Spend: fullSpend()}.ForMode(m) },
		"jobs":         func(m Mode) any { return JobsForMode([]Job{{Spend: fullSpend()}}, m) },
		"rows":         func(m Mode) any { return RowsForMode([]Row{{Metrics: fullMetrics()}}, m) },
		"completeness": func(m Mode) any { return Completeness{Tokens: &cov, Price: &cov}.ForMode(m) },
	}
	tests := []struct {
		mode              Mode
		wantMoney, wantTk bool
	}{
		{ModeFull, true, true},
		{ModeNoMoney, false, true},
		{ModeTimeOnly, false, false},
	}
	for name, piece := range pieces {
		for _, tc := range tests {
			got := piece(tc.mode)
			moneyKey, priceKey := "money", "price"
			tokenKey := "tokens"
			if name == "completeness" {
				if hasKey(t, got, priceKey) != tc.wantMoney {
					t.Errorf("%s/%s: price key presence != %v", name, tc.mode, tc.wantMoney)
				}
			} else if hasKey(t, got, moneyKey) != tc.wantMoney {
				t.Errorf("%s/%s: money key presence != %v", name, tc.mode, tc.wantMoney)
			}
			if hasKey(t, got, tokenKey) != tc.wantTk {
				t.Errorf("%s/%s: tokens key presence != %v", name, tc.mode, tc.wantTk)
			}
		}
	}
}

func TestForModeDoesNotModifyInput(t *testing.T) {
	rows := []Row{{Metrics: fullMetrics()}}
	RowsForMode(rows, ModeTimeOnly)
	if rows[0].Tokens == nil || rows[0].Money == nil {
		t.Fatal("RowsForMode modified its input")
	}
	jobs := []Job{{Spend: fullSpend()}}
	JobsForMode(jobs, ModeTimeOnly)
	if jobs[0].Tokens == nil || jobs[0].Money == nil {
		t.Fatal("JobsForMode modified its input")
	}
}

func TestHealthForModeMarksUsageDisabled(t *testing.T) {
	h := Health{Usage: StoreOK, Jobs: StoreOK}
	if got := h.ForMode(ModeTimeOnly).Usage; got != StoreDisabled {
		t.Errorf("time_only usage = %q, want %q", got, StoreDisabled)
	}
	if got := h.ForMode(ModeNoMoney).Usage; got != StoreOK {
		t.Errorf("no_money usage = %q, want %q", got, StoreOK)
	}
}

func TestModeFor(t *testing.T) {
	tests := []struct {
		enabled, showMoney bool
		want               Mode
	}{
		{false, false, ModeTimeOnly},
		{false, true, ModeTimeOnly},
		{true, false, ModeNoMoney},
		{true, true, ModeFull},
	}
	for _, tc := range tests {
		if got := ModeFor(tc.enabled, tc.showMoney); got != tc.want {
			t.Errorf("ModeFor(%v, %v) = %q, want %q", tc.enabled, tc.showMoney, got, tc.want)
		}
	}
}

func TestModeMetricsAndSort(t *testing.T) {
	if got := ModeFull.Metrics(); len(got) != 3 || got[0] != MetricCost {
		t.Errorf("full metrics = %v", got)
	}
	if got := ModeTimeOnly.Metrics(); len(got) != 1 || got[0] != MetricAgentTime {
		t.Errorf("time_only metrics = %v", got)
	}
	if ModeNoMoney.AllowsSort(SortCost) || !ModeNoMoney.AllowsSort(SortTokens) {
		t.Error("no_money must reject cost sort and allow tokens sort")
	}
	if ModeTimeOnly.AllowsSort(SortTokens) || !ModeTimeOnly.AllowsSort(SortTime) {
		t.Error("time_only must reject tokens sort and allow time sort")
	}
}

func TestOrClosed(t *testing.T) {
	for _, m := range []Mode{"", "bogus"} {
		if got := m.OrClosed(); got != ModeTimeOnly {
			t.Errorf("%q.OrClosed() = %q, want time_only", m, got)
		}
	}
	if got := ModeFull.OrClosed(); got != ModeFull {
		t.Errorf("full.OrClosed() = %q", got)
	}
}

func TestNewMetricsAlwaysFillsSpend(t *testing.T) {
	cost := 2.0
	got := NewMetrics(accounting.Metrics{Jobs: 2, Money: accounting.MoneyMetrics{EstimatedCostUSD: &cost, Priced: 1, Unpriced: 1}})
	if got.Tokens == nil || got.Money == nil {
		t.Fatal("mapping must fill tokens and money; ForMode hides them")
	}
	if got.Tokens.Counts != nil {
		t.Error("no counts means null counts, not zero")
	}
	if got.Money.PricedJobs != 1 || got.Money.UnpricedJobs != 1 || *got.Money.EstimatedCostUSD != 2 {
		t.Errorf("money = %+v", got.Money)
	}
}
