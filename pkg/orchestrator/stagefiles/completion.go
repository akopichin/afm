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

// Причины, по которым execution_summary.md не принят: файла нет либо он пуст.
const (
	SummaryMissing = "missing"
	SummaryEmpty   = "empty"
)

// SummaryError — summary автономной стадии отсутствует или пуст; это единственная причина, которая даёт
// право на summary-recovery. Намеренно не *IncompleteWorkError: .done, план и verify не тратят этот бюджет.
type SummaryError struct {
	Path   string
	Reason string // SummaryMissing | SummaryEmpty
}

func (e *SummaryError) Error() string {
	if e.Reason == SummaryEmpty {
		return "execution_summary.md is empty"
	}
	return "missing execution_summary.md"
}

// SummaryPath возвращает путь execution_summary.md внутри каталога стадии.
func SummaryPath(stageDir string) string { return filepath.Join(stageDir, "execution_summary.md") }

// readSummaryFile — шов для тестов: EACCES под root нельзя получить через chmod.
var readSummaryFile = os.ReadFile

// CheckAutonomousCompletion проверяет, что execution_summary.md существует и не пуст. Нет файла или он
// пуст — *SummaryError; любая другая ошибка чтения возвращается как есть (с *fs.PathError внутри).
func CheckAutonomousCompletion(stageDir string) error {
	path := SummaryPath(stageDir)
	data, err := readSummaryFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &SummaryError{Path: path, Reason: SummaryMissing}
	}
	if err != nil {
		return fmt.Errorf("read execution_summary.md: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return &SummaryError{Path: path, Reason: SummaryEmpty}
	}
	return nil
}

// CheckCompletion verifies that .done exists and all declared artifacts are present. Returns
// IncompleteWorkError if .done is missing (retryable).
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
