package prompts

import (
	"regexp"
	"strings"
)

type PlanIssues struct {
	MissingSections []string
}

var headingRE = regexp.MustCompile(`(?m)^##\s+(.+?)\s*$`)

// sectionAliases сопоставляет канонический (английский) раздел плана набору
// заголовков в нижнем регистре, которые его удовлетворяют.
//
// Планирующий агент пишет разделы на рабочем языке проекта: для русскоязычных
// флоу это «Задачи»/«Допущения»/«Критерии приёмки». Жёсткая проверка только
// английских названий приводила к ложным ошибкам «plan missing sections» и
// бесполезным повторным запросам — поэтому валидатор принимает локализованные
// эквиваленты наравне с каноническим именем.
//
// Для раздела, которого нет в карте, единственным допустимым вариантом остаётся
// сам канонический заголовок.
var sectionAliases = map[string][]string{
	"tasks":               {"tasks", "задачи"},
	"assumptions":         {"assumptions", "допущения", "предположения"},
	"acceptance criteria": {"acceptance criteria", "критерии приемки", "критерии приёмки"},
}

func ValidatePlan(md string, required []string) PlanIssues {
	matches := headingRE.FindAllStringSubmatch(md, -1)
	seen := make(map[string]bool, len(matches))
	for _, m := range matches {
		seen[strings.ToLower(strings.TrimSpace(m[1]))] = true
	}

	var missing []string
	for _, req := range required {
		if !anyHeadingPresent(seen, req) {
			missing = append(missing, req)
		}
	}
	return PlanIssues{MissingSections: missing}
}

// anyHeadingPresent reports whether the plan contains any heading that satisfies
// the required canonical section name (the canonical name itself or a known
// localized alias).
func anyHeadingPresent(seen map[string]bool, canonical string) bool {
	key := strings.ToLower(canonical)
	for _, alias := range sectionAliases[key] {
		if seen[alias] {
			return true
		}
	}
	return seen[key]
}

func (p PlanIssues) IsClean() bool { return len(p.MissingSections) == 0 }
