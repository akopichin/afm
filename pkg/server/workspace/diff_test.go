//go:build linux

package workspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestDiff_TrackedModifiedAndUntracked exercises the full HEAD→working-tree
// diff path against a real git repo: a tracked file with a local modification
// (status "modified", unified diff body containing the new line) and an
// untracked file (status "added").
func TestDiff_TrackedModifiedAndUntracked(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "f.go"), []byte("package a\nold\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-qm", "init")
	if err := os.WriteFile(filepath.Join(dir, "f.go"), []byte("package a\nnew\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "u.go"), []byte("package u\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	fs, err := New([]Root{{ID: "project", Path: dir, Kind: "project"}})
	if err != nil {
		t.Skip(err)
	}
	defer fs.Close()

	d, err := fs.Diff(context.Background(), "project", "f.go")
	if err != nil || d.Status != "modified" || !strings.Contains(d.Diff, "new") {
		t.Fatalf("tracked diff: %+v err=%v", d, err)
	}
	u, err := fs.Diff(context.Background(), "project", "u.go")
	if err != nil || u.Status != "added" {
		t.Fatalf("untracked diff: %+v err=%v", u, err)
	}
}

// TestDiff_NoRepoUnavailable verifies that a root with no .git anywhere above
// it (up to the root itself) returns ErrDiffUnavailable, not a panic or a
// misleading empty diff.
func TestDiff_NoRepoUnavailable(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs, err := New([]Root{{ID: "extra-1", Path: dir, Kind: "extra"}})
	if err != nil {
		t.Skip(err)
	}
	defer fs.Close()
	if _, err := fs.Diff(context.Background(), "extra-1", "f.txt"); !errors.Is(err, ErrDiffUnavailable) {
		t.Errorf("no repo → ErrDiffUnavailable, got %v", err)
	}
}
