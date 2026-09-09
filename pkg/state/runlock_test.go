package state

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestTryLockRun_MutualExclusionWithStoreOpen_LockFirst(t *testing.T) {
	dir := t.TempDir()

	rl, err := TryLockRun(dir)
	if err != nil {
		t.Fatalf("TryLockRun: %v", err)
	}
	defer rl.Close()

	if _, err := Open(dir, []string{"a"}); !errors.Is(err, ErrRunLocked) {
		t.Fatalf("Store.Open while RunLock held: want ErrRunLocked, got %v", err)
	}
}

func TestTryLockRun_MutualExclusionWithStoreOpen_OpenFirst(t *testing.T) {
	dir := t.TempDir()

	s, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Store.Open: %v", err)
	}
	defer s.Close()

	if _, err := TryLockRun(dir); !errors.Is(err, ErrRunLocked) {
		t.Fatalf("TryLockRun while Store.Open holds: want ErrRunLocked, got %v", err)
	}
}

func TestTryLockRun_ReleasedOnClose(t *testing.T) {
	dir := t.TempDir()

	rl, err := TryLockRun(dir)
	if err != nil {
		t.Fatalf("TryLockRun: %v", err)
	}
	if err := rl.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rl2, err := TryLockRun(dir)
	if err != nil {
		t.Fatalf("TryLockRun after release: %v", err)
	}
	rl2.Close()
}

func TestTryLockRun_CreatesOnlyLockFile(t *testing.T) {
	dir := t.TempDir()

	rl, err := TryLockRun(dir)
	if err != nil {
		t.Fatalf("TryLockRun: %v", err)
	}
	defer rl.Close()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != ".lock" {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Fatalf("expected only .lock, got %v", names)
	}
}

func TestTryLockRun_IOErrorNotRunLocked(t *testing.T) {
	dir := t.TempDir()
	// runDir is a file, not a directory -> genuine I/O error, not contention.
	runDir := filepath.Join(dir, "notadir")
	if err := os.WriteFile(runDir, []byte("x"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := TryLockRun(runDir)
	if err == nil || errors.Is(err, ErrRunLocked) {
		t.Fatalf("want a real I/O error, not ErrRunLocked, got %v", err)
	}
}
