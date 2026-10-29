package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// The turn's own detection of its agent's open question (the question poller has not moved the stage yet): an answer
// or a Revise landing between that detection and the turn leaving the stage with the user. A frozen clock keeps the
// question poller from interfering.

func newAnswerWindowRig(t *testing.T) (*recoveryRig, *stageScriptRunner) {
	t.Helper()
	runner := &stageScriptRunner{}
	rig := newRecoveryRigVerifying(t, t.TempDir(), runner, flow.VerifySpec{})
	freezePoller(rig)
	runner.script = askTheUser(rig, false)
	return rig, runner
}

// The answer is written after the turn saw the question open and before it handed the stage to the user. Never an
// answered question left in awaiting_user_input: the turn sees the answer and parks on its wait.
func TestAnswerWindow_AnswerBeforeTheTurnLeavesIsNotStuck(t *testing.T) {
	rig, runner := newAnswerWindowRig(t)
	answered := make(chan struct{})
	rig.o.dialogHook = func(_ string, left bool) {
		if left {
			return
		}
		if err := answerTheQuestion(rig); err != nil {
			t.Errorf("answer: %v", err)
		}
		close(answered)
	}
	rig.start()

	<-answered
	waitUntilFor(t, 5*time.Second, "impl to settle", func() bool {
		st := rig.store.Get(recoveryImpl)
		return st == state.StatusAwaitingPeer || st == state.StatusAwaitingUserInput
	})
	if got := rig.store.Get(recoveryImpl); got == state.StatusAwaitingUserInput {
		t.Fatalf("impl = %s with its question already answered: nothing will ever resume it", got)
	}
	rig.requireParked()
	if got := runner.turnsOf(recoveryImpl); got != 1 {
		t.Fatalf("impl turns = %d, want 1: the turn that saw the answer parks itself", got)
	}
}

// A Revise lands in the same window: the stage belongs to the Revise's feedback run, never to the user.
func TestAnswerWindow_ReviseBeforeTheTurnLeavesRunsTheFeedbackTurn(t *testing.T) {
	rig, runner := newAnswerWindowRig(t)
	rig.o.dialogHook = func(stage string, left bool) {
		if left {
			return
		}
		if applied, _, err := rig.o.Revise(context.Background(), stage, "do it differently"); err != nil || !applied {
			t.Errorf("Revise = %v, %v", applied, err)
		}
	}
	rig.start()

	waitUntilFor(t, 5*time.Second, "impl's feedback turn", func() bool { return runner.turnsOf(recoveryImpl) >= 2 })
	history, err := rig.store.History()
	if err != nil {
		t.Fatal(err)
	}
	claimed := false
	for _, tr := range history {
		if tr.StageID == recoveryImpl && tr.From == state.StatusRevising && tr.Event == string(bus.EvStartRun) {
			claimed = true
		}
		if tr.StageID == recoveryImpl && tr.From == state.StatusRevising && tr.Event == string(bus.EvAskUser) {
			t.Fatalf("the old turn handed the revised stage to the user (seq %d): its feedback run was lost", tr.Seq)
		}
	}
	if !claimed {
		t.Fatalf("the Revise was never claimed by its feedback run; impl = %s", rig.store.Get(recoveryImpl))
	}
}

// Pause and Continue land in the same window, and the agent wrote no completion marker: the old turn must not hand
// the stage to the user while the continuation's agent runs.
func TestAnswerWindow_PauseContinueBeforeTheTurnLeavesKeepsTheContinuationRunning(t *testing.T) {
	runner := &stageScriptRunner{noCompletion: true}
	rig := newRecoveryRigVerifying(t, t.TempDir(), runner, flow.VerifySpec{})
	freezePoller(rig)
	var statusInContinuation atomic.Value
	runner.script = func(stage string, turn int) error {
		if stage != recoveryImpl {
			return nil
		}
		if turn == 1 {
			question := filepath.Join(rig.runDir, recoveryImpl, "autonomous_execution.q1.question.json")
			return os.WriteFile(question, []byte(`{"id":"q1","question":"which one?"}`), 0o644)
		}
		statusInContinuation.Store(rig.store.Get(recoveryImpl))
		return nil
	}
	var takeOver sync.Once
	rig.o.dialogHook = func(stage string, left bool) {
		if left {
			return
		}
		takeOver.Do(func() {
			if err := rig.o.Pause(context.Background(), stage); err != nil {
				t.Errorf("Pause: %v", err)
			}
			if err := rig.o.Continue(context.Background(), stage); err != nil {
				t.Errorf("Continue: %v", err)
			}
		})
	}
	rig.start()

	waitUntilFor(t, 5*time.Second, "the continuation's turn", func() bool { return statusInContinuation.Load() != nil })
	if got := statusInContinuation.Load(); got != state.StatusRunning {
		t.Fatalf("stage status while the continuation's agent runs = %v, want running: the old turn handed it to the user", got)
	}
}

// The answer races the turn's dialog decision itself, from the moment before it takes answerMu: whichever wins, the
// stage ends parked on its wait, never in awaiting_user_input with the question already answered. A stress test:
// the lock's absence is only visible when the interleaving happens to hit it.
func TestAnswerWindow_AnswerRacingTheDialogDecisionNeverStrandsTheStage(t *testing.T) {
	for i := 0; i < 40; i++ {
		rig, _ := newAnswerWindowRig(t)
		rig.o.dialogHook = func(_ string, left bool) {
			if !left {
				go func() { _ = answerTheQuestion(rig) }()
			}
		}
		rig.start()

		rig.requireParked()
		rig.stop()
	}
}
