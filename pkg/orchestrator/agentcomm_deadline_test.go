package orchestrator

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/clock"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// Agent Mesh Phase 5, T5.6: deadlines, cancellation and conflicting outcomes.

// waitForCriticalEvent polls events (an o.critical.Recv() channel) until one matching want/stageID shows up,
// or fails the test after timeout — the same polling shape TestMesh_SendWakesStageWaitAndPublishesPeerReplied
// already uses inline, factored out here since this file needs it repeatedly.
func waitForCriticalEvent(t *testing.T, events <-chan bus.Event, want bus.EventType, stageID string, timeout time.Duration) bus.Event {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case ev := <-events:
			if ev.Type == want && ev.StageID == stageID {
				return ev
			}
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Fatalf("expected %s for stage %s within %s, never observed", want, stageID, timeout)
	return bus.Event{}
}

// requireNoCriticalEvent asserts no event of type want for stageID shows up within a short grace window —
// used to prove a guard actually suppressed a resume, not that we simply didn't wait long enough.
func requireNoCriticalEvent(t *testing.T, events <-chan bus.Event, want bus.EventType, stageID string, grace time.Duration) {
	t.Helper()
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		select {
		case ev := <-events:
			if ev.Type == want && ev.StageID == stageID {
				t.Fatalf("unexpected %s for stage %s", want, stageID)
			}
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// --- 1. Deadline timer basics: a stage's own wait expires and resumes it, never via EventPeerReplied. ---

func TestMesh_WaitDeadline_ExpiresStageWaitAndResumesViaPeerWaitEnded(t *testing.T) {
	o, _ := setupHookOrch(t, "s1") // setupHookOrch's clock is a pumped fake — any positive deadline fires fast.
	events := o.critical.Recv()
	parkStageAwaitingPeer(t, o, "s1")
	<-events // drain EvAwaitPeer's own stage_status_changed from parkStageAwaitingPeer's Trigger
	o.prePeerPhase.Store("s1", phaseImplementation)

	deadline := o.clk().Now().Add(time.Millisecond)
	waiter := meshStageParticipantID("s1")
	receipt, err := o.Wait(context.Background(), WaitRequest{
		OperationID: "op-dl-1-wait", Waiter: waiter, Deadline: &deadline,
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: "t-nobody-ever-writes-here"},
	})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if receipt.Status != agentcomm.WaitPending {
		t.Fatalf("wait status = %q, want pending", receipt.Status)
	}

	waitForCriticalEvent(t, events, bus.EventPeerWaitEnded, "s1", 2*time.Second)

	rs := o.opts.Store.Snapshot()
	w := rs.Assistants.Mesh.Waits[receipt.WaitID]
	if w.Status != agentcomm.WaitExpired {
		t.Fatalf("wait status = %q, want expired", w.Status)
	}
	if w.Reason != meshWaitExpiredReason {
		t.Fatalf("wait reason = %q, want %q (stored, visible reason)", w.Reason, meshWaitExpiredReason)
	}
	// Actually dispatching EventPeerWaitEnded into a live spawn is covered by
	// TestMesh_RecoverMeshWaitDeadlines_OverdueResolvesStageSynchronouslyAndResumes, which uses a harness with a
	// real concurrency.Manager; setupHookOrch (used here for its pumped fake clock) has none, so this test stops
	// at "the right event was published with the right payload", matching TestMesh_SendWakesStageWaitAndPublishesPeerReplied's
	// own scope for the T5.3 case.
}

// --- 2. Deadline timer for a helper task (waiting_peer), T5.4's twin. ---

func TestMesh_WaitDeadline_ExpiresHelperTaskWaitAndResumesToCompletion(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	fc := clock.NewFake(time.Now())
	o.clock = fc
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)

	taskID := deriveTaskID("op-dl-2")
	helperID := meshHelperParticipantID(taskID)
	var calls atomic.Int32
	o.runHelperAgent = func(ctx context.Context, tid, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		if tid != string(taskID) {
			return passResultOutcome(), nil
		}
		if calls.Add(1) == 1 {
			deadline := fc.Now().Add(time.Minute)
			if _, err := o.Wait(context.Background(), WaitRequest{
				OperationID: "op-dl-2-wait", Waiter: helperID, Deadline: &deadline,
				Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: "t-does-not-exist-yet"},
			}); err != nil {
				t.Errorf("Wait: %v", err)
			}
			return executor.HelperOutcome{ProcessOK: true}, nil
		}
		return passResultOutcome(), nil
	}

	attachTestTask(ctx, t, o, "op-dl-2")
	waitForTaskStatus(t, store, taskID, assistants.StatusWaitingPeer, 2*time.Second)

	fc.BlockUntil(1)
	fc.Advance(time.Minute)

	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 2*time.Second)
	if got := calls.Load(); got != 2 {
		t.Fatalf("runHelperAgent invoked %d times, want exactly 2 (one yield, one resumed turn after expiry)", got)
	}
	rs := store.Snapshot()
	var found bool
	for _, w := range rs.Assistants.Mesh.Waits {
		if w.Waiter == helperID {
			found = true
			if w.Status != agentcomm.WaitExpired {
				t.Fatalf("wait status = %q, want expired", w.Status)
			}
		}
	}
	if !found {
		t.Fatal("no wait found for helper participant")
	}
	o.concurrency.WaitAgents()
}

