package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// Board task tests below name their three stage agents A (the coordinator), B and C.
var (
	boardTaskA = meshStageParticipantID(assistantsTestStageID)
	boardTaskB = meshStageParticipantID(boardTaskTestBackend)
	boardTaskC = meshStageParticipantID(boardTaskTestFrontend)
)

// boardTaskMessagesTo returns the board task messages (assignments and notices) sent to recipient, in
// commit order.
func boardTaskMessagesTo(store *state.Store, recipient agentcomm.ParticipantID) []*agentcomm.Message {
	rs := store.Snapshot()
	var out []*agentcomm.Message
	for _, id := range rs.Assistants.Mesh.Inbox[recipient] {
		if m := rs.Assistants.Mesh.Messages[id]; m != nil && strings.HasPrefix(m.Body, "Board task ") {
			out = append(out, m)
		}
	}
	return out
}

// fillInbox sends recipient MaxInboxUnread unread messages from the human.
func fillInbox(t *testing.T, o *Orchestrator, recipient agentcomm.ParticipantID) {
	t.Helper()
	ctx := context.Background()
	th, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-fill-thread-" + string(recipient), Scope: agentcomm.ScopeRun,
		Members: []agentcomm.ParticipantID{meshUserParticipantID, recipient},
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	for i := range agentcomm.MaxInboxUnread {
		if _, err := o.Send(ctx, SendRequest{
			OperationID: fmt.Sprintf("op-fill-%s-%d", recipient, i), ThreadID: th.ID, Sender: meshUserParticipantID,
			Recipients: []agentcomm.ParticipantID{recipient}, Kind: agentcomm.MessageKindMessage, Body: "x",
		}); err != nil {
			t.Fatalf("fill %d: %v", i, err)
		}
	}
}

func TestBoardTaskAssignSendsMessageAtomically(t *testing.T) {
	o, store := newBoardTaskTestOrchestrator(t)
	const key = "task/api"
	mustBoardTask(t, o, BoardTaskRequest{OperationID: "create", Action: BoardTaskCreate, Actor: agentcomm.UserParticipantID, Key: key, Title: "API"})

	r := mustBoardTask(t, o, BoardTaskRequest{
		OperationID: "assign", Action: BoardTaskAssign, Actor: agentcomm.UserParticipantID, Key: key, ExpectedVersion: 1, To: boardTaskTestBackend,
	})
	if r.MessageID == "" || r.ThreadID == "" {
		t.Fatalf("assign receipt = %+v, want the message and its thread", r)
	}
	rs := store.Snapshot()
	m := rs.Assistants.Mesh.Messages[r.MessageID]
	if m == nil || m.ThreadID != r.ThreadID || m.Seq != r.Seq {
		t.Fatalf("message %s = %+v, want it in thread %s at the assign's seq %d", r.MessageID, m, r.ThreadID, r.Seq)
	}
	a, ok := rs.Assistants.Mesh.BoardTasks[key].Assignment(r.AssignmentID)
	if !ok || a.AssignedSeq != r.Seq {
		t.Fatalf("assignment = %+v, want it committed at seq %d with the message", a, r.Seq)
	}
	want := fmt.Sprintf("Board task %s assigned to you (assignment %s): API. Run `afm agent task start --key %s --assignment-id %s --expected-version 2` when you begin, `done` when finished.",
		key, r.AssignmentID, key, r.AssignmentID)
	if m.Body != want {
		t.Fatalf("body = %q, want %q", m.Body, want)
	}
	if inbox := boardTaskMessagesTo(store, boardTaskB); len(inbox) != 1 || inbox[0].ID != r.MessageID {
		t.Fatalf("inbox of B = %+v, want the assignment message", inbox)
	}

	// A create that assigns at once sends the message too.
	created := mustBoardTask(t, o, BoardTaskRequest{
		OperationID: "create-assign", Action: BoardTaskCreate, Actor: agentcomm.UserParticipantID, Key: "task/ui", Title: "UI", To: boardTaskTestFrontend,
	})
	if created.MessageID == "" || len(boardTaskMessagesTo(store, boardTaskC)) != 1 {
		t.Fatalf("create with an assignee: receipt = %+v, want one message to C", created)
	}
}

