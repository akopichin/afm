package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/sideagent"
	"github.com/akopichin/afm/pkg/state"
)

// gatedRun — один запуск агента: тест видит промпт и сам решает, когда ход завершится.
type gatedRun struct {
	prompt  string
	release chan struct{}
}

type gatedFactory struct{ runs chan *gatedRun }

type gatedRunner struct {
	f   *gatedFactory
	cfg executor.Config
}

func (f *gatedFactory) make(cfg executor.Config) sideagent.AgentRunner {
	return &gatedRunner{f: f, cfg: cfg}
}

func (r *gatedRunner) RunAgent(ctx context.Context, _, _, prompt, _ string) error {
	if r.cfg.OnDelivered != nil {
		r.cfg.OnDelivered()
	}
	run := &gatedRun{prompt: prompt, release: make(chan struct{})}
	r.f.runs <- run
	select {
	case <-run.release:
	case <-r.cfg.InterruptCh:
		return executor.ErrUserInterrupted
	case <-ctx.Done():
		return ctx.Err()
	}
	r.cfg.OnAction("text", "answer")
	return nil
}

func (f *gatedFactory) next(t *testing.T) *gatedRun {
	t.Helper()
	select {
	case r := <-f.runs:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("side turn did not start")
		return nil
	}
}

func (f *gatedFactory) expectNone(t *testing.T) {
	t.Helper()
	select {
	case r := <-f.runs:
		t.Fatalf("unexpected side turn: %q", r.prompt)
	case <-time.After(150 * time.Millisecond):
	}
}

