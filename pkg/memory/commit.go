package memory

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// Commit stages and commits only changes under dir, leaving staged files elsewhere untouched.
func Commit(dir, message string) (committed bool, err error) {
	// Step 1: Add all changes in the directory
	addCmd := exec.Command("git", "-C", dir, "add", ".")
	if err := addCmd.Run(); err != nil {
		return false, err
	}

	// Check staged changes only under dir: git diff --quiet returns 0 for no changes, 1 for a diff.
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

// CommitPaths stages and commits only the supplied paths, returning the SHA. Empty input/no diff is a
// no-op; other git failures are errors.
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
