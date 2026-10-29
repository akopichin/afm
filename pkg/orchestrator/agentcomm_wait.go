package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
	"github.com/akopichin/afm/pkg/state"
)

// meshWaitExpiredReason/meshWaitRunClosedReason (Agent Mesh Phase 5, T5.6) are the stored, visible Reason
// values for the two system-driven (not human/agent-requested) terminal wait outcomes this file adds.
const (
	meshWaitExpiredReason   = "deadline exceeded"
	meshWaitRunClosedReason = "the run closed while this wait was still pending"
	// meshWaitSupersededReason ends the waits a stage left open when Revise starts its next attempt.
	meshWaitSupersededReason = "the stage was revised: this wait belonged to its previous attempt"
)

// Agent Mesh Phase 2, T2.3: Wait/CancelWait and the cycle check. Registration and the "is this already
// satisfied" check happen inside the SAME ApplyRunOperation (applyMeshWaitRegistered,
// pkg/state/mesh_waits.go) — a reply that arrived before Wait was called still resolves the wait on the
// very first read after commit, with no separate catch-up pass.

// MaxBatchWaits is the most requests one batch wait may name.
const MaxBatchWaits = 16

// ErrWaitRequiresDeadline is returned when a wait has no known producer yet — a board_key never published, or
// a thread not opened yet — and the caller did not set a Deadline: an unreachable producer must not be allowed
// to wait forever with no visible way out (tmp/agent-mesh.md §6.3).
var ErrWaitRequiresDeadline = errors.New("agentcomm: a wait with no known producer requires a deadline")

// requireDeadline rejects a speculative wait (its producer may never appear) that has no deadline.
func requireDeadline(req WaitRequest, what string) error {
	if req.Deadline != nil {
		return nil
	}
	return &state.OperationRejectedError{
		Code: assistants.CodeInvalidRequest, Reason: fmt.Sprintf("%s: %v (pass --deadline-seconds)", what, ErrWaitRequiresDeadline), Cause: ErrWaitRequiresDeadline,
	}
}

// waitConditionHint explains what an invalid wait condition is missing, so the agent can fix the call.
func waitConditionHint(kind agentcomm.WaitConditionKind) string {
	switch kind {
	case agentcomm.WaitOnReplyTo:
		return "condition reply_to needs --message-id"
	case agentcomm.WaitOnTaskResult:
		return "condition task_result needs --task-id"
	case agentcomm.WaitOnBoardKey:
		return "condition board_key needs --key and --status"
	case agentcomm.WaitOnStageStatus:
		return "condition stage_status needs --stage-id and --status"
	default:
		return fmt.Sprintf("unknown wait condition %q (want reply_to|addressed_message|task_result|board_key|stage_status)", kind)
	}
}

// waitRules are the rule flags a wait is registered with: a task_result wait wants a fresh result, a reply_to
// wait a reply from a recipient. A retry of a request committed before a flag existed is the same request: it
// keeps the committed record's flags, so it replays the original wait instead of conflicting with it.
func (o *Orchestrator) waitRules(req WaitRequest) (freshOnly, recipientOnly bool, err error) {
	freshOnly = req.Condition.Kind == agentcomm.WaitOnTaskResult
	recipientOnly = req.Condition.Kind == agentcomm.WaitOnReplyTo
	op, committed, err := o.opts.Store.CommittedOperation(req.OperationID, opKindMeshWait)
	if errors.Is(err, state.ErrOperationIDConflict) || (err == nil && !committed) {
		return freshOnly, recipientOnly, nil // a new request, or one the commit itself will reject as a conflict
	}
	if err != nil {
		return false, false, err
	}
	var p state.MeshWaitRegisteredPayload
	if _, found, err := op.Effect("mesh_wait_registered", &p); err != nil || !found {
		return freshOnly, recipientOnly, err
	}
	return p.FreshResultOnly, p.RecipientReplyOnly, nil
}

// Wait implements Service.Wait.
func (o *Orchestrator) Wait(ctx context.Context, req WaitRequest) (WaitReceipt, error) {
	if req.OperationID == "" {
		return WaitReceipt{}, errors.New("agentcomm: operation id is required")
	}
	if err := rejectReservedRequestID(req.OperationID, req.Actor); err != nil {
		return WaitReceipt{}, err
	}
	if req.Waiter == "" {
		return WaitReceipt{}, errors.New("agentcomm: waiter is required")
	}
	req.invocation = MeshInvocation(ctx)
	if len(req.MessageIDs) > 0 {
		if req.Condition.Kind != agentcomm.WaitOnReplyTo || req.Condition.MessageID != "" {
			return WaitReceipt{}, &state.OperationRejectedError{Code: assistants.CodeInvalidRequest, Reason: "message ids go with a reply_to condition that has no message id"}
		}
		return o.waitBatch(req)
	}
	if !req.Condition.Valid() {
		return WaitReceipt{}, &state.OperationRejectedError{Code: assistants.CodeInvalidRequest, Reason: waitConditionHint(req.Condition.Kind)}
	}

	waitID := deriveMeshWaitID(req.OperationID)
	freshOnly, recipientOnly, err := o.waitRules(req)
	if err != nil {
		return WaitReceipt{}, err
	}
	eff, err := marshalEffect("mesh_wait_registered", state.Target{WaitID: string(waitID)}, state.MeshWaitRegisteredPayload{
		Waiter: string(req.Waiter), Condition: req.Condition, Deadline: req.Deadline, ContinuationNote: req.ContinuationNote,
		FreshResultOnly: freshOnly, RecipientReplyOnly: recipientOnly,
	})
	if err != nil {
		return WaitReceipt{}, err
	}
	stages := o.reg().Stages()
	stored, err := o.registerWaits(req, []state.Effect{eff}, func(rs *state.RunState) error {
		id, found := equivalentPendingWait(*rs, req.Waiter, req.Condition, freshOnly, recipientOnly)
		admitted := req
		if found {
			admitted.Deadline = rs.Assistants.Mesh.Waits[id].Deadline // the first wait's deadline stands for the repeat
		}
		if err := validateWaitAdmission(*rs, admitted, waitGraphStages(stages, *rs)); err != nil {
			return err
		}
		if found {
			return &alreadyPendingWait{id: id}
		}
		return nil
	})
	var duplicate *alreadyPendingWait
	if errors.As(err, &duplicate) {
		return WaitReceipt{WaitID: duplicate.id, Status: agentcomm.WaitPending, AlreadyPending: true}, nil
	}
	if err != nil {
		return WaitReceipt{}, err
	}
	return WaitReceipt{WaitID: waitID, Status: stored[0].Status}, nil
}

// alreadyPendingWait ends a registration under the commit's lock without writing: an equivalent wait is still
// pending. A retry of this operation id after that wait ended registers a new one — the accepted cost of no new event.
type alreadyPendingWait struct{ id agentcomm.WaitID }

