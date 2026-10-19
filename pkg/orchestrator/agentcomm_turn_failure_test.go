package orchestrator

import (
	"context"
	"fmt"
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

// A run agent is a standing role: one turn whose answer cannot be accepted must not end it, and nobody waiting
// on it may wait forever.

// invalidResultOutcome is an answer the strict decoder rejects: a verdict outside the enum.
func invalidResultOutcome() executor.HelperOutcome {
	return executor.HelperOutcome{ProcessOK: true, RawText: `{"schema_version":1,"kind":"result","verdict":"approved","summary":"fine"}`}
}

// A run agent's turn with an invalid answer ends idle, not failed: the message that woke it gets a passive reply
// saying why there is no result, the agent stays reachable, and the next message starts a normal turn.
func TestRunAgentInvalidResult_GoesIdleAndAnswersWhy(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	var calls atomic.Int32
	o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
		if calls.Add(1) == 1 {
			return invalidResultOutcome(), nil
		}
		return passResultOutcome(), nil
	}
	msg := "please review"
	res := addTestRunAgent(t, o, "op-add", &msg)
	waitUntil(t, "the failed turn reply", func() bool { return len(repliesTo(store, *res.MessageID)) == 1 })
	o.concurrency.WaitAgents()

	tk := store.Snapshot().Assistants.Tasks[res.TaskID]
	if tk.Status != assistants.StatusQueued || !strings.HasPrefix(tk.StatusReason, runAgentIdleReason) || !strings.Contains(tk.StatusReason, "invalid result") {
		t.Fatalf("task = %s %q, want idle with the failure recorded", tk.Status, tk.StatusReason)
	}
	reply := repliesTo(store, *res.MessageID)[0]
	if !reply.AutoResult || reply.Sender != res.ParticipantID {
		t.Fatalf("reply = %+v, want a passive reply from the agent", reply)
	}
	for _, want := range []string{"QA could not produce a valid result", "invalid result", `"approved"`} {
		if !strings.Contains(reply.Body, want) {
			t.Fatalf("reply body %q lacks %q", reply.Body, want)
		}
	}
	if p, _ := resolveParticipant(store.Snapshot(), res.ParticipantID); !p.Reachable {
		t.Fatalf("participant = %+v, want reachable after a failed turn", p)
	}
	if !idleRunAgent(store.Snapshot(), tk) {
		t.Fatal("the agent must be idle: it starts only on a new message")
	}

	sendAs(t, o, "op-again", res.ThreadID, meshUserParticipantID, res.ParticipantID, "try again", nil, false)
	waitForRunAgentTurn(t, store, res.TaskID, 2*time.Second)
	if got := store.Snapshot().Assistants.Tasks[res.TaskID].LastVerdict; got != assistants.VerdictPass {
		t.Fatalf("second turn verdict = %q, want pass", got)
	}
}

// A stage helper (not a standing role) still fails on an invalid answer — its retry is the human's call.
func TestHelperInvalidResult_StillFails(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
		return invalidResultOutcome(), nil
	}
	taskID := attachTestTask(context.Background(), t, o, "op-helper")
	tk := waitForTaskStatus(t, store, taskID, assistants.StatusFailed, 2*time.Second)
	if !strings.HasPrefix(tk.StatusReason, "protocol_error: ") {
		t.Fatalf("reason = %q, want the protocol error", tk.StatusReason)
	}
}

// reviewOutcome is how one reviewer of reviewRun behaves.
type reviewOutcome int

const (
	reviewPasses reviewOutcome = iota
	reviewInvalid
	reviewCannotStart
	reviewCancelled
)

