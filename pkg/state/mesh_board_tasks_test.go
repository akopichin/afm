package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
)

const testTaskKey = "task/login"

// openTypedStore opens a fresh store whose run is marked with the typed board task protocol.
func openTypedStore(t *testing.T, dir string) *Store {
	t.Helper()
	store := openProtocolStore(t, dir)
	if err := store.RecordBoardTaskProtocol(agentcomm.BoardTaskProtocolTypedV1, protocolActor); err != nil {
		t.Fatalf("RecordBoardTaskProtocol: %v", err)
	}
	return store
}

// applyTaskEffects commits effects on testTaskKey as one operation performed by actor.
func applyTaskEffects(t *testing.T, store *Store, actor string, effects ...Effect) error {
	t.Helper()
	for i := range effects {
		effects[i].Target = Target{BoardKey: testTaskKey, ParticipantID: actor}
	}
	op := Operation{
		ID:      fmt.Sprintf("task-op-%d-%s", store.Snapshot().LastSeq, actor),
		Kind:    "board_task_test",
		Actor:   Actor{Kind: "agent", ID: actor},
		Effects: effects,
	}
	_, err := store.ApplyRunOperation(op, nil)
	return err
}

func mustApplyTask(t *testing.T, store *Store, actor string, effects ...Effect) {
	t.Helper()
	if err := applyTaskEffects(t, store, actor, effects...); err != nil {
		t.Fatalf("apply %s by %s: %v", effects[0].Kind, actor, err)
	}
}

func taskEffect(t *testing.T, kind string, payload any) Effect {
	t.Helper()
	return Effect{Kind: kind, Payload: mustMarshal(t, payload)}
}

func meta(version int) MeshBoardTaskMeta { return MeshBoardTaskMeta{ExpectedVersion: version} }

func createTask(t *testing.T, coordinator agentcomm.ParticipantID) Effect {
	return taskEffect(t, kindMeshBoardTaskCreated, MeshBoardTaskCreatedPayload{
		Title: "Login form", Description: "Build it", CoordinatorID: coordinator, MeshBoardTaskMeta: meta(0),
	})
}

func assignTask(t *testing.T, version int, id string, who agentcomm.ParticipantID, start bool) Effect {
	return taskEffect(t, kindMeshBoardTaskAssigned, MeshBoardTaskAssignedPayload{
		AssignmentID: id, ParticipantID: who, Start: start, MeshBoardTaskMeta: meta(version),
	})
}

func startTask(t *testing.T, version int, id string) Effect {
	return taskEffect(t, kindMeshBoardTaskStarted, MeshBoardTaskStartedPayload{AssignmentID: id, MeshBoardTaskMeta: meta(version)})
}

func progressTask(t *testing.T, version int, id, text string) Effect {
	return taskEffect(t, kindMeshBoardTaskProgressed, MeshBoardTaskProgressedPayload{AssignmentID: id, Text: text, MeshBoardTaskMeta: meta(version)})
}

func completeTask(t *testing.T, version int, id string) Effect {
	return taskEffect(t, kindMeshBoardTaskCompleted, MeshBoardTaskCompletedPayload{
		AssignmentID: id, Result: "shipped", ArtifactRefs: []string{"artifacts/report.md"}, MeshBoardTaskMeta: meta(version),
	})
}

func releaseTask(t *testing.T, version int, id string) Effect {
	return taskEffect(t, kindMeshBoardTaskReleased, MeshBoardTaskReleasedPayload{AssignmentID: id, Reason: "busy", MeshBoardTaskMeta: meta(version)})
}

func reopenTask(t *testing.T, version int) Effect {
	return taskEffect(t, kindMeshBoardTaskReopened, MeshBoardTaskReopenedPayload{Reason: "redo", MeshBoardTaskMeta: meta(version)})
}

// assignAndStart has manager assign who at version, then who starts it: the task ends at version+2.
func assignAndStart(t *testing.T, store *Store, manager string, version int, id string, who agentcomm.ParticipantID) {
	t.Helper()
	mustApplyTask(t, store, manager, assignTask(t, version, id, who, false))
	mustApplyTask(t, store, string(who), startTask(t, version+1, id))
}

