package executor

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// scriptPath возвращает абсолютный путь к scripts/openai-as-claude.sh относительно
// корня модуля. Тест живёт в pkg/executor, поэтому поднимаемся на два уровня.
func scriptPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// file = .../pkg/executor/openai_translator_test.go → корень на 2 уровня выше
	root := filepath.Join(filepath.Dir(file), "..", "..")
	p := filepath.Join(root, "scripts", "openai-as-claude.sh")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("script not found: %s: %v", p, err)
	}
	return p
}

// writeFakeCurl создаёт поддельный curl, печатающий заданный SSE-ответ. Возвращает
// абсолютный путь к временному каталогу, который нужно добавить в начало PATH.
func writeFakeCurl(t *testing.T, sseResponse string) string {
	t.Helper()
	dir := t.TempDir()
	curlPath := filepath.Join(dir, "curl")
	// heredoc-стиль; пишем через сам bash, чтобы printf-эскейпы сработали единообразно.
	content := "#!/usr/bin/env bash\n" + sseResponse + "\n"
	if err := os.WriteFile(curlPath, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake curl: %v", err)
	}
	return dir
}

// TestOpenAIAsClaude_OutputParses запускает РЕАЛЬНЫЙ scripts/openai-as-claude.sh с
// поддельным curl (без сети) и проверяет, что:
//  1. stdout содержит строку {"type":"assistant",...} с агрегированным текстом;
//  2. эта строка парсится внутренним parseStreamEvent и текст совпадает;
//  3. есть корректная финальная {"type":"result","subtype":"success"} строка.
//
// Этот тест связывает скрипт-транслятор и парсер executor'а, ловя регрессию, когда
// скрипт эмитит content_block_delta (которые executor молча отбрасывает).
func TestOpenAIAsClaude_OutputParses(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}

	// Поддельный curl печатает два SSE-чанка с текстом, затем [DONE].
	fakeCurlDir := writeFakeCurl(t, `printf 'data: {"choices":[{"delta":{"content":"Hello "}}]}\ndata: {"choices":[{"delta":{"content":"world"}}]}\ndata: [DONE]\n'`)

	// PATH: сначала поддельный curl, потом системный PATH (для jq).
	cmd := exec.Command("bash", scriptPath(t))
	cmd.Env = append(os.Environ(),
		"PATH="+fakeCurlDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"OPENAI_API_KEY=test",
		"OPENAI_BASE_URL=http://fake/v1",
		"OPENAI_MODEL=m",
	)
	cmd.Stdin = bytes.NewBufferString("do the thing\n")

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("script failed: %v\noutput:\n%s", err, out)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var assistantLine, resultLine string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if strings.Contains(l, `"type":"assistant"`) && assistantLine == "" {
			assistantLine = l
		}
		if strings.Contains(l, `"type":"result"`) && resultLine == "" {
			resultLine = l
		}
	}

	if assistantLine == "" {
		t.Fatalf("no assistant envelope in output:\n%s", out)
	}
	if resultLine == "" {
		t.Fatalf("no result line in output:\n%s", out)
	}

	// Главная проверка: parseStreamEvent принимает assistant-конверт и достаёт текст.
	ev, ok := parseStreamEvent(assistantLine)
	if !ok {
		t.Fatalf("parseStreamEvent rejected assistant line:\n  %s", assistantLine)
	}
	if len(ev.Message.Content) == 0 {
		t.Fatalf("assistant envelope has no content blocks:\n  %s", assistantLine)
	}
	got := ev.Message.Content[0].Text
	const want = "Hello world"
	if got != want {
		t.Errorf("parsed text = %q, want %q (line: %s)", got, want, assistantLine)
	}

	// Финальный result-ивент должен сигнализировать успех, а не быть пустым.
	if !strings.Contains(resultLine, `"subtype":"success"`) {
		t.Errorf("result line missing subtype:success: %s", resultLine)
	}
}

// TestOpenAIAsClaude_CurlFailureStillEmitsEnvelope: даже при падении curl скрипт
// обязан выдать well-formed (возможно, пустой) assistant-конверт + result.
func TestOpenAIAsClaude_CurlFailureStillEmitsEnvelope(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}
	// Поддельный curl печатает частичный чанк и завершается с кодом 1.
	fakeCurlDir := writeFakeCurl(t, `printf 'data: {"choices":[{"delta":{"content":"partial"}}]}\n'; exit 1`)

	cmd := exec.Command("bash", scriptPath(t))
	cmd.Env = append(os.Environ(),
		"PATH="+fakeCurlDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"OPENAI_API_KEY=test",
		"OPENAI_BASE_URL=http://fake/v1",
		"OPENAI_MODEL=m",
	)
	cmd.Stdin = bytes.NewBufferString("x\n")

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("script exited non-zero on curl failure (should swallow it): %v\n%s", err, out)
	}
	if !strings.Contains(string(out), `"type":"assistant"`) {
		t.Errorf("expected assistant envelope even on curl failure, got:\n%s", out)
	}
	if !strings.Contains(string(out), `"subtype":"success"`) {
		t.Errorf("expected result success line even on curl failure, got:\n%s", out)
	}
}
