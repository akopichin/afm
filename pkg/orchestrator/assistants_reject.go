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

// deliverDelegationRejection turns an admitChildren error into a typed, human-readable note saved as the
// task's own follow-up (the SAME mechanism FollowUp/StopAndClarify already use, T4.7 — reused rather than
// inventing a second "message to a task" channel) and requeues the task for a fresh turn that will read it.
// Never fails the task outright: a rejection is information for the model to act on, not a crash.
func (o *Orchestrator) deliverDelegationRejection(taskID assistants.TaskID, rejectErr error) {
	code := assistants.ErrorCode("delegation_rejected")
	reason := rejectErr.Error()
	var rej *state.OperationRejectedError
	if errors.As(rejectErr, &rej) {
		code = rej.Code
		reason = rej.Reason
	}

	note := fmt.Sprintf(
		"## Delegation rejected\n\n"+
			"Your request to create child tasks was rejected: %s — %s\n\n"+
			"No child was created (the whole group is rejected together, never a partial one). "+
			"Either complete the remaining work yourself, or honestly report this limitation in your own "+
			"result instead of repeating the same delegation request.", code, reason)
	if werr := o.saveFollowUp(taskID, note); werr != nil {
		o.finishTaskFrom(taskID, assistants.StatusRunning, assistants.StatusFailed, "assistants: persist delegation rejection: "+werr.Error())
		return
	}
	o.finishTaskFrom(taskID, assistants.StatusRunning, assistants.StatusQueued, "delegation_rejected: "+string(code))
	o.maybeScheduleTask(o.runContext(context.Background()), taskID)
}
