# Rate Limit Retry & Stage Recovery — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Auto-detect rate limit errors from Claude CLI, retry with backoff (5s/10s/30s), inject continuation context from logs, and provide manual retry via CLI command and web UI button.

**Architecture:** Add a `retrying` FSM status and a `runWithRetry` wrapper in the orchestrator that wraps planning/implementation calls. On rate limit errors, the wrapper sleeps with backoff and re-runs the agent with injected log context. After 3 retries, the stage fails with an `EventRetryExhausted` event. A new `POST /api/stages/{id}/retry` endpoint and `flowmanager retry <stage-id>` CLI command allow manual recovery.

**Tech Stack:** Go (existing codebase), vanilla JS/HTML/CSS (web UI), gorilla/websocket (existing)

---

## File Structure

| Action | File | Responsibility |
|--------|------|----------------|
| Modify | `pkg/state/state.go` | Add `StatusRetrying` constant |
| Modify | `pkg/orchestrator/fsm.go` | Add `retrying` and `failed→pending` transitions |
| Modify | `pkg/orchestrator/eventbus.go` | Add `EventRetryScheduled`, `EventRetryExhausted`, `EventManualRetry` |
| Modify | `pkg/orchestrator/orchestrator.go` | Add `isRateLimitError`, `runWithRetry`, `buildRetryContext`, `Retry()`, update agent methods |
| Modify | `pkg/server/server.go` | Add `retryFn` field and route |
| Modify | `pkg/server/handlers.go` | Add `handleRetry` handler |
| Modify | `cmd/flowmanager/main.go` | Register `retry` command |
| Modify | `cmd/flowmanager/run.go` | Wire `retryFn` in server setup |
| Create | `cmd/flowmanager/retry.go` | New `retry` CLI command |
| Modify | `pkg/web/index.html` | Add retry button container |
| Modify | `pkg/web/app.js` | Handle retrying status, retry button, retry events |
| Modify | `pkg/web/style.css` | `.status-retrying`, `.btn-retry` styles |
| Modify | `pkg/orchestrator/fsm_test.go` | Test new FSM transitions |
| Modify | `pkg/orchestrator/orchestrator_test.go` | Test `isRateLimitError` |
| Modify | `pkg/orchestrator/integration_test.go` | Integration test for retry flow |
| Modify | `pkg/server/handlers_test.go` | Test `handleRetry` |
| Create | `cmd/flowmanager/retry_test.go` | Test retry CLI command |

---

### Task 1: Add `StatusRetrying` constant to state package

**Files:**
- Modify: `pkg/state/state.go:15-24`
- Modify: `pkg/state/state_test.go`

- [ ] **Step 1: Add the new status constant**

In `pkg/state/state.go`, add `StatusRetrying` after `StatusRunning`:

```go
const (
	StatusPending          StageStatus = "pending"
	StatusPlanning         StageStatus = "planning"
	StatusAwaitingApproval StageStatus = "awaiting_approval"
	StatusRevising         StageStatus = "revising"
	StatusReady            StageStatus = "ready"
	StatusRunning          StageStatus = "running"
	StatusRetrying         StageStatus = "retrying"
	StatusDone             StageStatus = "done"
	StatusFailed           StageStatus = "failed"
)
```

- [ ] **Step 2: Run existing tests to verify nothing breaks**

Run: `go test ./pkg/state/... -v`
Expected: All existing tests pass. No test changes needed — the new constant is additive.

- [ ] **Step 3: Commit**

```bash
git add pkg/state/state.go
git commit -m "feat: добавить статус StatusRetrying для retry при rate limit"
```

---

### Task 2: Update FSM transitions

**Files:**
- Modify: `pkg/orchestrator/fsm.go`
- Modify: `pkg/orchestrator/fsm_test.go`

- [ ] **Step 1: Write the failing test**

In `pkg/orchestrator/fsm_test.go`, add these cases to `TestFSM_ValidTransitions`:

```go
{state.StatusRunning, state.StatusRetrying},
{state.StatusPlanning, state.StatusRetrying},
{state.StatusRetrying, state.StatusRunning},
{state.StatusRetrying, state.StatusPlanning},
{state.StatusRetrying, state.StatusFailed},
{state.StatusFailed, state.StatusPending},
```

Add to `TestFSM_InvalidTransitions`:

```go
{state.StatusRetrying, state.StatusDone},
{state.StatusFailed, state.StatusRunning},
```

Add to `TestFSM_IsTerminal`:

