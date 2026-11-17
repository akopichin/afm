package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/serve"
)

// isolatedHome keeps the host's state and registry away from the real ~/.afm.
func isolatedHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

func runServe(ctx context.Context, args ...string) error {
	cmd := newServeCmd()
	cmd.SetContext(ctx)
	cmd.SetArgs(args)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	return cmd.Execute()
}

func TestServeRefusesASecondHost(t *testing.T) {
	home := isolatedHome(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runServe(ctx, "--port", "0") }()
	t.Cleanup(func() { cancel(); <-done })

	infoPath := filepath.Join(home, ".afm", "serve", "host.json")
	deadline := time.Now().Add(10 * time.Second)
	for {
		if data, _ := os.ReadFile(infoPath); strings.Contains(string(data), "endpoint") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the first host never published its endpoint")
		}
		time.Sleep(20 * time.Millisecond)
	}

	err := runServe(context.Background(), "--port", "0")
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 1 {
		t.Fatalf("second host error = %v, want exit code 1", err)
	}
}

func TestServeRejectsBadFlagsAndConfig(t *testing.T) {
	home := isolatedHome(t)
	if err := runServe(context.Background(), "--mcp-allow", "read,runs"); err == nil || !strings.Contains(err.Error(), "--mcp-allow") {
		t.Errorf("read with runs: %v", err)
	}
	bad := filepath.Join(home, "serve.yaml")
	if err := os.WriteFile(bad, []byte("limits:\n  max_running: 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runServe(context.Background(), "--config", bad); err == nil || !strings.Contains(err.Error(), "max_running") {
		t.Errorf("invalid config: %v", err)
	}
	// A refused start must not leave state behind.
	if _, err := os.Stat(filepath.Join(home, ".afm", "serve")); err == nil {
		t.Error("a host that failed validation created its state directory")
	}
}

func TestServeConfigPathDefaultsToTheUsersAfmDir(t *testing.T) {
	home := isolatedHome(t)
	if got, want := serveConfigPath(""), filepath.Join(home, ".afm", "serve.yaml"); got != want {
		t.Errorf("serveConfigPath = %q, want %q", got, want)
	}
	if got := serveConfigPath("/etc/serve.yaml"); got != "/etc/serve.yaml" {
		t.Errorf("explicit path = %q", got)
	}
	if serve.DefaultPort != 8100 {
		t.Errorf("default port = %d", serve.DefaultPort)
	}
}
