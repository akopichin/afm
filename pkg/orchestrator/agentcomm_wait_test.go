package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/state"
)

// Agent Mesh Phase 2, T2.3: Wait/CancelWait and the cycle check.

func TestMesh_WaitAlreadySatisfiedIsReadyImmediately(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	h1 := meshHelperParticipantID(newRunningHelperForMesh(t, o, store, "op-w-1a"))
	h2 := meshHelperParticipantID(newRunningHelperForMesh(t, o, store, "op-w-1b"))

	thread, err := o.OpenThread(ctx, OpenThreadRequest{OperationID: "op-w-1-open", Members: []agentcomm.ParticipantID{h1, h2}, Scope: agentcomm.ScopeStage})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	send, err := o.Send(ctx, SendRequest{OperationID: "op-w-1-send", ThreadID: thread.ID, Sender: h1, Recipients: []agentcomm.ParticipantID{h2}, Kind: agentcomm.MessageKindRequest, Body: "ping"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	reply, err := o.Send(ctx, SendRequest{OperationID: "op-w-1-reply", ThreadID: thread.ID, Sender: h2, Recipients: []agentcomm.ParticipantID{h1}, Kind: agentcomm.MessageKindReply, InReplyTo: &send.MessageID, Body: "pong"})
	if err != nil {
		t.Fatalf("Send reply: %v", err)
	}
	_ = reply

	// The reply already exists BEFORE Wait is ever called — the classic "reply arrived before Wait" race.
	receipt, err := o.Wait(ctx, WaitRequest{
		OperationID: "op-w-1-wait", Waiter: h1,
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: send.MessageID},
	})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if receipt.Status != agentcomm.WaitReady {
		t.Fatalf("wait status = %q, want ready (reply already existed)", receipt.Status)
	}
}

func TestMesh_WaitMutualCycleRejected(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	h1 := meshHelperParticipantID(newRunningHelperForMesh(t, o, store, "op-w-2a"))
	h2 := meshHelperParticipantID(newRunningHelperForMesh(t, o, store, "op-w-2b"))

	thread, err := o.OpenThread(ctx, OpenThreadRequest{OperationID: "op-w-2-open", Members: []agentcomm.ParticipantID{h1, h2}, Scope: agentcomm.ScopeStage})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}

	msg1, err := o.Send(ctx, SendRequest{OperationID: "op-w-2-send1", ThreadID: thread.ID, Sender: h1, Recipients: []agentcomm.ParticipantID{h2}, Kind: agentcomm.MessageKindRequest, Body: "q1"})
	if err != nil {
		t.Fatalf("Send 1: %v", err)
	}
	wait1, err := o.Wait(ctx, WaitRequest{OperationID: "op-w-2-wait1", Waiter: h1, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: msg1.MessageID}})
	if err != nil {
		t.Fatalf("Wait 1 (h1 waits on h2): %v", err)
	}
	// Agent Mesh Phase 5, T5.4: wait1 is a genuine, still-PENDING mesh wait registered directly against h1's
	// own participant id — unlike the rest of this test file's fixtures, it is never resolved. Left dangling,
	// it would make newRunningHelperForMesh's cleanup (close(release), expecting h1 to reach StatusCompleted)
	// fail instead: runHelperTask now treats ANY open mesh wait on its own participant as a cooperative yield
	// (hasOpenMeshWaitFor), exactly like T5.2 already does for a stage. Cancel it here, before h1's cleanup
	// runs (t.Cleanup is LIFO — this runs before newRunningHelperForMesh's own registered cleanup for h1).
	t.Cleanup(func() {
		if err := o.CancelWait(ctx, CancelWaitRequest{OperationID: "op-w-2-wait1-cancel", WaitID: wait1.WaitID, Actor: state.Actor{Kind: "agent", ID: string(h1)}}); err != nil {
			t.Fatalf("CancelWait wait1: %v", err)
		}
	})

	msg2, err := o.Send(ctx, SendRequest{OperationID: "op-w-2-send2", ThreadID: thread.ID, Sender: h2, Recipients: []agentcomm.ParticipantID{h1}, Kind: agentcomm.MessageKindRequest, Body: "q2"})
	if err != nil {
		t.Fatalf("Send 2: %v", err)
	}
	_, err = o.Wait(ctx, WaitRequest{OperationID: "op-w-2-wait2", Waiter: h2, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: msg2.MessageID}})
	if err == nil {
		t.Fatal("Wait 2 (h2 waits on h1, who already waits on h2): want wait_cycle rejection, got nil error")
	}
	var rejected *state.OperationRejectedError
	if !errors.As(err, &rejected) || rejected.Code != agentcomm.CodeWaitCycle {
		t.Fatalf("expected a CodeWaitCycle rejection, got %v", err)
	}
}

