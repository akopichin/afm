package browser

import (
	"os"
	"testing"
)

func TestIsTransportEnv(t *testing.T) {
	cases := map[string]bool{
		"AFM_BROWSER_TRANSPORT_TOKEN=x": true,
		"AFM_BROWSER_ENDPOINT=host:1":   true,
		"AFM_BROWSER_APP_URL=http://x":  true,
		"AFM_STAGE_DIR=/x":              false,
		"PATH=/usr/bin":                 false,
	}
	for kv, want := range cases {
		if got := IsTransportEnv(kv); got != want {
			t.Fatalf("IsTransportEnv(%q) = %v, want %v", kv, got, want)
		}
	}
}

// TestUnsetTransportVars проверяет, что из окружения afm удаляются ВСЕ
// AFM_BROWSER_* (включая секретный токен), а посторонние переменные остаются —
// иначе прямой спавн (например git из memory.Commit) унаследовал бы токен.
func TestUnsetTransportVars(t *testing.T) {
	t.Setenv("AFM_BROWSER_TRANSPORT_TOKEN", "secret")
	t.Setenv("AFM_BROWSER_ENDPOINT", "host.docker.internal:1234")
	t.Setenv("AFM_BROWSER_APP_URL", "http://127.0.0.1:1")
	t.Setenv("AFM_STAGE_DIR", "/keep/me")

	UnsetTransportVars()

	for _, name := range []string{"AFM_BROWSER_TRANSPORT_TOKEN", "AFM_BROWSER_ENDPOINT", "AFM_BROWSER_APP_URL"} {
		if v, ok := os.LookupEnv(name); ok {
			t.Fatalf("%s must be unset, still present as %q", name, v)
		}
	}
	if os.Getenv("AFM_STAGE_DIR") != "/keep/me" {
		t.Fatal("a non-transport variable must not be removed")
	}
}
