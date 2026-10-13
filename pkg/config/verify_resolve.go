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