func (e *alreadyPendingWait) Error() string {
	return fmt.Sprintf("agentcomm: an equivalent wait %s is still pending", e.id)
}

// equivalentPendingWait finds the pending single wait of waiter for the same thing under the same rules. A batch
// item never matches, and deadline and note stay the first registration's.
func equivalentPendingWait(rs state.RunState, waiter agentcomm.ParticipantID, cond agentcomm.WaitCondition, freshOnly, recipientOnly bool) (agentcomm.WaitID, bool) {
	for _, id := range rs.Assistants.Mesh.WaitOrder {
		w := rs.Assistants.Mesh.Waits[id]
		if w.Status == agentcomm.WaitPending && w.BatchID == "" && w.Waiter == waiter && sameWaitCondition(w.Condition, cond) &&
			w.FreshResultOnly == freshOnly && w.RecipientReplyOnly == recipientOnly {
			return id, true
		}
	}
	return "", false
}

// sameWaitCondition compares only the fields of the condition's own kind: Valid lets a caller pass others along.
func sameWaitCondition(a, b agentcomm.WaitCondition) bool {
	if a.Kind != b.Kind {
		return false
	}
	switch a.Kind {
	case agentcomm.WaitOnReplyTo:
		return a.MessageID == b.MessageID
	case agentcomm.WaitOnAddressedMessage:
		return a.ThreadID == b.ThreadID
	case agentcomm.WaitOnTaskResult:
		return a.TaskID == b.TaskID
	case agentcomm.WaitOnBoardKey:
		return a.Key == b.Key && a.Status == b.Status
	case agentcomm.WaitOnStageStatus:
		return a.StageID == b.StageID && a.Status == b.Status
	default:
		return false
	}
}

// waitBatch registers one reply_to wait per request of req.MessageIDs in ONE operation, all under one batch id:
// the whole set is admitted or refused together, and a retry of the same operation id replays it.
func (o *Orchestrator) waitBatch(req WaitRequest) (WaitReceipt, error) {
	if err := validateBatchShape(req.MessageIDs); err != nil {
		return WaitReceipt{}, err
	}
	batchID := deriveMeshBatchID(req.OperationID)
	effects := make([]state.Effect, len(req.MessageIDs))
	for i, id := range req.MessageIDs {
		eff, err := marshalEffect("mesh_wait_registered", state.Target{WaitID: string(deriveMeshWaitID(fmt.Sprintf("%s#%d", req.OperationID, i)))}, state.MeshWaitRegisteredPayload{
			Waiter: string(req.Waiter), Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: id},
			Deadline: req.Deadline, ContinuationNote: req.ContinuationNote, RecipientReplyOnly: true, BatchID: batchID,
		})
		if err != nil {
			return WaitReceipt{}, err
		}
		effects[i] = eff
	}
	stages := o.reg().Stages()
	stored, err := o.registerWaits(req, effects, func(rs *state.RunState) error {
		return validateBatchAdmission(*rs, req, waitGraphStages(stages, *rs))
	})
	if err != nil {
		return WaitReceipt{}, err
	}
	items := make([]WaitItemReceipt, len(effects))
	for i, eff := range effects {
		items[i] = WaitItemReceipt{MessageID: req.MessageIDs[i], WaitID: agentcomm.WaitID(eff.Target.WaitID), Status: stored[i].Status, Reason: stored[i].Reason}
	}
	return WaitReceipt{BatchID: batchID, Status: batchStatus(items), Items: items}, nil
}

// batchStatus is the status of a batch as a whole: pending while an item is, else expired if the deadline ended
// any item (even when others were answered, so a timeout is never taken for a complete success), else ready —
// every item finished before the deadline, whether answered, cancelled, or a mix of both. The item's own status
// and reason still tell cancelled from expired.
func batchStatus(items []WaitItemReceipt) agentcomm.WaitStatus {
	for _, item := range items {
		if item.Status == agentcomm.WaitPending {
			return agentcomm.WaitPending
		}
	}
	for _, item := range items {
		if item.Status == agentcomm.WaitExpired {
			return agentcomm.WaitExpired
		}
	}
	return agentcomm.WaitReady
}

// validateBatchShape rejects a request list a batch cannot be built from, before anything is written.
func validateBatchShape(ids []agentcomm.MessageID) error {
	reject := func(format string, args ...any) error {
		return &state.OperationRejectedError{Code: assistants.CodeInvalidRequest, Reason: fmt.Sprintf(format, args...)}
	}
	if len(ids) > MaxBatchWaits {
		return reject("a batch wait takes at most %d message ids, got %d", MaxBatchWaits, len(ids))
	}
	seen := make(map[agentcomm.MessageID]bool, len(ids))
	for _, id := range ids {
		if id == "" {
			return reject("a batch wait got an empty message id")
		}
		if seen[id] {
			return reject("message %s is listed twice in a batch wait", id)
		}
		seen[id] = true
	}
	return nil
}

// registerWaits commits the waits of effects (one mesh_wait_registered each) in one operation, decided under the
// commit's lock by check, then announces them and starts the deadline timer of each one still pending. It
// returns every wait as it is stored now (its status and reason), in effect order.
func (o *Orchestrator) registerWaits(req WaitRequest, effects []state.Effect, check func(*state.RunState) error) ([]state.MeshWait, error) {
	op := state.Operation{ID: req.OperationID, Kind: opKindMeshWait, Actor: req.Actor, Effects: effects, Expect: state.Preconditions{
		// Decided under the commit's lock: two waits registered at the same time must not both pass the cycle
		// check against a snapshot that has neither of them, and a Revise cannot slip between the attempt check
		// and the registration.
		Check: func(rs *state.RunState) error {
			if err := o.checkInvocation(*rs, req); err != nil {
				return err
			}
			return check(rs)
		},
	}}
	if _, err := o.opts.Store.ApplyRunOperation(op, func(state.OperationResult) {
		for _, eff := range effects {
			o.publishNotice(bus.Event{Type: bus.EventMeshWait, Data: map[string]any{"wait_id": eff.Target.WaitID, "waiter_id": string(req.Waiter)}})
		}
	}); err != nil {
		return nil, err
	}

	waits := o.opts.Store.Snapshot().Assistants.Mesh.Waits
	stored := make([]state.MeshWait, len(effects))
	for i, eff := range effects {
		waitID := agentcomm.WaitID(eff.Target.WaitID)
		stored[i].Status = agentcomm.WaitPending
		if w, ok := waits[waitID]; ok {
			stored[i] = *w
		}
		// T5.6: a still-PENDING wait with a deadline gets a live in-memory timer, riding o.clk() like every other
		// backoff/retry wait in this package (hooks.go, retry.go, verify.go) — afm has no durable scheduled-timer
		// facility (dev-plan.md's own Review decision), so a crash/restart before this fires is recovered purely
		// from the durable Deadline by recoverMeshWaitDeadlines at startup, never from this goroutine surviving.
		// Already-Ready (condition satisfied at registration) needs no timer: there is nothing left to time out.
		if stored[i].Status == agentcomm.WaitPending && req.Deadline != nil {
			o.armMeshWaitDeadline(waitID, *req.Deadline)
		}
	}
	return stored, nil
}

