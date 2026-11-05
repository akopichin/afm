package orchestrator

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/prompts"
	"github.com/akopichin/afm/pkg/state"
)

// SpawnHelperRequest is what a stage agent chooses for a helper: profile, title, instruction and trigger.
// The stage, scope, mode and executor are resolved on the server, never by the caller.
type SpawnHelperRequest struct {
	OperationID string
	ProfileID   assistants.ProfileID
	Title       string
	Instruction string
	Trigger     assistants.Trigger // "" or one_shot: a one-shot helper; on_message: a helper that waits for requests
	Actor       state.Actor        // the stage agent: Kind "agent", ID "stage:<id>"
}

// SpawnHelper starts a helper for the stage actor through AttachAndSend, so the task, its participant, the
// thread and the stage's request to it are ONE commit under the existing limits (quota, depth, profile
// availability, a stage that has not finished). The returned message id is the request the helper answers:
// the stage can wait on it like on any other request (reply_to / reply_to_all), and the helper's result comes
// back as a reply to it (finishing a helper of OriginStageAgent, runHelperTask), or the wait ends with the
// helper's failure reason. An on_message helper is created resting, with no request: the agent sends its first
// message itself, so the result carries no message id.
func (o *Orchestrator) SpawnHelper(ctx context.Context, req SpawnHelperRequest) (AttachAndSendResult, error) {
	stageID, ok := stageAgentOf(req.Actor)
	if !ok {
		return AttachAndSendResult{}, attachRejected(assistants.CodeActorNotAllowed, "only a stage agent can spawn a helper, not %s %q", req.Actor.Kind, req.Actor.ID)
	}
	instruction := strings.TrimSpace(req.Instruction)
	if instruction == "" {
		return AttachAndSendResult{}, attachRejected(assistants.CodeInvalidRequest, "an instruction is required")
	}
	stage := meshStageParticipantID(stageID)
	attach := AttachAndSendRequest{
		OperationID: req.OperationID, ProfileID: req.ProfileID, Scope: assistants.ScopeStage, StageID: stageID,
		Mode: assistants.ModeAdvisory, Title: req.Title, Instruction: req.Instruction,
		Recipients: []agentcomm.ParticipantID{stage}, Origin: assistants.OriginStageAgent, Actor: req.Actor,
	}
	switch req.Trigger {
	case assistants.TriggerOnMessage:
		attach.Trigger = assistants.TriggerOnMessage
	case "", assistants.TriggerOneShot:
		// The request the helper answers is its instruction; the task keeps the full text.
		request := prompts.TruncateUTF8(instruction, agentcomm.MaxBodyBytes, "…")
		attach.FirstMessage, attach.FirstKind, attach.Sender = &request, agentcomm.MessageKindRequest, stage
	default:
		return AttachAndSendResult{}, attachRejected(assistants.CodeInvalidRequest, "a spawned helper's trigger must be %q or %q, got %q", assistants.TriggerOneShot, assistants.TriggerOnMessage, req.Trigger)
	}
	return o.AttachAndSend(ctx, attach)
}

// stageAgentOf is the stage whose agent actor is: ok only for the mesh identity "stage:<id>" of an agent.
func stageAgentOf(actor state.Actor) (stageID string, ok bool) {
	participant := agentcomm.ParticipantID(actor.ID)
	stageID, isStage := stageIDFromMeshParticipant(participant)
	return stageID, isStage && actor == boardTaskActor(participant)
}

// HelperRequestState is how one request to an on_message helper stands.
type HelperRequestState string

const (
	HelperRequestPending HelperRequestState = "pending" // queued behind the request being worked on
	HelperRequestActive  HelperRequestState = "active"  // a turn works on it now
	HelperRequestResult  HelperRequestState = "result"  // answered: the turn committed a result for it
	HelperRequestFailed  HelperRequestState = "failed"  // its turn failed: no result, Reason says why
)

// HelperRequestView is one request of an on_message helper: its state and the turn that took it (0 while it
// waits). Reason is set only for a failed request.
type HelperRequestView struct {
	ID     agentcomm.MessageID
	State  HelperRequestState
	Turn   uint64
	Reason string
}

// HelperResultView is how a helper stands now: its status and, once it finished with a result, the last result
// the journal accepted. Result is nil while the helper runs and for a helper that failed or was cancelled — the
// StatusReason then says why. Request is set only when one request of an on_message helper was asked for; Result
// is then that request's answer, not the helper's last one.
type HelperResultView struct {
	TaskID       assistants.TaskID
	Title        string
	Status       assistants.TaskStatus
	StatusReason string
	Generation   uint64
	Request      *HelperRequestView
	Result       json.RawMessage
}

