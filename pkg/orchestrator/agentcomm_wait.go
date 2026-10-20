package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// meshWaitExpiredReason/meshWaitRunClosedReason (Agent Mesh Phase 5, T5.6) are the stored, visible Reason
// values for the two system-driven (not human/agent-requested) terminal wait outcomes this file adds.
const (
	meshWaitExpiredReason   = "deadline exceeded"
	meshWaitRunClosedReason = "the run closed while this wait was still pending"
)

// Agent Mesh Phase 2, T2.3: Wait/CancelWait and the cycle check. Registration and the "is this already
// satisfied" check happen inside the SAME ApplyRunOperation (applyMeshWaitRegistered,
// pkg/state/mesh_waits.go) — a reply that arrived before Wait was called still resolves the wait on the
// very first read after commit, with no separate catch-up pass.

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
	op := state.Operation{ID: req.OperationID, Kind: opKindMeshWait, Actor: req.Actor, Effects: []state.Effect{eff}, Expect: state.Preconditions{
		// Decided under the commit's lock: two waits registered at the same time must not both pass the cycle
		// check against a snapshot that has neither of them.
		Check: func(rs *state.RunState) error { return validateWaitAdmission(*rs, req) },
	}}
	if _, err := o.opts.Store.ApplyRunOperation(op, func(state.OperationResult) {
		o.publishNotice(bus.Event{Type: bus.EventMeshWait, Data: map[string]any{"wait_id": string(waitID), "waiter_id": string(req.Waiter)}})
	}); err != nil {
		return WaitReceipt{}, err
	}

	after := o.opts.Store.Snapshot()
	status := agentcomm.WaitPending
	if w, ok := after.Assistants.Mesh.Waits[waitID]; ok {
		status = w.Status
	}
	// T5.6: a still-PENDING wait with a deadline gets a live in-memory timer, riding o.clk() like every other
	// backoff/retry wait in this package (hooks.go, retry.go, verify.go) — afm has no durable scheduled-timer
	// facility (dev-plan.md's own Review decision), so a crash/restart before this fires is recovered purely
	// from the durable Deadline by recoverMeshWaitDeadlines at startup, never from this goroutine surviving.
	// Already-Ready (condition satisfied at registration) needs no timer: there is nothing left to time out.
	if status == agentcomm.WaitPending && req.Deadline != nil {
		o.armMeshWaitDeadline(waitID, *req.Deadline)
	}
	return WaitReceipt{WaitID: waitID, Status: status}, nil
}

// validateWaitAdmission rejects a wait that could never end on its own: one on a message, task or stage that
// does not exist, on a closed thread, on a stage already finished in another status, a speculative wait (a key
// nobody has published, a thread not opened yet) without a deadline, a wait only the waiter itself could
// satisfy, a wait for an answer nobody left can send (every recipient or thread member finished), or a wait that would close a cycle — every participant that could satisfy it already
// (transitively) waits on the waiter. A condition that already holds is always admitted: it is ready at once.
// Run under the commit's lock.
func validateWaitAdmission(rs state.RunState, req WaitRequest) error {
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
	if !slices.ContainsFunc(targets, func(p agentcomm.ParticipantID) bool { return canStillAnswer(rs, req.Condition, p) }) {
		return &state.OperationRejectedError{
			Code: assistants.CodeInvalidStatus, Reason: fmt.Sprintf("everyone who could satisfy this wait (%v) has finished — nobody will answer", targets),
		}
	}
	graph := buildMeshWaitGraph(rs)
	memo := map[agentcomm.ParticipantID]bool{}
	for _, target := range targets {
		if !graph.dependsOn(target, req.Waiter, 0, memo, map[agentcomm.ParticipantID]bool{}) {
			return nil // this one can still act on its own
		}
	}
	return &state.OperationRejectedError{
		Code:   agentcomm.CodeWaitCycle,
		Reason: fmt.Sprintf("everyone who could satisfy this wait (%v) already (transitively) waits on %q, or the chain is longer than %d hops", targets, req.Waiter, agentcomm.MaxCausalHops),
	}
}

