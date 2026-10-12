//go:build linux

package workspace

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestOpenat_BlocksTraversalAndSymlink(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ok.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "evil")); err != nil {
		t.Fatal(err)
	}

	fs, err := New([]Root{{ID: "project", Path: dir, Kind: "project"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer fs.Close()

	impl, ok := fs.(*fsImpl)
	if !ok {
		t.Fatalf("New returned %T, want *fsImpl", fs)
	}
	if _, ok := impl.byID["project"]; !ok {
		t.Skip("openat2 unavailable on this kernel — New degraded to zero roots (validated in Docker E2E)")
	}

	// (a) The safe file opens and reads back its bytes.
	h, rel, err := impl.resolve("project", "ok.txt")
	if err != nil {
		t.Fatalf("resolve ok.txt: %v", err)
	}
	fd, err := h.openat(rel, unix.O_RDONLY)
	if err != nil {
		t.Fatalf("openat ok.txt: %v", err)
	}
	f := os.NewFile(uintptr(fd), rel)
	got, err := io.ReadAll(f)
	_ = f.Close()
	if err != nil {
		t.Fatalf("read ok.txt: %v", err)
	}
	if string(got) != "hi" {
		t.Fatalf("ok.txt content = %q, want %q", got, "hi")
	}

	// (b) A traversal path is rejected before it can escape the root — both by
	// resolve's validateRelPath and, defensively, by openat's RESOLVE_BENEATH.
	if _, _, err := impl.resolve("project", "../etc/passwd"); err == nil {
		t.Error("resolve must reject ../etc/passwd")
	}
	if fd, err := h.openat("../etc/passwd", unix.O_RDONLY); err == nil {
		_ = closeFD(fd)
		t.Error("openat must reject ../etc/passwd (RESOLVE_BENEATH)")
	}

	// (c) A symlink pointing outside the root returns ErrSymlink.
	hs, rels, err := impl.resolve("project", "evil")
	if err != nil {
		t.Fatalf("resolve evil: %v", err)
	}
	if fd, err := hs.openat(rels, unix.O_RDONLY); !errors.Is(err, ErrSymlink) {
		_ = closeFD(fd)
		t.Errorf("openat symlink = %v, want ErrSymlink", err)
	}

	// An unknown root is an invalid-root error, exercised via resolve.
	if _, _, err := impl.resolve("nope", "ok.txt"); !errors.Is(err, ErrInvalidRootOrPath) {
		t.Errorf("resolve unknown root = %v, want ErrInvalidRootOrPath", err)
	}
}
