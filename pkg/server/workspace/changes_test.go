//go:build linux

package workspace

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// gitOut runs a git command in dir and returns its trimmed stdout, failing the
// test on error.
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s", args, out)
	}
	return strings.TrimSpace(string(out))
}

// gitRun runs a git command in dir, failing the test on error.
func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	_ = gitOut(t, dir, args...)
}

func TestMapStatus(t *testing.T) {
	cases := map[byte]struct {
		want ChangeStatus
		ok   bool
	}{
		'A': {ChangeAdded, true},
		'D': {ChangeDeleted, true},
		'M': {ChangeModified, true},
		'T': {ChangeModified, true},
		'U': {ChangeModified, true},
		'X': {"", false},
	}
	for in, exp := range cases {
		got, ok := mapStatus(in)
		if got != exp.want || ok != exp.ok {
			t.Errorf("mapStatus(%q) = (%q,%v), want (%q,%v)", in, got, ok, exp.want, exp.ok)
		}
	}
}

func TestParseNameStatusZ_Basic(t *testing.T) {
	// "M<NUL>a.go<NUL>D<NUL>dir/b.go<NUL>" — two complete records.
	data := []byte("M\x00a.go\x00D\x00dir/b.go\x00")
	recs, err := parseNameStatusZ(data, false)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	want := []nameStatusRec{{'M', "a.go"}, {'D', "dir/b.go"}}
	if !reflect.DeepEqual(recs, want) {
		t.Fatalf("got %+v, want %+v", recs, want)
	}
}

func TestParseNameStatusZ_PathWithWhitespace(t *testing.T) {
	// -z keeps spaces/tabs/newlines literally inside the path field.
	data := []byte("M\x00a b\tc\nd.go\x00")
	recs, err := parseNameStatusZ(data, false)
	if err != nil || len(recs) != 1 || recs[0].path != "a b\tc\nd.go" {
		t.Fatalf("got %+v err %v", recs, err)
	}
}

func TestParseNameStatusZ_UnknownStatusIsError(t *testing.T) {
	if _, err := parseNameStatusZ([]byte("R\x00a.go\x00"), false); err == nil {
		t.Fatal("want error for unknown status R (renames are disabled), got nil")
	}
}

func TestParseNameStatusZ_IncompleteTailRejectedUnlessTruncated(t *testing.T) {
	data := []byte("M\x00a.go\x00D") // dangling status, no path
	if _, err := parseNameStatusZ(data, false); err == nil {
		t.Fatal("want error for incomplete tail when not truncated")
	}
	recs, err := parseNameStatusZ(data, true)
	if err != nil || len(recs) != 1 || recs[0].path != "a.go" {
		t.Fatalf("truncated tail should drop the partial record: got %+v err %v", recs, err)
	}
}

