package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/orchestrator"
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
	Threads(ctx context.Context) (orchestrator.ThreadPage, error)
	OpenThread(ctx context.Context, requestID string, req MeshOpenThreadRequest) (agentcomm.Thread, error)
	CloseThread(ctx context.Context, requestID, threadID string) error
	UpdateThreadTitle(ctx context.Context, requestID, threadID string, req MeshTopicRequest) (orchestrator.UpdateThreadTitleReceipt, error)
	// Send is the human's message: into req.ThreadID, or — without one — to req.Recipients (aliases or ids)
	// in the conversation the Service picks or opens for them (orchestrator.SendFromAgent).
	Send(ctx context.Context, requestID string, req MeshUserSendRequest) (MeshSendReceipt, error)
	Thread(ctx context.Context, threadID string, cursor uint64, limit int) (MeshMessagePage, error)
	Inbox(ctx context.Context, cursor uint64, limit int) (MeshMessagePage, error)
	// StageMessages pages the conversation messages a stage's feed shows, in journal order, after cursor.
	StageMessages(ctx context.Context, stageID string, cursor uint64, limit int) (MeshStageMessagePage, error)
	Board(ctx context.Context, key string) ([]agentcomm.BoardEntry, error)
	// PublishBoard posts an entry to the shared board as the human operator (e.g. a task for the agents) and
	// tells req.Notify about it with one message.
	PublishBoard(ctx context.Context, requestID string, req MeshBoardPublishRequest) (agentcomm.BoardEntry, error)
	AttachAndSend(ctx context.Context, requestID string, req MeshAttachAndSendRequest) (MeshAttachAndSendResult, error)
	// BoardTasksEnabled reports whether the run keeps typed board tasks (created with the typed protocol).
	BoardTasksEnabled() bool
	// BoardTasks returns every typed board task, or only the one under key.
	BoardTasks(ctx context.Context, key string) ([]agentcomm.BoardTaskView, error)
	// BoardTask makes one change to a typed board task as the human operator.
	BoardTask(ctx context.Context, requestID string, req MeshBoardTaskRequest) (orchestrator.BoardTaskReceipt, error)
	// Loops lists every loop of the run; Loop adds or stops one as the human operator.
	Loops(ctx context.Context) (orchestrator.LoopPage, error)
	Loop(ctx context.Context, requestID string, cmd MeshLoopCommand) (orchestrator.LoopView, error)
}

// OwnerWaitActions is the run owner's recovery of a stage parked on peer waits. It is separate from MeshActions
// because its routes need the owner's own credential (Config.OwnerToken), not the dashboard's same-origin trust.
type OwnerWaitActions interface {
	// StageWaits lists the waits of one stage's agent; it only reads.
	StageWaits(ctx context.Context, stageID string) ([]orchestrator.StageWaitView, error)
	// CancelStageWaits ends the listed waits of the stage in one operation keyed by requestID, as the run owner,
	// and returns them as they stand afterwards — one that was answered first keeps that outcome.
	CancelStageWaits(ctx context.Context, requestID, stageID string, req OwnerCancelWaitsRequest) ([]orchestrator.StageWaitView, error)
}

