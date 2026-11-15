package runreport

import "github.com/akopichin/afm/contracts/budget"

// Budgets are the spending limits of the whole run, read separately from the usage: a stage report still
// lists every scope. They keep their own display policy in every mode: tokens show with a token limit (or no
// limit at all) and money only with a money limit.
type Budgets struct {
	Scope             string       `json:"scope"`
	Items             []BudgetItem `json:"items,omitempty"`
	UnavailableReason string       `json:"unavailable_reason,omitempty"`
}

// BudgetItem is one scope with its spend and limit.
type BudgetItem struct {
	Scope    string        `json:"scope"`
	Kind     string        `json:"kind"`
	Tokens   *BudgetTokens `json:"tokens,omitempty"`
	Money    *BudgetMoney  `json:"money,omitempty"`
	Policy   string        `json:"policy,omitempty"`
	Level    string        `json:"level"`
	Hold     *BudgetHold   `json:"hold,omitempty"`
	Draining bool          `json:"draining,omitempty"`
	// View is the engine's view the Markdown section renders; it is not part of the JSON document.
	View budget.View `json:"-"`
}

// BudgetTokens is the token spend against an optional limit; Exact is false when some usage was not metered.
type BudgetTokens struct {
	Spent uint64  `json:"spent"`
	Limit *uint64 `json:"limit"`
	Exact bool    `json:"exact"`
}

// BudgetMoney exists only for a scope with a money limit.
type BudgetMoney struct {
	SpentUSD float64 `json:"spent_usd"`
	LimitUSD float64 `json:"limit_usd"`
	Exact    bool    `json:"exact"`
}

// BudgetHold says why a scope is not running.
type BudgetHold struct {
	Reason string `json:"reason"`
	Scope  string `json:"scope,omitempty"`
}

const (
	budgetsRunWide    = "run_wide"
	levelNoLimit      = "no_limit"
	budgetsUnreadable = "budgets_unreadable"
)

func buildBudgets(src *Sources) Budgets {
	if src.BudgetsErr != nil {
		return Budgets{Scope: budgetsRunWide, UnavailableReason: budgetsUnreadable}
	}
	out := Budgets{Scope: budgetsRunWide, Items: make([]BudgetItem, 0, len(src.Budgets))}
	for _, v := range src.Budgets {
		out.Items = append(out.Items, budgetItem(v))
	}
	return out
}

func budgetItem(v budget.View) BudgetItem {
	item := BudgetItem{Scope: string(v.Scope), Kind: string(v.Kind), Level: levelNoLimit, Draining: v.Draining, View: v}
	if v.Hold != nil {
		item.Hold = &BudgetHold{Reason: string(v.Hold.Reason), Scope: string(v.Hold.Scope)}
	}
	if v.Effective == nil {
		item.Tokens = &BudgetTokens{Spent: v.Spent.Tokens, Exact: v.Spent.TokensExact()}
		return item
	}
	item.Policy = string(v.Effective.Normalize().StopPolicy)
	if worst := v.Evaluation.Worst(); worst != budget.LevelNone {
		item.Level = string(worst)
	}
	if limit := v.Effective.Tokens; limit > 0 {
		item.Tokens = &BudgetTokens{Spent: v.Spent.Tokens, Limit: &limit, Exact: v.Spent.TokensExact()}
	}
	if limit := v.Effective.MoneyUSD; limit > 0 {
		item.Money = &BudgetMoney{SpentUSD: v.Spent.MoneyUSD(), LimitUSD: limit, Exact: v.Spent.MoneyExact()}
	}
	return item
}
