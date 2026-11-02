package config_test

import (
	"testing"

	"github.com/akopichin/afm/pkg/config"
)

func TestCommandEnvName(t *testing.T) {
	cases := map[string]string{
		"glm51":                "GLM51",
		"deepseek-v4":          "DEEPSEEK_V4",
		"ai-free.claude-glm":   "AI_FREE_CLAUDE_GLM",
		"/abs/my-as-claude.sh": "_ABS_MY_AS_CLAUDE_SH",
		"":                     "",
	}
	for in, want := range cases {
		if got := config.CommandEnvName(in); got != want {
			t.Errorf("CommandEnvName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTransportEnvVars(t *testing.T) {
	if got := config.SecretEnvVar("fake-recipe"); got != "AFM_SECRET_FAKE_RECIPE" {
		t.Errorf("SecretEnvVar = %q", got)
	}
	if got := config.SyspromptEnvVar("fake-recipe"); got != "AFM_SYSPROMPT_FAKE_RECIPE" {
		t.Errorf("SyspromptEnvVar = %q", got)
	}
}
