package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

// A run agent's accepted turn result is posted as a PASSIVE reply to the message that woke the turn.

const needsChangesWithTwoFindings = `{"schema_version":1,"kind":"result","verdict":"needs_changes","summary":"checked the checkout flow",` +
	`"findings":[{"id":"f1","severity":"minor","detail":"a"},{"id":"f2","severity":"major","detail":"b"}]}`

func sendAs(t *testing.T, o *Orchestrator, opID string, thread agentcomm.ThreadID, from, to agentcomm.ParticipantID, body string, inReplyTo *agentcomm.MessageID, auto bool) agentcomm.MessageID {
	t.Helper()
	kind := agentcomm.MessageKindMessage
	if inReplyTo != nil {
		kind = agentcomm.MessageKindReply
	}
	receipt, err := o.Send(context.Background(), SendRequest{
		OperationID: opID, ThreadID: thread, Sender: from, Recipients: []agentcomm.ParticipantID{to},
		Kind: kind, InReplyTo: inReplyTo, Body: body, AutoResult: auto,
	})
	if err != nil {
		t.Errorf("Send %s: %v", opID, err)
	}
	return receipt.MessageID
}

func openThread(t *testing.T, o *Orchestrator, opID string, maxMessages int, members ...agentcomm.ParticipantID) agentcomm.ThreadID {
	t.Helper()
	th, err := o.OpenThread(context.Background(), OpenThreadRequest{OperationID: opID, Members: members, Scope: agentcomm.ScopeRun, MaxMessages: maxMessages})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	return th.ID
}

// repliesTo returns the messages answering id.
func repliesTo(store *state.Store, id agentcomm.MessageID) []agentcomm.Message {
	var out []agentcomm.Message
	for _, m := range store.Snapshot().Assistants.Mesh.Messages {
		if m.InReplyTo != nil && *m.InReplyTo == id {
			out = append(out, *m)
		}
	}
	return out
}

func noticesText(t *testing.T, o *Orchestrator) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(o.opts.RunDir, "notices.jsonl"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(raw)
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting until %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestTurnReply_AnswersTheWakingMessage(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
		return executor.HelperOutcome{ProcessOK: true, RawText: needsChangesWithTwoFindings}, nil
	}
	msg := "please review the checkout flow"
	res := addTestRunAgent(t, o, "op-reply", &msg)
	waitForRunAgentTurn(t, store, res.TaskID, 2*time.Second)
	o.concurrency.WaitAgents()

	replies := repliesTo(store, *res.MessageID)
	if len(replies) != 1 {
		t.Fatalf("replies to the waking message = %d, want 1", len(replies))
	}
	r := replies[0]
	if r.ID != turnReplyMessageID(res.TaskID, 1) || r.Sender != res.ParticipantID || r.ThreadID != res.ThreadID ||
		len(r.Recipients) != 1 || r.Recipients[0] != meshUserParticipantID || !r.AutoResult || r.Kind != agentcomm.MessageKindReply {
		t.Fatalf("turn reply = %+v, want a passive reply from the agent to the human in the same thread", r)
	}
	for _, want := range []string{"checked the checkout flow", "2 findings", "needs_changes", string(res.TaskID)} {
		if !strings.Contains(r.Body, want) {
			t.Fatalf("turn reply body %q lacks %q", r.Body, want)
		}
	}
	if strings.Contains(r.Body, "schema_version") {
		t.Fatalf("the turn reply must be text, not the raw result JSON: %q", r.Body)
	}
	if tk := store.Snapshot().Assistants.Tasks[res.TaskID]; tk.Status != assistants.StatusQueued || tk.LastVerdict != assistants.VerdictNeedsChanges {
		t.Fatalf("task = %s/%s, want idle with the turn's result", tk.Status, tk.LastVerdict)
	}
}

