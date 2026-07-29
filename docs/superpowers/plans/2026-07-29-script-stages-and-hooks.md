# Script-стейджи и script_before/script_after хуки — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `script` stage type (runs a shell script, no LLM agent) and `script_before`/`script_after` hook fields (run a shell script immediately before/after any stage's main content), with full event-log/UI visibility and a retry/skip gate for hook failures.

**Architecture:** `pkg/flow` gains the schema + validation; `pkg/executor` gains a `RunScript` primitive reusing the existing subprocess/line-streaming machinery; `pkg/orchestrator` gains a new `runScriptStage` runner plus a hook layer (`pkg/orchestrator/hooks.go`) that gates *stage activation* (before-hook, blocking, new `hook_failed` status) and taps the single stage-completion chokepoint `onAgentCompleted` (after-hook, non-blocking); `pkg/server` exposes `retry-hook`/`skip-hook` endpoints mirroring the existing `retry` endpoint; the dashboard gets a new event-feed line type, a new stage status badge, and Retry/Skip buttons mirroring the existing Approve/Revise buttons.

**Tech Stack:** Go (backend), React/TypeScript (dashboard, Vitest for tests), YAML (flow config, parsed via `gopkg.in/yaml.v3`).

## Global Constraints

- Hook/script retry policy is fixed: 3 retries, backoff 1s → 2s → 3s between attempts (4 total attempts). This is a **separate** policy from the existing LLM retry (`pkg/orchestrator/retry.go`, 15 retries / flat 5s) — do not touch `runWithRetry`, `MaxRetries`, or `RetryBackoff`.
- `script_timeout` / `script_before_timeout` / `script_after_timeout` are Go `time.Duration` fields, parsed natively by `gopkg.in/yaml.v3` from strings like `"120s"` — no custom `UnmarshalYAML` needed (exact precedent: `ExecutorConfig.IdleTimeout` in `pkg/config/config.go:36-41`).
- A `script` stage must have **only** `script`/`script_timeout` (plus generic stage fields `id`/`name`/`depends_on`/`artifacts`/`inputs`/`max_parallel`) — no `agents`/`command`/`interactive`/`plan`/`verify`/`supervisor`.
- `script_before`/`script_after` are legal on **any** stage type, alongside all of that stage's other fields.
- `script_after` runs **only** on successful stage completion (`done`) and never reverts the stage back to `failed` — hook failure there is a non-blocking, dismissable notice.
- `script_before` failure (after exhausting retries) **blocks** the stage in a new `hook_failed` status until the user hits Retry or Skip in the dashboard.
- All shell execution goes through `sh -c "<script>"`, matching the existing `verify:` field's mechanism (`pkg/orchestrator/completion.go:98-101`).
- Commit messages must be in Russian, no `Co-Authored-By` trailer (per project CLAUDE.md).
- Run `make lint` and `make test` (or targeted `go test ./...`) after each task; this repo's pre-commit hook already runs lint+build+test on every commit — do not bypass it (`--no-verify`).

---

## File Structure

| File | Change |
|---|---|
| `pkg/flow/flow.go` | Add `Script`, `ScriptTimeout`, `ScriptBefore`, `ScriptBeforeTimeout`, `ScriptAfter`, `ScriptAfterTimeout` to `Stage`; add `IsScript()`; extend `validate()` |
| `pkg/state/state.go` | Add `StatusHookFailed` |
| `pkg/orchestrator/bus.go` | Add `EventScriptOutput`, `EventHookFailed`, `EventHookResolved` |
| `pkg/orchestrator/fsm.go` | Add `EvHookFailed`, `EvHookResolved` + 2 rules |
| `pkg/executor/executor.go` | Add `RunScript` method |
| `pkg/orchestrator/hooks.go` (new) | `runScriptWithRetry`, `hookPending` persistence, `hookDecision` channel infra, `execScript`, `runBeforeHook`, `runAfterHook`, `withBeforeHook`, `maybeRunAfterHook` |
| `pkg/orchestrator/agents.go` | Add `runScriptStage` |
| `pkg/orchestrator/scheduling.go` | Add `activateScriptStage`; wire `IsScript()` branch + `withBeforeHook` into `startReadyStages`/`tryActivatePrePlanned`/`retryStage` |
| `pkg/orchestrator/orchestrator.go` | Add `hookWaiters sync.Map` field; call `maybeRunAfterHook` from `onAgentCompleted` |
| `pkg/orchestrator/control_api.go` | Add `RetryHook`/`SkipHook` methods |
| `pkg/orchestrator/recovery.go` | Resume stages stuck in `hook_failed` / pending after-hook |
| `pkg/server/handlers.go` | Add `handleRetryHook`/`handleSkipHook` |
| `pkg/server/server.go` | Route `/retry-hook`/`/skip-hook`; add `Config`/`Server` fields |
| `cmd/afm/run.go` | Wire `RetryHookFn: orch.RetryHook`, `SkipHookFn: orch.SkipHook` |
| `pkg/web/dashboard/src/types/afm-event.ts` | Add new event type names |
| `pkg/web/dashboard/src/components/event-feed/EventFeedPanel.tsx` | Render `script_output`/`hook_failed`/`hook_resolved` |
| `pkg/web/dashboard/src/types/stage.ts` | Add `hook_failed` to `STAGE_STATUSES`/`STAGE_STATUS_LABELS` |
| `pkg/web/dashboard/src/components/plan-panel/PlanPanel.tsx` | Add Skip button + hook-failed Retry/Skip section |
| `pkg/server/handlers.go` (log handler) | Concatenate `before.log`/`script.log`/`after.log` |

---

### Task 1: `flow.Stage` script fields + `IsScript()`

**Files:**
- Modify: `pkg/flow/flow.go:54-87` (Stage struct), after line 138 (add method)
- Test: `pkg/flow/flow_test.go`

**Interfaces:**
- Produces: `Stage.Script string`, `Stage.ScriptTimeout time.Duration`, `Stage.ScriptBefore string`, `Stage.ScriptBeforeTimeout time.Duration`, `Stage.ScriptAfter string`, `Stage.ScriptAfterTimeout time.Duration`, `func (s *Stage) IsScript() bool`

- [ ] **Step 1: Write the failing test**

Add to `pkg/flow/flow_test.go`:

```go
func TestParseScriptStageFields(t *testing.T) {
	yaml := `
name: f
description: d
stages:
  - id: notify
    name: N
    description: d
    script: |
      echo "hello"
    script_timeout: 45s
`
	f, err := flow.ParseFile(writeTemp(t, yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	st := f.Stages[0]
	if st.Script != "echo \"hello\"\n" {
		t.Errorf("Script = %q", st.Script)
	}
	if st.ScriptTimeout != 45*time.Second {
		t.Errorf("ScriptTimeout = %v, want 45s", st.ScriptTimeout)
	}
	if !st.IsScript() {
		t.Error("IsScript() should be true")
	}
}

func TestParseScriptBeforeAfterFields(t *testing.T) {
	yaml := `
name: f
description: d
stages:
  - id: build
    name: B
    description: d
    agents: [implementation]
    script_before: |
      echo "before"
    script_before_timeout: 10s
    script_after: |
      echo "after"
    script_after_timeout: 20s
`
	f, err := flow.ParseFile(writeTemp(t, yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	st := f.Stages[0]
	if st.ScriptBefore != "echo \"before\"\n" || st.ScriptBeforeTimeout != 10*time.Second {
		t.Errorf("ScriptBefore = %q / %v", st.ScriptBefore, st.ScriptBeforeTimeout)
	}
	if st.ScriptAfter != "echo \"after\"\n" || st.ScriptAfterTimeout != 20*time.Second {
		t.Errorf("ScriptAfter = %q / %v", st.ScriptAfter, st.ScriptAfterTimeout)
	}
	if st.IsScript() {
		t.Error("IsScript() should be false for an agent stage with hooks")
	}
}
```

Make sure `"time"` is imported in `flow_test.go` (add if missing).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/flow/... -run TestParseScript -v`
Expected: FAIL — `Stage` has no field `Script`/`ScriptTimeout`/etc, compile error.

- [ ] **Step 3: Write minimal implementation**

In `pkg/flow/flow.go`, add fields to the `Stage` struct (after the `SupervisorPrompt` field, flow.go:87):

```go
	// Script, if set, makes this a script-only stage: it runs the given shell
	// script (via sh -c) instead of any agent, with no planning/supervisor/
	// approval gate. Mutually exclusive with Agents/Command/Interactive/Plan/
	// Verify/Supervisor.
	Script        string        `yaml:"script"`
	ScriptTimeout time.Duration `yaml:"script_timeout"`
	// ScriptBefore/ScriptAfter run a shell script immediately before/after this
	// stage's own main content (agent, script, or interactive). Legal on any
	// stage type, alongside its other fields.
	ScriptBefore        string        `yaml:"script_before"`
	ScriptBeforeTimeout time.Duration `yaml:"script_before_timeout"`
	ScriptAfter         string        `yaml:"script_after"`
	ScriptAfterTimeout  time.Duration `yaml:"script_after_timeout"`
```

Add `"time"` to the import block at the top of `flow.go` if not already present.

Add the method after `IsAuto()` (flow.go:136-138):

```go
// IsScript reports whether the stage runs a plain shell script instead of an
// agent (agents: [] entirely absent, replaced by the Script field).
func (s *Stage) IsScript() bool {
	return s.Script != ""
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/flow/... -run TestParseScript -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/flow/flow.go pkg/flow/flow_test.go
git commit -m "$(cat <<'EOF'
feat(flow): добавить поля script/script_before/script_after в Stage

EOF
)"
```

---

### Task 2: Validation — `script` mutual exclusivity + "must have a way to do work"

**Files:**
- Modify: `pkg/flow/flow.go:194-198` (extend existing check), `pkg/flow/flow.go:217` (insert new validation loop after auto-checks)
- Test: `pkg/flow/flow_test.go`

**Interfaces:**
- Consumes: `Stage.IsScript()`, `Stage.Script`, `Stage.Agents`, `Stage.Command`, `Stage.Interactive`, `Stage.Plan`, `Stage.Verify`, `Stage.Supervisor` (Task 1)

- [ ] **Step 1: Write the failing tests**

Add to `pkg/flow/flow_test.go`:

```go
func TestValidateScriptCannotCombineWithAgents(t *testing.T) {
	yaml := `
name: f
description: d
stages:
  - id: s1
    name: S1
    description: d
    script: "echo hi"
    agents: [implementation]
`
	_, err := flow.ParseFile(writeTemp(t, yaml))
	if err == nil || !strings.Contains(err.Error(), "script") {
		t.Fatalf("expected script-combination error, got %v", err)
	}
}

func TestValidateScriptCannotCombineWithVerify(t *testing.T) {
	yaml := `
name: f
description: d
stages:
  - id: s1
    name: S1
    description: d
    script: "echo hi"
    verify: "true"
`
	_, err := flow.ParseFile(writeTemp(t, yaml))
	if err == nil || !strings.Contains(err.Error(), "script") {
		t.Fatalf("expected script-combination error, got %v", err)
	}
}

func TestValidateScriptStageNeedsNoOtherWorkField(t *testing.T) {
	yaml := `
name: f
description: d
stages:
  - id: s1
    name: S1
    description: d
    script: "echo hi"
`
	_, err := flow.ParseFile(writeTemp(t, yaml))
	if err != nil {
		t.Fatalf("unexpected error for valid script-only stage: %v", err)
	}
}
```

`strings` must already be imported in `flow_test.go` (used by existing `auto_test.go`-style substring checks) — verify and add if missing.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/flow/... -run TestValidateScript -v`
Expected: `TestValidateScriptCannotCombineWithAgents` and `TestValidateScriptCannotCombineWithVerify` FAIL (no error currently returned); `TestValidateScriptStageNeedsNoOtherWorkField` FAILS too (current line 195's `must have planning agent or a plan path` check rejects a stage with no `agents`/`interactive`/`auto`/`plan`, since `IsScript()` isn't excluded yet).

