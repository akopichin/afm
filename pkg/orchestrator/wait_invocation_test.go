package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

// A stage agent's wait belongs to the invocation that sent it: once that invocation has ended (its token revoked,
// which precedes its turn's exit) the turn has already decided without it, so a late request registers nothing —
// even within the same attempt, where only the next turn or retry would find it.

func TestWaitInvocation_RequestOfAnEndedInvocationRegistersNothing(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	o.opts.DashboardURL = "http://127.0.0.1:0"
	_, token, revoke := o.meshClientFor(spawnStage).Mint()
	revoke() // the process exited while its request was still on its way

	_, err := o.Wait(WithMeshInvocation(context.Background(), token), WaitRequest{
		OperationID: "op-late", Waiter: spawnStage, Actor: spawnStageActor, Condition: anyBoardKeyWait, Deadline: testFarDeadline(),
	})

	requireRejected(t, err, assistants.CodeInvalidStatus)
	if n := len(pendingWaitsOf(store.Snapshot().Assistants.Mesh, spawnStage)); n != 0 {
		t.Fatalf("pending waits = %d, want none", n)
	}
}

// The token is minted before the Revise; the request is built and sent after it (the epoch comes from the mint).
func TestWaitInvocation_TokenMintedBeforeAReviseIsOfTheOldAttempt(t *testing.T) {
	f := newReviseFence(t)
	oldToken := mintStageToken(t, f.o)
	f.revise()

	_, err := f.o.Wait(WithMeshInvocation(context.Background(), oldToken), WaitRequest{
		OperationID: "op-old-attempt", Waiter: spawnStage, Actor: spawnStageActor, Condition: anyBoardKeyWait, Deadline: testFarDeadline(),
	})

	requireRejected(t, err, assistants.CodeInvalidStatus)
}

func TestWaitInvocation_LiveInvocationRegisters(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	o.opts.DashboardURL = "http://127.0.0.1:0"
	_, token, revoke := o.meshClientFor(spawnStage).Mint()
	defer revoke()

	receipt, err := o.Wait(WithMeshInvocation(context.Background(), token), WaitRequest{
		OperationID: "op-live", Waiter: spawnStage, Actor: spawnStageActor, Condition: anyBoardKeyWait, Deadline: testFarDeadline(),
	})

	if err != nil || receipt.Status != agentcomm.WaitPending {
		t.Fatalf("Wait = %+v, %v; want pending", receipt, err)
	}
	if n := len(pendingWaitsOf(store.Snapshot().Assistants.Mesh, spawnStage)); n != 1 {
		t.Fatalf("pending waits = %d, want 1", n)
	}
}

// An invocation killed by a retryable error ends its turn's attempt; its request, delivered while the next attempt
// of the same turn runs (same epoch), must not park that attempt.
func TestWaitInvocation_LateRequestOfAnInvocationKilledByARetryableErrorIsRejected(t *testing.T) {
	runner := &stageScriptRunner{}
	rig := newRecoveryRigVerifying(t, t.TempDir(), runner, flow.VerifySpec{})
	rig.o.opts.DashboardURL = "http://127.0.0.1:0"
	rig.o.retryBackoff = time.Millisecond
	var killedToken string
	var lateErr error
	runner.script = func(stage string, turn int) error {
		if stage != recoveryImpl {
			return nil
		}
		impl := meshStageParticipantID(recoveryImpl)
		if turn == 1 {
			_, token, revoke := rig.o.meshClientFor(impl).Mint()
			defer revoke()
			killedToken = token
			return errors.New("overloaded")
		}
		_, lateErr = rig.o.Wait(WithMeshInvocation(context.Background(), killedToken), WaitRequest{
			OperationID: "op-late-retry", Waiter: impl, Actor: state.Actor{Kind: "agent", ID: string(impl)},
			Condition: anyBoardKeyWait, Deadline: testFarDeadline(),
		})
		return nil
	}
	rig.start()

	rig.waitStage(recoveryDocs, state.StatusDone)
	requireRejected(t, lateErr, assistants.CodeInvalidStatus)
	if got := runner.turnsOf(recoveryImpl); got != 2 {
		t.Fatalf("impl turns = %d, want 2: the failed attempt and its retry", got)
	}
}
