package runreport

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/contracts/accounting"
	"github.com/akopichin/afm/contracts/agentjobs"
	"github.com/akopichin/afm/contracts/budget"
	"github.com/akopichin/afm/contracts/state"
	"github.com/akopichin/afm/pkg/usageview"
)

const reportRunID = "flow-20261101-100000"

func rec(stage, phase, job string, mut ...func(*accounting.UsageRecord)) accounting.UsageRecord {
	r := accounting.UsageRecord{
		RecordVersion: 1, StageID: stage, Phase: phase, JobID: job, Channel: "claude", Model: "m1",
		Metered: true, Priced: true, Tokens: accounting.Tokens{UncachedInput: 100, Output: 50}, EstimatedCostUSD: 0.5,
		Rate: &accounting.RateSnapshot{Key: "claude/m1", Source: "builtin", AsOf: "2026-01-01"},
	}
	for _, m := range mut {
		m(&r)
	}
	return r
}

func unpriced(r *accounting.UsageRecord) { r.Priced, r.EstimatedCostUSD, r.Rate = false, 0, nil }

func stageJob(id, stage string) agentjobs.JobMeta {
	return agentjobs.JobMeta{JobID: id, StageID: stage, AgentID: "stage:" + stage, AgentKind: accounting.AgentKindStage, Phase: "implementation"}
}

// reportSources loads a two-stage run with the usage, jobs and Assistant usage the caller wrote.
func reportSources(t *testing.T, prepare func(runDir, sideDir string)) *Sources {
	t.Helper()
	_, runDir, sideDir := newAfmRoot(t, reportRunID, "build", "test")
	prepare(runDir, sideDir)
	src, err := Load(runDir, LoadOptions{Now: func() time.Time { return fixtureStart.Add(time.Hour) }})
	if err != nil {
		t.Fatal(err)
	}
	if len(src.State.StageOrder) == 0 { // the test events carry transitions only, not the flow's stage order
		src.State.StageOrder = []string{"build", "test"}
	}
	return src
}

func mustBuild(t *testing.T, src *Sources, opts Options) *Report {
	t.Helper()
	r, err := Build(src, opts)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestBuildStageScopeKeepsAttemptsAndHelpersWithoutRunWideData(t *testing.T) {
	src := reportSources(t, func(runDir, sideDir string) {
		writeUsage(t, runDir,
			rec("build", "implementation", "j1"), rec("build", "implementation", "j2"),
			rec("test", "implementation", "j3"),
			rec("", "overhead", "", func(r *accounting.UsageRecord) { r.Scope = accounting.ScopeRunOverhead }),
		)
		writeJobs(t, runDir, stageJob("j1", "build"), stageJob("j2", "build"), stageJob("j3", "test"))
		writeUsage(t, sideDir, rec("", "assistant", "a1"))
		writeJobs(t, sideDir, agentjobs.JobMeta{JobID: "a1", AgentID: "side:" + reportRunID, AgentKind: accounting.AgentKindAssistant})
	})

	all := mustBuild(t, src, Options{Mode: usageview.ModeFull})
	stage := mustBuild(t, src, Options{Mode: usageview.ModeFull, StageID: "build"})

	if all.Analytics.Totals.Jobs != 3 || all.Analytics.OutsideRunTotal == nil || all.Analytics.OutsideRunTotal.Jobs != 1 {
		t.Errorf("run totals = %+v outside = %+v, want 3 jobs and the Assistant outside", all.Analytics.Totals, all.Analytics.OutsideRunTotal)
	}
	if stage.Analytics.Totals.Jobs != 2 || len(stage.Analytics.Jobs) != 2 || stage.Analytics.OutsideRunTotal != nil {
		t.Errorf("stage analytics = %+v, want the two attempts and no outside total", stage.Analytics.Totals)
	}
	if len(stage.Cost.Stages) != 1 || stage.Cost.Stages[0].StageID != "build" || stage.Cost.Stages[0].Invocations != 2 {
		t.Errorf("stage cost rows = %+v", stage.Cost.Stages)
	}
	if stage.Cost.Total != nil || stage.Cost.Overhead != nil || stage.Cost.Unattributed != nil {
		t.Error("a stage report must not carry run-wide cost lines")
	}
	if all.Cost.Overhead == nil || all.Cost.Total.Invocations != 4 || len(all.Cost.Stages) != 2 {
		t.Errorf("run cost = %+v, want the overhead and a total of every invocation", all.Cost)
	}
	if len(stage.Stages) != 1 || stage.Scope.Kind != ScopeStage || !hasDiagnostic(stage, "stage_scope") {
		t.Errorf("stage scope = %+v diagnostics = %+v", stage.Scope, stage.Diagnostics)
	}
}

func TestBuildRejectsUnknownStage(t *testing.T) {
	src := reportSources(t, func(runDir, _ string) {})
	if _, err := Build(src, Options{Mode: usageview.ModeFull, StageID: "nope"}); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("err = %v, want an unknown stage error", err)
	}
}

