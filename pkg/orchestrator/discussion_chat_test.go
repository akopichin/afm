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

// liveAgent registers an interrupt channel for the stage, as runWithRetry does while an agent runs.
func liveAgent(o *Orchestrator) chan struct{} {
	ch := make(chan struct{}, 1)
	o.interruptChans.Store(assistantsTestStageID, ch)
	return ch
}

// captureAutonomousPrompt runs one fresh autonomous attempt of the test stage with a fake runner (the stage has
// no Command, so never a real agent) and returns its prompt.
func captureAutonomousPrompt(t *testing.T, o *Orchestrator, run func(context.Context, flow.Stage)) string {
	t.Helper()
	stageDir := filepath.Join(o.opts.RunDir, assistantsTestStageID)
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	var prompt string
	fake := &fakeAgentRunner{onAgent: func(p string) {
		prompt = p
		_ = os.WriteFile(filepath.Join(stageDir, "execution_summary.md"), []byte("done"), 0o644)
	}}
	o.opts.Runner = fake
	o.runner = fake
	run(context.Background(), flow.Stage{ID: assistantsTestStageID})
	return prompt
}

func stageAgentReply(t *testing.T, o *Orchestrator, opID, to, body string) {
	t.Helper()
	stageAgent := meshStageParticipantID(assistantsTestStageID)
	in := agentcomm.MessageID(to)
	if _, err := o.SendFromAgent(context.Background(), SendRequest{
		OperationID: opID, Sender: stageAgent, Kind: agentcomm.MessageKindReply, InReplyTo: &in,
		Body: body, Actor: state.Actor{Kind: "agent", ID: string(stageAgent)},
	}); err != nil {
		t.Fatalf("stage agent reply: %v", err)
	}
}

func assertAsksQuestion(t *testing.T, prompt, qID, body string) {
	t.Helper()
	for _, want := range []string{"QUESTION to answer", "> " + body, "afm agent reply --in-reply-to <message id>", "message " + qID, "continue your current work where you left off"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt lacks %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "User note (added while this stage was running)") {
		t.Errorf("a question must not look like plan feedback:\n%s", prompt)
	}
}

// An interrupting post restarts the live agent through Revise's running path; the restarted prompt asks the
// QUESTION (not a revision), the agent's reply lands in the Discussion, and then the question is gone.
func TestDiscussion_InterruptRestartsLiveAgentWithQuestion(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	interrupt := liveAgent(o)

	qID, err := o.PostDiscussionMessage(context.Background(), humanPost("op-q", "how is the plan going?", nil, true))
	if err != nil {
		t.Fatalf("PostDiscussionMessage: %v", err)
	}
	if got := o.currentStatus(assistantsTestStageID); got != state.StatusRevising {
		t.Fatalf("status = %v, want revising: the live agent is interrupted before the API returns", got)
	}
	if reason := store.LastReviseReason(assistantsTestStageID); !strings.Contains(reason, qID) {
		t.Fatalf("EvRevise reason = %q, want it to name %s", reason, qID)
	}
	select {
	case <-interrupt:
	default:
		t.Fatal("the live agent's interrupt channel was not signaled")
	}
	if _, err := os.Stat(filepath.Join(o.opts.RunDir, assistantsTestStageID, "feedback.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a question must not be written as feedback (stat err = %v)", err)
	}

	assertAsksQuestion(t, captureAutonomousPrompt(t, o, o.runAutonomousWithFeedback), qID, "how is the plan going?")

	stageAgentReply(t, o, "op-a", qID, "half done, writing the API section")
	msgs := o.DiscussionMessages(assistantsTestStageID)
	if last := msgs[len(msgs)-1]; last.Author.Kind != assistants.AuthorMainAgent || last.ParentMessageID != qID {
		t.Fatalf("mirrored answer = %+v, want the stage agent's reply to %s", last, qID)
	}
	if note := o.discussionQuestionNote(assistantsTestStageID); note != "" {
		t.Fatalf("an answered question must leave the prompt, got:\n%s", note)
	}
}

// The interrupt request is part of the post: recorded in the same operation, and a retry asking differently
// under the same request id is a conflict.
func TestDiscussion_InterruptIntentIsDurableAndReplayed(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	ctx := context.Background()
	qID, err := o.PostDiscussionMessage(ctx, humanPost("op-q", "status?", nil, true))
	if err != nil {
		t.Fatalf("PostDiscussionMessage: %v", err)
	}
	if !o.DiscussionMessages(assistantsTestStageID)[0].Interrupt {
		t.Fatal("the post must record the interrupt request")
	}
	if again, err := o.PostDiscussionMessage(ctx, humanPost("op-q", "status?", nil, true)); err != nil || again != qID {
		t.Fatalf("replay = %q, %v; want %q", again, err, qID)
	}
	if _, err := o.PostDiscussionMessage(ctx, humanPost("op-q", "status?", nil, false)); err == nil {
		t.Fatal("the same request id without the interrupt must be a conflict")
	}
}

