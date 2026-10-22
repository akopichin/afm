package state

import (
	"errors"
	"fmt"
	"slices"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
)

// Typed board tasks (agentcomm.BoardTask) change only through the mesh_board_task_* effects below. Every rule
// is checked here, in the projection, so a live commit and a journal replay decide exactly the same way.
// Target.BoardKey names the task and Target.ParticipantID is the actor making the change.

var (
	// ErrBoardTaskStaleVersion — the effect's ExpectedVersion is not the task's current Version (or a create
	// targets a key that already exists).
	ErrBoardTaskStaleVersion = errors.New("mesh: board task change is based on a stale version")
	// ErrBoardTaskNotAuthorized — the actor may not make this change to the task.
	ErrBoardTaskNotAuthorized = errors.New("mesh: participant is not allowed to make this board task change")
	// ErrBoardTaskInvalidTransition — the change is not possible in the task's current state.
	ErrBoardTaskInvalidTransition = errors.New("mesh: board task change is not valid in its current state")
	// ErrBoardTaskProtocol — the run was not created with the typed board task protocol.
	ErrBoardTaskProtocol = errors.New("mesh: this run does not use typed board tasks")
)

// MeshBoardTaskMeta is embedded in every board task effect payload.
type MeshBoardTaskMeta struct {
	// ExpectedVersion is the task version the change is based on; 0 for a create.
	ExpectedVersion int `json:"expected_version"`
	// Status and NoticeSkipped are kept for the orchestrator to rebuild a retry's receipt: the task's status
	// after the operation and why its coordinator notice was skipped. The projection ignores both.
	Status        agentcomm.BoardTaskStatus `json:"status,omitempty"`
	NoticeSkipped string                    `json:"notice_skipped,omitempty"`
}

func (m MeshBoardTaskMeta) boardTaskMeta() MeshBoardTaskMeta { return m }

// MeshBoardTaskCreatedPayload is the payload of mesh_board_task_created; the actor becomes the task author.
type MeshBoardTaskCreatedPayload struct {
	Title         string                  `json:"title"`
	Description   string                  `json:"description,omitempty"`
	CoordinatorID agentcomm.ParticipantID `json:"coordinator_id,omitempty"`
	MeshBoardTaskMeta
}

// MeshBoardTaskAssignedPayload is the payload of mesh_board_task_assigned. Start is a claim: the actor, who
// must be ParticipantID itself, takes a free task and starts it at once.
type MeshBoardTaskAssignedPayload struct {
	AssignmentID  string                  `json:"assignment_id"`
	ParticipantID agentcomm.ParticipantID `json:"participant_id"`
	Start         bool                    `json:"start,omitempty"`
	MeshBoardTaskMeta
}

// MeshBoardTaskStartedPayload is the payload of mesh_board_task_started.
type MeshBoardTaskStartedPayload struct {
	AssignmentID string `json:"assignment_id"`
	MeshBoardTaskMeta
}

// MeshBoardTaskProgressedPayload is the payload of mesh_board_task_progressed.
type MeshBoardTaskProgressedPayload struct {
	AssignmentID string   `json:"assignment_id"`
	Text         string   `json:"text"`
	ArtifactRefs []string `json:"artifact_refs,omitempty"`
	MeshBoardTaskMeta
}

// MeshBoardTaskCompletedPayload is the payload of mesh_board_task_completed.
type MeshBoardTaskCompletedPayload struct {
	AssignmentID string   `json:"assignment_id"`
	Result       string   `json:"result,omitempty"`
	ArtifactRefs []string `json:"artifact_refs,omitempty"`
	MeshBoardTaskMeta
}

// MeshBoardTaskReleasedPayload is the payload of mesh_board_task_released.
type MeshBoardTaskReleasedPayload struct {
	AssignmentID string `json:"assignment_id"`
	Reason       string `json:"reason,omitempty"`
	MeshBoardTaskMeta
}

// MeshBoardTaskReopenedPayload is the payload of mesh_board_task_reopened.
type MeshBoardTaskReopenedPayload struct {
	Reason string `json:"reason,omitempty"`
	MeshBoardTaskMeta
}

// MeshBoardTaskEditedPayload is the payload of mesh_board_task_edited; only the non-nil fields change.
type MeshBoardTaskEditedPayload struct {
	Title         *string                  `json:"title,omitempty"`
	Description   *string                  `json:"description,omitempty"`
	CoordinatorID *agentcomm.ParticipantID `json:"coordinator_id,omitempty"`
	MeshBoardTaskMeta
}

type boardTaskPayload interface {
	boardTaskMeta() MeshBoardTaskMeta
}

