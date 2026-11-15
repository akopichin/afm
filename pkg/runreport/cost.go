package runreport

import (
	"maps"
	"slices"
	"strings"

	"github.com/akopichin/afm/contracts/accounting"
	"github.com/akopichin/afm/pkg/usageview"
)

// costUnit says what the cost block counts; it differs from analytics, which counts jobs.
const costUnit = "invocations"

// Cost states: ok and no_data come from a readable ledger, the rest mirror the ledger source.
const (
	CostOK      = "ok"
	CostNoData  = "no_data"
	CostCorrupt = "corrupt"
	CostUnread  = "unavailable"
)

// Cost is the usage ledger, counted in invocations. Total is the whole ledger; stage rows, overhead and
// unattributed usage are its parts, so the sum of stages and overhead is never presented as the run total.
type Cost struct {
	Unit         string         `json:"unit"`
	State        string         `json:"state"`
	Stages       []CostStage    `json:"stages"`
	Overhead     *CostLine      `json:"overhead,omitempty"`
	Unattributed *CostLine      `json:"unattributed,omitempty"`
	Total        *CostLine      `json:"total,omitempty"`
	Issues       []CostIssue    `json:"coverage_issues"`
	Rates        []CostRate     `json:"rates,omitempty"`
	Mismatches   []CostMismatch `json:"reported_vs_estimated"`
}

// CostStage is one stage row.
type CostStage struct {
	StageID string `json:"stage_id"`
	CostLine
}

// CostLine is the accounting.CostView of a group of records with the money and tokens the mode allows.
type CostLine struct {
	Invocations int            `json:"invocations"`
	Metered     int            `json:"metered"`
	Unmetered   int            `json:"unmetered"`
	Unpriced    int            `json:"unpriced"`
	Models      []string       `json:"models"`
	Phases      map[string]int `json:"phases"`
	Tokens      *CostTokens    `json:"tokens,omitempty"`
	Money       *CostMoney     `json:"money,omitempty"`
}

// CostTokens uses the accounting convention: total = uncached input + cache read + cache writes + output.
type CostTokens struct {
	UncachedInput   uint64   `json:"uncached_input"`
	CacheRead       uint64   `json:"cache_read"`
	CacheWrite5m    uint64   `json:"cache_write_5m"`
	CacheWrite1h    uint64   `json:"cache_write_1h"`
	CacheWriteOther uint64   `json:"cache_write_other"`
	CacheWriteTotal uint64   `json:"cache_write_total"`
	Output          uint64   `json:"output"`
	ReasoningOutput uint64   `json:"reasoning_output"`
	Total           uint64   `json:"total"`
	CacheHitRatio   *float64 `json:"cache_hit_ratio"`
}

// CostMoney is a partial sum when some invocations are unpriced; DisplayCost marks it with "+".
type CostMoney struct {
	DisplayCost       string  `json:"display_cost"`
	Coverage          string  `json:"coverage"`
	EstimatedCostUSD  float64 `json:"estimated_cost_usd"`
	PricedInvocations int     `json:"priced_invocations"`
}

// CostIssue is a group of unmetered or unpriced invocations (the dashboard's coverage issue).
type CostIssue = accounting.CoverageIssue

// CostRate is a pricing rate used by at least one priced invocation.
type CostRate struct {
	Key    string `json:"key"`
	Source string `json:"source"`
	AsOf   string `json:"as_of,omitempty"`
}

// CostMismatch is an invocation whose reported cost differs from the estimate. The amounts exist only where
// money is shown; otherwise the entry is the fact of the mismatch.
type CostMismatch struct {
	Attribution accounting.Attribution `json:"attribution"`
	Phase       string                 `json:"phase"`
	Money       *MismatchMoney         `json:"money,omitempty"`
}

// MismatchMoney holds the two amounts; reported is null when the provider gave none.
type MismatchMoney struct {
	ReportedUSD  *float64 `json:"reported_usd"`
	EstimatedUSD float64  `json:"estimated_usd"`
}

const mismatchWarning = "reported_cost_differs_from_estimate"

func buildCost(src *Sources, mode usageview.Mode, stageID string) *Cost {
	cost := &Cost{Unit: costUnit, State: CostOK, Stages: []CostStage{}, Issues: []CostIssue{}, Mismatches: []CostMismatch{}}
	switch src.Usage.Status.State {
	case SourceCorrupt:
		cost.State = CostCorrupt
		return cost
	case SourceUnavailable:
		cost.State = CostUnread
		return cost
	}
	recs := src.Usage.Records
	if len(recs) == 0 {
		cost.State = CostNoData
		return cost
	}

	byStage, overhead, unattributed := splitRecords(recs)
	inScope := recs
	if stageID != "" {
		// A stage report carries the stage's own rows only; overhead and unattributed usage are run-wide.
		inScope = byStage[stageID]
		byStage = map[string][]accounting.UsageRecord{stageID: inScope}
		overhead, unattributed = nil, nil
	}
	for _, id := range orderedStages(stageIDsOf(src.State), byStage) {
		if len(byStage[id]) > 0 {
			cost.Stages = append(cost.Stages, CostStage{StageID: id, CostLine: *costLine(byStage[id], mode)})
		}
	}
	if stageID == "" {
		cost.Total = costLine(recs, mode)
		if len(overhead) > 0 {
			cost.Overhead = costLine(overhead, mode)
		}
		if len(unattributed) > 0 {
			cost.Unattributed = costLine(unattributed, mode)
		}
	}
	var scope accounting.Ledger
	for _, r := range inScope {
		scope.Add(r)
	}
	if issues := scope.CoverageIssues(); len(issues) > 0 {
		cost.Issues = issues
	}
	if mode.HasMoney() {
		cost.Rates = ratesUsed(inScope)
	}
	cost.Mismatches = mismatches(inScope, mode)
	return cost
}

