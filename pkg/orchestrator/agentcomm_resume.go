package orchestrator

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/memory"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// Agent Mesh Phase 5, T5.3: resume on reply — the peer twin of onUserAnswered/resumeAfterAnswer.
//
// Agent Mesh Phase 5, T5.5 (this file also): answering a counter-question while still waiting. A waiter
// parked in awaiting_peer/waiting_peer can be addressed by a colleague who is NOT the one it is waiting on —
// a genuine counter-question, not the reply notifyAndResumeNewlyReadyWaits already resumes it for. Left
// alone, that message would sit undelivered until the ORIGINAL wait happens to resolve on its own, which can
// deadlock if the sender is itself blocked on this waiter's answer. The fix reuses every primitive T5.3/T5.4
// already built: the SAME CAS-protected "resume this phase exactly once" dispatch (EvPeerQuestion is an exact
// FSM twin of EvPeerReplied), the SAME durable phase marker, and the SAME automatic re-park (retry.go's own
// hasOpenMeshWait recheck on exit already re-parks whenever the ORIGINAL wait is still open, regardless of
// why the process was resumed) — so a bounded counter-question turn requires no new status and no new
// "is a turn in flight" marker: resuming for a question never resolves the original wait, so hasOpenMeshWait
// is still true when it exits, and retry.go/assistants_lifecycle.go re-park it exactly as if nothing special
// happened. The only genuinely new pieces are (1) detecting that a just-delivered message is a counter-
// question rather than the awaited reply, and (2) the separate EvPeerQuestion/EventPeerQuestion event, kept
// distinct from EvPeerReplied/EventPeerReplied purely for observability and tests.

// resumeWaitersAfterCommit is called from Send/Publish's externalWork (after their commit) for every
// waiter a wait just resolved for — a stage-scoped waiter resumes via resumeAfterPeerReply (T5.3), a
// helper-scoped one via tryResumePeer (T5.4). Any other participant kind (the human, a future run/side
// agent) has nothing of its own to resume — it is the one initiating these replies, never parked waiting for
// one.
func (o *Orchestrator) resumeWaitersAfterCommit(ctx context.Context, waiters []agentcomm.ParticipantID) {
	for _, w := range waiters {
		if stageID, ok := stageIDFromMeshParticipant(w); ok {
			o.resumeAfterPeerReply(ctx, stageID)
			continue
		}
		if taskID, ok := o.taskIDFromMeshParticipant(w); ok {
			o.tryResumePeer(ctx, taskID)
		}
	}
}

// readyWaitSet snapshots which wait ids are CURRENTLY WaitReady — the "before" side of a stage transition's
// before/after diff (notifyAndResumeNewlyReadyWaits). An operation needs none: its result names the waits it
// made ready.
func readyWaitSet(rs state.RunState) map[agentcomm.WaitID]bool {
	out := make(map[agentcomm.WaitID]bool, len(rs.Assistants.Mesh.Waits))
	for id, w := range rs.Assistants.Mesh.Waits {
		if w.Status == agentcomm.WaitReady {
			out[id] = true
		}
	}
	return out
}

// notifyAndResumeWaits is the shared post-commit tail of every operation that can resolve waits: for each wait
// the commit made ready (state.OperationResult.ReadyWaits — decided under the store lock, so two concurrent
// commits never both claim one), it publishes the EventMeshWait notice and resumes its waiter.
func (o *Orchestrator) notifyAndResumeWaits(ctx context.Context, ready []agentcomm.WaitID) {
	if len(ready) == 0 {
		return
	}
	rs := o.opts.Store.Snapshot()
	for _, id := range ready {
		w, ok := rs.Assistants.Mesh.Waits[id]
		if !ok {
			continue
		}
		o.publishNotice(bus.Event{Type: bus.EventMeshWait, Data: map[string]any{"wait_id": string(id), "status": string(agentcomm.WaitReady)}})
		o.resumeWaitersAfterCommit(ctx, []agentcomm.ParticipantID{w.Waiter})
	}
}

