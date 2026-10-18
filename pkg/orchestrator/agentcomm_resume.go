package orchestrator

import (
	"context"
	"fmt"
	"strings"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// Agent Mesh Phase 5, T5.3: resume on reply — the peer twin of onUserAnswered/resumeAfterAnswer.

// resumeWaitersAfterCommit is called from Send/Publish's externalWork (after their commit) for every
// waiter a wait just resolved for. Only stage-scoped waiters are resumed here — a helper-scoped waiter
// (T5.4, not yet implemented) stays Ready in the durable projection; nothing picks it up automatically yet.
func (o *Orchestrator) resumeWaitersAfterCommit(ctx context.Context, waiters []agentcomm.ParticipantID) {
	for _, w := range waiters {
		if stageID, ok := stageIDFromMeshParticipant(w); ok {
			o.resumeAfterPeerReply(ctx, stageID)
		}
	}
}

// hasOpenMeshWait reports whether stageID currently has a PENDING mesh wait registered (T5.2). Only
// WaitPending counts: a wait already WaitReady at this point means the condition was satisfied before the
// process actually exited — nothing will ever re-trigger a resume for an already-resolved wait, so parking
// in awaiting_peer for one would strand the stage forever. (In practice `afm agent wait` already reports
// "ready" instead of "yield" for this case, so a well-behaved agent would not have exited expecting one.)
func (o *Orchestrator) hasOpenMeshWait(stageID string) bool {
	rs := o.opts.Store.Snapshot()
	waiter := meshStageParticipantID(stageID)
	for _, w := range rs.Assistants.Mesh.Waits {
		if w.Waiter == waiter && w.Status == agentcomm.WaitPending {
			return true
		}
	}
	return false
}

// stageIDFromMeshParticipant inverts meshStageParticipantID.
func stageIDFromMeshParticipant(p agentcomm.ParticipantID) (string, bool) {
	id, ok := strings.CutPrefix(string(p), meshStagePrefix)
	return id, ok && id != ""
}

// resumeAfterPeerReply drives stageID out of awaiting_peer once its wait is ready. Unlike
// resumeAfterAnswer's human-dialog twin, there is no "the live process is polling a file itself" case to
// special-case: a stage in awaiting_peer has, by construction, already exited (that is what the yield in
// T5.2 means) — the only paths are "resume it" or "a concurrent resume already won" (o.Trigger's CAS
// decides, never a mutex, so two replies arriving simultaneously still make exactly one continuation).
func (o *Orchestrator) resumeAfterPeerReply(ctx context.Context, stageID string) {
	if o.currentStatus(stageID) != state.StatusAwaitingPeer {
		return
	}
	o.publishCritical(ctx, bus.Event{Type: bus.EventPeerReplied, StageID: stageID})
}

// onPeerReplied resumes a stage parked in awaiting_peer: FSM-transitions it back to the phase it was
// running (phaseDispatch, keyed on the phase stashed in prePeerPhase when it yielded) and spawns a FRESH
// process for that phase — mirrors onUserAnswered exactly, including spawnKind's existing "no before-hook on
// resume" behavior.
func (o *Orchestrator) onPeerReplied(ctx context.Context, ev bus.Event) error {
	if o.currentStatus(ev.StageID) != state.StatusAwaitingPeer {
		return nil
	}

	phase, ok := o.prePeerPhase.LoadAndDelete(ev.StageID)
	if !ok {
		return nil
	}
	phaseStr, _ := phase.(string)

	stage := o.reg().Stage(ev.StageID)
	if stage == nil {
		return nil
	}

	switch phaseStr {
	case phasePlanning:
		o.Trigger(ev.StageID, bus.EvPeerReplied, bus.GuardCtx{Phase: phasePlanning}, "")
		o.spawnKind(ctx, *stage, kindPlanning, o.runPlanningAgent)
	case phaseImplementation:
		o.Trigger(ev.StageID, bus.EvPeerReplied, bus.GuardCtx{Phase: phaseImplementation}, "")
		o.spawnKind(ctx, *stage, kindImplementation, o.runImplementationAgent)
	case phaseReview:
		o.Trigger(ev.StageID, bus.EvPeerReplied, bus.GuardCtx{Phase: phaseReview}, "")
		o.spawnKind(ctx, *stage, kindReview, o.runReviewAgent)
	case phaseAutonomous:
		o.Trigger(ev.StageID, bus.EvPeerReplied, bus.GuardCtx{Phase: phaseAutonomous}, "")
		o.spawnKind(ctx, *stage, kindAutonomous, o.runAutonomousAgent)
	default:
		return fmt.Errorf("unexpected phase: %q", phaseStr)
	}
	return nil
}
