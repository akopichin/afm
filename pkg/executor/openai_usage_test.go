package executor

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/accounting"
)

// --- scripts/openai-as-claude.sh -------------------------------------------------

// TestOpenAIAsClaude_UsageEnvelopeEmitted: a final SSE chunk carrying .usage
// (with the nested prompt_tokens_details.cached_tokens OpenAI actually sends)
// must produce a synthetic result line with the exact field names/shape
// pkg/accounting's Collector parses, and it must normalize into real tokens
// through the REAL collector — not just look right by string matching.
func TestOpenAIAsClaude_UsageEnvelopeEmitted(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}

	fakeCurlDir := writeFakeCurl(t, `printf 'data: {"choices":[{"delta":{"content":"Hello "}}]}\n'
printf 'data: {"choices":[{"delta":{"content":"world"}}]}\n'
printf 'data: {"choices":[],"model":"gpt-4o-2024","usage":{"prompt_tokens":50,"completion_tokens":10,"prompt_tokens_details":{"cached_tokens":20}}}\n'
printf 'data: [DONE]\n'`)

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

	resultLine := extractResultLine(t, string(out))
	wantFragments := []string{
		`"usage_contract_version":1`,
		`"usage_schema":"openai_chat"`,
		`"channel":"openai-api"`,
		`"model":"gpt-4o-2024"`,
		`"prompt_tokens":50`,
		`"completion_tokens":10`,
		`"prompt_cached_tokens":20`,
	}
	for _, frag := range wantFragments {
		if !strings.Contains(resultLine, frag) {
			t.Errorf("result line missing %q: %s", frag, resultLine)
		}
	}
	// the raw nested OpenAI shape must not survive — it's flattened.
	if strings.Contains(resultLine, "prompt_tokens_details") {
		t.Errorf("nested prompt_tokens_details must be flattened away: %s", resultLine)
	}

	c := accounting.NewCollector(accounting.UsageHint{})
	c.Observe([]byte(resultLine))
	obs := c.Finish(nil)
	if !obs.Metered || obs.Schema != accounting.SchemaOpenAIChat || obs.Channel != "openai-api" || obs.Model != "gpt-4o-2024" {
		t.Fatalf("obs = %+v", obs)
	}
	// prompt_tokens=50 inclusive of 20 cached -> uncached=30; completion=10.
	if obs.Tokens.UncachedInput != 30 || obs.Tokens.CacheRead != 20 || obs.Tokens.Output != 10 {
		t.Fatalf("tokens = %+v", obs.Tokens)
	}
}

// TestOpenAIAsClaude_NoUsageInResponseStaysBareResult: without a usage-bearing
// chunk (e.g. the provider doesn't honor stream_options, or a test double
// omits it) the result line must stay the original bare shape — no fabricated
// usage_contract_version — so Collector.Finish reports unmetered, not $0.
func TestOpenAIAsClaude_NoUsageInResponseStaysBareResult(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}

	fakeCurlDir := writeFakeCurl(t, `printf 'data: {"choices":[{"delta":{"content":"ok"}}]}\ndata: [DONE]\n'`)

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
	resultLine := extractResultLine(t, string(out))
	if strings.Contains(resultLine, "usage_contract_version") {
		t.Errorf("must not fabricate a usage envelope: %s", resultLine)
	}

	c := accounting.NewCollector(accounting.UsageHint{})
	c.Observe([]byte(resultLine))
	if obs := c.Finish(nil); obs.Metered {
		t.Errorf("expected unmetered, got: %+v", obs)
	}
}

