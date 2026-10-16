package flow

import (
	"errors"
	"fmt"

	"github.com/akopichin/afm/pkg/assistants"
	"gopkg.in/yaml.v3"
)

// StageAssistant declares one helper profile a stage auto-attaches at the start of its execution phase, the
// same way a manual dashboard Attach click would (pkg/orchestrator's attachDeclaredAssistants, wired into
// spawnKind). Title/Instruction/Mode/Trigger mirror Attach's own fields one-to-one; empty Mode/Trigger are
// left as-is (not defaulted here) so Attach's own defaults (advisory/one_shot) apply.
type StageAssistant struct {
	Profile     string
	Title       string
	Instruction string
	Mode        assistants.Mode
	Trigger     assistants.Trigger
}

// StageAssistants is a stage's assistants: field — one or more StageAssistant, parsed from a profile name
// scalar, a single object, or a list mixing both forms. See UnmarshalYAML.
type StageAssistants []StageAssistant

// UnmarshalYAML accepts a profile name scalar, a single object, or a list of either (mixed), mirroring the
// scalar-or-list precedent of lifecyclehooks.EventSelector.UnmarshalYAML.
func (a *StageAssistants) UnmarshalYAML(value *yaml.Node) error {
	parsed, err := parseStageAssistantsNode(value)
	if err != nil {
		return err
	}
	*a = parsed
	return nil
}

func parseStageAssistantsNode(value *yaml.Node) (StageAssistants, error) {
	switch value.Kind {
	case yaml.ScalarNode:
		name, err := decodeProfileName(value)
		if err != nil {
			return nil, err
		}
		return StageAssistants{{Profile: name}}, nil
	case yaml.MappingNode:
		var one StageAssistant
		if err := one.unmarshalMapping(value); err != nil {
			return nil, err
		}
		return StageAssistants{one}, nil
	case yaml.SequenceNode:
		out := make(StageAssistants, 0, len(value.Content))
		for _, item := range value.Content {
			switch item.Kind {
			case yaml.ScalarNode:
				name, err := decodeProfileName(item)
				if err != nil {
					return nil, err
				}
				out = append(out, StageAssistant{Profile: name})
			case yaml.MappingNode:
				var one StageAssistant
				if err := one.unmarshalMapping(item); err != nil {
					return nil, err
				}
				out = append(out, one)
			default:
				return nil, errors.New("assistants: list items must be a profile name or an object")
			}
		}
		return out, nil
	default:
		return nil, errors.New("assistants must be a profile name, an object, or a list of them")
	}
}

// decodeProfileName requires the scalar's tag to be an explicit string — a bare YAML scalar like `42` or
// `true` resolves to !!int/!!bool, not !!str, so this rejects those rather than silently stringifying them.
func decodeProfileName(value *yaml.Node) (string, error) {
	if value.Tag != "!!str" {
		return "", errors.New("assistants: profile name must be a string")
	}
	return value.Value, nil
}

// unmarshalMapping decodes the object form via an explicit key switch — mirrors FlowCollaboration's own
// UnmarshalYAML/verifyStepFromMapping's pattern rather than a lenient `type plain` Decode, so a typo'd key
// (e.g. "triger") fails loudly instead of being silently ignored.
func (a *StageAssistant) unmarshalMapping(value *yaml.Node) error {
	for i := 0; i+1 < len(value.Content); i += 2 {
		key := value.Content[i].Value
		val := value.Content[i+1]
		switch key {
		case "profile":
			if err := val.Decode(&a.Profile); err != nil {
				return fmt.Errorf("assistants: profile: %w", err)
			}
		case "title":
			if err := val.Decode(&a.Title); err != nil {
				return fmt.Errorf("assistants: title: %w", err)
			}
		case "instruction":
			if err := val.Decode(&a.Instruction); err != nil {
				return fmt.Errorf("assistants: instruction: %w", err)
			}
		case "mode":
			var v string
			if err := val.Decode(&v); err != nil {
				return fmt.Errorf("assistants: mode: %w", err)
			}
			a.Mode = assistants.Mode(v)
		case "trigger":
			var v string
			if err := val.Decode(&v); err != nil {
				return fmt.Errorf("assistants: trigger: %w", err)
			}
			a.Trigger = assistants.Trigger(v)
		default:
			return fmt.Errorf("assistants: unknown field %q; use \"profile\"", key)
		}
	}
	return nil
}
