package executor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// textSSE is a streamed final answer with the given text.
func textSSE(t *testing.T, text string) string {
	t.Helper()
	return strings.Replace(finalTextSSE, `"content":"done"`, fmt.Sprintf(`"content":%q`, text), 1)
}

// writeFakeAfm installs an `afm schema validate` stand-in: stdin containing "VALID" passes, anything else
// fails with the given stderr and exit code. It records every call's stdin.
func writeFakeAfm(t *testing.T, failStderr string, failExit int) (binDir, callsFile string) {
	t.Helper()
	binDir = t.TempDir()
	callsFile = filepath.Join(binDir, "calls.log")
	script := fmt.Sprintf(`#!/usr/bin/env bash
[[ "$1 $2 $3" == "schema validate --schema" ]] || { echo "unexpected args: $*" >&2; exit 2; }
input=$(cat)
printf '%%s\n' "$input" >> %q
case "$input" in *VALID*) exit 0 ;; esac
printf '%%s\n' %q >&2
exit %d
`, callsFile, failStderr, failExit)
	if err := os.WriteFile(filepath.Join(binDir, "afm"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return binDir, callsFile
}

const outputInvalidStderr = `{"error":"output_invalid","message":"missing property verdict"}`

// schemaEnv sets the schema file and puts the fake curl and the fake afm first on PATH.
func schemaEnv(fakeCurlDir, afmDir string) []string {
	path := strings.Join([]string{fakeCurlDir, afmDir, os.Getenv("PATH")}, string(os.PathListSeparator))
	return []string{"AFM_OUTPUT_SCHEMA_FILE=/schemas/verify.json", "PATH=" + path}
}

// requestMessages returns the messages of the n-th curl request.
func requestMessages(t *testing.T, captureDir string, n int) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(captureDir, fmt.Sprintf("call_%d.args", n)))
	if err != nil {
		t.Fatal(err)
	}
	var messages []map[string]any
	for _, m := range decodeCapturedRequestBody(t, raw)["messages"].([]any) {
		messages = append(messages, m.(map[string]any))
	}
	return messages
}

func finalAnswerText(t *testing.T, out string) string {
	t.Helper()
	var text string
	for _, line := range strings.Split(out, "\n") {
		if ev, ok := parseStreamEvent(strings.TrimSpace(line)); ok && len(ev.Message.Content) > 0 && ev.Message.Content[0].Type == "text" {
			text = ev.Message.Content[0].Text
		}
	}
	return text
}

func TestOpenAIAgentAsClaude_SchemaInvalidFinalIsCorrectedOnce(t *testing.T) {
	requireAgentShimTools(t)
	afmDir, calls := writeFakeAfm(t, outputInvalidStderr, 1)
	fakeCurlDir, captureDir := writeStatefulFakeCurl(t, []string{textSSE(t, "pass, all good"), textSSE(t, "VALID verdict")}, "200")

	out, err := runAgentScript(t, fakeCurlDir, "verify it\n", schemaEnv(fakeCurlDir, afmDir)...)
	if err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}

	if got := countCaptures(t, captureDir); got != 2 {
		t.Fatalf("curl calls = %d, want 2 (answer + one correction)", got)
	}
	messages := requestMessages(t, captureDir, 1)
	correction := messages[len(messages)-1]
	content, _ := correction["content"].(string)
	if correction["role"] != "user" || !strings.Contains(content, "missing property verdict") || !strings.Contains(content, "only the corrected JSON") {
		t.Errorf("correction message = %v", correction)
	}
	if prev := messages[len(messages)-2]; prev["role"] != "assistant" || prev["content"] != "pass, all good" {
		t.Errorf("the rejected answer is not in the history: %v", prev)
	}
	if got := finalAnswerText(t, out); got != "VALID verdict" {
		t.Errorf("final text = %q", got)
	}
	if !strings.Contains(out, `"subtype":"success"`) {
		t.Errorf("no success result:\n%s", out)
	}
	if logged, _ := os.ReadFile(calls); strings.Count(string(logged), "\n") != 2 {
		t.Errorf("validator calls:\n%s", logged)
	}
}

func TestOpenAIAgentAsClaude_SchemaCorrectionsStopAfterTwo(t *testing.T) {
	requireAgentShimTools(t)
	afmDir, _ := writeFakeAfm(t, outputInvalidStderr, 1)
	fakeCurlDir, captureDir := writeStatefulFakeCurl(t, []string{textSSE(t, "bad 1"), textSSE(t, "bad 2"), textSSE(t, "bad 3")}, "200")

	out, err := runAgentScript(t, fakeCurlDir, "verify it\n", schemaEnv(fakeCurlDir, afmDir)...)
	if err != nil {
		t.Fatalf("exhausted corrections must still succeed: %v\n%s", err, out)
	}

	if got := countCaptures(t, captureDir); got != 3 {
		t.Fatalf("curl calls = %d, want 3 (answer + two corrections)", got)
	}
	if got := finalAnswerText(t, out); got != "bad 3" {
		t.Errorf("final text = %q, want the last answer", got)
	}
	if !strings.Contains(out, `"subtype":"success"`) {
		t.Errorf("no success result:\n%s", out)
	}
}

