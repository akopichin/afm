package state

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
)

// The fixture: stage s runs; its on_message helper t1 shares thread th-1 with the stage's agent and the human,
// and thread th-2 with the stage's agent alone.
const (
	omStage  = "s"
	omTask   = "t1"
	omHelper = "helper:t1"
	omAgent  = "stage:s"
	omUser   = "user"
)

// omSetup is what attaches the helper: the task, resting in watching, and its conversations.
func omSetup(t *testing.T, mode assistants.Mode) []Effect {
	t.Helper()
	created := TaskCreatedPayload{
		ProfileID: "p", Mode: string(mode), Trigger: string(assistants.TriggerOnMessage), Scope: string(assistants.ScopeStage),
	}
	return []Effect{
		{Kind: kindTaskCreated, Target: Target{TaskID: omTask, StageID: omStage}, Payload: mustMarshal(t, created)},
		omStatus(t, TaskStatusChangedPayload{Status: string(assistants.StatusWatching)}),
		meshEffectOf(meshParticipantAddedControl(omUser, string(agentcomm.KindUser), string(agentcomm.ScopeRun))),
		meshEffectOf(meshParticipantAddedControl(omAgent, string(agentcomm.KindStageAgent), string(agentcomm.ScopeStage))),
		meshEffectOf(meshParticipantAddedControl(omHelper, string(agentcomm.KindHelper), string(agentcomm.ScopeStage))),
		meshEffectOf(meshThreadOpenedControl("th-1", []string{omUser, omAgent, omHelper})),
		meshEffectOf(meshThreadOpenedControl("th-2", []string{omAgent, omHelper})),
	}
}

func omStatus(t *testing.T, p TaskStatusChangedPayload) Effect {
	t.Helper()
	return Effect{Kind: kindTaskStatusChanged, Target: Target{TaskID: omTask}, Payload: mustMarshal(t, p)}
}

func omTurnStart(t *testing.T, turn uint64, request string) Effect {
	t.Helper()
	return omStatus(t, TaskStatusChangedPayload{Status: string(assistants.StatusRunning), Turn: turn, RequestID: request})
}

func omTurnEnd(t *testing.T, turn uint64, request string, outcome RequestOutcome) Effect {
	t.Helper()
	return omStatus(t, TaskStatusChangedPayload{
		Status: string(assistants.StatusWatching), Turn: turn, RequestID: request, RequestOutcome: outcome,
	})
}

func omResult(t *testing.T, turn uint64, request string, verdict assistants.Verdict) Effect {
	t.Helper()
	p := TaskResultPayload{Result: assistants.Result{Verdict: verdict, Summary: "done " + request}, RequestID: request, Turn: turn}
	return Effect{Kind: kindTaskResult, Target: Target{TaskID: omTask}, Payload: mustMarshal(t, p)}
}

// omMsg is one message: in th-1 unless thread says otherwise, a reply when replyTo is set.
type omMsg struct {
	id, from, to string
	thread       string
	replyTo      string
	auto         bool
}

func (m omMsg) effect(t *testing.T) Effect {
	t.Helper()
	p := MeshMessageSentPayload{
		Sender: m.from, Recipients: []string{m.to}, Kind: string(agentcomm.MessageKindMessage), Body: m.id, AutoResult: m.auto,
	}
	if m.replyTo != "" {
		p.Kind, p.InReplyTo = string(agentcomm.MessageKindReply), &m.replyTo
	}
	thread := m.thread
	if thread == "" {
		thread = "th-1"
	}
	return Effect{Kind: kindMeshMessageSent, Target: Target{MessageID: m.id, ThreadID: thread}, Payload: mustMarshal(t, p)}
}

// omQueue renders the helper's request queue for comparison.
func omQueue(ts *TaskState) string {
	return fmt.Sprintf("pending=%v active=%s last=%s outcome=%s", ts.PendingRequests, ts.ActiveRequest, ts.LastRequest, ts.LastOutcome)
}

// onMessageRun is the fixture as a live store: operations commit through ApplyRunOperation.
type onMessageRun struct {
	t     *testing.T
	dir   string
	store *Store
}

func newOnMessageRun(t *testing.T, mode assistants.Mode) *onMessageRun {
	t.Helper()
	dir := t.TempDir()
	store, err := Open(dir, []string{omStage})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	mustRecordPolicy(t, store)
	r := &onMessageRun{t: t, dir: dir, store: store}
	r.moveStage(StatusPending, StatusRunning)
	r.mustOp("attach", omSetup(t, mode)...)
	return r
}

func (r *onMessageRun) op(id string, effects ...Effect) error {
	_, err := r.store.ApplyRunOperation(Operation{ID: id, Kind: "test", Effects: effects}, nil)
	return err
}

func (r *onMessageRun) mustOp(id string, effects ...Effect) {
	r.t.Helper()
	if err := r.op(id, effects...); err != nil {
		r.t.Fatalf("%s: %v", id, err)
	}
}

func (r *onMessageRun) moveStage(from, to StageStatus) {
	r.t.Helper()
	if err := r.store.Apply(&Transition{StageID: omStage, From: from, To: to, Event: "test"}); err != nil {
		r.t.Fatalf("stage %s -> %s: %v", from, to, err)
	}
}

