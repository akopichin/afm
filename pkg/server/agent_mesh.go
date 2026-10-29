package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/orchestrator"
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
	// WaitStatus reads the waits actor registered: every batch with an empty id, else the batch or single wait of
	// that id as a group. One of another participant is refused as unknown.
	WaitStatus(ctx context.Context, actor, id string) ([]MeshWaitReceipt, error)
	CancelWait(ctx context.Context, requestID, actor string, req MeshCancelWaitRequest) error
	// Spawn starts a one-shot helper of actor's stage. Only a stage agent may; the helper's stage, scope, mode and
	// executor all follow from the actor and the profile.
	Spawn(ctx context.Context, requestID, actor string, req MeshSpawnRequest) (MeshSpawnReceipt, error)
	// HelperResult reads the status and the last committed result of a helper actor's stage spawned. A task of
	// another stage is refused as unknown.
	HelperResult(ctx context.Context, actor, taskID string) (MeshHelperResult, error)
	PublishBoard(ctx context.Context, requestID, actor string, req MeshPublishRequest) (agentcomm.BoardEntry, error)
	Board(ctx context.Context, key string) ([]agentcomm.BoardEntry, error)
	// BoardTask makes one change to a typed board task as actor.
	BoardTask(ctx context.Context, requestID, actor string, req MeshBoardTaskRequest) (orchestrator.BoardTaskReceipt, error)
	// BoardTasks returns every typed board task, or only the one under key.
	BoardTasks(ctx context.Context, key string) ([]agentcomm.BoardTaskView, error)
	// Loops lists the loops actor created; Loop adds or stops one of them as actor.
	Loops(ctx context.Context, actor string) (orchestrator.LoopPage, error)
	Loop(ctx context.Context, requestID, actor string, cmd MeshLoopCommand) (orchestrator.LoopView, error)
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
	// SideAgentFailedIDs is a read-only UI projection, never a Mesh journal field.
	SideAgentFailedIDs map[agentcomm.MessageID]bool `json:"-"`
}

// MeshWaitRequest is POST /api/agent-mesh/wait's body. MessageIDs turns a reply_to condition into a batch: one
// wait for the answer of every listed request, with one deadline for the whole set.
type MeshWaitRequest struct {
	Condition        agentcomm.WaitCondition `json:"condition"`
	MessageIDs       []string                `json:"message_ids,omitempty"`
	DeadlineUnix     *int64                  `json:"deadline_unix,omitempty"`
	ContinuationNote string                  `json:"continuation_note,omitempty"`
}

// MeshWaitReceipt answers a single wait with WaitID, or a batch with BatchID and one Item per request. Status
// is that of the wait, or of the batch as a whole. AlreadyPending says no new wait was registered: an equivalent
// one is still pending and WaitID is that one.
type MeshWaitReceipt struct {
	WaitID         string         `json:"wait_id,omitempty"`
	Status         string         `json:"status"`
	BatchID        string         `json:"batch_id,omitempty"`
	Items          []MeshWaitItem `json:"items,omitempty"`
	AlreadyPending bool           `json:"already_pending,omitempty"`
}

// MeshWaitItem is the wait of one request of a batch. Reason says why a wait ended without an answer.
type MeshWaitItem struct {
	MessageID string `json:"message_id"`
	WaitID    string `json:"wait_id"`
	Status    string `json:"status"`
	Reason    string `json:"reason,omitempty"`
}