// reopenStore simulates a restart: the store is closed and the run is loaded again from its journal.
func reopenStore(t *testing.T, o *Orchestrator, store *state.Store) *state.Store {
	t.Helper()
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
	o.interruptChans.Delete(assistantsTestStageID)
	return reopened
}

// A crash after the post committed but before the agent was interrupted loses nothing: the question is in the
// journal, so the agent started after the restart is asked it.
func TestDiscussion_CrashBeforeInterruptStillAsksAfterRestart(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	// No live agent registered: the interrupt step does nothing, exactly like a process gone before it ran.
	qID, err := o.PostDiscussionMessage(context.Background(), humanPost("op-q", "still on track?", nil, true))
	if err != nil {
		t.Fatalf("PostDiscussionMessage: %v", err)
	}
	reopenStore(t, o, store)
	assertAsksQuestion(t, captureAutonomousPrompt(t, o, o.runAutonomousAgent), qID, "still on track?")
}

// A stage that is running but has no agent yet (before-hook, waiting for a command slot) is not interrupted:
// its first prompt carries the question.
func TestDiscussion_InterruptWithoutLiveAgentGoesIntoFirstPrompt(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	qID, err := o.PostDiscussionMessage(context.Background(), humanPost("op-q", "which API version?", nil, true))
	if err != nil {
		t.Fatalf("PostDiscussionMessage: %v", err)
	}
	if got := o.currentStatus(assistantsTestStageID); got != state.StatusRunning {
		t.Fatalf("status = %v, want running: there is no agent process to interrupt", got)
	}
	if reason := store.LastReviseReason(assistantsTestStageID); reason != "" {
		t.Fatalf("EvRevise reason = %q, want no revise", reason)
	}
	assertAsksQuestion(t, captureAutonomousPrompt(t, o, o.runAutonomousAgent), qID, "which API version?")
}

// Pause committed after the post's status check wins: the interrupt never moves a paused stage, and neither
// does a running-stage Revise decided before the pause.
func TestDiscussion_InterruptLosesToConcurrentPause(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	liveAgent(o)
	if _, ok := o.Trigger(assistantsTestStageID, bus.EvPause, bus.GuardCtx{}, "user pause"); !ok {
		t.Fatal("pause failed")
	}

	o.interruptForDiscussionQuestion(assistantsTestStageID, "dm-late")
	if got := o.currentStatus(assistantsTestStageID); got != state.StatusPaused {
		t.Fatalf("status = %v, want paused: an interrupt must not undo a pause", got)
	}
	if applied, _, err := o.reviseRunning(assistantsTestStageID, "late revise", func(string) error { return nil }); applied || err != nil {
		t.Fatalf("reviseRunning on a paused stage = %v, %v; want not applied", applied, err)
	}
	if got := o.currentStatus(assistantsTestStageID); got != state.StatusPaused {
		t.Fatalf("status = %v, want paused", got)
	}
}

// The question stays open until the stage agent replies — handing it over in an inbox (delivered receipt) is
// not an answer, so a crash between delivery and the reply still asks it again.
func TestDiscussion_QuestionSurvivesInboxDelivery(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	ctx := context.Background()
	qID, err := o.PostDiscussionMessage(ctx, humanPost("op-q", "blocked on anything?", nil, true))
	if err != nil {
		t.Fatalf("PostDiscussionMessage: %v", err)
	}
	if err := o.MarkMessageDelivered(ctx, agentcomm.MessageID(qID), meshStageParticipantID(assistantsTestStageID)); err != nil {
		t.Fatalf("MarkMessageDelivered: %v", err)
	}
	if note := o.discussionQuestionNote(assistantsTestStageID); !strings.Contains(note, qID) {
		t.Fatalf("a delivered but unanswered question must still be asked, got:\n%s", note)
	}
	stageAgentReply(t, o, "op-a", qID, "no")
	if note := o.discussionQuestionNote(assistantsTestStageID); note != "" {
		t.Fatalf("answered question still asked:\n%s", note)
	}
}

// A human post wakes the stage agent parked waiting for a colleague, so it gets a bounded turn to answer.
func TestDiscussion_HumanPostWakesStageWaitingForColleague(t *testing.T) {
	o, _ := newTwoStageMeshOrchestrator(t)
	ctx := context.Background()
	events := o.critical.Recv()
	waiter := meshStageParticipantID(assistantsTestStageID)
	colleagueThread, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-colleague-open", Members: []agentcomm.ParticipantID{waiter, meshStageParticipantID("docs")}, Scope: agentcomm.ScopeStage,
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	o.Trigger(assistantsTestStageID, bus.EvAwaitPeer, bus.GuardCtx{Phase: phaseImplementation}, "")
	o.prePeerPhase.Store(assistantsTestStageID, phaseImplementation)
	if _, err := o.Wait(ctx, WaitRequest{
		OperationID: "op-wait", Waiter: waiter,
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: colleagueThread.ID},
	}); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	for len(events) > 0 {
		<-events
	}

	if _, err := o.PostDiscussionMessage(ctx, humanPost("op-q", "while you wait: which DB?", nil, false)); err != nil {
		t.Fatalf("PostDiscussionMessage: %v", err)
	}
	for {
		select {
		case ev := <-events:
			if ev.Type == bus.EventPeerQuestion && ev.StageID == assistantsTestStageID {
				return
			}
		default:
			t.Fatal("the waiting stage was not given a turn to answer the human")
		}
	}
}

