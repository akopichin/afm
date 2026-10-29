package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/state"
)

// Recovery finishes what a crash cut short by the journal record that started it, whatever status the stage has
// moved to since: a Revise's bump after a Pause, a yielded turn's park after a retry was scheduled.

func TestRecoveryAnyStatus_RevisePausedBeforeTheBumpIsFinishedAtStartup(t *testing.T) {
	f := newReviseFence(t)
	f.reviseTransitionOnly()
	if err := f.o.Pause(context.Background(), assistantsTestStageID); err != nil {
		t.Fatal(err)
	}
	if got := f.store.Get(assistantsTestStageID); got != state.StatusPaused {
		t.Fatalf("setup: status = %s, want paused", got)
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	resumed, store := reopenAssistantsTestOrchestrator(t, f.o)
	resumed.runner = f.runner
	resumed.absRunDir = resumed.opts.RunDir

	resumed.startPlanningForPending(context.Background())
	resumed.concurrency.WaitAgents()

	if got := store.Snapshot().Assistants.RootGenerations[assistantsTestStageID]; got != 1 {
		t.Fatalf("root generation after a restart of the paused Revise = %d, want 1", got)
	}
	waitForTaskStatus(t, store, f.required, assistants.StatusCancelled, taskWait)
	if _, open := store.Snapshot().Assistants.RequiredBlocker(assistantsTestStageID); open {
		t.Fatal("the previous attempt's required helper still blocks the revised stage")
	}
	if got := store.Get(assistantsTestStageID); got != state.StatusPaused {
		t.Fatalf("status = %s, want still paused: recovery finishes the bump, it does not continue the stage", got)
	}
}

func TestRecoveryAnyStatus_RetryingVerifiedStageWithAPendingWaitParks(t *testing.T) {
	runDir := t.TempDir()
	runner := &stageScriptRunner{}
	rig := newRecoveryRig(t, runDir, runner)
	runner.script = rig.yieldOnce
	rig.start()
	rig.requireParked()
	rig.stop()
	if err := rig.store.Apply(&state.Transition{StageID: recoveryImpl, From: state.StatusAwaitingPeer, To: state.StatusRetrying, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	if err := rig.store.Close(); err != nil {
		t.Fatal(err)
	}

	second := &stageScriptRunner{}
	restarted := newRecoveryRig(t, runDir, second)
	restarted.start()

	restarted.requireParked()
	if got := second.turnsOf(recoveryImpl); got != 0 {
		t.Fatalf("impl turns after the restart = %d, want none: the stage waits for its colleague first", got)
	}
}

// Continue of a stage paused while retrying resumes through the same recovery path: a turn that yielded on a wait
// goes on waiting for it instead of running another author turn.
func TestRecoveryAnyStatus_ContinueOfAPausedRetryingStageWithAPendingWaitParks(t *testing.T) {
	runner := &stageScriptRunner{}
	rig := newRecoveryRig(t, t.TempDir(), runner)
	runner.script = rig.yieldOnce
	rig.start()
	rig.requireParked()
	if err := rig.store.Apply(&state.Transition{StageID: recoveryImpl, From: state.StatusAwaitingPeer, To: state.StatusRetrying, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	if err := rig.o.Pause(context.Background(), recoveryImpl); err != nil {
		t.Fatal(err)
	}
	if got := rig.store.Get(recoveryImpl); got != state.StatusPaused {
		t.Fatalf("setup: status = %s, want paused", got)
	}

	if err := rig.o.Continue(context.Background(), recoveryImpl); err != nil {
		t.Fatal(err)
	}

	rig.requireParked()
	if got := runner.turnsOf(recoveryImpl); got != 1 {
		t.Fatalf("impl turns = %d, want 1: Continue ran another turn instead of waiting for the colleague", got)
	}
}

// Continue of a stage paused while its turn, which had yielded on a wait, was still exiting: the turn never parked, and
// the stage goes on waiting for the colleague instead of running another author turn.
func TestRecoveryAnyStatus_ContinueOfAPausedRunningStageWithAPendingWaitParks(t *testing.T) {
	runner := &stageScriptRunner{}
	rig := newRecoveryRig(t, t.TempDir(), runner)
	runner.script = func(stage string, turn int) error {
		if err := rig.yieldOnce(stage, turn); err != nil || stage != recoveryImpl || turn != 1 {
			return err
		}
		return rig.o.Pause(context.Background(), stage)
	}
	rig.start()
	rig.waitStage(recoveryImpl, state.StatusPaused)
	waitUntilFor(t, 5*time.Second, "the paused turn to end", func() bool { return !rig.o.concurrency.IsActive(recoveryImpl) })
	if got := rig.o.opts.Store.PausedFrom(recoveryImpl); got != state.StatusRunning {
		t.Fatalf("setup: paused from %s, want running", got)
	}

	if err := rig.o.Continue(context.Background(), recoveryImpl); err != nil {
		t.Fatal(err)
	}

	rig.requireParked()
	if got := runner.turnsOf(recoveryImpl); got != 1 {
		t.Fatalf("impl turns = %d, want 1: Continue ran another turn instead of waiting for the colleague", got)
	}
}