// reviewRun reproduces the live run: the stage asks two run agents for a review and waits for both replies
// (reply_to) — a yield, so it parks in awaiting_peer. The reviewers are held until the stage has parked. The
// second reviewer then behaves as second says. Whatever it does, the stage must get its continuation turn and
// finish — never wait forever on a reply nobody will send.
func reviewRun(t *testing.T, second reviewOutcome) {
	t.Helper()
	runner := &scriptedStageRunner{}
	o, store := newUnreadTestOrchestrator(t, flow.AgentAuto, runner)
	release := make(chan struct{})
	var revs []agentcomm.ParticipantID
	var tasks []assistants.TaskID
	o.runHelperAgent = func(_ context.Context, taskID, _, _, _, _, _ string) (executor.HelperOutcome, error) {
		awaitRelease(t, release)
		if assistants.TaskID(taskID) != tasks[1] {
			return passResultOutcome(), nil
		}
		switch second {
		case reviewInvalid:
			return invalidResultOutcome(), nil
		case reviewCannotStart:
			return executor.HelperOutcome{ProcessErr: fmt.Errorf("%w: helper-cmd: not found", executor.ErrAgentStart)}, nil
		case reviewCancelled:
			return executor.HelperOutcome{Interrupted: true}, nil
		default:
			return passResultOutcome(), nil
		}
	}
	members := []agentcomm.ParticipantID{unreadTestStage}
	for i := range 2 {
		res, err := o.AddRunAgent(context.Background(), AddRunAgentRequest{
			OperationID: fmt.Sprintf("op-reviewer-%d", i), ProfileID: "qa", Title: fmt.Sprintf("Reviewer %d", i), Actor: state.Actor{Kind: "user"},
		})
		if err != nil {
			t.Fatalf("AddRunAgent: %v", err)
		}
		revs = append(revs, res.ParticipantID)
		tasks = append(tasks, res.TaskID)
		members = append(members, res.ParticipantID)
	}
	th, err := o.OpenThread(context.Background(), OpenThreadRequest{OperationID: "op-review-thread", Members: members, Scope: agentcomm.ScopeStage})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	runner.turn = func(n int32) error {
		if n == 1 {
			askReviewers(t, o, unreadTestStage, th.ID, revs)
			go func() {
				waitUntil(t, "the stage parks", func() bool { return store.Get(unreadTestStageID) == state.StatusAwaitingPeer })
				if second == reviewCancelled {
					if err := o.CancelTask(context.Background(), tasks[1]); err != nil {
						t.Errorf("CancelTask: %v", err)
					}
				}
				close(release)
			}()
		}
		return nil
	}

	runToEnd(t, o)

	// Each reply may get its own continuation (the first one re-parks for the other), so 2 or 3 turns.
	if got := runner.turns.Load(); got < 2 || got > 3 {
		t.Fatalf("stage turns = %d, want the asking turn and one continuation per answer", got)
	}
	if got := store.Get(unreadTestStageID); got != state.StatusDone {
		t.Fatalf("stage status = %s, want done", got)
	}
}

func TestReviewWait_StageContinuesWhateverTheReviewerDoes(t *testing.T) {
	cases := map[string]reviewOutcome{
		"both pass":                        reviewPasses,
		"reviewer answers an invalid JSON": reviewInvalid,
		"reviewer cannot start (failed)":   reviewCannotStart,
		"reviewer cancelled by the user":   reviewCancelled,
	}
	for name, second := range cases {
		t.Run(name, func(t *testing.T) { reviewRun(t, second) })
	}
}

