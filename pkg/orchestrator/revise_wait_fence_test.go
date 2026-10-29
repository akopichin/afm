package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// Revise of a stage parked in awaiting_assistants starts a new attempt: the waits the stage left open belong to
// the previous attempt, so they end with the Revise itself instead of continuing the new attempt later.

// reviseFence is stage impl in awaiting_assistants: its required helper still runs, and it waits for the reply of
// QA to a request, with QA's turn held until the test releases it.
type reviseFence struct {
	t       *testing.T
	o       *Orchestrator
	store   *state.Store
	runner  *scriptedStageRunner
	gates   *gatedHelpers
	qa      AttachAndSendResult
	thread  agentcomm.ThreadID
	request agentcomm.MessageID
	waitID  agentcomm.WaitID
	// required is the helper whose turn is still held: the stage waits for it in awaiting_assistants.
	required assistants.TaskID
}

func newReviseFence(t *testing.T) *reviseFence {
	t.Helper()
	o, store, runner := newRunningStage(t)
	o.opts.DashboardURL = fenceDashboardURL // before the QA helper starts reading it
	f := &reviseFence{t: t, o: o, store: store, runner: runner, gates: installGatedHelpers(o, func(string) (executor.HelperOutcome, error) { return passResultOutcome(), nil })}

	f.qa = addTestRunAgent(t, o, "op-qa", nil)
	f.thread = openThread(t, o, "op-th", 0, spawnStage, f.qa.ParticipantID)
	f.request = f.send("req")
	w, err := o.Wait(context.Background(), WaitRequest{
		OperationID: "wait-req", Waiter: spawnStage, Actor: spawnStageActor,
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: f.request},
	})
	if err != nil || w.Status != agentcomm.WaitPending {
		t.Fatalf("Wait = %+v, %v; want pending", w, err)
	}
	f.waitID = w.WaitID

	f.required = attachRequiredTask(context.Background(), t, o, assistantsTestStageID, "op-required")
	waitForTaskStatus(t, store, f.required, assistants.StatusRunning, 2*time.Second)
	o.completeStage(context.Background(), assistantsTestStageID, state.StatusRunning, "main done")
	if got := o.currentStatus(assistantsTestStageID); got != state.StatusAwaitingAssistants {
		t.Fatalf("setup: status = %v, want awaiting_assistants", got)
	}
	return f
}

func (f *reviseFence) send(id string) agentcomm.MessageID {
	f.t.Helper()
	receipt, err := f.o.Send(context.Background(), SendRequest{
		OperationID: "send-" + id, ThreadID: f.thread, Sender: spawnStage, Recipients: []agentcomm.ParticipantID{f.qa.ParticipantID},
		Kind: agentcomm.MessageKindRequest, Body: "request " + id, Actor: spawnStageActor,
	})
	if err != nil {
		f.t.Fatalf("Send %s: %v", id, err)
	}
	return receipt.MessageID
}

func (f *reviseFence) revise() {
	f.t.Helper()
	if applied, _, err := f.o.Revise(context.Background(), assistantsTestStageID, "try it differently"); err != nil || !applied {
		f.t.Fatalf("Revise = %v, %v; want applied", applied, err)
	}
}

func (f *reviseFence) wait(id agentcomm.WaitID) *state.MeshWait {
	return f.store.Snapshot().Assistants.Mesh.Waits[id]
}

func (f *reviseFence) requireSuperseded(id agentcomm.WaitID) {
	f.t.Helper()
	if w := f.wait(id); w.Status != agentcomm.WaitCancelled || w.Reason != meshWaitSupersededReason {
		f.t.Fatalf("wait %s = %s %q, want cancelled: %s", id, w.Status, w.Reason, meshWaitSupersededReason)
	}
}

// answer lets QA's turn end: its result answers the request it was woken with.
func (f *reviseFence) answer() {
	f.t.Helper()
	f.gates.release(f.qa.TaskID)
	waitForRunAgentTurn(f.t, f.store, f.qa.TaskID, taskWait)
}

