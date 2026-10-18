// Package orchestrator_test (this file, Agent Mesh Phase 8, T8.1): "the whole flow with the HTTP server
// present but no client connected" — a real *server.Server, wired to the SAME *Orchestrator driving a QA/
// DevOps board handoff, constructed alongside the scenario but never queried (no ListenAndServe, no request
// against its Handler). This MUST be the external orchestrator_test package, not orchestrator (internal,
// white-box, agentcomm_integration_test.go's own package): pkg/server imports pkg/orchestrator itself
// (action_drafts.go's own adapter types), so an INTERNAL test file importing pkg/server would be a genuine
// import cycle — only the external test package can sit on both sides of that edge. Every id/participant this
// file touches is therefore reached through orchestrator's own EXPORTED surface (AddRunAgentRequest's own
// result, orchestrator.New/SetRunHelperAgentForTest) rather than any unexported helper, mirroring
// run_barrier_test.go's own harness shape for this package.
package orchestrator_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/server"
	"github.com/akopichin/afm/pkg/state"
)

// meshServerActionsForTest adapts *orchestrator.Orchestrator to server.MeshActions — mirrors cmd/afm/
// run_mesh.go's production adapter (not importable here: it lives in package main) trimmed to exactly what
// this test needs to type-check a real *server.Server, but every method still delegates to the SAME real
// Service methods a live dashboard would use — not a stub, even though this test never actually calls any of
// them.
type meshServerActionsForTest struct{ orch *orchestrator.Orchestrator }

func (a meshServerActionsForTest) ListParticipants(ctx context.Context) ([]agentcomm.Participant, error) {
	page, err := a.orch.ListParticipants(ctx, orchestrator.ListParticipantsRequest{Actor: state.Actor{Kind: "user"}})
	if err != nil {
		return nil, err
	}
	return page.Participants, nil
}

func (a meshServerActionsForTest) Threads(ctx context.Context) ([]agentcomm.Thread, error) {
	page, err := a.orch.ListThreads(ctx, orchestrator.ListThreadsRequest{Participant: "user", Actor: state.Actor{Kind: "user"}})
	if err != nil {
		return nil, err
	}
	return page.Threads, nil
}

func (a meshServerActionsForTest) OpenThread(ctx context.Context, requestID string, req server.MeshOpenThreadRequest) (agentcomm.Thread, error) {
	members := make([]agentcomm.ParticipantID, len(req.Members))
	for i, m := range req.Members {
		members[i] = agentcomm.ParticipantID(m)
	}
	return a.orch.OpenThread(ctx, orchestrator.OpenThreadRequest{
		OperationID: requestID, Title: req.Title, Scope: agentcomm.Scope(req.Scope),
		Members: members, MaxMessages: req.MaxMessages, Actor: state.Actor{Kind: "user"},
	})
}

func (a meshServerActionsForTest) CloseThread(ctx context.Context, requestID, threadID string) error {
	return a.orch.CloseThread(ctx, orchestrator.CloseThreadRequest{OperationID: requestID, ThreadID: agentcomm.ThreadID(threadID), Actor: state.Actor{Kind: "user"}})
}