// notifyAndResumeNewlyReadyWaits is notifyAndResumeWaits for a stage transition, which is not an operation and
// reports no ReadyWaits: every wait ready now but not in beforeReady.
func (o *Orchestrator) notifyAndResumeNewlyReadyWaits(ctx context.Context, beforeReady map[agentcomm.WaitID]bool) {
	after := o.opts.Store.Snapshot()
	var ready []agentcomm.WaitID
	for _, id := range sortedWaitIDs(after.Assistants.Mesh.Waits) {
		if after.Assistants.Mesh.Waits[id].Status == agentcomm.WaitReady && !beforeReady[id] {
			ready = append(ready, id)
		}
	}
	o.notifyAndResumeWaits(ctx, ready)
}

// stageStatusWaitsPending reports whether some pending stage_status wait names stageID, with the waits ready
// before the transition about to be applied — the "before" side for resumeWaitersAfterTransition.
func (o *Orchestrator) stageStatusWaitsPending(stageID string) (map[agentcomm.WaitID]bool, bool) {
	if o.opts.Store == nil {
		return nil, false // a bare test Orchestrator built around an FSM alone
	}
	rs := o.opts.Store.Snapshot()
	for _, w := range rs.Assistants.Mesh.Waits {
		if w.Status == agentcomm.WaitPending && w.Condition.Kind == agentcomm.WaitOnStageStatus && w.Condition.StageID == stageID {
			return readyWaitSet(rs), true
		}
	}
	return nil, false
}

// resumeWaitersAfterTransition resumes the waiters whose stage_status waits a transition just resolved (the
// store resolves them in the transition itself, state.RunState.resolveStageStatusWaits). It runs off the
// caller's goroutine: a transition may be applied on the event loop, which a stage resume publishes to.
func (o *Orchestrator) resumeWaitersAfterTransition(beforeReady map[agentcomm.WaitID]bool) {
	o.concurrency.SpawnDetached(o.runContext(context.Background()), func(ctx context.Context) {
		o.notifyAndResumeNewlyReadyWaits(ctx, beforeReady)
	})
}

// hasOpenMeshWaitFor reports whether waiter currently has a PENDING mesh wait registered — the shared
// primitive behind hasOpenMeshWait (stage-scoped, T5.2) and runHelperTask's own yield check (helper-scoped,
// T5.4). Only WaitPending counts: a wait already WaitReady at this point means the condition was satisfied
// before the process actually exited — nothing will ever re-trigger a resume for an already-resolved wait, so
// parking in awaiting_peer/waiting_peer for one would strand the waiter forever. (In practice `afm agent wait`
// already reports "ready" instead of "yield" for this case, so a well-behaved agent would not have exited
// expecting one.)
func (o *Orchestrator) hasOpenMeshWaitFor(waiter agentcomm.ParticipantID) bool {
	rs := o.opts.Store.Snapshot()
	for _, w := range rs.Assistants.Mesh.Waits {
		if w.Waiter == waiter && w.Status == agentcomm.WaitPending {
			return true
		}
	}
	return false
}

// hasOpenMeshWait is hasOpenMeshWaitFor's stage-scoped convenience wrapper (T5.2's own call sites).
func (o *Orchestrator) hasOpenMeshWait(stageID string) bool {
	return o.hasOpenMeshWaitFor(meshStageParticipantID(stageID))
}

// stageIDFromMeshParticipant inverts meshStageParticipantID.
func stageIDFromMeshParticipant(p agentcomm.ParticipantID) (string, bool) {
	id, ok := strings.CutPrefix(string(p), meshStagePrefix)
	return id, ok && id != ""
}

// taskIDFromMeshParticipant inverts meshHelperParticipantID and meshRunParticipantID (see
// runTaskForParticipant for the legacy "run:<profileID>" form).
func (o *Orchestrator) taskIDFromMeshParticipant(p agentcomm.ParticipantID) (assistants.TaskID, bool) {
	if id, ok := strings.CutPrefix(string(p), meshHelperPrefix); ok && id != "" {
		return assistants.TaskID(id), true
	}
	return runTaskForParticipant(o.opts.Store.Snapshot(), p)
}

