package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

// A request a task agent was handed and then left unanswered — read in the middle of a turn, or one of several
// the turn started with — cannot be answered any more: its result answers only the waking message. The waits for
// it end, with the reason, instead of holding their waiters forever.

const leftReason = "read the message and its turn ended without a reply"

// leftFixture is a stage that waits on a run agent (QA). QA's turn 1 starts handed one request (A) and holds
// until the test releases it, so requests can reach it in the middle of the turn; turn(n) is what a turn does
// after that. The agent's inbox is delivered as the real executor does once the process has started.
type leftFixture struct {
	t       *testing.T
	o       *Orchestrator
	store   *state.Store
	runner  *scriptedStageRunner
	rev     AttachAndSendResult
	thread  agentcomm.ThreadID
	started chan struct{}
	release chan struct{}
	ready   chan struct{}
	turns   atomic.Int32
	turn    func(n int32) executor.HelperOutcome // nil: a pass result
}

func newLeftFixtureAt(t *testing.T, runDir string) *leftFixture {
	t.Helper()
	runner := &scriptedStageRunner{turn: func(int32) error { return nil }}
	o, store := newUnreadTestOrchestratorAt(t, runDir, flow.AgentAuto, runner)
	f := &leftFixture{t: t, o: o, store: store, runner: runner, started: make(chan struct{}), release: make(chan struct{}), ready: make(chan struct{})}
	o.runHelperAgent = func(ctx context.Context, _, _, _, _, _, _ string) (executor.HelperOutcome, error) {
		awaitRelease(t, f.ready)
		n := f.turns.Add(1)
		o.deliverRenderedInbox(f.rev.ParticipantID)()
		if n == 1 {
			close(f.started)
			select {
			case <-f.release:
			case <-ctx.Done(): // a Pause stops the process
				return executor.HelperOutcome{}, nil
			}
		}
		if f.turn != nil {
			return f.turn(n), nil
		}
		return passResultOutcome(), nil
	}
	msg := "task A"
	f.rev = addTestRunAgent(t, o, "op-rev", &msg)
	f.thread = openThread(t, o, "op-th", 0, unreadTestStage, f.rev.ParticipantID)
	close(f.ready)
	<-f.started
	return f
}

func newLeftFixture(t *testing.T) *leftFixture { return newLeftFixtureAt(t, t.TempDir()) }

// ask sends a request from the stage to QA and registers the stage's reply_to wait on it.
func (f *leftFixture) ask(id string) (agentcomm.MessageID, agentcomm.WaitID) {
	f.t.Helper()
	msg := f.send(id)
	w, err := f.o.Wait(context.Background(), WaitRequest{
		OperationID: "wait-" + id, Waiter: unreadTestStage, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: msg},
	})
	if err != nil || w.Status != agentcomm.WaitPending {
		f.t.Fatalf("Wait on %s = %+v, %v; want pending", id, w, err)
	}
	return msg, w.WaitID
}

// send sends a request from the stage to QA, with no wait on it.
func (f *leftFixture) send(id string) agentcomm.MessageID {
	f.t.Helper()
	receipt, err := f.o.Send(context.Background(), SendRequest{
		OperationID: "send-" + id, ThreadID: f.thread, Sender: unreadTestStage, Recipients: []agentcomm.ParticipantID{f.rev.ParticipantID},
		Kind: agentcomm.MessageKindRequest, Body: "request " + id, Actor: state.Actor{Kind: "assistant", ID: unreadTestStageID},
	})
	if err != nil {
		f.t.Fatalf("Send %s: %v", id, err)
	}
	return receipt.MessageID
}

// readInboxMidTurn is what `afm agent inbox` does: it hands the agent what it has not seen.
func (f *leftFixture) readInboxMidTurn() { readInbox(f.t, f.o, f.rev.ParticipantID) }

// finish lets turn 1 end and waits until the agent is idle again.
func (f *leftFixture) finish() {
	f.t.Helper()
	close(f.release)
	waitForRunAgentTurn(f.t, f.store, f.rev.TaskID, taskWait)
	f.o.concurrency.WaitAgents()
}

func (f *leftFixture) wait(id agentcomm.WaitID) *state.MeshWait {
	return f.store.Snapshot().Assistants.Mesh.Waits[id]
}

