package executor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// agentScriptPath returns the absolute path to scripts/openai-agent-as-claude.sh.
func agentScriptPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Join(filepath.Dir(file), "..", "..")
	p := filepath.Join(root, "scripts", "openai-agent-as-claude.sh")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("script not found: %s: %v", p, err)
	}
	return p
}

// writeStatefulFakeCurl creates a fake curl on PATH that returns responses[n]
// (clamped to the last entry once exhausted) on its (n+1)-th invocation —
// the real script calls curl once per loop turn, so this lets a test control
// what each successive turn sees. Each invocation's full argument list is
// captured to <captureDir>/call_<n>.args so a test can inspect exactly what
// request body a given turn sent (proving real tool output made it back into
// history, not just that the script printed something plausible). Each
// response body must be complete SSE text (including "data: [DONE]"); this
// helper appends the "\n<httpCode>" suffix mirroring the script's own
// `curl -w '\n%{http_code}'` usage.
func writeStatefulFakeCurl(t *testing.T, responses []string, httpCode string) (fakeCurlDir, captureDir string) {
	t.Helper()
	fakeCurlDir = t.TempDir()
	respDir := t.TempDir()
	captureDir = t.TempDir()
	for i, r := range responses {
		if err := os.WriteFile(filepath.Join(respDir, fmt.Sprintf("%d", i)), []byte(r), 0o644); err != nil {
			t.Fatalf("write response %d: %v", i, err)
		}
	}
	counterFile := filepath.Join(fakeCurlDir, "counter")
	if err := os.WriteFile(counterFile, []byte("0"), 0o644); err != nil {
		t.Fatalf("write counter: %v", err)
	}
	script := fmt.Sprintf(`#!/usr/bin/env bash
n=$(cat %q)
printf '%%s' "$*" > %q/"call_$n.args"
idx=$n
max=%d
if [ "$idx" -gt "$max" ]; then idx=$max; fi
cat %q/"$idx"
printf '\n%s'
echo $((n + 1)) > %q
`, counterFile, captureDir, len(responses)-1, respDir, httpCode, counterFile)
	curlPath := filepath.Join(fakeCurlDir, "curl")
	if err := os.WriteFile(curlPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake curl: %v", err)
	}
	return fakeCurlDir, captureDir
}

func runAgentScript(t *testing.T, fakeCurlDir, prompt string, extraEnv ...string) (stdout string, err error) {
	t.Helper()
	cmd := exec.Command("bash", agentScriptPath(t))
	env := append(os.Environ(),
		"PATH="+fakeCurlDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"OPENAI_API_KEY=test",
		"OPENAI_BASE_URL=http://fake/v1",
		"OPENAI_MODEL=m",
	)
	cmd.Env = append(env, extraEnv...)
	cmd.Stdin = strings.NewReader(prompt)
	out, runErr := cmd.CombinedOutput()
	return string(out), runErr
}

func countCaptures(t *testing.T, captureDir string) int {
	t.Helper()
	entries, err := os.ReadDir(captureDir)
	if err != nil {
		t.Fatalf("read capture dir: %v", err)
	}
	return len(entries)
}

