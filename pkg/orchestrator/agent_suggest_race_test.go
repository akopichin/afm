package orchestrator_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/state"
)

// naturalCompletionRaceRunner reproduces the OTHER agent_suggest race (see
// blockingThenFeedbackRunner above for the SIGINT-observed case): the agent
// subprocess finishes NATURALLY (RunAgent returns nil, no error) at almost the
// same instant Revise() fires Running -> Revising. Unlike
// blockingThenFeedbackRunner, this mock never reads interruptChans at all on
// the first call — it deliberately models a subprocess that has already
// returned before any interrupt signal could be observed, so
// runWithRetry.onUserInterrupted never fires and onAgentCompleted is the only
// place left that can notice the stage needs reconciling out of Revising.
type naturalCompletionRaceRunner struct {
	calls   int
	proceed chan struct{} // test closes this only after Revise() has returned,
	// so call 1 cannot finish before the running->revising transition and
	// feedback.md are already durably on disk — reproducing the interleaving
	// from the bug report deterministically instead of via real timing.
	secondCallFeedback string // captured feedback.md content on the reconciliation restart
}

func (r *naturalCompletionRaceRunner) RunPlanning(_ context.Context, _, _, outFile, _ string) error {
	plan := "## Tasks\n\n- [ ] implement feature\n\n## Assumptions\n\n- none\n\n## Acceptance Criteria\n\n- [ ] feature works\n"
	return os.WriteFile(outFile, []byte(plan), 0644)
}

func (r *naturalCompletionRaceRunner) RunAgent(_ context.Context, _, _, _, logFile string) error {
	r.calls++
	stageDir := filepath.Dir(logFile)
	if r.calls == 1 {
		<-r.proceed
		// Natural completion: the agent finished its work on its own, with no
		// idea that Revise() had already (or was about to) fire EvRevise and
		// send a signal into interruptChans that nobody will ever read.
		return os.WriteFile(filepath.Join(stageDir, ".done"), []byte("done\n"), 0644)
	}
	// Reconciliation restart (runImplementationWithFeedback): must actually
	// read the feedback the user submitted via Revise(), not just re-run
	// blindly — this is what distinguishes "stage got unstuck" from "stage
	// got unstuck AND the feedback was applied".
	feedback, err := os.ReadFile(filepath.Join(stageDir, "feedback.md"))
	if err != nil {
		return errors.New("expected feedback.md to be readable on the reconciliation restart")
	}
	r.secondCallFeedback = string(feedback)
	return os.WriteFile(filepath.Join(stageDir, ".done"), []byte("done\n"), 0644)
}

func (r *naturalCompletionRaceRunner) RunJSONQuery(_ context.Context, _ string) ([]byte, error) {
	return nil, errors.New("not used in this test")
}

var _ executor.Runner = (*naturalCompletionRaceRunner)(nil)

// TestAgentSuggest_NaturalCompletionRaceReconciles reproduces the race where
// Revise() succeeds (Running -> Revising, feedback.md saved) at almost the
// same instant the running agent finishes on its own (returns nil, not
// executor.ErrUserInterrupted). Because the agent didn't return
// ErrUserInterrupted, onUserInterrupted never fires — runWithRetry instead
// takes its normal "success" path and publishes EventAgentCompleted. Before
// the fix, onAgentCompleted's `case phaseImplementation, phaseAutonomous:`
// branch only proceeds when current status is Running/Retrying; since the
// status is now Revising, it silently returns and the stage is stuck in
// Revising forever for the rest of the live run. After the fix, that branch
// reconciles a Revising completion by dispatching to
// runImplementationWithFeedback (the same function recovery.go's
// startPlanningForPending already dispatches to for the equivalent
// crash-and-restart case), which picks up feedback.md and finishes the stage.
func TestAgentSuggest_NaturalCompletionRaceReconciles(t *testing.T) {
	runDir := t.TempDir()
	stages := []flow.Stage{{
		ID: "impl", Name: "Impl",
		Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation},
	}}

	store, err := state.Open(runDir, []string{"impl"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	stateFile := filepath.Join(runDir, "state.json")

	runner := &naturalCompletionRaceRunner{proceed: make(chan struct{})}
	orch := orchestrator.New(orchestrator.Options{
		RunDir:  runDir,
		Stages:  stages,
		Store:   store,
		Config:  config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
		Runner:  runner,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	runOrchestratorAsync(ctx, t, orch, cancel)

	waitForStatus(t, stateFile, "impl", state.StatusRunning, 10*time.Second)

	// Fires EvRevise (Running -> Revising) and durably saves feedback.md
	// BEFORE this call returns (Revise is synchronous, see control_api.go).
	// The non-blocking send on interruptChans either lands in an empty buffer
	// nobody will read, or is a no-op — call 1's RunAgent below never checks
	// that channel at all.
	if err := orch.Revise(ctx, "impl", "please add extra logging"); err != nil {
		t.Fatalf("Revise: %v", err)
	}
	waitForStatus(t, stateFile, "impl", state.StatusRevising, 5*time.Second)

	// Only now let the "already finishing" agent return nil — reproducing the
	// exact interleaving from the bug report: the natural completion is
	// observed strictly after Revise() has already transitioned the stage.
	close(runner.proceed)

	waitForStatus(t, stateFile, "impl", state.StatusDone, 15*time.Second)

	if runner.calls != 2 {
		t.Fatalf("expected exactly 2 RunAgent calls (natural completion + reconciliation restart), got %d", runner.calls)
	}
	if runner.secondCallFeedback == "" {
		t.Fatal("reconciliation restart did not read feedback.md content")
	}
	if want := "please add extra logging"; !strings.Contains(runner.secondCallFeedback, want) {
		t.Errorf("reconciliation restart's feedback.md = %q, want it to contain %q", runner.secondCallFeedback, want)
	}
}
