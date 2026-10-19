package orchestrator

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// Agent Mesh Phase 5, T5.5: answering a counter-question while still waiting. See agentcomm_resume.go's own
// header comment for the design — these tests exercise it at both scopes: helper-task (waiting_peer, full
// end-to-end via the fake runHelperAgent seam, mirroring agentcomm_peer_test.go's T5.4 tests) and stage
// (awaiting_peer, unit-level dispatch/FSM wiring, mirroring agentcomm_yield_test.go's T5.3 tests — stage-scope
// has no fake-executor seam in this codebase even for T5.3's own peer-reply resume, so it is not end-to-end
// tested there either).

// waitForCounter blocks until counter reaches at least want, or fails the test after timeout — used where a
// status-based poll (waitForTaskStatus) could return a false "already there" positive because the status in
// question was already true before the event under test even happened (a plain re-park looks identical to
// the ORIGINAL park unless the invocation count is checked directly).
func waitForCounter(t *testing.T, counter *atomic.Int32, want int32, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if counter.Load() >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("counter = %d after %s, want >= %d", counter.Load(), timeout, want)
}

// TestWait_SurvivesCounterQuestion is T5.5's central, end-to-end claim: A (a helper task) waits for B's
// result on one thread; B asks A a question on a DIFFERENT thread — a genuine counter-question, not the
// reply A is actually waiting for. A is resumed for a bounded turn, answers B, and its ORIGINAL wait is still
// registered (WaitPending, same WaitID) afterwards: the counter-question is not mistaken for the answer to
// the original request, the task is not completed, and it is not re-run concurrently (exactly 2 invocations
// after the question, never a 3rd until B's real reply arrives). Only then does the real reply complete it.
func TestWait_SurvivesCounterQuestion(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)

	taskID := deriveTaskID("op-cq-1")
	a := meshHelperParticipantID(taskID)
	b := agentcomm.ParticipantID("user") // B, always a known participant, no extra fixture needed
	realThread := deriveMeshThreadID("op-cq-1-real-open")
	sideThread := deriveMeshThreadID("op-cq-1-side-open")

	var calls atomic.Int32
	var waitID agentcomm.WaitID
	o.runHelperAgent = func(ctx context.Context, tid, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		if tid != string(taskID) {
			return passResultOutcome(), nil
		}
		switch calls.Add(1) {
		case 1:
			// A's first turn: register the REAL wait (on realThread) and exit.
			receipt, err := o.Wait(context.Background(), WaitRequest{
				OperationID: "op-cq-1-wait", Waiter: a,
				Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: realThread}, Deadline: testFarDeadline(),
			})
			if err != nil {
				t.Errorf("Wait: %v", err)
			}
			waitID = receipt.WaitID
			return executor.HelperOutcome{ProcessOK: true}, nil
		case 2:
			// The BOUNDED turn: A was resumed because of B's counter-question on sideThread. A well-behaved
			// agent answers it and simply exits again — it must NOT touch its own wait (a real agent would not
			// even know its WaitID; the orchestrator is what must not mistake this for the answer).
			if _, err := o.Send(context.Background(), SendRequest{
				OperationID: "op-cq-1-answer", ThreadID: sideThread, Sender: a,
				Recipients: []agentcomm.ParticipantID{b}, Kind: agentcomm.MessageKindMessage, Body: "staging, see you there",
			}); err != nil {
				t.Errorf("answering B's counter-question: %v", err)
			}
			return executor.HelperOutcome{ProcessOK: true}, nil
		default:
			// The FINAL turn, only reached once B's REAL reply resolves A's original wait.
			return passResultOutcome(), nil
		}
	}

	attachTestTask(ctx, t, o, "op-cq-1")
	waitForTaskStatus(t, store, taskID, assistants.StatusWaitingPeer, 2*time.Second)
	if got := calls.Load(); got != 1 {
		t.Fatalf("runHelperAgent invoked %d times before any message, want exactly 1 (the initial yield)", got)
	}

	// B opens a SEPARATE thread and asks A a question — NOT the reply A is actually waiting for.
	if _, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-cq-1-side-open", Members: []agentcomm.ParticipantID{a, b}, Scope: agentcomm.ScopeStage,
	}); err != nil {
		t.Fatalf("OpenThread (side): %v", err)
	}
	if _, err := o.Send(ctx, SendRequest{
		OperationID: "op-cq-1-question", ThreadID: sideThread, Sender: b,
		Recipients: []agentcomm.ParticipantID{a}, Kind: agentcomm.MessageKindMessage, Body: "quick one: which environment?",
	}); err != nil {
		t.Fatalf("Send (question): %v", err)
	}

	// The bounded turn must run exactly once, answer, and leave A re-parked in waiting_peer.
	waitForCounter(t, &calls, 2, 2*time.Second)
	waitForTaskStatus(t, store, taskID, assistants.StatusWaitingPeer, 2*time.Second)

	rs := store.Snapshot()
	if w, ok := rs.Assistants.Mesh.Waits[waitID]; !ok || w.Status != agentcomm.WaitPending {
		t.Fatalf("original wait status = %+v, want still pending — the counter-question must not resolve it", w)
	}
	if got := rs.Assistants.Tasks[taskID].Status; got != assistants.StatusWaitingPeer {
		t.Fatalf("task status = %v, want still waiting_peer — the communication turn must not complete the task", got)
	}

	// Give the (absent) third invocation a moment to NOT happen: proves the bounded turn did not also spawn a
	// concurrent second resume racing itself.
	time.Sleep(30 * time.Millisecond)
	if got := calls.Load(); got != 2 {
		t.Fatalf("runHelperAgent invoked %d times, want exactly 2 (one yield, one bounded Q&A turn) before the real reply arrives", got)
	}

	// NOW B's REAL reply arrives on realThread — this resolves A's ACTUAL wait and lets it complete.
	if _, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-cq-1-real-open", Members: []agentcomm.ParticipantID{a, b}, Scope: agentcomm.ScopeStage,
	}); err != nil {
		t.Fatalf("OpenThread (real): %v", err)
	}
	if _, err := o.Send(ctx, SendRequest{
		OperationID: "op-cq-1-real-send", ThreadID: realThread, Sender: b,
		Recipients: []agentcomm.ParticipantID{a}, Kind: agentcomm.MessageKindMessage, Body: "ready",
	}); err != nil {
		t.Fatalf("Send (real reply): %v", err)
	}

	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 2*time.Second)
	if got := calls.Load(); got != 3 {
		t.Fatalf("runHelperAgent invoked %d times, want exactly 3 (yield, bounded Q&A turn, final completion)", got)
	}
	o.concurrency.WaitAgents()
}

