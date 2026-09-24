package accounting

import (
	"fmt"
	"slices"
	"strings"
)

// StageInfo carries the minimal per-stage status/duration a report needs
// from pkg/state, passed in by the caller (cmd/afm) so this package stays
// free of a pkg/state import — see the package doc: accounting imports no
// other afm package.
type StageInfo struct {
	Status string
	// Duration is pre-formatted by the caller (e.g. "12m34s"); "" when
	// unknown (a stage that never started, or whose duration the caller
	// chose not to compute).
	Duration string
}

// RenderState distinguishes WHY a report has no usage figures, so
// RenderMarkdown can render the reason directly into the markdown itself —
// `afm report run > report.md` must be self-contained: stdout survives the
// redirect, stderr doesn't.
type RenderState int

const (
	// RenderNormal is the default: render the full report from led, or (if
	// led is nil/empty — a run genuinely never recorded any usage) fall
	// back to a "No usage data" note, same as always.
	RenderNormal RenderState = iota
	// RenderCorrupt means the ledger's usage.jsonl exists but a complete
	// line in it failed to parse (accounting.ErrCorruptUsage) — led is
	// always nil for this state; nothing about the (possibly partial, now
	// untrusted) file is rendered.
	RenderCorrupt
	// RenderUnreadable means usage.jsonl could not even be read (permission
	// denied, the path is a directory, a transient I/O error) — distinct
	// from RenderCorrupt so the note never claims "corrupt" for a cause
	// that isn't a parse failure.
	RenderUnreadable
)

// RenderMarkdown renders a deterministic markdown cost/usage report for one
// run: a per-stage token/cost table, a run-overhead summary, a grand total,
// and a coverage/pricing appendix (unmetered/unpriced invocations, rate
// provenance, reported-vs-estimated mismatches). Stages, phases and models
// are all rendered in sorted order so the output is stable and
// golden-testable.
//
// stages may be nil — it only supplies the optional status/duration column,
// and a stage present in the ledger but absent from stages simply renders
// with an unknown status. For any degraded state (missing/corrupt/
// unreadable) the FSM-derived stage rows from `stages` are still rendered —
// only the usage/cost columns are omitted.
//
// led is only consulted when state == RenderNormal; for RenderCorrupt/
// RenderUnreadable the caller passes nil (accounting.Load already failed)
// and this function renders the matching self-contained note instead of any
// usage data. A nil/empty led under RenderNormal (a run with no usage.jsonl
// at all, or accounting simply disabled for it) renders the same "No usage
// data" note as it always has — that shape isn't an error, so it doesn't
// need its own RenderState.
// showMoney gates monetary ($) figures only (accounting.show_money): when
// false, every cost column/figure is dropped and reported/estimated mismatch
// amounts are suppressed, while ALL token output stays. Degraded shapes never
// render cost anyway, so the flag has no effect there.
func RenderMarkdown(runName string, stages map[string]StageInfo, led *Ledger, state RenderState, showMoney bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Cost report: %s\n\n", runName)

	switch state {
	case RenderCorrupt:
		renderStageStatusOnly(&b, stages)
		b.WriteString("> Cost unavailable — usage ledger is corrupt; token and cost totals are omitted.\n")
		return b.String()
	case RenderUnreadable:
		renderStageStatusOnly(&b, stages)
		b.WriteString("> Cost unavailable — cannot read the usage ledger; totals omitted.\n")
		return b.String()
	}

	if led == nil || len(led.Records()) == 0 {
		renderStageStatusOnly(&b, stages)
		b.WriteString("No usage data.\n")
		return b.String()
	}

	recs := led.Records()
	renderStagesTable(&b, recs, led.SummaryByStage(), stages, showMoney)
	renderOverhead(&b, recs, showMoney)
	renderTotal(&b, led.RunSummary(), showMoney)
	renderCoverage(&b, recs, showMoney)

	return b.String()
}

