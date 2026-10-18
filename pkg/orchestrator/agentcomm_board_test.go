package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
)

// Agent Mesh Phase 2, T2.4: Publish/ReadBoard.

func TestMesh_PublishFirstVersionThenStaleRevisionRejected(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	qa := meshHelperParticipantID(newRunningHelperForMesh(t, o, store, "op-b-1"))

	entry, err := o.Publish(ctx, PublishRequest{OperationID: "op-b-1-pub1", Author: qa, Key: "qa.tests", Status: "preparing", ExpectedVersion: 0})
	if err != nil {
		t.Fatalf("Publish v1: %v", err)
	}
	if entry.Version != 1 {
		t.Fatalf("first publish version = %d, want 1", entry.Version)
	}

	if _, err := o.Publish(ctx, PublishRequest{OperationID: "op-b-1-pub-stale", Author: qa, Key: "qa.tests", Status: "ready", ExpectedVersion: 0}); err == nil {
		t.Fatal("Publish with a stale expected_version: want error, got nil")
	}

	entry2, err := o.Publish(ctx, PublishRequest{OperationID: "op-b-1-pub2", Author: qa, Key: "qa.tests", Status: "ready", ExpectedVersion: 1, ArtifactRefs: []string{"artifacts/report.json"}})
	if err != nil {
		t.Fatalf("Publish v2: %v", err)
	}
	if entry2.Version != 2 {
		t.Fatalf("second publish version = %d, want 2", entry2.Version)
	}

	page, err := o.ReadBoard(ctx, ReadBoardRequest{Key: "qa.tests"})
	if err != nil {
		t.Fatalf("ReadBoard: %v", err)
	}
	if len(page.Entries) != 1 || page.Entries[0].Version != 2 {
		t.Fatalf("ReadBoard = %+v, want one entry at version 2", page.Entries)
	}
}

// TestMesh_PublishWakesPendingBoardWait is the QA->DevOps scenario at Service-call granularity (full
// scripted-runner integration coverage is T8.1): DevOps registers a board_key wait while QA has not
// published yet; QA's publish resolves it in the SAME operation, with no UI/live-bus step involved.
func TestMesh_PublishWakesPendingBoardWait(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	qa := meshHelperParticipantID(newRunningHelperForMesh(t, o, store, "op-b-2a"))
	devops := meshHelperParticipantID(newRunningHelperForMesh(t, o, store, "op-b-2b"))

	deadline := time.Now().Add(time.Hour)
	receipt, err := o.Wait(ctx, WaitRequest{
		OperationID: "op-b-2-wait", Waiter: devops, Deadline: &deadline,
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnBoardKey, Key: "qa.tests", Status: "ready"},
	})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if receipt.Status != agentcomm.WaitPending {
		t.Fatalf("wait status before publish = %q, want pending", receipt.Status)
	}

	if _, err := o.Publish(ctx, PublishRequest{OperationID: "op-b-2-pub", Author: qa, Key: "qa.tests", Status: "ready", ExpectedVersion: 0}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	rs := store.Snapshot()
	if got := rs.Assistants.Mesh.Waits[receipt.WaitID].Status; got != agentcomm.WaitReady {
		t.Fatalf("wait status after matching publish = %q, want ready", got)
	}
}

func TestMesh_PublishRejectsUnauthorizedNonOwner(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	qa := meshHelperParticipantID(newRunningHelperForMesh(t, o, store, "op-b-3a"))
	other := meshHelperParticipantID(newRunningHelperForMesh(t, o, store, "op-b-3b"))

	if _, err := o.Publish(ctx, PublishRequest{OperationID: "op-b-3-pub1", Author: qa, Key: "qa.tests", Status: "preparing", ExpectedVersion: 0}); err != nil {
		t.Fatalf("Publish v1: %v", err)
	}
	if _, err := o.Publish(ctx, PublishRequest{OperationID: "op-b-3-pub2", Author: other, Key: "qa.tests", Status: "ready", ExpectedVersion: 1}); err == nil {
		t.Fatal("Publish by a non-owner, non-authorized participant: want error, got nil")
	}
}
