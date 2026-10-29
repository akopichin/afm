// Package orchestrator (this file, T6.5/T6.6): FeedbackIntent — the durable, idempotent pipeline that
// delivers an ACCEPTED proposal (or, in principle, any durably-recorded feedback) to the stage's real
// execution path EXACTLY ONCE, through the SAME Revise mechanics control_api.go already uses for manual
// notes/buttons (reviseRunning/reviseAwaitingApproval) — never a second, parallel restart path (§10.1).
//
// Delivery order (§10.3, T6.6): one operation durably commits proposal "accepted" + the FeedbackIntent ->
// the dispatcher (attemptFeedbackDelivery) re-validates the stage's CURRENT status/generation -> for an
// immediately-interruptible stage, the SAME EvRevise + feedback-file + interrupt/spawn mechanics run, using
// state.SaveFeedbackOnce (keyed by the intent id) so a crash/retry/duplicate delivery attempt never appends
// the feedback twice -> only on definite success is the intent marked "delivered" (a separate, later,
// durable commit). A crash between any of these steps leaves the intent "accepted" — recoverFeedbackIntents
// (called once at startup) and the per-transition hook in orchestrator.go's triggerWithSeq both re-attempt
// delivery, and every step along the way is itself idempotent, so re-running from scratch is always safe.
package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// FeedbackProposalPayload is the Proposal.Payload shape for assistants.ProposalFeedback — the only Proposal
// type Этап 6 delivers end-to-end.
type FeedbackProposalPayload struct {
	Feedback string `json:"feedback"`
}

// deriveFeedbackIntentID computes a deterministic, path-safe intent id from an AcceptProposal operation id.
func deriveFeedbackIntentID(operationID string) string {
	sum := sha256.Sum256([]byte("feedback-intent|" + operationID))
	return "fi-" + hex.EncodeToString(sum[:])[:20]
}

func feedbackIntentCreatedEffect(stageID, intentID string, p state.FeedbackIntentCreatedPayload) (state.Effect, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return state.Effect{}, fmt.Errorf("marshal feedback_intent_created payload: %w", err)
	}
	return state.Effect{Kind: "feedback_intent_created", Target: state.Target{StageID: stageID, IntentID: intentID}, Payload: raw}, nil
}

func feedbackIntentConsumedEffect(intentID string, p state.FeedbackIntentConsumedPayload) (state.Effect, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return state.Effect{}, fmt.Errorf("marshal feedback_intent_consumed payload: %w", err)
	}
	return state.Effect{Kind: "feedback_intent_consumed", Target: state.Target{IntentID: intentID}, Payload: raw}, nil
}

// ErrProposalTypeNotDeliverable — Этап 10 wires end-to-end delivery for assistants.ProposalAddStage/
// ProposalAttachAssistant too (proposal_delivery.go), joining ProposalFeedback; only
// assistants.ProposalRequiredChange (the advisory/required mode flip) still has no delivery path and
// AcceptProposal rejects it rather than silently doing nothing — see proposal.go's package doc comment and
// dev-plan.md's Review decisions for Этап 6/10.
var ErrProposalTypeNotDeliverable = errors.New("assistants: this proposal type has no delivery path yet")

// AcceptProposal is the ONLY path that turns a human's "yes" on a proposal into something that reaches the
// main agent (ProposalFeedback), the task tree (ProposalAttachAssistant, Этап 10/T10.1), or the flow graph
// (ProposalAddStage, Этап 10/T10.1). requestID is the idempotency key for the WHOLE accept+delivery step
// (T6.6): the SAME requestID repeated (double-click, retry-after-timeout, a second browser tab racing a
// slightly different requestID) either replays the identical first outcome or is cleanly rejected — never
// double-delivers. assistants.ProposalRequiredChange has no delivery path yet (Этап 6/Этап 10 Review
// decisions) — ErrProposalTypeNotDeliverable for that one, same as before Этап 10.
func (o *Orchestrator) AcceptProposal(ctx context.Context, proposalID, requestID string, actor state.Actor) error {
	if !o.opts.AssistantsEnabled {
		return &state.OperationRejectedError{Code: assistants.CodeFeatureDisabled, Reason: "collaboration is disabled"}
	}
	if requestID == "" {
		return errors.New("assistants: request id is required")
	}
	if err := rejectReservedRequestID(requestID, actor); err != nil {
		return err
	}
	p, ok := o.Proposal(proposalID)
	if !ok {
		return ErrProposalNotFound
	}
	switch p.Type {
	case assistants.ProposalFeedback:
		return o.acceptFeedbackProposal(ctx, p, requestID, actor)
	case assistants.ProposalAddStage:
		return o.acceptAddStageProposal(ctx, p, requestID, actor)
	case assistants.ProposalAttachAssistant:
		return o.acceptAttachAssistantProposal(ctx, p, requestID, actor)
	default:
		return ErrProposalTypeNotDeliverable
	}
}

