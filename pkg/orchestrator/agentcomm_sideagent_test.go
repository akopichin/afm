package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/state"
)

// Agent Mesh Phase 6, T6.7: the bound Side agent becomes a participant of the SAME Service.

func registerTestSideAgent(t *testing.T, o *Orchestrator) agentcomm.Participant {
	t.Helper()
	p, err := o.RegisterSideAgent(context.Background(), RegisterSideAgentRequest{
		DisplayName: "claude", Actor: state.Actor{Kind: "system"},
	})
	if err != nil {
		t.Fatalf("RegisterSideAgent: %v", err)
	}
	o.SetSideAgentAvailable(true)
	return p
}

func TestSideAgentMesh_AvailabilityGatesRosterAndSends(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	if _, err := o.RegisterSideAgent(context.Background(), RegisterSideAgentRequest{DisplayName: "codex", Actor: state.Actor{Kind: "system"}}); err != nil {
		t.Fatal(err)
	}
	checkReachable := func(want bool) {
		t.Helper()
		page, err := o.ListParticipants(context.Background(), ListParticipantsRequest{})
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range page.Participants {
			if p.ID == o.SideAgentParticipantID() {
				if p.Reachable != want {
					t.Fatalf("side reachable = %v, want %v", p.Reachable, want)
				}
				return
			}
		}
		t.Fatal("historical side participant disappeared")
	}
	send := func(op string) error {
		_, err := o.SendFromAgent(context.Background(), SendRequest{OperationID: op, Sender: "user", Recipients: []agentcomm.ParticipantID{o.SideAgentParticipantID()}, Kind: agentcomm.MessageKindMessage, Body: "hello", Actor: state.Actor{Kind: "user"}})
		return err
	}
	checkReachable(false)
	if err := send("side-off-1"); err == nil {
		t.Fatal("side accepted send before bridge startup")
	}
	o.SetSideAgentAvailable(true)
	checkReachable(true)
	if err := send("side-on"); err != nil {
		t.Fatal(err)
	}
	o.SetSideAgentAvailable(false)
	checkReachable(false)
	if err := send("side-off-2"); err == nil {
		t.Fatal("side accepted send after shutdown")
	}
	resumed := New(o.opts) // same durable participant, capability not restarted
	page, err := resumed.ListParticipants(context.Background(), ListParticipantsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range page.Participants {
		if p.ID == resumed.SideAgentParticipantID() {
			if p.Reachable {
				t.Fatal("resumed side without bridge is reachable")
			}
			return
		}
	}
	t.Fatal("resumed run lost historical side participant")
}

func TestSideAgentMesh_FailureMarkerReplaysNoticeAndExplicitReplyWins(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	side := registerTestSideAgent(t, o).ID
	ctx := context.Background()
	first, err := o.SendFromAgent(ctx, SendRequest{OperationID: "side-failure-one", Sender: "user", Recipients: []agentcomm.ParticipantID{side}, Kind: agentcomm.MessageKindMessage, Body: "one", Actor: state.Actor{Kind: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := o.SideAgentMeshResponseFailed(first.MessageID, "conversation unavailable"); err != nil {
		t.Fatal(err)
	}
	if got := store.Snapshot().Assistants.Mesh.Receipts[first.MessageID][side]; got != agentcomm.DeliveryUndeliverable {
		t.Fatalf("receipt = %s", got)
	}
	path := filepath.Join(o.opts.RunDir, "notices.jsonl")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	} // crash after marker, before notice append
	o.RepublishSideAgentMeshFailure(first.MessageID)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"type":"side_agent_mesh_failure"`) || !strings.Contains(string(data), string(first.MessageID)) {
		t.Fatalf("missing keyed replay notice: %s", data)
	}

	second, err := o.SendFromAgent(ctx, SendRequest{OperationID: "side-failure-two", Sender: "user", Recipients: []agentcomm.ParticipantID{side}, Kind: agentcomm.MessageKindMessage, Body: "two", Actor: state.Actor{Kind: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.SendFromAgent(ctx, SendRequest{OperationID: "side-explicit-before-failure", Sender: side, Kind: agentcomm.MessageKindReply, InReplyTo: &second.MessageID, Body: "answered", Actor: state.Actor{Kind: "agent", ID: string(side)}}); err != nil {
		t.Fatal(err)
	}
	if err := o.SideAgentMeshResponseFailed(second.MessageID, "could not answer"); !errors.Is(err, ErrSideAgentExplicitReply) {
		t.Fatalf("failure after explicit reply = %v", err)
	}
	if got := store.Snapshot().Assistants.Mesh.Receipts[second.MessageID][side]; got == agentcomm.DeliveryUndeliverable {
		t.Fatal("explicit answer was marked undeliverable")
	}
}

// TestSideAgent_HasParticipantIdentity — the bound side agent appears in ListParticipants as
// "side:<runID>", kind side_agent, with its configured executor as DisplayName and a display alias; a
// message it originates is attributed to that id, never to "user" and never to a stage agent id.
func TestSideAgent_HasParticipantIdentity(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()

	p := registerTestSideAgent(t, o)
	wantID := agentcomm.ParticipantID("side:run1") // newAssistantsTestOrchestrator fixes RunID to "run1"
	if p.ID != wantID {
		t.Fatalf("participant id = %q, want %q", p.ID, wantID)
	}
	if p.Kind != agentcomm.KindSideAgent {
		t.Fatalf("kind = %q, want %q", p.Kind, agentcomm.KindSideAgent)
	}
	if p.Alias == "" {
		t.Fatal("expected a non-empty display alias (so @-mentions can address it)")
	}
	if p.DisplayName != "claude" {
		t.Fatalf("display name = %q, want the configured executor %q", p.DisplayName, "claude")
	}
	if !p.Reachable {
		t.Fatal("a freshly registered side agent must be reachable")
	}

	page, err := o.ListParticipants(ctx, ListParticipantsRequest{Actor: state.Actor{Kind: "user"}})
	if err != nil {
		t.Fatalf("ListParticipants: %v", err)
	}
	found := false
	for _, got := range page.Participants {
		if got.ID != wantID {
			continue
		}
		found = true
		if got.Kind != agentcomm.KindSideAgent {
			t.Fatalf("roster entry kind = %q, want %q", got.Kind, agentcomm.KindSideAgent)
		}
	}
	if !found {
		t.Fatalf("ListParticipants did not include %q", wantID)
	}

	// A message the side agent originates is attributed to ITS id — never "user", never a stage agent.
	thread, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-side-thread", Title: "side chat", Scope: agentcomm.ScopeRun,
		Members: []agentcomm.ParticipantID{meshUserParticipantID, wantID}, Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	receipt, err := o.Send(ctx, SendRequest{
		OperationID: "op-side-send", ThreadID: thread.ID, Sender: wantID,
		Recipients: []agentcomm.ParticipantID{meshUserParticipantID}, Kind: agentcomm.MessageKindMessage,
		Body: "hello from the side agent", Actor: state.Actor{Kind: "system"},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	rs := store.Snapshot()
	msg, ok := rs.Assistants.Mesh.Messages[receipt.MessageID]
	if !ok {
		t.Fatalf("message %s not found after Send", receipt.MessageID)
	}
	if msg.Sender != wantID {
		t.Fatalf("message sender = %q, want %q", msg.Sender, wantID)
	}
	if msg.Sender == meshUserParticipantID {
		t.Fatal("a side-agent-originated message must never be attributed to \"user\"")
	}
	if msg.Sender == meshStageParticipantID(assistantsTestStageID) {
		t.Fatal("a side-agent-originated message must never be attributed to a stage agent")
	}
}

// TestSideAgent_CanAddressAndBeAddressed — the side agent can Send to a stage agent, a helper and a run
// agent, and can receive a reply; delivery obeys the same receipts and gates as any other participant (no
// privileged inbox).
func TestSideAgent_CanAddressAndBeAddressed(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	stubPassingHelper(o)

	sideID := registerTestSideAgent(t, o).ID
	stageID := meshStageParticipantID(assistantsTestStageID)
	// Run agent ниже стартует с тем же блокирующим stub-ом, что и helper. Cleanup выполняется в обратном
	// порядке, поэтому этот (зарегистрированный раньше) дожидается run agent уже после того, как cleanup
	// newRunningHelperForMesh отпустит stub, и до удаления TempDir.
	var runAgentTask assistants.TaskID
	t.Cleanup(func() {
		if runAgentTask != "" {
			waitForRunAgentTurn(t, store, runAgentTask, 2*time.Second)
		}
	})
	taskID := newRunningHelperForMesh(t, o, store, "op-side-helper")
	helperID := meshHelperParticipantID(taskID)

	msg := "hi run agent"
	runRes, err := o.AttachAndSend(ctx, AttachAndSendRequest{
		OperationID: "op-side-run-agent", ProfileID: "qa", Scope: assistants.ScopeRun,
		Title: "My QA", Instruction: "watch for regressions", FirstMessage: &msg, Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Fatalf("AttachAndSend (run agent): %v", err)
	}
	runAgentID := runRes.ParticipantID
	runAgentTask = runRes.TaskID

	for _, to := range []agentcomm.ParticipantID{stageID, helperID, runAgentID} {
		thread, err := o.OpenThread(ctx, OpenThreadRequest{
			OperationID: "op-side-open-" + string(to), Title: "side <-> " + string(to), Scope: agentcomm.ScopeRun,
			Members: []agentcomm.ParticipantID{sideID, to}, Actor: state.Actor{Kind: "system"},
		})
		if err != nil {
			t.Fatalf("OpenThread(side,%s): %v", to, err)
		}

		sent, err := o.Send(ctx, SendRequest{
			OperationID: "op-side-send-" + string(to), ThreadID: thread.ID, Sender: sideID,
			Recipients: []agentcomm.ParticipantID{to}, Kind: agentcomm.MessageKindMessage, Body: "ping",
			Actor: state.Actor{Kind: "system"},
		})
		if err != nil {
			t.Fatalf("Send(side -> %s): %v", to, err)
		}

		replyID := sent.MessageID
		if _, err := o.Send(ctx, SendRequest{
			OperationID: "op-side-reply-" + string(to), ThreadID: thread.ID, Sender: to,
			Recipients: []agentcomm.ParticipantID{sideID}, Kind: agentcomm.MessageKindReply, InReplyTo: &replyID,
			Body: "pong", Actor: state.Actor{Kind: "system"},
		}); err != nil {
			t.Fatalf("Send(%s -> side, reply): %v", to, err)
		}

		// Delivered into the side agent's inbox exactly like any other participant — no privileged bypass.
		inbox, err := o.ReadInbox(ctx, ReadInboxRequest{Participant: sideID, Actor: state.Actor{Kind: "system"}})
		if err != nil {
			t.Fatalf("ReadInbox(side): %v", err)
		}
		foundReply := false
		for _, m := range inbox.Messages {
			if m.ThreadID == thread.ID && m.Sender == to {
				foundReply = true
			}
		}
		if !foundReply {
			t.Fatalf("side agent inbox missing the reply from %s; inbox=%+v thread=%s sent=%s", to, inbox, thread.ID, sent.MessageID)
		}
	}
}

// TestSideAgent_DisabledCapabilityLeavesNoParticipant — with the side agent off (RegisterSideAgent never
// called — the real gate lives in cmd/afm's sideAgentEnabled check around the one production call site), no
// "side:" participant exists and nothing in the mesh references it.
func TestSideAgent_DisabledCapabilityLeavesNoParticipant(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)

	page, err := o.ListParticipants(ctx, ListParticipantsRequest{Actor: state.Actor{Kind: "user"}})
	if err != nil {
		t.Fatalf("ListParticipants: %v", err)
	}
	for _, p := range page.Participants {
		if p.Kind == agentcomm.KindSideAgent {
			t.Fatalf("unexpected side_agent participant %+v with the side agent never registered", p)
		}
	}

	rs := store.Snapshot()
	wantID := o.SideAgentParticipantID()
	if _, ok := rs.Assistants.Mesh.Participants[wantID]; ok {
		t.Fatalf("mesh durably references %q although RegisterSideAgent was never called", wantID)
	}
}

func TestSideAgent_MeshClientLazyAndScoped(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	if o.SideAgentMeshClient() != nil {
		t.Fatal("client minted before dashboard URL")
	}
	o.opts.DashboardURL = "http://127.0.0.1:9876"
	client := o.SideAgentMeshClient()
	if client == nil || client.ParticipantID != string(o.SideAgentParticipantID()) {
		t.Fatalf("wrong side client: %+v", client)
	}
	_, token, revoke := client.Mint()
	if got, ok := o.ResolveMeshToken(token); !ok || got != o.SideAgentParticipantID() {
		t.Fatal("side token not scoped to side participant")
	}
	revoke()
	if _, ok := o.ResolveMeshToken(token); ok {
		t.Fatal("side token was not revoked")
	}
}