// journalRecordsWith returns the journal lines that contain every one of parts.
func journalRecordsWith(t *testing.T, runDir string, parts ...string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(runDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
lines:
	for _, line := range strings.Split(string(raw), "\n") {
		for _, p := range parts {
			if !strings.Contains(line, p) {
				continue lines
			}
		}
		out = append(out, line)
	}
	return out
}

func TestReviseFence_EndsTheStagesOpenWaitInTheRecordOfTheBump(t *testing.T) {
	f := newReviseFence(t)
	noticesBefore := strings.Count(noticesText(t, f.o), string(f.waitID))

	f.revise()

	f.requireSuperseded(f.waitID)
	if got := f.store.Snapshot().Assistants.RootGenerations[assistantsTestStageID]; got != 1 {
		t.Fatalf("root generation = %d, want 1", got)
	}
	if n := len(journalRecordsWith(t, f.o.opts.RunDir, "root_generation_bumped", "mesh_wait_resolved", string(f.waitID))); n != 1 {
		t.Fatalf("records carrying the bump and the wait's end = %d, want exactly 1", n)
	}
	if n := len(journalRecordsWith(t, f.o.opts.RunDir, "mesh_wait_resolved", string(f.waitID))); n != 1 {
		t.Fatalf("records ending the wait = %d, want exactly 1", n)
	}
	if got := strings.Count(noticesText(t, f.o), string(f.waitID)) - noticesBefore; got != 1 {
		t.Fatalf("notices about the wait after Revise = %d, want 1", got)
	}
}

func TestReviseFence_LateAnswerDoesNotReviveTheEndedWait(t *testing.T) {
	f := newReviseFence(t)
	f.revise()

	f.answer()

	f.requireSuperseded(f.waitID)
	if peer := f.peerEventsPublished(); len(peer) != 0 {
		t.Fatalf("peer events after the late answer = %v, want none", peer)
	}
	if len(repliesTo(f.store, f.request)) != 1 {
		t.Fatalf("replies to the request = %d, want the late answer recorded once", len(repliesTo(f.store, f.request)))
	}
	if n := len(journalRecordsWith(t, f.o.opts.RunDir, "mesh_wait_resolved", string(f.waitID))); n != 1 {
		t.Fatalf("records ending the wait = %d, want only the Revise's", n)
	}
}

// completionPublished waits for the stage's completion event; the event loop of Run would turn it into done.
func (f *reviseFence) completionPublished() (bus.Event, bool) {
	timeout := time.After(taskWait)
	for {
		select {
		case ev := <-f.o.critical.Recv():
			if ev.Type == bus.EventAgentCompleted && ev.StageID == assistantsTestStageID {
				return ev, true
			}
		case <-timeout:
			return bus.Event{}, false
		}
	}
}

// peerEventsPublished drains the events already published and reports the peer ones.
func (f *reviseFence) peerEventsPublished() []bus.EventType {
	var peer []bus.EventType
	for {
		select {
		case ev := <-f.o.critical.Recv():
			switch ev.Type {
			case bus.EventPeerReplied, bus.EventPeerQuestion, bus.EventPeerWaitEnded:
				peer = append(peer, ev.Type)
			default:
			}
		default:
			return peer
		}
	}
}

func TestReviseFence_NewAttemptIsNotParkedOnTheOldWait(t *testing.T) {
	f := newReviseFence(t)
	f.revise()

	if _, ok := f.completionPublished(); !ok {
		t.Fatalf("the feedback turn did not complete; stage status = %v", f.store.Get(assistantsTestStageID))
	}
	if n := f.runner.turns.Load(); n != 1 {
		t.Fatalf("stage turns after Revise = %d, want 1 (the feedback turn)", n)
	}
	f.requireSuperseded(f.waitID)
}

// A batch of the stage: the items still pending end with the Revise, the answered one stays answered, and the
// new attempt continues on none of them.
func TestReviseFence_BatchEndsItsPendingItemsOnly(t *testing.T) {
	f := newReviseFence(t)
	answered, pending := f.send("batch-1"), f.send("batch-2")
	batch, err := f.o.Wait(context.Background(), WaitRequest{
		OperationID: "wait-batch", Waiter: spawnStage, Actor: spawnStageActor,
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo}, MessageIDs: []agentcomm.MessageID{answered, pending},
	})
	if err != nil || batch.Status != agentcomm.WaitPending {
		t.Fatalf("batch Wait = %+v, %v; want pending", batch, err)
	}
	if _, err := f.o.Send(context.Background(), SendRequest{
		OperationID: "reply-batch-1", ThreadID: f.thread, Sender: f.qa.ParticipantID, Recipients: []agentcomm.ParticipantID{spawnStage},
		Kind: agentcomm.MessageKindReply, InReplyTo: &answered, Body: "done", Actor: state.Actor{Kind: "agent", ID: string(f.qa.ParticipantID)},
	}); err != nil {
		t.Fatalf("reply: %v", err)
	}
	if st := f.wait(batch.Items[0].WaitID).Status; st != agentcomm.WaitReady {
		t.Fatalf("answered item = %s, want ready", st)
	}

	f.revise()

	if st := f.wait(batch.Items[0].WaitID).Status; st != agentcomm.WaitReady {
		t.Fatalf("answered item after Revise = %s, want ready", st)
	}
	f.requireSuperseded(batch.Items[1].WaitID)
	f.requireSuperseded(f.waitID)
	if _, ok := f.completionPublished(); !ok {
		t.Fatalf("the feedback turn did not complete; stage status = %v", f.store.Get(assistantsTestStageID))
	}
	if n := f.runner.turns.Load(); n != 1 {
		t.Fatalf("stage turns = %d, want the feedback turn only", n)
	}
}

