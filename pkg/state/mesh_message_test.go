package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
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

// A thread's own message budget (MaxMessages) is enforced exactly at its boundary, not the global limit.
func TestMesh_MessageSentRejectsAtThreadBudget(t *testing.T) {
	as := newAssistantsState()
	mustApply(t, &as, meshParticipantAddedControl("a", string(agentcomm.KindStageAgent), string(agentcomm.ScopeStage)))
	mustApply(t, &as, meshParticipantAddedControl("b", string(agentcomm.KindStageAgent), string(agentcomm.ScopeStage)))
	payload, _ := json.Marshal(MeshThreadOpenedPayload{Scope: string(agentcomm.ScopeStage), Members: []string{"a", "b"}, MaxMessages: 3})
	mustApply(t, &as, &Control{Kind: kindMeshThreadOpened, SchemaVersion: ControlSchemaVersion, Target: Target{ThreadID: "th-small"}, Payload: payload})

	for i := range 3 {
		mustApply(t, &as, meshMessageSentControl(uint64(i+1), fmt.Sprintf("m%d", i), "th-small", "a", []string{"b"}, string(agentcomm.MessageKindMessage), nil, "q"))
	}
	err := dispatchControlKind(&as, meshMessageSentControl(4, "m-over", "th-small", "a", []string{"b"}, string(agentcomm.MessageKindMessage), nil, "one too many"), true)
	var rej *OperationRejectedError
	if !errors.As(err, &rej) || rej.Code != assistants.CodeBudgetExceeded {
		t.Fatalf("4th message in a 3-message thread: err = %v, want budget_exceeded", err)
	}
	if got := len(as.Mesh.MessagesByThread["th-small"]); got != 3 {
		t.Fatalf("thread has %d messages, want 3", got)
	}
}

// TestMesh_ReplayKeepsThreadFilledPastItsBudget: journals written before a thread's own budget was enforced
// hold threads filled past it (only the global limit applied). Replaying one must not fail at the first
// message above the budget.
func TestMesh_ReplayKeepsThreadFilledPastItsBudget(t *testing.T) {
	payload, _ := json.Marshal(MeshThreadOpenedPayload{Scope: string(agentcomm.ScopeStage), Members: []string{"a", "b"}, MaxMessages: 1})
	controls := []*Control{
		meshParticipantAddedControl("a", string(agentcomm.KindStageAgent), string(agentcomm.ScopeStage)),
		meshParticipantAddedControl("b", string(agentcomm.KindStageAgent), string(agentcomm.ScopeStage)),
		{Kind: kindMeshThreadOpened, SchemaVersion: ControlSchemaVersion, Target: Target{ThreadID: "th-small"}, Payload: payload},
		meshMessageSentControl(0, "m1", "th-small", "a", []string{"b"}, string(agentcomm.MessageKindMessage), nil, "first"),
		meshMessageSentControl(0, "m2", "th-small", "b", []string{"a"}, string(agentcomm.MessageKindMessage), nil, "second, past the budget"),
	}
	rs := NewRunState(nil)
	if res := parseEventLog(journalOf(t, controls), rs); res.corrupted || res.unsupported != nil {
		t.Fatalf("an old journal with a thread past its own budget must replay: corrupted=%v unsupported=%v", res.corrupted, res.unsupported)
	}
	if got := len(rs.Assistants.Mesh.MessagesByThread["th-small"]); got != 2 {
		t.Fatalf("replayed thread has %d messages, want 2", got)
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

// TestMesh_InboxCapacityCountsOnlyMessagesWaitingForHandover: the inbox limit bounds messages not yet handed
// over. Delivered messages free their place, and the human — who reads threads in the dashboard, never
// through a handover — is never blocked by messages addressed to them.
func TestMesh_InboxCapacityCountsOnlyMessagesWaitingForHandover(t *testing.T) {
	as := newAssistantsState()
	mustApply(t, &as, meshParticipantAddedControl("user", string(agentcomm.KindUser), string(agentcomm.ScopeRun)))
	mustApply(t, &as, meshParticipantAddedControl("qa", string(agentcomm.KindStageAgent), string(agentcomm.ScopeStage)))

	var seq uint64
	send := func(id, thread, from, to string) error {
		seq++
		return dispatchControlKind(&as, meshMessageSentControl(seq, id, thread, from, []string{to}, string(agentcomm.MessageKindMessage), nil, "q"), false)
	}
	for i := range agentcomm.MaxInboxUnread {
		thread := fmt.Sprintf("th-%d", i)
		mustApply(t, &as, meshThreadOpenedControl(thread, []string{"user", "qa"}))
		if err := send(fmt.Sprintf("to-qa-%d", i), thread, "user", "qa"); err != nil {
			t.Fatalf("send to qa %d: %v", i, err)
		}
		mustApply(t, &as, meshMessageReceiptControl(kindMeshMessageDelivered, fmt.Sprintf("to-qa-%d", i), "qa"))
		if err := send(fmt.Sprintf("to-user-%d", i), thread, "qa", "user"); err != nil {
			t.Fatalf("send to the human %d: %v", i, err)
		}
	}
	mustApply(t, &as, meshThreadOpenedControl("th-more", []string{"user", "qa"}))
	if err := send("to-qa-more", "th-more", "user", "qa"); err != nil {
		t.Fatalf("qa has read every message, a new one must be accepted: %v", err)
	}
	if err := send("to-user-more", "th-more", "qa", "user"); err != nil {
		t.Fatalf("messages to the human must never hit the inbox limit: %v", err)
	}
}

// TestMesh_HumanInboxKeepsNewestMessages: the human's inbox index is bounded by agentcomm.MaxHumanInbox and
// keeps the newest messages; an agent's inbox is bounded by capacity instead, never trimmed.
func TestMesh_HumanInboxKeepsNewestMessages(t *testing.T) {
	ms := newMeshState()
	ms.Participants["user"] = &agentcomm.Participant{ID: "user", Kind: agentcomm.KindUser, Scope: agentcomm.ScopeRun}
	ms.Participants["helper:t1"] = &agentcomm.Participant{ID: "helper:t1", Kind: agentcomm.KindHelper, Scope: agentcomm.ScopeStage}
	total := agentcomm.MaxHumanInbox + 5
	for i := range total {
		id := agentcomm.MessageID(fmt.Sprintf("m%d", i))
		ms.appendToInbox("user", id)
		ms.appendToInbox("helper:t1", id)
	}
	human := ms.Inbox["user"]
	if len(human) != agentcomm.MaxHumanInbox || human[0] != "m5" || human[len(human)-1] != agentcomm.MessageID(fmt.Sprintf("m%d", total-1)) {
		t.Fatalf("human inbox = %d entries from %s to %s, want the newest %d", len(human), human[0], human[len(human)-1], agentcomm.MaxHumanInbox)
	}
	if got := len(ms.Inbox["helper:t1"]); got != total {
		t.Fatalf("agent inbox = %d entries, want all %d", got, total)
	}
}
