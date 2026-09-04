//go:build linux

package workspace

import (
	"reflect"
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