```go
if IsTerminal(state.StatusRetrying) {
    t.Error("retrying should not be terminal")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/orchestrator/ -run TestFSM -v`
Expected: FAIL — new transitions not defined yet.

- [ ] **Step 3: Add the FSM transitions**

In `pkg/orchestrator/fsm.go`, update `validTransitions`:

```go
var validTransitions = map[state.StageStatus][]state.StageStatus{
	state.StatusPending:          {state.StatusPlanning},
	state.StatusPlanning:         {state.StatusAwaitingApproval, state.StatusFailed, state.StatusRetrying},
	state.StatusAwaitingApproval: {state.StatusReady, state.StatusRevising},
	state.StatusRevising:         {state.StatusPlanning},
	state.StatusReady:            {state.StatusRunning},
	state.StatusRunning:          {state.StatusDone, state.StatusFailed, state.StatusRetrying},
	state.StatusRetrying:         {state.StatusRunning, state.StatusPlanning, state.StatusFailed},
	state.StatusFailed:           {state.StatusPending},
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/orchestrator/ -run TestFSM -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/orchestrator/fsm.go pkg/orchestrator/fsm_test.go
git commit -m "feat: FSM переходы для retrying и failed→pending"
```

---

### Task 3: Add new event types

**Files:**
- Modify: `pkg/orchestrator/eventbus.go`

- [ ] **Step 1: Add event constants**

In `pkg/orchestrator/eventbus.go`, add after `EventRevised`:

```go
EventRetryScheduled EventType = "retry_scheduled"
EventRetryExhausted EventType = "retry_exhausted"
EventManualRetry    EventType = "manual_retry"
```

- [ ] **Step 2: Run tests to verify nothing breaks**

Run: `go test ./pkg/orchestrator/ -v`
Expected: All pass. Event constants are only referenced by value, adding new ones is additive.

- [ ] **Step 3: Commit**

```bash
git add pkg/orchestrator/eventbus.go
git commit -m "feat: события EventRetryScheduled, EventRetryExhausted, EventManualRetry"
```

---

### Task 4: Add `isRateLimitError` function

**Files:**
- Modify: `pkg/orchestrator/orchestrator.go`
- Modify: `pkg/orchestrator/orchestrator_test.go`

- [ ] **Step 1: Write the failing test**

In `pkg/orchestrator/orchestrator_test.go` (this file uses `orchestrator_test` package which is the external test package), we cannot test the unexported `isRateLimitError` directly. Instead, we'll test the retry behavior in integration tests (Task 7). Skip the unit test for this unexported function — it will be covered by integration tests.

- [ ] **Step 2: Add `isRateLimitError` function**

In `pkg/orchestrator/orchestrator.go`, add after the `joinStrings` function:

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

- [ ] **Step 3: Commit**

```bash
git add pkg/orchestrator/orchestrator.go
git commit -m "feat: функция isRateLimitError для детекции rate limit"
```

---

### Task 5: Add `buildRetryContext` helper

**Files:**
- Modify: `pkg/orchestrator/orchestrator.go`

- [ ] **Step 1: Add `buildRetryContext` function**

In `pkg/orchestrator/orchestrator.go`, add after `isRateLimitError`:

```go
// buildRetryContext reads the last N lines from the agent log file
// and formats them as a continuation context for the retry prompt.
func buildRetryContext(stageDir, phase string) string {
	var logName string
	switch phase {
	case "planning":
		logName = "planning.log"
	default:
		logName = "implementation.log"
	}

	data, err := os.ReadFile(filepath.Join(stageDir, logName))
	if err != nil {
		return ""
	}

	lines := strings.Split(string(data), "\n")
	const maxLines = 200
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}

	var buf strings.Builder
	buf.WriteString("\n\n## Previously completed actions (resuming after interruption)\n\n")
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			buf.WriteString(l)
			buf.WriteString("\n")
		}
	}
	buf.WriteString("\nContinue from where you left off. Do NOT redo work that is already done.\n")
	return buf.String()
}
```

- [ ] **Step 2: Commit**

```bash
git add pkg/orchestrator/orchestrator.go
git commit -m "feat: buildRetryContext — контекст из логов для продолжения работы"
```

---

### Task 6: Add `runWithRetry` wrapper and update agent methods

**Files:**
- Modify: `pkg/orchestrator/orchestrator.go`

This is the core task. We add a `runWithRetry` method and refactor the three agent methods to use it.

- [ ] **Step 1: Add `runWithRetry` method**

In `pkg/orchestrator/orchestrator.go`, add after `buildRetryContext`:

