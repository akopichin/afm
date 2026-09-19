package orchestrator_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/orchestrator/verify"
	"github.com/akopichin/afm/pkg/state"
)

// TestIntegration_PauseDuringVerifier_StopsSubprocessAndStaysPaused —
// V5a.1(a): Pause() во время РЕАЛЬНО работающего verify-субпроцесса должен
// оставить стадию в paused, а не done — верификатор прерван (через wired
// InterruptCh, runnerForVerify), а не успешно завершился с вердиктом.
// Использует настоящий bash-скрипт как verify-шаг (не инъектированный
// o.runVerifyAgent), чтобы проверить реальную доставку сигнала, а не только
// FSM-логику на фейковом раннере.
//
// Не проверяет доставку именно SIGINT отдельным маркером/trap'ом в скрипте —
// живое исследование на реальной машине показало, что доставка SIGINT
// процессной группе с graceful-trap'ом в дочернем bash иногда (не всегда)
// проигрывает гонку с завершением заведомо untrapped-потомка (sleep) в той
// же группе, а изредка сам сигнал попросту не долетает до процесса вовсе —
// известная особенность взаимодействия ОС/bash/сигналы/wait(), не имеющая
// отношения к корректности executor.run: он ВСЕГДА в итоге возвращает
// ErrUserInterrupted — либо быстрым путём, либо гарантированным
// SIGKILL-фолбэком после interruptGracePeriod (executor.go, 15s). Поэтому
// единственное, что стадия обязана делать по итогу verify.Interrupted —
// остаться paused — и есть предмет проверки здесь, с таймаутом, комфортно
// перекрывающим худший случай (SIGKILL-фолбэк).
func TestIntegration_PauseDuringVerifier_StopsSubprocessAndStaysPaused(t *testing.T) {
	scriptDir := t.TempDir()
	ready := filepath.Join(scriptDir, "ready.txt")
	script := fmt.Sprintf("#!/bin/bash\ntouch %q\nsleep 30\n", ready)
	scriptPath := filepath.Join(scriptDir, "verify-agent.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	stages := []flow.Stage{{
		ID:     "auto1",
		Name:   "Auto1",
		Agents: []flow.AgentType{flow.AgentAuto},
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: scriptPath}}},
	}}

	orch, _, stateFile := setupOrchestratorWithRunner(t, stages, &autonomousSummaryRunner{})

	// Generous timeouts throughout: real subprocess + real signal delivery,
	// worst case bounded by executor.go's interruptGracePeriod (15s
	// SIGKILL fallback) plus scheduling slack under `go test -race`.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	runOrchestratorAsync(ctx, t, orch, cancel)

	waitForStatus(t, stateFile, "auto1", state.StatusRunning, 20*time.Second)

	// Дождаться, что верификатор реально стартовал, прежде чем ставить на
	// паузу — иначе SIGINT мог бы уйти раньше, чем процесс успеет
	// запуститься.
	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("verify subprocess never signaled readiness")
		}
		time.Sleep(20 * time.Millisecond)
	}

	if err := orch.Pause(ctx, "auto1"); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	waitForStatus(t, stateFile, "auto1", state.StatusPaused, 30*time.Second)

	final := loadStateJSON(t, stateFile)
	if final.Stages["auto1"].Status != state.StatusPaused {
		t.Fatalf("expected stage to remain paused after the verifier was interrupted, got %v", final.Stages["auto1"].Status)
	}
	if final.Stages["auto1"].PausedFrom != state.StatusRunning {
		t.Errorf("PausedFrom = %q, want %q", final.Stages["auto1"].PausedFrom, state.StatusRunning)
	}
}

// TestIntegration_PauseAfterVerifyPassBeforeCompletion_StaysPaused —
// V5a.1(c): a Pause() landing durably WHILE the verify agent step is
// computing its verdict (but before RunVerification returns to runWithRetry)
// must not be overridden by the pass that follows — completion must never
// land on a stage that's already durably paused. The fake verify runner
// itself calls orch.Pause synchronously to reproduce the exact race
// deterministically, no sleeps required.
func TestIntegration_PauseAfterVerifyPassBeforeCompletion_StaysPaused(t *testing.T) {
	stages := []flow.Stage{{
		ID:     "auto1",
		Name:   "Auto1",
		Agents: []flow.AgentType{flow.AgentAuto},
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "codex"}}},
	}}
	orch, _, stateFile := setupOrchestratorWithRunner(t, stages, &autonomousSummaryRunner{})

	orchestrator.SetVerifyAgentRunnerForTest(orch, func(ctx context.Context, s flow.Stage, cmd, prompt, logFile, resultFile string) (verify.RunOutcome, error) {
		// Симулирует пользователя, нажавшего "Pause" ровно в момент, когда
		// верификатор уже посчитал вердикт, но ДО того, как RunVerification
		// вернёт управление runWithRetry — та же горутина, детерминированно,
		// без сна.
		if err := orch.Pause(context.Background(), "auto1"); err != nil {
			t.Errorf("Pause: %v", err)
		}
		return verify.RunOutcome{ProcessOK: true, Result: &verify.ModelResult{
			SchemaVersion: verify.SupportedSchemaVersion, Verdict: verify.VerdictPass, Summary: "ok",
		}}, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	runOrchestratorAsync(ctx, t, orch, cancel)

	waitForStatus(t, stateFile, "auto1", state.StatusPaused, 5*time.Second)

	// Дать событийному циклу время, за которое баг (позднее завершение)
	// успел бы проявиться, прежде чем финально проверять статус.
	time.Sleep(200 * time.Millisecond)

	final := loadStateJSON(t, stateFile)
	if final.Stages["auto1"].Status != state.StatusPaused {
		t.Fatalf("expected stage to stay paused despite a late verify pass, got %v", final.Stages["auto1"].Status)
	}
	if final.Stages["auto1"].PausedFrom != state.StatusRunning {
		t.Errorf("PausedFrom = %q, want %q", final.Stages["auto1"].PausedFrom, state.StatusRunning)
	}
}
