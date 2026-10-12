package browser

import (
	"os"
	"strings"
)

// TransportEnvPrefix is the env-var prefix for every managed-browser transport variable.
const TransportEnvPrefix = "AFM_BROWSER_"

// IsTransportEnv reports whether an env entry ("KEY=VALUE") is a managed-browser
// transport variable that must be stripped from non-author child environments.
func IsTransportEnv(kv string) bool {
	return strings.HasPrefix(kv, TransportEnvPrefix)
}

// UnsetTransportVars removes every AFM_BROWSER_* variable from afm's OWN process environment.
func UnsetTransportVars() {
	for _, kv := range os.Environ() {
		name, _, ok := strings.Cut(kv, "=")
		if ok && strings.HasPrefix(name, TransportEnvPrefix) {
			_ = os.Unsetenv(name)
		}
	}
}
