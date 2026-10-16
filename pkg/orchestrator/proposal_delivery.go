// Package orchestrator (this file, Этап 10/T10.1): delivery for the two Proposal types T6.4 modeled but left
// undelivered — assistants.ProposalAddStage and assistants.ProposalAttachAssistant (assistants.
// ProposalRequiredChange remains undelivered, per dev-plan.md's Этап 6 Review decisions; no test in Этап 10
// asks for it). Both deliveries reuse the EXACT SAME validated entry points the manual dashboard/side-agent
// paths already use — orchestrator.CommitAddStage (Этап 9) and Attach/admitChildren (Этап 3/4a) — never a
// parallel, hand-rolled copy of the DAG or delegation-depth rules (dev-plan's
// TestAction_AddStageDraftValidatedLikeManual / TestAction_InternalDelegationReusesStage4aChecks).
//
// Confirmation gate (§12.5): CreateProposal (proposal.go) NEVER applies anything — it only durably records a
// "pending" proposal. Only AcceptProposal, an explicit separate operation the dashboard calls after a human
// clicks Accept, ever reaches the code in this file (dev-plan's TestAction_AssistantCanProposeNotApplyGraphChange
// / TestConfirmation_RequiredForNewRootOrSiblingTask). Delivery here is synchronous (unlike feedback's
// deferred, stage-status-gated FeedbackIntent pipeline): a graph/task mutation has no "wait for the stage to
// become interruptible" phase, it either commits now or is rejected now. Known gap, recorded in dev-plan.md's
// Review decisions: a crash between markProposalAccepted and the delivery call below leaves the proposal
// stuck "accepted" with no automatic retry (feedback's recoverFeedbackIntents has no counterpart for these
// two types) — a human re-driving the same proposal id would see ErrOperationIDConflict-style idempotent
// behavior resolve it, but nothing retries automatically today.
package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// AttachAssistantProposalPayload is assistants.ProposalAttachAssistant's Payload shape (T10.1). Proposal.TaskID
// is the admission-path selector: empty means a new TOP-LEVEL task on Proposal.StageID (delivered through
// Attach, Этап 3); non-empty means a CHILD of that existing, still-active task, delivered through
// admitChildren — the SAME admission gate Этап 4a's internal delegation already uses, never a second,
// hand-rolled check (TestAction_InternalDelegationReusesStage4aChecks). No field here can ever widen depth,
// pick a different root stage, or reserve a separate budget — those are never caller-supplied in either
// delivery path (T10.2, "no self elevation").
type AttachAssistantProposalPayload struct {
	ProfileID   string `json:"profile_id"`
	Title       string `json:"title"`
	Instruction string `json:"instruction"`
	Mode        string `json:"mode,omitempty"`
	Trigger     string `json:"trigger,omitempty"`
}

// markProposalAccepted durably commits p's pending->accepted transition, guarded by the SAME "still pending"
// CAS precondition the feedback path uses — requestID doubles as this transition's idempotency key (a
// repeated AcceptProposal call with the SAME requestID replays this step instead of re-validating it).
func (o *Orchestrator) markProposalAccepted(p assistants.Proposal, requestID string, actor state.Actor) error {
	statusEff, err := proposalStatusEffect(p.ID, state.ProposalStatusChangedPayload{
		Status: string(assistants.ProposalAccepted), Actor: string(actor.Kind), OperationID: requestID,
	})
	if err != nil {
		return err
	}
	op := state.Operation{
		ID: requestID, Kind: "accept_proposal", Actor: actor,
		Expect:  state.Preconditions{ProposalStatus: map[string]assistants.ProposalStatus{p.ID: assistants.ProposalPending}},
		Effects: []state.Effect{statusEff},
	}
	_, err = o.opts.Store.ApplyRunOperation(op, func(state.OperationResult) {
		o.publishNotice(bus.Event{Type: bus.EventProposalStatus, StageID: p.StageID, Data: map[string]any{
			"proposal_id": p.ID, "status": string(assistants.ProposalAccepted),
		}})
	})
	return err
}