// waitGraphStages is the stage list as of rs: the registry read before the commit's lock plus every stage the journal
// added since (its registry install may still be pending), so the cycle check sees each depends_on edge rs has.
func waitGraphStages(read []flow.Stage, rs state.RunState) []flow.Stage {
	known := make(map[string]bool, len(read))
	for _, s := range read {
		known[s.ID] = true
	}
	var added []state.DynamicStageAdded
	for _, d := range rs.DynamicStages {
		if !known[d.Stage.ID] {
			added = append(added, d)
		}
	}
	if len(added) == 0 {
		return read
	}
	return graph.Materialize(read, added, rs.StageOrder)
}

// validateBatchAdmission admits a batch only if every one of its requests could be waited on alone by the
// waiter, and it sent them all: one refused request refuses the set, and the error names it. A batch is blocked
// until ALL its items end, so one item no one can answer — or one that closes a cycle — leaves the whole batch
// unable to finish. Run under the commit's lock.
func validateBatchAdmission(rs state.RunState, req WaitRequest, stages []flow.Stage) error {
	for _, id := range req.MessageIDs {
		if m, ok := rs.Assistants.Mesh.Messages[id]; ok && m.Sender != req.Waiter {
			return &state.OperationRejectedError{
				Code: assistants.CodeActorNotAllowed, Reason: fmt.Sprintf("request %s was not sent by %s — a batch waits only for the waiter's own requests", id, req.Waiter),
			}
		}
		item := req
		item.Condition = agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: id}
		if err := validateWaitAdmission(rs, item, stages); err != nil {
			var rej *state.OperationRejectedError
			if !errors.As(err, &rej) {
				return err
			}
			named := *rej
			named.Reason = fmt.Sprintf("request %s: %s", id, rej.Reason)
			return &named
		}
	}
	return nil
}

// validateWaitAdmission rejects a wait that could never end on its own: one on a message, task or stage that
// does not exist, on a closed thread, on a stage already finished in another status, a speculative wait (a key
// nobody has published, a thread not opened yet) without a deadline, a wait only the waiter itself could
// satisfy, a wait for an answer nobody left can send (canStillAnswer), or a wait that would close a cycle — every
// participant that could satisfy it already (transitively) waits on the waiter. A condition that already holds is always admitted: it is ready at once.
// stages is the run's runtime stage list: a stage blocked by an unfinished depends_on is waiting too. Run under the commit's lock.
func validateWaitAdmission(rs state.RunState, req WaitRequest, stages []flow.Stage) error {
	if err := validateWaitTarget(rs, req); err != nil {
		return err
	}
	if waitConditionHolds(rs, req.Condition) {
		return nil
	}
	all := meshWaitTargets(rs, req.Condition)
	targets := slices.DeleteFunc(slices.Clone(all), func(p agentcomm.ParticipantID) bool { return p == req.Waiter })
	if len(all) > 0 && len(targets) == 0 {
		return &state.OperationRejectedError{
			Code: agentcomm.CodeWaitCycle, Reason: fmt.Sprintf("only %s itself could satisfy this wait — it would wait on itself forever", req.Waiter),
		}
	}
	if len(targets) == 0 {
		return nil
	}
	if reason, none := nobodyLeft(rs, req.Condition, targets); none {
		return &state.OperationRejectedError{Code: assistants.CodeInvalidStatus, Reason: reason}
	}
	waitGraph := buildMeshWaitGraph(rs, &agentcomm.Wait{Waiter: req.Waiter, Condition: req.Condition}, stages)
	edge := waitEdge(rs, req.Condition, targets, stages)
	if !waitGraph.edgeDependsOn(edge, req.Waiter, 0, map[agentcomm.ParticipantID]bool{}, map[agentcomm.ParticipantID]bool{}) {
		return nil // what it waits for can still happen on its own
	}
	return &state.OperationRejectedError{
		Code:   agentcomm.CodeWaitCycle,
		Reason: fmt.Sprintf("everyone who could satisfy this wait (%v) already (transitively) waits on %q, or the chain is longer than %d hops", edge.targets, req.Waiter, agentcomm.MaxCausalHops),
	}
}

// canStillAnswer reports whether participant p can still send the message a reply_to or addressed_message wait
// cond is waiting for: p has not finished, and for reply_to its copy of the message was not given up as
// undeliverable (agentcomm_unread.go) nor read and left unanswered (leftUnanswered). Other conditions are not
// decided here. An unknown participant is not ruled out.
func canStillAnswer(rs state.RunState, cond agentcomm.WaitCondition, p agentcomm.ParticipantID) bool {
	switch cond.Kind {
	case agentcomm.WaitOnReplyTo:
		if rs.Assistants.Mesh.Receipts[cond.MessageID][p] == agentcomm.DeliveryUndeliverable || leftUnanswered(rs, cond.MessageID, p) {
			return false
		}
	case agentcomm.WaitOnAddressedMessage:
	default:
		return true
	}
	participant, known := resolveParticipant(rs, p)
	return !known || participant.Reachable
}

