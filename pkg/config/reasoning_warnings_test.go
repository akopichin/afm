package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/config"
)

func TestReasoningWarningsNoneForCanonical(t *testing.T) {
	c := config.Config{
		Client: config.ClientConfig{Reasoning: "high"},
		Docker: config.DockerConfig{Agents: map[string]config.AgentRecipe{
			"glm": {Type: "claude", Reasoning: "max"},
			"cdx": {Type: "codex", Reasoning: "xhigh"},
		}},
	}
	if w := c.ReasoningWarnings(); len(w) != 0 {
		t.Fatalf("expected no warnings, got %v", w)
	}
}

func TestReasoningWarningsNonCanonicalClient(t *testing.T) {
	c := config.Config{Client: config.ClientConfig{Reasoning: "turbo"}}
	w := c.ReasoningWarnings()
	if len(w) != 1 {
		t.Fatalf("expected 1 warning, got %v", w)
	}
	if !strings.Contains(w[0], "client") || !strings.Contains(w[0], "turbo") {
		t.Errorf("unexpected warning: %q", w[0])
	}
}

func TestReasoningWarningsNonCanonicalRecipe(t *testing.T) {
	c := config.Config{Docker: config.DockerConfig{Agents: map[string]config.AgentRecipe{
		"glm": {Type: "openai", Reasoning: "turbo"},
	}}}
	w := c.ReasoningWarnings()
	if len(w) != 1 {
		t.Fatalf("expected 1 warning, got %v", w)
	}
	if !strings.Contains(w[0], "docker.agents.glm") || !strings.Contains(w[0], "turbo") {
		t.Errorf("unexpected warning: %q", w[0])
	}
}

func TestReasoningWarningsCursorIgnored(t *testing.T) {
	c := config.Config{Docker: config.DockerConfig{Agents: map[string]config.AgentRecipe{
		"cur": {Type: "cursor", Reasoning: "high"}, // canonical value, but cursor has no mechanism
	}}}
	w := c.ReasoningWarnings()
	if len(w) != 1 {
		t.Fatalf("expected 1 warning, got %v", w)
	}
	if !strings.Contains(w[0], "docker.agents.cur") || !strings.Contains(w[0], "cursor") {
		t.Errorf("unexpected warning: %q", w[0])
	}
}

func TestReasoningWarningsEmptyIsSilent(t *testing.T) {
	c := config.Config{
		Client: config.ClientConfig{},
		Docker: config.DockerConfig{Agents: map[string]config.AgentRecipe{"glm": {Type: "codex"}}},
	}
	if w := c.ReasoningWarnings(); len(w) != 0 {
		t.Fatalf("expected no warnings for empty reasoning, got %v", w)
	}
}

func writeConfigYAML(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func TestClientReasoningRoundTrip(t *testing.T) {
	dir := t.TempDir()
	writeConfigYAML(t, dir, "client:\n  reasoning: high\ndocker:\n  agents:\n    glm:\n      type: codex\n      reasoning: xhigh\n")
	cfg, err := config.LoadFrom("", dir)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if cfg.Client.Reasoning != "high" {
		t.Errorf("client.reasoning = %q, want high", cfg.Client.Reasoning)
	}
	if got := cfg.Docker.Agents["glm"].Reasoning; got != "xhigh" {
		t.Errorf("recipe reasoning = %q, want xhigh", got)
	}
}

func TestClientReasoningOverlayMerge(t *testing.T) {
	globalDir := t.TempDir()
	projectDir := t.TempDir()
	writeConfigYAML(t, globalDir, "client:\n  reasoning: low\ndocker:\n  agents:\n    glm:\n      type: codex\n      reasoning: high\n")
	writeConfigYAML(t, projectDir, "client:\n  reasoning: max\n")
	cfg, err := config.LoadFrom(globalDir, projectDir)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if cfg.Client.Reasoning != "max" {
		t.Errorf("client.reasoning after overlay = %q, want max", cfg.Client.Reasoning)
	}
	// The recipe (from the global layer) must survive the docker.agents keyed merge.
	if got := cfg.Docker.Agents["glm"].Reasoning; got != "high" {
		t.Errorf("recipe reasoning after overlay = %q, want high", got)
	}
}
