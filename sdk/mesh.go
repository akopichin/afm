package afmsdk

import (
	"context"
	"net/url"
	"strconv"
	"time"
)

const meshMessagesPath = "/api/mesh/messages"

// Participant is a currently addressable run member, including unreachable
// historical members kept in the roster.
type Participant struct {
	ID                  string `json:"id"`
	Kind                string `json:"kind"`
	Scope               string `json:"scope"`
	Alias               string `json:"alias,omitempty"`
	DisplayName         string `json:"display_name,omitempty"`
	StageID             string `json:"stage_id,omitempty"`
	Reachable           bool   `json:"reachable"`
	UnreachableReason   string `json:"unreachable_reason,omitempty"`
	Activity            string `json:"activity,omitempty"`
	Command             string `json:"command,omitempty"`
	ReadonlyEnforcement string `json:"readonly_enforcement,omitempty"`
}

type Thread struct {
	ID          string   `json:"id"`
	Title       string   `json:"title,omitempty"`
	Scope       string   `json:"scope"`
	Members     []string `json:"members"`
	Status      string   `json:"status"`
	OriginStage string   `json:"origin_stage,omitempty"`
}

type Message struct {
	ID           string    `json:"id"`
	ThreadID     string    `json:"thread_id"`
	SenderID     string    `json:"sender_id"`
	RecipientIDs []string  `json:"recipient_ids"`
	Kind         string    `json:"kind"`
	InReplyTo    string    `json:"in_reply_to,omitempty"`
	Body         string    `json:"body"`
	AutoResult   bool      `json:"auto_result,omitempty"`
	Seq          uint64    `json:"seq"`
	CreatedAt    time.Time `json:"created_at"`
}

type MessagePage struct {
	Messages   []Message `json:"messages"`
	NextCursor uint64    `json:"next_cursor"`
}

type StageMessagePage struct {
	Messages   []Message `json:"messages"`
	Threads    []Thread  `json:"threads"`
	NextCursor uint64    `json:"next_cursor"`
	HasMore    bool      `json:"has_more"`
}

type MeshSendRequest struct {
	RequestID    string   `json:"request_id"`
	ThreadID     string   `json:"thread_id,omitempty"`
	Recipients   []string `json:"recipient_ids,omitempty"`
	Kind         string   `json:"kind"`
	InReplyTo    string   `json:"in_reply_to,omitempty"`
	Body         string   `json:"body"`
	OriginStage  string   `json:"origin_stage,omitempty"`
	ArtifactRefs []string `json:"artifact_refs,omitempty"`
}

type MessageReceipt struct {
	MessageID string `json:"message_id"`
	ThreadID  string `json:"thread_id,omitempty"`
	Seq       uint64 `json:"seq,omitempty"`
}

type AttachAndSendRequest struct {
	RequestID    string   `json:"request_id"`
	ProfileID    string   `json:"profile_id"`
	Scope        string   `json:"scope,omitempty"`
	StageID      string   `json:"stage_id,omitempty"`
	Mode         string   `json:"mode,omitempty"`
	Title        string   `json:"title,omitempty"`
	Instruction  string   `json:"instruction,omitempty"`
	FirstMessage *string  `json:"first_message,omitempty"`
	Recipients   []string `json:"recipient_ids,omitempty"`
	OriginStage  string   `json:"origin_stage,omitempty"`
}

type AttachAndSendResult struct {
	TaskID        string       `json:"task_id"`
	ParticipantID string       `json:"participant_id"`
	ThreadID      string       `json:"thread_id"`
	MessageID     *string      `json:"message_id,omitempty"`
	Participant   *Participant `json:"participant,omitempty"`
}

func pageQuery(cursor uint64, limit int) string {
	v := url.Values{}
	v.Set("cursor", strconv.FormatUint(cursor, 10))
	v.Set("limit", strconv.Itoa(limit))
	return "?" + v.Encode()
}

func (r *Run) Participants(ctx context.Context) ([]Participant, error) {
	var out struct {
		Participants []Participant `json:"participants"`
	}
	err := r.getJSON(ctx, "/api/mesh/participants", &out)
	return out.Participants, err
}

// Threads lists all human-visible team conversations, including those absent
// from the user's addressed-message inbox.
func (r *Run) Threads(ctx context.Context) ([]Thread, error) {
	var out struct {
		Threads []Thread `json:"threads"`
	}
	err := r.getJSON(ctx, "/api/mesh/threads", &out)
	return out.Threads, err
}

func (r *Run) Inbox(ctx context.Context, cursor uint64, limit int) (MessagePage, error) {
	var out MessagePage
	err := r.getJSON(ctx, "/api/mesh/inbox"+pageQuery(cursor, limit), &out)
	return out, err
}

func (r *Run) Thread(ctx context.Context, threadID string, cursor uint64, limit int) (MessagePage, error) {
	var out MessagePage
	err := r.getJSON(ctx, "/api/mesh/threads/"+url.PathEscape(threadID)+pageQuery(cursor, limit), &out)
	return out, err
}

func (r *Run) StageMessages(ctx context.Context, stageID string, cursor uint64, limit int) (StageMessagePage, error) {
	var out StageMessagePage
	err := r.getJSON(ctx, "/api/mesh/stages/"+url.PathEscape(stageID)+"/messages"+pageQuery(cursor, limit), &out)
	return out, err
}

// Send uses a caller-supplied stable request ID. The same request can be
// retried after an ambiguous response without creating a second AFM message.
func (r *Run) Send(ctx context.Context, request MeshSendRequest) (MessageReceipt, error) {
	path := meshMessagesPath
	if request.ThreadID != "" {
		path = "/api/mesh/threads/" + url.PathEscape(request.ThreadID) + "/messages"
	}
	var out MessageReceipt
	err := r.postJSON(ctx, path, request, &out)
	return out, err
}

func (r *Run) AttachAndSend(ctx context.Context, request AttachAndSendRequest) (AttachAndSendResult, error) {
	var out AttachAndSendResult
	err := r.postJSON(ctx, "/api/mesh/attach-and-send", request, &out)
	return out, err
}
