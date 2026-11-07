package accounting

import (
	"fmt"
	"maps"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/akopichin/afm/contracts/agentjobs"
)

type cacheFixture struct {
	usage *Store
	jobs  *agentjobs.Store
	meta  AnalyticsMeta
	mu    sync.Mutex
	a     *Analytics
}

func newCacheFixture(t *testing.T) *cacheFixture {
	t.Helper()
	dir := t.TempDir()
	usage, err := Open(dir, NewResolver(PricingConfig{}))
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := agentjobs.Open(dir, agentjobs.Header{RecordingStartedAt: anT0})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = usage.Close(); _ = jobs.Close() })
	fx := &cacheFixture{usage: usage, jobs: jobs, meta: anMeta()}
	fx.a = NewAnalytics(AnalyticsSources{Usage: usage, Jobs: jobs, Meta: func() AnalyticsMeta {
		fx.mu.Lock()
		defer fx.mu.Unlock()
		return fx.meta
	}})
	return fx
}

func (fx *cacheFixture) setMetaRevision(rev uint64) {
	fx.mu.Lock()
	defer fx.mu.Unlock()
	fx.meta.Revision = rev
}

func (fx *cacheFixture) appendJob(id string) error {
	meta := agentjobs.JobMeta{JobID: id, AgentID: "stage:build", AgentKind: AgentKindStage, StageID: "build", RootStageID: "build", Title: id}
	return fx.jobs.Append(agentjobs.Record{Type: agentjobs.TypeStarted, JobMeta: meta, At: anT0.Add(time.Minute)})
}

func (fx *cacheFixture) addJob(t *testing.T, id string) {
	t.Helper()
	if err := fx.appendJob(id); err != nil {
		t.Fatal(err)
	}
}

func TestAnalytics_PollWithUnchangedRevisionsDoesNotRebuild(t *testing.T) {
	fx := newCacheFixture(t)
	fx.addJob(t, "a")

	first := fx.a.Facts()
	for i := 0; i < 50; i++ {
		if fx.a.Facts() != first {
			t.Fatal("unchanged revisions must return the cached facts")
		}
	}
	if got := fx.a.builds.Load(); got != 1 {
		t.Fatalf("builds = %d, want 1", got)
	}
}

func TestAnalytics_InvalidatesOnEveryInputChange(t *testing.T) {
	fx := newCacheFixture(t)
	prev := fx.a.Facts()
	changed := func(what string) {
		t.Helper()
		next := fx.a.Facts()
		if next == prev {
			t.Fatalf("%s did not invalidate the cache", what)
		}
		prev = next
	}

	if err := fx.usage.Append(Observation{Metered: true, Channel: "anthropic", Model: "m", Schema: SchemaAnthropic, Tokens: anTok(1, 0, 0, 1)}, "build", "implementation", ""); err != nil {
		t.Fatal(err)
	}
	changed("usage append")

	fx.addJob(t, "a")
	changed("jobs append")

	fx.setMetaRevision(99)
	changed("metadata revision")

	// A journal that cannot take a fact (conflict) changes health without adding a record.
	meta := agentjobs.JobMeta{JobID: "a", AgentID: "stage:build", StageID: "build", Title: "other title"}
	if err := fx.jobs.Append(agentjobs.Record{Type: agentjobs.TypeStarted, JobMeta: meta, At: anT0.Add(time.Minute)}); err == nil {
		t.Fatal("expected a conflict")
	}
	changed("jobs health")
	if prev.Health().Jobs != HealthOK {
		t.Fatalf("a degraded journal is still readable: %+v", prev.Health())
	}

	_ = fx.usage.Close() // Append after Close fails to write and latches the store
	_ = fx.usage.Append(Observation{Metered: true, Tokens: anTok(1, 0, 0, 1)}, "build", "x", "")
	changed("usage store latch")
	if prev.Health().Usage != HealthUnavailable {
		t.Fatalf("health = %+v", prev.Health())
	}
}

func TestAnalytics_BuildDoesNotHoldTheWriterLock(t *testing.T) {
	fx := newCacheFixture(t)
	fx.addJob(t, "a")
	fx.a.beforeAggregate = func() {
		done := make(chan struct{})
		go func() {
			defer close(done)
			if err := fx.usage.Append(Observation{Metered: true, Channel: "anthropic", Model: "m", Schema: SchemaAnthropic, Tokens: anTok(1, 0, 0, 1)}, "build", "implementation", ""); err != nil {
				t.Error(err)
			}
			if err := fx.appendJob("b"); err != nil {
				t.Error(err)
			}
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("a writer was blocked while analytics aggregated")
		}
	}
	f := fx.a.Facts()
	fx.a.beforeAggregate = nil

	// The facts are an immutable snapshot taken before the writes; the next poll sees them.
	if got := f.Select(Filter{}, anAsOf()).Totals().Jobs; got != 1 {
		t.Fatalf("jobs in the built facts = %d, want 1", got)
	}
	if got := fx.a.Facts().Select(Filter{}, anAsOf()).Totals().Jobs; got != 2 {
		t.Fatalf("jobs after the writers finished = %d, want 2", got)
	}
}

