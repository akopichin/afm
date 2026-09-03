package workspace

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"path"
	"slices"
	"strings"
	"syscall"
)

// listPageSize caps the number of entries returned by one List call. A
// directory with more entries than this is paginated via NextCursor.
const listPageSize = 500

// Entry.Kind values.
const (
	kindDirectory = "directory"
	kindFile      = "file"
	kindSymlink   = "symlink"
)

// List returns a lazily-paginated, sorted listing of relPath inside rootID:
// directories first, then files and symlinks together, both groups sorted
// case-insensitively (original name as tie-break). `.git`/`.afm` entries are
// hidden. A symlink is classified as kind "symlink" and is never selectable —
// its target is never followed (the directory itself is opened securely via
// rootHandle.openat, so a symlinked directory component can't be entered
// either; here it's only ever a leaf classification).
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

	fd, err := rh.openat(clean, syscall.O_RDONLY|syscall.O_DIRECTORY)
	if err != nil {
		return Page{}, err
	}
	f := os.NewFile(uintptr(fd), clean)
	defer f.Close()

	dirents, err := f.ReadDir(-1)
	if err != nil {
		return Page{}, fmt.Errorf("%w: %v", ErrReadFailed, err)
	}

	entries := buildEntries(clean, dirents)
	sortEntries(entries)
	entries = applyCursor(entries, after)

	page := Page{Entries: entries}
	if len(page.Entries) > listPageSize {
		last := page.Entries[listPageSize-1]
		page.Entries = page.Entries[:listPageSize]
		page.NextCursor = encodeCursor(rootID, clean, last.Name)
	}
	return page, nil
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
	if d.IsDir() {
		kind = kindDirectory
	} else if d.Type()&os.ModeSymlink != 0 {
		kind = kindSymlink
	}

	e := Entry{
		Name:       name,
		Path:       path.Join(dirRelPath, name),
		Kind:       kind,
		Selectable: kind == kindFile,
	}
	if kind == kindFile {
		// A concurrent removal between readdir and stat is not fatal — the
		// entry is still listed, just without a known size.
		if info, err := d.Info(); err == nil {
			e.Size = info.Size()
		}
		e.Language = detectLanguage(name)
	}
	return e
}

// sortEntries orders directories before files/symlinks, then
// case-insensitively by name within each group, with the original name as a
// stable tie-break (filenames are unique within a directory, so this is a
// total order).
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

// applyCursor drops entries up to and including the one named after, giving
// resume-after semantics. A name that no longer exists in the current listing
// (e.g. deleted between pages) is not an error — the listing simply resumes
// from the start, matching the cursor's binding contract enforced separately
// by decodeCursor.
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

// cursorSep separates the rootID/relPath/name fields of an opaque cursor. It
// can't appear in any of the three fields: relPath is validated by
// validateRelPath (which rejects NUL bytes), and root ids/file names never
// contain NUL either.
const cursorSep = "\x00"

// encodeCursor binds a resume position to the exact root+relPath it was
// produced from, so a cursor accidentally (or maliciously) reused against a
// different listing is cheaply detectable in decodeCursor.
func encodeCursor(rootID, relPath, name string) string {
	return rootID + cursorSep + relPath + cursorSep + name
}

// decodeCursor validates that cursor was produced for this exact
// rootID+relPath and returns the resume-after name. Any structural mismatch —
// wrong shape, wrong root, wrong path — is rejected as ErrInvalidRootOrPath
// rather than silently ignored, since a mismatched cursor almost always means
// the client is paging through the wrong listing.
func decodeCursor(cursor, rootID, relPath string) (string, error) {
	parts := strings.SplitN(cursor, cursorSep, 3)
	if len(parts) != 3 || parts[0] != rootID || parts[1] != relPath {
		return "", ErrInvalidRootOrPath
	}
	return parts[2], nil
}