func TestBoardTaskAssignRejectsUndeliverable(t *testing.T) {
	cases := map[string]func(t *testing.T, o *Orchestrator) agentcomm.ParticipantID{
		"finished stage": func(t *testing.T, o *Orchestrator) agentcomm.ParticipantID {
			activateStage(t, o, boardTaskTestBackend)
			finishStage(t, o, boardTaskTestBackend)
			return boardTaskB
		},
		"full inbox": func(t *testing.T, o *Orchestrator) agentcomm.ParticipantID {
			fillInbox(t, o, boardTaskB)
			return boardTaskB
		},
		"watching helper": func(t *testing.T, o *Orchestrator) agentcomm.ParticipantID {
			stubPassingHelper(o)
			t.Cleanup(o.concurrency.WaitAgents)
			activateStage(t, o, assistantsTestStageID)
			taskID := attachWatcherTask(t, o, "op-watch", assistantsTestStageID, assistants.TriggerCheckpoints, assistants.ModeAdvisory)
			waitForTaskStatus(t, o.opts.Store, taskID, assistants.StatusWatching, 2*time.Second)
			return meshHelperParticipantID(taskID)
		},
		"stage awaiting assistants": func(t *testing.T, o *Orchestrator) agentcomm.ParticipantID {
			activateStage(t, o, boardTaskTestBackend)
			if _, ok := o.Trigger(boardTaskTestBackend, bus.EvAwaitAssistants, bus.GuardCtx{}, ""); !ok {
				t.Fatal("EvAwaitAssistants failed")
			}
			return boardTaskB
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			o, store := newBoardTaskTestOrchestrator(t)
			const key = "task/x"
			mustBoardTask(t, o, BoardTaskRequest{OperationID: "create", Action: BoardTaskCreate, Actor: agentcomm.UserParticipantID, Key: key, Title: "X"})
			to := setup(t, o)
			messages := len(store.Snapshot().Assistants.Mesh.Messages)

			_, err := o.BoardTask(context.Background(), BoardTaskRequest{
				OperationID: "assign", Action: BoardTaskAssign, Actor: agentcomm.UserParticipantID, Key: key, ExpectedVersion: 1, To: string(to),
			})
			requireRejection(t, err, assistants.CodeInvalidStatus)
			rs := store.Snapshot()
			if task := rs.Assistants.Mesh.BoardTasks[key]; task.Version != 1 || len(task.Assignments) != 0 {
				t.Fatalf("task = %+v, want it unassigned at version 1", task)
			}
			if got := len(rs.Assistants.Mesh.Messages); got != messages {
				t.Fatalf("messages = %d, want %d: a rejected assign sends nothing", got, messages)
			}
		})
	}
}

func TestBoardTaskSelfAssignAndClaimNoMessage(t *testing.T) {
	o, store := newBoardTaskTestOrchestrator(t)
	self := mustBoardTask(t, o, BoardTaskRequest{
		OperationID: "create-self", Action: BoardTaskCreate, Actor: boardTaskA, Key: "task/self", Title: "Self", To: string(boardTaskA),
	})
	mustBoardTask(t, o, BoardTaskRequest{OperationID: "create-free", Action: BoardTaskCreate, Actor: agentcomm.UserParticipantID, Key: "task/free", Title: "Free"})
	claim := mustBoardTask(t, o, BoardTaskRequest{OperationID: "claim", Action: BoardTaskClaim, Actor: boardTaskB, Key: "task/free", ExpectedVersion: 1})

	if self.MessageID != "" || claim.MessageID != "" {
		t.Fatalf("receipts = %+v, %+v; want no message for a self-assign or a claim", self, claim)
	}
	if n := len(store.Snapshot().Assistants.Mesh.Messages); n != 0 {
		t.Fatalf("messages = %d, want none", n)
	}
}

