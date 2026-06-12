package prompts

import (
	"regexp"
	"strings"
)

type PlanIssues struct {
	MissingSections []string
}

var headingRE = regexp.MustCompile(`(?m)^##\s+(.+?)\s*$`)

func ValidatePlan(md string, required []string) PlanIssues {
	matches := headingRE.FindAllStringSubmatch(md, -1)
	seen := make(map[string]bool, len(matches))
	for _, m := range matches {
		seen[strings.ToLower(strings.TrimSpace(m[1]))] = true
	}

	var missing []string
	for _, req := range required {
		if !seen[strings.ToLower(req)] {
			missing = append(missing, req)
		}
	}
	return PlanIssues{MissingSections: missing}
}

func (p PlanIssues) IsClean() bool { return len(p.MissingSections) == 0 }
