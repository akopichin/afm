package accounting

import (
	"maps"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentjobs"
)

var anT0 = time.Date(2026, 10, 31, 11, 0, 0, 0, time.UTC)

// anJournal builds journal snapshots without touching disk; the store's own tests cover the file side.
type anJournal struct {
	header  agentjobs.Header
	records []agentjobs.Record
	health  agentjobs.Health
}

func newAnJournal(recordingStart time.Time) *anJournal {
	return &anJournal{
		header: agentjobs.Header{Type: agentjobs.TypeHeader, RecordVersion: 1, RecordingStartedAt: recordingStart},
		health: agentjobs.Health{Status: agentjobs.HealthOK},
	}
}

func (j *anJournal) start(meta agentjobs.JobMeta, at time.Time, offsetMS int64) *anJournal {
	rec := agentjobs.Record{Type: agentjobs.TypeStarted, JobMeta: meta, At: at}
	if offsetMS >= 0 {
		rec.Segment = agentjobs.Segment{RunStartedAt: anT0, BaseMS: 0, OffsetMS: offsetMS}
	}
	j.records = append(j.records, rec)
	return j
}

func (j *anJournal) startInSegment(meta agentjobs.JobMeta, at time.Time, base, offsetMS int64) *anJournal {
	rec := agentjobs.Record{Type: agentjobs.TypeStarted, JobMeta: meta, At: at,
		Segment: agentjobs.Segment{RunStartedAt: anT0.Add(time.Duration(base) * time.Millisecond), BaseMS: base, OffsetMS: offsetMS}}
	j.records = append(j.records, rec)
	return j
}

func (j *anJournal) finish(id string, outcome agentjobs.Outcome, at time.Time, dur time.Duration) *anJournal {
	j.records = append(j.records, agentjobs.Record{
		Type: agentjobs.TypeFinished, JobMeta: agentjobs.JobMeta{JobID: id}, At: at,
		Outcome: outcome, DurationMS: dur.Milliseconds(),
	})
	return j
}

func (j *anJournal) startFailed(meta agentjobs.JobMeta, at time.Time) *anJournal {
	j.records = append(j.records, agentjobs.Record{Type: agentjobs.TypeStartFailed, JobMeta: meta, At: at, Reason: "agent_start"})
	return j
}

func (j *anJournal) snapshot() agentjobs.Snapshot {
	return agentjobs.Snapshot{
		Header:   j.header,
		Records:  slices.Clone(j.records),
		Revision: uint64(len(j.records)),
		Health:   j.health,
	}
}

func anStageMeta(id, title string, startedAt time.Time) agentjobs.JobMeta {
	return agentjobs.JobMeta{AgentID: "stage:" + id, AgentKind: AgentKindStage, StageID: id, RootStageID: id, Title: title}
}

func anMeta() AnalyticsMeta {
	return AnalyticsMeta{
		Revision:  3,
		LiveSince: anT0.Add(8 * time.Minute),
		Stages: []StageMeta{
			{ID: "build", Title: "Build", StartedAt: anT0},
			{ID: "review", Title: "Review", StartedAt: anT0.Add(400 * time.Second)},
		},
		Agents: []AgentMeta{
			{ID: "helper:t-1", Kind: AgentKindHelper, Title: "Reviewer"},
			{ID: "side:run-1", Kind: AgentKindAssistant, Title: "Assistant"},
		},
	}
}

func anUsage(jobID, stage, channel, model string, tok Tokens, cost float64) UsageRecord {
	rec := UsageRecord{
		RecordVersion: 1, StageID: stage, Phase: "implementation",
		Channel: channel, Model: model, JobID: jobID, ModelSource: ModelSourceObserved,
		Metered: true, Priced: cost > 0, Tokens: tok, EstimatedCostUSD: cost,
		StartedAt: anT0.Format(time.RFC3339), FinishedAt: anT0.Format(time.RFC3339),
	}
	return rec
}

func anUnmetered(jobID, stage string) UsageRecord {
	return UsageRecord{RecordVersion: 1, StageID: stage, Phase: "implementation", JobID: jobID, Reason: "interrupted"}
}

func anTok(in, cr, cw5, out uint64) Tokens {
	return Tokens{UncachedInput: in, CacheRead: cr, CacheWrite5m: cw5, Output: out}
}

// anFullRun mirrors the contract fixture of the dashboard: six run jobs, one running, one interrupted
// by a crash, one Assistant job outside the run total.
func anFullRun() AnalyticsInput {
	j := newAnJournal(anT0)
	j.start(withTitle(anStageMeta("build", "", anT0), "j1", "Build · planning"), anT0, 0)
	j.finish("j1", agentjobs.OutcomeOK, anT0.Add(120*time.Second), 120*time.Second)
	j.start(withTitle(anStageMeta("build", "", anT0), "j2", "Build · implementation"), anT0.Add(130*time.Second), 130000)
	j.finish("j2", agentjobs.OutcomeOK, anT0.Add(430*time.Second), 300*time.Second)
	j.start(agentjobs.JobMeta{JobID: "j3", AgentID: "helper:t-1", AgentKind: AgentKindHelper, StageID: "build", RootStageID: "build", TaskID: "t-1", Title: "Review the diff"},
		anT0.Add(200*time.Second), 200000)
	j.finish("j3", agentjobs.OutcomeOK, anT0.Add(290*time.Second), 90*time.Second)
	j.start(agentjobs.JobMeta{JobID: "j4", AgentID: "verify:review#0", AgentKind: AgentKindVerify, StageID: "review", RootStageID: "review", Title: "Verify · step 1"},
		anT0.Add(400*time.Second), 400000)
	j.finish("j4", agentjobs.OutcomeOK, anT0.Add(460*time.Second), 60*time.Second)
	j.start(withHint(withTitle(anStageMeta("review", "", anT0), "j5", "Review · implementation"), "anthropic", "glm-5.3"), anT0.Add(480*time.Second), 480000)
	j.start(withTitle(anStageMeta("build", "", anT0), "j7", "Build · implementation"), anT0.Add(420*time.Second), 420000)
	j.start(agentjobs.JobMeta{JobID: "j6", AgentID: "side:run-1", AgentKind: AgentKindAssistant, Title: "Assistant · job 1"}, anT0.Add(500*time.Second), -1)
	j.finish("j6", agentjobs.OutcomeOK, anT0.Add(515*time.Second), 15*time.Second)

	return AnalyticsInput{
		Usage: []UsageRecord{
			anUsage("j1", "build", "anthropic", "claude-opus-4-8", anTok(1000, 5000, 400, 600), 0.42),
			anUsage("j2", "build", "anthropic", "claude-opus-4-8", anTok(3000, 20000, 1000, 2000), 1.35),
			anUsage("j3", "build", "codex", "gpt-5.6-sol", anTok(4000, 0, 0, 1000), 0),
			anUsage("j4", "review", "codex", "gpt-5.6-sol", anTok(2000, 0, 0, 500), 0.15),
			anUnmetered("j7", "build"),
			anUnmetered("j6", ""),
		},
		UsageRevision: 7,
		UsageState:    UsageStateOK,
		Jobs:          j.snapshot(),
		Meta:          anMeta(),
	}
}

func withTitle(m agentjobs.JobMeta, id, title string) agentjobs.JobMeta {
	m.JobID, m.Title = id, title
	return m
}

func withHint(m agentjobs.JobMeta, channel, model string) agentjobs.JobMeta {
	m.Channel, m.Model = channel, model
	return m
}

func anAsOf() time.Time { return anT0.Add(12 * time.Minute) }

