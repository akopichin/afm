// Package orchestrator (this file, T6.4/T6.7): the Proposal board. CreateProposal is the durable commit a
// bounded synthesizer run (T6.3) is meant to call once it produces a real Proposal (wiring an actual
// synthesizer AGENT invocation is out of Этап 6's scope — see dev-plan.md's Review decisions; this entry
// point is what that future wiring, and today's tests, drive). DismissProposal/Proposals are the read/dismiss
// halves of the HTTP API (T6.7). Accepting a proposal — the one path that actually touches the main agent —
// lives in feedback_intent.go, never here.
package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// deriveProposalID computes a deterministic, path-safe proposal id from a CreateProposal operation id —
// mirrors deriveTaskID/deriveMessageID.
func deriveProposalID(operationID string) string {
	sum := sha256.Sum256([]byte("proposal|" + operationID))
	return "prop-" + hex.EncodeToString(sum[:])[:20]
}

// CreateProposalRequest is everything needed to durably record one synthesized Proposal.
type CreateProposalRequest struct {
	OperationID             string
	StageID                 string
	TaskID                  string
	Type                    assistants.ProposalType
	Title                   string
	Rationale               string
	Payload                 json.RawMessage
	Authors                 []assistants.MessageAuthor
	SourceMessageIDs        []string
	SourceFindingIDs        []string
	ExpectedStageGeneration uint64
	Actor                   state.Actor
}

func proposalCreatedEffect(stageID, proposalID string, p state.ProposalCreatedPayload) (state.Effect, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return state.Effect{}, fmt.Errorf("marshal proposal_created payload: %w", err)
	}
	return state.Effect{Kind: "proposal_created", Target: state.Target{StageID: stageID, ProposalID: proposalID}, Payload: raw}, nil
}

func proposalStatusEffect(proposalID string, p state.ProposalStatusChangedPayload) (state.Effect, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return state.Effect{}, fmt.Errorf("marshal proposal_status_changed payload: %w", err)
	}
	return state.Effect{Kind: "proposal_status_changed", Target: state.Target{ProposalID: proposalID}, Payload: raw}, nil
}

// maxPendingSelfProposals caps how many UNCONFIRMED ("pending") proposals authored (at least in part) by an
// assistant can be outstanding across the WHOLE run at once (Этап 10, T10.4: "собственные предложения
// помощников не порождают лавину задач"). dev-plan.md names no exact number for this cap — 5 is a judgment
// call (same order of magnitude as assistants.MaxDelegateChildren's own per-group cap), recorded in
// dev-plan.md's Review decisions: small enough to keep one human's confirmation queue manageable, not so
// tight that a legitimate multi-step improvement plan cannot even be proposed.
const maxPendingSelfProposals = 5

// isSelfSourced reports whether at least one of authors is the assistant team itself — a proposal with NO
// assistant author (e.g. one a human drafted directly, should that ever exist) is never counted against the
// avalanche cap below.
func isSelfSourced(authors []assistants.MessageAuthor) bool {
	for _, a := range authors {
		if a.Kind == assistants.AuthorAssistant {
			return true
		}
	}
	return false
}

// pendingSelfProposalCount counts currently-"pending" (unconfirmed) proposals authored at least in part by an
// assistant, across every stage of the run.
func (o *Orchestrator) pendingSelfProposalCount() int {
	rs := o.opts.Store.Snapshot()
	n := 0
	for _, p := range rs.Assistants.Proposals.Proposals {
		if p != nil && p.Status == assistants.ProposalPending && isSelfSourced(p.Authors) {
			n++
		}
	}
	return n
}

