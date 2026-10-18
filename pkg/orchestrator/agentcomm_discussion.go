// Package orchestrator (this file, Agent Mesh Phase 6, T6.4): mirrors a stage discussion message into that
// stage's own mesh thread, in the SAME ApplyRunOperation as discussion.go's discussion_message effect — so
// one PostDiscussionMessage call durably writes both records, under the SAME message id, and
// DiscussionMessages(stageID)/Service.ReadThread agree on identity without any reconciliation step.
//
// One stage thread per stage, created lazily: the thread id is derived from the stage id ALONE (not the
// posting operation's id), and every post rebuilds the identical mesh_thread_opened/mesh_participant_added
// effects from scratch — the same "re-derive on every call, let the projection dedupe a byte-identical
// re-add" discipline agentcomm_roster.go's ensureParticipantEffects already documents. applyMeshThreadOpened
// (pkg/state/mesh_projection.go) was extended to treat a byte-identical re-open as a no-op for exactly this
// reason: only the FIRST post to a given stage actually creates its thread, every later one is harmless.
package orchestrator

import (
	"fmt"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/state"
)

// discussionThreadMembers is the fixed member set of stageID's discussion mesh thread: the human and the
// stage's own main agent — the two roles every stage discussion already has by construction, long before any
// helper is ever attached. See discussionMeshSender's doc comment for why mirroring stops there for now.
func discussionThreadMembers(stageID string) []agentcomm.ParticipantID {
	return []agentcomm.ParticipantID{meshUserParticipantID, meshStageParticipantID(stageID)}
}

// deriveDiscussionThreadID returns stageID's one, permanent discussion-thread id — deterministic from the
// stage id alone (unlike deriveMeshThreadID, which is keyed on a caller-supplied OPERATION id and so mints a
// fresh thread per OpenThread call). Sharing the same namespace helper (deriveMeshID) as deriveMeshThreadID/
// deriveMeshMessageID/deriveMeshWaitID keeps this one more hash convention, not a new one.
func deriveDiscussionThreadID(stageID string) agentcomm.ThreadID {
	return agentcomm.ThreadID("mt-" + deriveMeshID("discussion-thread", stageID))
}

// discussionMeshSender maps a discussion message's author onto the mesh identity that should appear as the
// mirrored mesh message's Sender. ok=false means "do not mirror this post into the mesh" — today that is
// every AuthorKind except the two stable, always-known roles discussionThreadMembers fixes the thread's
// membership to. AuthorAssistant (a helper's own result) and AuthorAFM (system/synthesis bookkeeping) are not
// yet addressable under their own identity from a two-member thread with no mechanism to grow its member set
// after creation (agentcomm.Thread's own doc comment: "adding a colleague means opening a new thread, never
// mutating this one's Members") — widening the discussion thread to more roles is left to a later phase, not
// invented speculatively here. A post whose author does not map onto a mesh identity still durably records
// its discussion_message exactly as it did before this file existed — nothing about it is skipped or weakened.
func discussionMeshSender(stageID string, kind assistants.MessageAuthorKind) (agentcomm.ParticipantID, bool) {
	switch kind {
	case assistants.AuthorHuman:
		return meshUserParticipantID, true
	case assistants.AuthorMainAgent:
		return meshStageParticipantID(stageID), true
	default:
		return "", false
	}
}

// discussionMeshEffects returns the extra mesh_participant_added/mesh_thread_opened/mesh_message_sent effects
// PostDiscussionMessage (discussion.go) bundles into its single ApplyRunOperation call, alongside the
// discussion_message effect itself. messageID is reused VERBATIM from the caller (deriveMessageID, applied to
// the SAME operation id) as the mirrored mesh message's own id — the id-unification T6.4 requires.
//
// Returns (nil, nil) — no error, no effects — when discussionMeshSender reports the author cannot be mirrored
// yet; PostDiscussionMessage then commits only the discussion_message effect, unchanged from before T6.4.
func discussionMeshEffects(rs state.RunState, stageID, messageID string, req PostMessageRequest) ([]state.Effect, error) {
	sender, ok := discussionMeshSender(stageID, req.AuthorKind)
	if !ok {
		return nil, nil
	}

	members := discussionThreadMembers(stageID)
	effects, err := ensureParticipantEffects(rs, members)
	if err != nil {
		return nil, fmt.Errorf("discussion mesh mirror: %w", err)
	}

	memberStrs := make([]string, len(members))
	for i, m := range members {
		memberStrs[i] = string(m)
	}
	threadID := deriveDiscussionThreadID(stageID)
	threadEff, err := marshalEffect("mesh_thread_opened", state.Target{ThreadID: string(threadID)},
		state.MeshThreadOpenedPayload{Scope: string(agentcomm.ScopeStage), Members: memberStrs})
	if err != nil {
		return nil, err
	}
	effects = append(effects, threadEff)

	recipients := make([]string, 0, len(members)-1)
	for _, m := range members {
		if m != sender {
			recipients = append(recipients, string(m))
		}
	}
	body := redactMeshText(req.Body)
	msgEff, err := marshalEffect("mesh_message_sent", state.Target{ThreadID: string(threadID), MessageID: messageID},
		state.MeshMessageSentPayload{Sender: string(sender), Recipients: recipients, Kind: string(agentcomm.MessageKindMessage), Body: body})
	if err != nil {
		return nil, err
	}
	effects = append(effects, msgEff)

	// A post to a stage that has already finished keeps its place in the thread's history, but nobody will
	// ever read it: its receipt is undeliverable from the start, so it never counts as an unread message.
	for _, r := range recipients {
		p, ok := resolveParticipant(rs, agentcomm.ParticipantID(r))
		if !ok || p.Reachable {
			continue
		}
		undeliverable, err := marshalEffect("mesh_message_undeliverable", state.Target{MessageID: messageID, ParticipantID: r},
			state.MeshMessageReceiptPayload{Reason: p.UnreachableReason})
		if err != nil {
			return nil, err
		}
		effects = append(effects, undeliverable)
	}
	return effects, nil
}

// discussionMirrorOfStageReply is the way back of the discussion mirror: when a stage agent writes in its own
// stage's discussion thread (typically answering the human's discussion post), the same message — same id, in
// the same operation — is also recorded as the stage's discussion message, so the Discussion tab shows the
// answer and not only the question. Anything else is not mirrored (ok=false).
func discussionMirrorOfStageReply(req SendRequest, messageID agentcomm.MessageID, body string) (state.Effect, bool, error) {
	stageID, ok := stageIDFromMeshParticipant(req.Sender)
	if !ok || req.ThreadID != deriveDiscussionThreadID(stageID) {
		return state.Effect{}, false, nil
	}
	var parent string
	if req.InReplyTo != nil {
		parent = string(*req.InReplyTo)
	}
	eff, err := discussionMessageEffect(stageID, string(messageID), state.DiscussionMessagePayload{
		AuthorKind: string(assistants.AuthorMainAgent), AuthorID: string(req.Sender), Role: string(assistants.AuthorMainAgent),
		Round: assistants.RoundFreeform, ParentMessageID: parent, Body: body,
	})
	return eff, err == nil, err
}
