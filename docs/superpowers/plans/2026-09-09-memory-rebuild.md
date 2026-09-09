# `afm memory rebuild` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an offline `afm memory rebuild` command that reruns the agent-memory pipeline over an already-completed run, writing per-stage memory and the project `memory.md` without creating a new run or touching the historical FSM/event log.

**Architecture:** Extract the whole memory engine out of `pkg/orchestrator` into a new reusable `pkg/memorypipeline` package; rewire the live orchestrator to call it. Add read-only run selection/locking to `pkg/state`, a shared interprocess memory-dir lock, a transactional attempt-workspace with a manifest and durable per-file publish, and a strict CLI on top. Docker re-exec, root_dir/prompt resolution, and git-commit are shared with `afm run` via extracted helpers.

**Tech Stack:** Go (stdlib `testing`), cobra CLI, `gopkg.in/yaml.v3`, `syscall.Flock` (via `pkg/progress`), `os/exec` git, existing `pkg/executor`.

**Spec:** `docs/superpowers/specs/2026-09-09-memory-rebuild-design.md`

## Global Constraints

- **Do not change the Go version in `go.mod`.** (user rule)
- **All git commit messages in Russian.** No `Co-Authored-By` trailers. (user rule)
- **Lint must be clean after every task:** `make lint-ci` → `0 issues` (runs `golangci-lint` for host AND `GOOS=linux`). The commit step in this repo runs a pre-commit hook doing `make lint` + `make build`; a dirty lint blocks the commit.
- **No deprecated constructs;** prefer removing/simplifying over adding (user rule).
- **Human-readable Go** matching surrounding style; comments in the file's existing language (Russian in `pkg/orchestrator`/`pkg/flow`, English in `pkg/memory`).
- **The explicit `memory rebuild` command is STRICT** (returns non-zero on any failure). The **live** end-of-run/capture path stays **best-effort** (errors → `reflect_failed` notices, never touch the FSM). Every task must preserve that asymmetry.
- **Memory agents run fresh-context:** no `SessionID`, no `Resume`, no `StageDir`/`AFM_STAGE_DIR`; command comes from `config.client.command`/`client.extra_args` (never `stage.command`).
- **Never write outside the attempt workdir until all LLM steps succeed.** `--dry-run` never writes a target and never commits.
- **Phase file names come from `pkg/flow` constants** (`flow.PhaseLogFiles`, `flow.PhaseStreamLogs`, `flow.PhaseJSONL`, `flow.PhaseLogFile`) — never hardcode `"planning.log"` etc.

### Key existing signatures (verified, for reference in every task)

```go
// pkg/progress
func NewLock(path string) (*Lock, error) // does NOT acquire
func (l *Lock) TryLock() error           // LOCK_EX|LOCK_NB
func (l *Lock) Lock() error              // LOCK_EX (blocking)
func (l *Lock) Unlock()                  // LOCK_UN + close

// pkg/state
func FindLatestRunDir(base, flowName string) (string, error)
func FindLatestRunForStage(base, stageID string) (string, []string, error)
func LoadRunState(runDir string) (RunState, error) // reads events.jsonl, no flock
func (rs *RunState) AllDone() bool
var ErrRunLocked  error
var ErrCorruptLog error

// pkg/memory
func ProjectFile(dir string) string          // <dir>/memory.md
func StageFile(dir, rel string) string        // filepath.Join(dir, rel)
func AtomicWrite(path string, data []byte) error // temp+rename, NO fsync (to be hardened)
func SelectHigh(prioritized string) string
func Commit(dir, message string) (committed bool, err error)

// pkg/executor
type Config struct { Command string; ExtraArgs []string; IdleTimeout time.Duration;
    WrapperDir, Dir, RunDir, StageID, SessionID, StageDir string; Resume, Debug bool; /* ... */ }
func ResolveArgs(extra []string) []string
func New(cfg Config) *Executor
func (e *Executor) RunAgent(ctx context.Context, agentType, stageName, prompt, logFile string) error

// pkg/flow
type MemoryConfig struct { Path, Mode string; MemoryUse bool; MaxRules int; Commit bool }
func (m MemoryConfig) CanReadProject() bool
func (m MemoryConfig) CanWriteProject() bool
type Reflect struct { File, Mode string }
func (r *Reflect) CanRead() bool
func (r *Reflect) CanWrite() bool
func (s *Stage) IsScript() bool
func (f *Flow) MemoryEnabled() bool // Memory.Path != ""
func PhaseLogFiles(p Phase) []string
func PhaseStreamLogs(p Phase) []string
func Phases() []Phase
```

---

## Task ordering rationale

Tasks are ordered so each produces an independently-testable deliverable and later tasks consume earlier ones. Tasks 1–2 add read-only state helpers (no dependency on the new package). Task 3 extracts the engine (large, mechanical, must keep live tests green). Tasks 4–6 build the offline pipeline pieces on top of the extracted engine. Task 7 adds the shared lock. Task 8 rewires the live orchestrator onto the new engine + lock. Tasks 9–11 build the CLI, commit-paths, and Docker wiring. Task 12 is docs.

---

## Task 1: Read-only completed-run resolver + safe explicit `--run`

**Files:**
- Modify: `pkg/state/state.go` (add `FindLatestCompletedRunDir`)
- Create: `pkg/state/run_lookup_completed_test.go`
- Create: `cmd/afm/memory.go` (parent `memory` cobra command)
- Create: `cmd/afm/memory_rebuild.go` (child command skeleton + safe explicit-run resolver)
- Create: `cmd/afm/memory_rebuild_test.go`
- Modify: `cmd/afm/main.go` (register `newMemoryCmd()`)

**Interfaces:**
- Produces: `state.FindLatestCompletedRunDir(base, flowName string) (string, error)`
- Produces: `newMemoryCmd() *cobra.Command`, `newMemoryRebuildCmd() *cobra.Command`
- Produces: `resolveExplicitRunDir(runsDir, flowName, id string) (string, error)` (in `cmd/afm`)
- Produces: seam `var rebuildHandler func(ctx context.Context, opts rebuildOptions) error` where `rebuildOptions` carries parsed flags (see below) — real handler filled in Task 9.

- [ ] **Step 1: Write failing test for `FindLatestCompletedRunDir`**

Create `pkg/state/run_lookup_completed_test.go`. Reuse the existing test helper pattern in `pkg/state` for writing an `events.jsonl` that replays to given stage statuses (grep the package for an existing helper such as `writeEvents`/`newTestRun`; if none, build a run dir by opening a `Store`, applying transitions, and `Close`). Cover:

```go
func TestFindLatestCompletedRunDir_PicksLatestAllDone(t *testing.T) {
    base := t.TempDir()
    // older completed run
    mkCompletedRun(t, base, "flow-20260101-000000-aaaa")
    // newer INCOMPLETE run (one stage still running)
    mkIncompleteRun(t, base, "flow-20260201-000000-bbbb")
    // newest completed run
    mkCompletedRun(t, base, "flow-20260301-000000-cccc")

    got, err := FindLatestCompletedRunDir(base, "flow")
    if err != nil { t.Fatal(err) }
    if filepath.Base(got) != "flow-20260301-000000-cccc" {
        t.Fatalf("got %s", got)
    }
}

func TestFindLatestCompletedRunDir_SkipsNewerIncomplete(t *testing.T) {
    base := t.TempDir()
    mkCompletedRun(t, base, "flow-20260101-000000-aaaa")
    mkIncompleteRun(t, base, "flow-20260301-000000-cccc") // newest but not done
    got, err := FindLatestCompletedRunDir(base, "flow")
    if err != nil { t.Fatal(err) }
    if filepath.Base(got) != "flow-20260101-000000-aaaa" { t.Fatalf("got %s", got) }
}

func TestFindLatestCompletedRunDir_AnchoredPrefix(t *testing.T) {
    base := t.TempDir()
    mkCompletedRun(t, base, "foo-bar-20260101-000000-aaaa") // belongs to flow "foo-bar"
    _, err := FindLatestCompletedRunDir(base, "foo")        // must NOT match
    if !errors.Is(err, ErrNoRun) { t.Fatalf("want ErrNoRun, got %v", err) }
}

func TestFindLatestCompletedRunDir_CorruptNewestNotHidden(t *testing.T) {
    base := t.TempDir()
    mkCompletedRun(t, base, "flow-20260101-000000-aaaa")
    mkCorruptRun(t, base, "flow-20260301-000000-cccc") // corrupt middle line
    _, err := FindLatestCompletedRunDir(base, "flow")
    if !errors.Is(err, ErrCorruptLog) { t.Fatalf("want ErrCorruptLog, got %v", err) }
}

func TestFindLatestCompletedRunDir_NoCompletedRuns(t *testing.T) {
    base := t.TempDir()
    mkIncompleteRun(t, base, "flow-20260101-000000-aaaa")
    _, err := FindLatestCompletedRunDir(base, "flow")
    if !errors.Is(err, ErrNoRun) { t.Fatalf("want ErrNoRun, got %v", err) }
}
```

If `ErrNoRun` does not already exist in `pkg/state`, add `var ErrNoRun = errors.New("no matching run found")` in this task (check first with `grep -n "ErrNoRun\|no run" pkg/state/*.go`; `FindLatestRunDir` may already return a sentinel — reuse it).

- [ ] **Step 2: Run the test, verify it fails**

Run: `go test ./pkg/state -run TestFindLatestCompletedRunDir -v`
Expected: FAIL (undefined `FindLatestCompletedRunDir`).

- [ ] **Step 3: Implement `FindLatestCompletedRunDir`**

In `pkg/state/state.go`, next to `FindLatestRunForStage`. Model the anchoring on `FindLatestRunDir` (digit-after-prefix guard) and the corrupt-handling on `FindLatestRunForStage` (surface `ErrCorruptLog`, skip only missing/unreadable logs):

```go
// FindLatestCompletedRunDir returns the newest run dir for flowName whose
// every stage is StatusDone. Reads events.jsonl (authoritative), takes no
// flock. Incomplete runs are skipped so a fresh run started on top of the
// last completed one does not hide it. A corrupt matching log is surfaced,
// never silently skipped to an older run.
func FindLatestCompletedRunDir(base, flowName string) (string, error) {
    entries, err := os.ReadDir(base)
    if err != nil {
        if os.IsNotExist(err) { return "", ErrNoRun }
        return "", err
    }
    prefix := flowName + "-"
    var names []string
    for _, e := range entries {
        if !e.IsDir() { continue }
        n := e.Name()
        if len(n) <= len(prefix) || n[:len(prefix)] != prefix { continue }
        if c := n[len(prefix)]; c < '0' || c > '9' { continue } // anchor: digit after "<flow>-"
        names = append(names, n)
    }
    slices.SortFunc(names, func(a, b string) int { return strings.Compare(b, a) }) // newest first
    for _, n := range names {
        runDir := filepath.Join(base, n)
        rs, err := LoadRunState(runDir)
        if err != nil {
            if errors.Is(err, ErrCorruptLog) { return "", fmt.Errorf("run %s: %w", n, err) }
            continue // missing/unreadable events.jsonl: not a candidate
        }
        if rs.AllDone() { return runDir, nil }
    }
    return "", ErrNoRun
}
```

- [ ] **Step 4: Run the test, verify it passes**

Run: `go test ./pkg/state -run TestFindLatestCompletedRunDir -v`
Expected: PASS. Then `go test ./pkg/state` (whole package still green).

- [ ] **Step 5: Write failing test for the CLI skeleton + safe explicit-run resolver**

Create `cmd/afm/memory_rebuild_test.go`:

