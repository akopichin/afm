// Package orchestrator (this file, T3.4): admission ("Attach") and the small amount of state-mutation
// plumbing a helper task needs — the actual turn execution + lifecycle transitions live in
// assistants_lifecycle.go. Advisory helper tasks remain observational: an advisory task's own outcome never
// changes its root stage's FSM status. Этап 7 (T7.2) adds the ONE exception for REQUIRED tasks: finishTaskFrom
// calls maybeCompleteAfterAssistants after every terminal transition, which re-attempts completeStage — but
// that is a correct no-op unless the stage is CURRENTLY awaiting_assistants (entered only once its own main
// executor already finished), so an advisory task's completion still never moves a stage that never left
// running/retrying in the first place.
package orchestrator

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/concurrency"
	"github.com/akopichin/afm/pkg/prompts"
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

// holdOwnerManual is the hold owner key used by PauseTask/ContinueTask — mirrors the stage-level manual
// pause concept (AGENTS.md's "Manual pause"), reusing the SAME hold mechanism (task_hold_set/cleared,
// pkg/state/assistants_projection.go) rather than inventing a second one (dev-plan's explicit instruction).
const holdOwnerManual = "manual"

// taskCreatedEffect/taskStatusEffect/taskHoldEffect build an Effect whose Payload is one of pkg/state's
// EXPORTED payload shapes (state.TaskCreatedPayload/TaskStatusChangedPayload/TaskHoldPayload,
// pkg/state/assistants_projection.go) — the SAME struct pkg/state's own applyTaskCreated/
// applyTaskStatusChanged/applyTaskHoldSet/applyTaskHoldCleared decode, not a hand-kept-in-sync mirror. This
// package already imports pkg/state for state.Effect/state.Target/state.Operation, so sharing the type adds
// no new dependency.
func taskCreatedEffect(taskID assistants.TaskID, stageID string, p state.TaskCreatedPayload) (state.Effect, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return state.Effect{}, fmt.Errorf("marshal task_created payload: %w", err)
	}
	return state.Effect{Kind: "task_created", Target: state.Target{TaskID: string(taskID), StageID: stageID}, Payload: raw}, nil
}

func taskStatusEffect(taskID assistants.TaskID, status assistants.TaskStatus, reason string) (state.Effect, error) {
	return taskStatusChangeEffect(taskID, state.TaskStatusChangedPayload{Status: string(status), Reason: reason})
}

// taskStatusChangeEffect is taskStatusEffect with the whole payload — for a status change that also starts or
// ends a turn (TaskStatusChangedPayload.Turn/TurnInbox).
func taskStatusChangeEffect(taskID assistants.TaskID, p state.TaskStatusChangedPayload) (state.Effect, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return state.Effect{}, fmt.Errorf("marshal task_status_changed payload: %w", err)
	}
	return state.Effect{Kind: "task_status_changed", Target: state.Target{TaskID: string(taskID)}, Payload: raw}, nil
}

func taskHoldEffect(kind string, taskID assistants.TaskID, owner, reason string) (state.Effect, error) {
	raw, err := json.Marshal(state.TaskHoldPayload{Owner: owner, Reason: reason})
	if err != nil {
		return state.Effect{}, fmt.Errorf("marshal %s payload: %w", kind, err)
	}
	return state.Effect{Kind: kind, Target: state.Target{TaskID: string(taskID)}, Payload: raw}, nil
}

// taskResultEffectKind is the task_result effect's Kind string — a named constant (not a repeated literal)
// so childResultMeshEffects (assistants_delegate.go) can recognize the SAME effect this function builds
// among finishTaskFrom's extra effects without hand-duplicating the string (goconst).
const taskResultEffectKind = "task_result"

