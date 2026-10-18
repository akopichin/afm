package orchestrator

import (
	"context"
	"errors"
	"fmt"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// Agent Mesh Phase 2, T2.3: Wait/CancelWait and the cycle check. Registration and the "is this already
// satisfied" check happen inside the SAME ApplyRunOperation (applyMeshWaitRegistered,
// pkg/state/mesh_waits.go) — a reply that arrived before Wait was called still resolves the wait on the
// very first read after commit, with no separate catch-up pass.

// ErrWaitRequiresDeadline is returned when a board_key condition names a key the board has never seen
// published and the caller did not set a Deadline — an unreachable producer must not be allowed to wait
// forever with no visible way out (tmp/agent-mesh.md §6.3).
var ErrWaitRequiresDeadline = errors.New("agentcomm: board_key wait with no known producer requires a deadline")

// Wait implements Service.Wait.
func (o *Orchestrator) Wait(ctx context.Context, req WaitRequest) (WaitReceipt, error) {
	if req.OperationID == "" {
		return WaitReceipt{}, errors.New("agentcomm: operation id is required")
	}
	if req.Waiter == "" {
		return WaitReceipt{}, errors.New("agentcomm: waiter is required")
	}
	if !req.Condition.Valid() {
		return WaitReceipt{}, fmt.Errorf("agentcomm: invalid wait condition %q", req.Condition.Kind)
	}

	rs := o.opts.Store.Snapshot()

	if req.Condition.Kind == agentcomm.WaitOnBoardKey {
		if _, published := rs.Assistants.Mesh.Board[req.Condition.Key]; !published && req.Deadline == nil {
			return WaitReceipt{}, ErrWaitRequiresDeadline
		}
	}

	graph := buildMeshWaitGraph(rs)
	for _, target := range meshWaitTargets(rs, req.Condition) {
		if target == req.Waiter {
			continue
		}
		if meshWaitReachable(graph, target, req.Waiter) {
			return WaitReceipt{}, &state.OperationRejectedError{
				Code:   agentcomm.CodeWaitCycle,
				Reason: fmt.Sprintf("%q already (transitively) waits on %q", target, req.Waiter),
			}
		}
	}

	waitID := deriveMeshWaitID(req.OperationID)
	eff, err := marshalEffect("mesh_wait_registered", state.Target{WaitID: string(waitID)}, state.MeshWaitRegisteredPayload{
		Waiter: string(req.Waiter), Condition: req.Condition, Deadline: req.Deadline, ContinuationNote: req.ContinuationNote,
	})
	if err != nil {
		return WaitReceipt{}, err
	}
	op := state.Operation{ID: req.OperationID, Kind: "mesh_wait", Actor: req.Actor, Effects: []state.Effect{eff}}
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
	return WaitReceipt{WaitID: waitID, Status: status}, nil
}

// CancelWait implements Service.CancelWait. Idempotent: cancelling an already-resolved wait is a no-op,
// never an error (applyMeshWaitResolved).
func (o *Orchestrator) CancelWait(ctx context.Context, req CancelWaitRequest) error {
	if req.OperationID == "" {
		return errors.New("agentcomm: operation id is required")
	}
	reason := req.Reason
	if reason == "" {
		reason = "cancelled"
	}
	eff, err := marshalEffect("mesh_wait_resolved", state.Target{WaitID: string(req.WaitID)},
		state.MeshWaitResolvedPayload{Status: string(agentcomm.WaitCancelled), Reason: reason})
	if err != nil {
		return err
	}
	op := state.Operation{ID: req.OperationID, Kind: "mesh_cancel_wait", Actor: req.Actor, Effects: []state.Effect{eff}}
	_, err = o.opts.Store.ApplyRunOperation(op, func(state.OperationResult) {
		o.publishNotice(bus.Event{Type: bus.EventMeshWait, Data: map[string]any{"wait_id": string(req.WaitID), "status": string(agentcomm.WaitCancelled)}})
	})
	return err
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
		return []agentcomm.ParticipantID{meshHelperParticipantID(assistants.TaskID(cond.TaskID))}
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
