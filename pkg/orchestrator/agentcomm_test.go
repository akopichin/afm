package orchestrator

import (
	"context"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// Agent Mesh Phase 2, T2.1: ListParticipants.

func TestMesh_ListParticipantsIncludesUserStageAndHelper(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	taskID := newRunningHelperForMesh(t, o, store, "op-roster-1")

	page, err := o.ListParticipants(ctx, ListParticipantsRequest{Actor: state.Actor{Kind: "user"}})
	if err != nil {
		t.Fatalf("ListParticipants: %v", err)
	}

	byID := map[agentcomm.ParticipantID]agentcomm.Participant{}
	for _, p := range page.Participants {
		byID[p.ID] = p
	}

	user, ok := byID[meshUserParticipantID]
	if !ok || user.Kind != agentcomm.KindUser || !user.Reachable {
		t.Fatalf("expected reachable user participant, got %+v (ok=%v)", user, ok)
	}

	stageP, ok := byID[meshStageParticipantID(assistantsTestStageID)]
	if !ok || stageP.Kind != agentcomm.KindStageAgent || !stageP.Reachable {
		t.Fatalf("expected reachable stage participant, got %+v (ok=%v)", stageP, ok)
	}

	helperP, ok := byID[meshHelperParticipantID(taskID)]
	if !ok || helperP.Kind != agentcomm.KindHelper || !helperP.Reachable {
		t.Fatalf("expected reachable helper participant, got %+v (ok=%v)", helperP, ok)
	}
}

func TestMesh_TerminalStageStaysInRosterUnreachable(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	if _, ok := o.Trigger(assistantsTestStageID, bus.EvComplete, bus.GuardCtx{}, ""); !ok {
		t.Fatalf("EvComplete failed for %s", assistantsTestStageID)
	}

	page, err := o.ListParticipants(ctx, ListParticipantsRequest{})
	if err != nil {
		t.Fatalf("ListParticipants: %v", err)
	}
	var found *agentcomm.Participant
	for i := range page.Participants {
		if page.Participants[i].ID == meshStageParticipantID(assistantsTestStageID) {
			found = &page.Participants[i]
		}
	}
	if found == nil {
		t.Fatal("terminal stage removed from roster entirely")
	}
	if found.Reachable {
		t.Fatal("terminal stage should be unreachable, got reachable=true")
	}
	if found.UnreachableReason == "" {
		t.Fatal("expected a non-empty unreachable reason")
	}
}
