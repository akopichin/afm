package orchestrator_test

// Task 10: script-boundary lifecycle events emitted from runScriptStage and runBeforeHook/runAfterHook.

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/lifecyclehooks"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/state"
)

// stageScript returns a minimal single-stage flow.Stage running script as its main body — the seed value
// newScriptStageOrch below layers withBefore/withAfter on top of.
func stageScript(script string) flow.Stage {
	return flow.Stage{ID: "notify", Name: "Notify", Script: script}
}

// withBefore/withAfter set the stage's script_before/script_after.
func withBefore(script string) func(*flow.Stage) {
	return func(s *flow.Stage) { s.ScriptBefore = script }
}

func withAfter(script string) func(*flow.Stage) {
	return func(s *flow.Stage) { s.ScriptAfter = script }
}

// newFlowRecorder constructs the recorder; newScriptStageOrch wires its dispatcher.
func newFlowRecorder(t *testing.T) *flowEventRecorder {
	t.Helper()
	return &flowEventRecorder{}
}

// scriptTestOrch holds the orchestrator, state path, and run completion channel.
type scriptTestOrch struct {
	orch      *orchestrator.Orchestrator
	stateFile string
	done      chan struct{}
}

// newScriptStageOrch wires a single stage and recorder, allowing before/after hooks through options.
func newScriptStageOrch(t *testing.T, rec *flowEventRecorder, stage flow.Stage, opts ...func(*flow.Stage)) *scriptTestOrch {
	t.Helper()
	for _, opt := range opts {
		opt(&stage)
	}

	rootDir := t.TempDir()
	runDir := t.TempDir()

	store, err := state.Open(runDir, []string{stage.ID})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	orch := orchestrator.New(orchestrator.Options{
		RunDir:  runDir,
		RootDir: rootDir,
		Stages:  []flow.Stage{stage},
		Store:   store,
		Config:  config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
		Hooks:   newFlowRecorderDispatcher(t, rec),
	})
	return &scriptTestOrch{orch: orch, stateFile: filepath.Join(runDir, "state.json")}
}

// runOrchestratorAsync starts a bounded run, stores its done channel, and registers cancel-and-wait
// cleanup.
func runOrchestratorAsyncS(t *testing.T, o *scriptTestOrch) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	o.done = make(chan struct{})
	go func() {
		_ = o.orch.Run(ctx)
		close(o.done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-o.done:
		case <-time.After(10 * time.Second):
			t.Error("orch.Run did not return within 10s of cancellation")
		}
	})
}

// waitForRunDone blocks until the Run started by runOrchestratorAsyncS has
// actually returned, or fails the test after timeout.
func waitForRunDone(t *testing.T, o *scriptTestOrch, timeout time.Duration) {
	t.Helper()
	select {
	case <-o.done:
	case <-time.After(timeout):
		t.Fatalf("orch.Run did not finish within %s", timeout)
	}
}

// waitForEvent polls recorded events until want arrives or timeout fails the test.
func waitForEvent(t *testing.T, rec *flowEventRecorder, want lifecyclehooks.EventType, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if containsEvent(rec.ordered(), want) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("event %s not observed within %s (got %v)", want, timeout, rec.ordered())
}

func assertContains(t *testing.T, evs []lifecyclehooks.EventType, want lifecyclehooks.EventType) {
	t.Helper()
	if !containsEvent(evs, want) {
		t.Fatalf("missing %s: %v", want, evs)
	}
}

func assertNotContains(t *testing.T, evs []lifecyclehooks.EventType, unwanted lifecyclehooks.EventType) {
	t.Helper()
	if containsEvent(evs, unwanted) {
		t.Fatalf("unexpected %s: %v", unwanted, evs)
	}
}

func TestScriptStage_Events(t *testing.T) {
	rec := newFlowRecorder(t)
	o := newScriptStageOrch(t, rec, stageScript("true"))
	runOrchestratorAsyncS(t, o)
	waitForRunDone(t, o, 15*time.Second)

	evs := rec.ordered()
	assertOrder(t, evs, lifecyclehooks.EventStageScriptStarted, lifecyclehooks.EventStageScriptFinished)
	assertContains(t, evs, lifecyclehooks.EventStageFinished)
	assertNotContains(t, evs, lifecyclehooks.EventStageScriptFailed)
}

func TestScriptStage_FailureEvents(t *testing.T) {
	rec := newFlowRecorder(t)
	o := newScriptStageOrch(t, rec, stageScript("exit 1"))
	runOrchestratorAsyncS(t, o)
	waitForRunDone(t, o, 20*time.Second)

	evs := rec.ordered()
	assertOrder(t, evs, lifecyclehooks.EventStageScriptStarted, lifecyclehooks.EventStageScriptFailed)
	assertContains(t, evs, lifecyclehooks.EventStageFailed)
	assertNotContains(t, evs, lifecyclehooks.EventStageScriptFinished)
}

func TestBeforeAfterHook_Events(t *testing.T) {
	rec := newFlowRecorder(t)
	o := newScriptStageOrch(t, rec, stageScript("true"),
		withBefore("echo before"), withAfter("echo after"))
	runOrchestratorAsyncS(t, o)
	waitForRunDone(t, o, 15*time.Second)

	evs := rec.ordered()
	assertContains(t, evs, lifecyclehooks.EventStageScriptBeforeStarted)
	assertContains(t, evs, lifecyclehooks.EventStageScriptBeforeFinished)
	assertContains(t, evs, lifecyclehooks.EventStageScriptAfterStarted)
	assertContains(t, evs, lifecyclehooks.EventStageScriptAfterFinished)
	assertOrder(t, evs, lifecyclehooks.EventStageScriptBeforeFinished, lifecyclehooks.EventStageScriptStarted)
	assertOrder(t, evs, lifecyclehooks.EventStageScriptFinished, lifecyclehooks.EventStageScriptAfterStarted)
}

func TestBeforeHook_FailureEvent(t *testing.T) {
	rec := newFlowRecorder(t)
	o := newScriptStageOrch(t, rec, stageScript("true"), withBefore("exit 1"))
	runOrchestratorAsyncS(t, o)

	waitForEvent(t, rec, lifecyclehooks.EventStageScriptBeforeFailed, 15*time.Second)
	assertContains(t, rec.ordered(), lifecyclehooks.EventStageScriptBeforeFailed)
}
