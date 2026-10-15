package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/concurrency"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
	"github.com/akopichin/afm/pkg/state"
)

const assistantsTestStageID = "impl"

// newAssistantsTestOrchestrator builds a minimal, real (non-mocked) Store + graph + concurrency Manager —
// enough to exercise Attach/the task lifecycle end-to-end, with a fake runHelperAgent standing in for a real
// subprocess (same seam pattern as newVerifyTestOrchestrator/o.runVerifyAgent).
func newAssistantsTestOrchestrator(t *testing.T, enabled bool) (*Orchestrator, *state.Store) {
	t.Helper()
	dir := t.TempDir()
	stage := flow.Stage{ID: assistantsTestStageID, Command: "claude"}
	store, err := state.Open(dir, []string{assistantsTestStageID})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	if _, err := store.EnsurePolicyRecorded(assistants.DepthPolicy{MaxDepth: 3, Source: assistants.DepthSourceDefault, Revision: "rev-1"}, state.Actor{Kind: "system"}); err != nil {
		t.Fatalf("EnsurePolicyRecorded: %v", err)
	}

	reg, err := assistants.BuildRegistry(nil, []assistants.Profile{{ID: "reviewer", Title: "Reviewer", Command: "helper-cmd"}}, nil, "claude",
		func(cmd string) (bool, string, assistants.ReadonlyEnforcement) {
			return true, "", assistants.ReadonlyEnforcementOS
		})
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}

	stages := []flow.Stage{stage}
	o := &Orchestrator{
		opts: Options{
			RunDir: dir, Stages: stages, Store: store, RunID: "run1",
			AssistantsEnabled: enabled, AssistantsRegistry: reg,
		},
		graph:         graph.NewGraph(stages),
		ui:            bus.NewUIBus(),
		critical:      bus.NewCriticalBus(16),
		fsm:           bus.NewFSM(store),
		concurrency:   concurrency.New(bus.NewCriticalBus(16), stages, "claude", 0, nil),
		fingerprintFn: func(string) (string, bool) { return "", false },
	}
	return o, store
}

func activateStage(t *testing.T, o *Orchestrator, stageID string) {
	t.Helper()
	if _, ok := o.Trigger(stageID, bus.EvReady, bus.GuardCtx{}, ""); !ok {
		t.Fatalf("EvReady failed for %s", stageID)
	}
	if _, ok := o.Trigger(stageID, bus.EvStartRun, bus.GuardCtx{}, ""); !ok {
		t.Fatalf("EvStartRun failed for %s", stageID)
	}
}

func attachTestTask(ctx context.Context, t *testing.T, o *Orchestrator, opID string) assistants.TaskID {
	t.Helper()
	taskID, err := o.Attach(ctx, AttachRequest{
		OperationID: opID, StageID: assistantsTestStageID, ProfileID: "reviewer",
		Instruction: "look at this", Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	return taskID
}

func waitForTaskStatus(t *testing.T, store *state.Store, taskID assistants.TaskID, want assistants.TaskStatus, timeout time.Duration) *state.TaskState {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		rs := store.Snapshot()
		if tk, ok := rs.Assistants.Tasks[taskID]; ok && tk.Status == want {
			return tk
		}
		time.Sleep(5 * time.Millisecond)
	}
	rs := store.Snapshot()
	tk, ok := rs.Assistants.Tasks[taskID]
	if !ok {
		t.Fatalf("task %s not found waiting for status %s", taskID, want)
		return nil
	}
	t.Fatalf("task %s status = %s, want %s (reason=%q)", taskID, tk.Status, want, tk.StatusReason)
	return nil
}

func passResultOutcome() executor.HelperOutcome {
	return executor.HelperOutcome{ProcessOK: true, RawText: `{"schema_version":1,"kind":"result","verdict":"pass","summary":"looks fine"}`}
}

// ---- Attach / admission ----

func TestAttach_ToPendingStageStaysWaitingParent(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	taskID := attachTestTask(context.Background(), t, o, "op-1")

	rs := store.Snapshot()
	tk, ok := rs.Assistants.Tasks[taskID]
	if !ok {
		t.Fatal("task not found in projection")
	}
	if tk.Status != assistants.StatusWaitingParent {
		t.Fatalf("status = %s, want waiting_parent (stage is still pending)", tk.Status)
	}
}

func TestAttach_RejectedWhenDisabled(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, false)
	_, err := o.Attach(context.Background(), AttachRequest{
		OperationID: "op-1", StageID: assistantsTestStageID, ProfileID: "reviewer", Actor: state.Actor{Kind: "user"},
	})
	var rej *state.OperationRejectedError
	if !errors.As(err, &rej) || rej.Code != assistants.CodeFeatureDisabled {
		t.Fatalf("err = %v (%T), want *OperationRejectedError{Code: feature_disabled}", err, err)
	}
}

