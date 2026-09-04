package workspace

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"os"
	"os/exec"
	"path"
	"slices"
	"strings"
	"unicode/utf8"
)

// nameStatusRec is one record of `git diff --name-status -z` (rename/copy
// detection is disabled, so there are never old+new path triples).
type nameStatusRec struct {
	status byte
	path   string
}

// mapStatus maps a git single-letter status to a ChangeStatus. ok=false for any
// status this feature does not expect (with --no-renames, R/C never appear).
func mapStatus(s byte) (ChangeStatus, bool) {
	switch s {
	case 'A':
		return ChangeAdded, true
	case 'D':
		return ChangeDeleted, true
	case 'M', 'T', 'U':
		return ChangeModified, true
	default:
		return "", false
	}
}

// parseNameStatusZ parses `git diff --name-status -z` output: NUL-separated
// fields alternating status,path,status,path,… (NOT a "status\tpath" line).
// A dangling final record (status without a path) is an error unless truncated
// is true (byte-cap fired mid-record), in which case the partial tail is
// dropped. An unknown/unmapped status is an error.
func parseNameStatusZ(data []byte, truncated bool) ([]nameStatusRec, error) {
	fields := splitZ(data)
	// git -z terminates every field with NUL, so complete output ends in NUL
	// (splitZ drops that trailing empty). A non-NUL final byte means the stream
	// was cut mid-field: legal only when the byte-cap fired, and the partial
	// final field must be discarded before pairing.
	if len(data) > 0 && data[len(data)-1] != 0 {
		if !truncated {
			return nil, ErrReadFailed
		}
		if len(fields) > 0 {
			fields = fields[:len(fields)-1]
		}
	}
	recs := make([]nameStatusRec, 0, len(fields)/2)
	for i := 0; i+1 < len(fields); i += 2 {
		st := fields[i]
		if len(st) != 1 {
			return nil, ErrReadFailed
		}
		if _, ok := mapStatus(st[0]); !ok {
			return nil, ErrReadFailed
		}
		recs = append(recs, nameStatusRec{status: st[0], path: string(fields[i+1])})
	}
	// An odd leftover field (a status with no following path) is malformed
	// unless the byte-cap fired.
	if len(fields)%2 != 0 && !truncated {
		return nil, ErrReadFailed
	}
	return recs, nil
}

// parseUntrackedZ parses `git ls-files -z` output: NUL-separated paths.
func parseUntrackedZ(data []byte) []string {
	fields := splitZ(data)
	// A final field not terminated by NUL is a byte-cap fragment — drop it.
	if len(data) > 0 && data[len(data)-1] != 0 && len(fields) > 0 {
		fields = fields[:len(fields)-1]
	}
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		out = append(out, string(f))
	}
	return out
}

// splitZ splits on NUL and drops the trailing empty element that a
// NUL-terminated stream always produces. Empty fields elsewhere are impossible
// (git never emits an empty path/status).
func splitZ(data []byte) [][]byte {
	parts := bytes.Split(data, []byte{0})
	if n := len(parts); n > 0 && len(parts[n-1]) == 0 {
		parts = parts[:n-1]
	}
	return parts
}

// maxChangesOutputBytes caps stdout captured from one git process; the rest is
// drained and discarded and the result is flagged truncated. A var so tests can
// shrink it. Distinct from an entry cap: exec buffering could otherwise grow
// without bound before any entry cap applies.
var maxChangesOutputBytes = 4 << 20

// maxChangesEntries caps how many Change entries Changes returns; exceeding it
// sets ChangeList.Truncated. A var so tests can shrink it.
var maxChangesEntries = 5000

// cappedBuffer captures up to cap bytes and records whether more arrived. It
// keeps draining (returns len(p), nil) past the cap so the child process is
// never blocked on a full pipe.
type cappedBuffer struct {
	buf      bytes.Buffer
	cap      int
	overflow bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if room := c.cap - c.buf.Len(); room > 0 {
		if len(p) <= room {
			c.buf.Write(p)
		} else {
			c.buf.Write(p[:room])
			c.overflow = true
		}
	} else if len(p) > 0 {
		c.overflow = true
	}
	return len(p), nil
}

