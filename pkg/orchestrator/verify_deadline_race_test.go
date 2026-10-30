package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
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

	assertVerifyDeadlineRace(t, o, err, 1, verifyKindAgent, "codex")
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

	assertVerifyDeadlineRace(t, o, err, 1, verifyKindAgent, "codex")
}

func TestRunVerification_DeadlineRaceMasksPass(t *testing.T) {
	for _, kind := range []flow.VerifyKind{flow.VerifyAgent, flow.VerifyShell} {
		step := flow.VerifyStep{Kind: kind, Command: "codex", Run: "check", Timeout: 20 * time.Millisecond}
		t.Run(verifyStepManifestKind(step), func(t *testing.T) {
			stage := flow.Stage{ID: verifyTestStageID, Verify: flow.VerifySpec{Steps: []flow.VerifyStep{
				{Kind: flow.VerifyShell, Run: "first"}, step,
			}}}
			o, _ := newVerifyTestOrchestrator(t, stage)
			o.runVerifyShell = func(ctx context.Context, _, command string) (string, error) {
				if command == "check" {
					<-ctx.Done()
				}
				return "ok", nil
			}
			o.runVerifyAgent = func(ctx context.Context, _ flow.Stage, _, _, _, _ string) (verify.RunOutcome, error) {
				<-ctx.Done()
				return verify.RunOutcome{ProcessOK: true, Result: &verify.ModelResult{
					SchemaVersion: verify.SupportedSchemaVersion, Verdict: verify.VerdictPass, Summary: "late pass",
				}}, nil
			}
			err := o.RunVerification(context.Background(), stage, phaseImplementation)
			assertVerifyDeadlineRace(t, o, err, 2, verifyStepManifestKind(step), verifyStepManifestCommand(step))
		})
	}
}

func assertVerifyDeadlineRace(t *testing.T, o *Orchestrator, err error, step int, kind, command string) {
	t.Helper()
	var execErr *VerifyExecError
	if !errors.As(err, &execErr) {
		t.Fatalf("expected *VerifyExecError, got %v (%T)", err, err)
	}
	if execErr.Reason != verifyTimedOutReason || execErr.Step != step || execErr.Transient {
		t.Fatalf("deadline error = %+v, want reason %q, step %d, non-transient", execErr, verifyTimedOutReason, step)
	}
	want := fmt.Sprintf("verify execution failed at step %d: %s", step, verifyTimedOutReason)
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
	results := noticesOfType(readVerifyNotices(t, o.opts.RunDir), bus.EventVerifyResult)
	if len(results) != step {
		t.Fatalf("result count = %d, want %d", len(results), step)
	}
	result := results[step-1].Data
	if result[keyVerifyReason] != want || result[keyExecErrorKind] != execErrorKindExecFailure ||
		result[keyStep] != float64(step) || result[keyVerifyKind] != kind || result[keyVerifyCommand] != command {
		t.Errorf("deadline notice = %+v", result)
	}
	if _, ok := result[keyVerdict]; ok {
		t.Error("deadline notice must not carry a verdict")
	}
	manifest := readManifest(t, filepath.Join(o.opts.RunDir, verifyTestStageID), result[keyVerificationID].(string))
	if manifest.Steps[step-1].Outcome != verifyOutcomeError {
		t.Errorf("deadline manifest = %+v", manifest.Steps)
	}
}