// A post that mentions another agent goes to the stage agent AND that agent, in a stage discussion thread
// with exactly those members; the mentioned agent's reply shows in this stage's Discussion under its own name,
// while a new message in that thread that answers nothing stays out of the Discussion.
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

	if _, err := o.Send(ctx, SendRequest{
		OperationID: "op-docs-aside", ThreadID: threadID, Sender: docs, Recipients: []agentcomm.ParticipantID{stageAgent},
		Kind: agentcomm.MessageKindMessage, Body: "unrelated: lunch?", Actor: state.Actor{Kind: "agent", ID: string(docs)},
	}); err != nil {
		t.Fatalf("aside: %v", err)
	}
	if got := len(o.DiscussionMessages(assistantsTestStageID)); got != len(msgs) {
		t.Fatalf("discussion has %d messages after a non-reply in its thread, want %d: only answers are mirrored", got, len(msgs))
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

// A stage that finishes without answering the human says so: a feed notice, and the post is marked
// unanswered for the Discussion. A replied post is not.
func TestDiscussion_FinishedStageMarksUnansweredHumanPosts(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	ctx := context.Background()
	answered, err := o.PostDiscussionMessage(ctx, humanPost("op-1", "first", nil, false))
	if err != nil {
		t.Fatal(err)
	}
	stageAgentReply(t, o, "op-1a", answered, "done")
	ignored, err := o.PostDiscussionMessage(ctx, humanPost("op-2", "second", nil, false))
	if err != nil {
		t.Fatal(err)
	}
	if got := o.UnansweredDiscussionPosts(assistantsTestStageID); len(got) != 0 {
		t.Fatalf("a running stage has no unanswered posts yet, got %v", got)
	}

	if _, ok := o.Trigger(assistantsTestStageID, bus.EvComplete, bus.GuardCtx{}, "done"); !ok {
		t.Fatal("complete failed")
	}
	if got := o.UnansweredDiscussionPosts(assistantsTestStageID); len(got) != 1 || !got[ignored] {
		t.Fatalf("unanswered = %v, want only %s", got, ignored)
	}
	notices, err := os.ReadFile(filepath.Join(o.opts.RunDir, "notices.jsonl"))
	if err != nil || !strings.Contains(string(notices), "without answering 1 of your discussion message(s): "+ignored) {
		t.Fatalf("notices = %q (%v), want the unanswered post named", notices, err)
	}
}

// A journal written by the code before mentions and interrupts existed (testdata, captured from main) loads
// unchanged, and the discussion keeps working on it.
func TestDiscussion_PreMentionsJournalReplays(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
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
	fixture, err := os.ReadFile(filepath.Join("testdata", "discussion-pre-mentions", "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(o.opts.RunDir, "events.jsonl"), fixture, 0o644); err != nil {
		t.Fatal(err)
	}
	reopened, err := state.Open(o.opts.RunDir, []string{assistantsTestStageID})
	if err != nil {
		t.Fatalf("open the pre-change journal: %v", err)
	}
	t.Cleanup(func() { reopened.Close() })
	o.opts.Store = reopened
	o.fsm = bus.NewFSM(reopened)

	msgs := o.DiscussionMessages(assistantsTestStageID)
	if len(msgs) != 2 || msgs[0].Body != "old question" || msgs[0].Mentions != nil || msgs[0].Interrupt ||
		msgs[1].Body != "old answer" || msgs[1].ParentMessageID != msgs[0].ID {
		t.Fatalf("replayed discussion = %+v, want the old question and its mirrored answer", msgs)
	}
	if note := o.discussionQuestionNote(assistantsTestStageID); note != "" {
		t.Fatalf("an old post never asked to interrupt, got:\n%s", note)
	}
	qID, err := o.PostDiscussionMessage(context.Background(), humanPost("op-new", "new question", nil, false))
	if err != nil {
		t.Fatalf("post on the old journal: %v", err)
	}
	stageAgentReply(t, o, "op-new-a", qID, "new answer")
	if msgs := o.DiscussionMessages(assistantsTestStageID); len(msgs) != 4 || msgs[3].ParentMessageID != qID {
		t.Fatalf("discussion = %+v, want the new answer mirrored in the old thread", msgs)
	}
}
