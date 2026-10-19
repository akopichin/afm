//go:build !windows

package executor_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/executor"
)

// TestRunHelperAgent_KillsDescendantHoldingStderr: a background process the helper left behind, still holding
// the stderr pipe, is killed before the turn is reported complete — it must not keep writing the project after
// the task is recorded as finished.
func TestRunHelperAgent_KillsDescendantHoldingStderr(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	body := "sleep 30 >/dev/null &\necho $! > " + pidFile + "\n" +
		emitAssistantTextScript(t, helperValidResultJSON) + "\n" + resultSuccessLine() + "\n"
	outcome, _ := runHelperScript(t, executor.Config{Redact: func(s string) string { return s }}, body)
	if !outcome.ProcessOK {
		t.Fatalf("outcome = %+v, want a clean finish", outcome)
	}

	requireKilled(t, pidFile)
}

// TestRunHelperAgent_KillsDescendantHoldingStderrAfterFailedExit: the same leftover descendant is killed when
// the agent itself exits with an error — the exit code does not decide whether the turn's processes are cleaned up.
func TestRunHelperAgent_KillsDescendantHoldingStderrAfterFailedExit(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	body := "sleep 30 >/dev/null &\necho $! > " + pidFile + "\nexit 3\n"
	_, _ = runHelperScriptWithin(t, dir, body, 8*time.Second) // the failed exit is the point; only the cleanup matters
	requireKilled(t, pidFile)
}

// TestRunHelperAgent_KillsDescendantHoldingStdout: a descendant holding the stdout pipe does not keep the turn
// open until the idle timeout — once the agent has exited and the grace period passed, it is killed and the turn
// finishes with the agent's own result.
func TestRunHelperAgent_KillsDescendantHoldingStdout(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	body := "sleep 30 2>/dev/null &\necho $! > " + pidFile + "\n" +
		emitAssistantTextScript(t, helperValidResultJSON) + "\n" + resultSuccessLine() + "\n"
	outcome, err := runHelperScriptWithin(t, dir, body, 8*time.Second)
	if err != nil || !outcome.ProcessOK {
		t.Fatalf("outcome = %+v, err = %v, want a clean finish", outcome, err)
	}
	requireKilled(t, pidFile)
}

// runHelperScriptWithin runs body as a helper agent whose idle timeout is far longer than limit, and fails the
// test if the turn does not finish within limit.
func runHelperScriptWithin(t *testing.T, dir, body string, limit time.Duration) (executor.HelperOutcome, error) {
	t.Helper()
	scriptPath := filepath.Join(dir, "agent.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := executor.Config{Command: scriptPath, IdleTimeout: time.Minute, Redact: func(s string) string { return s }}
	type result struct {
		outcome executor.HelperOutcome
		err     error
	}
	done := make(chan result, 1)
	go func() {
		outcome, err := executor.New(cfg).RunHelperAgent(context.Background(), "t1", "reviewer", "review",
			filepath.Join(dir, "agent.log"), filepath.Join(dir, "result.json.raw"))
		done <- result{outcome, err}
	}()
	select {
	case r := <-done:
		return r.outcome, r.err
	case <-time.After(limit):
		t.Fatalf("the helper turn did not finish within %s", limit)
		return executor.HelperOutcome{}, nil
	}
}

// requireKilled fails the test unless the process whose pid is in pidFile is gone within a few seconds.
func requireKilled(t *testing.T, pidFile string) {
	t.Helper()
	pid, err := strconv.Atoi(strings.TrimSpace(readFile(t, pidFile)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	deadline := time.Now().Add(3 * time.Second)
	for !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
		if time.Now().After(deadline) {
			t.Fatalf("descendant %d still runs after the turn completed", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
