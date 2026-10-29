package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"slices"
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

// Agent Mesh Phase 2, T2.3: Wait/CancelWait and the cycle check.

// A batch's own status: pending while any item is, ready once every item finished before its deadline — answered,
// cancelled, or a mix — and expired only when the deadline actually ended at least one item.
func TestBatchStatus(t *testing.T) {
	item := func(s agentcomm.WaitStatus) WaitItemReceipt { return WaitItemReceipt{Status: s} }
	cases := []struct {
		name  string
		items []WaitItemReceipt
		want  agentcomm.WaitStatus
	}{
		{"all ready", []WaitItemReceipt{item(agentcomm.WaitReady), item(agentcomm.WaitReady)}, agentcomm.WaitReady},
		{"all cancelled", []WaitItemReceipt{item(agentcomm.WaitCancelled), item(agentcomm.WaitCancelled)}, agentcomm.WaitReady},
		{"ready mixed with cancelled", []WaitItemReceipt{item(agentcomm.WaitReady), item(agentcomm.WaitCancelled)}, agentcomm.WaitReady},
		{"any pending wins", []WaitItemReceipt{item(agentcomm.WaitReady), item(agentcomm.WaitPending)}, agentcomm.WaitPending},
		{"any expired wins even with a ready item", []WaitItemReceipt{item(agentcomm.WaitReady), item(agentcomm.WaitExpired)}, agentcomm.WaitExpired},
		{"pending outranks expired", []WaitItemReceipt{item(agentcomm.WaitExpired), item(agentcomm.WaitPending)}, agentcomm.WaitPending},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := batchStatus(c.items); got != c.want {
				t.Fatalf("batchStatus(%v) = %s, want %s", c.items, got, c.want)
			}
		})
	}
}

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

	err := validateWaitAdmission(meshWaitCycleState(n, waits...), newWait, nil)
	var rej *state.OperationRejectedError
	if !errors.As(err, &rej) || rej.Code != agentcomm.CodeWaitCycle {
		t.Fatalf("a %d-hop cycle: err = %v, want wait_cycle", n-1, err)
	}

	short := waits[:4] // s1 -> ... -> s5, and s5 waits on nobody
	if err := validateWaitAdmission(meshWaitCycleState(n, short...), newWait, nil); err != nil {
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

	if err := validateWaitAdmission(withGroup(stageStatusWait("s1", "s0")), groupWait, nil); err != nil {
		t.Fatalf("b waits on a, but c is free to answer: %v", err)
	}
	err := validateWaitAdmission(withGroup(stageStatusWait("s1", "s0"), stageStatusWait("s2", "s0")), groupWait, nil)
	var rej *state.OperationRejectedError
	if !errors.As(err, &rej) || rej.Code != agentcomm.CodeWaitCycle {
		t.Fatalf("every other member waits on a: err = %v, want wait_cycle", err)
	}
}

// replyWait is a pending reply_to wait of waiter on message msgID.
func replyWait(waiter agentcomm.ParticipantID, msgID agentcomm.MessageID) *state.MeshWait {
	return &state.MeshWait{Wait: agentcomm.Wait{
		Waiter: waiter, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: msgID},
	}}
}

// taskResultWait is a pending task_result wait of waiter on the task taskID.
func taskResultWait(waiter agentcomm.ParticipantID, taskID assistants.TaskID) *state.MeshWait {
	return &state.MeshWait{Wait: agentcomm.Wait{
		Waiter: waiter, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnTaskResult, TaskID: string(taskID)},
	}}
}

// meshAllWaitsState is a run where each message in recipients (id -> addressees) exists and every task in
// runningTasks is a running stage helper, plus the given pending waits.
func meshAllWaitsState(recipients map[agentcomm.MessageID][]agentcomm.ParticipantID, runningTasks []assistants.TaskID, waits ...*state.MeshWait) state.RunState {
	rs := meshWaitCycleState(1, waits...)
	rs.Assistants.Mesh.Messages = map[agentcomm.MessageID]*agentcomm.Message{}
	for id, to := range recipients {
		rs.Assistants.Mesh.Messages[id] = &agentcomm.Message{ID: id, ThreadID: "mt-all", Recipients: to}
	}
	rs.Assistants.Tasks = map[assistants.TaskID]*state.TaskState{}
	for _, id := range runningTasks {
		rs.Assistants.Tasks[id] = &state.TaskState{Task: assistants.Task{ID: id, Status: assistants.StatusRunning}}
	}
	return rs
}

