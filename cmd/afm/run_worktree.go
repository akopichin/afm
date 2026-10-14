package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/docker"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/worktree"
)

// parentDir is the ".." path segment, named so goconst (min-occurrences: 2) does not flag its reuse
// across this package (isSubPath here and resolveExplicitRunDir in memory_rebuild.go).
const parentDir = ".."

// flowLocksDir holds one flock file per flow (worktree.FlowLock), separate from any run directory so
// it survives a run restarting into a fresh run dir.
func flowLocksDir() string {
	return filepath.Join(runsDir(), ".worktree-locks")
}

// worktreesEnabled resolves the effective git-worktree toggle: --worktree=<name> (if non-empty) > flow (if
// set) > env AFM_WORKTREES > config worktrees.enabled > default OFF. An explicit --worktree is an explicit
// opt-in that outranks every other layer, including a flow that turned worktrees off. Otherwise mirrors
// sideAgentEnabled's shape, but the default is inverted (worktrees are opt-in, the side agent is opt-out).
func worktreesEnabled(worktreeName string, cfg config.Config, f *flow.Flow) bool {
	if worktreeName != "" {
		return true
	}
	if f != nil && f.Worktrees != nil {
		return *f.Worktrees
	}
	return cfg.Worktrees.IsEnabled()
}

// runNameFor resolves the run directory prefix / flow-lock key: an explicit --worktree name when given,
// otherwise the flow's own name. f.Name itself is never changed by --worktree — the flow still shows up
// under its real name in lifecycle payloads, the dashboard, and memory config; this only decides the
// prefix of .afm/runs/<name>-... and the worktree's branch/directory name.
func runNameFor(worktreeName, flowName string) string {
	return cmp.Or(worktreeName, flowName)
}

// worktreeBase returns the project root the worktree anchors to: flow.root_dir resolved against afmRoot
// when set, otherwise afm-root itself. Unlike absoluteAgentRoot this never falls back to the process CWD —
// the worktree must live in the repository that owns .afm/, and afm-root (the parent of .afm/) is what
// AGENTS.md defines as that project root. absoluteAgentRoot serves a different purpose (giving agents
// *some* real CWD) and its os.Getwd() fallback would be the wrong anchor for a git worktree.
func worktreeBase(afmRoot string, f *flow.Flow) (string, error) {
	if f.RootDir == "" {
		return filepath.Abs(afmRoot)
	}
	base := f.RootDir
	if !filepath.IsAbs(base) {
		base = filepath.Join(afmRoot, base)
	}
	return filepath.Abs(base)
}

// clearModeFromFlags turns the two mutually exclusive CLI flags into a worktree.ClearMode. Extracted as its
// own function so the mutual-exclusion rule is unit-testable without going through cobra flag parsing.
func clearModeFromFlags(wantClear, clearForce bool) (worktree.ClearMode, error) {
	switch {
	case wantClear && clearForce:
		return worktree.ClearNone, errors.New("--worktree-clear and --worktree-clear-force are mutually exclusive")
	case clearForce:
		return worktree.ClearAll, nil
	case wantClear:
		return worktree.ClearWorktree, nil
	default:
		return worktree.ClearNone, nil
	}
}