func TestBuildLegacyRunHasCostButExplicitJobGaps(t *testing.T) {
	src := reportSources(t, func(runDir, _ string) { writeUsage(t, runDir, rec("build", "implementation", "")) })

	r := mustBuild(t, src, Options{Mode: usageview.ModeFull})

	if r.Cost.State != CostOK || r.Cost.Total == nil || r.Cost.Total.Tokens.Total != 150 {
		t.Errorf("cost = %+v, a legacy run still has tokens", r.Cost)
	}
	if !hasDiagnostic(r, "jobs_absent") || r.Sources.Jobs.State != SourceAbsent {
		t.Errorf("sources = %+v diagnostics = %+v, want the journal gap named", r.Sources.Jobs, r.Diagnostics)
	}
	if r.Analytics.Health.Jobs != usageview.StoreAbsent {
		t.Errorf("analytics health = %+v", r.Analytics.Health)
	}
}

func TestBuildNoLedgerIsNoDataNotZero(t *testing.T) {
	src := reportSources(t, func(runDir, _ string) {})
	r := mustBuild(t, src, Options{Mode: usageview.ModeFull})
	if r.Cost.State != CostNoData || r.Cost.Total != nil || len(r.Cost.Stages) != 0 {
		t.Errorf("cost = %+v, want no_data without a total", r.Cost)
	}
}

func TestBuildCorruptLedgerKeepsTheReportAndFailsOnlyWithTokens(t *testing.T) {
	src := reportSources(t, func(runDir, _ string) {
		writeUsage(t, runDir, rec("build", "implementation", ""))
		appendRaw(t, filepath.Join(runDir, "usage.jsonl"), "{broken}\n")
	})

	full := mustBuild(t, src, Options{Mode: usageview.ModeNoMoney})

	if full.Cost.State != CostCorrupt || full.Cost.Total != nil || !hasDiagnostic(full, "usage_corrupt") {
		t.Errorf("cost = %+v diagnostics = %+v", full.Cost, full.Diagnostics)
	}
	if Failure(src, usageview.ModeNoMoney) == "" {
		t.Error("a corrupt ledger must fail with tokens shown")
	}
	if got := Failure(src, usageview.ModeTimeOnly); got != "" {
		t.Errorf("time_only failure = %q, the ledger is not shown", got)
	}
}

func TestBuildLedgerAndFactsDifferOnMixedPricing(t *testing.T) {
	src := reportSources(t, func(runDir, _ string) {
		writeUsage(t, runDir, rec("build", "implementation", "j1"), rec("build", "review", "j1", unpriced))
		writeJobs(t, runDir, stageJob("j1", "build"))
	})

	r := mustBuild(t, src, Options{Mode: usageview.ModeFull})

	line := r.Cost.Stages[0]
	if line.Invocations != 2 || line.Unpriced != 1 || line.Money.DisplayCost != "$0.5000+" || line.Money.PricedInvocations != 1 {
		t.Errorf("ledger line = %+v %+v, want a partial sum counted in invocations", line.CostLine, line.Money)
	}
	money := r.Analytics.Totals.Money
	if money.EstimatedCostUSD != nil || money.UnpricedJobs != 1 {
		t.Errorf("facts money = %+v, a job with an unpriced record has no price", money)
	}
	if len(r.Cost.Issues) != 1 || r.Cost.Issues[0].Kind != accounting.IssueKindUnpriced {
		t.Errorf("issues = %+v", r.Cost.Issues)
	}
}

