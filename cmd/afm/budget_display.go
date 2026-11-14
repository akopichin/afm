package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/akopichin/afm-engine/engine"
	"github.com/akopichin/afm/contracts/budget"
)

const (
	incompleteMark = " (incomplete)"
	budgetsTitle   = "Budgets"
	snapshotMark   = " (snapshot of a live run)"
)

// budgetLabel names a scope for people; scope IDs are the only identity the CLI has without a live run.
func budgetLabel(v budget.View) string {
	switch v.Kind {
	case budget.ViewRun:
		return "Run"
	case budget.ViewStage:
		return "Stage " + v.Scope.ObjectID()
	case budget.ViewHelper:
		return "Helper " + string(v.Scope)
	case budget.ViewRunAgent:
		return "Run agent " + string(v.Scope)
	}
	return string(v.Scope)
}

// budgetFigures is "spent / limit" for every limited metric, the reached one first. Money shows only for a
// money limit, and "(incomplete)" says some usage could not be priced.
func budgetFigures(v budget.View) string {
	if v.Effective == nil {
		return "tokens " + groupDigits(v.Spent.Tokens) + " · no limit"
	}
	var parts []string
	if v.Effective.Tokens > 0 {
		text := fmt.Sprintf("tokens %s / %s", groupDigits(v.Spent.Tokens), groupDigits(v.Effective.Tokens))
		if !v.Spent.TokensExact() {
			text += incompleteMark
		}
		parts = append(parts, text)
	}
	if v.Effective.MoneyUSD > 0 {
		text := fmt.Sprintf("$%.2f / $%.2f", v.Spent.MoneyUSD(), v.Effective.MoneyUSD)
		if !v.Spent.MoneyExact() {
			text += incompleteMark
		}
		parts = append(parts, text)
	}
	if v.Evaluation.Money == budget.LevelReached && v.Evaluation.Tokens != budget.LevelReached && len(parts) == 2 {
		parts[0], parts[1] = parts[1], parts[0]
	}
	return strings.Join(parts, " · ")
}

func groupDigits(n uint64) string {
	digits := strconv.FormatUint(n, 10)
	for i := len(digits) - 3; i > 0; i -= 3 {
		digits = digits[:i] + "," + digits[i:]
	}
	return digits
}

var budgetHoldText = map[budget.HoldReason]string{
	budget.HoldLimitReached:          "limit reached",
	budget.HoldParentLimitReached:    "parent limit reached",
	budget.HoldRatesMissing:          "rates missing",
	budget.HoldAccountingUnavailable: "accounting unavailable",
}

// budgetState is what the budget does to the scope right now.
func budgetState(v budget.View) string {
	switch {
	case v.Hold != nil:
		text := "held: " + budgetHoldText[v.Hold.Reason]
		if v.Hold.Scope != "" && v.Hold.Scope != v.Scope {
			text += " (" + string(v.Hold.Scope) + ")"
		}
		if v.Draining {
			text += " · draining"
		}
		return text
	case v.Draining:
		return "draining: running work may finish, nothing new starts"
	}
	switch v.Evaluation.Worst() {
	case budget.LevelReached:
		return "reached"
	case budget.LevelWarning:
		return "80% used"
	case budget.LevelOK:
		return "ok"
	}
	return "no limit"
}

func budgetPolicy(v budget.View) string {
	if v.Effective == nil {
		return "-"
	}
	return string(v.Effective.Normalize().StopPolicy)
}

// printBudgets writes the Budgets section; snapshot marks the numbers of a run that is still going.
func printBudgets(w io.Writer, views []budget.View, snapshot bool) {
	if len(views) == 0 {
		return
	}
	title := budgetsTitle
	if snapshot {
		title += snapshotMark
	}
	_, _ = fmt.Fprintf(w, "\n%s\n", title)
	for _, v := range views {
		_, _ = fmt.Fprintf(w, "  %-22s %s · %s · %s\n", budgetLabel(v), budgetFigures(v), budgetPolicy(v), budgetState(v))
	}
}

// printBudgetsMarkdown is the Budgets section of `afm report`.
func printBudgetsMarkdown(w io.Writer, views []budget.View, snapshot bool) {
	if len(views) == 0 {
		return
	}
	title := budgetsTitle
	if snapshot {
		title += snapshotMark
	}
	_, _ = fmt.Fprintf(w, "\n## %s\n\n| Scope | Spent / limit | Policy | State |\n|---|---|---|---|\n", title)
	for _, v := range views {
		_, _ = fmt.Fprintf(w, "| %s | %s | %s | %s |\n", budgetLabel(v), budgetFigures(v), budgetPolicy(v), budgetState(v))
	}
}

// loadRunBudgets reads the budget views of a run without the writer lock. A read failure is reported as one
// note and never fails the command.
func loadRunBudgets(w io.Writer, runDir string) []budget.View {
	views, err := engine.BudgetViewsOffline(runDir)
	if err != nil {
		printBudgetsUnavailable(w, err)
		return nil
	}
	return views
}

// printBudgetsUnavailable keeps a broken journal from failing the command.
func printBudgetsUnavailable(w io.Writer, err error) {
	_, _ = fmt.Fprintf(w, "\nBudgets: unavailable (%s)\n", strings.ReplaceAll(err.Error(), "\n", " "))
}