// TestMesh_WaitCycleTaskRequiresAllPendingWaits: a helper or run agent resumes only once ALL its pending waits
// are cleared (resumeTaskIfWaitCleared), so with two waits A->B and A->C a new wait B->A is a deadlock even
// though C is free — C answering still leaves A waiting on B. A stage or side participant resumes on its first
// cleared wait and keeps any-of. Several recipients of ONE message stay any-of for everyone.
func TestMesh_WaitCycleTaskRequiresAllPendingWaits(t *testing.T) {
	const (
		b agentcomm.ParticipantID = "helper:b"
		c agentcomm.ParticipantID = "helper:c"
	)
	for _, tc := range []struct {
		name      string
		a         agentcomm.ParticipantID
		waitsOfA  func(a agentcomm.ParticipantID) []*state.MeshWait
		wantCycle bool
	}{
		{"helper with two waits", "helper:a", twoReplyWaits, true},
		{"run agent with two waits", "run:t-a", twoReplyWaits, true},
		{"legacy run:<profileID> with two waits", "run:qa", twoReplyWaits, true},
		{"one wait on the free participant", "helper:a", func(a agentcomm.ParticipantID) []*state.MeshWait {
			return []*state.MeshWait{replyWait(a, "m-ac")}
		}, false},
		{"one group message wait, other member free", "helper:a", func(a agentcomm.ParticipantID) []*state.MeshWait {
			return []*state.MeshWait{replyWait(a, "m-group")}
		}, false},
		{"stage with two waits stays any-of", "stage:a", twoReplyWaits, false},
		{"side agent with two waits stays any-of", "side:a", twoReplyWaits, false},
		{"reply_to plus task_result", "helper:a", func(a agentcomm.ParticipantID) []*state.MeshWait {
			return []*state.MeshWait{replyWait(a, "m-ab"), taskResultWait(a, "c")}
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recipients := map[agentcomm.MessageID][]agentcomm.ParticipantID{
				"m-ab": {b}, "m-ac": {c}, "m-group": {b, c}, "m-ba": {tc.a},
			}
			rs := meshAllWaitsState(recipients, []assistants.TaskID{"c"}, tc.waitsOfA(tc.a)...)

			err := validateWaitAdmission(rs, WaitRequest{Waiter: b, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: "m-ba"}}, nil)

			var rej *state.OperationRejectedError
			isCycle := errors.As(err, &rej) && rej.Code == agentcomm.CodeWaitCycle
			if tc.wantCycle != isCycle || (!tc.wantCycle && err != nil) {
				t.Fatalf("B waits on A: err = %v, want wait_cycle = %v", err, tc.wantCycle)
			}
		})
	}
}

// twoReplyWaits: a waits on b's message m-ab and on c's message m-ac.
func twoReplyWaits(a agentcomm.ParticipantID) []*state.MeshWait {
	return []*state.MeshWait{replyWait(a, "m-ab"), replyWait(a, "m-ac")}
}

// testFarDeadline is a deadline that never fires in a test — for a wait whose thread is not open yet, which
// needs one to be admitted.
func testFarDeadline() *time.Time {
	d := time.Now().Add(time.Hour)
	return &d
}