func TestOpenAIAgentAsClaude_SchemaValidatorProblemsAcceptTheAnswer(t *testing.T) {
	requireAgentShimTools(t)
	cases := map[string]struct{ stderr string }{
		"schema file unusable": {`{"error":"schema_invalid","message":"bad schema"}`},
		"validator crashed":    {`panic: boom`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			afmDir, _ := writeFakeAfm(t, tc.stderr, 1)
			fakeCurlDir, captureDir := writeStatefulFakeCurl(t, []string{textSSE(t, "the answer")}, "200")

			out, err := runAgentScript(t, fakeCurlDir, "verify it\n", schemaEnv(fakeCurlDir, afmDir)...)
			if err != nil {
				t.Fatalf("script failed: %v\n%s", err, out)
			}
			if got := countCaptures(t, captureDir); got != 1 {
				t.Errorf("curl calls = %d, want 1: only output_invalid asks for a correction", got)
			}
			if got := finalAnswerText(t, out); got != "the answer" {
				t.Errorf("final text = %q", got)
			}
		})
	}
}

func TestOpenAIAgentAsClaude_SchemaWithoutAfmAcceptsTheAnswer(t *testing.T) {
	requireAgentShimTools(t)
	if _, err := exec.LookPath("afm"); err == nil {
		t.Skip("a real afm is on PATH")
	}
	fakeCurlDir, captureDir := writeStatefulFakeCurl(t, []string{textSSE(t, "the answer")}, "200")

	out, err := runAgentScript(t, fakeCurlDir, "verify it\n", "AFM_OUTPUT_SCHEMA_FILE=/schemas/verify.json")
	if err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}
	if got := countCaptures(t, captureDir); got != 1 {
		t.Errorf("curl calls = %d, want 1", got)
	}
}

func TestOpenAIAgentAsClaude_NoSchemaFileMeansNoValidation(t *testing.T) {
	requireAgentShimTools(t)
	afmDir, calls := writeFakeAfm(t, outputInvalidStderr, 1)
	fakeCurlDir, captureDir := writeStatefulFakeCurl(t, []string{textSSE(t, "free text")}, "200")

	out, err := runAgentScript(t, fakeCurlDir, "verify it\n", "AFM_OUTPUT_SCHEMA_FILE=", "PATH="+fakeCurlDir+string(os.PathListSeparator)+afmDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}
	if got := countCaptures(t, captureDir); got != 1 {
		t.Errorf("curl calls = %d, want 1", got)
	}
	if _, statErr := os.Stat(calls); statErr == nil {
		t.Error("validator ran without a schema file")
	}
}

// The schema file is the shim's own control: commands the model runs (go test, say) must not inherit it, while
// the shim still checks its final answer against it.
func TestOpenAIAgentAsClaude_ToolCommandsDoNotSeeTheSchemaFile(t *testing.T) {
	requireAgentShimTools(t)
	afmDir, calls := writeFakeAfm(t, "", 0)
	fakeCurlDir, captureDir := writeStatefulFakeCurl(t, []string{toolCallSSE(t, `{"command": "echo schema=${AFM_OUTPUT_SCHEMA_FILE:-unset}"}`), finalTextSSE}, "200")

	if out, err := runAgentScript(t, fakeCurlDir, "go\n", schemaEnv(fakeCurlDir, afmDir)...); err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(filepath.Join(captureDir, "call_1.args"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "schema=unset") {
		t.Errorf("tool command saw the schema file: %s", raw)
	}
	if _, statErr := os.Stat(calls); statErr != nil {
		t.Errorf("the final answer was not validated: %v", statErr)
	}
}

func TestOpenAIAgentAsClaude_ExportsLongAnswerWait(t *testing.T) {
	requireAgentShimTools(t)
	for name, tc := range map[string]struct{ env, want string }{
		"default":           {"", "600"},
		"explicit override": {"AFM_WAIT_ANSWER_TIMEOUT=45", "45"},
	} {
		t.Run(name, func(t *testing.T) {
			fakeCurlDir, captureDir := writeStatefulFakeCurl(t, []string{toolCallSSE(t, `{"command": "echo wait=$AFM_WAIT_ANSWER_TIMEOUT"}`), finalTextSSE}, "200")
			env := []string{"AFM_WAIT_ANSWER_TIMEOUT="}
			if tc.env != "" {
				env = []string{tc.env}
			}
			if out, err := runAgentScript(t, fakeCurlDir, "go\n", env...); err != nil {
				t.Fatalf("script failed: %v\n%s", err, out)
			}
			raw, err := os.ReadFile(filepath.Join(captureDir, "call_1.args"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), "wait="+tc.want+`\n`) {
				t.Errorf("tool output lacks wait=%s:\n%s", tc.want, raw)
			}
		})
	}
}
