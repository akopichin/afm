package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

func TestMesh_OpenThreadRedactsSecretBeforeCommit(t *testing.T) {
	o, _ := newRedactingTestOrchestrator(t)
	ctx := context.Background()
	req := OpenThreadRequest{
		OperationID: "secret-open", Title: "Topic " + redactionTestSecret, Scope: agentcomm.ScopeStage,
		Members: []agentcomm.ParticipantID{meshUserParticipantID, meshStageParticipantID(assistantsTestStageID)},
		Actor:   state.Actor{Kind: "user"},
	}
	wantTitle := o.RedactSecrets(req.Title)
	first, err := o.OpenThread(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if first.Title != wantTitle {
		t.Fatalf("open receipt title = %q, want %q", first.Title, wantTitle)
	}
	page, err := o.ListThreads(ctx, ListThreadsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Threads) != 1 || page.Threads[0].Title != wantTitle {
		t.Fatalf("listed threads = %+v, want redacted title %q", page.Threads, wantTitle)
	}
	notices := noticesOfType(readVerifyNotices(t, o.opts.RunDir), bus.EventMeshThread)
	if len(notices) != 1 || notices[0].Data["title"] != wantTitle {
		t.Fatalf("thread notices = %+v, want redacted title %q", notices, wantTitle)
	}
	assertNoSecretIn(t, journalOf(o), filepath.Join(o.opts.RunDir, "notices.jsonl"))

	retry, err := o.OpenThread(ctx, req)
	if err != nil || !reflect.DeepEqual(retry, first) {
		t.Fatalf("open retry = %+v, %v; want %+v", retry, err, first)
	}
	if got := noticesOfType(readVerifyNotices(t, o.opts.RunDir), bus.EventMeshThread); len(got) != len(notices) {
		t.Fatalf("retry published another thread notice: %+v", got)
	}
	changed := req
	changed.Title = "Different topic"
	if _, err := o.OpenThread(ctx, changed); !errors.Is(err, state.ErrOperationIDConflict) {
		t.Fatalf("changed request with same id = %v, want conflict", err)
	}
	assertNoSecretIn(t, journalOf(o), filepath.Join(o.opts.RunDir, "notices.jsonl"))
}

func TestMesh_OpenThreadRetryOfLegacyUnredactedTitle(t *testing.T) {
	o, store := newRedactingTestOrchestrator(t)
	req := OpenThreadRequest{
		OperationID: "legacy-secret-open", Title: "Topic " + redactionTestSecret, Scope: agentcomm.ScopeStage,
		Members: []agentcomm.ParticipantID{meshUserParticipantID, meshStageParticipantID(assistantsTestStageID)},
		Actor:   state.Actor{Kind: "user"},
	}
	threadID := deriveMeshThreadID(req.OperationID)
	effects, err := threadOpenEffects(store.Snapshot(), threadID, req.Title, req.Scope, req.Members, req.MaxMessages, req.OriginStage)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyRunOperation(state.Operation{
		ID: req.OperationID, Kind: opKindMeshOpenThread, Actor: req.Actor, Effects: effects,
	}, nil); err != nil {
		t.Fatal(err)
	}
	beforeSeq := store.Snapshot().LastSeq
	beforeNotices := len(noticesOfType(readVerifyNotices(t, o.opts.RunDir), bus.EventMeshThread))
	retry, err := o.OpenThread(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if retry.ID != threadID || retry.Title != o.RedactSecrets(req.Title) {
		t.Fatalf("legacy retry = %+v, want id %s and redacted title", retry, threadID)
	}
	if got := store.Snapshot().LastSeq; got != beforeSeq {
		t.Fatalf("legacy retry committed another event: seq %d, want %d", got, beforeSeq)
	}
	if got := len(noticesOfType(readVerifyNotices(t, o.opts.RunDir), bus.EventMeshThread)); got != beforeNotices {
		t.Fatalf("legacy retry published a thread notice: got %d, want %d", got, beforeNotices)
	}
}

func TestMesh_UpdateThreadTitleRedactsSecretBeforeCommit(t *testing.T) {
	o, store := newRedactingTestOrchestrator(t)
	ctx := context.Background()
	thread, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "secret-topic-open", Title: "Original", Scope: agentcomm.ScopeStage,
		Members: []agentcomm.ParticipantID{meshUserParticipantID, meshStageParticipantID(assistantsTestStageID)},
		Actor:   state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	const rawTitle = "Topic " + redactionTestSecret
	want := o.RedactSecrets(rawTitle)
	if strings.Contains(want, redactionTestSecret) {
		t.Fatal("test secret was not configured for redaction")
	}
	req := UpdateThreadTitleRequest{OperationID: "secret-topic-edit", ThreadID: thread.ID,
		Title: " " + rawTitle + " ", ExpectedTitleSeq: 0, Actor: state.Actor{Kind: "user"}}
	first, err := o.UpdateThreadTitle(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if first.Title != want {
		t.Fatalf("title receipt = %q, want %q", first.Title, want)
	}
	if got := store.Snapshot().Assistants.Mesh.ThreadTitles[thread.ID].Title; got != want {
		t.Fatalf("stored title = %q, want %q", got, want)
	}
	page, err := o.ListThreads(ctx, ListThreadsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if got := page.Threads[0].Title; got != want {
		t.Fatalf("visible title = %q, want %q", got, want)
	}
	notices := noticesOfType(readVerifyNotices(t, o.opts.RunDir), bus.EventMeshThread)
	if got := notices[len(notices)-1].Data["title"]; got != want {
		t.Fatalf("title notice = %v, want %q", got, want)
	}
	assertNoSecretIn(t, journalOf(o), filepath.Join(o.opts.RunDir, "notices.jsonl"))

	if _, err := o.UpdateThreadTitle(ctx, UpdateThreadTitleRequest{OperationID: "secret-topic-later", ThreadID: thread.ID,
		Title: "Later", ExpectedTitleSeq: first.TitleSeq, Actor: state.Actor{Kind: "user"}}); err != nil {
		t.Fatal(err)
	}
	retry, err := o.UpdateThreadTitle(ctx, req)
	if err != nil || retry != first {
		t.Fatalf("retry = %+v, %v; want %+v", retry, err, first)
	}
	page, err = o.ListThreads(ctx, ListThreadsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if got := page.Threads[0].Title; got != "Later" {
		t.Fatalf("retry changed current title to %q", got)
	}
	assertNoSecretIn(t, journalOf(o), filepath.Join(o.opts.RunDir, "notices.jsonl"))
}

func TestMesh_UpdateThreadTitleRevisionRetryAndFeed(t *testing.T) {
	o, store := newTwoStageMeshOrchestrator(t)
	ctx := context.Background()
	open := OpenThreadRequest{OperationID: "topic-open", Title: "Original", Scope: agentcomm.ScopeStage,
		Members:     []agentcomm.ParticipantID{meshUserParticipantID, meshStageParticipantID(assistantsTestStageID)},
		OriginStage: assistantsTestStageID, Actor: state.Actor{Kind: "user"}}
	thread, err := o.OpenThread(ctx, open)
	if err != nil {
		t.Fatal(err)
	}
	sent, err := o.Send(ctx, SendRequest{OperationID: "topic-message", ThreadID: thread.ID,
		Sender: meshUserParticipantID, Recipients: []agentcomm.ParticipantID{meshStageParticipantID(assistantsTestStageID)},
		Kind: agentcomm.MessageKindMessage, Body: "hello", Actor: state.Actor{Kind: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	before := o.StageMessages(assistantsTestStageID, 0, 0).NextCursor
	first, err := o.UpdateThreadTitle(ctx, UpdateThreadTitleRequest{OperationID: "topic-1", ThreadID: thread.ID,
		Title: "Renamed", ExpectedTitleSeq: 0, Actor: state.Actor{Kind: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	if first.Title != "Renamed" || first.TitleSeq <= before {
		t.Fatalf("receipt = %+v", first)
	}
	threadNotices := noticesOfType(readVerifyNotices(t, o.opts.RunDir), bus.EventMeshThread)
	if got := threadNotices[len(threadNotices)-1].Data; got["action"] != "title_updated" || got["title"] != "Renamed" {
		t.Fatalf("rename notice = %+v", got)
	}
	page, err := o.ListThreads(ctx, ListThreadsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if page.Threads[0].Title != "Renamed" || page.Summaries[thread.ID].TitleSeq != first.TitleSeq {
		t.Fatalf("list = %+v", page)
	}
	if page.Summaries[thread.ID].LastMessage.Seq != sent.Seq {
		t.Fatal("title changed message recency")
	}
	if store.Snapshot().Assistants.Mesh.Threads[thread.ID].Title != "Original" {
		t.Fatal("opening title was mutated")
	}
	feed := o.StageMessages(assistantsTestStageID, before, 0)
	if len(feed.Messages) != 0 || len(feed.Threads) != 1 || feed.Threads[0].Title != "Renamed" {
		t.Fatalf("feed = %+v", feed)
	}
	if _, err := o.OpenThread(ctx, open); err != nil {
		t.Fatalf("original open retry: %v", err)
	}

	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, id := range []string{"topic-2a", "topic-2b"} {
		wg.Go(func() {
			_, err := o.UpdateThreadTitle(ctx, UpdateThreadTitleRequest{OperationID: id, ThreadID: thread.ID,
				Title: id, ExpectedTitleSeq: first.TitleSeq, Actor: state.Actor{Kind: "user"}})
			results <- err
		})
	}
	wg.Wait()
	close(results)
	okCount, staleCount := 0, 0
	for err := range results {
		if err == nil {
			okCount++
		} else {
			var rejected *state.OperationRejectedError
			if errors.As(err, &rejected) && rejected.Code == assistants.CodeStaleRevision {
				staleCount++
			} else {
				t.Fatalf("edit error: %v", err)
			}
		}
	}
	if okCount != 1 || staleCount != 1 {
		t.Fatalf("concurrent edits: success=%d stale=%d", okCount, staleCount)
	}
	retry, err := o.UpdateThreadTitle(ctx, UpdateThreadTitleRequest{OperationID: "topic-1", ThreadID: thread.ID,
		Title: "Renamed", ExpectedTitleSeq: 0, Actor: state.Actor{Kind: "user"}})
	if err != nil || retry != first {
		t.Fatalf("retry = %+v, %v; want %+v", retry, err, first)
	}
	if _, err := o.UpdateThreadTitle(ctx, UpdateThreadTitleRequest{OperationID: "topic-1", ThreadID: thread.ID,
		Title: "Different", ExpectedTitleSeq: 0, Actor: state.Actor{Kind: "user"}}); !errors.Is(err, state.ErrOperationIDConflict) {
		t.Fatalf("changed retry = %v, want request id conflict", err)
	}
	current, err := o.ListThreads(ctx, ListThreadsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	currentSeq := current.Summaries[thread.ID].TitleSeq
	if err := o.CloseThread(ctx, CloseThreadRequest{OperationID: "topic-close", ThreadID: thread.ID, Actor: state.Actor{Kind: "user"}}); err != nil {
		t.Fatal(err)
	}
	closeCursor := o.StageMessages(assistantsTestStageID, feed.NextCursor, 0).NextCursor
	clearReceipt, err := o.UpdateThreadTitle(ctx, UpdateThreadTitleRequest{OperationID: "topic-clear", ThreadID: thread.ID,
		Title: "", ExpectedTitleSeq: currentSeq, Actor: state.Actor{Kind: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	threadNotices = noticesOfType(readVerifyNotices(t, o.opts.RunDir), bus.EventMeshThread)
	if got := threadNotices[len(threadNotices)-1].Data; got["action"] != "title_updated" || got["title"] != "" {
		t.Fatalf("clear notice = %+v", got)
	}
	page, err = o.ListThreads(ctx, ListThreadsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if page.Threads[0].Title != "" || page.Threads[0].Status != agentcomm.ThreadClosed || page.Summaries[thread.ID].TitleSeq != clearReceipt.TitleSeq || page.Summaries[thread.ID].LastMessage.Seq != sent.Seq {
		t.Fatalf("cleared closed thread = %+v", page)
	}
	clearedFeed := o.StageMessages(assistantsTestStageID, closeCursor, 0)
	if len(clearedFeed.Messages) != 0 || len(clearedFeed.Threads) != 1 || clearedFeed.Threads[0].Title != "" || clearedFeed.Threads[0].Status != agentcomm.ThreadClosed || clearedFeed.NextCursor != clearReceipt.TitleSeq {
		t.Fatalf("caught-up feed after clear = %+v", clearedFeed)
	}
	replayed, err := state.LoadRunState(o.opts.RunDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := replayed.Assistants.Mesh.ThreadTitles[thread.ID]; got.Title != "" || got.Seq != clearReceipt.TitleSeq {
		t.Fatalf("replayed title = %+v", got)
	}
	if replayed.Assistants.Mesh.Threads[thread.ID].Title != "Original" {
		t.Fatal("replay changed opening descriptor")
	}
	if got, _ := replayed.Assistants.Mesh.ThreadView(thread.ID); got.Title != "" || got.Status != agentcomm.ThreadClosed {
		t.Fatalf("replayed view = %+v", got)
	}
}

func TestMesh_DiscussionPostAfterTitleUpdateKeepsOpeningDescriptor(t *testing.T) {
	o, store := newTwoStageMeshOrchestrator(t)
	ctx := context.Background()
	post := PostMessageRequest{
		OperationID: "discussion-before-title", StageID: assistantsTestStageID,
		AuthorKind: assistants.AuthorHuman, Round: assistants.RoundFreeform,
		Body: "first", Actor: state.Actor{Kind: "user"},
	}
	if _, err := o.PostDiscussionMessage(ctx, post); err != nil {
		t.Fatal(err)
	}
	threadID := deriveDiscussionThreadID(assistantsTestStageID)
	if _, err := o.UpdateThreadTitle(ctx, UpdateThreadTitleRequest{
		OperationID: "discussion-title-update", ThreadID: threadID,
		Title: "Renamed discussion", ExpectedTitleSeq: 0, Actor: state.Actor{Kind: "user"},
	}); err != nil {
		t.Fatal(err)
	}
	post.OperationID = "discussion-after-title"
	post.Body = "second"
	secondID, err := o.PostDiscussionMessage(ctx, post)
	if err != nil {
		t.Fatalf("post after title update: %v", err)
	}
	page, err := o.ReadThread(ctx, ReadThreadRequest{ThreadID: threadID, Reader: meshUserParticipantID})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 2 || page.Messages[1].ID != agentcomm.MessageID(secondID) || page.Messages[1].Body != "second" {
		t.Fatalf("mirrored discussion after title update = %+v", page)
	}
	if got := o.DiscussionMessages(assistantsTestStageID); len(got) != 2 || got[1].ID != secondID {
		t.Fatalf("discussion messages = %+v, want second message %s", got, secondID)
	}
	rs := store.Snapshot()
	if view, ok := rs.Assistants.Mesh.ThreadView(threadID); !ok || view.Title != "Renamed discussion" {
		t.Fatalf("thread view = %+v, %v, want renamed title", view, ok)
	}
	if got := rs.Assistants.Mesh.Threads[threadID].Title; got != "" {
		t.Fatalf("opening descriptor title changed to %q", got)
	}
}

// Agent Mesh Phase 2, T2.2: OpenThread/CloseThread/Send/ReadThread/ReadInbox/Acknowledge.

// newRunningHelperForMesh attaches a helper task and blocks its fake runHelperAgent on a channel this
// helper itself closes during t.Cleanup — so the goroutine runs to completion (and finishes writing its
// result) BEFORE the surrounding newAssistantsTestOrchestrator's store.Close cleanup runs (Cleanup funcs run
// LIFO, and this one is registered after that one). Blocking on ctx.Done() instead (which this test package
// never cancels) leaves the goroutine parked forever and races the *next* test's os.RemoveAll on an
// unrelated TempDir — observed as a flaky "directory not empty" cleanup failure.
func newRunningHelperForMesh(t *testing.T, o *Orchestrator, store *state.Store, opID string) assistants.TaskID {
	t.Helper()
	release := make(chan struct{})
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		awaitRelease(t, release)
		return passResultOutcome(), nil
	}
	taskID := attachTestTask(context.Background(), t, o, opID)
	waitForTaskStatus(t, store, taskID, assistants.StatusRunning, 2*time.Second)
	t.Cleanup(func() {
		close(release)
		waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 2*time.Second)
	})
	return taskID
}

func TestMesh_OpenThreadRegistersUnknownMembersLazily(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	taskID := newRunningHelperForMesh(t, o, store, "op-mt-1")
	helperID := meshHelperParticipantID(taskID)

	thread, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-open-1", Title: "chat", Scope: agentcomm.ScopeStage,
		Members: []agentcomm.ParticipantID{meshUserParticipantID, helperID}, Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}

	rs := store.Snapshot()
	if _, ok := rs.Assistants.Mesh.Participants[helperID]; !ok {
		t.Fatalf("helper participant %s was not lazily registered", helperID)
	}
	if _, ok := rs.Assistants.Mesh.Threads[thread.ID]; !ok {
		t.Fatalf("thread %s not found after OpenThread", thread.ID)
	}
}

// TestMesh_OpenThreadRetryAfterLazyRegistrationIsIdempotent is the regression test for a bug where retrying
// the EXACT SAME OpenThread call (same operation id) after it already lazily registered a member failed with
// ErrOperationIDConflict: ensureParticipantEffects used to skip members already present in Mesh.Participants,
// so the second call built a SHORTER effects list than the first — and ApplyRunOperation's idempotency check
// compares effects byte-for-byte. A client retrying a dropped response (the whole point of operation ids)
// must get back the ORIGINAL result, not a conflict.
func TestMesh_OpenThreadRetryAfterLazyRegistrationIsIdempotent(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	taskID := newRunningHelperForMesh(t, o, store, "op-mt-retry")
	helperID := meshHelperParticipantID(taskID)

	req := OpenThreadRequest{
		OperationID: "op-open-retry", Title: "chat", Scope: agentcomm.ScopeStage,
		Members: []agentcomm.ParticipantID{meshUserParticipantID, helperID}, Actor: state.Actor{Kind: "user"},
	}

	first, err := o.OpenThread(ctx, req)
	if err != nil {
		t.Fatalf("OpenThread (first): %v", err)
	}

	retry, err := o.OpenThread(ctx, req)
	if err != nil {
		t.Fatalf("OpenThread (retry of the same operation id): %v", err)
	}
	if retry.ID != first.ID {
		t.Fatalf("retry returned a different thread: first=%s retry=%s", first.ID, retry.ID)
	}
}

func TestMesh_SendRejectsNonMemberRecipientAndWritesNothing(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	taskID := newRunningHelperForMesh(t, o, store, "op-mt-2")
	helperID := meshHelperParticipantID(taskID)

	thread, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-open-2", Members: []agentcomm.ParticipantID{meshUserParticipantID, helperID}, Scope: agentcomm.ScopeStage,
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}

	before := store.Snapshot()
	beforeCount := len(before.Assistants.Mesh.Messages)

	_, err = o.Send(ctx, SendRequest{
		OperationID: "op-send-1", ThreadID: thread.ID, Sender: meshUserParticipantID,
		Recipients: []agentcomm.ParticipantID{helperID, "helper:does-not-exist"}, Kind: agentcomm.MessageKindMessage, Body: "hi",
	})
	if err == nil {
		t.Fatal("Send with a non-member recipient: want error, got nil")
	}

	after := store.Snapshot()
	if len(after.Assistants.Mesh.Messages) != beforeCount {
		t.Fatalf("Send partially wrote a message: before=%d after=%d", beforeCount, len(after.Assistants.Mesh.Messages))
	}
}

func TestMesh_SendIsIdempotentPerOperationID(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	taskID := newRunningHelperForMesh(t, o, store, "op-mt-3")
	helperID := meshHelperParticipantID(taskID)

	thread, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-open-3", Members: []agentcomm.ParticipantID{meshUserParticipantID, helperID}, Scope: agentcomm.ScopeStage,
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}

	req := SendRequest{OperationID: "op-send-repeat", ThreadID: thread.ID, Sender: meshUserParticipantID, Recipients: []agentcomm.ParticipantID{helperID}, Kind: agentcomm.MessageKindMessage, Body: "hi"}
	r1, err := o.Send(ctx, req)
	if err != nil {
		t.Fatalf("Send #1: %v", err)
	}
	r2, err := o.Send(ctx, req)
	if err != nil {
		t.Fatalf("Send #2 (repeat): %v", err)
	}
	if r1.MessageID != r2.MessageID {
		t.Fatalf("repeated Send produced a different message id: %s vs %s", r1.MessageID, r2.MessageID)
	}
	rs := store.Snapshot()
	if len(rs.Assistants.Mesh.MessagesByThread[thread.ID]) != 1 {
		t.Fatalf("repeated Send created more than one message: %d", len(rs.Assistants.Mesh.MessagesByThread[thread.ID]))
	}
}

func TestMesh_ReadThreadMembershipEnforced(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	taskID := newRunningHelperForMesh(t, o, store, "op-mt-4")
	helperID := meshHelperParticipantID(taskID)

	thread, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-open-4", Members: []agentcomm.ParticipantID{meshUserParticipantID, helperID}, Scope: agentcomm.ScopeStage,
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}

	if _, err := o.ReadThread(ctx, ReadThreadRequest{ThreadID: thread.ID, Reader: meshStageParticipantID(assistantsTestStageID)}); err == nil {
		t.Fatal("ReadThread by a non-member: want error, got nil")
	}
	if _, err := o.ReadThread(ctx, ReadThreadRequest{ThreadID: thread.ID, Reader: meshUserParticipantID}); err != nil {
		t.Fatalf("ReadThread by the human run owner: %v", err)
	}
	if _, err := o.ReadThread(ctx, ReadThreadRequest{ThreadID: thread.ID, Reader: helperID}); err != nil {
		t.Fatalf("ReadThread by a member: %v", err)
	}
}

func TestMesh_AcknowledgeTransitionsReceiptAndReadingDoesNotAck(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	taskID := newRunningHelperForMesh(t, o, store, "op-mt-5")
	helperID := meshHelperParticipantID(taskID)

	thread, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-open-5", Members: []agentcomm.ParticipantID{meshUserParticipantID, helperID}, Scope: agentcomm.ScopeStage,
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	send, err := o.Send(ctx, SendRequest{OperationID: "op-send-5", ThreadID: thread.ID, Sender: meshUserParticipantID, Recipients: []agentcomm.ParticipantID{helperID}, Kind: agentcomm.MessageKindMessage, Body: "hi"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	if _, err := o.ReadInbox(ctx, ReadInboxRequest{Participant: helperID}); err != nil {
		t.Fatalf("ReadInbox: %v", err)
	}
	rs := store.Snapshot()
	if got := rs.Assistants.Mesh.Receipts[send.MessageID][helperID]; got != agentcomm.DeliveryAccepted {
		t.Fatalf("reading the inbox must not advance the receipt: got %q, want accepted", got)
	}

	if err := o.Acknowledge(ctx, AcknowledgeRequest{OperationID: "op-ack-5", MessageID: send.MessageID, Participant: helperID}); err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}
	rs = store.Snapshot()
	if got := rs.Assistants.Mesh.Receipts[send.MessageID][helperID]; got != agentcomm.DeliveryAcknowledged {
		t.Fatalf("Acknowledge did not advance the receipt: got %q", got)
	}
}

// TestMesh_SendResolvesWaitRegisteredBeforeIt and TestMesh_WaitRegisteredAfterSendStartsReady together cover
// BOTH possible commit orderings between a Wait registration and the Send that satisfies it — the exact
// interleaving a codex review round flagged: Send used to decide which waits it resolved from a snapshot
// read BEFORE its own commit, so a Wait that committed in the gap between that snapshot and Send's commit
// would see an unsatisfied condition at registration AND never be resolved by Send either, stranding it
// Pending forever. Resolution now happens inside applyMeshMessageSent itself (pkg/state/mesh_projection.go),
// against live state under the commit's own lock, so the wait resolves correctly regardless of which
// operation's ApplyRunOperation call actually lands first.
func TestMesh_SendResolvesWaitRegisteredBeforeIt(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	taskID := newRunningHelperForMesh(t, o, store, "op-mt-7")
	helperID := meshHelperParticipantID(taskID)

	thread, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-open-7", Members: []agentcomm.ParticipantID{meshUserParticipantID, helperID}, Scope: agentcomm.ScopeStage,
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}

	// Order A: Wait registers FIRST (still unsatisfied — no message addressed to helperID yet).
	waitReceipt, err := o.Wait(ctx, WaitRequest{
		OperationID: "op-wait-7", Waiter: helperID,
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: thread.ID},
	})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if waitReceipt.Status != agentcomm.WaitPending {
		t.Fatalf("expected a fresh wait to start pending, got %q", waitReceipt.Status)
	}

	// Send commits SECOND — it must resolve the already-registered wait, not just its own message.
	if _, err := o.Send(ctx, SendRequest{
		OperationID: "op-send-7", ThreadID: thread.ID, Sender: meshUserParticipantID,
		Recipients: []agentcomm.ParticipantID{helperID}, Kind: agentcomm.MessageKindMessage, Body: "hi",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	rs := store.Snapshot()
	if got := rs.Assistants.Mesh.Waits[waitReceipt.WaitID].Status; got != agentcomm.WaitReady {
		t.Fatalf("wait status after Send = %q, want ready", got)
	}
}

func TestMesh_WaitRegisteredAfterSendStartsReady(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	taskID := newRunningHelperForMesh(t, o, store, "op-mt-8")
	helperID := meshHelperParticipantID(taskID)

	thread, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-open-8", Members: []agentcomm.ParticipantID{meshUserParticipantID, helperID}, Scope: agentcomm.ScopeStage,
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}

	// Order B: Send commits FIRST.
	if _, err := o.Send(ctx, SendRequest{
		OperationID: "op-send-8", ThreadID: thread.ID, Sender: meshUserParticipantID,
		Recipients: []agentcomm.ParticipantID{helperID}, Kind: agentcomm.MessageKindMessage, Body: "hi",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Wait registers SECOND — applyMeshWaitRegistered's own "already satisfied at registration" check
	// (meshWaitSatisfied) must see the message that already landed and start it Ready directly.
	waitReceipt, err := o.Wait(ctx, WaitRequest{
		OperationID: "op-wait-8", Waiter: helperID,
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: thread.ID},
	})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if waitReceipt.Status != agentcomm.WaitReady {
		t.Fatalf("wait registered after a satisfying Send: status = %q, want ready", waitReceipt.Status)
	}
}

// TestMesh_SendRetryAfterResolvingWaitStaysIdempotent regression-tests a second codex finding: Send used to
// bundle a mesh_wait_resolved effect for every wait it resolved INTO THE SAME operation as the message
// itself. On an identical retry (same operation_id), the wait is no longer Pending, so that effect would not
// be recomputed — the retry's effects list would differ from the original, and Store.ApplyRunOperation's
// idempotent-replay check (reconcileIdempotentRepeatLocked) would reject it with ErrOperationIDConflict
// instead of returning the original result. Send's own effect is now always just the one, deterministic
// mesh_message_sent effect — wait resolution is a side effect of applying it, not a separate effect — so a
// retry is a plain, stable replay.
func TestMesh_SendRetryAfterResolvingWaitStaysIdempotent(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	taskID := newRunningHelperForMesh(t, o, store, "op-mt-9")
	helperID := meshHelperParticipantID(taskID)

	thread, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-open-9", Members: []agentcomm.ParticipantID{meshUserParticipantID, helperID}, Scope: agentcomm.ScopeStage,
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	if _, err := o.Wait(ctx, WaitRequest{
		OperationID: "op-wait-9", Waiter: helperID,
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: thread.ID},
	}); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	req := SendRequest{
		OperationID: "op-send-9", ThreadID: thread.ID, Sender: meshUserParticipantID,
		Recipients: []agentcomm.ParticipantID{helperID}, Kind: agentcomm.MessageKindMessage, Body: "hi",
	}
	r1, err := o.Send(ctx, req)
	if err != nil {
		t.Fatalf("Send #1 (resolves the pending wait): %v", err)
	}

	// By now the wait this Send resolved is Ready, not Pending — the exact state that broke the old
	// effects-recomputation-on-replay approach.
	r2, err := o.Send(ctx, req)
	if err != nil {
		t.Fatalf("Send #2 (identical retry): %v", err)
	}
	if r1.MessageID != r2.MessageID || r1.Seq != r2.Seq {
		t.Fatalf("retry of an already-committed Send produced a different result: %+v vs %+v", r1, r2)
	}
}

// TestMesh_SendRedactsSecretsInBody regression-tests the third codex finding: Send used to copy req.Body
// straight into the durable mesh_message_sent payload with no redaction, so a body containing a configured
// secret value was written to events.jsonl (and delivered to the recipient's inbox) verbatim. Send now
// redacts the body the same way buildTaskContext redacts everything else a task sees
// (assistants_context.go's secretValuesForRedaction + pkg/redact).
func TestMesh_SendRedactsSecretsInBody(t *testing.T) {
	const secretValue = "sk-test-supersecret-token-12345" //nolint:gosec // test fixture value, not a real credential
	t.Setenv("ANTHROPIC_API_KEY", secretValue)

	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	taskID := newRunningHelperForMesh(t, o, store, "op-mt-10")
	helperID := meshHelperParticipantID(taskID)

	thread, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-open-10", Members: []agentcomm.ParticipantID{meshUserParticipantID, helperID}, Scope: agentcomm.ScopeStage,
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}

	send, err := o.Send(ctx, SendRequest{
		OperationID: "op-send-10", ThreadID: thread.ID, Sender: meshUserParticipantID,
		Recipients: []agentcomm.ParticipantID{helperID}, Kind: agentcomm.MessageKindMessage,
		Body: "here is the token: " + secretValue,
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	rs := store.Snapshot()
	body := rs.Assistants.Mesh.Messages[send.MessageID].Body
	if strings.Contains(body, secretValue) {
		t.Fatalf("persisted message body still contains the raw secret: %q", body)
	}
}

func TestMesh_CloseThreadResolvesDependentWaits(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	taskID := newRunningHelperForMesh(t, o, store, "op-mt-6")
	helperID := meshHelperParticipantID(taskID)

	thread, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-open-6", Members: []agentcomm.ParticipantID{meshUserParticipantID, helperID}, Scope: agentcomm.ScopeStage,
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	waitReceipt, err := o.Wait(ctx, WaitRequest{
		OperationID: "op-wait-6", Waiter: helperID,
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: thread.ID},
	})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if waitReceipt.Status != agentcomm.WaitPending {
		t.Fatalf("expected a fresh wait to start pending, got %q", waitReceipt.Status)
	}

	if err := o.CloseThread(ctx, CloseThreadRequest{OperationID: "op-close-6", ThreadID: thread.ID}); err != nil {
		t.Fatalf("CloseThread: %v", err)
	}

	rs := store.Snapshot()
	w := rs.Assistants.Mesh.Waits[waitReceipt.WaitID]
	if w.Status != agentcomm.WaitCancelled {
		t.Fatalf("wait status after CloseThread = %q, want cancelled", w.Status)
	}
}

// TestMesh_ListThreadsReturnsOnlyThreadsTheParticipantBelongsTo is the T7.3 dependency test (dev-plan.md):
// ReadInbox only surfaces threads where a message was addressed TO the participant and ReadThread needs a
// known id, so neither can answer "which threads is X in" for a UI roster. ListThreads fills that gap with a
// plain membership scan.
func TestMesh_ListThreadsReturnsOnlyThreadsTheParticipantBelongsTo(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	taskID := newRunningHelperForMesh(t, o, store, "op-mt-lt-1")
	helperID := meshHelperParticipantID(taskID)

	threadIn, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-open-lt-1", Title: "with helper", Members: []agentcomm.ParticipantID{meshUserParticipantID, helperID}, Scope: agentcomm.ScopeStage,
	})
	if err != nil {
		t.Fatalf("OpenThread (with helper): %v", err)
	}
	threadIn2, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-open-lt-2", Title: "stage and helper", Members: []agentcomm.ParticipantID{meshStageParticipantID(assistantsTestStageID), helperID}, Scope: agentcomm.ScopeStage,
	})
	if err != nil {
		t.Fatalf("OpenThread (stage and helper): %v", err)
	}
	threadOut, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-open-lt-3", Title: "without helper", Members: []agentcomm.ParticipantID{meshUserParticipantID, meshStageParticipantID(assistantsTestStageID)}, Scope: agentcomm.ScopeStage,
	})
	if err != nil {
		t.Fatalf("OpenThread (without helper): %v", err)
	}

	page, err := o.ListThreads(ctx, ListThreadsRequest{Participant: helperID})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	got := map[agentcomm.ThreadID]bool{}
	for _, th := range page.Threads {
		got[th.ID] = true
	}
	if len(got) != 2 || !got[threadIn.ID] || !got[threadIn2.ID] {
		t.Fatalf("ListThreads(helper) = %v, want exactly {%s, %s}", got, threadIn.ID, threadIn2.ID)
	}
	if got[threadOut.ID] {
		t.Fatalf("ListThreads(helper) unexpectedly includes a thread %s is not a member of", threadOut.ID)
	}
}

// TestMesh_ListThreadsEmptyForParticipantWithNoThreads asserts the "zero threads is an empty page, not an
// error" rule.
func TestMesh_ListThreadsEmptyForParticipantWithNoThreads(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	taskID := newRunningHelperForMesh(t, o, store, "op-mt-lt-4")
	helperID := meshHelperParticipantID(taskID)

	page, err := o.ListThreads(ctx, ListThreadsRequest{Participant: helperID})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(page.Threads) != 0 {
		t.Fatalf("ListThreads for a participant with no threads = %v, want empty", page.Threads)
	}
}

// TestMesh_ListThreadsForTheHumanSeesEveryThread mirrors ReadThread's own carve-out (agentcomm_messages.go):
// the human run owner sees every thread in the run, not just the ones naming it as a Member — ListThreads does
// not invent a stricter rule than the rest of the Service already enforces.
func TestMesh_ListThreadsForTheHumanSeesEveryThread(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	taskID := newRunningHelperForMesh(t, o, store, "op-mt-lt-5")
	helperID := meshHelperParticipantID(taskID)

	thread, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-open-lt-5", Members: []agentcomm.ParticipantID{meshStageParticipantID(assistantsTestStageID), helperID}, Scope: agentcomm.ScopeStage,
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}

	page, err := o.ListThreads(ctx, ListThreadsRequest{Participant: meshUserParticipantID})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	found := false
	for _, th := range page.Threads {
		if th.ID == thread.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("ListThreads(human) did not include thread %s the human is not a member of", thread.ID)
	}
}

func TestMesh_ListThreadsStageOnlyMessageIsOtherToHuman(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	stage := meshStageParticipantID(assistantsTestStageID)
	thread, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "summary-stage-only", Scope: agentcomm.ScopeRun,
		Members: []agentcomm.ParticipantID{meshUserParticipantID, stage},
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	message, err := o.Send(ctx, SendRequest{
		OperationID: "summary-stage-only-message", ThreadID: thread.ID,
		Sender: stage, Recipients: []agentcomm.ParticipantID{meshUserParticipantID},
		Kind: agentcomm.MessageKindMessage, Body: "stage message",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	for _, participant := range []agentcomm.ParticipantID{stage, meshUserParticipantID} {
		page, err := o.ListThreads(ctx, ListThreadsRequest{Participant: participant})
		if err != nil {
			t.Fatalf("ListThreads(%s): %v", participant, err)
		}
		if got := page.Summaries[thread.ID].LastOtherMessageSeq; got != message.Seq {
			t.Errorf("ListThreads(%s) last other seq = %d, want stage message seq %d", participant, got, message.Seq)
		}
	}
}

func TestMesh_ListThreadsSummariesAreScopedAndReplayStable(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	stage := meshStageParticipantID(assistantsTestStageID)

	open := func(id string, members ...agentcomm.ParticipantID) agentcomm.Thread {
		t.Helper()
		th, err := o.OpenThread(ctx, OpenThreadRequest{OperationID: id, Scope: agentcomm.ScopeRun, Members: members})
		if err != nil {
			t.Fatalf("OpenThread(%s): %v", id, err)
		}
		return th
	}
	main := open("summary-main", meshUserParticipantID, stage)
	empty := open("summary-empty", meshUserParticipantID, stage)
	private := open("summary-private", meshUserParticipantID)

	send := func(id string, thread agentcomm.ThreadID, sender, recipient agentcomm.ParticipantID, kind agentcomm.MessageKind, body string, reply *agentcomm.MessageID, auto bool) SendReceipt {
		t.Helper()
		r, err := o.Send(ctx, SendRequest{OperationID: id, ThreadID: thread, Sender: sender, Recipients: []agentcomm.ParticipantID{recipient}, Kind: kind, Body: body, InReplyTo: reply, AutoResult: auto})
		if err != nil {
			t.Fatalf("Send(%s): %v", id, err)
		}
		return r
	}
	first := send("summary-first", main.ID, meshUserParticipantID, stage, agentcomm.MessageKindMessage, "first", nil, false)
	second := send("summary-reply", main.ID, stage, meshUserParticipantID, agentcomm.MessageKindReply, "  <b>reply</b>\n  with\tspaces  ", &first.MessageID, false)
	send("summary-own-between", main.ID, meshUserParticipantID, stage, agentcomm.MessageKindMessage, "own message", nil, false)
	last := send("summary-result", main.ID, stage, meshUserParticipantID, agentcomm.MessageKindResult, "turn result", &first.MessageID, true)
	ownAfter := send("summary-own-after", main.ID, meshUserParticipantID, stage, agentcomm.MessageKindMessage, "latest own", nil, false)
	firstPage, err := o.ReadThread(ctx, ReadThreadRequest{ThreadID: main.ID, Reader: stage, Limit: 1})
	if err != nil || len(firstPage.Messages) != 1 || firstPage.NextCursor != first.Seq {
		t.Fatalf("first message page = %+v, err %v", firstPage, err)
	}
	secondPage, err := o.ReadThread(ctx, ReadThreadRequest{ThreadID: main.ID, Reader: stage, Cursor: firstPage.NextCursor, Limit: 1})
	if err != nil || len(secondPage.Messages) != 1 || secondPage.NextCursor != second.Seq {
		t.Fatalf("second message page = %+v, err %v", secondPage, err)
	}
	if err := o.CloseThread(ctx, CloseThreadRequest{OperationID: "summary-close", ThreadID: main.ID}); err != nil {
		t.Fatalf("CloseThread: %v", err)
	}

	before := store.Snapshot()
	page, err := o.ListThreads(ctx, ListThreadsRequest{Participant: stage})
	if err != nil {
		t.Fatalf("ListThreads(stage): %v", err)
	}
	if len(page.Threads) != 2 || page.Threads[0].ID != main.ID || page.Threads[1].ID != empty.ID {
		t.Fatalf("stage threads = %v, want main then empty", page.Threads)
	}
	if len(page.Summaries) != 2 {
		t.Fatalf("stage summaries = %v, want exactly two", page.Summaries)
	}
	if _, ok := page.Summaries[private.ID]; ok {
		t.Fatal("non-member can see private summary")
	}
	got := page.Summaries[main.ID]
	if got.LastMessage == nil || got.LastMessage.ID != ownAfter.MessageID || got.LastMessage.Seq != ownAfter.Seq ||
		got.LastMessage.CreatedAt != before.Assistants.Mesh.Messages[ownAfter.MessageID].CreatedAt ||
		got.LastMessage.SenderID != meshUserParticipantID || got.LastMessage.Preview != "latest own" ||
		got.LastOtherMessageSeq != last.Seq || got.TitleSeq != 0 {
		t.Fatalf("stage summary = %+v, want latest own message and last other message %d", got, last.Seq)
	}
	if got := page.Summaries[empty.ID]; got.LastMessage != nil || got.LastOtherMessageSeq != 0 || got.TitleSeq != 0 {
		t.Fatalf("empty thread summary = %+v", got)
	}
	mainJSON, err := json.Marshal(page.Summaries[main.ID])
	if err != nil {
		t.Fatal(err)
	}
	var mainSummary map[string]json.RawMessage
	if err := json.Unmarshal(mainJSON, &mainSummary); err != nil {
		t.Fatal(err)
	}
	if _, ok := mainSummary["other_message_seqs"]; ok {
		t.Fatalf("summary includes unneeded other_message_seqs: %s", mainJSON)
	}
	emptyJSON, err := json.Marshal(page.Summaries[empty.ID])
	if err != nil {
		t.Fatal(err)
	}
	var emptySummary map[string]json.RawMessage
	if err := json.Unmarshal(emptyJSON, &emptySummary); err != nil {
		t.Fatal(err)
	}
	if _, ok := emptySummary["other_message_seqs"]; ok {
		t.Fatalf("empty summary includes unneeded other_message_seqs: %s", emptyJSON)
	}
	if after := store.Snapshot(); !reflect.DeepEqual(before.Assistants.Mesh.Receipts, after.Assistants.Mesh.Receipts) || before.LastSeq != after.LastSeq {
		t.Fatal("ListThreads changed receipts or journal sequence")
	}

	human, err := o.ListThreads(ctx, ListThreadsRequest{Participant: meshUserParticipantID})
	if err != nil {
		t.Fatalf("ListThreads(human): %v", err)
	}
	if len(human.Summaries) != 3 || human.Summaries[main.ID].LastOtherMessageSeq != last.Seq {
		t.Fatalf("human summaries = %+v", human.Summaries)
	}
	if human.Summaries[main.ID].LastMessage.Seq <= second.Seq {
		t.Fatal("last message did not use commit sequence")
	}

	store.Close()
	store2, err := state.Open(o.opts.RunDir, []string{assistantsTestStageID})
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer store2.Close()
	replayed, err := reopenAssistantsOrchestrator(o, store2).ListThreads(ctx, ListThreadsRequest{Participant: stage})
	if err != nil {
		t.Fatalf("ListThreads after replay: %v", err)
	}
	beforeJSON, err := json.Marshal(page)
	if err != nil {
		t.Fatalf("marshal page: %v", err)
	}
	afterJSON, err := json.Marshal(replayed)
	if err != nil {
		t.Fatalf("marshal replayed page: %v", err)
	}
	if string(beforeJSON) != string(afterJSON) {
		t.Fatalf("replayed page differs: before=%s after=%s", beforeJSON, afterJSON)
	}
}

func TestMesh_ThreadSummaryPreviewUsesRunesAndSavedPlainText(t *testing.T) {
	stamp := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	old := &agentcomm.Message{ID: "old", Seq: 6, Sender: "user", CreatedAt: stamp, Body: "old"}
	message := &agentcomm.Message{ID: "msg", Seq: 7, Sender: "agent", CreatedAt: stamp, Body: strings.Repeat("é", 159) + " \n <b>tail</b>"}
	got := summarizeThreadMessages(state.MeshState{
		Messages:         map[agentcomm.MessageID]*agentcomm.Message{old.ID: old, message.ID: message},
		MessagesByThread: map[agentcomm.ThreadID][]agentcomm.MessageID{"thread": {old.ID, message.ID}},
	}, "thread")
	if got.LastMessage == nil || got.LastMessage.Preview != strings.Repeat("é", 159)+" " || len([]rune(got.LastMessage.Preview)) != 160 || got.LastOtherMessageSeq != 7 {
		t.Fatalf("Unicode preview = %+v", got)
	}
}

// Only a message an agent explicitly addressed to the human advances LastAgentToUserMessageSeq: not the human's
// own messages, not agent-to-agent chatter and not a passive turn result, while LastOtherMessageSeq keeps
// following any non-human sender.
func TestMesh_ListThreadsLastAgentToUserMessageSeq(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true, flow.Stage{ID: "qa", Command: "claude"})
	ctx := context.Background()
	for _, id := range []string{assistantsTestStageID, "qa"} {
		activateStage(t, o, id)
	}
	impl, qa := meshStageParticipantID(assistantsTestStageID), meshStageParticipantID("qa")
	thread, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "a2u-open", Scope: agentcomm.ScopeRun,
		Members: []agentcomm.ParticipantID{meshUserParticipantID, impl, qa},
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	send := func(id string, sender agentcomm.ParticipantID, auto bool, recipients ...agentcomm.ParticipantID) SendReceipt {
		t.Helper()
		r, err := o.Send(ctx, SendRequest{
			OperationID: id, ThreadID: thread.ID, Sender: sender, Recipients: recipients,
			Kind: agentcomm.MessageKindMessage, Body: id, AutoResult: auto,
		})
		if err != nil {
			t.Fatalf("Send(%s): %v", id, err)
		}
		return r
	}
	summary := func() ThreadSummary {
		t.Helper()
		page, err := o.ListThreads(ctx, ListThreadsRequest{Participant: meshUserParticipantID})
		if err != nil {
			t.Fatalf("ListThreads: %v", err)
		}
		return page.Summaries[thread.ID]
	}
	if got := summary().LastAgentToUserMessageSeq; got != 0 {
		t.Fatalf("empty thread LastAgentToUserMessageSeq = %d, want 0", got)
	}

	toUser := send("a2u-explicit", impl, false, meshUserParticipantID)
	if got := summary().LastAgentToUserMessageSeq; got != toUser.Seq {
		t.Fatalf("after explicit agent→user: %d, want %d", got, toUser.Seq)
	}
	toBoth := send("a2u-multi", impl, false, meshUserParticipantID, qa)
	if got := summary().LastAgentToUserMessageSeq; got != toBoth.Seq {
		t.Fatalf("after agent→{user, agent}: %d, want %d", got, toBoth.Seq)
	}

	send("a2u-from-user", meshUserParticipantID, false, impl)
	send("a2u-agent-chatter", qa, false, impl)
	auto := send("a2u-auto-result", impl, true, meshUserParticipantID)
	got := summary()
	if got.LastAgentToUserMessageSeq != toBoth.Seq {
		t.Fatalf("later non-explicit messages moved LastAgentToUserMessageSeq to %d, want %d", got.LastAgentToUserMessageSeq, toBoth.Seq)
	}
	if got.LastOtherMessageSeq != auto.Seq {
		t.Fatalf("LastOtherMessageSeq = %d, want %d (any non-human sender)", got.LastOtherMessageSeq, auto.Seq)
	}

	if err := o.CloseThread(ctx, CloseThreadRequest{OperationID: "a2u-close", ThreadID: thread.ID}); err != nil {
		t.Fatalf("CloseThread: %v", err)
	}
	if got := summary().LastAgentToUserMessageSeq; got != toBoth.Seq {
		t.Fatalf("closing the thread changed LastAgentToUserMessageSeq to %d, want %d", got, toBoth.Seq)
	}
}

// The newest explicit agent→user message can sit behind newer chatter: the backward scan must not stop at the
// last message and the last other message.
func TestMesh_ThreadSummaryScansPastNewerChatterForAgentToUser(t *testing.T) {
	stamp := time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC)
	msgs := []*agentcomm.Message{
		{ID: "m1", Seq: 1, Sender: "stage:a", Recipients: []agentcomm.ParticipantID{"user"}, CreatedAt: stamp},
		{ID: "m2", Seq: 2, Sender: "stage:a", Recipients: []agentcomm.ParticipantID{"stage:b"}, CreatedAt: stamp},
		{ID: "m3", Seq: 3, Sender: "user", Recipients: []agentcomm.ParticipantID{"stage:a"}, CreatedAt: stamp},
	}
	mesh := state.MeshState{
		Messages:         map[agentcomm.MessageID]*agentcomm.Message{},
		MessagesByThread: map[agentcomm.ThreadID][]agentcomm.MessageID{},
	}
	for _, m := range msgs {
		mesh.Messages[m.ID] = m
		mesh.MessagesByThread["thread"] = append(mesh.MessagesByThread["thread"], m.ID)
	}
	got := summarizeThreadMessages(mesh, "thread")
	if got.LastOtherMessageSeq != 2 || got.LastAgentToUserMessageSeq != 1 {
		t.Fatalf("summary = %+v, want other seq 2 and agent→user seq 1", got)
	}
}
