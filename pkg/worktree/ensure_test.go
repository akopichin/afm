package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newRepo creates a fresh git repository in a temp dir with one commit and
// returns its absolute path. Modeled on pkg/server/workspace/diff_test.go's
// gitInit/gitCommitAll helpers.
func newRepo(t *testing.T) string {
	t.Helper()
	requireGit(t)

	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	runGit(t, dir, "config", "user.email", "t@t")
	runGit(t, dir, "config", "user.name", "t")

	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-qm", "initial")

	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}
	return abs
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %v (dir=%s): %v\n%s", args, dir, err, out)
	}
}

func headCommit(t *testing.T, dir string) string {
	t.Helper()
	c := exec.Command("git", "rev-parse", "HEAD")
	c.Dir = dir
	out, err := c.Output()
	if err != nil {
		t.Fatalf("rev-parse HEAD in %s: %v", dir, err)
	}
	return strings.TrimSpace(string(out))
}

func branchTip(t *testing.T, dir, branch string) string {
	t.Helper()
	c := exec.Command("git", "rev-parse", branch)
	c.Dir = dir
	out, err := c.Output()
	if err != nil {
		t.Fatalf("rev-parse %s in %s: %v", branch, dir, err)
	}
	return strings.TrimSpace(string(out))
}

func worktreeList(t *testing.T, dir string) string {
	t.Helper()
	c := exec.Command("git", "worktree", "list", "--porcelain")
	c.Dir = dir
	out, err := c.Output()
	if err != nil {
		t.Fatalf("worktree list in %s: %v", dir, err)
	}
	return string(out)
}

func readExclude(t *testing.T, base string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(base, ".git", "info", "exclude"))
	if err != nil {
		t.Fatalf("ReadFile exclude: %v", err)
	}
	return string(data)
}

// --- Happy paths ---

func TestEnsure_CreatesWorktreeAndBranch(t *testing.T) {
	base := newRepo(t)
	p, err := PlanFor(base, "myflow")
	if err != nil {
		t.Fatalf("PlanFor: %v", err)
	}

	if err := Ensure(context.Background(), p, ClearNone); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	if _, err := os.Stat(p.Path); err != nil {
		t.Fatalf("worktree path does not exist: %v", err)
	}

	list := worktreeList(t, base)
	entries := ParseList(list)
	var found *Entry
	for i := range entries {
		if filepath.Clean(entries[i].Path) == filepath.Clean(p.Path) {
			found = &entries[i]
		}
	}
	if found == nil {
		t.Fatalf("worktree list does not contain %s:\n%s", p.Path, list)
	}
	if found.Branch != "myflow" {
		t.Fatalf("worktree branch = %q, want %q", found.Branch, "myflow")
	}

	if branchTip(t, base, "myflow") != headCommit(t, base) {
		t.Fatal("branch tip does not match HEAD")
	}

	if !strings.Contains(readExclude(t, base), ".worktrees/") {
		t.Fatal("info/exclude does not contain .worktrees/")
	}
}

func TestEnsure_ReusesExistingWorktree(t *testing.T) {
	base := newRepo(t)
	p, err := PlanFor(base, "myflow")
	if err != nil {
		t.Fatalf("PlanFor: %v", err)
	}

	if err := Ensure(context.Background(), p, ClearNone); err != nil {
		t.Fatalf("Ensure (1st): %v", err)
	}

	marker := filepath.Join(p.Path, "survivor.txt")
	if err := os.WriteFile(marker, []byte("still here\n"), 0o644); err != nil {
		t.Fatalf("WriteFile marker: %v", err)
	}

	if err := Ensure(context.Background(), p, ClearNone); err != nil {
		t.Fatalf("Ensure (2nd): %v", err)
	}

	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("marker did not survive: %v", err)
	}
	if string(data) != "still here\n" {
		t.Fatalf("marker content changed: %q", data)
	}
}