// validateWaitTarget rejects a wait whose target does not exist or can no longer produce what it waits for.
func validateWaitTarget(rs state.RunState, req WaitRequest) error {
	cond := req.Condition
	notFound := func(format string, args ...any) error {
		return &state.OperationRejectedError{Code: assistants.CodeNotFound, Reason: fmt.Sprintf(format, args...)}
	}
	closedThread := func(id agentcomm.ThreadID) error {
		if th, ok := rs.Assistants.Mesh.Threads[id]; ok && th.Status == agentcomm.ThreadClosed {
			return &state.OperationRejectedError{Code: assistants.CodeInvalidStatus, Reason: fmt.Sprintf("thread %s is closed — nothing more will arrive in it", id)}
		}
		return nil
	}
	switch cond.Kind {
	case agentcomm.WaitOnReplyTo:
		m, ok := rs.Assistants.Mesh.Messages[cond.MessageID]
		if !ok {
			return notFound("unknown message %s", cond.MessageID)
		}
		if rs.Assistants.Mesh.MessageAnsweredByRecipient(cond.MessageID) {
			return nil
		}
		return closedThread(m.ThreadID)
	case agentcomm.WaitOnAddressedMessage:
		if cond.ThreadID == "" {
			return nil
		}
		// A thread not opened yet may still be opened (thread ids are derived from request ids), but nobody is
		// known to do it — such a wait needs a deadline. A closed thread never gets another message.
		if _, opened := rs.Assistants.Mesh.Threads[cond.ThreadID]; !opened {
			return requireDeadline(req, fmt.Sprintf("thread %s is not open yet", cond.ThreadID))
		}
		return closedThread(cond.ThreadID)
	case agentcomm.WaitOnTaskResult:
		if _, ok := rs.Assistants.Tasks[assistants.TaskID(cond.TaskID)]; !ok {
			return notFound("unknown task %s", cond.TaskID)
		}
	case agentcomm.WaitOnStageStatus:
		st, ok := rs.Stages[cond.StageID]
		if !ok {
			return notFound("unknown stage %s", cond.StageID)
		}
		if (st.Status == state.StatusDone || st.Status == state.StatusFailed) && string(st.Status) != cond.Status {
			return &state.OperationRejectedError{
				Code: assistants.CodeInvalidStatus, Reason: fmt.Sprintf("stage %s already finished as %s — it will not become %s", cond.StageID, st.Status, cond.Status),
			}
		}
	case agentcomm.WaitOnBoardKey:
		if _, published := rs.Assistants.Mesh.Board[cond.Key]; !published {
			return requireDeadline(req, fmt.Sprintf("board key %s was never published", cond.Key))
		}
	default:
	}
	return nil
}

// waitConditionHolds reports whether cond is already satisfied for kinds whose answer does not depend on the
// waiter: such a wait is ready at once, whoever its producer is.
func waitConditionHolds(rs state.RunState, cond agentcomm.WaitCondition) bool {
	switch cond.Kind {
	case agentcomm.WaitOnReplyTo:
		return rs.Assistants.Mesh.MessageAnsweredByRecipient(cond.MessageID)
	case agentcomm.WaitOnStageStatus:
		st, ok := rs.Stages[cond.StageID]
		return ok && string(st.Status) == cond.Status
	case agentcomm.WaitOnBoardKey:
		e, ok := rs.Assistants.Mesh.Board[cond.Key]
		return ok && e.Status == cond.Status
	case agentcomm.WaitOnTaskResult:
		t, ok := rs.Assistants.Tasks[assistants.TaskID(cond.TaskID)]
		return ok && t.Status.Terminal()
	default:
		return false
	}
}

// CancelWait implements Service.CancelWait. Idempotent: cancelling an already-resolved wait is a no-op,
// never an error (applyMeshWaitResolved). Only the wait's own waiter or the human (the run owner) may
// cancel it — otherwise one participant could permanently livelock another's awaiting_peer resume by
// cancelling a WaitID it merely learned about (it is never exposed to callers other than the waiter today,
// but ownership is still enforced at the single admission point, not by obscurity).
// A list of WaitIDs (the run owner's recovery) is validated as a whole under the commit's lock and ends in one
// operation: at most one wake, whatever the number of waits.
//
// T5.6: once the cancellation durably sticks (resolveMeshWaitDurable reports THIS call's resolution — not a
// reply that raced in and won instead — is what the wait's status actually holds now), the waiter is woken
// via resumeWaiterAfterTerminalWait. Previously CancelWait never did this at all (see dev-plan.md's own
// "Review decisions" note), stranding a stage parked in awaiting_peer until the next restart — fixed here,
// decided alongside the rest of T5.6's terminal-but-unsatisfied outcomes rather than half-scoped earlier.
func (o *Orchestrator) CancelWait(ctx context.Context, req CancelWaitRequest) error {
	if req.OperationID == "" {
		return errors.New("agentcomm: operation id is required")
	}
	if err := rejectReservedRequestID(req.OperationID, req.Actor); err != nil {
		return err
	}
	waitIDs := []agentcomm.WaitID{req.WaitID}
	var check func(*state.RunState) error
	switch {
	case len(req.WaitIDs) > 0:
		if req.WaitID != "" || req.BatchID != "" {
			return &state.OperationRejectedError{Code: assistants.CodeInvalidRequest, Reason: "cancel one wait, a batch or a list of waits, not several"}
		}
		if err := validateWaitList(req.WaitIDs); err != nil {
			return err
		}
		waitIDs, check = req.WaitIDs, func(rs *state.RunState) error { return checkWaitListOwnership(*rs, req) }
	case req.BatchID != "":
		if req.WaitID != "" {
			return &state.OperationRejectedError{Code: assistants.CodeInvalidRequest, Reason: "cancel a wait or a batch, not both"}
		}
		waitIDs = batchWaitIDs(o.opts.Store.Snapshot().Assistants.Mesh, req.BatchID)
		if len(waitIDs) == 0 {
			return &state.OperationRejectedError{Code: assistants.CodeNotFound, Reason: fmt.Sprintf("unknown batch %s", req.BatchID)}
		}
	default:
	}
	if check == nil && req.Actor.Kind != actorKindUser {
		rs := o.opts.Store.Snapshot()
		for _, id := range waitIDs {
			if w, ok := rs.Assistants.Mesh.Waits[id]; ok && string(w.Waiter) != req.Actor.ID {
				return &state.OperationRejectedError{Code: assistants.CodeActorNotAllowed, Reason: "only the wait's own waiter or the human may cancel it"}
			}
		}
	}
	reason := req.Reason
	if reason == "" {
		reason = "cancelled"
	}
	waiter, changed, err := o.resolveMeshWaitsChecked(req.OperationID, waitIDs, req.Actor, agentcomm.WaitCancelled, reason, check)
	if err != nil {
		return err
	}
	if changed {
		o.resumeWaiterAfterTerminalWait(o.runContext(ctx), waiter) // must not die with the HTTP request
	}
	return nil
}

// ownerCancelOperationPrefix keeps the run owner's cancellations apart from every other operation id of the run.
const ownerCancelOperationPrefix = "owner-cancel-waits:"

// OwnerCancelOperationID is the idempotency key of the owner's cancellation requestID for stageID. The stage is
// part of it: a repeat of a request against another stage is a new operation that passes its own ownership check
// instead of replaying the first one's result. Stage ids have no ':', so the pair is unambiguous.
func OwnerCancelOperationID(stageID, requestID string) string {
	return ownerCancelOperationPrefix + stageID + ":" + requestID
}

// validateWaitList rejects a list of wait ids that names an empty id or one twice, before anything is committed.
func validateWaitList(ids []agentcomm.WaitID) error {
	seen := make(map[agentcomm.WaitID]bool, len(ids))
	for _, id := range ids {
		if id == "" || seen[id] {
			return &state.OperationRejectedError{Code: assistants.CodeInvalidRequest, Reason: "a list of waits takes distinct, non-empty wait ids"}
		}
		seen[id] = true
	}
	return nil
}

