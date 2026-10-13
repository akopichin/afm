//go:build linux

package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// commitOn stages+commits everything in the given worktree dir on its current branch.
func commitOn(t *testing.T, dir, msg string) {
	t.Helper()
	gitCommitAll(t, dir, msg)
}

// TestDiff_WorktreeBaselineIsForkNotMainTip proves the per-file diff of a file inside a
// linked worktree baselines against the branch's FORK POINT, not the main checkout's
// moving HEAD. The agent's committed work must show; a change main made to the SAME
// file AFTER the fork must NOT leak in (an implementation that baselined on main HEAD
// would fail this).
func TestDiff_WorktreeBaselineIsForkNotMainTip(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	gitInit(t, root)
	writeFile(t, root, "pkg/foo.go", "base\n")
	gitCommitAll(t, root, "init")

	addWorktree(t, root, ".worktrees/myflow", "myflow")
	wt := filepath.Join(root, ".worktrees", "myflow")

	// Agent commits on the worktree branch.
	writeFile(t, root, ".worktrees/myflow/pkg/foo.go", "base\nAGENT_LINE\n")
	commitOn(t, wt, "agent work")

	// main changes the SAME file AFTER the fork — must not appear in the worktree diff.
	writeFile(t, root, "pkg/foo.go", "base\nMAIN_LINE\n")
	gitCommitAll(t, root, "main moves on")

	fs := newFS(t, Root{ID: "project", Path: root, Kind: "project"})
	defer fs.Close()

	d, err := fs.Diff(context.Background(), "project", ".worktrees/myflow/pkg/foo.go")
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if d.Status != statusModified {
		t.Fatalf("status = %q, want %q; diff=%+v", d.Status, statusModified, d)
	}
	if !strings.Contains(d.Diff, "AGENT_LINE") {
		t.Fatalf("diff must contain the worktree's committed line; got: %q", d.Diff)
	}
	if strings.Contains(d.Diff, "MAIN_LINE") {
		t.Fatalf("diff must NOT contain main's later change (baseline is the fork, not main HEAD); got: %q", d.Diff)
	}
	if d.Baseline == "HEAD" || d.Baseline == "" {
		t.Fatalf("Baseline must be the fork sha, not %q", d.Baseline)
	}
}

// TestDiff_WorktreeBaselineBinary checks the binary branch (diff.go) honors the worktree
// baseline rather than a hardcoded "HEAD".
func TestDiff_WorktreeBaselineBinary(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	gitInit(t, root)
	writeFile(t, root, "bin.dat", "a\x00b\x00c\n")
	gitCommitAll(t, root, "init")

	addWorktree(t, root, ".worktrees/myflow", "myflow")
	wt := filepath.Join(root, ".worktrees", "myflow")
	writeFile(t, root, ".worktrees/myflow/bin.dat", "a\x00b\x00CHANGED\x00\n")
	commitOn(t, wt, "agent binary change")

	fs := newFS(t, Root{ID: "project", Path: root, Kind: "project"})
	defer fs.Close()

	d, err := fs.Diff(context.Background(), "project", ".worktrees/myflow/bin.dat")
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !d.Binary {
		t.Fatalf("expected binary diff, got %+v", d)
	}
	if d.Status != statusModified {
		t.Fatalf("binary tracked-in-fork should be modified, got %q", d.Status)
	}
	if d.Baseline == "HEAD" || d.Baseline == "" {
		t.Fatalf("binary branch must report the worktree baseline, got %q", d.Baseline)
	}
}

