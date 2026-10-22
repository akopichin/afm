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
	"github.com/akopichin/afm/pkg/prompts"
	"github.com/akopichin/afm/pkg/state"
)

// Typed board tasks (agentcomm.BoardTask). Every rule of a change — who may make it, in which state, on which
// version — lives in the projection (pkg/state/mesh_board_tasks.go). BoardTask only turns a request into the
// change's effects, plans the task the change leads to by running that same projection on a copy, and lets
// ApplyRunOperation decide.

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
	To string
	// Title, Description and CoordinatorID (the coordinator's id or alias) describe the task on a create; an
	// edit changes only the ones that are set, and an empty description or coordinator clears it.
	Title         *string
	Description   *string
	CoordinatorID *string
	// Text is the progress text, the done result or the release/reopen reason.
	Text string
	// ArtifactRefs of a progress or done replace the assignment's refs; an empty list clears them, nil keeps them.
	ArtifactRefs *[]string
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
//
// An assignment to another participant commits together with the message that tells it about the task; when
// that message cannot be delivered now, the assignment is refused. A done, release or reopen by anyone but the
// task's coordinator notifies the coordinator in the same commit; a notice that cannot be delivered now is
// skipped (the receipt's NoticeSkipped says why) and never refuses the change.
func (o *Orchestrator) BoardTask(ctx context.Context, req BoardTaskRequest) (BoardTaskReceipt, error) {
	// A message to the Side agent commits before SetSideAgentAvailable can close its gate (see Publish).
	o.sideAgentGate.RLock()
	defer o.sideAgentGate.RUnlock()
	if req.OperationID == "" {
		return BoardTaskReceipt{}, errors.New("agentcomm: operation id is required")
	}
	if req.Actor == "" {
		return BoardTaskReceipt{}, &state.OperationRejectedError{Code: assistants.CodeInvalidRequest, Reason: "a board task change needs an actor"}
	}
	actor := boardTaskActor(req.Actor)
	if err := rejectReservedRequestID(req.OperationID, actor); err != nil {
		return BoardTaskReceipt{}, err
	}
	if !boardTasksEnabled(o.opts.Store.Snapshot()) {
		return BoardTaskReceipt{}, &state.OperationRejectedError{Code: assistants.CodeInvalidStatus, Reason: "board tasks are not enabled for this run"}
	}
	runCtx := o.runContext(ctx) // a recipient's turn must not die with the HTTP request.

	const maxAttempts = 3
	for attempt := 1; ; attempt++ {
		if receipt, replayed, err := o.replayedBoardTask(req); replayed {
			return receipt, err
		}
		if o.testAfterBoardTaskLookup != nil {
			o.testAfterBoardTaskLookup()
		}
		// Checked after the replay lookup: a retry of a committed request always gets its stored receipt.
		// The key names the task in the journal, prompts and messages and is never redacted, so it must hold no secret.
		if o.RedactSecrets(req.Key) != req.Key {
			return BoardTaskReceipt{}, &state.OperationRejectedError{Code: assistants.CodeInvalidRequest, Reason: "the task key contains a secret value"}
		}
		plan, err := o.planBoardTask(o.opts.Store.Snapshot(), req)
		if err != nil {
			return o.answerFailedBoardTask(req, err)
		}
		res, err := o.commitBoardTask(runCtx, req, plan)
		if err == nil {
			return boardTaskReceipt(plan.effects, res.Seq)
		}
		replan := func(rs state.RunState) ([]plannedMessage, error) {
			again, err := o.planBoardTask(rs, req)
			return again.msgs, err
		}
		if attempt == maxAttempts || !o.worthAnotherAttempt(req.OperationID, err, plan.msgs, replan) {
			return o.answerFailedBoardTask(req, err)
		}
	}
}

// answerFailedBoardTask answers a request whose planning or commit failed with err. A duplicate of the request
// under the same id may have committed after this one's lookup — its planning then met the state after that
// commit — so the committed operation, when there is one, answers instead: with its receipt, or a request id
// conflict.
func (o *Orchestrator) answerFailedBoardTask(req BoardTaskRequest, err error) (BoardTaskReceipt, error) {
	if receipt, replayed, lookupErr := o.replayedBoardTask(req); replayed {
		return receipt, lookupErr
	}
	return BoardTaskReceipt{}, err
}