// The reviewer finished, but the process stopped before its waiters were released. The restart releases them:
// the wait is cancelled and the parked stage continues.
func TestReviewWait_RecoveryReleasesAWaitOnAFinishedReviewer(t *testing.T) {
	runDir := t.TempDir()
	o, store := newUnreadTestOrchestratorAt(t, runDir, flow.AgentAuto, &scriptedStageRunner{turn: func(int32) error { return nil }})
	ctx := context.Background()
	activateStage(t, o, unreadTestStageID)
	rev, err := o.AddRunAgent(ctx, AddRunAgentRequest{OperationID: "op-rev", ProfileID: "qa", Title: "QA", Actor: state.Actor{Kind: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	th, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-rev-thread", Members: []agentcomm.ParticipantID{unreadTestStage, rev.ParticipantID}, Scope: agentcomm.ScopeStage,
	})
	if err != nil {
		t.Fatal(err)
	}
	o.runHelperAgent = func(ctx context.Context, _, _, _, _, _, _ string) (executor.HelperOutcome, error) {
		<-ctx.Done() // the reviewer never answers: it is cancelled below
		return executor.HelperOutcome{Interrupted: true}, nil
	}
	waits := askReviewers(t, o, unreadTestStage, th.ID, []agentcomm.ParticipantID{rev.ParticipantID})
	epoch := store.Snapshot().Stages[unreadTestStageID].Epoch
	o.parkForPeers(ctx, unreadTestStageID, phaseAutonomous, epoch, o.turnEpisode(unreadTestStage, nil), func() { t.Error("no revise here") })
	// The reviewer's end is committed directly — as if the process stopped right after that commit.
	eff, err := taskStatusEffect(rev.TaskID, assistants.StatusCancelled, "cancelled by user")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyRunOperation(state.Operation{ID: "op-crash-cancel", Kind: "finish_task", Effects: []state.Effect{eff}}, nil); err != nil {
		t.Fatal(err)
	}
	if got := store.Snapshot().Assistants.Mesh.Waits[waits[0]].Status; got != agentcomm.WaitPending {
		t.Fatalf("wait before the restart = %s, want pending", got)
	}
	o.concurrency.WaitAgents()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	var turns atomic.Int32
	restarted, store2 := newUnreadTestOrchestratorAt(t, runDir, flow.AgentAuto, &scriptedStageRunner{turn: func(int32) error { turns.Add(1); return nil }})
	runCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(func() { cancel(); restarted.concurrency.WaitAgents() })

	restarted.releaseUnanswerableWaits(runCtx, restarted.resumeWaiterAfterTerminalWaitAtStartup)

	w := store2.Snapshot().Assistants.Mesh.Waits[waits[0]]
	if w.Status != agentcomm.WaitCancelled || !strings.Contains(w.Reason, "nobody left can answer") || !strings.Contains(w.Reason, "QA") {
		t.Fatalf("wait after recovery = %s %q, want cancelled with the reason", w.Status, w.Reason)
	}
	waitUntil(t, "the continuation turn", func() bool { return turns.Load() > 0 })
}

// A finished task's roster entry says how it ended, not just that it did.
func TestTaskEndedReason_SaysWhatHappened(t *testing.T) {
	cases := []struct {
		status assistants.TaskStatus
		reason string
		want   string
	}{
		{assistants.StatusFailed, "protocol_error: invalid verdict value \"approved\"", `task t-1 failed: invalid result — invalid verdict value "approved"`},
		{assistants.StatusFailed, "helper agent timed out", "task t-1 failed: helper agent timed out"},
		{assistants.StatusFailed, "", "task t-1 failed: no reason recorded"},
		{assistants.StatusCancelled, "cancelled by user", "task t-1 was cancelled by user"},
		{assistants.StatusCancelled, "", "task t-1 was cancelled"},
		{assistants.StatusCancelled, "its stage was revised", "task t-1 was cancelled: its stage was revised"},
		{assistants.StatusCompleted, "", "task t-1 has finished"},
	}
	for _, tc := range cases {
		ts := &state.TaskState{Task: assistants.Task{ID: "t-1", Status: tc.status, StatusReason: tc.reason}}
		if got := helperParticipant("t-1", ts).UnreachableReason; got != tc.want {
			t.Errorf("helper %s/%q: reason = %q, want %q", tc.status, tc.reason, got, tc.want)
		}
	}
}

// A rejected answer's reason can quote the model's output. It is redacted and bounded before it is recorded —
// in the task status, the notice, the reply and the roster alike.
func TestInvalidResultReason_RedactedAndBounded(t *testing.T) {
	huge := strings.Repeat("x", 64*1024)
	for _, scope := range []string{"run agent", "stage helper"} {
		t.Run(scope, func(t *testing.T) {
			o, store := newRedactingTestOrchestrator(t)
			o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
				return executor.HelperOutcome{ProcessOK: true, RawText: `{"schema_version":1,"kind":"result","verdict":"` +
					redactionTestSecret + huge + `","summary":"fine"}`}, nil
			}
			var taskID assistants.TaskID
			if scope == "run agent" {
				msg := "please review"
				res := addTestRunAgent(t, o, "op-add", &msg)
				taskID = res.TaskID
				waitUntil(t, "the failed turn reply", func() bool { return len(repliesTo(store, *res.MessageID)) == 1 })
			} else {
				taskID = attachTestTask(context.Background(), t, o, "op-helper")
				waitForTaskStatus(t, store, taskID, assistants.StatusFailed, 2*time.Second)
			}
			o.concurrency.WaitAgents()

			tk := store.Snapshot().Assistants.Tasks[taskID]
			if !strings.Contains(tk.StatusReason, "invalid verdict value") || len(tk.StatusReason) > 2*maxTaskReasonBytes {
				t.Fatalf("reason (%d bytes) = %.200q, want the bounded protocol error", len(tk.StatusReason), tk.StatusReason)
			}
			assertNoSecretIn(t, journalOf(o), filepath.Join(o.opts.RunDir, "notices.jsonl"))
			if strings.Contains(tk.StatusReason, redactionTestSecret) {
				t.Fatalf("status reason carries the secret: %.200q", tk.StatusReason)
			}
		})
	}
}

