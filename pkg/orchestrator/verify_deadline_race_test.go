package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/verify"
)

func TestRunVerification_AgentStepDeadlineExpiredMasksNeedsChangesAsTimeout(t *testing.T) {
	stage := flow.Stage{
		ID: verifyTestStageID,
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{
			{Kind: flow.VerifyAgent, Command: "codex", Timeout: 20 * time.Millisecond},
		}},
	}
	o, _ := newVerifyTestOrchestrator(t, stage)
	o.runVerifyAgent = func(ctx context.Context, _ flow.Stage, _, _, _, _ string) (verify.RunOutcome, error) {
		<-ctx.Done() // дождаться истечения st.Timeout — гонка воспроизведена детерминированно
		return verify.RunOutcome{ProcessOK: true, Result: &verify.ModelResult{
			SchemaVersion: verify.SupportedSchemaVersion,
			Verdict:       verify.VerdictNeedsChanges,
			Summary:       "found a bug after the deadline",
		}}, nil
	}

	err := o.RunVerification(context.Background(), stage, phaseImplementation)

	var execErr *VerifyExecError
	if !errors.As(err, &execErr) {
		t.Fatalf("expected *VerifyExecError (timeout), got %v (%T)", err, err)
	}
	if !strings.Contains(execErr.Error(), verifyTimedOutReason) {
		t.Errorf("expected timeout reason %q in error, got %q", verifyTimedOutReason, execErr.Error())
	}
	var rejected *VerifyRejectedError
	if errors.As(err, &rejected) {
		t.Fatal("an expired deadline must never be masked as a needs_changes rejection (no free author retry for a stale verdict)")
	}
}

func TestRunVerification_AgentStepDeadlineExpiredMasksInconclusiveAsTimeout(t *testing.T) {
	stage := flow.Stage{
		ID: verifyTestStageID,
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{
			{Kind: flow.VerifyAgent, Command: "codex", Timeout: 20 * time.Millisecond},
		}},
	}
	o, _ := newVerifyTestOrchestrator(t, stage)
	o.runVerifyAgent = func(ctx context.Context, _ flow.Stage, _, _, _, _ string) (verify.RunOutcome, error) {
		<-ctx.Done()
		return verify.RunOutcome{ProcessOK: true, Result: &verify.ModelResult{
			SchemaVersion: verify.SupportedSchemaVersion,
			Verdict:       verify.VerdictInconclusive,
			Summary:       "unsure after the deadline",
		}}, nil
	}

	err := o.RunVerification(context.Background(), stage, phaseImplementation)

	var execErr *VerifyExecError
	if !errors.As(err, &execErr) {
		t.Fatalf("expected *VerifyExecError (timeout), got %v (%T)", err, err)
	}
	if !strings.Contains(execErr.Error(), verifyTimedOutReason) {
		t.Errorf("expected timeout reason %q in error, got %q", verifyTimedOutReason, execErr.Error())
	}
}
