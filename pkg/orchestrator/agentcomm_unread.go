package orchestrator

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// A stage agent must not finish while a teammate's message to it is still unread.
//
// 1. Extra turn. When a stage agent's turn ends normally (onAgentCompleted) and its inbox still holds messages
//    no turn has been handed yet, the stage does not complete: it parks in awaiting_peer and is resumed at once
//    through the same path a counter-question uses (onPeerQuestion). The fresh turn gets those messages in its
//    <inbox> plus unreadTurnNoteText. At most maxUnreadTurns such turns per stage, so two agents writing to
//    each other forever cannot keep a stage open.
//
// 2. Finishing anyway. A stage that reaches done/failed with unread messages (limit reached, failed, a stale
//    question completed it) will never read them. Each such message becomes undeliverable for that stage, and
//    when no recipient can read it any more, its sender's pending waits for an answer to it are cancelled and
//    the sender is resumed (resumeWaiterAfterTerminalWait). Otherwise a run agent waiting for that reply would
//    hold the run open forever.

// maxUnreadTurns bounds the extra turns a stage gets for messages that arrived while it was working.
const maxUnreadTurns = 5

// unreadTurnNoteText follows the <inbox> block of an extra turn.
const unreadTurnNoteText = "\nTeammates wrote to you while you were working; answer them, then finish.\n"

// continueForUnreadMessages gives stageID one more turn of phase when its agent finished with unread messages.
// It reports whether it did; false means the caller completes the stage as usual. Runs on the event loop.
//
// Only a stage still running its turn (running/retrying) gets one. awaiting_user_input — a stale question let
// the stage complete — finishes as before: a new turn would ask that question again and hang the stage. Part 2
// of the rule above releases the senders.
func (o *Orchestrator) continueForUnreadMessages(ctx context.Context, stageID, phase string) bool {
	if cur := o.currentStatus(stageID); cur != state.StatusRunning && cur != state.StatusRetrying {
		return false
	}
	if !o.hasUndeliveredInbox(meshStageParticipantID(stageID)) {
		return false
	}
	used := 0
	if v, ok := o.unreadTurns.Load(stageID); ok {
		used = v.(int)
	}
	if used >= maxUnreadTurns {
		log.Printf("mesh: stage %q finishes with unread messages after %d extra turns", stageID, used)
		return false
	}
	// The phase marker is saved before the park, as retry.go does for a real wait: a restart between the two
	// recovers the stage through recoverMeshWaits.
	o.prePeerPhase.Store(stageID, phase)
	saveMeshPeerWaitPhase(o.stageDir(stageID), phase)
	if _, ok := o.Trigger(stageID, bus.EvAwaitPeer, bus.GuardCtx{Phase: phase}, "unread messages from teammates"); !ok {
		o.prePeerPhase.Delete(stageID)
		return false
	}
	o.unreadTurns.Store(stageID, used+1)
	o.unreadNotePending.Store(stageID, true)
	if err := o.onPeerQuestion(ctx, bus.Event{StageID: stageID}); err != nil {
		log.Printf("WARN: mesh: resume stage %q for unread messages: %v", stageID, err)
	}
	return true
}

// unreadTurnNote returns the note for stageID's prompt when this turn was started for unread messages, once.
func (o *Orchestrator) unreadTurnNote(stageID string) string {
	if _, ok := o.unreadNotePending.LoadAndDelete(stageID); ok {
		return unreadTurnNoteText
	}
	return ""
}

// releaseUnreadAfterTransition runs part 2 of the rule for a stage that has just finished. It runs off the
// caller's goroutine: a transition may be applied on the event loop, which a stage resume publishes to.
func (o *Orchestrator) releaseUnreadAfterTransition(stageID string) {
	o.concurrency.SpawnDetached(o.runContext(context.Background()), func(ctx context.Context) {
		o.releaseUnreadMessages(ctx, stageID, o.resumeWaiterAfterTerminalWait)
	})
}

// recoverUnreadOfFinishedStages repeats part 2 at startup for every finished stage, in case the process
// stopped between a stage finishing and its unread messages being released. Waiters are resumed by direct
// dispatch: the event loop is not running yet.
func (o *Orchestrator) recoverUnreadOfFinishedStages(ctx context.Context) {
	for _, s := range o.reg().Stages() {
		if bus.IsTerminal(o.currentStatus(s.ID)) {
			o.releaseUnreadMessages(ctx, s.ID, o.resumeWaiterAfterTerminalWaitAtStartup)
		}
	}
}

