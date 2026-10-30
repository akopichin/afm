package executor

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
)

// settingsFlag is the Claude CLI flag that takes the per-run settings file.
const settingsFlag = "--settings"

// StopHookRuntime is the CLI family a Stop hook is installed into; each takes its hook definition differently.
type StopHookRuntime string

const (
	StopHookClaude StopHookRuntime = "claude" // Claude Code and the wrappers that exec it (GLM recipes)
	StopHookCodex  StopHookRuntime = "codex"  // codex-as-claude, which hands the hook to the real `codex exec`
)

// StopHookUnavailable says why a hook that was meant to run does not. The codes are fixed, so a notice built from
// one never carries raw CLI output.
type StopHookUnavailable string

const (
	StopHookSettingsConflict StopHookUnavailable = "settings_conflict" // the caller's own --settings would be replaced
	StopHookBridgeFailed     StopHookUnavailable = "bridge_failed"     // the loopback bridge could not open
	StopHookNotAsked         StopHookUnavailable = "not_asked"         // the CLI ended cleanly without running the hook
)

const (
	// StopHookCodexHooksEnv and StopHookCodexStateEnv carry the TOML values of the two `-c` overrides the shim
	// passes to `codex exec`: the hook itself and the trust of exactly that hook.
	StopHookCodexHooksEnv = "AFM_STOP_HOOK_CODEX_HOOKS"
	StopHookCodexStateEnv = "AFM_STOP_HOOK_CODEX_STATE"

	// stopHookTimeoutSeconds bounds one hook command; it is part of the definition Codex hashes for trust.
	stopHookTimeoutSeconds = 30

	// codexSessionHookKey is how Codex 0.163.0 names the only Stop hook of the session-flags layer.
	codexSessionHookKey = "/<session-flags>/config.toml:stop:0:0"
)

// stopHookCommandLine is the command a CLI runs for the hook. The launcher is absolute and quoted for the shell
// the CLI uses; the changing part (the descriptor) travels in the environment, so the line itself stays stable.
func stopHookCommandLine(launcher string) string {
	return "'" + strings.ReplaceAll(launcher, "'", `'\''`) + "' stop-hook"
}

// claudeStopHookSettings is the --settings value that adds the hook; Claude merges it with the Stop hooks of the
// user's and the project's own settings.
func claudeStopHookSettings(command string) (string, error) {
	type handler struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
	}
	type group struct {
		Hooks []handler `json:"hooks"`
	}
	settings := map[string]map[string][]group{"hooks": {"Stop": {{Hooks: []handler{{Type: "command", Command: command, Timeout: stopHookTimeoutSeconds}}}}}}
	data, err := json.Marshal(settings)
	return string(data), err
}

// hasSettingsFlag reports whether the caller already passes --settings, which the CLI would let a second one replace.
func hasSettingsFlag(args []string) bool {
	return slices.ContainsFunc(args, func(a string) bool {
		return a == settingsFlag || strings.HasPrefix(a, settingsFlag+"=")
	})
}

// codexStopHookEnv returns the two TOML override values for `codex exec -c`. Codex runs a non-managed hook only
// when its exact definition is trusted; the trust is given for this one definition alone, per invocation, so
// the user's config, CODEX_HOME and the trust of other hooks stay as they are.
func codexStopHookEnv(command string) ([]string, error) {
	quoted, err := encodeJSON(command)
	if err != nil {
		return nil, err
	}
	// The exact JSON Codex hashes: sorted keys, a stop hook with one command handler.
	type handler struct {
		Async   bool   `json:"async"`
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
		Type    string `json:"type"`
	}
	identity := struct {
		EventName string    `json:"event_name"`
		Hooks     []handler `json:"hooks"`
	}{EventName: "stop", Hooks: []handler{{Async: false, Command: command, Timeout: stopHookTimeoutSeconds, Type: "command"}}}
	canonical, err := encodeJSON(identity)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(canonical))
	hooks := fmt.Sprintf(`[{hooks=[{type="command",command=%s,timeout=%d}]}]`, quoted, stopHookTimeoutSeconds)
	state := fmt.Sprintf(`{%q={trusted_hash="sha256:%s"}}`, codexSessionHookKey, hex.EncodeToString(sum[:]))
	return []string{StopHookCodexHooksEnv + "=" + hooks, StopHookCodexStateEnv + "=" + state}, nil
}

// encodeJSON writes v as compact JSON without HTML escaping: Codex hashes the characters as they are, and a JSON
// string is also a valid TOML basic string.
func encodeJSON(v any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

// stopHookRun is the Stop hook of one process invocation: what it adds to the command line and the environment,
// and, when the process ends, whether the CLI ever used it. The zero value is "no hook".
type stopHookRun struct {
	client *StopHookClient
	args   []string
	env    []string
	opened *openedStopHook
}

// startStopHook installs the hook for an author run, or reports why it cannot be: AFM's own recovery then stays
// the only guard and the agent runs unchanged. args are the command line built so far.
func (e *Executor) startStopHook(args []string, stderr io.Writer) stopHookRun {
	client := e.cfg.StopHook
	if client == nil || e.cfg.effectiveMode().isolated() {
		return stopHookRun{}
	}
	var hookArgs, hookEnv []string
	command := stopHookCommandLine(client.Launcher)
	switch client.Runtime {
	case StopHookClaude:
		if hasSettingsFlag(args) {
			client.unavailable(StopHookSettingsConflict)
			return stopHookRun{}
		}
		settings, err := claudeStopHookSettings(command)
		if err != nil {
			client.unavailable(StopHookBridgeFailed)
			return stopHookRun{}
		}
		hookArgs = []string{settingsFlag, settings}
	case StopHookCodex:
		env, err := codexStopHookEnv(command)
		if err != nil {
			client.unavailable(StopHookBridgeFailed)
			return stopHookRun{}
		}
		hookEnv = env
	default:
		// An unknown runtime gets no CLI-specific definition; the bridge below still serves the descriptor.
	}
	opened, err := e.setupStopHook()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "afm: stop hook unavailable: %v\n", err)
		client.unavailable(StopHookBridgeFailed)
		return stopHookRun{}
	}
	return stopHookRun{client: client, args: hookArgs, env: append(opened.env, hookEnv...), opened: opened}
}

func (c *StopHookClient) unavailable(reason StopHookUnavailable) {
	if c.OnUnavailable != nil {
		c.OnUnavailable(reason)
	}
}

// finish closes the bridge. A CLI that ended cleanly without once asking the hook did not run it (hooks switched
// off, the hook untrusted, --bare): that is reported, so AFM's recovery is not mistaken for a working hook.
func (h stopHookRun) finish(cleanExit bool) {
	if h.opened == nil {
		return
	}
	h.opened.cleanup()
	if cleanExit && !h.opened.asked.Load() {
		h.client.unavailable(StopHookNotAsked)
	}
}
