package serve

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDesktopRuntimeChecksHostAndImage(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", home)
	t.Setenv("AFM_IN_DOCKER", "")
	t.Setenv("AFM_USE_DOCKER", "0")
	t.Setenv("AFM_DESKTOP_RUNTIME_IMAGE", "afm:v3.1.17")
	t.Setenv("AFM_DOCKER_IMAGE", "custom:old")
	g := Gateway{cfg: GatewayConfig{Version: "3.1.17"}}
	check := func(want string, in runtimeRequest) runtimeStatus {
		t.Helper()
		s, err := g.dockerRuntime(context.Background(), in)
		if err != nil || s.State != want {
			t.Fatalf("want %s, got %+v, %v", want, s, err)
		}
		return s
	}
	check("native", runtimeRequest{})
	t.Setenv("AFM_USE_DOCKER", "1")
	check("cli", runtimeRequest{})
	docker := filepath.Join(home, "docker")
	script := `#!/bin/sh
case "$1" in
info) [ "$ENGINE" = on ] && echo 29.9.3 ;;
image) [ "$MISSING" != yes ] || [ -f "$HOME/pulled" ] ;;
pull) echo "$3" > "$HOME/pulled" ;;
run) case " $* " in *" custom:old "*) echo "afm version v3.1.8";; *) echo "afm version 3.1.17";; esac ;;
esac
`
	if err := os.WriteFile(docker, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENGINE", "off")
	check("engine", runtimeRequest{})
	t.Setenv("ENGINE", "on")
	t.Setenv("MISSING", "yes")
	check("missing", runtimeRequest{})
	if _, err := os.Stat(filepath.Join(home, "pulled")); !os.IsNotExist(err) {
		t.Fatal("check downloaded an image")
	}
	s := check("ready", runtimeRequest{Pull: true, MatchingImage: true})
	if s.Image != "afm:v3.1.17" || s.CLI != docker {
		t.Fatal(s)
	}
	check("mismatch", runtimeRequest{})
}