// The waking message is the last of the turn's inventory that is not itself a passive turn result.
func TestTurnReply_AnswersTheLastNonPassiveMessage(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	var calls atomic.Int32
	firstTurn := make(chan struct{})
	release := make(chan struct{})
	o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
		if calls.Add(1) == 1 {
			close(firstTurn)
			<-release
		}
		return passResultOutcome(), nil
	}
	msg := "first"
	res := addTestRunAgent(t, o, "op-last", &msg)
	<-firstTurn
	older := sendAs(t, o, "op-older", res.ThreadID, meshUserParticipantID, res.ParticipantID, "older", nil, false)
	waking := sendAs(t, o, "op-waking", res.ThreadID, meshUserParticipantID, res.ParticipantID, "the latest question", nil, false)
	passive := sendAs(t, o, "op-passive", res.ThreadID, meshUserParticipantID, res.ParticipantID, "someone's turn result", nil, true)
	close(release)
	waitUntil(t, "the second turn finished", func() bool {
		return calls.Load() == 2 && store.Snapshot().Assistants.Tasks[res.TaskID].Status == assistants.StatusQueued
	})
	o.concurrency.WaitAgents()

	if got := repliesTo(store, waking); len(got) != 1 || got[0].ID != turnReplyMessageID(res.TaskID, 2) {
		t.Fatalf("replies to the waking message = %+v, want the second turn's reply", got)
	}
	if len(repliesTo(store, older)) != 0 || len(repliesTo(store, passive)) != 0 {
		t.Fatal("only the waking message gets the turn reply")
	}
}

// No waking input — the turn was handed only a passive message, or nothing — no reply.
func TestTurnReply_NoWakingInputNoReply(t *testing.T) {
	ms := state.MeshState{Messages: map[agentcomm.MessageID]*agentcomm.Message{"p": {ID: "p", AutoResult: true}}}
	if _, ok := wakingMessage(ms, nil); ok {
		t.Fatal("a turn without input has no waking message")
	}
	if _, ok := wakingMessage(ms, []agentcomm.MessageID{"p"}); ok {
		t.Fatal("a passive turn result never wakes a turn")
	}
}

// Dedup: the agent's own reply to the waking message replaces the turn reply; a counter-question to someone
// else, a new message in the thread or a reply to a different message do not.
func TestTurnReply_SkippedOnlyWhenTheAgentAnsweredTheWakingMessage(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	var calls atomic.Int32
	var res AttachAndSendResult
	var first, second agentcomm.MessageID
	added := make(chan struct{})
	o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
		<-added
		switch calls.Add(1) {
		case 1:
			sendAs(t, o, "op-own-answer", res.ThreadID, res.ParticipantID, meshUserParticipantID, "my own answer", &first, false)
		case 2:
			other := openThread(t, o, "op-other-thread", 0, res.ParticipantID, meshStageParticipantID(assistantsTestStageID))
			sendAs(t, o, "op-counter-question", other, res.ParticipantID, meshStageParticipantID(assistantsTestStageID), "which port?", nil, false)
			sendAs(t, o, "op-note", res.ThreadID, res.ParticipantID, meshUserParticipantID, "working on it", nil, false)
			sendAs(t, o, "op-late-answer", res.ThreadID, res.ParticipantID, meshUserParticipantID, "one more thing about the first", &first, false)
		default:
		}
		return passResultOutcome(), nil
	}
	msg := "first question"
	res = addTestRunAgent(t, o, "op-dedup", &msg)
	first = *res.MessageID
	close(added)
	waitForRunAgentTurn(t, store, res.TaskID, 2*time.Second)
	o.concurrency.WaitAgents()
	if got := repliesTo(store, first); len(got) != 1 || got[0].AutoResult {
		t.Fatalf("replies to the first question = %+v, want only the agent's own", got)
	}

	second = sendAs(t, o, "op-second", res.ThreadID, meshUserParticipantID, res.ParticipantID, "second question", nil, false)
	waitUntil(t, "the second turn finished", func() bool {
		return calls.Load() == 2 && store.Snapshot().Assistants.Tasks[res.TaskID].Status == assistants.StatusQueued
	})
	o.concurrency.WaitAgents()
	if got := repliesTo(store, second); len(got) != 1 || !got[0].AutoResult {
		t.Fatalf("replies to the second question = %+v, want the passive turn reply", got)
	}
	if strings.Contains(noticesText(t, o), "not delivered") {
		t.Fatal("a skipped duplicate is not a delivery failure")
	}
}

