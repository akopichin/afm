// Package orchestrator (this file): tests for the Этап 5 watcher glue (watch_tasks.go/observations.go) — T5.4's
// trigger semantics (one_shot vs checkpoints vs on_completion, the required-watcher final bounded pass, and
// parent-failure stopping new passes), T5.5's "a child task is not auto-subscribed to an unrelated watcher's
// feed", and T5.6's dashboard-facing watcher status (trigger kind + pass count + last processed checkpoint,
// plus the advisory-error-is-not-blocking-attention invariant). Reuses the SAME lightweight test harness
// (newAssistantsTestOrchestrator/attachTestTask/waitForTaskStatus) assistants_test.go already built — no
// second harness.
package orchestrator

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/concurrency"
	"github.com/akopichin/afm/pkg/state"
	"github.com/akopichin/afm/pkg/watch"
)

// watchFakeClock is a manually-advanced watch.Clock — mirrors pkg/watch/collector_test.go's own fakeClock
// (unexported there, so not importable across packages) and o.fingerprintFn's injectable-seam convention.
// Shared across every test in this file that needs deterministic Debounce/Cooldown behavior.
type watchFakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newWatchFakeClock() *watchFakeClock { return &watchFakeClock{now: time.Unix(1700000000, 0)} }

func (c *watchFakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *watchFakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

var _ watch.Clock = (*watchFakeClock)(nil)

// attachWatcherTask attaches a top-level task with an explicit Trigger/Mode (unlike attachTestTask, which
// always uses the one_shot/advisory defaults) to stageID.
func attachWatcherTask(t *testing.T, o *Orchestrator, opID, stageID string, trigger assistants.Trigger, mode assistants.Mode) assistants.TaskID {
	t.Helper()
	taskID, err := o.Attach(context.Background(), AttachRequest{
		OperationID: opID, StageID: stageID, ProfileID: "reviewer",
		Instruction: "watch this", Trigger: trigger, Mode: mode, Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Fatalf("Attach (watcher): %v", err)
	}
	return taskID
}

// substantialObservation builds a Complete, main-agent-shaped Observation carrying enough content to cross
// watch.DefaultConfig's MinNewContentBytes (200) in one shot.
func substantialObservation(stageID string, seq int) executor.Observation {
	return executor.Observation{
		Seq: seq, Complete: true, Type: executor.ObservationAssistantMessage,
		Content: strings.Repeat("x", 500), Actor: stageID + "/implementation",
	}
}

// ---- T5.4: triggers and the final bounded pass ----

// TestTrigger_OneShotRunsOnce: a plain one_shot task (the pre-Этап-5 default) runs exactly once at attach
// time and is never touched again by the watcher poller — it was never bound as a watcher in the first place
// (isWatcherTrigger is false for it).
func TestTrigger_OneShotRunsOnce(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)

	calls := make(chan string, 4)
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		calls <- taskID
		return passResultOutcome(), nil
	}

	taskID := attachTestTask(context.Background(), t, o, "op-1")
	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 2*time.Second)

	select {
	case id := <-calls:
		if id != string(taskID) {
			t.Fatalf("fired task = %s, want %s", id, taskID)
		}
	default:
		t.Fatal("expected exactly one recorded call by the time the task completed")
	}

	// Feeding observations and polling the watcher mechanism must be a complete no-op for a one_shot task.
	o.feedWatchCollectors(assistantsTestStageID, substantialObservation(assistantsTestStageID, 99))
	o.pollWatchersOnce(context.Background())
	select {
	case id := <-calls:
		t.Fatalf("one_shot task ran a second time (task %s) — it must never be bound as a watcher", id)
	case <-time.After(200 * time.Millisecond):
	}

	if _, _, _, ok := o.WatcherStatus(taskID); ok {
		t.Fatal("WatcherStatus must report ok=false for a plain one_shot task — it was never bound")
	}
}

// TestTrigger_OnCompletionRunsAtStageResultReady: an on_completion watcher attached to an ALREADY ACTIVE
// stage must not run before that stage's own result is known — the run is driven solely by EvComplete
// reaching finalizeWatchersForStage, never by Attach's own "parent already active" fast path.
func TestTrigger_OnCompletionRunsAtStageResultReady(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)

	calls := make(chan string, 4)
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		calls <- taskID
		return passResultOutcome(), nil
	}

	taskID := attachWatcherTask(t, o, "op-1", assistantsTestStageID, assistants.TriggerOnCompletion, assistants.ModeAdvisory)
	waitForTaskStatus(t, store, taskID, assistants.StatusWatching, 2*time.Second)

	select {
	case id := <-calls:
		t.Fatalf("on_completion watcher ran prematurely (task %s) before its root stage's result was ready", id)
	case <-time.After(200 * time.Millisecond):
	}

	if _, ok := o.Trigger(assistantsTestStageID, bus.EvComplete, bus.GuardCtx{}, ""); !ok {
		t.Fatal("EvComplete failed for the parent stage")
	}

	select {
	case id := <-calls:
		if id != string(taskID) {
			t.Fatalf("fired task = %s, want %s", id, taskID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("on_completion watcher never ran after its root stage completed")
	}
	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 2*time.Second)

	// Exactly one turn, ever.
	o.pollWatchersOnce(context.Background())
	select {
	case id := <-calls:
		t.Fatalf("on_completion watcher ran a second time (task %s)", id)
	case <-time.After(200 * time.Millisecond):
	}
}

// turnCounter counts the helper turns each task started.
type turnCounter struct {
	mu    sync.Mutex
	turns map[string]int
}