// TestOpenAIAsClaude_NoSecretOrPromptLeakage: neither the API key nor the
// prompt text may appear anywhere in the emitted synthetic result — the usage
// envelope is numbers-only by contract.
func TestOpenAIAsClaude_NoSecretOrPromptLeakage(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}

	const secretMarker = "sk-super-secret-should-never-leak"
	const promptMarker = "PROMPT_MUST_NOT_LEAK_abc987"

	fakeCurlDir := writeFakeCurl(t, `printf 'data: {"choices":[{"delta":{"content":"ok"}}]}\n'
printf 'data: {"choices":[],"model":"m","usage":{"prompt_tokens":1,"completion_tokens":1}}\n'
printf 'data: [DONE]\n'`)

	cmd := exec.Command("bash", scriptPath(t))
	cmd.Env = append(os.Environ(),
		"PATH="+fakeCurlDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"OPENAI_API_KEY="+secretMarker,
		"OPENAI_BASE_URL=http://fake/v1",
		"OPENAI_MODEL=m",
	)
	cmd.Stdin = strings.NewReader(promptMarker)

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("script failed: %v\noutput:\n%s", err, out)
	}
	resultLine := extractResultLine(t, string(out))
	if strings.Contains(resultLine, secretMarker) {
		t.Errorf("leaked API key into result line: %s", resultLine)
	}
	if strings.Contains(resultLine, promptMarker) {
		t.Errorf("leaked prompt text into result line: %s", resultLine)
	}
}

// --- scripts/openai-agent-as-claude.sh --------------------------------------------

// TestOpenAIAgentAsClaude_UpstreamUsagesArrayNotSummed: two internal turns
// each report their own usage; the script must attach BOTH raw objects to
// upstream_usages (no jq-side summation), and the REAL accounting.Collector
// must sum them into the correct total tokens.
func TestOpenAIAgentAsClaude_UpstreamUsagesArrayNotSummed(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}

	turn1 := `data: {"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"id":"call_1","function":{"name":"bash","arguments":"{\"command\": \"echo hi\"}"},"type":"function","index":0}]},"finish_reason":"tool_calls"}],"model":"gpt-x"}
data: {"choices":[],"model":"gpt-x","usage":{"prompt_tokens":30,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":10}}}
data: [DONE]
`
	turn2 := `data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"model":"gpt-x"}
data: {"choices":[],"model":"gpt-x","usage":{"prompt_tokens":60,"completion_tokens":8}}
data: [DONE]
`
	fakeCurlDir, _ := writeStatefulFakeCurl(t, []string{turn1, turn2}, "200")

	out, err := runAgentScript(t, fakeCurlDir, "do the thing\n")
	if err != nil {
		t.Fatalf("script failed: %v\noutput:\n%s", err, out)
	}
	resultLine := extractResultLine(t, out)

	wantFragments := []string{
		`"usage_contract_version":1`,
		`"usage_schema":"openai_chat"`,
		`"channel":"openai-api"`,
		`"model":"gpt-x"`,
		`"upstream_usages":[{"prompt_tokens":30,"completion_tokens":5,"prompt_cached_tokens":10},{"prompt_tokens":60,"completion_tokens":8}]`,
	}
	for _, frag := range wantFragments {
		if !strings.Contains(resultLine, frag) {
			t.Errorf("result line missing %q: %s", frag, resultLine)
		}
	}
	if strings.Contains(resultLine, "prompt_tokens_details") {
		t.Errorf("nested prompt_tokens_details must be flattened away: %s", resultLine)
	}

	c := accounting.NewCollector(accounting.UsageHint{})
	c.Observe([]byte(resultLine))
	obs := c.Finish(nil)
	if !obs.Metered {
		t.Fatalf("obs = %+v", obs)
	}
	// prompt totals: 30+60=90 inclusive of 10 cached -> uncached=80; completion 5+8=13.
	if obs.Tokens.UncachedInput != 80 || obs.Tokens.CacheRead != 10 || obs.Tokens.Output != 13 {
		t.Fatalf("tokens = %+v (accounting must sum upstream_usages itself)", obs.Tokens)
	}
}

