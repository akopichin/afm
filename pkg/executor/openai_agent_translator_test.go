package executor

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
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

// writeStatefulFakeCurl serves successive SSE responses and captures each request body; the final response
// repeats.
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
%s
n=$(cat %q)
capture_body "$@" > %q/"call_$n.args"
idx=$n
max=%d
if [ "$idx" -gt "$max" ]; then idx=$max; fi
cat %q/"$idx"
printf '\n%s'
echo $((n + 1)) > %q
`, captureCurlBody, counterFile, captureDir, len(responses)-1, respDir, httpCode, counterFile)
	curlPath := filepath.Join(fakeCurlDir, "curl")
	if err := os.WriteFile(curlPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake curl: %v", err)
	}
	return fakeCurlDir, captureDir
}

func runAgentScript(t *testing.T, fakeCurlDir, prompt string, extraEnv ...string) (stdout string, err error) {
	t.Helper()
	cmd := exec.Command("bash", agentScriptPath(t))
	// The shim's control variables come from the test, never from the environment go test inherited.
	env := append(os.Environ(),
		"AFM_OUTPUT_SCHEMA_FILE=",
		"AFM_WAIT_ANSWER_TIMEOUT=",
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

const agentExt = StreamExtensionEnv + "=1"

func sseChunk(delta string) string {
	return `data: {"choices":[{"index":0,"delta":` + delta + `,"finish_reason":""}]}` + "\n"
}

func contentChunk(text string) string { return sseChunk(fmt.Sprintf(`{"content":%q}`, text)) }

func toolCallRound(id, command string) string {
	args := fmt.Sprintf("%q", fmt.Sprintf(`{"command": %q}`, command))
	return sseChunk(fmt.Sprintf(`{"role":"assistant","tool_calls":[{"id":%q,"type":"function","index":0,"function":{"name":"bash","arguments":""}}]}`, id)) +
		sseChunk(fmt.Sprintf(`{"tool_calls":[{"id":"","index":0,"function":{"arguments":%s}}]}`, args)) +
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\ndata: [DONE]\n"
}

func finalRound(text string) string {
	return contentChunk(text) + `data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\ndata: [DONE]\n"
}

// decodeLines decodes every JSON object line of a wrapper's output.
func decodeLines(out string) []map[string]any {
	var lines []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) == nil {
			lines = append(lines, m)
		}
	}
	return lines
}

func lineOfType(lines []map[string]any, typ string) (out []map[string]any) {
	for _, l := range lines {
		if l["type"] == typ {
			out = append(out, l)
		}
	}
	return out
}

func afmField(line map[string]any) map[string]any {
	a, _ := line["afm"].(map[string]any)
	return a
}

func TestOpenAIAgentAsClaude_StreamExtension_ToolLifecycleAndFinal(t *testing.T) {
	requireAgentShimTools(t)
	fakeCurlDir, _ := writeStatefulFakeCurl(t, []string{
		contentChunk("Checking the repo.") + toolCallRound("call_1", "echo hi; exit 3"),
		toolCallRound("call_1", "echo again"), // a provider that reuses an id in the next round
		finalRound("All done"),
	}, "200")
	out, err := runAgentScript(t, fakeCurlDir, "go\n", agentExt)
	if err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}
	lines := decodeLines(out)
	if first := lines[0]; first["type"] != "system" || first["subtype"] != "afm_stream" || first["version"] != float64(1) {
		t.Fatalf("first line = %v, want the announcement", first)
	}
	var starts, results []string
	for _, l := range lines {
		if b := contentBlock(l); b != nil && b["type"] == "tool_use" {
			starts = append(starts, b["id"].(string))
		} else if b != nil && b["type"] == "tool_result" {
			results = append(results, b["tool_use_id"].(string))
			if b["tool_use_id"] == starts[0] {
				hint := afmField(l)
				if !isErrorResult(b) || hint["exit_code"] != float64(3) || hint["tool"] != "Bash" || hint["detail"] != "echo hi; exit 3" {
					t.Errorf("first tool finish = %v %v", b, hint)
				}
			}
		}
	}
	if len(starts) != 2 || starts[0] == starts[1] || fmt.Sprint(starts) != fmt.Sprint(results) {
		t.Errorf("tool ids: starts %v results %v, want two distinct ids finished in order", starts, results)
	}
	// Round order: note, start, finish, start, finish, answer, afm_final, result.
	types := lineTypes(lines)
	if types[len(types)-2] != "afm_final" || types[len(types)-1] != "result" {
		t.Fatalf("line types = %v, want afm_final then result at the end", types)
	}
	finals := finalLines(lines)
	if len(finals) != 1 || finals[0]["text"] != "All done" || finals[0]["item_id"] == nil {
		t.Fatalf("afm_final = %v", finals)
	}
	var answerMsg map[string]any
	for _, l := range lineOfType(lines, "assistant") {
		if b := contentBlock(l); b["type"] == "text" && b["text"] == "All done" {
			answerMsg = l
		}
	}
	if answerMsg == nil || afmField(answerMsg)["item_id"] != finals[0]["item_id"] || !afmComplete(answerMsg) {
		t.Errorf("the answer message %v must be the item afm_final names", answerMsg)
	}
}