```go
func TestMemoryRebuildCmd_Registered(t *testing.T) {
    root := newRootCmd()
    mem, _, err := root.Find([]string{"memory", "rebuild"})
    if err != nil { t.Fatal(err) }
    if mem.Name() != "rebuild" { t.Fatalf("got %s", mem.Name()) }
}

func TestMemoryRebuildCmd_CommitFlagsMutuallyExclusive(t *testing.T) {
    root := newRootCmd()
    root.SetArgs([]string{"memory", "rebuild", "flow.yaml", "--commit", "--no-commit"})
    root.SetOut(io.Discard); root.SetErr(io.Discard)
    err := root.Execute()
    if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
        t.Fatalf("want mutually-exclusive error, got %v", err)
    }
}

func TestResolveExplicitRunDir(t *testing.T) {
    base := t.TempDir()
    if err := os.MkdirAll(filepath.Join(base, "flow-20260101-000000-aaaa"), 0755); err != nil { t.Fatal(err) }
    good, err := resolveExplicitRunDir(base, "flow", "flow-20260101-000000-aaaa")
    if err != nil { t.Fatal(err) }
    if good != filepath.Join(base, "flow-20260101-000000-aaaa") { t.Fatalf("got %s", good) }

    for _, bad := range []string{
        "../escape", "sub/flow-x", `a\b`, ".", "..", "/abs/flow-x",
        "other-20260101-000000-aaaa", // wrong flow name
        "flow-20260101-000000-zzzz",  // does not exist
    } {
        if _, err := resolveExplicitRunDir(base, "flow", bad); err == nil {
            t.Errorf("expected error for %q", bad)
        }
    }
}
```

- [ ] **Step 6: Run the test, verify it fails**

Run: `go test ./cmd/afm -run 'TestMemoryRebuild|TestResolveExplicitRunDir' -v`
Expected: FAIL (undefined symbols).

- [ ] **Step 7: Implement the command skeleton + resolver**

`cmd/afm/memory.go`:

```go
package main

import "github.com/spf13/cobra"

func newMemoryCmd() *cobra.Command {
    cmd := &cobra.Command{
        Use:   "memory",
        Short: "Agent-memory maintenance commands",
    }
    cmd.AddCommand(newMemoryRebuildCmd())
    return cmd
}
```

`cmd/afm/memory_rebuild.go`:

```go
package main

import (
    "context"
    "fmt"
    "path/filepath"

    "github.com/spf13/cobra"
)

// rebuildOptions carries parsed CLI state into the handler seam.
type rebuildOptions struct {
    FlowArg      string
    RunID        string
    DryRun       bool
    ForceReflect bool
    CommitSet    bool // one of --commit/--no-commit was passed
    Commit       bool // effective value when CommitSet
}

// rebuildHandler is the seam so the command tree/flags can be tested before
// the full pipeline exists. Task 9 replaces the body with the real handler.
var rebuildHandler = func(ctx context.Context, opts rebuildOptions) error {
    return fmt.Errorf("memory rebuild: not implemented")
}

func newMemoryRebuildCmd() *cobra.Command {
    var dryRun, forceReflect, commit, noCommit bool
    cmd := &cobra.Command{
        Use:   "rebuild [flow.yaml]",
        Short: "Rebuild agent memory from a completed run's session logs",
        Args:  cobra.MaximumNArgs(1),
        RunE: func(cmd *cobra.Command, args []string) error {
            if commit && noCommit {
                return fmt.Errorf("--commit and --no-commit are mutually exclusive")
            }
            opts := rebuildOptions{
                DryRun:       dryRun,
                ForceReflect: forceReflect,
                CommitSet:    commit || noCommit,
                Commit:       commit,
            }
            if len(args) == 1 { opts.FlowArg = args[0] }
            opts.RunID, _ = cmd.Flags().GetString("run")
            return rebuildHandler(cmd.Context(), opts)
        },
    }
    cmd.Flags().String("run", "", "explicit run id (default: latest completed run of the flow)")
    cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show diffs without writing memory or committing")
    cmd.Flags().BoolVar(&forceReflect, "force-reflect", false, "regenerate reflect datasets from logs")
    cmd.Flags().BoolVar(&commit, "commit", false, "git-commit changed memory files")
    cmd.Flags().BoolVar(&noCommit, "no-commit", false, "do not git-commit even if flow enables it")
    return cmd
}

// resolveExplicitRunDir accepts only a safe basename anchored to flowName.
func resolveExplicitRunDir(runsBase, flowName, id string) (string, error) {
    if id == "" { return "", fmt.Errorf("empty run id") }
    if filepath.Base(id) != id || id == "." || id == ".." {
        return "", fmt.Errorf("invalid run id %q: must be a bare run directory name", id)
    }
    prefix := flowName + "-"
    if len(id) <= len(prefix) || id[:len(prefix)] != prefix || id[len(prefix)] < '0' || id[len(prefix)] > '9' {
        return "", fmt.Errorf("run id %q does not belong to flow %q", id, flowName)
    }
    dir := filepath.Join(runsBase, id)
    info, err := os.Stat(dir)
    if err != nil || !info.IsDir() {
        return "", fmt.Errorf("run %q not found under %s", id, runsBase)
    }
    return dir, nil
}
```

Add `os` to imports. In `cmd/afm/main.go`, register the command in the subcommand block: `rootCmd.AddCommand(newMemoryCmd())`.

- [ ] **Step 8: Run tests, verify they pass**

Run: `go test ./cmd/afm -run 'TestMemoryRebuild|TestResolveExplicitRunDir' -v`
Expected: PASS.

- [ ] **Step 9: Lint + commit**

```bash
make lint-ci
git add pkg/state/state.go pkg/state/run_lookup_completed_test.go cmd/afm/memory.go cmd/afm/memory_rebuild.go cmd/afm/memory_rebuild_test.go cmd/afm/main.go
git commit -m "feat(memory-rebuild): резолвер completed-run и скелет команды memory rebuild"
```

---

## Task 2: Read-only run lock helper; refactor `Store.Open` onto it

**Files:**
- Create: `pkg/state/runlock.go`
- Create: `pkg/state/runlock_test.go`
- Modify: `pkg/state/store.go` (use `TryLockRun` internally)

**Interfaces:**
- Produces: `state.TryLockRun(runDir string) (*RunLock, error)` (returns `ErrRunLocked` when busy), `(*RunLock).Close() error`
- Consumes: `pkg/progress.NewLock`, `(*progress.Lock).TryLock/Unlock`

- [ ] **Step 1: Write failing test**

Create `pkg/state/runlock_test.go`:

```go
func TestTryLockRun_MutualExclusionWithStore(t *testing.T) {
    dir := t.TempDir()
    // A live Store holds the lock:
    st, err := Open(dir) // adjust to the real Store.Open signature/args
    if err != nil { t.Fatal(err) }
    defer st.Close()

    if _, err := TryLockRun(dir); !errors.Is(err, ErrRunLocked) {
        t.Fatalf("want ErrRunLocked while Store open, got %v", err)
    }
}

func TestTryLockRun_BlocksStoreOpen(t *testing.T) {
    dir := t.TempDir()
    lock, err := TryLockRun(dir)
    if err != nil { t.Fatal(err) }
    defer lock.Close()

    if _, err := Open(dir); !errors.Is(err, ErrRunLocked) {
        t.Fatalf("want ErrRunLocked while rebuild lock held, got %v", err)
    }
}

func TestTryLockRun_DoesNotTouchEventLog(t *testing.T) {
    dir := t.TempDir()
    // no events.jsonl exists yet
    lock, err := TryLockRun(dir)
    if err != nil { t.Fatal(err) }
    defer lock.Close()
    if _, err := os.Stat(filepath.Join(dir, "events.jsonl")); !os.IsNotExist(err) {
        t.Fatalf("lock must not create events.jsonl")
    }
    if _, err := os.Stat(filepath.Join(dir, "state.json")); !os.IsNotExist(err) {
        t.Fatalf("lock must not create state.json")
    }
    // only .lock may exist
    if _, err := os.Stat(filepath.Join(dir, ".lock")); err != nil {
        t.Fatalf("expected .lock, got %v", err)
    }
}
```

Check the real `Store.Open` name/signature first (`grep -n "func Open" pkg/state/store.go`) and adjust the calls.

- [ ] **Step 2: Run test, verify it fails**

Run: `go test ./pkg/state -run TestTryLockRun -v`
Expected: FAIL (undefined `TryLockRun`/`RunLock`).

- [ ] **Step 3: Implement `runlock.go`**

```go
package state

import (
    "path/filepath"
    "github.com/<module>/pkg/progress" // use the real module path
)

// RunLock is a read-only exclusive advisory lock over <runDir>/.lock,
// the same file Store.Open locks, but without opening/truncating the event
// log or writing a snapshot. Used by offline tooling (memory rebuild) that
// must not run concurrently with a live afm run against the same run dir.
type RunLock struct { l *progress.Lock }

func TryLockRun(runDir string) (*RunLock, error) {
    l, err := progress.NewLock(filepath.Join(runDir, ".lock"))
    if err != nil { return nil, err }
    if err := l.TryLock(); err != nil { return nil, ErrRunLocked }
    return &RunLock{l: l}, nil
}

func (r *RunLock) Close() error { if r.l != nil { r.l.Unlock(); r.l = nil }; return nil }
```

- [ ] **Step 4: Refactor `Store.Open` to use the shared helper**

In `pkg/state/store.go`, replace the inline `progress.NewLock(...)+TryLock()` block with a call that reuses the same acquisition. Minimal change: keep `Store` holding a `*progress.Lock`, but acquire it through a tiny shared internal function so both paths are identical. Example:

```go
// acquireRunLock takes the exclusive non-blocking lock on <runDir>/.lock,
// returning ErrRunLocked when busy. Shared by Store.Open and TryLockRun.
func acquireRunLock(runDir string) (*progress.Lock, error) {
    l, err := progress.NewLock(filepath.Join(runDir, ".lock"))
    if err != nil { return nil, err }
    if err := l.TryLock(); err != nil { return nil, ErrRunLocked }
    return l, nil
}
```

Have `TryLockRun` and `Store.Open` both call `acquireRunLock`. Do not change `Store.Close`'s unlock behavior.

- [ ] **Step 5: Run tests, verify they pass**

Run: `go test ./pkg/state -v`
Expected: PASS (new tests + all existing store/lock tests still green).

- [ ] **Step 6: Lint + commit**

```bash
make lint-ci
git add pkg/state/runlock.go pkg/state/runlock_test.go pkg/state/store.go
git commit -m "feat(memory-rebuild): read-only RunLock и общий helper блокировки run"
```

---

## Task 3: Extract the memory engine into `pkg/memorypipeline`

This is a mechanical move + rename. Keep the live orchestrator compiling and all its tests green at every step. **Do not change prompt text or agent args.**

**Files:**
- Create: `pkg/memorypipeline/types.go` (public `Prompts`, `AgentConfig`, `AgentSpec`, kind constants, `AgentRunner` seam)
- Create: `pkg/memorypipeline/prompts.go` (moved `buildMemoryPrompt` → `BuildPrompt`)
- Create: `pkg/memorypipeline/executor.go` (production `AgentRunner` using `executor.RunAgent`)
- Create: `pkg/memorypipeline/pipeline_test.go` (moved unit tests)
- Modify: `pkg/orchestrator/memory_prompts.go`, `pkg/orchestrator/memory_agent.go` (delegate to the new package)
- Modify: `pkg/orchestrator/orchestrator.go` (wire seam via adapter)
- Modify: `cmd/afm/run.go` (`loadPrompts` returns/also builds a `memorypipeline.Prompts`)

**Interfaces:**
- Produces (public):
  ```go
  type Prompts struct { Reflect, Aggregate, Prioritize, Update string }
  type AgentConfig struct {
      Command    string
      ExtraArgs  []string
      WrapperDir string
      RootDir    string
      RunDir     string
      IdleTimeout time.Duration
      Debug      bool
  }
  const (
      KindReflect    = "reflect"
      KindAggregate  = "aggregate"
      KindPrioritize = "prioritize"
      KindUpdate     = "update"
  )
  type AgentSpec struct {
      Kind       string
      StageName  string
      Command    string   // "" → AgentConfig.Command
      LogFile    string
      Sources    []string // reflect: files to read
      DatasetOut string   // reflect: yaml out
      InPaths    []string // aggregate: dataset files
      In         string   // prioritize: patterns.md
      Out        string   // aggregate/prioritize output
      HighPath   string   // update: high.md
      TargetFile string   // update: memory file to rewrite
      MaxRules   int      // update
  }
  type AgentRunner func(ctx context.Context, spec AgentSpec) error
  func BuildPrompt(p Prompts, spec AgentSpec) string
  func NewExecRunner(cfg AgentConfig) AgentRunner
  ```