// --- 3/5. The race: a deadline and a reply landing together resolve to exactly one outcome, reply never lost.

// newRaceFixture builds a stage-scoped wait (WaitOnAddressedMessage) with a deadline, ready for either a
// direct expireMeshWait call or a Send to resolve it — shared setup for both orderings and the concurrent
// variant below.
func newRaceFixture(t *testing.T) (o *Orchestrator, waitID agentcomm.WaitID, threadID agentcomm.ThreadID, waiter, colleague agentcomm.ParticipantID) {
	t.Helper()
	o, _ = newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	waiter = meshStageParticipantID(assistantsTestStageID)
	colleague = agentcomm.ParticipantID("user")
	thread, err := o.OpenThread(ctx, OpenThreadRequest{OperationID: "op-race-open", Members: []agentcomm.ParticipantID{waiter, colleague}, Scope: agentcomm.ScopeStage})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	deadline := time.Now().Add(time.Hour) // never fires on its own; this test drives expiry directly
	receipt, err := o.Wait(ctx, WaitRequest{
		OperationID: "op-race-wait", Waiter: waiter, Deadline: &deadline,
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: thread.ID},
	})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	return o, receipt.WaitID, thread.ID, waiter, colleague
}

func sendRaceReply(t *testing.T, o *Orchestrator, threadID agentcomm.ThreadID, waiter, colleague agentcomm.ParticipantID, opID string) {
	t.Helper()
	if _, err := o.Send(context.Background(), SendRequest{
		OperationID: opID, ThreadID: threadID, Sender: colleague,
		Recipients: []agentcomm.ParticipantID{waiter}, Kind: agentcomm.MessageKindMessage, Body: "qa says: ready",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
}

// assertMessageDelivered proves the reply is durably present in the recipient's inbox regardless of which
// side won the wait resolution race — "never lose the reply" is about the MESSAGE, not the wait's status.
func assertMessageDelivered(t *testing.T, o *Orchestrator, waiter agentcomm.ParticipantID) {
	t.Helper()
	rs := o.opts.Store.Snapshot()
	if len(rs.Assistants.Mesh.Inbox[waiter]) == 0 {
		t.Fatal("reply message missing from waiter's inbox — a reply must never be lost regardless of the wait-resolution race's outcome")
	}
}

func TestMesh_WaitDeadline_RaceReplyFirst_ReplyWinsAndExpiryIsHarmlessNoOp(t *testing.T) {
	o, waitID, threadID, waiter, colleague := newRaceFixture(t)
	sendRaceReply(t, o, threadID, waiter, colleague, "op-race-reply-1")

	rs := o.opts.Store.Snapshot()
	if got := rs.Assistants.Mesh.Waits[waitID].Status; got != agentcomm.WaitReady {
		t.Fatalf("wait status after reply = %q, want ready", got)
	}

	o.expireMeshWait(context.Background(), waitID) // the timer "fires" after the reply already won

	rs = o.opts.Store.Snapshot()
	if got := rs.Assistants.Mesh.Waits[waitID].Status; got != agentcomm.WaitReady {
		t.Fatalf("wait status after a losing expiry attempt = %q, want still ready (exactly one outcome)", got)
	}
	assertMessageDelivered(t, o, waiter)
}

func TestMesh_WaitDeadline_RaceTimeoutFirst_TimeoutWinsAndReplyStillDelivered(t *testing.T) {
	o, waitID, threadID, waiter, colleague := newRaceFixture(t)

	o.expireMeshWait(context.Background(), waitID)
	rs := o.opts.Store.Snapshot()
	if got := rs.Assistants.Mesh.Waits[waitID].Status; got != agentcomm.WaitExpired {
		t.Fatalf("wait status after expiry = %q, want expired", got)
	}

	sendRaceReply(t, o, threadID, waiter, colleague, "op-race-reply-2") // the reply "lands" right after

	rs = o.opts.Store.Snapshot()
	if got := rs.Assistants.Mesh.Waits[waitID].Status; got != agentcomm.WaitExpired {
		t.Fatalf("wait status after the reply lands post-expiry = %q, want still expired (exactly one outcome)", got)
	}
	// The load-bearing assertion: the message itself is NEVER lost just because it could no longer additionally
	// resolve an already-timed-out wait.
	assertMessageDelivered(t, o, waiter)
}

// TestMesh_WaitDeadline_RaceConcurrent_ExactlyOneOutcomeReplyNeverLost is the true concurrent version, meant to
// be run with -race: expireMeshWait and Send race for real (goroutine scheduling decides who wins), but either
// way the wait must land on EXACTLY ONE terminal status and the message must always be present.
func TestMesh_WaitDeadline_RaceConcurrent_ExactlyOneOutcomeReplyNeverLost(t *testing.T) {
	o, waitID, threadID, waiter, colleague := newRaceFixture(t)

	var wg sync.WaitGroup
	wg.Go(func() { o.expireMeshWait(context.Background(), waitID) })
	wg.Go(func() { sendRaceReply(t, o, threadID, waiter, colleague, "op-race-reply-3") })
	wg.Wait()

	rs := o.opts.Store.Snapshot()
	got := rs.Assistants.Mesh.Waits[waitID].Status
	if got != agentcomm.WaitReady && got != agentcomm.WaitExpired {
		t.Fatalf("wait status = %q, want exactly one of {ready, expired}", got)
	}
	assertMessageDelivered(t, o, waiter)
}

// --- 4/6. CancelWait: idempotent, and now actually resumes a parked waiter (dev-plan's own "Review decisions"
// gap) — without stealing the win from a reply that got there first.

func TestMesh_CancelWait_ResumesAwaitingPeerStageViaPeerWaitEnded(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	events := o.critical.Recv()
	parkStageAwaitingPeer(t, o, "s1")
	<-events
	o.prePeerPhase.Store("s1", phaseImplementation)

	waiter := meshStageParticipantID("s1")
	receipt, err := o.Wait(context.Background(), WaitRequest{
		OperationID: "op-cancel-resume-wait", Waiter: waiter,
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: "t-does-not-exist-yet"}, Deadline: testFarDeadline(),
	})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}

	if err := o.CancelWait(context.Background(), CancelWaitRequest{
		OperationID: "op-cancel-resume-cancel", WaitID: receipt.WaitID, Actor: state.Actor{Kind: "user"},
	}); err != nil {
		t.Fatalf("CancelWait: %v", err)
	}

	waitForCriticalEvent(t, events, bus.EventPeerWaitEnded, "s1", 2*time.Second)
}