// HelperResult reads the helper taskID of the stage agent actor: one it spawned or an on_message helper of its
// stage (stageOwnHelper). Any other task and an id that does not exist are the same NotFound, so the answer does
// not tell whether a task of someone else exists. The result is the task's last COMMITTED one, never result.json, which may hold an
// answer the journal did not accept. The status and the result are read from Store.TaskAndEvents in ONE lock
// acquisition: two separate Store reads could let a finish commit land between them and pair a stale (not yet
// terminal) status with the result it just committed.
//
// A non-empty requestID narrows the answer to that request of an on_message helper. Only the helper's own
// journal is searched, so the id of someone else's request is as unknown as a mistyped one.
func (o *Orchestrator) HelperResult(actor state.Actor, taskID assistants.TaskID, requestID agentcomm.MessageID) (HelperResultView, error) {
	stageID, ok := stageAgentOf(actor)
	if !ok {
		return HelperResultView{}, attachRejected(assistants.CodeActorNotAllowed, "only a stage agent reads the result of its helper, not %s %q", actor.Kind, actor.ID)
	}
	task, events, found := o.opts.Store.TaskAndEvents(taskID)
	if !found || !stageOwnHelper(o.opts.Store.Snapshot(), stageID, &task) {
		return HelperResultView{}, &state.OperationRejectedError{Code: assistants.CodeNotFound, Reason: fmt.Sprintf("unknown helper %s", taskID)}
	}
	view := HelperResultView{
		TaskID: taskID, Title: task.Title, Status: task.Status, StatusReason: task.StatusReason, Generation: task.RootGeneration,
	}
	if requestID == "" {
		if raw := latestTaskResultJSON(events); raw != "" {
			view.Result = json.RawMessage(raw)
		}
		return view, nil
	}
	if !state.IsOnMessageHelper(task.Task) {
		return HelperResultView{}, attachRejected(assistants.CodeInvalidRequest, "helper %s answers one request: read it without a message id", taskID)
	}
	request, result, found := helperRequestOf(task, events, requestID)
	if !found {
		return HelperResultView{}, &state.OperationRejectedError{Code: assistants.CodeNotFound, Reason: fmt.Sprintf("helper %s has no request %s", taskID, requestID)}
	}
	view.Request, view.Result = &request, result
	return view, nil
}

// helperRequestOf finds request id in an on_message helper's queue or journal: waiting, being worked on, answered
// (with that turn's result) or failed. A turn-start record carries the id too but is not an answer.
func helperRequestOf(task state.TaskState, events []state.TaskEvent, id agentcomm.MessageID) (HelperRequestView, json.RawMessage, bool) {
	if id == "" {
		return HelperRequestView{}, nil, false
	}
	if slices.Contains(task.PendingRequests, id) {
		return HelperRequestView{ID: id, State: HelperRequestPending}, nil, true
	}
	if task.ActiveRequest == id {
		return HelperRequestView{ID: id, State: HelperRequestActive, Turn: task.Turn}, nil, true
	}
	for _, e := range slices.Backward(events) {
		switch e.Kind {
		case taskResultEffectKind:
			var p state.TaskResultPayload
			if json.Unmarshal(e.Payload, &p) == nil && p.RequestID == string(id) {
				return HelperRequestView{ID: id, State: HelperRequestResult, Turn: p.Turn}, json.RawMessage(indentJSON(e.Payload)), true
			}
		case taskStatusChangedEffectKind:
			var p state.TaskStatusChangedPayload
			if json.Unmarshal(e.Payload, &p) == nil && p.RequestID == string(id) && p.RequestOutcome == state.RequestOutcomeFailed {
				return HelperRequestView{ID: id, State: HelperRequestFailed, Turn: p.Turn, Reason: p.Reason}, nil, true
			}
		default:
		}
	}
	return HelperRequestView{}, nil, false
}

// maxListedHelpers bounds the <your_helpers> block, which repeats on every turn of the stage.
const maxListedHelpers = 20

// stageHelpersBlock renders the <your_helpers> block of stage stageID: its own helpers (stageOwnHelper), oldest
// first, from the durable tasks (id, title and status only) of the stage's CURRENT root generation. The newest
// ones are dropped when there are too many.
func stageHelpersBlock(rs state.RunState, stageID string) string {
	var mine []*state.TaskState
	for _, t := range rs.Assistants.Tasks {
		if stageOwnHelper(rs, stageID, t) {
			mine = append(mine, t)
		}
	}
	slices.SortFunc(mine, func(a, b *state.TaskState) int {
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), cmp.Compare(a.ID, b.ID))
	})
	omitted := max(0, len(mine)-maxListedHelpers)
	mine = mine[:len(mine)-omitted]
	helpers := make([]prompts.SpawnedHelper, len(mine))
	for i, t := range mine {
		helpers[i] = prompts.SpawnedHelper{TaskID: string(t.ID), Title: t.Title, Status: string(t.Status), OnMessage: state.IsOnMessageHelper(t.Task)}
	}
	return prompts.AgentCommHelpersBlock(helpers, omitted)
}