// gitCmd builds a hardened git *exec.Cmd: no shell, verified --git-dir,
// allowlisted --work-tree, no pager, no repo-configured fsmonitor, optional
// index locks disabled (read-only endpoint must never mutate the index).
func gitCmd(ctx context.Context, gitDir, workTree string, args ...string) *exec.Cmd {
	full := append([]string{
		"--no-pager",
		"-c", "core.fsmonitor=false",
		"--git-dir=" + gitDir,
		"--work-tree=" + workTree,
	}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	// Drop every ambient GIT_* variable before adding our own: the explicit
	// --git-dir/--work-tree flags do NOT override env selectors like
	// GIT_INDEX_FILE, GIT_OBJECT_DIRECTORY, GIT_ALTERNATE_OBJECT_DIRECTORIES,
	// GIT_COMMON_DIR, or GIT_CONFIG*, so a stray one in the process environment
	// would let git read an index/object store/config outside the verified root
	// (e.g. GIT_INDEX_FILE pointing at another repo surfaces phantom paths).
	cmd.Env = append(sanitizeGitEnv(cmd.Environ()), "GIT_OPTIONAL_LOCKS=0")
	return cmd
}

// sanitizeGitEnv returns env with every GIT_* variable removed, so the only
// git configuration the child process sees comes from the explicit --git-dir/
// --work-tree flags and the -c overrides passed on the command line.
func sanitizeGitEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, "GIT_") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// runGitChanges runs a git command that MUST succeed (exit 0). Any non-zero
// exit, timeout, or exec failure is ErrReadFailed — unlike diff.go's runGit, it
// never collapses a failure into an empty success. stdout is byte-capped;
// stderr is discarded (never returned to the client).
func runGitChanges(ctx context.Context, gitDir, workTree string, args ...string) ([]byte, bool, error) {
	cctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := gitCmd(cctx, gitDir, workTree, args...)
	out := &cappedBuffer{cap: maxChangesOutputBytes}
	cmd.Stdout = out
	cmd.Stderr = nil // discard git diagnostics
	if err := cmd.Run(); err != nil {
		return nil, false, ErrReadFailed
	}
	return out.buf.Bytes(), out.overflow, nil
}

// gitProbe runs a git command whose non-zero exit is a meaningful, benign
// signal rather than a failure. It returns exitZero=true (and stdout) when git
// exits 0; exitZero=false,err=nil for a clean non-zero exit; and err!=nil only
// for a genuine infra failure (timeout, exec could not start git).
func gitProbe(ctx context.Context, gitDir, workTree string, args ...string) (exitZero bool, stdout []byte, err error) {
	cctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	out, rerr := gitCmd(cctx, gitDir, workTree, args...).Output()
	if rerr == nil {
		return true, out, nil
	}
	if cctx.Err() != nil {
		return false, nil, ErrReadFailed
	}
	var exitErr *exec.ExitError
	if errors.As(rerr, &exitErr) {
		return false, nil, nil // clean non-zero exit → benign signal
	}
	return false, nil, ErrReadFailed // could not start git
}