// resolveWorktree prepares the flow's worktree on the host and returns its absolute path ("" when the
// feature is off), plus a lock the caller must hold for the matching duration described below.
//
// Serializing a run against a destructive clear needs a lock scoped to the FLOW, not to any run
// directory: resolveRun (run.go) starts a brand-new run directory whenever the latest one is locked, so
// a run can restart into a fresh directory mid-flight, and nothing about "which run directories exist"
// stays valid across that restart. worktree.FlowLock has no such snapshot to go stale.
//
//   - Disabled, or re-exec'd into Docker with the feature off: (_, nil, nil) — nothing to hold.
//   - Otherwise: the returned lock is non-nil and must be held by the caller for the run's ENTIRE
//     lifetime (released only when the run finishes), regardless of mode. A clear flag is never a
//     standalone operation — "Both additionally force a fresh run" — so the SAME invocation that clears
//     the worktree immediately starts using it; the lock guarding that use must already be held by the
//     time Ensure's destructive calls run, with no gap in between.
//
// Docker mode needs no special handling here: docker.ReExec (via execFunc) spawns `docker run` as an
// ordinary child process and blocks until it exits — it does NOT replace the host's process image — so
// RunE's `defer wtLock.Close()` already spans the container's entire execution with no handoff gap. The
// re-exec'd container's own call to this function (ReExecedIntoContainer branch) must NOT acquire the
// lock again: the host already holds it for the container's whole lifetime, and a second exclusive
// attempt on the same underlying (bind-mounted) lock file would deadlock against the host's own hold.
func resolveWorktree(ctx context.Context, afmRoot string, f *flow.Flow, cfg config.Config, mode worktree.ClearMode, worktreeName string) (string, *worktree.FlowLock, error) {
	if config.ReExecedIntoContainer() {
		return os.Getenv(docker.WorktreeEnvVar), nil, nil
	}
	if !worktreesEnabled(worktreeName, cfg, f) {
		if mode != worktree.ClearNone {
			return "", nil, errors.New("worktree: --worktree-clear requires worktrees to be enabled (set worktrees: true in flow.yaml, AFM_WORKTREES=1, or worktrees.enabled in config)")
		}
		return "", nil, nil
	}

	base, err := worktreeBase(afmRoot, f)
	if err != nil {
		return "", nil, err
	}

	// Docker containment guard: reexecFlow only bind-mounts filepath.Abs(afmRoot) as ProjectDir, at the
	// identical container path. An absolute root_dir pointing outside afm-root would put the worktree on a
	// path that simply does not exist in the container. This is a pre-existing root_dir+Docker limitation,
	// not one worktrees introduce — the guard just turns a silent failure into a fail-fast one.
	if cfg.Docker.IsDockerEnabled() {
		absAfmRoot, absErr := filepath.Abs(afmRoot)
		if absErr != nil {
			return "", nil, absErr
		}
		if !isSubPath(absAfmRoot, base) {
			return "", nil, fmt.Errorf("worktree: root_dir resolves to %q, which is outside the Docker-mounted project directory %q; move root_dir inside the project, or disable Docker", base, absAfmRoot)
		}
	}

	p, err := worktree.PlanFor(base, runNameFor(worktreeName, f.Name))
	if err != nil {
		return "", nil, err
	}

	// Acquired here, right before the first mutating git call, and held by the caller for the run that
	// follows — whether Ensure reused the worktree as-is or cleared it first. Keyed by p.Branch (the
	// worktree's effective name), the same source the error below names it from, so the lock and the
	// message it produces can never disagree.
	runLock, err := worktree.TryLockFlow(flowLocksDir(), p.Branch)
	if err != nil {
		return "", nil, flowLockError(err, p.Branch)
	}
	if err := worktree.Ensure(ctx, p, mode); err != nil {
		runLock.Close() //nolint:errcheck
		return "", nil, err
	}
	return p.Path, runLock, nil
}

// flowLockError turns worktree.ErrFlowLockBusy into an actionable message naming the worktree (the flow
// name by default, or an explicit --worktree override); any other error (e.g. failure to create the lock
// directory) passes through unwrapped.
func flowLockError(err error, name string) error {
	if errors.Is(err, worktree.ErrFlowLockBusy) {
		return fmt.Errorf("worktree: %q's worktree is in use by another `afm run` or `--worktree-clear`; wait a moment and retry", name)
	}
	return err
}

// isSubPath reports whether child is parent itself or lives somewhere underneath it.
func isSubPath(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel != parentDir && !strings.HasPrefix(rel, parentDir+string(filepath.Separator))
}
