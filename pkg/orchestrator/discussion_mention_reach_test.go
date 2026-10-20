package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

// A post that explicitly mentions someone who can no longer read it (here a stage that has finished) is
// refused with the reason, and nothing is written — the sender must not be told it reached them.
func TestDiscussion_UnreachableMentionIsRejected(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true, flow.Stage{ID: "qa", Command: "claude"})
	activateStage(t, o, assistantsTestStageID)
	activateStage(t, o, "qa")
	finishStage(t, o, "qa")

	_, err := o.PostDiscussionMessage(context.Background(), humanPost("op-post", "@qa did you check this?", []agentcomm.ParticipantID{meshStageParticipantID("qa")}, false))

	var rej *state.OperationRejectedError
	if !errors.As(err, &rej) || !strings.Contains(rej.Reason, "qa") {
		t.Fatalf("post mentioning a finished stage: err = %v, want a rejection naming it", err)
	}
	if n := len(store.Snapshot().Assistants.Discussions.ByStage[assistantsTestStageID]); n != 0 {
		t.Fatalf("discussion messages = %d, want none written", n)
	}
}

// The feed stage's own finished agent is the documented exception: the post is recorded (for the history and
// anyone else it names), marked undeliverable to that agent instead of being refused.
func TestDiscussion_PostToTheFeedStagesFinishedAgentIsRecorded(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	finishStage(t, o, assistantsTestStageID)

	id, err := o.PostDiscussionMessage(context.Background(), humanPost("op-post", "thanks", nil, false))
	if err != nil {
		t.Fatalf("post to a finished feed stage: %v, want it recorded", err)
	}
	if got := store.Snapshot().Assistants.Mesh.Receipts[agentcomm.MessageID(id)][meshStageParticipantID(assistantsTestStageID)]; got != agentcomm.DeliveryUndeliverable {
		t.Fatalf("receipt for the finished stage = %q, want undeliverable", got)
	}
}
