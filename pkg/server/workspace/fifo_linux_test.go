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

// TestReadReference_FIFO_DoesNotHang is the regression test for finding 4: a
// FIFO opened without O_NONBLOCK blocks until a writer shows up — potentially
// forever, wedging the handler goroutine. Nobody ever opens the write end
// here, so if Read/Reference regressed back to a blocking open this test
// would hang past its own deadline and get killed by `go test`'s timeout
// instead of failing cleanly; the explicit per-call timeout below turns that
// into a fast, readable failure.
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

// TestList_FIFO_NotSelectable exercises finding 4's list-side half: a FIFO in
// a directory listing must never be reported as selectable — the frontend
// must not be able to ask the browser to open something that can hang the
// handler.
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
