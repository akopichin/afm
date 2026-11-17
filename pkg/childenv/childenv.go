// Package childenv filters the environment afm hands to child processes it starts.
package childenv

import (
	"slices"
	"strings"

	"github.com/akopichin/afm/contracts/browser"
	"github.com/akopichin/afm/contracts/config"
	"github.com/akopichin/afm/contracts/lifecyclehooks"
)

// runScopedNames are set by afm for one agent or hook invocation; a new afm process must not inherit them.
var runScopedNames = []string{
	"AFM_IN_DOCKER", // a leaked flag makes the child believe it runs in a container
	"AFM_STAGE_DIR", "AFM_STAGE_ID", "AFM_STAGE_NAME", "AFM_STAGE_FROM", "AFM_STAGE_TO",
	"AFM_DIALOG_PHASE", "AFM_SCRATCH_DIR", "AFM_OUTPUT_SCHEMA_FILE", "AFM_STREAM_EXTENSION",
	"AFM_RUN_DIR", "AFM_ROOT_DIR", "AFM_FLOW_CYCLE", "AFM_PIPELINE_DIR",
}

// runScopedPrefixes cover families of per-invocation descriptors and event fields.
var runScopedPrefixes = []string{"AFM_MESH_", "AFM_STOP_HOOK_", "AFM_HOOK_"}

// StripTransport drops afm's internal secret transport variables, which child tools and their credential or
// ssh helpers must not inherit.
func StripTransport(env []string) []string {
	return filter(env, isTransport)
}

// ForAFMChild prepares the environment of a new afm process: no transport secrets and nothing that belongs to
// a run or agent invocation of the parent.
func ForAFMChild(env []string) []string {
	return filter(env, func(kv string) bool { return isTransport(kv) || isRunScoped(kv) })
}

func filter(env []string, drop func(kv string) bool) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if !drop(kv) {
			out = append(out, kv)
		}
	}
	return out
}

func isTransport(kv string) bool {
	return strings.HasPrefix(kv, config.SecretEnvPrefix) || strings.HasPrefix(kv, config.SyspromptEnvPrefix) ||
		strings.HasPrefix(kv, lifecyclehooks.HookSecretTransportPrefix) || browser.IsTransportEnv(kv)
}

func isRunScoped(kv string) bool {
	name, _, _ := strings.Cut(kv, "=")
	if slices.Contains(runScopedNames, name) {
		return true
	}
	return slices.ContainsFunc(runScopedPrefixes, func(p string) bool { return strings.HasPrefix(name, p) })
}
