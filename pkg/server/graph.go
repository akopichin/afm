package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
	"github.com/akopichin/afm/pkg/state"
)

// GraphActions is the write/read service for Этап 9's dynamic-stage API — a SEPARATE, narrow interface, same
// convention as ProposalActions/AssistantActions: nil = capability off, every route 404s.
type GraphActions interface {
	// PreviewAddStage validates req against the CURRENT registry and returns the resolved dependency diff
	// WITHOUT mutating anything.
	PreviewAddStage(req orchestrator.AddStageDraft) (orchestrator.AddStagePreview, error)
	// CommitAddStage durably adds the stage and wakes the scheduler.
	CommitAddStage(ctx context.Context, req orchestrator.AddStageDraft) (orchestrator.AddStageResult, error)
	// GraphRevision returns the registry's current revision — surfaced in GET /api/status for the dashboard
	// to detect a stale dialog without a round trip to preview.
	GraphRevision() int64
}

const maxAddStageBodyBytes = 16 << 10

// addStageRequest is the shared wire shape for POST /api/run/graph/preview and /api/run/graph/commit.
type addStageRequest struct {
	Kind             string   `json:"kind"`
	ID               string   `json:"id"`
	Name             string   `json:"name,omitempty"`
	Command          string   `json:"command"`
	Prompt           string   `json:"prompt"`
	TargetID         string   `json:"target_id,omitempty"`
	DependsOn        []string `json:"depends_on,omitempty"`
	ExpectedRevision int64    `json:"expected_revision,omitempty"`
	// RequestID is required for /commit (idempotency key), ignored for /preview.
	RequestID string `json:"request_id,omitempty"`
}

func (req addStageRequest) toDraft() orchestrator.AddStageDraft {
	return orchestrator.AddStageDraft{
		Kind:             req.Kind,
		TargetID:         req.TargetID,
		ExplicitDeps:     req.DependsOn,
		ExpectedRevision: req.ExpectedRevision,
		RequestID:        req.RequestID,
		Stage: graph.StageDraft{
			ID: req.ID, Name: req.Name, Command: req.Command, Prompt: req.Prompt,
		},
	}
}

// stagePreviewView is the JSON-facing projection of a graph.InsertionPlan — T9.6's dialog reads DependsOn/
// ExtraDeps/Order/Warnings to render the actual dependency diff, not just "a new row will appear."
type stagePreviewView struct {
	ID        string              `json:"id"`
	Name      string              `json:"name,omitempty"`
	Command   string              `json:"command"`
	Prompt    string              `json:"prompt"`
	DependsOn []string            `json:"depends_on"`
	ExtraDeps map[string][]string `json:"extra_deps,omitempty"`
	Order     []string            `json:"order"`
	Warnings  []string            `json:"warnings,omitempty"`
}

func stagePreviewFromPlan(plan graph.InsertionPlan) stagePreviewView {
	return stagePreviewView{
		ID: plan.Stage.ID, Name: plan.Stage.Name, Command: plan.Stage.Command, Prompt: plan.Stage.Prompt,
		DependsOn: plan.Stage.DependsOn, ExtraDeps: plan.ExtraDeps, Order: plan.Order, Warnings: plan.Warnings,
	}
}

// routeRunGraph handles POST /api/run/graph/{preview|commit}.
func (s *Server) routeRunGraph(w http.ResponseWriter, r *http.Request) {
	if s.graphActions == nil {
		http.NotFound(w, r)
		return
	}
	if !sameOriginOK(r) {
		writeGraphErrCode(w, http.StatusForbidden, "forbidden_origin")
		return
	}
	if r.Method != http.MethodPost {
		writeGraphErrCode(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	switch r.URL.Path {
	case "/api/run/graph/preview":
		s.handleGraphPreview(w, r)
	case "/api/run/graph/commit":
		s.handleGraphCommit(w, r)
	default:
		http.NotFound(w, r)
	}
}

func decodeAddStageRequest(w http.ResponseWriter, r *http.Request) (addStageRequest, bool) {
	var req addStageRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAddStageBodyBytes)).Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeGraphErrCode(w, http.StatusRequestEntityTooLarge, "request_too_large")
			return addStageRequest{}, false
		}
		writeGraphErrCode(w, http.StatusBadRequest, "invalid_request_body")
		return addStageRequest{}, false
	}
	return req, true
}

func (s *Server) handleGraphPreview(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeAddStageRequest(w, r)
	if !ok {
		return
	}
	preview, err := s.graphActions.PreviewAddStage(req.toDraft())
	if err != nil {
		writeGraphError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"stage":          stagePreviewFromPlan(preview.Plan),
		"graph_revision": preview.GraphRevision,
	})
}

func (s *Server) handleGraphCommit(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeAddStageRequest(w, r)
	if !ok {
		return
	}
	res, err := s.graphActions.CommitAddStage(r.Context(), req.toDraft())
	if err != nil {
		writeGraphError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"stage": stagePreviewView{
			ID: res.Stage.ID, Name: res.Stage.Name, Command: res.Stage.Command,
			Prompt: res.Stage.Prompt, DependsOn: res.Stage.DependsOn,
		},
		"graph_revision": res.GraphRevision,
	})
}

// writeGraphError renders an error from GraphActions as {"error":<code>}: 409 for stale_revision/
// invalid_status/flow_paused, 503 for run_finalizing, 400 for every other draft-validation rejection, 500
// otherwise.
func writeGraphError(w http.ResponseWriter, err error) {
	if errors.Is(err, orchestrator.ErrFlowPaused) {
		writeGraphErrCode(w, http.StatusConflict, "flow_paused")
		return
	}
	if errors.Is(err, orchestrator.ErrRequestIDRequired) {
		writeGraphErrCode(w, http.StatusBadRequest, "request_id_required")
		return
	}
	if errors.Is(err, orchestrator.ErrUnknownInsertionKind) {
		writeGraphErrCode(w, http.StatusBadRequest, "invalid_kind")
		return
	}
	var rej *state.OperationRejectedError
	if errors.As(err, &rej) {
		writeGraphErrCode(w, graphRejectedStatus(string(rej.Code)), string(rej.Code))
		return
	}
	var ve *graph.ValidationError
	if errors.As(err, &ve) {
		writeGraphErrCode(w, graphRejectedStatus(ve.Code), ve.Code)
		return
	}
	writeGraphErrCode(w, http.StatusInternalServerError, errCodeInternal)
}

func graphRejectedStatus(code string) int {
	switch code {
	case string(assistants.CodeStaleRevision), string(assistants.CodeInvalidStatus):
		return http.StatusConflict
	case string(assistants.CodeRunFinalizing):
		return http.StatusServiceUnavailable
	default:
		// invalid_stage_id/duplicate_stage_id/unknown_dependency/command_not_prepared/target_not_found
		// (graph.ValidationError's codes) and anything unrecognized all land here.
		return http.StatusBadRequest
	}
}

func writeGraphErrCode(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}
