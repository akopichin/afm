package mcp

import (
	"strings"
)

// DialogSnippet returns the first non-empty line of text, trimmed and truncated. Truncation is rune-safe
// and never breaks multibyte characters.
func DialogSnippet(text string) string {
	// Split by lines
	lines := strings.Split(text, "\n")

	// Find the first non-empty line
	var firstLine string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			firstLine = trimmed
			break
		}
	}

	// Return empty if no non-empty line found
	if firstLine == "" {
		return ""
	}

	// Convert to runes for safe truncation
	runes := []rune(firstLine)

	// Truncate to 120 runes with ellipsis
	maxRunes := 120
	if len(runes) > maxRunes {
		return string(runes[:maxRunes]) + "…"
	}

	return firstLine
}

// DialogFeedNotice returns a map containing the phase, id, and title for use as both a live bus.Event.Data
// and stagefiles.AppendNotice Data payload.
func DialogFeedNotice(phase, id, title string) map[string]any {
	return map[string]any{
		"phase": phase,
		"id":    id,
		"title": title,
	}
}

// AutoAnsweredNotice returns the payload for a navigable auto_answered feed message: the
// phase/id/answer/from_options an auto-answer already carries, plus question_title and dialog:true.
func AutoAnsweredNotice(phase, id, answer string, fromOptions bool, questionTitle string) map[string]any {
	return map[string]any{
		"phase":          phase,
		"id":             id,
		"answer":         answer,
		"from_options":   fromOptions,
		"question_title": questionTitle,
		"dialog":         true,
	}
}
