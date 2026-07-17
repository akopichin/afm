package orchestrator

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

// checkPlanCompletion verifies that plan.md exists and is not empty.
func checkPlanCompletion(stageDir string) error {
	return checkPlanCompletionFor(stageDir, false)
}

// checkPlanCompletionFor verifies that plan.md exists and is not empty.
// For interactive stages it also validates required sections, returning
// IncompleteWorkError (retryable once) so runWithRetry can retry with log context.
func checkPlanCompletionFor(stageDir string, interactive bool) error {
	data, err := os.ReadFile(filepath.Join(stageDir, "plan.md"))
	if err != nil {
		return fmt.Errorf("missing plan.md: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return errors.New("plan.md is empty")
	}
	if interactive {
		issues := prompts.ValidatePlan(string(data), requiredPlanSections)
		if !issues.IsClean() {
			return &IncompleteWorkError{Reason: "plan missing sections: " + strings.Join(issues.MissingSections, ", ")}
		}
	}
	return nil
}

// isIncompleteWorkError checks if err is an incomplete work error (retryable once).
func isIncompleteWorkError(err error) bool {
	if err == nil {
		return false
	}
	var target *IncompleteWorkError
	return errors.As(err, &target)
}

// checkAutonomousCompletion проверяет, что execution_summary.md существует и не пуст.
// Используется как completion-check для runAutonomousAgent (автономный трек).
// Возвращает IncompleteWorkError (retryable once) если файл отсутствует или пуст —
// агент получит один retry с контекстом ошибки.
func checkAutonomousCompletion(stageDir string) error {
	data, err := os.ReadFile(filepath.Join(stageDir, "execution_summary.md"))
	if err != nil {
		return &IncompleteWorkError{Reason: "missing execution_summary.md"}
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return &IncompleteWorkError{Reason: "execution_summary.md is empty"}
	}
	return nil
}

// checkCompletion verifies that .done exists and all declared artifacts are present.
// Returns incompleteWorkError if .done is missing (retryable).
// Returns missingArtifactError if an artifact is missing (not retryable).
func checkCompletion(stageDir, projectDir string, stage flow.Stage) error {
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
		if err := runVerify(projectDir, stage.Verify); err != nil {
			return err
		}
	}

	return nil
}

// verifyOutputLimit caps how much verify command output goes into the error reason.
const verifyOutputLimit = 2000

// runVerify executes the stage verify command via sh in the project directory.
// Non-zero exit returns IncompleteWorkError carrying the command output,
// so the stage gets one retry with the failure details.
func runVerify(projectDir, command string) error {
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
