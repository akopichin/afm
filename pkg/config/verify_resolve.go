package config

import (
	"fmt"

	"github.com/akopichin/afm/pkg/flow"
)

// codexAdapterCommand — имя РЕАЛЬНОГО read-only адаптера, когда verify.command указывает на него БЕЗ
// recipe в docker.agents.
const codexAdapterCommand = "codex-as-claude"

// ResolveAgentCommand resolves recipes or host binaries; supported marks known recipes/Claude. Empty
// command uses ClaudeCommand, not Client.Command.
func ResolveAgentCommand(cmd string, cfg Config) (resolved string, supported bool) {
	if cmd == "" {
		return ClaudeCommand, true
	}
	if _, ok := cfg.Docker.Agents[cmd]; ok {
		return cmd, true
	}
	if cmd == ClaudeCommand {
		return cmd, true
	}
	return cmd, false
}

// VerifyCommandRejection returns a non-empty reason if cmd provably cannot act as a verify adapter, and ""
// if it can. We reject only text-only recipes: they don't guarantee strict JSON, and DecodeModelResult
// would fail at the end of an expensive pass.
func VerifyCommandRejection(cmd string, cfg Config) string {
	resolved, _ := ResolveAgentCommand(cmd, cfg)
	recipe, ok := cfg.Docker.Agents[resolved]
	if !ok {
		return ""
	}
	switch recipe.Type {
	case RecipeTypeOpenAI, RecipeTypeCursor:
		return fmt.Sprintf("recipe type %q is text-only (no tool loop, no guaranteed JSON output)", recipe.Type)
	default:
		return ""
	}
}

// IsCodexCommand reports whether a command resolves to the codex read-only adapter — the bare shim name
// codex-as-claude, or a docker.agents recipe of type codex.
func (c Config) IsCodexCommand(cmd string) bool {
	resolved, _ := ResolveAgentCommand(cmd, c)
	if recipe, ok := c.Docker.Agents[resolved]; ok {
		return recipe.Type == RecipeTypeCodex
	}
	return resolved == codexAdapterCommand
}

// HelperCommandRejection returns a non-empty reason if cmd cannot be trusted to run a read-only helper
// profile (dev-plan.md Этап 1 T1.5, §7.4 + grooming decision 2.3a), and "" if it can. Modeled on
// VerifyCommandRejection and reusing the existing IsCodexCommand classifier — no new command resolver.
//
// In any container (config.InContainer(), NOT ReExecedIntoContainer() — see AGENTS.md's "Container
// auto-detection": the bwrap-namespace limitation that breaks codex's read-only sandbox in an unprivileged
// container is a property of ANY container, not of afm's own docker transport), there is no verifiable OS
// read-only guarantee for any agent, so the trust boundary becomes the container itself — the same model
// already accepted for AI-verify in Docker. Every command is "available" there.
//
// On the host, only the codex read-only adapter (bare codex-as-claude, or a docker.agents recipe of type
// codex) has a verifiable OS guarantee (Seatbelt/Landlock via `-s read-only`, wired through CODEX_VERIFY).
// Every other command — including bare claude, which DefaultClaudeArgs runs with
// --dangerously-skip-permissions (pkg/executor/executor.go) — has no such guarantee and is rejected. There
// is no silent fallback to an unrestricted agent: a non-empty reason here must keep the profile from ever
// starting (enforced at task-creation time, T3.4).
// Значения HelperReadonlyEnforcement — зеркалят assistants.ReadonlyEnforcement{OS,None}, но этот пакет не
// импортирует pkg/assistants (остаётся нейтральным к доменным типам collaboration).
const (
	readonlyEnforcementOS   = "os"
	readonlyEnforcementNone = "none"
)

func HelperCommandRejection(cmd string, cfg Config) string {
	if InContainer() {
		return ""
	}
	if cfg.IsCodexCommand(cmd) {
		return ""
	}
	return fmt.Sprintf("no verifiable read-only policy for command %q on the host (only the codex read-only adapter has an OS-enforced sandbox here; this command would run unrestricted)", cmd)
}

// HelperReadonlyEnforcement reports which read-only guarantee is backing a helper profile's availability —
// "os" when HelperCommandRejection's host+codex branch applies (a verifiable OS sandbox), "none" when
// either the container-trust branch applies or the command is not available at all. Mirrors
// HelperCommandRejection's branches exactly so the two can never disagree about which guarantee is active;
// this value is frozen into a task's ProfileSnapshot at creation time so history does not get silently
// reinterpreted if config changes later.
func HelperReadonlyEnforcement(cmd string, cfg Config) string {
	if HelperCommandRejection(cmd, cfg) != "" {
		return readonlyEnforcementNone
	}
	if InContainer() {
		return readonlyEnforcementNone
	}
	return readonlyEnforcementOS
}

// ValidateVerifySpecs проверяет ДО старта рана, что каждый агентский verify-шаг резолвится в
// поддерживаемый verify-адаптер. Shell-шаги (Kind==VerifyShell) не резолвятся вовсе.
func ValidateVerifySpecs(f *flow.Flow, cfg Config) error {
	for _, s := range f.Stages {
		for i, step := range s.Verify.Steps {
			if step.Kind != flow.VerifyAgent {
				continue
			}
			if reason := VerifyCommandRejection(step.Command, cfg); reason != "" {
				return fmt.Errorf("stage %q: verify[%d]: command %q cannot act as a verify adapter: %s", s.ID, i+1, step.Command, reason)
			}
		}
	}
	return nil
}
