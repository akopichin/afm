package fsutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestAtomicWriteFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sub", "f.md")
	if err := AtomicWriteFile(p, []byte("v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWriteFile(p, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(p)
	if err != nil || string(got) != "v2" {
		t.Fatalf("got %q, %v", got, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o644 {
			t.Fatalf("mode %v, want 0644", info.Mode().Perm())
		}
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, "sub", "*.tmp-*"))
	if len(leftovers) != 0 {
		t.Fatalf("temp files left behind: %v", leftovers)
	}
}