// TestOpenAIAgentAsClaude_SingleToolCallThenFinalAnswer запускает РЕАЛЬНЫЙ
// scripts/openai-agent-as-claude.sh с поддельным curl (без сети). Первый
// ответ — стримингом (по чанкам, как настоящий IdeaLab) переданный tool_call
// для bash("echo hello-from-tool"), второй — финальный текст без tool_calls.
// Проверяет: скрипт реально выполняет команду (не просто печатает текст),
// передаёт её вывод обратно в историю следующего запроса, эмитит live
// tool_use конверт формы, которую понимает ParseToolAction/дашборд, и
// завершается success-конвертом.
func TestOpenAIAgentAsClaude_SingleToolCallThenFinalAnswer(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}

	turn1 := `data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"","tool_calls":[{"id":"call_1","function":{"name":"bash","arguments":""},"type":"function","index":0}]},"finish_reason":""}]}
data: {"choices":[{"index":0,"delta":{"tool_calls":[{"id":"","function":{"arguments":"{\"command\": \"echo hello-from-tool\"}"},"type":"function","index":0}]},"finish_reason":""}]}
data: {"choices":[{"index":0,"delta":{"tool_calls":[{"id":"","function":{"arguments":""},"type":"function","index":0}]},"finish_reason":"tool_calls"}]}
data: [DONE]
`
	turn2 := `data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"All done"},"finish_reason":""}]}
data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}
data: [DONE]
`
	fakeCurlDir, captureDir := writeStatefulFakeCurl(t, []string{turn1, turn2}, "200")

	out, err := runAgentScript(t, fakeCurlDir, "do the thing\n")
	if err != nil {
		t.Fatalf("script failed: %v\noutput:\n%s", err, out)
	}

	if got := countCaptures(t, captureDir); got != 2 {
		t.Fatalf("expected exactly 2 curl invocations, got %d", got)
	}

	call1, readErr := os.ReadFile(filepath.Join(captureDir, "call_1.args"))
	if readErr != nil {
		t.Fatalf("read call_1.args: %v", readErr)
	}
	if !strings.Contains(string(call1), "hello-from-tool") {
		t.Errorf("second request did not include real tool output:\n%s", call1)
	}
	if !strings.Contains(string(call1), `"tool_call_id":"call_1"`) {
		t.Errorf("second request missing tool_call_id linking back to call_1:\n%s", call1)
	}
	if !strings.Contains(string(call1), `"role":"tool"`) {
		t.Errorf("second request missing role:tool message:\n%s", call1)
	}

	lines := strings.Split(strings.TrimSpace(out), "\n")
	var toolUseLine, textLine, resultLine string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if strings.Contains(l, `"type":"tool_use"`) && toolUseLine == "" {
			toolUseLine = l
		}
		if strings.Contains(l, `"type":"text"`) && textLine == "" {
			textLine = l
		}
		if strings.Contains(l, `"type":"result"`) && resultLine == "" {
			resultLine = l
		}
	}
	if toolUseLine == "" {
		t.Fatalf("no tool_use envelope in output:\n%s", out)
	}
	toolName, detail, ok := ParseToolAction(toolUseLine, 0)
	if !ok || toolName != "Bash" || detail != "echo hello-from-tool" {
		t.Errorf("ParseToolAction(toolUseLine) = (%q, %q, %v), want (\"Bash\", \"echo hello-from-tool\", true)", toolName, detail, ok)
	}
	if textLine == "" {
		t.Fatalf("no final text envelope in output:\n%s", out)
	}
	ev, ok := parseStreamEvent(textLine)
	if !ok || len(ev.Message.Content) == 0 || ev.Message.Content[0].Text != "All done" {
		t.Errorf("final envelope wrong: %s", textLine)
	}
	if resultLine == "" || !strings.Contains(resultLine, `"subtype":"success"`) {
		t.Errorf("missing/bad result line:\n%s", out)
	}
}

// TestOpenAIAgentAsClaude_MaxTurnsReached: поддельный curl всегда возвращает
// новый tool_call и никогда не завершает работу — проверяет, что скрипт
// останавливается ровно после OPENAI_AGENT_MAX_TURNS обращений (не крутится
// бесконечно), и что это не считается ошибкой скрипта (exit 0, а не 1) —
// afm's существующий retry для незавершённой autonomous-стадии уже это покрывает.
func TestOpenAIAgentAsClaude_MaxTurnsReached(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}

	alwaysToolCall := `data: {"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"id":"call_x","function":{"name":"bash","arguments":"{\"command\": \"true\"}"},"type":"function","index":0}]},"finish_reason":"tool_calls"}]}
data: [DONE]
`
	fakeCurlDir, captureDir := writeStatefulFakeCurl(t, []string{alwaysToolCall}, "200")

	out, err := runAgentScript(t, fakeCurlDir, "loop forever\n", "OPENAI_AGENT_MAX_TURNS=2")
	if err != nil {
		t.Fatalf("script should exit 0 on max-turns cap, got error: %v\noutput:\n%s", err, out)
	}

	if got := countCaptures(t, captureDir); got != 2 {
		t.Fatalf("expected exactly OPENAI_AGENT_MAX_TURNS=2 curl invocations, got %d", got)
	}
	if !strings.Contains(out, "max turns reached") {
		t.Errorf("expected max-turns note in output, got:\n%s", out)
	}
	if !strings.Contains(out, `"subtype":"success"`) {
		t.Errorf("expected a success result line even after hitting max turns, got:\n%s", out)
	}
}

// TestOpenAIAgentAsClaude_APIFailureExitsNonZero: поддельный curl возвращает
// HTTP 500 на первом же обращении — скрипт обязан завершиться с ошибкой (в
// отличие от openai-as-claude.sh, который проглатывает сбой curl в пустой
// success: здесь "тихий успех" означал бы, что afm принимает пустой/
// незавершённый tool-loop за штатный незавершённый прогон вместо немедленно
// видимого сбоя).
func TestOpenAIAgentAsClaude_APIFailureExitsNonZero(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}

	fakeCurlDir, _ := writeStatefulFakeCurl(t, []string{`data: {"error":"server error"}` + "\n"}, "500")

	out, err := runAgentScript(t, fakeCurlDir, "x\n")
	if err == nil {
		t.Fatalf("expected script to exit non-zero on HTTP 500, got success. output:\n%s", out)
	}
}

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
	if content != "do the thing" {
		t.Errorf("content = %q, want %q", content, "do the thing")
	}
}

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
