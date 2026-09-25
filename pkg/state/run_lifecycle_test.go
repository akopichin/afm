package state

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func replayRunEvents(t *testing.T, events []Transition) RunState {
	t.Helper()
	var log bytes.Buffer
	for _, event := range events {
		if err := json.NewEncoder(&log).Encode(event); err != nil {
			t.Fatal(err)
		}
	}
	rs := RunState{Stages: map[string]StageState{"a": {Status: StatusPending}}}
	result := parseEventLog(log.Bytes(), &rs)
	if result.corrupted {
		t.Fatal("run log was reported corrupt")
	}
	return rs
}

func TestRunEvents_CloseAndResumeIdleWithoutCountingDowntime(t *testing.T) {
	t0 := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	events := []Transition{
		{Seq: 1, Time: t0, Event: runEventStarted},
		{Seq: 2, Time: t0.Add(time.Second), StageID: "a", To: StatusFailed},
		{Seq: 3, Time: t0.Add(4 * time.Second), Event: runEventFailed},
	}
	rs := replayRunEvents(t, events)
	if !rs.StartedAt.Equal(t0) || rs.RunStatus != RunStatusFailed || rs.EndedAt == nil || !rs.EndedAt.Equal(t0.Add(4*time.Second)) {
		t.Fatalf("unexpected finished run: %+v", rs)
	}
	if rs.IdleAccumulatedMs != 3000 || rs.IdleSince() != nil {
		t.Fatalf("finished idle: accumulated=%d since=%v", rs.IdleAccumulatedMs, rs.IdleSince())
	}
	if rs.ElapsedAccumulatedMs != 4000 || rs.ElapsedSince() != nil {
		t.Fatalf("finished elapsed: accumulated=%d since=%v", rs.ElapsedAccumulatedMs, rs.ElapsedSince())
	}

	resume := t0.Add(time.Hour)
	events = append(events, Transition{Seq: 4, Time: resume, Event: runEventStarted})
	rs = replayRunEvents(t, events)
	if rs.EndedAt != nil || rs.RunStatus != RunStatusRunning || rs.IdleSince() == nil || !rs.IdleSince().Equal(resume) {
		t.Fatalf("unexpected resumed run: %+v", rs)
	}
	if rs.ElapsedAccumulatedMs != 4000 || rs.ElapsedSince() == nil || !rs.ElapsedSince().Equal(resume) {
		t.Fatalf("resumed elapsed = %+v", rs)
	}
	events = append(events, Transition{Seq: 5, Time: resume.Add(2 * time.Second), StageID: "a", To: StatusRunning})
	rs = replayRunEvents(t, events)
	if rs.IdleAccumulatedMs != 5000 || rs.IdleSince() != nil {
		t.Fatalf("resumed idle: accumulated=%d since=%v", rs.IdleAccumulatedMs, rs.IdleSince())
	}
	events = append(events, Transition{Seq: 6, Time: resume.Add(5 * time.Second), Event: runEventFinished})
	rs = replayRunEvents(t, events)
	if rs.ElapsedAccumulatedMs != 9000 {
		t.Fatalf("finished elapsed after resume = %d, want 9000", rs.ElapsedAccumulatedMs)
	}
}

func TestRunEvents_CloseAndResumeBackoffWithoutCountingDowntime(t *testing.T) {
	t0 := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	events := []Transition{
		{Seq: 1, Time: t0, Event: runEventStarted},
		{Seq: 2, Time: t0.Add(time.Second), StageID: "a", To: StatusRetrying},
		{Seq: 3, Time: t0.Add(4 * time.Second), Event: runEventInterrupted},
	}
	rs := replayRunEvents(t, events)
	if rs.BackoffAccumulatedMs != 3000 || len(rs.BackoffOpenSince()) != 0 {
		t.Fatalf("finished backoff: accumulated=%d since=%v", rs.BackoffAccumulatedMs, rs.BackoffOpenSince())
	}
	resume := t0.Add(time.Hour)
	events = append(events, Transition{Seq: 4, Time: resume, Event: runEventStarted})
	rs = replayRunEvents(t, events)
	open := rs.BackoffOpenSince()
	if len(open) != 1 || !open[0].Equal(resume) {
		t.Fatalf("resumed backoff anchors = %v", open)
	}
	events = append(events, Transition{Seq: 5, Time: resume.Add(2 * time.Second), StageID: "a", To: StatusRunning})
	rs = replayRunEvents(t, events)
	if rs.BackoffAccumulatedMs != 5000 {
		t.Fatalf("resumed backoff accumulated = %d, want 5000", rs.BackoffAccumulatedMs)
	}
}

func TestRunEvents_OfflineStageTransitionDoesNotExtendIdle(t *testing.T) {
	t0 := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	rs := replayRunEvents(t, []Transition{
		{Seq: 1, Time: t0, Event: runEventStarted},
		{Seq: 2, Time: t0.Add(time.Second), StageID: "a", To: StatusFailed},
		{Seq: 3, Time: t0.Add(4 * time.Second), Event: runEventFailed},
		{Seq: 4, Time: t0.Add(time.Hour), StageID: "a", To: StatusReady},
	})
	if rs.IdleAccumulatedMs != 3000 || rs.IdleSince() != nil {
		t.Fatalf("offline transition counted as idle: %+v", rs)
	}
}

func TestRunEvents_CrashResumeCountsOnlyDurableActiveTime(t *testing.T) {
	t0 := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	resume := t0.Add(time.Hour)
	rs := replayRunEvents(t, []Transition{
		{Seq: 1, Time: t0, Event: runEventStarted},
		{Seq: 2, Time: t0.Add(3 * time.Second), StageID: "a", To: StatusRunning},
		{Seq: 3, Time: resume, Event: runEventStarted},
		{Seq: 4, Time: resume.Add(2 * time.Second), Event: runEventInterrupted},
	})
	if rs.ElapsedAccumulatedMs != 5000 {
		t.Fatalf("elapsed across crash = %d, want 5000", rs.ElapsedAccumulatedMs)
	}
}

func TestStoreRunEventsAreDurableAndExcludedFromStageHistory(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BeginRun(); err != nil {
		t.Fatal(err)
	}
	if running := store.Snapshot(); running.RunStatus != RunStatusRunning || running.ElapsedSince() == nil {
		t.Fatalf("running run boundary = %+v", running)
	}
	if err := store.EndRun(RunStatusInterrupted); err != nil {
		t.Fatal(err)
	}
	live := store.Snapshot()
	if live.LastSeq != 2 || live.EndedAt == nil {
		t.Fatalf("live run boundary = %+v", live)
	}
	if history, err := store.History(); err != nil || len(history) != 0 {
		t.Fatalf("stage history = %v, %v", history, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	replayed, err := LoadRunState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.RunStatus != live.RunStatus || replayed.EndedAt == nil || !replayed.EndedAt.Equal(*live.EndedAt) || replayed.LastSeq != live.LastSeq {
		t.Fatalf("replayed run = %+v, live = %+v", replayed, live)
	}
	store, err = Open(dir, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.BeginRun(); err != nil {
		t.Fatal(err)
	}
	if got := store.Snapshot(); got.EndedAt != nil || got.RunStatus != RunStatusRunning || got.LastSeq != 3 {
		t.Fatalf("resumed run = %+v", got)
	}
}

func TestHookFailedCountsAsIdle(t *testing.T) {
	if !isIdle(map[string]StageState{"a": {Status: StatusHookFailed}}) {
		t.Fatal("hook_failed waits for user action and must count as idle")
	}
}
