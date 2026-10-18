package orchestrator

// Agent Mesh Phase 6, T6.6: a Proposal's provenance should name not just WHICH messages it is grounded in
// (SourceMessageIDs, T6.3/T6.4) but WHICH THREAD those messages live in — so a client can jump straight to
// the grounding conversation without re-deriving deriveDiscussionThreadID itself. Delivery (the accepted ->
// FeedbackIntent -> Revise path, proposal_delivery.go/feedback_intent.go) and CommitAddStage are UNCHANGED by
// this field — covered here only as regression assertions that they still behave, per dev-plan.md's "Done
// when" clause ("no proposal test regresses").

import (
	"context"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/state"
)

// TestProposal_LinksToThread is T6.6's core TDD assertion: a proposal created with SourceMessageIDs drawn
// from a stage's discussion also carries that discussion's mesh thread id — the SAME id
// deriveDiscussionThreadID computes and TestDiscussion_MirroredIntoMeshThreadWithSameID already proves the
// mirrored message is readable through.
func TestProposal_LinksToThread(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)

	msgID, err := o.PostDiscussionMessage(ctx, PostMessageRequest{
		OperationID: "op-thread-discuss", StageID: assistantsTestStageID, AuthorKind: assistants.AuthorHuman,
		Round: assistants.RoundFreeform, Body: "what about the retry loop?", Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Fatalf("PostDiscussionMessage: %v", err)
	}

	propID, err := o.CreateProposal(ctx, CreateProposalRequest{
		OperationID: "op-thread-propose", StageID: assistantsTestStageID, Type: assistants.ProposalFeedback,
		Title: "bound the retry loop", SourceMessageIDs: []string{msgID},
		Authors: []assistants.MessageAuthor{{Kind: assistants.AuthorAssistant, ID: "synth"}},
	})
	if err != nil {
		t.Fatalf("CreateProposal: %v", err)
	}

	prop, ok := o.Proposal(propID)
	if !ok {
		t.Fatalf("Proposal(%s): not found", propID)
	}
	wantThread := string(deriveDiscussionThreadID(assistantsTestStageID))
	if prop.ThreadID != wantThread {
		t.Fatalf("Proposal.ThreadID = %q, want %q (deriveDiscussionThreadID(%s))", prop.ThreadID, wantThread, assistantsTestStageID)
	}

	// The thread this id names must be the SAME one the source message actually lives in — not merely a
	// plausible-looking string.
	page, err := o.ReadThread(ctx, ReadThreadRequest{ThreadID: agentcomm.ThreadID(prop.ThreadID), Reader: meshUserParticipantID})
	if err != nil {
		t.Fatalf("ReadThread(%s): %v", prop.ThreadID, err)
	}
	found := false
	for _, m := range page.Messages {
		if string(m.ID) == msgID {
			found = true
		}
	}
	if !found {
		t.Fatalf("thread %s named by the proposal does not contain source message %s: %+v", prop.ThreadID, msgID, page.Messages)
	}
}

// TestProposal_LinksToThread_NoDiscussionYet proves ThreadID is still populated — a stable, correct,
// deterministic id — for a stage that has had NO discussion message posted at all, and that computing it
// does not force the thread into existence as a side effect of creating the proposal (CreateProposal must
// never mint mesh_thread_opened/mesh records nobody asked for).
func TestProposal_LinksToThread_NoDiscussionYet(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)

	propID, err := o.CreateProposal(ctx, CreateProposalRequest{
		OperationID: "op-thread-propose-empty", StageID: assistantsTestStageID, Type: assistants.ProposalFeedback,
		Title:   "a proposal with no prior discussion",
		Authors: []assistants.MessageAuthor{{Kind: assistants.AuthorAssistant, ID: "synth"}},
	})
	if err != nil {
		t.Fatalf("CreateProposal: %v", err)
	}

	prop, ok := o.Proposal(propID)
	if !ok {
		t.Fatalf("Proposal(%s): not found", propID)
	}
	wantThread := string(deriveDiscussionThreadID(assistantsTestStageID))
	if prop.ThreadID != wantThread {
		t.Fatalf("Proposal.ThreadID = %q, want the deterministic %q even with no discussion yet", prop.ThreadID, wantThread)
	}

	rs := store.Snapshot()
	if n := len(rs.Assistants.Mesh.ThreadOrder); n != 0 {
		t.Fatalf("CreateProposal must not force the thread into existence: got %d mesh threads, want 0", n)
	}
}
