//go:build linux

package workspace

import (
	"context"
	"fmt"
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

// TestSearch_Top200IsGloballyRanked guards finding #1: the returned window must
// be the globally best-ranked matches, not the first maxSearchResults met in
// filesystem order. A strong prefix match is placed DEEP (visited last by the
// breadth-first walk) behind many weak path-only matches; it must still appear,
// and appear first.
func TestSearch_Top200IsGloballyRanked(t *testing.T) {
	dir := t.TempDir()
	// A depth-1 directory whose NAME contains the query → every file inside is
	// a tier-3 (path-only) match, all scanned before deeper levels.
	weakDir := filepath.Join(dir, "needle_dir")
	mustMkdirAll(t, weakDir)
	const weakCount = 12
	for i := 0; i < weakCount; i++ {
		mustWriteFile(t, filepath.Join(weakDir, fmt.Sprintf("f%03d.txt", i)), "x")
	}
	// The strong match sits three levels deep, so BFS reaches it only after all
	// the shallow weak matches: basename "needle.go" is a tier-1 prefix match.
	deep := filepath.Join(dir, "sub", "deep")
	mustMkdirAll(t, deep)
	mustWriteFile(t, filepath.Join(deep, "needle.go"), "package deep")

	fs := newSearchFS(t, dir)

	orig := maxSearchResults
	maxSearchResults = 5 // smaller than weakCount, so the cap is exercised
	t.Cleanup(func() { maxSearchResults = orig })

	res, err := fs.Search(context.Background(), "project", "needle")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res.Entries) != maxSearchResults {
		t.Fatalf("len(Entries) = %d, want %d (capped)", len(res.Entries), maxSearchResults)
	}
	if got := res.Entries[0].Path; got != "sub/deep/needle.go" {
		t.Errorf("Entries[0].Path = %q, want the tier-1 match sub/deep/needle.go — top-N is not globally ranked", got)
	}
	if !res.Truncated {
		t.Error("Truncated = false, want true (more matches than maxSearchResults)")
	}
}

// TestSearch_ScanBudgetBoundary guards finding #3: an entry landing exactly on
// the scan budget is still processed (no off-by-one), and a tree that fits
// exactly within the budget is NOT falsely flagged truncated.
func TestSearch_ScanBudgetBoundary(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"amatch.go", "bmatch.go", "cmatch.go"} {
		mustWriteFile(t, filepath.Join(dir, name), "x")
	}
	fs := newSearchFS(t, dir)

	t.Run("budget equals entry count: all matches, not truncated", func(t *testing.T) {
		orig := maxSearchScan
		maxSearchScan = 3
		t.Cleanup(func() { maxSearchScan = orig })

		res, err := fs.Search(context.Background(), "project", "match")
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if len(res.Entries) != 3 {
			t.Errorf("len(Entries) = %d, want 3 (entry on the exact boundary must be processed)", len(res.Entries))
		}
		if res.Truncated {
			t.Error("Truncated = true, want false (the whole tree fit exactly within the budget)")
		}
	})

	t.Run("budget below entry count: truncated", func(t *testing.T) {
		orig := maxSearchScan
		maxSearchScan = 2
		t.Cleanup(func() { maxSearchScan = orig })

		res, err := fs.Search(context.Background(), "project", "match")
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if !res.Truncated {
			t.Error("Truncated = false, want true (budget below the number of entries)")
		}
	})
}

// TestSearch_CancelledContextAborts guards finding #2: a cancelled context
// stops the walk with the context error instead of returning partial results.
func TestSearch_CancelledContextAborts(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 20; i++ {
		mustWriteFile(t, filepath.Join(dir, fmt.Sprintf("match%02d.go", i)), "x")
	}
	fs := newSearchFS(t, dir)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fs.Search(ctx, "project", "match"); err == nil {
		t.Error("Search with a cancelled context must return an error")
	}
}