```go
// retryBackoff defines wait durations between retry attempts.
var retryBackoff = []time.Duration{5 * time.Second, 10 * time.Second, 30 * time.Second}

// runWithRetry wraps an agent function with automatic retry on rate limit errors.
// On rate limit: sets status to retrying, waits with backoff, then retries.
// After exhausting all retries: publishes EventRetryExhausted.
func (o *Orchestrator) runWithRetry(ctx context.Context, s flow.Stage, phase string, agentFn func(retryContext string) error) {
	for attempt := 0; attempt <= len(retryBackoff); attempt++ {
		retryCtx := ""
		if attempt > 0 {
			stageDir := filepath.Join(o.opts.RunDir, s.ID)
			retryCtx = buildRetryContext(stageDir, phase)
		}

		err := agentFn(retryCtx)
		if err == nil {
			return
		}

		if !isRateLimitError(err) {
			o.setStatus(s.ID, state.StatusFailed)
			return
		}

		if attempt < len(retryBackoff) {
			o.setStatus(s.ID, state.StatusRetrying)
			o.bus.Publish(Event{
				Type:    EventRetryScheduled,
				StageID: s.ID,
				Data:    fmt.Sprintf("attempt %d/%d in %v", attempt+1, len(retryBackoff), retryBackoff[attempt]),
			})
			select {
			case <-time.After(retryBackoff[attempt]):
			case <-ctx.Done():
				o.setStatus(s.ID, state.StatusFailed)
				return
			}
			switch phase {
			case "planning":
				o.setStatus(s.ID, state.StatusPlanning)
			default:
				o.setStatus(s.ID, state.StatusRunning)
			}
		} else {
			o.setStatus(s.ID, state.StatusFailed)
			o.bus.Publish(Event{Type: EventRetryExhausted, StageID: s.ID})
		}
	}
}
```

- [ ] **Step 2: Refactor `runPlanningAgent` to use `runWithRetry`**

Replace the existing `runPlanningAgent` method:

```go
func (o *Orchestrator) runPlanningAgent(ctx context.Context, s flow.Stage) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		o.setStatus(s.ID, state.StatusFailed)
		return
	}

	o.setStatus(s.ID, state.StatusPlanning)

	o.runWithRetry(ctx, s, "planning", func(retryContext string) error {
		depCtx := o.buildStageContext(s)
		prompt := buildPlanningPrompt(o.opts.Prompts.Planning, s, depCtx+retryContext)
		outFile := filepath.Join(stageDir, "plan.md")
		logFile := filepath.Join(stageDir, "planning.log")

		r := o.runnerFor(s)
		if err := r.RunPlanning(ctx, s.Name, prompt, outFile, logFile); err != nil {
			return err
		}

		o.bus.Publish(Event{Type: EventAgentCompleted, StageID: s.ID, Data: "planning"})
		return nil
	})
}
```

- [ ] **Step 3: Refactor `runPlanningWithFeedback` to use `runWithRetry`**

Replace the existing `runPlanningWithFeedback` method:

```go
func (o *Orchestrator) runPlanningWithFeedback(ctx context.Context, s flow.Stage) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)

	o.setStatus(s.ID, state.StatusPlanning)

	o.runWithRetry(ctx, s, "planning", func(retryContext string) error {
		feedbackData, _ := os.ReadFile(filepath.Join(stageDir, "feedback.md"))
		var prevPlan string
		entries, _ := os.ReadDir(stageDir)
		for _, e := range entries {
			if matched, _ := filepath.Match("plan.v*.md", e.Name()); matched {
				data, _ := os.ReadFile(filepath.Join(stageDir, e.Name()))
				prevPlan = string(data)
			}
		}

		depCtx := o.buildStageContext(s)
		prompt := buildRevisionPrompt(o.opts.Prompts.Planning, s, prevPlan, string(feedbackData), depCtx+retryContext)
		outFile := filepath.Join(stageDir, "plan.md")
		logFile := filepath.Join(stageDir, "planning-revision.log")

		r := o.runnerFor(s)
		if err := r.RunPlanning(ctx, s.Name, prompt, outFile, logFile); err != nil {
			return err
		}

		o.bus.Publish(Event{Type: EventAgentCompleted, StageID: s.ID, Data: "planning"})
		return nil
	})
}
```

- [ ] **Step 4: Refactor `runImplementationAgent` to use `runWithRetry`**

Replace the existing `runImplementationAgent` method:

