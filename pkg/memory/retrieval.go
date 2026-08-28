package memory

import (
	"regexp"
	"strings"
)

var tokenizeRe = regexp.MustCompile(`[a-z0-9]+`)

type RetrievalConfig struct {
	Threshold        int
	CoreConfirmCount int
}

// Tokenize converts a string into a set-like slice of tokens.
// Lowercase, split on non-alphanumeric, drop tokens < 3 chars.
func Tokenize(s string) []string {
	s = strings.ToLower(s)
	// Split on non-alphanumeric
	tokens := tokenizeRe.FindAllString(s, -1)

	// Drop tokens < 3 chars and deduplicate via map
	seen := make(map[string]bool)
	var result []string
	for _, tok := range tokens {
		if len(tok) >= 3 && !seen[tok] {
			result = append(result, tok)
			seen[tok] = true
		}
	}

	return result
}

// Select filters findings by relevance and confidence.
// Returns (all findings, injectAll signal).
func Select(project, session Store, stageTokens []string, cfg RetrievalConfig) ([]Finding, bool) {
	total := len(project.Findings) + len(session.Findings)

	// Small store: return all
	if total <= cfg.Threshold {
		var all []Finding
		all = append(all, session.Findings...)
		all = append(all, project.Findings...)
		return all, true
	}

	// Large store: filter by core or relevant
	tokenSet := make(map[string]bool)
	for _, t := range stageTokens {
		tokenSet[t] = true
	}

	var result []Finding

	// Add all session findings first
	result = append(result, session.Findings...)

	// Track which project findings we've added
	addedSet := make(map[string]bool)
	for _, f := range session.Findings {
		addedSet[f.ID] = true
	}

	// Add project findings that are core or relevant
	for _, f := range project.Findings {
		if addedSet[f.ID] {
			continue // Skip duplicates
		}

		// Core: high confirm count
		if f.ConfirmCount >= cfg.CoreConfirmCount {
			result = append(result, f)
			addedSet[f.ID] = true
			continue
		}

		// Relevant: tokenize topic + statement
		findingTokens := tokenizeTopicAndStatement(f)
		if hasIntersection(findingTokens, tokenSet) {
			result = append(result, f)
			addedSet[f.ID] = true
		}
	}

	return result, false
}

func tokenizeTopicAndStatement(f Finding) map[string]bool {
	text := strings.Join(f.Topic, " ") + " " + f.Statement
	tokens := Tokenize(text)
	m := make(map[string]bool)
	for _, t := range tokens {
		m[t] = true
	}
	return m
}

func hasIntersection(a map[string]bool, b map[string]bool) bool {
	for k := range a {
		if b[k] {
			return true
		}
	}
	return false
}

// Render formats findings as a compact text block.
func Render(findings []Finding) string {
	if len(findings) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("## Agent Memory\n")

	for _, f := range findings {
		scope := f.Scope
		kind := f.Kind
		scopeKind := scope + "/" + kind
		topics := strings.Join(f.Topic, ",")
		if topics == "" {
			topics = "general"
		}
		sb.WriteString("- [" + scopeKind + "] " + f.Statement + " (topic: " + topics + ") — evidence: " + f.Evidence + "\n")
	}

	return sb.String()
}