// TestMesh_WaitAdmissionRejectsTerminallyImpossibleWaits: a stage already finished in another status, a wait
// only the waiter itself could satisfy, and a wait on a thread not opened yet without a deadline are refused;
// a condition that already holds is admitted (it is ready at once).
func TestMesh_WaitAdmissionRejectsTerminallyImpossibleWaits(t *testing.T) {
	rs := *state.NewRunState([]string{"build", "docs"})
	rs.Stages["build"] = state.StageState{Status: state.StatusDone}
	rs.Stages["docs"] = state.StageState{Status: state.StatusRunning}
	self := meshStageParticipantID("docs")
	other := meshStageParticipantID("build")

	cases := []struct {
		name     string
		waiter   agentcomm.ParticipantID
		cond     agentcomm.WaitCondition
		deadline *time.Time
		want     string // "" = admitted
	}{
		{"finished stage, other status", other, agentcomm.WaitCondition{Kind: agentcomm.WaitOnStageStatus, StageID: "build", Status: "failed"}, nil, "invalid_status"},
		{"finished stage, its own status", other, agentcomm.WaitCondition{Kind: agentcomm.WaitOnStageStatus, StageID: "build", Status: "done"}, nil, ""},
		{"stage waits for its own done", self, agentcomm.WaitCondition{Kind: agentcomm.WaitOnStageStatus, StageID: "docs", Status: "done"}, nil, string(agentcomm.CodeWaitCycle)},
		{"stage at the status it waits for", self, agentcomm.WaitCondition{Kind: agentcomm.WaitOnStageStatus, StageID: "docs", Status: "running"}, nil, ""},
		{"thread not open, no deadline", self, agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: "mt-later"}, nil, "invalid_request"},
		{"thread not open, with deadline", self, agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: "mt-later"}, testFarDeadline(), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateWaitAdmission(rs, WaitRequest{Waiter: tc.waiter, Condition: tc.cond, Deadline: tc.deadline}, nil)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("admission: %v, want admitted", err)
				}
				return
			}
			var rej *state.OperationRejectedError
			if !errors.As(err, &rej) || string(rej.Code) != tc.want {
				t.Fatalf("admission: err = %v, want a %s rejection", err, tc.want)
			}
		})
	}
}

// ---- Batch waits: one registration for several requests, one continuation after all of them ended. ----

// batchFixture is stage "s1" that sent requests to the human (a participant that always exists) and is about to
// wait for the answers, with the critical bus watched for what resumes the stage.
type batchFixture struct {
	t        *testing.T
	o        *Orchestrator
	events   <-chan bus.Event
	thread   agentcomm.ThreadID
	stage    agentcomm.ParticipantID
	requests []agentcomm.MessageID
}

