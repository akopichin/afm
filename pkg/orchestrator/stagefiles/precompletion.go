package stagefiles

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	preCompletionDirName      = "pre-completion"
	preCompletionFeedbackFile = "feedback.md"
	preCompletionReportFile   = "report.md"
)

// PreCompletionDir is the pre-completion namespace of a stage: one report per round and the active feedback.
func PreCompletionDir(stageDir string) string {
	return filepath.Join(stageDir, preCompletionDirName)
}

// PreCompletionReportPath is the full report of the round started by the transition roundSeq.
func PreCompletionReportPath(stageDir string, roundSeq uint64) string {
	return filepath.Join(PreCompletionDir(stageDir), fmt.Sprint(roundSeq), preCompletionReportFile)
}

// PreCompletionFeedbackPath is the active feedback, whichever round wrote it.
func PreCompletionFeedbackPath(stageDir string) string {
	return filepath.Join(PreCompletionDir(stageDir), preCompletionFeedbackFile)
}

// PreCompletionFeedbackMark opens the feedback of the round number round started by the transition roundSeq: the
// reader takes the file as that round's only by it.
func PreCompletionFeedbackMark(round int, roundSeq uint64) string {
	return fmt.Sprintf("<!-- afm-pre-completion round=%d seq=%d -->\n", round, roundSeq)
}

// WritePreCompletionRound stores the round's report and makes feedback the active feedback of the author's next turn;
// an empty one leaves none. The report goes first: the feedback links to it, so a half-written round never leaves
// feedback that points nowhere.
func WritePreCompletionRound(stageDir string, roundSeq uint64, report, feedback string) error {
	reportPath := PreCompletionReportPath(stageDir, roundSeq)
	if err := os.MkdirAll(filepath.Dir(reportPath), 0o755); err != nil {
		return fmt.Errorf("create pre-completion round dir: %w", err)
	}
	if err := atomicWriteFile(reportPath, []byte(report)); err != nil {
		return fmt.Errorf("write pre-completion report: %w", err)
	}
	if feedback == "" {
		return ClearPreCompletionFeedback(stageDir)
	}
	if err := atomicWriteFile(PreCompletionFeedbackPath(stageDir), []byte(feedback)); err != nil {
		return fmt.Errorf("write pre-completion feedback: %w", err)
	}
	return nil
}

// LoadPreCompletionFeedbackOf reads the active feedback when the round roundSeq (number round) wrote it; ok=false when
// there is none or the file is another round's.
func LoadPreCompletionFeedbackOf(stageDir string, round int, roundSeq uint64) (text string, ok bool) {
	data, err := os.ReadFile(PreCompletionFeedbackPath(stageDir))
	if err != nil || !strings.HasPrefix(string(data), PreCompletionFeedbackMark(round, roundSeq)) {
		return "", false
	}
	return string(data), true
}

// ClearPreCompletionFeedback drops the active feedback; the reports stay.
func ClearPreCompletionFeedback(stageDir string) error {
	if err := os.Remove(PreCompletionFeedbackPath(stageDir)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove active pre-completion feedback: %w", err)
	}
	return nil
}