// OwnerCancelWaitsRequest is what POST /api/mesh/stages/{id}/waits/cancel carries besides request_id.
type OwnerCancelWaitsRequest struct {
	WaitIDs []string `json:"wait_ids"`
	Reason  string   `json:"reason,omitempty"`
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

// MeshTopicRequest contains only the topic edit and its expected revision; the adapter supplies the actor.
type MeshTopicRequest struct {
	Title            string `json:"title"`
	ExpectedTitleSeq uint64 `json:"expected_title_seq"`
}

// MeshUserSendRequest is the body of the human's sends: POST /api/mesh/messages (no thread — the server
// resolves the recipients' names and picks or opens their conversation in the same operation) and POST
// /api/mesh/threads/{id}/messages (the path names the thread). OriginStage — the stage feed the message is
// written from: the conversation it goes into shows in that feed.
type MeshUserSendRequest struct {
	MeshSendRequest
	OriginStage string `json:"origin_stage,omitempty"`
}

// MeshStageMessagePage is GET /api/mesh/stages/{id}/messages' response: a page of the stage feed's
// conversation messages in journal order, the feed's threads opened or closed after the cursor (a client merges
// them by id, so a thread's status change arrives without a new message), and the cursor of the next page.
type MeshStageMessagePage struct {
	Messages           []agentcomm.Message          `json:"messages"`
	Threads            []agentcomm.Thread           `json:"threads"`
	NextCursor         uint64                       `json:"next_cursor"`
	HasMore            bool                         `json:"has_more"`
	SideAgentFailedIDs map[agentcomm.MessageID]bool `json:"-"`
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
	// Participant — the new participant's roster entry (alias included), so the dashboard can show and
	// address it before its next roster poll.
	Participant *agentcomm.Participant `json:"participant,omitempty"`
}

// Route segment names for /api/mesh/* — named constants so routeMesh's repeated literals (the bare
// "threads" segment is tested for POST, GET-list-not-implemented AND as segs[0] in the two sub-routes below)
// can never silently drift apart (goconst).
const (
	meshSegParticipants   = "participants"
	meshSegBoard          = "board"
	meshSegTasks          = "tasks"
	meshSegInbox          = "inbox"
	meshSegThreads        = "threads"
	meshSegAttachAndSend  = "attach-and-send"
	meshSegThreadMessages = "messages"
	meshSegThreadClose    = "close"
	meshSegThreadTopic    = "topic"
	meshSegStages         = "stages"
	meshSegWaits          = "waits"
	meshSegWaitsCancel    = "cancel"
	meshSegLoops          = "loops"
	meshSegLoop           = "loop"
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
	case path == meshSegTasks && r.Method == http.MethodPost:
		s.handleMeshBoardTask(w, r)
	case path == meshSegLoops && r.Method == http.MethodGet:
		s.handleMeshLoops(w, r)
	case path == meshSegLoop && r.Method == http.MethodPost:
		s.handleMeshLoop(w, r)
	case path == meshSegInbox && r.Method == http.MethodGet:
		s.handleMeshInbox(w, r)
	case path == meshSegThreads && r.Method == http.MethodGet:
		s.handleMeshThreads(w, r)
	case path == meshSegThreads && r.Method == http.MethodPost:
		s.handleMeshOpenThread(w, r)
	case path == meshSegAttachAndSend && r.Method == http.MethodPost:
		s.handleMeshAttachAndSend(w, r)
	case path == meshSegThreadMessages && r.Method == http.MethodPost:
		s.handleMeshSend(w, r, "")
	case len(segs) == 2 && segs[0] == meshSegThreads && segs[1] != "" && r.Method == http.MethodGet:
		s.handleMeshReadThread(w, r, segs[1])
	case len(segs) == 3 && segs[0] == meshSegThreads && segs[1] != "" && segs[2] == meshSegThreadMessages && r.Method == http.MethodPost:
		s.handleMeshSend(w, r, segs[1])
	case len(segs) == 3 && segs[0] == meshSegThreads && segs[1] != "" && segs[2] == meshSegThreadClose && r.Method == http.MethodPost:
		s.handleMeshCloseThread(w, r, segs[1])
	case len(segs) == 3 && segs[0] == meshSegThreads && segs[1] != "" && segs[2] == meshSegThreadTopic && r.Method == http.MethodPost:
		s.handleMeshUpdateThreadTitle(w, r, segs[1])
	case len(segs) == 3 && segs[0] == meshSegStages && segs[2] == meshSegThreadMessages && r.Method == http.MethodGet:
		s.handleMeshStageMessages(w, r, segs[1])
	case len(segs) == 3 && segs[0] == meshSegStages && segs[2] == meshSegWaits && r.Method == http.MethodGet:
		s.handleOwnerListWaits(w, r, segs[1])
	case len(segs) == 4 && segs[0] == meshSegStages && segs[2] == meshSegWaits && segs[3] == meshSegWaitsCancel && r.Method == http.MethodPost:
		s.handleOwnerCancelWaits(w, r, segs[1])
	default:
		http.NotFound(w, r)
	}
}

// authenticateOwner admits only the run owner's bearer token, before any body or Service access: Origin, loopback
// or mesh trust never stand in for it. Without a configured token the routes 404 — no unauthenticated mode.
func (s *Server) authenticateOwner(w http.ResponseWriter, r *http.Request) bool {
	if s.ownerWaits == nil || s.ownerTokenDigest == nil {
		http.NotFound(w, r)
		return false
	}
	scheme, token, found := strings.Cut(r.Header.Get("Authorization"), " ")
	if found && strings.EqualFold(scheme, "Bearer") && token != "" {
		// Compare digests so the check takes the same time whatever the length of what the caller sent.
		digest := sha256.Sum256([]byte(token))
		if subtle.ConstantTimeCompare(digest[:], s.ownerTokenDigest[:]) == 1 {
			w.Header().Set("Cache-Control", "no-store")
			return true
		}
	}
	w.Header().Set("WWW-Authenticate", "Bearer")
	writeAssistantErrCode(w, http.StatusUnauthorized, "unauthorized")
	return false
}

