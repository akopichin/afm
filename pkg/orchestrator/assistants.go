// Package orchestrator (this file, T3.4): admission ("Attach") and the small amount of state-mutation
// plumbing a helper task needs — the actual turn execution + lifecycle transitions live in
// assistants_lifecycle.go. Advisory helper tasks are observational: nothing in either file ever calls
// Trigger/completeStage — a task's outcome never changes its root stage's FSM status (dev-plan's Этап 3
// acceptance criterion).
package orchestrator

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/concurrency"
	"github.com/akopichin/afm/pkg/state"
)

// AttachRequest is a fully server-validated request to attach one new top-level helper task to a stage
// (T3.4/Этап 3 scope: one-shot advisory assistants only — no parent task, no delegation; that lands in
// Этап 4a). OperationID is the caller's idempotency key (HTTP's request_id, T2.5/T3.7): the SAME
// OperationID with the SAME content always resolves to the SAME TaskID, never a second task — see
// deriveTaskID.
type AttachRequest struct {
	OperationID string
	StageID     string
	ProfileID   assistants.ProfileID
	Title       string
	Instruction string
	Mode        assistants.Mode
	Trigger     assistants.Trigger
	Actor       state.Actor
}

// taskCreatedPayload/taskStatusChangedPayload/taskHoldPayload mirror — field-for-field, identical json tags
// — the UNEXPORTED payload shapes pkg/state's applyTaskCreated/applyTaskStatusChanged/applyTaskHoldSet
// decode (pkg/state/assistants_projection.go). An Effect's Payload is just json.RawMessage; building the
// same JSON shape here instead of pkg/state exporting its internal type is the same pattern
// pkg/state/operation_test.go's own taskCreatedEffect test helper already uses — keep these three structs in
// sync with pkg/state if its shapes ever change.
type taskCreatedPayload struct {
	ParentTaskID     *string            `json:"parent_task_id,omitempty"`
	ProfileID        string             `json:"profile_id"`
	ProfileSnapshot  assistants.Profile `json:"profile_snapshot"`
	Title            string             `json:"title"`
	Instruction      string             `json:"instruction"`
	Mode             string             `json:"mode"`
	Trigger          string             `json:"trigger"`
	PolicyRev        string             `json:"policy_rev"`
	MaxDepthAtCreate int                `json:"max_depth_at_create"`
	CreatedBy        string             `json:"created_by"`
}

type taskStatusChangedPayload struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

type taskHoldPayload struct {
	Owner  string `json:"owner"`
	Reason string `json:"reason,omitempty"`
}

// holdOwnerManual is the hold owner key used by PauseTask/ContinueTask — mirrors the stage-level manual
// pause concept (AGENTS.md's "Manual pause"), reusing the SAME hold mechanism (task_hold_set/cleared,
// pkg/state/assistants_projection.go) rather than inventing a second one (dev-plan's explicit instruction).
const holdOwnerManual = "manual"

func taskCreatedEffect(taskID assistants.TaskID, stageID string, p taskCreatedPayload) (state.Effect, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return state.Effect{}, fmt.Errorf("marshal task_created payload: %w", err)
	}
	return state.Effect{Kind: "task_created", Target: state.Target{TaskID: string(taskID), StageID: stageID}, Payload: raw}, nil
}

func taskStatusEffect(taskID assistants.TaskID, status assistants.TaskStatus, reason string) (state.Effect, error) {
	raw, err := json.Marshal(taskStatusChangedPayload{Status: string(status), Reason: reason})
	if err != nil {
		return state.Effect{}, fmt.Errorf("marshal task_status_changed payload: %w", err)
	}
	return state.Effect{Kind: "task_status_changed", Target: state.Target{TaskID: string(taskID)}, Payload: raw}, nil
}

func taskHoldEffect(kind string, taskID assistants.TaskID, owner, reason string) (state.Effect, error) {
	raw, err := json.Marshal(taskHoldPayload{Owner: owner, Reason: reason})
	if err != nil {
		return state.Effect{}, fmt.Errorf("marshal %s payload: %w", kind, err)
	}
	return state.Effect{Kind: kind, Target: state.Target{TaskID: string(taskID)}, Payload: raw}, nil
}

// deriveTaskID computes a deterministic, path-safe task id from an Attach operation id: the SAME
// OperationID always yields the SAME TaskID. That is what lets a duplicate Attach
// (T3.7's TestAttach_DuplicateRequestIdReturnsSameTask) be a true no-op idempotent replay — the Effects
// built on a retry are byte-identical to the first attempt without this code needing to look anything up
// first. 20 hex chars keeps it short while making an accidental collision between two DIFFERENT operation
// ids practically impossible.
func deriveTaskID(operationID string) assistants.TaskID {
	sum := sha256.Sum256([]byte("assistants-task|" + operationID))
	return assistants.TaskID("t-" + hex.EncodeToString(sum[:])[:20])
}

