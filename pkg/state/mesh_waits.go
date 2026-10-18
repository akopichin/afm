package state

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
)

// MeshWait wraps agentcomm.Wait with the run's own generation-fencing fields (T1.3): a wait registered
// against a task/stage generation that has since been superseded (the owning task was retried, or the root
// stage's whole tree was invalidated — see TaskGeneration/RootGenerations) must never be resolved, the same
// "stamp once at registration, compare against the CURRENT value at read time" convention
// assistants.Task.ParentGeneration/RootGeneration already use (see kindTaskGenerationBumped/
// kindRootGenerationBumped's doc comments in record.go). At most one of TargetTaskID/TargetStageID is set,
// naming which generation counter Generation was frozen from; if neither is set, Generation is meaningless
// and the wait is never considered stale (e.g. a wait registered by the human or a run-scoped participant,
// which has no generation to be superseded by).
type MeshWait struct {
	agentcomm.Wait
	TargetTaskID  string `json:"target_task_id,omitempty"`
	TargetStageID string `json:"target_stage_id,omitempty"`
	Generation    uint64 `json:"generation,omitempty"`
}

// MeshWaitRegisteredPayload is the mesh_wait_registered control record's payload — decoded here by
// applyMeshWaitRegistered. The new wait's id is Target.WaitID; Target.TaskID/Target.StageID (when set) name
// the generation-fencing owner (see MeshWait's doc comment) — reusing the existing Target fields rather than
// inventing new ones, since they already mean exactly "the task/stage this record is about".
type MeshWaitRegisteredPayload struct {
	Waiter           string                  `json:"waiter_id"`
	Condition        agentcomm.WaitCondition `json:"condition"`
	Deadline         *time.Time              `json:"deadline,omitempty"`
	ContinuationNote string                  `json:"continuation_note,omitempty"`
	Generation       uint64                  `json:"generation,omitempty"`
}

// applyMeshWaitRegistered projects one mesh_wait_registered control record. If the condition is ALREADY
// satisfied at registration time (e.g. a reply arrived before the Wait was registered — the classic race this
// guards against), the wait is created directly in WaitReady status rather than WaitPending: ReadyWaits then
// surfaces it on the very next call, with no separate "catch up on missed events" pass needed.
func applyMeshWaitRegistered(as *AssistantsState, c *Control, dryRun bool) error {
	id := agentcomm.WaitID(c.Target.WaitID)
	if id == "" {
		return errors.New("mesh_wait_registered: empty target wait_id")
	}
	if _, exists := as.Mesh.Waits[id]; exists {
		return fmt.Errorf("mesh_wait_registered: id %s already exists", id)
	}
	var p MeshWaitRegisteredPayload
	if err := decodePayload(c.Payload, &p); err != nil {
		return fmt.Errorf("decode mesh_wait_registered payload: %w", err)
	}
	w := agentcomm.Wait{
		ID: id, Waiter: agentcomm.ParticipantID(p.Waiter), Condition: p.Condition,
		Deadline: p.Deadline, ContinuationNote: p.ContinuationNote, Status: agentcomm.WaitPending,
	}
	if !w.Valid() {
		return fmt.Errorf("mesh_wait_registered %s: invalid wait", id)
	}
	if dryRun {
		return nil
	}

	mw := &MeshWait{Wait: w, TargetTaskID: c.Target.TaskID, TargetStageID: c.Target.StageID, Generation: p.Generation}
	if as.meshWaitConditionSatisfied(w.Condition, w.Waiter) {
		mw.Status = agentcomm.WaitReady
	}
	as.Mesh.Waits[id] = mw
	as.Mesh.WaitOrder = append(as.Mesh.WaitOrder, id)
	return nil
}

// meshWaitConditionSatisfied reports whether cond already holds RIGHT NOW, purely from data AssistantsState
// itself carries. WaitOnStageStatus is NOT decidable here: stage status lives in RunState.Stages, which
// dispatchControlKind's AssistantsState-only handlers never see (see kindStageGraphAdded's doc comment in
// record.go for the one existing kind that DOES need full RunState, and why it is dispatched differently).
// A WaitOnStageStatus condition therefore always starts WaitPending and can only become WaitReady via an
// explicit mesh_wait_resolved record — the orchestrator (which does read RunState.Stages) is responsible for
// firing it once the named stage reaches the expected status.
func (as *AssistantsState) meshWaitConditionSatisfied(cond agentcomm.WaitCondition, waiter agentcomm.ParticipantID) bool {
	switch cond.Kind {
	case agentcomm.WaitOnReplyTo:
		return as.Mesh.MessageAnswered(cond.MessageID)
	case agentcomm.WaitOnAddressedMessage:
		if cond.ThreadID == "" {
			return as.Mesh.hasUnseenMessageFor(waiter)
		}
		return as.Mesh.hasMessageAddressedTo(cond.ThreadID, waiter)
	case agentcomm.WaitOnTaskResult:
		t, ok := as.Tasks[assistants.TaskID(cond.TaskID)]
		return ok && t.Status.Terminal()
	case agentcomm.WaitOnBoardKey:
		e, ok := as.Mesh.Board[cond.Key]
		return ok && e.Status == cond.Status
	default:
		// WaitOnStageStatus (never decidable here, see doc comment above) and any other/unknown kind.
		return false
	}
}

