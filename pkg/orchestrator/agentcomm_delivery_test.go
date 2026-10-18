package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// Delivery of a run agent's inbox, the run barrier for its unread messages and task_result waits on it.

func addTestRunAgent(t *testing.T, o *Orchestrator, opID string, firstMessage *string) AttachAndSendResult {
	t.Helper()
	res, err := o.AddRunAgent(context.Background(), AddRunAgentRequest{
		OperationID: opID, ProfileID: "qa", Title: "QA", Instruction: "review", FirstMessage: firstMessage, Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Fatalf("AddRunAgent: %v", err)
	}
	return res
}

func receiptOf(store *state.Store, messageID agentcomm.MessageID, recipient agentcomm.ParticipantID) agentcomm.DeliveryStatus {
	return store.Snapshot().Assistants.Mesh.Receipts[messageID][recipient]
}

// A turn whose process never started has delivered nothing: the prompt's messages stay unseen.
func TestInboxDelivery_FailedStartKeepsMessagesUnseen(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		return executor.HelperOutcome{ProcessErr: fmt.Errorf("%w: helper-cmd: not found", executor.ErrAgentStart)}, nil
	}
	msg := "hello"
	res := addTestRunAgent(t, o, "op-add", &msg)
	waitForTaskStatus(t, store, res.TaskID, assistants.StatusFailed, 2*time.Second)

	if got := receiptOf(store, *res.MessageID, res.ParticipantID); got != agentcomm.DeliveryAccepted {
		t.Fatalf("receipt after a failed start = %s, want accepted", got)
	}
}

// OnDelivered marks exactly the messages of the prompt it belongs to: a message that arrives after the prompt
// was built stays unseen and reaches the agent's next turn.
func TestInboxDelivery_MessageArrivingAfterPromptIsNotMarked(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	var (
		calls          atomic.Int32
		res            AttachAndSendResult
		lateID         agentcomm.MessageID
		firstAtStart   agentcomm.DeliveryStatus
		lateAtStart    agentcomm.DeliveryStatus
		secondPromptOK atomic.Bool
	)
	ready := make(chan struct{})
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		<-ready
		if calls.Add(1) == 1 {
			receipt, err := o.Send(ctx, SendRequest{
				OperationID: "op-late", ThreadID: res.ThreadID, Sender: meshUserParticipantID,
				Recipients: []agentcomm.ParticipantID{res.ParticipantID}, Kind: agentcomm.MessageKindMessage, Body: "late note",
				Actor: state.Actor{Kind: "user"},
			})
			if err != nil {
				t.Errorf("Send: %v", err)
			}
			lateID = receipt.MessageID
			o.helperExecutorConfig(assistants.TaskID(taskID), cmd).OnDelivered() // what the executor does at process start
			firstAtStart = receiptOf(store, *res.MessageID, res.ParticipantID)
			lateAtStart = receiptOf(store, lateID, res.ParticipantID)
		} else {
			secondPromptOK.Store(strings.Contains(prompt, "late note"))
		}
		return passResultOutcome(), nil
	}
	msg := "first note"
	res = addTestRunAgent(t, o, "op-add", &msg)
	close(ready)

	deadline := time.Now().Add(2 * time.Second)
	for calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	waitForRunAgentTurn(t, store, res.TaskID, 2*time.Second)

	if firstAtStart != agentcomm.DeliveryDelivered {
		t.Fatalf("prompt message at process start = %s, want delivered", firstAtStart)
	}
	if lateAtStart != agentcomm.DeliveryAccepted {
		t.Fatalf("message that arrived after the prompt = %s at process start, want still accepted", lateAtStart)
	}
	if calls.Load() != 2 || !secondPromptOK.Load() {
		t.Fatalf("turns = %d, late note in the second prompt = %v; want a second turn that reads it", calls.Load(), secondPromptOK.Load())
	}
	if got := receiptOf(store, lateID, res.ParticipantID); got != agentcomm.DeliveryDelivered {
		t.Fatalf("late message after the second turn = %s, want delivered", got)
	}
}

// A runner that never calls OnDelivered (a stub, or any helperAgentRunner) still has its turn's messages
// delivered once the turn ran — otherwise a run agent would restart itself forever.
func TestInboxDelivery_RunnerWithoutOnDeliveredRunsOnce(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	var calls atomic.Int32
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		calls.Add(1)
		return passResultOutcome(), nil
	}
	msg := "hello"
	res := addTestRunAgent(t, o, "op-add", &msg)
	waitForRunAgentTurn(t, store, res.TaskID, 2*time.Second)
	time.Sleep(50 * time.Millisecond)

	if n := calls.Load(); n != 1 {
		t.Fatalf("turns = %d, want 1", n)
	}
	if got := receiptOf(store, *res.MessageID, res.ParticipantID); got != agentcomm.DeliveryDelivered {
		t.Fatalf("receipt = %s, want delivered", got)
	}
}