- Consumes: `pkg/executor`.

- [ ] **Step 1: Create the new package by moving code verbatim**

Copy `pkg/orchestrator/memory_prompts.go`'s `memoryAgentSpec` → `AgentSpec` (exported fields), the kind constants → exported `Kind*`, and `buildMemoryPrompt` → `BuildPrompt` into `pkg/memorypipeline/{types.go,prompts.go}`. Keep the `<FILEPATH>`/`<MAX_RULES>` substitution logic and the "AFM FILE I/O" wrapper text **byte-for-byte identical** (copy, don't retype).

Create `pkg/memorypipeline/executor.go` with the production runner, lifted from `execMemoryAgent` (`pkg/orchestrator/memory_agent.go`) — but taking config explicitly instead of `o.opts`:

```go
func NewExecRunner(cfg AgentConfig) AgentRunner {
    return func(ctx context.Context, spec AgentSpec) error {
        cmd := spec.Command
        var extra []string
        if cmd == "" { cmd, extra = cfg.Command, cfg.ExtraArgs }
        ec := executor.Config{
            Command:     cmd,
            ExtraArgs:   executor.ResolveArgs(extra),
            IdleTimeout: cfg.IdleTimeout,
            WrapperDir:  cfg.WrapperDir, // caller passes wrapperDirFor(cmd, ...) result
            Dir:         cfg.RootDir,
            Debug:       cfg.Debug,
            RunDir:      cfg.RunDir,
            // NB: no SessionID/Resume/StageDir — fresh context.
        }
        ex := executor.New(ec)
        prompt := BuildPrompt(promptsFromCtx, spec) // see Step 2 note
        return ex.RunAgent(ctx, "memory-"+spec.Kind, spec.StageName, prompt, spec.LogFile)
    }
}
```

- [ ] **Step 2: Decide prompt threading (avoid a hidden global)**

`BuildPrompt` needs `Prompts`. Thread it through the runner constructor, not a package global:

```go
func NewExecRunner(cfg AgentConfig, prompts Prompts) AgentRunner {
    return func(ctx context.Context, spec AgentSpec) error {
        // ... build ec ...
        return executor.New(ec).RunAgent(ctx, "memory-"+spec.Kind, spec.StageName, BuildPrompt(prompts, spec), spec.LogFile)
    }
}
```

Note: `wrapperDirFor(cmd, wrapperDir, generatedAgents)` currently lives in `pkg/orchestrator`. Move a copy into `pkg/memorypipeline` (small pure helper) OR compute the wrapper dir at the call site and pass it in `AgentConfig.WrapperDir`. Prefer **passing it in** so `memorypipeline` stays free of orchestrator internals — the caller (orchestrator or CLI) computes `wrapperDirFor` and sets `AgentConfig.WrapperDir`. Since wrapper dir depends on `cmd`, and `cmd` may be empty→default, resolve `cmd` in the caller before constructing `AgentConfig` for the common case (global command); for memory agents `cmd` is always the global client command, so this is a single known value.

- [ ] **Step 3: Move the unit tests**

Move `pkg/orchestrator/memory_prompts_test.go` and the prompt/spec-building parts of `memory_agent_test.go` into `pkg/memorypipeline/pipeline_test.go`, updating to exported names (`BuildPrompt`, `AgentSpec`, `Kind*`). Keep every assertion on prompt text identical.

- [ ] **Step 4: Make the orchestrator delegate**

In `pkg/orchestrator`, replace the private `memoryAgentSpec`/`buildMemoryPrompt`/`execMemoryAgent` bodies with thin adapters to `memorypipeline`. Keep the internal seam field `runMemoryAgent func(ctx, memoryAgentSpec) error` working by converting `memoryAgentSpec` → `memorypipeline.AgentSpec`. Simplest: change the orchestrator to store and call a `memorypipeline.AgentRunner` directly, and have `distill`/`maybeRunReflection` build `memorypipeline.AgentSpec` values. Update `orchestrator.New` (orchestrator.go:405 area) to wire:

```go
o.memRunner = memorypipeline.NewExecRunner(memorypipeline.AgentConfig{
    Command:     o.opts.Config.Client.Command,
    ExtraArgs:   o.opts.Config.Client.ExtraArgs,
    WrapperDir:  wrapperDirFor(o.opts.Config.Client.Command, o.opts.WrapperDir, o.opts.GeneratedAgents),
    RootDir:     o.opts.RootDir,
    RunDir:      o.opts.RunDir,
    IdleTimeout: o.opts.Config.Executor.IdleTimeout,
    Debug:       o.opts.Debug,
}, memorypipeline.Prompts{
    Reflect:    o.opts.Prompts.Reflect,
    Aggregate:  o.opts.Prompts.Aggregate,
    Prioritize: o.opts.Prompts.Prioritize,
    Update:     o.opts.Prompts.Update,
})
```

Keep the test-injection seam: allow tests to replace `o.memRunner` (as they replace `o.runMemoryAgent` today). Update `reflection_test.go`'s `stubMemoryAgentByKind` to set `o.memRunner` instead. **This preserves resolveMemoryCommand semantics** (global command) because memory specs always leave `Command:""`.

- [ ] **Step 5: Run the full orchestrator + new-package tests**

Run:
```
go test ./pkg/memorypipeline -v
go test ./pkg/orchestrator/... 
```
Expected: PASS. If a memory-related orchestrator test references the old private names, update the reference (not the assertion).

- [ ] **Step 6: Race check on the touched package**

Run: `go test -race ./pkg/orchestrator/... ./pkg/memorypipeline/...`
Expected: PASS, no races.

- [ ] **Step 7: Lint + commit**

```bash
make lint-ci
git add pkg/memorypipeline/ pkg/orchestrator/memory_prompts.go pkg/orchestrator/memory_agent.go pkg/orchestrator/orchestrator.go pkg/orchestrator/reflection_test.go cmd/afm/run.go
git commit -m "refactor(memory): вынести memory-agent engine в pkg/memorypipeline"
```

---

## Task 4: Source inventory + strict dataset validation

**Files:**
- Create: `pkg/memorypipeline/sources.go`
- Create: `pkg/memorypipeline/sources_test.go`
- Create: `pkg/memorypipeline/dataset.go`
- Create: `pkg/memorypipeline/dataset_test.go`
- Modify: `pkg/memorypipeline/prompts.go` (reflect wrapper lists explicit files, not "the directory")

**Interfaces:**
- Produces: `SourceInventory(stageDir string) ([]string, error)` — sorted absolute regular-file paths, session-only, no symlinks.
- Produces: `ValidateDataset(data []byte) error`; `type Dataset struct { ProjectLevel, SessionLevel []DatasetItem }`, `type DatasetItem struct { Prompt, Chosen, Rejected, Source string }`; `ParseDataset(data []byte) (Dataset, error)`.
- Produces: `const maxDatasetBytes = 10 << 20`.

- [ ] **Step 1: Write failing tests for `SourceInventory`**

Create `pkg/memorypipeline/sources_test.go`. Build a fake stage dir with fixtures covering every include/exclude class:

```go
func TestSourceInventory_IncludesSessionsExcludesMemoryArtifacts(t *testing.T) {
    dir := t.TempDir()
    write := func(name string) { os.WriteFile(filepath.Join(dir, name), []byte("x"), 0644) }
    // included (session/user)
    for _, n := range []string{
        "planning.log", "planning.jsonl", "planning.stderr.log",
        "planning-revision.log", "implementation.log", "implementation-feedback.log",
        "review.log", "autonomous.log", "autonomous.jsonl",
        "plan.md", "execution_summary.md",
        "planning.dialog.jsonl", "planning.q1.question.json", "planning.q1.answer.json",
        "prenote.md", "feedback.md", "before.log", "after.log", "before.stderr.log",
    } { write(n) }
    // excluded (memory pipeline artifacts)
    for _, n := range []string{
        "reflect_dataset.yaml", "reflect.log", "reflect.jsonl", "reflect.stderr.log",
        "aggregate.log", "prioritize.log", "update.log",
        "patterns.md", "prioritized.md", "high.md",
    } { write(n) }
    // excluded: attempt workspace
    os.MkdirAll(filepath.Join(dir, "memory-rebuild", "20260101-000000-aaaa"), 0755)
    os.WriteFile(filepath.Join(dir, "memory-rebuild", "20260101-000000-aaaa", "reflect.log"), []byte("x"), 0644)

    got, err := SourceInventory(dir)
    if err != nil { t.Fatal(err) }
    gotSet := toBaseSet(got)
    mustHave := []string{"planning.log", "plan.md", "prenote.md", "feedback.md", "before.log", "autonomous.jsonl"}
    for _, n := range mustHave {
        if !gotSet[n] { t.Errorf("missing %s", n) }
    }
    mustNotHave := []string{"reflect_dataset.yaml", "reflect.log", "patterns.md", "high.md", "aggregate.log"}
    for _, n := range mustNotHave {
        if gotSet[n] { t.Errorf("must exclude %s", n) }
    }
}

func TestSourceInventory_ScriptOnlyIsEmpty(t *testing.T) {
    dir := t.TempDir()
    os.WriteFile(filepath.Join(dir, "script.log"), []byte("x"), 0644)
    os.WriteFile(filepath.Join(dir, "script.stderr.log"), []byte("x"), 0644)
    got, err := SourceInventory(dir)
    if err != nil { t.Fatal(err) }
    if len(got) != 0 { t.Fatalf("script-only dir must yield no sources, got %v", got) }
}

func TestSourceInventory_SkipsSymlinks(t *testing.T) {
    dir := t.TempDir()
    os.WriteFile(filepath.Join(dir, "planning.log"), []byte("x"), 0644)
    outside := filepath.Join(t.TempDir(), "secret")
    os.WriteFile(outside, []byte("x"), 0644)
    if err := os.Symlink(outside, filepath.Join(dir, "implementation.log")); err != nil { t.Skip("symlink unsupported") }
    got, _ := SourceInventory(dir)
    for _, p := range got {
        if filepath.Base(p) == "implementation.log" { t.Fatalf("must not follow symlink source") }
    }
}

func TestSourceInventory_Deterministic(t *testing.T) {
    dir := t.TempDir()
    for _, n := range []string{"review.log", "planning.log", "implementation.log"} {
        os.WriteFile(filepath.Join(dir, n), []byte("x"), 0644)
    }
    a, _ := SourceInventory(dir)
    b, _ := SourceInventory(dir)
    if !slices.Equal(a, b) { t.Fatalf("nondeterministic: %v vs %v", a, b) }
    if !slices.IsSorted(a) { t.Fatalf("not sorted: %v", a) }
}
```

Add a small `toBaseSet` helper in the test file.

- [ ] **Step 2: Run tests, verify they fail**

Run: `go test ./pkg/memorypipeline -run TestSourceInventory -v`
Expected: FAIL (undefined `SourceInventory`).

- [ ] **Step 3: Implement `sources.go`**

Build the included-set from `flow.Phases()` → `flow.PhaseLogFiles`/`flow.PhaseStreamLogs`, plus their `*.stderr.log`, plus the fixed user/session files. Classify by name; explicitly exclude memory-artifact prefixes; `os.Lstat` each entry and skip symlinks and non-regular files:

