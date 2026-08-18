package orchestrator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/state"
)

// hookMaxRetries/hookRetryBackoff — fixed retry policy for script_before/
// script_after/script-stage failures, deliberately separate from
// runWithRetry's 15x/5s LLM rate-limit backoff (retry.go:56-63): these
// failures are deterministic shell-command errors, not rate limits, so a
// short fixed schedule is the right fit.
const hookMaxRetries = 3

var hookRetryBackoff = []time.Duration{1 * time.Second, 2 * time.Second, 3 * time.Second}

// runScriptWithRetry runs fn up to hookMaxRetries+1 times (1 initial attempt
// + up to 3 retries), waiting hookRetryBackoff[attempt] between attempts.
// Returns the last error if every attempt fails, or ctx.Err() if cancelled
// while waiting between attempts.
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

// hookPending records which hook is currently blocked on a user decision, so
// a crash mid-wait can be resumed (recovery.go) without losing which
// script/timeout to re-run.
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
	return os.WriteFile(hookPendingPath(stageDir), data, 0644)
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

// waitForHookDecision blocks until RetryHook/SkipHook resolves stageID, or
// ctx is cancelled (full-run shutdown — the stage resumes waiting on the
// next `afm run` via recovery.go). Only one waiter per stageID at a time.
func (o *Orchestrator) waitForHookDecision(ctx context.Context, stageID string) (hookDecision, bool) {
	return o.waitOnHookChan(ctx, stageID, o.registerHookWaiter(stageID))
}

// registerHookWaiter creates and registers the channel a hook decision will
// arrive on for stageID. Split out from waitForHookDecision so a caller (see
// runBeforeHook) can register the waiter BEFORE making the stage's
// hook_failed status observable (FSM transition + event publish) — otherwise
// a fast resolver (dashboard, automation hitting the HTTP API) could see
// hook_failed and call resolveHook in the narrow window before the waiter
// exists, which would silently drop the decision (resolveHook returns false
// with nobody to retry).
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

// execScript runs a shell script for stage s in its project root_dir,
// streaming output into logFile via progress.Logger and publishing one
// EventScriptOutput per line so the dashboard event feed can show it live.
// Deliberately bypasses runnerFor/executor.Runner: script stages need no
// per-phase LLM command routing, only Dir/StageDir plumbing.
func (o *Orchestrator) execScript(ctx context.Context, s flow.Stage, hook, script string, timeout time.Duration, logFile string) error {
	ex := executor.New(executor.Config{
		Command:     "sh",
		ExtraArgs:   []string{"-c", script},
		IdleTimeout: 24 * time.Hour, // effectively disabled; timeout below is the real bound
		Dir:         o.opts.RootDir,
		StageDir:    filepath.Join(o.opts.RunDir, s.ID),
		OnAction: func(_, line string) {
			data := map[string]string{"hook": hook, "line": line}
			o.ui.Publish(bus.Event{Type: bus.EventScriptOutput, StageID: s.ID, Data: data})
			// stagefiles.AppendNotice — тот же механизм, которым EventAgentCompleted/
			// EventContextWarning уже становятся durable+реплеиваемыми через
			// /api/events (см. stagefiles/notices.go, reconstructNotices). Без этого
			// клиент, подключившийся ПОСЛЕ завершения быстрого script/hook
			// (обычно <1с), никогда не увидит его вывод в ленте событий —
			// EventScriptOutput publish в o.ui эфемерен и не реплеится.
			stagefiles.AppendNotice(o.opts.RunDir, s.ID, string(bus.EventScriptOutput), data)
		},
	})
	return ex.RunScript(ctx, timeout, logFile)
}

