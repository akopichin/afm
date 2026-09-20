package orchestrator

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/orchestrator/verify"
)

// TestVerifyStepDeadlineRace_ExpiredDeadlineNeverPasses — unit-тест чистого
// хелпера: гонка между успешным исходом шага и истечением его собственного
// дедлайна (D3a код-ревью) НИКОГДА не должна дать доверять успеху, если
// stepCtx уже показывает DeadlineExceeded.
func TestVerifyStepDeadlineRace_ExpiredDeadlineNeverPasses(t *testing.T) {
	t.Run("expired deadline -> VerifyExecError", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
		defer cancel()
		time.Sleep(5 * time.Millisecond) // гарантированно истёк

		err := verifyStepDeadlineRace(ctx, 3)
		var execErr *VerifyExecError
		if !errors.As(err, &execErr) {
			t.Fatalf("expected *VerifyExecError, got %v (%T)", err, err)
		}
		if execErr.Step != 3 {
			t.Errorf("Step = %d, want 3", execErr.Step)
		}
	})

	t.Run("live context -> nil", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := verifyStepDeadlineRace(ctx, 1); err != nil {
			t.Errorf("expected nil for a live (non-expired) context, got %v", err)
		}
	})
}

// TestRunVerification_AgentPassRacesExpiredStepTimeout_NeverPasses — D3a
// код-ревью: фейковый agent-шаг НАМЕРЕННО игнорирует ctx и "завершается" уже
// ПОСЛЕ истечения st.Timeout, но возвращает честный успех (ProcessOK=true,
// Verdict=pass) — воспроизводит гонку select'а внутри реального executor'а
// (успех и истёкший дедлайн готовы одновременно). Движок обязан перепроверить
// stepCtx.Err() и НИКОГДА не засчитать такой "успех" как pass.
func TestRunVerification_AgentPassRacesExpiredStepTimeout_NeverPasses(t *testing.T) {
	stage := flow.Stage{
		ID: verifyTestStageID,
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{
			{Kind: flow.VerifyAgent, Command: "codex", Timeout: 10 * time.Millisecond},
		}},
	}
	o, stageDir := newVerifyTestOrchestrator(t, stage)
	o.runVerifyAgent = func(ctx context.Context, _ flow.Stage, _, _, _, _ string) (verify.RunOutcome, error) {
		// Дедлайн шага истечёт задолго до этого возврата — но фейк
		// сообщает штатный успех, как это может сделать реальный
		// executor, если select проиграл гонку (см. D3a).
		time.Sleep(50 * time.Millisecond)
		return verify.RunOutcome{ProcessOK: true, Result: &verify.ModelResult{
			SchemaVersion: verify.SupportedSchemaVersion,
			Verdict:       verify.VerdictPass,
			Summary:       "raced pass",
		}}, nil
	}

	err := o.RunVerification(context.Background(), stage, phaseImplementation)

	var execErr *VerifyExecError
	if !errors.As(err, &execErr) {
		t.Fatalf("expected *VerifyExecError (timeout, never pass), got %v (%T)", err, err)
	}
	var rejected *VerifyRejectedError
	if errors.As(err, &rejected) {
		t.Error("a raced timeout must never be classified as needs_changes (VerifyRejectedError)")
	}
	matches, globErr := filepath.Glob(filepath.Join(stagefiles.VerifyDir(stageDir), "*", "step-*", "result.json"))
	if globErr != nil {
		t.Fatalf("glob accepted results: %v", globErr)
	}
	if len(matches) != 0 {
		t.Errorf("no accepted result should have been persisted for a raced timeout, found: %v", matches)
	}
}

// TestRunVerification_ShellPassRacesExpiredStepTimeout_NeverPasses —
// зеркало предыдущего теста для shell-шага, через инъектируемый
// o.runVerifyShell (production runVerifyShellCommand заменяется фейком,
// который детерминированно воспроизводит ту же гонку select'а).
func TestRunVerification_ShellPassRacesExpiredStepTimeout_NeverPasses(t *testing.T) {
	stage := flow.Stage{
		ID: verifyTestStageID,
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{
			{Kind: flow.VerifyShell, Run: "true", Timeout: 10 * time.Millisecond},
		}},
	}
	o, _ := newVerifyTestOrchestrator(t, stage)
	o.runVerifyShell = func(ctx context.Context, dir, command string) (string, error) {
		time.Sleep(50 * time.Millisecond) // дедлайн шага истечёт до возврата
		return "", nil                    // нулевой exit — честный успех
	}

	err := o.RunVerification(context.Background(), stage, phaseImplementation)

	var execErr *VerifyExecError
	if !errors.As(err, &execErr) {
		t.Fatalf("expected *VerifyExecError (timeout, never pass), got %v (%T)", err, err)
	}
	var rejected *VerifyRejectedError
	if errors.As(err, &rejected) {
		t.Error("a raced timeout must never be classified as needs_changes (VerifyRejectedError)")
	}
}
