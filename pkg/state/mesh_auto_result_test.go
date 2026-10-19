package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
)

// A turn result afm posts on an agent's behalf (mesh_message_sent with auto_result) is PASSIVE: it lands in
// the recipient's inbox, but never counts as a waking unread message.

func autoResultControl(seq uint64, msgID, threadID, sender string, recipients []string, inReplyTo *string, body string) *Control {
	payload, _ := json.Marshal(MeshMessageSentPayload{
		Sender: sender, Recipients: recipients, Kind: string(agentcomm.MessageKindReply), InReplyTo: inReplyTo,
		Body: body, AutoResult: true,
	})
	return &Control{
		Kind: kindMeshMessageSent, SchemaVersion: ControlSchemaVersion, Seq: seq,
		Actor: Actor{Kind: "system"}, Target: Target{MessageID: msgID, ThreadID: threadID}, Payload: payload,
	}
}

func TestMesh_AutoResultIsInTheInboxButDoesNotWake(t *testing.T) {
	as := setupMeshThread(t, "a", "b")
	mustApply(t, &as, meshMessageSentControl(1, "q", "th-1", "a", []string{"b"}, string(agentcomm.MessageKindRequest), nil, "question"))
	mustApply(t, &as, meshMessageReceiptControl(kindMeshMessageDelivered, "q", "b"))
	mustApply(t, &as, autoResultControl(2, "r", "th-1", "b", []string{"a"}, strp("q"), "summary"))

	if !as.Mesh.Messages["r"].AutoResult {
		t.Fatal("the projected message lost auto_result")
	}
	if ids := as.Mesh.Inbox["a"]; len(ids) != 1 || ids[0] != "r" || as.Mesh.Receipts["r"]["a"] != agentcomm.DeliveryAccepted {
		t.Fatalf("the turn result must wait in a's inbox as accepted, got inbox %v receipts %v", ids, as.Mesh.Receipts["r"])
	}
	if as.Mesh.HasWakingUnreadFor("a") {
		t.Fatal("a passive turn result must not count as a waking unread message")
	}
	mustApply(t, &as, meshMessageSentControl(3, "m", "th-1", "b", []string{"a"}, string(agentcomm.MessageKindMessage), nil, "a real message"))
	if !as.Mesh.HasWakingUnreadFor("a") {
		t.Fatal("an ordinary message must still wake its recipient")
	}
}

// A thread-less addressed_message wait ("wake me when anybody writes") is not satisfied by a turn result; a
// reply_to wait on the very message it answers is.
func TestMesh_AutoResultResolvesReplyToButNotThreadlessAddressedWait(t *testing.T) {
	as := setupMeshThread(t, "a", "b")
	mustApply(t, &as, meshMessageSentControl(1, "q", "th-1", "a", []string{"b"}, string(agentcomm.MessageKindRequest), nil, "question"))
	mustApply(t, &as, meshWaitRegisteredControl("anybody", "a", agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage}, "", "", 0))
	mustApply(t, &as, meshWaitRegisteredControl("answer", "a", agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: "q"}, "", "", 0))

	mustApply(t, &as, autoResultControl(2, "r", "th-1", "b", []string{"a"}, strp("q"), "summary"))

	if got := as.Mesh.Waits["anybody"].Status; got != agentcomm.WaitPending {
		t.Fatalf("thread-less addressed_message wait = %q, want pending", got)
	}
	if got := as.Mesh.Waits["answer"].Status; got != agentcomm.WaitReady {
		t.Fatalf("reply_to wait = %q, want ready", got)
	}
}

// TestMesh_JournalWithoutAutoResultLoads: a journal written before auto_result existed loads, and its messages
// stay ordinary — waking — messages.
func TestMesh_JournalWithoutAutoResultLoads(t *testing.T) {
	dir := t.TempDir()
	raw, err := os.ReadFile(filepath.Join("testdata", "mesh-pre-auto-result", "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	rs, err := LoadRunState(dir)
	if err != nil {
		t.Fatalf("load the old journal: %v", err)
	}
	const agent = agentcomm.ParticipantID("run:t-c81bb0462afcbdc62a60")
	m := rs.Assistants.Mesh.Messages["mm-05c6db070e50052ddd72"]
	if m == nil || m.AutoResult {
		t.Fatalf("old message = %+v, want an ordinary message", m)
	}
	if !rs.Assistants.Mesh.HasWakingUnreadFor(agent) {
		t.Fatal("an old unread message must still wake its recipient")
	}
}
