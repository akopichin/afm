package workspace

import "testing"

func TestRankMatch(t *testing.T) {
	cases := []struct {
		needle, name, path string
		wantScore          int
		wantOK             bool
	}{
		{"workspace.go", "workspace.go", "pkg/workspace.go", 0, true}, // exact basename
		{"work", "workspace.go", "pkg/workspace.go", 1, true},         // basename prefix
		{"space", "workspace.go", "pkg/workspace.go", 2, true},        // basename substring
		{"pkg", "workspace.go", "pkg/workspace.go", 3, true},          // path-only match
		{"folder", "file.ts", "folder/file.ts", 3, true},              // folder name via path
		{"nope", "file.ts", "folder/file.ts", 0, false},               // no match
		{"WORK", "workspace.go", "pkg/workspace.go", 1, true},         // case-insensitive
	}
	for _, c := range cases {
		got, ok := rankMatch(toLowerASCII(c.needle), c.name, c.path)
		if ok != c.wantOK || (ok && got != c.wantScore) {
			t.Errorf("rankMatch(%q,%q,%q) = (%d,%v), want (%d,%v)", c.needle, c.name, c.path, got, ok, c.wantScore, c.wantOK)
		}
	}
}

func TestSortScored(t *testing.T) {
	rs := []scoredEntry{
		{Entry{Path: "z/long/path/match.go"}, 3},
		{Entry{Path: "match.go"}, 0},
		{Entry{Path: "b.go"}, 2},
		{Entry{Path: "a.go"}, 2},
	}
	sortScored(rs)
	var order []string
	for _, r := range rs {
		order = append(order, r.entry.Path)
	}
	// score asc, then shorter path, then lexical
	want := []string{"match.go", "a.go", "b.go", "z/long/path/match.go"}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
}
