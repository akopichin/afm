// Package orchestrator (this file, Этап 5 T5.1–T5.5): wires the standalone pkg/watch collector/checkpoint
// subsystem into the EXISTING helper-task lifecycle (assistants.go/assistants_lifecycle.go) — a watcher is
// just another assistants.Task (Trigger ∈ {checkpoints, on_completion}), scheduled through the SAME
// maybeScheduleTask/spawnTask/runHelperTask path every other task uses. This file only ever decides WHEN that
// existing machinery should run the task's next turn; it never spawns a process on its own and never touches
// the root stage's FSM (Инвариант: advisory tasks are observational).
package orchestrator

import (
	"context"
	"path/filepath"
	"time"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
	"github.com/akopichin/afm/pkg/watch"
)

// defaultWatchPollInterval mirrors the file-based dialog poller's own cadence (dialog_poller.go's
// startQuestionPoller) — a 1s tick is cheap and keeps a fired checkpoint's observable latency low without
// busy-polling.
const defaultWatchPollInterval = 1 * time.Second

// isWatcherTrigger reports whether a task's Trigger makes it a Stage-5 "watcher" — anything other than
// one_shot keeps a standing Collector bound to its root stage's main-agent output instead of running exactly
// once.
func isWatcherTrigger(trigger assistants.Trigger) bool {
	return trigger == assistants.TriggerCheckpoints || trigger == assistants.TriggerOnCompletion
}

// watcherBinding is the live, IN-MEMORY-ONLY state for one attached watcher task. Nothing here is itself the
// durable record of anything FSM-relevant: every actual Task status transition still commits through the
// existing state.Operation/Effect journal exactly as for any other task (finishTaskFrom/maybeScheduleTask,
// unchanged). The collector's own "how much has accumulated since the last fired checkpoint" is a pure
// scheduling aid — like a retry backoff timer, safe to lose across a crash (see bindWatcher's restart
// handling): the next observations simply start a fresh pending batch, and the durable
// watch.CheckpointStore's cursor (persisted under the task's own directory) is what actually prevents
// reprocessing an already-shown range.
type watcherBinding struct {
	taskID  assistants.TaskID
	stageID string
	task    assistants.Task
	coll    *watch.Collector
	store   *watch.CheckpointStore
}

// watchDirFor returns the durable checkpoint/cursor directory for one watcher task — a subdirectory of its
// existing taskDir namespace (assistants_files.go), not a new top-level location.
func (o *Orchestrator) watchDirFor(taskID assistants.TaskID) string {
	return filepath.Join(o.taskDir(taskID), "watch")
}

// bindWatcher creates (or, after a restart, RE-creates) the in-memory Collector/CheckpointStore pair for one
// watcher task and registers it for both stage-keyed observation fan-out and task-keyed poll/fire dispatch.
// Safe to call more than once for the same taskID (e.g. a redundant Attach replay) — it simply replaces the
// binding; the CheckpointStore underneath always points at the same durable directory regardless of how many
// times the in-memory Collector itself gets rebuilt.
func (o *Orchestrator) bindWatcher(taskID assistants.TaskID, task assistants.Task) {
	if !isWatcherTrigger(task.Trigger) {
		return
	}
	store := watch.NewCheckpointStore(o.watchDirFor(taskID))
	clock := o.watchClock
	if clock == nil {
		clock = watch.RealClock{}
	}
	coll := watch.NewCollector(watch.DefaultConfig(), clock, watch.IsMainAgentObservation)
	b := &watcherBinding{taskID: taskID, stageID: task.RootStageID, task: task, coll: coll, store: store}

	o.watchMu.Lock()
	defer o.watchMu.Unlock()
	if o.watchByStage == nil {
		o.watchByStage = map[string][]*watcherBinding{}
	}
	if o.watchByTask == nil {
		o.watchByTask = map[assistants.TaskID]*watcherBinding{}
	}
	if existing, ok := o.watchByTask[taskID]; ok {
		o.removeFromStageListLocked(existing)
	}
	o.watchByTask[taskID] = b
	o.watchByStage[task.RootStageID] = append(o.watchByStage[task.RootStageID], b)
}

// removeFromStageListLocked removes b from watchByStage[b.stageID]. Caller holds watchMu.
func (o *Orchestrator) removeFromStageListLocked(b *watcherBinding) {
	list := o.watchByStage[b.stageID]
	for i, x := range list {
		if x.taskID == b.taskID {
			o.watchByStage[b.stageID] = append(list[:i], list[i+1:]...)
			return
		}
	}
}

// unbindWatcher permanently removes a terminal task's binding — called once its status is known to be
// terminal (settleWatcherBinding), so the poller stops iterating a task that will never fire again.
func (o *Orchestrator) unbindWatcher(taskID assistants.TaskID) {
	o.watchMu.Lock()
	defer o.watchMu.Unlock()
	b, ok := o.watchByTask[taskID]
	if !ok {
		return
	}
	delete(o.watchByTask, taskID)
	o.removeFromStageListLocked(b)
}

