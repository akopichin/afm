package state

import (
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/clock"
)

// withFakeClock installs fc as the package clock for the duration of the test and restores the real clock
// on cleanup — pkg/state's clock seam is a single package-level var (like the existing applyHook), so tests
// must not leak it into unrelated tests that run afterward in the same binary.
func withFakeClock(t *testing.T, fc clock.Clock) {
	t.Helper()
	SetClockForTest(fc)
	t.Cleanup(func() { SetClockForTest(nil) })
}

// TestApply_StampsTransitionWithInjectedClock asserts Store.Apply stamps Transition.Time from the injected
// clock, not real wall time (T9, dev-plan.md) — the whole point of the seam.
func TestApply_StampsTransitionWithInjectedClock(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	fc := clock.NewFake(start)
	withFakeClock(t, fc)

	dir := t.TempDir()
	s, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	fc.Advance(5 * time.Minute)
	want := fc.Now()

	tr := &Transition{StageID: "a", From: StatusPending, To: StatusPlanning, Event: "start_planning"}
	if err := s.Apply(tr); err != nil {
		t.Fatal(err)
	}
	if !tr.Time.Equal(want) {
		t.Fatalf("Transition.Time = %v, want exactly the injected clock's %v (real time.Now() was used instead)", tr.Time, want)
	}
	if got := s.Snapshot().Stages["a"].UpdatedAt; !got.Equal(want) {
		t.Fatalf("Stages[a].UpdatedAt = %v, want exactly %v", got, want)
	}
}

// TestAppendControl_StampsWithInjectedClock mirrors the above for the separate AppendControl write path
// (store.go), which stamps its own Control.Time independently of Apply.
func TestAppendControl_StampsWithInjectedClock(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	fc := clock.NewFake(start)
	withFakeClock(t, fc)

	dir := t.TempDir()
	s, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	fc.Advance(90 * time.Second)
	want := fc.Now()

	c := &Control{Kind: kindFeedbackIntentCreated, SchemaVersion: ControlSchemaVersion, OperationID: "op-1",
		Target:  Target{StageID: "a", IntentID: "fi-1"},
		Payload: mustJSON(t, FeedbackIntentCreatedPayload{StageID: "a", Feedback: "x", Origin: "user"}),
	}
	if err := s.AppendControl(c); err != nil {
		t.Fatal(err)
	}
	if !c.Time.Equal(want) {
		t.Fatalf("Control.Time = %v, want exactly the injected clock's %v", c.Time, want)
	}
}

// TestNewRunState_StartedAtUsesInjectedClock asserts RunState.StartedAt (feeds ElapsedSince) is stamped
// from the package clock, not time.Now directly.
func TestNewRunState_StartedAtUsesInjectedClock(t *testing.T) {
	start := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	fc := clock.NewFake(start)
	withFakeClock(t, fc)

	rs := NewRunState([]string{"a"})
	if !rs.StartedAt.Equal(start) {
		t.Fatalf("StartedAt = %v, want exactly the injected clock's %v", rs.StartedAt, start)
	}
}

// TestAccountIdleAndBackoff_AccumulatesInstantlyViaClockAdvance is T9's main payoff: simulating an offline
// idle/backoff/elapsed pause no longer needs a real sleep — Advance does it instantly — and all three
// accumulators must survive a full close+replay unchanged, proving parseEventLog uses the historical
// Transition.Time from the log rather than the current (possibly advanced further) clock (dev-plan.md T9's
// replay invariant). Idle (stage "a") and backoff (stage "b") are driven independently so neither
// accumulator can hide a bug in the other; elapsed is driven by BeginRun/EndRun around both.
func TestAccountIdleAndBackoff_AccumulatesInstantlyViaClockAdvance(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	fc := clock.NewFake(start)
	withFakeClock(t, fc)

	dir := t.TempDir()
	s, err := Open(dir, []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}

	if err := s.BeginRun(); err != nil {
		t.Fatal(err)
	}

	if err := s.Apply(&Transition{StageID: "a", From: StatusPending, To: StatusAwaitingUserInput, Event: "ask"}); err != nil {
		t.Fatal(err)
	}

	fc.Advance(10 * time.Minute) // an "offline pause" — instant, no real sleep

	if err := s.Apply(&Transition{StageID: "a", From: StatusAwaitingUserInput, To: StatusRunning, Event: "answer"}); err != nil {
		t.Fatal(err)
	}

	wantIdle := (10 * time.Minute).Milliseconds()
	if got := s.Snapshot().IdleAccumulatedMs; got != wantIdle {
		t.Fatalf("IdleAccumulatedMs = %d, want %d", got, wantIdle)
	}

	if err := s.Apply(&Transition{StageID: "b", From: StatusPending, To: StatusRetrying, Event: "retry"}); err != nil {
		t.Fatal(err)
	}

	fc.Advance(7 * time.Minute) // backoff window, also instant

	if err := s.Apply(&Transition{StageID: "b", From: StatusRetrying, To: StatusRunning, Event: "retry_run"}); err != nil {
		t.Fatal(err)
	}

	wantBackoff := (7 * time.Minute).Milliseconds()
	if got := s.Snapshot().BackoffAccumulatedMs; got != wantBackoff {
		t.Fatalf("BackoffAccumulatedMs = %d, want %d", got, wantBackoff)
	}

	fc.Advance(3 * time.Minute)
	if err := s.EndRun(RunStatusFinished); err != nil {
		t.Fatal(err)
	}

	wantElapsed := (20 * time.Minute).Milliseconds() // 10 + 7 + 3 minutes since BeginRun
	if got := s.Snapshot().ElapsedAccumulatedMs; got != wantElapsed {
		t.Fatalf("ElapsedAccumulatedMs = %d, want %d", got, wantElapsed)
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// Advance further AFTER closing: if replay ever started using clk() instead of the log's historical
	// t.Time, this extra Advance would leak into the replayed values and these assertions would catch it.
	fc.Advance(time.Hour)

	rs2, err := LoadRunState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := rs2.IdleAccumulatedMs; got != wantIdle {
		t.Fatalf("after replay: IdleAccumulatedMs = %d, want %d (replay must use historical t.Time, not clk())", got, wantIdle)
	}
	if got := rs2.BackoffAccumulatedMs; got != wantBackoff {
		t.Fatalf("after replay: BackoffAccumulatedMs = %d, want %d (replay must use historical t.Time, not clk())", got, wantBackoff)
	}
	if got := rs2.ElapsedAccumulatedMs; got != wantElapsed {
		t.Fatalf("after replay: ElapsedAccumulatedMs = %d, want %d (replay must use historical t.Time, not clk())", got, wantElapsed)
	}
}

// TestClk_NilClockDefaultsToReal asserts the nil-safe fallback: without an injected clock, clk() behaves
// like clock.Real() (monotonically increasing, not frozen).
func TestClk_NilClockDefaultsToReal(t *testing.T) {
	SetClockForTest(nil)
	a := clk().Now()
	time.Sleep(time.Millisecond)
	b := clk().Now()
	if !b.After(a) {
		t.Fatalf("clk().Now() did not advance with nil clock: a=%v b=%v", a, b)
	}
}
