package orchestrator

import (
	"context"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
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

	cases := []struct {
		name           string
		id             agentcomm.ParticipantID
		command, guard string
	}{
		{"stage agent", meshStageParticipantID(assistantsTestStageID), "claude", ""},
		{"script stage", meshStageParticipantID("lint"), "", ""},
		{"helper", meshHelperParticipantID(helperTask), "helper-cmd", "os"},
		{"run agent", added.ParticipantID, "helper-cmd", "os"},
		{"user", meshUserParticipantID, "", ""},
	}
	for _, c := range cases {
		p, ok := byID[c.id]
		if !ok {
			t.Fatalf("%s: %s missing from the roster", c.name, c.id)
		}
		if p.Command != c.command || p.ReadonlyEnforcement != c.guard {
			t.Errorf("%s: command=%q readonly_enforcement=%q, want %q/%q", c.name, p.Command, p.ReadonlyEnforcement, c.command, c.guard)
		}
	}

	// Derived on read, never journaled: the run agent's stored descriptor carries neither field.
	stored := store.Snapshot().Assistants.Mesh.Participants[added.ParticipantID]
	if stored == nil || stored.Command != "" || stored.ReadonlyEnforcement != "" {
		t.Fatalf("journaled run participant = %+v, want no command/readonly_enforcement", stored)
	}
}
