// Package orchestrator (this file, Agent Mesh Phase 8, T8.3): the crash/race/replay durability sweep.
// dev-plan.md's T8.3 lists thirteen durability clauses; most of them are ALREADY proven by tests Phases 2-6
// wrote for their own reasons (the mesh was built with exactly this discipline from the start — commit
// first, notify after, outside the lock; every resolution idempotent; every resume CAS-gated). Per T8.3's own
// "Done when" line ("every bullet ... has a named test"), each clause below is pointed at either a NEW test in
// this file or an EXISTING one elsewhere, named explicitly so the mapping is traceable without duplicating a
// scenario that already exists:
//
//  1. crash after Send commit but before the live publish:
//     TestCrash_HelperWaitResolvedBeforeRequeue_RecoveredOnRestart (assistants_crash_recovery_test.go, the
//     mesh_send/tryResumePeer case) and TestAgentMesh_QADevOps_RestartMidScenario (agentcomm_integration_test.go,
//     the mesh_publish/tryResumePeer case, T8.1) — both commit the resolving fact via the SAME
//     store.ApplyRunOperation(op, nil) idiom this file also uses below, and both prove a fresh Orchestrator's
//     startup recovery still resumes the waiter.
//  2. crash during delivery: TestMesh_CrashDuringDelivery_ReceiptSurvivesRestartWithoutDuplication (below).
//  3. crash before spawning a continuation: TestCrash_ChildResultRecordedBeforeParentContinuation_RecoveredOnRestart
//     (assistants_crash_recovery_test.go) and TestAgentMesh_QADevOps_RestartMidScenario (T8.1) — the mesh
//     scenario IS "a continuation never spawned before the crash", for a board-publish resolution.
//  4. reply before Wait: TestMesh_WaitAlreadySatisfiedIsReadyImmediately (agentcomm_wait_test.go).
//  5. two simultaneous replies: TestMesh_TwoSimultaneousRepliesOneWaitResolutionBothMessagesDelivered (below).
//  6. timeout simultaneous with a reply: TestMesh_WaitDeadline_RaceConcurrent_ExactlyOneOutcomeReplyNeverLost
//     (agentcomm_deadline_test.go, run under -race).
//  7. repeated Send/Ack: TestMesh_SendIsIdempotentPerOperationID (agentcomm_messages_test.go) for Send;
//     TestMesh_AcknowledgeIsIdempotentAndNeverRegresses (below) for Ack.
//  8. mutual waiting detected: TestMesh_WaitMutualCycleRejected (agentcomm_wait_test.go).
//  9. a parent→child→parent cycle detected: TestMesh_ParentChildMeshWaitCycleRejected (below).
//  10. a deadline ending a wait on an unreachable recipient with a visible reason:
//     TestMesh_DeadlineEndsWaitOnUnreachableBoardProducerWithVisibleReason (below; the already-existing
//     TestMesh_WaitDeadline_ExpiresStageWaitAndResumesViaPeerWaitEnded in agentcomm_deadline_test.go proves the
//     same mechanism for an addressed-message condition with a live, attached recipient).
//  11. ids and history surviving a restart and a lost live connection:
//     TestMesh_IdsAndHistorySurviveRestartAndLostLiveConnection (below).
//  12. old journals read with no repeated side effects: TestMesh_ReplayingOldJournalCausesNoRepeatedSideEffects
//     (below).
//  13. costs accounted exactly once: TestMesh_CostsAccountedExactlyOncePerTaskAcrossRestart (below).
package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/accounting"
	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

