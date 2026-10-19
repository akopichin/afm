package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// A stage turn that yields on waits (`afm agent wait` → "yield") whose replies all arrive BEFORE its process
// exits: the live resume ran while the stage was still running and did nothing, so the exit itself must
// continue the stage — never send it to the completion check.

// yieldExitFixture is a running stage plus reviewers — idle run agents whose turn is held until release, then
// answers with a passing result (posted as a passive turn-result reply to the stage's message).
type yieldExitFixture struct {
	o         *Orchestrator
	store     *state.Store
	stage     agentcomm.ParticipantID
	thread    agentcomm.ThreadID
	reviewers []agentcomm.ParticipantID
	release   chan struct{}
}

func newYieldExitFixture(t *testing.T, reviewers int) *yieldExitFixture {
	t.Helper()
	o, store := newAssistantsTestOrchestrator(t, true)
	o.absRunDir = o.opts.RunDir
	if err := os.MkdirAll(filepath.Join(o.opts.RunDir, assistantsTestStageID), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(&state.Transition{StageID: assistantsTestStageID, From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	f := &yieldExitFixture{o: o, store: store, stage: meshStageParticipantID(assistantsTestStageID), release: make(chan struct{})}
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		awaitRelease(t, f.release)
		return passResultOutcome(), nil
	}
	members := []agentcomm.ParticipantID{f.stage}
	for i := range reviewers {
		res, err := o.AddRunAgent(context.Background(), AddRunAgentRequest{
			OperationID: fmt.Sprintf("op-reviewer-%d", i), ProfileID: "qa", Title: fmt.Sprintf("Reviewer %d", i), Actor: state.Actor{Kind: "user"},
		})
		if err != nil {
			t.Fatalf("AddRunAgent: %v", err)
		}
		f.reviewers = append(f.reviewers, res.ParticipantID)
		members = append(members, res.ParticipantID)
	}
	th, err := o.OpenThread(context.Background(), OpenThreadRequest{OperationID: "op-review-thread", Members: members, Scope: agentcomm.ScopeStage})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	f.thread = th.ID
	return f
}

// askAndYield is the stage's turn: one review request per reviewer, a reply_to wait on each (still pending —
// a yield), then the reviewers answer before the turn ends.
func (f *yieldExitFixture) askAndYield(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	var waits []agentcomm.WaitID
	for i, r := range f.reviewers {
		sent, err := f.o.Send(ctx, SendRequest{
			OperationID: fmt.Sprintf("op-ask-%d", i), ThreadID: f.thread, Sender: f.stage, Recipients: []agentcomm.ParticipantID{r},
			Kind: agentcomm.MessageKindMessage, Body: "please review", Actor: state.Actor{Kind: "assistant", ID: assistantsTestStageID},
		})
		if err != nil {
			t.Fatalf("Send: %v", err)
		}
		mid := sent.MessageID
		w, err := f.o.Wait(ctx, WaitRequest{
			OperationID: fmt.Sprintf("op-wait-%d", i), Waiter: f.stage,
			Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: mid},
		})
		if err != nil || w.Status != agentcomm.WaitPending {
			t.Fatalf("Wait = %+v, %v; want a pending wait (a yield)", w, err)
		}
		waits = append(waits, w.WaitID)
	}
	close(f.release)
	deadline := time.Now().Add(5 * time.Second)
	for _, id := range waits {
		for f.store.Snapshot().Assistants.Mesh.Waits[id].Status != agentcomm.WaitReady {
			if time.Now().After(deadline) {
				t.Fatalf("wait %s never became ready", id)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
}

// runYieldedTurn runs one stage turn through runWithRetry and reports whether the completion check ran and how
// many continuations started. Each EventPeerReplied published for the stage is applied the way onPeerReplied
// does — the awaiting_peer → running CAS — so a late live resume racing the exit's own one counts once.
func (f *yieldExitFixture) runYieldedTurn(t *testing.T, turn func()) (completionChecked bool, continuations int) {
	t.Helper()
	events := f.o.critical.Recv()
	f.o.runWithRetry(context.Background(), flow.Stage{ID: assistantsTestStageID}, phaseImplementation,
		func(string) error { turn(); return nil },
		func() error { completionChecked = true; return nil },
		func() { t.Error("onUserInterrupted must not be called") },
	)
	parked := f.o.currentStatus(assistantsTestStageID) == state.StatusAwaitingPeer
	if !completionChecked && !parked {
		t.Errorf("status = %v, want awaiting_peer (parked for the continuation)", f.o.currentStatus(assistantsTestStageID))
	}
	settle := time.After(200 * time.Millisecond) // let a late live resume publish too
	for {
		select {
		case ev := <-events:
			if ev.Type != bus.EventPeerReplied || ev.StageID != assistantsTestStageID {
				continue
			}
			if _, ok := f.o.Trigger(assistantsTestStageID, bus.EvPeerReplied, bus.GuardCtx{Phase: phaseImplementation}, ""); ok {
				continuations++
			}
		case <-settle:
			return completionChecked, continuations
		}
	}
}

func TestRunWithRetry_WaitsSatisfiedBeforeExitContinueTheStage(t *testing.T) {
	for _, reviewers := range []int{1, 2} {
		t.Run(fmt.Sprintf("%d reviewers", reviewers), func(t *testing.T) {
			f := newYieldExitFixture(t, reviewers)

			checked, continuations := f.runYieldedTurn(t, func() { f.askAndYield(t) })

			if checked {
				t.Error("the completion check ran: a turn whose yielded waits were answered must continue, not finish")
			}
			if continuations != 1 {
				t.Errorf("continuations = %d, want exactly 1", continuations)
			}
		})
	}
}

// A wait that is ready at registration is not a yield: `afm agent wait` said "ready", the agent read the reply
// in the same turn, and its exit completes normally.
func TestRunWithRetry_ReadyAtRegistrationIsNotAYield(t *testing.T) {
	f := newYieldExitFixture(t, 1)
	close(f.release)
	ctx := context.Background()

	checked, continuations := f.runYieldedTurn(t, func() {
		sent, err := f.o.Send(ctx, SendRequest{
			OperationID: "op-ask", ThreadID: f.thread, Sender: f.stage, Recipients: f.reviewers,
			Kind: agentcomm.MessageKindMessage, Body: "please review", Actor: state.Actor{Kind: "assistant", ID: assistantsTestStageID},
		})
		if err != nil {
			t.Fatalf("Send: %v", err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for !f.store.Snapshot().Assistants.Mesh.MessageAnswered(sent.MessageID) {
			if time.Now().After(deadline) {
				t.Fatal("the reviewer never answered")
			}
			time.Sleep(5 * time.Millisecond)
		}
		w, err := f.o.Wait(ctx, WaitRequest{
			OperationID: "op-wait", Waiter: f.stage,
			Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: sent.MessageID},
		})
		if err != nil || w.Status != agentcomm.WaitReady {
			t.Fatalf("Wait = %+v, %v; want ready at registration", w, err)
		}
	})

	if !checked || continuations != 0 {
		t.Fatalf("completion checked = %v, continuations = %d; want a normal completion", checked, continuations)
	}
}
