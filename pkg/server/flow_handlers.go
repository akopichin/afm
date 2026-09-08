package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/akopichin/afm/pkg/orchestrator"
)

// codeNoNotes is shared with flow_handlers_test.go so the expected error
// code in the test can't drift from the one the handler actually writes.
const codeNoNotes = "no_notes"

// writeFlowError writes the scoped JSON error shape used only by the
// /api/flow/* handlers — {"error": "<code>"}, mirroring writeFilesError's
// convention so both API surfaces stay consistent for the dashboard client.
func writeFlowError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}

// flowErrCode maps an orchestrator review-pause sentinel error to an HTTP
// status and a stable machine-readable error code. Any unrecognized error
// (including nil-unsafe callers, which never reach here since callers only
// invoke this on a non-nil err) falls into the default 500 "internal" case.
func flowErrCode(err error) (int, string) {
	switch {
	case errors.Is(err, orchestrator.ErrRunFinalizing):
		return http.StatusConflict, "run_finalizing"
	case errors.Is(err, orchestrator.ErrNoReviewPause):
		return http.StatusConflict, "flow_not_paused"
	case errors.Is(err, orchestrator.ErrNoNotes):
		return http.StatusBadRequest, codeNoNotes
	case errors.Is(err, orchestrator.ErrNotOwned):
		return http.StatusBadRequest, "not_owned"
	case errors.Is(err, orchestrator.ErrTargetNotPaused):
		return http.StatusBadRequest, "target_not_paused"
	default:
		return http.StatusInternalServerError, "internal"
	}
}

// routeFlow dispatches POST /api/flow/pause, /api/flow/notes/inject and
// /api/flow/notes/cancel to their handlers. The whole capability is 404
// when no FlowActions is configured (host runs without review-pause wiring
// never expose these routes at all, matching the /api/files/ precedent).
// Notes CRUD under /api/flow/notes/* (list/create/update/delete a single
// note) is added in Task 18; until then any other path under that prefix
// falls through to the default 404.
func (s *Server) routeFlow(w http.ResponseWriter, r *http.Request) {
	if s.flowActions == nil {
		http.NotFound(w, r)
		return
	}
	switch {
	case r.URL.Path == "/api/flow/pause" && r.Method == http.MethodPost:
		s.handleFlowPause(w, r)
	case r.URL.Path == "/api/flow/notes/inject" && r.Method == http.MethodPost:
		s.handleFlowInject(w, r)
	case r.URL.Path == "/api/flow/notes/cancel" && r.Method == http.MethodPost:
		s.handleFlowCancel(w, r)
	default:
		http.NotFound(w, r)
	}
}

// handleFlowPause holds every active stage for review and returns the ids
// it actually paused.
func (s *Server) handleFlowPause(w http.ResponseWriter, r *http.Request) {
	ids, err := s.flowActions.PauseFlow(r.Context())
	if err != nil {
		status, code := flowErrCode(err)
		writeFlowError(w, status, code)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"paused_stages": ids})
}

// handleFlowInject closes the review-pause round by injecting the collected
// notes into the target stage and resuming the flow.
func (s *Server) handleFlowInject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StageID string `json:"stage_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.StageID == "" {
		writeFlowError(w, http.StatusBadRequest, "invalid_body")
		return
	}
	if err := s.flowActions.InjectNotesAndResume(r.Context(), req.StageID); err != nil {
		status, code := flowErrCode(err)
		writeFlowError(w, status, code)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "resuming"})
}

// handleFlowCancel closes the review-pause round by discarding the
// collected notes and resuming the flow.
func (s *Server) handleFlowCancel(w http.ResponseWriter, r *http.Request) {
	if err := s.flowActions.CancelNotesAndResume(r.Context()); err != nil {
		status, code := flowErrCode(err)
		writeFlowError(w, status, code)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "resuming"})
}