// TestWait_SurvivesCounterQuestion_ChildQuestionsWaitingParent is T5.5's "same holds for a child questioning
// a waiting parent" claim. Structurally, a task's admitChildren parent/child relationship and waiting_peer
// are mutually exclusive on the SAME task: handleDelegate moves a parent straight to waiting_children in the
// same commit that creates its children, and tryResumeParent only ever fires once every current-generation
// child is terminal — so a task cannot simultaneously have a live (non-terminal) admitChildren child AND be
// independently parked in waiting_peer. "A child questions a waiting parent" therefore cannot mean anything
// structurally different from the generic case TestWait_SurvivesCounterQuestion already proves: ANY mesh
// participant (sibling task, helper, or — in the dashboard's own language — a "child" worker attached to the
// same stage) can send a waiting_peer task a counter-question, with no participant-kind-specific machinery.
// This test demonstrates it with two independently-attached helper tasks playing the parent/child roles,
// confirming maybeResumeAwaitingPeerQuestion/tryResumePeerForQuestion are agnostic to which one does the
// asking.
func TestWait_SurvivesCounterQuestion_ChildQuestionsWaitingParent(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)

	parentID := deriveTaskID("op-cq-parent")
	childID := deriveTaskID("op-cq-child")
	parent := meshHelperParticipantID(parentID)
	child := meshHelperParticipantID(childID)
	realThread := deriveMeshThreadID("op-cq-parent-real-open")
	sideThread := deriveMeshThreadID("op-cq-parent-side-open")

	var parentCalls atomic.Int32
	o.runHelperAgent = func(ctx context.Context, tid, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		switch tid {
		case string(childID):
			// The "child" worker's only job is to ask the parent a question, then report its own result —
			// independent of the parent's own wait. OpenThread lazily registers both ends (T2.2) — by now both
			// parent and child already exist as real tasks, so this is the first point either CAN be named as
			// a thread member.
			if _, err := o.OpenThread(context.Background(), OpenThreadRequest{
				OperationID: "op-cq-parent-side-open", Members: []agentcomm.ParticipantID{parent, child}, Scope: agentcomm.ScopeStage,
			}); err != nil {
				t.Errorf("OpenThread (child->parent): %v", err)
			}
			if _, err := o.Send(context.Background(), SendRequest{
				OperationID: "op-cq-child-question", ThreadID: sideThread, Sender: child,
				Recipients: []agentcomm.ParticipantID{parent}, Kind: agentcomm.MessageKindMessage, Body: "parent, which format do you want?",
			}); err != nil {
				t.Errorf("Send (child question): %v", err)
			}
			return passResultOutcome(), nil
		case string(parentID):
			switch parentCalls.Add(1) {
			case 1:
				if _, err := o.Wait(context.Background(), WaitRequest{
					OperationID: "op-cq-parent-wait", Waiter: parent,
					Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: realThread}, Deadline: testFarDeadline(),
				}); err != nil {
					t.Errorf("Wait: %v", err)
				}
				return executor.HelperOutcome{ProcessOK: true}, nil
			case 2:
				// Bounded turn: answer the child's question, do not touch the parent's own wait.
				return executor.HelperOutcome{ProcessOK: true}, nil
			default:
				return passResultOutcome(), nil
			}
		default:
			return passResultOutcome(), nil
		}
	}

	attachTestTask(ctx, t, o, "op-cq-parent")
	waitForTaskStatus(t, store, parentID, assistants.StatusWaitingPeer, 2*time.Second)

	attachTestTask(ctx, t, o, "op-cq-child")
	waitForTaskStatus(t, store, childID, assistants.StatusCompleted, 2*time.Second)

	waitForCounter(t, &parentCalls, 2, 2*time.Second)
	waitForTaskStatus(t, store, parentID, assistants.StatusWaitingPeer, 2*time.Second)
	if got := store.Snapshot().Assistants.Tasks[parentID].Status; got != assistants.StatusWaitingPeer {
		t.Fatalf("parent status = %v, want still waiting_peer after answering the child's question", got)
	}

	if _, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-cq-parent-real-open", Members: []agentcomm.ParticipantID{parent, child}, Scope: agentcomm.ScopeStage,
	}); err != nil {
		t.Fatalf("OpenThread (real): %v", err)
	}
	if _, err := o.Send(ctx, SendRequest{
		OperationID: "op-cq-parent-real-send", ThreadID: realThread, Sender: child,
		Recipients: []agentcomm.ParticipantID{parent}, Kind: agentcomm.MessageKindMessage, Body: "json, please",
	}); err != nil {
		t.Fatalf("Send (real reply): %v", err)
	}
	waitForTaskStatus(t, store, parentID, assistants.StatusCompleted, 2*time.Second)
	o.concurrency.WaitAgents()
}