func TestBoardTaskDoneNotifiesCoordinator(t *testing.T) {
	o, store := newBoardTaskTestOrchestrator(t)
	ctx := context.Background()
	const key = "task/report"
	assigned := mustBoardTask(t, o, BoardTaskRequest{
		OperationID: "create", Action: BoardTaskCreate, Actor: boardTaskA, Key: key, Title: "Report", To: string(boardTaskB),
	})
	accepted := assigned.MessageID
	if _, err := o.Send(ctx, SendRequest{
		OperationID: "accept", ThreadID: assigned.ThreadID, Sender: boardTaskB, Recipients: []agentcomm.ParticipantID{boardTaskA},
		Kind: agentcomm.MessageKindReply, InReplyTo: &accepted, Body: "on it",
	}); err != nil {
		t.Fatalf("Send accept: %v", err)
	}
	readInbox(t, o, boardTaskA)

	wait, err := o.Wait(ctx, WaitRequest{
		OperationID: "wait", Waiter: boardTaskA, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage},
		Deadline: testFarDeadline(),
	})
	if err != nil || wait.Status != agentcomm.WaitPending {
		t.Fatalf("Wait = %+v, %v; want it pending", wait, err)
	}

	done := mustBoardTask(t, o, BoardTaskRequest{
		OperationID: "done", Action: BoardTaskDone, Actor: boardTaskB, Key: key, ExpectedVersion: 2, AssignmentID: assigned.AssignmentID, Text: "shipped",
	})
	if done.NoticeSkipped != "" {
		t.Fatalf("done receipt = %+v, want the notice sent", done)
	}
	if w := store.Snapshot().Assistants.Mesh.Waits[wait.WaitID]; w == nil || w.Status != agentcomm.WaitReady {
		t.Fatalf("coordinator's wait = %+v, want it ready", w)
	}
	notices := boardTaskMessagesTo(store, boardTaskA)
	want := fmt.Sprintf("Board task %s: assignment %s of %s is done: shipped", key, assigned.AssignmentID, boardTaskB)
	if len(notices) != 1 || notices[0].Body != want {
		t.Fatalf("notices to A = %+v, want one %q", notices, want)
	}
}

func TestBoardTaskNoticeGoesToCoordinatorNotAssigner(t *testing.T) {
	o, store := newBoardTaskTestOrchestrator(t)

	// The user assigns, A coordinates: B's done goes to A.
	byUser := mustBoardTask(t, o, BoardTaskRequest{
		OperationID: "create-1", Action: BoardTaskCreate, Actor: agentcomm.UserParticipantID, Key: "task/one", Title: "One",
		CoordinatorID: string(boardTaskA), To: string(boardTaskB),
	})
	mustBoardTask(t, o, BoardTaskRequest{OperationID: "done-1", Action: BoardTaskDone, Actor: boardTaskB, Key: "task/one", ExpectedVersion: 2, AssignmentID: byUser.AssignmentID})
	if got := boardTaskMessagesTo(store, boardTaskA); len(got) != 1 || !strings.HasSuffix(got[0].Body, "is done") {
		t.Fatalf("notices to A = %+v, want B's done", got)
	}

	// B claims A's task: its done goes to A.
	mustBoardTask(t, o, BoardTaskRequest{OperationID: "create-2", Action: BoardTaskCreate, Actor: boardTaskA, Key: "task/two", Title: "Two"})
	claim := mustBoardTask(t, o, BoardTaskRequest{OperationID: "claim-2", Action: BoardTaskClaim, Actor: boardTaskB, Key: "task/two", ExpectedVersion: 1})
	mustBoardTask(t, o, BoardTaskRequest{OperationID: "done-2", Action: BoardTaskDone, Actor: boardTaskB, Key: "task/two", ExpectedVersion: 2, AssignmentID: claim.AssignmentID})
	if got := boardTaskMessagesTo(store, boardTaskA); len(got) != 2 || !strings.Contains(got[1].Body, "task/two") {
		t.Fatalf("notices to A = %+v, want B's done of task/two", got)
	}

	// The user releases A's own assignment: A learns about it.
	self := mustBoardTask(t, o, BoardTaskRequest{
		OperationID: "create-3", Action: BoardTaskCreate, Actor: agentcomm.UserParticipantID, Key: "task/three", Title: "Three",
		CoordinatorID: string(boardTaskA), To: string(boardTaskA),
	})
	mustBoardTask(t, o, BoardTaskRequest{
		OperationID: "release-3", Action: BoardTaskRelease, Actor: agentcomm.UserParticipantID, Key: "task/three", ExpectedVersion: 2,
		AssignmentID: self.AssignmentID, Text: "reprioritized",
	})
	got := boardTaskMessagesTo(store, boardTaskA)
	want := fmt.Sprintf("Board task task/three: assignment %s of %s is released: reprioritized", self.AssignmentID, boardTaskA)
	if last := got[len(got)-1]; last.Body != want {
		t.Fatalf("last message to A = %q, want %q", last.Body, want)
	}
}