func TestEnsure_ReusesWithUncommittedChanges(t *testing.T) {
	base := newRepo(t)
	p, err := PlanFor(base, "myflow")
	if err != nil {
		t.Fatalf("PlanFor: %v", err)
	}
	if err := Ensure(context.Background(), p, ClearNone); err != nil {
		t.Fatalf("Ensure (1st): %v", err)
	}

	dirty := filepath.Join(p.Path, "dirty.txt")
	if err := os.WriteFile(dirty, []byte("uncommitted\n"), 0o644); err != nil {
		t.Fatalf("WriteFile dirty: %v", err)
	}

	if err := Ensure(context.Background(), p, ClearNone); err != nil {
		t.Fatalf("Ensure (2nd): %v", err)
	}

	data, err := os.ReadFile(dirty)
	if err != nil {
		t.Fatalf("dirty file did not survive: %v", err)
	}
	if string(data) != "uncommitted\n" {
		t.Fatalf("dirty file content changed: %q", data)
	}
}

// --- Refusals ---

func TestEnsure_BranchCheckedOutElsewhere(t *testing.T) {
	base := newRepo(t)
	runGit(t, base, "worktree", "add", "other", "-b", "myflow")

	p, err := PlanFor(base, "myflow")
	if err != nil {
		t.Fatalf("PlanFor: %v", err)
	}

	err = Ensure(context.Background(), p, ClearNone)
	if err == nil {
		t.Fatal("Ensure: expected error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "myflow") {
		t.Errorf("error does not mention branch: %q", msg)
	}
	if !strings.Contains(msg, filepath.Join(base, "other")) {
		t.Errorf("error does not mention other path: %q", msg)
	}
	if !strings.Contains(msg, "rename the flow") {
		t.Errorf("error does not suggest renaming the flow: %q", msg)
	}
}

func TestEnsure_BranchIsMainCheckoutBranch(t *testing.T) {
	base := newRepo(t)
	runGit(t, base, "branch", "myflow")
	runGit(t, base, "checkout", "-q", "myflow")

	p, err := PlanFor(base, "myflow")
	if err != nil {
		t.Fatalf("PlanFor: %v", err)
	}

	if err := Ensure(context.Background(), p, ClearNone); err == nil {
		t.Fatal("Ensure: expected error, got nil")
	}
}

func TestEnsure_PathHoldsDifferentBranch(t *testing.T) {
	base := newRepo(t)
	runGit(t, base, "worktree", "add", filepath.Join(".worktrees", "myflow"), "-b", "other")

	p, err := PlanFor(base, "myflow")
	if err != nil {
		t.Fatalf("PlanFor: %v", err)
	}

	if err := Ensure(context.Background(), p, ClearNone); err == nil {
		t.Fatal("Ensure: expected error, got nil")
	}
}

func TestEnsure_BranchExistsWithoutWorktree(t *testing.T) {
	base := newRepo(t)
	runGit(t, base, "branch", "myflow")

	p, err := PlanFor(base, "myflow")
	if err != nil {
		t.Fatalf("PlanFor: %v", err)
	}

	err = Ensure(context.Background(), p, ClearNone)
	if err == nil {
		t.Fatal("Ensure: expected error, got nil")
	}
	msg := err.Error()
	for _, want := range []string{"git worktree add", p.Path, "myflow", "--worktree-clear"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error does not mention %q: %q", want, msg)
		}
	}
}