func TestParseUntrackedZ(t *testing.T) {
	got := parseUntrackedZ([]byte("new.go\x00dir/other.go\x00"))
	want := []string{"new.go", "dir/other.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestParseNameStatusZ_TruncatedMidPathDropsRecord(t *testing.T) {
	data := []byte("M\x00a.go\x00D\x00dir/b") // byte-cap cut mid second path
	recs, err := parseNameStatusZ(data, true)
	if err != nil || len(recs) != 1 || recs[0].path != "a.go" {
		t.Fatalf("mid-path truncation should drop the partial record: got %+v err %v", recs, err)
	}
	if _, err := parseNameStatusZ(data, false); err == nil {
		t.Fatal("non-terminated stream without truncated flag must error")
	}
}

func TestParseUntrackedZ_TruncatedMidPathDropsFragment(t *testing.T) {
	got := parseUntrackedZ([]byte("new.go\x00dir/frag")) // no trailing NUL
	if len(got) != 1 || got[0] != "new.go" {
		t.Fatalf("mid-path fragment should be dropped: got %v", got)
	}
}

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRunGitChanges_TrackedModified(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	writeFile(t, dir, "a.go", "package a\n")
	gitCommitAll(t, dir, "init")
	writeFile(t, dir, "a.go", "package a\n// edit\n")

	gitDir := filepath.Join(dir, ".git")
	out, truncated, err := runGitChanges(context.Background(), gitDir, dir,
		"diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--name-status", "-z", "--")
	if err != nil || truncated {
		t.Fatalf("err=%v truncated=%v", err, truncated)
	}
	if !bytes.Contains(out, []byte("a.go")) {
		t.Fatalf("expected a.go in output, got %q", out)
	}
}

func TestRunGitChanges_ByteCapTruncates(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	for i := 0; i < 50; i++ {
		writeFile(t, dir, "f"+strconv.Itoa(i)+".txt", "x")
	}
	old := maxChangesOutputBytes
	maxChangesOutputBytes = 8
	defer func() { maxChangesOutputBytes = old }()

	gitDir := filepath.Join(dir, ".git")
	out, truncated, err := runGitChanges(context.Background(), gitDir, dir,
		"ls-files", "--others", "--exclude-standard", "-z", "--")
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if !truncated || len(out) > 8 {
		t.Fatalf("expected truncated at 8 bytes, got truncated=%v len=%d", truncated, len(out))
	}
}

func TestRunGitChanges_NonZeroExitIsError(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	gitDir := filepath.Join(dir, ".git")
	// A bogus subcommand exits non-zero → strict runner must surface an error,
	// never a silent empty success (contrast with diff.go runGit).
	if _, _, err := runGitChanges(context.Background(), gitDir, dir, "not-a-command"); err == nil {
		t.Fatal("expected error for non-zero git exit")
	}
}

func TestAggregateChanges_SortAndDedup(t *testing.T) {
	tracked := []Change{
		{Name: "b.go", Path: "z/b.go", Status: ChangeModified, Selectable: true},
		{Name: "a.go", Path: "a/a.go", Status: ChangeModified, Selectable: true},
	}
	got, truncated := aggregateChanges(tracked, nil)
	if truncated {
		t.Fatal("unexpected truncation")
	}
	// Full-path case-insensitive sort: a/a.go before z/b.go.
	if got[0].Path != "a/a.go" || got[1].Path != "z/b.go" {
		t.Fatalf("bad order: %+v", got)
	}
}

func TestAggregateChanges_DeletedPlusUntrackedCollapsesToModified(t *testing.T) {
	tracked := []Change{{Name: "x.go", Path: "x.go", Status: ChangeDeleted}}
	untracked := []Change{{Name: "x.go", Path: "x.go", Status: ChangeAdded}}
	got, _ := aggregateChanges(tracked, untracked)
	if len(got) != 1 {
		t.Fatalf("want 1 entry, got %+v", got)
	}
	// aggregateChanges collapses the pair to a single "modified"; Selectable is
	// no longer resolved here (resolveSelectable does that on the capped
	// survivors — verified end-to-end in TestChanges_* against a real repo).
	if got[0].Status != ChangeModified {
		t.Fatalf("want modified, got %+v", got[0])
	}
}

func TestAggregateChanges_TrackedWinsOtherCollisions(t *testing.T) {
	tracked := []Change{{Name: "x.go", Path: "x.go", Status: ChangeModified, Selectable: true}}
	untracked := []Change{{Name: "x.go", Path: "x.go", Status: ChangeAdded, Selectable: true}}
	got, _ := aggregateChanges(tracked, untracked)
	if len(got) != 1 || got[0].Status != ChangeModified {
		t.Fatalf("tracked should win, got %+v", got)
	}
}

func TestAggregateChanges_EntryCapTruncates(t *testing.T) {
	old := maxChangesEntries
	maxChangesEntries = 2
	defer func() { maxChangesEntries = old }()
	tracked := []Change{
		{Path: "a", Name: "a", Status: ChangeModified},
		{Path: "b", Name: "b", Status: ChangeModified},
		{Path: "c", Name: "c", Status: ChangeModified},
	}
	got, truncated := aggregateChanges(tracked, nil)
	if !truncated || len(got) != 2 {
		t.Fatalf("want 2 entries + truncated, got len=%d truncated=%v", len(got), truncated)
	}
}

func TestHeadState(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	gitDir := filepath.Join(dir, ".git")
	if ok, err := headState(context.Background(), gitDir, dir); err != nil || ok {
		t.Fatalf("unborn repo: ok=%v err=%v, want false,nil", ok, err)
	}
	writeFile(t, dir, "a.go", "package a\n")
	gitCommitAll(t, dir, "init")
	if ok, err := headState(context.Background(), gitDir, dir); err != nil || !ok {
		t.Fatalf("after commit: ok=%v err=%v, want true,nil", ok, err)
	}
}

// TestHeadState_CorruptHeadIsError proves the unborn/corrupt distinction: a HEAD
// whose commit object is missing must be a read error, NOT silently treated as
// unborn (which would mislabel a damaged repo's whole tree as "added").
func TestHeadState_CorruptHeadIsError(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	writeFile(t, dir, "a.go", "package a\n")
	gitCommitAll(t, dir, "init")
	gitDir := filepath.Join(dir, ".git")

	// Delete the loose commit object HEAD points at — the branch ref still
	// resolves, but the object can no longer be peeled to a commit.
	sha := gitOut(t, dir, "rev-parse", "HEAD")
	obj := filepath.Join(gitDir, "objects", sha[:2], sha[2:])
	if err := os.Remove(obj); err != nil {
		t.Skipf("commit object not loose (packed?), can't corrupt: %v", err)
	}

	ok, err := headState(context.Background(), gitDir, dir)
	if ok || !errors.Is(err, ErrReadFailed) {
		t.Fatalf("corrupt HEAD: ok=%v err=%v, want false,ErrReadFailed", ok, err)
	}

	// And Changes(head) on a corrupt repo must surface the read error, not the
	// unborn "everything added" listing.
	fs := newFS(t, Root{ID: "r", Label: "root", Path: dir})
	defer fs.Close()
	if _, cerr := fs.Changes(context.Background(), "r", ChangeModeHead); !errors.Is(cerr, ErrReadFailed) {
		t.Fatalf("Changes(head) on corrupt repo: err=%v, want ErrReadFailed", cerr)
	}
}

// TestChanges_StagedDiffersBetweenModes locks in the one semantic difference
// between the two modes: a fully-staged edit (worktree == index) is absent from
// Unstaged (worktree-vs-index) but present in vs HEAD (worktree-vs-HEAD). The
// existing matrix test only exercises unstaged edits, where the modes agree.
func TestChanges_StagedDiffersBetweenModes(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	writeFile(t, dir, "s.go", "package a\n")
	gitCommitAll(t, dir, "init")

	writeFile(t, dir, "s.go", "package a\n// staged edit\n")
	gitRun(t, dir, "add", "s.go") // now worktree == index, but differs from HEAD

	fs := newFS(t, Root{ID: "r", Label: "root", Path: dir})
	defer fs.Close()

	idx, err := fs.Changes(context.Background(), "r", ChangeModeIndex)
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	for _, c := range idx.Entries {
		if c.Path == "s.go" {
			t.Errorf("Unstaged must not list a fully-staged file, got %+v", c)
		}
	}

	head, err := fs.Changes(context.Background(), "r", ChangeModeHead)
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	var found bool
	for _, c := range head.Entries {
		if c.Path == "s.go" {
			found = c.Status == ChangeModified
		}
	}
	if !found {
		t.Errorf("vs HEAD must list the staged edit as modified, got %+v", head.Entries)
	}
}

// TestChanges_IgnoresAmbientGitEnv proves the env-sanitization hardening: an
// ambient GIT_INDEX_FILE pointing at a different repo's index must not leak
// phantom paths into this root's listing.
func TestChanges_IgnoresAmbientGitEnv(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	writeFile(t, dir, "real.go", "package a\n")
	gitCommitAll(t, dir, "init")
	writeFile(t, dir, "real.go", "package a\n// edit\n") // a genuine local change

	// A separate repo whose index records a file that does NOT exist in dir.
	other := t.TempDir()
	gitInit(t, other)
	writeFile(t, other, "phantom.go", "package b\n")
	gitRun(t, other, "add", "phantom.go")

	t.Setenv("GIT_INDEX_FILE", filepath.Join(other, ".git", "index"))

	fs := newFS(t, Root{ID: "r", Label: "root", Path: dir})
	defer fs.Close()

	got, err := fs.Changes(context.Background(), "r", ChangeModeIndex)
	if err != nil {
		t.Fatalf("changes: %v", err)
	}
	for _, c := range got.Entries {
		if c.Path == "phantom.go" {
			t.Fatalf("ambient GIT_INDEX_FILE leaked a foreign path: %+v", c)
		}
	}
}

func TestChanges_Matrix(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	writeFile(t, dir, "tracked.go", "package a\n")
	writeFile(t, dir, "deleteme.go", "package a\n")
	gitCommitAll(t, dir, "init")

	writeFile(t, dir, "tracked.go", "package a\n// edit\n") // modified
	os.Remove(filepath.Join(dir, "deleteme.go"))            // deleted
	writeFile(t, dir, "brand-new.go", "package a\n")        // untracked → added
	writeFile(t, dir, "ignored.log", "x")
	writeFile(t, dir, ".gitignore", "*.log\n")

	fs := newFS(t, Root{ID: "r", Label: "root", Path: dir})
	defer fs.Close()

	for _, mode := range []ChangeMode{ChangeModeIndex, ChangeModeHead} {
		got, err := fs.Changes(context.Background(), "r", mode)
		if err != nil {
			t.Fatalf("mode %s: %v", mode, err)
		}
		byPath := map[string]Change{}
		for _, c := range got.Entries {
			byPath[c.Path] = c
		}
		if c := byPath["tracked.go"]; c.Status != ChangeModified || !c.Selectable {
			t.Errorf("mode %s tracked.go = %+v", mode, c)
		}
		if c := byPath["deleteme.go"]; c.Status != ChangeDeleted || c.Selectable {
			t.Errorf("mode %s deleteme.go = %+v", mode, c)
		}
		if c := byPath["brand-new.go"]; c.Status != ChangeAdded || !c.Selectable {
			t.Errorf("mode %s brand-new.go = %+v", mode, c)
		}
		if _, ok := byPath["ignored.log"]; ok {
			t.Errorf("mode %s: ignored file must not appear", mode)
		}
		if _, ok := byPath[".gitignore"]; !ok {
			t.Errorf("mode %s: new .gitignore should appear as added", mode)
		}
	}
}

func TestChanges_UnbornRepoHeadMode(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir) // no commit → unborn HEAD
	writeFile(t, dir, "a.go", "package a\n")

	fs := newFS(t, Root{ID: "r", Label: "root", Path: dir})
	defer fs.Close()

	got, err := fs.Changes(context.Background(), "r", ChangeModeHead)
	if err != nil {
		t.Fatalf("unborn head: %v", err)
	}
	if len(got.Entries) != 1 || got.Entries[0].Path != "a.go" || got.Entries[0].Status != ChangeAdded {
		t.Fatalf("unborn head should list a.go as added, got %+v", got.Entries)
	}
}