func TestBoardTaskNoNoticeForCoordinatorOrUser(t *testing.T) {
	o, store := newBoardTaskTestOrchestrator(t)

	own := mustBoardTask(t, o, BoardTaskRequest{
		OperationID: "create-own", Action: BoardTaskCreate, Actor: boardTaskA, Key: "task/own", Title: "Own", To: string(boardTaskA),
	})
	mustBoardTask(t, o, BoardTaskRequest{OperationID: "done-own", Action: BoardTaskDone, Actor: boardTaskA, Key: "task/own", ExpectedVersion: 2, AssignmentID: own.AssignmentID})

	mustBoardTask(t, o, BoardTaskRequest{OperationID: "create-user", Action: BoardTaskCreate, Actor: agentcomm.UserParticipantID, Key: "task/user", Title: "User"})
	claim := mustBoardTask(t, o, BoardTaskRequest{OperationID: "claim-user", Action: BoardTaskClaim, Actor: boardTaskB, Key: "task/user", ExpectedVersion: 1})
	done := mustBoardTask(t, o, BoardTaskRequest{OperationID: "done-user", Action: BoardTaskDone, Actor: boardTaskB, Key: "task/user", ExpectedVersion: 2, AssignmentID: claim.AssignmentID})

	if done.MessageID != "" || done.NoticeSkipped != "" {
		t.Fatalf("done receipt = %+v, want no notice at all for a task without a coordinator", done)
	}
	if n := len(store.Snapshot().Assistants.Mesh.Messages); n != 0 {
		t.Fatalf("messages = %d, want none", n)
	}
}

func TestBoardTaskNoticeSkippedStillCommits(t *testing.T) {
	cases := map[string]func(t *testing.T, o *Orchestrator){
		"coordinator inbox full": func(t *testing.T, o *Orchestrator) { fillInbox(t, o, boardTaskA) },
		"coordinator finished": func(t *testing.T, o *Orchestrator) {
			activateStage(t, o, assistantsTestStageID)
			finishStage(t, o, assistantsTestStageID)
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			o, store := newBoardTaskTestOrchestrator(t)
			const key = "task/skip"
			mustBoardTask(t, o, BoardTaskRequest{OperationID: "create", Action: BoardTaskCreate, Actor: boardTaskA, Key: key, Title: "Skip"})
			claim := mustBoardTask(t, o, BoardTaskRequest{OperationID: "claim", Action: BoardTaskClaim, Actor: boardTaskB, Key: key, ExpectedVersion: 1})
			setup(t, o)
			messages := len(store.Snapshot().Assistants.Mesh.Messages)

			done := mustBoardTask(t, o, BoardTaskRequest{OperationID: "done", Action: BoardTaskDone, Actor: boardTaskB, Key: key, ExpectedVersion: 2, AssignmentID: claim.AssignmentID})
			if done.Status != agentcomm.BoardTaskDone || done.NoticeSkipped == "" || done.MessageID != "" {
				t.Fatalf("done receipt = %+v, want done with the notice skipped", done)
			}
			if got := len(store.Snapshot().Assistants.Mesh.Messages); got != messages {
				t.Fatalf("messages = %d, want %d: the skipped notice is not sent", got, messages)
			}
			again := mustBoardTask(t, o, BoardTaskRequest{OperationID: "done", Action: BoardTaskDone, Actor: boardTaskB, Key: key, ExpectedVersion: 2, AssignmentID: claim.AssignmentID})
			if again != done {
				t.Fatalf("retry receipt = %+v, want the original %+v", again, done)
			}
		})
	}
}