```go
package memorypipeline

import (
    "os"
    "path/filepath"
    "slices"
    "strings"

    "github.com/<module>/pkg/flow"
)

func includedNames() map[string]bool {
    m := map[string]bool{
        "plan.md": true, "execution_summary.md": true,
        "prenote.md": true, "feedback.md": true,
        "before.log": true, "after.log": true,
        "before.stderr.log": true, "after.stderr.log": true,
    }
    for _, p := range flow.Phases() {
        for _, f := range flow.PhaseLogFiles(p) {
            m[f] = true
            m[strings.TrimSuffix(f, ".log")+".stderr.log"] = true
        }
        for _, f := range flow.PhaseStreamLogs(p) { m[f] = true }
    }
    return m
}

var excludedPrefixes = []string{"reflect", "aggregate", "prioritize", "update", "patterns", "prioritized", "high"}

func isExcludedArtifact(name string) bool {
    if name == "reflect_dataset.yaml" { return true }
    for _, p := range excludedPrefixes {
        if strings.HasPrefix(name, p) { return true }
    }
    return false
}

// dialog/question/answer files match by suffix, not a fixed name.
func isDialogArtifact(name string) bool {
    return strings.HasSuffix(name, ".dialog.jsonl") ||
        strings.HasSuffix(name, ".question.json") ||
        strings.HasSuffix(name, ".answer.json")
}

// SourceInventory returns the deterministically-sorted absolute paths of the
// real agent-session source files in stageDir. Memory-pipeline artifacts,
// the attempt workspace, script-only logs, symlinks and non-regular files
// are excluded. Never recurses.
func SourceInventory(stageDir string) ([]string, error) {
    entries, err := os.ReadDir(stageDir)
    if err != nil {
        if os.IsNotExist(err) { return nil, nil }
        return nil, err
    }
    inc := includedNames()
    var out []string
    for _, e := range entries {
        name := e.Name()
        if e.IsDir() { continue } // excludes memory-rebuild/ and any nested dir
        if isExcludedArtifact(name) { continue }
        if !inc[name] && !isDialogArtifact(name) { continue }
        full := filepath.Join(stageDir, name)
        info, err := os.Lstat(full)
        if err != nil { continue }
        if info.Mode()&os.ModeSymlink != 0 { continue } // no symlink following
        if !info.Mode().IsRegular() { continue }
        out = append(out, full)
    }
    slices.Sort(out)
    return out, nil
}
```

- [ ] **Step 4: Run tests, verify they pass**

Run: `go test ./pkg/memorypipeline -run TestSourceInventory -v`
Expected: PASS.

- [ ] **Step 5: Write failing tests for dataset validation**

Create `pkg/memorypipeline/dataset_test.go`:

```go
func TestValidateDataset_Valid(t *testing.T) {
    y := []byte(`
project_level:
  - prompt: "when X"
    chosen: "do A"
    rejected: "do B"
    source: user
session_level: []
`)
    if err := ValidateDataset(y); err != nil { t.Fatal(err) }
}

func TestValidateDataset_EmptyBothSectionsValid(t *testing.T) {
    if err := ValidateDataset([]byte("project_level: []\nsession_level: []\n")); err != nil {
        t.Fatalf("empty-but-valid dataset should pass: %v", err)
    }
}

func TestValidateDataset_Rejects(t *testing.T) {
    cases := map[string]string{
        "unknown top key":  "project_level: []\nsession_level: []\nextra: 1\n",
        "bad source":       "project_level:\n  - {prompt: p, chosen: c, rejected: r, source: bogus}\nsession_level: []\n",
        "empty prompt":     "project_level:\n  - {prompt: '', chosen: c, rejected: r, source: log}\nsession_level: []\n",
        "missing field":    "project_level:\n  - {prompt: p, chosen: c, source: log}\nsession_level: []\n",
        "not a mapping":    "just a string\n",
    }
    for name, y := range cases {
        if err := ValidateDataset([]byte(y)); err == nil {
            t.Errorf("%s: expected error", name)
        }
    }
}

func TestValidateDataset_OversizeRejected(t *testing.T) {
    big := make([]byte, maxDatasetBytes+1)
    if err := ValidateDataset(big); err == nil { t.Fatal("oversize must be rejected") }
}
```

- [ ] **Step 6: Run tests, verify they fail**

Run: `go test ./pkg/memorypipeline -run TestValidateDataset -v`
Expected: FAIL.

- [ ] **Step 7: Implement `dataset.go`**

```go
package memorypipeline

import (
    "errors"
    "fmt"
    "strings"

    "gopkg.in/yaml.v3"
)

const maxDatasetBytes = 10 << 20 // 10 MiB

type DatasetItem struct {
    Prompt   string `yaml:"prompt"`
    Chosen   string `yaml:"chosen"`
    Rejected string `yaml:"rejected"`
    Source   string `yaml:"source"`
}

type Dataset struct {
    ProjectLevel []DatasetItem `yaml:"project_level"`
    SessionLevel []DatasetItem `yaml:"session_level"`
}

func ParseDataset(data []byte) (Dataset, error) {
    if len(data) > maxDatasetBytes {
        return Dataset{}, fmt.Errorf("dataset too large: %d bytes", len(data))
    }
    dec := yaml.NewDecoder(strings.NewReader(string(data)))
    dec.KnownFields(true) // reject unknown top/item keys
    var ds Dataset
    if err := dec.Decode(&ds); err != nil {
        return Dataset{}, fmt.Errorf("parse dataset: %w", err)
    }
    return ds, nil
}

func ValidateDataset(data []byte) error {
    ds, err := ParseDataset(data)
    if err != nil { return err }
    check := func(section string, items []DatasetItem) error {
        for i, it := range items {
            if strings.TrimSpace(it.Prompt) == "" ||
                strings.TrimSpace(it.Chosen) == "" ||
                strings.TrimSpace(it.Rejected) == "" {
                return fmt.Errorf("%s[%d]: prompt/chosen/rejected must be non-empty", section, i)
            }
            if it.Source != "user" && it.Source != "log" {
                return fmt.Errorf("%s[%d]: source must be user|log, got %q", section, i, it.Source)
            }
        }
        return nil
    }
    return errors.Join(check("project_level", ds.ProjectLevel), check("session_level", ds.SessionLevel))
}
```

Note on `KnownFields`: verify it rejects a scalar top-level document (the "not a mapping" case) — if `yaml.v3` decodes a bare string into an empty struct without error, add an explicit check that the decoded YAML node kind is a mapping (decode into `yaml.Node` first, assert `Kind == yaml.MappingNode`). Adjust the test/impl together so "not a mapping" fails.

- [ ] **Step 8: Update the reflect prompt wrapper to list explicit files**

In `pkg/memorypipeline/prompts.go`, the reflect branch of `BuildPrompt` must instruct the agent to read **exactly** `spec.Sources` (absolute paths, one per line) rather than "every *.log under the directory". Keep the existing highest-priority-user-input wording. Update the corresponding prompt-text assertion in `pipeline_test.go`.

- [ ] **Step 9: Run tests, verify they pass**

Run: `go test ./pkg/memorypipeline -v`
Expected: PASS.

- [ ] **Step 10: Lint + commit**

```bash
make lint-ci
git add pkg/memorypipeline/sources.go pkg/memorypipeline/sources_test.go pkg/memorypipeline/dataset.go pkg/memorypipeline/dataset_test.go pkg/memorypipeline/prompts.go pkg/memorypipeline/pipeline_test.go
git commit -m "feat(memory-rebuild): инвентаризация исходников reflect и строгая валидация dataset"
```

---

## Task 5: Staging distill + output validation + durable AtomicWrite

**Files:**
- Create: `pkg/memorypipeline/distill.go`
- Create: `pkg/memorypipeline/rules.go` (validate the update-agent's Markdown output)
- Create: `pkg/memorypipeline/distill_test.go`
- Modify: `pkg/memory/paths.go` (`AtomicWrite` → fsync file + parent)
- Modify: `pkg/memory/paths_test.go` (or create) — assert durability contract

**Interfaces:**
- Produces: `func (p *Pipeline) DistillTarget(ctx, target DistillTarget) (StepArtifacts, error)` where
  ```go
  type DistillTarget struct {
      Name       string   // stage name or "flow-memory" (for logs)
      Datasets   []string // absolute reflect_dataset.yaml paths
      StagingDir string   // attempt workdir for this target's intermediate files
      TargetFile string   // real destination (used to seed staging target.md); never written here
      MaxRules   int
  }
  type StepArtifacts struct {
      PatternsPath, PrioritizedPath, HighPath, CandidatePath string
      NoHigh bool // true → no high-priority patterns; candidate == existing
  }
  ```
- Produces: `func ValidateRules(md string, maxRules int) error`
- Consumes: `AgentRunner`, `memory.SelectHigh`, `memory.AtomicWrite` (hardened).

- [ ] **Step 1: Harden `memory.AtomicWrite` (durability) first**

Write/extend `pkg/memory/paths_test.go`:

```go
func TestAtomicWrite_WritesAndReplaces(t *testing.T) {
    dir := t.TempDir()
    p := filepath.Join(dir, "sub", "memory.md")
    if err := AtomicWrite(p, []byte("v1")); err != nil { t.Fatal(err) }
    if b, _ := os.ReadFile(p); string(b) != "v1" { t.Fatalf("got %q", b) }
    if err := AtomicWrite(p, []byte("v2")); err != nil { t.Fatal(err) }
    if b, _ := os.ReadFile(p); string(b) != "v2" { t.Fatalf("got %q", b) }
    // no leftover temp files
    entries, _ := os.ReadDir(filepath.Dir(p))
    for _, e := range entries {
        if strings.HasPrefix(e.Name(), "tmp-") { t.Fatalf("leftover temp %s", e.Name()) }
    }
}
```

Then update `AtomicWrite` in `pkg/memory/paths.go` to fsync the file before close and fsync the parent dir after rename (API unchanged):

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
    if d, err := os.Open(dir); err == nil { _ = d.Sync(); d.Close() } // best-effort parent fsync
    return nil
}
```

Run: `go test ./pkg/memory -v` → PASS.

- [ ] **Step 2: Write failing tests for `ValidateRules`**

Create the rules cases in `pkg/memorypipeline/distill_test.go`:

```go
func TestValidateRules(t *testing.T) {
    ok := "# Project rules\n\n## Alpha\nbody\n\n## Beta\nbody\n"
    if err := ValidateRules(ok, 25); err != nil { t.Fatalf("valid rules rejected: %v", err) }

    bad := map[string]string{
        "no header":     "## Alpha\nbody\n",
        "tier heading":  "# Project rules\n\n## High\n## Alpha\nbody\n",
        "too many":      "# Project rules\n\n## A\nx\n\n## B\nx\n\n## C\nx\n",
    }
    if err := ValidateRules(bad["no header"], 25); err == nil { t.Error("no header must fail") }
    if err := ValidateRules(bad["tier heading"], 25); err == nil { t.Error("tier heading must fail") }
    if err := ValidateRules(bad["too many"], 2); err == nil { t.Error("cap overflow must fail") }
}
```

- [ ] **Step 3: Run, verify fail; implement `rules.go`**

```go
package memorypipeline

import (
    "bufio"
    "fmt"
    "strings"
    "unicode/utf8"
)

var tierHeadings = map[string]bool{"## High": true, "## Medium": true, "## Low": true}

// ValidateRules checks the update-agent's output: "# Project rules" header,
// only "## <Pattern>" blocks (no tier headings), <= maxRules patterns, UTF-8.
func ValidateRules(md string, maxRules int) error {
    if !utf8.ValidString(md) { return fmt.Errorf("rules output is not valid UTF-8") }
    sc := bufio.NewScanner(strings.NewReader(md))
    sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
    sawHeader, count := false, 0
    for sc.Scan() {
        line := strings.TrimRight(sc.Text(), " \t")
        switch {
        case line == "# Project rules":
            sawHeader = true
        case strings.HasPrefix(line, "## "):
            if tierHeadings[line] {
                return fmt.Errorf("tier heading %q not allowed in output", line)
            }
            count++
        }
    }
    if err := sc.Err(); err != nil { return err }
    if !sawHeader { return fmt.Errorf("missing '# Project rules' header") }
    if maxRules > 0 && count > maxRules {
        return fmt.Errorf("too many patterns: %d > max_rules %d", count, maxRules)
    }
    return nil
}
```

Run: `go test ./pkg/memorypipeline -run TestValidateRules -v` → PASS.

- [ ] **Step 4: Write failing test for `DistillTarget` (with a stub runner)**

```go
func TestDistillTarget_ChainOrderAndStaging(t *testing.T) {
    work := t.TempDir()
    ds := filepath.Join(work, "reflect_dataset.yaml")
    os.WriteFile(ds, []byte("project_level: []\nsession_level: []\n"), 0644)
    realTarget := filepath.Join(t.TempDir(), "memory.md")
    os.WriteFile(realTarget, []byte("# Project rules\n\n## Old\nx\n"), 0644)

    var calls []string
    runner := func(ctx context.Context, spec AgentSpec) error {
        calls = append(calls, spec.Kind)
        switch spec.Kind {
        case KindAggregate:  return os.WriteFile(spec.Out, []byte("1. Foo — bar\n"), 0644)
        case KindPrioritize: return os.WriteFile(spec.Out, []byte("## High\nFoo — bar\n## Medium\n## Low\n"), 0644)
        case KindUpdate:
            // update must receive the STAGING target, not realTarget
            if spec.TargetFile == realTarget { t.Errorf("update got real target, want staging copy") }
            return os.WriteFile(spec.TargetFile, []byte("# Project rules\n\n## Foo\nbar\n"), 0644)
        }
        return nil
    }
    p := New(Prompts{}, AgentConfig{}, WithRunner(runner))
    art, err := p.DistillTarget(context.Background(), DistillTarget{
        Name: "flow-memory", Datasets: []string{ds},
        StagingDir: filepath.Join(work, "flow"), TargetFile: realTarget, MaxRules: 25,
    })
    if err != nil { t.Fatal(err) }
    if !slices.Equal(calls, []string{KindAggregate, KindPrioritize, KindUpdate}) {
        t.Fatalf("wrong order: %v", calls)
    }
    if art.NoHigh { t.Fatal("expected high patterns") }
    // real target must be UNTOUCHED by distill (publish is a later phase)
    if b, _ := os.ReadFile(realTarget); string(b) != "# Project rules\n\n## Old\nx\n" {
        t.Fatalf("distill must not write the real target, got %q", b)
    }
}

