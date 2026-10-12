package executor_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/orchestrator/verify"
)

// assistantTextLine строит одну stream-json строку с единственным финальным
// текстовым блоком ассистента — то же самое, что реально шлёт claude/адаптер.
func assistantTextLine(t *testing.T, text string) string {
	t.Helper()
	line, err := json.Marshal(map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": text},
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal assistant line: %v", err)
	}
	return string(line)
}

// emitAssistantTextScript возвращает shell-фрагмент, печатающий text как один финальный ассистентский
// stream-json блок.
func emitAssistantTextScript(t *testing.T, text string) string {
	t.Helper()
	return "cat <<'EOF'\n" + assistantTextLine(t, text) + "\nEOF"
}

func resultSuccessLine() string {
	return `echo '{"type":"result","subtype":"success"}'`
}

const validNeedsChangesJSON = `{"schema_version":1,"verdict":"needs_changes","summary":"found a real bug","findings":[{"blocking":true,"title":"nil deref","path":"pkg/x/y.go","line_start":10,"line_end":12,"requirement":"must not panic","evidence":"code dereferences p without a nil check","minimal_fix":"add a nil check"}]}`

func TestRunVerifyAgent_FreshSessionNoResumeNoStageDir(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "verify.log")
	resultFile := filepath.Join(dir, "result.json")
	argsFile := filepath.Join(dir, "args.txt")
	envFile := filepath.Join(dir, "env.txt")

	script := "#!/bin/bash\n" +
		`echo "$@" > ` + argsFile + "\n" +
		`printf '%s' "$AFM_STAGE_DIR" > ` + envFile + "\n" +
		emitAssistantTextScript(t, validNeedsChangesJSON) + "\n" +
		resultSuccessLine() + "\n"
	scriptPath := filepath.Join(dir, "agent.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	ex := executor.New(executor.Config{
		Command:     scriptPath,
		IdleTimeout: 5 * time.Second,
		SessionID:   "AUTHOR-SESSION-SHOULD-NOT-LEAK",
		Resume:      true,
		StageDir:    "/leaked/stage/dir",
	})

	_, err := ex.RunVerifyAgent(context.Background(), "codex", "s1", "verify the stage", logFile, resultFile)
	if err != nil {
		t.Fatalf("RunVerifyAgent: %v", err)
	}

	argsData, rErr := os.ReadFile(argsFile)
	if rErr != nil {
		t.Fatalf("read args: %v", rErr)
	}
	args := string(argsData)
	if strings.Contains(args, "--resume") {
		t.Errorf("argv must not contain --resume: %q", args)
	}
	if strings.Contains(args, "AUTHOR-SESSION-SHOULD-NOT-LEAK") {
		t.Errorf("argv must not contain the reused session id: %q", args)
	}
	if strings.Contains(args, "--session-id") {
		t.Errorf("argv must not contain --session-id for a fresh verify run: %q", args)
	}

	envData, rErr := os.ReadFile(envFile)
	if rErr != nil {
		t.Fatalf("read env: %v", rErr)
	}
	if got := strings.TrimSpace(string(envData)); got != "" {
		t.Errorf("AFM_STAGE_DIR must be empty for the verifier, got %q", got)
	}
}

func TestRunVerifyAgent_SetsCodexVerifyEnv(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "verify.log")
	resultFile := filepath.Join(dir, "result.json")
	envFile := filepath.Join(dir, "env.txt")

	script := "#!/bin/bash\n" +
		`env > ` + envFile + "\n" +
		emitAssistantTextScript(t, validNeedsChangesJSON) + "\n" +
		resultSuccessLine() + "\n"
	scriptPath := filepath.Join(dir, "agent.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	ex := executor.New(executor.Config{
		Command:     scriptPath,
		IdleTimeout: 5 * time.Second,
	})

	if _, err := ex.RunVerifyAgent(context.Background(), "codex", "s1", "verify the stage", logFile, resultFile); err != nil {
		t.Fatalf("RunVerifyAgent: %v", err)
	}

	envData, rErr := os.ReadFile(envFile)
	if rErr != nil {
		t.Fatalf("read env: %v", rErr)
	}
	if !envContainsLine(string(envData), "CODEX_VERIFY=1") {
		t.Errorf("child env missing CODEX_VERIFY=1, RunVerifyAgent must propagate VerifyMode into the subprocess env: %q", envData)
	}
}