// randomOpSuffix returns a short random hex string used to make an internally-generated Operation.ID unique
// per attempt (scheduleTask/finishTaskFrom/PauseTask/ContinueTask) — unlike Attach's OperationID, these are
// never meant to be replayed idempotently: each call is its own fresh attempt, and uniqueness is what makes
// a LOST race (Preconditions mismatch under the store's lock) a clean, silent no-op rather than a false
// "idempotent repeat".
func randomOpSuffix() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// profileView looks up a profile's current availability view by id — Registry.Get returns the raw Profile
// definition (used for ProfileSnapshot); this is the separate availability check (T1.5's gate), enforced
// here at admission time per dev-plan's T3.4 spec.
func profileView(reg *assistants.Registry, id assistants.ProfileID) (assistants.ProfileView, bool) {
	for _, v := range reg.List() {
		if v.ID == id {
			return v, true
		}
	}
	return assistants.ProfileView{}, false
}

// taskDir returns the file namespace root for one helper task — see assistants_files.go (T3.6) for the
// full layout, id-safety net, and scratch/ writable-path helpers.

// taskRootStage reads a task's root stage id from the current projection — "" if the task is unknown.
func (o *Orchestrator) taskRootStage(taskID assistants.TaskID) string {
	rs := o.opts.Store.Snapshot()
	if t, ok := rs.Assistants.Tasks[taskID]; ok {
		return t.RootStageID
	}
	return ""
}

// taskHeld reports whether taskID is currently held (itself or via an ancestor) — the SAME
// AssistantsState.EffectivelyHeld used by task_hold_set/task_hold_cleared's projection.
func (o *Orchestrator) taskHeld(taskID assistants.TaskID) bool {
	rs := o.opts.Store.Snapshot()
	held, _ := rs.Assistants.EffectivelyHeld(taskID)
	return held
}

// Attach creates one new top-level helper task for a stage: capability + stage + profile + delegation-depth
// checks, then a single durable ApplyRunOperation — task_created alone (parent stage still pending: nothing
// to run yet, task stays "waiting_parent"), or task_created PLUS task_status_changed-to-"queued" in the SAME
// atomic batch when the parent stage is already active (TestAttach_ToPendingStageStaysWaitingParent /
// TestLifecycle_HappyPath).
//
// Known Этап-3 scope limit: a task attached while the stage is pending is never automatically promoted when
// that stage later starts — Этап 3 is the one-shot, non-recursive MVP, and wiring that promotion means
// touching the stage-activation call sites (scheduling.go/recovery.go), which is explicitly out of scope
// here. Not a silent bug: the task's real resulting status is always what Attach reports and what the
// journal records.
func (o *Orchestrator) Attach(ctx context.Context, req AttachRequest) (assistants.TaskID, error) {
	if !o.opts.AssistantsEnabled {
		return "", &state.OperationRejectedError{Code: assistants.CodeFeatureDisabled, Reason: "collaboration is disabled"}
	}
	if req.OperationID == "" {
		return "", errors.New("assistants: operation id is required")
	}
	st := o.graph.Stage(req.StageID)
	if st == nil {
		return "", fmt.Errorf("assistants: unknown stage %q", req.StageID)
	}
	if st.IsScript() {
		return "", fmt.Errorf("assistants: stage %q is a script stage — helpers are not supported there", req.StageID)
	}
	if o.opts.AssistantsRegistry == nil {
		return "", &state.OperationRejectedError{Code: assistants.CodeFeatureDisabled, Reason: "no assistant profiles configured"}
	}
	profile, ok := o.opts.AssistantsRegistry.Get(req.ProfileID)
	if !ok {
		return "", &state.OperationRejectedError{Code: assistants.CodeCapabilityUnavailable, Reason: fmt.Sprintf("unknown profile %q", req.ProfileID)}
	}
	view, _ := profileView(o.opts.AssistantsRegistry, req.ProfileID)
	if !view.Available {
		return "", &state.OperationRejectedError{Code: assistants.CodeCapabilityUnavailable, Reason: view.UnavailableReason}
	}

	rs := o.opts.Store.Snapshot()
	depth := assistants.ComputeDepth(nil) // top-level: Этап 3 scope has no parent task
	if rs.Assistants.Policy.Blocks(depth) {
		return "", &state.OperationRejectedError{Code: assistants.CodeDelegationDepthExceeded, Reason: fmt.Sprintf("max_depth=%d", rs.Assistants.Policy.MaxDepth)}
	}

	taskID := deriveTaskID(req.OperationID)
	mode := req.Mode
	if mode == "" {
		mode = assistants.ModeAdvisory
	}
	trigger := req.Trigger
	if trigger == "" {
		trigger = assistants.TriggerOneShot
	}

	created, err := taskCreatedEffect(taskID, req.StageID, taskCreatedPayload{
		ProfileID:        string(req.ProfileID),
		ProfileSnapshot:  profile,
		Title:            req.Title,
		Instruction:      req.Instruction,
		Mode:             string(mode),
		Trigger:          string(trigger),
		PolicyRev:        rs.Assistants.Policy.Revision,
		MaxDepthAtCreate: rs.Assistants.Policy.MaxDepth,
		CreatedBy:        string(req.Actor.Kind),
	})
	if err != nil {
		return "", err
	}
	effects := []state.Effect{created}

	parentActive := o.currentStatus(req.StageID) != state.StatusPending
	if parentActive {
		queuedEffect, qerr := taskStatusEffect(taskID, assistants.StatusQueued, "")
		if qerr != nil {
			return "", qerr
		}
		effects = append(effects, queuedEffect)
	}

	op := state.Operation{
		ID:      req.OperationID,
		Kind:    "attach_task",
		Actor:   req.Actor,
		Expect:  state.Preconditions{PolicyRevision: rs.Assistants.Policy.Revision},
		Effects: effects,
	}
	runCtx := o.runContext(ctx) // Инвариант №6: спавн не должен зависеть от HTTP request ctx.
	_, err = o.opts.Store.ApplyRunOperation(op, func(state.OperationResult) {
		o.publishNotice(bus.Event{Type: bus.EventAssistantTaskCreated, StageID: req.StageID, Data: map[string]any{
			"task_id": string(taskID), "profile_id": string(req.ProfileID),
		}})
		if parentActive {
			o.maybeScheduleTask(runCtx, taskID)
		}
	})
	if err != nil {
		return "", err
	}
	return taskID, nil
}

