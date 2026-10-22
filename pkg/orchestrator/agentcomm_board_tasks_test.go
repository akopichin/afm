package orchestrator

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

// Stages whose agents take part in the board task tests, by alias.
const (
	boardTaskTestBackend  = "dev-backend"
	boardTaskTestFrontend = "dev-frontend"
)

// newBoardTaskTestOrchestrator builds the assistants test orchestrator for a run created with the typed board
// task protocol, with two extra stage agents to assign tasks to.
func newBoardTaskTestOrchestrator(t *testing.T) (*Orchestrator, *state.Store) {
	t.Helper()
	recordProtocol := func(store *state.Store) {
		if err := store.RecordBoardTaskProtocol(agentcomm.BoardTaskProtocolTypedV1, state.Actor{Kind: "system", ID: "afm"}); err != nil {
			t.Fatalf("RecordBoardTaskProtocol: %v", err)
		}
	}
	return newAssistantsTestOrchestratorWith(t, true, recordProtocol,
		flow.Stage{ID: boardTaskTestBackend, Command: "claude"}, flow.Stage{ID: boardTaskTestFrontend, Command: "claude"})
}

// mustBoardTask runs req and fails the test on an error.
func mustBoardTask(t *testing.T, o *Orchestrator, req BoardTaskRequest) BoardTaskReceipt {
	t.Helper()
	r, err := o.BoardTask(context.Background(), req)
	if err != nil {
		t.Fatalf("BoardTask %s %s (%s): %v", req.Action, req.Key, req.OperationID, err)
	}
	return r
}

// requireRejection fails the test unless err is an operation rejection with code.
func requireRejection(t *testing.T, err error, code assistants.ErrorCode) {
	t.Helper()
	var rejected *state.OperationRejectedError
	if !errors.As(err, &rejected) || rejected.Code != code {
		t.Fatalf("error = %v, want a %s rejection", err, code)
	}
}

func TestBoardTaskServiceLifecycle(t *testing.T) {
	o, _ := newBoardTaskTestOrchestrator(t)
	backend := meshStageParticipantID(boardTaskTestBackend)
	const key = "task/login"

	created := mustBoardTask(t, o, BoardTaskRequest{
		OperationID: "create-1", Action: BoardTaskCreate, Actor: agentcomm.UserParticipantID, Key: key,
		Title: new("Login form"), Description: new("email + password"),
	})
	if created.Key != key || created.Version != 1 || created.Status != agentcomm.BoardTaskTodo || created.Seq == 0 {
		t.Fatalf("create receipt = %+v, want %s at version 1, todo, with a seq", created, key)
	}

	claimed := mustBoardTask(t, o, BoardTaskRequest{OperationID: "claim-1", Action: BoardTaskClaim, Actor: backend, Key: key, ExpectedVersion: 1})
	if claimed.Version != 2 || claimed.Status != agentcomm.BoardTaskInProgress || claimed.AssignmentID == "" {
		t.Fatalf("claim receipt = %+v, want version 2, in_progress, with an assignment id", claimed)
	}

	progressed := mustBoardTask(t, o, BoardTaskRequest{
		OperationID: "progress-1", Action: BoardTaskProgress, Actor: backend, Key: key, ExpectedVersion: 2,
		AssignmentID: claimed.AssignmentID, Text: "form renders",
	})
	if progressed.Version != 3 || progressed.Status != agentcomm.BoardTaskInProgress || progressed.AssignmentID != claimed.AssignmentID {
		t.Fatalf("progress receipt = %+v, want version 3, in_progress, assignment %s", progressed, claimed.AssignmentID)
	}

	done := mustBoardTask(t, o, BoardTaskRequest{
		OperationID: "done-1", Action: BoardTaskDone, Actor: backend, Key: key, ExpectedVersion: 3,
		AssignmentID: claimed.AssignmentID, Text: "merged", ArtifactRefs: &[]string{"artifacts/login.md"},
	})
	if done.Version != 4 || done.Status != agentcomm.BoardTaskDone {
		t.Fatalf("done receipt = %+v, want version 4, done", done)
	}

	views, err := o.BoardTasks(context.Background(), key)
	if err != nil {
		t.Fatalf("BoardTasks: %v", err)
	}
	if len(views) != 1 || views[0].Status != agentcomm.BoardTaskDone || views[0].Version != 4 {
		t.Fatalf("BoardTasks(%s) = %+v, want the task done at version 4", key, views)
	}
	a := views[0].Assignments[0]
	if a.ParticipantID != backend || a.Progress != "form renders" || a.Result != "merged" {
		t.Fatalf("assignment = %+v, want %s's progress and result", a, backend)
	}
}