func TestRunAgent_DoesNotSetCodexVerifyEnv(t *testing.T) {
	t.Setenv("CODEX_VERIFY", "1") // симулируем утечку из окружения afm-процесса

	dir := t.TempDir()
	logFile := filepath.Join(dir, "impl.log")
	envFile := filepath.Join(dir, "env.txt")

	script := "#!/bin/bash\n" +
		`env > ` + envFile + "\n" +
		resultSuccessLine() + "\n"
	scriptPath := filepath.Join(dir, "agent.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	ex := executor.New(executor.Config{
		Command:     scriptPath,
		IdleTimeout: 5 * time.Second,
	})

	if err := ex.RunAgent(context.Background(), "implementation", "s1", "do work", logFile); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	envData, rErr := os.ReadFile(envFile)
	if rErr != nil {
		t.Fatalf("read env: %v", rErr)
	}
	if envContainsLine(string(envData), "CODEX_VERIFY=1") {
		t.Errorf("non-verify RunAgent must not set/leak CODEX_VERIFY=1: %q", envData)
	}
}

func TestRunVerifyAgent_StripsCrossAgentTransportSecrets(t *testing.T) {
	t.Setenv("AFM_SECRET_GLM51", "leaked-agent-token")
	t.Setenv("AFM_HOOK_SECRET_0_0", "leaked-hook-token")
	t.Setenv("AFM_SYSPROMPT_GLM51", "leaked-system-prompt")
	// Обычный env верификатора, не относящийся ни к транспортным секретам, ни к Claude-авторизации автора.
	t.Setenv("MY_OWN_TOOL_VAR", "own-legit-value")

	dir := t.TempDir()
	logFile := filepath.Join(dir, "verify.log")
	resultFile := filepath.Join(dir, "result.json")
	envFile := filepath.Join(dir, "env.txt")

	script := "#!/bin/bash\n" +
		`env > ` + envFile + "\n" +
		emitAssistantTextScript(t, validNeedsChangesJSON) + "\n" +
		resultSuccessLine() + "\n"
	scriptPath := filepath.Join(dir, "agent.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	ex := executor.New(executor.Config{
		Command:     scriptPath,
		IdleTimeout: 5 * time.Second,
	})

	if _, err := ex.RunVerifyAgent(context.Background(), "codex", "s1", "verify the stage", logFile, resultFile); err != nil {
		t.Fatalf("RunVerifyAgent: %v", err)
	}

	envData, rErr := os.ReadFile(envFile)
	if rErr != nil {
		t.Fatalf("read env: %v", rErr)
	}
	got := string(envData)
	for _, leaked := range []string{"AFM_SECRET_GLM51=", "AFM_HOOK_SECRET_0_0=", "AFM_SYSPROMPT_GLM51="} {
		if strings.Contains(got, leaked) {
			t.Errorf("verifier env must not contain %q, got:\n%s", leaked, got)
		}
	}
	if !envContainsLine(got, "MY_OWN_TOOL_VAR=own-legit-value") {
		t.Errorf("verifier's own legitimate env must still be inherited: %q", got)
	}
}

func TestRunVerifyAgent_StripsClaudeAuthorCredentials(t *testing.T) {
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "leaked-oauth-token")
	t.Setenv("ANTHROPIC_API_KEY", "leaked-api-key")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "leaked-auth-token")
	t.Setenv("ANTHROPIC_BASE_URL", "https://leaked.example.com")

	dir := t.TempDir()
	logFile := filepath.Join(dir, "verify.log")
	resultFile := filepath.Join(dir, "result.json")
	envFile := filepath.Join(dir, "env.txt")

	script := "#!/bin/bash\n" +
		`env > ` + envFile + "\n" +
		emitAssistantTextScript(t, validNeedsChangesJSON) + "\n" +
		resultSuccessLine() + "\n"
	scriptPath := filepath.Join(dir, "agent.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	ex := executor.New(executor.Config{
		Command:     scriptPath,
		IdleTimeout: 5 * time.Second,
	})

	if _, err := ex.RunVerifyAgent(context.Background(), "codex", "s1", "verify the stage", logFile, resultFile); err != nil {
		t.Fatalf("RunVerifyAgent: %v", err)
	}

	envData, rErr := os.ReadFile(envFile)
	if rErr != nil {
		t.Fatalf("read env: %v", rErr)
	}
	got := string(envData)
	for _, leaked := range []string{
		"CLAUDE_CODE_OAUTH_TOKEN=", "ANTHROPIC_API_KEY=", "ANTHROPIC_AUTH_TOKEN=", "ANTHROPIC_BASE_URL=",
	} {
		if strings.Contains(got, leaked) {
			t.Errorf("verifier env must not contain Claude author credential %q, got:\n%s", leaked, got)
		}
	}
}

