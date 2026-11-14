package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/akopichin/afm/contracts/budget"
)

func heldStageView() budget.View {
	limit := budget.Spec{Tokens: 100_000, MoneyUSD: 10, StopPolicy: budget.PolicyHard}
	return budget.View{
		Scope:      budget.StageScope("alpha"),
		Kind:       budget.ViewStage,
		Effective:  &limit,
		Spent:      budget.Spend{Tokens: 100_000, MoneyNanoUSD: 8_200_000_000, Records: 3},
		Evaluation: budget.Evaluation{Tokens: budget.LevelReached, Money: budget.LevelWarning},
		Hold:       &budget.Hold{Reason: budget.HoldLimitReached, Scope: budget.StageScope("alpha"), Metric: budget.MetricTokens},
	}
}

func TestBudgetDisplay_RowStates(t *testing.T) {
	tokensOnly := budget.Spec{Tokens: 1000, StopPolicy: budget.PolicyMedium}
	cases := []struct {
		name string
		view budget.View
		want []string
	}{
		{"held by its own limit", heldStageView(), []string{"Stage alpha", "tokens 100,000 / 100,000", "$8.20 / $10.00", "hard", "held: limit reached"}},
		{"held by a parent", budget.View{
			Scope: budget.StageScope("beta"), Kind: budget.ViewStage,
			Hold: &budget.Hold{Reason: budget.HoldParentLimitReached, Scope: budget.RunScope()},
		}, []string{"Stage beta", "no limit", "held: parent limit reached (run)"}},
		{"draining", budget.View{
			Scope: budget.StageScope("gamma"), Kind: budget.ViewStage, Effective: &tokensOnly,
			Spent: budget.Spend{Tokens: 1200}, Evaluation: budget.Evaluation{Tokens: budget.LevelReached}, Draining: true,
		}, []string{"Stage gamma", "tokens 1,200 / 1,000", "medium", "draining"}},
		{"held and draining", func() budget.View {
			v := heldStageView()
			v.Draining = true
			return v
		}(), []string{"held: limit reached · draining"}},
		{"warning", budget.View{
			Scope: budget.RunScope(), Kind: budget.ViewRun, Effective: &tokensOnly,
			Spent: budget.Spend{Tokens: 850}, Evaluation: budget.Evaluation{Tokens: budget.LevelWarning},
		}, []string{"Run", "tokens 850 / 1,000", "80% used"}},
		{"ok", budget.View{
			Scope: budget.RunScope(), Kind: budget.ViewRun, Effective: &tokensOnly,
			Spent: budget.Spend{Tokens: 10}, Evaluation: budget.Evaluation{Tokens: budget.LevelOK},
		}, []string{"Run", "tokens 10 / 1,000", "ok"}},
		{"unpriced money is incomplete", func() budget.View {
			v := heldStageView()
			v.Spent.Unpriced = 1
			return v
		}(), []string{"$8.20 / $10.00 (incomplete)"}},
		{"unmetered tokens are incomplete", func() budget.View {
			v := heldStageView()
			v.Spent.Unmetered = 2
			return v
		}(), []string{"tokens 100,000 / 100,000 (incomplete)"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			printBudgets(&out, []budget.View{tc.view}, false)
			for _, want := range tc.want {
				if !strings.Contains(out.String(), want) {
					t.Errorf("output lacks %q:\n%s", want, out.String())
				}
			}
		})
	}
}

func TestBudgetDisplay_MoneyColumnOnlyForMoneyLimit(t *testing.T) {
	tokensOnly := budget.Spec{Tokens: 1000, StopPolicy: budget.PolicyHard}
	view := budget.View{Scope: budget.RunScope(), Kind: budget.ViewRun, Effective: &tokensOnly, Spent: budget.Spend{Tokens: 1, MoneyNanoUSD: 5_000_000_000}}
	var out bytes.Buffer
	printBudgets(&out, []budget.View{view}, false)
	if strings.Contains(out.String(), "$") {
		t.Errorf("money shown without a money limit:\n%s", out.String())
	}
}

func TestBudgetDisplay_ReachedMetricComesFirst(t *testing.T) {
	limit := budget.Spec{Tokens: 1000, MoneyUSD: 1, StopPolicy: budget.PolicyHard}
	view := budget.View{
		Scope: budget.RunScope(), Kind: budget.ViewRun, Effective: &limit,
		Spent:      budget.Spend{Tokens: 10, MoneyNanoUSD: 1_000_000_000},
		Evaluation: budget.Evaluation{Tokens: budget.LevelOK, Money: budget.LevelReached},
	}
	if got := budgetFigures(view); !strings.HasPrefix(got, "$1.00 / $1.00") {
		t.Errorf("figures = %q, want the reached money first", got)
	}
}

func TestBudgetDisplay_NoViewsOmitsTheSection(t *testing.T) {
	var out bytes.Buffer
	printBudgets(&out, nil, true)
	printBudgetsMarkdown(&out, nil, true)
	if out.Len() != 0 {
		t.Errorf("empty budgets printed %q", out.String())
	}
}

func TestBudgetDisplay_SnapshotMarker(t *testing.T) {
	var live, stopped bytes.Buffer
	printBudgets(&live, []budget.View{heldStageView()}, true)
	printBudgets(&stopped, []budget.View{heldStageView()}, false)
	if !strings.Contains(live.String(), "snapshot of a live run") || strings.Contains(stopped.String(), "snapshot") {
		t.Errorf("live:\n%s\nstopped:\n%s", live.String(), stopped.String())
	}
}

func TestBudgetDisplay_MarkdownTable(t *testing.T) {
	var out bytes.Buffer
	printBudgetsMarkdown(&out, []budget.View{heldStageView()}, false)
	for _, want := range []string{"## Budgets", "| Scope | Spent / limit | Policy | State |", "| Stage alpha | tokens 100,000 / 100,000 · $8.20 / $10.00 | hard | held: limit reached |"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("markdown lacks %q:\n%s", want, out.String())
		}
	}
}

func TestGroupDigits(t *testing.T) {
	for in, want := range map[uint64]string{0: "0", 999: "999", 1000: "1,000", 1234567: "1,234,567", 100000: "100,000"} {
		if got := groupDigits(in); got != want {
			t.Errorf("groupDigits(%d) = %q, want %q", in, got, want)
		}
	}
}
