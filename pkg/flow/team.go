package flow

import (
	"errors"
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/akopichin/afm/pkg/skills"
)

// YAML keys shared by the team: and assistants: object forms.
const (
	keyProfile     = "profile"
	keyTitle       = "title"
	keyInstruction = "instruction"
	keyMode        = "mode"
	keySkills      = "skills"
	keySkillsDir   = "skills_dir"
)

// TeamMember declares one run agent the flow adds before its stages start — the same agent the dashboard's
// "+ Add agent" creates (pkg/orchestrator's initTeam). ID is the declaration key: it names the agent's
// creation in the run journal ("team/<id>") so a resumed run finds it again, and is NOT the agent's @alias —
// that one is built from Title, with collision resolution.
type TeamMember struct {
	ID          string `yaml:"id"`
	Profile     string `yaml:"profile"`
	Title       string `yaml:"title"`
	Instruction string `yaml:"instruction,omitempty"`
	// Skills are the skills the agent must load before work; SkillsDir overrides the flow's library.
	Skills    skills.Names `yaml:"skills,omitempty"`
	SkillsDir skills.Dir   `yaml:"skills_dir,omitempty"`
}

// Team is the flow-level team: field, in declaration order (the order the agents are created in).
type Team []TeamMember

// UnmarshalYAML accepts only a list of objects with the known keys, so a typo'd key fails loudly.
func (t *Team) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.SequenceNode {
		return errTeamNotList
	}
	out := make(Team, 0, len(value.Content))
	for _, item := range value.Content {
		var m TeamMember
		if err := m.unmarshalMapping(item); err != nil {
			return err
		}
		out = append(out, m)
	}
	*t = out
	return nil
}

// rejectNullKey rejects "key:" with no value (or null) in the flow's top-level mapping, as the schema does:
// yaml.v3 never calls a field's UnmarshalYAML for a null node, so the decoded Flow cannot tell it from an
// absent key.
func rejectNullKey(doc *yaml.Node, key string, err error) error {
	if doc.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(doc.Content); i += 2 {
		if doc.Content[i].Value == key && doc.Content[i+1].ShortTag() == "!!null" {
			return err
		}
	}
	return nil
}

var errTeamNotList = errors.New("team must be a list of objects with id, profile, title and instruction")

func (m *TeamMember) unmarshalMapping(value *yaml.Node) error {
	if value.Kind != yaml.MappingNode {
		return errors.New("team: each member must be an object with id, profile, title and instruction")
	}
	seen := make(map[string]bool, len(value.Content)/2)
	for i := 0; i+1 < len(value.Content); i += 2 {
		key := value.Content[i].Value
		if seen[key] {
			return fmt.Errorf("team: duplicate field %q", key)
		}
		seen[key] = true
		var dst *string
		switch key {
		case keySkills:
			names, err := skills.ParseNames(value.Content[i+1])
			if err != nil {
				return fmt.Errorf("team: %w", err)
			}
			m.Skills = names
			continue
		case keySkillsDir:
			dir, err := skills.ParseDir(value.Content[i+1])
			if err != nil {
				return fmt.Errorf("team: %w", err)
			}
			m.SkillsDir = dir
			continue
		case "id":
			dst = &m.ID
		case keyProfile:
			dst = &m.Profile
		case keyTitle:
			dst = &m.Title
		case keyInstruction:
			dst = &m.Instruction
		default:
			return fmt.Errorf("team: unknown field %q; use id, profile, title, instruction, skills, skills_dir", key)
		}
		if err := value.Content[i+1].Decode(dst); err != nil {
			return fmt.Errorf("team: %s: %w", key, err)
		}
	}
	return nil
}

// validate checks every member on its own and that ids are unique. Whether a profile exists depends on
// config, so that is checked where config is known (afm run preflight, afm validate).
func (t Team) validate() error {
	seen := make(map[string]bool, len(t))
	for i, m := range t {
		if m.ID == "" {
			return fmt.Errorf("team[%d]: id is required", i)
		}
		if !StageIDSafe(m.ID) {
			return fmt.Errorf("team member %q: id must match [A-Za-z0-9._-] (non-empty, not \".\"/\"..\")", m.ID)
		}
		if seen[m.ID] {
			return fmt.Errorf("duplicate team member id %q", m.ID)
		}
		seen[m.ID] = true
		if m.Profile == "" {
			return fmt.Errorf("team member %q: profile is required", m.ID)
		}
		if m.Title == "" {
			return fmt.Errorf("team member %q: title is required (the agent's @name is made from it)", m.ID)
		}
	}
	return nil
}
