package orchestrator

import (
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/state"
)

// TestResultNoticeStage: the "turn result not delivered" warning goes to a feed that shows the conversation —
// the addressed stage, else the task's own stage, else (a run agent answering the human or another run agent)
// the stage the reply's conversation was opened from.
func TestResultNoticeStage(t *testing.T) {
	rs := *state.NewRunState(nil)
	rs.Assistants.Mesh.Threads = map[agentcomm.ThreadID]*agentcomm.Thread{
		"th-impl": {ID: "th-impl", OriginStage: "impl"},
		"th-none": {ID: "th-none"},
	}
	reply := func(thread agentcomm.ThreadID, to agentcomm.ParticipantID) *plannedMessage {
		return &plannedMessage{send: SendRequest{ThreadID: thread, Recipients: []agentcomm.ParticipantID{to}}}
	}
	for _, tc := range []struct {
		name      string
		rootStage string
		reply     *plannedMessage
		want      string
	}{
		{"no reply: the task's stage", "plan", nil, "plan"},
		{"reply to a stage: that stage", "plan", reply("th-impl", meshStageParticipantID("review")), "review"},
		{"helper replying to the human: its own stage", "plan", reply("th-impl", meshUserParticipantID), "plan"},
		{"run agent replying to the human: the conversation's origin stage", "", reply("th-impl", meshUserParticipantID), "impl"},
		{"run agent in a conversation from no stage: the global feed", "", reply("th-none", "run:t-2"), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := resultNoticeStage(rs, tc.rootStage, tc.reply); got != tc.want {
				t.Fatalf("resultNoticeStage = %q, want %q", got, tc.want)
			}
		})
	}
}