// TestMesh_ResumeAfterPeerQuestion_PublishesDistinctEvent proves resumeAfterPeerQuestion goes through the
// same CAS-gated publish path as resumeAfterPeerReply (T5.3), but with its OWN event type — mirrors
// TestMesh_ResumeAfterPeerReply_PublishesWhenAwaitingPeer.
func TestMesh_ResumeAfterPeerQuestion_PublishesDistinctEvent(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	events := o.critical.Recv()
	o.Trigger("s1", bus.EvAwaitPeer, bus.GuardCtx{Phase: phaseImplementation}, "")
	<-events // drain this Trigger's own stage_status_changed notification

	o.resumeAfterPeerQuestion(context.Background(), "s1")

	select {
	case ev := <-events:
		if ev.Type != bus.EventPeerQuestion || ev.StageID != "s1" {
			t.Fatalf("got %+v, want EventPeerQuestion for s1", ev)
		}
	default:
		t.Fatal("expected EventPeerQuestion to be published")
	}
}

// TestMesh_ResumeAfterPeerQuestion_NoOpWhenNotAwaitingPeer mirrors
// TestMesh_ResumeAfterPeerReply_NoOpWhenNotAwaitingPeer.
func TestMesh_ResumeAfterPeerQuestion_NoOpWhenNotAwaitingPeer(t *testing.T) {
	o, _ := setupHookOrch(t, "s1") // status is StatusRunning from setupHookOrch
	events := o.critical.Recv()

	o.resumeAfterPeerQuestion(context.Background(), "s1")

	select {
	case ev := <-events:
		t.Fatalf("expected no event when the stage is not awaiting_peer, got %+v", ev)
	default:
	}
}

