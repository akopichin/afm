package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/mcp"
	"github.com/akopichin/afm/pkg/state"
)

const (
	keyStageID = "stage_id"
	keyStatus  = "status"
)

// statusResponse is GET /api/status's wire shape: run-level fields plus one
// ordered []StageView (see stageview.go) instead of five parallel per-stage
// maps the frontend used to re-join by id.
type statusResponse struct {
	FlowName             string      `json:"flow_name"`
	StartedAt            time.Time   `json:"started_at"`
	Description          string      `json:"description,omitempty"`
	Stages               []StageView `json:"stages"`
	LastSeq              uint64      `json:"last_seq"`
	IdleAccumulatedMs    int64       `json:"idle_accumulated_ms"`
	IdleSince            *time.Time  `json:"idle_since,omitempty"`
	BackoffAccumulatedMs int64       `json:"backoff_accumulated_ms"`
	BackoffOpenSince     []time.Time `json:"backoff_open_since,omitempty"`
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	rs := s.store.Snapshot()
	resp := statusResponse{
		FlowName:             rs.FlowName,
		StartedAt:            rs.StartedAt,
		Description:          s.Description,
		Stages:               buildStageViews(rs, s.runDir, s.stageInteractive, s.stageAutoApprove, s.stageIsScript, s.stageDependsOn),
		LastSeq:              rs.LastSeq,
		IdleAccumulatedMs:    rs.IdleAccumulatedMs,
		IdleSince:            rs.IdleSince(),
		BackoffAccumulatedMs: rs.BackoffAccumulatedMs,
		BackoffOpenSince:     rs.BackoffOpenSince(),
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
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
	appendLog := func(name string) {
		data, err := os.ReadFile(filepath.Join(stageDir, name))
		if err == nil {
			logContent += string(data)
		}
	}
	// before.log/script.log/after.log — логи script_before/script-стадий/
	// script_after хуков (см. pkg/orchestrator/hooks.go, agents.go). Стадии
	// без хуков их просто не пишут — appendLog молча пропускает отсутствующие
	// файлы, как и обычные phase-логи ниже.
	appendLog("before.log")
	for _, p := range flow.Phases() {
		for _, name := range flow.PhaseLogFiles(p) {
			appendLog(name)
		}
	}
	appendLog("script.log")
	appendLog("after.log")
	if logContent == "" {
		http.Error(w, "no logs found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = fmt.Fprint(w, logContent) //nolint:gosec // G705: log content from server-side files
}

// handleSupervisor возвращает последнее решение супервизора для стадии
// (читает <runDir>/supervisor.jsonl). Даёт UI показать резолюцию
// (autonomous/standard + reason) персистентно: событие шины EventSupervisorDecision
// live-only и теряется, если дашборд подключился после старта стадии.
func (s *Server) handleSupervisor(w http.ResponseWriter, r *http.Request) {
	stageID := extractStageID(r.URL.Path, "/api/stages/", "/supervisor")
	if !isValidStageID(stageID) {
		http.Error(w, "invalid stage id", http.StatusBadRequest)
		return
	}
	data, err := os.ReadFile(filepath.Join(s.runDir, "supervisor.jsonl"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var latest map[string]string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry map[string]string
		if json.Unmarshal([]byte(line), &entry) != nil {
			continue
		}
		if entry["stage_id"] == stageID {
			latest = entry
		}
	}
	if latest == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(latest)
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
	if err := s.actions.Approve(r.Context(), stageID); err != nil {
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
	allowed := st.Status == state.StatusAwaitingApproval || st.Status == state.StatusRunning
	if !allowed {
		http.Error(w, fmt.Sprintf("stage is %s, not awaiting_approval or running", st.Status), http.StatusBadRequest)
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
	if err := s.actions.Revise(r.Context(), stageID, req.Feedback); err != nil {
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

	if err := s.actions.Retry(r.Context(), stageID); err != nil {
		http.Error(w, "retry failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{keyStatus: "retried", keyStageID: stageID})
}

func (s *Server) handlePause(w http.ResponseWriter, r *http.Request) {
	stageID := extractStageID(r.URL.Path, "/api/stages/", "/pause")
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
	switch st.Status {
	case state.StatusRunning, state.StatusPlanning, state.StatusRevising, state.StatusRetrying:
	default:
		http.Error(w, fmt.Sprintf("stage is %s, cannot be paused", st.Status), http.StatusBadRequest)
		return
	}
	if s.stageIsScript[stageID] && st.Status == state.StatusRunning {
		http.Error(w, "pause is not supported mid-script execution", http.StatusConflict)
		return
	}

	if err := s.actions.Pause(r.Context(), stageID); err != nil {
		http.Error(w, "pause failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{keyStatus: "paused", keyStageID: stageID})
}

func (s *Server) handleContinue(w http.ResponseWriter, r *http.Request) {
	stageID := extractStageID(r.URL.Path, "/api/stages/", "/continue")
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
	if st.Status != state.StatusPaused {
		http.Error(w, fmt.Sprintf("stage is %s, not paused", st.Status), http.StatusBadRequest)
		return
	}

	if err := s.actions.Continue(r.Context(), stageID); err != nil {
		http.Error(w, "continue failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{keyStatus: "continued", keyStageID: stageID})
}

// handleRetryHook re-runs the retry cycle of a before/after hook currently
// blocked on a user decision (stage in hook_failed for script_before, or the
// stage's after-hook notice for script_after — the latter never changes the
// stage's FSM status, see runAfterHook's doc comment). Unlike handleRetry,
// there is no status precondition to check here: RetryHook's own "no waiter"
// error is the only source of truth for whether a hook is actually pending.
func (s *Server) handleRetryHook(w http.ResponseWriter, r *http.Request) {
	stageID := extractStageID(r.URL.Path, "/api/stages/", "/retry-hook")
	if !isValidStageID(stageID) {
		http.Error(w, "invalid stage id", http.StatusBadRequest)
		return
	}
	if s.secondary == nil {
		http.Error(w, "retry-hook not supported", http.StatusNotImplemented)
		return
	}
	if err := s.secondary.RetryHook(stageID); err != nil {
		http.Error(w, "retry-hook failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{keyStatus: "retried", keyStageID: stageID})
}

// handleSkipHook skips a before/after hook currently blocked on a user
// decision. See handleRetryHook's comment on why there is no status check.
func (s *Server) handleSkipHook(w http.ResponseWriter, r *http.Request) {
	stageID := extractStageID(r.URL.Path, "/api/stages/", "/skip-hook")
	if !isValidStageID(stageID) {
		http.Error(w, "invalid stage id", http.StatusBadRequest)
		return
	}
	if s.secondary == nil {
		http.Error(w, "skip-hook not supported", http.StatusNotImplemented)
		return
	}
	if err := s.secondary.SkipHook(stageID); err != nil {
		http.Error(w, "skip-hook failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{keyStatus: "skipped", keyStageID: stageID})
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
	Type         string   `json:"type,omitempty"`
	Text         string   `json:"text,omitempty"`
	ID           string   `json:"id,omitempty"`
	Phase        string   `json:"phase"`
	TS           string   `json:"ts,omitempty"`
	Question     string   `json:"question,omitempty"`
	Options      []string `json:"options,omitempty"`
	AllowCustom  bool     `json:"allow_custom"`
	Answer       *string  `json:"answer,omitempty"`
	AnswerTS     string   `json:"answer_ts,omitempty"`
	FromOptions  bool     `json:"from_options,omitempty"`
	AutoAnswered bool     `json:"auto_answered,omitempty"`
}

// buildDialogEntries собирает диалоговую ленту стейджа: вопросы/ответы из
// <phase>.dialog.jsonl, перемежённые текстовыми сообщениями агента из
// stream-json логов. Порядок берётся из stream-лога — там и текст, и вызовы
// ask_user идут в реальной последовательности. Тексты показываются только
// для фаз, где есть диалог (интерактивные стейджи), чтобы не раздувать
// панель на обычных стейджах.
func buildDialogEntries(stageDir string) []dialogUIEntry {
	var out []dialogUIEntry
	for _, p := range flow.Phases() {
		entries, err := mcp.ReadDialog(filepath.Join(stageDir, string(p)+".dialog.jsonl"))
		if err != nil || len(entries) == 0 {
			continue
		}
		byID := make(map[string]mcp.Entry, len(entries))
		for _, e := range entries {
			byID[e.ID] = e
		}
		emitted := map[string]bool{}
		for _, logName := range flow.PhaseStreamLogs(p) {
			for _, it := range executor.DialogTranscript(filepath.Join(stageDir, logName)) {
				if it.Text != "" {
					out = append(out, dialogUIEntry{Type: typeAgentText, Phase: string(p), Text: it.Text})
					continue
				}
				e, ok := byID[it.AskUserID]
				if !ok {
					continue // вопрос ещё не записан в dialog-файл
				}
				emitted[e.ID] = true
				out = append(out, questionUIEntry(string(p), e))
			}
		}
		// Вопросы, не найденные в stream-логе (например, лог недоступен),
		// добавляем в конце фазы — прежнее поведение без текстов агента.
		for _, e := range entries {
			if !emitted[e.ID] {
				out = append(out, questionUIEntry(string(p), e))
			}
		}
	}
	// Гарантия видимости: текущий unanswered вопрос показываем ВСЕГДА, даже если
	// poller ещё не дописал его в <phase>.dialog.jsonl (или staging-состояние) —
	// читаем *.question.json напрямую. Иначе UI не покажет вопрос, который агент
	// уже задал (особенно autonomous-фаза, диалог без предистории).
	if pending, perr := mcp.FindUnansweredQuestions(stageDir); perr == nil {
		haveID := make(map[string]bool, len(out))
		for _, e := range out {
			if e.ID != "" {
				haveID[e.ID] = true
			}
		}
		for _, q := range pending {
			if haveID[q.ID] {
				continue
			}
			// Malformed question.json is mid-retry (pollQuestions's state
			// machine, dialog_poller.go): afm is silently nudging the agent
			// to rewrite it, or still waiting a tick to rule out a torn read.
			// Showing it here would leak that in-progress state to the user
			// before the poller itself has decided to give up — skip it;
			// once retries are exhausted the poller persists a real,
			// parseable stub and this same scan picks it up normally.
			if q.Malformed {
				continue
			}
			out = append(out, dialogUIEntry{
				Phase: q.Phase, ID: q.ID, Question: q.Question,
				Options: q.Options, AllowCustom: q.AllowCustom,
			})
			haveID[q.ID] = true
		}
	}
	return out
}

func questionUIEntry(phase string, e mcp.Entry) dialogUIEntry {
	return dialogUIEntry{
		ID: e.ID, Phase: phase, TS: e.TS, Question: e.Question,
		Options: e.Options, AllowCustom: e.AllowCustom,
		Answer: e.Answer, AnswerTS: e.AnswerTS, FromOptions: e.FromOptions,
		AutoAnswered: e.AutoAnswered,
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
	if !flow.IsValidPhase(req.Phase) {
		http.Error(w, "invalid phase", http.StatusBadRequest)
		return
	}
	// req.ID is embedded in the question/answer filenames, so it must be a
	// safe filename component — this guards against path traversal via a
	// crafted id (e.g. "../../foo").
	if !isValidDialogID(req.ID) {
		http.Error(w, "invalid question id", http.StatusBadRequest)
		return
	}
	stageDir := filepath.Join(s.runDir, stageID)
	questionPath := filepath.Join(stageDir, req.Phase+"."+req.ID+".question.json")
	answerPath := filepath.Join(stageDir, req.Phase+"."+req.ID+".answer.json")

	// Question must exist as a file written by the agent. We'll re-check this
	// after writing the answer to ensure the question didn't disappear (TOCTOU race).
	if _, err := os.Stat(questionPath); err != nil {
		http.Error(w, "question not found", http.StatusNotFound)
		return
	}

	// Read question.json to validate answer against options if allow_custom is false.
	var qf struct {
		Options     []string `json:"options,omitempty"`
		AllowCustom *bool    `json:"allow_custom"`
	}
	questionData, err := os.ReadFile(questionPath)
	if err != nil {
		http.Error(w, "read question: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := json.Unmarshal(questionData, &qf); err != nil {
		http.Error(w, "parse question: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Validate answer against options if custom answers are not allowed.
	// Default to allow_custom=true if not specified (protocol default).
	allowCustom := true
	if qf.AllowCustom != nil {
		allowCustom = *qf.AllowCustom
	}
	if !allowCustom {
		if len(qf.Options) == 0 {
			http.Error(w, "question has no options but allow_custom=false", http.StatusBadRequest)
			return
		}
		allowed := make(map[string]bool)
		for _, opt := range qf.Options {
			allowed[opt] = true
		}
		if !allowed[req.Answer] {
			http.Error(w, "answer not in allowed options", http.StatusBadRequest)
			return
		}
	}

	// Atomically write answer.json FIRST (mcp.WriteAnswer) so the agent's bash
	// loop can pick it up, then persist to dialog.jsonl for UI history
	// (best-effort inside WriteAnswer). This is the critical path: dialog
	// history must never be persisted before the agent's answer exists on disk.
	if err := mcp.WriteAnswer(stageDir, req.Phase, req.ID, req.Answer, req.FromOptions, false); err != nil {
		if os.IsExist(err) {
			http.Error(w, "question already answered", http.StatusConflict)
			return
		}
		http.Error(w, "write answer: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Re-check that the question file still exists to ensure we maintain the
	// invariant that answer.json only exists when its question.json exists.
	// Another goroutine could have deleted the question between our initial
	// check and now.
	if _, err := os.Stat(questionPath); err != nil {
		_ = os.Remove(answerPath)
		http.Error(w, "question disappeared during write", http.StatusBadRequest)
		return
	}

	if s.secondary != nil {
		if err := s.secondary.NotifyAnswer(stageID, req.Phase, req.ID, req.Answer, req.FromOptions); err != nil {
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
	if s.secondary != nil {
		if err := s.secondary.CancelDialog(stageID); err != nil {
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

// isValidDialogID validates a question id that is embedded in question/answer
// filenames (<phase>.<id>.{question,answer}.json). Same rules as a stage id:
// safe filename component, no path traversal.
func isValidDialogID(id string) bool {
	return isValidStageID(id)
}