// What other participants wait for from the revised stage is not the stage's own wait: the new attempt satisfies it.
func TestReviseFence_AWaitOnTheRevisedStageSurvives(t *testing.T) {
	f := newReviseFence(t)
	other := addTestRunAgent(t, f.o, "op-qa2", nil)
	foreign, err := f.o.Wait(context.Background(), WaitRequest{
		OperationID: "wait-impl-done", Waiter: other.ParticipantID, Actor: state.Actor{Kind: "agent", ID: string(other.ParticipantID)},
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnStageStatus, StageID: assistantsTestStageID, Status: "done"},
	})
	if err != nil || foreign.Status != agentcomm.WaitPending {
		t.Fatalf("Wait = %+v, %v; want pending", foreign, err)
	}

	f.revise()

	if w := f.wait(foreign.WaitID); w.Status != agentcomm.WaitPending {
		t.Fatalf("a wait of another participant on the stage = %s %q after Revise, want pending", w.Status, w.Reason)
	}
	ev, ok := f.completionPublished()
	if !ok {
		t.Fatalf("the feedback turn did not complete; stage status = %v", f.store.Get(assistantsTestStageID))
	}
	if err := f.o.onAgentCompleted(context.Background(), ev); err != nil {
		t.Fatalf("onAgentCompleted: %v", err)
	}
	waitUntil(t, "the new attempt satisfies the wait", func() bool { return f.wait(foreign.WaitID).Status == agentcomm.WaitReady })
}

// An equivalent wait registered by the new attempt is a wait of its own, not the ended one again.
func TestReviseFence_NewAttemptRegistersItsOwnWait(t *testing.T) {
	f := newReviseFence(t)
	f.revise()

	again, err := f.o.Wait(context.Background(), WaitRequest{
		OperationID: "wait-req-again", Waiter: spawnStage, Actor: spawnStageActor,
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: f.request},
	})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if again.WaitID == f.waitID || again.AlreadyPending || again.Status != agentcomm.WaitPending {
		t.Fatalf("Wait = %+v, want a new pending wait, not %s again", again, f.waitID)
	}
	f.requireSuperseded(f.waitID)
}

// An answer that arrives before the Revise is plain information: the wait it satisfied is not touched.
func TestReviseFence_AnswerBeforeReviseKeepsTheWaitReady(t *testing.T) {
	f := newReviseFence(t)
	f.answer()
	if st := f.wait(f.waitID).Status; st != agentcomm.WaitReady {
		t.Fatalf("wait after the answer = %s, want ready", st)
	}

	f.revise()

	if st := f.wait(f.waitID).Status; st != agentcomm.WaitReady {
		t.Fatalf("wait after Revise = %s, want ready", st)
	}
	if _, ok := f.completionPublished(); !ok {
		t.Fatalf("the feedback turn did not complete; stage status = %v", f.store.Get(assistantsTestStageID))
	}
}

// The end of the wait is in the journal: a restart reads it, and no answer after it wakes the new attempt.
func TestReviseFence_SurvivesARestart(t *testing.T) {
	f := newReviseFence(t)
	f.revise()
	if _, ok := f.completionPublished(); !ok {
		t.Fatalf("the feedback turn did not complete; stage status = %v", f.store.Get(assistantsTestStageID))
	}
	f.answer()
	f.o.concurrency.WaitAgents()
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}

	replayed, err := state.LoadRunState(f.o.opts.RunDir)
	if err != nil {
		t.Fatalf("LoadRunState: %v", err)
	}
	if w := replayed.Assistants.Mesh.Waits[f.waitID]; w.Status != agentcomm.WaitCancelled || w.Reason != meshWaitSupersededReason {
		t.Fatalf("replayed wait = %s %q, want cancelled: %s", w.Status, w.Reason, meshWaitSupersededReason)
	}
	resumed, store := reopenAssistantsTestOrchestrator(t, f.o)
	rs := store.Snapshot()
	if w := rs.Assistants.Mesh.Waits[f.waitID]; w.Status != agentcomm.WaitCancelled || w.Reason != meshWaitSupersededReason {
		t.Fatalf("wait after the restart = %s %q, want cancelled: %s", w.Status, w.Reason, meshWaitSupersededReason)
	}
	late := f.request
	if _, err := resumed.Send(context.Background(), SendRequest{
		OperationID: "late-reply", ThreadID: f.thread, Sender: f.qa.ParticipantID, Recipients: []agentcomm.ParticipantID{spawnStage},
		Kind: agentcomm.MessageKindReply, InReplyTo: &late, Body: "after the restart", Actor: state.Actor{Kind: "agent", ID: string(f.qa.ParticipantID)},
	}); err != nil {
		t.Fatalf("late reply: %v", err)
	}
	if w := store.Snapshot().Assistants.Mesh.Waits[f.waitID]; w.Status != agentcomm.WaitCancelled {
		t.Fatalf("wait after a late answer = %s, want cancelled", w.Status)
	}
	resumed.recoverMeshWaits(context.Background())
	if peer := (&reviseFence{o: resumed}).peerEventsPublished(); len(peer) != 0 {
		t.Fatalf("restart recovery published %v, want nothing", peer)
	}
}
