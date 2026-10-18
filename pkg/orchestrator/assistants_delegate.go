// Package orchestrator (this file, T4a.2/T4a.3): admitting a GROUP of children under an existing task —
// "delegation", as opposed to assistants.go's Attach (one new TOP-LEVEL task, no parent). The checks here
// (depth/actor/quota, all atomically) are the single server-side admission gate the dev-plan requires to be
// shared by every path that can create a child under an existing task — today that is only the automatic
// delegation path (runHelperTask processing a kind:"delegate" turn, assistants_lifecycle.go); a later stage
// (dev-plan's T7/T8 area) wires an explicit UI/side-agent "create child" action through the SAME
// admitChildren rather than a second, parallel check.
package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// quotaReservedEffect builds a "quota_reserved" Effect reserving amount units of the whole-run budget —
// state.QuotaReservedPayload is the SAME shape applyQuotaReserved decodes (pkg/state/assistants_projection.go),
// mirroring taskCreatedEffect/taskStatusEffect/taskHoldEffect's existing convention in assistants.go.
func quotaReservedEffect(amount int) (state.Effect, error) {
	raw, err := json.Marshal(state.QuotaReservedPayload{Amount: amount})
	if err != nil {
		return state.Effect{}, fmt.Errorf("marshal quota_reserved payload: %w", err)
	}
	return state.Effect{Kind: "quota_reserved", Payload: raw}, nil
}

// taskContinuationEffect builds a "task_continuation_saved" Effect — assistants.Continuation's own json
// tags are the wire shape (mirrors taskResultEffect's reuse of assistants.Result directly, no mirror payload
// struct needed): applyTaskContinuationSaved decodes straight into it.
func taskContinuationEffect(taskID assistants.TaskID, cont *assistants.Continuation) (state.Effect, error) {
	raw, err := json.Marshal(cont)
	if err != nil {
		return state.Effect{}, fmt.Errorf("marshal task_continuation_saved payload: %w", err)
	}
	return state.Effect{Kind: "task_continuation_saved", Target: state.Target{TaskID: string(taskID)}, Payload: raw}, nil
}

