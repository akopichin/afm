package worktree

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseList(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []Entry
	}{
		{
			name: "main only",
			in: "worktree /path/to/main\n" +
				"HEAD abc123\n" +
				"branch refs/heads/main\n" +
				"\n",
			want: []Entry{
				{Path: "/path/to/main", Branch: "main"},
			},
		},
		{
			name: "main plus linked",
			in: "worktree /path/to/main\n" +
				"HEAD abc123\n" +
				"branch refs/heads/main\n" +
				"\n" +
				"worktree /path/to/linked\n" +
				"HEAD def456\n" +
				"branch refs/heads/feature\n" +
				"\n",
			want: []Entry{
				{Path: "/path/to/main", Branch: "main"},
				{Path: "/path/to/linked", Branch: "feature"},
			},
		},
		{
			name: "detached",
			in: "worktree /path/to/detached\n" +
				"HEAD 789abc\n" +
				"detached\n" +
				"\n",
			want: []Entry{
				{Path: "/path/to/detached", Branch: "", Detached: true},
			},
		},
		{
			name: "locked without reason",
			in: "worktree /path/to/locked\n" +
				"HEAD aaa111\n" +
				"branch refs/heads/x\n" +
				"locked\n" +
				"\n",
			want: []Entry{
				{Path: "/path/to/locked", Branch: "x", Locked: true},
			},
		},
		{
			name: "locked with reason",
			in: "worktree /path/to/locked\n" +
				"HEAD aaa111\n" +
				"branch refs/heads/x\n" +
				"locked some reason text\n" +
				"\n",
			want: []Entry{
				{Path: "/path/to/locked", Branch: "x", Locked: true},
			},
		},
		{
			name: "prunable without reason",
			in: "worktree /path/to/prunable\n" +
				"HEAD bbb222\n" +
				"branch refs/heads/y\n" +
				"prunable\n" +
				"\n",
			want: []Entry{
				{Path: "/path/to/prunable", Branch: "y", Prunable: true},
			},
		},
		{
			name: "prunable with reason",
			in: "worktree /path/to/prunable\n" +
				"HEAD bbb222\n" +
				"branch refs/heads/y\n" +
				"prunable gitdir file points to non-existent location\n" +
				"\n",
			want: []Entry{
				{Path: "/path/to/prunable", Branch: "y", Prunable: true},
			},
		},
		{
			name: "bare main",
			in: "worktree /path/to/bare\n" +
				"bare\n" +
				"\n",
			want: []Entry{
				{Path: "/path/to/bare", Branch: ""},
			},
		},
		{
			name: "trailing and duplicated blank lines",
			in: "worktree /path/to/main\n" +
				"HEAD abc123\n" +
				"branch refs/heads/main\n" +
				"\n" +
				"\n" +
				"worktree /path/to/linked\n" +
				"HEAD def456\n" +
				"branch refs/heads/feature\n" +
				"\n" +
				"\n" +
				"\n",
			want: []Entry{
				{Path: "/path/to/main", Branch: "main"},
				{Path: "/path/to/linked", Branch: "feature"},
			},
		},
		{
			name: "CRLF line endings",
			in: "worktree /path/to/main\r\n" +
				"HEAD abc123\r\n" +
				"branch refs/heads/main\r\n" +
				"\r\n" +
				"worktree /path/to/linked\r\n" +
				"HEAD def456\r\n" +
				"branch refs/heads/feature\r\n" +
				"\r\n",
			want: []Entry{
				{Path: "/path/to/main", Branch: "main"},
				{Path: "/path/to/linked", Branch: "feature"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseList(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ParseList(%q) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseList_Empty(t *testing.T) {
	got := ParseList("")
	if len(got) != 0 {
		t.Fatalf("ParseList(\"\") = %#v, want empty", got)
	}
	got = ParseList(strings.Repeat("\n", 5))
	if len(got) != 0 {
		t.Fatalf("ParseList of blank lines = %#v, want empty", got)
	}
}