// The agent's own reply committing after the turn reply was planned still wins: the check runs at commit time,
// and the turn's result is recorded without the duplicate.
func TestTurnReply_OwnReplyRacingTheFinishWins(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	started := make(chan struct{})
	release := make(chan struct{})
	o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
		close(started)
		<-release
		return passResultOutcome(), nil
	}
	msg := "question"
	res := addTestRunAgent(t, o, "op-race", &msg)
	<-started

	result := assistants.Result{Verdict: assistants.VerdictPass, Summary: "done"}
	reply := o.planTurnReply(res.TaskID, 1, result)
	if reply == nil {
		t.Fatal("no turn reply planned")
	}
	sendAs(t, o, "op-own", res.ThreadID, res.ParticipantID, meshUserParticipantID, "my own answer", res.MessageID, false)
	resultEff, err := taskResultEffect(res.TaskID, result)
	if err != nil {
		t.Fatal(err)
	}
	o.finishTaskTurnReplying(res.TaskID, 1, assistants.StatusRunning, assistants.StatusQueued, runAgentIdleReason, reply, resultEff)
	close(release)
	o.concurrency.WaitAgents()

	if got := repliesTo(store, *res.MessageID); len(got) != 1 || got[0].AutoResult {
		t.Fatalf("replies = %+v, want only the agent's own", got)
	}
	if tk := store.Snapshot().Assistants.Tasks[res.TaskID]; tk.Status != assistants.StatusQueued || tk.LastVerdict != assistants.VerdictPass {
		t.Fatalf("task = %s/%s, want the result recorded", tk.Status, tk.LastVerdict)
	}
	if strings.Contains(noticesText(t, o), "not delivered") {
		t.Fatal("a skipped duplicate is not a delivery failure")
	}
}

// A turn reply the mesh refuses (closed or full thread) never keeps the turn from finishing: the result is
// recorded without it, and the feed says why it was not delivered.
func TestTurnReply_UndeliverableReplyFinishesTheTurnWithoutIt(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		setup        func(t *testing.T, o *Orchestrator, agent agentcomm.ParticipantID) (agentcomm.ThreadID, agentcomm.MessageID)
		duringTurn   func(t *testing.T, o *Orchestrator, thread agentcomm.ThreadID)
	}{
		{
			name: "thread closed", reason: "closed",
			setup: func(t *testing.T, o *Orchestrator, agent agentcomm.ParticipantID) (agentcomm.ThreadID, agentcomm.MessageID) {
				th := openThread(t, o, "op-th", 0, meshUserParticipantID, agent)
				return th, sendAs(t, o, "op-wake", th, meshUserParticipantID, agent, "question", nil, false)
			},
			duringTurn: func(t *testing.T, o *Orchestrator, thread agentcomm.ThreadID) {
				if err := o.CloseThread(context.Background(), CloseThreadRequest{OperationID: "op-close", ThreadID: thread}); err != nil {
					t.Errorf("CloseThread: %v", err)
				}
			},
		},
		{
			name: "thread full", reason: "limit",
			setup: func(t *testing.T, o *Orchestrator, agent agentcomm.ParticipantID) (agentcomm.ThreadID, agentcomm.MessageID) {
				th := openThread(t, o, "op-th", 1, meshUserParticipantID, agent)
				return th, sendAs(t, o, "op-wake", th, meshUserParticipantID, agent, "question", nil, false)
			},
			duringTurn: func(*testing.T, *Orchestrator, agentcomm.ThreadID) {},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, store := newAssistantsTestOrchestrator(t, true)
			var thread agentcomm.ThreadID
			ready := make(chan struct{})
			o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
				<-ready
				tc.duringTurn(t, o, thread)
				return passResultOutcome(), nil
			}
			res := addTestRunAgent(t, o, "op-agent", nil)
			var waking agentcomm.MessageID
			thread, waking = tc.setup(t, o, res.ParticipantID)
			close(ready)
			waitForRunAgentTurn(t, store, res.TaskID, 2*time.Second)
			o.concurrency.WaitAgents()

			if got := repliesTo(store, waking); len(got) != 0 {
				t.Fatalf("replies = %+v, want none (undeliverable)", got)
			}
			notices := noticesText(t, o)
			if !strings.Contains(notices, "turn result of task "+string(res.TaskID)+" not delivered") || !strings.Contains(notices, tc.reason) {
				t.Fatalf("notices = %s, want the reason the turn result was not delivered", notices)
			}
		})
	}
}

