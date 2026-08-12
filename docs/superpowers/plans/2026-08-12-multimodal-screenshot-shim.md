# Multimodal `[Screenshot: <path>]` Shim Support Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `[Screenshot: <path>]` references (already produced by the dashboard's clipboard-paste feature) reach a vision-capable model as a real embedded image in both `scripts/openai-as-claude.sh` and `scripts/openai-agent-as-claude.sh`, via both paths a marker can reach an agent today — the initial prompt, and (for the tool-loop script only) a mid-loop tool result.

**Architecture:** Two small bash functions (`extract_image_blocks`, `build_user_content`) added near-identically to both scripts — no shared library file, matching the existing convention that each adapter script is fully self-contained. They detect `[Screenshot: <path>]` markers, base64-encode readable/recognized image files, and build an OpenAI-format multimodal `content` array in place of the plain string used today. No Go/config changes anywhere — this is adapter-script behavior only.

**Tech Stack:** bash + `jq` + `grep -P`/`sed -E`/`base64 -w0` (all GNU, verified present in `akopichin/afm:latest`), Go `testing` + `image/png` (test fixtures only).

## Global Constraints

- Do not change the Go version in go.mod.
- Every task touching `.go` files must leave `make lint` clean.
- Commit messages must be in Russian, no `Co-Authored-By` trailer.
- No new tool surface, no new config fields (no `vision:` flag) — the script always attempts embedding when it finds a marker.
- Spec: `docs/superpowers/specs/2026-08-12-multimodal-screenshot-shim-design.md`.

---

### Task 1: `extract_image_blocks`/`build_user_content` + wiring in `openai-as-claude.sh`

**Files:**
- Modify: `scripts/openai-as-claude.sh:42-44`
- Modify: `pkg/executor/openai_translator_test.go` (new imports + new helpers + new tests)

**Interfaces:**
- Produces (Go test helpers, same package `executor`, reused by Tasks 2 and 3): `writeTestPNG(t *testing.T) string` (path to a real, valid 20x20 PNG), `readTestPNGBase64(t *testing.T, path string) string` (standard base64 of that file's raw bytes), `decodeCapturedRequestBody(t *testing.T, raw []byte) map[string]any` (finds the first `{` in a fake curl's captured `"$*"` dump and decodes exactly one JSON value from there, ignoring the curl flags/URL text around it).
- Produces (bash, `scripts/openai-as-claude.sh`): `extract_image_blocks`, `build_user_content` — exact bodies below, reused verbatim (only variable names may shift slightly for the second script's context) in Task 2.

This task has no dependency on any other and fully proves the core mechanism in the simpler of the two scripts before Task 2 replicates it into the tool-loop script.

- [ ] **Step 1: Write the failing tests**

Add these imports to `pkg/executor/openai_translator_test.go` (merge into the existing `import` block):

```go
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
```

Add these helpers and tests anywhere after the existing `writeFakeCurl` function (e.g. right before `TestOpenAIAsClaude_OutputParses`):

```go
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
	idx := bytes.IndexByte(raw, '{')
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
	if content != "do the thing\n" {
		t.Errorf("content = %q, want %q", content, "do the thing\n")
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
	if content != prompt {
		t.Errorf("content = %q, want unchanged prompt %q", content, prompt)
	}
}
```

- [ ] **Step 2: Run the tests, verify they fail**

Run: `go test ./pkg/executor/... -run TestOpenAIAsClaude_Screenshot -v` and `go test ./pkg/executor/... -run TestOpenAIAsClaude_NoMarker -v`
Expected: FAIL — `scripts/openai-as-claude.sh` doesn't build multimodal content yet, so `content` stays a plain string in all three cases (the first test's `.([]any)` type assertion fails).

- [ ] **Step 3: Implement**

In `scripts/openai-as-claude.sh`, replace:

```bash
# формируем тело запроса
body=$(jq -nc --arg model "$OPENAI_MODEL" --arg content "$prompt" \
    '{model: $model, stream: true, messages: [{role: "user", content: $content}]}')
```

with:

