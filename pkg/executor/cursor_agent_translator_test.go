package executor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/accounting"
)

const cursorTestKey = "crsr_test_secret_key_123"

const cursorSuccessEvents = `{"type":"system","subtype":"init","apiKeySource":"env","cwd":"/work","session_id":"s1","model":"Auto","permissionMode":"default"}
{"type":"user","message":{"role":"user","content":[{"type":"text","text":"do it"}]},"session_id":"s1"}
{"type":"thinking","subtype":"delta","text":"Let me ","session_id":"s1"}
{"type":"thinking","subtype":"delta","text":"look around.","session_id":"s1"}
{"type":"thinking","subtype":"completed","session_id":"s1"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Checking the directory."}]},"session_id":"s1"}
{"type":"tool_call","subtype":"started","call_id":"c1","tool_call":{"shellToolCall":{"args":{"command":"ls -la"}}},"session_id":"s1"}
{"type":"tool_call","subtype":"completed","call_id":"c1","tool_call":{"shellToolCall":{"args":{"command":"ls -la"},"result":{"success":{"exitCode":0,"stdout":"total 0","stderr":""}}}},"session_id":"s1"}
{"type":"tool_call","subtype":"started","call_id":"c2","tool_call":{"globToolCall":{"args":{"globPattern":"*.go"}}},"session_id":"s1"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"All done."}]},"session_id":"s1"}
{"type":"result","subtype":"success","is_error":false,"duration_ms":1234,"result":"All done.","session_id":"s1","usage":{"inputTokens":100,"outputTokens":20,"cacheReadTokens":5,"cacheWriteTokens":7}}
`

type fakeCursor struct {
	binDir, argvFile, stdinFile string
}