func TestAnalytics_MissingStoresMeanDisabledAndAbsent(t *testing.T) {
	a := NewAnalytics(AnalyticsSources{Meta: func() AnalyticsMeta { return anMeta() }})
	f := a.Facts()

	if f.Health().Usage != HealthDisabled || f.Health().Jobs != HealthAbsent {
		t.Fatalf("health = %+v", f.Health())
	}
	if a.Facts() != f {
		t.Fatal("a static source is cached too")
	}
}

// A ledger that could not be opened is a failure, not a choice: health says unavailable, the time is still reported.
func TestAnalytics_UsageOpenFailureIsUnavailableNotDisabled(t *testing.T) {
	cases := []struct {
		name       string
		src        AnalyticsSources
		wantHealth Health
		wantReason string
	}{
		{"open failed", AnalyticsSources{UsageOpenFailed: true}, HealthUnavailable, "usage_store_unavailable"},
		{"accounting off", AnalyticsSources{}, HealthDisabled, "usage_disabled"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			j := newAnJournal(anT0)
			j.start(agentjobs.JobMeta{JobID: "j1", AgentID: "stage:build", AgentKind: AgentKindStage, StageID: "build", RootStageID: "build"}, anT0, 0)
			j.finish("j1", agentjobs.OutcomeOK, anT0.Add(time.Second), time.Second)
			tc.src.Jobs = frozenJobsSource{j.snapshot()}
			tc.src.Meta = func() AnalyticsMeta { return anMeta() }

			f := NewAnalytics(tc.src).Facts()
			c := f.Select(Filter{}, anAsOf()).Completeness()

			if f.Health().Usage != tc.wantHealth || !slices.Contains(c.Tokens.Reasons, tc.wantReason) {
				t.Fatalf("health usage = %q, token reasons = %v, want %q with %q", f.Health().Usage, c.Tokens.Reasons, tc.wantHealth, tc.wantReason)
			}
			if got := f.Select(Filter{}, anAsOf()).Totals().Time.DurationMS; got == nil || *got != 1000 {
				t.Fatalf("time must stay reported without the usage ledger: %v", got)
			}
		})
	}
}

type frozenJobsSource struct{ snap agentjobs.Snapshot }

func (s frozenJobsSource) Snapshot() agentjobs.Snapshot { return s.snap }

func TestAnalytics_ScaleThousandsOfJobsAndDozensOfModels(t *testing.T) {
	const jobs, models = 4000, 40
	j := newAnJournal(anT0)
	var usage []UsageRecord
	var wantTokens uint64
	for i := 0; i < jobs; i++ {
		id := fmt.Sprintf("job-%d", i)
		stage := fmt.Sprintf("stage-%d", i%8)
		at := anT0.Add(time.Duration(i) * time.Second)
		j.start(agentjobs.JobMeta{JobID: id, AgentID: "stage:" + stage, AgentKind: AgentKindStage, StageID: stage, RootStageID: stage, Title: id}, at, int64(i)*1000)
		j.finish(id, agentjobs.OutcomeOK, at.Add(time.Second), time.Second)
		usage = append(usage, anUsage(id, stage, "anthropic", fmt.Sprintf("model-%d", i%models), anTok(10, 20, 0, 5), 0.01))
		wantTokens += 35
	}
	in := AnalyticsInput{Usage: usage, UsageRevision: 1, UsageState: UsageStateOK, Jobs: j.snapshot(), Meta: AnalyticsMeta{Revision: 1, LiveSince: anT0}}

	f := BuildFacts(in)
	sel := f.Select(Filter{}, anT0.Add(time.Hour*24))
	totals := sel.Totals()
	if totals.Jobs != jobs || totals.Tokens.Counts.Total() != wantTokens {
		t.Fatalf("totals = %+v", totals)
	}
	rows := sel.Rows(GroupModel, SortTokens, true)
	if len(rows) != models {
		t.Fatalf("model rows = %d", len(rows))
	}
	if got := len(f.Catalog().Models); got != models {
		t.Fatalf("catalog models = %d", got)
	}
	if tl := sel.Timeline(2000); tl.Total != jobs || len(tl.Intervals) != 2000 || !tl.Truncated {
		t.Fatalf("timeline = total %d, shown %d, truncated %v", tl.Total, len(tl.Intervals), tl.Truncated)
	}
	if got := len(sel.Jobs(SortStarted, true)); got != jobs {
		t.Fatalf("jobs = %d", got)
	}
}

