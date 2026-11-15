package main

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/akopichin/afm/contracts/accounting"
	"github.com/akopichin/afm/contracts/budget"
	"github.com/akopichin/afm/contracts/state"
	"github.com/akopichin/afm/pkg/runreport"
	"github.com/akopichin/afm/pkg/usageview"
)

const dash = "—"

// agentRoles are the labels the dashboard gives the agent kinds.
var agentRoles = map[string]string{
	usageview.AgentStage: "Stage agent", usageview.AgentHelper: "Helper", usageview.AgentRunAgent: "Run agent",
	usageview.AgentVerify: "Verify", usageview.AgentAssistant: "Assistant", usageview.AgentRunOverhead: "Run overhead",
}

// reportMarkdown writes the human form of the report. It only formats the model: every figure comes from the
// named block it belongs to, and a column the mode hides is not printed at all.
type reportMarkdown struct {
	w io.Writer
	r *runreport.Report
}

func renderReportMarkdown(w io.Writer, r *runreport.Report) {
	m := reportMarkdown{w: w, r: r}
	m.printf("# Cost report: %s\n\n", r.Run.ID)
	m.header()
	m.summary()
	m.stages()
	if r.Cost != nil && r.Cost.State == runreport.CostOK {
		m.overhead()
		m.unattributed()
		m.total()
		m.coverage()
	}
	m.models()
	m.agents()
	m.jobs()
	m.budgets()
	m.diagnostics()
}

func (m reportMarkdown) printf(format string, args ...any) { _, _ = fmt.Fprintf(m.w, format, args...) }

func (m reportMarkdown) header() {
	r := m.r
	scope := "whole run"
	if r.Scope.Kind == runreport.ScopeStage {
		scope = "stage " + r.Scope.StageID
	}
	m.printf("Flow: %s · Status: %s · Scope: %s · As of: %s\n\n", cell(orDash(r.Run.Flow)), r.Run.Status, scope, r.AsOf.Format(time.RFC3339))
}

func (m reportMarkdown) summary() {
	r := m.r
	m.printf("## Summary\n\n")
	elapsed := formatMillis(r.Run.ElapsedMS)
	if open := r.Run.OpenInterval; open != nil {
		elapsed += " (still running since " + open.Since.Format(time.RFC3339) + ", the open part is not counted)"
	}
	m.printf("- Run elapsed: %s\n", elapsed)
	t := r.StageTime
	m.printf("- Stage work: %s · waiting: %s · paused: %s\n", formatMillis(t.WorkingMS), formatMillis(t.WaitingMS), formatMillis(t.PausedMS))
	if len(t.IncompleteID) > 0 {
		m.printf("- Stage time unknown, not counted: %s\n", strings.Join(t.IncompleteID, ", "))
	}
	a := r.Analytics
	m.printf("- Agent jobs: %d · agent time: %s\n", a.Totals.Jobs, formatDurationPtr(a.Totals.AgentTime.DurationMS))
	if a.OutsideRunTotal != nil {
		m.printf("- Assistant (outside the run total): %d jobs · %s\n", a.OutsideRunTotal.Jobs, formatDurationPtr(a.OutsideRunTotal.AgentTime.DurationMS))
	}
	if tokens := a.Totals.Tokens; tokens != nil {
		m.printf("- Agent tokens: %s (metered jobs %d, unmetered %d)\n", tokenCell(tokens), tokens.MeteredJobs, tokens.UnmeteredJobs)
	}
	if money := a.Totals.Money; money != nil {
		m.printf("- Agent estimated cost: %s (priced jobs %d, unpriced %d)\n", moneyCell(money), money.PricedJobs, money.UnpricedJobs)
	}
	m.printf("- Completeness: time %s", a.Completeness.Time.Status)
	if c := a.Completeness.Tokens; c != nil {
		m.printf(" · tokens %s", c.Status)
	}
	if c := a.Completeness.Price; c != nil {
		m.printf(" · price %s", c.Status)
	}
	m.printf("\n\n")
}

