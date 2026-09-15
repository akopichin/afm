package accounting

// usageRecordVersion is the current shape of a persisted usage.jsonl line.
// Bump it (and teach readers to cope) if the JSON shape ever changes
// incompatibly.
const usageRecordVersion = 1

// ScopeRunOverhead marks a UsageRecord as run-level rather than
// stage-attributed (currently only the end-of-run memory pipeline). Every
// other record uses the zero value "" (stage-attributed).
const ScopeRunOverhead = "run_overhead"

// costToleranceUSD is the absolute reconciliation tolerance between a
// vendor-reported cost and our own list-price estimate. A vendor figure
// within one cent of the estimate is treated as agreement (rounding,
// discounts, etc.); beyond that we flag it so a human can look closer.
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

// BuildRecord turns one Observation into a fully-priced UsageRecord: it
// resolves a rate, computes the estimate, snapshots the rate used, and
// reconciles against any vendor-reported cost. It never mutates obs.
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
		// Resolved (config-only, no builtin to fill the gap) but a category
		// the tokens actually use has no rate: unpriced entirely, not a
		// misleadingly partial total that silently prices the rest at $0.
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

// costDiffers reports whether a vendor-reported cost and our estimate
// disagree beyond costToleranceUSD (an absolute USD tolerance, chosen over a
// relative one so a tiny, near-zero estimate can't produce a warning from an
// equally tiny absolute difference).
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
	// Priced is true only when the group contains at least one metered
	// record AND every metered record in it resolved to a price. A group
	// with no metered records at all (e.g. only unmetered/failed
	// invocations) is NOT priced — there is nothing to trust a cost figure
	// for, so callers should render it as unknown ("—"), not as $0.00.
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

// Ledger accumulates UsageRecords for a single run and aggregates them into
// per-stage and per-run summaries. It is not safe for concurrent use — the
// caller (Store) is responsible for serializing writes.
type Ledger struct {
	recs []UsageRecord
}

// Add appends one record to the ledger.
func (l *Ledger) Add(rec UsageRecord) {
	l.recs = append(l.recs, rec)
}

// Records returns every record added so far, in insertion order. Exported
// so callers outside the package (e.g. `afm report`'s coverage/pricing
// appendix) can inspect per-record detail — model, rate provenance,
// warnings — that SummaryByStage/RunSummary intentionally aggregate away.
func (l *Ledger) Records() []UsageRecord {
	return l.recs
}

// SummaryByStage groups records by stage_id, excluding run_overhead-scoped
// records and any record with an empty stage_id (run-level records carry
// no stage to attribute to).
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

// CoverageIssues groups every unmetered/unpriced record in the ledger via
// the same aggregation the dashboard's cost tab and `afm report`'s coverage
// appendix use (see aggregateIssues), so every surface computes pricing-gap
// totals identically instead of each re-deriving them from Summary's
// Unmetered/Unpriced counters by hand. Empty when every metered record in
// the ledger was priced.
func (l *Ledger) CoverageIssues() []CoverageIssue {
	return aggregateIssues(l.Records())
}
