package flow

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/akopichin/afm/contracts/agentcomm"
	"github.com/akopichin/afm/contracts/assistants"
	"github.com/akopichin/afm/contracts/skills"
)

const (
	keyLoop       = "loop"
	keyLoopEvery  = "every"
	keyLoopPrompt = "prompt"
)

// StageAssistant declares one helper profile a stage auto-attaches at the start of its execution phase, the
// same way a manual dashboard Attach click would (pkg/orchestrator's attachDeclaredAssistants, wired into
// spawnKind). Title/Instruction/Mode/Trigger mirror Attach's own fields one-to-one; empty Mode/Trigger are
// left as-is (not defaulted here) so Attach's own defaults (advisory/one_shot) apply.
type StageAssistant struct {
	Profile     string             `yaml:"profile"`
	Title       string             `yaml:"title,omitempty"`
	Instruction string             `yaml:"instruction,omitempty"`
	Mode        assistants.Mode    `yaml:"mode,omitempty"`
	Trigger     assistants.Trigger `yaml:"trigger,omitempty"`
	// Skills are the skills the helper must load before work; SkillsDir overrides the flow's library.
	// The helper does not inherit its stage's skills.
	Skills    skills.Names `yaml:"skills,omitempty"`
	SkillsDir skills.Dir   `yaml:"skills_dir,omitempty"`
	// Loop makes the helper wake itself on a fixed interval; nil means no loop. Needs trigger on_message.
	Loop *StageAssistantLoop `yaml:"loop,omitempty"`
	// decodeErr is the first malformed field of the object form. Flow.validate reports it with the stage and
	// profile, which decoding does not know yet: the profile may follow the bad field in the YAML.
	decodeErr error
}

// StageAssistantLoop is a helper's recurring prompt. Every stays a string so the flow file round-trips
// with the author's own spelling; Interval parses it.
type StageAssistantLoop struct {
	Every  string `yaml:"every"`
	Prompt string `yaml:"prompt"`
}