func (a meshServerActionsForTest) Send(ctx context.Context, requestID, threadID string, req server.MeshSendRequest) (server.MeshSendReceipt, error) {
	recipients := make([]agentcomm.ParticipantID, len(req.Recipients))
	for i, r := range req.Recipients {
		recipients[i] = agentcomm.ParticipantID(r)
	}
	var inReplyTo *agentcomm.MessageID
	if req.InReplyTo != "" {
		id := agentcomm.MessageID(req.InReplyTo)
		inReplyTo = &id
	}
	kind := agentcomm.MessageKind(req.Kind)
	if kind == "" {
		kind = agentcomm.MessageKindMessage
	}
	receipt, err := a.orch.Send(ctx, orchestrator.SendRequest{
		OperationID: requestID, ThreadID: agentcomm.ThreadID(threadID), Sender: "user",
		Recipients: recipients, Kind: kind, InReplyTo: inReplyTo, Body: req.Body, ArtifactRefs: req.ArtifactRefs,
		Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		return server.MeshSendReceipt{}, err
	}
	return server.MeshSendReceipt{MessageID: string(receipt.MessageID), Seq: receipt.Seq}, nil
}

func (a meshServerActionsForTest) Thread(ctx context.Context, threadID string, cursor uint64, limit int) (server.MeshMessagePage, error) {
	page, err := a.orch.ReadThread(ctx, orchestrator.ReadThreadRequest{ThreadID: agentcomm.ThreadID(threadID), Reader: "user", Cursor: cursor, Limit: limit, Actor: state.Actor{Kind: "user"}})
	if err != nil {
		return server.MeshMessagePage{}, err
	}
	return server.MeshMessagePage{Messages: page.Messages, NextCursor: page.NextCursor}, nil
}

func (a meshServerActionsForTest) Inbox(ctx context.Context, cursor uint64, limit int) (server.MeshMessagePage, error) {
	page, err := a.orch.ReadInbox(ctx, orchestrator.ReadInboxRequest{Participant: "user", Cursor: cursor, Limit: limit, Actor: state.Actor{Kind: "user"}})
	if err != nil {
		return server.MeshMessagePage{}, err
	}
	return server.MeshMessagePage{Messages: page.Messages, NextCursor: page.NextCursor}, nil
}

func (a meshServerActionsForTest) Board(ctx context.Context, key string) ([]agentcomm.BoardEntry, error) {
	page, err := a.orch.ReadBoard(ctx, orchestrator.ReadBoardRequest{Key: key})
	if err != nil {
		return nil, err
	}
	return page.Entries, nil
}

func (a meshServerActionsForTest) AttachAndSend(ctx context.Context, requestID string, req server.MeshAttachAndSendRequest) (server.MeshAttachAndSendResult, error) {
	recipients := make([]agentcomm.ParticipantID, len(req.Recipients))
	for i, r := range req.Recipients {
		recipients[i] = agentcomm.ParticipantID(r)
	}
	result, err := a.orch.AttachAndSend(ctx, orchestrator.AttachAndSendRequest{
		OperationID: requestID, ProfileID: assistants.ProfileID(req.ProfileID), Scope: assistants.Scope(req.Scope),
		StageID: req.StageID, Mode: assistants.Mode(req.Mode), Title: req.Title, Instruction: req.Instruction,
		FirstMessage: req.FirstMessage, Recipients: recipients, Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		return server.MeshAttachAndSendResult{}, err
	}
	var msgID *string
	if result.MessageID != nil {
		id := string(*result.MessageID)
		msgID = &id
	}
	return server.MeshAttachAndSendResult{
		TaskID: string(result.TaskID), ParticipantID: string(result.ParticipantID),
		ThreadID: string(result.ThreadID), MessageID: msgID,
	}, nil
}

var _ server.MeshActions = meshServerActionsForTest{}

// TestAgentMesh_QADevOps_WithHTTPServerPresent drives a reduced QA/DevOps board handoff (QA publishes
// board:qa.tests=ready, DevOps's board wait wakes and uses it — the full thread-agreement/counter-question
// drama is already covered by TestAgentMesh_QADevOps_EndToEnd in the internal package; this test's own job is
// only the server-presence claim) with a real *server.Server constructed alongside, never queried.
func TestAgentMesh_QADevOps_WithHTTPServerPresent(t *testing.T) {
	runDir := t.TempDir()
	rootDir := t.TempDir()
	const stageID = "impl"
	stages := []flow.Stage{{ID: stageID, Name: "Work", Script: "sleep 0.01"}}

	store, err := state.Open(runDir, []string{stageID})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if _, err := store.EnsurePolicyRecorded(assistants.DepthPolicy{MaxDepth: 3, Source: assistants.DepthSourceDefault, Revision: "rev-1"}, state.Actor{Kind: "system"}); err != nil {
		t.Fatalf("EnsurePolicyRecorded: %v", err)
	}
	reg, err := assistants.BuildRegistry(nil, []assistants.Profile{
		{ID: "qa", Title: "QA", Command: "helper-cmd"},
		{ID: "devops", Title: "DevOps", Command: "helper-cmd"},
	}, nil, "claude", func(cmd string) (bool, string, assistants.ReadonlyEnforcement) {
		return true, "", assistants.ReadonlyEnforcementOS
	})
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}

	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir, RootDir: rootDir, Stages: stages, Store: store,
		Config: config.Default(), Prompts: orchestrator.DefaultPrompts(),
		AssistantsEnabled: true, AssistantsRegistry: reg,
	})

	srv := server.New(server.Config{RunDir: runDir, Store: store, UIBus: bus.NewUIBus(), Mesh: meshServerActionsForTest{orch: orch}})
	if srv == nil {
		t.Fatal("server.New returned nil")
	}
	if srv.Handler() == nil {
		t.Fatal("server.Handler() returned nil")
	}
	// Deliberately never queried from here on — see this test's own doc comment.

	ctx := context.Background()
	// "run:<profileID>" is AddRunAgent's own documented participant-id vocabulary (dev-plan.md's Vocabulary
	// table, agentcomm_attach.go's own meshRunParticipantID doc comment) — stable and public, not derived from
	// any unexported symbol this external test package could not otherwise reach.
	qaParticipant := agentcomm.ParticipantID("run:qa")
	devopsParticipant := agentcomm.ParticipantID("run:devops")

	var qaCalls, devopsCalls atomic.Int32
	orchestrator.SetRunHelperAgentForTest(orch, func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		switch assistants.ProfileID(profileID) {
		case "qa":
			qaCalls.Add(1)
			if _, err := orch.Publish(context.Background(), orchestrator.PublishRequest{
				OperationID: "op-srv-publish", Author: qaParticipant, Key: "qa.tests", Status: "ready",
				ArtifactRefs: []string{"artifacts/qa-report.json"},
			}); err != nil {
				t.Errorf("Publish: %v", err)
			}
			return passResultOutcome(), nil
		case "devops":
			switch devopsCalls.Add(1) {
			case 1:
				deadline := time.Now().Add(time.Hour)
				if _, err := orch.Wait(context.Background(), orchestrator.WaitRequest{
					OperationID: "op-srv-wait-board", Waiter: devopsParticipant, Deadline: &deadline,
					Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnBoardKey, Key: "qa.tests", Status: "ready"},
				}); err != nil {
					t.Errorf("Wait: %v", err)
				}
				return executor.HelperOutcome{ProcessOK: true}, nil
			default:
				return passResultOutcome(), nil
			}
		default:
			return passResultOutcome(), nil
		}
	})

	// DevOps is attached FIRST so its board wait is genuinely PENDING (not already-satisfied) when it
	// registers — QA publishing afterwards is what actually exercises "DevOps's board wait wakes" live,
	// instead of Wait() itself immediately returning Ready for an already-published key.
	doMsg := "verify once ready"
	doRes, err := orch.AddRunAgent(ctx, orchestrator.AddRunAgentRequest{ProfileID: "devops", Title: "DevOps", FirstMessage: &doMsg, Actor: state.Actor{Kind: "user"}})
	if err != nil {
		t.Fatalf("AddRunAgent (devops): %v", err)
	}
	waitForTaskStatus(t, store, doRes.TaskID, assistants.StatusWaitingPeer, 5*time.Second)

	qaMsg := "publish your result"
	qaRes, err := orch.AddRunAgent(ctx, orchestrator.AddRunAgentRequest{ProfileID: "qa", Title: "QA", FirstMessage: &qaMsg, Actor: state.Actor{Kind: "user"}})
	if err != nil {
		t.Fatalf("AddRunAgent (qa): %v", err)
	}
	waitForTaskStatus(t, store, qaRes.TaskID, assistants.StatusCompleted, 5*time.Second)
	waitForTaskStatus(t, store, doRes.TaskID, assistants.StatusCompleted, 5*time.Second)

	if got := qaCalls.Load(); got != 1 {
		t.Fatalf("qa invoked %d times, want exactly 1", got)
	}
	if got := devopsCalls.Load(); got != 2 {
		t.Fatalf("devops invoked %d times, want exactly 2 (wait, then use the result)", got)
	}

	rs := store.Snapshot()
	if entry, ok := rs.Assistants.Mesh.Board["qa.tests"]; !ok || entry.Status != "ready" {
		t.Fatalf("board entry = %+v, want ready", entry)
	}
}