// taskResultEffect carries a completed task's decoded assistants.Result as a "task_result" effect's Payload —
// bundled into the SAME ApplyRunOperation call as the task_status_changed-to-"completed" effect (see
// finishTaskFrom's extra parameter and runHelperTask's call site) so both commit in one atomic control
// record. assistants.Result's own json tags ARE the wire shape (verdict/summary/findings/evidence/
// context_version/suggested_change/limitations) — no mirror payload struct needed, unlike
// state.TaskCreatedPayload/TaskStatusChangedPayload/TaskHoldPayload above, since pkg/state's applyTaskResult
// decodes directly into assistants.Result too.
func taskResultEffect(taskID assistants.TaskID, result assistants.Result) (state.Effect, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return state.Effect{}, fmt.Errorf("marshal task_result payload: %w", err)
	}
	return state.Effect{Kind: taskResultEffectKind, Target: state.Target{TaskID: string(taskID)}, Payload: raw}, nil
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

// taskParent reads a task's parent id from the current projection — ("", false) for a top-level task or an
// unknown task id.
func (o *Orchestrator) taskParent(taskID assistants.TaskID) (assistants.TaskID, bool) {
	rs := o.opts.Store.Snapshot()
	t, ok := rs.Assistants.Tasks[taskID]
	if !ok || t.ParentTaskID == nil {
		return "", false
	}
	return *t.ParentTaskID, true
}

// taskTerminal reports whether taskID's CURRENT projected status is already terminal — an unknown task id
// reports false (nothing to be terminal about yet). Used by runHelperTask at two points: before doing any
// file I/O at all (a CancelTask that raced ahead of this goroutine's very first instruction may have already
// committed "cancelled") and again after the agent call returns (CancelTask racing the subprocess call
// itself) — see runHelperTask's doc comments for why a plain ctx.Err() check cannot stand in for this.
func (o *Orchestrator) taskTerminal(taskID assistants.TaskID) bool {
	rs := o.opts.Store.Snapshot()
	t, ok := rs.Assistants.Tasks[taskID]
	return ok && t.Status.Terminal()
}

// handleTaskOperationErr classifies an ApplyRunOperation failure for the task lifecycle: a *state.StorageError
// means the authoritative journal write itself failed — the run cannot safely continue making decisions
// against a broken log, so it ends the run (dev-plan.md invariant #5), mirroring triggerWithSeq's existing
// treatment of bus.StorageError for stage FSM transitions. Any other error (a CAS/precondition miss — the op
// simply lost a race, e.g. task already terminal/held/cancelled) is a benign no-op: the caller that actually
// won already did whatever this one was trying to do.
func (o *Orchestrator) handleTaskOperationErr(err error) {
	if err == nil {
		return
	}
	var se *state.StorageError
	if errors.As(err, &se) {
		log.Printf("FATAL: storage failure in assistants task operation: %v", err)
		o.setFatal(err)
	}
}

