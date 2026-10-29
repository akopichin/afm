package orchestrator

import (
	"context"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

// The cycle check sees the stage graph the journal has when the wait is committed, not the in-memory registry read
// before the lock: a stage added in between (its registry install still pending) brings its depends_on edge along.

// addDependentInTheJournalOnly commits docs (depends_on impl) the way CommitAddStage does, but leaves the in-memory
// registry as it was: the moment between AddStageToGraph and the registry install, or a wait that read it before.
func addDependentInTheJournalOnly(t *testing.T, store *state.Store) {
	t.Helper()
	_, err := store.AddStageToGraph(state.StageGraphOperation{ID: "op-add-docs", Payload: state.StageGraphPayload{
		Stage: flow.Stage{ID: "docs", DependsOn: []string{assistantsTestStageID}},
		Order: []string{assistantsTestStageID, "docs"},
	}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWaitGraphLock_StageAddedBeforeTheCommitClosesTheCycle(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	addDependentInTheJournalOnly(t, store)

	requireRejected(t, waitForStageDone(o, "op-impl-waits-docs", spawnStage, "docs"), agentcomm.CodeWaitCycle)
	if n := len(pendingWaitsOf(store.Snapshot().Assistants.Mesh, spawnStage)); n != 0 {
		t.Fatalf("pending waits = %d, want none", n)
	}
}

func TestWaitGraphLock_BatchSeesTheAddedStageToo(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	addDependentInTheJournalOnly(t, store)
	thread := openThread(t, o, "op-th-docs", 0, spawnStage, meshStageParticipantID("docs"))
	sent, err := o.Send(context.Background(), SendRequest{
		OperationID: "op-ask-docs", ThreadID: thread, Sender: spawnStage, Recipients: []agentcomm.ParticipantID{meshStageParticipantID("docs")},
		Kind: agentcomm.MessageKindRequest, Body: "ready?", Actor: spawnStageActor,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = o.Wait(context.Background(), WaitRequest{
		OperationID: "op-batch", Waiter: spawnStage, Actor: spawnStageActor,
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo}, MessageIDs: []agentcomm.MessageID{sent.MessageID},
	})

	requireRejected(t, err, agentcomm.CodeWaitCycle)
}
