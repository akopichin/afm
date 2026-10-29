package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
	"github.com/akopichin/afm/pkg/state"
)

// Revise from awaiting_assistants is two durable records: the transition and the bump of the root generation that
// invalidates the previous attempt's helpers and waits. A restart between them must not lose the bump, and one
// after it must not repeat it.

// reviseTransitionOnly commits the Revise transition and its feedback, then stops: the journal a crash right
// before the bump leaves.
func (f *reviseFence) reviseTransitionOnly() uint64 {
	f.t.Helper()
	_, seq, ok := f.o.triggerWithSeq(assistantsTestStageID, bus.EvRevise, bus.GuardCtx{ExpectFrom: state.StatusAwaitingAssistants}, "try it differently")
	if !ok {
		f.t.Fatal("EvRevise was not applied")
	}
	if err := state.SaveFeedback(filepath.Join(f.o.opts.RunDir, assistantsTestStageID), "try it differently"); err != nil {
		f.t.Fatal(err)
	}
	return seq
}

// restartAndRecover reopens the run in a fresh Orchestrator and recovers the revising stage as startup does.
func (f *reviseFence) restartAndRecover() (*Orchestrator, *state.Store) {
	f.t.Helper()
	if err := f.store.Close(); err != nil {
		f.t.Fatal(err)
	}
	resumed, store := reopenAssistantsTestOrchestrator(f.t, f.o)
	resumed.runner = f.runner
	resumed.absRunDir = resumed.opts.RunDir
	if got := store.Get(assistantsTestStageID); got != state.StatusRevising {
		f.t.Fatalf("status after the restart = %v, want revising", got)
	}
	resumed.resumeStageAtStatus(context.Background(), flow.Stage{ID: assistantsTestStageID}, state.StatusRevising)
	resumed.concurrency.WaitAgents()
	if n := f.runner.turns.Load(); n != 1 {
		f.t.Fatalf("stage turns after recovery = %d, want the feedback turn", n)
	}
	return resumed, store
}

func bumpRecords(t *testing.T, runDir string) int {
	t.Helper()
	return len(journalRecordsWith(t, runDir, "root_generation_bumped"))
}

func TestReviseRecovery_CrashBeforeTheBumpFinishesIt(t *testing.T) {
	f := newReviseFence(t)
	f.reviseTransitionOnly()
	if got := f.store.Snapshot().Assistants.RootGenerations[assistantsTestStageID]; got != 0 {
		t.Fatalf("setup: root generation = %d, want 0 before the bump", got)
	}
	if _, open := f.store.Snapshot().Assistants.RequiredBlocker(assistantsTestStageID); !open {
		t.Fatal("setup: the previous attempt's required helper must still block")
	}

	resumed, store := f.restartAndRecover()

	rs := store.Snapshot()
	if got := rs.Assistants.RootGenerations[assistantsTestStageID]; got != 1 {
		t.Fatalf("root generation after recovery = %d, want 1", got)
	}
	if n := bumpRecords(t, resumed.opts.RunDir); n != 1 {
		t.Fatalf("bump records = %d, want exactly 1", n)
	}
	waitForTaskStatus(t, store, f.required, assistants.StatusCancelled, taskWait)
	if w := rs.Assistants.Mesh.Waits[f.waitID]; w.Status != agentcomm.WaitCancelled || w.Reason != meshWaitSupersededReason {
		t.Fatalf("the previous attempt's wait = %s %q, want cancelled: %s", w.Status, w.Reason, meshWaitSupersededReason)
	}
	if _, open := store.Snapshot().Assistants.RequiredBlocker(assistantsTestStageID); open {
		t.Fatal("the previous attempt's required helper still blocks the new attempt")
	}

	// The journal alone says the same: a second restart replays one bump and runs none.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	replayed, err := state.LoadRunState(resumed.opts.RunDir)
	if err != nil {
		t.Fatalf("LoadRunState: %v", err)
	}
	if got := replayed.Assistants.RootGenerations[assistantsTestStageID]; got != 1 {
		t.Fatalf("replayed root generation = %d, want 1", got)
	}
	if w := replayed.Assistants.Mesh.Waits[f.waitID]; w.Reason != meshWaitSupersededReason {
		t.Fatalf("replayed wait reason = %q, want %q", w.Reason, meshWaitSupersededReason)
	}
	if got := replayed.Assistants.Tasks[f.required].Status; got != assistants.StatusCancelled {
		t.Fatalf("replayed required task = %s, want cancelled", got)
	}
}

