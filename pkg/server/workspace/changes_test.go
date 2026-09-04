//go:build linux

package workspace

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
)

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

func TestHeadExists(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	gitDir := filepath.Join(dir, ".git")
	if ok, err := headExists(context.Background(), gitDir, dir); err != nil || ok {
		t.Fatalf("unborn repo: ok=%v err=%v, want false,nil", ok, err)
	}
	writeFile(t, dir, "a.go", "package a\n")
	gitCommitAll(t, dir, "init")
	if ok, err := headExists(context.Background(), gitDir, dir); err != nil || !ok {
		t.Fatalf("after commit: ok=%v err=%v, want true,nil", ok, err)
	}
}
