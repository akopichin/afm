package orchestrator

import (
	"context"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
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

// The roster says what a run agent or helper is doing now: its task's status, with an idle run agent (queued,
// nothing to read) told apart from one whose next turn is due.
func TestTaskActivity(t *testing.T) {
	task := func(status assistants.TaskStatus) *state.TaskState {
		return &state.TaskState{Task: assistants.Task{Scope: assistants.ScopeRun, Status: status}}
	}
	held := task(assistants.StatusRunning)
	held.Holds = map[string]state.Hold{"user": {}}
	for _, tc := range []struct {
		name string
		task *state.TaskState
		idle bool
		want agentcomm.Activity
	}{
		{"running", task(assistants.StatusRunning), false, agentcomm.ActivityWorking},
		{"queued with a turn due", task(assistants.StatusQueued), false, agentcomm.ActivityWorking},
		{"queued with nothing to read", task(assistants.StatusQueued), true, agentcomm.ActivityIdle},
		{"waiting for a colleague", task(assistants.StatusWaitingPeer), false, agentcomm.ActivityWaiting},
		{"waiting for children", task(assistants.StatusWaitingChildren), false, agentcomm.ActivityWaiting},
		{"paused", task(assistants.StatusPaused), false, agentcomm.ActivityPaused},
		{"held while running", held, false, agentcomm.ActivityPaused},
		{"completed", task(assistants.StatusCompleted), false, agentcomm.ActivityDone},
	} {
		if got := taskActivity(tc.task, tc.idle); got != tc.want {
			t.Errorf("%s: activity = %q, want %q", tc.name, got, tc.want)
		}
	}
}