// Attach creates one new top-level helper task for a stage: admission (admitTopLevelTask, shared with
// AttachAndSend), then a single durable ApplyRunOperation — task_created alone (parent stage still pending: nothing
// to run yet, task stays "waiting_parent"), or task_created PLUS a status transition in the SAME atomic batch
// when the parent stage is already active: to "queued" (immediately scheduled) for a plain one_shot task
// (TestAttach_ToPendingStageStaysWaitingParent/TestLifecycle_HappyPath), or to "watching" (NOT scheduled —
// its collector starts every pass) for a checkpoints/on_completion watcher (T5.4).
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
	if err := rejectReservedRequestID(req.OperationID, req.Actor); err != nil {
		return "", err
	}
	treq := topLevelTaskRequest{
		OperationID: req.OperationID, ProfileID: req.ProfileID, Scope: assistants.ScopeStage, StageID: req.StageID,
		Title: req.Title, Instruction: req.Instruction, Mode: req.Mode, Trigger: req.Trigger, Actor: req.Actor,
	}
	// A retry of an already-committed attach replays it, whatever changed since (the stage finished, the
	// profile went away).
	if op, committed, err := o.opts.Store.CommittedOperation(req.OperationID, opKindAttachTask); err != nil || committed {
		if err != nil {
			return "", err
		}
		return o.replayedTopLevelTask(op, treq)
	}

	task, err := o.admitTopLevelTask(o.opts.Store.Snapshot(), treq)
	if err != nil {
		return "", err
	}
	op := state.Operation{ID: req.OperationID, Kind: opKindAttachTask, Actor: req.Actor, Expect: task.Expect, Effects: task.Effects}
	runCtx := o.runContext(ctx) // Инвариант №6: спавн не должен зависеть от HTTP request ctx.
	_, err = o.opts.Store.ApplyRunOperation(op, func(state.OperationResult) {
		o.publishNotice(bus.Event{Type: bus.EventAssistantTaskCreated, StageID: req.StageID, Data: map[string]any{
			"task_id": string(task.ID), "profile_id": string(req.ProfileID),
		}})
		if isWatcherTrigger(task.Trigger) {
			// bindWatcher only needs the identity fields (RootStageID/Trigger) — read back the just-committed
			// task rather than constructing a partial assistants.Task by hand here.
			if t, ok := o.opts.Store.Snapshot().Assistants.Tasks[task.ID]; ok {
				o.bindWatcher(task.ID, t.Task)
			}
		}
		if task.Status == assistants.StatusQueued {
			o.maybeScheduleTask(runCtx, task.ID)
		}
	})
	if err != nil {
		return "", err
	}
	return task.ID, nil
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
		o.handleTaskOperationErr(err)
		return err
	}
	// T4a.6: the hold itself (above) already blocks the WHOLE subtree from starting/restarting via
	// EffectivelyHeld's ancestor-walk — but an ALREADY-RUNNING descendant has no reason to notice a hold on
	// an ancestor it never polls; interruptLiveTask across the whole subtree is what actually stops it
	// (TestPause_CascadesToSubtree), not just a future one.
	o.interruptLiveTask(taskID)
	for _, id := range o.descendantIDs(taskID) {
		o.interruptLiveTask(id)
	}
	return nil
}

// ContinueTask clears a manual hold and, if the task (or any of its descendants, T4a.6) is still eligible
// (queued), attempts to schedule it — a no-op for anything the hold was not the only thing blocking. Only
// taskID's OWN "manual" hold is cleared (TestContinue_DoesNotClearOtherHolds) — a descendant separately
// PauseTask'd in its own right keeps ITS OWN hold entry untouched and stays paused.
func (o *Orchestrator) ContinueTask(ctx context.Context, taskID assistants.TaskID) error {
	eff, err := taskHoldEffect("task_hold_cleared", taskID, holdOwnerManual, "")
	if err != nil {
		return err
	}
	op := state.Operation{ID: "task-continue:" + string(taskID) + ":" + randomOpSuffix(), Kind: "continue_task", Effects: []state.Effect{eff}}
	runCtx := o.runContext(ctx)
	descendants := o.descendantIDs(taskID)
	_, err = o.opts.Store.ApplyRunOperation(op, func(state.OperationResult) {
		o.maybeScheduleTask(runCtx, taskID)
		// A descendant held ONLY through the ancestor chain (no hold entry of its own) is now eligible again,
		// but nothing else will ever re-trigger its scheduling — maybeScheduleTask's own "queued" precondition
		// makes a redundant call here (a still-held or already-running descendant) a safe no-op.
		for _, id := range descendants {
			o.maybeScheduleTask(runCtx, id)
		}
	})
	o.handleTaskOperationErr(err)
	return err
}

// CancelTask stops a non-terminal task immediately (cancelling its in-flight subprocess, if any), marks it
// cancelled, and cascades the SAME to its whole subtree (T4a.6) — a cancelled branch must not leave orphaned
// descendants still running against a parent that will never read their result. A task (or descendant) that
// already reached a terminal status is left untouched — its history (including any partial result already
// persisted) is never erased (TestCancel_KeepsHistory/TestCancel_CascadesAndKeepsHistory).
func (o *Orchestrator) CancelTask(ctx context.Context, taskID assistants.TaskID) error {
	rs := o.opts.Store.Snapshot()
	if _, ok := rs.Assistants.Tasks[taskID]; !ok {
		return fmt.Errorf("assistants: unknown task %q", taskID)
	}
	o.cancelOneTask(taskID)
	for _, id := range o.descendantIDs(taskID) {
		o.cancelOneTask(id)
	}
	return nil
}