// commitBoardTask commits plan as req's operation. Under the commit's lock it checks again that the new
// assignee, if any, can still take work and that the plan's messages can be delivered: either may have changed
// since the plan was made.
func (o *Orchestrator) commitBoardTask(runCtx context.Context, req BoardTaskRequest, plan boardTaskPlan) (state.OperationResult, error) {
	reachable := o.recipientsReachable(plan.msgs)
	check := func(rs *state.RunState) error {
		if plan.assignee != "" {
			if err := assignableParticipant(*rs, plan.assignee); err != nil {
				return err
			}
		}
		return reachable(rs)
	}
	op := state.Operation{ID: req.OperationID, Kind: opKindMeshBoardTask, Actor: boardTaskActor(req.Actor), Effects: plan.effects, Expect: state.Preconditions{
		Check: check,
	}}
	return o.opts.Store.ApplyRunOperation(op, func(res state.OperationResult) {
		o.deliverCommittedMessages(runCtx, plan.msgs, res.ReadyWaits)
	})
}

// boardTaskPlan is what one attempt of a board task change commits.
type boardTaskPlan struct {
	effects  []state.Effect          // the message's effects, then the task's
	msgs     []plannedMessage        // the one message the change sends, if any
	assignee agentcomm.ParticipantID // the participant a new assignment goes to, if the change makes one
}

// planBoardTask plans req against rs: the task's effects — refused here when the projection refuses them, the
// last one annotated with the task's status after them — and the message the change sends.
func (o *Orchestrator) planBoardTask(rs state.RunState, req BoardTaskRequest) (boardTaskPlan, error) {
	resolve := func(name string) (agentcomm.ParticipantID, error) { return o.boardTaskParticipant(rs, name) }
	changes, err := o.planBoardTaskChanges(req, resolve)
	if err != nil {
		return boardTaskPlan{}, err
	}
	target := state.Target{BoardKey: req.Key, ParticipantID: string(req.Actor)}
	after, err := projectBoardTask(rs, target, changes)
	if err != nil {
		return boardTaskPlan{}, err
	}
	last := changes[len(changes)-1]
	last.meta.Status = after.Status()
	var plan boardTaskPlan
	if a, ok := last.payload.(*state.MeshBoardTaskAssignedPayload); ok {
		if err := assignableParticipant(rs, a.ParticipantID); err != nil {
			return boardTaskPlan{}, err
		}
		plan.assignee = a.ParticipantID
	}
	msgs, err := o.planBoardTaskMessage(rs, req, last, after)
	if err != nil {
		return boardTaskPlan{}, err
	}
	taskEffects, err := boardTaskEffects(target, changes)
	if err != nil {
		return boardTaskPlan{}, err
	}
	for _, m := range msgs {
		plan.effects = append(plan.effects, m.effects...)
	}
	plan.effects = append(plan.effects, taskEffects...)
	plan.msgs = msgs
	return plan, nil
}

// planBoardTaskMessage plans the message a change sends: to the new assignee when the actor assigns someone
// else (a claim or a self-assign sends nothing), or to the coordinator on a done, release or reopen by anyone
// else. last is the change's last effect and after the task after the change. A skipped coordinator notice is
// recorded in last's NoticeSkipped.
func (o *Orchestrator) planBoardTaskMessage(rs state.RunState, req BoardTaskRequest, last boardTaskChange, after agentcomm.BoardTask) ([]plannedMessage, error) {
	switch p := last.payload.(type) {
	case *state.MeshBoardTaskAssignedPayload:
		if p.ParticipantID == req.Actor {
			return nil, nil
		}
		return o.planAssignmentMessage(rs, req, *p, after)
	case *state.MeshBoardTaskCompletedPayload, *state.MeshBoardTaskReleasedPayload, *state.MeshBoardTaskReopenedPayload:
		return o.planCoordinatorNotice(rs, req, last.meta)
	default:
		return nil, nil
	}
}

