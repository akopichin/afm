package memorypipeline

import (
	"fmt"
	"strconv"
	"strings"
)

// BuildPrompt оборачивает вкомпиленный шаблон (p.Reflect/Aggregate/
// Prioritize/Update) конкретными абсолютными путями и инструкциями файлового
// I/O.
func BuildPrompt(p Prompts, spec AgentSpec) string {
	switch spec.Kind {
	case KindReflect:
		var b strings.Builder
		b.WriteString(p.Reflect)
		b.WriteString("\n\n# AFM FILE I/O (added by afm)\n")
		b.WriteString("Read these sources (if a path is a directory, read every *.log file under it, plus execution_summary.md and plan.md if present, AND any direct user input in the stage dir — *.dialog.jsonl (user dialog answers), prenote.md and feedback.md (user notes) — if present):\n")
		for _, s := range spec.Sources {
			fmt.Fprintf(&b, "  - %s\n", s)
		}
		fmt.Fprintf(&b, "Write the resulting YAML dataset to this EXACT file, and write nothing else:\n  %s\n", spec.DatasetOut)
		b.WriteString("Do not modify any other file. Do not ask questions.\n")
		return b.String()
	case KindAggregate:
		var b strings.Builder
		b.WriteString(p.Aggregate)
		b.WriteString("\n\n# AFM FILE I/O (added by afm)\n")
		b.WriteString("Read these dataset files:\n")
		for _, s := range spec.InPaths {
			fmt.Fprintf(&b, "  - %s\n", s)
		}
		fmt.Fprintf(&b, "Write the numbered pattern list to this EXACT file, and write nothing else:\n  %s\n", spec.Out)
		b.WriteString("Do not modify any other file. Do not ask questions.\n")
		return b.String()
	case KindPrioritize:
		var b strings.Builder
		b.WriteString(p.Prioritize)
		b.WriteString("\n\n# AFM FILE I/O (added by afm)\n")
		fmt.Fprintf(&b, "Read the patterns from this file:\n  %s\n", spec.In)
		fmt.Fprintf(&b, "Write the prioritized High/Medium/Low sections to this EXACT file, and write nothing else:\n  %s\n", spec.Out)
		b.WriteString("Do not modify any other file. Do not ask questions.\n")
		return b.String()
	case KindUpdate:
		tmpl := p.Update
		tmpl = strings.ReplaceAll(tmpl, "<FILEPATH>", spec.TargetFile)
		tmpl = strings.ReplaceAll(tmpl, "<MAX_RULES>", strconv.Itoa(spec.MaxRules))
		var b strings.Builder
		b.WriteString(tmpl)
		b.WriteString("\n\n# AFM FILE I/O (added by afm)\n")
		fmt.Fprintf(&b, "The current high-priority patterns are in this file:\n  %s\n", spec.HighPath)
		fmt.Fprintf(&b, "Read it and the existing target file (it may not exist yet — treat that as empty):\n  %s\n", spec.TargetFile)
		fmt.Fprintf(&b, "Rewrite %s in place with the merged result, and write nothing else.\n", spec.TargetFile)
		b.WriteString("Do not modify any other file. Do not ask questions.\n")
		return b.String()
	default:
		return ""
	}
}
