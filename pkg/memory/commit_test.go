package memory

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initTestRepo initializes a git repo in a fresh t.TempDir() with a local
// user.email/name configured, so commits work without touching global git
// config. Returns the repo root.
func initTestRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	for _, args := range [][]string{{"init"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}} {
		c := exec.Command("git", args...)
		c.Dir = repo
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return repo
}

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

// --- CommitPaths -------------------------------------------------------

func TestCommitPaths_CommitsOnlyGivenPaths(t *testing.T) {
	repo := initTestRepo(t)

	// Two files in the repo: one we ask CommitPaths to commit, one we don't.
	wanted := filepath.Join(repo, "m", "memory.md")
	unrelated := filepath.Join(repo, "other.txt")
	if err := os.MkdirAll(filepath.Dir(wanted), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wanted, []byte("# rules\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unrelated, []byte("unrelated\n"), 0644); err != nil {
		t.Fatal(err)
	}

	committed, sha, err := CommitPaths(repo, "chore(memory): test", []string{wanted})
	if err != nil {
		t.Fatalf("CommitPaths: %v", err)
	}
	if !committed {
		t.Fatal("expected committed=true")
	}
	if sha == "" {
		t.Fatal("expected non-empty sha")
	}

	// wanted is committed; unrelated stays untracked.
	statusCmd := exec.Command("git", "status", "--porcelain")
	statusCmd.Dir = repo
	out, err := statusCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git status: %v %s", err, out)
	}
	status := string(out)
	if strings.Contains(status, "memory.md") {
		t.Errorf("memory.md should be committed, but still shows in status: %q", status)
	}
	if !strings.Contains(status, "other.txt") {
		t.Errorf("other.txt should remain untracked, but is missing from status: %q", status)
	}

	// sha matches HEAD.
	headCmd := exec.Command("git", "-C", repo, "rev-parse", "HEAD")
	headOut, err := headCmd.Output()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	if strings.TrimSpace(string(headOut)) != sha {
		t.Errorf("sha=%q does not match HEAD=%q", sha, strings.TrimSpace(string(headOut)))
	}
}

func TestCommitPaths_NothingToCommit(t *testing.T) {
	repo := initTestRepo(t)
	file := filepath.Join(repo, "m", "memory.md")
	if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("# rules\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// Commit it once so a second attempt with the same content has no diff.
	if _, _, err := CommitPaths(repo, "chore(memory): initial", []string{file}); err != nil {
		t.Fatalf("initial CommitPaths: %v", err)
	}

	committed, sha, err := CommitPaths(repo, "chore(memory): no-op", []string{file})
	if err != nil {
		t.Fatalf("CommitPaths: %v", err)
	}
	if committed || sha != "" {
		t.Fatalf("expected no-op commit, got committed=%v sha=%q", committed, sha)
	}
}

func TestCommitPaths_EmptyPaths(t *testing.T) {
	repo := initTestRepo(t)
	committed, sha, err := CommitPaths(repo, "chore(memory): nothing", nil)
	if err != nil || committed || sha != "" {
		t.Fatalf("want (false,\"\",nil) for empty paths, got (%v,%q,%v)", committed, sha, err)
	}
}

func TestCommitPaths_DedupesPaths(t *testing.T) {
	repo := initTestRepo(t)
	file := filepath.Join(repo, "m", "memory.md")
	if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("# rules\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// Same path given twice (plus an uncleaned variant) must not error out
	// from git seeing a duplicate pathspec.
	committed, _, err := CommitPaths(repo, "chore(memory): dedupe", []string{file, file, filepath.Dir(file) + "/./memory.md"})
	if err != nil {
		t.Fatalf("CommitPaths: %v", err)
	}
	if !committed {
		t.Fatal("expected committed=true")
	}
}

func TestCommitPaths_DiffLaunchErrorPropagates(t *testing.T) {
	// dir does not exist / is not a repo at all -> "git add" itself fails
	// (128), which must propagate as a real error, never as "has diff".
	nonRepo := t.TempDir()
	file := filepath.Join(nonRepo, "memory.md")
	if err := os.WriteFile(file, []byte("x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, _, err := CommitPaths(nonRepo, "chore(memory): fail", []string{file})
	if err == nil {
		t.Fatal("expected an error for a non-git directory")
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		t.Fatalf("launch/128 error must not be mistaken for exit-1 diff: %v", err)
	}
}