// planAssignmentMessage plans the message that hands the task to its new assignee. An assignment nobody would
// learn about is refused: the assignee must be able to receive the message now.
func (o *Orchestrator) planAssignmentMessage(rs state.RunState, req BoardTaskRequest, a state.MeshBoardTaskAssignedPayload, after agentcomm.BoardTask) ([]plannedMessage, error) {
	// The title is left out: it is the task author's text, and this message may come from the user. The
	// assignee reads it in <your_board_tasks>, marked as data.
	body := o.messageBody(fmt.Sprintf("Board task %s assigned to you (assignment %s). "+
		"Run `afm agent task start --key %s --assignment-id %s --expected-version %d` when you begin, `done` when finished.",
		req.Key, a.AssignmentID, req.Key, a.AssignmentID, after.Version))
	msg, skip, err := o.planNoticeTo(rs, req.OperationID, req.Actor, a.ParticipantID, body)
	if err != nil {
		return nil, err
	}
	if skip != "" {
		return nil, &state.OperationRejectedError{Code: assistants.CodeInvalidStatus, Reason: fmt.Sprintf("task %s cannot be assigned to %s now: %s", req.Key, a.ParticipantID, skip)}
	}
	return []plannedMessage{msg}, nil
}

// planCoordinatorNotice plans the notice of a done, release or reopen to the task's coordinator — none when
// there is no coordinator or it made the change itself. A notice that cannot be delivered now is skipped:
// meta.NoticeSkipped says why.
func (o *Orchestrator) planCoordinatorNotice(rs state.RunState, req BoardTaskRequest, meta *state.MeshBoardTaskMeta) ([]plannedMessage, error) {
	task := rs.Assistants.Mesh.BoardTasks[req.Key]
	if task == nil {
		return nil, nil
	}
	coordinator := task.Coordinator()
	if coordinator == "" || coordinator == req.Actor {
		return nil, nil
	}
	body := o.messageBody(coordinatorNoticeBody(*task, req))
	msg, skip, err := o.planNoticeTo(rs, req.OperationID, req.Actor, coordinator, body)
	if err != nil {
		return nil, err
	}
	if skip != "" {
		meta.NoticeSkipped = skip
		return nil, nil
	}
	return []plannedMessage{msg}, nil
}

// coordinatorNoticeBody tells the coordinator which assignments of task (as it is before req) req ended and how:
// "Board task <key>: assignment <id> of <participant> is done: <result>".
func coordinatorNoticeBody(task agentcomm.BoardTask, req BoardTaskRequest) string {
	var ended []agentcomm.BoardTaskAssignment
	outcome := "reopened"
	switch req.Action {
	case BoardTaskDone, BoardTaskRelease:
		if a, ok := task.Assignment(req.AssignmentID); ok {
			ended = append(ended, a)
		}
		outcome = "done"
		if req.Action == BoardTaskRelease {
			outcome = "released"
		}
	default:
		ended = task.Assignments
	}

	parts := make([]string, len(ended))
	for i, a := range ended {
		parts[i] = fmt.Sprintf("assignment %s of %s", a.ID, a.ParticipantID)
	}
	var body string
	switch len(parts) {
	case 0:
		body = fmt.Sprintf("Board task %s is %s", task.Key, outcome)
	case 1:
		body = fmt.Sprintf("Board task %s: %s is %s", task.Key, parts[0], outcome)
	default:
		body = fmt.Sprintf("Board task %s: %s are %s", task.Key, strings.Join(parts, ", "), outcome)
	}
	if req.Text != "" {
		body += ": " + req.Text
	}
	return body
}

// messageBody makes body fit a message: secrets are redacted before the cut to agentcomm.MaxBodyBytes, so a cut
// never leaves part of a secret behind.
func (o *Orchestrator) messageBody(body string) string {
	return prompts.TruncateUTF8(o.RedactSecrets(body), agentcomm.MaxBodyBytes, "…")
}