// resumeAwaitingPeerStage drives stageID out of awaiting_peer via evType (EventPeerReplied or, T5.5,
// EventPeerQuestion) — the shared publish-side body behind resumeAfterPeerReply/resumeAfterPeerQuestion.
// Unlike resumeAfterAnswer's human-dialog twin, there is no "the live process is polling a file itself" case
// to special-case: a stage in awaiting_peer has, by construction, already exited (that is what the yield in
// T5.2 means) — the only paths are "resume it" or "a concurrent resume already won" (o.Trigger's CAS
// decides, never a mutex, so two replies — or two counter-questions, or one of each — arriving simultaneously
// still make exactly one continuation).
func (o *Orchestrator) resumeAwaitingPeerStage(ctx context.Context, stageID string, evType bus.EventType) {
	if o.currentStatus(stageID) != state.StatusAwaitingPeer {
		return
	}
	o.publishCritical(ctx, bus.Event{Type: evType, StageID: stageID})
}

// resumeAfterPeerReply is resumeAwaitingPeerStage's T5.3 entry point: stageID's OWN wait just became ready.
func (o *Orchestrator) resumeAfterPeerReply(ctx context.Context, stageID string) {
	o.resumeAwaitingPeerStage(ctx, stageID, bus.EventPeerReplied)
}

// resumeAfterPeerQuestion is resumeAwaitingPeerStage's T5.5 entry point: stageID's own wait is STILL pending,
// but a colleague just addressed it a message anyway — see maybeResumeAwaitingPeerQuestion for the detection
// this is called from.
func (o *Orchestrator) resumeAfterPeerQuestion(ctx context.Context, stageID string) {
	o.resumeAwaitingPeerStage(ctx, stageID, bus.EventPeerQuestion)
}

// resumeAwaitingPeerPhase resumes a stage parked in awaiting_peer: FSM-transitions it back to the phase it
// was running (via fsmEv — EvPeerReplied or, T5.5, EvPeerQuestion, both an identical transition shape) and
// spawns a FRESH process for that phase — mirrors onUserAnswered via the shared resumeStageForPhase
// (orchestrator.go), including spawnKind's existing "no before-hook on resume" behavior. The phase comes from
// prePeerPhase when this runs in-process (the normal live-trigger path); popPeerWaitPhase falls back to the
// durable marker file when prePeerPhase was never populated in THIS process — the restart case recoverMeshWaits
// below drives. Which fsmEv is passed in does not change any of this: a bounded T5.5 counter-question turn
// needs no different spawn behavior, only a differently-labeled resume (see this file's own header comment).
func (o *Orchestrator) resumeAwaitingPeerPhase(ctx context.Context, ev bus.Event, fsmEv bus.FSMEvent) error {
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
	return o.resumeStageForPhase(ctx, *stage, phase, fsmEv)
}

// onPeerReplied is resumeAwaitingPeerPhase's T5.3 handler, dispatched from EventPeerReplied.
func (o *Orchestrator) onPeerReplied(ctx context.Context, ev bus.Event) error {
	return o.resumeAwaitingPeerPhase(ctx, ev, bus.EvPeerReplied)
}

// onPeerQuestion is resumeAwaitingPeerPhase's T5.5 handler, dispatched from EventPeerQuestion: the resumed
// turn is bounded in practice (not in code) — once it exits, retry.go's own hasOpenMeshWait recheck re-parks
// the stage in awaiting_peer again automatically if the ORIGINAL wait this function never touches is still
// pending, with no new wait registration.
func (o *Orchestrator) onPeerQuestion(ctx context.Context, ev bus.Event) error {
	return o.resumeAwaitingPeerPhase(ctx, ev, bus.EvPeerQuestion)
}

// resumeAfterPeerWaitEnded is resumeAwaitingPeerStage's T5.6 entry point: stageID's own wait resolved to a
// terminal-but-UNSATISFIED outcome (cancelled or expired) — see EventPeerWaitEnded's own doc comment for why
// this is never EventPeerReplied.
func (o *Orchestrator) resumeAfterPeerWaitEnded(ctx context.Context, stageID string) {
	o.resumeAwaitingPeerStage(ctx, stageID, bus.EventPeerWaitEnded)
}