- [ ] **Step 3: Write minimal implementation**

In `pkg/flow/flow.go`, extend the existing check at line 195:

```go
	for _, s := range f.Stages {
		if s.Plan == "" && !s.HasAgent(AgentPlanning) && !s.Interactive && !s.IsAuto() && !s.IsScript() {
			return fmt.Errorf("stage %q: must have planning agent, a plan path, or script", s.ID)
		}
	}
```

Insert a new validation loop right after the auto-checks block (after line 217, before `detectCycles` at line 219):

```go
	for _, s := range f.Stages {
		if !s.IsScript() {
			continue
		}
		if len(s.Agents) > 0 {
			return fmt.Errorf("stage %q: \"script\" cannot be combined with agents", s.ID)
		}
		if s.Command != "" {
			return fmt.Errorf("stage %q: \"script\" cannot be combined with command", s.ID)
		}
		if s.Interactive {
			return fmt.Errorf("stage %q: \"script\" cannot be combined with interactive", s.ID)
		}
		if s.Plan != "" {
			return fmt.Errorf("stage %q: \"script\" cannot be combined with plan", s.ID)
		}
		if s.Verify != "" {
			return fmt.Errorf("stage %q: \"script\" cannot be combined with verify", s.ID)
		}
		if s.Supervisor {
			return fmt.Errorf("stage %q: \"script\" cannot be combined with supervisor", s.ID)
		}
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/flow/... -v`
Expected: PASS (all flow package tests, including the new ones and the existing suite unaffected)

- [ ] **Step 5: Commit**

```bash
git add pkg/flow/flow.go pkg/flow/flow_test.go
git commit -m "$(cat <<'EOF'
feat(flow): валидация script-стейджа — взаимоисключение с agents/command/interactive/plan/verify/supervisor

EOF
)"
```

---

### Task 3: New stage status `hook_failed`

**Files:**
- Modify: `pkg/state/state.go:19-29`
- Test: none needed standalone (a bare string const) — covered by Task 4/5 tests

**Interfaces:**
- Produces: `state.StatusHookFailed StageStatus`

- [ ] **Step 1: Write minimal implementation**

This is a plain const addition with no independent test surface (it's exercised end-to-end by Task 5's FSM tests). In `pkg/state/state.go`, add to the const block after `StatusFailed`:

```go
const (
	StatusPending           StageStatus = "pending"
	StatusPlanning          StageStatus = "planning"
	StatusAwaitingApproval  StageStatus = "awaiting_approval"
	StatusRevising          StageStatus = "revising"
	StatusReady             StageStatus = "ready"
	StatusRunning           StageStatus = "running"
	StatusRetrying          StageStatus = "retrying"
	StatusAwaitingUserInput StageStatus = "awaiting_user_input"
	StatusDone              StageStatus = "done"
	StatusFailed            StageStatus = "failed"
	// StatusHookFailed — script_before exhausted its retries; the stage is
	// blocked until the user retries or skips the hook via the dashboard.
	// Non-terminal (see orchestrator.IsTerminal).
	StatusHookFailed StageStatus = "hook_failed"
)
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./...`
Expected: succeeds (no other code references this const yet).

- [ ] **Step 3: Commit**

```bash
git add pkg/state/state.go
git commit -m "$(cat <<'EOF'
feat(state): новый статус hook_failed

EOF
)"
```

---

### Task 4: New event types

**Files:**
- Modify: `pkg/orchestrator/bus.go:10-22`

**Interfaces:**
- Produces: `orchestrator.EventScriptOutput`, `orchestrator.EventHookFailed`, `orchestrator.EventHookResolved` (all `EventType`)

- [ ] **Step 1: Write minimal implementation**

In `pkg/orchestrator/bus.go`, add to the `EventType` const block:

```go
const (
	EventStageStatusChanged EventType = "stage_status_changed"
	EventAgentAction        EventType = "agent_action"
	EventAgentCompleted     EventType = "agent_completed"
	EventApproved           EventType = "approved"
	EventRetryScheduled     EventType = "retry_scheduled"
	EventRetryExhausted     EventType = "retry_exhausted"
	EventAskUser            EventType = "ask_user"
	EventUserAnswered       EventType = "user_answered"
	EventSupervisorDecision EventType = "supervisor_decision"
	EventContextWarning     EventType = "context_warning"
	// EventScriptOutput carries one line of stdout from a script/hook run.
	// Data: map[string]string{"hook": "before"|"script"|"after", "line": "..."}.
	EventScriptOutput EventType = "script_output"
	// EventHookFailed fires when a before/after hook exhausts its 3x/1-2-3s
	// retries. Data: map[string]string{"hook": ..., "error": "..."}.
	EventHookFailed EventType = "hook_failed"
	// EventHookResolved fires when the user retries or skips a failed hook.
	// Data: map[string]string{"hook": ..., "resolution": "retried"|"skipped"}.
	EventHookResolved EventType = "hook_resolved"
)
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./...`

- [ ] **Step 3: Commit**

```bash
git add pkg/orchestrator/bus.go
git commit -m "$(cat <<'EOF'
feat(orchestrator): новые типы событий script_output/hook_failed/hook_resolved

EOF
)"
```

---

### Task 5: FSM events/rules for `hook_failed`