// newFakeCursor installs a cursor-agent stand-in that records argv and stdin, prints events and exits with code.
func newFakeCursor(t *testing.T, events string, exitCode int) fakeCursor {
	t.Helper()
	dir := t.TempDir()
	f := fakeCursor{binDir: dir, argvFile: filepath.Join(dir, "argv"), stdinFile: filepath.Join(dir, "stdin")}
	eventsFile := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(eventsFile, []byte(events), 0o644); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf("#!/usr/bin/env bash\nprintf '%%s\\n' \"$@\" > %q\ncat > %q\ncat %q\nexit %d\n", f.argvFile, f.stdinFile, eventsFile, exitCode)
	if err := os.WriteFile(filepath.Join(dir, "cursor-agent"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return f
}

type cursorRun struct {
	stdout, stderr string
	exitCode       int
}

func (f fakeCursor) run(t *testing.T, args []string, env ...string) cursorRun {
	t.Helper()
	return f.runWithStdin(t, "the prompt text\n", args, env...)
}

func (f fakeCursor) runWithStdin(t *testing.T, stdin string, args []string, env ...string) cursorRun {
	t.Helper()
	for _, tool := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not available")
		}
	}
	script := repoFile(t, "scripts", "cursor-agent-as-claude.sh")
	cmd := exec.Command("bash", append([]string{script}, args...)...)
	cmd.Env = append(os.Environ(), "PATH="+f.binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"CURSOR_API_KEY="+cursorTestKey, "CURSOR_MODEL=")
	cmd.Env = append(cmd.Env, env...)
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	res := cursorRun{stdout: stdout.String(), stderr: stderr.String()}
	if exitErr, ok := err.(*exec.ExitError); ok {
		res.exitCode = exitErr.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return res
}

func (f fakeCursor) argv(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(f.argvFile)
	if err != nil {
		t.Fatalf("cursor-agent was not started: %v", err)
	}
	return strings.Fields(string(raw))
}

func nonEmptyLines(s string) []string {
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

func TestCursorAgentAsClaude_TranslatesEventsAndReportsSuccess(t *testing.T) {
	f := newFakeCursor(t, cursorSuccessEvents, 0)
	// Claude flags (including session flags and a different model) are ignored.
	res := f.run(t, []string{"--model", "sonnet", "--session-id", "abc", "--resume", "xyz", "--dangerously-skip-permissions"})
	if res.exitCode != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", res.exitCode, res.stdout, res.stderr)
	}

	lines := nonEmptyLines(res.stdout)
	var wantKinds = []string{`"type":"thinking"`, `"text":"Checking the directory."`, `"name":"Bash"`, `"name":"Glob"`, `"text":"All done."`, `"type":"result"`}
	if len(lines) != len(wantKinds) {
		t.Fatalf("got %d lines, want %d:\n%s", len(lines), len(wantKinds), res.stdout)
	}
	for i, want := range wantKinds {
		if !strings.Contains(lines[i], want) {
			t.Errorf("line %d = %s, want it to contain %s", i, lines[i], want)
		}
	}
	if !strings.Contains(lines[0], `"thinking":"Let me look around."`) {
		t.Errorf("thinking deltas were not joined: %s", lines[0])
	}
	if tool, detail, ok := ParseToolAction(lines[2], 0); !ok || tool != "Bash" || detail != "ls -la" {
		t.Errorf("ParseToolAction(shell) = %q %q %v", tool, detail, ok)
	}
	if tool, detail, ok := ParseToolAction(lines[3], 0); !ok || tool != "Glob" || detail != "*.go" {
		t.Errorf("ParseToolAction(glob) = %q %q %v", tool, detail, ok)
	}
	if !strings.Contains(lines[5], `"subtype":"success"`) || strings.Contains(lines[5], "is_error") {
		t.Errorf("result line = %s", lines[5])
	}
}

func TestCursorAgentAsClaude_ResultUsageIsAccountedAsAnthropicSchema(t *testing.T) {
	f := newFakeCursor(t, cursorSuccessEvents, 0)
	res := f.run(t, nil)

	collector := accounting.NewCollector(accounting.UsageHint{})
	for _, line := range nonEmptyLines(res.stdout) {
		collector.Observe([]byte(line))
	}
	obs := collector.Finish(nil)
	want := accounting.Tokens{UncachedInput: 100, Output: 20, CacheRead: 5, CacheWriteOther: 7}
	if !obs.Metered || obs.Channel != "cursor" || obs.Model != "Auto" || obs.Tokens.UncachedInput != want.UncachedInput ||
		obs.Tokens.Output != want.Output || obs.Tokens.CacheRead != want.CacheRead || obs.Tokens.CacheWriteTotal() != want.CacheWriteOther {
		t.Errorf("observation = %+v", obs)
	}
}

func TestCursorAgentAsClaude_PassesPromptViaStdinAndModelViaFlag(t *testing.T) {
	cases := map[string]struct{ model, want string }{
		"default is auto": {"", "auto"},
		"recipe model":    {"gpt-5", "gpt-5"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFakeCursor(t, cursorSuccessEvents, 0)
			res := f.run(t, nil, "CURSOR_MODEL="+tc.model)
			if res.exitCode != 0 {
				t.Fatalf("exit %d: %s", res.exitCode, res.stderr)
			}
			wantArgs := []string{"-p", "--force", "--output-format", "stream-json", "--model", tc.want}
			if got := f.argv(t); strings.Join(got, " ") != strings.Join(wantArgs, " ") {
				t.Errorf("argv = %v, want %v", got, wantArgs)
			}
			if stdin, _ := os.ReadFile(f.stdinFile); strings.TrimSpace(string(stdin)) != "the prompt text" {
				t.Errorf("prompt on stdin = %q", stdin)
			}
		})
	}
}

func TestCursorAgentAsClaude_APIKeyStaysOutOfArgvAndOutput(t *testing.T) {
	f := newFakeCursor(t, strings.Replace(cursorSuccessEvents, `"is_error":false`, `"is_error":true`, 1), 0)
	res := f.run(t, nil)

	argv, _ := os.ReadFile(f.argvFile)
	for name, text := range map[string]string{"argv": string(argv), "stdout": res.stdout, "stderr": res.stderr} {
		if strings.Contains(text, cursorTestKey) {
			t.Errorf("API key leaked into %s", name)
		}
	}
}

func TestCursorAgentAsClaude_FailuresAreErrorsNotSuccess(t *testing.T) {
	withoutResult := strings.Join(strings.Split(cursorSuccessEvents, "\n")[:10], "\n") + "\n"
	cases := map[string]struct {
		events   string
		exitCode int
		wantText string
	}{
		"result reports is_error": {
			`{"type":"result","subtype":"error","is_error":true,"result":"usage limit reached"}` + "\n", 0, "usage limit reached"},
		"is_error with an empty result": {
			`{"type":"result","subtype":"error","is_error":true,"result":""}` + "\n", 0, "cursor-agent reported an error"},
		"is_error with a blank result": {
			`{"type":"result","subtype":"error","is_error":true,"result":"  "}` + "\n", 0, "cursor-agent reported an error"},
		"no result event":  {withoutResult, 0, "without a result event"},
		"non-zero exit":    {cursorSuccessEvents, 3, "exited with status 3"},
		"no output at all": {"", 0, "without a result event"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFakeCursor(t, tc.events, tc.exitCode)
			res := f.run(t, nil)
			if res.exitCode != 1 {
				t.Fatalf("exit = %d, want 1\n%s", res.exitCode, res.stdout)
			}
			lines := nonEmptyLines(res.stdout)
			last := lines[len(lines)-1]
			for _, want := range []string{`"type":"result"`, `"subtype":"error_during_execution"`, `"is_error":true`, tc.wantText} {
				if !strings.Contains(last, want) {
					t.Errorf("last line %s lacks %s", last, want)
				}
			}
			if !strings.Contains(res.stderr, tc.wantText) {
				t.Errorf("stderr lacks %q:\n%s", tc.wantText, res.stderr)
			}
		})
	}
}

