package orchestrator

import (
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/state"
)

// rejectPreCompletionTopLevel refuses a pre_completion helper that has no author to review: a run agent, a stage
// that never reaches a completion (planning-only) and a stage that has not started yet.
func (o *Orchestrator) rejectPreCompletionTopLevel(trigger assistants.Trigger, scope assistants.Scope, stageID string, stageStarted bool) error {
	if trigger != assistants.TriggerPreCompletion {
		return nil
	}
	switch {
	case scope == assistants.ScopeRun:
		return attachRejected(assistants.CodeInvalidRequest, "trigger pre_completion is for a stage's helper — a run agent has no author turn to review")
	case !o.reg().Stage(stageID).IsExecutionStage():
		return attachRejected(assistants.CodeInvalidRequest, "stage %q has no completion to review (planning-only stage) — pre_completion helpers are not supported there", stageID)
	case !stageStarted:
		return attachRejected(assistants.CodeInvalidStatus, "stage %q has not started yet — attach a pre_completion helper once it runs", stageID)
	default:
		return nil
	}
}

// preCompletionWindowCheck refuses, under the store lock, a pre_completion helper that would miss the round it
// races: while a round is open, or from its acceptance to verify, a helper joining now would be reviewed by nobody.
func preCompletionWindowCheck(stageID string) func(rs *state.RunState) error {
	return func(rs *state.RunState) error {
		p := rs.Stages[stageID].PreCompletion
		switch {
		case p.Open():
			return attachRejected(assistants.CodeInvalidStatus, "stage %q is in pre-completion review round %d — attach the helper after the round ends", stageID, p.Round)
		case p != nil && p.Accepted:
			return attachRejected(assistants.CodeInvalidStatus, "stage %q passed its pre-completion review and is moving to verification — attach the helper after it", stageID)
		default:
			return nil
		}
	}
}