func TestBuildUnmeteredAndMismatch(t *testing.T) {
	reported := 0.9
	src := reportSources(t, func(runDir, _ string) {
		writeUsage(t, runDir,
			rec("build", "implementation", "", func(r *accounting.UsageRecord) { r.Metered, r.Priced, r.Reason = false, false, "no_usage_event" }),
			rec("build", "review", "", func(r *accounting.UsageRecord) {
				r.ReportedCostUSD, r.Warnings = &reported, []string{mismatchWarning}
			}),
		)
	})

	full := mustBuild(t, src, Options{Mode: usageview.ModeFull})
	noMoney := mustBuild(t, src, Options{Mode: usageview.ModeNoMoney})

	if full.Cost.Total.Unmetered != 1 || len(full.Cost.Mismatches) != 1 || full.Cost.Mismatches[0].Money.EstimatedUSD != 0.5 {
		t.Errorf("full cost = %+v mismatches %+v", full.Cost.Total, full.Cost.Mismatches)
	}
	if len(full.Cost.Rates) != 1 || len(noMoney.Cost.Rates) != 0 {
		t.Errorf("rates full %v / no_money %v: only full shows them", full.Cost.Rates, noMoney.Cost.Rates)
	}
	if len(noMoney.Cost.Mismatches) != 1 || noMoney.Cost.Mismatches[0].Money != nil {
		t.Errorf("no_money mismatches = %+v, want the fact without amounts", noMoney.Cost.Mismatches)
	}
}