func TestDistillTarget_NoHighIsNoOp(t *testing.T) {
    // prioritize returns empty High → NoHigh true, no update call
    // ... runner returns "## High\n## Medium\nx\n## Low\n" (empty High) ...
}
```

Add `New(...)` with an `Option`/`WithRunner` seam (a functional option that overrides the default `AgentRunner`). If `New` is already stubbed from Task 6's interface, keep signatures consistent (`New(prompts, agent, opts...)`).

- [ ] **Step 5: Run, verify fail; implement `distill.go` + `Pipeline`/`New`/`WithRunner`**

```go
package memorypipeline

import (
    "context"
    "fmt"
    "os"
    "path/filepath"

    "github.com/<module>/pkg/memory"
)

type Pipeline struct {
    prompts Prompts
    agent   AgentConfig
    run     AgentRunner
}

type Option func(*Pipeline)

func WithRunner(r AgentRunner) Option { return func(p *Pipeline) { p.run = r } }

func New(prompts Prompts, agent AgentConfig, opts ...Option) *Pipeline {
    p := &Pipeline{prompts: prompts, agent: agent}
    p.run = NewExecRunner(agent, prompts)
    for _, o := range opts { o(p) }
    return p
}

// DistillTarget runs aggregate → prioritize → SelectHigh → update against a
// STAGING copy of TargetFile inside StagingDir. It NEVER writes TargetFile.
func (p *Pipeline) DistillTarget(ctx context.Context, t DistillTarget) (StepArtifacts, error) {
    if err := os.MkdirAll(t.StagingDir, 0755); err != nil { return StepArtifacts{}, err }
    a := StepArtifacts{
        PatternsPath:    filepath.Join(t.StagingDir, "patterns.md"),
        PrioritizedPath: filepath.Join(t.StagingDir, "prioritized.md"),
        HighPath:        filepath.Join(t.StagingDir, "high.md"),
        CandidatePath:   filepath.Join(t.StagingDir, "target.md"),
    }
    // aggregate
    if err := p.run(ctx, AgentSpec{Kind: KindAggregate, StageName: t.Name,
        InPaths: t.Datasets, Out: a.PatternsPath,
        LogFile: filepath.Join(t.StagingDir, "aggregate.log")}); err != nil {
        return a, fmt.Errorf("aggregate: %w", err)
    }
    if err := requireFresh(a.PatternsPath); err != nil { return a, fmt.Errorf("aggregate: %w", err) }
    // prioritize
    if err := p.run(ctx, AgentSpec{Kind: KindPrioritize, StageName: t.Name,
        In: a.PatternsPath, Out: a.PrioritizedPath,
        LogFile: filepath.Join(t.StagingDir, "prioritize.log")}); err != nil {
        return a, fmt.Errorf("prioritize: %w", err)
    }
    if err := requireFresh(a.PrioritizedPath); err != nil { return a, fmt.Errorf("prioritize: %w", err) }
    // select High (code)
    pr, err := os.ReadFile(a.PrioritizedPath)
    if err != nil { return a, err }
    high := memory.SelectHigh(string(pr))
    if high == "" { a.NoHigh = true; return a, nil }
    if err := memory.AtomicWrite(a.HighPath, []byte(high)); err != nil { return a, err }
    // seed staging target from the existing real target (missing → empty)
    if err := seedStagingTarget(t.TargetFile, a.CandidatePath); err != nil { return a, err }
    // update (writes only the staging candidate)
    if err := p.run(ctx, AgentSpec{Kind: KindUpdate, StageName: t.Name,
        HighPath: a.HighPath, TargetFile: a.CandidatePath, MaxRules: t.MaxRules,
        LogFile: filepath.Join(t.StagingDir, "update.log")}); err != nil {
        return a, fmt.Errorf("update: %w", err)
    }
    cand, err := os.ReadFile(a.CandidatePath)
    if err != nil { return a, fmt.Errorf("update produced no output: %w", err) }
    if err := ValidateRules(string(cand), t.MaxRules); err != nil {
        return a, fmt.Errorf("update output invalid: %w", err)
    }
    return a, nil
}

func requireFresh(path string) error {
    info, err := os.Stat(path)
    if err != nil { return fmt.Errorf("expected output %s: %w", filepath.Base(path), err) }
    if !info.Mode().IsRegular() { return fmt.Errorf("%s is not a regular file", filepath.Base(path)) }
    return nil
}

func seedStagingTarget(realTarget, staging string) error {
    data, err := os.ReadFile(realTarget)
    if err != nil {
        if os.IsNotExist(err) { data = nil } else { return err }
    }
    return memory.AtomicWrite(staging, data)
}
```

Add `DistillTarget`/`StepArtifacts` structs to `types.go`.

**Shared staging target for multiple stages sharing one `reflect.file`:** `DistillTarget` seeds from `TargetFile` on disk. For the shared-file case (Task 6), the caller passes each subsequent stage a `TargetFile` pointing at the **previous stage's candidate**, so merges chain through staging. Document this on the struct.

- [ ] **Step 6: Run tests, verify they pass**

Run: `go test ./pkg/memorypipeline -v`
Expected: PASS.

- [ ] **Step 7: Lint + commit**

```bash
make lint-ci
git add pkg/memorypipeline/distill.go pkg/memorypipeline/rules.go pkg/memorypipeline/distill_test.go pkg/memorypipeline/types.go pkg/memory/paths.go pkg/memory/paths_test.go
git commit -m "feat(memory-rebuild): staging distill, валидация правил и durable AtomicWrite"
```

---

## Task 6: Rebuilder — capture, attempt workspace, manifest, transactional publish

**Files:**
- Create: `pkg/memorypipeline/rebuild.go` (`CaptureStage`, `Rebuild`)
- Create: `pkg/memorypipeline/manifest.go`
- Create: `pkg/memorypipeline/rebuild_test.go`

**Interfaces:**
- Produces:
  ```go
  func (p *Pipeline) CaptureStage(ctx context.Context, stage flow.Stage, stageDir, datasetOut, logDir string) error
  func (p *Pipeline) Rebuild(ctx context.Context, req RebuildRequest) (Report, error)
  type RebuildRequest struct {
      RunID, RunDir, MemoryDir, WorkDir string // WorkDir = the attempt dir <runDir>/memory-rebuild/<ts>-<rand>
      Stages []flow.Stage
      Memory flow.MemoryConfig
      ForceReflect, DryRun bool
  }
  type Report struct {
      RunID, WorkDir string
      Datasets []DatasetResult // {StageID, Source: "reused"|"generated"}
      Targets  []TargetResult  // {Path, Changed bool, NoHigh bool}
      Committed bool
  }
  ```
- Consumes: `SourceInventory`, `ValidateDataset`, `DistillTarget`, `memory.StageFile`/`ProjectFile`, `memory.AtomicWrite`.
- Note: `WorkDir` is created by the CLI (Task 9) with a unique `<ts>-<rand4>` name; the pipeline treats it as given (deterministic under test). Timestamp/rand come from the caller — `Date.now`/rand are not used inside the pipeline (keeps tests deterministic).

- [ ] **Step 1: Write failing test for `CaptureStage` (reuse vs generate vs force)**

```go
func TestCaptureStage_ReuseValidGenerateInvalidForce(t *testing.T) {
    stageDir := t.TempDir()
    os.WriteFile(filepath.Join(stageDir, "planning.log"), []byte("session"), 0644)
    valid := []byte("project_level: []\nsession_level: []\n")

    var reflectCalls int
    runner := func(ctx context.Context, spec AgentSpec) error {
        if spec.Kind == KindReflect { reflectCalls++; return os.WriteFile(spec.DatasetOut, valid, 0644) }
        return nil
    }
    p := New(Prompts{}, AgentConfig{}, WithRunner(runner))

    // (a) canonical valid dataset present → reuse, no reflect call
    canonical := filepath.Join(stageDir, "reflect_dataset.yaml")
    os.WriteFile(canonical, valid, 0644)
    if err := p.CaptureStage(context.Background(), flow.Stage{Name: "s"}, stageDir, canonical, stageDir); err != nil { t.Fatal(err) }
    if reflectCalls != 0 { t.Fatalf("valid dataset should be reused, reflect called %d", reflectCalls) }

    // (b) invalid canonical → regenerate
    os.WriteFile(canonical, []byte("garbage: ["), 0644)
    out := filepath.Join(t.TempDir(), "reflect_dataset.yaml")
    if err := p.CaptureStage(context.Background(), flow.Stage{Name: "s"}, stageDir, out, stageDir); err != nil { t.Fatal(err) }
    if reflectCalls != 1 { t.Fatalf("invalid dataset should regenerate, got %d", reflectCalls) }
}

func TestCaptureStage_NoAgentSourceIsError(t *testing.T) {
    stageDir := t.TempDir() // only script.log
    os.WriteFile(filepath.Join(stageDir, "script.log"), []byte("x"), 0644)
    p := New(Prompts{}, AgentConfig{}, WithRunner(func(context.Context, AgentSpec) error { return nil }))
    err := p.CaptureStage(context.Background(), flow.Stage{Name: "s"},
        stageDir, filepath.Join(t.TempDir(), "d.yaml"), stageDir)
    if err == nil { t.Fatal("no agent session source must be an error for explicit capture") }
}
```

`CaptureStage` signature note: add a `force bool` via `RebuildRequest.ForceReflect` at the `Rebuild` level; for the unit test call `CaptureStage` directly with reuse/regenerate logic driven by dataset validity. If you want `force` testable at this level, add it as a field the pipeline reads (e.g. `p.forceReflect`) or a param — pick one and keep it consistent with `Rebuild`.

- [ ] **Step 2: Run, verify fail; implement `CaptureStage`**

```go
func (p *Pipeline) CaptureStage(ctx context.Context, stage flow.Stage, stageDir, datasetOut, logDir string) error {
    // reuse a valid canonical dataset unless forced
    canonical := filepath.Join(stageDir, "reflect_dataset.yaml")
    if !p.forceReflect && datasetOut == canonical {
        if data, err := os.ReadFile(canonical); err == nil && ValidateDataset(data) == nil {
            return nil // reuse
        }
    }
    sources, err := SourceInventory(stageDir)
    if err != nil { return err }
    if len(sources) == 0 {
        return fmt.Errorf("stage %q: no agent-session sources in %s (script-only?)", stage.Name, stageDir)
    }
    if err := p.run(ctx, AgentSpec{Kind: KindReflect, StageName: stage.Name,
        Sources: sources, DatasetOut: datasetOut,
        LogFile: filepath.Join(logDir, "reflect.log")}); err != nil {
        return fmt.Errorf("reflect: %w", err)
    }
    data, err := os.ReadFile(datasetOut)
    if err != nil { return fmt.Errorf("reflect produced no dataset: %w", err) }
    if err := ValidateDataset(data); err != nil { return fmt.Errorf("reflect dataset invalid: %w", err) }
    return nil
}
```

Add `forceReflect bool` to `Pipeline`, set from an option `WithForceReflect(bool)` or from `RebuildRequest` inside `Rebuild` (temporarily set `p.forceReflect = req.ForceReflect` — but avoid mutating shared state across concurrent Rebuilds; simplest is a per-call flag threaded into `CaptureStage`). **Recommended:** thread `force bool` as an explicit param to `CaptureStage` and drop the field; update the test call accordingly.

- [ ] **Step 3: Write failing test for `Rebuild` end-to-end (stub runner, dry-run + publish)**

```go
func TestRebuild_DryRunLeavesTargetsUntouched(t *testing.T) {
    // one stage with reflect{file, mode rw}, memory mode rw
    // stub runner produces valid dataset + patterns + prioritized(High) + rules
    // assert: canonical dataset NOT written, stage target file NOT written,
    //         memory.md NOT written, Report.Targets[].Changed reported, manifest status=dry_run
}