func TestCursorAgentAsClaude_MissingKeyOrPromptNeverStartsTheAgent(t *testing.T) {
	for name, tc := range map[string]struct {
		stdin, env, wantErr string
	}{
		"missing key":  {"the prompt text\n", "CURSOR_API_KEY=", "CURSOR_API_KEY is not set"},
		"empty prompt": {"", "", "empty prompt"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeCursor(t, cursorSuccessEvents, 0)
			res := f.runWithStdin(t, tc.stdin, nil, tc.env)
			if res.exitCode != 1 || !strings.Contains(res.stderr, tc.wantErr) {
				t.Fatalf("exit %d, stderr %q", res.exitCode, res.stderr)
			}
			if _, err := os.Stat(f.argvFile); err == nil {
				t.Error("cursor-agent was started")
			}
		})
	}
}

func TestCursorAgentAsClaude_ResultTextBecomesAssistantWhenNothingStreamed(t *testing.T) {
	events := `{"type":"system","subtype":"init","model":"Auto"}` + "\n" +
		`{"type":"result","subtype":"success","is_error":false,"result":"only in result"}` + "\n"
	f := newFakeCursor(t, events, 0)
	res := f.run(t, nil)
	lines := nonEmptyLines(res.stdout)
	if res.exitCode != 0 || len(lines) != 2 || !strings.Contains(lines[0], `"text":"only in result"`) {
		t.Fatalf("exit %d:\n%s", res.exitCode, res.stdout)
	}
	if strings.Contains(lines[1], "usage") {
		t.Errorf("a result without usage must stay unmetered: %s", lines[1])
	}
}

func TestCursorAgentAsClaude_IgnoresMalformedLines(t *testing.T) {
	events := "not json at all\n{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"hi\"}]}}\n{broken\n" +
		`{"type":"result","subtype":"success","is_error":false,"result":"hi"}` + "\n"
	f := newFakeCursor(t, events, 0)
	res := f.run(t, nil)
	lines := nonEmptyLines(res.stdout)
	if res.exitCode != 0 || len(lines) != 2 {
		t.Fatalf("exit %d:\n%s", res.exitCode, res.stdout)
	}
}

// cursor_agent_stream.jsonl is a real cursor-agent 2026.10.22 run (glob + shell tool calls, thinking, result).
func TestCursorAgentAsClaude_TranslatesARealRecordedRun(t *testing.T) {
	recorded, err := os.ReadFile(filepath.Join("testdata", "cursor_agent_stream.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	f := newFakeCursor(t, string(recorded), 0)
	res := f.run(t, nil)
	if res.exitCode != 0 {
		t.Fatalf("exit %d: %s", res.exitCode, res.stderr)
	}

	var tools []string
	for _, line := range nonEmptyLines(res.stdout) {
		if name, detail, ok := ParseToolAction(line, 0); ok && name != "text" {
			tools = append(tools, name+":"+detail)
		}
	}
	if got := strings.Join(tools, "|"); got != "Glob:.cursor/rules/**/*|Bash:echo hello-probe" {
		t.Errorf("tool actions = %q", got)
	}
	if strings.Contains(res.stdout, "parsingResult") {
		t.Error("the shell parse tree leaked into the feed")
	}
	if got := strings.Count(res.stdout, `"type":"thinking"`); got != 2 {
		t.Errorf("thinking blocks = %d, want 2 (one per completed thought)", got)
	}
	last := nonEmptyLines(res.stdout)
	if !strings.Contains(last[len(last)-1], `"input_tokens":5817`) || !strings.Contains(last[len(last)-1], `"cache_read_input_tokens":13312`) {
		t.Errorf("result usage = %s", last[len(last)-1])
	}
}
