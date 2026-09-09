# `afm memory rebuild` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an offline `afm memory rebuild` command that reruns the agent-memory pipeline over an already-completed run, writing per-stage memory and the project `memory.md` without creating a new run or touching the historical FSM/event log.

**Architecture:** Extract the memory engine out of `pkg/orchestrator` into a reusable `pkg/memorypipeline` with a **two-phase** shape — `Capture` (no shared lock) and `Finalize` (staging distill + durable publish, run under a caller-held memory lock). The CLI owns lock ordering (run-lock → capture → memory-lock → re-preflight → finalize → commit → unlock), a transactional attempt-workspace with a per-step manifest, and strict error/exit semantics. The live orchestrator calls the SAME two-phase engine (best-effort). Docker re-exec, root/memory resolution, and git-commit are shared with `afm run` via extracted helpers.

**Tech Stack:** Go (stdlib `testing`), cobra, `gopkg.in/yaml.v3`, `syscall.Flock` via `pkg/progress`, `os/exec` git, existing `pkg/executor`, `github.com/aymanbagabas/go-udiff` (already in the module, used by `pkg/server/workspace`).

**Spec:** `docs/superpowers/specs/2026-09-09-memory-rebuild-design.md`

## Review incorporation (why this plan differs from a naive spec transcription)

This plan was revised against TWO rounds of design review. The corrected ordering follows the review's recommendation: **(1)** fix completed-run semantics + full stage-set source; **(2)** fix path/stage-ID validation and move it BEFORE any new path joins; **(3)** lock-busy vs I/O distinction; **(4)** lock the whole seed→distill→publish→commit window at the CLI level; **(5)** freshness/NoHigh/empty-dataset semantics + failure-injection; **(6)** only then extraction, CLI, Docker. Finding→task map is in each task's header.

**Round-2 finding → fix:** R2-#1 explicit `--run` now checks `rs.AllDone()` independently (T11); R2-#2 single manifest state machine, CLI creates `running`+meta and owns the terminal `completed`/`failed`, central defer, `DatasetResult` gains source/dataset hashes (T6/T8/T11); R2-#3 explicit `CaptureStage(datasetOut, force)` primitive both live and offline use (T8/T10); R2-#4 freshness = remove-before-call + `requireFreshFile`, `update` uses Lstat-before-read, "valid candidate" not "fresh" (T7); R2-#5 `validateTargetUnderMemoryDir` under lock before seed and each rename (T8); R2-#6 after-hook→reflection via `maybeRunAfterHookThen` in `hooks.go` (T10); R2-#7 early commit preflight before capture + re-check under lock (T11); R2-#8 `AtomicWriter func(path,data)` seam + dataset promotion bookkeeping (T8); R2-#9 `NewUniqueAttemptDir` MkdirAll-parent + bounded retry (T6/T11); R2-#10 selection-semantics divergence documented + spec §5 updated (T14); R2-#11 `datasetsAllEmpty (bool,error)` + separate `maxDatasetBytes`/`maxRulesBytes` bounds (T7); R2-#12 NUL-reject, `Inventory.All` intentional ordering, `ScanUnfinishedPromotions` scope note, `CommitPaths` rev-parse error, `AtomicWrite` close/fsync errors, clock seam, concrete Windows errno (T2/T3/T5/T6/T12).

## Global Constraints