func TestRebuild_PublishWritesOnlyChangedTargets(t *testing.T) {
    // same, DryRun=false → per-stage file and memory.md written durably,
    // canonical dataset published into stage dir, manifest status=completed,
    // unchanged target (identical bytes) not rewritten.
}

func TestRebuild_FailureBeforePublishLeavesCanonicalUnchanged(t *testing.T) {
    // runner returns error on KindUpdate → Rebuild returns error,
    // no target written, canonical dataset unchanged, manifest status=failed.
}

func TestRebuild_ProjectAggregateOnlyEligibleDatasets(t *testing.T) {
    // run dir has a stray <run>/ghost/reflect_dataset.yaml for a stage NOT in flow;
    // assert project distill Datasets excludes it (only eligible stages).
}

func TestRebuild_SharedReflectFileMergesSequentially(t *testing.T) {
    // two stages, same reflect.file; assert stage2's update TargetFile == stage1's candidate.
}
```

These are the core behavioral guarantees — write them concretely with a scripted stub runner keyed by `spec.Kind` (+ `spec.StageName` for per-stage disambiguation).

- [ ] **Step 4: Run, verify fail; implement `Rebuild`**

Algorithm inside `Rebuild`:

1. `stagesDir := filepath.Join(req.WorkDir, "stages")`, `flowDir := filepath.Join(req.WorkDir, "flow")`.
2. Write initial `manifest.json` (status `running`) via `writeManifest` (atomic).
3. **Capture pass** (declaration order): for each stage with `Reflect != nil && Reflect.CanWrite() && !IsScript()`:
   - `canonical := memory.StageFile(req.RunDir, filepath.Join(stageID, "reflect_dataset.yaml"))` — i.e. `<runDir>/<stageID>/reflect_dataset.yaml`.
   - Decide reuse vs generate: if `!force` and canonical valid → `source="reused"`, dataset path = canonical. Else generate into `stagesDir/<stageID>/reflect_dataset.yaml`, `source="generated"`.
   - Record `DatasetResult{StageID, Source}`; collect the dataset path for later.
   - On error → update manifest `failed`, return error (nothing published).
4. **Per-stage distill pass** (gated by each stage's `Reflect.CanWrite()`): group stages by resolved `reflect.file`. For each group (declaration order), run `DistillTarget` chaining `TargetFile` across the group's stages (first seeds from the real per-stage memory file `memory.StageFile(req.MemoryDir, file)`, each next seeds from the prior candidate). Final candidate → a pending target `{finalPath: memory.StageFile(MemoryDir, file), candidate}`.
5. **Project distill pass** (only if `req.Memory.CanWriteProject()` and ≥1 eligible dataset): `DistillTarget{Name:"flow-memory", Datasets: eligibleDatasets, StagingDir: flowDir, TargetFile: memory.ProjectFile(MemoryDir)}` → pending target `{ProjectFile, candidate}`.
6. **Compute changed**: for each pending target, compare candidate bytes to the current file (missing = changed). `NoHigh` targets contribute no change.
7. **Publish** (skip entirely if `req.DryRun`): set manifest `promoting` with the pending target list; for each changed target `memory.AtomicWrite(finalPath, candidateBytes)`, mark published in manifest; publish generated canonical datasets into their stage dirs the same way. Set manifest `completed`.
8. Dry-run: set manifest `dry_run`, do not write any target/dataset.
9. Build and return `Report`.

Concurrency note: capture agents are independent; you MAY capture in parallel later, but v1 keeps it sequential (declaration order) for deterministic manifest and simplicity. Distill is inherently sequential (shared staging).

- [ ] **Step 5: Implement `manifest.go`**

```go
type Manifest struct {
    SchemaVersion int               `json:"schema_version"`
    OperationID   string            `json:"operation_id"`
    RunID         string            `json:"run_id"`
    RunPath       string            `json:"run_path"`
    FlowPath      string            `json:"flow_path"`
    FlowSHA256    string            `json:"flow_sha256"`
    RootDir       string            `json:"root_dir"`
    MemoryDir     string            `json:"memory_dir"`
    MemoryMode    string            `json:"memory_mode"`
    MaxRules      int               `json:"max_rules"`
    Commit        bool              `json:"commit"`
    PromptSHA256  map[string]string `json:"prompt_sha256"` // reflect/aggregate/prioritize/update
    Status        string            `json:"status"`        // running|failed|dry_run|promoting|completed
    StartedAt     string            `json:"started_at"`
    FinishedAt    string            `json:"finished_at,omitempty"`
    Datasets      []ManifestDataset `json:"datasets"`
    Targets       []ManifestTarget  `json:"targets"`
    Errors        []string          `json:"errors,omitempty"`
}
// ManifestDataset{StageID, Source, SourceFiles []string, SourceHashes []string, DatasetHash string}
// ManifestTarget{Path, OldHash, NewHash, Changed, Published bool}

func writeManifest(path string, m *Manifest) error {
    b, err := json.MarshalIndent(m, "", "  ")
    if err != nil { return err }
    return memory.AtomicWrite(path, b) // atomic, durable
}
```

Timestamps (`StartedAt`/`FinishedAt`) are passed in from the CLI (Task 9) — do not call `time.Now()` inside the pipeline paths that tests exercise; accept them via `RebuildRequest` or a small clock field so tests are deterministic. (Simplest: add `StartedAt string` to `RebuildRequest`, and stamp `FinishedAt` from a `Clock func() time.Time` field defaulting to `time.Now`, overridable in tests.)

- [ ] **Step 6: Run tests, verify they pass**

Run: `go test ./pkg/memorypipeline -v`
Expected: PASS.

- [ ] **Step 7: Race check**

Run: `go test -race ./pkg/memorypipeline`
Expected: PASS.

- [ ] **Step 8: Lint + commit**

```bash
make lint-ci
git add pkg/memorypipeline/rebuild.go pkg/memorypipeline/manifest.go pkg/memorypipeline/rebuild_test.go pkg/memorypipeline/types.go
git commit -m "feat(memory-rebuild): Rebuilder с capture, manifest и транзакционной публикацией"
```

---

## Task 7: Shared interprocess memory-dir lock

**Files:**
- Create: `pkg/memorypipeline/lock.go`
- Create: `pkg/memorypipeline/lock_test.go`

**Interfaces:**
- Produces:
  ```go
  func MemoryLockPath(memoryDir string) (string, error) // ~/.afm/locks/memory-<sha256(canonical abs dir)>.lock
  func AcquireMemoryLock(ctx context.Context, memoryDir string) (*MemoryLock, error)
  func (l *MemoryLock) Close() error
  ```
- Consumes: `pkg/progress`.

- [ ] **Step 1: Write failing tests**

```go
func TestMemoryLockPath_StableAndSymlinkAware(t *testing.T) {
    d := t.TempDir()
    p1, err := MemoryLockPath(d); if err != nil { t.Fatal(err) }
    p2, _ := MemoryLockPath(d + "/") // trailing slash → same cleaned dir
    if p1 != p2 { t.Fatalf("path not stable: %s vs %s", p1, p2) }
    // alias via symlink → same key
    alias := filepath.Join(t.TempDir(), "alias")
    if err := os.Symlink(d, alias); err == nil {
        pa, _ := MemoryLockPath(alias)
        if pa != p1 { t.Fatalf("symlink alias got different key: %s vs %s", pa, p1) }
    }
}

func TestAcquireMemoryLock_Serializes(t *testing.T) {
    d := t.TempDir()
    l1, err := AcquireMemoryLock(context.Background(), d); if err != nil { t.Fatal(err) }
    ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
    defer cancel()
    if _, err := AcquireMemoryLock(ctx, d); !errors.Is(err, context.DeadlineExceeded) {
        t.Fatalf("second acquire should block until ctx done, got %v", err)
    }
    l1.Close()
    l3, err := AcquireMemoryLock(context.Background(), d); if err != nil { t.Fatal(err) }
    l3.Close()
}
```

Set `HOME` to a temp dir in the test (or make the locks-root injectable) so `~/.afm/locks` is sandboxed: `t.Setenv("HOME", t.TempDir())`.

- [ ] **Step 2: Run, verify fail; implement `lock.go`**

```go
package memorypipeline

import (
    "context"
    "crypto/sha256"
    "encoding/hex"
    "os"
    "path/filepath"
    "time"

    "github.com/<module>/pkg/progress"
)

func MemoryLockPath(memoryDir string) (string, error) {
    abs, err := filepath.Abs(memoryDir)
    if err != nil { return "", err }
    abs = filepath.Clean(abs)
    if resolved, err := filepath.EvalSymlinks(abs); err == nil { abs = resolved } // best-effort: dir may not exist yet
    sum := sha256.Sum256([]byte(abs))
    home, err := os.UserHomeDir()
    if err != nil { return "", err }
    locks := filepath.Join(home, ".afm", "locks")
    if err := os.MkdirAll(locks, 0755); err != nil { return "", err }
    return filepath.Join(locks, "memory-"+hex.EncodeToString(sum[:])+".lock"), nil
}

type MemoryLock struct { l *progress.Lock }

// AcquireMemoryLock blocks (context-aware) until the shared lock is held.
func AcquireMemoryLock(ctx context.Context, memoryDir string) (*MemoryLock, error) {
    path, err := MemoryLockPath(memoryDir)
    if err != nil { return nil, err }
    l, err := progress.NewLock(path)
    if err != nil { return nil, err }
    ticker := time.NewTicker(100 * time.Millisecond)
    defer ticker.Stop()
    for {
        if err := l.TryLock(); err == nil { return &MemoryLock{l: l}, nil }
        select {
        case <-ctx.Done(): return nil, ctx.Err()
        case <-ticker.C:
        }
    }
}

