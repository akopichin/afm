package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/state"
)

// Typed board tasks (agentcomm.BoardTask). Every rule of a change — who may make it, in which state, on which
// version — lives in the projection (pkg/state/mesh_board_tasks.go). BoardTask only turns a request into the
// change's effects, plans the task status each effect leads to by running that same projection on a copy, and
// lets ApplyRunOperation decide.

// BoardTaskAction names the change BoardTask makes to a task.
type BoardTaskAction string

const (
	BoardTaskCreate   BoardTaskAction = "create"
	BoardTaskAssign   BoardTaskAction = "assign"
	BoardTaskClaim    BoardTaskAction = "claim"
	BoardTaskStart    BoardTaskAction = "start"
	BoardTaskProgress BoardTaskAction = "progress"
	BoardTaskDone     BoardTaskAction = "done"
	BoardTaskRelease  BoardTaskAction = "release"
	BoardTaskReopen   BoardTaskAction = "reopen"
	BoardTaskEdit     BoardTaskAction = "edit"
)

// BoardTaskRequest is one change to a typed board task. Actor MUST be set by the authenticated caller (the
// agent's token or the human's session), never from a request body.
type BoardTaskRequest struct {
	OperationID     string
	Action          BoardTaskAction
	Actor           agentcomm.ParticipantID
	Key             string
	ExpectedVersion int
	// AssignmentID names the assignment a start, progress, done or release acts on.
	AssignmentID string
	// To is the participant (id or alias) an assign — or a create that assigns at once — hands the task to.
	To          string
	Title       string
	Description string
	// CoordinatorID is the task's coordinator (id or alias) on a create or an edit.
	CoordinatorID string
	// Text is the progress text, the done result or the release/reopen reason.
	Text         string
	ArtifactRefs []string
}

// BoardTaskReceipt describes the task right after the operation it answers — rebuilt from that operation's
// own effects, never read from the board, which may already hold a newer version.
type BoardTaskReceipt struct {
	Key           string                    `json:"key"`
	Version       int                       `json:"version"`
	Status        agentcomm.BoardTaskStatus `json:"status"`
	AssignmentID  string                    `json:"assignment_id,omitempty"`
	Seq           uint64                    `json:"seq"`
	MessageID     agentcomm.MessageID       `json:"message_id,omitempty"`
	ThreadID      agentcomm.ThreadID        `json:"thread_id,omitempty"`
	NoticeSkipped string                    `json:"notice_skipped,omitempty"`
}

// boardTaskEffectPrefix starts the kind of every board task effect (mesh_board_task_created, ...).
const boardTaskEffectPrefix = "mesh_board_task_"

// BoardTasksEnabled reports whether this run was created with the typed board task protocol.
func (o *Orchestrator) BoardTasksEnabled() bool {
	return boardTasksEnabled(o.opts.Store.Snapshot())
}

func boardTasksEnabled(rs state.RunState) bool {
	return rs.Assistants.Mesh.BoardTaskProtocol == agentcomm.BoardTaskProtocolTypedV1
}

// BoardTask makes one change to a typed board task and answers with the task right after it. A retry under
// the same request id answers from the operation it committed, whatever has happened to the task since.
func (o *Orchestrator) BoardTask(ctx context.Context, req BoardTaskRequest) (BoardTaskReceipt, error) {
	if req.OperationID == "" {
		return BoardTaskReceipt{}, errors.New("agentcomm: operation id is required")
	}
	actor := boardTaskActor(req.Actor)
	if err := rejectReservedRequestID(req.OperationID, actor); err != nil {
		return BoardTaskReceipt{}, err
	}
	rs := o.opts.Store.Snapshot()
	if !boardTasksEnabled(rs) {
		return BoardTaskReceipt{}, &state.OperationRejectedError{Code: assistants.CodeInvalidStatus, Reason: "board tasks are not enabled for this run"}
	}

	changes, err := o.planBoardTaskChanges(rs, req)
	if err != nil {
		return BoardTaskReceipt{}, err
	}
	target := state.Target{BoardKey: req.Key, ParticipantID: string(req.Actor)}
	effects, err := boardTaskEffects(target, changes)
	if err != nil {
		return BoardTaskReceipt{}, err
	}
	if receipt, replayed, err := o.replayedBoardTask(req.OperationID, effects); replayed {
		return receipt, err
	}
	if effects, err = planBoardTaskStatuses(rs, target, changes); err != nil {
		return BoardTaskReceipt{}, err
	}

	op := state.Operation{ID: req.OperationID, Kind: opKindMeshBoardTask, Actor: actor, Effects: effects}
	res, err := o.opts.Store.ApplyRunOperation(op, nil)
	if err != nil {
		return BoardTaskReceipt{}, err
	}
	return boardTaskReceipt(effects, res.Seq)
}

