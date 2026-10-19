package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/akopichin/afm/pkg/agentcomm"
)

// Agent Mesh Phase 3, T3.1: /api/agent-mesh/* — the agent-facing adapter over the shared Service. The actor
// is ALWAYS resolved from the bearer token (ResolveToken); no request body here ever carries a sender/actor
// field a model could set itself (mirrors TestAttach_RejectsClientSuppliedCommand's rule for /api/mesh/*).

// maxAgentMeshBodyBytes bounds every POST body on this surface.
const maxAgentMeshBodyBytes = 32 << 10

// AgentMeshActions is the agent-facing Service adapter. nil = capability off: every route 404s (same
// nil-service convention as AssistantActions/SideAgent).
type AgentMeshActions interface {
	// ResolveToken validates a per-invocation bearer token, returning the authenticated participant id.
	ResolveToken(token string) (participantID string, ok bool)
	ListParticipants(ctx context.Context, actor string) ([]agentcomm.Participant, error)
	Send(ctx context.Context, requestID, actor string, req MeshSendRequest) (MeshSendReceipt, error)
	// Inbox returns the messages addressed to actor that it has not been handed yet, or with all its whole
	// inbox history.
	Inbox(ctx context.Context, actor string, cursor uint64, limit int, all bool) (MeshMessagePage, error)
	Thread(ctx context.Context, actor, threadID string, cursor uint64, limit int) (MeshMessagePage, error)
	Ack(ctx context.Context, requestID, actor, messageID string) error
	Wait(ctx context.Context, requestID, actor string, req MeshWaitRequest) (MeshWaitReceipt, error)
	CancelWait(ctx context.Context, requestID, actor, waitID, reason string) error
	PublishBoard(ctx context.Context, requestID, actor string, req MeshPublishRequest) (agentcomm.BoardEntry, error)
	Board(ctx context.Context, key string) ([]agentcomm.BoardEntry, error)
}

// MeshSendRequest is POST /api/agent-mesh/{send,reply}'s body. Kind is set by the handler for /reply
// ("reply", requiring InReplyTo), and read from the body for /send.
type MeshSendRequest struct {
	ThreadID     string   `json:"thread_id"`
	Recipients   []string `json:"recipient_ids"`
	Kind         string   `json:"kind"`
	InReplyTo    string   `json:"in_reply_to,omitempty"`
	Body         string   `json:"body"`
	ArtifactRefs []string `json:"artifact_refs,omitempty"`
}

type MeshSendReceipt struct {
	MessageID string `json:"message_id"`
	Seq       uint64 `json:"seq"`
	// ThreadID — the thread the message went into; an agent that sent without --thread continues there.
	ThreadID string `json:"thread_id,omitempty"`
}

type MeshMessagePage struct {
	Messages   []agentcomm.Message `json:"messages"`
	NextCursor uint64              `json:"next_cursor"`
}

type MeshWaitRequest struct {
	Condition        agentcomm.WaitCondition `json:"condition"`
	DeadlineUnix     *int64                  `json:"deadline_unix,omitempty"`
	ContinuationNote string                  `json:"continuation_note,omitempty"`
}

type MeshWaitReceipt struct {
	WaitID string `json:"wait_id"`
	Status string `json:"status"`
}

type MeshPublishRequest struct {
	Key             string   `json:"key"`
	Status          string   `json:"status"`
	ArtifactRefs    []string `json:"artifact_refs,omitempty"`
	ExpectedVersion int      `json:"expected_version"`
}

// authenticateAgentMesh resolves the bearer token from r's Authorization header. A missing, malformed or
// unknown token gets 401 and the handler returns before touching the Service — "changes nothing" by
// construction, since nothing downstream of this check ever runs.
func (s *Server) authenticateAgentMesh(w http.ResponseWriter, r *http.Request) (actor string, ok bool) {
	auth := r.Header.Get("Authorization")
	token, found := strings.CutPrefix(auth, "Bearer ")
	if !found || token == "" {
		writeAssistantErrCode(w, http.StatusUnauthorized, "unauthorized")
		return "", false
	}
	participant, known := s.agentMesh.ResolveToken(token)
	if !known {
		writeAssistantErrCode(w, http.StatusUnauthorized, "unauthorized")
		return "", false
	}
	return participant, true
}