func TestMesh_WaitOnBoardKeyWithNoProducerRequiresDeadline(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	h1 := meshHelperParticipantID(newRunningHelperForMesh(t, o, store, "op-w-3"))

	_, err := o.Wait(ctx, WaitRequest{
		OperationID: "op-w-3-wait", Waiter: h1,
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnBoardKey, Key: "qa.tests", Status: "ready"},
	})
	if !errors.Is(err, ErrWaitRequiresDeadline) {
		t.Fatalf("Wait on an unpublished board key with no deadline: want ErrWaitRequiresDeadline, got %v", err)
	}
}

func TestMesh_CancelWaitIsIdempotent(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	h1 := meshHelperParticipantID(newRunningHelperForMesh(t, o, store, "op-w-4"))
	h2 := meshHelperParticipantID(newRunningHelperForMesh(t, o, store, "op-w-4b"))

	thread, err := o.OpenThread(ctx, OpenThreadRequest{OperationID: "op-w-4-open", Members: []agentcomm.ParticipantID{h1, h2}, Scope: agentcomm.ScopeStage})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	receipt, err := o.Wait(ctx, WaitRequest{OperationID: "op-w-4-wait", Waiter: h1, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: thread.ID}})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	cancelByWaiter := CancelWaitRequest{WaitID: receipt.WaitID, Actor: state.Actor{Kind: "agent", ID: string(h1)}}
	cancelByWaiter.OperationID = "op-w-4-cancel1"
	if err := o.CancelWait(ctx, cancelByWaiter); err != nil {
		t.Fatalf("CancelWait #1: %v", err)
	}
	cancelByWaiter.OperationID = "op-w-4-cancel2"
	if err := o.CancelWait(ctx, cancelByWaiter); err != nil {
		t.Fatalf("CancelWait #2 (already cancelled): %v", err)
	}
	rs := store.Snapshot()
	if got := rs.Assistants.Mesh.Waits[receipt.WaitID].Status; got != agentcomm.WaitCancelled {
		t.Fatalf("wait status = %q, want cancelled", got)
	}
}

// TestMesh_CancelWaitRejectsNonOwner is a follow-up to the security review round: only the wait's own
// waiter (or the human) may cancel it — otherwise a different participant that merely learns a WaitID could
// permanently livelock someone else's awaiting_peer resume.
func TestMesh_CancelWaitRejectsNonOwner(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	h1 := meshHelperParticipantID(newRunningHelperForMesh(t, o, store, "op-w-5a"))
	h2 := meshHelperParticipantID(newRunningHelperForMesh(t, o, store, "op-w-5b"))

	thread, err := o.OpenThread(ctx, OpenThreadRequest{OperationID: "op-w-5-open", Members: []agentcomm.ParticipantID{h1, h2}, Scope: agentcomm.ScopeStage})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	receipt, err := o.Wait(ctx, WaitRequest{OperationID: "op-w-5-wait", Waiter: h1, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: thread.ID}})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}

	err = o.CancelWait(ctx, CancelWaitRequest{OperationID: "op-w-5-cancel", WaitID: receipt.WaitID, Actor: state.Actor{Kind: "agent", ID: string(h2)}})
	if err == nil {
		t.Fatal("h2 cancelling h1's wait: want rejection, got nil error")
	}
	rs := store.Snapshot()
	if got := rs.Assistants.Mesh.Waits[receipt.WaitID].Status; got != agentcomm.WaitPending {
		t.Fatalf("wait status after a rejected cancel = %q, want still pending", got)
	}

	// The human (run owner) may cancel any wait.
	if err := o.CancelWait(ctx, CancelWaitRequest{OperationID: "op-w-5-cancel-human", WaitID: receipt.WaitID, Actor: state.Actor{Kind: "user"}}); err != nil {
		t.Fatalf("CancelWait by the human: %v", err)
	}
}