```bash
# extract_image_blocks <text> -> JSON array of image_url content blocks for every
# readable [Screenshot: <path>] reference in text (empty "[]" if none, or if none
# were readable/recognized). Unreadable/unrecognized paths are skipped with a
# stderr warning, not a hard failure.
extract_image_blocks() {
    local text="$1"
    local blocks='[]'
    local marker path
    while IFS= read -r marker; do
        [[ -z "$marker" ]] && continue
        # marker уже включает скобки — обрезаем "[Screenshot: " и "]" в bash,
        # чтобы обойтись POSIX-совместимым -E (без \K, который есть только в GNU/PCRE
        # grep и отсутствует в BSD grep из macOS — go test запускает этот скрипт
        # напрямую на хосте раннера, не внутри Docker-образа).
        path="${marker#\[Screenshot: }"
        path="${path%\]}"
        if [[ ! -r "$path" ]]; then
            echo "warning: [Screenshot: $path] not readable, skipping" >&2
            continue
        fi
        local mime=""
        case "$path" in
            *.png) mime="image/png" ;;
            *.jpg|*.jpeg) mime="image/jpeg" ;;
            *.webp) mime="image/webp" ;;
            *.gif) mime="image/gif" ;;
            *) echo "warning: [Screenshot: $path] unrecognized image extension, skipping" >&2; continue ;;
        esac
        local b64
        # base64 -w0 — GNU-only флаг (BSD base64 из macOS падает "invalid argument").
        # Портируемый вариант — читать файл через stdin (одинаковый вывод у обеих
        # реализаций) и убрать переводы строк вручную.
        b64=$(base64 <"$path" | tr -d '\n')
        blocks=$(jq -nc --argjson blocks "$blocks" --arg mime "$mime" --arg b64 "$b64" \
            '$blocks + [{type:"image_url", image_url:{url: ("data:" + $mime + ";base64," + $b64)}}]')
    done < <(printf '%s' "$text" | grep -oE '\[Screenshot: [^]]+\]' || true)
    printf '%s' "$blocks"
}

# build_user_content <text> -> plain JSON string if no image was embedded, else a
# [{type:"text",...}, image_url...] array with the [Screenshot: <path>] marker(s)
# stripped from the text portion.
build_user_content() {
    local text="$1"
    local blocks
    blocks=$(extract_image_blocks "$text")
    if [[ "$blocks" == "[]" ]]; then
        jq -nc --arg t "$text" '$t'
        return
    fi
    local cleaned
    cleaned=$(printf '%s' "$text" | sed -E 's/\[Screenshot: [^]]+\]//g')
    jq -nc --arg t "$cleaned" --argjson imgs "$blocks" '[{type:"text", text:$t}] + $imgs'
}

# формируем тело запроса
content=$(build_user_content "$prompt")
body=$(jq -nc --arg model "$OPENAI_MODEL" --argjson content "$content" \
    '{model: $model, stream: true, messages: [{role: "user", content: $content}]}')
```

- [ ] **Step 4: Run the tests, verify they pass**

Run: `go test ./pkg/executor/... -v`
Expected: PASS — all three new tests, plus every pre-existing test in the package (in particular the two original `TestOpenAIAsClaude_*` tests, unaffected).

- [ ] **Step 5: Lint and commit**

```bash
make lint
git add scripts/openai-as-claude.sh pkg/executor/openai_translator_test.go
git commit -m "$(cat <<'EOF'
feat(scripts): openai-as-claude — встраиваем [Screenshot: ...] как настоящую картинку

extract_image_blocks/build_user_content: находит маркер, base64-кодирует
файл, подставляет image_url-блок вместо текста. Без маркера — content
как и раньше, простая строка (без изменений в поведении).
EOF
)"
```

---

### Task 2: Path A wiring in `openai-agent-as-claude.sh` (initial prompt)

**Files:**
- Modify: `scripts/openai-agent-as-claude.sh:45-53`
- Modify: `pkg/executor/openai_agent_translator_test.go` (new tests only — no new imports needed)

**Interfaces:**
- Consumes: `writeTestPNG`, `readTestPNGBase64`, `decodeCapturedRequestBody` from Task 1 (same package `executor`, defined in `openai_translator_test.go`, visible here with no import needed).
- Produces: `extract_image_blocks`/`build_user_content` inside `scripts/openai-agent-as-claude.sh` (same bodies as Task 1's, copied into this script — Task 3 extends the same file's loop body to call `extract_image_blocks` again).

This task depends on Task 1 only for the shared Go test helpers being present in the package; the bash-side functions are copied fresh into this second script (per spec, deliberately not shared as a sourced file).

- [ ] **Step 1: Write the failing tests**