func boardTask(t *testing.T, store *Store) agentcomm.BoardTask {
	t.Helper()
	task, ok := store.Snapshot().Assistants.Mesh.BoardTasks[testTaskKey]
	if !ok {
		t.Fatalf("board task %s is not projected", testTaskKey)
	}
	return *task
}

func taskVersion(t *testing.T, store *Store) int {
	t.Helper()
	return boardTask(t, store).Version
}

func wantStatus(t *testing.T, store *Store, want agentcomm.BoardTaskStatus) {
	t.Helper()
	if got := boardTask(t, store).Status(); got != want {
		t.Fatalf("status = %s, want %s", got, want)
	}
}

func wantRejected(t *testing.T, err error, code assistants.ErrorCode, sentinel error) {
	t.Helper()
	var rejected *OperationRejectedError
	if !errors.As(err, &rejected) || rejected.Code != code {
		t.Fatalf("err = %v, want OperationRejectedError with code %s", err, code)
	}
	if sentinel != nil && !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want errors.Is %v", err, sentinel)
	}
}

func TestBoardTaskLifecycle(t *testing.T) {
	store := openTypedStore(t, t.TempDir())
	mustApplyTask(t, store, "a", createTask(t, ""))
	task := boardTask(t, store)
	if task.Version != 1 || task.AuthorID != "a" || task.Title != "Login form" {
		t.Fatalf("created task = %+v", task)
	}

	mustApplyTask(t, store, "a", assignTask(t, 1, "a1", "b", false))
	wantStatus(t, store, agentcomm.BoardTaskTodo)
	if _, ok := boardTask(t, store).ActiveFor("b"); !ok {
		t.Fatal("b has no active assignment after assign")
	}

	mustApplyTask(t, store, "b", startTask(t, 2, "a1"))
	wantStatus(t, store, agentcomm.BoardTaskInProgress)

	mustApplyTask(t, store, "b", progressTask(t, 3, "a1", "done — all good"))
	wantStatus(t, store, agentcomm.BoardTaskInProgress)
	if a, _ := boardTask(t, store).Assignment("a1"); a.Progress != "done — all good" {
		t.Fatalf("progress text = %q", a.Progress)
	}

	mustApplyTask(t, store, "b", completeTask(t, 4, "a1"))
	wantStatus(t, store, agentcomm.BoardTaskDone)
	a, _ := boardTask(t, store).Assignment("a1")
	if a.Result != "shipped" || a.CompletedSeq == 0 || len(a.ArtifactRefs) != 1 {
		t.Fatalf("completed assignment = %+v", a)
	}
	if v := taskVersion(t, store); v != 5 {
		t.Fatalf("version = %d, want 5", v)
	}
}

func TestBoardTaskMultiAssignee(t *testing.T) {
	store := openTypedStore(t, t.TempDir())
	mustApplyTask(t, store, "user", createTask(t, ""))
	assignAndStart(t, store, "user", 1, "a1", "a")
	assignAndStart(t, store, "user", 3, "b1", "b")

	mustApplyTask(t, store, "a", completeTask(t, 5, "a1"))
	wantStatus(t, store, agentcomm.BoardTaskInProgress)
	mustApplyTask(t, store, "b", completeTask(t, 6, "b1"))
	wantStatus(t, store, agentcomm.BoardTaskDone)
}

func TestBoardTaskStaleDoneAfterReassign(t *testing.T) {
	store := openTypedStore(t, t.TempDir())
	mustApplyTask(t, store, "user", createTask(t, ""))
	mustApplyTask(t, store, "user", assignTask(t, 1, "a1", "b", false))
	mustApplyTask(t, store, "user", releaseTask(t, 2, "a1"))
	mustApplyTask(t, store, "user", assignTask(t, 3, "a2", "b", false))

	err := applyTaskEffects(t, store, "b", completeTask(t, 4, "a1"))
	wantRejected(t, err, assistants.CodeInvalidStatus, ErrBoardTaskInvalidTransition)

	a2, ok := boardTask(t, store).Assignment("a2")
	if !ok || a2.State != agentcomm.AssignmentAssigned || a2.Result != "" {
		t.Fatalf("a2 changed by a stale done: %+v", a2)
	}
	if ended, ok := boardTask(t, store).EndedAssignment("a1"); !ok || ended.Ended != agentcomm.AssignmentReleased {
		t.Fatalf("a1 in history = %+v, ok=%v", ended, ok)
	}
}

