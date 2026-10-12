//go:build linux

package workspace

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestReadReference_FIFO_DoesNotHang(t *testing.T) {
	dir := t.TempDir()
	fifoPath := filepath.Join(dir, "pipe")
	if err := unix.Mkfifo(fifoPath, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	fs, err := New([]Root{{ID: "project", Path: dir, Kind: "project"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if len(fs.Roots()) == 0 {
		t.Skip("secure file browser unavailable on this kernel (no openat2)")
	}
	defer fs.Close()

	t.Run("Read", func(t *testing.T) {
		done := make(chan error, 1)
		go func() {
			_, err := fs.Read(context.Background(), "project", "pipe")
			done <- err
		}()
		select {
		case err := <-done:
			if !errors.Is(err, ErrNotFound) {
				t.Errorf("Read(fifo) = %v, want ErrNotFound", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Read(fifo) blocked — O_NONBLOCK regression (would hang forever without a writer)")
		}
	})

	t.Run("Reference", func(t *testing.T) {
		done := make(chan error, 1)
		go func() {
			_, err := fs.Reference(context.Background(), "project", "pipe")
			done <- err
		}()
		select {
		case err := <-done:
			if !errors.Is(err, ErrNotFound) {
				t.Errorf("Reference(fifo) = %v, want ErrNotFound", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Reference(fifo) blocked — O_NONBLOCK regression (would hang forever without a writer)")
		}
	})
}

func TestList_FIFO_NotSelectable(t *testing.T) {
	dir := t.TempDir()
	fifoPath := filepath.Join(dir, "pipe")
	if err := unix.Mkfifo(fifoPath, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	fs, err := New([]Root{{ID: "project", Path: dir, Kind: "project"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if len(fs.Roots()) == 0 {
		t.Skip("secure file browser unavailable on this kernel (no openat2)")
	}
	defer fs.Close()

	p, err := fs.List(context.Background(), "project", ".", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(p.Entries) != 1 {
		t.Fatalf("Entries = %+v, want exactly the fifo", p.Entries)
	}
	e := p.Entries[0]
	if e.Name != "pipe" {
		t.Fatalf("entry name = %q, want %q", e.Name, "pipe")
	}
	if e.Selectable {
		t.Errorf("fifo entry must not be selectable: %+v", e)
	}
}
