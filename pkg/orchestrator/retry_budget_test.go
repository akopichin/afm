package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/clock"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

func TestTransportRetryDecision_BudgetBoundaries(t *testing.T) {
	tests := []struct {
		name               string
		class              Classification
		attempt            int
		unknownRetries     int
		maxRetries         int
		maxUnknownRetries  int
		wantRetry          bool
		wantBudget         int
		wantNumerator      int
		wantUnknownRetries int
	}{
		{"known first", ClassRetryable, 0, 0, 5, 3, true, 5, 1, 0},
		{"known before last", ClassRetryable, 4, 0, 5, 3, true, 5, 5, 0},
		{"known last", ClassRetryable, 5, 0, 5, 3, false, 5, 6, 0},
		{"known zero budget", ClassRetryable, 0, 0, 0, 3, false, 0, 1, 0},
		{"known after unknown budget", ClassRetryable, 3, 3, 5, 3, true, 5, 4, 3},
		{"unknown first", ClassRetryableUnknown, 0, 0, 5, 3, true, 3, 1, 1},
		{"unknown at budget", ClassRetryableUnknown, 2, 2, 5, 3, true, 3, 3, 3},
		{"unknown past budget", ClassRetryableUnknown, 3, 3, 5, 3, false, 3, 4, 4},
		{"unknown before last", ClassRetryableUnknown, 4, 0, 5, 3, true, 3, 1, 1},
		{"unknown last", ClassRetryableUnknown, 5, 0, 5, 3, false, 3, 1, 1},
		{"unknown zero budget", ClassRetryableUnknown, 0, 0, 5, 0, false, 0, 1, 1},
		{"unknown zero outer budget", ClassRetryableUnknown, 0, 0, 0, 3, false, 3, 1, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			retry, budget, numerator, unknown := transportRetryDecision(tt.class, tt.attempt, tt.unknownRetries, tt.maxRetries, tt.maxUnknownRetries)
			if retry != tt.wantRetry || budget != tt.wantBudget || numerator != tt.wantNumerator || unknown != tt.wantUnknownRetries {
				t.Fatalf("decision = (%t, %d, %d, %d), want (%t, %d, %d, %d)",
					retry, budget, numerator, unknown, tt.wantRetry, tt.wantBudget, tt.wantNumerator, tt.wantUnknownRetries)
			}
		})
	}
}

