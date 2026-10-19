package orchestrator_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/state"
)

// --- Fix #3: completeStage must not run side effects when EvComplete loses CAS ---

func TestCompleteStage_LostCASSkipsSideEffects(t *testing.T) {
	runDir := t.TempDir()
	stages := []flow.Stage{
		{ID: "s1", Name: "S1", Agents: []flow.AgentType{flow.AgentAuto}, ScriptAfter: "true", ScriptAfterTimeout: 30 * time.Second},
	}
	store, err := state.Open(runDir, []string{"s1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	// Stage is actually paused; completeStage will be called with a stale
	// current=running (what onAgentCompleted had read just before the pause).
	if err := store.Apply(&state.Transition{StageID: "s1", From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(&state.Transition{StageID: "s1", From: state.StatusRunning, To: state.StatusPaused, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}

	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir, Stages: stages, Store: store, Config: config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
	})

	orchestrator.CompleteStageForTest(orch, "s1", state.StatusRunning)
	orchestrator.WaitAgentsForTest(orch)

	if got := store.Get("s1"); got != state.StatusPaused {
		t.Fatalf("EvComplete must not apply to a paused stage: status = %v, want paused", got)
	}
	if n := orchestrator.PendingAfterHooksForTest(orch); n != 0 {
		t.Fatalf("pendingAfterHooks leaked after lost-CAS completeStage: got %d, want 0", n)
	}
}

// --- Fix #1: Pause→Continue during script_before must launch the agent once ---

// barrierAutoRunner counts how many agent invocations ENTER RunAgent and holds each one in-flight until
// release is closed.
type barrierAutoRunner struct {
	t       testing.TB
	mu      sync.Mutex
	entered int
	release chan struct{}
}

func (r *barrierAutoRunner) RunPlanning(_ context.Context, _, _, _, _ string) error { return nil }

func (r *barrierAutoRunner) RunAgent(_ context.Context, _, _, _, logFile string) error {
	r.mu.Lock()
	r.entered++
	r.mu.Unlock()
	orchestrator.AwaitReleaseForTest(r.t, r.release)
	stageDir := filepath.Dir(logFile)
	return os.WriteFile(filepath.Join(stageDir, "execution_summary.md"), []byte("## Summary\ndone\n"), 0644)
}

func (r *barrierAutoRunner) RunJSONQuery(_ context.Context, _ string) ([]byte, error) {
	return []byte("{}"), nil
}

func (r *barrierAutoRunner) enteredCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.entered
}

var _ executor.Runner = (*barrierAutoRunner)(nil)

func TestPauseContinue_DuringBeforeHook_LaunchesOnce(t *testing.T) {
	runDir := t.TempDir()
	stages := []flow.Stage{
		{ID: "s1", Name: "S1", Agents: []flow.AgentType{flow.AgentAuto}, ScriptBefore: "sleep 1.5", ScriptBeforeTimeout: 30 * time.Second},
	}
	store, err := state.Open(runDir, []string{"s1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	stateFile := filepath.Join(runDir, "state.json")

	// MaxParallel >= 2 so Continue's relaunch can start while the original
	// before-hook goroutine still holds a slot (sleeping in script_before).
	cfg := config.Default()
	cfg.Executor.MaxParallel = 4

	runner := &barrierAutoRunner{t: t, release: make(chan struct{})}
	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir, Stages: stages, Store: store, Config: cfg,
		Prompts: orchestrator.DefaultPrompts(), Runner: runner,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	runOrchestratorAsync(ctx, t, orch, cancel)

	// EvStartRun sets running before script_before starts sleeping — Pause now
	// lands squarely inside the (uninterruptible) before-hook window.
	waitForStatus(t, stateFile, "s1", state.StatusRunning, 5*time.Second)
	if err := orch.Pause(ctx, "s1"); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	waitForStatus(t, stateFile, "s1", state.StatusPaused, 3*time.Second)

	// Continue spawns exactly one relaunch (agent A), which enters RunAgent and
	// blocks on release.
	if err := orch.Continue(ctx, "s1"); err != nil {
		t.Fatalf("Continue: %v", err)
	}

	// Wait past before-hook completion to prove its stale generation cannot start a second agent.
	time.Sleep(2500 * time.Millisecond)
	entered := runner.enteredCount()
	close(runner.release) // let the in-flight agent(s) finish and the stage complete

	waitForStatus(t, stateFile, "s1", state.StatusDone, 8*time.Second)

	if entered != 1 {
		t.Fatalf("mainFn launched %d agents after Pause→Continue during script_before, want exactly 1", entered)
	}
}

// --- Fix #5: a losing concurrent Retry must not clear the winner's fresh run ---

func TestRetry_LostCASDoesNotClearInteractiveSessions(t *testing.T) {
	runDir := t.TempDir()
	stageDir := filepath.Join(runDir, "s1")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	// The winner's freshly-created planning session; a losing retry must leave
	// it intact.
	sessionPath := filepath.Join(stageDir, "planning.session.json")
	if err := os.WriteFile(sessionPath, []byte(`{"session_id":"winner"}`), 0644); err != nil {
		t.Fatal(err)
	}

	stages := []flow.Stage{
		{ID: "s1", Name: "S1", Interactive: true, Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
	}
	store, err := state.Open(runDir, []string{"s1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	if err := store.Apply(&state.Transition{StageID: "s1", From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(&state.Transition{StageID: "s1", From: state.StatusRunning, To: state.StatusFailed, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}

	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir, Stages: stages, Store: store, Config: config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
	})

	// Simulate the winner grabbing the retry right first: move failed→pending
	// between this call's status check and its CAS, so its EvManualRetry loses.
	orchestrator.SetRetryCASBarrierForTest(orch, func(stageID string) {
		_ = store.Apply(&state.Transition{StageID: stageID, From: state.StatusFailed, To: state.StatusPending, Event: "winner_retry"})
	})

	orchestrator.RetryStageForTest(orch, "s1")

	if _, err := os.Stat(sessionPath); err != nil {
		t.Fatalf("losing retry cleared the winner's session file: %v", err)
	}
}
