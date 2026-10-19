package gitcmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(SanitizeEnv(os.Environ()), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t",
		"GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func commitFile(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(name+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, dir, "add", ".")
	gitT(t, dir, "commit", "-q", "-m", name)
}

func gitDirOf(t *testing.T, dir string) string {
	t.Helper()
	return gitT(t, dir, "rev-parse", "--path-format=absolute", "--git-dir")
}

func TestDiffBaseRef(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	gitT(t, repo, "init", "-q", "-b", "main")
	commitFile(t, repo, "a.txt")
	forkPoint := gitT(t, repo, "rev-parse", "HEAD")

	wt := filepath.Join(repo, ".worktrees", "feature")
	gitT(t, repo, "worktree", "add", "-q", "-b", "feature", wt)
	commitFile(t, wt, "b.txt")
	commitFile(t, repo, "c.txt") // main moves on after the fork

	t.Run("ordinary repository: HEAD", func(t *testing.T) {
		if got := DiffBaseRef(ctx, RunGitDir, gitDirOf(t, repo), ""); got != "HEAD" {
			t.Fatalf("base = %q, want HEAD", got)
		}
	})
	t.Run("linked worktree: the fork point", func(t *testing.T) {
		if got := DiffBaseRef(ctx, RunGitDir, gitDirOf(t, wt), ""); got != forkPoint {
			t.Fatalf("base = %q, want the fork point %s", got, forkPoint)
		}
		if got := DiffBaseRef(ctx, RunGitDir, gitDirOf(t, wt), repo); got != forkPoint {
			t.Fatalf("base within the browse root = %q, want the fork point %s", got, forkPoint)
		}
	})
	t.Run("ordinary repository through a symlink: still HEAD", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(repo, link); err != nil {
			t.Fatal(err)
		}
		if got := DiffBaseRef(ctx, RunGitDir, filepath.Join(link, ".git"), ""); got != "HEAD" {
			t.Fatalf("base = %q, want HEAD: a symlinked path is not a linked worktree", got)
		}
	})
	t.Run("common git dir outside the root: HEAD", func(t *testing.T) {
		if got := DiffBaseRef(ctx, RunGitDir, gitDirOf(t, wt), wt); got != "HEAD" {
			t.Fatalf("base = %q, want HEAD when the main repository is outside the root", got)
		}
	})
}