func TestMesh_CancelWait_SecondCallDoesNotDoubleResume(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	events := o.critical.Recv()
	parkStageAwaitingPeer(t, o, "s1")
	<-events
	o.prePeerPhase.Store("s1", phaseImplementation)

	waiter := meshStageParticipantID("s1")
	receipt, err := o.Wait(context.Background(), WaitRequest{
		OperationID: "op-cancel-twice-wait", Waiter: waiter,
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: "t-does-not-exist-yet"}, Deadline: testFarDeadline(),
	})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	cancel := CancelWaitRequest{WaitID: receipt.WaitID, Actor: state.Actor{Kind: "user"}}
	cancel.OperationID = "op-cancel-twice-1"
	if err := o.CancelWait(context.Background(), cancel); err != nil {
		t.Fatalf("CancelWait #1: %v", err)
	}
	waitForCriticalEvent(t, events, bus.EventPeerWaitEnded, "s1", 2*time.Second)
	// Simulate the event loop having already dispatched that EventPeerWaitEnded and moved the stage on (this
	// harness has no concurrency.Manager to actually spawn a phase) — resumeAwaitingPeerStage's own
	// currentStatus guard is what makes a second, redundant resume attempt harmless in the real system, same
	// as EventPeerReplied/EventPeerQuestion; this proves that guard holds for EventPeerWaitEnded too.
	if _, ok := o.Trigger("s1", bus.EvComplete, bus.GuardCtx{}, "simulate the live loop having already resumed it"); !ok {
		t.Fatal("EvComplete from awaiting_peer should be allowed by the FSM table")
	}
	<-events // drain that stage_status_changed

	cancel.OperationID = "op-cancel-twice-2"
	if err := o.CancelWait(context.Background(), cancel); err != nil {
		t.Fatalf("CancelWait #2 (already cancelled): %v", err)
	}
	// The stage is no longer awaiting_peer; a second (no-op) cancel must not fire a second resume event.
	requireNoCriticalEvent(t, events, bus.EventPeerWaitEnded, "s1", 150*time.Millisecond)
}