// CreateProposal durably commits one new Proposal in "pending" status. OperationID is the idempotency key —
// the SAME OperationID always resolves to the SAME proposal id.
func (o *Orchestrator) CreateProposal(ctx context.Context, req CreateProposalRequest) (string, error) {
	if !o.opts.AssistantsEnabled {
		return "", &state.OperationRejectedError{Code: assistants.CodeFeatureDisabled, Reason: "collaboration is disabled"}
	}
	if req.OperationID == "" {
		return "", errors.New("assistants: operation id is required")
	}
	if err := rejectReservedRequestID(req.OperationID, req.Actor); err != nil {
		return "", err
	}
	if o.reg().Stage(req.StageID) == nil {
		return "", fmt.Errorf("assistants: unknown stage %q", req.StageID)
	}

	proposalID := deriveProposalID(req.OperationID)
	// Avalanche cap (T10.4): checked only for a GENUINELY new proposal — an idempotent replay of an already-
	// committed OperationID must keep resolving to its own prior outcome regardless of how the cap has since
	// filled up (ApplyRunOperation's own operation-id index is what actually makes a replay a no-op; this
	// check runs before that call and would otherwise spuriously reject a legitimate repeat right at the cap).
	if _, exists := o.Proposal(proposalID); !exists && isSelfSourced(req.Authors) {
		if n := o.pendingSelfProposalCount(); n >= maxPendingSelfProposals {
			return "", &state.OperationRejectedError{Code: assistants.CodeBudgetExceeded, Reason: fmt.Sprintf("%d pending self-proposals already outstanding (max %d)", n, maxPendingSelfProposals)}
		}
	}
	// ThreadID (T6.6): the mesh thread req.SourceMessageIDs are grounded in. Every proposal targets exactly
	// one stage, and that stage's discussion thread id is deterministic (deriveDiscussionThreadID,
	// agentcomm_discussion.go) — computing it here never forces the thread into existence; a proposal for a
	// stage with no discussion yet still gets a stable, correct id it can reference once one starts.
	threadID := deriveDiscussionThreadID(req.StageID)
	eff, err := proposalCreatedEffect(req.StageID, proposalID, state.ProposalCreatedPayload{
		TaskID: req.TaskID, Type: string(req.Type), Title: req.Title, Rationale: req.Rationale,
		Payload: req.Payload, Authors: req.Authors, SourceMessageIDs: req.SourceMessageIDs,
		SourceFindingIDs: req.SourceFindingIDs, ThreadID: string(threadID),
		ExpectedStageGeneration: req.ExpectedStageGeneration,
	})
	if err != nil {
		return "", err
	}

	op := state.Operation{ID: req.OperationID, Kind: "create_proposal", Actor: req.Actor, Effects: []state.Effect{eff}}
	_, err = o.opts.Store.ApplyRunOperation(op, func(state.OperationResult) {
		o.publishNotice(bus.Event{Type: bus.EventProposalCreated, StageID: req.StageID, Data: map[string]any{
			"proposal_id": proposalID, "type": string(req.Type), "title": req.Title,
		}})
	})
	if err != nil {
		return "", err
	}
	return proposalID, nil
}

// Proposals returns stageID's proposals in commit order.
func (o *Orchestrator) Proposals(stageID string) []assistants.Proposal {
	rs := o.opts.Store.Snapshot()
	ids := rs.Assistants.Proposals.ByStage[stageID]
	out := make([]assistants.Proposal, 0, len(ids))
	for _, id := range ids {
		if p, ok := rs.Assistants.Proposals.Proposals[id]; ok && p != nil {
			out = append(out, *p)
		}
	}
	return out
}

// Proposal returns one proposal by id.
func (o *Orchestrator) Proposal(id string) (assistants.Proposal, bool) {
	rs := o.opts.Store.Snapshot()
	p, ok := rs.Assistants.Proposals.Proposals[id]
	if !ok || p == nil {
		return assistants.Proposal{}, false
	}
	return *p, true
}

// ErrProposalNotFound is returned by DismissProposal/AcceptProposal for an unknown proposal id.
var ErrProposalNotFound = errors.New("assistants: proposal not found")

// DismissProposal durably marks a pending proposal dismissed. Dismissal never touches the main agent in any
// way (TestProposal_DismissedDoesNotAffectMainAgent) — it is a pure proposal_status_changed commit.
func (o *Orchestrator) DismissProposal(ctx context.Context, proposalID, requestID, reason string, actor state.Actor) error {
	if !o.opts.AssistantsEnabled {
		return &state.OperationRejectedError{Code: assistants.CodeFeatureDisabled, Reason: "collaboration is disabled"}
	}
	if err := rejectReservedRequestID(requestID, actor); err != nil {
		return err
	}
	p, ok := o.Proposal(proposalID)
	if !ok {
		return ErrProposalNotFound
	}
	eff, err := proposalStatusEffect(proposalID, state.ProposalStatusChangedPayload{Status: string(assistants.ProposalDismissed), Reason: reason, Actor: string(actor.Kind)})
	if err != nil {
		return err
	}
	op := state.Operation{
		ID: requestID, Kind: "dismiss_proposal", Actor: actor,
		Expect:  state.Preconditions{ProposalStatus: map[string]assistants.ProposalStatus{proposalID: assistants.ProposalPending}},
		Effects: []state.Effect{eff},
	}
	_, err = o.opts.Store.ApplyRunOperation(op, func(state.OperationResult) {
		o.publishNotice(bus.Event{Type: bus.EventProposalStatus, StageID: p.StageID, Data: map[string]any{
			"proposal_id": proposalID, "status": string(assistants.ProposalDismissed),
		}})
	})
	return err
}
