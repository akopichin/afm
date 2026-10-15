package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

// ProposalView is one Proposal's dashboard-facing projection — GET /api/stages/{id}/proposals.
type ProposalView struct {
	ID                      string    `json:"id"`
	StageID                 string    `json:"stage_id"`
	TaskID                  string    `json:"task_id,omitempty"`
	Type                    string    `json:"type"`
	Title                   string    `json:"title"`
	Rationale               string    `json:"rationale,omitempty"`
	Authors                 []string  `json:"authors,omitempty"`
	SourceMessageIDs        []string  `json:"source_message_ids,omitempty"`
	SourceFindingIDs        []string  `json:"source_finding_ids,omitempty"`
	ExpectedStageGeneration uint64    `json:"expected_stage_generation"`
	Status                  string    `json:"status"`
	CreatedAt               time.Time `json:"created_at"`
}

// ProposalActions is the read+write service for proposals (T6.7, §13.1) — a SEPARATE, narrow interface from
// StageActions/DiscussionService, not methods bolted onto either. nil = capability off: every route 404s.
type ProposalActions interface {
	// Proposals returns stageID's proposals in commit order.
	Proposals(stageID string) ([]ProposalView, error)
	// Accept durably accepts a pending proposal and starts its delivery — requestID is the idempotency key
	// (T6.6): the SAME requestID repeated resolves to the SAME outcome; a DIFFERENT requestID racing an
	// already-accepted/no-longer-pending proposal is rejected (maps to 409 via writeAssistantError's
	// stale_revision handling).
	Accept(ctx context.Context, proposalID, requestID string) error
	// Dismiss durably marks a pending proposal dismissed. Never touches the main agent.
	Dismiss(ctx context.Context, proposalID, requestID, reason string) error
}

const maxProposalActionBodyBytes = 4 << 10

// routeStageProposals handles GET /api/stages/{id}/proposals, dispatched from routeStages.
func (s *Server) routeStageProposals(w http.ResponseWriter, r *http.Request, stageID string) {
	if s.proposalActions == nil {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		writeAssistantErrCode(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if !isValidStageID(stageID) {
		writeAssistantErrCode(w, http.StatusBadRequest, "invalid_stage_id")
		return
	}
	proposals, err := s.proposalActions.Proposals(stageID)
	if err != nil {
		writeAssistantError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"proposals": proposals})
}

// proposalActionName — action names shared between routeProposals' routing switch and
// handleProposalAction's dispatch switch.
const (
	proposalActionAccept  = "accept"
	proposalActionDismiss = "dismiss"
)

// routeProposals handles POST /api/proposals/{id}/{accept|dismiss}.
func (s *Server) routeProposals(w http.ResponseWriter, r *http.Request) {
	if s.proposalActions == nil {
		http.NotFound(w, r)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/proposals/")
	segs := strings.Split(path, "/")
	if len(segs) != 2 || segs[0] == "" || segs[1] == "" {
		http.NotFound(w, r)
		return
	}
	proposalID, action := segs[0], segs[1]
	if !isValidStageID(proposalID) { // same safe-path-component charset as a stage/task id
		writeAssistantErrCode(w, http.StatusBadRequest, "invalid_proposal_id")
		return
	}
	if r.Method != http.MethodPost || (action != proposalActionAccept && action != proposalActionDismiss) {
		http.NotFound(w, r)
		return
	}
	s.handleProposalAction(w, r, proposalID, action)
}

func (s *Server) handleProposalAction(w http.ResponseWriter, r *http.Request, proposalID, action string) {
	if !sameOriginOK(r) {
		writeAssistantErrCode(w, http.StatusForbidden, "forbidden_origin")
		return
	}
	var body struct {
		RequestID string `json:"request_id"`
		Reason    string `json:"reason"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxProposalActionBodyBytes)).Decode(&body); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeAssistantErrCode(w, http.StatusRequestEntityTooLarge, "request_too_large")
			return
		}
		writeAssistantErrCode(w, http.StatusBadRequest, "invalid_request_body")
		return
	}
	if body.RequestID == "" {
		writeAssistantErrCode(w, http.StatusBadRequest, "request_id_required")
		return
	}

	var err error
	switch action {
	case proposalActionAccept:
		err = s.proposalActions.Accept(r.Context(), proposalID, body.RequestID)
	case proposalActionDismiss:
		err = s.proposalActions.Dismiss(r.Context(), proposalID, body.RequestID, body.Reason)
	default:
		http.NotFound(w, r) // unreachable: routeProposals already validated action
		return
	}
	if err != nil {
		writeAssistantError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{keyStatus: "ok"})
}
