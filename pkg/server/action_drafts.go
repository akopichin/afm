package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/jsonstrict"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
	"github.com/akopichin/afm/pkg/sideagent"
)

// maxDraftConfirmBodyBytes bounds the confirm request body — it carries only an id and a small int, never
// free text, so this is deliberately much smaller than maxAttachBodyBytes.
const maxDraftConfirmBodyBytes = 4 << 10

// attachAssistantTarget/attachAssistantPayload are the ONLY fields a Type: "attach_assistant" draft's
// Target/Payload may carry (T4.5's "validated against allowed fields" — jsonstrict rejects anything else,
// the same discipline handleAttachAssistant already applies to a manually-typed Attach request).
type attachAssistantTarget struct {
	StageID   string `json:"stage_id"`
	ProfileID string `json:"profile_id"`
}

type attachAssistantPayload struct {
	Title       string `json:"title"`
	Instruction string `json:"instruction"`
}

// addStageTarget/addStagePayload are the ONLY fields a Type: "add_stage" draft's Target/Payload may carry
// (Этап 10, T10.1) — the same discipline attachAssistantTarget/attachAssistantPayload already apply, and the
// same field split as addStageRequest (graph.go), which decodeAddStageDraft below builds an
// orchestrator.AddStageDraft from — PreviewAddStage/CommitAddStage are called VERBATIM (TestAction_AddStageDraftValidatedLikeManual),
// never a parallel copy of the insertion rules.
type addStageTarget struct {
	Kind             string   `json:"kind"`
	TargetID         string   `json:"target_id,omitempty"`
	DependsOn        []string `json:"depends_on,omitempty"`
	ExpectedRevision int64    `json:"expected_revision,omitempty"`
}

type addStagePayload struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	Command string `json:"command"`
	Prompt  string `json:"prompt"`
}

// decodeAddStageDraft validates draft.Target/Payload against the add_stage schema and builds the SAME
// orchestrator.AddStageDraft shape the manual /api/run/graph/{preview,commit} routes build from their own
// wire request (graph.go's addStageRequest.toDraft) — one conversion, reused by both entry points.
func decodeAddStageDraft(draft sideagent.ActionDraft) (orchestrator.AddStageDraft, error) {
	var target addStageTarget
	if err := jsonstrict.DecodeDisallowUnknown(draft.Target, &target); err != nil {
		return orchestrator.AddStageDraft{}, err
	}
	var payload addStagePayload
	if err := jsonstrict.DecodeDisallowUnknown(draft.Payload, &payload); err != nil {
		return orchestrator.AddStageDraft{}, err
	}
	return orchestrator.AddStageDraft{
		Kind: target.Kind, TargetID: target.TargetID, ExplicitDeps: target.DependsOn,
		ExpectedRevision: target.ExpectedRevision,
		Stage:            graph.StageDraft{ID: payload.ID, Name: payload.Name, Command: payload.Command, Prompt: payload.Prompt},
	}, nil
}

// findActionDraft locates the Index-th action draft recorded on turnID's turn_completed event. A turn with
// no action drafts, an unknown turnID, or an out-of-range index are all equally "not found" — there is
// nothing here for a caller to distinguish them by, and nothing should.
func findActionDraft(history []sideagent.Event, turnID string, index int) (sideagent.ActionDraft, bool) {
	for _, e := range history {
		if e.Type != sideagent.EventTurnCompleted || e.TurnID != turnID {
			continue
		}
		var d sideagent.TurnCompletedData
		if err := e.DecodeData(&d); err != nil {
			return sideagent.ActionDraft{}, false
		}
		if index < 0 || index >= len(d.ActionDrafts) {
			return sideagent.ActionDraft{}, false
		}
		return d.ActionDrafts[index], true
	}
	return sideagent.ActionDraft{}, false
}

