// Package orchestrator_test (this file, Agent Mesh Phase 6, T6.3): the run-level completion barrier —
// state.AssistantsState.RunBlocker (pkg/state/assistants_barrier.go), consulted from Orchestrator.shouldExit
// (pkg/orchestrator/scheduling.go). Unlike RequiredBlocker's per-stage required-gate walk, this one looks at
// the WHOLE run: a run-scoped standing role (T6.1/T6.2, no RootStageID at all) or an advisory task whose root
// stage already finished are both invisible to any one stage's own FSM status, so nothing would otherwise stop
// shouldExit from firing while one of them still has live work. These tests drive the REAL Orchestrator.Run
// loop (not the white-box newAssistantsTestOrchestrator harness in assistants_test.go, which never calls Run
// at all) so the barrier is exercised exactly where it lives.
package orchestrator_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/lifecyclehooks"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/state"
)

const runBarrierStageID = "work"

// newRunBarrierTestOrch builds a single-script-stage orchestrator with the collaboration capability enabled
// and one assistant profile ("qa") registered — just enough to drive AddRunAgent/Wait through a real
// Orchestrator.Run loop. store.Close is registered BEFORE the caller starts Run: a LIFO t.Cleanup — cancel+
// wait registered AFTER this one, either via runOrchestratorAsync or inline — always stops agents before the
// journal closes (pkg/orchestrator/AGENTS.md's "State persistence & run lifecycle").
func newRunBarrierTestOrch(t *testing.T, dispatcher *lifecyclehooks.Dispatcher, script string) (*orchestrator.Orchestrator, *state.Store, string) {
	t.Helper()
	runDir := t.TempDir()
	rootDir := t.TempDir()
	stages := []flow.Stage{{ID: runBarrierStageID, Name: "Work", Script: script}}

	store, err := state.Open(runDir, []string{runBarrierStageID})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	if _, err := store.EnsurePolicyRecorded(assistants.DepthPolicy{MaxDepth: 3, Source: assistants.DepthSourceDefault, Revision: "rev-1"}, state.Actor{Kind: "system"}); err != nil {
		t.Fatalf("EnsurePolicyRecorded: %v", err)
	}

	reg, err := assistants.BuildRegistry(nil, []assistants.Profile{
		{ID: "qa", Title: "QA", Command: "helper-cmd", Capabilities: assistants.Capabilities{Delegate: true}},
	}, nil, "claude",
		func(cmd string) (bool, string, assistants.ReadonlyEnforcement) {
			return true, "", assistants.ReadonlyEnforcementOS
		})
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}

	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir, RootDir: rootDir, Stages: stages, Store: store,
		Config: config.Default(), Prompts: orchestrator.DefaultPrompts(),
		Clock:              newFakeClockWithPump(t),
		Hooks:              dispatcher,
		AssistantsEnabled:  true,
		AssistantsRegistry: reg,
	})
	return orch, store, filepath.Join(runDir, "state.json")
}

// waitForTaskStatus polls store until taskID reaches want or timeout — the orchestrator_test-package
// equivalent of assistants_test.go's unexported helper of the same name (not shareable across the internal/
// external test package split, see clockpump_test.go's own doc comment for the same reason).
func waitForTaskStatus(t *testing.T, store *state.Store, taskID assistants.TaskID, want assistants.TaskStatus, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if tk := store.Snapshot().Assistants.Tasks[taskID]; tk != nil && tk.Status == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	got := "<missing>"
	if tk := store.Snapshot().Assistants.Tasks[taskID]; tk != nil {
		got = string(tk.Status)
	}
	t.Fatalf("task %s did not reach status %s within %s (last seen: %s)", taskID, want, timeout, got)
}

// passResultOutcome mirrors assistants_test.go's helper of the same name (package orchestrator) — same
// not-shareable-across-the-split reason as waitForTaskStatus above.
func passResultOutcome() executor.HelperOutcome {
	return executor.HelperOutcome{ProcessOK: true, RawText: `{"schema_version":1,"kind":"result","verdict":"pass","summary":"looks fine"}`}
}