func TestAttach_RejectedForScriptStage(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	scriptStage := flow.Stage{ID: "scr", Script: "echo hi"}
	o.opts.Stages = append(o.opts.Stages, scriptStage)
	o.graph = graph.NewGraph(o.opts.Stages)

	_, err := o.Attach(context.Background(), AttachRequest{
		OperationID: "op-1", StageID: "scr", ProfileID: "reviewer", Actor: state.Actor{Kind: "user"},
	})
	if err == nil {
		t.Fatal("Attach to a script stage: want rejection, got nil error")
	}
}

// ---- Lifecycle ----

func TestLifecycle_HappyPath(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		return passResultOutcome(), nil
	}

	taskID := attachTestTask(context.Background(), t, o, "op-1")
	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 2*time.Second)

	raw, err := os.ReadFile(filepath.Join(o.taskDir(taskID), "result.json"))
	if err != nil {
		t.Fatalf("read durable result.json: %v", err)
	}
	var result assistants.Result
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("parse result.json: %v", err)
	}
	if result.Verdict != assistants.VerdictPass {
		t.Fatalf("verdict = %q, want pass", result.Verdict)
	}
}

func TestLifecycle_RunnerFailure(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		return executor.HelperOutcome{}, errors.New("boom: subprocess exec failed")
	}

	taskID := attachTestTask(context.Background(), t, o, "op-1")
	tk := waitForTaskStatus(t, store, taskID, assistants.StatusFailed, 2*time.Second)
	if !strings.Contains(tk.StatusReason, "boom") {
		t.Fatalf("reason = %q, want it to mention the runner error", tk.StatusReason)
	}
	if _, err := os.Stat(filepath.Join(o.taskDir(taskID), "result.json")); err == nil {
		t.Fatal("result.json must not exist after a runner failure — partial output must never be presented as a result")
	}
}

func TestLifecycle_MalformedResult(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		return executor.HelperOutcome{ProcessOK: true, RawText: `this is not json at all`}, nil
	}

	taskID := attachTestTask(context.Background(), t, o, "op-1")
	tk := waitForTaskStatus(t, store, taskID, assistants.StatusFailed, 2*time.Second)
	if !strings.Contains(tk.StatusReason, "protocol_error") {
		t.Fatalf("reason = %q, want it to report protocol_error (never pass)", tk.StatusReason)
	}
	if _, err := os.Stat(filepath.Join(o.taskDir(taskID), "result.json")); err == nil {
		t.Fatal("result.json must not exist for a malformed answer")
	}
}

func TestLifecycle_Timeout(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	// The actual OS-level kill-by-process-group is exercised by pkg/executor's own idle-timeout tests
	// (RunHelperAgent shares executor.run, which already uses killProcessGroup) — here we only verify the
	// orchestrator classifies a HelperOutcome{TimedOut:true} as a failed task, never a pass.
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		return executor.HelperOutcome{TimedOut: true}, nil
	}

	taskID := attachTestTask(context.Background(), t, o, "op-1")
	tk := waitForTaskStatus(t, store, taskID, assistants.StatusFailed, 2*time.Second)
	if !strings.Contains(tk.StatusReason, "timed out") {
		t.Fatalf("reason = %q, want it to mention the timeout", tk.StatusReason)
	}
}

func TestPause_StopsTurnAndBlocksNewTriggers(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)

	// Occupy the helper's command slot BEFORE Attach so its SpawnOwned goroutine queues behind the
	// semaphore instead of running immediately — giving PauseTask a window to hold the task before its
	// subprocess ever starts.
	blockSem := concurrency.ChannelSemaphore(make(chan struct{}, 1))
	blockSem <- struct{}{}
	o.concurrency = concurrency.NewWithSemaphores(bus.NewCriticalBus(16), map[string]concurrency.Semaphore{"helper-cmd": blockSem}, "claude")

	var ranAgent atomic.Bool
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		ranAgent.Store(true)
		return passResultOutcome(), nil
	}

	taskID := attachTestTask(context.Background(), t, o, "op-1")

	if err := o.PauseTask(context.Background(), taskID); err != nil {
		t.Fatalf("PauseTask: %v", err)
	}
	<-blockSem // free the slot the queued goroutine is waiting on

	// Held while queued behind the semaphore: the goroutine's own re-check puts it visibly back to
	// "queued" instead of ever invoking the helper agent.
	waitForTaskStatus(t, store, taskID, assistants.StatusQueued, 2*time.Second)
	if ranAgent.Load() {
		t.Fatal("helper agent must not have run — the task was paused while queued behind the command semaphore")
	}

	// A new scheduling attempt while still held must not restart it either.
	o.maybeScheduleTask(context.Background(), taskID)
	time.Sleep(30 * time.Millisecond)
	if ranAgent.Load() {
		t.Fatal("helper agent must not run while the task is still held")
	}
}

