package workspace

import "testing"

func TestTruncateOnLine(t *testing.T) {
	cases := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"under cap returns unchanged", "line1\nline2\n", 100, "line1\nline2\n"},
		{"cuts at last newline at or before cap", "aaaa\nbbbb\ncccc\n", 10, "aaaa\nbbbb\n"},
		{"no newline before cap returns empty", "aaaaaaaaaa", 4, ""},
		{"exact newline at cap boundary included", "aaaa\nbb", 5, "aaaa\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := truncateOnLine(tc.in, tc.max); got != tc.want {
				t.Errorf("truncateOnLine(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
			}
		})
	}
}
