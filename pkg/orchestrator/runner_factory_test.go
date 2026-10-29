package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/browser"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/state"
)

func TestWillResume_NonInteractiveNeverResumes(t *testing.T) {
	stageDir := t.TempDir()
	phase := phaseImplementation
	if _, err := stagefiles.LoadOrCreateSession(stageDir, phase); err != nil {
		t.Fatal(err)
	}
	stage := flow.Stage{ID: "s1", Interactive: false}
	if willResume(stage, stageDir, phase) {
		t.Fatal("non-interactive stage must never resume, even with an existing session file")
	}
}

func TestWillResume_InteractiveWithoutSession(t *testing.T) {
	stageDir := t.TempDir()
	stage := flow.Stage{ID: "s1", Interactive: true}
	if willResume(stage, stageDir, phaseImplementation) {
		t.Fatal("expected false: interactive stage with no session file yet does not resume")
	}
}

func TestWillResume_InteractiveWithSession(t *testing.T) {
	stageDir := t.TempDir()
	phase := phaseImplementation
	if _, err := stagefiles.LoadOrCreateSession(stageDir, phase); err != nil {
		t.Fatal(err)
	}
	stage := flow.Stage{ID: "s1", Interactive: true}
	if !willResume(stage, stageDir, phase) {
		t.Fatal("expected true: interactive stage with an existing session resumes")
	}
}

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

func TestRunnerForVerify_WiresInterruptChFromStage(t *testing.T) {
	runDir := t.TempDir()
	stage := flow.Stage{ID: "s1", Name: "S1"}

	store, err := state.Open(runDir, []string{stage.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	ready := filepath.Join(runDir, "ready.txt")
	script := fmt.Sprintf("#!/bin/bash\ntouch %q\nsleep 30\n", ready)
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

	// Дождаться, что скрипт реально стартовал, прежде чем слать сигнал —
	// иначе SIGINT мог бы прийти раньше, чем процесс успеет запуститься.
	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("verify script never signaled it started")
		}
		time.Sleep(5 * time.Millisecond)
	}
	interruptCh <- struct{}{}

	// Таймаут комфортно перекрывает executor.go's interruptGracePeriod (15s) — именно столько может занять
	// худший случай, когда graceful SIGINT проигрывает гонку и executor сам форсирует SIGKILL.
	select {
	case got := <-outcomeCh:
		if got.err != nil {
			t.Fatalf("RunVerifyAgent: %v", got.err)
		}
		if !got.interrupted {
			t.Error("expected RunOutcome.Interrupted=true — the subprocess should have received SIGINT/SIGKILL via the wired InterruptCh")
		}
	case <-time.After(25 * time.Second):
		t.Fatal("RunVerifyAgent did not return — the subprocess was never interrupted (InterruptCh not wired?)")
	}
}

