package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/akopichin/afm/pkg/mcp"
	"github.com/akopichin/afm/pkg/state"
)

const (
	phasePlanning       = "planning"
	phaseImplementation = "implementation"
	phaseReview         = "review"
	keyStageID          = "stage_id"
	keyStatus           = "status"
)

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	rs, err := state.Load(s.stateFile)
	if err != nil {
		http.Error(w, "failed to load state", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rs)
}

func (s *Server) handlePlan(w http.ResponseWriter, r *http.Request) {
	stageID := extractStageID(r.URL.Path, "/api/stages/", "/plan")
	if !isValidStageID(stageID) {
		http.Error(w, "invalid stage id", http.StatusBadRequest)
		return
	}
	planFile := filepath.Join(s.runDir, stageID, "plan.md")
	data, err := os.ReadFile(planFile)
	if err != nil {
		http.Error(w, fmt.Sprintf("plan not found: %v", err), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	_, _ = w.Write(data) //nolint:gosec // G705: data read from server-side file, not user input
}

func (s *Server) handleLog(w http.ResponseWriter, r *http.Request) {
	stageID := extractStageID(r.URL.Path, "/api/stages/", "/log")
	if !isValidStageID(stageID) {
		http.Error(w, "invalid stage id", http.StatusBadRequest)
		return
	}
	stageDir := filepath.Join(s.runDir, stageID)

	var logContent string
	for _, name := range []string{"planning.log", "planning-revision.log", "implementation.log", "review.log"} {
		data, err := os.ReadFile(filepath.Join(stageDir, name))
		if err == nil {
			logContent += string(data)
		}
	}
	if logContent == "" {
		http.Error(w, "no logs found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = fmt.Fprint(w, logContent) //nolint:gosec // G705: log content from server-side files
}

func (s *Server) handleApprove(w http.ResponseWriter, r *http.Request) {
	stageID := extractStageID(r.URL.Path, "/api/stages/", "/approve")
	if !isValidStageID(stageID) {
		http.Error(w, "invalid stage id", http.StatusBadRequest)
		return
	}
	rs, err := state.Load(s.stateFile)
	if err != nil {
		http.Error(w, "failed to load state", http.StatusInternalServerError)
		return
	}
	st, ok := rs.Stages[stageID]
	if !ok {
		http.Error(w, "stage not found", http.StatusNotFound)
		return
	}
	if st.Status != state.StatusAwaitingApproval {
		http.Error(w, fmt.Sprintf("stage is %s, not awaiting_approval", st.Status), http.StatusBadRequest)
		return
	}
	s.approveFn(stageID)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{keyStatus: "approved", keyStageID: stageID})
}

type reviseRequest struct {
	Feedback string `json:"feedback"`
}

func (s *Server) handleRevise(w http.ResponseWriter, r *http.Request) {
	stageID := extractStageID(r.URL.Path, "/api/stages/", "/revise")
	if !isValidStageID(stageID) {
		http.Error(w, "invalid stage id", http.StatusBadRequest)
		return
	}
	rs, err := state.Load(s.stateFile)
	if err != nil {
		http.Error(w, "failed to load state", http.StatusInternalServerError)
		return
	}
	st, ok := rs.Stages[stageID]
	if !ok {
		http.Error(w, "stage not found", http.StatusNotFound)
		return
	}
	if st.Status != state.StatusAwaitingApproval {
		http.Error(w, fmt.Sprintf("stage is %s, not awaiting_approval", st.Status), http.StatusBadRequest)
		return
	}
	var req reviseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Feedback == "" {
		http.Error(w, "feedback is required", http.StatusBadRequest)
		return
	}
	s.reviseFn(stageID, req.Feedback)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{keyStatus: "revised", keyStageID: stageID})
}

func (s *Server) handleRetry(w http.ResponseWriter, r *http.Request) {
	stageID := extractStageID(r.URL.Path, "/api/stages/", "/retry")
	if !isValidStageID(stageID) {
		http.Error(w, "invalid stage id", http.StatusBadRequest)
		return
	}

	rs, err := state.Load(s.stateFile)
	if err != nil {
		http.Error(w, "failed to load state", http.StatusInternalServerError)
		return
	}
	st, ok := rs.Stages[stageID]
	if !ok {
		http.Error(w, "stage not found", http.StatusNotFound)
		return
	}
	if st.Status != state.StatusFailed {
		http.Error(w, fmt.Sprintf("stage is %s, not failed", st.Status), http.StatusBadRequest)
		return
	}

	s.retryFn(stageID)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{keyStatus: "retried", keyStageID: stageID})
}

func (s *Server) handleDialogGet(w http.ResponseWriter, r *http.Request) {
	stageID := extractStageID(r.URL.Path, "/api/stages/", "/dialog")
	if !isValidStageID(stageID) {
		http.Error(w, "invalid stage id", http.StatusBadRequest)
		return
	}
	stageDir := filepath.Join(s.runDir, stageID)
	type uiEntry struct {
		ID          string   `json:"id"`
		Phase       string   `json:"phase"`
		TS          string   `json:"ts"`
		Question    string   `json:"question"`
		Options     []string `json:"options,omitempty"`
		AllowCustom bool     `json:"allow_custom"`
		Answer      *string  `json:"answer,omitempty"`
		AnswerTS    string   `json:"answer_ts,omitempty"`
		FromOptions bool     `json:"from_options,omitempty"`
	}
	var out []uiEntry
	for _, phase := range []string{phasePlanning, phaseImplementation, phaseReview} {
		path := filepath.Join(stageDir, phase+".dialog.jsonl")
		entries, err := mcp.ReadDialog(path)
		if err != nil {
			continue
		}
		for _, e := range entries {
			out = append(out, uiEntry{
				ID: e.ID, Phase: phase, TS: e.TS, Question: e.Question,
				Options: e.Options, AllowCustom: e.AllowCustom,
				Answer: e.Answer, AnswerTS: e.AnswerTS, FromOptions: e.FromOptions,
			})
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

type dialogAnswerRequest struct {
	ID          string `json:"id"`
	Phase       string `json:"phase"`
	Answer      string `json:"answer"`
	FromOptions bool   `json:"from_options"`
}

func (s *Server) handleDialogAnswer(w http.ResponseWriter, r *http.Request) {
	stageID := extractStageID(r.URL.Path, "/api/stages/", "/dialog/answer")
	if !isValidStageID(stageID) {
		http.Error(w, "invalid stage id", http.StatusBadRequest)
		return
	}
	var req dialogAnswerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if req.ID == "" || req.Phase == "" || req.Answer == "" {
		http.Error(w, "id, phase, answer required", http.StatusBadRequest)
		return
	}
	if req.Phase != phasePlanning && req.Phase != phaseImplementation && req.Phase != phaseReview {
		http.Error(w, "invalid phase", http.StatusBadRequest)
		return
	}
	dialogPath := filepath.Join(s.runDir, stageID, req.Phase+".dialog.jsonl")

	// Verify the question exists.
	entry, _ := mcp.FindEntry(dialogPath, req.ID)
	if entry == nil {
		http.Error(w, "question not found", http.StatusNotFound)
		return
	}

	// Reject duplicate answers.
	if entry.Answer != nil {
		http.Error(w, "question already answered", http.StatusConflict)
		return
	}

	if err := mcp.AppendAnswer(dialogPath, mcp.Answer{
		ID: req.ID, Answer: req.Answer, FromOptions: req.FromOptions,
	}); err != nil {
		http.Error(w, "persist answer: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if s.dialogAnswerFn != nil {
		if err := s.dialogAnswerFn(stageID, req.Phase, req.ID, req.Answer, req.FromOptions); err != nil {
			http.Error(w, "notify: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{keyStatus: "ok"})
}

func (s *Server) handleDialogCancel(w http.ResponseWriter, r *http.Request) {
	stageID := extractStageID(r.URL.Path, "/api/stages/", "/dialog/cancel")
	if !isValidStageID(stageID) {
		http.Error(w, "invalid stage id", http.StatusBadRequest)
		return
	}
	rs, err := state.Load(s.stateFile)
	if err != nil {
		http.Error(w, "load state: "+err.Error(), http.StatusInternalServerError)
		return
	}
	st, ok := rs.Stages[stageID]
	if !ok || st.Status != state.StatusAwaitingUserInput {
		http.Error(w, "stage is not awaiting user input", http.StatusBadRequest)
		return
	}
	if s.dialogCancelFn != nil {
		if err := s.dialogCancelFn(stageID); err != nil {
			http.Error(w, "cancel: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{keyStatus: "cancelled"})
}

func extractStageID(path, prefix, suffix string) string {
	path = strings.TrimPrefix(path, prefix)
	path = strings.TrimSuffix(path, suffix)
	return path
}

// safeStageIDRe restricts stage IDs to alphanumeric, dash, underscore, dot.
var safeStageIDRe = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// isValidStageID rejects path traversal and special characters in stage IDs.
func isValidStageID(id string) bool {
	if !safeStageIDRe.MatchString(id) || strings.Contains(id, "..") {
		return false
	}
	return true
}
