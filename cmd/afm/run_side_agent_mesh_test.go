package main

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/server"
	"github.com/akopichin/afm/pkg/sideagent"
	"github.com/akopichin/afm/pkg/state"
)

type meshSideRunner struct {
	cfg           executor.Config
	started       chan struct{}
	release       chan struct{}
	answer        string
	interruptible bool
}

type failingJournalRunner struct {
	cfg      executor.Config
	started  chan string
	writeNow chan struct{}
}

func (r *failingJournalRunner) RunAgent(ctx context.Context, _, _, _, _ string) error {
	_, token, revoke := r.cfg.MeshClient.Mint()
	defer revoke()
	r.cfg.OnDelivered()
	r.started <- token
	select {
	case <-r.writeNow:
	case <-ctx.Done():
		return ctx.Err()
	}
	r.cfg.OnAction("text", "attempted answer")
	select {
	case <-r.cfg.InterruptCh:
		return executor.ErrUserInterrupted
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *meshSideRunner) RunAgent(ctx context.Context, _, _, _, _ string) error {
	if r.cfg.OnDelivered != nil {
		r.cfg.OnDelivered()
	}
	r.started <- struct{}{}
	if r.release != nil {
		select {
		case <-r.release:
		case <-r.cfg.InterruptCh:
			return executor.ErrUserInterrupted
		case <-ctx.Done():
			return ctx.Err()
		}
	} else if r.interruptible {
		select {
		case <-r.cfg.InterruptCh:
			return executor.ErrUserInterrupted
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if r.answer != "" {
		r.cfg.OnAction("text", r.answer)
	}
	return nil
}

func meshSideHarness(t *testing.T, runner *meshSideRunner, builders ...sideagent.ContextBuilder) (*orchestrator.Orchestrator, *state.Store, *sideAgentMeshBridge) {
	t.Helper()
	orch, meshStore := newSideMeshOrch(t)
	sideStore, err := sideagent.Open(t.TempDir(), "side-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sideStore.Close() })
	var builder sideagent.ContextBuilder
	if len(builders) > 0 {
		builder = builders[0]
	}
	mgr := sideagent.New(sideagent.Config{
		Store:       sideStore,
		Command:     "codex",
		Context:     builder,
		SourceReply: orch.SideAgentExplicitReply,
		OnSourceDelivered: func(id string) {
			if err := orch.MarkMessageDelivered(context.Background(), agentcomm.MessageID(id), orch.SideAgentParticipantID()); err != nil {
				t.Errorf("deliver: %v", err)
			}
		},
		NewExecutor: func(cfg executor.Config) sideagent.AgentRunner {
			return &meshSideRunner{cfg: cfg, started: runner.started, release: runner.release, answer: runner.answer, interruptible: runner.interruptible}
		},
	})
	b := startSideAgentMesh(orch, mgr)
	t.Cleanup(func() {
		if runner.release != nil {
			select {
			case <-runner.release:
			default:
				close(runner.release)
			}
		}
		b.CloseInput()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if !b.Drain(ctx) {
			t.Error("bridge did not drain")
		}
		if !mgr.Drain(ctx) {
			t.Error("manager did not drain")
		}
	})
	return orch, meshStore, b
}

func newSideMeshOrch(t *testing.T) (*orchestrator.Orchestrator, *state.Store) {
	t.Helper()
	return newSideMeshOrchAt(t, t.TempDir())
}

func newSideMeshOrchAt(t *testing.T, runDir string) (*orchestrator.Orchestrator, *state.Store) {
	t.Helper()
	meshStore, err := state.Open(runDir, []string{"impl"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = meshStore.Close() })
	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir, RunID: "side-test", Store: meshStore, Config: config.Default(), Prompts: orchestrator.DefaultPrompts(),
		Stages: []flow.Stage{{ID: "impl", Name: "impl", Agents: []flow.AgentType{flow.AgentAuto}}},
	})
	if _, err := orch.RegisterSideAgent(context.Background(), orchestrator.RegisterSideAgentRequest{DisplayName: "codex", Actor: state.Actor{Kind: "system"}}); err != nil {
		t.Fatal(err)
	}
	return orch, meshStore
}

func TestSideAgentMesh_FreshCapabilityOffDoesNotStartBridge(t *testing.T) {
	runDir := t.TempDir()
	store, err := state.Open(runDir, []string{"impl"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir, RunID: "side-test", Store: store, Config: config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
		Stages:  []flow.Stage{{ID: "impl", Name: "impl", Agents: []flow.AgentType{flow.AgentAuto}}},
	})
	bridge, err := startHistoricalSideAgentMesh(context.Background(), orch)
	if err != nil || bridge != nil {
		t.Fatalf("fresh capability-off bridge = %v, %v; want none", bridge, err)
	}
}

func waitSideMesh(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("side Mesh state did not converge")
}

func TestSideAgentMesh_QueuedMessagesAndLinkedReplies(t *testing.T) {
	runner := &meshSideRunner{started: make(chan struct{}, 3), release: make(chan struct{}), answer: "side answer"}
	orch, store, bridge := meshSideHarness(t, runner)
	ctx := context.Background()
	if _, err := bridge.mgr.Send("modal-first", "direct modal turn", sideagent.TurnTarget{}); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	first, err := orch.SendFromAgent(ctx, orchestrator.SendRequest{OperationID: "side-ui-1", Sender: "user", Recipients: []agentcomm.ParticipantID{orch.SideAgentParticipantID()}, Kind: agentcomm.MessageKindMessage, Body: "from UI", Actor: state.Actor{Kind: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := orch.SendFromAgent(ctx, orchestrator.SendRequest{OperationID: "side-agent-2", Sender: "stage:impl", Recipients: []agentcomm.ParticipantID{orch.SideAgentParticipantID()}, Kind: agentcomm.MessageKindMessage, Body: "from agent", Actor: state.Actor{Kind: "agent", ID: "stage:impl"}})
	if err != nil {
		t.Fatal(err)
	}
	queued := store.Snapshot()
	for _, id := range []agentcomm.MessageID{first.MessageID, second.MessageID} {
		if got := queued.Assistants.Mesh.Receipts[id][orch.SideAgentParticipantID()]; got != agentcomm.DeliveryAccepted {
			t.Fatalf("queued message %s receipt = %s", id, got)
		}
	}
	select {
	case <-runner.started:
		t.Fatal("busy manager started second turn early")
	case <-time.After(80 * time.Millisecond):
	}
	close(runner.release)
	waitSideMesh(t, func() bool {
		rs := store.Snapshot()
		return sideReplyCount(rs, first.MessageID) == 1 && sideReplyCount(rs, second.MessageID) == 1
	})
	rs := store.Snapshot()
	for _, id := range []agentcomm.MessageID{first.MessageID, second.MessageID} {
		if rs.Assistants.Mesh.Receipts[id][orch.SideAgentParticipantID()] != agentcomm.DeliveryDelivered {
			t.Fatalf("%s was not delivered at runner start", id)
		}
	}
}

func sideReplyCount(rs state.RunState, source agentcomm.MessageID) int {
	n := 0
	for _, m := range rs.Assistants.Mesh.Messages {
		if m.InReplyTo != nil && *m.InReplyTo == source && m.AutoResult {
			n++
		}
	}
	return n
}

func TestSideAgentMesh_ExplicitReplySuppressesAutoResult(t *testing.T) {
	runner := &meshSideRunner{started: make(chan struct{}, 1), release: make(chan struct{}), answer: "automatic"}
	orch, store, bridge := meshSideHarness(t, runner)
	msg, err := orch.SendFromAgent(context.Background(), orchestrator.SendRequest{OperationID: "side-explicit-source", Sender: "user", Recipients: []agentcomm.ParticipantID{orch.SideAgentParticipantID()}, Kind: agentcomm.MessageKindMessage, Body: "question", Actor: state.Actor{Kind: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	<-runner.started
	_, err = orch.SendFromAgent(context.Background(), orchestrator.SendRequest{OperationID: "side-explicit-answer", Sender: orch.SideAgentParticipantID(), Kind: agentcomm.MessageKindReply, InReplyTo: &msg.MessageID, Body: "explicit", Actor: state.Actor{Kind: "agent", ID: string(orch.SideAgentParticipantID())}})
	if err != nil {
		t.Fatal(err)
	}
	close(runner.release)
	bridge.CloseInput()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !bridge.Drain(ctx) || !bridge.mgr.Drain(ctx) {
		t.Fatal("explicit reply bridge did not settle")
	}
	if n := sideReplyCount(store.Snapshot(), msg.MessageID); n != 0 {
		t.Fatalf("automatic replies = %d", n)
	}
}

func TestSideAgentMesh_ExplicitReplyWithoutFinalTextAppearsInModal(t *testing.T) {
	runner := &meshSideRunner{started: make(chan struct{}, 1), release: make(chan struct{})}
	orch, store, bridge := meshSideHarness(t, runner)
	msg, err := orch.SendFromAgent(context.Background(), orchestrator.SendRequest{
		OperationID: "side-no-final-source", Sender: "user",
		Recipients: []agentcomm.ParticipantID{orch.SideAgentParticipantID()},
		Kind:       agentcomm.MessageKindMessage, Body: "question", Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	<-runner.started
	_, err = orch.SendFromAgent(context.Background(), orchestrator.SendRequest{
		OperationID: "side-no-final-reply", Sender: orch.SideAgentParticipantID(),
		Kind: agentcomm.MessageKindReply, InReplyTo: &msg.MessageID,
		Body: "explicit answer", Actor: state.Actor{Kind: "agent", ID: string(orch.SideAgentParticipantID())},
	})
	if err != nil {
		t.Fatal(err)
	}
	close(runner.release)
	bridge.CloseInput()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !bridge.Drain(ctx) || !bridge.mgr.Drain(ctx) {
		t.Fatal("explicit reply bridge did not settle")
	}
	events := bridge.mgr.History()
	if got := events[len(events)-1].Type; got != sideagent.EventTurnCompleted {
		t.Fatalf("modal terminal event = %s, want completed", got)
	}
	var answer sideagent.AssistantTextData
	if err := events[len(events)-2].DecodeData(&answer); err != nil || answer.Text != "explicit answer" {
		t.Fatalf("modal answer = %q, %v", answer.Text, err)
	}
	if n := sideReplyCount(store.Snapshot(), msg.MessageID); n != 0 {
		t.Fatalf("automatic replies = %d", n)
	}
}

func TestSideAgentMesh_EmergencyReconcilesActiveAndQueuedBeforeReturn(t *testing.T) {
	runner := &meshSideRunner{started: make(chan struct{}, 1), interruptible: true}
	orch, store, bridge := meshSideHarness(t, runner)
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
	active := send("emergency-active")
	<-runner.started
	queued := send("emergency-queued")
	for _, id := range []string{"side-mesh-reply:" + string(active.MessageID), "side-mesh-unprocessed:" + string(queued.MessageID)} {
		if _, err := orch.SendFromAgent(ctx, orchestrator.SendRequest{
			OperationID: id, Sender: "stage:impl", Recipients: []agentcomm.ParticipantID{orch.SideAgentParticipantID()},
			Kind: agentcomm.MessageKindMessage, Body: "preclaim", Actor: state.Actor{Kind: "agent", ID: "stage:impl"},
		}); err == nil {
			t.Fatalf("client preclaimed internal operation %q", id)
		}
	}
	waits := make([]orchestrator.WaitReceipt, 0, 2)
	for _, source := range []orchestrator.SendReceipt{active, queued} {
		w, err := orch.Wait(ctx, orchestrator.WaitRequest{
			OperationID: "wait-" + string(source.MessageID), Waiter: "stage:impl",
			Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: source.MessageID},
			Actor:     state.Actor{Kind: "agent", ID: "stage:impl"},
		})
		if err != nil || w.Status != agentcomm.WaitPending {
			t.Fatalf("pending wait = %+v, %v", w, err)
		}
		waits = append(waits, w)
	}
	if got := store.Snapshot().Assistants.Mesh.Receipts[queued.MessageID][orch.SideAgentParticipantID()]; got != agentcomm.DeliveryAccepted {
		t.Fatalf("queued receipt before shutdown = %s", got)
	}
	runtime := &sideAgentRuntime{manager: bridge.mgr, mesh: bridge}
	if !runtime.shutdownEmergency(5 * time.Second) {
		t.Fatal("emergency shutdown did not join and reconcile")
	}
	rs := store.Snapshot()
	for i, source := range []orchestrator.SendReceipt{active, queued} {
		if n := sideReplyCount(rs, source.MessageID); n != 1 {
			t.Fatalf("%s: terminal replies before restart = %d", source.MessageID, n)
		}
		if got := rs.Assistants.Mesh.Receipts[source.MessageID][orch.SideAgentParticipantID()]; got == agentcomm.DeliveryAccepted {
			t.Fatalf("%s: receipt still accepted", source.MessageID)
		}
		if got := rs.Assistants.Mesh.Waits[waits[i].WaitID].Status; got != agentcomm.WaitReady {
			t.Fatalf("%s: reply wait = %s, want ready", source.MessageID, got)
		}
	}
}

func TestSideAgentMesh_InboxReadDoesNotDeliverQueuedTurnAndEarlyReplyClosesReceipt(t *testing.T) {
	runner := &meshSideRunner{started: make(chan struct{}, 2), release: make(chan struct{}), answer: "answer A"}
	orch, store, bridge := meshSideHarness(t, runner)
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
	a := agentMeshActions{orch}
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
	for _, ev := range bridge.mgr.History() {
		if ev.Type == sideagent.EventTurnAccepted {
			var d sideagent.TurnAcceptedData
			if ev.DecodeData(&d) == nil && d.SourceMessageID == string(queued.MessageID) {
				t.Fatal("queued early-replied source started a side turn")
			}
		}
	}
}

func TestSideAgentMesh_WriteFailureJoinsAndRevokesBeforeFailureReply(t *testing.T) {
	orch, store := newSideMeshOrch(t)
	orch.SetDashboardURL("http://127.0.0.1:1")
	sideStore, err := sideagent.Open(t.TempDir(), "side-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sideStore.Close() })
	runner := &failingJournalRunner{started: make(chan string, 1), writeNow: make(chan struct{})}
	mgr := sideagent.New(sideagent.Config{
		Store: sideStore, Command: "codex", MeshClient: orch.SideAgentMeshClient,
		OnSourceDelivered: func(id string) {
			if err := orch.MarkMessageDelivered(context.Background(), agentcomm.MessageID(id), orch.SideAgentParticipantID()); err != nil {
				t.Errorf("mark delivered: %v", err)
			}
		},
		NewExecutor: func(cfg executor.Config) sideagent.AgentRunner {
			runner.cfg = cfg
			return runner
		},
	})
	bridge := startSideAgentMesh(orch, mgr)
	t.Cleanup(func() {
		bridge.CloseInput()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = bridge.Drain(ctx)
		_ = mgr.Drain(ctx)
	})
	ctx := context.Background()
	source, err := orch.SendFromAgent(ctx, orchestrator.SendRequest{
		OperationID: "failure-source", Sender: "stage:impl", Recipients: []agentcomm.ParticipantID{orch.SideAgentParticipantID()},
		Kind: agentcomm.MessageKindMessage, Body: "question", Actor: state.Actor{Kind: "agent", ID: "stage:impl"},
	})
	if err != nil {
		t.Fatal(err)
	}
	token := <-runner.started
	if token == "" {
		t.Fatal("runner did not mint Mesh token")
	}
	wait, err := orch.Wait(ctx, orchestrator.WaitRequest{
		OperationID: "failure-wait", Waiter: "stage:impl",
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: source.MessageID},
		Actor:     state.Actor{Kind: "agent", ID: "stage:impl"},
	})
	if err != nil || wait.Status != agentcomm.WaitPending {
		t.Fatalf("pending wait = %+v, %v", wait, err)
	}
	// Closing the lower journal after turn start makes the next action append
	// fail deterministically, without racing the Store's own mutex.
	if err := sideStore.Close(); err != nil {
		t.Fatal(err)
	}
	close(runner.writeNow)
	waitSideMesh(t, func() bool { return sideReplyCount(store.Snapshot(), source.MessageID) == 1 })
	if _, valid := orch.ResolveMeshToken(token); valid {
		t.Fatal("failure was published while side invocation token was still valid")
	}
	rs := store.Snapshot()
	if got := rs.Assistants.Mesh.Waits[wait.WaitID].Status; got != agentcomm.WaitReady {
		t.Fatalf("reply wait = %s, want ready", got)
	}
	if got := rs.Assistants.Mesh.Receipts[source.MessageID][orch.SideAgentParticipantID()]; got == agentcomm.DeliveryAccepted {
		t.Fatalf("source receipt = %s", got)
	}
	bridge.CloseInput()
	drainCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if !bridge.Drain(drainCtx) {
		t.Fatal("failed bridge did not drain")
	}
	replay := startSideAgentMesh(orch, nil)
	replay.CloseInput()
	if !replay.Drain(drainCtx) {
		t.Fatal("failure replay did not drain")
	}
	if n := sideReplyCount(store.Snapshot(), source.MessageID); n != 1 {
		t.Fatalf("failure replies after replay = %d", n)
	}
}

func TestSideAgentMesh_HistoricalModalIDClaimCannotStrandSource(t *testing.T) {
	orch, store := newSideMeshOrch(t)
	orch.SetSideAgentAvailable(true)
	ctx := context.Background()
	source, err := orch.SendFromAgent(ctx, orchestrator.SendRequest{
		OperationID: "claimed-source", Sender: "stage:impl", Recipients: []agentcomm.ParticipantID{orch.SideAgentParticipantID()},
		Kind: agentcomm.MessageKindMessage, Body: "same body", Actor: state.Actor{Kind: "agent", ID: "stage:impl"},
	})
	if err != nil {
		t.Fatal(err)
	}
	wait, err := orch.Wait(ctx, orchestrator.WaitRequest{
		OperationID: "claimed-source-wait", Waiter: "stage:impl",
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: source.MessageID},
		Actor:     state.Actor{Kind: "agent", ID: "stage:impl"},
	})
	if err != nil || wait.Status != agentcomm.WaitPending {
		t.Fatalf("wait = %+v, %v", wait, err)
	}
	sideStore, err := sideagent.Open(t.TempDir(), "side-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sideStore.Close() })
	mgr := sideagent.New(sideagent.Config{
		Store: sideStore, Command: "codex",
		NewExecutor: func(cfg executor.Config) sideagent.AgentRunner {
			return &meshSideRunner{cfg: cfg, started: make(chan struct{}, 1), answer: "modal answer"}
		},
	})
	if _, err := mgr.Send("mesh:"+string(source.MessageID), "same body", sideagent.TurnTarget{}); err != nil {
		t.Fatal(err)
	}
	drainCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if !mgr.Drain(drainCtx) {
		t.Fatal("modal turn did not finish")
	}
	bridge := startSideAgentMesh(orch, mgr)
	t.Cleanup(func() {
		bridge.CloseInput()
		_ = bridge.Drain(drainCtx)
	})
	waitSideMesh(t, func() bool {
		return sideReplyCount(store.Snapshot(), source.MessageID) == 1
	})
	rs := store.Snapshot()
	if got := rs.Assistants.Mesh.Waits[wait.WaitID].Status; got != agentcomm.WaitReady {
		t.Fatalf("source wait = %s", got)
	}
	if got := rs.Assistants.Mesh.Receipts[source.MessageID][orch.SideAgentParticipantID()]; got != agentcomm.DeliveryUndeliverable {
		t.Fatalf("unstarted source receipt = %s", got)
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
	httpServer := httptest.NewServer(server.New(server.Config{AgentMesh: agentMeshActions{orch}, Mesh: meshActions{orch}}).Handler())
	t.Cleanup(httpServer.Close)
	orch.SetDashboardURL(httpServer.URL)
	sideStore, err := sideagent.Open(t.TempDir(), "side-test")
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
	mgr := sideagent.New(sideagent.Config{
		Store: sideStore, Command: script, ExtraArgs: []string{"--test"},
		MeshClient: orch.SideAgentMeshClient,
		OnSourceDelivered: func(id string) {
			if err := orch.MarkMessageDelivered(context.Background(), agentcomm.MessageID(id), orch.SideAgentParticipantID()); err != nil {
				t.Errorf("mark delivered: %v", err)
			}
		},
	})
	bridge := startSideAgentMesh(orch, mgr)
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
	orch, store, _ := meshSideHarness(t, runner)
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

func TestSideAgentMesh_FullThreadCancelsReplyWait(t *testing.T) {
	runner := &meshSideRunner{started: make(chan struct{}, 2), release: make(chan struct{}), answer: "answer"}
	orch, store, _ := meshSideHarness(t, runner)
	ctx := context.Background()
	thread, err := orch.OpenThread(ctx, orchestrator.OpenThreadRequest{OperationID: "side-full-thread", Scope: agentcomm.ScopeRun, Members: []agentcomm.ParticipantID{"stage:impl", orch.SideAgentParticipantID()}, MaxMessages: 2, Actor: state.Actor{Kind: "system"}})
	if err != nil {
		t.Fatal(err)
	}
	source, err := orch.Send(ctx, orchestrator.SendRequest{OperationID: "side-full-source", ThreadID: thread.ID, Sender: "stage:impl", Recipients: []agentcomm.ParticipantID{orch.SideAgentParticipantID()}, Kind: agentcomm.MessageKindMessage, Body: "question", Actor: state.Actor{Kind: "agent", ID: "stage:impl"}})
	if err != nil {
		t.Fatal(err)
	}
	<-runner.started
	wait, err := orch.Wait(ctx, orchestrator.WaitRequest{OperationID: "side-full-wait", Waiter: "stage:impl", Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: source.MessageID}, Actor: state.Actor{Kind: "agent", ID: "stage:impl"}})
	if err != nil {
		t.Fatal(err)
	}
	if wait.Status != agentcomm.WaitPending {
		t.Fatalf("wait status = %s", wait.Status)
	}
	_, err = orch.Send(ctx, orchestrator.SendRequest{OperationID: "side-full-filler", ThreadID: thread.ID, Sender: "stage:impl", Recipients: []agentcomm.ParticipantID{orch.SideAgentParticipantID()}, Kind: agentcomm.MessageKindMessage, Body: "filler", Actor: state.Actor{Kind: "agent", ID: "stage:impl"}})
	if err != nil {
		t.Fatal(err)
	}
	close(runner.release)
	waitSideMesh(t, func() bool {
		rs := store.Snapshot()
		return rs.Assistants.Mesh.Receipts[source.MessageID][orch.SideAgentParticipantID()] == agentcomm.DeliveryUndeliverable && rs.Assistants.Mesh.Waits[wait.WaitID].Status != agentcomm.WaitPending
	})
	rs := store.Snapshot()
	if got := rs.Assistants.Mesh.Waits[wait.WaitID].Status; got != agentcomm.WaitCancelled {
		t.Fatalf("wait status = %s, want cancelled", got)
	}
	if sideReplyCount(rs, source.MessageID) != 0 {
		t.Fatal("full thread contains impossible side response")
	}
}

func TestSideAgentMesh_BoundedUTF8Body(t *testing.T) {
	body := boundedSideBody(strings.Repeat("🦊", agentcomm.MaxBodyBytes))
	if len(body) > agentcomm.MaxBodyBytes || !utf8.ValidString(body) || !strings.HasSuffix(body, "[Response truncated]") {
		t.Fatalf("invalid bounded body: %d bytes", len(body))
	}
	if got := boundedSideBody("bad\xffbyte"); !utf8.ValidString(got) {
		t.Fatalf("invalid UTF-8 remained: %q", got)
	}
}

func TestSideAgentMesh_OverlongFinalAnswerIsLinkedAndBounded(t *testing.T) {
	runner := &meshSideRunner{started: make(chan struct{}, 1), answer: strings.Repeat("🦊", agentcomm.MaxBodyBytes)}
	orch, store, _ := meshSideHarness(t, runner)
	source, err := orch.SendFromAgent(context.Background(), orchestrator.SendRequest{OperationID: "side-long-source", Sender: "user", Recipients: []agentcomm.ParticipantID{orch.SideAgentParticipantID()}, Kind: agentcomm.MessageKindMessage, Body: "long answer please", Actor: state.Actor{Kind: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	waitSideMesh(t, func() bool { return sideReplyCount(store.Snapshot(), source.MessageID) == 1 })
	for _, m := range store.Snapshot().Assistants.Mesh.Messages {
		if m.InReplyTo == nil || *m.InReplyTo != source.MessageID {
			continue
		}
		if len(m.Body) > agentcomm.MaxBodyBytes || !utf8.ValidString(m.Body) || !strings.HasSuffix(m.Body, "[Response truncated]") {
			t.Fatalf("invalid Mesh body: %d bytes", len(m.Body))
		}
		return
	}
	t.Fatal("missing long answer")
}

func TestSideAgentMesh_ContextAdmissionFailureDoesNotClaimDelivery(t *testing.T) {
	runner := &meshSideRunner{started: make(chan struct{}, 1), answer: "never runs"}
	orch, store, _ := meshSideHarness(t, runner, sideagent.ContextBuilder{ByteLimit: 1})
	source, err := orch.SendFromAgent(context.Background(), orchestrator.SendRequest{OperationID: "side-context-source", Sender: "user", Recipients: []agentcomm.ParticipantID{orch.SideAgentParticipantID()}, Kind: agentcomm.MessageKindMessage, Body: "too much for context", Actor: state.Actor{Kind: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	waitSideMesh(t, func() bool {
		rs := store.Snapshot()
		return sideReplyCount(rs, source.MessageID) == 1 && rs.Assistants.Mesh.Receipts[source.MessageID][orch.SideAgentParticipantID()] == agentcomm.DeliveryUndeliverable
	})
	select {
	case <-runner.started:
		t.Fatal("rejected message started executor")
	default:
	}
	rs := store.Snapshot()
	if got := rs.Assistants.Mesh.Receipts[source.MessageID][orch.SideAgentParticipantID()]; got != agentcomm.DeliveryUndeliverable {
		t.Fatalf("receipt = %s, want undeliverable", got)
	}
	for _, m := range rs.Assistants.Mesh.Messages {
		if m.InReplyTo != nil && *m.InReplyTo == source.MessageID && m.Kind != agentcomm.MessageKindResult {
			t.Fatalf("failure kind = %s", m.Kind)
		}
	}
}

func TestSideAgentMesh_PassiveResultIsInputWithoutReflex(t *testing.T) {
	runner := &meshSideRunner{started: make(chan struct{}, 1), answer: "I read it"}
	orch, store, bridge := meshSideHarness(t, runner)
	msg, err := orch.SendFromAgent(context.Background(), orchestrator.SendRequest{
		OperationID: "side-passive-source", Sender: "stage:impl", Recipients: []agentcomm.ParticipantID{orch.SideAgentParticipantID()},
		Kind: agentcomm.MessageKindResult, Body: "passive result", AutoResult: true, Actor: state.Actor{Kind: "system"},
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.started:
	case <-time.After(5 * time.Second):
		t.Fatal("passive result did not enter a side turn")
	}
	waitSideMesh(t, func() bool {
		accepted, terminal := sideMeshTurns(bridge.mgr.History())
		_, done := terminal[accepted[string(msg.MessageID)]]
		return done
	})
	if n := sideReplyCount(store.Snapshot(), msg.MessageID); n != 0 {
		t.Fatalf("passive input got %d reflex replies", n)
	}
}

func TestSideAgentMesh_ReconcilePassiveOrphanDoesNotReplayFailureNotice(t *testing.T) {
	orch, meshStore := newSideMeshOrch(t)
	orch.SetSideAgentAvailable(true)
	source, err := orch.SendFromAgent(context.Background(), orchestrator.SendRequest{
		OperationID: "side-passive-orphan", Sender: "stage:impl", Recipients: []agentcomm.ParticipantID{orch.SideAgentParticipantID()},
		Kind: agentcomm.MessageKindResult, Body: "passive result", AutoResult: true, Actor: state.Actor{Kind: "system"},
	})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	sideStore, err := sideagent.Open(root, "side-test")
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
	sideStore, err = sideagent.Open(root, "side-test") // recovers the accepted turn as interrupted
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sideStore.Close() })
	mgr := sideagent.New(sideagent.Config{Store: sideStore, Command: "codex", NewExecutor: func(executor.Config) sideagent.AgentRunner {
		t.Error("passive orphan was rerun")
		return nil
	}})
	bridge := &sideAgentMeshBridge{orch: orch, mgr: mgr, noticed: make(map[agentcomm.MessageID]bool)}
	if !bridge.reconcile() {
		t.Fatal("orphan reconciliation did not finish")
	}
	if got := meshStore.Snapshot().Assistants.Mesh.Receipts[source.MessageID][orch.SideAgentParticipantID()]; got != agentcomm.DeliveryUndeliverable {
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
	// A process restart scans this same durable inbox and receipt from the beginning.
	subID, events := orch.UIBus().Subscribe(8)
	defer orch.UIBus().Unsubscribe(subID)
	restarted := &sideAgentMeshBridge{orch: orch, mgr: mgr, noticed: make(map[agentcomm.MessageID]bool)}
	if !restarted.reconcile() {
		t.Fatal("restarted reconciliation did not finish")
	}
	select {
	case notice := <-events:
		if notice.Type == bus.EventSideAgentMeshFailure {
			t.Fatalf("passive orphan produced a failure notice: %+v", notice)
		}
	default:
	}
	if n := sideReplyCount(meshStore.Snapshot(), source.MessageID); n != 0 {
		t.Fatalf("passive orphan got %d reflex replies", n)
	}
}

func TestSideAgentMesh_RestartReconcilesOrphansAndCompletedAnswer(t *testing.T) {
	orch, meshStore := newSideMeshOrch(t)
	orch.SetSideAgentAvailable(true)
	var sources []orchestrator.SendReceipt
	for i, body := range []string{"accepted orphan", "started orphan", "completed reply"} {
		r, err := orch.SendFromAgent(context.Background(), orchestrator.SendRequest{OperationID: "side-recovery-" + string(rune('a'+i)), Sender: "user", Recipients: []agentcomm.ParticipantID{orch.SideAgentParticipantID()}, Kind: agentcomm.MessageKindMessage, Body: body, Actor: state.Actor{Kind: "user"}})
		if err != nil {
			t.Fatal(err)
		}
		sources = append(sources, r)
	}
	root := t.TempDir()
	sideStore, err := sideagent.Open(root, "side-test")
	if err != nil {
		t.Fatal(err)
	}
	for i, source := range sources {
		turn := "orphan-" + string(rune('a'+i))
		if _, err := sideStore.Append(sideagent.EventTurnAccepted, turn, sideagent.TurnAcceptedData{Message: []string{"accepted orphan", "started orphan", "completed reply"}[i], ClientMessageID: "mesh:" + string(source.MessageID), SourceMessageID: string(source.MessageID), SourceThreadID: string(source.ThreadID), SourceSenderID: "user"}); err != nil {
			t.Fatal(err)
		}
		if i > 0 {
			if _, err := sideStore.Append(sideagent.EventTurnStarted, turn, sideagent.TurnStartedData{Command: "codex"}); err != nil {
				t.Fatal(err)
			}
		}
		if i == 2 {
			if _, err := sideStore.Append(sideagent.EventAssistantText, turn, sideagent.AssistantTextData{Text: "saved answer"}); err != nil {
				t.Fatal(err)
			}
			if _, err := sideStore.Append(sideagent.EventTurnCompleted, turn, sideagent.TurnCompletedData{}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := sideStore.Close(); err != nil {
		t.Fatal(err)
	}
	sideStore, err = sideagent.Open(root, "side-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sideStore.Close() })
	mgr := sideagent.New(sideagent.Config{Store: sideStore, Command: "codex", NewExecutor: func(executor.Config) sideagent.AgentRunner { t.Error("orphan was rerun"); return nil }})
	b := startSideAgentMesh(orch, mgr)
	t.Cleanup(func() {
		b.CloseInput()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if !b.Drain(ctx) {
			t.Error("bridge failed to drain")
		}
	})
	waitSideMesh(t, func() bool {
		rs := meshStore.Snapshot()
		for _, source := range sources {
			if sideReplyCount(rs, source.MessageID) != 1 || rs.Assistants.Mesh.Receipts[source.MessageID][orch.SideAgentParticipantID()] != agentcomm.DeliveryUndeliverable {
				return false
			}
		}
		return true
	})
	for i, source := range sources {
		var got *agentcomm.Message
		snap := meshStore.Snapshot()
		for _, m := range snap.Assistants.Mesh.Messages {
			if m.InReplyTo != nil && *m.InReplyTo == source.MessageID && m.AutoResult {
				got = m
			}
		}
		if got == nil {
			t.Fatal("missing terminal response")
		}
		if i < 2 && got.Kind != agentcomm.MessageKindResult {
			t.Fatalf("orphan %d kind = %s", i, got.Kind)
		}
		if gotReceipt := snap.Assistants.Mesh.Receipts[source.MessageID][orch.SideAgentParticipantID()]; gotReceipt != agentcomm.DeliveryUndeliverable {
			t.Fatalf("recovered turn %d receipt = %s; no subprocess started in this process", i, gotReceipt)
		}
		if i == 2 && (got.Kind != agentcomm.MessageKindReply || got.Body != "saved answer") {
			t.Fatalf("completed outcome = %+v", got)
		}
	}
	b.CloseInput()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !b.Drain(ctx) {
		t.Fatal("bridge did not drain")
	}
	for range 2 {
		if !b.reconcile() {
			t.Fatal("idempotent reconciliation was not idle")
		}
	}
	if len(meshStore.Snapshot().Assistants.Mesh.Messages) != 6 {
		t.Fatal("duplicate response after reconciliation")
	}
}

func TestSideAgentMesh_FlowFinalizationWaitsForActiveAndQueuedTurns(t *testing.T) {
	runner := &meshSideRunner{started: make(chan struct{}, 3), release: make(chan struct{}), answer: "answer"}
	orch, store, bridge := meshSideHarness(t, runner)
	orch.SetSideAgentDrain(func(ctx context.Context) bool {
		bridge.CloseInput()
		return bridge.Drain(ctx) && bridge.mgr.Drain(ctx)
	})
	var sources []agentcomm.MessageID
	for i := range 3 {
		r, err := orch.SendFromAgent(context.Background(), orchestrator.SendRequest{
			OperationID: "side-finalize-" + string(rune('a'+i)), Sender: "user", Recipients: []agentcomm.ParticipantID{orch.SideAgentParticipantID()},
			Kind: agentcomm.MessageKindMessage, Body: "question", Actor: state.Actor{Kind: "user"},
		})
		if err != nil {
			t.Fatal(err)
		}
		sources = append(sources, r.MessageID)
	}
	<-runner.started
	for _, ev := range []bus.FSMEvent{bus.EvReady, bus.EvStartRun, bus.EvComplete} {
		if _, ok := orch.Trigger("impl", ev, bus.GuardCtx{}, "test"); !ok {
			t.Fatalf("stage transition %s rejected", ev)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- orch.Run(ctx) }()
	select {
	case err := <-done:
		t.Fatalf("run finalized before side drain: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(runner.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("run did not finalize after side drain")
	}
	rs := store.Snapshot()
	for _, id := range sources {
		if n := sideReplyCount(rs, id); n != 1 {
			t.Fatalf("source %s has %d terminal replies", id, n)
		}
	}
}

func TestSideAgentMesh_FinalizedFlowKeepsDirectModalAdmissionUntilShutdown(t *testing.T) {
	runner := &meshSideRunner{started: make(chan struct{}, 1), answer: "modal answer"}
	orch, _, bridge := meshSideHarness(t, runner)
	runtime := &sideAgentRuntime{manager: bridge.mgr, mesh: bridge}
	orch.SetSideAgentDrain(runtime.drainMesh)
	for _, ev := range []bus.FSMEvent{bus.EvReady, bus.EvStartRun, bus.EvComplete} {
		if _, ok := orch.Trigger("impl", ev, bus.GuardCtx{}, "test"); !ok {
			t.Fatalf("stage transition %s rejected", ev)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := orch.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.manager.Send("modal-after-flow", "still here?", sideagent.TurnTarget{}); err != nil {
		t.Fatalf("direct modal turn rejected while dashboard grace remains: %v", err)
	}
	select {
	case <-runner.started:
	case <-ctx.Done():
		t.Fatal("direct modal turn did not start")
	}
	if !runtime.shutdownNormal(ctx) {
		t.Fatal("direct modal turn did not drain at dashboard shutdown")
	}
}

func TestSideAgentMesh_CorruptSideStoreDoesNotStartRuntimeOrHoldLock(t *testing.T) {
	previous := rootDir
	rootDir = t.TempDir()
	t.Cleanup(func() { rootDir = previous })
	s, err := sideagent.Open(fmDir(), "corrupt-side")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Dir(), "chat.jsonl")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{broken}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runtime, err := startSideAgent("corrupt-side", config.Default(), runEnvironment{}, nil, nil)
	if err == nil || runtime != nil {
		t.Fatalf("corrupt store started runtime: %v, %v", runtime, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	reopened, err := sideagent.Open(fmDir(), "corrupt-side")
	if errors.Is(err, sideagent.ErrLocked) {
		t.Fatal("corrupt store lock leaked")
	}
	if err != nil {
		t.Fatal(err)
	}
	_ = reopened.Close()
}

func TestSideAgentMesh_UnavailableStartupReleasesHistoricalInboxAndReplaysNotice(t *testing.T) {
	for _, mode := range []string{"disabled", "failed-store"} {
		t.Run(mode, func(t *testing.T) {
			runDir := t.TempDir()
			orch, meshStore := newSideMeshOrchAt(t, runDir)
			orch.SetSideAgentAvailable(true)
			ctx := context.Background()
			thread, err := orch.OpenThread(ctx, orchestrator.OpenThreadRequest{
				OperationID: "unavailable-thread", Scope: agentcomm.ScopeRun,
				Members:     []agentcomm.ParticipantID{"stage:impl", orch.SideAgentParticipantID()},
				MaxMessages: 1, Actor: state.Actor{Kind: "system"},
			})
			if err != nil {
				t.Fatal(err)
			}
			source, err := orch.Send(ctx, orchestrator.SendRequest{
				OperationID: "unavailable-source", ThreadID: thread.ID, Sender: "stage:impl",
				Recipients: []agentcomm.ParticipantID{orch.SideAgentParticipantID()},
				Kind:       agentcomm.MessageKindMessage, Body: "question", Actor: state.Actor{Kind: "agent", ID: "stage:impl"},
			})
			if err != nil {
				t.Fatal(err)
			}
			wait, err := orch.Wait(ctx, orchestrator.WaitRequest{
				OperationID: "unavailable-wait", Waiter: "stage:impl",
				Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: source.MessageID},
				Actor:     state.Actor{Kind: "agent", ID: "stage:impl"},
			})
			if err != nil || wait.Status != agentcomm.WaitPending {
				t.Fatalf("pending reply wait = %+v, %v", wait, err)
			}
			var mgr *sideagent.Manager
			if mode == "failed-store" {
				root := t.TempDir()
				s, err := sideagent.Open(root, "side-test")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := s.Append(sideagent.EventTurnAccepted, "orphan", sideagent.TurnAcceptedData{
					Message: "question", ClientMessageID: "mesh:" + string(source.MessageID),
					SourceMessageID: string(source.MessageID), SourceThreadID: string(source.ThreadID), SourceSenderID: "stage:impl",
				}); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(s.Dir(), "chat.jsonl")
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.WriteString("{broken}\n"); err != nil {
					t.Fatal(err)
				}
				if err := f.Close(); err != nil {
					t.Fatal(err)
				}
				s, err = sideagent.Open(root, "side-test")
				if err == nil || s == nil || !s.Unavailable() {
					t.Fatalf("expected unavailable side store, got %v, %v", s, err)
				}
				t.Cleanup(func() { _ = s.Close() })
				mgr = sideagent.New(sideagent.Config{Store: s, Command: "codex"})
			}
			var bridge *sideAgentMeshBridge
			if mode == "disabled" {
				bridge, err = startHistoricalSideAgentMesh(ctx, orch)
				if err != nil || bridge == nil {
					t.Fatalf("historical side bridge = %v, %v", bridge, err)
				}
			} else {
				bridge = startSideAgentMesh(orch, mgr)
			}
			defer bridge.Abort()
			drainCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			if !bridge.Drain(drainCtx) {
				t.Fatal("unavailable bridge did not drain")
			}
			rs := meshStore.Snapshot()
			if got := rs.Assistants.Mesh.Receipts[source.MessageID][orch.SideAgentParticipantID()]; got != agentcomm.DeliveryUndeliverable {
				t.Fatalf("source receipt = %s, want undeliverable", got)
			}
			if got := rs.Assistants.Mesh.Waits[wait.WaitID].Status; got != agentcomm.WaitCancelled {
				t.Fatalf("reply wait = %s, want cancelled", got)
			}
			if sideReplyCount(rs, source.MessageID) != 0 {
				t.Fatal("full thread gained a side reply")
			}
			page, err := orch.ListParticipants(ctx, orchestrator.ListParticipantsRequest{})
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range page.Participants {
				if p.ID == orch.SideAgentParticipantID() && p.Reachable {
					t.Fatal("unavailable historical side appears reachable")
				}
			}
			if _, err := orch.SendFromAgent(ctx, orchestrator.SendRequest{
				OperationID: "unavailable-new-send", Sender: "user", Recipients: []agentcomm.ParticipantID{orch.SideAgentParticipantID()},
				Kind: agentcomm.MessageKindMessage, Body: "later", Actor: state.Actor{Kind: "user"},
			}); err == nil {
				t.Fatal("unavailable side accepted a new message")
			}
			// Recreate the crash gap after the durable marker and before its
			// best-effort notice survived, then replay from the Mesh receipt.
			noticePath := filepath.Join(runDir, "notices.jsonl")
			if err := os.Remove(noticePath); err != nil {
				t.Fatal(err)
			}
			restarted, err := startHistoricalSideAgentMesh(ctx, orch)
			if err != nil || restarted == nil {
				t.Fatalf("restarted historical bridge = %v, %v", restarted, err)
			}
			defer restarted.Abort()
			if !restarted.Drain(drainCtx) {
				t.Fatal("restarted unavailable bridge did not drain")
			}
			raw, err := os.ReadFile(noticePath)
			if err != nil || !strings.Contains(string(raw), `"type":"side_agent_mesh_failure"`) || !strings.Contains(string(raw), string(source.MessageID)) {
				t.Fatalf("missing replayed failure notice: %s, %v", raw, err)
			}
		})
	}
}
