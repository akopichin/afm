//go:build linux

package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestList_OrderHidingSymlinkPagination exercises the real List
// implementation end to end on a live temp root: directories sort before
// files, `.git`/`.afm` are hidden, a symlink is classified and non-selectable,
// and Language is detected for a real file. Pagination itself (cursor
// round-trip across a page boundary) is covered separately in Docker E2E
// (Task 16), where a root with >listPageSize entries is practical to build.
func TestList_OrderHidingSymlinkPagination(t *testing.T) {
	dir := t.TempDir()
	mustMkdirAll(t, filepath.Join(dir, "zeta"))
	mustMkdirAll(t, filepath.Join(dir, ".git"))
	mustMkdirAll(t, filepath.Join(dir, ".afm"))
	mustWriteFile(t, filepath.Join(dir, "Alpha.go"), "package a")
	mustWriteFile(t, filepath.Join(dir, "beta.txt"), "x")
	if err := os.Symlink("/etc", filepath.Join(dir, "link")); err != nil {
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

	p, err := fs.List(context.Background(), "project", ".", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if p.NextCursor != "" {
		t.Errorf("NextCursor = %q, want empty (page under listPageSize)", p.NextCursor)
	}

	var names []string
	for _, e := range p.Entries {
		names = append(names, e.Name)
	}
	got := strings.Join(names, ",")
	if strings.Contains(got, ".git") || strings.Contains(got, ".afm") {
		t.Fatalf(".git/.afm not hidden: %s", got)
	}
	// dirs first (zeta), then files case-insensitive (Alpha.go, beta.txt, link).
	want := "zeta,Alpha.go,beta.txt,link"
	if got != want {
		t.Fatalf("order = %q, want %q", got, want)
	}

	byName := map[string]Entry{}
	for _, e := range p.Entries {
		byName[e.Name] = e
	}
	if e := byName["zeta"]; e.Kind != "directory" || e.Selectable {
		t.Errorf("zeta entry wrong: %+v", e)
	}
	if e := byName["link"]; e.Kind != "symlink" || e.Selectable {
		t.Errorf("symlink entry wrong: %+v", e)
	}
	if e := byName["Alpha.go"]; e.Kind != "file" || !e.Selectable || e.Language != "go" {
		t.Errorf("Alpha.go entry wrong: %+v", e)
	}
	if e := byName["beta.txt"]; e.Kind != "file" || !e.Selectable || e.Language != "plain" {
		t.Errorf("beta.txt entry wrong: %+v", e)
	}

	// A cursor from a different relPath under the same root is rejected.
	if _, err := fs.List(context.Background(), "project", "zeta", "project\x00.\x00zeta"); err == nil {
		t.Error("List with cursor bound to a different relPath must fail")
	}

	// A well-formed cursor bound to this listing resumes correctly: asking to
	// resume after "zeta" must not repeat it.
	p2, err := fs.List(context.Background(), "project", ".", "project\x00.\x00zeta")
	if err != nil {
		t.Fatalf("List with valid cursor: %v", err)
	}
	for _, e := range p2.Entries {
		if e.Name == "zeta" {
			t.Errorf("resume-after cursor must not re-emit zeta: %+v", p2.Entries)
		}
	}
}

// TestList_TruncatesAtMaxDirEntries exercises finding 9's bounded scan
// end-to-end through the real List/openat2 path, not just readDirBounded in
// isolation (bounded_scan_test.go). maxDirEntries is a package var precisely
// so a test can shrink it instead of building a real 50000-entry directory.
func TestList_TruncatesAtMaxDirEntries(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 10; i++ {
		mustWriteFile(t, filepath.Join(dir, "f"+string(rune('0'+i))), "x")
	}

	fs, err := New([]Root{{ID: "project", Path: dir, Kind: "project"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer fs.Close()
	if len(fs.Roots()) == 0 {
		t.Skip("secure file browser unavailable on this kernel (no openat2)")
	}

	orig := maxDirEntries
	maxDirEntries = 4
	t.Cleanup(func() { maxDirEntries = orig })

	p, err := fs.List(context.Background(), "project", ".", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !p.Truncated {
		t.Error("Truncated = false, want true (directory has more raw entries than maxDirEntries)")
	}
	if len(p.Entries) != 4 {
		t.Errorf("len(Entries) = %d, want 4 (capped at maxDirEntries)", len(p.Entries))
	}
}

func mustMkdirAll(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWriteFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
