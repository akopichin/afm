package main

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const yesString = "yes"

// promptLine prints label to w, reads one line from scanner, trims whitespace.
func promptLine(scanner *bufio.Scanner, w io.Writer, label string) string {
	_, _ = fmt.Fprint(w, label)
	scanner.Scan()
	return strings.TrimSpace(scanner.Text())
}

// promptChoice prints a numbered menu (1-based) and reads a selection.
// Empty input selects defaultIdx (0-based). Out-of-range or non-numeric
// input re-prompts until a valid choice is entered.
func promptChoice(scanner *bufio.Scanner, w io.Writer, label string, options []string, defaultIdx int) int {
	_, _ = fmt.Fprintln(w, label)
	for i, opt := range options {
		marker := ""
		if i == defaultIdx {
			marker = " [default]"
		}
		_, _ = fmt.Fprintf(w, "  %d. %s%s\n", i+1, opt, marker)
	}
	for {
		raw := promptLine(scanner, w, "> ")
		if raw == "" {
			return defaultIdx
		}
		n, err := strconv.Atoi(raw)
		if err == nil && n >= 1 && n <= len(options) {
			return n - 1
		}
		_, _ = fmt.Fprintf(w, "Please enter a number between 1 and %d.\n", len(options))
	}
}

// promptYesNo asks a yes/no question. Empty or unrecognized input returns def.
func promptYesNo(scanner *bufio.Scanner, w io.Writer, label string, def bool) bool {
	raw := strings.ToLower(promptLine(scanner, w, label))
	switch raw {
	case "y", yesString:
		return true
	case "n", "no":
		return false
	default:
		return def
	}
}

// promptInt reads an integer. Empty or unparseable input returns def.
func promptInt(scanner *bufio.Scanner, w io.Writer, label string, def int) int {
	raw := promptLine(scanner, w, label)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return n
}

// parsePhaseSelection parses a comma-separated list of 1-based option
// indices (e.g. "1,2"). Empty input returns defaults unchanged. Invalid
// or out-of-range indices are silently skipped.
func parsePhaseSelection(raw string, numOptions int, defaults []int) []int {
	if strings.TrimSpace(raw) == "" {
		return defaults
	}
	var result []int
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		n, err := strconv.Atoi(part)
		if err != nil || n < 1 || n > numOptions {
			continue
		}
		result = append(result, n-1)
	}
	return result
}
