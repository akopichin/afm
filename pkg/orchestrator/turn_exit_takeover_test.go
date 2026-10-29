package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
	"github.com/akopichin/afm/pkg/state"
)

// Whatever a turn's agent returns after Pause, Continue or Revise took the stage over — a fatal or a retryable error,
// or a completion still queued for the event loop — belongs to a turn that no longer owns the stage: it changes
// nothing, except that a Revise of that turn gets its feedback run, once.

// agentErrors are the two ways an agent can fail: the turn either fails the stage or schedules a retry.
var agentErrors = map[string]error{
	"fatal":     fmt.Errorf("%w: claude: no such file", executor.ErrAgentStart),
	"retryable": errors.New("API overloaded, try again"),
}

// newRunningStage is a bare orchestrator (no Run) whose one stage, on the default command, is running with its plan
// written; its turns run on the returned runner.
func newRunningStage(t *testing.T) (*Orchestrator, *state.Store, *scriptedStageRunner) {
	t.Helper()
	o, store := newAssistantsTestOrchestrator(t, true)
	runner := &scriptedStageRunner{turn: func(int32) error { return nil }}
	o.opts.Runner = runner
	o.runner = runner
	// The injected runner stands in only for a stage on the default command.
	o.opts.Stages = []flow.Stage{{ID: assistantsTestStageID}}
	o.graph = graph.NewStore(graph.NewRegistry(o.opts.Stages))
	// New() sets absRunDir; this literal-built harness must, or the peer-wait phase marker lands in the source tree.
	o.absRunDir = o.opts.RunDir
	stageDir := filepath.Join(o.opts.RunDir, assistantsTestStageID)
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("## Tasks\n- build the page\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	activateStage(t, o, assistantsTestStageID)
	return o, store, runner
}

// transitionsOf returns the events of stage's journal transitions that ended in one of statuses.
func transitionsOf(t *testing.T, store *state.Store, stage string, statuses ...state.StageStatus) []string {
	t.Helper()
	history, err := store.History()
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, tr := range history {
		for _, s := range statuses {
			if tr.StageID == stage && tr.To == s {
				found = append(found, fmt.Sprintf("%s→%s (%s)", tr.From, tr.To, tr.Event))
			}
		}
	}
	return found
}

// feedbackClaims counts the feedback runs that claimed a Revise of stage.
func feedbackClaims(t *testing.T, store *state.Store, stage string) int {
	t.Helper()
	history, err := store.History()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, tr := range history {
		if tr.StageID == stage && tr.From == state.StatusRevising && tr.Event == string(bus.EvStartRun) {
			n++
		}
	}
	return n
}

// Pause → Continue while the agent still runs: its later error is not the continuation's, which decides the stage.
func TestTurnExit_ErrorOfATurnTakenOverByPauseContinueChangesNothing(t *testing.T) {
	for name, agentErr := range agentErrors {
		t.Run(name, func(t *testing.T) {
			runner := &stageScriptRunner{}
			rig := newRecoveryRigVerifying(t, t.TempDir(), runner, flow.VerifySpec{})
			runner.script = func(stage string, turn int) error {
				if stage != recoveryImpl || turn != 1 {
					return nil
				}
				if err := rig.o.Pause(context.Background(), stage); err != nil {
					return err
				}
				if err := rig.o.Continue(context.Background(), stage); err != nil {
					return err
				}
				return agentErr
			}
			rig.start()

			waitUntilFor(t, 10*time.Second, "impl to end", func() bool {
				st := rig.store.Get(recoveryImpl)
				return st == state.StatusDone || st == state.StatusFailed
			})
			if got := transitionsOf(t, rig.store, recoveryImpl, state.StatusFailed, state.StatusRetrying); len(got) != 0 {
				t.Fatalf("the taken-over turn moved the continued stage: %v", got)
			}
			if got := rig.store.Get(recoveryImpl); got != state.StatusDone {
				t.Fatalf("impl = %s, want done by the continuation", got)
			}
			if got := runner.turnsOf(recoveryImpl); got != 2 {
				t.Fatalf("impl turns = %d, want 2: the taken-over turn and the continuation", got)
			}
		})
	}
}

// A Revise while the agent still runs, and the agent then fails: the Revise's feedback run takes the stage, once.
func TestTurnExit_ErrorOfARevisedTurnRunsTheFeedbackRunOnce(t *testing.T) {
	for name, agentErr := range agentErrors {
		t.Run(name, func(t *testing.T) {
			runner := &stageScriptRunner{}
			rig := newRecoveryRigVerifying(t, t.TempDir(), runner, flow.VerifySpec{})
			runner.script = func(stage string, turn int) error {
				if stage != recoveryImpl || turn != 1 {
					return nil
				}
				if applied, _, err := rig.o.Revise(context.Background(), stage, "use YAML"); err != nil || !applied {
					return fmt.Errorf("revise = %v, %v", applied, err)
				}
				return agentErr
			}
			rig.start()

			waitUntilFor(t, 10*time.Second, "impl to leave revising", func() bool {
				st := rig.store.Get(recoveryImpl)
				return st == state.StatusDone || st == state.StatusFailed
			})
			if got := rig.store.Get(recoveryImpl); got != state.StatusDone {
				t.Fatalf("impl = %s (transitions %v), want done by the feedback run", got,
					transitionsOf(t, rig.store, recoveryImpl, state.StatusFailed, state.StatusRetrying))
			}
			if got := feedbackClaims(t, rig.store, recoveryImpl); got != 1 {
				t.Fatalf("feedback runs = %d, want 1", got)
			}
			if got := runner.turnsOf(recoveryImpl); got != 2 {
				t.Fatalf("impl turns = %d, want 2: the revised turn and its feedback run", got)
			}
		})
	}
}

// The turn queued its completion with a question left open, and a Revise committed before the event loop handled it.
// The stage cannot go to the user from revising: the completion hands the Revise to its feedback run instead.
func TestTurnExit_CompletionOfARevisedTurnWithAnOpenQuestionRunsTheFeedbackRun(t *testing.T) {
	o, store, runner := newRunningStage(t)
	question := filepath.Join(o.opts.RunDir, assistantsTestStageID, "implementation.q1.question.json")
	if err := os.WriteFile(question, []byte(`{"id":"q1","question":"which one?"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	epoch := o.stageEpoch(assistantsTestStageID)
	if applied, _, err := o.Revise(context.Background(), assistantsTestStageID, "use YAML"); err != nil || !applied {
		t.Fatalf("Revise = %v, %v", applied, err)
	}

	if err := o.onAgentCompleted(context.Background(), bus.Event{
		Type: bus.EventAgentCompleted, StageID: assistantsTestStageID, Data: phaseImplementation, Epoch: &epoch,
	}); err != nil {
		t.Fatal(err)
	}
	o.concurrency.WaitAgents()

	if got := feedbackClaims(t, store, assistantsTestStageID); got != 1 {
		t.Fatalf("feedback runs = %d (status %s), want 1: the Revise was left with no executor", got, store.Get(assistantsTestStageID))
	}
	if got := runner.turns.Load(); got != 1 {
		t.Fatalf("turns = %d, want the feedback run's only", got)
	}
}

// A standalone review's turn queued its completion. A Revise committed before the event loop handled it hands the
// review's feedback run the stage, once; without one the completion finishes the stage as before.
func TestTurnExit_CompletionOfAReviewTurn(t *testing.T) {
	completeReview := func(t *testing.T, o *Orchestrator, epoch uint64) {
		t.Helper()
		if err := o.onAgentCompleted(context.Background(), bus.Event{
			Type: bus.EventAgentCompleted, StageID: assistantsTestStageID, Data: phaseReview, Epoch: &epoch,
		}); err != nil {
			t.Fatal(err)
		}
		o.concurrency.WaitAgents()
	}

	t.Run("revised before it is handled", func(t *testing.T) {
		o, store, runner := newRunningStage(t)
		epoch := o.stageEpoch(assistantsTestStageID)
		if applied, _, err := o.Revise(context.Background(), assistantsTestStageID, "check the edge cases too"); err != nil || !applied {
			t.Fatalf("Revise = %v, %v", applied, err)
		}

		completeReview(t, o, epoch)

		if got := feedbackClaims(t, store, assistantsTestStageID); got != 1 {
			t.Fatalf("feedback runs = %d (status %s), want 1: the Revise was left with no executor", got, store.Get(assistantsTestStageID))
		}
		if got := runner.turns.Load(); got != 1 {
			t.Fatalf("turns = %d, want the feedback run's only", got)
		}
		if got := o.runnerKindOf(assistantsTestStageID); got != kindReview {
			t.Fatalf("feedback run kind = %q, want %q", got, kindReview)
		}
	})

	t.Run("not revised", func(t *testing.T) {
		o, store, runner := newRunningStage(t)

		completeReview(t, o, o.stageEpoch(assistantsTestStageID))

		if got := store.Get(assistantsTestStageID); got != state.StatusDone {
			t.Fatalf("stage = %s, want done", got)
		}
		if claims, turns := feedbackClaims(t, store, assistantsTestStageID), runner.turns.Load(); claims != 0 || turns != 0 {
			t.Fatalf("feedback runs = %d, turns = %d; want none", claims, turns)
		}
	})
}

// A Revise commits after the completion read the stage as running and before its fenced completion: the completion
// loses to the Revise, and the Revise still gets its feedback run, once — for a standalone review and for the
// implementation alike.
func TestTurnExit_ReviseInsideTheCompletionWindowRunsTheFeedbackRun(t *testing.T) {
	for _, tc := range []struct {
		phase, kind string
	}{
		{phaseReview, kindReview},
		{phaseImplementation, kindImplementation},
	} {
		t.Run(tc.phase, func(t *testing.T) {
			o, store, runner := newRunningStage(t)
			epoch := o.stageEpoch(assistantsTestStageID)
			fired := 0
			o.completionStatusReadHook = func(stageID string) {
				fired++
				if applied, _, err := o.Revise(context.Background(), stageID, "one more thing"); err != nil || !applied {
					t.Errorf("Revise = %v, %v", applied, err)
				}
			}

			if err := o.onAgentCompleted(context.Background(), bus.Event{
				Type: bus.EventAgentCompleted, StageID: assistantsTestStageID, Data: tc.phase, Epoch: &epoch,
			}); err != nil {
				t.Fatal(err)
			}
			o.concurrency.WaitAgents()

			if fired != 1 {
				t.Fatalf("the window seam fired %d times, want 1", fired)
			}
			if got := feedbackClaims(t, store, assistantsTestStageID); got != 1 {
				t.Fatalf("feedback runs = %d (status %s), want 1: the Revise was left with no executor", got, store.Get(assistantsTestStageID))
			}
			if got := runner.turns.Load(); got != 1 {
				t.Fatalf("turns = %d, want the feedback run's only", got)
			}
			if got := o.runnerKindOf(assistantsTestStageID); got != tc.kind {
				t.Fatalf("feedback run kind = %q, want %q", got, tc.kind)
			}
		})
	}
}