// renderStageStatusOnly renders a bare stage/status(duration) table with no
// usage columns at all — used by every degraded RenderMarkdown shape
// (missing/corrupt/unreadable usage ledger), since a stage's FSM status is
// always available from the caller regardless of whether accounting is.
func renderStageStatusOnly(b *strings.Builder, stages map[string]StageInfo) {
	b.WriteString("## Stages\n\n")
	if len(stages) == 0 {
		b.WriteString("No stages recorded.\n\n")
		return
	}

	ids := make([]string, 0, len(stages))
	for id := range stages {
		ids = append(ids, id)
	}
	slices.Sort(ids)

	b.WriteString("| Stage | Status (duration) |\n")
	b.WriteString("|---|---|\n")
	for _, id := range ids {
		fmt.Fprintf(b, "| %s | %s |\n", id, statusAndDuration(stages[id]))
	}
	b.WriteString("\n")
}

// renderStagesTable renders one markdown table row per stage: id, per-phase
// invocation counts, distinct models, the token breakdown, and the
// estimated cost, followed by the caller-supplied status/duration.
func renderStagesTable(b *strings.Builder, recs []UsageRecord, byStage map[string]Summary, stages map[string]StageInfo, showMoney bool) {
	b.WriteString("## Stages\n\n")
	if len(byStage) == 0 {
		b.WriteString("No per-stage usage recorded.\n\n")
		return
	}

	ids := make([]string, 0, len(byStage))
	for id := range byStage {
		ids = append(ids, id)
	}
	slices.Sort(ids)

	if showMoney {
		b.WriteString("| Stage | Phases (invocations) | Model(s) | Uncached In | Cache Read | Cache Write | Output | Total Tokens | Est. Cost | Status (duration) |\n")
		b.WriteString("|---|---|---|---|---|---|---|---|---|---|\n")
	} else {
		b.WriteString("| Stage | Phases (invocations) | Model(s) | Uncached In | Cache Read | Cache Write | Output | Total Tokens | Status (duration) |\n")
		b.WriteString("|---|---|---|---|---|---|---|---|---|\n")
	}
	for _, id := range ids {
		sum := byStage[id]
		if showMoney {
			fmt.Fprintf(b, "| %s | %s | %s | %d | %d | %d | %d | %d | %s | %s |\n",
				id, phaseCounts(recs, id), joinModels(sum.Models),
				sum.Tokens.UncachedInput, sum.Tokens.CacheRead, sum.Tokens.CacheWriteTotal(),
				sum.Tokens.Output, sum.Tokens.Total(), DisplayCost(sum), statusAndDuration(stages[id]),
			)
		} else {
			fmt.Fprintf(b, "| %s | %s | %s | %d | %d | %d | %d | %d | %s |\n",
				id, phaseCounts(recs, id), joinModels(sum.Models),
				sum.Tokens.UncachedInput, sum.Tokens.CacheRead, sum.Tokens.CacheWriteTotal(),
				sum.Tokens.Output, sum.Tokens.Total(), statusAndDuration(stages[id]),
			)
		}
	}
	b.WriteString("\n")
}

// statusAndDuration renders "status (duration)", falling back to "—" for an
// unknown status and dropping the parens entirely when duration is unknown.
func statusAndDuration(info StageInfo) string {
	status := info.Status
	if status == "" {
		status = dashUnknown
	}
	if info.Duration == "" {
		return status
	}
	return fmt.Sprintf("%s (%s)", status, info.Duration)
}

// phaseCounts renders the invocation count per phase for the records in
// recs matching stageID, e.g. "implementation×2, review×1", sorted by phase
// name for determinism. "—" when nothing matches. It does NOT filter by
// Scope itself — callers control which records are in scope: renderStagesTable
// passes the full record set with a real (non-empty) stageID, which
// naturally excludes run_overhead records (their StageID is always ""), and
// renderOverhead passes an already-scope-filtered overhead slice with
// stageID="" to match them. Re-excluding ScopeRunOverhead here as well
// previously made the Run overhead section always render "—", since its own
// records ARE the run_overhead ones (found in review).
func phaseCounts(recs []UsageRecord, stageID string) string {
	counts := map[string]int{}
	for _, r := range recs {
		if r.StageID != stageID {
			continue
		}
		counts[r.Phase]++
	}
	if len(counts) == 0 {
		return dashUnknown
	}
	phases := make([]string, 0, len(counts))
	for p := range counts {
		phases = append(phases, p)
	}
	slices.Sort(phases)
	parts := make([]string, 0, len(phases))
	for _, p := range phases {
		parts = append(parts, fmt.Sprintf("%s×%d", p, counts[p]))
	}
	return strings.Join(parts, ", ")
}

