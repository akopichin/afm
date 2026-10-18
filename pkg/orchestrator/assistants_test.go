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
	"github.com/akopichin/afm/pkg/assistants/assistantstest"
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
// subprocess (same seam pattern as newVerifyTestOrchestrator/o.runVerifyAgent). extraStages (optional) are
// additional stages initialized in the SAME store/graph alongside assistantsTestStageID — needed by any test
// that must attach watchers rooted at more than one stage (e.g. TestLoop_ChildNotAutoSubscribedToAncestors):
// state.Open must know about a stage id up front, or that stage's own EvReady/EvStartRun has no record to
// apply against.
func newAssistantsTestOrchestrator(t *testing.T, enabled bool, extraStages ...flow.Stage) (*Orchestrator, *state.Store) {
	t.Helper()
	dir := t.TempDir()
	stages := append([]flow.Stage{{ID: assistantsTestStageID, Command: "claude"}}, extraStages...)
	stageIDs := make([]string, len(stages))
	for i, s := range stages {
		stageIDs[i] = s.ID
	}
	store, err := state.Open(dir, stageIDs)
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	if _, err := store.EnsurePolicyRecorded(assistants.DepthPolicy{MaxDepth: 3, Source: assistants.DepthSourceDefault, Revision: "rev-1"}, state.Actor{Kind: "system"}); err != nil {
		t.Fatalf("EnsurePolicyRecorded: %v", err)
	}

	reg, err := assistants.BuildRegistry(nil, []assistants.Profile{
		{ID: "reviewer", Title: "Reviewer", Command: "helper-cmd", Capabilities: assistants.Capabilities{Delegate: true}},
		{ID: "qa", Title: "QA", Command: "helper-cmd", Capabilities: assistants.Capabilities{Delegate: true}},
	}, nil, "claude",
		func(cmd string) (bool, string, assistants.ReadonlyEnforcement) {
			return true, "", assistants.ReadonlyEnforcementOS
		})
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}

	o := &Orchestrator{
		opts: Options{
			RunDir: dir, Stages: stages, Store: store, RunID: "run1",
			AssistantsEnabled: enabled, AssistantsRegistry: reg,
		},
		graph:         graph.NewStore(graph.NewRegistry(stages)),
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

// waitForRunAgentTurn waits until a run agent finished a turn: it is back to idle (queued — a standing role
// never completes) with that turn's result recorded.
func waitForRunAgentTurn(t *testing.T, store *state.Store, taskID assistants.TaskID, timeout time.Duration) *state.TaskState {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if tk, ok := store.Snapshot().Assistants.Tasks[taskID]; ok && tk.Status == assistants.StatusQueued && tk.LastVerdict != "" {
			return tk
		}
		time.Sleep(5 * time.Millisecond)
	}
	tk := store.Snapshot().Assistants.Tasks[taskID]
	t.Fatalf("run agent %s did not finish a turn (status %v)", taskID, tk)
	return nil
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
	o.graph = graph.NewStore(graph.NewRegistry(o.opts.Stages))

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

func TestLifecycle_FencedResultIsRepaired(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		return executor.HelperOutcome{ProcessOK: true, RawText: "```json\n" + assistantstest.FixtureValidResult + "\n```"}, nil
	}

	subID, ch := o.ui.Subscribe(16)
	defer o.ui.Unsubscribe(subID)

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
	if !result.Repaired {
		t.Fatal("result.Repaired = false, want true for a fenced answer")
	}

	var liveRepaired int
	drainUIBus(ch, func(ev bus.Event) {
		if ev.Type == bus.EventHelperResultRepaired {
			liveRepaired++
			if data, ok := ev.Data.(map[string]any); !ok || data["task_id"] != string(taskID) {
				t.Errorf("helper_result_repaired event missing task_id: %+v", ev.Data)
			}
		}
	})
	if liveRepaired != 1 {
		t.Fatalf("expected exactly one live helper_result_repaired event, got %d", liveRepaired)
	}

	notices := readVerifyNotices(t, o.opts.RunDir)
	repaired := noticesOfType(notices, bus.EventHelperResultRepaired)
	if len(repaired) != 1 {
		t.Fatalf("expected exactly one durable helper_result_repaired notice, got %d", len(repaired))
	}
	if repaired[0].Data["task_id"] != string(taskID) {
		t.Errorf("durable notice task_id = %v, want %v", repaired[0].Data["task_id"], taskID)
	}
}