func TestMesh_CancelWait_RaceWithReply_ReplyWinsIsNotOverridden(t *testing.T) {
	o, waitID, threadID, waiter, colleague := newRaceFixture(t)
	sendRaceReply(t, o, threadID, waiter, colleague, "op-cancel-race-reply")

	if err := o.CancelWait(context.Background(), CancelWaitRequest{
		OperationID: "op-cancel-race-cancel", WaitID: waitID, Actor: state.Actor{Kind: "user"},
	}); err != nil {
		t.Fatalf("CancelWait: %v", err)
	}

	rs := o.opts.Store.Snapshot()
	if got := rs.Assistants.Mesh.Waits[waitID].Status; got != agentcomm.WaitReady {
		t.Fatalf("wait status after CancelWait races a winning reply = %q, want still ready", got)
	}
	assertMessageDelivered(t, o, waiter)
}

// --- 7. Pause/Retry/Revise racing with delivery: the SAME currentStatus guard EventPeerReplied/EventPeerQuestion
// already rely on protects EventPeerWaitEnded too — a stage no longer in awaiting_peer is never resumed, so a
// manual pause is never silently lifted and no stale turn starts. Regression tests, not new guard code: the
// mechanism is resumeAwaitingPeerStage/resumeAwaitingPeerPhase's existing check, reused unchanged.