// handleConfirmActionDraft validates and executes one action draft a side-agent turn proposed (T4.4's
// extraction, T4.5's validation + execution). The draft's actual content is read from the DURABLE
// conversation history by (turn_id, index) — NEVER trusted from the request body, so a client cannot forge
// or tamper with Target/Payload; the body carries ONLY the addressing of which draft to confirm, nothing
// else (strict decoding rejects any extra field, e.g. an attempted actor/scope override —
// TestDraft_ActorScopeEnforced: same-origin alone is not authorization).
func (s *Server) handleConfirmActionDraft(w http.ResponseWriter, r *http.Request) {
	if s.sideAgent == nil {
		http.NotFound(w, r)
		return
	}
	if !sameOriginOK(r) {
		writeAssistantErrCode(w, http.StatusForbidden, "forbidden_origin")
		return
	}

	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxDraftConfirmBodyBytes))
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeAssistantErrCode(w, http.StatusRequestEntityTooLarge, "request_too_large")
			return
		}
		writeAssistantErrCode(w, http.StatusBadRequest, "invalid_request_body")
		return
	}
	var body struct {
		TurnID string `json:"turn_id"`
		Index  int    `json:"index"`
	}
	if err := jsonstrict.DecodeDisallowUnknown(raw, &body); err != nil {
		writeAssistantErrCode(w, http.StatusBadRequest, "invalid_request_body")
		return
	}
	if body.TurnID == "" || body.Index < 0 {
		writeAssistantErrCode(w, http.StatusBadRequest, "invalid_request_body")
		return
	}

	// T10.3 (TestStaleTurn_ActionRejected/TestActionFromToolOutput_NotExecuted): the draft's content is read
	// ONLY from a durable turn_completed event — extractActionDrafts (pkg/sideagent) never records a draft
	// from tool output or from an interrupted/failed turn (those produce turn_interrupted/turn_failed, never
	// turn_completed) to begin with, so a stale/cancelled/stopped turn simply has nothing here to find.
	draft, ok := findActionDraft(s.sideAgent.History(), body.TurnID, body.Index)
	if !ok {
		writeAssistantErrCode(w, http.StatusNotFound, "draft_not_found")
		return
	}

	requestID := draftOperationID(body.TurnID, body.Index)
	switch draft.Type {
	case string(assistants.ActionAttachAssistant):
		s.confirmAttachAssistantDraft(w, r, draft, requestID)
	case string(assistants.ActionAddStage):
		s.confirmAddStageDraft(w, r, draft, requestID)
	default:
		writeAssistantErrCode(w, http.StatusUnprocessableEntity, "unknown_action_type")
	}
}

// confirmAttachAssistantDraft is handleConfirmActionDraft's attach_assistant branch (T4.5), unchanged in
// behavior from before Этап 10 — only split out so handleConfirmActionDraft can dispatch by draft.Type.
func (s *Server) confirmAttachAssistantDraft(w http.ResponseWriter, r *http.Request, draft sideagent.ActionDraft, requestID string) {
	if s.assistantActions == nil {
		http.NotFound(w, r)
		return
	}
	var target attachAssistantTarget
	if err := jsonstrict.DecodeDisallowUnknown(draft.Target, &target); err != nil {
		writeAssistantErrCode(w, http.StatusUnprocessableEntity, "invalid_draft_target")
		return
	}
	var payload attachAssistantPayload
	if err := jsonstrict.DecodeDisallowUnknown(draft.Payload, &payload); err != nil {
		writeAssistantErrCode(w, http.StatusUnprocessableEntity, "invalid_draft_payload")
		return
	}

	currentRevision, maxDepth := s.assistantActions.PolicyRevision()
	if draft.ExpectedRevision != currentRevision {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": "stale_revision", "current_revision": currentRevision, "max_depth": maxDepth,
		})
		return
	}

	// Deterministic, per-draft operation id: a repeat confirm of the SAME draft (double click, retried
	// request) must resolve to the SAME task, exactly like a manual Attach's client-supplied request_id —
	// this IS that same id, derived instead of client-chosen, since the client never saw an id to send back.
	taskID, err := s.assistantActions.Attach(r.Context(), target.StageID, AttachRequest{
		RequestID:   requestID,
		ProfileID:   target.ProfileID,
		Title:       payload.Title,
		Instruction: payload.Instruction,
	})
	if err != nil {
		writeAssistantError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"task_id": taskID})
}

// confirmAddStageDraft is handleConfirmActionDraft's add_stage branch (Этап 10, T10.1): decodeAddStageDraft
// builds the SAME orchestrator.AddStageDraft shape the manual dashboard dialog sends to
// /api/run/graph/commit, and CommitAddStage is the EXACT SAME function that route calls
// (TestAction_AddStageDraftValidatedLikeManual) — no second copy of the DAG/insertion rules here. A stale
// graph revision surfaces as the SAME stale_revision code CommitAddStage already returns for the manual path
// (writeGraphError's own 409 mapping, reused via writeAssistantError's *graph.ValidationError branch); like
// the manual dialog, re-obtaining a fresh preview after that is the CLIENT's job
// (POST /api/run/graph/preview with the same draft), not something this endpoint does on the caller's behalf.
func (s *Server) confirmAddStageDraft(w http.ResponseWriter, r *http.Request, draft sideagent.ActionDraft, requestID string) {
	if s.graphActions == nil {
		http.NotFound(w, r)
		return
	}
	addDraft, err := decodeAddStageDraft(draft)
	if err != nil {
		writeAssistantErrCode(w, http.StatusUnprocessableEntity, "invalid_draft_payload")
		return
	}
	addDraft.RequestID = requestID
	res, err := s.graphActions.CommitAddStage(r.Context(), addDraft)
	if err != nil {
		writeAssistantError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"stage_id": res.Stage.ID, "graph_revision": res.GraphRevision})
}

func draftOperationID(turnID string, index int) string {
	return "draft-confirm:" + turnID + ":" + strconv.Itoa(index)
}
