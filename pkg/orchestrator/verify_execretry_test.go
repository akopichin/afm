package orchestrator

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/verify"
)

// shrinkVerifyBackoff makes the retry backoff negligible for the duration of a test so the
// bounded retry loop runs fast; it restores the production value on cleanup.
func shrinkVerifyBackoff(t *testing.T) {
	t.Helper()
	prev := verifyExecBackoff
	verifyExecBackoff = time.Millisecond
	t.Cleanup(func() { verifyExecBackoff = prev })
}

func execRetryStage() flow.Stage {
	return flow.Stage{ID: verifyTestStageID,
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "codex"}}}}
}

func passOutcome() verify.RunOutcome {
	return verify.RunOutcome{ProcessOK: true, Result: &verify.ModelResult{
		SchemaVersion: verify.SupportedSchemaVersion, Verdict: verify.VerdictPass, Summary: "all good",
	}}
}

// Test 1: a transient exec-failure (ProcessOK:false, nil err) that recovers within budget → stage passes;
// notices carry one verify_retry per failed attempt and a final passing verify_result.
func TestVerifyExecRetry_TransientThenPass(t *testing.T) {
	shrinkVerifyBackoff(t)
	stage := execRetryStage()
	o, _ := newVerifyTestOrchestrator(t, stage)

	calls := 0
	o.runVerifyAgent = func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
		calls++
		if calls <= 2 { // two transient failures, then a pass
			return verify.RunOutcome{ProcessOK: false}, nil
		}
		return passOutcome(), nil
	}

	if err := o.RunVerification(context.Background(), stage, phaseImplementation); err != nil {
		t.Fatalf("RunVerification: %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 runVerifyAgent calls (2 transient + 1 pass), got %d", calls)
	}

	notices := readVerifyNotices(t, o.opts.RunDir)
	retries := noticesOfType(notices, bus.EventVerifyRetry)
	if len(retries) != 2 {
		t.Fatalf("expected 2 verify_retry notices, got %d", len(retries))
	}
	results := noticesOfType(notices, bus.EventVerifyResult)
	if len(results) != 1 {
		t.Fatalf("expected 1 verify_result notice, got %d", len(results))
	}
	if results[0].Data["verdict"] != "pass" {
		t.Errorf("verdict = %v, want pass", results[0].Data["verdict"])
	}
}

// Test 2: a transient exec-failure that never recovers → terminal *VerifyExecError, exactly
// verifyExecMaxRetries verify_retry notices and a final exec_error verify_result.
func TestVerifyExecRetry_TransientExceedsBudget(t *testing.T) {
	shrinkVerifyBackoff(t)
	stage := execRetryStage()
	o, _ := newVerifyTestOrchestrator(t, stage)

	calls := 0
	o.runVerifyAgent = func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
		calls++
		return verify.RunOutcome{ProcessOK: false}, nil
	}

	err := o.RunVerification(context.Background(), stage, phaseImplementation)
	var execErr *VerifyExecError
	if !errors.As(err, &execErr) {
		t.Fatalf("expected *VerifyExecError, got %v (%T)", err, err)
	}
	if calls != verifyExecMaxRetries+1 {
		t.Fatalf("expected %d calls (initial + %d retries), got %d", verifyExecMaxRetries+1, verifyExecMaxRetries, calls)
	}

	notices := readVerifyNotices(t, o.opts.RunDir)
	if retries := noticesOfType(notices, bus.EventVerifyRetry); len(retries) != verifyExecMaxRetries {
		t.Fatalf("expected %d verify_retry notices, got %d", verifyExecMaxRetries, len(retries))
	}
	results := noticesOfType(notices, bus.EventVerifyResult)
	if len(results) != 1 {
		t.Fatalf("expected 1 verify_result notice, got %d", len(results))
	}
	if results[0].Data["exec_error"] != execErrorKindExecFailure {
		t.Errorf("exec_error = %v, want %q", results[0].Data["exec_error"], execErrorKindExecFailure)
	}
}

// Test 3: an interruption on the first call is never retried.
func TestVerifyExecRetry_InterruptedNoRetry(t *testing.T) {
	shrinkVerifyBackoff(t)
	stage := execRetryStage()
	o, _ := newVerifyTestOrchestrator(t, stage)

	calls := 0
	o.runVerifyAgent = func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
		calls++
		return verify.RunOutcome{Interrupted: true}, nil
	}

	err := o.RunVerification(context.Background(), stage, phaseImplementation)
	if !errors.Is(err, executor.ErrUserInterrupted) {
		t.Fatalf("expected executor.ErrUserInterrupted, got %v (%T)", err, err)
	}
	if calls != 1 {
		t.Fatalf("expected exactly 1 call (no retry on interrupt), got %d", calls)
	}
	if retries := noticesOfType(readVerifyNotices(t, o.opts.RunDir), bus.EventVerifyRetry); len(retries) != 0 {
		t.Fatalf("expected 0 verify_retry notices on interrupt, got %d", len(retries))
	}
}