// MeshCancelWaitRequest is POST /api/agent-mesh/cancel-wait's body: one wait, or every still-pending wait of a
// batch.
type MeshCancelWaitRequest struct {
	WaitID  string `json:"wait_id,omitempty"`
	BatchID string `json:"batch_id,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// MeshSpawnRequest is POST /api/agent-mesh/spawn's body: the helper's profile, its task's title and its
// instruction — nothing else is the agent's to choose.
type MeshSpawnRequest struct {
	ProfileID   string `json:"profile_id"`
	Title       string `json:"title"`
	Instruction string `json:"instruction"`
}

// MeshSpawnReceipt names what a spawn created. MessageID is the request the helper answers: wait for it like for
// any other request.
type MeshSpawnReceipt struct {
	TaskID        string `json:"task_id"`
	ParticipantID string `json:"participant_id"`
	MessageID     string `json:"message_id"`
}

// MeshHelperResult is GET /api/agent-mesh/helper's answer. Result is the helper's last committed result,
// absent while it runs and when it ended without one (StatusReason then says why).
type MeshHelperResult struct {
	TaskID       string          `json:"task_id"`
	Title        string          `json:"title"`
	Status       string          `json:"status"`
	StatusReason string          `json:"status_reason,omitempty"`
	Result       json.RawMessage `json:"result,omitempty"`
}

type MeshPublishRequest struct {
	Key             string   `json:"key"`
	Status          string   `json:"status"`
	ArtifactRefs    []string `json:"artifact_refs,omitempty"`
	ExpectedVersion int      `json:"expected_version"`
}

// MeshBoardTaskRequest is the body of a typed board task change on both surfaces — POST
// /api/agent-mesh/task and POST /api/mesh/tasks. It has no actor field: the caller is the token's participant
// or the human. Text is the progress text, the done result or the release/reopen reason. The pointer fields
// keep whether the body has them: an edit changes only the fields it has ("" clears the description or the
// coordinator), and a progress or done with "artifact_refs": [] clears the assignment's refs.
type MeshBoardTaskRequest struct {
	Action          string    `json:"action"`
	Key             string    `json:"key"`
	ExpectedVersion int       `json:"expected_version"`
	AssignmentID    string    `json:"assignment_id,omitempty"`
	To              string    `json:"to,omitempty"`
	Title           *string   `json:"title,omitempty"`
	Description     *string   `json:"description,omitempty"`
	Coordinator     *string   `json:"coordinator,omitempty"`
	Text            string    `json:"text,omitempty"`
	ArtifactRefs    *[]string `json:"artifact_refs,omitempty"`
}

// decodeBoardTaskBody reads a board task change's body; it has answered the request itself when ok is false.
func decodeBoardTaskBody(w http.ResponseWriter, r *http.Request) (requestID string, req MeshBoardTaskRequest, ok bool) {
	var body struct {
		RequestID string `json:"request_id"`
		MeshBoardTaskRequest
	}
	if !decodeAgentMeshBody(w, r, &body) {
		return "", MeshBoardTaskRequest{}, false
	}
	if body.RequestID == "" {
		writeAssistantErrCode(w, http.StatusBadRequest, "request_id_required")
		return "", MeshBoardTaskRequest{}, false
	}
	return body.RequestID, body.MeshBoardTaskRequest, true
}

// The two loop actions of POST /api/agent-mesh/loop and POST /api/mesh/loop.
const (
	MeshLoopAdd  = "add"
	MeshLoopStop = "stop"
)

// MeshLoopCommand shares the dashboard and agent body, with caller identity supplied by the adapter.
// Every is parsed before the Service validates the target and stage semantics.
type MeshLoopCommand struct {
	Action      string
	Target      string
	Every       time.Duration
	Prompt      string
	UntilStage  string
	OriginStage string
	LoopID      string
}

// decodeLoopBody reads a loop change's body; it has answered the request itself when ok is false.
func decodeLoopBody(w http.ResponseWriter, r *http.Request) (requestID string, cmd MeshLoopCommand, ok bool) {
	var body struct {
		RequestID   string `json:"request_id"`
		Action      string `json:"action"`
		Target      string `json:"target"`
		Every       string `json:"every"`
		Prompt      string `json:"prompt"`
		UntilStage  string `json:"until_stage"`
		OriginStage string `json:"origin_stage"`
		LoopID      string `json:"loop_id"`
	}
	if !decodeAgentMeshBody(w, r, &body) {
		return "", MeshLoopCommand{}, false
	}
	reject := func(code string) (string, MeshLoopCommand, bool) {
		writeAssistantErrCode(w, http.StatusBadRequest, code)
		return "", MeshLoopCommand{}, false
	}
	switch {
	case body.RequestID == "":
		return reject("request_id_required")
	case body.UntilStage != "" && !isValidStageID(body.UntilStage), body.OriginStage != "" && !isValidStageID(body.OriginStage):
		return reject("invalid_stage_id")
	}
	cmd = MeshLoopCommand{
		Action: body.Action, Target: body.Target, Prompt: body.Prompt,
		UntilStage: body.UntilStage, OriginStage: body.OriginStage, LoopID: body.LoopID,
	}
	switch body.Action {
	case MeshLoopAdd:
		every, err := time.ParseDuration(body.Every)
		if err != nil {
			return reject("invalid_every")
		}
		cmd.Every = every
	case MeshLoopStop:
		if body.LoopID == "" {
			return reject("loop_id_required")
		}
	default:
		return reject("invalid_action")
	}
	return body.RequestID, cmd, true
}

// loopsForClient shows no loops as an empty list, never null.
func loopsForClient(page orchestrator.LoopPage) orchestrator.LoopPage {
	if page.Loops == nil {
		page.Loops = []orchestrator.LoopView{}
	}
	return page
}

// boardTasksForClient shows a task without active assignments with an empty assignments array, never null,
// and no tasks as an empty list.
func boardTasksForClient(tasks []agentcomm.BoardTaskView) []agentcomm.BoardTaskView {
	out := make([]agentcomm.BoardTaskView, len(tasks))
	for i, t := range tasks {
		if t.Assignments == nil {
			t.Assignments = []agentcomm.BoardTaskAssignment{}
		}
		out[i] = t
	}
	return out
}

// authenticateAgentMesh resolves the bearer token from r's Authorization header. A missing, malformed or
// unknown token gets 401 and the handler returns before touching the Service — "changes nothing" by
// construction, since nothing downstream of this check ever runs.
func (s *Server) authenticateAgentMesh(w http.ResponseWriter, r *http.Request) (actor string, ok bool) {
	token := bearerToken(r)
	if token == "" {
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

// bearerToken is the token of r's Authorization header, "" when there is none.
func bearerToken(r *http.Request) string {
	token, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !found {
		return ""
	}
	return token
}

// WithMeshToken returns ctx carrying the bearer token a request was authenticated with. The orchestrator decides a
// wait against that invocation: the actor alone cannot tell two invocations of one stage apart.
func WithMeshToken(ctx context.Context, token string) context.Context {
	return orchestrator.WithMeshInvocation(ctx, token)
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
	case action == "wait-status" && r.Method == http.MethodGet:
		s.handleAgentMeshWaitStatus(w, r, actor)
	case action == "helper" && r.Method == http.MethodGet:
		s.handleAgentMeshHelperResult(w, r, actor)
	case action == "tasks" && r.Method == http.MethodGet:
		s.handleAgentMeshBoardTasks(w, r)
	case action == "task" && r.Method == http.MethodPost:
		s.handleAgentMeshBoardTask(w, r, actor)
	case action == "loops" && r.Method == http.MethodGet:
		s.handleAgentMeshLoops(w, r, actor)
	case action == "loop" && r.Method == http.MethodPost:
		s.handleAgentMeshLoop(w, r, actor)
	case action == "send" && r.Method == http.MethodPost:
		s.handleAgentMeshSend(w, r, actor, "")
	case action == agentMeshActionReply && r.Method == http.MethodPost:
		s.handleAgentMeshSend(w, r, actor, string(agentcomm.MessageKindReply))
	case action == "ack" && r.Method == http.MethodPost:
		s.handleAgentMeshAck(w, r, actor)
	case action == "wait" && r.Method == http.MethodPost:
		s.handleAgentMeshWait(w, r, actor)
	case action == "spawn" && r.Method == http.MethodPost:
		s.handleAgentMeshSpawn(w, r, actor)
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

func (s *Server) handleAgentMeshBoardTasks(w http.ResponseWriter, r *http.Request) {
	tasks, err := s.agentMesh.BoardTasks(r.Context(), r.URL.Query().Get("key"))
	if err != nil {
		s.writeAssistantError(w, err)
		return
	}
	writeJSON(w, map[string]any{"tasks": boardTasksForClient(tasks)})
}

func (s *Server) handleAgentMeshBoardTask(w http.ResponseWriter, r *http.Request, actor string) {
	requestID, req, ok := decodeBoardTaskBody(w, r)
	if !ok {
		return
	}
	receipt, err := s.agentMesh.BoardTask(r.Context(), requestID, actor, req)
	if err != nil {
		s.writeAssistantError(w, err)
		return
	}
	writeJSON(w, receipt)
}

func (s *Server) handleAgentMeshLoops(w http.ResponseWriter, r *http.Request, actor string) {
	page, err := s.agentMesh.Loops(r.Context(), actor)
	if err != nil {
		s.writeAssistantError(w, err)
		return
	}
	writeJSON(w, loopsForClient(page))
}

// handleAgentMeshLoop is POST /api/agent-mesh/loop. The agent's Feed is derived from the agent itself, so a
// body origin_stage is dropped here rather than trusted.
func (s *Server) handleAgentMeshLoop(w http.ResponseWriter, r *http.Request, actor string) {
	requestID, cmd, ok := decodeLoopBody(w, r)
	if !ok {
		return
	}
	cmd.OriginStage = ""
	view, err := s.agentMesh.Loop(r.Context(), requestID, actor, cmd)
	if err != nil {
		s.writeAssistantError(w, err)
		return
	}
	writeJSON(w, view)
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
	receipt, err := s.agentMesh.Wait(WithMeshToken(r.Context(), bearerToken(r)), body.RequestID, actor, body.MeshWaitRequest)
	if err != nil {
		s.writeAssistantError(w, err)
		return
	}
	writeJSON(w, receipt)
}

func (s *Server) handleAgentMeshWaitStatus(w http.ResponseWriter, r *http.Request, actor string) {
	batches, err := s.agentMesh.WaitStatus(r.Context(), actor, r.URL.Query().Get("wait_id"))
	if err != nil {
		s.writeAssistantError(w, err)
		return
	}
	if batches == nil {
		batches = []MeshWaitReceipt{}
	}
	writeJSON(w, map[string]any{"batches": batches})
}

func (s *Server) handleAgentMeshSpawn(w http.ResponseWriter, r *http.Request, actor string) {
	var body struct {
		RequestID string `json:"request_id"`
		MeshSpawnRequest
	}
	if !decodeAgentMeshBody(w, r, &body) {
		return
	}
	if body.RequestID == "" || body.ProfileID == "" || body.Title == "" || body.Instruction == "" {
		writeAssistantErrCode(w, http.StatusBadRequest, "request_id_profile_id_title_and_instruction_required")
		return
	}
	receipt, err := s.agentMesh.Spawn(r.Context(), body.RequestID, actor, body.MeshSpawnRequest)
	if err != nil {
		s.writeAssistantError(w, err)
		return
	}
	writeJSON(w, receipt)
}

func (s *Server) handleAgentMeshHelperResult(w http.ResponseWriter, r *http.Request, actor string) {
	taskID := r.URL.Query().Get("task_id")
	if taskID == "" {
		writeAssistantErrCode(w, http.StatusBadRequest, "task_id_required")
		return
	}
	result, err := s.agentMesh.HelperResult(r.Context(), actor, taskID)
	if err != nil {
		s.writeAssistantError(w, err)
		return
	}
	writeJSON(w, result)
}

func (s *Server) handleAgentMeshCancelWait(w http.ResponseWriter, r *http.Request, actor string) {
	var body struct {
		RequestID string `json:"request_id"`
		MeshCancelWaitRequest
	}
	if !decodeAgentMeshBody(w, r, &body) {
		return
	}
	if body.RequestID == "" || (body.WaitID == "") == (body.BatchID == "") {
		writeAssistantErrCode(w, http.StatusBadRequest, "request_id_and_exactly_one_of_wait_id_or_batch_id_required")
		return
	}
	if err := s.agentMesh.CancelWait(r.Context(), body.RequestID, actor, body.MeshCancelWaitRequest); err != nil {
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
