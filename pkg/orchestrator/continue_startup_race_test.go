package orchestrator

// T11.3 (§19.3 scenario 3): "Continue одновременно со startup recovery" — a user's Continue action races the
// orchestrator's own startup-recovery pass (startPlanningForPending, what a real `afm run` calls once at
// boot) for the SAME paused stage. AGENTS.md documents the guard: continueMu ties Continue's CAS transition
// together with marking continuedThisProcess, and startup reads BOTH the current status and that marker
// under the SAME mutex before deciding what to do — without it, startup could observe the stage already
// flipped back to "running" by a concurrent Continue (recovery.go's `case state.StatusRevising,
// state.StatusRunning: ... o.resumeStageAtStatus(ctx, s, current)`) and spawn a SECOND agent for the exact
// same resume.

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

// raceCountingBlockingRunner counts RunAgent invocations and blocks each one on release — letting the test
// observe "exactly one spawn happened" before anything can complete and trigger further (unrelated)
// scheduling noise.
type raceCountingBlockingRunner struct {
	calls   atomic.Int32
	release chan struct{}
}

func (r *raceCountingBlockingRunner) RunPlanning(context.Context, string, string, string, string) error {
	return nil
}

func (r *raceCountingBlockingRunner) RunAgent(ctx context.Context, agentType, stageName, prompt, logFile string) error {
	r.calls.Add(1)
	select {
	case <-r.release:
	case <-ctx.Done():
	}
	return ctx.Err()
}

func (r *raceCountingBlockingRunner) RunJSONQuery(context.Context, string) ([]byte, error) {
	return nil, nil
}

// TestContinue_RacesStartupRecovery_NeverDoubleSpawns races Orchestrator.Continue against
// startPlanningForPending (both operating on the SAME stage, paused with PausedFrom == running — the exact
// shape a real afm restart would find if a user's dashboard click lands the instant the process comes back
// up) and asserts the agent is spawned EXACTLY ONCE, no matter which of the two wins continueMu first.
func TestContinue_RacesStartupRecovery_NeverDoubleSpawns(t *testing.T) {
	for i := 0; i < 20; i++ { // run repeatedly under -race to flush out either ordering
		t.Run("", runOneContinueStartupRace)
	}
}

func runOneContinueStartupRace(t *testing.T) {
	t.Helper()
	runDir := t.TempDir()
	stage := flow.Stage{ID: "s1", Name: "S1", Agents: []flow.AgentType{flow.AgentImplementation}}
	// runImplementationAttempt reads plan.md before ever calling the runner — without it, the resumed
	// implementation agent fails fast (missing plan) and RunAgent is never actually invoked, which would make
	// this test pass for the wrong reason (nothing spawned at all, rather than spawned exactly once).
	if err := os.MkdirAll(filepath.Join(runDir, "s1"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "s1", "plan.md"), []byte("## Tasks\n- x\n"), 0644); err != nil {
		t.Fatal(err)
	}

	store, err := state.Open(runDir, []string{"s1"})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	// Seed: stage was running, then paused — SetStageStatusAt stamps PausedFrom from whatever status preceded
	// the pause (pkg/state/state.go), exactly as a real Orchestrator.Pause would have left it.
	if err := store.Apply(&state.Transition{StageID: "s1", From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(&state.Transition{StageID: "s1", From: state.StatusRunning, To: state.StatusPaused, Event: "test_setup_pause"}); err != nil {
		t.Fatal(err)
	}

	runner := &raceCountingBlockingRunner{release: make(chan struct{})}
	o := New(Options{
		RunDir: runDir, Stages: []flow.Stage{stage}, Store: store, Config: config.Default(),
		Prompts: DefaultPrompts(), Runner: runner,
	})
	t.Cleanup(func() {
		close(runner.release)
		o.concurrency.WaitAgents()
	})

	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		<-start
		_ = o.Continue(context.Background(), "s1")
	})
	wg.Go(func() {
		<-start
		o.startPlanningForPending(context.Background())
	})
	close(start)
	wg.Wait()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && runner.calls.Load() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	// Drain any further async scheduling noise before the final count — spawnKind runs the agent goroutine
	// asynchronously relative to Continue/startPlanningForPending returning.
	time.Sleep(50 * time.Millisecond)

	if n := runner.calls.Load(); n != 1 {
		t.Fatalf("RunAgent invoked %d times, want exactly 1 — Continue and startup recovery must never both resume the same paused stage", n)
	}
}