func TestChanges_Errors(t *testing.T) {
	fs := newFS(t, Root{ID: "r", Label: "root", Path: t.TempDir()}) // no .git
	defer fs.Close()

	if _, err := fs.Changes(context.Background(), "r", ChangeModeIndex); !errors.Is(err, ErrDiffUnavailable) {
		t.Errorf("non-repo want ErrDiffUnavailable, got %v", err)
	}
	if _, err := fs.Changes(context.Background(), "r", "bogus"); !errors.Is(err, ErrInvalidRootOrPath) {
		t.Errorf("bad mode want ErrInvalidRootOrPath, got %v", err)
	}
	if _, err := fs.Changes(context.Background(), "nope", ChangeModeIndex); !errors.Is(err, ErrInvalidRootOrPath) {
		t.Errorf("bad root want ErrInvalidRootOrPath, got %v", err)
	}
}

func TestChanges_UnbornRepoExcludesCachedDeleted(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir) // no commit → unborn HEAD
	writeFile(t, dir, "staged-then-gone.go", "package a\n")
	// stage it, then remove it from the worktree before any commit
	for _, args := range [][]string{{"add", "staged-then-gone.go"}} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	if err := os.Remove(filepath.Join(dir, "staged-then-gone.go")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "present.go", "package a\n") // untracked, exists

	fs := newFS(t, Root{ID: "r", Label: "root", Path: dir})
	defer fs.Close()

	got, err := fs.Changes(context.Background(), "r", ChangeModeHead)
	if err != nil {
		t.Fatalf("unborn head: %v", err)
	}
	for _, c := range got.Entries {
		if c.Path == "staged-then-gone.go" {
			t.Fatalf("cached-but-deleted path must be excluded, got %+v", c)
		}
	}
	// the still-present untracked file must survive as added
	var found bool
	for _, c := range got.Entries {
		if c.Path == "present.go" && c.Status == ChangeAdded && c.Selectable {
			found = true
		}
	}
	if !found {
		t.Fatalf("present untracked file should be added+selectable, got %+v", got.Entries)
	}
}

