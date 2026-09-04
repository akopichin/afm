//go:build linux

package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func newSearchFS(t *testing.T, dir string) FS {
	t.Helper()
	fs, err := New([]Root{{ID: "project", Path: dir, Kind: "project"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	if len(fs.Roots()) == 0 {
		t.Skip("secure file browser unavailable on this kernel (no openat2)")
	}
	return fs
}

func TestSearch_MatchesRankingHidingAndSkips(t *testing.T) {
	dir := t.TempDir()
	mustMkdirAll(t, filepath.Join(dir, "pkg", "server", "workspace"))
	mustMkdirAll(t, filepath.Join(dir, "node_modules", "left-pad"))
	mustMkdirAll(t, filepath.Join(dir, ".git"))
	mustWriteFile(t, filepath.Join(dir, "pkg", "server", "workspace", "workspace.go"), "package w")
	mustWriteFile(t, filepath.Join(dir, "pkg", "server", "workspace", "list.go"), "package w")
	mustWriteFile(t, filepath.Join(dir, "node_modules", "left-pad", "workspace.js"), "x") // must be skipped
	mustWriteFile(t, filepath.Join(dir, ".git", "workspace"), "x")                        // must be hidden
	if err := os.Symlink("/etc", filepath.Join(dir, "workspace-link")); err != nil {
		t.Fatal(err)
	}

	fs := newSearchFS(t, dir)

	res, err := fs.Search(context.Background(), "project", "workspace")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	var paths []string
	for _, e := range res.Entries {
		paths = append(paths, e.Path)
		if !e.Selectable || e.Kind != "file" {
			t.Errorf("non-file/non-selectable in results: %+v", e)
		}
	}
	// workspace.go (basename match, tier ≤2) ranks above list.go (path-only, tier 3).
	// node_modules/*, .git/*, and the symlink never appear.
	want := []string{"pkg/server/workspace/workspace.go", "pkg/server/workspace/list.go"}
	if len(paths) != len(want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("paths = %v, want %v", paths, want)
		}
	}
	if e := res.Entries[0]; e.Language != "go" {
		t.Errorf("language not detected: %+v", e)
	}
}

func TestSearch_RejectsBadQuery(t *testing.T) {
	fs := newSearchFS(t, t.TempDir())
	if _, err := fs.Search(context.Background(), "project", ""); err == nil {
		t.Error("empty query must error")
	}
	long := make([]byte, maxSearchQueryBytes+1)
	for i := range long {
		long[i] = 'a'
	}
	if _, err := fs.Search(context.Background(), "project", string(long)); err == nil {
		t.Error("over-long query must error")
	}
	if _, err := fs.Search(context.Background(), "nope", "x"); err == nil {
		t.Error("unknown root must error")
	}
}

func TestSearch_TruncatesAtScanBudget(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 10; i++ {
		mustWriteFile(t, filepath.Join(dir, "match"+string(rune('0'+i))+".go"), "x")
	}
	fs := newSearchFS(t, dir)

	orig := maxSearchScan
	maxSearchScan = 3
	t.Cleanup(func() { maxSearchScan = orig })

	res, err := fs.Search(context.Background(), "project", "match")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if !res.Truncated {
		t.Error("Truncated = false, want true (scan budget exceeded)")
	}
}
