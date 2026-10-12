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

// SupportedVerifyCommand сообщает, поддерживается ли алиас как verify-адаптер в V1.
func SupportedVerifyCommand(cmd string, cfg Config) bool {
	resolved, _ := ResolveAgentCommand(cmd, cfg)
	if recipe, ok := cfg.Docker.Agents[resolved]; ok {
		return recipe.Type == RecipeTypeCodex
	}
	return resolved == codexAdapterCommand
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
func ValidateVerifySpecs(f *flow.Flow, cfg Config, codexRecipesShimmed bool) error {
	for _, s := range f.Stages {
		for i, step := range s.Verify.Steps {
			if step.Kind != flow.VerifyAgent {
				continue
			}
			if !SupportedVerifyCommand(step.Command, cfg) {
				return fmt.Errorf("stage %q: verify[%d]: command %q is not a supported verify adapter (v1 supports codex-as-claude only)", s.ID, i+1, step.Command)
			}
			resolved, _ := ResolveAgentCommand(step.Command, cfg)
			if recipe, ok := cfg.Docker.Agents[resolved]; ok && recipe.Type == RecipeTypeCodex && !codexRecipesShimmed {
				return fmt.Errorf("stage %q: verify[%d]: command %q: codex recipe requires docker autoShim to enforce read-only; use codex-as-claude on host", s.ID, i+1, step.Command)
			}
		}
	}
	return nil
}