func TestBoardTaskReopenOneNotice(t *testing.T) {
	o, store := newBoardTaskTestOrchestrator(t)
	const key = "task/pair"
	first := mustBoardTask(t, o, BoardTaskRequest{
		OperationID: "create", Action: BoardTaskCreate, Actor: agentcomm.UserParticipantID, Key: key, Title: "Pair",
		CoordinatorID: string(boardTaskA), To: string(boardTaskB),
	})
	second := mustBoardTask(t, o, BoardTaskRequest{
		OperationID: "assign-c", Action: BoardTaskAssign, Actor: agentcomm.UserParticipantID, Key: key, ExpectedVersion: 2, To: string(boardTaskC),
	})
	before := len(store.Snapshot().Assistants.Mesh.Messages)

	mustBoardTask(t, o, BoardTaskRequest{OperationID: "reopen", Action: BoardTaskReopen, Actor: agentcomm.UserParticipantID, Key: key, ExpectedVersion: 3, Text: "scope changed"})
	if got := len(store.Snapshot().Assistants.Mesh.Messages); got != before+1 {
		t.Fatalf("messages = %d, want exactly one more than %d", got, before)
	}
	notices := boardTaskMessagesTo(store, boardTaskA)
	want := fmt.Sprintf("Board task %s: assignment %s of %s, assignment %s of %s are reopened: scope changed",
		key, first.AssignmentID, boardTaskB, second.AssignmentID, boardTaskC)
	if len(notices) != 1 || notices[0].Body != want {
		t.Fatalf("notices to A = %+v, want one %q", notices, want)
	}
}

func TestBoardTaskReplayNoDuplicateNotice(t *testing.T) {
	o, store := newBoardTaskTestOrchestrator(t)
	const key = "task/replay"
	mustBoardTask(t, o, BoardTaskRequest{OperationID: "create", Action: BoardTaskCreate, Actor: boardTaskA, Key: key, Title: "Replay"})
	claim := mustBoardTask(t, o, BoardTaskRequest{OperationID: "claim", Action: BoardTaskClaim, Actor: boardTaskB, Key: key, ExpectedVersion: 1})
	doneReq := BoardTaskRequest{OperationID: "done", Action: BoardTaskDone, Actor: boardTaskB, Key: key, ExpectedVersion: 2, AssignmentID: claim.AssignmentID}
	done := mustBoardTask(t, o, doneReq)
	if done.MessageID == "" {
		t.Fatalf("done receipt = %+v, want the notice", done)
	}
	messages := len(store.Snapshot().Assistants.Mesh.Messages)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	resumed, resumedStore := reopenAssistantsTestOrchestrator(t, o)
	again := mustBoardTask(t, resumed, doneReq)
	if again != done {
		t.Fatalf("receipt after a restart = %+v, want the original %+v", again, done)
	}
	if got := len(resumedStore.Snapshot().Assistants.Mesh.Messages); got != messages {
		t.Fatalf("messages after a restart and retry = %d, want %d", got, messages)
	}
}

func TestBoardTaskRejectsEmptyActor(t *testing.T) {
	o, _ := newBoardTaskTestOrchestrator(t)
	_, err := o.BoardTask(context.Background(), BoardTaskRequest{OperationID: "create", Action: BoardTaskCreate, Key: "task/x", Title: "X"})
	requireRejection(t, err, assistants.CodeInvalidRequest)
}

// A retry answers from its committed operation even when the alias it named now matches another participant
// too: here a stage added on resume takes the run agent's alias.
func TestBoardTaskRetryAfterAliasBecameAmbiguous(t *testing.T) {
	o, store := newBoardTaskTestOrchestrator(t)
	stubPassingHelper(o)
	const key = "task/alias-retry"
	mustBoardTask(t, o, BoardTaskRequest{OperationID: "create", Action: BoardTaskCreate, Actor: agentcomm.UserParticipantID, Key: key, Title: "Alias"})
	agent := addTestRunAgent(t, o, "op-qa", nil)
	alias := store.Snapshot().Assistants.Mesh.Participants[agent.ParticipantID].Alias
	assignReq := BoardTaskRequest{OperationID: "assign", Action: BoardTaskAssign, Actor: agentcomm.UserParticipantID, Key: key, ExpectedVersion: 1, To: alias}
	first := mustBoardTask(t, o, assignReq)
	o.concurrency.WaitAgents()
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	o.opts.Stages = append(slices.Clone(o.opts.Stages), flow.Stage{ID: alias, Command: "claude"})
	resumed, _ := reopenAssistantsTestOrchestrator(t, o)
	t.Cleanup(resumed.concurrency.WaitAgents)
	if _, err := resolveRecipients(resumed.liveRoster(resumed.opts.Store.Snapshot()), []agentcomm.ParticipantID{agentcomm.ParticipantID(alias)}); err == nil {
		t.Fatalf("test setup: alias %q still resolves to one participant", alias)
	}
	again := mustBoardTask(t, resumed, assignReq)
	if again != first {
		t.Fatalf("retry receipt = %+v, want the original %+v", again, first)
	}
}