func newBatchFixture(t *testing.T, requests int) *batchFixture {
	t.Helper()
	o, _ := setupHookOrch(t, "s1")
	f := &batchFixture{t: t, o: o, events: o.critical.Recv(), stage: meshStageParticipantID("s1")}
	thread, err := o.OpenThread(context.Background(), OpenThreadRequest{
		OperationID: "op-batch-thread", Members: []agentcomm.ParticipantID{f.stage, meshUserParticipantID}, Scope: agentcomm.ScopeStage,
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	f.thread = thread.ID
	for i := range requests {
		sent, err := o.Send(context.Background(), SendRequest{
			OperationID: fmt.Sprintf("op-batch-ask-%d", i), ThreadID: f.thread, Sender: f.stage,
			Recipients: []agentcomm.ParticipantID{meshUserParticipantID}, Kind: agentcomm.MessageKindRequest, Body: "please review",
			Actor: state.Actor{Kind: "assistant", ID: string(f.stage)},
		})
		if err != nil {
			t.Fatalf("Send request %d: %v", i, err)
		}
		f.requests = append(f.requests, sent.MessageID)
	}
	return f
}

// wait registers one batch wait on the requests with the given indexes.
func (f *batchFixture) wait(opID string, deadline *time.Time, indexes ...int) (WaitReceipt, error) {
	var ids []agentcomm.MessageID
	for _, i := range indexes {
		ids = append(ids, f.requests[i])
	}
	return f.o.Wait(context.Background(), WaitRequest{
		OperationID: opID, Waiter: f.stage, Deadline: deadline,
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo}, MessageIDs: ids,
	})
}

func (f *batchFixture) mustWait(opID string, deadline *time.Time, indexes ...int) WaitReceipt {
	f.t.Helper()
	receipt, err := f.wait(opID, deadline, indexes...)
	if err != nil {
		f.t.Fatalf("batch Wait: %v", err)
	}
	return receipt
}

func receiptWaitIDs(receipt WaitReceipt) []agentcomm.WaitID {
	ids := make([]agentcomm.WaitID, len(receipt.Items))
	for i, item := range receipt.Items {
		ids[i] = item.WaitID
	}
	return ids
}

// park is what the exit of the yielding turn does: the stage goes to awaiting_peer on the episode.
func (f *batchFixture) park(episode []agentcomm.WaitID) {
	f.t.Helper()
	f.o.prePeerPhase.Store("s1", phaseImplementation)
	if _, ok := f.o.Trigger("s1", bus.EvAwaitPeer, bus.GuardCtx{Phase: phaseImplementation, PeerWaits: episode}, ""); !ok {
		f.t.Fatal("EvAwaitPeer failed")
	}
	f.continuations() // drops the park's own status event
}

// reply is the human answering request i.
func (f *batchFixture) reply(i int) {
	f.t.Helper()
	if _, err := f.o.Send(context.Background(), SendRequest{
		OperationID: fmt.Sprintf("op-batch-reply-%d", i), ThreadID: f.thread, Sender: meshUserParticipantID,
		Recipients: []agentcomm.ParticipantID{f.stage}, Kind: agentcomm.MessageKindReply, InReplyTo: &f.requests[i], Body: "done",
	}); err != nil {
		f.t.Fatalf("reply %d: %v", i, err)
	}
}

// continuations lists, in order, the events that would resume the stage and arrived within a short grace window.
// An event repeated right after itself counts once: both a reply's resume and the question check may publish
// it for the same park, and the park's ParkSeq lets the first one handled win — one turn however many arrive.
func (f *batchFixture) continuations() []bus.EventType {
	var got []bus.EventType
	deadline := time.After(150 * time.Millisecond)
	for {
		select {
		case ev := <-f.events:
			switch ev.Type {
			case bus.EventPeerReplied, bus.EventPeerQuestion, bus.EventPeerWaitEnded:
				if ev.StageID == "s1" && (len(got) == 0 || got[len(got)-1] != ev.Type) {
					got = append(got, ev.Type)
				}
			default:
			}
		case <-deadline:
			return got
		}
	}
}

func (f *batchFixture) requireResumes(want ...bus.EventType) {
	f.t.Helper()
	if got := f.continuations(); !slices.Equal(got, want) {
		f.t.Fatalf("stage resumes = %v, want %v", got, want)
	}
}

func (f *batchFixture) waitStatus(id agentcomm.WaitID) agentcomm.WaitStatus {
	return f.o.opts.Store.Snapshot().Assistants.Mesh.Waits[id].Status
}

// One Wait call registers one durable wait per request under a shared batch id, in request order; the answers
// that came before it count at once, like a single wait's.
func TestMesh_BatchWaitRegistersEveryRequestAndCountsEarlyAnswers(t *testing.T) {
	f := newBatchFixture(t, 3)
	f.reply(1)

	receipt := f.mustWait("op-batch-wait", nil, 0, 1, 2)

	if receipt.BatchID == "" || len(receipt.Items) != 3 || receipt.Status != agentcomm.WaitPending {
		t.Fatalf("receipt = %+v, want a batch id, 3 items and pending", receipt)
	}
	waits := f.o.opts.Store.Snapshot().Assistants.Mesh
	wantStatus := []agentcomm.WaitStatus{agentcomm.WaitPending, agentcomm.WaitReady, agentcomm.WaitPending}
	for i, item := range receipt.Items {
		w := waits.Waits[item.WaitID]
		if w == nil || w.BatchID != receipt.BatchID || w.Waiter != f.stage || w.Condition.MessageID != f.requests[i] {
			t.Fatalf("item %d = %+v, wait %+v: want a wait on request %s under batch %s", i, item, w, f.requests[i], receipt.BatchID)
		}
		if item.MessageID != f.requests[i] || item.Status != wantStatus[i] || w.Status != wantStatus[i] {
			t.Fatalf("item %d status = %s / stored %s, want %s", i, item.Status, w.Status, wantStatus[i])
		}
	}
	if !slices.Equal(waits.WaitOrder, receiptWaitIDs(receipt)) {
		t.Fatalf("wait order = %v, want the items in request order %v", waits.WaitOrder, receiptWaitIDs(receipt))
	}

	// Retrying the same registration is a replay of it, not a second batch.
	again := f.mustWait("op-batch-wait", nil, 0, 1, 2)
	if again.BatchID != receipt.BatchID || !slices.Equal(receiptWaitIDs(again), receiptWaitIDs(receipt)) || len(f.o.opts.Store.Snapshot().Assistants.Mesh.Waits) != 3 {
		t.Fatalf("retry = %+v, want the same batch and no new waits", again)
	}
}

// Every request already answered: the whole batch is ready at registration, nothing to yield on.
func TestMesh_BatchWaitAllAnsweredIsReady(t *testing.T) {
	f := newBatchFixture(t, 2)
	f.reply(0)
	f.reply(1)
	if receipt := f.mustWait("op-batch-wait", nil, 0, 1); receipt.Status != agentcomm.WaitReady {
		t.Fatalf("status = %s, want ready", receipt.Status)
	}
}

// The first answers of N do not wake the parked stage; the last one does, exactly once — and the answers stay
// unread in its inbox for that turn, not a reason to wake it earlier.
func TestMesh_BatchWaitWakesTheStageOnlyAfterTheLastAnswer(t *testing.T) {
	f := newBatchFixture(t, 3)
	receipt := f.mustWait("op-batch-wait", nil, 0, 1, 2)
	f.park(receiptWaitIDs(receipt))

	f.reply(0)
	f.requireResumes()
	f.reply(2)
	f.requireResumes()
	f.reply(1)
	f.requireResumes(bus.EventPeerReplied)
}

// A single wait next to a batch keeps its meaning: the stage continues on the first of the alternatives to end.
func TestMesh_SingleWaitStaysAnAlternativeToABatch(t *testing.T) {
	f := newBatchFixture(t, 3)
	batch := f.mustWait("op-batch-wait", nil, 0, 1)
	single, err := f.o.Wait(context.Background(), WaitRequest{
		OperationID: "op-single-wait", Waiter: f.stage, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: f.requests[2]},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.park(append(receiptWaitIDs(batch), single.WaitID))

	f.reply(0)
	f.requireResumes()
	f.reply(2)
	f.requireResumes(bus.EventPeerReplied)
}

// Two plain waits and no batch: the first answer wakes the stage, as before batches existed.
func TestMesh_PlainWaitsAreStillAnyOf(t *testing.T) {
	f := newBatchFixture(t, 2)
	var episode []agentcomm.WaitID
	for i := range 2 {
		receipt, err := f.o.Wait(context.Background(), WaitRequest{
			OperationID: fmt.Sprintf("op-plain-wait-%d", i), Waiter: f.stage,
			Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: f.requests[i]},
		})
		if err != nil {
			t.Fatal(err)
		}
		episode = append(episode, receipt.WaitID)
	}
	f.park(episode)

	f.reply(1)
	f.requireResumes(bus.EventPeerReplied)
}

// Answers and cancellations mix: the stage continues once, when the last item ends — with the answer in hand if
// there is one, else as a wait that ended.
func TestMesh_BatchWaitMixOfAnswersAndCancellationsWakesOnce(t *testing.T) {
	cancel := func(f *batchFixture, id agentcomm.WaitID, op string) {
		t.Helper()
		if err := f.o.CancelWait(context.Background(), CancelWaitRequest{OperationID: op, WaitID: id, Actor: state.Actor{Kind: "user"}}); err != nil {
			t.Fatalf("CancelWait: %v", err)
		}
	}
	t.Run("answer then cancel", func(t *testing.T) {
		f := newBatchFixture(t, 2)
		ids := receiptWaitIDs(f.mustWait("op-batch-wait", nil, 0, 1))
		f.park(ids)
		f.reply(0)
		f.requireResumes()
		cancel(f, ids[1], "op-cancel-1")
		f.requireResumes(bus.EventPeerReplied)
	})
	t.Run("cancel then answer", func(t *testing.T) {
		f := newBatchFixture(t, 2)
		ids := receiptWaitIDs(f.mustWait("op-batch-wait", nil, 0, 1))
		f.park(ids)
		cancel(f, ids[0], "op-cancel-0")
		f.requireResumes()
		f.reply(1)
		f.requireResumes(bus.EventPeerReplied)
	})
	t.Run("all cancelled", func(t *testing.T) {
		f := newBatchFixture(t, 2)
		ids := receiptWaitIDs(f.mustWait("op-batch-wait", nil, 0, 1))
		f.park(ids)
		cancel(f, ids[0], "op-cancel-0")
		cancel(f, ids[1], "op-cancel-1")
		f.requireResumes(bus.EventPeerWaitEnded)
	})
}

// A clarifying question from a colleague gives the stage an early turn while the batch is still open; after it,
// the stage parks on the same batch again and still waits for all of it.
func TestMesh_BatchWaitQuestionGivesAnEarlyTurnAndTheStageParksOnTheSameBatch(t *testing.T) {
	f := newBatchFixture(t, 2)
	receipt := f.mustWait("op-batch-wait", nil, 0, 1)
	ids := receiptWaitIDs(receipt)
	f.park(ids)
	f.reply(0)
	f.requireResumes()

	if _, err := f.o.Send(context.Background(), SendRequest{
		OperationID: "op-batch-question", ThreadID: f.thread, Sender: meshUserParticipantID,
		Recipients: []agentcomm.ParticipantID{f.stage}, Kind: agentcomm.MessageKindMessage, Body: "which branch?",
	}); err != nil {
		t.Fatal(err)
	}
	f.requireResumes(bus.EventPeerQuestion)

	// The early turn: the stage resumes, reads its inbox (the answer and the question) and yields again.
	if _, ok := f.o.Trigger("s1", bus.EvPeerQuestion, bus.GuardCtx{Phase: phaseImplementation}, ""); !ok {
		t.Fatal("EvPeerQuestion failed")
	}
	f.continuations()
	before := f.o.meshWaitStatusesOf(f.stage)
	readInbox(t, f.o, f.stage)
	episode := f.o.turnEpisode(f.stage, before)
	if !slices.Equal(episode, sortedIDs(ids)) {
		t.Fatalf("episode after the early turn = %v, want the whole batch %v", episode, sortedIDs(ids))
	}
	f.park(episode)

	f.requireResumes()
	f.reply(1)
	f.requireResumes(bus.EventPeerReplied)
}

func sortedIDs(ids []agentcomm.WaitID) []agentcomm.WaitID {
	out := slices.Clone(ids)
	slices.Sort(out)
	return out
}

// cancel-wait by the batch id ends every item still pending in one commit and keeps the answered ones; the stage
// is woken once. Only the waiter or the human may do it; an unknown batch is not found.
func TestMesh_CancelWaitByBatchEndsThePendingItemsAtomically(t *testing.T) {
	f := newBatchFixture(t, 3)
	receipt := f.mustWait("op-batch-wait", nil, 0, 1, 2)
	ids := receiptWaitIDs(receipt)
	f.park(ids)
	f.reply(0)
	f.requireResumes()

	err := f.o.CancelWait(context.Background(), CancelWaitRequest{OperationID: "op-cancel-stranger", BatchID: receipt.BatchID, Actor: state.Actor{Kind: "agent", ID: "stage:other"}})
	requireRejected(t, err, assistants.CodeActorNotAllowed)
	err = f.o.CancelWait(context.Background(), CancelWaitRequest{OperationID: "op-cancel-unknown", BatchID: "mb-unknown", Actor: state.Actor{Kind: "user"}})
	requireRejected(t, err, assistants.CodeNotFound)
	if got := f.waitStatus(ids[1]); got != agentcomm.WaitPending {
		t.Fatalf("a rejected cancel changed item 1 to %s", got)
	}

	cancelReq := CancelWaitRequest{OperationID: "op-cancel-batch", BatchID: receipt.BatchID, Reason: "not needed any more", Actor: state.Actor{Kind: "agent", ID: string(f.stage)}}
	if err := f.o.CancelWait(context.Background(), cancelReq); err != nil {
		t.Fatalf("CancelWait: %v", err)
	}
	waits := f.o.opts.Store.Snapshot().Assistants.Mesh.Waits
	if waits[ids[0]].Status != agentcomm.WaitReady || waits[ids[1]].Status != agentcomm.WaitCancelled || waits[ids[2]].Status != agentcomm.WaitCancelled || waits[ids[1]].Reason != "not needed any more" {
		t.Fatalf("statuses = %s/%s/%s (reason %q), want ready/cancelled/cancelled with the reason", waits[ids[0]].Status, waits[ids[1]].Status, waits[ids[2]].Status, waits[ids[1]].Reason)
	}
	f.requireResumes(bus.EventPeerReplied)

	cancelReq.OperationID = "op-cancel-batch-again"
	if err := f.o.CancelWait(context.Background(), cancelReq); err != nil {
		t.Fatalf("second CancelWait: %v", err)
	}
	f.requireResumes()
}

// A helper task parked on a batch is not resumed by the answer to its first request: it waits in its inbox for
// the one turn after the last item has ended.
func TestMesh_BatchWaitOfAHelperTaskResumesOnceAfterTheLastAnswer(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)

	taskID := deriveTaskID("op-bh-1")
	helper := meshHelperParticipantID(taskID)
	var calls atomic.Int32
	var thread agentcomm.ThreadID
	var requests []agentcomm.MessageID
	o.runHelperAgent = func(ctx context.Context, tid, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		if tid != string(taskID) || calls.Add(1) > 1 {
			return passResultOutcome(), nil
		}
		opened, err := o.OpenThread(ctx, OpenThreadRequest{OperationID: "op-bh-thread", Members: []agentcomm.ParticipantID{helper, meshUserParticipantID}, Scope: agentcomm.ScopeStage})
		if err != nil {
			t.Errorf("OpenThread: %v", err)
			return executor.HelperOutcome{}, err
		}
		thread = opened.ID
		for i := range 2 {
			sent, err := o.Send(ctx, SendRequest{
				OperationID: fmt.Sprintf("op-bh-ask-%d", i), ThreadID: thread, Sender: helper,
				Recipients: []agentcomm.ParticipantID{meshUserParticipantID}, Kind: agentcomm.MessageKindRequest, Body: "please review",
			})
			if err != nil {
				t.Errorf("Send request %d: %v", i, err)
				return executor.HelperOutcome{}, err
			}
			requests = append(requests, sent.MessageID)
		}
		if _, err := batchWait(o, "op-bh-wait", helper, requests...); err != nil {
			t.Errorf("batch Wait: %v", err)
		}
		return executor.HelperOutcome{ProcessOK: true}, nil
	}
	attachTestTask(ctx, t, o, "op-bh-1")
	waitForTaskStatus(t, store, taskID, assistants.StatusWaitingPeer, 2*time.Second)

	reply := func(i int) {
		t.Helper()
		if _, err := o.Send(ctx, SendRequest{
			OperationID: fmt.Sprintf("op-bh-reply-%d", i), ThreadID: thread, Sender: meshUserParticipantID,
			Recipients: []agentcomm.ParticipantID{helper}, Kind: agentcomm.MessageKindReply, InReplyTo: &requests[i], Body: "done",
		}); err != nil {
			t.Fatalf("reply %d: %v", i, err)
		}
	}
	reply(0)
	time.Sleep(150 * time.Millisecond)
	if got := store.Snapshot().Assistants.Tasks[taskID].Status; got != assistants.StatusWaitingPeer || calls.Load() != 1 {
		t.Fatalf("after the first answer: task %s, turns %d; want it still parked, no new turn", got, calls.Load())
	}

	reply(1)
	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 2*time.Second)
	if got := calls.Load(); got != 2 {
		t.Fatalf("turns = %d, want exactly 2 (the yield and one continuation)", got)
	}
	o.concurrency.WaitAgents()
}