// checkWaitListOwnership runs under the commit's lock: every wait exists and belongs to one waiter (StageID's
// agent when named) that req.Actor may cancel for, or nothing is committed.
func checkWaitListOwnership(rs state.RunState, req CancelWaitRequest) error {
	var waiter agentcomm.ParticipantID
	for i, id := range req.WaitIDs {
		w := rs.Assistants.Mesh.Waits[id]
		if w == nil || (req.StageID != "" && w.Waiter != meshStageParticipantID(req.StageID)) {
			return &state.OperationRejectedError{Code: assistants.CodeNotFound, Reason: fmt.Sprintf("unknown wait %s", id)}
		}
		if i > 0 && w.Waiter != waiter {
			return &state.OperationRejectedError{Code: assistants.CodeInvalidRequest, Reason: "the waits of one cancellation must belong to one waiter"}
		}
		waiter = w.Waiter
	}
	if req.Actor.Kind != actorKindUser && string(waiter) != req.Actor.ID {
		return &state.OperationRejectedError{Code: assistants.CodeActorNotAllowed, Reason: "only the wait's own waiter or the human may cancel it"}
	}
	return nil
}

// StageWaitView is one peer wait of a stage as the run owner and the completion-marker notice show it.
type StageWaitView struct {
	WaitID    agentcomm.WaitID        `json:"wait_id"`
	BatchID   string                  `json:"batch_id,omitempty"`
	Condition agentcomm.WaitCondition `json:"condition"`
	Status    agentcomm.WaitStatus    `json:"status"`
	Reason    string                  `json:"reason,omitempty"`
	Deadline  *time.Time              `json:"deadline,omitempty"`
}

// stageWaitViews shows the waits of ids that exist, in the order given.
func stageWaitViews(mesh state.MeshState, ids []agentcomm.WaitID) []StageWaitView {
	views := make([]StageWaitView, 0, len(ids))
	for _, id := range ids {
		if w := mesh.Waits[id]; w != nil {
			views = append(views, StageWaitView{WaitID: id, BatchID: w.BatchID, Condition: w.Condition, Status: w.Status, Reason: w.Reason, Deadline: w.Deadline})
		}
	}
	return views
}

// StageWaits lists the peer waits of stageID's agent in registration order, the ended ones with their reason. It
// only reads: the run owner uses it to choose what to cancel.
func (o *Orchestrator) StageWaits(stageID string) ([]StageWaitView, error) {
	rs := o.opts.Store.Snapshot()
	if _, ok := rs.Stages[stageID]; !ok {
		return nil, &state.OperationRejectedError{Code: assistants.CodeNotFound, Reason: fmt.Sprintf("unknown stage %s", stageID)}
	}
	waiter := meshStageParticipantID(stageID)
	var ids []agentcomm.WaitID
	for _, id := range rs.Assistants.Mesh.WaitOrder {
		if w := rs.Assistants.Mesh.Waits[id]; w != nil && w.Waiter == waiter {
			ids = append(ids, id)
		}
	}
	return stageWaitViews(rs.Assistants.Mesh, ids), nil
}

// batchWaitIDs lists the waits of batch batchID in registration order.
func batchWaitIDs(mesh state.MeshState, batchID string) []agentcomm.WaitID {
	var ids []agentcomm.WaitID
	for _, id := range mesh.WaitOrder {
		if w := mesh.Waits[id]; w != nil && w.BatchID == batchID {
			ids = append(ids, id)
		}
	}
	return ids
}

// resolveMeshWaitDurable is the SINGLE commit path for every non-Ready wait resolution this package produces
// (CancelWait above, a firing deadline timer, startup deadline recovery, the run-shutdown sweep, and the
// release of a wait nobody can answer) — one mesh_wait_resolved effect through the same ApplyRunOperation/notice
// shape CancelWait always used, never a second resolution code path. opID must be a stable, deterministic key
// per resolution ATTEMPT (not merely per wait): ApplyRunOperation's own idempotency means a repeat of the SAME
// opID with the SAME status/reason is a safe no-op replay, which is exactly what lets the live timer and the
// startup-recovery sweep share one key (mesh-wait-expire:<id>) without double-committing if both ever somehow
// ran for the same wait.
//
// changed reports whether THIS commit moved the wait out of pending (state.OperationResult.EndedWaits, decided
// under the store lock). A reply or another resolution that landed first leaves the wait as it was, and then
// this call neither announces it nor may its caller wake the waiter: the outcome that won owns both. waiter is
// the wait's waiter either way.
func (o *Orchestrator) resolveMeshWaitDurable(opID string, waitID agentcomm.WaitID, actor state.Actor, status agentcomm.WaitStatus, reason string) (agentcomm.ParticipantID, bool, error) {
	return o.resolveMeshWaitChecked(opID, waitID, actor, status, reason, nil)
}

// resolveMeshWaitChecked is resolveMeshWaitDurable with a commit-time check (nil: none).
func (o *Orchestrator) resolveMeshWaitChecked(opID string, waitID agentcomm.WaitID, actor state.Actor, status agentcomm.WaitStatus, reason string, check func(*state.RunState) error) (agentcomm.ParticipantID, bool, error) {
	return o.resolveMeshWaitsChecked(opID, []agentcomm.WaitID{waitID}, actor, status, reason, check)
}

// resolveMeshWaitsChecked resolves several waits of one waiter (a batch) in ONE operation — all of them or, on a
// rejected check, none. changed reports whether this commit moved any of them out of pending; waiters woken for
// it decide through peerContinuation whether the whole batch has ended.
func (o *Orchestrator) resolveMeshWaitsChecked(opID string, waitIDs []agentcomm.WaitID, actor state.Actor, status agentcomm.WaitStatus, reason string, check func(*state.RunState) error) (agentcomm.ParticipantID, bool, error) {
	effects, err := meshWaitResolvedEffects(waitIDs, status, reason)
	if err != nil {
		return "", false, err
	}
	op := state.Operation{ID: opID, Kind: "mesh_resolve_wait", Actor: actor, Effects: effects, Expect: state.Preconditions{Check: check}}
	var changed bool
	_, err = o.opts.Store.ApplyRunOperation(op, func(res state.OperationResult) {
		for _, id := range waitIDs {
			if slices.Contains(res.EndedWaits, id) {
				changed = true
				o.announceEndedWait(id, status, reason)
			}
		}
	})
	if err != nil {
		return "", false, err
	}
	var waiter agentcomm.ParticipantID
	if w := o.opts.Store.Snapshot().Assistants.Mesh.Waits[waitIDs[0]]; w != nil {
		waiter = w.Waiter
	}
	return waiter, changed, nil
}

