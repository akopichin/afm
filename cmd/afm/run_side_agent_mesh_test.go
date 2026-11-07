package main

import (
	implexecutor "github.com/akopichin/afm-engine/testkit/executor"
	orchestrator "github.com/akopichin/afm/contracts/control"

	"github.com/akopichin/afm/contracts/state"

	sideagent "github.com/akopichin/afm/contracts/assistant"

	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm-engine/testkit/engine"
	implsideagent "github.com/akopichin/afm-engine/testkit/sideagent"
	"github.com/akopichin/afm/contracts/agentcomm"
	"github.com/akopichin/afm/pkg/server"
)

func TestSideAgentMesh_InboxReadDoesNotDeliverQueuedTurnAndEarlyReplyClosesReceipt(t *testing.T) {
	runner := &meshSideRunner{started: make(chan struct{}, 2), release: make(chan struct{}), answer: "answer A"}
	orch, store, _, mgr := meshSideHarness(t, runner)
	ctx := context.Background()
	send := func(op string) orchestrator.SendReceipt {
		t.Helper()
		got, err := orch.SendFromAgent(ctx, orchestrator.SendRequest{
			OperationID: op, Sender: "stage:impl", Recipients: []agentcomm.ParticipantID{orch.SideAgentParticipantID()},
			Kind: agentcomm.MessageKindMessage, Body: op, Actor: state.Actor{Kind: "agent", ID: "stage:impl"},
		})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	_ = send("inbox-active")
	<-runner.started
	queued := send("inbox-queued")
	a := agentMeshActions{Mesh: orch}
	page, err := a.Inbox(ctx, string(orch.SideAgentParticipantID()), 0, 100, false)
	if err != nil || len(page.Messages) != 1 || page.Messages[0].ID != queued.MessageID {
		t.Fatalf("side inbox = %+v, %v", page, err)
	}
	if got := store.Snapshot().Assistants.Mesh.Receipts[queued.MessageID][orch.SideAgentParticipantID()]; got != agentcomm.DeliveryAccepted {
		t.Fatalf("queued receipt after inbox = %s, want accepted", got)
	}
	if _, err := a.Send(ctx, "inbox-early-reply", string(orch.SideAgentParticipantID()), server.MeshSendRequest{
		Kind: string(agentcomm.MessageKindReply), InReplyTo: string(queued.MessageID), Body: "early answer",
	}); err != nil {
		t.Fatal(err)
	}
	close(runner.release)
	waitSideMesh(t, func() bool {
		return store.Snapshot().Assistants.Mesh.Receipts[queued.MessageID][orch.SideAgentParticipantID()] == agentcomm.DeliveryAcknowledged
	})
	if sideReplyCount(store.Snapshot(), queued.MessageID) != 0 {
		t.Fatal("queued early reply gained automatic duplicate")
	}
	for _, ev := range mgr.History() {
		if ev.Type == sideagent.EventTurnAccepted {
			var d sideagent.TurnAcceptedData
			if ev.DecodeData(&d) == nil && d.SourceMessageID == string(queued.MessageID) {
				t.Fatal("queued early-replied source started a side turn")
			}
		}
	}
}

func TestSideAgentMesh_RealSubprocessCLIUsesAndRevokesDescriptor(t *testing.T) {
	if testing.Short() {
		t.Skip("builds an afm test binary")
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "afm-test")
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build afm CLI: %v\n%s", err, output)
	}
	orch, store := newSideMeshOrch(t)
	httpServer := httptest.NewServer(server.New(server.Config{AgentMesh: agentMeshActions{Mesh: orch}, Mesh: meshActions{orch}}).Handler())
	t.Cleanup(httpServer.Close)
	orch.SetAgentAPIURL(httpServer.URL)
	sideStore, err := implsideagent.Open(t.TempDir(), "side-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sideStore.Close() })
	listPath := filepath.Join(dir, "list.json")
	tokenPath := filepath.Join(dir, "token")
	descriptorPath := filepath.Join(dir, "descriptor-path")
	script := filepath.Join(dir, "side-test-client.sh")
	scriptBody := `#!/bin/sh
set -eu
printf '%s' "$AFM_MESH_DESCRIPTOR" > "$AFM_TEST_DESCRIPTOR_PATH"
sed -n 's/.*"token":"\([^"]*\)".*/\1/p' "$AFM_MESH_DESCRIPTOR" > "$AFM_TEST_TOKEN_PATH"
"$AFM_TEST_BINARY" agent list > "$AFM_TEST_LIST_PATH"
"$AFM_TEST_BINARY" agent send --to user --body "side sent from subprocess"
"$AFM_TEST_BINARY" agent reply --in-reply-to "$AFM_TEST_SOURCE" --body "side explicit subprocess reply"
echo '{"type":"assistant","message":{"content":[{"type":"text","text":"side final text"}]}}'
`
	if err := os.WriteFile(script, []byte(scriptBody), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AFM_TEST_BINARY", binary)
	t.Setenv("AFM_TEST_LIST_PATH", listPath)
	t.Setenv("AFM_TEST_TOKEN_PATH", tokenPath)
	t.Setenv("AFM_TEST_DESCRIPTOR_PATH", descriptorPath)
	mgr := newStartedSideManager(implsideagent.Config{
		Store: sideStore, Command: script, ExtraArgs: []string{"--test"},
		MeshClient: orch.SideAgentMeshClient,
		OnSourceDelivered: func(id string) {
			if err := orch.MarkMessageDelivered(context.Background(), agentcomm.MessageID(id), orch.SideAgentParticipantID()); err != nil {
				t.Errorf("mark delivered: %v", err)
			}
		},
	})
	bridge := engine.StartAssistantMesh(orch, mgr)
	t.Cleanup(func() {
		bridge.CloseInput()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = bridge.Drain(ctx)
		_ = mgr.Drain(ctx)
	})
	ctx := context.Background()
	source, err := orch.SendFromAgent(ctx, orchestrator.SendRequest{
		OperationID: "subprocess-source", Sender: "stage:impl", Recipients: []agentcomm.ParticipantID{orch.SideAgentParticipantID()},
		Kind: agentcomm.MessageKindMessage, Body: "please answer", Actor: state.Actor{Kind: "agent", ID: "stage:impl"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AFM_TEST_SOURCE", string(source.MessageID))
	waitSideMesh(t, func() bool {
		rs := store.Snapshot()
		for _, msg := range rs.Assistants.Mesh.Messages {
			if msg != nil && msg.Sender == orch.SideAgentParticipantID() && msg.InReplyTo != nil && *msg.InReplyTo == source.MessageID && !msg.AutoResult {
				return true
			}
		}
		return false
	})
	bridge.CloseInput()
	drainCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if !bridge.Drain(drainCtx) || !mgr.Drain(drainCtx) {
		t.Fatal("subprocess did not finish and reconcile")
	}
	list, err := os.ReadFile(listPath)
	if err != nil || !strings.Contains(string(list), string(orch.SideAgentParticipantID())) {
		t.Fatalf("afm agent list omitted side identity: %q, %v", list, err)
	}
	tokenBytes, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimSpace(string(tokenBytes))
	if token == "" {
		t.Fatal("subprocess did not inherit Mesh descriptor")
	}
	if _, valid := orch.ResolveMeshToken(token); valid {
		t.Fatal("subprocess token remains valid after turn")
	}
	pathBytes, err := os.ReadFile(descriptorPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(string(pathBytes)); !os.IsNotExist(err) {
		t.Fatalf("descriptor still exists after turn: %v", err)
	}
	rs := store.Snapshot()
	var sent, replied bool
	for _, msg := range rs.Assistants.Mesh.Messages {
		if msg == nil || msg.Sender != orch.SideAgentParticipantID() {
			continue
		}
		if msg.Body == "side sent from subprocess" {
			sent = true
		}
		if msg.InReplyTo != nil && *msg.InReplyTo == source.MessageID && msg.Body == "side explicit subprocess reply" && !msg.AutoResult {
			replied = true
		}
	}
	if !sent || !replied || sideReplyCount(rs, source.MessageID) != 0 {
		t.Fatalf("side attribution or explicit reply failed: sent=%t replied=%t auto=%d", sent, replied, sideReplyCount(rs, source.MessageID))
	}
}

func TestSideAgentMesh_ClosedThreadReleasesWait(t *testing.T) {
	runner := &meshSideRunner{started: make(chan struct{}, 1), release: make(chan struct{}), answer: "answer"}
	orch, store, _, _ := meshSideHarness(t, runner)
	msg, err := orch.SendFromAgent(context.Background(), orchestrator.SendRequest{OperationID: "side-closed-source", Sender: "user", Recipients: []agentcomm.ParticipantID{orch.SideAgentParticipantID()}, Kind: agentcomm.MessageKindMessage, Body: "question", OriginStage: "impl", Actor: state.Actor{Kind: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	<-runner.started
	wait, err := orch.Wait(context.Background(), orchestrator.WaitRequest{
		OperationID: "side-closed-wait", Waiter: "user",
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: msg.MessageID},
		Actor:     state.Actor{Kind: "user"},
	})
	if err != nil || wait.Status != agentcomm.WaitPending {
		t.Fatalf("pending reply wait = %+v, %v", wait, err)
	}
	if err := orch.CloseThread(context.Background(), orchestrator.CloseThreadRequest{OperationID: "side-close-thread", ThreadID: msg.ThreadID, Actor: state.Actor{Kind: "user"}}); err != nil {
		t.Fatal(err)
	}
	close(runner.release)
	waitSideMesh(t, func() bool {
		return store.Snapshot().Assistants.Mesh.Receipts[msg.MessageID][orch.SideAgentParticipantID()] == agentcomm.DeliveryUndeliverable
	})
	if n := sideReplyCount(store.Snapshot(), msg.MessageID); n != 0 {
		t.Fatalf("linked results = %d in closed thread", n)
	}
	if got := store.Snapshot().Assistants.Mesh.Waits[wait.WaitID].Status; got != agentcomm.WaitCancelled {
		t.Fatalf("reply wait after closed thread = %s, want cancelled", got)
	}
	ui := meshActions{orch}
	threadPage, err := ui.Thread(context.Background(), string(msg.ThreadID), 0, 100)
	if err != nil || !threadPage.SideAgentFailedIDs[msg.MessageID] {
		t.Fatalf("human thread read lost durable side failure: %+v, %v", threadPage, err)
	}
	stagePage, err := ui.StageMessages(context.Background(), "impl", 0, 100)
	if err != nil || !stagePage.SideAgentFailedIDs[msg.MessageID] {
		t.Fatalf("human stage read lost durable side failure: %+v, %v", stagePage, err)
	}
}

// A passive result whose side turn was cut off by a restart is closed as undeliverable, and the dashboard
// adapter must not show it as a failed side answer.
func TestSideAgentMesh_PassiveOrphanIsNotProjectedAsFailedAnswer(t *testing.T) {
	orch, store := newSideMeshOrch(t)
	orch.SetSideAgentAvailable(true)
	source, err := orch.SendFromAgent(context.Background(), orchestrator.SendRequest{
		OperationID: "side-passive-orphan", Sender: "stage:impl", Recipients: []agentcomm.ParticipantID{orch.SideAgentParticipantID()},
		Kind: agentcomm.MessageKindResult, Body: "passive result", AutoResult: true, Actor: state.Actor{Kind: "system"},
	})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	sideStore, err := implsideagent.Open(root, "side-test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sideStore.Append(sideagent.EventTurnAccepted, "passive-orphan", sideagent.TurnAcceptedData{
		Message: "passive result", ClientMessageID: "mesh:" + string(source.MessageID), SourceMessageID: string(source.MessageID),
		SourceThreadID: string(source.ThreadID), SourceSenderID: "stage:impl",
	}); err != nil {
		t.Fatal(err)
	}
	if err := sideStore.Close(); err != nil {
		t.Fatal(err)
	}
	sideStore, err = implsideagent.Open(root, "side-test") // recovers the accepted turn as interrupted
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sideStore.Close() })
	mgr := newStartedSideManager(implsideagent.Config{Store: sideStore, Command: "codex", NewExecutor: func(implexecutor.Config) implsideagent.AgentRunner {
		t.Error("passive orphan was rerun")
		return nil
	}})
	bridge := engine.StartAssistantMesh(orch, mgr)
	bridge.CloseInput()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !bridge.Drain(ctx) || !mgr.Drain(ctx) {
		t.Fatal("orphan reconciliation did not settle")
	}
	if got := store.Snapshot().Assistants.Mesh.Receipts[source.MessageID][orch.SideAgentParticipantID()]; got != agentcomm.DeliveryUndeliverable {
		t.Fatalf("passive orphan receipt = %s, want undeliverable", got)
	}
	page, err := (meshActions{orch: orch}).StageMessages(context.Background(), "impl", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 || page.Messages[0].ID != source.MessageID {
		t.Fatalf("stage Mesh messages = %+v, want passive source", page.Messages)
	}
	if page.SideAgentFailedIDs[source.MessageID] {
		t.Fatal("passive orphan was projected as a failed side answer")
	}
}