func TestMesh_WaitDeadline_DoesNotResumeStageAlreadyPaused(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	events := o.critical.Recv()
	parkStageAwaitingPeer(t, o, "s1")
	<-events
	o.prePeerPhase.Store("s1", phaseImplementation)

	waiter := meshStageParticipantID("s1")
	deadline := o.clk().Now().Add(time.Millisecond)
	receipt, err := o.Wait(context.Background(), WaitRequest{
		OperationID: "op-pause-race-wait", Waiter: waiter, Deadline: &deadline,
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: "t-does-not-exist-yet"},
	})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}

	// Simulate a concurrent manual pause winning the race BEFORE the deadline timer's resume attempt runs —
	// bus/fsm.go's EvPause.From already includes AwaitingPeer (T5.1: "awaiting_peer... is pausable"), even
	// though the higher-level Orchestrator.Pause gate currently never reaches this status (a separate,
	// pre-existing gap unrelated to T5.6 — see this change's report). Triggering the FSM event directly
	// exercises exactly the state the guard must protect against.
	if _, ok := o.Trigger("s1", bus.EvPause, bus.GuardCtx{}, "manual pause"); !ok {
		t.Fatal("EvPause from awaiting_peer should be allowed by the FSM table")
	}
	<-events // drain EvPause's own stage_status_changed

	// The durable resolution still happens — the wait itself is not special-cased by stage status.
	waitForWaitStatus(t, o, receipt.WaitID, agentcomm.WaitExpired, 2*time.Second)

	requireNoCriticalEvent(t, events, bus.EventPeerWaitEnded, "s1", 200*time.Millisecond)
	if got := o.opts.Store.Get("s1"); got != state.StatusPaused {
		t.Fatalf("status = %v, want still paused — a deadline firing must never lift a manual pause", got)
	}
}

func waitForWaitStatus(t *testing.T, o *Orchestrator, waitID agentcomm.WaitID, want agentcomm.WaitStatus, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if w, ok := o.opts.Store.Snapshot().Assistants.Mesh.Waits[waitID]; ok && w.Status == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("wait %s did not reach status %s within %s", waitID, want, timeout)
}

// TestMesh_WaitDeadline_PausedHeldTaskStaysQueuedAfterExpiry mirrors TestPauseTask_HeldWaitingPeerDoesNotResumeUntilContinue
// (agentcomm_peer_test.go) with an EXPIRED wait instead of a satisfied one: the hold still applies regardless
// of why waiting_peer's wait resolved.
func TestMesh_WaitDeadline_PausedHeldTaskStaysQueuedAfterExpiry(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	fc := clock.NewFake(time.Now())
	o.clock = fc
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)

	taskID := deriveTaskID("op-dl-pause")
	helperID := meshHelperParticipantID(taskID)
	var calls atomic.Int32
	o.runHelperAgent = func(ctx context.Context, tid, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		if tid != string(taskID) {
			return passResultOutcome(), nil
		}
		if calls.Add(1) == 1 {
			deadline := fc.Now().Add(time.Minute)
			if _, err := o.Wait(context.Background(), WaitRequest{
				OperationID: "op-dl-pause-wait", Waiter: helperID, Deadline: &deadline,
				Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: "t-does-not-exist-yet"},
			}); err != nil {
				t.Errorf("Wait: %v", err)
			}
			return executor.HelperOutcome{ProcessOK: true}, nil
		}
		return passResultOutcome(), nil
	}

	attachTestTask(ctx, t, o, "op-dl-pause")
	waitForTaskStatus(t, store, taskID, assistants.StatusWaitingPeer, 2*time.Second)

	if err := o.PauseTask(ctx, taskID); err != nil {
		t.Fatalf("PauseTask: %v", err)
	}

	fc.BlockUntil(1)
	fc.Advance(time.Minute)

	waitForTaskStatus(t, store, taskID, assistants.StatusQueued, 2*time.Second)
	time.Sleep(30 * time.Millisecond)
	if got := store.Snapshot().Assistants.Tasks[taskID].Status; got != assistants.StatusQueued {
		t.Fatalf("status = %s, want still queued (held) — expiry must not start a held task", got)
	}

	if err := o.ContinueTask(ctx, taskID); err != nil {
		t.Fatalf("ContinueTask: %v", err)
	}
	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 2*time.Second)
	o.concurrency.WaitAgents()
}

// --- 8. Startup re-derivation: no durable scheduled-timer facility, so an overdue deadline is resolved
// synchronously at recovery, and a future one gets a freshly armed timer.

