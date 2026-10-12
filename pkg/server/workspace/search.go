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

// rankMatch ranks case-insensitive matches: exact basename, basename prefix, basename substring, then path
// substring.
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

// sortScored orders by match tier, shorter path, then lexical path for deterministic results.
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

// Search bounds and the search-only heavy-dir skip set.
const maxSearchQueryBytes = 256

// maxSearchScan bounds how many directory entries one Search walks before giving up; maxSearchResults caps
// the ranked window returned.
var (
	maxSearchScan    = 100_000
	maxSearchResults = 200
)

var searchSkipDirs = map[string]bool{
	"node_modules": true,
	"vendor":       true,
	"dist":         true,
	"build":        true,
	".next":        true,
	"target":       true,
}

// Search walks rootID iteratively, matching query case-insensitively against each file's full relative
// path. Ranking is rankMatch/sortScored (see above).
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
	for len(queue) > 0 {
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
		// readDirBounded reads at most `remaining` entries and checks ctx between batches; dirTruncated means
		// this directory had MORE entries than the remaining budget allowed.
		dirents, dirTruncated, rerr := readDirBounded(ctx, f, remaining)
		_ = f.Close()
		if rerr != nil {
			// A cancelled context aborts the whole search; any other read error
			// just skips this one directory.
			if ctx.Err() != nil {
				return SearchResult{}, ctx.Err()
			}
			continue
		}

		for i, d := range dirents {
			// Honor cancellation periodically while processing a large batch —
			// the frontend aborts on every keystroke and root switch.
			if i%1024 == 0 {
				if err := ctx.Err(); err != nil {
					return SearchResult{}, err
				}
			}
			scanned++
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
			// Rank on name/path BEFORE buildEntry, so buildEntry's per-file stat (DirEntry.Info for size) runs only
			// for actual matches, not for every scanned file.
			relPath := path.Join(dirRel, name)
			score, ok := rankMatch(needle, name, relPath)
			if !ok {
				continue
			}
			e := buildEntry(dirRel, d, name)
			if !e.Selectable { // symlink / FIFO / socket / device
				continue
			}
			results = append(results, scoredEntry{entry: e, score: score})
		}

		// The budget is checked AFTER fully processing the directory, so an entry landing exactly on the
		// boundary is still ranked — no off-by-one.
		if dirTruncated {
			truncated = true
			break
		}
		if scanned >= maxSearchScan {
			truncated = len(queue) > 0
			break
		}
	}

	sortScored(results)
	if len(results) > maxSearchResults {
		results = results[:maxSearchResults]
		truncated = true // more matches existed than we return
	}
	entries := make([]Entry, len(results))
	for i, r := range results {
		entries[i] = r.entry
	}
	return SearchResult{Entries: entries, Truncated: truncated}, nil
}
