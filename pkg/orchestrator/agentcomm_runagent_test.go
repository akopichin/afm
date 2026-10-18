package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/state"
)

// Agent Mesh Phase 6, T6.2b: AddRunAgent, the scope=run caller of AttachAndSend.

// TestAddRunAgent_IdempotentPerRequest: replaying the SAME "Add run agent" request (a double-click, a retry
// after a lost response — the dashboard reuses one request id per dialog) resolves to the same participant,
// task and thread, never a second agent.
func TestAddRunAgent_IdempotentPerRequest(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	stubPassingHelper(o)

	msg := "hello QA"
	req := AddRunAgentRequest{
		OperationID: "op-add-qa", ProfileID: "qa", Title: "My QA", Instruction: "watch for regressions", FirstMessage: &msg,
		Actor: state.Actor{Kind: "user"},
	}
	r1, err := o.AddRunAgent(ctx, req)
	if err != nil {
		t.Fatalf("AddRunAgent (first): %v", err)
	}
	r2, err := o.AddRunAgent(ctx, req)
	if err != nil {
		t.Fatalf("AddRunAgent (replay): %v", err)
	}
	waitForRunAgentTurn(t, store, r1.TaskID, 2*time.Second)
	if r1.TaskID != r2.TaskID || r1.ParticipantID != r2.ParticipantID || r1.ThreadID != r2.ThreadID || *r1.MessageID != *r2.MessageID {
		t.Fatalf("replay created a different result: first=%+v replay=%+v", r1, r2)
	}
	if n := len(store.Snapshot().Assistants.Tasks); n != 1 {
		t.Fatalf("Tasks = %d, want exactly 1", n)
	}
}

// TestAddRunAgent_SeveralAgentsOfOneProfile: two separate requests for the SAME profile add two agents —
// distinct participants (keyed by task, not by profile), each addressable by an alias made from its title.
// Before, the second one collided on the profile-keyed participant id and failed with a bare 500.
func TestAddRunAgent_SeveralAgentsOfOneProfile(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	stubPassingHelper(o)

	add := func(opID, title string) AttachAndSendResult {
		t.Helper()
		msg := "start"
		r, err := o.AddRunAgent(ctx, AddRunAgentRequest{
			OperationID: opID, ProfileID: "qa", Title: title, Instruction: "review", FirstMessage: &msg, Actor: state.Actor{Kind: "user"},
		})
		if err != nil {
			t.Fatalf("AddRunAgent %q: %v", title, err)
		}
		return r
	}
	r1 := add("op-new1", "New One")
	r2 := add("op-new2", "new2")
	waitForRunAgentTurn(t, store, r1.TaskID, 2*time.Second)
	waitForRunAgentTurn(t, store, r2.TaskID, 2*time.Second)

	if r1.ParticipantID == r2.ParticipantID || r1.TaskID == r2.TaskID {
		t.Fatalf("two agents of one profile share an identity: %+v vs %+v", r1, r2)
	}
	rs := store.Snapshot()
	p1, p2 := rs.Assistants.Mesh.Participants[r1.ParticipantID], rs.Assistants.Mesh.Participants[r2.ParticipantID]
	if p1 == nil || p2 == nil || p1.Kind != agentcomm.KindRunAgent || p2.Kind != agentcomm.KindRunAgent {
		t.Fatalf("participants = %+v / %+v, want two run agents", p1, p2)
	}
	if p1.Alias != "new-one" || p2.Alias != "new2" {
		t.Fatalf("aliases = %q / %q, want slugs of the titles", p1.Alias, p2.Alias)
	}
	if p1.DisplayName != "New One" || p2.DisplayName != "new2" {
		t.Fatalf("display names = %q / %q", p1.DisplayName, p2.DisplayName)
	}
	// Each one is its own task's mesh identity (inbox, waits, resume).
	if got := meshParticipantForTask(rs, rs.Assistants.Tasks[r2.TaskID].Task); got != r2.ParticipantID {
		t.Fatalf("meshParticipantForTask(second) = %s, want %s", got, r2.ParticipantID)
	}
	if id, ok := o.taskIDFromMeshParticipant(r2.ParticipantID); !ok || id != r2.TaskID {
		t.Fatalf("taskIDFromMeshParticipant(%s) = %s,%v, want %s", r2.ParticipantID, id, ok, r2.TaskID)
	}
}