Add these tests to `pkg/executor/openai_agent_translator_test.go` (after the existing tests, no new imports required — `fmt`, `os`, `path/filepath`, `strings` are already imported):

```go
// TestOpenAIAgentAsClaude_ScreenshotInInitialPromptEmbedsImage: a [Screenshot: <path>]
// in the initial prompt (Path A) must produce a multimodal seed user message —
// same mechanism as openai-as-claude.sh, verified here through the tool-loop script.
// Also checks the system prompt was updated to mention the convention.
func TestOpenAIAgentAsClaude_ScreenshotInInitialPromptEmbedsImage(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}

	pngPath := writeTestPNG(t)
	wantB64 := readTestPNGBase64(t, pngPath)

	finalAnswer := `data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"red"},"finish_reason":""}]}
data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}
data: [DONE]
`
	fakeCurlDir, captureDir := writeStatefulFakeCurl(t, []string{finalAnswer}, "200")

	prompt := fmt.Sprintf("what color is [Screenshot: %s]?\n", pngPath)
	out, err := runAgentScript(t, fakeCurlDir, prompt)
	if err != nil {
		t.Fatalf("script failed: %v\noutput:\n%s", err, out)
	}

	raw, readErr := os.ReadFile(filepath.Join(captureDir, "call_0.args"))
	if readErr != nil {
		t.Fatalf("read call_0.args: %v", readErr)
	}
	body := decodeCapturedRequestBody(t, raw)
	messages, _ := body["messages"].([]any)
	if len(messages) < 2 {
		t.Fatalf("expected at least system+user messages, got %d: %v", len(messages), messages)
	}

	sysMsg, _ := messages[0].(map[string]any)
	sysContent, _ := sysMsg["content"].(string)
	if !strings.Contains(sysContent, "Screenshot") {
		t.Errorf("system prompt should mention the [Screenshot: ...] convention, got: %q", sysContent)
	}

	userMsg, _ := messages[1].(map[string]any)
	content, ok := userMsg["content"].([]any)
	if !ok {
		t.Fatalf("expected seed user content to be a multimodal array, got %T: %v", userMsg["content"], userMsg["content"])
	}
	if len(content) != 2 {
		t.Fatalf("expected 2 content blocks, got %d: %v", len(content), content)
	}
	imgBlock, _ := content[1].(map[string]any)
	imageURLField, _ := imgBlock["image_url"].(map[string]any)
	url, _ := imageURLField["url"].(string)
	wantURL := "data:image/png;base64," + wantB64
	if url != wantURL {
		t.Errorf("image_url = %q, want %q", url, wantURL)
	}
}

// TestOpenAIAgentAsClaude_NoMarkerInitialPromptUnchanged: no [Screenshot: ...] in
// the initial prompt keeps the seed user message content a plain string.
func TestOpenAIAgentAsClaude_NoMarkerInitialPromptUnchanged(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}

	finalAnswer := `data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":""}]}
data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}
data: [DONE]
`
	fakeCurlDir, captureDir := writeStatefulFakeCurl(t, []string{finalAnswer}, "200")

	out, err := runAgentScript(t, fakeCurlDir, "do the thing\n")
	if err != nil {
		t.Fatalf("script failed: %v\noutput:\n%s", err, out)
	}

	raw, readErr := os.ReadFile(filepath.Join(captureDir, "call_0.args"))
	if readErr != nil {
		t.Fatalf("read call_0.args: %v", readErr)
	}
	body := decodeCapturedRequestBody(t, raw)
	messages, _ := body["messages"].([]any)
	userMsg, _ := messages[1].(map[string]any)
	content, ok := userMsg["content"].(string)
	if !ok {
		t.Fatalf("expected seed user content to stay a plain string, got %T: %v", userMsg["content"], userMsg["content"])
	}
	if content != "do the thing\n" {
		t.Errorf("content = %q, want %q", content, "do the thing\n")
	}
}
```

- [ ] **Step 2: Run the tests, verify they fail**

Run: `go test ./pkg/executor/... -run TestOpenAIAgentAsClaude_ScreenshotInInitialPrompt -v` and `-run TestOpenAIAgentAsClaude_NoMarkerInitialPrompt -v`
Expected: FAIL — `scripts/openai-agent-as-claude.sh` doesn't build multimodal content yet, and its system prompt doesn't mention "Screenshot".

- [ ] **Step 3: Implement**

In `scripts/openai-agent-as-claude.sh`, replace:

```bash
system_prompt='You have exactly one tool, "bash", which runs a shell command in the current working directory and returns its combined stdout+stderr and exit code. Use it to read and write files, run scripts, and wait for external input (a blocking command is fine to run -- it will return once ready). If the task mentions a skill by name, first read its instructions with bash (e.g. `cat .claude/skills/<name>/SKILL.md` or `~/.claude/skills/<name>/SKILL.md`) before proceeding. When the task is fully complete, respond with your final answer as plain text and do not call any tool.'

tools_json='[{"type":"function","function":{"name":"bash","description":"Execute a shell command in the current working directory and return its combined stdout+stderr and exit code.","parameters":{"type":"object","properties":{"command":{"type":"string","description":"The shell command to run"}},"required":["command"]}}}]'

messages_file=$(mktemp)
trap 'rm -f "$messages_file" "${messages_file}.tmp"' EXIT

jq -nc --arg sys "$system_prompt" --arg user "$prompt" \
    '[{role:"system", content:$sys}, {role:"user", content:$user}]' > "$messages_file"
```

with:

```bash
system_prompt='You have exactly one tool, "bash", which runs a shell command in the current working directory and returns its combined stdout+stderr and exit code. Use it to read and write files, run scripts, and wait for external input (a blocking command is fine to run -- it will return once ready). If the task mentions a skill by name, first read its instructions with bash (e.g. `cat .claude/skills/<name>/SKILL.md` or `~/.claude/skills/<name>/SKILL.md`) before proceeding. If a message mentions [Screenshot: <path>], an image of it is attached directly to that message -- you do not need to read the file yourself. When the task is fully complete, respond with your final answer as plain text and do not call any tool.'

tools_json='[{"type":"function","function":{"name":"bash","description":"Execute a shell command in the current working directory and return its combined stdout+stderr and exit code.","parameters":{"type":"object","properties":{"command":{"type":"string","description":"The shell command to run"}},"required":["command"]}}}]'

# extract_image_blocks <text> -> JSON array of image_url content blocks for every
# readable [Screenshot: <path>] reference in text (empty "[]" if none, or if none
# were readable/recognized). Unreadable/unrecognized paths are skipped with a
# stderr warning, not a hard failure.
extract_image_blocks() {
    local text="$1"
    local blocks='[]'
    local marker path
    while IFS= read -r marker; do
        [[ -z "$marker" ]] && continue
        # marker уже включает скобки — обрезаем "[Screenshot: " и "]" в bash,
        # чтобы обойтись POSIX-совместимым -E (без \K, который есть только в GNU/PCRE
        # grep и отсутствует в BSD grep из macOS — go test запускает этот скрипт
        # напрямую на хосте раннера, не внутри Docker-образа).
        path="${marker#\[Screenshot: }"
        path="${path%\]}"
        if [[ ! -r "$path" ]]; then
            echo "warning: [Screenshot: $path] not readable, skipping" >&2
            continue
        fi
        local mime=""
        case "$path" in
            *.png) mime="image/png" ;;
            *.jpg|*.jpeg) mime="image/jpeg" ;;
            *.webp) mime="image/webp" ;;
            *.gif) mime="image/gif" ;;
            *) echo "warning: [Screenshot: $path] unrecognized image extension, skipping" >&2; continue ;;
        esac
        local b64
        # base64 -w0 — GNU-only флаг (BSD base64 из macOS падает "invalid argument").
        # Портируемый вариант — читать файл через stdin (одинаковый вывод у обеих
        # реализаций) и убрать переводы строк вручную.
        b64=$(base64 <"$path" | tr -d '\n')
        blocks=$(jq -nc --argjson blocks "$blocks" --arg mime "$mime" --arg b64 "$b64" \
            '$blocks + [{type:"image_url", image_url:{url: ("data:" + $mime + ";base64," + $b64)}}]')
    done < <(printf '%s' "$text" | grep -oE '\[Screenshot: [^]]+\]' || true)
    printf '%s' "$blocks"
}

# build_user_content <text> -> plain JSON string if no image was embedded, else a
# [{type:"text",...}, image_url...] array with the [Screenshot: <path>] marker(s)
# stripped from the text portion.
build_user_content() {
    local text="$1"
    local blocks
    blocks=$(extract_image_blocks "$text")
    if [[ "$blocks" == "[]" ]]; then
        jq -nc --arg t "$text" '$t'
        return
    fi
    local cleaned
    cleaned=$(printf '%s' "$text" | sed -E 's/\[Screenshot: [^]]+\]//g')
    jq -nc --arg t "$cleaned" --argjson imgs "$blocks" '[{type:"text", text:$t}] + $imgs'
}

messages_file=$(mktemp)
trap 'rm -f "$messages_file" "${messages_file}.tmp"' EXIT

user_content=$(build_user_content "$prompt")
jq -nc --arg sys "$system_prompt" --argjson user "$user_content" \
    '[{role:"system", content:$sys}, {role:"user", content:$user}]' > "$messages_file"
```