// The stage asks a reviewer; the reviewer yields on a question of its own, and its next turn is woken only by a
// passive answer to that question. Whatever that turn produces still answers the stage's request — it is the
// request the turns were working on — so the stage's reply_to wait never stays pending while the reviewer idles.
func TestTurnReply_AnswersTheRequestAcrossAYield(t *testing.T) {
	for _, second := range []string{"invalid", "pass"} {
		t.Run(second, func(t *testing.T) {
			o, store := newAssistantsTestOrchestrator(t, true)
			ctx := context.Background()
			activateStage(t, o, assistantsTestStageID)
			stage := meshStageParticipantID(assistantsTestStageID)
			rev, err := o.AddRunAgent(ctx, AddRunAgentRequest{OperationID: "op-rev", ProfileID: "qa", Title: "QA", Actor: state.Actor{Kind: "user"}})
			if err != nil {
				t.Fatal(err)
			}
			reviewThread := openThread(t, o, "op-review-thread", 0, stage, rev.ParticipantID)
			userThread := openThread(t, o, "op-user-thread", 0, rev.ParticipantID, meshUserParticipantID)

			var calls atomic.Int32
			var question atomic.Value
			o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
				if calls.Add(1) == 1 {
					q := sendAs(t, o, "op-rev-question", userThread, rev.ParticipantID, meshUserParticipantID, "which base revision?", nil, false)
					question.Store(q)
					if _, err := o.Wait(context.Background(), WaitRequest{
						OperationID: "op-rev-wait", Waiter: rev.ParticipantID,
						Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: q},
					}); err != nil {
						t.Errorf("reviewer Wait: %v", err)
					}
					return executor.HelperOutcome{ProcessOK: true}, nil // a yield
				}
				if second == "invalid" {
					return invalidResultOutcome(), nil
				}
				return passResultOutcome(), nil
			}

			request := sendAs(t, o, "op-request", reviewThread, stage, rev.ParticipantID, "please review", nil, false)
			stageWait, err := o.Wait(ctx, WaitRequest{
				OperationID: "op-stage-wait", Waiter: stage, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: request},
			})
			if err != nil {
				t.Fatalf("stage Wait: %v", err)
			}
			waitForTaskStatus(t, store, rev.TaskID, assistants.StatusWaitingPeer, 2*time.Second)
			q := question.Load().(agentcomm.MessageID)
			sendAs(t, o, "op-user-answer", userThread, meshUserParticipantID, rev.ParticipantID, "HEAD~1", &q, true)

			waitUntil(t, "the reply to the stage's request", func() bool { return len(repliesTo(store, request)) == 1 })
			o.concurrency.WaitAgents()
			reply := repliesTo(store, request)[0]
			want := "could not produce a valid result"
			if second == "pass" {
				want = "looks fine"
			}
			if reply.Sender != rev.ParticipantID || !reply.AutoResult || !strings.Contains(reply.Body, want) {
				t.Fatalf("reply = %+v, want the reviewer's passive reply containing %q", reply, want)
			}
			if got := store.Snapshot().Assistants.Mesh.Waits[stageWait.WaitID].Status; got != agentcomm.WaitReady {
				t.Fatalf("stage wait = %s, want ready", got)
			}
			if got := calls.Load(); got != 2 {
				t.Fatalf("reviewer turns = %d, want 2", got)
			}
		})
	}
}

