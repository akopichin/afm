package memory

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommit_AddsAndCommitsOnlyOnChange(t *testing.T) {
	repo := t.TempDir()
	for _, args := range [][]string{{"init"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}} {
		c := exec.Command("git", args...)
		c.Dir = repo
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	memDir := filepath.Join(repo, "m")
	os.MkdirAll(memDir, 0755)
	if err := os.WriteFile(filepath.Join(memDir, "memory.md"), []byte("# Project rules\n"), 0644); err != nil {
		t.Fatal(err)
	}

	committed, err := Commit(memDir, "chore(memory): update project memory")
	if err != nil || !committed {
		t.Fatalf("first commit: committed=%v err=%v", committed, err)
	}
	// no change → committed=false
	committed, err = Commit(memDir, "chore(memory): update project memory")
	if err != nil || committed {
		t.Fatalf("second commit should be no-op: committed=%v err=%v", committed, err)
	}
}

func TestCommit_DoesNotSweepUnrelatedStagedFiles(t *testing.T) {
	// Regression test for pathspec scoping: ensure Commit(dir) doesn't sweep
	// unrelated staged files from elsewhere in the repo into the memory commit.
	repo := t.TempDir()
	for _, args := range [][]string{{"init"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}} {
		c := exec.Command("git", args...)
		c.Dir = repo
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}

	// Create memory directory with memory.md
	memDir := filepath.Join(repo, "m")
	os.MkdirAll(memDir, 0755)
	if err := os.WriteFile(filepath.Join(memDir, "memory.md"), []byte("# Project rules\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create an unrelated file outside memDir
	unrelatedPath := filepath.Join(repo, "other.txt")
	if err := os.WriteFile(unrelatedPath, []byte("unrelated content\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Stage both files
	addCmd := exec.Command("git", "add", ".")
	addCmd.Dir = repo
	if out, err := addCmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v %s", err, out)
	}

	// Commit memory directory (should only commit memory.md)
	committed, err := Commit(memDir, "chore(memory): initial memory")
	if err != nil || !committed {
		t.Fatalf("first commit: committed=%v err=%v", committed, err)
	}

	// Verify that other.txt is still staged (not swept into the commit)
	// by checking "git diff --cached --name-only"
	statusCmd := exec.Command("git", "diff", "--cached", "--name-only")
	statusCmd.Dir = repo
	output, err := statusCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git diff --cached: %v %s", err, output)
	}
	stagedFiles := string(output)
	if !strings.Contains(stagedFiles, "other.txt") {
		t.Errorf("other.txt should still be staged, but git status shows: %q", stagedFiles)
	}
	if strings.Contains(stagedFiles, "m/memory.md") {
		t.Errorf("m/memory.md should NOT be staged (already committed), but git status shows: %q", stagedFiles)
	}

	// Also verify the log shows only memory.md was committed (not other.txt)
	logCmd := exec.Command("git", "log", "--oneline", "-1", "--")
	logCmd.Dir = repo
	logOutput, err := logCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git log: %v %s", err, logOutput)
	}
	logLine := string(logOutput)
	if logLine == "" {
		t.Error("expected at least one commit in log")
	}
}