// TestMesh_StageStatusWaitResolves: a stage_status wait is ready at once when the stage is already at the
// status, and otherwise resolves on the transition that reaches it.
func TestMesh_StageStatusWaitResolves(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	wait := func(id string, status state.StageStatus) WaitReceipt {
		t.Helper()
		r, err := o.Wait(ctx, WaitRequest{
			OperationID: id, Waiter: meshUserParticipantID, Actor: state.Actor{Kind: "user"},
			Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnStageStatus, StageID: assistantsTestStageID, Status: string(status)},
		})
		if err != nil {
			t.Fatalf("Wait %s: %v", id, err)
		}
		return r
	}

	if r := wait("op-ss-now", state.StatusPending); r.Status != agentcomm.WaitReady {
		t.Fatalf("wait on the status the stage already has = %s, want ready", r.Status)
	}
	later := wait("op-ss-later", state.StatusRunning)
	if later.Status != agentcomm.WaitPending {
		t.Fatalf("wait on a status not reached yet = %s, want pending", later.Status)
	}
	activateStage(t, o, assistantsTestStageID)
	if got := store.Snapshot().Assistants.Mesh.Waits[later.WaitID].Status; got != agentcomm.WaitReady {
		t.Fatalf("wait after the stage reached running = %s, want ready", got)
	}
}

// TestMesh_WaitOnMissingOrClosedTargetRejected: a wait that nothing could ever satisfy is refused instead of
// parking its waiter forever.
func TestMesh_WaitOnMissingOrClosedTargetRejected(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	stage := meshStageParticipantID(assistantsTestStageID)
	h1 := meshHelperParticipantID(newRunningHelperForMesh(t, o, store, "op-wt-h1"))
	closed, err := o.OpenThread(ctx, OpenThreadRequest{OperationID: "op-wt-open", Members: []agentcomm.ParticipantID{meshUserParticipantID, stage}, Scope: agentcomm.ScopeStage})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	question, err := o.Send(ctx, SendRequest{OperationID: "op-wt-q", ThreadID: closed.ID, Sender: meshUserParticipantID, Recipients: []agentcomm.ParticipantID{stage}, Kind: agentcomm.MessageKindRequest, Body: "q"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if err := o.CloseThread(ctx, CloseThreadRequest{OperationID: "op-wt-close", ThreadID: closed.ID}); err != nil {
		t.Fatalf("CloseThread: %v", err)
	}

	cases := []struct {
		name string
		cond agentcomm.WaitCondition
		want string
	}{
		{"unknown message", agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: "mm-nope"}, "not_found"},
		{"question in a closed thread", agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: question.MessageID}, "invalid_status"},
		{"closed thread", agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: closed.ID}, "invalid_status"},
		{"unknown task", agentcomm.WaitCondition{Kind: agentcomm.WaitOnTaskResult, TaskID: "t-nope"}, "not_found"},
		{"unknown stage", agentcomm.WaitCondition{Kind: agentcomm.WaitOnStageStatus, StageID: "nope", Status: "done"}, "not_found"},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := o.Wait(ctx, WaitRequest{OperationID: fmt.Sprintf("op-wt-%d", i), Waiter: h1, Condition: tc.cond})
			var rej *state.OperationRejectedError
			if !errors.As(err, &rej) || string(rej.Code) != tc.want {
				t.Fatalf("Wait: err = %v, want a %s rejection", err, tc.want)
			}
			for _, w := range store.Snapshot().Assistants.Mesh.Waits {
				if w.Waiter == h1 && w.Condition == tc.cond {
					t.Fatalf("the rejected wait was registered: %+v", w)
				}
			}
		})
	}
}

