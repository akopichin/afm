package executor

import (
	"encoding/json"
	"slices"
	"strings"
)

// eventTypeUser is the stream-json type of a message sent to the model, including the CLI's own synthetic ones.
const eventTypeUser = "user"

// structuredOutputEnforceMarker opens the message the claude CLI injects when a native-schema turn ended without
// calling StructuredOutput: the payload that follows was forced, not volunteered.
const structuredOutputEnforceMarker = "[structured-output-enforce]"

// finalAnswer captures the answer a verify, helper or format run ends with. The last text block wins, so
// reasoning never mixes with the answer; in Claude's native schema mode the structured payload of the result
// event replaces any text.
type finalAnswer struct {
	claudeNative bool // the run uses Claude's native schema mode
	text         string
	structured   bool // text is Claude's structured payload
	enforced     bool // the CLI forced the model to give a payload
	// textBeforeEnforcement is the last text when the CLI first forced a payload: the model's own answer.
	textBeforeEnforcement string
}

// observeText records an assistant text block.
func (a *finalAnswer) observeText(text string) {
	a.text = text
}

// observeLine records what a line other than an assistant event adds to the answer and reports whether it added
// anything, so the caller does not also treat it as an error line.
func (a *finalAnswer) observeLine(line string) bool {
	if !a.claudeNative {
		return false
	}
	if payload, found := structuredPayload(line); found {
		a.text, a.structured = payload, true
		return true
	}
	if isEnforcementMessage(line) {
		if !a.enforced {
			a.textBeforeEnforcement = a.text
		}
		a.enforced = true
		return true
	}
	return false
}

// answer is the captured answer; blank means the run gave none.
func (a *finalAnswer) answer() string { return a.text }

// payloadMissing reports a clean native run that ended without a structured payload: its text is validated as usual.
func (a *finalAnswer) payloadMissing(runErr error) bool {
	return a.claudeNative && !a.structured && runErr == nil
}

// isEnforcementMessage reports the synthetic user message the CLI sends to force a StructuredOutput call.
func isEnforcementMessage(line string) bool {
	if !strings.Contains(line, structuredOutputEnforceMarker) {
		return false
	}
	var ev struct {
		Type    string         `json:"type"`
		Message *streamMessage `json:"message"`
	}
	if json.Unmarshal([]byte(line), &ev) != nil || ev.Type != eventTypeUser || ev.Message == nil {
		return false
	}
	return slices.ContainsFunc(ev.Message.Content, func(c streamContent) bool {
		return c.Type == contentTypeText && strings.HasPrefix(c.Text, structuredOutputEnforceMarker)
	})
}
