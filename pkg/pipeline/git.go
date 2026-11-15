package pipeline

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/akopichin/afm/contracts/browser"
	"github.com/akopichin/afm/contracts/config"
	"github.com/akopichin/afm/contracts/gitcmd"
	"github.com/akopichin/afm/contracts/lifecyclehooks"
	"github.com/akopichin/afm/contracts/progress"
)

const (
	gitCacheDir  = "git"
	gitLockPoll  = 50 * time.Millisecond
	gitFetchWait = 5 * time.Minute
	// Only these transports may run: git's ext:: and other helpers execute commands.
	gitAllowedProtocols = "file:https:http:ssh"
	maxGitStderr        = 4 << 10
)

var fullCommitPattern = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

// Resolved is a Git package pinned to one commit and published as a snapshot.
type Resolved struct {
	Commit   string   `json:"commit"`
	Snapshot Snapshot `json:"snapshot"`
}

// ResolveGit pins src to a full commit in a bare cache outside the project and publishes it as a snapshot.
// A non-empty offlineCommit (a recorded commit) and a full-SHA ref present in the cache never touch the network;
// a branch or tag always fetches so a stale resolution is never reused.
func ResolveGit(ctx context.Context, src Source, cacheRoot, offlineCommit string) (Resolved, error) {
	if err := checkGitSource(src, offlineCommit); err != nil {
		return Resolved{}, err
	}
	gitRoot := filepath.Join(cacheRoot, gitCacheDir)
	repoRoot := filepath.Join(gitRoot, sha256Hex(src.Location))
	if err := os.MkdirAll(repoRoot, 0o755); err != nil {
		return Resolved{}, fmt.Errorf("create git cache: %w", err)
	}
	staging, err := os.MkdirTemp(gitRoot, ".stage-")
	if err != nil {
		return Resolved{}, fmt.Errorf("create git staging directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(staging) }()

	commit, skipped, err := stageCommit(ctx, src, repoRoot, offlineCommit, staging)
	if err != nil {
		return Resolved{}, err
	}
	m, err := LoadManifest(staging)
	if err != nil {
		return Resolved{}, noteSkipped(fmt.Errorf("%w (commit %s)", err, commit), skipped)
	}
	if err := rejectSkippedResources(m, skipped); err != nil {
		return Resolved{}, err
	}
	snap, err := BuildSnapshot(staging, m, cacheRoot)
	if err != nil {
		return Resolved{}, noteSkipped(err, skipped)
	}
	return Resolved{Commit: commit, Snapshot: snap}, nil
}

func checkGitSource(src Source, offlineCommit string) error {
	if src.Kind != SourceGit {
		return fmt.Errorf("pipeline source %q is not a Git source", src.Location)
	}
	// Re-validating guards against a hand-built Source carrying credentials or an odd scheme.
	if _, err := normalizeRepo(src.Location); err != nil {
		return err
	}
	if err := rejectLocationUserinfo(src.Location); err != nil {
		return err
	}
	if strings.HasPrefix(src.Ref, "-") || strings.HasPrefix(src.Subdir, "-") {
		return errors.New("pipeline source: a ref or subdirectory must not start with '-'")
	}
	if err := checkRefSyntax(src.Ref); err != nil {
		return err
	}
	if offlineCommit != "" && !fullCommitPattern.MatchString(offlineCommit) {
		return fmt.Errorf("recorded commit %q is not a full commit hash", offlineCommit)
	}
	return nil
}

// rejectLocationUserinfo keeps credentials out of git's argv and error text whatever the caller validated: a
// password, or any userinfo on a file URL, is refused. A bare ssh username such as git@ stays allowed.
func rejectLocationUserinfo(location string) error {
	u, err := url.Parse(location)
	if err != nil || u.User == nil {
		return nil
	}
	if _, hasPassword := u.User.Password(); hasPassword || u.Scheme == schemeFile {
		return errURLCredentials
	}
	return nil
}

// checkRefSyntax refuses anything git itself would not accept as a ref name. The ref is passed to fetch as a
// refspec, so ':' (destination), '+' (force), globs and negation could write refs such as refs/replace/* into the
// shared cache.
func checkRefSyntax(ref string) error {
	bad := strings.HasPrefix(ref, "+") || strings.Contains(ref, "..") || strings.Contains(ref, "@{") ||
		strings.ContainsFunc(ref, func(r rune) bool { return r < ' ' || r == 0x7f || strings.ContainsRune(`:^~?*[\ `, r) })
	if bad {
		return fmt.Errorf("pipeline source: ref %q is not a valid branch, tag or commit name", ref)
	}
	return nil
}

// rejectSkippedResources fails when an archive entry that was not extracted (a symlink or special file) lies inside,
// or on the path to, a declared package file; the local loader rejects the same package.
func rejectSkippedResources(m Manifest, skipped []string) error {
	declared := append([]string{ManifestFile, m.Entrypoint, m.Agents}, m.Resources...)
	for _, name := range skipped {
		for _, d := range declared {
			if d == "" {
				continue // an optional manifest field that is not set
			}
			d = path.Clean(d)
			if d == name || strings.HasPrefix(name, d+"/") || strings.HasPrefix(d, name+"/") {
				return fmt.Errorf("package resource %q is a symlink or special file; symlinks are not supported in a package", name)
			}
		}
	}
	return nil
}

// stageCommit resolves the commit and extracts the package tree into staging while holding the repo lock, so
// concurrent fetches into one cache neither race nor read a half-written repository. skipped lists archive
// entries that were not extracted (symlinks, special files).
func stageCommit(ctx context.Context, src Source, repoRoot, offlineCommit, staging string) (commit string, skipped []string, err error) {
	unlock, err := lockGitCache(ctx, filepath.Join(repoRoot, "lock"))
	if err != nil {
		return "", nil, err
	}
	defer unlock()

	gitDir := filepath.Join(repoRoot, "repo.git")
	if _, err := os.Stat(filepath.Join(gitDir, "HEAD")); err != nil {
		if _, err := runGit(ctx, "", "init", "--bare", "--quiet", gitDir); err != nil {
			return "", nil, err
		}
	}
	commit, err = pinCommit(ctx, src, gitDir, offlineCommit)
	if err != nil {
		return "", nil, err
	}
	skipped, err = extractArchive(ctx, gitDir, commit, src.Subdir, staging)
	if err != nil {
		return "", nil, err
	}
	return commit, skipped, nil
}

func lockGitCache(ctx context.Context, lockPath string) (func(), error) {
	lock, err := progress.NewLock(lockPath)
	if err != nil {
		return nil, err
	}
	for {
		err := lock.TryLock()
		if err == nil {
			return lock.Unlock, nil
		}
		if !errors.Is(err, progress.ErrLockBusy) {
			return nil, fmt.Errorf("lock git cache: %w", err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(gitLockPoll):
		}
	}
}

// pinCommit returns the full commit to use and keeps it reachable from a ref so git gc cannot drop it before an
// offline resume.
func pinCommit(ctx context.Context, src Source, gitDir, offlineCommit string) (string, error) {
	if offlineCommit != "" {
		if !hasCommit(ctx, gitDir, offlineCommit) {
			return "", fmt.Errorf("recorded commit %s is not in the local cache; run it online once to restore it", offlineCommit)
		}
		return offlineCommit, nil
	}
	if fullCommitPattern.MatchString(src.Ref) && hasCommit(ctx, gitDir, src.Ref) {
		return src.Ref, nil
	}
	wanted := src.Ref
	if wanted == "" {
		wanted = "HEAD"
	}
	fetchCtx, cancel := context.WithTimeout(ctx, gitFetchWait)
	defer cancel()
	if _, err := runGit(fetchCtx, gitDir, "fetch", "--quiet", "--no-tags", "--", src.Location, wanted); err != nil {
		return "", fmt.Errorf("fetch %q from %s: %w", wanted, redactLocation(src.Location), err)
	}
	out, err := runGit(ctx, gitDir, "rev-parse", "--verify", "--quiet", "FETCH_HEAD^{commit}")
	if err != nil {
		return "", fmt.Errorf("ref %q in %s does not point to a commit: %w", wanted, redactLocation(src.Location), err)
	}
	commit := strings.TrimSpace(out)
	if !fullCommitPattern.MatchString(commit) {
		return "", fmt.Errorf("git returned an unexpected commit %q for ref %q", commit, wanted)
	}
	if _, err := runGit(ctx, gitDir, "update-ref", "refs/afm/"+commit, commit); err != nil {
		return "", err
	}
	return commit, nil
}

// redactLocation hides a password in a repository URL for diagnostics.
func redactLocation(location string) string {
	if u, err := url.Parse(location); err == nil {
		return u.Redacted()
	}
	return "<unparsable repository URL>"
}

func hasCommit(ctx context.Context, gitDir, commit string) bool {
	_, err := runGit(ctx, gitDir, "cat-file", "-e", commit+"^{commit}")
	return err == nil
}

func extractArchive(ctx context.Context, gitDir, commit, subdir, dst string) ([]string, error) {
	cmd := newGitCmd(ctx, gitDir, "archive", "--format=tar", commit+":"+subdir)
	var stderr limitedBuffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("git archive: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("git archive: %w", err)
	}
	skipped, extractErr := extractTar(stdout, dst)
	_, _ = io.Copy(io.Discard, stdout) // let git finish writing before Wait
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if subdir != "" {
			return nil, fmt.Errorf("subdirectory %q not found in commit %s: %s", subdir, commit, stderr.String())
		}
		return nil, fmt.Errorf("git archive %s: %w: %s", commit, err, stderr.String())
	}
	return skipped, extractErr
}

// noteSkipped explains a later "file not found" when the missing file was a symlink the archive reader dropped.
func noteSkipped(err error, skipped []string) error {
	if len(skipped) == 0 {
		return err
	}
	return fmt.Errorf("%w (%d symlink or special entries were not extracted, first: %s)", err, len(skipped), skipped[0])
}

// extractTar writes regular files and directories into dst. Symlinks, hard links and devices are never created, so
// no later entry can be written through them; their names are returned for diagnostics.
func extractTar(r io.Reader, dst string) (skipped []string, err error) {
	tr := tar.NewReader(r)
	entries, budget := 0, int64(maxSnapshotBytes)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return skipped, nil
		}
		if err != nil {
			return skipped, fmt.Errorf("read git archive: %w", err)
		}
		if hdr.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		name, err := archiveEntryPath(hdr.Name)
		if err != nil {
			return skipped, err
		}
		if name == "" {
			continue
		}
		entries++
		if entries > maxSnapshotFiles {
			return skipped, fmt.Errorf("git archive has more than %d entries", maxSnapshotFiles)
		}
		target := filepath.Join(dst, filepath.FromSlash(name))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return skipped, fmt.Errorf("extract %s: %w", name, err)
			}
		case tar.TypeReg:
			if hdr.Size > budget {
				return skipped, fmt.Errorf("git archive is larger than %d bytes", maxSnapshotBytes)
			}
			if err := writeArchiveFile(tr, target, hdr.Mode&0o111 != 0); err != nil {
				return skipped, fmt.Errorf("extract %s: %w", name, err)
			}
			budget -= hdr.Size
		default:
			skipped = append(skipped, name)
		}
	}
}