// TestRunBarrier_IdleRoleDoesNotHoldRun: a scope=run standing role attached with NO first message (T6.2 —
// "addressable, starts no work") stays queued forever on its own, and its thread (opened in the SAME
// AttachAndSend operation) has the human and the role as members but zero messages — an "open but empty
// thread". Neither should prevent the run from finishing once its one real stage is done.
func TestRunBarrier_IdleRoleDoesNotHoldRun(t *testing.T) {
	rec := &flowEventRecorder{}
	dispatcher := newFlowRecorderDispatcher(t, rec)
	// A brief sleep (not an instant "true") gives this test a safe window to attach the role WHILE the stage is
	// still running — attaching only after the stage is already Done would race the run's own shouldExit (with
	// no task yet in the projection, nothing stops it from finalizing before AddRunAgent even lands); attaching
	// before the stage reaches Running at all would race Run's own startup recovery pass (recoverAssistants
	// unconditionally reschedules any task it finds already queued — crash-recovery semantics, see
	// assistants_delegate.go — which would wrongly "start" this intentionally idle task).
	orch, store, stateFile := newRunBarrierTestOrch(t, dispatcher, "sleep 0.2")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	runOrchestratorAsync(ctx, t, orch, cancel)

	waitForStatus(t, stateFile, runBarrierStageID, state.StatusRunning, 10*time.Second)

	res, err := orch.AddRunAgent(ctx, orchestrator.AddRunAgentRequest{OperationID: "op-add-qa",
		ProfileID: "qa", Title: "My QA", Instruction: "watch for regressions", Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Fatalf("AddRunAgent: %v", err)
	}

	waitForStatus(t, stateFile, runBarrierStageID, state.StatusDone, 10*time.Second)

	// The load-bearing assertion: the run actually reaches EventFlowFinished despite the idle role still
	// sitting there — if RunBlocker wrongly counted a queued, never-started task as a blocker, this would hang
	// until the 5s timeout below (and runOrchestratorAsync's own cleanup would additionally fail at 10s).
	waitForFlowEvent(t, rec, lifecyclehooks.EventFlowFinished, 5*time.Second)

	rs := store.Snapshot()
	tk := rs.Assistants.Tasks[res.TaskID]
	if tk == nil || tk.Status != assistants.StatusQueued {
		t.Fatalf("idle run-scoped role status = %+v, want untouched queued", tk)
	}
	th, ok := rs.Assistants.Mesh.Threads[res.ThreadID]
	if !ok {
		t.Fatal("AddRunAgent's own thread not found")
	}
	if th.Status != agentcomm.ThreadOpen || len(th.Members) == 0 {
		t.Fatalf("thread = %+v, want open with a non-empty member set", th)
	}
	if len(rs.Assistants.Mesh.Messages) != 0 {
		t.Fatalf("no first message was sent — thread must carry zero messages, got %d", len(rs.Assistants.Mesh.Messages))
	}
	if _, blocked := rs.Assistants.RunBlocker(); blocked {
		t.Fatal("RunBlocker reports blocked for an idle run-scoped role + empty thread")
	}
}

// TestRunBarrier_AcceptedWorkHoldsRun_ShutdownBounded: once the stage is done, a run-scoped task that has
// actually been accepted (StatusRunning, from a FirstMessage) must hold the run open until it finishes
// naturally — shutdown still stays bounded, and the task reaches its real terminal status (completed), never a
// forced failure from being cut off early.
func TestRunBarrier_AcceptedWorkHoldsRun_ShutdownBounded(t *testing.T) {
	rec := &flowEventRecorder{}
	dispatcher := newFlowRecorderDispatcher(t, rec)
	// A brief sleep (not an instant "true") gives this test a safe window to attach the role WHILE the stage is
	// still running — see TestRunBarrier_IdleRoleDoesNotHoldRun's identical comment for why both "before the
	// stage reaches Running" and "after the stage is already Done" are each their own race.
	orch, store, stateFile := newRunBarrierTestOrch(t, dispatcher, "sleep 0.2")

	started := make(chan string, 1)
	release := make(chan struct{})
	orchestrator.SetRunHelperAgentForTest(orch, func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		started <- taskID
		<-release
		return passResultOutcome(), nil
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

	select {
	case got := <-started:
		if got != string(res.TaskID) {
			t.Fatalf("started task id = %s, want %s", got, res.TaskID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run-scoped task never started")
	}

	// Let the stage finish on its own (the sleep elapses) while the mesh task is still blocked on release.
	waitForStatus(t, stateFile, runBarrierStageID, state.StatusDone, 10*time.Second)

	// The stage is now terminal; the run-scoped task is still accepted-but-unfinished work. Without the
	// run-level barrier, shouldExit would already have fired on the stage's own done transition — prove it did
	// NOT: Run must not have returned yet.
	select {
	case <-runDone:
		t.Fatal("Run returned while a run-scoped mesh task still had accepted, unfinished work — the barrier did not hold")
	case <-time.After(300 * time.Millisecond):
	}

	close(release) // let the helper turn finish naturally, as if nothing had tried to shut the run down
	select {
	case <-runDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the blocking mesh task finished")
	}

	waitForFlowEvent(t, rec, lifecyclehooks.EventFlowFinished, 2*time.Second)
	tk := store.Snapshot().Assistants.Tasks[res.TaskID]
	// A run agent's finished turn leaves it idle (queued) with its result — not force-failed by a premature shutdown.
	if tk == nil || tk.Status != assistants.StatusQueued || tk.LastVerdict == "" {
		t.Fatalf("task status = %+v, want an idle run agent with its turn's result", tk)
	}
}

// TestRunBarrier_RegisteredWaitHoldsRun_WakesOnCancel: a task that registers a mesh Wait with a continuation
// and cooperatively yields (empty RawText, runHelperTask's hasOpenMeshWaitFor branch) moves to waiting_peer and
// its OWN GOROUTINE EXITS right away, releasing its concurrency slot (T5.4) — concurrency.Manager's agentWG no
// longer tracks it at all from that point on. Only the run-level barrier's own task-status check is left to
// keep the run open. This also regression-guards the WakeEventLoop call added to finishTaskFrom: without it,
// cancelling the task would durably commit "cancelled" but never wake Run's event loop to re-check shouldExit,
// hanging the run forever once every stage is terminal.
func TestRunBarrier_RegisteredWaitHoldsRun_WakesOnCancel(t *testing.T) {
	rec := &flowEventRecorder{}
	dispatcher := newFlowRecorderDispatcher(t, rec)
	// A brief sleep (not an instant "true") gives this test a safe window to attach the role WHILE the stage is
	// still running — see TestRunBarrier_IdleRoleDoesNotHoldRun's identical comment for why both "before the
	// stage reaches Running" and "after the stage is already Done" are each their own race. One second, not less:
	// the role must register its wait on the stage before the stage finishes, even on a loaded machine.
	orch, store, stateFile := newRunBarrierTestOrch(t, dispatcher, "sleep 1")

	registered := make(chan struct{})
	orchestrator.SetRunHelperAgentForTest(orch, func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		// AddRunAgent's real mesh identity for this scope=run task is "run:<taskID>".
		waiter := orchestrator.RunAgentParticipantIDForTest("op-add-qa")
		if _, err := orch.Wait(ctx, orchestrator.WaitRequest{
			OperationID:      "wait-" + taskID,
			Waiter:           waiter,
			Condition:        agentcomm.WaitCondition{Kind: agentcomm.WaitOnStageStatus, StageID: runBarrierStageID, Status: string(state.StatusFailed)},
			ContinuationNote: "resume if the stage ever fails (it never will in this test)",
			Actor:            state.Actor{Kind: "assistant", ID: taskID},
		}); err != nil {
			t.Errorf("Wait: %v", err)
		}
		close(registered)
		// Cooperative yield: ProcessOK with no RawText plus an open mesh wait is classified as waiting_peer,
		// not a protocol error — mirrors a real helper subprocess exiting after registering a wait.
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

	select {
	case <-registered:
	case <-time.After(5 * time.Second):
		t.Fatal("task never registered its mesh wait")
	}
	waitForTaskStatus(t, store, res.TaskID, assistants.StatusWaitingPeer, 5*time.Second)

	// Let the stage finish on its own (the sleep elapses) while the mesh task sits in waiting_peer.
	waitForStatus(t, stateFile, runBarrierStageID, state.StatusDone, 10*time.Second)

	// The task's own goroutine has already exited (waiting_peer released its concurrency slot) — only the
	// run-level barrier's own check is left to hold the run open here.
	select {
	case <-runDone:
		t.Fatal("Run returned while a registered mesh wait with a continuation was still pending")
	case <-time.After(300 * time.Millisecond):
	}

	if err := orch.CancelTask(ctx, res.TaskID); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}

	select {
	case <-runDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the blocking wait's task was cancelled (WakeEventLoop regression?)")
	}
	waitForFlowEvent(t, rec, lifecyclehooks.EventFlowFinished, 2*time.Second)
}
