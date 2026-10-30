package prompts

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/akopichin/afm/pkg/flow"
)

// CompletionContract — обязательный блок завершения, который afm строит САМ по контракту фазы.
// В шаблоне его держать нельзя: prompts_dir заменяет шаблон целиком, а гейт остаётся (D5). Поэтому блок
// доклеивается кодом последним в RetryContext — на каждой попытке, независимо от того, что лежит в
// implementation.md/review.md. Путь — литеральный абсолютный (не $AFM_STAGE_DIR): это убирает целый класс
// ошибок с CWD агента, а не маскирует его проверкой переменной.
func CompletionContract(phase flow.Phase, stageDir string) string {
	switch phase {
	case flow.PhaseImplementation, flow.PhaseReview:
		return markerFileContract(stageDir, ".done")
	case flow.PhaseAutonomous:
		return markerFileContract(stageDir, "execution_summary.md")
	default:
		// planning (и всё остальное) — свой контракт/секции, этот блок там не нужен.
		return ""
	}
}

// shellQuote оборачивает путь в двойные кавычки — простейший способ держать пример команды копипастно
// исполнимым, даже если в пути (stageDir) есть пробел.
func shellQuote(path string) string {
	return `"` + path + `"`
}

// markerFileContract строит текст контракта для одного файла-маркера завершения (".done" или
// "execution_summary.md"), общий для implementation/review и autonomous — различается только имя файла.
func markerFileContract(stageDir, markerName string) string {
	markerPath := filepath.Join(stageDir, markerName)
	quoted := shellQuote(markerPath)
	return fmt.Sprintf(`

## Completion contract (mandatory)
Only after the task is actually done and any checks have passed, write a non-empty summary of your work to this exact file:

  %s

A bare "touch" is not enough — the file must contain real content (what you changed, why, how you verified it). Example:

cat > %s <<'EOF'
Implemented X; ran the relevant checks; all green.
EOF
`, markerPath, quoted)
}

// SummaryCorrection is what AFM tells the author when its turn ended without a usable summary: problem is the
// probe's verdict ("missing" or "empty"), attempt/limit the correction's number and the budget. The author writes
// the file itself after checking its work; AFM never writes a placeholder.
func SummaryCorrection(summaryPath, problem string, attempt, limit int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n\n## Summary correction (%d of %d)\n\n", attempt, limit)
	fmt.Fprintf(&b, "AFM cannot finish this stage: the summary file is %s:\n\n  %s\n\n", problem, summaryPath)
	b.WriteString("Continue the current task; do not start it over. Check the working tree and the results of the checks you already ran. " +
		"If the work is finished, write the summary (what you changed, why, how you verified it) to that exact file. " +
		"If the summary already exists elsewhere, check that it is still accurate and move it there. " +
		"If some actions are unfinished, finish them before writing it. Do not call a check successful without its result.\n")
	if attempt >= limit {
		b.WriteString("\nThis is the last correction: the stage fails if the file is still not there afterwards.\n")
	}
	return b.String()
}

// SummaryReminder is the one Stop hook reminder the live session gets before it ends without a usable summary:
// the author is still in its session, so it only has to finish its checks and write the file. Background work that
// serves the task is left running.
func SummaryReminder(summaryPath, problem string) string {
	return fmt.Sprintf("AFM: the summary file is %s: %s\n\n"+
		"Do not finish yet. Check the working tree and the results of the checks and background tasks you started, and finish unfinished actions; "+
		"leave running whatever still serves the task. Then write the summary (what you changed, why, how you verified it) to that exact file. "+
		"Do not call a check successful without its result. This is the only reminder in this session.", problem, summaryPath)
}
