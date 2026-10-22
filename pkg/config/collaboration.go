package config

import (
	"fmt"

	"github.com/akopichin/afm/pkg/assistants"
	"gopkg.in/yaml.v3"
)

// CollaborationConfig gates the helper-agent ("collaboration") feature: a tri-state enable switch plus the
// locally configured assistant profiles and delegation limits. Shape mirrors SideAgentConfig
// (pkg/config/config.go:308) — same tri-state-*bool + IsEnabled pattern.
type CollaborationConfig struct {
	// Enabled is a tri-state switch: nil → ON by default; explicit false / env AFM_COLLABORATION=0 turns it
	// off. See IsEnabled.
	Enabled *bool `yaml:"enabled,omitempty"`
	// MaxDepth overrides the default delegation depth (assistants.ResolveDepth's own default of 3). Pointer
	// so an explicit 0 ("do not allow any helpers") survives merge distinctly from "not set" — Default()
	// deliberately does NOT set this field; the numeric default lives only in assistants.ResolveDepth, the
	// same way IsEnabled (not Default()) holds the default for the bool switch above.
	MaxDepth *int `yaml:"max_depth,omitempty"`
	// Mode — dashboard view mode (assistants.CollaborationMode). nil = not set by this layer; like MaxDepth,
	// Default() deliberately does NOT set it: the "team" default lives only at mode resolution
	// (cmd/afm/run_collaboration.go). The value is validated per layer by UnmarshalYAML.
	Mode *assistants.CollaborationMode `yaml:"mode,omitempty"`
	// Profiles — locally configured assistant roles, keyed by profile id. No marketplace, no install step:
	// builtin + this (global/project config) are the only sources (§3.5 of dev-plan.md's grooming notes).
	Profiles map[string]ProfileConfig `yaml:"profiles,omitempty"`
	// Limits will carry delegation/quota limits (concurrent helpers, budget caps, …) added by later stages
	// (T4a admission, T7 required-subtask budgets). Intentionally empty for now.
	Limits CollaborationLimits `yaml:"limits,omitempty"`

	// maxDepthGlobal/maxDepthProject retain the RAW collaboration.max_depth value read from each config
	// layer (global ~/.afm/config.yaml vs project .afm/config.yaml) separately from the merged MaxDepth
	// above. mergeFile folds global into project (project wins when both set it), so by the time LoadFrom
	// returns, MaxDepth alone can no longer say WHICH layer actually set it — and resolveCollaboration
	// (cmd/afm/run_collaboration.go) needs that provenance to report assistants.DepthPolicy.Source
	// ("global" vs "project") correctly instead of always blaming the more specific layer. Not part of the
	// YAML shape; populated only by mergeFile.
	maxDepthGlobal  *int
	maxDepthProject *int
}

// UnmarshalYAML decodes the struct normally but first validates the raw `mode` node. A plain *string field
// cannot tell an explicit `mode: null` (or bare `mode:`) from an absent key and would silently stringify a
// number or bool, so the node is checked before it collapses into a pointer.
func (c *CollaborationConfig) UnmarshalYAML(node *yaml.Node) error {
	if modeNode := findMapValue(node, "mode"); modeNode != nil {
		if _, err := assistants.ParseCollaborationModeNode(modeNode); err != nil {
			return err
		}
	}
	type plain CollaborationConfig // no methods: decoding it does not recurse into UnmarshalYAML
	return node.Decode((*plain)(c))
}

// findMapValue returns the value node of key in a YAML mapping, or nil when the mapping does not set it.
// Keys written directly win over keys that arrive through a merge key (`<<: *anchor`, or a sequence of
// them, where earlier maps win) — the same precedence yaml.v3 applies while decoding. Merge expansion
// happens only inside node.Decode, so without looking into `<<` here a value supplied through a shared
// anchor would reach the config unvalidated. Keys that are aliases (`*k: value`) are compared by their
// target. Mappings already seen are skipped, so a self-referential anchor (`c: &c {<<: *c}`) ends the
// search instead of overflowing the stack; node.Decode then reports that cycle itself.
func findMapValue(node *yaml.Node, key string) *yaml.Node {
	return findMapValueSeen(node, key, map[*yaml.Node]bool{})
}

// yamlMergeTag is yaml.v3's tag for the special `<<` merge key.
const yamlMergeTag = "!!merge"