- [ ] **Step 4: Run the tests, verify they pass**

Run: `go test ./pkg/executor/... -v`
Expected: PASS — the two new tests, plus every pre-existing test in the package (in particular `TestOpenAIAgentAsClaude_SingleToolCallThenFinalAnswer`, `_MaxTurnsReached`, `_APIFailureExitsNonZero`, and everything added in Task 1 — none of these should regress).

- [ ] **Step 5: Lint and commit**

```bash
make lint
git add scripts/openai-agent-as-claude.sh pkg/executor/openai_agent_translator_test.go
git commit -m "$(cat <<'EOF'
feat(scripts): openai-agent-as-claude — [Screenshot: ...] в начальном prompt

Тот же механизм, что и в openai-as-claude (Path A) — seed user-сообщение
становится мультимодальным, если найден маркер. Системный промпт учит
модель не читать файл руками, картинка уже приложена к сообщению.
EOF
)"
```

---

### Task 3: Path B wiring in `openai-agent-as-claude.sh` (mid-loop tool output)

**Files:**
- Modify: `scripts/openai-agent-as-claude.sh:142-143` (the tool-message append inside the per-tool-call loop)
- Modify: `pkg/executor/openai_agent_translator_test.go` (new tests only)

**Interfaces:**
- Consumes: `extract_image_blocks` from Task 2 (already present in this script by the time this task runs); `writeTestPNG`, `readTestPNGBase64`, `decodeCapturedRequestBody` from Task 1.
- No new interfaces produced — this is the last piece of the feature.

This task depends on Task 2 (same file, sequential edits to the same script).

- [ ] **Step 1: Write the failing tests**

Add these tests to `pkg/executor/openai_agent_translator_test.go`:

```go
// TestOpenAIAgentAsClaude_ScreenshotInToolOutputInjectsFollowupMessage: when a
// tool call's real output happens to contain [Screenshot: <path>] (e.g. cat'ing
// a dialog answer.json that references a pasted image), the NEXT request must
// carry both the unmodified tool-role message and a separate user-role message
// with the image — Path B.
func TestOpenAIAgentAsClaude_ScreenshotInToolOutputInjectsFollowupMessage(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}

	pngPath := writeTestPNG(t)
	wantB64 := readTestPNGBase64(t, pngPath)

	fixtureDir := t.TempDir()
	answerPath := filepath.Join(fixtureDir, "answer.json")
	answerContent := fmt.Sprintf(`{"id":"q1","answer":"here you go [Screenshot: %s]"}`, pngPath)
	if err := os.WriteFile(answerPath, []byte(answerContent), 0o644); err != nil {
		t.Fatalf("write fixture answer file: %v", err)
	}

	catCommand := fmt.Sprintf("cat %s", answerPath)
	turn1 := fmt.Sprintf(`data: {"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"id":"call_1","function":{"name":"bash","arguments":"{\"command\": \"%s\"}"},"type":"function","index":0}]},"finish_reason":"tool_calls"}]}
data: [DONE]
`, catCommand)
	turn2 := `data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"got it"},"finish_reason":""}]}
