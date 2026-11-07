package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/akopichin/afm-engine/engine"
	implexecutor "github.com/akopichin/afm-engine/engine/inspection/executor"
	"github.com/akopichin/afm/contracts/accounting"
	"github.com/akopichin/afm/contracts/assistants"
	"github.com/akopichin/afm/contracts/config"
	"github.com/akopichin/afm/contracts/flow"
	"github.com/akopichin/afm/contracts/mcp"
	"github.com/akopichin/afm/contracts/state"
	"github.com/akopichin/afm/contracts/timezone"
)

const (
	keyStageID = "stage_id"
	keyStatus  = "status"
)

// writeCommandError renders a failed engine command: a machine-readable Code as the JSON error body,
// anything else as the plain message, with the HTTP status of the error kind.
func writeCommandError(w http.ResponseWriter, err error) {
	var ce *engine.Error
	if !errors.As(err, &ce) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	status := commandErrorStatus(ce.Kind)
	if ce.Code != "" {
		writeFlowError(w, status, ce.Code)
		return
	}
	http.Error(w, ce.Message, status)
}

func commandErrorStatus(kind engine.Kind) int {
	switch kind {
	case engine.KindInvalid:
		return http.StatusBadRequest
	case engine.KindNotFound:
		return http.StatusNotFound
	case engine.KindConflict:
		return http.StatusConflict
	case engine.KindClosed:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// writeStageStatus answers a successful stage command with {"status": status, "stage_id": id}.
func writeStageStatus(w http.ResponseWriter, status, stageID string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{keyStatus: status, keyStageID: stageID})
}

// statusResponse is GET /api/status's wire shape: run-level fields plus one ordered []StageView (see
// stageview.go) instead of five parallel per-stage maps the frontend used to re-join by id.
type statusResponse struct {
	FlowName string `json:"flow_name"`
	Version  string `json:"version,omitempty"`
	// RunID — id рана (имя run directory). Им же назван разговор бокового агента, а Team хранит по нему
	// курсоры непрочитанного, поэтому поле есть и без бокового агента.
	RunID                string          `json:"run_id,omitempty"`
	StartedAt            time.Time       `json:"started_at"`
	RunStatus            state.RunStatus `json:"run_status,omitempty"`
	EndedAt              *time.Time      `json:"ended_at,omitempty"`
	ElapsedAccumulatedMs int64           `json:"elapsed_accumulated_ms"`
	ElapsedSince         *time.Time      `json:"elapsed_since,omitempty"`
	Description          string          `json:"description,omitempty"`
	Stages               []StageView     `json:"stages"`
	LastSeq              uint64          `json:"last_seq"`
	IdleAccumulatedMs    int64           `json:"idle_accumulated_ms"`
	IdleSince            *time.Time      `json:"idle_since,omitempty"`
	BackoffAccumulatedMs int64           `json:"backoff_accumulated_ms"`
	BackoffOpenSince     []time.Time     `json:"backoff_open_since,omitempty"`
	Capabilities         capabilities    `json:"capabilities"`
	// CollaborationMode — режим представления collaboration ("team"|"assistants"). Всегда присутствует и не
	// зависит от capabilities.collaboration: dashboard по нему выбирает разделы, а сервер — доступность Board.
	CollaborationMode assistants.CollaborationMode `json:"collaboration_mode"`
	// UnknownCommandName is the label prefix the dashboard numbers commands with when a response has no
	// command_name; always present so the fallback never needs a client-side default.
	UnknownCommandName string `json:"unknown_command_name"`
	// HideAttachHelpers — подсказка dashboard скрыть кнопки ручного attach helper. Всегда присутствует и не
	// зависит от capabilities.collaboration.
	HideAttachHelpers bool `json:"hide_attach_helpers"`
	// GraphRevision (Этап 9) — the runtime stage registry's current revision; omitted when the add-stage
	// capability is off. The dashboard compares this against a previously-fetched preview's graph_revision to
	// detect staleness without a dedicated endpoint.
	GraphRevision int64 `json:"graph_revision,omitempty"`
	// FlowPauseState/FlowPausedStages surface the flow-wide review-pause round: "none"|"paused"|"resuming",
	// plus the ids of stages this round is holding paused.
	FlowPauseState   string   `json:"flow_pause_state"`
	FlowPausedStages []string `json:"flow_paused_stages,omitempty"`
	// TimeZone/UTCOffsetMinutes — host time zone for rendering message times: the IANA name (empty when the
	// zone is unknown — NOT a silent UTC) and the host's current signed UTC offset. The offset is a snapshot
	// at status time and only a fallback when the name is unavailable; DST history resolves from the name.
	TimeZone         string `json:"time_zone"`
	UTCOffsetMinutes int    `json:"utc_offset_minutes"`
	// RunCost/RunOverheadCost/CoverageIssues/Accounting — все четыре приходят из одного accounting.CostBundle
	// и опускаются целиком, когда Server.accounting == nil.
	RunCost         *accounting.CostView       `json:"run_cost,omitempty"`
	RunOverheadCost *accounting.CostView       `json:"run_overhead_cost,omitempty"`
	CoverageIssues  []accounting.CoverageIssue `json:"coverage_issues,omitempty"`
	Accounting      *accountingHealth          `json:"accounting,omitempty"`
	// Analytics — ревизия, режим и счётчики models/agents для обзора; данные деталей приходят из /api/usage/analytics.
	Analytics *UsageAnalyticsStatus `json:"analytics,omitempty"`
}

// accountingHealth forwards provider health and data presence; healthy storage can still have no coverage.
type accountingHealth struct {
	Health  string `json:"health"`
	HasData bool   `json:"has_data"`
	// ShowMoney сообщает фронтенду, показывать ли денежные ($) величины
	// (accounting.show_money). Токены отображаются всегда.
	ShowMoney bool `json:"show_money"`
}

// capabilities advertises optional dashboard features gated by server-side
// setup (e.g. whether the Docker project file browser is wired up).
type capabilities struct {
	FileBrowser   bool `json:"file_browser"`
	SideAgent     bool `json:"side_agent"`
	Collaboration bool `json:"collaboration"`
	// AddStage (Этап 9) — whether POST /api/run/graph/{preview|commit} is wired.
	AddStage bool `json:"add_stage"`
	// UsageAnalytics — подключён ли GET /api/usage/analytics (не «есть ли данные»).
	UsageAnalytics bool `json:"usage_analytics"`
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	rs := s.store.Snapshot()

	// One CostSnapshot call per request feeds both the per-stage views and the run-level fields below — never
	// two independent snapshots.
	var stageCosts map[string]*accounting.CostView
	resp := statusResponse{
		FlowName:             rs.FlowName,
		Version:              s.version,
		StartedAt:            rs.StartedAt,
		RunStatus:            rs.RunStatus,
		EndedAt:              rs.EndedAt,
		ElapsedAccumulatedMs: rs.ElapsedAccumulatedMs,
		ElapsedSince:         rs.ElapsedSince(),
		Description:          s.Description,
		LastSeq:              rs.LastSeq,
		IdleAccumulatedMs:    rs.IdleAccumulatedMs,
		IdleSince:            rs.IdleSince(),
		BackoffAccumulatedMs: rs.BackoffAccumulatedMs,
		BackoffOpenSince:     rs.BackoffOpenSince(),
		CollaborationMode:    s.collaborationMode,
		UnknownCommandName:   s.unknownCommandName,
		HideAttachHelpers:    s.hideAttachHelpers,
	}
	if s.accounting != nil {
		bundle := s.accounting.CostSnapshot()
		stageCosts = bundle.Stages
		resp.RunCost = bundle.Run
		resp.RunOverheadCost = bundle.Overhead
		resp.CoverageIssues = bundle.Issues
		resp.Accounting = &accountingHealth{Health: string(bundle.Health), HasData: bundle.HasData, ShowMoney: s.showMoney}
	}
	resp.Stages = buildStageViews(rs, s.runDir, s.stageConfigs(), stageCosts)
	for _, view := range resp.Stages {
		if v := view.Verify; v != nil {
			v.CommandName = verifyCommandName(v.Kind, v.Command, s.commandName)
		}
	}
	resp.Capabilities.FileBrowser = s.workspace != nil && len(s.workspace.Roots()) > 0
	resp.Capabilities.SideAgent = s.sideAgent != nil
	resp.Capabilities.Collaboration = s.assistantProfiles != nil
	resp.Capabilities.AddStage = s.graphActions != nil
	if s.usageAnalytics != nil {
		resp.Capabilities.UsageAnalytics = true
		facts := s.usageAnalytics.Facts()
		counts := facts.Counts()
		resp.Analytics = &UsageAnalyticsStatus{Revision: facts.Revision(), Mode: s.usageMode, Models: counts.Models, Agents: counts.Agents}
	}
	if s.graphActions != nil {
		resp.GraphRevision = s.graphActions.GraphRevision()
	}
	resp.RunID = s.runID()
	if s.reviewState != nil {
		st, owners := s.reviewState()
		resp.FlowPauseState = st
		resp.FlowPausedStages = owners
	} else {
		resp.FlowPauseState = reviewStateNone
	}
	// Resolve at status time: native runs use the local zone; Docker re-exec uses
	// the host transport, including its zone file for current fallback offset.
	var hostTZ timezone.Info
	if config.ReExecedIntoContainer() {
		hostTZ = timezone.FromTransport()
	} else {
		hostTZ = timezone.Host()
	}
	resp.TimeZone = hostTZ.Name
	resp.UTCOffsetMinutes = hostTZ.OffsetMinutes
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

// stageCommandID extracts and validates the stage id of /api/stages/{id}/<action>; on failure it has already
// written the response.
func stageCommandID(w http.ResponseWriter, r *http.Request, suffix string) (string, bool) {
	stageID := extractStageID(r.URL.Path, "/api/stages/", suffix)
	if !isValidStageID(stageID) {
		http.Error(w, "invalid stage id", http.StatusBadRequest)
		return "", false
	}
	return stageID, true
}

func (s *Server) handleApprove(w http.ResponseWriter, r *http.Request) {
	stageID, ok := stageCommandID(w, r, "/approve")
	if !ok {
		return
	}
	if err := s.actions.Approve(r.Context(), stageID); err != nil {
		writeCommandError(w, err)
		return
	}
	writeStageStatus(w, "approved", stageID)
}

type reviseRequest struct {
	Feedback string `json:"feedback"`
}

func (s *Server) handleRevise(w http.ResponseWriter, r *http.Request) {
	stageID, ok := stageCommandID(w, r, "/revise")
	if !ok {
		return
	}
	if err := s.actions.CheckRevise(stageID); err != nil {
		writeCommandError(w, err)
		return
	}
	var req reviseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if err := s.actions.Revise(r.Context(), stageID, req.Feedback); err != nil {
		writeCommandError(w, err)
		return
	}
	writeStageStatus(w, "revised", stageID)
}

type stageNoteRequest struct {
	Note string `json:"note"`
}

// handleStageNote прикрепляет pre-note к стадии, которая ещё не стартовала. Пустой текст = удалить
// заметку.
func (s *Server) handleStageNote(w http.ResponseWriter, r *http.Request) {
	stageID, ok := stageCommandID(w, r, "/note")
	if !ok {
		return
	}
	if err := s.actions.CheckPreNote(stageID); err != nil {
		writeCommandError(w, err)
		return
	}
	var req stageNoteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if err := s.actions.SetPreNote(stageID, req.Note); err != nil {
		writeCommandError(w, err)
		return
	}
	writeStageStatus(w, "noted", stageID)
}

type stageButtonRequest struct {
	Name string `json:"name"`
}

// handleStageButton accepts a declared label; prompts are resolved by the engine.
func (s *Server) handleStageButton(w http.ResponseWriter, r *http.Request) {
	stageID, ok := stageCommandID(w, r, "/button")
	if !ok {
		return
	}
	if err := s.actions.CheckButton(stageID); err != nil {
		writeCommandError(w, err)
		return
	}
	var req stageButtonRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if err := s.actions.Button(r.Context(), stageID, req.Name); err != nil {
		writeCommandError(w, err)
		return
	}
	writeStageStatus(w, "button", stageID)
}

func (s *Server) handleRetry(w http.ResponseWriter, r *http.Request) {
	stageID, ok := stageCommandID(w, r, "/retry")
	if !ok {
		return
	}
	if err := s.actions.Retry(r.Context(), stageID); err != nil {
		writeCommandError(w, err)
		return
	}
	writeStageStatus(w, "retried", stageID)
}

func (s *Server) handlePause(w http.ResponseWriter, r *http.Request) {
	stageID, ok := stageCommandID(w, r, "/pause")
	if !ok {
		return
	}
	if err := s.actions.Pause(r.Context(), stageID); err != nil {
		writeCommandError(w, err)
		return
	}
	writeStageStatus(w, reviewStatePaused, stageID)
}

func (s *Server) handleContinue(w http.ResponseWriter, r *http.Request) {
	stageID, ok := stageCommandID(w, r, "/continue")
	if !ok {
		return
	}
	if err := s.actions.Continue(r.Context(), stageID); err != nil {
		writeCommandError(w, err)
		return
	}
	writeStageStatus(w, "continued", stageID)
}

// handleRetryHook re-runs the retry cycle of a before/after hook currently blocked on a user decision.
func (s *Server) handleRetryHook(w http.ResponseWriter, r *http.Request) {
	stageID, ok := stageCommandID(w, r, "/retry-hook")
	if !ok {
		return
	}
	if s.secondary == nil {
		http.Error(w, "retry-hook not supported", http.StatusNotImplemented)
		return
	}
	if err := s.secondary.RetryHook(stageID); err != nil {
		writeCommandError(w, err)
		return
	}
	writeStageStatus(w, "retried", stageID)
}

// handleSkipHook skips a before/after hook currently blocked on a user
// decision. See handleRetryHook's comment on why there is no status check.
func (s *Server) handleSkipHook(w http.ResponseWriter, r *http.Request) {
	stageID, ok := stageCommandID(w, r, "/skip-hook")
	if !ok {
		return
	}
	if s.secondary == nil {
		http.Error(w, "skip-hook not supported", http.StatusNotImplemented)
		return
	}
	if err := s.secondary.SkipHook(stageID); err != nil {
		writeCommandError(w, err)
		return
	}
	writeStageStatus(w, "skipped", stageID)
}

func (s *Server) handleDialogGet(w http.ResponseWriter, r *http.Request) {
	stageID := extractStageID(r.URL.Path, "/api/stages/", "/dialog")
	if !isValidStageID(stageID) {
		http.Error(w, "invalid stage id", http.StatusBadRequest)
		return
	}
	stageDir := filepath.Join(s.runDir, stageID)
	out := buildDialogEntries(stageDir, s.stageConfig(stageID).Interactive)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// typeAgentText is the dialogUIEntry.Type value for agent text messages.
const typeAgentText = "agent_text"

// dialogUIEntry — элемент диалоговой ленты для UI. Type == typeAgentText означает текстовое сообщение
// агента (заполнен Text); пустой Type — вопрос/ответ из ask_user.
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

// buildDialogEntries собирает диалоговую ленту стейджа: вопросы/ответы из <phase>.dialog.jsonl,
// перемежённые текстовыми сообщениями агента из stream-json логов.
func buildDialogEntries(stageDir string, serialize bool) []dialogUIEntry {
	var out []dialogUIEntry
	for _, p := range flow.Phases() {
		entries, err := mcp.ReadDialog(mcp.DialogPath(stageDir, string(p)))
		if err != nil || len(entries) == 0 {
			continue
		}
		byID := make(map[string]mcp.Entry, len(entries))
		for _, e := range entries {
			byID[e.ID] = e
		}
		emitted := map[string]bool{}
		for _, logName := range flow.PhaseStreamLogs(p) {
			for _, it := range implexecutor.DialogTranscript(filepath.Join(stageDir, logName)) {
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
	// Гарантия видимости: текущий unanswered вопрос показываем ВСЕГДА, даже если poller ещё не дописал его в
	// <phase>.dialog.jsonl — читаем *.question.json напрямую.
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
			// Malformed question.json is mid-retry: afm is silently nudging the agent to rewrite it, or still
			// waiting a tick to rule out a torn read.
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
	if !serialize {
		return out // non-interactive: unchanged behavior (all pending shown)
	}

	// Interactive stages: at most ONE open question — the current (oldest
	// unanswered) valid one. Determine it once, fail closed on error.
	cur, hasCur, curErr := mcp.CurrentQuestion(stageDir)
	if curErr != nil {
		log.Printf("WARN: dialog current-question lookup failed for %s: %v", stageDir, curErr) //nolint:gosec // G706: stageDir built from a validated stageID (isValidStageID)
		hasCur = false
	}
	showCur := hasCur && !cur.Malformed

	// Is the current question already present as an UNANSWERED entry? (An
	// answered entry with a reused id must NOT block adding it.)
	haveUnansweredCur := false
	for _, e := range out {
		if e.Type != typeAgentText && e.Phase == cur.Phase && e.ID == cur.ID && e.Answer == nil {
			haveUnansweredCur = true
			break
		}
	}
	if showCur && !haveUnansweredCur {
		out = append(out, dialogUIEntry{
			Phase: cur.Phase, ID: cur.ID, Question: cur.Question,
			Options: cur.Options, AllowCustom: cur.AllowCustom,
		})
	}

	// Keep answered history + agent text; among unanswered questions keep exactly ONE — the current valid one
	// (first occurrence wins, dropping any transcript duplicates).
	filtered := out[:0]
	keptCur := false
	for _, e := range out {
		isUnanswered := e.Type != typeAgentText && e.ID != "" && e.Answer == nil
		if isUnanswered {
			isCur := showCur && e.Phase == cur.Phase && e.ID == cur.ID
			if !isCur || keptCur {
				continue
			}
			keptCur = true
		}
		filtered = append(filtered, e)
	}
	return filtered
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
	stageID, ok := stageCommandID(w, r, "/dialog/answer")
	if !ok {
		return
	}
	var req dialogAnswerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if err := s.actions.Answer(stageID, req.Phase, req.ID, req.Answer, req.FromOptions); err != nil {
		writeCommandError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{keyStatus: "ok"})
}

func (s *Server) handleDialogCancel(w http.ResponseWriter, r *http.Request) {
	stageID, ok := stageCommandID(w, r, "/dialog/cancel")
	if !ok {
		return
	}
	if err := s.actions.CancelDialog(stageID); err != nil {
		writeCommandError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{keyStatus: "cancelled"})
}

func extractStageID(path, prefix, suffix string) string {
	path = strings.TrimPrefix(path, prefix)
	path = strings.TrimSuffix(path, suffix)
	return path
}

// isValidStageID rejects path traversal and special characters in stage IDs.
func isValidStageID(id string) bool { return engine.ValidStageID(id) }

// safeArtifactNameRe restricts artifact filenames to a safe single component:
// alphanumerics, dash, underscore, dot. Mirrors safeStageIDRe.
var safeArtifactNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// isValidArtifactName gates the {name} segment of an artifact URL: non-empty, safe charset, no
// path-traversal ".." sequence.
func isValidArtifactName(name string) bool {
	if name == "" || strings.Contains(name, "..") {
		return false
	}
	return safeArtifactNameRe.MatchString(name)
}

// runID — id рана: имя run directory, а при боковом агенте — id его разговора (они совпадают).
func (s *Server) runID() string {
	if s.sideAgent != nil {
		return s.sideAgent.ConversationID()
	}
	if s.runDir != "" {
		return filepath.Base(s.runDir)
	}
	return ""
}