func (f *leftFixture) requireCancelled(id agentcomm.WaitID) {
	f.t.Helper()
	if w := f.wait(id); w.Status != agentcomm.WaitCancelled || !strings.Contains(w.Reason, leftReason) || !strings.Contains(w.Reason, "QA") {
		f.t.Fatalf("wait %s = %s %q, want cancelled: QA %s", id, w.Status, w.Reason, leftReason)
	}
}

const taskWait = 5 * time.Second

// The incident: a request read in the middle of the turn and left without a reply. The turn's result answers
// A only; the wait for B is cancelled with the reason and points at the result.
func TestLeftUnanswered_ReadMidTurnWithoutReply(t *testing.T) {
	f := newLeftFixture(t)
	b, wait := f.ask("b")
	f.turn = nil
	f.readInboxMidTurn()
	f.finish()

	waitUntil(t, "the wait for B ends", func() bool { return f.wait(wait).Status != agentcomm.WaitPending })
	f.requireCancelled(wait)
	if got := repliesTo(f.store, b); len(got) != 0 {
		t.Fatalf("replies to B = %+v, want none: the turn result answers A only", got)
	}
	if answerToA := repliesTo(f.store, *f.rev.MessageID); len(answerToA) != 1 || strings.Contains(f.wait(wait).Reason, "turn result") {
		t.Fatalf("reason %q must not name the turn result %v: it went to the user, not to the waiter", f.wait(wait).Reason, answerToA)
	}
}

// The same request, handed at the start of the next turn instead (it was not read in the middle), is answered
// by that turn's result and the wait is satisfied: nothing is cancelled.
func TestLeftUnanswered_UnreadRequestStartsTheNextTurnAndIsAnswered(t *testing.T) {
	f := newLeftFixture(t)
	b, wait := f.ask("b")
	f.finish()
	waitUntil(t, "the second turn answers B", func() bool { return len(repliesTo(f.store, b)) == 1 })
	f.o.concurrency.WaitAgents()

	if w := f.wait(wait); w.Status != agentcomm.WaitReady {
		t.Fatalf("wait for B = %s %q, want ready", w.Status, w.Reason)
	}
}

// Of two requests handed to one turn, the result answers the last one; the sibling nobody asked about is left
// unanswered and its wait ends.
func TestLeftUnanswered_SiblingOfTheWakingMessageOfTheStartInbox(t *testing.T) {
	f := newLeftFixture(t)
	_, waitB := f.ask("b")
	a2 := f.send("a2")
	f.finish()
	waitUntil(t, "the second turn answers A2", func() bool { return len(repliesTo(f.store, a2)) == 1 })
	f.o.concurrency.WaitAgents()

	f.requireCancelled(waitB)
}

// A request read in the middle of the turn and answered explicitly is satisfied; the check does nothing.
func TestLeftUnanswered_ReadMidTurnAndRepliedIsReady(t *testing.T) {
	f := newLeftFixture(t)
	b, wait := f.ask("b")
	f.readInboxMidTurn()
	sendAs(t, f.o, "op-reply-b", f.thread, f.rev.ParticipantID, unreadTestStage, "done B", &b, false)
	f.finish()

	if w := f.wait(wait); w.Status != agentcomm.WaitReady {
		t.Fatalf("wait for B = %s %q, want ready", w.Status, w.Reason)
	}
}

// Two requests read in the middle of a turn, only one answered: the answered one is ready, the other is
// cancelled — never closed by the common result.
func TestLeftUnanswered_OneOfTwoReadRequestsIsNotClosedByTheResult(t *testing.T) {
	f := newLeftFixture(t)
	b2, wait2 := f.ask("b2")
	_, wait3 := f.ask("b3")
	f.readInboxMidTurn()
	sendAs(t, f.o, "op-reply-b2", f.thread, f.rev.ParticipantID, unreadTestStage, "done 2", &b2, false)
	f.finish()
	waitUntil(t, "the unanswered wait ends", func() bool { return f.wait(wait3).Status != agentcomm.WaitPending })

	if w := f.wait(wait2); w.Status != agentcomm.WaitReady {
		t.Fatalf("answered wait = %s %q, want ready", w.Status, w.Reason)
	}
	f.requireCancelled(wait3)
}