// routeAgentMesh deliberately does NOT check sameOriginOK, unlike every other mutating POST route in this
// package (assistants.go, discussion.go, proposals.go, graph.go, side_agent.go all do): the `afm agent` CLI
// is not a browser and never sends an Origin header, so requiring one would 403 every real agent request.
// The bearer token (authenticateAgentMesh) is this surface's actual CSRF-equivalent defense — a same-origin
// check would add nothing a browser-originated request could exploit anyway, since no browser ever holds a
// valid per-invocation mesh token.
// agentMeshActionReply is the /api/agent-mesh/reply route segment.
const agentMeshActionReply = "reply"

func (s *Server) routeAgentMesh(w http.ResponseWriter, r *http.Request) {
	if s.agentMesh == nil {
		http.NotFound(w, r)
		return
	}
	action := strings.TrimPrefix(r.URL.Path, "/api/agent-mesh/")
	if action == "" || strings.Contains(action, "/") {
		http.NotFound(w, r)
		return
	}
	actor, ok := s.authenticateAgentMesh(w, r)
	if !ok {
		return
	}

	switch {
	case action == "whoami" && r.Method == http.MethodGet:
		writeJSON(w, map[string]string{"participant_id": actor})
	case action == "participants" && r.Method == http.MethodGet:
		s.handleAgentMeshParticipants(w, r, actor)
	case action == "inbox" && r.Method == http.MethodGet:
		s.handleAgentMeshInbox(w, r, actor)
	case action == "thread" && r.Method == http.MethodGet:
		s.handleAgentMeshThread(w, r, actor)
	case action == "board" && r.Method == http.MethodGet:
		s.handleAgentMeshBoard(w, r)
	case action == "send" && r.Method == http.MethodPost:
		s.handleAgentMeshSend(w, r, actor, "")
	case action == agentMeshActionReply && r.Method == http.MethodPost:
		s.handleAgentMeshSend(w, r, actor, string(agentcomm.MessageKindReply))
	case action == "ack" && r.Method == http.MethodPost:
		s.handleAgentMeshAck(w, r, actor)
	case action == "wait" && r.Method == http.MethodPost:
		s.handleAgentMeshWait(w, r, actor)
	case action == "cancel-wait" && r.Method == http.MethodPost:
		s.handleAgentMeshCancelWait(w, r, actor)
	case action == "publish" && r.Method == http.MethodPost:
		s.handleAgentMeshPublish(w, r, actor)
	default:
		http.NotFound(w, r)
	}
}

func decodeAgentMeshBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAgentMeshBodyBytes)).Decode(v); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeAssistantErrCode(w, http.StatusRequestEntityTooLarge, "request_too_large")
		} else {
			writeAssistantErrCode(w, http.StatusBadRequest, "invalid_request_body")
		}
		return false
	}
	return true
}

func parseCursorLimit(r *http.Request) (cursor uint64, limit int) {
	cursor, _ = strconv.ParseUint(r.URL.Query().Get("cursor"), 10, 64)
	limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
	return cursor, limit
}

func (s *Server) handleAgentMeshParticipants(w http.ResponseWriter, r *http.Request, actor string) {
	participants, err := s.agentMesh.ListParticipants(r.Context(), actor)
	if err != nil {
		s.writeAssistantError(w, err)
		return
	}
	writeJSON(w, map[string]any{"participants": participants})
}

func (s *Server) handleAgentMeshInbox(w http.ResponseWriter, r *http.Request, actor string) {
	cursor, limit := parseCursorLimit(r)
	all := r.URL.Query().Get("all") == "1"
	page, err := s.agentMesh.Inbox(r.Context(), actor, cursor, limit, all)
	if err != nil {
		s.writeAssistantError(w, err)
		return
	}
	writeJSON(w, page)
}