// The answer to the first request landed while the turn was still running: parking the stage right after it
// does not start a continuation, the last answer does — once.
func TestMesh_BatchWaitStageParkedAfterTheFirstAnswerWaitsForTheLast(t *testing.T) {
	f := newBatchFixture(t, 2)
	ids := receiptWaitIDs(f.mustWait("op-batch-wait", nil, 0, 1))
	f.reply(0)
	f.requireResumes()

	f.park(ids)
	f.o.continueParkedStage(context.Background(), "s1") // the exit path's re-check after the park
	f.requireResumes()

	f.reply(1)
	f.requireResumes(bus.EventPeerReplied)
}

// A batch that ended without a single answer is not ready: the aggregate follows its items — pending while one
// is, ready only when one was answered, else expired — and a retry of the registration reports the same.
func TestMesh_BatchWaitReceiptStatusFollowsTheItems(t *testing.T) {
	f := newBatchFixture(t, 2)
	deadline := f.o.clk().Now().Add(time.Millisecond) // the pumped fake clock runs it out at once
	first := f.mustWait("op-batch-wait", &deadline, 0, 1)
	for _, id := range receiptWaitIDs(first) {
		waitForWaitStatus(t, f.o, id, agentcomm.WaitExpired, 2*time.Second)
	}

	again := f.mustWait("op-batch-wait", &deadline, 0, 1)
	if again.Status != agentcomm.WaitExpired {
		t.Fatalf("status of a batch whose items all expired = %s, want expired", again.Status)
	}
}