func (m *MemoryLock) Close() error { if m.l != nil { m.l.Unlock(); m.l = nil }; return nil }
```

- [ ] **Step 3: Run tests + race**

Run: `go test -race ./pkg/memorypipeline -run 'TestMemoryLock|TestAcquireMemoryLock' -v`
Expected: PASS.

- [ ] **Step 4: Wire the lock into `Rebuild`'s publish window**

The lock must cover: seeding staging from the real target, the whole distill (update), change computation, and publish. Since distill seeds from the real target, acquire the memory lock in `Rebuild` **before** the per-stage/project distill passes and release after publish (or after dry-run diff). Add a test that `Rebuild` acquires it (inject a pre-held lock in the same process → the second acquire inside `Rebuild` blocks until ctx cancel → `Rebuild` returns ctx error). Keep run→memory ordering: the CLI already holds the run lock; `Rebuild` takes the memory lock internally.

- [ ] **Step 5: Lint + commit**

```bash
make lint-ci
git add pkg/memorypipeline/lock.go pkg/memorypipeline/lock_test.go pkg/memorypipeline/rebuild.go pkg/memorypipeline/rebuild_test.go
git commit -m "feat(memory-rebuild): общий interprocess memory-lock и его подключение в Rebuild"
```

---

## Task 8: Rewire the live orchestrator onto the shared engine + lock

**Files:**
- Modify: `pkg/orchestrator/reflection.go` (`maybeRunReflection` → `Pipeline.CaptureStage`; `runEndOfRunMemory` → `Pipeline.DistillTarget` under the shared lock)
- Modify: `pkg/orchestrator/orchestrator.go` (hold a `*memorypipeline.Pipeline`)
- Modify: `pkg/orchestrator/reflection_test.go`, `pkg/orchestrator/memory_integration_test.go`

**Interfaces:**
- Consumes: `memorypipeline.Pipeline`, `AcquireMemoryLock`.
- Preserves: `pendingReflections`, `finalReflectDone`, `reflectMu` semantics; best-effort `reflect_failed` notices; FSM untouched.

- [ ] **Step 1: Keep existing memory integration tests as the safety net**

Run the current memory tests to capture green baseline:
Run: `go test ./pkg/orchestrator -run 'Memory|Reflect' -v`
Expected: PASS (baseline before rewire).

- [ ] **Step 2: Rewire `maybeRunReflection` to call `Pipeline.CaptureStage`**

Keep the exact envelope: `MemoryDir==""`/nil-stage/`!CanWrite()`/`IsScript()` guards, `pendingReflections.Add(1)`/`SpawnDetached`/deferred `Add(-1)+WakeEventLoop()`. Inside, call `o.mem.CaptureStage(ctx, stage, stageDir, canonicalDataset, stageDir)` where the pipeline uses the injected runner (`o.memRunner`). On error, `o.reflectFailed(stage.Name, "reflect", err)`. **The live path must reuse a valid canonical dataset too — pass `force=false`.**

- [ ] **Step 3: Rewire `runEndOfRunMemory` to `Pipeline.DistillTarget` under the shared lock**

Replace the two `o.distill(...)` call sites with `o.mem.DistillTarget(...)` + code publish. Because the live path writes the **real** target (not a staging copy for `--dry-run`), wrap the end-of-run passes in:

```go
lock, err := memorypipeline.AcquireMemoryLock(ctx, o.opts.MemoryDir)
if err != nil { o.reflectFailed("flow-memory", "lock", err); return }
defer lock.Close()
```

For each target: `art, err := o.mem.DistillTarget(ctx, ...)`; if `art.NoHigh` → `reflectNotice`; else read `art.CandidatePath` and `memory.AtomicWrite(realTarget, candidate)`. This preserves the current "update writes the real file" behavior but now goes through staging+validation. Keep `reflectMu` around the shared-file writes (the in-process serialization) in addition to the interprocess lock — they guard different scopes (goroutines vs processes). Keep `finalReflectDone` guard and `if o.opts.Memory.Commit { memory.Commit(...) }`.

- [ ] **Step 4: Update injection in tests**

`reflection_test.go`/`memory_integration_test.go` inject via `o.memRunner` (the `AgentRunner` seam) and/or a test `*memorypipeline.Pipeline` built with `WithRunner`. Update `stubMemoryAgentByKind` to produce outputs at `spec.Out`/`spec.DatasetOut`/`spec.TargetFile` (staging paths) — the stub now writes the candidate, and the orchestrator publishes it. Adjust assertions that previously checked the real target was written by the agent: now the agent writes staging, the orchestrator publishes.

- [ ] **Step 5: Run the full orchestrator suite + race**

Run:
```
go test ./pkg/orchestrator/... -v
go test -race ./pkg/orchestrator/...
```
Expected: PASS. Regression focus: a normal flow still auto-creates per-stage files AND `memory.md`; `memory.mode: r` writes no `memory.md` but still builds per-stage files; `commit` gate unchanged.

- [ ] **Step 6: Lint + commit**

```bash
make lint-ci
git add pkg/orchestrator/reflection.go pkg/orchestrator/orchestrator.go pkg/orchestrator/reflection_test.go pkg/orchestrator/memory_integration_test.go
git commit -m "refactor(memory): live orchestrator использует общий memorypipeline и memory-lock"
```

---

## Task 9: Full CLI `RunE` — preflight, resolution, execution, output

**Files:**
- Modify: `cmd/afm/memory_rebuild.go` (replace `rebuildHandler` body)
- Modify: `cmd/afm/memory_rebuild_test.go`
- Modify: `cmd/afm/main.go` if any shared helper is needed

**Interfaces:**
- Consumes: `config.Load*`, `flow.ParseFile`, `resolveFlowPath`, `state.FindLatestCompletedRunDir`/`resolveExplicitRunDir`/`TryLockRun`/`LoadRunState`, `loadPrompts`, `memorypipeline.New`/`Rebuild`, the root/memory-dir helpers (extracted in Task 11 — until then, inline resolution matching run.go:207-225).
- Produces: the real `rebuildHandler`.

- [ ] **Step 1: Write failing integration-style tests (no real agents)**

Use the `AgentRunner` seam by exposing a package-level override in `cmd/afm` for tests, e.g. `var newRebuildPipeline = func(p memorypipeline.Prompts, a memorypipeline.AgentConfig) *memorypipeline.Pipeline { return memorypipeline.New(p, a) }`, and in tests replace it with `memorypipeline.New(p, a, memorypipeline.WithRunner(stub))`. Cover:

```go
func TestRebuild_MissingMemoryConfig(t *testing.T) {
    // flow without memory.path → error before any agent, exit non-zero
}
func TestRebuild_StageSetMismatch(t *testing.T) {
    // completed run has stages {a,b}; current flow has {a,c} → error listing
    // missing:[c] extra-in-run:[b] (sorted)
}
func TestRebuild_ActiveRunLocked(t *testing.T) {
    // hold TryLockRun on the target run dir → command fails with ErrRunLocked message
}
func TestRebuild_DryRunNoWriteNoFSMChange(t *testing.T) {
    // capture events.jsonl + state.json bytes before; run --dry-run with stub runner;
    // assert byte-for-byte unchanged, no target files created.
}
func TestRebuild_PersistentDirFlag(t *testing.T) {
    // --dir <fixture> resolves runsDir under it
}
```

- [ ] **Step 2: Run, verify fail; implement the handler**

`rebuildHandler` body (pseudocode filled with real calls):

```go
rebuildHandler = func(ctx context.Context, opts rebuildOptions) error {
    ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
    defer stop()

    cfg, err := loadConfig()               // same loader run.go uses
    if err != nil { return err }
    flowPath, err := resolveFlowPath([]string{opts.FlowArg}) // handles empty arg
    if err != nil { return err }
    f, err := flow.ParseFile(flowPath)
    if err != nil { return err }
    if !f.MemoryEnabled() {
        return fmt.Errorf("memory rebuild: flow %q has no memory.path — nothing to build", f.Name)
    }
    if !hasWritableTarget(f) { // any non-script write-reflect stage, or project write
        return fmt.Errorf("memory rebuild: no writable memory target (need reflect.mode w|rw or memory.mode w|rw)")
    }

    // resolve run dir
    var runDir string
    if opts.RunID != "" {
        runDir, err = resolveExplicitRunDir(runsDir(), f.Name, opts.RunID)
    } else {
        runDir, err = state.FindLatestCompletedRunDir(runsDir(), f.Name)
    }
    if err != nil { return err }

    // run lock (strict — do NOT wait)
    lock, err := state.TryLockRun(runDir)
    if err != nil {
        if errors.Is(err, state.ErrRunLocked) {
            return fmt.Errorf("run %s is active (afm run in progress); stop it or wait", filepath.Base(runDir))
        }
        return err
    }
    defer lock.Close()

    rs, err := state.LoadRunState(runDir)
    if err != nil { return err }
    if !rs.AllDone() { return fmt.Errorf("run %s is not fully done; only completed runs can be rebuilt", filepath.Base(runDir)) }
    if err := checkStageSetMatches(rs, f); err != nil { return err } // sorted missing/extra lists

    // resolve roots + memory dir (Task 11 helpers; inline until then)
    agentRoot, err := resolveAgentRoot(rootDir, f)
    if err != nil { return err }
    memDir, err := resolveMemoryDir(rootDir, agentRoot, f)
    if err != nil { return err }
    if err := verifyRootAndMemory(agentRoot, memDir); err != nil { return err }

    prompts := loadPrompts(cfg.PromptsDir) // orchestrator.Prompts
    pipe := newRebuildPipeline(
        memorypipeline.Prompts{Reflect: prompts.Reflect, Aggregate: prompts.Aggregate, Prioritize: prompts.Prioritize, Update: prompts.Update},
        memorypipeline.AgentConfig{
            Command: cfg.Client.Command, ExtraArgs: cfg.Client.ExtraArgs,
            WrapperDir: wrapperDirFor(cfg.Client.Command, "", nil), // see Task 11 for generated wrappers
            RootDir: agentRoot, RunDir: runDir,
            IdleTimeout: cfg.Executor.IdleTimeout, Debug: debugEnabled,
        })

    // unique attempt workspace
    work := filepath.Join(runDir, "memory-rebuild", newAttemptID()) // <ts>-<rand4>
    if err := os.MkdirAll(work, 0755); err != nil { return err }

    fmt.Printf("using current flow definition and current prompts for historical run %s\n", filepath.Base(runDir))
    printPreflight(f, opts) // expected LLM calls: missing/forced reflects + 3 per distill target

    report, err := pipe.Rebuild(ctx, memorypipeline.RebuildRequest{
        RunID: filepath.Base(runDir), RunDir: runDir, MemoryDir: memDir, WorkDir: work,
        Stages: f.Stages, Memory: f.Memory,
        ForceReflect: opts.ForceReflect, DryRun: opts.DryRun,
        StartedAt: nowRFC3339(),
    })
    if err != nil {
        return fmt.Errorf("memory rebuild failed (see %s): %w", work, err)
    }
    printReport(report, opts.DryRun) // reused/generated, changed/unchanged, no-high, unified diffs in dry-run

    // commit (Task 10)
    if !opts.DryRun {
        if err := maybeCommit(memDir, report, effectiveCommit(opts, f)); err != nil { return err }
    }
    return nil
}
```

Implement the small helpers: `hasWritableTarget`, `checkStageSetMatches` (sorted lists), `verifyRootAndMemory`, `newAttemptID` (`<YYYYMMDD-HHMMSS>-<2 hex>` mirroring `newRunID`), `printPreflight`, `printReport` (unified diff via a minimal differ or the `pkg/server/workspace` `go-udiff` dependency already in the module — reuse it), `effectiveCommit` (`--commit`→true, `--no-commit`→false, else `f.Memory.Commit`, `--dry-run`→false).

**Guarantee: stdout prints no prompt/dialog/secret** — `printReport` shows only counts, target paths, and diffs of memory files (which are user rules, not secrets). No agent stdout is echoed.

- [ ] **Step 3: Run tests, verify they pass**

Run: `go test ./cmd/afm -run TestRebuild -v`
Expected: PASS.

- [ ] **Step 4: Add the no-FSM-mutation assertion test**

Verify `events.jsonl`, `state.json`, and the stage statuses/notices are byte-for-byte unchanged after a real (stubbed-agent) rebuild — this is the headline acceptance criterion.

- [ ] **Step 5: Lint + commit**

```bash
make lint-ci
git add cmd/afm/memory_rebuild.go cmd/afm/memory_rebuild_test.go cmd/afm/main.go
git commit -m "feat(memory-rebuild): полный RunE команды с preflight, locks и strict-выводом"
```

---

## Task 10: Git commit — changed-paths only + overrides

**Files:**
- Modify: `pkg/memory/commit.go` (add `CommitPaths`)
- Modify: `pkg/memory/commit_test.go`
- Modify: `cmd/afm/memory_rebuild.go` (`maybeCommit` + preflight)

**Interfaces:**
- Produces: `func CommitPaths(dir, message string, paths []string) (committed bool, err error)` — `git -C dir add <paths...>` then `git -C dir commit -m msg -- <paths...>`.
- Produces (cmd/afm): `maybeCommit`, `commitPreflight`.

- [ ] **Step 1: Write failing tests for `CommitPaths`**

```go
func TestCommitPaths_OnlyGivenPaths(t *testing.T) {
    dir := gitInit(t)
    writeFile(t, dir, "memory.md", "rules")
    writeFile(t, dir, "unrelated.txt", "other")
    committed, err := CommitPaths(dir, "chore(memory): rebuild from r1", []string{"memory.md"})
    if err != nil || !committed { t.Fatalf("commit failed: %v committed=%v", err, committed) }
    // unrelated.txt must remain untracked/uncommitted
    if isTracked(t, dir, "unrelated.txt") { t.Fatal("unrelated file was committed") }
}

