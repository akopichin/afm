package orchestrator_test

// Task 10: script-boundary lifecycle events emitted from runScriptStage
// (agents.go) and runBeforeHook/runAfterHook (hooks.go). Lives in the
// external orchestrator_test package alongside lifecycle_flow_test.go and
// reuses its flowEventRecorder/newFlowRecorderDispatcher/containsEvent/
// assertOrder instead of redefining them.
//
// task-10-brief.md's Step 1 pseudocode names helpers (newFlowRecorder,
// newScriptFlowOrch, runOrchestratorAsync, waitForRunDone) that don't map
// 1:1 onto the actual Task 8/9 infra: newScriptFlowOrch already exists with
// a different signature (script string + resumed/withDashboard bools, no
// before/after knobs), and runOrchestratorAsync already exists taking an
// externally-owned ctx/cancel (testrun_helper_test.go) rather than a bare
// *Orchestrator. The helpers below are the adaptation: same behavior, names
// disambiguated where the brief's name is already taken by a different
// signature.

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

// stageScript returns a minimal single-stage flow.Stage running script as
// its main body — the seed value newScriptStageOrch below layers
// withBefore/withAfter on top of.
func stageScript(script string) flow.Stage {
	return flow.Stage{ID: "notify", Name: "Notify", Script: script}
}

// withBefore/withAfter set the stage's script_before/script_after. Tasks 8-9
// build stages as Go literals rather than parsed YAML flow files, so no
// flow-file/YAML step is needed to attach a hook — these are plain
// functional options over flow.Stage.
func withBefore(script string) func(*flow.Stage) {
	return func(s *flow.Stage) { s.ScriptBefore = script }
}

func withAfter(script string) func(*flow.Stage) {
	return func(s *flow.Stage) { s.ScriptAfter = script }
}

// newFlowRecorder is the bare rec constructor the brief's pseudocode calls
// directly; the dispatcher itself is wired inside newScriptStageOrch via
// lifecycle_flow_test.go's newFlowRecorderDispatcher.
func newFlowRecorder(t *testing.T) *flowEventRecorder {
	t.Helper()
	return &flowEventRecorder{}
}

// scriptTestOrch bundles the *Orchestrator, its state.json path, and its
// Run's completion signal — the adapted shape of the bare "o" the brief's
// pseudocode passes to runOrchestratorAsync/waitForRunDone.
type scriptTestOrch struct {
	orch      *orchestrator.Orchestrator
	stateFile string
	done      chan struct{}
}

// newScriptStageOrch builds a single-stage *orchestrator.Orchestrator wired
// to rec's dispatcher, mirroring newScriptFlowOrch (lifecycle_flow_test.go)
// but taking a flow.Stage + functional options instead of a bare script
// string, so TestBeforeAfterHook_Events/TestBeforeHook_FailureEvent below can
// attach ScriptBefore/ScriptAfter to the same stage.
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

// runOrchestratorAsync starts o.orch.Run in the background under a bounded
// context and registers the same cancel+wait t.Cleanup pattern as
// testrun_helper_test.go's runOrchestratorAsync — kept as a distinct
// function (rather than overloading that name/signature, which Go doesn't
// support) because it also stashes the done channel on scriptTestOrch for
// waitForRunDone below to poll mid-test, not just at cleanup.
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

// waitForEvent polls rec.ordered() until want has been recorded, or fails
// the test after timeout — same polling idiom as lifecycle_flow_test.go's
// waitForFlowEvent, reproduced here under the name task-10-brief.md uses.
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

// TestScriptStage_Events: a plain script stage that succeeds emits
// stage_script_started then stage_script_finished (never stage_script_failed),
// alongside the ordinary FSM stage_finished event.
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

// TestScriptStage_FailureEvents: a script stage that always exits 1 exhausts
// runScriptWithRetry's built-in retries and emits stage_script_started then
// stage_script_failed (never stage_script_finished), alongside the ordinary
// FSM stage_failed event.
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

// TestBeforeAfterHook_Events: a script stage with both script_before and
// script_after emits all four before/after started/finished events, with the
// before hook strictly preceding the main script and the after hook strictly
// following it.
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

// TestBeforeHook_FailureEvent: a before-hook that always exits 1 exhausts
// its retries and emits stage_script_before_failed; hook_failed then blocks
// the stage (waiting on a RetryHook/SkipHook decision), so Run itself never
// returns here — it's enough that the event is observed, followed by the
// normal cleanup-driven cancellation.
func TestBeforeHook_FailureEvent(t *testing.T) {
	rec := newFlowRecorder(t)
	o := newScriptStageOrch(t, rec, stageScript("true"), withBefore("exit 1"))
	runOrchestratorAsyncS(t, o)

	waitForEvent(t, rec, lifecyclehooks.EventStageScriptBeforeFailed, 15*time.Second)
	assertContains(t, rec.ordered(), lifecyclehooks.EventStageScriptBeforeFailed)
}