// Базовая конфигурация хода собирается на каждую попытку: interrupt-канал читается в момент вызова, а mesh
// токен минтится только при старте процесса и отзывается его завершением.
func TestBaseStageExecutorConfig_BindsStageIdentityPerCall(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	o.opts.DashboardURL = "http://127.0.0.1:9876"
	o.opts.RootDir = t.TempDir() // отличается от каталога рана: StageDir не зависит от CWD агента
	o.absRunDir = t.TempDir()
	stage := flow.Stage{ID: assistantsTestStageID}
	participant := meshStageParticipantID(stage.ID)

	first := make(chan struct{}, 1)
	o.interruptChans.Store(stage.ID, first)
	cfg := o.baseStageExecutorConfig(stage, phaseImplementation, "agent-cmd", []string{"--flag"})
	second := make(chan struct{}, 1)
	o.interruptChans.Store(stage.ID, second)
	next := o.baseStageExecutorConfig(stage, phaseImplementation, "agent-cmd", nil)

	if cfg.Command != "agent-cmd" || len(cfg.ExtraArgs) != 1 || cfg.ExtraArgs[0] != "--flag" {
		t.Errorf("command/args = %q %q, want agent-cmd [--flag]", cfg.Command, cfg.ExtraArgs)
	}
	if cfg.StageID != stage.ID || cfg.Phase != phaseImplementation {
		t.Errorf("identity = %q/%q, want %q/%q", cfg.StageID, cfg.Phase, stage.ID, phaseImplementation)
	}
	if wantDir := filepath.Join(o.absRunDir, stage.ID); !filepath.IsAbs(cfg.StageDir) || cfg.StageDir != wantDir || cfg.Dir != o.opts.RootDir {
		t.Errorf("StageDir/Dir = %q/%q, want %q and %q", cfg.StageDir, cfg.Dir, wantDir, o.opts.RootDir)
	}
	if cfg.InterruptCh != (<-chan struct{})(first) || next.InterruptCh != (<-chan struct{})(second) {
		t.Error("InterruptCh must be the channel registered at the moment of each call")
	}
	if cfg.OnLimitWarning == nil || cfg.OnDelivered == nil || cfg.OnObservation == nil {
		t.Error("stage callbacks must be wired")
	}

	subID, events := o.ui.Subscribe(4)
	defer o.ui.Unsubscribe(subID)
	cfg.OnAction("Bash", "ls")
	if got := waitForAgentAction(t, events); got.StageID != stage.ID {
		t.Errorf("OnAction StageID = %q, want %q", got.StageID, stage.ID)
	}

	if cfg.MeshClient == nil || cfg.MeshClient.ParticipantID != string(participant) {
		t.Fatalf("MeshClient = %+v, want participant %q", cfg.MeshClient, participant)
	}
	if _, ok := o.ResolveMeshToken("anything"); ok {
		t.Fatal("building the config must not issue a token")
	}
	_, token, revoke := cfg.MeshClient.Mint()
	_, nextToken, nextRevoke := next.MeshClient.Mint()
	defer nextRevoke()
	if got, ok := o.ResolveMeshToken(token); !ok || got != participant {
		t.Fatalf("ResolveMeshToken = (%q, %v), want (%q, true)", got, ok, participant)
	}
	revoke()
	if _, ok := o.ResolveMeshToken(token); ok {
		t.Error("a revoked token must not resolve")
	}
	if _, ok := o.ResolveMeshToken(nextToken); !ok {
		t.Error("revoking one turn's token must not revoke another turn's token")
	}
}

// Verify и helper не получают стадийные возможности автора: StageDir, managed browser и диалог остаются у стадии.
func TestVerifyAndHelperConfigs_DoNotInheritStageCapabilities(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	o.opts.BrowserDescriptor = &browser.Descriptor{Endpoint: "127.0.0.1:1", Epoch: "ep1"}
	o.opts.Config.Docker.Agents = nil
	stage := normalClaudeStage(assistantsTestStageID)

	if o.baseStageExecutorConfig(stage, phaseImplementation, "claude", nil).BrowserMCP == nil {
		t.Fatal("precondition: the stage turn must get the managed browser")
	}
	if execConfigHasBrowser(o.runnerForVerify(stage, "claude")) {
		t.Error("verify runner must not get the managed browser")
	}
	helper := o.helperExecutorConfig(assistants.TaskID("t-caps"), "claude")
	if helper.BrowserMCP != nil || helper.StageDir != "" || helper.StageID != "" {
		t.Errorf("helper config inherited stage capabilities: browser=%v stageDir=%q stageID=%q", helper.BrowserMCP, helper.StageDir, helper.StageID)
	}
}

// stubRunner отличает инъектированный runner от настоящего executor.
type stubRunner struct{ executor.Runner }