// stages lists every stage with its status and time; the usage columns appear when the ledger has data.
func (m reportMarkdown) stages() {
	r := m.r
	m.printf("## Stages\n\n")
	if len(r.Stages) == 0 {
		m.printf("No stages recorded.\n\n")
		return
	}
	cost := r.Cost
	if cost != nil && cost.State != runreport.CostOK {
		m.costNote(cost.State)
	}
	withUsage := cost != nil && cost.State == runreport.CostOK && len(cost.Stages) > 0
	if cost != nil && cost.State == runreport.CostNoData {
		m.printf("No usage data.\n\n")
	}
	if !withUsage {
		m.printf("| Stage | Status (duration) |\n|---|---|\n")
		for _, s := range r.Stages {
			m.printf("| %s | %s |\n", cell(s.ID), statusAndTime(s))
		}
		m.printf("\n")
		return
	}

	usage := map[string]runreport.CostStage{}
	for _, s := range cost.Stages {
		usage[s.StageID] = s
	}
	money := r.Mode.HasMoney()
	header := "| Stage | Phases (invocations) | Model(s) | Uncached In | Cache Read | Cache Write | Output | Total Tokens |"
	if money {
		header += " Est. Cost |"
	}
	m.printf("%s Status (duration) |\n|%s\n", header, strings.Repeat("---|", strings.Count(header, "|")))
	for _, s := range r.Stages {
		row, ok := usage[s.ID]
		if !ok {
			m.printf("| %s |%s %s |\n", cell(s.ID), strings.Repeat(" "+dash+" |", strings.Count(header, "|")-2), statusAndTime(s))
			continue
		}
		t := row.Tokens
		m.printf("| %s | %s | %s | %d | %d | %d | %d | %d |", cell(s.ID), phaseList(row.Phases), joinOrDash(row.Models),
			t.UncachedInput, t.CacheRead, t.CacheWriteTotal, t.Output, t.Total)
		if money {
			m.printf(" %s |", row.Money.DisplayCost)
		}
		m.printf(" %s |\n", statusAndTime(s))
	}
	m.printf("\n")
}

func (m reportMarkdown) costNote(costState string) {
	if costState == runreport.CostCorrupt {
		m.printf("> Cost unavailable — usage ledger is corrupt; token and cost totals are omitted.\n\n")
		return
	}
	m.printf("> Cost unavailable — cannot read the usage ledger; totals omitted.\n\n")
}

func (m reportMarkdown) overhead() {
	m.printf("## Run overhead\n\n")
	if m.r.Scope.Kind == runreport.ScopeStage {
		m.printf("Not part of a stage report.\n\n")
		return
	}
	line := m.r.Cost.Overhead
	if line == nil {
		m.printf("None.\n\n")
		return
	}
	m.printf("%s\n\n", m.lineSummary(line))
}

func (m reportMarkdown) unattributed() {
	line := m.r.Cost.Unattributed
	if line == nil {
		return
	}
	m.printf("## Unattributed usage\n\nRecords with no stage that are not run overhead.\n\n%s\n\n", m.lineSummary(line))
}

func (m reportMarkdown) total() {
	line := m.r.Cost.Total
	if line == nil {
		return
	}
	m.printf("## Total\n\nThe whole usage ledger, counted in invocations.\n\n%s\n\n", m.lineSummary(line))
}

// lineSummary is one sentence about a group of invocations.
func (m reportMarkdown) lineSummary(l *runreport.CostLine) string {
	t := l.Tokens
	text := fmt.Sprintf("Invocations: %d (%s). Tokens: %d total (uncached in %d, cache read %d, cache write %d, output %d).",
		l.Invocations, phaseList(l.Phases), t.Total, t.UncachedInput, t.CacheRead, t.CacheWriteTotal, t.Output)
	if l.Money != nil {
		text += " Estimated cost: " + l.Money.DisplayCost + "."
	}
	return text
}

