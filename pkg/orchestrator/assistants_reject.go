// Package orchestrator (this file, T4a.7): typed delegation rejections delivered BACK to the delegating
// task, not just a bare "failed" — admitChildren is already strictly all-or-nothing (no partial group is
// ever created, whatever the rejection reason) and delegateOperationID already makes a retried/duplicated
// delegate turn idempotent (see its own doc comment) — what is missing without this file is giving the model
// a chance to react: do the work itself, or honestly report the limitation, instead of the run just failing
// outright on a depth/budget rejection it had no way to see.
package orchestrator

import (
	"context"
	"errors"
	"fmt"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/state"
)

// maxConsecutiveDelegationRejections caps how many times in a row a single task may have a delegate turn
// rejected before the orchestrator stops requeueing it and fails it outright. The rejection path (below) is
// deliberately non-fatal — it hands the model a follow-up note so it can do the work itself or report the
// limitation instead — but a model that ignores the note and re-emits the SAME rejected delegate every turn
// would otherwise spin running⇄queued forever with no backoff, flooding events.jsonl. This bound makes that
// pathological loop terminate instead. The counter resets on any successful delegation (handleDelegate).
const maxConsecutiveDelegationRejections = 3

// deliverDelegationRejection turns an admitChildren error into a typed, human-readable note saved as the
// task's own follow-up (the SAME mechanism FollowUp/StopAndClarify already use, T4.7 — reused rather than
// inventing a second "message to a task" channel) and requeues the task for a fresh turn that will read it.
// A rejection is information for the model to act on, not a crash — but after
// maxConsecutiveDelegationRejections in a row the task is failed instead of requeued, so a model that keeps
// re-sending the same rejected delegate cannot spin forever.
// turn is the rejected delegating turn, ended by the status change recorded here.
func (o *Orchestrator) deliverDelegationRejection(taskID assistants.TaskID, turn uint64, rejectErr error, guard func(*state.RunState) error) error {
	if o.helperOutputCommitHook != nil {
		o.helperOutputCommitHook()
	}
	code := assistants.ErrorCode("delegation_rejected")
	reason := rejectErr.Error()
	var rej *state.OperationRejectedError
	if errors.As(rejectErr, &rej) {
		code = rej.Code
		reason = rej.Reason
	}

	count := 1
	if prev, ok := o.taskDelegationRejects.Load(taskID); ok {
		count = prev.(int) + 1
	}
	if count >= maxConsecutiveDelegationRejections {
		return o.finishTaskTurnReplyingGuarded(taskID, turn, assistants.StatusRunning, assistants.StatusFailed,
			fmt.Sprintf("delegation_rejected %d times in a row (%s): %s", count, code, reason), nil, guard,
			func(state.OperationResult) { o.taskDelegationRejects.Delete(taskID) })
	}

	note := fmt.Sprintf(
		"## Delegation rejected\n\n"+
			"Your request to create child tasks was rejected: %s — %s\n\n"+
			"No child was created (the whole group is rejected together, never a partial one). "+
			"Either complete the remaining work yourself, or honestly report this limitation in your own "+
			"result instead of repeating the same delegation request.", code, reason)
	var writeErr error
	committed := func(state.OperationResult) {
		// SpawnOwned keeps the next turn behind this callback, including its follow-up publication.
		o.taskDelegationRejects.Store(taskID, count)
		writeErr = o.saveFollowUp(taskID, note)
	}
	if err := o.finishTaskTurnReplyingGuarded(taskID, turn, assistants.StatusRunning, assistants.StatusQueued, "delegation_rejected: "+string(code), nil, guard, committed); err != nil {
		return err
	}
	if writeErr != nil {
		o.finishTaskFrom(taskID, assistants.StatusQueued, assistants.StatusFailed, "assistants: persist delegation rejection: "+writeErr.Error())
		return writeErr
	}
	o.maybeScheduleTask(o.runContext(context.Background()), taskID)
	return nil
}
