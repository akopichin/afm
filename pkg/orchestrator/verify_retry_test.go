package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/state"
)

// TestRunWithRetry_VerifyRejected_OneFreeRetryThenFailsWithDiagnosis — V4b.3:
// VerifyRejectedError embeds *stagefiles.IncompleteWorkError (V4a), so it
// routes through the EXISTING incomplete-retry policy without any new code
// path — attempt 0 gets one free retry (no backoff), the second rejection
// fails the stage. The failure reason must preserve the verify diagnosis
// (report id / step), not the old generic "missing artifact or incomplete".
func TestRunWithRetry_VerifyRejected_OneFreeRetryThenFailsWithDiagnosis(t *testing.T) {
	o, runDir := setupHookOrch(t, "s1")
	o.maxRetries = 15

	rejected := &VerifyRejectedError{
		IncompleteWorkError: &stagefiles.IncompleteWorkError{Reason: "verify needs changes (step 1): missing tests"},
		ReportID:            "ver123",
		Step:                1,
	}

	var attempts int
	var secondRetryContext string
	o.runWithRetry(context.Background(), flow.Stage{ID: "s1"}, phaseImplementation,
		func(retryContext string) error {
			attempts++
			if attempts == 2 {
				secondRetryContext = retryContext
			}
			return nil
		},
		func() error { return rejected },
		func() { t.Error("onUserInterrupted should not be called") },
	)

	if attempts != 2 {
		t.Fatalf("expected exactly 2 attempts (one free incomplete-retry), got %d", attempts)
	}
	if !strings.Contains(secondRetryContext, "ver123") {
		t.Errorf("correction attempt's retryContext should carry the verify report id, got %q", secondRetryContext)
	}
	if !strings.Contains(secondRetryContext, "missing tests") {
		t.Errorf("correction attempt's retryContext should carry the blocking reason, got %q", secondRetryContext)
	}

	if got := o.opts.Store.Get("s1"); got != state.StatusFailed {
		t.Fatalf("status = %v, want failed after exhausting the one free retry", got)
	}

	data, err := os.ReadFile(filepath.Join(runDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "ver123") {
		t.Errorf("EvFail reason should preserve the verify report id, not a generic message; log: %s", data)
	}
}

// TestRunWithRetry_VerifyExecError_NoRetryFailsImmediatelyWithDiagnosis —
// V4b.3: VerifyExecError (transport/protocol/timeout/inconclusive/storage
// failure of the VERIFIER itself) is NOT an IncompleteWorkError — re-running
// the author cannot fix a verification that never produced a verdict, so it
// must fail on attempt 0 without ever calling agentFn a second time, and the
// failure reason must still carry the verify diagnosis.
func TestRunWithRetry_VerifyExecError_NoRetryFailsImmediatelyWithDiagnosis(t *testing.T) {
	o, runDir := setupHookOrch(t, "s1")
	o.maxRetries = 15

	execErr := &VerifyExecError{Reason: "verify timed out", Step: 2}

	var attempts int
	o.runWithRetry(context.Background(), flow.Stage{ID: "s1"}, phaseImplementation,
		func(retryContext string) error { attempts++; return nil },
		func() error { return execErr },
		func() { t.Error("onUserInterrupted should not be called") },
	)

	if attempts != 1 {
		t.Fatalf("author must NOT be re-run on VerifyExecError, got %d attempts", attempts)
	}
	if stagefiles.IsIncompleteWorkError(execErr) {
		t.Fatal("sanity: VerifyExecError must not classify as incomplete work")
	}
	if got := o.opts.Store.Get("s1"); got != state.StatusFailed {
		t.Fatalf("status = %v, want failed", got)
	}

	data, err := os.ReadFile(filepath.Join(runDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "verify execution failed at step 2") {
		t.Errorf("EvFail reason should preserve the verify diagnosis, got log: %s", data)
	}
	if !strings.Contains(string(data), "verify timed out") {
		t.Errorf("EvFail reason should preserve the verify reason text, got log: %s", data)
	}
}