// releaseUnreadMessages marks every message stageID never read as undeliverable to it and cancels the waits
// that were waiting for an answer to such a message, resuming their waiters through wake.
func (o *Orchestrator) releaseUnreadMessages(ctx context.Context, stageID string, wake func(context.Context, agentcomm.ParticipantID)) {
	recipient := meshStageParticipantID(stageID)
	rs := o.opts.Store.Snapshot()
	var unread []string
	for _, id := range rs.Assistants.Mesh.Inbox[recipient] {
		if rs.Assistants.Mesh.Receipts[id][recipient] == agentcomm.DeliveryAccepted {
			unread = append(unread, string(id))
		}
	}
	if len(unread) == 0 {
		return
	}
	o.publishNotice(bus.Event{Type: bus.EventContextWarning, StageID: stageID, Data: fmt.Sprintf(
		"stage finished with %d unread message(s): %s; teammates waiting for an answer were released", len(unread), strings.Join(unread, ", "))})
	for _, id := range unread {
		ended, err := o.markUnreadUndeliverable(agentcomm.MessageID(id), recipient, stageID)
		if err != nil {
			log.Printf("WARN: mesh: release unread message %s of stage %q: %v", id, stageID, err)
			continue
		}
		for _, waitID := range ended {
			if waiter, won := o.meshWaitResolvedTo(waitID, agentcomm.WaitCancelled); won {
				wake(ctx, waiter)
			}
		}
	}
	// The run does not finish while a finished stage has unread messages (finishedStageHasUnread); they are
	// released now, so let the event loop decide again.
	o.concurrency.WakeEventLoop()
}

// markUnreadUndeliverable commits, in one operation, message id becoming undeliverable to recipient and the
// cancellation of every pending wait of its sender that waited for an answer to it — only when no recipient
// can read the message any more (another one may still answer). It returns the waits it cancelled. The
// operation id is fixed per (message, recipient), so a repeat after a restart is an idempotent replay.
func (o *Orchestrator) markUnreadUndeliverable(id agentcomm.MessageID, recipient agentcomm.ParticipantID, stageID string) ([]agentcomm.WaitID, error) {
	rs := o.opts.Store.Snapshot()
	m := rs.Assistants.Mesh.Messages[id]
	if m == nil {
		return nil, nil
	}
	reason := fmt.Sprintf("stage %s finished without reading message %s", stageID, id)
	undeliverable, err := marshalEffect("mesh_message_undeliverable",
		state.Target{MessageID: string(id), ParticipantID: string(recipient)}, state.MeshMessageReceiptPayload{Reason: reason})
	if err != nil {
		return nil, err
	}
	effects := []state.Effect{undeliverable}
	var ended []agentcomm.WaitID
	if noOtherReader(rs, m, recipient) {
		for _, waitID := range sortedWaitIDs(rs.Assistants.Mesh.Waits) {
			w := rs.Assistants.Mesh.Waits[waitID]
			if w.Status != agentcomm.WaitPending || w.Waiter != m.Sender || !waitsForAnswerTo(w.Condition, m) {
				continue
			}
			eff, err := marshalEffect("mesh_wait_resolved", state.Target{WaitID: string(waitID)},
				state.MeshWaitResolvedPayload{Status: string(agentcomm.WaitCancelled), Reason: reason})
			if err != nil {
				return nil, err
			}
			effects = append(effects, eff)
			ended = append(ended, waitID)
		}
	}
	opID := "mesh-unread-" + deriveMeshID("mesh-unread", string(id)+"|"+string(recipient))
	op := state.Operation{ID: opID, Kind: "mesh_release_unread", Actor: state.Actor{Kind: "system"}, Effects: effects}
	_, err = o.opts.Store.ApplyRunOperation(op, func(state.OperationResult) {
		for _, waitID := range ended {
			o.publishNotice(bus.Event{Type: bus.EventMeshWait, Data: map[string]any{
				"wait_id": string(waitID), "status": string(agentcomm.WaitCancelled), "reason": reason}})
		}
	})
	return ended, err
}

// noOtherReader reports whether no recipient of m other than finished can still answer it: every other
// recipient has it as undeliverable too. A human recipient reads the thread in the dashboard, so a message to
// the human always has a reader.
func noOtherReader(rs state.RunState, m *agentcomm.Message, finished agentcomm.ParticipantID) bool {
	for _, r := range m.Recipients {
		if r == finished {
			continue
		}
		if p, ok := rs.Assistants.Mesh.Participants[r]; ok && p.Kind == agentcomm.KindUser {
			return false
		}
		if rs.Assistants.Mesh.Receipts[m.ID][r] != agentcomm.DeliveryUndeliverable {
			return false
		}
	}
	return true
}

// waitsForAnswerTo reports whether a wait with cond can be satisfied by an answer to m: a reply_to m, or an
// addressed_message wait on m's thread or on any thread.
func waitsForAnswerTo(cond agentcomm.WaitCondition, m *agentcomm.Message) bool {
	switch cond.Kind {
	case agentcomm.WaitOnReplyTo:
		return cond.MessageID == m.ID
	case agentcomm.WaitOnAddressedMessage:
		return cond.ThreadID == "" || cond.ThreadID == m.ThreadID
	default:
		return false
	}
}

// finishedStageHasUnread reports whether some finished stage still has unread messages: part 2 above has not
// released them yet, and a sender may be waiting for an answer. The run must not finish before that.
func finishedStageHasUnread(rs state.RunState) bool {
	for id, st := range rs.Stages {
		if bus.IsTerminal(st.Status) && rs.Assistants.Mesh.HasUnseenMessageFor(meshStageParticipantID(id)) {
			return true
		}
	}
	return false
}

// stageHasUnreadMessages reports whether stageID's inbox holds a message no turn has been handed yet.
func (o *Orchestrator) stageHasUnreadMessages(stageID string) bool {
	return o.opts.Store != nil && o.hasUndeliveredInbox(meshStageParticipantID(stageID))
}