// acceptFeedbackProposal is AcceptProposal's original ProposalFeedback-only body (Этап 6), factored out
// unchanged so AcceptProposal itself can dispatch on p.Type (Этап 10).
func (o *Orchestrator) acceptFeedbackProposal(ctx context.Context, p assistants.Proposal, requestID string, actor state.Actor) error {
	proposalID := p.ID
	var payload FeedbackProposalPayload
	if len(p.Payload) > 0 {
		if err := json.Unmarshal(p.Payload, &payload); err != nil {
			return fmt.Errorf("assistants: decode proposal payload: %w", err)
		}
	}

	currentGen := o.opts.Store.StageGeneration(p.StageID)
	if err := p.CheckGeneration(currentGen); err != nil {
		return &state.OperationRejectedError{Code: assistants.CodeStaleRevision, Reason: err.Error()}
	}

	intentID := deriveFeedbackIntentID(requestID)
	statusEff, err := proposalStatusEffect(proposalID, state.ProposalStatusChangedPayload{
		Status: string(assistants.ProposalAccepted), Actor: string(actor.Kind), OperationID: requestID,
	})
	if err != nil {
		return err
	}
	participants := proposalParticipants(p)
	intentEff, err := feedbackIntentCreatedEffect(p.StageID, intentID, state.FeedbackIntentCreatedPayload{
		StageID: p.StageID, Feedback: payload.Feedback, Origin: string(state.FeedbackOriginProposal),
		ProposalID: proposalID, Participants: participants, ExpectedStageGeneration: p.ExpectedStageGeneration,
	})
	if err != nil {
		return err
	}

	op := state.Operation{
		ID: requestID, Kind: "accept_proposal", Actor: actor,
		Expect: state.Preconditions{
			ProposalStatus:  map[string]assistants.ProposalStatus{proposalID: assistants.ProposalPending},
			StageGeneration: map[string]uint64{p.StageID: p.ExpectedStageGeneration},
		},
		Effects: []state.Effect{statusEff, intentEff},
	}
	runCtx := o.runContext(ctx) // Инвариант №6: доставка не должна зависеть от HTTP request ctx.
	_, err = o.opts.Store.ApplyRunOperation(op, func(state.OperationResult) {
		o.publishNotice(bus.Event{Type: bus.EventProposalStatus, StageID: p.StageID, Data: map[string]any{
			"proposal_id": proposalID, "status": string(assistants.ProposalAccepted),
		}})
		o.attemptFeedbackDelivery(runCtx, intentID)
	})
	return err
}

// proposalParticipants names every real author of p for feed-provenance purposes.
func proposalParticipants(p assistants.Proposal) []string {
	out := make([]string, 0, len(p.Authors))
	for _, a := range p.Authors {
		if a.ID != "" {
			out = append(out, a.ID)
		}
	}
	return out
}

// renderForwardedFeedback wraps a proposal-originated feedback body with an honest, visible provenance header
// — the agent (and the feed, T6.5's TestFeedUI_ShowsUserForwardedProposal) must never see this as if the
// human typed it directly.
func renderForwardedFeedback(fi *state.FeedbackIntent) string {
	if fi.Origin != state.FeedbackOriginProposal {
		return fi.Feedback
	}
	who := "the assistant team"
	if len(fi.Participants) > 0 {
		names := make([]string, len(fi.Participants))
		for i, p := range fi.Participants {
			names[i] = "@" + p
		}
		who = fmt.Sprintf("%v", names)
	}
	return fmt.Sprintf("## User forwarded a proposal from %s (accepted, not authored by the user)\n\n%s", who, fi.Feedback)
}