func TestBoardTaskRetryReturnsOriginalReceipt(t *testing.T) {
	o, store := newBoardTaskTestOrchestrator(t)
	backend := meshStageParticipantID(boardTaskTestBackend)
	const key = "task/retry"
	mustBoardTask(t, o, BoardTaskRequest{OperationID: "create-r", Action: BoardTaskCreate, Actor: agentcomm.UserParticipantID, Key: key, Title: new("Retry")})
	claimed := mustBoardTask(t, o, BoardTaskRequest{OperationID: "claim-r", Action: BoardTaskClaim, Actor: backend, Key: key, ExpectedVersion: 1})

	doneReq := BoardTaskRequest{
		OperationID: "done-r", Action: BoardTaskDone, Actor: backend, Key: key, ExpectedVersion: 2,
		AssignmentID: claimed.AssignmentID, Text: "finished",
	}
	first := mustBoardTask(t, o, doneReq)
	mustBoardTask(t, o, BoardTaskRequest{OperationID: "reopen-r", Action: BoardTaskReopen, Actor: agentcomm.UserParticipantID, Key: key, ExpectedVersion: 3, Text: "not yet"})
	journaled := store.Snapshot().LastSeq

	again := mustBoardTask(t, o, doneReq)
	if again != first {
		t.Fatalf("retry receipt = %+v, want the original %+v", again, first)
	}
	if again.Status != agentcomm.BoardTaskDone || again.Version != 3 {
		t.Fatalf("retry receipt = %+v, want done at version 3 although the task was reopened since", again)
	}
	if got := store.Snapshot().LastSeq; got != journaled {
		t.Fatalf("journal grew on a retry: last seq %d, want %d", got, journaled)
	}

	changed := doneReq
	changed.Text = "something else"
	_, err := o.BoardTask(context.Background(), changed)
	if !errors.Is(err, state.ErrOperationIDConflict) {
		t.Fatalf("same request id with another text: error = %v, want a request id conflict", err)
	}
}

func TestBoardTaskDisabledForLegacyRun(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	if o.BoardTasksEnabled() {
		t.Fatal("BoardTasksEnabled() = true for a run without the typed protocol")
	}
	journaled := store.Snapshot().LastSeq

	for _, req := range []BoardTaskRequest{
		{OperationID: "legacy-create", Action: BoardTaskCreate, Actor: agentcomm.UserParticipantID, Key: "task/x", Title: new("X")},
		{OperationID: "legacy-reopen", Action: BoardTaskReopen, Actor: agentcomm.UserParticipantID, Key: "task/x", ExpectedVersion: 1},
	} {
		_, err := o.BoardTask(context.Background(), req)
		requireRejection(t, err, assistants.CodeInvalidStatus)
	}
	views, err := o.BoardTasks(context.Background(), "")
	if err != nil || len(views) != 0 {
		t.Fatalf("BoardTasks on a legacy run = %+v, %v; want empty without an error", views, err)
	}
	if got := store.Snapshot().LastSeq; got != journaled {
		t.Fatalf("journal grew on a legacy run: last seq %d, want %d", got, journaled)
	}
}