// TestMesh_CrashDuringDelivery_ReceiptSurvivesRestartWithoutDuplication is T8.3's "crash during delivery"
// clause: a message has been Sent (durably Accepted) and MarkMessageDelivered has ALSO already committed
// (Delivered) when the process dies — modeled directly (no externalWork to bypass here: MarkMessageDelivered
// has nothing else to do after its own commit, unlike Send/Publish). A fresh Orchestrator reopening the same
// run directory must see the exact same receipt and message history, and replaying the SAME deterministic
// MarkMessageDelivered operation id again (e.g. a retried invocation re-including the same message in its own
// <inbox> block) must be a harmless no-op, never a duplicate entry or a regressed/corrupted receipt.
func TestMesh_CrashDuringDelivery_ReceiptSurvivesRestartWithoutDuplication(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	runDir := o.opts.RunDir
	recipient := meshStageParticipantID(assistantsTestStageID)

	thread, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-deliv-open", Members: []agentcomm.ParticipantID{meshUserParticipantID, recipient}, Scope: agentcomm.ScopeStage,
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	send, err := o.Send(ctx, SendRequest{
		OperationID: "op-deliv-send", ThreadID: thread.ID, Sender: meshUserParticipantID,
		Recipients: []agentcomm.ParticipantID{recipient}, Kind: agentcomm.MessageKindMessage, Body: "hi",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if err := o.MarkMessageDelivered(ctx, send.MessageID, recipient); err != nil {
		t.Fatalf("MarkMessageDelivered: %v", err)
	}

	store.Close() // release the flock, standing in for the OS reclaiming it on process death

	store2, err := state.Open(runDir, []string{assistantsTestStageID})
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer store2.Close()
	o2 := reopenAssistantsOrchestrator(o, store2)

	rs := store2.Snapshot()
	if got := rs.Assistants.Mesh.Receipts[send.MessageID][recipient]; got != agentcomm.DeliveryDelivered {
		t.Fatalf("receipt after restart = %q, want delivered (the pre-crash commit must survive)", got)
	}
	if ids := rs.Assistants.Mesh.MessagesByThread[thread.ID]; len(ids) != 1 {
		t.Fatalf("message history after restart = %v, want exactly [m1], never duplicated", ids)
	}
	if ids := rs.Assistants.Mesh.Inbox[recipient]; len(ids) != 1 {
		t.Fatalf("inbox after restart = %v, want exactly one reference, never duplicated", ids)
	}

	// Replaying the SAME deterministic op id (a retried invocation re-marking the same message delivered) must
	// not duplicate or regress anything.
	if err := o2.MarkMessageDelivered(ctx, send.MessageID, recipient); err != nil {
		t.Fatalf("replaying MarkMessageDelivered after restart: %v", err)
	}
	rs2 := store2.Snapshot()
	if got := rs2.Assistants.Mesh.Receipts[send.MessageID][recipient]; got != agentcomm.DeliveryDelivered {
		t.Fatalf("receipt after replay = %q, want still exactly delivered", got)
	}
	if ids := rs2.Assistants.Mesh.Inbox[recipient]; len(ids) != 1 {
		t.Fatalf("inbox after replay = %v, want still exactly one reference", ids)
	}
}

// TestMesh_TwoSimultaneousRepliesOneWaitResolutionBothMessagesDelivered is T8.3's "two simultaneous replies"
// clause: TWO DIFFERENT colleagues concurrently address the SAME waiter in the SAME thread, each message
// independently satisfying its WaitOnAddressedMessage condition. Exactly one commit can be the one that moves
// the wait Pending->Ready (resolvePendingWaits' own guard, pkg/state/mesh_waits.go); the other's commit must
// still durably land — "unable to also resolve an already-resolved wait" must never mean "the message itself
// was dropped". Run under -race: real goroutine-scheduling nondeterminism decides which one wins.
func TestMesh_TwoSimultaneousRepliesOneWaitResolutionBothMessagesDelivered(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	waiter := meshStageParticipantID(assistantsTestStageID)
	colleagueA := meshUserParticipantID
	colleagueB := meshHelperParticipantID(newRunningHelperForMesh(t, o, store, "op-2reply-helper"))

	thread, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-2reply-open", Members: []agentcomm.ParticipantID{waiter, colleagueA, colleagueB}, Scope: agentcomm.ScopeStage,
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	receipt, err := o.Wait(ctx, WaitRequest{
		OperationID: "op-2reply-wait", Waiter: waiter,
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: thread.ID},
	})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if receipt.Status != agentcomm.WaitPending {
		t.Fatalf("expected a fresh wait to start pending, got %q", receipt.Status)
	}

	var errA, errB error
	var wg sync.WaitGroup
	wg.Go(func() {
		_, errA = o.Send(context.Background(), SendRequest{
			OperationID: "op-2reply-a", ThreadID: thread.ID, Sender: colleagueA,
			Recipients: []agentcomm.ParticipantID{waiter}, Kind: agentcomm.MessageKindMessage, Body: "from A",
		})
	})
	wg.Go(func() {
		_, errB = o.Send(context.Background(), SendRequest{
			OperationID: "op-2reply-b", ThreadID: thread.ID, Sender: colleagueB,
			Recipients: []agentcomm.ParticipantID{waiter}, Kind: agentcomm.MessageKindMessage, Body: "from B",
		})
	})
	wg.Wait()
	if errA != nil {
		t.Fatalf("Send A: %v", errA)
	}
	if errB != nil {
		t.Fatalf("Send B: %v", errB)
	}

	rs := store.Snapshot()
	if got := rs.Assistants.Mesh.Waits[receipt.WaitID].Status; got != agentcomm.WaitReady {
		t.Fatalf("wait status = %q, want ready (exactly one resolution, whichever reply won the race)", got)
	}
	if got := len(rs.Assistants.Mesh.MessagesByThread[thread.ID]); got != 2 {
		t.Fatalf("thread message count = %d, want exactly 2 — neither simultaneous reply may be lost just because only one could resolve the wait", got)
	}
	if got := len(rs.Assistants.Mesh.Inbox[waiter]); got != 2 {
		t.Fatalf("waiter inbox = %d, want exactly 2 (both replies delivered)", got)
	}
}

// TestMesh_AcknowledgeIsIdempotentAndNeverRegresses is T8.3's "repeated ... Ack" half of the "repeated
// Send/Ack" clause (Send's own half is TestMesh_SendIsIdempotentPerOperationID, agentcomm_messages_test.go):
// a retried Acknowledge call with the SAME operation id is a plain ApplyRunOperation replay, and a SECOND,
// distinct Ack operation for an ALREADY-acknowledged message/recipient pair is also a harmless no-op
// (applyMeshMessageReceipt's monotonic rank check, pkg/state/mesh_projection.go) — never an error, never a
// regression back toward "accepted".
func TestMesh_AcknowledgeIsIdempotentAndNeverRegresses(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	taskID := newRunningHelperForMesh(t, o, store, "op-ack-idem")
	helperID := meshHelperParticipantID(taskID)

	thread, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-ack-idem-open", Members: []agentcomm.ParticipantID{meshUserParticipantID, helperID}, Scope: agentcomm.ScopeStage,
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	send, err := o.Send(ctx, SendRequest{
		OperationID: "op-ack-idem-send", ThreadID: thread.ID, Sender: meshUserParticipantID,
		Recipients: []agentcomm.ParticipantID{helperID}, Kind: agentcomm.MessageKindMessage, Body: "hi",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	if err := o.Acknowledge(ctx, AcknowledgeRequest{OperationID: "op-ack-idem-1", MessageID: send.MessageID, Participant: helperID}); err != nil {
		t.Fatalf("Acknowledge #1: %v", err)
	}
	// Same operation id again: plain idempotent replay.
	if err := o.Acknowledge(ctx, AcknowledgeRequest{OperationID: "op-ack-idem-1", MessageID: send.MessageID, Participant: helperID}); err != nil {
		t.Fatalf("Acknowledge replay (same operation id): %v", err)
	}
	// A DIFFERENT operation id for the same, already-acknowledged (message, recipient): still a safe no-op.
	if err := o.Acknowledge(ctx, AcknowledgeRequest{OperationID: "op-ack-idem-2", MessageID: send.MessageID, Participant: helperID}); err != nil {
		t.Fatalf("Acknowledge (second operation id, already acknowledged): %v", err)
	}

	rs := store.Snapshot()
	if got := rs.Assistants.Mesh.Receipts[send.MessageID][helperID]; got != agentcomm.DeliveryAcknowledged {
		t.Fatalf("receipt = %q, want still exactly acknowledged after repeated Ack calls", got)
	}
}

// TestMesh_ParentChildMeshWaitCycleRejected is T8.3's "a parent→child→parent cycle detected" clause:
// buildMeshWaitGraph (agentcomm_wait.go) already folds a waiting_children parent->child edge into the SAME
// graph a mesh Wait's own cycle check walks — this proves the two combine correctly: a parent is genuinely
// waiting_children on a still-running child (admitChildren's own delegation, not a mesh Wait at all), and the
// CHILD then tries to register a mesh Wait whose target is the parent — closing a cycle no participant could
// ever break (the parent cannot move without the child, the child's new wait would need the parent to move
// first). Wait() must reject it with CodeWaitCycle, exactly like the flat mutual case
// (TestMesh_WaitMutualCycleRejected, agentcomm_wait_test.go) already proves for two mesh waits with no task
// hierarchy involved.
func TestMesh_ParentChildMeshWaitCycleRejected(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)

	parentID := deriveTaskID("op-cycle-parent")
	childRelease := make(chan struct{})
	var delegated atomic.Bool
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		if taskID == string(parentID) {
			if delegated.CompareAndSwap(false, true) {
				return delegateOutcome(childJSON(assistants.ModeAdvisory)), nil
			}
			// Any LATER parent turn (its own continuation, once the child below completes) just finishes
			// normally — only the first turn delegates.
			return passResultOutcome(), nil
		}
		awaitRelease(t, childRelease) // the child's own turn: stays non-terminal until the test is done with it
		return passResultOutcome(), nil
	}

	gotParentID := attachTestTask(ctx, t, o, "op-cycle-parent")
	if gotParentID != parentID {
		t.Fatalf("parent task id = %s, want deterministic %s", gotParentID, parentID)
	}
	waitForTaskStatus(t, store, parentID, assistants.StatusWaitingChildren, 2*time.Second)
	childID := store.Snapshot().Assistants.ByParent[parentID][0]
	waitForTaskStatus(t, store, childID, assistants.StatusRunning, 2*time.Second)

	parentParticipant := meshHelperParticipantID(parentID)
	childParticipant := meshHelperParticipantID(childID)

	thread, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-cycle-open", Members: []agentcomm.ParticipantID{parentParticipant, childParticipant}, Scope: agentcomm.ScopeStage,
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}

	_, err = o.Wait(ctx, WaitRequest{
		OperationID: "op-cycle-child-wait", Waiter: childParticipant,
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: thread.ID},
	})
	if err == nil {
		t.Fatal("child registering a wait that targets its own waiting_children parent: want a wait_cycle rejection, got nil error")
	}
	var rejected *state.OperationRejectedError
	if !errors.As(err, &rejected) || rejected.Code != agentcomm.CodeWaitCycle {
		t.Fatalf("expected a CodeWaitCycle rejection, got %v", err)
	}

	close(childRelease)
	waitForTaskStatus(t, store, childID, assistants.StatusCompleted, 2*time.Second)
	waitForTaskStatus(t, store, parentID, assistants.StatusCompleted, 2*time.Second)
	o.concurrency.WaitAgents()
}