func TestOpenAIAgentAsClaude_StreamExtension_TextAndToolStartAreVisibleWhileTheToolRuns(t *testing.T) {
	requireAgentShimTools(t)
	dir := t.TempDir()
	toolGate := filepath.Join(dir, "tool-gate")
	// The tool itself waits for the test, so the start can only be seen while the tool is running.
	round1 := contentChunk(strings.Repeat("thinking out loud ", 8)) + toolCallRound("c1", "until [ -e "+toolGate+" ]; do sleep 0.05; done")
	fakeCurlDir, _ := writeStatefulFakeCurl(t, []string{round1, finalRound("ok")}, "200")
	cmd := exec.Command("bash", agentScriptPath(t))
	cmd.Env = append(os.Environ(), "AFM_OUTPUT_SCHEMA_FILE=", "PATH="+fakeCurlDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"OPENAI_API_KEY=test", "OPENAI_BASE_URL=http://fake/v1", "OPENAI_MODEL=m", agentExt)
	cmd.Stdin = strings.NewReader("go\n")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	release := func() { _ = os.WriteFile(toolGate, nil, 0o644) }
	defer func() { release(); _ = cmd.Wait() }()

	seen := make(chan string, 64)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			seen <- sc.Text()
		}
		close(seen)
	}()
	var text, start bool
	deadline := time.After(10 * time.Second) // only a failure guard: the gate, not time, decides
	for !text || !start {
		select {
		case l, ok := <-seen:
			if !ok {
				t.Fatal("output ended while the tool was still blocked")
			}
			if strings.Contains(l, `"tool_result"`) {
				t.Fatalf("tool_finished arrived while the tool was blocked: %s", l)
			}
			text = text || strings.Contains(l, "thinking out loud")
			start = start || strings.Contains(l, `"tool_use"`)
		case <-deadline:
			t.Fatalf("text seen %v, tool start seen %v while the tool ran: the wrapper holds output until the tool ends", text, start)
		}
	}
	release()
	for finished := false; !finished; {
		select {
		case l, ok := <-seen:
			if !ok {
				t.Fatal("output ended without tool_finished after the tool was released")
			}
			finished = strings.Contains(l, `"tool_result"`)
		case <-deadline:
			t.Fatal("no tool_finished after the tool was released")
		}
	}
}

