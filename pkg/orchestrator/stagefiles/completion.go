package stagefiles

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/prompts"
)

// IncompleteWorkError signals a completion check that failed but is
// retryable once (missing/empty .done, missing plan sections, failing
// verify command). orchestrator.IncompleteWorkError is a type alias to
// this type (see pkg/orchestrator/errors.go) so errors.As continues to
// match across the package boundary.
type IncompleteWorkError struct{ Reason string }

func (e *IncompleteWorkError) Error() string { return "incomplete work: " + e.Reason }

// MissingArtifactError signals a declared stage artifact that never
// appeared on disk — not retryable. orchestrator.MissingArtifactError is a
// type alias to this type (see pkg/orchestrator/errors.go).
type MissingArtifactError struct{ Name string }

func (e *MissingArtifactError) Error() string { return "missing artifact: " + e.Name }

// RequiredPlanSections lists the "## <Section>" headings a plan.md must
// contain to pass prompts.ValidatePlan. Shared between the planning agents
// in pkg/orchestrator (which validate an agent's freshly written plan) and
// the plan completion/adoption checks in this package.
var RequiredPlanSections = []string{"Tasks", "Assumptions", "Acceptance Criteria"}

// CheckPlanCompletion verifies that plan.md exists and is not empty.
func CheckPlanCompletion(stageDir string) error {
	return CheckPlanCompletionFor(stageDir, false)
}

// CheckPlanCompletionFor verifies that plan.md exists and is not empty.
// For interactive stages it also validates required sections, returning
// IncompleteWorkError (retryable once) so runWithRetry can retry with log context.
func CheckPlanCompletionFor(stageDir string, interactive bool) error {
	data, err := os.ReadFile(filepath.Join(stageDir, "plan.md"))
	if err != nil {
		return fmt.Errorf("missing plan.md: %w", err)
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

// CheckAutonomousCompletion проверяет, что execution_summary.md существует и не пуст.
// Используется как completion-check для runAutonomousAgent (автономный трек).
// Возвращает IncompleteWorkError (retryable once) если файл отсутствует или пуст —
// агент получит один retry с контекстом ошибки.
func CheckAutonomousCompletion(stageDir string) error {
	data, err := os.ReadFile(filepath.Join(stageDir, "execution_summary.md"))
	if err != nil {
		return &IncompleteWorkError{Reason: "missing execution_summary.md"}
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return &IncompleteWorkError{Reason: "execution_summary.md is empty"}
	}
	return nil
}

// CheckCompletion verifies that .done exists and all declared artifacts are present.
// Returns IncompleteWorkError if .done is missing (retryable).
// Returns MissingArtifactError if an artifact is missing (not retryable).
func CheckCompletion(stageDir, projectDir string, stage flow.Stage) error {
	data, err := os.ReadFile(filepath.Join(stageDir, ".done"))
	if err != nil {
		return &IncompleteWorkError{Reason: "missing .done file"}
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return &IncompleteWorkError{Reason: ".done file is empty"}
	}

	for _, art := range stage.Artifacts {
		resolved := resolveArtifactPath(projectDir, filepath.Dir(stageDir), stage.ID, art.Path)
		if _, err := os.Stat(resolved); err != nil {
			return &MissingArtifactError{Name: art.Name}
		}
	}

	if stage.Verify != "" {
		if err := RunVerify(projectDir, stage.Verify); err != nil {
			return err
		}
	}

	return nil
}

// verifyOutputLimit caps how much verify command output goes into the error reason.
const verifyOutputLimit = 2000

// RunVerify executes the stage verify command via sh in the project directory.
// Non-zero exit returns IncompleteWorkError carrying the command output,
// so the stage gets one retry with the failure details.
func RunVerify(projectDir, command string) error {
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = projectDir
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	tail := strings.TrimSpace(string(out))
	if len(tail) > verifyOutputLimit {
		tail = "…" + tail[len(tail)-verifyOutputLimit:]
	}
	return &IncompleteWorkError{
		Reason: fmt.Sprintf("verify command failed (%v):\n%s", err, tail),
	}
}