// TestMesh_RunParentChildWaitCycleRejected: the delegation edge of a scope=run parent belongs to its real mesh
// identity run:<taskID>, not to a helper:<taskID> nobody ever registered. Otherwise the waiting_children
// parent looks free to act and its child's task_result wait on it is admitted — a deadlock.
func TestMesh_RunParentChildWaitCycleRejected(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()

	runID := deriveTaskID("op-run-cycle-parent")
	childRelease := make(chan struct{})
	var runTurns atomic.Int32
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		if taskID == string(runID) {
			if runTurns.Add(1) == 1 {
				return delegateOutcome(childJSON(assistants.ModeAdvisory)), nil
			}
			return passResultOutcome(), nil // the continuation turn once the child completes
		}
		awaitRelease(t, childRelease) // the child's own turn: stays non-terminal until the test is done with it
		return passResultOutcome(), nil
	}

	msg := "delegate this"
	res, err := o.AddRunAgent(ctx, AddRunAgentRequest{
		OperationID: "op-run-cycle-parent", ProfileID: "qa", Title: "QA", Instruction: "review", FirstMessage: &msg, Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Fatalf("AddRunAgent: %v", err)
	}
	if res.TaskID != runID {
		t.Fatalf("run agent task id = %s, want deterministic %s", res.TaskID, runID)
	}
	waitForTaskStatus(t, store, runID, assistants.StatusWaitingChildren, 2*time.Second)
	childID := store.Snapshot().Assistants.ByParent[runID][0]
	waitForTaskStatus(t, store, childID, assistants.StatusRunning, 2*time.Second)

	rs := store.Snapshot()
	runParticipant := meshParticipantForTask(rs, rs.Assistants.Tasks[runID].Task)
	childParticipant := meshParticipantForTask(rs, rs.Assistants.Tasks[childID].Task)
	if runParticipant != meshRunParticipantID(runID) || childParticipant != meshHelperParticipantID(childID) {
		t.Fatalf("participants = %s, %s, want run:<parent> and helper:<child>", runParticipant, childParticipant)
	}

	_, err = o.Wait(ctx, WaitRequest{
		OperationID: "op-run-cycle-child-wait", Waiter: childParticipant,
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnTaskResult, TaskID: string(runID)},
	})
	var rejected *state.OperationRejectedError
	if !errors.As(err, &rejected) || rejected.Code != agentcomm.CodeWaitCycle {
		t.Fatalf("child waiting on the result of its waiting_children run parent: err = %v, want a wait_cycle rejection", err)
	}
	for _, w := range store.Snapshot().Assistants.Mesh.Waits {
		if w.Waiter == childParticipant {
			t.Fatalf("a rejected wait left a record: %+v", w)
		}
	}

	close(childRelease)
	waitForTaskStatus(t, store, childID, assistants.StatusCompleted, 2*time.Second)
	o.concurrency.WaitAgents()
	if got := runTurns.Load(); got != 2 {
		t.Fatalf("run agent turns = %d, want the delegating turn and the continuation", got)
	}
	waitForRunAgentTurn(t, store, runID, 2*time.Second)
}