func TestLifecycle_RepairedPassClosesRequiredGate(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		return executor.HelperOutcome{ProcessOK: true, RawText: "```json\n" + assistantstest.FixtureValidResult + "\n```"}, nil
	}

	taskID, err := o.Attach(context.Background(), AttachRequest{
		OperationID: "op-1", StageID: assistantsTestStageID, ProfileID: "reviewer", Mode: assistants.ModeRequired,
		Instruction: "block until resolved", Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Fatalf("Attach(required): %v", err)
	}
	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 2*time.Second)

	o.completeStage(context.Background(), assistantsTestStageID, state.StatusRunning, "main done")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && o.currentStatus(assistantsTestStageID) != state.StatusDone {
		time.Sleep(5 * time.Millisecond)
	}
	if got := o.currentStatus(assistantsTestStageID); got != state.StatusDone {
		t.Fatalf("status after a repaired pass = %v, want done — mechanical repair changes syntax only, the verdict is still the model's own", got)
	}
}

func TestLifecycle_TextlessTurnLeavesNoStaleRawAnswer(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)

	var turn atomic.Int32
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		if turn.Add(1) == 1 {
			if err := os.WriteFile(resultFile, []byte(assistantstest.FixtureValidResult), 0o644); err != nil {
				t.Fatalf("seed first turn's raw answer: %v", err)
			}
			return passResultOutcome(), nil
		}
		return executor.HelperOutcome{ProcessOK: true, ProtocolErr: errors.New("helper produced no final answer")}, nil
	}

	// A watcher (checkpoints) trigger is the real-world way the SAME task id re-enters runHelperTask for a
	// second turn: it starts life resting in "watching" with NO turn yet run (Attach's startsWatching path),
	// and fireWatcherTask (the same transition a real checkpoint firing or a forced final pass uses) is what
	// drives each turn, one at a time, under the identical task id.
	taskID := attachWatcherTask(t, o, "op-1", assistantsTestStageID, assistants.TriggerCheckpoints, assistants.ModeAdvisory)
	waitForTaskStatus(t, store, taskID, assistants.StatusWatching, 2*time.Second)

	resultRawPath := filepath.Join(o.taskDir(taskID), "result.json.raw")

	o.fireWatcherTask(context.Background(), taskID)
	waitForTaskStatus(t, store, taskID, assistants.StatusWatching, 2*time.Second)
	if _, err := os.Stat(resultRawPath); err != nil {
		t.Fatalf("first turn must have left result.json.raw behind: %v", err)
	}

	o.fireWatcherTask(context.Background(), taskID)
	waitForTaskStatus(t, store, taskID, assistants.StatusFailed, 2*time.Second)

	if _, err := os.Stat(resultRawPath); err == nil {
		t.Fatal("result.json.raw must not exist after a textless second turn — it must never show the previous turn's answer as this failure's own")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat result.json.raw: %v", err)
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

// TestPause_RunningTaskRequeuesForResumeInsteadOfFailing is the companion to
// TestPause_StopsTurnAndBlocksNewTriggers: that test pauses a task still queued behind the command semaphore
// (subprocess never started); this one pauses a task whose helper subprocess is ALREADY running. PauseTask
// cancels runHelperTask's runCtx directly (not via executor's InterruptCh), which pkg/executor/hardening.go's
// RunHelperAgent surfaces as a bare !ProcessOK outcome (no Interrupted/TimedOut flag — those are InterruptCh-
// only). Before the fix, runHelperTask's switch had no way to tell that apart from a genuine crash and always
// finished the task as "failed", discarding the pause/resume contract entirely.
func TestPause_RunningTaskRequeuesForResumeInsteadOfFailing(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)

	var calls atomic.Int32
	started := make(chan struct{}, 1)
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		if calls.Add(1) == 1 {
			started <- struct{}{}
			<-ctx.Done()
			// Mirrors the real executor's default branch for a plain ctx cancellation: no Interrupted, no
			// TimedOut, just a process that did not finish cleanly.
			return executor.HelperOutcome{}, nil
		}
		return passResultOutcome(), nil
	}

	taskID := attachTestTask(context.Background(), t, o, "op-1")
	<-started
	waitForTaskStatus(t, store, taskID, assistants.StatusRunning, 2*time.Second)

	if err := o.PauseTask(context.Background(), taskID); err != nil {
		t.Fatalf("PauseTask: %v", err)
	}

	// Must land back in "queued" (resumable), never "failed".
	waitForTaskStatus(t, store, taskID, assistants.StatusQueued, 2*time.Second)

	if err := o.ContinueTask(context.Background(), taskID); err != nil {
		t.Fatalf("ContinueTask: %v", err)
	}
	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 2*time.Second)
}

