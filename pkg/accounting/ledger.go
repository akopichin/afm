package accounting

// usageRecordVersion is the current shape of a persisted usage.jsonl line. Bump it (and teach readers to
// cope) if the JSON shape ever changes incompatibly.
const usageRecordVersion = 1

// ScopeRunOverhead marks a UsageRecord as run-level rather than stage-attributed (currently only the
// end-of-run memory pipeline). Every other record uses the zero value "" (stage-attributed).
const ScopeRunOverhead = "run_overhead"

// costToleranceUSD is the absolute reconciliation tolerance between a vendor-reported cost and our own
// list-price estimate.
const costToleranceUSD = 0.01

// RateSnapshot freezes the rate actually used to price a UsageRecord, so the
// record stays reproducible even if pricing config changes later.
type RateSnapshot struct {
	Key          string `json:"key"`
	Source       string `json:"source"` // "builtin" | "config"
	AsOf         string `json:"as_of,omitempty"`
	Input        Rate   `json:"input"`
	CacheRead    Rate   `json:"cache_read"`
	CacheWrite5m Rate   `json:"cache_write_5m"`
	CacheWrite1h Rate   `json:"cache_write_1h"`
	Output       Rate   `json:"output"`
}

// UsageRecord is one line of usage.jsonl: one process invocation's usage,
// priced (or honestly marked unpriced/unmetered) at write time.
type UsageRecord struct {
	RecordVersion int    `json:"record_version"`
	StartedAt     string `json:"started_at,omitempty"`
	FinishedAt    string `json:"finished_at,omitempty"`

	StageID string `json:"stage_id"`
	Phase   string `json:"phase"`
	Scope   string `json:"scope,omitempty"`

	// Task attribution (T3.8) — all omitempty, so a record with none of them set (every pre-T3.8 record,
	// and every non-task record going forward) parses identically to before: old usage.jsonl lines still
	// parse, and the zero value ("") for every one of these fields means "not a helper-task invocation".
	TaskID       string `json:"task_id,omitempty"`
	RootStageID  string `json:"root_stage_id,omitempty"`
	ParentTaskID string `json:"parent_task_id,omitempty"`
	ExecutionID  string `json:"execution_id,omitempty"`
	ProfileID    string `json:"profile_id,omitempty"`

	Channel string `json:"channel,omitempty"`
	Schema  Schema `json:"schema,omitempty"`
	Model   string `json:"model,omitempty"`

	Metered bool `json:"metered"`
	Priced  bool `json:"priced"`

	Tokens Tokens        `json:"tokens"`
	Rate   *RateSnapshot `json:"rate,omitempty"`

	EstimatedCostUSD float64  `json:"estimated_cost_usd"`
	ReportedCostUSD  *float64 `json:"reported_cost_usd,omitempty"`

	Reason   string   `json:"reason,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// BuildRecord turns one Observation into a fully-priced UsageRecord: it resolves a rate, computes the
// estimate, snapshots the rate used, and reconciles against any vendor-reported cost.
func BuildRecord(r *Resolver, obs Observation, stageID, phase, scope string) UsageRecord {
	rec := UsageRecord{
		RecordVersion:   usageRecordVersion,
		StageID:         stageID,
		Phase:           phase,
		Scope:           scope,
		Channel:         obs.Channel,
		Schema:          obs.Schema,
		Model:           obs.Model,
		Metered:         obs.Metered,
		Tokens:          obs.Tokens,
		ReportedCostUSD: obs.ReportedCostUSD,
		Reason:          obs.Reason,
		Warnings:        append([]string(nil), obs.Warnings...),
	}

	if !obs.Metered {
		// Unmetered: no tokens, no cost, just the reason why. Leave
		// Priced=false — there is nothing to price.
		return rec
	}

	rr, ok := r.Resolve(obs.Channel, obs.Model)
	if !ok {
		// Metered but we don't know a rate for it: unpriced, not $0.
		return rec
	}
	if rr.missingCategory(obs.Tokens) {
		// Resolved but a category the tokens actually use has no rate: unpriced entirely, not a misleadingly
		// partial total that silently prices the rest at $0.
		return rec
	}

	rec.Priced = true
	rec.EstimatedCostUSD = rr.cost(obs.Tokens)
	rec.Rate = &RateSnapshot{
		Key:          rr.key,
		Source:       rr.source,
		AsOf:         rr.asOf,
		Input:        rate(rr.card.Input),
		CacheRead:    rate(rr.card.CacheRead),
		CacheWrite5m: rr.cacheWriteRate(rr.card.CacheWrite5m),
		CacheWrite1h: rr.cacheWriteRate(rr.card.CacheWrite1h),
		Output:       rate(rr.card.Output),
	}

	if obs.ReportedCostUSD != nil && costDiffers(*obs.ReportedCostUSD, rec.EstimatedCostUSD) {
		rec.Warnings = append(rec.Warnings, "reported_cost_differs_from_estimate")
	}

	return rec
}

// TaskMeta carries the cost-attribution identity for one helper task's invocation (T3.8): which task it
// belongs to, that task's root stage and parent task (empty for a top-level task), the profile that ran,
// and an execution_id distinguishing repeated invocations of the SAME task (e.g. a future retry) so each
// invocation remains individually identifiable in usage.jsonl.
type TaskMeta struct {
	TaskID       string
	RootStageID  string
	ParentTaskID string
	ProfileID    string
	ExecutionID  string
}

// BuildTaskRecord is BuildRecord plus task attribution: identical pricing logic (same Observation →
// estimate/rate-snapshot/reconciliation pipeline), with the task identity fields folded on afterward. scope
// stays a free parameter — a helper task's cost is still stage-attributed via meta.RootStageID so it shows
// up in that stage's SummaryByStage bucket, PLUS carries the finer task_id/profile_id/parent_task_id
// identity CostOwnVsSubtree (below) needs to tell tasks apart within that stage.
func BuildTaskRecord(r *Resolver, obs Observation, meta TaskMeta, phase, scope string) UsageRecord {
	rec := BuildRecord(r, obs, meta.RootStageID, phase, scope)
	rec.TaskID = meta.TaskID
	rec.RootStageID = meta.RootStageID
	rec.ParentTaskID = meta.ParentTaskID
	rec.ProfileID = meta.ProfileID
	rec.ExecutionID = meta.ExecutionID
	return rec
}

// TaskCost is one task's own cost (its OWN invocations only) and subtree cost (own + every descendant's own,
// each invocation counted exactly once) — computed from primary UsageRecords, never summed twice per level.
// Incomplete is true when ANY contributing invocation (own or any descendant's) was unmetered (interrupted
// before a terminal usage line, or an otherwise-unresolvable cost) — a $0 total must never be read as
// "verified free" when it might just be "unknown" (TestCost_InterruptedNotShownAsZero).
type TaskCost struct {
	TaskID         string
	OwnCostUSD     float64
	SubtreeCostUSD float64
	Incomplete     bool
}

// ComputeTaskCosts aggregates records' task-attributed costs into a per-task own/subtree view. parentOf maps
// a task id to its PARENT TASK id (absent/"" = top-level, parented directly by its root stage, not by
// another task) — the same shape as assistants.Task.ParentTaskID, deliberately untyped here so this package
// does not need to import pkg/assistants for one string mapping.
//
// Every record contributes to its own task's OwnCostUSD exactly once (a flat sum keyed by task_id); subtree
// cost is then computed bottom-up over the parent/child tree built from parentOf, so a record NEVER gets
// added twice (once for its own task, again for every ancestor's subtree sum via redundant re-scanning) —
// each ancestor's subtree total is the sum of its children's ALREADY-COMPUTED subtree totals, not a re-walk
// of the raw record list.
func ComputeTaskCosts(records []UsageRecord, parentOf map[string]string) map[string]*TaskCost {
	own := make(map[string]float64)
	ownIncomplete := make(map[string]bool)
	for _, rec := range records {
		if rec.TaskID == "" {
			continue
		}
		own[rec.TaskID] += rec.EstimatedCostUSD
		if !rec.Metered {
			ownIncomplete[rec.TaskID] = true
		}
	}

	children := make(map[string][]string)
	for id, parent := range parentOf {
		children[parent] = append(children[parent], id)
	}

	result := make(map[string]*TaskCost)
	var subtreeOf func(id string) (float64, bool)
	inProgress := make(map[string]bool)
	subtreeOf = func(id string) (float64, bool) {
		if r, ok := result[id]; ok {
			return r.SubtreeCostUSD, r.Incomplete
		}
		if inProgress[id] {
			// Defensive cycle guard — assistants.ValidateTree already rejects cycles before a task is ever
			// created, so this branch should be unreachable in practice.
			return 0, false
		}
		inProgress[id] = true
		total := own[id]
		incomplete := ownIncomplete[id]
		for _, c := range children[id] {
			cTotal, cIncomplete := subtreeOf(c)
			total += cTotal
			incomplete = incomplete || cIncomplete
		}
		delete(inProgress, id)
		result[id] = &TaskCost{TaskID: id, OwnCostUSD: own[id], SubtreeCostUSD: total, Incomplete: incomplete}
		return total, incomplete
	}

	ids := make(map[string]bool, len(own)+len(parentOf))
	for id := range own {
		ids[id] = true
	}
	for id := range parentOf {
		ids[id] = true
	}
	for id := range ids {
		subtreeOf(id)
	}
	return result
}

// costDiffers reports whether a vendor-reported cost and our estimate disagree beyond costToleranceUSD.
func costDiffers(reported, estimated float64) bool {
	diff := reported - estimated
	if diff < 0 {
		diff = -diff
	}
	return diff > costToleranceUSD
}

// Summary aggregates a group of UsageRecords: folded tokens, summed cost,
// coverage counters, and the distinct models involved.
type Summary struct {
	Tokens  Tokens
	CostUSD float64
	// Priced is true only when the group contains at least one metered record AND every metered record in it
	// resolved to a price.
	Priced    bool
	Models    []string
	Metered   int
	Unmetered int
	Unpriced  int
}

// summarize folds a slice of records into a Summary.
func summarize(recs []UsageRecord) Summary {
	var s Summary
	seenModel := make(map[string]bool)
	hasPriced := false
	allMeteredPriced := true

	for _, rec := range recs {
		s.Tokens = s.Tokens.Add(rec.Tokens)
		s.CostUSD += rec.EstimatedCostUSD

		if rec.Model != "" && !seenModel[rec.Model] {
			seenModel[rec.Model] = true
			s.Models = append(s.Models, rec.Model)
		}

		if !rec.Metered {
			s.Unmetered++
			continue
		}
		s.Metered++
		if rec.Priced {
			hasPriced = true
		} else {
			s.Unpriced++
			allMeteredPriced = false
		}
	}

	s.Priced = hasPriced && allMeteredPriced
	return s
}

// Ledger accumulates UsageRecords for a single run and aggregates them into per-stage and per-run
// summaries.
type Ledger struct {
	recs []UsageRecord
}

// Add appends one record to the ledger.
func (l *Ledger) Add(rec UsageRecord) {
	l.recs = append(l.recs, rec)
}

// Records returns every record added so far, in insertion order.
func (l *Ledger) Records() []UsageRecord {
	return l.recs
}

// SummaryByStage groups records by stage_id, excluding run_overhead-scoped records and any record with an
// empty stage_id (run-level records carry no stage to attribute to).
func (l *Ledger) SummaryByStage() map[string]Summary {
	byStage := make(map[string][]UsageRecord)
	for _, rec := range l.Records() {
		if rec.Scope == ScopeRunOverhead || rec.StageID == "" {
			continue
		}
		byStage[rec.StageID] = append(byStage[rec.StageID], rec)
	}

	out := make(map[string]Summary, len(byStage))
	for stageID, recs := range byStage {
		out[stageID] = summarize(recs)
	}
	return out
}

// RunSummary folds every record in the ledger, stage-attributed and
// run_overhead alike.
func (l *Ledger) RunSummary() Summary {
	return summarize(l.Records())
}

// CoverageIssues groups unmetered/unpriced records consistently across the dashboard and reports.
func (l *Ledger) CoverageIssues() []CoverageIssue {
	return aggregateIssues(l.Records())
}