data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}
data: [DONE]
`
	fakeCurlDir, captureDir := writeStatefulFakeCurl(t, []string{turn1, turn2}, "200")

	out, err := runAgentScript(t, fakeCurlDir, "check the answer file\n")
	if err != nil {
		t.Fatalf("script failed: %v\noutput:\n%s", err, out)
	}

	raw, readErr := os.ReadFile(filepath.Join(captureDir, "call_1.args"))
	if readErr != nil {
		t.Fatalf("read call_1.args: %v", readErr)
	}
	body := decodeCapturedRequestBody(t, raw)
	messages, _ := body["messages"].([]any)
	// system, user, assistant(tool_calls), tool, user(image) = 5 messages
	if len(messages) != 5 {
		t.Fatalf("expected 5 messages (system,user,assistant,tool,user-image), got %d: %v", len(messages), messages)
	}

	toolMsg, _ := messages[3].(map[string]any)
	if toolMsg["role"] != "tool" {
		t.Fatalf("messages[3].role = %v, want \"tool\"", toolMsg["role"])
	}
	toolContent, _ := toolMsg["content"].(string)
	if !strings.Contains(toolContent, "[Screenshot:") {
		t.Errorf("tool message should keep the raw marker text unmodified, got: %q", toolContent)
	}

	followupMsg, _ := messages[4].(map[string]any)
	if followupMsg["role"] != "user" {
		t.Fatalf("messages[4].role = %v, want \"user\"", followupMsg["role"])
	}
	followupContent, ok := followupMsg["content"].([]any)
	if !ok || len(followupContent) != 2 {
		t.Fatalf("expected followup content to be a 2-block array, got %T: %v", followupMsg["content"], followupMsg["content"])
	}
	imgBlock, _ := followupContent[1].(map[string]any)
	imageURLField, _ := imgBlock["image_url"].(map[string]any)
	url, _ := imageURLField["url"].(string)
	wantURL := "data:image/png;base64," + wantB64
	if url != wantURL {
		t.Errorf("image_url = %q, want %q", url, wantURL)
	}
}

// TestOpenAIAgentAsClaude_NoScreenshotInToolOutputNoFollowup: ordinary tool output
// with no marker must NOT get a spurious followup image message appended.
func TestOpenAIAgentAsClaude_NoScreenshotInToolOutputNoFollowup(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}

	turn1 := `data: {"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"id":"call_1","function":{"name":"bash","arguments":"{\"command\": \"echo hello-from-tool\"}"},"type":"function","index":0}]},"finish_reason":"tool_calls"}]}
data: [DONE]
`
	turn2 := `data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"done"},"finish_reason":""}]}
data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}
data: [DONE]
`
	fakeCurlDir, captureDir := writeStatefulFakeCurl(t, []string{turn1, turn2}, "200")

	out, err := runAgentScript(t, fakeCurlDir, "do the thing\n")
	if err != nil {
		t.Fatalf("script failed: %v\noutput:\n%s", err, out)
	}

	raw, readErr := os.ReadFile(filepath.Join(captureDir, "call_1.args"))
	if readErr != nil {
		t.Fatalf("read call_1.args: %v", readErr)
	}
	body := decodeCapturedRequestBody(t, raw)
	messages, _ := body["messages"].([]any)
	// system, user, assistant(tool_calls), tool = 4 messages — no followup injected
	if len(messages) != 4 {
		t.Fatalf("expected 4 messages (no image followup), got %d: %v", len(messages), messages)
	}
}
```

- [ ] **Step 2: Run the tests, verify they fail**

Run: `go test ./pkg/executor/... -run TestOpenAIAgentAsClaude_ScreenshotInToolOutput -v` and `-run TestOpenAIAgentAsClaude_NoScreenshotInToolOutput -v`
Expected: the "injects followup" test FAILs (only 4 messages present, no image followup yet — the assertion of 5 messages fails); the "no followup" test currently PASSes already (nothing injects a 5th message yet) — that's fine, it becomes the regression guard once Step 3 is implemented.

- [ ] **Step 3: Implement**

In `scripts/openai-agent-as-claude.sh`, replace:

```bash
        tool_msg=$(jq -nc --arg id "$call_id" --arg out "$tool_output" '{role:"tool", tool_call_id:$id, content:$out}')
        jq -c --argjson m "$tool_msg" '. + [$m]' "$messages_file" > "${messages_file}.tmp" && mv "${messages_file}.tmp" "$messages_file"
    done
```

with:

