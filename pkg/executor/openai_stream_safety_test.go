//go:build !windows

package executor

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// A response that merely stops after HTTP 200 is a cut stream: nothing of it may be published as complete, no tool
// may run, no final answer may be named.
func TestOpenAIAgentAsClaude_StreamExtension_TruncatedStreamIsAnError(t *testing.T) {
	requireAgentShimTools(t)
	ran := filepath.Join(t.TempDir(), "ran")
	cutToolCall := sseChunk(`{"role":"assistant","tool_calls":[{"id":"c1","type":"function","index":0,"function":{"name":"bash","arguments":""}}]}`) +
		sseChunk(`{"tool_calls":[{"id":"","index":0,"function":{"arguments":`+strconv.Quote(`{"command":"touch `+ran+`"}`)+`}}]}`)
	for name, body := range map[string]string{
		"text only":      contentChunk("half an answer"),
		"cut tool round": contentChunk("running") + cutToolCall,
	} {
		t.Run(name, func(t *testing.T) {
			fakeCurlDir, _ := writeStatefulFakeCurl(t, []string{body}, "200")
			out, err := runAgentScript(t, fakeCurlDir, "go\n", agentExt)
			if err == nil {
				t.Fatalf("a cut stream must exit non-zero:\n%s", out)
			}
			lines := decodeLines(out)
			if finals := finalLines(lines); len(finals) != 0 {
				t.Errorf("afm_final after a cut stream: %v", finals)
			}
			for _, l := range lineOfType(lines, "assistant") {
				if b := contentBlock(l); b["type"] == "tool_use" || afmComplete(l) {
					t.Errorf("a cut stream published a complete step: %v", l)
				}
			}
			if _, statErr := os.Stat(ran); statErr == nil {
				t.Error("a tool of a cut round was executed")
			}
			if res := lineOfType(lines, "result"); len(res) != 1 || !isErrorResult(res[0]) {
				t.Errorf("result = %v", res)
			}
		})
	}
}

// secretKey is long enough to be recognised in argv dumps.
const secretKey = "sk-argv-secret-0123456789" //nolint:gosec // test fixture, not a real credential

// curlTaps are the files a fake curl leaves behind: its argv and the config it read from stdin.
type curlTaps struct{ argv, cfg string }

// writeArgvCurl writes a fake curl that records its argv and the config read from stdin (-K -), then answers body.
func writeArgvCurl(t *testing.T, body string) (fakeDir string, taps curlTaps) {
	t.Helper()
	fakeDir, out := t.TempDir(), t.TempDir()
	taps = curlTaps{argv: filepath.Join(out, "argv"), cfg: filepath.Join(out, "cfg")}
	resp := filepath.Join(out, "resp")
	if err := os.WriteFile(resp, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	script := `#!/usr/bin/env bash
printf '%s\n' "$@" > ` + strconv.Quote(taps.argv) + `
prev=""
for a in "$@"; do
    if { [ "$prev" = "-K" ] || [ "$prev" = "--config" ]; } && [ "$a" = "-" ]; then
        cat > ` + strconv.Quote(taps.cfg) + `
    fi
    prev=$a
done
cat ` + strconv.Quote(resp) + `
printf '\n200'
`
	if err := os.WriteFile(filepath.Join(fakeDir, "curl"), []byte(script), 0o755); err != nil { //nolint:gosec // test fixture: an executable fake curl
		t.Fatal(err)
	}
	return fakeDir, taps
}

func assertKeyStaysOutOfArgv(t *testing.T, taps curlTaps) {
	t.Helper()
	argv, err := os.ReadFile(taps.argv)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(argv), secretKey) {
		t.Errorf("the API key is in curl's argv:\n%s", argv)
	}
	cfg, err := os.ReadFile(taps.cfg)
	if err != nil || !strings.Contains(string(cfg), "Authorization: Bearer "+secretKey) {
		t.Fatalf("the Authorization header did not reach curl through its stdin config (err %v):\n%s", err, cfg)
	}
}

// wrapperRun starts a wrapper by name with its own environment; the agent wrapper reads one prompt line.
type wrapperRun struct {
	name, file string
}

var openAIWrappers = []wrapperRun{{"agent", "openai-agent-as-claude.sh"}, {"text", "openai-as-claude.sh"}}

func (w wrapperRun) command(t *testing.T, fakeDir string, env ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("bash", repoFile(t, "scripts", w.file))
	cmd.Env = append(os.Environ(), "PATH="+fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"AFM_OUTPUT_SCHEMA_FILE=", "AFM_WAIT_ANSWER_TIMEOUT=", "OPENAI_BASE_URL=http://fake/v1", "OPENAI_MODEL=m")
	cmd.Env = append(cmd.Env, env...)
	cmd.Stdin = strings.NewReader("go\n")
	return cmd
}