func (s *Server) handleAgentMeshThread(w http.ResponseWriter, r *http.Request, actor string) {
	threadID := r.URL.Query().Get("id")
	if threadID == "" {
		writeAssistantErrCode(w, http.StatusBadRequest, "thread_id_required")
		return
	}
	cursor, limit := parseCursorLimit(r)
	page, err := s.agentMesh.Thread(r.Context(), actor, threadID, cursor, limit)
	if err != nil {
		s.writeAssistantError(w, err)
		return
	}
	writeJSON(w, page)
}

func (s *Server) handleAgentMeshBoard(w http.ResponseWriter, r *http.Request) {
	entries, err := s.agentMesh.Board(r.Context(), r.URL.Query().Get("key"))
	if err != nil {
		s.writeAssistantError(w, err)
		return
	}
	writeJSON(w, map[string]any{"entries": entries})
}

func (s *Server) handleAgentMeshSend(w http.ResponseWriter, r *http.Request, actor, forcedKind string) {
	var body struct {
		RequestID string `json:"request_id"`
		MeshSendRequest
	}
	if !decodeAgentMeshBody(w, r, &body) {
		return
	}
	if body.RequestID == "" {
		writeAssistantErrCode(w, http.StatusBadRequest, "request_id_required")
		return
	}
	if forcedKind != "" {
		body.Kind = forcedKind
	}
	receipt, err := s.agentMesh.Send(r.Context(), body.RequestID, actor, body.MeshSendRequest)
	if err != nil {
		s.writeAssistantError(w, err)
		return
	}
	writeJSON(w, receipt)
}

func (s *Server) handleAgentMeshAck(w http.ResponseWriter, r *http.Request, actor string) {
	var body struct {
		RequestID string `json:"request_id"`
		MessageID string `json:"message_id"`
	}
	if !decodeAgentMeshBody(w, r, &body) {
		return
	}
	if body.RequestID == "" || body.MessageID == "" {
		writeAssistantErrCode(w, http.StatusBadRequest, "request_id_and_message_id_required")
		return
	}
	if err := s.agentMesh.Ack(r.Context(), body.RequestID, actor, body.MessageID); err != nil {
		s.writeAssistantError(w, err)
		return
	}
	writeJSON(w, map[string]string{keyStatus: "ok"})
}

func (s *Server) handleAgentMeshWait(w http.ResponseWriter, r *http.Request, actor string) {
	var body struct {
		RequestID string `json:"request_id"`
		MeshWaitRequest
	}
	if !decodeAgentMeshBody(w, r, &body) {
		return
	}
	if body.RequestID == "" {
		writeAssistantErrCode(w, http.StatusBadRequest, "request_id_required")
		return
	}
	receipt, err := s.agentMesh.Wait(r.Context(), body.RequestID, actor, body.MeshWaitRequest)
	if err != nil {
		s.writeAssistantError(w, err)
		return
	}
	writeJSON(w, receipt)
}

func (s *Server) handleAgentMeshCancelWait(w http.ResponseWriter, r *http.Request, actor string) {
	var body struct {
		RequestID string `json:"request_id"`
		WaitID    string `json:"wait_id"`
		Reason    string `json:"reason,omitempty"`
	}
	if !decodeAgentMeshBody(w, r, &body) {
		return
	}
	if body.RequestID == "" || body.WaitID == "" {
		writeAssistantErrCode(w, http.StatusBadRequest, "request_id_and_wait_id_required")
		return
	}
	if err := s.agentMesh.CancelWait(r.Context(), body.RequestID, actor, body.WaitID, body.Reason); err != nil {
		s.writeAssistantError(w, err)
		return
	}
	writeJSON(w, map[string]string{keyStatus: "ok"})
}

func (s *Server) handleAgentMeshPublish(w http.ResponseWriter, r *http.Request, actor string) {
	var body struct {
		RequestID string `json:"request_id"`
		MeshPublishRequest
	}
	if !decodeAgentMeshBody(w, r, &body) {
		return
	}
	if body.RequestID == "" || body.Key == "" {
		writeAssistantErrCode(w, http.StatusBadRequest, "request_id_and_key_required")
		return
	}
	entry, err := s.agentMesh.PublishBoard(r.Context(), body.RequestID, actor, body.MeshPublishRequest)
	if err != nil {
		s.writeAssistantError(w, err)
		return
	}
	writeJSON(w, entry)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
