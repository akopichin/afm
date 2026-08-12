package executor

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
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

// writeTestPNG writes a small valid solid-red PNG to a temp file and returns its
// path. Used to verify the real bytes an [Screenshot: <path>] reference embeds
// round-trip correctly through base64 in the request body.
func writeTestPNG(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.png")
	img := image.NewRGBA(image.Rect(0, 0, 20, 20))
	for y := 0; y < 20; y++ {
		for x := 0; x < 20; x++ {
			img.Set(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create test png: %v", err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatalf("encode test png: %v", err)
	}
	return path
}

// readTestPNGBase64 reads path and returns its raw bytes' standard base64
// encoding, for comparing against the base64 payload embedded in a captured
// request body's image_url data URL.
func readTestPNGBase64(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read test png: %v", err)
	}
	return base64.StdEncoding.EncodeToString(data)
}

// decodeCapturedRequestBody extracts and JSON-decodes the {"...":...} request body
// embedded inside a fake curl's captured "$*" argument dump (which also contains
// unrelated curl flags/headers/URL as plain text before and after the JSON). It
// finds the first "{" and decodes exactly one JSON value from there — json.Decoder
// stops once one value is fully read, ignoring any trailing text.
func decodeCapturedRequestBody(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	idx := bytes.Index(raw, []byte(`{"`))
	if idx < 0 {
		t.Fatalf("no JSON object found in captured args: %s", raw)
	}
	var body map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw[idx:]))
	if err := dec.Decode(&body); err != nil {
		t.Fatalf("decode captured request body: %v\nraw: %s", err, raw)
	}
	return body
}

// writeFakeCurlCapturing creates a fake curl (single response, single invocation)
// that both returns sseResponse and captures its own full "$*" argument list to
// <dir>/captured.args, so a test can inspect exactly what request body a
// single-shot script (openai-as-claude.sh) sent.
func writeFakeCurlCapturing(t *testing.T, sseResponse string) (fakeCurlDir, captureFile string) {
	t.Helper()
	dir := t.TempDir()
	captureFile = filepath.Join(dir, "captured.args")
	curlPath := filepath.Join(dir, "curl")
	content := fmt.Sprintf("#!/usr/bin/env bash\nprintf '%%s' \"$*\" > %q\n%s\n", captureFile, sseResponse)
	if err := os.WriteFile(curlPath, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake curl: %v", err)
	}
	return dir, captureFile
}

// TestOpenAIAsClaude_ScreenshotMarkerEmbedsImage: a prompt containing
// [Screenshot: <path-to-a-real-png>] must produce a multimodal request body —
// content becomes an array with a text block (marker stripped) and an
// image_url block whose base64 payload matches the source file's bytes exactly.
func TestOpenAIAsClaude_ScreenshotMarkerEmbedsImage(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}

	pngPath := writeTestPNG(t)
	wantB64 := readTestPNGBase64(t, pngPath)

	fakeCurlDir, captureFile := writeFakeCurlCapturing(t, `printf 'data: {"choices":[{"delta":{"content":"ok"}}]}\ndata: [DONE]\n'`)

	cmd := exec.Command("bash", scriptPath(t))
	cmd.Env = append(os.Environ(),
		"PATH="+fakeCurlDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"OPENAI_API_KEY=test",
		"OPENAI_BASE_URL=http://fake/v1",
		"OPENAI_MODEL=m",
	)
	cmd.Stdin = strings.NewReader(fmt.Sprintf("what color is [Screenshot: %s]?\n", pngPath))

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("script failed: %v\noutput:\n%s", err, out)
	}

	raw, readErr := os.ReadFile(captureFile)
	if readErr != nil {
		t.Fatalf("read captured request: %v", readErr)
	}
	body := decodeCapturedRequestBody(t, raw)

	messages, ok := body["messages"].([]any)
	if !ok || len(messages) == 0 {
		t.Fatalf("messages missing/wrong shape: %v", body["messages"])
	}
	msg0, _ := messages[0].(map[string]any)
	content, ok := msg0["content"].([]any)
	if !ok {
		t.Fatalf("expected content to be a multimodal array, got %T: %v", msg0["content"], msg0["content"])
	}
	if len(content) != 2 {
		t.Fatalf("expected 2 content blocks (text + image), got %d: %v", len(content), content)
	}
	textBlock, _ := content[0].(map[string]any)
	if textBlock["type"] != "text" {
		t.Errorf("content[0].type = %v, want \"text\"", textBlock["type"])
	}
	if strings.Contains(fmt.Sprint(textBlock["text"]), "[Screenshot:") {
		t.Errorf("marker should be stripped from text block, got: %v", textBlock["text"])
	}
	imgBlock, _ := content[1].(map[string]any)
	if imgBlock["type"] != "image_url" {
		t.Errorf("content[1].type = %v, want \"image_url\"", imgBlock["type"])
	}
	imageURLField, _ := imgBlock["image_url"].(map[string]any)
	url, _ := imageURLField["url"].(string)
	wantURL := "data:image/png;base64," + wantB64
	if url != wantURL {
		t.Errorf("image_url = %q, want %q", url, wantURL)
	}
}

