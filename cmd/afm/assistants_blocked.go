package main

import (
	"fmt"

	"github.com/akopichin/afm/pkg/state"
)

// explainAwaitingAssistants (T11.5) renders a CLI-actionable explanation for a stage parked in
// awaiting_assistants — naming the specific required helper task still blocking its completion barrier
// (state.AssistantsState.RequiredBlocker) instead of letting approve/retry/revise fall through to their
// generic "wrong status" error. States the two ways an operator can actually resolve it: let the
// collaboration workflow run its course (resume/retry the blocking task, or give it a follow-up), or
// explicitly waive the requirement from the dashboard if collaboration should not have blocked this run at
// all — including the case where collaboration is now disabled for this process but an older required branch
// from before it was disabled is still open.
//
// Returns "" when stageID is not currently awaiting_assistants at all — callers fall back to their own
// generic status-mismatch message in that case.
func explainAwaitingAssistants(store *state.Store, stageID string) string {
	if store.Get(stageID) != state.StatusAwaitingAssistants {
		return ""
	}
	rs := store.Snapshot()
	blocker, ok := rs.Assistants.RequiredBlocker(stageID)
	if !ok {
		// RequiredBlocker's own documented race: the barrier may have just closed between the status read
		// above and this one. Still more actionable than a bare "not failed/awaiting_approval".
		return fmt.Sprintf("stage %q is awaiting_assistants, but its required task(s) have no current blocker — try again in a moment", stageID)
	}
	title := blocker.Title
	if title == "" {
		title = string(blocker.ID)
	}
	return fmt.Sprintf(
		"stage %q is awaiting_assistants: blocked by required helper task %s (status %s, profile %s): %s.\n"+
			"To unblock it: resolve the task itself (let it finish, retry it, or give it a follow-up) from the "+
			"dashboard, or explicitly waive the requirement (POST /api/assistants/%s/waive) if collaboration "+
			"should not have required it here. If collaboration is disabled for this run (collaboration.enabled "+
			"/ AFM_COLLABORATION), re-enable it to interact with the task normally — a disabled feature does not "+
			"remove an already-open required branch.",
		stageID, blocker.ID, blocker.Status, blocker.ProfileID, title, blocker.ID,
	)
}