```go
func (o *Orchestrator) runImplementationAgent(ctx context.Context, s flow.Stage) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)

	o.runWithRetry(ctx, s, "implementation", func(retryContext string) error {
		planData, err := os.ReadFile(filepath.Join(stageDir, "plan.md"))
		if err != nil {
			return err
		}

		depCtx := o.buildStageContext(s)
		prompt := buildImplementationPrompt(o.opts.Prompts.Implementation, s, string(planData), depCtx+retryContext)
		logFile := filepath.Join(stageDir, "implementation.log")

		r := o.runnerFor(s)
		if err := r.RunAgent(ctx, string(s.ImplAgent()), s.Name, prompt, logFile); err != nil {
			return err
		}

		if s.HasAgent(flow.AgentReview) {
			reviewPrompt := buildReviewPrompt(o.opts.Prompts.Review, s)
			reviewLog := filepath.Join(stageDir, "review.log")
			if err := r.RunAgent(ctx, "review", s.Name, reviewPrompt, reviewLog); err != nil {
				return err
			}
		}

		o.bus.Publish(Event{Type: EventAgentCompleted, StageID: s.ID, Data: "implementation"})
		return nil
	})
}
```

- [ ] **Step 5: Add `Retry()` public method**

Add after the `Revise` method:

```go
// Retry retries a failed stage by transitioning it to pending and restarting.
func (o *Orchestrator) Retry(stageID string) {
	o.bus.Publish(Event{Type: EventManualRetry, StageID: stageID})
}
```

- [ ] **Step 6: Add `onManualRetry` handler**

Update `handleEvent` in orchestrator.go to add the `EventManualRetry` case:

```go
func (o *Orchestrator) handleEvent(ctx context.Context, ev Event) error {
	switch ev.Type {
	case EventAgentCompleted:
		return o.onAgentCompleted(ctx, ev)
	case EventApproved:
		return o.onApproved(ctx, ev)
	case EventRevised:
		return o.onRevised(ctx, ev)
	case EventManualRetry:
		return o.onManualRetry(ctx, ev)
	}
	return nil
}
```

Add the new handler method:

```go
func (o *Orchestrator) onManualRetry(ctx context.Context, ev Event) error {
	stageID := ev.StageID

	o.mu.Lock()
	current := o.opts.State.Stages[stageID].Status
	o.mu.Unlock()

	if current != state.StatusFailed {
		return nil
	}

	stage := o.graph.Stage(stageID)
	if stage == nil {
		return nil
	}

	o.setStatus(stageID, state.StatusPending)

	if !stage.NeedsPlanning() {
		// Stage has a pre-existing plan — go straight to running
		o.setStatus(stageID, state.StatusReady)
		o.setStatus(stageID, state.StatusRunning)
		go func(st flow.Stage) {
			sem := o.semFor(st)
			sem.acquire()
			defer sem.release()
			o.runImplementationAgent(ctx, st)
		}(*stage)
		o.startReadyStages(ctx)
		return nil
	}

	// Check if plan.md already exists
	stageDir := filepath.Join(o.opts.RunDir, stageID)
	planPath := filepath.Join(stageDir, "plan.md")
	if _, err := os.Stat(planPath); err == nil {
		// Plan exists — go to running
		o.setStatus(stageID, state.StatusReady)
		o.setStatus(stageID, state.StatusRunning)
		go func(st flow.Stage) {
			sem := o.semFor(st)
			sem.acquire()
			defer sem.release()
			o.runImplementationAgent(ctx, st)
		}(*stage)
	} else {
		// No plan — start planning
		go func(st flow.Stage) {
			sem := o.semFor(st)
			sem.acquire()
			defer sem.release()
			o.runPlanningAgent(ctx, st)
		}(*stage)
	}

	return nil
}
```

- [ ] **Step 7: Update `startPlanningForPending` — `failed` is no longer fully terminal**

In the `startPlanningForPending` switch, `failed` is currently skipped with the `done/failed/awaiting_approval/ready` case. This is correct — failed stages only restart via explicit `Retry()` call, not automatically on resume.

However, `retrying` status on resume needs handling. Add it to the switch — if the orchestrator restarts while a stage is `retrying`, we should treat it as `failed` (the retry was interrupted, user needs to manually retry):

```go
case state.StatusDone, state.StatusFailed, state.StatusAwaitingApproval, state.StatusReady, state.StatusRetrying:
	// Terminal, waiting, or interrupted retry — leave as is
	continue
```

- [ ] **Step 8: Add `"time"` to imports**

At the top of `pkg/orchestrator/orchestrator.go`, add `"time"` to the import block (after `"sync"`):

