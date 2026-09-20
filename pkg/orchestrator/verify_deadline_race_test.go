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

// TestRunVerification_AgentStepDeadlineExpiredMasksNeedsChangesAsTimeout — F3
// (4-е код-ревью): раньше stepCtx.Err() (DeadlineExceeded) проверялся ТОЛЬКО
// внутри ветки VerdictPass (verifyStepDeadlineRace) — needs_changes/
// inconclusive принимались как есть, даже если st.Timeout уже истёк к
// моменту получения результата (subprocess completion и ctx.Done() могут
// стать готовы одновременно). Итог должен быть VerifyExecError с меткой
// таймаута, а не VerifyRejectedError/бесплатный author-retry — иначе поздний
// результат, пришедший ПОСЛЕ дедлайна, мог триггернуть коррекцию автора по
// фактически просроченной проверке.
//
// Фейковый o.runVerifyAgent дожидается <-ctx.Done() (истечения st.Timeout)
// ПЕРЕД тем как вернуть needs_changes — воспроизводит гонку детерминированно,
// без polling на реальном времени выполнения executor'а.
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

// TestRunVerification_AgentStepDeadlineExpiredMasksInconclusiveAsTimeout —
// то же самое (F3), но для вердикта inconclusive — вторая ветка switch'а,
// которая раньше тоже не перепроверяла дедлайн.
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