// TestChanges_SymlinkAtChangedPath_NotSelectable is the security-critical case
// for isSelectable: git output is untrusted, so a changed path must not be
// treated as viewable just because git reports it. A tracked regular file
// replaced on disk by a symlink is reported by git as a typechange ('T',
// mapped to ChangeModified) — the entry must still be listed (so the user sees
// something changed there) but Selectable must be false, because the real
// openat2 (RESOLVE_NO_SYMLINKS) refuses to follow the symlink.
func TestChanges_SymlinkAtChangedPath_NotSelectable(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	writeFile(t, dir, "link-me.txt", "regular content\n")
	gitCommitAll(t, dir, "init")

	full := filepath.Join(dir, "link-me.txt")
	if err := os.Remove(full); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", full); err != nil {
		t.Fatal(err)
	}

	fs := newFS(t, Root{ID: "r", Label: "root", Path: dir})
	defer fs.Close()

	got, err := fs.Changes(context.Background(), "r", ChangeModeIndex)
	if err != nil {
		t.Fatalf("Changes: %v", err)
	}
	var found bool
	for _, c := range got.Entries {
		if c.Path == "link-me.txt" {
			found = true
			if c.Selectable {
				t.Fatalf("symlinked changed path must not be selectable, got %+v", c)
			}
		}
	}
	if !found {
		t.Fatalf("symlinked changed path should still be listed, got %+v", got.Entries)
	}
}

// TestValidGitPath unit-tests the untrusted-git-output path filter directly:
// it must drop any path escaping the root, any absolute path, any hidden
// service subtree, any NUL byte, and any invalid-UTF-8 byte sequence — git
// output is attacker-influenced (a malicious repo could report a crafted
// path) and none of this may reach the secure openat layer unfiltered.
func TestValidGitPath(t *testing.T) {
	fs := newFS(t, Root{ID: "r", Label: "root", Path: t.TempDir()})
	defer fs.Close()
	impl, ok := fs.(*fsImpl)
	if !ok {
		t.Fatal("fs is not *fsImpl")
	}

	reject := []string{
		"../escape",
		"/etc/passwd",
		".git/config",
		".afm/x",
		"a\x00b",
		string([]byte{0xff, 0xfe}),
	}
	for _, p := range reject {
		if clean, ok := impl.validGitPath(p); ok {
			t.Errorf("validGitPath(%q) = (%q, true), want ok=false", p, clean)
		}
	}

	clean, ok := impl.validGitPath("dir/file.go")
	if !ok || clean != "dir/file.go" {
		t.Errorf(`validGitPath("dir/file.go") = (%q, %v), want ("dir/file.go", true)`, clean, ok)
	}
}