// assignableParticipant checks that id can take a new assignment now: a known agent that can receive
// messages, is not resting in a state no message wakes it from and has the board task protocol in its context
// (the Assistant has not).
func assignableParticipant(rs state.RunState, id agentcomm.ParticipantID) error {
	p, ok := resolveParticipant(rs, id)
	switch {
	case !ok:
		return &state.OperationRejectedError{Code: assistants.CodeNotFound, Reason: fmt.Sprintf("%s is not in this run", id)}
	case p.Kind == agentcomm.KindUser:
		return &state.OperationRejectedError{Code: assistants.CodeInvalidRequest, Reason: "a board task cannot be assigned to the user"}
	case p.Kind == agentcomm.KindSideAgent:
		return &state.OperationRejectedError{Code: assistants.CodeInvalidStatus, Reason: fmt.Sprintf("%s: the Assistant does not take board tasks", id)}
	case !p.Reachable:
		return &state.OperationRejectedError{Code: assistants.CodeInvalidStatus, Reason: fmt.Sprintf("%s cannot take work (%s)", id, p.UnreachableReason)}
	}
	if taskID, ok := participantTask(rs, id); ok {
		if t := rs.Assistants.Tasks[taskID]; t != nil && (t.Status == assistants.StatusWatching || t.Status == assistants.StatusWaitingParent) {
			return &state.OperationRejectedError{Code: assistants.CodeInvalidStatus, Reason: fmt.Sprintf("%s is %s: passive helper is not woken for new work", id, t.Status)}
		}
	}
	if stageID, ok := stageIDFromMeshParticipant(id); ok && rs.Stages[stageID].Status == state.StatusAwaitingAssistants {
		return &state.OperationRejectedError{Code: assistants.CodeInvalidStatus, Reason: fmt.Sprintf("stage %s has finished its own work and waits for its assistants", stageID)}
	}
	return nil
}

// boardTaskActor is the journal actor of a change made by participant p.
func boardTaskActor(p agentcomm.ParticipantID) state.Actor {
	if p == agentcomm.UserParticipantID {
		return state.Actor{Kind: state.ActorKindUser}
	}
	return state.Actor{Kind: "agent", ID: string(p)}
}

// boardTaskChange is one board task effect being planned: its kind and a pointer to its payload, with the
// payload's embedded meta, where the expected version (and, on the last change, the planned status) is filled in.
type boardTaskChange struct {
	kind    string
	payload any
	meta    *state.MeshBoardTaskMeta
}