func (r *onMessageRun) task() *TaskState { return r.store.Snapshot().Assistants.Tasks[omTask] }

func (r *onMessageRun) requireQueue(why, want string) {
	r.t.Helper()
	if got := omQueue(r.task()); got != want {
		r.t.Fatalf("%s: queue = %q, want %q", why, got, want)
	}
}

// replay closes the store and rebuilds the run from its journal, as a read-only reader does.
func (r *onMessageRun) replay() RunState {
	r.t.Helper()
	if err := r.store.Close(); err != nil {
		r.t.Fatal(err)
	}
	rs, err := LoadRunState(r.dir)
	if err != nil {
		r.t.Fatalf("LoadRunState: %v", err)
	}
	return rs
}

// requireReplayedQueue checks that the journal replays to the queue the live store holds.
func requireReplayedQueue(t *testing.T, live, replayed *TaskState) {
	t.Helper()
	if omQueue(replayed) != omQueue(live) || replayed.ActiveSinceSeq != live.ActiveSinceSeq {
		t.Fatalf("replayed queue %q since %d, live %q since %d",
			omQueue(replayed), replayed.ActiveSinceSeq, omQueue(live), live.ActiveSinceSeq)
	}
}

// newOnMessageRunState is the fixture as a bare projection, for pure apply.
func newOnMessageRunState(t *testing.T, mode assistants.Mode) *RunState {
	t.Helper()
	rs := NewRunState([]string{omStage})
	rs.SetStageStatus(omStage, StatusRunning)
	for _, e := range omSetup(t, mode) {
		omDispatch(t, rs, 1, e)
	}
	return rs
}

func omDispatch(t *testing.T, rs *RunState, seq uint64, e Effect) {
	t.Helper()
	c := &Control{Kind: e.Kind, SchemaVersion: ControlSchemaVersion, Seq: seq, Target: e.Target, Payload: e.Payload}
	if err := dispatchControlKind(rs, c, false); err != nil {
		t.Fatalf("dispatch %s: %v", e.Kind, err)
	}
}

