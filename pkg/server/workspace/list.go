package workspace

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"strings"
)

// listPageSize caps the number of entries returned by one List call. A
// directory with more entries than this is paginated via NextCursor.
const listPageSize = 500

// maxDirEntries caps entries read before sorting; oversized directories report Page.Truncated. Mutable for
// tests.
var maxDirEntries = 50000

// dirReadBatch is how many entries a single f.ReadDir call reads while
// scanning up to maxDirEntries, so that call itself stays bounded too.
const dirReadBatch = 4096

// Entry.Kind values.
const (
	kindDirectory = "directory"
	kindFile      = "file"
	kindSymlink   = "symlink"
)

// List returns a lazily-paginated, sorted listing of relPath inside rootID: directories first, then files
// and symlinks together, both groups sorted case-insensitively.
func (fs *fsImpl) List(ctx context.Context, rootID, relPath, cursor string) (Page, error) {
	if err := ctx.Err(); err != nil {
		return Page{}, err
	}

	rh, clean, err := fs.resolve(rootID, relPath)
	if err != nil {
		return Page{}, err
	}

	after := ""
	if cursor != "" {
		after, err = decodeCursor(cursor, rootID, clean)
		if err != nil {
			return Page{}, err
		}
	}

	fd, err := rh.openat(clean, openDirReadOnly)
	if err != nil {
		return Page{}, err
	}
	f := os.NewFile(uintptr(fd), clean)
	defer f.Close()

	dirents, truncated, err := readDirBounded(ctx, f, maxDirEntries)
	if err != nil {
		if ctx.Err() != nil {
			return Page{}, ctx.Err()
		}
		return Page{}, fmt.Errorf("%w: %v", ErrReadFailed, err)
	}

	entries := buildEntries(clean, dirents)
	sortEntries(entries)
	entries = applyCursor(entries, after)

	page := Page{Entries: entries, Truncated: truncated}
	if len(page.Entries) > listPageSize {
		last := page.Entries[listPageSize-1]
		page.Entries = page.Entries[:listPageSize]
		page.NextCursor = encodeCursor(rootID, clean, last.Name)
	}
	return page, nil
}

// readDirBounded reads up to max entries from f in bounded batches of dirReadBatch, instead of a single
// f.ReadDir that reads the entire directory regardless of size.
func readDirBounded(ctx context.Context, f *os.File, limit int) (entries []os.DirEntry, truncated bool, err error) {
	for len(entries) < limit {
		if cerr := ctx.Err(); cerr != nil {
			return entries, false, cerr
		}
		n := dirReadBatch
		if remaining := limit - len(entries); remaining < n {
			n = remaining
		}
		chunk, derr := f.ReadDir(n)
		entries = append(entries, chunk...)
		if derr != nil {
			if errors.Is(derr, io.EOF) {
				return entries, false, nil
			}
			return entries, false, derr
		}
	}
	// The cap was reached exactly when the directory also ended — probe one
	// more entry so an exact-boundary directory isn't flagged truncated.
	if _, derr := f.ReadDir(1); derr == nil {
		return entries, true, nil
	}
	return entries, false, nil
}

// buildEntries converts directory entries into Entry values, skipping the
// hidden service subtrees `.git`/`.afm`.
func buildEntries(dirRelPath string, dirents []os.DirEntry) []Entry {
	entries := make([]Entry, 0, len(dirents))
	for _, d := range dirents {
		name := d.Name()
		if name == hiddenGitDir || name == hiddenAfmDir {
			continue
		}
		entries = append(entries, buildEntry(dirRelPath, d, name))
	}
	return entries
}

func buildEntry(dirRelPath string, d os.DirEntry, name string) Entry {
	kind := kindFile
	regular := true
	switch t := d.Type(); {
	case d.IsDir():
		kind = kindDirectory
		regular = false
	case t&os.ModeSymlink != 0:
		kind = kindSymlink
		regular = false
	case t&os.ModeType != 0:
		// Anything else non-zero here is a special file — FIFO, socket, device, char device.
		regular = false
	default:
		// A regular file: kind and regular already hold their zero-value
		// defaults set above.
	}

	e := Entry{
		Name:       name,
		Path:       path.Join(dirRelPath, name),
		Kind:       kind,
		Selectable: regular,
	}
	if regular {
		// A concurrent removal between readdir and stat is not fatal — the
		// entry is still listed, just without a known size.
		if info, err := d.Info(); err == nil {
			e.Size = info.Size()
		}
		e.Language = detectLanguage(name)
	}
	return e
}

// sortEntries orders directories before files/symlinks, then case-insensitively by name within each group,
// with the original name as a stable tie-break.
func sortEntries(entries []Entry) {
	slices.SortFunc(entries, func(a, b Entry) int {
		if ad, bd := a.Kind == kindDirectory, b.Kind == kindDirectory; ad != bd {
			if ad {
				return -1
			}
			return 1
		}
		if c := cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
			return c
		}
		return cmp.Compare(a.Name, b.Name)
	})
}

// applyCursor drops entries up to and including the one named after, giving resume-after semantics.
func applyCursor(entries []Entry, after string) []Entry {
	if after == "" {
		return entries
	}
	for i, e := range entries {
		if e.Name == after {
			return entries[i+1:]
		}
	}
	return entries
}

// cursorSep separates the rootID/relPath/name fields of an opaque cursor.
const cursorSep = "\x00"

// encodeCursor binds a resume position to the exact root+relPath it was produced from, so a cursor
// accidentally reused against a different listing is cheaply detectable in decodeCursor.
func encodeCursor(rootID, relPath, name string) string {
	return rootID + cursorSep + relPath + cursorSep + name
}

// decodeCursor validates that cursor was produced for this exact rootID+relPath and returns the
// resume-after name.
func decodeCursor(cursor, rootID, relPath string) (string, error) {
	parts := strings.SplitN(cursor, cursorSep, 3)
	if len(parts) != 3 || parts[0] != rootID || parts[1] != relPath {
		return "", ErrInvalidRootOrPath
	}
	return parts[2], nil
}
