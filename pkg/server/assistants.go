package server

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
	"github.com/akopichin/afm/pkg/redact"
	"github.com/akopichin/afm/pkg/state"
)

// ErrAssistantsUnavailable is returned by an AssistantActions/AssistantsReader implementation when the
// underlying service cannot currently accept the request at all (e.g. the run store is shutting down) —
// distinct from a validation rejection; maps to 503.
var ErrAssistantsUnavailable = errors.New("assistants: service unavailable")

// errCodeInternal — the error code of every unexpected server-side failure.
const errCodeInternal = "internal"

// AssistantTaskView is one task's dashboard-facing projection — GET /api/stages/{id}/assistants.
type AssistantTaskView struct {
	ID           string `json:"id"`
	ParentTaskID string `json:"parent_task_id,omitempty"`
	ProfileID    string `json:"profile_id"`
	Title        string `json:"title,omitempty"`
	Mode         string `json:"mode"`
	Trigger      string `json:"trigger"`
	Status       string `json:"status"`
	StatusReason string `json:"status_reason,omitempty"`
	// Depth (T4a.8) — 1 for a top-level task, parent.Depth+1 for each delegated level below it
	// (assistants.ComputeDepth). The dashboard's breadcrumb uses this plus the run's max_depth (already
	// surfaced separately by GET /api/assistant-profiles) to show "depth N of max_depth".
	Depth int `json:"depth"`
	// ReadonlyEnforcement — which read-only guarantee (assistants.ReadonlyEnforcement: "os"/"none") was
	// backing this task's profile at the moment it was admitted (frozen into ProfileSnapshot by
	// orchestrator.Attach, see pkg/assistants/types.go's Profile.ReadonlyEnforcement) — not the profile's
	// CURRENT availability, which may have since changed.
	ReadonlyEnforcement string `json:"readonly_enforcement,omitempty"`
	// WatchPasses/WatchLastCheckpointSeq (T5.6) — only set for a checkpoints/on_completion watcher task
	// (Trigger). nil for a plain one_shot task: it was never bound as a watcher, so there is nothing to show.
	// WatchPasses is how many turns the watcher's collector has fired so far (0 before its first checkpoint).
	// WatchLastCheckpointSeq is the durable cursor's "processed up to" position (pkg/watch.CheckpointStore) —
	// absent until the FIRST checkpoint is actually committed, never a fabricated value.
	WatchPasses            *int `json:"watch_passes,omitempty"`
	WatchLastCheckpointSeq *int `json:"watch_last_checkpoint_seq,omitempty"`
	// BlocksRootCompletion (Этап 7, T7.5) — whether an UNRESOLVED instance of this task would block the ROOT
	// STAGE's own completion (assistants.BlocksRootCompletion: true only if every edge from this task up to the
	// stage is "required" — one advisory link anywhere in the chain breaks it for everything below). A task
	// with Mode=="required" and BlocksRootCompletion==false blocks only its immediate parent helper
	// (ParentTaskID), never the stage directly — the dashboard renders "blocks stage" vs "blocks helper <X>"
	// from this plus Mode/ParentTaskID (§4.3). Purely structural (mode-chain only) — does not account for a
	// stale/old task-tree generation, which the durable barrier (RequiredBlocker) already excludes on its own.
	BlocksRootCompletion bool `json:"blocks_root_completion"`
	// Waived/WaiverReason (Этап 7, T7.6) — true once a human has explicitly accepted the risk of this task
	// never closing its own gate (POST /api/assistants/{id}/waive). A waived required task's gate reads as
	// CLOSED to the durable barrier regardless of Status/verdict — the dashboard must not keep showing it as
	// "blocks stage"/"blocks helper X" once Waived is true, or render it as outstanding work.
	Waived       bool   `json:"waived,omitempty"`
	WaiverReason string `json:"waiver_reason,omitempty"`
}

