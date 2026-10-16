// Package orchestrator (this file, T6.1/T6.2/T6.7): the stage-discussion log. A discussion message NEVER
// touches stage FSM — posting into it is observational, exactly like a helper task's own result
// (dev-plan.md's "сообщение в обсуждении не меняет статус родительской стадии само по себе", §9.2,
// TestDiscussion_UserMessageIsNotAutoRevise). Delivering an ACCEPTED proposal to the live agent is a
// completely separate, explicit step — see feedback_intent.go.
package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// deriveMessageID computes a deterministic, path-safe discussion-message id from a PostDiscussionMessage
// operation id — the SAME OperationID always yields the SAME message id, mirroring deriveTaskID's rationale
// (assistants.go) for idempotent replay.
func deriveMessageID(operationID string) string {
	sum := sha256.Sum256([]byte("discussion-message|" + operationID))
	return "dm-" + hex.EncodeToString(sum[:])[:20]
}

// PostMessageRequest is a fully server-validated request to append one message to a stage's discussion.
type PostMessageRequest struct {
	OperationID     string
	StageID         string
	AuthorKind      assistants.MessageAuthorKind
	AuthorID        string
	Role            string
	Round           int
	ParentMessageID string
	ContextVersion  string
	Evidence        []string
	Body            string
	Actor           state.Actor
}

// discussionMessageEffect builds the discussion_message Effect pkg/state's applyDiscussionMessage decodes.
func discussionMessageEffect(stageID, messageID string, p state.DiscussionMessagePayload) (state.Effect, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return state.Effect{}, fmt.Errorf("marshal discussion_message payload: %w", err)
	}
	return state.Effect{Kind: "discussion_message", Target: state.Target{StageID: stageID, MessageID: messageID}, Payload: raw}, nil
}

// PostDiscussionMessage durably appends one message to stageID's discussion — a human reply, a forwarded
// note, or (internally) a participant's independent-round result. It NEVER triggers an FSM transition: the
// stage's status is completely unaffected by discussion traffic (TestDiscussion_UserMessageIsNotAutoRevise).
func (o *Orchestrator) PostDiscussionMessage(ctx context.Context, req PostMessageRequest) (string, error) {
	if !o.opts.AssistantsEnabled {
		return "", &state.OperationRejectedError{Code: assistants.CodeFeatureDisabled, Reason: "collaboration is disabled"}
	}
	if req.OperationID == "" {
		return "", errors.New("assistants: operation id is required")
	}
	st := o.reg().Stage(req.StageID)
	if st == nil {
		return "", fmt.Errorf("assistants: unknown stage %q", req.StageID)
	}

	messageID := deriveMessageID(req.OperationID)
	eff, err := discussionMessageEffect(req.StageID, messageID, state.DiscussionMessagePayload{
		AuthorKind: string(req.AuthorKind), AuthorID: req.AuthorID, Role: req.Role,
		Round: req.Round, ParentMessageID: req.ParentMessageID, ContextVersion: req.ContextVersion,
		Evidence: req.Evidence, Body: req.Body,
	})
	if err != nil {
		return "", err
	}

	op := state.Operation{ID: req.OperationID, Kind: "post_discussion_message", Actor: req.Actor, Effects: []state.Effect{eff}}
	_, err = o.opts.Store.ApplyRunOperation(op, func(state.OperationResult) {
		o.publishNotice(bus.Event{Type: bus.EventDiscussionMessage, StageID: req.StageID, Data: map[string]any{
			"message_id": messageID, "author_kind": string(req.AuthorKind), "author_id": req.AuthorID, "round": req.Round,
		}})
	})
	if err != nil {
		return "", err
	}
	return messageID, nil
}

// DiscussionMessages returns stageID's discussion log in commit order (never re-sorted by timestamp —
// assistants.RestoreOrder).
func (o *Orchestrator) DiscussionMessages(stageID string) []assistants.DiscussionMessage {
	rs := o.opts.Store.Snapshot()
	ids := rs.Assistants.Discussions.ByStage[stageID]
	return assistants.RestoreOrder(ids, rs.Assistants.Discussions.Messages)
}
