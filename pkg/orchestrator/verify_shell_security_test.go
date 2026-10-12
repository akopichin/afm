package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunVerifyShellCommand_StripsCrossAgentTransportSecrets(t *testing.T) {
	t.Setenv("AFM_SECRET_GLM51", "leaked-agent-token")
	t.Setenv("AFM_HOOK_SECRET_0_0", "leaked-hook-token")
	t.Setenv("AFM_SYSPROMPT_GLM51", "leaked-system-prompt")
	t.Setenv("SHELL_VERIFY_OWN_VAR", "own-legit-value")

	out, err := runVerifyShellCommand(context.Background(), ".", "env")
	if err != nil {
		t.Fatalf("runVerifyShellCommand: %v", err)
	}

	for _, leaked := range []string{"AFM_SECRET_GLM51=", "AFM_HOOK_SECRET_0_0=", "AFM_SYSPROMPT_GLM51="} {
		if strings.Contains(out, leaked) {
			t.Errorf("shell verify env must not contain %q, got:\n%s", leaked, out)
		}
	}
	if !strings.Contains(out, "SHELL_VERIFY_OWN_VAR=own-legit-value") {
		t.Errorf("shell verify's own legitimate env must still be inherited, got:\n%s", out)
	}
}

func TestRunVerifyShellCommand_PreCancelledCtxNeverStarts(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "marker")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // уже отменён до вызова

	_, err := runVerifyShellCommand(ctx, dir, "touch "+marker)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runVerifyShellCommand error = %v, want errors.Is(err, context.Canceled)", err)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Error("process must never have started — marker file exists")
	}
}
