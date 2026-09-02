package orchestrator

import (
	"fmt"
	"strconv"
	"strings"
)

// memoryKindReflect/Aggregate/Prioritize/Update — значения memoryAgentSpec.kind,
// общие с switch в buildMemoryPrompt (ниже) и с конвейером в reflection.go —
// единые константы, а не разбросанные строковые литералы (goconst).
const (
	memoryKindReflect    = "reflect"
	memoryKindAggregate  = "aggregate"
	memoryKindPrioritize = "prioritize"
	memoryKindUpdate     = "update"
)

// memoryAgentSpec — единый параметр для запуска одного агента конвейера
// памяти. Заполняются только поля, релевантные kind. Один seam
// (o.runMemoryAgent) принимает этот spec — так тесты подменяют реальный
// запуск процесса.
type memoryAgentSpec struct {
	kind      string // "reflect" | "aggregate" | "prioritize" | "update"
	stageName string // для лога/имени
	command   string // разрешённая команда агента (пусто → дефолтный клиент)
	logFile   string // абс. путь к логу этого агента

	// reflect:
	sources    []string // абс. пути (файлы или директории) для чтения
	datasetOut string   // абс. путь, куда записать YAML-датасет (project_level/session_level)

	// aggregate: inPaths = датасет-файлы (reflect_dataset.yaml, один или несколько
	// за end-of-run проход), out = абс. путь для patterns.md.
	// prioritize: in = patterns.md, out = абс. путь для prioritized.md.
	// (одни и те же поля переиспользуются между aggregate и prioritize — так
	// проще, чем заводить по паре полей на каждый шаг ради симметрии.)
	inPaths []string
	in      string
	out     string

	// update:
	highPath   string // абс. путь к high.md (High-паттерны, отобранные кодом)
	targetFile string // абс. путь к файлу памяти, который нужно переписать
	maxRules   int    // предел количества паттернов в targetFile
}

// buildMemoryPrompt оборачивает вкомпиленный шаблон (p.Reflect/Aggregate/
// Prioritize/Update) конкретными абсолютными путями и инструкциями файлового
// I/O.
func buildMemoryPrompt(p Prompts, spec memoryAgentSpec) string {
	switch spec.kind {
	case memoryKindReflect:
		var b strings.Builder
		b.WriteString(p.Reflect)
		b.WriteString("\n\n# AFM FILE I/O (added by afm)\n")
		b.WriteString("Read these sources (if a path is a directory, read every *.log file under it, plus execution_summary.md and plan.md if present, AND any direct user input in the stage dir — *.dialog.jsonl (user dialog answers), prenote.md and feedback.md (user notes) — if present):\n")
		for _, s := range spec.sources {
			fmt.Fprintf(&b, "  - %s\n", s)
		}
		fmt.Fprintf(&b, "Write the resulting YAML dataset to this EXACT file, and write nothing else:\n  %s\n", spec.datasetOut)
		b.WriteString("Do not modify any other file. Do not ask questions.\n")
		return b.String()
	case memoryKindAggregate:
		var b strings.Builder
		b.WriteString(p.Aggregate)
		b.WriteString("\n\n# AFM FILE I/O (added by afm)\n")
		b.WriteString("Read these dataset files:\n")
		for _, s := range spec.inPaths {
			fmt.Fprintf(&b, "  - %s\n", s)
		}
		fmt.Fprintf(&b, "Write the numbered pattern list to this EXACT file, and write nothing else:\n  %s\n", spec.out)
		b.WriteString("Do not modify any other file. Do not ask questions.\n")
		return b.String()
	case memoryKindPrioritize:
		var b strings.Builder
		b.WriteString(p.Prioritize)
		b.WriteString("\n\n# AFM FILE I/O (added by afm)\n")
		fmt.Fprintf(&b, "Read the patterns from this file:\n  %s\n", spec.in)
		fmt.Fprintf(&b, "Write the prioritized High/Medium/Low sections to this EXACT file, and write nothing else:\n  %s\n", spec.out)
		b.WriteString("Do not modify any other file. Do not ask questions.\n")
		return b.String()
	case memoryKindUpdate:
		tmpl := p.Update
		tmpl = strings.ReplaceAll(tmpl, "<FILEPATH>", spec.targetFile)
		tmpl = strings.ReplaceAll(tmpl, "<MAX_RULES>", strconv.Itoa(spec.maxRules))
		var b strings.Builder
		b.WriteString(tmpl)
		b.WriteString("\n\n# AFM FILE I/O (added by afm)\n")
		fmt.Fprintf(&b, "The current high-priority patterns are in this file:\n  %s\n", spec.highPath)
		fmt.Fprintf(&b, "Read it and the existing target file (it may not exist yet — treat that as empty):\n  %s\n", spec.targetFile)
		fmt.Fprintf(&b, "Rewrite %s in place with the merged result, and write nothing else.\n", spec.targetFile)
		b.WriteString("Do not modify any other file. Do not ask questions.\n")
		return b.String()
	default:
		return ""
	}
}
