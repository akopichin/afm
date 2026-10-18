// Package orchestrator_test (this file, Agent Mesh Phase 5, T5.6): an interrupted run keeps its pending mesh
// waits pending for the resumed run, exercised through the REAL Orchestrator.Run shutdown sequence (cancel ->
// WaitAgents -> closeMeshWaitsIfFinalized -> EndRun). Only a run that really finished closes them as run_closed.
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

// TestMesh_RunShutdown_KeepsRemainingWaitPending registers a mesh wait that is not satisfied yet —
// state.AssistantsState.RunBlocker (T6.3) holds the run open for exactly this reason, so this run ends only by a
// forced shutdown (ctx cancellation), the same path a real `afm run` takes on SIGINT/SIGTERM. The run is resumed
// later, so the wait must still be pending — not closed as if the run had ended.
func TestMesh_RunShutdown_KeepsRemainingWaitPending(t *testing.T) {
	rec := &flowEventRecorder{}
	dispatcher := newFlowRecorderDispatcher(t, rec)
	orch, store, stateFile := newRunBarrierTestOrch(t, dispatcher, "sleep 0.2")

	registered := make(chan agentcomm.WaitID, 1)
	orchestrator.SetRunHelperAgentForTest(orch, func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		// AddRunAgent's real mesh identity for this scope=run task is "run:<taskID>".
		waiter := orchestrator.RunAgentParticipantIDForTest("op-add-qa")
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
	res, err := orch.AddRunAgent(ctx, orchestrator.AddRunAgentRequest{OperationID: "op-add-qa",
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
	if w.Status != agentcomm.WaitPending {
		t.Fatalf("wait status after forced shutdown = %q, want pending (the run is resumed later)", w.Status)
	}
}