func TestBoardTaskReopenArchivesAll(t *testing.T) {
	store := openTypedStore(t, t.TempDir())
	mustApplyTask(t, store, "user", createTask(t, ""))
	assignAndStart(t, store, "user", 1, "a1", "a")
	mustApplyTask(t, store, "user", assignTask(t, 3, "b1", "b", false))

	mustApplyTask(t, store, "user", reopenTask(t, 4))
	task := boardTask(t, store)
	if len(task.Assignments) != 0 || len(task.History) != 2 {
		t.Fatalf("after reopen: assignments=%d history=%d", len(task.Assignments), len(task.History))
	}
	for _, h := range task.History {
		if h.Ended != agentcomm.AssignmentReopened || h.EndedBy != "user" || h.EndReason != "redo" {
			t.Fatalf("history entry = %+v", h)
		}
	}
	if task.Status() != agentcomm.BoardTaskTodo || task.Generation != 1 {
		t.Fatalf("status = %s generation = %d, want todo/1", task.Status(), task.Generation)
	}
}

func TestBoardTaskReleaseDoneRejected(t *testing.T) {
	t.Run("single", func(t *testing.T) {
		store := openTypedStore(t, t.TempDir())
		mustApplyTask(t, store, "user", createTask(t, ""))
		assignAndStart(t, store, "user", 1, "a1", "a")
		mustApplyTask(t, store, "a", completeTask(t, 3, "a1"))

		err := applyTaskEffects(t, store, "user", releaseTask(t, 4, "a1"))
		wantRejected(t, err, assistants.CodeInvalidStatus, ErrBoardTaskInvalidTransition)
		wantStatus(t, store, agentcomm.BoardTaskDone)
	})
	t.Run("double", func(t *testing.T) {
		store := openTypedStore(t, t.TempDir())
		mustApplyTask(t, store, "user", createTask(t, ""))
		assignAndStart(t, store, "user", 1, "a1", "a")
		assignAndStart(t, store, "user", 3, "b1", "b")
		mustApplyTask(t, store, "a", completeTask(t, 5, "a1"))

		err := applyTaskEffects(t, store, "user", releaseTask(t, 6, "a1"))
		wantRejected(t, err, assistants.CodeInvalidStatus, ErrBoardTaskInvalidTransition)
		if len(boardTask(t, store).Assignments) != 2 {
			t.Fatal("a rejected release changed the assignments")
		}
	})
}

func TestBoardTaskDuplicateActiveAssignmentRejected(t *testing.T) {
	store := openTypedStore(t, t.TempDir())
	mustApplyTask(t, store, "user", createTask(t, ""))
	mustApplyTask(t, store, "user", assignTask(t, 1, "a1", "b", false))

	err := applyTaskEffects(t, store, "user", assignTask(t, 2, "a2", "b", false))
	wantRejected(t, err, assistants.CodeInvalidStatus, ErrBoardTaskInvalidTransition)

	err = applyTaskEffects(t, store, "b", assignTask(t, 2, "a3", "b", true))
	wantRejected(t, err, assistants.CodeInvalidStatus, ErrBoardTaskInvalidTransition)

	if task := boardTask(t, store); len(task.Assignments) != 1 || task.Version != 2 {
		t.Fatalf("rejected assigns changed the task: %+v", task)
	}
}

func TestBoardTaskDuplicateAssignmentIDRejected(t *testing.T) {
	store := openTypedStore(t, t.TempDir())
	mustApplyTask(t, store, "user", createTask(t, ""))
	mustApplyTask(t, store, "user", assignTask(t, 1, "a1", "b", false))
	mustApplyTask(t, store, "user", releaseTask(t, 2, "a1"))

	err := applyTaskEffects(t, store, "user", assignTask(t, 3, "a1", "c", false))
	wantRejected(t, err, assistants.CodeInvalidStatus, ErrBoardTaskInvalidTransition)

	err = applyTaskEffects(t, store, "user", assignTask(t, 3, "u1", "user", false))
	wantRejected(t, err, assistants.CodeInvalidStatus, ErrBoardTaskInvalidTransition)
}