// admitChildren is the one shared admission check for creating a GROUP of children under an existing,
// non-terminal parent task — depth, actor, policy revision ("generation") and whole-run quota are all
// validated against ONE snapshot and committed as ONE ApplyRunOperation: either every child in the group is
// created (plus, optionally, the parent's own transition/continuation — see below), or none of it is
// (TestAdmission_GroupIsAllOrNothing).
//
//   - actor: when actor.Kind == "assistant" (assistants.OriginAssistant), actor.ID MUST equal parentTaskID —
//     an assistant may only delegate to its OWN task (TestAdmission_OnlyOwnTaskCanGetChildren). A "user"/
//     "side_agent" actor is an explicit, already-confirmed action and is not subject to that self-match.
//   - parentToStatus/continuation: optional extra effects bundled into the SAME atomic operation as the
//     children — runHelperTask (T4a.3) passes StatusWaitingChildren + the model's requested continuation so
//     the parent's own "it is now waiting" transition can never land without its children, or vice versa. A
//     future explicit "create child under an already-settled task" caller passes "" / nil to skip both.
func (o *Orchestrator) admitChildren(
	ctx context.Context,
	operationID string,
	parentTaskID assistants.TaskID,
	children []assistants.DelegateRequest,
	actor state.Actor,
	parentToStatus assistants.TaskStatus,
	continuation *assistants.Continuation,
) ([]assistants.TaskID, error) {
	if !o.opts.AssistantsEnabled {
		return nil, &state.OperationRejectedError{Code: assistants.CodeFeatureDisabled, Reason: "collaboration is disabled"}
	}
	if operationID == "" {
		return nil, errors.New("assistants: operation id is required")
	}
	if len(children) == 0 {
		return nil, errors.New("assistants: at least one child is required")
	}
	if len(children) > assistants.MaxDelegateChildren {
		return nil, fmt.Errorf("assistants: %d children exceeds the cap of %d", len(children), assistants.MaxDelegateChildren)
	}
	if o.opts.AssistantsRegistry == nil {
		return nil, &state.OperationRejectedError{Code: assistants.CodeFeatureDisabled, Reason: "no assistant profiles configured"}
	}

	rs := o.opts.Store.Snapshot()
	parent, ok := rs.Assistants.Tasks[parentTaskID]
	if !ok {
		return nil, &state.OperationRejectedError{Code: assistants.CodeInvalidParent, Reason: fmt.Sprintf("unknown parent task %q", parentTaskID)}
	}
	if parent.Status.Terminal() {
		return nil, &state.OperationRejectedError{Code: assistants.CodeAncestorInactive, Reason: "parent task already finished"}
	}
	if actor.Kind == string(assistants.OriginAssistant) && actor.ID != string(parentTaskID) {
		return nil, &state.OperationRejectedError{Code: assistants.CodeInvalidParent, Reason: "an assistant may only delegate children to its own task"}
	}
	if actor.Kind == string(assistants.OriginAssistant) && !parent.ProfileSnapshot.Capabilities.Delegate {
		return nil, &state.OperationRejectedError{Code: assistants.CodeCapabilityUnavailable, Reason: "this profile is not permitted to delegate"}
	}

	depth := assistants.ComputeDepth(&parent.Task)
	if rs.Assistants.Policy.Blocks(depth) {
		return nil, &state.OperationRejectedError{Code: assistants.CodeDelegationDepthExceeded, Reason: fmt.Sprintf("max_depth=%d", rs.Assistants.Policy.MaxDepth)}
	}

	childIDs := make([]assistants.TaskID, len(children))
	effects := make([]state.Effect, 0, len(children)*2+2)
	parentIDStr := string(parentTaskID)
	for i, child := range children {
		profile, ok := o.opts.AssistantsRegistry.Get(child.ProfileID)
		if !ok {
			return nil, &state.OperationRejectedError{Code: assistants.CodeCapabilityUnavailable, Reason: fmt.Sprintf("unknown profile %q", child.ProfileID)}
		}
		view, _ := profileView(o.opts.AssistantsRegistry, child.ProfileID)
		if !view.Available {
			return nil, &state.OperationRejectedError{Code: assistants.CodeCapabilityUnavailable, Reason: view.UnavailableReason}
		}
		// Этап 10 (T10.2, "no self elevation"): an assistant delegating to ITS OWN task (actor.Kind ==
		// assistant, already self-match-checked above) must never be able to hand a child a LESS restricted
		// executor than its own profile's — otherwise a read-only-enforced helper could escape its own
		// guarantee simply by delegating to a profile with none. A "user"/"side_agent" actor's explicit,
		// already-confirmed child-creation is NOT subject to this (same carve-out as the self-match check
		// above, and for the same reason: a human/side-agent choosing a different profile for a child is an
		// affirmative decision, not the model elevating itself).
		if actor.Kind == string(assistants.OriginAssistant) && view.ReadonlyEnforcement.WeakerThan(parent.ProfileSnapshot.ReadonlyEnforcement) {
			return nil, &state.OperationRejectedError{Code: assistants.CodeExecutorNotPermitted, Reason: fmt.Sprintf("child profile %q (%s) is less restricted than the parent's own (%s)", child.ProfileID, view.ReadonlyEnforcement, parent.ProfileSnapshot.ReadonlyEnforcement)}
		}
		// Same freeze-at-admission-time rule as Attach (assistants.go) — see its own comment for why.
		profile.ReadonlyEnforcement = view.ReadonlyEnforcement

		childID := deriveTaskID(operationID + ":" + strconv.Itoa(i))
		childIDs[i] = childID
		mode := child.Mode
		if mode == "" {
			mode = assistants.ModeAdvisory
		}
		trigger := child.Trigger
		if trigger == "" {
			trigger = assistants.TriggerOneShot
		}

		created, err := taskCreatedEffect(childID, parent.RootStageID, state.TaskCreatedPayload{
			ParentTaskID:             &parentIDStr,
			ProfileID:                string(child.ProfileID),
			ProfileSnapshot:          profile,
			Title:                    child.Title,
			Instruction:              child.Instruction,
			Mode:                     string(mode),
			Trigger:                  string(trigger),
			PolicyRev:                rs.Assistants.Policy.Revision,
			MaxDepthAtCreate:         rs.Assistants.Policy.MaxDepth,
			CreatedBy:                actor.Kind,
			ParentGenerationAtCreate: parent.TaskGeneration,
			RootGenerationAtCreate:   rs.Assistants.RootGenerations[parent.RootStageID],
		})
		if err != nil {
			return nil, err
		}
		effects = append(effects, created)

		// The delegating parent task is, by construction, active right now (it is this very turn that
		// produced the delegate request) — its children start eligible to run immediately, same as Attach's
		// parentActive branch for a top-level task under an already-running stage.
		queued, err := taskStatusEffect(childID, assistants.StatusQueued, "")
		if err != nil {
			return nil, err
		}
		effects = append(effects, queued)
	}

	quotaEff, err := quotaReservedEffect(len(children))
	if err != nil {
		return nil, err
	}
	effects = append(effects, quotaEff)

	if parentToStatus != "" {
		parentEff, err := taskStatusEffect(parentTaskID, parentToStatus, "")
		if err != nil {
			return nil, err
		}
		effects = append(effects, parentEff)
	}
	if continuation != nil {
		contEff, err := taskContinuationEffect(parentTaskID, continuation)
		if err != nil {
			return nil, err
		}
		effects = append(effects, contEff)
	}

	var maxQuota *int
	if mt := o.opts.Config.Collaboration.Limits.MaxTasks; mt != nil {
		maxQuota = mt
	}

	op := state.Operation{
		ID:    operationID,
		Kind:  "attach_children",
		Actor: actor,
		Expect: state.Preconditions{
			PolicyRevision: rs.Assistants.Policy.Revision,
			TaskStatus:     map[string]assistants.TaskStatus{parentIDStr: parent.Status},
			MaxQuota:       maxQuota,
		},
		Effects: effects,
	}
	runCtx := o.runContext(ctx) // Инвариант №6: спавн не должен зависеть от HTTP request ctx.
	_, err = o.opts.Store.ApplyRunOperation(op, func(state.OperationResult) {
		for _, id := range childIDs {
			o.publishNotice(bus.Event{Type: bus.EventAssistantTaskCreated, StageID: parent.RootStageID, Data: map[string]any{
				"task_id": string(id), "parent_task_id": parentIDStr,
			}})
			o.maybeScheduleTask(runCtx, id)
		}
	})
	if err != nil {
		return nil, err
	}
	return childIDs, nil
}

