// Package orchestrator (this file, T4a.2/T4a.3): admitting a GROUP of children under an existing task —
// "delegation", as opposed to assistants.go's Attach (one new TOP-LEVEL task, no parent). The checks here
// (depth/actor/quota, all atomically) are the single server-side admission gate the dev-plan requires to be
// shared by every path that can create a child under an existing task — today that is only the automatic
// delegation path (runHelperTask processing a kind:"delegate" turn, assistants_lifecycle.go); a later stage
// (dev-plan's T7/T8 area) wires an explicit UI/side-agent "create child" action through the SAME
// admitChildren rather than a second, parallel check.
package orchestrator

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"slices"
	"strconv"

	"github.com/akopichin/afm/pkg/agentcomm"
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
	return o.admitChildrenWithParentChange(ctx, operationID, parentTaskID, children, actor,
		state.TaskStatusChangedPayload{Status: string(parentToStatus)}, continuation, nil)
}

// admitChildrenWithParentChange is admitChildren with the parent's whole status change — handleDelegate's
// change also ends the delegating turn (TaskStatusChangedPayload.Turn). An empty Status skips it.
func (o *Orchestrator) admitChildrenWithParentChange(
	ctx context.Context,
	operationID string,
	parentTaskID assistants.TaskID,
	children []assistants.DelegateRequest,
	actor state.Actor,
	parentTo state.TaskStatusChangedPayload,
	continuation *assistants.Continuation,
	check func(*state.RunState) error,
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
	// A retry of an already-committed admission replays it, whatever changed since (the config, the parent's status).
	if op, committed, err := o.opts.Store.CommittedOperation(operationID, opKindAttachChildren); err != nil || committed {
		if err != nil {
			return nil, err
		}
		return o.replayedChildren(op, operationID, parentTaskID, children, actor, parentTo, continuation)
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
	parentExec := o.taskExecution(rs, parent.Task)
	_, parentEnforcement := o.helperCommandAccess(parentExec.Command)
	for i, child := range children {
		profile, ok := o.opts.AssistantsRegistry.Get(child.ProfileID)
		if !ok {
			return nil, &state.OperationRejectedError{Code: assistants.CodeCapabilityUnavailable, Reason: fmt.Sprintf("unknown profile %q", child.ProfileID)}
		}
		// Same freeze-at-admission rule as a top-level task; a child without its own command runs its parent's.
		profile, binding, err := o.admitExecution(profile, o.executionInputs(parent.RootStageID, &parentExec))
		if err != nil {
			return nil, err
		}
		// "No self elevation": an assistant delegating to its own task must not hand a child a less restricted
		// executor than the one it runs itself here, or a read-only helper could escape its guarantee by delegating.
		// A human's or the side agent's confirmed choice of a child profile is not the model elevating itself.
		if actor.Kind == string(assistants.OriginAssistant) && profile.ReadonlyEnforcement.WeakerThan(parentEnforcement) {
			return nil, &state.OperationRejectedError{Code: assistants.CodeExecutorNotPermitted, Reason: fmt.Sprintf("child profile %q (%s) is less restricted than the parent's own (%s)", child.ProfileID, profile.ReadonlyEnforcement, parentEnforcement)}
		}

		childID := childTaskID(operationID, i)
		childIDs[i] = childID
		child = withChildDefaults(child)

		created, err := taskCreatedEffect(childID, parent.RootStageID, state.TaskCreatedPayload{
			ParentTaskID:             &parentIDStr,
			ProfileID:                string(child.ProfileID),
			ProfileSnapshot:          o.redactProfile(profile),
			Title:                    o.RedactSecrets(child.Title),
			Instruction:              o.RedactSecrets(child.Instruction),
			Mode:                     string(child.Mode),
			Trigger:                  string(child.Trigger),
			PolicyRev:                rs.Assistants.Policy.Revision,
			MaxDepthAtCreate:         rs.Assistants.Policy.MaxDepth,
			CreatedBy:                actor.Kind,
			ParentGenerationAtCreate: parent.TaskGeneration,
			RootGenerationAtCreate:   rs.Assistants.RootGenerations[parent.RootStageID],
			Execution:                &binding,
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

	if parentTo.Status != "" {
		parentEff, err := taskStatusChangeEffect(parentTaskID, parentTo)
		if err != nil {
			return nil, err
		}
		effects = append(effects, parentEff)
	}
	if continuation != nil {
		redacted := *continuation
		redacted.Note = o.RedactSecrets(redacted.Note) // the model's own text, journaled
		contEff, err := taskContinuationEffect(parentTaskID, &redacted)
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
		Kind:  opKindAttachChildren,
		Actor: actor,
		Expect: state.Preconditions{
			PolicyRevision: rs.Assistants.Policy.Revision,
			TaskStatus:     map[string]assistants.TaskStatus{parentIDStr: parent.Status},
			MaxQuota:       maxQuota,
			Check:          check,
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

// opKindAttachChildren is the kind a child admission commits under; a retry reads it back as that kind.
const opKindAttachChildren = "attach_children"

// childTaskID is the id of the i-th child an admission creates: derived from its operation, so a retry finds it.
func childTaskID(operationID string, i int) assistants.TaskID {
	return deriveTaskID(operationID + ":" + strconv.Itoa(i))
}

// withChildDefaults is the child as admission records it: advisory and one_shot unless it names otherwise.
func withChildDefaults(child assistants.DelegateRequest) assistants.DelegateRequest {
	child.Mode = cmp.Or(child.Mode, assistants.ModeAdvisory)
	child.Trigger = cmp.Or(child.Trigger, assistants.TriggerOneShot)
	return child
}

// replayedChildren answers a committed child admission without resolving executors again: only the whole same request
// (children, actor kind, parent transition and continuation, set or omitted) gets the original children back, anything
// else under that id is a conflict. Stored text was redacted at commit, so it is compared as redacted text.
func (o *Orchestrator) replayedChildren(op state.CommittedOperation, operationID string, parentTaskID assistants.TaskID,
	children []assistants.DelegateRequest, actor state.Actor, parentTo state.TaskStatusChangedPayload, continuation *assistants.Continuation,
) ([]assistants.TaskID, error) {
	conflict := state.OperationIDConflictError(operationID)
	var childIDs []assistants.TaskID
	var savedParentTo *state.TaskStatusChangedPayload
	var savedContinuation *assistants.Continuation
	for _, e := range op.Effects {
		switch {
		case e.Kind == "task_created":
			var p state.TaskCreatedPayload
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return nil, fmt.Errorf("decode committed task_created: %w", err)
			}
			i := len(childIDs)
			if i == len(children) {
				return nil, conflict
			}
			child := withChildDefaults(children[i])
			same := assistants.TaskID(e.Target.TaskID) == childTaskID(operationID, i) && p.ParentTaskID != nil &&
				*p.ParentTaskID == string(parentTaskID) && p.ProfileID == string(child.ProfileID) &&
				o.sameRedactedText(p.Title, child.Title) && o.sameRedactedText(p.Instruction, child.Instruction) &&
				p.Mode == string(child.Mode) && p.Trigger == string(child.Trigger) && p.CreatedBy == actor.Kind
			if !same {
				return nil, conflict
			}
			childIDs = append(childIDs, assistants.TaskID(e.Target.TaskID))
		case e.Kind == taskStatusChangedEffectKind && e.Target.TaskID == string(parentTaskID):
			savedParentTo = new(state.TaskStatusChangedPayload)
			if err := json.Unmarshal(e.Payload, savedParentTo); err != nil {
				return nil, fmt.Errorf("decode committed task_status_changed: %w", err)
			}
		case e.Kind == "task_continuation_saved":
			savedContinuation = new(assistants.Continuation)
			if err := json.Unmarshal(e.Payload, savedContinuation); err != nil {
				return nil, fmt.Errorf("decode committed task_continuation_saved: %w", err)
			}
		default: // the children's queued records and the quota reservation follow from the child list
		}
	}
	if len(childIDs) != len(children) {
		return nil, conflict
	}
	sameParentTo, err := sameStatusChange(savedParentTo, parentTo)
	if err != nil {
		return nil, err
	}
	if !sameParentTo || !o.sameContinuation(savedContinuation, continuation) {
		return nil, conflict
	}
	return childIDs, nil
}

// sameStatusChange reports whether a committed parent change is the requested one; an empty requested status
// asks for none. Both sides are compared as encoded, as admission journals them.
func sameStatusChange(saved *state.TaskStatusChangedPayload, requested state.TaskStatusChangedPayload) (bool, error) {
	if saved == nil || requested.Status == "" {
		return saved == nil && requested.Status == "", nil
	}
	a, err := json.Marshal(saved)
	if err != nil {
		return false, err
	}
	b, err := json.Marshal(requested)
	if err != nil {
		return false, err
	}
	return bytes.Equal(a, b), nil
}

// sameContinuation reports whether a committed continuation is the requested one; nil asks for none.
func (o *Orchestrator) sameContinuation(saved, requested *assistants.Continuation) bool {
	if saved == nil || requested == nil {
		return saved == nil && requested == nil
	}
	return saved.Trigger == requested.Trigger && o.sameRedactedText(saved.Note, requested.Note)
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

// childResultMeshEffects mirrors a terminal, CURRENT-generation child's outcome into a dedicated mesh thread
// between it and its delegating parent — bundled into finishTaskFrom's SAME ApplyRunOperation as the child's
// own task_status_changed/task_result effects (T6.5: "a child's result reaching its parent is one durable
// operation that both records the result and wakes the parent"). The wake itself (tryResumeParent) stays its
// own follow-on, CAS-guarded resume — see that function's doc comment for why it cannot be folded into THIS
// same commit: the parent's readiness can only be decided by re-reading state AFTER this commit lands, since
// a sibling may finish in the very same instant (TestContinuation_TwoChildrenFinishSimultaneously_OneResume).
// What THIS function folds in is making the result observable through the mesh's one delivery primitive
// (agentCommInboxFor/Service.ReadInbox — the SAME read a reply from a peer helper already uses), instead of
// ONLY through the bespoke collectChildResultsJSON read (assistants_context.go) — so there is exactly one "a
// participant learns it has new input" mechanism, not two independent ones (dev-plan.md's T6.5 single-inbox
// requirement). collectChildResultsJSON itself is left untouched and stays what buildTaskContext renders for
// a resumed parent's own "Results from the children you delegated to" section — no existing delegation
// test's expected prompt text changes because of this mirror.
//
// The mirror is a passive turn result (AutoResult): the parent is resumed by its children finishing
// (tryResumeParent), never by this message, and its own turn result is never posted back to a finished child.
// Its effects are message effects of the finish operation: a rejection (the parent's inbox is full) commits the
// child's outcome without them (finishTaskTurnReplying).
//
// Returns (nil, nil) — no error, no effects — for a task with no parent, or for a STALE child (currentChildren's
// own rule: one from a generation the parent has since superseded via RetryTask/BumpRootGeneration) — a dead
// branch's outcome must never be injected into the parent's LIVE inbox after it has already moved on.
func (o *Orchestrator) childResultMeshEffects(rs state.RunState, parentID, childID assistants.TaskID, reason string, extra []state.Effect) ([]state.Effect, error) {
	child, ok := rs.Assistants.Tasks[childID]
	if !ok {
		return nil, nil
	}
	parent, ok := rs.Assistants.Tasks[parentID]
	if !ok || child.ParentGeneration != parent.TaskGeneration {
		return nil, nil
	}

	members := []agentcomm.ParticipantID{meshHelperParticipantID(parentID), meshHelperParticipantID(childID)}
	threadID := deriveMeshThreadID("delegate-thread:" + string(parentID) + ":" + string(childID))
	// The human reads along (D1). A thread opened before that keeps its members: it is not re-opened.
	var effects []state.Effect
	if _, exists := rs.Assistants.Mesh.Threads[threadID]; !exists {
		var err error
		effects, err = threadOpenEffects(rs, threadID, "", agentcomm.ScopeStage, append([]agentcomm.ParticipantID{meshUserParticipantID}, members...), 0, participantStage(rs, members[0]))
		if err != nil {
			return nil, fmt.Errorf("child result mesh mirror: %w", err)
		}
	}

	// The message body carries the SAME content collectChildResultsJSON would show for this child: its
	// decoded task_result payload when one exists (located by Kind among extra — finishTaskFrom's success
	// path bundles it as a SECOND effect of the very same operation, see taskResultEffect's doc comment), or
	// finishTaskFrom's own reason text for a terminal child that never produced one (a failed/cancelled child
	// never has a task_result — TestLifecycle_FailurePathHasNoResultEvent/TestCancel_HasNoResultEvent).
	body := reason
	for _, e := range extra {
		if e.Kind == taskResultEffectKind {
			body = string(e.Payload)
			break
		}
	}
	body = truncateMeshBody(o.RedactSecrets(body))

	msgEff, err := messageSentEffect(SendRequest{
		ThreadID: threadID, Sender: members[1], Recipients: []agentcomm.ParticipantID{members[0]},
		Kind: agentcomm.MessageKindMessage, Body: body, AutoResult: true,
	}, deriveMeshMessageID("delegate-result:"+string(childID)))
	if err != nil {
		return nil, err
	}
	effects = append(effects, msgEff)
	return effects, nil
}

// childResultMeshBodyLimit caps a mirrored mesh message body well under agentcomm.MaxBodyBytes. A child's
// full result JSON (findings/evidence included) could in principle be large enough to trip
// agentcomm.ValidateMessage's own limit — and that rejection would fail childResultMeshEffects' CALLER's
// whole ApplyRunOperation, since the mirror effects are bundled into the same commit as the child's own
// terminal status transition. A large result must never be able to block a task from finishing. The
// untruncated result stays available exactly as before, via the task's own result.json/
// collectChildResultsJSON — this mirror is an ADDITIONAL read path, not the only one.
const childResultMeshBodyLimit = 4000

func truncateMeshBody(body string) string {
	if len(body) <= childResultMeshBodyLimit {
		return body
	}
	return body[:childResultMeshBodyLimit] + "\n... (truncated — see the task's own result for the full record)"
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
// turn is the delegating turn, ended by the same commit (its input consumed); 0 ends none.
func (o *Orchestrator) handleDelegate(ctx context.Context, taskID assistants.TaskID, turn uint64, contextVersion assistants.ContextVersion, delegate assistants.Delegate, guard func(*state.RunState) error) error {
	continuation := delegate.Continuation
	if continuation == nil {
		// No explicit continuation trigger named — the only sensible default is "wake me once every
		// requested child has finished" (assistants.TriggerOnCompletion); dev-plan.md's Continuation doc
		// comment names on_completion/checkpoints as the two realistic values here.
		continuation = &assistants.Continuation{Trigger: assistants.TriggerOnCompletion}
	}
	actor := state.Actor{Kind: string(assistants.OriginAssistant), ID: string(taskID)}
	opID := delegateOperationID(taskID, contextVersion)
	parentTo := state.TaskStatusChangedPayload{Status: string(assistants.StatusWaitingChildren), Turn: turn}
	if _, err := o.admitChildrenWithParentChange(ctx, opID, taskID, delegate.Children, actor, parentTo, continuation, guard); err != nil {
		if errors.Is(err, errHelperOutputSuperseded) {
			return err
		}
		if guard != nil {
			rs := o.opts.Store.Snapshot()
			if guardErr := guard(&rs); guardErr != nil {
				return guardErr
			}
		}
		// T4a.7 gives the parent a typed, actionable rejection instead of a bare failure — see
		// assistants_reject.go's deliverDelegationRejection.
		return o.deliverDelegationRejection(taskID, turn, err, guard)
	}
	// Successful delegation resets the consecutive-rejection guard: the task reacted correctly, so a future
	// rejection starts counting from zero again.
	o.taskDelegationRejects.Delete(taskID)
	// The commit consumed the turn's input: drop its cache, as finishTaskTurn does for every other outcome.
	if turn > 0 {
		if err := o.clearTurnInbox(taskID); err != nil {
			log.Printf("assistants: task %s: drop the delegating turn's inbox cache: %v", taskID, err)
		}
	}
	return nil
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
		// Idle again: what the waiting turn read and left is unanswerable, even if a hold keeps the next turn off.
		o.releaseUnanswerableWaitsAfterFinish()
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
	o.settleAssistantsAfterRestart()
	o.resumeAssistantsAfterRestart(ctx)
}

// settleAssistantsAfterRestart is recoverAssistants' first half: it rebuilds the watcher bindings and puts the
// turns a crash cut short back in the queue, and starts nothing: Run decides the waits for requests an idle agent
// left between the halves, before a new turn makes it busy.
func (o *Orchestrator) settleAssistantsAfterRestart() {
	defer o.helpersSettled.Store(true)
	if !o.opts.AssistantsEnabled {
		return
	}
	rs := o.opts.Store.Snapshot()
	for _, id := range slices.Sorted(maps.Keys(rs.Assistants.Tasks)) {
		t := rs.Assistants.Tasks[id]
		// Этап 5: rebuild the in-memory Collector/CheckpointStore binding for any non-terminal watcher task —
		// the binding itself is never durable (see watcherBinding's doc comment, watch_tasks.go), so a fresh
		// process must recreate it before the poller can ever fire this task again. A watcher in "watching"
		// needs nothing else: binding it is what lets a RESUMED watcher resume watching, instead of being stuck.
		if !t.Status.Terminal() && isWatcherTrigger(t.Trigger) {
			o.reconcileWatcherBinding(id)
		}
		if t.Status == assistants.StatusRunning {
			o.finishTaskFrom(id, assistants.StatusRunning, assistants.StatusQueued, "resumed after restart")
		}
		// The failure may have come before the crash, or under a binary that did not end the pass.
		if isCompletionHelper(t.Task) && rs.Stages[t.RootStageID].Status == state.StatusFailed {
			o.cancelCompletionPassOfFailedStage(id)
		}
	}
}

// resumeAssistantsAfterRestart is recoverAssistants' second half: it starts what the restart left ready.
func (o *Orchestrator) resumeAssistantsAfterRestart(ctx context.Context) {
	if !o.opts.AssistantsEnabled {
		return
	}
	rs := o.opts.Store.Snapshot()
	for _, id := range slices.Sorted(maps.Keys(rs.Assistants.Tasks)) {
		t := rs.Assistants.Tasks[id]
		switch t.Status {
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
			// T5.5: that still-pending wait does not rule out a counter-question whose own live trigger was
			// lost to the SAME crash — recover it the same way recoverMeshWaits does for a stage. Safe to call
			// unconditionally for the same reason tryResumePeer above is: both conditions it checks
			// (hasOpenMeshWaitFor, hasWakingUnread) are re-derived fresh, and the resume it triggers is
			// CAS-protected, so finding nothing to do (or racing tryResumePeer's own resume above) is a no-op.
			o.maybeResumeAwaitingPeerQuestion(ctx, meshParticipantForTask(o.opts.Store.Snapshot(), t.Task))
		case assistants.StatusWatching:
			// The crash may have come between the stage's result and the pass it owes this helper.
			if isCompletionHelper(t.Task) {
				o.issueCompletionPasses(ctx, t.RootStageID)
			}
		default:
			// waiting_parent/paused/terminal: nothing for a fresh process to do on its own —
			// waiting_parent is reactivated by the stage's own startup path, paused stays paused until an
			// explicit ContinueTask, and a terminal status needs no recovery at all.
		}
	}
}
