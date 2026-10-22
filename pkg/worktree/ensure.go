package worktree

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/akopichin/afm/pkg/fsutil"
	"github.com/akopichin/afm/pkg/gitcmd"
)

// ClearMode selects how aggressively Ensure resets an existing worktree.
type ClearMode int

const (
	ClearNone     ClearMode = iota // re-use an existing worktree as-is
	ClearWorktree                  // discard uncommitted work, keep branch and commits
	ClearAll                       // delete branch too, re-create from HEAD
)

// pathState is the three-way classification of p.Path from step 4 of the
// algorithm: decide before touching anything, so a clear flag can never
// destroy a worktree the conflict check is supposed to refuse.
type pathState int

const (
	stateAbsent pathState = iota
	stateOurs
	stateConflict
)

// checkoutTimeout bounds `worktree add`/`worktree remove`, which do a full file checkout/teardown and
// can take longer than gitcmd.DefaultTimeout on a large repo — unlike this package's other calls
// (prune/list/rev-parse/branch -D), which are cheap metadata operations.
const checkoutTimeout = 2 * time.Minute

// Ensure makes p.Path a worktree of p.Branch, creating it when absent and
// re-using it when it is already exactly that. Any other on-disk state is a
// conflict and returns an error instead of being silently repaired.
func Ensure(ctx context.Context, p Plan, mode ClearMode) error {
	// Step 1: p.Base must be a git repository at all.
	if _, ok, err := gitcmd.Run(ctx, p.Base, "rev-parse", "--git-common-dir"); err != nil || !ok {
		return fmt.Errorf("worktree: %s is not a git repository", p.Base)
	}

	// Step 2: prune stale registrations. Best-effort cleanup, not critical path.
	if _, ok, err := gitcmd.Run(ctx, p.Base, "worktree", "prune"); err != nil || !ok {
		log.Printf("WARN: worktree prune in %s failed (continuing)", p.Base)
	}

	// Step 3: read current state.
	listOut, ok, err := gitcmd.Run(ctx, p.Base, "worktree", "list", "--porcelain")
	if err != nil || !ok {
		return fmt.Errorf("worktree: failed to list worktrees in %s", p.Base)
	}
	entries := ParseList(listOut)

	_, branchExists, err := gitcmd.Run(ctx, p.Base, "rev-parse", "--verify", "--quiet", "refs/heads/"+p.Branch)
	if err != nil {
		return fmt.Errorf("worktree: failed to check branch %q in %s", p.Branch, p.Base)
	}

	cleanPath := canonicalPath(p.Path)

	// Step 4: classify p.Path, and independently guard any other entry that
	// already owns p.Branch — in every mode, before any mutation.
	for _, e := range entries {
		if canonicalPath(e.Path) == cleanPath {
			continue
		}
		if e.Branch == p.Branch {
			return fmt.Errorf("worktree: branch %q is already checked out at %s; rename the flow or remove that worktree", p.Branch, e.Path)
		}
	}

	state, conflictDetail := classify(entries, cleanPath, p.Branch)
	if state == stateConflict {
		return fmt.Errorf("worktree: %s: %s", p.Path, conflictDetail)
	}

	// Step 5: clear modes reset only what afm owns.
	if mode != ClearNone {
		done, err := resetForClear(ctx, p, mode, state, branchExists)
		if err != nil {
			return err
		}
		if done {
			return finish(ctx, p)
		}
		// Falling through to step 6: nothing was left to reset, so there is
		// no branch left over either.
		branchExists = false
		state = stateAbsent
	}

	// Step 6: create or re-use.
	switch {
	case state == stateOurs:
		return finish(ctx, p)
	case branchExists:
		return fmt.Errorf("worktree: branch %q exists but is not checked out anywhere; re-attach it by hand (git worktree add %s %s), delete the branch, or re-run with --worktree-clear", p.Branch, p.Path, p.Branch)
	default:
		if err := os.MkdirAll(p.Dir, 0o755); err != nil {
			return fmt.Errorf("worktree: create %s: %w", p.Dir, err)
		}
		if _, ok, err := gitcmd.RunWithTimeout(ctx, checkoutTimeout, p.Base, "worktree", "add", "-b", p.Branch, p.Path); err != nil || !ok {
			return fmt.Errorf("worktree: failed to create worktree %s on branch %q", p.Path, p.Branch)
		}
		return finish(ctx, p)
	}
}

// canonicalPath приводит путь к виду, в котором его отдаёт `git worktree list`: git раскрывает симлинки
// (на macOS /var -> /private/var), а путь рана может прийти через симлинк. Сравнивать строки без этого —
// не узнать собственный worktree и отказать «branch already checked out». Ошибка — очищенный исходный путь.
func canonicalPath(p string) string {
	if c, err := fsutil.CanonicalPath(p); err == nil {
		return c
	}
	return filepath.Clean(p)
}

