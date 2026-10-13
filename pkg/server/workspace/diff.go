package workspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/akopichin/afm/pkg/gitcmd"
	"github.com/aymanbagabas/go-udiff"
)

// maxDiffBytes caps inline unified diffs, truncating at a line boundary with Truncated=true.
const maxDiffBytes = 4 << 20

// gitTimeout bounds every git subprocess this package spawns. git reads a single blob from the local
// object store — anything slower than this signals a wedged repo, not real work.
const gitTimeout = 3 * time.Second

// Diff.Status values.
const (
	statusModified = "modified"
	statusAdded    = "added"
	statusClean    = "clean"
)

// Diff computes the unified diff of relPath against its baseline — HEAD for a normal repo, or the branch's
// fork point inside a linked worktree (see diffBaselineRef). Only then is the current content read (fs.Read,
// itself openat-guarded).
func (fs *fsImpl) Diff(ctx context.Context, rootID, relPath string) (Diff, error) {
	rh, clean, err := fs.resolve(rootID, relPath)
	if err != nil {
		return Diff{}, err
	}

	// Locate a usable, in-root repo BEFORE deciding the binary response: a
	// file with no verified in-root repo cannot be diffed, binary or not.
	gitDir, repoRel, ok := findGitDir(rh, clean)
	if !ok {
		return Diff{}, ErrDiffUnavailable
	}

	// Inside a linked git worktree, baseline against the branch's fork point so work the agent has already
	// committed on the worktree branch still shows. A normal repo (or any git hiccup) resolves to "HEAD".
	baseRef := diffBaselineRef(ctx, gitDir, rh.root.Path)

	cur, rerr := fs.Read(ctx, rootID, relPath)
	if errors.Is(rerr, ErrBinary) {
		tracked, gerr := gitTracked(ctx, gitDir, baseRef, repoRel)
		if gerr != nil {
			return Diff{}, ErrReadFailed
		}
		status := statusAdded
		if tracked {
			status = statusModified
		}
		return Diff{Path: clean, Baseline: baseRef, Status: status, Binary: true}, nil
	}
	if rerr != nil {
		return Diff{}, rerr
	}

	baseline, tracked, oversize, gerr := gitBaseline(ctx, gitDir, baseRef, repoRel)
	if gerr != nil {
		return Diff{}, ErrReadFailed
	}
	if oversize {
		// The baseline blob is larger than the max viewable file — an inline diff wouldn't be useful and
		// buffering it risks OOM.
		return Diff{Path: clean, Baseline: baseRef, Status: statusModified, Truncated: true}, nil
	}

	status := statusModified
	switch {
	case !tracked:
		status = statusAdded
	case baseline == cur.Content:
		return Diff{Path: clean, Baseline: baseRef, Status: statusClean}, nil
	default:
		// tracked and content differs → modified (already set).
	}

	body := udiff.Unified(baseRef+":"+repoRel, repoRel, baseline, cur.Content)
	truncated := false
	if len(body) > maxDiffBytes {
		body = truncateOnLine(body, maxDiffBytes)
		truncated = true
	}
	return Diff{Path: clean, Baseline: baseRef, Status: status, Diff: body, Truncated: truncated}, nil
}