// boardTaskActor is the journal actor of a change made by participant p.
func boardTaskActor(p agentcomm.ParticipantID) state.Actor {
	if p == agentcomm.UserParticipantID {
		return state.Actor{Kind: state.ActorKindUser}
	}
	return state.Actor{Kind: "agent", ID: string(p)}
}

// boardTaskChange is one board task effect being planned: its kind and a pointer to its payload, with the
// payload's embedded meta, where the expected version and the planned status are filled in.
type boardTaskChange struct {
	kind    string
	payload any
	meta    *state.MeshBoardTaskMeta
}

// planBoardTaskChanges maps req to the board task effects it makes, in commit order. Text is redacted and
// participant aliases resolved here; whether the change is allowed is the projection's decision. A new
// assignment's id is derived from the request id, so a retry plans the same one.
func (o *Orchestrator) planBoardTaskChanges(rs state.RunState, req BoardTaskRequest) ([]boardTaskChange, error) {
	newAssignmentID := deriveMeshID("board-assignment", req.OperationID)
	text, refs := o.RedactSecrets(req.Text), o.redactAll(req.ArtifactRefs)

	var changes []boardTaskChange
	switch req.Action {
	case BoardTaskCreate:
		coordinator, err := o.boardTaskParticipant(rs, req.CoordinatorID)
		if err != nil {
			return nil, err
		}
		p := &state.MeshBoardTaskCreatedPayload{Title: o.RedactSecrets(req.Title), Description: o.RedactSecrets(req.Description), CoordinatorID: coordinator}
		changes = append(changes, boardTaskChange{"mesh_board_task_created", p, &p.MeshBoardTaskMeta})
		if req.To != "" {
			assignee, err := o.boardTaskParticipant(rs, req.To)
			if err != nil {
				return nil, err
			}
			a := &state.MeshBoardTaskAssignedPayload{AssignmentID: newAssignmentID, ParticipantID: assignee}
			changes = append(changes, boardTaskChange{"mesh_board_task_assigned", a, &a.MeshBoardTaskMeta})
		}
	case BoardTaskAssign:
		assignee, err := o.boardTaskParticipant(rs, req.To)
		if err != nil {
			return nil, err
		}
		p := &state.MeshBoardTaskAssignedPayload{AssignmentID: newAssignmentID, ParticipantID: assignee}
		changes = append(changes, boardTaskChange{"mesh_board_task_assigned", p, &p.MeshBoardTaskMeta})
	case BoardTaskClaim:
		p := &state.MeshBoardTaskAssignedPayload{AssignmentID: newAssignmentID, ParticipantID: req.Actor, Start: true}
		changes = append(changes, boardTaskChange{"mesh_board_task_assigned", p, &p.MeshBoardTaskMeta})
	case BoardTaskStart:
		p := &state.MeshBoardTaskStartedPayload{AssignmentID: req.AssignmentID}
		changes = append(changes, boardTaskChange{"mesh_board_task_started", p, &p.MeshBoardTaskMeta})
	case BoardTaskProgress:
		p := &state.MeshBoardTaskProgressedPayload{AssignmentID: req.AssignmentID, Text: text, ArtifactRefs: refs}
		changes = append(changes, boardTaskChange{"mesh_board_task_progressed", p, &p.MeshBoardTaskMeta})
	case BoardTaskDone:
		p := &state.MeshBoardTaskCompletedPayload{AssignmentID: req.AssignmentID, Result: text, ArtifactRefs: refs}
		changes = append(changes, boardTaskChange{"mesh_board_task_completed", p, &p.MeshBoardTaskMeta})
	case BoardTaskRelease:
		p := &state.MeshBoardTaskReleasedPayload{AssignmentID: req.AssignmentID, Reason: text}
		changes = append(changes, boardTaskChange{"mesh_board_task_released", p, &p.MeshBoardTaskMeta})
	case BoardTaskReopen:
		p := &state.MeshBoardTaskReopenedPayload{Reason: text}
		changes = append(changes, boardTaskChange{"mesh_board_task_reopened", p, &p.MeshBoardTaskMeta})
	case BoardTaskEdit:
		p, err := o.boardTaskEdit(rs, req)
		if err != nil {
			return nil, err
		}
		changes = append(changes, boardTaskChange{"mesh_board_task_edited", p, &p.MeshBoardTaskMeta})
	default:
		return nil, &state.OperationRejectedError{Code: assistants.CodeInvalidRequest, Reason: fmt.Sprintf("unknown board task action %q", req.Action)}
	}

	// Each change is based on the version the one before it leads to.
	for i, c := range changes {
		c.meta.ExpectedVersion = req.ExpectedVersion + i
	}
	return changes, nil
}

