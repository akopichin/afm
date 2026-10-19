package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

// Two stage agents running in parallel (impl, docs) — the live-run setup where agent-to-agent messaging
// failed: an agent had no way to open a thread, so every `afm agent send` hit "unknown thread" → HTTP 500.
func newTwoStageMeshOrchestrator(t *testing.T) (*Orchestrator, *state.Store) {
	t.Helper()
	o, store := newAssistantsTestOrchestrator(t, true, flow.Stage{ID: "docs", Command: "claude"})
	activateStage(t, o, assistantsTestStageID)
	activateStage(t, o, "docs")
	return o, store
}

func agentSend(o *Orchestrator, opID, sender string, to []string, body string) (SendReceipt, error) {
	recipients := make([]agentcomm.ParticipantID, len(to))
	for i, r := range to {
		recipients[i] = agentcomm.ParticipantID(r)
	}
	return o.SendFromAgent(context.Background(), SendRequest{
		OperationID: opID, Sender: agentcomm.ParticipantID(sender), Recipients: recipients,
		Kind: agentcomm.MessageKindRequest, Body: body, Actor: state.Actor{Kind: "agent", ID: sender},
	})
}

func TestSendFromAgent_WithoutThreadStartsAConversation(t *testing.T) {
	o, store := newTwoStageMeshOrchestrator(t)
	docs := "stage:docs"
	impl := string(meshStageParticipantID(assistantsTestStageID))

	r, err := agentSend(o, "op-docs-ask", docs, []string{impl}, "what does an invalid dueDate return?")
	if err != nil {
		t.Fatalf("SendFromAgent without a thread: %v", err)
	}
	if r.ThreadID == "" || r.MessageID == "" {
		t.Fatalf("receipt = %+v, want the thread it went into and the message id", r)
	}
	th := store.Snapshot().Assistants.Mesh.Threads[r.ThreadID]
	if th == nil || len(agentMemberSet(th.Members)) != 2 {
		t.Fatalf("opened thread = %+v, want exactly the two agents (and the human)", th)
	}

	// The answer (no thread either) lands in the SAME conversation, not a second one.
	r2, err := agentSend(o, "op-impl-answer", impl, []string{docs}, "HTTP 400 {\"error\":\"dueDate must be ...\"}")
	if err != nil {
		t.Fatalf("answer: %v", err)
	}
	if r2.ThreadID != r.ThreadID {
		t.Fatalf("answer went to thread %s, want the existing conversation %s", r2.ThreadID, r.ThreadID)
	}
	if n := len(store.Snapshot().Assistants.Mesh.Threads); n != 1 {
		t.Fatalf("threads = %d, want 1", n)
	}
}

func TestSendFromAgent_RetryOfTheSameRequestIsIdempotent(t *testing.T) {
	o, store := newTwoStageMeshOrchestrator(t)
	impl := string(meshStageParticipantID(assistantsTestStageID))
	r1, err := agentSend(o, "op-retry", "stage:docs", []string{impl}, "hi")
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	r2, err := agentSend(o, "op-retry", "stage:docs", []string{impl}, "hi")
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if r1 != r2 {
		t.Fatalf("retry receipt %+v != first %+v", r2, r1)
	}
	if n := len(store.Snapshot().Assistants.Mesh.Messages); n != 1 {
		t.Fatalf("messages = %d, want 1", n)
	}
}

func TestSendFromAgent_RecipientsByAlias(t *testing.T) {
	o, store := newTwoStageMeshOrchestrator(t)
	for _, alias := range []string{"impl", "@impl", "IMPL"} {
		r, err := agentSend(o, "op-alias-"+alias, "stage:docs", []string{alias}, "hi")
		if err != nil {
			t.Fatalf("to %q: %v", alias, err)
		}
		msg := store.Snapshot().Assistants.Mesh.Messages[r.MessageID]
		if len(msg.Recipients) != 1 || msg.Recipients[0] != meshStageParticipantID(assistantsTestStageID) {
			t.Fatalf("to %q: recipients = %v, want the impl stage agent", alias, msg.Recipients)
		}
	}
}

func TestSendFromAgent_UnknownRecipientIsNotFound(t *testing.T) {
	o, _ := newTwoStageMeshOrchestrator(t)
	_, err := agentSend(o, "op-nobody", "stage:docs", []string{"nobody"}, "hi")
	var rej *state.OperationRejectedError
	if !errors.As(err, &rej) || rej.Code != assistants.CodeNotFound {
		t.Fatalf("err = %v, want not_found", err)
	}
}

