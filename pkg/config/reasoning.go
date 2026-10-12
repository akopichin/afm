package config

import (
	"fmt"
	"slices"
	"strings"
)

// Mechanism identifies the effort-control transport family an adapter uses.
const (
	MechanismCodex  = "codex"
	MechanismClaude = "claude"
	MechanismOpenAI = "openai"
)

// CanonicalReasoningLevels is the union dictionary of reasoning levels afm
// understands, in ascending order. A value outside this set is forwarded
// verbatim with a warning (forward-compat; the backend may reject it).
var CanonicalReasoningLevels = []string{"minimal", "low", "medium", "high", "xhigh", "max"}

// reasoningClamp maps each mechanism to the concrete value it should receive for
// a given canonical level. A canonical level that a mechanism does not support
// natively is clamped to the nearest valid level for that mechanism (see the
// table below — it is the single source of truth).
var reasoningClamp = map[string]map[string]string{
	MechanismCodex: {
		"minimal": "minimal", "low": "low", "medium": "medium",
		"high": "high", "xhigh": "xhigh", "max": "xhigh",
	},
	MechanismClaude: {
		"minimal": "low", "low": "low", "medium": "medium",
		"high": "high", "xhigh": "xhigh", "max": "max",
	},
	MechanismOpenAI: {
		"minimal": "minimal", "low": "low", "medium": "medium",
		"high": "high", "xhigh": "high", "max": "high",
	},
}

// ResolveReasoning maps a canonical-or-raw reasoning level to the concrete value
// for a mechanism. A canonical value is clamped per the table and returns
// warn=false; a non-canonical value is returned verbatim with warn=true so the
// caller can emit a preflight warning (the run never fails on reasoning). Empty
// raw returns ("", false) — callers skip it.
func ResolveReasoning(mechanism, raw string) (value string, warn bool) {
	if raw == "" {
		return "", false
	}
	table, ok := reasoningClamp[mechanism]
	if !ok {
		return raw, true
	}
	if clamped, ok := table[raw]; ok {
		return clamped, false
	}
	return raw, true
}

// ReasoningWarnings returns human-readable preflight warnings for a config: one
// per non-canonical client/recipe reasoning value (forwarded as-is), and one per
// cursor-recipe that sets reasoning (ignored, no API). Empty slice = no warnings.
// reasoning is NEVER a hard error, so this never returns an error.
func (c Config) ReasoningWarnings() []string {
	var out []string
	known := strings.Join(CanonicalReasoningLevels, ", ")
	if _, warn := ResolveReasoning(MechanismClaude, c.Client.Reasoning); warn {
		out = append(out, fmt.Sprintf(
			"client: reasoning %q is not a recognized level (known: %s); forwarding as-is, the backend may reject it",
			c.Client.Reasoning, known))
	}
	ids := make([]string, 0, len(c.Docker.Agents))
	for id := range c.Docker.Agents {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		recipe := c.Docker.Agents[id]
		if recipe.Reasoning == "" {
			continue
		}
		mechanism, ok := MechanismForRecipeType(recipe.Type)
		if !ok {
			out = append(out, fmt.Sprintf("docker.agents.%s: %s does not support reasoning; ignoring", id, recipe.Type))
			continue
		}
		if _, warn := ResolveReasoning(mechanism, recipe.Reasoning); warn {
			out = append(out, fmt.Sprintf(
				"docker.agents.%s: reasoning %q is not a recognized level (known: %s); forwarding as-is, the backend may reject it",
				id, recipe.Reasoning, known))
		}
	}
	return out
}

// MechanismForRecipeType maps an AgentRecipe.Type to its reasoning mechanism.
// ok=false means the type has no reasoning mechanism (cursor / unknown); the
// caller should warn and skip.
func MechanismForRecipeType(recipeType string) (mechanism string, ok bool) {
	switch recipeType {
	case "", ClaudeCommand:
		return MechanismClaude, true
	case RecipeTypeCodex:
		return MechanismCodex, true
	case RecipeTypeOpenAI, RecipeTypeOpenAIAgent:
		return MechanismOpenAI, true
	default:
		return "", false
	}
}
