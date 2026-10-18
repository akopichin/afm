package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
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

// ErrWaitRequiresDeadline is returned when a board_key condition names a key the board has never seen
// published and the caller did not set a Deadline — an unreachable producer must not be allowed to wait
// forever with no visible way out (tmp/agent-mesh.md §6.3).
var ErrWaitRequiresDeadline = errors.New("agentcomm: board_key wait with no known producer requires a deadline")

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

// Wait implements Service.Wait.
func (o *Orchestrator) Wait(ctx context.Context, req WaitRequest) (WaitReceipt, error) {
	if req.OperationID == "" {
		return WaitReceipt{}, errors.New("agentcomm: operation id is required")
	}
	if req.Waiter == "" {
		return WaitReceipt{}, errors.New("agentcomm: waiter is required")
	}
	if !req.Condition.Valid() {
		return WaitReceipt{}, &state.OperationRejectedError{Code: assistants.CodeInvalidRequest, Reason: waitConditionHint(req.Condition.Kind)}
	}

	waitID := deriveMeshWaitID(req.OperationID)
	eff, err := marshalEffect("mesh_wait_registered", state.Target{WaitID: string(waitID)}, state.MeshWaitRegisteredPayload{
		Waiter: string(req.Waiter), Condition: req.Condition, Deadline: req.Deadline, ContinuationNote: req.ContinuationNote,
	})
	if err != nil {
		return WaitReceipt{}, err
	}
	op := state.Operation{ID: req.OperationID, Kind: "mesh_wait", Actor: req.Actor, Effects: []state.Effect{eff}, Expect: state.Preconditions{
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

// validateWaitAdmission rejects a wait that could never end on its own: a board_key wait on a key nobody has
// published, without a deadline, or a wait that would close a cycle — its target already (transitively)
// waits on the waiter.
func validateWaitAdmission(rs state.RunState, req WaitRequest) error {
	if req.Condition.Kind == agentcomm.WaitOnBoardKey {
		if _, published := rs.Assistants.Mesh.Board[req.Condition.Key]; !published && req.Deadline == nil {
			return &state.OperationRejectedError{
				Code: assistants.CodeInvalidRequest, Reason: ErrWaitRequiresDeadline.Error() + " (pass --deadline-seconds)", Cause: ErrWaitRequiresDeadline,
			}
		}
	}
	graph := buildMeshWaitGraph(rs)
	for _, target := range meshWaitTargets(rs, req.Condition) {
		if target == req.Waiter {
			continue
		}
		if meshWaitReachable(graph, target, req.Waiter) {
			return &state.OperationRejectedError{
				Code:   agentcomm.CodeWaitCycle,
				Reason: fmt.Sprintf("%q already (transitively) waits on %q", target, req.Waiter),
			}
		}
	}
	return nil
}

// CancelWait implements Service.CancelWait. Idempotent: cancelling an already-resolved wait is a no-op,
// never an error (applyMeshWaitResolved). Only the wait's own waiter or the human (the run owner) may
// cancel it — otherwise one participant could permanently livelock another's awaiting_peer resume by
// cancelling a WaitID it merely learned about (it is never exposed to callers other than the waiter today,
// but ownership is still enforced at the single admission point, not by obscurity).
//
// T5.6: once the cancellation durably sticks (meshWaitResolvedTo confirms THIS call's resolution — not a
// reply that raced in and won instead — is what the wait's status actually holds now), the waiter is woken
// via resumeWaiterAfterTerminalWait. Previously CancelWait never did this at all (see dev-plan.md's own
// "Review decisions" note), stranding a stage parked in awaiting_peer until the next restart — fixed here,
// decided alongside the rest of T5.6's terminal-but-unsatisfied outcomes rather than half-scoped earlier.
func (o *Orchestrator) CancelWait(ctx context.Context, req CancelWaitRequest) error {
	if req.OperationID == "" {
		return errors.New("agentcomm: operation id is required")
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
	if err := o.resolveMeshWaitDurable(req.OperationID, req.WaitID, req.Actor, agentcomm.WaitCancelled, reason); err != nil {
		return err
	}
	if waiter, won := o.meshWaitResolvedTo(req.WaitID, agentcomm.WaitCancelled); won {
		o.resumeWaiterAfterTerminalWait(o.runContext(ctx), waiter) // must not die with the HTTP request
	}
	return nil
}

// resolveMeshWaitDurable is the SINGLE commit path for every non-Ready wait resolution this package produces
// (CancelWait above, a firing deadline timer, startup deadline recovery, and the run-shutdown sweep) — one
// mesh_wait_resolved effect through the same ApplyRunOperation/notice shape CancelWait always used, never a
// second resolution code path. opID must be a stable, deterministic key per resolution ATTEMPT (not merely per
// wait): ApplyRunOperation's own idempotency means a repeat of the SAME opID with the SAME status/reason is a
// safe no-op replay, which is exactly what lets the live timer and the startup-recovery sweep share one key
// (mesh-wait-expire:<id>) without double-committing if both ever somehow ran for the same wait.
func (o *Orchestrator) resolveMeshWaitDurable(opID string, waitID agentcomm.WaitID, actor state.Actor, status agentcomm.WaitStatus, reason string) error {
	eff, err := marshalEffect("mesh_wait_resolved", state.Target{WaitID: string(waitID)},
		state.MeshWaitResolvedPayload{Status: string(status), Reason: reason})
	if err != nil {
		return err
	}
	op := state.Operation{ID: opID, Kind: "mesh_resolve_wait", Actor: actor, Effects: []state.Effect{eff}}
	_, err = o.opts.Store.ApplyRunOperation(op, func(state.OperationResult) {
		o.publishNotice(bus.Event{Type: bus.EventMeshWait, Data: map[string]any{"wait_id": string(waitID), "status": string(status), "reason": reason}})
	})
	return err
}

// meshWaitResolvedTo re-reads the current snapshot and reports whether waitID's status is now exactly want,
// returning its waiter either way. This is T5.6's "did I actually win this resolution" check: a successful
// ApplyRunOperation only means the OPERATION committed, not that THIS call's target status is what stuck —
// applyMeshWaitResolved is a no-op once the wait is no longer Pending (see its own doc comment), so a reply or
// another resolution landing first leaves the wait in some OTHER status. Every caller that would otherwise
// wake a waiter for an outcome that was not actually theirs (CancelWait, a firing deadline timer, startup
// recovery) checks this first — the other outcome's own resume path already owns waking it.
func (o *Orchestrator) meshWaitResolvedTo(waitID agentcomm.WaitID, want agentcomm.WaitStatus) (agentcomm.ParticipantID, bool) {
	rs := o.opts.Store.Snapshot()
	w, ok := rs.Assistants.Mesh.Waits[waitID]
	if !ok {
		return "", false
	}
	return w.Waiter, w.Status == want
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
// resolved another way), and the waiter is woken ONLY if meshWaitResolvedTo confirms Expired is what actually
// stuck — never a fake ack, never a synthesised answer: if a reply won the race instead, that reply's own
// resume path (resumeWaitersAfterCommit) already owns waking the waiter, and the reply's own message/board
// record was never affected by this call at all.
func (o *Orchestrator) expireMeshWait(ctx context.Context, waitID agentcomm.WaitID) {
	if err := o.resolveMeshWaitDurable(meshWaitExpireOpID(waitID), waitID, state.Actor{Kind: "system"}, agentcomm.WaitExpired, meshWaitExpiredReason); err != nil {
		log.Printf("WARN: mesh: expire wait %s: %v", waitID, err)
		return
	}
	if waiter, won := o.meshWaitResolvedTo(waitID, agentcomm.WaitExpired); won {
		o.resumeWaiterAfterTerminalWait(ctx, waiter)
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
		if err := o.resolveMeshWaitDurable(meshWaitExpireOpID(id), id, state.Actor{Kind: "system"}, agentcomm.WaitExpired, meshWaitExpiredReason); err != nil {
			log.Printf("WARN: mesh: recover overdue wait %s: %v", id, err)
			continue
		}
		if waiter, won := o.meshWaitResolvedTo(id, agentcomm.WaitExpired); won {
			o.resumeWaiterAfterTerminalWaitAtStartup(ctx, waiter)
		}
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
		if err := o.resolveMeshWaitDurable("mesh-wait-run-closed:"+string(id), id, state.Actor{Kind: "system"}, agentcomm.WaitRunClosed, meshWaitRunClosedReason); err != nil {
			log.Printf("WARN: mesh: close remaining wait %s at shutdown: %v", id, err)
		}
	}
}

// buildMeshWaitGraph returns every "X cannot proceed until Y acts" edge currently known to the projection:
// live (WaitPending) mesh waits plus assistants' own waiting_children links. This is deliberately NOT the
// full scheduling graph — stage depends_on is static and already cycle-free by flow validation, so it can
// never itself CLOSE a new cycle that a fresh mesh wait could complete; only the two dynamic relationships
// below can.
func buildMeshWaitGraph(rs state.RunState) map[agentcomm.ParticipantID][]agentcomm.ParticipantID {
	g := map[agentcomm.ParticipantID][]agentcomm.ParticipantID{}
	add := func(from, to agentcomm.ParticipantID) {
		if from == "" || to == "" || from == to {
			return
		}
		g[from] = append(g[from], to)
	}

	for _, w := range rs.Assistants.Mesh.Waits {
		if w.Status != agentcomm.WaitPending {
			continue
		}
		for _, target := range meshWaitTargets(rs, w.Condition) {
			add(w.Waiter, target)
		}
	}

	for id, t := range rs.Assistants.Tasks {
		if t.Status != assistants.StatusWaitingChildren {
			continue
		}
		parent := meshHelperParticipantID(id)
		for _, cid := range currentChildren(rs, id, t.TaskGeneration) {
			if c, ok := rs.Assistants.Tasks[cid]; ok && !c.Status.Terminal() {
				add(parent, meshHelperParticipantID(cid))
			}
		}
	}
	return g
}

// meshWaitTargets returns the participant(s) whose action would satisfy cond — who the waiter is, in
// effect, blocked on. A board_key condition with a known current author counts (its publisher IS the
// producer the waiter needs to act); one with no publish yet has no known target and contributes no edge
// (its only guard is ErrWaitRequiresDeadline above, not the cycle check).
func meshWaitTargets(rs state.RunState, cond agentcomm.WaitCondition) []agentcomm.ParticipantID {
	switch cond.Kind {
	case agentcomm.WaitOnReplyTo:
		if m, ok := rs.Assistants.Mesh.Messages[cond.MessageID]; ok {
			return m.Recipients
		}
	case agentcomm.WaitOnAddressedMessage:
		if th, ok := rs.Assistants.Mesh.Threads[cond.ThreadID]; ok {
			return th.Members
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

// meshWaitReachable reports whether, in g, there is a path from start to target within
// agentcomm.MaxCausalHops steps — used to detect that registering a NEW start->target edge would close a
// cycle (target can already reach start, so the two together form a loop no one can break).
func meshWaitReachable(g map[agentcomm.ParticipantID][]agentcomm.ParticipantID, start, target agentcomm.ParticipantID) bool {
	seen := map[agentcomm.ParticipantID]bool{start: true}
	queue := []agentcomm.ParticipantID{start}
	for hops := 0; len(queue) > 0 && hops < agentcomm.MaxCausalHops; hops++ {
		var next []agentcomm.ParticipantID
		for _, n := range queue {
			for _, m := range g[n] {
				if m == target {
					return true
				}
				if !seen[m] {
					seen[m] = true
					next = append(next, m)
				}
			}
		}
		queue = next
	}
	return false
}
