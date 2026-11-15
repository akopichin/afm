//go:build unix

package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func replaceWithFIFO(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Error(err)
		return
	}
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Error(err)
	}
}

// returnsWithin runs fn and fails the test if it is still blocked after the deadline. Opening the FIFO's write end
// then releases an open waiting for a writer, so fn can finish before the test ends.
func returnsWithin(t *testing.T, fifo string, fn func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		if w, err := os.OpenFile(fifo, os.O_WRONLY|unix.O_NONBLOCK, 0); err == nil {
			w.Close()
		}
		select {
		case <-done:
		case <-time.After(10 * time.Second):
		}
		t.Fatal("opening a FIFO put in place of a package file blocked")
		return nil
	}
}

func TestBuildSnapshotRefusesFIFOSwappedInBeforeCopy(t *testing.T) {
	root, m := newPackage(t)
	leaf := filepath.Join(root, "skills/a/SKILL.md")
	swapped := false
	onOpenComponent(t, func(rel, prefix string) {
		if rel == "skills/a/SKILL.md" && prefix == rel && !swapped {
			swapped = true
			replaceWithFIFO(t, leaf)
		}
	})
	cache := cacheDir(t)
	err := returnsWithin(t, leaf, func() error {
		_, err := BuildSnapshot(root, m, cache)
		return err
	})
	if !swapped {
		t.Fatal("the copy never opened the swapped file")
	}
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("err = %v, want a refusal of the FIFO", err)
	}
	requireNoSnapshots(t, cache)
}

func TestVerifySnapshotRefusesFIFOSwappedInAfterListing(t *testing.T) {
	root, m := newPackage(t)
	// An empty FIFO reads like the empty file it replaces, so only the opened file's type tells them apart.
	writeFile(t, root, "skills/a/SKILL.md", "")
	snap, err := BuildSnapshot(root, m, cacheDir(t))
	if err != nil {
		t.Fatal(err)
	}
	leaf := filepath.Join(snap.Dir, "skills/a/SKILL.md")
	if err := os.Chmod(filepath.Dir(leaf), 0o755); err != nil {
		t.Fatal(err)
	}
	swapped := false
	onOpenComponent(t, func(rel, prefix string) {
		if rel == "skills/a/SKILL.md" && prefix == rel && !swapped {
			swapped = true
			replaceWithFIFO(t, leaf)
		}
	})
	err = returnsWithin(t, leaf, func() error {
		_, err := VerifySnapshot(snap.Dir)
		return err
	})
	if !swapped {
		t.Fatal("verification never opened the swapped file")
	}
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("err = %v, want a refusal of the FIFO", err)
	}
}