// TestOpenAIAgentAsClaude_APIFailureEmitsGatheredUsageBeforeExit: the first
// turn reports usage and a tool call; the second turn's request fails
// outright (HTTP 500) — the script must still emit the usage gathered from
// turn 1 (with a non-success subtype) before exiting non-zero.
func TestOpenAIAgentAsClaude_APIFailureEmitsGatheredUsageBeforeExit(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}

	turn1 := `data: {"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"id":"call_1","function":{"name":"bash","arguments":"{\"command\": \"echo hi\"}"},"type":"function","index":0}]},"finish_reason":"tool_calls"}],"model":"gpt-x"}
data: {"choices":[],"model":"gpt-x","usage":{"prompt_tokens":30,"completion_tokens":5}}
data: [DONE]
`
	// writeStatefulFakeCurl returns the SAME http code for every invocation, so
	// turn 2's failure (a different http code than turn 1's 200) needs a
	// hand-rolled fake curl instead: call 0 succeeds with turn1's SSE body,
	// any later call returns a bare HTTP 500.
	fakeCurlDir := t.TempDir()
	counterFile := fakeCurlDir + "/counter"
	respOK := fakeCurlDir + "/ok"
	if err := writeFile(counterFile, "0"); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(respOK, turn1); err != nil {
		t.Fatal(err)
	}
	curlPath := fakeCurlDir + "/curl"
	script := "#!/usr/bin/env bash\n" +
		"n=$(cat " + counterFile + ")\n" +
		"echo $((n + 1)) > " + counterFile + "\n" +
		"if [ \"$n\" = \"0\" ]; then cat " + respOK + "; printf '\\n200'; else printf '{\"error\":\"server error\"}\\n500'; fi\n"
	if err := writeFile(curlPath, script); err != nil {
		t.Fatal(err)
	}
	if err := chmodExec(curlPath); err != nil {
		t.Fatal(err)
	}

	out, err := runAgentScript(t, fakeCurlDir, "do the thing\n")
	if err == nil {
		t.Fatalf("expected non-zero exit on turn-2 API failure, got success. output:\n%s", out)
	}
	if !strings.Contains(out, `"type":"assistant"`) {
		t.Errorf("expected an assistant envelope even on failure, got:\n%s", out)
	}
	resultLine := extractResultLine(t, out)
	if strings.Contains(resultLine, `"subtype":"success"`) {
		t.Errorf("failure path must not report subtype:success: %s", resultLine)
	}
	if !strings.Contains(resultLine, `"usage_contract_version":1`) {
		t.Errorf("expected turn-1 usage to survive the failure path: %s", resultLine)
	}
	if !strings.Contains(resultLine, `"prompt_tokens":30`) {
		t.Errorf("expected turn-1's own usage object, got: %s", resultLine)
	}
}

// TestOpenAIAgentAsClaude_NoSecretOrPromptLeakage: neither the API key nor the
// initial prompt text may appear anywhere in the emitted synthetic result.
func TestOpenAIAgentAsClaude_NoSecretOrPromptLeakage(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}

	const secretMarker = "sk-agent-secret-should-never-leak"
	const promptMarker = "AGENT_PROMPT_MUST_NOT_LEAK_qwe456"

	finalAnswer := `data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":""}],"model":"gpt-x"}
data: {"choices":[],"model":"gpt-x","usage":{"prompt_tokens":1,"completion_tokens":1}}
data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}
data: [DONE]
`
	fakeCurlDir, _ := writeStatefulFakeCurl(t, []string{finalAnswer}, "200")

	out, err := runAgentScript(t, fakeCurlDir, promptMarker+"\n", "OPENAI_API_KEY="+secretMarker)
	if err != nil {
		t.Fatalf("script failed: %v\noutput:\n%s", err, out)
	}
	resultLine := extractResultLine(t, out)
	if strings.Contains(resultLine, secretMarker) {
		t.Errorf("leaked API key into result line: %s", resultLine)
	}
	if strings.Contains(resultLine, promptMarker) {
		t.Errorf("leaked prompt text into result line: %s", resultLine)
	}
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}

func chmodExec(path string) error {
	return os.Chmod(path, 0o755)
}