// runBeforeHook runs s.ScriptBefore with retries; on exhaustion it blocks the
// stage in hook_failed until the user retries or skips via the dashboard.
// Returns true once the stage should proceed to its main content (hook
// succeeded or was skipped), false if ctx was cancelled while waiting for a
// decision (full-run shutdown — recovery.go resumes the wait on next start).
func (o *Orchestrator) runBeforeHook(ctx context.Context, s flow.Stage) bool {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	// Best-effort: runBeforeHook is the first thing to touch the stage
	// directory (mirrors the MkdirAll done by every other stage-content
	// entry point — agents.go, scheduling.go, recovery.go). If it genuinely
	// fails, execScript below will surface the real error through the normal
	// hook_failed retry path.
	_ = os.MkdirAll(stageDir, 0755)
	logFile := filepath.Join(stageDir, "before.log")

	for {
		err := runScriptWithRetry(ctx, func() error {
			return o.execScript(ctx, s, hookBefore, s.ScriptBefore, s.ScriptBeforeTimeout, logFile)
		})
		if err == nil {
			return true
		}

		// Register the waiter BEFORE the transition/event below make
		// hook_failed observable — closes the race where a fast resolver
		// (dashboard, automation) could call resolveHook before anyone is
		// listening (see registerHookWaiter's doc comment).
		waitCh := o.registerHookWaiter(s.ID)

		_ = writeHookPending(stageDir, hookPending{Hook: hookBefore, Script: s.ScriptBefore, Timeout: s.ScriptBeforeTimeout})
		_, seq, _ := o.triggerWithSeq(s.ID, bus.EvHookFailed, bus.GuardCtx{}, err.Error())
		_ = o.critical.Publish(ctx, bus.Event{
			Type:    bus.EventHookFailed,
			StageID: s.ID,
			Data:    map[string]string{"hook": hookBefore, "error": err.Error()},
			Seq:     seq,
		})

		decision, ok := o.waitOnHookChan(ctx, s.ID, waitCh)
		if !ok {
			return false
		}
		clearHookPending(stageDir)
		_, seq, _ = o.triggerWithSeq(s.ID, bus.EvHookResolved, bus.GuardCtx{}, "before hook "+resolutionName(decision))
		o.ui.Publish(bus.Event{
			Type:    bus.EventHookResolved,
			StageID: s.ID,
			Data:    map[string]string{"hook": hookBefore, "resolution": resolutionName(decision)},
			Seq:     seq,
		})
		if decision == hookDecisionSkip {
			return true
		}
		// hookDecisionRetry: loop back and re-run the full 3x/1-2-3s cycle.
	}
}

// runAfterHook runs s.ScriptAfter after the stage has already completed
// successfully. Failure here does NOT touch the FSM — the stage stays done
// regardless (no Trigger/triggerWithSeq call anywhere in this function). It
// only surfaces a dismissable EventHookFailed notice with a retry/skip
// decision, mirroring runBeforeHook's UI but never blocking the stage's
// status.
func (o *Orchestrator) runAfterHook(ctx context.Context, s flow.Stage) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	// Best-effort, mirrors runBeforeHook: the stage directory may not exist
	// yet if this hook is the first thing to touch it. If MkdirAll genuinely
	// fails, execScript below surfaces the real error through the normal
	// hook_failed retry path.
	_ = os.MkdirAll(stageDir, 0755)
	logFile := filepath.Join(stageDir, "after.log")

	for {
		err := runScriptWithRetry(ctx, func() error {
			return o.execScript(ctx, s, hookAfter, s.ScriptAfter, s.ScriptAfterTimeout, logFile)
		})
		if err == nil {
			return
		}

		// Register the waiter BEFORE writing hook_pending.json/publishing
		// EventHookFailed below make the failure observable — closes the
		// same TOCTOU race documented on registerHookWaiter/runBeforeHook: a
		// fast resolver could otherwise call resolveHook before anyone is
		// listening and silently lose the decision.
		waitCh := o.registerHookWaiter(s.ID)

		_ = writeHookPending(stageDir, hookPending{Hook: hookAfter, Script: s.ScriptAfter, Timeout: s.ScriptAfterTimeout})
		o.ui.Publish(bus.Event{
			Type:    bus.EventHookFailed,
			StageID: s.ID,
			Data:    map[string]string{"hook": hookAfter, "error": err.Error()},
		})

		decision, ok := o.waitOnHookChan(ctx, s.ID, waitCh)
		if !ok {
			return
		}
		clearHookPending(stageDir)
		o.ui.Publish(bus.Event{
			Type:    bus.EventHookResolved,
			StageID: s.ID,
			Data:    map[string]string{"hook": hookAfter, "resolution": resolutionName(decision)},
		})
		if decision == hookDecisionSkip {
			return
		}
		// retry: loop back and re-run the full cycle.
	}
}

