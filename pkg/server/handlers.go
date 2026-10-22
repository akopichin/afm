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
	"slices"
	"strings"
	"time"

	"github.com/akopichin/afm/pkg/accounting"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/mcp"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/state"
	"github.com/akopichin/afm/pkg/timezone"
)

const (
	keyStageID = "stage_id"
	keyStatus  = "status"
)

// writeActionError renders an error from a stage-control action. Any other error keeps the caller's
// plain-text fallback.
func writeActionError(w http.ResponseWriter, err error, fallbackMsg string, fallbackStatus int) {
	if errors.Is(err, orchestrator.ErrFlowPaused) {
		writeFlowError(w, http.StatusConflict, "flow_paused")
		return
	}
	if errors.Is(err, orchestrator.ErrNotApplied) {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	http.Error(w, fallbackMsg+": "+err.Error(), fallbackStatus)
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
	// зависит от capabilities.collaboration: это только подсказка dashboard, какие разделы показывать.
	CollaborationMode assistants.CollaborationMode `json:"collaboration_mode"`
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
	resp.Capabilities.FileBrowser = s.workspace != nil && len(s.workspace.Roots()) > 0
	resp.Capabilities.SideAgent = s.sideAgent != nil
	resp.Capabilities.Collaboration = s.assistantProfiles != nil
	resp.Capabilities.AddStage = s.graphActions != nil
	if s.graphActions != nil {
		resp.GraphRevision = s.graphActions.GraphRevision()
	}
	if s.runDir != "" {
		resp.RunID = filepath.Base(s.runDir)
	}
	if s.sideAgent != nil {
		resp.RunID = s.sideAgent.ConversationID()
	}
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
		writeActionError(w, err, "approve failed", http.StatusInternalServerError)
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
	// The statuses Orchestrator.Revise acts on; awaiting_assistants — the author finished and waits for its
	// helpers, a note reaches it only this way.
	allowed := st.Status == state.StatusAwaitingApproval || st.Status == state.StatusRunning || st.Status == state.StatusAwaitingAssistants
	if !allowed {
		http.Error(w, fmt.Sprintf("stage is %s, not running, awaiting_approval or awaiting_assistants", st.Status), http.StatusBadRequest)
		return
	}
	// A script stage has no live agent to revise; Revise would move it to `revising`, after which script
	// completion is dropped and the stage would hang.
	if s.stageConfig(stageID).IsScript {
		http.Error(w, "script stage has no agent to note", http.StatusBadRequest)
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
	applied, seq, err := s.actions.Revise(r.Context(), stageID, req.Feedback)
	if err != nil {
		writeActionError(w, err, "revise failed", http.StatusInternalServerError)
		return
	}
	// A no-op Revise delivered nothing — report a conflict so the caller keeps the typed text instead of
	// clearing it as if the note had been sent.
	if !applied {
		http.Error(w, "stage no longer accepts a note", http.StatusConflict)
		return
	}
	// Publish one identical note payload live and durably; transition seq identifies the note for replay
	// deduplication.
	payload := map[string]any{"id": seq, "text": mcp.DialogSnippet(req.Feedback)}
	s.uiBus.Publish(bus.Event{Type: bus.EventAgentNote, StageID: stageID, Data: payload})
	stagefiles.AppendNotice(s.runDir, stageID, string(bus.EventAgentNote), payload)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{keyStatus: "revised", keyStageID: stageID})
}

type stageNoteRequest struct {
	Note string `json:"note"`
}

// handleStageNote прикрепляет pre-note к стадии, которая ещё не стартовала. Пустой текст = удалить
// заметку.
func (s *Server) handleStageNote(w http.ResponseWriter, r *http.Request) {
	stageID := extractStageID(r.URL.Path, "/api/stages/", "/note")
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
	if st.Status != state.StatusPending {
		http.Error(w, fmt.Sprintf("stage is %s, not pending", st.Status), http.StatusBadRequest)
		return
	}
	if s.stageConfig(stageID).IsScript {
		http.Error(w, "script stage has no agent to note", http.StatusBadRequest)
		return
	}
	var req stageNoteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	stageDir := filepath.Join(s.runDir, stageID)
	if err := state.SavePreNote(stageDir, req.Note); err != nil {
		http.Error(w, "save note failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Стадия могла стартовать между проверкой pending выше и записью prenote.md, прочитав ещё пустую заметку:
	// тогда заметка на диске уже не попадёт в первый промпт агента.
	if s.store.Snapshot().Stages[stageID].Status != state.StatusPending {
		http.Error(w, "stage started before the note was saved; it may not reach the first prompt", http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{keyStatus: "noted", keyStageID: stageID})
}

type stageButtonRequest struct {
	Name string `json:"name"`
}

// handleStageButton accepts a declared label and delegates to Button → Revise; prompts are resolved
// server-side.
func (s *Server) handleStageButton(w http.ResponseWriter, r *http.Request) {
	stageID := extractStageID(r.URL.Path, "/api/stages/", "/button")
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
	if s.stageConfig(stageID).IsScript {
		http.Error(w, "script stage has no agent", http.StatusBadRequest)
		return
	}
	var req stageButtonRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		http.Error(w, "button name is required", http.StatusBadRequest)
		return
	}
	if !slices.Contains(s.stageConfig(stageID).Buttons, req.Name) {
		http.Error(w, "unknown button", http.StatusBadRequest)
		return
	}
	if err := s.actions.Button(r.Context(), stageID, req.Name); err != nil {
		writeActionError(w, err, "button failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{keyStatus: "button", keyStageID: stageID})
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
		writeActionError(w, err, "retry failed", http.StatusInternalServerError)
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
	// awaiting_assistants/awaiting_peer: see Orchestrator.Pause's matching comments (control_api.go).
	case state.StatusRunning, state.StatusPlanning, state.StatusRevising, state.StatusRetrying, state.StatusAwaitingAssistants, state.StatusAwaitingPeer:
	default:
		http.Error(w, fmt.Sprintf("stage is %s, cannot be paused", st.Status), http.StatusBadRequest)
		return
	}
	if s.stageConfig(stageID).IsScript && st.Status == state.StatusRunning {
		http.Error(w, "pause is not supported mid-script execution", http.StatusConflict)
		return
	}

	if err := s.actions.Pause(r.Context(), stageID); err != nil {
		writeActionError(w, err, "pause failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{keyStatus: reviewStatePaused, keyStageID: stageID})
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
		writeActionError(w, err, "continue failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{keyStatus: "continued", keyStageID: stageID})
}

// handleRetryHook re-runs the retry cycle of a before/after hook currently blocked on a user decision.
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
		writeActionError(w, err, "retry-hook failed", http.StatusBadRequest)
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
		writeActionError(w, err, "skip-hook failed", http.StatusBadRequest)
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
	// req.ID is embedded in the question/answer filenames, so it must be a safe filename component — this
	// guards against path traversal via a crafted id (e.g. "../../foo").
	if !isValidDialogID(req.ID) {
		http.Error(w, "invalid question id", http.StatusBadRequest)
		return
	}
	// Flow-wide review-pause guard must run BEFORE any durable mutation. Reject up front with a
	// machine-readable 409 instead.
	if s.currentReviewState() != reviewStateNone {
		writeFlowError(w, http.StatusConflict, "flow_paused")
		return
	}

	stageDir := filepath.Join(s.runDir, stageID)

	// Interactive answers must follow question order or the polling agent deadlocks; non-interactive
	// auto-answers are exempt.
	if s.stageConfig(stageID).Interactive {
		cur, hasCur, err := mcp.CurrentQuestion(stageDir)
		if err != nil {
			writeFlowError(w, http.StatusInternalServerError, "current_question_lookup_failed")
			return
		}
		if hasCur && (cur.Phase != req.Phase || cur.ID != req.ID) {
			writeFlowError(w, http.StatusConflict, "answer_out_of_order")
			return
		}
	}

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

	// Atomically write ONLY answer.json first so the agent's bash loop can pick it up.
	if err := mcp.WriteAnswerFile(stageDir, req.Phase, req.ID, req.Answer, req.FromOptions); err != nil {
		if os.IsExist(err) {
			http.Error(w, "question already answered", http.StatusConflict)
			return
		}
		http.Error(w, "write answer: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Re-check that the question file still exists to ensure we maintain the invariant that answer.json only
	// exists when its question.json exists.
	if _, err := os.Stat(questionPath); err != nil {
		_ = os.Remove(answerPath)
		http.Error(w, "question disappeared during write", http.StatusBadRequest)
		return
	}

	if s.secondary != nil {
		if err := s.secondary.NotifyAnswer(stageID, req.Phase, req.ID, req.Answer, req.FromOptions); err != nil {
			// Review-pause won the race: roll back answer.json before writing dialog history and return the same
			// 409 as the initial guard.
			if errors.Is(err, orchestrator.ErrFlowPaused) {
				_ = os.Remove(answerPath)
				writeFlowError(w, http.StatusConflict, "flow_paused")
				return
			}
			http.Error(w, "notify: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	// The answer is authorized and durable on disk — now record it in the
	// human-facing dialog history (best-effort, same as mcp.WriteAnswer does).
	dialogPath := mcp.DialogPath(stageDir, req.Phase)
	if err := mcp.AppendAnswer(dialogPath, mcp.Answer{ID: req.ID, Answer: req.Answer, FromOptions: req.FromOptions}); err != nil {
		log.Printf("WARN: persist dialog answer for %s/%s.%s: %v (answer.json already written)", stageDir, req.Phase, req.ID, err) //nolint:gosec // G706: phase/id validated safe (flow.IsValidPhase/isValidDialogID) above
	}

	// The human answer took effect — mirror dialog_question on the answer side: one payload map, published
	// live on the ui bus and duplicated into notices.jsonl.
	payload := mcp.DialogFeedNotice(req.Phase, req.ID, mcp.DialogSnippet(req.Answer))
	s.uiBus.Publish(bus.Event{Type: bus.EventDialogAnswer, StageID: stageID, Data: payload})
	stagefiles.AppendNotice(s.runDir, stageID, string(bus.EventDialogAnswer), payload)

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
			writeActionError(w, err, "cancel", http.StatusInternalServerError)
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

// isValidDialogID validates a question id that is embedded in question/answer filenames. Same rules as a
// stage id: safe filename component, no path traversal.
func isValidDialogID(id string) bool {
	return isValidStageID(id)
}
