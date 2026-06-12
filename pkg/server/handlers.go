package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/akopichin/afm/pkg/executor"
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
	rs := s.store.Snapshot()
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
	rs := s.store.Snapshot()
	st, ok := rs.Stages[stageID]
	if !ok {
		http.Error(w, "stage not found", http.StatusNotFound)
		return
	}
	if st.Status != state.StatusAwaitingApproval {
		http.Error(w, fmt.Sprintf("stage is %s, not awaiting_approval", st.Status), http.StatusBadRequest)
		return
	}
	if err := s.approveFn(r.Context(), stageID); err != nil {
		http.Error(w, "approve failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
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
	rs := s.store.Snapshot()
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
	if err := s.reviseFn(r.Context(), stageID, req.Feedback); err != nil {
		http.Error(w, "revise failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{keyStatus: "revised", keyStageID: stageID})
}

func (s *Server) handleRetry(w http.ResponseWriter, r *http.Request) {
	stageID := extractStageID(r.URL.Path, "/api/stages/", "/retry")
	if !isValidStageID(stageID) {
		http.Error(w, "invalid stage id", http.StatusBadRequest)
		return
	}

	rs := s.store.Snapshot()
	st, ok := rs.Stages[stageID]
	if !ok {
		http.Error(w, "stage not found", http.StatusNotFound)
		return
	}
	if st.Status != state.StatusFailed {
		http.Error(w, fmt.Sprintf("stage is %s, not failed", st.Status), http.StatusBadRequest)
		return
	}

	if err := s.retryFn(r.Context(), stageID); err != nil {
		http.Error(w, "retry failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
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
	out := buildDialogEntries(stageDir)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// typeAgentText is the dialogUIEntry.Type value for agent text messages.
const typeAgentText = "agent_text"

// dialogUIEntry — элемент диалоговой ленты для UI. Type == typeAgentText
// означает текстовое сообщение агента (заполнен Text); пустой Type —
// вопрос/ответ из ask_user.
type dialogUIEntry struct {
	Type        string   `json:"type,omitempty"`
	Text        string   `json:"text,omitempty"`
	ID          string   `json:"id,omitempty"`
	Phase       string   `json:"phase"`
	TS          string   `json:"ts,omitempty"`
	Question    string   `json:"question,omitempty"`
	Options     []string `json:"options,omitempty"`
	AllowCustom bool     `json:"allow_custom"`
	Answer      *string  `json:"answer,omitempty"`
	AnswerTS    string   `json:"answer_ts,omitempty"`
	FromOptions bool     `json:"from_options,omitempty"`
}

// phaseStreamLogs — stream-json логи каждой фазы в хронологическом порядке
// запусков (см. имена logFile в orchestrator).
var phaseStreamLogs = map[string][]string{
	phasePlanning:       {"planning.jsonl", "planning-reprompt.jsonl", "planning-revision.jsonl"},
	phaseImplementation: {"implementation.jsonl"},
	phaseReview:         {"review.jsonl"},
}

// buildDialogEntries собирает диалоговую ленту стейджа: вопросы/ответы из
// <phase>.dialog.jsonl, перемежённые текстовыми сообщениями агента из
// stream-json логов. Порядок берётся из stream-лога — там и текст, и вызовы
// ask_user идут в реальной последовательности. Тексты показываются только
// для фаз, где есть диалог (интерактивные стейджи), чтобы не раздувать
// панель на обычных стейджах.
func buildDialogEntries(stageDir string) []dialogUIEntry {
	var out []dialogUIEntry
	for _, phase := range []string{phasePlanning, phaseImplementation, phaseReview} {
		entries, err := mcp.ReadDialog(filepath.Join(stageDir, phase+".dialog.jsonl"))
		if err != nil || len(entries) == 0 {
			continue
		}
		byID := make(map[string]mcp.Entry, len(entries))
		for _, e := range entries {
			byID[e.ID] = e
		}
		emitted := map[string]bool{}
		for _, logName := range phaseStreamLogs[phase] {
			for _, it := range executor.DialogTranscript(filepath.Join(stageDir, logName)) {
				if it.Text != "" {
					out = append(out, dialogUIEntry{Type: typeAgentText, Phase: phase, Text: it.Text})
					continue
				}
				e, ok := byID[it.AskUserID]
				if !ok {
					continue // вопрос ещё не записан в dialog-файл
				}
				emitted[e.ID] = true
				out = append(out, questionUIEntry(phase, e))
			}
		}
		// Вопросы, не найденные в stream-логе (например, лог недоступен),
		// добавляем в конце фазы — прежнее поведение без текстов агента.
		for _, e := range entries {
			if !emitted[e.ID] {
				out = append(out, questionUIEntry(phase, e))
			}
		}
	}
	return out
}

func questionUIEntry(phase string, e mcp.Entry) dialogUIEntry {
	return dialogUIEntry{
		ID: e.ID, Phase: phase, TS: e.TS, Question: e.Question,
		Options: e.Options, AllowCustom: e.AllowCustom,
		Answer: e.Answer, AnswerTS: e.AnswerTS, FromOptions: e.FromOptions,
	}
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
	rs := s.store.Snapshot()
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