// diffBaselineRef returns the ref to diff relPath against. For a file inside a linked git worktree it is
// the branch's fork point — merge-base(worktree HEAD, main-worktree HEAD) — so committed branch work shows.
// A normal repo, or any failure (missing merge-base / unresolvable paths / git error), returns "HEAD", which
// keeps non-worktree behavior byte-for-byte unchanged.
func diffBaselineRef(ctx context.Context, gitDir, rootAbs string) string {
	const head = "HEAD"
	common, ok, err := runGit(ctx, gitDir, "rev-parse", "--git-common-dir")
	if err != nil || !ok {
		return head
	}
	commonDir := strings.TrimSpace(string(common))
	if commonDir == "" {
		return head
	}
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(gitDir, commonDir)
	}
	// Canonicalize BOTH sides before comparing: gitDir from a normal repo (a real ".git" dir) is not
	// pre-resolved, so a repo reached through a symlinked root must not be mistaken for a linked worktree.
	canonCommon, err := filepath.EvalSymlinks(commonDir)
	if err != nil {
		return head
	}
	canonGit, err := filepath.EvalSymlinks(gitDir)
	if err != nil {
		return head
	}
	if canonCommon == canonGit {
		return head // common dir == git dir → an ordinary repo, not a linked worktree
	}
	if _, contained := verifyContained(rootAbs, canonCommon); !contained {
		return head // the common ".git" must live within the browse root
	}
	mainHead, ok, err := runGit(ctx, canonCommon, "rev-parse", "HEAD")
	if err != nil || !ok {
		return head
	}
	mainRef := strings.TrimSpace(string(mainHead))
	if mainRef == "" {
		return head
	}
	// No merge-base (unrelated histories) or a git error both degrade to HEAD — a single-file diff against
	// HEAD is still useful, so there is nothing to surface to the client here.
	if ref, ferr := forkPoint(ctx, gitDir, "HEAD", mainRef); ferr == nil && ref != "" {
		return ref
	}
	return head
}

// findGitDir walks up from clean's directory to the root, locating a `.git` entry through the secure fd at
// each level.
func findGitDir(rh *rootHandle, clean string) (gitDir, repoRel string, ok bool) {
	rootPath := filepath.Clean(rh.root.Path)
	relDir := "."
	if clean != "." {
		relDir = path.Dir(clean)
	}
	for {
		relGit := hiddenGitDir
		if relDir != "." {
			relGit = relDir + "/" + hiddenGitDir
		}
		isDir, isRegular, content, err := rh.gitEntry(relGit)
		if err == nil {
			absDir := rootPath
			if relDir != "." {
				absDir = filepath.Join(rootPath, filepath.FromSlash(relDir))
			}
			gd, contained := gitDirFromEntry(rootPath, absDir, isDir, isRegular, content)
			if !contained {
				return "", "", false // external / unusable → refuse, don't fall through
			}
			return gd, repoRelPath(relDir, clean), true
		}
		if errors.Is(err, ErrSymlink) {
			// A symlink `.git` is a deliberate escape attempt — refuse entirely
			// rather than silently diffing against an outer repo.
			return "", "", false
		}
		// Not found at this level (or an unreadable entry) → keep walking up.
		if relDir == "." {
			return "", "", false
		}
		relDir = path.Dir(relDir)
	}
}

// gitDirFromEntry resolves the real git directory for a `.git` entry that sits in absDir, and requires the
// result to reside within rootPath. Anything else is refused.
func gitDirFromEntry(rootPath, absDir string, isDir, isRegular bool, content []byte) (string, bool) {
	if isDir {
		return filepath.Join(absDir, hiddenGitDir), true
	}
	if !isRegular {
		return "", false
	}
	target, ok := parseGitfile(string(content))
	if !ok {
		return "", false
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(absDir, target)
	}
	return verifyContained(rootPath, target)
}

// parseGitfile extracts the target of a git worktree/submodule gitfile, whose sole meaningful line is
// `gitdir: <path>`. It returns ok=false when no such line is present.
func parseGitfile(s string) (string, bool) {
	for _, line := range strings.Split(s, "\n") {
		if rest, found := strings.CutPrefix(strings.TrimSpace(line), "gitdir:"); found {
			if p := strings.TrimSpace(rest); p != "" {
				return p, true
			}
		}
	}
	return "", false
}

