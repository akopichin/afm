package state

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func openQuotaStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(dir, []string{"s"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, dir
}

func quotaPark(t *testing.T, s *Store, from StageStatus, q *QuotaWait) {
	t.Helper()
	if err := s.Apply(&Transition{StageID: "s", From: from, To: StatusAwaitingQuota, Event: "await_quota", Quota: q}); err != nil {
		t.Fatalf("park: %v", err)
	}
}

func replayStage(t *testing.T, s *Store, dir string) StageState {
	t.Helper()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	rs, err := LoadRunState(dir)
	if err != nil {
		t.Fatal(err)
	}
	return rs.Stages["s"]
}

// A park records the quota metadata; replay restores every field.
func TestQuotaPark_RecordedAndReplayed(t *testing.T) {
	s, dir := openQuotaStore(t)
	applyTr(t, s, StatusPending, StatusRunning, "start_run")
	reset := time.Date(2026, 10, 23, 23, 50, 0, 0, time.UTC)
	until := reset.Add(time.Minute)
	want := QuotaWait{
		Provider:         "claude",
		ResetAt:          reset,
		Until:            until,
		Window:           "five_hour",
		Source:           QuotaSourceStructured,
		Target:           QuotaTargetVerify,
		VerifyStep:       3,
		VerifyFailures:   1,
		VerifyStalemates: 2,
		Waits:            2,
		ProgressMark:     17,
	}
	quotaPark(t, s, StatusRunning, &want)

	got := s.Snapshot().Stages["s"]
	if got.Status != StatusAwaitingQuota || got.Quota == nil || !reflect.DeepEqual(*got.Quota, want) {
		t.Fatalf("live state = %+v quota=%+v, want parked with %+v", got.Status, got.Quota, want)
	}
	replayed := replayStage(t, s, dir)
	if replayed.Status != StatusAwaitingQuota || replayed.Quota == nil || !replayed.Quota.ResetAt.Equal(reset) ||
		!replayed.Quota.Until.Equal(until) || replayed.Quota.Provider != "claude" || replayed.Quota.Window != "five_hour" ||
		replayed.Quota.Source != QuotaSourceStructured || replayed.Quota.Target != QuotaTargetVerify ||
		replayed.Quota.VerifyStep != 3 || replayed.Quota.VerifyFailures != 1 || replayed.Quota.VerifyStalemates != 2 ||
		replayed.Quota.Waits != 2 || replayed.Quota.ProgressMark != 17 {
		t.Fatalf("replayed quota = %+v, want %+v", replayed.Quota, want)
	}
}

// The park is not mutated through the caller's pointer afterwards.
func TestQuotaPark_SnapshotIsolatedFromCaller(t *testing.T) {
	s, _ := openQuotaStore(t)
	applyTr(t, s, StatusPending, StatusRunning, "start_run")
	q := &QuotaWait{Provider: "codex", Waits: 1}
	quotaPark(t, s, StatusRunning, q)
	q.Waits = 99
	if got := s.Snapshot().Stages["s"].Quota.Waits; got != 1 {
		t.Fatalf("stored Waits = %d, want 1 (state must copy the caller's value)", got)
	}
}

// Pausing a park keeps its quota (reason included), so PausedFrom=awaiting_quota and the cause survive a
// restart; Continue back into the park may replace it with the one-shot manual probe.
func TestQuotaPark_PauseKeepsQuotaAndContinueCarriesProbe(t *testing.T) {
	s, dir := openQuotaStore(t)
	applyTr(t, s, StatusPending, StatusRunning, "start_run")
	quotaPark(t, s, StatusRunning, &QuotaWait{Provider: "claude", Source: QuotaSourceText, PauseReason: QuotaPauseNoReset, Waits: 3})
	applyTr(t, s, StatusAwaitingQuota, StatusPaused, "pause")

	paused := s.Snapshot().Stages["s"]
	if paused.PausedFrom != StatusAwaitingQuota {
		t.Fatalf("PausedFrom = %q, want awaiting_quota", paused.PausedFrom)
	}
	if paused.Quota == nil || paused.Quota.PauseReason != QuotaPauseNoReset || paused.Quota.Waits != 3 {
		t.Fatalf("quota after pause = %+v", paused.Quota)
	}

	probe := *paused.Quota
	probe.PauseReason, probe.ManualProbe, probe.Waits = "", true, 0
	if err := s.Apply(&Transition{StageID: "s", From: StatusPaused, To: StatusAwaitingQuota, Event: "continue", Quota: &probe}); err != nil {
		t.Fatal(err)
	}
	got := replayStage(t, s, dir)
	if got.Status != StatusAwaitingQuota || got.PausedFrom != StatusAwaitingQuota {
		t.Fatalf("status/pausedFrom = %q/%q", got.Status, got.PausedFrom)
	}
	if got.Quota == nil || !got.Quota.ManualProbe || got.Quota.PauseReason != "" || got.Quota.Waits != 0 {
		t.Fatalf("quota after Continue = %+v, want the manual probe with a fresh series", got.Quota)
	}
}

// Feedback marks the parked turn as a Revise's feedback run: it survives Pause, the manual-probe Continue, the
// wake and a replay, and a later park that does not set it is a fresh turn again.
func TestQuotaPark_FeedbackSurvivesPauseContinueWakeAndReplay(t *testing.T) {
	s, dir := openQuotaStore(t)
	applyTr(t, s, StatusPending, StatusRunning, "start_run")
	quotaPark(t, s, StatusRunning, &QuotaWait{Provider: "claude", Waits: 1, Feedback: true})
	applyTr(t, s, StatusAwaitingQuota, StatusPaused, "pause")

	probe := *s.Snapshot().Stages["s"].Quota
	probe.ManualProbe = true
	if err := s.Apply(&Transition{StageID: "s", From: StatusPaused, To: StatusAwaitingQuota, Event: "continue", Quota: &probe}); err != nil {
		t.Fatal(err)
	}
	if q := s.Snapshot().Stages["s"].Quota; q == nil || !q.Feedback {
		t.Fatalf("quota after Pause and the manual-probe Continue = %+v, want Feedback kept", q)
	}

	applyTr(t, s, StatusAwaitingQuota, StatusRunning, "quota_reset")
	if q := s.Snapshot().Stages["s"].Quota; q == nil || !q.Feedback {
		t.Fatalf("quota after the wake = %+v, want Feedback kept for the resumed turn", q)
	}

	quotaPark(t, s, StatusRunning, &QuotaWait{Provider: "claude", Waits: 1})
	if q := replayStage(t, s, dir).Quota; q == nil || q.Feedback {
		t.Fatalf("replayed quota of a later fresh-turn park = %+v, want Feedback cleared", q)
	}
}

// Continue without a replacement keeps the paused park's quota as it was.
func TestQuotaPark_ContinueWithoutQuotaKeepsPark(t *testing.T) {
	s, _ := openQuotaStore(t)
	applyTr(t, s, StatusPending, StatusRunning, "start_run")
	quotaPark(t, s, StatusRunning, &QuotaWait{Provider: "claude", Waits: 2})
	applyTr(t, s, StatusAwaitingQuota, StatusPaused, "pause")
	applyTr(t, s, StatusPaused, StatusAwaitingQuota, "continue")
	if q := s.Snapshot().Stages["s"].Quota; q == nil || q.Waits != 2 {
		t.Fatalf("quota = %+v, want the original park", q)
	}
}

// The wake keeps the series counter and the one-shot probe for the process that is about to start; any
// other transition drops the probe (and a terminal one drops everything).
func TestQuotaPark_WakeKeepsCounterAndProbeUntilNextTransition(t *testing.T) {
	s, dir := openQuotaStore(t)
	applyTr(t, s, StatusPending, StatusRunning, "start_run")
	quotaPark(t, s, StatusRunning, &QuotaWait{Provider: "claude", Waits: 2, ProgressMark: 5, ManualProbe: true})
	applyTr(t, s, StatusAwaitingQuota, StatusRunning, "quota_reset")

	woke := s.Snapshot().Stages["s"]
	if woke.Quota == nil || woke.Quota.Waits != 2 || woke.Quota.ProgressMark != 5 || !woke.Quota.ManualProbe {
		t.Fatalf("quota after wake = %+v, want counter and probe kept", woke.Quota)
	}
	applyTr(t, s, StatusRunning, StatusRetrying, "schedule_retry")
	retrying := s.Snapshot().Stages["s"]
	if retrying.Quota == nil || retrying.Quota.Waits != 2 || retrying.Quota.ManualProbe {
		t.Fatalf("quota after the next transition = %+v, want counter kept and probe dropped", retrying.Quota)
	}
	applyTr(t, s, StatusRetrying, StatusRunning, "resume_after_retry")
	applyTr(t, s, StatusRunning, StatusDone, "complete")
	done := replayStage(t, s, dir)
	if done.Quota != nil {
		t.Fatalf("quota after done = %+v, want nil", done.Quota)
	}
}

// A journal written before quota existed replays unchanged: no quota anywhere.
func TestQuota_OldJournalReplaysWithoutQuota(t *testing.T) {
	s, dir := openQuotaStore(t)
	applyTr(t, s, StatusPending, StatusRunning, "start_run")
	applyTr(t, s, StatusRunning, StatusPaused, "pause")
	applyTr(t, s, StatusPaused, StatusRunning, "continue")
	got := replayStage(t, s, dir)
	if got.Quota != nil || got.Status != StatusRunning {
		t.Fatalf("state = %+v, want running without quota", got)
	}
}

// Stage Seq identifies the park; a wake fenced to an older park is refused, as is a stale author's park.
func TestQuotaPark_FencesApplyInStore(t *testing.T) {
	s, _ := openQuotaStore(t)
	applyTr(t, s, StatusPending, StatusRunning, "start_run")
	staleEpoch := s.Snapshot().Stages["s"].Epoch
	quotaPark(t, s, StatusRunning, &QuotaWait{Provider: "claude"})
	firstPark := s.Snapshot().Stages["s"].Seq
	applyTr(t, s, StatusAwaitingQuota, StatusPaused, "pause")
	applyTr(t, s, StatusPaused, StatusRunning, "continue")

	err := s.Apply(&Transition{StageID: "s", From: StatusRunning, To: StatusAwaitingQuota, Event: "await_quota", ExpectEpoch: &staleEpoch})
	if !errors.Is(err, ErrConcurrentChange) {
		t.Fatalf("stale author park: err = %v, want ErrConcurrentChange", err)
	}
	quotaPark(t, s, StatusRunning, &QuotaWait{Provider: "claude"})
	err = s.Apply(&Transition{StageID: "s", From: StatusAwaitingQuota, To: StatusRunning, Event: "quota_reset", ExpectStageSeq: &firstPark})
	if !errors.Is(err, ErrConcurrentChange) {
		t.Fatalf("timer of an older park: err = %v, want ErrConcurrentChange", err)
	}
	current := s.Snapshot().Stages["s"].Seq
	if err := s.Apply(&Transition{StageID: "s", From: StatusAwaitingQuota, To: StatusRunning, Event: "quota_reset", ExpectStageSeq: &current}); err != nil {
		t.Fatalf("timer of the current park: %v", err)
	}
}

// A parked stage is machine work: it must not let a flow with a failed sibling look idle, and the wait is
// not backoff. Live and replay agree.
func TestQuotaPark_IdleAccountingIsActivityLikeAndReplays(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, []string{"s", "failed"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.Apply(&Transition{StageID: "failed", From: StatusPending, To: StatusFailed, Event: "fail"}); err != nil {
		t.Fatal(err)
	}
	applyTr(t, s, StatusPending, StatusRunning, "start_run")
	quotaPark(t, s, StatusRunning, &QuotaWait{Provider: "claude"})
	snap := s.Snapshot()
	if snap.IdleSince() != nil {
		t.Fatalf("IdleSince = %v while a stage waits for quota, want nil", snap.IdleSince())
	}
	if len(snap.BackoffOpenSince()) != 0 {
		t.Fatal("quota wait must not open a backoff interval")
	}
}

// The wake of a verify park records the one-shot intent to repeat the parked verify step; it survives replay and
// dies with the next transition (Pause, Revise, retry, ...). The wake of any other park records none.
func TestQuotaPark_VerifyWakeRecordsOneShotResumeIntent(t *testing.T) {
	park := &QuotaWait{Provider: "codex", Target: QuotaTargetVerify, VerifyID: "v-1", VerifyStep: 2}

	s, dir := openQuotaStore(t)
	applyTr(t, s, StatusPending, StatusRunning, "start_run")
	quotaPark(t, s, StatusRunning, park)
	if q := s.Snapshot().Stages["s"].Quota; q.VerifyResume {
		t.Fatal("a parked verify step must not carry the resume intent before the wake")
	}
	applyTr(t, s, StatusAwaitingQuota, StatusRunning, "quota_reset")
	if q := s.Snapshot().Stages["s"].Quota; !q.VerifyResume || q.VerifyID != "v-1" || q.VerifyStep != 2 {
		t.Fatalf("quota after the verify wake = %+v, want the intent with the saved cursor", q)
	}
	if q := replayStage(t, s, dir).Quota; q == nil || !q.VerifyResume || q.VerifyStep != 2 {
		t.Fatalf("replayed quota = %+v, want the intent restored", q)
	}

	for _, next := range []struct {
		name string
		to   StageStatus
		ev   string
	}{
		{"pause", StatusPaused, "pause"},
		{"revise", StatusRevising, "revise"},
		{"retry", StatusRetrying, "schedule_retry"},
	} {
		t.Run(next.name, func(t *testing.T) {
			s, dir := openQuotaStore(t)
			applyTr(t, s, StatusPending, StatusRunning, "start_run")
			quotaPark(t, s, StatusRunning, park)
			applyTr(t, s, StatusAwaitingQuota, StatusRunning, "quota_reset")
			applyTr(t, s, StatusRunning, next.to, next.ev)
			if q := replayStage(t, s, dir).Quota; q == nil || q.VerifyResume || q.VerifyID != "v-1" {
				t.Fatalf("quota after %s = %+v, want the intent consumed and the series kept", next.ev, q)
			}
		})
	}

	s, _ = openQuotaStore(t)
	applyTr(t, s, StatusPending, StatusRunning, "start_run")
	quotaPark(t, s, StatusRunning, &QuotaWait{Provider: "claude", Target: "implementation"})
	applyTr(t, s, StatusAwaitingQuota, StatusRunning, "quota_reset")
	if q := s.Snapshot().Stages["s"].Quota; q.VerifyResume {
		t.Fatal("the wake of an author park must not record a verify resume intent")
	}
}
