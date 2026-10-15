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
