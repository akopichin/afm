package accounting

import "github.com/akopichin/afm/contracts/budget"

// ScopesFor lists every budget scope one record counts toward: the run, its stage and its task chain.
// run_overhead belongs to the run only; parentOf maps a task to its parent task.
func ScopesFor(rec UsageRecord, parentOf map[string]string) []budget.ScopeID {
	scopes := []budget.ScopeID{budget.RunScope()}
	if rec.Scope == ScopeRunOverhead {
		return scopes
	}
	if rec.StageID != "" {
		scopes = append(scopes, budget.StageScope(rec.StageID))
	}
	// The seen set stops a corrupt parent cycle from looping forever.
	seen := map[string]bool{}
	for id := rec.TaskID; id != "" && !seen[id]; id = parentOf[id] {
		seen[id] = true
		scopes = append(scopes, budget.TaskScope(id))
	}
	return scopes
}

// SpendByScope folds records into per-scope spend using ScopesFor, so each record is counted once per scope.
func SpendByScope(records []UsageRecord, parentOf map[string]string) map[budget.ScopeID]budget.Spend {
	out := map[budget.ScopeID]budget.Spend{}
	for _, rec := range records {
		spend := recordSpend(rec)
		for _, scope := range ScopesFor(rec, parentOf) {
			out[scope] = out[scope].Add(spend)
		}
	}
	return out
}

// recordSpend is the spend of one record; money counts only when priced from explicit rates.
func recordSpend(rec UsageRecord) budget.Spend {
	spend := budget.Spend{Records: 1}
	switch {
	case !rec.Metered:
		spend.Unmetered = 1
		return spend
	case !rec.Priced:
		spend.Unpriced = 1
	case rec.Rate == nil || !rec.Rate.Explicit:
		spend.NotExplicit = 1
	default:
		spend.MoneyNanoUSD = budget.MoneyNanoUSD(rec.EstimatedCostUSD)
	}
	spend.Tokens = rec.Tokens.Total()
	return spend
}
