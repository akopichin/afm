package assets

import (
	"embed"
	"os"
	"path/filepath"
)

//go:embed prompts
var FS embed.FS

//go:embed claude/skills
var SkillsFS embed.FS

// ReadPrompt returns a prompt by filename. If overrideDir is non-empty,
// reads from that directory instead of the embedded files.
func ReadPrompt(name, overrideDir string) (string, error) {
	if overrideDir != "" {
		data, err := os.ReadFile(filepath.Join(overrideDir, name))
		return string(data), err
	}
	data, err := FS.ReadFile("prompts/" + name)
	return string(data), err
}
