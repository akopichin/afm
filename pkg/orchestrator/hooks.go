package orchestrator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
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
	ch := make(chan hookDecision, 1)
	o.hookWaiters.Store(stageID, ch)
	defer o.hookWaiters.Delete(stageID)
	select {
	case d := <-ch:
		return d, true
	case <-ctx.Done():
		return 0, false
	}
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