// PauseTask holds a task via the shared hold mechanism: cancels its in-flight subprocess immediately if
// running, or blocks it from starting if still queued (spawnTask's shouldRun re-check, same pattern as a
// stage Pause racing SpawnAgent's semaphore queue — concurrency.SpawnOwned's shouldRun-after-acquire).
func (o *Orchestrator) PauseTask(ctx context.Context, taskID assistants.TaskID) error {
	eff, err := taskHoldEffect("task_hold_set", taskID, holdOwnerManual, "")
	if err != nil {
		return err
	}
	op := state.Operation{ID: "task-pause:" + string(taskID) + ":" + randomOpSuffix(), Kind: "pause_task", Effects: []state.Effect{eff}}
	if _, err := o.opts.Store.ApplyRunOperation(op, nil); err != nil {
		return err
	}
	if cancel, ok := o.taskCancels.Load(taskID); ok {
		cancel.(context.CancelFunc)()
	}
	return nil
}

// ContinueTask clears a manual hold and, if the task is still eligible (queued), attempts to schedule it —
// a no-op if the hold was the only thing blocking an already-finished or already-running task.
func (o *Orchestrator) ContinueTask(ctx context.Context, taskID assistants.TaskID) error {
	eff, err := taskHoldEffect("task_hold_cleared", taskID, holdOwnerManual, "")
	if err != nil {
		return err
	}
	op := state.Operation{ID: "task-continue:" + string(taskID) + ":" + randomOpSuffix(), Kind: "continue_task", Effects: []state.Effect{eff}}
	runCtx := o.runContext(ctx)
	_, err = o.opts.Store.ApplyRunOperation(op, func(state.OperationResult) {
		o.maybeScheduleTask(runCtx, taskID)
	})
	return err
}

// CancelTask stops a non-terminal task immediately (cancelling its in-flight subprocess, if any) and marks
// it cancelled. A task that already reached a terminal status is left untouched — its history (including any
// partial result already persisted) is never erased (TestCancel_KeepsHistory).
func (o *Orchestrator) CancelTask(ctx context.Context, taskID assistants.TaskID) error {
	rs := o.opts.Store.Snapshot()
	t, ok := rs.Assistants.Tasks[taskID]
	if !ok {
		return fmt.Errorf("assistants: unknown task %q", taskID)
	}
	if t.Status.Terminal() {
		return nil
	}
	if cancel, ok := o.taskCancels.Load(taskID); ok {
		cancel.(context.CancelFunc)()
	}
	o.finishTaskFrom(taskID, t.Status, assistants.StatusCancelled, "cancelled by user")
	return nil
}

