package assets

import (
	"embed"
	"os"
	"path/filepath"
	"strings"

	"github.com/akopichin/afm/schema"
)

//go:embed prompts
var FS embed.FS

//go:embed claude/skills
var SkillsFS embed.FS

// ReadPrompt returns a prompt by filename. An error is returned only if the embedded default is also
// unavailable.
func ReadPrompt(name, overrideDir string) (text string, fromOverride bool, err error) {
	if overrideDir != "" {
		if data, rerr := os.ReadFile(filepath.Join(overrideDir, name)); rerr == nil {
			return expandOutputSchema(name, string(data)), true, nil
		}
	}
	data, err := FS.ReadFile("prompts/" + name)
	return expandOutputSchema(name, string(data)), false, err
}

func expandOutputSchema(name, text string) string {
	contracts := map[string]schema.Contract{"helper-result.md": schema.AssistantResultOnly, "verify.md": schema.VerifyResult}
	if contract, ok := contracts[name]; ok && strings.Contains(text, "{{OUTPUT_SCHEMA}}") {
		return strings.ReplaceAll(text, "{{OUTPUT_SCHEMA}}", schema.JSON(contract))
	}
	return text
}