func (m reportMarkdown) coverage() {
	c := m.r.Cost
	m.printf("## Coverage and pricing\n\n")
	var unmetered, unpriced []runreport.CostIssue
	for _, issue := range c.Issues {
		if issue.Kind == accounting.IssueKindUnmetered {
			unmetered = append(unmetered, issue)
		} else {
			unpriced = append(unpriced, issue)
		}
	}
	if len(c.Rates) == 0 && len(unmetered) == 0 && len(unpriced) == 0 && len(c.Mismatches) == 0 {
		m.printf("Every invocation was metered and priced; no reported/estimated mismatches.\n\n")
		return
	}
	if len(c.Rates) > 0 {
		m.printf("Rates used:\n\n")
		for _, rate := range c.Rates {
			m.printf("- `%s` (source: %s, as of: %s)\n", rate.Key, rate.Source, orDash(rate.AsOf))
		}
		m.printf("\n")
	}
	if len(unmetered) > 0 {
		m.printf("Unmetered invocations:\n\n")
		for _, issue := range unmetered {
			reason := issue.Reason
			if reason == "" {
				reason = "no reason recorded"
			}
			m.printf("- %s/%s: %s (×%d)\n", issue.Attribution.Label(), issue.Phase, reason, issue.Count)
		}
		m.printf("\n")
	}
	if len(unpriced) > 0 {
		m.printf("Unpriced invocations (metered, no rate resolved):\n\n")
		for _, issue := range unpriced {
			m.printf("- %s/%s: channel=%s model=%s (×%d)\n", issue.Attribution.Label(), issue.Phase, issue.Channel, orDefault(issue.Model, "unknown model"), issue.Count)
		}
		m.printf("\n")
	}
	if len(c.Mismatches) > 0 {
		m.printf("Reported vs. estimated cost mismatches:\n\n")
		for _, mm := range c.Mismatches {
			if mm.Money == nil {
				m.printf("- %s/%s: reported cost differs from estimate (reported_cost_differs_from_estimate)\n", mm.Attribution.Label(), mm.Phase)
				continue
			}
			reported := dash
			if mm.Money.ReportedUSD != nil {
				reported = accounting.FormatUSD(*mm.Money.ReportedUSD, true)
			}
			m.printf("- %s/%s: reported %s vs. estimated %s (reported_cost_differs_from_estimate)\n",
				mm.Attribution.Label(), mm.Phase, reported, accounting.FormatUSD(mm.Money.EstimatedUSD, true))
		}
		m.printf("\n")
	}
}

func (m reportMarkdown) models() {
	m.printf("## Models\n\n")
	m.rows(m.r.Analytics.Models, "Model", false)
}

func (m reportMarkdown) agents() {
	m.printf("## Agents\n\n")
	m.rows(m.r.Analytics.Agents, "Agent", true)
}

// rows renders a breakdown; agent rows add the role and mark the Assistant as outside the run total.
func (m reportMarkdown) rows(rows []usageview.Row, name string, agents bool) {
	if len(rows) == 0 {
		m.printf("None.\n\n")
		return
	}
	cols := []string{name}
	if agents {
		cols = append(cols, "Role")
	}
	cols = append(cols, "Jobs", "Agent time")
	hasTokens, hasMoney := m.r.Mode.HasTokens(), m.r.Mode.HasMoney()
	if hasTokens {
		cols = append(cols, "Tokens")
	}
	if hasMoney {
		cols = append(cols, "Est. cost")
	}
	m.tableHeader(cols)
	for _, row := range rows {
		label := cell(row.Label)
		if row.Agent != nil && row.Agent.OutsideRunTotal {
			label += " *Outside run total*"
		}
		cells := []string{label}
		if agents {
			cells = append(cells, roleOf(row.Agent))
		}
		cells = append(cells, fmt.Sprint(row.Jobs), formatDurationPtr(row.AgentTime.DurationMS))
		if hasTokens {
			cells = append(cells, tokenCell(row.Tokens))
		}
		if hasMoney {
			cells = append(cells, moneyCell(row.Money))
		}
		m.tableRow(cells)
	}
	m.printf("\n")
}

func (m reportMarkdown) jobs() {
	m.printf("## Agent jobs\n\n")
	jobs := m.r.Analytics.Jobs
	if len(jobs) == 0 {
		m.printf("None.\n\n")
		return
	}
	cols := []string{"Job", "Agent", "Stage", "Model", "Status", "Started", "Duration"}
	hasTokens, hasMoney := m.r.Mode.HasTokens(), m.r.Mode.HasMoney()
	if hasTokens {
		cols = append(cols, "Tokens")
	}
	if hasMoney {
		cols = append(cols, "Est. cost")
	}
	m.tableHeader(cols)
	for _, job := range jobs {
		status, duration := job.Status, formatDurationPtr(job.DurationMS)
		if job.EndRecorded != nil && !*job.EndRecorded {
			status += " (no recorded end)"
		}
		model := dash
		if job.Model != nil {
			model = cell(job.Model.Model)
		}
		cells := []string{
			cell(orDefault(job.Title, job.ID)), cell(job.Agent.Title), orDash(job.StageID), model, status,
			job.StartedAt.Format(time.RFC3339), duration,
		}
		if hasTokens {
			cells = append(cells, tokenCell(job.Tokens))
		}
		if hasMoney {
			cells = append(cells, moneyCell(job.Money))
		}
		m.tableRow(cells)
	}
	m.printf("\n")
}