func TestOpenAIWrappers_APIKeyIsNotInCurlArgv(t *testing.T) {
	requireAgentShimTools(t)
	body := finalRound("done")
	for _, w := range openAIWrappers {
		for _, ext := range []string{StreamExtensionEnv + "=1", StreamExtensionEnv + "=0"} {
			t.Run(w.name+" "+ext, func(t *testing.T) {
				fakeDir, taps := writeArgvCurl(t, body)
				if out, err := w.command(t, fakeDir, "OPENAI_API_KEY="+secretKey, ext).CombinedOutput(); err != nil {
					t.Fatalf("script failed: %v\n%s", err, out)
				}
				assertKeyStaysOutOfArgv(t, taps)
			})
		}
	}
}

// containsKey reports the first file under dir that holds the key.
func containsKey(t *testing.T, dir string) string {
	t.Helper()
	var found string
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // a file that vanished mid-walk cannot hold the key
		}
		if data, readErr := os.ReadFile(path); readErr == nil && strings.Contains(string(data), secretKey) { //nolint:gosec // walks a private test temp dir, no symlink race
			found = path
		}
		return nil
	})
	return found
}

// The executor stops a helper with SIGKILL, which no trap survives: the key must never be on disk while curl runs,
// and certainly not after the wrapper's process group is killed.
func TestOpenAIWrappers_KilledRunLeavesNoKeyOnDisk(t *testing.T) {
	requireAgentShimTools(t)
	for _, w := range openAIWrappers {
		for _, ext := range []string{StreamExtensionEnv + "=1", StreamExtensionEnv + "=0"} {
			t.Run(w.name+" "+ext, func(t *testing.T) {
				fakeDir, tmp, out := t.TempDir(), t.TempDir(), t.TempDir()
				started, gate := filepath.Join(out, "started"), filepath.Join(out, "never")
				script := `#!/usr/bin/env bash
cat > ` + strconv.Quote(filepath.Join(out, "cfg")) + `
: > ` + strconv.Quote(started) + `
until [ -e ` + strconv.Quote(gate) + ` ]; do sleep 0.02; done
`
				if err := os.WriteFile(filepath.Join(fakeDir, "curl"), []byte(script), 0o755); err != nil { //nolint:gosec // test fixture: an executable fake curl
					t.Fatal(err)
				}
				cmd := w.command(t, fakeDir, "TMPDIR="+tmp, "OPENAI_API_KEY="+secretKey, ext)
				cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				killed := false
				kill := func() {
					if !killed {
						killed = true
						_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
						_ = cmd.Wait()
					}
				}
				t.Cleanup(kill)
				waitForMarker(t, started)
				if path := containsKey(t, tmp); path != "" {
					t.Errorf("the API key is on disk while curl runs: %s", path)
				}
				kill()
				if path := containsKey(t, tmp); path != "" {
					t.Errorf("the API key is left on disk after the kill: %s", path)
				}
			})
		}
	}
}

// A line break in the key would end its config line and let the rest act as further curl directives.
func TestOpenAIWrappers_KeyWithLineBreakIsRejected(t *testing.T) {
	requireAgentShimTools(t)
	for _, w := range openAIWrappers {
		for _, sep := range []string{"\n", "\r"} {
			t.Run(w.name+" "+strconv.Quote(sep), func(t *testing.T) {
				fakeDir, taps := writeArgvCurl(t, finalRound("done"))
				key := secretKey + sep + `url = "http://evil"`
				out, err := w.command(t, fakeDir, "OPENAI_API_KEY="+key).CombinedOutput()
				if err == nil {
					t.Fatalf("a key with a line break was accepted:\n%s", out)
				}
				if strings.Contains(string(out), secretKey) {
					t.Errorf("the error shows the key:\n%s", out)
				}
				if _, statErr := os.Stat(taps.argv); statErr == nil {
					t.Error("curl ran with a key that has a line break")
				}
			})
		}
	}
}

func waitForMarker(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second) // only a failure guard
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s did not appear", path)
}

// longCommand puts activitySecret across byte 2000 of the command, where a wrapper-side cut would split it.
func longCommand() string {
	return "#" + strings.Repeat("x", 1986) + activitySecret + " tail"
}

// finishDetails returns the afm.detail of every tool_result line.
func finishDetails(lines []map[string]any) (out []string) {
	for _, l := range lines {
		if b := contentBlock(l); b != nil && b["type"] == "tool_result" {
			d, _ := afmField(l)["detail"].(string)
			out = append(out, d)
		}
	}
	return out
}

// The wrapper cannot redact, so it must not cut the command either: a secret cut in half would no longer match.
func TestOpenAIAgentAsClaude_StreamExtension_FinishDetailIsNotCutBeforeRedaction(t *testing.T) {
	requireAgentShimTools(t)
	fakeCurlDir, _ := writeStatefulFakeCurl(t, []string{toolCallRound("c1", longCommand()), finalRound("ok")}, "200")
	out, err := runAgentScript(t, fakeCurlDir, "go\n", agentExt)
	if err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}
	if got := finishDetails(decodeLines(out)); len(got) != 1 || got[0] != longCommand() {
		t.Errorf("finish details = %q, want the whole command", got)
	}
}

