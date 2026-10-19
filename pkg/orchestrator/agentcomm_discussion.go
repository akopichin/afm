// Package orchestrator (this file, Agent Mesh Phase 6, T6.4): mirrors a stage discussion message into that
// stage's own mesh thread, in the SAME ApplyRunOperation as discussion.go's discussion_message effect — so
// one PostDiscussionMessage call durably writes both records, under the SAME message id, and
// DiscussionMessages(stageID)/Service.ReadThread agree on identity without any reconciliation step.
//
// One stage thread per stage and mention set, created lazily: the thread id is derived from the stage id and
// the sorted set of participants the human mentioned (not the posting operation's id; no mentions is the
// original per-stage thread), and every post rebuilds the identical mesh_thread_opened/mesh_participant_added
// effects from scratch — the same "re-derive on every call, let the projection dedupe a byte-identical
// re-add" discipline agentcomm_roster.go's ensureParticipantEffects already documents. applyMeshThreadOpened
// (pkg/state/mesh_projection.go) was extended to treat a byte-identical re-open as a no-op for exactly this
// reason: only the FIRST post to a given thread actually creates it, every later one is harmless.
package orchestrator

import (
	"fmt"
	"slices"
	"strings"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/state"
)

// discussionThreadMembers is the member set of a stage discussion thread: the human, the stage's own main
// agent and the participants the human mentioned (already normalized by normalizeDiscussionMentions). A
// thread can never grow members, so every distinct mention set is a thread of its own.
func discussionThreadMembers(stageID string, mentions []agentcomm.ParticipantID) []agentcomm.ParticipantID {
	return append([]agentcomm.ParticipantID{meshUserParticipantID, meshStageParticipantID(stageID)}, mentions...)
}

// deriveDiscussionThreadID returns stageID's base discussion-thread id (the human and the stage agent) —
// deterministic from the stage id alone (unlike deriveMeshThreadID, which is keyed on a caller-supplied
// OPERATION id and so mints a fresh thread per OpenThread call).
func deriveDiscussionThreadID(stageID string) agentcomm.ThreadID {
	return agentcomm.ThreadID("mt-" + deriveMeshID("discussion-thread", stageID))
}

// discussionThreadID returns the discussion thread of stageID with these extra members. No mentions is the
// base thread, so posts written before mentions existed keep their thread; otherwise the id also hashes the
// sorted mention set, so the same set always meets in the same thread.
func discussionThreadID(stageID string, mentions []agentcomm.ParticipantID) agentcomm.ThreadID {
	if len(mentions) == 0 {
		return deriveDiscussionThreadID(stageID)
	}
	ids := make([]string, len(mentions))
	for i, m := range mentions {
		ids[i] = string(m)
	}
	return agentcomm.ThreadID("mt-" + deriveMeshID("discussion-thread", stageID+"|"+strings.Join(ids, ",")))
}

// normalizeDiscussionMentions returns the mentions as a sorted set without the human and the stage agent
// itself — both are members of every discussion thread anyway.
func normalizeDiscussionMentions(stageID string, mentions []agentcomm.ParticipantID) []agentcomm.ParticipantID {
	var out []agentcomm.ParticipantID
	for _, m := range mentions {
		if m == "" || m == meshUserParticipantID || m == meshStageParticipantID(stageID) || slices.Contains(out, m) {
			continue
		}
		out = append(out, m)
	}
	slices.Sort(out)
	return out
}

// discussionStageOfThread maps a mesh thread back to the stage whose discussion it is: a thread is stage S's
// discussion thread when its id is the one discussionThreadID derives for S and the thread's other members.
// Recomputed from the projection, so no extra state is journaled for the reverse lookup.
func discussionStageOfThread(rs state.RunState, threadID agentcomm.ThreadID) (string, bool) {
	thread, ok := rs.Assistants.Mesh.Threads[threadID]
	if !ok || !slices.Contains(thread.Members, meshUserParticipantID) {
		return "", false
	}
	for _, m := range thread.Members {
		stageID, isStage := stageIDFromMeshParticipant(m)
		if !isStage {
			continue
		}
		extra := normalizeDiscussionMentions(stageID, thread.Members)
		if discussionThreadID(stageID, extra) == threadID {
			return stageID, true
		}
	}
	return "", false
}

// discussionMeshSender maps a discussion message's author onto the mesh identity that should appear as the
// mirrored mesh message's Sender. ok=false means "do not mirror this post into the mesh": AuthorAssistant (a
// helper's own result) and AuthorAFM (system/synthesis bookkeeping) posts are recorded in the discussion
// only. A post whose author does not map onto a mesh identity still durably records its discussion_message.
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
// req.Body is already redacted (PostDiscussionMessage): both copies of the post carry the same text.
func discussionMeshEffects(rs state.RunState, stageID, messageID string, req PostMessageRequest) ([]state.Effect, error) {
	sender, ok := discussionMeshSender(stageID, req.AuthorKind)
	if !ok {
		return nil, nil
	}

	mentions := normalizeDiscussionMentions(stageID, req.Mentions)
	members := discussionThreadMembers(stageID, mentions)
	effects, err := ensureParticipantEffects(rs, members)
	if err != nil {
		return nil, fmt.Errorf("discussion mesh mirror: %w", err)
	}

	memberStrs := make([]string, len(members))
	for i, m := range members {
		memberStrs[i] = string(m)
	}
	threadID := discussionThreadID(stageID, mentions)
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
	msgEff, err := marshalEffect("mesh_message_sent", state.Target{ThreadID: string(threadID), MessageID: messageID},
		state.MeshMessageSentPayload{Sender: string(sender), Recipients: recipients, Kind: string(agentcomm.MessageKindMessage), Body: req.Body})
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

// discussionMirrorOfReply is the way back of the discussion mirror: when any member but the human writes in
// one of a stage's discussion threads (typically answering the human's discussion post), the same message —
// same id, in the same operation — is also recorded as that stage's discussion message, so the Discussion tab
// shows the answer and not only the question. The stage agent appears as the main agent; anybody the human
// mentioned appears under their own participant id, with their mesh kind as the role. Anything else is not
// mirrored (ok=false).
//
// The reply keeps its parent only when the parent is itself a discussion message: the Team view can write into
// this thread directly, and such a message exists only in the mesh — naming it as the discussion parent would
// make the discussion projection reject the agent's whole reply.
func discussionMirrorOfReply(rs state.RunState, req SendRequest, messageID agentcomm.MessageID, body string) (state.Effect, bool, error) {
	if req.Sender == meshUserParticipantID {
		return state.Effect{}, false, nil
	}
	stageID, ok := discussionStageOfThread(rs, req.ThreadID)
	if !ok {
		return state.Effect{}, false, nil
	}
	var parent string
	if req.InReplyTo != nil {
		if _, inDiscussion := rs.Assistants.Discussions.Messages[string(*req.InReplyTo)]; inDiscussion {
			parent = string(*req.InReplyTo)
		}
	}
	author, role := assistants.AuthorMainAgent, string(assistants.AuthorMainAgent)
	if req.Sender != meshStageParticipantID(stageID) {
		author, role = assistants.AuthorAssistant, string(assistants.AuthorAssistant)
		if p, known := resolveParticipant(rs, req.Sender); known {
			role = string(p.Kind)
		}
	}
	eff, err := discussionMessageEffect(stageID, string(messageID), state.DiscussionMessagePayload{
		AuthorKind: string(author), AuthorID: string(req.Sender), Role: role,
		Round: assistants.RoundFreeform, ParentMessageID: parent, Body: body,
	})
	return eff, err == nil, err
}
