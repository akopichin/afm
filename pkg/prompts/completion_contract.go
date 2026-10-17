package prompts

import (
	"fmt"
	"path/filepath"

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