// boardTaskEdit is the payload of an edit: the request's non-empty fields are the ones it changes.
func (o *Orchestrator) boardTaskEdit(rs state.RunState, req BoardTaskRequest) (*state.MeshBoardTaskEditedPayload, error) {
	coordinator, err := o.boardTaskParticipant(rs, req.CoordinatorID)
	if err != nil {
		return nil, err
	}
	p := &state.MeshBoardTaskEditedPayload{
		Title:         nonEmpty(o.RedactSecrets(req.Title)),
		Description:   nonEmpty(o.RedactSecrets(req.Description)),
		CoordinatorID: nonEmpty(coordinator),
	}
	if p.Title == nil && p.Description == nil && p.CoordinatorID == nil {
		return nil, &state.OperationRejectedError{Code: assistants.CodeInvalidRequest, Reason: fmt.Sprintf("an edit of task %s changes nothing", req.Key)}
	}
	return p, nil
}

// nonEmpty is a pointer to v, or nil when v is the zero value.
func nonEmpty[T comparable](v T) *T {
	var zero T
	if v == zero {
		return nil
	}
	return &v
}

// boardTaskParticipant resolves a participant id or alias to the participant's id; an empty name stays
// empty — the projection decides whether the change needs one.
func (o *Orchestrator) boardTaskParticipant(rs state.RunState, name string) (agentcomm.ParticipantID, error) {
	if name == "" {
		return "", nil
	}
	ids, err := resolveRecipients(o.liveRoster(rs), []agentcomm.ParticipantID{agentcomm.ParticipantID(name)})
	if err != nil {
		return "", err
	}
	return ids[0], nil
}

// boardTaskEffects marshals the planned changes into effects on target.
func boardTaskEffects(target state.Target, changes []boardTaskChange) ([]state.Effect, error) {
	effects := make([]state.Effect, len(changes))
	for i, c := range changes {
		e, err := marshalEffect(c.kind, target, c.payload)
		if err != nil {
			return nil, err
		}
		effects[i] = e
	}
	return effects, nil
}

// planBoardTaskStatuses fills in each change's status: the task's status after it, computed by the
// projection on a copy of rs. A change the projection refuses is refused here, with the same rejection the
// commit would give. It returns the effects with the planned statuses.
func planBoardTaskStatuses(rs state.RunState, target state.Target, changes []boardTaskChange) ([]state.Effect, error) {
	for i := range changes {
		effects, err := boardTaskEffects(target, changes[:i+1])
		if err != nil {
			return nil, err
		}
		after, err := rs.ProjectEffects(effects)
		if err != nil {
			return nil, err
		}
		changes[i].meta.Status = after.Assistants.Mesh.BoardTasks[target.BoardKey].Status()
	}
	return boardTaskEffects(target, changes)
}

// boardTaskReceipt describes the task after the last board task effect of an operation committed at seq.
func boardTaskReceipt(effects []state.Effect, seq uint64) (BoardTaskReceipt, error) {
	board := boardTaskEffectsOf(effects)
	if len(board) == 0 {
		return BoardTaskReceipt{}, errors.New("agentcomm: board task operation has no board task effect")
	}
	last := board[len(board)-1]
	// Every board task payload carries these fields under the same names.
	var p struct {
		AssignmentID string `json:"assignment_id"`
		state.MeshBoardTaskMeta
	}
	if err := json.Unmarshal(last.Payload, &p); err != nil {
		return BoardTaskReceipt{}, fmt.Errorf("decode %s payload: %w", last.Kind, err)
	}
	return BoardTaskReceipt{
		Key: last.Target.BoardKey, Version: p.ExpectedVersion + 1, Status: p.Status, AssignmentID: p.AssignmentID,
		Seq: seq, NoticeSkipped: p.NoticeSkipped,
	}, nil
}

