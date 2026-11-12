package accounting

import (
	"reflect"
	"slices"
	"testing"

	"github.com/akopichin/afm/contracts/budget"
)

func sortedScopes(ids []budget.ScopeID) []budget.ScopeID {
	slices.Sort(ids)
	return ids
}

func TestScopesFor(t *testing.T) {
	parents := map[string]string{"t3": "t2", "t2": "t1", "loop-a": "loop-b", "loop-b": "loop-a"}
	tests := []struct {
		name string
		rec  UsageRecord
		want []budget.ScopeID
	}{
		{"plain stage record", UsageRecord{StageID: "plan"},
			[]budget.ScopeID{"run", "stage:plan"}},
		{"helper chain includes the intermediate task without own records", UsageRecord{StageID: "plan", TaskID: "t3"},
			[]budget.ScopeID{"run", "stage:plan", "task:t1", "task:t2", "task:t3"}},
		{"run agent has no stage but keeps its chain", UsageRecord{TaskID: "t2"},
			[]budget.ScopeID{"run", "task:t1", "task:t2"}},
		{"run overhead is run only", UsageRecord{StageID: "plan", Scope: ScopeRunOverhead},
			[]budget.ScopeID{"run"}},
		{"run overhead of a task is still run only", UsageRecord{Scope: ScopeRunOverhead, TaskID: "t3"},
			[]budget.ScopeID{"run"}},
		{"parent cycle stops at the repeated node", UsageRecord{StageID: "s", TaskID: "loop-a"},
			[]budget.ScopeID{"run", "stage:s", "task:loop-a", "task:loop-b"}},
		{"no ids at all", UsageRecord{}, []budget.ScopeID{"run"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := sortedScopes(ScopesFor(tc.rec, parents))
			if !reflect.DeepEqual(got, sortedScopes(tc.want)) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestScopesFor_NilParents(t *testing.T) {
	got := ScopesFor(UsageRecord{StageID: "s", TaskID: "t"}, nil)
	want := []budget.ScopeID{"run", "stage:s", "task:t"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func pricedRecord(stage, task, exec string, usd float64, tok Tokens) UsageRecord {
	return UsageRecord{
		StageID: stage, TaskID: task, ExecutionID: exec,
		Metered: true, Priced: true, Tokens: tok, EstimatedCostUSD: usd,
		Rate: &RateSnapshot{Explicit: true},
	}
}

func TestSpendByScope_NestingCountsEachRecordOnce(t *testing.T) {
	parents := map[string]string{"child": "parent"}
	recs := []UsageRecord{
		pricedRecord("s1", "", "", 0.5, Tokens{Output: 10}),
		pricedRecord("s1", "parent", "e1", 0.25, Tokens{Output: 20}),
		pricedRecord("s1", "child", "e2", 0.125, Tokens{Output: 40}),
		pricedRecord("", "agent", "e3", 1, Tokens{Output: 80}),
		{StageID: "s1", Scope: ScopeRunOverhead, Metered: true, Priced: true, Tokens: Tokens{Output: 160},
			EstimatedCostUSD: 2, Rate: &RateSnapshot{Explicit: true}},
	}
	got := SpendByScope(recs, parents)

	want := map[budget.ScopeID]budget.Spend{
		"run":         {Tokens: 310, MoneyNanoUSD: 3_875_000_000, Records: 5},
		"stage:s1":    {Tokens: 70, MoneyNanoUSD: 875_000_000, Records: 3},
		"task:parent": {Tokens: 60, MoneyNanoUSD: 375_000_000, Records: 2},
		"task:child":  {Tokens: 40, MoneyNanoUSD: 125_000_000, Records: 1},
		"task:agent":  {Tokens: 80, MoneyNanoUSD: 1_000_000_000, Records: 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestSpendByScope_RetryAndLoopShareScope(t *testing.T) {
	recs := []UsageRecord{
		pricedRecord("s", "t", "exec-1", 0.1, Tokens{Output: 1}),
		pricedRecord("s", "t", "exec-2", 0.1, Tokens{Output: 1}),
		pricedRecord("s", "t", "exec-3", 0.1, Tokens{Output: 1}),
	}
	got := SpendByScope(recs, nil)["task:t"]
	if got.Records != 3 || got.Tokens != 3 {
		t.Errorf("got %+v, want 3 records and 3 tokens in one scope", got)
	}
}

func TestSpendByScope_SameProfileInstancesAreSeparate(t *testing.T) {
	recs := []UsageRecord{
		{StageID: "s", TaskID: "task-a", ProfileID: "reviewer", Metered: true, Tokens: Tokens{Output: 5}},
		{StageID: "s", TaskID: "task-b", ProfileID: "reviewer", Metered: true, Tokens: Tokens{Output: 7}},
	}
	got := SpendByScope(recs, nil)
	if got["task:task-a"].Tokens != 5 || got["task:task-b"].Tokens != 7 {
		t.Errorf("got %+v", got)
	}
}

func TestSpendByScope_TokensDoNotDoubleCountCacheOrReasoning(t *testing.T) {
	tok := Tokens{UncachedInput: 1, CacheRead: 2, CacheWrite5m: 4, CacheWrite1h: 8, CacheWriteOther: 16, Output: 32, ReasoningOutput: 20}
	got := SpendByScope([]UsageRecord{{StageID: "s", Metered: true, Tokens: tok}}, nil)["run"]
	if got.Tokens != 63 || got.Tokens != tok.Total() {
		t.Errorf("tokens = %d, want 63 (= Tokens.Total, reasoning is part of output)", got.Tokens)
	}
}

func TestSpendByScope_TenDimesReachOneDollar(t *testing.T) {
	var recs []UsageRecord
	for range 10 {
		recs = append(recs, pricedRecord("s", "", "", 0.10, Tokens{Output: 1}))
	}
	got := SpendByScope(recs, nil)["run"]
	if got.MoneyNanoUSD != 1_000_000_000 {
		t.Fatalf("money = %d nano-USD, want exactly 1e9", got.MoneyNanoUSD)
	}
	eval := budget.Evaluate(budget.Spec{MoneyUSD: 1}, got)
	if eval.Money != budget.LevelReached {
		t.Errorf("money level = %q, want reached", eval.Money)
	}
}

func TestSpendByScope_UnknownMoneyIsCountedNotSummed(t *testing.T) {
	recs := []UsageRecord{
		{StageID: "s", Metered: false, Reason: "no usage"},
		{StageID: "s", Metered: true, Tokens: Tokens{Output: 3}},
		{StageID: "s", Metered: true, Priced: true, Tokens: Tokens{Output: 4}, EstimatedCostUSD: 9,
			Rate: &RateSnapshot{Source: "builtin"}},
		{StageID: "s", Metered: true, Priced: true, Tokens: Tokens{Output: 5}, EstimatedCostUSD: 9},
		pricedRecord("s", "", "", 0.5, Tokens{Output: 6}),
	}
	got := SpendByScope(recs, nil)["run"]
	want := budget.Spend{Tokens: 18, MoneyNanoUSD: 500_000_000, Records: 5, Unmetered: 1, Unpriced: 1, NotExplicit: 2}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if got.MoneyExact() || got.TokensExact() {
		t.Errorf("exactness must be false: money=%v tokens=%v", got.MoneyExact(), got.TokensExact())
	}
}

func TestSpendByScope_RoundsEachRecordToNanoUSD(t *testing.T) {
	// 0.6 nano-USD per record: rounding each gives 1, truncating gives 0, rounding the sum (1.2) gives 1.
	recs := []UsageRecord{
		pricedRecord("s", "", "", 0.0000000006, Tokens{Output: 1}),
		pricedRecord("s", "", "", 0.0000000006, Tokens{Output: 1}),
	}
	if got := SpendByScope(recs, nil)["run"].MoneyNanoUSD; got != 2 {
		t.Errorf("money = %d nano-USD, want 2 (each record rounded)", got)
	}
}