// TestMesh_SendCounterQuestionPublishesPeerQuestionNotReply is T5.5's stage-scope detection claim at the
// Service level, mirroring TestMesh_SendWakesStageWaitAndPublishesPeerReplied: an addressed message that does
// NOT resolve a parked stage's own wait publishes EventPeerQuestion (never EventPeerReplied), and the
// original wait stays WaitPending — the counter-question is not recorded as its answer.
func TestMesh_SendCounterQuestionPublishesPeerQuestionNotReply(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	events := o.critical.Recv()
	ctx := context.Background()

	waiter := meshStageParticipantID("s1")
	colleague := agentcomm.ParticipantID("user")
	realThread, err := o.OpenThread(ctx, OpenThreadRequest{OperationID: "op-cqs-real-open", Members: []agentcomm.ParticipantID{waiter, colleague}, Scope: agentcomm.ScopeStage})
	if err != nil {
		t.Fatalf("OpenThread (real): %v", err)
	}

	o.Trigger("s1", bus.EvAwaitPeer, bus.GuardCtx{Phase: phaseImplementation}, "")
	<-events // drain stage_status_changed
	o.prePeerPhase.Store("s1", phaseImplementation)

	waitReceipt, err := o.Wait(ctx, WaitRequest{
		OperationID: "op-cqs-wait", Waiter: waiter,
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: realThread.ID},
	})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}

	sideThread, err := o.OpenThread(ctx, OpenThreadRequest{OperationID: "op-cqs-side-open", Members: []agentcomm.ParticipantID{waiter, colleague}, Scope: agentcomm.ScopeStage})
	if err != nil {
		t.Fatalf("OpenThread (side): %v", err)
	}
	if _, err := o.Send(ctx, SendRequest{
		OperationID: "op-cqs-question", ThreadID: sideThread.ID, Sender: colleague,
		Recipients: []agentcomm.ParticipantID{waiter}, Kind: agentcomm.MessageKindMessage, Body: "quick one while you wait",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	rs := o.opts.Store.Snapshot()
	if w := rs.Assistants.Mesh.Waits[waitReceipt.WaitID]; w.Status != agentcomm.WaitPending {
		t.Fatalf("original wait status = %v, want still pending (the counter-question must not resolve it)", w.Status)
	}

	var sawPeerQuestion bool
	for {
		select {
		case ev := <-events:
			if ev.Type == bus.EventPeerReplied {
				t.Fatal("got EventPeerReplied for an unrelated message — the counter-question must not be mistaken for the awaited reply")
			}
			if ev.Type == bus.EventPeerQuestion && ev.StageID == "s1" {
				sawPeerQuestion = true
			}
		default:
			if !sawPeerQuestion {
				t.Fatal("expected EventPeerQuestion to be published when an unrelated message arrives while parked")
			}
			return
		}
	}
}

// TestMesh_HasUndeliveredInbox is a focused unit test for the T5.5 detection primitive: a message addressed
// to a participant is "undelivered" until that participant's own spawned turn marks it so (T4.4's
// OnDelivered), regardless of whether any wait is involved at all.
func TestMesh_HasUndeliveredInbox(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	ctx := context.Background()
	waiter := meshStageParticipantID("s1")
	colleague := agentcomm.ParticipantID("user")

	if o.hasWakingUnread(waiter) {
		t.Fatal("no message sent yet: want false")
	}

	thread, err := o.OpenThread(ctx, OpenThreadRequest{OperationID: "op-undelivered-open", Members: []agentcomm.ParticipantID{waiter, colleague}, Scope: agentcomm.ScopeStage})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	send, err := o.Send(ctx, SendRequest{
		OperationID: "op-undelivered-send", ThreadID: thread.ID, Sender: colleague,
		Recipients: []agentcomm.ParticipantID{waiter}, Kind: agentcomm.MessageKindMessage, Body: "hello",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !o.hasWakingUnread(waiter) {
		t.Fatal("a message was just sent and never delivered: want true")
	}

	if err := o.MarkMessageDelivered(ctx, send.MessageID, waiter); err != nil {
		t.Fatalf("MarkMessageDelivered: %v", err)
	}
	if o.hasWakingUnread(waiter) {
		t.Fatal("the only message was just marked delivered: want false")
	}
}

// TestWait_SurvivesCounterQuestion_CrashRecovery is T5.5's crash variant: a stage parked in awaiting_peer
// with a genuinely pending ORIGINAL wait, plus an undelivered counter-question committed before the process
// went down (nothing in this bare harness ever drains the critical bus the live Send path would normally
// publish to — exactly the gap a startup recovery sweep must close). recoverMeshWaits must resume it to
// exactly one pending (bounded) turn: a second, redundant sweep must not start a second one.
func TestWait_SurvivesCounterQuestion_CrashRecovery(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	parkStageAwaitingPeer(t, o, assistantsTestStageID)

	waiter := meshStageParticipantID(assistantsTestStageID)
	colleague := agentcomm.ParticipantID("user")

	// The ORIGINAL request: a wait that has genuinely NOT resolved.
	realThread, err := o.OpenThread(ctx, OpenThreadRequest{OperationID: "op-cqc-real-open", Members: []agentcomm.ParticipantID{waiter, colleague}, Scope: agentcomm.ScopeStage})
	if err != nil {
		t.Fatalf("OpenThread (real): %v", err)
	}
	waitReceipt, err := o.Wait(ctx, WaitRequest{
		OperationID: "op-cqc-wait", Waiter: waiter,
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: realThread.ID},
	})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if waitReceipt.Status != agentcomm.WaitPending {
		t.Fatalf("original wait status = %q, want pending", waitReceipt.Status)
	}

	// A colleague's counter-question, committed on a DIFFERENT thread, with nothing ever reacting to it live.
	sideThread, err := o.OpenThread(ctx, OpenThreadRequest{OperationID: "op-cqc-side-open", Members: []agentcomm.ParticipantID{waiter, colleague}, Scope: agentcomm.ScopeStage})
	if err != nil {
		t.Fatalf("OpenThread (side): %v", err)
	}
	if _, err := o.Send(ctx, SendRequest{
		OperationID: "op-cqc-question", ThreadID: sideThread.ID, Sender: colleague,
		Recipients: []agentcomm.ParticipantID{waiter}, Kind: agentcomm.MessageKindMessage, Body: "quick one while you wait",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// A FRESH process's startup sweep must recover this to exactly one pending turn.
	o.recoverMeshWaits(ctx)

	if got := o.currentStatus(assistantsTestStageID); got == state.StatusAwaitingPeer {
		t.Fatal("recoverMeshWaits must resume a stage with an undelivered counter-question, not leave it stranded")
	}
	rs := o.opts.Store.Snapshot()
	if w := rs.Assistants.Mesh.Waits[waitReceipt.WaitID]; w.Status != agentcomm.WaitPending {
		t.Fatalf("original wait status = %v, want still pending — the counter-question must not be mistaken for the reply", w.Status)
	}

	// A second, redundant sweep (e.g. racing a live trigger, or simply run twice) must not start a second,
	// concurrent clarification turn — the FSM's own CAS (status is no longer awaiting_peer) makes this a
	// silent no-op, exactly like T5.3's "two replies arriving simultaneously still make one continuation".
	statusAfterFirst := o.currentStatus(assistantsTestStageID)
	o.recoverMeshWaits(ctx)
	if got := o.currentStatus(assistantsTestStageID); got != statusAfterFirst {
		t.Fatalf("status changed on a second recovery pass (%v -> %v) — want exactly one resume", statusAfterFirst, got)
	}
}
