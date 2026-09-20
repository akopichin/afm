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

// TestRunVerification_ShellStepTimeoutIsExecErrorNotRejection — C3 код-ревью:
// flow.VerifyStep.Timeout парсился и валидировался, но никогда не
// применялся. Зависший shell-шаг (`sleep 30`) с timeout: 1s должен вернуть
// *VerifyExecError с текстом "timed out" БЫСТРО (в пределах таймаута
// шага, а не 30 секунд) — и НИКОГДА не как needs_changes-эквивалент
// (*VerifyRejectedError), несмотря на ненулевой exit убитого процесса.
func TestRunVerification_ShellStepTimeoutIsExecErrorNotRejection(t *testing.T) {
	stage := flow.Stage{
		ID: verifyTestStageID,
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{
			{Kind: flow.VerifyShell, Run: "sleep 30", Timeout: time.Second},
		}},
	}
	o, _ := newVerifyTestOrchestrator(t, stage)

	start := time.Now()
	err := o.RunVerification(context.Background(), stage, phaseImplementation)
	elapsed := time.Since(start)

	if elapsed > 10*time.Second {
		t.Fatalf("RunVerification took %v — step timeout must bound the shell command, not the executor idle timeout", elapsed)
	}

	var execErr *VerifyExecError
	if !errors.As(err, &execErr) {
		t.Fatalf("expected *VerifyExecError, got %v (%T)", err, err)
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("expected 'timed out' in the error text, got %q", err.Error())
	}
	var rejected *VerifyRejectedError
	if errors.As(err, &rejected) {
		t.Error("a step timeout must never be classified as needs_changes (VerifyRejectedError)")
	}
}

// TestRunVerification_AgentStepTimeoutIsExecError — C3 код-ревью: agent-шаг,
// чей фейковый раннер блокируется дольше короткого timeout, должен
// вернуться как *VerifyExecError с "timed out" — раннер уважает переданный
// ctx (реалистичная имитация настоящего executor.RunVerifyAgent, который
// возвращает ProcessOK=false при отмене ctx.Done()).
func TestRunVerification_AgentStepTimeoutIsExecError(t *testing.T) {
	stage := flow.Stage{
		ID: verifyTestStageID,
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{
			{Kind: flow.VerifyAgent, Command: "codex", Timeout: 200 * time.Millisecond},
		}},
	}
	o, _ := newVerifyTestOrchestrator(t, stage)
	o.runVerifyAgent = func(ctx context.Context, _ flow.Stage, _, _, _, _ string) (verify.RunOutcome, error) {
		<-ctx.Done()
		return verify.RunOutcome{}, nil
	}

	start := time.Now()
	err := o.RunVerification(context.Background(), stage, phaseImplementation)
	elapsed := time.Since(start)

	if elapsed > 10*time.Second {
		t.Fatalf("RunVerification took %v — step timeout must bound the agent step", elapsed)
	}

	var execErr *VerifyExecError
	if !errors.As(err, &execErr) {
		t.Fatalf("expected *VerifyExecError, got %v (%T)", err, err)
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("expected 'timed out' in the error text, got %q", err.Error())
	}
}
