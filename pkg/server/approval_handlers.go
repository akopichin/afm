package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"unicode/utf8"

	"github.com/akopichin/afm-engine/engine"
	"github.com/akopichin/afm/contracts/control"
)

// approvalRequest is the strict body of POST /api/stages/{id}/approval; the stage comes from the URL.
type approvalRequest struct {
	RunID            string                   `json:"run_id"`
	ExpectedRevision control.ApprovalRevision `json:"expected_revision"`
}

// routeApproval serves the conditional approval of one stage behind the local guard. Without ApprovalActions
// the route answers 404 like every other capability that is off.
func (s *Server) routeApproval(w http.ResponseWriter, r *http.Request) {
	if s.approval == nil {
		http.NotFound(w, r)
		return
	}
	LocalGuard(http.HandlerFunc(s.handleApproval)).ServeHTTP(w, r)
}

func (s *Server) handleApproval(w http.ResponseWriter, r *http.Request) {
	stageID, ok := stageCommandID(w, r, "/approval")
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		snap, err := s.approval.ApprovalSnapshot(r.Context(), stageID)
		if err != nil {
			writeCommandError(w, err)
			return
		}
		// JSON would replace invalid bytes, so the client would approve a revision of text it never saw.
		if !utf8.ValidString(snap.PlanMarkdown) {
			writeCommandError(w, approvalConflict(control.CodeApprovalUnavailable, "plan is not valid UTF-8"))
			return
		}
		writeJSON(w, snap)
		return
	}
	req, ok := decodeApprovalRequest(w, r)
	if !ok {
		return
	}
	// Another run's state is meaningless here, so this rejection reads nothing from the engine.
	if runID := s.runID(); runID != "" && req.RunID != runID {
		writeCommandError(w, approvalConflict(control.CodeApprovalRunChanged, "run_id does not match the run served here"))
		return
	}
	approve := control.ApproveExpectedRequest{RunID: req.RunID, StageID: stageID, ExpectedRevision: req.ExpectedRevision}
	if _, err := ApprovalPreflight(r.Context(), s.approval, approve); err != nil {
		writeCommandError(w, err)
		return
	}
	res, err := s.approval.ApproveExpected(r.Context(), approve)
	if err != nil {
		writeCommandError(w, err)
		return
	}
	writeJSON(w, res)
}

// ApprovalPreflight returns the current snapshot only when req names exactly that plan and a transport can show
// it unchanged. The engine's revision covers raw bytes, not displayability, so without this a client could
// approve text it never saw. It is no substitute for ApproveExpected, whose atomic check must still follow.
func ApprovalPreflight(ctx context.Context, approval ApprovalActions, req control.ApproveExpectedRequest) (control.ApprovalSnapshot, error) {
	snap, err := approval.ApprovalSnapshot(ctx, req.StageID)
	var ee *engine.Error
	if errors.As(err, &ee) && ee.Kind == engine.KindInvalid {
		// The stage no longer waits for approval, which ApproveExpected itself reports as stale.
		return control.ApprovalSnapshot{}, approvalConflict(control.CodeApprovalStale, "the stage is no longer waiting for approval")
	}
	switch {
	case err != nil:
		return control.ApprovalSnapshot{}, err
	case snap.RunID != req.RunID:
		return control.ApprovalSnapshot{}, approvalConflict(control.CodeApprovalRunChanged, "run_id does not match the run served here")
	case snap.StageID != req.StageID || !utf8.ValidString(snap.PlanMarkdown):
		return control.ApprovalSnapshot{}, approvalConflict(control.CodeApprovalUnavailable, "the plan cannot be shown unchanged")
	case snap.Revision != req.ExpectedRevision:
		return control.ApprovalSnapshot{}, approvalConflict(control.CodeApprovalStale, "the plan changed since it was read")
	}
	return snap, nil
}

func approvalConflict(code, message string) *engine.Error {
	return &engine.Error{Kind: engine.KindConflict, Code: code, Message: message}
}

// decodeApprovalRequest rejects an empty body, unknown fields, trailing data and empty identifiers: a
// conditional approve with a missing revision must never fall back to approving whatever is waiting.
func decodeApprovalRequest(w http.ResponseWriter, r *http.Request) (approvalRequest, bool) {
	var req approvalRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	err := dec.Decode(&req)
	if err == nil && dec.Decode(&struct{}{}) != io.EOF {
		err = errors.New("trailing data")
	}
	if err != nil || req.RunID == "" || req.ExpectedRevision == "" {
		http.Error(w, "run_id and expected_revision are required", http.StatusBadRequest)
		return approvalRequest{}, false
	}
	return req, true
}
