package orchestrator

import (
	"context"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
)

// Agent Mesh Phase 4, T4.4: the <inbox> block and delivery-on-handoff-only semantics.

func TestMesh_InboxBlockRendersPendingMessagesOnly(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	h1 := meshHelperParticipantID(newRunningHelperForMesh(t, o, store, "op-i-1a"))
	h2 := meshHelperParticipantID(newRunningHelperForMesh(t, o, store, "op-i-1b"))

	thread, err := o.OpenThread(ctx, OpenThreadRequest{OperationID: "op-i-1-open", Members: []agentcomm.ParticipantID{h1, h2}, Scope: agentcomm.ScopeStage})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	send, err := o.Send(ctx, SendRequest{OperationID: "op-i-1-send", ThreadID: thread.ID, Sender: h1, Recipients: []agentcomm.ParticipantID{h2}, Kind: agentcomm.MessageKindMessage, Body: "hello h2"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	block, onDelivered := o.agentCommInboxFor(h2)
	if block == "" {
		t.Fatal("expected a non-empty inbox block for h2")
	}
	if !containsAll(block, string(send.MessageID), "hello h2", string(h1)) {
		t.Fatalf("inbox block missing expected content:\n%s", block)
	}

	// h1 has nothing pending.
	h1Block, _ := o.agentCommInboxFor(h1)
	if h1Block != "" {
		t.Fatalf("h1 has no pending messages, expected an empty block, got:\n%s", h1Block)
	}

	// Crash-safety: if onDelivered is never called, the receipt stays at accepted, and a fresh read still
	// includes the same message (re-injected on the next attempt).
	rs := store.Snapshot()
	if got := rs.Assistants.Mesh.Receipts[send.MessageID][h2]; got != agentcomm.DeliveryAccepted {
		t.Fatalf("receipt before any OnDelivered call = %q, want accepted", got)
	}
	block2, _ := o.agentCommInboxFor(h2)
	if block2 != block {
		t.Fatal("a second read before OnDelivered must reproduce the same block")
	}

	// Once OnDelivered actually fires (the real handoff point), the receipt advances and a NEW read no
	// longer includes the message.
	onDelivered()
	rs = store.Snapshot()
	if got := rs.Assistants.Mesh.Receipts[send.MessageID][h2]; got != agentcomm.DeliveryDelivered {
		t.Fatalf("receipt after OnDelivered = %q, want delivered", got)
	}
	block3, _ := o.agentCommInboxFor(h2)
	if block3 != "" {
		t.Fatalf("an already-delivered message must not be re-rendered as pending, got:\n%s", block3)
	}
}
