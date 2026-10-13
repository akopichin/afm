package config_test

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
)

func TestResolveAgentCommand(t *testing.T) {
	cfg := config.Config{
		Docker: config.DockerConfig{
			Agents: map[string]config.AgentRecipe{
				"mycodex": {Type: config.RecipeTypeCodex, Auth: config.RecipeAuth{To: "env:OPENAI_API_KEY"}},
				"glm51":   {Type: config.RecipeTypeOpenAI, Model: "glm", URL: "https://x", Auth: config.RecipeAuth{To: "env:OPENAI_API_KEY"}},
			},
		},
	}

	tests := []struct {
		name          string
		cmd           string
		wantResolved  string
		wantSupported bool
	}{
		{"empty resolves to claude", "", config.ClaudeCommand, true},
		{"explicit claude", config.ClaudeCommand, config.ClaudeCommand, true},
		{"recipe key is supported", "mycodex", "mycodex", true},
		{"other recipe key is supported", "glm51", "glm51", true},
		{"opaque host binary is not \"supported\" (no recipe)", "some-random-binary", "some-random-binary", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolved, supported := config.ResolveAgentCommand(tt.cmd, cfg)
			if resolved != tt.wantResolved || supported != tt.wantSupported {
				t.Errorf("ResolveAgentCommand(%q) = (%q, %v), want (%q, %v)", tt.cmd, resolved, supported, tt.wantResolved, tt.wantSupported)
			}
		})
	}
}

// verifyRejectionCfg is shared between the VerifyCommandRejection tests: it carries one recipe of every
// AgentRecipe.Type so the allow/deny split can be exercised without re-declaring the map everywhere.
func verifyRejectionCfg() config.Config {
	return config.Config{
		Docker: config.DockerConfig{
			Agents: map[string]config.AgentRecipe{
				"mycodex":        {Type: config.RecipeTypeCodex, Auth: config.RecipeAuth{To: "env:OPENAI_API_KEY"}},
				"myclaude":       {Type: config.ClaudeCommand, Model: "m", URL: "https://x", Auth: config.RecipeAuth{To: "env:ANTHROPIC_API_KEY"}},
				"implicitclaude": {Type: "", Model: "m", URL: "https://x", Auth: config.RecipeAuth{To: "env:ANTHROPIC_API_KEY"}},
				"myagent":        {Type: config.RecipeTypeOpenAIAgent, Model: "m", URL: "https://x", Auth: config.RecipeAuth{To: "env:OPENAI_API_KEY"}},
				"mytextonly":     {Type: config.RecipeTypeOpenAI, Model: "m", URL: "https://x", Auth: config.RecipeAuth{To: "env:OPENAI_API_KEY"}},
				"mycursor":       {Type: config.RecipeTypeCursor, Model: "m", URL: "https://x", Auth: config.RecipeAuth{To: "env:CURSOR_API_KEY"}},
			},
		},
	}
}

func TestVerifyCommandRejection_Allowed(t *testing.T) {
	cfg := verifyRejectionCfg()

	tests := []struct {
		name string
		cmd  string
	}{
		{"empty resolves to default claude client", ""},
		{"bare claude", config.ClaudeCommand},
		{"codex-as-claude real read-only shim", "codex-as-claude"},
		{"type: codex recipe", "mycodex"},
		{"type: claude recipe (explicit)", "myclaude"},
		{"type: claude recipe (implicit empty type)", "implicitclaude"},
		{"type: openai-agent recipe (real tool-loop)", "myagent"},
		{"unknown host binary with no recipe", "glm51"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := config.VerifyCommandRejection(tt.cmd, cfg); got != "" {
				t.Errorf("VerifyCommandRejection(%q) = %q, want allowed (\"\")", tt.cmd, got)
			}
		})
	}
}

func TestVerifyCommandRejection_TextOnlyRecipes(t *testing.T) {
	cfg := verifyRejectionCfg()

	tests := []struct {
		name     string
		cmd      string
		wantType string
	}{
		{"type: openai recipe is text-only, no guaranteed JSON", "mytextonly", config.RecipeTypeOpenAI},
		{"type: cursor recipe is text-only, no guaranteed JSON", "mycursor", config.RecipeTypeCursor},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := config.VerifyCommandRejection(tt.cmd, cfg)
			if got == "" {
				t.Fatalf("VerifyCommandRejection(%q) = \"\", want a non-empty rejection reason", tt.cmd)
			}
			if !strings.Contains(got, tt.wantType) {
				t.Errorf("rejection reason %q does not mention recipe type %q", got, tt.wantType)
			}
		})
	}
}

func TestValidateVerifySpecs_AllowsClaude(t *testing.T) {
	cfg := config.Config{}
	f := &flow.Flow{Stages: []flow.Stage{
		{ID: "s1", Verify: agentVerify(t, "claude", "")},
	}}
	if err := config.ValidateVerifySpecs(f, cfg); err != nil {
		t.Errorf("claude must be allowed as a verify adapter: unexpected error: %v", err)
	}
}

func TestValidateVerifySpecs_RejectsTextOnlyRecipe(t *testing.T) {
	cfg := verifyRejectionCfg()
	// Two steps, rejected one second: proves the index in the error is the step's real position
	// (i+1), not a hard-coded 1.
	f := &flow.Flow{Stages: []flow.Stage{
		{ID: "s1", Verify: flow.VerifySpec{Steps: []flow.VerifyStep{
			{Kind: flow.VerifyAgent, Command: "codex-as-claude"},
			{Kind: flow.VerifyAgent, Command: "mytextonly"},
		}}},
	}}
	err := config.ValidateVerifySpecs(f, cfg)
	if err == nil {
		t.Fatal("expected error: type:openai recipe is text-only and cannot act as a verify adapter")
	}
	if !strings.Contains(err.Error(), "s1") || !strings.Contains(err.Error(), "verify[2]") {
		t.Errorf("error = %q, want it to mention the stage id and verify[2]", err.Error())
	}
}

func TestValidateVerifySpecs_IgnoresShellSteps(t *testing.T) {
	cfg := verifyRejectionCfg()
	// "mytextonly" would be rejected if resolved as a command; a shell step must never attempt resolution.
	f := &flow.Flow{Stages: []flow.Stage{
		{ID: "s1", Verify: flow.NewShellVerify("mytextonly")},
	}}
	if err := config.ValidateVerifySpecs(f, cfg); err != nil {
		t.Errorf("shell verify steps must never be resolved as commands: unexpected error: %v", err)
	}
}

func TestValidateVerifySpecs_CodexRecipeWithoutShim(t *testing.T) {
	cfg := verifyRejectionCfg()
	f := &flow.Flow{Stages: []flow.Stage{
		{ID: "s1", Verify: agentVerify(t, "mycodex", "")},
	}}
	if err := config.ValidateVerifySpecs(f, cfg); err != nil {
		t.Errorf("type:codex recipe must be allowed regardless of docker autoShim: unexpected error: %v", err)
	}
}

// agentVerify строит однодшаговый агентский VerifySpec напрямую через YAML.
func agentVerify(t *testing.T, command, prompt string) flow.VerifySpec {
	t.Helper()
	src := "verify:\n  command: " + command + "\n"
	if prompt != "" {
		src += "  prompt: " + prompt + "\n"
	}
	var holder struct {
		Verify flow.VerifySpec `yaml:"verify"`
	}
	if err := yaml.Unmarshal([]byte(src), &holder); err != nil {
		t.Fatalf("build agent verify: %v", err)
	}
	return holder.Verify
}