// attemptFeedbackDelivery is the ONE dispatcher for a single intent: safe to call any number of times
// (idempotent retry after a crash, a deferred retry once the stage's status changes, a duplicate wake-up) —
// each internal step either no-ops against durable state it has already committed, or genuinely makes
// progress. Called from AcceptProposal's externalWork, recoverFeedbackIntents (startup), and the
// per-transition hook (orchestrator.go's triggerWithSeq) for deferred intents.
func (o *Orchestrator) attemptFeedbackDelivery(ctx context.Context, intentID string) {
	// Every call first leaves a wake-up, then tries to become the one deliverer of this intent. Reentrancy
	// guard (see the field's doc comment, orchestrator.go): delivering to a running stage triggers a NESTED
	// transition whose own hook calls this again for the same intent — that call, like any other one arriving
	// while a delivery is in flight (e.g. Continue right after a Pause beat our Revise), only leaves its
	// wake-up. The deliverer keeps deciding against the CURRENT status while wake-ups arrive, and checks for
	// one once more after releasing the guard, so a status change can never be lost between the two.
	o.feedbackWakeups.Store(intentID, struct{}{})
	for {
		if _, busy := o.inFlightFeedbackIntents.LoadOrStore(intentID, struct{}{}); busy {
			return // the deliverer in flight will see our wake-up
		}
		for {
			if _, woke := o.feedbackWakeups.LoadAndDelete(intentID); !woke {
				break
			}
			if o.deliverFeedbackIntentOnce(ctx, intentID) {
				// Our Revise applied nothing: a transition beat it, so decide again against the new status.
				o.feedbackWakeups.Store(intentID, struct{}{})
			}
		}
		o.inFlightFeedbackIntents.Delete(intentID)
		if _, woke := o.feedbackWakeups.Load(intentID); !woke {
			return
		}
	}
}

// deliverFeedbackIntentOnce makes one delivery decision for the intent against the stage's current status.
// It reports whether the Revise it chose applied nothing (a transition beat it), so the caller decides again.
func (o *Orchestrator) deliverFeedbackIntentOnce(ctx context.Context, intentID string) (raced bool) {
	rs := o.opts.Store.Snapshot()
	fi, ok := rs.Assistants.FeedbackIntents[intentID]
	if !ok || fi.Status != state.FeedbackIntentAccepted {
		return false // already consumed (delivered/rejected) by a concurrent attempt, or genuinely unknown
	}

	// Re-validate generation IMMEDIATELY before acting — TestDeferredIntent_RevalidatedBeforeExecution: a
	// deferred intent must not apply its advice to a DIFFERENT attempt than the one it was computed against,
	// even if that check already passed once at accept time. EXCEPTION: if the stage's generation moved on
	// because resuming THIS VERY intent's own (crash-interrupted) delivery attempt caused it — reviseRunning/
	// reviseAwaitingApproval stamp fi.ID into the EvRevise reason below — that is not staleness, it is this
	// delivery attempt continuing after a restart (TestCrashAfterTransitionBeforeFeedbackFile).
	currentGen := o.opts.Store.StageGeneration(fi.StageID)
	ownResume := strings.Contains(o.opts.Store.LastReviseReason(fi.StageID), fi.ID)
	if currentGen != fi.ExpectedStageGeneration && !ownResume {
		o.rejectFeedbackIntent(fi, "stale: stage generation changed since this intent was accepted")
		return false
	}

	plan := feedbackDeliveryPlanForStatus(o.currentStatus(fi.StageID))
	if o.feedbackDeliveryDecidedHook != nil {
		o.feedbackDeliveryDecidedHook(fi.StageID)
	}
	switch plan {
	case deliverInterruptRunning:
		body := renderForwardedFeedback(fi)
		applied, _, err := o.reviseRunning(fi.StageID, "feedback intent "+fi.ID, func(stageDir string) error {
			_, err := state.SaveFeedbackOnce(stageDir, fi.ID, body)
			return err
		})
		if err != nil {
			log.Printf("feedback intent %s: deliver (running) failed: %v", fi.ID, err)
			return false
		}
		if !applied {
			return true // lost the race: never mark delivered what was not delivered
		}
		o.finishFeedbackDelivery(fi)
	case deliverAwaitingApproval:
		body := renderForwardedFeedback(fi)
		applied, _, err := o.reviseAwaitingApproval(ctx, fi.StageID, "feedback intent "+fi.ID, func(stageDir string) error {
			_, err := state.SaveFeedbackOnce(stageDir, fi.ID, body)
			return err
		})
		if err != nil {
			log.Printf("feedback intent %s: deliver (awaiting_approval) failed: %v", fi.ID, err)
			return false
		}
		if !applied {
			return true // lost the race: never mark delivered what was not delivered
		}
		o.finishFeedbackDelivery(fi)
	case deliverStartWithFeedback:
		body := renderForwardedFeedback(fi)
		applied, _, err := o.reviseAwaitingAssistants(ctx, fi.StageID, "feedback intent "+fi.ID, func(stageDir string) error {
			_, err := state.SaveFeedbackOnce(stageDir, fi.ID, body)
			return err
		})
		if err != nil {
			log.Printf("feedback intent %s: deliver (awaiting_assistants) failed: %v", fi.ID, err)
			return false
		}
		if !applied {
			return true // lost the race: never mark delivered what was not delivered
		}
		o.finishFeedbackDelivery(fi)
	case deliverPreNote:
		stageDir := o.stageDirFor(fi.StageID)
		body := renderForwardedFeedback(fi)
		if err := state.SavePreNote(stageDir, body); err != nil {
			log.Printf("feedback intent %s: save pre-note failed: %v", fi.ID, err)
			return false
		}
		// A stage that started between the status read above and this write would have its prenote.md
		// consumed differently (prenote only applies on fresh start) — re-check and fall through to the
		// interruptible path if it is now live, rather than leaving a note that may never be read.
		if o.currentStatus(fi.StageID) != state.StatusPending {
			return false // next transition (or the caller's own retry) re-dispatches against the new status
		}
		o.finishFeedbackDelivery(fi)
	case deliverRejectTerminal:
		o.rejectFeedbackIntent(fi, "stage already reached a terminal status; an explicit Retry or a new stage is required")
	default: // deliverDefer
		// Nothing to do right now — the parent is mid-attempt (planning/revising/retrying) or waiting on the
		// user (paused/awaiting_user_input/hook_failed). Stays "accepted"; re-dispatched by the next relevant
		// stage transition or by recoverFeedbackIntents after a restart.
	}
	return false
}

