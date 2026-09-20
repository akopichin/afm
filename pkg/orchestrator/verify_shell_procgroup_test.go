package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
)

// TestRunVerifyShellCommand_KillsProcessGroupOnCancel — C4(b) код-ревью:
// shell-verify команда, чей потомок не exec'нулся в родителя (хвостовой
// `sleep 30 &` в фоне), наследует stdout/stderr-пайпы — если runVerifyShellCommand
// киллит только прямой процесс "sh", осиротевший потомок держит пайп
// открытым, и CombinedOutput() виснет до его естественного конца (30s). С
// process-group killом (setProcessGroup + cmd.Cancel) отмена ctx должна
// вернуть управление быстро, а не через 30 секунд.
func TestRunVerifyShellCommand_KillsProcessGroupOnCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := runVerifyShellCommand(ctx, ".", "sleep 30 & exit 0")
	elapsed := time.Since(start)

	if elapsed > 5*time.Second {
		t.Fatalf("runVerifyShellCommand took %v — the orphaned grandchild must not keep the pipe open after ctx cancellation (process-group kill)", elapsed)
	}
	if err == nil {
		t.Fatal("expected an error after the process group was killed by ctx cancellation")
	}
}

// TestRunVerification_ShellStepInterruptedByPause — C4(a) код-ревью: zависший
// shell-verify шаг (`sleep 30`) должен реагировать на Pause/Revise через тот
// же interruptChans-канал, что и agent-шаг верификации, а не только на
// отмену родительского ctx. Барьер по файлу (не фиксированный sleep):
// сигнал паузы посылается только после того, как шелл-скрипт реально
// стартовал и создал ready-файл.
func TestRunVerification_ShellStepInterruptedByPause(t *testing.T) {
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")

	stage := flow.Stage{
		ID: verifyTestStageID,
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{
			{Kind: flow.VerifyShell, Run: "touch " + ready + "; sleep 30"},
		}},
	}
	o, _ := newVerifyTestOrchestrator(t, stage)

	interruptCh := make(chan struct{}, 1)
	o.interruptChans.Store(stage.ID, interruptCh)

	done := make(chan error, 1)
	go func() { done <- o.RunVerification(context.Background(), stage, phaseImplementation) }()

	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, statErr := os.Stat(ready); statErr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("shell verify step never signaled readiness")
		}
		time.Sleep(20 * time.Millisecond)
	}

	select {
	case interruptCh <- struct{}{}:
	default:
		t.Fatal("interrupt channel should have room for one signal")
	}

	select {
	case err := <-done:
		if !errors.Is(err, executor.ErrUserInterrupted) {
			t.Fatalf("expected executor.ErrUserInterrupted, got %v (%T)", err, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunVerification did not return promptly after the pause signal — shell step ignored interruptChans")
	}
}