// AssistantEventView is one entry of a task's event history page — GET /api/assistants/{id}/events.
type AssistantEventView struct {
	Seq  uint64 `json:"seq"`
	Type string `json:"type"`
	Data any    `json:"data,omitempty"`
}

// AssistantsReader is the read-side service for collaboration data: a stage's task tree, and one task's
// event history page. nil = capability off (same convention as AssistantActions/assistantProfiles): GET on
// these routes 404s, exactly like POST does when AssistantActions is nil.
type AssistantsReader interface {
	// TaskTree returns every task rooted at stageID, normalized (each task id appears exactly once, nested
	// under its parent — never duplicated as a second top-level entry), in a stable deterministic order.
	TaskTree(stageID string) ([]AssistantTaskView, error)
	// TaskEvents returns a bounded page of events for taskID strictly AFTER cursor, in stable (seq) order,
	// plus the cursor to pass for the next page and whether more are available.
	TaskEvents(taskID string, cursor uint64, limit int) (events []AssistantEventView, nextCursor uint64, hasMore bool, err error)
}

const defaultAssistantEventsLimit = 100
const maxAssistantEventsLimit = 500

// maxAttachBodyBytes bounds the Attach request body — same discipline as the side-agent/flow-notes free-text
// endpoints (pkg/server/side_agent.go's maxSideAgentBodyBytes, pkg/server/flow_handlers.go's
// maxNoteBodyBytes): title/instruction are free text, so an unbounded body would let a client force an
// arbitrarily large read/allocation before validation ever runs.
const maxAttachBodyBytes = 64 << 10

// routeStageAssistants handles GET|POST /api/stages/{id}/assistants, dispatched from routeStages.
func (s *Server) routeStageAssistants(w http.ResponseWriter, r *http.Request, stageID string) {
	switch r.Method {
	case http.MethodGet:
		s.handleListAssistants(w, r, stageID)
	case http.MethodPost:
		s.handleAttachAssistant(w, r, stageID)
	default:
		writeAssistantErrCode(w, http.StatusMethodNotAllowed, "method_not_allowed")
	}
}

func (s *Server) handleListAssistants(w http.ResponseWriter, r *http.Request, stageID string) {
	if s.assistantsReader == nil {
		http.NotFound(w, r)
		return
	}
	if !isValidStageID(stageID) {
		writeAssistantErrCode(w, http.StatusBadRequest, "invalid_stage_id")
		return
	}
	tree, err := s.assistantsReader.TaskTree(stageID)
	if err != nil {
		writeAssistantError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"tasks": tree})
}

