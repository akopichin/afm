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

// TestAddRunAgent_IdempotentPerProfile: two "Add run agent" submissions for the SAME profile — e.g. a
// double-click, or two browser tabs each minting their own request id — must resolve to the SAME participant,
// task and thread, never a second standing role for that profile. AddRunAgentRequest carries no OperationID
// of its own (see addRunAgentOperationID's doc comment); this test is what proves that design choice actually
// delivers "idempotent per profile", not just "idempotent per operation id".
func TestAddRunAgent_IdempotentPerProfile(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	stubPassingHelper(o)

	msg := "hello QA"
	req := AddRunAgentRequest{
		ProfileID: "qa", Title: "My QA", Instruction: "watch for regressions", FirstMessage: &msg, Actor: state.Actor{Kind: "user"},
	}

	r1, err := o.AddRunAgent(ctx, req)
	if err != nil {
		t.Fatalf("AddRunAgent (first): %v", err)
	}
	r2, err := o.AddRunAgent(ctx, req)
	if err != nil {
		t.Fatalf("AddRunAgent (repeat submit): %v", err)
	}
	if r1.TaskID != r2.TaskID || r1.ParticipantID != r2.ParticipantID || r1.ThreadID != r2.ThreadID {
		t.Fatalf("repeat submit for the same profile created a different result: first=%+v repeat=%+v", r1, r2)
	}
	waitForTaskStatus(t, store, r1.TaskID, assistants.StatusCompleted, 2*time.Second)

	rs := store.Snapshot()
	if len(rs.Assistants.Tasks) != 1 {
		t.Fatalf("Tasks = %d, want exactly 1 (no duplicate task from the repeat submit)", len(rs.Assistants.Tasks))
	}
	runAgentCount := 0
	for _, p := range rs.Assistants.Mesh.Participants {
		// Kind, not Scope: the human "user" participant is ALSO durably registered with Scope==ScopeRun (a
		// standing role of its own, agentcomm_roster.go) — KindRunAgent is what actually identifies a
		// scope=run STANDING ROLE created by AttachAndSend/AddRunAgent.
		if p.Kind == agentcomm.KindRunAgent {
			runAgentCount++
		}
	}
	if runAgentCount != 1 {
		t.Fatalf("want exactly one run_agent participant, got %d", runAgentCount)
	}

	// Addressable afterwards, independent of any one stage — resolveParticipant (agentcomm_roster.go) is what
	// ListThread/OpenThread from a DIFFERENT stage would use to find it.
	p, ok := rs.Assistants.Mesh.Participants[r1.ParticipantID]
	if !ok {
		t.Fatal("run participant not found after the repeat submit")
	}
	if !p.Reachable {
		t.Fatal("standing run-scoped role must remain reachable")
	}
	if p.Alias != "qa" {
		t.Fatalf("participant alias = %q, want the profile id %q", p.Alias, "qa")
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
		ProfileID: "qa", Title: "My QA", Instruction: "watch for regressions", FirstMessage: &msg, Actor: state.Actor{Kind: "user"},
	}); err == nil {
		t.Fatal("AddRunAgent after the run started finalizing: want error, got nil")
	}

	after := store.Snapshot()
	at, ap, ath, am := meshSnapshotCounts(after)
	if at != bt || ap != bp || ath != bth || am != bm {
		t.Fatalf("partial attach detected while finalizing: before=(%d,%d,%d,%d) after=(%d,%d,%d,%d)", bt, bp, bth, bm, at, ap, ath, am)
	}
}