- **Do not change the Go version in `go.mod`.** (user rule)
- **Development git commit messages (the ones in this plan's commit steps) are in Russian.** No `Co-Authored-By`. (user rule)
  - **NOTE (review finding #5):** the *runtime* commit afm itself makes when publishing memory is a product string, not a developer commit. The existing live path uses English (`memory.Commit(dir, "chore(memory): update project memory")`). For consistency with that existing behavior, the rebuild runtime message stays English: `chore(memory): rebuild from <run-id>`. The Russian-commit rule applies to *our* development commits only.
- **Lint clean after every task:** `make lint-ci` → `0 issues` (host + `GOOS=linux`). The commit step runs a pre-commit hook (`make lint` + `make build`); dirty lint blocks the commit.
- **The explicit `memory rebuild` command is STRICT** (non-zero on any failure). The **live** capture/end-of-run path stays **best-effort** (`reflect_failed` notices, FSM untouched). Preserve that asymmetry in every task.
- **Memory agents run fresh-context:** no `SessionID`, no `Resume`, no `AFM_STAGE_DIR` (explicitly stripped — Task 4), command from `config.client.command`/`client.extra_args` (never `stage.command`).
- **Never write outside the attempt workdir until publish** (offline). This includes executor debug output: offline `AgentConfig.RunDir` MUST be the attempt `WorkDir`, so `<RunDir>/debug.log` lands inside it (review #4). `--dry-run` never writes a target and never commits.
- **Phase/plan file names come from `pkg/flow`/`pkg/state`** (`flow.PhaseLogFiles`, `flow.PhaseStreamLogs`; historical plans are `plan.v<N>.md`, see `state.VersionPlan`) — never hardcode.
- **`<module>` in import blocks below is a placeholder** — resolve the real module path with `head -1 go.mod` before writing imports.

### Verified existing signatures (do not re-derive)

```go
// pkg/progress — flock; Lock()/TryLock() currently return a wrapped generic error on ANY failure.
func NewLock(path string) (*Lock, error) // does NOT acquire
func (l *Lock) TryLock() error
func (l *Lock) Lock() error
func (l *Lock) Unlock()

// pkg/state
func FindLatestRunDir(base, flowName string) (string, error)
func LoadRunState(runDir string) (RunState, error) // rs.Stages STARTS EMPTY; only stages with a transition appear
func (rs *RunState) AllDone() bool                  // true on an empty/partial Stages map (review #1)
func VersionPlan(stageDir string) (int, error)      // plan.md -> plan.v<N>.md
var ErrRunLocked, ErrCorruptLog error
// Store.Open acquires progress.Lock via TryLock -> ErrRunLocked when busy.

// pkg/memory
func ProjectFile(dir string) string          // <dir>/memory.md
func StageFile(dir, rel string) string
func AtomicWrite(path string, data []byte) error // temp+rename, no fsync (hardened in Task 6)
func SelectHigh(prioritized string) string
func Commit(dir, message string) (committed bool, err error) // git add . ; commit -- .

// pkg/executor — env filter strips ONLY "CLAUDECODE=" today (NOT AFM_STAGE_DIR); Debug writes <RunDir>/debug.log
type Config struct { Command string; ExtraArgs []string; IdleTimeout time.Duration
    WrapperDir, Dir, RunDir, StageID, SessionID, StageDir string; Resume, Debug bool; /* ... */ }
func ResolveArgs(extra []string) []string
func New(cfg Config) *Executor
func (e *Executor) RunAgent(ctx, agentType, stageName, prompt, logFile string) error

// pkg/orchestrator (private, to be moved/exported)
func wrapperDirFor(cmd, wrapperDir string, generated map[string]bool) string // -> becomes executor.WrapperDirFor

// pkg/flow
func Phases() []Phase; func PhaseLogFiles(p Phase) []string; func PhaseStreamLogs(p Phase) []string
type MemoryConfig struct { Path, Mode string; MemoryUse bool; MaxRules int; Commit bool }
func (m MemoryConfig) CanReadProject() bool; func (m MemoryConfig) CanWriteProject() bool
type Reflect struct { File, Mode string }
func (r *Reflect) CanRead() bool; func (r *Reflect) CanWrite() bool
func (s *Stage) IsScript() bool; func (f *Flow) MemoryEnabled() bool
// flow.validate() currently has NO stage-ID path check and NO reflect.file path check (review #7)

// cmd/afm
func resolveFlowPath(args []string) (string, error) // len(args)==0 -> scan; []string{""} -> returns "" (review #12)
func loadPrompts(overrideDir string) (orchestrator.Prompts, error)
// config: config.LoadFrom(filepath.Join(home, config.AfmDir), fmDir()) — there is NO loadConfig()
```

---

## Task 1: Completed-run resolver (stage-set aware) + safe explicit `--run` + CLI skeleton

Fixes review **#1** (partial-log false-positive), **#7/#12** (explicit-run symlink/separator/`Changed()`), scaffolds the command.

**Files:**
- Modify: `pkg/state/state.go`
- Create: `pkg/state/run_lookup_completed_test.go`
- Create: `cmd/afm/memory.go`, `cmd/afm/memory_rebuild.go`, `cmd/afm/memory_rebuild_test.go`
- Modify: `cmd/afm/main.go`

**Interfaces:**
- Produces: `state.FindLatestCompletedRunDir(base, flowName string, wantStages []string) (string, error)` — returns the newest run whose **exact** stage set equals `wantStages`, all `done`. A run missing any expected stage, having extras, empty, or partial is NOT completed. Corrupt matching log → `ErrCorruptLog`; none → `ErrNoRun`.
- Produces: `newMemoryCmd()`, `newMemoryRebuildCmd()`, `resolveExplicitRunDir(runsBase, flowName, id string) (string, error)` (Lstat, no symlink, no separators), `rebuildOptions{FlowArg, RunID string; DryRun, ForceReflect, CommitSet, Commit bool}`, seam `var rebuildHandler func(ctx, rebuildOptions) error`.

- [ ] **Step 1: Failing test for the stage-set-aware resolver**

`pkg/state/run_lookup_completed_test.go`. Build runs via the package's real event-writing path (grep for an existing helper that applies transitions and closes a `Store`; if none, open a `Store`, apply `SetStageStatus`/transitions, `Close`). Cover exactly the review's regression cases:

```go
func TestFindLatestCompletedRunDir_ExactSetAllDone(t *testing.T) {
    base := t.TempDir()
    mkRun(t, base, "flow-20260101-000000-aaaa", map[string]Status{"a": StatusDone, "b": StatusDone})
    got, err := FindLatestCompletedRunDir(base, "flow", []string{"a", "b"})
    if err != nil { t.Fatal(err) }
    if filepath.Base(got) != "flow-20260101-000000-aaaa" { t.Fatalf("got %s", got) }
}

func TestFindLatestCompletedRunDir_EmptyActiveRunNotCompleted(t *testing.T) {
    base := t.TempDir()
    mkRun(t, base, "flow-20260101-000000-aaaa", map[string]Status{"a": StatusDone, "b": StatusDone})
    mkEmptyRun(t, base, "flow-20260301-000000-cccc") // newest, events.jsonl empty
    got, err := FindLatestCompletedRunDir(base, "flow", []string{"a", "b"})
    if err != nil { t.Fatal(err) }
    if filepath.Base(got) != "flow-20260101-000000-aaaa" { t.Fatalf("empty active run must be skipped, got %s", got) }
}

func TestFindLatestCompletedRunDir_DonePlusNeverStartedPendingNotCompleted(t *testing.T) {
    base := t.TempDir()
    // newest run: only "a" ever transitioned (to done); "b" never emitted an event
    mkRun(t, base, "flow-20260301-000000-cccc", map[string]Status{"a": StatusDone})
    mkRun(t, base, "flow-20260101-000000-aaaa", map[string]Status{"a": StatusDone, "b": StatusDone})
    got, err := FindLatestCompletedRunDir(base, "flow", []string{"a", "b"})
    if err != nil { t.Fatal(err) }
    if filepath.Base(got) != "flow-20260101-000000-aaaa" { t.Fatalf("partial run must be skipped, got %s", got) }
}

func TestFindLatestCompletedRunDir_DifferentTopologySkipped(t *testing.T) {
    base := t.TempDir()
    mkRun(t, base, "flow-20260301-000000-cccc", map[string]Status{"a": StatusDone, "x": StatusDone}) // extra stage
    mkRun(t, base, "flow-20260101-000000-aaaa", map[string]Status{"a": StatusDone, "b": StatusDone})
    got, err := FindLatestCompletedRunDir(base, "flow", []string{"a", "b"})
    if err != nil { t.Fatal(err) }
    if filepath.Base(got) != "flow-20260101-000000-aaaa" { t.Fatalf("topology-mismatch run must be skipped, got %s", got) }
}

func TestFindLatestCompletedRunDir_AnchoredPrefix(t *testing.T) {
    base := t.TempDir()
    mkRun(t, base, "foo-bar-20260101-000000-aaaa", map[string]Status{"a": StatusDone})
    _, err := FindLatestCompletedRunDir(base, "foo", []string{"a"})
    if !errors.Is(err, ErrNoRun) { t.Fatalf("want ErrNoRun, got %v", err) }
}

func TestFindLatestCompletedRunDir_CorruptNewestNotHidden(t *testing.T) {
    base := t.TempDir()
    mkRun(t, base, "flow-20260101-000000-aaaa", map[string]Status{"a": StatusDone, "b": StatusDone})
    mkCorruptRun(t, base, "flow-20260301-000000-cccc")
    _, err := FindLatestCompletedRunDir(base, "flow", []string{"a", "b"})
    if !errors.Is(err, ErrCorruptLog) { t.Fatalf("want ErrCorruptLog, got %v", err) }
}
```

Add `var ErrNoRun` if the package lacks one (check `grep -n "ErrNo\|no run\|no matching" pkg/state/*.go` and reuse `FindLatestRunDir`'s sentinel if present).

- [ ] **Step 2: Run, verify fail** — `go test ./pkg/state -run TestFindLatestCompletedRunDir -v` → FAIL.

- [ ] **Step 3: Implement `FindLatestCompletedRunDir`**

```go
// FindLatestCompletedRunDir returns the newest run dir for flowName whose set
// of stages that reached a terminal status equals wantStages exactly, with
// every one StatusDone. events.jsonl only records stages that transitioned, so
// a run missing an expected stage (never started) is NOT completed; an empty
// active run is NOT completed; a run with extra/different stages is skipped
// (a newer-but-different-topology run does not shadow an older matching one).
// Reads events.jsonl (authoritative), no flock. Corrupt matching log surfaces.
func FindLatestCompletedRunDir(base, flowName string, wantStages []string) (string, error) {
    entries, err := os.ReadDir(base)
    if err != nil {
        if os.IsNotExist(err) { return "", ErrNoRun }
        return "", err
    }
    want := make(map[string]bool, len(wantStages))
    for _, s := range wantStages { want[s] = true }
    prefix := flowName + "-"
    var names []string
    for _, e := range entries {
        n := e.Name()
        if !e.IsDir() || len(n) <= len(prefix) || n[:len(prefix)] != prefix { continue }
        if c := n[len(prefix)]; c < '0' || c > '9' { continue }
        names = append(names, n)
    }
    slices.SortFunc(names, func(a, b string) int { return strings.Compare(b, a) }) // newest first
    for _, n := range names {
        runDir := filepath.Join(base, n)
        rs, err := LoadRunState(runDir)
        if err != nil {
            if errors.Is(err, ErrCorruptLog) { return "", fmt.Errorf("run %s: %w", n, err) }
            continue
        }
        if stageSetAllDone(rs, want) { return runDir, nil }
    }
    return "", ErrNoRun
}

func stageSetAllDone(rs RunState, want map[string]bool) bool {
    if len(rs.Stages) != len(want) { return false } // exact set (no extras, none missing)
    for id, st := range rs.Stages {
        if !want[id] || st.Status != StatusDone { return false }
    }
    return true
}
```

- [ ] **Step 4: Run, verify pass** — `go test ./pkg/state -v` → PASS.

- [ ] **Step 5: Failing tests for the CLI skeleton + explicit resolver**

`cmd/afm/memory_rebuild_test.go`:

```go
func TestMemoryRebuildCmd_Registered(t *testing.T) {
    c, _, err := newRootCmd().Find([]string{"memory", "rebuild"})
    if err != nil || c.Name() != "rebuild" { t.Fatalf("got %v %v", c, err) }
}

func TestMemoryRebuildCmd_CommitFlagsMutuallyExclusive(t *testing.T) {
    root := newRootCmd()
    root.SetArgs([]string{"memory", "rebuild", "flow.yaml", "--commit", "--no-commit"})
    root.SetOut(io.Discard); root.SetErr(io.Discard)
    if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "exclusive") {
        t.Fatalf("want mutual-exclusion error, got %v", err)
    }
}

func TestResolveExplicitRunDir(t *testing.T) {
    base := t.TempDir()
    os.MkdirAll(filepath.Join(base, "flow-20260101-000000-aaaa"), 0755)
    if _, err := resolveExplicitRunDir(base, "flow", "flow-20260101-000000-aaaa"); err != nil { t.Fatal(err) }
    for _, bad := range []string{
        "../escape", "sub/flow-x", `flow-2026\escape`, "flow\\x", ".", "..", "/abs",
        "other-20260101-000000-aaaa", "flow-20260101-000000-zzzz",
    } {
        if _, err := resolveExplicitRunDir(base, "flow", bad); err == nil { t.Errorf("want error for %q", bad) }
    }
}

func TestResolveExplicitRunDir_RejectsSymlink(t *testing.T) {
    base := t.TempDir()
    outside := t.TempDir()
    if err := os.Symlink(outside, filepath.Join(base, "flow-20260101-000000-aaaa")); err != nil { t.Skip("symlink unsupported") }
    if _, err := resolveExplicitRunDir(base, "flow", "flow-20260101-000000-aaaa"); err == nil {
        t.Fatal("symlinked run id must be rejected")
    }
}
```

- [ ] **Step 6: Run, verify fail** — FAIL.

- [ ] **Step 7: Implement skeleton + resolver**

`cmd/afm/memory.go`:
```go
package main
import "github.com/spf13/cobra"
func newMemoryCmd() *cobra.Command {
    c := &cobra.Command{Use: "memory", Short: "Agent-memory maintenance commands"}
    c.AddCommand(newMemoryRebuildCmd())
    return c
}
```

`cmd/afm/memory_rebuild.go`:
```go
package main

import (
    "context"
    "fmt"
    "os"
    "path/filepath"
    "strings"

    "github.com/spf13/cobra"
)

type rebuildOptions struct {
    FlowArg, RunID              string
    DryRun, ForceReflect        bool
    CommitSet, Commit           bool // CommitSet: a --commit/--no-commit flag was actually passed
}

var rebuildHandler = func(ctx context.Context, o rebuildOptions) error {
    return fmt.Errorf("memory rebuild: not implemented")
}

func newMemoryRebuildCmd() *cobra.Command {
    cmd := &cobra.Command{
        Use:   "rebuild [flow.yaml]",
        Short: "Rebuild agent memory from a completed run's session logs",
        Args:  cobra.MaximumNArgs(1),
        RunE: func(cmd *cobra.Command, args []string) error {
            commit := cmd.Flags().Changed("commit")   // review #12: Changed(), not the value
            noCommit := cmd.Flags().Changed("no-commit")
            if commit && noCommit {
                return fmt.Errorf("--commit and --no-commit are mutually exclusive")
            }
            o := rebuildOptions{CommitSet: commit || noCommit}
            if commit { v, _ := cmd.Flags().GetBool("commit"); o.Commit = v }
            if noCommit { o.Commit = false }
            o.DryRun, _ = cmd.Flags().GetBool("dry-run")
            o.ForceReflect, _ = cmd.Flags().GetBool("force-reflect")
            o.RunID, _ = cmd.Flags().GetString("run")
            if len(args) == 1 { o.FlowArg = args[0] }
            return rebuildHandler(cmd.Context(), o)
        },
    }
    cmd.Flags().String("run", "", "explicit run id (default: latest completed run of the flow)")
    cmd.Flags().Bool("dry-run", false, "show diffs without writing memory or committing")
    cmd.Flags().Bool("force-reflect", false, "regenerate reflect datasets from logs")
    cmd.Flags().Bool("commit", false, "git-commit changed memory files")
    cmd.Flags().Bool("no-commit", false, "do not git-commit even if the flow enables it")
    return cmd
}

func resolveExplicitRunDir(runsBase, flowName, id string) (string, error) {
    if id == "" || filepath.Base(id) != id || id == "." || id == ".." ||
        strings.ContainsAny(id, `/\`) {
        return "", fmt.Errorf("invalid run id %q: must be a bare run directory name", id)
    }
    prefix := flowName + "-"
    if len(id) <= len(prefix) || id[:len(prefix)] != prefix || id[len(prefix)] < '0' || id[len(prefix)] > '9' {
        return "", fmt.Errorf("run id %q does not belong to flow %q", id, flowName)
    }
    dir := filepath.Join(runsBase, id)
    info, err := os.Lstat(dir) // Lstat: reject a symlinked run dir (review #7)
    if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
        return "", fmt.Errorf("run %q not found (or not a real directory) under %s", id, runsBase)
    }
    return dir, nil
}
```

Register in `cmd/afm/main.go`: `rootCmd.AddCommand(newMemoryCmd())`.

- [ ] **Step 8: Run, verify pass** — `go test ./cmd/afm -run 'TestMemoryRebuild|TestResolveExplicitRunDir' -v` → PASS.

- [ ] **Step 9: Lint + commit**
```bash
make lint-ci
git add pkg/state/state.go pkg/state/run_lookup_completed_test.go cmd/afm/memory.go cmd/afm/memory_rebuild.go cmd/afm/memory_rebuild_test.go cmd/afm/main.go
git commit -m "feat(memory-rebuild): stage-set-aware резолвер completed-run и скелет команды"
```

---

## Task 2: Read-only RunLock + `progress.ErrLockBusy` (contention vs I/O)

Fixes review **#8**. A lock helper must distinguish real contention from `EACCES`/missing-parent/FD-exhaustion.

**Files:**
- Modify: `pkg/progress/progress.go` (sentinel), `pkg/progress/flock_unix.go`, `pkg/progress/flock_windows.go` (return sentinel only on true busy)
- Create: `pkg/progress/flock_test.go` (or extend)
- Create: `pkg/state/runlock.go`, `pkg/state/runlock_test.go`
- Modify: `pkg/state/store.go`

**Interfaces:**
- Produces: `progress.ErrLockBusy` (returned by `TryLock` ONLY on `EWOULDBLOCK`/`EAGAIN`); any other error is a real error.
- Produces: `state.TryLockRun(runDir) (*RunLock, error)` → `ErrRunLocked` on busy, propagates I/O errors; `(*RunLock).Close() error`.

- [ ] **Step 1: Failing test for busy-vs-error in progress**

```go
func TestTryLock_BusyReturnsSentinel(t *testing.T) {
    p := filepath.Join(t.TempDir(), ".lock")
    a, _ := NewLock(p); if err := a.TryLock(); err != nil { t.Fatal(err) }
    defer a.Unlock()
    b, _ := NewLock(p)
    if err := b.TryLock(); !errors.Is(err, ErrLockBusy) { t.Fatalf("want ErrLockBusy, got %v", err) }
}

func TestTryLock_OpenErrorNotBusy(t *testing.T) {
    // parent dir does not exist -> open fails -> NOT ErrLockBusy
    p := filepath.Join(t.TempDir(), "missing", ".lock")
    l, _ := NewLock(p)
    err := l.TryLock()
    if err == nil || errors.Is(err, ErrLockBusy) { t.Fatalf("open error must not be ErrLockBusy, got %v", err) }
}
```

- [ ] **Step 2: Run, verify fail; implement sentinel in progress**

`pkg/progress/progress.go`: `var ErrLockBusy = errors.New("lock held by another process")`.
`flock_unix.go` `TryLock`: separate the open error from the flock error, and map only busy:
```go
func (l *Lock) TryLock() error {
    f, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDWR, 0644)
    if err != nil { return fmt.Errorf("open lock file: %w", err) }
    if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
        f.Close()
        if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
            return ErrLockBusy
        }
        return fmt.Errorf("flock: %w", err)
    }
    l.f = f
    return nil
}
```
Mirror in `flock_windows.go` (review #12 — concrete, no placeholder): `TryLock` uses `LockFileEx` with `LOCKFILE_FAIL_IMMEDIATELY`, which returns `windows.ERROR_LOCK_VIOLATION` on contention. Map only that to `ErrLockBusy`:
```go
if err := windows.LockFileEx(h, flags, 0, 1, 0, ol); err != nil {
    f.Close()
    if errors.Is(err, windows.ERROR_LOCK_VIOLATION) { return ErrLockBusy }
    return fmt.Errorf("LockFileEx: %w", err)
}
```
Keep `Lock()`/`Unlock()` unchanged. Add a Windows-tagged test for the busy mapping (build-tagged `//go:build windows`, run in CI's Windows matrix if present; otherwise it at least compiles under `GOOS=windows go vet`).

- [ ] **Step 3: Failing tests for RunLock**

`pkg/state/runlock_test.go` — mutual exclusion with `Store.Open` both directions; lock creates only `.lock` (no `events.jsonl`/`state.json`); a genuine I/O error (e.g. runDir is a file, not a dir) propagates and is NOT `ErrRunLocked`.

- [ ] **Step 4: Run, verify fail; implement `runlock.go` + refactor `Store.Open`**

```go
type RunLock struct { l *progress.Lock }

func TryLockRun(runDir string) (*RunLock, error) {
    l, err := acquireRunLock(runDir)
    if err != nil { return nil, err }
    return &RunLock{l: l}, nil
}
func (r *RunLock) Close() error { if r.l != nil { r.l.Unlock(); r.l = nil }; return nil }

// acquireRunLock is shared by Store.Open and TryLockRun. Busy -> ErrRunLocked;
// any other failure propagates (review #8).
func acquireRunLock(runDir string) (*progress.Lock, error) {
    l, err := progress.NewLock(filepath.Join(runDir, ".lock"))
    if err != nil { return nil, err }
    if err := l.TryLock(); err != nil {
        if errors.Is(err, progress.ErrLockBusy) { return nil, ErrRunLocked }
        return nil, err
    }
    return l, nil
}
```
Point `Store.Open` at `acquireRunLock`; keep its `Close` unlock behavior.

- [ ] **Step 5: Run tests** — `go test ./pkg/progress ./pkg/state -v` → PASS.

- [ ] **Step 6: Lint + commit**
```bash
make lint-ci
git add pkg/progress/ pkg/state/runlock.go pkg/state/runlock_test.go pkg/state/store.go
git commit -m "feat(memory-rebuild): RunLock и ErrLockBusy — различать contention и I/O ошибку"
```

---

## Task 3: Path-safety + stage-ID validation in `flow.validate` (early, breaking)

Fixes review **#7** and moves validation BEFORE any new path joins (review's ordering point). Breaking for previously-unsafe configs.

**Files:**
- Modify: `pkg/flow/flow.go` (`validate()`), `pkg/flow/flow_test.go`

**Interfaces:**
- Modifies: `flow.validate()` — rejects unsafe stage `id`/`name` used as a path component, unsafe `reflect.file`, and `reflect.file` colliding with the project target after `Clean`.

- [ ] **Step 1: Failing tests**

```go
func TestValidate_StageIDPathSafety(t *testing.T) {
    for _, id := range []string{"a/b", `a\b`, ".", "..", "", "../x"} {
        f := minimalFlowWithStageID(id)
        if err := f.validate(); err == nil { t.Errorf("stage id %q must be rejected", id) }
    }
}

func TestValidate_ReflectFilePathSafety(t *testing.T) {
    for _, rf := range []string{"../escape.md", "/abs/mem.md", "a/../../b.md", "memory.md", "./memory.md", "sub/../memory.md"} {
        f := minimalFlowWithReflect(rf) // memory.path set; one non-script stage reflect{file: rf, mode: rw}
        if err := f.validate(); err == nil { t.Errorf("reflect.file %q must be rejected", rf) }
    }
    if err := minimalFlowWithReflect("sub/dir/stage.md").validate(); err != nil {
        t.Errorf("safe reflect.file rejected: %v", err)
    }
}
```

Add `minimalFlowWithStageID`/`minimalFlowWithReflect` helpers (reuse the package's existing flow-builder test helpers).

- [ ] **Step 2: Run, verify fail; implement**

In `validate()`: add a stage-ID/path check applied to every stage's identity used on disk (whatever field becomes `<stageID>` — confirm whether it's `Stage.ID` or `Stage.Name`; grep how run dirs/stage dirs are named, e.g. `filepath.Join(runDir, s.ID)`):
```go
func safePathComponent(s string) bool { // review #12: also reject NUL and any separator
    return s != "" && s != "." && s != ".." &&
        !strings.ContainsAny(s, `/\`) && !strings.ContainsRune(s, 0)
}
```
Reject when `!safePathComponent(stageID)`. In the memory block, for each stage with `Reflect != nil`:
```go
rf := stage.Reflect.File
if !filepath.IsLocal(rf) {
    return fmt.Errorf("stage %q: reflect.file %q must be a local relative path (no .. or absolute)", stage.Name, rf)
}
if filepath.Clean(rf) == "memory.md" {
    return fmt.Errorf("stage %q: reflect.file must not be memory.md (collides with the project memory file)", stage.Name)
}
```
(`filepath.Clean("./memory.md") == "memory.md"`, catching the `./` alias.)

- [ ] **Step 3: Run, verify pass** — `go test ./pkg/flow -v` → PASS.

- [ ] **Step 4: Lint + commit**
```bash
make lint-ci
git add pkg/flow/flow.go pkg/flow/flow_test.go
git commit -m "feat(memory-rebuild): валидация path-safety stage id и reflect.file в flow.validate"
```

---

## Task 4: Extract the memory engine into `pkg/memorypipeline`; strip `AFM_STAGE_DIR`; export `WrapperDirFor`

Fixes review **#4** (AFM_STAGE_DIR leak) and **#12** (wrapperDirFor visibility). Mechanical move — keep live tests green.

**Files:**
- Modify: `pkg/executor/executor.go` (strip inherited `AFM_STAGE_DIR`), `pkg/executor/executor_test.go`
- Create: `pkg/executor/wrapper.go` (move `WrapperDirFor`) — or export in place; update `pkg/orchestrator/runner_factory.go`
- Create: `pkg/memorypipeline/types.go`, `prompts.go`, `executor.go`, `pipeline_test.go`
- Modify: `pkg/orchestrator/memory_prompts.go`, `memory_agent.go`, `orchestrator.go`, `reflection_test.go`

**Interfaces:**
- Produces (public):
  ```go
  type Prompts struct { Reflect, Aggregate, Prioritize, Update string }
  type AgentConfig struct { Command string; ExtraArgs []string; WrapperDir, RootDir, RunDir string; IdleTimeout time.Duration; Debug bool }
  const ( KindReflect = "reflect"; KindAggregate = "aggregate"; KindPrioritize = "prioritize"; KindUpdate = "update" )
  type AgentSpec struct { Kind, StageName, Command, LogFile string; Sources []string; DatasetOut string; InPaths []string; In, Out, HighPath, TargetFile string; MaxRules int }
  type AgentRunner func(ctx context.Context, spec AgentSpec) error
  func BuildPrompt(p Prompts, spec AgentSpec) string
  func NewExecRunner(cfg AgentConfig, prompts Prompts) AgentRunner
  ```
- Produces: `executor.WrapperDirFor(cmd, wrapperDir string, generated map[string]bool) string`.

- [ ] **Step 1: Failing test — executor strips AFM_STAGE_DIR when StageDir==""**

`pkg/executor/executor_test.go`: run a tiny agent command (a shell script echoing `$AFM_STAGE_DIR`) with `Config{StageDir:""}` while the parent env has `AFM_STAGE_DIR=/leaked`; assert the child sees it empty. If the package has no such harness, assert at the env-construction layer (extract env building into a testable `buildAgentEnv(cfg) []string` if needed).

- [ ] **Step 2: Run, verify fail; implement the strip**

In the env filter loop, add a case so inherited `AFM_STAGE_DIR` is always dropped and only re-added when set:
```go
case strings.HasPrefix(kv, "AFM_STAGE_DIR="):
    // always strip inherited; re-added below only if cfg.StageDir != ""
```
(Keeps the existing `if e.cfg.StageDir != "" { filtered = append(..., "AFM_STAGE_DIR="+...) }`.)

- [ ] **Step 3: Move `wrapperDirFor` → `executor.WrapperDirFor`**

Move the body from `pkg/orchestrator/runner_factory.go` into `pkg/executor` (exported), update the orchestrator call site to `executor.WrapperDirFor(...)`. Run `go build ./...`.

- [ ] **Step 4: Create `pkg/memorypipeline` by copying engine code verbatim**

Copy `memoryAgentSpec`→`AgentSpec` (exported fields), kind consts→`Kind*`, `buildMemoryPrompt`→`BuildPrompt` (byte-for-byte prompt text, incl. `<FILEPATH>`/`<MAX_RULES>` substitution). `executor.go`:
```go
func NewExecRunner(cfg AgentConfig, prompts Prompts) AgentRunner {
    return func(ctx context.Context, spec AgentSpec) error {
        cmd, extra := spec.Command, []string(nil)
        if cmd == "" { cmd, extra = cfg.Command, cfg.ExtraArgs }
        ex := executor.New(executor.Config{
            Command: cmd, ExtraArgs: executor.ResolveArgs(extra),
            IdleTimeout: cfg.IdleTimeout, WrapperDir: cfg.WrapperDir,
            Dir: cfg.RootDir, RunDir: cfg.RunDir, Debug: cfg.Debug,
            // fresh context: SessionID/Resume/StageDir intentionally unset (AFM_STAGE_DIR stripped by executor)
        })
        return ex.RunAgent(ctx, "memory-"+spec.Kind, spec.StageName, BuildPrompt(prompts, spec), spec.LogFile)
    }
}
```
Move `memory_prompts_test.go` + prompt-building parts of `memory_agent_test.go` into `pipeline_test.go` (exported names, same assertions).

- [ ] **Step 5: Orchestrator delegates**

Replace the private spec/prompt/exec bodies with a stored `memorypipeline.AgentRunner` (`o.memRunner`), built in `orchestrator.New` from `o.opts.Config.Client.*` + `executor.WrapperDirFor(o.opts.Config.Client.Command, o.opts.WrapperDir, o.opts.GeneratedAgents)` + `o.opts.RootDir`/`RunDir`/`Debug` + `memorypipeline.Prompts{...}`. `distill`/`maybeRunReflection` build `memorypipeline.AgentSpec`. Keep the test seam: tests set `o.memRunner`. Update `reflection_test.go`'s stub accordingly. Memory specs always leave `Command:""` → preserves global-command semantics.

- [ ] **Step 6: Run + race** — `go test ./pkg/executor ./pkg/memorypipeline ./pkg/orchestrator/...` then `go test -race ./pkg/orchestrator/... ./pkg/memorypipeline/...` → PASS.

- [ ] **Step 7: Lint + commit**
```bash
make lint-ci
git add pkg/executor/ pkg/memorypipeline/ pkg/orchestrator/memory_prompts.go pkg/orchestrator/memory_agent.go pkg/orchestrator/orchestrator.go pkg/orchestrator/runner_factory.go pkg/orchestrator/reflection_test.go
git commit -m "refactor(memory): вынести engine в pkg/memorypipeline, strip AFM_STAGE_DIR, экспорт WrapperDirFor"
```

---

## Task 5: Source inventory + dataset validation

Fixes review **#9** (abs paths, agent-session vs supplemental, `plan.vN`, no silent Lstat swallow) and **#10** (both sections required, trailing-doc rejection).

**Files:**
- Create: `pkg/memorypipeline/sources.go`, `sources_test.go`, `dataset.go`, `dataset_test.go`
- Modify: `pkg/memorypipeline/prompts.go` (reflect: explicit file list), `pipeline_test.go`

**Interfaces:**
- Produces: `SourceInventory(stageDir string) (Inventory, error)` where `type Inventory struct { AgentSessions, Supplemental []string }` — **absolute** paths, sorted, no symlinks; `func (i Inventory) All() []string`; `func (i Inventory) HasAgentSession() bool`.
- Produces: `ValidateDataset(data []byte) error`, `ParseDataset(data []byte) (Dataset, error)`, `const maxDatasetBytes = 10 << 20`.

- [ ] **Step 1: Failing tests for `SourceInventory`**

Cover (each an assertion): agent-session files (phase logs/jsonl/stderr, autonomous, `plan.md`, `plan.v1.md`/`plan.v2.md`, `execution_summary.md`, dialog/question/answer) are classified `AgentSessions`; user/hook files (`prenote.md`, `feedback.md`, `before.log`, `after.log`, stderr) are `Supplemental`; memory artifacts (`reflect*`,`aggregate*`,`prioritize*`,`update*`,`patterns.md`,`prioritized.md`,`high.md`,`reflect_dataset.yaml`) and the `memory-rebuild/` dir are excluded; **a stage with only `before.log`/`after.log`/`script.log` has `HasAgentSession()==false`** (review #9); results are **absolute** and sorted; symlinks skipped; a `Lstat` error on an entry is returned, not swallowed.

- [ ] **Step 2: Run, verify fail; implement `sources.go`**

```go
type Inventory struct{ AgentSessions, Supplemental []string }
// All returns agent-session sources FIRST, then supplemental — this grouping is
// intentional and deterministic (each group is independently sorted), so the reflect
// prompt sees the primary session material before hook/user notes (review #12).
func (i Inventory) All() []string { return append(append([]string{}, i.AgentSessions...), i.Supplemental...) }
func (i Inventory) HasAgentSession() bool { return len(i.AgentSessions) > 0 }

func agentSessionNames() map[string]bool {
    m := map[string]bool{"plan.md": true, "execution_summary.md": true}
    for _, p := range flow.Phases() {
        for _, f := range flow.PhaseLogFiles(p) { m[f] = true; m[strings.TrimSuffix(f, ".log")+".stderr.log"] = true }
        for _, f := range flow.PhaseStreamLogs(p) { m[f] = true }
    }
    return m
}
var supplementalNames = map[string]bool{
    "prenote.md": true, "feedback.md": true,
    "before.log": true, "after.log": true, "before.stderr.log": true, "after.stderr.log": true,
}
var planVersionRe = regexp.MustCompile(`^plan\.v\d+\.md$`)
func isDialog(n string) bool {
    return strings.HasSuffix(n, ".dialog.jsonl") || strings.HasSuffix(n, ".question.json") || strings.HasSuffix(n, ".answer.json")
}
var artifactPrefixes = []string{"reflect", "aggregate", "prioritize", "update", "patterns", "prioritized", "high"}
func isArtifact(n string) bool {
    if n == "reflect_dataset.yaml" { return true }
    for _, p := range artifactPrefixes { if strings.HasPrefix(n, p) { return true } }
    return false
}

func SourceInventory(stageDir string) (Inventory, error) {
    entries, err := os.ReadDir(stageDir)
    if err != nil {
        if os.IsNotExist(err) { return Inventory{}, nil }
        return Inventory{}, err
    }
    abs, err := filepath.Abs(stageDir)
    if err != nil { return Inventory{}, err }
    sess := agentSessionNames()
    var inv Inventory
    for _, e := range entries {
        n := e.Name()
        if e.IsDir() || isArtifact(n) { continue }
        info, err := os.Lstat(filepath.Join(stageDir, n))
        if err != nil { return Inventory{}, err } // do not swallow (review #9)
        if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() { continue }
        full := filepath.Join(abs, n)
        switch {
        case sess[n] || isDialog(n) || planVersionRe.MatchString(n):
            inv.AgentSessions = append(inv.AgentSessions, full)
        case supplementalNames[n]:
            inv.Supplemental = append(inv.Supplemental, full)
        }
    }
    slices.Sort(inv.AgentSessions); slices.Sort(inv.Supplemental)
    return inv, nil
}
```

- [ ] **Step 3: Run, verify pass** — `go test ./pkg/memorypipeline -run TestSourceInventory -v` → PASS.

- [ ] **Step 4: Failing tests for dataset validation (review #10)**

Add cases: valid; both sections empty valid; **missing `session_level` key rejected**; **missing `project_level` key rejected**; unknown top key; bad `source`; empty trimmed field; **a trailing second YAML document rejected**; oversize rejected; a bare scalar document rejected.

- [ ] **Step 5: Run, verify fail; implement `dataset.go`**

```go
const maxDatasetBytes = 10 << 20

type DatasetItem struct { Prompt, Chosen, Rejected, Source string }
type Dataset struct { ProjectLevel, SessionLevel []DatasetItem }

func ParseDataset(data []byte) (Dataset, error) {
    if len(data) > maxDatasetBytes { return Dataset{}, fmt.Errorf("dataset too large: %d bytes", len(data)) }
    var root yaml.Node
    dec := yaml.NewDecoder(bytes.NewReader(data))
    if err := dec.Decode(&root); err != nil { return Dataset{}, fmt.Errorf("parse dataset: %w", err) }
    if err := dec.Decode(new(yaml.Node)); err != io.EOF { // reject a trailing document
        return Dataset{}, fmt.Errorf("dataset must contain exactly one YAML document")
    }
    doc := &root
    if doc.Kind == yaml.DocumentNode && len(doc.Content) == 1 { doc = doc.Content[0] }
    if doc.Kind != yaml.MappingNode { return Dataset{}, fmt.Errorf("dataset root must be a mapping") }
    seen := map[string]bool{}
    for i := 0; i+1 < len(doc.Content); i += 2 {
        key := doc.Content[i].Value
        if key != "project_level" && key != "session_level" {
            return Dataset{}, fmt.Errorf("unknown top-level key %q", key)
        }
        seen[key] = true
    }
    if !seen["project_level"] || !seen["session_level"] {
        return Dataset{}, fmt.Errorf("both project_level and session_level are required")
    }
    var ds struct {
        ProjectLevel []struct{ Prompt, Chosen, Rejected, Source string } `yaml:"project_level"`
        SessionLevel []struct{ Prompt, Chosen, Rejected, Source string } `yaml:"session_level"`
    }
    strict := yaml.NewDecoder(bytes.NewReader(data)); strict.KnownFields(true)
    if err := strict.Decode(&ds); err != nil { return Dataset{}, fmt.Errorf("parse dataset items: %w", err) }
    conv := func(in []struct{ Prompt, Chosen, Rejected, Source string }) []DatasetItem {
        out := make([]DatasetItem, len(in))
        for i, it := range in { out[i] = DatasetItem(it) }
        return out
    }
    return Dataset{ProjectLevel: conv(ds.ProjectLevel), SessionLevel: conv(ds.SessionLevel)}, nil
}

func ValidateDataset(data []byte) error {
    ds, err := ParseDataset(data)
    if err != nil { return err }
    check := func(sec string, items []DatasetItem) error {
        for i, it := range items {
            if strings.TrimSpace(it.Prompt) == "" || strings.TrimSpace(it.Chosen) == "" || strings.TrimSpace(it.Rejected) == "" {
                return fmt.Errorf("%s[%d]: prompt/chosen/rejected must be non-empty", sec, i)
            }
            if it.Source != "user" && it.Source != "log" {
                return fmt.Errorf("%s[%d]: source must be user|log, got %q", sec, i, it.Source)
            }
        }
        return nil
    }
    return errors.Join(check("project_level", ds.ProjectLevel), check("session_level", ds.SessionLevel))
}
```

- [ ] **Step 6: Reflect prompt lists explicit files**

`BuildPrompt` reflect branch: instruct "read exactly these source paths" and inline `spec.Sources` (absolute, one per line). Keep highest-priority-user-input wording. Update the assertion in `pipeline_test.go`.

- [ ] **Step 7: Run, verify pass** — `go test ./pkg/memorypipeline -v` → PASS.

- [ ] **Step 8: Lint + commit**
```bash
make lint-ci
git add pkg/memorypipeline/sources.go pkg/memorypipeline/sources_test.go pkg/memorypipeline/dataset.go pkg/memorypipeline/dataset_test.go pkg/memorypipeline/prompts.go pkg/memorypipeline/pipeline_test.go
git commit -m "feat(memory-rebuild): инвентаризация исходников (agent-session vs supplemental) и строгая валидация dataset"
```

---

## Task 6: Operation metadata, typed errors, manifest lifecycle, durable AtomicWrite

Fixes review **#5** (manifest API + metadata + report/diff), **#3.minor** (typed errors), **#minor.1** (durable AtomicWrite errors propagated), **#minor.4** (cancelled/manifest-write states). This task defines the shared operation API consumed by Tasks 7–8.

**Files:**
- Create: `pkg/memorypipeline/operation.go` (types), `pkg/memorypipeline/manifest.go`, `pkg/memorypipeline/operation_test.go`
- Modify: `pkg/memory/paths.go` (durable `AtomicWrite`, errors returned), `pkg/memory/paths_test.go`

**Interfaces:**
- Produces:
  ```go
  type OperationMeta struct {
      RunID, RunPath, FlowPath string
      FlowSHA256               string
      RootDir, MemoryDir       string
      MemoryMode               string
      MaxRules                 int
      EffectiveCommit          bool
      PromptsDir               string            // prompt source: override dir, or "" = embedded defaults (review #2)
      PromptSHA256             map[string]string // reflect/aggregate/prioritize/update
      StartedAt                string            // RFC3339, supplied by caller (deterministic in tests)
  }
  type StepError struct { StageID, Target, Step, LogPath string; Err error }
  func (e *StepError) Error() string; func (e *StepError) Unwrap() error
  // review #2/#8: DatasetResult carries full provenance + promotion bookkeeping.
  type DatasetResult struct {
      StageID, Source, DatasetPath string // Source: "reused"|"generated"; DatasetPath = staging (generated) or canonical (reused)
      SourceFiles  []string                // inputs the reflect agent read
      SourceHashes []string                // SHA-256 of each SourceFile, positionally aligned
      DatasetSHA256 string                 // SHA-256 of the dataset bytes
      CanonicalPath string                 // <RunDir>/<stageID>/reflect_dataset.yaml (publish target for a generated dataset)
      Published    bool                    // set when a generated dataset has been promoted to CanonicalPath
  }
  type TargetResult struct {
      Label, FinalPath, CandidatePath string
      OldSHA256, NewSHA256            string
      Changed, NoHigh, Published      bool
      Diff                            string // unified diff old->candidate (filled for changed targets)
  }
  type Report struct { RunID, WorkDir string; Datasets []DatasetResult; Targets []TargetResult }
  type Manifest struct { /* all OperationMeta fields + Status, FinishedAt, Datasets, Targets, Errors, CommitCreated bool, CommitSHA, CommitError string */ }
  func WriteManifest(path string, m *Manifest) error   // atomic + durable
  func LoadManifest(path string) (*Manifest, error)
  func NewUniqueAttemptDir(base string, gen func() string) (string, error) // MkdirAll(base) + os.Mkdir(child), bounded retry on EEXIST
  func ScanUnfinishedPromotions(runDir string) []string // attempt dirs of runDir with Status=="promoting"
  // manifest lifecycle helpers used by the CLI (Task 11):
  func NewRunningManifest(meta OperationMeta) *Manifest                       // status "running" + full meta + StartedAt
  func FinalizeManifestOnExit(path string, finalErr *error, ctx context.Context) // defer: non-terminal -> "cancelled" if ctx.Err(), else "failed"
  func MarkManifestCompleted(path string) error                              // status -> "completed"
  ```
- **Manifest state machine (single source of truth — review #2).** Exactly one lifecycle, driven by the CLI (Task 11) + `Finalize` (Task 8):
  `running` (CLI, right after the attempt dir is created, with full `OperationMeta`) → `capturing` (CaptureAll) → `distilling` (Finalize) → then a Finalize terminal-for-its-phase status: `dry_run` | `cancelled` | `failed` | `promoting` → `awaiting_commit` (all files published, commit pending) OR `published` (no commit requested). The CLI then sets the FINAL status: `completed` (committed or no-commit) or `failed` (commit failed, with `CommitError`). A central `defer` in the handler flips any still-non-terminal manifest to `failed`/`cancelled`. `Finalize` never writes `completed` — that belongs to the CLI after the commit decision.
- **Clock seam:** `Pipeline` holds `clock func() time.Time` (default `time.Now`, overridable via `WithClock` in tests) used for `FinishedAt`; `StartedAt` is supplied in `OperationMeta` (both deterministic under test).
- Modifies: `memory.AtomicWrite` — fsync file + parent, **return** parent open/sync errors AND the `d.Close()` error (review #12; no longer best-effort).

- [ ] **Step 1: Durable `AtomicWrite` first**

`pkg/memory/paths_test.go`: assert write+replace, no leftover `tmp-*`, and (error path) that a temp-write failure surfaces. Update impl:
```go
func AtomicWrite(path string, data []byte) error {
    dir := filepath.Dir(path)
    if err := os.MkdirAll(dir, 0755); err != nil { return err }
    tmp, err := os.CreateTemp(dir, "tmp-*")
    if err != nil { return err }
    tmpPath := tmp.Name()
    if _, err := tmp.Write(data); err != nil { tmp.Close(); os.Remove(tmpPath); return err }
    if err := tmp.Sync(); err != nil { tmp.Close(); os.Remove(tmpPath); return err }
    if err := tmp.Close(); err != nil { os.Remove(tmpPath); return err }
    if err := os.Rename(tmpPath, path); err != nil { os.Remove(tmpPath); return err }
    d, err := os.Open(dir)
    if err != nil { return fmt.Errorf("open parent for fsync: %w", err) }
    if err := d.Sync(); err != nil { d.Close(); return fmt.Errorf("fsync parent: %w", err) }
    if err := d.Close(); err != nil { return fmt.Errorf("close parent after fsync: %w", err) }
    return nil
}
```
Run `go test ./pkg/memory -v` → PASS.
**Caveat for the promotion protocol (review #12):** the rename already succeeded before the parent fsync; an error return here does NOT mean the bytes are absent. Task 8's promotion treats a target as `Published` on rename success and only records this fsync error, never re-attempting the rename.

- [ ] **Step 2: Failing tests for manifest + StepError + attempt-dir**

`operation_test.go`: `WriteManifest`→`LoadManifest` round-trip; atomic (no `tmp-*` left); `StepError.Error()` includes stage/step/log; `NewUniqueAttemptDir` (a) creates a missing parent `<run>/memory-rebuild` (review #9: first rebuild must not `ENOENT`), (b) `os.Mkdir`s a fresh child exclusively, (c) on `EEXIST` retries with a new `gen()` id up to a bound (e.g. 8 tries) then errors; `ScanUnfinishedPromotions(runDir)` returns attempt dirs whose manifest is `promoting` (review #6).

- [ ] **Step 3: Implement `operation.go` + `manifest.go`**

```go
func NewUniqueAttemptDir(base string, gen func() string) (string, error) {
    if err := os.MkdirAll(base, 0755); err != nil { return "", err } // stable parent (review #9)
    for i := 0; i < 8; i++ {
        dir := filepath.Join(base, gen())
        err := os.Mkdir(dir, 0755) // exclusive: fails on EEXIST
        if err == nil { return dir, nil }
        if !errors.Is(err, fs.ErrExist) { return "", err }
    }
    return "", fmt.Errorf("could not allocate a unique attempt dir under %s", base)
}
```
`WriteManifest` uses `memory.AtomicWrite(path, json.MarshalIndent(...))`. `ScanUnfinishedPromotions` globs `<runDir>/memory-rebuild/*/manifest.json`, loads each, collects `Status=="promoting"`. **Scope note (review #12):** this scan is deliberately per-run (the selected run's attempts) — a stale `promoting` from a *different* run writing the same `memory.path` is not surfaced here; that is acceptable because re-running rebuild rebuilds cleanly from canonical/attempt state, and the shared memory lock prevents concurrent promotion. Add the clock seam here: `Pipeline.clock`/`WithClock` for `FinishedAt`.

- [ ] **Step 4: Run, verify pass** — `go test ./pkg/memorypipeline -run 'Manifest|StepError|Attempt|Promotion' -v` → PASS.

- [ ] **Step 5: Lint + commit**
```bash
make lint-ci
git add pkg/memorypipeline/operation.go pkg/memorypipeline/manifest.go pkg/memorypipeline/operation_test.go pkg/memory/paths.go pkg/memory/paths_test.go
git commit -m "feat(memory-rebuild): operation metadata, typed StepError, manifest lifecycle, durable AtomicWrite"
```

---

## Task 7: Staging distill + strict freshness + NoHigh/empty-dataset semantics + rules

Fixes review **#3** (NoHigh keeps candidate), **#10** (freshness via Lstat, ValidateRules strict), **#11** (empty-dataset no-op).

**Files:**
- Create: `pkg/memorypipeline/distill.go`, `rules.go`, `distill_test.go`
- Modify: `pkg/memorypipeline/operation.go` (add `DistillTarget`, `StepArtifacts` if not already)

**Interfaces:**
- Produces:
  ```go
  type DistillTarget struct { Name string; Datasets []string; StagingDir, SeedFrom string; MaxRules int }
  // SeedFrom: path whose bytes seed the staging candidate (the real target, or a prior stage's candidate).
  type StepArtifacts struct { PatternsPath, PrioritizedPath, HighPath, CandidatePath string; NoHigh bool }
  func (p *Pipeline) DistillTarget(ctx context.Context, t DistillTarget) (StepArtifacts, error)
  func ValidateRules(md string, maxRules int) error
  func requireFreshFile(path string, maxBytes int64) error // Lstat (reject symlink), regular, size in (0, maxBytes]
  const maxRulesBytes = 1 << 20 // Markdown output bound (distinct from maxDatasetBytes = 10 MiB — review #11)
  ```
- **Freshness contract (review #4).** `requireFreshFile` alone cannot prove an agent produced output *this* call — a stale non-empty file satisfies Lstat+regular+size. So each agent call that writes a NEW file (`aggregate`→patterns, `prioritize`→prioritized, `reflect`→dataset) is preceded by `os.Remove(path)` (idempotent; the attempt dir is exclusive so nothing else writes there), then followed by `requireFreshFile`. `update` is the exception: its output IS the seeded candidate; it may legitimately leave bytes unchanged, so after `update` we do NOT demand a change — we `Lstat` (reject a symlink swap — review #4), then `os.ReadFile` and `ValidateRules`. The concept for `update` is "valid candidate", not "fresh output".
- Key rule: the candidate (`<StagingDir>/target.md`) is **always** seeded from `SeedFrom` FIRST. On `NoHigh` and on empty datasets, `CandidatePath` exists and equals the seed — so a downstream stage sharing the file never loses prior rules (review #3).

- [ ] **Step 1: Failing tests**

```go
func TestDistillTarget_SeedsBeforeAggregate_NoHighKeepsCandidate(t *testing.T) {
    // prioritize returns empty High -> NoHigh true; CandidatePath must exist and equal SeedFrom bytes
}
func TestDistillTarget_EmptyDatasetsNoOp(t *testing.T) {
    // all datasets project_level:[]/session_level:[] -> no aggregate/prioritize/update calls,
    // NoHigh true, candidate == seed
}
func TestDistillTarget_ChainOrderAndStaging(t *testing.T) { /* aggregate,prioritize,update; update TargetFile == CandidatePath, never SeedFrom */ }
func TestDistillTarget_AggregateWritesNothingRejected(t *testing.T) { /* pre-place a stale patterns.md; runner writes nothing; DistillTarget removes it first, so requireFreshFile fails -> StepError */ }
func TestDistillTarget_UpdateSymlinkCandidateRejected(t *testing.T) { /* update runner replaces candidate with a symlink; Lstat-before-read rejects it */ }
func TestDistillTarget_DatasetReadErrorSurfaces(t *testing.T) { /* an unreadable dataset -> datasetsAllEmpty returns the error, DistillTarget returns it, no aggregate call (review #11) */ }
func TestValidateRules(t *testing.T) { /* header required at top; only "## <Pattern>"; >=1 pattern; tier heading any case rejected; extra H1 rejected; cap; size */ }
```

- [ ] **Step 2: Run, verify fail; implement `rules.go`**

`ValidateRules`: require the FIRST non-blank line to be exactly `# Project rules`; reject any other `# ` H1; count `## ` blocks, reject tier headings case-insensitively (`strings.EqualFold(line, "## High")` etc.); require `count >= 1`; enforce `count <= maxRules` when `maxRules>0`; UTF-8; total size `<= 1<<20`.

- [ ] **Step 3: Implement `distill.go`**

```go
func (p *Pipeline) DistillTarget(ctx context.Context, t DistillTarget) (StepArtifacts, error) {
    if err := os.MkdirAll(t.StagingDir, 0755); err != nil { return StepArtifacts{}, err }
    a := StepArtifacts{
        PatternsPath:    filepath.Join(t.StagingDir, "patterns.md"),
        PrioritizedPath: filepath.Join(t.StagingDir, "prioritized.md"),
        HighPath:        filepath.Join(t.StagingDir, "high.md"),
        CandidatePath:   filepath.Join(t.StagingDir, "target.md"),
    }
    if err := seedCandidate(t.SeedFrom, a.CandidatePath); err != nil { return a, err } // ALWAYS first (review #3)
    empty, err := datasetsAllEmpty(t.Datasets)
    if err != nil { return a, &StepError{Target: t.Name, Step: "aggregate", Err: err} } // read/parse error surfaces (review #11)
    if empty { a.NoHigh = true; return a, nil }                                          // review #11
    _ = os.Remove(a.PatternsPath) // require the agent to actually produce it this call (review #4)
    if err := p.run(ctx, AgentSpec{Kind: KindAggregate, StageName: t.Name, InPaths: t.Datasets,
        Out: a.PatternsPath, LogFile: filepath.Join(t.StagingDir, "aggregate.log")}); err != nil {
        return a, &StepError{Target: t.Name, Step: "aggregate", LogPath: filepath.Join(t.StagingDir, "aggregate.log"), Err: err}
    }
    if err := requireFreshFile(a.PatternsPath, maxRulesBytes); err != nil { return a, &StepError{Target: t.Name, Step: "aggregate", Err: err} }
    _ = os.Remove(a.PrioritizedPath)
    if err := p.run(ctx, AgentSpec{Kind: KindPrioritize, StageName: t.Name, In: a.PatternsPath,
        Out: a.PrioritizedPath, LogFile: filepath.Join(t.StagingDir, "prioritize.log")}); err != nil {
        return a, &StepError{Target: t.Name, Step: "prioritize", LogPath: filepath.Join(t.StagingDir, "prioritize.log"), Err: err}
    }
    if err := requireFreshFile(a.PrioritizedPath, maxRulesBytes); err != nil { return a, &StepError{Target: t.Name, Step: "prioritize", Err: err} }
    pr, err := os.ReadFile(a.PrioritizedPath); if err != nil { return a, err }
    high := memory.SelectHigh(string(pr))
    if strings.TrimSpace(high) == "" { a.NoHigh = true; return a, nil } // candidate already == seed
    if err := memory.AtomicWrite(a.HighPath, []byte(high)); err != nil { return a, err }
    if err := p.run(ctx, AgentSpec{Kind: KindUpdate, StageName: t.Name, HighPath: a.HighPath,
        TargetFile: a.CandidatePath, MaxRules: t.MaxRules, LogFile: filepath.Join(t.StagingDir, "update.log")}); err != nil {
        return a, &StepError{Target: t.Name, Step: "update", LogPath: filepath.Join(t.StagingDir, "update.log"), Err: err}
    }
    // update's output IS the seeded candidate — do not remove-before; may be unchanged; reject a symlink swap (review #4).
    info, err := os.Lstat(a.CandidatePath)
    if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
        return a, &StepError{Target: t.Name, Step: "update", Err: fmt.Errorf("candidate missing or not a regular file: %v", err)}
    }
    cand, err := os.ReadFile(a.CandidatePath); if err != nil { return a, &StepError{Target: t.Name, Step: "update", Err: err} }
    if err := ValidateRules(string(cand), t.MaxRules); err != nil { return a, &StepError{Target: t.Name, Step: "update", Err: err} }
    return a, nil
}

func seedCandidate(seedFrom, candidate string) error {
    data, err := os.ReadFile(seedFrom) // review #5: containment of seedFrom is enforced by validateTargetUnderMemoryDir at the Finalize call site, under the memory lock
    if err != nil { if os.IsNotExist(err) { data = nil } else { return err } }
    return memory.AtomicWrite(candidate, data)
}
func datasetsAllEmpty(paths []string) (bool, error) { // review #11: surface read/parse errors, don't mask as "non-empty"
    for _, p := range paths {
        b, err := os.ReadFile(p); if err != nil { return false, err }
        ds, err := ParseDataset(b); if err != nil { return false, err }
        if len(ds.ProjectLevel) > 0 || len(ds.SessionLevel) > 0 { return false, nil }
    }
    return true, nil
}
func requireFreshFile(path string, maxBytes int64) error {
    info, err := os.Lstat(path)
    if err != nil { return fmt.Errorf("expected output %s: %w", filepath.Base(path), err) }
    if info.Mode()&os.ModeSymlink != 0 { return fmt.Errorf("%s is a symlink", filepath.Base(path)) }
    if !info.Mode().IsRegular() || info.Size() == 0 { return fmt.Errorf("%s is empty or not a regular file", filepath.Base(path)) }
    if info.Size() > maxBytes { return fmt.Errorf("%s exceeds size bound %d", filepath.Base(path), maxBytes) }
    return nil
}
```
`Pipeline`/`New`/`WithRunner` (functional option overriding the default `NewExecRunner`) live here or in `operation.go` — keep `New(prompts Prompts, agent AgentConfig, opts ...Option) *Pipeline`.

- [ ] **Step 4: Run + race** — `go test -race ./pkg/memorypipeline -v` → PASS.

- [ ] **Step 5: Lint + commit**
```bash
make lint-ci
git add pkg/memorypipeline/distill.go pkg/memorypipeline/rules.go pkg/memorypipeline/distill_test.go pkg/memorypipeline/operation.go
git commit -m "feat(memory-rebuild): staging distill с seed-before-aggregate, NoHigh/empty no-op и строгая freshness"
```

---

## Task 8: Two-phase Rebuilder — Capture (no lock) + Finalize (lock held by caller) + staged promotion

Fixes review **#2** (lock ownership), **#6** (staged/durable promotion, recovery), **#15** (ctx before promotion). The pipeline is split so the CLI/orchestrator own the memory lock around Finalize+commit.

**Files:**
- Create: `pkg/memorypipeline/rebuild.go`, `rebuild_test.go`
- Modify: `pkg/memorypipeline/operation.go` (`CaptureRequest`/`FinalizeRequest`)

**Interfaces:**
- Produces:
  ```go
  // CaptureStage is the single low-level capture primitive (review #3): it writes
  // (or reuses) ONE stage's dataset at the caller-chosen DatasetOut. Offline passes
  // a staging path; the live orchestrator passes the canonical path directly. This
  // is the explicit "output mode" the first plan lacked.
  func (p *Pipeline) CaptureStage(ctx context.Context, stage flow.Stage, stageDir, datasetOut, logDir string, force bool) (DatasetResult, error)

  // CaptureAll iterates CaptureStage over eligible stages, always writing generated
  // datasets into <WorkDir>/stages/<id>/reflect_dataset.yaml (canonical untouched
  // until promotion). No shared memory lock needed. Requires a non-empty ManifestPath.
  func (p *Pipeline) CaptureAll(ctx context.Context, req CaptureRequest) ([]DatasetResult, error)
  type CaptureRequest struct { RunDir, WorkDir string; Stages []flow.Stage; ForceReflect bool; ManifestPath string }

  // Finalize distills + computes diffs + (unless DryRun) publishes, ASSUMING the
  // caller already holds the memory lock for MemoryDir. Writes/updates ManifestPath
  // per step. Never commits, never writes "completed" (caller does, after commit).
  func (p *Pipeline) Finalize(ctx context.Context, req FinalizeRequest) (Report, error)
  type AtomicWriter func(path string, data []byte) error // seam over memory.AtomicWrite (review #8)
  type FinalizeRequest struct {
      Meta OperationMeta; RunDir, WorkDir, MemoryDir string
      Stages []flow.Stage; Memory flow.MemoryConfig; Datasets []DatasetResult
      DryRun bool; ManifestPath string
      writer AtomicWriter // test seam; nil -> memory.AtomicWrite. Receives (path, bytes) so it can fully replace the write (review #8)
  }
  ```
- **Live path uses `CaptureStage` directly (review #3):** the orchestrator (Task 10) calls `CaptureStage(ctx, stage, stageDir, canonical, stageDir, false)` where `canonical = <RunDir>/<stageID>/reflect_dataset.yaml`, so live capture writes the canonical dataset in place BEFORE end-of-run `Finalize` reads it — no staging/promotion for the live capture, matching today's behavior.

- [ ] **Step 1: Failing tests for `CaptureStage` + `CaptureAll`**

`CaptureStage`: reuse-valid (no reflect call, `DatasetPath==datasetOut` untouched when reuse points at canonical); invalid canonical → regenerate at `datasetOut`; `force` → regenerate; script-only / `!HasAgentSession()` → `StepError` (review #9); `datasetOut` written to the caller's chosen path (test both a staging path and a canonical path — proves live/offline share the primitive). `CaptureAll`: writes generated into `WorkDir/stages/<id>/reflect_dataset.yaml`; canonical untouched; result carries `SourceFiles`/`SourceHashes`/`DatasetSHA256`/`CanonicalPath`; manifest advanced to `capturing`.

- [ ] **Step 2: Implement `CaptureStage` then `CaptureAll`**

`CaptureStage(ctx, stage, stageDir, datasetOut, logDir, force)`: `canonical = <stageDir>/reflect_dataset.yaml`; if `!force && datasetOut==canonical` and canonical passes `ValidateDataset` → `DatasetResult{Source:"reused", DatasetPath: canonical, CanonicalPath: canonical, DatasetSHA256: sha}`. Else `SourceInventory(stageDir)`; if `!inv.HasAgentSession()` → `&StepError{StageID: stage.ID, Step:"reflect", Err: fmt.Errorf("no agent-session sources in %s", stageDir)}`; `os.Remove(datasetOut)` then run reflect (`Sources: inv.All()`, `DatasetOut: datasetOut`, `LogFile: <logDir>/reflect.log`); `requireFreshFile(datasetOut, maxDatasetBytes)` (review #11 bound) + `ValidateDataset`; return `DatasetResult{Source:"generated", DatasetPath: datasetOut, CanonicalPath: canonical, SourceFiles: inv.All(), SourceHashes: sha256Each(inv.All()), DatasetSHA256: sha}`.

`CaptureAll`: for each stage with `Reflect != nil && CanWrite() && !IsScript()` (declaration order), call `CaptureStage(ctx, stage, <RunDir>/<stageID>, <WorkDir>/stages/<stageID>/reflect_dataset.yaml, <WorkDir>/stages/<stageID>, req.ForceReflect)` — note the reuse branch still points `DatasetPath` at canonical when valid. Update manifest `capturing`. Return the slice.

- [ ] **Step 3: Failing tests for `Finalize`**

```go
func TestFinalize_DryRunLeavesTargetsUntouched(t *testing.T) { /* candidates built, diffs in Report, NO real target/dataset written, manifest dry_run */ }
func TestFinalize_PublishOnlyChanged(t *testing.T) { /* per-stage file + memory.md written durably; canonical datasets published; unchanged (identical bytes) not rewritten; manifest ends "published" (no commit) with per-target Published */ }
func TestFinalize_ProjectAggregateOnlyEligibleDatasets(t *testing.T) { /* a stray <run>/ghost/reflect_dataset.yaml excluded */ }
func TestFinalize_SharedReflectFileChainsThroughCandidate(t *testing.T) { /* stage2 SeedFrom == stage1 candidate */ }
func TestFinalize_CtxCancelledBeforePromotionNoWrite(t *testing.T) { /* cancel after distill, before publish -> ctx err, nothing published, manifest cancelled */ }
func TestFinalize_SecondWriteFailsLeavesPromoting(t *testing.T) { /* req.writer fails on the 2nd target; first target Published, manifest stays promoting, error returned */ }
func TestFinalize_SymlinkedTargetParentRejected(t *testing.T) { /* validateTargetUnderMemoryDir rejects a symlinked parent before any write */ }
```

- [ ] **Step 4: Implement `Finalize`**

0. **Containment pre-check under lock (review #5):** for every final target (each `memory.StageFile(MemoryDir, file)` and `memory.ProjectFile(MemoryDir)`), call `validateTargetUnderMemoryDir(MemoryDir, final)` (below) BEFORE seeding — the memory lock only serializes AFM writers, not an external process that may have swapped a parent to a symlink after Task 3's lexical check. `SeedFrom` for the first stage in a group is the (validated) final target.
1. Manifest → `distilling`.
2. **Per-stage distill** (group eligible write-reflect stages by resolved `reflect.file`, declaration order): first stage in a group `SeedFrom = memory.StageFile(MemoryDir, file)`, each subsequent `SeedFrom = prior.CandidatePath`. Collect pending `TargetResult{Label, FinalPath: memory.StageFile(MemoryDir,file), CandidatePath, NoHigh}`.
3. **Project distill** (only if `Memory.CanWriteProject()` and ≥1 non-empty eligible dataset — filter empties, review #11): `DistillTarget{Name:"flow-memory", Datasets: eligible, StagingDir: <WorkDir>/flow, SeedFrom: memory.ProjectFile(MemoryDir)}` → pending `TargetResult{FinalPath: ProjectFile, ...}`.
4. **Compute change/diff**: for each pending target, read candidate + current final (missing = changed); `OldSHA256`/`NewSHA256`; `Changed`; `Diff = udiff.Unified("old","new",old,cand)` when changed. Also mark which generated `DatasetResult`s are `Changed` (candidate vs canonical bytes) for dataset promotion bookkeeping.
5. **`ctx.Err()` check** (review #15) — return `ctx.Err()` + manifest `cancelled` if cancelled here. This is the last interruption point; once promotion starts it runs to a consistent state.
6. If `DryRun`: manifest `dry_run`; return Report (no writes, no dataset publish).
7. **Promotion** (review #6/#8): manifest `promoting` listing pending memory targets AND generated datasets. `w := req.writer; if w == nil { w = memory.AtomicWrite }`. For each changed target: re-run `validateTargetUnderMemoryDir` (a final guard immediately before write), back up old bytes into `<WorkDir>/backup/<sha>.bak`, `w(final, candidateBytes)`, set `TargetResult.Published=true`, `WriteManifest` after each. Publish generated canonical datasets the same way (`w(CanonicalPath, datasetBytes)`, set `DatasetResult.Published=true`, per-file manifest update). On a mid-promotion error: leave manifest `promoting` with accurate `Published` flags, return the error (no rollback). After all files done: set manifest `awaiting_commit` (commit pending) or `published` (no commit requested) — **NOT `completed`; the CLI writes the final status after the commit decision (review #2).**

Helper (put in `operation.go` or a `paths.go` in the package):
```go
// validateTargetUnderMemoryDir rejects a final target whose real location escapes
// memoryDir or whose deepest-existing path component is a symlink (review #5).
func validateTargetUnderMemoryDir(memoryDir, target string) error {
    md, err := filepath.EvalSymlinks(memoryDir) // memoryDir must exist by publish time; seed created it
    if err != nil { return fmt.Errorf("resolve memory dir: %w", err) }
    // reject a symlinked final file itself
    if fi, err := os.Lstat(target); err == nil && fi.Mode()&os.ModeSymlink != 0 {
        return fmt.Errorf("target %s is a symlink", target)
    }
    parent := canonicalizeExistingAncestor(filepath.Dir(target)) // reuse the Task 9 helper
    rel, err := filepath.Rel(md, parent)
    if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
        return fmt.Errorf("target %s escapes memory dir %s", target, md)
    }
    return nil
}
```
Add tests: symlinked parent dir; symlinked final file; a parent swapped to a symlink between the step-0 check and promotion (the step-7 re-check catches it).
8. Return `Report{Targets, Datasets, WorkDir}`.

Do NOT interrupt an in-progress promotion on ctx cancel (review #15: once promotion starts, drive the short rename loop to a consistent state); the ctx check is strictly BEFORE step 7.

- [ ] **Step 5: Run + race** — `go test -race ./pkg/memorypipeline -v` → PASS.

- [ ] **Step 6: Lint + commit**
```bash
make lint-ci
git add pkg/memorypipeline/rebuild.go pkg/memorypipeline/rebuild_test.go pkg/memorypipeline/operation.go
git commit -m "feat(memory-rebuild): двухфазный Rebuilder (Capture без lock, Finalize под lock) и staged promotion"
```

---

## Task 9: Shared memory lock (busy-aware, ancestor-symlink-canonical) — CLI-owned

Fixes review **#8** (busy vs error, ctx checks) and **#14** (symlink canonicalization of the deepest existing ancestor for a not-yet-created memory dir).

**Files:**
- Create: `pkg/memorypipeline/lock.go`, `lock_test.go`

**Interfaces:**
- Produces: `MemoryLockPath(memoryDir string) (string, error)`, `AcquireMemoryLock(ctx, memoryDir string) (*MemoryLock, error)`, `(*MemoryLock).Close() error`. The lock is acquired by the CALLER (CLI Task 11 / orchestrator Task 10), NOT inside `Finalize`.

- [ ] **Step 1: Failing tests**

Stable path for trailing slash; symlink alias → same key even when the memory dir does not exist yet but an existing ancestor is a symlink; `AcquireMemoryLock` serializes (second acquire blocks until ctx cancel → `context.DeadlineExceeded`); an already-cancelled ctx never acquires; a non-busy `TryLock` error propagates immediately (inject via an unwritable locks root — `t.Setenv("HOME", ...)` to sandbox). 

- [ ] **Step 2: Implement `lock.go`**

```go
func MemoryLockPath(memoryDir string) (string, error) {
    abs, err := filepath.Abs(memoryDir)
    if err != nil { return "", err }
    abs = filepath.Clean(abs)
    canon := canonicalizeExistingAncestor(abs) // resolve symlinks of the deepest existing ancestor, re-append the missing suffix
    sum := sha256.Sum256([]byte(canon))
    home, err := os.UserHomeDir(); if err != nil { return "", err }
    locks := filepath.Join(home, ".afm", "locks")
    if err := os.MkdirAll(locks, 0755); err != nil { return "", err }
    return filepath.Join(locks, "memory-"+hex.EncodeToString(sum[:])+".lock"), nil
}

// canonicalizeExistingAncestor walks up until an existing path, EvalSymlinks it,
// then re-joins the non-existent trailing components (review #14).
func canonicalizeExistingAncestor(abs string) string {
    missing := ""
    cur := abs
    for {
        if resolved, err := filepath.EvalSymlinks(cur); err == nil {
            return filepath.Join(resolved, missing)
        }
        parent := filepath.Dir(cur)
        if parent == cur { return abs } // reached root without resolving
        missing = filepath.Join(filepath.Base(cur), missing)
        cur = parent
    }
}

func AcquireMemoryLock(ctx context.Context, memoryDir string) (*MemoryLock, error) {
    if err := ctx.Err(); err != nil { return nil, err }
    path, err := MemoryLockPath(memoryDir); if err != nil { return nil, err }
    l, err := progress.NewLock(path); if err != nil { return nil, err }
    ticker := time.NewTicker(100 * time.Millisecond); defer ticker.Stop()
    for {
        err := l.TryLock()
        if err == nil { return &MemoryLock{l: l}, nil }
        if !errors.Is(err, progress.ErrLockBusy) { return nil, err } // real error, not contention (review #8)
        select {
        case <-ctx.Done(): return nil, ctx.Err()
        case <-ticker.C:
        }
    }
}
func (m *MemoryLock) Close() error { if m.l != nil { m.l.Unlock(); m.l = nil }; return nil }
```

- [ ] **Step 3: Run + race** — `go test -race ./pkg/memorypipeline -run 'MemoryLock|Acquire' -v` → PASS.

- [ ] **Step 4: Lint + commit**
```bash
make lint-ci
git add pkg/memorypipeline/lock.go pkg/memorypipeline/lock_test.go
git commit -m "feat(memory-rebuild): общий memory-lock (busy-aware, канонизация symlink-ancestor)"
```

---

## Task 10: Rewire the live orchestrator onto the two-phase engine

Fixes review **#13** (eligible-only project datasets, unique live staging, NoHigh/invalid/ghost integration tests, after-hook ordering).

**Files:**
- Modify: `pkg/orchestrator/reflection.go`, `orchestrator.go`, `hooks.go`, `reflection_test.go`, `memory_integration_test.go`, `integration_hooks_test.go`

- [ ] **Step 1: Baseline** — `go test ./pkg/orchestrator -run 'Memory|Reflect|Hook' -v` → PASS (green before rewire).

- [ ] **Step 2: `maybeRunReflection` → `Pipeline.CaptureStage` (single stage)**

Keep the `SpawnDetached`/`pendingReflections`/guards envelope. Inside, call `o.mem.CaptureStage(ctx, stage, stageDir, canonical, stageDir, false)` where `canonical = memory.StageFile(o.opts.RunDir, filepath.Join(stage.ID, "reflect_dataset.yaml"))` — the live path writes the canonical dataset in place (review #3: `CaptureAll` always stages, so the live single-stage path uses the primitive directly). Error → `reflectFailed`.

- [ ] **Step 3: After-hook/reflection ordering — concrete callback in `hooks.go` (review #6)**

`maybeRunAfterHook` currently spawns a tracked goroutine and returns nothing; a stage without `ScriptAfter` spawns nothing. Reordering the two calls in `completeStage` is therefore NOT enough — `after.log` may not exist when reflection captures. Add a continuation-carrying variant in `hooks.go`:

```go
// maybeRunAfterHookThen runs script_after (if any) and then invokes cont —
// inside the SAME tracked goroutine, AFTER runAfterHook returns — so a
// consumer (reflection) sees after.log. With no ScriptAfter, cont runs inline
// (unchanged fast path). cont must be cheap/non-blocking (it only spawns a
// detached reflection agent).
func (o *Orchestrator) maybeRunAfterHookThen(ctx context.Context, stageID string, cont func(context.Context)) {
    stage := o.graph.Stage(stageID)
    if stage == nil || stage.ScriptAfter == "" { cont(ctx); return } // no hook: unchanged behavior
    o.pendingAfterHooks.Add(1)
    o.concurrency.SpawnAgent(ctx, *stage, func(ctx context.Context, s flow.Stage) {
        defer func() { o.pendingAfterHooks.Add(-1); o.concurrency.WakeEventLoop() }()
        o.runAfterHook(ctx, s)
        if ctx.Err() == nil { cont(ctx) } // skip reflection on shutdown; retry/skip decisions already resolved in runAfterHook
    })
}
```
In `completeStage`, replace the two separate calls:
```go
o.maybeRunAfterHookThen(ctx, stageID, func(c context.Context) { o.maybeRunReflection(c, stageID) })
o.failBlockedStages(); o.startPlanningForUnblocked(ctx); o.startReadyStages(ctx); o.tryActivatePrePlanned(ctx)
```
The unblock cascade still runs immediately (only reflection is deferred behind the hook). Keep the original `maybeRunAfterHook` if other callers use it (`approveStage`, `control_api.go:135`) — those do NOT chain reflection, so leave them on `maybeRunAfterHook`. Tests: a stage WITH an after-hook → reflect inventory includes `after.log`; a stage WITHOUT → reflection still runs (inline); shutdown during the hook → no reflection spawned; `pendingAfterHooks` bookkeeping unchanged.

- [ ] **Step 4: `runEndOfRunMemory` → `Finalize` under the memory lock**

Acquire `memorypipeline.AcquireMemoryLock(ctx, MemoryDir)` (best-effort: on error `reflectFailed` + return). Build `FinalizeRequest` with `Datasets` = **eligible non-script write-reflect stages only** (drop the `Glob(<run>/*/reflect_dataset.yaml)` — build the list explicitly, review #13), `DryRun:false`, a **unique live staging dir** per finalize (e.g. `<RunDir>/.memory-finalize/<attempt>`; NOT a reused stage dir, so `requireFreshFile` can't accept stale output), `ManifestPath:""` (live path writes no manifest). Publish happens inside `Finalize`. Keep `reflectMu` around this (in-process serialization) AND the interprocess lock (cross-process). Keep `finalReflectDone` and the `if o.opts.Memory.Commit { memory.Commit(...) }` afterwards (under the same held lock — move the commit before `lock.Close()`).

- [ ] **Step 5: Integration tests (review #13)**

Add/keep: normal flow still auto-builds per-stage files AND `memory.md`; `memory.mode:r` builds per-stage files but no `memory.md`; a stage with empty dataset → no-op keeps prior rules; two stages sharing `reflect.file` with first NoHigh → prior rules preserved; a stale ghost `reflect_dataset.yaml` for a removed stage is NOT included in the project aggregate; `commit` gate unchanged.

- [ ] **Step 6: Run + race** — `go test ./pkg/orchestrator/... -v` then `go test -race ./pkg/orchestrator/...` → PASS.

- [ ] **Step 7: Lint + commit**
```bash
make lint-ci
git add pkg/orchestrator/reflection.go pkg/orchestrator/orchestrator.go pkg/orchestrator/reflection_test.go pkg/orchestrator/memory_integration_test.go
git commit -m "refactor(memory): live-путь на двухфазный engine, eligible-only datasets, memory-lock, порядок after-hook/reflect"
```

---

## Task 11: Full CLI `RunE` — preflight, lock ordering, execution, manifest finalization

Fixes review **#12** (compile-correct calls), **#2** (CLI owns the lock window), **#5** (manifest finalization + real diffs), **#6** (warn on stale `promoting`), **#15** (signal cancellation).

**Files:**
- Modify: `cmd/afm/memory_rebuild.go`, `memory_rebuild_test.go`, `main.go`

**Interfaces:**
- Consumes: `config.LoadFrom`, `flow.ParseFile`, `resolveFlowPath`, `state.FindLatestCompletedRunDir`/`resolveExplicitRunDir`/`TryLockRun`/`LoadRunState`, `loadPrompts`, `executor.WrapperDirFor`, `memorypipeline.New`/`CaptureAll`/`AcquireMemoryLock`/`Finalize`/`ScanUnfinishedPromotions`, and the root/memory resolvers (Task 13 — until then inline, matching run.go:207-225).
- Produces: real `rebuildHandler`; `hasWritableTarget(f)` (review #12: a write-reflect stage OR project-write WITH ≥1 eligible write-reflect stage — a bare `memory.mode:w` with no reflect stage is NOT writable); `checkStageSetMatches(rs, f)` (sorted missing/extra lists); the preflight summary comes from the pipeline's plan, not a duplicated validation.

- [ ] **Step 1: Failing integration tests (stubbed runner)**

Expose a test seam `var newRebuildPipeline = func(p memorypipeline.Prompts, a memorypipeline.AgentConfig) *memorypipeline.Pipeline { return memorypipeline.New(p, a) }`; tests replace it with `memorypipeline.New(p, a, memorypipeline.WithRunner(stub))`. Cover: missing memory config; no-writable-target (bare `memory.mode:w`, no reflect stage); stage-set mismatch (sorted lists); active run → `ErrRunLocked` message; **dry-run leaves `events.jsonl`/`state.json` byte-for-byte unchanged and writes no target**; persistent `--dir`; a stale `promoting` manifest from a prior attempt prints a warning.

- [ ] **Step 2: Run, verify fail; implement `rebuildHandler`**

```go
rebuildHandler = func(ctx context.Context, o rebuildOptions) error {
    ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM); defer stop()

    home, _ := os.UserHomeDir()
    cfg, err := config.LoadFrom(filepath.Join(home, config.AfmDir), fmDir()); if err != nil { return err }

    var flowArgs []string
    if o.FlowArg != "" { flowArgs = []string{o.FlowArg} } // review #12: nil -> resolveFlowPath scans
    flowPath, err := resolveFlowPath(flowArgs); if err != nil { return err }
    f, err := flow.ParseFile(flowPath); if err != nil { return err }
    if !f.MemoryEnabled() { return fmt.Errorf("flow %q has no memory.path — nothing to build", f.Name) }
    if !hasWritableTarget(f) { return fmt.Errorf("no writable memory target (need a stage with reflect.mode w|rw)") }

    stageIDs := stageIDsOf(f)
    var runDir string
    if o.RunID != "" { runDir, err = resolveExplicitRunDir(runsDir(), f.Name, o.RunID) } else {
        runDir, err = state.FindLatestCompletedRunDir(runsDir(), f.Name, stageIDs)
    }
    if err != nil { return err }

    runLock, err := state.TryLockRun(runDir)
    if err != nil {
        if errors.Is(err, state.ErrRunLocked) { return fmt.Errorf("run %s is active; stop `afm run` or wait", filepath.Base(runDir)) }
        return err
    }
    defer runLock.Close()

    rs, err := state.LoadRunState(runDir); if err != nil { return err }
    // review #1: explicit --run is NOT filtered by the completed-run resolver, so
    // check completeness AND the exact stage set independently — they are orthogonal.
    if !rs.AllDone() { return fmt.Errorf("run %s is not completed (some stages are not done)", filepath.Base(runDir)) }
    if err := checkStageSetMatchesExact(rs, stageIDs); err != nil { return err }

    agentRoot, err := resolveAgentRoot(rootDir, f); if err != nil { return err }
    memDir, err := resolveMemoryDir(rootDir, agentRoot, f); if err != nil { return err }

    // review #7: fail-fast commit preflight BEFORE the expensive capture, when commit is on.
    commit := effectiveCommit(o, f)
    if commit { if err := commitPreflight(memDir); err != nil { return err } }

    if warns := memorypipeline.ScanUnfinishedPromotions(runDir); len(warns) > 0 {
        fmt.Printf("warning: previous rebuild left an unfinished promotion in %v; re-running rebuilds cleanly\n", warns)
    }

    prompts, err := loadPrompts(cfg.PromptsDir); if err != nil { return err } // review #12: two-return
    agentCfg := memorypipeline.AgentConfig{
        Command: cfg.Client.Command, ExtraArgs: cfg.Client.ExtraArgs,
        WrapperDir: executor.WrapperDirFor(cfg.Client.Command, "", nil), // generated-wrapper dir wired in Task 13
        RootDir: agentRoot, IdleTimeout: cfg.Executor.IdleTimeout, Debug: debugEnabled,
    }

    work, err := memorypipeline.NewUniqueAttemptDir(filepath.Join(runDir, "memory-rebuild"), newAttemptID) // review #9
    if err != nil { return err }
    agentCfg.RunDir = work // review #4: debug.log stays inside the attempt workspace
    pipe := newRebuildPipeline(
        memorypipeline.Prompts{Reflect: prompts.Reflect, Aggregate: prompts.Aggregate, Prioritize: prompts.Prioritize, Update: prompts.Update},
        agentCfg)

    fmt.Printf("using current flow definition and current prompts for historical run %s\n", filepath.Base(runDir))
    manifestPath := filepath.Join(work, "manifest.json")
    meta := buildOperationMeta(cfg, f, flowPath, runDir, agentRoot, memDir, prompts, commit) // review #5: computes FlowSHA256, PromptSHA256, PromptsDir

    // review #2: CLI creates the manifest (status "running", full meta) and a central
    // defer flips any still-non-terminal manifest to failed/cancelled on early return.
    if err := memorypipeline.WriteManifest(manifestPath, memorypipeline.NewRunningManifest(meta)); err != nil { return err }
    var finalErr error
    defer memorypipeline.FinalizeManifestOnExit(manifestPath, &finalErr, ctx) // "running/capturing/distilling/promoting" -> failed; ctx cancelled -> cancelled

    // Phase 1: capture (no memory lock)
    datasets, err := pipe.CaptureAll(ctx, memorypipeline.CaptureRequest{
        RunDir: runDir, WorkDir: work, Stages: f.Stages, ForceReflect: o.ForceReflect, ManifestPath: manifestPath})
    if err != nil { finalErr = err; return fmt.Errorf("capture failed (see %s): %w", work, err) }

    // Phase 2: memory lock -> re-preflight commit -> finalize -> commit (all under lock)
    memLock, err := memorypipeline.AcquireMemoryLock(ctx, memDir); if err != nil { finalErr = err; return err }
    defer memLock.Close()
    if commit { if err := commitPreflight(memDir); err != nil { finalErr = err; return err } } // re-check UNDER lock (review #7)

    report, err := pipe.Finalize(ctx, memorypipeline.FinalizeRequest{
        Meta: meta, RunDir: runDir, WorkDir: work, MemoryDir: memDir, Stages: f.Stages, Memory: f.Memory,
        Datasets: datasets, DryRun: o.DryRun, ManifestPath: manifestPath})
    if err != nil { finalErr = err; return fmt.Errorf("rebuild failed (see %s): %w", work, err) }
    printReport(report, o.DryRun)

    if o.DryRun { return nil } // manifest already "dry_run" from Finalize; defer sees terminal, no-op
    if commit {
        if err := maybeCommit(memDir, report, manifestPath); err != nil { finalErr = err; return err } // patches manifest CommitError + failed
    }
    return memorypipeline.MarkManifestCompleted(manifestPath) // review #2: CLI writes the FINAL "completed"
}
```

Implement helpers: `hasWritableTarget`, `stageIDsOf`, `checkStageSetMatchesExact` (sorted missing/extra), `buildOperationMeta` (computes `FlowSHA256` from the flow bytes, `PromptSHA256` from the four templates, and `PromptsDir = cfg.PromptsDir` — hashing/provenance lives in the CLI, feeding `OperationMeta`; review #2/#5), `newAttemptID` (`func() string` → `<YYYYMMDD-HHMMSS>-<2 hex>`; the timestamp/rand come from the CLI, not the pipeline), `printReport` (counts + `report.Targets[].Diff` for dry-run/changed; **never echoes agent stdout/prompts/dialog** — review §9). Manifest helpers `NewRunningManifest`/`FinalizeManifestOnExit`/`MarkManifestCompleted` live in `pkg/memorypipeline/manifest.go` (add to Task 6's file set). `signal.NotifyContext` cancels before any promotion; `Finalize` checks `ctx.Err()` before publish (Task 8).

- [ ] **Step 3: Run tests + the no-FSM-mutation assertion** — `go test ./cmd/afm -run TestRebuild -v` → PASS. Add the byte-for-byte `events.jsonl`/`state.json`/notices assertion.

- [ ] **Step 4: Lint + commit**
```bash
make lint-ci
git add cmd/afm/memory_rebuild.go cmd/afm/memory_rebuild_test.go cmd/afm/main.go
git commit -m "feat(memory-rebuild): полный RunE — двухфазный запуск под run+memory локами, strict-вывод"
```

---

## Task 12: Git commit — changed-paths, exit-1-only diff, preflight from existing ancestor, manifest finalization

Fixes review **#10.minor** (exit-1-only), **#14** (preflight from nearest existing ancestor, don't create memory dir), **#5** (commit outcome in manifest).

**Files:**
- Modify: `pkg/memory/commit.go`, `commit_test.go`, `cmd/afm/memory_rebuild.go`, `memory_rebuild_test.go`

**Interfaces:**
- Produces: `memory.CommitPaths(dir, message string, paths []string) (committed bool, sha string, err error)` — dedupes/normalizes paths, treats only `git diff --cached --quiet` exit code 1 as "has diff" (128/launch errors propagate), returns the commit SHA.
- Produces (cmd/afm): `effectiveCommit(o, f)`, `commitPreflight(memDir)`, `maybeCommit(memDir, report, manifestPath)`.

- [ ] **Step 1: Failing tests for `CommitPaths`** — only-given-paths committed; unrelated file untracked; nothing-to-commit → `(false,"",nil)`; a launch/128 error propagates (not treated as diff). Use `exec.ExitError` code inspection.

- [ ] **Step 2: Implement `CommitPaths`** (exit-code-aware):
```go
func CommitPaths(dir, message string, paths []string) (bool, string, error) {
    paths = dedupeClean(paths)
    if len(paths) == 0 { return false, "", nil }
    if out, err := run("git", "-C", dir, "add", "--", paths...); err != nil { return false, "", fmt.Errorf("git add: %v: %s", err, out) }
    err := exec.Command("git", append([]string{"-C", dir, "diff", "--cached", "--quiet", "--"}, paths...)...).Run()
    if err == nil { return false, "", nil } // exit 0 -> nothing staged
    var ee *exec.ExitError
    if !errors.As(err, &ee) || ee.ExitCode() != 1 { return false, "", fmt.Errorf("git diff: %w", err) } // 128/other -> real error
    if out, err := run("git", "-C", dir, "commit", "-m", message, "--", paths...); err != nil { return false, "", fmt.Errorf("git commit: %v: %s", err, out) }
    sha, err := run("git", "-C", dir, "rev-parse", "HEAD")
    if err != nil { return true, "", fmt.Errorf("commit created but rev-parse failed: %w", err) } // review #12: strict CLI surfaces it
    return true, strings.TrimSpace(sha), nil
}
```

- [ ] **Step 3: Failing tests for cmd/afm commit glue** — `effectiveCommit` matrix (`--commit`/`--no-commit`/config/dry-run-false); `commitPreflight` rejects pre-staged memory changes and works when the memory dir does NOT exist yet (finds repo root from the nearest existing ancestor, review #14); `maybeCommit` on `CommitPaths` error returns `"memory files updated but commit failed: ..."` and records `CommitError` in the manifest; success records `CommitCreated`/`CommitSHA` and finalizes manifest.

- [ ] **Step 4: Implement glue.** `commitPreflight(memDir)`: walk up to the nearest existing ancestor, `git -C <ancestor> rev-parse --show-toplevel`; if inside a worktree, `git ... diff --cached --quiet -- <memDir>` must be clean. `maybeCommit` collects `report.Targets` where `Changed && Published`, calls `CommitPaths` with `chore(memory): rebuild from <run-id>`, then patches the manifest (`CommitCreated`/`CommitSHA`/`CommitError`, final status).

- [ ] **Step 5: Run** — `go test ./pkg/memory ./cmd/afm -run 'Commit|Rebuild' -v` → PASS.

- [ ] **Step 6: Lint + commit**
```bash
make lint-ci
git add pkg/memory/commit.go pkg/memory/commit_test.go cmd/afm/memory_rebuild.go cmd/afm/memory_rebuild_test.go
git commit -m "feat(memory-rebuild): CommitPaths (exit-1-only), preflight от существующего ancestor, финализация manifest"
```

---

## Task 13: Shared root/memory helpers + Docker for rebuild

Fixes review **#14** (absolute-root normalization vs behavior-preserving extraction; resolved absolute flow path for re-exec; writable-mount contract for external `memory.path`) and **#12** (wrapper wiring).

**Files:**
- Create: `cmd/afm/agent_environment.go`, `agent_environment_test.go`
- Modify: `cmd/afm/run.go`, `cmd/afm/memory_rebuild.go`, `pkg/docker/launcher.go` (+ tests)

**Interfaces:**
- Produces: `resolveAgentRoot(rootDir string, f *flow.Flow) (string, error)` — behavior-preserving for `afm run`; PLUS `absoluteAgentRoot(rootDir string, f *flow.Flow) (string, error)` that returns an **absolute** root (empty `root_dir` → absolute CWD) for rebuild/manifest/lock stability (review #14: one helper can't satisfy both contracts). `resolveMemoryDir(afmRoot, agentRoot string, f *flow.Flow) (string, error)`.
- Produces: nil-flow-tolerant `docker.UsedRecipes/ScanCommands/UsesCodex` (or explicit command-list variants) taking only the global memory-agent command.

- [ ] **Step 1: Behavior-preserving extraction + regression** — extract run.go:207-225 into `resolveAgentRoot`/`resolveMemoryDir`; test that representative flows resolve identically to before; `go test ./cmd/afm -v` unchanged for `afm run`.

- [ ] **Step 2: Absolute-root normalization for rebuild** — `absoluteAgentRoot` returns absolute CWD when `root_dir` is empty (rebuild needs a stable path for `Executor.Dir`, manifest, lock key). Test with `--dir` != CWD. Wire the rebuild handler to `absoluteAgentRoot`.

- [ ] **Step 3: Docker nil-flow helpers** — make `pkg/docker` recipe/command scanners accept `nil` flow (no deref) or add explicit command-list variants; unit-test with `nil` flow + `[cfg.Client.Command]` asserting only that command's mounts/recipes/secrets.

- [ ] **Step 4: Rebuild Docker re-exec** — mirror run.go:74-146 minus dashboard/browser/file-browser; command list `[cfg.Client.Command]`; **pass a resolved ABSOLUTE flow path** (review #14: a relative flow arg resolved against host CWD breaks under the container's `-w`) + absolute `--dir`; gate on `cfg.Docker.IsDockerEnabled() && os.Getenv("AFM_IN_DOCKER") != "1"`, re-exec BEFORE locks. Reuse `CheckClaudeDockerAuth`, auto-shim, generated wrappers, Codex OAuth mount, secret filtering. Preflight in-container reachability of runDir/agentRoot/memDir/flow/prompts_dir. **External absolute `memory.path` covered only by a `:ro` extra_mount is NOT writable** → fail fast with a clear "external memory.path needs a writable mount" error (review #14). Set `agentCfg.WrapperDir = executor.WrapperDirFor(cfg.Client.Command, generatedDir, generatedAgents)` and clean up the generated wrapper dir on exit.

- [ ] **Step 5: Run + race** — `go test ./cmd/afm ./pkg/docker ./pkg/flow -v` then `go test -race ./cmd/afm` → PASS.

- [ ] **Step 6: Lint + commit**
```bash
make lint-ci
git add cmd/afm/agent_environment.go cmd/afm/agent_environment_test.go cmd/afm/run.go cmd/afm/memory_rebuild.go pkg/docker/
git commit -m "feat(memory-rebuild): общие root/memory-хелперы, absolute-root для rebuild, Docker re-exec"
```

---

## Task 14: Documentation + final verification

**Files:** `README.md`, `AGENTS.md`, `docs/superpowers/specs/2026-09-09-memory-rebuild-design.md`

- [ ] **Step 0: Reconcile the design spec with the implemented selection semantics (review #10)** — the spec §5 describes "pick the latest completed run, then hard-error on stage-set mismatch." The implemented resolver instead **skips** a newer completed run whose stage set differs and selects the newest run whose set matches exactly. Update spec §5 to state this, and note the consequence: **the command may silently analyze a substantially older run** if the newest completed run has a different topology. (This trades "select-then-mismatch-error" for "select-the-matching-one", chosen so a topology change doesn't block backfill.)
- [ ] **Step 1: README** — `afm memory rebuild` section: flag examples/table, effective-commit rule, dry-run/audit-workspace, the two locks (run + shared memory) + no-FSM-mutation guarantee, dataset reuse/`--force-reflect`, "current YAML + current prompts analyze historical logs", exact-stage-set + completed-only rules (**including the "may pick an older matching run" note from Step 0**), and the **breaking path-safety validation** (`afm run` now rejects unsafe stage ids / `reflect.file` / `reflect.file: memory.md`).
- [ ] **Step 2: AGENTS.md** — subsection describing `pkg/memorypipeline` (two-phase Capture/Finalize engine shared by live + offline), the attempt-workspace/manifest/staged-promotion model, the shared memory lock (all writers, `~/.afm/locks/memory-<sha256>.lock`, run→memory order, ancestor-symlink canonicalization), CLI-owned lock window (capture → lock → re-preflight → finalize → commit), and strict-vs-best-effort asymmetry.
- [ ] **Step 3: Final verification (spec §16)**
```bash
go test ./pkg/state ./pkg/memory ./pkg/memorypipeline ./cmd/afm ./pkg/progress ./pkg/executor
go test -race ./pkg/orchestrator/... ./pkg/memorypipeline/... ./cmd/afm
go test ./... -race
make lint-ci
go run ./cmd/afm memory rebuild --help
```
Expected: all green; `--help` shows the command.
- [ ] **Step 4: Commit**
```bash
git add README.md AGENTS.md
git commit -m "docs(memory-rebuild): README и AGENTS для команды memory rebuild"
```

---

## Manual smoke test (after Task 14; not a code task)

Per spec §15: (1) small flow without memory; (2) add `memory.path`+`memory.mode:rw`+`reflect{file,mode:rw}`; (3) `--dry-run` shows diff, no target files; (4) normal rebuild writes canonical datasets/per-stage files/`memory.md`; (5) re-run reuses dataset, empty diff, no duplicate patterns; (6) `--force-reflect` new attempt, replace only on full success; (7) concurrent live `afm run` on the same memory dir → serialized; (8) rebuild an active run → immediate clear error; (9) repeat in Docker with default Claude + one generated recipe / Codex variant; (10) **NoHigh + shared reflect.file** case: first stage empty High, second non-empty — final file keeps original rules.

---

## Self-review notes (author checklist — done, review-incorporated)

- **Review findings → tasks:** #1→T1; #2→T8/T11; #3→T7; #4→T4/T11; #5→T6/T11/T12; #6→T6/T8/T11; #7→T1/T3; #8→T2/T9; #9→T5/T10; #10→T5/T6/T7/T12; #11→T7; #12→T1/T4/T11; #13→T10; #14→T9/T12/T13; #15→T8/T11; minor 1→T6; minor 2→T12; minor 3→T6/T7; minor 4→T6; minor 5→Global Constraints note; minor 6→whole re-ordering.
- **Spec coverage:** §2→T1/T11; §4→T4; §5→T1/T2; §6→T5; §7→T5/T8; §8→T6/T8; §9→T8/T11; §10→T9/T10/T11; §11→T12; §12→T13; §13→T3; §14 deferred (unbuilt). All mapped.
- **Placeholder scan:** no "TBD"/"handle errors"/"similar to"; concrete code on every changed/tricky piece; `<module>` is the deliberate module-path token.
- **Type consistency:** `AgentSpec`/`AgentRunner`/`Prompts`/`AgentConfig`/`Pipeline`/`Inventory`/`Dataset`/`DistillTarget`/`StepArtifacts`/`OperationMeta`/`StepError`/`DatasetResult`/`TargetResult`/`Report`/`Manifest`/`CaptureRequest`/`FinalizeRequest`/`AtomicWriter` are defined once (T4/T5/T6/T7/T8) and used consistently; `FindLatestCompletedRunDir(base,flowName,wantStages)`, `TryLockRun`, `progress.ErrLockBusy`, `AcquireMemoryLock`, `canonicalizeExistingAncestor` (shared by lock path + `validateTargetUnderMemoryDir`), `SourceInventory→Inventory`, `ValidateDataset`, `ValidateRules`, `requireFreshFile(path,maxBytes)`, `datasetsAllEmpty(...)(bool,error)`, `DistillTarget`, `CaptureStage`/`CaptureAll`, `Finalize`, `NewUniqueAttemptDir(base,gen func()string)`, `NewRunningManifest`/`FinalizeManifestOnExit`/`MarkManifestCompleted`, `CommitPaths(...)(bool,string,error)`, `executor.WrapperDirFor` match all call sites. `maxDatasetBytes = 10 MiB` (dataset) and `maxRulesBytes = 1 MiB` (Markdown) are distinct, deliberate bounds.
```
