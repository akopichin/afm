package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

func humanPost(opID, body string, mentions []agentcomm.ParticipantID, interrupt bool) PostMessageRequest {
	return PostMessageRequest{
		OperationID: opID, StageID: assistantsTestStageID, AuthorKind: assistants.AuthorHuman,
		Round: assistants.RoundFreeform, Body: body, Actor: state.Actor{Kind: "user"},
		Mentions: mentions, Interrupt: interrupt,
	}
}

// An interrupting discussion post stops the running agent the way Revise does, but the restarted agent is
// asked a QUESTION to answer and then continue — never handed a "User note" revising its work — and its
// reply lands back in the Discussion.
func TestDiscussion_InterruptAsksRunningAgentAndMirrorsItsReply(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	interrupt := make(chan struct{}, 1)
	o.interruptChans.Store(assistantsTestStageID, interrupt)

	qID, err := o.PostDiscussionMessage(ctx, humanPost("op-q", "how is the plan going?", nil, true))
	if err != nil {
		t.Fatalf("PostDiscussionMessage: %v", err)
	}

	if got := o.currentStatus(assistantsTestStageID); got != state.StatusRevising {
		t.Fatalf("status = %v, want revising: the running agent is interrupted before the API returns", got)
	}
	if reason := store.LastReviseReason(assistantsTestStageID); !strings.Contains(reason, qID) {
		t.Fatalf("EvRevise reason = %q, want it to name the discussion message %s", reason, qID)
	}
	select {
	case <-interrupt:
	default:
		t.Fatal("the running agent's interrupt channel was not signaled")
	}

	stageDir := filepath.Join(o.opts.RunDir, assistantsTestStageID)
	if _, err := os.Stat(filepath.Join(stageDir, "feedback.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a question must not be written as feedback.md (stat err = %v)", err)
	}

	var prompt string
	fake := &fakeAgentRunner{onAgent: func(p string) {
		prompt = p
		_ = os.WriteFile(filepath.Join(stageDir, "execution_summary.md"), []byte("done"), 0o644)
	}}
	o.opts.Runner = fake
	o.runner = fake
	o.runAutonomousWithFeedback(ctx, flow.Stage{ID: assistantsTestStageID}) // no Command: the injected fake runner, never a real agent

	for _, want := range []string{"QUESTION to answer", "how is the plan going?", "afm agent reply --in-reply-to <message id>", qID, "continue your current work where you left off"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("restarted prompt lacks %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "User note (added while this stage was running)") {
		t.Errorf("a question must not look like plan feedback:\n%s", prompt)
	}
	if again := o.restartNoteBlock(stageDir); strings.Contains(again, qID) {
		t.Errorf("the question must reach exactly one restarted prompt, a later restart got:\n%s", again)
	}

	stageAgent := meshStageParticipantID(assistantsTestStageID)
	in := agentcomm.MessageID(qID)
	if _, err := o.SendFromAgent(ctx, SendRequest{
		OperationID: "op-a", Sender: stageAgent, Kind: agentcomm.MessageKindReply, InReplyTo: &in,
		Body: "half done, writing the API section", Actor: state.Actor{Kind: "agent", ID: string(stageAgent)},
	}); err != nil {
		t.Fatalf("reply: %v", err)
	}
	msgs := o.DiscussionMessages(assistantsTestStageID)
	last := msgs[len(msgs)-1]
	if last.Author.Kind != assistants.AuthorMainAgent || last.ParentMessageID != qID || last.Body != "half done, writing the API section" {
		t.Fatalf("mirrored answer = %+v, want the stage agent's reply to %s", last, qID)
	}
}

// Interrupt only acts on a running stage; anywhere else the post simply waits in the inbox, as without it.
func TestDiscussion_InterruptIgnoredWhenStageNotRunning(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	if _, err := o.PostDiscussionMessage(context.Background(), humanPost("op-q", "anything new?", nil, true)); err != nil {
		t.Fatalf("PostDiscussionMessage: %v", err)
	}
	if got := o.currentStatus(assistantsTestStageID); got != state.StatusPending {
		t.Fatalf("status = %v, want pending: interrupting a stage that is not running has no effect", got)
	}
	if reason := store.LastReviseReason(assistantsTestStageID); reason != "" {
		t.Fatalf("EvRevise reason = %q, want no revise", reason)
	}
	if _, err := os.Stat(filepath.Join(o.opts.RunDir, assistantsTestStageID, "discussion-questions.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("no question must be saved for a stage that is not running (stat err = %v)", err)
	}
}

// A post that mentions another agent goes to the stage agent AND that agent, in a stage discussion thread
// with exactly those members; the mentioned agent's reply shows in this stage's Discussion under its own name.
func TestDiscussion_MentionDeliversAndMirrorsMentionedReply(t *testing.T) {
	o, store := newTwoStageMeshOrchestrator(t)
	ctx := context.Background()
	docs := meshStageParticipantID("docs")
	stageAgent := meshStageParticipantID(assistantsTestStageID)

	qID, err := o.PostDiscussionMessage(ctx, humanPost("op-q", "@docs does the API page cover errors?",
		[]agentcomm.ParticipantID{docs, meshUserParticipantID, stageAgent, docs}, false))
	if err != nil {
		t.Fatalf("PostDiscussionMessage: %v", err)
	}

	rs := store.Snapshot()
	threadID := discussionThreadID(assistantsTestStageID, []agentcomm.ParticipantID{docs})
	if threadID == deriveDiscussionThreadID(assistantsTestStageID) {
		t.Fatal("a post with mentions must not share the base discussion thread, which cannot grow members")
	}
	thread := rs.Assistants.Mesh.Threads[threadID]
	if thread == nil || !slices.Equal(thread.Members, []agentcomm.ParticipantID{meshUserParticipantID, stageAgent, docs}) {
		t.Fatalf("thread = %+v, want user + stage agent + docs", thread)
	}
	msg := rs.Assistants.Mesh.Messages[agentcomm.MessageID(qID)]
	if msg == nil || !slices.Equal(msg.Recipients, []agentcomm.ParticipantID{stageAgent, docs}) {
		t.Fatalf("mesh message = %+v, want it addressed to the stage agent and docs", msg)
	}
	if got := o.DiscussionMessages(assistantsTestStageID)[0].Mentions; !slices.Equal(got, []string{string(docs)}) {
		t.Fatalf("discussion post mentions = %v, want [%s] (deduped, without user and the stage agent)", got, docs)
	}

	in := agentcomm.MessageID(qID)
	if _, err := o.SendFromAgent(ctx, SendRequest{
		OperationID: "op-docs-a", Sender: docs, Kind: agentcomm.MessageKindReply, InReplyTo: &in,
		Body: "yes, section 4", Actor: state.Actor{Kind: "agent", ID: string(docs)},
	}); err != nil {
		t.Fatalf("docs reply: %v", err)
	}
	msgs := o.DiscussionMessages(assistantsTestStageID)
	last := msgs[len(msgs)-1]
	if last.Author.Kind != assistants.AuthorAssistant || last.Author.ID != string(docs) || last.Role != string(agentcomm.KindStageAgent) ||
		last.ParentMessageID != qID || last.Body != "yes, section 4" {
		t.Fatalf("mirrored reply = %+v, want docs' answer under its own id", last)
	}
	if n := len(o.DiscussionMessages("docs")); n != 0 {
		t.Fatalf("docs' own discussion got %d messages, want 0: the thread belongs to %s's discussion", n, assistantsTestStageID)
	}

	// The same request again (mentions in another order) replays; different mentions under its id conflict.
	again, err := o.PostDiscussionMessage(ctx, humanPost("op-q", "@docs does the API page cover errors?", []agentcomm.ParticipantID{docs}, false))
	if err != nil || again != qID {
		t.Fatalf("replay = %q, %v; want %q", again, err, qID)
	}
	if _, err := o.PostDiscussionMessage(ctx, humanPost("op-q", "@docs does the API page cover errors?", nil, false)); err == nil {
		t.Fatal("the same request id with other mentions must be a conflict")
	}
}

// An unknown mention is refused and writes nothing.
func TestDiscussion_UnknownMentionRejected(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	if _, err := o.PostDiscussionMessage(context.Background(), humanPost("op-q", "hi", []agentcomm.ParticipantID{"run:nobody"}, false)); err == nil {
		t.Fatal("want an error for a mention nobody in the run answers to")
	}
	if n := len(store.Snapshot().Assistants.Discussions.Messages); n != 0 {
		t.Fatalf("discussion has %d messages after a rejected post, want 0", n)
	}
}

// A journal written before mentions existed (posts in the base thread, no mentions field) loads unchanged,
// and a stage agent's reply in that old thread is still mirrored after the restart.
func TestDiscussion_OldJournalReplaysAndMirrorsReplies(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	qID, err := o.PostDiscussionMessage(ctx, humanPost("op-old", "old question", nil, false))
	if err != nil {
		t.Fatalf("PostDiscussionMessage: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(o.opts.RunDir, "events.jsonl"))
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	if strings.Contains(string(raw), `"mentions"`) {
		t.Fatalf("a post without mentions must keep the old journal shape:\n%s", raw)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened, err := state.Open(o.opts.RunDir, []string{assistantsTestStageID})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { reopened.Close() })
	o.opts.Store = reopened
	o.fsm = bus.NewFSM(reopened)

	msgs := o.DiscussionMessages(assistantsTestStageID)
	if len(msgs) != 1 || msgs[0].ID != qID || msgs[0].Mentions != nil {
		t.Fatalf("replayed discussion = %+v, want the old post without mentions", msgs)
	}
	stageAgent := meshStageParticipantID(assistantsTestStageID)
	in := agentcomm.MessageID(qID)
	if _, err := o.SendFromAgent(ctx, SendRequest{
		OperationID: "op-old-a", Sender: stageAgent, Kind: agentcomm.MessageKindReply, InReplyTo: &in,
		Body: "old answer", Actor: state.Actor{Kind: "agent", ID: string(stageAgent)},
	}); err != nil {
		t.Fatalf("reply after replay: %v", err)
	}
	if msgs := o.DiscussionMessages(assistantsTestStageID); len(msgs) != 2 || msgs[1].ParentMessageID != qID {
		t.Fatalf("discussion after reply = %+v, want the answer mirrored", msgs)
	}
}