```bash
        tool_msg=$(jq -nc --arg id "$call_id" --arg out "$tool_output" '{role:"tool", tool_call_id:$id, content:$out}')
        jq -c --argjson m "$tool_msg" '. + [$m]' "$messages_file" > "${messages_file}.tmp" && mv "${messages_file}.tmp" "$messages_file"

        # если вывод команды содержит [Screenshot: ...] (например, cat ответа на
        # диалог со вставленным скриншотом) — картинка идёт отдельным user-сообщением
        # сразу за tool-результатом: tool-роль в OpenAI-протоколе не гарантированно
        # поддерживает мультимодальный content, а user-роль — везде.
        img_blocks=$(extract_image_blocks "$tool_output")
        if [[ "$img_blocks" != "[]" ]]; then
            followup_msg=$(jq -nc --argjson imgs "$img_blocks" \
                '{role:"user", content: ([{type:"text", text:"Screenshot referenced in the tool result above:"}] + $imgs)}')
            jq -c --argjson m "$followup_msg" '. + [$m]' "$messages_file" > "${messages_file}.tmp" && mv "${messages_file}.tmp" "$messages_file"
        fi
    done
```

- [ ] **Step 4: Run the tests, verify they pass**

Run: `go test ./pkg/executor/... -v`
Expected: PASS — every test in the package, including both new tests here and everything from Tasks 1 and 2.

- [ ] **Step 5: Lint and commit**

```bash
make lint
git add scripts/openai-agent-as-claude.sh pkg/executor/openai_agent_translator_test.go
git commit -m "$(cat <<'EOF'
feat(scripts): openai-agent-as-claude — [Screenshot: ...] из tool-вывода (Path B)

Модель сама вычитывает диалоговый ответ через bash cat — если в нём
маркер, картинка идёт отдельным user-сообщением сразу за tool-
результатом (не в сам tool-content: мультимодальный tool-content не
гарантированно поддержан всеми OpenAI-совместимыми провайдерами).
EOF
)"
```

---

### Task 4: Document the feature in `CLAUDE.md`

**Files:**
- Modify: `CLAUDE.md:377` (end of the `openai` type subsection, right before its "Требования в образе" line)
- Modify: `CLAUDE.md:446` (end of the `openai-agent` type subsection, right before its "Требования в образе" line — line number shifts by however many lines the `openai` section edit above adds; locate by searching for the exact anchor text, not the raw line number)

**Interfaces:** None (documentation only).

- [ ] **Step 1: Add a one-line cross-reference to the `openai` subsection**

In `CLAUDE.md`, find (inside the `#### Тип \`openai\`` subsection):

```
Поддерживаемые провайдеры: DeepSeek (`api.deepseek.com`), OpenAI, локальные Ollama/любые
эндпоинты с `POST /v1/chat/completions` (в т.ч. SSE-стриминг). **Важно:** Cursor сюда
НЕ относится — см. ниже `type: cursor`; IdeaLab тоже НЕ относится — этому провайдеру
нужен реальный tool-loop, см. ниже `type: openai-agent`.

Требования в образе: `jq`, `curl` (оба присутствуют в `Dockerfile.runtime`).
```

Insert one paragraph between the two:

```
Поддерживает мультимодальные `[Screenshot: <path>]`-вставки из дашборда так
же, как `openai-agent` (см. ниже) — единственный доступный здесь путь
доставки маркера: сам начальный prompt, скрипт не крутит цикл и не читает
диалоговые ответы сам.
```

- [ ] **Step 2: Add the full explanation to the `openai-agent` subsection**

In `CLAUDE.md`, find (inside the `#### Тип \`openai-agent\`` subsection):

```
Известное (не новое) ограничение: если модель зависает на диалоговом поллинге
дольше 30 минут (человек долго не отвечает), сработает тот же `idle_timeout`,
что уже документирован для файлового диалогового протокола выше — это
свойство самого механизма, не специфика этого типа.

Требования в образе: `jq`, `curl` (оба уже есть в `Dockerfile.runtime`).
```

Insert one paragraph between the two:

```
**Мультимодальные скриншоты (`[Screenshot: <path>]`).** Вставленный в дашборде
скриншот (см. "paste a clipboard screenshot" в release notes) доходит до
`openai`/`openai-agent` не как путь-который-надо-прочитать самому, а как
настоящая картинка: адаптер сам находит маркер, base64-кодирует файл и
подставляет `image_url`-блок вместо/вместе с текстом — работает, только если
сконфигурированная модель реально мультимодальна (отдельного `vision:`-флага
в рецепте нет, шим просто всегда пытается встроить найденную картинку). Для
`type: openai-agent` это покрывает оба пути, которыми маркер может дойти до
агента: и начальный prompt (revise/заметка), и текст, который модель сама
вычитывает через `bash cat` ответа на диалоговый вопрос внутри цикла — второй
случай подставляется отдельным user-сообщением сразу за tool-результатом, а
не в сам tool-результат (мультимодальный `tool`-content не гарантированно
поддержан всеми провайдерами).
```

