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

// TestAttach_FreezesReadonlyEnforcementIntoSnapshot is a regression test for the ReadonlyEnforcement wiring
// gap: Attach must copy the registry's CURRENT availability-check verdict (ProfileView.ReadonlyEnforcement)
// onto the frozen ProfileSnapshot it durably records — otherwise a task's journal/dashboard history can never
// say which read-only guarantee (OS-enforced vs container-only) was actually in effect when THAT task ran.
// newAssistantsTestOrchestrator's registry is built with an availability check that reports
// assistants.ReadonlyEnforcementOS (see its `func(cmd string) (...)` literal above) — the snapshot must carry
// exactly that, not the zero value a plain Registry.Get would hand back.
func TestAttach_FreezesReadonlyEnforcementIntoSnapshot(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	taskID := attachTestTask(context.Background(), t, o, "op-1")

	rs := store.Snapshot()
	tk, ok := rs.Assistants.Tasks[taskID]
	if !ok {
		t.Fatal("task not found in projection")
	}
	if tk.ProfileSnapshot.ReadonlyEnforcement != assistants.ReadonlyEnforcementOS {
		t.Fatalf("ProfileSnapshot.ReadonlyEnforcement = %q, want %q (the registry's availability check at admission time)",
			tk.ProfileSnapshot.ReadonlyEnforcement, assistants.ReadonlyEnforcementOS)
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

	// The same decoded result must also be visible as a durable "task_result" event — not just on disk — so
	// the dashboard's GET /api/assistants/{id}/events can surface verdict/summary (the gap this fix closes).
	events, _, _, err := o.TaskEvents(taskID, 0, 100)
	if err != nil {
		t.Fatalf("TaskEvents: %v", err)
	}
	var resultEvent *state.TaskEvent
	var statusEvent *state.TaskEvent
	for i := range events {
		switch events[i].Kind {
		case "task_result":
			resultEvent = &events[i]
		case "task_status_changed":
			var p struct {
				Status string `json:"status"`
			}
			if json.Unmarshal(events[i].Payload, &p) == nil && p.Status == string(assistants.StatusCompleted) {
				statusEvent = &events[i]
			}
		default:
		}
	}
	if resultEvent == nil {
		t.Fatal("TaskEvents is missing a task_result event")
	}
	var eventResult assistants.Result
	if err := json.Unmarshal(resultEvent.Payload, &eventResult); err != nil {
		t.Fatalf("decode task_result event payload: %v", err)
	}
	if eventResult.Verdict != assistants.VerdictPass || eventResult.Summary != "looks fine" {
		t.Fatalf("task_result event = %+v, want verdict=pass summary=%q", eventResult, "looks fine")
	}

	// Atomicity: the result event and the terminal status-transition event must land in the SAME control
	// record (same Seq) — proving they were committed together via one ApplyRunOperation call, not two.
	if statusEvent == nil {
		t.Fatal("TaskEvents is missing the task_status_changed -> completed event")
	}
	if resultEvent.Seq != statusEvent.Seq {
		t.Fatalf("task_result Seq=%d, task_status_changed(completed) Seq=%d — want the SAME seq (one atomic operation)", resultEvent.Seq, statusEvent.Seq)
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

// taskEventKinds returns, in Seq order, every event Kind recorded for taskID — used by the regression tests
// below to assert the failure/cancel paths attach exactly one effect (status only), never a stray task_result.
func taskEventKinds(t *testing.T, o *Orchestrator, taskID assistants.TaskID) []string {
	t.Helper()
	events, _, _, err := o.TaskEvents(taskID, 0, 100)
	if err != nil {
		t.Fatalf("TaskEvents: %v", err)
	}
	kinds := make([]string, len(events))
	for i, e := range events {
		kinds[i] = e.Kind
	}
	return kinds
}

// TestLifecycle_FailurePathHasNoResultEvent is a regression test for the task_result wiring: a failed task
// (runner error) must never carry a task_result event — attaching a zero-value result on a path that never
// decoded one would be worse than the original gap (a fabricated/empty "result").
func TestLifecycle_FailurePathHasNoResultEvent(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		return executor.HelperOutcome{}, errors.New("boom: subprocess exec failed")
	}

	taskID := attachTestTask(context.Background(), t, o, "op-1")
	waitForTaskStatus(t, store, taskID, assistants.StatusFailed, 2*time.Second)

	for _, kind := range taskEventKinds(t, o, taskID) {
		if kind == "task_result" {
			t.Fatal("a failed task must not carry a task_result event")
		}
	}
}

// TestCancel_HasNoResultEvent mirrors TestLifecycle_FailurePathHasNoResultEvent for CancelTask: cancelling a
// running task finishes it with a reason string, never a decoded result — no task_result effect must appear.
func TestCancel_HasNoResultEvent(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)

	release := make(chan struct{})
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		<-release
		return passResultOutcome(), nil
	}

	taskID := attachTestTask(context.Background(), t, o, "op-1")
	waitForTaskStatus(t, store, taskID, assistants.StatusRunning, 2*time.Second)

	if err := o.CancelTask(context.Background(), taskID); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	waitForTaskStatus(t, store, taskID, assistants.StatusCancelled, 2*time.Second)
	close(release) // let the blocked fake runner return so its goroutine doesn't leak past the test

	for _, kind := range taskEventKinds(t, o, taskID) {
		if kind == "task_result" {
			t.Fatal("a cancelled task must not carry a task_result event")
		}
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

// ---- Read side: TaskTree / TaskEvents (cmd/afm's AssistantsReader wiring reads through these) ----

func TestTaskTree_ListsTasksRootedAtStage(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		return passResultOutcome(), nil
	}

	taskID := attachTestTask(context.Background(), t, o, "op-1")
	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 2*time.Second)

	tree, err := o.TaskTree(assistantsTestStageID)
	if err != nil {
		t.Fatalf("TaskTree: %v", err)
	}
	if len(tree) != 1 || tree[0].ID != taskID {
		t.Fatalf("TaskTree = %+v, want exactly the one attached task %s", tree, taskID)
	}
	if tree[0].Status != assistants.StatusCompleted {
		t.Fatalf("TaskTree status = %s, want completed", tree[0].Status)
	}

	if _, err := o.TaskTree("does-not-exist"); err == nil {
		t.Fatal("TaskTree for an unknown stage must error")
	}
}