// A queued run agent with an unread message holds the run open — its next turn is due — while an idle one
// with nothing to read does not.
func TestShouldExit_QueuedRunAgentWithUnreadMessageHoldsRun(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	if _, ok := o.Trigger(assistantsTestStageID, bus.EvComplete, bus.GuardCtx{}, ""); !ok {
		t.Fatal("EvComplete failed")
	}
	res := addTestRunAgent(t, o, "op-add", nil)
	if !o.shouldExit() {
		t.Fatal("an idle run agent with nothing to read must not hold the run open")
	}

	// The message is committed but the agent's next turn is not scheduled yet — the window between a turn's
	// running→queued commit and its follow-up scheduling.
	eff, err := marshalEffect("mesh_message_sent", state.Target{ThreadID: string(res.ThreadID), MessageID: "mm-direct"},
		state.MeshMessageSentPayload{Sender: string(meshUserParticipantID), Recipients: []string{string(res.ParticipantID)}, Kind: string(agentcomm.MessageKindMessage), Body: "one more"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyRunOperation(state.Operation{ID: "op-direct", Kind: "mesh_send", Effects: []state.Effect{eff}}, nil); err != nil {
		t.Fatalf("commit message: %v", err)
	}
	if o.shouldExit() {
		t.Fatal("a queued run agent with an unread message must hold the run open")
	}
}

// A task_result wait on a run agent resolves on a turn's result committed AFTER the wait was registered —
// never on an earlier turn's result, which a waiter asking for new work does not want.
func TestTaskResultWait_RunAgentTurn(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	stubPassingHelper(o)
	res := addTestRunAgent(t, o, "op-add", nil)

	cond := agentcomm.WaitCondition{Kind: agentcomm.WaitOnTaskResult, TaskID: string(res.TaskID)}
	wait := func(id string) WaitReceipt {
		t.Helper()
		r, err := o.Wait(ctx, WaitRequest{OperationID: id, Waiter: meshUserParticipantID, Condition: cond, Actor: state.Actor{Kind: "user"}})
		if err != nil {
			t.Fatalf("Wait %s: %v", id, err)
		}
		return r
	}
	turn := func(id string) {
		t.Helper()
		if _, err := o.Send(ctx, SendRequest{
			OperationID: id, ThreadID: res.ThreadID, Sender: meshUserParticipantID,
			Recipients: []agentcomm.ParticipantID{res.ParticipantID}, Kind: agentcomm.MessageKindMessage, Body: "go", Actor: state.Actor{Kind: "user"},
		}); err != nil {
			t.Fatalf("Send: %v", err)
		}
		waitForRunAgentTurn(t, store, res.TaskID, 2*time.Second)
	}

	first := wait("op-wait-1")
	if first.Status != agentcomm.WaitPending {
		t.Fatalf("wait before any turn = %s, want pending", first.Status)
	}
	turn("op-go-1")
	if got := store.Snapshot().Assistants.Mesh.Waits[first.WaitID].Status; got != agentcomm.WaitReady {
		t.Fatalf("wait registered before the turn = %s after it, want ready", got)
	}

	second := wait("op-wait-2")
	if second.Status != agentcomm.WaitPending {
		t.Fatalf("wait registered after turn 1 = %s, want pending — turn 1's result is not the new result", second.Status)
	}
	turn("op-go-2")
	deadline := time.Now().Add(2 * time.Second)
	for store.Snapshot().Assistants.Mesh.Waits[second.WaitID].Status != agentcomm.WaitReady {
		if time.Now().After(deadline) {
			t.Fatal("wait registered after turn 1 did not resolve on turn 2's result")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A run agent starts every turn with a fresh session: the inbox it reads must not offer a message it was
// already handed in an earlier turn, only new ones — the whole history stays available on request.
func TestReadInbox_OnlyNewSkipsMessagesHandedInEarlierTurns(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	res := addTestRunAgent(t, o, "op-add", nil)

	// Messages are committed directly, so no turn runs on its own; delivery is marked the way a turn's
	// <inbox> (OnDelivered) or `afm agent inbox` marks it.
	commit := func(id, body string) {
		t.Helper()
		eff, err := marshalEffect("mesh_message_sent", state.Target{ThreadID: string(res.ThreadID), MessageID: id},
			state.MeshMessageSentPayload{Sender: string(meshUserParticipantID), Recipients: []string{string(res.ParticipantID)}, Kind: string(agentcomm.MessageKindMessage), Body: body})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.ApplyRunOperation(state.Operation{ID: "op-" + id, Kind: "mesh_send", Effects: []state.Effect{eff}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	deliver := func(id agentcomm.MessageID) {
		t.Helper()
		if err := o.MarkMessageDelivered(ctx, id, res.ParticipantID); err != nil {
			t.Fatal(err)
		}
	}
	read := func(onlyNew bool) []agentcomm.MessageID {
		t.Helper()
		page, err := o.ReadInbox(ctx, ReadInboxRequest{Participant: res.ParticipantID, OnlyNew: onlyNew})
		if err != nil {
			t.Fatal(err)
		}
		var ids []agentcomm.MessageID
		for _, m := range page.Messages {
			ids = append(ids, m.ID)
		}
		return ids
	}

	commit("mm-one", "question one")
	deliver("mm-one") // turn 1 had it in its <inbox>
	if got := read(true); len(got) != 0 {
		t.Fatalf("new messages on turn 2 = %v, want none — turn 1 already handled mm-one", got)
	}

	commit("mm-two", "question two")
	if got := read(true); len(got) != 1 || got[0] != "mm-two" {
		t.Fatalf("new messages = %v, want only mm-two", got)
	}
	if got := read(false); len(got) != 2 {
		t.Fatalf("whole inbox = %v, want both messages", got)
	}
	deliver("mm-two") // what `afm agent inbox` does with what it returned
	if got := read(true); len(got) != 0 {
		t.Fatalf("new messages after reading them = %v, want none", got)
	}
}