func TestBoardTaskClaim(t *testing.T) {
	store := openTypedStore(t, t.TempDir())
	mustApplyTask(t, store, "user", createTask(t, ""))

	mustApplyTask(t, store, "a", assignTask(t, 1, "a1", "a", true))
	wantStatus(t, store, agentcomm.BoardTaskInProgress)

	err := applyTaskEffects(t, store, "b", assignTask(t, 1, "b1", "b", true))
	wantRejected(t, err, assistants.CodeStaleRevision, ErrBoardTaskStaleVersion)

	err = applyTaskEffects(t, store, "b", assignTask(t, 2, "b1", "b", true))
	wantRejected(t, err, assistants.CodeInvalidStatus, ErrBoardTaskInvalidTransition)
}

func TestBoardTaskAuthorization(t *testing.T) {
	t.Run("agent author is coordinator", func(t *testing.T) {
		store := openTypedStore(t, t.TempDir())
		mustApplyTask(t, store, "x", createTask(t, ""))

		err := applyTaskEffects(t, store, "c", assignTask(t, 1, "b1", "b", false))
		wantRejected(t, err, assistants.CodeActorNotAllowed, ErrBoardTaskNotAuthorized)

		mustApplyTask(t, store, "x", assignTask(t, 1, "b1", "b", false))
		err = applyTaskEffects(t, store, "c", startTask(t, 2, "b1"))
		wantRejected(t, err, assistants.CodeActorNotAllowed, ErrBoardTaskNotAuthorized)

		// The coordinator may self-assign without starting.
		mustApplyTask(t, store, "x", assignTask(t, 2, "x1", "x", false))
		// The assignee may not release its own assignment.
		err = applyTaskEffects(t, store, "b", releaseTask(t, 3, "b1"))
		wantRejected(t, err, assistants.CodeActorNotAllowed, ErrBoardTaskNotAuthorized)
	})
	t.Run("user manages everything", func(t *testing.T) {
		store := openTypedStore(t, t.TempDir())
		mustApplyTask(t, store, "x", createTask(t, ""))
		mustApplyTask(t, store, "user", assignTask(t, 1, "b1", "b", false))
		mustApplyTask(t, store, "user", releaseTask(t, 2, "b1"))
		mustApplyTask(t, store, "user", reopenTask(t, 3))
		title := "Renamed"
		mustApplyTask(t, store, "user", taskEffect(t, kindMeshBoardTaskEdited, MeshBoardTaskEditedPayload{Title: &title, MeshBoardTaskMeta: meta(4)}))
		if task := boardTask(t, store); task.Title != "Renamed" || task.Description != "Build it" {
			t.Fatalf("edit changed more than the title: %+v", task)
		}
	})
	t.Run("user task with coordinator", func(t *testing.T) {
		store := openTypedStore(t, t.TempDir())
		mustApplyTask(t, store, "user", createTask(t, "x"))
		mustApplyTask(t, store, "x", assignTask(t, 1, "b1", "b", false))
		err := applyTaskEffects(t, store, "b", assignTask(t, 2, "c1", "c", false))
		wantRejected(t, err, assistants.CodeActorNotAllowed, ErrBoardTaskNotAuthorized)
	})
	t.Run("user task without coordinator", func(t *testing.T) {
		store := openTypedStore(t, t.TempDir())
		mustApplyTask(t, store, "user", createTask(t, ""))
		err := applyTaskEffects(t, store, "x", assignTask(t, 1, "b1", "b", false))
		wantRejected(t, err, assistants.CodeActorNotAllowed, ErrBoardTaskNotAuthorized)
		err = applyTaskEffects(t, store, "x", reopenTask(t, 1))
		wantRejected(t, err, assistants.CodeActorNotAllowed, ErrBoardTaskNotAuthorized)
	})
}

