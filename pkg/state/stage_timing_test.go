package state

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/clock"
)

// timingRig drives a real Store on a fake clock, so every bucket of the stage-time projection is an exact
// duration and no test sleeps.
type timingRig struct {
	t      *testing.T
	dir    string
	stages []string
	clock  *clock.FakeClock
	store  *Store
}

func newTimingRig(t *testing.T, stages ...string) *timingRig {
	t.Helper()
	return newTimingRigAt(t, time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC), stages...)
}

func newTimingRigAt(t *testing.T, start time.Time, stages ...string) *timingRig {
	t.Helper()
	fc := clock.NewFake(start)
	withFakeClock(t, fc)
	r := &timingRig{t: t, dir: t.TempDir(), stages: stages, clock: fc}
	r.open()
	return r
}

func (r *timingRig) open() {
	r.t.Helper()
	s, err := Open(r.dir, r.stages)
	if err != nil {
		r.t.Fatalf("Open: %v", err)
	}
	r.t.Cleanup(func() { _ = s.Close() })
	r.store = s
}

// reopen closes the store and replays the journal into a new one, the way a restarted run does.
func (r *timingRig) reopen() {
	r.t.Helper()
	if err := r.store.Close(); err != nil {
		r.t.Fatalf("Close: %v", err)
	}
	r.open()
}

func (r *timingRig) wait(d time.Duration) { r.clock.Advance(d) }

func (r *timingRig) begin() {
	r.t.Helper()
	if err := r.store.BeginRun(); err != nil {
		r.t.Fatalf("BeginRun: %v", err)
	}
}

func (r *timingRig) end(status RunStatus) {
	r.t.Helper()
	if err := r.store.EndRun(status); err != nil {
		r.t.Fatalf("EndRun(%s): %v", status, err)
	}
}

func (r *timingRig) move(stage string, from, to StageStatus) {
	r.t.Helper()
	if err := r.store.Apply(&Transition{StageID: stage, From: from, To: to, Event: "test"}); err != nil {
		r.t.Fatalf("Apply(%s %s→%s): %v", stage, from, to, err)
	}
}

func (r *timingRig) control(c *Control) {
	r.t.Helper()
	if err := r.store.AppendControl(c); err != nil {
		r.t.Fatalf("AppendControl(%s): %v", c.Kind, err)
	}
}

func (r *timingRig) timing(stage string) StageTiming {
	return r.store.Snapshot().Stages[stage].Timing
}

// timingWant is the expected stage time, one duration per bucket plus the open interval's kind.
type timingWant struct {
	work, approval, helpers, backoff, scheduling, paused time.Duration
	open                                                 TimeKind
}

func (w timingWant) total() time.Duration {
	return w.work + w.approval + w.helpers + w.backoff + w.scheduling + w.paused
}

func assertTiming(t *testing.T, got StageTiming, want timingWant) {
	t.Helper()
	have := timingWant{
		work:       time.Duration(got.WorkMs) * time.Millisecond,
		approval:   time.Duration(got.ApprovalMs) * time.Millisecond,
		helpers:    time.Duration(got.HelpersMs) * time.Millisecond,
		backoff:    time.Duration(got.BackoffMs) * time.Millisecond,
		scheduling: time.Duration(got.SchedulingMs) * time.Millisecond,
		paused:     time.Duration(got.PausedMs) * time.Millisecond,
		open:       got.OpenKind,
	}
	if have != want {
		t.Fatalf("stage time = %+v, want %+v", have, want)
	}
	if (got.OpenKind == TimeNone) != got.OpenSince.IsZero() {
		t.Fatalf("OpenKind %q and OpenSince %v disagree", got.OpenKind, got.OpenSince)
	}
}

func timingTestRunState(status StageStatus, started bool) *RunState {
	return &RunState{
		Stages:     map[string]StageState{"a": {Status: status, Timing: StageTiming{Started: started}}},
		Assistants: newAssistantsState(),
	}
}

