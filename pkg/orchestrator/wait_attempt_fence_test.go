package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
	"github.com/akopichin/afm/pkg/state"
)

// An `afm agent wait` of a stage agent belongs to the attempt of its stage the agent started in. A request that
// was authenticated before a Revise and reaches registration after it must register nothing: the new attempt is
// not parked on a wait of the old one.

// mintStageToken is the token of an agent process the stage starts now: its attempt is the stage's current one.
// The orchestrator needs its DashboardURL set before any goroutine starts: the mesh client reads it unsynchronised.
func mintStageToken(t *testing.T, o *Orchestrator) string {
	t.Helper()
	client := o.meshClientFor(spawnStage)
	if client == nil {
		t.Fatal("no mesh client")
	}
	_, token, _ := client.Mint()
	if token == "" {
		t.Fatal("no token minted")
	}
	return token
}

const fenceDashboardURL = "http://127.0.0.1:0"

// stageWait is the wait request of the stage's agent; it reaches Wait through the invocation's token, as over HTTP.
func stageWait(opID string, cond agentcomm.WaitCondition) WaitRequest {
	return WaitRequest{OperationID: opID, Waiter: spawnStage, Actor: spawnStageActor, Condition: cond, Deadline: testFarDeadline()}
}

func waitWithToken(o *Orchestrator, token string, req WaitRequest) (WaitReceipt, error) {
	return o.Wait(WithMeshInvocation(context.Background(), token), req)
}

var anyBoardKeyWait = agentcomm.WaitCondition{Kind: agentcomm.WaitOnBoardKey, Key: "nobody-published-this", Status: "ready"}

func pendingWaitsOfStage(store *state.Store) int {
	mesh := store.Snapshot().Assistants.Mesh
	return len(pendingWaitsOf(mesh, spawnStage))
}

// Revise of a stage in awaiting_assistants: the request delayed across it, past the bump commit.
func TestWaitAttemptFence_RequestDelayedAcrossReviseFromAwaitingAssistantsRegistersNothing(t *testing.T) {
	f := newReviseFence(t)
	oldToken := mintStageToken(t, f.o)
	delayed := stageWait("op-delayed-wait", anyBoardKeyWait)

	f.revise()
	_, err := waitWithToken(f.o, oldToken, delayed)

	requireRejected(t, err, assistants.CodeInvalidStatus)
	if n := pendingWaitsOfStage(f.store); n != 0 {
		t.Fatalf("pending waits of the stage = %d, want none: the old attempt's request registered one", n)
	}
	if _, ok := f.completionPublished(); !ok {
		t.Fatalf("the feedback turn did not complete; stage status = %v", f.store.Get(assistantsTestStageID))
	}
	if n := f.runner.turns.Load(); n != 1 {
		t.Fatalf("stage turns after Revise = %d, want the feedback turn only", n)
	}
	if got := f.store.Get(assistantsTestStageID); got == state.StatusAwaitingPeer {
		t.Fatal("the new attempt is parked on the old attempt's wait")
	}
}

// The new attempt's own agent has a token of the new attempt, and its wait registers.
func TestWaitAttemptFence_RequestOfTheNewAttemptRegisters(t *testing.T) {
	f := newReviseFence(t)
	f.revise()
	newToken := mintStageToken(t, f.o)

	receipt, err := waitWithToken(f.o, newToken, stageWait("op-new-attempt-wait", anyBoardKeyWait))

	if err != nil || receipt.Status != agentcomm.WaitPending {
		t.Fatalf("Wait of the new attempt = %+v, %v; want pending", receipt, err)
	}
	if n := pendingWaitsOfStage(f.store); n != 1 {
		t.Fatalf("pending waits of the stage = %d, want 1", n)
	}
}

// A batch is refused the same way, as a whole.
func TestWaitAttemptFence_BatchOfAnOldAttemptRegistersNothing(t *testing.T) {
	f := newReviseFence(t)
	oldToken := mintStageToken(t, f.o)
	other := f.send("batch-other")
	delayed := WaitRequest{
		OperationID: "op-delayed-batch", Waiter: spawnStage, Actor: spawnStageActor,
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo}, MessageIDs: []agentcomm.MessageID{other},
	}

	f.revise()
	_, err := waitWithToken(f.o, oldToken, delayed)

	requireRejected(t, err, assistants.CodeInvalidStatus)
	if n := pendingWaitsOfStage(f.store); n != 0 {
		t.Fatalf("pending waits of the stage = %d, want none", n)
	}
}

// Revise of a running stage takes the attempt over too: its interrupted agent's late request registers nothing.
func TestWaitAttemptFence_RequestDelayedAcrossReviseOfARunningStageRegistersNothing(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	o.opts.DashboardURL = fenceDashboardURL
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
	oldToken := mintStageToken(t, o)
	delayed := stageWait("op-delayed-wait", anyBoardKeyWait)

	if applied, _, err := o.Revise(context.Background(), assistantsTestStageID, "use YAML"); err != nil || !applied {
		t.Fatalf("Revise = %v, %v", applied, err)
	}
	_, err := waitWithToken(o, oldToken, delayed)

	requireRejected(t, err, assistants.CodeInvalidStatus)
	if n := pendingWaitsOfStage(store); n != 0 {
		t.Fatalf("pending waits of the stage = %d, want none", n)
	}
	o.concurrency.WaitAgents()
}

// Only a stage agent's own token fences a wait: a request without one, or with another participant's live token, is
// decided like any other. Wait gets only tokens the server authenticated, so a stage token the run does not hold live
// is an ended invocation's: refused.
func TestWaitAttemptFence_OnlyAStageInvocationFencesAWait(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	o.opts.DashboardURL = "http://127.0.0.1:0"
	_, helperToken, _ := o.meshClientFor(meshHelperParticipantID("t-helper")).Mint()

	for name, token := range map[string]string{"no token": "", "a helper's token": helperToken} {
		req := stageWait("op-"+name, anyBoardKeyWait)
		if receipt, err := waitWithToken(o, token, req); err != nil || receipt.Status != agentcomm.WaitPending {
			t.Errorf("%s: Wait = %+v, %v; want pending", name, receipt, err)
		}
	}
	_, err := waitWithToken(o, "not-live", stageWait("op-not-live", anyBoardKeyWait))
	requireRejected(t, err, assistants.CodeInvalidStatus)
}

// A token revoked when its invocation ended no longer speaks for it: the turn's exit decided without the wait.
func TestWaitAttemptFence_RevokedTokenIsRejected(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	o.opts.DashboardURL = "http://127.0.0.1:0"
	_, token, revoke := o.meshClientFor(spawnStage).Mint()
	revoke()

	_, err := waitWithToken(o, token, stageWait("op-revoked", anyBoardKeyWait))

	requireRejected(t, err, assistants.CodeInvalidStatus)
	if n := pendingWaitsOfStage(store); n != 0 {
		t.Fatalf("pending waits of the stage = %d, want none", n)
	}
}
