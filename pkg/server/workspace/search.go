package workspace

import (
	"cmp"
	"slices"
	"strings"
)

// scoredEntry pairs a matched file Entry with its rank tier (lower = better).
type scoredEntry struct {
	entry Entry
	score int
}

// toLowerASCII lowercases for case-insensitive matching. strings.ToLower is
// fine for our filenames; a dedicated name documents intent at call sites.
func toLowerASCII(s string) string { return strings.ToLower(s) }

// rankMatch classifies how well needle (already lowercased) matches a file,
// returning a tier and whether it matched at all:
//
//	0 — basename equals the query exactly
//	1 — basename starts with the query
//	2 — basename contains the query
//	3 — the query appears only elsewhere in the relative path
//
// name is the basename, fullPath the root-relative path; both are lowercased
// here for the comparison.
func rankMatch(needle, name, fullPath string) (int, bool) {
	ln := strings.ToLower(name)
	switch {
	case ln == needle:
		return 0, true
	case strings.HasPrefix(ln, needle):
		return 1, true
	case strings.Contains(ln, needle):
		return 2, true
	case strings.Contains(strings.ToLower(fullPath), needle):
		return 3, true
	default:
		return 0, false
	}
}

// sortScored orders results by tier, then by shorter path, then lexically —
// a total, stable order (paths are unique within a root).
func sortScored(rs []scoredEntry) {
	slices.SortFunc(rs, func(a, b scoredEntry) int {
		if a.score != b.score {
			return cmp.Compare(a.score, b.score)
		}
		if la, lb := len(a.entry.Path), len(b.entry.Path); la != lb {
			return cmp.Compare(la, lb)
		}
		return cmp.Compare(a.entry.Path, b.entry.Path)
	})
}
