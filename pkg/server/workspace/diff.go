package workspace

import (
	"context"
	"errors"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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

// Diff computes the unified diff of relPath against its HEAD baseline. Only then is the current content
// read (fs.Read, itself openat-guarded).
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

	cur, rerr := fs.Read(ctx, rootID, relPath)
	if errors.Is(rerr, ErrBinary) {
		tracked, gerr := gitTracked(ctx, gitDir, repoRel)
		if gerr != nil {
			return Diff{}, ErrReadFailed
		}
		status := statusAdded
		if tracked {
			status = statusModified
		}
		return Diff{Path: clean, Baseline: "HEAD", Status: status, Binary: true}, nil
	}
	if rerr != nil {
		return Diff{}, rerr
	}

	baseline, tracked, oversize, gerr := gitBaseline(ctx, gitDir, repoRel)
	if gerr != nil {
		return Diff{}, ErrReadFailed
	}
	if oversize {
		// The HEAD blob is larger than the max viewable file — an inline diff wouldn't be useful and buffering
		// it risks OOM.
		return Diff{Path: clean, Baseline: "HEAD", Status: statusModified, Truncated: true}, nil
	}

	status := statusModified
	switch {
	case !tracked:
		status = statusAdded
	case baseline == cur.Content:
		return Diff{Path: clean, Baseline: "HEAD", Status: statusClean}, nil
	default:
		// tracked and content differs → modified (already set).
	}

	body := udiff.Unified("HEAD:"+repoRel, repoRel, baseline, cur.Content)
	truncated := false
	if len(body) > maxDiffBytes {
		body = truncateOnLine(body, maxDiffBytes)
		truncated = true
	}
	return Diff{Path: clean, Baseline: "HEAD", Status: status, Diff: body, Truncated: truncated}, nil
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
		relGit := ".git"
		if relDir != "." {
			relGit = relDir + "/.git"
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
		return filepath.Join(absDir, ".git"), true
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

// gitBaseline reads the HEAD blob for repoRel out of the verified gitDir's object store, with an explicit
// --git-dir so git never re-discovers or re-follows `.git`.
func gitBaseline(ctx context.Context, gitDir, repoRel string) (content string, tracked, oversize bool, err error) {
	out, ok, err := runGit(ctx, gitDir, "cat-file", "-s", "HEAD:"+repoRel)
	if err != nil {
		return "", false, false, err
	}
	if !ok {
		return "", false, false, nil // not in HEAD → untracked
	}
	size, perr := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if perr != nil {
		return "", false, false, nil // unexpected output → treat as untracked, don't crash
	}
	if size > maxContentBytes {
		return "", true, true, nil
	}
	blob, ok, err := runGit(ctx, gitDir, "cat-file", "blob", "HEAD:"+repoRel)
	if err != nil {
		return "", false, false, err
	}
	if !ok {
		return "", false, false, nil
	}
	return string(blob), true, false, nil
}

// gitTracked reports whether repoRel exists in HEAD, without reading its blob. Used for binary files,
// where only tracked/untracked matters.
func gitTracked(ctx context.Context, gitDir, repoRel string) (bool, error) {
	_, ok, err := runGit(ctx, gitDir, "cat-file", "-e", "HEAD:"+repoRel)
	if err != nil {
		return false, err
	}
	return ok, nil
}

// runGit runs `git --git-dir=<gitDir> <args...>` bounded by gitTimeout and classifies the outcome into
// three cases: ok=true; ok=false,err=nil; err!=nil.
func runGit(ctx context.Context, gitDir string, args ...string) (out []byte, ok bool, err error) {
	cctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	full := append([]string{"--git-dir=" + gitDir}, args...)
	out, err = exec.CommandContext(cctx, "git", full...).Output()
	if err == nil {
		return out, true, nil
	}
	if cerr := cctx.Err(); cerr != nil {
		return nil, false, cerr // deadline / cancellation → infra failure
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return nil, false, nil // git exited non-zero → benign (untracked)
	}
	return nil, false, err // exec could not start git → infra failure
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
