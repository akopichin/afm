package executor

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
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

// runOpenAIFailure запускает wrapper против fake curl, отвечающего HTTP-ошибкой, и возвращает stdout и stderr.
func runOpenAIFailure(t *testing.T, script, body, status string) (stdout, stderr string) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}
	fakeCurlDir := writeFakeCurl(t, `printf '%s\n%s' '`+body+`' '`+status+`'`)
	cmd := exec.Command("bash", script)
	cmd.Env = append(os.Environ(),
		"PATH="+fakeCurlDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"OPENAI_API_KEY=test",
		"OPENAI_BASE_URL=http://fake/v1",
		"OPENAI_MODEL=m",
	)
	cmd.Stdin = bytes.NewBufferString("x\n")
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &outBuf, &errBuf
	if err := cmd.Run(); err == nil {
		t.Fatalf("expected non-zero exit on HTTP %s", status)
	}
	return outBuf.String(), errBuf.String()
}

// TestOpenAIWrappers_429KeepsErrorCode: 429 сохраняет error.code и тело в stderr и в result-строке;
// insufficient_quota распознаётся как лимит без сброса, rate_limit_exceeded остаётся transient.
func TestOpenAIWrappers_429KeepsErrorCode(t *testing.T) {
	const (
		quotaBody = `{"error":{"code":"insufficient_quota","message":"You exceeded your current quota, please check your plan and billing details."}}`
		rateBody  = `{"error":{"code":"rate_limit_exceeded","message":"Rate limit reached for m. Please try again in 20s."}}`
	)
	scripts := map[string]string{
		"openai":       scriptPath(t),
		"openai-agent": agentScriptPath(t),
	}
	for name, script := range scripts {
		t.Run(name, func(t *testing.T) {
			stdout, stderr := runOpenAIFailure(t, script, quotaBody, "429")
			if !strings.Contains(stderr, "insufficient_quota") {
				t.Errorf("stderr must keep error.code, got:\n%s", stderr)
			}
			var env struct {
				IsError bool   `json:"is_error"`
				Result  string `json:"result"`
			}
			results := countResultLines(stdout)
			if len(results) != 1 {
				t.Fatalf("want exactly one result line, got %d:\n%s", len(results), stdout)
			}
			if err := json.Unmarshal([]byte(results[0]), &env); err != nil {
				t.Fatalf("result line is not valid JSON: %v\n%s", err, results[0])
			}
			if !env.IsError || !strings.Contains(env.Result, "insufficient_quota") {
				t.Errorf("result must carry is_error and error.code, got %+v", env)
			}
			if limit, ok := parseUsageLimitText(env.Result, time.Now(), time.UTC); !ok || !limit.NoReset {
				t.Errorf("insufficient_quota must be a no-reset usage limit, got %+v ok=%v", limit, ok)
			}

			stdout, stderr = runOpenAIFailure(t, script, rateBody, "429")
			if !strings.Contains(stderr, "rate_limit_exceeded") || !strings.Contains(stdout, "rate_limit_exceeded") {
				t.Errorf("rate limit code must be kept in stderr and result:\nstderr: %s\nstdout: %s", stderr, stdout)
			}
			if _, ok := parseUsageLimitText(stdout, time.Now(), time.UTC); ok {
				t.Error("rate_limit_exceeded must stay a transient error, not a usage limit")
			}
		})
	}
}

// TestOpenAIWrappers_HugeErrorBodyKeepsResultLine: тело ошибки больше 128 KiB не должно ронять сборку
// result-строки на лимите длины аргумента exec («Argument list too long»): строка с is_error (и upstream_usages
// агентского wrapper'а) печатается, а текст ошибки в ней ограничен.
func TestOpenAIWrappers_HugeErrorBodyKeepsResultLine(t *testing.T) {
	const resultErrorCap = 16 * 1024
	usageChunk := `data: {"model":"m","usage":{"prompt_tokens":3,"completion_tokens":4}}`
	errorBody := `{"error":{"code":"insufficient_quota","message":"` + strings.Repeat("x", 200*1024) + `"}}`
	body := usageChunk + "\n" + errorBody

	tests := []struct {
		name       string
		script     string
		wantUsages bool
	}{
		{"openai", scriptPath(t), false},
		{"openai-agent", agentScriptPath(t), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr := runOpenAIFailure(t, tc.script, body, "429")
			if strings.Contains(stderr, "Argument list too long") {
				t.Fatalf("wrapper hit the exec argument limit:\n%.500s", stderr)
			}
			results := countResultLines(stdout)
			if len(results) != 1 {
				t.Fatalf("want exactly one result line, got %d:\n%.500s", len(results), stdout)
			}
			var env struct {
				IsError        bool              `json:"is_error"`
				Result         string            `json:"result"`
				UpstreamUsages []json.RawMessage `json:"upstream_usages"`
			}
			if err := json.Unmarshal([]byte(results[0]), &env); err != nil {
				t.Fatalf("result line is not valid JSON: %v", err)
			}
			if !env.IsError || !strings.Contains(env.Result, "insufficient_quota") {
				t.Errorf("result must carry is_error and error.code, got is_error=%v result=%.200q", env.IsError, env.Result)
			}
			if len(env.Result) > resultErrorCap {
				t.Errorf("result error text must be capped, got %d bytes", len(env.Result))
			}
			if tc.wantUsages && len(env.UpstreamUsages) != 1 {
				t.Errorf("upstream_usages must survive, got %d items", len(env.UpstreamUsages))
			}
		})
	}
}