// runAgentIdleReason is the status reason of a run agent that finished a turn and waits for its next message.
// A turn whose answer was rejected adds why after it (runAgentIdleAfterFailure); both mean idle.
const runAgentIdleReason = "idle: waiting for a message"

// maxTaskReasonBytes bounds a task's status reason. A failure reason can quote the model's own output (a
// decode error names the offending value), so it is also redacted (sanitizeTaskReason).
const maxTaskReasonBytes = 2048

// sanitizeTaskReason is reason as it may be recorded and shown: secrets masked first, then cut to
// maxTaskReasonBytes — in this order, so a cut never leaves part of a secret the redaction cannot recognise.
func (o *Orchestrator) sanitizeTaskReason(reason string) string {
	return prompts.TruncateUTF8(o.RedactSecrets(reason), maxTaskReasonBytes, "…")
}

// runAgentIdleAfterFailure is the status reason of a run agent idle again after a turn that failed for reason.
func runAgentIdleAfterFailure(reason string) string {
	return runAgentIdleReason + " (last turn failed: " + describeTaskFailure(reason) + ")"
}

// idleRunAgent reports whether t is a standing run agent with nothing to do: queued because it was attached
// without a first message or finished its last turn (not because a wait resumed it), and nobody has written
// to it since. Such an agent only starts on a new message — never on its own after a restart or a Continue.
func idleRunAgent(rs state.RunState, t *state.TaskState) bool {
	if t.Scope != assistants.ScopeRun || t.Status != assistants.StatusQueued {
		return false
	}
	if t.StatusReason != "" && !strings.HasPrefix(t.StatusReason, runAgentIdleReason) {
		return false
	}
	return !rs.Assistants.Mesh.HasWakingUnreadFor(meshParticipantForTask(rs, t.Task))
}

// queuedTurnDue reports whether some queued task still has a turn due, so the run must not finish under it.
// state.RunBlocker deliberately ignores queued tasks — an idle run agent with nothing to read never holds the
// run open — but queued is also the step between a commit that makes a task runnable and the commit that
// starts it: a resume (waiting_peer/waiting_children → queued, then queued → running in a separate operation)
// or a message accepted during a run agent's last turn. Without this check the run loop could finalize the run
// in between, and the start would then be rejected run_finalizing, losing the turn. Only an idle run agent
// (idleRunAgent) is exempt. A held (paused) task is not: the hold defers its accepted work, it does not drop
// it, so the run stays open — like a paused stage — until the human continues or cancels the task.
func queuedTurnDue(rs state.RunState) bool {
	for _, t := range rs.Assistants.Tasks {
		if t.Status != assistants.StatusQueued {
			continue
		}
		if t.Scope == assistants.ScopeRun && idleRunAgent(rs, t) {
			continue
		}
		return true
	}
	return false
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
	if idleRunAgent(rs, t) {
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
		// queued anymore — not an error in the benign case: whoever actually won already spawned it, or it's
		// no longer eligible. handleTaskOperationErr still classifies it — a genuine *state.StorageError here
		// must end the run, not be treated the same as a lost CAS race.
		o.handleTaskOperationErr(err)
		return
	}
}

