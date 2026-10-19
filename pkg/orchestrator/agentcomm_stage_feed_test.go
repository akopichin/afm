package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// stageMessageBodies is the bodies of stage stageID's conversation messages, in the order the feed shows them.
func stageMessageBodies(t *testing.T, o *Orchestrator, stageID string) []string {
	t.Helper()
	page := o.StageMessages(stageID, 0, 0)
	var out []string
	for _, m := range page.Messages {
		out = append(out, m.Body)
	}
	return out
}

func TestSendFromAgent_ThreadRecordsTheSendersStage(t *testing.T) {
	o, store := newTwoStageMeshOrchestrator(t)
	r, err := agentSend(o, "op-ask", "stage:docs", []string{assistantsTestStageID}, "question?")
	if err != nil {
		t.Fatalf("SendFromAgent: %v", err)
	}
	if got := store.Snapshot().Assistants.Mesh.Threads[r.ThreadID].OriginStage; got != "docs" {
		t.Fatalf("origin_stage = %q, want the sender's stage docs", got)
	}
}

func TestOpenThread_OriginStageFromTheFeed(t *testing.T) {
	o, store := newTwoStageMeshOrchestrator(t)
	ctx := context.Background()
	req := OpenThreadRequest{
		OperationID: "op-open", Scope: agentcomm.ScopeRun, OriginStage: "docs",
		Members: []agentcomm.ParticipantID{meshUserParticipantID, meshStageParticipantID(assistantsTestStageID)},
	}
	th, err := o.OpenThread(ctx, req)
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	if th.OriginStage != "docs" || store.Snapshot().Assistants.Mesh.Threads[th.ID].OriginStage != "docs" {
		t.Fatalf("thread = %+v, want origin_stage docs", th)
	}
	// A retry is the same open; the same request id with another origin is a different request.
	if _, err := o.OpenThread(ctx, req); err != nil {
		t.Fatalf("retry: %v", err)
	}
	req.OriginStage = assistantsTestStageID
	if _, err := o.OpenThread(ctx, req); err == nil {
		t.Fatal("the same request id with another origin stage: want a conflict")
	}
	req.OperationID, req.OriginStage = "op-open-unknown", "nope"
	if _, err := o.OpenThread(ctx, req); err == nil {
		t.Fatal("an unknown origin stage: want a rejection")
	}
}

