package state

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/akopichin/afm/pkg/assistants"
)

// ProposalCreatedPayload is the proposal_created control record's payload — decoded here by
// applyProposalCreated, encoded by pkg/orchestrator's proposalCreatedEffect (T6.4).
type ProposalCreatedPayload struct {
	TaskID                  string                     `json:"task_id,omitempty"`
	Type                    string                     `json:"type"`
	Title                   string                     `json:"title"`
	Rationale               string                     `json:"rationale,omitempty"`
	Payload                 json.RawMessage            `json:"payload,omitempty"`
	Authors                 []assistants.MessageAuthor `json:"authors,omitempty"`
	SourceMessageIDs        []string                   `json:"source_message_ids,omitempty"`
	SourceFindingIDs        []string                   `json:"source_finding_ids,omitempty"`
	ExpectedStageGeneration uint64                     `json:"expected_stage_generation"`
	ExpectedGraphRevision   string                     `json:"expected_graph_revision,omitempty"`
}

// ProposalStatusChangedPayload is the proposal_status_changed control record's payload — decoded here by
// applyProposalStatusChanged, encoded by pkg/orchestrator's proposalStatusEffect (T6.4/T6.5). OperationID is
// stamped onto the Proposal only on the FIRST transition into "accepted" — the natural idempotency anchor the
// delivery pipeline (T6.6) keys off.
type ProposalStatusChangedPayload struct {
	Status      string `json:"status"`
	Reason      string `json:"reason,omitempty"`
	Actor       string `json:"actor,omitempty"`
	OperationID string `json:"operation_id,omitempty"`
}

// ErrUnknownProposal — a control record addresses a proposal_id that does not exist in the projection.
var ErrUnknownProposal = errors.New("assistants: unknown proposal_id")

// ErrIllegalProposalTransition — a proposal_status_changed record asks for a transition
// assistants.Proposal.CanTransition rejects.
var ErrIllegalProposalTransition = errors.New("assistants: illegal proposal status transition")

func applyProposalCreated(as *AssistantsState, c *Control, dryRun bool) error {
	var p ProposalCreatedPayload
	if err := decodePayload(c.Payload, &p); err != nil {
		return fmt.Errorf("decode proposal_created payload: %w", err)
	}
	id := c.Target.ProposalID
	if id == "" {
		return errors.New("proposal_created: empty target proposal_id")
	}
	stageID := c.Target.StageID
	if stageID == "" {
		return errors.New("proposal_created: empty target stage_id")
	}
	if _, exists := as.Proposals.Proposals[id]; exists {
		return fmt.Errorf("proposal_created: id %s already exists", id)
	}
	if dryRun {
		return nil
	}

	prop := assistants.Proposal{
		ID:                      id,
		StageID:                 stageID,
		TaskID:                  p.TaskID,
		Type:                    assistants.ProposalType(p.Type),
		Title:                   p.Title,
		Rationale:               p.Rationale,
		Payload:                 p.Payload,
		Authors:                 p.Authors,
		SourceMessageIDs:        p.SourceMessageIDs,
		SourceFindingIDs:        p.SourceFindingIDs,
		ExpectedStageGeneration: p.ExpectedStageGeneration,
		ExpectedGraphRevision:   p.ExpectedGraphRevision,
		Status:                  assistants.ProposalPending,
		History:                 []assistants.ProposalDecision{{Status: assistants.ProposalPending, At: c.Time}},
		CreatedAt:               c.Time,
	}
	as.Proposals.Proposals[id] = &prop
	as.Proposals.ByStage[stageID] = append(as.Proposals.ByStage[stageID], id)
	return nil
}

func applyProposalStatusChanged(as *AssistantsState, c *Control, dryRun bool) error {
	id := c.Target.ProposalID
	p, ok := as.Proposals.Proposals[id]
	if !ok {
		return fmt.Errorf("proposal %s: %w", id, ErrUnknownProposal)
	}
	var payload ProposalStatusChangedPayload
	if err := decodePayload(c.Payload, &payload); err != nil {
		return fmt.Errorf("proposal %s: decode proposal_status_changed payload: %w", id, err)
	}
	to := assistants.ProposalStatus(payload.Status)
	if !p.CanTransition(to) {
		return fmt.Errorf("proposal %s: %w: %s -> %s", id, ErrIllegalProposalTransition, p.Status, to)
	}
	if dryRun {
		return nil
	}
	p.Status = to
	if to == assistants.ProposalAccepted && payload.OperationID != "" {
		p.OperationID = payload.OperationID
	}
	p.History = append(p.History, assistants.ProposalDecision{
		Status: to, Reason: payload.Reason, Actor: payload.Actor, At: c.Time,
	})
	return nil
}
