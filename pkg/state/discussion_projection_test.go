package state

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/akopichin/afm/pkg/assistants"
)

func discussionMessageControl(opID, msgID, stageID string, round int, parent string) *Control {
	payload := DiscussionMessagePayload{AuthorKind: "assistant", AuthorID: "qa", Round: round, ParentMessageID: parent, Body: "x"}
	raw, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return &Control{
		Kind: kindDiscussionMessage, SchemaVersion: ControlSchemaVersion, OperationID: opID,
		Actor: Actor{Kind: "assistant"}, Target: Target{StageID: stageID, MessageID: msgID},
		Payload: raw,
	}
}

func TestDiscussionProjection_RoundTripSurvivesReplay(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	c := discussionMessageControl("op-1", "m-1", "a", assistants.RoundIndependent, "")
	if err := store.AppendControl(c); err != nil {
		t.Fatalf("AppendControl: %v", err)
	}
	store.Close()

	rs, err := LoadRunState(dir)
	if err != nil {
		t.Fatalf("LoadRunState: %v", err)
	}
	msg, ok := rs.Assistants.Discussions.Messages["m-1"]
	if !ok {
		t.Fatal("message m-1 not restored after replay")
	}
	if msg.Author.ID != "qa" || msg.Round != assistants.RoundIndependent {
		t.Fatalf("message content not restored correctly: %+v", msg)
	}
	ids := rs.Assistants.Discussions.ByStage["a"]
	if len(ids) != 1 || ids[0] != "m-1" {
		t.Fatalf("ByStage index not restored: %+v", ids)
	}
}

func TestDiscussionProjection_RoundLimitEnforcedAtCommit(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	if err := store.AppendControl(discussionMessageControl("op-1", "m-1", "a", assistants.RoundIndependent, "")); err != nil {
		t.Fatalf("first independent message: %v", err)
	}
	err = store.AppendControl(discussionMessageControl("op-2", "m-2", "a", assistants.RoundIndependent, ""))
	if !errors.Is(err, assistants.ErrRoundLimitExceeded) {
		t.Fatalf("a second independent-round message must be rejected at commit time, got %v", err)
	}
	// A different stage's discussion has its own independent budget.
	if err := store.AppendControl(discussionMessageControl("op-3", "m-3", "b", assistants.RoundIndependent, "")); err != nil {
		t.Fatalf("stage b's own independent round must be unaffected: %v", err)
	}
}

func TestDiscussionProjection_UnknownParentRejected(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	err = store.AppendControl(discussionMessageControl("op-1", "m-1", "a", assistants.RoundDiscussion, "does-not-exist"))
	if err == nil {
		t.Fatal("expected rejection for unknown parent_message_id")
	}
}

// TestDiscussionProjection_OldJournalWithoutMeshEffectsStillReplays is the Agent Mesh Phase 6, T6.4
// compatibility regression: a journal written by the PRE-T6.4 binary contains ONLY discussion_message
// records — orchestrator.PostDiscussionMessage never bundled a mesh effect alongside them. T6.4's mesh
// mirror (pkg/orchestrator/agentcomm_discussion.go) is purely ADDITIVE to the SAME ApplyRunOperation call a
// NEW post makes; it never rewrites or re-interprets an EXISTING discussion_message record, and replay never
// invokes any delivery/spawn side effect regardless (pkg/state has no dependency on pkg/orchestrator at all —
// LoadRunState only ever calls the pure apply* projection functions in this file). This test proves the
// DiscussionMessageView-equivalent data (ids, order, content) survives replay unchanged, and that nothing
// was synthesized into the Mesh projection for a run whose journal never mentions it.
func TestDiscussionProjection_OldJournalWithoutMeshEffectsStillReplays(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := store.AppendControl(discussionMessageControl("op-1", "m-1", "a", assistants.RoundIndependent, "")); err != nil {
		t.Fatalf("AppendControl m-1: %v", err)
	}
	if err := store.AppendControl(discussionMessageControl("op-2", "m-2", "a", assistants.RoundDiscussion, "m-1")); err != nil {
		t.Fatalf("AppendControl m-2: %v", err)
	}
	store.Close()

	rs, err := LoadRunState(dir)
	if err != nil {
		t.Fatalf("LoadRunState: %v", err)
	}

	ids := rs.Assistants.Discussions.ByStage["a"]
	if len(ids) != 2 || ids[0] != "m-1" || ids[1] != "m-2" {
		t.Fatalf("ByStage order not preserved: got %v, want [m-1 m-2]", ids)
	}
	msgs := assistants.RestoreOrder(ids, rs.Assistants.Discussions.Messages)
	if len(msgs) != 2 || msgs[0].ID != "m-1" || msgs[1].ID != "m-2" || msgs[1].ParentMessageID != "m-1" {
		t.Fatalf("restored messages do not match the original journal: %+v", msgs)
	}

	// No mesh mirror was ever written for this run — replaying it must not synthesize one.
	if n := len(rs.Assistants.Mesh.Threads); n != 0 {
		t.Fatalf("replay must not synthesize any mesh thread for an old discussion-only journal, got %d", n)
	}
	if n := len(rs.Assistants.Mesh.Participants); n != 0 {
		t.Fatalf("replay must not synthesize any mesh participant for an old discussion-only journal, got %d", n)
	}
	if n := len(rs.Assistants.Mesh.Messages); n != 0 {
		t.Fatalf("replay must not synthesize any mesh message for an old discussion-only journal, got %d", n)
	}
}

// A discussion message carries the journal seq of its record — the run's one durable order, shared with mesh
// messages — live and after a replay, so a reader can interleave the two without trusting timestamps.
func TestDiscussionProjection_MessageCarriesItsJournalSeq(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for _, id := range []string{"m-1", "m-2"} {
		if err := store.AppendControl(discussionMessageControl("op-"+id, id, "a", assistants.RoundFreeform, "")); err != nil {
			t.Fatalf("AppendControl: %v", err)
		}
	}
	live := store.Snapshot().Assistants.Discussions.Messages
	store.Close()
	rs, err := LoadRunState(dir)
	if err != nil {
		t.Fatalf("LoadRunState: %v", err)
	}
	for _, msgs := range []map[string]*assistants.DiscussionMessage{live, rs.Assistants.Discussions.Messages} {
		first, second := msgs["m-1"], msgs["m-2"]
		if first == nil || second == nil || first.Seq == 0 || second.Seq <= first.Seq {
			t.Fatalf("seqs = %+v / %+v, want increasing journal seqs", first, second)
		}
	}
}
