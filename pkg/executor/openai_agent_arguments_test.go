package executor

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// toolCallSSE is a one-chunk streamed response calling the bash tool with the given raw arguments string.
func toolCallSSE(t *testing.T, arguments string) string {
	t.Helper()
	chunk := map[string]any{"choices": []any{map[string]any{
		"index": 0,
		"delta": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
			"index": 0, "id": "call_1", "type": "function",
			"function": map[string]any{"name": "bash", "arguments": arguments},
		}}},
		"finish_reason": "tool_calls",
	}}}
	raw, err := json.Marshal(chunk)
	if err != nil {
		t.Fatal(err)
	}
	return "data: " + string(raw) + "\ndata: [DONE]\n"
}

const finalTextSSE = `data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"done"},"finish_reason":""}]}
data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}
data: [DONE]
`

func requireAgentShimTools(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not available")
		}
	}
}

func TestOpenAIAgentAsClaude_InvalidToolArgumentsAreReportedNotExecuted(t *testing.T) {
	requireAgentShimTools(t)

	sideEffect := filepath.Join(t.TempDir(), "ran")
	cases := map[string]string{
		"not JSON":              `{"command": "touch ` + sideEffect + `"`,
		"empty arguments":       ``,
		"JSON array":            `["touch ` + sideEffect + `"]`,
		"JSON string":           `"touch ` + sideEffect + `"`,
		"missing command":       `{"cmd": "touch ` + sideEffect + `"}`,
		"command is a number":   `{"command": 5}`,
		"command is empty":      `{"command": ""}`,
		"command is not string": `{"command": ["touch", "` + sideEffect + `"]}`,
	}
	for name, arguments := range cases {
		t.Run(name, func(t *testing.T) {
			fakeCurlDir, captureDir := writeStatefulFakeCurl(t, []string{toolCallSSE(t, arguments), finalTextSSE}, "200")

			out, err := runAgentScript(t, fakeCurlDir, "do the thing\n")
			if err != nil {
				t.Fatalf("script failed: %v\noutput:\n%s", err, out)
			}

			if _, statErr := os.Stat(sideEffect); statErr == nil {
				t.Fatal("the command ran although its arguments were invalid")
			}
			if strings.Contains(out, `"type":"tool_use"`) {
				t.Errorf("an empty or invalid Bash tool_use was emitted:\n%s", out)
			}

			raw, readErr := os.ReadFile(filepath.Join(captureDir, "call_1.args"))
			if readErr != nil {
				t.Fatalf("read call_1.args: %v", readErr)
			}
			messages, _ := decodeCapturedRequestBody(t, raw)["messages"].([]any)
			toolMsg, _ := messages[len(messages)-1].(map[string]any)
			content, _ := toolMsg["content"].(string)
			if toolMsg["role"] != "tool" || toolMsg["tool_call_id"] != "call_1" {
				t.Fatalf("last message is not the tool answer for call_1: %v", toolMsg)
			}
			if !strings.HasPrefix(content, "error: invalid arguments JSON: ") || !strings.HasSuffix(content, `expected {"command": string}`) {
				t.Errorf("tool message = %q", content)
			}
		})
	}
}

func TestOpenAIAgentAsClaude_ValidToolArgumentsStillRun(t *testing.T) {
	requireAgentShimTools(t)

	fakeCurlDir, captureDir := writeStatefulFakeCurl(t, []string{toolCallSSE(t, `{"command": "echo ran-fine"}`), finalTextSSE}, "200")

	out, err := runAgentScript(t, fakeCurlDir, "do the thing\n")
	if err != nil {
		t.Fatalf("script failed: %v\noutput:\n%s", err, out)
	}
	raw, readErr := os.ReadFile(filepath.Join(captureDir, "call_1.args"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(raw), "ran-fine") || !strings.Contains(out, `"type":"tool_use"`) {
		t.Errorf("valid call did not run:\nrequest: %s\noutput: %s", raw, out)
	}
}