func TestTaskEvents_PaginatesInSeqOrderAfterCursor(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		return passResultOutcome(), nil
	}

	taskID := attachTestTask(context.Background(), t, o, "op-1")
	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 2*time.Second)

	all, _, hasMore, err := o.TaskEvents(taskID, 0, 100)
	if err != nil {
		t.Fatalf("TaskEvents: %v", err)
	}
	if hasMore {
		t.Fatal("hasMore must be false when the page holds every event")
	}
	if len(all) < 2 {
		t.Fatalf("expected at least task_created + a terminal task_status_changed, got %d: %+v", len(all), all)
	}
	for i := 1; i < len(all); i++ {
		if all[i].Seq < all[i-1].Seq {
			t.Fatalf("events out of Seq order: %+v", all)
		}
	}

	// Pagination: cursor on the first event's Seq must exclude every event AT that Seq (Attach bundles
	// task_created + task_status_changed-to-"queued" into the SAME operation/Seq when the stage is already
	// active — see Attach's parentActive branch — so "strictly after cursor" can drop more than one event).
	wantAfterFirstSeq := 0
	for _, e := range all {
		if e.Seq > all[0].Seq {
			wantAfterFirstSeq++
		}
	}
	rest, next, _, err := o.TaskEvents(taskID, all[0].Seq, 100)
	if err != nil {
		t.Fatalf("TaskEvents with cursor: %v", err)
	}
	if len(rest) != wantAfterFirstSeq {
		t.Fatalf("page after cursor = %d events, want %d", len(rest), wantAfterFirstSeq)
	}
	if next != all[len(all)-1].Seq {
		t.Fatalf("next cursor = %d, want %d (last event's seq)", next, all[len(all)-1].Seq)
	}

	// limit smaller than the full history must set hasMore.
	firstPage, _, hasMoreLimited, err := o.TaskEvents(taskID, 0, 1)
	if err != nil {
		t.Fatalf("TaskEvents with limit=1: %v", err)
	}
	if len(firstPage) != 1 || !hasMoreLimited {
		t.Fatalf("limited page = %+v hasMore=%v, want exactly 1 event and hasMore=true", firstPage, hasMoreLimited)
	}

	if events, _, _, err := o.TaskEvents("unknown-task", 0, 100); err != nil || len(events) != 0 {
		t.Fatalf("TaskEvents for an unknown task = %+v, %v, want empty/no error", events, err)
	}
}

// ---- Review-round fixes: storage-fatal propagation + requeue lost-wakeup ----

