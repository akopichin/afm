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
	"reflect"
	"slices"

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
//
// Agent Mesh Phase 6, T6.4: the SAME ApplyRunOperation call also mirrors this message into stageID's mesh
// thread (agentcomm_discussion.go's discussionMeshEffects) whenever the author resolves to a mesh identity —
// one durable record, one operation, the SAME message id readable through both DiscussionMessages and
// Service.ReadThread. Round-limit enforcement (ComputeRoundUsage/Allow, inside applyDiscussionMessage) runs
// as part of the SAME operation's pre-write validation (Store.ApplyRunOperation dry-runs every bundled effect
// before any of them is journaled — pkg/state/assistants_projection.go's applyOperationCommitted), so an
// over-limit post is rejected with NOTHING written at all, mesh included.
func (o *Orchestrator) PostDiscussionMessage(ctx context.Context, req PostMessageRequest) (string, error) {
	if !o.opts.AssistantsEnabled {
		return "", &state.OperationRejectedError{Code: assistants.CodeFeatureDisabled, Reason: "collaboration is disabled"}
	}
	if req.OperationID == "" {
		return "", errors.New("assistants: operation id is required")
	}
	messageID := deriveMessageID(req.OperationID)
	// A retry of an already-committed post replays it, whatever changed since (the stage finished, so the
	// mesh mirror would now be built differently).
	if replayed, err := o.replayedDiscussionPost(messageID, req); replayed {
		return messageID, err
	}
	// Redacted once, before either copy of the post (the discussion message and its mesh mirror) is built.
	req.Body = o.RedactSecrets(req.Body)
	rs := o.opts.Store.Snapshot()
	st := o.reg().Stage(req.StageID)
	if st == nil {
		return "", fmt.Errorf("assistants: unknown stage %q", req.StageID)
	}

	eff, err := discussionMessageEffect(req.StageID, messageID, state.DiscussionMessagePayload{
		AuthorKind: string(req.AuthorKind), AuthorID: req.AuthorID, Role: req.Role,
		Round: req.Round, ParentMessageID: req.ParentMessageID, ContextVersion: req.ContextVersion,
		Evidence: req.Evidence, Body: req.Body,
	})
	if err != nil {
		return "", err
	}
	effects := []state.Effect{eff}

	meshEffects, err := discussionMeshEffects(rs, req.StageID, messageID, req)
	if err != nil {
		return "", err
	}
	effects = append(effects, meshEffects...)
	beforeReady := readyWaitSet(rs)
	runCtx := o.runContext(ctx) // a resumed waiter must not die with the HTTP request.

	threadID := deriveDiscussionThreadID(req.StageID)
	op := state.Operation{ID: req.OperationID, Kind: "post_discussion_message", Actor: req.Actor, Effects: effects, Expect: state.Preconditions{
		// The mirror was built from a snapshot; whether its recipient stage is still reachable is decided again
		// under the commit's lock, so a stage finishing in between cannot get a deliverable receipt.
		Check: func(cur *state.RunState) error {
			again, err := discussionMeshEffects(*cur, req.StageID, messageID, req)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(again, meshEffects) {
				return &state.OperationRejectedError{Code: assistants.CodeStaleRevision, Reason: fmt.Sprintf("stage %s changed status while posting — post again", req.StageID)}
			}
			return nil
		},
	}}
	_, err = o.opts.Store.ApplyRunOperation(op, func(state.OperationResult) {
		o.publishNotice(bus.Event{Type: bus.EventDiscussionMessage, StageID: req.StageID, Data: map[string]any{
			"message_id": messageID, "author_kind": string(req.AuthorKind), "author_id": req.AuthorID, "round": req.Round,
		}})
		if len(meshEffects) == 0 {
			return
		}
		for _, m := range discussionThreadMembers(req.StageID) {
			o.publishNotice(bus.Event{Type: bus.EventMeshParticipant, Data: map[string]any{"participant_id": string(m)}})
		}
		o.publishNotice(bus.Event{Type: bus.EventMeshThread, Data: map[string]any{"thread_id": string(threadID)}})
		o.publishNotice(bus.Event{Type: bus.EventMeshMessage, Data: map[string]any{
			"message_id": messageID, "thread_id": string(threadID), "round": req.Round,
		}})
		// A discussion reply can be exactly what a stage agent parked on a mesh wait (e.g. an
		// addressed_message wait on this very thread) is waiting for — resume it the SAME way Send/Publish do
		// (agentcomm_resume.go), rather than leaving it stranded until something else happens to wake it.
		o.notifyAndResumeNewlyReadyWaits(runCtx, beforeReady)
	})
	if err != nil {
		return "", err
	}
	return messageID, nil
}

// replayedDiscussionPost reports whether req's request id already committed a post, and whether that post is
// exactly req (every field of its discussion message) — anything else under that id is a conflict.
func (o *Orchestrator) replayedDiscussionPost(messageID string, req PostMessageRequest) (bool, error) {
	op, committed, err := o.opts.Store.CommittedOperation(req.OperationID)
	if err != nil || !committed {
		return err != nil, err // a storage error answers the request too
	}
	var p state.DiscussionMessagePayload
	target, found, err := op.Effect("discussion_message", &p)
	if err != nil {
		return true, err
	}
	same := found && target.StageID == req.StageID && target.MessageID == messageID &&
		p.AuthorKind == string(req.AuthorKind) && p.AuthorID == req.AuthorID && p.Role == req.Role && p.Round == req.Round &&
		p.ParentMessageID == req.ParentMessageID && p.ContextVersion == req.ContextVersion &&
		slices.Equal(p.Evidence, req.Evidence) && o.sameRedactedText(p.Body, req.Body)
	if !same {
		return true, state.OperationIDConflictError(req.OperationID)
	}
	return true, nil
}

// DiscussionMessages returns stageID's discussion log in commit order (never re-sorted by timestamp —
// assistants.RestoreOrder).
func (o *Orchestrator) DiscussionMessages(stageID string) []assistants.DiscussionMessage {
	rs := o.opts.Store.Snapshot()
	ids := rs.Assistants.Discussions.ByStage[stageID]
	return assistants.RestoreOrder(ids, rs.Assistants.Discussions.Messages)
}
