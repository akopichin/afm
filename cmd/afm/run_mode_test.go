package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/contracts/config"
)

func TestParseLaunchMode(t *testing.T) {
	tests := []struct {
		in      string
		want    launchMode
		wantErr string
	}{
		{in: "", want: launchDashboard},
		{in: "dashboard", want: launchDashboard},
		{in: "core", want: launchCore},
		{in: "bogus", wantErr: `--mode "bogus": want dashboard or core`},
		{in: "Core", wantErr: `--mode "Core": want dashboard or core`},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := parseLaunchMode(tt.in)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("parseLaunchMode(%q) error = %v, want %q", tt.in, err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("parseLaunchMode(%q) = %v, %v; want %v", tt.in, got, err, tt.want)
			}
		})
	}
}

func TestLaunchModeValidate(t *testing.T) {
	cfgWithPort := func(port int) config.Config {
		return config.Config{Server: config.ServerConfig{Port: &port}}
	}
	const needsAPI = "--mode core needs the HTTP API: set a positive --port or server.port; 0 disables it"

	if err := launchCore.validate(cfgWithPort(0)); err == nil || err.Error() != needsAPI {
		t.Errorf("core with port 0: error = %v, want %q", err, needsAPI)
	}
	if err := launchCore.validate(cfgWithPort(8080)); err != nil {
		t.Errorf("core with port 8080: %v", err)
	}
	if err := launchDashboard.validate(cfgWithPort(0)); err != nil {
		t.Errorf("dashboard with port 0 must stay allowed: %v", err)
	}
}

// A refused mode must stop before anything is created: no run directory and no stage side effects.
func TestRunMain_RefusedModeCreatesNothing(t *testing.T) {
	tests := []struct {
		name string
		args []string
		cfg  string
		want string // часть сообщения об отказе режимом, а не любым другим препятствием
	}{
		{name: "unknown mode", args: []string{"--mode", "bogus"}, want: `--mode "bogus": want dashboard or core`},
		{name: "core with --port 0", args: []string{"--mode", "core", "--port", "0"}, want: "--mode core needs the HTTP API"},
		{name: "core with server.port 0 in config", args: []string{"--mode", "core"}, cfg: "server:\n  port: 0\n", want: "--mode core needs the HTTP API"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newCycleFixture(t)
			t.Setenv("HOME", t.TempDir())
			// Окружение процесса не должно подменить причину отказа: AFM_EXIT_ON_FAIL проверяется раньше режима.
			t.Setenv("AFM_EXIT_ON_FAIL", "")
			t.Setenv("AFM_DIR", "")
			t.Setenv("AFM_WORKTREES", "0")
			t.Setenv("AFM_USE_DOCKER", "0")
			t.Setenv("AFM_BROWSER", "0")
			fx.write("flow.yaml", fx.flowYAML(flowSpec{}))
			if tt.cfg != "" {
				if err := os.MkdirAll(filepath.Join(fx.root, ".afm"), 0o755); err != nil {
					t.Fatal(err)
				}
				fx.write(filepath.Join(".afm", "config.yaml"), tt.cfg)
			}

			args := append([]string{"run", fx.flowPath, "--dir", fx.root}, tt.args...)
			var code int
			_, stderr := captureOutput(t, func() { code = runMain(args) })
			if code == 0 {
				t.Fatal("runMain exit code = 0, want non-zero")
			}
			if !strings.Contains(stderr, tt.want) {
				t.Fatalf("stderr = %q, want the mode refusal %q", stderr, tt.want)
			}
			if _, err := os.Stat(filepath.Join(fx.root, ".afm", "runs")); !os.IsNotExist(err) {
				t.Errorf("runs directory exists after refused mode (stat err = %v)", err)
			}
			if _, err := os.Stat(fx.tickLog); !os.IsNotExist(err) {
				t.Errorf("stage script ran: tick marker exists (stat err = %v)", err)
			}
		})
	}
}
