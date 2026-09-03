package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// mkTempDirWithFiles creates n empty files "f0".."f{n-1}" inside a fresh temp
// directory and returns it opened for reading.
func mkTempDirWithFiles(t *testing.T, n int) *os.File {
	t.Helper()
	dir := t.TempDir()
	for i := 0; i < n; i++ {
		p := filepath.Join(dir, fmt.Sprintf("f%d", i))
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// TestReadDirBounded exercises the finding-9 bounded scan directly against a
// real directory (portable — os.File.ReadDir, no openat2 involved), so it
// runs on darwin too. It uses a small injected cap rather than the real
// maxDirEntries (50000) — building a directory that size in a unit test would
// be slow and wasteful; the cap value itself doesn't change readDirBounded's
// logic.
func TestReadDirBounded(t *testing.T) {
	t.Run("under cap reads everything, not truncated", func(t *testing.T) {
		f := mkTempDirWithFiles(t, 3)
		entries, truncated, err := readDirBounded(f, 10)
		if err != nil {
			t.Fatalf("readDirBounded: %v", err)
		}
		if len(entries) != 3 {
			t.Errorf("len(entries) = %d, want 3", len(entries))
		}
		if truncated {
			t.Error("truncated = true, want false (directory has fewer entries than the cap)")
		}
	})

	t.Run("over cap stops early and signals truncation", func(t *testing.T) {
		f := mkTempDirWithFiles(t, 10)
		entries, truncated, err := readDirBounded(f, 5)
		if err != nil {
			t.Fatalf("readDirBounded: %v", err)
		}
		if len(entries) != 5 {
			t.Errorf("len(entries) = %d, want 5 (capped)", len(entries))
		}
		if !truncated {
			t.Error("truncated = false, want true (directory has more entries than the cap)")
		}
	})

	t.Run("exact boundary is not falsely truncated", func(t *testing.T) {
		f := mkTempDirWithFiles(t, 5)
		entries, truncated, err := readDirBounded(f, 5)
		if err != nil {
			t.Fatalf("readDirBounded: %v", err)
		}
		if len(entries) != 5 {
			t.Errorf("len(entries) = %d, want 5", len(entries))
		}
		if truncated {
			t.Error("truncated = true, want false (directory size lands exactly on the cap)")
		}
	})

	t.Run("cap smaller than a single batch still works", func(t *testing.T) {
		// dirReadBatch is 4096; this exercises the remaining-vs-batch clamp
		// in readDirBounded's loop with a cap far below one batch.
		f := mkTempDirWithFiles(t, 3)
		entries, truncated, err := readDirBounded(f, 2)
		if err != nil {
			t.Fatalf("readDirBounded: %v", err)
		}
		if len(entries) != 2 {
			t.Errorf("len(entries) = %d, want 2 (capped)", len(entries))
		}
		if !truncated {
			t.Error("truncated = false, want true")
		}
	})
}