// A batch whose deadline ended the rest reads as expired even though other items got a real answer, so a
// client cannot take the timeout for a complete success.
func TestMesh_BatchWaitReceiptOfAnsweredAndExpiredItemsIsExpired(t *testing.T) {
	f := newBatchFixture(t, 2)
	f.reply(0)
	deadline := f.o.clk().Now().Add(time.Millisecond) // the pumped fake clock runs it out at once
	first := f.mustWait("op-batch-wait", &deadline, 0, 1)
	waitForWaitStatus(t, f.o, first.Items[1].WaitID, agentcomm.WaitExpired, 2*time.Second)

	again := f.mustWait("op-batch-wait", &deadline, 0, 1)
	if again.Items[0].Status != agentcomm.WaitReady || again.Status != agentcomm.WaitExpired {
		t.Fatalf("receipt = %s with items %s/%s, want expired over a ready and an expired item", again.Status, again.Items[0].Status, again.Items[1].Status)
	}
}

// Only the answer of a recipient of the request closes its item. A reply from anyone else (here the human, who
// is a member of the thread but was not asked) leaves the item pending, so it is a message like any other:
// it wakes the waiter instead of being held back for the end of the batch.
func TestWakingExceptBatchAnswers_ReplyOfANonRecipientStillWakes(t *testing.T) {
	const waiter, reviewer, human = agentcomm.ParticipantID("stage"), agentcomm.ParticipantID("reviewer"), agentcomm.ParticipantID("user")
	request := agentcomm.MessageID("q1")
	reply := func(sender agentcomm.ParticipantID) state.MeshState {
		answer := agentcomm.MessageID("r1")
		return state.MeshState{
			Messages: map[agentcomm.MessageID]*agentcomm.Message{
				request: {ID: request, Sender: waiter, Recipients: []agentcomm.ParticipantID{reviewer}},
				answer:  {ID: answer, Sender: sender, Recipients: []agentcomm.ParticipantID{waiter}, InReplyTo: &request},
			},
			Inbox:    map[agentcomm.ParticipantID][]agentcomm.MessageID{waiter: {answer}},
			Receipts: map[agentcomm.MessageID]map[agentcomm.ParticipantID]agentcomm.DeliveryStatus{answer: {waiter: agentcomm.DeliveryAccepted}},
			Waits: map[agentcomm.WaitID]*state.MeshWait{"w1": {
				Wait:    agentcomm.Wait{ID: "w1", Waiter: waiter, Status: agentcomm.WaitPending, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: request}},
				BatchID: "b1", RecipientReplyOnly: true,
			}},
		}
	}

	if wakingExceptBatchAnswers(reply(reviewer), waiter) {
		t.Fatal("the reviewer's answer to its own request woke the stage before the batch ended")
	}
	if !wakingExceptBatchAnswers(reply(human), waiter) {
		t.Fatal("the human's reply to a request meant for the reviewer was held back: it does not answer the item, so it must wake")
	}
}

