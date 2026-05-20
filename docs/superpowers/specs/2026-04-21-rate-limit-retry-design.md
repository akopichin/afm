# Rate Limit Retry & Stage Recovery

Date: 2026-04-21

## Problem

When Claude CLI returns a rate limit error ("You've hit your limit"), the stage transitions to `failed` — a terminal state with no recovery path. The entire pipeline stalls. The user must manually intervene with no tooling support.

Current state of the run `new-pipeline-20260421-142114`: `init` stage failed with rate limit after 53 turns of productive work, blocking `implement`, `setup`, and `test`.

## Requirements

1. **Auto-detect rate limit errors** from agent output and retry with backoff (5s, 10s, 30s — 3 attempts).
2. **Continue from where the agent stopped** — inject context from logs into the retry prompt so the agent sees what was already done.
3. **Show a "Try Again" button** in the web UI when retries are exhausted.
4. **CLI command `flowmanager retry <stage-id>`** for manual retry of any failed stage.
5. **API endpoint `POST /api/stages/{id}/retry`** for the web button.

## Design

### 1. FSM: new status `retrying`

File: `pkg/orchestrator/fsm.go`

New status constant: `state.StatusRetrying = "retrying"`

Updated transitions:

```
running    → retrying    // rate limit detected
planning   → retrying    // rate limit during planning
retrying   → running     // retry started (implementation)
retrying   → planning    // retry started (planning)
retrying   → failed      // all retries exhausted
failed     → pending     // manual retry (CLI/web)
```

`IsTerminal` remains: `done || failed` — `retrying` is transient.

File: `pkg/state/state.go`

Add `RetryCount int` field to `StageState`. Reset to 0 on any non-retry status transition.

### 2. Rate limit detection

File: `pkg/orchestrator/orchestrator.go`

New function:

```go
func isRateLimitError(err error) bool {
    if err == nil {
        return false
    }
    msg := strings.ToLower(err.Error())
    patterns := []string{
        "hit your limit",
        "rate limit",
        "too many requests",
        "overloaded",
        "capacity",
    }
    for _, p := range patterns {
        if strings.Contains(msg, p) {
            return true
        }
    }
    return false
}
```

### 3. Retry loop

File: `pkg/orchestrator/orchestrator.go`

New method `runWithRetry(ctx, stage, phase, agentFn)`:

```go
var retryBackoff = []time.Duration{5 * time.Second, 10 * time.Second, 30 * time.Second}

func (o *Orchestrator) runWithRetry(ctx context.Context, s flow.Stage, phase string, agentFn func() error) {
    for attempt := 0; attempt <= len(retryBackoff); attempt++ {
        err := agentFn()
        if err == nil {
            return // success
        }

        if !isRateLimitError(err) {
            o.setStatus(s.ID, state.StatusFailed)
            return
        }

        if attempt < len(retryBackoff) {
            o.setStatus(s.ID, state.StatusRetrying)
            o.bus.Publish(Event{
                Type:   EventRetryScheduled,
                StageID: s.ID,
                Data:   fmt.Sprintf("attempt %d/%d in %v", attempt+1, len(retryBackoff), retryBackoff[attempt]),
            })
            select {
            case <-time.After(retryBackoff[attempt]):
            case <-ctx.Done():
                o.setStatus(s.ID, state.StatusFailed)
                return
            }
            // Re-enter the appropriate phase
            switch phase {
            case "planning":
                o.setStatus(s.ID, state.StatusPlanning)
            default:
                o.setStatus(s.ID, state.StatusRunning)
            }
        } else {
            // All retries exhausted
            o.setStatus(s.ID, state.StatusFailed)
            o.bus.Publish(Event{Type: EventRetryExhausted, StageID: s.ID})
        }
    }
}
```

**Context injection for continuation:** When retrying an implementation, read the last 200 lines from `implementation.log` and prepend to the prompt:

```
## Previously completed actions (resuming after interruption)

[last 200 lines of implementation.log]

Continue from where you left off. Do NOT redo work that is already done.
```

New helper `buildRetryContext(stageDir, phase string) string` reads the log and formats this section.

### 4. Events

File: `pkg/orchestrator/eventbus.go`

Two new event types:

- `EventRetryScheduled` — published when a retry is scheduled. Data: "attempt N/3 in Xs". Web UI shows countdown.
- `EventRetryExhausted` — published when all retries fail. Triggers "Try Again" button in web UI.
- `EventManualRetry` — published by `Retry()` method or API handler. Handled in `onManualRetry()`: transitions `failed → pending`, then starts planning or implementation depending on whether `plan.md` exists.

### 5. API endpoint

File: `pkg/server/server.go` — register new route.

File: `pkg/server/handlers.go` — new handler:

```
POST /api/stages/{id}/retry
```

Logic:
1. Load state, verify stage is `failed`.
2. Transition `failed → pending → planning/running` depending on whether the stage needs planning.
3. Publish `EventManualRetry` on the event bus.
4. Orchestrator picks up the event and starts the agent goroutine.

New field in Server: `retryFn func(stageID string)` — analogous to `approveFn` and `reviseFn`.

### 6. CLI command

File: `cmd/flowmanager/retry.go` — new cobra command:

```
flowmanager retry <stage-id>
```

Logic:
1. Find latest run directory.
2. Load state, verify stage exists and is `failed`.
3. Publish retry event, start orchestrator.
4. Exit with success once the stage is running.

### 7. Web UI changes

File: `pkg/web/app.js`

**Status `retrying`:** Show countdown badge "Повтор через Xс (попытка N/3)". Listen for `EventRetryScheduled` events to get the timer value.

**Status `failed` after `EventRetryExhausted`:** Show a red "Попробовать ещё раз" button in the detail panel. On click, `POST /api/stages/{id}/retry`, then update status.

File: `pkg/web/style.css`

- `.status-retrying` style with orange color and pulse animation.
- `.btn-retry` style for the retry button (red accent, hover effect).

## Files changed

| File | Change |
|------|--------|
| `pkg/state/state.go` | Add `StatusRetrying`, `RetryCount` field |
| `pkg/orchestrator/fsm.go` | Add `retrying` transitions |
| `pkg/orchestrator/orchestrator.go` | `isRateLimitError`, `runWithRetry`, `buildRetryContext`, update `runPlanningAgent`/`runImplementationAgent`/`runPlanningWithFeedback` to use retry wrapper, add `Retry()` method |
| `pkg/orchestrator/eventbus.go` | `EventRetryScheduled`, `EventRetryExhausted` |
| `pkg/server/server.go` | Register `/api/stages/{id}/retry` route |
| `pkg/server/handlers.go` | `handleRetry` handler |
| `cmd/flowmanager/retry.go` | New `retry` command |
| `cmd/flowmanager/run.go` | Wire `retryFn` in server setup |
| `pkg/web/app.js` | Retry button, retrying status handling |
| `pkg/web/style.css` | `.status-retrying`, `.btn-retry` styles |

## Not in scope

- Partial progress checkpointing (saving agent state mid-execution).
- Token budget tracking or pre-emptive rate limit avoidance.
- Retry count persistence across flowmanager restarts (retry counter resets on restart).