// Дефолтная команда берёт клиент из конфига, custom command — своё: args/session зависят от режима и fallback.
func TestStageRunners_ArgsAndSessionMatrix(t *testing.T) {
	defaultArgs := []string{"--default-flag"}
	cases := []struct {
		name        string
		custom      bool
		interactive bool
		noStageDir  bool // нет каталога стадии: сессию не создать, interactive откатывается на non-interactive
		wantArgs    func() []string
		wantSession bool
	}{
		{"default non-interactive", false, false, false, func() []string { return defaultArgs }, false},
		{"custom non-interactive", true, false, false, executor.DefaultClaudeArgs, false},
		{"default interactive", false, true, false, func() []string { return executor.ResolveArgs(defaultArgs) }, true},
		{"custom interactive", true, true, false, func() []string { return executor.ResolveArgs(defaultArgs) }, true},
		{"default fallback", false, true, true, func() []string { return defaultArgs }, false},
		{"custom fallback", true, true, true, executor.DefaultClaudeArgs, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			runDir := filepath.Join(base, "run")
			stage := flow.Stage{ID: "s1", Name: "Backend", Interactive: tc.interactive}
			store, err := state.Open(runDir, []string{stage.ID})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { store.Close() })
			argsFile := filepath.Join(base, "args.txt")
			script := filepath.Join(base, "agent.sh")
			body := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %q\nprintf '{\"type\":\"result\",\"subtype\":\"success\"}\\n'\n", argsFile)
			if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
				t.Fatal(err)
			}
			cfg := config.Default()
			cfg.Client.Command = script
			cfg.Client.ExtraArgs = defaultArgs
			cfg.Executor.IdleTimeout = 5 * time.Second
			if tc.custom {
				customScript := filepath.Join(base, "custom.sh")
				if err := os.Rename(script, customScript); err != nil {
					t.Fatal(err)
				}
				stage.Command = customScript
				cfg.Client.Command = "unused-default-client"
			}

			o := New(Options{RunDir: runDir, Stages: []flow.Stage{stage}, Store: store, Config: cfg})
			// Каталог стадии отсутствует, пока тест не решит иначе: так сессию не создать и interactive откатывается.
			stageDir := filepath.Join(runDir, stage.ID)
			if err := os.RemoveAll(stageDir); err != nil {
				t.Fatal(err)
			}
			if !tc.noStageDir {
				if err := os.MkdirAll(stageDir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			run := func() []string {
				t.Helper()
				if err := o.runnerFor(stage, phaseImplementation).RunAgent(context.Background(), phaseImplementation, stage.Name, "go", filepath.Join(base, "impl.log")); err != nil {
					t.Fatalf("RunAgent: %v", err)
				}
				got, err := os.ReadFile(argsFile)
				if err != nil {
					t.Fatal(err)
				}
				return strings.Split(strings.TrimSuffix(string(got), "\n"), "\n")
			}

			args := run()
			want := tc.wantArgs()
			if !tc.wantSession {
				if !slices.Equal(args, want) {
					t.Fatalf("args = %q, want %q", args, want)
				}
				return
			}
			if len(args) != len(want)+2 || !slices.Equal(args[:len(want)], want) || args[len(want)] != "--session-id" {
				t.Fatalf("args = %q, want %q followed by --session-id <id>", args, want)
			}
			sessionID := args[len(args)-1]
			// Вторая попытка той же фазы продолжает сессию.
			again := run()
			if flag := again[len(again)-2]; flag != "--resume" || again[len(again)-1] != sessionID {
				t.Fatalf("second attempt args = %q, want --resume %s", again, sessionID)
			}
		})
	}
}

// Инъектированный runner подменяет только дефолтную команду non-interactive хода и fallback'а.
func TestRunnerFor_InjectedRunnerOnlyForDefaultCommand(t *testing.T) {
	runDir := t.TempDir()
	injected := stubRunner{}
	o := New(Options{RunDir: runDir, Config: config.Default(), Runner: injected})
	if err := os.MkdirAll(filepath.Join(runDir, "s1"), 0o755); err != nil {
		t.Fatal(err)
	}

	isInjected := func(r executor.Runner) bool { _, ok := r.(stubRunner); return ok }
	if !isInjected(o.runnerFor(flow.Stage{ID: "s1"}, phaseImplementation)) {
		t.Error("default-command non-interactive stage must use the injected runner")
	}
	if !isInjected(o.runnerForFallback(flow.Stage{ID: "s1", Interactive: true}, phaseImplementation)) {
		t.Error("default-command fallback must use the injected runner")
	}
	if isInjected(o.runnerFor(flow.Stage{ID: "s1", Command: "custom"}, phaseImplementation)) {
		t.Error("custom command must never use the injected runner")
	}
	if isInjected(o.runnerFor(flow.Stage{ID: "s1", Interactive: true}, phaseImplementation)) {
		t.Error("interactive stage must build its own session executor")
	}
}