func TestVerifyCorrection_RetryBudgetBoundaries(t *testing.T) {
	tests := []struct {
		name           string
		isVerifyReject bool
		attempt        int
		maxRetries     int
		failures       int
		maxFailures    int
		want           bool
	}{
		{"verify first", true, 0, 5, 0, 2, true},
		{"verify last correction", true, 3, 5, 1, 2, true},
		{"verify exhausted", true, 2, 5, 2, 2, false},
		{"verify disabled", true, 0, 5, 0, 0, false},
		{"verify last attempt", true, 5, 5, 0, 2, false},
		{"verify zero outer budget", true, 0, 0, 0, 2, false},
		{"incomplete first", false, 0, 5, 0, 2, true},
		{"incomplete ignores verify budget", false, 0, 5, 2, 2, true},
		{"incomplete subsequent", false, 1, 5, 0, 2, false},
		{"incomplete last attempt", false, 5, 5, 0, 2, false},
		{"incomplete zero outer budget", false, 0, 0, 0, 2, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			correction := verifyCorrection{failures: tt.failures, maxFailures: tt.maxFailures}
			if got := correction.canRetry(tt.isVerifyReject, tt.attempt, tt.maxRetries); got != tt.want {
				t.Fatalf("canRetry = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestRunWithRetry_VerifyCorrectionThenTransportPreservesBudgetAndPayload(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
		msg  string
	}{
		{"known", errors.New("connection reset by peer"), "attempt 2/5 in 2ms"},
		{"unknown", errors.New("exit status 1"), "attempt 1/3 in 2ms"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			o, runDir := setupHookOrch(t, "s1")
			withRetryBudgets(o)
			o.maxRetries, o.maxUnknownRetries = 5, 3
			o.jitterFrac = 0
			s := agentVerifyStage(0, 2, agentStep())
			agentFn, calls := scriptedAgentFn(func(call int) error {
				if call == 2 {
					return tt.err
				}
				return nil
			})
			rejected := newVerifyRejected(1)
			checks := 0
			o.runWithRetry(context.Background(), s, phaseImplementation, agentFn, func() error {
				checks++
				return rejected
			}, func() { t.Error("onUserInterrupted should not be called") })

			if calls() != 4 || checks != 3 {
				t.Fatalf("author calls = %d, checks = %d, want 4 and 3 (two corrections and one transport retry)", calls(), checks)
			}
			if got := o.opts.Store.Get(s.ID); got != state.StatusFailed {
				t.Fatalf("status = %v, want failed after exhausting verify corrections", got)
			}
			if got := countRetryNotices(t, runDir); got != 2 {
				t.Errorf("correction notices = %d, want 2", got)
			}
			history, err := o.opts.Store.History()
			if err != nil {
				t.Fatal(err)
			}
			var retries, resumes, verifyFails int
			for _, tr := range history {
				switch tr.Event {
				case string(bus.EvScheduleRetry):
					retries++
					if tr.Reason != tt.msg {
						t.Errorf("retry payload = %q, want %q", tr.Reason, tt.msg)
					}
				case string(bus.EvResumeAfterRetry):
					resumes++
				case string(bus.EvVerifyFail):
					verifyFails++
					if tr.Reason != rejected.Error() {
						t.Errorf("verify failure reason = %q, want %q", tr.Reason, rejected.Error())
					}
				default:
				}
			}
			if retries != 1 || resumes != 1 || verifyFails != 1 {
				t.Errorf("retry/resume/verify-fail transitions = %d/%d/%d, want 1/1/1", retries, resumes, verifyFails)
			}
		})
	}
}

// T5: два независимых бюджета ретраев в runWithRetry (D1) — ClassRetryable держит полный maxRetries,
// ClassRetryableUnknown ограничен maxUnknownRetries. Тесты white-box (как остальные в retry_test.go):
// runWithRetry принимает agentFn как голую функцию, поэтому фейковый executor.Runner не нужен — достаточно
// скриптованного agentFn и Orchestrator, собранного вручную через setupHookOrch.

// scriptedAgentFn returns errAt(call) for the call-th invocation (1-indexed); nil means success. count()
// reports how many times the function has been invoked so far.
func scriptedAgentFn(errAt func(call int) error) (fn func(string) error, count func() int) {
	var mu sync.Mutex
	calls := 0
	fn = func(string) error {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		return errAt(n)
	}
	count = func() int {
		mu.Lock()
		defer mu.Unlock()
		return calls
	}
	return fn, count
}

// withRetryBudgets sets maxRetries/retryBackoff/maxUnknownRetries/retryBackoffMax directly on the instance —
// setupHookOrch builds the Orchestrator by hand (not via New), so these snapshots default to zero. It also
// widens the critical bus: every EvScheduleRetry/EvResumeAfterRetry transition lands its own
// stage_status_changed notice there (triggerWithSeq), and nothing in these white-box tests drains it as it
// goes — the final EventRetryExhausted/EventAgentCompleted uses the BLOCKING Publish, so an undrained,
// undersized buffer would hang the test on a long retry sequence (MaxRetries full-budget tests).
func withRetryBudgets(o *Orchestrator) {
	o.maxRetries = MaxRetries
	o.maxUnknownRetries = MaxUnknownRetries
	o.retryBackoff = time.Millisecond
	o.retryBackoffMax = 2 * time.Millisecond
	o.critical = bus.NewCriticalBus(256)
}

// completeViaOnAgentCompleted drains the EventAgentCompleted that a successful runWithRetry publishes on the
// critical bus and drives the FSM the rest of the way to done — runWithRetry only publishes the event; in a
// real run, the live event loop (absent here) is what normally consumes it (mirrors
// TestRunWithRetry_CompletionMarkerOverridesStaleOpenQuestion in retry_test.go). Every intermediate retry
// (EvScheduleRetry/EvResumeAfterRetry) also lands its own stage_status_changed notice on the same bus
// (triggerWithSeq), so this drains past those rather than assuming EventAgentCompleted is first in the queue.
func completeViaOnAgentCompleted(t *testing.T, o *Orchestrator, stageID string) {
	t.Helper()
	for {
		select {
		case ev := <-o.critical.Recv():
			if ev.Type != bus.EventAgentCompleted {
				continue
			}
			if err := o.onAgentCompleted(context.Background(), ev); err != nil {
				t.Fatalf("onAgentCompleted: %v", err)
			}
			return
		default:
			t.Fatal("expected EventAgentCompleted on the critical bus after a successful run")
		}
	}
}

func TestRunWithRetry_NetworkErrorRetriesThenSucceeds(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	withRetryBudgets(o)

	agentFn, calls := scriptedAgentFn(func(call int) error {
		if call <= 4 {
			return errors.New("read: connection reset by peer")
		}
		return nil
	})

	o.runWithRetry(context.Background(), flow.Stage{ID: "s1"}, phaseImplementation, agentFn, nil,
		func() { t.Error("onUserInterrupted should not be called") })

	if got := calls(); got != 5 {
		t.Errorf("expected exactly 5 invocations (4 fails + 1 success), got %d", got)
	}
	completeViaOnAgentCompleted(t, o, "s1")
	if got := o.opts.Store.Get("s1"); got != state.StatusDone {
		t.Errorf("status = %v, want done", got)
	}
}

func TestRunWithRetry_UnknownErrorUsesLimitedBudget(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	withRetryBudgets(o)

	agentFn, calls := scriptedAgentFn(func(int) error { return errors.New("exit status 1") })

	o.runWithRetry(context.Background(), flow.Stage{ID: "s1"}, phaseImplementation, agentFn, nil,
		func() { t.Error("onUserInterrupted should not be called") })

	want := MaxUnknownRetries + 1
	if got := calls(); got != want {
		t.Errorf("expected exactly %d invocations (MaxUnknownRetries+1), got %d", want, got)
	}
	if got := o.opts.Store.Get("s1"); got != state.StatusFailed {
		t.Errorf("status = %v, want failed", got)
	}
}

func TestRunWithRetry_KnownTransientUsesFullBudget(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	withRetryBudgets(o)

	agentFn, calls := scriptedAgentFn(func(int) error { return errors.New("rate limit") })

	o.runWithRetry(context.Background(), flow.Stage{ID: "s1"}, phaseImplementation, agentFn, nil,
		func() { t.Error("onUserInterrupted should not be called") })

	want := MaxRetries + 1
	if got := calls(); got != want {
		t.Errorf("expected exactly %d invocations (MaxRetries+1), got %d", want, got)
	}
	if got := o.opts.Store.Get("s1"); got != state.StatusFailed {
		t.Errorf("status = %v, want failed", got)
	}
}

func TestRunWithRetry_StartFailureFailsImmediately(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	withRetryBudgets(o)

	agentFn, calls := scriptedAgentFn(func(int) error {
		return fmt.Errorf("%w: x", executor.ErrAgentStart)
	})

	o.runWithRetry(context.Background(), flow.Stage{ID: "s1"}, phaseImplementation, agentFn, nil,
		func() { t.Error("onUserInterrupted should not be called") })

	if got := calls(); got != 1 {
		t.Errorf("expected exactly 1 invocation (fatal, no retry), got %d", got)
	}
	if got := o.opts.Store.Get("s1"); got != state.StatusFailed {
		t.Errorf("status = %v, want failed", got)
	}
}

func TestRunWithRetry_MixedUnknownThenNetwork(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	withRetryBudgets(o)

	agentFn, calls := scriptedAgentFn(func(call int) error {
		switch {
		case call <= 3:
			return errors.New("exit status 1") // ClassRetryableUnknown
		case call <= 13:
			return errors.New("connection reset by peer") // ClassRetryable
		default:
			return nil
		}
	})

	o.runWithRetry(context.Background(), flow.Stage{ID: "s1"}, phaseImplementation, agentFn, nil,
		func() { t.Error("onUserInterrupted should not be called") })

	if got := calls(); got != 14 {
		t.Errorf("expected exactly 14 invocations (3 unknown + 10 network + 1 success), got %d", got)
	}
	completeViaOnAgentCompleted(t, o, "s1")
	if got := o.opts.Store.Get("s1"); got != state.StatusDone {
		t.Errorf("status = %v, want done — exhausting the unknown budget must not touch the network budget", got)
	}
}

// TestRunWithRetry_UnknownErrorOnLastAllowedAttemptStillFails is a regression for a bug found in review: the
// outer `for attempt <= maxRetries` loop can silently exit (no EvFail, no EventRetryExhausted) if the LAST
// allowed attempt fails with an unrecognized (ClassRetryableUnknown) error whose OWN budget isn't exhausted
// yet — the loop's attempt bound must gate that decision too, or the stage is left parked in `running`
// forever with zero diagnostics. Here MaxRetries (15) ClassRetryable failures consume the whole outer budget,
// then the 16th (last allowed) call fails unrecognized — unknownRetries=1 is well within MaxUnknownRetries,
// but attempt has already reached maxRetries, so the stage must still fail cleanly.
func TestRunWithRetry_UnknownErrorOnLastAllowedAttemptStillFails(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	withRetryBudgets(o)

	agentFn, calls := scriptedAgentFn(func(call int) error {
		if call <= MaxRetries {
			return errors.New("connection reset by peer") // ClassRetryable
		}
		return errors.New("exit status 1") // ClassRetryableUnknown, on the last allowed attempt
	})

	done := make(chan struct{})
	go func() {
		o.runWithRetry(context.Background(), flow.Stage{ID: "s1"}, phaseImplementation, agentFn, nil,
			func() { t.Error("onUserInterrupted should not be called") })
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runWithRetry did not return — stage is hung instead of failing")
	}

	want := MaxRetries + 1
	if got := calls(); got != want {
		t.Errorf("expected exactly %d invocations (MaxRetries+1), got %d", want, got)
	}
	if got := o.opts.Store.Get("s1"); got != state.StatusFailed {
		t.Errorf("status = %v, want failed — must not be left hanging in running/retrying", got)
	}
}

func TestRunWithRetry_NoticeShowsActualDelay(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	withRetryBudgets(o)

	agentFn, _ := scriptedAgentFn(func(call int) error {
		if call <= 3 {
			return errors.New("connection reset by peer")
		}
		return nil
	})

	subID, events := o.ui.Subscribe(64)
	defer o.ui.Unsubscribe(subID)

	o.runWithRetry(context.Background(), flow.Stage{ID: "s1"}, phaseImplementation, agentFn, nil,
		func() { t.Error("onUserInterrupted should not be called") })

	var msgs []string
drain:
	for {
		select {
		case ev := <-events:
			if ev.Type == bus.EventRetryScheduled {
				if s, ok := ev.Data.(string); ok {
					msgs = append(msgs, s)
				}
			}
		default:
			break drain
		}
	}

	if len(msgs) < 2 {
		t.Fatalf("expected at least 2 EventRetryScheduled notices, got %d: %v", len(msgs), msgs)
	}
	// Сам msg — "attempt %d/%d in %v": ведущий "attempt N/M" меняется каждую попытку независимо от задержки
	// (numerator растёт на 1), так что сравнение ЦЕЛЫХ сообщений всегда проходит и ничего не проверяет про
	// рост паузы — нужно сравнивать именно хвост после "in ".
	//
	// Сравнение СТРОК хвоста тоже недостаточно: jittered() вычитает СЛУЧАЙНУЮ долю (0..20%), поэтому даже
	// плоский (не растущий) backoff почти всегда даёт две РАЗНЫЕ по тексту паузы — просто из-за джиттера, а
	// не из-за роста. Чтобы отличить "растёт" от "плоский + шум джиттера" детерминированно, используем то,
	// что withRetryBudgets фиксирует retryBackoff=1ms/retryBackoffMax=2ms: диапазон attempt=0 — (0.8ms,1ms],
	// диапазон attempt=1 — (1.6ms,2ms] (jittered режет максимум 20% от удвоенного потолка) — эти диапазоны НЕ
	// пересекаются. Поэтому сравниваем числовые значения с порогом, а не текст.
	parseDelay := func(msg string) time.Duration {
		idx := strings.LastIndex(msg, " in ")
		if idx == -1 {
			t.Fatalf("notice message missing ' in <delay>' suffix: %q", msg)
		}
		d, err := time.ParseDuration(msg[idx+len(" in "):])
		if err != nil {
			t.Fatalf("notice delay %q did not parse as a duration: %v", msg[idx+len(" in "):], err)
		}
		return d
	}
	first, second := parseDelay(msgs[0]), parseDelay(msgs[1])
	const firstAttemptCeiling = time.Millisecond // attempt 0's maximum possible jittered value (see above)
	if first > firstAttemptCeiling {
		t.Fatalf("1st delay %v exceeds the expected attempt-0 ceiling %v — test assumptions about withRetryBudgets changed?", first, firstAttemptCeiling)
	}
	if second <= firstAttemptCeiling {
		t.Errorf("2nd delay %v did not grow past attempt-0's ceiling %v — backoff must grow, not stay flat", second, firstAttemptCeiling)
	}
}

func TestRunWithRetry_InterruptCheckedBeforeClassification(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	withRetryBudgets(o)

	agentFn, calls := scriptedAgentFn(func(int) error { return executor.ErrUserInterrupted })

	var interruptedCalled bool
	o.runWithRetry(context.Background(), flow.Stage{ID: "s1"}, phaseImplementation, agentFn, nil,
		func() { interruptedCalled = true })

	if !interruptedCalled {
		t.Fatal("expected onUserInterrupted to fire — ErrUserInterrupted must be checked BEFORE Classify(err)")
	}
	if got := calls(); got != 1 {
		t.Errorf("expected exactly 1 invocation, got %d", got)
	}
	if got := o.opts.Store.Get("s1"); got == state.StatusRetrying {
		t.Error("interrupted stage must not transition to retrying")
	}
}

// TestRunWithRetry_PauseDuringBackoffStillWorks is a T5 regression for the existing interruptCh-inside-
// backoff-select behavior (mirrors TestPause_RetryingStage_CancelsBackoffImmediately in
// pause_continue_test.go, white-box here): a Pause landing while a retry is sleeping in its backoff window
// must still interrupt it promptly, even though T5 replaced the flat retryBackoff sleep with a
// jittered/growing delay.
func TestRunWithRetry_PauseDuringBackoffStillWorks(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	// A fake clock that is NEVER Advanced, overriding setupHookOrch's pumped default: its backoff timer
	// cannot fire on its own, so the stage reaching paused PROVES Pause (interruptCh) ended the wait, not
	// the timer — deterministic by construction, not "30s is probably longer than the test takes" (T8).
	o.clock = clock.NewFake(time.Unix(0, 0))
	o.maxRetries = MaxRetries
	o.maxUnknownRetries = MaxUnknownRetries
	o.retryBackoff = 30 * time.Second
	o.retryBackoffMax = 60 * time.Second

	agentFn, calls := scriptedAgentFn(func(int) error { return errors.New("connection reset by peer") })

	done := make(chan struct{})
	go func() {
		o.runWithRetry(context.Background(), flow.Stage{ID: "s1"}, phaseImplementation, agentFn, nil,
			func() { t.Error("onUserInterrupted should not be called — Pause already recorded paused durably") })
		close(done)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for o.opts.Store.Get("s1") != state.StatusRetrying {
		if time.Now().After(deadline) {
			t.Fatal("stage did not reach retrying before timeout")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := o.Pause(context.Background(), "s1"); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("runWithRetry did not return promptly after Pause — the 30s backoff wait was not interrupted")
	}

	if got := o.opts.Store.Get("s1"); got != state.StatusPaused {
		t.Errorf("status = %v, want paused", got)
	}
	if got := calls(); got != 1 {
		t.Errorf("expected exactly 1 invocation before pause landed, got %d", got)
	}
}