func TestOnMessagePredicates(t *testing.T) {
	parent := assistants.TaskID("parent")
	helper := assistants.Task{Scope: assistants.ScopeStage, Trigger: assistants.TriggerOnMessage, Status: assistants.StatusWatching}
	child := helper
	child.ParentTaskID = &parent
	runAgent := helper
	runAgent.Scope = assistants.ScopeRun
	oneShot := helper
	oneShot.Trigger = assistants.TriggerOneShot
	for _, tc := range []struct {
		name string
		task assistants.Task
		want bool
	}{
		{"stage top-level on_message", helper, true},
		{"delegated child", child, false},
		{"run agent", runAgent, false},
		{"one_shot helper", oneShot, false},
	} {
		if got := IsOnMessageHelper(tc.task); got != tc.want {
			t.Errorf("IsOnMessageHelper(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}

	cancelled := helper
	cancelled.Status = assistants.StatusCancelled
	for _, tc := range []struct {
		name string
		task TaskState
		want bool
	}{
		{"waiting with nothing to do", TaskState{Task: helper}, true},
		{"a request waits", TaskState{Task: helper, PendingRequests: []agentcomm.MessageID{"m1"}}, false},
		{"a request is worked on", TaskState{Task: helper, ActiveRequest: "m1"}, false},
		{"closed", TaskState{Task: cancelled}, false},
		{"not on_message", TaskState{Task: oneShot}, false},
	} {
		if got := OnMessageIdle(tc.task); got != tc.want {
			t.Errorf("OnMessageIdle(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A message is a continuation of the active request only when it answers the helper's own message sent during
// that request; everything else addressed to the helper is a request, queued in arrival order. The journal
// replays to the same queue.
func TestOnMessageQueue_ClassifiesMessagesLiveAndOnReplay(t *testing.T) {
	r := newOnMessageRun(t, assistants.ModeAdvisory)
	r.mustOp("send-m1", omMsg{id: "m1", from: omAgent, to: omHelper}.effect(t))
	r.mustOp("send-m2", omMsg{id: "m2", from: omUser, to: omHelper}.effect(t))
	r.requireQueue("requests wait in arrival order", "pending=[m1 m2] active= last= outcome=")

	r.mustOp("start-1", omTurnStart(t, 1, "m1"))
	r.requireQueue("the turn takes the oldest request", "pending=[m2] active=m1 last= outcome=")
	activeSince := r.task().ActiveSinceSeq
	if activeSince == 0 {
		t.Fatal("ActiveSinceSeq not set by the turn start")
	}

	r.mustOp("ask", omMsg{id: "q1", from: omHelper, to: omAgent}.effect(t))
	r.mustOp("answer", omMsg{id: "a1", from: omAgent, to: omHelper, replyTo: "q1"}.effect(t))
	r.mustOp("passive", omMsg{id: "p1", from: omAgent, to: omHelper, auto: true}.effect(t))
	r.requireQueue("an answer to the helper's question and a passive result are no requests", "pending=[m2] active=m1 last= outcome=")

	r.mustOp("yield-1", omStatus(t, TaskStatusChangedPayload{Status: string(assistants.StatusWaitingPeer), Turn: 1}))
	r.mustOp("resume-2", omTurnStart(t, 2, "m1"))
	r.requireQueue("a resumed turn keeps the request", "pending=[m2] active=m1 last= outcome=")
	if got := r.task().ActiveSinceSeq; got != activeSince {
		t.Fatalf("ActiveSinceSeq after resume = %d, want %d", got, activeSince)
	}

	r.mustOp("finish-m1", omTurnEnd(t, 2, "m1", RequestOutcomeResult), omResult(t, 2, "m1", ""))
	r.requireQueue("the outcome ends the request", "pending=[m2] active= last=m1 outcome=result")

	r.mustOp("auto-reply", omMsg{id: "r1", from: omHelper, to: omAgent, replyTo: "m1", auto: true}.effect(t))
	r.mustOp("follow-up", omMsg{id: "f1", from: omAgent, to: omHelper, replyTo: "r1"}.effect(t))
	r.requireQueue("a follow-up to the passive answer is a new request", "pending=[m2 f1] active= last=m1 outcome=result")

	r.mustOp("start-3", omTurnStart(t, 3, "m2"))
	r.mustOp("late", omMsg{id: "a2", from: omAgent, to: omHelper, replyTo: "q1"}.effect(t))
	r.requireQueue("a late answer to the previous request's question is a new request", "pending=[f1 a2] active=m2 last=m1 outcome=result")

	live := r.task()
	requireReplayedQueue(t, live, r.replay().Assistants.Tasks[omTask])
}

// A message that resolves the helper's own wait serves the request that wait belongs to.
func TestOnMessageQueue_MessageResolvingTheHelpersWaitContinuesItsRequest(t *testing.T) {
	r := newOnMessageRun(t, assistants.ModeAdvisory)
	r.mustOp("send-m1", omMsg{id: "m1", from: omAgent, to: omHelper}.effect(t))
	r.mustOp("start-1", omTurnStart(t, 1, "m1"))
	r.mustOp("wait", meshEffectOf(meshWaitRegisteredControl("w1", omHelper,
		agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: "th-2"}, omTask, "", 0)))
	r.mustOp("yield-1", omStatus(t, TaskStatusChangedPayload{Status: string(assistants.StatusWaitingPeer), Turn: 1}))

	r.mustOp("elsewhere", omMsg{id: "x1", from: omUser, to: omHelper}.effect(t))
	r.mustOp("awaited", omMsg{id: "x2", thread: "th-2", from: omAgent, to: omHelper}.effect(t))
	r.requireQueue("only the message the helper waited for continues m1", "pending=[x1] active=m1 last= outcome=")
	if w := r.store.Snapshot().Assistants.Mesh.Waits["w1"]; w.Status != agentcomm.WaitReady {
		t.Fatalf("wait w1 = %s, want ready", w.Status)
	}

	live := r.task()
	replayed := r.replay()
	requireReplayedQueue(t, live, replayed.Assistants.Tasks[omTask])
	if w := replayed.Assistants.Mesh.Waits["w1"]; w.Status != agentcomm.WaitReady {
		t.Fatalf("replayed wait w1 = %s, want ready", w.Status)
	}
}

// A full queue of new requests never blocks what continues the active request — the answer to the helper's
// question or the message its wait is for — or the helper could not finish and work the queue off.
func TestOnMessageQueue_FullBacklogStillTakesTheActiveRequestsContinuation(t *testing.T) {
	for _, tc := range []struct {
		name string
		// prepare makes the helper, working on m1 in th-1, wait for a continuation; continuation then sends it.
		prepare, continuation func(r *onMessageRun)
	}{
		{"answer to the helper's question",
			func(r *onMessageRun) { r.mustOp("ask", omMsg{id: "q1", from: omHelper, to: omAgent}.effect(r.t)) },
			func(r *onMessageRun) {
				r.mustOp("answer", omMsg{id: "a1", from: omAgent, to: omHelper, replyTo: "q1"}.effect(r.t))
			}},
		{"message the helper waits for",
			func(r *onMessageRun) {
				r.mustOp("open-th-3", meshEffectOf(meshThreadOpenedControl("th-3", []string{omAgent, omHelper})))
				r.mustOp("wait", meshEffectOf(meshWaitRegisteredControl("w1", omHelper,
					agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: "th-3"}, omTask, "", 0)))
			},
			func(r *onMessageRun) {
				r.mustOp("awaited", omMsg{id: "a1", thread: "th-3", from: omAgent, to: omHelper}.effect(r.t))
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newOnMessageRun(t, assistants.ModeAdvisory)
			r.mustOp("send-m1", omMsg{id: "m1", from: omAgent, to: omHelper}.effect(t))
			r.mustOp("start-1", omTurnStart(t, 1, "m1"))
			r.mustOp("deliver-m1", meshEffectOf(meshMessageReceiptControl(kindMeshMessageDelivered, "m1", omHelper)))
			tc.prepare(r)
			// th-2 takes exactly a full backlog of requests the helper has not been handed yet.
			for i := range agentcomm.MaxInboxUnread {
				id := fmt.Sprintf("r%d", i)
				r.mustOp("send-"+id, omMsg{id: id, thread: "th-2", from: omAgent, to: omHelper}.effect(t))
			}

			tc.continuation(r)
			if got := r.task(); got.ActiveRequest != "m1" || len(got.PendingRequests) != agentcomm.MaxInboxUnread {
				t.Fatalf("after the continuation: %s with %d waiting, want m1 active and %d waiting",
					omQueue(got), len(got.PendingRequests), agentcomm.MaxInboxUnread)
			}
			requireRejected(t, r.op("send-over", omMsg{id: "r-over", from: omAgent, to: omHelper}.effect(t)), assistants.CodeBudgetExceeded)
			if _, recorded := r.store.Snapshot().Assistants.Mesh.Messages["r-over"]; recorded {
				t.Fatal("the refused request was recorded")
			}

			live := r.task()
			requireReplayedQueue(t, live, r.replay().Assistants.Tasks[omTask])
		})
	}
}

func TestOnMessageQueue_RefusesNewRequestsOnceClosed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		close func(r *onMessageRun)
	}{
		{"stage done", func(r *onMessageRun) { r.moveStage(StatusRunning, StatusDone) }},
		{"stage failed", func(r *onMessageRun) { r.moveStage(StatusRunning, StatusFailed) }},
		{"superseded attempt", func(r *onMessageRun) {
			r.mustOp("revise", Effect{Kind: kindRootGenerationBumped, Target: Target{StageID: omStage}})
		}},
		{"helper cancelled", func(r *onMessageRun) {
			r.mustOp("cancel", omStatus(r.t, TaskStatusChangedPayload{Status: string(assistants.StatusCancelled)}))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newOnMessageRun(t, assistants.ModeAdvisory)
			tc.close(r)
			requireRejected(t, r.op("late-request", omMsg{id: "m9", from: omAgent, to: omHelper}.effect(t)), assistants.CodeInvalidStatus)
			if _, recorded := r.store.Snapshot().Assistants.Mesh.Messages["m9"]; recorded {
				t.Fatal("the refused request was recorded")
			}
		})
	}
}

// Work accepted before the stage ended goes on: its answers and passive results are still delivered.
func TestOnMessageQueue_AcceptedRequestKeepsItsMessagesAfterTheStageEnds(t *testing.T) {
	r := newOnMessageRun(t, assistants.ModeAdvisory)
	r.mustOp("send-m1", omMsg{id: "m1", from: omAgent, to: omHelper}.effect(t))
	r.mustOp("start-1", omTurnStart(t, 1, "m1"))
	r.mustOp("ask", omMsg{id: "q1", from: omHelper, to: omAgent}.effect(t))
	r.moveStage(StatusRunning, StatusDone)

	r.mustOp("answer", omMsg{id: "a1", from: omAgent, to: omHelper, replyTo: "q1"}.effect(t))
	r.mustOp("passive", omMsg{id: "p1", from: omAgent, to: omHelper, auto: true}.effect(t))
	r.requireQueue("the stage's end closes only new requests", "pending=[] active=m1 last= outcome=")
	requireRejected(t, r.op("new", omMsg{id: "m2", from: omAgent, to: omHelper}.effect(t)), assistants.CodeInvalidStatus)
}

func TestOnMessageQueue_RetriedStageTakesRequestsAgain(t *testing.T) {
	r := newOnMessageRun(t, assistants.ModeAdvisory)
	r.moveStage(StatusRunning, StatusFailed)
	requireRejected(t, r.op("refused", omMsg{id: "m1", from: omAgent, to: omHelper}.effect(t)), assistants.CodeInvalidStatus)
	r.moveStage(StatusFailed, StatusRunning)
	r.mustOp("accepted", omMsg{id: "m2", from: omAgent, to: omHelper}.effect(t))
	r.requireQueue("a retried stage reopens the queue", "pending=[m2] active= last= outcome=")
}

// Requests the helper has already read still count against its inbox limit, so the backlog stays bounded.
func TestOnMessageQueue_BacklogCountsRequestsAlreadyRead(t *testing.T) {
	rs := newOnMessageRunState(t, assistants.ModeAdvisory)
	for i := range agentcomm.MaxInboxUnread {
		thread := "th-1"
		if i >= agentcomm.MaxInboxUnread/2 {
			thread = "th-2"
		}
		id := fmt.Sprintf("m%d", i)
		omDispatch(t, rs, uint64(10+i), omMsg{id: id, thread: thread, from: omAgent, to: omHelper}.effect(t))
		omDispatch(t, rs, uint64(10+i), meshEffectOf(meshMessageReceiptControl(kindMeshMessageDelivered, id, omHelper)))
	}
	over := omMsg{id: "m-over", thread: "th-2", from: omAgent, to: omHelper}.effect(t)
	c := &Control{Kind: over.Kind, SchemaVersion: ControlSchemaVersion, Seq: 999, Target: over.Target, Payload: over.Payload}
	requireRejected(t, dispatchControlKind(rs, c, true), assistants.CodeBudgetExceeded)
	if got := len(rs.Assistants.Tasks[omTask].PendingRequests); got != agentcomm.MaxInboxUnread {
		t.Fatalf("pending after the refusal = %d, want %d", got, agentcomm.MaxInboxUnread)
	}
	// The limit binds new records only: a journal that holds the message replays.
	if err := dispatchControlKind(rs, c, false); err != nil {
		t.Fatalf("replay of a journaled request: %v", err)
	}
}

// The check made before planning a message refuses what the commit would: for an on_message helper a full request
// queue, read requests included, or a closed stage; for anyone else a full inbox.
func TestCheckNewMessageRoom_FollowsTheRequestQueue(t *testing.T) {
	rs := newOnMessageRunState(t, assistants.ModeAdvisory)
	if err := rs.CheckNewMessageRoom(omHelper); err != nil {
		t.Fatalf("room for a first request: %v", err)
	}
	for i := range agentcomm.MaxInboxUnread {
		thread := "th-1"
		if i >= agentcomm.MaxInboxUnread/2 {
			thread = "th-2"
		}
		id := fmt.Sprintf("m%d", i)
		omDispatch(t, rs, uint64(10+i), omMsg{id: id, thread: thread, from: omAgent, to: omHelper}.effect(t))
		omDispatch(t, rs, uint64(10+i), meshEffectOf(meshMessageReceiptControl(kindMeshMessageDelivered, id, omHelper)))
	}
	if err := rs.Assistants.Mesh.CheckInboxCapacity([]agentcomm.ParticipantID{omHelper}, false); err != nil {
		t.Fatalf("setup: the inbox check alone must see room, every request being read: %v", err)
	}
	requireRejected(t, rs.CheckNewMessageRoom(omHelper), assistants.CodeBudgetExceeded)
	if err := rs.CheckNewMessageRoom(omAgent); err != nil {
		t.Fatalf("room in the stage's inbox: %v", err)
	}

	closed := newOnMessageRunState(t, assistants.ModeAdvisory)
	closed.SetStageStatus(omStage, StatusDone)
	requireRejected(t, closed.CheckNewMessageRoom(omHelper), assistants.CodeInvalidStatus)
}

// Continuations have a room of their own: bounded like any inbox, and never taking the room of new requests.
func TestOnMessageQueue_ContinuationsHaveTheirOwnRoom(t *testing.T) {
	rs := newOnMessageRunState(t, assistants.ModeAdvisory)
	seq := uint64(10)
	dispatch := func(e Effect) {
		t.Helper()
		omDispatch(t, rs, seq, e)
		seq++
	}
	admit := func(m omMsg) error {
		e := m.effect(t)
		return dispatchControlKind(rs, &Control{Kind: e.Kind, SchemaVersion: ControlSchemaVersion, Seq: seq, Target: e.Target, Payload: e.Payload}, true)
	}
	dispatch(omMsg{id: "m1", from: omAgent, to: omHelper}.effect(t))
	dispatch(omTurnStart(t, 1, "m1"))
	// One question per thread: a thread holds fewer messages than a full room of answers.
	dispatch(omMsg{id: "q1", from: omHelper, to: omAgent}.effect(t))
	dispatch(omMsg{id: "q2", thread: "th-2", from: omHelper, to: omAgent}.effect(t))
	for i := range agentcomm.MaxInboxUnread {
		question, thread := "q1", "th-1"
		if i%2 == 1 {
			question, thread = "q2", "th-2"
		}
		dispatch(omMsg{id: fmt.Sprintf("a%d", i), thread: thread, from: omAgent, to: omHelper, replyTo: question}.effect(t))
	}

	requireRejected(t, admit(omMsg{id: "a-over", from: omAgent, to: omHelper, replyTo: "q1"}), assistants.CodeBudgetExceeded)
	if err := admit(omMsg{id: "m2", from: omAgent, to: omHelper}); err != nil {
		t.Fatalf("a new request behind a full room of answers: %v", err)
	}
}

// A cancelled helper has nothing left to do: its queue drops, and the newest request it accepted fails with it.
func TestOnMessageQueue_TerminalStatusDropsTheQueue(t *testing.T) {
	r := newOnMessageRun(t, assistants.ModeAdvisory)
	for _, id := range []string{"m1", "m2", "m3"} {
		r.mustOp("send-"+id, omMsg{id: id, from: omAgent, to: omHelper}.effect(t))
	}
	r.mustOp("start-1", omTurnStart(t, 1, "m1"))
	r.mustOp("finish-1", omTurnEnd(t, 1, "m1", RequestOutcomeFailed))
	r.mustOp("start-2", omTurnStart(t, 2, "m2"))
	r.mustOp("cancel", omStatus(t, TaskStatusChangedPayload{Status: string(assistants.StatusCancelled)}))
	r.requireQueue("a closed helper has nothing left to do; its newest request failed with it", "pending=[] active= last=m3 outcome=failed")
	if got := r.task().ActiveSinceSeq; got != 0 {
		t.Fatalf("ActiveSinceSeq = %d after the queue was dropped", got)
	}
}

func TestOnMessageQueue_RepeatedSendQueuesOnce(t *testing.T) {
	r := newOnMessageRun(t, assistants.ModeAdvisory)
	send := omMsg{id: "m1", from: omAgent, to: omHelper}.effect(t)
	r.mustOp("send-m1", send)
	r.mustOp("send-m1", send)
	r.requireQueue("an idempotent repeat adds nothing", "pending=[m1] active= last= outcome=")
}

// A yielded turn keeps its request active; it is answered by its own outcome, never carried as an open request.
func TestOnMessageQueue_YieldDoesNotCarryQueuedRequests(t *testing.T) {
	r := newOnMessageRun(t, assistants.ModeAdvisory)
	r.mustOp("send-m1", omMsg{id: "m1", from: omAgent, to: omHelper}.effect(t))
	r.mustOp("send-m2", omMsg{id: "m2", from: omAgent, to: omHelper}.effect(t))
	r.mustOp("start-1", omStatus(t, TaskStatusChangedPayload{
		Status: string(assistants.StatusRunning), Turn: 1, RequestID: "m1", TurnInbox: []string{"m1", "m2"},
	}))
	r.mustOp("yield-1", omStatus(t, TaskStatusChangedPayload{Status: string(assistants.StatusWaitingPeer), Turn: 1}))
	if got := r.task().OpenRequests; len(got) != 0 {
		t.Fatalf("OpenRequests = %v, want none: queued requests are answered by their outcome", got)
	}
}

// Only an on_message helper with nothing to do lets the run finish; accepted work holds it, queued included.
func TestRunBlocker_IdleOnMessageHelperDoesNotHoldTheRun(t *testing.T) {
	rs := newOnMessageRunState(t, assistants.ModeAdvisory)
	requireRunBlocker := func(why string, want bool) {
		t.Helper()
		if _, got := rs.Assistants.RunBlocker(); got != want {
			t.Fatalf("%s: RunBlocker = %v, want %v", why, got, want)
		}
	}
	requireRunBlocker("waiting for a message", false)
	omDispatch(t, rs, 10, omMsg{id: "m1", from: omAgent, to: omHelper}.effect(t))
	requireRunBlocker("a request waits", true)
	omDispatch(t, rs, 11, omStatus(t, TaskStatusChangedPayload{Status: string(assistants.StatusQueued)}))
	requireRunBlocker("woken for the request", true)
	omDispatch(t, rs, 12, omTurnStart(t, 1, "m1"))
	omDispatch(t, rs, 13, omStatus(t, TaskStatusChangedPayload{Status: string(assistants.StatusWaitingPeer), Turn: 1}))
	requireRunBlocker("the request waits for a colleague", true)
	omDispatch(t, rs, 14, omTurnStart(t, 2, "m1"))
	omDispatch(t, rs, 15, omTurnEnd(t, 2, "m1", RequestOutcomeResult))
	requireRunBlocker("back to waiting for a message", false)
}

// An idle required on_message helper needs a person — a new request or a decision — unless its last request
// ended with a pass, which the system closes on its own; while a request is open the helper is machine work.
func TestNeedsHumanDecision_IdleRequiredOnMessageHelper(t *testing.T) {
	rs := newOnMessageRunState(t, assistants.ModeRequired)
	seq := uint64(10)
	step := func(e ...Effect) {
		t.Helper()
		for _, one := range e {
			omDispatch(t, rs, seq, one)
		}
		seq++
	}
	requireDecision := func(why string, want bool) {
		t.Helper()
		if got := rs.Assistants.NeedsHumanDecision(omStage); got != want {
			t.Fatalf("%s: NeedsHumanDecision = %v, want %v", why, got, want)
		}
	}
	requireDecision("no request yet", true)
	step(omMsg{id: "m1", from: omAgent, to: omHelper}.effect(t))
	requireDecision("a request waits", false)
	step(omTurnStart(t, 1, "m1"))
	step(omTurnEnd(t, 1, "m1", RequestOutcomeResult), omResult(t, 1, "m1", assistants.VerdictPass))
	requireDecision("the last request passed", false)

	step(omMsg{id: "m2", from: omAgent, to: omHelper}.effect(t))
	requireDecision("a new request is open", false)
	step(omTurnStart(t, 2, "m2"))
	step(omTurnEnd(t, 2, "m2", RequestOutcomeFailed))
	requireDecision("the new request failed: the old pass does not count", true)

	step(omMsg{id: "m3", from: omAgent, to: omHelper}.effect(t))
	step(omTurnStart(t, 3, "m3"))
	step(omTurnEnd(t, 3, "m3", RequestOutcomeResult), omResult(t, 3, "m3", assistants.VerdictNeedsChanges))
	requireDecision("the last request needs changes", true)
}

// The status records drive the queue only in its own order: a turn takes the active request or, when none is,
// the oldest waiting one, and an outcome ends the active request.
func TestOnMessageQueue_StatusRecordsFollowTheQueue(t *testing.T) {
	r := newOnMessageRun(t, assistants.ModeAdvisory)
	r.mustOp("send-m1", omMsg{id: "m1", from: omAgent, to: omHelper}.effect(t))
	r.mustOp("send-m2", omMsg{id: "m2", from: omAgent, to: omHelper}.effect(t))
	for _, tc := range []struct {
		name   string
		effect Effect
	}{
		{"a turn skips the oldest request", omTurnStart(t, 1, "m2")},
		{"a turn takes an unknown request", omTurnStart(t, 1, "m9")},
		{"a turn names no request", omTurnStart(t, 1, "")},
		{"an outcome for a request not worked on", omTurnEnd(t, 1, "m1", RequestOutcomeResult)},
	} {
		if err := r.op("bad-"+tc.name, tc.effect); err == nil {
			t.Errorf("%s: committed", tc.name)
		}
	}
	r.mustOp("start-1", omTurnStart(t, 1, "m1"))
	for _, tc := range []struct {
		name   string
		effect Effect
	}{
		{"a turn takes another request while one is active", omTurnStart(t, 2, "m2")},
		{"a resumed turn names no request", omTurnStart(t, 2, "")},
		{"an outcome for another request", omTurnEnd(t, 1, "m2", RequestOutcomeResult)},
		{"an outcome without its request", omTurnEnd(t, 1, "", RequestOutcomeResult)},
		{"an unknown outcome", omTurnEnd(t, 1, "m1", "maybe")},
	} {
		if err := r.op("bad-"+tc.name, tc.effect); err == nil {
			t.Errorf("%s: committed", tc.name)
		}
	}
	r.requireQueue("refused records change nothing", "pending=[m2] active=m1 last= outcome=")
	if got := r.task(); got.Turn != 1 || got.Status != assistants.StatusRunning {
		t.Fatalf("refused records changed the turn: %s turn %d", got.Status, got.Turn)
	}
}

// A failed helper stopped before it could work on its requests, so it keeps them: its Retry takes them up where
// they were, in order, and an earlier pass no longer speaks for a required helper while they are open. Live and
// after replay alike; the pass stays history.
func TestOnMessageQueue_FailedHelperKeepsItsRequestsForTheRetry(t *testing.T) {
	r := newOnMessageRun(t, assistants.ModeRequired)
	r.mustOp("send-m1", omMsg{id: "m1", from: omAgent, to: omHelper}.effect(t))
	r.mustOp("start-1", omTurnStart(t, 1, "m1"))
	r.mustOp("finish-1", omTurnEnd(t, 1, "m1", RequestOutcomeResult), omResult(t, 1, "m1", assistants.VerdictPass))
	pass := *r.task()
	r.mustOp("send-m2", omMsg{id: "m2", from: omAgent, to: omHelper}.effect(t))
	r.mustOp("send-m3", omMsg{id: "m3", from: omAgent, to: omHelper}.effect(t))
	r.mustOp("start-2", omTurnStart(t, 2, "m2"))
	r.mustOp("fail", omStatus(t, TaskStatusChangedPayload{Status: string(assistants.StatusFailed), Reason: "the process did not start"}))
	r.requireQueue("a failed helper keeps the requests it accepted", "pending=[m3] active=m2 last=m1 outcome=result")
	r.moveStage(StatusRunning, StatusAwaitingAssistants)
	if !r.store.Snapshot().Assistants.NeedsHumanDecision(omStage) {
		t.Fatal("a failed required helper asks for no decision")
	}

	r.mustOp("retry",
		Effect{Kind: kindTaskGenerationBumped, Target: Target{TaskID: omTask}},
		omStatus(t, TaskStatusChangedPayload{Status: string(assistants.StatusWatching), Reason: "retried"}))
	if OnMessageIdle(*r.task()) || r.store.Snapshot().Assistants.NeedsHumanDecision(omStage) {
		t.Fatal("the retried helper rests as if it had no request: the earlier pass would close its gate")
	}
	r.mustOp("start-3", omTurnStart(t, 3, "m2"))
	r.requireQueue("the retry works on the request the failure stopped", "pending=[m3] active=m2 last=m1 outcome=result")
	live := *r.task()
	if live.LastVerdict != assistants.VerdictPass || live.ResultSeq != pass.ResultSeq {
		t.Fatalf("verdict %q at %d, want the earlier pass at %d kept as history", live.LastVerdict, live.ResultSeq, pass.ResultSeq)
	}

	replayed := r.replay()
	requireReplayedQueue(t, &live, replayed.Assistants.Tasks[omTask])
	if replayed.Assistants.NeedsHumanDecision(omStage) {
		t.Fatal("replayed: the retried helper's open request asks for a decision")
	}
}

// A terminal status with no request left changes no outcome, and other tasks have none to change.
func TestOnMessageQueue_TerminalWithoutRequestsKeepsTheLastOutcome(t *testing.T) {
	r := newOnMessageRun(t, assistants.ModeRequired)
	r.mustOp("send-m1", omMsg{id: "m1", from: omAgent, to: omHelper}.effect(t))
	r.mustOp("start-1", omTurnStart(t, 1, "m1"))
	r.mustOp("finish-1", omTurnEnd(t, 1, "m1", RequestOutcomeResult), omResult(t, 1, "m1", assistants.VerdictPass))
	before := *r.task()
	r.mustOp("close", omStatus(t, TaskStatusChangedPayload{Status: string(assistants.StatusCompleted), Reason: "closed"}))
	r.requireQueue("closing an idle helper", "pending=[] active= last=m1 outcome=result")
	if got := r.task(); got.LastVerdict != before.LastVerdict || got.ResultSeq != before.ResultSeq {
		t.Fatalf("verdict %q at %d, want %q at %d", got.LastVerdict, got.ResultSeq, before.LastVerdict, before.ResultSeq)
	}

	rs := NewRunState([]string{omStage})
	rs.SetStageStatus(omStage, StatusRunning)
	created := TaskCreatedPayload{ProfileID: "p", Mode: string(assistants.ModeAdvisory), Trigger: string(assistants.TriggerOneShot), Scope: string(assistants.ScopeStage)}
	omDispatch(t, rs, 1, Effect{Kind: kindTaskCreated, Target: Target{TaskID: omTask, StageID: omStage}, Payload: mustMarshal(t, created)})
	omDispatch(t, rs, 2, omTurnStart(t, 1, ""))
	omDispatch(t, rs, 3, omStatus(t, TaskStatusChangedPayload{Status: string(assistants.StatusFailed)}))
	if got := omQueue(rs.Assistants.Tasks[omTask]); got != "pending=[] active= last= outcome=" {
		t.Fatalf("one_shot task failed with queue %q, want none", got)
	}
}

// Other tasks start their turns without naming a request, as journals before on_message did.
func TestOnMessageQueue_OtherTasksStartTurnsWithoutARequest(t *testing.T) {
	rs := NewRunState([]string{omStage})
	rs.SetStageStatus(omStage, StatusRunning)
	created := TaskCreatedPayload{ProfileID: "p", Mode: string(assistants.ModeAdvisory), Trigger: string(assistants.TriggerOneShot), Scope: string(assistants.ScopeStage)}
	for _, e := range []Effect{
		{Kind: kindTaskCreated, Target: Target{TaskID: omTask, StageID: omStage}, Payload: mustMarshal(t, created)},
		omTurnStart(t, 1, ""),
	} {
		c := &Control{Kind: e.Kind, SchemaVersion: ControlSchemaVersion, Seq: 1, Target: e.Target, Payload: e.Payload}
		if err := dispatchControlKind(rs, c, true); err != nil {
			t.Fatalf("admit %s: %v", e.Kind, err)
		}
		omDispatch(t, rs, 1, e)
	}
	if got := rs.Assistants.Tasks[omTask]; got.Status != assistants.StatusRunning || got.Turn != 1 {
		t.Fatalf("one_shot task = %s turn %d, want running turn 1", got.Status, got.Turn)
	}
}

// A failed request is the helper's latest outcome, but it erases nothing: the earlier request's committed
// verdict and its result stay in the projection and the journal, live and after replay.
func TestOnMessageQueue_FailedRequestKeepsTheEarlierResult(t *testing.T) {
	r := newOnMessageRun(t, assistants.ModeRequired)
	r.mustOp("send-m1", omMsg{id: "m1", from: omAgent, to: omHelper}.effect(t))
	r.mustOp("start-1", omTurnStart(t, 1, "m1"))
	r.mustOp("finish-1", omTurnEnd(t, 1, "m1", RequestOutcomeResult), omResult(t, 1, "m1", assistants.VerdictPass))
	passSeq := r.task().ResultSeq
	if passSeq == 0 || r.task().LastVerdict != assistants.VerdictPass {
		t.Fatalf("committed pass = %q at %d", r.task().LastVerdict, passSeq)
	}
	requireEarlierPass := func(why string, task *TaskState) {
		t.Helper()
		if task.LastVerdict != assistants.VerdictPass || task.ResultSeq != passSeq {
			t.Fatalf("%s: verdict %q at %d, want the pass at %d", why, task.LastVerdict, task.ResultSeq, passSeq)
		}
	}

	r.mustOp("send-m2", omMsg{id: "m2", from: omAgent, to: omHelper}.effect(t))
	requireEarlierPass("a new request is admitted", r.task())
	r.mustOp("start-2", omTurnStart(t, 2, "m2"))
	requireEarlierPass("the new request starts", r.task())
	r.mustOp("fail-2", omTurnEnd(t, 2, "m2", RequestOutcomeFailed))
	requireEarlierPass("the new request failed", r.task())
	r.requireQueue("the failure is the latest outcome", "pending=[] active= last=m2 outcome=failed")
	if !r.store.Snapshot().Assistants.NeedsHumanDecision(omStage) {
		t.Fatal("a failed request after a pass needs no decision")
	}

	replayed := r.replay()
	got := replayed.Assistants.Tasks[omTask]
	requireEarlierPass("after replay", got)
	if omQueue(got) != "pending=[] active= last=m2 outcome=failed" || !replayed.Assistants.NeedsHumanDecision(omStage) {
		t.Fatalf("after replay: queue %q, NeedsHumanDecision %v", omQueue(got), replayed.Assistants.NeedsHumanDecision(omStage))
	}
}

// The task_result payload is the helper's Result with the server's binding next to it: a reader of the plain
// Result keeps working, and the verdict still reaches the projection.
func TestTaskResultPayload_AddsTheBindingNextToTheResult(t *testing.T) {
	raw := mustMarshal(t, TaskResultPayload{Result: assistants.Result{Verdict: assistants.VerdictPass, Summary: "ok"}, RequestID: "m1", Turn: 2})
	if want := `{"verdict":"pass","summary":"ok","request_id":"m1","turn":2}`; string(raw) != want {
		t.Fatalf("payload = %s, want %s", raw, want)
	}
	var plain assistants.Result
	if err := json.Unmarshal(raw, &plain); err != nil || plain.Verdict != assistants.VerdictPass || plain.Summary != "ok" {
		t.Fatalf("plain Result = %+v, %v", plain, err)
	}

	rs := newOnMessageRunState(t, assistants.ModeAdvisory)
	omDispatch(t, rs, 10, Effect{Kind: kindTaskResult, Target: Target{TaskID: omTask}, Payload: raw})
	if got := rs.Assistants.Tasks[omTask]; got.LastVerdict != assistants.VerdictPass || got.ResultSeq != 10 {
		t.Fatalf("projected verdict %q at %d, want pass at 10", got.LastVerdict, got.ResultSeq)
	}
}
