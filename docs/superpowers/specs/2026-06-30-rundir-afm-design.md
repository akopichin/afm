# Design: `--dir` flag + rename `.flowManager` → `.afm`

**Date:** 2026-06-30
**Status:** Approved

---

## Problem

`.flowManager` always creates its working directory in the current project folder. Users can't specify an alternative location (e.g. `~/my-flows/` to keep flow state outside the project tree). Additionally, the directory name `.flowManager` is long and inconvenient.

---

## Goals

1. Rename the working directory from `.flowManager` to `.afm` everywhere.
2. Allow users to specify a custom parent directory via `--dir` CLI flag or `FLOWMANAGER_DIR` environment variable.
3. All subcommands respect the same setting.

---

## Non-Goals

- Migration of existing `.flowManager/` directories (out of scope; users rename manually).
- Renaming the binary or Go module path.

---

## Design

### Directory naming

The working directory `.flowManager` is renamed to `.afm` globally:

| Before | After |
|--------|-------|
| `.flowManager/flows/` | `.afm/flows/` |
| `.flowManager/runs/` | `.afm/runs/` |
| `.flowManager/config.yaml` | `.afm/config.yaml` |
| `~/.flowManager/config.yaml` | `~/.afm/config.yaml` |

This is a breaking change. Existing `.flowManager/` directories will not be picked up automatically. Users must rename them manually.

### `--dir` flag and `FLOWMANAGER_DIR` env variable

A persistent flag `--dir` is added to the root cobra command and propagated to all subcommands automatically.

**Priority (highest to lowest):**
1. `--dir` CLI flag
2. `FLOWMANAGER_DIR` environment variable
3. `.` (current working directory, same behaviour as before)

**Usage examples:**
```bash
flowmanager --dir ~/my-flows run flow.yaml
flowmanager --dir ~/my-flows check
FLOWMANAGER_DIR=~/my-flows flowmanager approve my-stage
```

The effective `.afm` directory is always `<dir>/.afm/`.

---

## Implementation

### `cmd/flowmanager/main.go`

Add a package-level variable `rootDir` and a helper `fmDir()`. Register the persistent flag and resolve env fallback in `PersistentPreRunE`:

```go
var rootDir string

func fmDir() string {
    return filepath.Join(rootDir, ".afm")
}

func newRootCmd() *cobra.Command {
    root := &cobra.Command{...}
    root.PersistentFlags().StringVar(&rootDir, "dir", "", "base directory for .afm (default: current dir)")
    root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
        if rootDir == "" {
            rootDir = os.Getenv("FLOWMANAGER_DIR")
        }
        if rootDir == "" {
            rootDir = "."
        }
        return nil
    }
    ...
}
```

### `cmd/flowmanager/*.go`

Replace all hardcoded `filepath.Join(".flowManager", ...)` with `filepath.Join(fmDir(), ...)`.

Affected files: `run.go`, `check.go`, `approve.go`, `revise.go`, `retry.go`, `init.go`, `list.go`.

The helper function `findLatestRunDir` in `approve.go` uses `fmDir()` instead of the hardcoded path.

### `pkg/state/state.go`

`FindLatestRunDir` currently hardcodes `.flowManager/runs`. Change signature to accept the base directory:

```go
// Before
func FindLatestRunDir(flowName string) (string, error)

// After
func FindLatestRunDir(base, flowName string) (string, error)
```

Callers in `run.go` and `approve.go` pass `fmDir()`.

### `pkg/config/config.go`

`Load()` hardcodes `~/.flowManager` and `.flowManager`. Since `LoadFrom(globalDir, projectDir)` already exists, update `Load()` to use `.afm`:

```go
func Load() (Config, error) {
    home, _ := os.UserHomeDir()
    return LoadFrom(
        filepath.Join(home, ".afm"),
        ".afm",
    )
}
```

In `run.go`, replace `config.Load()` with `config.LoadFrom(filepath.Join(home, ".afm"), fmDir())` so that `--dir` affects which project config is read.

### Other files

- `CLAUDE.md` — update all `.flowManager` references to `.afm`
- `README.md` — update directory references
- `config.example.yaml` — no changes needed (no `.flowManager` refs)
- `example-flow.yaml`, `example-flow-interactive.yaml` — update if they reference `.flowManager`

---

## Error handling

- If `--dir` points to a non-existent path that can't be created: propagate `os.MkdirAll` error.
- No explicit error if `.flowManager/` exists instead of `.afm/` — the old directory is simply ignored (no migration hint in v1).

---

## Testing

- Existing unit tests for `pkg/state` and `pkg/config` pass `base` parameter explicitly — no implicit global state in packages.
- Integration tests in `cmd/flowmanager` use `t.TempDir()` and `--dir` flag to avoid touching the real filesystem.
- `pkg/config` tests use `LoadFrom` (already parametric) — no changes needed.

---

## Affected files summary

| File | Change |
|------|--------|
| `cmd/flowmanager/main.go` | Add `rootDir`, `fmDir()`, persistent flag, `PersistentPreRunE` |
| `cmd/flowmanager/run.go` | Use `fmDir()` in `resolveFlowPath`, `resolveRun`; use `config.LoadFrom` |
| `cmd/flowmanager/check.go` | Use `fmDir()` |
| `cmd/flowmanager/approve.go` | Use `fmDir()` in `findLatestRunDir` |
| `cmd/flowmanager/revise.go` | No direct changes — inherits `fmDir()` via `findLatestRunDir` in `approve.go` |
| `cmd/flowmanager/retry.go` | No direct changes — inherits `fmDir()` via `findLatestRunDir` in `approve.go` |
| `cmd/flowmanager/init.go` | Use `fmDir()` |
| `cmd/flowmanager/list.go` | Use `fmDir()` |
| `pkg/state/state.go` | `FindLatestRunDir(base, flowName string)` |
| `pkg/config/config.go` | `Load()` uses `.afm` |
| `CLAUDE.md` | Update `.flowManager` → `.afm` |
| `README.md` | Update `.flowManager` → `.afm` |