// TestDiff_WorktreeBaselineOversized checks the oversized-baseline early return honors the
// worktree baseline. The FORK blob is >2 MiB (maxContentBytes) while the worktree's current
// file is small, so fs.Read succeeds but the baseline is oversized.
func TestDiff_WorktreeBaselineOversized(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	gitInit(t, root)
	big := strings.Repeat("x", (2<<20)+1024)
	writeFile(t, root, "big.txt", big+"\n")
	gitCommitAll(t, root, "init") // this init is the fork point; big.txt is >2 MiB here

	addWorktree(t, root, ".worktrees/myflow", "myflow")
	wt := filepath.Join(root, ".worktrees", "myflow")
	writeFile(t, root, ".worktrees/myflow/big.txt", "small\n") // worktree branch shrinks it and commits
	commitOn(t, wt, "agent shrinks big.txt")

	fs := newFS(t, Root{ID: "project", Path: root, Kind: "project"})
	defer fs.Close()

	d, err := fs.Diff(context.Background(), "project", ".worktrees/myflow/big.txt")
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !d.Truncated || d.Status != statusModified {
		t.Fatalf("expected oversized truncated/modified, got %+v", d)
	}
	if d.Baseline == "HEAD" || d.Baseline == "" {
		t.Fatalf("oversized branch must report the worktree baseline, got %q", d.Baseline)
	}
}

// TestDiff_NonWorktreeBaselineStaysHead — a plain repo (no linked worktree) with a
// committed file and no uncommitted edit stays clean with baseline HEAD. Guards the
// fail-safe: non-worktree behavior is byte-for-byte unchanged.
func TestDiff_NonWorktreeBaselineStaysHead(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	gitInit(t, root)
	writeFile(t, root, "a.go", "package a\n")
	gitCommitAll(t, root, "init")

	fs := newFS(t, Root{ID: "project", Path: root, Kind: "project"})
	defer fs.Close()

	d, err := fs.Diff(context.Background(), "project", "a.go")
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if d.Status != statusClean {
		t.Fatalf("plain committed file = clean, got %q", d.Status)
	}
	if d.Baseline != "HEAD" {
		t.Fatalf("plain repo baseline must stay HEAD, got %q", d.Baseline)
	}
}

// TestDiff_NonWorktreeUnderSymlinkRootStaysHead — a plain repo whose root is reached via
// a symlink must NOT be misdetected as a linked worktree (finding 5): baseline stays HEAD.
func TestDiff_NonWorktreeUnderSymlinkRootStaysHead(t *testing.T) {
	requireGit(t)
	realDir := t.TempDir()
	gitInit(t, realDir)
	writeFile(t, realDir, "a.go", "package a\n")
	gitCommitAll(t, realDir, "init")
	// Edit without committing so a correct HEAD baseline yields a non-empty diff.
	writeFile(t, realDir, "a.go", "package a\n// edit\n")

	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	fs := newFS(t, Root{ID: "project", Path: link, Kind: "project"})
	defer fs.Close()

	d, err := fs.Diff(context.Background(), "project", "a.go")
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if d.Baseline != "HEAD" {
		t.Fatalf("symlinked-root plain repo must stay HEAD (not misdetected as worktree), got %q", d.Baseline)
	}
	if !strings.Contains(d.Diff, "edit") {
		t.Fatalf("expected the uncommitted edit vs HEAD, got %+v", d)
	}
}

// TestDiff_WorktreeNoMergeBaseFallsBackToHead — a worktree branch with an UNRELATED
// history (no merge-base with main) falls back to the HEAD baseline (no-base rule).
func TestDiff_WorktreeNoMergeBaseFallsBackToHead(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	gitInit(t, root)
	writeFile(t, root, "a.go", "package a\n")
	gitCommitAll(t, root, "init")

	// An orphan worktree: unrelated history, no merge-base with main.
	wt := filepath.Join(root, ".worktrees", "orphan")
	gitRun(t, root, "worktree", "add", "--detach", filepath.FromSlash(".worktrees/orphan"))
	gitRun(t, wt, "checkout", "--orphan", "orphanbranch")
	gitRun(t, wt, "rm", "-rf", ".")
	writeFile(t, root, ".worktrees/orphan/b.go", "package b\n")
	commitOn(t, wt, "orphan root")
	// Uncommitted edit so a HEAD baseline yields a visible diff.
	writeFile(t, root, ".worktrees/orphan/b.go", "package b\n// edit\n")

	fs := newFS(t, Root{ID: "project", Path: root, Kind: "project"})
	defer fs.Close()

	d, err := fs.Diff(context.Background(), "project", ".worktrees/orphan/b.go")
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if d.Baseline != "HEAD" {
		t.Fatalf("no merge-base → baseline must fall back to HEAD, got %q", d.Baseline)
	}
	if !strings.Contains(d.Diff, "edit") {
		t.Fatalf("expected uncommitted edit vs HEAD, got %+v", d)
	}
}

