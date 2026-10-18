package state

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
)

// ---- T1.2: messages, inbox references and receipts ----

func strp(s string) *string { return &s }

func meshMessageSentControl(seq uint64, msgID, threadID, sender string, recipients []string, kind string, inReplyTo *string, body string) *Control {
	payload, _ := json.Marshal(MeshMessageSentPayload{
		Sender: sender, Recipients: recipients, Kind: kind, InReplyTo: inReplyTo, Body: body,
	})
	return &Control{
		Kind: kindMeshMessageSent, SchemaVersion: ControlSchemaVersion, Seq: seq,
		Actor: Actor{Kind: "system"}, Target: Target{MessageID: msgID, ThreadID: threadID}, Payload: payload,
	}
}

func meshMessageReceiptControl(kind, msgID, participantID string) *Control {
	payload, _ := json.Marshal(MeshMessageReceiptPayload{})
	return &Control{
		Kind: kind, SchemaVersion: ControlSchemaVersion,
		Target: Target{MessageID: msgID, ParticipantID: participantID}, Payload: payload,
	}
}

// setupMeshThread creates one participant per name in members, opens thread "th-1" with exactly those
// members, and returns the resulting AssistantsState — the common fixture every T1.2 test below starts from.
func setupMeshThread(t *testing.T, members ...string) AssistantsState {
	t.Helper()
	as := newAssistantsState()
	for _, m := range members {
		mustApply(t, &as, meshParticipantAddedControl(m, string(agentcomm.KindStageAgent), string(agentcomm.ScopeStage)))
	}
	mustApply(t, &as, meshThreadOpenedControl("th-1", members))
	return as
}

func TestMesh_MessageSentCreatesInboxAndReceipts(t *testing.T) {
	as := setupMeshThread(t, "a", "b", "c")
	c := meshMessageSentControl(10, "m1", "th-1", "a", []string{"b", "c"}, string(agentcomm.MessageKindMessage), nil, "hello")
	mustApply(t, &as, c)

	if len(as.Mesh.Messages) != 1 {
		t.Fatalf("Messages = %d, want 1", len(as.Mesh.Messages))
	}
	msg, ok := as.Mesh.Messages["m1"]
	if !ok {
		t.Fatal("message m1 not projected")
	}
	if msg.Seq != 10 {
		t.Fatalf("message.Seq = %d, want 10 (carried from the control record)", msg.Seq)
	}
	for _, r := range []agentcomm.ParticipantID{"b", "c"} {
		if ids := as.Mesh.Inbox[r]; len(ids) != 1 || ids[0] != "m1" {
			t.Fatalf("Inbox[%s] = %v, want exactly [m1]", r, ids)
		}
		if got := as.Mesh.Receipts["m1"][r]; got != agentcomm.DeliveryAccepted {
			t.Fatalf("Receipts[m1][%s] = %q, want accepted", r, got)
		}
	}
	if ids := as.Mesh.MessagesByThread["th-1"]; len(ids) != 1 || ids[0] != "m1" {
		t.Fatalf("MessagesByThread[th-1] = %v, want [m1]", ids)
	}
}

func TestMesh_MessageToNonMemberRejected(t *testing.T) {
	as := setupMeshThread(t, "a", "b")
	mustApply(t, &as, meshParticipantAddedControl("ghost", string(agentcomm.KindHelper), string(agentcomm.ScopeStage)))

	c := meshMessageSentControl(1, "m1", "th-1", "a", []string{"ghost"}, string(agentcomm.MessageKindMessage), nil, "hi")
	if err := dispatchControlKind(&as, c, false); err == nil {
		t.Fatal("message addressed to a non-member of the thread: want error, got nil")
	}
	if len(as.Mesh.Messages) != 0 {
		t.Fatalf("Messages = %d, want 0 (rejection must happen before any write)", len(as.Mesh.Messages))
	}
	if len(as.Mesh.Inbox["ghost"]) != 0 {
		t.Fatalf("Inbox[ghost] = %v, want empty", as.Mesh.Inbox["ghost"])
	}
	if len(as.Mesh.MessagesByThread["th-1"]) != 0 {
		t.Fatalf("MessagesByThread[th-1] = %v, want empty", as.Mesh.MessagesByThread["th-1"])
	}
}

func TestMesh_MessageInReplyToUnknownRejected(t *testing.T) {
	as := setupMeshThread(t, "a", "b")

	c := meshMessageSentControl(1, "m1", "th-1", "a", []string{"b"}, string(agentcomm.MessageKindReply), strp("nope"), "re: x")
	if err := dispatchControlKind(&as, c, false); err == nil {
		t.Fatal("in_reply_to naming an unknown message id: want error, got nil")
	}
	if len(as.Mesh.Messages) != 0 {
		t.Fatalf("Messages = %d, want 0", len(as.Mesh.Messages))
	}
}

