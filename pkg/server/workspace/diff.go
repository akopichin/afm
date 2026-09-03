package workspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/aymanbagabas/go-udiff"
)

// maxDiffBytes caps the size of a unified-diff body returned inline; a larger
// diff is truncated on a line boundary (Truncated:true) rather than rejected
// outright — a diff, unlike raw content, is still useful in prefix form.
const maxDiffBytes = 4 << 20

// gitTimeout bounds every git subprocess this package spawns. git reads a
// single blob from the local object store — anything slower than this
// signals a wedged repo, not real work.
const gitTimeout = 3 * time.Second

// Diff computes the unified diff of relPath against its HEAD baseline.
//
// Ordering matters for safety: fs.Read runs FIRST. It goes through
// resolve+openat, so it validates and reads relPath (rejecting `..`,
// symlinks, and the hidden `.git`/`.afm` subtrees) entirely through the
// secure fd path before any git subprocess is ever spawned. Git itself only
// ever touches the HEAD blob (`git cat-file blob HEAD:<repoRel>`) — never the
// working-tree path — so the untrusted filesystem is never handed to git.
func (fs *fsImpl) Diff(ctx context.Context, rootID, relPath string) (Diff, error) {
	cur, rerr := fs.Read(ctx, rootID, relPath)
	if errors.Is(rerr, ErrBinary) {
		// Read already validated the path via resolve+openat; resolve here
		// is cheap (pure string validation, no fd) and just recovers the
		// cleaned path for the response.
		_, clean, err := fs.resolve(rootID, relPath)
		if err != nil {
			return Diff{}, err
		}
		return Diff{Path: clean, Baseline: "HEAD", Status: "modified", Binary: true}, nil
	}
	if rerr != nil {
		return Diff{}, rerr
	}

	rh, clean, err := fs.resolve(rootID, relPath)
	if err != nil {
		return Diff{}, err
	}

	repoDir, repoRel, ok := findRepo(rh.root.Path, clean)
	if !ok {
		return Diff{}, ErrDiffUnavailable
	}

	baseline, tracked := gitBaseline(ctx, repoDir, repoRel)
	status := "modified"
	switch {
	case !tracked:
		status = "added"
	case baseline == cur.Content:
		return Diff{Path: clean, Baseline: "HEAD", Status: "clean"}, nil
	default:
		// status already "modified"
	}

	body := udiff.Unified("HEAD:"+repoRel, repoRel, baseline, cur.Content)
	truncated := false
	if len(body) > maxDiffBytes {
		body = truncateOnLine(body, maxDiffBytes)
		truncated = true
	}
	return Diff{Path: clean, Baseline: "HEAD", Status: status, Diff: body, Truncated: truncated}, nil
}

// gitBaseline reads the HEAD blob for repoRel out of repoDir's object store.
// It never invokes a shell (exec.CommandContext with a fixed argv) and is
// bounded by gitTimeout. A non-zero exit — untracked file, or the repo has no
// HEAD yet (no commits) — is reported as (\"\", false); the caller treats
// that as "added", not as an error.
func gitBaseline(ctx context.Context, repoDir, repoRel string) (string, bool) {
	cctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "git", "-C", repoDir, "cat-file", "blob", "HEAD:"+repoRel)
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return string(out), true
}

// findRepo walks up from clean's directory, looking for a `.git` entry, and
// never walks above rootPath. It returns the repo directory and clean's path
// relative to it (slash-separated), or ok=false if no repo was found at or
// below rootPath.
func findRepo(rootPath, clean string) (repoDir, repoRel string, ok bool) {
	rootPath = filepath.Clean(rootPath)
	dir := filepath.Dir(filepath.Join(rootPath, clean))
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			rel, err := filepath.Rel(dir, filepath.Join(rootPath, clean))
			if err != nil {
				return "", "", false
			}
			return dir, filepath.ToSlash(rel), true
		}
		if dir == rootPath {
			return "", "", false
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached the filesystem root without ever reaching rootPath —
			// defensive, unreachable in practice since dir starts inside
			// rootPath and rootPath is absolute.
			return "", "", false
		}
		dir = parent
	}
}

// truncateOnLine cuts s at the last newline at or before byte offset limit,
// so a truncated diff never splits a line in half. If no newline exists
// before limit, it returns "" — a diff without a single complete line is not
// useful.
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
