package browser

import (
	"os"
	"strings"
)

// TransportEnvPrefix is the env-var prefix for every managed-browser transport
// variable (AFM_BROWSER_*). It is the SINGLE source for the default-deny strip
// applied to every child-process environment — the executor author/verify/JSON
// paths, shell-verify, and lifecycle hooks all reference this one place rather
// than re-inlining the literal.
const TransportEnvPrefix = "AFM_BROWSER_"

// IsTransportEnv reports whether an env entry ("KEY=VALUE") is a managed-browser
// transport variable that must be stripped from non-author child environments.
func IsTransportEnv(kv string) bool {
	return strings.HasPrefix(kv, TransportEnvPrefix)
}

// UnsetTransportVars removes every AFM_BROWSER_* variable (including the secret
// transport token) from afm's OWN process environment. It mirrors
// lifecyclehooks.UnsetTransportVars and is called in the container once the
// descriptor has been rebuilt from the transport env: after that, no child —
// including direct, non-executor spawns such as `git` run by memory.Commit (which
// could trigger project hooks) — should inherit the token. The per-executor env
// filter (IsTransportEnv) still covers agent spawns; this closes the direct-spawn
// path at the single point where the transport is consumed.
func UnsetTransportVars() {
	for _, kv := range os.Environ() {
		name, _, ok := strings.Cut(kv, "=")
		if ok && strings.HasPrefix(name, TransportEnvPrefix) {
			_ = os.Unsetenv(name)
		}
	}
}
