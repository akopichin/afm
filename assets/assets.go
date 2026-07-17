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

// ReadPrompt returns a prompt by filename. If overrideDir is non-empty and contains
// the file, that copy is used (fromOverride=true). Otherwise the embedded default is
// returned (fromOverride=false) — a custom prompts_dir need not provide every prompt
// (e.g. a dir with only planning/implementation/review still works, and the newer
// summary/autonomous prompts fall back to the compiled-in defaults). An error is
// returned only if the embedded default is also unavailable.
func ReadPrompt(name, overrideDir string) (text string, fromOverride bool, err error) {
	if overrideDir != "" {
		if data, rerr := os.ReadFile(filepath.Join(overrideDir, name)); rerr == nil {
			return string(data), true, nil
		}
	}
	data, err := FS.ReadFile("prompts/" + name)
	return string(data), false, err
}
