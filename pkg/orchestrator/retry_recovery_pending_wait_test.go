package orchestrator

import (
	"testing"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

// A stage whose process stopped during retry backoff, with a valid completion marker, completes on recovery
// only when it waits for nobody: a wait it left open parks it first, exactly as the exit of a live turn would.

// restartDuringRetryBackoff runs impl until it yields on a wait and parks, stops the process, optionally lets
// the owner cancel the wait, and leaves the journal as a stop during retry backoff would: impl retrying, with
// the marker its turn wrote still on disk.
func restartDuringRetryBackoff(t *testing.T, cancelWait bool) (runDir string) {
	t.Helper()
	runDir = t.TempDir()
	runner := &stageScriptRunner{}
	rig := newRecoveryRigVerifying(t, runDir, runner, flow.VerifySpec{})
	runner.script = rig.yieldOnce
	rig.start()
	rig.requireParked()
	rig.stop()
	if cancelWait {
		rig.ownerCancels("req-cancel-before-restart")
	}
	if err := rig.store.Apply(&state.Transition{StageID: recoveryImpl, From: state.StatusAwaitingPeer, To: state.StatusRetrying, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	if err := rig.store.Close(); err != nil {
		t.Fatal(err)
	}
	return runDir
}

func TestRetryRecovery_PendingWaitParksTheStageInsteadOfCompleting(t *testing.T) {
	runDir := restartDuringRetryBackoff(t, false)

	second := &stageScriptRunner{}
	restarted := newRecoveryRigVerifying(t, runDir, second, flow.VerifySpec{})
	restarted.start()

	restarted.requireParked()
	if got := second.turnsOf(recoveryImpl); got != 0 {
		t.Fatalf("impl turns after the restart = %d, want none: the stage waits, it is not rerun", got)
	}

	// The wait ends: the stage continues with one turn and completes normally, its dependent after it.
	restarted.ownerCancels("req-cancel-after-restart")
	restarted.waitStage(recoveryDocs, state.StatusDone)
	if got := second.turnsOf(recoveryImpl); got != 1 {
		t.Fatalf("impl turns = %d, want the single continuation", got)
	}
}

func TestRetryRecovery_WithoutPendingWaitsTheMarkerStillCompletes(t *testing.T) {
	runDir := restartDuringRetryBackoff(t, true)

	second := &stageScriptRunner{}
	restarted := newRecoveryRigVerifying(t, runDir, second, flow.VerifySpec{})
	restarted.start()

	restarted.waitStage(recoveryDocs, state.StatusDone)
	if got := second.turnsOf(recoveryImpl); got != 0 {
		t.Fatalf("impl turns after the restart = %d, want none: the marker completes it", got)
	}
}