// The delegate-result mirror follows the same rule: a parent whose inbox is full never keeps its child running.
func TestDelegateMirror_RejectedMirrorStillFinishesTheChild(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	var calls atomic.Int32
	releaseChild := make(chan struct{})
	o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
		switch calls.Add(1) {
		case 1:
			return delegateOutcome(childJSON(assistants.ModeAdvisory)), nil
		case 2:
			<-releaseChild
		default:
		}
		return passResultOutcome(), nil
	}
	parentID := attachTestTask(context.Background(), t, o, "op-parent")
	waitForTaskStatus(t, store, parentID, assistants.StatusWaitingChildren, 2*time.Second)
	childID := store.Snapshot().Assistants.ByParent[parentID][0]

	// Fill the parent's inbox to MaxInboxUnread in one operation.
	parent := meshHelperParticipantID(parentID)
	thread := openThread(t, o, "op-fill-thread", 0, meshUserParticipantID, parent)
	var effects []state.Effect
	for i := range agentcomm.MaxInboxUnread {
		eff, err := messageSentEffect(SendRequest{ThreadID: thread, Sender: meshUserParticipantID, Recipients: []agentcomm.ParticipantID{parent},
			Kind: agentcomm.MessageKindMessage, Body: "x"}, deriveMeshMessageID(fmt.Sprintf("fill-%d", i)))
		if err != nil {
			t.Fatal(err)
		}
		effects = append(effects, eff)
	}
	if _, err := store.ApplyRunOperation(state.Operation{ID: "op-fill", Kind: opKindMeshSend, Effects: effects}, nil); err != nil {
		t.Fatalf("fill the inbox: %v", err)
	}

	close(releaseChild)
	waitForTaskStatus(t, store, childID, assistants.StatusCompleted, 2*time.Second)
	o.concurrency.WaitAgents()
	if _, ok := store.Snapshot().Assistants.Mesh.Messages[deriveMeshMessageID("delegate-result:"+string(childID))]; ok {
		t.Fatal("the mirror cannot have been delivered to a full inbox")
	}
	if !strings.Contains(noticesText(t, o), "turn result of task "+string(childID)+" not delivered") {
		t.Fatal("no notice that the child's result was not delivered")
	}
}

// A run agent's turn answering a human's Discussion post (the agent was mentioned) is mirrored into the
// Discussion as the answer, so the post is not left "Not answered".
func TestTurnReply_AnswersADiscussionPostThroughTheMirror(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
		return passResultOutcome(), nil
	}
	res := addTestRunAgent(t, o, "op-agent", nil)
	post, err := o.PostDiscussionMessage(context.Background(), PostMessageRequest{
		OperationID: "op-post", StageID: assistantsTestStageID, AuthorKind: assistants.AuthorHuman, AuthorID: "user",
		Round: assistants.RoundFreeform, Body: "@qa does the checkout work?", Mentions: []agentcomm.ParticipantID{res.ParticipantID},
		Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Fatalf("PostDiscussionMessage: %v", err)
	}
	waitForRunAgentTurn(t, store, res.TaskID, 2*time.Second)
	o.concurrency.WaitAgents()

	var answer *assistants.DiscussionMessage
	for _, m := range o.DiscussionMessages(assistantsTestStageID) {
		if m.ParentMessageID == post {
			answer = &m
		}
	}
	if answer == nil || !answer.AutoResult || answer.Author.ID != string(res.ParticipantID) {
		t.Fatalf("discussion answer = %+v, want the agent's turn result mirrored as a passive answer", answer)
	}
	rs := store.Snapshot()
	rs.Stages[assistantsTestStageID] = state.StageState{Status: state.StatusDone}
	if got := unansweredHumanPosts(rs, assistantsTestStageID); len(got) != 0 {
		t.Fatalf("unanswered posts = %v, want none: the agent's turn result answered it", got)
	}
}