func TestBuildOpenJobIsAnnotatedNotRewritten(t *testing.T) {
	src := reportSources(t, func(runDir, _ string) {
		store, err := agentjobs.Open(runDir, agentjobs.Header{RecordingStartedAt: fixtureStart})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Append(agentjobs.Record{Type: agentjobs.TypeStarted, JobMeta: stageJob("j1", "build"), At: fixtureStart.Add(time.Second)}); err != nil {
			t.Fatal(err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	})

	r := mustBuild(t, src, Options{Mode: usageview.ModeFull})

	job := r.Analytics.Jobs[0]
	if job.EndRecorded == nil || *job.EndRecorded || job.Liveness != "unknown" || job.DurationMS != nil {
		t.Errorf("job = %+v, want the open-end annotation and no duration", job)
	}
	if !hasDiagnostic(r, "open_jobs_without_end") {
		t.Errorf("diagnostics = %+v", r.Diagnostics)
	}
}

// forbiddenKeys walks a decoded document and returns the dotted paths of keys the check rejects; the budgets
// block is skipped because it keeps its own display policy.
func forbiddenKeys(doc any, reject func(key string) bool) []string {
	var found []string
	var walk func(path string, v any)
	walk = func(path string, v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, child := range x {
				if path == "" && k == "budgets" {
					continue
				}
				if reject(k) {
					found = append(found, path+"/"+k)
				}
				walk(path+"/"+k, child)
			}
		case []any:
			for _, child := range x {
				walk(path, child)
			}
		default:
			// a scalar has no keys
		}
	}
	walk("", doc)
	return found
}

func isMoneyKey(k string) bool {
	return k == "money" || k == "price" || k == "rates" || strings.HasSuffix(k, "_usd")
}

func decoded(t *testing.T, r *Report) map[string]any {
	t.Helper()
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestBuildModesHideMoneyAndTokensByKeyName(t *testing.T) {
	reported := 0.9
	src := reportSources(t, func(runDir, sideDir string) {
		writeUsage(t, runDir,
			rec("build", "implementation", "j1", func(r *accounting.UsageRecord) {
				r.ReportedCostUSD, r.Warnings = &reported, []string{mismatchWarning}
			}),
			rec("build", "review", "j1", unpriced),
		)
		writeJobs(t, runDir, stageJob("j1", "build"))
		writeUsage(t, sideDir, rec("", "assistant", "a1"))
		writeJobs(t, sideDir, agentjobs.JobMeta{JobID: "a1", AgentID: "side:" + reportRunID, AgentKind: accounting.AgentKindAssistant})
	})

	full := decoded(t, mustBuild(t, src, Options{Mode: usageview.ModeFull}))
	if len(forbiddenKeys(full, isMoneyKey)) == 0 {
		t.Fatal("the scan found no money key in full mode: the check proves nothing")
	}

	noMoney := mustBuild(t, src, Options{Mode: usageview.ModeNoMoney})
	if bad := forbiddenKeys(decoded(t, noMoney), isMoneyKey); len(bad) > 0 {
		t.Errorf("no_money leaks money keys: %v", bad)
	}
	if noMoney.Cost == nil || noMoney.Cost.Total.Tokens == nil || noMoney.Cost.Total.Invocations == 0 {
		t.Errorf("no_money keeps the ledger block with tokens: %+v", noMoney.Cost)
	}

	timeOnly := mustBuild(t, src, Options{Mode: usageview.ModeTimeOnly})
	doc := decoded(t, timeOnly)
	if bad := forbiddenKeys(doc, func(k string) bool { return isMoneyKey(k) || k == "tokens" || k == "cost" }); len(bad) > 0 {
		t.Errorf("time_only leaks keys: %v", bad)
	}
	if timeOnly.Cost != nil || timeOnly.Analytics.Totals.AgentTime.MeasuredJobs == 0 {
		t.Errorf("time_only = cost %v, agent time %+v: want no cost and the job time", timeOnly.Cost, timeOnly.Analytics.Totals.AgentTime)
	}
	if timeOnly.Analytics.Health.Usage != usageview.StoreDisabled {
		t.Errorf("time_only usage health = %q", timeOnly.Analytics.Health.Usage)
	}
}

func TestBuildBudgetsFollowTheirOwnPolicyInEveryMode(t *testing.T) {
	src := reportSources(t, func(runDir, _ string) {})
	tokenLimit := budget.Spec{Tokens: 1000}
	moneyLimit := budget.Spec{MoneyUSD: 2}
	src.Budgets = []budget.View{
		{Scope: "run", Kind: budget.ViewRun, Effective: &tokenLimit, Spent: budget.Spend{Tokens: 900}, Evaluation: budget.Evaluate(tokenLimit, budget.Spend{Tokens: 900})},
		{Scope: "stage:build", Kind: budget.ViewStage, Effective: &moneyLimit, Spent: budget.Spend{Tokens: 5, MoneyNanoUSD: 1_000_000_000}},
		{Scope: "stage:test", Kind: budget.ViewStage, Spent: budget.Spend{Tokens: 7}},
	}

	for _, mode := range []usageview.Mode{usageview.ModeFull, usageview.ModeTimeOnly} {
		items := mustBuild(t, src, Options{Mode: mode}).Budgets.Items
		if len(items) != 3 {
			t.Fatalf("%s: items = %+v", mode, items)
		}
		if items[0].Tokens == nil || *items[0].Tokens.Limit != 1000 || items[0].Money != nil || items[0].Level != "warning" {
			t.Errorf("%s: token-limited item = %+v", mode, items[0])
		}
		if items[1].Money == nil || items[1].Money.SpentUSD != 1 || items[1].Tokens != nil {
			t.Errorf("%s: money-limited item = %+v", mode, items[1])
		}
		if items[2].Tokens == nil || items[2].Tokens.Limit != nil || items[2].Level != levelNoLimit {
			t.Errorf("%s: unlimited item = %+v", mode, items[2])
		}
	}
}

func TestBuildBudgetsUnavailableStaysInTheModel(t *testing.T) {
	src := reportSources(t, func(runDir, _ string) {})
	src.Budgets, src.BudgetsErr = nil, os.ErrInvalid

	r := mustBuild(t, src, Options{Mode: usageview.ModeFull})

	if r.Budgets.UnavailableReason == "" || len(r.Budgets.Items) != 0 || !hasDiagnostic(r, "budgets_unavailable") {
		t.Errorf("budgets = %+v diagnostics = %+v", r.Budgets, r.Diagnostics)
	}
	if Failure(src, usageview.ModeFull) != "" {
		t.Error("a budget read error must not change the exit code")
	}
}

func TestBuildStageTimeGroupsExcludeIncompleteStages(t *testing.T) {
	src := reportSources(t, func(runDir, _ string) {})
	src.State.StageOrder = []string{"a", "b", "c"}
	src.State.Stages = map[string]state.StageState{}
	src.State.Stages["a"] = stageWithTiming(1000, 200, 300, 400, 500, 600)
	src.State.Stages["b"] = stageWithIncompleteTiming()
	src.State.Stages["c"] = stageWithTiming(10, 0, 0, 0, 0, 0)

	r := mustBuild(t, src, Options{Mode: usageview.ModeTimeOnly})

	want := StageTimeSum{WorkingMS: 1010, WaitingMS: 200 + 300 + 400 + 500, PausedMS: 600, IncompleteID: []string{"b"}}
	got := r.StageTime
	if got.WorkingMS != want.WorkingMS || got.WaitingMS != want.WaitingMS || got.PausedMS != want.PausedMS || len(got.IncompleteID) != 1 || got.IncompleteID[0] != "b" {
		t.Errorf("stage time = %+v, want %+v", got, want)
	}
}

func hasDiagnostic(r *Report, code string) bool {
	for _, d := range r.Diagnostics {
		if d.Code == code {
			return true
		}
	}
	return false
}

func stageWithTiming(work, approval, helpers, backoff, scheduling, paused int64) state.StageState {
	return state.StageState{Timing: state.StageTiming{
		Started: true, WorkMs: work, ApprovalMs: approval, HelpersMs: helpers, BackoffMs: backoff,
		SchedulingMs: scheduling, PausedMs: paused,
	}}
}

func stageWithIncompleteTiming() state.StageState {
	return state.StageState{Timing: state.StageTiming{Started: true, Incomplete: true}}
}

func TestBuildKeepsAssistantJobsWhenRunJournalIsUnreadable(t *testing.T) {
	src := reportSources(t, func(runDir, sideDir string) {
		if err := os.MkdirAll(filepath.Join(runDir, "agent-jobs.jsonl"), 0o755); err != nil {
			t.Fatal(err)
		}
		writeJobs(t, sideDir, agentjobs.JobMeta{JobID: "a1", AgentID: "side:" + reportRunID})
	})

	r := mustBuild(t, src, Options{Mode: usageview.ModeFull})

	if r.Sources.Jobs.State != SourceUnavailable || r.Analytics.Health.Jobs == "" {
		t.Errorf("sources = %+v, health = %+v, want the unreadable run journal named", r.Sources.Jobs, r.Analytics.Health)
	}
	if len(r.Analytics.Jobs) != 1 || r.Analytics.OutsideRunTotal == nil {
		t.Errorf("jobs = %+v, outside = %+v, want the Assistant job kept", r.Analytics.Jobs, r.Analytics.OutsideRunTotal)
	}
}

func TestLoadDerivesTheSameReportFromAnyRunDirSpelling(t *testing.T) {
	_, runDir, sideDir := newAfmRoot(t, reportRunID, "build", "test")
	writeUsage(t, runDir, rec("build", "implementation", "j1"))
	writeJobs(t, runDir, stageJob("j1", "build"))
	writeJobs(t, sideDir, agentjobs.JobMeta{JobID: "a1", AgentID: "side:" + reportRunID})
	now := func() time.Time { return fixtureStart.Add(time.Hour) }

	build := func(dir string) string {
		src, err := Load(dir, LoadOptions{Now: now})
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(mustBuild(t, src, Options{Mode: usageview.ModeFull}))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	want := build(runDir)

	t.Chdir(runDir)
	if got := build("."); got != want {
		t.Errorf("report of %q differs from the absolute one:\n%s\nvs\n%s", ".", got, want)
	}
	t.Chdir(filepath.Dir(runDir))
	if got := build(reportRunID); got != want {
		t.Error("report of the bare name differs from the absolute one")
	}
	if !strings.Contains(want, `"id":"`+reportRunID+`"`) || !strings.Contains(want, `"outside_run_total"`) {
		t.Errorf("fixture lost the run id or Assistant data: %s", want)
	}
}
