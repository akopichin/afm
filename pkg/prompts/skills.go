package prompts

import (
	"fmt"
	"strings"

	"github.com/akopichin/afm/pkg/skills"
)

// SkillLibrary is the extra skill library an agent may read: its root and the pre-rendered catalog lines
// ("- name: description (path)"). The zero value means no library.
type SkillLibrary struct {
	Root    string
	Catalog string
}

// SkillsBlock renders the <skills> block shared by stage prompts and run-agent turns: the required skills
// and/or the library catalog, with the loading rules. It returns "" when there is nothing to say. Every
// user-supplied value is escaped, since names, descriptions and paths come from YAML and from library files.
func SkillsBlock(required []string, lib SkillLibrary) string {
	if len(required) == 0 && lib.Catalog == "" {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("<skills>\n")
	if len(required) > 0 {
		fmt.Fprintf(&sb, "Required skills, in this order: %s.\n", escapeTags(strings.Join(required, ", ")))
		sb.WriteString("Load every required skill before you start the task, using your native skill mechanism first.\n")
		if lib.Catalog != "" {
			sb.WriteString("Only if a required skill is not available natively, find its exact name in the library catalog below and read the listed SKILL.md.\n")
		}
		sb.WriteString("Apply the skill's instructions while you work; open its scripts/ and references/ (next to its SKILL.md) when it points to them.\n")
		sb.WriteString("If a required skill is available in none of these places, say so before you do any work, do not substitute a similar skill, ")
		sb.WriteString("then continue with the tools you have and name the missing skill in your final answer.\n")
	}
	if lib.Catalog != "" {
		fmt.Fprintf(&sb, "Library %s:\n", escapeTags(skills.SingleLine(lib.Root)))
		if len(required) == 0 {
			sb.WriteString("Choose the skills from this catalog that fit your task and read their SKILL.md before using them; none of them is required.\n")
		}
		sb.WriteString(escapeTags(lib.Catalog))
	}
	sb.WriteString("</skills>\n")
	return sb.String()
}