// meshWaitResolvedEffects is one mesh_wait_resolved effect per wait, all ending in status with reason.
func meshWaitResolvedEffects(waitIDs []agentcomm.WaitID, status agentcomm.WaitStatus, reason string) ([]state.Effect, error) {
	effects := make([]state.Effect, len(waitIDs))
	for i, id := range waitIDs {
		eff, err := marshalEffect("mesh_wait_resolved", state.Target{WaitID: string(id)}, state.MeshWaitResolvedPayload{Status: string(status), Reason: reason})
		if err != nil {
			return nil, err
		}
		effects[i] = eff
	}
	return effects, nil
}

// pendingWaitsOf is the waits of waiter that are still pending, in registration order.
func pendingWaitsOf(mesh state.MeshState, waiter agentcomm.ParticipantID) []agentcomm.WaitID {
	var pending []agentcomm.WaitID
	for _, id := range mesh.WaitOrder {
		if w := mesh.Waits[id]; w.Waiter == waiter && w.Status == agentcomm.WaitPending {
			pending = append(pending, id)
		}
	}
	return pending
}

// armMeshWaitDeadline starts a background timer for waitID, riding o.clk() like hooks.go's retry backoff and
// verify.go's timeout — the established in-memory timer idiom for this codebase (dev-plan.md's own Review
// decision: afm has no durable scheduled-timer facility). It runs under o.runContext, NEVER the ctx a caller
// happened to pass to Wait (Invariant No.6 — an HTTP request's context is cancelled the instant its handler
// returns, long before most deadlines elapse), so it is cancelled at the right moment: the instant Run's own
// shutdown sequence cancels the run context, which happens BEFORE Store.Close (pkg/orchestrator/AGENTS.md).
// If the process exits (clean or crash) before this fires, nothing is lost: recoverMeshWaitDeadlines
// re-derives the identical decision from the durable Deadline on the next startup.
func (o *Orchestrator) armMeshWaitDeadline(waitID agentcomm.WaitID, deadline time.Time) {
	ctx := o.runContext(context.Background())
	d := deadline.Sub(o.clk().Now())
	if d < 0 {
		d = 0
	}
	go func() {
		select {
		case <-o.clk().After(d):
			o.expireMeshWait(ctx, waitID)
		case <-ctx.Done():
		}
	}()
}

// expireMeshWait is armMeshWaitDeadline's firing handler (the LIVE path — the process is still up). It
// re-reads the durable wait record as the source of truth: resolving to WaitExpired is attempted
// unconditionally (resolveMeshWaitDurable's own Pending-only guard makes this safe even if the wait already
// resolved another way), and the waiter is woken ONLY if resolveMeshWaitDurable reports that Expired is what actually
// stuck — never a fake ack, never a synthesised answer: if a reply won the race instead, that reply's own
// resume path (resumeWaitersAfterCommit) already owns waking the waiter, and the reply's own message/board
// record was never affected by this call at all.
func (o *Orchestrator) expireMeshWait(ctx context.Context, waitID agentcomm.WaitID) {
	waiter, changed, err := o.resolveMeshWaitDurable(meshWaitExpireOpID(waitID), waitID, state.Actor{Kind: "system"}, agentcomm.WaitExpired, meshWaitExpiredReason)
	if err != nil {
		log.Printf("WARN: mesh: expire wait %s: %v", waitID, err)
		return
	}
	if changed {
		o.resumeWaiterAfterTerminalWait(ctx, waiter)
	}
}

// announceEndedWait publishes that waitID ended without an answer (status, reason). Only the operation whose
// commit ended it (state.OperationResult.EndedWaits) calls it.
func (o *Orchestrator) announceEndedWait(waitID agentcomm.WaitID, status agentcomm.WaitStatus, reason string) {
	o.publishNotice(bus.Event{Type: bus.EventMeshWait, Data: map[string]any{"wait_id": string(waitID), "status": string(status), "reason": reason}})
}

// announceAndWakeEnded is the tail of an operation that may end waits as a side effect of its own facts (a
// thread closing, a finish dropping a reply it cannot deliver): for each wait its commit ended (EndedWaits — a
// replay ends none), it announces the end with reason(waitID) and resumes the waiter as "wait ended", like
// CancelWait does for its one wait.
func (o *Orchestrator) announceAndWakeEnded(ended []agentcomm.WaitID, reason func(agentcomm.WaitID) string) {
	rs := o.opts.Store.Snapshot()
	for _, waitID := range ended {
		w := rs.Assistants.Mesh.Waits[waitID]
		if w == nil {
			continue
		}
		o.announceEndedWait(waitID, w.Status, reason(waitID))
		o.resumeWaiterAfterTerminalWait(o.runContext(context.Background()), w.Waiter)
	}
}

// meshWaitExpireOpID is the deterministic operation id shared by expireMeshWait's live path and
// recoverMeshWaitDeadlines' startup-recovery path for the SAME wait — see resolveMeshWaitDurable's own doc
// comment for why sharing one key here is deliberate, not an oversight.
func meshWaitExpireOpID(waitID agentcomm.WaitID) string {
	return "mesh-wait-expire:" + string(waitID)
}

// recoverMeshWaitDeadlines re-derives, on startup, every live deadline timer that a PREVIOUS process (crashed
// or cleanly exited) was holding in memory — gone the moment that process stopped, since afm has no durable
// scheduled-timer facility. The durable Deadline on each still-PENDING wait is the only source of truth:
// overdue, it is resolved to WaitExpired right here, synchronously, with DIRECT dispatch to the waiter's
// resume path (resumeWaiterAfterTerminalWaitAtStartup) — never via publishCritical, for the exact reason
// recoverMeshWaits' own doc comment gives (this runs before Run's event loop starts draining the critical
// bus); still in the future, a fresh timer is armed for the REMAINING duration, identical to the one Wait()
// itself would have started. Called once from Run(), after recoverMeshWaits/recoverAssistants so this only
// ever acts on a wait still genuinely pending after their own Ready-resolution recovery passes.
func (o *Orchestrator) recoverMeshWaitDeadlines(ctx context.Context) {
	rs := o.opts.Store.Snapshot()
	now := o.clk().Now()
	for _, id := range sortedWaitIDs(rs.Assistants.Mesh.Waits) {
		w := rs.Assistants.Mesh.Waits[id]
		if w.Status != agentcomm.WaitPending || w.Deadline == nil {
			continue
		}
		if now.Before(*w.Deadline) {
			o.armMeshWaitDeadline(id, *w.Deadline)
			continue
		}
		waiter, changed, err := o.resolveMeshWaitDurable(meshWaitExpireOpID(id), id, state.Actor{Kind: "system"}, agentcomm.WaitExpired, meshWaitExpiredReason)
		if err != nil {
			log.Printf("WARN: mesh: recover overdue wait %s: %v", id, err)
			continue
		}
		if changed {
			o.resumeWaiterAfterTerminalWaitAtStartup(ctx, waiter)
		}
	}
}

