package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/prompts"
	"github.com/akopichin/afm/pkg/state"
)

// A wait for an answer must end once nobody left can send it: its answerers all finished, gave the message up
// unread, or went idle after reading it without replying (leftUnanswered). Admission refuses such a wait too.

// errWaitStillAnswerable rejects a cancellation decided on a snapshot in which nobody could answer, when the
// commit's state says otherwise (the wait was answered or resolved meanwhile).
var errWaitStillAnswerable = errors.New("mesh: the wait can still be answered")

// releaseUnanswerableWaitsAfterFinish runs releaseUnanswerableWaits off the caller's goroutine: a finish may be
// committed on the event loop, which a stage resume publishes to. A bare test harness without a store or a
// concurrency manager has no mesh to release.
//
// It spawns only when some wait is unanswerable right now — the common finish has none, and a stage finishing
// while the run is already draining its agents must not start new work then.
func (o *Orchestrator) releaseUnanswerableWaitsAfterFinish() {
	if o.opts.Store == nil || o.concurrency == nil || !anyUnanswerableWait(o.opts.Store.Snapshot()) {
		return
	}
	o.concurrency.SpawnDetached(o.runContext(context.Background()), func(ctx context.Context) {
		if o.sweepGate != nil {
			o.sweepGate()
		}
		o.releaseUnanswerableWaits(ctx, o.resumeWaiterAfterTerminalWait)
	})
}

// releaseUnanswerableWaits cancels, in one commit, every pending wait nobody left can answer and wakes each
// waiter once through wake — only for the waits that commit ended, so a reply or another resolution that won
// is never announced or woken twice.
func (o *Orchestrator) releaseUnanswerableWaits(ctx context.Context, wake func(context.Context, agentcomm.ParticipantID)) {
	if o.opts.Store == nil {
		return
	}
	rs := o.opts.Store.Snapshot()
	var ids []agentcomm.WaitID
	reasons := map[agentcomm.WaitID]string{}
	for _, id := range sortedWaitIDs(rs.Assistants.Mesh.Waits) {
		if reason, ok := unanswerableWait(rs, id); ok {
			ids = append(ids, id)
			reasons[id] = reason
		}
	}
	if len(ids) == 0 {
		return
	}
	if o.settlePlannedHook != nil {
		o.settlePlannedHook()
	}
	stillUnanswerable := func(cur *state.RunState) error {
		for _, id := range ids {
			if _, ok := unanswerableWait(*cur, id); !ok {
				return errWaitStillAnswerable
			}
		}
		return nil
	}
	op := state.Operation{ID: "mesh-unanswerable:" + randomOpSuffix(), Kind: "mesh_resolve_wait", Actor: state.Actor{Kind: "system"},
		Effects: cancelWaitEffects(ids, reasons), Expect: state.Preconditions{Check: stillUnanswerable}}
	res, err := o.opts.Store.ApplyRunOperation(op, nil)
	if errors.Is(err, errWaitStillAnswerable) {
		o.releaseUnanswerableWaits(ctx, wake) // decided on a state that changed meanwhile: decide again
		return
	}
	if err != nil {
		log.Printf("WARN: mesh: release unanswerable waits: %v", err)
		o.handleTaskOperationErr(err)
		return
	}
	// Woken only after every wait is ended: a waiter resumed by the first of its waits could otherwise find
	// another still pending, park again and be woken a second time.
	for _, waiter := range o.announceEndedWaits(res.EndedWaits, reasons) {
		wake(ctx, waiter)
	}
}

// leftWaits are the pending waits nobody left can answer because task agent p, idle now, left their request
// (leftUnanswered), with why, in id order: the waits p's next turn would hide by making it busy.
func leftWaits(rs state.RunState, p agentcomm.ParticipantID) ([]agentcomm.WaitID, map[agentcomm.WaitID]string) {
	var ids []agentcomm.WaitID
	reasons := map[agentcomm.WaitID]string{}
	for _, id := range sortedWaitIDs(rs.Assistants.Mesh.Waits) {
		w := rs.Assistants.Mesh.Waits[id]
		if w.Status != agentcomm.WaitPending || w.Condition.Kind != agentcomm.WaitOnReplyTo || !leftUnanswered(rs, w.Condition.MessageID, p) {
			continue
		}
		if reason, ok := unanswerableWait(rs, id); ok {
			ids = append(ids, id)
			reasons[id] = reason
		}
	}
	return ids, reasons
}

// errLeftWaitsChanged rejects a turn start planned on a snapshot whose left waits (leftWaits) are not the
// commit's any more.
var errLeftWaitsChanged = errors.New("mesh: the waits a turn start settles changed")

// cancelWaitEffects cancels each of ids with its reason.
func cancelWaitEffects(ids []agentcomm.WaitID, reasons map[agentcomm.WaitID]string) []state.Effect {
	effects := make([]state.Effect, 0, len(ids))
	for _, id := range ids {
		eff, err := marshalEffect("mesh_wait_resolved", state.Target{WaitID: string(id)},
			state.MeshWaitResolvedPayload{Status: string(agentcomm.WaitCancelled), Reason: reasons[id]})
		if err != nil {
			log.Printf("WARN: mesh: cancel the wait %s: %v", id, err)
			continue
		}
		effects = append(effects, eff)
	}
	return effects
}

// announceEndedWaits announces each wait a commit ended (cancelled, with its reason) and returns their waiters,
// each once.
func (o *Orchestrator) announceEndedWaits(ended []agentcomm.WaitID, reasons map[agentcomm.WaitID]string) []agentcomm.ParticipantID {
	rs := o.opts.Store.Snapshot()
	var waiters []agentcomm.ParticipantID
	for _, id := range ended {
		w := rs.Assistants.Mesh.Waits[id]
		if w == nil {
			continue
		}
		o.announceEndedWait(id, w.Status, reasons[id])
		if !slices.Contains(waiters, w.Waiter) {
			waiters = append(waiters, w.Waiter)
		}
	}
	return waiters
}

