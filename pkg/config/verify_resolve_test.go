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

// --- T1.5: HelperCommandRejection / HelperReadonlyEnforcement — the read-only capability gate ----------

// TestHelperCapability_HostNonCodexIsUnavailable is the main gate test: DefaultClaudeArgs runs claude with
// --dangerously-skip-permissions (pkg/executor/executor.go), and CODEX_VERIFY is read only by
// scripts/codex-as-claude.sh — so on the host, bare claude (and any other non-codex command) has no
// verifiable read-only policy and must be rejected.
func TestHelperCapability_HostNonCodexIsUnavailable(t *testing.T) {
	restore := config.SwapContainerMarkerPresent(func() bool { return false })
	defer restore()
	t.Setenv("AFM_IN_DOCKER", "")
	cfg := verifyRejectionCfg()
	for _, cmd := range []string{config.ClaudeCommand, "myclaude", "implicitclaude", "myagent", "mytextonly", "mycursor"} {
		if got := config.HelperCommandRejection(cmd, cfg); got == "" {
			t.Errorf("HelperCommandRejection(%q) = \"\", want a non-empty reason on the host for a non-codex command", cmd)
		}
	}
}

// TestHelperCapability_HostCodexIsAvailable — the codex read-only adapter has a verifiable OS guarantee
// (Seatbelt/Landlock via -s read-only) on the host, so it is available with readonly_enforcement "os".
func TestHelperCapability_HostCodexIsAvailable(t *testing.T) {
	restore := config.SwapContainerMarkerPresent(func() bool { return false })
	defer restore()
	t.Setenv("AFM_IN_DOCKER", "")
	cfg := verifyRejectionCfg()
	for _, cmd := range []string{"codex-as-claude", "mycodex"} {
		if got := config.HelperCommandRejection(cmd, cfg); got != "" {
			t.Errorf("HelperCommandRejection(%q) = %q, want allowed (\"\") on the host via the codex read-only adapter", cmd, got)
		}
		if got := config.HelperReadonlyEnforcement(cmd, cfg); got != "os" {
			t.Errorf("HelperReadonlyEnforcement(%q) = %q, want \"os\"", cmd, got)
		}
	}
}

// TestHelperCapability_NoSilentFallback — an unavailable profile's command is named in the rejection
// reason and stays rejected; there is no second path that would silently start it unrestricted instead.
func TestHelperCapability_NoSilentFallback(t *testing.T) {
	restore := config.SwapContainerMarkerPresent(func() bool { return false })
	defer restore()
	t.Setenv("AFM_IN_DOCKER", "")
	cfg := verifyRejectionCfg()
	reason := config.HelperCommandRejection(config.ClaudeCommand, cfg)
	if reason == "" {
		t.Fatal("expected bare claude to be rejected on the host (no verifiable read-only policy)")
	}
	if !strings.Contains(reason, config.ClaudeCommand) {
		t.Errorf("reason %q does not name the rejected command", reason)
	}
	if got := config.HelperReadonlyEnforcement(config.ClaudeCommand, cfg); got != "none" {
		t.Errorf("HelperReadonlyEnforcement(claude) = %q, want \"none\" when unavailable", got)
	}
}

// TestHelperCapability_ContainerAllowsAnyCommandWithoutOsGuarantee — inside any container, there is no
// verifiable OS read-only guarantee for ANY agent, so the trust boundary becomes the container itself (the
// same model already accepted for AI-verify in Docker). Any command is "available" there, but with
// readonly_enforcement "none".
func TestHelperCapability_ContainerAllowsAnyCommandWithoutOsGuarantee(t *testing.T) {
	restore := config.SwapContainerMarkerPresent(func() bool { return true })
	defer restore()
	t.Setenv("AFM_IN_DOCKER", "")
	cfg := verifyRejectionCfg()
	if got := config.HelperCommandRejection(config.ClaudeCommand, cfg); got != "" {
		t.Errorf("HelperCommandRejection(claude) in a container = %q, want allowed (\"\")", got)
	}
	if got := config.HelperReadonlyEnforcement(config.ClaudeCommand, cfg); got != "none" {
		t.Errorf("HelperReadonlyEnforcement(claude) in a container = %q, want \"none\" (no OS guarantee, trust boundary is the container)", got)
	}
}

// TestHelperCapability_PredicateUsesInContainerNotReExec — a foreign container (marker file present, but
// NOT afm's own docker re-exec: AFM_IN_DOCKER unset) must still be treated as a container. The
// bwrap-namespace limitation is a property of any unprivileged container, not of afm's own docker
// transport (see AGENTS.md's "Container auto-detection").
func TestHelperCapability_PredicateUsesInContainerNotReExec(t *testing.T) {
	restore := config.SwapContainerMarkerPresent(func() bool { return true })
	defer restore()
	t.Setenv("AFM_IN_DOCKER", "")
	if config.ReExecedIntoContainer() {
		t.Fatal("test setup invalid: ReExecedIntoContainer() must be false here")
	}
	if !config.InContainer() {
		t.Fatal("test setup invalid: InContainer() must be true via the marker")
	}
	cfg := verifyRejectionCfg()
	if got := config.HelperCommandRejection(config.ClaudeCommand, cfg); got != "" {
		t.Errorf("HelperCommandRejection(claude) = %q, want allowed (\"\") — gate must use InContainer(), not ReExecedIntoContainer()", got)
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