// ---- The waking-unread rule at every call site: a passive turn result wakes nobody. ----

// Idle run agent: a turn reply addressed to another run agent leaves it idle; its next real message starts a
// turn that sees both and answers the real one.
func TestPassive_DoesNotWakeAnIdleRunAgent(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	var prompts []string
	o.runHelperAgent = func(_ context.Context, _, _, _, prompt, _, _ string) (executor.HelperOutcome, error) {
		prompts = append(prompts, prompt)
		return passResultOutcome(), nil
	}
	a := addTestRunAgent(t, o, "op-a", nil)
	b, err := o.AddRunAgent(context.Background(), AddRunAgentRequest{OperationID: "op-b", ProfileID: "reviewer", Title: "B", Instruction: "review", Actor: state.Actor{Kind: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	th := openThread(t, o, "op-ab", 0, a.ParticipantID, b.ParticipantID)
	question := sendAs(t, o, "op-b-asks", th, b.ParticipantID, a.ParticipantID, "b asks a", nil, false)
	waitForRunAgentTurn(t, store, a.TaskID, 2*time.Second)
	o.concurrency.WaitAgents()

	reply := repliesTo(store, question)
	if len(reply) != 1 || !reply[0].AutoResult {
		t.Fatalf("replies to b's question = %+v, want a's passive turn reply", reply)
	}
	rs := store.Snapshot()
	if tk := rs.Assistants.Tasks[b.TaskID]; tk.Status != assistants.StatusQueued || tk.Turn != 0 {
		t.Fatalf("b = %s turn %d, want still idle: a turn result does not wake", tk.Status, tk.Turn)
	}
	if !idleRunAgent(rs, rs.Assistants.Tasks[b.TaskID]) {
		t.Fatal("b must count as idle with only a passive message waiting")
	}
	if runAgentHasUnseenMessages(rs) {
		t.Fatal("a passive message must not hold the run open (run-finalization blocker)")
	}

	userThread := openThread(t, o, "op-ub", 0, meshUserParticipantID, b.ParticipantID)
	userMsg := sendAs(t, o, "op-user-b", userThread, meshUserParticipantID, b.ParticipantID, "user asks b", nil, false)
	waitForRunAgentTurn(t, store, b.TaskID, 2*time.Second)
	o.concurrency.WaitAgents()
	if last := prompts[len(prompts)-1]; !strings.Contains(last, "looks fine") {
		t.Fatalf("b's turn must see a's turn result in its inbox:\n%s", last)
	}
	if got := repliesTo(store, userMsg); len(got) != 1 {
		t.Fatalf("b's turn reply must answer the user, got %+v", got)
	}
	if got := repliesTo(store, reply[0].ID); len(got) != 0 {
		t.Fatal("a turn result is never answered by another turn result")
	}
}

// Stage extra turns: a passive message arriving mid-turn does not earn the stage another turn.
func TestPassive_DoesNotExtendAStageTurn(t *testing.T) {
	runner := &scriptedStageRunner{}
	o, store := newUnreadTestOrchestrator(t, flow.AgentAuto, runner)
	runner.turn = func(n int32) error {
		if n == 1 {
			th := deriveMeshThreadID("unread-thread")
			if _, err := o.OpenThread(context.Background(), OpenThreadRequest{
				OperationID: "unread-thread", Members: []agentcomm.ParticipantID{meshUserParticipantID, unreadTestStage}, Scope: agentcomm.ScopeStage,
			}); err != nil {
				t.Errorf("OpenThread: %v", err)
			}
			sendAs(t, o, "op-passive", th, meshUserParticipantID, unreadTestStage, "a turn result", nil, true)
		}
		return nil
	}
	runToEnd(t, o)
	if got := runner.turns.Load(); got != 1 {
		t.Fatalf("stage turns = %d, want 1: a turn result is no reason for an extra turn", got)
	}
	if got := store.Get(unreadTestStageID); got != state.StatusDone {
		t.Fatalf("stage = %s, want done", got)
	}
}

// Run finalization: a finished stage with only a passive message unread does not hold the run open.
func TestPassive_FinishedStageDoesNotBlockTheRun(t *testing.T) {
	ms := state.MeshState{
		Messages: map[agentcomm.MessageID]*agentcomm.Message{"p": {ID: "p", AutoResult: true}},
		Inbox:    map[agentcomm.ParticipantID][]agentcomm.MessageID{meshStageParticipantID("s"): {"p"}},
		Receipts: map[agentcomm.MessageID]map[agentcomm.ParticipantID]agentcomm.DeliveryStatus{"p": {meshStageParticipantID("s"): agentcomm.DeliveryAccepted}},
	}
	rs := *state.NewRunState([]string{"s"})
	rs.Stages["s"] = state.StageState{Status: state.StatusDone}
	rs.Assistants.Mesh = ms
	if finishedStageHasUnread(rs) {
		t.Fatal("a passive message must not keep a finished stage's run open")
	}
	ms.Messages["p"] = &agentcomm.Message{ID: "p"}
	if !finishedStageHasUnread(rs) {
		t.Fatal("an ordinary unread message still does")
	}
}

// Counter-question and thread-less addressed_message: a run agent parked on a wait is not resumed by a passive
// message; its reply_to wait on the very message the passive reply answers is.
func TestPassive_WaitingAgentIsResumedOnlyByWhatItWaitsFor(t *testing.T) {
	for _, tc := range []struct {
		name       string
		cond       func(asked agentcomm.MessageID) agentcomm.WaitCondition
		resolvedBy string // "reply" — the passive reply resolves the wait; "" — nothing passive does
	}{
		{name: "counter-question to a reply_to waiter", cond: func(asked agentcomm.MessageID) agentcomm.WaitCondition {
			return agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: asked}
		}, resolvedBy: "reply"},
		{name: "thread-less addressed_message", cond: func(agentcomm.MessageID) agentcomm.WaitCondition {
			return agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, store := newAssistantsTestOrchestrator(t, true)
			var calls atomic.Int32
			var a AttachAndSendResult
			var asked agentcomm.MessageID
			var th agentcomm.ThreadID
			ready := make(chan struct{})
			o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
				<-ready
				o.deliverRenderedInbox(a.ParticipantID)() // what the real executor does once the process starts
				if calls.Add(1) == 1 {
					th = openThread(t, o, "op-th", 0, a.ParticipantID, meshUserParticipantID)
					asked = sendAs(t, o, "op-ask", th, a.ParticipantID, meshUserParticipantID, "what now?", nil, false)
					if _, err := o.Wait(context.Background(), WaitRequest{OperationID: "op-wait", Waiter: a.ParticipantID, Condition: tc.cond(asked), Deadline: testFarDeadline()}); err != nil {
						t.Errorf("Wait: %v", err)
					}
					return executor.HelperOutcome{ProcessOK: true}, nil
				}
				return passResultOutcome(), nil
			}
			msg := "start"
			a = addTestRunAgent(t, o, "op-a", &msg)
			close(ready)
			waitForTaskStatus(t, store, a.TaskID, assistants.StatusWaitingPeer, 2*time.Second)

			// A passive message that answers nothing: neither a counter-question nor "anybody wrote to me".
			sendAs(t, o, "op-passive-note", th, meshUserParticipantID, a.ParticipantID, "a turn result", nil, true)
			time.Sleep(50 * time.Millisecond)
			if got := store.Snapshot().Assistants.Tasks[a.TaskID].Status; got != assistants.StatusWaitingPeer || calls.Load() != 1 {
				t.Fatalf("after a passive message: %s with %d turns, want still waiting", got, calls.Load())
			}
			if tc.resolvedBy == "" {
				return
			}
			// A passive reply to the very message the agent waits on resolves its reply_to wait.
			sendAs(t, o, "op-passive-reply", th, meshUserParticipantID, a.ParticipantID, "the answer", &asked, true)
			waitForRunAgentTurn(t, store, a.TaskID, 2*time.Second)
			o.concurrency.WaitAgents()
			if calls.Load() != 2 {
				t.Fatalf("turns = %d, want the waiter resumed by its reply", calls.Load())
			}
		})
	}
}