// spawnTask hands the task's turn to concurrency.SpawnOwned under its profile command's semaphore. The
// pause/TOCTOU re-check happens INSIDE the run body (not via SpawnOwned's shouldRun hook) so a task that
// gets held while queued behind a full command semaphore can be put visibly BACK to "queued" instead of
// being left stuck in "running" with no process ever having started (SpawnOwned's shouldRun-false path is
// silent/fire-and-forget by design — fine for stages, which have no equivalent requeue state machine, but
// wrong for tasks, which do). After the requeue commits, a not-held re-check retries maybeScheduleTask once —
// closing a lost-wakeup window where a concurrent ContinueTask's own reschedule attempt landed too early
// (status still "running") and silently no-op'd, leaving the task stuck in "queued" with the hold already
// cleared and nothing left to wake it (see the finish-queue block below for the full trace).
func (o *Orchestrator) spawnTask(ctx context.Context, taskID assistants.TaskID, task assistants.Task) {
	cmd := task.ProfileSnapshot.Command
	o.concurrency.SpawnOwned(ctx, concurrency.TaskOwnerKey(string(taskID)), cmd, nil, func(runCtx context.Context, lease *concurrency.Lease) {
		if o.taskHeld(taskID) {
			if o.taskRequeueBarrier != nil {
				o.taskRequeueBarrier(taskID)
			}
			o.finishTaskFrom(taskID, assistants.StatusRunning, assistants.StatusQueued, "re-queued: paused before start")
			// A concurrent ContinueTask may have already cleared the hold and attempted to reschedule BEFORE
			// this requeue committed (it would have seen status still "running" and silently no-op'd, per
			// maybeScheduleTask's CAS precondition) — retry scheduling now that the task is actually "queued"
			// again, closing that lost-wakeup window. Use the OUTER ctx (spawnTask's own parameter), not
			// runCtx: runCtx belongs to the subprocess invocation that never actually started on this path.
			// Safe to call unconditionally when not held: maybeScheduleTask's own queued-only CAS precondition
			// means a redundant call here is always a safe no-op if someone else already won the
			// queued->running transition in the meantime.
			if !o.taskHeld(taskID) {
				o.maybeScheduleTask(ctx, taskID)
			}
			return
		}
		o.runHelperTask(runCtx, taskID, task)
	})
}

// finishTaskFrom durably transitions a task to a terminal (or, for the requeue case above, non-terminal)
// status, guarded by a Preconditions check against the status it is expected to be coming FROM — without
// this guard, a racing CancelTask and a task's own natural completion could each successfully overwrite the
// other's terminal status (applyTaskStatusChanged has no built-in "already terminal" guard of its own).
//
// extra bundles additional effects into the SAME ApplyRunOperation call as the status transition — used only
// by runHelperTask's success path to attach a task_result effect (taskResultEffect) so the result and the
// "completed" status land in one atomic control record (T2.4's "all effects of one operation commit
// together" invariant). The failure/cancel/requeue call sites all pass none.
func (o *Orchestrator) finishTaskFrom(taskID assistants.TaskID, from, to assistants.TaskStatus, reason string, extra ...state.Effect) {
	o.finishTaskTurn(taskID, 0, from, to, reason, extra...)
}

// finishTaskTurn is finishTaskFrom for a commit that also ends turn — a turn whose process ran to its end, so
// its input is consumed together with the outcome this commit records (state.TaskState.TurnInbox). turn 0 ends
// no turn: a cut-short turn keeps its input for the retry.
func (o *Orchestrator) finishTaskTurn(taskID assistants.TaskID, turn uint64, from, to assistants.TaskStatus, reason string, extra ...state.Effect) {
	o.finishTaskTurnReplying(taskID, turn, from, to, reason, nil, extra...)
}