func TestBoardTaskActorFromRequestOnly(t *testing.T) {
	o, _ := newBoardTaskTestOrchestrator(t)
	backend := meshStageParticipantID(boardTaskTestBackend)
	frontend := meshStageParticipantID(boardTaskTestFrontend)
	const key = "task/owned"
	mustBoardTask(t, o, BoardTaskRequest{OperationID: "create-o", Action: BoardTaskCreate, Actor: agentcomm.UserParticipantID, Key: key, Title: new("Owned")})
	assigned := mustBoardTask(t, o, BoardTaskRequest{
		OperationID: "assign-o", Action: BoardTaskAssign, Actor: agentcomm.UserParticipantID, Key: key, ExpectedVersion: 1, To: string(frontend),
	})

	_, err := o.BoardTask(context.Background(), BoardTaskRequest{
		OperationID: "start-o", Action: BoardTaskStart, Actor: backend, Key: key, ExpectedVersion: 2, AssignmentID: assigned.AssignmentID,
	})
	requireRejection(t, err, assistants.CodeActorNotAllowed)
}

func TestBoardTaskAliasResolution(t *testing.T) {
	o, _ := newBoardTaskTestOrchestrator(t)
	backend := meshStageParticipantID(boardTaskTestBackend)
	const key = "task/alias"
	mustBoardTask(t, o, BoardTaskRequest{
		OperationID: "create-a", Action: BoardTaskCreate, Actor: agentcomm.UserParticipantID, Key: key, Title: new("Alias"),
		CoordinatorID: new(boardTaskTestFrontend),
	})

	assigned := mustBoardTask(t, o, BoardTaskRequest{
		OperationID: "assign-a", Action: BoardTaskAssign, Actor: agentcomm.UserParticipantID, Key: key, ExpectedVersion: 1, To: boardTaskTestBackend,
	})
	views, err := o.BoardTasks(context.Background(), key)
	if err != nil {
		t.Fatalf("BoardTasks: %v", err)
	}
	task := views[0]
	if task.CoordinatorID != meshStageParticipantID(boardTaskTestFrontend) {
		t.Fatalf("coordinator = %q, want the participant of alias %s", task.CoordinatorID, boardTaskTestFrontend)
	}
	if a, ok := task.Assignment(assigned.AssignmentID); !ok || a.ParticipantID != backend {
		t.Fatalf("assignment %s = %+v, want it for %s", assigned.AssignmentID, a, backend)
	}

	_, err = o.BoardTask(context.Background(), BoardTaskRequest{
		OperationID: "assign-unknown", Action: BoardTaskAssign, Actor: agentcomm.UserParticipantID, Key: key, ExpectedVersion: 2, To: "nobody",
	})
	requireRejection(t, err, assistants.CodeNotFound)
	_, err = o.BoardTask(context.Background(), BoardTaskRequest{
		OperationID: "edit-unknown", Action: BoardTaskEdit, Actor: agentcomm.UserParticipantID, Key: key, ExpectedVersion: 2, CoordinatorID: new("nobody"),
	})
	requireRejection(t, err, assistants.CodeNotFound)
}

func TestBoardTaskCreateWithAssignee(t *testing.T) {
	o, _ := newBoardTaskTestOrchestrator(t)
	r := mustBoardTask(t, o, BoardTaskRequest{
		OperationID: "create-assign", Action: BoardTaskCreate, Actor: agentcomm.UserParticipantID, Key: "task/both", Title: new("Both"),
		To: boardTaskTestBackend,
	})
	if r.Version != 2 || r.Status != agentcomm.BoardTaskTodo || r.AssignmentID == "" {
		t.Fatalf("create with an assignee: receipt = %+v, want version 2, todo, with the assignment id", r)
	}
	views, err := o.BoardTasks(context.Background(), "task/both")
	if err != nil {
		t.Fatalf("BoardTasks: %v", err)
	}
	if a, ok := views[0].Assignment(r.AssignmentID); !ok || a.ParticipantID != meshStageParticipantID(boardTaskTestBackend) {
		t.Fatalf("assignment = %+v, want it for %s", a, boardTaskTestBackend)
	}
}

