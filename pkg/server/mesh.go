package server

import (
	"context"
	"net/http"
	"strings"

	"github.com/akopichin/afm/pkg/agentcomm"
)

// Agent Mesh Phase 7, T7.1: /api/mesh/* — the UI-facing adapter over the shared Service. Unlike
// /api/agent-mesh/* (agent_mesh.go), the actor is NEVER resolved per-request at all: every call on this
// surface is, by construction, the human dashboard operator (cmd/afm/run_mesh.go stamps
// state.Actor{Kind: "user"} itself, the same convention run_collaboration.go already uses for its own
// dashboard-originated calls) — so no method here even takes an actor parameter, and no request body field
// could ever be read as one. Every POST body on this surface is decoded with decodeAgentMeshBody
// (agent_mesh.go) — same maxAgentMeshBodyBytes bound, reused rather than duplicated: this is same-trust
// local dashboard traffic, same as the agent-facing surface, so a second constant would add nothing.
//
// `GET /api/mesh/threads` (bare, no id) lists every thread the human run owner belongs to — Agent Mesh
// Phase 7, T7.3's backend dependency: orchestrator.Service.ListThreads (pkg/orchestrator/agentcomm_messages.go)
// closed the gap this comment used to describe (neither ReadThread, which needs a known id, nor ReadInbox,
// which only surfaces threads where a message was addressed TO the participant, could answer "which threads
// is X in").

// MeshActions is the UI-facing Service adapter. nil = capability off: every route 404s (same nil-service
// convention as AgentMeshActions/AssistantActions).
type MeshActions interface {
	ListParticipants(ctx context.Context) ([]agentcomm.Participant, error)
	Threads(ctx context.Context) ([]agentcomm.Thread, error)
	OpenThread(ctx context.Context, requestID string, req MeshOpenThreadRequest) (agentcomm.Thread, error)
	CloseThread(ctx context.Context, requestID, threadID string) error
	Send(ctx context.Context, requestID, threadID string, req MeshSendRequest) (MeshSendReceipt, error)
	Thread(ctx context.Context, threadID string, cursor uint64, limit int) (MeshMessagePage, error)
	Inbox(ctx context.Context, cursor uint64, limit int) (MeshMessagePage, error)
	// StageMessages pages the conversation messages a stage's feed shows, in journal order, after cursor.
	StageMessages(ctx context.Context, stageID string, cursor uint64, limit int) (MeshStageMessagePage, error)
	Board(ctx context.Context, key string) ([]agentcomm.BoardEntry, error)
	// PublishBoard posts an entry to the shared board as the human operator (e.g. a task for the agents) and
	// tells req.Notify about it with one message.
	PublishBoard(ctx context.Context, requestID string, req MeshBoardPublishRequest) (agentcomm.BoardEntry, error)
	AttachAndSend(ctx context.Context, requestID string, req MeshAttachAndSendRequest) (MeshAttachAndSendResult, error)
}

// MeshOpenThreadRequest is POST /api/mesh/threads' body (opening a NEW thread — orchestrator.OpenThread).
// Members names every OTHER participant; the adapter (cmd/afm/run_mesh.go) is responsible for making sure
// the human is in the resulting member set, exactly like AttachAndSend already does for its own thread.
type MeshOpenThreadRequest struct {
	Title       string   `json:"title"`
	Scope       string   `json:"scope"` // "stage" or "run" — required, no default (agentcomm.Scope has none)
	Members     []string `json:"member_ids"`
	MaxMessages int      `json:"max_messages,omitempty"`
	// OriginStage — the stage whose feed the thread is opened from, so it shows in that feed.
	OriginStage string `json:"origin_stage,omitempty"`
}

// MeshStageMessagePage is GET /api/mesh/stages/{id}/messages' response: a page of the stage feed's
// conversation messages in journal order, the threads they belong to, and the cursor of the next page.
type MeshStageMessagePage struct {
	Messages   []agentcomm.Message `json:"messages"`
	Threads    []agentcomm.Thread  `json:"threads"`
	NextCursor uint64              `json:"next_cursor"`
	HasMore    bool                `json:"has_more"`
}