// TestForkPoint_BadRefIsError — forkPoint must distinguish a real git failure (bad ref → exit 128) from a
// benign "no common ancestor" (exit 1). A bad ref returns an error so the caller surfaces it rather than
// silently treating the worktree as having no base.
func TestForkPoint_BadRefIsError(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	gitInit(t, root)
	writeFile(t, root, "a.go", "package a\n")
	gitCommitAll(t, root, "init")
	gitDir := filepath.Join(root, ".git")

	if _, err := forkPoint(context.Background(), gitDir, "HEAD", strings.Repeat("0", 40)); err == nil {
		t.Fatal("forkPoint with a bad ref must return an error, not a silent empty 'no base'")
	}
	ref, err := forkPoint(context.Background(), gitDir, "HEAD", "HEAD")
	if err != nil || ref == "" {
		t.Fatalf("forkPoint HEAD..HEAD should resolve the commit, got ref=%q err=%v", ref, err)
	}
}

// TestEnumerateLinkedWorktrees covers the shared primitive: lists linked worktrees under
// the root (with Head), keeps a detached one, excludes the main worktree, rejects a
// worktree physically OUTSIDE the root, and returns nothing for a plain repo.
func TestEnumerateLinkedWorktrees(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	gitInit(t, root)
	writeFile(t, root, "a.go", "package a\n")
	gitCommitAll(t, root, "init")

	gitDir := filepath.Join(root, ".git")

	// Plain repo: no linked worktrees.
	if got, _, err := enumerateLinkedWorktrees(context.Background(), gitDir, root, root); err != nil || len(got) != 0 {
		t.Fatalf("plain repo: want 0 linked worktrees (err=%v), got %+v", err, got)
	}

	// An in-root attached worktree, an in-root detached worktree, and an OUTSIDE one.
	addWorktree(t, root, ".worktrees/attached", "attached")
	gitRun(t, root, "worktree", "add", "--detach", filepath.FromSlash(".worktrees/detached"))
	outside := filepath.Join(t.TempDir(), "outside")
	gitRun(t, root, "worktree", "add", "--detach", outside)

	got, _, err := enumerateLinkedWorktrees(context.Background(), gitDir, root, root)
	if err != nil {
		t.Fatalf("enumerate: %v", err)
	}
	rels := map[string]string{}
	for _, w := range got {
		rels[w.RelDir] = w.Head
	}
	if _, ok := rels[".worktrees/attached"]; !ok {
		t.Fatalf("missing attached worktree; got %+v", got)
	}
	if _, ok := rels[".worktrees/detached"]; !ok {
		t.Fatalf("detached worktree must be kept (Head suffices); got %+v", got)
	}
	for _, w := range got {
		if w.Head == "" {
			t.Fatalf("every entry needs a Head sha; got %+v", w)
		}
		if w.RelDir == "." || strings.HasPrefix(w.RelDir, "..") {
			t.Fatalf("must exclude the main worktree and anything outside root; got %q", w.RelDir)
		}
	}
	if len(got) != 2 {
		t.Fatalf("want exactly the 2 in-root worktrees (outside excluded), got %d: %+v", len(got), got)
	}
}