// Test 4: a generic (non-nil) error from runVerifyAgent is a deterministic executor-storage failure,
// NOT transient — it must never retry. Regression for the earlier codex-round defect.
func TestVerifyExecRetry_GenericErrorNoRetry(t *testing.T) {
	shrinkVerifyBackoff(t)
	stage := execRetryStage()
	o, _ := newVerifyTestOrchestrator(t, stage)

	calls := 0
	o.runVerifyAgent = func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
		calls++
		return verify.RunOutcome{}, errors.New("NewLogger: disk full")
	}

	err := o.RunVerification(context.Background(), stage, phaseImplementation)
	var execErr *VerifyExecError
	if !errors.As(err, &execErr) {
		t.Fatalf("expected *VerifyExecError, got %v (%T)", err, err)
	}
	if calls != 1 {
		t.Fatalf("expected exactly 1 call (generic error is not transient), got %d", calls)
	}
	if retries := noticesOfType(readVerifyNotices(t, o.opts.RunDir), bus.EventVerifyRetry); len(retries) != 0 {
		t.Fatalf("expected 0 verify_retry notices, got %d", len(retries))
	}
}

// Test 5: ProcessOK:false with TimedOut:true is a timeout, not transient infra — no retry.
func TestVerifyExecRetry_TimedOutNoRetry(t *testing.T) {
	shrinkVerifyBackoff(t)
	stage := execRetryStage()
	o, _ := newVerifyTestOrchestrator(t, stage)

	calls := 0
	o.runVerifyAgent = func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
		calls++
		return verify.RunOutcome{ProcessOK: false, TimedOut: true}, nil
	}

	err := o.RunVerification(context.Background(), stage, phaseImplementation)
	var execErr *VerifyExecError
	if !errors.As(err, &execErr) {
		t.Fatalf("expected *VerifyExecError, got %v (%T)", err, err)
	}
	if calls != 1 {
		t.Fatalf("expected exactly 1 call (timeout is not transient), got %d", calls)
	}
	if retries := noticesOfType(readVerifyNotices(t, o.opts.RunDir), bus.EventVerifyRetry); len(retries) != 0 {
		t.Fatalf("expected 0 verify_retry notices, got %d", len(retries))
	}
}

// Test 5b: interruption DURING the backoff wait (after a transient failure) must return
// executor.ErrUserInterrupted, never a terminal exec_failure — a reliability-core invariant (Pause/Revise/
// cancel must not be recorded as a bad terminal state). The interrupt is pre-buffered so it is observed in
// the backoff select; a large backoff keeps the win deterministic (the interrupt cancels near-instantly, so
// the timer never fires and the test does not actually wait).
func TestVerifyExecRetry_InterruptDuringBackoff(t *testing.T) {
	prev := verifyExecBackoff
	verifyExecBackoff = time.Hour
	t.Cleanup(func() { verifyExecBackoff = prev })

	stage := execRetryStage()
	o, _ := newVerifyTestOrchestrator(t, stage)

	interruptCh := make(chan struct{}, 1)
	interruptCh <- struct{}{} // сигнал лежит в канале ДО входа в backoff-select
	o.interruptChans.Store(stage.ID, interruptCh)

	calls := 0
	o.runVerifyAgent = func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
		calls++
		return verify.RunOutcome{ProcessOK: false}, nil // транзиентный сбой → уход в backoff
	}

	err := o.RunVerification(context.Background(), stage, phaseImplementation)
	if !errors.Is(err, executor.ErrUserInterrupted) {
		t.Fatalf("expected executor.ErrUserInterrupted, got %v (%T)", err, err)
	}
	if calls != 1 {
		t.Fatalf("expected exactly 1 call (interrupt during first backoff, no second run), got %d", calls)
	}
	notices := readVerifyNotices(t, o.opts.RunDir)
	if retries := noticesOfType(notices, bus.EventVerifyRetry); len(retries) != 1 {
		t.Fatalf("expected exactly 1 verify_retry notice (fired before backoff), got %d", len(retries))
	}
	results := noticesOfType(notices, bus.EventVerifyResult)
	if len(results) != 1 {
		t.Fatalf("expected 1 verify_result notice, got %d", len(results))
	}
	if results[0].Data["exec_error"] != execErrorKindInterrupted {
		t.Errorf("exec_error = %v, want interrupted (not exec_failure)", results[0].Data["exec_error"])
	}
}