func TestOpenAIAgentAsClaude_StreamExtension_ManySmallChunksStayBounded(t *testing.T) {
	requireAgentShimTools(t)
	const chunks = 2000
	var sb strings.Builder
	for range chunks {
		sb.WriteString(contentChunk("ab"))
	}
	sb.WriteString(`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\ndata: [DONE]\n")
	fakeCurlDir, _ := writeStatefulFakeCurl(t, []string{sb.String()}, "200")
	out, err := runAgentScript(t, fakeCurlDir, "go\n", agentExt)
	if err != nil {
		t.Fatalf("script failed: %v", err)
	}
	if n := len(strings.Split(strings.TrimSpace(out), "\n")); n > 40 {
		t.Errorf("%d output lines for %d chunks, want a bounded number", n, chunks)
	}
	if len(out) > 64<<10 {
		t.Errorf("%d bytes of output for %d bytes of text, want roughly linear", len(out), chunks*2)
	}
	if finals := finalLines(decodeLines(out)); len(finals) != 1 || len(finals[0]["text"].(string)) != chunks*2 {
		t.Errorf("afm_final = %d finals, want the whole text", len(finals))
	}
}

func TestOpenAIAgentAsClaude_StreamExtension_FailureHasNoFinal(t *testing.T) {
	requireAgentShimTools(t)
	fakeCurlDir, _ := writeStatefulFakeCurl(t, []string{contentChunk("partial") + `{"error":{"message":"boom"}}` + "\n"}, "500")
	out, err := runAgentScript(t, fakeCurlDir, "go\n", agentExt)
	if err == nil {
		t.Fatalf("a failed request must exit non-zero:\n%s", out)
	}
	lines := decodeLines(out)
	if finals := finalLines(lines); len(finals) != 0 {
		t.Errorf("afm_final after a failed request: %v", finals)
	}
	if res := lineOfType(lines, "result"); len(res) != 1 || res[0]["subtype"] != "error_during_execution" || !isErrorResult(res[0]) {
		t.Errorf("result = %v", res)
	}
}

func TestOpenAIAgentAsClaude_StreamExtension_SchemaCorrectionKeepsOnlyTheAcceptedAnswerFinal(t *testing.T) {
	requireAgentShimTools(t)
	afmDir, _ := writeFakeAfm(t, outputInvalidStderr, 1)
	fakeCurlDir, _ := writeStatefulFakeCurl(t, []string{textSSE(t, "pass, all good"), textSSE(t, "VALID verdict")}, "200")
	out, err := runAgentScript(t, fakeCurlDir, "verify it\n", append(schemaEnv(fakeCurlDir, afmDir), agentExt)...)
	if err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}
	if finals := finalLines(decodeLines(out)); len(finals) != 1 || finals[0]["text"] != "VALID verdict" {
		t.Fatalf("afm_final = %v, want only the corrected answer", finals)
	}
}

// Usage is summed once whether or not the stream is observed live.
func TestOpenAIAgentAsClaude_StreamExtension_UsageMatchesBufferedRun(t *testing.T) {
	requireAgentShimTools(t)
	usage := `data: {"model":"m-1","choices":[],"usage":{"prompt_tokens":7,"completion_tokens":3}}` + "\n"
	rounds := []string{toolCallRound("c1", "true") + usage, finalRound("fin") + usage}
	resultOf := func(extra ...string) string {
		fakeCurlDir, _ := writeStatefulFakeCurl(t, rounds, "200")
		out, err := runAgentScript(t, fakeCurlDir, "go\n", extra...)
		if err != nil {
			t.Fatalf("script failed: %v\n%s", err, out)
		}
		return extractResultLine(t, out)
	}
	if plain, ext := resultOf(), resultOf(agentExt); plain != ext {
		t.Errorf("result differs:\n plain: %s\n ext:   %s", plain, ext)
	}
}

// Without the flag the wrapper speaks the old protocol: no announcement, ids, afm fields or afm_final.
func TestOpenAIAgentAsClaude_WithoutExtensionUsesTheOldProtocol(t *testing.T) {
	requireAgentShimTools(t)
	fakeCurlDir, _ := writeStatefulFakeCurl(t, []string{toolCallRound("c1", "true"), finalRound("fin")}, "200")
	out, err := runAgentScript(t, fakeCurlDir, "go\n", StreamExtensionEnv+"=0")
	if err != nil {
		t.Fatalf("script failed: %v", err)
	}
	for _, banned := range []string{"afm_stream", "afm_final", `"afm"`, `"tool_result"`, `"id"`} {
		if strings.Contains(out, banned) {
			t.Errorf("old-protocol output contains %s:\n%s", banned, out)
		}
	}
}