// TestOpenAIAsClaude_NoMarkerContentUnchanged: a prompt with no [Screenshot: ...]
// reference must keep content as a plain string, exactly as before this feature.
func TestOpenAIAsClaude_NoMarkerContentUnchanged(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}

	fakeCurlDir, captureFile := writeFakeCurlCapturing(t, `printf 'data: {"choices":[{"delta":{"content":"ok"}}]}\ndata: [DONE]\n'`)

	cmd := exec.Command("bash", scriptPath(t))
	cmd.Env = append(os.Environ(),
		"PATH="+fakeCurlDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"OPENAI_API_KEY=test",
		"OPENAI_BASE_URL=http://fake/v1",
		"OPENAI_MODEL=m",
	)
	cmd.Stdin = strings.NewReader("do the thing\n")

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("script failed: %v\noutput:\n%s", err, out)
	}

	raw, readErr := os.ReadFile(captureFile)
	if readErr != nil {
		t.Fatalf("read captured request: %v", readErr)
	}
	body := decodeCapturedRequestBody(t, raw)
	messages, _ := body["messages"].([]any)
	msg0, _ := messages[0].(map[string]any)
	content, ok := msg0["content"].(string)
	if !ok {
		t.Fatalf("expected content to stay a plain string, got %T: %v", msg0["content"], msg0["content"])
	}
	if content != "do the thing" {
		t.Errorf("content = %q, want %q", content, "do the thing")
	}
}

// TestOpenAIAsClaude_MissingScreenshotFileFallsBackToText: a [Screenshot: <path>]
// reference to a file that doesn't exist must not crash the script — it falls
// back to sending the original text (marker included) as a plain string.
func TestOpenAIAsClaude_MissingScreenshotFileFallsBackToText(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}

	fakeCurlDir, captureFile := writeFakeCurlCapturing(t, `printf 'data: {"choices":[{"delta":{"content":"ok"}}]}\ndata: [DONE]\n'`)

	cmd := exec.Command("bash", scriptPath(t))
	cmd.Env = append(os.Environ(),
		"PATH="+fakeCurlDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"OPENAI_API_KEY=test",
		"OPENAI_BASE_URL=http://fake/v1",
		"OPENAI_MODEL=m",
	)
	prompt := "check [Screenshot: /nonexistent/path.png] please\n"
	cmd.Stdin = strings.NewReader(prompt)

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("script failed: %v\noutput:\n%s", err, out)
	}

	raw, readErr := os.ReadFile(captureFile)
	if readErr != nil {
		t.Fatalf("read captured request: %v", readErr)
	}
	body := decodeCapturedRequestBody(t, raw)
	messages, _ := body["messages"].([]any)
	msg0, _ := messages[0].(map[string]any)
	content, ok := msg0["content"].(string)
	if !ok {
		t.Fatalf("expected content to fall back to a plain string, got %T: %v", msg0["content"], msg0["content"])
	}
	want := strings.TrimSuffix(prompt, "\n")
	if content != want {
		t.Errorf("content = %q, want unchanged prompt %q", content, want)
	}
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
