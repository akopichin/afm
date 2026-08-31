package memory

import (
	"strings"
)

// SelectHigh extracts the text under the "## High" heading from prioritized content.
// It returns the text from the "## High" heading up to the next "## " heading (case-insensitive).
// If no "## High" section is found, it returns an empty string.
// The result is trimmed of leading/trailing whitespace.
func SelectHigh(prioritized string) string {
	lines := strings.Split(prioritized, "\n")

	highStart := -1
	highEnd := len(lines)

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Look for the "## High" heading
		if highStart == -1 {
			if strings.EqualFold(trimmed, "## High") {
				highStart = i + 1 // Start from the next line
			}
		} else {
			// We're inside the High section, look for the next "## " heading
			if strings.HasPrefix(trimmed, "## ") && !strings.EqualFold(trimmed, "## High") {
				highEnd = i
				break
			}
		}
	}

	// If no High section found, return empty string
	if highStart == -1 {
		return ""
	}

	// Extract lines from highStart to highEnd
	result := strings.Join(lines[highStart:highEnd], "\n")
	return strings.TrimSpace(result)
}