// canStillAnswer reports whether participant p can still send the message a reply_to or addressed_message wait
// cond is waiting for: p has not finished, and for reply_to its copy of the message was not given up as
// undeliverable (agentcomm_unread.go). Other conditions are not decided here. An unknown participant is not
// ruled out.
func canStillAnswer(rs state.RunState, cond agentcomm.WaitCondition, p agentcomm.ParticipantID) bool {
	switch cond.Kind {
	case agentcomm.WaitOnReplyTo:
		if rs.Assistants.Mesh.Receipts[cond.MessageID][p] == agentcomm.DeliveryUndeliverable {
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
	if req.Actor.Kind != actorKindUser {
		rs := o.opts.Store.Snapshot()
		if w, ok := rs.Assistants.Mesh.Waits[req.WaitID]; ok && string(w.Waiter) != req.Actor.ID {
			return &state.OperationRejectedError{Code: assistants.CodeActorNotAllowed, Reason: "only the wait's own waiter or the human may cancel it"}
		}
	}
	reason := req.Reason
	if reason == "" {
		reason = "cancelled"
	}
	waiter, changed, err := o.resolveMeshWaitDurable(req.OperationID, req.WaitID, req.Actor, agentcomm.WaitCancelled, reason)
	if err != nil {
		return err
	}
	if changed {
		o.resumeWaiterAfterTerminalWait(o.runContext(ctx), waiter) // must not die with the HTTP request
	}
	return nil
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
	eff, err := marshalEffect("mesh_wait_resolved", state.Target{WaitID: string(waitID)},
		state.MeshWaitResolvedPayload{Status: string(status), Reason: reason})
	if err != nil {
		return "", false, err
	}
	op := state.Operation{ID: opID, Kind: "mesh_resolve_wait", Actor: actor, Effects: []state.Effect{eff}, Expect: state.Preconditions{Check: check}}
	var changed bool
	_, err = o.opts.Store.ApplyRunOperation(op, func(res state.OperationResult) {
		changed = slices.Contains(res.EndedWaits, waitID)
		if changed {
			o.announceEndedWait(waitID, status, reason)
		}
	})
	if err != nil {
		return "", false, err
	}
	var waiter agentcomm.ParticipantID
	if w := o.opts.Store.Snapshot().Assistants.Mesh.Waits[waitID]; w != nil {
		waiter = w.Waiter
	}
	return waiter, changed, nil
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
type meshWaitEdge struct {
	targets  []agentcomm.ParticipantID
	needsAll bool
}

// meshWaitGraph maps a blocked participant to the edges that can unblock it; any one edge resolving wakes it.
type meshWaitGraph map[agentcomm.ParticipantID][]meshWaitEdge

// buildMeshWaitGraph returns every "X cannot proceed until Y acts" relationship currently known to the
// projection: live (WaitPending) mesh waits plus assistants' own waiting_children links. This is deliberately
// NOT the full scheduling graph — stage depends_on is static and already cycle-free by flow validation, so it
// can never itself CLOSE a new cycle that a fresh mesh wait could complete; only the two dynamic relationships
// below can.
func buildMeshWaitGraph(rs state.RunState) meshWaitGraph {
	g := meshWaitGraph{}
	for _, w := range rs.Assistants.Mesh.Waits {
		if w.Status != agentcomm.WaitPending {
			continue
		}
		targets := slices.DeleteFunc(meshWaitTargets(rs, w.Condition), func(p agentcomm.ParticipantID) bool { return p == w.Waiter })
		if len(targets) > 0 {
			g[w.Waiter] = append(g[w.Waiter], meshWaitEdge{targets: targets})
		}
	}
	for id, t := range rs.Assistants.Tasks {
		if t.Status != assistants.StatusWaitingChildren {
			continue
		}
		var children []agentcomm.ParticipantID
		for _, cid := range currentChildren(rs, id, t.TaskGeneration) {
			if c, ok := rs.Assistants.Tasks[cid]; ok && !c.Status.Terminal() {
				children = append(children, meshHelperParticipantID(cid))
			}
		}
		if len(children) > 0 {
			parent := meshHelperParticipantID(id)
			g[parent] = append(g[parent], meshWaitEdge{targets: children, needsAll: true})
		}
	}
	return g
}

// dependsOn reports whether node cannot act until waiter does: node is the waiter, or every edge that could
// unblock node depends on the waiter. A participant with no edges is free to act. A chain longer than
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
	result := len(g[node]) > 0
	for _, e := range g[node] {
		if !g.edgeDependsOn(e, waiter, depth+1, memo, path) {
			result = false
			break
		}
	}
	memo[node] = result
	return result
}

// edgeDependsOn: an any-of edge depends on the waiter when every target does; an all-of edge when one does.
func (g meshWaitGraph) edgeDependsOn(e meshWaitEdge, waiter agentcomm.ParticipantID, depth int, memo, path map[agentcomm.ParticipantID]bool) bool {
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