// wakeAfterSettledStart wakes, off the caller's goroutine, the waiters of the waits a turn start settled: the
// start may run on the event loop, which a stage resume publishes to.
func (o *Orchestrator) wakeAfterSettledStart(ended []agentcomm.WaitID, reasons map[agentcomm.WaitID]string) {
	waiters := o.announceEndedWaits(ended, reasons)
	if len(waiters) == 0 {
		return
	}
	o.concurrency.SpawnDetached(o.runContext(context.Background()), func(ctx context.Context) {
		for _, waiter := range waiters {
			o.resumeWaiterAfterTerminalWait(ctx, waiter)
		}
	})
}

// anyUnanswerableWait reports whether some pending wait of rs is unanswerable.
func anyUnanswerableWait(rs state.RunState) bool {
	for id := range rs.Assistants.Mesh.Waits {
		if _, ok := unanswerableWait(rs, id); ok {
			return true
		}
	}
	return false
}

// unanswerableWait reports whether waitID is a pending wait for an answer that nobody left can send — none of the
// participants who could send it (other than the waiter) can still answer it (canStillAnswer) — and says why. Other kinds of waits end
// on their own: a task_result wait when its task finishes, a stage_status wait with its stage.
func unanswerableWait(rs state.RunState, waitID agentcomm.WaitID) (string, bool) {
	w := rs.Assistants.Mesh.Waits[waitID]
	if w == nil || w.Status != agentcomm.WaitPending {
		return "", false
	}
	if w.Condition.Kind != agentcomm.WaitOnReplyTo && w.Condition.Kind != agentcomm.WaitOnAddressedMessage {
		return "", false
	}
	targets := slices.DeleteFunc(slices.Clone(meshWaitTargets(rs, w.Condition)), func(p agentcomm.ParticipantID) bool { return p == w.Waiter })
	return nobodyLeft(rs, w.Condition, targets)
}

// nobodyLeft reports whether none of targets can still answer cond, and says why. The reasons come from recorded
// (already sanitised) task status reasons; the joined text is bounded too.
func nobodyLeft(rs state.RunState, cond agentcomm.WaitCondition, targets []agentcomm.ParticipantID) (string, bool) {
	if len(targets) == 0 || slices.ContainsFunc(targets, func(p agentcomm.ParticipantID) bool { return canStillAnswer(rs, cond, p) }) {
		return "", false
	}
	why := make([]string, len(targets))
	for i, p := range targets {
		why[i] = whyCannotAnswer(rs, cond, p)
	}
	return prompts.TruncateUTF8("nobody left can answer: "+strings.Join(why, "; "), maxTaskReasonBytes, "…"), true
}

// whyCannotAnswer names p and why it can no longer answer cond.
func whyCannotAnswer(rs state.RunState, cond agentcomm.WaitCondition, p agentcomm.ParticipantID) string {
	participant, _ := resolveParticipant(rs, p)
	name := participant.DisplayName
	if name == "" {
		name = string(p)
	}
	switch {
	case cond.Kind == agentcomm.WaitOnReplyTo && leftUnanswered(rs, cond.MessageID, p):
		why := name + " " + leftUnansweredReason
		if request := rs.Assistants.Mesh.Messages[cond.MessageID]; request != nil {
			if id := latestTurnResult(rs, p, request.Sender); id != "" {
				why += " (its latest turn result: " + string(id) + ")"
			}
		}
		return why
	case participant.UnreachableReason == "":
		return fmt.Sprintf("%s gave the message up unread", name)
	default:
		return fmt.Sprintf("%s — %s", name, participant.UnreachableReason)
	}
}

// leftUnansweredReason is why a task agent that read a request no longer answers it.
const leftUnansweredReason = "read the message and its turn ended without a reply"

// leftUnanswered reports whether task agent p read request id and is idle without having answered it: every
// turn is a new session and its result answers only the waking message. A request still in the turn input, or
// owed by an earlier turn that yielded, is not left; nor is any in a journal from before turns were recorded.
func leftUnanswered(rs state.RunState, id agentcomm.MessageID, p agentcomm.ParticipantID) bool {
	if r := rs.Assistants.Mesh.Receipts[id][p]; r != agentcomm.DeliveryDelivered && r != agentcomm.DeliveryAcknowledged {
		return false
	}
	taskID, ok := participantTask(rs, p)
	if !ok {
		return false
	}
	t := rs.Assistants.Tasks[taskID]
	if t == nil || t.Turn == 0 {
		return false
	}
	switch t.Status {
	case assistants.StatusRunning, assistants.StatusWaitingPeer, assistants.StatusWaitingChildren:
		return false
	}
	return !t.Status.Terminal() && !slices.Contains(t.TurnInbox, id) && !slices.Contains(t.OpenRequests, id) &&
		!rs.Assistants.Mesh.MessageAnsweredByRecipient(id)
}

// latestTurnResult is the newest turn result p posted to requester, "" if none: the only one requester can open.
func latestTurnResult(rs state.RunState, p, requester agentcomm.ParticipantID) agentcomm.MessageID {
	var latest *agentcomm.Message
	for _, m := range rs.Assistants.Mesh.Messages {
		if m.Sender == p && m.AutoResult && slices.Contains(m.Recipients, requester) && (latest == nil || m.Seq > latest.Seq) {
			latest = m
		}
	}
	if latest == nil {
		return ""
	}
	return latest.ID
}