// onPeerWaitEnded is resumeAwaitingPeerPhase's T5.6 handler, dispatched from EventPeerWaitEnded.
func (o *Orchestrator) onPeerWaitEnded(ctx context.Context, ev bus.Event) error {
	return o.resumeAwaitingPeerPhase(ctx, ev, bus.EvPeerWaitEnded)
}

// resumeWaiterAfterTerminalWait wakes waiter — LIVE path — once one of its waits has resolved to a terminal-
// but-UNSATISFIED status (WaitCancelled or WaitExpired, never WaitReady). Callers (CancelWait, a firing
// deadline timer) must first confirm via meshWaitResolvedTo that THEIR OWN resolution attempt is the one that
// actually stuck — a reply landing first resolves the SAME wait to WaitReady instead, and that outcome's own
// resume path (resumeWaitersAfterCommit) already owns waking the waiter; calling this one too would fire a
// second, contradictory resume for the same episode. A stage goes through the critical bus exactly like
// resumeAfterPeerReply/resumeAfterPeerQuestion (CAS-protected by resumeAwaitingPeerStage's own currentStatus
// guard — a concurrent Pause/Retry/Revise that already moved the stage out of awaiting_peer makes this a
// silent no-op, never a stale turn or a lifted manual stop); a task never touches the bus at all, like every
// other task resume in this file.
func (o *Orchestrator) resumeWaiterAfterTerminalWait(ctx context.Context, waiter agentcomm.ParticipantID) {
	if stageID, ok := stageIDFromMeshParticipant(waiter); ok {
		o.resumeAfterPeerWaitEnded(ctx, stageID)
		return
	}
	if taskID, ok := o.taskIDFromMeshParticipant(waiter); ok {
		o.tryResumePeerForTerminalWait(ctx, taskID)
	}
}

