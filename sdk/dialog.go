package afmsdk

import (
	"context"
	"time"
)

// DialogEntry is either an agent text entry (Type="agent_text") or a
// question/answer entry. Phase and ID identify the question to answer.
type DialogEntry struct {
	Type         string   `json:"type,omitempty"`
	Text         string   `json:"text,omitempty"`
	ID           string   `json:"id,omitempty"`
	Phase        string   `json:"phase"`
	TS           string   `json:"ts,omitempty"`
	Question     string   `json:"question,omitempty"`
	Options      []string `json:"options,omitempty"`
	AllowCustom  bool     `json:"allow_custom"`
	Answer       *string  `json:"answer,omitempty"`
	AnswerTS     string   `json:"answer_ts,omitempty"`
	FromOptions  bool     `json:"from_options,omitempty"`
	AutoAnswered bool     `json:"auto_answered,omitempty"`
}

type DialogAnswer struct {
	ID          string `json:"id"`
	Phase       string `json:"phase"`
	Answer      string `json:"answer"`
	FromOptions bool   `json:"from_options"`
}

type DiscussionMessage struct {
	ID              string    `json:"id"`
	AuthorKind      string    `json:"author_kind"`
	AuthorID        string    `json:"author_id,omitempty"`
	Role            string    `json:"role,omitempty"`
	Round           int       `json:"round"`
	ParentMessageID string    `json:"parent_message_id,omitempty"`
	Body            string    `json:"body,omitempty"`
	Mentions        []string  `json:"mentions,omitempty"`
	Interrupt       bool      `json:"interrupt,omitempty"`
	Unanswered      bool      `json:"unanswered,omitempty"`
	AutoResult      bool      `json:"auto_result,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	Seq             uint64    `json:"seq,omitempty"`
}

type DiscussionPage struct {
	Messages   []DiscussionMessage `json:"messages"`
	NextCursor uint64              `json:"next_cursor"`
	HasMore    bool                `json:"has_more"`
}

type DiscussionPost struct {
	RequestID string   `json:"request_id"`
	Body      string   `json:"body"`
	Mentions  []string `json:"mentions,omitempty"`
	Interrupt bool     `json:"interrupt"`
}

type AssistantProfile struct {
	ID                string `json:"id"`
	Title             string `json:"title"`
	Command           string `json:"command"`
	Available         bool   `json:"available"`
	UnavailableReason string `json:"unavailable_reason,omitempty"`
}

type AssistantProfilesResponse struct {
	Profiles       []AssistantProfile `json:"profiles"`
	MaxDepth       int                `json:"max_depth"`
	MaxDepthSource string             `json:"max_depth_source"`
}

func (r *Run) Dialog(ctx context.Context, stageID string) ([]DialogEntry, error) {
	var out []DialogEntry
	err := r.getJSON(ctx, stagePath(stageID, "/dialog"), &out)
	return out, err
}

func (r *Run) Answer(ctx context.Context, stageID string, answer DialogAnswer) error {
	return r.postJSON(ctx, stagePath(stageID, "/dialog/answer"), answer, nil)
}

func (r *Run) Discussion(ctx context.Context, stageID string, cursor uint64, limit int) (DiscussionPage, error) {
	var out DiscussionPage
	err := r.getJSON(ctx, stagePath(stageID, "/discussion")+pageQuery(cursor, limit), &out)
	return out, err
}

func (r *Run) PostDiscussion(ctx context.Context, stageID string, post DiscussionPost) (MessageReceipt, error) {
	var out MessageReceipt
	err := r.postJSON(ctx, stagePath(stageID, "/discussion"), post, &out)
	return out, err
}

func (r *Run) AssistantProfiles(ctx context.Context) (AssistantProfilesResponse, error) {
	var out AssistantProfilesResponse
	err := r.getJSON(ctx, "/api/assistant-profiles", &out)
	return out, err
}