// TestMesh_WaitCycleAfterReplayUsesAllPendingWaits: the all-of rule for a helper's pending waits holds on a
// projection rebuilt from events.jsonl (a restart), not only on the live one. A waits on B and on C (both
// recorded through the real store); the replayed state must still reject B waiting on A.
func TestMesh_WaitCycleAfterReplayUsesAllPendingWaits(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	a := meshHelperParticipantID(newRunningHelperForMesh(t, o, store, "op-replay-a"))
	b := meshHelperParticipantID(newRunningHelperForMesh(t, o, store, "op-replay-b"))
	c := meshHelperParticipantID(newRunningHelperForMesh(t, o, store, "op-replay-c"))

	thread, err := o.OpenThread(ctx, OpenThreadRequest{OperationID: "op-replay-open", Members: []agentcomm.ParticipantID{a, b, c}, Scope: agentcomm.ScopeStage})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	var waitIDs []agentcomm.WaitID
	for _, peer := range []agentcomm.ParticipantID{b, c} {
		send, err := o.Send(ctx, SendRequest{OperationID: "op-replay-send-" + string(peer), ThreadID: thread.ID, Sender: a, Recipients: []agentcomm.ParticipantID{peer}, Kind: agentcomm.MessageKindRequest, Body: "q"})
		if err != nil {
			t.Fatalf("Send to %s: %v", peer, err)
		}
		wait, err := o.Wait(ctx, WaitRequest{OperationID: "op-replay-wait-" + string(peer), Waiter: a, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: send.MessageID}})
		if err != nil {
			t.Fatalf("Wait of A on %s: %v", peer, err)
		}
		waitIDs = append(waitIDs, wait.WaitID)
	}
	// A's waits stay pending for the whole test; cancel them before the helpers' own cleanups, which expect
	// every helper to finish (t.Cleanup is LIFO — this runs before theirs).
	t.Cleanup(func() {
		for _, id := range waitIDs {
			if err := o.CancelWait(ctx, CancelWaitRequest{OperationID: "op-replay-cancel-" + string(id), WaitID: id, Actor: state.Actor{Kind: "agent", ID: string(a)}}); err != nil {
				t.Errorf("CancelWait %s: %v", id, err)
			}
		}
	})
	toA, err := o.Send(ctx, SendRequest{OperationID: "op-replay-send-ba", ThreadID: thread.ID, Sender: b, Recipients: []agentcomm.ParticipantID{a}, Kind: agentcomm.MessageKindRequest, Body: "q"})
	if err != nil {
		t.Fatalf("Send B to A: %v", err)
	}

	replayed, err := state.LoadRunState(o.opts.RunDir)
	if err != nil {
		t.Fatalf("LoadRunState: %v", err)
	}
	for _, id := range waitIDs {
		if w := replayed.Assistants.Mesh.Waits[id]; w == nil || w.Status != agentcomm.WaitPending || w.Waiter != a {
			t.Fatalf("replayed wait %s = %+v, want pending wait of %s", id, w, a)
		}
	}
	err = validateWaitAdmission(replayed, WaitRequest{Waiter: b, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: toA.MessageID}}, nil)
	var rej *state.OperationRejectedError
	if !errors.As(err, &rej) || rej.Code != agentcomm.CodeWaitCycle {
		t.Fatalf("replayed state, B waits on A (A waits on B and C): err = %v, want wait_cycle", err)
	}
}

