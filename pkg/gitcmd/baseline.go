package gitcmd

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// GitDirRunner runs `git --git-dir=<gitDir> <args...>`: ok reports a zero exit, err an infrastructure failure.
type GitDirRunner func(ctx context.Context, gitDir string, args ...string) (stdout []byte, ok bool, err error)

// RunGitDir is the default GitDirRunner: a sanitized environment, no pager, bounded by DefaultTimeout.
func RunGitDir(ctx context.Context, gitDir string, args ...string) ([]byte, bool, error) {
	cctx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "git", append([]string{"--no-pager", "--git-dir=" + gitDir}, args...)...)
	cmd.Env = SanitizeEnv(os.Environ())
	out := &cappedWriter{cap: maxOutputBytes}
	cmd.Stdout = out
	runErr := cmd.Run()
	if runErr == nil {
		return out.buf.Bytes(), true, nil
	}
	if cerr := cctx.Err(); cerr != nil {
		return nil, false, cerr
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		return nil, false, nil
	}
	return nil, false, runErr
}

// DiffBaseRef is THE baseline policy for "what changed in this checkout" — shared by the dashboard's diff view
// and a helper's task context, so both diff against the same revision. For a linked git worktree it is the
// branch's fork point (merge-base of the worktree's HEAD and the main worktree's HEAD), so committed branch
// work shows; for an ordinary repository, or on any failure, it is "HEAD".
//
// gitDir is the checkout's git directory. Both it and the common git directory are canonicalized (symlinks
// resolved) before they are compared, so a repository reached through a symlinked path is not mistaken for a
// linked worktree. When root is not empty, the common git directory must also lie within root (the dashboard
// never reads a repository outside the tree it browses); otherwise the answer is "HEAD".
func DiffBaseRef(ctx context.Context, run GitDirRunner, gitDir, root string) string {
	const head = "HEAD"
	common, ok, err := run(ctx, gitDir, "rev-parse", "--git-common-dir")
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
	canonCommon, err := filepath.EvalSymlinks(commonDir)
	if err != nil {
		return head
	}
	canonGit, err := filepath.EvalSymlinks(gitDir)
	if err != nil {
		return head
	}
	if canonCommon == canonGit {
		return head // common dir == git dir: an ordinary repository, not a linked worktree
	}
	if root != "" && !within(root, canonCommon) {
		return head
	}
	mainHead, ok, err := run(ctx, canonCommon, "rev-parse", "HEAD")
	if err != nil || !ok {
		return head
	}
	mainRef := strings.TrimSpace(string(mainHead))
	if mainRef == "" {
		return head
	}
	// No merge-base (unrelated histories) or a git error both fall back to HEAD.
	base, ok, err := run(ctx, gitDir, "merge-base", head, mainRef)
	if err != nil || !ok {
		return head
	}
	if ref, _, _ := strings.Cut(strings.TrimSpace(string(base)), "\n"); ref != "" {
		return ref // several bases are possible in criss-cross history — the first is enough
	}
	return head
}

// within reports whether the canonical path p lies within root (after resolving root's symlinks).
func within(root, p string) bool {
	canonRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	return p == canonRoot || strings.HasPrefix(p, canonRoot+string(filepath.Separator))
}
