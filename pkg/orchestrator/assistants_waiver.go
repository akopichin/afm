// Package orchestrator (this file, Этап 7 T7.6): waiving a required helper task's contribution to its root
// stage's completion barrier — a SEPARATE, explicitly-audited operation (actor + reason recorded durably),
// never a side effect of Cancel/Retry/any task_result (§13.2). Only a human actor may waive: a model's own
// task result can never declare itself waived (§19.5) — enforced here, at the one production admission path,
// not in pkg/state (which has no notion of "human" vs "model" beyond the free-form Actor.Kind string it is
// handed).
package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// taskWaivedEffect builds a "task_waived" control effect — mirrors taskResultEffect/taskStatusEffect above.
func taskWaivedEffect(taskID assistants.TaskID, reason string) (state.Effect, error) {
	raw, err := json.Marshal(state.TaskWaivedPayload{Reason: reason})
	if err != nil {
		return state.Effect{}, fmt.Errorf("marshal task_waived payload: %w", err)
	}
	return state.Effect{Kind: "task_waived", Target: state.Target{TaskID: string(taskID)}, Payload: raw}, nil
}

// WaiveTask records a human decision to accept the risk of taskID never closing its own required gate
// (failed/cancelled/needs_changes/inconclusive, or simply abandoned) — unblocking its root stage through the
// NORMAL completion cascade (maybeCompleteAfterAssistants), never a direct FSM nudge. Idempotent: waiving an
// already-waived task is a silent no-op, not an error (TestWaiver_IsSeparateAuditedOperation can retry safely).
func (o *Orchestrator) WaiveTask(ctx context.Context, taskID assistants.TaskID, actor state.Actor, reason string) error {
	// §19.5: a waiver is a human decision, period — OriginAssistant (the model's own task result) and
	// OriginSideAgent are both rejected, same as any actor string this package has never heard of.
	if actor.Kind != string(assistants.OriginUser) {
		return &state.OperationRejectedError{Code: assistants.CodeActorNotAllowed, Reason: "a waiver must be recorded by a human actor, not a model result"}
	}
	if reason == "" {
		return errors.New("assistants: waiver reason is required")
	}
	rs := o.opts.Store.Snapshot()
	t, ok := rs.Assistants.Tasks[taskID]
	if !ok {
		return fmt.Errorf("assistants: unknown task %q", taskID)
	}
	if t.Waived {
		return nil
	}

	eff, err := taskWaivedEffect(taskID, reason)
	if err != nil {
		return err
	}
	op := state.Operation{
		ID:      "task-waive:" + string(taskID) + ":" + randomOpSuffix(),
		Kind:    "waive_task",
		Actor:   actor,
		Effects: []state.Effect{eff},
	}
	rootStage := t.RootStageID
	runCtx := o.runContext(ctx)
	_, err = o.opts.Store.ApplyRunOperation(op, func(state.OperationResult) {
		o.publishNotice(bus.Event{Type: bus.EventAssistantTaskStatus, StageID: rootStage, Data: map[string]any{
			"task_id": string(taskID), "waived": true, "reason": reason,
		}})
		// T7.6: the waiver unblocks the stage through the SAME barrier re-check every other task-resolution
		// path uses — never a bare Trigger/EvComplete (TestWaiver_UnblocksStageThroughNormalCascade).
		o.maybeCompleteAfterAssistants(runCtx, rootStage)
	})
	if err != nil {
		o.handleTaskOperationErr(err)
		return err
	}
	return nil
}