func TestAnalytics_AssistantSourcesJoinTheFactsOutsideTheRunTotal(t *testing.T) {
	fx := newCacheFixture(t)
	fx.addJob(t, "run-job")

	dir := t.TempDir()
	sideUsage, err := Open(dir, NewResolver(PricingConfig{}))
	if err != nil {
		t.Fatal(err)
	}
	sideJobs, err := agentjobs.Open(dir, agentjobs.Header{RecordingStartedAt: anT0})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sideUsage.Close(); _ = sideJobs.Close() })
	a := NewAnalytics(AnalyticsSources{
		Usage: fx.usage, Jobs: fx.jobs, Meta: func() AnalyticsMeta { return anMeta() },
		Assistant: &AssistantSources{Usage: sideUsage, Jobs: sideJobs},
	})

	before := a.Facts()
	meta := agentjobs.JobMeta{JobID: "side-job", AgentID: "side:r1", AgentKind: AgentKindAssistant, Title: "Assistant · job 1"}
	if err := sideJobs.Append(agentjobs.Record{Type: agentjobs.TypeStarted, JobMeta: meta, At: anT0.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := sideJobs.Append(agentjobs.Record{Type: agentjobs.TypeFinished, JobMeta: meta, At: anT0.Add(2 * time.Minute), Outcome: agentjobs.OutcomeOK, DurationMS: 60_000}); err != nil {
		t.Fatal(err)
	}
	after := a.Facts()

	if after == before {
		t.Fatal("an Assistant journal change must invalidate the cache")
	}
	sel := after.Select(Filter{}, anAsOf())
	if got := sel.Totals().Jobs; got != 1 {
		t.Fatalf("run total jobs = %d, want 1 (the Assistant is outside it)", got)
	}
	if sel.Outside() == nil || sel.Outside().Jobs != 1 {
		t.Fatalf("outside = %+v, want the Assistant job", sel.Outside())
	}
	if after.Health().Jobs != HealthOK {
		t.Fatalf("health = %+v", after.Health())
	}
	if after.Revision() == before.Revision() {
		t.Fatal("the facts revision must move with the Assistant journal")
	}
	if a.Facts() != after {
		t.Fatal("unchanged inputs must hit the cache")
	}
}

// openSideStores opens the Assistant's own ledger and journal in a directory of their own.
func openSideStores(t *testing.T) (*Store, *agentjobs.Store) {
	t.Helper()
	dir := t.TempDir()
	usage, err := Open(dir, NewResolver(PricingConfig{}))
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := agentjobs.Open(dir, agentjobs.Header{RecordingStartedAt: anT0})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = usage.Close(); _ = jobs.Close() })
	return usage, jobs
}

// Assistant usage stays outside the run total even when its job is not on the Assistant's journal: a legacy
// record without a job ID, or one whose started line was lost with a failed journal. It is recorded under the
// stage ID "side" like in production, and that must not make it a stage of the run.
func TestAnalytics_AssistantUsageWithoutItsJobStaysOutsideTheRunTotal(t *testing.T) {
	fx := newCacheFixture(t)
	opus := func(in, out uint64, jobID string) Observation {
		return Observation{Metered: true, Channel: "anthropic", Model: "claude-opus-4-8", Schema: SchemaAnthropic,
			ModelSource: ModelSourceObserved, Tokens: anTok(in, 0, 0, out), JobID: jobID}
	}
	if err := fx.usage.Append(opus(6, 4, ""), "build", "implementation", ""); err != nil {
		t.Fatal(err)
	}
	sideUsage, sideJobs := openSideStores(t)
	if err := sideUsage.Append(opus(40, 10, ""), "side", "side", ""); err != nil { // legacy: no job ID
		t.Fatal(err)
	}
	if err := sideUsage.Append(opus(40, 10, "job-lost"), "side", "side", ""); err != nil { // its started line is lost
		t.Fatal(err)
	}
	a := NewAnalytics(AnalyticsSources{
		Usage: fx.usage, Jobs: fx.jobs, Meta: func() AnalyticsMeta { return anMeta() },
		Assistant: &AssistantSources{Usage: sideUsage, Jobs: sideJobs},
	})
	f := a.Facts()
	sel := f.Select(Filter{}, anAsOf())

	want := fx.usage.Snapshot().RunSummary()
	totals := sel.Totals()
	if totals.Tokens.Counts == nil || totals.Tokens.Counts.Total() != want.Tokens.Total() || totals.Tokens.Metered != 1 {
		t.Fatalf("run total tokens = %+v, want the run ledger's %d only", totals.Tokens, want.Tokens.Total())
	}
	if totals.Money.EstimatedCostUSD == nil || !closeTo(*totals.Money.EstimatedCostUSD, want.CostUSD) {
		t.Fatalf("run total money = %+v, want the run ledger's %v", totals.Money, want.CostUSD)
	}
	outside := sel.Outside()
	if outside == nil || outside.Tokens.Metered != 2 || outside.Tokens.Counts.Total() != 100 {
		t.Fatalf("outside = %+v, want both Assistant records", outside)
	}
	side := rowByKey(t, sel.Rows(GroupAgent, SortTokens, true), "side:run-1")
	if side.Agent == nil || !side.Agent.OutsideRunTotal || side.Agent.Kind != AgentKindAssistant || side.Tokens.Metered != 2 {
		t.Fatalf("Assistant row = %+v", side)
	}
	for _, s := range f.Catalog().Stages {
		if s.ID == "side" {
			t.Fatalf("the Assistant's records made %q a stage: %+v", s.ID, f.Catalog().Stages)
		}
	}
	if stage := f.Select(Filter{Scope: FilterStage, StageID: "side"}, anAsOf()); len(stage.Rows(GroupAgent, SortName, false)) != 0 {
		t.Fatalf("stage filter side = %+v, want nothing", stage.Rows(GroupAgent, SortName, false))
	}
}

