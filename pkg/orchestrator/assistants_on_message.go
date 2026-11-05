package orchestrator

import (
	"context"
	"errors"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/state"
)

// stageOwnHelper reports whether t works for the current attempt of stage stageID and answers its agent: a
// one-shot helper the agent spawned, or any on_message helper of the stage — declared, attached or spawned.
func stageOwnHelper(rs state.RunState, stageID string, t *state.TaskState) bool {
	if t.RootStageID != stageID || t.RootGeneration != rs.Assistants.RootGenerations[stageID] {
		return false
	}
	return (t.CreatedBy == assistants.OriginStageAgent && !isWatcherTrigger(t.Trigger)) || state.IsOnMessageHelper(t.Task)
}

// rejectOnMessageTopLevel refuses an on_message top-level task that has no stage to serve yet: a run agent
// already answers every message, and a pending stage has nobody to send requests.
func rejectOnMessageTopLevel(trigger assistants.Trigger, scope assistants.Scope, stageID string, stageStarted bool) error {
	switch {
	case trigger != assistants.TriggerOnMessage:
		return nil
	case scope == assistants.ScopeRun:
		return attachRejected(assistants.CodeInvalidRequest, "trigger on_message is for a stage's helper — a run agent already answers every message")
	case !stageStarted:
		return attachRejected(assistants.CodeInvalidStatus, "stage %q has not started yet — attach an on_message helper once it runs", stageID)
	default:
		return nil
	}
}

// rejectOnMessageDelegation refuses on_message below a stage's top level: a delegated child is a single piece of
// its parent's work, and the parent resumes when its children finish, not on a message.
func rejectOnMessageDelegation(children []assistants.DelegateRequest, continuation *assistants.Continuation) error {
	for i, child := range children {
		if child.Trigger == assistants.TriggerOnMessage {
			return attachRejected(assistants.CodeInvalidRequest, "children[%d]: trigger on_message is only for a stage's top-level helper", i)
		}
	}
	if continuation != nil && continuation.Trigger == assistants.TriggerOnMessage {
		return attachRejected(assistants.CodeInvalidRequest, "continuation trigger on_message is not supported — a delegating task resumes when its children finish")
	}
	return nil
}

// onMessageTurnRequest is the request a turn of on_message helper t works on: the active one, which a turn after a
// yield, delegation, Pause or crash keeps, or else the oldest waiting one. "" for any other task.
func onMessageTurnRequest(t *state.TaskState) agentcomm.MessageID {
	switch {
	case !state.IsOnMessageHelper(t.Task):
		return ""
	case t.ActiveRequest != "":
		return t.ActiveRequest
	case len(t.PendingRequests) > 0:
		return t.PendingRequests[0]
	default:
		return ""
	}
}

// onMessageRequestDue reports whether on_message helper t has a request to work on for the current attempt of its
// stage.
func onMessageRequestDue(rs state.RunState, t *state.TaskState) bool {
	return t != nil && !t.Status.Terminal() && onMessageTurnRequest(t) != "" &&
		t.RootGeneration == rs.Assistants.RootGenerations[t.RootStageID]
}

// onMessageWakeReason is the status reason of an on_message helper put in the queue for its next request.
const onMessageWakeReason = "request waiting"

// errNoRequestDue rejects waking or starting an on_message helper that has no request to work on.
var errNoRequestDue = errors.New("assistants: the on_message helper has no request to work on")

// errOnMessageGated rejects starting a turn of an on_message helper whose stage paused or moved on since the decision.
var errOnMessageGated = errors.New("assistants: the stage of this on_message helper is paused or superseded")

// errOnMessageNotClosable rejects closing an on_message helper that got a request or lost its reason to close.
var errOnMessageNotClosable = errors.New("assistants: the on_message helper can no longer be closed")

const (
	// onMessageClosedReasonPrefix opens the status reason of an on_message helper the system closed.
	onMessageClosedReasonPrefix = "closed: "
	// onMessageStagePausedReason is the status reason of a turn put back in the queue because its stage paused.
	onMessageStagePausedReason = "re-queued: stage paused"
)

// onMessageGated reports whether on_message helper t may not start a turn now: its stage is paused, or the helper
// serves a superseded attempt of it. The requests wait; the stage's Continue starts them (resumeOnMessageHelpers).
func onMessageGated(rs state.RunState, t *state.TaskState) bool {
	if t == nil || !state.IsOnMessageHelper(t.Task) {
		return false
	}
	return t.RootGeneration != rs.Assistants.RootGenerations[t.RootStageID] || rs.Stages[t.RootStageID].Status == state.StatusPaused
}

// onMessageStartBlocked is why on_message helper t may not start a turn now, nil when it may or t is another task.
func onMessageStartBlocked(rs state.RunState, t *state.TaskState) error {
	switch {
	case t == nil || !state.IsOnMessageHelper(t.Task):
		return nil
	case state.OnMessageIdle(*t):
		return errNoRequestDue
	case onMessageGated(rs, t):
		return errOnMessageGated
	default:
		return nil
	}
}

// onMessageCloseReason is why the system may close on_message helper taskID now, "" while it may not. Only a helper
// resting with no request for the current attempt of its stage is closed: when the stage is done, or when it awaits
// its helpers and this one is required and passed its last request.
func onMessageCloseReason(rs state.RunState, taskID assistants.TaskID) string {
	t := rs.Assistants.Tasks[taskID]
	if t == nil || t.Status != assistants.StatusWatching || !state.OnMessageIdle(*t) ||
		t.RootGeneration != rs.Assistants.RootGenerations[t.RootStageID] {
		return ""
	}
	switch rs.Stages[t.RootStageID].Status {
	case state.StatusDone:
		return onMessageClosedReasonPrefix + "stage " + t.RootStageID + " is done"
	case state.StatusAwaitingAssistants:
		if t.Mode == assistants.ModeRequired && t.LastOutcome == state.RequestOutcomeResult && t.LastVerdict == assistants.VerdictPass {
			return onMessageClosedReasonPrefix + "the last request passed and stage " + t.RootStageID + " awaits its helpers"
		}
		return ""
	default:
		return ""
	}
}

