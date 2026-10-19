package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// A stage turn that ends for waits (`afm agent wait` → "yield") is parked in awaiting_peer, and the park records
// the waits it depends on (its episode). Whether the parked stage continues is one decision (peerContinuation)
// for the exit, the live reply paths and recovery. These tests cover what the exit must get right: answers that
// arrived while the process was exiting, a wait that ended meanwhile, and a stage someone else took over.

// askReviewers sends each reviewer its own request from the stage and registers a reply_to wait on each — a
// yield, as `afm agent wait` reports it.
func askReviewers(t *testing.T, o *Orchestrator, stage agentcomm.ParticipantID, thread agentcomm.ThreadID, reviewers []agentcomm.ParticipantID) []agentcomm.WaitID {
	t.Helper()
	ctx := context.Background()
	var waits []agentcomm.WaitID
	for i, r := range reviewers {
		sent, err := o.Send(ctx, SendRequest{
			OperationID: fmt.Sprintf("op-ask-%d", i), ThreadID: thread, Sender: stage, Recipients: []agentcomm.ParticipantID{r},
			Kind: agentcomm.MessageKindMessage, Body: "please review", Actor: state.Actor{Kind: "assistant", ID: string(stage)},
		})
		if err != nil {
			t.Fatalf("Send: %v", err)
		}
		w, err := o.Wait(ctx, WaitRequest{
			OperationID: fmt.Sprintf("op-wait-%d", i), Waiter: stage,
			Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: sent.MessageID},
		})
		if err != nil || w.Status != agentcomm.WaitPending {
			t.Fatalf("Wait = %+v, %v; want a pending wait (a yield)", w, err)
		}
		waits = append(waits, w.WaitID)
	}
	return waits
}

