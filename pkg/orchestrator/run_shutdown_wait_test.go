// Package orchestrator_test (this file, Agent Mesh Phase 5, T5.6): closing the run finishes remaining mesh
// waits with a visible run_closed outcome, exercised through the REAL Orchestrator.Run shutdown sequence
// (cancel -> WaitAgents -> closeRemainingMeshWaits -> EndRun), not just the unit-level
// closeRemainingMeshWaits call the internal package tests already cover directly.
package orchestrator_test

import (
	"context"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/state"
)

// TestMesh_RunShutdown_ClosesRemainingWaitWithRunClosed registers a mesh wait with no deadline that will never
// be satisfied (its colleague never replies) — state.AssistantsState.RunBlocker (T6.3) holds the run open for
// exactly this reason, so the only way this run ever ends is a forced shutdown (ctx cancellation), the same
// path a real `afm run` takes on SIGINT/SIGTERM. Once Run actually returns, the wait must not be left dangling
// in the journal forever: it must show a visible, stored run_closed outcome.
func TestMesh_RunShutdown_ClosesRemainingWaitWithRunClosed(t *testing.T) {
	rec := &flowEventRecorder{}
	dispatcher := newFlowRecorderDispatcher(t, rec)
	orch, store, stateFile := newRunBarrierTestOrch(t, dispatcher, "sleep 0.2")

	registered := make(chan agentcomm.WaitID, 1)
	orchestrator.SetRunHelperAgentForTest(orch, func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		waiter := agentcomm.ParticipantID("helper:" + taskID)
		receipt, err := orch.Wait(ctx, orchestrator.WaitRequest{
			OperationID:      "wait-" + taskID,
			Waiter:           waiter,
			Condition:        agentcomm.WaitCondition{Kind: agentcomm.WaitOnStageStatus, StageID: runBarrierStageID, Status: string(state.StatusFailed)},
			ContinuationNote: "nobody will ever satisfy this",
			Actor:            state.Actor{Kind: "assistant", ID: taskID},
		})
		if err != nil {
			t.Errorf("Wait: %v", err)
			return executor.HelperOutcome{}, err
		}
		registered <- receipt.WaitID
		return executor.HelperOutcome{ProcessOK: true}, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	runDone := make(chan struct{})
	go func() { _ = orch.Run(ctx); close(runDone) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-runDone:
		case <-time.After(10 * time.Second):
			t.Error("orch.Run did not return within 10s of cancellation")
		}
	})

	waitForStatus(t, stateFile, runBarrierStageID, state.StatusRunning, 10*time.Second)

	msg := "go"
	res, err := orch.AddRunAgent(ctx, orchestrator.AddRunAgentRequest{
		ProfileID: "qa", Title: "My QA", Instruction: "watch for regressions", FirstMessage: &msg, Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Fatalf("AddRunAgent: %v", err)
	}

	var waitID agentcomm.WaitID
	select {
	case waitID = <-registered:
	case <-time.After(5 * time.Second):
		t.Fatal("task never registered its mesh wait")
	}
	waitForTaskStatus(t, store, res.TaskID, assistants.StatusWaitingPeer, 5*time.Second)

	// Let the stage finish on its own; the task's wait is NEVER satisfied, so the run-level barrier (T6.3) holds
	// Run open — the only way out from here is a forced shutdown.
	waitForStatus(t, stateFile, runBarrierStageID, state.StatusDone, 10*time.Second)
	select {
	case <-runDone:
		t.Fatal("Run returned on its own despite an unsatisfiable registered mesh wait still pending — the barrier did not hold")
	case <-time.After(300 * time.Millisecond):
	}

	cancel() // forced shutdown: the same path a real SIGINT/SIGTERM takes
	select {
	case <-runDone:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after ctx cancellation")
	}

	rs := store.Snapshot()
	w, ok := rs.Assistants.Mesh.Waits[waitID]
	if !ok {
		t.Fatalf("wait %s missing from the final snapshot", waitID)
	}
	if w.Status != agentcomm.WaitRunClosed {
		t.Fatalf("wait status after forced shutdown = %q, want run_closed", w.Status)
	}
	if w.Reason == "" {
		t.Fatal("wait reason after forced shutdown is empty, want a stored, visible reason")
	}
}
