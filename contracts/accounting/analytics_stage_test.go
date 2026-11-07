package accounting

import (
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/akopichin/afm/contracts/agentjobs"
)

func TestStageSnapshot_AccumulatesAttemptsAndHelpersAndNothingElse(t *testing.T) {
	f := BuildFacts(anFullRun())
	at := anT0.Add(10 * time.Minute)
	got := f.StageSnapshot("build", at, false)

	// j1 and j2 are two attempts of the stage agent, j3 a helper of the same root stage, j7 died in a crash.
	if !got.Jobs.Known || got.Jobs.Count != 4 || got.Jobs.Active != 0 || got.Jobs.Measured != 3 || got.Jobs.UnknownTime != 1 {
		t.Fatalf("jobs = %+v", got.Jobs)
	}
	if got.Jobs.DurationMS != 510000 {
		t.Fatalf("duration = %d, want the sum of attempts and helpers", got.Jobs.DurationMS)
	}
	if got.Jobs.Complete || !slices.Contains(got.Jobs.Reasons, "interrupted_job") {
		t.Fatalf("a crashed job leaves the stage incomplete: %+v", got.Jobs)
	}
	if got.Usage.Tokens == nil || got.Usage.Tokens.Total() != 7000+26000+5000 {
		t.Fatalf("tokens = %+v", got.Usage.Tokens)
	}
	if got.Usage.Metered != 3 || got.Usage.Unmetered != 1 || got.Usage.Pending != 0 || got.Usage.Coverage != CoveragePartial {
		t.Fatalf("usage = %+v", got.Usage)
	}
	if !got.AsOf.Equal(at) || got.UsageRevision != 7 || got.JobsRevision != f.JobsRevision() {
		t.Fatalf("identity = %+v", got)
	}
}

func TestStageSnapshot_IsFrozenAtTheTransitionTime(t *testing.T) {
	f := BuildFacts(anFullRun())
	got := f.StageSnapshot("build", anT0.Add(250*time.Second), false)

	// j7 starts later and is out. j2 and j3 were still running at that moment, although the journal
	// already knows how they ended.
	if got.Jobs.Count != 3 || got.Jobs.Active != 2 || got.Jobs.Measured != 1 || got.Jobs.DurationMS != 120000 {
		t.Fatalf("jobs = %+v", got.Jobs)
	}
	if got.Jobs.Complete || !slices.Contains(got.Jobs.Reasons, "active_jobs") {
		t.Fatalf("active work makes the snapshot partial: %+v", got.Jobs)
	}
	if got.Usage.Tokens == nil || got.Usage.Tokens.Total() != 7000 || got.Usage.Metered != 1 || got.Usage.Pending != 2 {
		t.Fatalf("usage of unfinished jobs must not leak in: %+v", got.Usage)
	}
	if !slices.Contains(got.Usage.Reasons, "pending_usage") || got.Usage.Coverage != CoveragePartial {
		t.Fatalf("usage = %+v", got.Usage)
	}
}

func TestStageSnapshot_TakesOnlyItsOwnStage(t *testing.T) {
	got := BuildFacts(anFullRun()).StageSnapshot("review", anT0.Add(10*time.Minute), false)

	// j4 is review's verify step, j5 is still running; the Assistant and the build stage are not here.
	if got.Jobs.Count != 2 || got.Jobs.Active != 1 || got.Jobs.Measured != 1 || got.Jobs.DurationMS != 60000 {
		t.Fatalf("jobs = %+v", got.Jobs)
	}
	if got.Usage.Tokens == nil || got.Usage.Tokens.Total() != 2500 {
		t.Fatalf("tokens = %+v", got.Usage.Tokens)
	}
}

func TestStageSnapshot_ScriptOnlyStageIsAProvenZero(t *testing.T) {
	in := anFullRun()
	in.Meta.Stages = append(in.Meta.Stages, StageMeta{ID: "script", Title: "Script", StartedAt: anT0.Add(time.Minute)})
	got := BuildFacts(in).StageSnapshot("script", anT0.Add(2*time.Minute), false)

	if !got.Jobs.Known || got.Jobs.Count != 0 || got.Jobs.Active != 0 || got.Jobs.Measured != 0 || got.Jobs.DurationMS != 0 || !got.Jobs.Complete || len(got.Jobs.Reasons) != 0 {
		t.Fatalf("jobs = %+v", got.Jobs)
	}
	if got.Usage.Tokens == nil || got.Usage.Tokens.Total() != 0 || got.Usage.Coverage != CoverageFull || len(got.Usage.Reasons) != 0 {
		t.Fatalf("usage = %+v", got.Usage)
	}
}

