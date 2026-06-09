package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
)

func writeYAML(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return p
}

const defaultCommand = "claude"

func TestDefaultConfig(t *testing.T) {
	cfg := config.Default()
	if cfg.Client.Command != defaultCommand {
		t.Errorf("default command: got %q want %q", cfg.Client.Command, "claude")
	}
	if cfg.Executor.IdleTimeout != 30*time.Minute {
		t.Errorf("default idle timeout: got %v", cfg.Executor.IdleTimeout)
	}
	if cfg.Executor.MaxParallel != 0 {
		t.Errorf("default max_parallel: got %d", cfg.Executor.MaxParallel)
	}
}

func TestLoadProjectOverridesGlobal(t *testing.T) {
	globalDir := t.TempDir()
	projectDir := t.TempDir()

	writeYAML(t, globalDir, "config.yaml", `
client:
  command: claude
executor:
  idle_timeout: 10m
`)
	writeYAML(t, projectDir, "config.yaml", `
client:
  command: gemini
`)

	cfg, err := config.LoadFrom(globalDir, projectDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Client.Command != "gemini" {
		t.Errorf("project should override command: got %q", cfg.Client.Command)
	}
	if cfg.Executor.IdleTimeout != 10*time.Minute {
		t.Errorf("global idle_timeout should carry over: got %v", cfg.Executor.IdleTimeout)
	}
}

func TestLoadMissingFiles(t *testing.T) {
	cfg, err := config.LoadFrom("/nonexistent", "/also/nonexistent")
	if err != nil {
		t.Fatalf("missing config files should not error: %v", err)
	}
	if cfg.Client.Command != defaultCommand {
		t.Errorf("should fall back to defaults: got %q", cfg.Client.Command)
	}
}

func TestServerConfigDefaults(t *testing.T) {
	cfg := config.Default()
	if cfg.Server.GetPort() != 9876 {
		t.Errorf("default port: got %d, want 9876", cfg.Server.GetPort())
	}
	if !cfg.Server.IsOpenBrowser() {
		t.Error("default open_browser should be true")
	}
}

func TestServerConfigOverride(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, dir, "config.yaml", "server:\n  port: 8080\n  open_browser: false\n")

	cfg, err := config.LoadFrom("", dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.GetPort() != 8080 {
		t.Errorf("port: got %d, want 8080", cfg.Server.GetPort())
	}
	if cfg.Server.IsOpenBrowser() {
		t.Error("open_browser should be false")
	}
}

func TestServerPortZeroDisablesServer(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, dir, "config.yaml", "server:\n  port: 0\n")

	cfg, err := config.LoadFrom("", dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.GetPort() != 0 {
		t.Errorf("port should be 0 when explicitly set: got %d", cfg.Server.GetPort())
	}
}