// joinModels renders a summary's distinct models sorted and comma-joined;
// "—" when none were recorded (e.g. a group of entirely unmetered records).
func joinModels(models []string) string {
	if len(models) == 0 {
		return dashUnknown
	}
	sorted := append([]string(nil), models...)
	slices.Sort(sorted)
	return strings.Join(sorted, ", ")
}

// renderOverhead summarizes every ScopeRunOverhead record (currently only
// the end-of-run memory pipeline) as its own section, separate from the
// per-stage table — it has no stage to attribute to.
func renderOverhead(b *strings.Builder, recs []UsageRecord, showMoney bool) {
	b.WriteString("## Run overhead\n\n")

	var overhead []UsageRecord
	for _, r := range recs {
		if r.Scope == ScopeRunOverhead {
			overhead = append(overhead, r)
		}
	}
	if len(overhead) == 0 {
		b.WriteString("None.\n\n")
		return
	}

	sum := summarize(overhead)
	if showMoney {
		fmt.Fprintf(b, "Invocations: %d (%s). Tokens: %d total (uncached in %d, cache read %d, cache write %d, output %d). Estimated cost: %s.\n\n",
			len(overhead), phaseCounts(overhead, ""),
			sum.Tokens.Total(), sum.Tokens.UncachedInput, sum.Tokens.CacheRead, sum.Tokens.CacheWriteTotal(), sum.Tokens.Output,
			DisplayCost(sum))
	} else {
		fmt.Fprintf(b, "Invocations: %d (%s). Tokens: %d total (uncached in %d, cache read %d, cache write %d, output %d).\n\n",
			len(overhead), phaseCounts(overhead, ""),
			sum.Tokens.Total(), sum.Tokens.UncachedInput, sum.Tokens.CacheRead, sum.Tokens.CacheWriteTotal(), sum.Tokens.Output)
	}
}

// renderTotal reports the whole-run total (stages + overhead combined) —
// exactly Ledger.RunSummary(), never recomputed here.
func renderTotal(b *strings.Builder, run Summary, showMoney bool) {
	b.WriteString("## Total\n\n")
	if showMoney {
		fmt.Fprintf(b, "Tokens: %d total. Estimated cost: %s.\n\n", run.Tokens.Total(), DisplayCost(run))
	} else {
		fmt.Fprintf(b, "Tokens: %d total.\n\n", run.Tokens.Total())
	}
}