// countTurns makes every helper turn of o count itself and pass, once release is closed (nil: at once).
func countTurns(t *testing.T, o *Orchestrator, release <-chan struct{}) *turnCounter {
	c := &turnCounter{turns: map[string]int{}}
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		c.mu.Lock()
		c.turns[taskID]++
		c.mu.Unlock()
		if release != nil {
			awaitRelease(t, release)
		}
		return passResultOutcome(), nil
	}
	return c
}

func (c *turnCounter) of(id assistants.TaskID) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.turns[string(id)]
}

// An on_completion helper never starts from the main agent's output, whatever its mode and however long the
// output goes on: it starts once, from the stage's completion path, even when there was no output at all. A
// checkpoints watcher fed the same output starts a turn on each checkpoint.
func TestCompletionHelper_DoesNotRunBeforeParentResult(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mode   assistants.Mode
		rounds int // checkpoints worth of main-agent output before the stage completes
	}{
		{"advisory", assistants.ModeAdvisory, 3},
		{"required", assistants.ModeRequired, 3},
		{"advisory without any output", assistants.ModeAdvisory, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, store := newAssistantsTestOrchestrator(t, true)
			activateStage(t, o, assistantsTestStageID)
			clock := newWatchFakeClock()
			o.watchClock = clock
			turns := countTurns(t, o, nil)

			helper := attachWatcherTask(t, o, "op-helper", assistantsTestStageID, assistants.TriggerOnCompletion, tc.mode)
			control := attachWatcherTask(t, o, "op-control", assistantsTestStageID, assistants.TriggerCheckpoints, assistants.ModeAdvisory)
			for seq := 1; seq <= tc.rounds; seq++ {
				o.feedWatchCollectors(assistantsTestStageID, substantialObservation(assistantsTestStageID, seq))
				clock.Advance(10 * time.Second)
				o.pollWatchersOnce(context.Background())
				o.concurrency.WaitAgents()
			}
			if got := turns.of(helper); got != 0 {
				t.Fatalf("on_completion helper ran %d turn(s) before its stage's result was ready", got)
			}
			if got := store.Snapshot().Assistants.Tasks[helper].Status; got != assistants.StatusWatching {
				t.Fatalf("on_completion helper status = %s before completion, want watching", got)
			}
			if got := turns.of(control); got != tc.rounds {
				t.Fatalf("checkpoints watcher ran %d turn(s) on %d checkpoints of the same output", got, tc.rounds)
			}

			o.completeStage(context.Background(), assistantsTestStageID, state.StatusRunning, "main done")
			waitForTaskStatus(t, store, helper, assistants.StatusCompleted, 2*time.Second)
			waitStageStatus(t, store, assistantsTestStageID, state.StatusDone, 2*time.Second)

			o.pollWatchersOnce(context.Background())
			o.finalizeWatchersForStage(assistantsTestStageID, bus.EvComplete)
			o.concurrency.WaitAgents()
			if got := turns.of(helper); got != 1 {
				t.Fatalf("on_completion helper ran %d turns, want exactly one after its stage completed", got)
			}
		})
	}
}

// commitTaskStatus records a task's status in the journal alone, as a commit whose follow-up a crash cut off.
func commitTaskStatus(t *testing.T, store *state.Store, id assistants.TaskID, status assistants.TaskStatus) {
	t.Helper()
	eff, err := taskStatusEffect(id, status, "test")
	if err != nil {
		t.Fatal(err)
	}
	op := state.Operation{ID: "test-status:" + string(id) + ":" + randomOpSuffix(), Kind: "test_status", Effects: []state.Effect{eff}}
	if _, err := store.ApplyRunOperation(op, nil); err != nil {
		t.Fatalf("commit %s %s: %v", id, status, err)
	}
}

// commitStageEvent records a stage transition in the journal alone, without what triggerWithSeq does after it.
func commitStageEvent(t *testing.T, o *Orchestrator, stageID string, ev bus.FSMEvent) {
	t.Helper()
	if _, _, _, ok, err := o.applyTransition(stageID, ev, bus.GuardCtx{}, "test"); !ok || err != nil {
		t.Fatalf("%s %s: ok=%v err=%v", stageID, ev, ok, err)
	}
}

// taskStartRecords are the journal lines of the turn starts of id.
func taskStartRecords(t *testing.T, o *Orchestrator, id assistants.TaskID) []string {
	t.Helper()
	var out []string
	for _, line := range operationRecords(t, o.opts.RunDir, "start_task") {
		if strings.Contains(line, string(id)) {
			out = append(out, line)
		}
	}
	return out
}

// queueCompletionHelper attaches an on_completion helper to the running test stage and records it queued, as a
// fire, a retry or a recovery leaves it: due to start, with nothing having tried yet.
func queueCompletionHelper(t *testing.T, o *Orchestrator, store *state.Store, opID string, mode assistants.Mode) assistants.TaskID {
	t.Helper()
	id := attachWatcherTask(t, o, opID, assistantsTestStageID, assistants.TriggerOnCompletion, mode)
	commitTaskStatus(t, store, id, assistants.StatusQueued)
	return id
}

// requireNoTurn fails unless id is still in want with no turn started: neither a process nor a start record.
func requireNoTurn(t *testing.T, o *Orchestrator, turns *turnCounter, id assistants.TaskID, want assistants.TaskStatus) {
	t.Helper()
	o.concurrency.WaitAgents()
	if got := turns.of(id); got != 0 {
		t.Fatalf("on_completion helper ran %d turn(s) before its stage's result was ready", got)
	}
	if got := taskStartRecords(t, o, id); len(got) != 0 {
		t.Fatalf("on_completion helper start recorded before its stage's result was ready: %q", got)
	}
	if got := o.opts.Store.Snapshot().Assistants.Tasks[id].Status; got != want {
		t.Fatalf("on_completion helper status = %s, want %s", got, want)
	}
}

