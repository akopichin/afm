package orchestrator

import (
	"testing"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/state"
)

// TestQueuedTurnDue_HoldsTheRunUntilTheStartCommits: a queued task holds the run open while its turn is due —
// a resumed run agent between its resume and its start, a run agent with a message to read, a stage helper —
// but an idle run agent and a held task never do.
func TestQueuedTurnDue_HoldsTheRunUntilTheStartCommits(t *testing.T) {
	queued := func(id string, scope assistants.Scope, reason string) *state.TaskState {
		return &state.TaskState{Task: assistants.Task{ID: assistants.TaskID(id), Scope: scope, Status: assistants.StatusQueued, StatusReason: reason}}
	}
	for _, tc := range []struct {
		name string
		task *state.TaskState
		held bool
		want bool
	}{
		{"idle run agent", queued("idle", assistants.ScopeRun, runAgentIdleReason), false, false},
		{"run agent attached without a message", queued("fresh", assistants.ScopeRun, ""), false, false},
		{"resumed run agent", queued("resumed", assistants.ScopeRun, "resumed: mesh wait ready"), false, true},
		{"queued stage helper", queued("helper", assistants.ScopeStage, ""), false, true},
		{"held task", queued("held", assistants.ScopeStage, "re-queued: paused while running"), true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rs := *state.NewRunState(nil)
			if tc.held {
				tc.task.Holds = map[string]state.Hold{holdOwnerManual: {}}
			}
			rs.Assistants.Tasks = map[assistants.TaskID]*state.TaskState{tc.task.ID: tc.task}
			if got := queuedTurnDue(rs); got != tc.want {
				t.Fatalf("queuedTurnDue = %v, want %v", got, tc.want)
			}
		})
	}
}
