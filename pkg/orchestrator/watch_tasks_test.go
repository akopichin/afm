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

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
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