// maybeRunAfterHook fires the stage's script_after hook (if any) in a tracked
// goroutine via concurrency.Manager.SpawnAgent, reusing its semaphore/agentWG
// bookkeeping — the hook may block for an arbitrarily long time waiting on a
// user decision, so it must never run inline in its callers
// (onAgentCompleted/approveStage — event-loop callbacks that must return
// promptly). A no-op when the stage has no ScriptAfter: no goroutine is
// spawned, so a stage without the hook behaves unchanged.
//
// pendingAfterHooks is incremented here (synchronously, before SpawnAgent
// returns to the caller — completeStage/approveStage, both running on Run()'s
// own goroutine) and decremented from the wrapper below once runAfterHook
// actually returns, so shouldExit() (scheduling.go) never observes a stage
// as fully done while its after-hook is still in flight or awaiting a
// RetryHook/SkipHook decision. Scoped to just this one spawn — see
// SpawnAgent's doc comment on why the general agent-spawn path doesn't carry
// this bookkeeping too.
func (o *Orchestrator) maybeRunAfterHook(ctx context.Context, stageID string) {
	stage := o.graph.Stage(stageID)
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

// withBeforeHook wraps a stage's fresh-activation run function with
// script_before: if the stage has no ScriptBefore, mainFn runs unchanged. If
// the hook fails and is blocked in hook_failed, ctx cancellation during the
// wait (full shutdown) skips mainFn entirely — the stage resumes via
// recovery.go on the next `afm run`.
//
// Used only at genuine "fresh activation" spawn sites (startReadyStages,
// retryStage's non-planning branches, recovery.go's startPlanningForPending)
// — NOT at resumeInteractiveAgent (already past its before-hook) or at
// Revise/onUserAnswered/onAgentCompleted's revising branch (mid-flight
// continuations of an already-hooked run).
func (o *Orchestrator) withBeforeHook(mainFn func(context.Context, flow.Stage)) func(context.Context, flow.Stage) {
	return func(ctx context.Context, s flow.Stage) {
		if s.ScriptBefore != "" {
			if !o.runBeforeHook(ctx, s) {
				return
			}
		}
		// script_before runs as a bare shell script with no InterruptCh (only
		// the main agent gets one, inside runWithRetry) — so a manual Pause()
		// can succeed (durable EvPause transition) while the hook is still
		// executing in the background, unnoticed. Without this check we'd
		// spawn mainFn on a stage the user just paused — and a second time
		// again if they'd already clicked Continue in the meantime (Continue
		// spawns independently via resumeStageAtStatus).
		if o.currentStatus(s.ID) == state.StatusPaused {
			return
		}
		mainFn(ctx, s)
	}
}

// resumeHookFailedWait resumes a stage that crashed while blocked in
// hook_failed: re-reads the persisted hook_pending.json (written by
// runBeforeHook before blocking) and re-enters the wait for a user decision,
// WITHOUT silently re-attempting the hook — matching runBeforeHook's own
// retry-decision loop from that point on. Called from recovery.go via
// concurrency.Manager.SpawnAgent, so it's tracked the same way as any other
// resumed agent.
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
	o.ui.Publish(bus.Event{
		Type:    bus.EventHookResolved,
		StageID: s.ID,
		Data:    map[string]string{"hook": hookBefore, "resolution": resolutionName(decision)},
		Seq:     seq,
	})
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

// dispatchMainAfterBeforeHook runs a stage's real content after its
// before-hook resolved during a resume (script/autonomous/implementation),
// mirroring the branch startReadyStages/retryStage use for a fresh
// activation (withBeforeHook(mainFn) — not reused directly here since the
// hook already failed once and is being resumed mid-wait, not started fresh).
// The autonomous check matches startReadyStages exactly: isAutonomousStage
// (autonomous.flag on disk) catches a stage the supervisor dynamically
// routed to the autonomous track, not just one hard-coded via IsAuto()
// (agents: [auto]) — either way its before-hook ran ahead of runAutonomousAgent,
// never runImplementationAgent.
func (o *Orchestrator) dispatchMainAfterBeforeHook(ctx context.Context, s flow.Stage) {
	switch {
	case s.IsScript():
		o.runScriptStage(ctx, s)
	case isAutonomousStage(filepath.Join(o.opts.RunDir, s.ID)) || s.IsAuto():
		o.runAutonomousAgent(ctx, s)
	default:
		o.runImplementationAgent(ctx, s)
	}
}

// resumeAfterHookWait resumes a stage that crashed while its script_after
// hook was blocked on a retry/skip decision. Unlike script_before's
// hook_failed, a pending after-hook decision leaves NO trace in the FSM —
// runAfterHook never calls Trigger, so the stage stays StatusDone straight
// through the crash. recovery.go finds it only by scanning every stage's
// hook_pending.json for Hook == "after", regardless of status, and resumes
// it via resumeAfterHook below (not called directly — see its doc comment).
// Mirrors resumeHookFailedWait: re-enters the wait without re-running the
// hook, and re-runs the full retry cycle only if the user asks to retry.
func (o *Orchestrator) resumeAfterHookWait(ctx context.Context, s flow.Stage) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	decision, ok := o.waitForHookDecision(ctx, s.ID)
	if !ok {
		// ctx cancelled (full shutdown) — hook_pending.json is left on disk
		// and this same scan resumes the wait again on the next `afm run`.
		return
	}
	clearHookPending(stageDir)
	o.ui.Publish(bus.Event{
		Type:    bus.EventHookResolved,
		StageID: s.ID,
		Data:    map[string]string{"hook": hookAfter, "resolution": resolutionName(decision)},
	})
	if decision == hookDecisionSkip {
		return
	}
	// Retry: re-enter the normal after-hook cycle from scratch.
	o.runAfterHook(ctx, s)
}

// resumeAfterHook wraps resumeAfterHookWait with the same pendingAfterHooks
// bookkeeping maybeRunAfterHook uses: incremented synchronously here, BEFORE
// SpawnAgent returns to the caller (recovery.go, running on Run()'s own
// goroutine), so shouldExit() can never observe zero in-flight after-hooks
// in the narrow window between spawning this goroutine and the goroutine's
// own first instruction — see maybeRunAfterHook's doc comment for the full
// race this closes.
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
