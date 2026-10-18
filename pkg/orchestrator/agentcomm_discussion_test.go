package orchestrator

import (
	"context"
	"errors"
	"testing"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/state"
)

// TestDiscussion_MirroredIntoMeshThreadWithSameID is T6.4's core TDD assertion: posting a discussion message
// durably writes a mesh message in the stage's own thread, readable through BOTH DiscussionMessages (the
// discussion API) and Service.ReadThread (the mesh API) — and the two APIs agree on the message's id.
func TestDiscussion_MirroredIntoMeshThreadWithSameID(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)

	msgID, err := o.PostDiscussionMessage(ctx, PostMessageRequest{
		OperationID: "op-discuss-1", StageID: assistantsTestStageID, AuthorKind: assistants.AuthorHuman,
		Round: assistants.RoundFreeform, Body: "what about the retry loop?", Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Fatalf("PostDiscussionMessage: %v", err)
	}
	if msgID == "" {
		t.Fatal("PostDiscussionMessage returned an empty message id")
	}

	discussionMsgs := o.DiscussionMessages(assistantsTestStageID)
	if len(discussionMsgs) != 1 || discussionMsgs[0].ID != msgID {
		t.Fatalf("DiscussionMessages: got %+v, want exactly one message with id %s", discussionMsgs, msgID)
	}

	threadID := deriveDiscussionThreadID(assistantsTestStageID)
	page, err := o.ReadThread(ctx, ReadThreadRequest{ThreadID: threadID, Reader: meshUserParticipantID})
	if err != nil {
		t.Fatalf("ReadThread: %v", err)
	}
	if len(page.Messages) != 1 {
		t.Fatalf("ReadThread: got %d messages, want 1 (%+v)", len(page.Messages), page.Messages)
	}
	if string(page.Messages[0].ID) != msgID {
		t.Fatalf("ReadThread message id = %q, want the SAME id as DiscussionMessages: %q", page.Messages[0].ID, msgID)
	}
	if page.Messages[0].Sender != meshUserParticipantID {
		t.Fatalf("mirrored message sender = %q, want %q", page.Messages[0].Sender, meshUserParticipantID)
	}
	if page.Messages[0].Body != "what about the retry loop?" {
		t.Fatalf("mirrored message body = %q, want the original body", page.Messages[0].Body)
	}
}

// TestDiscussion_MainAgentReplyAlsoMirrored exercises the other stable mesh identity discussionMeshSender
// resolves (AuthorMainAgent) — not just AuthorHuman, the only kind the live HTTP endpoint posts today.
func TestDiscussion_MainAgentReplyAlsoMirrored(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)

	msgID, err := o.PostDiscussionMessage(ctx, PostMessageRequest{
		OperationID: "op-discuss-agent-1", StageID: assistantsTestStageID, AuthorKind: assistants.AuthorMainAgent,
		Round: assistants.RoundFreeform, Body: "looking into it now", Actor: state.Actor{Kind: "system"},
	})
	if err != nil {
		t.Fatalf("PostDiscussionMessage: %v", err)
	}

	threadID := deriveDiscussionThreadID(assistantsTestStageID)
	page, err := o.ReadThread(ctx, ReadThreadRequest{ThreadID: threadID, Reader: meshUserParticipantID})
	if err != nil {
		t.Fatalf("ReadThread: %v", err)
	}
	if len(page.Messages) != 1 || string(page.Messages[0].ID) != msgID {
		t.Fatalf("ReadThread: got %+v, want exactly one message with id %s", page.Messages, msgID)
	}
	if page.Messages[0].Sender != meshStageParticipantID(assistantsTestStageID) {
		t.Fatalf("mirrored message sender = %q, want the stage agent", page.Messages[0].Sender)
	}
}