// An agent that reads the request and yields keeps the wait: it may still answer. The wait ends when a later
// turn goes idle without answering.
func TestLeftUnanswered_YieldKeepsTheWaitUntilTheAgentIsIdle(t *testing.T) {
	f := newLeftFixture(t)
	_, wait := f.ask("b")
	var asked agentcomm.MessageID
	f.turn = func(n int32) executor.HelperOutcome {
		if n > 1 {
			return passResultOutcome()
		}
		asked = sendAs(t, f.o, "op-q", f.rev.ThreadID, f.rev.ParticipantID, meshUserParticipantID, "a question", nil, false)
		if _, err := f.o.Wait(context.Background(), WaitRequest{
			OperationID: "op-q-wait", Waiter: f.rev.ParticipantID,
			Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: asked}, Deadline: testFarDeadline(),
		}); err != nil {
			t.Errorf("Wait: %v", err)
		}
		return executor.HelperOutcome{ProcessOK: true}
	}
	f.readInboxMidTurn()
	close(f.release)
	waitForTaskStatus(t, f.store, f.rev.TaskID, assistants.StatusWaitingPeer, taskWait)
	if w := f.wait(wait); w.Status != agentcomm.WaitPending {
		t.Fatalf("wait while the agent waits itself = %s %q, want pending", w.Status, w.Reason)
	}
	// A new wait on the request is admitted too: an agent that waits itself may still answer.
	b := f.store.Snapshot().Assistants.Mesh.Waits[wait].Condition.MessageID
	other := addTestRunAgent(t, f.o, "op-other", nil)
	if _, err := f.o.Wait(context.Background(), WaitRequest{
		OperationID: "wait-while-parked", Waiter: other.ParticipantID, Deadline: testFarDeadline(),
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: b},
	}); err != nil {
		t.Fatalf("Wait on a request of an agent that waits for a colleague: %v, want admitted", err)
	}

	sendAs(t, f.o, "op-q-answer", f.rev.ThreadID, meshUserParticipantID, f.rev.ParticipantID, "the answer", &asked, false)
	waitUntil(t, "the wait ends once the agent is idle", func() bool { return f.wait(wait).Status != agentcomm.WaitPending })
	f.requireCancelled(wait)
	f.o.concurrency.WaitAgents() // the resumed turn must not still write while the run directory is removed
}

// Admission: while the agent runs, a wait for a request it read is admitted; once it is idle without having
// answered, the same wait is refused with the reason and nothing is written.
func TestLeftUnanswered_AdmissionRefusesAWaitOnAnUnansweredRequest(t *testing.T) {
	f := newLeftFixture(t)
	b := f.send("b")
	f.readInboxMidTurn()
	cond := agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: b}
	if _, err := f.o.Wait(context.Background(), WaitRequest{OperationID: "wait-running", Waiter: unreadTestStage, Condition: cond}); err != nil {
		t.Fatalf("Wait while the agent runs: %v, want admitted", err)
	}
	f.finish()
	waitUntil(t, "the first wait ends", func() bool {
		return f.store.Snapshot().Assistants.Mesh.Waits[deriveMeshWaitID("wait-running")].Status != agentcomm.WaitPending
	})

	before := len(f.store.Snapshot().Assistants.Mesh.Waits)
	_, err := f.o.Wait(context.Background(), WaitRequest{OperationID: "wait-idle", Waiter: meshUserParticipantID, Condition: cond, Deadline: testFarDeadline()})
	var rejected *state.OperationRejectedError
	if !errors.As(err, &rejected) || !strings.Contains(rejected.Reason, leftReason) {
		t.Fatalf("Wait on an unanswered request = %v, want a rejection saying %q", err, leftReason)
	}
	if got := len(f.store.Snapshot().Assistants.Mesh.Waits); got != before {
		t.Fatalf("waits = %d, want %d: the refusal writes nothing", got, before)
	}
}