// TestMesh_DeadlineEndsWaitOnUnreachableBoardProducerWithVisibleReason is T8.3's "a deadline ending a wait on
// an unreachable recipient with a visible reason" clause for a board_key condition that has — and will ever
// have — no producer at all (not merely "nobody replied yet on a real thread", which
// TestMesh_WaitDeadline_ExpiresStageWaitAndResumesViaPeerWaitEnded, agentcomm_deadline_test.go, already
// covers for an addressed-message condition): the deadline must still fire and leave a stored, HONEST reason
// behind, never a silently-dropped wait and never anything that could be mistaken for success.
func TestMesh_DeadlineEndsWaitOnUnreachableBoardProducerWithVisibleReason(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	waiter := meshStageParticipantID(assistantsTestStageID)

	deadline := time.Now().Add(5 * time.Millisecond)
	receipt, err := o.Wait(ctx, WaitRequest{
		OperationID: "op-unreach-wait", Waiter: waiter, Deadline: &deadline,
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnBoardKey, Key: "nobody.ever.publishes.this", Status: "ready"},
	})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if receipt.Status != agentcomm.WaitPending {
		t.Fatalf("wait status = %q, want pending", receipt.Status)
	}

	waitForWaitStatus(t, o, receipt.WaitID, agentcomm.WaitExpired, 2*time.Second)
	rs := o.opts.Store.Snapshot()
	w := rs.Assistants.Mesh.Waits[receipt.WaitID]
	if w.Reason != meshWaitExpiredReason {
		t.Fatalf("reason = %q, want the honest %q — never a fabricated success for an unreachable producer", w.Reason, meshWaitExpiredReason)
	}
}