// A queued on_completion helper does not start before its stage has a result, whatever asks it to: the scheduler,
// a Continue of the task, its wake timer or a message, which stays unread for it. Once the stage has finished its
// own work, the same call starts it.
func TestCompletionHelper_QueuedTurnWaitsForParentResult(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	turns := countTurns(t, o, nil)
	ctx := context.Background()
	helper := queueCompletionHelper(t, o, store, "op-helper", assistants.ModeRequired)
	participant := meshHelperParticipantID(helper)

	o.maybeScheduleTask(ctx, helper)
	if err := o.PauseTask(ctx, helper); err != nil {
		t.Fatalf("PauseTask: %v", err)
	}
	if err := o.ContinueTask(ctx, helper); err != nil {
		t.Fatalf("ContinueTask: %v", err)
	}
	o.retryTaskAt(ctx, helper, o.clk().Now())
	th, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-open", Scope: agentcomm.ScopeStage, Members: []agentcomm.ParticipantID{meshUserParticipantID, participant},
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	if _, err := o.Send(ctx, SendRequest{
		OperationID: "op-message", ThreadID: th.ID, Sender: meshUserParticipantID, Recipients: []agentcomm.ParticipantID{participant},
		Kind: agentcomm.MessageKindMessage, Body: "collect the logs", Actor: state.Actor{Kind: actorKindUser},
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	requireNoTurn(t, o, turns, helper, assistants.StatusQueued)
	if got := unreadOf(store.Snapshot(), participant); len(got) != 1 {
		t.Fatalf("helper inbox holds %d unread message(s), want the message kept for its turn", len(got))
	}

	commitStageEvent(t, o, assistantsTestStageID, bus.EvAwaitAssistants)
	o.maybeScheduleTask(ctx, helper)
	o.concurrency.WaitAgents()
	if got := turns.of(helper); got != 1 {
		t.Fatalf("on_completion helper ran %d turn(s) once its stage awaited it, want 1", got)
	}
}

// A Retry of a failed on_completion helper queues it again, and it waits for its stage's result like any turn: the
// stage's completion starts that queued turn once.
func TestCompletionHelper_RetryWaitsForParentResult(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	turns := countTurns(t, o, nil)
	helper := attachWatcherTask(t, o, "op-helper", assistantsTestStageID, assistants.TriggerOnCompletion, assistants.ModeAdvisory)
	commitTaskStatus(t, store, helper, assistants.StatusFailed)

	if err := o.RetryTask(context.Background(), helper); err != nil {
		t.Fatalf("RetryTask: %v", err)
	}
	requireNoTurn(t, o, turns, helper, assistants.StatusQueued)

	o.completeStage(context.Background(), assistantsTestStageID, state.StatusRunning, "main done")
	waitForTaskStatus(t, store, helper, assistants.StatusCompleted, 2*time.Second)
	waitStageStatus(t, store, assistantsTestStageID, state.StatusDone, 2*time.Second)
	o.concurrency.WaitAgents()
	if got := turns.of(helper); got != 1 {
		t.Fatalf("on_completion helper ran %d turns after its stage completed, want 1", got)
	}
}

// A paused stage holds its on_completion helpers even when it paused while awaiting them.
func TestCompletionHelper_PausedStageHoldsItsHelper(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	turns := countTurns(t, o, nil)
	ctx := context.Background()
	helper := queueCompletionHelper(t, o, store, "op-helper", assistants.ModeRequired)
	commitStageEvent(t, o, assistantsTestStageID, bus.EvAwaitAssistants)
	commitStageEvent(t, o, assistantsTestStageID, bus.EvPause)

	o.maybeScheduleTask(ctx, helper)
	requireNoTurn(t, o, turns, helper, assistants.StatusQueued)

	if _, to, _, ok, err := o.applyTransition(assistantsTestStageID, bus.EvContinue, bus.GuardCtx{PausedFrom: state.StatusAwaitingAssistants}, ""); !ok || err != nil || to != state.StatusAwaitingAssistants {
		t.Fatalf("continue: to=%s ok=%v err=%v", to, ok, err)
	}
	o.maybeScheduleTask(ctx, helper)
	o.concurrency.WaitAgents()
	if got := turns.of(helper); got != 1 {
		t.Fatalf("on_completion helper ran %d turn(s) after its stage continued, want 1", got)
	}
}

// A Revise committed after the scheduler read a ready stage, and before the start commits, rejects the start: the
// gate is checked again under the Store lock, so the old attempt's helper never runs.
func TestCompletionHelper_ReviseBeforeStartCommitRejectsTheStart(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	turns := countTurns(t, o, nil)
	helper := queueCompletionHelper(t, o, store, "op-helper", assistants.ModeRequired)
	commitStageEvent(t, o, assistantsTestStageID, bus.EvAwaitAssistants)
	var revise sync.Once
	o.settlePlannedHook = func() {
		revise.Do(func() {
			commitStageEvent(t, o, assistantsTestStageID, bus.EvRevise)
			bump := state.Effect{Kind: "root_generation_bumped", Target: state.Target{StageID: assistantsTestStageID}}
			if _, err := store.ApplyRunOperation(state.Operation{ID: "test-bump", Kind: opKindBumpRootGeneration, Effects: []state.Effect{bump}}, nil); err != nil {
				t.Errorf("bump: %v", err)
			}
		})
	}

	o.maybeScheduleTask(context.Background(), helper)
	requireNoTurn(t, o, turns, helper, assistants.StatusQueued)
}

// startCompletionHelperBehindASlot queues a required on_completion helper of a stage awaiting it and starts its
// turn behind a full command slot: the start is committed, the process waits for the returned slot to free up.
func startCompletionHelperBehindASlot(t *testing.T, o *Orchestrator, store *state.Store) (assistants.TaskID, concurrency.ChannelSemaphore) {
	t.Helper()
	slot := concurrency.ChannelSemaphore(make(chan struct{}, 1))
	slot <- struct{}{}
	o.concurrency = concurrency.NewWithSemaphores(bus.NewCriticalBus(16), map[string]concurrency.Semaphore{"helper-cmd": slot}, "claude")
	helper := queueCompletionHelper(t, o, store, "op-helper", assistants.ModeRequired)
	commitStageEvent(t, o, assistantsTestStageID, bus.EvAwaitAssistants)
	o.maybeScheduleTask(context.Background(), helper)
	waitForTaskStatus(t, store, helper, assistants.StatusRunning, 2*time.Second)
	return helper, slot
}

// A turn whose start committed while its stage awaited it, but which waited for its command slot, goes back to the
// queue when the stage paused meanwhile: the gate is checked again once the slot is granted. The stage's Continue
// starts the queued pass once, and the stage completes on its result.
func TestCompletionHelper_PauseWhileWaitingForTheSlotRequeuesTheTurn(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	turns := countTurns(t, o, nil)
	ctx := context.Background()
	helper, slot := startCompletionHelperBehindASlot(t, o, store)
	if err := o.Pause(ctx, assistantsTestStageID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	<-slot

	requeued := waitForTaskStatus(t, store, helper, assistants.StatusQueued, 2*time.Second)
	if requeued.StatusReason != "re-queued: parent result not ready" {
		t.Fatalf("requeue reason = %q", requeued.StatusReason)
	}
	o.concurrency.WaitAgents()
	if got := turns.of(helper); got != 0 {
		t.Fatalf("on_completion helper ran %d turn(s) while its stage was paused", got)
	}
	startsBefore := len(taskStartRecords(t, o, helper))

	if err := o.Continue(ctx, assistantsTestStageID); err != nil {
		t.Fatalf("Continue: %v", err)
	}
	waitForTaskStatus(t, store, helper, assistants.StatusCompleted, 2*time.Second)
	waitStageStatus(t, store, assistantsTestStageID, state.StatusDone, 2*time.Second)
	o.concurrency.WaitAgents()
	if got := turns.of(helper); got != 1 {
		t.Fatalf("on_completion helper ran %d turns after its stage continued, want 1", got)
	}
	if got := len(taskStartRecords(t, o, helper)) - startsBefore; got != 1 {
		t.Fatalf("on_completion helper has %d start records after the Continue, want 1", got)
	}
}

// A Continue of the stage that lands while such a turn is going back to the queue finds the task still running, so
// it issues nothing: the requeue itself schedules the pass again, which runs once, and the stage completes.
func TestCompletionHelper_ContinueDuringTheRequeueStillStartsThePass(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	turns := countTurns(t, o, nil)
	ctx := context.Background()
	helper, slot := startCompletionHelperBehindASlot(t, o, store)
	if err := o.Pause(ctx, assistantsTestStageID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	continued := make(chan struct{})
	o.taskRequeueBarrier = func(id assistants.TaskID) {
		defer close(continued)
		if err := o.Continue(ctx, assistantsTestStageID); err != nil {
			t.Errorf("Continue: %v", err)
		}
		if got := o.currentTaskStatus(id); got != assistants.StatusRunning {
			t.Errorf("helper = %s when the Continue issued its pass, want running (the requeue not committed yet)", got)
		}
	}
	<-slot

	select {
	case <-continued:
	case <-time.After(2 * time.Second):
		t.Fatal("taskRequeueBarrier was never invoked: spawnTask did not requeue the turn of the paused stage")
	}
	waitForTaskStatus(t, store, helper, assistants.StatusCompleted, 2*time.Second)
	waitStageStatus(t, store, assistantsTestStageID, state.StatusDone, 2*time.Second)
	o.concurrency.WaitAgents()
	if got := turns.of(helper); got != 1 {
		t.Fatalf("on_completion helper ran %d turns, want 1", got)
	}
}

// A delegated child with trigger on_completion is not a stage's completion helper: its parent may be waiting for
// it, so it starts while the stage is still running.
func TestCompletionHelper_DelegatedChildIsNotGated(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	childRan := make(chan struct{}, 1)
	parentID := attachTestTask(context.Background(), t, o, "op-parent")
	childID := deriveTaskID("op-child:0")
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		if taskID == string(childID) {
			childRan <- struct{}{}
			return passResultOutcome(), nil
		}
		awaitRelease(t, release)
		return passResultOutcome(), nil
	}
	parent := string(parentID)
	created, err := taskCreatedEffect(childID, assistantsTestStageID, state.TaskCreatedPayload{
		ParentTaskID: &parent, ProfileID: "reviewer", Title: "child", Instruction: "do it",
		Mode: "advisory", Trigger: string(assistants.TriggerOnCompletion), CreatedBy: "assistant",
	})
	if err != nil {
		t.Fatal(err)
	}
	quota, err := quotaReservedEffect(1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyRunOperation(state.Operation{ID: "op-child", Kind: "attach_children", Effects: []state.Effect{created, quota}}, nil); err != nil {
		t.Fatalf("commit child: %v", err)
	}
	commitTaskStatus(t, store, childID, assistants.StatusQueued)

	o.maybeScheduleTask(context.Background(), childID)
	select {
	case <-childRan:
	case <-time.After(2 * time.Second):
		t.Fatal("a delegated on_completion child did not start while its stage was running")
	}
}

// One completion pass is one turn however many paths issue it — the stage's result, a second issue, the final-pass
// funnel and a recovery scan — and that turn is the helper's last: it ends completed and the stage completes.
func TestCompletionHelper_IssuedOncePerPass(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	release := make(chan struct{})
	turns := countTurns(t, o, release)
	ctx := context.Background()
	helper := attachWatcherTask(t, o, "op-helper", assistantsTestStageID, assistants.TriggerOnCompletion, assistants.ModeRequired)
	commitStageEvent(t, o, assistantsTestStageID, bus.EvAwaitAssistants)

	o.issueCompletionPasses(ctx, assistantsTestStageID)
	o.issueCompletionPasses(ctx, assistantsTestStageID)
	o.finalizeWatchersForStage(assistantsTestStageID, bus.EvAwaitAssistants)
	o.resumeAssistantsAfterRestart(ctx)
	close(release)

	waitForTaskStatus(t, store, helper, assistants.StatusCompleted, 2*time.Second)
	waitStageStatus(t, store, assistantsTestStageID, state.StatusDone, 2*time.Second)
	o.concurrency.WaitAgents()
	if got := turns.of(helper); got != 1 {
		t.Fatalf("on_completion helper ran %d turns, want 1", got)
	}
	if got := taskStartRecords(t, o, helper); len(got) != 1 {
		t.Fatalf("on_completion helper has %d start records, want 1", len(got))
	}
}

// A stage paused while it awaits its helpers keeps an on_completion pass it has not started; its Continue starts
// that pass once, and the stage completes on its result.
func TestCompletionHelper_ContinueOfAPausedStageStartsItsPass(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	turns := countTurns(t, o, nil)
	ctx := context.Background()
	helper := attachWatcherTask(t, o, "op-helper", assistantsTestStageID, assistants.TriggerOnCompletion, assistants.ModeRequired)
	commitStageEvent(t, o, assistantsTestStageID, bus.EvAwaitAssistants)
	if err := o.Pause(ctx, assistantsTestStageID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	o.issueCompletionPasses(ctx, assistantsTestStageID)
	requireNoTurn(t, o, turns, helper, assistants.StatusWatching)

	if err := o.Continue(ctx, assistantsTestStageID); err != nil {
		t.Fatalf("Continue: %v", err)
	}
	waitForTaskStatus(t, store, helper, assistants.StatusCompleted, 2*time.Second)
	waitStageStatus(t, store, assistantsTestStageID, state.StatusDone, 2*time.Second)
	o.concurrency.WaitAgents()
	if got := turns.of(helper); got != 1 {
		t.Fatalf("on_completion helper ran %d turns, want 1", got)
	}
}

// A stage Pause does not hold a pass already under way: its result is final although the stage is paused, so the
// Continue completes the stage instead of running the helper again.
func TestCompletionHelper_PauseDuringThePassDoesNotRunItAgain(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	release := make(chan struct{})
	turns := countTurns(t, o, release)
	ctx := context.Background()
	helper := attachWatcherTask(t, o, "op-helper", assistantsTestStageID, assistants.TriggerOnCompletion, assistants.ModeRequired)
	o.completeStage(ctx, assistantsTestStageID, state.StatusRunning, "main done")
	waitUntil(t, "the pass started its process", func() bool { return turns.of(helper) == 1 })
	if err := o.Pause(ctx, assistantsTestStageID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	close(release)
	waitForTaskStatus(t, store, helper, assistants.StatusCompleted, 2*time.Second)

	if err := o.Continue(ctx, assistantsTestStageID); err != nil {
		t.Fatalf("Continue: %v", err)
	}
	waitStageStatus(t, store, assistantsTestStageID, state.StatusDone, 2*time.Second)
	o.concurrency.WaitAgents()
	if got := turns.of(helper); got != 1 {
		t.Fatalf("on_completion helper ran %d turns, want 1", got)
	}
}

// An on_completion helper attached when its stage already awaits its assistants starts at once, and only once: an
// advisory one does not hold the stage, a required one closes the gate with its result.
func TestCompletionHelper_AttachedAfterTheStageResultRunsOnce(t *testing.T) {
	for _, mode := range []assistants.Mode{assistants.ModeAdvisory, assistants.ModeRequired} {
		t.Run(string(mode), func(t *testing.T) {
			o, store := newAssistantsTestOrchestrator(t, true)
			activateStage(t, o, assistantsTestStageID)
			release := make(chan struct{})
			turns := countTurns(t, o, release)
			ctx := context.Background()
			blocker := attachRequiredTask(ctx, t, o, assistantsTestStageID, "op-blocker")
			waitForTaskStatus(t, store, blocker, assistants.StatusRunning, 2*time.Second)
			o.completeStage(ctx, assistantsTestStageID, state.StatusRunning, "main done")
			waitStageStatus(t, store, assistantsTestStageID, state.StatusAwaitingAssistants, 2*time.Second)

			helper := attachWatcherTask(t, o, "op-helper", assistantsTestStageID, assistants.TriggerOnCompletion, mode)
			waitForTaskStatus(t, store, helper, assistants.StatusRunning, 2*time.Second)
			close(release)

			waitForTaskStatus(t, store, helper, assistants.StatusCompleted, 2*time.Second)
			waitStageStatus(t, store, assistantsTestStageID, state.StatusDone, 2*time.Second)
			o.concurrency.WaitAgents()
			if got := turns.of(helper); got != 1 {
				t.Fatalf("on_completion helper ran %d turns, want 1", got)
			}
		})
	}
}

// A stage that fails has no result: its on_completion helpers get no pass, whoever asks.
func TestCompletionHelper_FailedStageGetsNoPass(t *testing.T) {
	for _, ev := range []bus.FSMEvent{bus.EvFail, bus.EvVerifyFail} {
		t.Run(string(ev), func(t *testing.T) {
			o, _ := newAssistantsTestOrchestrator(t, true)
			activateStage(t, o, assistantsTestStageID)
			turns := countTurns(t, o, nil)
			helper := attachWatcherTask(t, o, "op-helper", assistantsTestStageID, assistants.TriggerOnCompletion, assistants.ModeRequired)
			if _, ok := o.Trigger(assistantsTestStageID, ev, bus.GuardCtx{}, "boom"); !ok {
				t.Fatalf("%s failed", ev)
			}
			o.issueCompletionPasses(context.Background(), assistantsTestStageID)
			requireNoTurn(t, o, turns, helper, assistants.StatusWatching)
		})
	}
}

// A provider hold at the stage's result defers the pass without losing it: the helper waits queued and runs once
// the hold ends.
func TestCompletionHelper_ProviderHoldDefersThePassWithoutLosingIt(t *testing.T) {
	o, store, clk := newHelperHoldOrch(t)
	turns := countTurns(t, o, nil)
	activateStage(t, o, assistantsTestStageID)
	helper := attachWatcherTask(t, o, "op-helper", assistantsTestStageID, assistants.TriggerOnCompletion, assistants.ModeAdvisory)
	parkStageForTest(t, o, "other", holdHelperPark(holdNow.Add(time.Hour)))

	o.completeStage(context.Background(), assistantsTestStageID, state.StatusRunning, "main done")
	if got := store.Snapshot().Assistants.Tasks[helper].Status; got != assistants.StatusQueued || turns.of(helper) != 0 {
		t.Fatalf("on_completion helper = %s after %d turns under the provider hold, want queued without a turn", got, turns.of(helper))
	}

	clk.BlockUntil(1)
	if _, ok := o.Trigger("other", bus.EvPause, bus.GuardCtx{}, "release the hold"); !ok {
		t.Fatal("pause failed")
	}
	clk.Advance(time.Hour)
	waitForTaskStatus(t, store, helper, assistants.StatusCompleted, 5*time.Second)
	if got := turns.of(helper); got != 1 {
		t.Fatalf("on_completion helper ran %d turns, want 1 after the hold", got)
	}
}

// TestTrigger_RequiredWatcherHasFinalBoundedPass: a REQUIRED checkpoints watcher that never naturally crossed
// the collector's firing threshold still gets exactly one guaranteed, bounded pass once its root stage
// completes — "watching" forever is never an acceptable terminal state for it. A plain ADVISORY checkpoints
// watcher under the identical conditions gets no such forced pass (contrast case, same test: this IS what
// makes the required guarantee special).
func TestTrigger_RequiredWatcherHasFinalBoundedPass(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)

	calls := make(chan string, 4)
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		calls <- taskID
		return passResultOutcome(), nil
	}

	required := attachWatcherTask(t, o, "op-required", assistantsTestStageID, assistants.TriggerCheckpoints, assistants.ModeRequired)
	advisory := attachWatcherTask(t, o, "op-advisory", assistantsTestStageID, assistants.TriggerCheckpoints, assistants.ModeAdvisory)
	waitForTaskStatus(t, store, required, assistants.StatusWatching, 2*time.Second)
	waitForTaskStatus(t, store, advisory, assistants.StatusWatching, 2*time.Second)

	// Neither watcher ever receives anything substantial — the stage just finishes with nothing having ever
	// crossed the collector's firing threshold.
	if _, ok := o.Trigger(assistantsTestStageID, bus.EvComplete, bus.GuardCtx{}, ""); !ok {
		t.Fatal("EvComplete failed for the parent stage")
	}

	select {
	case id := <-calls:
		if id != string(required) {
			t.Fatalf("fired task = %s, want the required watcher %s", id, required)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("required watcher never got its guaranteed final pass once the root stage completed")
	}
	waitForTaskStatus(t, store, required, assistants.StatusCompleted, 2*time.Second)

	// The plain advisory watcher gets no such guarantee.
	select {
	case id := <-calls:
		t.Fatalf("advisory watcher must not get a forced final pass, but task %s ran", id)
	case <-time.After(200 * time.Millisecond):
	}
	rs := store.Snapshot()
	if tk := rs.Assistants.Tasks[advisory]; tk.Status != assistants.StatusWatching {
		t.Fatalf("advisory watcher status = %s, want still watching (never forced)", tk.Status)
	}
}

// TestTrigger_ParentFailedStopsNewPasses: once the root stage fails, a checkpoints watcher must never fire
// again, however much substantial content keeps arriving — while whatever it already recorded from passes
// BEFORE the failure stays exactly as it was (partial results and reason preserved).
func TestTrigger_ParentFailedStopsNewPasses(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	clock := newWatchFakeClock()
	o.watchClock = clock

	calls := make(chan string, 8)
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		calls <- taskID
		return passResultOutcome(), nil
	}

	taskID := attachWatcherTask(t, o, "op-1", assistantsTestStageID, assistants.TriggerCheckpoints, assistants.ModeAdvisory)
	waitForTaskStatus(t, store, taskID, assistants.StatusWatching, 2*time.Second)

	// One pass fires normally, before the failure.
	o.feedWatchCollectors(assistantsTestStageID, substantialObservation(assistantsTestStageID, 1))
	clock.Advance(10 * time.Second)
	o.pollWatchersOnce(context.Background())
	select {
	case id := <-calls:
		if id != string(taskID) {
			t.Fatalf("fired task = %s, want %s", id, taskID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watcher never fired its first pass")
	}
	waitForTaskStatus(t, store, taskID, assistants.StatusWatching, 2*time.Second)

	eventsBefore, _, _, err := o.TaskEvents(taskID, 0, 100)
	if err != nil {
		t.Fatalf("TaskEvents: %v", err)
	}
	var sawResult bool
	for _, e := range eventsBefore {
		if e.Kind == "task_result" {
			sawResult = true
		}
	}
	if !sawResult {
		t.Fatal("the first pass's result must be durably recorded before the failure")
	}

	if _, ok := o.Trigger(assistantsTestStageID, bus.EvFail, bus.GuardCtx{}, "boom"); !ok {
		t.Fatal("EvFail failed for the parent stage")
	}

	// Plenty more substantial content, plenty of elapsed time — no new pass must ever fire again.
	o.feedWatchCollectors(assistantsTestStageID, substantialObservation(assistantsTestStageID, 2))
	clock.Advance(time.Hour)
	o.pollWatchersOnce(context.Background())
	select {
	case id := <-calls:
		t.Fatalf("watcher fired again (task %s) after its root stage failed", id)
	case <-time.After(200 * time.Millisecond):
	}

	eventsAfter, _, _, err := o.TaskEvents(taskID, 0, 100)
	if err != nil {
		t.Fatalf("TaskEvents: %v", err)
	}
	if len(eventsAfter) != len(eventsBefore) {
		t.Fatalf("event history changed after the stage failed: before=%d after=%d (partial results must be preserved untouched)", len(eventsBefore), len(eventsAfter))
	}
}

// ---- T5.5: a child's sources are explicit, never an implicit subscription to an unrelated feed ----

// TestLoop_ChildNotAutoSubscribedToAncestors: attaching a NEW watcher task binds it ONLY to the observation
// feed of its OWN root stage — never, implicitly, to the feed of some OTHER already-bound watcher just
// because both happen to exist in the same run. Sources are explicit (keyed by RootStageID), never an
// automatic "subscribe to everything that already exists" broadcast.
func TestLoop_ChildNotAutoSubscribedToAncestors(t *testing.T) {
	const otherStageID = "other-stage"
	o, store := newAssistantsTestOrchestrator(t, true, flow.Stage{ID: otherStageID, Command: "claude"})
	activateStage(t, o, assistantsTestStageID)
	activateStage(t, o, otherStageID)

	clock := newWatchFakeClock()
	o.watchClock = clock

	calls := make(chan string, 8)
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		calls <- taskID
		return passResultOutcome(), nil
	}

	watcherA := attachWatcherTask(t, o, "op-a", assistantsTestStageID, assistants.TriggerCheckpoints, assistants.ModeAdvisory)
	watcherB := attachWatcherTask(t, o, "op-b", otherStageID, assistants.TriggerCheckpoints, assistants.ModeAdvisory)
	waitForTaskStatus(t, store, watcherA, assistants.StatusWatching, 2*time.Second)
	waitForTaskStatus(t, store, watcherB, assistants.StatusWatching, 2*time.Second)

	// Feed substantial content ONLY for watcherA's stage.
	o.feedWatchCollectors(assistantsTestStageID, substantialObservation(assistantsTestStageID, 1))
	clock.Advance(10 * time.Second)
	o.pollWatchersOnce(context.Background())

	select {
	case id := <-calls:
		if id != string(watcherA) {
			t.Fatalf("fired task = %s, want watcherA %s", id, watcherA)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watcherA never fired after receiving substantial content")
	}
	waitForTaskStatus(t, store, watcherA, assistants.StatusWatching, 2*time.Second)

	// watcherB, rooted at a DIFFERENT stage, must never have received that feed — give it every chance to
	// (wrongly) fire too before asserting it never did.
	clock.Advance(10 * time.Second)
	o.pollWatchersOnce(context.Background())
	select {
	case id := <-calls:
		t.Fatalf("watcherB fired (task %s) from a feed meant only for watcherA's stage — sources must be explicit, not an implicit subscription", id)
	case <-time.After(200 * time.Millisecond):
	}
	rs := store.Snapshot()
	if tk := rs.Assistants.Tasks[watcherB]; tk.Status != assistants.StatusWatching {
		t.Fatalf("watcherB status = %s, want still watching (never fired)", tk.Status)
	}
	if passes, _, _, ok := o.WatcherStatus(watcherB); !ok || passes != 0 {
		t.Fatalf("watcherB WatcherStatus passes=%d,ok=%v, want 0,true", passes, ok)
	}
}

// ---- T5.6: dashboard-facing watcher status ----

// TestWatcherUI_ShowsTriggerAndPassCount: WatcherStatus reports a bound watcher's own fired-pass count,
// growing by exactly one per fired checkpoint — this is what the dashboard's AssistantTaskView surfaces
// alongside the Trigger field the view already carried (cmd/afm/run_collaboration.go's TaskTree).
func TestWatcherUI_ShowsTriggerAndPassCount(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	clock := newWatchFakeClock()
	o.watchClock = clock

	calls := make(chan string, 8)
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		calls <- taskID
		return passResultOutcome(), nil
	}

	taskID := attachWatcherTask(t, o, "op-1", assistantsTestStageID, assistants.TriggerCheckpoints, assistants.ModeAdvisory)
	waitForTaskStatus(t, store, taskID, assistants.StatusWatching, 2*time.Second)

	if passes, _, _, ok := o.WatcherStatus(taskID); !ok || passes != 0 {
		t.Fatalf("WatcherStatus before any fire = passes=%d,ok=%v, want 0,true", passes, ok)
	}

	for i := 0; i < 2; i++ {
		o.feedWatchCollectors(assistantsTestStageID, substantialObservation(assistantsTestStageID, i+1))
		clock.Advance(10 * time.Second)
		o.pollWatchersOnce(context.Background())
		select {
		case <-calls:
		case <-time.After(2 * time.Second):
			t.Fatalf("pass %d never fired", i+1)
		}
		waitForTaskStatus(t, store, taskID, assistants.StatusWatching, 2*time.Second)
		// StatusWatching is committed before the helper's deferred collector cleanup.
		// Finish that turn before advancing the fake clock for the next checkpoint;
		// otherwise cooldown can start at the newly advanced time.
		o.concurrency.WaitAgents()
	}

	passes, _, _, ok := o.WatcherStatus(taskID)
	if !ok || passes != 2 {
		t.Fatalf("WatcherStatus passes = %d,%v, want 2,true after two fired checkpoints", passes, ok)
	}

	// Trigger kind itself is already carried by the plain assistants.Task/AssistantTaskView.Trigger field —
	// confirm it reads back exactly what was requested at Attach time.
	rs := store.Snapshot()
	if tk, ok := rs.Assistants.Tasks[taskID]; !ok || tk.Trigger != assistants.TriggerCheckpoints {
		t.Fatalf("Task.Trigger = %v, want %q", tk.Trigger, assistants.TriggerCheckpoints)
	}
}

// TestWatcherUI_ShowsLastProcessedCheckpoint: WatcherStatus's last-processed-checkpoint reporting mirrors the
// durable CheckpointStore's own cursor exactly — absent before any checkpoint was ever committed, and visible
// the instant commitAndFire durably records it (BEFORE the fired turn's own subprocess even returns — T5.2's
// commit-before-fire ordering, observable here through the SAME read path the dashboard uses).
func TestWatcherUI_ShowsLastProcessedCheckpoint(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	clock := newWatchFakeClock()
	o.watchClock = clock

	release := make(chan struct{})
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		awaitRelease(t, release)
		return passResultOutcome(), nil
	}

	taskID := attachWatcherTask(t, o, "op-1", assistantsTestStageID, assistants.TriggerCheckpoints, assistants.ModeAdvisory)
	waitForTaskStatus(t, store, taskID, assistants.StatusWatching, 2*time.Second)

	if _, _, hasCheckpoint, ok := o.WatcherStatus(taskID); !ok || hasCheckpoint {
		t.Fatalf("hasCheckpoint must be false before any checkpoint was ever committed, got ok=%v hasCheckpoint=%v", ok, hasCheckpoint)
	}

	o.feedWatchCollectors(assistantsTestStageID, substantialObservation(assistantsTestStageID, 7))
	clock.Advance(10 * time.Second)
	o.pollWatchersOnce(context.Background())
	waitForTaskStatus(t, store, taskID, assistants.StatusRunning, 2*time.Second)

	_, lastSeq, hasCheckpoint, ok := o.WatcherStatus(taskID)
	if !ok || !hasCheckpoint || lastSeq != 7 {
		t.Fatalf("WatcherStatus = lastSeq=%d hasCheckpoint=%v ok=%v, want 7,true,true (durable BEFORE the turn even finishes)", lastSeq, hasCheckpoint, ok)
	}

	close(release)
	waitForTaskStatus(t, store, taskID, assistants.StatusWatching, 2*time.Second)
}

// TestWatcherUI_AdvisoryErrorIsNotBlockingAttention: an advisory watcher task reaching "failed" — even right
// as, or after, its OWN root stage already completed — must never change that stage's FSM status. The
// dashboard's attention mechanism (use-attention.ts's ATTENTION_STATUSES) reads ONLY stage status, so this is
// exactly what keeps a failed advisory watcher from masquerading as a blocking, attention-worthy failure.
func TestWatcherUI_AdvisoryErrorIsNotBlockingAttention(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)

	release := make(chan struct{})
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		awaitRelease(t, release)
		return executor.HelperOutcome{}, errors.New("boom: watcher crashed")
	}

	taskID := attachWatcherTask(t, o, "op-1", assistantsTestStageID, assistants.TriggerOnCompletion, assistants.ModeAdvisory)
	waitForTaskStatus(t, store, taskID, assistants.StatusWatching, 2*time.Second)

	if _, ok := o.Trigger(assistantsTestStageID, bus.EvComplete, bus.GuardCtx{}, ""); !ok {
		t.Fatal("EvComplete failed for the parent stage")
	}
	// The forced final pass is now in flight (blocked on release) — the stage is already "done".
	waitForTaskStatus(t, store, taskID, assistants.StatusRunning, 2*time.Second)
	if o.currentStatus(assistantsTestStageID) != state.StatusDone {
		t.Fatal("parent stage should already be done by the time its forced final watcher pass starts")
	}

	close(release)
	waitForTaskStatus(t, store, taskID, assistants.StatusFailed, 2*time.Second)

	if o.currentStatus(assistantsTestStageID) != state.StatusDone {
		t.Fatal("a watcher task failing AFTER its stage already completed must not change the stage's FSM status")
	}
}
