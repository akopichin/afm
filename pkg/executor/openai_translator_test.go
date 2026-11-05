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
	"strconv"
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

// writeTestPNG writes a small valid solid-red PNG to a temp file and returns its path.
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

// readTestPNGBase64 reads path and returns its raw bytes' standard base64 encoding, for comparing against
// the base64 payload embedded in a captured request body's image_url data URL.
func readTestPNGBase64(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read test png: %v", err)
	}
	return base64.StdEncoding.EncodeToString(data)
}

// decodeCapturedRequestBody decodes the body captured by a fake curl.
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

// Fake curl must read file/stdin bodies while the adapter's temporary files
// still exist. Keep support for inline bodies to exercise either transport.
const captureCurlBody = `
capture_body() {
    while [[ $# -gt 0 ]]; do
        case "$1" in
            -d|--data|--data-binary)
                shift
                case "$1" in
                    @-) cat ;;
                    @*) cat -- "${1#@}" ;;
                    *) printf '%s' "$1" ;;
                esac
                return
                ;;
        esac
        shift
    done
    return 1
}
`

// writeFakeCurlCapturing returns one SSE response and saves the request body.
func writeFakeCurlCapturing(t *testing.T, sseResponse string) (fakeCurlDir, captureFile string) {
	t.Helper()
	dir := t.TempDir()
	captureFile = filepath.Join(dir, "captured.args")
	curlPath := filepath.Join(dir, "curl")
	content := fmt.Sprintf("#!/usr/bin/env bash\n%scapture_body \"$@\" > %q\n%s\n", captureCurlBody, captureFile, sseResponse)
	if err := os.WriteFile(curlPath, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake curl: %v", err)
	}
	return dir, captureFile
}

func TestOpenAIAsClaude_ScreenshotMarkerEmbedsImage(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}

	pngPath := writeTestPNG(t)
	wantB64 := readTestPNGBase64(t, pngPath)

	fakeCurlDir, captureFile := writeFakeCurlCapturing(t, `printf 'data: {"choices":[{"delta":{"content":"ok"}}]}\ndata: [DONE]\n'; printf '200'`)

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

	fakeCurlDir, captureFile := writeFakeCurlCapturing(t, `printf 'data: {"choices":[{"delta":{"content":"ok"}}]}\ndata: [DONE]\n'; printf '200'`)

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

func TestOpenAIAsClaude_MissingScreenshotFileFallsBackToText(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}

	fakeCurlDir, captureFile := writeFakeCurlCapturing(t, `printf 'data: {"choices":[{"delta":{"content":"ok"}}]}\ndata: [DONE]\n'; printf '200'`)

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

func TestOpenAIAsClaude_OutputParses(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}

	// Поддельный curl печатает два SSE-чанка с текстом, затем [DONE].
	fakeCurlDir := writeFakeCurl(t, `printf 'data: {"choices":[{"delta":{"content":"Hello "}}]}\ndata: {"choices":[{"delta":{"content":"world"}}]}\ndata: [DONE]\n'; printf '200'`)

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

// TestOpenAIAsClaude_CurlFailureWithPartialBodyStillFails: a curl invocation that
// emitted a partial chunk before dying with a non-zero exit code must still fail
// the stage (no success envelope) — partial stdout must not be mistaken for a
// completed response. See TestOpenAIAsClaude_CurlFailureExitsNonZero in
// openai_failure_test.go for the plain (no partial body) case.
func TestOpenAIAsClaude_CurlFailureWithPartialBodyStillFails(t *testing.T) {
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

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err == nil {
		t.Fatalf("expected non-zero exit on curl failure, got success; stdout:\n%s", out)
	}
	if !strings.Contains(stderr.String(), "curl exit 1") {
		t.Errorf("expected stderr to mention \"curl exit 1\", got:\n%s", stderr.String())
	}
	if strings.Contains(string(out), `"subtype":"success"`) {
		t.Errorf("must not report success after curl failure, got:\n%s", out)
	}
}

// runTextExt runs openai-as-claude.sh with the stream extension against a fake curl that prints body and then
// the given tail; it returns stdout lines decoded as JSON objects and the exit error.
func runTextExt(t *testing.T, body, curlTail string, extraEnv ...string) ([]map[string]any, string, error) {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "body"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := writeFakeCurl(t, "cat "+strconv.Quote(filepath.Join(dir, "body"))+"\n"+curlTail)
	cmd := exec.Command("bash", scriptPath(t))
	cmd.Env = append(os.Environ(), "PATH="+fake+string(os.PathListSeparator)+os.Getenv("PATH"),
		"OPENAI_API_KEY=test", "OPENAI_BASE_URL=http://fake/v1", "OPENAI_MODEL=m", StreamExtensionEnv+"=1")
	cmd.Env = append(cmd.Env, extraEnv...)
	cmd.Stdin = strings.NewReader("do it\n")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	return decodeLines(string(out)), stderr.String(), err
}

