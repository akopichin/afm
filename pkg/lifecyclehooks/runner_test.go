package lifecyclehooks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func hasEnvKey(env []string, key string) bool {
	for _, kv := range env {
		if kv == key || strings.HasPrefix(kv, key+"=") {
			return true
		}
	}
	return false
}

func hasEnv(env []string, key, val string) bool {
	for _, kv := range env {
		if kv == key+"="+val {
			return true
		}
	}
	return false
}

func TestBuildEnv_Modes(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("UNRELATED_KEY", "leak-me")
	t.Setenv("AFM_HOOK_SECRET_0_0", "transport-secret") // должен вырезаться при inherit
	cfg := DispatcherConfig{RunID: "r"}
	p := BuildPayload(cfg, Event{Type: EventFlowStarted}, "id")

	// spec-дефолт: без inherit_env → минимальное окружение (UNRELATED НЕ виден)
	e1 := buildEnv(Hook{ID: "h"}, cfg, p)
	if hasEnvKey(e1, "UNRELATED_KEY") {
		t.Fatal("default must be minimal env (no ambient leakage)")
	}
	if !hasEnvKey(e1, "PATH") || !hasEnv(e1, "AFM_HOOK_EVENT", "flow_started") {
		t.Fatalf("minimal must include PATH + AFM_*: %v", e1)
	}
	// с Env → минимальное + резолвнутый секрет
	e2 := buildEnv(Hook{ID: "h", Env: map[string]SecretRef{"T": "env:X"}, ResolvedEnv: map[string]string{"T": "secret"}}, cfg, p)
	if hasEnvKey(e2, "UNRELATED_KEY") || !hasEnv(e2, "T", "secret") {
		t.Fatalf("env-hook: minimal + resolved: %v", e2)
	}
	// inherit_env: true → наследование + resolved, но БЕЗ transport-переменных
	e3 := buildEnv(Hook{ID: "h", InheritEnv: true, Env: map[string]SecretRef{"T": "env:X"}, ResolvedEnv: map[string]string{"T": "secret"}}, cfg, p)
	if !hasEnv(e3, "UNRELATED_KEY", "leak-me") || !hasEnv(e3, "T", "secret") {
		t.Fatal("inherit_env:true must inherit AND include resolved")
	}
	if hasEnvKey(e3, "AFM_HOOK_SECRET_0_0") {
		t.Fatal("inherit_env:true must still strip transport vars (codex #2)")
	}
}

func TestRunCommand_RedactsSecretInLog(t *testing.T) {
	dir := t.TempDir()
	h := Hook{ID: "h", Command: "printf 'my token is s3cr3t\\n'", Timeout: 5 * time.Second,
		Env: map[string]SecretRef{"T": "env:X"}, ResolvedEnv: map[string]string{"T": "s3cr3t"}}
	cfg := DispatcherConfig{RunID: "r", RunDir: dir, RootDir: dir}
	p := BuildPayload(cfg, Event{Type: EventFlowStarted, Time: time.Now()}, "id")
	logPath := filepath.Join(dir, "h.log")
	if err := runCommand(context.Background(), h, cfg, p, logPath); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(logPath)
	if strings.Contains(string(raw), "s3cr3t") {
		t.Fatalf("secret leaked into log: %s", raw)
	}
	if !strings.Contains(string(raw), defaultRedactionMarker) {
		t.Fatalf("expected redaction: %s", raw)
	}
}

// TestRunCommand_HeaderRoutedThroughRedactor — Finding #3: заголовок попытки
// (event/id/stage) раньше писался напрямую в logFile ДО оборачивания
// редактором. Проверяем defense-in-depth: если (гипотетически) значение
// секрета хука совпадает с полем заголовка (здесь — stage id), это значение
// не должно попасть в лог сырым.
func TestRunCommand_HeaderRoutedThroughRedactor(t *testing.T) {
	dir := t.TempDir()
	h := Hook{ID: "h", Command: "true", Timeout: 5 * time.Second,
		Env: map[string]SecretRef{"T": "env:X"}, ResolvedEnv: map[string]string{"T": "s3cr3t-stage"}}
	cfg := DispatcherConfig{RunID: "r", RunDir: dir, RootDir: dir}
	p := BuildPayload(cfg, Event{Type: EventStageFailed, StageID: "s3cr3t-stage", Time: time.Now()}, "eid-header")
	logPath := filepath.Join(dir, "h.log")
	if err := runCommand(context.Background(), h, cfg, p, logPath); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(logPath)
	if strings.Contains(string(raw), "s3cr3t-stage") {
		t.Fatalf("secret leaked raw via attempt header: %s", raw)
	}
	if !strings.Contains(string(raw), defaultRedactionMarker) {
		t.Fatalf("expected header to be redacted: %s", raw)
	}
	if !strings.Contains(string(raw), "event=stage_failed") {
		t.Fatalf("header must still carry non-secret metadata: %s", raw)
	}
}