**Files:**
- Modify: `pkg/orchestrator/fsm.go:12-28` (events), `pkg/orchestrator/fsm.go:56-78` (rules)
- Test: `pkg/orchestrator/fsm_test.go` (create if it doesn't exist, or add to existing FSM test file — check first)

**Interfaces:**
- Consumes: `state.StatusHookFailed` (Task 3)
- Produces: `orchestrator.EvHookFailed`, `orchestrator.EvHookResolved` (both `FSMEvent`)

- [ ] **Step 1: Check for an existing FSM test file**

Run: `ls pkg/orchestrator/fsm_test.go 2>/dev/null || echo "no fsm_test.go"`

If it exists, read it to match its exact test style before adding new tests to it. If it doesn't exist, create `pkg/orchestrator/fsm_test.go` with the package/import header matching other orchestrator white-box tests (`package orchestrator`, importing `"testing"` and `"github.com/akopichin/afm/pkg/state"`).

- [ ] **Step 2: Write the failing test**

Add (to the existing file or the new one):

```go
func TestFSM_HookFailedTransitions(t *testing.T) {
	store, err := state.Open(t.TempDir(), []string{"s1"})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	defer store.Close()

	fsm := NewFSM(store)

	// running -> hook_failed
	if err := store.Apply(&state.Transition{StageID: "s1", From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatalf("setup transition: %v", err)
	}
	to, _, ok, err := fsm.Apply("s1", EvHookFailed, GuardCtx{}, "before hook failed")
	if err != nil || !ok || to != state.StatusHookFailed {
		t.Fatalf("EvHookFailed from running: to=%v ok=%v err=%v", to, ok, err)
	}

	// hook_failed -> running (resolved)
	to, _, ok, err = fsm.Apply("s1", EvHookResolved, GuardCtx{}, "user retried")
	if err != nil || !ok || to != state.StatusRunning {
		t.Fatalf("EvHookResolved from hook_failed: to=%v ok=%v err=%v", to, ok, err)
	}

	// EvHookFailed from done should be rejected (not in the From list)
	if err := store.Apply(&state.Transition{StageID: "s1", From: state.StatusRunning, To: state.StatusDone, Event: "test_setup"}); err != nil {
		t.Fatalf("setup transition to done: %v", err)
	}
	_, _, ok, err = fsm.Apply("s1", EvHookFailed, GuardCtx{}, "after hook failed")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("EvHookFailed should not apply from done (after-hook failures don't use the FSM)")
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./pkg/orchestrator/... -run TestFSM_HookFailedTransitions -v`
Expected: FAIL — compile error, `EvHookFailed`/`EvHookResolved` undefined.

- [ ] **Step 4: Write minimal implementation**

In `pkg/orchestrator/fsm.go`, add to the `FSMEvent` const block:

```go
const (
	EvStartPlanning      FSMEvent = "start_planning"
	EvPlanReady          FSMEvent = "plan_ready"
	EvApprove            FSMEvent = "approve"
	EvRevise             FSMEvent = "revise"
	EvStartRun           FSMEvent = "start_run"
	EvComplete           FSMEvent = "complete"
	EvFail               FSMEvent = "fail"
	EvAskUser            FSMEvent = "ask_user"
	EvUserAnswered       FSMEvent = "user_answered"
	EvScheduleRetry      FSMEvent = "schedule_retry"
	EvResumeAfterRetry   FSMEvent = "resume_after_retry"
	EvManualRetry        FSMEvent = "manual_retry"
	EvBlockedByDep       FSMEvent = "blocked_by_dep"
	EvReady              FSMEvent = "ready"
	EvSupervisorApproved FSMEvent = "supervisor_approved"
	// EvHookFailed: script_before exhausted its retries — blocks the stage.
	// Only fired for the blocking "before" hook; "after" hook failures do not
	// go through the FSM (the stage is already done and must stay done).
	EvHookFailed FSMEvent = "hook_failed"
	// EvHookResolved: user retried (succeeded) or skipped a failed before-hook.
	EvHookResolved FSMEvent = "hook_resolved"
)
```

Add to the rules map in `NewFSM` (after `EvSupervisorApproved`):

```go
			EvHookFailed:   {From: []state.StageStatus{state.StatusRunning}, To: to(state.StatusHookFailed)},
			EvHookResolved: {From: []state.StageStatus{state.StatusHookFailed}, To: to(state.StatusRunning)},
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./pkg/orchestrator/... -run TestFSM_HookFailedTransitions -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add pkg/orchestrator/fsm.go pkg/orchestrator/fsm_test.go
git commit -m "$(cat <<'EOF'
feat(orchestrator): FSM-события EvHookFailed/EvHookResolved и статус hook_failed

EOF
)"
```

---

### Task 6: `executor.RunScript`

**Files:**
- Modify: `pkg/executor/executor.go` (add method after `RunAgent`, i.e. after line 413)
- Test: `pkg/executor/executor_test.go`

**Interfaces:**
- Consumes: `Executor.run` (existing, unchanged), `progress.NewLogger`/`LogStart`/`LogAction`/`LogEnd` (existing), `openStderrLog` (existing unexported helper)
- Produces: `func (e *Executor) RunScript(ctx context.Context, timeout time.Duration, logFile string) error`

- [ ] **Step 1: Write the failing tests**

Add to `pkg/executor/executor_test.go`:

```go
func TestRunScript_Success(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "script.log")

	ex := executor.New(executor.Config{
		Command:   testCmdShell,
		ExtraArgs: []string{testFlagC, "echo hello-script"},
	})

	if err := ex.RunScript(context.Background(), 5*time.Second, logFile); err != nil {
		t.Fatalf("RunScript: %v", err)
	}

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "hello-script") {
		t.Errorf("log missing script output: %q", content)
	}
	if !strings.Contains(content, "completed") {
		t.Errorf("log missing completion banner: %q", content)
	}
}

func TestRunScript_NonZeroExit(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "script.log")

	ex := executor.New(executor.Config{
		Command:   testCmdShell,
		ExtraArgs: []string{testFlagC, "exit 3"},
	})

	err := ex.RunScript(context.Background(), 5*time.Second, logFile)
	if err == nil {
		t.Fatal("expected error for non-zero exit, got nil")
	}
	data, _ := os.ReadFile(logFile)
	if !strings.Contains(string(data), "FAILED") {
		t.Errorf("log should contain FAILED banner: %q", string(data))
	}
}

func TestRunScript_HardTimeout(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "script.log")

	ex := executor.New(executor.Config{
		Command:   testCmdShell,
		ExtraArgs: []string{testFlagC, "sleep 10"},
	})

	start := time.Now()
	err := ex.RunScript(context.Background(), 200*time.Millisecond, logFile)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if elapsed > 5*time.Second {
		t.Errorf("RunScript took too long to time out: %v", elapsed)
	}
}

func TestRunScript_OnActionCalledPerLine(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "script.log")

	var got []string
	ex := executor.New(executor.Config{
		Command:   testCmdShell,
		ExtraArgs: []string{testFlagC, "echo line1\necho line2"},
		OnAction: func(tool, detail string) {
			got = append(got, detail)
		},
	})

	if err := ex.RunScript(context.Background(), 5*time.Second, logFile); err != nil {
		t.Fatalf("RunScript: %v", err)
	}
	if len(got) != 2 || got[0] != "line1" || got[1] != "line2" {
		t.Errorf("OnAction lines = %v, want [line1 line2]", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/executor/... -run TestRunScript -v`
Expected: FAIL — compile error, `RunScript` undefined on `*Executor`.

- [ ] **Step 3: Write minimal implementation**

In `pkg/executor/executor.go`, add after `RunAgent` (after line 413). Make sure `"errors"` is imported (check the existing import block — `ErrUserInterrupted` at line 45 already uses `errors.New`, so `"errors"` is already imported):

```go
// RunScript runs a plain shell script (no stream-json parsing, no session/
// resume args) with a hard, non-resetting timeout — unlike RunAgent's
// idle-timeout (reset per output line), timeout here bounds the whole run
// regardless of how much output streams. Each output line is logged via
// LogAction("stdout", line) and forwarded to Config.OnAction if set, so
// callers get the same per-line visibility RunAgent gives for tool actions.
func (e *Executor) RunScript(ctx context.Context, timeout time.Duration, logFile string) error {
	lg, err := progress.NewLogger(logFile)
	if err != nil {
		return err
	}
	defer lg.Close()

	var stderr io.Writer = io.Discard
	if sf := openStderrLog(logFile); sf != nil {
		stderr = sf
		defer sf.Close()
	}

	lg.LogStart("script", strings.TrimSuffix(filepath.Base(logFile), filepath.Ext(logFile)))

	runCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	runErr := e.run(runCtx, "", "script", stderr, func(line string) {
		lg.LogAction("stdout", line)
		if e.cfg.OnAction != nil {
			e.cfg.OnAction("stdout", line)
		}
	})
	if errors.Is(runErr, context.DeadlineExceeded) {
		runErr = fmt.Errorf("script timeout after %v", timeout)
	}

	lg.LogEnd(runErr)
	return runErr
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/executor/... -run TestRunScript -v`
Expected: PASS

- [ ] **Step 5: Run the full executor test suite to check for regressions**

Run: `go test ./pkg/executor/... -v`
Expected: PASS (no existing test touches `RunScript`, so no regressions expected)

- [ ] **Step 6: Commit**

```bash
git add pkg/executor/executor.go pkg/executor/executor_test.go
git commit -m "$(cat <<'EOF'
feat(executor): RunScript — выполнение shell-скрипта с жёстким таймаутом и построчным логом

EOF
)"
```

---

### Task 7: `pkg/orchestrator/hooks.go` — retry helper, hook-pending persistence, decision channel

**Files:**
- Create: `pkg/orchestrator/hooks.go`
- Modify: `pkg/orchestrator/orchestrator.go` (add `hookWaiters sync.Map` field to `Orchestrator` struct, after `preAskPhase sync.Map` at line 88)
- Test: `pkg/orchestrator/hooks_test.go` (new file)

**Interfaces:**
- Produces: `runScriptWithRetry(ctx, fn) error`, `hookDecision` type + `hookDecisionRetry`/`hookDecisionSkip` consts, `hookPending` struct + `writeHookPending`/`readHookPending`/`clearHookPending`, `(*Orchestrator).waitForHookDecision(ctx, stageID) (hookDecision, bool)`, `(*Orchestrator).resolveHook(stageID, d) bool`

- [ ] **Step 1: Write the failing tests**

Create `pkg/orchestrator/hooks_test.go`:

```go
package orchestrator

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestRunScriptWithRetry_SucceedsOnThirdAttempt(t *testing.T) {
	attempts := 0
	err := runScriptWithRetry(context.Background(), func() error {
		attempts++
		if attempts < 3 {
			return errors.New("boom")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
}

func TestRunScriptWithRetry_ExhaustsAfterFourAttempts(t *testing.T) {
	attempts := 0
	err := runScriptWithRetry(context.Background(), func() error {
		attempts++
		return errors.New("always fails")
	})
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if attempts != hookMaxRetries+1 {
		t.Errorf("attempts = %d, want %d", attempts, hookMaxRetries+1)
	}
}

func TestRunScriptWithRetry_RespectsBackoffTiming(t *testing.T) {
	attempts := 0
	start := time.Now()
	_ = runScriptWithRetry(context.Background(), func() error {
		attempts++
		return errors.New("fail")
	})
	elapsed := time.Since(start)
	// 1s + 2s + 3s = 6s minimum between the 4 attempts.
	if elapsed < 6*time.Second {
		t.Errorf("elapsed = %v, want >= 6s (backoff not respected)", elapsed)
	}
}

func TestHookPending_WriteReadClear(t *testing.T) {
	dir := t.TempDir()
	p := hookPending{Hook: "before", Script: "echo hi", Timeout: 30 * time.Second}
	if err := writeHookPending(dir, p); err != nil {
		t.Fatalf("writeHookPending: %v", err)
	}
	got, ok := readHookPending(dir)
	if !ok {
		t.Fatal("readHookPending: not found")
	}
	if got != p {
		t.Errorf("readHookPending = %+v, want %+v", got, p)
	}
	clearHookPending(dir)
	if _, ok := readHookPending(dir); ok {
		t.Error("expected pending to be cleared")
	}
}

func TestWaitForHookDecision_ResolveDelivers(t *testing.T) {
	o := &Orchestrator{}
	done := make(chan hookDecision, 1)
	go func() {
		d, ok := o.waitForHookDecision(context.Background(), "s1")
		if !ok {
			t.Error("expected ok=true")
		}
		done <- d
	}()

	// Give the goroutine a moment to register the waiter.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if o.resolveHook("s1", hookDecisionSkip) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	select {
	case d := <-done:
		if d != hookDecisionSkip {
			t.Errorf("decision = %v, want skip", d)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for decision to be delivered")
	}
}

func TestWaitForHookDecision_CtxCancelReturnsFalse(t *testing.T) {
	o := &Orchestrator{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, ok := o.waitForHookDecision(ctx, "s1")
	if ok {
		t.Error("expected ok=false when ctx is already cancelled")
	}
}

func TestResolveHook_NoWaiterReturnsFalse(t *testing.T) {
	o := &Orchestrator{}
	if o.resolveHook("nonexistent", hookDecisionRetry) {
		t.Error("expected false when no stage is waiting")
	}
}

var _ = filepath.Join // silence unused import if not otherwise used
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/orchestrator/... -run 'TestRunScriptWithRetry|TestHookPending|TestWaitForHookDecision|TestResolveHook' -v`
Expected: FAIL — compile errors, none of these symbols exist yet.

- [ ] **Step 3: Write minimal implementation**

Create `pkg/orchestrator/hooks.go`:

```go
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
```

Add the new field to the `Orchestrator` struct in `pkg/orchestrator/orchestrator.go`, right after `preAskPhase sync.Map` (line 88):

```go
	// hookWaiters holds, per stageID, the channel a blocked before/after hook
	// is waiting on for a user decision (see hooks.go: waitForHookDecision/
	// resolveHook). Only populated while a hook is actually blocked.
	hookWaiters sync.Map
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/orchestrator/... -run 'TestRunScriptWithRetry|TestHookPending|TestWaitForHookDecision|TestResolveHook' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/orchestrator/hooks.go pkg/orchestrator/hooks_test.go pkg/orchestrator/orchestrator.go
git commit -m "$(cat <<'EOF'
feat(orchestrator): retry-политика хуков, персистентность hook_pending.json, канал решений

EOF
)"
```

---

### Task 8: `execScript` helper

**Files:**
- Modify: `pkg/orchestrator/hooks.go` (add function)
- Test: `pkg/orchestrator/hooks_test.go`

**Interfaces:**
- Consumes: `Options.RootDir` (`orchestrator.go:57`), `executor.New`/`executor.Config`/`RunScript` (Task 6)
- Produces: `(*Orchestrator).execScript(ctx, s, hook, script string, timeout time.Duration, logFile string) error`

- [ ] **Step 1: Write the failing test**

Add to `pkg/orchestrator/hooks_test.go`:

```go
func TestExecScript_RunsInRootDirAndPublishesOutput(t *testing.T) {
	rootDir := t.TempDir()
	runDir := t.TempDir()
	stageDir := filepath.Join(runDir, "s1")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}

	ui := NewUIBus()
	events := make(chan Event, 10)
	unsub := ui.Subscribe(func(e Event) { events <- e })
	defer unsub()

	o := &Orchestrator{opts: Options{RootDir: rootDir, RunDir: runDir}, ui: ui}

	s := flow.Stage{ID: "s1"}
	logFile := filepath.Join(stageDir, "before.log")
	err := o.execScript(context.Background(), s, "before", "pwd; echo output-line", 5*time.Second, logFile)
	if err != nil {
		t.Fatalf("execScript: %v", err)
	}

	data, _ := os.ReadFile(logFile)
	if !strings.Contains(string(data), rootDir) {
		t.Errorf("script should have run in rootDir, log: %q", string(data))
	}

	select {
	case ev := <-events:
		if ev.Type != EventScriptOutput {
			t.Errorf("event type = %v, want EventScriptOutput", ev.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected at least one EventScriptOutput to be published")
	}
}
```

Check `pkg/orchestrator/hooks_test.go`'s imports and add `"os"`, `"strings"`, `"github.com/akopichin/afm/pkg/flow"` as needed. If `NewUIBus`/`UIBus.Subscribe` have different exact names, grep `pkg/orchestrator/bus.go` first (`grep -n "func NewUIBus\|func.*UIBus.*Subscribe" pkg/orchestrator/bus.go`) and adjust the test to match the real API before proceeding.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/orchestrator/... -run TestExecScript -v`
Expected: FAIL — `execScript` undefined.

- [ ] **Step 3: Write minimal implementation**

Add to `pkg/orchestrator/hooks.go` (add `"github.com/akopichin/afm/pkg/executor"` and `"github.com/akopichin/afm/pkg/flow"` to the imports):

```go
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/orchestrator/... -run TestExecScript -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/orchestrator/hooks.go pkg/orchestrator/hooks_test.go
git commit -m "$(cat <<'EOF'
feat(orchestrator): execScript — запуск shell-скрипта стейджа с публикацией построчных событий

EOF
)"
```

---

### Task 9: `runBeforeHook` — blocking gate with retry/skip

**Files:**
- Modify: `pkg/orchestrator/hooks.go`
- Test: `pkg/orchestrator/hooks_test.go`

**Interfaces:**
- Consumes: `runScriptWithRetry`, `execScript`, `writeHookPending`/`clearHookPending`, `waitForHookDecision`, `Orchestrator.triggerWithSeq`, `EvHookFailed`/`EvHookResolved` (Task 5), `EventHookFailed`/`EventHookResolved` (Task 4)
- Produces: `(*Orchestrator).runBeforeHook(ctx, s flow.Stage) bool`

- [ ] **Step 1: Write the failing tests**

Add to `pkg/orchestrator/hooks_test.go`:

```go
func setupHookOrch(t *testing.T, stageID string) (*Orchestrator, string) {
	t.Helper()
	rootDir := t.TempDir()
	runDir := t.TempDir()
	store, err := state.Open(runDir, []string{stageID})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Apply(&state.Transition{StageID: stageID, From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	ui := NewUIBus()
	critical := NewCriticalBus()
	o := &Orchestrator{
		opts:  Options{RootDir: rootDir, RunDir: runDir, Store: store},
		ui:    ui,
		critical: critical,
		fsm:   NewFSM(store),
	}
	return o, runDir
}

func TestRunBeforeHook_SucceedsFirstTry(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	s := flow.Stage{ID: "s1", ScriptBefore: "echo ok"}
	if !o.runBeforeHook(context.Background(), s) {
		t.Fatal("expected true (proceed) on success")
	}
	if got := o.opts.Store.Get("s1"); got != state.StatusRunning {
		t.Errorf("status = %v, want running (unchanged)", got)
	}
}

func TestRunBeforeHook_BlocksThenRetrySucceeds(t *testing.T) {
	o, runDir := setupHookOrch(t, "s1")
	stageDir := filepath.Join(runDir, "s1")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(stageDir, "attempt.marker")
	// Script fails until the marker file exists; the test creates it after
	// the stage blocks, then issues a Retry decision.
	s := flow.Stage{ID: "s1", ScriptBefore: "test -f " + marker}

	done := make(chan bool, 1)
	go func() {
		done <- o.runBeforeHook(context.Background(), s)
	}()

	// Wait for the stage to block in hook_failed (4 failed attempts: 1s+2s+3s backoff).
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if o.opts.Store.Get("s1") == state.StatusHookFailed {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if o.opts.Store.Get("s1") != state.StatusHookFailed {
		t.Fatal("stage did not reach hook_failed")
	}
	if _, ok := readHookPending(stageDir); !ok {
		t.Error("expected hook_pending.json to exist")
	}

	if err := os.WriteFile(marker, nil, 0644); err != nil {
		t.Fatal(err)
	}
	if !o.resolveHook("s1", hookDecisionRetry) {
		t.Fatal("resolveHook returned false")
	}

	select {
	case ok := <-done:
		if !ok {
			t.Error("expected true (proceed) after successful retry")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for runBeforeHook to return")
	}
	if got := o.opts.Store.Get("s1"); got != state.StatusRunning {
		t.Errorf("status = %v, want running after resolution", got)
	}
	if _, ok := readHookPending(stageDir); ok {
		t.Error("expected hook_pending.json to be cleared")
	}
}

func TestRunBeforeHook_BlocksThenSkip(t *testing.T) {
	o, runDir := setupHookOrch(t, "s1")
	stageDir := filepath.Join(runDir, "s1")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	s := flow.Stage{ID: "s1", ScriptBefore: "exit 1"}

	done := make(chan bool, 1)
	go func() {
		done <- o.runBeforeHook(context.Background(), s)
	}()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if o.opts.Store.Get("s1") == state.StatusHookFailed {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !o.resolveHook("s1", hookDecisionSkip) {
		t.Fatal("resolveHook returned false")
	}

	select {
	case ok := <-done:
		if !ok {
			t.Error("expected true (proceed) after skip")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out")
	}
	if got := o.opts.Store.Get("s1"); got != state.StatusRunning {
		t.Errorf("status = %v, want running after skip", got)
	}
}
```

Check whether `NewCriticalBus`/`o.critical.Publish` names match reality — grep `pkg/orchestrator/bus.go` for the constructor name if this doesn't compile, and adjust.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/orchestrator/... -run TestRunBeforeHook -v`
Expected: FAIL — `runBeforeHook` undefined.

- [ ] **Step 3: Write minimal implementation**

Add to `pkg/orchestrator/hooks.go`:

```go
// runBeforeHook runs s.ScriptBefore with retries; on exhaustion it blocks the
// stage in hook_failed until the user retries or skips via the dashboard.
// Returns true once the stage should proceed to its main content (hook
// succeeded or was skipped), false if ctx was cancelled while waiting for a
// decision (full-run shutdown — recovery.go resumes the wait on next start).
func (o *Orchestrator) runBeforeHook(ctx context.Context, s flow.Stage) bool {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	logFile := filepath.Join(stageDir, "before.log")

	for {
		err := runScriptWithRetry(ctx, func() error {
			return o.execScript(ctx, s, "before", s.ScriptBefore, s.ScriptBeforeTimeout, logFile)
		})
		if err == nil {
			return true
		}

		_ = writeHookPending(stageDir, hookPending{Hook: "before", Script: s.ScriptBefore, Timeout: s.ScriptBeforeTimeout})
		_, seq, _ := o.triggerWithSeq(s.ID, EvHookFailed, GuardCtx{}, err.Error())
		_ = o.critical.Publish(ctx, Event{
			Type:    EventHookFailed,
			StageID: s.ID,
			Data:    map[string]string{"hook": "before", "error": err.Error()},
			Seq:     seq,
		})

		decision, ok := o.waitForHookDecision(ctx, s.ID)
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/orchestrator/... -run TestRunBeforeHook -v -timeout 120s`
Expected: PASS (the retry test takes ~6+ seconds due to real 1s/2s/3s backoff — this is intentional, not flaky)

- [ ] **Step 5: Commit**

```bash
git add pkg/orchestrator/hooks.go pkg/orchestrator/hooks_test.go
git commit -m "$(cat <<'EOF'
feat(orchestrator): runBeforeHook — блокирующий гейт script_before с retry/skip

EOF
)"
```

---

### Task 10: `runAfterHook` — non-blocking notice with retry/skip

**Files:**
- Modify: `pkg/orchestrator/hooks.go`
- Test: `pkg/orchestrator/hooks_test.go`

**Interfaces:**
- Consumes: same as Task 9, minus FSM triggers (no status change)
- Produces: `(*Orchestrator).runAfterHook(ctx, s flow.Stage)`

- [ ] **Step 1: Write the failing tests**

Add to `pkg/orchestrator/hooks_test.go`:

```go
func TestRunAfterHook_SucceedsFirstTry_NoEvents(t *testing.T) {
	o, runDir := setupHookOrch(t, "s1")
	if err := o.opts.Store.Apply(&state.Transition{StageID: "s1", From: state.StatusRunning, To: state.StatusDone, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	s := flow.Stage{ID: "s1", ScriptAfter: "echo ok"}
	o.runAfterHook(context.Background(), s)
	if got := o.opts.Store.Get("s1"); got != state.StatusDone {
		t.Errorf("status = %v, want done (unchanged)", got)
	}
	if _, ok := readHookPending(filepath.Join(runDir, "s1")); ok {
		t.Error("no pending hook expected on success")
	}
}

func TestRunAfterHook_FailsThenSkip_StageStaysDone(t *testing.T) {
	o, runDir := setupHookOrch(t, "s1")
	if err := o.opts.Store.Apply(&state.Transition{StageID: "s1", From: state.StatusRunning, To: state.StatusDone, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	stageDir := filepath.Join(runDir, "s1")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	s := flow.Stage{ID: "s1", ScriptAfter: "exit 1"}

	done := make(chan struct{})
	go func() {
		o.runAfterHook(context.Background(), s)
		close(done)
	}()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := readHookPending(stageDir); ok {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, ok := readHookPending(stageDir); !ok {
		t.Fatal("expected hook_pending.json for the failed after-hook")
	}
	// Status must NOT have moved to hook_failed for the after-hook.
	if got := o.opts.Store.Get("s1"); got != state.StatusDone {
		t.Errorf("status = %v, want done (after-hook must never block/revert)", got)
	}

	if !o.resolveHook("s1", hookDecisionSkip) {
		t.Fatal("resolveHook returned false")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out")
	}
	if got := o.opts.Store.Get("s1"); got != state.StatusDone {
		t.Errorf("status = %v, want done after skip", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/orchestrator/... -run TestRunAfterHook -v`
Expected: FAIL — `runAfterHook` undefined.

- [ ] **Step 3: Write minimal implementation**

Add to `pkg/orchestrator/hooks.go`:

```go
// runAfterHook runs s.ScriptAfter after the stage already completed
// successfully. Failure here does NOT touch the FSM — the stage stays done
// regardless. It only surfaces a dismissable EventHookFailed notice with a
// retry/skip decision, mirroring the before-hook's UI but never blocking.
func (o *Orchestrator) runAfterHook(ctx context.Context, s flow.Stage) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	logFile := filepath.Join(stageDir, "after.log")

	for {
		err := runScriptWithRetry(ctx, func() error {
			return o.execScript(ctx, s, "after", s.ScriptAfter, s.ScriptAfterTimeout, logFile)
		})
		if err == nil {
			return
		}

		_ = writeHookPending(stageDir, hookPending{Hook: "after", Script: s.ScriptAfter, Timeout: s.ScriptAfterTimeout})
		o.ui.Publish(Event{
			Type:    EventHookFailed,
			StageID: s.ID,
			Data:    map[string]string{"hook": "after", "error": err.Error()},
		})

		decision, ok := o.waitForHookDecision(ctx, s.ID)
		if !ok {
			return
		}
		clearHookPending(stageDir)
		o.ui.Publish(Event{
			Type:    EventHookResolved,
			StageID: s.ID,
			Data:    map[string]string{"hook": "after", "resolution": resolutionName(decision)},
		})
		if decision == hookDecisionSkip {
			return
		}
		// retry: loop back and re-run the full cycle.
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/orchestrator/... -run TestRunAfterHook -v -timeout 120s`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/orchestrator/hooks.go pkg/orchestrator/hooks_test.go
git commit -m "$(cat <<'EOF'
feat(orchestrator): runAfterHook — неблокирующий script_after с retry/skip, стейдж остаётся done

EOF
)"
```

---

### Task 11: `runScriptStage` + wiring into `startReadyStages`/`tryActivatePrePlanned`

**Files:**
- Modify: `pkg/orchestrator/agents.go` (add function), `pkg/orchestrator/scheduling.go` (add `activateScriptStage`, wire branches)
- Test: `pkg/orchestrator/integration_script_test.go` (new)

**Interfaces:**
- Consumes: `execScript`, `runScriptWithRetry` (Tasks 7-8), `appendNotice`/`EventAgentCompleted`/`o.critical.Publish` (existing, same pattern as `runWithRetry`'s success path)
- Produces: `(*Orchestrator).runScriptStage(ctx, s flow.Stage)`, `(*Orchestrator).activateScriptStage(s) bool`

- [ ] **Step 1: Write the failing integration test**

Create `pkg/orchestrator/integration_script_test.go`:

```go
package orchestrator_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/state"
)