func (s *Server) handleAttachAssistant(w http.ResponseWriter, r *http.Request, stageID string) {
	if s.assistantActions == nil {
		http.NotFound(w, r)
		return
	}
	if !sameOriginOK(r) {
		writeAssistantErrCode(w, http.StatusForbidden, "forbidden_origin")
		return
	}
	if !isValidStageID(stageID) {
		writeAssistantErrCode(w, http.StatusBadRequest, "invalid_stage_id")
		return
	}

	// Decode into a struct that carries ONLY the fields the client is trusted to supply. Any extra JSON
	// field the client sends (command/args/env/cwd/depth/root_stage_id/...) is simply never read — there is
	// no field here for it to land in, so it cannot influence anything downstream.
	var body struct {
		RequestID   string `json:"request_id"`
		ProfileID   string `json:"profile_id"`
		Title       string `json:"title"`
		Instruction string `json:"instruction"`
		Mode        string `json:"mode"`
		Trigger     string `json:"trigger"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAttachBodyBytes)).Decode(&body); err != nil {
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

	taskID, err := s.assistantActions.Attach(r.Context(), stageID, AttachRequest{
		RequestID: body.RequestID, ProfileID: body.ProfileID, Title: body.Title,
		Instruction: body.Instruction, Mode: body.Mode, Trigger: body.Trigger,
	})
	if err != nil {
		writeAssistantError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"task_id": taskID})
}

// Task action names — shared between routeAssistants' routing switch and handleAssistantTaskAction's
// dispatch switch, so the two can never silently drift apart.
const (
	taskActionPause    = "pause"
	taskActionContinue = "continue"
	taskActionCancel   = "cancel"
	// taskActionWaive (Этап 7, T7.6) is the ONE task action with a JSON body ({"reason":"..."}) — routed
	// separately below since the other three take no body at all.
	taskActionWaive = "waive"
)

// routeAssistants handles /api/assistants/{id}/{pause|continue|cancel|events}.
func (s *Server) routeAssistants(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/assistants/")
	segs := strings.Split(path, "/")
	if len(segs) != 2 || segs[0] == "" || segs[1] == "" {
		http.NotFound(w, r)
		return
	}
	taskID, action := segs[0], segs[1]
	if !isValidStageID(taskID) { // same safe-path-component charset as a stage id
		writeAssistantErrCode(w, http.StatusBadRequest, "invalid_task_id")
		return
	}

	switch {
	case action == "events" && r.Method == http.MethodGet:
		s.handleAssistantEvents(w, r, taskID)
	case action == "raw" && r.Method == http.MethodGet:
		s.handleAssistantRawResult(w, r, taskID)
	case action == taskActionWaive && r.Method == http.MethodPost:
		s.handleWaiveTask(w, r, taskID)
	case action == taskActionPause && r.Method == http.MethodPost,
		action == taskActionContinue && r.Method == http.MethodPost,
		action == taskActionCancel && r.Method == http.MethodPost:
		// Dispatch by NAME, not by forming a method value on s.assistantActions here: in Go, forming a
		// method value on a nil interface (e.g. `s.assistantActions.PauseTask` when assistantActions is
		// nil) panics IMMEDIATELY at the point of formation, before handleAssistantTaskAction ever gets a
		// chance to run its own nil check. AssistantsReader-wired-but-AssistantActions-nil (read-only
		// collaboration) is an explicitly valid, tested configuration (TestPauseTask_404WhenActionsOffButReaderOn)
		// — it must 404, not crash the handler.
		s.handleAssistantTaskAction(w, r, taskID, action)
	default:
		http.NotFound(w, r)
	}
}

// handleWaiveTask (Этап 7, T7.6) is routed separately from pause/continue/cancel because it is the one task
// action that takes a JSON body ({"reason":"..."}) — the actor is NEVER read from that body (only the
// authenticated dashboard operator can waive, stamped by the adapter; see collaborationActions.WaiveTask).
func (s *Server) handleWaiveTask(w http.ResponseWriter, r *http.Request, taskID string) {
	if s.assistantActions == nil {
		http.NotFound(w, r)
		return
	}
	if !sameOriginOK(r) {
		writeAssistantErrCode(w, http.StatusForbidden, "forbidden_origin")
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAttachBodyBytes)).Decode(&body); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeAssistantErrCode(w, http.StatusRequestEntityTooLarge, "request_too_large")
			return
		}
		writeAssistantErrCode(w, http.StatusBadRequest, "invalid_request_body")
		return
	}
	if strings.TrimSpace(body.Reason) == "" {
		writeAssistantErrCode(w, http.StatusBadRequest, "reason_required")
		return
	}
	if err := s.assistantActions.WaiveTask(r.Context(), taskID, body.Reason); err != nil {
		writeAssistantError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{keyStatus: "ok"})
}

func (s *Server) handleAssistantTaskAction(w http.ResponseWriter, r *http.Request, taskID, action string) {
	if s.assistantActions == nil {
		http.NotFound(w, r)
		return
	}
	if !sameOriginOK(r) {
		writeAssistantErrCode(w, http.StatusForbidden, "forbidden_origin")
		return
	}
	var err error
	switch action {
	case taskActionPause:
		err = s.assistantActions.PauseTask(r.Context(), taskID)
	case taskActionContinue:
		err = s.assistantActions.ContinueTask(r.Context(), taskID)
	case taskActionCancel:
		err = s.assistantActions.CancelTask(r.Context(), taskID)
	default:
		// Unreachable in production: routeAssistants only calls this with one of the three names above,
		// already validated against r.Method there. Fail closed rather than silently report "ok" with a
		// nil err for some future action name a routing change might introduce without updating this switch.
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeAssistantError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{keyStatus: "ok"})
}

func (s *Server) handleAssistantEvents(w http.ResponseWriter, r *http.Request, taskID string) {
	if s.assistantsReader == nil {
		http.NotFound(w, r)
		return
	}
	cursor := parseUintQuery(r, "cursor")
	limit := defaultAssistantEventsLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= maxAssistantEventsLimit {
			limit = n
		}
	}
	events, nextCursor, hasMore, err := s.assistantsReader.TaskEvents(taskID, cursor, limit)
	if err != nil {
		writeAssistantError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"events": events, "next_cursor": nextCursor, "has_more": hasMore,
	})
}

// writeAssistantError renders an error from an AssistantActions/AssistantsReader call as
// {"error":<code>} with the appropriate status: 409 for stale_revision/invalid_status/flow_paused, 422 for
// every other admission/validation rejection (capability_unavailable, delegation_depth_exceeded,
// invalid_parent, ancestor_inactive), 404 for feature_disabled, 503 when the service reports itself
// unavailable outright.
func writeAssistantError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrAssistantsUnavailable) {
		writeAssistantErrCode(w, http.StatusServiceUnavailable, "service_unavailable")
		return
	}
	var rej *state.OperationRejectedError
	if errors.As(err, &rej) {
		writeAssistantErrMessage(w, assistantErrStatus(rej.Code), string(rej.Code), rej.Reason)
		return
	}
	// Этап 10 (T10.1): a delivered add_stage proposal can ALSO fail with a *graph.ValidationError — the exact
	// same error shape /api/run/graph/commit already renders via graphRejectedStatus (graph.go). Proposal
	// delivery deliberately reuses orchestrator.CommitAddStage verbatim (not a hand-rolled copy), so its
	// errors must be rendered with the SAME fidelity here, not fall through to a bare 500.
	var ve *graph.ValidationError
	if errors.As(err, &ve) {
		writeAssistantErrCode(w, graphRejectedStatus(ve.Code), ve.Code)
		return
	}
	// Unexpected: never a silent 500 — the error goes to the afm log, the client is pointed there.
	log.Printf("ERROR: assistants/mesh request failed: %q", redact.String(err.Error(), executor.SecretValuesForRedaction())) //nolint:gosec // G706: secrets redacted, %q escapes newlines so a value cannot forge log lines
	writeAssistantErrMessage(w, http.StatusInternalServerError, errCodeInternal, "internal error — see the afm log for details")
}

func assistantErrStatus(code assistants.ErrorCode) int {
	switch code {
	case assistants.CodeStaleRevision, assistants.CodeInvalidStatus, assistants.CodeFlowPaused, assistants.CodeBudgetExceeded, agentcomm.CodeWaitCycle:
		return http.StatusConflict
	case assistants.CodeFeatureDisabled, assistants.CodeNotFound:
		return http.StatusNotFound
	case assistants.CodeRunFinalizing:
		return http.StatusServiceUnavailable
	case assistants.CodeCapabilityUnavailable, assistants.CodeDelegationDepthExceeded, assistants.CodeInvalidParent, assistants.CodeAncestorInactive, assistants.CodeExecutorNotPermitted:
		return http.StatusUnprocessableEntity
	case assistants.CodeActorNotAllowed:
		return http.StatusForbidden
	case assistants.CodeInvalidRequest:
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

func writeAssistantErrCode(w http.ResponseWriter, status int, code string) {
	writeAssistantErrMessage(w, status, code, "")
}

// writeAssistantErrMessage writes {"error": code, "message": reason}; message is omitted when empty.
func writeAssistantErrMessage(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body := map[string]string{"error": code}
	if message != "" {
		body["message"] = message
	}
	_ = json.NewEncoder(w).Encode(body)
}