func TestReviseRecovery_CrashAfterTheBumpDoesNotBumpAgain(t *testing.T) {
	f := newReviseFence(t)
	seq := f.reviseTransitionOnly()
	if err := f.o.bumpRootGenerationKeyed(assistantsTestStageID, reviseBumpOpID(assistantsTestStageID, seq)); err != nil {
		t.Fatalf("bump: %v", err)
	}

	resumed, store := f.restartAndRecover()

	if got := store.Snapshot().Assistants.RootGenerations[assistantsTestStageID]; got != 1 {
		t.Fatalf("root generation after recovery = %d, want 1: the bump ran twice", got)
	}
	if n := bumpRecords(t, resumed.opts.RunDir); n != 1 {
		t.Fatalf("bump records = %d, want exactly 1", n)
	}
}

// A crash after the bump but before the old tasks were cancelled leaves them running: recovery cancels them, and
// only them — never a task of the new attempt.
func TestReviseRecovery_FinishesTheCancellationAfterTheBump(t *testing.T) {
	f := newReviseFence(t)
	seq := f.reviseTransitionOnly()
	opID := reviseBumpOpID(assistantsTestStageID, seq)
	bumpOnly := state.Operation{ID: opID, Kind: opKindBumpRootGeneration, Effects: []state.Effect{{Kind: "root_generation_bumped", Target: state.Target{StageID: assistantsTestStageID}}}}
	if _, err := f.store.ApplyRunOperation(bumpOnly, nil); err != nil {
		t.Fatalf("bump record: %v", err)
	}
	if got := f.store.Snapshot().Assistants.Tasks[f.required].Status; got != assistants.StatusRunning {
		t.Fatalf("setup: required task = %s, want still running", got)
	}
	// Attached after the bump, before the crash: a task of the new attempt.
	fresh := attachRequiredTask(context.Background(), t, f.o, assistantsTestStageID, "op-required-new-attempt")
	waitForTaskStatus(t, f.store, fresh, assistants.StatusRunning, taskWait)

	_, store := f.restartAndRecover()

	waitForTaskStatus(t, store, f.required, assistants.StatusCancelled, taskWait)
	if got := store.Snapshot().Assistants.Tasks[fresh].Status; got == assistants.StatusCancelled {
		t.Fatal("recovery cancelled a task of the new attempt")
	}
	if got := store.Snapshot().Assistants.RootGenerations[assistantsTestStageID]; got != 1 {
		t.Fatalf("root generation = %d, want 1", got)
	}
}

// The live Revise keys its bump by its own seq: that is what lets recovery tell a committed bump from a missing one.
func TestReviseRecovery_LiveReviseKeysTheBumpBySeq(t *testing.T) {
	f := newReviseFence(t)

	applied, seq, err := f.o.Revise(context.Background(), assistantsTestStageID, "try it differently")
	if err != nil || !applied {
		t.Fatalf("Revise = %v, %v, %v", applied, seq, err)
	}

	if _, committed, err := f.store.CommittedOperation(reviseBumpOpID(assistantsTestStageID, seq), opKindBumpRootGeneration); err != nil || !committed {
		t.Fatalf("bump keyed by the Revise seq committed = %v, %v; want true", committed, err)
	}
	if n := bumpRecords(t, f.o.opts.RunDir); n != 1 {
		t.Fatalf("bump records = %d, want 1", n)
	}
}

// Only a Revise from awaiting_assistants invalidates the previous attempt: one from a running stage keeps its
// helpers, so recovering it must not start bumping.
func TestReviseRecovery_ReviseOfARunningStageDoesNotBump(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	runner := &scriptedStageRunner{turn: func(int32) error { return nil }}
	o.opts.Runner = runner
	o.runner = runner
	o.opts.Stages = []flow.Stage{{ID: assistantsTestStageID}}
	o.graph = graph.NewStore(graph.NewRegistry(o.opts.Stages))
	o.absRunDir = o.opts.RunDir
	if err := os.MkdirAll(filepath.Join(o.opts.RunDir, assistantsTestStageID), 0o755); err != nil {
		t.Fatal(err)
	}
	activateStage(t, o, assistantsTestStageID)
	if _, _, ok := o.triggerWithSeq(assistantsTestStageID, bus.EvRevise, bus.GuardCtx{ExpectFrom: state.StatusRunning}, "feedback"); !ok {
		t.Fatal("EvRevise was not applied")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	resumed, resumedStore := reopenAssistantsTestOrchestrator(t, o)
	resumed.runner = runner
	resumed.absRunDir = resumed.opts.RunDir

	resumed.resumeStageAtStatus(context.Background(), flow.Stage{ID: assistantsTestStageID}, state.StatusRevising)
	resumed.concurrency.WaitAgents()

	if got := resumedStore.Snapshot().Assistants.RootGenerations[assistantsTestStageID]; got != 0 {
		t.Fatalf("root generation = %d, want 0: a Revise of a running stage must not bump", got)
	}
	if n := bumpRecords(t, resumed.opts.RunDir); n != 0 {
		t.Fatalf("bump records = %d, want none", n)
	}
}