// finishTaskTurnReplying is finishTaskTurn that also posts replies (planTurnReplies; none: nil) in the same
// commit.
//
// The messages a finish carries — replies and a child's result mirrored to its delegating parent — never keep
// the task from finishing. A reply whose message the agent answered itself meanwhile is dropped and the finish
// tried again. When the operation is rejected because of the others (the recipient's inbox or the thread is
// full, the thread is closed, the recipient has finished), the finish is committed again without them, together
// with the cancellation of the waits for those answers that nobody else can give (undeliveredReplyWaitEffects),
// and a notice says why the result was not delivered. A task never stays running without a process, and a
// requester never waits for an answer that was dropped.
func (o *Orchestrator) finishTaskTurnReplying(taskID assistants.TaskID, turn uint64, from, to assistants.TaskStatus, reason string, replies []plannedMessage, extra ...state.Effect) {
	reason = o.sanitizeTaskReason(reason)
	eff, err := taskStatusChangeEffect(taskID, state.TaskStatusChangedPayload{Status: string(to), Reason: reason, Turn: turn})
	if err != nil {
		return
	}
	effects := append([]state.Effect{eff}, extra...)
	rootStage := o.taskRootStage(taskID)
	parentID, hasParent := o.taskParent(taskID)

	// Agent Mesh T6.5: fold the result-delivery-to-parent mirror into THIS SAME commit as the status/result
	// effects above — see childResultMeshEffects' own doc comment. A build failure here is not expected in
	// practice (both ids already name known, just-read tasks) and must never block the task's own terminal
	// transition from landing, so it is logged and the finish proceeds without the mirror rather than failing
	// the whole operation.
	rs := o.opts.Store.Snapshot()
	var messages []state.Effect
	if to.Terminal() && hasParent {
		if mirror, merr := o.childResultMeshEffects(rs, parentID, taskID, reason, extra); merr != nil {
			log.Printf("WARN: assistants: child result mesh mirror (task=%s parent=%s): %v", taskID, parentID, merr)
		} else {
			messages = append(messages, mirror...)
		}
	}
	mirror := messages

	finishOp := func(effects []state.Effect, check func(*state.RunState) error) state.Operation {
		return state.Operation{
			ID:      "task-finish:" + string(taskID) + ":" + string(to) + ":" + randomOpSuffix(),
			Kind:    "finish_task",
			Expect:  state.Preconditions{TaskStatus: map[string]assistants.TaskStatus{string(taskID): from}, Check: check},
			Effects: effects,
		}
	}
	afterFinish := func(delivered []plannedMessage, endedReasons map[agentcomm.WaitID]string) func(state.OperationResult) {
		return func(res state.OperationResult) {
			o.publishNotice(bus.Event{Type: bus.EventAssistantTaskStatus, StageID: rootStage, Data: map[string]any{
				"task_id": string(taskID), "status": string(to), "reason": reason,
			}})
			// A turn reply is passive: announcing it is all its delivery needs — it wakes nobody, and the waits it
			// resolves (a reply_to the message it answers) are resumed below with the rest of this commit's.
			for _, d := range delivered {
				o.announceMessage(d.send, d.messageID, false)
			}
			// The waits this commit cancelled because their answer was dropped: their waiters continue.
			o.announceAndWakeEnded(res.EndedWaits, func(id agentcomm.WaitID) string { return endedReasons[id] })
			// T4a.5: a terminal child (pass, needs_changes, failure, or cancel — ANY terminal outcome, see
			// TestWaitingChildren_FailedChildDelivered) is the ONLY trigger that re-checks whether its parent's
			// whole waiting_children group just became ready. tryResumeParent's own CAS (not this check) is what
			// makes a resume happen exactly once even when several siblings finish at the same time.
			if to.Terminal() && hasParent {
				o.tryResumeParent(o.runContext(context.Background()), parentID)
			}
			// Этап 7 (T7.2): ANY task reaching a terminal status could be exactly what just closed its root stage's
			// required-completion barrier (directly, or transitively once tryResumeParent above settles an
			// ancestor) — re-attempt completion unconditionally; maybeCompleteAfterAssistants itself is a cheap
			// no-op for a stage that isn't currently awaiting_assistants.
			if to.Terminal() {
				// A finished task answers nobody any more: waits only it could answer end now.
				o.releaseUnanswerableWaitsAfterFinish()
				o.maybeCompleteAfterAssistants(o.runContext(context.Background()), rootStage)
				// Agent Mesh T6.3: this terminal transition may be exactly what the run-level completion barrier
				// (state.AssistantsState.RunBlocker, consulted by shouldExit) was waiting on — in particular for a
				// scope=run task (no RootStageID at all) or an advisory task whose root stage already finished,
				// neither of which maybeCompleteAfterAssistants above reaches (it only re-checks ONE stage's own
				// awaiting_assistants gate). Without this, Run's event loop would have nothing left to wake it once
				// every stage is already terminal, and would never re-evaluate shouldExit() again — the same
				// no-op marker WakeEventLoop already exists for (after-hooks, which also do not publish
				// EventAgentCompleted).
				o.concurrency.WakeEventLoop()
			}
			if to == assistants.StatusQueued {
				// queued never holds the run open either (state.RunBlocker): a run agent going idle after its turn
				// may be the last thing the run-level barrier was waiting on.
				o.concurrency.WakeEventLoop()
			}
			// Mirrors Send/PostDiscussionMessage's own tail: this commit can be exactly what a pending mesh Wait
			// needed — a task_result wait on this task (its terminal status, or a run agent's recorded turn
			// result), a reply_to wait answered by the turn reply, or an addressed_message wait on the delegate
			// thread the result was mirrored into — resume it the same way any other mesh write does.
			o.notifyAndResumeWaits(o.runContext(context.Background()), res.ReadyWaits)
		}
	}

	// Each rejected attempt drops what made it fail — a reply the agent answered itself meanwhile, the replies
	// that cannot be delivered (their requesters' waits are cancelled in the same commit), then the mirror, then
	// any remaining reply — so the loop ends after a few attempts with the task finished.
	var (
		storageErr  *state.StorageError
		undelivered []undeliveredReply
		dropped     error
		withMirror  = len(mirror) > 0
	)
	for range len(replies) + 3 {
		rs := o.opts.Store.Snapshot()
		all := slices.Clone(effects)
		if withMirror {
			all = append(all, mirror...)
		}
		var check func(*state.RunState) error
		for _, r := range replies {
			all = append(all, r.effects...)
		}
		if len(replies) > 0 {
			check = o.turnRepliesCheck(replies)
		}
		cancels, endedReasons := undeliveredReplyWaitEffects(rs, undelivered)
		all = append(all, cancels...)
		_, err = o.opts.Store.ApplyRunOperation(finishOp(all, check), afterFinish(replies, endedReasons))
		if err == nil || errors.As(err, &storageErr) {
			break
		}
		rs = o.opts.Store.Snapshot()
		if errors.Is(err, errTurnAlreadyAnswered) {
			if rest := unansweredReplies(rs, replies); len(rest) < len(replies) {
				replies = rest
				continue
			}
		}
		if dropped == nil {
			dropped = err
		}
		if good, bad := o.splitDeliverable(rs, replies); len(bad) > 0 {
			replies, undelivered = good, append(undelivered, bad...)
			continue
		}
		if withMirror {
			withMirror = false
			continue
		}
		if len(replies) == 0 {
			break // nothing left to drop: the rejection is the finish's own (e.g. the task moved on)
		}
		reason := "the answer to it could not be delivered: " + o.RedactSecrets(rejectionReason(err))
		for _, r := range replies {
			undelivered = append(undelivered, undeliveredReply{reply: r, reason: reason})
		}
		replies = nil
	}
	if err == nil && dropped != nil {
		var first *plannedMessage
		if len(undelivered) > 0 {
			first = &undelivered[0].reply
		}
		o.noticeResultNotDelivered(taskID, resultNoticeStage(o.opts.Store.Snapshot(), rootStage, first), dropped)
	}
	if err == nil && turn > 0 {
		if cerr := o.clearTurnInbox(taskID); cerr != nil {
			log.Printf("assistants: task %s: drop the finished turn's inbox cache: %v", taskID, cerr)
		}
	}
	o.handleTaskOperationErr(err)
}

// resultNoticeStage is the stage whose feed hears that a result was not delivered: the stage the turn reply was
// addressed to, if any, else the task's own root stage, else — for a run agent answering the human or another
// run agent — the stage the reply's conversation was opened from, whose feed shows that conversation.
func resultNoticeStage(rs state.RunState, rootStage string, reply *plannedMessage) string {
	if reply == nil {
		return rootStage
	}
	if stageID, ok := stageIDFromMeshParticipant(reply.send.Recipients[0]); ok {
		return stageID
	}
	if rootStage != "" {
		return rootStage
	}
	if th := rs.Assistants.Mesh.Threads[reply.send.ThreadID]; th != nil {
		return th.OriginStage
	}
	return ""
}
