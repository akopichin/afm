package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

// A park lives through what happens around it: a crash between the yield and the park, and a Pause/Continue of
// the parked stage. Both keep the waits the turn ended for and decide the continuation on them.

// yieldedStage is a running stage whose turn asked the human and yielded on the answer (`afm agent wait`), with
// the turn's completion markers already written — as when the process stops right before its exit parks it.
func yieldedStage(t *testing.T, o *Orchestrator) (agentcomm.ThreadID, agentcomm.MessageID) {
	t.Helper()
	activateStage(t, o, unreadTestStageID)
	th, err := o.OpenThread(context.Background(), OpenThreadRequest{
		OperationID: "op-yield-thread", Members: []agentcomm.ParticipantID{meshUserParticipantID, unreadTestStage}, Scope: agentcomm.ScopeStage,
	})
	if err != nil {
		t.Fatal(err)
	}
	waits := askReviewers(t, o, unreadTestStage, th.ID, []agentcomm.ParticipantID{meshUserParticipantID})
	stageDir := filepath.Join(o.opts.RunDir, unreadTestStageID)
	for _, name := range []string{".done", "execution_summary.md"} {
		if err := os.WriteFile(filepath.Join(stageDir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return th.ID, o.opts.Store.Snapshot().Assistants.Mesh.Waits[waits[0]].Condition.MessageID
}

func answerAsUser(t *testing.T, o *Orchestrator, thread agentcomm.ThreadID, question agentcomm.MessageID) {
	t.Helper()
	if _, err := o.Send(context.Background(), SendRequest{
		OperationID: "op-user-answer", ThreadID: thread, Sender: meshUserParticipantID, Recipients: []agentcomm.ParticipantID{unreadTestStage},
		Kind: agentcomm.MessageKindReply, InReplyTo: &question, Body: "JSON",
	}); err != nil {
		t.Fatal(err)
	}
}

// restartAndRecover reopens the run in a new process and runs what Run does at startup before its loop.
func restartAndRecover(t *testing.T, runDir string, runner *scriptedStageRunner) (*Orchestrator, *state.Store) {
	t.Helper()
	o, store := newUnreadTestOrchestratorAt(t, runDir, flow.AgentAuto, runner)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); o.concurrency.WaitAgents() })
	o.runMu.Lock()
	o.runCtx = ctx
	o.runMu.Unlock()
	o.startPlanningForPending(ctx)
	o.recoverMeshWaits(ctx)
	return o, store
}

// Ctrl+C after `afm agent wait` answered "yield" but before the exit parked the stage: recovery must treat the
// stage as parked on that wait — neither rerun the author (a second review request) nor let the surviving
// completion marker finish it.
func TestParkLifecycle_CrashAfterYieldKeepsWaiting(t *testing.T) {
	runDir := t.TempDir()
	o, store := newUnreadTestOrchestratorAt(t, runDir, flow.AgentAuto, &scriptedStageRunner{turn: func(int32) error { return nil }})
	yieldedStage(t, o)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	runner := &scriptedStageRunner{turn: func(int32) error { return nil }}
	_, store2 := restartAndRecover(t, runDir, runner)
	time.Sleep(100 * time.Millisecond)

	if got := store2.Get(unreadTestStageID); got != state.StatusAwaitingPeer {
		t.Fatalf("status after recovery = %s, want awaiting_peer (the turn yielded)", got)
	}
	if got := runner.turns.Load(); got != 0 {
		t.Fatalf("author turns after recovery = %d, want 0: the author must not be rerun while it waits", got)
	}
}

// The same crash, but the answer arrived before it: recovery parks the stage and continues it at once.
func TestParkLifecycle_CrashAfterYieldWithTheAnswerContinues(t *testing.T) {
	runDir := t.TempDir()
	o, store := newUnreadTestOrchestratorAt(t, runDir, flow.AgentAuto, &scriptedStageRunner{turn: func(int32) error { return nil }})
	thread, question := yieldedStage(t, o)
	answerAsUser(t, o, thread, question)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	runner := &scriptedStageRunner{turn: func(int32) error { return nil }}
	_, store2 := restartAndRecover(t, runDir, runner)

	deadline := time.Now().Add(5 * time.Second)
	for runner.turns.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("no continuation turn after recovery (status %s)", store2.Get(unreadTestStageID))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A parked stage can be paused. The park keeps its waits; an answer arriving while paused does not resume it;
// Continue decides the continuation on the same waits and resumes it through the real spawn path.
func TestParkLifecycle_PauseAndContinueAParkedStage(t *testing.T) {
	runner := &scriptedStageRunner{turn: func(int32) error { return nil }}
	o, store := newUnreadTestOrchestrator(t, flow.AgentAuto, runner)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); o.concurrency.WaitAgents() })
	thread, question := yieldedStage(t, o)
	epoch := store.Snapshot().Stages[unreadTestStageID].Epoch
	o.parkForPeers(ctx, unreadTestStageID, phaseAutonomous, epoch, o.turnEpisode(unreadTestStage, nil), func() { t.Error("no revise here") })
	waits := store.Snapshot().Stages[unreadTestStageID].PeerWaits

	if err := o.Pause(ctx, unreadTestStageID); err != nil {
		t.Fatal(err)
	}
	if got := store.Snapshot().Stages[unreadTestStageID]; got.Status != state.StatusPaused || len(got.PeerWaits) != len(waits) {
		t.Fatalf("after Pause: %+v, want paused with the park's waits", got)
	}
	answerAsUser(t, o, thread, question)
	if got := store.Get(unreadTestStageID); got != state.StatusPaused {
		t.Fatalf("an answer resumed a paused stage: status %s", got)
	}

	if err := o.Continue(ctx, unreadTestStageID); err != nil {
		t.Fatal(err)
	}
	// Drive the event loop by hand until the continuation's turn runs.
	deadline := time.Now().Add(5 * time.Second)
	for runner.turns.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("no continuation turn after Continue (status %s)", store.Get(unreadTestStageID))
		}
		select {
		case ev := <-o.critical.Recv():
			if err := o.handleEvent(ctx, ev); err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Millisecond):
		}
	}
}
