package orchestrator

import (
	"context"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/state"
)

// TestPlanMessage_RedactsAndMirrorsInOnePlace: every operation that carries a message builds it through
// planMessage — the thread-open effects it is given come first, the message body is redacted, and a reply to a
// Discussion post gets its Discussion mirror in the same effect list.
func TestPlanMessage_RedactsAndMirrorsInOnePlace(t *testing.T) {
	o, store := newTwoStageMeshOrchestrator(t)
	o.opts.SecretValues = []string{"s3cr3t-value"}
	qID, err := o.PostDiscussionMessage(context.Background(), PostMessageRequest{
		OperationID: "op-plan-q", StageID: assistantsTestStageID, AuthorKind: assistants.AuthorHuman,
		Round: assistants.RoundFreeform, Body: "question", Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Fatalf("PostDiscussionMessage: %v", err)
	}
	in := agentcomm.MessageID(qID)
	open := state.Effect{Kind: "mesh_thread_opened"} // stands for the effects that open the thread
	req := SendRequest{
		ThreadID: deriveDiscussionThreadID(assistantsTestStageID), Sender: meshStageParticipantID(assistantsTestStageID),
		Recipients: []agentcomm.ParticipantID{meshUserParticipantID}, Kind: agentcomm.MessageKindReply, InReplyTo: &in,
		Body: "the token is s3cr3t-value",
	}

	msg, err := o.planMessage(store.Snapshot(), req, "m-plan", []state.Effect{open})
	if err != nil {
		t.Fatalf("planMessage: %v", err)
	}
	if !msg.openedThread || msg.messageID != "m-plan" || strings.Contains(msg.send.Body, "s3cr3t-value") {
		t.Fatalf("planned message = %+v, want an opened thread, its id and a redacted body", msg)
	}
	var kinds []string
	for _, e := range msg.effects {
		kinds = append(kinds, e.Kind)
		if strings.Contains(string(e.Payload), "s3cr3t-value") {
			t.Fatalf("effect %s carries the secret: %s", e.Kind, e.Payload)
		}
	}
	if want := []string{"mesh_thread_opened", "mesh_message_sent", "discussion_message"}; strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Fatalf("effects = %v, want %v", kinds, want)
	}
}