func TestMesh_MessageSeqMonotonic(t *testing.T) {
	as := setupMeshThread(t, "a", "b")
	mustApply(t, &as, meshMessageSentControl(3, "m1", "th-1", "a", []string{"b"}, string(agentcomm.MessageKindMessage), nil, "one"))
	mustApply(t, &as, meshMessageSentControl(7, "m2", "th-1", "a", []string{"b"}, string(agentcomm.MessageKindMessage), nil, "two"))

	if as.Mesh.Messages["m1"].Seq != 3 {
		t.Fatalf("m1.Seq = %d, want 3 (the control record's own Seq field)", as.Mesh.Messages["m1"].Seq)
	}
	if as.Mesh.Messages["m2"].Seq != 7 {
		t.Fatalf("m2.Seq = %d, want 7", as.Mesh.Messages["m2"].Seq)
	}
	if as.Mesh.Messages["m2"].Seq <= as.Mesh.Messages["m1"].Seq {
		t.Fatal("seq must be monotonic across multiple sends")
	}
}

// TestMesh_DuplicateMessageSentIsNoop mirrors TestMesh_DuplicateParticipantRejected's convention: the
// message id is the dedup key (minted deterministically by the caller, e.g. from the operation id), so
// re-applying the same mesh_message_sent record a second time is rejected before anything is mutated —
// exactly like every other T1.1/T1.2 "duplicate own id" case in this package — leaving exactly one message
// behind rather than a second copy.
func TestMesh_DuplicateMessageSentIsNoop(t *testing.T) {
	as := setupMeshThread(t, "a", "b")
	mustApply(t, &as, meshMessageSentControl(1, "m1", "th-1", "a", []string{"b"}, string(agentcomm.MessageKindMessage), nil, "one"))

	dup := meshMessageSentControl(2, "m1", "th-1", "a", []string{"b"}, string(agentcomm.MessageKindMessage), nil, "one-again")
	if err := dispatchControlKind(&as, dup, false); err == nil {
		t.Fatal("re-applying mesh_message_sent with the same message id: want error, got nil")
	}
	if len(as.Mesh.Messages) != 1 {
		t.Fatalf("Messages = %d, want 1 (no second message from the replayed duplicate)", len(as.Mesh.Messages))
	}
	if as.Mesh.Messages["m1"].Body != "one" {
		t.Fatalf("original message mutated by the rejected duplicate: %+v", as.Mesh.Messages["m1"])
	}
	if len(as.Mesh.Inbox["b"]) != 1 {
		t.Fatalf("Inbox[b] = %v, want exactly one entry (no second inbox reference)", as.Mesh.Inbox["b"])
	}
}

// TestMesh_ReceiptLifecycle walks one message through accepted -> delivered -> acknowledged -> answered
// (answered is always derived from MessageAnswered, never a stored receipt value — see Receipts' own doc
// comment), then proves a receipt can never regress to an earlier status once it has advanced.
func TestMesh_ReceiptLifecycle(t *testing.T) {
	as := setupMeshThread(t, "a", "b")
	mustApply(t, &as, meshMessageSentControl(1, "m1", "th-1", "a", []string{"b"}, string(agentcomm.MessageKindMessage), nil, "q"))

	if got := as.Mesh.Receipts["m1"]["b"]; got != agentcomm.DeliveryAccepted {
		t.Fatalf("initial receipt = %q, want accepted", got)
	}

	mustApply(t, &as, meshMessageReceiptControl(kindMeshMessageDelivered, "m1", "b"))
	if got := as.Mesh.Receipts["m1"]["b"]; got != agentcomm.DeliveryDelivered {
		t.Fatalf("after delivered receipt = %q, want delivered", got)
	}

	mustApply(t, &as, meshMessageReceiptControl(kindMeshMessageAcked, "m1", "b"))
	if got := as.Mesh.Receipts["m1"]["b"]; got != agentcomm.DeliveryAcknowledged {
		t.Fatalf("after acked receipt = %q, want acknowledged", got)
	}

	if as.Mesh.MessageAnswered("m1") {
		t.Fatal("MessageAnswered(m1) = true before any reply exists")
	}
	mustApply(t, &as, meshMessageSentControl(2, "m2", "th-1", "b", []string{"a"}, string(agentcomm.MessageKindReply), strp("m1"), "a"))
	if !as.Mesh.MessageAnswered("m1") {
		t.Fatal("MessageAnswered(m1) = false after a reply naming it as in_reply_to")
	}
	// "answered" must never be written into Receipts itself — it stays acknowledged, computed as "answered"
	// only by MessageAnswered reading the message set fresh.
	if got := as.Mesh.Receipts["m1"]["b"]; got != agentcomm.DeliveryAcknowledged {
		t.Fatalf("an unrelated reply mutated the stored receipt: %q, want it to stay acknowledged", got)
	}

	// No regression: re-delivering an already-acknowledged message must not downgrade its receipt.
	mustApply(t, &as, meshMessageReceiptControl(kindMeshMessageDelivered, "m1", "b"))
	if got := as.Mesh.Receipts["m1"]["b"]; got != agentcomm.DeliveryAcknowledged {
		t.Fatalf("receipt regressed from acknowledged to %q after a stale delivered record", got)
	}
}

