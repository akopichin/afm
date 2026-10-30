package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const stopHookResultLine = `echo '{"type":"result","subtype":"success","is_error":false,"result":"ok"}'`

// unavailableLog records every OnUnavailable call of a StopHookClient.
type unavailableLog struct {
	mu      sync.Mutex
	reasons []StopHookUnavailable
}

func (l *unavailableLog) add(r StopHookUnavailable) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reasons = append(l.reasons, r)
}

func (l *unavailableLog) all() []StopHookUnavailable {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]StopHookUnavailable(nil), l.reasons...)
}

// adapterHook is a probe client for runtime rt whose unavailability is recorded in log.
func adapterHook(rt StopHookRuntime, launcher string, p *stopHookProbe, log *unavailableLog) *StopHookClient {
	c := p.client()
	c.Runtime, c.Launcher, c.OnUnavailable = rt, launcher, log.add
	return c
}

// recordingAgent writes its argv (one per line) and its environment into dir, then ends with a result line.
func recordingAgent(t *testing.T, dir string) (script, argsFile, envFile string) {
	t.Helper()
	argsFile, envFile = filepath.Join(dir, "args.txt"), filepath.Join(dir, "env.txt")
	body := `printf '%s\n' "$@" > ` + argsFile + "\nenv > " + envFile + "\n" + stopHookResultLine + "\n"
	return writeScript(t, dir, "agent.sh", body), argsFile, envFile
}

func fileLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func runAuthor(t *testing.T, cfg Config) string {
	t.Helper()
	var stderr bytes.Buffer
	if err := New(cfg).run(context.Background(), "prompt", "autonomous", &stderr, func(string) {}); err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, stderr.String())
	}
	return stderr.String()
}