// headState reports whether the repo has a resolvable HEAD commit.
//   - (true, nil)   HEAD peels to a commit.
//   - (false, nil)  genuinely unborn: HEAD is a symbolic ref to a branch that
//     does not exist yet (git init with no commits).
//   - (false, err)  HEAD exists but cannot be resolved to a commit (corrupt or
//     missing object, detached HEAD that won't peel) or any infra failure —
//     the caller MUST treat this as a read error, never as unborn, so a damaged
//     repo isn't silently reported as "everything added vs an empty baseline".
func headState(ctx context.Context, gitDir, workTree string) (bool, error) {
	ok, _, err := gitProbe(ctx, gitDir, workTree, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	if err != nil {
		return false, err
	}
	if ok {
		return true, nil
	}
	// The peel failed cleanly: unborn or corrupt. A genuinely unborn repo has
	// HEAD as a symbolic ref pointing at a branch that does not exist yet.
	symOK, refBytes, err := gitProbe(ctx, gitDir, workTree, "symbolic-ref", "--quiet", "HEAD")
	if err != nil {
		return false, err
	}
	ref := strings.TrimSpace(string(refBytes))
	if !symOK || ref == "" {
		return false, ErrReadFailed // detached / unreadable HEAD that won't peel → corrupt
	}
	// Does the target branch hold a stored object id? for-each-ref prints it
	// WITHOUT loading the object (unlike rev-parse HEAD^{commit} or show-ref
	// --verify, both of which fail on a missing object): a non-empty oid means
	// the ref exists but HEAD couldn't peel → corrupt object; an empty result
	// means the branch has no commit yet → genuinely unborn.
	_, oidBytes, err := gitProbe(ctx, gitDir, workTree, "for-each-ref", "--format=%(objectname)", ref)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(string(oidBytes)) != "" {
		return false, ErrReadFailed // branch ref has an oid but HEAD won't peel → corrupt object
	}
	return false, nil // symbolic HEAD → branch with no commit → genuinely unborn
}

// aggregateChanges merges tracked and untracked entries into the final listing.
// Tracked entries seed the map. An untracked path that collides with a tracked
// "deleted" collapses to a single "modified" (the file was re-created after a
// staged deletion); any other collision keeps the tracked entry (tracked wins).
// The result is sorted by full Path (case-insensitive, original Path as
// tie-break) and capped to maxChangesEntries — hitting the cap sets truncated.
// Selectable is not set here; the caller resolves it on the capped survivors.
func aggregateChanges(tracked, untracked []Change) (entries []Change, truncated bool) {
	byPath := make(map[string]Change, len(tracked)+len(untracked))
	for _, c := range tracked {
		byPath[c.Path] = c
	}
	for _, u := range untracked {
		existing, ok := byPath[u.Path]
		switch {
		case !ok:
			byPath[u.Path] = u
		case existing.Status == ChangeDeleted:
			// A path deleted from the index but re-created untracked is a
			// modification, not a deletion. Selectable is resolved later.
			byPath[u.Path] = Change{Name: u.Name, Path: u.Path, Status: ChangeModified}
		default:
			// tracked wins — keep existing
		}
	}

	entries = make([]Change, 0, len(byPath))
	for _, c := range byPath {
		entries = append(entries, c)
	}
	slices.SortFunc(entries, func(a, b Change) int {
		if c := cmp.Compare(strings.ToLower(a.Path), strings.ToLower(b.Path)); c != 0 {
			return c
		}
		return cmp.Compare(a.Path, b.Path)
	})
	if len(entries) > maxChangesEntries {
		entries = entries[:maxChangesEntries]
		truncated = true
	}
	return entries, truncated
}

// Changes lists files that differ from the chosen baseline in rootID, plus
// untracked files (as "added"). It works only when rootID is itself a git repo
// root (findGitDir does not walk above the root). git output is untrusted:
// every path is UTF-8- and validateRelPath-checked, and Selectable is decided
// by a real openat+fstat. See the spec for the security rationale.
func (fs *fsImpl) Changes(ctx context.Context, rootID string, mode ChangeMode) (ChangeList, error) {
	if err := ctx.Err(); err != nil {
		return ChangeList{}, err
	}
	if mode != ChangeModeIndex && mode != ChangeModeHead {
		return ChangeList{}, ErrInvalidRootOrPath
	}
	rh, _, err := fs.resolve(rootID, ".")
	if err != nil {
		return ChangeList{}, err
	}
	gitDir, _, ok := findGitDir(rh, ".")
	if !ok {
		return ChangeList{}, ErrDiffUnavailable
	}
	workTree := rh.root.Path

	var (
		tracked  []Change
		byteTrun bool
	)
	untrackedArgs := []string{"ls-files", "--others", "--exclude-standard", "-z", "--"}

	unbornHead := false
	useDiff := mode == ChangeModeIndex
	if mode == ChangeModeHead {
		has, herr := headState(ctx, gitDir, workTree)
		if herr != nil {
			return ChangeList{}, herr
		}
		useDiff = has
		if !has {
			// Unborn repo: no HEAD to diff against. Everything currently on
			// disk (cached + untracked) is "added" vs the empty baseline.
			// `ls-files --cached` lists staged paths REGARDLESS of whether
			// they still exist on disk — a path that was `git add`-ed and
			// then deleted before any commit is not a difference from the
			// empty baseline, so pathsToChanges below drops it via its
			// dropMissing existence check (unbornHead=true).
			unbornHead = true
			untrackedArgs = []string{"ls-files", "--cached", "--others", "--exclude-standard", "-z", "--"}
		}
	}

	if useDiff {
		diffArgs := []string{"diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--name-status", "-z"}
		if mode == ChangeModeHead {
			diffArgs = append(diffArgs, "HEAD")
		}
		diffArgs = append(diffArgs, "--")
		out, trun, derr := runGitChanges(ctx, gitDir, workTree, diffArgs...)
		if derr != nil {
			return ChangeList{}, derr
		}
		byteTrun = byteTrun || trun
		recs, perr := parseNameStatusZ(out, trun)
		if perr != nil {
			return ChangeList{}, perr
		}
		tracked, err = fs.recsToChanges(ctx, recs)
		if err != nil {
			return ChangeList{}, err
		}
	}

	uout, utrun, uerr := runGitChanges(ctx, gitDir, workTree, untrackedArgs...)
	if uerr != nil {
		return ChangeList{}, uerr
	}
	byteTrun = byteTrun || utrun
	untracked, err := fs.pathsToChanges(ctx, rh, parseUntrackedZ(uout), unbornHead)
	if err != nil {
		return ChangeList{}, err
	}

	// Aggregation/sort/cap run on plain values (no syscalls); only then is the
	// per-file Selectable resolved, and only for the survivors — so the number
	// of secure openat+fstat calls is bounded by maxChangesEntries, not by the
	// (possibly far larger) number of raw changed paths git reported.
	entries, capTrun := aggregateChanges(tracked, untracked)
	if err := fs.resolveSelectable(ctx, rh, entries); err != nil {
		return ChangeList{}, err
	}
	return ChangeList{Entries: entries, Truncated: byteTrun || capTrun}, nil
}

// recsToChanges validates each diff record's path (git output is untrusted) and
// builds a Change WITHOUT resolving Selectable — that is deferred to
// resolveSelectable, which runs after the cap so the syscall count stays
// bounded. A record with an unmapped status or a path that fails validation is
// dropped. ctx is checked each iteration so a cancelled request stops promptly.
func (fs *fsImpl) recsToChanges(ctx context.Context, recs []nameStatusRec) ([]Change, error) {
	out := make([]Change, 0, len(recs))
	for _, r := range recs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		status, ok := mapStatus(r.status)
		if !ok {
			continue
		}
		clean, ok := fs.validGitPath(r.path)
		if !ok {
			continue
		}
		out = append(out, Change{Name: path.Base(clean), Path: clean, Status: status})
	}
	return out, nil
}

