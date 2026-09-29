package mcp

import (
	"strings"
)

// DialogSnippet returns the first non-empty line of text, trimmed and truncated.
//
// It skips leading blank lines, extracts the first non-empty line, trims
// surrounding whitespace, and truncates to at most 120 runes. If truncation
// occurs, a single ellipsis character (…, U+2026) is appended.
// Truncation is rune-safe and never breaks multibyte characters.
// Empty or all-whitespace input returns an empty string.
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

// DialogFeedNotice returns a map containing the phase, id, and title for use
// as both a live bus.Event.Data and stagefiles.AppendNotice Data payload.
// This ensures both the live event and the persisted notice have identical JSON.
func DialogFeedNotice(phase, id, title string) map[string]any {
	return map[string]any{
		"phase": phase,
		"id":    id,
		"title": title,
	}
}

// AutoAnsweredNotice returns the payload for a navigable auto_answered feed
// message: the phase/id/answer/from_options an auto-answer already carries, plus
// question_title (a bounded snippet of the question) and dialog:true. The
// dialog:true marker is what tells the dashboard this notice is navigable to the
// full dialog history — the non-navigable ⚙️ repair-progress notice never sets
// it. The same map is used live and durable so their JSON stays byte-identical.
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
