package orchestrator

import (
	"context"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

// The roster tells the dashboard which command each participant runs and which read-only guarantee backs it,
// so the people panel never guesses them from a model name: a stage agent's effective command, a helper's and
// a run agent's command and guarantee frozen into their task's profile snapshot. Script stages run no agent.
func TestRoster_ParticipantCommandAndReadonlyEnforcement(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true, flow.Stage{ID: "lint", Script: "make lint"})
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	helperTask := newRunningHelperForMesh(t, o, store, "op-roster-cmd")

	msg := "hello"
	added, err := o.AddRunAgent(ctx, AddRunAgentRequest{
		OperationID: "op-roster-run", ProfileID: "qa", Title: "QA", Instruction: "review", FirstMessage: &msg,
		Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Fatalf("AddRunAgent: %v", err)
	}
	// Its turn blocks on the same stub as the helper's; newRunningHelperForMesh's cleanup releases both.

	page, err := o.ListParticipants(ctx, ListParticipantsRequest{})
	if err != nil {
		t.Fatalf("ListParticipants: %v", err)
	}
	byID := map[agentcomm.ParticipantID]agentcomm.Participant{}
	for _, p := range page.Participants {
		byID[p.ID] = p
	}

	// A live task's badge is what THIS environment gives its command now: the test config has no codex
	// read-only adapter for "helper-cmd", so "none" — while the admission-time "os" stays as history.
	none, os := string(assistants.ReadonlyEnforcementNone), string(assistants.ReadonlyEnforcementOS)
	cases := []struct {
		name                     string
		id                       agentcomm.ParticipantID
		command, guard, admitted string
		reachable                bool
	}{
		{"stage agent", meshStageParticipantID(assistantsTestStageID), "claude", "", "", true},
		{"script stage", meshStageParticipantID("lint"), "", "", "", false},
		{"helper", meshHelperParticipantID(helperTask), "helper-cmd", none, os, true},
		{"run agent", added.ParticipantID, "helper-cmd", none, os, true},
		{"user", meshUserParticipantID, "", "", "", true},
	}
	for _, c := range cases {
		p, ok := byID[c.id]
		if !ok {
			t.Fatalf("%s: %s missing from the roster", c.name, c.id)
		}
		if p.Command != c.command || p.ReadonlyEnforcement != c.guard || p.AdmittedReadonlyEnforcement != c.admitted {
			t.Errorf("%s: command=%q readonly_enforcement=%q admitted=%q, want %q/%q/%q",
				c.name, p.Command, p.ReadonlyEnforcement, p.AdmittedReadonlyEnforcement, c.command, c.guard, c.admitted)
		}
		if p.Reachable != c.reachable {
			t.Errorf("%s: reachable=%v, want %v", c.name, p.Reachable, c.reachable)
		}
	}
	// A script stage runs no agent: nobody would read a message, whatever its status.
	if got := byID[meshStageParticipantID("lint")].UnreachableReason; got != scriptStageUnreachableReason {
		t.Errorf("script stage unreachable reason = %q, want %q", got, scriptStageUnreachableReason)
	}

	// Derived on read, never journaled: the run agent's stored descriptor carries none of the fields.
	stored := store.Snapshot().Assistants.Mesh.Participants[added.ParticipantID]
	if stored == nil || stored.Command != "" || stored.ReadonlyEnforcement != "" || stored.AdmittedReadonlyEnforcement != "" {
		t.Fatalf("journaled run participant = %+v, want no command/readonly fields", stored)
	}
}

// A finished task keeps the guarantee it ran under; a live one gets what the current environment gives its
// command (here the host or a container without the codex read-only adapter: "none").
func TestEffectiveReadonlyEnforcement(t *testing.T) {
	task := func(status assistants.TaskStatus) *state.TaskState {
		return &state.TaskState{Task: assistants.Task{
			Status:          status,
			ProfileSnapshot: assistants.Profile{Command: "helper-cmd", ReadonlyEnforcement: assistants.ReadonlyEnforcementOS},
		}}
	}
	if got := effectiveReadonlyEnforcement(task(assistants.StatusCompleted), config.Config{}); got != string(assistants.ReadonlyEnforcementOS) {
		t.Errorf("finished task = %q, want its admission-time os", got)
	}
	if got := effectiveReadonlyEnforcement(task(assistants.StatusRunning), config.Config{}); got != string(assistants.ReadonlyEnforcementNone) {
		t.Errorf("live task = %q, want the current environment's none", got)
	}
}