// gatedSideHarness builds a started Manager and Mesh bridge over a gated runner factory.
func gatedSideHarness(t *testing.T) (*orchestrator.Orchestrator, *state.Store, *sideAgentMeshBridge, *gatedFactory) {
	t.Helper()
	orch, meshStore := newSideMeshOrch(t)
	sideStore, err := sideagent.Open(t.TempDir(), "side-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sideStore.Close() })
	f := &gatedFactory{runs: make(chan *gatedRun, 16)}
	mgr := newStartedSideManager(sideagent.Config{
		Store: sideStore, Command: "codex", SourceReply: orch.SideAgentExplicitReply,
		OnSourceDelivered: func(id string) {
			if err := orch.MarkMessageDelivered(context.Background(), agentcomm.MessageID(id), orch.SideAgentParticipantID()); err != nil {
				t.Errorf("deliver: %v", err)
			}
		},
		NewExecutor: f.make,
	})
	bridge := startSideAgentMesh(orch, mgr)
	t.Cleanup(func() {
		mgr.Shutdown()
		bridge.Abort()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		bridge.Drain(ctx)
		mgr.Drain(ctx)
	})
	return orch, meshStore, bridge, f
}

func sendMeshToSide(t *testing.T, orch *orchestrator.Orchestrator, op, body string) agentcomm.MessageID {
	t.Helper()
	r, err := orch.SendFromAgent(context.Background(), orchestrator.SendRequest{
		OperationID: op, Sender: "user", Recipients: []agentcomm.ParticipantID{orch.SideAgentParticipantID()},
		Kind: agentcomm.MessageKindMessage, Body: body, Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return r.MessageID
}

// Queued modal messages written before a restart run only after the runtime calls Start.
func TestSideAgentQueue_RestartKeepsQueueUntilStart(t *testing.T) {
	dir := t.TempDir()
	s1, err := sideagent.Open(dir, "side-test")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = s1.Append(sideagent.EventTurnAccepted, "t-a", sideagent.TurnAcceptedData{Message: "AAA", ClientMessageID: "cA"})
	_, _ = s1.Append(sideagent.EventMessageQueued, "", sideagent.MessageQueuedData{MessageID: "m1", ClientMessageID: "c1", Message: "BBB"})
	_, _ = s1.Append(sideagent.EventMessageQueued, "", sideagent.MessageQueuedData{MessageID: "m2", ClientMessageID: "c2", Message: "CCC"})
	_ = s1.Close()

	s2, err := sideagent.Open(dir, "side-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() }) // выполняется после Shutdown/Drain ниже
	f := &gatedFactory{runs: make(chan *gatedRun, 4)}
	mgr := sideagent.New(sideagent.Config{Store: s2, Command: "codex", NewExecutor: f.make})
	t.Cleanup(func() {
		mgr.Shutdown()
		mgr.Drain(context.Background())
	})
	f.expectNone(t)
	if st, _ := s2.Conversation().TurnState("t-a"); st != sideagent.TurnStateInterrupted {
		t.Fatalf("active turn after restart = %q, want interrupted", st)
	}
	mgr.Start()
	run := f.next(t)
	if !strings.Contains(run.prompt, "BBB") || !strings.Contains(run.prompt, "CCC") {
		t.Fatalf("restart group prompt:\n%s", run.prompt)
	}
	close(run.release)
	if !mgr.Drain(context.Background()) {
		t.Fatal("drain")
	}
}

// A Mesh turn keeps its source while modal messages queue behind it; its reply is linked, then the queue runs.
func TestSideAgentQueue_ModalMessagesWaitBehindMeshTurn(t *testing.T) {
	orch, store, bridge, f := gatedSideHarness(t)
	source := sendMeshToSide(t, orch, "side-mesh-1", "mesh question")
	mesh := f.next(t)
	rc, err := bridge.mgr.Enqueue("cB", "modal B", sideagent.TurnTarget{})
	if err != nil || rc.State != sideagent.MessageStateQueued {
		t.Fatalf("enqueue behind mesh turn = %+v, %v", rc, err)
	}
	src := sideagent.ProjectConversation(bridge.mgr.History()).ActiveSource()
	if src == nil || src.Kind != sideagent.SourceMesh || src.SenderID != "user" {
		t.Fatalf("active source = %+v", src)
	}
	f.expectNone(t)
	close(mesh.release)
	waitSideMesh(t, func() bool { return sideReplyCount(store.Snapshot(), source) == 1 })
	modal := f.next(t)
	if !strings.Contains(modal.prompt, "modal B") {
		t.Fatalf("queued turn prompt:\n%s", modal.prompt)
	}
	close(modal.release)
}

// A Mesh message behind a non-empty modal queue waits for the queue to empty; flow finalization still completes.
func TestSideAgentQueue_MeshMessageWaitsForModalQueueAndFinalizationCompletes(t *testing.T) {
	orch, store, bridge, f := gatedSideHarness(t)
	orch.SetSideAgentDrain(func(ctx context.Context) bool {
		bridge.CloseInput()
		return bridge.Drain(ctx) && bridge.mgr.Drain(ctx)
	})
	if _, err := bridge.mgr.Enqueue("cA", "modal A", sideagent.TurnTarget{}); err != nil {
		t.Fatal(err)
	}
	runA := f.next(t)
	for _, id := range []string{"B", "C"} {
		if _, err := bridge.mgr.Enqueue("c"+id, "modal "+id, sideagent.TurnTarget{}); err != nil {
			t.Fatal(err)
		}
	}
	source := sendMeshToSide(t, orch, "side-mesh-2", "mesh later")
	for _, ev := range []bus.FSMEvent{bus.EvReady, bus.EvStartRun, bus.EvComplete} {
		if _, ok := orch.Trigger("impl", ev, bus.GuardCtx{}, "test"); !ok {
			t.Fatalf("stage transition %s rejected", ev)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- orch.Run(ctx) }()

	f.expectNone(t) // the Mesh message must not jump the queue
	close(runA.release)
	group := f.next(t)
	if !strings.Contains(group.prompt, "modal B") || !strings.Contains(group.prompt, "modal C") || strings.Contains(group.prompt, "mesh later") {
		t.Fatalf("group prompt:\n%s", group.prompt)
	}
	close(group.release)
	meshRun := f.next(t)
	if !strings.Contains(meshRun.prompt, "mesh later") {
		t.Fatalf("mesh turn prompt:\n%s", meshRun.prompt)
	}
	close(meshRun.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("flow finalization did not complete")
	}
	if n := sideReplyCount(store.Snapshot(), source); n != 1 {
		t.Fatalf("mesh source has %d replies", n)
	}
}

// Emergency shutdown interrupts the active turn and leaves the not-yet-started queue durable.
func TestSideAgentQueue_EmergencyShutdownKeepsQueuedMessages(t *testing.T) {
	_, _, bridge, f := gatedSideHarness(t)
	if _, err := bridge.mgr.Enqueue("cA", "modal A", sideagent.TurnTarget{}); err != nil {
		t.Fatal(err)
	}
	f.next(t)
	rc, err := bridge.mgr.Enqueue("cB", "modal B", sideagent.TurnTarget{})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &sideAgentRuntime{manager: bridge.mgr, mesh: bridge}
	if !runtime.shutdownEmergency(5 * time.Second) {
		t.Fatal("emergency shutdown did not drain")
	}
	f.expectNone(t)
	if q := sideagent.ProjectConversation(bridge.mgr.History()).Queued(); len(q) != 1 || q[0].MessageID != rc.MessageID {
		t.Fatalf("queue after emergency = %+v", q)
	}
}
