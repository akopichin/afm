package workspace

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// parentDir is the ".." path segment, named so goconst does not flag its reuse in the containment check.
const parentDir = ".."

// linkedWorktree is one git worktree that lives physically beneath the browse root. Head (the worktree's
// committed tip SHA) is enough for both merge-base and diff — no branch name is needed, which also makes a
// detached worktree usable and avoids a name→commit race.
type linkedWorktree struct {
	RelDir string // slash path from the root, e.g. ".worktrees/myflow"
	Head   string // committed HEAD sha
}

// enumerateLinkedWorktrees lists the linked worktrees that sit strictly beneath rootAbs. It parses
// `git worktree list --porcelain -z` and keeps only entries that resolve (via EvalSymlinks) to a path
// physically under the root — the main worktree (the root itself) and any worktree outside the root are
// excluded. The `-z` form is required: plain `--porcelain` C-quotes paths with unusual bytes (non-ASCII,
// spaces), which would break EvalSymlinks; `-z` emits raw NUL-terminated fields. Returns truncated=true if
// the (byte-capped) git output overflowed, and a non-nil error only on a git/exec failure (callers decide
// whether to degrade to empty or surface it).
func enumerateLinkedWorktrees(ctx context.Context, gitDir, workTree, rootAbs string) (res []linkedWorktree, truncated bool, err error) {
	out, overflow, gerr := runGitChanges(ctx, gitDir, workTree, "worktree", "list", "--porcelain", "-z")
	if gerr != nil {
		return nil, false, gerr
	}
	evalRoot, serr := filepath.EvalSymlinks(rootAbs)
	if serr != nil {
		return nil, overflow, nil // root unresolvable → no worktrees, but not a git error
	}

	var curPath, curHead string
	flush := func() {
		defer func() { curPath, curHead = "", "" }()
		if curPath == "" || curHead == "" {
			return
		}
		if rel, ok := relUnderRoot(evalRoot, curPath); ok {
			res = append(res, linkedWorktree{RelDir: rel, Head: curHead})
		}
	}
	// `-z` fields are NUL-terminated `key value`; an empty field separates worktree records.
	for _, f := range splitZ(out) {
		field := string(f)
		switch {
		case field == "":
			flush()
		case strings.HasPrefix(field, "worktree "):
			curPath = strings.TrimPrefix(field, "worktree ")
		case strings.HasPrefix(field, "HEAD "):
			curHead = strings.TrimPrefix(field, "HEAD ")
		default:
			// Other porcelain fields (branch/detached/bare/locked/…) are not needed here.
		}
	}
	flush() // the final record's separator may have been dropped as the trailing empty
	return res, overflow, nil
}

// HasWorktrees reports whether rootID physically contains at least one linked git worktree.
func (fs *fsImpl) HasWorktrees(ctx context.Context, rootID string) bool {
	rh, _, err := fs.resolve(rootID, ".")
	if err != nil {
		return false
	}
	gitDir, _, ok := findGitDir(rh, ".")
	if !ok {
		return false
	}
	// workTree == rootAbs here: the main worktree IS the root, so the git --work-tree and the containment
	// anchor coincide. A git error degrades to "no worktrees" — this is only a button-visibility probe.
	wts, _, err := enumerateLinkedWorktrees(ctx, gitDir, rh.root.Path, rh.root.Path)
	return err == nil && len(wts) > 0
}

// firstLine returns the first line of a trimmed git output (e.g. `merge-base` may print several bases).
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// forkPoint returns the merge-base of topicRef and mainRef in gitDir's object store — the branch's fork
// point. It is the single definition of "the baseline" shared by the per-file diff and the "vs base" list,
// so the two views cannot drift. ref is "" with err==nil when there is no common ancestor (unrelated
// histories); err is non-nil only on a git/exec failure — kept distinct from "no ancestor" so the list can
// surface a real failure while the per-file diff degrades to HEAD.
func forkPoint(ctx context.Context, gitDir, topicRef, mainRef string) (ref string, err error) {
	// merge-base exit codes carry the meaning: 0 = base printed, 1 = valid refs with no common ancestor
	// (benign — unrelated histories), anything else (e.g. 128 for a bad/corrupt ref) is a real failure that
	// must not be mistaken for "no base". runGit would flatten 1 and 128 together, so use runGitExit.
	out, code, gerr := runGitExit(ctx, gitDir, "merge-base", topicRef, mainRef)
	if gerr != nil {
		return "", gerr
	}
	switch code {
	case 0:
		return firstLine(string(out)), nil // several bases possible in criss-cross history — the first is enough
	case 1:
		return "", nil // no common ancestor
	default:
		return "", fmt.Errorf("git merge-base exited %d", code)
	}
}

// relUnderRoot returns the slash relative path of wtPath under evalRoot, but only if wtPath resolves to a
// location physically beneath the root (never the root itself, never an escape). The check is on the
// symlink-resolved paths so a symlinked component cannot hide an outside worktree or exclude a valid one.
func relUnderRoot(evalRoot, wtPath string) (string, bool) {
	evalWT, err := filepath.EvalSymlinks(wtPath)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(evalRoot, evalWT)
	if err != nil {
		return "", false
	}
	if rel == "." || rel == parentDir || strings.HasPrefix(rel, parentDir+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}