// MeshBoardPublishRequest is POST /api/mesh/board's body: the entry the human publishes (a key is only ever
// revised by its own author, so there is no field to hand it to someone else) plus Notify — participant ids
// that get one message from the human about the new entry, so an idle agent learns about the task.
type MeshBoardPublishRequest struct {
	MeshPublishRequest
	Notify []string `json:"notify,omitempty"`
}

// MeshAttachAndSendRequest is POST /api/mesh/attach-and-send's body — the T6.2 composite
// (orchestrator.AttachAndSend). There is deliberately no command/model/executor field, mirroring
// handleAttachAssistant's rule: the client cannot supply one because the struct has nowhere for it to go.
// Scope "" and an absent StageID together mean "stage scope, but the server has no stage to bind to" — the
// Service itself rejects that combination; omitting StageID with Scope "run" is how a run-scoped standing
// role is attached through this SAME endpoint, per dev-plan.md's T7.1 note.
type MeshAttachAndSendRequest struct {
	ProfileID    string   `json:"profile_id"`
	Scope        string   `json:"scope,omitempty"`
	StageID      string   `json:"stage_id,omitempty"`
	Mode         string   `json:"mode,omitempty"`
	Title        string   `json:"title,omitempty"`
	Instruction  string   `json:"instruction,omitempty"`
	FirstMessage *string  `json:"first_message,omitempty"`
	Recipients   []string `json:"recipient_ids,omitempty"`
	// OriginStage — for a run agent, the stage whose feed it was added from (its first thread shows there).
	OriginStage string `json:"origin_stage,omitempty"`
}

// MeshAttachAndSendResult is attach-and-send's response — string-ids mirror AttachAndSendResult's typed ids
// (orchestrator.AttachAndSendResult), rendered as plain JSON strings for the dashboard.
type MeshAttachAndSendResult struct {
	TaskID        string  `json:"task_id"`
	ParticipantID string  `json:"participant_id"`
	ThreadID      string  `json:"thread_id"`
	MessageID     *string `json:"message_id,omitempty"`
}

// Route segment names for /api/mesh/* — named constants so routeMesh's repeated literals (the bare
// "threads" segment is tested for POST, GET-list-not-implemented AND as segs[0] in the two sub-routes below)
// can never silently drift apart (goconst).
const (
	meshSegParticipants   = "participants"
	meshSegBoard          = "board"
	meshSegInbox          = "inbox"
	meshSegThreads        = "threads"
	meshSegAttachAndSend  = "attach-and-send"
	meshSegThreadMessages = "messages"
	meshSegThreadClose    = "close"
	meshSegStages         = "stages"
)

