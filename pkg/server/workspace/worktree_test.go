//go:build linux

package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// requireGit skips the test when git is not on PATH — the git worktree tests
// exercise a real subprocess, unlike most of this package's pure-logic tests.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
}

// addWorktree runs `git -C root worktree add <rel> -b <branch>`, failing the
// test on error.
func addWorktree(t *testing.T, root, rel, branch string) {
	t.Helper()
	gitRun(t, root, "worktree", "add", filepath.FromSlash(rel), "-b", branch)
}

// TestDiff_InsideLinkedWorktree proves Diff addresses a file inside a linked
// git worktree (`.worktrees/<flow>/...`) and diffs it against the worktree's
// OWN HEAD, not the main checkout's.
func TestDiff_InsideLinkedWorktree(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	gitInit(t, root)
	writeFile(t, root, "main.go", "package main\n\nfunc main() {}\n")
	gitCommitAll(t, root, "init")

	addWorktree(t, root, ".worktrees/myflow", "myflow")
	writeFile(t, root, ".worktrees/myflow/main.go", "package main\n\nfunc main() { println(\"hi\") }\n")

	fs := newFS(t, Root{ID: "project", Path: root, Kind: "project"})
	defer fs.Close()

	d, err := fs.Diff(context.Background(), "project", ".worktrees/myflow/main.go")
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if d.Status != statusModified {
		t.Fatalf("status = %q, want %q; diff=%+v", d.Status, statusModified, d)
	}
	if !strings.Contains(d.Diff, `println("hi")`) {
		t.Fatalf("diff body missing the worktree-local edit: %q", d.Diff)
	}
}

// TestDiff_InsideLinkedWorktreeOnDivergedBranch commits an extra change ON the
// worktree branch, then edits the file again without committing. The baseline is
// the branch's FORK POINT (merge-base with the main checkout), so BOTH the
// committed "marker" change and the uncommitted "final" change show — this is the
// "vs base" behavior: work the agent already committed on the branch stays visible.
func TestDiff_InsideLinkedWorktreeOnDivergedBranch(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	gitInit(t, root)
	writeFile(t, root, "main.go", "package main\n\nfunc main() {}\n")
	gitCommitAll(t, root, "init") // the fork point

	addWorktree(t, root, ".worktrees/myflow", "myflow")
	wtDir := filepath.Join(root, ".worktrees", "myflow")

	// Commit something extra ON the worktree branch — committed flow work.
	writeFile(t, root, ".worktrees/myflow/main.go", "package main\n\nfunc main() {\n\tprintln(\"marker\")\n}\n")
	gitCommitAll(t, wtDir, "marker on worktree branch")

	// Now modify the file again, without committing.
	writeFile(t, root, ".worktrees/myflow/main.go", "package main\n\nfunc main() {\n\tprintln(\"marker\")\n\tprintln(\"final\")\n}\n")

	fs := newFS(t, Root{ID: "project", Path: root, Kind: "project"})
	defer fs.Close()

	d, err := fs.Diff(context.Background(), "project", ".worktrees/myflow/main.go")
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if d.Status != statusModified {
		t.Fatalf("status = %q, want %q; diff=%+v", d.Status, statusModified, d)
	}
	// Both the committed and the uncommitted branch work must appear (baseline = fork point).
	if !strings.Contains(d.Diff, `println("marker")`) {
		t.Fatalf("diff body missing the COMMITTED branch change (baseline should be the fork, not the branch tip): %q", d.Diff)
	}
	if !strings.Contains(d.Diff, `println("final")`) {
		t.Fatalf("diff body missing the uncommitted edit: %q", d.Diff)
	}
	if d.Baseline == "HEAD" || d.Baseline == "" {
		t.Fatalf("baseline must be the worktree fork point, not %q", d.Baseline)
	}
}

// TestDiff_IgnoresAmbientGitEnv is the Diff-side mirror of
// TestChanges_IgnoresAmbientGitEnv: a linked worktree's `.git` file points at
// `<root>/.git/worktrees/<flow>`, whose `commondir` file in turn points back
// at `<root>/.git` for the shared object store. If runGit's environment were
// not sanitized, an ambient GIT_COMMON_DIR would override that redirection
// and the diff would fail (or silently use the wrong object store).
func TestDiff_IgnoresAmbientGitEnv(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	gitInit(t, root)
	writeFile(t, root, "main.go", "package main\n")
	gitCommitAll(t, root, "init")

	addWorktree(t, root, ".worktrees/myflow", "myflow")
	writeFile(t, root, ".worktrees/myflow/main.go", "package main\n// edit\n")

	t.Setenv("GIT_COMMON_DIR", "/nonexistent")

	fs := newFS(t, Root{ID: "project", Path: root, Kind: "project"})
	defer fs.Close()

	d, err := fs.Diff(context.Background(), "project", ".worktrees/myflow/main.go")
	if err != nil {
		t.Fatalf("Diff under ambient GIT_COMMON_DIR: %v", err)
	}
	if d.Status != statusModified || !strings.Contains(d.Diff, "edit") {
		t.Fatalf("diff wrong under ambient GIT_COMMON_DIR: %+v", d)
	}
}

// TestList_ShowsWorktreeButHidesItsGitFile lists a linked worktree directory
// and asserts its tracked file is visible while its `.git` entry — a FILE
// here, not a directory — is hidden exactly like a normal repo's `.git` dir.
func TestList_ShowsWorktreeButHidesItsGitFile(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	gitInit(t, root)
	writeFile(t, root, "main.go", "package main\n")
	gitCommitAll(t, root, "init")
	addWorktree(t, root, ".worktrees/myflow", "myflow")

	// Confirm the fixture actually has a `.git` FILE (not a directory) before
	// asserting anything about hiding it.
	info, err := os.Lstat(filepath.Join(root, ".worktrees", "myflow", ".git"))
	if err != nil {
		t.Fatalf("lstat worktree .git: %v", err)
	}
	if info.IsDir() {
		t.Fatal("expected the linked worktree's .git to be a file, got a directory")
	}

	fs := newFS(t, Root{ID: "project", Path: root, Kind: "project"})
	defer fs.Close()

	p, err := fs.List(context.Background(), "project", ".worktrees/myflow", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var sawMainGo, sawGit bool
	for _, e := range p.Entries {
		switch e.Name {
		case "main.go":
			sawMainGo = true
		case ".git":
			sawGit = true
		default:
			// Other entries in the worktree don't matter for this assertion.
		}
	}
	if !sawMainGo {
		t.Fatalf("worktree listing missing main.go: %+v", p.Entries)
	}
	if sawGit {
		t.Fatalf("worktree listing must hide its .git file: %+v", p.Entries)
	}
}
