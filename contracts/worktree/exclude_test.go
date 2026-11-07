package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitInitExclude runs `git init` + identity config in dir, failing the test on
// error. It also removes any template-populated info/exclude (most git
// installs ship a template with boilerplate comments) so callers start from a
// genuinely absent file, matching the scenarios under test.
func gitInitExclude(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
	} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	if err := os.Remove(filepath.Join(dir, ".git", "info", "exclude")); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove template exclude: %v", err)
	}
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH")
	}
}

func TestEnsureExcluded_CreatesFile(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	gitInitExclude(t, dir)

	if err := EnsureExcluded(context.Background(), dir); err != nil {
		t.Fatalf("EnsureExcluded: %v", err)
	}

	excludePath := filepath.Join(dir, ".git", "info", "exclude")
	data, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.HasSuffix(string(data), ".worktrees/\n") {
		t.Fatalf("exclude file does not end with .worktrees/\\n, got %q", string(data))
	}
}

func TestEnsureExcluded_Idempotent(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	gitInitExclude(t, dir)

	if err := EnsureExcluded(context.Background(), dir); err != nil {
		t.Fatalf("EnsureExcluded (1st): %v", err)
	}
	excludePath := filepath.Join(dir, ".git", "info", "exclude")
	first, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatalf("ReadFile (1st): %v", err)
	}

	if err := EnsureExcluded(context.Background(), dir); err != nil {
		t.Fatalf("EnsureExcluded (2nd): %v", err)
	}
	second, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatalf("ReadFile (2nd): %v", err)
	}

	if string(first) != string(second) {
		t.Fatalf("content changed between calls:\n1st: %q\n2nd: %q", first, second)
	}
	if n := strings.Count(string(second), ".worktrees"); n != 1 {
		t.Fatalf("expected exactly one .worktrees occurrence, got %d in %q", n, second)
	}
}

func TestEnsureExcluded_PreservesExistingContent(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	gitInitExclude(t, dir)

	infoDir := filepath.Join(dir, ".git", "info")
	if err := os.MkdirAll(infoDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	excludePath := filepath.Join(infoDir, "exclude")
	// No trailing newline on the last line - exercises leading-newline insertion.
	preseed := "# comment\n*.tmp"
	if err := os.WriteFile(excludePath, []byte(preseed), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := EnsureExcluded(context.Background(), dir); err != nil {
		t.Fatalf("EnsureExcluded: %v", err)
	}

	data, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	content := string(data)
	lines := strings.Split(content, "\n")
	if !strings.Contains(content, "# comment") || !strings.Contains(content, "*.tmp") {
		t.Fatalf("original content lost: %q", content)
	}
	found := false
	for _, line := range lines {
		if line == ".worktrees/" {
			found = true
		}
		// Ensure .worktrees/ was not glued onto *.tmp.
		if line == "*.tmp.worktrees/" || strings.HasPrefix(line, "*.tmp.worktrees") {
			t.Fatalf(".worktrees/ was run together with previous line: %q", content)
		}
	}
	if !found {
		t.Fatalf(".worktrees/ not found on its own line: %q", content)
	}
}

func TestEnsureExcluded_AlreadyPresentInAnyForm(t *testing.T) {
	requireGit(t)

	cases := []struct {
		name    string
		preseed string
	}{
		{"with slash and whitespace", "# head\n  .worktrees/  \n"},
		{"without trailing slash", "# head\n.worktrees\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			gitInitExclude(t, dir)

			infoDir := filepath.Join(dir, ".git", "info")
			if err := os.MkdirAll(infoDir, 0o755); err != nil {
				t.Fatalf("MkdirAll: %v", err)
			}
			excludePath := filepath.Join(infoDir, "exclude")
			if err := os.WriteFile(excludePath, []byte(tc.preseed), 0o644); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}

			if err := EnsureExcluded(context.Background(), dir); err != nil {
				t.Fatalf("EnsureExcluded: %v", err)
			}

			data, err := os.ReadFile(excludePath)
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			if n := strings.Count(string(data), ".worktrees"); n != 1 {
				t.Fatalf("expected exactly one .worktrees occurrence, got %d in %q", n, data)
			}
		})
	}
}

func TestEnsureExcluded_NonFatalOnUnwritable(t *testing.T) {
	requireGit(t)
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not block writes")
	}
	dir := t.TempDir()
	gitInitExclude(t, dir)

	gitDir := filepath.Join(dir, ".git")
	infoDir := filepath.Join(gitDir, "info")
	if err := os.MkdirAll(infoDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// gitInitExclude already removed any template-populated exclude file, so
	// the write below must create a new directory entry and is actually
	// blocked by the read-only directory below, not just an existing
	// writable file.
	if err := os.Chmod(infoDir, 0o555); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(infoDir, 0o755)
	})

	if err := EnsureExcluded(context.Background(), dir); err == nil {
		t.Fatal("EnsureExcluded: expected error on unwritable info dir, got nil")
	}
}

// TestEnsureExcluded_IgnoresAmbientGitEnv is the mirror of
// TestRun_IgnoresAmbientGitEnv / TestChanges_IgnoresAmbientGitEnv /
// TestDiff_IgnoresAmbientGitEnv: an ambient GIT_DIR/GIT_COMMON_DIR must not
// redirect gitCommonDir away from dir, or EnsureExcluded would write
// .worktrees/ into the wrong repository's info/exclude.
func TestEnsureExcluded_IgnoresAmbientGitEnv(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	gitInitExclude(t, dir)

	t.Setenv("GIT_DIR", "/nonexistent")
	t.Setenv("GIT_COMMON_DIR", "/nonexistent")

	if err := EnsureExcluded(context.Background(), dir); err != nil {
		t.Fatalf("EnsureExcluded: %v", err)
	}

	excludePath := filepath.Join(dir, ".git", "info", "exclude")
	data, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.HasSuffix(string(data), ".worktrees/\n") {
		t.Fatalf("exclude file does not end with .worktrees/\\n, got %q", string(data))
	}
}