// pathsToChanges builds "added" Changes for untracked paths, dropping any path
// that fails validation. Selectable is resolved later (resolveSelectable), not
// here. dropMissing additionally drops a path that no longer exists on disk at
// all — needed only for the unborn-repo head listing, whose source
// (`ls-files --cached --others`) includes staged-but-since-deleted paths that
// `ls-files --others` alone (the normal untracked source) never would; passing
// false preserves the previous behavior for that normal case. The existence
// probe must run here (pre-cap) so a missing path never occupies a cap slot.
// ctx is checked each iteration.
func (fs *fsImpl) pathsToChanges(ctx context.Context, rh *rootHandle, paths []string, dropMissing bool) ([]Change, error) {
	out := make([]Change, 0, len(paths))
	for _, p := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		clean, ok := fs.validGitPath(p)
		if !ok {
			continue
		}
		if dropMissing && !fs.existsInWorktree(rh, clean) {
			continue
		}
		out = append(out, Change{Name: path.Base(clean), Path: clean, Status: ChangeAdded})
	}
	return out, nil
}

// resolveSelectable fills Selectable for the final, already-capped entries with
// one secure openat+fstat per non-deleted path. A deleted path is never on disk,
// so it stays non-selectable without a syscall. Running after aggregateChanges'
// cap bounds the syscall count to maxChangesEntries regardless of how many raw
// paths git reported. ctx is checked each iteration.
func (fs *fsImpl) resolveSelectable(ctx context.Context, rh *rootHandle, entries []Change) error {
	for i := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entries[i].Status == ChangeDeleted {
			continue
		}
		entries[i].Selectable = fs.isSelectable(rh, entries[i].Path)
	}
	return nil
}

// validGitPath requires valid UTF-8 and passes the path through validateRelPath
// (rejecting absolute/.. /NUL and hidden .git/.afm). It returns the cleaned
// path, or ok=false to drop it.
func (fs *fsImpl) validGitPath(p string) (string, bool) {
	if !utf8.ValidString(p) {
		return "", false
	}
	clean, err := validateRelPath(p)
	if err != nil || clean == "." {
		return "", false
	}
	return clean, true
}

// isSelectable reports whether clean is currently a regular file, decided by a
// real secure open (openFileReadNonblock never blocks on a FIFO) + fstat — not
// by trusting git's name. A symlink/dir/special/vanished path is not selectable.
func (fs *fsImpl) isSelectable(rh *rootHandle, clean string) bool {
	fd, err := rh.openat(clean, openFileReadNonblock)
	if err != nil {
		return false
	}
	f := os.NewFile(uintptr(fd), clean)
	st, serr := f.Stat()
	_ = f.Close()
	return serr == nil && st.Mode().IsRegular()
}

// existsInWorktree reports whether clean currently exists on disk at all —
// unlike isSelectable, it does not care about the file's type. It is used
// only to drop a `git ls-files --cached` path that was staged and then
// deleted before any commit (see pathsToChanges' dropMissing). A failure
// other than ErrNotFound (e.g. ErrSymlink, refused by RESOLVE_NO_SYMLINKS)
// still means the path exists — isSelectable, computed separately, is what
// decides whether it's viewable, not this check.
func (fs *fsImpl) existsInWorktree(rh *rootHandle, clean string) bool {
	fd, err := rh.openat(clean, openFileReadNonblock)
	if err == nil {
		_ = os.NewFile(uintptr(fd), clean).Close()
		return true
	}
	return !errors.Is(err, ErrNotFound)
}