func TestPublishRejectsTaskNamespaceInTypedRun(t *testing.T) {
	o, store := newBoardTaskTestOrchestrator(t)
	_, err := publishAsUser(o, "op-task", "task/login", "Login form", 0)
	requireRejection(t, err, assistants.CodeInvalidRequest)
	if n := len(store.Snapshot().Assistants.Mesh.Board); n != 0 {
		t.Fatalf("board entries = %d, want none", n)
	}
	if _, err := publishAsUser(o, "op-notes", "notes/login", "fine", 0); err != nil {
		t.Fatalf("Publish outside task/: %v", err)
	}
}

func TestPublishAllowsTaskNamespaceInLegacyRun(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	if _, err := publishAsUser(o, "op-task", "task/login", "Login form", 0); err != nil {
		t.Fatalf("Publish task/ in a legacy run: %v", err)
	}
}

// A registered secret never reaches the journal, the task or a message — not even in part where a long text
// is cut to the message limit: the text is redacted first and only then cut.
func TestBoardTaskRedactsSecrets(t *testing.T) {
	o, store := newBoardTaskTestOrchestrator(t)
	const planted = "s3cr3t-board-value-1234" // a registered secret value
	o.opts.SecretValues = []string{planted}
	const key = "task/secret"
	// straddling is filler that puts the secret across the cut of a message body that starts with prefix.
	straddling := func(prefix string) string {
		return strings.Repeat("x", agentcomm.MaxBodyBytes-len("…")-len(prefix)-10) + planted + " tail"
	}

	assignmentID := deriveMeshID("board-assignment", "create")
	createReq := BoardTaskRequest{
		OperationID: "create", Action: BoardTaskCreate, Actor: boardTaskA, Key: key, To: string(boardTaskB),
		Title:       straddling(fmt.Sprintf("Board task %s assigned to you (assignment %s): ", key, assignmentID)),
		Description: "uses " + planted,
	}
	created := mustBoardTask(t, o, createReq)
	mustBoardTask(t, o, BoardTaskRequest{OperationID: "start", Action: BoardTaskStart, Actor: boardTaskB, Key: key, ExpectedVersion: 2, AssignmentID: assignmentID})
	mustBoardTask(t, o, BoardTaskRequest{
		OperationID: "progress", Action: BoardTaskProgress, Actor: boardTaskB, Key: key, ExpectedVersion: 3, AssignmentID: assignmentID,
		Text: "half, token " + planted, ArtifactRefs: []string{"artifacts/" + planted + ".md"},
	})
	doneReq := BoardTaskRequest{
		OperationID: "done", Action: BoardTaskDone, Actor: boardTaskB, Key: key, ExpectedVersion: 4, AssignmentID: assignmentID,
		Text: straddling(fmt.Sprintf("Board task %s: assignment %s of %s is done: ", key, assignmentID, boardTaskB)),
	}
	done := mustBoardTask(t, o, doneReq)

	partial := planted[:8] // what a cut inside the secret would leave behind
	rs := store.Snapshot()
	task := rs.Assistants.Mesh.BoardTasks[key]
	a, _ := task.Assignment(assignmentID)
	for name, text := range map[string]string{
		"title": task.Title, "description": task.Description, "progress": a.Progress, "result": a.Result,
		"artifact ref":       strings.Join(a.ArtifactRefs, ","),
		"assignment message": rs.Assistants.Mesh.Messages[created.MessageID].Body,
		"coordinator notice": rs.Assistants.Mesh.Messages[done.MessageID].Body,
	} {
		if strings.Contains(text, partial) {
			t.Errorf("%s holds the secret: %q", name, text)
		}
	}
	journal, err := os.ReadFile(filepath.Join(o.opts.RunDir, "events.jsonl"))
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	if strings.Contains(string(journal), partial) {
		t.Error("events.jsonl holds the secret")
	}

	for _, r := range []struct {
		req  BoardTaskRequest
		want BoardTaskReceipt
	}{{createReq, created}, {doneReq, done}} {
		if again := mustBoardTask(t, o, r.req); again != r.want {
			t.Errorf("retry of %s = %+v, want the original %+v", r.req.OperationID, again, r.want)
		}
	}
}

