package stagefiles

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/prompts"
)

// IncompleteWorkError signals a completion check that failed but is retryable once.
type IncompleteWorkError struct{ Reason string }

func (e *IncompleteWorkError) Error() string { return "incomplete work: " + e.Reason }

// MissingArtifactError signals a declared stage artifact that never appeared on disk — not retryable.
type MissingArtifactError struct{ Name string }

func (e *MissingArtifactError) Error() string { return "missing artifact: " + e.Name }

// RequiredPlanSections lists the "## <Section>" headings a plan.md must contain to pass
// prompts.ValidatePlan.
var RequiredPlanSections = []string{"Tasks", "Assumptions", "Acceptance Criteria"}

// CheckPlanCompletion verifies that plan.md exists and is not empty.
func CheckPlanCompletion(stageDir string) error {
	return CheckPlanCompletionFor(stageDir, false)
}

// CheckPlanCompletionFor verifies that plan.md exists and is not empty.
func CheckPlanCompletionFor(stageDir string, interactive bool) error {
	planPath := filepath.Join(stageDir, "plan.md")
	data, err := os.ReadFile(planPath)
	if err != nil {
		return fmt.Errorf("missing %s: %w", planPath, err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return errors.New("plan.md is empty")
	}
	if interactive {
		issues := prompts.ValidatePlan(string(data), RequiredPlanSections)
		if !issues.IsClean() {
			return &IncompleteWorkError{Reason: "plan missing sections: " + strings.Join(issues.MissingSections, ", ")}
		}
	}
	return nil
}

// IsIncompleteWorkError checks if err is an incomplete work error (retryable once).
func IsIncompleteWorkError(err error) bool {
	if err == nil {
		return false
	}
	var target *IncompleteWorkError
	return errors.As(err, &target)
}

// checkMarkerFile reads a completion marker file (.done / execution_summary.md) and
// turns a failure into an *IncompleteWorkError whose text always carries the full
// path (taken from the very variable passed to os.ReadFile, so the message and the
// actual read target can never diverge) and distinguishes three separate causes:
// missing, empty, or unreadable for some other reason (bad permissions, the path
// being a directory, etc). label names the marker in the message, e.g. ".done file".
func checkMarkerFile(path, label string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &IncompleteWorkError{Reason: fmt.Sprintf("missing %s: %v", label, err)}
		}
		return &IncompleteWorkError{Reason: fmt.Sprintf("cannot read %s: %v", label, err)}
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return &IncompleteWorkError{Reason: fmt.Sprintf("%s is empty: %s", label, path)}
	}
	return nil
}

// CheckAutonomousCompletion проверяет, что execution_summary.md существует и не пуст. Используется как
// completion-check для runAutonomousAgent (автономный трек).
func CheckAutonomousCompletion(stageDir string) error {
	return checkMarkerFile(filepath.Join(stageDir, "execution_summary.md"), "execution_summary.md")
}

// CheckCompletion verifies that .done exists and all declared artifacts are present. Returns
// IncompleteWorkError if .done is missing (retryable).
func CheckCompletion(stageDir, projectDir string, stage flow.Stage) error {
	if err := checkMarkerFile(filepath.Join(stageDir, ".done"), ".done file"); err != nil {
		return err
	}

	for _, art := range stage.Artifacts {
		resolved := resolveArtifactPath(projectDir, filepath.Dir(stageDir), stage.ID, art.Path)
		if _, err := os.Stat(resolved); err != nil {
			return &MissingArtifactError{Name: art.Name}
		}
	}

	// CheckCompletion — чистый file-probe. Раньше здесь же гонялись shell-шаги V1 — это значило "verify
	// выполняется в двух местах", что и устраняет V4a.1.
	return nil
}

// VerifyOutputLimit caps how much verify shell-step output goes into the error reason/tail.
const VerifyOutputLimit = 2000

// TailOutput обрезает output до последних limit байт, добавляя многоточие спереди при обрезании.
func TailOutput(output string, limit int) string {
	tail := strings.TrimSpace(output)
	if len(tail) > limit {
		tail = "…" + tail[len(tail)-limit:]
	}
	return tail
}
