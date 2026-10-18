package orchestrator

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/memory"
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

// readyWaitSet snapshots which wait ids are CURRENTLY WaitReady — used by Send/Publish as the "before" side
// of a before/after diff (see notifyAndResumeNewlyReadyWaits): the resolution itself happens inside
// applyMeshMessageSent/applyMeshBoardPublished under the commit's own lock, so this snapshot's only job is
// telling that diff which waits were ALREADY ready and must not be re-notified/re-resumed.
func readyWaitSet(rs state.RunState) map[agentcomm.WaitID]bool {
	out := make(map[agentcomm.WaitID]bool, len(rs.Assistants.Mesh.Waits))
	for id, w := range rs.Assistants.Mesh.Waits {
		if w.Status == agentcomm.WaitReady {
			out[id] = true
		}
	}
	return out
}

// notifyAndResumeNewlyReadyWaits is Send/Publish's shared externalWork tail: it re-reads the store AFTER
// their commit and, for every wait that is Ready now but was not in beforeReady, publishes the EventMeshWait
// notice and resumes its waiter — exactly the waits THIS call's commit resolved, without either of them
// needing to carry their own resolved-ids list out of the projection.
func (o *Orchestrator) notifyAndResumeNewlyReadyWaits(ctx context.Context, beforeReady map[agentcomm.WaitID]bool) {
	after := o.opts.Store.Snapshot()
	for _, id := range sortedWaitIDs(after.Assistants.Mesh.Waits) {
		w := after.Assistants.Mesh.Waits[id]
		if w.Status != agentcomm.WaitReady || beforeReady[id] {
			continue
		}
		o.publishNotice(bus.Event{Type: bus.EventMeshWait, Data: map[string]any{"wait_id": string(id), "status": string(agentcomm.WaitReady)}})
		o.resumeWaitersAfterCommit(ctx, []agentcomm.ParticipantID{w.Waiter})
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
// running and spawns a FRESH process for that phase — mirrors onUserAnswered via the shared
// resumeStageForPhase (orchestrator.go), including spawnKind's existing "no before-hook on resume" behavior.
// The phase comes from prePeerPhase when this runs in-process (the normal live-reply path); popPeerWaitPhase
// falls back to the durable marker file when prePeerPhase was never populated in THIS process — the restart
// case recoverMeshWaits below drives.
func (o *Orchestrator) onPeerReplied(ctx context.Context, ev bus.Event) error {
	if o.currentStatus(ev.StageID) != state.StatusAwaitingPeer {
		return nil
	}
	phase, ok := o.popPeerWaitPhase(ev.StageID)
	if !ok {
		return nil
	}
	stage := o.reg().Stage(ev.StageID)
	if stage == nil {
		return nil
	}
	return o.resumeStageForPhase(ctx, *stage, phase, bus.EvPeerReplied)
}

// popPeerWaitPhase returns the phase stashed when stageID parked in awaiting_peer (T5.2), preferring the
// in-memory prePeerPhase (set by THIS process when it yielded) and falling back to the durable marker file
// (set by ANY process, read back after a restart — prePeerPhase itself does not survive one).
func (o *Orchestrator) popPeerWaitPhase(stageID string) (string, bool) {
	if v, ok := o.prePeerPhase.LoadAndDelete(stageID); ok {
		if phase, ok := v.(string); ok && phase != "" {
			return phase, true
		}
	}
	if phase := loadMeshPeerWaitPhase(o.stageDir(stageID)); phase != "" {
		return phase, true
	}
	return "", false
}

// meshPeerWaitPhaseFile is the durable marker retry.go writes right before parking a stage in awaiting_peer
// (T5.2) — prePeerPhase alone is a sync.Map, lost on restart, which would otherwise strand a stage whose
// wait resolved while the orchestrator process was down (recoverMeshWaits below needs this to know which
// kind of agent to respawn).
const meshPeerWaitPhaseFile = "mesh_peer_wait.phase"

func meshPeerWaitPhasePath(stageDir string) string {
	return filepath.Join(stageDir, meshPeerWaitPhaseFile)
}

// saveMeshPeerWaitPhase persists phase for stageDir. Best-effort: a write failure here degrades recovery
// after a crash (resumeStageForPhase would then be unreachable until a human intervenes) but must never
// fail the yield itself — the in-memory prePeerPhase already covers the live, same-process resume path.
func saveMeshPeerWaitPhase(stageDir, phase string) {
	if err := memory.AtomicWrite(meshPeerWaitPhasePath(stageDir), []byte(phase)); err != nil {
		log.Printf("WARN: mesh: persist peer-wait phase for stage dir %q: %v", stageDir, err)
	}
}

func loadMeshPeerWaitPhase(stageDir string) string {
	data, err := os.ReadFile(meshPeerWaitPhasePath(stageDir))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// recoverMeshWaits re-derives, on startup, every stage parked in awaiting_peer whose wait has ALREADY
// become ready — e.g. a reply committed right before a crash, with the live resume never happening because
// the process went down between the commit and the spawn. Mirrors recoverAssistants' role for
// waiting_children: a wait STILL pending is untouched here (a future live Send/Publish will wake it
// normally, through the same path a running orchestrator already uses).
//
// The condition is deliberately "no PENDING wait remains" (hasOpenMeshWait is false), not merely "some Ready
// wait exists": a waiter's OLDER wait from a past, already-resumed awaiting_peer episode stays in the
// journal as Ready forever (waits are never deleted). If this only checked "any Ready wait", a stage on its
// SECOND yield — with a brand-new, still-PENDING wait — would be wrongly resumed on that stale Ready record
// from its FIRST episode, abandoning the second wait's real condition.
func (o *Orchestrator) recoverMeshWaits(ctx context.Context) {
	rs := o.opts.Store.Snapshot()
	for _, s := range o.reg().Stages() {
		if o.currentStatus(s.ID) != state.StatusAwaitingPeer {
			continue
		}
		if o.hasOpenMeshWait(s.ID) {
			continue // the current wait has not resolved yet; a future live Send/Publish will wake it
		}
		waiter := meshStageParticipantID(s.ID)
		ready := false
		for _, w := range rs.Assistants.Mesh.Waits {
			if w.Waiter == waiter && w.Status == agentcomm.WaitReady {
				ready = true
				break
			}
		}
		if !ready {
			continue
		}
		if err := o.onPeerReplied(ctx, bus.Event{StageID: s.ID}); err != nil {
			log.Printf("WARN: mesh: recover awaiting_peer stage %q: %v", s.ID, err)
		}
	}
}
