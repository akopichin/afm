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

// RenderMarkdown renders a deterministic markdown cost/usage report for one
// run: a per-stage token/cost table, a run-overhead summary, a grand total,
// and a coverage/pricing appendix (unmetered/unpriced invocations, rate
// provenance, reported-vs-estimated mismatches). Stages, phases and models
// are all rendered in sorted order so the output is stable and
// golden-testable.
//
// stages may be nil — it only supplies the optional status/duration column,
// and a stage present in the ledger but absent from stages simply renders
// with an unknown status.
//
// led may be nil, or non-nil with zero records — either shape (a run with
// no usage.jsonl at all, or accounting simply disabled for it) renders a
// plain "No usage data" report instead of a misleading $0.00.
func RenderMarkdown(runName string, stages map[string]StageInfo, led *Ledger) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Cost report: %s\n\n", runName)

	if led == nil || len(led.Records()) == 0 {
		b.WriteString("No usage data.\n")
		return b.String()
	}

	recs := led.Records()
	renderStagesTable(&b, recs, led.SummaryByStage(), stages)
	renderOverhead(&b, recs)
	renderTotal(&b, led.RunSummary())
	renderCoverage(&b, recs)

	return b.String()
}

// renderStagesTable renders one markdown table row per stage: id, per-phase
// invocation counts, distinct models, the token breakdown, and the
// estimated cost, followed by the caller-supplied status/duration.
func renderStagesTable(b *strings.Builder, recs []UsageRecord, byStage map[string]Summary, stages map[string]StageInfo) {
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

	b.WriteString("| Stage | Phases (invocations) | Model(s) | Uncached In | Cache Read | Cache Write | Output | Total Tokens | Est. Cost | Status (duration) |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|\n")
	for _, id := range ids {
		sum := byStage[id]
		fmt.Fprintf(b, "| %s | %s | %s | %d | %d | %d | %d | %d | %s | %s |\n",
			id,
			phaseCounts(recs, id),
			joinModels(sum.Models),
			sum.Tokens.UncachedInput,
			sum.Tokens.CacheRead,
			sum.Tokens.CacheWriteTotal(),
			sum.Tokens.Output,
			sum.Tokens.Total(),
			FormatUSD(sum.CostUSD, sum.Priced),
			statusAndDuration(stages[id]),
		)
	}
	b.WriteString("\n")
}

// statusAndDuration renders "status (duration)", falling back to "—" for an
// unknown status and dropping the parens entirely when duration is unknown.
func statusAndDuration(info StageInfo) string {
	status := info.Status
	if status == "" {
		status = "—"
	}
	if info.Duration == "" {
		return status
	}
	return fmt.Sprintf("%s (%s)", status, info.Duration)
}

// phaseCounts renders a stage's invocation count per phase, e.g.
// "implementation×2, review×1", sorted by phase name for determinism. "—"
// when the stage has no stage-attributed records (shouldn't happen for an
// id present in byStage, but kept honest rather than assumed).
func phaseCounts(recs []UsageRecord, stageID string) string {
	counts := map[string]int{}
	for _, r := range recs {
		if r.StageID != stageID || r.Scope == ScopeRunOverhead {
			continue
		}
		counts[r.Phase]++
	}
	if len(counts) == 0 {
		return "—"
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
		return "—"
	}
	sorted := append([]string(nil), models...)
	slices.Sort(sorted)
	return strings.Join(sorted, ", ")
}

// renderOverhead summarizes every ScopeRunOverhead record (currently only
// the end-of-run memory pipeline) as its own section, separate from the
// per-stage table — it has no stage to attribute to.
func renderOverhead(b *strings.Builder, recs []UsageRecord) {
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
	fmt.Fprintf(b, "Invocations: %d (%s). Tokens: %d total (uncached in %d, cache read %d, cache write %d, output %d). Estimated cost: %s.\n\n",
		len(overhead), phaseCounts(overhead, ""),
		sum.Tokens.Total(), sum.Tokens.UncachedInput, sum.Tokens.CacheRead, sum.Tokens.CacheWriteTotal(), sum.Tokens.Output,
		FormatUSD(sum.CostUSD, sum.Priced))
}

// renderTotal reports the whole-run total (stages + overhead combined) —
// exactly Ledger.RunSummary(), never recomputed here.
func renderTotal(b *strings.Builder, run Summary) {
	b.WriteString("## Total\n\n")
	fmt.Fprintf(b, "Tokens: %d total. Estimated cost: %s.\n\n", run.Tokens.Total(), FormatUSD(run.CostUSD, run.Priced))
}

// renderCoverage lists everything a reader should distrust or double-check
// about the totals above: unmetered invocations (with their reason),
// metered-but-unpriced invocations (unknown model/channel), the rate
// provenance behind every priced invocation, and any reported-vs-estimated
// mismatch a record was flagged with at write time. All of this is read
// straight off the already-built UsageRecords — no new arithmetic here.
func renderCoverage(b *strings.Builder, recs []UsageRecord) {
	b.WriteString("## Coverage and pricing\n\n")

	var unmetered, unpriced, mismatched []UsageRecord
	rates := map[string]RateSnapshot{}
	for _, r := range recs {
		switch {
		case !r.Metered:
			unmetered = append(unmetered, r)
		case !r.Priced:
			unpriced = append(unpriced, r)
		default:
			if r.Rate != nil {
				rates[r.Rate.Key] = *r.Rate
			}
		}
		if recordHasWarning(r, "reported_cost_differs_from_estimate") {
			mismatched = append(mismatched, r)
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
				asOf = "—"
			}
			fmt.Fprintf(b, "- `%s` (source: %s, as of: %s)\n", k, rs.Source, asOf)
		}
		b.WriteString("\n")
	}

	if len(unmetered) > 0 {
		b.WriteString("Unmetered invocations:\n\n")
		for _, r := range sortedByStagePhase(unmetered) {
			reason := r.Reason
			if reason == "" {
				reason = "no reason recorded"
			}
			fmt.Fprintf(b, "- %s/%s: %s\n", recordLabel(r), r.Phase, reason)
		}
		b.WriteString("\n")
	}

	if len(unpriced) > 0 {
		b.WriteString("Unpriced invocations (metered, no rate resolved):\n\n")
		for _, r := range sortedByStagePhase(unpriced) {
			model := r.Model
			if model == "" {
				model = "unknown model"
			}
			fmt.Fprintf(b, "- %s/%s: channel=%s model=%s\n", recordLabel(r), r.Phase, r.Channel, model)
		}
		b.WriteString("\n")
	}

	if len(mismatched) > 0 {
		b.WriteString("Reported vs. estimated cost mismatches:\n\n")
		for _, r := range sortedByStagePhase(mismatched) {
			reported := "—"
			if r.ReportedCostUSD != nil {
				reported = FormatUSD(*r.ReportedCostUSD, true)
			}
			fmt.Fprintf(b, "- %s/%s: reported %s vs. estimated %s (reported_cost_differs_from_estimate)\n",
				recordLabel(r), r.Phase, reported, FormatUSD(r.EstimatedCostUSD, true))
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
		return "—"
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
