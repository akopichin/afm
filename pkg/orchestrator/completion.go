package orchestrator

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/akopichin/afm/pkg/flow"
)

// checkPlanCompletion verifies that plan.md exists and is not empty.
func checkPlanCompletion(stageDir string) error {
	data, err := os.ReadFile(filepath.Join(stageDir, "plan.md"))
	if err != nil {
		return fmt.Errorf("missing plan.md: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return errors.New("plan.md is empty")
	}
	return nil
}

// incompleteWorkError signals that the agent exited successfully but didn't
// create the .done marker. This is retryable (1 attempt).
type incompleteWorkError struct {
	reason string
}

func (e *incompleteWorkError) Error() string {
	return "incomplete work: " + e.reason
}

// isIncompleteWorkError checks if err is an incomplete work error (retryable once).
func isIncompleteWorkError(err error) bool {
	if err == nil {
		return false
	}
	var target *incompleteWorkError
	return errors.As(err, &target)
}

// missingArtifactError signals that a declared artifact is missing. Not retryable.
type missingArtifactError struct {
	name string
}

func (e *missingArtifactError) Error() string {
	return "missing artifact: " + e.name
}

// checkCompletion verifies that .done exists and all declared artifacts are present.
// Returns incompleteWorkError if .done is missing (retryable).
// Returns missingArtifactError if an artifact is missing (not retryable).
func checkCompletion(stageDir, projectDir string, stage flow.Stage) error {
	data, err := os.ReadFile(filepath.Join(stageDir, ".done"))
	if err != nil {
		return &incompleteWorkError{reason: "missing .done file"}
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return &incompleteWorkError{reason: ".done file is empty"}
	}

	for _, art := range stage.Artifacts {
		resolved := resolveArtifactPath(projectDir, filepath.Dir(stageDir), stage.ID, art.Path)
		if _, err := os.Stat(resolved); err != nil {
			return &missingArtifactError{name: art.Name}
		}
	}

	return nil
}
