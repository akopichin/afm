package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/state"
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

// TestMesh_InboxBlockStaysWithinByteBudget: a full inbox of maximum-size messages must not become one
// multi-megabyte prompt. The block carries what fits inboxBlockByteBudget, says how many more are waiting, and
// only the rendered messages are handed over — the rest stay accepted for the next turn.
func TestMesh_InboxBlockStaysWithinByteBudget(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	h1 := meshHelperParticipantID(newRunningHelperForMesh(t, o, store, "op-ib-a"))
	h2 := meshHelperParticipantID(newRunningHelperForMesh(t, o, store, "op-ib-b"))
	thread, err := o.OpenThread(ctx, OpenThreadRequest{OperationID: "op-ib-open", Members: []agentcomm.ParticipantID{h1, h2}, Scope: agentcomm.ScopeStage})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	const sent = 10
	body := strings.Repeat("x", agentcomm.MaxBodyBytes)
	for i := range sent {
		if _, err := o.Send(ctx, SendRequest{OperationID: fmt.Sprintf("op-ib-send-%d", i), ThreadID: thread.ID, Sender: h1, Recipients: []agentcomm.ParticipantID{h2}, Kind: agentcomm.MessageKindMessage, Body: body}); err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
	}

	block, onDelivered := o.agentCommInboxFor(h2)
	if len(block) > inboxBlockByteBudget+4096 {
		t.Fatalf("inbox block is %d bytes, want it bounded near %d", len(block), inboxBlockByteBudget)
	}
	rendered := strings.Count(block, body)
	if rendered == 0 || rendered == sent {
		t.Fatalf("rendered %d of %d maximum-size messages, want some but not all", rendered, sent)
	}
	if want := fmt.Sprintf("%d more messages are waiting", sent-rendered); !strings.Contains(block, want) {
		t.Fatalf("inbox block must say %q:\n%.300s", want, block[len(block)-300:])
	}

	onDelivered()
	rs := store.Snapshot()
	accepted := 0
	for _, id := range rs.Assistants.Mesh.Inbox[h2] {
		if rs.Assistants.Mesh.Receipts[id][h2] == agentcomm.DeliveryAccepted {
			accepted++
		}
	}
	if accepted != sent-rendered {
		t.Fatalf("%d messages still accepted after the handover, want the %d that did not fit", accepted, sent-rendered)
	}
}

// TestMesh_DeliveryJournalFailureEndsRun: the handover of a rendered inbox is durable. If writing it fails,
// the messages are already in the agent's prompt but would stay accepted and be handed over again — so the
// failure is storage-fatal, like every other journal write failure.
func TestMesh_DeliveryJournalFailureEndsRun(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	stage := meshStageParticipantID(assistantsTestStageID)
	thread, err := o.OpenThread(ctx, OpenThreadRequest{OperationID: "op-df-open", Members: []agentcomm.ParticipantID{meshUserParticipantID, stage}, Scope: agentcomm.ScopeStage})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	if _, err := o.Send(ctx, SendRequest{OperationID: "op-df-send", ThreadID: thread.ID, Sender: meshUserParticipantID, Recipients: []agentcomm.ParticipantID{stage}, Kind: agentcomm.MessageKindMessage, Body: "hi"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	_, onDelivered := o.agentCommInboxFor(stage)

	store.CloseEventsLogForTest()
	onDelivered()

	var se *state.StorageError
	if fatal := o.loadFatal(); !errors.As(fatal, &se) {
		t.Fatalf("loadFatal() = %v, want the delivery journal failure as a *state.StorageError", fatal)
	}
}