func TestMesh_RecoverMeshWaitDeadlines_OverdueResolvesStageSynchronouslyAndResumes(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	past := time.Now().Add(-time.Hour)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	parkStageAwaitingPeer(t, o, assistantsTestStageID)

	waiter := meshStageParticipantID(assistantsTestStageID)
	receipt, err := o.Wait(ctx, WaitRequest{
		OperationID: "op-rec-dl-1-wait", Waiter: waiter, Deadline: &past,
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: "t-does-not-exist-yet"},
	})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}

	o.recoverMeshWaitDeadlines(ctx)

	rs := o.opts.Store.Snapshot()
	if got := rs.Assistants.Mesh.Waits[receipt.WaitID].Status; got != agentcomm.WaitExpired {
		t.Fatalf("wait status = %q, want expired (deadline already past at recovery)", got)
	}
	if got := o.currentStatus(assistantsTestStageID); got == state.StatusAwaitingPeer {
		t.Error("recoverMeshWaitDeadlines must resume a stage whose deadline already passed, not leave it stranded")
	}
}

func TestMesh_RecoverMeshWaitDeadlines_FutureRearmsTimer(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	fc := clock.NewFake(time.Now())
	o.clock = fc
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	parkStageAwaitingPeer(t, o, assistantsTestStageID)

	waiter := meshStageParticipantID(assistantsTestStageID)
	future := fc.Now().Add(time.Minute)
	receipt, err := o.Wait(ctx, WaitRequest{
		OperationID: "op-rec-dl-2-wait", Waiter: waiter, Deadline: &future,
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: "t-does-not-exist-yet"},
	})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	// Wait() itself already armed one timer; recoverMeshWaitDeadlines simulates what a FRESH process would do
	// (that original goroutine is gone), so it must arm its own.
	fc.BlockUntil(1)

	o.recoverMeshWaitDeadlines(ctx)
	fc.BlockUntil(2) // the original process's timer plus the recovery-armed one are both now live waiters

	if got := o.currentStatus(assistantsTestStageID); got != state.StatusAwaitingPeer {
		t.Fatalf("status = %v, want unchanged awaiting_peer (deadline still in the future)", got)
	}

	fc.Advance(time.Minute)
	waitForWaitStatus(t, o, receipt.WaitID, agentcomm.WaitExpired, 2*time.Second)
}

func TestMesh_RecoverMeshWaitDeadlines_OverdueHelperTaskResolvesAndResumes(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	// A FAKE, non-pumped clock (unlike setupHookOrch's) never fires armMeshWaitDeadline's own live timer on its
	// own — only an explicit Advance does — so the "overdue" deadline below is guaranteed to still be
	// unresolved when recoverMeshWaitDeadlines runs, instead of racing the live path on a real clock.
	fc := clock.NewFake(time.Now())
	o.clock = fc
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)

	taskID := deriveTaskID("op-rec-dl-3")
	helperID := meshHelperParticipantID(taskID)
	var calls atomic.Int32
	o.runHelperAgent = func(ctx context.Context, tid, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		if tid != string(taskID) {
			return passResultOutcome(), nil
		}
		if calls.Add(1) == 1 {
			past := fc.Now().Add(-time.Hour)
			if _, err := o.Wait(context.Background(), WaitRequest{
				OperationID: "op-rec-dl-3-wait", Waiter: helperID, Deadline: &past,
				Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: "t-does-not-exist-yet"},
			}); err != nil {
				t.Errorf("Wait: %v", err)
			}
			return executor.HelperOutcome{ProcessOK: true}, nil
		}
		return passResultOutcome(), nil
	}

	attachTestTask(ctx, t, o, "op-rec-dl-3")
	waitForTaskStatus(t, store, taskID, assistants.StatusWaitingPeer, 2*time.Second)

	o.recoverMeshWaitDeadlines(ctx)

	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 2*time.Second)
	if got := calls.Load(); got != 2 {
		t.Fatalf("runHelperAgent invoked %d times, want exactly 2 (one yield, one resumed turn after recovery)", got)
	}
	o.concurrency.WaitAgents()
}

// --- 9. Closing the run resolves remaining waits with run_closed, without resuming anyone (the run is ending
// regardless — see closeRemainingMeshWaits's own doc comment).