// A run agent without a title is addressable by its profile id.
func TestRunAgentAlias_FallsBackToProfile(t *testing.T) {
	if got := runAgentAlias("", "qa"); got != "qa" {
		t.Fatalf("runAgentAlias(\"\", qa) = %q, want qa", got)
	}
	if got := runAgentAlias("  Release QA #2 ", "qa"); got != "release-qa-2" {
		t.Fatalf("runAgentAlias = %q, want release-qa-2", got)
	}
}

// TestAddRunAgent_RejectedWhenRunFinalizing: attaching a new standing role after the run has started
// finalizing is rejected (AttachAndSend's own ApplyRunOperation call already enforces this — see
// agentcomm_attach.go), and nothing is written.
func TestAddRunAgent_RejectedWhenRunFinalizing(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	stubPassingHelper(o)
	store.SetRunFinalizing(true)

	before := store.Snapshot()
	bt, bp, bth, bm := meshSnapshotCounts(before)

	msg := "hi"
	if _, err := o.AddRunAgent(ctx, AddRunAgentRequest{
		OperationID: "op-finalizing", ProfileID: "qa", Title: "My QA", Instruction: "watch for regressions", FirstMessage: &msg, Actor: state.Actor{Kind: "user"},
	}); err == nil {
		t.Fatal("AddRunAgent after the run started finalizing: want error, got nil")
	}

	after := store.Snapshot()
	at, ap, ath, am := meshSnapshotCounts(after)
	if at != bt || ap != bp || ath != bth || am != bm {
		t.Fatalf("partial attach detected while finalizing: before=(%d,%d,%d,%d) after=(%d,%d,%d,%d)", bt, bp, bth, bm, at, ap, ath, am)
	}
}

// A journal written before run agents were keyed by task has a "run:<profileID>" participant. That old agent
// keeps its identity after a restart (inbox, waits), and adding a NEW agent of the same profile never makes
// the legacy id point at the new one.
func TestRunParticipant_LegacyProfileKeyedIdentity(t *testing.T) {
	legacy := agentcomm.ParticipantID("run:qa")
	oldTask, newTask := assistants.TaskID("t-old"), assistants.TaskID("t-new")
	rs := state.RunState{Assistants: state.AssistantsState{
		Tasks: map[assistants.TaskID]*state.TaskState{
			oldTask: {Task: assistants.Task{ID: oldTask, Scope: assistants.ScopeRun, ProfileID: "qa"}},
			newTask: {Task: assistants.Task{ID: newTask, Scope: assistants.ScopeRun, ProfileID: "qa"}},
		},
		Mesh: state.MeshState{Participants: map[agentcomm.ParticipantID]*agentcomm.Participant{
			legacy:                        {ID: legacy, Kind: agentcomm.KindRunAgent, Scope: agentcomm.ScopeRun},
			meshRunParticipantID(newTask): {ID: meshRunParticipantID(newTask), Kind: agentcomm.KindRunAgent, Scope: agentcomm.ScopeRun},
		}},
	}}
	if owner, ok := runTaskForParticipant(rs, legacy); !ok || owner != oldTask {
		t.Fatalf("legacy run:qa -> %s,%v, want the old task", owner, ok)
	}
	if got := meshParticipantForTask(rs, rs.Assistants.Tasks[oldTask].Task); got != legacy {
		t.Fatalf("old task identity = %s, want the legacy %s", got, legacy)
	}
	if got := meshParticipantForTask(rs, rs.Assistants.Tasks[newTask].Task); got != meshRunParticipantID(newTask) {
		t.Fatalf("new task identity = %s, want run:<taskID>", got)
	}
	if owner, ok := runTaskForParticipant(rs, meshRunParticipantID(newTask)); !ok || owner != newTask {
		t.Fatalf("run:<new task> -> %s,%v", owner, ok)
	}
}
