package assistants

import (
	"encoding/json"
	"fmt"
	"time"
)

// ProposalType enumerates what kind of change a Proposal asks AFM to durably commit (§9.4, dev-plan.md T6.4).
// Этап 6 fully wires delivery for ProposalFeedback only (via the existing Revise mechanism, T6.5); the other
// three are modeled here for a stable on-disk shape but their delivery paths land in later stages (attach_assistant
// mirrors Этап 3's Attach, add_stage is Этап 9, required_change is Этап 7's mode-flip) — see dev-plan.md's
// Review decisions for Этап 6.
type ProposalType string

const (
	// ProposalFeedback asks AFM to deliver free-text feedback to the stage's live/next agent via the existing
	// Revise mechanism (T6.5) — the only type delivered end-to-end in Этап 6.
	ProposalFeedback ProposalType = "feedback"
	// ProposalAttachAssistant mirrors ActionAttachAssistant (actions.go) — attach one new helper task.
	ProposalAttachAssistant ProposalType = "attach_assistant"
	// ProposalAddStage asks AFM to insert a new ordinary stage into the flow graph (Этап 9).
	ProposalAddStage ProposalType = "add_stage"
	// ProposalRequiredChange asks AFM to flip a helper task's Mode between advisory and required (Этап 7).
	ProposalRequiredChange ProposalType = "required_change"
)

// ProposalStatus is one state in the Proposal lifecycle (§9.4):
//
//	pending ─┬─ dismissed
//	         ├─ stale
//	         └─ accepted → queued → delivered
//	                              └─ delivery_failed
type ProposalStatus string

const (
	ProposalPending        ProposalStatus = "pending"
	ProposalDismissed      ProposalStatus = "dismissed"
	ProposalStale          ProposalStatus = "stale"
	ProposalAccepted       ProposalStatus = "accepted"
	ProposalQueued         ProposalStatus = "queued"
	ProposalDelivered      ProposalStatus = "delivered"
	ProposalDeliveryFailed ProposalStatus = "delivery_failed"
)

// Terminal reports whether status is terminal — nothing further ever happens to a proposal from here.
func (s ProposalStatus) Terminal() bool {
	switch s {
	case ProposalDismissed, ProposalStale, ProposalDelivered, ProposalDeliveryFailed:
		return true
	default:
		return false
	}
}

// proposalTransitions is the full, exhaustive allow-list for Proposal.CanTransition — deliberately a closed
// set: there is no status anywhere in this map spelling "fixed"/"resolved"/"applied" (TestProposal_DeliveredMeansAcceptedCorrection),
// and 'delivered' is reachable ONLY from 'queued', never directly from 'pending' (TestProposal_NoFixedStatusAfterHttp200)
// — an HTTP 200 on accept can only ever land on 'accepted'.
var proposalTransitions = map[ProposalStatus][]ProposalStatus{
	ProposalPending:  {ProposalDismissed, ProposalStale, ProposalAccepted},
	ProposalAccepted: {ProposalQueued, ProposalDeliveryFailed},
	ProposalQueued:   {ProposalDelivered, ProposalDeliveryFailed},
}

// ProposalDecision is one entry of a Proposal's decision history (dev-plan.md T6.4: "статус и история решения").
type ProposalDecision struct {
	Status ProposalStatus `json:"status"`
	Reason string         `json:"reason,omitempty"`
	Actor  string         `json:"actor,omitempty"`
	At     time.Time      `json:"at"`
}

// Proposal is a structured, human-confirmable change request produced by the bounded synthesizer (T6.3) from
// one discussion (T6.1/T6.2). See package doc comment on ProposalStatus for the lifecycle diagram.
type Proposal struct {
	ID string `json:"id"`
	// StageID/TaskID — target of the proposal. TaskID is empty for a stage-level proposal (e.g. plain
	// feedback); set when the proposal targets one specific helper task (e.g. required_change).
	StageID string       `json:"stage_id"`
	TaskID  string       `json:"task_id,omitempty"`
	Type    ProposalType `json:"type"`
	Title   string       `json:"title"`
	// Rationale is the human-readable justification; Payload is the exact, typed, machine-actionable change
	// (e.g. the feedback text itself for ProposalFeedback) — kept separate so delivery code never has to
	// parse prose to recover the actionable part.
	Rationale string          `json:"rationale,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	// Authors/SourceMessageIDs/SourceFindingIDs — full provenance: who contributed, and which exact discussion
	// messages/findings this proposal is grounded in (TestSynthesis_DoesNotInventEvidence in synthesis.go
	// validates these resolve to real messages/findings BEFORE a Proposal is ever accepted as valid).
	Authors          []MessageAuthor `json:"authors,omitempty"`
	SourceMessageIDs []string        `json:"source_message_ids,omitempty"`
	SourceFindingIDs []string        `json:"source_finding_ids,omitempty"`
	// ThreadID is the mesh thread SourceMessageIDs live in (Agent Mesh Phase 6, T6.6) — today always the
	// target stage's one discussion thread (see pkg/orchestrator's deriveDiscussionThreadID), computed
	// deterministically from StageID whether or not that thread has actually been opened yet. A client can
	// use this to jump straight to the grounding conversation instead of re-deriving the thread id itself.
	ThreadID string `json:"thread_id,omitempty"`
	// ExpectedStageGeneration/ExpectedGraphRevision — the context version this proposal was computed against.
	// A stage generation that has since moved on makes the proposal stale (CheckGeneration) before it is ever
	// accepted. ExpectedGraphRevision is reserved for Этап 10 (flow-graph-changing proposals); Этап 6 never
	// checks it.
	ExpectedStageGeneration uint64 `json:"expected_stage_generation"`
	ExpectedGraphRevision   string `json:"expected_graph_revision,omitempty"`

	Status  ProposalStatus     `json:"status"`
	History []ProposalDecision `json:"history,omitempty"`
	// OperationID is the operation that created this proposal — the natural idempotency anchor for the accept
	// step that follows (T6.6).
	OperationID string    `json:"operation_id,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// CanTransition reports whether `to` is a legal next status from p's CURRENT status, per proposalTransitions.
func (p Proposal) CanTransition(to ProposalStatus) bool {
	for _, allowed := range proposalTransitions[p.Status] {
		if allowed == to {
			return true
		}
	}
	return false
}

// CheckGeneration reports an error if currentGeneration no longer matches the generation this proposal was
// computed against — the stage has moved on to a different attempt since synthesis ran
// (TestProposal_StaleWhenGenerationChanged).
func (p Proposal) CheckGeneration(currentGeneration uint64) error {
	if p.ExpectedStageGeneration != currentGeneration {
		return fmt.Errorf("assistants: proposal %s is stale: expected stage generation %d, current is %d",
			p.ID, p.ExpectedStageGeneration, currentGeneration)
	}
	return nil
}