// renderCoverage lists everything a reader should distrust or double-check
// about the totals above: unmetered invocations (with their reason),
// metered-but-unpriced invocations (unknown model/channel), the rate
// provenance behind every priced invocation, and any reported-vs-estimated
// mismatch a record was flagged with at write time. All of this is read
// straight off the already-built UsageRecords — no new arithmetic here.
func renderCoverage(b *strings.Builder, recs []UsageRecord, showMoney bool) {
	b.WriteString("## Coverage and pricing\n\n")

	var mismatched []UsageRecord
	rates := map[string]RateSnapshot{}
	for _, r := range recs {
		if r.Metered && r.Priced && r.Rate != nil {
			rates[r.Rate.Key] = *r.Rate
		}
		if recordHasWarning(r, "reported_cost_differs_from_estimate") {
			mismatched = append(mismatched, r)
		}
	}

	// Gaps (unmetered/unpriced) go through the same grouping the dashboard's
	// cost tab uses, so `afm report`'s appendix and the API total pricing
	// gaps identically — see CoverageIssue's count semantics (sum(count),
	// never len(issues)).
	var unmetered, unpriced []CoverageIssue
	for _, issue := range aggregateIssues(recs) {
		switch issue.Kind {
		case IssueKindUnmetered:
			unmetered = append(unmetered, issue)
		case IssueKindUnpriced:
			unpriced = append(unpriced, issue)
		default:
			// aggregateIssues only ever produces these two kinds.
		}
	}

	if len(rates) == 0 && len(unmetered) == 0 && len(unpriced) == 0 && len(mismatched) == 0 {
		b.WriteString("Every invocation was metered and priced; no reported/estimated mismatches.\n")
		return
	}

	if len(rates) > 0 {
		b.WriteString("Rates used:\n\n")
		keys := make([]string, 0, len(rates))
		for k := range rates {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			rs := rates[k]
			asOf := rs.AsOf
			if asOf == "" {
				asOf = dashUnknown
			}
			fmt.Fprintf(b, "- `%s` (source: %s, as of: %s)\n", k, rs.Source, asOf)
		}
		b.WriteString("\n")
	}

	if len(unmetered) > 0 {
		b.WriteString("Unmetered invocations:\n\n")
		for _, issue := range unmetered {
			reason := issue.Reason
			if reason == "" {
				reason = "no reason recorded"
			}
			fmt.Fprintf(b, "- %s/%s: %s (×%d)\n", issue.Attribution.Label(), issue.Phase, reason, issue.Count)
		}
		b.WriteString("\n")
	}

	if len(unpriced) > 0 {
		b.WriteString("Unpriced invocations (metered, no rate resolved):\n\n")
		for _, issue := range unpriced {
			model := issue.Model
			if model == "" {
				model = "unknown model"
			}
			fmt.Fprintf(b, "- %s/%s: channel=%s model=%s (×%d)\n", issue.Attribution.Label(), issue.Phase, issue.Channel, model, issue.Count)
		}
		b.WriteString("\n")
	}

	if len(mismatched) > 0 {
		b.WriteString("Reported vs. estimated cost mismatches:\n\n")
		for _, r := range sortedByStagePhase(mismatched) {
			// showMoney=false всё равно сообщает о факте расхождения (и его
			// warning-токене), но без денежных сумм.
			if showMoney {
				reported := dashUnknown
				if r.ReportedCostUSD != nil {
					reported = FormatUSD(*r.ReportedCostUSD, true)
				}
				fmt.Fprintf(b, "- %s/%s: reported %s vs. estimated %s (reported_cost_differs_from_estimate)\n",
					recordLabel(r), r.Phase, reported, FormatUSD(r.EstimatedCostUSD, true))
			} else {
				fmt.Fprintf(b, "- %s/%s: reported cost differs from estimate (reported_cost_differs_from_estimate)\n",
					recordLabel(r), r.Phase)
			}
		}
		b.WriteString("\n")
	}
}

// recordHasWarning reports whether rec carries the given warning string.
func recordHasWarning(rec UsageRecord, target string) bool {
	for _, w := range rec.Warnings {
		if w == target {
			return true
		}
	}
	return false
}

// recordLabel renders a record's attribution for the coverage appendix:
// its stage id, or "run_overhead" for a run-scoped record, or "—" if
// somehow neither is set.
func recordLabel(r UsageRecord) string {
	if r.Scope == ScopeRunOverhead {
		return "run_overhead"
	}
	if r.StageID == "" {
		return dashUnknown
	}
	return r.StageID
}

// sortedByStagePhase returns a stage/phase-sorted copy for deterministic
// appendix listings, without mutating the caller's slice.
func sortedByStagePhase(recs []UsageRecord) []UsageRecord {
	out := append([]UsageRecord(nil), recs...)
	slices.SortStableFunc(out, func(a, b UsageRecord) int {
		if a.StageID != b.StageID {
			return strings.Compare(a.StageID, b.StageID)
		}
		return strings.Compare(a.Phase, b.Phase)
	})
	return out
}