func TestBoardTasksListsSortedAndUnknownKeyIsNotFound(t *testing.T) {
	o, _ := newBoardTaskTestOrchestrator(t)
	for _, key := range []string{"task/b", "task/a"} {
		mustBoardTask(t, o, BoardTaskRequest{OperationID: "create-" + key, Action: BoardTaskCreate, Actor: agentcomm.UserParticipantID, Key: key, Title: new(key)})
	}
	views, err := o.BoardTasks(context.Background(), "")
	if err != nil {
		t.Fatalf("BoardTasks: %v", err)
	}
	if len(views) != 2 || views[0].Key != "task/a" || views[1].Key != "task/b" {
		t.Fatalf("BoardTasks() = %+v, want task/a then task/b", views)
	}
	_, err = o.BoardTasks(context.Background(), "task/missing")
	requireRejection(t, err, assistants.CodeNotFound)
}

func TestBoardTaskRequiresRequestID(t *testing.T) {
	o, store := newBoardTaskTestOrchestrator(t)
	journaled := store.Snapshot().LastSeq
	if _, err := o.BoardTask(context.Background(), BoardTaskRequest{Action: BoardTaskCreate, Actor: agentcomm.UserParticipantID, Key: "task/x", Title: new("X")}); err == nil {
		t.Fatal("BoardTask without a request id: want an error")
	}
	_, err := o.BoardTask(context.Background(), BoardTaskRequest{OperationID: "x", Action: "steal", Actor: agentcomm.UserParticipantID, Key: "task/x"})
	requireRejection(t, err, assistants.CodeInvalidRequest)
	if got := store.Snapshot().LastSeq; got != journaled {
		t.Fatalf("journal grew on rejected requests: last seq %d, want %d", got, journaled)
	}
}

// Concurrent claims of one free task: exactly one wins, every other gets a rejection, and the task ends with one
// active assignment.
func TestBoardTaskConcurrentClaimsOneWinner(t *testing.T) {
	o, store := newBoardTaskTestOrchestrator(t)
	const key = "task/race"
	mustBoardTask(t, o, BoardTaskRequest{OperationID: "create", Action: BoardTaskCreate, Actor: agentcomm.UserParticipantID, Key: key, Title: new("Race")})

	claimants := []agentcomm.ParticipantID{
		meshStageParticipantID(assistantsTestStageID), meshStageParticipantID(boardTaskTestBackend), meshStageParticipantID(boardTaskTestFrontend),
	}
	errs := make([]error, len(claimants))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, who := range claimants {
		wg.Go(func() {
			<-start
			_, errs[i] = o.BoardTask(context.Background(), BoardTaskRequest{
				OperationID: "claim-" + string(who), Action: BoardTaskClaim, Actor: who, Key: key, ExpectedVersion: 1,
			})
		})
	}
	close(start)
	wg.Wait()

	wins := 0
	for i, err := range errs {
		var rejected *state.OperationRejectedError
		switch {
		case err == nil:
			wins++
		case errors.As(err, &rejected) && (rejected.Code == assistants.CodeStaleRevision || rejected.Code == assistants.CodeInvalidStatus):
		default:
			t.Errorf("claim by %s: %v, want success or a stale_revision/invalid_status rejection", claimants[i], err)
		}
	}
	task := store.Snapshot().Assistants.Mesh.BoardTasks[key]
	if wins != 1 || len(task.Assignments) != 1 || task.Version != 2 {
		t.Fatalf("wins = %d, task = %+v; want one winner and one active assignment at version 2", wins, task)
	}
}