func TestBoardTaskStaleVersion(t *testing.T) {
	store := openTypedStore(t, t.TempDir())
	mustApplyTask(t, store, "user", createTask(t, ""))
	mustApplyTask(t, store, "user", assignTask(t, 1, "a1", "a", false))
	before := boardTask(t, store)

	err := applyTaskEffects(t, store, "user", assignTask(t, 1, "b1", "b", false))
	wantRejected(t, err, assistants.CodeStaleRevision, ErrBoardTaskStaleVersion)
	if after := boardTask(t, store); !reflect.DeepEqual(before, after) {
		t.Fatalf("stale assign changed the projection:\nbefore %+v\nafter  %+v", before, after)
	}

	err = applyTaskEffects(t, store, "user", createTask(t, ""))
	wantRejected(t, err, assistants.CodeStaleRevision, ErrBoardTaskStaleVersion)
}

func TestBoardTaskStatusMismatchRejected(t *testing.T) {
	store := openTypedStore(t, t.TempDir())
	mustApplyTask(t, store, "user", createTask(t, ""))
	payload := MeshBoardTaskAssignedPayload{
		AssignmentID: "a1", ParticipantID: "a",
		MeshBoardTaskMeta: MeshBoardTaskMeta{ExpectedVersion: 1, Status: agentcomm.BoardTaskInProgress, NoticeSkipped: "a"},
	}
	err := applyTaskEffects(t, store, "user", taskEffect(t, kindMeshBoardTaskAssigned, payload))
	wantRejected(t, err, assistants.CodeInvalidStatus, ErrBoardTaskInvalidTransition)

	payload.Status = agentcomm.BoardTaskTodo
	mustApplyTask(t, store, "user", taskEffect(t, kindMeshBoardTaskAssigned, payload))
}

func TestBoardTaskRejectedBatchDoesNotLeak(t *testing.T) {
	store := openTypedStore(t, t.TempDir())
	mustApplyTask(t, store, "user", createTask(t, ""))
	mustApplyTask(t, store, "user", assignTask(t, 1, "a1", "a", false))

	// Valid assign of b, then an invalid start of a1 (actor user is not its assignee).
	err := applyTaskEffects(t, store, "user", assignTask(t, 2, "b1", "b", false), startTask(t, 3, "a1"))
	wantRejected(t, err, assistants.CodeActorNotAllowed, ErrBoardTaskNotAuthorized)

	task := boardTask(t, store)
	if len(task.Assignments) != 1 || task.Version != 2 {
		t.Fatalf("rejected batch leaked into the projection: %+v", task)
	}
}

func TestBoardTaskReplayMatchesLive(t *testing.T) {
	dir := t.TempDir()
	store := openTypedStore(t, dir)
	mustApplyTask(t, store, "x", createTask(t, ""))
	mustApplyTask(t, store, "x", assignTask(t, 1, "a1", "a", false))
	mustApplyTask(t, store, "a", startTask(t, 2, "a1"), progressTask(t, 3, "a1", "half"))
	assignAndStart(t, store, "x", 4, "b1", "b")
	mustApplyTask(t, store, "x", releaseTask(t, 6, "b1"))
	mustApplyTask(t, store, "a", completeTask(t, 7, "a1"))
	mustApplyTask(t, store, "user", reopenTask(t, 8))
	live := store.Snapshot().Assistants.Mesh.BoardTasks
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "state.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("remove state.json: %v", err)
	}

	replayed := openProtocolStore(t, dir).Snapshot().Assistants.Mesh.BoardTasks
	if !reflect.DeepEqual(live, replayed) {
		t.Fatalf("replay differs from live:\nlive     %+v\nreplayed %+v", *live[testTaskKey], *replayed[testTaskKey])
	}
}

func TestBoardTaskRequiresProtocol(t *testing.T) {
	store := openProtocolStore(t, t.TempDir())
	seqBefore := store.Snapshot().LastSeq

	err := applyTaskEffects(t, store, "user", createTask(t, ""))
	wantRejected(t, err, assistants.CodeInvalidStatus, ErrBoardTaskProtocol)
	if got := store.Snapshot().LastSeq; got != seqBefore {
		t.Fatalf("journal grew: LastSeq %d -> %d", seqBefore, got)
	}
}

