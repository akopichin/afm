package assistants

// ActionType enumerates the kinds of typed action draft a side-agent turn may propose (dev-plan.md T4.4,
// §12.3). Extraction-time validation (pkg/sideagent's extractActionDrafts) rejects any other value outright —
// a draft never reaches T4.5's RunOperation translation carrying an unrecognized type.
type ActionType string

// ActionAttachAssistant proposes creating one new helper task (mirrors Orchestrator.Attach, Этап 3) — the
// only draft type Этап 4 implements end-to-end (T4.5's validation, T4.6's confirmation card).
const ActionAttachAssistant ActionType = "attach_assistant"

var knownActionTypes = map[ActionType]struct{}{
	ActionAttachAssistant: {},
}

// KnownActionType reports whether typ is a recognized action draft type — the narrow predicate
// pkg/sideagent.Config.KnownActionType is built from (pkg/sideagent does not import pkg/assistants, same
// injection pattern as Config.MentionResolve).
func KnownActionType(typ string) bool {
	_, ok := knownActionTypes[ActionType(typ)]
	return ok
}