func TestMesh_CloseRemainingMeshWaits_ResolvesPendingToRunClosedWithoutResuming(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)

	taskID := deriveTaskID("op-close-1")
	helperID := meshHelperParticipantID(taskID)
	o.runHelperAgent = func(ctx context.Context, tid, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		if _, err := o.Wait(context.Background(), WaitRequest{
			OperationID: "op-close-1-wait", Waiter: helperID,
			Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: "t-does-not-exist-yet"}, Deadline: testFarDeadline(),
		}); err != nil {
			t.Errorf("Wait: %v", err)
		}
		return executor.HelperOutcome{ProcessOK: true}, nil
	}
	attachTestTask(ctx, t, o, "op-close-1")
	waitForTaskStatus(t, store, taskID, assistants.StatusWaitingPeer, 2*time.Second)

	o.closeRemainingMeshWaits()

	rs := store.Snapshot()
	var found bool
	for _, w := range rs.Assistants.Mesh.Waits {
		if w.Waiter != helperID {
			continue
		}
		found = true
		if w.Status != agentcomm.WaitRunClosed {
			t.Fatalf("wait status = %q, want run_closed", w.Status)
		}
		if w.Reason != meshWaitRunClosedReason {
			t.Fatalf("wait reason = %q, want %q", w.Reason, meshWaitRunClosedReason)
		}
	}
	if !found {
		t.Fatal("no wait found for helper participant")
	}
	// closeRemainingMeshWaits deliberately does not resume anyone — the task must stay exactly where it was.
	time.Sleep(50 * time.Millisecond)
	if got := store.Snapshot().Assistants.Tasks[taskID].Status; got != assistants.StatusWaitingPeer {
		t.Fatalf("task status = %s, want unchanged waiting_peer (closeRemainingMeshWaits must not resume anyone)", got)
	}
}