func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunCommand_Success_StdinAndEnv(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.log")
	script := writeScript(t, dir, "capture.sh", `#!/bin/sh
cat > "`+out+`.payload"
printf '%s|%s|%s|%s' "$AFM_HOOK_EVENT" "$AFM_RUN_ID" "$AFM_STAGE_ID" "$AFM_FLOW_NAME" > "`+out+`"
`)
	h := Hook{ID: "cap", Command: script, Timeout: 5 * time.Second}
	cfg := DispatcherConfig{FlowName: "fl", RunID: "run-1", RunDir: dir, RootDir: dir}
	p := BuildPayload(cfg, Event{Type: EventStageFailed, StageID: "deploy", Time: time.Now()}, "eid-1")
	logPath := filepath.Join(dir, "hook.log")

	if err := runCommand(context.Background(), h, cfg, p, logPath); err != nil {
		t.Fatalf("runCommand: %v", err)
	}
	got, _ := os.ReadFile(out)
	if string(got) != "stage_failed|run-1|deploy|fl" {
		t.Fatalf("env capture: %q", got)
	}
	payloadRaw, _ := os.ReadFile(out + ".payload")
	if !strings.Contains(string(payloadRaw), `"schema_version":1`) || !strings.Contains(string(payloadRaw), `"event":"stage_failed"`) {
		t.Fatalf("payload: %s", payloadRaw)
	}
	logRaw, _ := os.ReadFile(logPath)
	if !strings.Contains(string(logRaw), "stage_failed") {
		t.Fatalf("log must mention event: %s", logRaw)
	}
}

func TestRunCommand_RetriesThenSuccess(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "attempts")
	script := writeScript(t, dir, "flaky.sh", `#!/bin/sh
n=$(cat "`+marker+`" 2>/dev/null || echo 0)
n=$((n+1))
echo "$n" > "`+marker+`"
[ "$n" -ge 3 ] || exit 1
`)
	h := Hook{ID: "flaky", Command: script, Timeout: 5 * time.Second, Retries: 3}
	cfg := DispatcherConfig{RunID: "r", RunDir: dir, RootDir: dir}
	p := BuildPayload(cfg, Event{Type: EventFlowStarted, Time: time.Now()}, "eid-2")

	start := time.Now()
	if err := runCommand(context.Background(), h, cfg, p, filepath.Join(dir, "h.log")); err != nil {
		t.Fatalf("expected eventual success: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 2*hookRetryBackoff { // минимум 2 паузы между 3 попытками
		t.Fatalf("backoff not applied: %v", elapsed)
	}
}

func TestRunCommand_FinalFailure(t *testing.T) {
	dir := t.TempDir()
	h := Hook{ID: "bad", Command: "exit 7", Timeout: 5 * time.Second, Retries: 1}
	cfg := DispatcherConfig{RunID: "r", RunDir: dir, RootDir: dir}
	p := BuildPayload(cfg, Event{Type: EventFlowStarted, Time: time.Now()}, "eid-3")

	err := runCommand(context.Background(), h, cfg, p, filepath.Join(dir, "h.log"))
	if err == nil || !strings.Contains(err.Error(), "exit status") {
		t.Fatalf("want exit-status error, got %v", err)
	}
}

func TestRunCommand_TimeoutKillsProcessGroup(t *testing.T) {
	dir := t.TempDir()
	// скрипт порождает внучка sleep и сам висит: kill только прямого ребёнка
	// оставил бы pipe открытым (урок из pkg/executor — killProcessGroup).
	script := writeScript(t, dir, "hang.sh", `#!/bin/sh
sleep 30 &
sleep 30
`)
	h := Hook{ID: "hang", Command: script, Timeout: 300 * time.Millisecond}
	cfg := DispatcherConfig{RunID: "r", RunDir: dir, RootDir: dir}
	p := BuildPayload(cfg, Event{Type: EventFlowStarted, Time: time.Now()}, "eid-4")

	start := time.Now()
	err := runCommand(context.Background(), h, cfg, p, filepath.Join(dir, "h.log"))
	if err == nil {
		t.Fatal("want timeout error")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("timeout did not kill the group: %v", time.Since(start))
	}
}
