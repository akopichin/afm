package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunVerifyShellCommand_StripsCrossAgentTransportSecrets — D2a код-ревью:
// shell-verify раньше исполнялся с cmd.Env==nil (== "унаследовать
// os.Environ() целиком"), включая транспортные секреты ЧУЖИХ агентов/хуков
// (AFM_SECRET_*/AFM_HOOK_SECRET_*/AFM_SYSPROMPT_*), которые попадали бы в
// command.log и (при needs_changes) в evidence отчёта. Проверяем, что они
// вырезаны, а обычный (не транспортный) env верификатора выживает.
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

// TestRunVerifyShellCommand_PreCancelledCtxNeverStarts — D4 код-ревью:
// последняя неблокирующая проверка ctx.Err() ПЕРЕД cmd.Start(). Если ctx уже
// отменён (напр. Pause() уже durable зафиксировал переход и просигналил ДО
// того, как мы дошли сюда), subprocess не должен стартовать вовсе — маркер,
// который команда создала бы первой строкой, должен остаться отсутствующим.
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
