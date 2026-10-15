package sideagent

import (
	"encoding/json"
	"regexp"
)

// ActionDraft is one typed, server-UNvalidated proposal extracted from a side-agent turn's final narration
// (dev-plan.md T4.4, §12.3). T4.5 is what actually validates Target/Payload against the proposed Type's own
// schema and executes it through a single operation path (mirrors assistants.Attach, Этап 3) — this type only
// carries the raw shape the model proposed. ExpectedRevision guards T4.5 against acting on a precondition the
// model reasoned about before someone else changed it (same optimistic-concurrency idea as
// state.Operation.Expect).
type ActionDraft struct {
	Type             string          `json:"type"`
	Target           json.RawMessage `json:"target,omitempty"`
	Payload          json.RawMessage `json:"payload,omitempty"`
	ExpectedRevision string          `json:"expected_revision,omitempty"`
}

const (
	// maxActionDraftsPerTurn bounds how many drafts one turn's narration can propose at once — a runaway or
	// adversarial response does not get to spam an unbounded number of pending confirmation cards.
	maxActionDraftsPerTurn = 5
	// maxActionDraftBytes bounds one draft's own encoded Target+Payload size — independent of the overall
	// message size limit (ContextBuilder.ByteLimit guards the OTHER direction, the user's input).
	maxActionDraftBytes = 4096
)

// actionDraftsFence matches a fenced code block whose info string is EXACTLY "action_drafts", anchored at
// the start of a line (multiline ^) — the one reserved channel action drafts can come from. Anchoring on
// line-start is what excludes a blockquoted example for free: markdown prefixes every line of a blockquote
// with "> ", so "> ```action_drafts" never matches "^```action_drafts" to begin with
// (TestActions_NotParsedFromQuotedText) — no separate blockquote-detection pass needed.
var actionDraftsFence = regexp.MustCompile("(?m)^```action_drafts\\s*\\n([\\s\\S]*?)\\n```\\s*$")

// extractActionDrafts parses the final narration text of a successfully completed turn for action drafts.
// Callers must pass ONLY the turn's final assistant_text content — never tool output (a completely separate
// event type the manager never feeds in here) and never an earlier/intermediate assistant_text block; this
// function itself has no notion of "which text block" — that discipline lives in the one call site
// (Manager.runTurn). known reports whether a draft's Type is recognized; nil fails CLOSED (same convention as
// Config.MentionResolve's nil — "capability off" must never silently pass everything through). Malformed
// JSON, or no fence at all, is not an error: it is simply zero drafts — a question about a role, or plain
// prose, must never fail the turn.
func extractActionDrafts(text string, known func(typ string) bool) []ActionDraft {
	matches := actionDraftsFence.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}
	// Last fence wins — mirrors the "last text block is the answer" convention already used by
	// RunHelperAgent/RunVerifyAgent: an agent that drafts an example then gives the real one last is common.
	raw := matches[len(matches)-1][1]
	var drafts []ActionDraft
	if err := json.Unmarshal([]byte(raw), &drafts); err != nil {
		return nil
	}
	var out []ActionDraft
	for _, d := range drafts {
		if known == nil || !known(d.Type) {
			continue
		}
		if len(d.Target)+len(d.Payload) > maxActionDraftBytes {
			continue
		}
		out = append(out, d)
		if len(out) == maxActionDraftsPerTurn {
			break
		}
	}
	return out
}
