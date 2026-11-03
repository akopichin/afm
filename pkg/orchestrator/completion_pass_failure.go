package orchestrator

import (
	"errors"
	"fmt"
	"strings"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// completionPassFailedReason opens the reason of a pass cancelled because its stage failed before producing the
// result the pass waits for. A Retry of the stage restores only passes cancelled with it.
const completionPassFailedReason = "parent stage failed before completion: "

// errCompletionPassChanged rejects a cancellation or a restore decided on a state that changed before its commit.
var errCompletionPassChanged = errors.New("assistants: the completion pass or its stage changed since the decision")

// cancelCompletionPassesOfFailedStage cancels the passes of stageID's on_completion helpers that have not started:
// the failed attempt will never produce their stage's result, and a pass left waiting would hold the run forever.
func (o *Orchestrator) cancelCompletionPassesOfFailedStage(stageID string) {
	for _, id := range o.opts.Store.Snapshot().Assistants.ByRootStage[stageID] {
		o.cancelCompletionPassOfFailedStage(id)
	}
}

// cancelCompletionPassOfFailedStage cancels taskID's pass if its stage failed before the pass could start, and
// reports whether it did. The failure, the stage's attempt and the task's generation are checked again under the
// Store lock, so a Retry committed meanwhile keeps its pass.
func (o *Orchestrator) cancelCompletionPassOfFailedStage(taskID assistants.TaskID) bool {
	if !o.opts.AssistantsEnabled {
		return false
	}
	rs := o.opts.Store.Snapshot()
	t, ok := rs.Assistants.Tasks[taskID]
	if !ok || !completionPassPending(rs, t) {
		return false
	}
	stage := rs.Stages[t.RootStageID]
	if stage.Status != state.StatusFailed {
		return false
	}
	generation := t.TaskGeneration
	stillPending := func(cur *state.RunState) error {
		ct := cur.Assistants.Tasks[taskID]
		cs := cur.Stages[t.RootStageID]
		if cs.Status != state.StatusFailed || cs.Seq != stage.Seq || ct.TaskGeneration != generation || !completionPassPending(*cur, ct) {
			return errCompletionPassChanged
		}
		return nil
	}
	reason := fmt.Sprintf("%s%s, seq=%d", completionPassFailedReason, t.RootStageID, stage.Seq)
	reconcileBinding := func(state.OperationResult) { o.reconcileWatcherBinding(taskID) }
	return o.finishTaskTurnReplyingGuarded(taskID, 0, t.Status, assistants.StatusCancelled, reason, nil, stillPending, reconcileBinding) == nil
}

// passCancelledByStageFailure reports whether t is a completion helper of its stage's current attempt whose pass
// the stage's failure cancelled. A user's Cancel, a failed turn and an older generation never match.
func passCancelledByStageFailure(rs state.RunState, t *state.TaskState) bool {
	return isCompletionHelper(t.Task) && t.Status == assistants.StatusCancelled &&
		strings.HasPrefix(t.StatusReason, completionPassFailedReason) &&
		t.RootGeneration == rs.Assistants.RootGenerations[t.RootStageID]
}

// restoreCompletionPassesAfterRetry gives a retried stage back, in a new task generation, the passes its failure
// cancelled. It keys on the reason, not on one failure: a stage that failed again before the restore gets them back
// on its next Retry. Only a journal write failure is returned.
func (o *Orchestrator) restoreCompletionPassesAfterRetry(stageID string) error {
	rs := o.opts.Store.Snapshot()
	if rs.Stages[stageID].Status == state.StatusFailed {
		return nil
	}
	for _, id := range rs.Assistants.ByRootStage[stageID] {
		t := rs.Assistants.Tasks[id]
		if !passCancelledByStageFailure(rs, t) {
			continue
		}
		if err := o.restoreCompletionPass(id, t.TaskGeneration); err != nil {
			return err
		}
	}
	return nil
}

// restoreCompletionPass puts taskID back to watching in a new task generation, unless its stage failed again or the
// task changed since the decision.
func (o *Orchestrator) restoreCompletionPass(taskID assistants.TaskID, generation uint64) error {
	const reason = "restored: the stage was retried"
	watching, err := taskStatusEffect(taskID, assistants.StatusWatching, reason)
	if err != nil {
		return err
	}
	op := state.Operation{
		ID:   "task-restore-pass:" + string(taskID) + ":" + randomOpSuffix(),
		Kind: "restore_completion_pass",
		Expect: state.Preconditions{TaskStatus: map[string]assistants.TaskStatus{string(taskID): assistants.StatusCancelled}, Check: func(cur *state.RunState) error {
			ct := cur.Assistants.Tasks[taskID]
			if cur.Stages[ct.RootStageID].Status == state.StatusFailed || ct.TaskGeneration != generation || !passCancelledByStageFailure(*cur, ct) {
				return errCompletionPassChanged
			}
			return nil
		}},
		Effects: []state.Effect{{Kind: "task_generation_bumped", Target: state.Target{TaskID: string(taskID)}}, watching},
	}
	_, err = o.opts.Store.ApplyRunOperation(op, func(state.OperationResult) {
		o.publishNotice(bus.Event{Type: bus.EventAssistantTaskStatus, StageID: o.taskRootStage(taskID), Data: map[string]any{
			"task_id": string(taskID), "status": string(assistants.StatusWatching), "reason": reason,
		}})
		o.reconcileWatcherBinding(taskID)
	})
	if err == nil {
		return nil
	}
	o.handleTaskOperationErr(err)
	var storageErr *state.StorageError
	if errors.As(err, &storageErr) {
		return err
	}
	return nil
}
