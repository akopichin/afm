package docker

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/config"
)

// recordingClaudeOnPATH installs a claude that records its argv (one per line) and environment, and returns the
// two files it writes.
func recordingClaudeOnPATH(t *testing.T) (argsFile, envFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile, envFile = filepath.Join(dir, "args.txt"), filepath.Join(dir, "env.txt")
	body := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\nenv > " + envFile + "\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	return argsFile, envFile
}

// A Claude recipe wrapper hands the caller's arguments on as they are (the hook's --settings JSON stays one
// argument) and leaves the descriptor path in the environment: inside AFM's own re-exec nothing crosses Docker.
func TestClaudeWrapper_KeepsTheStopHookSettingsAndDescriptor(t *testing.T) {
	argsFile, envFile := recordingClaudeOnPATH(t)
	dir, err := CreateWrappers([]WrapperSpec{{Command: "glm", Model: "glm-5", AuthTo: "ANTHROPIC_AUTH_TOKEN", Bare: true}})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup(dir)

	settings := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"'/opt/with space/afm' stop-hook","timeout":30}]}]}}`
	callerArgs := []string{"--print", "--output-format", "stream-json", "--settings", settings}
	cmd := exec.Command(filepath.Join(dir, "glm"), callerArgs...)
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "AFM_SECRET_GLM=glm-secret", "AFM_STOP_HOOK_DESCRIPTOR=/tmp/afm stop hook/descriptor.json")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("wrapper: %v\n%s", err, out)
	}

	got := strings.Split(strings.TrimSuffix(string(mustRead(t, argsFile)), "\n"), "\n")
	if !slices.Equal(got[:len(callerArgs)], callerArgs) {
		t.Fatalf("args = %q, want the caller's %q first", got, callerArgs)
	}
	if n := strings.Count(strings.Join(got, "\n"), "--settings"); n != 1 {
		t.Fatalf("args = %q: --settings appears %d times, the wrapper must not add one", got, n)
	}
	env := string(mustRead(t, envFile))
	if !strings.Contains(env, "AFM_STOP_HOOK_DESCRIPTOR=/tmp/afm stop hook/descriptor.json\n") {
		t.Fatalf("the descriptor path did not reach the CLI:\n%s", env)
	}
	if strings.Contains(env, "AFM_SECRET_GLM=") {
		t.Fatalf("the wrapper must drop its transport secret from the CLI's environment:\n%s", env)
	}
}

// The Codex wrapper execs the shim with the caller's environment and arguments; it neither sets nor unsets any
// hook variable, so the hook definition afm computed in the same container reaches `codex exec` through the shim.
func TestCodexWrapper_LeavesTheStopHookEnvironmentAlone(t *testing.T) {
	stubCodexOnPATH(t)
	dir, err := CreateWrappers([]WrapperSpec{{Type: config.RecipeTypeCodex, Command: "codex", Model: "gpt-5.1-codex"}})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup(dir)
	script := string(mustRead(t, filepath.Join(dir, "codex")))
	if strings.Contains(script, "AFM_STOP_HOOK") {
		t.Fatalf("the Codex wrapper must not touch the hook environment:\n%s", script)
	}
	if !strings.Contains(script, `exec /usr/local/bin/codex-as-claude "$@"`) {
		t.Fatalf("the wrapper must pass its arguments to the shim:\n%s", script)
	}
}