// An edit changes exactly the fields it carries: an empty description or coordinator clears it (the
// coordinator then falls back to the author), an absent one stays.
func TestBoardTaskEditClearsFields(t *testing.T) {
	o, store := newBoardTaskTestOrchestrator(t)
	const key = "task/clear"
	mustBoardTask(t, o, BoardTaskRequest{
		OperationID: "create", Action: BoardTaskCreate, Actor: boardTaskA, Key: key,
		Title: new("Clear"), Description: new("old text"), CoordinatorID: new(boardTaskTestBackend),
	})
	editReq := BoardTaskRequest{
		OperationID: "edit", Action: BoardTaskEdit, Actor: agentcomm.UserParticipantID, Key: key, ExpectedVersion: 1,
		Description: new(""), CoordinatorID: new(""),
	}
	edited := mustBoardTask(t, o, editReq)
	task := store.Snapshot().Assistants.Mesh.BoardTasks[key]
	if task.Title != "Clear" || task.Description != "" || task.CoordinatorID != "" || task.Coordinator() != boardTaskA {
		t.Fatalf("task after the edit = %+v, want the title kept, the description and coordinator cleared, %s coordinating", task, boardTaskA)
	}

	if again := mustBoardTask(t, o, editReq); again != edited {
		t.Fatalf("retry receipt = %+v, want the original %+v", again, edited)
	}
	absent := editReq
	absent.Description = nil
	if _, err := o.BoardTask(context.Background(), absent); !errors.Is(err, state.ErrOperationIDConflict) {
		t.Fatalf("same request id without the description: error = %v, want a request id conflict", err)
	}

	_, err := o.BoardTask(context.Background(), BoardTaskRequest{
		OperationID: "edit-title", Action: BoardTaskEdit, Actor: agentcomm.UserParticipantID, Key: key, ExpectedVersion: 2, Title: new(""),
	})
	requireRejection(t, err, assistants.CodeInvalidRequest)
	_, err = o.BoardTask(context.Background(), BoardTaskRequest{
		OperationID: "edit-nothing", Action: BoardTaskEdit, Actor: agentcomm.UserParticipantID, Key: key, ExpectedVersion: 2,
	})
	requireRejection(t, err, assistants.CodeInvalidRequest)
}

// A done with an empty artifact list clears the refs a progress reported; one without a list keeps them.
func TestBoardTaskDoneClearsArtifactRefs(t *testing.T) {
	o, store := newBoardTaskTestOrchestrator(t)
	const key = "task/refs"
	mustBoardTask(t, o, BoardTaskRequest{OperationID: "create", Action: BoardTaskCreate, Actor: agentcomm.UserParticipantID, Key: key, Title: new("Refs")})
	claimed := mustBoardTask(t, o, BoardTaskRequest{OperationID: "claim", Action: BoardTaskClaim, Actor: boardTaskB, Key: key, ExpectedVersion: 1})
	refs := func() []string {
		a, _ := store.Snapshot().Assistants.Mesh.BoardTasks[key].Assignment(claimed.AssignmentID)
		return a.ArtifactRefs
	}
	mustBoardTask(t, o, BoardTaskRequest{
		OperationID: "progress", Action: BoardTaskProgress, Actor: boardTaskB, Key: key, ExpectedVersion: 2,
		AssignmentID: claimed.AssignmentID, Text: "draft", ArtifactRefs: &[]string{"artifacts/draft.md"},
	})
	mustBoardTask(t, o, BoardTaskRequest{
		OperationID: "progress-2", Action: BoardTaskProgress, Actor: boardTaskB, Key: key, ExpectedVersion: 3,
		AssignmentID: claimed.AssignmentID, Text: "still going",
	})
	if got := refs(); len(got) != 1 {
		t.Fatalf("refs after a progress without a list = %v, want the draft kept", got)
	}
	doneReq := BoardTaskRequest{
		OperationID: "done", Action: BoardTaskDone, Actor: boardTaskB, Key: key, ExpectedVersion: 4,
		AssignmentID: claimed.AssignmentID, Text: "nothing to show", ArtifactRefs: &[]string{},
	}
	done := mustBoardTask(t, o, doneReq)
	if got := refs(); got != nil {
		t.Fatalf("refs after a done with an empty list = %v, want none", got)
	}
	if again := mustBoardTask(t, o, doneReq); again != done {
		t.Fatalf("retry receipt = %+v, want the original %+v", again, done)
	}
	absent := doneReq
	absent.ArtifactRefs = nil
	if _, err := o.BoardTask(context.Background(), absent); !errors.Is(err, state.ErrOperationIDConflict) {
		t.Fatalf("same request id without the list: error = %v, want a request id conflict", err)
	}
}