// classify reduces cleanPath to one of the three states from step 4, using
// entries already read from `git worktree list --porcelain` plus a disk stat
// for the "stray directory" case. It never mutates anything.
func classify(entries []Entry, cleanPath, branch string) (state pathState, conflictDetail string) {
	for i := range entries {
		e := &entries[i]
		if canonicalPath(e.Path) != cleanPath {
			continue
		}
		if e.Detached {
			return stateConflict, "registered as a detached worktree, not on a branch"
		}
		if e.Branch != branch {
			return stateConflict, fmt.Sprintf("registered on branch %q, not %q", e.Branch, branch)
		}
		return stateOurs, ""
	}

	// Not registered with git. A directory on disk that git does not know
	// about is a stray afm must never touch.
	if _, err := os.Stat(cleanPath); err == nil {
		return stateConflict, "exists on disk but is not a registered git worktree"
	}

	return stateAbsent, ""
}

// resetForClear implements step 5's table. done reports whether the cell
// finished the job itself (caller should go straight to finish); when done is
// false, the caller falls through to step 6 with branchExists now false.
func resetForClear(ctx context.Context, p Plan, mode ClearMode, state pathState, branchExists bool) (done bool, err error) {
	switch {
	case state == stateOurs:
		if err := removeWorktree(ctx, p); err != nil {
			return false, err
		}
		if mode == ClearAll {
			if err := deleteBranch(ctx, p); err != nil {
				return false, err
			}
			if _, ok, err := gitcmd.RunWithTimeout(ctx, checkoutTimeout, p.Base, "worktree", "add", "-b", p.Branch, p.Path); err != nil || !ok {
				return false, fmt.Errorf("worktree: failed to re-create worktree %s on branch %q", p.Path, p.Branch)
			}
			return true, nil
		}
		// ClearWorktree: branch was deliberately kept. -b would fail on an
		// existing branch, so re-attach without it.
		if _, ok, err := gitcmd.RunWithTimeout(ctx, checkoutTimeout, p.Base, "worktree", "add", p.Path, p.Branch); err != nil || !ok {
			return false, fmt.Errorf("worktree: failed to re-attach worktree %s to branch %q", p.Path, p.Branch)
		}
		return true, nil

	case state == stateAbsent && branchExists:
		if mode == ClearAll {
			if err := deleteBranch(ctx, p); err != nil {
				return false, err
			}
			return false, nil // fall through to step 6, branchExists now false
		}
		// ClearWorktree: re-attach the preserved branch.
		if _, ok, err := gitcmd.RunWithTimeout(ctx, checkoutTimeout, p.Base, "worktree", "add", p.Path, p.Branch); err != nil || !ok {
			return false, fmt.Errorf("worktree: failed to re-attach worktree %s to branch %q", p.Path, p.Branch)
		}
		return true, nil

	default: // absent, no branch: nothing to reset
		return false, nil
	}
}

// removeWorktree force-removes the worktree registered at p.Path and refuses
// to proceed if git left the directory behind — afm never force-deletes a
// directory itself.
func removeWorktree(ctx context.Context, p Plan) error {
	// --force twice: a single --force only overrides the dirty-working-tree check; removing a *locked*
	// worktree (e.g. `git worktree lock`, or git itself locking one it suspects is on a removable medium)
	// needs a second --force or git refuses with "cannot remove a locked working tree".
	if _, ok, err := gitcmd.RunWithTimeout(ctx, checkoutTimeout, p.Base, "worktree", "remove", "--force", "--force", p.Path); err != nil || !ok {
		return fmt.Errorf("worktree: failed to remove worktree %s", p.Path)
	}
	if _, statErr := os.Stat(p.Path); statErr == nil {
		return fmt.Errorf("worktree: %s still exists on disk after removal; afm will not force-delete it", p.Path)
	}
	return nil
}

// deleteBranch deletes p.Branch. By the time this is called, step 4 has
// already proved the branch is checked out nowhere else, so this should not
// fail; if it does anyway, wrap it rather than leaking raw git output (which
// gitcmd.Run does not even give us on ok==false).
func deleteBranch(ctx context.Context, p Plan) error {
	if _, ok, err := gitcmd.Run(ctx, p.Base, "branch", "-D", p.Branch); err != nil || !ok {
		return fmt.Errorf("worktree: failed to delete branch %q", p.Branch)
	}
	return nil
}

// finish runs the best-effort exclude step shared by every successful path
// and returns nil: EnsureExcluded failing must never fail Ensure.
func finish(ctx context.Context, p Plan) error {
	if err := EnsureExcluded(ctx, p.Base); err != nil {
		log.Printf("WARN: worktree: EnsureExcluded for %s: %v", p.Base, err)
	}
	return nil
}