func findMapValueSeen(node *yaml.Node, key string, seen map[*yaml.Node]bool) *yaml.Node {
	if node.Kind == yaml.AliasNode {
		node = node.Alias
	}
	if node.Kind != yaml.MappingNode || seen[node] {
		return nil
	}
	seen[node] = true
	// Content holds key/value node pairs.
	for i := 0; i+1 < len(node.Content); i += 2 {
		k := node.Content[i]
		if k.Kind == yaml.AliasNode {
			k = k.Alias
		}
		if k.Kind == yaml.ScalarNode && k.Value == key && k.Tag != yamlMergeTag {
			return node.Content[i+1]
		}
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Tag != yamlMergeTag {
			continue
		}
		merged := node.Content[i+1]
		if merged.Kind == yaml.AliasNode {
			merged = merged.Alias
		}
		sources := []*yaml.Node{merged}
		if merged.Kind == yaml.SequenceNode {
			sources = merged.Content
		}
		for _, src := range sources {
			if v := findMapValueSeen(src, key, seen); v != nil {
				return v
			}
		}
	}
	return nil
}

// MaxDepthLayers returns the raw collaboration.max_depth value read from each config layer, as populated by
// LoadFrom's two mergeFile passes. Both nil means neither layer set it.
func (c CollaborationConfig) MaxDepthLayers() (global, project *int) {
	return c.maxDepthGlobal, c.maxDepthProject
}

// IsEnabled reports whether the collaboration feature is on. Priority: env AFM_COLLABORATION > config >
// default ON. Mirrors SideAgentConfig.IsEnabled — reuses the shared envBool helper, no second bool-env
// parser.
func (c CollaborationConfig) IsEnabled() bool {
	if v, set := envBool("AFM_COLLABORATION"); set {
		return v
	}
	return c.Enabled == nil || *c.Enabled
}

// ProfileConfig describes one locally configured assistant role: the title shown in the UI, the
// already-configured command/recipe to run it with (not a new binary/transport), and its system
// instructions.
type ProfileConfig struct {
	Title        string `yaml:"title,omitempty"`
	Command      string `yaml:"command,omitempty"`
	Instructions string `yaml:"instructions,omitempty"`
	// Delegate opts this profile into creating its own child tasks (assistants.Capabilities.Delegate).
	// Default false: delegation spawns additional helper subprocesses, so it is opt-in per profile. Without
	// this field no configured profile could ever delegate (BuildRegistry is called with no builtin layer),
	// leaving the whole recursive-delegation path unreachable in production.
	Delegate bool `yaml:"delegate,omitempty"`
}

// CollaborationLimits carries delegation/quota limits (T4a.2 admission). MaxTasks is a whole-RUN ceiling —
// state.Preconditions.MaxQuota/AssistantsState.QuotaReserved are already whole-tree (not per-level, T2.6),
// so this is the one number admission needs; nil means unlimited (back-compat with the empty struct this
// replaces). Further limits (T7 required-subtask budgets) are added when that stage needs them.
type CollaborationLimits struct {
	// MaxTasks caps the total number of helper tasks (top-level attaches plus every delegated descendant)
	// created over the lifetime of one run. nil/absent = unlimited.
	MaxTasks *int `yaml:"max_tasks,omitempty"`
}

// validateCollaborationMaxDepth rejects a negative collaboration.max_depth for one config layer, naming
// both the layer ("global"/"project") and the key path in the error — unlike validateVerify's post-merge
// checks, this runs per-layer inside mergeFile (mirroring the Hooks per-layer ValidateLayer call) so the
// message can say which file was wrong.
func validateCollaborationMaxDepth(layer string, v *int) error {
	if v != nil && *v < 0 {
		return fmt.Errorf("%s config: collaboration.max_depth: must not be negative, got %d", layer, *v)
	}
	return nil
}

// validateCollaborationMaxTasks rejects a negative collaboration.limits.max_tasks for one config layer —
// mirrors validateCollaborationMaxDepth above. 0 is legal (same meaning as max_depth:0: no helpers at all,
// just reached via the task-count ceiling instead of the depth one).
func validateCollaborationMaxTasks(layer string, v *int) error {
	if v != nil && *v < 0 {
		return fmt.Errorf("%s config: collaboration.limits.max_tasks: must not be negative, got %d", layer, *v)
	}
	return nil
}