// finishFeedbackDelivery durably marks fi delivered and publishes the feed-facing notice. Called ONLY after
// the feedback has actually been durably projected into the stage's real execution path.
func (o *Orchestrator) finishFeedbackDelivery(fi *state.FeedbackIntent) {
	o.consumeFeedbackIntent(fi.ID, string(state.FeedbackIntentDelivered), "")
	data := map[string]any{"stage_id": fi.StageID, "origin": string(fi.Origin)}
	if fi.Origin == state.FeedbackOriginProposal {
		data["proposal_id"] = fi.ProposalID
		data["participants"] = fi.Participants
	}
	o.publishNotice(bus.Event{Type: bus.EventFeedbackDelivered, StageID: fi.StageID, Data: data})
	if fi.Origin != state.FeedbackOriginProposal {
		return
	}
	// Promote the proposal itself to its terminal "delivered" status — queued is an internal bookkeeping step
	// (see proposalTransitions), committed here in the SAME breath as finishing the intent so the two can
	// never drift apart for long.
	o.advanceProposalToDelivered(fi.ProposalID)
}

// rejectFeedbackIntent durably marks fi rejected (never delivered) and, if it originated from a proposal,
// advances that proposal straight to delivery_failed with the same reason — a terminal/stale intent must
// never leave its proposal stuck looking "accepted" forever.
func (o *Orchestrator) rejectFeedbackIntent(fi *state.FeedbackIntent, reason string) {
	o.consumeFeedbackIntent(fi.ID, string(state.FeedbackIntentRejected), reason)
	if fi.Origin != state.FeedbackOriginProposal {
		return
	}
	o.commitProposalStatus(fi.ProposalID, fi.StageID, assistants.ProposalAccepted, assistants.ProposalDeliveryFailed, reason)
}

// advanceProposalToDelivered walks a proposal from accepted -> queued -> delivered. Safe to call
// unconditionally/repeatedly: each transition's precondition makes a redundant call a silent no-op once the
// proposal has already moved on.
func (o *Orchestrator) advanceProposalToDelivered(proposalID string) {
	p, ok := o.Proposal(proposalID)
	if !ok {
		return
	}
	if p.Status == assistants.ProposalAccepted {
		o.commitProposalStatus(proposalID, p.StageID, assistants.ProposalAccepted, assistants.ProposalQueued, "")
		p.Status = assistants.ProposalQueued
	}
	if p.Status == assistants.ProposalQueued {
		o.commitProposalStatus(proposalID, p.StageID, assistants.ProposalQueued, assistants.ProposalDelivered, "")
	}
}

// commitProposalStatus durably commits one proposal_status_changed transition, guarded by a from-status
// precondition — a lost race (someone else already advanced it) is a silent, benign no-op. stageID is only
// for the live notice's routing (the durable record itself does not need it — Target.ProposalID is enough to
// look the proposal back up).
func (o *Orchestrator) commitProposalStatus(proposalID, stageID string, from, to assistants.ProposalStatus, reason string) {
	eff, err := proposalStatusEffect(proposalID, state.ProposalStatusChangedPayload{Status: string(to), Reason: reason})
	if err != nil {
		return
	}
	op := state.Operation{
		ID:      "proposal-status:" + proposalID + ":" + string(to) + ":" + randomOpSuffix(),
		Kind:    "proposal_status",
		Expect:  state.Preconditions{ProposalStatus: map[string]assistants.ProposalStatus{proposalID: from}},
		Effects: []state.Effect{eff},
	}
	if _, err := o.opts.Store.ApplyRunOperation(op, func(state.OperationResult) {
		o.publishNotice(bus.Event{Type: bus.EventProposalStatus, StageID: stageID, Data: map[string]any{
			"proposal_id": proposalID, "status": string(to),
		}})
	}); err != nil {
		o.handleTaskOperationErr(err)
	}
}

