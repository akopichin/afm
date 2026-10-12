package memorypipeline

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// tierHeadings — legacy "## High/Medium/Low" section headings from the staging prioritized.md.
var tierHeadings = []string{"## High", "## Medium", "## Low"}

// ValidateRules requires UTF-8, '# Project rules', named H2 patterns without tier headings, and
// rule-count/size limits.
func ValidateRules(md string, maxRules int) error {
	if len(md) > maxRulesBytes {
		return fmt.Errorf("rules file too large: %d bytes (limit %d)", len(md), maxRulesBytes)
	}
	if !utf8.ValidString(md) {
		return errors.New("rules file is not valid UTF-8")
	}

	lines := strings.Split(md, "\n")

	headerLine := -1
	for i, line := range lines {
		if strings.TrimSpace(line) != "" {
			headerLine = i
			break
		}
	}
	if headerLine == -1 || strings.TrimSpace(lines[headerLine]) != "# Project rules" {
		return errors.New(`rules file must start with "# Project rules" as its first non-blank line`)
	}

	patternCount := 0
	for i, line := range lines {
		if i == headerLine {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "# ") {
			return fmt.Errorf("rules file must not contain another top-level heading: %q", trimmed)
		}
		if !strings.HasPrefix(trimmed, "## ") {
			continue
		}
		for _, tier := range tierHeadings {
			if strings.EqualFold(trimmed, tier) {
				return fmt.Errorf("rules file must not contain a tier heading %q — priority is order-only", trimmed)
			}
		}
		patternCount++
	}

	if patternCount == 0 {
		return errors.New(`rules file must contain at least one "## <Pattern Name>" block`)
	}
	if maxRules > 0 && patternCount > maxRules {
		return fmt.Errorf("rules file has %d pattern blocks, exceeds max_rules=%d", patternCount, maxRules)
	}
	return nil
}