func TestCursorAgentAsClaude_FinishDetailIsNotCutBeforeRedaction(t *testing.T) {
	events := `{"type":"tool_call","subtype":"completed","call_id":"c1","tool_call":{"shellToolCall":{"args":{"command":"` + longCommand() +
		`"},"result":{"success":{"exitCode":0}}}}}` + "\n" + `{"type":"result","subtype":"success","is_error":false,"result":"done"}` + "\n"
	res := newFakeCursor(t, events, 0).run(t, nil)
	if res.exitCode != 0 {
		t.Fatalf("exit %d: %s", res.exitCode, res.stderr)
	}
	if got := finishDetails(decodeLines(res.stdout)); len(got) != 1 || got[0] != longCommand() {
		t.Errorf("finish details = %q, want the whole command", got)
	}
}

// Both wrappers run through the real curl against a server that flushes one chunk and then waits for the test: the
// text must be on the wrapper's stdout while the response is still open.
func TestOpenAIWrappers_StreamExtension_PartialTextShowsWhileTheResponseIsOpen(t *testing.T) {
	requireAgentShimTools(t)
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not available")
	}
	wrappers := map[string]string{"agent": "openai-agent-as-claude.sh", "text": "openai-as-claude.sh"}
	for name, file := range wrappers {
		t.Run(name, func(t *testing.T) {
			gate := make(chan struct{})
			var once sync.Once
			release := func() { once.Do(func() { close(gate) }) }
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				flusher := w.(http.Flusher)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, contentChunk(strings.Repeat("streaming right now ", 6)))
				flusher.Flush()
				select {
				case <-gate:
				case <-r.Context().Done():
					return
				}
				_, _ = io.WriteString(w, `data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\ndata: [DONE]\n")
			}))
			t.Cleanup(srv.Close)
			t.Cleanup(release)

			cmd := exec.Command("bash", repoFile(t, "scripts", file))
			cmd.Env = append(os.Environ(), "AFM_OUTPUT_SCHEMA_FILE=", "OPENAI_API_KEY=test", "OPENAI_BASE_URL="+srv.URL+"/v1",
				"OPENAI_MODEL=m", agentExt)
			cmd.Stdin = strings.NewReader("go\n")
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { release(); _ = cmd.Wait() }()

			seen := make(chan string, 16)
			go func() {
				sc := bufio.NewScanner(stdout)
				for sc.Scan() {
					seen <- sc.Text()
				}
				close(seen)
			}()
			deadline := time.After(10 * time.Second) // only a failure guard: the gate, not time, decides
			for {
				select {
				case l, ok := <-seen:
					if !ok {
						t.Fatal("output ended while the response was still open")
					}
					if strings.Contains(l, "streaming right now") {
						if m := lineOfType(decodeLines(l), "assistant"); len(m) != 1 || !afmPartial(m[0]) {
							t.Fatalf("first text line = %s, want a partial snapshot", l)
						}
						return
					}
				case <-deadline:
					t.Fatal("no snapshot while the response was open: the wrapper waits for the whole response")
				}
			}
		})
	}
}

// A transport error after HTTP 200 must not echo the raw SSE deltas: a secret split across them would reach
// stderr and the result line, where whole-value redaction cannot match it.
func TestOpenAIWrappers_StreamTransportErrorDoesNotEchoSSEDeltas(t *testing.T) {
	requireAgentShimTools(t)
	const head, tail = "sk-live-secret-", "value-123"
	body := contentChunk("token "+head) + contentChunk(tail)
	for _, w := range openAIWrappers {
		t.Run(w.name, func(t *testing.T) {
			fakeDir := t.TempDir()
			resp := filepath.Join(t.TempDir(), "resp")
			if err := os.WriteFile(resp, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			script := "#!/usr/bin/env bash\ncat " + strconv.Quote(resp) + "\nprintf '\\n200'\nexit 18\n"
			if err := os.WriteFile(filepath.Join(fakeDir, "curl"), []byte(script), 0o755); err != nil { //nolint:gosec // test fixture: an executable fake curl
				t.Fatal(err)
			}
			out, err := w.command(t, fakeDir, "OPENAI_API_KEY="+secretKey, StreamExtensionEnv+"=1").CombinedOutput()
			if err == nil {
				t.Fatalf("a transport error must exit non-zero:\n%s", out)
			}
			if !strings.Contains(string(out), "curl exit 18") {
				t.Errorf("curl exit code missing from the diagnostics:\n%s", out)
			}
			for _, part := range []string{head, tail} {
				if strings.Contains(string(out), part) {
					t.Errorf("SSE delta fragment %q leaked:\n%s", part, out)
				}
			}
		})
	}
}

// afmPartial reports whether the line carries afm.partial=true.
func afmPartial(line map[string]any) bool {
	partial, _ := afmField(line)["partial"].(bool)
	return partial
}

// afmComplete reports whether the line carries an explicit afm.partial=false.
func afmComplete(line map[string]any) bool {
	partial, ok := afmField(line)["partial"].(bool)
	return ok && !partial
}

func isErrorResult(line map[string]any) bool {
	isErr, _ := line["is_error"].(bool)
	return isErr
}

// isCleanResult reports whether the line carries an explicit is_error=false.
func isCleanResult(line map[string]any) bool {
	isErr, ok := line["is_error"].(bool)
	return ok && !isErr
}
