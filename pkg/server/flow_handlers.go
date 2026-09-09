package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/akopichin/afm/pkg/orchestrator"
)

// codeNoNotes is shared with flow_handlers_test.go so the expected error
// code in the test can't drift from the one the handler actually writes.
const codeNoNotes = "no_notes"

// pathFlowNotes is the exact (no trailing id) notes-collection path — used
// by both the GET/POST cases in routeFlowNotes below.
const pathFlowNotes = "/api/flow/notes"

// maxNoteBodyBytes bounds a single notes add/update request body. A note's text
// is capped at 8 KiB server-side (orchestrator.maxNoteTextBytes); 128 KiB leaves
// generous room for the path/reference/content_sha envelope while refusing a
// pathological multi-megabyte body before it's ever buffered/decoded.
const maxNoteBodyBytes = 128 << 10

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
	case errors.Is(err, orchestrator.ErrResumeInProgress):
		return http.StatusConflict, "resume_in_progress"
	case errors.Is(err, orchestrator.ErrNoNotes):
		return http.StatusBadRequest, codeNoNotes
	case errors.Is(err, orchestrator.ErrNotOwned):
		return http.StatusBadRequest, "not_owned"
	case errors.Is(err, orchestrator.ErrTargetNotPaused):
		return http.StatusBadRequest, "target_not_paused"
	case errors.Is(err, orchestrator.ErrRevConflict):
		return http.StatusConflict, "rev_conflict"
	case errors.Is(err, orchestrator.ErrStaleContent):
		return http.StatusConflict, "stale_content"
	case errors.Is(err, orchestrator.ErrStaleLine):
		return http.StatusConflict, "stale_line"
	case errors.Is(err, orchestrator.ErrEmptyText):
		return http.StatusBadRequest, "empty_text"
	case errors.Is(err, orchestrator.ErrNoteTooLong):
		return http.StatusRequestEntityTooLarge, "note_too_long"
	case errors.Is(err, orchestrator.ErrTooManyNotes):
		return http.StatusConflict, "too_many_notes"
	case errors.Is(err, orchestrator.ErrNoteNotFound):
		return http.StatusNotFound, "note_not_found"
	default:
		return http.StatusInternalServerError, "internal"
	}
}

// currentReviewState nil-safely reads the flow-wide review-pause state — the
// same "none" default handleStatus falls back to when no ReviewState func is
// wired (see server.go's Config.ReviewState).
func (s *Server) currentReviewState() string {
	if s.reviewState == nil {
		return "none"
	}
	st, _ := s.reviewState()
	return st
}

// routeFlow dispatches POST /api/flow/pause, /api/flow/notes/inject,
// /api/flow/notes/cancel, and (Task 18) the notes CRUD routes under
// /api/flow/notes to their handlers. The whole capability is 404 when no
// FlowActions is configured (host runs without review-pause wiring never
// expose these routes at all, matching the /api/files/ precedent). The
// notes-prefix case must come after the two exact /api/flow/notes/inject and
// /api/flow/notes/cancel cases above it — otherwise it would shadow them.
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
	case strings.HasPrefix(r.URL.Path, "/api/flow/notes"):
		s.routeFlowNotes(w, r)
	default:
		http.NotFound(w, r)
	}
}

// routeFlowNotes dispatches the notes CRUD routes: GET/POST on
// /api/flow/notes (list/add), PUT/DELETE on /api/flow/notes/{id}
// (update/delete a single note).
func (s *Server) routeFlowNotes(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == pathFlowNotes && r.Method == http.MethodGet:
		s.handleNotesList(w, r)
	case r.URL.Path == pathFlowNotes && r.Method == http.MethodPost:
		s.handleNotesAdd(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/flow/notes/") && r.Method == http.MethodPut:
		s.handleNotesUpdate(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/flow/notes/") && r.Method == http.MethodDelete:
		s.handleNotesDelete(w, r)
	default:
		http.NotFound(w, r)
	}
}

// handleNotesList returns every currently collected review note plus the
// notes-list revision (for optimistic-concurrency writes). Safe to call
// regardless of pause state — ListNotes has no pause gate.
func (s *Server) handleNotesList(w http.ResponseWriter, r *http.Request) {
	notes, err := s.flowActions.ListNotes()
	if err != nil {
		status, code := flowErrCode(err)
		writeFlowError(w, status, code)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"rev": notes.Rev, "notes": notes.Notes})
}

// handleNotesAdd adds (or replaces, for the same root/path/line) a review
// note. The flow must be paused for review — checked here against
// currentReviewState() BEFORE calling into the orchestrator, so the 409
// happens even against a fake/mock FlowActions in tests that hasn't been
// told to also reject the call.
func (s *Server) handleNotesAdd(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Root        string `json:"root"`
		Path        string `json:"path"`
		Line        *int   `json:"line"`
		Text        string `json:"text"`
		ContentSHA  string `json:"content_sha"`
		ExpectedRev int    `json:"expected_rev"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxNoteBodyBytes)).Decode(&req); err != nil {
		writeFlowError(w, http.StatusBadRequest, "invalid_body")
		return
	}
	if s.currentReviewState() != "paused" {
		writeFlowError(w, http.StatusConflict, "flow_not_paused")
		return
	}
	note, rev, err := s.flowActions.AddNote(req.Root, req.Path, req.Line, req.Text, req.ContentSHA, req.ExpectedRev)
	if err != nil {
		status, code := flowErrCode(err)
		writeFlowError(w, status, code)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"note": note, "rev": rev})
}

// handleNotesUpdate edits the text of an existing note ({id} from the path).
func (s *Server) handleNotesUpdate(w http.ResponseWriter, r *http.Request) {
	id := extractStageID(r.URL.Path, "/api/flow/notes/", "")
	var req struct {
		Text        string `json:"text"`
		ExpectedRev int    `json:"expected_rev"`
	}
	if id == "" {
		writeFlowError(w, http.StatusBadRequest, "invalid_body")
		return
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxNoteBodyBytes)).Decode(&req); err != nil {
		writeFlowError(w, http.StatusBadRequest, "invalid_body")
		return
	}
	if s.currentReviewState() != "paused" {
		writeFlowError(w, http.StatusConflict, "flow_not_paused")
		return
	}
	rev, err := s.flowActions.UpdateNote(id, req.Text, req.ExpectedRev)
	if err != nil {
		status, code := flowErrCode(err)
		writeFlowError(w, status, code)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"rev": rev})
}

// handleNotesDelete removes an existing note ({id} from the path,
// expected_rev from the query string — DELETE has no conventional body).
func (s *Server) handleNotesDelete(w http.ResponseWriter, r *http.Request) {
	id := extractStageID(r.URL.Path, "/api/flow/notes/", "")
	if id == "" {
		writeFlowError(w, http.StatusBadRequest, "invalid_body")
		return
	}
	expectedRev, err := strconv.Atoi(r.URL.Query().Get("expected_rev"))
	if err != nil {
		writeFlowError(w, http.StatusBadRequest, "invalid_body")
		return
	}
	if s.currentReviewState() != "paused" {
		writeFlowError(w, http.StatusConflict, "flow_not_paused")
		return
	}
	rev, err := s.flowActions.DeleteNote(id, expectedRev)
	if err != nil {
		status, code := flowErrCode(err)
		writeFlowError(w, status, code)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"rev": rev})
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