// resumeWaiterAfterTerminalWaitAtStartup is resumeWaiterAfterTerminalWait's RECOVERY twin, called from
// recoverMeshWaitDeadlines before Run's event loop starts draining the critical bus — a stage branch must
// dispatch onPeerWaitEnded DIRECTLY instead of publishing, for the exact same reason recoverMeshWaits' own doc
// comment gives for onPeerReplied/onPeerQuestion. The task branch is identical to the live path:
// resumeTaskViaCAS never goes through the critical bus either way.
func (o *Orchestrator) resumeWaiterAfterTerminalWaitAtStartup(ctx context.Context, waiter agentcomm.ParticipantID) {
	if stageID, ok := stageIDFromMeshParticipant(waiter); ok {
		if err := o.onPeerWaitEnded(ctx, bus.Event{StageID: stageID}); err != nil {
			log.Printf("WARN: mesh: recover peer-wait-ended for stage %q: %v", stageID, err)
		}
		return
	}
	if taskID, ok := o.taskIDFromMeshParticipant(waiter); ok {
		o.tryResumePeerForTerminalWait(ctx, taskID)
	}
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

// tryResumePeer moves taskID from waiting_peer back to queued once its mesh wait has resolved (T5.4) — the
// helper-task twin of tryResumeParent (assistants_delegate.go): both share resumeTaskViaCAS's CAS-and-requeue
// step, but unlike tryResumeParent there is no separate "are all children terminal" readiness condition to
// re-derive here — a mesh wait that is no longer PENDING already means "go" (see hasOpenMeshWaitFor's own doc
// comment for why an unresolved/pending wait must never be requeued). That check is what makes this function
// safe to call unconditionally for EVERY task sitting in waiting_peer, including from a startup recovery sweep
// racing a live resume for the exact same task — see resumeTaskViaCAS's own doc comment for why a lost CAS
// race is always a silent no-op, never a double requeue.
func (o *Orchestrator) tryResumePeer(ctx context.Context, taskID assistants.TaskID) {
	o.resumeTaskIfWaitCleared(ctx, taskID, "resumed: peer replied")
}

// resumeTaskIfWaitCleared is the shared guard+CAS-resume step behind tryResumePeer (T5.4, a satisfied wait)
// and tryResumePeerForTerminalWait (T5.6, an unsatisfied one — cancelled/expired): only resume once the
// task's own mesh wait set is completely clear (no WaitPending left) — resumeTaskViaCAS's own TaskStatus
// precondition is what then makes a redundant/racing call (two terminal outcomes landing together, or a live
// call racing a recovery sweep) a safe no-op, not this check. reason is the only thing that differs between
// the two callers: a truthful status-change label, never shared text that would misdescribe what happened.
func (o *Orchestrator) resumeTaskIfWaitCleared(ctx context.Context, taskID assistants.TaskID, reason string) {
	rs := o.opts.Store.Snapshot()
	t, ok := rs.Assistants.Tasks[taskID]
	if !ok || t.Status != assistants.StatusWaitingPeer {
		return
	}
	if o.hasOpenMeshWaitFor(meshParticipantForTask(rs, t.Task)) {
		return // the wait that parked this task is still genuinely pending — nothing to resume yet
	}
	o.resumeTaskViaCAS(ctx, taskID, t.RootStageID, assistants.StatusWaitingPeer, reason)
}

// tryResumePeerForTerminalWait is tryResumePeer's T5.6 counterpart for a wait that resolved WITHOUT being
// satisfied (cancelled or expired): same guard, but "resumed: peer replied" would be a fake ack for an
// outcome that never produced a reply, so it gets its own truthful reason instead.
func (o *Orchestrator) tryResumePeerForTerminalWait(ctx context.Context, taskID assistants.TaskID) {
	o.resumeTaskIfWaitCleared(ctx, taskID, "resumed: peer wait ended without a reply")
}

// tryResumePeerForQuestion is tryResumePeer's T5.5 counterpart: unlike tryResumePeer (which only ever fires
// once the task's OWN wait has resolved), this fires while that wait is STILL pending, purely because a
// colleague addressed the task a message it has not had a turn to read yet — "a child questions a waiting
// parent" is exactly this, applied to a parent task sitting in waiting_peer like any other mesh participant,
// no separate machinery needed. Shares resumeTaskViaCAS's CAS-and-requeue step with tryResumePeer/
// tryResumeParent (dev-plan.md's own instruction for T5.4, extended here rather than duplicated): the
// precondition (TaskStatus: waiting_peer) is what makes a redundant call (a live Send racing a recovery sweep
// for the same task) a safe no-op, same as always.
func (o *Orchestrator) tryResumePeerForQuestion(ctx context.Context, taskID assistants.TaskID) {
	rs := o.opts.Store.Snapshot()
	t, ok := rs.Assistants.Tasks[taskID]
	if !ok || t.Status != assistants.StatusWaitingPeer {
		return
	}
	o.resumeTaskViaCAS(ctx, taskID, t.RootStageID, assistants.StatusWaitingPeer, "resumed: peer question while waiting")
}

// hasWakingUnread reports whether participant has a message that should wake it (state.MeshState's
// HasWakingUnreadFor): one still at DeliveryAccepted — never included in any of its own spawned turns' <inbox>
// block — and not a passive turn result. agentCommInboxFor's OnDelivered closure (T4.4) marks every CURRENTLY
// pending message Delivered as soon as a fresh process for that participant actually starts — so in practice
// this is only ever true for a message that arrived strictly AFTER the participant's most recent turn began,
// exactly the "a colleague has something for you that you have not had a chance to see yet" signal T5.5 needs,
// with no separate watermark.
func (o *Orchestrator) hasWakingUnread(participant agentcomm.ParticipantID) bool {
	return o.opts.Store.Snapshot().Assistants.Mesh.HasWakingUnreadFor(participant)
}

// maybeResumeAwaitingPeerQuestion resumes participant for a bounded T5.5 counter-question turn if, and only
// if, it is CURRENTLY parked with its own wait still pending (hasOpenMeshWaitFor) AND has at least one
// undelivered message (hasWakingUnread) — both conditions re-derived fresh from durable state, so this is
// safe to call redundantly from anywhere: Send's own externalWork tail (the live path, right after the
// message that might BE a counter-question commits) and recoverMeshWaits/recoverAssistants' waiting_peer
// branch (startup recovery, T5.5's "crash in the middle of the clarification recovers to exactly one pending
// turn" — the resume this triggers is CAS-protected exactly like T5.3's, so a redundant call here racing a
// live trigger, or a second recovery pass, is always a silent no-op, never a second turn).
func (o *Orchestrator) maybeResumeAwaitingPeerQuestion(ctx context.Context, participant agentcomm.ParticipantID) {
	if !o.hasOpenMeshWaitFor(participant) || !o.hasWakingUnread(participant) {
		return
	}
	if stageID, ok := stageIDFromMeshParticipant(participant); ok {
		o.resumeAfterPeerQuestion(ctx, stageID)
		return
	}
	if taskID, ok := o.taskIDFromMeshParticipant(participant); ok {
		o.tryResumePeerForQuestion(ctx, taskID)
	}
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
//
// T5.5: when the current wait is STILL pending, this used to simply move on ("a future live Send/Publish will
// wake it normally") — but a counter-question's live trigger (maybeResumeAwaitingPeerQuestion, called from
// Send's own externalWork tail) could itself have been lost to the same crash this sweep exists to recover
// from, exactly like an ordinary reply's live trigger can (see the doc comment above). Re-deriving the SAME
// condition here (still-pending wait + an undelivered message) recovers that case too, to exactly one pending
// turn: the resume it triggers is CAS-protected the same way, so finding this stage again on a second sweep —
// after the first already resumed it — is a correct, silent no-op (status is no longer awaiting_peer).
//
// This calls onPeerQuestion directly, the same way the ready-wait branch below calls onPeerReplied directly,
// rather than going through resumeAfterPeerQuestion's publishCritical: recoverMeshWaits runs BEFORE Run's own
// event loop starts draining the critical bus (orchestrator.go), so a published-but-unprocessed event here
// would only be picked up once that loop starts — direct dispatch makes recovery deterministic instead of
// relying on the bus's buffer and ordering. maybeResumeAwaitingPeerQuestion stays the right call for the LIVE
// Send path (agentcomm_messages.go) and the helper-task recovery branch below, both of which either already
// run with the loop active or already dispatch directly (resumeTaskViaCAS never goes through the critical
// bus at all).
func (o *Orchestrator) recoverMeshWaits(ctx context.Context) {
	rs := o.opts.Store.Snapshot()
	for _, s := range o.reg().Stages() {
		if o.currentStatus(s.ID) != state.StatusAwaitingPeer {
			continue
		}
		waiter := meshStageParticipantID(s.ID)
		if o.hasOpenMeshWait(s.ID) {
			// The ORIGINAL wait has not resolved — but a colleague may have asked a counter-question while the
			// process was down; recover that as a bounded T5.5 turn instead of leaving it stranded until the
			// original wait happens to resolve on its own.
			if o.hasWakingUnread(waiter) {
				if err := o.onPeerQuestion(ctx, bus.Event{StageID: s.ID}); err != nil {
					log.Printf("WARN: mesh: recover awaiting_peer counter-question for stage %q: %v", s.ID, err)
				}
			}
			continue
		}
		ready, ended := false, false
		for _, w := range rs.Assistants.Mesh.Waits {
			if w.Waiter != waiter {
				continue
			}
			ready = ready || w.Status == agentcomm.WaitReady
			ended = ended || w.Status == agentcomm.WaitCancelled || w.Status == agentcomm.WaitExpired
		}
		switch {
		case ready:
			if err := o.onPeerReplied(ctx, bus.Event{StageID: s.ID}); err != nil {
				log.Printf("WARN: mesh: recover awaiting_peer stage %q: %v", s.ID, err)
			}
		case ended:
			// The wait was cancelled or expired (e.g. its colleague finished without reading the question) and
			// the process stopped before the waiter was woken.
			if err := o.onPeerWaitEnded(ctx, bus.Event{StageID: s.ID}); err != nil {
				log.Printf("WARN: mesh: recover awaiting_peer stage %q after its wait ended: %v", s.ID, err)
			}
		case o.hasWakingUnread(waiter):
			// Parked for unread messages with no wait at all (agentcomm_unread.go), and the process stopped
			// before the extra turn started.
			if err := o.onPeerQuestion(ctx, bus.Event{StageID: s.ID}); err != nil {
				log.Printf("WARN: mesh: recover awaiting_peer stage %q for unread messages: %v", s.ID, err)
			}
		default:
		}
	}
}
