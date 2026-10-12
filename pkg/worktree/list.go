package worktree

import "strings"

// Entry is one record from `git worktree list --porcelain`.
type Entry struct {
	Path     string
	Branch   string // short name, "" when detached or bare
	Detached bool
	Locked   bool
	Prunable bool
}

// ParseList parses `git worktree list --porcelain` output into Entry records.
//
// The porcelain format is a sequence of blocks separated by blank lines; each
// block starts with a "worktree <path>" line followed by attribute lines
// (HEAD/branch/bare/detached/locked/prunable, the latter two with an optional
// trailing reason). This parser only targets that one command's output, not
// general git porcelain.
func ParseList(out string) []Entry {
	entries := make([]Entry, 0)
	var cur *Entry

	flush := func() {
		if cur != nil {
			entries = append(entries, *cur)
			cur = nil
		}
	}

	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			flush()
			continue
		}
		switch {
		case strings.HasPrefix(line, "worktree "):
			flush()
			cur = &Entry{Path: strings.TrimPrefix(line, "worktree ")}
		case cur == nil:
			// Stray attribute line outside any worktree block; ignore.
		case strings.HasPrefix(line, "branch "):
			cur.Branch = strings.TrimPrefix(strings.TrimPrefix(line, "branch "), "refs/heads/")
		case line == "detached":
			cur.Detached = true
		case line == "bare":
			// No branch/HEAD for a bare main worktree.
		case line == "locked" || strings.HasPrefix(line, "locked "):
			cur.Locked = true
		case line == "prunable" || strings.HasPrefix(line, "prunable "):
			cur.Prunable = true
		default:
			// Other attribute lines (HEAD, worktree-order hints, etc.) don't matter here.
		}
	}
	flush()

	return entries
}