// budgets reuses the budget helpers of `afm check`; the section follows the budgets' own display policy.
func (m reportMarkdown) budgets() {
	b := m.r.Budgets
	snapshot := m.r.Run.Status == state.RunStatusRunning
	switch {
	case b.UnavailableReason != "":
		m.printf("## %s\n\n> Budgets unavailable (%s).\n\n", budgetsTitle, b.UnavailableReason)
	case len(b.Items) > 0:
		views := make([]budget.View, 0, len(b.Items))
		for _, item := range b.Items {
			views = append(views, item.View)
		}
		printBudgetsMarkdown(m.w, views, snapshot)
		m.printf("\n")
	default:
		// No scopes: the section is omitted.
	}
}

func (m reportMarkdown) diagnostics() {
	m.printf("## Diagnostics\n\n")
	if len(m.r.Diagnostics) == 0 {
		m.printf("None.\n\n")
		return
	}
	for _, d := range m.r.Diagnostics {
		m.printf("- `%s`: %s\n", d.Code, d.Message)
	}
	m.printf("\n")
}

func (m reportMarkdown) tableHeader(cols []string) {
	m.printf("| %s |\n|%s\n", strings.Join(cols, " | "), strings.Repeat("---|", len(cols)))
}

func (m reportMarkdown) tableRow(cells []string) { m.printf("| %s |\n", strings.Join(cells, " | ")) }

func statusAndTime(s runreport.StageReport) string {
	status := orDash(s.Status)
	if s.Timing.StageTimeSums == nil {
		return status
	}
	t := s.Timing.StageTimeSums
	total := t.WorkMS + t.ApprovalMS + t.HelpersMS + t.BackoffMS + t.SchedulingMS + t.PausedMS
	if total <= 0 {
		return status
	}
	return fmt.Sprintf("%s (%s)", status, formatMillis(total))
}

func phaseList(phases map[string]int) string {
	if len(phases) == 0 {
		return dash
	}
	names := make([]string, 0, len(phases))
	for name := range phases {
		names = append(names, name)
	}
	slices.Sort(names)
	parts := make([]string, len(names))
	for i, name := range names {
		parts[i] = fmt.Sprintf("%s×%d", name, phases[name])
	}
	return strings.Join(parts, ", ")
}

func tokenCell(t *usageview.Tokens) string {
	if t == nil || t.Counts == nil {
		return dash
	}
	return groupDigits(t.Counts.Total)
}

// moneyCell shows a partial sum with "+" when some jobs could not be priced.
func moneyCell(m *usageview.Money) string {
	if m == nil || m.EstimatedCostUSD == nil {
		return dash
	}
	text := accounting.FormatUSD(*m.EstimatedCostUSD, true)
	if m.UnpricedJobs > 0 {
		text += "+"
	}
	return text
}

func roleOf(a *usageview.Agent) string {
	if a == nil {
		return dash
	}
	if role, ok := agentRoles[a.Kind]; ok {
		return role
	}
	return a.Kind
}

// formatMillis renders a measured duration to whole seconds.
func formatMillis(ms int64) string {
	return (time.Duration(ms) * time.Millisecond).Round(time.Second).String()
}

func formatDurationPtr(ms *int64) string {
	if ms == nil {
		return dash
	}
	return formatMillis(*ms)
}

// cell keeps a value from breaking the table row it is in.
func cell(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "|", `\|`), "\n", " ")
}

func joinOrDash(items []string) string {
	if len(items) == 0 {
		return dash
	}
	return cell(strings.Join(items, ", "))
}

func orDash(s string) string { return orDefault(s, dash) }

func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