func TestRunVerifyAgent_KeepsOwnSecretButStripsClaudeAuthorCredentials(t *testing.T) {
	t.Setenv("AFM_SECRET_MYCODEX", "own-verifier-token")
	t.Setenv("ANTHROPIC_API_KEY", "leaked-api-key")

	wrapDir := t.TempDir()
	dir := t.TempDir()
	logFile := filepath.Join(dir, "verify.log")
	resultFile := filepath.Join(dir, "result.json")
	envFile := filepath.Join(dir, "env.txt")

	script := "#!/bin/bash\n" +
		`env > ` + envFile + "\n" +
		emitAssistantTextScript(t, validNeedsChangesJSON) + "\n" +
		resultSuccessLine() + "\n"
	scriptPath := filepath.Join(wrapDir, "mycodex")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	ex := executor.New(executor.Config{
		Command:     "mycodex",
		WrapperDir:  wrapDir,
		IdleTimeout: 5 * time.Second,
	})

	if _, err := ex.RunVerifyAgent(context.Background(), "mycodex", "s1", "verify the stage", logFile, resultFile); err != nil {
		t.Fatalf("RunVerifyAgent: %v", err)
	}

	envData, rErr := os.ReadFile(envFile)
	if rErr != nil {
		t.Fatalf("read env: %v", rErr)
	}
	got := string(envData)
	if !envContainsLine(got, "AFM_SECRET_MYCODEX=own-verifier-token") {
		t.Errorf("verifier's own recipe secret must survive, got:\n%s", got)
	}
	if strings.Contains(got, "ANTHROPIC_API_KEY=") {
		t.Errorf("verifier env must not contain the Claude author credential ANTHROPIC_API_KEY, got:\n%s", got)
	}
}

func TestRunAgent_KeepsClaudeAuthorCredentials(t *testing.T) {
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "own-oauth-token")
	t.Setenv("ANTHROPIC_API_KEY", "own-api-key")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "own-auth-token")
	t.Setenv("ANTHROPIC_BASE_URL", "https://own.example.com")

	dir := t.TempDir()
	logFile := filepath.Join(dir, "impl.log")
	envFile := filepath.Join(dir, "env.txt")

	script := "#!/bin/bash\n" +
		`env > ` + envFile + "\n" +
		resultSuccessLine() + "\n"
	scriptPath := filepath.Join(dir, "agent.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	ex := executor.New(executor.Config{
		Command:     scriptPath,
		IdleTimeout: 5 * time.Second,
	})

	if err := ex.RunAgent(context.Background(), "implementation", "s1", "do work", logFile); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	envData, rErr := os.ReadFile(envFile)
	if rErr != nil {
		t.Fatalf("read env: %v", rErr)
	}
	got := string(envData)
	for _, kept := range []string{
		"CLAUDE_CODE_OAUTH_TOKEN=own-oauth-token",
		"ANTHROPIC_API_KEY=own-api-key",
		"ANTHROPIC_AUTH_TOKEN=own-auth-token",
		"ANTHROPIC_BASE_URL=https://own.example.com",
	} {
		if !envContainsLine(got, kept) {
			t.Errorf("non-verify RunAgent must keep inheriting %q unchanged, got:\n%s", kept, got)
		}
	}
}