// applyBoardTaskEffect is the part every board task effect shares: it checks the protocol, the key and the
// expected version, lets change check and apply its own rule to a copy of the task, and stores the copy with
// the next version. The stored task is never changed in place, so earlier snapshots keep their own view.
func applyBoardTaskEffect[P boardTaskPayload](
	as *AssistantsState, c *Control, dryRun bool,
	change func(task *agentcomm.BoardTask, actor agentcomm.ParticipantID, c *Control, p P) error,
) error {
	key := c.Target.BoardKey
	actor := agentcomm.ParticipantID(c.Target.ParticipantID)
	if as.Mesh.BoardTaskProtocol != agentcomm.BoardTaskProtocolTypedV1 {
		return boardRejected(assistants.CodeInvalidStatus, ErrBoardTaskProtocol, "%s: this run does not use typed board tasks", c.Kind)
	}
	if !agentcomm.ValidBoardTaskKey(key) {
		return boardTaskBadInput("%s: invalid task key %q", c.Kind, key)
	}
	if actor == "" {
		return fmt.Errorf("%s: empty target participant_id", c.Kind)
	}
	var p P
	if err := decodePayload(c.Payload, &p); err != nil {
		return fmt.Errorf("decode %s payload: %w", c.Kind, err)
	}
	task, err := boardTaskToChange(as.Mesh, c.Kind, key, p.boardTaskMeta().ExpectedVersion)
	if err != nil {
		return err
	}
	if err := change(&task, actor, c, p); err != nil {
		return err
	}
	task.Version++
	task.UpdatedSeq, task.UpdatedAt = c.Seq, c.Time

	if dryRun {
		return nil
	}
	as.Mesh.BoardTasks[key] = &task
	as.resolvePendingWaits(agentcomm.WaitReady, func(w *MeshWait) bool {
		return as.meshWaitSatisfied(w)
	})
	return nil
}

// boardTaskToChange returns an independent copy of the task to change: a new empty task for a create, the
// current one otherwise — after checking that the change is based on the task's current version.
func boardTaskToChange(ms MeshState, kind, key string, expectedVersion int) (agentcomm.BoardTask, error) {
	creating := kind == kindMeshBoardTaskCreated
	task := agentcomm.BoardTask{Key: key}
	existing, exists := ms.BoardTasks[key]
	if exists {
		task = cloneBoardTask(*existing)
	}
	switch {
	case creating && exists:
		return task, boardRejected(assistants.CodeStaleRevision, ErrBoardTaskStaleVersion, "board task %s already exists", key)
	case !creating && !exists:
		return task, boardRejected(assistants.CodeNotFound, nil, "board task %s does not exist", key)
	case expectedVersion != task.Version:
		return task, boardRejected(assistants.CodeStaleRevision, ErrBoardTaskStaleVersion, "board task %s is at version %d, not %d — read it again", key, task.Version, expectedVersion)
	}
	return task, nil
}

func createBoardTask(task *agentcomm.BoardTask, actor agentcomm.ParticipantID, c *Control, p MeshBoardTaskCreatedPayload) error {
	if p.Title == "" {
		return boardTaskBadInput("board task %s needs a title", task.Key)
	}
	if err := checkBoardTaskText(task.Key, p.Title, p.Description); err != nil {
		return err
	}
	task.AuthorID = actor
	task.CoordinatorID = p.CoordinatorID
	task.Title = p.Title
	task.Description = p.Description
	task.CreatedSeq, task.CreatedAt = c.Seq, c.Time
	return nil
}

func assignBoardTask(task *agentcomm.BoardTask, actor agentcomm.ParticipantID, c *Control, p MeshBoardTaskAssignedPayload) error {
	who := p.ParticipantID
	if p.AssignmentID == "" || who == "" {
		return boardTaskBadInput("assigning task %s needs an assignment id and a participant", task.Key)
	}
	// Only the participant itself starts its work, so Start on an assign always means a claim.
	if p.Start && who != actor {
		return boardTaskForbidden("%s cannot start task %s for %s: only the assignee starts its work", actor, task.Key, who)
	}
	claim := p.Start
	if claim && len(task.Assignments) > 0 {
		return boardTaskInvalid("%s cannot claim task %s: it is already assigned", actor, task.Key)
	}
	if !claim && !mayManageBoardTask(*task, actor) {
		return boardTaskForbidden("%s may not assign task %s: only the user or its coordinator can", actor, task.Key)
	}
	if task.Status() == agentcomm.BoardTaskDone {
		return boardTaskInvalid("task %s is done — reopen it first", task.Key)
	}
	if who == agentcomm.UserParticipantID {
		return boardTaskInvalid("task %s cannot be assigned to the user", task.Key)
	}
	if _, busy := task.ActiveFor(who); busy {
		return boardTaskInvalid("%s already has an active assignment on task %s", who, task.Key)
	}
	_, active := task.Assignment(p.AssignmentID)
	_, ended := task.EndedAssignment(p.AssignmentID)
	if active || ended {
		return boardTaskInvalid("assignment id %s is already used on task %s", p.AssignmentID, task.Key)
	}

	a := agentcomm.BoardTaskAssignment{
		ID: p.AssignmentID, ParticipantID: who, State: agentcomm.AssignmentAssigned, AssignedBy: actor, AssignedSeq: c.Seq,
	}
	if p.Start {
		a.State, a.StartedSeq = agentcomm.AssignmentInProgress, c.Seq
	}
	task.Assignments = append(task.Assignments, a)
	return nil
}

