package orchestrator

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/lifecyclehooks"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// hookMaxRetries/hookRetryBackoff define short shell retries, independent of LLM rate-limit backoff.
const hookMaxRetries = 3

var hookRetryBackoff = []time.Duration{1 * time.Second, 2 * time.Second, 3 * time.Second}

// stderrTailMaxLines/stderrTailMaxBytes bound stderr excerpts for script and hook failure notices.
const (
	stderrTailMaxLines = 20
	stderrTailMaxBytes = 4096
)

// dataKeyStderrTail identifies stderr excerpts in script_failed and hook_failed notices.
const dataKeyStderrTail = "stderr_tail"

// dataKeySeq carries EvFail's sequence to distinguish separate script_failed notices during frontend
// deduplication.
const dataKeySeq = "seq"

// runScriptWithRetry runs fn up to hookMaxRetries+1 times, waiting hookRetryBackoff[attempt] between
// attempts.
func runScriptWithRetry(ctx context.Context, fn func() error) error {
	var lastErr error
	for attempt := 0; attempt <= hookMaxRetries; attempt++ {
		lastErr = fn()
		if lastErr == nil {
			return nil
		}
		if attempt < hookMaxRetries {
			select {
			case <-time.After(hookRetryBackoff[attempt]):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	return lastErr
}

// hookBefore/hookAfter — the two valid hookPending.Hook values.
const (
	hookBefore = "before"
	hookAfter  = "after"
)

// hookPending records which hook is currently blocked on a user decision, so a crash mid-wait can be
// resumed (recovery.go) without losing which script/timeout to re-run.
type hookPending struct {
	Hook    string        `json:"hook"` // hookBefore or hookAfter
	Script  string        `json:"script"`
	Timeout time.Duration `json:"timeout"`
}

func hookPendingPath(stageDir string) string {
	return filepath.Join(stageDir, "hook_pending.json")
}

func writeHookPending(stageDir string, p hookPending) error {
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	// Durable temp+fsync+rename сохраняет данные Retry/Skip для recovery; after-hook не имеет отдельного
	// FSM-перехода.
	path := hookPendingPath(stageDir)
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if dir, derr := os.Open(stageDir); derr == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

func readHookPending(stageDir string) (hookPending, bool) {
	data, err := os.ReadFile(hookPendingPath(stageDir))
	if err != nil {
		return hookPending{}, false
	}
	var p hookPending
	if json.Unmarshal(data, &p) != nil {
		return hookPending{}, false
	}
	return p, true
}

func clearHookPending(stageDir string) {
	_ = os.Remove(hookPendingPath(stageDir))
}

// hookDecision is the user's response to a hook_failed notice.
type hookDecision int

const (
	hookDecisionRetry hookDecision = iota
	hookDecisionSkip
)

// waitForHookDecision blocks until RetryHook/SkipHook resolves stageID, or ctx is cancelled. Only one
// waiter per stageID at a time.
func (o *Orchestrator) waitForHookDecision(ctx context.Context, stageID string) (hookDecision, bool) {
	return o.waitOnHookChan(ctx, stageID, o.registerHookWaiter(stageID))
}

// registerHookWaiter creates and registers the channel a hook decision will arrive on for stageID.
func (o *Orchestrator) registerHookWaiter(stageID string) chan hookDecision {
	ch := make(chan hookDecision, 1)
	o.hookWaiters.Store(stageID, ch)
	return ch
}

// waitOnHookChan blocks on a channel already registered via
// registerHookWaiter, until a decision arrives or ctx is cancelled.
func (o *Orchestrator) waitOnHookChan(ctx context.Context, stageID string, ch chan hookDecision) (hookDecision, bool) {
	defer o.hookWaiters.Delete(stageID)
	select {
	case d := <-ch:
		return d, true
	case <-ctx.Done():
		return 0, false
	}
}

// execScript runs in root_dir, streams its log, and publishes EventScriptOutput per line without LLM
// command routing.
func (o *Orchestrator) execScript(ctx context.Context, s flow.Stage, hook, script string, timeout time.Duration, logFile string) error {
	ex := executor.New(executor.Config{
		Command:     "sh",
		ExtraArgs:   []string{"-c", script},
		IdleTimeout: 24 * time.Hour, // effectively disabled; timeout below is the real bound
		Dir:         o.opts.RootDir,
		StageDir:    filepath.Join(o.opts.RunDir, s.ID),
		OnAction: func(stream, line string) {
			data := map[string]string{"hook": hook, "line": line, "stream": stream}
			o.publishNotice(bus.Event{Type: bus.EventScriptOutput, StageID: s.ID, Data: data})
		},
	})
	return ex.RunScript(ctx, timeout, logFile)
}

// publishHookNotice publishes a hook_failed/hook_resolved event live to the UI bus AND mirrors it to
// notices.jsonl so it survives a reload/reconnect.
func (o *Orchestrator) publishHookNotice(stageID string, evType bus.EventType, seq uint64, data map[string]string) {
	o.publishNotice(bus.Event{Type: evType, StageID: stageID, Data: data, Seq: seq})
}

// runBeforeHook runs s.ScriptBefore with retries; on exhaustion it blocks the stage in hook_failed until
// the user retries or skips via the dashboard.
func (o *Orchestrator) runBeforeHook(ctx context.Context, s flow.Stage) bool {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	// Best-effort: runBeforeHook is the first thing to touch the stage directory. If it genuinely fails,
	// execScript below will surface the real error through the normal hook_failed retry path.
	_ = os.MkdirAll(stageDir, 0755)
	logFile := filepath.Join(stageDir, "before.log")

	for {
		// Публикуем started для каждой серии попыток; пользовательский Retry начинает новую серию.
		o.emitStageEvent(lifecyclehooks.EventStageScriptBeforeStarted, s.ID, "")
		err := runScriptWithRetry(ctx, func() error {
			return o.execScript(ctx, s, hookBefore, s.ScriptBefore, s.ScriptBeforeTimeout, logFile)
		})
		if err == nil {
			o.emitStageEvent(lifecyclehooks.EventStageScriptBeforeFinished, s.ID, "")
			return true
		}
		o.emitStageEvent(lifecyclehooks.EventStageScriptBeforeFailed, s.ID, err.Error())

		// Register the waiter BEFORE the transition/event below make hook_failed observable — closes the race
		// where a fast resolver could call resolveHook before anyone is listening.
		waitCh := o.registerHookWaiter(s.ID)

		if werr := writeHookPending(stageDir, hookPending{Hook: hookBefore, Script: s.ScriptBefore, Timeout: s.ScriptBeforeTimeout}); werr != nil {
			log.Printf("WARN: persist hook_pending (before) for %s: %v", s.ID, werr)
		}
		_, seq, _ := o.triggerWithSeq(s.ID, bus.EvHookFailed, bus.GuardCtx{}, err.Error())
		// o.ui, not o.critical: this is a dashboard notice, not an FSM-relevant signal — the transition already
		// happened via triggerWithSeq above.
		tail := executor.ReadStderrTail(logFile, stderrTailMaxLines, stderrTailMaxBytes)
		o.publishHookNotice(s.ID, bus.EventHookFailed, seq, map[string]string{"hook": hookBefore, "error": err.Error(), dataKeyStderrTail: tail})

		decision, ok := o.waitOnHookChan(ctx, s.ID, waitCh)
		if !ok {
			return false
		}
		clearHookPending(stageDir)
		_, seq, _ = o.triggerWithSeq(s.ID, bus.EvHookResolved, bus.GuardCtx{}, "before hook "+resolutionName(decision))
		o.publishHookNotice(s.ID, bus.EventHookResolved, seq, map[string]string{"hook": hookBefore, "resolution": resolutionName(decision)})
		if decision == hookDecisionSkip {
			return true
		}
		// hookDecisionRetry: loop back and re-run the full 3x/1-2-3s cycle.
	}
}

// runAfterHook runs s.ScriptAfter after the stage has already completed successfully. Intentionally
// cumulative; no per-attempt log segmentation.
func (o *Orchestrator) runAfterHook(ctx context.Context, s flow.Stage) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	// Best-effort, mirrors runBeforeHook: the stage directory may not exist yet if this hook is the first
	// thing to touch it.
	_ = os.MkdirAll(stageDir, 0755)
	logFile := filepath.Join(stageDir, "after.log")

	for {
		// Публикуем started для каждой серии попыток; пользовательский Retry начинает новую серию.
		o.emitStageEvent(lifecyclehooks.EventStageScriptAfterStarted, s.ID, "")
		err := runScriptWithRetry(ctx, func() error {
			return o.execScript(ctx, s, hookAfter, s.ScriptAfter, s.ScriptAfterTimeout, logFile)
		})
		if err == nil {
			o.emitStageEvent(lifecyclehooks.EventStageScriptAfterFinished, s.ID, "")
			return
		}
		o.emitStageEvent(lifecyclehooks.EventStageScriptAfterFailed, s.ID, err.Error())

		// Register the waiter before persisting or publishing failure, so a fast user decision cannot be lost.
		waitCh := o.registerHookWaiter(s.ID)

		if werr := writeHookPending(stageDir, hookPending{Hook: hookAfter, Script: s.ScriptAfter, Timeout: s.ScriptAfterTimeout}); werr != nil {
			// Решение after-hook хранится только в pending-файле; ошибка записи лишает recovery данных Retry/Skip.
			log.Printf("WARN: persist hook_pending (after) for %s: %v", s.ID, werr)
		}
		tail := executor.ReadStderrTail(logFile, stderrTailMaxLines, stderrTailMaxBytes)
		o.publishHookNotice(s.ID, bus.EventHookFailed, 0, map[string]string{"hook": hookAfter, "error": err.Error(), dataKeyStderrTail: tail})

		decision, ok := o.waitOnHookChan(ctx, s.ID, waitCh)
		if !ok {
			return
		}
		clearHookPending(stageDir)
		o.publishHookNotice(s.ID, bus.EventHookResolved, 0, map[string]string{"hook": hookAfter, "resolution": resolutionName(decision)})
		if decision == hookDecisionSkip {
			return
		}
		// retry: loop back and re-run the full cycle.
	}
}

// maybeRunAfterHook spawns a tracked goroutine to keep the event loop responsive; increment
// pendingAfterHooks before spawning.
func (o *Orchestrator) maybeRunAfterHook(ctx context.Context, stageID string) {
	stage := o.reg().Stage(stageID)
	if stage == nil || stage.ScriptAfter == "" {
		return
	}
	o.pendingAfterHooks.Add(1)
	o.concurrency.SpawnAgent(ctx, *stage, func(ctx context.Context, s flow.Stage) {
		defer func() {
			o.pendingAfterHooks.Add(-1)
			o.concurrency.WakeEventLoop()
		}()
		o.runAfterHook(ctx, s)
	})
}

// maybeRunAfterHookThen runs script_after and then invokes cont — inside the SAME tracked goroutine, AFTER
// runAfterHook returns — so a consumer of cont sees after.log on disk.
func (o *Orchestrator) maybeRunAfterHookThen(ctx context.Context, stageID string, cont func(context.Context)) {
	stage := o.reg().Stage(stageID)
	if stage == nil || stage.ScriptAfter == "" {
		cont(ctx)
		return
	}
	o.pendingAfterHooks.Add(1)
	o.concurrency.SpawnAgent(ctx, *stage, func(ctx context.Context, s flow.Stage) {
		defer func() {
			o.pendingAfterHooks.Add(-1)
			o.concurrency.WakeEventLoop()
		}()
		o.runAfterHook(ctx, s)
		if ctx.Err() == nil {
			cont(ctx) // skip reflection on shutdown; retry/skip decisions already resolved in runAfterHook
		}
	})
}

// withBeforeHook wraps a stage's fresh-activation run function with script_before: if the stage has no
// ScriptBefore, mainFn runs unchanged.
func (o *Orchestrator) withBeforeHook(mainFn func(context.Context, flow.Stage)) func(context.Context, flow.Stage) {
	return func(ctx context.Context, s flow.Stage) {
		// Захватываем generation паузы ДО хука; любой Pause за время его
		// исполнения его изменит (см. поле pauseGen).
		gen := o.loadPauseGen(s.ID)
		if s.ScriptBefore != "" {
			if !o.runBeforeHook(ctx, s) {
				return
			}
		}
		// script_before runs as a bare shell script with no InterruptCh — so a manual Pause can succeed while
		// the hook is still executing in the background, unnoticed. The status check catches "paused and still
		// paused"; the generation check catches the ABA running->paused->running (Pause then Continue): status
		// reads running again, but Continue has ALREADY relaunched the agent, so running mainFn here would
		// double-launch. Any pause during the hook bumped pauseGen → we abdicate and let Continue own the relaunch.
		if o.loadPauseGen(s.ID) != gen || o.currentStatus(s.ID) == state.StatusPaused {
			return
		}
		mainFn(ctx, s)
	}
}

// resumeHookFailedWait restores the persisted Retry/Skip wait without rerunning the hook automatically.
func (o *Orchestrator) resumeHookFailedWait(ctx context.Context, s flow.Stage) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	pending, ok := readHookPending(stageDir)
	if !ok || pending.Hook != hookBefore {
		// Nothing to resume from disk; fail safe rather than guessing.
		o.Trigger(s.ID, bus.EvFail, bus.GuardCtx{}, "hook_failed with no pending hook on disk")
		return
	}
	decision, ok := o.waitForHookDecision(ctx, s.ID)
	if !ok {
		// ctx cancelled (full shutdown) — stage stays in hook_failed and
		// resumes again via this same path on the next `afm run`.
		return
	}
	clearHookPending(stageDir)
	_, seq, _ := o.triggerWithSeq(s.ID, bus.EvHookResolved, bus.GuardCtx{}, "before hook "+resolutionName(decision))
	o.publishHookNotice(s.ID, bus.EventHookResolved, seq, map[string]string{"hook": hookBefore, "resolution": resolutionName(decision)})
	if decision == hookDecisionSkip {
		o.dispatchMainAfterBeforeHook(ctx, s)
		return
	}
	// Retry: re-enter the normal before-hook flow from scratch (full
	// 3x/1-2-3s cycle); if it fails again it re-blocks in hook_failed itself.
	if o.runBeforeHook(ctx, s) {
		o.dispatchMainAfterBeforeHook(ctx, s)
	}
}

// dispatchMainAfterBeforeHook runs a stage's real content after its before-hook resolved during a resume,
// mirroring the branch startReadyStages/retryStage use for a fresh activation.
func (o *Orchestrator) dispatchMainAfterBeforeHook(ctx context.Context, s flow.Stage) {
	if s.IsScript() {
		o.runScriptStage(ctx, s)
		return
	}
	o.plainRunner(o.executionKind(s))(ctx, s)
}

// resumeAfterHookWait resumes a stage that crashed while its script_after hook was blocked on a retry/skip
// decision.
func (o *Orchestrator) resumeAfterHookWait(ctx context.Context, s flow.Stage) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	decision, ok := o.waitForHookDecision(ctx, s.ID)
	if !ok {
		// ctx cancelled (full shutdown) — hook_pending.json is left on disk
		// and this same scan resumes the wait again on the next `afm run`.
		return
	}
	clearHookPending(stageDir)
	o.publishHookNotice(s.ID, bus.EventHookResolved, 0, map[string]string{"hook": hookAfter, "resolution": resolutionName(decision)})
	if decision == hookDecisionSkip {
		return
	}
	// Retry: re-enter the normal after-hook cycle from scratch.
	o.runAfterHook(ctx, s)
}

// resumeAfterHook increments pendingAfterHooks before spawning so shouldExit cannot miss a queued hook.
func (o *Orchestrator) resumeAfterHook(ctx context.Context, s flow.Stage) {
	o.pendingAfterHooks.Add(1)
	o.concurrency.SpawnAgent(ctx, s, func(ctx context.Context, s flow.Stage) {
		defer func() {
			o.pendingAfterHooks.Add(-1)
			o.concurrency.WakeEventLoop()
		}()
		o.resumeAfterHookWait(ctx, s)
	})
}

func resolutionName(d hookDecision) string {
	if d == hookDecisionSkip {
		return "skipped"
	}
	return "retried"
}

// resolveHook delivers a user decision to a stage currently blocked in
// waitForHookDecision. Returns false if no stage is waiting.
func (o *Orchestrator) resolveHook(stageID string, d hookDecision) bool {
	v, ok := o.hookWaiters.Load(stageID)
	if !ok {
		return false
	}
	ch, ok := v.(chan hookDecision)
	if !ok {
		return false
	}
	select {
	case ch <- d:
		return true
	default:
		return false
	}
}