// feedWatchCollectors fans out one Observation to every watcher Collector currently bound to stageID — called
// synchronously from observationSink for every normalized Observation (including incomplete ones; Collector
// itself drops those, see watch.Collector.Observe).
func (o *Orchestrator) feedWatchCollectors(stageID string, obs executor.Observation) {
	o.watchMu.Lock()
	bindings := append([]*watcherBinding{}, o.watchByStage[stageID]...)
	o.watchMu.Unlock()
	for _, b := range bindings {
		b.coll.Observe(obs)
	}
}

// settleWatcherBinding is called once, from runHelperTask's existing top-level defer, after EVERY exit path of
// a watcher task's turn (success→watching, success→completed, failed, cancelled, or requeued). A terminal
// outcome drops the binding entirely; a non-terminal one (watching, or requeued back to queued) simply clears
// "busy" so the collector can fire again.
func (o *Orchestrator) settleWatcherBinding(taskID assistants.TaskID) {
	o.watchMu.Lock()
	b, ok := o.watchByTask[taskID]
	o.watchMu.Unlock()
	if !ok {
		return
	}
	if o.taskTerminal(taskID) {
		o.unbindWatcher(taskID)
		return
	}
	b.coll.SetBusy(false)
}

// startWatchPoller launches the goroutine that periodically asks every bound watcher's Collector "are you
// ready for your next turn" — mirrors dialog_poller.go's startQuestionPoller cadence/shape. Registered with
// concurrency.Manager's SpawnDetached so it participates in clean shutdown's WaitAgents drain like every other
// background producer in this run.
func (o *Orchestrator) startWatchPoller(ctx context.Context) {
	interval := o.watchPollInterval
	if interval <= 0 {
		interval = defaultWatchPollInterval
	}
	o.concurrency.SpawnDetached(ctx, func(ctx context.Context) {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				o.pollWatchersOnce(ctx)
			}
		}
	})
}

// pollWatchersOnce checks every currently-bound watcher once.
func (o *Orchestrator) pollWatchersOnce(ctx context.Context) {
	o.watchMu.Lock()
	bindings := make([]*watcherBinding, 0, len(o.watchByTask))
	for _, b := range o.watchByTask {
		bindings = append(bindings, b)
	}
	o.watchMu.Unlock()
	for _, b := range bindings {
		o.maybeFireWatcher(ctx, b)
	}
}

// maybeFireWatcher asks b's Collector whether a new turn is due and, if so, durably commits the checkpoint
// (T5.2's "context durable before reference committed" — done HERE, before the fire, not inside runHelperTask)
// and then fires the task's next turn through the ordinary task lifecycle (fireWatcherTask).
func (o *Orchestrator) maybeFireWatcher(ctx context.Context, b *watcherBinding) {
	bundle, ready := b.coll.Ready()
	if !ready {
		return
	}
	o.commitAndFire(ctx, b, bundle)
}

// commitAndFire durably commits bundle to b.store and, only on success, fires the task. A storage failure
// returns the bundle to the collector's pending queue (watch.Collector.Cancel) instead of losing it or
// silently "spending" a pass — mirrors T5.2's TestCheckpoint_MissingLogMeansNotVerified posture: a checkpoint
// that was never durably written is never treated as processed.
func (o *Orchestrator) commitAndFire(ctx context.Context, b *watcherBinding, bundle watch.Bundle) {
	content := watch.RenderBundle(bundle)
	if err := b.store.Commit(watch.Checkpoint{UpToSeq: bundle.UpToSeq, Content: content}); err != nil {
		b.coll.Cancel(bundle)
		return
	}
	if bundle.Final {
		o.taskFinalPass.Store(b.taskID, true)
	}
	o.fireWatcherTask(ctx, b.taskID)
}

// fireWatcherTask transitions a resting "watching" task back to "queued" — the SAME status maybeScheduleTask
// already knows how to pick up and spawn (spawnTask/runHelperTask, unchanged). Guarded by the EXISTING
// Preconditions CAS: only succeeds if the task is CURRENTLY "watching"; any race (a concurrent Cancel/Pause,
// or a duplicate fire attempt — T5.5's dedup concern) makes this a safe, silent no-op, exactly like every
// other task-lifecycle transition in this codebase.
func (o *Orchestrator) fireWatcherTask(ctx context.Context, taskID assistants.TaskID) {
	eff, err := taskStatusEffect(taskID, assistants.StatusQueued, "checkpoint reached")
	if err != nil {
		return
	}
	op := state.Operation{
		ID:      "task-watch-fire:" + string(taskID) + ":" + randomOpSuffix(),
		Kind:    "fire_watch_task",
		Expect:  state.Preconditions{TaskStatus: map[string]assistants.TaskStatus{string(taskID): assistants.StatusWatching}},
		Effects: []state.Effect{eff},
	}
	runCtx := o.runContext(ctx)
	_, err = o.opts.Store.ApplyRunOperation(op, func(state.OperationResult) {
		o.publishNotice(bus.Event{Type: bus.EventAssistantTaskStatus, StageID: o.taskRootStage(taskID), Data: map[string]any{
			"task_id": string(taskID), "status": string(assistants.StatusQueued), "reason": "checkpoint reached",
		}})
		o.maybeScheduleTask(runCtx, taskID)
	})
	o.handleTaskOperationErr(err)
}

