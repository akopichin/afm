package orchestrator

import (
	"context"
	"errors"
	"fmt"
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
	if th == nil || len(th.Members) != 2 {
		t.Fatalf("opened thread = %+v, want exactly the two agents", th)
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