// An old journal: the agent read the request, finished its turn and the wait for it was registered without any
// admission check. The restart cancels it once; a second restart writes nothing more; live and replayed state agree.
func TestLeftUnanswered_StartupHealsAnOldJournalOnce(t *testing.T) {
	runDir := t.TempDir()
	f := newLeftFixtureAt(t, runDir)
	b := f.send("b")
	f.readInboxMidTurn()
	f.finish()
	legacy := []agentcomm.WaitID{
		registerLegacyWait(t, f.store, "legacy-wait-1", unreadTestStage, b), // a journal from before waits were de-duplicated
		registerLegacyWait(t, f.store, "legacy-wait-2", unreadTestStage, b),
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}

	restart := func() (*Orchestrator, *state.Store) {
		o, store := newUnreadTestOrchestratorAt(t, runDir, flow.AgentAuto, &scriptedStageRunner{turn: func(int32) error { return nil }})
		o.releaseUnanswerableWaits(context.Background(), o.resumeWaiterAfterTerminalWaitAtStartup)
		o.concurrency.WaitAgents()
		return o, store
	}
	_, first := restart()
	for _, id := range legacy {
		if w := first.Snapshot().Assistants.Mesh.Waits[id]; w.Status != agentcomm.WaitCancelled || !strings.Contains(w.Reason, leftReason) {
			t.Fatalf("wait %s after the restart = %s %q, want cancelled: %s", id, w.Status, w.Reason, leftReason)
		}
	}
	healed := journalLines(t, runDir)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	_, second := restart()
	if got := journalLines(t, runDir); got != healed {
		t.Fatalf("journal lines after the second restart = %d, want %d: the heal is written once", got, healed)
	}
	replayed, err := state.LoadRunState(runDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range legacy {
		live, replay := second.Snapshot().Assistants.Mesh.Waits[id], replayed.Assistants.Mesh.Waits[id]
		if live.Status != replay.Status || live.Reason != replay.Reason {
			t.Fatalf("wait %s: live %s %q, replayed %s %q", id, live.Status, live.Reason, replay.Status, replay.Reason)
		}
	}
}

// registerLegacyWait writes waiter's reply_to wait on id with no admission check, as an old journal holds it.
func registerLegacyWait(t *testing.T, store *state.Store, op string, waiter agentcomm.ParticipantID, id agentcomm.MessageID) agentcomm.WaitID {
	t.Helper()
	eff, err := marshalEffect("mesh_wait_registered", state.Target{WaitID: string(deriveMeshWaitID(op))},
		state.MeshWaitRegisteredPayload{Waiter: string(waiter), Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: id}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyRunOperation(state.Operation{ID: op, Kind: "mesh_wait", Actor: state.Actor{Kind: "system"}, Effects: []state.Effect{eff}}, nil); err != nil {
		t.Fatal(err)
	}
	return deriveMeshWaitID(op)
}

func journalLines(t *testing.T, runDir string) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(runDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(raw), "\n")
}

// The stage-level incident: the coordinator stage sends A and a separate request B, waits for B and yields; the
// agent reads B in the middle of its turn and answers A only. The stage must continue and finish, once.
func TestLeftUnanswered_ParkedStageContinuesAfterTheAgentLeavesItsRequest(t *testing.T) {
	runner := &scriptedStageRunner{}
	o, store := newUnreadTestOrchestrator(t, flow.AgentAuto, runner)
	started, release, ready := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var rev AttachAndSendResult
	var turns atomic.Int32
	o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
		awaitRelease(t, ready)
		o.deliverRenderedInbox(rev.ParticipantID)()
		if turns.Add(1) == 1 {
			close(started)
			awaitRelease(t, release)
			readInbox(t, o, rev.ParticipantID)
		}
		return passResultOutcome(), nil
	}
	var err error
	rev, err = o.AddRunAgent(context.Background(), AddRunAgentRequest{OperationID: "op-rev", ProfileID: "qa", Title: "QA", Actor: state.Actor{Kind: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	th := openThread(t, o, "op-th", 0, unreadTestStage, rev.ParticipantID)
	close(ready)

	var wait agentcomm.WaitID
	runner.turn = func(n int32) error {
		if n != 1 {
			return nil
		}
		for _, id := range []string{"a", "b"} {
			sent, err := o.Send(context.Background(), SendRequest{
				OperationID: "send-" + id, ThreadID: th, Sender: unreadTestStage, Recipients: []agentcomm.ParticipantID{rev.ParticipantID},
				Kind: agentcomm.MessageKindRequest, Body: "request " + id, Actor: state.Actor{Kind: "assistant", ID: unreadTestStageID},
			})
			if err != nil {
				return err
			}
			if id == "a" {
				<-started
				continue
			}
			w, err := o.Wait(context.Background(), WaitRequest{
				OperationID: "wait-b", Waiter: unreadTestStage, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: sent.MessageID},
			})
			if err != nil {
				return err
			}
			wait = w.WaitID
		}
		go func() {
			waitUntil(t, "the stage parks", func() bool { return store.Get(unreadTestStageID) == state.StatusAwaitingPeer })
			close(release)
		}()
		return nil
	}

	runToEnd(t, o)

	if got := store.Get(unreadTestStageID); got != state.StatusDone {
		t.Fatalf("stage = %s, want done", got)
	}
	if got := runner.turns.Load(); got != 2 {
		t.Fatalf("stage turns = %d, want the asking turn and exactly one continuation", got)
	}
	if w := store.Snapshot().Assistants.Mesh.Waits[wait]; w.Status != agentcomm.WaitCancelled || !strings.Contains(w.Reason, leftReason) {
		t.Fatalf("wait = %s %q, want cancelled: %s", w.Status, w.Reason, leftReason)
	}
}

// leftState is a run state with one task agent QA in status, which was handed requests B (receipt) and, as its
// turn input, inbox; open are the requests it still owes after an earlier turn that waited.
func leftState(status assistants.TaskStatus, receipt agentcomm.DeliveryStatus, inbox, open []agentcomm.MessageID) (state.RunState, agentcomm.ParticipantID) {
	qa := meshRunParticipantID("t-qa")
	rs := *state.NewRunState([]string{"s"})
	rs.Assistants.Tasks = map[assistants.TaskID]*state.TaskState{
		"t-qa": {Task: assistants.Task{ID: "t-qa", Scope: assistants.ScopeRun, Status: status}, Turn: 1, TurnInbox: inbox, OpenRequests: open},
	}
	rs.Assistants.Mesh.Participants = map[agentcomm.ParticipantID]*agentcomm.Participant{
		qa: {ID: qa, Kind: agentcomm.KindRunAgent, Scope: agentcomm.ScopeRun, DisplayName: "QA", Reachable: true},
	}
	rs.Assistants.Mesh.Messages = map[agentcomm.MessageID]*agentcomm.Message{
		"B": {ID: "B", Sender: unreadTestStage, Recipients: []agentcomm.ParticipantID{qa}, Kind: agentcomm.MessageKindRequest},
	}
	rs.Assistants.Mesh.Receipts = map[agentcomm.MessageID]map[agentcomm.ParticipantID]agentcomm.DeliveryStatus{"B": {qa: receipt}}
	return rs, qa
}

func TestLeftUnanswered_Predicate(t *testing.T) {
	const (
		queued   = assistants.StatusQueued
		delivery = agentcomm.DeliveryDelivered
	)
	cases := []struct {
		name    string
		status  assistants.TaskStatus
		receipt agentcomm.DeliveryStatus
		inbox   []agentcomm.MessageID
		open    []agentcomm.MessageID
		want    bool
	}{
		{"idle, read, not in its input", queued, delivery, nil, nil, true},
		{"idle, acknowledged, not in its input", queued, agentcomm.DeliveryAcknowledged, nil, nil, true},
		{"observer between passes", assistants.StatusWatching, delivery, nil, nil, true},
		{"idle but not read yet", queued, agentcomm.DeliveryAccepted, nil, nil, false},
		{"still running", assistants.StatusRunning, delivery, nil, nil, false},
		{"waiting for a colleague", assistants.StatusWaitingPeer, delivery, nil, nil, false},
		{"waiting for children", assistants.StatusWaitingChildren, delivery, nil, nil, false},
		{"input kept for an interrupted turn", queued, delivery, []agentcomm.MessageID{"B"}, nil, false},
		{"still owed by an earlier turn that yielded", queued, delivery, nil, []agentcomm.MessageID{"B"}, false},
		{"finished tasks are decided by their end", assistants.StatusCompleted, delivery, nil, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rs, qa := leftState(tc.status, tc.receipt, tc.inbox, tc.open)
			if got := leftUnanswered(rs, "B", qa); got != tc.want {
				t.Fatalf("leftUnanswered = %v, want %v", got, tc.want)
			}
		})
	}
	t.Run("answered by the agent", func(t *testing.T) {
		rs, qa := leftState(queued, delivery, nil, nil)
		reply := agentcomm.MessageID("B")
		rs.Assistants.Mesh.Messages["R"] = &agentcomm.Message{ID: "R", Sender: qa, Kind: agentcomm.MessageKindReply, InReplyTo: &reply}
		if leftUnanswered(rs, "B", qa) {
			t.Fatal("a request the agent answered is not left unanswered")
		}
	})
	t.Run("a journal from before turns were recorded", func(t *testing.T) {
		// Its interrupted turn's input lives only in turn-inbox.json and the retry hands it again.
		rs, qa := leftState(queued, delivery, nil, nil)
		rs.Assistants.Tasks["t-qa"].Turn = 0
		if leftUnanswered(rs, "B", qa) {
			t.Fatal("a request of a turn whose input was not recorded is handed again, not left")
		}
		rs.Assistants.Mesh.Waits = map[agentcomm.WaitID]*state.MeshWait{"mw": {Wait: agentcomm.Wait{
			ID: "mw", Waiter: unreadTestStage, Status: agentcomm.WaitPending, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: "B"},
		}}}
		if reason, ok := unanswerableWait(rs, "mw"); ok {
			t.Fatalf("startup would cancel the wait (%q) of a request the retry hands again", reason)
		}
	})
	t.Run("not a task agent", func(t *testing.T) {
		rs, _ := leftState(queued, delivery, nil, nil)
		if leftUnanswered(rs, "B", unreadTestStage) {
			t.Fatal("only task agents are decided by their task")
		}
	})
}