// TestPause_ImmediateContinueBeforeClassificationStillRequeues is a regression test for a race codex's verify
// gate found: TestPause_RunningTaskRequeuesForResumeInsteadOfFailing only calls ContinueTask AFTER waiting for
// the requeue to "queued" — it never exercises an immediate Continue that races ahead of runHelperTask's own
// classification of the cancelled subprocess. PauseTask's hold is the ONLY signal that branch reads
// (o.taskHeld); if ContinueTask clears it (and its own now-too-early scheduling attempt sees status still
// "running" and silently no-ops, same as TestLostWakeup_ContinueDuringRequeueStillSchedules) before
// classification runs, taskHeld reads false there and the switch falls through to "failed" — mistaking a
// deliberate pause for a genuine crash.
//
// The fake runHelperAgent calls ContinueTask itself, synchronously, right after <-ctx.Done() and before
// returning — since runHelperTask calls o.runHelperAgent inline (no separate goroutine), this deterministically
// forces ContinueTask's hold-clear to land before the switch below it ever runs, with no sleep/timing
// assumption needed.
func TestPause_ImmediateContinueBeforeClassificationStillRequeues(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)

	var calls atomic.Int32
	started := make(chan struct{}, 1)
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		if calls.Add(1) == 1 {
			started <- struct{}{}
			<-ctx.Done()
			if err := o.ContinueTask(context.Background(), assistants.TaskID(taskID)); err != nil {
				t.Errorf("ContinueTask: %v", err)
			}
			return executor.HelperOutcome{}, nil
		}
		return passResultOutcome(), nil
	}

	taskID := attachTestTask(context.Background(), t, o, "op-1")
	<-started
	waitForTaskStatus(t, store, taskID, assistants.StatusRunning, 2*time.Second)

	if err := o.PauseTask(context.Background(), taskID); err != nil {
		t.Fatalf("PauseTask: %v", err)
	}

	// Must reach "completed" (the retried second attempt) — never "failed": an immediate Continue racing ahead
	// of classification must not turn a deliberate pause into a false crash report.
	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 2*time.Second)
}