func TestEnsure_PathOccupiedByStrayDirectory(t *testing.T) {
	base := newRepo(t)
	p, err := PlanFor(base, "myflow")
	if err != nil {
		t.Fatalf("PlanFor: %v", err)
	}
	if err := os.MkdirAll(p.Path, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	marker := filepath.Join(p.Path, "stray.txt")
	if err := os.WriteFile(marker, []byte("stray\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := Ensure(context.Background(), p, ClearNone); err == nil {
		t.Fatal("Ensure: expected error, got nil")
	}

	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("stray file was removed: %v", err)
	}
}

func TestEnsure_NotAGitRepository(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	p := Plan{Base: dir, Dir: filepath.Join(dir, ".worktrees"), Path: filepath.Join(dir, ".worktrees", "myflow"), Branch: "myflow"}

	err := Ensure(context.Background(), p, ClearNone)
	if err == nil {
		t.Fatal("Ensure: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("error = %q, want mention of 'not a git repository'", err.Error())
	}
}

func TestEnsure_DetachedWorktreeAtPath(t *testing.T) {
	base := newRepo(t)
	p, err := PlanFor(base, "myflow")
	if err != nil {
		t.Fatalf("PlanFor: %v", err)
	}
	runGit(t, base, "worktree", "add", "--detach", p.Path)

	if err := Ensure(context.Background(), p, ClearNone); err == nil {
		t.Fatal("Ensure: expected error, got nil")
	}
}

// --- Clear modes ---

func TestEnsure_ClearWorktree_KeepsCommits(t *testing.T) {
	base := newRepo(t)
	p, err := PlanFor(base, "myflow")
	if err != nil {
		t.Fatalf("PlanFor: %v", err)
	}
	if err := Ensure(context.Background(), p, ClearNone); err != nil {
		t.Fatalf("Ensure (create): %v", err)
	}

	committed := filepath.Join(p.Path, "committed.txt")
	if err := os.WriteFile(committed, []byte("committed\n"), 0o644); err != nil {
		t.Fatalf("WriteFile committed: %v", err)
	}
	runGit(t, p.Path, "add", ".")
	runGit(t, p.Path, "commit", "-qm", "add committed file")
	tipBefore := branchTip(t, base, "myflow")

	uncommitted := filepath.Join(p.Path, "uncommitted.txt")
	if err := os.WriteFile(uncommitted, []byte("uncommitted\n"), 0o644); err != nil {
		t.Fatalf("WriteFile uncommitted: %v", err)
	}

	if err := Ensure(context.Background(), p, ClearWorktree); err != nil {
		t.Fatalf("Ensure (ClearWorktree): %v", err)
	}

	if _, err := os.Stat(uncommitted); err == nil {
		t.Fatal("uncommitted file still present after ClearWorktree")
	}
	if _, err := os.Stat(committed); err != nil {
		t.Fatalf("committed file missing after ClearWorktree: %v", err)
	}
	if branchTip(t, base, "myflow") != tipBefore {
		t.Fatal("branch tip changed after ClearWorktree")
	}

	entries := ParseList(worktreeList(t, base))
	var found *Entry
	for i := range entries {
		if filepath.Clean(entries[i].Path) == filepath.Clean(p.Path) {
			found = &entries[i]
		}
	}
	if found == nil {
		t.Fatalf("worktree not registered at %s after ClearWorktree", p.Path)
	}
	if found.Branch != "myflow" {
		t.Fatalf("worktree branch = %q, want %q", found.Branch, "myflow")
	}
}

func TestEnsure_ClearAll_DropsCommits(t *testing.T) {
	base := newRepo(t)
	p, err := PlanFor(base, "myflow")
	if err != nil {
		t.Fatalf("PlanFor: %v", err)
	}
	if err := Ensure(context.Background(), p, ClearNone); err != nil {
		t.Fatalf("Ensure (create): %v", err)
	}

	committed := filepath.Join(p.Path, "committed.txt")
	if err := os.WriteFile(committed, []byte("committed\n"), 0o644); err != nil {
		t.Fatalf("WriteFile committed: %v", err)
	}
	runGit(t, p.Path, "add", ".")
	runGit(t, p.Path, "commit", "-qm", "add committed file")

	if err := Ensure(context.Background(), p, ClearAll); err != nil {
		t.Fatalf("Ensure (ClearAll): %v", err)
	}

	if branchTip(t, base, "myflow") != headCommit(t, base) {
		t.Fatal("branch tip does not equal main checkout HEAD after ClearAll")
	}
	if _, err := os.Stat(committed); err == nil {
		t.Fatal("previously committed file still present after ClearAll")
	}

	entries := ParseList(worktreeList(t, base))
	var found *Entry
	for i := range entries {
		if filepath.Clean(entries[i].Path) == filepath.Clean(p.Path) {
			found = &entries[i]
		}
	}
	if found == nil {
		t.Fatalf("worktree not registered at %s after ClearAll", p.Path)
	}
	if found.Branch != "myflow" {
		t.Fatalf("worktree branch = %q, want %q", found.Branch, "myflow")
	}
}

func TestEnsure_ClearOnFreshRepo(t *testing.T) {
	for _, mode := range []ClearMode{ClearWorktree, ClearAll} {
		t.Run("", func(t *testing.T) {
			base := newRepo(t)
			p, err := PlanFor(base, "myflow")
			if err != nil {
				t.Fatalf("PlanFor: %v", err)
			}

			if err := Ensure(context.Background(), p, mode); err != nil {
				t.Fatalf("Ensure(mode=%v): %v", mode, err)
			}
			if _, err := os.Stat(p.Path); err != nil {
				t.Fatalf("worktree not created: %v", err)
			}
		})
	}
}

func TestEnsure_ClearRefusesStrayDirectory(t *testing.T) {
	for _, mode := range []ClearMode{ClearWorktree, ClearAll} {
		t.Run("", func(t *testing.T) {
			base := newRepo(t)
			p, err := PlanFor(base, "myflow")
			if err != nil {
				t.Fatalf("PlanFor: %v", err)
			}
			if err := os.MkdirAll(p.Path, 0o755); err != nil {
				t.Fatalf("MkdirAll: %v", err)
			}
			marker := filepath.Join(p.Path, "stray.txt")
			if err := os.WriteFile(marker, []byte("stray\n"), 0o644); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}

			if err := Ensure(context.Background(), p, mode); err == nil {
				t.Fatalf("Ensure(mode=%v): expected error, got nil", mode)
			}
			if _, err := os.Stat(marker); err != nil {
				t.Fatalf("stray directory touched: %v", err)
			}
		})
	}
}

func TestEnsure_ClearRefusesWorktreeOnAnotherBranch(t *testing.T) {
	for _, mode := range []ClearMode{ClearWorktree, ClearAll} {
		t.Run("", func(t *testing.T) {
			base := newRepo(t)
			p, err := PlanFor(base, "myflow")
			if err != nil {
				t.Fatalf("PlanFor: %v", err)
			}
			runGit(t, base, "worktree", "add", p.Path, "-b", "other")

			uncommitted := filepath.Join(p.Path, "uncommitted.txt")
			if err := os.WriteFile(uncommitted, []byte("uncommitted\n"), 0o644); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}

			if err := Ensure(context.Background(), p, mode); err == nil {
				t.Fatalf("Ensure(mode=%v): expected error, got nil", mode)
			}

			data, err := os.ReadFile(uncommitted)
			if err != nil {
				t.Fatalf("uncommitted file removed: %v", err)
			}
			if string(data) != "uncommitted\n" {
				t.Fatalf("uncommitted file content changed: %q", data)
			}
		})
	}
}