// Interval parses Every with time.ParseDuration, the one parser validation and the engine share.
func (l StageAssistantLoop) Interval() (time.Duration, error) {
	return time.ParseDuration(l.Every)
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
	value = resolveAlias(value)
	switch value.Kind {
	case yaml.ScalarNode:
		name, err := decodeProfileName(value)
		if err != nil {
			return nil, err
		}
		return StageAssistants{{Profile: name}}, nil
	case yaml.MappingNode:
		var one StageAssistant
		one.unmarshalMapping(value)
		return StageAssistants{one}, nil
	case yaml.SequenceNode:
		out := make(StageAssistants, 0, len(value.Content))
		for _, item := range value.Content {
			item = resolveAlias(item)
			switch item.Kind {
			case yaml.ScalarNode:
				name, err := decodeProfileName(item)
				if err != nil {
					return nil, err
				}
				out = append(out, StageAssistant{Profile: name})
			case yaml.MappingNode:
				var one StageAssistant
				one.unmarshalMapping(item)
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

// resolveAlias returns the node an alias refers to, so aliased values pass the same shape checks as literal
// ones. YAML cannot anchor an alias, so one step is enough.
func resolveAlias(node *yaml.Node) *yaml.Node {
	if node.Kind == yaml.AliasNode {
		return node.Alias
	}
	return node
}

// decodeProfileName requires the scalar's tag to be an explicit string — a bare YAML scalar like `42` or
// `true` resolves to !!int/!!bool, not !!str, so this rejects those rather than silently stringifying them.
func decodeProfileName(value *yaml.Node) (string, error) {
	if value.Tag != yamlStringTag {
		return "", errors.New("assistants: profile name must be a string")
	}
	return value.Value, nil
}

// unmarshalMapping decodes the object form via an explicit key switch — mirrors FlowCollaboration's own
// UnmarshalYAML/verifyStepFromMapping's pattern rather than a lenient `type plain` Decode, so a typo'd key
// (e.g. "triger") fails loudly. Decoding goes on past a bad field so a profile after it names the helper.
func (a *StageAssistant) unmarshalMapping(value *yaml.Node) {
	seen := make(map[string]bool, len(value.Content)/2)
	for i := 0; i+1 < len(value.Content); i += 2 {
		key := value.Content[i].Value
		if seen[key] {
			a.keepDecodeErr(fmt.Errorf("duplicate field %q", key))
			continue
		}
		seen[key] = true
		a.keepDecodeErr(a.decodeField(key, resolveAlias(value.Content[i+1])))
	}
}

func (a *StageAssistant) keepDecodeErr(err error) {
	if a.decodeErr == nil {
		a.decodeErr = err
	}
}

func (a *StageAssistant) decodeField(key string, val *yaml.Node) error {
	switch key {
	case keyProfile:
		if err := val.Decode(&a.Profile); err != nil {
			return fmt.Errorf("profile: %w", err)
		}
	case keyTitle:
		if err := val.Decode(&a.Title); err != nil {
			return fmt.Errorf("title: %w", err)
		}
	case keyInstruction:
		if err := val.Decode(&a.Instruction); err != nil {
			return fmt.Errorf("instruction: %w", err)
		}
	case keyMode:
		var v string
		if err := val.Decode(&v); err != nil {
			return fmt.Errorf("mode: %w", err)
		}
		a.Mode = assistants.Mode(v)
	case "trigger":
		var v string
		if err := val.Decode(&v); err != nil {
			return fmt.Errorf("trigger: %w", err)
		}
		a.Trigger = assistants.Trigger(v)
	case keySkills:
		names, err := skills.ParseNames(val)
		if err != nil {
			return err
		}
		a.Skills = names
	case keySkillsDir:
		dir, err := skills.ParseDir(val)
		if err != nil {
			return err
		}
		a.SkillsDir = dir
	case keyLoop:
		loop, err := parseStageAssistantLoop(val)
		if err != nil {
			return err
		}
		a.Loop = &loop
	default:
		return fmt.Errorf("unknown field %q; use profile, title, instruction, mode, trigger, skills, skills_dir, loop", key)
	}
	return nil
}

// parseStageAssistantLoop is strict like unmarshalMapping: only an object, only string every/prompt. Missing
// values are left to validateLoop.
func parseStageAssistantLoop(value *yaml.Node) (StageAssistantLoop, error) {
	var loop StageAssistantLoop
	if value.Kind != yaml.MappingNode {
		return loop, errors.New("loop must be an object with every and prompt")
	}
	seen := make(map[string]bool, len(value.Content)/2)
	for i := 0; i+1 < len(value.Content); i += 2 {
		key := value.Content[i].Value
		val := resolveAlias(value.Content[i+1])
		if seen[key] {
			return loop, fmt.Errorf("loop: duplicate field %q", key)
		}
		seen[key] = true
		var target *string
		switch key {
		case keyLoopEvery:
			target = &loop.Every
		case keyLoopPrompt:
			target = &loop.Prompt
		default:
			return loop, fmt.Errorf("loop: unknown field %q; use every, prompt", key)
		}
		if val.Kind != yaml.ScalarNode || val.Tag != yamlStringTag {
			return loop, fmt.Errorf("loop.%s must be a string", key)
		}
		*target = val.Value
	}
	return loop, nil
}

// stageAssistantError is the one format of every helper configuration error, whether decoding or validation
// found it: the stage, the index and, once known, the profile.
func stageAssistantError(stageID string, index int, profile string, err error) error {
	if profile == "" {
		return fmt.Errorf("stage %q: assistants[%d]: %w", stageID, index, err)
	}
	return fmt.Errorf("stage %q: assistants[%d] (profile %q): %w", stageID, index, profile, err)
}

// validate checks one helper; the caller names it with stageAssistantError.
func (a StageAssistant) validate() error {
	if a.decodeErr != nil {
		return a.decodeErr
	}
	if a.Profile == "" {
		return errors.New("profile is required")
	}
	if !a.Mode.Valid() {
		return fmt.Errorf("mode must be %q or %q%s", assistants.ModeAdvisory, assistants.ModeRequired, a.rejected(string(a.Mode)))
	}
	if !a.Trigger.Valid() {
		return fmt.Errorf("trigger must be %q, %q, %q, or %q%s", assistants.TriggerOneShot, assistants.TriggerCheckpoints, assistants.TriggerOnCompletion, assistants.TriggerOnMessage, a.rejected(string(a.Trigger)))
	}
	return a.validateLoop()
}

// rejected shows a rejected value in an error. A looping helper hides it: a YAML alias can make any of its
// values the loop prompt.
func (a StageAssistant) rejected(value string) string {
	if a.Loop != nil {
		return ""
	}
	return fmt.Sprintf(", got %q", value)
}

// validateLoop checks a declared helper loop. No value is echoed: the prompt may hold user text, and a YAML
// alias can make every the prompt.
func (a StageAssistant) validateLoop() error {
	if a.Loop == nil {
		return nil
	}
	if a.Trigger != assistants.TriggerOnMessage {
		return fmt.Errorf("loop requires trigger %q", assistants.TriggerOnMessage)
	}
	if a.Loop.Every == "" {
		return errors.New("loop.every is required")
	}
	every, err := a.Loop.Interval()
	if err != nil {
		return errors.New("loop.every is not a valid duration such as 5m or 1h30m")
	}
	if every < agentcomm.MinLoopInterval {
		return fmt.Errorf("loop.every must be at least %s", agentcomm.MinLoopInterval)
	}
	prompt := strings.TrimSpace(a.Loop.Prompt)
	if prompt == "" {
		return errors.New("loop.prompt is required")
	}
	if len(prompt) > agentcomm.MaxLoopPromptBytes {
		return fmt.Errorf("loop.prompt is %d bytes, the limit is %d", len(prompt), agentcomm.MaxLoopPromptBytes)
	}
	return nil
}
