package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// TestRunnerForAttributesStageIDForDefaultCommand воспроизводит косяк №1:
// у стадии без собственного command (использует дефолтный клиент) события
// agent_action уходили с пустым StageID — дашборд не рисовал бейдж стадии.
// Причина: runnerFor отдавал разделяемый o.runner, чей OnAction был привязан
// к пустому stageID (uiActionPublisher(ui, "")). После фикла каждая стадия
// получает per-stage runner с корректным stageID.
func TestRunnerForAttributesStageIDForDefaultCommand(t *testing.T) {
	runDir := t.TempDir()
	stage := flow.Stage{ID: "s1", Name: "Backend"}

	store, err := state.Open(runDir, []string{stage.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	// Дефолтный клиент — sh-скрипт, эмитящий одно tool_use событие в stream-json,
	// чтобы сработал OnAction (по образцу executor_test).
	script := `printf '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]}}\n'
printf '{"type":"result","subtype":"success"}\n'`
	cfg := config.Default()
	cfg.Client.Command = "sh"
	cfg.Client.ExtraArgs = []string{"-c", script}
	cfg.Executor.IdleTimeout = 5 * time.Second

	o := New(Options{
		RunDir: runDir,
		Stages: []flow.Stage{stage},
		Store:  store,
		Config: cfg,
		// Runner: nil — production-путь (реальный executor).
	})

	subID, ch := o.ui.Subscribe(64)
	defer o.ui.Unsubscribe(subID)

	runner := o.runnerFor(stage, phaseImplementation)
	logFile := filepath.Join(runDir, "impl.log")
	if err := runner.RunAgent(context.Background(), phaseImplementation, stage.Name, "do work", logFile); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	got := waitForAgentAction(t, ch)
	if got.StageID != stage.ID {
		t.Errorf("agent_action StageID = %q, want %q", got.StageID, stage.ID)
	}
}

func waitForAgentAction(t *testing.T, ch <-chan bus.Event) bus.Event {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Type == bus.EventAgentAction {
				return ev
			}
		case <-deadline:
			t.Fatal("timed out waiting for agent_action event")
			return bus.Event{}
		}
	}
}

// TestRunnerForSetsStageDirForNonInteractiveStage закрывает главный пробел
// этой фичи: без AFM_STAGE_DIR агенту non-interactive стадии физически
// некуда писать question.json (см. docs/superpowers/specs/2026-08-07-non-interactive-auto-answer-design.md).
func TestRunnerForSetsStageDirForNonInteractiveStage(t *testing.T) {
	runDir := t.TempDir()
	stage := flow.Stage{ID: "s1", Name: "Backend"} // Interactive: false (default)

	store, err := state.Open(runDir, []string{stage.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	marker := filepath.Join(runDir, "stagedir-seen.txt")
	script := fmt.Sprintf(`printf '%%s' "$AFM_STAGE_DIR" > %q
printf '{"type":"result","subtype":"success"}\n'`, marker)
	cfg := config.Default()
	cfg.Client.Command = "sh"
	cfg.Client.ExtraArgs = []string{"-c", script}
	cfg.Executor.IdleTimeout = 5 * time.Second

	o := New(Options{
		RunDir: runDir,
		Stages: []flow.Stage{stage},
		Store:  store,
		Config: cfg,
	})

	runner := o.runnerFor(stage, phaseImplementation)
	logFile := filepath.Join(runDir, "impl.log")
	if err := runner.RunAgent(context.Background(), phaseImplementation, stage.Name, "do work", logFile); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("AFM_STAGE_DIR not set for non-interactive stage: %v", err)
	}
	if want := filepath.Join(runDir, stage.ID); string(got) != want {
		t.Errorf("AFM_STAGE_DIR = %q, want %q", got, want)
	}
}

// TestRunnerForVerify_WiresInterruptChFromStage — V5a.1: runnerForVerify
// (в отличие от runnerFor) не подключало Config.InterruptCh к
// o.interruptChans[s.ID] — Pause()/Revise() во время работающего
// verify-субпроцесса сигналили канал, но некому было его слушать, и
// верификатор продолжал бы работать до собственного естественного
// завершения. Тест гоняет РЕАЛЬНЫЙ bash-скрипт через RunVerifyAgent (не
// инъектированный o.runVerifyAgent) и проверяет, что сигнал на канал
// действительно доставляется процессу как SIGINT (скрипт перехватывает его
// trap'ом и пишет маркер) — RunVerifyAgent должен вернуть
// verify.RunOutcome{Interrupted:true}.
func TestRunnerForVerify_WiresInterruptChFromStage(t *testing.T) {
	runDir := t.TempDir()
	stage := flow.Stage{ID: "s1", Name: "S1"}

	store, err := state.Open(runDir, []string{stage.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	marker := filepath.Join(runDir, "sigint-seen.txt")
	ready := filepath.Join(runDir, "trap-ready.txt")
	// "ready" is touched right AFTER the trap is installed — the test waits
	// for it before sending the interrupt, so there's no race against bash's
	// own startup time (unlike a fixed settle-sleep, this can't flake under
	// system load).
	script := fmt.Sprintf("#!/bin/bash\ntrap 'touch %q; exit 130' INT\ntouch %q\nsleep 30\n", marker, ready)
	scriptPath := filepath.Join(runDir, "verify-agent.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	o := New(Options{
		RunDir: runDir,
		Stages: []flow.Stage{stage},
		Store:  store,
		Config: config.Default(),
	})

	interruptCh := make(chan struct{}, 1)
	o.interruptChans.Store(stage.ID, interruptCh)

	ex := o.runnerForVerify(stage, scriptPath)
	logFile := filepath.Join(runDir, "verify.log")
	resultFile := filepath.Join(runDir, "verify-result.json")

	outcomeCh := make(chan struct {
		interrupted bool
		err         error
	}, 1)
	go func() {
		oc, err := ex.RunVerifyAgent(context.Background(), "verify", stage.Name, "check it", logFile, resultFile)
		outcomeCh <- struct {
			interrupted bool
			err         error
		}{oc.Interrupted, err}
	}()

	// Дождаться, что скрипт реально установил trap (см. ready выше), прежде
	// чем слать сигнал — иначе SIGINT мог бы прийти раньше, чем bash успеет
	// его перехватить, и убил бы процесс дефолтным обработчиком без маркера.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("verify script never signaled it installed the trap")
		}
		time.Sleep(5 * time.Millisecond)
	}
	interruptCh <- struct{}{}

	select {
	case got := <-outcomeCh:
		if got.err != nil {
			t.Fatalf("RunVerifyAgent: %v", got.err)
		}
		if !got.interrupted {
			t.Error("expected RunOutcome.Interrupted=true — the subprocess should have received SIGINT via the wired InterruptCh")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("RunVerifyAgent did not return — the subprocess was never interrupted (InterruptCh not wired?)")
	}

	if _, err := os.Stat(marker); err != nil {
		t.Errorf("expected the subprocess to have received a real SIGINT (marker file), got: %v", err)
	}
}