// routeMesh handles every /api/mesh/* route. Capability-off (s.mesh == nil) 404s before anything else, the
// same convention as routeAgentMesh/routeAssistants.
func (s *Server) routeMesh(w http.ResponseWriter, r *http.Request) {
	if s.mesh == nil {
		http.NotFound(w, r)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/mesh/")
	segs := strings.Split(path, "/")

	switch {
	case path == meshSegParticipants && r.Method == http.MethodGet:
		s.handleMeshParticipants(w, r)
	case path == meshSegBoard && r.Method == http.MethodGet:
		s.handleMeshBoard(w, r)
	case path == meshSegBoard && r.Method == http.MethodPost:
		s.handleMeshPublishBoard(w, r)
	case path == meshSegInbox && r.Method == http.MethodGet:
		s.handleMeshInbox(w, r)
	case path == meshSegThreads && r.Method == http.MethodGet:
		s.handleMeshThreads(w, r)
	case path == meshSegThreads && r.Method == http.MethodPost:
		s.handleMeshOpenThread(w, r)
	case path == meshSegAttachAndSend && r.Method == http.MethodPost:
		s.handleMeshAttachAndSend(w, r)
	case len(segs) == 2 && segs[0] == meshSegThreads && segs[1] != "" && r.Method == http.MethodGet:
		s.handleMeshReadThread(w, r, segs[1])
	case len(segs) == 3 && segs[0] == meshSegThreads && segs[1] != "" && segs[2] == meshSegThreadMessages && r.Method == http.MethodPost:
		s.handleMeshSend(w, r, segs[1])
	case len(segs) == 3 && segs[0] == meshSegThreads && segs[1] != "" && segs[2] == meshSegThreadClose && r.Method == http.MethodPost:
		s.handleMeshCloseThread(w, r, segs[1])
	case len(segs) == 3 && segs[0] == meshSegStages && segs[2] == meshSegThreadMessages && r.Method == http.MethodGet:
		s.handleMeshStageMessages(w, r, segs[1])
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleMeshParticipants(w http.ResponseWriter, r *http.Request) {
	participants, err := s.mesh.ListParticipants(r.Context())
	if err != nil {
		s.writeAssistantError(w, err)
		return
	}
	writeJSON(w, map[string]any{"participants": participants})
}

// handleMeshThreads is GET /api/mesh/threads (bare, list form) — the T7.3 dependency: every thread the human
// run owner belongs to (orchestrator.Service.ListThreads sees the human as belonging to every thread in the
// run, mirroring ReadThread's own carve-out). Like handleMeshParticipants/handleMeshBoard, this is a read with
// no actor to resolve and no same-origin requirement — see the other GET handlers on this surface.
func (s *Server) handleMeshThreads(w http.ResponseWriter, r *http.Request) {
	threads, err := s.mesh.Threads(r.Context())
	if err != nil {
		s.writeAssistantError(w, err)
		return
	}
	writeJSON(w, map[string]any{"threads": threads})
}

func (s *Server) handleMeshBoard(w http.ResponseWriter, r *http.Request) {
	entries, err := s.mesh.Board(r.Context(), r.URL.Query().Get("key"))
	if err != nil {
		s.writeAssistantError(w, err)
		return
	}
	writeJSON(w, map[string]any{"entries": entries})
}

func (s *Server) handleMeshInbox(w http.ResponseWriter, r *http.Request) {
	cursor, limit := parseCursorLimit(r)
	page, err := s.mesh.Inbox(r.Context(), cursor, limit)
	if err != nil {
		s.writeAssistantError(w, err)
		return
	}
	writeJSON(w, page)
}

func (s *Server) handleMeshReadThread(w http.ResponseWriter, r *http.Request, threadID string) {
	if !isValidStageID(threadID) { // same safe-path-component charset as a stage id
		writeAssistantErrCode(w, http.StatusBadRequest, "invalid_thread_id")
		return
	}
	cursor, limit := parseCursorLimit(r)
	page, err := s.mesh.Thread(r.Context(), threadID, cursor, limit)
	if err != nil {
		s.writeAssistantError(w, err)
		return
	}
	writeJSON(w, page)
}

// handleMeshStageMessages is GET /api/mesh/stages/{id}/messages?cursor=&limit= — the conversations a stage's
// feed shows, paged forward by cursor (the seq of the last message read).
func (s *Server) handleMeshStageMessages(w http.ResponseWriter, r *http.Request, stageID string) {
	if !isValidStageID(stageID) {
		writeAssistantErrCode(w, http.StatusBadRequest, "invalid_stage_id")
		return
	}
	cursor, limit := parseCursorLimit(r)
	page, err := s.mesh.StageMessages(r.Context(), stageID, cursor, limit)
	if err != nil {
		s.writeAssistantError(w, err)
		return
	}
	writeJSON(w, page)
}

// handleMeshOpenThread decodes ONLY the fields a dashboard operator is trusted to supply — there is no actor
// or sender field here for a client to set; the human identity is fixed server-side by the adapter.
func (s *Server) handleMeshOpenThread(w http.ResponseWriter, r *http.Request) {
	if !sameOriginOK(r) {
		writeAssistantErrCode(w, http.StatusForbidden, "forbidden_origin")
		return
	}
	var body struct {
		RequestID string `json:"request_id"`
		MeshOpenThreadRequest
	}
	if !decodeAgentMeshBody(w, r, &body) {
		return
	}
	if body.RequestID == "" {
		writeAssistantErrCode(w, http.StatusBadRequest, "request_id_required")
		return
	}
	thread, err := s.mesh.OpenThread(r.Context(), body.RequestID, body.MeshOpenThreadRequest)
	if err != nil {
		s.writeAssistantError(w, err)
		return
	}
	writeJSON(w, thread)
}

// handleMeshSend's thread id ALWAYS comes from the path, never the body: a MeshSendRequest.ThreadID the
// client might set is silently overwritten below, the same "path wins" rule handleAttachAssistant documents
// for stage id.
func (s *Server) handleMeshSend(w http.ResponseWriter, r *http.Request, threadID string) {
	if !sameOriginOK(r) {
		writeAssistantErrCode(w, http.StatusForbidden, "forbidden_origin")
		return
	}
	if !isValidStageID(threadID) {
		writeAssistantErrCode(w, http.StatusBadRequest, "invalid_thread_id")
		return
	}
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
	body.ThreadID = threadID
	receipt, err := s.mesh.Send(r.Context(), body.RequestID, threadID, body.MeshSendRequest)
	if err != nil {
		s.writeAssistantError(w, err)
		return
	}
	writeJSON(w, receipt)
}

func (s *Server) handleMeshCloseThread(w http.ResponseWriter, r *http.Request, threadID string) {
	if !sameOriginOK(r) {
		writeAssistantErrCode(w, http.StatusForbidden, "forbidden_origin")
		return
	}
	if !isValidStageID(threadID) {
		writeAssistantErrCode(w, http.StatusBadRequest, "invalid_thread_id")
		return
	}
	var body struct {
		RequestID string `json:"request_id"`
	}
	if !decodeAgentMeshBody(w, r, &body) {
		return
	}
	if body.RequestID == "" {
		writeAssistantErrCode(w, http.StatusBadRequest, "request_id_required")
		return
	}
	if err := s.mesh.CloseThread(r.Context(), body.RequestID, threadID); err != nil {
		s.writeAssistantError(w, err)
		return
	}
	writeJSON(w, map[string]string{keyStatus: "ok"})
}

// handleMeshAttachAndSend decodes into MeshAttachAndSendRequest ONLY — there is no command/model/executor
// field in that struct for a client to supply, mirroring handleAttachAssistant's own comment.
// handleMeshPublishBoard — POST /api/mesh/board: the human operator publishes a board entry (a task for the
// agents, a decision) and notifies the participants named in `notify`. Same-origin and request_id rules as
// every other dashboard mutation.
func (s *Server) handleMeshPublishBoard(w http.ResponseWriter, r *http.Request) {
	if !sameOriginOK(r) {
		writeAssistantErrCode(w, http.StatusForbidden, "forbidden_origin")
		return
	}
	var body struct {
		RequestID string `json:"request_id"`
		MeshBoardPublishRequest
	}
	if !decodeAgentMeshBody(w, r, &body) {
		return
	}
	if body.RequestID == "" {
		writeAssistantErrCode(w, http.StatusBadRequest, "request_id_required")
		return
	}
	entry, err := s.mesh.PublishBoard(r.Context(), body.RequestID, body.MeshBoardPublishRequest)
	if err != nil {
		s.writeAssistantError(w, err)
		return
	}
	writeJSON(w, entry)
}

func (s *Server) handleMeshAttachAndSend(w http.ResponseWriter, r *http.Request) {
	if !sameOriginOK(r) {
		writeAssistantErrCode(w, http.StatusForbidden, "forbidden_origin")
		return
	}
	var body struct {
		RequestID string `json:"request_id"`
		MeshAttachAndSendRequest
	}
	if !decodeAgentMeshBody(w, r, &body) {
		return
	}
	if body.RequestID == "" {
		writeAssistantErrCode(w, http.StatusBadRequest, "request_id_required")
		return
	}
	result, err := s.mesh.AttachAndSend(r.Context(), body.RequestID, body.MeshAttachAndSendRequest)
	if err != nil {
		s.writeAssistantError(w, err)
		return
	}
	writeJSON(w, result)
}