// closeMeshWaitsIfFinalized closes the remaining waits only when the run has really finished (FinalizeIf
// decided it). An interrupted run (Ctrl+C, a fatal error) is resumed later: its waits stay pending, and their
// deadlines are re-armed by recoverMeshWaitDeadlines on the next start.
func (o *Orchestrator) closeMeshWaitsIfFinalized() {
	if o.finalizing.Load() {
		o.closeRemainingMeshWaits()
	}
}

// closeRemainingMeshWaits resolves every still-WaitPending mesh wait to WaitRunClosed as Run winds down
// (T5.6): a wait nobody will ever revisit again (the whole run is ending) must not dangle in the journal
// forever with no visible terminal outcome. Called from Run's own defer chain AFTER WaitAgents has drained
// every live agent goroutine (so this never races a live Send/Publish/CancelWait still resolving the SAME
// wait — resolveMeshWaitDurable's Pending-only guard would make it harmless even if it did) and BEFORE EndRun
// persists the run's terminal status, so these closures are part of the SAME run's final, inspectable history.
// Deliberately does NOT resume any waiter: the run is exiting regardless, there is nothing left to resume
// into, and every write here goes through the bounded, synchronous ApplyRunOperation path — no new goroutine,
// nothing that could make shutdown unbounded.
func (o *Orchestrator) closeRemainingMeshWaits() {
	rs := o.opts.Store.Snapshot()
	for _, id := range sortedWaitIDs(rs.Assistants.Mesh.Waits) {
		if rs.Assistants.Mesh.Waits[id].Status != agentcomm.WaitPending {
			continue
		}
		if _, _, err := o.resolveMeshWaitDurable("mesh-wait-run-closed:"+string(id), id, state.Actor{Kind: "system"}, agentcomm.WaitRunClosed, meshWaitRunClosedReason); err != nil {
			log.Printf("WARN: mesh: close remaining wait %s at shutdown: %v", id, err)
		}
	}
}

// meshWaitEdge is one way a participant can be unblocked: by ANY of targets (a mesh wait — any recipient of a
// message, any other member of a thread may answer), or only by ALL of them (a task waiting for its children).
// A batch of waits is one edge with every item in allOf: it unblocks only when all of them have.
type meshWaitEdge struct {
	targets  []agentcomm.ParticipantID
	needsAll bool
	allOf    []meshWaitEdge
}

// meshWaitGraph maps a blocked participant to the edges that can unblock it; any one edge resolving wakes it.
type meshWaitGraph map[agentcomm.ParticipantID][]meshWaitEdge

// buildMeshWaitGraph returns every "X cannot proceed until Y acts" relationship currently known to the
// projection: live (WaitPending) mesh waits, assistants' own waiting_children links, and the unfinished
// depends_on of the stages that have not started or that a wait needs done (waitEdge). depends_on alone is
// cycle-free by flow validation, but it closes a cycle together with a mesh wait of a dependency for its dependent stage.
func buildMeshWaitGraph(rs state.RunState, candidate *agentcomm.Wait, stages []flow.Stage) meshWaitGraph {
	g := meshWaitGraph{}
	batchItems := map[string][]meshWaitEdge{} // the pending items of each batch
	batchWaiter := map[string]agentcomm.ParticipantID{}
	for _, w := range rs.Assistants.Mesh.Waits {
		if w.Status != agentcomm.WaitPending {
			continue
		}
		targets := slices.DeleteFunc(meshWaitTargets(rs, w.Condition), func(p agentcomm.ParticipantID) bool { return p == w.Waiter })
		if len(targets) == 0 || stageWaitsOnlyForCallbackHelpers(rs, w.Waiter, targets, candidate) {
			continue
		}
		edge := waitEdge(rs, w.Condition, targets, stages)
		if w.BatchID == "" {
			g[w.Waiter] = append(g[w.Waiter], edge)
		} else {
			batchItems[w.BatchID] = append(batchItems[w.BatchID], edge)
			batchWaiter[w.BatchID] = w.Waiter
		}
	}
	for batchID, items := range batchItems {
		g[batchWaiter[batchID]] = append(g[batchWaiter[batchID]], meshWaitEdge{allOf: items})
	}
	for id, t := range rs.Assistants.Tasks {
		if t.Status != assistants.StatusWaitingChildren {
			continue
		}
		var children []agentcomm.ParticipantID
		for _, cid := range currentChildren(rs, id, t.TaskGeneration) {
			if c, ok := rs.Assistants.Tasks[cid]; ok && !c.Status.Terminal() {
				children = append(children, meshParticipantForTask(rs, c.Task))
			}
		}
		if len(children) > 0 {
			parent := meshParticipantForTask(rs, t.Task)
			g[parent] = append(g[parent], meshWaitEdge{targets: children, needsAll: true})
		}
	}
	for _, s := range stages {
		if edge, blocked := unfinishedDependencies(rs, s); blocked {
			g[meshStageParticipantID(s.ID)] = append(g[meshStageParticipantID(s.ID)], edge)
		}
	}
	return g
}

// unfinishedDependencies is the edge of a stage that has not started and still waits for dependencies to be done.
func unfinishedDependencies(rs state.RunState, s flow.Stage) (meshWaitEdge, bool) {
	if rs.Stages[s.ID].Status != state.StatusPending {
		return meshWaitEdge{}, false
	}
	deps := dependenciesNotDone(rs, s)
	return meshWaitEdge{targets: deps, needsAll: true}, len(deps) > 0
}

func dependenciesNotDone(rs state.RunState, s flow.Stage) []agentcomm.ParticipantID {
	var deps []agentcomm.ParticipantID
	for _, id := range s.DependsOn {
		if rs.Stages[id].Status != state.StatusDone {
			deps = append(deps, meshStageParticipantID(id))
		}
	}
	return deps
}

// waitEdge is the edge a wait for cond gives its waiter, targets being who can satisfy it. A stage is done only after
// its depends_on are, whatever it does before them (an eager-planning one plans and answers messages): a wait for that
// needs them too. An eager planning-only stage is the exception: the approval of its plan finishes it (approveStage).
func waitEdge(rs state.RunState, cond agentcomm.WaitCondition, targets []agentcomm.ParticipantID, stages []flow.Stage) meshWaitEdge {
	if cond.Kind != agentcomm.WaitOnStageStatus || cond.Status != string(state.StatusDone) {
		return meshWaitEdge{targets: targets}
	}
	i := slices.IndexFunc(stages, func(s flow.Stage) bool { return s.ID == cond.StageID })
	if i < 0 || stages[i].EagerPlanning && stages[i].NeedsPlanning() && !stages[i].HasAgent(flow.AgentImplementation) {
		return meshWaitEdge{targets: targets}
	}
	deps := dependenciesNotDone(rs, stages[i])
	if len(deps) == 0 {
		return meshWaitEdge{targets: targets}
	}
	return meshWaitEdge{targets: append(slices.Clone(targets), deps...), needsAll: true}
}