func TestRunVerifyAgent_KeepsOwnRecipeSecretButStripsOthers(t *testing.T) {
	t.Setenv("AFM_SECRET_MYCODEX", "own-verifier-token")
	t.Setenv("AFM_SECRET_OTHER", "someone-elses-token")
	t.Setenv("AFM_HOOK_SECRET_0_0", "leaked-hook-token")

	wrapDir := t.TempDir()
	dir := t.TempDir()
	logFile := filepath.Join(dir, "verify.log")
	resultFile := filepath.Join(dir, "result.json")
	envFile := filepath.Join(dir, "env.txt")

	script := "#!/bin/bash\n" +
		`env > ` + envFile + "\n" +
		emitAssistantTextScript(t, validNeedsChangesJSON) + "\n" +
		resultSuccessLine() + "\n"
	scriptPath := filepath.Join(wrapDir, "mycodex")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	ex := executor.New(executor.Config{
		Command:     "mycodex", // алиас — резолвится через WrapperDir, не абсолютный путь
		WrapperDir:  wrapDir,
		IdleTimeout: 5 * time.Second,
	})

	if _, err := ex.RunVerifyAgent(context.Background(), "mycodex", "s1", "verify the stage", logFile, resultFile); err != nil {
		t.Fatalf("RunVerifyAgent: %v", err)
	}

	envData, rErr := os.ReadFile(envFile)
	if rErr != nil {
		t.Fatalf("read env: %v", rErr)
	}
	got := string(envData)
	if !envContainsLine(got, "AFM_SECRET_MYCODEX=own-verifier-token") {
		t.Errorf("verifier's own recipe secret AFM_SECRET_MYCODEX must survive VerifyMode strip, got:\n%s", got)
	}
	for _, leaked := range []string{"AFM_SECRET_OTHER=", "AFM_HOOK_SECRET_0_0="} {
		if strings.Contains(got, leaked) {
			t.Errorf("verifier env must not contain %q, got:\n%s", leaked, got)
		}
	}
}

func TestRunAgent_KeepsCrossAgentTransportSecrets(t *testing.T) {
	t.Setenv("AFM_SECRET_GLM51", "leaked-agent-token")
	t.Setenv("AFM_HOOK_SECRET_0_0", "leaked-hook-token")
	t.Setenv("AFM_SYSPROMPT_GLM51", "leaked-system-prompt")

	dir := t.TempDir()
	logFile := filepath.Join(dir, "impl.log")
	envFile := filepath.Join(dir, "env.txt")

	script := "#!/bin/bash\n" +
		`env > ` + envFile + "\n" +
		resultSuccessLine() + "\n"
	scriptPath := filepath.Join(dir, "agent.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	ex := executor.New(executor.Config{
		Command:     scriptPath,
		IdleTimeout: 5 * time.Second,
	})

	if err := ex.RunAgent(context.Background(), "implementation", "s1", "do work", logFile); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	envData, rErr := os.ReadFile(envFile)
	if rErr != nil {
		t.Fatalf("read env: %v", rErr)
	}
	got := string(envData)
	for _, kept := range []string{"AFM_SECRET_GLM51=leaked-agent-token", "AFM_HOOK_SECRET_0_0=leaked-hook-token", "AFM_SYSPROMPT_GLM51=leaked-system-prompt"} {
		if !envContainsLine(got, kept) {
			t.Errorf("non-verify RunAgent must keep inheriting %q unchanged, got:\n%s", kept, got)
		}
	}
}

// envContainsLine сравнивает полную key=value строку, исключая ложное совпадение CODEX_VERIFY=10 с
// CODEX_VERIFY=1.
func envContainsLine(env, line string) bool {
	for _, l := range strings.Split(env, "\n") {
		if strings.TrimSpace(l) == line {
			return true
		}
	}
	return false
}

func TestRunVerifyAgent_HappyPath(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "verify.log")
	resultFile := filepath.Join(dir, "result.json")

	script := emitAssistantTextScript(t, validNeedsChangesJSON) + "\n" + resultSuccessLine()

	ex := executor.New(executor.Config{
		Command:     testCmdShell,
		ExtraArgs:   []string{testFlagC, script},
		IdleTimeout: 5 * time.Second,
	})

	outcome, err := ex.RunVerifyAgent(context.Background(), "codex", "s1", "verify the stage", logFile, resultFile)
	if err != nil {
		t.Fatalf("RunVerifyAgent: %v", err)
	}
	if !outcome.ProcessOK {
		t.Fatalf("ProcessOK = false, want true: %+v", outcome)
	}
	if outcome.ProtocolErr != nil {
		t.Fatalf("ProtocolErr = %v, want nil", outcome.ProtocolErr)
	}
	if outcome.Result == nil {
		t.Fatal("Result is nil, want decoded ModelResult")
	}
	if outcome.Result.Verdict != verify.VerdictNeedsChanges {
		t.Errorf("Verdict = %q, want %q", outcome.Result.Verdict, verify.VerdictNeedsChanges)
	}
	if len(outcome.Result.Findings) != 1 || !outcome.Result.Findings[0].Blocking {
		t.Errorf("Findings = %+v, want one blocking finding", outcome.Result.Findings)
	}

	// resultFile должен содержать сырые байты финального ответа.
	raw, rErr := os.ReadFile(resultFile)
	if rErr != nil {
		t.Fatalf("read result file: %v", rErr)
	}
	if !strings.Contains(string(raw), "needs_changes") {
		t.Errorf("resultFile missing raw JSON: %q", raw)
	}
}

