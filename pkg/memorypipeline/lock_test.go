package memorypipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/progress"
)

// --- MemoryLockPath ----------------------------------------------------

func TestMemoryLockPath_StableForTrailingSlash(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	base := t.TempDir()

	p1, err := MemoryLockPath(base)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := MemoryLockPath(base + string(filepath.Separator))
	if err != nil {
		t.Fatal(err)
	}
	if p1 != p2 {
		t.Fatalf("path differs for trailing slash: %q vs %q", p1, p2)
	}
}

func TestMemoryLockPath_SymlinkAliasSameKey_EvenWhenMemoryDirDoesNotExist(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatal(err)
	}

	// memoryDir itself does not exist under either path — only the ancestor
	// (real/link) exists. MemoryLockPath must still canonicalize to the same
	// key via the existing ancestor.
	viaReal := filepath.Join(realDir, "notyet", "memdir")
	viaLink := filepath.Join(link, "notyet", "memdir")

	p1, err := MemoryLockPath(viaReal)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := MemoryLockPath(viaLink)
	if err != nil {
		t.Fatal(err)
	}
	if p1 != p2 {
		t.Fatalf("symlink alias produced different lock keys: %q (real) vs %q (link)", p1, p2)
	}
}

// --- AcquireMemoryLock ---------------------------------------------------

func TestAcquireMemoryLock_Serializes_SecondBlocksUntilCtxCancel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	memDir := t.TempDir()

	first, err := AcquireMemoryLock(context.Background(), memDir)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	_, err = AcquireMemoryLock(ctx, memDir)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want context.DeadlineExceeded, got %v", err)
	}
}

func TestAcquireMemoryLock_AlreadyCancelledCtx_NeverAcquires(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	memDir := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	l, err := AcquireMemoryLock(ctx, memDir)
	if err == nil {
		l.Close()
		t.Fatal("want error for already-cancelled ctx, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

func TestAcquireMemoryLock_NonBusyErrorPropagatesImmediately(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory write permission is not enforced")
	}
	t.Setenv("HOME", t.TempDir())
	memDir := t.TempDir()

	// Prime ~/.afm/locks (created as a side effect of MemoryLockPath), then
	// strip write permission so the underlying TryLock's O_CREATE open fails
	// with a real I/O error — not lock contention.
	lockPath, err := MemoryLockPath(memDir)
	if err != nil {
		t.Fatal(err)
	}
	locksDir := filepath.Dir(lockPath)
	if err := os.Chmod(locksDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(locksDir, 0o755); err != nil {
			t.Logf("cleanup chmod failed: %v", err)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()
	_, err = AcquireMemoryLock(ctx, memDir)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("want a non-busy error, got nil")
	}
	if errors.Is(err, progress.ErrLockBusy) {
		t.Fatalf("want a genuine I/O error, got ErrLockBusy: %v", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error must propagate immediately, not after ctx deadline: %v", err)
	}
	if elapsed >= 1*time.Second {
		t.Fatalf("AcquireMemoryLock took %v — looks like it retried instead of propagating immediately", elapsed)
	}
}
