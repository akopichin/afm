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
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/mcp"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/state"
)

const (
	keyStageID = "stage_id"
	keyStatus  = "status"
)

// writeActionError renders an error from a stage-control action (Approve/
// Revise/Retry/Pause/Continue/Button/RetryHook/SkipHook/CancelDialog). A
// review-pause rejection (ErrFlowPaused) becomes a machine-readable
// 409 {"error":"flow_paused"} — the same shape the /api/flow/* handlers use —
// so the dashboard can recognize "flow is held for review" instead of parsing a
// plain-text 500. Any other error keeps the caller's plain-text fallback.
func writeActionError(w http.ResponseWriter, err error, fallbackMsg string, fallbackStatus int) {
	if errors.Is(err, orchestrator.ErrFlowPaused) {
		writeFlowError(w, http.StatusConflict, "flow_paused")
		return
	}
	http.Error(w, fallbackMsg+": "+err.Error(), fallbackStatus)
}

// statusResponse is GET /api/status's wire shape: run-level fields plus one
// ordered []StageView (see stageview.go) instead of five parallel per-stage
// maps the frontend used to re-join by id.
type statusResponse struct {
	FlowName             string       `json:"flow_name"`
	StartedAt            time.Time    `json:"started_at"`
	Description          string       `json:"description,omitempty"`
	Stages               []StageView  `json:"stages"`
	LastSeq              uint64       `json:"last_seq"`
	IdleAccumulatedMs    int64        `json:"idle_accumulated_ms"`
	IdleSince            *time.Time   `json:"idle_since,omitempty"`
	BackoffAccumulatedMs int64        `json:"backoff_accumulated_ms"`
	BackoffOpenSince     []time.Time  `json:"backoff_open_since,omitempty"`
	Capabilities         capabilities `json:"capabilities"`
	// FlowPauseState/FlowPausedStages surface the flow-wide review-pause
	// round (see pkg/orchestrator/reviewpause.go): "none"|"paused"|"resuming",
	// plus the ids of stages this round is holding paused. Populated via
	// Config.ReviewState (a lock-free closure over the orchestrator's
	// reviewMarker) — nil means the server was wired without review-pause
	// support, in which case FlowPauseState is always "none".
	FlowPauseState   string   `json:"flow_pause_state"`
	FlowPausedStages []string `json:"flow_paused_stages,omitempty"`
	// RunCost/RunOverheadCost/CoverageIssues/Accounting — все четыре приходят
	// из одного accounting.CostBundle (один CostSnapshot() за запрос, см.
	// handleStatus) и опускаются целиком, когда Server.accounting == nil
	// (accounting вообще не подключён к этому серверу — отличается от
	// подключённого, но неработающего провайдера, у которого Accounting всё
	// равно присутствует со health:"unavailable").
	RunCost         *accounting.CostView       `json:"run_cost,omitempty"`
	RunOverheadCost *accounting.CostView       `json:"run_overhead_cost,omitempty"`
	CoverageIssues  []accounting.CoverageIssue `json:"coverage_issues,omitempty"`
	Accounting      *accountingHealth          `json:"accounting,omitempty"`
}

// accountingHealth surfaces CostBundle.Health/HasData as-is — the server does
// no interpretation of coverage from health (see CostBundle doc): a healthy
// provider can still report Coverage:"none" on a given CostView, and that is
// not a contradiction.
type accountingHealth struct {
	Health  string `json:"health"`
	HasData bool   `json:"has_data"`
}