func TestCancel_KeepsHistory(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		return passResultOutcome(), nil
	}

	taskID := attachTestTask(context.Background(), t, o, "op-1")
	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 2*time.Second)

	if err := o.CancelTask(context.Background(), taskID); err != nil {
		t.Fatalf("CancelTask on an already-terminal task: %v", err)
	}
	rs := store.Snapshot()
	tk := rs.Assistants.Tasks[taskID]
	if tk.Status != assistants.StatusCompleted {
		t.Fatalf("status = %s, want completed — Cancel on a terminal task must be a no-op", tk.Status)
	}
	if _, err := os.Stat(filepath.Join(o.taskDir(taskID), "result.json")); err != nil {
		t.Fatal("result.json should still be present after Cancel on a completed task")
	}
}

func TestParentDone_ClosesNewTriggers(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)

	release := make(chan struct{})
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		<-release
		return passResultOutcome(), nil
	}

	taskID := attachTestTask(context.Background(), t, o, "op-1")
	waitForTaskStatus(t, store, taskID, assistants.StatusRunning, 2*time.Second)

	if _, ok := o.Trigger(assistantsTestStageID, bus.EvComplete, bus.GuardCtx{}, ""); !ok {
		t.Fatal("EvComplete failed for the parent stage")
	}
	if o.currentStatus(assistantsTestStageID) != state.StatusDone {
		t.Fatal("parent stage should be done")
	}

	close(release)
	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 2*time.Second)

	if o.currentStatus(assistantsTestStageID) != state.StatusDone {
		t.Fatal("a late advisory task completion must not reopen the already-finished parent stage")
	}
}

func TestHttpDisconnectDoesNotKillTask(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)

	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	o.runMu.Lock()
	o.runCtx = runCtx
	o.runMu.Unlock()

	started := make(chan struct{})
	var sawCancel atomic.Bool
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		close(started)
		select {
		case <-ctx.Done():
			sawCancel.Store(true)
		case <-time.After(150 * time.Millisecond):
		}
		return passResultOutcome(), nil
	}

	requestCtx, cancelRequest := context.WithCancel(context.Background())
	taskID := attachTestTask(requestCtx, t, o, "op-1")
	cancelRequest() // simulate the HTTP handler returning and net/http cancelling its request context

	<-started
	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 2*time.Second)
	if sawCancel.Load() {
		t.Fatal("a cancelled HTTP request ctx must not cancel the task — it must be spawned under the run-scoped ctx")
	}
}

func TestShutdown_NoWritesAfterStoreClose(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)

	runCtx, cancelRun := context.WithCancel(context.Background())
	o.runMu.Lock()
	o.runCtx = runCtx
	o.runMu.Unlock()

	started := make(chan struct{})
	proceed := make(chan struct{})
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		close(started)
		select {
		case <-ctx.Done():
		case <-proceed:
		}
		return passResultOutcome(), nil
	}

	taskID := attachTestTask(context.Background(), t, o, "op-1")
	// Wait for the subprocess to ACTUALLY start (not just the optimistic queued→running status write, which
	// commits before concurrency.SpawnOwned's goroutine is even guaranteed to have been scheduled) — cancelling
	// too early would abandon the task mid-AcquireLease, the same (accepted, pre-existing) behavior stages
	// already have when the run ctx dies before their goroutine gets CPU time.
	<-started

	cancelRun() // mirrors Run()'s shutdown: cancel the run-scoped ctx first
	close(proceed)
	o.concurrency.WaitAgents() // must not return until the task's own terminal write has already happened

	rs := store.Snapshot()
	tk := rs.Assistants.Tasks[taskID]
	if tk == nil || !tk.Status.Terminal() {
		t.Fatalf("task must have reached a terminal status before WaitAgents returned, got %+v", tk)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