func TestRunVerifyAgent_ExitNonZeroAfterPass(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "verify.log")
	resultFile := filepath.Join(dir, "result.json")

	passJSON := `{"schema_version":1,"verdict":"pass","summary":"all good","findings":[]}` //nolint:gosec // test fixture text, not a real secret
	script := emitAssistantTextScript(t, passJSON) + "\nexit 1"

	ex := executor.New(executor.Config{
		Command:     testCmdShell,
		ExtraArgs:   []string{testFlagC, script},
		IdleTimeout: 5 * time.Second,
	})

	outcome, _ := ex.RunVerifyAgent(context.Background(), "codex", "s1", "verify the stage", logFile, resultFile)
	if outcome.ProcessOK {
		t.Fatalf("ProcessOK = true, want false (nonzero exit after emitting pass): %+v", outcome)
	}
	if outcome.Result != nil {
		t.Fatalf("Result must be nil when the process did not exit cleanly, got %+v", outcome.Result)
	}
}

func TestRunVerifyAgent_ProtocolErrOnEmptyFinalMessage(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "verify.log")
	resultFile := filepath.Join(dir, "result.json")

	script := resultSuccessLine() // только result-строка, ни одного text-блока

	ex := executor.New(executor.Config{
		Command:     testCmdShell,
		ExtraArgs:   []string{testFlagC, script},
		IdleTimeout: 5 * time.Second,
	})

	outcome, err := ex.RunVerifyAgent(context.Background(), "codex", "s1", "verify the stage", logFile, resultFile)
	if err != nil {
		t.Fatalf("RunVerifyAgent unexpected error: %v", err)
	}
	if !outcome.ProcessOK {
		t.Fatalf("ProcessOK = false, want true (clean exit): %+v", outcome)
	}
	if outcome.ProtocolErr == nil {
		t.Fatal("ProtocolErr = nil, want an error for an empty final answer")
	}
	if outcome.Result != nil {
		t.Fatalf("Result must be nil on protocol error, got %+v", outcome.Result)
	}
}

// TestRunVerifyAgent_Interrupted проверяет, что сигнал на InterruptCh
// приводит к Interrupted=true, а не к молчаливому ProcessOK/pass.
func TestRunVerifyAgent_Interrupted(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "verify.log")
	resultFile := filepath.Join(dir, "result.json")

	script := "trap 'touch " + dir + "/signaled; exit 0' INT\n" +
		"touch " + dir + "/ready\n" +
		"while :; do sleep 0.1; done\n"
	scriptPath := filepath.Join(dir, "agent.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}

	interruptCh := make(chan struct{}, 1)
	ex := executor.New(executor.Config{
		Command:     scriptPath,
		IdleTimeout: 10 * time.Second,
		InterruptCh: interruptCh,
	})

	done := make(chan verify.RunOutcome, 1)
	errCh := make(chan error, 1)
	go func() {
		outcome, err := ex.RunVerifyAgent(context.Background(), "codex", "s1", "verify the stage", logFile, resultFile)
		errCh <- err
		done <- outcome
	}()

	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "ready")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("script did not reach trap installation within 10s")
		}
		time.Sleep(10 * time.Millisecond)
	}
	interruptCh <- struct{}{}

	select {
	case outcome := <-done:
		if err := <-errCh; err != nil {
			t.Fatalf("RunVerifyAgent returned unexpected error: %v", err)
		}
		if !outcome.Interrupted {
			t.Errorf("Interrupted = false, want true: %+v", outcome)
		}
		if outcome.ProcessOK {
			t.Errorf("ProcessOK = true, want false on interrupt: %+v", outcome)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("RunVerifyAgent did not return within 20s of interrupt signal")
	}

	if _, err := os.Stat(filepath.Join(dir, "signaled")); err != nil {
		t.Errorf("script did not receive SIGINT (marker file missing): %v", err)
	}
}