func TestSendFromAgent_ReplyWithoutThreadUsesTheRepliedMessagesThread(t *testing.T) {
	o, _ := newTwoStageMeshOrchestrator(t)
	impl := string(meshStageParticipantID(assistantsTestStageID))
	ask, err := agentSend(o, "op-ask", "stage:docs", []string{impl}, "question?")
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	in := ask.MessageID
	r, err := o.SendFromAgent(context.Background(), SendRequest{
		OperationID: "op-reply", Sender: agentcomm.ParticipantID(impl), Kind: agentcomm.MessageKindReply,
		InReplyTo: &in, Body: "answer", Actor: state.Actor{Kind: "agent", ID: impl},
	})
	if err != nil {
		t.Fatalf("reply without thread/recipients: %v", err)
	}
	if r.ThreadID != ask.ThreadID {
		t.Fatalf("reply thread = %s, want %s", r.ThreadID, ask.ThreadID)
	}
}

// An explicit thread id is still honored, and an unknown one is a clear not_found (no more bare 500).
func TestSendFromAgent_UnknownExplicitThreadIsNotFound(t *testing.T) {
	o, _ := newTwoStageMeshOrchestrator(t)
	impl := string(meshStageParticipantID(assistantsTestStageID))
	_, err := o.SendFromAgent(context.Background(), SendRequest{
		OperationID: "op-made-up", ThreadID: "docs-api-validation", Sender: "stage:docs",
		Recipients: []agentcomm.ParticipantID{agentcomm.ParticipantID(impl)}, Kind: agentcomm.MessageKindMessage, Body: "hi",
	})
	var rej *state.OperationRejectedError
	if !errors.As(err, &rej) || rej.Code != assistants.CodeNotFound {
		t.Fatalf("err = %v, want not_found", err)
	}
}

// A retry of an already-committed send replays its outcome even if the world changed in between (here the
// conversation it went into was closed) — never a conflict or a second message.
func TestSendFromAgent_RetryAfterThreadClosedReplaysOriginal(t *testing.T) {
	o, store := newTwoStageMeshOrchestrator(t)
	impl := string(meshStageParticipantID(assistantsTestStageID))
	r1, err := agentSend(o, "op-r", "stage:docs", []string{impl}, "hi")
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := o.CloseThread(context.Background(), CloseThreadRequest{OperationID: "op-close", ThreadID: r1.ThreadID}); err != nil {
		t.Fatalf("close: %v", err)
	}
	r2, err := agentSend(o, "op-r", "stage:docs", []string{impl}, "hi")
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if r2 != r1 {
		t.Fatalf("retry = %+v, want the original %+v", r2, r1)
	}
	if n := len(store.Snapshot().Assistants.Mesh.Messages); n != 1 {
		t.Fatalf("messages = %d, want 1", n)
	}
}