func TestIntegration_ScriptStage_HappyPath(t *testing.T) {
	rootDir := t.TempDir()
	runDir := t.TempDir()
	marker := filepath.Join(rootDir, "script-ran.marker")

	stages := []flow.Stage{{
		ID:     "notify",
		Name:   "Notify",
		Script: "touch " + marker + "; echo done-output",
	}}

	ids := []string{"notify"}
	store, err := state.Open(runDir, ids)
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	orch := orchestrator.New(orchestrator.Options{
		RunDir:  runDir,
		RootDir: rootDir,
		Stages:  stages,
		Store:   store,
		Config:  config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := orch.Run(ctx); err != nil && err != context.DeadlineExceeded {
		t.Fatalf("orch.Run: %v", err)
	}

	if st := orchestrator.StoreFromOrch(orch).Get("notify"); st != state.StatusDone {
		t.Fatalf("expected status done, got %s", st)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("script side effect (marker file) missing: %v", err)
	}

	logData, err := os.ReadFile(filepath.Join(runDir, "notify", "script.log"))
	if err != nil {
		t.Fatalf("script.log missing: %v", err)
	}
	if !strings.Contains(string(logData), "done-output") {
		t.Errorf("script.log missing output: %q", string(logData))
	}
}
```

Verify `orchestrator.StoreFromOrch`/`orchestrator.DefaultPrompts`/`config.Default` exist with these exact names (they're used in Task's earlier research from `integration_supervisor_test.go`) — grep first if unsure: `grep -n "func StoreFromOrch\|func DefaultPrompts" pkg/orchestrator/*.go`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/orchestrator/... -run TestIntegration_ScriptStage_HappyPath -v`
Expected: FAIL — script stage never activates (no `IsScript()` handling yet), test times out or stage stays `pending`/never reaches `done`.

- [ ] **Step 3: Write minimal implementation**

Add to `pkg/orchestrator/agents.go` (near `runAutonomousAgent`):

```go
// phaseScript identifies a script-stage's own log namespace (script.log),
// distinct from the before/after hook logs.
const phaseScript = "script"

// runScriptStage executes a script-only stage (Stage.IsScript()): no plan.md,
// no approval, no LLM agent — just Stage.Script via execScript, retried with
// the same fixed 3x/1-2-3s policy as hooks (a script failure is just as
// deterministic and fast as a hook failure).
func (o *Orchestrator) runScriptStage(ctx context.Context, s flow.Stage) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		o.Trigger(s.ID, EvFail, GuardCtx{}, "mkdir failed")
		return
	}
	logFile := filepath.Join(stageDir, phaseScript+".log")

	err := runScriptWithRetry(ctx, func() error {
		return o.execScript(ctx, s, phaseScript, s.Script, s.ScriptTimeout, logFile)
	})
	if err != nil {
		o.Trigger(s.ID, EvFail, GuardCtx{}, err.Error())
		o.failBlockedStages()
		return
	}

	appendNotice(o.opts.RunDir, s.ID, string(EventAgentCompleted), phaseScript)
	_ = o.critical.Publish(ctx, Event{Type: EventAgentCompleted, StageID: s.ID, Data: phaseScript})
}
```

Add to `pkg/orchestrator/scheduling.go`, right after `activateAutoStage` (after line 39):

```go
// activateScriptStage activates a script-only stage (Stage.IsScript()) the
// same way activateAutoStage activates an auto stage: no plan.md, straight
// to Ready. Returns false (no-op) if s is not a script stage.
func (o *Orchestrator) activateScriptStage(s flow.Stage) bool {
	if !s.IsScript() {
		return false
	}
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		o.Trigger(s.ID, EvFail, GuardCtx{}, "mkdir failed")
		return true
	}
	o.Trigger(s.ID, EvReady, GuardCtx{}, "script stage")
	return true
}
```

In `tryActivatePrePlanned` (scheduling.go, inside the loop), add the script branch right after the `activateAutoStage` check (after line 61 in the original, i.e. right after `if o.activateAutoStage(s) { continue }`):

```go
		if o.activateAutoStage(s) {
			continue
		}
		if o.activateScriptStage(s) {
			continue
		}
```

In `startReadyStages`, the current code (from the existing `stageDir := ...` line through the final `spawnAgent` call) reads:

```go
		stageDir := filepath.Join(o.opts.RunDir, id)
		if isAutonomousStage(stageDir) || stage.IsAuto() {
			if stage.IsAuto() {
				_ = os.WriteFile(filepath.Join(stageDir, "autonomous.flag"), nil, 0644)
			}
			o.spawnAgent(ctx, *stage, o.runAutonomousAgent)
			continue
		}
		o.spawnAgent(ctx, *stage, o.runImplementationAgent)
```

**Replace that entire block** (not just insert before it — the `stageDir :=` line must appear exactly once) with:

```go
		stageDir := filepath.Join(o.opts.RunDir, id)
		if stage.IsScript() {
			o.spawnAgent(ctx, *stage, o.withBeforeHook(o.runScriptStage))
			continue
		}
		if isAutonomousStage(stageDir) || stage.IsAuto() {
			if stage.IsAuto() {
				_ = os.WriteFile(filepath.Join(stageDir, "autonomous.flag"), nil, 0644)
			}
			o.spawnAgent(ctx, *stage, o.withBeforeHook(o.runAutonomousAgent))
			continue
		}
		o.spawnAgent(ctx, *stage, o.withBeforeHook(o.runImplementationAgent))
```

(Note: `withBeforeHook` doesn't exist yet — this task will not compile until Task 12 adds it. Write Task 12 immediately after this one in the same session before running the full suite, or stub `withBeforeHook` as `func (o *Orchestrator) withBeforeHook(fn func(context.Context, flow.Stage)) func(context.Context, flow.Stage) { return fn }` temporarily in this task, then replace it for real in Task 12. **Prefer doing Task 12 immediately after this step** rather than stubbing, to avoid a throwaway edit — see Task 12.)

- [ ] **Step 4: Run test to verify it passes**

This step depends on Task 12's `withBeforeHook` existing. Complete Task 12's Step 3 (the `withBeforeHook` implementation only, not its own tests) before running this test. Then:

Run: `go test ./pkg/orchestrator/... -run TestIntegration_ScriptStage_HappyPath -v`
Expected: PASS

- [ ] **Step 5: Commit**

Commit Task 11 and Task 12 together, since Task 11 doesn't compile without Task 12's `withBeforeHook`:

```bash
git add pkg/orchestrator/agents.go pkg/orchestrator/scheduling.go pkg/orchestrator/hooks.go pkg/orchestrator/integration_script_test.go
git commit -m "$(cat <<'EOF'
feat(orchestrator): runScriptStage + активация script-стейджа + withBeforeHook

EOF
)"
```

---

### Task 12: `withBeforeHook` + wiring into `retryStage`

**Files:**
- Modify: `pkg/orchestrator/hooks.go` (add `withBeforeHook`), `pkg/orchestrator/scheduling.go` (wire into `retryStage`'s 3 non-planning spawns)
- Test: `pkg/orchestrator/integration_hooks_test.go` (new)

**Interfaces:**
- Consumes: `runBeforeHook` (Task 9)
- Produces: `(*Orchestrator).withBeforeHook(mainFn func(context.Context, flow.Stage)) func(context.Context, flow.Stage)`

- [ ] **Step 1: Write the failing integration test**

Create `pkg/orchestrator/integration_hooks_test.go`:

```go
package orchestrator_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/state"
)

// scriptStageRunner is a minimal executor.Runner mock — script stages bypass
// runnerFor entirely (execScript builds its own executor.New), so Runner is
// unused here but Options requires the field name to exist if we ever add
// mixed agent+script flows; omitted for this pure-script test.

func TestIntegration_ScriptBefore_FailsThenSkip(t *testing.T) {
	rootDir := t.TempDir()
	runDir := t.TempDir()
	mainMarker := filepath.Join(rootDir, "main-ran.marker")

	stages := []flow.Stage{{
		ID:           "notify",
		Name:         "Notify",
		Script:       "touch " + mainMarker,
		ScriptBefore: "exit 1", // always fails
	}}

	store, err := state.Open(runDir, []string{"notify"})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	orch := orchestrator.New(orchestrator.Options{
		RunDir:  runDir,
		RootDir: rootDir,
		Stages:  stages,
		Store:   store,
		Config:  config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- orch.Run(ctx) }()

	stateFile := filepath.Join(runDir, "state.json")
	waitForStatus(t, stateFile, "notify", state.StatusHookFailed, 20*time.Second)

	// Main script must NOT have run yet.
	if _, err := os.Stat(mainMarker); err == nil {
		t.Fatal("main script ran before before-hook was resolved")
	}

	if err := orch.SkipHook("notify"); err != nil {
		t.Fatalf("SkipHook: %v", err)
	}

	waitForStatus(t, stateFile, "notify", state.StatusDone, 20*time.Second)
	if _, err := os.Stat(mainMarker); err != nil {
		t.Error("main script should have run after skip")
	}

	cancel()
	<-runDone
}

func TestIntegration_ScriptBefore_FailsThenRetrySucceeds(t *testing.T) {
	rootDir := t.TempDir()
	runDir := t.TempDir()
	gateFile := filepath.Join(rootDir, "gate")
	mainMarker := filepath.Join(rootDir, "main-ran.marker")

	stages := []flow.Stage{{
		ID:           "notify",
		Name:         "Notify",
		Script:       "touch " + mainMarker,
		ScriptBefore: "test -f " + gateFile,
	}}

	store, err := state.Open(runDir, []string{"notify"})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	orch := orchestrator.New(orchestrator.Options{
		RunDir:  runDir,
		RootDir: rootDir,
		Stages:  stages,
		Store:   store,
		Config:  config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- orch.Run(ctx) }()

	stateFile := filepath.Join(runDir, "state.json")
	waitForStatus(t, stateFile, "notify", state.StatusHookFailed, 20*time.Second)

	if err := os.WriteFile(gateFile, nil, 0644); err != nil {
		t.Fatal(err)
	}
	if err := orch.RetryHook("notify"); err != nil {
		t.Fatalf("RetryHook: %v", err)
	}

	waitForStatus(t, stateFile, "notify", state.StatusDone, 20*time.Second)
	if _, err := os.Stat(mainMarker); err != nil {
		t.Error("main script should have run after successful retry")
	}

	cancel()
	<-runDone
}
```

This reuses the `waitForStatus` helper from `pkg/orchestrator/integration_test.go:104-120` (same package `orchestrator_test`, already shared across integration test files — no new helper needed).

`orch.SkipHook`/`orch.RetryHook` don't exist yet — that's Task 15. **This task's test will not fully pass until Task 15 lands.** Write the test now (it documents the target behavior), confirm it fails to compile for the right reason (`SkipHook`/`RetryHook` undefined), implement `withBeforeHook` (this task), then come back and run this test for real once Task 15 is done. Note this dependency clearly when executing the plan.

- [ ] **Step 2: Run test to verify it fails**

Run: `go build ./...`
Expected: FAIL — compile error, `orch.SkipHook`/`orch.RetryHook` undefined (expected at this point; these land in Task 15).

- [ ] **Step 3: Write minimal implementation**

Add to `pkg/orchestrator/hooks.go`:

```go
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
		mainFn(ctx, s)
	}
}
```

In `pkg/orchestrator/scheduling.go`'s `retryStage`, wrap the three non-planning spawn calls:

```go
		o.spawnAgent(ctx, *stage, o.withBeforeHook(o.runAutonomousAgent))
		o.startReadyStages(ctx)
		return
	}
```
(replacing the bare `o.spawnAgent(ctx, *stage, o.runAutonomousAgent)` at line 212)

```go
		o.spawnAgent(ctx, *stage, o.withBeforeHook(o.runImplementationAgent))
		o.startReadyStages(ctx)
		return
	}
```
(replacing line 244)

```go
		o.spawnAgent(ctx, *stage, o.withBeforeHook(o.runImplementationAgent))
	} else {
```
(replacing line 257)

Leave line 269 (`o.spawnAgent(ctx, *stage, o.runPlanningAgent)`, the planning-fallback branch) **unwrapped** — planning is not "the stage's real work," so `script_before` must not fire there (it fires later, at the `startReadyStages` spawn that follows plan approval).

- [ ] **Step 4: Run test to verify it passes**

Run: `go build ./...` — should now succeed except for the `SkipHook`/`RetryHook` references in `integration_hooks_test.go`, which is expected until Task 15. Run the rest of the suite to confirm no regressions:

Run: `go test ./pkg/orchestrator/... -run 'TestIntegration_ScriptStage_HappyPath|TestIntegration_AutoStage|TestIntegration_Supervisor|TestFullDialogCycle' -v`
Expected: PASS (existing auto/supervisor/dialog tests unaffected by the wrapping, since none of those stages set `ScriptBefore`)

- [ ] **Step 5: Commit**

```bash
git add pkg/orchestrator/hooks.go pkg/orchestrator/scheduling.go pkg/orchestrator/integration_hooks_test.go
git commit -m "$(cat <<'EOF'
feat(orchestrator): withBeforeHook — врезка script_before в startReadyStages/retryStage

EOF
)"
```

---

### Task 13: `maybeRunAfterHook` wired into `onAgentCompleted`

**Files:**
- Modify: `pkg/orchestrator/orchestrator.go:385` (insert call), `pkg/orchestrator/hooks.go` (add function)

**Interfaces:**
- Consumes: `runAfterHook` (Task 10), `o.graph.Stage(id)` (existing), `o.spawnAgent` (existing, unchanged)
- Produces: `(*Orchestrator).maybeRunAfterHook(ctx, stageID string)`

- [ ] **Step 1: Write the failing test**

This is exercised by `TestIntegration_ScriptAfter_FailsThenSkip`/`TestIntegration_ScriptAfter_FailsThenRetry` below — add both to `pkg/orchestrator/integration_hooks_test.go`:

```go
func TestIntegration_ScriptAfter_FailsThenSkip_StageStaysDone(t *testing.T) {
	rootDir := t.TempDir()
	runDir := t.TempDir()

	stages := []flow.Stage{{
		ID:          "notify",
		Name:        "Notify",
		Script:      "echo main-ok",
		ScriptAfter: "exit 1",
	}}

	store, err := state.Open(runDir, []string{"notify"})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	orch := orchestrator.New(orchestrator.Options{
		RunDir:  runDir,
		RootDir: rootDir,
		Stages:  stages,
		Store:   store,
		Config:  config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- orch.Run(ctx) }()

	stateFile := filepath.Join(runDir, "state.json")
	// The stage reaches done immediately (after-hook failure never blocks it).
	waitForStatus(t, stateFile, "notify", state.StatusDone, 20*time.Second)

	// Give the async after-hook goroutine time to fail and write its pending marker.
	stageDir := filepath.Join(runDir, "notify")
	deadline := time.Now().Add(20 * time.Second)
	pendingPath := filepath.Join(stageDir, "hook_pending.json")
	for time.Now().Before(deadline) {
		if _, err := os.Stat(pendingPath); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := os.Stat(pendingPath); err != nil {
		t.Fatal("expected hook_pending.json for the failed after-hook")
	}

	if err := orch.SkipHook("notify"); err != nil {
		t.Fatalf("SkipHook: %v", err)
	}
	// Status must remain done throughout.
	if st := orchestrator.StoreFromOrch(orch).Get("notify"); st != state.StatusDone {
		t.Errorf("status = %v, want done", st)
	}

	cancel()
	<-runDone
}
```

(`SkipHook` again depends on Task 15 — same cross-task dependency noted as in Task 12.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go build ./...`
Expected: compiles except for `SkipHook` (expected, Task 15); the after-hook itself never fires yet since `onAgentCompleted` doesn't call it.

- [ ] **Step 3: Write minimal implementation**

Add to `pkg/orchestrator/hooks.go`:

```go
// maybeRunAfterHook fires the stage's script_after hook (if any) in a tracked
// goroutine via spawnAgent, reusing its semaphore/agentWG bookkeeping — the
// hook may block for an arbitrarily long time waiting on a user decision, so
// it must never run inline in onAgentCompleted (an event-loop callback that
// must return promptly).
func (o *Orchestrator) maybeRunAfterHook(ctx context.Context, stageID string) {
	stage := o.graph.Stage(stageID)
	if stage == nil || stage.ScriptAfter == "" {
		return
	}
	o.spawnAgent(ctx, *stage, o.runAfterHook)
}
```

In `pkg/orchestrator/orchestrator.go`'s `onAgentCompleted`, insert the call right after the `EvComplete` trigger (line 385), before `o.failBlockedStages()` (line 386):

```go
		o.Trigger(ev.StageID, EvComplete, GuardCtx{}, "")
		o.maybeRunAfterHook(ctx, ev.StageID)
		o.failBlockedStages()
```

- [ ] **Step 4: Run test to verify it passes**

This test still needs `SkipHook` from Task 15 to fully pass; for now confirm the after-hook actually fires and writes its pending marker (the part of the test before `orch.SkipHook` is called):

Run: `go test ./pkg/orchestrator/... -run TestIntegration_ScriptAfter -v -timeout 60s` (expect it to get through the `hook_pending.json` wait and then fail at the `SkipHook` call — that's expected until Task 15).

- [ ] **Step 5: Commit**

```bash
git add pkg/orchestrator/hooks.go pkg/orchestrator/orchestrator.go pkg/orchestrator/integration_hooks_test.go
git commit -m "$(cat <<'EOF'
feat(orchestrator): maybeRunAfterHook — врезка script_after в onAgentCompleted

EOF
)"
```

---

### Task 14: Recovery — resume `hook_failed` and pending after-hook

**Files:**
- Modify: `pkg/orchestrator/recovery.go`
- Test: `pkg/orchestrator/recovery_hooks_test.go` (new)

**Interfaces:**
- Consumes: `readHookPending`, `waitForHookDecision` (Task 7), `state.StatusHookFailed` (Task 3)

- [ ] **Step 1: Read the current recovery entry point**

Run: `grep -n "^func (o \*Orchestrator)" pkg/orchestrator/recovery.go`

Read the full function that dispatches based on `state.StageStatus` (likely named `Recover`, `resume`, or similar, calling `startPlanningForPending`/`resumeInteractiveAgent` per status) — this is necessary before adding a new case, since its exact current switch/dispatch shape wasn't fully captured in earlier research. Note its name and signature for the steps below.

- [ ] **Step 2: Write the failing tests**

Create `pkg/orchestrator/recovery_hooks_test.go` (adjust the call to the top-level recovery entry point once its real name is known from Step 1):

```go
package orchestrator_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/state"
)

func TestRecovery_ResumesHookFailed(t *testing.T) {
	rootDir := t.TempDir()
	runDir := t.TempDir()
	mainMarker := filepath.Join(rootDir, "main-ran.marker")

	stages := []flow.Stage{{
		ID:           "notify",
		Name:         "Notify",
		Script:       "touch " + mainMarker,
		ScriptBefore: "exit 1",
	}}

	// Simulate a prior crash: stage already in hook_failed with a pending
	// before-hook recorded on disk (as runBeforeHook would have left it).
	store, err := state.Open(runDir, []string{"notify"})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	if err := store.Apply(&state.Transition{StageID: "notify", From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(&state.Transition{StageID: "notify", From: state.StatusRunning, To: state.StatusHookFailed, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	stageDir := filepath.Join(runDir, "notify")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	store.Close() // simulate process exit

	// Reopen (simulating `afm run` restart) and re-run with a fresh Orchestrator.
	store2, err := state.Open(runDir, []string{"notify"})
	if err != nil {
		t.Fatalf("state.Open (reopen): %v", err)
	}
	t.Cleanup(func() { store2.Close() })

	orch := orchestrator.New(orchestrator.Options{
		RunDir:  runDir,
		RootDir: rootDir,
		Stages:  stages,
		Store:   store2,
		Config:  config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- orch.Run(ctx) }()

	// The stage should still be waiting in hook_failed (not silently retried).
	time.Sleep(500 * time.Millisecond)
	if got := orchestrator.StoreFromOrch(orch).Get("notify"); got != state.StatusHookFailed {
		t.Fatalf("status = %v, want hook_failed (resumed, not auto-retried)", got)
	}

	if err := orch.SkipHook("notify"); err != nil {
		t.Fatalf("SkipHook: %v", err)
	}

	stateFile := filepath.Join(runDir, "state.json")
	waitForStatus(t, stateFile, "notify", state.StatusDone, 20*time.Second)
	if _, err := os.Stat(mainMarker); err != nil {
		t.Error("main script should have run after skip")
	}

	cancel()
	<-runDone
}
```

(Again depends on `SkipHook`, Task 15 — same cross-task note applies.)

- [ ] **Step 3: Run test to verify it fails**

Run: `go build ./...` then `go test ./pkg/orchestrator/... -run TestRecovery_ResumesHookFailed -v`
Expected: the stage does NOT resume into `hook_failed`'s wait state (nothing currently detects `StatusHookFailed` on resume) — the test will hang or the status check will fail.

- [ ] **Step 4: Write minimal implementation**

In `pkg/orchestrator/recovery.go`, using the entry-point function found in Step 1, add a case for `state.StatusHookFailed`:

```go
	case state.StatusHookFailed:
		o.spawnAgent(ctx, s, o.resumeHookFailedWait)
```

Add the new method to `pkg/orchestrator/hooks.go`:

```go
// resumeHookFailedWait resumes a stage that crashed while blocked in
// hook_failed: re-reads the persisted hook_pending.json (written by
// runBeforeHook before blocking) and re-enters the wait for a user decision,
// WITHOUT silently re-attempting the hook — matching runBeforeHook's own
// retry-decision loop from that point on.
func (o *Orchestrator) resumeHookFailedWait(ctx context.Context, s flow.Stage) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	pending, ok := readHookPending(stageDir)
	if !ok || pending.Hook != "before" {
		// Nothing to resume from disk; fail safe rather than guessing.
		o.Trigger(s.ID, EvFail, GuardCtx{}, "hook_failed with no pending hook on disk")
		return
	}
	decision, ok := o.waitForHookDecision(ctx, s.ID)
	if !ok {
		return
	}
	clearHookPending(stageDir)
	_, seq, _ := o.triggerWithSeq(s.ID, EvHookResolved, GuardCtx{}, "before hook "+resolutionName(decision))
	o.ui.Publish(Event{
		Type:    EventHookResolved,
		StageID: s.ID,
		Data:    map[string]string{"hook": "before", "resolution": resolutionName(decision)},
		Seq:     seq,
	})
	if decision == hookDecisionSkip {
		o.dispatchMainAfterBeforeHook(ctx, s)
		return
	}
	// Retry: re-enter the normal before-hook flow from scratch.
	if o.runBeforeHook(ctx, s) {
		o.dispatchMainAfterBeforeHook(ctx, s)
	}
}

// dispatchMainAfterBeforeHook runs a stage's real content after its
// before-hook resolved during a resume (script/autonomous/implementation),
// mirroring the branch startReadyStages uses for a fresh activation.
func (o *Orchestrator) dispatchMainAfterBeforeHook(ctx context.Context, s flow.Stage) {
	switch {
	case s.IsScript():
		o.runScriptStage(ctx, s)
	case s.IsAuto():
		o.runAutonomousAgent(ctx, s)
	default:
		o.runImplementationAgent(ctx, s)
	}
}
```

Also add a scan for pending after-hooks on resume — locate the top-level function iterating all stages during recovery (found in Step 1) and add, for every stage regardless of status:

```go
	if _, ok := readHookPending(filepath.Join(o.opts.RunDir, s.ID)); ok {
		if pending, _ := readHookPending(filepath.Join(o.opts.RunDir, s.ID)); pending.Hook == "after" {
			o.spawnAgent(ctx, s, o.runAfterHook)
			continue // already resumed; skip the normal status-based dispatch below for this stage
		}
	}
```

Place this check before the main status-based switch for each stage, since a `done` stage with a pending after-hook wouldn't otherwise be revisited by the status switch at all.

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./pkg/orchestrator/... -run TestRecovery_ResumesHookFailed -v -timeout 60s`
Expected: PASS once Task 15's `SkipHook` also lands (note the cross-task dependency — implement Task 15 next, then return to verify this test).

- [ ] **Step 6: Commit**

```bash
git add pkg/orchestrator/recovery.go pkg/orchestrator/hooks.go pkg/orchestrator/recovery_hooks_test.go
git commit -m "$(cat <<'EOF'
feat(orchestrator): resume hook_failed и pending after-hook после краша

EOF
)"
```

---

### Task 15: `RetryHook`/`SkipHook` orchestrator methods + HTTP endpoints

**Files:**
- Modify: `pkg/orchestrator/control_api.go` (add methods), `pkg/server/handlers.go` (add handlers), `pkg/server/server.go` (route + struct fields), `cmd/afm/run.go` (wire)
- Test: `pkg/orchestrator/control_api_test.go` (check if exists first), `pkg/server/handlers_test.go`

**Interfaces:**
- Consumes: `resolveHook`, `hookDecisionRetry`/`hookDecisionSkip` (Task 7)
- Produces: `(*Orchestrator).RetryHook(stageID string) error`, `(*Orchestrator).SkipHook(stageID string) error`

- [ ] **Step 1: Write the failing orchestrator-level test**

Check for an existing test file first: `ls pkg/orchestrator/control_api_test.go 2>/dev/null || echo none`. Add to that file (or create it, matching `control_api.go`'s package/import style — `package orchestrator`):

```go
func TestRetryHook_NoWaiterReturnsError(t *testing.T) {
	o := &Orchestrator{}
	if err := o.RetryHook("nonexistent"); err == nil {
		t.Error("expected error when no stage is waiting on a hook decision")
	}
}

func TestSkipHook_DeliversDecision(t *testing.T) {
	o := &Orchestrator{}
	done := make(chan hookDecision, 1)
	go func() {
		d, _ := o.waitForHookDecision(context.Background(), "s1")
		done <- d
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := o.SkipHook("s1"); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case d := <-done:
		if d != hookDecisionSkip {
			t.Errorf("decision = %v, want skip", d)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/orchestrator/... -run 'TestRetryHook|TestSkipHook' -v`
Expected: FAIL — `RetryHook`/`SkipHook` undefined.

- [ ] **Step 3: Write minimal implementation (orchestrator layer)**

Add to `pkg/orchestrator/control_api.go`:

```go
// RetryHook resumes a stage currently blocked on a failed before/after hook
// by re-running that hook's 3x/1-2-3s retry cycle.
func (o *Orchestrator) RetryHook(stageID string) error {
	if !o.resolveHook(stageID, hookDecisionRetry) {
		return fmt.Errorf("stage %q has no hook awaiting a decision", stageID)
	}
	return nil
}

// SkipHook resumes a stage currently blocked on a failed before/after hook
// by skipping it entirely.
func (o *Orchestrator) SkipHook(stageID string) error {
	if !o.resolveHook(stageID, hookDecisionSkip) {
		return fmt.Errorf("stage %q has no hook awaiting a decision", stageID)
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/orchestrator/... -run 'TestRetryHook|TestSkipHook' -v`
Expected: PASS. Then re-run the tasks 12/13/14 tests that depended on `SkipHook`/`RetryHook`:

Run: `go test ./pkg/orchestrator/... -v -timeout 180s`
Expected: full package PASS, including `TestIntegration_ScriptBefore_FailsThenSkip`, `TestIntegration_ScriptBefore_FailsThenRetrySucceeds`, `TestIntegration_ScriptAfter_FailsThenSkip_StageStaysDone`, `TestRecovery_ResumesHookFailed`.

- [ ] **Step 5: Read the existing `handleRetry` handler for the exact pattern to mirror**

Run: `grep -n "^func (s \*Server) handleRetry\|^func extractStageID\|^func isValidStageID" pkg/server/handlers.go`

Read the full `handleRetry` function body plus `extractStageID`/`isValidStageID` (or whatever the actual helper names/signatures are) before writing the new handlers, so the new code matches exactly.

- [ ] **Step 6: Write the failing HTTP handler test**

Add to `pkg/server/handlers_test.go`, matching whatever setup helper the existing `TestHandleRetry` test uses (found in Step 5):

```go
func TestHandleRetryHook_Success(t *testing.T) {
	srv, _ := newTestServer(t) // reuse the same setup helper TestHandleRetry uses
	called := ""
	srv.retryHookFn = func(stageID string) error {
		called = stageID
		return nil
	}

	req := httptest.NewRequest(http.MethodPost, "/api/stages/s1/retry-hook", nil)
	w := httptest.NewRecorder()
	srv.routeStages(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if called != "s1" {
		t.Errorf("retryHookFn called with %q, want s1", called)
	}
}

func TestHandleSkipHook_Success(t *testing.T) {
	srv, _ := newTestServer(t)
	called := ""
	srv.skipHookFn = func(stageID string) error {
		called = stageID
		return nil
	}

	req := httptest.NewRequest(http.MethodPost, "/api/stages/s1/skip-hook", nil)
	w := httptest.NewRecorder()
	srv.routeStages(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if called != "s1" {
		t.Errorf("skipHookFn called with %q, want s1", called)
	}
}

func TestHandleRetryHook_FnReturnsError(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.retryHookFn = func(stageID string) error {
		return fmt.Errorf("stage %q has no hook awaiting a decision", stageID)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/stages/s1/retry-hook", nil)
	w := httptest.NewRecorder()
	srv.routeStages(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", w.Code, w.Body.String())
	}
}
```

Adjust `newTestServer`/field names (`srv.retryHookFn` etc.) to match whatever the real setup helper and `Server` struct field naming convention turns out to be from Step 5 — the existing `Server` struct (confirmed in research) uses lowercase unexported fields like `retryFn`, so the new ones should be `retryHookFn`/`skipHookFn` for consistency.

- [ ] **Step 7: Run test to verify it fails**

Run: `go test ./pkg/server/... -run 'TestHandleRetryHook|TestHandleSkipHook' -v`
Expected: FAIL — compile error, `retryHookFn`/`skipHookFn` fields and `/retry-hook`/`/skip-hook` routing don't exist.

- [ ] **Step 8: Write minimal implementation (server layer)**

In `pkg/server/server.go`, add two fields to `Server` (after `retryFn`, line 78):

```go
	retryHookFn func(stageID string) error
	skipHookFn  func(stageID string) error
```

Add matching fields to `Config` (after `RetryFn`, line 105):

```go
	RetryHookFn func(stageID string) error
	SkipHookFn  func(stageID string) error
```

Wire them in `New` (after `retryFn: cfg.RetryFn,`, line 140):

```go
		retryHookFn:         cfg.RetryHookFn,
		skipHookFn:          cfg.SkipHookFn,
```

Add two new case arms to `routeStages` (after the `/retry` case, line 285):

```go
	case strings.HasSuffix(path, "/retry-hook") && r.Method == http.MethodPost:
		s.handleRetryHook(w, r)
	case strings.HasSuffix(path, "/skip-hook") && r.Method == http.MethodPost:
		s.handleSkipHook(w, r)
```

(Order matters: Go's `switch` with no fallthrough evaluates cases top-to-bottom and stops at the first match, but since `/retry-hook` and `/retry` are checked with distinct `HasSuffix` calls on the full literal suffix, there's no overlap regardless of order — `strings.HasSuffix(".../retry-hook", "/retry")` is `false`. Placing the new cases after `/retry` is purely stylistic grouping.)

In `pkg/server/handlers.go`, add the two handlers, matching the exact helper calls found in Step 5 (adjust `extractStageID`/`isValidStageID` calls to the real signatures observed):

```go
func (s *Server) handleRetryHook(w http.ResponseWriter, r *http.Request) {
	stageID := extractStageID(r.URL.Path, "/retry-hook")
	if !isValidStageID(stageID) {
		http.Error(w, "invalid stage id", http.StatusBadRequest)
		return
	}
	if s.retryHookFn == nil {
		http.Error(w, "retry-hook not supported", http.StatusNotImplemented)
		return
	}
	if err := s.retryHookFn(stageID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "retried", "stage_id": stageID})
}

func (s *Server) handleSkipHook(w http.ResponseWriter, r *http.Request) {
	stageID := extractStageID(r.URL.Path, "/skip-hook")
	if !isValidStageID(stageID) {
		http.Error(w, "invalid stage id", http.StatusBadRequest)
		return
	}
	if s.skipHookFn == nil {
		http.Error(w, "skip-hook not supported", http.StatusNotImplemented)
		return
	}
	if err := s.skipHookFn(stageID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "skipped", "stage_id": stageID})
}
```

- [ ] **Step 9: Run test to verify it passes**

Run: `go test ./pkg/server/... -run 'TestHandleRetryHook|TestHandleSkipHook' -v`
Expected: PASS

- [ ] **Step 10: Wire into `cmd/afm/run.go`**

In `cmd/afm/run.go`, add to the `server.Config{...}` literal (after `RetryFn: orch.Retry,`, line 263):

```go
					RetryHookFn:         orch.RetryHook,
					SkipHookFn:          orch.SkipHook,
```

- [ ] **Step 11: Full build + test check**

Run: `go build ./... && go test ./... -race`
Expected: PASS across the whole repo.

- [ ] **Step 12: Commit**

```bash
git add pkg/orchestrator/control_api.go pkg/orchestrator/control_api_test.go pkg/server/handlers.go pkg/server/server.go pkg/server/handlers_test.go cmd/afm/run.go
git commit -m "$(cat <<'EOF'
feat(server): эндпойнты retry-hook/skip-hook + оркестраторские RetryHook/SkipHook

EOF
)"
```

---

### Task 16: Log handler — concatenate `before.log`/`script.log`/`after.log`

**Files:**
- Modify: `pkg/server/handlers.go` (the `/log` handler, `handleLog`)
- Test: `pkg/server/handlers_test.go`

- [ ] **Step 1: Read the current log handler**

Run: `grep -n "^func (s \*Server) handleLog" pkg/server/handlers.go` and read its full body.

- [ ] **Step 2: Write the failing test**

Add to `pkg/server/handlers_test.go` (using whatever `newTestServer`-style helper exists, writing real files into the run dir's stage directory):

```go
func TestHandleLog_ConcatenatesHookLogs(t *testing.T) {
	srv, runDir := newTestServer(t)
	stageDir := filepath.Join(runDir, "s1")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "before.log"), []byte("BEFORE-CONTENT\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "script.log"), []byte("SCRIPT-CONTENT\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "after.log"), []byte("AFTER-CONTENT\n"), 0644); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/stages/s1/log", nil)
	w := httptest.NewRecorder()
	srv.routeStages(w, req)

	body := w.Body.String()
	beforeIdx := strings.Index(body, "BEFORE-CONTENT")
	scriptIdx := strings.Index(body, "SCRIPT-CONTENT")
	afterIdx := strings.Index(body, "AFTER-CONTENT")
	if beforeIdx == -1 || scriptIdx == -1 || afterIdx == -1 {
		t.Fatalf("log body missing hook content: %q", body)
	}
	if !(beforeIdx < scriptIdx && scriptIdx < afterIdx) {
		t.Errorf("expected order before < script < after, got positions %d, %d, %d", beforeIdx, scriptIdx, afterIdx)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./pkg/server/... -run TestHandleLog_ConcatenatesHookLogs -v`
Expected: FAIL — current handler doesn't know about `before.log`/`script.log`/`after.log`.

- [ ] **Step 4: Write minimal implementation**

Based on the handler body read in Step 1 (which concatenates phase log files for the stage), add reads for `before.log` (prepended, if present) and `after.log` (appended, if present) around the existing phase-log concatenation, and include `script.log` alongside/instead of the phase logs for script stages. Since the exact current loop structure depends on Step 1's findings, apply this pattern: build the ordered list of log file paths as `[before.log (if exists), <existing phase log files...>, script.log (if exists), after.log (if exists)]`, then concatenate their contents in that order, skipping files that don't exist (matching the tolerant behavior the existing handler already has for missing phase-log variants).

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./pkg/server/... -run TestHandleLog_ConcatenatesHookLogs -v`
Expected: PASS

- [ ] **Step 6: Run the full server test suite**

Run: `go test ./pkg/server/... -v`
Expected: PASS (no regression on existing `/log` behavior for non-hook stages, since `before.log`/`after.log`/`script.log` simply don't exist for them and are skipped)

- [ ] **Step 7: Commit**

```bash
git add pkg/server/handlers.go pkg/server/handlers_test.go
git commit -m "$(cat <<'EOF'
feat(server): /log отдаёт before.log/script.log/after.log вместе с логом стейджа

EOF
)"
```

---

### Task 17: Dashboard — `EventScriptOutput`/`EventHookFailed`/`EventHookResolved` in the event feed

**Files:**
- Modify: `pkg/web/dashboard/src/types/afm-event.ts`, `pkg/web/dashboard/src/components/event-feed/EventFeedPanel.tsx`
- Test: `pkg/web/dashboard/src/components/event-feed/EventFeedPanel.test.tsx`

**Interfaces:**
- Consumes: `AfmEvent` type (existing)

- [ ] **Step 1: Write the failing test**

Add to `EventFeedPanel.test.tsx` (matching the existing test's style shown in research):

```tsx
test('renders script_output, hook_failed, and hook_resolved lines', () => {
  const events: AfmEvent[] = [
    { type: 'script_output', payload: { hook: 'before', line: 'setting up' }, stageId: 's1', timestamp: '2026-07-29T10:00:00Z' },
    { type: 'hook_failed', payload: { hook: 'before', error: 'exit 1' }, stageId: 's1', timestamp: '2026-07-29T10:00:01Z' },
    { type: 'hook_resolved', payload: { hook: 'before', resolution: 'skipped' }, stageId: 's1', timestamp: '2026-07-29T10:00:02Z' },
  ]

  const { container } = render(<EventFeedPanel events={events} />)
  const entries = container.querySelectorAll('.feed-entry')
  expect(entries).toHaveLength(3)

  expect(entries[0]?.textContent).toContain('before')
  expect(entries[0]?.textContent).toContain('setting up')
  expect(entries[1]?.textContent).toContain('before')
  expect(entries[1]?.textContent).toContain('exit 1')
  expect(entries[2]?.textContent).toContain('skipped')
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd pkg/web/dashboard && npm test -- EventFeedPanel`
Expected: FAIL — `script_output`/`hook_failed`/`hook_resolved` fall through to the `default:` branch, rendering just the raw type string (so the specific text assertions like `'setting up'` fail).

- [ ] **Step 3: Write minimal implementation**

In `pkg/web/dashboard/src/types/afm-event.ts`, add to `AFM_EVENT_TYPES`:

```ts
export const AFM_EVENT_TYPES = [
  'stage_status_changed',
  'approved',
  'revised',
  'retry_scheduled',
  'retry_exhausted',
  'manual_retry',
  'ask_user',
  'user_answered',
  'agent_action',
  'agent_completed',
  'supervisor_decision',
  'script_output',
  'hook_failed',
  'hook_resolved',
] as const
```

In `EventFeedPanel.tsx`, add three new `case`s to `toFeedLine`'s switch (after the `agent_action` case):

```tsx
    case 'script_output': {
      const obj = isRecord(data) ? data : {}
      const hook = typeof obj.hook === 'string' ? obj.hook : ''
      const line = typeof obj.line === 'string' ? obj.line : ''
      msg = `[${hook}] ${line}`
      msgClass = 'feed-msg action'
      break
    }
    case 'hook_failed': {
      const obj = isRecord(data) ? data : {}
      const hook = typeof obj.hook === 'string' ? obj.hook : ''
      const error = typeof obj.error === 'string' ? obj.error : ''
      msg = `${hook}-hook failed: ${error}`
      msgClass = 'feed-msg error'
      statusClass = 'status-hook_failed'
      break
    }
    case 'hook_resolved': {
      const obj = isRecord(data) ? data : {}
      const hook = typeof obj.hook === 'string' ? obj.hook : ''
      const resolution = typeof obj.resolution === 'string' ? obj.resolution : ''
      msg = `${hook}-hook ${resolution}`
      break
    }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd pkg/web/dashboard && npm test -- EventFeedPanel`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/web/dashboard/src/types/afm-event.ts pkg/web/dashboard/src/components/event-feed/EventFeedPanel.tsx pkg/web/dashboard/src/components/event-feed/EventFeedPanel.test.tsx
git commit -m "$(cat <<'EOF'
feat(dashboard): отображение script_output/hook_failed/hook_resolved в ленте событий

EOF
)"
```

---

### Task 18: Dashboard — `hook_failed` status + Retry/Skip buttons

**Files:**
- Modify: `pkg/web/dashboard/src/types/stage.ts`, `pkg/web/dashboard/src/components/plan-panel/PlanPanel.tsx`, `pkg/web/dashboard/src/skins/base/plan-panel.css`, `pkg/web/dashboard/src/skins/base/stages-list.css`, `pkg/web/dashboard/src/skins/base/event-feed.css`, all 3 skin `index.css` files (`coffee`, `goga`, `novacorps`)
- Test: `pkg/web/dashboard/src/components/plan-panel/PlanPanel.test.tsx`

**Interfaces:**
- Consumes: `StageStatus` type (existing)

- [ ] **Step 1: Write the failing test**

Add to `PlanPanel.test.tsx`:

```tsx
test('shows Retry and Skip buttons when the stage is hook_failed', async () => {
  vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse(''))

  render(<PlanPanel stage={makeStage({ status: 'hook_failed' })} />)

  const retryBtn = await screen.findByRole('button', { name: 'Retry' })
  const skipBtn = await screen.findByRole('button', { name: 'Skip' })
  expect(retryBtn).toBeInTheDocument()
  expect(skipBtn).toBeInTheDocument()
})

test('skip(): posts to the skip-hook endpoint when the stage is hook_failed', async () => {
  const calls: string[] = []
  vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
    const url = typeof input === 'string' ? input : (input as Request).url
    calls.push(url)
    return textResponse('')
  })

  render(<PlanPanel stage={makeStage({ status: 'hook_failed' })} />)

  const skipBtn = await screen.findByRole('button', { name: 'Skip' })
  fireEvent.click(skipBtn)

  await waitFor(() => expect(calls.some((c) => c.endsWith('/skip-hook'))).toBe(true))
})

test('retry section for hook_failed posts to retry-hook, not retry', async () => {
  const calls: string[] = []
  vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
    const url = typeof input === 'string' ? input : (input as Request).url
    calls.push(url)
    return textResponse('')
  })

  render(<PlanPanel stage={makeStage({ status: 'hook_failed' })} />)

  const retryBtn = await screen.findByRole('button', { name: 'Retry' })
  fireEvent.click(retryBtn)

  await waitFor(() => expect(calls.some((c) => c.endsWith('/retry-hook'))).toBe(true))
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd pkg/web/dashboard && npm test -- PlanPanel`
Expected: FAIL — `'hook_failed'` isn't a valid `StageStatus` (TS compile error) and no Skip button exists.

- [ ] **Step 3: Write minimal implementation**

In `pkg/web/dashboard/src/types/stage.ts`, add to `STAGE_STATUSES`:

```ts
export const STAGE_STATUSES = [
  'pending', 'planning', 'awaiting_approval', 'revising', 'ready',
  'running', 'done', 'failed', 'retrying', 'awaiting_user_input',
  'hook_failed',
] as const
```

Add to `STAGE_STATUS_LABELS`:

```ts
export const STAGE_STATUS_LABELS: Record<StageStatus, string> = {
  pending: 'Pending',
  planning: 'Planning',
  awaiting_approval: 'Awaiting approval',
  revising: 'Revising',
  ready: 'Ready',
  running: 'Running',
  done: 'Done',
  failed: 'Failed',
  retrying: 'Retrying',
  awaiting_user_input: 'Awaiting reply',
  hook_failed: 'Hook failed',
}
```

In `PlanPanel.tsx`, add a `showHookFailed` flag alongside `showRetry` (near line 30):

```tsx
  const showHookFailed = stage.status === 'hook_failed'
```

Add `retryHook`/`skipHook` functions alongside `retry()` (near line 143):

```tsx
  async function retryHook() {
    flashButton('retry')
    setBusy(true)
    try {
      await postJson(`/api/stages/${encodeURIComponent(stage.id)}/retry-hook`, null)
    } finally {
      setBusy(false)
    }
  }

  async function skipHook() {
    flashButton('revise') // reuse the 'revise'-style flash slot; no dedicated 'skip' clicked-state needed
    setBusy(true)
    try {
      await postJson(`/api/stages/${encodeURIComponent(stage.id)}/skip-hook`, null)
    } finally {
      setBusy(false)
    }
  }
```

Add a new section rendered when `showHookFailed`, mirroring the `#retry-section` block (near line 193):

```tsx
        {showHookFailed && (
          <div id="hook-failed-section" className="section">
            <div className="actions-row">
              <button id="btn-retry-hook" className="btn btn-retry" type="button" disabled={busy} onClick={retryHook}>
                <span className="btn-ripple" aria-hidden="true" />
                <span className="btn-label">Retry</span>
                <span className="btn-done" aria-hidden="true">✓</span>
              </button>
              <button id="btn-skip-hook" className="btn btn-revise" type="button" disabled={busy} onClick={skipHook}>
                <span className="btn-ripple" aria-hidden="true" />
                <span className="btn-label">Skip</span>
                <span className="btn-done" aria-hidden="true">✓</span>
              </button>
            </div>
          </div>
        )}
```

Add a `--c-hook-failed` variable and 3 CSS rules, reusing the failed color for simplicity (no new visual token needed — a hook failure is still a failure-colored state). In each of `skins/coffee/index.css`, `skins/goga/index.css`, `skins/novacorps/index.css` (both light/dark blocks), add:

```css
--c-hook-failed: var(--c-failed);
```

In `skins/base/plan-panel.css` (after the `[data-status="retrying"]` rule):

```css
.status-badge[data-status="hook_failed"] { color: var(--c-hook-failed); }
```

In `skins/base/stages-list.css` (after the `[data-status="retrying"]` rule):

```css
.status-dot[data-status="hook_failed"] { color: var(--c-hook-failed); border-color: var(--c-hook-failed); }
```

In `skins/base/event-feed.css` (after the `.status-retrying` rule):

```css
.feed-stage-badge.status-hook_failed { color: var(--c-hook-failed); }
```

(These CSS additions are visual-only and have no automated test — verified manually per the note in Task 19.)

- [ ] **Step 4: Run test to verify it passes**

Run: `cd pkg/web/dashboard && npm test -- PlanPanel`
Expected: PASS

- [ ] **Step 5: Run the full dashboard test suite**

Run: `cd pkg/web/dashboard && npm test`
Expected: PASS, no regressions

- [ ] **Step 6: Commit**

```bash
git add pkg/web/dashboard/src/types/stage.ts pkg/web/dashboard/src/components/plan-panel/PlanPanel.tsx pkg/web/dashboard/src/components/plan-panel/PlanPanel.test.tsx pkg/web/dashboard/src/skins
git commit -m "$(cat <<'EOF'
feat(dashboard): статус hook_failed + кнопки Retry/Skip

EOF
)"
```

---

### Task 19: Manual verification pass

**Files:** none (verification only)

- [ ] **Step 1: Build the CLI and dashboard**

Run: `make build`
Expected: succeeds, embeds the updated dashboard bundle.

- [ ] **Step 2: Run the full test suite once more**

Run: `go test ./... -race && (cd pkg/web/dashboard && npm test)`
Expected: all green.

- [ ] **Step 3: Manual smoke test — script stage happy path**

Create a scratch flow file (e.g. `/tmp/script-test/flow.yaml`):

```yaml
name: script-smoke-test
description: manual smoke test
stages:
  - id: hello
    name: Hello
    script: |
      echo "hello from script stage"
```

Run: `cd /tmp/script-test && afm run flow.yaml --port 9999` (adjust binary path to the freshly built `bin/afm`), open the dashboard URL, confirm the stage shows a "script" indicator, runs to `done`, and its output line appears both in the event feed and the log panel.

- [ ] **Step 4: Manual smoke test — script_before failure + Retry/Skip**

Extend the scratch flow with a stage using `script_before: "exit 1"`, run it, confirm the dashboard shows `hook_failed` with Retry/Skip buttons, and both buttons work as expected (Retry re-attempts; Skip proceeds to the main script).

- [ ] **Step 5: Report findings**

If any manual step reveals a bug, fix it with a small follow-up commit (still TDD: add/adjust a test reproducing the bug, then fix). Do not mark this plan complete until the manual smoke tests pass.

---

## Cross-task dependency note

Tasks 12, 13, and 14 each write integration tests that call `orch.RetryHook`/`orch.SkipHook`, which are only implemented in Task 15. When executing this plan with fresh subagents per task, either:
(a) implement Task 15 immediately after Task 11 (before 12-14), reordering the plan execution, or
(b) implement Tasks 12-14 as written (test written, implementation done, but the specific assertions calling `RetryHook`/`SkipHook` left failing/commented with a `t.Skip("pending Task 15")` until Task 15 lands, then remove the skip in Task 15's step 4.

Prefer (a): reorder execution to do Task 15 right after Task 11, then 12, 13, 14 — this avoids any skipped-test bookkeeping. The task numbering above reflects logical grouping (hooks infra before wiring before HTTP), not a strict required execution order beyond the FSM/state/bus/executor prerequisites (Tasks 1-8 must precede 9-19).
