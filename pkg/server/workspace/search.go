package workspace

import (
	"cmp"
	"context"
	"os"
	"path"
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
// the full path is the final tie-break and paths are unique within a root,
// so this is a total order over distinct paths and the result is deterministic.
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

// Search bounds and the search-only heavy-dir skip set. These dirs are skipped
// ONLY by search (to keep the scan budget on files the user is likely after) —
// List/tree still lets you browse into them by hand.
const (
	maxSearchQueryBytes = 256
	maxSearchResults    = 200
)

// maxSearchScan bounds how many directory entries one Search walks before
// giving up (Truncated=true). A var so tests can shrink it instead of building
// a 100000-entry tree.
var maxSearchScan = 100_000

var searchSkipDirs = map[string]bool{
	"node_modules": true,
	"vendor":       true,
	"dist":         true,
	"build":        true,
	".next":        true,
	"target":       true,
}

// Search walks rootID iteratively (a queue of root-relative dir paths, no Go
// recursion), matching query case-insensitively against each file's full
// relative path. It reuses the secure openat2 path (rootHandle.openat) so
// containment/symlink policy stays the kernel's job. Only regular selectable
// files are returned; `.git`/`.afm` and the heavy-dir skip set are pruned.
// The scan is bounded by maxSearchScan entries and maxSearchResults hits; any
// limit sets Truncated. Ranking is rankMatch/sortScored (see search.go).
func (fs *fsImpl) Search(ctx context.Context, rootID, query string) (SearchResult, error) {
	if err := ctx.Err(); err != nil {
		return SearchResult{}, err
	}
	if query == "" || len(query) > maxSearchQueryBytes {
		return SearchResult{}, ErrInvalidRootOrPath
	}
	rh, _, err := fs.resolve(rootID, ".")
	if err != nil {
		return SearchResult{}, err
	}
	needle := toLowerASCII(query)

	var (
		results   []scoredEntry
		scanned   int
		truncated bool
	)
	queue := []string{"."}
	for len(queue) > 0 && !truncated {
		if err := ctx.Err(); err != nil {
			return SearchResult{}, err
		}
		dirRel := queue[0]
		queue = queue[1:]

		fd, err := rh.openat(dirRel, openDirReadOnly)
		if err != nil {
			// A dir that vanished / turned into a symlink mid-walk is skipped,
			// not fatal — search is best-effort over a live working tree.
			continue
		}
		f := os.NewFile(uintptr(fd), dirRel)
		remaining := maxSearchScan - scanned
		dirents, _, rerr := readDirBounded(f, remaining)
		_ = f.Close()
		if rerr != nil {
			continue
		}

		for _, d := range dirents {
			scanned++
			if scanned >= maxSearchScan {
				truncated = true
				break
			}
			name := d.Name()
			if name == hiddenGitDir || name == hiddenAfmDir {
				continue
			}
			if d.IsDir() {
				if !searchSkipDirs[name] {
					queue = append(queue, path.Join(dirRel, name))
				}
				continue
			}
			e := buildEntry(dirRel, d, name)
			if !e.Selectable { // symlink / FIFO / socket / device
				continue
			}
			score, ok := rankMatch(needle, name, e.Path)
			if !ok {
				continue
			}
			results = append(results, scoredEntry{entry: e, score: score})
			if len(results) >= maxSearchResults {
				truncated = true
				break
			}
		}
	}

	sortScored(results)
	entries := make([]Entry, len(results))
	for i, r := range results {
		entries[i] = r.entry
	}
	return SearchResult{Entries: entries, Truncated: truncated}, nil
}