// currentChildren filters parentID's ByParent set down to only the children stamped under
// currentParentGeneration (T4a.6) — a child created before the parent's most recent RetryTask belongs to a
// superseded attempt and must be invisible to every decision that reads "the parent's children" (required-
// gate checks, waiting_children readiness, the context a resumed parent sees). Shared by requiredChildOpen,
// tryResumeParent, and collectChildResultsJSON (assistants_context.go) so the one rule lives in one place.
func currentChildren(rs state.RunState, parentID assistants.TaskID, currentParentGeneration uint64) []assistants.TaskID {
	var out []assistants.TaskID
	for _, cid := range rs.Assistants.ByParent[parentID] {
		if c, ok := rs.Assistants.Tasks[cid]; ok && c.ParentGeneration == currentParentGeneration {
			out = append(out, cid)
		}
	}
	return out
}

// delegateOperationID derives a stable, per-turn idempotency key for the admission group a task's own
// kind:"delegate" turn produces — reusing taskID's ContextVersion (the context the model actually answered
// against) means a retried/duplicated classification of the SAME turn (e.g. a crash right after the
// subprocess returned but before this admission committed, followed by a resumed retry that reproduces an
// identical raw answer) lands on the SAME operation id and is therefore a true idempotent no-op rather than
// a second, duplicate group of children (dev-plan's T4a.7 "SameRequestIdDoesNotCreateDuplicates").
func delegateOperationID(taskID assistants.TaskID, contextVersion assistants.ContextVersion) string {
	return "delegate:" + string(taskID) + ":" + string(contextVersion)
}

// handleDelegate processes a decoded kind:"delegate" turn (T4a.3): admits the requested children and, in the
// SAME atomic operation as their creation, moves taskID itself to waiting_children with the model's
// requested (or a default on_completion) continuation saved. Called from runHelperTask right before it
// returns — by construction that is also the moment spawnTask's SpawnOwned releases this task's command-slot
// lease and clears its "task:" activity marker (T4a.4: no extra code needed here to "release the lease
// before waiting", it falls out of handleDelegate doing nothing further after admitChildren commits).
func (o *Orchestrator) handleDelegate(ctx context.Context, taskID assistants.TaskID, contextVersion assistants.ContextVersion, delegate assistants.Delegate) {
	continuation := delegate.Continuation
	if continuation == nil {
		// No explicit continuation trigger named — the only sensible default is "wake me once every
		// requested child has finished" (assistants.TriggerOnCompletion); dev-plan.md's Continuation doc
		// comment names on_completion/checkpoints as the two realistic values here.
		continuation = &assistants.Continuation{Trigger: assistants.TriggerOnCompletion}
	}
	actor := state.Actor{Kind: string(assistants.OriginAssistant), ID: string(taskID)}
	opID := delegateOperationID(taskID, contextVersion)
	if _, err := o.admitChildren(ctx, opID, taskID, delegate.Children, actor, assistants.StatusWaitingChildren, continuation); err != nil {
		// T4a.7 gives the parent a typed, actionable rejection instead of a bare failure — see
		// assistants_reject.go's deliverDelegationRejection.
		o.deliverDelegationRejection(taskID, err)
		return
	}
	// Successful delegation resets the consecutive-rejection guard: the task reacted correctly, so a future
	// rejection starts counting from zero again.
	o.taskDelegationRejects.Delete(taskID)
}

