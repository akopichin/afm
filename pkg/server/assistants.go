package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/state"
)

// ErrAssistantsUnavailable is returned by an AssistantActions/AssistantsReader implementation when the
// underlying service cannot currently accept the request at all (e.g. the run store is shutting down) —
// distinct from a validation rejection; maps to 503.
var ErrAssistantsUnavailable = errors.New("assistants: service unavailable")

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

// routeStageAssistants handles GET|POST /api/stages/{id}/assistants, dispatched from routeStages.
func (s *Server) routeStageAssistants(w http.ResponseWriter, r *http.Request, stageID string) {
	switch r.Method {
	case http.MethodGet:
		s.handleListAssistants(w, r, stageID)
	case http.MethodPost:
		s.handleAttachAssistant(w, r, stageID)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleListAssistants(w http.ResponseWriter, r *http.Request, stageID string) {
	if s.assistantsReader == nil {
		http.NotFound(w, r)
		return
	}
	if !isValidStageID(stageID) {
		http.Error(w, "invalid stage id", http.StatusBadRequest)
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
		http.Error(w, "forbidden origin", http.StatusForbidden)
		return
	}
	if !isValidStageID(stageID) {
		http.Error(w, "invalid stage id", http.StatusBadRequest)
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
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if body.RequestID == "" {
		http.Error(w, "request_id is required", http.StatusBadRequest)
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
		http.Error(w, "invalid task id", http.StatusBadRequest)
		return
	}

	switch {
	case action == "events" && r.Method == http.MethodGet:
		s.handleAssistantEvents(w, r, taskID)
	case action == "pause" && r.Method == http.MethodPost:
		s.handleAssistantTaskAction(w, r, taskID, s.assistantActions.PauseTask)
	case action == "continue" && r.Method == http.MethodPost:
		s.handleAssistantTaskAction(w, r, taskID, s.assistantActions.ContinueTask)
	case action == "cancel" && r.Method == http.MethodPost:
		s.handleAssistantTaskAction(w, r, taskID, s.assistantActions.CancelTask)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleAssistantTaskAction(w http.ResponseWriter, r *http.Request, taskID string, action func(ctx context.Context, taskID string) error) {
	if s.assistantActions == nil {
		http.NotFound(w, r)
		return
	}
	if !sameOriginOK(r) {
		http.Error(w, "forbidden origin", http.StatusForbidden)
		return
	}
	if err := action(r.Context(), taskID); err != nil {
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
		writeAssistantErrCode(w, assistantErrStatus(rej.Code), string(rej.Code))
		return
	}
	writeAssistantErrCode(w, http.StatusInternalServerError, "internal")
}

func assistantErrStatus(code assistants.ErrorCode) int {
	switch code {
	case assistants.CodeStaleRevision, assistants.CodeInvalidStatus, assistants.CodeFlowPaused, assistants.CodeBudgetExceeded:
		return http.StatusConflict
	case assistants.CodeFeatureDisabled:
		return http.StatusNotFound
	case assistants.CodeRunFinalizing:
		return http.StatusServiceUnavailable
	case assistants.CodeCapabilityUnavailable, assistants.CodeDelegationDepthExceeded, assistants.CodeInvalidParent, assistants.CodeAncestorInactive:
		return http.StatusUnprocessableEntity
	default:
		return http.StatusInternalServerError
	}
}

func writeAssistantErrCode(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}
