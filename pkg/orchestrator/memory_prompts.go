package orchestrator

import (
	"fmt"
	"strings"
)

// memoryAgentSpec — единый параметр для запуска одного агента конвейера памяти.
// Заполняются только поля, релевантные kind. Один seam (o.runMemoryAgent)
// принимает этот spec — так тесты подменяют реальный запуск процесса.
//
//nolint:unused
type memoryAgentSpec struct {
	kind      string // "reflect" | "updater" | "compressor"
	stageName string // для лога/имени
	command   string // разрешённая команда агента (пусто → дефолтный клиент)
	logFile   string // абс. путь к логу этого агента

	// reflect:
	sources    []string // абс. пути (файлы или директории) для чтения
	datasetOut string   // абс. путь, куда записать YAML-датасет

	// updater:
	datasetPath string // reflect_dataset.yaml
	projectPath string // PROJECT_MEMORY.md
	sessionPath string // SESSION_MEMORY.md

	// compressor:
	targetFile string // единственный файл для сжатия
	lineLimit  int    // >0 → добавить динамический «reduce to under N lines»
}

// buildMemoryPrompt оборачивает вкомпиленный шаблон (p.Reflect/Updater/
// Compressor) конкретными абсолютными путями и инструкциями файлового I/O.
func buildMemoryPrompt(p Prompts, spec memoryAgentSpec) string {
	switch spec.kind {
	case "reflect":
		var b strings.Builder
		b.WriteString(p.Reflect)
		b.WriteString("\n\n# AFM FILE I/O (added by afm)\n")
		b.WriteString("Read ONLY these sources (if a path is a directory, read every *.log file under it):\n")
		for _, s := range spec.sources {
			fmt.Fprintf(&b, "  - %s\n", s)
		}
		fmt.Fprintf(&b, "Write the resulting YAML document to this EXACT file, and write nothing else:\n  %s\n", spec.datasetOut)
		b.WriteString("Do not modify any other file. Do not ask questions.\n")
		return b.String()
	case "updater":
		var b strings.Builder
		b.WriteString(p.Updater)
		b.WriteString("\n\n# AFM FILE I/O (added by afm)\n")
		fmt.Fprintf(&b, "Input YAML dataset: %s\n", spec.datasetPath)
		fmt.Fprintf(&b, "PROJECT_MEMORY.md path: %s\n", spec.projectPath)
		fmt.Fprintf(&b, "SESSION_MEMORY.md path: %s\n", spec.sessionPath)
		b.WriteString("Read the dataset and both memory files (they may not exist yet — treat a missing file as empty). ")
		b.WriteString("Rewrite BOTH memory files IN PLACE at the exact paths above with the consolidated content. ")
		b.WriteString("Do not create any other file. Do not ask questions.\n")
		return b.String()
	case "compressor":
		var b strings.Builder
		b.WriteString(p.Compressor)
		b.WriteString("\n\n# AFM FILE I/O (added by afm)\n")
		fmt.Fprintf(&b, "Compress this file IN PLACE (overwrite the same path): %s\n", spec.targetFile)
		b.WriteString("Do not modify any other file. Do not ask questions.\n")
		if spec.lineLimit > 0 {
			fmt.Fprintf(&b, "\nCRITICAL: reduce the total line count of this file to under %d lines while preserving all core safety principles and architectural invariants.\n", spec.lineLimit)
		}
		return b.String()
	default:
		return ""
	}
}