// planBoardTaskChanges maps req to the board task effects it makes, in commit order. Text is redacted and
// participant names resolved here by resolve; whether the change is allowed is the projection's decision. A
// new assignment's id is derived from the request id, so a retry plans the same one.
func (o *Orchestrator) planBoardTaskChanges(req BoardTaskRequest, resolve func(name string) (agentcomm.ParticipantID, error)) ([]boardTaskChange, error) {
	newAssignmentID := deriveMeshID("board-assignment", req.OperationID)
	text, refs := o.RedactSecrets(req.Text), o.redactRefs(req.ArtifactRefs)

	var changes []boardTaskChange
	switch req.Action {
	case BoardTaskCreate:
		coordinator, err := resolve(valueOf(req.CoordinatorID))
		if err != nil {
			return nil, err
		}
		p := &state.MeshBoardTaskCreatedPayload{
			Title: o.RedactSecrets(valueOf(req.Title)), Description: o.RedactSecrets(valueOf(req.Description)),
			CoordinatorID: coordinator, RequestedCoordinator: valueOf(req.CoordinatorID),
		}
		changes = append(changes, boardTaskChange{"mesh_board_task_created", p, &p.MeshBoardTaskMeta})
		if req.To != "" {
			assignee, err := resolve(req.To)
			if err != nil {
				return nil, err
			}
			a := &state.MeshBoardTaskAssignedPayload{AssignmentID: newAssignmentID, ParticipantID: assignee, RequestedTo: req.To}
			changes = append(changes, boardTaskChange{"mesh_board_task_assigned", a, &a.MeshBoardTaskMeta})
		}
	case BoardTaskAssign:
		assignee, err := resolve(req.To)
		if err != nil {
			return nil, err
		}
		p := &state.MeshBoardTaskAssignedPayload{AssignmentID: newAssignmentID, ParticipantID: assignee, RequestedTo: req.To}
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
		p, err := o.boardTaskEdit(req, resolve)
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

// boardTaskEdit is the payload of an edit: the request's set fields are the ones it changes.
func (o *Orchestrator) boardTaskEdit(req BoardTaskRequest, resolve func(name string) (agentcomm.ParticipantID, error)) (*state.MeshBoardTaskEditedPayload, error) {
	if req.Title == nil && req.Description == nil && req.CoordinatorID == nil {
		return nil, &state.OperationRejectedError{Code: assistants.CodeInvalidRequest, Reason: fmt.Sprintf("an edit of task %s changes nothing", req.Key)}
	}
	p := &state.MeshBoardTaskEditedPayload{Title: o.redactSet(req.Title), Description: o.redactSet(req.Description)}
	if req.CoordinatorID != nil {
		coordinator, err := resolve(*req.CoordinatorID) // an empty name stays empty: the coordinator is cleared
		if err != nil {
			return nil, err
		}
		p.CoordinatorID, p.RequestedCoordinator = &coordinator, req.CoordinatorID
	}
	return p, nil
}

// redactSet is the redacted text, or nil when it is not set.
func (o *Orchestrator) redactSet(text *string) *string {
	if text == nil {
		return nil
	}
	return new(o.RedactSecrets(*text))
}

// redactRefs is the redacted artifact refs, or nil when they are not set; a set empty list stays an empty list.
func (o *Orchestrator) redactRefs(refs *[]string) *[]string {
	if refs == nil {
		return nil
	}
	out := make([]string, len(*refs))
	for i, ref := range *refs {
		out[i] = o.RedactSecrets(ref)
	}
	return &out
}

// valueOf is *s, or "" when s is nil.
func valueOf(s *string) string {
	if s == nil {
		return ""
	}
	return *s
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

// projectBoardTask returns the task as it would be after changes, computed by the projection on a copy of rs.
// A change the projection refuses is refused here, with the same rejection the commit would give.
func projectBoardTask(rs state.RunState, target state.Target, changes []boardTaskChange) (agentcomm.BoardTask, error) {
	effects, err := boardTaskEffects(target, changes)
	if err != nil {
		return agentcomm.BoardTask{}, err
	}
	after, err := rs.ProjectEffects(effects)
	if err != nil {
		return agentcomm.BoardTask{}, err
	}
	return *after.Assistants.Mesh.BoardTasks[target.BoardKey], nil
}

// boardTaskReceipt describes the task after the last board task effect of an operation committed at seq, and
// the message the operation sent, if any.
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
	receipt := BoardTaskReceipt{
		Key: last.Target.BoardKey, Version: p.ExpectedVersion + 1, Status: p.Status, AssignmentID: p.AssignmentID,
		Seq: seq, NoticeSkipped: p.NoticeSkipped,
	}
	for _, e := range effects {
		if e.Kind == "mesh_message_sent" {
			receipt.MessageID, receipt.ThreadID = agentcomm.MessageID(e.Target.MessageID), agentcomm.ThreadID(e.Target.ThreadID)
		}
	}
	return receipt, nil
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
// notice, which depend on the state the original request met — or it is a request id conflict. The participants
// it names are compared as written with the names the original request wrote (requested_to,
// requested_coordinator), never resolved against today's roster, where a name may stand for someone else now.
// replayed=false: nothing committed under the id.
func (o *Orchestrator) replayedBoardTask(req BoardTaskRequest) (BoardTaskReceipt, bool, error) {
	op, committed, err := o.opts.Store.CommittedOperation(req.OperationID, opKindMeshBoardTask)
	if err != nil || !committed {
		return BoardTaskReceipt{}, err != nil, err // a storage error answers the request too
	}
	asWritten := func(name string) (agentcomm.ParticipantID, error) { return agentcomm.ParticipantID(name), nil }
	changes, err := o.planBoardTaskChanges(req, asWritten)
	if err != nil {
		return BoardTaskReceipt{}, true, err
	}
	planned, err := boardTaskEffects(state.Target{BoardKey: req.Key, ParticipantID: string(req.Actor)}, changes)
	if err != nil {
		return BoardTaskReceipt{}, true, err
	}
	same, err := o.sameBoardTaskEffects(boardTaskEffectsOf(op.Effects), planned)
	if err != nil {
		return BoardTaskReceipt{}, true, err
	}
	if !same {
		return BoardTaskReceipt{}, true, state.OperationIDConflictError(req.OperationID)
	}
	receipt, err := boardTaskReceipt(op.Effects, op.Result.Seq)
	return receipt, true, err
}

// sameBoardTaskEffects reports whether committed and planned board task effects are the same changes: the
// same kinds and targets, and payloads equal once the planned status and notice and the resolved participants
// are left out and the text in them is redacted with the current secret set (see sameRedactedText).
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
// status and notice and the resolved participant ids (the names as written stay), with every text redacted.
func (o *Orchestrator) comparableBoardTaskPayload(raw json.RawMessage) (map[string]any, error) {
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("decode board task payload: %w", err)
	}
	for _, planned := range []string{"status", "notice_skipped", "participant_id", "coordinator_id"} {
		delete(fields, planned)
	}
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
