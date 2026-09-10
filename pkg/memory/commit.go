package memory

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// Commit adds and commits changes in the given directory.
// It runs:
// 1. git -C <dir> add . (to stage changes)
// 2. git -C <dir> diff --cached --quiet (to check if there are staged changes)
// 3. git -C <dir> commit -m message (if there are changes)
//
// Returns:
// - committed=true, err=nil if changes were committed
// - committed=false, err=nil if there were no changes to commit
// - committed=false, err=<error> if git operations failed
//
// Never pushes changes. Uses os/exec.
func Commit(dir, message string) (committed bool, err error) {
	// Step 1: Add all changes in the directory
	addCmd := exec.Command("git", "-C", dir, "add", ".")
	if err := addCmd.Run(); err != nil {
		return false, err
	}

	// Step 2: Check if there are staged changes under dir only
	// "git diff --cached --quiet -- ." exits with 0 if no changes, 1 if there are changes
	// The "-- ." pathspec ensures we only check changes under this directory
	diffCmd := exec.Command("git", "-C", dir, "diff", "--cached", "--quiet", "--", ".")
	err = diffCmd.Run()

	// If exit code is 0, there are no staged changes
	if err == nil {
		return false, nil
	}

	// Check if it's an exit code 1 (meaning there are staged changes)
	// Any other error is a real error
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		return false, err
	}

	// Step 3: Commit the changes under dir only
	// The "-- ." pathspec ensures we only commit changes under this directory
	commitCmd := exec.Command("git", "-C", dir, "commit", "-m", message, "--", ".")
	if err := commitCmd.Run(); err != nil {
		return false, err
	}

	return true, nil
}

// CommitPaths adds and, if there is a resulting diff, commits exactly the
// given paths under dir — narrower than Commit (which stages/commits the
// whole directory tree, `-- .`): the caller passes only the files it
// actually published this run, so an unrelated staged file elsewhere under
// dir is never swept into the commit.
//
// paths are deduped and filepath.Clean'd first. An empty result (nil/empty
// input) is a no-op: (false, "", nil).
//
// "git diff --cached --quiet -- <paths>" exit code is interpreted strictly:
// 0 means nothing staged changed (no-op, not an error); exactly 1 means
// there is a diff (proceed to commit); any other outcome — a non-1 exit
// code (typically 128, e.g. dir is not a git repo) or a launch failure —
// is a real error and is never mistaken for "has diff".
//
// On success with a real commit, the new commit SHA is returned. If the
// commit itself succeeds but `git rev-parse HEAD` fails to report it, that
// is returned as a strict error (the caller should not silently treat this
// as an unknown-SHA success).
func CommitPaths(dir, message string, paths []string) (committed bool, sha string, err error) {
	paths = dedupeCleanPaths(paths)
	if len(paths) == 0 {
		return false, "", nil
	}

	addArgs := append([]string{"-C", dir, "add", "--"}, paths...)
	if out, err := exec.Command("git", addArgs...).CombinedOutput(); err != nil {
		return false, "", fmt.Errorf("git add: %w: %s", err, out)
	}

	diffArgs := append([]string{"-C", dir, "diff", "--cached", "--quiet", "--"}, paths...)
	diffErr := exec.Command("git", diffArgs...).Run()
	if diffErr == nil {
		return false, "", nil // exit 0 -> nothing staged changed
	}
	var exitErr *exec.ExitError
	if !errors.As(diffErr, &exitErr) || exitErr.ExitCode() != 1 {
		// Non-1 exit code (e.g. 128) or a launch failure — a real error,
		// never treated as "has diff".
		return false, "", fmt.Errorf("git diff: %w", diffErr)
	}

	commitArgs := append([]string{"-C", dir, "commit", "-m", message, "--"}, paths...)
	if out, err := exec.Command("git", commitArgs...).CombinedOutput(); err != nil {
		return false, "", fmt.Errorf("git commit: %w: %s", err, out)
	}

	shaOut, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return true, "", fmt.Errorf("commit created but rev-parse HEAD failed: %w", err)
	}
	return true, strings.TrimSpace(string(shaOut)), nil
}

// dedupeCleanPaths filepath.Clean's each path and drops empties/duplicates,
// preserving first-seen order — a stable, minimal pathspec for git.
func dedupeCleanPaths(paths []string) []string {
	seen := make(map[string]bool, len(paths))
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if p == "" {
			continue
		}
		c := filepath.Clean(p)
		if seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	return out
}