func anJob(t *testing.T, items []JobItem, id string) JobItem {
	t.Helper()
	for _, it := range items {
		if it.ID == id {
			return it
		}
	}
	t.Fatalf("job %s not in list %v", id, jobIDs(items))
	return JobItem{}
}

func jobIDs(items []JobItem) []string {
	ids := make([]string, len(items))
	for i, it := range items {
		ids[i] = it.ID
	}
	return ids
}

func rowKeys(rows []Row) []string {
	keys := make([]string, len(rows))
	for i, r := range rows {
		keys[i] = r.Key
	}
	return keys
}

func rowByKey(t *testing.T, rows []Row, key string) Row {
	t.Helper()
	for _, r := range rows {
		if r.Key == key {
			return r
		}
	}
	t.Fatalf("row %q not in %v", key, rowKeys(rows))
	return Row{}
}

func closeTo(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestAnalytics_JobsJoinUsageByJobID(t *testing.T) {
	f := BuildFacts(anFullRun())
	jobs := f.Select(Filter{}, anAsOf()).Jobs(SortStarted, true)

	j2 := anJob(t, jobs, "j2")
	if j2.Status != "ok" || j2.DurationMS == nil || *j2.DurationMS != 300000 {
		t.Fatalf("j2 = %+v", j2)
	}
	if j2.Agent.ID != "stage:build" || j2.Agent.Kind != AgentKindStage || j2.Agent.Title != "Build" {
		t.Fatalf("j2 agent = %+v", j2.Agent)
	}
	if j2.Model == nil || j2.Model.Key != "anthropic/claude-opus-4-8" || j2.ModelSource != ModelSourceObserved {
		t.Fatalf("j2 model = %+v source %q", j2.Model, j2.ModelSource)
	}
	if j2.Tokens.Counts == nil || j2.Tokens.Counts.Total() != 26000 || j2.Tokens.Metered != 1 {
		t.Fatalf("j2 tokens = %+v", j2.Tokens)
	}
	if j2.Money.EstimatedCostUSD == nil || !closeTo(*j2.Money.EstimatedCostUSD, 1.35) || j2.Money.Priced != 1 {
		t.Fatalf("j2 money = %+v", j2.Money)
	}
	if j2.RunOffsetMS == nil || *j2.RunOffsetMS != 130000 {
		t.Fatalf("j2 offset = %v", j2.RunOffsetMS)
	}
	if j2.StageID != "build" {
		t.Fatalf("j2 stage = %q", j2.StageID)
	}

	// A helper job is attributed to the stage that owns it, under its own agent.
	j3 := anJob(t, jobs, "j3")
	if j3.StageID != "build" || j3.Agent.ID != "helper:t-1" || j3.Agent.Title != "Reviewer" {
		t.Fatalf("j3 = %+v", j3)
	}
	// Priced=false is unpriced, not zero.
	if j3.Money.EstimatedCostUSD != nil || j3.Money.Unpriced != 1 || j3.Money.Priced != 0 {
		t.Fatalf("j3 money = %+v", j3.Money)
	}
}

func TestAnalytics_TotalsMatchFixture(t *testing.T) {
	f := BuildFacts(anFullRun())
	sel := f.Select(Filter{}, anAsOf())
	got := sel.Totals()

	if got.Jobs != 6 || got.ActiveJobs != 1 {
		t.Fatalf("jobs/active = %d/%d", got.Jobs, got.ActiveJobs)
	}
	if got.Time.DurationMS == nil || *got.Time.DurationMS != 810000 || got.Time.Measured != 5 || got.Time.Unknown != 1 {
		t.Fatalf("time = %+v", got.Time)
	}
	if got.Tokens.Metered != 4 || got.Tokens.Unmetered != 1 || got.Tokens.Pending != 1 {
		t.Fatalf("tokens = %+v", got.Tokens)
	}
	if got.Tokens.Counts == nil || got.Tokens.Counts.Total() != 7000+26000+5000+2500 {
		t.Fatalf("token counts = %+v", got.Tokens.Counts)
	}
	if got.Money.Priced != 3 || got.Money.Unpriced != 1 || got.Money.EstimatedCostUSD == nil || !closeTo(*got.Money.EstimatedCostUSD, 1.92) {
		t.Fatalf("money = %+v", got.Money)
	}

	outside := sel.Outside()
	if outside == nil || outside.Jobs != 1 || outside.Time.DurationMS == nil || *outside.Time.DurationMS != 15000 {
		t.Fatalf("outside = %+v", outside)
	}
	if outside.Tokens.Unmetered != 1 || outside.Tokens.Counts != nil {
		t.Fatalf("outside tokens = %+v", outside.Tokens)
	}
}

func TestAnalytics_RunningJobElapsedFollowsAsOfButFactsAreShared(t *testing.T) {
	f := BuildFacts(anFullRun())

	at12 := anJob(t, f.Select(Filter{}, anAsOf()).Jobs(SortStarted, true), "j5")
	at15 := anJob(t, f.Select(Filter{}, anT0.Add(15*time.Minute)).Jobs(SortStarted, true), "j5")
	if at12.Status != "running" || at12.DurationMS == nil || *at12.DurationMS != 240000 {
		t.Fatalf("at 12m = %+v", at12)
	}
	if at15.DurationMS == nil || *at15.DurationMS != 420000 {
		t.Fatalf("at 15m = %+v", at15)
	}
	if at12.Tokens.Pending != 1 || at12.Tokens.Counts != nil {
		t.Fatalf("running job tokens = %+v", at12.Tokens)
	}
	// A running job placed before its own start is never negative.
	early := anJob(t, f.Select(Filter{}, anT0.Add(7*time.Minute)).Jobs(SortStarted, true), "j5")
	if early.DurationMS == nil || *early.DurationMS != 0 {
		t.Fatalf("early = %+v", early)
	}
}

func TestAnalytics_OpenJobFromEarlierProcessIsInterruptedWithUnknownTime(t *testing.T) {
	f := BuildFacts(anFullRun())
	jobs := f.Select(Filter{}, anAsOf()).Jobs(SortStarted, true)

	j7 := anJob(t, jobs, "j7")
	if j7.Status != "interrupted" || j7.DurationMS != nil {
		t.Fatalf("j7 = %+v", j7)
	}
	// Its start stays a fact, and a timer never keeps ticking for it.
	later := anJob(t, f.Select(Filter{}, anT0.Add(time.Hour)).Jobs(SortStarted, true), "j7")
	if later.DurationMS != nil || !later.StartedAt.Equal(anT0.Add(420*time.Second)) {
		t.Fatalf("later = %+v", later)
	}
	if j7.Tokens.Unmetered != 1 {
		t.Fatalf("j7 tokens = %+v", j7.Tokens)
	}
}

func TestAnalytics_OfflineLoadHasNoRunningJobs(t *testing.T) {
	in := anFullRun()
	in.Meta.LiveSince = time.Time{}
	f := BuildFacts(in)
	jobs := f.Select(Filter{}, anAsOf()).Jobs(SortStarted, true)

	j5 := anJob(t, jobs, "j5")
	if j5.Status != "interrupted" || j5.DurationMS != nil {
		t.Fatalf("j5 = %+v", j5)
	}
	if f.Select(Filter{}, anAsOf()).Totals().ActiveJobs != 0 {
		t.Fatal("no process is alive, so nothing is active")
	}
}

func TestAnalytics_StartFailedIsNotAJobButIsReported(t *testing.T) {
	in := anFullRun()
	j := newAnJournal(anT0)
	j.records = slices.Clone(in.Jobs.Records)
	j.startFailed(agentjobs.JobMeta{JobID: "jf", AgentID: "stage:build", AgentKind: AgentKindStage, StageID: "build", RootStageID: "build"}, anT0.Add(9*time.Minute))
	in.Jobs = j.snapshot()
	in.Usage = append(in.Usage, anUnmetered("jf", "build"))
	f := BuildFacts(in)
	sel := f.Select(Filter{}, anAsOf())

	totals := sel.Totals()
	if totals.Jobs != 6 || totals.Time.Measured != 5 || totals.Time.Unknown != 1 {
		t.Fatalf("a failed start must not count as a job or as agent time: %+v", totals)
	}
	if f.Health().StartFailed != 1 {
		t.Fatalf("health = %+v", f.Health())
	}
	if !slices.Contains(sel.Completeness().Time.Reasons, "start_failed") {
		t.Fatalf("time reasons = %v", sel.Completeness().Time.Reasons)
	}
	for _, it := range sel.Jobs(SortStarted, true) {
		if it.ID == "jf" {
			t.Fatal("start_failed attempt listed as a job")
		}
	}
	// Its unmetered usage record still counts, as it does in the run total.
	if totals.Tokens.Unmetered != 2 {
		t.Fatalf("unmetered = %d, want j7 + the failed attempt", totals.Tokens.Unmetered)
	}
	// Scoping to another stage leaves the reason out.
	review := f.Select(Filter{Scope: FilterStage, StageID: "review"}, anAsOf()).Completeness()
	if slices.Contains(review.Time.Reasons, "start_failed") {
		t.Fatalf("review reasons = %v", review.Time.Reasons)
	}
}

func TestAnalytics_UsageWithTwoRecordsForOneJobCountsOneJob(t *testing.T) {
	in := anFullRun()
	in.Usage = append(in.Usage, anUsage("j1", "build", "anthropic", "claude-opus-4-8", anTok(10, 0, 0, 5), 0.01))
	f := BuildFacts(in)
	sel := f.Select(Filter{}, anAsOf())

	j1 := anJob(t, sel.Jobs(SortStarted, true), "j1")
	if j1.Tokens.Counts.Total() != 7000+15 || j1.Tokens.Metered != 1 {
		t.Fatalf("j1 tokens = %+v", j1.Tokens)
	}
	if sel.Totals().Jobs != 6 {
		t.Fatalf("jobs = %d", sel.Totals().Jobs)
	}
}

func TestAnalytics_AssistantIsOutsideTheRunTotalButListed(t *testing.T) {
	f := BuildFacts(anFullRun())
	sel := f.Select(Filter{}, anAsOf())

	agents := sel.Rows(GroupAgent, SortTime, true)
	side := rowByKey(t, agents, "side:run-1")
	if !side.Agent.OutsideRunTotal || side.Agent.Kind != AgentKindAssistant {
		t.Fatalf("assistant row = %+v", side.Agent)
	}
	models := sel.Rows(GroupModel, SortTime, true)
	var jobs int
	for _, r := range models {
		jobs += r.Jobs
	}
	if jobs != sel.Totals().Jobs {
		t.Fatalf("model rows hold %d jobs, run total %d: the Assistant belongs to neither", jobs, sel.Totals().Jobs)
	}
	if !slices.ContainsFunc(sel.Jobs(SortStarted, true), func(j JobItem) bool { return j.ID == "j6" }) {
		t.Fatal("the Assistant job must stay in the jobs list")
	}
	if !f.Catalog().Scopes.RunWide {
		t.Fatal("the Assistant is run-wide work")
	}
}

func TestAnalytics_ModelKeyIsChannelPlusModel(t *testing.T) {
	in := anFullRun()
	jr := newAnJournal(anT0)
	jr.records = slices.Clone(in.Jobs.Records)
	jr.start(withTitle(anStageMeta("build", "", anT0), "j8", "Build · other"), anT0.Add(2*time.Minute), 120000)
	jr.finish("j8", agentjobs.OutcomeOK, anT0.Add(3*time.Minute), time.Minute)
	in.Jobs = jr.snapshot()
	in.Usage = append(in.Usage, anUsage("j8", "build", "other-vendor", "claude-opus-4-8", anTok(1, 0, 0, 1), 0))

	rows := BuildFacts(in).Select(Filter{}, anAsOf()).Rows(GroupModel, SortName, false)
	keys := rowKeys(rows)
	if !slices.Contains(keys, "anthropic/claude-opus-4-8") || !slices.Contains(keys, "other-vendor/claude-opus-4-8") {
		t.Fatalf("same model name of two channels must stay two rows: %v", keys)
	}
}

func TestAnalytics_ModelProvenanceAndUnknown(t *testing.T) {
	f := BuildFacts(anFullRun())
	sel := f.Select(Filter{}, anAsOf())
	jobs := sel.Jobs(SortStarted, true)

	if j5 := anJob(t, jobs, "j5"); j5.ModelSource != ModelSourceHint || j5.Model == nil || j5.Model.Key != "anthropic/glm-5.3" {
		t.Fatalf("a configured model is a hint: %+v", j5)
	}
	if j6 := anJob(t, jobs, "j6"); j6.Model != nil || j6.ModelSource != "" {
		t.Fatalf("no model stays unknown, not guessed: %+v", j6)
	}
	// j7 was configured as opus-less: its usage is unmetered and carries no model, so it is unknown too.
	if j7 := anJob(t, jobs, "j7"); j7.Model != nil {
		t.Fatalf("j7 model = %+v", j7.Model)
	}

	agents := sel.Rows(GroupAgent, SortName, false)
	build := rowByKey(t, agents, "stage:build")
	if build.ModelState != ModelStateMixed || build.Model != nil {
		t.Fatalf("build mixes a known and an unknown model: %+v", build)
	}
	review := rowByKey(t, agents, "stage:review")
	if review.ModelState != ModelStateSingle || review.Model == nil || review.ModelSource != ModelSourceHint {
		t.Fatalf("review row = %+v", review)
	}
	verify := rowByKey(t, agents, "verify:review#0")
	if verify.ModelState != ModelStateSingle || verify.ModelSource != ModelSourceObserved {
		t.Fatalf("verify row = %+v", verify)
	}
	side := rowByKey(t, agents, "side:run-1")
	if side.ModelState != ModelStateUnknown || side.Model != nil || side.ModelSource != "" {
		t.Fatalf("assistant row = %+v", side)
	}

	models := sel.Rows(GroupModel, SortName, false)
	if unknown := rowByKey(t, models, UnknownModelKey); unknown.Model != nil || unknown.Label != "Unknown" || unknown.Jobs != 1 {
		t.Fatalf("unknown bucket = %+v", unknown)
	}
}

func TestAnalytics_ColorOrdinalIsFirstAppearanceAndStable(t *testing.T) {
	in := anFullRun()
	before := BuildFacts(in).Catalog()
	ordinals := func(c Catalog) map[string]int {
		out := map[string]int{}
		for _, m := range c.Models {
			out[m.Key] = m.ColorOrdinal
		}
		return out
	}
	got := ordinals(before)
	// opus first (j1), codex (j3), glm (j5, hint): in order of first appearance.
	if got["anthropic/claude-opus-4-8"] != 0 || got["codex/gpt-5.6-sol"] != 1 || got["anthropic/glm-5.3"] != 2 {
		t.Fatalf("ordinals = %v", got)
	}

	// A new model that appears later takes the next free ordinal and moves nobody.
	jr := newAnJournal(anT0)
	jr.records = slices.Clone(in.Jobs.Records)
	jr.start(withTitle(anStageMeta("build", "", anT0), "j9", "late"), anT0.Add(11*time.Minute), 660000)
	jr.finish("j9", agentjobs.OutcomeOK, anT0.Add(11*time.Minute+time.Second), time.Second)
	in.Jobs = jr.snapshot()
	in.Usage = append(in.Usage, anUsage("j9", "build", "anthropic", "claude-sonnet-new", anTok(1, 0, 0, 1), 0))
	after := ordinals(BuildFacts(in).Catalog())
	for k, v := range got {
		if after[k] != v {
			t.Fatalf("%s moved from %d to %d", k, v, after[k])
		}
	}
	if after["anthropic/claude-sonnet-new"] != 3 {
		t.Fatalf("new model ordinal = %v", after)
	}

	// Filters never renumber: the catalog ignores them and the rows reuse the same refs.
	rows := BuildFacts(in).Select(Filter{Scope: FilterStage, StageID: "review"}, anAsOf()).Rows(GroupModel, SortName, false)
	for _, r := range rows {
		if r.Model != nil && r.Model.ColorOrdinal != after[r.Model.Key] {
			t.Fatalf("row %s has ordinal %d, catalog %d", r.Key, r.Model.ColorOrdinal, after[r.Model.Key])
		}
	}
}

func TestAnalytics_CatalogIgnoresFilters(t *testing.T) {
	f := BuildFacts(anFullRun())
	c := f.Catalog()

	var stageIDs, agentIDs []string
	for _, s := range c.Stages {
		stageIDs = append(stageIDs, s.ID)
	}
	for _, a := range c.Agents {
		agentIDs = append(agentIDs, a.ID)
	}
	if !slices.Equal(stageIDs, []string{"build", "review"}) {
		t.Fatalf("stages = %v", stageIDs)
	}
	// Agents appear in order of their first job.
	want := []string{"stage:build", "helper:t-1", "verify:review#0", "stage:review", "side:run-1"}
	if !slices.Equal(agentIDs, want) {
		t.Fatalf("agents = %v, want %v", agentIDs, want)
	}
	if c.Scopes.Unattributed {
		t.Fatal("nothing is unattributed in this run")
	}
	if !reflect.DeepEqual(c, f.Select(Filter{Agent: "helper:t-1"}, anAsOf()).f.Catalog()) {
		t.Fatal("selecting must not change the catalog")
	}
}

func TestAnalytics_LegacyUsageKeepsItsAttribution(t *testing.T) {
	usage := []UsageRecord{
		{RecordVersion: 1, StageID: "build", Phase: "implementation", Channel: "anthropic", Model: "claude-opus-4-8", Metered: true, Priced: true, EstimatedCostUSD: 0.5, Tokens: anTok(3000, 5000, 0, 1000)},
		{RecordVersion: 1, StageID: "build", Phase: "implementation", Channel: "anthropic", Model: "claude-opus-4-8", Metered: true, Priced: true, EstimatedCostUSD: 0.3, Tokens: anTok(2000, 5000, 0, 1000)},
		{RecordVersion: 1, StageID: "build", RootStageID: "build", TaskID: "t-1", Phase: "check", Channel: "anthropic", Model: "glm-5.3", Metered: true, Priced: true, EstimatedCostUSD: 0.2, Tokens: anTok(2000, 0, 0, 500)},
		{RecordVersion: 1, Phase: "memory", Scope: ScopeRunOverhead, Channel: "anthropic", Model: "glm-5.3", Metered: true, Priced: true, EstimatedCostUSD: 0.1, Tokens: anTok(100, 0, 0, 10)},
		{RecordVersion: 1, Phase: "x", Reason: "interrupted"},
	}
	f := BuildFacts(AnalyticsInput{
		Usage: usage, UsageRevision: 5, UsageState: UsageStateOK,
		Jobs: absentJobs(), Meta: anMeta(),
	})
	sel := f.Select(Filter{}, anAsOf())

	totals := sel.Totals()
	if totals.Jobs != 0 || totals.Time.DurationMS != nil || totals.Time.Measured != 0 || totals.Time.Unknown != 0 {
		t.Fatalf("legacy has no jobs and no time: %+v", totals)
	}
	if totals.Tokens.Metered != 4 || totals.Tokens.Unmetered != 1 {
		t.Fatalf("legacy tokens count usage records: %+v", totals.Tokens)
	}
	if f.Health().Jobs != HealthAbsent || f.Health().Usage != HealthOK {
		t.Fatalf("health = %+v", f.Health())
	}
	c := sel.Completeness()
	if c.Time.Status != CompletenessUnknown || !slices.Contains(c.Time.Reasons, "legacy_journal") {
		t.Fatalf("legacy time must be unknown, never a zero: %+v", c.Time)
	}

	rows := sel.Rows(GroupAgent, SortTokens, true)
	build := rowByKey(t, rows, "stage:build")
	if build.Tokens.Metered != 2 || build.Tokens.Counts.Total() != 17000 || build.ModelSource != ModelSourceLegacy || build.ModelState != ModelStateSingle {
		t.Fatalf("build = %+v", build)
	}
	if helper := rowByKey(t, rows, "helper:t-1"); helper.Tokens.Metered != 1 {
		t.Fatalf("a task's usage belongs to its helper: %+v", helper)
	}
	if over := rowByKey(t, rows, RunOverheadAgentID); over.Agent.Kind != AgentKindRunOverhead {
		t.Fatalf("run overhead row = %+v", over)
	}
	un := rowByKey(t, rows, UnattributedAgentID)
	if un.Agent.Kind != AgentKindUnknown || un.Tokens.Unmetered != 1 || un.Tokens.Counts != nil {
		t.Fatalf("unattributed = %+v", un)
	}
	if !f.Catalog().Scopes.Unattributed {
		t.Fatal("unattributed scope must be offered")
	}
	// The helper's usage is attributed to its root stage for the stage filter.
	stage := f.Select(Filter{Scope: FilterStage, StageID: "build"}, anAsOf()).Totals()
	if stage.Tokens.Metered != 3 {
		t.Fatalf("stage build = %+v", stage.Tokens)
	}
}

func TestAnalytics_UsageWithUnknownJobIDFallsBackToItsFields(t *testing.T) {
	in := anFullRun()
	in.Usage = append(in.Usage, anUsage("ghost", "review", "anthropic", "claude-opus-4-8", anTok(10, 0, 0, 10), 0.01))
	sel := BuildFacts(in).Select(Filter{Scope: FilterStage, StageID: "review"}, anAsOf())

	if sel.Totals().Jobs != 2 {
		t.Fatalf("an orphan usage record is not a job: %+v", sel.Totals())
	}
	if sel.Totals().Tokens.Metered != 2 {
		t.Fatalf("but it is counted once: %+v", sel.Totals().Tokens)
	}
}

func TestAnalytics_TotalsReconcileWithRunSummary(t *testing.T) {
	in := anFullRun()
	// Mix in legacy, run-overhead and unattributed records so every bucket is exercised.
	in.Usage = append(in.Usage,
		UsageRecord{RecordVersion: 1, StageID: "build", Phase: "implementation", Channel: "anthropic", Model: "claude-opus-4-8", Metered: true, Priced: true, EstimatedCostUSD: 0.7, Tokens: anTok(500, 100, 0, 50)},
		UsageRecord{RecordVersion: 1, Phase: "memory", Scope: ScopeRunOverhead, Channel: "anthropic", Model: "glm-5.3", Metered: true, Priced: true, EstimatedCostUSD: 0.05, Tokens: anTok(70, 0, 0, 7)},
		UsageRecord{RecordVersion: 1, Phase: "?", Channel: "anthropic", Model: "mystery", Metered: true, Tokens: anTok(9, 0, 0, 1)},
	)
	// The Assistant keeps its own ledger, so the run's usage never holds its records.
	run := slices.DeleteFunc(slices.Clone(in.Usage), func(r UsageRecord) bool { return r.JobID == "j6" })
	in.Usage = run

	ledger := &Ledger{recs: run}
	want := ledger.RunSummary()

	f := BuildFacts(in)
	for _, name := range []string{"model/time", "agent/tokens", "model/name asc"} {
		t.Run(name, func(t *testing.T) {
			sel := f.Select(Filter{}, anAsOf())
			var rows []Row
			switch name {
			case "model/time":
				rows = sel.Rows(GroupModel, SortTime, true)
			case "agent/tokens":
				rows = sel.Rows(GroupAgent, SortTokens, true)
			default:
				rows = sel.Rows(GroupModel, SortName, false)
			}
			totals := sel.Totals()
			if totals.Tokens.Counts.Total() != want.Tokens.Total() {
				t.Fatalf("tokens %d != RunSummary %d", totals.Tokens.Counts.Total(), want.Tokens.Total())
			}
			if *totals.Money.EstimatedCostUSD < want.CostUSD-1e-9 || *totals.Money.EstimatedCostUSD > want.CostUSD+1e-9 {
				t.Fatalf("cost %v != RunSummary %v", *totals.Money.EstimatedCostUSD, want.CostUSD)
			}
			if totals.Tokens.Metered != want.Metered || totals.Money.Unpriced != want.Unpriced {
				t.Fatalf("counts %+v vs RunSummary metered=%d unpriced=%d", totals, want.Metered, want.Unpriced)
			}
			var sumTokens uint64
			var sumJobs int
			for _, r := range rows {
				if r.Agent != nil && r.Agent.OutsideRunTotal {
					continue
				}
				if r.Tokens.Counts != nil {
					sumTokens += r.Tokens.Counts.Total()
				}
				sumJobs += r.Jobs
			}
			if sumTokens != want.Tokens.Total() || sumJobs != totals.Jobs {
				t.Fatalf("rows hold %d tokens / %d jobs, totals %d / %d", sumTokens, sumJobs, want.Tokens.Total(), totals.Jobs)
			}
		})
	}
}

func TestAnalytics_FiltersAndSortsNeverChangeFacts(t *testing.T) {
	f := BuildFacts(anFullRun())
	base := f.Select(Filter{}, anAsOf()).Jobs(SortStarted, true)

	for _, sortBy := range []SortKey{SortTime, SortTokens, SortCost, SortName, SortModel, SortStarted} {
		for _, desc := range []bool{true, false} {
			got := f.Select(Filter{}, anAsOf()).Jobs(sortBy, desc)
			if len(got) != len(base) {
				t.Fatalf("sort %s desc=%v changed the job count", sortBy, desc)
			}
			for _, it := range got {
				if !reflect.DeepEqual(it, anJob(t, base, it.ID)) {
					t.Fatalf("sort %s desc=%v changed job %s", sortBy, desc, it.ID)
				}
			}
		}
	}
}

func TestAnalytics_FilterScopes(t *testing.T) {
	f := BuildFacts(anFullRun())
	asOf := anAsOf()
	ids := func(flt Filter) []string {
		items := f.Select(flt, asOf).Jobs(SortStarted, false)
		got := jobIDs(items)
		slices.Sort(got)
		return got
	}

	if got := ids(Filter{Scope: FilterStage, StageID: "build"}); !slices.Equal(got, []string{"j1", "j2", "j3", "j7"}) {
		t.Fatalf("stage build (helpers included) = %v", got)
	}
	if got := ids(Filter{Scope: FilterStage, StageID: "review"}); !slices.Equal(got, []string{"j4", "j5"}) {
		t.Fatalf("stage review = %v", got)
	}
	if got := ids(Filter{Scope: FilterRunWide}); !slices.Equal(got, []string{"j6"}) {
		t.Fatalf("run wide = %v", got)
	}
	if got := ids(Filter{Scope: FilterUnattributed}); len(got) != 0 {
		t.Fatalf("unattributed = %v", got)
	}
	if got := ids(Filter{Agent: "helper:t-1"}); !slices.Equal(got, []string{"j3"}) {
		t.Fatalf("agent = %v", got)
	}
	if got := ids(Filter{Model: "codex/gpt-5.6-sol"}); !slices.Equal(got, []string{"j3", "j4"}) {
		t.Fatalf("model = %v", got)
	}
	if got := ids(Filter{Model: UnknownModelKey}); !slices.Equal(got, []string{"j6", "j7"}) {
		t.Fatalf("unknown model = %v", got)
	}
	if got := ids(Filter{Scope: FilterStage, StageID: "build", Model: "codex/gpt-5.6-sol"}); !slices.Equal(got, []string{"j3"}) {
		t.Fatalf("combined = %v", got)
	}
	if got := ids(Filter{Agent: "nobody"}); len(got) != 0 {
		t.Fatalf("an unknown agent matches nothing, got %v", got)
	}
}

func TestAnalytics_SortsBothWaysWithUnknownLast(t *testing.T) {
	sel := BuildFacts(anFullRun()).Select(Filter{}, anAsOf())

	desc := jobIDs(sel.Jobs(SortTime, true))
	asc := jobIDs(sel.Jobs(SortTime, false))
	// j7 has no duration: last in both directions, never first.
	if desc[0] != "j2" || asc[0] != "j6" || desc[len(desc)-1] != "j7" || asc[len(asc)-1] != "j7" {
		t.Fatalf("time sort desc=%v asc=%v", desc, asc)
	}
	tokDesc := jobIDs(sel.Jobs(SortTokens, true))
	if tokDesc[0] != "j2" {
		t.Fatalf("tokens desc = %v", tokDesc)
	}
	// Unmetered and pending jobs have no token count: after every metered job.
	metered := map[string]bool{"j1": true, "j2": true, "j3": true, "j4": true}
	for i, id := range tokDesc {
		if !metered[id] && i < 4 {
			t.Fatalf("unknown tokens sorted before known ones: %v", tokDesc)
		}
	}
	costAsc := jobIDs(sel.Jobs(SortCost, false))
	if costAsc[0] != "j4" || costAsc[1] != "j1" || costAsc[2] != "j2" {
		t.Fatalf("cost asc = %v", costAsc)
	}
	started := jobIDs(sel.Jobs(SortStarted, true))
	if started[0] != "j6" || started[len(started)-1] != "j1" {
		t.Fatalf("started desc = %v", started)
	}
	// Equal keys keep a stable order: repeated calls agree.
	for i := 0; i < 5; i++ {
		if !slices.Equal(jobIDs(sel.Jobs(SortName, false)), jobIDs(sel.Jobs(SortName, false))) {
			t.Fatal("unstable order")
		}
	}
	rows := sel.Rows(GroupAgent, SortJobs, true)
	if rows[0].Key != "stage:build" {
		t.Fatalf("rows by jobs = %v", rowKeys(rows))
	}
}

func TestAnalytics_CompletenessIsJudgedIndependently(t *testing.T) {
	c := BuildFacts(anFullRun()).Select(Filter{}, anAsOf()).Completeness()

	if c.Time.Status != CompletenessPartial || c.Time.Known != 5 || c.Time.Unknown != 1 || !slices.Equal(c.Time.Reasons, []string{"interrupted_job"}) {
		t.Fatalf("time = %+v", c.Time)
	}
	if c.Tokens.Status != CompletenessPartial || c.Tokens.Known != 4 || c.Tokens.Unknown != 2 ||
		!slices.Equal(c.Tokens.Reasons, []string{"unmetered_job", "pending_usage"}) {
		t.Fatalf("tokens = %+v", c.Tokens)
	}
	if c.Price.Status != CompletenessPartial || c.Price.Known != 3 || c.Price.Unknown != 3 ||
		!slices.Equal(c.Price.Reasons, []string{"unmetered_job", "pending_usage", "unpriced_model"}) {
		t.Fatalf("price = %+v", c.Price)
	}

	// A fully measured stage has full time even though its price is incomplete.
	verify := BuildFacts(anFullRun()).Select(Filter{Agent: "verify:review#0"}, anAsOf()).Completeness()
	if verify.Time.Status != CompletenessFull || verify.Tokens.Status != CompletenessFull || verify.Price.Status != CompletenessFull {
		t.Fatalf("verify = %+v", verify)
	}
	helper := BuildFacts(anFullRun()).Select(Filter{Agent: "helper:t-1"}, anAsOf()).Completeness()
	if helper.Time.Status != CompletenessFull || helper.Tokens.Status != CompletenessFull || helper.Price.Status != CompletenessUnknown {
		t.Fatalf("helper = %+v", helper)
	}
}

func TestAnalytics_CompletenessOfAnEmptyRunIsProvenFull(t *testing.T) {
	in := AnalyticsInput{UsageState: UsageStateOK, Jobs: newAnJournal(anT0).snapshot(), Meta: anMeta()}
	sel := BuildFacts(in).Select(Filter{}, anAsOf())
	c := sel.Completeness()

	if c.Time.Status != CompletenessFull || c.Tokens.Status != CompletenessFull || c.Time.Known != 0 {
		t.Fatalf("a recorded run without jobs has proven zero: %+v", c)
	}
	if got := sel.Totals(); got.Time.DurationMS != nil || got.Tokens.Counts != nil || got.Money.EstimatedCostUSD != nil {
		t.Fatalf("no measurement is null, not zero: %+v", got)
	}
}

func TestAnalytics_UsageStoreStatesGateTokenCompleteness(t *testing.T) {
	for state, reason := range map[UsageState]string{UsageStateUnavailable: "usage_store_unavailable", UsageStateDisabled: "usage_disabled"} {
		in := anFullRun()
		in.UsageState = state
		in.Usage = nil
		f := BuildFacts(in)
		c := f.Select(Filter{}, anAsOf()).Completeness()
		if !slices.Contains(c.Tokens.Reasons, reason) || c.Tokens.Status == CompletenessFull {
			t.Fatalf("%s: tokens = %+v", state, c.Tokens)
		}
		if c.Time.Status != CompletenessPartial {
			t.Fatalf("%s: time must not depend on the usage store: %+v", state, c.Time)
		}
		if string(f.Health().Usage) != string(state) {
			t.Fatalf("health usage = %q", f.Health().Usage)
		}
	}
}

func TestAnalytics_JournalHealthShowsInCompleteness(t *testing.T) {
	cases := map[string]agentjobs.Health{
		"jobs_store_unavailable": {Status: agentjobs.HealthUnavailable, Reasons: []string{agentjobs.ReasonIOFailed}},
		"corrupt_line":           {Status: agentjobs.HealthDegraded, Reasons: []string{agentjobs.ReasonCorruptLine}},
	}
	for reason, health := range cases {
		in := anFullRun()
		in.Jobs.Health = health
		f := BuildFacts(in)
		c := f.Select(Filter{}, anAsOf()).Completeness()
		if !slices.Contains(c.Time.Reasons, reason) || c.Time.Status != CompletenessPartial {
			t.Fatalf("%s: %+v", reason, c.Time)
		}
		wantJobs := HealthOK
		if health.Status == agentjobs.HealthUnavailable {
			wantJobs = HealthUnavailable
		}
		if f.Health().Jobs != wantJobs {
			t.Fatalf("%s: health = %+v", reason, f.Health())
		}
	}
}

func TestAnalytics_StageStartedBeforeRecordingIsNotProven(t *testing.T) {
	in := anFullRun()
	in.Jobs.Header.RecordingStartedAt = anT0.Add(5 * time.Minute) // journal created on a run that was already going
	f := BuildFacts(in)

	build := f.Select(Filter{Scope: FilterStage, StageID: "build"}, anAsOf()).Completeness()
	if !slices.Contains(build.Time.Reasons, "started_before_recording") {
		t.Fatalf("build = %+v", build.Time)
	}
	// The review stage started at 400s: it is flagged when the journal began later, clean when earlier.
	in.Meta.Stages = []StageMeta{{ID: "build", Title: "Build", StartedAt: anT0}, {ID: "review", Title: "Review", StartedAt: anT0.Add(400 * time.Second)}}
	in.Jobs.Header.RecordingStartedAt = anT0.Add(7 * time.Minute)
	review := BuildFacts(in).Select(Filter{Scope: FilterStage, StageID: "review"}, anAsOf()).Completeness()
	if !slices.Contains(review.Time.Reasons, "started_before_recording") {
		t.Fatalf("review started at 400s, before 7m: %+v", review.Time)
	}
	in.Jobs.Header.RecordingStartedAt = anT0.Add(time.Minute)
	review = BuildFacts(in).Select(Filter{Scope: FilterStage, StageID: "review"}, anAsOf()).Completeness()
	if slices.Contains(review.Time.Reasons, "started_before_recording") {
		t.Fatalf("review reasons = %v", review.Time.Reasons)
	}
}

func TestAnalytics_RevisionIdentifiesAllThreeInputs(t *testing.T) {
	f := BuildFacts(anFullRun())
	if f.Revision() != "u7.j"+itoa(f.JobsRevision())+".m3" {
		t.Fatalf("revision = %q", f.Revision())
	}
	if f.UsageRevision() != 7 || f.MetaRevision() != 3 {
		t.Fatalf("revisions = %d %d", f.UsageRevision(), f.MetaRevision())
	}
}

func itoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var b []byte
	for ; v > 0; v /= 10 {
		b = append([]byte{byte('0' + v%10)}, b...)
	}
	return string(b)
}