// finalizeWatchersForStage is the single funnel (called from triggerWithSeq, orchestrator.go) for "this
// stage's own result is now known (or now gated on required assistants)" — regardless of which of the many
// EvFail/EvComplete/EvAwaitAssistants call sites produced it. T5.4's two remaining trigger rules live here:
//   - EvFail/EvVerifyFail: TestTrigger_ParentFailedStopsNewPasses — stop issuing new passes; whatever the
//     watcher already recorded (its own result.json / task_result events from earlier passes) is untouched.
//   - EvComplete/EvAwaitAssistants: TestTrigger_OnCompletionRunsAtStageResultReady /
//     TestTrigger_RequiredWatcherHasFinalBoundedPass — an on_completion watcher gets its one turn now; a
//     required watcher (any trigger) is guaranteed one final, bounded pass rather than being left "watching"
//     forever. T7.2 moved the moment this fires from "after the stage is already Done" to "the instant the
//     main executor's own part is done" (EvAwaitAssistants when the barrier is open, EvComplete when it
//     wasn't) — the whole point of the barrier is that this final pass's own verdict must still be able to
//     gate completion, which is impossible if it only ever runs once the stage is already marked done.
func (o *Orchestrator) finalizeWatchersForStage(stageID string, ev bus.FSMEvent) {
	o.watchMu.Lock()
	bindings := append([]*watcherBinding{}, o.watchByStage[stageID]...)
	o.watchMu.Unlock()
	for _, b := range bindings {
		switch ev {
		case bus.EvFail, bus.EvVerifyFail:
			b.coll.Stop()
		case bus.EvComplete, bus.EvAwaitAssistants:
			o.finalizeWatcherOnCompletion(context.Background(), b)
		default:
			// Unreachable: triggerWithSeq only calls this for EvComplete/EvFail/EvVerifyFail/EvAwaitAssistants
			// (see its own call site's doc comment) — fail closed (do nothing) rather than silently assume a
			// new caller's intent.
		}
	}
}

// finalizeWatcherOnCompletion forces b's bounded final pass if required by T5.4's rule (see
// finalizeWatchersForStage) and the task is not already terminal/mid-turn in a way that already covers it.
func (o *Orchestrator) finalizeWatcherOnCompletion(ctx context.Context, b *watcherBinding) {
	if o.taskTerminal(b.taskID) {
		return
	}
	if b.task.Trigger != assistants.TriggerOnCompletion && b.task.Mode != assistants.ModeRequired {
		// A plain advisory checkpoints-triggered watcher has no guaranteed final word — it simply stops
		// receiving NEW passes (handled the same way an EvFail would, via a Stop() on the next poll finding
		// nothing left to do) once its stage is done; nothing forces one more turn here.
		return
	}
	o.taskFinalPass.Store(b.taskID, true)
	status := o.currentTaskStatus(b.taskID)
	if status == assistants.StatusWatching {
		o.fireWatcherTask(o.runContext(ctx), b.taskID)
	}
	// If currently "running" or "queued", that in-flight/queued turn's own completion will consult
	// taskFinalPass (LoadAndDelete) and land in "completed" instead of "watching" — no extra fire needed here.
}

// currentTaskStatus reads a task's current projected status — "" for an unknown task id.
func (o *Orchestrator) currentTaskStatus(taskID assistants.TaskID) assistants.TaskStatus {
	rs := o.opts.Store.Snapshot()
	t, ok := rs.Assistants.Tasks[taskID]
	if !ok {
		return ""
	}
	return t.Status
}

// WatcherStatus reports a watcher's own, in-memory-only progress (T5.6: the dashboard's AssistantTaskView
// wants to show trigger kind + pass count + last-processed-checkpoint alongside the already-durable Trigger
// field). ok is false for a non-watcher task (isWatcherTrigger==false, never bound at all) or an unknown
// taskID — nothing to show, same posture as taskRootStage for an unknown id. passes is Collector.Passes() — 0
// before the first fired checkpoint. hasCheckpoint/lastCheckpointSeq mirror CheckpointStore.Cursor() exactly:
// hasCheckpoint is false until the FIRST checkpoint is durably committed (never a fabricated position — same
// "missing log means not verified" contract T5.2 gives every other reader of this store).
func (o *Orchestrator) WatcherStatus(taskID assistants.TaskID) (passes int, lastCheckpointSeq int, hasCheckpoint bool, ok bool) {
	o.watchMu.Lock()
	b, bound := o.watchByTask[taskID]
	o.watchMu.Unlock()
	if !bound {
		return 0, 0, false, false
	}
	lastCheckpointSeq, hasCheckpoint = b.store.Cursor()
	return b.coll.Passes(), lastCheckpointSeq, hasCheckpoint, true
}