// TestPauseTask_SurvivesImmediateRequeueRace is a regression test for a race codex's verify gate found.
// PauseTask alone does NOT reach this race — it sets a hold before cancelling, and runHelperTask's own
// `if !o.taskHeld(taskID) { maybeScheduleTask(...) }` branch is gated on that same hold being already clear.
// The real trigger (mirroring TestPause_ImmediateContinueBeforeClassificationStillRequeues) is an IMMEDIATE
// ContinueTask that races ahead of classification and clears the hold before the switch below reads it: that
// makes runHelperTask's own requeue branch SYNCHRONOUSLY (same goroutine, before this attempt's own defer
// runs) reschedule a NEW attempt via maybeScheduleTask → spawnTask → SpawnOwned. Even with free command slots
// (max_parallel >= 2) the NEW attempt must not start until the OLD attempt has finished, deferred cleanup
// included (SpawnOwned runs one goroutine per owner): otherwise the OLD attempt's cleanup could act on the NEW
// attempt's cancel entry, pause marker or activity marker, and a later PauseTask would find nothing to cancel.
//
// The interleaving is forced via o.taskRequeuePreDeferHook (test-only seam): the OLD attempt blocks right
// after scheduling the NEW attempt and right before its own defer runs.
func TestPauseTask_SurvivesImmediateRequeueRace(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)

	// Two concurrent slots for "helper-cmd": a free slot alone must not let the NEW attempt overlap the OLD.
	o.concurrency = concurrency.NewWithSemaphores(bus.NewCriticalBus(16), map[string]concurrency.Semaphore{"helper-cmd": concurrency.ChannelSemaphore(make(chan struct{}, 2))}, "claude")

	var calls atomic.Int32
	oldStarted := make(chan struct{}, 1)
	attempt2Started := make(chan struct{})
	newCtxDone := make(chan struct{})
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		switch calls.Add(1) {
		case 1:
			oldStarted <- struct{}{}
			<-ctx.Done()
			// Forces ContinueTask to race ahead of this goroutine's own classification, clearing the hold
			// BEFORE the switch below reads o.taskHeld — the precondition for its "!taskHeld" branch to
			// actually call maybeScheduleTask synchronously, in THIS goroutine.
			if err := o.ContinueTask(context.Background(), assistants.TaskID(taskID)); err != nil {
				t.Errorf("ContinueTask: %v", err)
			}
			return executor.HelperOutcome{}, nil
		case 2:
			close(attempt2Started)
			<-ctx.Done()
			close(newCtxDone)
			return executor.HelperOutcome{}, nil
		default:
			return passResultOutcome(), nil
		}
	}

	releaseOld := make(chan struct{})
	o.taskRequeuePreDeferHook = func(assistants.TaskID) { <-releaseOld }

	taskID := attachTestTask(context.Background(), t, o, "op-1")
	<-oldStarted
	waitForTaskStatus(t, store, taskID, assistants.StatusRunning, 2*time.Second)

	if err := o.PauseTask(context.Background(), taskID); err != nil {
		t.Fatalf("PauseTask: %v", err)
	}

	select {
	case <-attempt2Started:
		t.Fatal("the NEW attempt started while the OLD attempt had not finished its cleanup")
	case <-time.After(150 * time.Millisecond):
	}
	close(releaseOld) // the OLD attempt finishes; only now may the NEW one start
	select {
	case <-attempt2Started:
	case <-time.After(2 * time.Second):
		t.Fatal("the NEW (requeued) attempt never started after the OLD attempt finished")
	}

	// The NEW attempt's cancel entry is in place, and a second PauseTask reaches its subprocess.
	waitForTrue(t, func() bool {
		_, ok := o.taskCancels.Load(taskID)
		return ok
	}, 2*time.Second, "the NEW attempt must have its cancel entry")
	if err := o.PauseTask(context.Background(), taskID); err != nil {
		t.Fatalf("second PauseTask: %v", err)
	}
	select {
	case <-newCtxDone:
	case <-time.After(2 * time.Second):
		t.Fatal("the NEW (requeued) attempt's subprocess was never actually cancelled — its cancel entry was lost")
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

	// limit smaller than the full history must set hasMore. The first two events (task_created +
	// task_status_changed-to-"queued") share one Seq (same Attach operation/bundle), so a limit of 1 cannot
	// stop between them — the page must grow to include the whole Seq group, or the sibling would be
	// unreachable on the next page (cursor == that shared Seq filters it out as "already consumed").
	if all[0].Seq != all[1].Seq {
		t.Fatalf("test assumes the first two events share a Seq (Attach's same-operation bundle), got %+v", all[:2])
	}
	firstPage, _, hasMoreLimited, err := o.TaskEvents(taskID, 0, 1)
	if err != nil {
		t.Fatalf("TaskEvents with limit=1: %v", err)
	}
	if len(firstPage) != 2 || firstPage[0].Seq != firstPage[1].Seq {
		t.Fatalf("limited page = %+v, want both events sharing the first Seq kept together", firstPage)
	}
	if !hasMoreLimited {
		t.Fatal("hasMore must still be true: events remain beyond the widened page")
	}

	if events, _, _, err := o.TaskEvents("unknown-task", 0, 100); err != nil || len(events) != 0 {
		t.Fatalf("TaskEvents for an unknown task = %+v, %v, want empty/no error", events, err)
	}
}

// TestTaskEvents_SameSeqSiblingNeverLostAcrossPages walks the task's full event history one event at a time
// (limit=1) and asserts every page boundary — even when it falls across events sharing one Seq — reproduces
// the exact same set as a single unbounded fetch, with no event skipped and none repeated.
func TestTaskEvents_SameSeqSiblingNeverLostAcrossPages(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		return passResultOutcome(), nil
	}

	taskID := attachTestTask(context.Background(), t, o, "op-1")
	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 2*time.Second)

	want, _, _, err := o.TaskEvents(taskID, 0, 100)
	if err != nil {
		t.Fatalf("TaskEvents: %v", err)
	}

	var got []state.TaskEvent
	cursor := uint64(0)
	for i := 0; i < 100; i++ {
		page, next, hasMore, err := o.TaskEvents(taskID, cursor, 1)
		if err != nil {
			t.Fatalf("TaskEvents(cursor=%d, limit=1): %v", cursor, err)
		}
		got = append(got, page...)
		cursor = next
		if !hasMore {
			break
		}
	}

	if len(got) != len(want) {
		t.Fatalf("walked %d events via limit=1 pagination, want %d (full set): got=%+v want=%+v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i].Seq != want[i].Seq || got[i].Kind != want[i].Kind {
			t.Fatalf("event %d mismatch: got %+v, want %+v", i, got[i], want[i])
		}
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