func TestMesh_CloseRemainingMeshWaits_IsIdempotentAndLeavesSatisfiedWaitsAlone(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	h1 := meshHelperParticipantID(newRunningHelperForMesh(t, o, store, "op-close-2a"))
	h2 := meshHelperParticipantID(newRunningHelperForMesh(t, o, store, "op-close-2b"))

	thread, err := o.OpenThread(ctx, OpenThreadRequest{OperationID: "op-close-2-open", Members: []agentcomm.ParticipantID{h1, h2}, Scope: agentcomm.ScopeStage})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	send, err := o.Send(ctx, SendRequest{OperationID: "op-close-2-send", ThreadID: thread.ID, Sender: h1, Recipients: []agentcomm.ParticipantID{h2}, Kind: agentcomm.MessageKindRequest, Body: "ping"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if _, err := o.Send(ctx, SendRequest{OperationID: "op-close-2-reply", ThreadID: thread.ID, Sender: h2, Recipients: []agentcomm.ParticipantID{h1}, Kind: agentcomm.MessageKindReply, InReplyTo: &send.MessageID, Body: "pong"}); err != nil {
		t.Fatalf("Send reply: %v", err)
	}
	receipt, err := o.Wait(ctx, WaitRequest{OperationID: "op-close-2-wait", Waiter: h1, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: send.MessageID}})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if receipt.Status != agentcomm.WaitReady {
		t.Fatalf("wait status = %q, want ready", receipt.Status)
	}

	o.closeRemainingMeshWaits()
	o.closeRemainingMeshWaits() // idempotent: calling it twice must not error or change an already-settled wait

	rs := store.Snapshot()
	if got := rs.Assistants.Mesh.Waits[receipt.WaitID].Status; got != agentcomm.WaitReady {
		t.Fatalf("an already-satisfied wait = %q, want untouched ready (closeRemainingMeshWaits only acts on Pending)", got)
	}
}

// --- Batch waits: each item has its own deadline timer; the stage resumes only when the last item has ended. ---

func TestMesh_BatchWaitDeadlineEndingPartOfTheSetDoesNotResume(t *testing.T) {
	f := newBatchFixture(t, 2)
	ids := receiptWaitIDs(f.mustWait("op-batch-wait", testFarDeadline(), 0, 1))
	f.park(ids)

	f.o.expireMeshWait(context.Background(), ids[0])
	f.requireResumes()
	if got := f.waitStatus(ids[0]); got != agentcomm.WaitExpired {
		t.Fatalf("item 0 = %s, want expired", got)
	}

	f.o.expireMeshWait(context.Background(), ids[1])
	f.requireResumes(bus.EventPeerWaitEnded)
}

// The last pending item running out of time wakes the stage with what the answered items brought.
func TestMesh_BatchWaitDeadlineOfTheLastPendingItemWakesWithTheAnswers(t *testing.T) {
	f := newBatchFixture(t, 2)
	ids := receiptWaitIDs(f.mustWait("op-batch-wait", testFarDeadline(), 0, 1))
	f.park(ids)
	f.reply(0)
	f.requireResumes()

	f.o.expireMeshWait(context.Background(), ids[1])
	f.requireResumes(bus.EventPeerReplied)
	if got := f.waitStatus(ids[1]); got != agentcomm.WaitExpired {
		t.Fatalf("item 1 = %s, want expired", got)
	}
}

// The deadline of a batch applies to every item: each pending one gets its own timer and ends expired.
func TestMesh_BatchWaitDeadlineExpiresEveryPendingItem(t *testing.T) {
	f := newBatchFixture(t, 3)
	f.reply(0)
	deadline := f.o.clk().Now().Add(time.Millisecond) // the pumped fake clock runs it out at once
	receipt := f.mustWait("op-batch-wait", &deadline, 0, 1, 2)
	ids := receiptWaitIDs(receipt)

	for _, id := range ids[1:] {
		waitForWaitStatus(t, f.o, id, agentcomm.WaitExpired, 2*time.Second)
	}
	if got := f.waitStatus(ids[0]); got != agentcomm.WaitReady {
		t.Fatalf("the answered item = %s, want it left ready", got)
	}
}

// A restart finds a batch whose deadline passed while the process was down: every overdue item ends and the
// stage is resumed once, at the last of them, with the event that fits what the batch brought. The first
// process runs on a fake clock that never fires, so no live timer ends an item before the recovery sweep does.
func TestMesh_RecoverMeshWaitDeadlines_OverdueBatchEndsEveryItem(t *testing.T) {
	cases := []struct {
		name      string
		answered  []int // requests answered before the restart
		wantEvent bus.FSMEvent
	}{
		{"nothing answered", nil, bus.EvPeerWaitEnded},
		{"one item answered", []int{0}, bus.EvPeerReplied},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runDir := t.TempDir()
			o, store := newUnreadTestOrchestratorAt(t, runDir, flow.AgentAuto, &scriptedStageRunner{turn: func(int32) error { return nil }})
			o.clock = clock.NewFake(time.Now())
			past := o.clk().Now().Add(-time.Hour)
			thread, requests, waits := yieldedBatchStage(t, o, &past)
			for _, i := range tc.answered {
				answerRequest(t, o, thread, requests[i], fmt.Sprintf("op-answer-%d", i))
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}

			var turns atomic.Int32
			restarted, store2 := restartAndRecover(t, runDir, &scriptedStageRunner{turn: func(int32) error { turns.Add(1); return nil }})
			restarted.recoverMeshWaitDeadlines(context.Background())

			for i, id := range waits {
				if got := store2.Snapshot().Assistants.Mesh.Waits[id].Status; got == agentcomm.WaitPending {
					t.Fatalf("item %d still pending after the sweep", i)
				}
			}
			waitUntil(t, "the continuation turn", func() bool { return turns.Load() > 0 })
			time.Sleep(150 * time.Millisecond)
			if got := turns.Load(); got != 1 {
				t.Fatalf("turns = %d, want exactly 1", got)
			}
			history, err := store2.History()
			if err != nil {
				t.Fatal(err)
			}
			var continuations []string
			for _, tr := range history {
				if tr.StageID == unreadTestStageID && tr.From == state.StatusAwaitingPeer {
					continuations = append(continuations, tr.Event)
				}
			}
			if want := []string{string(tc.wantEvent)}; !slices.Equal(continuations, want) {
				t.Fatalf("continuations of the stage = %v, want exactly %v", continuations, want)
			}
		})
	}
}