// A turn's answer that cannot be delivered (here: the conversation is full) is not silently dropped while the
// requester keeps waiting: the requester's wait for it is cancelled in the same commit, with the reason.
func TestTurnReply_UndeliveredAnswerEndsTheRequestersWait(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	stage := meshStageParticipantID(assistantsTestStageID)
	o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
		return passResultOutcome(), nil
	}
	rev, err := o.AddRunAgent(ctx, AddRunAgentRequest{OperationID: "op-rev", ProfileID: "qa", Title: "QA", Actor: state.Actor{Kind: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	full := openThread(t, o, "op-full-thread", 1, stage, rev.ParticipantID)
	request := sendAs(t, o, "op-request", full, stage, rev.ParticipantID, "please review", nil, false)
	w, err := o.Wait(ctx, WaitRequest{OperationID: "op-stage-wait", Waiter: stage, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: request}})
	if err != nil {
		t.Fatalf("stage Wait: %v", err)
	}
	waitForRunAgentTurn(t, store, rev.TaskID, 2*time.Second)
	o.concurrency.WaitAgents()

	if got := repliesTo(store, request); len(got) != 0 {
		t.Fatalf("replies = %+v, want none: the conversation is full", got)
	}
	got := store.Snapshot().Assistants.Mesh.Waits[w.WaitID]
	if got.Status != agentcomm.WaitCancelled || !strings.Contains(got.Reason, "could not be delivered") {
		t.Fatalf("stage wait = %s %q, want cancelled because the answer could not be delivered", got.Status, got.Reason)
	}
	if !strings.Contains(noticesText(t, o), "not delivered") {
		t.Fatal("no notice that the turn result was not delivered")
	}
}

// A turn owes replies to two requesters, and one of their conversations is full. The reply that fits is still
// posted and answers its requester; only the wait whose reply cannot be delivered is cancelled.
func TestTurnReply_OneFullConversationDoesNotDropTheOtherReply(t *testing.T) {
	store, stage, peer := twoRequestersRun(t, false)
	if got := stage.wait(store); got.Status != agentcomm.WaitCancelled || !strings.Contains(got.Reason, "could not be delivered") {
		t.Fatalf("stage wait = %s %q, want cancelled: its conversation is full", got.Status, got.Reason)
	}
	if got := peer.wait(store).Status; got != agentcomm.WaitReady {
		t.Fatalf("peer wait = %s, want ready: its reply fits", got)
	}
	if len(repliesTo(store, stage.request)) != 0 || len(repliesTo(store, peer.request)) != 1 {
		t.Fatal("want exactly the peer's reply")
	}
}

// Both requests are in one conversation with room for one more message: each reply fits on its own, not both
// together. The first is delivered; only the second requester's wait is cancelled.
func TestTurnReply_TwoRepliesForTheLastSlotDeliverOne(t *testing.T) {
	store, stage, peer := twoRequestersRun(t, true)
	if got := stage.wait(store).Status; got != agentcomm.WaitReady {
		t.Fatalf("stage wait = %s, want ready: its reply took the last slot", got)
	}
	if got := peer.wait(store); got.Status != agentcomm.WaitCancelled || !strings.Contains(got.Reason, "could not be delivered") {
		t.Fatalf("peer wait = %s %q, want cancelled: no room was left for its reply", got.Status, got.Reason)
	}
	if len(repliesTo(store, stage.request)) != 1 || len(repliesTo(store, peer.request)) != 0 {
		t.Fatal("want exactly the stage's reply")
	}
}

// requester is one side of twoRequestersRun: the request it sent and its reply_to wait on it.
type requester struct {
	request agentcomm.MessageID
	waitID  agentcomm.WaitID
}

func (r requester) wait(store *state.Store) *state.MeshWait {
	return store.Snapshot().Assistants.Mesh.Waits[r.waitID]
}

// twoRequestersRun has a reviewer owe replies to two requesters in one turn: the stage asks, the reviewer asks
// the human and yields, the peer asks while it waits (another yield), and the human's passive answer wakes the
// turn that answers both. sharedThread: both requests are in one conversation with room for one more message;
// otherwise the stage's conversation is full and the peer's has room. Returns once both waits have ended.
func twoRequestersRun(t *testing.T, sharedThread bool) (*state.Store, requester, requester) {
	t.Helper()
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	stage := meshStageParticipantID(assistantsTestStageID)
	rev, err := o.AddRunAgent(ctx, AddRunAgentRequest{OperationID: "op-rev", ProfileID: "qa", Title: "QA", Actor: state.Actor{Kind: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	peer, err := o.AddRunAgent(ctx, AddRunAgentRequest{OperationID: "op-peer", ProfileID: "qa", Title: "Peer", Actor: state.Actor{Kind: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	stageThread := openThread(t, o, "op-full-thread", 1, stage, rev.ParticipantID)
	peerThread := openThread(t, o, "op-roomy-thread", 0, peer.ParticipantID, rev.ParticipantID)
	if sharedThread {
		stageThread = openThread(t, o, "op-shared-thread", 3, stage, peer.ParticipantID, rev.ParticipantID)
		peerThread = stageThread
	}
	userThread := openThread(t, o, "op-user-thread", 0, rev.ParticipantID, meshUserParticipantID)

	var revCalls atomic.Int32
	var question atomic.Value
	o.runHelperAgent = func(_ context.Context, taskID, _, _, _, _, _ string) (executor.HelperOutcome, error) {
		if assistants.TaskID(taskID) != rev.TaskID {
			return passResultOutcome(), nil
		}
		switch revCalls.Add(1) {
		case 1: // the stage's request: ask the human, then yield
			q := sendAs(t, o, "op-rev-question", userThread, rev.ParticipantID, meshUserParticipantID, "which base revision?", nil, false)
			question.Store(q)
			if _, err := o.Wait(context.Background(), WaitRequest{
				OperationID: "op-rev-wait", Waiter: rev.ParticipantID, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: q},
			}); err != nil {
				t.Errorf("reviewer Wait: %v", err)
			}
			return executor.HelperOutcome{ProcessOK: true}, nil
		case 2: // the peer's request arrives while it waits: still waiting, so yield again
			return executor.HelperOutcome{ProcessOK: true}, nil
		default:
			return passResultOutcome(), nil
		}
	}

	stageRequest := sendAs(t, o, "op-stage-request", stageThread, stage, rev.ParticipantID, "please review", nil, false)
	stageWait, err := o.Wait(ctx, WaitRequest{OperationID: "op-stage-wait", Waiter: stage, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: stageRequest}})
	if err != nil {
		t.Fatalf("stage Wait: %v", err)
	}
	waitForTaskStatus(t, store, rev.TaskID, assistants.StatusWaitingPeer, 2*time.Second)
	peerRequest := sendAs(t, o, "op-peer-request", peerThread, peer.ParticipantID, rev.ParticipantID, "and mine?", nil, false)
	peerWait, err := o.Wait(ctx, WaitRequest{OperationID: "op-peer-wait", Waiter: peer.ParticipantID, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: peerRequest}})
	if err != nil {
		t.Fatalf("peer Wait: %v", err)
	}
	waitUntil(t, "the second reviewer turn", func() bool {
		tk := store.Snapshot().Assistants.Tasks[rev.TaskID]
		return revCalls.Load() == 2 && tk.Status == assistants.StatusWaitingPeer && len(tk.OpenRequests) == 2
	})
	q := question.Load().(agentcomm.MessageID)
	sendAs(t, o, "op-user-answer", userThread, meshUserParticipantID, rev.ParticipantID, "HEAD~1", &q, true)

	waitUntil(t, "both waits to end", func() bool {
		waits := store.Snapshot().Assistants.Mesh.Waits
		return waits[stageWait.WaitID].Status != agentcomm.WaitPending && waits[peerWait.WaitID].Status != agentcomm.WaitPending
	})
	o.concurrency.WaitAgents()
	return store, requester{request: stageRequest, waitID: stageWait.WaitID}, requester{request: peerRequest, waitID: peerWait.WaitID}
}
