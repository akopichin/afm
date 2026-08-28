package orchestrator

import (
	"fmt"
	"strings"
)

// memoryKindReflect/Consolidator — значения memoryAgentSpec.kind, общие с
// switch в buildMemoryPrompt (ниже) и с конвейером в reflection.go — единые
// константы, а не разбросанные строковые литералы (goconst).
const (
	memoryKindReflect      = "reflect"
	memoryKindConsolidator = "consolidator"
)

// memoryAgentSpec — единый параметр для запуска одного агента конвейера
// памяти. Заполняются только поля, релевантные kind. Один seam
// (o.runMemoryAgent) принимает этот spec — так тесты подменяют реальный
// запуск процесса.
type memoryAgentSpec struct {
	kind      string // "reflect" | "consolidator"
	stageName string // для лога/имени
	command   string // разрешённая команда агента (пусто → дефолтный клиент)
	logFile   string // абс. путь к логу этого агента

	// reflect:
	sources    []string // абс. пути (файлы или директории) для чтения
	datasetOut string   // абс. путь, куда записать YAML-датасет кандидатов (Store)

	// consolidator:
	datasetPath string // reflect_dataset.yaml — датасет-кандидат от reflect
	projectPath string // текущий PROJECT-скоуп стор (YAML, только на чтение агентом)
	sessionPath string // текущий SESSION-скоуп стор (YAML, только на чтение агентом)
	outPath     string // абс. путь, куда записать смёрженный YAML (MergedStore, со status)
}

// buildMemoryPrompt оборачивает вкомпиленный шаблон (p.Reflect/Consolidator)
// конкретными абсолютными путями и инструкциями файлового I/O.
func buildMemoryPrompt(p Prompts, spec memoryAgentSpec) string {
	switch spec.kind {
	case memoryKindReflect:
		var b strings.Builder
		b.WriteString(p.Reflect)
		b.WriteString("\n\n# AFM FILE I/O (added by afm)\n")
		b.WriteString("Read these sources (if a path is a directory, read every *.log file under it, plus execution_summary.md and plan.md if present):\n")
		for _, s := range spec.sources {
			fmt.Fprintf(&b, "  - %s\n", s)
		}
		fmt.Fprintf(&b, "Write the resulting YAML document (a Store: a top-level `findings:` list of candidate findings) to this EXACT file, and write nothing else:\n  %s\n", spec.datasetOut)
		b.WriteString("Do not modify any other file. Do not ask questions.\n")
		return b.String()
	case memoryKindConsolidator:
		var b strings.Builder
		b.WriteString(p.Consolidator)
		b.WriteString("\n\n# AFM FILE I/O (added by afm)\n")
		fmt.Fprintf(&b, "Candidate findings from this stage's reflect pass: %s\n", spec.datasetPath)
		fmt.Fprintf(&b, "Current PROJECT memory store: %s\n", spec.projectPath)
		fmt.Fprintf(&b, "Current SESSION memory store: %s\n", spec.sessionPath)
		b.WriteString("Read the candidate dataset and both current stores (any of these files may not exist yet — treat a missing file as an empty `findings: []` store). ")
		b.WriteString("Write the merged result as a single YAML document to this EXACT file, and write nothing else:\n")
		fmt.Fprintf(&b, "  %s\n", spec.outPath)
		b.WriteString("Every finding in the output MUST carry a `status` field: one of new, reinforced, unchanged. Do not modify any other file. Do not ask questions.\n")
		return b.String()
	default:
		return ""
	}
}