func TestAnalytics_FactsAreImmutableViews(t *testing.T) {
	in := anFullRun()
	f := BuildFacts(in)
	before := f.Select(Filter{}, anAsOf()).Jobs(SortStarted, true)

	// Mutating the inputs afterwards (as a live writer would) must not reach into built facts.
	in.Usage[0].Tokens.Output = 999999
	in.Jobs.Records[0].Title = "mutated"
	in.Meta.Stages[0].Title = "mutated"
	after := f.Select(Filter{}, anAsOf()).Jobs(SortStarted, true)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("facts changed with their inputs")
	}
}

func TestAnalytics_JobTitleFallsBackToAgentAndOrder(t *testing.T) {
	in := anFullRun()
	jr := newAnJournal(anT0)
	jr.start(agentjobs.JobMeta{JobID: "n1", AgentID: "stage:build", AgentKind: AgentKindStage, StageID: "build", RootStageID: "build"}, anT0.Add(time.Minute), 60000)
	jr.start(agentjobs.JobMeta{JobID: "n2", AgentID: "stage:build", AgentKind: AgentKindStage, StageID: "build", RootStageID: "build"}, anT0.Add(2*time.Minute), 120000)
	in.Jobs = jr.snapshot()
	in.Usage = nil
	jobs := BuildFacts(in).Select(Filter{}, anAsOf()).Jobs(SortStarted, false)

	if jobs[0].Title != "Build · job 1" || jobs[1].Title != "Build · job 2" {
		t.Fatalf("titles = %q, %q", jobs[0].Title, jobs[1].Title)
	}
}