func TestClassifyStageTime_CoversEveryStatus(t *testing.T) {
	want := map[StageStatus]TimeKind{
		StatusPending:            TimeScheduling,
		StatusPlanning:           TimeWork,
		StatusAwaitingApproval:   TimeApproval,
		StatusRevising:           TimeWork,
		StatusReady:              TimeScheduling,
		StatusRunning:            TimeWork,
		StatusRetrying:           TimeBackoff,
		StatusPaused:             TimePaused,
		StatusAwaitingUserInput:  TimeApproval,
		StatusDone:               TimeNone,
		StatusFailed:             TimeNone,
		StatusHookFailed:         TimeApproval,
		StatusAwaitingAssistants: TimeHelpers, // no blocker: nothing waits for a human
		StatusAwaitingPeer:       TimeHelpers,
		StatusAwaitingQuota:      TimeBackoff,
	}
	for _, status := range AllStatuses() {
		kind, known := want[status]
		if !known {
			t.Fatalf("status %q has no expected time kind: classify it explicitly", status)
		}
		t.Run(string(status), func(t *testing.T) {
			if got := classifyStageTime(timingTestRunState(status, true), "a"); got != kind {
				t.Errorf("started stage: kind = %q, want %q", got, kind)
			}
			if got := classifyStageTime(timingTestRunState(status, false), "a"); got != TimeNone {
				t.Errorf("stage that never started: kind = %q, want none", got)
			}
			ended := timingTestRunState(status, true)
			endedAt := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
			ended.EndedAt = &endedAt
			if got := classifyStageTime(ended, "a"); got != TimeNone {
				t.Errorf("run ended: kind = %q, want none", got)
			}
		})
	}
}