// TestChanges_BaseModeForkNotMainTip — base mode lists the flow branch's committed changes
// vs the fork, with paths under .worktrees/<flow>/; a later same-file change on main does
// not alter the listing (it is committed branch work vs fork). Plain repo → empty.
func TestChanges_BaseModeForkNotMainTip(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	gitInit(t, root)
	writeFile(t, root, "pkg/foo.go", "base\n")
	gitCommitAll(t, root, "init")

	addWorktree(t, root, ".worktrees/myflow", "myflow")
	wt := filepath.Join(root, ".worktrees", "myflow")
	writeFile(t, root, ".worktrees/myflow/pkg/foo.go", "base\nAGENT\n")
	commitOn(t, wt, "agent work")
	// main moves on AFTER the fork: changes the same file AND adds a main-only file. Neither belongs in the
	// flow's "vs base" list (baseline is the fork, not main's tip).
	writeFile(t, root, "pkg/foo.go", "base\nMAIN\n")
	writeFile(t, root, "pkg/bar.go", "main only\n")
	gitCommitAll(t, root, "main moves on")

	fs := newFS(t, Root{ID: "project", Path: root, Kind: "project"})
	defer fs.Close()

	list, err := fs.Changes(context.Background(), "project", ChangeModeBase)
	if err != nil {
		t.Fatalf("Changes(base): %v", err)
	}
	var found bool
	for _, c := range list.Entries {
		if c.Path == ".worktrees/myflow/pkg/foo.go" {
			found = true
			if c.Status != ChangeModified {
				t.Fatalf("want modified, got %q", c.Status)
			}
		}
		// A main-only file committed after the fork must NOT appear — if it does, the baseline is main's tip,
		// not the fork point (it would surface as a spurious deleted entry).
		if strings.Contains(c.Path, "bar.go") {
			t.Fatalf("base list leaked a main-only post-fork file (baseline is main tip, not fork): %+v", c)
		}
	}
	if !found {
		t.Fatalf("base mode must list the flow's committed file under .worktrees/<flow>/; got %+v", list.Entries)
	}

	// Plain repo (no worktree) → empty base list.
	plain := t.TempDir()
	gitInit(t, plain)
	writeFile(t, plain, "a.go", "package a\n")
	gitCommitAll(t, plain, "init")
	pfs := newFS(t, Root{ID: "p", Path: plain, Kind: "project"})
	defer pfs.Close()
	pl, err := pfs.Changes(context.Background(), "p", ChangeModeBase)
	if err != nil {
		t.Fatalf("Changes(base) plain: %v", err)
	}
	if len(pl.Entries) != 0 {
		t.Fatalf("plain repo base mode must be empty, got %+v", pl.Entries)
	}
}

// TestChanges_BaseModeOrphanNoMergeBase — a worktree branch on an unrelated/orphan history (no merge-base
// with main) contributes NOTHING to the base list (the !forkOK skip), rather than dumping its whole tree.
func TestChanges_BaseModeOrphanNoMergeBase(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	gitInit(t, root)
	writeFile(t, root, "a.go", "package a\n")
	gitCommitAll(t, root, "init")

	wt := filepath.Join(root, ".worktrees", "orphan")
	gitRun(t, root, "worktree", "add", "--detach", filepath.FromSlash(".worktrees/orphan"))
	gitRun(t, wt, "checkout", "--orphan", "orphanbranch")
	gitRun(t, wt, "rm", "-rf", ".")
	writeFile(t, root, ".worktrees/orphan/b.go", "package b\n")
	commitOn(t, wt, "orphan root")

	fs := newFS(t, Root{ID: "project", Path: root, Kind: "project"})
	defer fs.Close()

	list, err := fs.Changes(context.Background(), "project", ChangeModeBase)
	if err != nil {
		t.Fatalf("Changes(base): %v", err)
	}
	if len(list.Entries) != 0 {
		t.Fatalf("orphan worktree (no merge-base) must contribute nothing to the base list, got %+v", list.Entries)
	}
}