// archiveEntryPath returns the cleaned relative path of a tar entry, or "" for the archive root.
func archiveEntryPath(name string) (string, error) {
	clean := path.Clean(strings.ReplaceAll(name, `\`, "/"))
	if clean == "." {
		return "", nil
	}
	if path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("git archive entry %q leaves the package directory", name)
	}
	return clean, nil
}

func writeArchiveFile(r io.Reader, target string, executable bool) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if executable {
		mode = 0o755
	}
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// newGitCmd builds a git command with an environment that cannot be redirected or made interactive.
// gitDir "" runs outside any repository.
func newGitCmd(ctx context.Context, gitDir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-pager"}, args...)...) //nolint:gosec // G702: argv is built by this package, never from a shell string
	env := append(stripTransportEnv(gitcmd.SanitizeEnv(os.Environ())),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ALLOW_PROTOCOL="+gitAllowedProtocols,
		// The cache is shared across runs: a refs/replace/* ref must never change what a pinned commit contains.
		"GIT_NO_REPLACE_OBJECTS=1",
	)
	if gitDir != "" {
		env = append(env, "GIT_DIR="+gitDir)
	}
	cmd.Env = env
	return cmd
}

// stripTransportEnv drops afm's internal secret transport variables, which git and its credential or ssh helpers
// must not inherit.
func stripTransportEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, config.SecretEnvPrefix) || strings.HasPrefix(kv, config.SyspromptEnvPrefix) ||
			strings.HasPrefix(kv, lifecyclehooks.HookSecretTransportPrefix) || browser.IsTransportEnv(kv) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// runGit runs git and returns stdout; a failure carries git's trimmed stderr.
func runGit(ctx context.Context, gitDir string, args ...string) (string, error) {
	cmd := newGitCmd(ctx, gitDir, args...)
	var stdout bytes.Buffer
	var stderr limitedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, stderr.String())
	}
	return stdout.String(), nil
}

// limitedBuffer keeps the head of git's diagnostics and drops the rest.
type limitedBuffer struct{ buf bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := maxGitStderr - b.buf.Len(); room > 0 {
		b.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string { return strings.TrimSpace(b.buf.String()) }

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
