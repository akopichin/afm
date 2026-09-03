//go:build linux

package workspace

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitInit runs `git init` + identity config in dir, failing the test on error.
func gitInit(t *testing.T, dir string) {
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
}

// gitCommitAll stages and commits everything in dir.
func gitCommitAll(t *testing.T, dir, msg string) {
	t.Helper()
	for _, args := range [][]string{{"add", "."}, {"commit", "-qm", msg}} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
}

// newFS builds a single-root FS and skips the test if openat2 is unavailable on
// the running kernel (New degrades to zero roots — the real run is the Docker E2E).
func newFS(t *testing.T, root Root) FS {
	t.Helper()
	fs, err := New([]Root{root})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	impl, ok := fs.(*fsImpl)
	if !ok || impl.byID[root.ID] == nil {
		fs.Close()
		t.Skip("openat2 unavailable on this kernel — validated in Docker E2E")
	}
	return fs
}

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

// makeExternalRepo builds a git repo OUTSIDE any allowed root, committing
// relPath with the given secret content, and returns the repo dir. It is the
// escape target the security tests must never leak.
func makeExternalRepo(t *testing.T, relPath, secret string) string {
	t.Helper()
	ext := t.TempDir()
	gitInit(t, ext)
	if err := os.WriteFile(filepath.Join(ext, relPath), []byte(secret), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCommitAll(t, ext, "secret")
	return ext
}

// TestDiff_GitSymlinkRefused: a `.git` SYMLINK pointing at an external repo's
// object store must be refused structurally by openat2 (RESOLVE_NO_SYMLINKS) —
// Diff returns ErrDiffUnavailable and never leaks the external baseline.
func TestDiff_GitSymlinkRefused(t *testing.T) {
	const secret = "package a\nSECRET_FROM_OUTSIDE\n"
	ext := makeExternalRepo(t, "f.go", secret)

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "f.go"), []byte("package a\nlocal\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(ext, ".git"), filepath.Join(root, ".git")); err != nil {
		t.Fatal(err)
	}

	fs := newFS(t, Root{ID: "project", Path: root, Kind: "project"})
	defer fs.Close()

	d, err := fs.Diff(context.Background(), "project", "f.go")
	if !errors.Is(err, ErrDiffUnavailable) {
		t.Fatalf("symlink .git → want ErrDiffUnavailable, got %v", err)
	}
	if strings.Contains(d.Diff, "SECRET") {
		t.Fatalf("external baseline leaked: %q", d.Diff)
	}
}

// TestDiff_GitfileOutsideRootRefused: a gitfile `.git` (regular file with
// `gitdir: <outside>`) pointing outside the root fails the containment check →
// ErrDiffUnavailable, no external baseline.
func TestDiff_GitfileOutsideRootRefused(t *testing.T) {
	const secret = "package a\nSECRET_FROM_OUTSIDE\n"
	ext := makeExternalRepo(t, "f.go", secret)

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "f.go"), []byte("package a\nlocal\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitfile := "gitdir: " + filepath.Join(ext, ".git") + "\n"
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte(gitfile), 0o644); err != nil {
		t.Fatal(err)
	}

	fs := newFS(t, Root{ID: "project", Path: root, Kind: "project"})
	defer fs.Close()

	d, err := fs.Diff(context.Background(), "project", "f.go")
	if !errors.Is(err, ErrDiffUnavailable) {
		t.Fatalf("outside gitfile → want ErrDiffUnavailable, got %v", err)
	}
	if strings.Contains(d.Diff, "SECRET") {
		t.Fatalf("external baseline leaked: %q", d.Diff)
	}
}

// TestDiff_BinaryNoRepoUnavailable: a binary file with NO usable repo must NOT
// report a bogus success — repo discovery runs before the binary response.
func TestDiff_BinaryNoRepoUnavailable(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "b.bin"), []byte{0x00, 0x01, 0x02, 0x00}, 0o644); err != nil {
		t.Fatal(err)
	}
	fs := newFS(t, Root{ID: "extra-1", Path: root, Kind: "extra"})
	defer fs.Close()
	if _, err := fs.Diff(context.Background(), "extra-1", "b.bin"); !errors.Is(err, ErrDiffUnavailable) {
		t.Fatalf("binary + no repo → want ErrDiffUnavailable, got %v", err)
	}
}

// TestDiff_BinaryInRepo: a binary file inside a repo reports Binary:true, with
// status "modified" when tracked and "added" when untracked.
func TestDiff_BinaryInRepo(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	if err := os.WriteFile(filepath.Join(root, "t.bin"), []byte{0x00, 0x01}, 0o644); err != nil {
		t.Fatal(err)
	}
	gitCommitAll(t, root, "bin")
	// Modify the tracked binary; add an untracked one.
	if err := os.WriteFile(filepath.Join(root, "t.bin"), []byte{0x00, 0x02, 0x03}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "u.bin"), []byte{0x00, 0xFF}, 0o644); err != nil {
		t.Fatal(err)
	}

	fs := newFS(t, Root{ID: "project", Path: root, Kind: "project"})
	defer fs.Close()

	d, err := fs.Diff(context.Background(), "project", "t.bin")
	if err != nil || !d.Binary || d.Status != "modified" {
		t.Fatalf("tracked binary: %+v err=%v", d, err)
	}
	u, err := fs.Diff(context.Background(), "project", "u.bin")
	if err != nil || !u.Binary || u.Status != "added" {
		t.Fatalf("untracked binary: %+v err=%v", u, err)
	}
}

// TestDiff_OversizeBaselineTruncated: a HEAD blob larger than maxContentBytes is
// reported modified+truncated without building a full diff body.
func TestDiff_OversizeBaselineTruncated(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	big := bytes.Repeat([]byte("a\n"), (maxContentBytes/2)+1024) // > maxContentBytes
	if err := os.WriteFile(filepath.Join(root, "big.txt"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	gitCommitAll(t, root, "big")
	// Shrink the working tree so the current content is tiny/viewable.
	if err := os.WriteFile(filepath.Join(root, "big.txt"), []byte("small\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	fs := newFS(t, Root{ID: "project", Path: root, Kind: "project"})
	defer fs.Close()

	d, err := fs.Diff(context.Background(), "project", "big.txt")
	if err != nil {
		t.Fatalf("oversize baseline: err=%v", err)
	}
	if d.Status != "modified" || !d.Truncated || d.Diff != "" {
		t.Fatalf("oversize baseline → want modified+truncated, empty body; got %+v", d)
	}
}