- [ ] **Step 3: Verify placement**

Run: `grep -n 'Мультимодальные скриншоты\|поддерживает мультимодальные' CLAUDE.md`
Expected: two hits, one inside each of the `openai`/`openai-agent` subsections (confirm by eye that each sits between its subsection's own content and its own "Требования в образе" line, not bleeding into the neighboring subsection).

- [ ] **Step 4: Commit**

```bash
git add CLAUDE.md
git commit -m "$(cat <<'EOF'
docs: описываем мультимодальную поддержку [Screenshot: ...] в CLAUDE.md

Оба пути доставки маркера для openai-agent, единственный (начальный
prompt) для openai — рядом с уже описанным поведением обоих типов.
EOF
)"
```

---

### Task 5: Manual end-to-end verification through the real dashboard UI

**Files (outside this repo, not committed here):**
- Temporary edit to `~/.afm/config.yaml` (or a scratch project's `.afm/config.yaml`) pointing an `openai-agent` recipe at a confirmed vision-capable, tool-calling-capable model — `qwen3-vl-plus` on IdeaLab was validated live during brainstorming (both Path A and Path B) for exactly this combination; reuse it unless it's no longer accessible.

This task has no automated test — it is the real-world proof that Tasks 1–4 actually deliver a picture to the model through the full stack: dashboard paste UI → `POST /attachments` → `[Screenshot: ...]` marker → revise/note → adapter script → live vision model → visible effect. Do this after Tasks 1–4 are merged, with a freshly built image and a freshly built host binary (the host binary validates config before the Docker re-exec, same gotcha as the tool-loop plan's Task 7).

- [ ] **Step 1: Rebuild the image and the host binary**

```bash
cd /Users/alexander.kopichin/work/personal/afm
make docker-build
go build -o ~/go/bin/afm-dev ./cmd/afm
```

- [ ] **Step 2: Point a recipe at a vision-capable model**

Confirm (or set) an `openai-agent` recipe using a model already verified live to support both vision and function calling — `qwen3-vl-plus` via IdeaLab:

```yaml
    idealab-vision:
      type: openai-agent
      model: qwen3-vl-plus
      url: https://idealab.alibaba-inc.com/api/openai/v1
      auth:
        from: "file:~/.ai-free/claude-glm/token-idealab"
        to: "env:OPENAI_API_KEY"
```

(A separate recipe name, not overwriting the existing `idealab` entry — keeps the text-only default undisturbed for other flows.)

- [ ] **Step 3: Run a minimal flow with a stage using this recipe**

A single non-interactive `agents: [auto]` stage, `command: idealab-vision`, whose prompt asks it to wait for a revision/note and then describe whatever image it's given — run it with `AFM_DOCKER_IMAGE=akopichin/afm:latest ~/go/bin/afm-dev run flow.yaml`, open the printed dashboard URL in a real browser tab (Chrome DevTools MCP tools).

- [ ] **Step 4: Paste a real screenshot through the actual dashboard UI and drive a revise**

Using the Chrome DevTools MCP tools against the open dashboard tab: upload a real test image through the same endpoint the UI's paste handler calls (`fetch('/api/stages/<id>/attachments', {method:'POST', headers:{'Content-Type':'image/png'}, body: <real PNG bytes>})`, executed via `evaluate_script` in-page — functionally identical to what a real clipboard paste does), take the returned path, and type `[Screenshot: <path>] what do you see in this image?` into the stage's note/comment textarea, then submit (agent-suggest note or a plan-revision comment, whichever the stage's current status exposes).

Expected: the stage's live log (dashboard event feed, or `<runDir>/<stageID>/autonomous.log`) shows the agent's own `bash` tool being invoked as usual, and its final answer correctly describes the actual pasted image's content (not a generic "I can't see images" or a description of the literal file path text) — proof the whole pipeline, not just the adapter script in isolation, delivers a real picture to a real model.

- [ ] **Step 5: Report back, no commit**

Nothing in this repo changes from this task. If the model's answer doesn't reflect the actual image, capture the stage's `.log`/`.stderr.log` and the exact request the adapter sent (temporarily add a `set -x` or an extra stderr dump of `$request_body` if needed to debug) before concluding the feature doesn't work — don't conclude success or failure from the dashboard UI alone without checking what was actually sent.
