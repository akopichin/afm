# auto_recover Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a config flag `auto_recover` (default `true`) that automatically resets every `failed` stage back to `pending` when `afm run` starts/resumes a run, so a run interrupted by a killed process/container recovers without manual `afm retry <id>` on each failed stage.

**Architecture:** A new `Config.AutoRecover *bool` field (yaml `auto_recover`) gates a new orchestrator method `autoRecoverFailedStages()`, called as the first line of the existing `startPlanningForPending` (the single entry point run once at `Orchestrator.Run()` startup, covering both fresh and resumed runs). It reuses the existing FSM transition `EvManualRetry: Failed → Pending` (same one the dashboard's manual retry uses) and the existing `clearInteractiveSessions` helper for interactive stages. No new ordering logic is needed: stages reset to `pending` re-enter the exact same `depsDone()`-gated flow that already serializes any stage that has never run, so `depends_on` order falls out for free.

**Tech Stack:** Go 1.x, existing `pkg/config`, `pkg/orchestrator`, `pkg/state`, `pkg/flow` packages. Tests use the existing `orchestrator_test` external test package pattern (real `Orchestrator.Run(ctx)` in a goroutine, polling `state.json`/`Store.Get` — see `pkg/orchestrator/recovery_hooks_test.go`).

## Global Constraints

- Config key is **top-level** `auto_recover` (not nested under `executor:`), yaml boolean, pointer-typed like `ServerConfig.OpenBrowser`/`ServerConfig.Port` so nil vs explicit-false are distinguishable.
- **Default: `true`.** Only an explicit `auto_recover: false` disables it.
- Retries **all** `failed` stages, with **no filtering by failure reason** (`context canceled` vs a genuine bug are treated identically) — this is a deliberate simplicity choice from the approved design, not an oversight.
- **No CLI flag** (no `--no-auto-recover`) and **no retry-count cap/backoff** — config-only, by design.
- Spec reference: `docs/superpowers/specs/2026-07-31-auto-recover-design.md`.

---

### Task 1: `Config.AutoRecover` field, `IsAutoRecover()` helper, merge, defaults

**Files:**
- Modify: `pkg/config/config.go:229-249` (the `Config` struct and `Default()`)
- Modify: `pkg/config/config.go:288-360` (`mergeFile`)
- Modify: `config.example.yaml:1-16` (documentation)
- Test: `pkg/config/config_test.go`

**Interfaces:**
- Produces: `config.Config.AutoRecover *bool` (field, yaml `auto_recover`); `func (c Config) IsAutoRecover() bool` (nil or `*true` → `true`; `*false` → `false`). Task 2 calls `o.opts.Config.IsAutoRecover()`.

- [ ] **Step 1: Write the failing tests**

Add to `pkg/config/config_test.go` (anywhere after the existing `TestServerConfig_IsOpenBrowser` test, e.g. right after it):

```go
func TestConfig_IsAutoRecover(t *testing.T) {
	var c config.Config
	if !c.IsAutoRecover() {
		t.Error("nil AutoRecover should default to true")
	}
	tb := true
	c.AutoRecover = &tb
	if !c.IsAutoRecover() {
		t.Error("AutoRecover=true should return true")
	}
	fb := false
	c.AutoRecover = &fb
	if c.IsAutoRecover() {
		t.Error("AutoRecover=false should return false")
	}
}

func TestDefaultConfig_AutoRecoverTrue(t *testing.T) {
	cfg := config.Default()
	if !cfg.IsAutoRecover() {
		t.Error("Default() should have auto_recover=true")
	}
}

func TestAutoRecoverMerge_ProjectDisablesGlobalDefault(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, dir, "config.yaml", "auto_recover: false\n")
	cfg, err := config.LoadFrom("", dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.IsAutoRecover() {
		t.Error("explicit auto_recover: false in project config should override the true default")
	}
}

func TestAutoRecoverMerge_AbsentKeepsDefault(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, dir, "config.yaml", "theme: goga\n")
	cfg, err := config.LoadFrom("", dir)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.IsAutoRecover() {
		t.Error("config without auto_recover key should keep the true default")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/config/... -run 'TestConfig_IsAutoRecover|TestDefaultConfig_AutoRecoverTrue|TestAutoRecoverMerge' -v`
Expected: FAIL — compile error, `config.Config` has no field/method `AutoRecover`/`IsAutoRecover`.

- [ ] **Step 3: Implement**

In `pkg/config/config.go`, add the field to the `Config` struct (around line 229-238):

```go
// Config is the merged configuration for afm.
type Config struct {
	Client     ClientConfig     `yaml:"client"`
	Executor   ExecutorConfig   `yaml:"executor"`
	Server     ServerConfig     `yaml:"server"`
	Docker     DockerConfig     `yaml:"docker"`
	Supervisor SupervisorConfig `yaml:"supervisor"`
	PromptsDir string           `yaml:"prompts_dir"`
	Theme      string           `yaml:"theme"`
	SkinDir    string           `yaml:"skin_dir"`
	// AutoRecover controls whether failed stages are automatically reset to
	// pending when a run starts/resumes (e.g. after a killed process/container
	// left stages in failed). nil/true = enabled (default); explicit false disables.
	AutoRecover *bool `yaml:"auto_recover"`
}

// IsAutoRecover reports whether failed stages should be auto-retried when a
// run starts or resumes. Defaults to true; only an explicit
// `auto_recover: false` disables it.
func (c Config) IsAutoRecover() bool {
	return c.AutoRecover == nil || *c.AutoRecover
}
```

Update `Default()` (currently lines 241-249) to set it explicitly true:

```go
// Default returns the built-in default configuration.
func Default() Config {
	openBrowser := false
	port := 9876
	autoRecover := true
	return Config{
		Client:      ClientConfig{Command: ClaudeCommand},
		Executor:    ExecutorConfig{IdleTimeout: 30 * time.Minute, MaxParallel: 0},
		Server:      ServerConfig{Port: &port, OpenBrowser: &openBrowser},
		AutoRecover: &autoRecover,
	}
}
```

In `mergeFile` (`pkg/config/config.go:288-360`), add the overlay propagation. Insert right before the final `return nil` (after the existing `if overlay.Supervisor.Command != "" { ... }` block):

```go
	if overlay.AutoRecover != nil {
		dst.AutoRecover = overlay.AutoRecover
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/config/... -v`
Expected: PASS (all config tests, including the 4 new ones).

- [ ] **Step 5: Document in config.example.yaml**

In `config.example.yaml`, add a new commented block right after the `theme` section (after line 15, before the `# server:` block):

```yaml
# Автоматически возвращать failed-стейджи в pending при старте/резюме рана
# (напр. если процесс/контейнер afm был прибит во время выполнения) — без
# ручного `afm retry <id>` на каждый упавший стейдж. Ретраятся все failed-
# стейджи без разбора причины падения; порядок соблюдается штатным
# depends_on-планировщиком (зависимый стейдж не стартует, пока его depends_on
# не в done).
# Default: true
# auto_recover: true
```

- [ ] **Step 6: Commit**

```bash
git add pkg/config/config.go pkg/config/config_test.go config.example.yaml
git commit -m "feat(config): auto_recover флаг (дефолт true) для автоматического retry failed-стейджей"
```

---

### Task 2: `autoRecoverFailedStages()` + wiring into `startPlanningForPending`

**Files:**
- Modify: `pkg/orchestrator/recovery.go:1-17` (imports + call site)
- Test: `pkg/orchestrator/recovery_auto_test.go` (new file, package `orchestrator_test`)

**Interfaces:**
- Consumes: `config.Config.IsAutoRecover()` (Task 1). `EvManualRetry` (existing, `pkg/orchestrator/fsm.go:24,81`). `clearInteractiveSessions(stageDir string)` (existing, `pkg/orchestrator/scheduling.go:172`). `o.opts.Store.Get(id string) state.StageStatus`, `o.Trigger(stageID string, ev FSMEvent, ctx GuardCtx, reason string) (state.StageStatus, bool)` (existing orchestrator methods).
- Produces: `func (o *Orchestrator) autoRecoverFailedStages()` (unexported, called once from `startPlanningForPending`). Nothing outside the package consumes it directly — behavior is observed through `Orchestrator.Run(ctx)`.

- [ ] **Step 1: Write the failing test**

Create `pkg/orchestrator/recovery_auto_test.go`:

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

// TestAutoRecover_ResumesFailedStageWithoutManualRetry simulates the exact
// scenario the feature targets: a stage genuinely failed (e.g. the process
// was killed mid-run, leaving reason "context canceled"), then a fresh
// Orchestrator.Run resumes the same run dir. With auto_recover enabled
// (the default), the stage must actually re-run and reach done — no manual
// `afm retry` call anywhere in this test.
func TestAutoRecover_ResumesFailedStageWithoutManualRetry(t *testing.T) {
	rootDir := t.TempDir()
	runDir := t.TempDir()
	marker := filepath.Join(rootDir, "ran.marker")

	stages := []flow.Stage{{ID: "a", Name: "A", Script: "touch " + marker}}

	store, err := state.Open(runDir, []string{"a"})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	if err := store.Apply(&state.Transition{StageID: "a", From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(&state.Transition{StageID: "a", From: state.StatusRunning, To: state.StatusFailed, Event: "fail", Reason: "context canceled"}); err != nil {
		t.Fatal(err)
	}
	store.Close() // simulate process exit

	store2, err := state.Open(runDir, []string{"a"})
	if err != nil {
		t.Fatalf("state.Open (reopen): %v", err)
	}
	t.Cleanup(func() { store2.Close() })

	orch := orchestrator.New(orchestrator.Options{
		RunDir:  runDir,
		RootDir: rootDir,
		Stages:  stages,
		Store:   store2,
		Config:  config.Default(), // AutoRecover defaults to true
		Prompts: orchestrator.DefaultPrompts(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- orch.Run(ctx) }()

	stateFile := filepath.Join(runDir, "state.json")
	waitForStatus(t, stateFile, "a", state.StatusDone, 20*time.Second)
	if _, err := os.Stat(marker); err != nil {
		t.Error("expected the stage to have actually re-run after auto-recover, not just flipped status")
	}

	cancel()
	<-runDone
}

// TestAutoRecover_Disabled_LeavesFailedStageUntouched is the regression guard:
// an explicit auto_recover: false must reproduce today's behavior exactly —
// a failed stage stays failed until a manual retry.
func TestAutoRecover_Disabled_LeavesFailedStageUntouched(t *testing.T) {
	rootDir := t.TempDir()
	runDir := t.TempDir()

	stages := []flow.Stage{{ID: "a", Name: "A", Script: "true"}}

	store, err := state.Open(runDir, []string{"a"})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	if err := store.Apply(&state.Transition{StageID: "a", From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(&state.Transition{StageID: "a", From: state.StatusRunning, To: state.StatusFailed, Event: "fail", Reason: "context canceled"}); err != nil {
		t.Fatal(err)
	}
	store.Close()

	store2, err := state.Open(runDir, []string{"a"})
	if err != nil {
		t.Fatalf("state.Open (reopen): %v", err)
	}
	t.Cleanup(func() { store2.Close() })

	cfg := config.Default()
	disabled := false
	cfg.AutoRecover = &disabled

	orch := orchestrator.New(orchestrator.Options{
		RunDir:  runDir,
		RootDir: rootDir,
		Stages:  stages,
		Store:   store2,
		Config:  cfg,
		Prompts: orchestrator.DefaultPrompts(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	runDone := make(chan error, 1)
	go func() { runDone <- orch.Run(ctx) }()

	time.Sleep(500 * time.Millisecond)
	if got := orchestrator.StoreFromOrch(orch).Get("a"); got != state.StatusFailed {
		t.Fatalf("status = %v, want failed (auto_recover: false must not touch it)", got)
	}

	cancel()
	<-runDone
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/orchestrator/... -run TestAutoRecover_ -v`
Expected: `TestAutoRecover_ResumesFailedStageWithoutManualRetry` FAILs (stage never leaves `failed`, `waitForStatus` times out) — proves the current code does not auto-recover. `TestAutoRecover_Disabled_LeavesFailedStageUntouched` PASSes already (nothing to disable yet); that's fine, it's the regression guard for after Step 3.

- [ ] **Step 3: Implement**

In `pkg/orchestrator/recovery.go`, add `log` to the import block (lines 1-11):

```go
package orchestrator

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)
```

Add the new method right before `startPlanningForPending` (before line 17):

```go
// autoRecoverFailedStages resets every stage currently in StatusFailed back
// to Pending when auto_recover is enabled (default true), so a run
// interrupted by a killed process/container resumes automatically instead of
// requiring manual `afm retry` on each failed stage. All failed stages are
// reset regardless of failure reason (context canceled vs a genuine bug are
// treated identically — see the design doc for why). Order does not matter
// here: the reset stages re-enter the Pending flow in startPlanningForPending
// below, which already gates on depsDone(), so depends_on order falls out on
// its own without any extra bookkeeping.
func (o *Orchestrator) autoRecoverFailedStages() {
	if !o.opts.Config.IsAutoRecover() {
		return
	}
	for _, s := range o.opts.Stages {
		if o.opts.Store.Get(s.ID) != state.StatusFailed {
			continue
		}
		if s.Interactive {
			clearInteractiveSessions(filepath.Join(o.opts.RunDir, s.ID))
		}
		if _, ok := o.Trigger(s.ID, EvManualRetry, GuardCtx{}, "auto_recover"); ok {
			log.Printf("auto_recover: stage %q failed -> pending", s.ID)
		}
	}
}
```

Add the call as the first line of `startPlanningForPending` (currently line 17-18):

```go
func (o *Orchestrator) startPlanningForPending(ctx context.Context) {
	o.autoRecoverFailedStages()
	for _, s := range o.opts.Stages {
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/orchestrator/... -run TestAutoRecover_ -v`
Expected: PASS for both tests.

- [ ] **Step 5: Run the full orchestrator suite to check for regressions**

Run: `go test ./pkg/orchestrator/... -race`
Expected: PASS — in particular the existing `recovery_hooks_test.go` and `integration_*` tests must be unaffected (none of them leave a stage in `StatusFailed` at `Run()` start, so `autoRecoverFailedStages` is a no-op for them).

- [ ] **Step 6: Commit**

```bash
git add pkg/orchestrator/recovery.go pkg/orchestrator/recovery_auto_test.go
git commit -m "feat(orchestrator): auto_recover сбрасывает failed-стейджи в pending при старте рана"
```

---

### Task 3: `depends_on` ordering under auto-recover (cascade failures)

**Files:**
- Test: `pkg/orchestrator/recovery_auto_test.go` (append to the file created in Task 2)

**Interfaces:**
- Consumes: everything from Task 2 (`autoRecoverFailedStages`, already wired). No new production code — this task is pure test coverage for a property Task 2's implementation already has by construction (depsDone() gating), making that property explicit and regression-proof.

- [ ] **Step 1: Write the test**

Append to `pkg/orchestrator/recovery_auto_test.go`:

```go
// TestAutoRecover_RespectsDependsOnOrder covers the exact cascade shape the
// feature targets: stage a genuinely failed, stage b failed only because a
// failed (blocked_by_dep) — both are "failed" on disk, but auto-recover must
// not let b start before a actually finishes. This is not new orchestrator
// logic: resetting both to pending just re-enters the same depsDone() gate
// every never-yet-run pending stage goes through.
func TestAutoRecover_RespectsDependsOnOrder(t *testing.T) {
	rootDir := t.TempDir()
	runDir := t.TempDir()
	bStarted := filepath.Join(rootDir, "b-started.marker")

	stages := []flow.Stage{
		{ID: "a", Name: "A", Script: "sleep 1"},
		{ID: "b", Name: "B", Script: "touch " + bStarted, DependsOn: []string{"a"}},
	}

	store, err := state.Open(runDir, []string{"a", "b"})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	// a: genuine failure (e.g. context canceled from a killed process).
	if err := store.Apply(&state.Transition{StageID: "a", From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(&state.Transition{StageID: "a", From: state.StatusRunning, To: state.StatusFailed, Event: "fail", Reason: "context canceled"}); err != nil {
		t.Fatal(err)
	}
	// b: cascade failure, exactly as failBlockedStages (scheduling.go) leaves it.
	if err := store.Apply(&state.Transition{StageID: "b", From: state.StatusPending, To: state.StatusFailed, Event: "blocked_by_dep", Reason: "dep failed"}); err != nil {
		t.Fatal(err)
	}
	store.Close()

	store2, err := state.Open(runDir, []string{"a", "b"})
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

	// Midway through a's 1s sleep, b must not have started yet.
	time.Sleep(500 * time.Millisecond)
	if _, err := os.Stat(bStarted); err == nil {
		t.Fatal("stage b started before its dependency a finished")
	}
	if got := orchestrator.StoreFromOrch(orch).Get("b"); got == state.StatusDone {
		t.Fatal("stage b reached done before a finished")
	}

	stateFile := filepath.Join(runDir, "state.json")
	waitForStatus(t, stateFile, "a", state.StatusDone, 20*time.Second)
	waitForStatus(t, stateFile, "b", state.StatusDone, 20*time.Second)
	if _, err := os.Stat(bStarted); err != nil {
		t.Error("expected stage b to have run after auto-recover once a completed")
	}

	cancel()
	<-runDone
}
```

- [ ] **Step 2: Run the test**

Run: `go test ./pkg/orchestrator/... -run TestAutoRecover_RespectsDependsOnOrder -v`
Expected: PASS. (If it fails, the bug is almost certainly in Task 2's placement of `autoRecoverFailedStages()` — it must run before the dependency-checking loop, not spawn agents itself.)

- [ ] **Step 3: Commit**

```bash
git add pkg/orchestrator/recovery_auto_test.go
git commit -m "test(orchestrator): auto_recover соблюдает порядок depends_on при каскадных failed"
```

---

### Task 4: Stale interactive session cleanup on auto-recover

**Files:**
- Test: `pkg/orchestrator/recovery_auto_test.go` (append)

**Interfaces:**
- Consumes: `clearInteractiveSessions` call already wired in Task 2's `autoRecoverFailedStages`. No new production code.

- [ ] **Step 1: Write the test**

Append to `pkg/orchestrator/recovery_auto_test.go`:

```go
// TestAutoRecover_ClearsStaleInteractiveSessionBeforeRetry covers the
// interactive-stage edge case: a leftover <phase>.session.json from before
// the crash would otherwise make the retried agent fail with "No
// conversation found" (the same reason retryStage's manual path clears
// sessions in scheduling.go). autoRecoverFailedStages must clear it too.
// This only checks the cleanup itself (which happens synchronously, before
// any agent spawns) — it does not wait for the interactive stage to fully
// complete, since that is already covered by the dialog-specific tests.
func TestAutoRecover_ClearsStaleInteractiveSessionBeforeRetry(t *testing.T) {
	rootDir := t.TempDir()
	runDir := t.TempDir()

	stages := []flow.Stage{{
		ID:          "review",
		Name:        "Review",
		Interactive: true,
		Command:     "true",
	}}

	store, err := state.Open(runDir, []string{"review"})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	if err := store.Apply(&state.Transition{StageID: "review", From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(&state.Transition{StageID: "review", From: state.StatusRunning, To: state.StatusFailed, Event: "fail", Reason: "context canceled"}); err != nil {
		t.Fatal(err)
	}
	stageDir := filepath.Join(runDir, "review")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	staleSession := filepath.Join(stageDir, "implementation.session.json")
	if err := os.WriteFile(staleSession, []byte(`{"session_id":"stale-phantom"}`), 0644); err != nil {
		t.Fatal(err)
	}
	store.Close()

	store2, err := state.Open(runDir, []string{"review"})
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	runDone := make(chan error, 1)
	go func() { runDone <- orch.Run(ctx) }()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(staleSession); os.IsNotExist(err) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(staleSession); err == nil {
		t.Error("expected stale session.json to be removed by auto-recover before retry")
	}
	if got := orchestrator.StoreFromOrch(orch).Get("review"); got == state.StatusFailed {
		t.Error("stage should have left failed status after auto-recover")
	}

	cancel()
	<-runDone
}
```

- [ ] **Step 2: Run the test**

Run: `go test ./pkg/orchestrator/... -run TestAutoRecover_ClearsStaleInteractiveSessionBeforeRetry -v`
Expected: PASS.

- [ ] **Step 3: Run the entire package one more time**

Run: `go test ./pkg/orchestrator/... -race`
Expected: PASS (all tests, no regressions from the whole `auto_recover` change).

- [ ] **Step 4: Commit**

```bash
git add pkg/orchestrator/recovery_auto_test.go
git commit -m "test(orchestrator): auto_recover чистит протухшую interactive-сессию перед retry"
```

---

## Final Step (not a task — run once, after Task 4, by the coordinating session, not a subagent)

Re-enable the pre-commit hook (`chmod +x .githooks/pre-commit`) and make one last verification commit (or amend-free empty check) so the full `make lint && make build && make test` gate actually runs at least once against the final state before considering this done. See the coordinating instructions for exact sequencing — subagents commit with the hook disabled; only the very last commit in this plan's sequence should run with it enabled.
