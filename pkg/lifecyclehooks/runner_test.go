package lifecyclehooks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
