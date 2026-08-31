package memory

import (
	"os/exec"
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

	// Step 2: Check if there are staged changes
	// "git diff --cached --quiet" exits with 0 if no changes, 1 if there are changes
	diffCmd := exec.Command("git", "-C", dir, "diff", "--cached", "--quiet")
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

	// Step 3: Commit the changes
	commitCmd := exec.Command("git", "-C", dir, "commit", "-m", message)
	if err := commitCmd.Run(); err != nil {
		return false, err
	}

	return true, nil
}