// A request with several recipients is not given up while one of them can still answer.
func TestLeftUnanswered_WaitStaysWhileAnotherRecipientCanAnswer(t *testing.T) {
	rs, qa := leftState(assistants.StatusQueued, agentcomm.DeliveryDelivered, nil, nil)
	other := meshRunParticipantID("t-other")
	rs.Assistants.Tasks["t-other"] = &state.TaskState{Task: assistants.Task{ID: "t-other", Scope: assistants.ScopeRun, Status: assistants.StatusRunning}, Turn: 1}
	rs.Assistants.Mesh.Participants[other] = &agentcomm.Participant{ID: other, Kind: agentcomm.KindRunAgent, Scope: agentcomm.ScopeRun, DisplayName: "Other", Reachable: true}
	rs.Assistants.Mesh.Messages["B"].Recipients = []agentcomm.ParticipantID{qa, other}
	rs.Assistants.Mesh.Receipts["B"][other] = agentcomm.DeliveryAccepted
	rs.Assistants.Mesh.Waits = map[agentcomm.WaitID]*state.MeshWait{"mw": {Wait: agentcomm.Wait{
		ID: "mw", Waiter: unreadTestStage, Status: agentcomm.WaitPending, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: "B"},
	}}}
	if reason, ok := unanswerableWait(rs, "mw"); ok {
		t.Fatalf("wait is unanswerable (%q) while Other can still answer", reason)
	}

	rs.Assistants.Tasks["t-other"].Status = assistants.StatusQueued
	rs.Assistants.Mesh.Receipts["B"][other] = agentcomm.DeliveryDelivered
	reason, ok := unanswerableWait(rs, "mw")
	if !ok || !strings.Contains(reason, "QA") || !strings.Contains(reason, "Other") || !strings.Contains(reason, leftReason) {
		t.Fatalf("unanswerableWait = %q, %v; want both recipients named as having read it without replying", reason, ok)
	}
}

// A thread-less addressed_message wait is not a wait for an answer to a request: an idle agent does not end it.
func TestLeftUnanswered_AddressedMessageWaitStaysPending(t *testing.T) {
	rs, _ := leftState(assistants.StatusQueued, agentcomm.DeliveryDelivered, nil, nil)
	rs.Assistants.Mesh.Waits = map[agentcomm.WaitID]*state.MeshWait{"mw": {Wait: agentcomm.Wait{
		ID: "mw", Waiter: unreadTestStage, Status: agentcomm.WaitPending, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage},
	}}}
	if reason, ok := unanswerableWait(rs, "mw"); ok {
		t.Fatalf("addressed_message wait ended (%q) by an idle agent", reason)
	}
}