// ownerWaitsResponse is what both owner routes answer: the stage's waits, never null.
func ownerWaitsResponse(stageID string, waits []orchestrator.StageWaitView) map[string]any {
	if waits == nil {
		waits = []orchestrator.StageWaitView{}
	}
	return map[string]any{"stage_id": stageID, "waits": waits}
}

// handleOwnerListWaits is GET /api/mesh/stages/{id}/waits: the stage's waits, read-only.
func (s *Server) handleOwnerListWaits(w http.ResponseWriter, r *http.Request, stageID string) {
	if !s.authenticateOwner(w, r) {
		return
	}
	if !isValidStageID(stageID) {
		writeAssistantErrCode(w, http.StatusBadRequest, "invalid_stage_id")
		return
	}
	waits, err := s.ownerWaits.StageWaits(r.Context(), stageID)
	if err != nil {
		s.writeAssistantError(w, err)
		return
	}
	writeJSON(w, ownerWaitsResponse(stageID, waits))
}

// handleOwnerCancelWaits is POST /api/mesh/stages/{id}/waits/cancel: the owner ends the listed waits of the stage
// in one operation. Same-origin and request_id rules as every other dashboard mutation, after authentication.
func (s *Server) handleOwnerCancelWaits(w http.ResponseWriter, r *http.Request, stageID string) {
	if !s.authenticateOwner(w, r) {
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
	var body struct {
		RequestID string `json:"request_id"`
		OwnerCancelWaitsRequest
	}
	if !decodeAgentMeshBody(w, r, &body) {
		return
	}
	if body.RequestID == "" || len(body.WaitIDs) == 0 {
		writeAssistantErrCode(w, http.StatusBadRequest, "request_id_and_wait_ids_required")
		return
	}
	waits, err := s.ownerWaits.CancelStageWaits(r.Context(), body.RequestID, stageID, body.OwnerCancelWaitsRequest)
	if err != nil {
		s.writeAssistantError(w, err)
		return
	}
	writeJSON(w, ownerWaitsResponse(stageID, waits))
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
	page, err := s.mesh.Threads(r.Context())
	if err != nil {
		s.writeAssistantError(w, err)
		return
	}
	if page.Threads == nil {
		page.Threads = []agentcomm.Thread{}
	}
	if page.Summaries == nil {
		page.Summaries = map[agentcomm.ThreadID]orchestrator.ThreadSummary{}
	}
	writeJSON(w, page)
}

// handleMeshBoard is GET /api/mesh/board: the board's entries (all, or the one under ?key=) and typed_tasks —
// whether the run keeps typed board tasks; when it does, tasks lists every one of them whatever the key.
func (s *Server) handleMeshBoard(w http.ResponseWriter, r *http.Request) {
	entries, err := s.mesh.Board(r.Context(), r.URL.Query().Get("key"))
	if err != nil {
		s.writeAssistantError(w, err)
		return
	}
	resp := map[string]any{"entries": entries, "typed_tasks": false}
	if s.mesh.BoardTasksEnabled() {
		tasks, err := s.mesh.BoardTasks(r.Context(), "")
		if err != nil {
			s.writeAssistantError(w, err)
			return
		}
		resp["typed_tasks"] = true
		resp["tasks"] = boardTasksForClient(tasks)
	}
	writeJSON(w, resp)
}

// handleMeshBoardTask is POST /api/mesh/tasks: the human operator's change to a typed board task. Same-origin
// and request_id rules as every other dashboard mutation.
func (s *Server) handleMeshBoardTask(w http.ResponseWriter, r *http.Request) {
	if !sameOriginOK(r) {
		writeAssistantErrCode(w, http.StatusForbidden, "forbidden_origin")
		return
	}
	requestID, req, ok := decodeBoardTaskBody(w, r)
	if !ok {
		return
	}
	receipt, err := s.mesh.BoardTask(r.Context(), requestID, req)
	if err != nil {
		s.writeAssistantError(w, err)
		return
	}
	writeJSON(w, receipt)
}

// handleMeshLoops is GET /api/mesh/loops: every loop of the run, whoever created it.
func (s *Server) handleMeshLoops(w http.ResponseWriter, r *http.Request) {
	page, err := s.mesh.Loops(r.Context())
	if err != nil {
		s.writeAssistantError(w, err)
		return
	}
	writeJSON(w, loopsForClient(page))
}

// handleMeshLoop is POST /api/mesh/loop: the human operator adds or stops a loop. Same-origin and request_id
// rules as every other dashboard mutation; origin_stage is the Feed the loop is created from.
func (s *Server) handleMeshLoop(w http.ResponseWriter, r *http.Request) {
	if !sameOriginOK(r) {
		writeAssistantErrCode(w, http.StatusForbidden, "forbidden_origin")
		return
	}
	requestID, cmd, ok := decodeLoopBody(w, r)
	if !ok {
		return
	}
	view, err := s.mesh.Loop(r.Context(), requestID, cmd)
	if err != nil {
		s.writeAssistantError(w, err)
		return
	}
	writeJSON(w, view)
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
	writeJSON(w, map[string]any{"messages": meshMessagesForClient(page.Messages, page.SideAgentFailedIDs), "next_cursor": page.NextCursor})
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
	writeJSON(w, map[string]any{"messages": meshMessagesForClient(page.Messages, page.SideAgentFailedIDs), "threads": page.Threads, "next_cursor": page.NextCursor, "has_more": page.HasMore})
}

// meshMessagesForClient adds the durable side failure bit only to human read
// responses. The underlying Message and event journal keep their schema.
func meshMessagesForClient(messages []agentcomm.Message, failed map[agentcomm.MessageID]bool) []any {
	views := make([]any, 0, len(messages))
	for _, msg := range messages {
		if failed[msg.ID] {
			views = append(views, struct {
				agentcomm.Message
				SideAgentFailed bool `json:"side_agent_failed"`
			}{Message: msg, SideAgentFailed: true})
		} else {
			views = append(views, msg)
		}
	}
	return views
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

// handleMeshSend is both of the human's sends. With a path thread id (POST /api/mesh/threads/{id}/messages)
// that id ALWAYS wins: a body thread_id the client might set is overwritten, the same "path wins" rule
// handleAttachAssistant documents for stage id. Without one (POST /api/mesh/messages) the body may name a
// thread, or leave it to the Service to pick or open the recipients' conversation.
func (s *Server) handleMeshSend(w http.ResponseWriter, r *http.Request, pathThreadID string) {
	if !sameOriginOK(r) {
		writeAssistantErrCode(w, http.StatusForbidden, "forbidden_origin")
		return
	}
	if pathThreadID != "" && !isValidStageID(pathThreadID) {
		writeAssistantErrCode(w, http.StatusBadRequest, "invalid_thread_id")
		return
	}
	var body struct {
		RequestID string `json:"request_id"`
		MeshUserSendRequest
	}
	if !decodeAgentMeshBody(w, r, &body) {
		return
	}
	if body.RequestID == "" {
		writeAssistantErrCode(w, http.StatusBadRequest, "request_id_required")
		return
	}
	if pathThreadID != "" {
		body.ThreadID = pathThreadID
	}
	if body.ThreadID != "" && !isValidStageID(body.ThreadID) {
		writeAssistantErrCode(w, http.StatusBadRequest, "invalid_thread_id")
		return
	}
	if body.OriginStage != "" && !isValidStageID(body.OriginStage) {
		writeAssistantErrCode(w, http.StatusBadRequest, "invalid_stage_id")
		return
	}
	receipt, err := s.mesh.Send(r.Context(), body.RequestID, body.MeshUserSendRequest)
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

func (s *Server) handleMeshUpdateThreadTitle(w http.ResponseWriter, r *http.Request, threadID string) {
	if !sameOriginOK(r) {
		writeAssistantErrCode(w, http.StatusForbidden, "forbidden_origin")
		return
	}
	if !isValidStageID(threadID) {
		writeAssistantErrCode(w, http.StatusBadRequest, "invalid_thread_id")
		return
	}
	var body struct {
		RequestID        string  `json:"request_id"`
		Title            *string `json:"title"`
		ExpectedTitleSeq *uint64 `json:"expected_title_seq"`
	}
	if !decodeAgentMeshBody(w, r, &body) {
		return
	}
	if body.RequestID == "" {
		writeAssistantErrCode(w, http.StatusBadRequest, "request_id_required")
		return
	}
	if body.Title == nil || body.ExpectedTitleSeq == nil {
		writeAssistantErrCode(w, http.StatusBadRequest, "invalid_request_body")
		return
	}
	receipt, err := s.mesh.UpdateThreadTitle(r.Context(), body.RequestID, threadID, MeshTopicRequest{Title: *body.Title, ExpectedTitleSeq: *body.ExpectedTitleSeq})
	if err != nil {
		s.writeAssistantError(w, err)
		return
	}
	writeJSON(w, receipt)
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
