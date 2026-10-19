//go:build !windows

package executor_test

import (
	"errors"
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