// maybeScheduleTask attempts to move a QUEUED, not-currently-held task to running and spawn its helper
// process. Safe to call any number of times for the same task — including from an idempotent Attach replay
// (T2.5), where externalWork fires again even though nothing new happened — because the transition's own
// CAS (a FRESH, never-reused operation id plus a TaskStatus precondition) means only the caller that
// actually wins the queued→running transition ever spawns; every later attempt sees a non-"queued" status
// and no-ops. A task currently held (PauseTask) is left queued — ContinueTask re-attempts scheduling once
// the hold clears.
func (o *Orchestrator) maybeScheduleTask(ctx context.Context, taskID assistants.TaskID) {
	if o.taskHeld(taskID) {
		return
	}
	rs := o.opts.Store.Snapshot()
	t, ok := rs.Assistants.Tasks[taskID]
	if !ok || t.Status != assistants.StatusQueued {
		return
	}
	eff, err := taskStatusEffect(taskID, assistants.StatusRunning, "")
	if err != nil {
		return
	}
	op := state.Operation{
		ID:      "task-start:" + string(taskID) + ":" + randomOpSuffix(),
		Kind:    "start_task",
		Expect:  state.Preconditions{TaskStatus: map[string]assistants.TaskStatus{string(taskID): assistants.StatusQueued}},
		Effects: []state.Effect{eff},
	}
	task := t.Task
	if _, err := o.opts.Store.ApplyRunOperation(op, func(state.OperationResult) {
		o.publishNotice(bus.Event{Type: bus.EventAssistantTaskStatus, StageID: task.RootStageID, Data: map[string]any{
			"task_id": string(taskID), "status": string(assistants.StatusRunning),
		}})
		o.spawnTask(ctx, taskID, task)
	}); err != nil {
		// Lost the CAS race (already running/cancelled/held by the time we tried), or the task simply isn't
		// queued anymore — not an error: whoever actually won already spawned it, or it's no longer eligible.
		return
	}
}

// spawnTask hands the task's turn to concurrency.SpawnOwned under its profile command's semaphore. The
// pause/TOCTOU re-check happens INSIDE the run body (not via SpawnOwned's shouldRun hook) so a task that
// gets held while queued behind a full command semaphore can be put visibly BACK to "queued" instead of
// being left stuck in "running" with no process ever having started (SpawnOwned's shouldRun-false path is
// silent/fire-and-forget by design — fine for stages, which have no equivalent requeue state machine, but
// wrong for tasks, which do).
func (o *Orchestrator) spawnTask(ctx context.Context, taskID assistants.TaskID, task assistants.Task) {
	cmd := task.ProfileSnapshot.Command
	o.concurrency.SpawnOwned(ctx, concurrency.TaskOwnerKey(string(taskID)), cmd, nil, func(runCtx context.Context, lease *concurrency.Lease) {
		if o.taskHeld(taskID) {
			o.finishTaskFrom(taskID, assistants.StatusRunning, assistants.StatusQueued, "re-queued: paused before start")
			return
		}
		o.runHelperTask(runCtx, taskID, task)
	})
}

// finishTaskFrom durably transitions a task to a terminal (or, for the requeue case above, non-terminal)
// status, guarded by a Preconditions check against the status it is expected to be coming FROM — without
// this guard, a racing CancelTask and a task's own natural completion could each successfully overwrite the
// other's terminal status (applyTaskStatusChanged has no built-in "already terminal" guard of its own).
func (o *Orchestrator) finishTaskFrom(taskID assistants.TaskID, from, to assistants.TaskStatus, reason string) {
	eff, err := taskStatusEffect(taskID, to, reason)
	if err != nil {
		return
	}
	op := state.Operation{
		ID:      "task-finish:" + string(taskID) + ":" + string(to) + ":" + randomOpSuffix(),
		Kind:    "finish_task",
		Expect:  state.Preconditions{TaskStatus: map[string]assistants.TaskStatus{string(taskID): from}},
		Effects: []state.Effect{eff},
	}
	rootStage := o.taskRootStage(taskID)
	_, _ = o.opts.Store.ApplyRunOperation(op, func(state.OperationResult) {
		o.publishNotice(bus.Event{Type: bus.EventAssistantTaskStatus, StageID: rootStage, Data: map[string]any{
			"task_id": string(taskID), "status": string(to), "reason": reason,
		}})
	})
}