// settleOnMessageHelpers closes the on_message helpers of stageID that its status lets go (onMessageCloseReason).
func (o *Orchestrator) settleOnMessageHelpers(stageID string) {
	if o.opts.Store == nil {
		return
	}
	for _, id := range o.opts.Store.Snapshot().Assistants.ByRootStage[stageID] {
		o.settleOnMessageTask(id)
	}
}

// settleOnMessageTask closes on_message helper taskID once its stage no longer needs it, with no model call or
// result: completed closes a required gate with the pass already recorded. It commits only while the reason holds,
// so a request accepted meanwhile keeps the helper.
func (o *Orchestrator) settleOnMessageTask(taskID assistants.TaskID) {
	reason := onMessageCloseReason(o.opts.Store.Snapshot(), taskID)
	if reason == "" {
		return
	}
	if o.settlePlannedHook != nil {
		o.settlePlannedHook()
	}
	stillClosable := func(cur *state.RunState) error {
		if onMessageCloseReason(*cur, taskID) == "" {
			return errOnMessageNotClosable
		}
		return nil
	}
	_ = o.finishTaskTurnReplyingGuarded(taskID, 0, assistants.StatusWatching, assistants.StatusCompleted, reason, nil, stillClosable, nil)
}

// resumeOnMessageHelpers starts the on_message helpers of stageID that have a request waiting: the stage held them
// while it was paused.
func (o *Orchestrator) resumeOnMessageHelpers(ctx context.Context, stageID string) {
	rs := o.opts.Store.Snapshot()
	for _, id := range rs.Assistants.ByRootStage[stageID] {
		t := rs.Assistants.Tasks[id]
		if t == nil || !state.IsOnMessageHelper(t.Task) {
			continue
		}
		switch t.Status {
		case assistants.StatusWatching:
			o.wakeOnMessageTask(ctx, id)
		case assistants.StatusQueued:
			o.maybeScheduleTask(ctx, id)
		default:
			// Running or waiting: the request under way goes on by its own path; terminal: nothing to start.
		}
	}
}

// wakeOnMessageTask puts on_message helper taskID, resting in watching with a request due, in the queue and schedules
// it. Every commit that can give it work calls it — a message to it, the end of its request, a Continue — and the
// CAS on watching lets one of them win.
func (o *Orchestrator) wakeOnMessageTask(ctx context.Context, taskID assistants.TaskID) {
	rs := o.opts.Store.Snapshot()
	t, ok := rs.Assistants.Tasks[taskID]
	if !ok || t.Status != assistants.StatusWatching || !onMessageRequestDue(rs, t) {
		return
	}
	eff, err := taskStatusEffect(taskID, assistants.StatusQueued, onMessageWakeReason)
	if err != nil {
		return
	}
	op := state.Operation{
		ID:   "task-wake:" + string(taskID) + ":" + randomOpSuffix(),
		Kind: "wake_task",
		Expect: state.Preconditions{TaskStatus: map[string]assistants.TaskStatus{string(taskID): assistants.StatusWatching}, Check: func(cur *state.RunState) error {
			if !onMessageRequestDue(*cur, cur.Assistants.Tasks[taskID]) {
				return errNoRequestDue
			}
			return nil
		}},
		Effects: []state.Effect{eff},
	}
	_, err = o.opts.Store.ApplyRunOperation(op, func(state.OperationResult) {
		o.publishTaskQueued(taskID, onMessageWakeReason)
		o.maybeScheduleTask(ctx, taskID)
	})
	o.handleTaskOperationErr(err)
}

// finishOnMessageRequest ends the request of on_message helper taskID that turn worked on: its outcome commits with
// the turn's end (and extra, the result of a successful turn) and the replies to its requester, then the helper
// rests in watching and takes its next request — or, with none left, is closed if its stage no longer needs it.
func (o *Orchestrator) finishOnMessageRequest(ctx context.Context, taskID assistants.TaskID, turn helperTurn, outcome state.RequestOutcome, reason string, replies []plannedMessage, guard func(*state.RunState) error, committed func(state.OperationResult), extra ...state.Effect) error {
	end := requestEnd{id: turn.request, outcome: outcome}
	if err := o.finishTaskTurnRequest(taskID, turn.number, end, assistants.StatusRunning, assistants.StatusWatching, reason, replies, guard, committed, extra...); err != nil {
		return err
	}
	o.taskDelegationRejects.Delete(taskID) // rejections in a row count within one request
	o.wakeOnMessageTask(ctx, taskID)
	o.settleOnMessageTask(taskID)
	return nil
}

// failOnMessageRequest ends turn's request as failed for reason: its requester is told why, with the unvalidated
// answer when there is one, and the helper takes the next request rather than trying this one again.
func (o *Orchestrator) failOnMessageRequest(ctx context.Context, taskID assistants.TaskID, task assistants.Task, turn helperTurn, reason string, answer unacceptedAnswer, guard func(*state.RunState) error) error {
	reason = o.sanitizeTaskReason(reason) // the reply and the recorded reason say the same
	replies := o.failureReplies(taskID, task, turn.number, reason, true, answer)
	return o.finishOnMessageRequest(ctx, taskID, turn, state.RequestOutcomeFailed, reason, replies, guard, nil)
}
