package state

import (
	"errors"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
)

// A rejected message is a typed OperationRejectedError (code + human reason), so the API answers with a
// clear 4xx instead of a bare 500 and the sending agent can tell what to fix.
func TestMesh_MessageRejectionsAreTyped(t *testing.T) {
	cases := []struct {
		name string
		prep func(as *AssistantsState)
		c    *Control
		want assistants.ErrorCode
	}{
		{
			name: "unknown thread",
			c:    meshMessageSentControl(10, "m1", "no-such-thread", "a", []string{"b"}, string(agentcomm.MessageKindMessage), nil, "hi"),
			want: assistants.CodeNotFound,
		},
		{
			name: "closed thread",
			prep: func(as *AssistantsState) {
				mustApply(t, as, &Control{Kind: kindMeshThreadClosed, SchemaVersion: ControlSchemaVersion, Target: Target{ThreadID: "th-1"}})
			},
			c:    meshMessageSentControl(10, "m1", "th-1", "a", []string{"b"}, string(agentcomm.MessageKindMessage), nil, "hi"),
			want: assistants.CodeInvalidStatus,
		},
		{
			name: "recipient is not a member",
			c:    meshMessageSentControl(10, "m1", "th-1", "a", []string{"stranger"}, string(agentcomm.MessageKindMessage), nil, "hi"),
			want: assistants.CodeInvalidRequest,
		},
		{
			name: "no recipients",
			c:    meshMessageSentControl(10, "m1", "th-1", "a", nil, string(agentcomm.MessageKindMessage), nil, "hi"),
			want: assistants.CodeInvalidRequest,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			as := setupMeshThread(t, "a", "b")
			if tc.prep != nil {
				tc.prep(&as)
			}
			err := dispatchControlKind(&as, tc.c, false)
			var rej *OperationRejectedError
			if !errors.As(err, &rej) {
				t.Fatalf("err = %v, want *OperationRejectedError", err)
			}
			if rej.Code != tc.want || rej.Reason == "" {
				t.Fatalf("rejection = %+v, want code %s with a reason", rej, tc.want)
			}
		})
	}
}