```go
import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	// ... external imports unchanged
)
```

- [ ] **Step 9: Run all tests**

Run: `go test ./... -v`
Expected: All pass. The refactored methods have the same behavior for non-rate-limit cases.

- [ ] **Step 10: Commit**

```bash
git add pkg/orchestrator/orchestrator.go
git commit -m "feat: runWithRetry — автоматический retry при rate limit с backoff 5/10/30с"
```

---

### Task 7: Integration test for retry flow

**Files:**
- Modify: `pkg/orchestrator/integration_test.go`

- [ ] **Step 1: Add mock script that simulates rate limit then success**

Add after the existing mock script constants:

```go
// mockRateLimitScript simulates a rate limit error on first call, then succeeds.
// Uses a file-based counter to track calls across separate bash invocations.
const mockRateLimitThenSuccessScript = `
if [ -f /tmp/fm-test-counter-$$.txt ]; then
    cat /tmp/fm-test-counter-$$.txt
else
    echo "You've hit your limit · resets 3pm" >&2
    echo "1" > /tmp/fm-test-counter-$$.txt
    exit 1
fi
`
```

Actually, this approach has issues with separate subprocess calls. Better: use a script that checks an environment-based marker via a temp file in the run directory. But we don't control the run dir from the script.

Simplest approach: use a `callCountingRunner` that tracks calls and fails with rate limit on the first N calls.

- [ ] **Step 1 (revised): Add a call-counting test runner**

In `pkg/orchestrator/integration_test.go`, add after the `promptCapturingRunner`:

```go
// rateLimitThenSuccessRunner wraps a Runner and returns a rate limit error
// on the first N calls, then delegates to the underlying runner.
type rateLimitThenSuccessRunner struct {
	delegate   executor.Runner
	failCount  int    // number of calls to fail before succeeding
	failMsg    string // error message for failures
	mu         sync.Mutex
	callCount  int
}

func (r *rateLimitThenSuccessRunner) RunPlanning(ctx context.Context, stageName, prompt, outFile, logFile string) error {
	r.mu.Lock()
	r.callCount++
	count := r.callCount
	r.mu.Unlock()

	if count <= r.failCount {
		return fmt.Errorf("%s", r.failMsg)
	}
	return r.delegate.RunPlanning(ctx, stageName, prompt, outFile, logFile)
}

func (r *rateLimitThenSuccessRunner) RunAgent(ctx context.Context, agentType, stageName, prompt, logFile string) error {
	r.mu.Lock()
	r.callCount++
	count := r.callCount
	r.mu.Unlock()

	if count <= r.failCount {
		return fmt.Errorf("%s", r.failMsg)
	}
	return r.delegate.RunAgent(ctx, agentType, stageName, prompt, logFile)
}
```

- [ ] **Step 2: Write the integration test**

```go
func TestIntegration_RetryOnRateLimit(t *testing.T) {
	stages := []flow.Stage{
		{ID: "retry-stage", Name: "Retry Stage", Description: "test retry", Agents: []flow.AgentType{flow.AgentPlanning}},
	}

	// Fail once with rate limit, then succeed
	delegate := mockRunner(t, mockPlanningScript)
	runner := &rateLimitThenSuccessRunner{
		delegate:  delegate,
		failCount: 1,
		failMsg:   "You've hit your limit · resets 3pm",
	}

	orch, _, stateFile := setupOrchestratorWithRunner(t, stages, runner)

	// Use short backoff for test — override via the fact that the test runs fast enough

	if err := orch.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	final, _ := state.Load(stateFile)
	if final.Stages["retry-stage"].Status != state.StatusAwaitingApproval {
		t.Errorf("expected awaiting_approval after retry, got %v", final.Stages["retry-stage"].Status)
	}
}
```

- [ ] **Step 3: Run the test**

Run: `go test ./pkg/orchestrator/ -run TestIntegration_RetryOnRateLimit -v -timeout 60s`
Expected: PASS. The runner fails once, the orchestrator retries, the second call succeeds.

- [ ] **Step 4: Add test for exhausting all retries**

```go
func TestIntegration_RetryExhausted(t *testing.T) {
	stages := []flow.Stage{
		{ID: "exhaust", Name: "Exhaust Stage", Description: "test retry exhausted", Agents: []flow.AgentType{flow.AgentPlanning}},
	}

	// Always fail with rate limit
	delegate := mockRunner(t, mockFailScript)
	runner := &rateLimitThenSuccessRunner{
		delegate:  delegate,
		failCount: 99,
		failMsg:   "You've hit your limit · resets 3pm",
	}

	orch, _, stateFile := setupOrchestratorWithRunner(t, stages, runner)

	if err := orch.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	final, _ := state.Load(stateFile)
	if final.Stages["exhaust"].Status != state.StatusFailed {
		t.Errorf("expected failed after retries exhausted, got %v", final.Stages["exhaust"].Status)
	}
}
```