// capabilities advertises optional dashboard features gated by server-side
// setup (e.g. whether the Docker project file browser is wired up).
type capabilities struct {
	FileBrowser bool `json:"file_browser"`
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	rs := s.store.Snapshot()

	// One CostSnapshot() call per request feeds both the per-stage views and
	// the run-level fields below — never two independent snapshots (which
	// could observe two different revisions of the ledger within the same
	// response).
	var stageCosts map[string]*accounting.CostView
	resp := statusResponse{
		FlowName:             rs.FlowName,
		StartedAt:            rs.StartedAt,
		Description:          s.Description,
		LastSeq:              rs.LastSeq,
		IdleAccumulatedMs:    rs.IdleAccumulatedMs,
		IdleSince:            rs.IdleSince(),
		BackoffAccumulatedMs: rs.BackoffAccumulatedMs,
		BackoffOpenSince:     rs.BackoffOpenSince(),
	}
	if s.accounting != nil {
		bundle := s.accounting.CostSnapshot()
		stageCosts = bundle.Stages
		resp.RunCost = bundle.Run
		resp.RunOverheadCost = bundle.Overhead
		resp.CoverageIssues = bundle.Issues
		resp.Accounting = &accountingHealth{Health: string(bundle.Health), HasData: bundle.HasData}
	}
	resp.Stages = buildStageViews(rs, s.runDir, s.stageInteractive, s.stageAutoApprove, s.stageIsScript, s.stageDependsOn, s.stageButtons, stageCosts)
	resp.Capabilities.FileBrowser = s.workspace != nil && len(s.workspace.Roots()) > 0
	if s.reviewState != nil {
		st, owners := s.reviewState()
		resp.FlowPauseState = st
		resp.FlowPausedStages = owners
	} else {
		resp.FlowPauseState = reviewStateNone
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
	allowed := st.Status == state.StatusAwaitingApproval || st.Status == state.StatusRunning
	if !allowed {
		http.Error(w, fmt.Sprintf("stage is %s, not awaiting_approval or running", st.Status), http.StatusBadRequest)
		return
	}
	// A script stage has no live agent to revise; Revise would move it to
	// `revising`, after which script completion is dropped (completeStage
	// rejects `revising`) and the stage would hang. Mirror the same guard
	// handleStageButton/handleStageNote apply. Defense-in-depth behind the
	// client-side !isScript gate on the feed composer.
	if s.stageIsScript[stageID] {
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
	// A no-op Revise (the stage left the running/awaiting_approval window
	// between our snapshot and the action, or the CAS was lost) delivered
	// nothing — report a conflict so the caller (feed composer) keeps the
	// typed text instead of clearing it as if the note had been sent.
	if !applied {
		http.Error(w, "stage no longer accepts a note", http.StatusConflict)
		return
	}
	// Surface the delivered note as a right-side feed line, mirroring
	// dialog_answer (T2): publish live so an already-connected dashboard sees
	// it, and duplicate into notices.jsonl so a client that connects/reloads
	// afterwards still sees it via /api/events' notices replay. The seq is the
	// note's unique id — the SAME payload object goes to both sinks so live and
	// replay reconcile in use-event-feed (see bus.EventAgentNote).
	payload := map[string]any{"id": seq, "text": mcp.DialogSnippet(req.Feedback)}
	s.uiBus.Publish(bus.Event{Type: bus.EventAgentNote, StageID: stageID, Data: payload})
	stagefiles.AppendNotice(s.runDir, stageID, string(bus.EventAgentNote), payload)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{keyStatus: "revised", keyStageID: stageID})
}

type stageNoteRequest struct {
	Note string `json:"note"`
}

// handleStageNote прикрепляет (или очищает) pre-note к стадии, которая ещё не
// стартовала. В отличие от handleRevise (заметка живому агенту → FSM-переход +
// SIGINT) здесь нет никакого FSM: стадия остаётся pending, заметка просто
// пишется в prenote.md и вклеится в контекст агента на старте
// (см. (*Orchestrator).preNoteBlock). Пустой текст = удалить заметку.
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
	if s.stageIsScript[stageID] {
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
	// Стадия могла стартовать между проверкой pending выше и записью prenote.md,
	// прочитав ещё пустую заметку (finding #9): тогда заметка на диске уже не
	// попадёт в первый промпт агента. Честно сообщаем об этом (409), а не молча
	// возвращаем 200 «noted» — клиент (AgentNoteModal) на ошибке не закрывает
	// модалку молча.
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

// handleStageButton fires a predefined kebab-menu button: it resolves the
// button by its declared label (never trusting a client-supplied prompt) and
// routes it through the orchestrator's Button → Revise path — same effect as a
// free-text note to the live agent. Gated exactly like handleRevise
// (running/awaiting_approval, non-script) plus a check that the name is a
// button actually declared for this stage.
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
	if s.stageIsScript[stageID] {
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
	if !slices.Contains(s.stageButtons[stageID], req.Name) {
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
	out := buildDialogEntries(stageDir, s.stageInteractive[stageID])
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
func buildDialogEntries(stageDir string, serialize bool) []dialogUIEntry {
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

	// Keep answered history + agent text; among unanswered questions keep
	// exactly ONE — the current valid one (first occurrence wins, dropping any
	// transcript duplicates).
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
	// req.ID is embedded in the question/answer filenames, so it must be a
	// safe filename component — this guards against path traversal via a
	// crafted id (e.g. "../../foo").
	if !isValidDialogID(req.ID) {
		http.Error(w, "invalid question id", http.StatusBadRequest)
		return
	}
	// Flow-wide review-pause guard must run BEFORE any durable mutation. If the
	// flow is held for review, writing answer.json first (and only discovering
	// the pause afterwards, inside NotifyAnswer) leaves the answer on disk while
	// returning an error: the question poller then stops treating the question
	// as unanswered, no resume event is published, and a non-owned
	// awaiting_user_input stage hangs forever. Reject up front with a
	// machine-readable 409 instead.
	if s.currentReviewState() != reviewStateNone {
		writeFlowError(w, http.StatusConflict, "flow_paused")
		return
	}

	stageDir := filepath.Join(s.runDir, stageID)

	// Serialize answers for interactive stages only: an agent's polling loop
	// blocks on the first question it wrote; answering a later one leaves the
	// earlier answer.json missing and deadlocks the agent. A well-behaved client
	// only shows the current question (buildDialogEntries), so this guards
	// stale/direct clients. Non-interactive stages auto-answer and are exempt.
	if s.stageInteractive[stageID] {
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

	// Atomically write ONLY answer.json first (mcp.WriteAnswerFile) so the
	// agent's bash loop can pick it up. The dialog.jsonl history entry is
	// deliberately deferred until AFTER NotifyAnswer authorizes the answer:
	// if a review pause started in the window past the up-front guard,
	// NotifyAnswer rejects with ErrFlowPaused and we roll back answer.json —
	// and because dialog.jsonl was never touched, the question does not end up
	// recorded as answered-in-history while unanswered-on-disk (which would
	// strand the stage in awaiting_user_input with no visible form until an afm
	// restart).
	if err := mcp.WriteAnswerFile(stageDir, req.Phase, req.ID, req.Answer, req.FromOptions); err != nil {
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
			// A review pause that started in the tiny window between the guard
			// above and this call: roll back answer.json (dialog.jsonl was not
			// yet written) so nothing durable records this answer, and report the
			// same machine-readable 409 the up-front guard would have.
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
	dialogPath := filepath.Join(stageDir, req.Phase+".dialog.jsonl")
	if err := mcp.AppendAnswer(dialogPath, mcp.Answer{ID: req.ID, Answer: req.Answer, FromOptions: req.FromOptions}); err != nil {
		log.Printf("WARN: persist dialog answer for %s/%s.%s: %v (answer.json already written)", stageDir, req.Phase, req.ID, err) //nolint:gosec // G706: phase/id validated safe (flow.IsValidPhase/isValidDialogID) above
	}

	// The human answer took effect — mirror dialog_question (T2) on the
	// answer side: one payload map, published live on the ui bus (so an
	// already-connected dashboard sees it without waiting for a reconnect)
	// and duplicated into notices.jsonl (so a client that connects/reloads
	// afterwards still sees it via /api/events' notices replay).
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

// isValidDialogID validates a question id that is embedded in question/answer
// filenames (<phase>.<id>.{question,answer}.json). Same rules as a stage id:
// safe filename component, no path traversal.
func isValidDialogID(id string) bool {
	return isValidStageID(id)
}