// verifyContained resolves symlinks in both the candidate git dir and the root and returns the real git
// dir only if it is the root itself or lies strictly beneath it.
func verifyContained(rootPath, gitDir string) (string, bool) {
	resolved, err := filepath.EvalSymlinks(gitDir)
	if err != nil {
		return "", false
	}
	resolvedRoot, err := filepath.EvalSymlinks(rootPath)
	if err != nil {
		return "", false
	}
	if resolved == resolvedRoot || strings.HasPrefix(resolved, resolvedRoot+string(filepath.Separator)) {
		return resolved, true
	}
	return "", false
}

// repoRelPath returns clean re-based onto the work-tree dir relDir.
func repoRelPath(relDir, clean string) string {
	if relDir == "." {
		return clean
	}
	return strings.TrimPrefix(clean, relDir+"/")
}

// gitBaseline reads the baseRef blob for repoRel out of the verified gitDir's object store, with an explicit
// --git-dir so git never re-discovers or re-follows `.git`.
func gitBaseline(ctx context.Context, gitDir, baseRef, repoRel string) (content string, tracked, oversize bool, err error) {
	out, ok, err := runGit(ctx, gitDir, "cat-file", "-s", baseRef+":"+repoRel)
	if err != nil {
		return "", false, false, err
	}
	if !ok {
		return "", false, false, nil // not in baseRef → untracked
	}
	size, perr := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if perr != nil {
		return "", false, false, nil // unexpected output → treat as untracked, don't crash
	}
	if size > maxContentBytes {
		return "", true, true, nil
	}
	blob, ok, err := runGit(ctx, gitDir, "cat-file", "blob", baseRef+":"+repoRel)
	if err != nil {
		return "", false, false, err
	}
	if !ok {
		return "", false, false, nil
	}
	return string(blob), true, false, nil
}

// gitTracked reports whether repoRel exists in baseRef, without reading its blob. Used for binary files,
// where only tracked/untracked matters.
func gitTracked(ctx context.Context, gitDir, baseRef, repoRel string) (bool, error) {
	_, ok, err := runGit(ctx, gitDir, "cat-file", "-e", baseRef+":"+repoRel)
	if err != nil {
		return false, err
	}
	return ok, nil
}

// runGitExit runs `git --git-dir=<gitDir> <args...>` bounded by gitTimeout and returns stdout plus the
// process exit code. err is non-nil ONLY when git could not be run at all (failed to start, or the context
// deadline/cancellation fired); a non-zero exit is reported via code, not err, so a caller can distinguish
// git's individual statuses (e.g. merge-base: 0 = base printed, 1 = no common ancestor, 128 = fatal).
func runGitExit(ctx context.Context, gitDir string, args ...string) (out []byte, code int, err error) {
	cctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	full := append([]string{"--git-dir=" + gitDir}, args...)
	cmd := exec.CommandContext(cctx, "git", full...)
	cmd.Env = gitcmd.SanitizeEnv(os.Environ())
	out, err = cmd.Output()
	if err == nil {
		return out, 0, nil
	}
	if cerr := cctx.Err(); cerr != nil {
		return nil, -1, cerr // deadline / cancellation → infra failure
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return out, exitErr.ExitCode(), nil // git ran and exited non-zero → report the code
	}
	return nil, -1, err // exec could not start git → infra failure
}

// runGit is runGitExit collapsed to the common "ran cleanly?" question: ok=true on exit 0, ok=false on any
// non-zero exit (benign, e.g. a path untracked), err only on an infra failure. Callers that must tell
// non-zero exit codes apart use runGitExit directly (see forkPoint).
func runGit(ctx context.Context, gitDir string, args ...string) (out []byte, ok bool, err error) {
	out, code, err := runGitExit(ctx, gitDir, args...)
	if err != nil {
		return nil, false, err
	}
	return out, code == 0, nil
}

// truncateOnLine cuts s at the last newline at or before byte offset limit, so a truncated diff never
// splits a line in half.
func truncateOnLine(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := strings.LastIndexByte(s[:limit], '\n')
	if cut < 0 {
		return ""
	}
	return s[:cut+1]
}
