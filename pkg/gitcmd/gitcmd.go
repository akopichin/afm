// Package gitcmd runs git subprocesses with a sanitized environment.
package gitcmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"
)

// DefaultTimeout bounds a single git invocation.
const DefaultTimeout = 10 * time.Second

// maxOutputBytes caps stdout captured from one git process; the rest is
// drained and discarded so a runaway command can't exhaust memory.
const maxOutputBytes = 4 << 20

// SanitizeEnv returns env without any GIT_* variable: an ambient GIT_DIR or
// GIT_COMMON_DIR would silently redirect an explicitly addressed repository.
func SanitizeEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, "GIT_") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// cappedWriter captures up to cap bytes and keeps draining past it (returns
// len(p), nil) so the child process is never blocked on a full pipe.
type cappedWriter struct {
	buf bytes.Buffer
	cap int
}

func (c *cappedWriter) Write(p []byte) (int, error) {
	if room := c.cap - c.buf.Len(); room > 0 {
		if len(p) <= room {
			c.buf.Write(p)
		} else {
			c.buf.Write(p[:room])
		}
	}
	return len(p), nil
}

// Run executes `git -C dir <args...>` with a sanitized environment and no pager, bounded by DefaultTimeout.
// ok reports a zero exit status; a non-zero exit is returned as ok=false, err=nil.
// err is non-nil only when git could not be run at all (or the context ended).
func Run(ctx context.Context, dir string, args ...string) (stdout string, ok bool, err error) {
	return RunWithTimeout(ctx, DefaultTimeout, dir, args...)
}

// RunWithTimeout is Run with an explicit timeout, for invocations — like a worktree checkout or
// teardown — that do real file I/O and can legitimately take longer than DefaultTimeout on a large
// repo; a kill mid-checkout would otherwise leave a half-populated worktree that later looks valid.
func RunWithTimeout(ctx context.Context, timeout time.Duration, dir string, args ...string) (stdout string, ok bool, err error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	full := append([]string{"--no-pager", "-C", dir}, args...)
	cmd := exec.CommandContext(cctx, "git", full...)
	cmd.Env = SanitizeEnv(os.Environ())
	out := &cappedWriter{cap: maxOutputBytes}
	cmd.Stdout = out
	cmd.Stderr = nil // discard git diagnostics

	runErr := cmd.Run()
	if runErr == nil {
		return out.buf.String(), true, nil
	}
	if cerr := cctx.Err(); cerr != nil {
		return "", false, cerr // deadline / cancellation → infra failure
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		return "", false, nil // git exited non-zero → benign signal
	}
	return "", false, runErr // could not start git → infra failure
}
