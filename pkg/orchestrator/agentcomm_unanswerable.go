package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// A wait for an answer must end once nobody left can send it. Admission already refuses such a wait
// (validateWaitAdmission); this is the same rule for a wait registered while its answerer could still reply,
// whose answerer then finished — a helper or run agent that failed or was cancelled, a stage that finished after
// reading the message without answering. Every pending reply_to or addressed_message wait whose possible
// answerers have all finished is cancelled, and its waiter resumed as "wait ended" (a parked stage continues
// through peerContinuation). The unread release (agentcomm_unread.go) covers messages a finished stage never
// read; this covers everyone else.

// errWaitStillAnswerable rejects a cancellation decided on a snapshot in which nobody could answer, when the
// commit's state says otherwise (the wait was answered or resolved meanwhile).
var errWaitStillAnswerable = errors.New("mesh: the wait can still be answered")

// releaseUnanswerableWaitsAfterFinish runs releaseUnanswerableWaits off the caller's goroutine: a finish may be
// committed on the event loop, which a stage resume publishes to. A bare test harness without a store or a
// concurrency manager has no mesh to release.
func (o *Orchestrator) releaseUnanswerableWaitsAfterFinish() {
	if o.opts.Store == nil || o.concurrency == nil {
		return
	}
	o.concurrency.SpawnDetached(o.runContext(context.Background()), func(ctx context.Context) {
		o.releaseUnanswerableWaits(ctx, o.resumeWaiterAfterTerminalWait)
	})
}

// releaseUnanswerableWaits cancels every pending wait nobody left can answer and wakes its waiter through wake.
func (o *Orchestrator) releaseUnanswerableWaits(ctx context.Context, wake func(context.Context, agentcomm.ParticipantID)) {
	rs := o.opts.Store.Snapshot()
	for _, id := range sortedWaitIDs(rs.Assistants.Mesh.Waits) {
		reason, ok := unanswerableWait(rs, id)
		if !ok {
			continue
		}
		if err := o.cancelUnanswerableWait(id, reason); err != nil {
			if !errors.Is(err, errWaitStillAnswerable) {
				log.Printf("WARN: mesh: release unanswerable wait %s: %v", id, err)
			}
			o.handleTaskOperationErr(err)
			continue
		}
		wake(ctx, rs.Assistants.Mesh.Waits[id].Waiter)
	}
}

// cancelUnanswerableWait commits waitID's cancellation, re-checking under the commit's lock that nobody can
// still answer it. Only the call whose commit wins returns nil, so a waiter is woken once.
func (o *Orchestrator) cancelUnanswerableWait(waitID agentcomm.WaitID, reason string) error {
	eff, err := marshalEffect("mesh_wait_resolved", state.Target{WaitID: string(waitID)},
		state.MeshWaitResolvedPayload{Status: string(agentcomm.WaitCancelled), Reason: reason})
	if err != nil {
		return err
	}
	op := state.Operation{
		ID:   "mesh-unanswerable:" + string(waitID) + ":" + randomOpSuffix(),
		Kind: "mesh_release_unanswerable", Actor: state.Actor{Kind: "system"}, Effects: []state.Effect{eff},
		Expect: state.Preconditions{Check: func(cur *state.RunState) error {
			if _, ok := unanswerableWait(*cur, waitID); !ok {
				return errWaitStillAnswerable
			}
			return nil
		}},
	}
	_, err = o.opts.Store.ApplyRunOperation(op, func(state.OperationResult) {
		o.publishNotice(bus.Event{Type: bus.EventMeshWait, Data: map[string]any{
			"wait_id": string(waitID), "status": string(agentcomm.WaitCancelled), "reason": reason}})
	})
	return err
}

// unanswerableWait reports whether waitID is a pending wait for an answer that nobody left can send — every
// participant who could send it (other than the waiter) has finished — and says why. Other kinds of waits end
// on their own: a task_result wait when its task finishes, a stage_status wait with its stage.
func unanswerableWait(rs state.RunState, waitID agentcomm.WaitID) (string, bool) {
	w := rs.Assistants.Mesh.Waits[waitID]
	if w == nil || w.Status != agentcomm.WaitPending {
		return "", false
	}
	if w.Condition.Kind != agentcomm.WaitOnReplyTo && w.Condition.Kind != agentcomm.WaitOnAddressedMessage {
		return "", false
	}
	var gone []string
	for _, p := range meshWaitTargets(rs, w.Condition) {
		if p == w.Waiter {
			continue
		}
		if canStillAnswer(rs, w.Condition, p) {
			return "", false
		}
		gone = append(gone, whyCannotAnswer(rs, p))
	}
	if len(gone) == 0 {
		return "", false
	}
	return "nobody left can answer: " + strings.Join(gone, "; "), true
}

// whyCannotAnswer names p and why it can no longer answer.
func whyCannotAnswer(rs state.RunState, p agentcomm.ParticipantID) string {
	participant, _ := resolveParticipant(rs, p)
	name := participant.DisplayName
	if name == "" {
		name = string(p)
	}
	if participant.UnreachableReason == "" {
		return fmt.Sprintf("%s gave the message up unread", name)
	}
	return fmt.Sprintf("%s — %s", name, participant.UnreachableReason)
}