// A cancelled item shows its durable reason in the receipt, not only in the stored wait.
func TestMesh_BatchWaitReceiptCarriesTheReasonOfAnEndedItem(t *testing.T) {
	f := newBatchFixture(t, 2)
	receipt := f.mustWait("op-batch-wait", nil, 0, 1)
	f.reply(0)
	cancel := CancelWaitRequest{OperationID: "op-cancel-batch", BatchID: receipt.BatchID, Reason: "not needed any more", Actor: state.Actor{Kind: "agent", ID: string(f.stage)}}
	if err := f.o.CancelWait(context.Background(), cancel); err != nil {
		t.Fatalf("CancelWait: %v", err)
	}

	again := f.mustWait("op-batch-wait", nil, 0, 1)
	if again.Status != agentcomm.WaitReady {
		t.Fatalf("status = %s, want ready (item 0 was answered)", again.Status)
	}
	if got := again.Items[0].Reason; got != "" {
		t.Fatalf("reason of the answered item = %q, want none", got)
	}
	if got := again.Items[1]; got.Status != agentcomm.WaitCancelled || got.Reason != "not needed any more" {
		t.Fatalf("item 1 = %+v, want cancelled with the reason given to cancel-wait", got)
	}
}

// A reply_to wait naming no request at all is the old single-wait mistake, not a malformed batch.
func TestMesh_ReplyToWithoutAnyMessageIDKeepsTheConditionHint(t *testing.T) {
	f := newBatchFixture(t, 1)
	_, err := f.o.Wait(context.Background(), WaitRequest{
		OperationID: "op-no-id", Waiter: f.stage, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo},
	})
	requireRejected(t, err, assistants.CodeInvalidRequest)
	if want := waitConditionHint(agentcomm.WaitOnReplyTo); !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want the hint %q", err, want)
	}
}
