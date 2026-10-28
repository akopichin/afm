package orchestrator

import (
	"errors"
	"fmt"

	"github.com/akopichin/afm/pkg/prompts"
	"github.com/akopichin/afm/pkg/skills"
)

// ErrSkillLibrary marks a skill library that cannot be used (unreadable, invalid or over the catalog limit).
// It does not change between attempts, so a retry cannot help and the stage fails at once.
var ErrSkillLibrary = errors.New("skill library is unusable")

// readSkillLibrary reads the extra skill library at dir (absolute) for a prompt; "" means none. A library
// that cannot be read, or whose catalog is over the limit, is an error: the turn must not start without the
// skills it was promised.
func readSkillLibrary(dir string) (prompts.SkillLibrary, error) {
	if dir == "" {
		return prompts.SkillLibrary{}, nil
	}
	catalog, err := skills.LoadCatalog(dir)
	if err != nil {
		return prompts.SkillLibrary{}, err
	}
	block, err := catalog.Block()
	if err != nil {
		return prompts.SkillLibrary{}, err
	}
	return prompts.SkillLibrary{Root: catalog.Root, Catalog: block}, nil
}

// CheckSkillLibrary reports whether the library at dir can be put into a prompt; "" is always fine.
func CheckSkillLibrary(dir string) error {
	_, err := readSkillLibrary(dir)
	return err
}

// buildStagePrompt is prompts.Build for a stage agent's turn, with the stage's skill library attached. Every
// stage phase builds its prompt through it, so a retry, a feedback run or a review reads the same library.
func buildStagePrompt(in prompts.Inputs) (string, error) {
	lib, err := readSkillLibrary(string(in.Stage.SkillsDir))
	if err != nil {
		return "", fmt.Errorf("%w: stage %q: %w", ErrSkillLibrary, in.Stage.ID, err)
	}
	in.SkillLibrary = lib
	return prompts.Build(in), nil
}