// TestMesh_IdsAndHistorySurviveRestartAndLostLiveConnection is T8.3's clause of the same name: a thread, a
// message and a board publish are committed, then the process is restarted with NO live subscriber ever
// having drained o.ui/o.critical at all (this bare harness's default state already models "a lost live
// connection" — nothing here ever wires up a dashboard/websocket). A FRESH Orchestrator's own Service calls
// must reproduce byte-identical, deterministic ids and the exact same history, entirely independent of
// whether any live notification was ever actually observed.
func TestMesh_IdsAndHistorySurviveRestartAndLostLiveConnection(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	runDir := o.opts.RunDir
	recipient := meshStageParticipantID(assistantsTestStageID)

	thread, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-hist-open", Members: []agentcomm.ParticipantID{meshUserParticipantID, recipient}, Scope: agentcomm.ScopeStage,
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	send, err := o.Send(ctx, SendRequest{
		OperationID: "op-hist-send", ThreadID: thread.ID, Sender: meshUserParticipantID,
		Recipients: []agentcomm.ParticipantID{recipient}, Kind: agentcomm.MessageKindMessage, Body: "hello",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if _, err := o.Publish(ctx, PublishRequest{OperationID: "op-hist-pub", Author: recipient, Key: "k", Status: "ready"}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	o.opts.Store.Close()
	store2, err := state.Open(runDir, []string{assistantsTestStageID})
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer store2.Close()
	o2 := reopenAssistantsOrchestrator(o, store2)

	if got := deriveMeshThreadID("op-hist-open"); got != thread.ID {
		t.Fatalf("deriveMeshThreadID is not stable across a restart: %s != %s", got, thread.ID)
	}
	if got := deriveMeshMessageID("op-hist-send"); got != send.MessageID {
		t.Fatalf("deriveMeshMessageID is not stable across a restart: %s != %s", got, send.MessageID)
	}

	page, err := o2.ReadThread(ctx, ReadThreadRequest{ThreadID: thread.ID, Reader: meshUserParticipantID})
	if err != nil {
		t.Fatalf("ReadThread after restart: %v", err)
	}
	if len(page.Messages) != 1 || page.Messages[0].ID != send.MessageID || page.Messages[0].Body != "hello" {
		t.Fatalf("ReadThread after restart = %+v, want exactly the pre-restart message intact", page.Messages)
	}

	boardPage, err := o2.ReadBoard(ctx, ReadBoardRequest{Key: "k"})
	if err != nil {
		t.Fatalf("ReadBoard after restart: %v", err)
	}
	if len(boardPage.Entries) != 1 || boardPage.Entries[0].Status != "ready" || boardPage.Entries[0].AuthorID != recipient {
		t.Fatalf("ReadBoard after restart = %+v, want the pre-restart entry intact", boardPage.Entries)
	}

	inbox, err := o2.ReadInbox(ctx, ReadInboxRequest{Participant: recipient})
	if err != nil {
		t.Fatalf("ReadInbox after restart: %v", err)
	}
	if len(inbox.Messages) != 1 {
		t.Fatalf("ReadInbox after restart = %+v, want exactly one message", inbox.Messages)
	}
}

// TestMesh_ReplayingOldJournalCausesNoRepeatedSideEffects is T8.3's "old journals read with no repeated side
// effects" clause, tested at the most literal level available: re-opening the SAME on-disk events.jsonl
// several times (state.Open replays the entire journal from byte zero into a fresh in-memory projection every
// time) must reproduce the exact same entity counts every single time — never duplicating a message, an
// inbox reference, a thread's message list or a board entry's version just because the journal was read more
// than once.
func TestMesh_ReplayingOldJournalCausesNoRepeatedSideEffects(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	runDir := o.opts.RunDir
	recipient := meshStageParticipantID(assistantsTestStageID)

	thread, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-replay-open", Members: []agentcomm.ParticipantID{meshUserParticipantID, recipient}, Scope: agentcomm.ScopeStage,
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	if _, err := o.Send(ctx, SendRequest{
		OperationID: "op-replay-send", ThreadID: thread.ID, Sender: meshUserParticipantID,
		Recipients: []agentcomm.ParticipantID{recipient}, Kind: agentcomm.MessageKindMessage, Body: "hi",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if _, err := o.Publish(ctx, PublishRequest{OperationID: "op-replay-pub", Author: recipient, Key: "k", Status: "ready"}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	store.Close()

	for i := 0; i < 3; i++ {
		s, err := state.Open(runDir, []string{assistantsTestStageID})
		if err != nil {
			t.Fatalf("replay #%d: state.Open: %v", i, err)
		}
		rs := s.Snapshot()
		if got := len(rs.Assistants.Mesh.Messages); got != 1 {
			t.Fatalf("replay #%d: Messages = %d, want 1", i, got)
		}
		if got := len(rs.Assistants.Mesh.MessagesByThread[thread.ID]); got != 1 {
			t.Fatalf("replay #%d: MessagesByThread = %d, want 1", i, got)
		}
		if got := len(rs.Assistants.Mesh.Inbox[recipient]); got != 1 {
			t.Fatalf("replay #%d: Inbox = %d, want 1", i, got)
		}
		if e, ok := rs.Assistants.Mesh.Board["k"]; !ok || e.Version != 1 {
			t.Fatalf("replay #%d: board entry = %+v, want version 1", i, e)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("replay #%d: close: %v", i, err)
		}
	}
}

// TestMesh_CostsAccountedExactlyOncePerTaskAcrossRestart is T8.3's "costs accounted exactly once" clause.
// execHelperAgent wires recordTaskUsage as the REAL executor's OnUsage callback (assistants_lifecycle.go),
// fired once per completed turn's terminal usage line — the fake runHelperAgent this package's own tests
// substitute for the real executor never calls it at all, so this test drives recordTaskUsage directly, the
// one place that actually exercises AppendTask's own durability. accounting.Store.Open's own contract is
// "replay what is on disk, never re-append it" — reopening the SAME usage.jsonl must reproduce exactly the
// one record written before the restart, never a duplicate.
func TestMesh_CostsAccountedExactlyOncePerTaskAcrossRestart(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	runDir := o.opts.RunDir

	acct, err := accounting.Open(runDir, accounting.NewResolver(accounting.PricingConfig{}))
	if err != nil {
		t.Fatalf("accounting.Open: %v", err)
	}
	o.opts.Accounting = acct

	taskID := assistants.TaskID("t-cost-1")
	obs := accounting.Observation{
		Metered: true, Schema: accounting.SchemaAnthropic, Channel: "claude-cli", Model: "glm-5.3",
		Tokens: accounting.Tokens{UncachedInput: 100, Output: 50},
	}
	o.recordTaskUsage(taskID, "reviewer")(obs)
	if err := acct.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	acct2, err := accounting.Open(runDir, accounting.NewResolver(accounting.PricingConfig{}))
	if err != nil {
		t.Fatalf("reopen accounting store: %v", err)
	}
	defer acct2.Close()

	var forTask []accounting.UsageRecord
	for _, r := range acct2.Snapshot().Records() {
		if r.TaskID == string(taskID) {
			forTask = append(forTask, r)
		}
	}
	if len(forTask) != 1 {
		t.Fatalf("usage records for task %s after restart = %d, want exactly 1 (costs accounted exactly once)", taskID, len(forTask))
	}
	if forTask[0].ProfileID != "reviewer" {
		t.Fatalf("task usage record profile = %q, want reviewer", forTask[0].ProfileID)
	}
}

// yieldedBatchStage is a running stage that sent two requests to the human, registered one batch wait on them and
// parked on it (with deadline, if given) — what the exit of a turn that yielded on `afm agent wait --message-ids`
// leaves behind. It returns the thread, the requests and the wait of each of them.
func yieldedBatchStage(t *testing.T, o *Orchestrator, deadline *time.Time) (agentcomm.ThreadID, []agentcomm.MessageID, []agentcomm.WaitID) {
	t.Helper()
	ctx := context.Background()
	activateStage(t, o, unreadTestStageID)
	thread, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-batch-thread", Members: []agentcomm.ParticipantID{meshUserParticipantID, unreadTestStage}, Scope: agentcomm.ScopeStage,
	})
	if err != nil {
		t.Fatal(err)
	}
	var requests []agentcomm.MessageID
	for i := range 2 {
		sent, err := o.Send(ctx, SendRequest{
			OperationID: fmt.Sprintf("op-batch-ask-%d", i), ThreadID: thread.ID, Sender: unreadTestStage,
			Recipients: []agentcomm.ParticipantID{meshUserParticipantID}, Kind: agentcomm.MessageKindRequest, Body: "please review",
			Actor: state.Actor{Kind: "assistant", ID: string(unreadTestStage)},
		})
		if err != nil {
			t.Fatal(err)
		}
		requests = append(requests, sent.MessageID)
	}
	receipt, err := o.Wait(ctx, WaitRequest{
		OperationID: "op-batch-wait", Waiter: unreadTestStage, Deadline: deadline,
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo}, MessageIDs: requests,
	})
	if err != nil || receipt.Status != agentcomm.WaitPending {
		t.Fatalf("batch Wait = %+v, %v; want a pending batch (a yield)", receipt, err)
	}
	epoch := o.opts.Store.Snapshot().Stages[unreadTestStageID].Epoch
	o.parkForPeers(ctx, unreadTestStageID, phaseAutonomous, epoch, receiptWaitIDs(receipt), func() { t.Error("no revise here") })
	return thread.ID, requests, receiptWaitIDs(receipt)
}

func answerRequest(t *testing.T, o *Orchestrator, thread agentcomm.ThreadID, request agentcomm.MessageID, opID string) {
	t.Helper()
	if _, err := o.Send(context.Background(), SendRequest{
		OperationID: opID, ThreadID: thread, Sender: meshUserParticipantID, Recipients: []agentcomm.ParticipantID{unreadTestStage},
		Kind: agentcomm.MessageKindReply, InReplyTo: &request, Body: "done",
	}); err != nil {
		t.Fatal(err)
	}
}

// The process died after the last answer of a batch committed but before the stage was resumed: recovery gives
// the stage exactly one turn. With the last answer still missing it leaves the stage parked.
func TestMesh_BatchWaitRecoveryResumesTheStageOnceAfterTheLastAnswer(t *testing.T) {
	t.Run("all answered", func(t *testing.T) {
		runDir := t.TempDir()
		o, store := newUnreadTestOrchestratorAt(t, runDir, flow.AgentAuto, &scriptedStageRunner{turn: func(int32) error { return nil }})
		thread, requests, _ := yieldedBatchStage(t, o, nil)
		answerRequest(t, o, thread, requests[0], "op-answer-0")
		answerRequest(t, o, thread, requests[1], "op-answer-1")
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}

		runner := &scriptedStageRunner{turn: func(int32) error { return nil }}
		_, store2 := restartAndRecover(t, runDir, runner)
		deadline := time.Now().Add(5 * time.Second)
		for runner.turns.Load() == 0 {
			if time.Now().After(deadline) {
				t.Fatalf("no continuation turn after recovery (status %s)", store2.Get(unreadTestStageID))
			}
			time.Sleep(5 * time.Millisecond)
		}
		time.Sleep(150 * time.Millisecond)
		if got := runner.turns.Load(); got != 1 {
			t.Fatalf("turns after recovery = %d, want exactly 1", got)
		}
	})
	t.Run("last answer missing", func(t *testing.T) {
		runDir := t.TempDir()
		o, store := newUnreadTestOrchestratorAt(t, runDir, flow.AgentAuto, &scriptedStageRunner{turn: func(int32) error { return nil }})
		thread, requests, _ := yieldedBatchStage(t, o, nil)
		answerRequest(t, o, thread, requests[0], "op-answer-0")
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}

		runner := &scriptedStageRunner{turn: func(int32) error { return nil }}
		_, store2 := restartAndRecover(t, runDir, runner)
		time.Sleep(150 * time.Millisecond)
		if got := store2.Get(unreadTestStageID); got != state.StatusAwaitingPeer || runner.turns.Load() != 0 {
			t.Fatalf("status %s, turns %d: want the stage still parked, no turn", got, runner.turns.Load())
		}
	})
}

// A stage parked on a batch of two requests, one to a reviewer who finished without answering and one to the
// human. The restart's release of that unanswerable item ends only a part of the batch: the stage stays
// parked. The last answer then continues it once.
func TestMesh_BatchWaitUnanswerableItemAtStartupDoesNotWakeTheStageEarly(t *testing.T) {
	runDir := t.TempDir()
	o, store := newUnreadTestOrchestratorAt(t, runDir, flow.AgentAuto, &scriptedStageRunner{turn: func(int32) error { return nil }})
	ctx := context.Background()
	activateStage(t, o, unreadTestStageID)
	rev, err := o.AddRunAgent(ctx, AddRunAgentRequest{OperationID: "op-rev", ProfileID: "qa", Title: "QA", Actor: state.Actor{Kind: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-batch-thread", Members: []agentcomm.ParticipantID{unreadTestStage, rev.ParticipantID, meshUserParticipantID}, Scope: agentcomm.ScopeStage,
	})
	if err != nil {
		t.Fatal(err)
	}
	o.runHelperAgent = func(ctx context.Context, _, _, _, _, _, _ string) (executor.HelperOutcome, error) {
		<-ctx.Done() // the reviewer never answers: it is cancelled below
		return executor.HelperOutcome{Interrupted: true}, nil
	}
	var requests []agentcomm.MessageID
	for i, to := range []agentcomm.ParticipantID{rev.ParticipantID, meshUserParticipantID} {
		sent, err := o.Send(ctx, SendRequest{
			OperationID: fmt.Sprintf("op-batch-ask-%d", i), ThreadID: thread.ID, Sender: unreadTestStage, Recipients: []agentcomm.ParticipantID{to},
			Kind: agentcomm.MessageKindRequest, Body: "please review", Actor: state.Actor{Kind: "assistant", ID: string(unreadTestStage)},
		})
		if err != nil {
			t.Fatal(err)
		}
		requests = append(requests, sent.MessageID)
	}
	receipt, err := batchWait(o, "op-batch-wait", unreadTestStage, requests...)
	if err != nil || receipt.Status != agentcomm.WaitPending {
		t.Fatalf("batch Wait = %+v, %v; want a pending batch (a yield)", receipt, err)
	}
	epoch := store.Snapshot().Stages[unreadTestStageID].Epoch
	o.parkForPeers(ctx, unreadTestStageID, phaseAutonomous, epoch, receiptWaitIDs(receipt), func() { t.Error("no revise here") })
	// The reviewer's end is committed directly — as if the process stopped right after that commit.
	eff, err := taskStatusEffect(rev.TaskID, assistants.StatusCancelled, "cancelled by user")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyRunOperation(state.Operation{ID: "op-crash-cancel", Kind: "finish_task", Effects: []state.Effect{eff}}, nil); err != nil {
		t.Fatal(err)
	}
	o.concurrency.WaitAgents()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	var turns atomic.Int32
	restarted, store2 := newUnreadTestOrchestratorAt(t, runDir, flow.AgentAuto, &scriptedStageRunner{turn: func(int32) error { turns.Add(1); return nil }})
	runCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(func() { cancel(); restarted.concurrency.WaitAgents() })

	restarted.releaseUnanswerableWaits(runCtx, restarted.resumeWaiterAfterTerminalWaitAtStartup)

	waits := store2.Snapshot().Assistants.Mesh.Waits
	if got := waits[receipt.Items[0].WaitID].Status; got != agentcomm.WaitCancelled {
		t.Fatalf("the unanswerable item = %s, want cancelled", got)
	}
	time.Sleep(150 * time.Millisecond)
	if got := store2.Get(unreadTestStageID); got != state.StatusAwaitingPeer || turns.Load() != 0 {
		t.Fatalf("status %s, turns %d: want the stage still parked while the human's item is pending", got, turns.Load())
	}

	answerRequest(t, restarted, thread.ID, requests[1], "op-answer-1")
	restarted.recoverMeshWaits(runCtx)
	waitUntil(t, "the continuation turn", func() bool { return turns.Load() > 0 })
	time.Sleep(150 * time.Millisecond)
	if got := turns.Load(); got != 1 {
		t.Fatalf("turns after the last answer = %d, want exactly 1", got)
	}
}