func TestMesh_MessageSentDryRunMutatesNothing(t *testing.T) {
	as := setupMeshThread(t, "a", "b")
	c := meshMessageSentControl(1, "m1", "th-1", "a", []string{"b"}, string(agentcomm.MessageKindMessage), nil, "q")
	if err := dispatchControlKind(&as, c, true); err != nil {
		t.Fatalf("dryRun apply: %v", err)
	}
	if len(as.Mesh.Messages) != 0 {
		t.Fatalf("dryRun created a message: %+v", as.Mesh.Messages)
	}
	if len(as.Mesh.Inbox["b"]) != 0 {
		t.Fatalf("dryRun created an inbox reference: %v", as.Mesh.Inbox["b"])
	}
	if len(as.Mesh.Receipts) != 0 {
		t.Fatalf("dryRun created a receipt: %+v", as.Mesh.Receipts)
	}
	if len(as.Mesh.MessagesByThread["th-1"]) != 0 {
		t.Fatalf("dryRun appended to MessagesByThread: %v", as.Mesh.MessagesByThread["th-1"])
	}
}

// TestMesh_MessageSentRejectsAtMaxThreadMessages (follow-up to the 5-agent review): MaxThreadMessages is an
// explicit rejection, never a silent truncation.
func TestMesh_MessageSentRejectsAtMaxThreadMessages(t *testing.T) {
	as := setupMeshThread(t, "a", "b")
	var seq uint64
	for i := 0; i < agentcomm.MaxThreadMessages; i++ {
		seq++
		mustApply(t, &as, meshMessageSentControl(seq, fmt.Sprintf("m%d", i), "th-1", "a", []string{"b"}, string(agentcomm.MessageKindMessage), nil, "q"))
	}
	if got := len(as.Mesh.MessagesByThread["th-1"]); got != agentcomm.MaxThreadMessages {
		t.Fatalf("thread has %d messages, want exactly %d before the rejection", got, agentcomm.MaxThreadMessages)
	}

	seq++
	err := dispatchControlKind(&as, meshMessageSentControl(seq, "m-overflow", "th-1", "a", []string{"b"}, string(agentcomm.MessageKindMessage), nil, "one too many"), false)
	if err == nil {
		t.Fatal("thread already at MaxThreadMessages: want an explicit rejection, got nil error")
	}
	if got := len(as.Mesh.MessagesByThread["th-1"]); got != agentcomm.MaxThreadMessages {
		t.Fatalf("the rejected message was still appended: thread now has %d messages", got)
	}
}

// TestMesh_MessageSentRejectsAtMaxInboxUnread spreads messages to the SAME recipient across many different
// threads (so no single thread's MaxThreadMessages is anywhere near its own limit) to isolate the
// cross-thread per-recipient MaxInboxUnread check.
func TestMesh_MessageSentRejectsAtMaxInboxUnread(t *testing.T) {
	as := newAssistantsState()
	mustApply(t, &as, meshParticipantAddedControl("a", string(agentcomm.KindStageAgent), string(agentcomm.ScopeStage)))
	mustApply(t, &as, meshParticipantAddedControl("qa", string(agentcomm.KindStageAgent), string(agentcomm.ScopeStage)))

	var seq uint64
	for i := 0; i < agentcomm.MaxInboxUnread; i++ {
		threadID := fmt.Sprintf("th-%d", i)
		seq++
		mustApply(t, &as, meshThreadOpenedControl(threadID, []string{"a", "qa"}))
		seq++
		mustApply(t, &as, meshMessageSentControl(seq, fmt.Sprintf("m%d", i), threadID, "a", []string{"qa"}, string(agentcomm.MessageKindMessage), nil, "q"))
	}
	if got := len(as.Mesh.Inbox["qa"]); got != agentcomm.MaxInboxUnread {
		t.Fatalf("qa's inbox has %d entries, want exactly %d before the rejection", got, agentcomm.MaxInboxUnread)
	}

	overflowThread := "th-overflow"
	seq++
	mustApply(t, &as, meshThreadOpenedControl(overflowThread, []string{"a", "qa"}))
	seq++
	err := dispatchControlKind(&as, meshMessageSentControl(seq, "m-overflow", overflowThread, "a", []string{"qa"}, string(agentcomm.MessageKindMessage), nil, "one too many"), false)
	if err == nil {
		t.Fatal("qa already at MaxInboxUnread: want an explicit rejection, got nil error")
	}
	if got := len(as.Mesh.Inbox["qa"]); got != agentcomm.MaxInboxUnread {
		t.Fatalf("the rejected message was still added to qa's inbox: now has %d entries", got)
	}
}