- [ ] **Step 5: Run all tests**

Run: `go test ./pkg/orchestrator/ -v -timeout 120s`
Expected: All pass, including new and existing tests.

- [ ] **Step 6: Commit**

```bash
git add pkg/orchestrator/integration_test.go
git commit -m "test: интеграционные тесты retry при rate limit"
```

---

### Task 8: Add retry API endpoint

**Files:**
- Modify: `pkg/server/server.go`
- Modify: `pkg/server/handlers.go`
- Modify: `pkg/server/handlers_test.go`

- [ ] **Step 1: Add `retryFn` field to Server and Config**

In `pkg/server/server.go`, update the `Server` struct:

```go
type Server struct {
	runDir    string
	stateFile string
	bus       *orchestrator.EventBus
	approveFn func(stageID string)
	reviseFn  func(stageID, feedback string)
	retryFn   func(stageID string)
	httpSrv   *http.Server
}
```

Update `Config`:

```go
type Config struct {
	Port      int
	RunDir    string
	StateFile string
	Bus       *orchestrator.EventBus
	ApproveFn func(stageID string)
	ReviseFn  func(stageID, feedback string)
	RetryFn   func(stageID string)
}
```

In `New`, add `retryFn`:

```go
func New(cfg Config) *Server {
	s := &Server{
		runDir:    cfg.RunDir,
		stateFile: cfg.StateFile,
		bus:       cfg.Bus,
		approveFn: cfg.ApproveFn,
		reviseFn:  cfg.ReviseFn,
		retryFn:   cfg.RetryFn,
	}
	// ... rest unchanged
```

Update `routeStages` to handle the retry route:

```go
func (s *Server) routeStages(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case strings.HasSuffix(path, "/plan"):
		s.handlePlan(w, r)
	case strings.HasSuffix(path, "/log"):
		s.handleLog(w, r)
	case strings.HasSuffix(path, "/approve") && r.Method == http.MethodPost:
		s.handleApprove(w, r)
	case strings.HasSuffix(path, "/revise") && r.Method == http.MethodPost:
		s.handleRevise(w, r)
	case strings.HasSuffix(path, "/retry") && r.Method == http.MethodPost:
		s.handleRetry(w, r)
	default:
		http.NotFound(w, r)
	}
}
```

- [ ] **Step 2: Add `handleRetry` handler**

In `pkg/server/handlers.go`, add:

```go
func (s *Server) handleRetry(w http.ResponseWriter, r *http.Request) {
	stageID := extractStageID(r.URL.Path, "/api/stages/", "/retry")
	if !isValidStageID(stageID) {
		http.Error(w, "invalid stage id", http.StatusBadRequest)
		return
	}

	// Verify stage is failed
	rs, err := state.Load(s.stateFile)
	if err != nil {
		http.Error(w, "failed to load state", http.StatusInternalServerError)
		return
	}
	st, ok := rs.Stages[stageID]
	if !ok {
		http.Error(w, "stage not found", http.StatusNotFound)
		return
	}
	if st.Status != state.StatusFailed {
		http.Error(w, fmt.Sprintf("stage is %s, not failed", st.Status), http.StatusBadRequest)
		return
	}

	s.retryFn(stageID)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "retried", "stage_id": stageID})
}
```

- [ ] **Step 3: Write the handler test**

In `pkg/server/handlers_test.go`, add:

```go
func TestHandleRetry(t *testing.T) {
	var retriedID string
	srv, _ := setupTestServer(t)
	// Set stage to failed for retry test
	rs, _ := state.Load(srv.stateFile)
	rs.SetStageStatus(testStageID, state.StatusFailed)
	rs.Save(srv.stateFile)
	srv.retryFn = func(id string) { retriedID = id }

	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/retry", nil)
	w := httptest.NewRecorder()
	srv.handleRetry(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", w.Code)
	}
	if retriedID != testStageID {
		t.Errorf("retry not called with s1, got %q", retriedID)
	}
}

func TestHandleRetryNotFailed(t *testing.T) {
	srv, _ := setupTestServer(t)
	srv.retryFn = func(id string) {}

	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/retry", nil)
	w := httptest.NewRecorder()
	srv.handleRetry(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for non-failed stage, got %d", w.Code)
	}
}
```