// TestDiscussion_SecondPostReusesTheSameThread proves "one stage thread per stage, created lazily": a second,
// unrelated post to the same stage must land in the SAME mesh thread as the first, not a new one.
func TestDiscussion_SecondPostReusesTheSameThread(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)

	if _, err := o.PostDiscussionMessage(ctx, PostMessageRequest{
		OperationID: "op-discuss-a", StageID: assistantsTestStageID, AuthorKind: assistants.AuthorHuman,
		Round: assistants.RoundFreeform, Body: "first", Actor: state.Actor{Kind: "user"},
	}); err != nil {
		t.Fatalf("first post: %v", err)
	}
	if _, err := o.PostDiscussionMessage(ctx, PostMessageRequest{
		OperationID: "op-discuss-b", StageID: assistantsTestStageID, AuthorKind: assistants.AuthorHuman,
		Round: assistants.RoundFreeform, Body: "second", Actor: state.Actor{Kind: "user"},
	}); err != nil {
		t.Fatalf("second post: %v", err)
	}

	rs := store.Snapshot()
	threadID := deriveDiscussionThreadID(assistantsTestStageID)
	if n := len(rs.Assistants.Mesh.ThreadOrder); n != 1 {
		t.Fatalf("want exactly one mesh thread for the stage, got %d (%v)", n, rs.Assistants.Mesh.ThreadOrder)
	}
	if ids := rs.Assistants.Mesh.MessagesByThread[threadID]; len(ids) != 2 {
		t.Fatalf("want both posts in the SAME thread %s, got %v", threadID, ids)
	}
}

// TestDiscussion_RoundLimitEnforcedBeforeMeshWrite is T6.4's second TDD assertion: an over-limit post is
// rejected before ANYTHING is written — discussion_message included, mesh included. ApplyRunOperation's
// dry-run-before-journal discipline (pkg/state/assistants_projection.go's applyOperationCommitted) is what
// makes this atomic; this test proves the end-to-end behavior, not just the projection unit.
func TestDiscussion_RoundLimitEnforcedBeforeMeshWrite(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)

	if _, err := o.PostDiscussionMessage(ctx, PostMessageRequest{
		OperationID: "op-ind-1", StageID: assistantsTestStageID, AuthorKind: assistants.AuthorMainAgent,
		Round: assistants.RoundIndependent, Body: "first independent review", Actor: state.Actor{Kind: "user"},
	}); err != nil {
		t.Fatalf("first independent post: %v", err)
	}

	_, err := o.PostDiscussionMessage(ctx, PostMessageRequest{
		OperationID: "op-ind-2", StageID: assistantsTestStageID, AuthorKind: assistants.AuthorMainAgent,
		Round: assistants.RoundIndependent, Body: "second independent review", Actor: state.Actor{Kind: "user"},
	})
	if !errors.Is(err, assistants.ErrRoundLimitExceeded) {
		t.Fatalf("second independent post: want ErrRoundLimitExceeded, got %v", err)
	}

	rs := store.Snapshot()
	if n := len(rs.Assistants.Discussions.ByStage[assistantsTestStageID]); n != 1 {
		t.Fatalf("discussion: want exactly 1 message after the rejected second post, got %d", n)
	}
	threadID := deriveDiscussionThreadID(assistantsTestStageID)
	if n := len(rs.Assistants.Mesh.MessagesByThread[threadID]); n != 1 {
		t.Fatalf("mesh thread: want exactly 1 message after the rejected second post (no partial mesh write), got %d", n)
	}
}

// TestDiscussion_UnmirrorableAuthorStillWritesDiscussionMessage proves the additive mesh mirror never
// regresses a post whose author does not resolve to a mesh identity today (AuthorAssistant/AuthorAFM —
// discussionMeshSender's doc comment) — PostDiscussionMessage must still succeed and record it exactly as it
// did before this file existed, just without a mesh mirror.
func TestDiscussion_UnmirrorableAuthorStillWritesDiscussionMessage(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)

	msgID, err := o.PostDiscussionMessage(ctx, PostMessageRequest{
		OperationID: "op-assistant-1", StageID: assistantsTestStageID, AuthorKind: assistants.AuthorAssistant,
		AuthorID: "qa", Round: assistants.RoundFreeform, Body: "assistant note", Actor: state.Actor{Kind: "assistant"},
	})
	if err != nil {
		t.Fatalf("PostDiscussionMessage: %v", err)
	}

	discussionMsgs := o.DiscussionMessages(assistantsTestStageID)
	if len(discussionMsgs) != 1 || discussionMsgs[0].ID != msgID {
		t.Fatalf("DiscussionMessages: got %+v, want exactly one message with id %s", discussionMsgs, msgID)
	}

	rs := store.Snapshot()
	if n := len(rs.Assistants.Mesh.ThreadOrder); n != 0 {
		t.Fatalf("an unmirrorable author must not create a mesh thread, got %d", n)
	}
}