// boardTaskEffectsOf returns the board task effects among an operation's effects, in commit order.
func boardTaskEffectsOf(effects []state.Effect) []state.Effect {
	var out []state.Effect
	for _, e := range effects {
		if strings.HasPrefix(e.Kind, boardTaskEffectPrefix) {
			out = append(out, e)
		}
	}
	return out
}

// replayedBoardTask answers a request whose id already committed with the receipt of that operation. The
// request must plan the same board task effects as the committed ones — apart from the planned status and
// notice, which depend on the state the original request met — or it is a request id conflict.
// replayed=false: nothing committed under the id.
func (o *Orchestrator) replayedBoardTask(opID string, planned []state.Effect) (BoardTaskReceipt, bool, error) {
	op, committed, err := o.opts.Store.CommittedOperation(opID, opKindMeshBoardTask)
	if err != nil || !committed {
		return BoardTaskReceipt{}, err != nil, err // a storage error answers the request too
	}
	same, err := o.sameBoardTaskEffects(boardTaskEffectsOf(op.Effects), planned)
	if err != nil {
		return BoardTaskReceipt{}, true, err
	}
	if !same {
		return BoardTaskReceipt{}, true, state.OperationIDConflictError(opID)
	}
	receipt, err := boardTaskReceipt(op.Effects, op.Result.Seq)
	return receipt, true, err
}

// sameBoardTaskEffects reports whether committed and planned board task effects are the same changes: the
// same kinds and targets, and payloads equal once the planned status and notice are left out and the text in
// them is redacted with the current secret set (see sameRedactedText).
func (o *Orchestrator) sameBoardTaskEffects(committed, planned []state.Effect) (bool, error) {
	if len(committed) != len(planned) {
		return false, nil
	}
	for i := range committed {
		if committed[i].Kind != planned[i].Kind || committed[i].Target != planned[i].Target {
			return false, nil
		}
		a, err := o.comparableBoardTaskPayload(committed[i].Payload)
		if err != nil {
			return false, err
		}
		b, err := o.comparableBoardTaskPayload(planned[i].Payload)
		if err != nil {
			return false, err
		}
		if !reflect.DeepEqual(a, b) {
			return false, nil
		}
	}
	return true, nil
}

// comparableBoardTaskPayload decodes a board task payload for sameBoardTaskEffects: without the planned
// status and notice, with every text redacted.
func (o *Orchestrator) comparableBoardTaskPayload(raw json.RawMessage) (map[string]any, error) {
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("decode board task payload: %w", err)
	}
	delete(fields, "status")
	delete(fields, "notice_skipped")
	for name, v := range fields {
		switch v := v.(type) {
		case string:
			fields[name] = o.RedactSecrets(v)
		case []any:
			for j, item := range v {
				if s, ok := item.(string); ok {
					v[j] = o.RedactSecrets(s)
				}
			}
		default:
			// Versions and flags compare as they are.
		}
	}
	return fields, nil
}

// BoardTasks returns the typed board tasks sorted by key — only the one under key when it is set (not_found
// when there is none). A run without the typed protocol has none.
func (o *Orchestrator) BoardTasks(ctx context.Context, key string) ([]agentcomm.BoardTaskView, error) {
	tasks := o.opts.Store.Snapshot().Assistants.Mesh.BoardTasks
	if key != "" {
		t, ok := tasks[key]
		if !ok {
			return nil, &state.OperationRejectedError{Code: assistants.CodeNotFound, Reason: fmt.Sprintf("board task %s does not exist", key)}
		}
		return []agentcomm.BoardTaskView{agentcomm.ViewOf(*t)}, nil
	}
	keys := slices.Sorted(maps.Keys(tasks))
	views := make([]agentcomm.BoardTaskView, 0, len(keys))
	for _, k := range keys {
		views = append(views, agentcomm.ViewOf(*tasks[k]))
	}
	return views, nil
}