func TestAnalytics_JobWithoutAgentIDIsAttributedByItsFields(t *testing.T) {
	in := anFullRun()
	jr := newAnJournal(anT0)
	jr.start(agentjobs.JobMeta{JobID: "n1", StageID: "review", RootStageID: "review", Title: "x"}, anT0.Add(time.Minute), 60000)
	jr.start(agentjobs.JobMeta{JobID: "n2", Title: "y"}, anT0.Add(2*time.Minute), 120000)
	in.Jobs = jr.snapshot()
	in.Usage = nil
	jobs := BuildFacts(in).Select(Filter{}, anAsOf()).Jobs(SortStarted, false)

	if jobs[0].Agent.ID != "stage:review" || jobs[1].Agent.ID != UnattributedAgentID || jobs[1].Agent.Kind != AgentKindUnknown {
		t.Fatalf("agents = %+v / %+v", jobs[0].Agent, jobs[1].Agent)
	}
}

func TestAnalytics_TenJobsOfOneAgentAreOneRow(t *testing.T) {
	j := newAnJournal(anT0)
	var usage []UsageRecord
	for i := 0; i < 10; i++ {
		id := "w" + itoa(uint64(i))
		at := anT0.Add(time.Duration(i) * time.Minute)
		j.start(agentjobs.JobMeta{JobID: id, AgentID: "helper:t-9", AgentKind: AgentKindHelper, StageID: "build", RootStageID: "build", TaskID: "t-9", Title: id}, at, int64(i)*60000)
		j.finish(id, agentjobs.OutcomeOK, at.Add(time.Second), time.Second)
		usage = append(usage, anUsage(id, "build", "anthropic", "claude-opus-4-8", anTok(1, 0, 0, 1), 0.01))
	}
	rows := BuildFacts(AnalyticsInput{Usage: usage, UsageState: UsageStateOK, Jobs: j.snapshot(), Meta: anMeta()}).
		Select(Filter{}, anAsOf()).Rows(GroupAgent, SortName, false)

	if len(rows) != 1 || rows[0].Jobs != 10 || rows[0].Tokens.Metered != 10 {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestAnalytics_TwoHelpersWithTheSameNameAreTwoAgents(t *testing.T) {
	j := newAnJournal(anT0)
	for i, task := range []string{"t-1", "t-2"} {
		j.start(agentjobs.JobMeta{JobID: "h" + task, AgentID: "helper:" + task, AgentKind: AgentKindHelper, StageID: "build", RootStageID: "build", TaskID: task, ProfileID: "reviewer", Title: "Review"},
			anT0.Add(time.Duration(i)*time.Minute), 0)
	}
	in := AnalyticsInput{UsageState: UsageStateOK, Jobs: j.snapshot(), Meta: anMeta()}
	in.Meta.Agents = []AgentMeta{{ID: "helper:t-1", Kind: AgentKindHelper, Title: "Reviewer"}, {ID: "helper:t-2", Kind: AgentKindHelper, Title: "Reviewer"}}
	rows := BuildFacts(in).Select(Filter{}, anAsOf()).Rows(GroupAgent, SortName, false)

	if len(rows) != 2 || rows[0].Key == rows[1].Key || rows[0].Label != rows[1].Label {
		t.Fatalf("one profile and title must not merge two agents: %+v", rows)
	}
}

func TestAnalytics_NestedHelperUsageIsCountedOnce(t *testing.T) {
	usage := []UsageRecord{
		{RecordVersion: 1, StageID: "build", RootStageID: "build", TaskID: "root", Phase: "check", Channel: "anthropic", Model: "glm-5.3", Metered: true, Priced: true, EstimatedCostUSD: 0.2, Tokens: anTok(1000, 0, 0, 100)},
		{RecordVersion: 1, StageID: "build", RootStageID: "build", TaskID: "child", ParentTaskID: "root", Phase: "check", Channel: "anthropic", Model: "glm-5.3", Metered: true, Priced: true, EstimatedCostUSD: 0.1, Tokens: anTok(500, 0, 0, 50)},
	}
	sel := BuildFacts(AnalyticsInput{Usage: usage, UsageState: UsageStateOK, Jobs: absentJobs(), Meta: anMeta()}).Select(Filter{}, anAsOf())

	if got := sel.Totals().Tokens.Counts.Total(); got != 1100+550 {
		t.Fatalf("total = %d: a parent's own usage never includes its descendants'", got)
	}
	rows := sel.Rows(GroupAgent, SortName, false)
	if rowByKey(t, rows, "helper:root").Tokens.Counts.Total() != 1100 || rowByKey(t, rows, "helper:child").Tokens.Counts.Total() != 550 {
		t.Fatalf("rows = %+v", rows)
	}
	if got := sel.f.Select(Filter{Scope: FilterStage, StageID: "build"}, anAsOf()).Totals().Tokens.Counts.Total(); got != 1650 {
		t.Fatalf("stage total = %d", got)
	}
}

func TestAnalytics_ObservedModelReplacesTheConfiguredHint(t *testing.T) {
	j := newAnJournal(anT0)
	j.start(withHint(withTitle(anStageMeta("build", "", anT0), "h1", "x"), "anthropic", "opus-alias"), anT0, 0)
	j.finish("h1", agentjobs.OutcomeOK, anT0.Add(time.Second), time.Second)
	in := AnalyticsInput{
		Usage:      []UsageRecord{anUsage("h1", "build", "anthropic", "claude-opus-4-8", anTok(1, 0, 0, 1), 0.01)},
		UsageState: UsageStateOK, Jobs: j.snapshot(), Meta: anMeta(),
	}
	f := BuildFacts(in)
	job := f.Select(Filter{}, anAsOf()).Jobs(SortStarted, true)[0]

	if job.Model == nil || job.Model.Key != "anthropic/claude-opus-4-8" || job.ModelSource != ModelSourceObserved {
		t.Fatalf("job = %+v", job)
	}
	models := f.Catalog().Models
	if len(models) != 1 || models[0].ColorOrdinal != 1 {
		t.Fatalf("only the observed model is offered, and the hint's number stays taken: %+v", models)
	}
}

func TestAnalytics_ModelRowCountsItsAgentsInScope(t *testing.T) {
	f := BuildFacts(anFullRun())
	cases := []struct {
		name   string
		filter Filter
		want   map[string]int
	}{
		// gpt is used by the helper and by the verify agent; the Assistant's job has no model.
		{"all", Filter{}, map[string]int{"anthropic/claude-opus-4-8": 1, "codex/gpt-5.6-sol": 2, "anthropic/glm-5.3": 1, UnknownModelKey: 1}},
		{"review stage", Filter{Scope: FilterStage, StageID: "review"}, map[string]int{"codex/gpt-5.6-sol": 1, "anthropic/glm-5.3": 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows := f.Select(tc.filter, anAsOf()).Rows(GroupModel, SortName, false)
			got := map[string]int{}
			for _, r := range rows {
				got[r.Key] = r.Agents
			}
			if !maps.Equal(got, tc.want) {
				t.Errorf("agents per model row = %v, want %v", got, tc.want)
			}
		})
	}
	for _, r := range f.Select(Filter{}, anAsOf()).Rows(GroupAgent, SortName, false) {
		if r.Agents != 0 {
			t.Errorf("agent row %s carries an agent count %d", r.Key, r.Agents)
		}
	}
}

func TestAnalytics_ModelRowDoesNotCountTheAssistant(t *testing.T) {
	in := anFullRun()
	in.Usage = append(in.Usage, anUsage("j6", "", "codex", "gpt-5.6-sol", anTok(1, 0, 0, 1), 0.01))
	in.Usage = slices.DeleteFunc(in.Usage, func(r UsageRecord) bool { return r.JobID == "j6" && r.Model == "" })
	rows := BuildFacts(in).Select(Filter{}, anAsOf()).Rows(GroupModel, SortName, false)
	if got := rowByKey(t, rows, "codex/gpt-5.6-sol").Agents; got != 2 {
		t.Errorf("gpt agents = %d, want 2: the Assistant sits outside the run total", got)
	}
}

func TestAnalytics_RunCountsExcludeTheAssistant(t *testing.T) {
	counts := BuildFacts(anFullRun()).Counts()
	if counts.Models != 3 || counts.Agents != 4 {
		t.Errorf("counts = %+v, want 3 models and 4 agents (Assistant excluded)", counts)
	}
	if empty := BuildFacts(AnalyticsInput{}).Counts(); empty != (RunCounts{}) {
		t.Errorf("an empty run counts %+v, want zeros", empty)
	}
}

func TestAnalytics_RunCountsIgnoreFiltersAndAssistantOnlyModels(t *testing.T) {
	in := anFullRun()
	in.Usage = append(in.Usage, anUsage("j6", "", "other", "only-assistant-model", anTok(1, 0, 0, 1), 0.01))
	in.Usage = slices.DeleteFunc(in.Usage, func(r UsageRecord) bool { return r.JobID == "j6" && r.Model == "" })
	f := BuildFacts(in)
	if got := f.Counts(); got.Models != 3 {
		t.Errorf("models = %d, want 3: a model used only by the Assistant is not a run model", got.Models)
	}
	if got := len(f.Catalog().Models); got != 4 {
		t.Fatalf("catalog models = %d, want 4 (the catalog lists every model seen)", got)
	}
}

// Usage a run agent wrote before jobs existed belongs to the participant its task maps to, in either ID form a
// run may use, so the old tokens and a new job of the same task are one agent.
func TestAnalytics_LegacyUsageOfARunAgentJoinsItsParticipant(t *testing.T) {
	for _, agentID := range []string{"run:t-qa", "run:qa"} {
		t.Run(agentID, func(t *testing.T) {
			in := anFullRun()
			in.Meta.Agents = append(in.Meta.Agents, AgentMeta{ID: agentID, Kind: AgentKindRunAgent, Title: "QA", TaskID: "t-qa"})
			jr := newAnJournal(anT0)
			jr.records = slices.Clone(in.Jobs.Records)
			jr.start(agentjobs.JobMeta{JobID: "qa-new", AgentID: agentID, AgentKind: AgentKindRunAgent, TaskID: "t-qa", ProfileID: "qa", Title: "QA"},
				anT0.Add(9*time.Minute), 540000)
			jr.finish("qa-new", agentjobs.OutcomeOK, anT0.Add(10*time.Minute), time.Minute)
			in.Jobs = jr.snapshot()
			legacy := UsageRecord{RecordVersion: 1, TaskID: "t-qa", ProfileID: "qa", Phase: "helper", Channel: "anthropic", Model: "glm-5.3",
				Metered: true, Tokens: anTok(100, 0, 0, 10)}
			fresh := anUsage("qa-new", "", "anthropic", "glm-5.3", anTok(200, 0, 0, 20), 0)
			fresh.TaskID, fresh.ProfileID = "t-qa", "qa"
			in.Usage = append(in.Usage, legacy, fresh)
			f := BuildFacts(in)

			var qaRows []string
			for _, r := range f.Select(Filter{}, anAsOf()).Rows(GroupAgent, SortName, false) {
				if strings.Contains(r.Key, "qa") {
					qaRows = append(qaRows, r.Key)
				}
			}
			if !slices.Equal(qaRows, []string{agentID}) {
				t.Fatalf("agent rows of task t-qa = %v, want only %s", qaRows, agentID)
			}
			got := f.Select(Filter{Agent: agentID}, anAsOf()).Totals()
			if got.Jobs != 1 || got.Tokens.Metered != 2 || got.Tokens.Counts == nil || got.Tokens.Counts.Total() != 330 {
				t.Fatalf("%s totals = %+v, want its job and both usage records", agentID, got)
			}
		})
	}
}

// An earlier job that ends with a model nobody has used yet takes the next free ordinal: the model of a job
// that started later and is already on screen keeps its colour.
func TestAnalytics_ObservedModelOfAnEarlierJobTakesTheNextOrdinal(t *testing.T) {
	jr := newAnJournal(anT0)
	jr.start(withHint(withTitle(anStageMeta("build", "", anT0), "early", "early"), "anthropic", "hint-h"), anT0.Add(time.Minute), 60000)
	jr.start(withHint(withTitle(anStageMeta("review", "", anT0), "later", "later"), "anthropic", "hint-m"), anT0.Add(2*time.Minute), 120000)
	in := AnalyticsInput{UsageState: UsageStateOK, Jobs: jr.snapshot(), Meta: anMeta()}
	ordinals := func(in AnalyticsInput) map[string]int {
		out := map[string]int{}
		for _, m := range BuildFacts(in).Catalog().Models {
			out[m.Key] = m.ColorOrdinal
		}
		return out
	}
	if got := ordinals(in); got["anthropic/hint-h"] != 0 || got["anthropic/hint-m"] != 1 {
		t.Fatalf("ordinals while both run = %v", got)
	}

	jr.finish("early", agentjobs.OutcomeOK, anT0.Add(3*time.Minute), 2*time.Minute)
	in.Jobs = jr.snapshot()
	x := anUsage("early", "build", "anthropic", "observed-x", anTok(1, 0, 0, 1), 0)
	x.StartedAt, x.FinishedAt = anT0.Add(3*time.Minute).Format(time.RFC3339), anT0.Add(3*time.Minute).Format(time.RFC3339)
	in.Usage = []UsageRecord{x}
	// The replaced hint keeps its 0 reserved; the catalog lists only models that are still in use.
	want := map[string]int{"anthropic/hint-m": 1, "anthropic/observed-x": 2}
	for i := range 2 { // the second build is the rebuild of the next poll
		if got := ordinals(in); !maps.Equal(got, want) {
			t.Fatalf("build %d: ordinals = %v, want %v", i+1, got, want)
		}
	}
}

// A stage that ran before its run was recorded (migrated from state.json) has no first start the read model
// can compare with the journal, yet its earlier work is unknown: its time is never a proven zero.
func TestAnalytics_StageThatRanBeforeTheRunWasRecordedIsNotProven(t *testing.T) {
	in := anFullRun()
	in.Meta.Stages = append(in.Meta.Stages, StageMeta{ID: "old", Title: "Old", StartUnknown: true})
	f := BuildFacts(in)
	c := f.Select(Filter{Scope: FilterStage, StageID: "old"}, anAsOf()).Completeness()
	if c.Time.Status == CompletenessFull || !slices.Contains(c.Time.Reasons, agentjobs.GapStartedBeforeRecording) {
		t.Fatalf("time completeness = %+v, want unproven with started_before_recording", c.Time)
	}
	if all := f.Select(Filter{}, anAsOf()).Completeness(); !slices.Contains(all.Time.Reasons, agentjobs.GapStartedBeforeRecording) {
		t.Fatalf("run time completeness = %+v, want started_before_recording", all.Time)
	}
}