func TestCommitPaths_NothingToCommit(t *testing.T) {
    dir := gitInit(t)
    writeFile(t, dir, "memory.md", "rules"); CommitPaths(dir, "m", []string{"memory.md"})
    committed, err := CommitPaths(dir, "m2", []string{"memory.md"})
    if err != nil || committed { t.Fatalf("expected no-op, got committed=%v err=%v", committed, err) }
}
```

- [ ] **Step 2: Run, verify fail; implement `CommitPaths`**

```go
func CommitPaths(dir, message string, paths []string) (bool, error) {
    if len(paths) == 0 { return false, nil }
    args := append([]string{"-C", dir, "add", "--"}, paths...)
    if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
        return false, fmt.Errorf("git add: %v: %s", err, out)
    }
    // anything staged among these paths?
    diffArgs := append([]string{"-C", dir, "diff", "--cached", "--quiet", "--"}, paths...)
    if err := exec.Command("git", diffArgs...).Run(); err == nil {
        return false, nil // exit 0 → nothing staged
    }
    commitArgs := append([]string{"-C", dir, "commit", "-m", message, "--"}, paths...)
    if out, err := exec.Command("git", commitArgs...).CombinedOutput(); err != nil {
        return false, fmt.Errorf("git commit: %v: %s", err, out)
    }
    return true, nil
}
```

- [ ] **Step 3: Write failing tests for the commit preflight + override matrix (cmd/afm)**

```go
func TestEffectiveCommit(t *testing.T) {
    // matrix: --commit, --no-commit, neither+flow.commit true/false, dry-run always false
}
func TestCommitPreflight_RejectsPreStagedMemory(t *testing.T) {
    // pre-stage a change under memory dir → commitPreflight returns error advising --no-commit
}
func TestMaybeCommit_ReportsPublishedButCommitFailed(t *testing.T) {
    // CommitPaths returns error → maybeCommit wraps with "memory published, commit not created"
}
```

- [ ] **Step 4: Run, verify fail; implement `maybeCommit`/`commitPreflight`/`effectiveCommit`**

`commitPreflight(memDir)`: verify memDir inside a git worktree (`git -C memDir rev-parse --show-toplevel`), and `git -C memDir diff --cached --quiet -- <memDir>` is clean; else error. Call it in the handler **before** the expensive agent calls when `effectiveCommit` is true. `maybeCommit` collects changed target paths from the `Report`, calls `CommitPaths`, and on error returns `fmt.Errorf("memory files updated but commit failed: %w", err)` (non-zero, explicit).

- [ ] **Step 5: Run tests, verify they pass**

Run: `go test ./pkg/memory ./cmd/afm -run 'Commit|Rebuild' -v`
Expected: PASS.

- [ ] **Step 6: Lint + commit**

```bash
make lint-ci
git add pkg/memory/commit.go pkg/memory/commit_test.go cmd/afm/memory_rebuild.go cmd/afm/memory_rebuild_test.go
git commit -m "feat(memory-rebuild): commit только изменённых memory-путей и override-матрица"
```

---

## Task 11: Shared CLI helpers + Docker for rebuild + path-safety in `flow.validate`

**Files:**
- Create: `cmd/afm/agent_environment.go` (extracted helpers)
- Create: `cmd/afm/agent_environment_test.go`
- Modify: `cmd/afm/run.go` (use extracted helpers — behavior unchanged)
- Modify: `cmd/afm/memory_rebuild.go` (Docker re-exec + wrapper lifecycle)
- Modify: `pkg/docker/launcher.go` (nil-flow support in `UsedRecipes`/`ScanCommands`/`UsesCodex` or dedicated command-list helpers)
- Modify: `pkg/flow/flow.go` + `pkg/flow/flow_test.go` (path-safety validation — breaking)

**Interfaces:**
- Produces: `resolveAgentRoot(rootDir string, f *flow.Flow) (string, error)`, `resolveMemoryDir(afmRoot, agentRoot string, f *flow.Flow) (string, error)`.
- Produces: Docker command-list helpers accepting the memory-agent command only (no stage commands), and nil-flow tolerance in `pkg/docker`.
- Modifies: `flow.validate()` to reject unsafe `reflect.file`/paths.

- [ ] **Step 1: Extract root/memory-dir helpers with a behavior-preserving test**

Move run.go:207-225 logic into `resolveAgentRoot`/`resolveMemoryDir`. Test that for representative flows (relative root_dir, absolute root_dir, empty root_dir, relative/absolute memory.path) the resolved values equal what run.go computed before. Then call the helpers from `run.go`'s RunE.

Run: `go test ./cmd/afm -run 'AgentRoot|MemoryDir|Resolve' -v` → PASS.

- [ ] **Step 2: Regression — `afm run` unchanged**

Add/keep a test (or run existing `cmd/afm` run tests) proving argument/mount computation for `afm run` is unchanged. Run: `go test ./cmd/afm -v`. Expected: PASS.

- [ ] **Step 3: Path-safety validation in `flow.validate` (breaking)**

Write failing tests in `pkg/flow/flow_test.go`:

```go
func TestValidate_ReflectFilePathSafety(t *testing.T) {
    bad := []string{"../escape.md", "/abs/mem.md", "a/../../b.md", "memory.md"}
    for _, rf := range bad {
        f := minimalFlowWithReflect(rf) // memory.path set, one stage reflect{file: rf, mode: rw}
        if err := f.validate(); err == nil { t.Errorf("reflect.file %q should be rejected", rf) }
    }
    good := minimalFlowWithReflect("sub/dir/stage.md")
    if err := good.validate(); err != nil { t.Errorf("safe reflect.file rejected: %v", err) }
}
```

Implement in the memory block of `validate()` (flow.go ~611-635): for each stage with `Reflect != nil`, require `filepath.IsLocal(Reflect.File)` and reject `Reflect.File == "memory.md"` (collision with project target) with a clear message. Keep existing checks.

Run: `go test ./pkg/flow -v` → PASS.

- [ ] **Step 4: Docker nil-flow helpers**

In `pkg/docker`, make `UsedRecipes`/`ScanCommands`/`UsesCodex` accept a `nil` flow (they must not deref it) OR add dedicated helpers taking an explicit command list. Add unit tests passing `nil` flow + the single global memory-agent command, asserting only that command's mounts/recipes/secrets are produced. Run: `go test ./pkg/docker -v` → PASS.

- [ ] **Step 5: Docker re-exec in `memory_rebuild.go`**

Mirror run.go:74-146 but: no dashboard port, no browser opener, no file-browser manifest; command list = `[cfg.Client.Command]` only; pass original args + absolute `--dir`; gate on `cfg.Docker.IsDockerEnabled() && os.Getenv("AFM_IN_DOCKER") != "1"`, and re-exec **before** acquiring locks. Preflight (in-container-reachability): verify `runDir`, `agentRoot`, `memDir`, `flowPath`, and `cfg.PromptsDir` resolve inside the container; an external absolute `root_dir` not covered by a mount → fail fast with the path. Reuse `CheckClaudeDockerAuth`, auto-shim validation, generated wrappers, Codex OAuth mount, secret filtering exactly as run.go.

Add a test asserting re-exec is skipped when `AFM_IN_DOCKER=1` and that only the global command feeds the mount/recipe computation.

- [ ] **Step 6: Wire generated-wrapper dir into the pipeline's `AgentConfig.WrapperDir`**

When Docker/auto-shim generates a wrapper for the memory-agent command, compute its dir and set `AgentConfig.WrapperDir` (via `wrapperDirFor(cfg.Client.Command, generatedDir, generatedAgents)`), matching how run.go wires it for the orchestrator. Clean up the generated wrapper dir on exit.

- [ ] **Step 7: Run tests + race**

Run:
```
go test ./cmd/afm ./pkg/docker ./pkg/flow -v
go test -race ./cmd/afm
```
Expected: PASS.

- [ ] **Step 8: Lint + commit**

```bash
make lint-ci
git add cmd/afm/agent_environment.go cmd/afm/agent_environment_test.go cmd/afm/run.go cmd/afm/memory_rebuild.go pkg/docker/ pkg/flow/flow.go pkg/flow/flow_test.go
git commit -m "feat(memory-rebuild): общие CLI-хелперы, Docker re-exec и path-safety валидация"
```

---

## Task 12: Documentation

**Files:**
- Modify: `README.md`
- Modify: `AGENTS.md`

**Steps:**

- [ ] **Step 1: README — command reference**

Add an `afm memory rebuild` section under Agent Memory: examples for all flags, a flags table, the effective-commit rule, and the dry-run/audit-workspace explanation.

- [ ] **Step 2: README — semantics + breaking change**

Document: "current YAML + current prompts analyze historical logs", the exact-stage-set requirement, completed-only rule, dataset reuse + `--force-reflect`, the two locks (run + shared memory) and the no-FSM-mutation guarantee, and the **breaking path-safety validation** (unsafe `reflect.file`/`reflect.file: memory.md` now rejected by `afm run` too).

- [ ] **Step 3: AGENTS.md — architecture note**

Add a subsection to the Agent Memory area describing `pkg/memorypipeline` (the extracted engine shared by live + offline), the attempt-workspace/manifest/staging-publish model, the shared memory lock (mandatory for all writers, `~/.afm/locks/memory-<sha256>.lock`, run→memory order), and that the explicit command is strict while the live path stays best-effort.

- [ ] **Step 4: Final full verification**

Run the whole matrix (spec §16):
```bash
go test ./pkg/state ./pkg/memory ./pkg/memorypipeline ./cmd/afm
go test -race ./pkg/orchestrator/... ./pkg/memorypipeline/... ./cmd/afm
go test ./... -race
make lint-ci
go run ./cmd/afm memory rebuild --help
```
Expected: all green; `--help` shows the command.

- [ ] **Step 5: Commit**

```bash
git add README.md AGENTS.md
git commit -m "docs(memory-rebuild): README и AGENTS для команды memory rebuild"
```

---

## Manual smoke test (after Task 12; not a code task)

Follow spec §15 / the source doc §15:
1. Run a small flow **without** memory.
2. Add `memory.path`, `memory.mode: rw`, `reflect:{file, mode: rw}` to the same YAML.
3. `--dry-run`: verify diff, no target files.
4. Normal rebuild: verify canonical datasets, per-stage files, `memory.md`.
5. Re-run: dataset reused, no duplicate patterns, empty diff.
6. `--force-reflect`: new attempt, replacement only after full success.
7. With a live `afm run` on another run of the same memory dir: rebuild allowed, shared memory updates serialized.
8. Rebuild an active run → immediate clear error.
9. Repeat in Docker with default Claude and one generated recipe / Codex variant.

---

## Self-review notes (author checklist — done)

- **Spec coverage:** §2 command → Tasks 1,9; §4 extraction → Task 3; §5 resolver+run lock → Tasks 1,2; §6 sources → Task 4; §7 dataset reuse/validation → Tasks 4,6; §8 workspace/manifest/publish + AtomicWrite durability → Tasks 5,6; §9 strict errors → Task 9; §10 shared lock → Tasks 7,8; §11 commit → Task 10; §12 Docker/root_dir/prompts → Tasks 9,11; §13 path safety → Task 11; §14 deferred → not built (correct). All spec sections map to a task.
- **Placeholder scan:** no "TBD"/"handle errors"/"similar to" — every code step has concrete code; the `<module>` token in imports is a deliberate instruction to use the repo's real module path (resolve with `head -1 go.mod`).
- **Type consistency:** `AgentSpec`/`AgentRunner`/`Prompts`/`AgentConfig`/`Pipeline`/`DistillTarget`/`StepArtifacts`/`RebuildRequest`/`Report` names are used consistently across Tasks 3–9; `CaptureStage`/`DistillTarget`/`Rebuild`/`SelectHigh`/`ValidateDataset`/`ValidateRules`/`AtomicWrite`/`CommitPaths`/`TryLockRun`/`AcquireMemoryLock`/`FindLatestCompletedRunDir`/`resolveExplicitRunDir` match their definitions and call sites.
