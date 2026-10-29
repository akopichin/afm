package orchestrator

import (
	"context"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
)

// A turn that registered a wait which was already answered before it could park is a yield only when it left no
// final text. A non-empty answer that does not decode is a protocol error, wait or no wait: treating it as a
// yield would rerun the agent on the same malformed output forever.
func TestTurnYielded_ReadyWaitNeedsAnAbsentFinalText(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	ctx := context.Background()
	waiter := meshStageParticipantID("s1")
	before := o.meshWaitStatusesOf(waiter)
	th, err := o.OpenThread(ctx, OpenThreadRequest{OperationID: "op-th", Members: []agentcomm.ParticipantID{waiter, meshUserParticipantID}, Scope: agentcomm.ScopeStage})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.Wait(ctx, WaitRequest{OperationID: "op-w", Waiter: waiter, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: th.ID}, Deadline: testFarDeadline()}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.Send(ctx, SendRequest{OperationID: "op-s", ThreadID: th.ID, Sender: meshUserParticipantID,
		Recipients: []agentcomm.ParticipantID{waiter}, Kind: agentcomm.MessageKindMessage, Body: "answer"}); err != nil {
		t.Fatal(err)
	}
	if o.hasOpenMeshWaitFor(waiter) {
		t.Fatal("setup: the wait should already be ready")
	}
	if !o.turnYielded(waiter, before, "") {
		t.Fatal("no final text after registering a wait is a yield")
	}
	if o.turnYielded(waiter, before, "{not json") {
		t.Fatal("a non-empty malformed answer is a protocol error, not a yield")
	}

	// An empty exit after waits that all ended before the turn is a protocol error, not a rerun.
	if o.turnYielded(waiter, o.meshWaitStatusesOf(waiter), "") {
		t.Fatal("no final text after the waits already ended is not a yield")
	}
}