// splitRecords groups the ledger the way accounting.Ledger.SummaryByStage and the cost bundle do: overhead is
// its own group, and a record with neither a stage nor the overhead scope is unattributed.
func splitRecords(recs []accounting.UsageRecord) (byStage map[string][]accounting.UsageRecord, overhead, unattributed []accounting.UsageRecord) {
	byStage = map[string][]accounting.UsageRecord{}
	for _, r := range recs {
		switch {
		case r.Scope == accounting.ScopeRunOverhead:
			overhead = append(overhead, r)
		case r.StageID != "":
			byStage[r.StageID] = append(byStage[r.StageID], r)
		default:
			unattributed = append(unattributed, r)
		}
	}
	return byStage, overhead, unattributed
}

// orderedStages is the flow order, then stages the flow no longer has in name order, so rows are stable.
func orderedStages(order []string, byStage map[string][]accounting.UsageRecord) []string {
	ids := slices.Clone(order)
	var extra []string
	for id := range byStage {
		if !slices.Contains(order, id) {
			extra = append(extra, id)
		}
	}
	slices.Sort(extra)
	return append(ids, extra...)
}

// costLine mirrors the cost bundle: accounting.BuildCostView over the summary and the invocation count per
// phase of the group (afm contracts/accounting/provider.go, buildCostBundle).
func costLine(recs []accounting.UsageRecord, mode usageview.Mode) *CostLine {
	var ledger accounting.Ledger
	phases := map[string]int{}
	for _, r := range recs {
		ledger.Add(r)
		phases[r.Phase]++
	}
	cv := accounting.BuildCostView(ledger.RunSummary(), phases)
	line := &CostLine{
		Invocations: cv.Metered + cv.Unmetered, Metered: cv.Metered, Unmetered: cv.Unmetered, Unpriced: cv.Unpriced,
		Models: sortedModels(cv.Models), Phases: cv.Phases,
	}
	if mode.HasTokens() {
		line.Tokens = &CostTokens{
			UncachedInput: cv.UncachedInput, CacheRead: cv.CacheRead, CacheWrite5m: cv.CacheWrite5m,
			CacheWrite1h: cv.CacheWrite1h, CacheWriteOther: cv.CacheWriteOther, CacheWriteTotal: cv.CacheWriteTotal,
			Output: cv.Output, ReasoningOutput: cv.ReasoningOutput, Total: cv.TotalTokens, CacheHitRatio: cv.CacheHitRatio,
		}
	}
	if mode.HasMoney() {
		line.Money = &CostMoney{
			DisplayCost: cv.DisplayCost, Coverage: cv.Coverage, EstimatedCostUSD: cv.EstimatedCostUSD,
			PricedInvocations: cv.PricedInvocations,
		}
	}
	return line
}

func ratesUsed(recs []accounting.UsageRecord) []CostRate {
	byKey := map[string]CostRate{}
	for _, r := range recs {
		if r.Metered && r.Priced && r.Rate != nil {
			byKey[r.Rate.Key] = CostRate{Key: r.Rate.Key, Source: r.Rate.Source, AsOf: r.Rate.AsOf}
		}
	}
	rates := make([]CostRate, 0, len(byKey))
	for _, key := range slices.Sorted(maps.Keys(byKey)) {
		rates = append(rates, byKey[key])
	}
	return rates
}

func mismatches(recs []accounting.UsageRecord, mode usageview.Mode) []CostMismatch {
	out := []CostMismatch{}
	for _, r := range recs {
		if !slices.Contains(r.Warnings, mismatchWarning) {
			continue
		}
		item := CostMismatch{Attribution: attributionOf(r), Phase: r.Phase}
		if mode.HasMoney() {
			item.Money = &MismatchMoney{ReportedUSD: r.ReportedCostUSD, EstimatedUSD: r.EstimatedCostUSD}
		}
		out = append(out, item)
	}
	slices.SortStableFunc(out, func(a, b CostMismatch) int {
		if c := strings.Compare(a.Attribution.Label(), b.Attribution.Label()); c != 0 {
			return c
		}
		return strings.Compare(a.Phase, b.Phase)
	})
	return out
}

// attributionOf is the record's place in the ledger: run overhead first, then its stage (afm
// contracts/accounting/present.go, attributionOf).
func attributionOf(r accounting.UsageRecord) accounting.Attribution {
	switch {
	case r.Scope == accounting.ScopeRunOverhead:
		return accounting.Attribution{Kind: accounting.AttrRunOverhead}
	case r.StageID != "":
		return accounting.Attribution{Kind: accounting.AttrStage, StageID: r.StageID}
	}
	return accounting.Attribution{Kind: accounting.AttrUnknown}
}

func sortedModels(models []string) []string {
	out := slices.Clone(models)
	if out == nil {
		out = []string{}
	}
	slices.Sort(out)
	return out
}
