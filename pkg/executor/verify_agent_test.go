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

// emitAssistantTextScript возвращает shell-фрагмент, печатающий text как
// один финальный ассистентский stream-json блок. Heredoc с закавыченным
// разделителем ('EOF') не подставляет ничего внутри — весь JSON уходит в
// stdout буквально, без риска конфликта shell-экранирования с кавычками
// внутри самого JSON (та же техника, что writeFakeCodex в codex_translator_test.go).
func emitAssistantTextScript(t *testing.T, text string) string {
	t.Helper()
	return "cat <<'EOF'\n" + assistantTextLine(t, text) + "\nEOF"
}

func resultSuccessLine() string {
	return `echo '{"type":"result","subtype":"success"}'`
}

const validNeedsChangesJSON = `{"schema_version":1,"verdict":"needs_changes","summary":"found a real bug","findings":[{"blocking":true,"title":"nil deref","path":"pkg/x/y.go","line_start":10,"line_end":12,"requirement":"must not panic","evidence":"code dereferences p without a nil check","minimal_fix":"add a nil check"}]}`

// TestRunVerifyAgent_FreshSessionNoResumeNoStageDir воспроизводит требование
// V2b.2: верификатор — ВСЕГДА свежая сессия, независимо от того, что несёт
// общий Config (унаследованный от автора --resume/session-id) и StageDir
// (диалоговый протокол верификатору не положен, §7.3 плана). Command —
// отдельный исполняемый скрипт-файл (не "bash -c"), чтобы захватить РЕАЛЬНЫЙ
// argv процесса, а не позиционные параметры внутри -c script.
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

// TestRunVerifyAgent_HappyPath проверяет, что валидный JSON, пришедший как
// финальный ассистентский текстовый блок, декодируется в Result, а процесс
// признаётся ProcessOK.
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

// TestRunVerifyAgent_ExitNonZeroAfterPass воспроизводит §5.4 плана: процесс
// вывел "pass" в финальном тексте, но завершился с ненулевым кодом — такой
// запуск НИКОГДА не считается пройденным.
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

// TestRunVerifyAgent_ProtocolErrOnEmptyFinalMessage — процесс завершился
// штатно (exit 0), но не оставил финального ассистентского текста → ошибка
// протокола, не "pass" по умолчанию.
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