// The assign's message id answers the assigner even when the first response was lost: the retry under the same
// request id returns it, and a reply_to wait on it resolves with the assignee's answer — no extra Send needed.
func TestBoardTaskAssignRetryThenWaitOnMessage(t *testing.T) {
	o, store := newBoardTaskTestOrchestrator(t)
	ctx := context.Background()
	const key = "task/wait"
	mustBoardTask(t, o, BoardTaskRequest{OperationID: "create", Action: BoardTaskCreate, Actor: boardTaskA, Key: key, Title: "Wait"})
	assignReq := BoardTaskRequest{OperationID: "assign", Action: BoardTaskAssign, Actor: boardTaskA, Key: key, ExpectedVersion: 1, To: string(boardTaskB)}
	mustBoardTask(t, o, assignReq) // its response is lost
	messages := len(store.Snapshot().Assistants.Mesh.Messages)

	assigned := mustBoardTask(t, o, assignReq)
	if assigned.MessageID == "" || len(store.Snapshot().Assistants.Mesh.Messages) != messages {
		t.Fatalf("retry receipt = %+v, want the original message and no new one", assigned)
	}
	wait, err := o.Wait(ctx, WaitRequest{
		OperationID: "wait", Waiter: boardTaskA, Deadline: testFarDeadline(),
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: assigned.MessageID},
	})
	if err != nil || wait.Status != agentcomm.WaitPending {
		t.Fatalf("Wait = %+v, %v; want it pending on the assignment message", wait, err)
	}

	on := assigned.MessageID
	if _, err := o.Send(ctx, SendRequest{
		OperationID: "accept", ThreadID: assigned.ThreadID, Sender: boardTaskB, Recipients: []agentcomm.ParticipantID{boardTaskA},
		Kind: agentcomm.MessageKindReply, InReplyTo: &on, Body: "on it",
	}); err != nil {
		t.Fatalf("Send reply: %v", err)
	}
	if w := store.Snapshot().Assistants.Mesh.Waits[wait.WaitID]; w == nil || w.Status != agentcomm.WaitReady {
		t.Fatalf("assigner's wait = %+v, want it ready after the reply", w)
	}
}

// A done that lands before the coordinator registers its threadless addressed_message wait is not lost: the
// unread notice resolves the wait at once.
func TestBoardTaskDoneBeforeCoordinatorWaits(t *testing.T) {
	o, _ := newBoardTaskTestOrchestrator(t)
	const key = "task/early"
	assigned := mustBoardTask(t, o, BoardTaskRequest{
		OperationID: "create", Action: BoardTaskCreate, Actor: boardTaskA, Key: key, Title: "Early", To: string(boardTaskB),
	})
	done := mustBoardTask(t, o, BoardTaskRequest{
		OperationID: "done", Action: BoardTaskDone, Actor: boardTaskB, Key: key, ExpectedVersion: 2, AssignmentID: assigned.AssignmentID, Text: "shipped",
	})
	if done.MessageID == "" {
		t.Fatalf("done receipt = %+v, want the coordinator notice", done)
	}

	wait, err := o.Wait(context.Background(), WaitRequest{
		OperationID: "wait", Waiter: boardTaskA, Deadline: testFarDeadline(),
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage},
	})
	if err != nil || wait.Status != agentcomm.WaitReady {
		t.Fatalf("Wait = %+v, %v; want it ready at once on the unread notice", wait, err)
	}
}
