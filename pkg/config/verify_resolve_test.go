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

func TestSupportedVerifyCommand(t *testing.T) {
	cfg := config.Config{
		Docker: config.DockerConfig{
			Agents: map[string]config.AgentRecipe{
				"mycodex": {Type: config.RecipeTypeCodex, Auth: config.RecipeAuth{To: "env:OPENAI_API_KEY"}},
				"glm51":   {Type: config.RecipeTypeOpenAI, Model: "glm", URL: "https://x", Auth: config.RecipeAuth{To: "env:OPENAI_API_KEY"}},
			},
		},
	}

	tests := []struct {
		name string
		cmd  string
		want bool
	}{
		{"claude is not a supported verify adapter (no read-only mode)", "claude", false},
		{"bare codex is not supported (raw binary has no read-only adapter)", "codex", false},
		{"codex-as-claude shim is supported", "codex-as-claude", true},
		{"custom codex-recipe alias is supported", "mycodex", true},
		{"nonexistent alias is not supported", "frobnicator", false},
		{"resolvable-but-unsupported-for-verify alias (openai recipe)", "glm51", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := config.SupportedVerifyCommand(tt.cmd, cfg); got != tt.want {
				t.Errorf("SupportedVerifyCommand(%q) = %v, want %v", tt.cmd, got, tt.want)
			}
		})
	}
}

// TestSupportedVerifyCommand_RecipeOverridesBareCodexName — регрессия на
// находку код-ревью: голая проверка resolved == "codex-as-claude" не должна
// затмевать собой recipe, если кто-то переопределил ключ "codex-as-claude" в
// docker.agents ДРУГИМ типом (напр. openai). Recipe для резолвнутого имени
// обязан быть решающим словом ПЕРЕД эвристикой "голое имя".
func TestSupportedVerifyCommand_RecipeOverridesBareCodexName(t *testing.T) {
	cfg := config.Config{
		Docker: config.DockerConfig{
			Agents: map[string]config.AgentRecipe{
				"codex-as-claude": {Type: config.RecipeTypeOpenAI, Model: "m", URL: "https://x", Auth: config.RecipeAuth{To: "env:OPENAI_API_KEY"}},
			},
		},
	}
	if got := config.SupportedVerifyCommand("codex-as-claude", cfg); got {
		t.Error(`SupportedVerifyCommand("codex-as-claude") = true, want false when the "codex-as-claude" key is overridden by a non-codex recipe`)
	}
}

func TestValidateVerifySpecs(t *testing.T) {
	cfg := config.Config{
		Docker: config.DockerConfig{
			Agents: map[string]config.AgentRecipe{
				"mycodex": {Type: config.RecipeTypeCodex, Auth: config.RecipeAuth{To: "env:OPENAI_API_KEY"}},
				"glm51":   {Type: config.RecipeTypeOpenAI, Model: "glm", URL: "https://x", Auth: config.RecipeAuth{To: "env:OPENAI_API_KEY"}},
			},
		},
	}

	t.Run("shell-only verify never touches command resolution", func(t *testing.T) {
		f := &flow.Flow{Stages: []flow.Stage{
			{ID: "s1", Verify: flow.NewShellVerify("go test ./...")},
		}}
		if err := config.ValidateVerifySpecs(f, cfg); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("supported agent verify command passes", func(t *testing.T) {
		f := &flow.Flow{Stages: []flow.Stage{
			{ID: "s1", Verify: agentVerify(t, "codex-as-claude", "")},
		}}
		if err := config.ValidateVerifySpecs(f, cfg); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("bare codex fails: no read-only adapter for the raw binary", func(t *testing.T) {
		f := &flow.Flow{Stages: []flow.Stage{
			{ID: "s1", Verify: agentVerify(t, "codex", "")},
		}}
		err := config.ValidateVerifySpecs(f, cfg)
		if err == nil {
			t.Fatal("expected error: bare codex has no read-only verify adapter, must not be supported")
		}
		if !strings.Contains(err.Error(), "s1") || !strings.Contains(err.Error(), "codex") {
			t.Errorf("error = %q, want it to mention the stage and the command", err.Error())
		}
	})

	t.Run("supported custom codex-recipe verify command passes", func(t *testing.T) {
		f := &flow.Flow{Stages: []flow.Stage{
			{ID: "s1", Verify: agentVerify(t, "mycodex", "")},
		}}
		if err := config.ValidateVerifySpecs(f, cfg); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("nonexistent alias fails before any run", func(t *testing.T) {
		f := &flow.Flow{Stages: []flow.Stage{
			{ID: "s1", Verify: agentVerify(t, "frobnicator", "")},
		}}
		err := config.ValidateVerifySpecs(f, cfg)
		if err == nil {
			t.Fatal("expected error for a nonexistent verify command alias")
		}
		if !strings.Contains(err.Error(), "s1") || !strings.Contains(err.Error(), "frobnicator") {
			t.Errorf("error = %q, want it to mention the stage and the command", err.Error())
		}
	})

	t.Run("resolvable-but-unsupported-for-verify alias fails", func(t *testing.T) {
		f := &flow.Flow{Stages: []flow.Stage{
			{ID: "s1", Verify: agentVerify(t, "glm51", "")},
		}}
		if err := config.ValidateVerifySpecs(f, cfg); err == nil {
			t.Fatal("expected error: glm51 resolves as an agent but is not a supported verify adapter")
		}
	})

	t.Run("claude verify command fails: no read-only verify mode", func(t *testing.T) {
		f := &flow.Flow{Stages: []flow.Stage{
			{ID: "s1", Verify: agentVerify(t, "claude", "")},
		}}
		err := config.ValidateVerifySpecs(f, cfg)
		if err == nil {
			t.Fatal("expected error: claude has no read-only verify mode, must not be a supported verify adapter")
		}
		if !strings.Contains(err.Error(), "s1") || !strings.Contains(err.Error(), "claude") {
			t.Errorf("error = %q, want it to mention the stage and the command", err.Error())
		}
	})
}

// agentVerify строит однодшаговый агентский VerifySpec напрямую через YAML
// (fromScalar/Kind собираются decoder'ом flow.VerifySpec, а не руками — в
// pkg/config нет доступа к непубличным деталям построения VerifyStep).
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