// deliverOperationID derives the delivery step's own idempotency key from the accept call's requestID — kept
// DISTINCT from requestID itself (used bare by markProposalAccepted above) so the two durable writes never
// collide in ApplyRunOperation's single operation-id index (each id must map to exactly one set of Effects).
func deliverOperationID(requestID string) string {
	return requestID + ":deliver"
}

// acceptAddStageProposal delivers an accepted assistants.ProposalAddStage: p.Payload decodes DIRECTLY into
// AddStageDraft (the same shape PreviewAddStage/CommitAddStage already validate against for the manual
// dialog and the side-agent draft-confirm path) — CommitAddStage is called VERBATIM, not re-implemented.
func (o *Orchestrator) acceptAddStageProposal(ctx context.Context, p assistants.Proposal, requestID string, actor state.Actor) error {
	if len(p.Payload) == 0 {
		return errors.New("assistants: add_stage proposal has no payload")
	}
	var draft AddStageDraft
	if err := json.Unmarshal(p.Payload, &draft); err != nil {
		return fmt.Errorf("assistants: decode add_stage proposal payload: %w", err)
	}
	draft.RequestID = deliverOperationID(requestID) // delivery's own idempotency key, never the stored payload's

	if err := o.markProposalAccepted(p, requestID, actor); err != nil {
		return err
	}
	if _, err := o.CommitAddStage(ctx, draft); err != nil {
		o.commitProposalStatus(p.ID, p.StageID, assistants.ProposalAccepted, assistants.ProposalDeliveryFailed, err.Error())
		return err
	}
	o.advanceProposalToDelivered(p.ID)
	return nil
}

// acceptAttachAssistantProposal delivers an accepted assistants.ProposalAttachAssistant: a new top-level task
// (Proposal.TaskID empty, via Attach) or a child of an existing task (Proposal.TaskID set, via admitChildren —
// Этап 4a's internal-delegation admission gate, reused verbatim). parentToStatus/continuation are both
// skipped in the child case: this is an explicit, already-confirmed one-off child create, exactly the
// "future caller" admitChildren's own doc comment (assistants_delegate.go) anticipates — the parent is not
// itself pausing to wait for it.
func (o *Orchestrator) acceptAttachAssistantProposal(ctx context.Context, p assistants.Proposal, requestID string, actor state.Actor) error {
	if len(p.Payload) == 0 {
		return errors.New("assistants: attach_assistant proposal has no payload")
	}
	var payload AttachAssistantProposalPayload
	if err := json.Unmarshal(p.Payload, &payload); err != nil {
		return fmt.Errorf("assistants: decode attach_assistant proposal payload: %w", err)
	}

	if err := o.markProposalAccepted(p, requestID, actor); err != nil {
		return err
	}

	deliverID := deliverOperationID(requestID)
	var deliverErr error
	if p.TaskID == "" {
		_, deliverErr = o.Attach(ctx, AttachRequest{
			OperationID: deliverID, StageID: p.StageID, ProfileID: assistants.ProfileID(payload.ProfileID),
			Title: payload.Title, Instruction: payload.Instruction,
			Mode: assistants.Mode(payload.Mode), Trigger: assistants.Trigger(payload.Trigger),
			Actor: actor,
		})
	} else {
		children := []assistants.DelegateRequest{{
			ProfileID: assistants.ProfileID(payload.ProfileID), Title: payload.Title, Instruction: payload.Instruction,
			Mode: assistants.Mode(payload.Mode), Trigger: assistants.Trigger(payload.Trigger),
		}}
		_, deliverErr = o.admitChildren(ctx, deliverID, assistants.TaskID(p.TaskID), children, actor, "", nil)
	}
	if deliverErr != nil {
		o.commitProposalStatus(p.ID, p.StageID, assistants.ProposalAccepted, assistants.ProposalDeliveryFailed, deliverErr.Error())
		return deliverErr
	}
	o.advanceProposalToDelivered(p.ID)
	return nil
}