func TestEnsure_ClearAllRefusesWhenBranchIsCheckedOutInMain(t *testing.T) {
	base := newRepo(t)
	runGit(t, base, "branch", "myflow")
	runGit(t, base, "checkout", "-q", "myflow")

	p, err := PlanFor(base, "myflow")
	if err != nil {
		t.Fatalf("PlanFor: %v", err)
	}

	err = Ensure(context.Background(), p, ClearAll)
	if err == nil {
		t.Fatal("Ensure: expected error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, base) && !strings.Contains(msg, "myflow") {
		t.Errorf("error does not look like an afm-authored message naming the main checkout/branch: %q", msg)
	}
	if strings.Contains(msg, "fatal:") {
		t.Errorf("error leaks raw git noise: %q", msg)
	}
}

func TestEnsure_ClearReattachesOrphanBranch(t *testing.T) {
	t.Run("ClearWorktree", func(t *testing.T) {
		base := newRepo(t)
		p, err := PlanFor(base, "myflow")
		if err != nil {
			t.Fatalf("PlanFor: %v", err)
		}

		// Create the branch with its own commit via a throwaway worktree, then
		// remove the worktree registration (but keep the branch) so the state
		// matches test 7's "branch exists, no worktree" case.
		tmpWt := filepath.Join(base, "tmp-wt")
		runGit(t, base, "worktree", "add", tmpWt, "-b", "myflow")
		marker := filepath.Join(tmpWt, "orphan.txt")
		if err := os.WriteFile(marker, []byte("orphan\n"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		runGit(t, tmpWt, "add", ".")
		runGit(t, tmpWt, "commit", "-qm", "orphan commit")
		runGit(t, base, "worktree", "remove", "--force", tmpWt)
		expectTip := branchTip(t, base, "myflow")

		if err := Ensure(context.Background(), p, ClearWorktree); err != nil {
			t.Fatalf("Ensure(ClearWorktree): %v", err)
		}
		if branchTip(t, base, "myflow") != expectTip {
			t.Fatal("branch tip changed on re-attach")
		}
		if _, err := os.Stat(filepath.Join(p.Path, "orphan.txt")); err != nil {
			t.Fatalf("orphan commit's file missing after re-attach: %v", err)
		}
	})

	t.Run("ClearAll", func(t *testing.T) {
		base := newRepo(t)
		p, err := PlanFor(base, "myflow")
		if err != nil {
			t.Fatalf("PlanFor: %v", err)
		}

		tmpWt := filepath.Join(base, "tmp-wt")
		runGit(t, base, "worktree", "add", tmpWt, "-b", "myflow")
		marker := filepath.Join(tmpWt, "orphan.txt")
		if err := os.WriteFile(marker, []byte("orphan\n"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		runGit(t, tmpWt, "add", ".")
		runGit(t, tmpWt, "commit", "-qm", "orphan commit")
		runGit(t, base, "worktree", "remove", "--force", tmpWt)

		if err := Ensure(context.Background(), p, ClearAll); err != nil {
			t.Fatalf("Ensure(ClearAll): %v", err)
		}
		if branchTip(t, base, "myflow") != headCommit(t, base) {
			t.Fatal("branch tip does not equal HEAD after ClearAll re-create")
		}
		if _, err := os.Stat(filepath.Join(p.Path, "orphan.txt")); err == nil {
			t.Fatal("orphan commit's file still present after ClearAll re-create")
		}
	})
}

func TestEnsure_ExcludeFailureIsNotFatal(t *testing.T) {
	requireGit(t)
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not block writes")
	}
	base := newRepo(t)
	p, err := PlanFor(base, "myflow")
	if err != nil {
		t.Fatalf("PlanFor: %v", err)
	}

	infoDir := filepath.Join(base, ".git", "info")
	if err := os.MkdirAll(infoDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.Remove(filepath.Join(infoDir, "exclude")); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove template exclude: %v", err)
	}
	if err := os.Chmod(infoDir, 0o555); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(infoDir, 0o755)
	})

	if err := Ensure(context.Background(), p, ClearNone); err != nil {
		t.Fatalf("Ensure: expected nil despite unwritable info dir, got %v", err)
	}
	if _, err := os.Stat(p.Path); err != nil {
		t.Fatalf("worktree not created despite exclude failure: %v", err)
	}
}

// TestEnsure_CreateFailsWhenDirIsUnwritable exercises the os.MkdirAll(p.Dir, ...)
// failure branch of the fresh-create path (step 6's default case) — a path
// not covered by any other test, since every other scenario either already has
// .worktrees/ present or runs on a writable filesystem.
func TestEnsure_CreateFailsWhenDirIsUnwritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not block writes")
	}
	base := newRepo(t)
	p, err := PlanFor(base, "myflow")
	if err != nil {
		t.Fatalf("PlanFor: %v", err)
	}

	// A regular file where .worktrees/ would need to be a directory makes
	// MkdirAll fail with ENOTDIR, without needing root-only permission tricks.
	if err := os.WriteFile(p.Dir, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := Ensure(context.Background(), p, ClearNone); err == nil {
		t.Fatal("Ensure: expected an error when .worktrees/ cannot be created, got nil")
	}
}

// TestEnsure_RespectsContextCancellation mirrors gitcmd's own cancellation
// test: Ensure must not block forever, and must return an error, when its
// context is already cancelled before the first git call.
func TestEnsure_RespectsContextCancellation(t *testing.T) {
	base := newRepo(t)
	p, err := PlanFor(base, "myflow")
	if err != nil {
		t.Fatalf("PlanFor: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan error, 1)
	go func() { done <- Ensure(ctx, p, ClearNone) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Ensure: err = nil, want non-nil for a cancelled context")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Ensure did not return promptly after context cancellation")
	}
}

// TestEnsure_ClearWorktreeRemovesLockedWorktree regression-tests removeWorktree's use of --force twice:
// git refuses `worktree remove --force` (a single --force) on a worktree that `git worktree lock` has
// marked locked — ClearWorktree must still be able to tear it down.
func TestEnsure_ClearWorktreeRemovesLockedWorktree(t *testing.T) {
	base := newRepo(t)
	p, err := PlanFor(base, "myflow")
	if err != nil {
		t.Fatalf("PlanFor: %v", err)
	}
	if err := Ensure(context.Background(), p, ClearNone); err != nil {
		t.Fatalf("Ensure (create): %v", err)
	}
	runGit(t, base, "worktree", "lock", p.Path)

	if err := Ensure(context.Background(), p, ClearWorktree); err != nil {
		t.Fatalf("Ensure(ClearWorktree) on a locked worktree: %v", err)
	}
	if _, err := os.Stat(p.Path); err != nil {
		t.Fatalf("worktree not re-created after clearing a locked one: %v", err)
	}
}