Update `setupTestServer` to include `RetryFn`:

```go
func setupTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	// ... existing setup code ...
	srv := New(Config{
		Port:      0,
		RunDir:    runDir,
		StateFile: stateFile,
		Bus:       bus,
		ApproveFn: func(id string) {},
		ReviseFn:  func(id, fb string) {},
		RetryFn:   func(id string) {},
	})
	return srv, runDir
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./pkg/server/... -v`
Expected: All pass.

- [ ] **Step 5: Commit**

```bash
git add pkg/server/server.go pkg/server/handlers.go pkg/server/handlers_test.go
git commit -m "feat: API endpoint POST /api/stages/{id}/retry"
```

---

### Task 9: Wire `retryFn` in run command

**Files:**
- Modify: `cmd/flowmanager/run.go`

- [ ] **Step 1: Add `retryFn` to server config**

In `cmd/flowmanager/run.go`, update the `server.New` call (around line 87):

```go
srv := server.New(server.Config{
	Port:      cfg.Server.GetPort(),
	RunDir:    runDir,
	StateFile: stateFile,
	Bus:       orch.Bus(),
	ApproveFn: orch.Approve,
	ReviseFn:  orch.Revise,
	RetryFn:   orch.Retry,
})
```

- [ ] **Step 2: Build and verify**

Run: `go build ./cmd/flowmanager/`
Expected: Compiles successfully.

- [ ] **Step 3: Commit**

```bash
git add cmd/flowmanager/run.go
git commit -m "feat: подключить retryFn к веб-серверу"
```

---

### Task 10: Add `flowmanager retry` CLI command

**Files:**
- Create: `cmd/flowmanager/retry.go`
- Modify: `cmd/flowmanager/main.go`
- Create: `cmd/flowmanager/retry_test.go`

- [ ] **Step 1: Create the retry command**

Create `cmd/flowmanager/retry.go`:

```go
package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/akopichin/afm/pkg/state"
)

func newRetryCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "retry [stage-id]",
		Short: "Retry a failed stage (transitions failed → pending)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			stageID := args[0]
			stateFile, err := findLatestStateFile(stageID)
			if err != nil {
				return err
			}
			rs, err := state.Load(stateFile)
			if err != nil {
				return fmt.Errorf("load state: %w", err)
			}
			st, ok := rs.Stages[stageID]
			if !ok {
				return fmt.Errorf("stage %q not found", stageID)
			}
			if st.Status != state.StatusFailed {
				return fmt.Errorf("stage %q is %v, not failed", stageID, st.Status)
			}
			rs.SetStageStatus(stageID, state.StatusPending)
			if err := rs.Save(stateFile); err != nil {
				return fmt.Errorf("save state: %w", err)
			}
			fmt.Printf("stage %q retried: run 'flowmanager run' to restart\n", stageID)
			return nil
		},
	}
}
```

- [ ] **Step 2: Register in main.go**

In `cmd/flowmanager/main.go`, add `newRetryCmd()` to `root.AddCommand`:

```go
root.AddCommand(
	newRunCmd(),
	newCheckCmd(),
	newApproveCmd(),
	newReviseCmd(),
	newInitCmd(),
	newListCmd(),
	newRetryCmd(),
)
```

- [ ] **Step 3: Write the test**

Create `cmd/flowmanager/retry_test.go`:

```go
package main

import (
	"path/filepath"
	"testing"

	"github.com/akopichin/afm/pkg/state"
)

func TestRetryFailedStage(t *testing.T) {
	chdirTemp(t)

	runDir := makeRunState(t, "flow-20260101-120000", "init", state.StatusFailed)

	cmd := newRetryCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"init"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("retry: %v", err)
	}

	sf := filepath.Join(runDir, "state.json")
	rs, err := state.Load(sf)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if rs.Stages["init"].Status != state.StatusPending {
		t.Errorf("expected pending, got: %v", rs.Stages["init"].Status)
	}
}

func TestRetryNonFailedStage(t *testing.T) {
	chdirTemp(t)

	makeRunState(t, "flow-20260101-120000", "init", state.StatusDone)

	cmd := newRetryCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"init"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for non-failed stage")
	}
}

func TestRetryNonexistentStage(t *testing.T) {
	chdirTemp(t)

	makeRunState(t, "flow-20260101-120000", "init", state.StatusFailed)

	cmd := newRetryCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"nonexistent"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for nonexistent stage")
	}
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./cmd/flowmanager/... -v`
Expected: All pass, including existing approve/revise tests and new retry tests.