// consumeFeedbackIntent durably commits the intent's terminal outcome. Guarded by the projection's own
// "already consumed" check (applyFeedbackIntentConsumed) — a lost race (two concurrent dispatch attempts) is
// a silent, benign no-op for the loser (TestFeedbackNotAppendedTwice's durable half).
func (o *Orchestrator) consumeFeedbackIntent(intentID, status, reason string) {
	eff, err := feedbackIntentConsumedEffect(intentID, state.FeedbackIntentConsumedPayload{Status: status, Reason: reason})
	if err != nil {
		return
	}
	op := state.Operation{ID: "intent-consume:" + intentID + ":" + randomOpSuffix(), Kind: "consume_feedback_intent", Effects: []state.Effect{eff}}
	if _, err := o.opts.Store.ApplyRunOperation(op, nil); err != nil {
		o.handleTaskOperationErr(err)
	}
}

// feedbackDeliveryPlan classifies what attemptFeedbackDelivery should do for the stage's CURRENT status
// (§10.2's per-status table, T6.5).
type feedbackDeliveryPlan int

const (
	deliverInterruptRunning feedbackDeliveryPlan = iota
	deliverAwaitingApproval
	deliverPreNote
	deliverDefer
	deliverRejectTerminal
	// deliverStartWithFeedback (Этап 7, T7.8) — awaiting_assistants: the main executor's own process has
	// ALREADY exited, there is nothing to interrupt. Delivery here is an explicit "start a fresh execution
	// pass with this feedback" action (reviseAwaitingAssistants, control_api.go), not reviseRunning's
	// interrupt-signal mechanics.
	deliverStartWithFeedback
)

// feedbackDeliveryPlanForStatus implements dev-plan.md's T6.5 per-parent-status table — a pure function, one
// test per row.
func feedbackDeliveryPlanForStatus(status state.StageStatus) feedbackDeliveryPlan {
	switch status {
	case state.StatusRunning:
		return deliverInterruptRunning
	case state.StatusAwaitingApproval:
		return deliverAwaitingApproval
	case state.StatusPending:
		return deliverPreNote
	case state.StatusAwaitingAssistants:
		return deliverStartWithFeedback
	case state.StatusDone, state.StatusFailed:
		return deliverRejectTerminal
	default:
		// planning/revising/retrying/paused/awaiting_user_input/hook_failed all defer: stay "accepted",
		// retried by the next relevant stage transition or by recoverFeedbackIntents after a restart.
		return deliverDefer
	}
}

// redeliverPendingFeedbackIntents re-attempts delivery of every still-"accepted" FeedbackIntent targeting
// stageID — called from the single FSM-transition funnel (orchestrator.go's triggerWithSeq) any time stageID
// settles into a new status, so a deferred intent (planning/revising/retrying/paused/awaiting_user_input/
// hook_failed) is retried the moment the stage becomes interruptible (or reaches a terminal status) again,
// without a separate poller.
func (o *Orchestrator) redeliverPendingFeedbackIntents(ctx context.Context, stageID string) {
	rs := o.opts.Store.Snapshot()
	var ids []string
	for id, fi := range rs.Assistants.FeedbackIntents {
		if fi.StageID == stageID && fi.Status == state.FeedbackIntentAccepted {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids) // deterministic order; not a correctness requirement
	for _, id := range ids {
		o.attemptFeedbackDelivery(ctx, id)
	}
}

// recoverFeedbackIntents re-attempts delivery of EVERY still-"accepted" FeedbackIntent across the whole run,
// once, at startup (T6.6: "после краша восстановление достраивает недостающие проекции") — the crash-recovery
// counterpart to redeliverPendingFeedbackIntents's live per-transition hook.
func (o *Orchestrator) recoverFeedbackIntents(ctx context.Context) {
	if !o.opts.AssistantsEnabled {
		return
	}
	rs := o.opts.Store.Snapshot()
	ids := make([]string, 0, len(rs.Assistants.FeedbackIntents))
	for id, fi := range rs.Assistants.FeedbackIntents {
		if fi.Status == state.FeedbackIntentAccepted {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	for _, id := range ids {
		o.attemptFeedbackDelivery(ctx, id)
	}
}
