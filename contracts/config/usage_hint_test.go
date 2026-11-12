package config_test

import (
	"testing"

	"github.com/akopichin/afm/contracts/accounting"
	"github.com/akopichin/afm/contracts/config"
)

func TestUsageHint(t *testing.T) {
	cfg := config.Config{Docker: config.DockerConfig{Agents: map[string]config.AgentRecipe{
		"glm": {Type: config.ClaudeCommand, Model: "glm-5.3"},
	}}}
	if got, want := cfg.UsageHint("glm"), (accounting.UsageHint{Channel: config.ClaudeCommand, Model: "glm-5.3"}); got != want {
		t.Errorf("recipe hint = %+v, want %+v", got, want)
	}
	if got := cfg.UsageHint("unknown-cmd"); got != (accounting.UsageHint{}) {
		t.Errorf("no recipe must give zero hint, got %+v", got)
	}
}