func startBoardTask(task *agentcomm.BoardTask, actor agentcomm.ParticipantID, c *Control, p MeshBoardTaskStartedPayload) error {
	a, err := ownActiveAssignment(task, actor, p.AssignmentID)
	if err != nil {
		return err
	}
	if a.State != agentcomm.AssignmentAssigned {
		return boardTaskInvalid("assignment %s of task %s is %s, only an assigned one can start", a.ID, task.Key, a.State)
	}
	a.State, a.StartedSeq = agentcomm.AssignmentInProgress, c.Seq
	return nil
}

func progressBoardTask(task *agentcomm.BoardTask, actor agentcomm.ParticipantID, _ *Control, p MeshBoardTaskProgressedPayload) error {
	a, err := ownActiveAssignment(task, actor, p.AssignmentID)
	if err != nil {
		return err
	}
	if err := requireUnfinished(task.Key, *a); err != nil {
		return err
	}
	if p.Text == "" {
		return boardTaskBadInput("progress on task %s needs a text", task.Key)
	}
	if err := checkBoardTaskReport(task.Key, p.Text, p.ArtifactRefs); err != nil {
		return err
	}
	a.Progress = p.Text
	if p.ArtifactRefs != nil {
		a.ArtifactRefs = p.ArtifactRefs
	}
	return nil
}

func completeBoardTask(task *agentcomm.BoardTask, actor agentcomm.ParticipantID, c *Control, p MeshBoardTaskCompletedPayload) error {
	a, err := ownActiveAssignment(task, actor, p.AssignmentID)
	if err != nil {
		return err
	}
	if err := requireUnfinished(task.Key, *a); err != nil {
		return err
	}
	if err := checkBoardTaskReport(task.Key, p.Result, p.ArtifactRefs); err != nil {
		return err
	}
	a.State, a.CompletedSeq = agentcomm.AssignmentDone, c.Seq
	a.Result = p.Result
	if p.ArtifactRefs != nil {
		a.ArtifactRefs = p.ArtifactRefs
	}
	return nil
}

func releaseBoardTask(task *agentcomm.BoardTask, actor agentcomm.ParticipantID, c *Control, p MeshBoardTaskReleasedPayload) error {
	if !mayManageBoardTask(*task, actor) {
		return boardTaskForbidden("%s may not release assignments of task %s: only the user or its coordinator can", actor, task.Key)
	}
	if err := checkBoardTaskText(task.Key, p.Reason); err != nil {
		return err
	}
	i := slices.IndexFunc(task.Assignments, func(a agentcomm.BoardTaskAssignment) bool { return a.ID == p.AssignmentID })
	if i < 0 {
		return boardTaskInvalid("assignment %s of task %s is not active", p.AssignmentID, task.Key)
	}
	if err := requireUnfinished(task.Key, task.Assignments[i]); err != nil {
		return err
	}
	task.History = append(task.History, endedAssignment(task.Assignments[i], agentcomm.AssignmentReleased, actor, p.Reason, c.Seq))
	task.Assignments = slices.Delete(task.Assignments, i, i+1)
	return nil
}

func reopenBoardTask(task *agentcomm.BoardTask, actor agentcomm.ParticipantID, c *Control, p MeshBoardTaskReopenedPayload) error {
	if !mayManageBoardTask(*task, actor) {
		return boardTaskForbidden("%s may not reopen task %s: only the user or its coordinator can", actor, task.Key)
	}
	if err := checkBoardTaskText(task.Key, p.Reason); err != nil {
		return err
	}
	for _, a := range task.Assignments {
		task.History = append(task.History, endedAssignment(a, agentcomm.AssignmentReopened, actor, p.Reason, c.Seq))
	}
	task.Assignments = nil
	task.Generation++
	return nil
}

