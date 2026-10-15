// Package orchestrator (this file, T4a.6): recursive holds/cancel across a task's whole subtree, and
// generation-based invalidation (RetryTask for one task's own branch, BumpRootGeneration for a whole root
// stage's tree) — the single-task PauseTask/ContinueTask/CancelTask in assistants.go only ever acted on ONE
// task id; delegation means pausing/cancelling a BRANCH must reach every descendant that branch has already
// spawned, not just block new ones (EffectivelyHeld's ancestor-walk already does that half — see its own doc
// comment in pkg/state/assistants_projection.go).
package orchestrator

import (
	"context"
	"errors"
	"fmt"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// descendantIDs returns every transitive descendant of rootID (NOT including rootID itself) via the current
// ByParent projection — BFS order, irrelevant to correctness (every cascade below treats the set as
// unordered), only completeness matters.
func (o *Orchestrator) descendantIDs(rootID assistants.TaskID) []assistants.TaskID {
	rs := o.opts.Store.Snapshot()
	var out []assistants.TaskID
	queue := []assistants.TaskID{rootID}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		children := rs.Assistants.ByParent[cur]
		out = append(out, children...)
		queue = append(queue, children...)
	}
	return out
}

// interruptLiveTask cancels taskID's in-flight subprocess, if any, marking it as a pause (not a crash) for
// runHelperTask's own classification — exactly PauseTask's original single-task body, factored out so
// PauseTask can apply it to a whole subtree without duplicating the signal/cancel pairing.
func (o *Orchestrator) interruptLiveTask(taskID assistants.TaskID) {
	if v, ok := o.taskCancels.Load(taskID); ok {
		o.taskPausePending.Store(taskID, struct{}{})
		v.(*taskCancelEntry).cancel()
	}
}

// cancelOneTask is CancelTask's original single-task body, factored out so CancelTask/RetryTask/
// BumpRootGeneration can apply it across a whole subtree. A no-op for an unknown or already-terminal task —
// never re-cancels/overwrites existing terminal history (TestCancel_KeepsHistory's invariant, preserved
// across every cascade call site below).
func (o *Orchestrator) cancelOneTask(taskID assistants.TaskID) {
	rs := o.opts.Store.Snapshot()
	t, ok := rs.Assistants.Tasks[taskID]
	if !ok || t.Status.Terminal() {
		return
	}
	if v, ok := o.taskCancels.Load(taskID); ok {
		v.(*taskCancelEntry).cancel()
	}
	o.finishTaskFrom(taskID, t.Status, assistants.StatusCancelled, "cancelled by user")
}

// RetryTask requeues a FAILED helper task for another attempt (T4a.6): bumps its OWN TaskGeneration (so any
// child created under the superseded attempt becomes invisible to requiredChildOpen/tryResumeParent/
// collectChildResultsJSON — see currentChildren's doc comment) and cancels any descendant that is STILL
// non-terminal (a task can fail with its own resumed turn's children still mid-flight) — never a SIBLING
// branch: descendantIDs only ever walks taskID's own subtree.
func (o *Orchestrator) RetryTask(ctx context.Context, taskID assistants.TaskID) error {
	rs := o.opts.Store.Snapshot()
	t, ok := rs.Assistants.Tasks[taskID]
	if !ok {
		return fmt.Errorf("assistants: unknown task %q", taskID)
	}
	if t.Status != assistants.StatusFailed {
		return &state.OperationRejectedError{Code: assistants.CodeInvalidStatus, Reason: "only a failed task can be retried"}
	}

	for _, id := range o.descendantIDs(taskID) {
		o.cancelOneTask(id)
	}

	bumpEff := state.Effect{Kind: "task_generation_bumped", Target: state.Target{TaskID: string(taskID)}}
	queuedEff, err := taskStatusEffect(taskID, assistants.StatusQueued, "retried")
	if err != nil {
		return err
	}
	op := state.Operation{
		ID:      "task-retry:" + string(taskID) + ":" + randomOpSuffix(),
		Kind:    "retry_task",
		Expect:  state.Preconditions{TaskStatus: map[string]assistants.TaskStatus{string(taskID): assistants.StatusFailed}},
		Effects: []state.Effect{bumpEff, queuedEff},
	}
	runCtx := o.runContext(ctx)
	_, err = o.opts.Store.ApplyRunOperation(op, func(state.OperationResult) {
		o.publishNotice(bus.Event{Type: bus.EventAssistantTaskStatus, StageID: t.RootStageID, Data: map[string]any{
			"task_id": string(taskID), "status": string(assistants.StatusQueued), "reason": "retried",
		}})
		o.maybeScheduleTask(runCtx, taskID)
	})
	if err != nil {
		o.handleTaskOperationErr(err)
		return err
	}
	return nil
}

// BumpRootGeneration invalidates a WHOLE root stage's task tree (T4a.6) — meant to be called whenever the
// root STAGE itself gets a fresh attempt (a later integration task wires the actual call site into the
// stage-level Revise/retry flow; see dev-plan.md's T4a.6 note on this deliberately-scoped boundary). Every
// top-level task under stageID that is still non-terminal, and transitively its own descendants, is
// cancelled; terminal tasks from the old tree are left untouched (history, never erased). The counter itself
// advances durably FIRST, so a storage failure here is fail-closed (no cascade runs against a counter that
// never actually committed).
func (o *Orchestrator) BumpRootGeneration(stageID string) error {
	if stageID == "" {
		return errors.New("assistants: stage id is required")
	}
	rs := o.opts.Store.Snapshot()
	var topLevel []assistants.TaskID
	for _, id := range rs.Assistants.ByRootStage[stageID] {
		if t, ok := rs.Assistants.Tasks[id]; ok && t.ParentTaskID == nil {
			topLevel = append(topLevel, id)
		}
	}

	eff := state.Effect{Kind: "root_generation_bumped", Target: state.Target{StageID: stageID}}
	op := state.Operation{ID: "root-generation-bump:" + stageID + ":" + randomOpSuffix(), Kind: "bump_root_generation", Effects: []state.Effect{eff}}
	if _, err := o.opts.Store.ApplyRunOperation(op, nil); err != nil {
		o.handleTaskOperationErr(err)
		return err
	}

	for _, id := range topLevel {
		o.cancelOneTask(id)
		for _, cid := range o.descendantIDs(id) {
			o.cancelOneTask(cid)
		}
	}
	return nil
}