// resolvePendingWaits flips every currently WaitPending wait matching match to newStatus, in place. Called
// from the fact-recording projection handlers themselves (applyMeshMessageSent, applyMeshBoardPublished,
// applyMeshThreadClosed) so a wait's resolution is decided against the SAME live Mesh state the fact is being
// committed into, under the SAME lock as that commit — never from a snapshot an orchestrator-level caller read
// before the commit, which could miss a Wait registered in the gap, or recompute differently on an idempotent
// replay (the operation's OWN effects must stay fixed; this mutation is not one of them).
func (as *AssistantsState) resolvePendingWaits(newStatus agentcomm.WaitStatus, match func(w *MeshWait) bool) {
	for _, w := range as.Mesh.Waits {
		if w.Status != agentcomm.WaitPending || !match(w) {
			continue
		}
		w.Status = newStatus
	}
}

// MeshWaitResolvedPayload is the mesh_wait_resolved control record's payload — decoded here by
// applyMeshWaitResolved. Status is one of agentcomm's non-Pending WaitStatus values: WaitReady (the
// orchestrator observed the condition become true, e.g. a WaitOnStageStatus it alone can evaluate),
// WaitCancelled (an explicit cancel), WaitExpired (deadline passed, T5.6) or WaitRunClosed (the run wound
// down while this wait was still open, T5.6).
type MeshWaitResolvedPayload struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// applyMeshWaitResolved projects one mesh_wait_resolved control record. Idempotent by construction: a wait
// that is no longer WaitPending never transitions again, so replaying the SAME resolution twice (the "two
// concurrent resolutions" race, T1.3) — or cancelling an already-cancelled wait — is a silent no-op, not an
// error, and never corrupts the stored status. This is also T5.6's whole race-safety story for a deadline
// timer firing at the same moment as a satisfying reply: both land here (the reply via resolvePendingWaits
// under the SAME applyMeshMessageSent/applyMeshBoardPublished commit, the timer via this handler), under the
// SAME store lock, so whichever commits first wins and the other is this exact no-op — never two outcomes,
// never a lost reply (the reply's own message/board record commits independently of whether this wait
// resolution kind wins or loses).
func applyMeshWaitResolved(as *AssistantsState, c *Control, dryRun bool) error {
	id := agentcomm.WaitID(c.Target.WaitID)
	w, ok := as.Mesh.Waits[id]
	if !ok {
		return fmt.Errorf("mesh_wait_resolved: unknown wait_id %s", id)
	}
	var p MeshWaitResolvedPayload
	if err := decodePayload(c.Payload, &p); err != nil {
		return fmt.Errorf("decode mesh_wait_resolved payload: %w", err)
	}
	status := agentcomm.WaitStatus(p.Status)
	if !status.Valid() || status == agentcomm.WaitPending {
		return fmt.Errorf("mesh_wait_resolved %s: invalid resolution status %q", id, p.Status)
	}
	if dryRun {
		return nil
	}
	if w.Status != agentcomm.WaitPending {
		return nil
	}
	w.Status = status
	w.Reason = p.Reason
	return nil
}

// meshWaitGenerationStale reports whether w's frozen Generation no longer matches its target's CURRENT
// generation counter — see MeshWait's doc comment. A wait naming neither a task nor a stage target is never
// stale.
func (as *AssistantsState) meshWaitGenerationStale(w *MeshWait) bool {
	if w.TargetTaskID != "" {
		t, ok := as.Tasks[assistants.TaskID(w.TargetTaskID)]
		return !ok || t.TaskGeneration != w.Generation
	}
	if w.TargetStageID != "" {
		return as.RootGenerations[w.TargetStageID] != w.Generation
	}
	return false
}

// ReadyWaits returns, in deterministic (sorted) order, every Wait currently in WaitReady status whose
// generation is still current — i.e. recovery should resume its waiter. This is a pure read computed fresh
// from the current projection every time, not a persisted queue — the same "no already-queued bit, readiness
// falls out of determinism" contract ReadyContinuations documents for task-tree recovery (T1.3 mirrors it for
// waits).
func (as AssistantsState) ReadyWaits() []agentcomm.Wait {
	ids := make([]string, 0, len(as.Mesh.Waits))
	for id := range as.Mesh.Waits {
		ids = append(ids, string(id))
	}
	slices.Sort(ids)

	var ready []agentcomm.Wait
	for _, idStr := range ids {
		id := agentcomm.WaitID(idStr)
		w := as.Mesh.Waits[id]
		if w.Status != agentcomm.WaitReady {
			continue
		}
		if as.meshWaitGenerationStale(w) {
			continue
		}
		ready = append(ready, w.Wait)
	}
	return ready
}