// TestFinishTaskFrom_StorageErrorSetsFatal is a regression test for the task lifecycle silently discarding
// ApplyRunOperation's error (AGENTS.md invariant "storage-fatal ends the run", mirrored by triggerWithSeq for
// the stage FSM but, until this fix, NOT by finishTaskFrom/maybeScheduleTask/PauseTask/ContinueTask).
// store.CloseEventsLogForTest() closes the journal's underlying file descriptor directly (the SAME trick
// pkg/state's own TestAppendControl_StorageErrorIsTyped/TestStorageFatalEndsRun use in-package, exported here
// so this cross-package test can trigger a real OS-level write failure) — the next ApplyRunOperation call
// returns a *state.StorageError, which finishTaskFrom must now classify via handleTaskOperationErr and feed
// into o.setFatal, instead of swallowing it with `_, _ = ...`.
func TestFinishTaskFrom_StorageErrorSetsFatal(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)

	release := make(chan struct{})
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		<-release
		return passResultOutcome(), nil
	}

	taskID := attachTestTask(context.Background(), t, o, "op-1")
	waitForTaskStatus(t, store, taskID, assistants.StatusRunning, 2*time.Second)

	if got := o.loadFatal(); got != nil {
		t.Fatalf("loadFatal() before any storage failure = %v, want nil", got)
	}

	store.CloseEventsLogForTest() // simulate a real OS-level journal write failure

	o.finishTaskFrom(taskID, assistants.StatusRunning, assistants.StatusFailed, "simulated storage failure")

	fatal := o.loadFatal()
	var se *state.StorageError
	if !errors.As(fatal, &se) {
		t.Fatalf("loadFatal() = %v (%T), want it to wrap a *state.StorageError — finishTaskFrom must end the run on a real journal write failure, not silently discard it", fatal, fatal)
	}

	// Let the blocked fake runner return so its goroutine doesn't leak past the test — its own finishTaskFrom
	// call will hit the same closed journal and re-classify the same way (setFatal keeps the FIRST error).
	close(release)
	o.concurrency.WaitAgents()
}

// TestLostWakeup_ContinueDuringRequeueStillSchedules is a deterministic regression test for the lost-wakeup
// race described in the review finding: a task held (PauseTask) while its SpawnOwned goroutine is still
// queued behind a full command semaphore gets requeued ("running"->"queued") by spawnTask's run body once
// the semaphore frees up. If a concurrent ContinueTask clears the hold and calls maybeScheduleTask BEFORE
// that requeue commits, it reads the task's status as still "running" and silently no-ops (maybeScheduleTask's
// own CAS precondition only fires on "queued") — without spawnTask's post-requeue retry, the task would then
// sit in "queued" forever: the hold is already cleared and nothing else is left to reschedule it.
//
// o.taskRequeueBarrier (test-only hook, nil in production) is invoked by spawnTask's run body AFTER
// confirming the task is held but BEFORE finishTaskFrom's requeue commits. Running ContinueTask SYNCHRONOUSLY
// from inside the hook forces the exact interleaving the race depends on — at that point in spawnTask's run
// body the requeue has provably not committed yet, so the task's projected status is guaranteed "running",
// and ContinueTask's own reschedule attempt is guaranteed to see it and no-op. No sleep/timing assumption is
// needed to force the race itself; only the FINAL assertion uses a bounded poll (waitForTaskStatus), which
// fails loudly on a timeout rather than hanging if the lost-wakeup bug were to return.
func TestLostWakeup_ContinueDuringRequeueStillSchedules(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)

	// Saturate the helper's command semaphore BEFORE Attach so spawnTask's SpawnOwned goroutine queues behind
	// it instead of running immediately (same setup as TestPause_StopsTurnAndBlocksNewTriggers).
	blockSem := concurrency.ChannelSemaphore(make(chan struct{}, 1))
	blockSem <- struct{}{}
	o.concurrency = concurrency.NewWithSemaphores(bus.NewCriticalBus(16), map[string]concurrency.Semaphore{"helper-cmd": blockSem}, "claude")

	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		return passResultOutcome(), nil
	}

	taskID := attachTestTask(context.Background(), t, o, "op-1")

	if err := o.PauseTask(context.Background(), taskID); err != nil {
		t.Fatalf("PauseTask: %v", err)
	}

	barrierHit := make(chan struct{})
	o.taskRequeueBarrier = func(id assistants.TaskID) {
		close(barrierHit)
		// finishTaskFrom's requeue commit has NOT happened yet at this point (we are still inside spawnTask's
		// `if o.taskHeld` branch, before that call) — the task's projected status is therefore guaranteed to
		// still be "running", so ContinueTask's own maybeScheduleTask attempt is guaranteed to no-op here,
		// exactly reproducing the race's precondition deterministically.
		if err := o.ContinueTask(context.Background(), id); err != nil {
			t.Errorf("ContinueTask: %v", err)
		}
	}

	<-blockSem // free the slot the queued goroutine is waiting on; spawnTask's run body now executes

	select {
	case <-barrierHit:
	case <-time.After(2 * time.Second):
		t.Fatal("taskRequeueBarrier was never invoked — spawnTask did not observe the hold")
	}

	// Without the post-requeue retry (Fix B), the task would be stuck in "queued" forever: the hold is
	// already cleared and ContinueTask's own reschedule attempt already came and went (as a no-op) before the
	// requeue committed. waitForTaskStatus's bounded timeout turns that hang into a clean test failure.
	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 2*time.Second)
}