// requiredChildOpen reports whether taskID has at least one DIRECT required child that has not (yet, or
// ever will) closed the required gate — a non-terminal required child, or one that finished WITHOUT a
// verdict:"pass" result, keeps it open. Used to reject a parent's own verdict:"pass" turn while that is still
// true (dev-plan.md's TestResult_ConsistencyWithRequiredChildren, enforced here — see
// pkg/assistants/delegate_test.go's note on why it cannot be decided inside pkg/assistants alone).
func (o *Orchestrator) requiredChildOpen(taskID assistants.TaskID) bool {
	rs := o.opts.Store.Snapshot()
	parent, ok := rs.Assistants.Tasks[taskID]
	if !ok {
		return false
	}
	for _, cid := range currentChildren(rs, taskID, parent.TaskGeneration) {
		c := rs.Assistants.Tasks[cid]
		if c.Mode != assistants.ModeRequired {
			continue
		}
		// Этап 7 (T7.6): a human-waived child no longer keeps the parent's own verdict:"pass" blocked — the
		// same rule AssistantsState.RequiredBlocker/taskGateClosed already applies at the root-stage level.
		// Without this, waiving a nested required child would unblock the ROOT stage's barrier but leave this
		// protocol check rejecting the parent's own pass forever — a human waiver must actually route around
		// every gate the child would otherwise hold closed, not just the outermost one.
		if c.Waived {
			continue
		}
		if !c.Status.Terminal() {
			return true
		}
		if c.Status != assistants.StatusCompleted {
			return true // failed/cancelled required child: the gate can never close for this attempt
		}
		var result assistants.Result
		raw := o.previousTaskResultJSON(cid)
		if err := json.Unmarshal([]byte(raw), &result); err != nil || !result.Verdict.ClosesGate() {
			return true
		}
	}
	return false
}

// resumeTaskViaCAS is the shared CAS-and-requeue step behind BOTH tryResumeParent (waiting_children -> queued,
// T4a.5) and tryResumePeer (waiting_peer -> queued, T5.4): build a FRESH, never-reused operation id, commit a
// "queued" transition guarded by a TaskStatus precondition on from, publish the task's new status, and let
// maybeScheduleTask take it the rest of the way. The precondition IS the "resume exactly once" mechanism, not
// a side signal — called redundantly (two siblings finishing at the same moment; a live resume racing a
// startup recovery sweep over the same task; two callers racing the same already-resolved wait) is always
// safe: the loser's ApplyRunOperation simply hits the stale-revision rejection and is a silent no-op
// (handleTaskOperationErr classifies it the same way maybeScheduleTask's own lost CAS races already are).
func (o *Orchestrator) resumeTaskViaCAS(ctx context.Context, taskID assistants.TaskID, rootStageID string, from assistants.TaskStatus, reason string) {
	eff, err := taskStatusEffect(taskID, assistants.StatusQueued, reason)
	if err != nil {
		return
	}
	op := state.Operation{
		ID:      "task-resume:" + string(taskID) + ":" + randomOpSuffix(),
		Kind:    "resume_task",
		Expect:  state.Preconditions{TaskStatus: map[string]assistants.TaskStatus{string(taskID): from}},
		Effects: []state.Effect{eff},
	}
	_, err = o.opts.Store.ApplyRunOperation(op, func(state.OperationResult) {
		o.publishNotice(bus.Event{Type: bus.EventAssistantTaskStatus, StageID: rootStageID, Data: map[string]any{
			"task_id": string(taskID), "status": string(assistants.StatusQueued),
		}})
		o.maybeScheduleTask(ctx, taskID)
	})
	o.handleTaskOperationErr(err)
}