// Two agents starting the same conversation at the same time end up in ONE thread.
func TestSendFromAgent_ConcurrentStartsShareOneThread(t *testing.T) {
	o, store := newTwoStageMeshOrchestrator(t)
	impl := string(meshStageParticipantID(assistantsTestStageID))
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i, pair := range [][2]string{{"stage:docs", impl}, {impl, "stage:docs"}} {
		wg.Go(func() {
			_, err := agentSend(o, fmt.Sprintf("op-concurrent-%d", i), pair[0], []string{pair[1]}, "hello")
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("send: %v", err)
		}
	}
	if n := len(store.Snapshot().Assistants.Mesh.Threads); n != 1 {
		t.Fatalf("threads = %d, want 1 shared conversation", n)
	}
}

// A request that cannot be sent is rejected before any thread is opened (no empty conversations left behind).
func TestSendFromAgent_InvalidRequestOpensNothing(t *testing.T) {
	o, store := newTwoStageMeshOrchestrator(t)
	impl := string(meshStageParticipantID(assistantsTestStageID))
	_, err := o.SendFromAgent(context.Background(), SendRequest{
		OperationID: "op-bad", Sender: "stage:docs", Recipients: []agentcomm.ParticipantID{agentcomm.ParticipantID(impl)},
		Kind: agentcomm.MessageKindReply, Body: "a reply to nothing", Actor: state.Actor{Kind: "agent"},
	})
	var rej *state.OperationRejectedError
	if !errors.As(err, &rej) || rej.Code != assistants.CodeInvalidRequest {
		t.Fatalf("err = %v, want invalid_request", err)
	}
	if n := len(store.Snapshot().Assistants.Mesh.Threads); n != 0 {
		t.Fatalf("threads = %d, want none opened", n)
	}
}

// Client-correctable mistakes are typed rejections (4xx with a reason), not plain errors that became a 500.
func TestMesh_CorrectableErrorsAreTyped(t *testing.T) {
	o, _ := newTwoStageMeshOrchestrator(t)
	ctx := context.Background()
	codeOf := func(err error) assistants.ErrorCode {
		var rej *state.OperationRejectedError
		if errors.As(err, &rej) {
			return rej.Code
		}
		return ""
	}

	_, err := o.ReadThread(ctx, ReadThreadRequest{ThreadID: "mt-nope", Reader: "stage:docs"})
	if codeOf(err) != assistants.CodeNotFound {
		t.Fatalf("ReadThread(unknown) = %v, want not_found", err)
	}
	err = o.Acknowledge(ctx, AcknowledgeRequest{OperationID: "op-ack", MessageID: "mm-nope", Participant: "stage:docs"})
	if codeOf(err) != assistants.CodeNotFound {
		t.Fatalf("Acknowledge(unknown) = %v, want not_found", err)
	}
	_, err = o.OpenThread(ctx, OpenThreadRequest{OperationID: "op-open-x", Scope: agentcomm.ScopeRun, Members: []agentcomm.ParticipantID{"stage:docs", "stage:ghost"}})
	if codeOf(err) != assistants.CodeNotFound {
		t.Fatalf("OpenThread(unknown member) = %v, want not_found", err)
	}
	_, err = o.OpenThread(ctx, OpenThreadRequest{OperationID: "op-open-y", Scope: "galaxy", Members: []agentcomm.ParticipantID{"stage:docs"}})
	if codeOf(err) != assistants.CodeInvalidRequest {
		t.Fatalf("OpenThread(bad scope) = %v, want invalid_request", err)
	}
}

// The stage agent's answer in its stage's discussion thread also shows up in the stage's Discussion (it used to
// stay only in the mesh, so the stage conversation log showed the human's question and never the answer).
func TestStageAgentReplyInDiscussionThreadIsMirroredToDiscussion(t *testing.T) {
	o, _ := newTwoStageMeshOrchestrator(t)
	ctx := context.Background()
	qID, err := o.PostDiscussionMessage(ctx, PostMessageRequest{
		OperationID: "op-disc-q", StageID: assistantsTestStageID, AuthorKind: assistants.AuthorHuman,
		Round: assistants.RoundFreeform, Body: "which status code for a bad dueDate?", Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Fatalf("PostDiscussionMessage: %v", err)
	}
	stageAgent := meshStageParticipantID(assistantsTestStageID)
	in := agentcomm.MessageID(qID)
	if _, err := o.SendFromAgent(ctx, SendRequest{
		OperationID: "op-disc-a", Sender: stageAgent, Kind: agentcomm.MessageKindReply, InReplyTo: &in,
		Body: "400 with a clear error", Actor: state.Actor{Kind: "agent", ID: string(stageAgent)},
	}); err != nil {
		t.Fatalf("reply: %v", err)
	}
	msgs := o.DiscussionMessages(assistantsTestStageID)
	if len(msgs) != 2 {
		t.Fatalf("discussion = %+v, want the question and the stage agent's answer", msgs)
	}
	last := msgs[1]
	if last.Author.Kind != assistants.AuthorMainAgent || last.Body != "400 with a clear error" || last.ParentMessageID != qID {
		t.Fatalf("mirrored answer = %+v", last)
	}
}

// The Team view can write straight into a stage's discussion thread; that message exists only in the mesh.
// The stage agent's reply to it is accepted, but it is a Team side conversation: it stays out of the
// Discussion, which shows only answers to its own messages.
func TestStageAgentReplyToTeamMessageInDiscussionThread(t *testing.T) {
	o, _ := newTwoStageMeshOrchestrator(t)
	ctx := context.Background()
	if _, err := o.PostDiscussionMessage(ctx, PostMessageRequest{
		OperationID: "op-disc-open", StageID: assistantsTestStageID, AuthorKind: assistants.AuthorHuman,
		Round: assistants.RoundFreeform, Body: "first question", Actor: state.Actor{Kind: "user"},
	}); err != nil {
		t.Fatalf("PostDiscussionMessage: %v", err)
	}
	stageAgent := meshStageParticipantID(assistantsTestStageID)
	fromTeam, err := o.Send(ctx, SendRequest{
		OperationID: "op-team-q", ThreadID: deriveDiscussionThreadID(assistantsTestStageID), Sender: meshUserParticipantID,
		Recipients: []agentcomm.ParticipantID{stageAgent}, Kind: agentcomm.MessageKindRequest, Body: "asked from Team", Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Fatalf("Send from Team: %v", err)
	}
	if _, err := o.SendFromAgent(ctx, SendRequest{
		OperationID: "op-team-a", Sender: stageAgent, Kind: agentcomm.MessageKindReply, InReplyTo: &fromTeam.MessageID,
		Body: "answered", Actor: state.Actor{Kind: "agent", ID: string(stageAgent)},
	}); err != nil {
		t.Fatalf("reply to a Team message in the discussion thread: %v", err)
	}
	for _, m := range o.DiscussionMessages(assistantsTestStageID) {
		if m.Body == "answered" {
			t.Fatalf("a reply to a Team-only message must not appear in the Discussion: %+v", m)
		}
	}
}

// userSend is the human's send from a stage feed's composer: recipients by alias or id, no thread.
func userSend(o *Orchestrator, opID, originStage string, to []string, body string) (SendReceipt, error) {
	recipients := make([]agentcomm.ParticipantID, len(to))
	for i, r := range to {
		recipients[i] = agentcomm.ParticipantID(r)
	}
	return o.SendFromAgent(context.Background(), SendRequest{
		OperationID: opID, Sender: meshUserParticipantID, Recipients: recipients, OriginStage: originStage,
		Kind: agentcomm.MessageKindMessage, Body: body, Actor: state.Actor{Kind: "user"},
	})
}

// The human writes to several people at once from a stage feed: one message to all of them, in one
// conversation opened in the same operation and shown in that feed; writing to the same people again continues it.
func TestSendFromAgent_UserSendsToASetFromAStageFeed(t *testing.T) {
	o, store := newTwoStageMeshOrchestrator(t)
	impl := meshStageParticipantID(assistantsTestStageID)

	r, err := userSend(o, "op-user-set", "docs", []string{"@docs", string(impl)}, "please both look at the API")
	if err != nil {
		t.Fatalf("user send to a set: %v", err)
	}
	rs := store.Snapshot()
	th := rs.Assistants.Mesh.Threads[r.ThreadID]
	if th == nil || th.OriginStage != "docs" {
		t.Fatalf("thread = %+v, want one opened from the docs feed", th)
	}
	if got := sortedMemberSet(th.Members); !slices.Equal(got, sortedMemberSet([]agentcomm.ParticipantID{meshUserParticipantID, "stage:docs", impl})) {
		t.Fatalf("members = %v, want the human and both stage agents", got)
	}
	msg := rs.Assistants.Mesh.Messages[r.MessageID]
	if msg == nil || !slices.Equal(sortedMemberSet(msg.Recipients), sortedMemberSet([]agentcomm.ParticipantID{"stage:docs", impl})) {
		t.Fatalf("message = %+v, want one message to both", msg)
	}

	again, err := userSend(o, "op-user-set-2", "docs", []string{"docs", "impl"}, "and one more thing")
	if err != nil {
		t.Fatalf("second send: %v", err)
	}
	if again.ThreadID != r.ThreadID {
		t.Fatalf("second send went to %s, want the same conversation %s", again.ThreadID, r.ThreadID)
	}
}

// A conversation with the same people that the stage's feed does not show is not reused for the human's send
// from that feed: the message would vanish from where it was written. A new conversation opens there instead.
func TestSendFromAgent_UserSendReusesOnlyAConversationTheFeedShows(t *testing.T) {
	o, store := newTwoStageMeshOrchestrator(t)
	first, err := userSend(o, "op-from-docs", "docs", []string{"docs"}, "hi from the docs feed")
	if err != nil {
		t.Fatalf("send from docs: %v", err)
	}
	fromImpl, err := userSend(o, "op-from-impl", assistantsTestStageID, []string{"docs"}, "hi from the impl feed")
	if err != nil {
		t.Fatalf("send from impl: %v", err)
	}
	if fromImpl.ThreadID == first.ThreadID {
		t.Fatalf("the impl feed's send went into the docs feed's conversation %s", first.ThreadID)
	}
	if th := store.Snapshot().Assistants.Mesh.Threads[fromImpl.ThreadID]; th == nil || th.OriginStage != assistantsTestStageID {
		t.Fatalf("thread = %+v, want one opened from the impl feed", th)
	}
	again, err := userSend(o, "op-from-impl-2", assistantsTestStageID, []string{"docs"}, "again")
	if err != nil {
		t.Fatalf("second send from impl: %v", err)
	}
	if again.ThreadID != fromImpl.ThreadID {
		t.Fatalf("second send from impl went to %s, want %s", again.ThreadID, fromImpl.ThreadID)
	}
}

func TestSendFromAgent_UnknownOriginStageIsRejected(t *testing.T) {
	o, store := newTwoStageMeshOrchestrator(t)
	_, err := userSend(o, "op-ghost-origin", "ghost", []string{"docs"}, "hi")
	var rej *state.OperationRejectedError
	if !errors.As(err, &rej) || rej.Code != assistants.CodeInvalidRequest {
		t.Fatalf("err = %v, want invalid_request", err)
	}
	if n := len(store.Snapshot().Assistants.Mesh.Threads); n != 0 {
		t.Fatalf("threads = %d, want none opened", n)
	}
}

// Two stage feeds writing to the same agent at the same time each get a conversation their own feed shows:
// a new conversation's id also names the feed it is opened from, so the second open is never a no-op that
// drops its message into the other feed's thread.
func TestSendFromAgent_ConcurrentSendsFromTwoFeedsEachShowInTheirFeed(t *testing.T) {
	o, store := newTwoStageMeshOrchestrator(t)
	origins := []string{"docs", assistantsTestStageID}
	receipts := make([]SendReceipt, len(origins))
	errs := make([]error, len(origins))
	var wg sync.WaitGroup
	for i, origin := range origins {
		wg.Go(func() {
			receipts[i], errs[i] = userSend(o, "op-feed-"+origin, origin, []string{"docs"}, "hi from "+origin)
		})
	}
	wg.Wait()
	rs := store.Snapshot()
	for i, origin := range origins {
		if errs[i] != nil {
			t.Fatalf("send from %s: %v", origin, errs[i])
		}
		th := rs.Assistants.Mesh.Threads[receipts[i].ThreadID]
		if th == nil || !threadInStageFeed(rs, th, origin) {
			t.Fatalf("the message written from the %s feed went into %+v, which that feed does not show", origin, th)
		}
	}
	members := conversationMembers(meshUserParticipantID, []agentcomm.ParticipantID{"stage:docs"})
	if conversationOpenID(rs, members, "docs") == conversationOpenID(rs, members, assistantsTestStageID) {
		t.Fatal("a new conversation's id does not depend on the feed it is opened from")
	}
}

// An open conversation of the same agents that the human is not in (opened before D1) cannot take the human's
// message: the human's send from a feed opens a new conversation instead of failing the membership check.
func TestSendFromAgent_UserSendSkipsALegacyThreadWithoutTheHuman(t *testing.T) {
	o, store := newTwoStageMeshOrchestrator(t)
	legacy, err := o.OpenThread(context.Background(), OpenThreadRequest{
		OperationID: "op-legacy", Scope: agentcomm.ScopeRun, Members: []agentcomm.ParticipantID{"stage:docs", meshStageParticipantID(assistantsTestStageID)},
		OriginStage: "docs",
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	r, err := userSend(o, "op-user-legacy", "docs", []string{"docs", assistantsTestStageID}, "both of you")
	if err != nil {
		t.Fatalf("user send: %v", err)
	}
	if r.ThreadID == legacy.ID {
		t.Fatalf("the human's message went into the legacy conversation %s it is not a member of", legacy.ID)
	}
	if th := store.Snapshot().Assistants.Mesh.Threads[r.ThreadID]; th == nil || !slices.Contains(th.Members, meshUserParticipantID) {
		t.Fatalf("thread = %+v, want a new conversation with the human in it", th)
	}
}

// A retry under the same request id from another feed is another intent: a conflict, never the first send's
// receipt (its message shows in a different feed).
func TestSendFromAgent_SameRequestFromAnotherFeedIsAConflict(t *testing.T) {
	o, _ := newTwoStageMeshOrchestrator(t)
	if _, err := userSend(o, "op-same", "docs", []string{"docs"}, "hi"); err != nil {
		t.Fatalf("first send: %v", err)
	}
	if _, err := userSend(o, "op-same", "docs", []string{"docs"}, "hi"); err != nil {
		t.Fatalf("retry from the same feed: %v", err)
	}
	if _, err := userSend(o, "op-same", assistantsTestStageID, []string{"docs"}, "hi"); !errors.Is(err, state.ErrOperationIDConflict) {
		t.Fatalf("err = %v, want an operation id conflict", err)
	}
}
