package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"
)

// DiscussionMessageView is one entry of a stage's discussion log — GET /api/stages/{id}/discussion.
type DiscussionMessageView struct {
	ID              string   `json:"id"`
	AuthorKind      string   `json:"author_kind"`
	AuthorID        string   `json:"author_id,omitempty"`
	Role            string   `json:"role,omitempty"`
	Executor        string   `json:"executor,omitempty"`
	Round           int      `json:"round"`
	ParentMessageID string   `json:"parent_message_id,omitempty"`
	ContextVersion  string   `json:"context_version,omitempty"`
	Evidence        []string `json:"evidence,omitempty"`
	Body            string   `json:"body,omitempty"`
	// Mentions — mesh participant ids the post was also addressed to with `@`.
	Mentions []string `json:"mentions,omitempty"`
	// Interrupt — the human asked the running stage agent to answer this post right away.
	Interrupt bool `json:"interrupt,omitempty"`
	// Unanswered — a human post of a finished stage that nobody replied to.
	Unanswered bool `json:"unanswered,omitempty"`
	// AutoResult — an agent's turn result afm posted as its reply; it woke nobody.
	AutoResult bool      `json:"auto_result,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	// Seq — the message's journal seq, the same order mesh messages carry; 0 when unknown (an older cache).
	Seq uint64 `json:"seq,omitempty"`
}

// DiscussionPost is one human post to a stage's discussion — the body of POST /api/stages/{id}/discussion.
type DiscussionPost struct {
	RequestID string
	Body      string
	// Mentions — mesh participant ids pulled into the post with `@`; they receive it besides the stage agent.
	Mentions []string
	// Interrupt — interrupt the running stage agent so it answers now; no effect for a stage not running.
	Interrupt bool
}

// DiscussionService is the read+write service for one stage's discussion log (T6.7, §13.1) — a SEPARATE,
// narrow interface from StageActions/AssistantActions, not a method bolted onto either. nil = capability off:
// both GET and POST 404, same convention as every other capability-gated service in this package.
type DiscussionService interface {
	// Messages returns a bounded page of stageID's discussion log strictly AFTER cursor, in stable (append)
	// order, plus the cursor for the next page and whether more are available.
	Messages(stageID string, cursor uint64, limit int) (messages []DiscussionMessageView, nextCursor uint64, hasMore bool, err error)
	// PostMessage appends one human (or forwarded) message to the discussion. This NEVER touches the parent
	// stage's FSM — a discussion message is purely observational (TestDiscussion_UserMessageIsNotAutoRevise).
	PostMessage(ctx context.Context, stageID string, post DiscussionPost) (messageID string, err error)
}

const (
	defaultDiscussionLimit       = 50
	maxDiscussionLimit           = 200
	maxDiscussionMessageBodyByte = 16 << 10
)

// routeStageDiscussion handles GET|POST /api/stages/{id}/discussion, dispatched from routeStages.
func (s *Server) routeStageDiscussion(w http.ResponseWriter, r *http.Request, stageID string) {
	if s.discussionService == nil {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.handleDiscussionGet(w, r, stageID)
	case http.MethodPost:
		s.handleDiscussionPost(w, r, stageID)
	default:
		writeAssistantErrCode(w, http.StatusMethodNotAllowed, "method_not_allowed")
	}
}

func (s *Server) handleDiscussionGet(w http.ResponseWriter, r *http.Request, stageID string) {
	if !isValidStageID(stageID) {
		writeAssistantErrCode(w, http.StatusBadRequest, "invalid_stage_id")
		return
	}
	cursor := parseUintQuery(r, "cursor")
	limit := defaultDiscussionLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= maxDiscussionLimit {
			limit = n
		}
	}
	messages, next, hasMore, err := s.discussionService.Messages(stageID, cursor, limit)
	if err != nil {
		s.writeAssistantError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"messages": messages, "next_cursor": next, "has_more": hasMore,
	})
}

func (s *Server) handleDiscussionPost(w http.ResponseWriter, r *http.Request, stageID string) {
	if !sameOriginOK(r) {
		writeAssistantErrCode(w, http.StatusForbidden, "forbidden_origin")
		return
	}
	if !isValidStageID(stageID) {
		writeAssistantErrCode(w, http.StatusBadRequest, "invalid_stage_id")
		return
	}
	var body struct {
		RequestID string   `json:"request_id"`
		Body      string   `json:"body"`
		Mentions  []string `json:"mentions"`
		Interrupt bool     `json:"interrupt"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxDiscussionMessageBodyByte)).Decode(&body); err != nil {
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
	if body.Body == "" {
		writeAssistantErrCode(w, http.StatusBadRequest, "body_required")
		return
	}
	id, err := s.discussionService.PostMessage(r.Context(), stageID, DiscussionPost{
		RequestID: body.RequestID, Body: body.Body, Mentions: body.Mentions, Interrupt: body.Interrupt,
	})
	if err != nil {
		s.writeAssistantError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"message_id": id})
}