var claudeBaseArgs = []string{"--print", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions", "--mcp-config", "m.json"}

// Claude gets the hook as one extra --settings flag: every other argument stays as the caller built it, and the
// CLI itself merges the hook with the user's and the project's own Stop hooks.
func TestStopHookClaude_AddsOnlyASettingsFlagWithTheHook(t *testing.T) {
	const launcher = "/tmp/with space/it's/afm"
	dir := t.TempDir()
	script, argsFile, envFile := recordingAgent(t, dir)
	p := &stopHookProbe{}
	runAuthor(t, Config{Command: script, ExtraArgs: claudeBaseArgs, IdleTimeout: 30 * time.Second, StopHook: adapterHook(StopHookClaude, launcher, p, &unavailableLog{})})

	args := fileLines(t, argsFile)
	if len(args) != len(claudeBaseArgs)+2 || !slices.Equal(args[:len(claudeBaseArgs)], claudeBaseArgs) || args[len(claudeBaseArgs)] != "--settings" {
		t.Fatalf("args = %q, want %q followed by --settings <json>", args, claudeBaseArgs)
	}
	var settings struct {
		Hooks map[string][]struct {
			Hooks []map[string]any `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(args[len(args)-1]), &settings); err != nil {
		t.Fatalf("settings are not JSON: %v", err)
	}
	groups := settings.Hooks["Stop"]
	if len(settings.Hooks) != 1 || len(groups) != 1 || len(groups[0].Hooks) != 1 {
		t.Fatalf("settings = %s, want exactly one Stop hook and nothing else", args[len(args)-1])
	}
	handler := groups[0].Hooks[0]
	if handler["type"] != "command" || handler["command"] != `'/tmp/with space/it'\''s/afm' stop-hook` || handler["timeout"] != float64(stopHookTimeoutSeconds) {
		t.Fatalf("handler = %v", handler)
	}

	env := string(readFileOrFail(t, envFile))
	descriptor := dumpEnvValue(env, StopHookDescriptorEnv)
	if !filepath.IsAbs(descriptor) {
		t.Fatalf("descriptor path %q is not absolute", descriptor)
	}
	if strings.Contains(env, "AFM_STOP_HOOK_CODEX") {
		t.Fatalf("a Claude run got Codex hook env:\n%s", env)
	}
}

// Without a StopHook capability the command line is exactly what the caller built.
func TestStopHookClaude_NoCapabilityLeavesTheArgsAlone(t *testing.T) {
	dir := t.TempDir()
	script, argsFile, _ := recordingAgent(t, dir)
	runAuthor(t, Config{Command: script, ExtraArgs: claudeBaseArgs, IdleTimeout: 30 * time.Second})
	if args := fileLines(t, argsFile); !slices.Equal(args, claudeBaseArgs) {
		t.Fatalf("args = %q, want %q", args, claudeBaseArgs)
	}
}

// A repeated --settings replaces the earlier one in the CLI, so AFM cannot add its own without dropping the
// caller's (or the other way round): the hook stays off, and the reason is reported once.
func TestStopHookClaude_ExistingSettingsFlagIsAConflict(t *testing.T) {
	for name, extra := range map[string][]string{
		"separate": {"--settings", "user.json"},
		"equals":   {"--settings=user.json"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			script, argsFile, envFile := recordingAgent(t, dir)
			p := &stopHookProbe{}
			log := &unavailableLog{}
			args := append(slices.Clone(claudeBaseArgs), extra...)
			runAuthor(t, Config{Command: script, ExtraArgs: args, IdleTimeout: 30 * time.Second, StopHook: adapterHook(StopHookClaude, "/afm", p, log)})

			if got := fileLines(t, argsFile); !slices.Equal(got, args) {
				t.Fatalf("args = %q, want the caller's %q untouched", got, args)
			}
			if env := string(readFileOrFail(t, envFile)); strings.Contains(env, StopHookDescriptorEnv) {
				t.Fatalf("a bridge was opened for a hook that cannot be installed:\n%s", env)
			}
			if p.minted != 0 {
				t.Fatalf("minted %d capabilities, want none", p.minted)
			}
			if got := log.all(); !slices.Equal(got, []StopHookUnavailable{StopHookSettingsConflict}) {
				t.Fatalf("unavailable = %v, want one settings conflict", got)
			}
		})
	}
}

// Codex takes its hook through the shim's environment, never through arguments. The trust hash is of the exact
// definition Codex will compute, so the hook runs without any bypass of Codex's own trust policy.
func TestStopHookCodex_HandsTheDefinitionToTheShimThroughEnv(t *testing.T) {
	const launcher = "/tmp/a&b<c> d/afm"
	dir := t.TempDir()
	script, argsFile, envFile := recordingAgent(t, dir)
	runAuthor(t, Config{Command: script, ExtraArgs: claudeBaseArgs, IdleTimeout: 30 * time.Second,
		StopHook: adapterHook(StopHookCodex, launcher, &stopHookProbe{}, &unavailableLog{})})

	if args := fileLines(t, argsFile); !slices.Equal(args, claudeBaseArgs) {
		t.Fatalf("args = %q, want %q: Codex gets no --settings", args, claudeBaseArgs)
	}
	env := string(readFileOrFail(t, envFile))
	command := `'/tmp/a&b<c> d/afm' stop-hook`
	wantHooks := `[{hooks=[{type="command",command="` + command + `",timeout=30}]}]`
	if got := dumpEnvValue(env, StopHookCodexHooksEnv); got != wantHooks {
		t.Fatalf("%s = %q, want %q", StopHookCodexHooksEnv, got, wantHooks)
	}
	// The canonical JSON of the hook as Codex 0.163.0 hashes it (key order and escaping matter).
	canonical := `{"event_name":"stop","hooks":[{"async":false,"command":"` + command + `","timeout":30,"type":"command"}]}`
	sum := sha256.Sum256([]byte(canonical))
	wantState := `{"/<session-flags>/config.toml:stop:0:0"={trusted_hash="sha256:` + hex.EncodeToString(sum[:]) + `"}}`
	if got := dumpEnvValue(env, StopHookCodexStateEnv); got != wantState {
		t.Fatalf("%s = %q, want %q", StopHookCodexStateEnv, got, wantState)
	}
	if dumpEnvValue(env, StopHookDescriptorEnv) == "" {
		t.Fatal("the shim's hook command cannot find its descriptor")
	}
}

// The hook command line must survive the shell the CLI runs it through, whatever the launcher path holds.
func TestStopHookCommandLine_SurvivesTheShell(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dir with 'quote' and space")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "argv.txt")
	launcher := writeScript(t, dir, "afm", `printf '%s\n' "$@" > `+out+"\n")
	if err := exec.Command("sh", "-c", stopHookCommandLine(launcher)).Run(); err != nil {
		t.Fatalf("sh -c %q: %v", stopHookCommandLine(launcher), err)
	}
	if got := fileLines(t, out); !slices.Equal(got, []string{"stop-hook"}) {
		t.Fatalf("launcher argv = %q, want [stop-hook]", got)
	}
}

// askingAgent is an agent that, once running, lets the test ask its bridge (or not) before it exits with exitCode.
func askingAgent(t *testing.T, dir string, exitCode string) (script, envFile, releaseFile string) {
	t.Helper()
	envFile, releaseFile = filepath.Join(dir, "env.txt"), filepath.Join(dir, "release")
	body := `printf '%s' "$` + StopHookDescriptorEnv + `" > ` + envFile + `.tmp && mv ` + envFile + `.tmp ` + envFile + "\n" +
		"while [ ! -e " + releaseFile + " ]; do sleep 0.05; done\n" + stopHookResultLine + "\nexit " + exitCode + "\n"
	return writeScript(t, dir, "agent.sh", body), envFile, releaseFile
}

// A CLI that ends cleanly without ever asking the hook did not run it (hooks off, hook untrusted, --bare): that is
// reported once, so AFM's own recovery is not mistaken for a working hook. A CLI that did ask is fine.
func TestStopHook_ACleanRunThatNeverAskedIsReported(t *testing.T) {
	for _, tc := range []struct {
		name string
		ask  bool
		exit string
		want []StopHookUnavailable
	}{
		{name: "never asked", ask: false, exit: "0", want: []StopHookUnavailable{StopHookNotAsked}},
		{name: "asked", ask: true, exit: "0"},
		{name: "failed run is not evidence", ask: false, exit: "3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			script, envFile, releaseFile := askingAgent(t, dir, tc.exit)
			p := &stopHookProbe{}
			log := &unavailableLog{}
			e := New(Config{Command: script, IdleTimeout: 30 * time.Second, StopHook: adapterHook(StopHookClaude, "/afm", p, log)})

			done := make(chan error, 1)
			go func() { done <- e.run(context.Background(), "prompt", "autonomous", &bytes.Buffer{}, func(string) {}) }()
			descriptor := string(waitForFile(t, envFile))
			if tc.ask {
				if _, err := RunStopHook(context.Background(), descriptor, strings.NewReader(`{}`)); err != nil {
					t.Fatalf("RunStopHook: %v", err)
				}
			}
			if err := os.WriteFile(releaseFile, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			<-done
			if got := log.all(); !slices.Equal(got, tc.want) {
				t.Fatalf("unavailable = %v, want %v", got, tc.want)
			}
		})
	}
}

// A bridge that cannot open is reported as such (the agent still runs, see the existing test).
func TestStopHook_BridgeFailureIsReportedOnce(t *testing.T) {
	dir := t.TempDir()
	script, _, _ := recordingAgent(t, dir)
	log := &unavailableLog{}
	hook := &StopHookClient{
		Runtime: StopHookClaude, Launcher: "/afm", OnUnavailable: log.add,
		Mint: func() (func(StopRequest) StopDecision, func()) { return nil, nil },
	}
	runAuthor(t, Config{Command: script, IdleTimeout: 30 * time.Second, StopHook: hook})
	if got := log.all(); !slices.Equal(got, []StopHookUnavailable{StopHookBridgeFailed}) {
		t.Fatalf("unavailable = %v, want one bridge failure", got)
	}
}

// Verify and helper runs never carry the hook, so they never report it unavailable either.
func TestStopHook_IsolatedRunsNeverReportUnavailable(t *testing.T) {
	for name, cfg := range map[string]Config{"verify": {VerifyMode: true}, "helper": {runMode: ModeHelper}} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			script, argsFile, _ := recordingAgent(t, dir)
			log := &unavailableLog{}
			cfg.Command, cfg.ExtraArgs, cfg.IdleTimeout = script, claudeBaseArgs, 30*time.Second
			cfg.StopHook = adapterHook(StopHookClaude, "/afm", &stopHookProbe{}, log)
			runAuthor(t, cfg)
			if got := fileLines(t, argsFile); !slices.Equal(got, claudeBaseArgs) {
				t.Fatalf("args = %q, want the caller's", got)
			}
			if got := log.all(); len(got) != 0 {
				t.Fatalf("unavailable = %v", got)
			}
		})
	}
}

func readFileOrFail(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// envValue returns the value of name in an `env` dump ("" when absent). Values here are single-line.
func dumpEnvValue(dump, name string) string {
	for _, line := range strings.Split(dump, "\n") {
		if v, ok := strings.CutPrefix(line, name+"="); ok {
			return v
		}
	}
	return ""
}