const textUsageChunk = `data: {"model":"m-1","choices":[],"usage":{"prompt_tokens":7,"completion_tokens":3}}` + "\n"

func TestOpenAIAsClaude_StreamExtension_FinalOnlyAfterACompleteStream(t *testing.T) {
	lines, _, err := runTextExt(t, contentChunk("Hello ")+contentChunk("world")+
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n"+textUsageChunk+"data: [DONE]\n", `printf '\n200'`)
	if err != nil {
		t.Fatalf("script failed: %v", err)
	}
	if first := lines[0]; first["type"] != "system" || first["subtype"] != "afm_stream" {
		t.Fatalf("first line = %v, want the announcement", first)
	}
	finals := finalLines(lines)
	if len(finals) != 1 || finals[0]["text"] != "Hello world" || finals[0]["item_id"] == nil {
		t.Fatalf("afm_final = %v", finals)
	}
	msgs := lineOfType(lines, "assistant")
	if len(msgs) != 1 || !afmComplete(msgs[0]) || afmField(msgs[0])["item_id"] != finals[0]["item_id"] {
		t.Errorf("message lines = %v, want one complete message that afm_final names", msgs)
	}
	types := lineTypes(lines)
	if types[len(types)-2] != "afm_final" || types[len(types)-1] != "result" {
		t.Errorf("line types = %v", types)
	}
	if res := lineOfType(lines, "result")[0]; res["subtype"] != "success" || res["model"] != "m-1" {
		t.Errorf("result = %v", res)
	}
}

func TestOpenAIAsClaude_StreamExtension_CutStreamIsAnErrorNotAPartialSuccess(t *testing.T) {
	cut := contentChunk("half an ans")
	cases := map[string]struct{ body, tail string }{
		"curl exit after partial body": {cut, `exit 18`},
		"clean close without a finish": {cut, `printf '\n200'`},
		"http error":                   {`{"error":"nope"}` + "\n", `printf '\n503'`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			lines, stderr, err := runTextExt(t, c.body, c.tail)
			if err == nil {
				t.Fatalf("a cut stream must exit non-zero; lines %v", lines)
			}
			if finals := finalLines(lines); len(finals) != 0 {
				t.Errorf("afm_final after a cut stream: %v", finals)
			}
			res := lineOfType(lines, "result")
			if len(res) != 1 || !isErrorResult(res[0]) || res[0]["subtype"] != "error_during_execution" || stderr == "" {
				t.Errorf("result %v stderr %q, want an error result and a stderr message", res, stderr)
			}
		})
	}
}

func TestOpenAIAsClaude_StreamExtension_ManySmallChunksStayBounded(t *testing.T) {
	var sb strings.Builder
	const chunks = 2000
	for range chunks {
		sb.WriteString(contentChunk("ab"))
	}
	sb.WriteString(`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\ndata: [DONE]\n")
	lines, _, err := runTextExt(t, sb.String(), `printf '\n200'`)
	if err != nil {
		t.Fatalf("script failed: %v", err)
	}
	raw, _ := json.Marshal(lines)
	if len(lines) > 40 || len(raw) > 64<<10 {
		t.Errorf("%d lines / %d bytes for %d chunks, want bounded and roughly linear", len(lines), len(raw), chunks)
	}
	if finals := finalLines(lines); len(finals) != 1 || len(finals[0]["text"].(string)) != chunks*2 {
		t.Errorf("afm_final = %v, want the whole text", finals)
	}
}

func TestOpenAIAsClaude_StreamExtension_UsageAndOldProtocolUnchanged(t *testing.T) {
	body := contentChunk("fin") + `data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n" + textUsageChunk + "data: [DONE]\n"
	ext, _, err := runTextExt(t, body, `printf '\n200'`)
	if err != nil {
		t.Fatal(err)
	}
	plain, _, err := runTextExt(t, body, `printf '\n200'`, StreamExtensionEnv+"=0")
	if err != nil {
		t.Fatal(err)
	}
	if a, b := lineOfType(ext, "result")[0], lineOfType(plain, "result")[0]; fmt.Sprint(a) != fmt.Sprint(b) {
		t.Errorf("result differs:\n ext:   %v\n plain: %v", a, b)
	}
	raw, _ := json.Marshal(plain)
	for _, banned := range []string{"afm_stream", "afm_final", `"afm"`} {
		if strings.Contains(string(raw), banned) {
			t.Errorf("old-protocol output contains %s: %s", banned, raw)
		}
	}
}
