package browser

import "strings"

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