- [ ] **Step 5: Commit**

```bash
git add cmd/flowmanager/retry.go cmd/flowmanager/retry_test.go cmd/flowmanager/main.go
git commit -m "feat: CLI команда flowmanager retry <stage-id>"
```

---

### Task 11: Web UI — retry button and retrying status

**Files:**
- Modify: `pkg/web/index.html`
- Modify: `pkg/web/app.js`
- Modify: `pkg/web/style.css`

- [ ] **Step 1: Add CSS styles for retrying status and retry button**

In `pkg/web/style.css`, add after the `.status-dot[data-status="failed"]` rule (line 138):

```css
.status-dot[data-status="retrying"]           { background: var(--c-revising); animation: pulse 1.2s ease-in-out infinite; }
```

Add after `.status-badge[data-status="failed"]` (line 195):

```css
.status-badge[data-status="retrying"]         { background: var(--c-revising); animation: pulse 1.2s ease-in-out infinite; }
```

Add after `.btn-cancel` (line 353):

```css
.btn-retry { background: var(--c-failed); color: #fff; }
.btn-retry:hover { background: #dc2626; }
```

Add after `.feed-stage.status-failed` (line 488):

```css
.feed-stage.status-retrying          { color: var(--c-revising); }
```

- [ ] **Step 2: Add retry button container in HTML**

In `pkg/web/index.html`, add after the `actions-section` div (after line 44, before the plan section closing):

```html
                <div id="retry-section" class="section hidden">
                    <div class="actions-row">
                        <button id="btn-retry" class="btn btn-retry">Попробовать ещё раз</button>
                    </div>
                </div>
```

- [ ] **Step 3: Add DOM refs and event handling in app.js**

At the top of the IIFE in `pkg/web/app.js`, add DOM refs after the existing ones (around line 28):

```js
var $retrySection = document.getElementById("retry-section");
var $btnRetry = document.getElementById("btn-retry");
```

Add `retrying` to `statusLabels` (after `failed`):

```js
retrying: "Повтор",
```

In the `renderDetail` function, add retry section visibility logic after the `actions-section` block (around line 453):

```js
// Show retry button for failed stages
if (st.status === "failed") {
    $retrySection.classList.remove("hidden");
} else {
    $retrySection.classList.add("hidden");
}
```

In `startLogPolling`, add `"retrying"` to the polling condition (around line 600):

```js
if (st && (st.status === "planning" || st.status === "running" || st.status === "revising" || st.status === "retrying")) {
```

Add retry button click handler after the revise button handler (around line 686):

```js
$btnRetry.addEventListener("click", function () {
    if (!selectedStageID) return;
    $btnRetry.disabled = true;
    apiPost("/api/stages/" + selectedStageID + "/retry", null, function (err) {
        $btnRetry.disabled = false;
        if (err) {
            console.error("retry error:", err);
        }
    });
});
```

In the `addFeedEntry` function's switch statement, add cases for the new events (after the `revised` case):

```js
case "retry_scheduled":
    msg = "повтор: " + (ev.data || "");
    statusClass = "status-retrying";
    break;
case "retry_exhausted":
    msg = "попытки исчерпаны";
    statusClass = "status-failed";
    msgClass = "feed-msg error";
    break;
case "manual_retry":
    msg = "ручной повтор";
    statusClass = "status-retrying";
    break;
```

- [ ] **Step 4: Rebuild binary and verify**

Run: `go build -o bin/flowmanager ./cmd/flowmanager/`
Expected: Compiles successfully. The web files are embedded, so the build picks up the changes.

- [ ] **Step 5: Commit**

```bash
git add pkg/web/index.html pkg/web/app.js pkg/web/style.css
git commit -m "feat: кнопка retry и статус retrying в веб-интерфейсе"
```

---

### Task 12: Final build and lint check

**Files:** None (verification only)

- [ ] **Step 1: Run all tests**

Run: `go test ./... -v -timeout 120s`
Expected: All pass.

- [ ] **Step 2: Run linter**

Run: `go vet ./...`
Expected: No issues.

- [ ] **Step 3: Build the binary**

Run: `go build -o bin/flowmanager ./cmd/flowmanager/`
Expected: Compiles successfully.

- [ ] **Step 4: Verify CLI help**

Run: `./bin/flowmanager --help`
Expected: Shows `retry` in the list of available commands.

Run: `./bin/flowmanager retry --help`
Expected: Shows "Retry a failed stage (transitions failed → pending)"