// stageWaitsOnlyForCallbackHelpers: a stage waiting only on its own helpers is woken by their callbacks, so a
// helper waiting for the answer to one is no deadlock. Any other wait of the helper (the candidate included)
// needs more than a callback turn, so the edge stays.
func stageWaitsOnlyForCallbackHelpers(rs state.RunState, waiter agentcomm.ParticipantID, targets []agentcomm.ParticipantID, candidate *agentcomm.Wait) bool {
	stageID, isStage := stageIDFromMeshParticipant(waiter)
	if !isStage {
		return false
	}
	for _, p := range targets {
		if !isOwnOneShotHelper(rs, stageID, p) || !waitsOnlyForAnswersTo(rs, p, waiter, candidate) {
			return false
		}
	}
	return true
}

// isOwnOneShotHelper reports whether p is a helper of stage stageID's current root generation that runs one turn
// rather than watching the stage.
func isOwnOneShotHelper(rs state.RunState, stageID string, p agentcomm.ParticipantID) bool {
	id, ok := strings.CutPrefix(string(p), meshHelperPrefix)
	if !ok {
		return false
	}
	t, found := rs.Assistants.Tasks[assistants.TaskID(id)]
	return found && isStageSpawnedHelper(rs, stageID, t) && !isWatcherTrigger(t.Trigger)
}

// waitsOnlyForAnswersTo reports whether every pending wait of helper, and candidate if it is the helper's, is a
// reply_to on a request addressed to stage. A helper waiting for its delegated children needs more than an answer.
func waitsOnlyForAnswersTo(rs state.RunState, helper, stage agentcomm.ParticipantID, candidate *agentcomm.Wait) bool {
	id, _ := strings.CutPrefix(string(helper), meshHelperPrefix)
	if t, ok := rs.Assistants.Tasks[assistants.TaskID(id)]; ok && t.Status == assistants.StatusWaitingChildren {
		return false
	}
	isAnswerToStage := func(cond agentcomm.WaitCondition) bool {
		m, ok := rs.Assistants.Mesh.Messages[cond.MessageID]
		return cond.Kind == agentcomm.WaitOnReplyTo && ok && slices.Contains(m.Recipients, stage)
	}
	for _, w := range rs.Assistants.Mesh.Waits {
		if w.Waiter == helper && w.Status == agentcomm.WaitPending && !isAnswerToStage(w.Condition) {
			return false
		}
	}
	return candidate == nil || candidate.Waiter != helper || isAnswerToStage(candidate.Condition)
}

// dependsOn reports whether node cannot act until waiter does: node is the waiter, or its edges depend on the
// waiter — one edge for a node that resumes only after all its waits clear (resumesAfterAllWaits), every edge
// for the rest. A participant with no edges is free to act. A chain longer than
// agentcomm.MaxCausalHops, or a loop that does not pass through the waiter, counts as dependent — the check
// must never admit a cycle it could not see to the end. memo keeps the walk linear in the graph size.
func (g meshWaitGraph) dependsOn(node, waiter agentcomm.ParticipantID, depth int, memo, path map[agentcomm.ParticipantID]bool) bool {
	if node == waiter {
		return true
	}
	if v, ok := memo[node]; ok {
		return v
	}
	if depth >= agentcomm.MaxCausalHops || path[node] {
		return true
	}
	path[node] = true
	defer delete(path, node)
	edgeDepends := func(e meshWaitEdge) bool { return g.edgeDependsOn(e, waiter, depth+1, memo, path) }
	var result bool
	if resumesAfterAllWaits(node) {
		result = slices.ContainsFunc(g[node], edgeDepends)
	} else {
		result = len(g[node]) > 0 && !slices.ContainsFunc(g[node], func(e meshWaitEdge) bool { return !edgeDepends(e) })
	}
	memo[node] = result
	return result
}

// resumesAfterAllWaits reports whether id is a task (helper or run agent, including a legacy "run:<profileID>")
// that continues only once ALL its pending waits are cleared (resumeTaskIfWaitCleared). A stage, the Side agent
// and the human continue on the first one that clears, so their waits stay any-of.
func resumesAfterAllWaits(id agentcomm.ParticipantID) bool {
	return strings.HasPrefix(string(id), meshHelperPrefix) || strings.HasPrefix(string(id), meshRunPrefix)
}

// edgeDependsOn: an any-of edge depends on the waiter when every target does; an all-of edge when one does; a
// batch when any of its items does.
func (g meshWaitGraph) edgeDependsOn(e meshWaitEdge, waiter agentcomm.ParticipantID, depth int, memo, path map[agentcomm.ParticipantID]bool) bool {
	if len(e.allOf) > 0 {
		return slices.ContainsFunc(e.allOf, func(item meshWaitEdge) bool { return g.edgeDependsOn(item, waiter, depth, memo, path) })
	}
	for _, t := range e.targets {
		if g.dependsOn(t, waiter, depth, memo, path) == e.needsAll {
			return e.needsAll
		}
	}
	return !e.needsAll
}

// meshWaitTargets returns the participant(s) whose action would satisfy cond — any one of them is enough. A
// board_key condition with a known current author counts (its publisher IS the producer the waiter needs to
// act); one with no publish yet has no known target and contributes no edge (its only guard is
// ErrWaitRequiresDeadline above, not the cycle check).
func meshWaitTargets(rs state.RunState, cond agentcomm.WaitCondition) []agentcomm.ParticipantID {
	switch cond.Kind {
	case agentcomm.WaitOnReplyTo:
		if m, ok := rs.Assistants.Mesh.Messages[cond.MessageID]; ok {
			return slices.Clone(m.Recipients)
		}
	case agentcomm.WaitOnAddressedMessage:
		if th, ok := rs.Assistants.Mesh.Threads[cond.ThreadID]; ok {
			return slices.Clone(th.Members)
		}
	case agentcomm.WaitOnTaskResult:
		taskID := assistants.TaskID(cond.TaskID)
		if t, ok := rs.Assistants.Tasks[taskID]; ok {
			return []agentcomm.ParticipantID{meshParticipantForTask(rs, t.Task)}
		}
		return []agentcomm.ParticipantID{meshHelperParticipantID(taskID)}
	case agentcomm.WaitOnStageStatus:
		return []agentcomm.ParticipantID{meshStageParticipantID(cond.StageID)}
	case agentcomm.WaitOnBoardKey:
		if e, ok := rs.Assistants.Mesh.Board[cond.Key]; ok {
			return []agentcomm.ParticipantID{e.AuthorID}
		}
	default:
		return nil
	}
	return nil
}