// tryResumeParent moves parentID from waiting_children back to queued EXACTLY ONCE when every one of its
// current children has reached a terminal status — the CAS precondition (TaskStatus: waiting_children) IS
// the "exactly once" mechanism, not a side signal: called redundantly (two siblings finishing at the same
// moment, T4a.5's TestContinuation_TwoChildrenFinishSimultaneously_OneResume; or a live finish racing
// startup recovery's own pass over the same task) is always safe — see resumeTaskViaCAS's own doc comment.
// Re-evaluating ByParent fresh on every call (rather than trusting a cached "N remaining" counter) is also
// what makes a NEW required child added mid-wait correctly reopen readiness
// (TestWaitingChildren_NewRequiredChildReopensReadiness) — there is nothing to invalidate, the set is just
// read again.
func (o *Orchestrator) tryResumeParent(ctx context.Context, parentID assistants.TaskID) {
	rs := o.opts.Store.Snapshot()
	parent, ok := rs.Assistants.Tasks[parentID]
	if !ok || parent.Status != assistants.StatusWaitingChildren || parent.Continuation == nil {
		return
	}
	children := currentChildren(rs, parentID, parent.TaskGeneration)
	if len(children) == 0 {
		return
	}
	for _, cid := range children {
		c := rs.Assistants.Tasks[cid]
		if !c.Status.Terminal() {
			return // at least one CURRENT-generation child is still open — not ready yet
		}
	}
	o.resumeTaskViaCAS(ctx, parentID, parent.RootStageID, assistants.StatusWaitingChildren, "resumed: all children finished")
}

// recoverAssistants re-activates every non-terminal helper task a FRESH process inherited from the journal
// at startup (T4a.3/mirrors T2.7's stage-level startPlanningForPending, but for the independent task
// lifecycle): "running" never survives a restart (its subprocess died with the old process) and must be
// requeued; "queued" never got its spawnTask goroutine started in THIS process; "waiting_children" may have
// had every child finish before the crash, in which case tryResumeParent's own CAS is what decides whether
// it is actually ready (calling it redundantly for an already-ready task is always safe — see its own doc
// comment). Call sites that already hold a task (paused) are left untouched: holds survive the journal
// unconditionally and ContinueTask remains the only way to clear one.
func (o *Orchestrator) recoverAssistants(ctx context.Context) {
	if !o.opts.AssistantsEnabled {
		return
	}
	rs := o.opts.Store.Snapshot()
	ids := make([]string, 0, len(rs.Assistants.Tasks))
	for id := range rs.Assistants.Tasks {
		ids = append(ids, string(id))
	}
	slices.Sort(ids) // deterministic order for reproducible tests; not a correctness requirement

	for _, idStr := range ids {
		id := assistants.TaskID(idStr)
		t, ok := rs.Assistants.Tasks[id]
		if !ok {
			continue
		}
		// Этап 5: rebuild the in-memory Collector/CheckpointStore binding for any non-terminal watcher task —
		// the binding itself is never durable (see watcherBinding's doc comment, watch_tasks.go), so a fresh
		// process must recreate it before the poller can ever fire this task again. A watcher in "watching"
		// falls through the switch below unchanged (status recovery, not binding, is this switch's job) —
		// binding it here is what lets a RESUMED watcher resume watching, instead of being stuck forever.
		if !t.Status.Terminal() && isWatcherTrigger(t.Trigger) {
			o.bindWatcher(id, t.Task)
		}
		switch t.Status {
		case assistants.StatusRunning:
			o.finishTaskFrom(id, assistants.StatusRunning, assistants.StatusQueued, "resumed after restart")
			o.maybeScheduleTask(ctx, id)
		case assistants.StatusQueued:
			o.maybeScheduleTask(ctx, id)
		case assistants.StatusWaitingChildren:
			o.tryResumeParent(ctx, id)
		case assistants.StatusWaitingPeer:
			// T5.4: a helper task whose mesh wait resolved right before a crash, with the live resume never
			// happening because the process went down between that commit and the requeue — mirrors
			// recoverMeshWaits' role for a stage's own awaiting_peer. tryResumePeer's own readiness check
			// (hasOpenMeshWaitFor) makes calling it here unconditionally safe: a wait still genuinely pending
			// leaves the task exactly where it is, untouched, for a future live Send/Publish to wake normally.
			o.tryResumePeer(ctx, id)
		default:
			// waiting_parent/watching/paused/terminal: nothing for a fresh process to do on its own —
			// waiting_parent is reactivated by the stage's own startup path, paused stays paused until an
			// explicit ContinueTask, and a terminal status needs no recovery at all.
		}
	}
}
