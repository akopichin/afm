package state

import (
	"errors"
	"fmt"

	"github.com/akopichin/afm/pkg/assistants"
)

// DiscussionMessagePayload is the discussion_message control record's payload — decoded here by
// applyDiscussionMessage, encoded by pkg/orchestrator's discussionMessageEffect (T6.1). CreatedAt is
// deliberately NOT part of the payload: it is always stamped from the Control record's own Time at apply
// time, the same "never trust a timestamp a caller could fabricate" discipline record.go's other apply*
// functions already follow for derived fields.
type DiscussionMessagePayload struct {
	AuthorKind      string             `json:"author_kind"`
	AuthorID        string             `json:"author_id,omitempty"`
	Role            string             `json:"role,omitempty"`
	Executor        string             `json:"executor,omitempty"`
	Round           int                `json:"round"`
	ParentMessageID string             `json:"parent_message_id,omitempty"`
	ContextVersion  string             `json:"context_version,omitempty"`
	Evidence        []string           `json:"evidence,omitempty"`
	Body            string             `json:"body,omitempty"`
	Result          *assistants.Result `json:"result,omitempty"`
	// Mentions — the mesh participants the human pulled into this post with `@`, besides the stage agent.
	// Absent in journals written before mentions existed.
	Mentions []string `json:"mentions,omitempty"`
}

// discussionMessagesForStage returns stageID's discussion messages in commit order (as.Discussions.ByStage —
// never sorted by any message's own CreatedAt).
func discussionMessagesForStage(as *AssistantsState, stageID string) []assistants.DiscussionMessage {
	ids := as.Discussions.ByStage[stageID]
	return assistants.RestoreOrder(ids, as.Discussions.Messages)
}

// applyDiscussionMessage projects one discussion_message control record (T6.1). Round-limit enforcement
// happens HERE, before anything is mutated — a record that would exceed the one-independent +
// one-discussion (+ a separately-bounded synthesis) budget is rejected the same fail-closed way
// applyTaskCreated rejects a tree-invariant violation, so it never reaches the durable journal.
func applyDiscussionMessage(as *AssistantsState, c *Control, dryRun bool) error {
	var p DiscussionMessagePayload
	if err := decodePayload(c.Payload, &p); err != nil {
		return fmt.Errorf("decode discussion_message payload: %w", err)
	}
	id := c.Target.MessageID
	if id == "" {
		return errors.New("discussion_message: empty target message_id")
	}
	stageID := c.Target.StageID
	if stageID == "" {
		return errors.New("discussion_message: empty target stage_id")
	}
	if _, exists := as.Discussions.Messages[id]; exists {
		return fmt.Errorf("discussion_message: id %s already exists", id)
	}
	if p.ParentMessageID != "" {
		if _, ok := as.Discussions.Messages[p.ParentMessageID]; !ok {
			return fmt.Errorf("discussion_message %s: unknown parent_message_id %q", id, p.ParentMessageID)
		}
	}

	existing := discussionMessagesForStage(as, stageID)
	usage := assistants.ComputeRoundUsage(existing)
	if err := usage.Allow(p.Round); err != nil {
		return fmt.Errorf("stage %s discussion: %w", stageID, err)
	}

	if dryRun {
		return nil
	}

	msg := assistants.DiscussionMessage{
		ID:              id,
		Author:          assistants.MessageAuthor{Kind: assistants.MessageAuthorKind(p.AuthorKind), ID: p.AuthorID},
		Role:            p.Role,
		Executor:        p.Executor,
		Round:           p.Round,
		ParentMessageID: p.ParentMessageID,
		ContextVersion:  assistants.ContextVersion(p.ContextVersion),
		Evidence:        p.Evidence,
		Body:            p.Body,
		Result:          p.Result,
		Mentions:        p.Mentions,
		CreatedAt:       c.Time,
	}
	as.Discussions.Messages[id] = &msg
	as.Discussions.ByStage[stageID] = append(as.Discussions.ByStage[stageID], id)
	return nil
}
