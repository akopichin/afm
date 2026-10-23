package config_test

import (
	"testing"

	"github.com/akopichin/afm/pkg/config"
)

func TestUsageLimitProvider(t *testing.T) {
	cfg := config.Config{
		Docker: config.DockerConfig{
			Agents: map[string]config.AgentRecipe{
				"team-a-codex":     {Type: config.RecipeTypeCodex},
				"team-b-codex":     {Type: config.RecipeTypeCodex},
				"my-claude":        {Type: config.ClaudeCommand},
				"gpt-chat":         {Type: config.RecipeTypeOpenAI},
				"untyped":          {},
				"codex-as-claude":  {Type: config.RecipeTypeOpenAI},
				"claude-via-proxy": {Type: config.RecipeTypeOpenAIAgent},
			},
		},
	}

	tests := []struct {
		name string
		cmd  string
		want string
	}{
		{"empty is claude", "", "claude"},
		{"bare claude", "claude", "claude"},
		{"bare codex shim is codex", "codex-as-claude", "openai"}, // recipe с тем же именем перекрывает имя команды
		{"codex recipe a", "team-a-codex", "codex"},
		{"codex recipe b shares provider with a", "team-b-codex", "codex"},
		{"claude recipe", "my-claude", "claude"},
		{"openai recipe", "gpt-chat", "openai"},
		{"openai-agent recipe", "claude-via-proxy", "openai-agent"},
		{"recipe without type falls back to its name", "untyped", "untyped"},
		{"unknown command keeps its own name", "my-agent", "my-agent"},
		{"another unknown command does not share it", "other-agent", "other-agent"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := cfg.UsageLimitProvider(tc.cmd); got != tc.want {
				t.Errorf("UsageLimitProvider(%q) = %q, want %q", tc.cmd, got, tc.want)
			}
		})
	}
}

func TestUsageLimitProvider_BareCodexShimWithoutRecipe(t *testing.T) {
	var cfg config.Config
	if got := cfg.UsageLimitProvider("codex-as-claude"); got != config.RecipeTypeCodex {
		t.Errorf("bare codex-as-claude provider = %q, want %q", got, config.RecipeTypeCodex)
	}
}
