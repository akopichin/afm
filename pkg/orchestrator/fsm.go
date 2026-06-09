package orchestrator

import "github.com/akopichin/afm/pkg/state"

// validTransitions defines allowed FSM transitions between stage statuses.
var validTransitions = map[state.StageStatus][]state.StageStatus{
	state.StatusPending:           {state.StatusPlanning, state.StatusReady, state.StatusFailed},
	state.StatusPlanning:          {state.StatusAwaitingApproval, state.StatusFailed, state.StatusRetrying, state.StatusAwaitingUserInput},
	state.StatusAwaitingApproval:  {state.StatusReady, state.StatusRevising},
	state.StatusRevising:          {state.StatusPlanning},
	state.StatusReady:             {state.StatusRunning},
	state.StatusRunning:           {state.StatusDone, state.StatusFailed, state.StatusRetrying, state.StatusAwaitingUserInput},
	state.StatusRetrying:          {state.StatusRunning, state.StatusPlanning, state.StatusFailed, state.StatusDone, state.StatusAwaitingApproval},
	state.StatusAwaitingUserInput: {state.StatusRunning, state.StatusPlanning, state.StatusFailed},
	state.StatusFailed:            {state.StatusPending},
}

// ValidTransition checks if transitioning from one status to another is allowed.
func ValidTransition(from, to state.StageStatus) bool {
	targets, ok := validTransitions[from]
	if !ok {
		return false
	}
	for _, t := range targets {
		if t == to {
			return true
		}
	}
	return false
}

// IsTerminal returns true for statuses that represent a final state.
func IsTerminal(s state.StageStatus) bool {
	return s == state.StatusDone || s == state.StatusFailed
}