func TestBoardTaskInputLimits(t *testing.T) {
	store := openTypedStore(t, t.TempDir())
	mustApplyTask(t, store, "user", createTask(t, ""))
	assignAndStart(t, store, "user", 1, "a1", "a")

	long := make([]byte, agentcomm.MaxBodyBytes+1)
	err := applyTaskEffects(t, store, "a", progressTask(t, 3, "a1", string(long)))
	wantRejected(t, err, assistants.CodeInvalidRequest, nil)

	refs := MeshBoardTaskCompletedPayload{AssignmentID: "a1", ArtifactRefs: []string{"/etc/passwd"}, MeshBoardTaskMeta: meta(3)}
	err = applyTaskEffects(t, store, "a", taskEffect(t, kindMeshBoardTaskCompleted, refs))
	wantRejected(t, err, assistants.CodeInvalidRequest, ErrBoardArtifactRefOutsideRun)
}

// boardTaskControl builds a standalone typed board task control record on testTaskKey.
func boardTaskControl(t *testing.T, kind, actor string, payload any) *Control {
	t.Helper()
	return &Control{
		Kind: kind, SchemaVersion: ControlSchemaVersion,
		Target: Target{BoardKey: testTaskKey, ParticipantID: actor}, Payload: mustMarshal(t, payload),
	}
}

// seedBoardTask marks as typed and creates testTaskKey by user, with a1 assigned to "a" (version 2) when
// assigned is true (version 1 otherwise).
func seedBoardTask(t *testing.T, as *AssistantsState, assigned bool) {
	t.Helper()
	mustApply(t, as, &Control{Kind: kindMeshBoardProtocolSet, SchemaVersion: ControlSchemaVersion, Payload: []byte(`{"protocol":"typed_v1"}`)})
	mustApply(t, as, boardTaskControl(t, kindMeshBoardTaskCreated, "user", MeshBoardTaskCreatedPayload{Title: "T"}))
	if assigned {
		mustApply(t, as, boardTaskControl(t, kindMeshBoardTaskAssigned, "user", MeshBoardTaskAssignedPayload{
			AssignmentID: "a1", ParticipantID: "a", MeshBoardTaskMeta: meta(1),
		}))
	}
}

func TestBoardTaskAssignOnDoneNeedsReopen(t *testing.T) {
	store := openTypedStore(t, t.TempDir())
	mustApplyTask(t, store, "user", createTask(t, ""))
	assignAndStart(t, store, "user", 1, "b1", "b")
	mustApplyTask(t, store, "b", completeTask(t, 3, "b1"))
	before := boardTask(t, store)

	err := applyTaskEffects(t, store, "user", assignTask(t, 4, "c1", "c", false))
	wantRejected(t, err, assistants.CodeInvalidStatus, ErrBoardTaskInvalidTransition)
	if after := boardTask(t, store); !reflect.DeepEqual(before, after) {
		t.Fatalf("rejected assign on a done task changed it:\nbefore %+v\nafter  %+v", before, after)
	}

	mustApplyTask(t, store, "user", reopenTask(t, 4))
	mustApplyTask(t, store, "user", assignTask(t, 5, "c1", "c", false))
	wantStatus(t, store, agentcomm.BoardTaskTodo)
}

func TestBoardTaskManagerCannotStartForOthers(t *testing.T) {
	store := openTypedStore(t, t.TempDir())
	mustApplyTask(t, store, "x", createTask(t, ""))

	for _, manager := range []string{"user", "x"} {
		err := applyTaskEffects(t, store, manager, assignTask(t, 1, "b1", "b", true))
		wantRejected(t, err, assistants.CodeActorNotAllowed, ErrBoardTaskNotAuthorized)
	}
	if task := boardTask(t, store); len(task.Assignments) != 0 || task.Version != 1 {
		t.Fatalf("rejected assign-with-start changed the task: %+v", task)
	}
}