// A model's colour ordinal is fixed by when it was first recorded: a model that an earlier job reports on its
// end comes after every model already shown, and reopening the stores gives the same numbers.
func TestAnalytics_ColorOrdinalsSurviveAReload(t *testing.T) {
	dir := t.TempDir()
	open := func() (*Store, *agentjobs.Store) {
		usage, err := Open(dir, NewResolver(PricingConfig{}))
		if err != nil {
			t.Fatal(err)
		}
		jobs, err := agentjobs.Open(dir, agentjobs.Header{RecordingStartedAt: anT0})
		if err != nil {
			t.Fatal(err)
		}
		return usage, jobs
	}
	ordinals := func(usage *Store, jobs *agentjobs.Store) map[string]int {
		out := map[string]int{}
		f := NewAnalytics(AnalyticsSources{Usage: usage, Jobs: jobs, Meta: func() AnalyticsMeta { return anMeta() }}).Facts()
		for _, m := range f.Catalog().Models {
			out[m.Key] = m.ColorOrdinal
		}
		return out
	}
	usage, jobs := open()
	early := withHint(withTitle(anStageMeta("build", "", anT0), "early", "early"), "anthropic", "hint-h")
	later := withHint(withTitle(anStageMeta("review", "", anT0), "later", "later"), "anthropic", "hint-m")
	for _, rec := range []agentjobs.Record{
		{Type: agentjobs.TypeStarted, JobMeta: early, At: anT0.Add(time.Minute)},
		{Type: agentjobs.TypeStarted, JobMeta: later, At: anT0.Add(2 * time.Minute)},
	} {
		if err := jobs.Append(rec); err != nil {
			t.Fatal(err)
		}
	}
	shown := ordinals(usage, jobs)
	if shown["anthropic/hint-h"] != 0 || shown["anthropic/hint-m"] != 1 {
		t.Fatalf("ordinals while both run = %v", shown)
	}

	// The early job ends and reports the model it really used.
	obs := Observation{Metered: true, Channel: "anthropic", Model: "observed-x", Schema: SchemaAnthropic,
		ModelSource: ModelSourceObserved, Tokens: anTok(1, 0, 0, 1), JobID: "early"}
	if err := usage.Append(obs, "build", "implementation", ""); err != nil {
		t.Fatal(err)
	}
	end := agentjobs.Record{Type: agentjobs.TypeFinished, JobMeta: agentjobs.JobMeta{JobID: "early"}, At: anT0.Add(3 * time.Minute),
		Outcome: agentjobs.OutcomeOK, DurationMS: 120_000}
	if err := jobs.Append(end); err != nil {
		t.Fatal(err)
	}
	// The replaced hint keeps its 0 reserved; the catalog lists only models that are still in use.
	want := map[string]int{"anthropic/hint-m": 1, "anthropic/observed-x": 2}
	if got := ordinals(usage, jobs); !maps.Equal(got, want) {
		t.Fatalf("ordinals after the early job ended = %v, want %v", got, want)
	}
	_ = usage.Close()
	_ = jobs.Close()

	usage, jobs = open()
	t.Cleanup(func() { _ = usage.Close(); _ = jobs.Close() })
	if got := ordinals(usage, jobs); !maps.Equal(got, want) {
		t.Fatalf("ordinals after a reload = %v, want %v", got, want)
	}
}