// meshWaitCycleState is a run with stages s0..s<n-1> and the given pending waits, for the cycle check alone.
func meshWaitCycleState(n int, waits ...*state.MeshWait) state.RunState {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("s%d", i)
	}
	rs := *state.NewRunState(ids)
	rs.Assistants.Mesh.Waits = map[agentcomm.WaitID]*state.MeshWait{}
	for i, w := range waits {
		w.ID = agentcomm.WaitID(fmt.Sprintf("w%d", i))
		w.Status = agentcomm.WaitPending
		rs.Assistants.Mesh.Waits[w.ID] = w
	}
	return rs
}

func stageStatusWait(waiter, stageID string) *state.MeshWait {
	return &state.MeshWait{Wait: agentcomm.Wait{
		Waiter: meshStageParticipantID(waiter), Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnStageStatus, StageID: stageID, Status: "done"},
	}}
}

// TestMesh_WaitCycleCheckRejectsChainLongerThanItsHops: a cycle longer than MaxCausalHops must not slip past
// the check because the walk stopped — running out of hops counts as a possible cycle.
func TestMesh_WaitCycleCheckRejectsChainLongerThanItsHops(t *testing.T) {
	n := agentcomm.MaxCausalHops + 2 // s1 -> s2 -> ... -> s13 -> s0: 13 hops back to the waiter
	var waits []*state.MeshWait
	for i := 1; i < n; i++ {
		waits = append(waits, stageStatusWait(fmt.Sprintf("s%d", i), fmt.Sprintf("s%d", (i+1)%n)))
	}
	newWait := WaitRequest{Waiter: meshStageParticipantID("s0"), Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnStageStatus, StageID: "s1", Status: "done"}}

	err := validateWaitAdmission(meshWaitCycleState(n, waits...), newWait)
	var rej *state.OperationRejectedError
	if !errors.As(err, &rej) || rej.Code != agentcomm.CodeWaitCycle {
		t.Fatalf("a %d-hop cycle: err = %v, want wait_cycle", n-1, err)
	}

	short := waits[:4] // s1 -> ... -> s5, and s5 waits on nobody
	if err := validateWaitAdmission(meshWaitCycleState(n, short...), newWait); err != nil {
		t.Fatalf("a chain that ends at a free participant is no cycle: %v", err)
	}
}

// TestMesh_WaitCycleCheckAnyGroupMemberMayAnswer: a wait on a group conversation is satisfied by any other
// member. One member waiting on the waiter does not make it a cycle while another member is free to answer.
func TestMesh_WaitCycleCheckAnyGroupMemberMayAnswer(t *testing.T) {
	a, b, c := meshStageParticipantID("s0"), meshStageParticipantID("s1"), meshStageParticipantID("s2")
	groupWait := WaitRequest{Waiter: a, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: "mt-group"}}
	withGroup := func(waits ...*state.MeshWait) state.RunState {
		rs := meshWaitCycleState(3, waits...)
		rs.Assistants.Mesh.Threads = map[agentcomm.ThreadID]*agentcomm.Thread{
			"mt-group": {ID: "mt-group", Scope: agentcomm.ScopeRun, Members: []agentcomm.ParticipantID{a, b, c}, Status: agentcomm.ThreadOpen},
		}
		return rs
	}

	if err := validateWaitAdmission(withGroup(stageStatusWait("s1", "s0")), groupWait); err != nil {
		t.Fatalf("b waits on a, but c is free to answer: %v", err)
	}
	err := validateWaitAdmission(withGroup(stageStatusWait("s1", "s0"), stageStatusWait("s2", "s0")), groupWait)
	var rej *state.OperationRejectedError
	if !errors.As(err, &rej) || rej.Code != agentcomm.CodeWaitCycle {
		t.Fatalf("every other member waits on a: err = %v, want wait_cycle", err)
	}
}