func TestStageSnapshot_ZeroIsNotProvenWithoutAReliableJournal(t *testing.T) {
	cases := map[string]func(*AnalyticsInput){
		"legacy_journal": func(in *AnalyticsInput) { in.Jobs = absentJobs() },
		"started_before_recording": func(in *AnalyticsInput) {
			in.Jobs.Header.RecordingStartedAt = anT0.Add(30 * time.Minute)
		},
		"jobs_store_unavailable": func(in *AnalyticsInput) {
			in.Jobs.Health = agentjobs.Health{Status: agentjobs.HealthUnavailable, Reasons: []string{agentjobs.ReasonIOFailed}}
		},
	}
	for reason, mutate := range cases {
		t.Run(reason, func(t *testing.T) {
			in := anFullRun()
			in.Usage, in.Jobs = nil, newAnJournal(anT0).snapshot()
			in.Meta.Stages = append(in.Meta.Stages, StageMeta{ID: "script", Title: "Script", StartedAt: anT0.Add(time.Minute)})
			mutate(&in)
			got := BuildFacts(in).StageSnapshot("script", anT0.Add(2*time.Minute), false)

			if got.Jobs.Complete || !slices.Contains(got.Jobs.Reasons, reason) {
				t.Fatalf("jobs = %+v", got.Jobs)
			}
			if got.Usage.Tokens != nil || got.Usage.Coverage != CoverageNone || !slices.Contains(got.Usage.Reasons, reason) {
				t.Fatalf("an unproven zero must be unknown, not 0 tokens: %+v", got.Usage)
			}
			if reason != "started_before_recording" && got.Jobs.Known {
				t.Fatalf("an unreadable journal gives no counts: %+v", got.Jobs)
			}
		})
	}
}

func TestStageSnapshot_LegacyRunStillCarriesItsTokens(t *testing.T) {
	usage := []UsageRecord{
		{RecordVersion: 1, StageID: "build", Phase: "implementation", Channel: "anthropic", Model: "claude-opus-4-8", Metered: true, Priced: true, EstimatedCostUSD: 1, Tokens: anTok(3000, 0, 0, 1000)},
	}
	got := BuildFacts(AnalyticsInput{Usage: usage, UsageState: UsageStateOK, Jobs: absentJobs(), Meta: anMeta()}).
		StageSnapshot("build", anT0.Add(time.Hour), false)

	if got.Jobs.Known || !slices.Contains(got.Jobs.Reasons, "legacy_journal") {
		t.Fatalf("jobs = %+v", got.Jobs)
	}
	if got.Usage.Tokens == nil || got.Usage.Tokens.Total() != 4000 || got.Usage.Metered != 1 || got.Usage.Coverage != CoverageFull {
		t.Fatalf("legacy usage keeps its attribution: %+v", got.Usage)
	}
}

func TestStageSnapshot_UsageStoreStates(t *testing.T) {
	for state, reason := range map[UsageState]string{UsageStateUnavailable: "usage_store_unavailable", UsageStateDisabled: "usage_disabled"} {
		in := anFullRun()
		in.UsageState, in.Usage = state, nil
		got := BuildFacts(in).StageSnapshot("build", anT0.Add(10*time.Minute), false)

		if got.Usage.Health != HealthUnavailable || got.Usage.Tokens != nil || !slices.Contains(got.Usage.Reasons, reason) {
			t.Fatalf("%s: usage = %+v", state, got.Usage)
		}
		if got.Jobs.Count != 4 {
			t.Fatalf("%s: time facts do not depend on usage: %+v", state, got.Jobs)
		}
	}
}

func TestStageSnapshot_PendingStartMakesItPartial(t *testing.T) {
	in := anFullRun()
	f := BuildFacts(in)
	got := f.StageSnapshot("review", anT0.Add(10*time.Minute), true)
	if got.Jobs.Complete || !slices.Contains(got.Jobs.Reasons, "pending_start") {
		t.Fatalf("jobs = %+v", got.Jobs)
	}
	// The known subtotal stays, but a job that has not started yet may still add tokens.
	if got.Usage.Tokens == nil || got.Usage.Tokens.Total() != 2500 || got.Usage.Coverage == CoverageFull || !slices.Contains(got.Usage.Reasons, "pending_start") {
		t.Fatalf("usage = %+v, want the known 2500 tokens without full coverage", got.Usage)
	}
	script := f.StageSnapshot("verify-only", anT0.Add(10*time.Minute), true)
	if script.Jobs.Complete || !slices.Contains(script.Jobs.Reasons, "pending_start") {
		t.Fatalf("an unknown stage with pending work is partial too: %+v", script.Jobs)
	}
	if script.Usage.Coverage == CoverageFull || !slices.Contains(script.Usage.Reasons, "pending_start") {
		t.Fatalf("zero tokens with a pending start are not proven: %+v", script.Usage)
	}
}

