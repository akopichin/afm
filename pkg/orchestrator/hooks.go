package orchestrator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
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

// hookPending records which hook is currently blocked on a user decision, so
// a crash mid-wait can be resumed (recovery.go) without losing which
// script/timeout to re-run.
type hookPending struct {
	Hook    string        `json:"hook"` // "before" or "after"
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
			o.ui.Publish(Event{
				Type:    EventScriptOutput,
				StageID: s.ID,
				Data:    map[string]string{"hook": hook, "line": line},
			})
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
			return o.execScript(ctx, s, "before", s.ScriptBefore, s.ScriptBeforeTimeout, logFile)
		})
		if err == nil {
			return true
		}

		// Register the waiter BEFORE the transition/event below make
		// hook_failed observable — closes the race where a fast resolver
		// (dashboard, automation) could call resolveHook before anyone is
		// listening (see registerHookWaiter's doc comment).
		waitCh := o.registerHookWaiter(s.ID)

		_ = writeHookPending(stageDir, hookPending{Hook: "before", Script: s.ScriptBefore, Timeout: s.ScriptBeforeTimeout})
		_, seq, _ := o.triggerWithSeq(s.ID, EvHookFailed, GuardCtx{}, err.Error())
		_ = o.critical.Publish(ctx, Event{
			Type:    EventHookFailed,
			StageID: s.ID,
			Data:    map[string]string{"hook": "before", "error": err.Error()},
			Seq:     seq,
		})

		decision, ok := o.waitOnHookChan(ctx, s.ID, waitCh)
		if !ok {
			return false
		}
		clearHookPending(stageDir)
		_, seq, _ = o.triggerWithSeq(s.ID, EvHookResolved, GuardCtx{}, "before hook "+resolutionName(decision))
		o.ui.Publish(Event{
			Type:    EventHookResolved,
			StageID: s.ID,
			Data:    map[string]string{"hook": "before", "resolution": resolutionName(decision)},
			Seq:     seq,
		})
		if decision == hookDecisionSkip {
			return true
		}
		// hookDecisionRetry: loop back and re-run the full 3x/1-2-3s cycle.
	}
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