func editBoardTask(task *agentcomm.BoardTask, actor agentcomm.ParticipantID, _ *Control, p MeshBoardTaskEditedPayload) error {
	if !mayManageBoardTask(*task, actor) {
		return boardTaskForbidden("%s may not edit task %s: only the user or its coordinator can", actor, task.Key)
	}
	if p.Title != nil {
		if *p.Title == "" {
			return boardTaskBadInput("board task %s needs a title", task.Key)
		}
		if err := checkBoardTaskText(task.Key, *p.Title); err != nil {
			return err
		}
		task.Title = *p.Title
	}
	if p.Description != nil {
		if err := checkBoardTaskText(task.Key, *p.Description); err != nil {
			return err
		}
		task.Description = *p.Description
	}
	if p.CoordinatorID != nil {
		task.CoordinatorID = *p.CoordinatorID
	}
	return nil
}

// mayManageBoardTask reports whether actor may assign others, release, reopen and edit the task: the user
// always may, an agent only as the task's coordinator. The actor is never empty here.
func mayManageBoardTask(task agentcomm.BoardTask, actor agentcomm.ParticipantID) bool {
	return actor == agentcomm.UserParticipantID || actor == task.Coordinator()
}

// ownActiveAssignment returns the actor's own active assignment by id, pointing into task.Assignments.
func ownActiveAssignment(task *agentcomm.BoardTask, actor agentcomm.ParticipantID, id string) (*agentcomm.BoardTaskAssignment, error) {
	i := slices.IndexFunc(task.Assignments, func(a agentcomm.BoardTaskAssignment) bool { return a.ID == id })
	if i < 0 {
		return nil, boardTaskInvalid("assignment %s of task %s is not active", id, task.Key)
	}
	a := &task.Assignments[i]
	if a.ParticipantID != actor {
		return nil, boardTaskForbidden("assignment %s of task %s belongs to %s, not %s", id, task.Key, a.ParticipantID, actor)
	}
	return a, nil
}

func requireUnfinished(key string, a agentcomm.BoardTaskAssignment) error {
	if a.State == agentcomm.AssignmentDone {
		return boardTaskInvalid("assignment %s of task %s is already done", a.ID, key)
	}
	return nil
}

func endedAssignment(a agentcomm.BoardTaskAssignment, end agentcomm.AssignmentEnd, by agentcomm.ParticipantID, reason string, seq uint64) agentcomm.BoardTaskAssignment {
	a.Ended, a.EndedBy, a.EndReason, a.EndedSeq = end, by, reason, seq
	return a
}

func checkBoardTaskText(key string, texts ...string) error {
	for _, text := range texts {
		if len(text) > agentcomm.MaxBodyBytes {
			return boardTaskBadInput("text of task %s is %d bytes, more than %d", key, len(text), agentcomm.MaxBodyBytes)
		}
	}
	return nil
}

// checkBoardTaskReport checks a progress or result report: its text and artifact references.
func checkBoardTaskReport(key, text string, refs []string) error {
	if err := checkBoardTaskText(key, text); err != nil {
		return err
	}
	if len(refs) > agentcomm.MaxArtifactRefs {
		return boardTaskBadInput("task %s report has %d artifact refs, more than %d", key, len(refs), agentcomm.MaxArtifactRefs)
	}
	return checkBoardArtifactRefs(key, refs)
}

// cloneBoardTask copies a task together with its assignment lists, so changing the copy never touches the
// original.
func cloneBoardTask(t agentcomm.BoardTask) agentcomm.BoardTask {
	t.Assignments = cloneBoardTaskAssignments(t.Assignments)
	t.History = cloneBoardTaskAssignments(t.History)
	return t
}

func cloneBoardTaskAssignments(list []agentcomm.BoardTaskAssignment) []agentcomm.BoardTaskAssignment {
	out := slices.Clone(list)
	for i := range out {
		out[i].ArtifactRefs = slices.Clone(out[i].ArtifactRefs)
	}
	return out
}

func boardTaskForbidden(format string, args ...any) error {
	return boardRejected(assistants.CodeActorNotAllowed, ErrBoardTaskNotAuthorized, format, args...)
}

func boardTaskInvalid(format string, args ...any) error {
	return boardRejected(assistants.CodeInvalidStatus, ErrBoardTaskInvalidTransition, format, args...)
}

func boardTaskBadInput(format string, args ...any) error {
	return boardRejected(assistants.CodeInvalidRequest, nil, format, args...)
}