// Test 5c: a step timeout (st.Timeout) that expires DURING the backoff wait must terminate as a verify
// exec timeout, NOT as a user interrupt — otherwise runWithRetry would re-spawn the author on
// ErrUserInterrupted and a deterministic verify failure would livelock the stage. No interruptCh is stored,
// so the only thing that can cut the backoff short is the step deadline; the backoff is large so the
// deadline reliably wins.
func TestVerifyExecRetry_StepTimeoutDuringBackoffNotInterrupt(t *testing.T) {
	prev := verifyExecBackoff
	verifyExecBackoff = 30 * time.Second
	t.Cleanup(func() { verifyExecBackoff = prev })

	stage := flow.Stage{ID: verifyTestStageID,
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "codex", Timeout: 80 * time.Millisecond}}}}
	o, _ := newVerifyTestOrchestrator(t, stage)

	calls := 0
	o.runVerifyAgent = func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
		calls++
		return verify.RunOutcome{ProcessOK: false}, nil // транзиентный сбой → backoff, который переживёт st.Timeout
	}

	err := o.RunVerification(context.Background(), stage, phaseImplementation)
	if errors.Is(err, executor.ErrUserInterrupted) {
		t.Fatalf("step timeout during backoff must NOT be ErrUserInterrupted (would re-spawn the author), got %v", err)
	}
	var execErr *VerifyExecError
	if !errors.As(err, &execErr) {
		t.Fatalf("expected *VerifyExecError (timeout), got %v (%T)", err, err)
	}
	if calls != 1 {
		t.Fatalf("expected exactly 1 call (deadline fires during the first backoff), got %d", calls)
	}
	results := noticesOfType(readVerifyNotices(t, o.opts.RunDir), bus.EventVerifyResult)
	if len(results) != 1 || results[0].Data["exec_error"] != execErrorKindExecFailure {
		t.Fatalf("expected a single exec_failure verify_result, got %+v", results)
	}
}

// Test 6a: Fix 1b — on an exec_failure the tail of agent.stderr.log is mixed into the returned error and
// the verify_result reason. The fake writes the stderr sibling of its logFile arg.
func TestVerifyExecRetry_EnrichesReasonFromStderr(t *testing.T) {
	shrinkVerifyBackoff(t)
	stage := execRetryStage()
	o, _ := newVerifyTestOrchestrator(t, stage)

	o.runVerifyAgent = func(_ context.Context, _ flow.Stage, _, _, logFile, _ string) (verify.RunOutcome, error) {
		stderrPath := strings.TrimSuffix(logFile, ".log") + ".stderr.log"
		if err := os.WriteFile(stderrPath, []byte("boom from codex"), 0644); err != nil {
			t.Fatalf("write stderr: %v", err)
		}
		return verify.RunOutcome{}, errors.New("exec failed")
	}

	err := o.RunVerification(context.Background(), stage, phaseImplementation)
	if err == nil || !strings.Contains(err.Error(), "boom from codex") {
		t.Fatalf("expected enriched error mentioning stderr tail, got %v", err)
	}
	results := noticesOfType(readVerifyNotices(t, o.opts.RunDir), bus.EventVerifyResult)
	if len(results) != 1 {
		t.Fatalf("expected 1 verify_result notice, got %d", len(results))
	}
	if reason, _ := results[0].Data["reason"].(string); !strings.Contains(reason, "boom from codex") {
		t.Errorf("expected verify_result reason to carry stderr tail, got %q", reason)
	}
}

// Test 6b: on an interruption the stderr tail is NOT mixed in — interrupted reasons stay clean.
func TestVerifyExecRetry_InterruptedNotEnriched(t *testing.T) {
	shrinkVerifyBackoff(t)
	stage := execRetryStage()
	o, _ := newVerifyTestOrchestrator(t, stage)

	o.runVerifyAgent = func(_ context.Context, _ flow.Stage, _, _, logFile, _ string) (verify.RunOutcome, error) {
		stderrPath := strings.TrimSuffix(logFile, ".log") + ".stderr.log"
		_ = os.WriteFile(stderrPath, []byte("boom from codex"), 0644)
		return verify.RunOutcome{Interrupted: true}, nil
	}

	_ = o.RunVerification(context.Background(), stage, phaseImplementation)
	results := noticesOfType(readVerifyNotices(t, o.opts.RunDir), bus.EventVerifyResult)
	if len(results) != 1 {
		t.Fatalf("expected 1 verify_result notice, got %d", len(results))
	}
	if results[0].Data["exec_error"] != execErrorKindInterrupted {
		t.Errorf("exec_error = %v, want interrupted", results[0].Data["exec_error"])
	}
	if reason, _ := results[0].Data["reason"].(string); strings.Contains(reason, "boom from codex") {
		t.Errorf("interrupted reason must not be enriched with stderr, got %q", reason)
	}
}