func waitUntilReady(t *testing.T, store *state.Store, ids ...agentcomm.WaitID) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for _, id := range ids {
		for store.Snapshot().Assistants.Mesh.Waits[id].Status != agentcomm.WaitReady {
			if time.Now().After(deadline) {
				t.Fatalf("wait %s never became ready", id)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
}

// TestYieldExit_AnswersBeforeExitContinueTheStage runs the real loop: the stage's first turn asks one or two
// reviewers (run agents), waits for their answers — passive turn results — and they all arrive before the turn
// ends. The stage must get exactly one continuation turn through the real resume and spawn path, then finish.
func TestYieldExit_AnswersBeforeExitContinueTheStage(t *testing.T) {
	for _, reviewers := range []int{1, 2} {
		t.Run(fmt.Sprintf("%d reviewers", reviewers), func(t *testing.T) {
			runner := &scriptedStageRunner{}
			o, store := newUnreadTestOrchestrator(t, flow.AgentAuto, runner)
			release := make(chan struct{})
			o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
				awaitRelease(t, release)
				return passResultOutcome(), nil
			}
			members := []agentcomm.ParticipantID{unreadTestStage}
			var revs []agentcomm.ParticipantID
			for i := range reviewers {
				res, err := o.AddRunAgent(context.Background(), AddRunAgentRequest{
					OperationID: fmt.Sprintf("op-reviewer-%d", i), ProfileID: "qa", Title: fmt.Sprintf("Reviewer %d", i), Actor: state.Actor{Kind: "user"},
				})
				if err != nil {
					t.Fatalf("AddRunAgent: %v", err)
				}
				revs = append(revs, res.ParticipantID)
				members = append(members, res.ParticipantID)
			}
			th, err := o.OpenThread(context.Background(), OpenThreadRequest{OperationID: "op-review-thread", Members: members, Scope: agentcomm.ScopeStage})
			if err != nil {
				t.Fatalf("OpenThread: %v", err)
			}
			runner.turn = func(n int32) error {
				if n == 1 {
					waits := askReviewers(t, o, unreadTestStage, th.ID, revs)
					close(release)
					waitUntilReady(t, store, waits...)
				}
				return nil
			}

			runToEnd(t, o)

			if got := runner.turns.Load(); got != 2 {
				t.Fatalf("stage turns = %d, want 2 (the asking turn and one continuation)", got)
			}
			if got := store.Get(unreadTestStageID); got != state.StatusDone {
				t.Fatalf("stage status = %s, want done", got)
			}
		})
	}
}

// exitFixture drives one stage turn through runWithRetry on the bare orchestrator of newAssistantsTestOrchestrator,
// recording what the exit did: the completion check, a Revise hand-off, and the continuation events it published
// (each applied the way the event loop applies them — the awaiting_peer → running transition — so a late live
// resume racing the exit's own one counts once).
type exitFixture struct {
	o      *Orchestrator
	store  *state.Store
	stage  agentcomm.ParticipantID
	thread agentcomm.ThreadID
}

type exitOutcome struct {
	completionChecked bool
	completions       int // EventAgentCompleted published
	interrupted       int
	continuations     map[bus.EventType]int
}

func newExitFixture(t *testing.T) *exitFixture {
	t.Helper()
	o, store := newAssistantsTestOrchestrator(t, true)
	o.absRunDir = o.opts.RunDir
	if err := os.MkdirAll(filepath.Join(o.opts.RunDir, assistantsTestStageID), 0o755); err != nil {
		t.Fatal(err)
	}
	activateStage(t, o, assistantsTestStageID)
	stage := meshStageParticipantID(assistantsTestStageID)
	th, err := o.OpenThread(context.Background(), OpenThreadRequest{
		OperationID: "op-exit-thread", Members: []agentcomm.ParticipantID{meshUserParticipantID, stage}, Scope: agentcomm.ScopeStage,
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	return &exitFixture{o: o, store: store, stage: stage, thread: th.ID}
}

func (f *exitFixture) runTurn(t *testing.T, turn func()) exitOutcome {
	t.Helper()
	out := exitOutcome{continuations: map[bus.EventType]int{}}
	events := f.o.critical.Recv()
	f.o.runWithRetry(context.Background(), flow.Stage{ID: assistantsTestStageID}, phaseImplementation,
		func(string) error { turn(); return nil },
		func() error { out.completionChecked = true; return nil },
		func() { out.interrupted++ },
	)
	transition := map[bus.EventType]bus.FSMEvent{
		bus.EventPeerReplied: bus.EvPeerReplied, bus.EventPeerWaitEnded: bus.EvPeerWaitEnded, bus.EventPeerQuestion: bus.EvPeerQuestion,
	}
	settle := time.After(200 * time.Millisecond) // let a late live resume publish too
	for {
		select {
		case ev := <-events:
			if ev.Type == bus.EventAgentCompleted {
				out.completions++
			}
			fsmEv, ok := transition[ev.Type]
			if !ok || ev.StageID != assistantsTestStageID {
				continue
			}
			if _, ok := f.o.Trigger(assistantsTestStageID, fsmEv, bus.GuardCtx{Phase: phaseImplementation}, ""); ok {
				out.continuations[ev.Type]++
			}
		case <-settle:
			return out
		}
	}
}

// askUser is a stage request to the human with a reply_to wait on it (a yield).
func (f *exitFixture) askUser(t *testing.T, opID string) (agentcomm.MessageID, agentcomm.WaitID) {
	t.Helper()
	ctx := context.Background()
	sent, err := f.o.Send(ctx, SendRequest{
		OperationID: opID, ThreadID: f.thread, Sender: f.stage, Recipients: []agentcomm.ParticipantID{meshUserParticipantID},
		Kind: agentcomm.MessageKindMessage, Body: "which format?", Actor: state.Actor{Kind: "assistant", ID: assistantsTestStageID},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	w, err := f.o.Wait(ctx, WaitRequest{
		OperationID: opID + "-wait", Waiter: f.stage, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: sent.MessageID},
	})
	if err != nil || w.Status != agentcomm.WaitPending {
		t.Fatalf("Wait = %+v, %v; want a pending wait", w, err)
	}
	return sent.MessageID, w.WaitID
}

// The only wait the turn yielded on was cancelled before the process exited: the stage continues as for a wait
// that ended without an answer — never through the completion check.
func TestYieldExit_WaitEndedBeforeExitContinuesAsWaitEnded(t *testing.T) {
	f := newExitFixture(t)

	out := f.runTurn(t, func() {
		_, wait := f.askUser(t, "op-ask")
		if err := f.o.CancelWait(context.Background(), CancelWaitRequest{
			OperationID: "op-cancel", WaitID: wait, Reason: "no longer needed", Actor: state.Actor{Kind: "user"},
		}); err != nil {
			t.Fatalf("CancelWait: %v", err)
		}
	})

	if out.completionChecked {
		t.Fatal("the completion check ran: a turn whose yielded wait ended must continue")
	}
	if out.continuations[bus.EventPeerWaitEnded] != 1 || len(out.continuations) != 1 {
		t.Fatalf("continuations = %v, want exactly one peer_wait_ended", out.continuations)
	}
}

// A wait that is ready at registration is not a yield: `afm agent wait` said "ready", the agent read the reply
// in the same turn, and its exit completes normally.
func TestYieldExit_ReadyAtRegistrationIsNotAYield(t *testing.T) {
	f := newExitFixture(t)
	ctx := context.Background()

	out := f.runTurn(t, func() {
		sent, err := f.o.Send(ctx, SendRequest{
			OperationID: "op-ask", ThreadID: f.thread, Sender: f.stage, Recipients: []agentcomm.ParticipantID{meshUserParticipantID},
			Kind: agentcomm.MessageKindMessage, Body: "which format?", Actor: state.Actor{Kind: "assistant", ID: assistantsTestStageID},
		})
		if err != nil {
			t.Fatalf("Send: %v", err)
		}
		mid := sent.MessageID
		if _, err := f.o.Send(ctx, SendRequest{
			OperationID: "op-answer", ThreadID: f.thread, Sender: meshUserParticipantID, Recipients: []agentcomm.ParticipantID{f.stage},
			Kind: agentcomm.MessageKindReply, InReplyTo: &mid, Body: "JSON",
		}); err != nil {
			t.Fatalf("answer: %v", err)
		}
		w, err := f.o.Wait(ctx, WaitRequest{
			OperationID: "op-wait", Waiter: f.stage, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: mid},
		})
		if err != nil || w.Status != agentcomm.WaitReady {
			t.Fatalf("Wait = %+v, %v; want ready at registration", w, err)
		}
		f.o.deliverRenderedInbox(f.stage)() // the agent read the answer in this turn
	})

	if !out.completionChecked || len(out.continuations) != 0 {
		t.Fatalf("outcome = %+v, want a normal completion", out)
	}
}

// Revise lands while the process is exiting after a yield: the exit must not park the revised stage (the peer
// continuation would restart the ordinary agent and lose the feedback run) — it hands over to the revise path.
func TestYieldExit_ReviseDuringExitRunsTheFeedbackTurn(t *testing.T) {
	f := newExitFixture(t)

	out := f.runTurn(t, func() {
		f.askUser(t, "op-ask")
		if applied, _, err := f.o.Revise(context.Background(), assistantsTestStageID, "use YAML instead"); err != nil || !applied {
			t.Fatalf("Revise = %v, %v; want applied", applied, err)
		}
	})

	if out.interrupted != 1 {
		t.Fatalf("revise hand-offs = %d, want 1 (the feedback run)", out.interrupted)
	}
	if got := f.o.currentStatus(assistantsTestStageID); got != state.StatusRevising {
		t.Fatalf("status = %s, want revising (not parked)", got)
	}
	if out.completionChecked || len(out.continuations) != 0 {
		t.Fatalf("outcome = %+v, want neither a completion nor a peer continuation", out)
	}
}

// Pause and Continue land while the process is exiting after a yield: Continue already started a new process,
// so the old exit is stale and must not park the stage under it (which would also give a stale extra turn).
func TestYieldExit_PauseContinueDuringExitIsStale(t *testing.T) {
	f := newExitFixture(t)

	out := f.runTurn(t, func() {
		f.askUser(t, "op-ask")
		// What Pause and Continue commit; Continue's own new process is not started by this bare harness.
		if _, ok := f.o.Trigger(assistantsTestStageID, bus.EvPause, bus.GuardCtx{}, "manual pause"); !ok {
			t.Fatal("EvPause failed")
		}
		if _, ok := f.o.Trigger(assistantsTestStageID, bus.EvContinue, bus.GuardCtx{PausedFrom: state.StatusRunning}, ""); !ok {
			t.Fatal("EvContinue failed")
		}
	})

	if got := f.o.currentStatus(assistantsTestStageID); got != state.StatusRunning {
		t.Fatalf("status = %s, want running (owned by the continued process)", got)
	}
	if out.completionChecked || out.interrupted != 0 || len(out.continuations) != 0 {
		t.Fatalf("outcome = %+v, want the stale exit to do nothing", out)
	}
}

// TestYieldExit_RecoveryAfterCrashContinuesOnTheRecordedEpisode: the turn yielded on two waits; one was
// answered by a passive turn result before the process exited, the other is still pending. The exit parked the
// stage and published its continuation, then the process crashed before handling it. After the restart, the
// recorded episode tells recovery that an awaited answer is there — a passive result wakes nobody, so without it
// the pending wait would leave the stage parked forever.
func TestYieldExit_RecoveryAfterCrashContinuesOnTheRecordedEpisode(t *testing.T) {
	runDir := t.TempDir()
	o, store := newUnreadTestOrchestratorAt(t, runDir, flow.AgentAuto, &scriptedStageRunner{turn: func(int32) error { return nil }})
	ctx := context.Background()
	activateStage(t, o, unreadTestStageID)
	th, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-crash-thread", Members: []agentcomm.ParticipantID{meshUserParticipantID, unreadTestStage}, Scope: agentcomm.ScopeStage,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Two requests to the human stand in for two reviewers; the human's passive reply to the first stands in for
	// a reviewer's posted turn result.
	waits := askReviewers(t, o, unreadTestStage, th.ID, []agentcomm.ParticipantID{meshUserParticipantID, meshUserParticipantID})
	first := store.Snapshot().Assistants.Mesh.Waits[waits[0]].Condition.MessageID
	if _, err := o.Send(ctx, SendRequest{
		OperationID: "op-passive", ThreadID: th.ID, Sender: meshUserParticipantID, Recipients: []agentcomm.ParticipantID{unreadTestStage},
		Kind: agentcomm.MessageKindReply, InReplyTo: &first, Body: "verdict: pass", AutoResult: true,
	}); err != nil {
		t.Fatal(err)
	}
	epoch := store.Snapshot().Stages[unreadTestStageID].Epoch
	o.parkForPeers(ctx, unreadTestStageID, phaseAutonomous, epoch, o.turnEpisode(unreadTestStage, nil), func() { t.Error("no revise here") })
	if got := store.Get(unreadTestStageID); got != state.StatusAwaitingPeer {
		t.Fatalf("status after the exit = %s, want awaiting_peer", got)
	}
	// Crash: the continuation the exit published is never handled.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	var turns atomic.Int32
	restarted, store2 := newUnreadTestOrchestratorAt(t, runDir, flow.AgentAuto, &scriptedStageRunner{turn: func(int32) error { turns.Add(1); return nil }})
	if got := store2.Snapshot().Stages[unreadTestStageID].PeerWaits; len(got) != 2 {
		t.Fatalf("replayed episode = %v, want both waits", got)
	}
	runCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(func() { cancel(); restarted.concurrency.WaitAgents() })

	restarted.recoverMeshWaits(runCtx)

	deadline := time.Now().Add(5 * time.Second)
	for turns.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("no continuation turn after recovery (status %s)", store2.Get(unreadTestStageID))
		}
		time.Sleep(5 * time.Millisecond)
	}
}
