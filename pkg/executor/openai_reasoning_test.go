package executor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func skipIfNoShellTools(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}
}

func TestOpenAIAsClaude_ReasoningEffortForwarded(t *testing.T) {
	skipIfNoShellTools(t)
	fakeCurlDir, captureFile := writeFakeCurlCapturing(t, `printf 'data: {"choices":[{"delta":{"content":"ok"}}]}\ndata: [DONE]\n'; printf '200'`)
	cmd := exec.Command("bash", scriptPath(t))
	cmd.Env = append(os.Environ(),
		"PATH="+fakeCurlDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"OPENAI_API_KEY=test", "OPENAI_BASE_URL=http://fake/v1", "OPENAI_MODEL=m",
		"OPENAI_REASONING_EFFORT=medium",
	)
	cmd.Stdin = strings.NewReader("hi\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(captureFile)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	body := decodeCapturedRequestBody(t, raw)
	if body["reasoning_effort"] != "medium" {
		t.Errorf("reasoning_effort = %v, want medium\nbody: %v", body["reasoning_effort"], body)
	}
}

func TestOpenAIAsClaude_ReasoningEffortUnsetOmitted(t *testing.T) {
	skipIfNoShellTools(t)
	fakeCurlDir, captureFile := writeFakeCurlCapturing(t, `printf 'data: {"choices":[{"delta":{"content":"ok"}}]}\ndata: [DONE]\n'; printf '200'`)
	cmd := exec.Command("bash", scriptPath(t))
	cmd.Env = append(os.Environ(),
		"PATH="+fakeCurlDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"OPENAI_API_KEY=test", "OPENAI_BASE_URL=http://fake/v1", "OPENAI_MODEL=m",
	)
	cmd.Stdin = strings.NewReader("hi\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(captureFile)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	body := decodeCapturedRequestBody(t, raw)
	if _, ok := body["reasoning_effort"]; ok {
		t.Errorf("unset reasoning must omit reasoning_effort\nbody: %v", body)
	}
}

// writeCapturingAgentCurl writes a fake curl for the openai-agent adapter: captures the first request body
// and returns a single-turn "stop" response with the trailing http_code the agent script parses.
func writeCapturingAgentCurl(t *testing.T) (dir, captureFile string) {
	t.Helper()
	dir = t.TempDir()
	captureFile = filepath.Join(dir, "captured.args")
	sse := `data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"done"},"finish_reason":""}]}
data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}
data: [DONE]`
	content := fmt.Sprintf("#!/usr/bin/env bash\n%scapture_body \"$@\" > %q\nprintf '%%s\\n200' %q\n",
		captureCurlBody, captureFile, sse)
	if err := os.WriteFile(filepath.Join(dir, "curl"), []byte(content), 0o755); err != nil {
		t.Fatalf("write fake curl: %v", err)
	}
	return dir, captureFile
}

func TestOpenAIAgentAsClaude_ReasoningEffortForwarded(t *testing.T) {
	skipIfNoShellTools(t)
	dir, captureFile := writeCapturingAgentCurl(t)
	out, err := runAgentScript(t, dir, "hi\n", "OPENAI_REASONING_EFFORT=high")
	if err != nil {
		t.Fatalf("agent script failed: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(captureFile)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	body := decodeCapturedRequestBody(t, raw)
	if body["reasoning_effort"] != "high" {
		t.Errorf("reasoning_effort = %v, want high\nbody: %v", body["reasoning_effort"], body)
	}
}

func TestOpenAIAgentAsClaude_ReasoningEffortUnsetOmitted(t *testing.T) {
	skipIfNoShellTools(t)
	dir, captureFile := writeCapturingAgentCurl(t)
	out, err := runAgentScript(t, dir, "hi\n")
	if err != nil {
		t.Fatalf("agent script failed: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(captureFile)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	body := decodeCapturedRequestBody(t, raw)
	if _, ok := body["reasoning_effort"]; ok {
		t.Errorf("unset reasoning must omit reasoning_effort\nbody: %v", body)
	}
}
