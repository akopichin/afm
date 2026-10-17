package executor

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestOpenAIAsClaude_CurlFailureExitsNonZero: if curl itself fails to execute
// the request (non-zero exit code, e.g. a network error), the script must not
// report a silent success — it must exit non-zero and name the curl exit code
// in stderr so afm can classify the failure correctly.
func TestOpenAIAsClaude_CurlFailureExitsNonZero(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}

	fakeCurlDir := writeFakeCurl(t, `exit 7`)

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
	err := cmd.Run()

	if err == nil {
		t.Fatalf("expected non-zero exit on curl failure, got success; stderr:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "curl exit 7") {
		t.Errorf("expected stderr to mention \"curl exit 7\", got:\n%s", stderr.String())
	}
}

// TestOpenAIAsClaude_HTTP503ExitsNonZero: curl succeeds (as a process) but the
// upstream returns a non-2xx status. The script must not treat this as
// success — it must exit non-zero and name the HTTP status in stderr.
func TestOpenAIAsClaude_HTTP503ExitsNonZero(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}

	fakeCurlDir := writeFakeCurl(t, `printf '{"error":"service unavailable"}\n503\n'`)

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
	err := cmd.Run()

	if err == nil {
		t.Fatalf("expected non-zero exit on HTTP 503, got success; stderr:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "503") {
		t.Errorf("expected stderr to mention \"503\", got:\n%s", stderr.String())
	}
}

// TestOpenAIAsClaude_SuccessStillEmitsEnvelope: regression guard — a normal
// successful SSE response must still produce the assistant + result:success
// envelope with exit code 0, same as before the failure-handling fix.
func TestOpenAIAsClaude_SuccessStillEmitsEnvelope(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}

	fakeCurlDir := writeFakeCurl(t, `printf 'data: {"choices":[{"delta":{"content":"Hello "}}]}\ndata: {"choices":[{"delta":{"content":"world"}}]}\ndata: [DONE]\n200\n'`)

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

	if !strings.Contains(string(out), `"type":"assistant"`) {
		t.Errorf("expected assistant envelope on success, got:\n%s", out)
	}
	if !strings.Contains(string(out), `"subtype":"success"`) {
		t.Errorf("expected result success line on success, got:\n%s", out)
	}

	var assistantLine string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.Contains(l, `"type":"assistant"`) {
			assistantLine = l
			break
		}
	}
	if assistantLine == "" {
		t.Fatalf("no assistant envelope in output:\n%s", out)
	}
	ev, ok := parseStreamEvent(assistantLine)
	if !ok {
		t.Fatalf("parseStreamEvent rejected assistant line:\n%s", assistantLine)
	}
	if len(ev.Message.Content) == 0 || ev.Message.Content[0].Text != "Hello world" {
		t.Fatalf("unexpected assistant content: %+v", ev.Message)
	}
}