// The stage feed shows a conversation when the stage opened it (origin_stage), when its agent or one of its
// helpers is a member, or when it is the stage's discussion — in journal order.
func TestStageMessages_ConversationsOfTheStage(t *testing.T) {
	o, _ := newTwoStageMeshOrchestrator(t)
	ctx := context.Background()
	impl := meshStageParticipantID(assistantsTestStageID)

	if _, err := o.PostDiscussionMessage(ctx, humanPost("op-post", "impl discussion", nil, false)); err != nil {
		t.Fatalf("PostDiscussionMessage: %v", err)
	}
	if _, err := agentSend(o, "op-agents", "stage:docs", []string{string(impl)}, "docs asks impl"); err != nil {
		t.Fatalf("SendFromAgent: %v", err)
	}
	// The human opens a conversation with impl from the docs feed: it belongs to docs by origin only.
	th, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-open", Scope: agentcomm.ScopeRun, OriginStage: "docs",
		Members: []agentcomm.ParticipantID{meshUserParticipantID, impl},
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	if _, err := o.Send(ctx, SendRequest{
		OperationID: "op-human", ThreadID: th.ID, Sender: meshUserParticipantID, Recipients: []agentcomm.ParticipantID{impl},
		Kind: agentcomm.MessageKindMessage, Body: "from the docs feed", Actor: state.Actor{Kind: actorKindUser},
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if got, want := stageMessageBodies(t, o, assistantsTestStageID), []string{"impl discussion", "docs asks impl", "from the docs feed"}; !slices.Equal(got, want) {
		t.Fatalf("impl feed = %q, want %q", got, want)
	}
	if got, want := stageMessageBodies(t, o, "docs"), []string{"docs asks impl", "from the docs feed"}; !slices.Equal(got, want) {
		t.Fatalf("docs feed = %q, want %q", got, want)
	}

	// The cursor walks the same messages one page at a time; a message's thread arrives no later than the
	// message itself.
	var bodies []string
	seen := map[agentcomm.ThreadID]bool{}
	cursor := uint64(0)
	for {
		page := o.StageMessages(assistantsTestStageID, cursor, 1)
		for _, th := range page.Threads {
			seen[th.ID] = true
		}
		for _, m := range page.Messages {
			bodies = append(bodies, m.Body)
			if !seen[m.ThreadID] {
				t.Fatalf("the thread of %s has not arrived by its page", m.ID)
			}
		}
		if !page.HasMore {
			break
		}
		cursor = page.NextCursor
	}
	if len(bodies) != 3 {
		t.Fatalf("paged impl feed = %q, want all three messages", bodies)
	}
}

// TestStageMessages_PollsReturnOnlyChangedThreads: a poll returns the feed's threads opened or closed after its
// cursor and nothing else — a quiet poll is empty, and a conversation closed after its last message shows as
// closed once, without a new message and without the feed's other threads.
func TestStageMessages_PollsReturnOnlyChangedThreads(t *testing.T) {
	o, _ := newTwoStageMeshOrchestrator(t)
	ctx := context.Background()
	impl := meshStageParticipantID(assistantsTestStageID)
	r, err := agentSend(o, "op-agents", "stage:docs", []string{string(impl)}, "docs asks impl")
	if err != nil {
		t.Fatalf("SendFromAgent: %v", err)
	}
	if _, err := o.PostDiscussionMessage(ctx, humanPost("op-post", "impl discussion", nil, false)); err != nil {
		t.Fatalf("PostDiscussionMessage: %v", err)
	}
	first := o.StageMessages(assistantsTestStageID, 0, 0)
	if len(first.Threads) != 2 {
		t.Fatalf("first poll threads = %+v, want both conversations", first.Threads)
	}
	cursor := first.NextCursor

	if quiet := o.StageMessages(assistantsTestStageID, cursor, 0); len(quiet.Threads) != 0 || len(quiet.Messages) != 0 || quiet.NextCursor != cursor {
		t.Fatalf("quiet poll = %+v, want nothing at cursor %d", quiet, cursor)
	}

	if err := o.CloseThread(ctx, CloseThreadRequest{OperationID: "op-close", ThreadID: r.ThreadID, Actor: state.Actor{Kind: actorKindUser}}); err != nil {
		t.Fatalf("CloseThread: %v", err)
	}
	page := o.StageMessages(assistantsTestStageID, cursor, 0)
	if len(page.Messages) != 0 || page.NextCursor <= cursor {
		t.Fatalf("page = %+v, want no new messages and a cursor past the close", page)
	}
	if len(page.Threads) != 1 || page.Threads[0].ID != r.ThreadID || page.Threads[0].Status != agentcomm.ThreadClosed {
		t.Fatalf("threads = %+v, want only %s, closed", page.Threads, r.ThreadID)
	}
	if again := o.StageMessages(assistantsTestStageID, page.NextCursor, 0); len(again.Threads) != 0 {
		t.Fatalf("next poll threads = %+v, want none: the close was already returned", again.Threads)
	}
}

func TestStageMessages_HelperOfTheStageMakesTheThreadVisible(t *testing.T) {
	o, store := newTwoStageMeshOrchestrator(t)
	ctx := context.Background()
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		<-ctx.Done() // the helper stays live for the whole test
		return executor.HelperOutcome{}, ctx.Err()
	}
	taskID := attachTestTask(ctx, t, o, "op-helper")
	if store.Snapshot().Assistants.Tasks[taskID].RootStageID != assistantsTestStageID {
		t.Fatal("test setup: the helper must belong to impl")
	}
	th, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-open", Scope: agentcomm.ScopeStage,
		Members: []agentcomm.ParticipantID{meshUserParticipantID, meshHelperParticipantID(taskID)},
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	if _, err := o.Send(ctx, SendRequest{
		OperationID: "op-human", ThreadID: th.ID, Sender: meshUserParticipantID, Recipients: []agentcomm.ParticipantID{meshHelperParticipantID(taskID)},
		Kind: agentcomm.MessageKindMessage, Body: "to the helper", Actor: state.Actor{Kind: actorKindUser},
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := stageMessageBodies(t, o, assistantsTestStageID); !slices.Equal(got, []string{"to the helper"}) {
		t.Fatalf("impl feed = %q, want the helper's conversation", got)
	}
	if got := stageMessageBodies(t, o, "docs"); len(got) != 0 {
		t.Fatalf("docs feed = %q, want nothing", got)
	}
}

// A journal written before origin_stage existed replays: its threads have no origin and show by membership,
// a new post re-opens the old discussion thread as a no-op, and the agents' old conversation is continued.
func TestStageMessages_PreOriginJournalReplays(t *testing.T) {
	o, store := newTwoStageMeshOrchestrator(t)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(o.opts.RunDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(o.opts.RunDir, e.Name())); err != nil {
			t.Fatal(err)
		}
	}
	fixture, err := os.ReadFile(filepath.Join("testdata", "mesh-pre-origin", "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(o.opts.RunDir, "events.jsonl"), fixture, 0o644); err != nil {
		t.Fatal(err)
	}
	reopened, err := state.Open(o.opts.RunDir, []string{assistantsTestStageID, "docs"})
	if err != nil {
		t.Fatalf("open the pre-change journal: %v", err)
	}
	t.Cleanup(func() { reopened.Close() })
	o.opts.Store = reopened
	o.fsm = bus.NewFSM(reopened)

	if got, want := stageMessageBodies(t, o, assistantsTestStageID), []string{"old question", "old agents question"}; !slices.Equal(got, want) {
		t.Fatalf("impl feed = %q, want %q", got, want)
	}
	if got, want := stageMessageBodies(t, o, "docs"), []string{"old agents question"}; !slices.Equal(got, want) {
		t.Fatalf("docs feed = %q, want %q", got, want)
	}

	if _, err := o.PostDiscussionMessage(context.Background(), humanPost("op-new", "new question", nil, false)); err != nil {
		t.Fatalf("post on the old journal: %v", err)
	}
	if origin := reopened.Snapshot().Assistants.Mesh.Threads[deriveDiscussionThreadID(assistantsTestStageID)].OriginStage; origin != "" {
		t.Fatalf("the old discussion thread's origin = %q, want it unchanged (empty)", origin)
	}
	r, err := agentSend(o, "op-new-conv", string(meshStageParticipantID(assistantsTestStageID)), []string{"stage:docs"}, "answer")
	if err != nil {
		t.Fatalf("SendFromAgent: %v", err)
	}
	if r.ThreadID != "mt-0ld0c0nversat10n000" {
		t.Fatalf("answer went to %s, want the old conversation", r.ThreadID)
	}
}

func TestStageMessages_DiscussionMirrorIsOneMessage(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	qID, err := o.PostDiscussionMessage(context.Background(), humanPost("op-q", "question", nil, false))
	if err != nil {
		t.Fatalf("PostDiscussionMessage: %v", err)
	}
	stageAgentReply(t, o, "op-a", qID, "answer")
	page := o.StageMessages(assistantsTestStageID, 0, 0)
	if len(page.Messages) != 2 || string(page.Messages[0].ID) != qID || page.Messages[1].InReplyTo == nil || string(*page.Messages[1].InReplyTo) != qID {
		t.Fatalf("messages = %+v, want the question and its answer once each, under the discussion ids", page.Messages)
	}
	if n := len(o.DiscussionMessages(assistantsTestStageID)); n != 2 {
		t.Fatalf("discussion = %d messages, want the same two", n)
	}
}

// A run agent added from a stage's feed: its first conversation shows in that feed (origin_stage), though the
// run agent itself belongs to no stage.
func TestAttachAndSend_RunAgentThreadBelongsToTheFeedItWasAddedFrom(t *testing.T) {
	o, store := newTwoStageMeshOrchestrator(t)
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		<-ctx.Done()
		return executor.HelperOutcome{}, ctx.Err()
	}
	first := "please review"
	res, err := o.AttachAndSend(context.Background(), AttachAndSendRequest{
		OperationID: "op-add", ProfileID: "qa", Scope: "run", Title: "QA", Instruction: "review",
		FirstMessage: &first, OriginStage: "docs", Actor: state.Actor{Kind: actorKindUser},
	})
	if err != nil {
		t.Fatalf("AttachAndSend: %v", err)
	}
	if got := store.Snapshot().Assistants.Mesh.Threads[res.ThreadID].OriginStage; got != "docs" {
		t.Fatalf("origin_stage = %q, want docs", got)
	}
	if got := stageMessageBodies(t, o, "docs"); !slices.Equal(got, []string{first}) {
		t.Fatalf("docs feed = %q, want the first message", got)
	}
	if got := stageMessageBodies(t, o, assistantsTestStageID); len(got) != 0 {
		t.Fatalf("impl feed = %q, want nothing", got)
	}
}