func TestClassifyStageTime_AwaitingAssistantsSplitsHelpersFromHumanDecision(t *testing.T) {
	cases := []struct {
		name   string
		status assistants.TaskStatus
		want   TimeKind
	}{
		{"helper still working", assistants.StatusRunning, TimeHelpers},
		{"helper failed: a human decides", assistants.StatusFailed, TimeApproval},
		{"helper paused: a human decides", assistants.StatusPaused, TimeApproval},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newBarrierFixture()
			f.addTask(assistants.Task{ID: "h", RootStageID: "a", Mode: assistants.ModeRequired, Status: tc.status})
			rs := timingTestRunState(StatusAwaitingAssistants, true)
			rs.Assistants = f.as
			if got := classifyStageTime(rs, "a"); got != tc.want {
				t.Errorf("kind = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStageTiming_LifecycleBuckets(t *testing.T) {
	r := newTimingRig(t, "a")
	r.begin()
	r.move("a", StatusPending, StatusPlanning)
	r.wait(10 * time.Second)
	r.move("a", StatusPlanning, StatusAwaitingApproval)
	r.wait(20 * time.Second)
	r.move("a", StatusAwaitingApproval, StatusReady)
	r.wait(3 * time.Second)
	r.move("a", StatusReady, StatusRunning)
	r.wait(30 * time.Second)
	assertTiming(t, r.timing("a"), timingWant{work: 10 * time.Second, approval: 20 * time.Second, scheduling: 3 * time.Second, open: TimeWork}) // the open 30s is the reader's

	r.move("a", StatusRunning, StatusPaused)
	r.wait(40 * time.Second)
	r.move("a", StatusPaused, StatusRunning)
	r.wait(5 * time.Second)
	r.move("a", StatusRunning, StatusRetrying)
	r.wait(6 * time.Second)
	r.move("a", StatusRetrying, StatusRunning)
	r.wait(7 * time.Second)
	r.move("a", StatusRunning, StatusDone)
	r.wait(time.Hour) // a finished stage accumulates nothing

	want := timingWant{work: 52 * time.Second, approval: 20 * time.Second, backoff: 6 * time.Second, scheduling: 3 * time.Second, paused: 40 * time.Second}
	got := r.timing("a")
	assertTiming(t, got, want)
	if !got.Started || got.Incomplete {
		t.Errorf("Started=%v Incomplete=%v, want started and complete", got.Started, got.Incomplete)
	}
	if want.total() != 121*time.Second {
		t.Fatalf("test arithmetic: buckets sum to %v, want the 121s between start and done", want.total())
	}
}

func TestStageTiming_NotStartedStagesAccumulateNothing(t *testing.T) {
	r := newTimingRig(t, "queued", "gated", "blocked")
	r.begin()
	r.move("queued", StatusPending, StatusReady)
	r.move("gated", StatusPending, StatusPaused) // auto_run: false gate
	r.move("blocked", StatusPending, StatusFailed)
	r.wait(time.Hour)
	for _, id := range []string{"queued", "gated", "blocked"} {
		got := r.timing(id)
		assertTiming(t, got, timingWant{})
		if got.Started {
			t.Errorf("%s: Started before its first start", id)
		}
	}

	r.move("gated", StatusPaused, StatusRunning)
	r.wait(8 * time.Second)
	r.move("gated", StatusRunning, StatusDone)
	assertTiming(t, r.timing("gated"), timingWant{work: 8 * time.Second})
	assertTiming(t, r.timing("queued"), timingWant{})
}

func TestStageTiming_RecoverPlanCountsAsStart(t *testing.T) {
	r := newTimingRig(t, "a")
	r.begin()
	r.move("a", StatusPending, StatusAwaitingApproval) // recoverPlan: pending → awaiting_approval
	r.wait(12 * time.Second)
	r.move("a", StatusAwaitingApproval, StatusRunning)
	assertTiming(t, r.timing("a"), timingWant{approval: 12 * time.Second, open: TimeWork})
}

func TestStageTiming_RetryDoesNotCountFailedGap(t *testing.T) {
	r := newTimingRig(t, "a")
	r.begin()
	r.move("a", StatusPending, StatusRunning)
	r.wait(10 * time.Second)
	r.move("a", StatusRunning, StatusFailed)
	r.wait(time.Hour)
	r.move("a", StatusFailed, StatusPending) // manual Retry
	r.wait(2 * time.Second)
	r.move("a", StatusPending, StatusReady)
	r.wait(3 * time.Second)
	r.move("a", StatusReady, StatusRunning)
	r.wait(4 * time.Second)
	r.move("a", StatusRunning, StatusDone)
	assertTiming(t, r.timing("a"), timingWant{work: 14 * time.Second, scheduling: 5 * time.Second})
}

func TestStageTiming_ParallelStagesAreIndependent(t *testing.T) {
	r := newTimingRig(t, "a", "b")
	r.begin()
	r.move("a", StatusPending, StatusRunning)
	r.wait(5 * time.Second)
	r.move("b", StatusPending, StatusAwaitingApproval)
	r.wait(10 * time.Second)
	r.move("a", StatusRunning, StatusDone)
	r.wait(20 * time.Second)
	r.move("b", StatusAwaitingApproval, StatusRunning)
	assertTiming(t, r.timing("a"), timingWant{work: 15 * time.Second})
	assertTiming(t, r.timing("b"), timingWant{approval: 30 * time.Second, open: TimeWork})
}

func requiredHelperTask(t *testing.T, taskID string) *Control {
	t.Helper()
	c := taskCreatedControl("op-"+taskID, taskID, "a", nil)
	c.Payload = mustMarshal(t, TaskCreatedPayload{ProfileID: "reviewer", Mode: "required"})
	return c
}

func TestStageTiming_ControlRecordFlipsHelpersToApprovalWithoutTransition(t *testing.T) {
	r := newTimingRig(t, "a")
	r.begin()
	r.move("a", StatusPending, StatusRunning)
	r.control(requiredHelperTask(t, "t1"))
	r.wait(5 * time.Second)
	r.move("a", StatusRunning, StatusAwaitingAssistants)
	r.wait(10 * time.Second) // the helper works
	assertTiming(t, r.timing("a"), timingWant{work: 5 * time.Second, open: TimeHelpers})

	r.control(&Control{Kind: "task_status_changed", SchemaVersion: ControlSchemaVersion,
		Target: Target{TaskID: "t1"}, Payload: mustMarshal(t, TaskStatusChangedPayload{Status: "completed", Reason: "turn finished"})})
	r.control(&Control{Kind: "task_result", SchemaVersion: ControlSchemaVersion,
		Target: Target{TaskID: "t1"}, Payload: mustMarshal(t, assistants.Result{Verdict: assistants.VerdictNeedsChanges, Summary: "fix"})})
	r.wait(20 * time.Second) // a human has to decide
	assertTiming(t, r.timing("a"), timingWant{work: 5 * time.Second, helpers: 10 * time.Second, open: TimeApproval})

	r.control(&Control{Kind: "task_waived", SchemaVersion: ControlSchemaVersion, Actor: Actor{Kind: ActorKindUser},
		Target: Target{TaskID: "t1"}, Payload: mustMarshal(t, TaskWaivedPayload{Reason: "accepted"})})
	r.wait(4 * time.Second)
	r.move("a", StatusAwaitingAssistants, StatusDone)
	want := timingWant{work: 5 * time.Second, helpers: 14 * time.Second, approval: 20 * time.Second}
	assertTiming(t, r.timing("a"), want)
	if want.helpers+want.approval != 34*time.Second {
		t.Fatal("test arithmetic: awaiting_assistants lasted 34s")
	}
}

func TestStageTiming_ResumeSkipsOfflineGap(t *testing.T) {
	r := newTimingRig(t, "a")
	r.begin()
	r.move("a", StatusPending, StatusRunning)
	r.wait(10 * time.Second)
	r.end(RunStatusInterrupted)
	got := r.timing("a")
	assertTiming(t, got, timingWant{work: 10 * time.Second})

	r.wait(time.Hour) // afm is not running
	r.begin()
	r.wait(5 * time.Second)
	assertTiming(t, r.timing("a"), timingWant{work: 10 * time.Second, open: TimeWork})
	r.end(RunStatusFinished)
	assertTiming(t, r.timing("a"), timingWant{work: 15 * time.Second})
}

func TestStageTiming_OfflineTransitionAfterEndRunOpensNothing(t *testing.T) {
	r := newTimingRig(t, "a")
	r.begin()
	r.move("a", StatusPending, StatusRunning)
	r.wait(10 * time.Second)
	r.move("a", StatusRunning, StatusFailed)
	r.end(RunStatusFailed)

	r.wait(time.Hour)
	r.move("a", StatusFailed, StatusReady) // offline CLI retry
	r.wait(time.Hour)
	assertTiming(t, r.timing("a"), timingWant{work: 10 * time.Second})

	r.begin()
	r.wait(5 * time.Second)
	r.end(RunStatusFinished)
	assertTiming(t, r.timing("a"), timingWant{work: 10 * time.Second, scheduling: 5 * time.Second})
}

func TestStageTiming_CrashBoundaryIsLastJournalRecord(t *testing.T) {
	r := newTimingRig(t, "a")
	r.begin()
	r.move("a", StatusPending, StatusRunning)
	r.wait(5 * time.Minute)
	r.control(requiredHelperTask(t, "t1")) // the last durable record before the crash
	r.wait(55 * time.Minute)
	r.begin() // the process died: no terminal record, the next run_started is the resume
	assertTiming(t, r.timing("a"), timingWant{work: 5 * time.Minute, open: TimeWork})

	r.wait(10 * time.Second)
	r.end(RunStatusFinished)
	assertTiming(t, r.timing("a"), timingWant{work: 5*time.Minute + 10*time.Second})

	// Run elapsed uses the same boundary, so a stage never outlasts the run that holds it.
	if got, want := r.store.Snapshot().ElapsedAccumulatedMs, (5*time.Minute + 10*time.Second).Milliseconds(); got != want {
		t.Errorf("ElapsedAccumulatedMs = %d, want %d", got, want)
	}
}

func TestStageTiming_OldJournalWithoutRunEventsIsIncomplete(t *testing.T) {
	t0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	rs := replayRunEvents(t, []Transition{
		{Seq: 1, Time: t0, StageID: "a", From: StatusPending, To: StatusRunning},
		{Seq: 2, Time: t0.Add(10 * time.Second), StageID: "a", From: StatusRunning, To: StatusDone},
	})
	got := rs.Stages["a"].Timing
	if !got.Started || !got.Incomplete {
		t.Errorf("Started=%v Incomplete=%v, want a started stage flagged incomplete", got.Started, got.Incomplete)
	}

	rs = replayRunEvents(t, []Transition{
		{Seq: 1, Time: t0, Event: runEventStarted},
		{Seq: 2, Time: t0, StageID: "a", From: StatusPending, To: StatusRunning},
		{Seq: 3, Time: t0.Add(10 * time.Second), StageID: "a", From: StatusRunning, To: StatusDone},
	})
	got = rs.Stages["a"].Timing
	if !got.Started || got.Incomplete {
		t.Errorf("with run events: Started=%v Incomplete=%v, want started and complete", got.Started, got.Incomplete)
	}
	assertTiming(t, got, timingWant{work: 10 * time.Second})
}

func TestStageTiming_DynamicStageHasNoAnchorUntilItStarts(t *testing.T) {
	r := newTimingRig(t, "a")
	r.begin()
	r.wait(5 * time.Second)
	if _, err := r.store.AddStageToGraph(testStageGraphOp("op-1", "n1", []string{"a", "n1"})); err != nil {
		t.Fatalf("AddStageToGraph: %v", err)
	}
	added := r.store.Snapshot().Stages["n1"]
	if !reflect.DeepEqual(added, StageState{Status: StatusPending}) {
		t.Fatalf("dynamic stage = %+v, want an untouched pending stage", added)
	}

	r.wait(10 * time.Second)
	r.move("n1", StatusPending, StatusRunning)
	r.wait(5 * time.Second)
	r.move("n1", StatusRunning, StatusDone)
	assertTiming(t, r.timing("n1"), timingWant{work: 5 * time.Second})
}

func TestStageTiming_NormalizesToUTC(t *testing.T) {
	zone := time.FixedZone("UTC+3", 3*60*60)
	r := newTimingRigAt(t, time.Date(2026, 10, 30, 13, 0, 0, 0, zone), "a")
	r.begin()
	r.move("a", StatusPending, StatusRunning)
	r.wait(time.Second)
	live := r.timing("a")
	if live.OpenSince != live.OpenSince.UTC() {
		t.Errorf("OpenSince %v is not UTC", live.OpenSince)
	}

	r.end(RunStatusFinished)
	rs, err := LoadRunState(r.dir)
	if err != nil {
		t.Fatalf("LoadRunState: %v", err)
	}
	if got, want := rs.Stages["a"].Timing, r.timing("a"); !reflect.DeepEqual(got, want) {
		t.Errorf("replayed timing = %+v, want %+v", got, want)
	}
}

// time.Now carries a monotonic reading that DeepEqual sees but the journal never stores: the live run times
// and stage intervals must be stripped of it, or live and replay differ.
func TestStageTiming_NormalizesWithoutMonotonicReading(t *testing.T) {
	r := newTimingRigAt(t, time.Now(), "a")
	r.begin()
	r.move("a", StatusPending, StatusRunning)
	r.wait(time.Second)
	live := r.timing("a")
	if live.OpenSince != live.OpenSince.Round(0) {
		t.Errorf("OpenSince %v carries a monotonic reading", live.OpenSince)
	}

	r.end(RunStatusFinished)
	liveRun := r.store.Snapshot()
	if liveRun.RunStartedAt != liveRun.RunStartedAt.Round(0) || liveRun.EndedAt == nil || *liveRun.EndedAt != liveRun.EndedAt.Round(0) {
		t.Errorf("live run times carry a monotonic reading: started %v, ended %v", liveRun.RunStartedAt, liveRun.EndedAt)
	}
	loaded, err := LoadRunState(r.dir)
	if err != nil {
		t.Fatalf("LoadRunState: %v", err)
	}
	if !loaded.RunStartedAt.Equal(liveRun.RunStartedAt) || loaded.ElapsedAccumulatedMs != liveRun.ElapsedAccumulatedMs {
		t.Errorf("replayed run clock = %v/%dms, live %v/%dms",
			loaded.RunStartedAt, loaded.ElapsedAccumulatedMs, liveRun.RunStartedAt, liveRun.ElapsedAccumulatedMs)
	}
	if got, want := loaded.Stages["a"].Timing, r.timing("a"); !reflect.DeepEqual(got, want) {
		t.Errorf("replayed timing = %+v, want %+v", got, want)
	}
}

// TestStageTiming_LiveEqualsReplay runs one scenario with every kind of record, then compares the live
// projection with the Store reopened from the journal and with the lock-free read-only load.
func TestStageTiming_LiveEqualsReplay(t *testing.T) {
	r := newTimingRig(t, "a", "b")
	r.begin()
	r.move("a", StatusPending, StatusPlanning)
	r.wait(3 * time.Second)
	r.move("a", StatusPlanning, StatusAwaitingApproval)
	r.move("b", StatusPending, StatusRunning)
	r.wait(4 * time.Second)
	r.control(requiredHelperTask(t, "t1"))
	r.move("a", StatusAwaitingApproval, StatusRunning)
	r.wait(5 * time.Second)
	r.move("a", StatusRunning, StatusAwaitingAssistants)
	r.wait(6 * time.Second)
	r.end(RunStatusInterrupted)
	r.wait(time.Hour)
	r.begin()
	r.wait(7 * time.Second)

	live := r.store.Snapshot()
	r.reopen()
	reopened := r.store.Snapshot()
	loaded, err := LoadRunState(r.dir)
	if err != nil {
		t.Fatalf("LoadRunState: %v", err)
	}
	for _, id := range []string{"a", "b"} {
		if !reflect.DeepEqual(live.Stages[id].Timing, reopened.Stages[id].Timing) {
			t.Errorf("%s: live timing %+v != reopened %+v", id, live.Stages[id].Timing, reopened.Stages[id].Timing)
		}
		if !reflect.DeepEqual(live.Stages[id].Timing, loaded.Stages[id].Timing) {
			t.Errorf("%s: live timing %+v != LoadRunState %+v", id, live.Stages[id].Timing, loaded.Stages[id].Timing)
		}
	}
	if live.ElapsedAccumulatedMs != reopened.ElapsedAccumulatedMs || live.ElapsedAccumulatedMs != loaded.ElapsedAccumulatedMs {
		t.Errorf("ElapsedAccumulatedMs live=%d reopened=%d loaded=%d",
			live.ElapsedAccumulatedMs, reopened.ElapsedAccumulatedMs, loaded.ElapsedAccumulatedMs)
	}
	if live.lastRecordAt != reopened.lastRecordAt || live.lastRecordAt != loaded.lastRecordAt {
		t.Errorf("lastRecordAt live=%v reopened=%v loaded=%v", live.lastRecordAt, reopened.lastRecordAt, loaded.lastRecordAt)
	}
}

func TestSnapshot_IncludesLastRecordAt(t *testing.T) {
	r := newTimingRig(t, "a")
	r.wait(time.Minute)
	r.move("a", StatusPending, StatusRunning)
	if got, want := r.store.Snapshot().lastRecordAt, r.clock.Now(); !got.Equal(want) {
		t.Errorf("lastRecordAt = %v, want the last record's time %v", got, want)
	}
}

func TestSnapshot_StageTimingIsACopy(t *testing.T) {
	r := newTimingRig(t, "a")
	r.begin()
	r.move("a", StatusPending, StatusRunning)
	r.wait(time.Second)
	r.move("a", StatusRunning, StatusDone)

	snap := r.store.Snapshot()
	st := snap.Stages["a"]
	st.Timing.WorkMs = 999999
	snap.Stages["a"] = st
	assertTiming(t, r.timing("a"), timingWant{work: time.Second})
}

func TestStageTiming_CloseBeforeAnchorAddsNothing(t *testing.T) {
	anchor := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	timing := StageTiming{Started: true, OpenKind: TimeWork, OpenSince: anchor}
	timing.closeAt(anchor.Add(-time.Minute))
	assertTiming(t, timing, timingWant{})
}

// A pre-journal run (state.json only) is migrated into synthetic legacy_load records: the live projection
// must hold what replaying those records gives, or the first restart would change the stage times.
func TestStageTiming_LegacyMigrationLiveEqualsReplay(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"stages":{"a":{"status":"running"},"b":{"status":"awaiting_approval"},"c":{"status":"pending"}}}`
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	fc := clock.NewFake(time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC))
	withFakeClock(t, fc)
	r := &timingRig{t: t, dir: dir, stages: []string{"a", "b", "c"}, clock: fc}
	r.open()

	migrated := r.store.Snapshot()
	for _, id := range []string{"a", "b"} {
		if !migrated.Stages[id].Timing.Started {
			t.Errorf("%s: migrated stage is not marked started: %+v", id, migrated.Stages[id].Timing)
		}
	}
	assertTiming(t, migrated.Stages["a"].Timing, timingWant{open: TimeWork})
	assertTiming(t, migrated.Stages["b"].Timing, timingWant{open: TimeApproval})

	loaded, err := LoadRunState(dir)
	if err != nil {
		t.Fatalf("LoadRunState: %v", err)
	}
	for _, id := range []string{"a", "b", "c"} {
		if !reflect.DeepEqual(migrated.Stages[id].Timing, loaded.Stages[id].Timing) {
			t.Errorf("%s: live %+v != LoadRunState %+v", id, migrated.Stages[id].Timing, loaded.Stages[id].Timing)
		}
	}

	// A transition after the migration keeps live and replay together too.
	r.wait(10 * time.Second)
	r.move("a", StatusRunning, StatusDone)
	live := r.store.Snapshot()
	r.reopen()
	reopened := r.store.Snapshot()
	for _, id := range []string{"a", "b", "c"} {
		if !reflect.DeepEqual(live.Stages[id].Timing, reopened.Stages[id].Timing) {
			t.Errorf("%s: live %+v != reopened %+v", id, live.Stages[id].Timing, reopened.Stages[id].Timing)
		}
	}
	if live.lastRecordAt != reopened.lastRecordAt {
		t.Errorf("lastRecordAt live=%v reopened=%v", live.lastRecordAt, reopened.lastRecordAt)
	}
}

// A stage that was already past its start when state.json was migrated has history nobody recorded: it must be
// reported as started with incomplete time (not as 0s), and stay so after a reopen. Never-started statuses stay
// not started.
func TestStageTiming_LegacyFinishedStageIsIncomplete(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"started_at":"2026-10-30T09:00:00Z","stages":{` +
		`"done":{"status":"done"},"failed":{"status":"failed"},"paused":{"status":"paused"},` +
		`"ready":{"status":"ready"},"pending":{"status":"pending"}}}`
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	fc := clock.NewFake(time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC))
	withFakeClock(t, fc)
	r := &timingRig{t: t, dir: dir, stages: []string{"done", "failed", "paused", "ready", "pending"}, clock: fc}
	r.open()

	check := func(label string, rs RunState) {
		t.Helper()
		for _, id := range []string{"done", "failed", "paused"} {
			if got := rs.Stages[id].Timing; !got.Started || !got.Incomplete {
				t.Errorf("%s: %s Started=%v Incomplete=%v, want started and incomplete", label, id, got.Started, got.Incomplete)
			}
		}
		for _, id := range []string{"ready", "pending"} {
			if got := rs.Stages[id].Timing; got.Started {
				t.Errorf("%s: %s is marked started: %+v", label, id, got)
			}
		}
	}
	check("migrated", r.store.Snapshot())
	r.reopen()
	check("reopened", r.store.Snapshot())
	loaded, err := LoadRunState(dir)
	if err != nil {
		t.Fatalf("LoadRunState: %v", err)
	}
	check("LoadRunState", loaded)
}