// The jobs of a snapshot are frozen before its usage is read: usage of a job that is not on that journal (a
// Retry that published its job and usage in between) stays out. Only legacy usage without a job ID is taken
// by its fields.
func TestStageSnapshot_UsageOfAJobNotOnTheFrozenJournalStaysOut(t *testing.T) {
	in := anFullRun()
	at := anT0.Add(250 * time.Second)
	before := BuildFacts(in).StageSnapshot("build", at, false)

	retry := anUsage("job-retry", "build", "anthropic", "claude-opus-4-8", anTok(17, 0, 0, 3), 0)
	in.Usage = append(in.Usage, retry)
	after := BuildFacts(in).StageSnapshot("build", at, false)
	if after.Usage.Tokens == nil || after.Usage.Tokens.Total() != before.Usage.Tokens.Total() || after.Usage.Metered != before.Usage.Metered {
		t.Fatalf("usage = %+v, want the frozen %+v", after.Usage, before.Usage)
	}

	legacy := retry
	legacy.JobID = ""
	in.Usage = append(in.Usage, legacy)
	withLegacy := BuildFacts(in).StageSnapshot("build", at, false)
	if withLegacy.Usage.Tokens.Total() != before.Usage.Tokens.Total()+20 {
		t.Fatalf("legacy usage without a job ID = %+v, want it counted by its fields", withLegacy.Usage)
	}
}

// A stage migrated from state.json ran before anything recorded it: zero recorded jobs and tokens are not a
// proven zero, whatever its first start looks like now.
func TestStageSnapshot_StageThatRanBeforeTheRunWasRecordedIsNotProven(t *testing.T) {
	in := anFullRun()
	in.Meta.Stages = append(in.Meta.Stages, StageMeta{ID: "old", Title: "Old", StartUnknown: true})
	got := BuildFacts(in).StageSnapshot("old", anT0.Add(10*time.Minute), false)

	if got.Jobs.Complete || !slices.Contains(got.Jobs.Reasons, agentjobs.GapStartedBeforeRecording) {
		t.Fatalf("jobs = %+v, want unproven with started_before_recording", got.Jobs)
	}
	if got.Usage.Tokens != nil || got.Usage.Coverage != CoverageNone || !slices.Contains(got.Usage.Reasons, agentjobs.GapStartedBeforeRecording) {
		t.Fatalf("usage = %+v, want unknown tokens with started_before_recording", got.Usage)
	}
}

func TestStageSnapshot_FactsAfterTheTransitionDoNotChangeIt(t *testing.T) {
	in := anFullRun()
	at := anT0.Add(250 * time.Second)
	first := BuildFacts(in).StageSnapshot("build", at, false)

	// New work starts and finishes after the transition and j2 has long ended: a rebuild at the same
	// instant shows the same picture, apart from the revisions that identify the facts.
	jr := newAnJournal(anT0)
	jr.records = slices.Clone(in.Jobs.Records)
	jr.start(withTitle(anStageMeta("build", "", anT0), "late", "late"), anT0.Add(260*time.Second), 260000)
	jr.finish("late", agentjobs.OutcomeOK, anT0.Add(270*time.Second), 10*time.Second)
	in.Jobs = jr.snapshot()
	in.Usage = append(in.Usage, anUsage("late", "build", "anthropic", "claude-opus-4-8", anTok(1, 0, 0, 1), 0))
	second := BuildFacts(in).StageSnapshot("build", at, false)

	if second.JobsRevision == first.JobsRevision {
		t.Fatal("the rebuilt facts must carry their own revision")
	}
	first.JobsRevision, second.JobsRevision = 0, 0
	first.UsageRevision, second.UsageRevision = 0, 0
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
}

func TestStageSnapshot_SameFactsGiveEqualSnapshots(t *testing.T) {
	f := BuildFacts(anFullRun())
	at := anT0.Add(10 * time.Minute)
	a, b := f.StageSnapshot("build", at, false), f.StageSnapshot("build", at, false)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("snapshots differ: %+v vs %+v", a, b)
	}
	a.Jobs.Reasons = append(a.Jobs.Reasons, "mutated")
	if reflect.DeepEqual(a, f.StageSnapshot("build", at, false)) {
		t.Fatal("a snapshot must not share its slices with later ones")
	}
}

func TestStageSnapshot_CarriesNoMoney(t *testing.T) {
	typ := reflect.TypeOf(StageSnapshot{})
	var walk func(reflect.Type)
	walk = func(rt reflect.Type) {
		for rt.Kind() == reflect.Pointer || rt.Kind() == reflect.Slice {
			rt = rt.Elem()
		}
		if rt.Kind() != reflect.Struct {
			return
		}
		for i := 0; i < rt.NumField(); i++ {
			name := rt.Field(i).Name
			for _, banned := range []string{"Cost", "Money", "USD", "Price", "Rate"} {
				if len(name) >= len(banned) && containsFold(name, banned) {
					t.Fatalf("%s.%s looks like money; hooks never carry prices", rt.Name(), name)
				}
			}
			walk(rt.Field(i).Type)
		}
	}
	walk(typ)
}

func containsFold(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
