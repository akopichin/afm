package state

import (
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
)

// An operation's result names exactly the waits its own commit made ready: two operations never both claim
// one, and a wait already ready before the commit is not reported again.
func TestApplyRunOperation_ReportsTheWaitsItMadeReady(t *testing.T) {
	store, err := Open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	mustOp := func(id string, controls ...*Control) OperationResult {
		t.Helper()
		var effects []Effect
		for _, c := range controls {
			effects = append(effects, Effect{Kind: c.Kind, Target: c.Target, Payload: c.Payload})
		}
		res, err := store.ApplyRunOperation(Operation{ID: id, Kind: "test", Effects: effects}, nil)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		return res
	}
	mustOp("setup",
		meshParticipantAddedControl("a", string(agentcomm.KindStageAgent), string(agentcomm.ScopeStage)),
		meshParticipantAddedControl("b", string(agentcomm.KindStageAgent), string(agentcomm.ScopeStage)),
		meshThreadOpenedControl("th-1", []string{"a", "b"}),
		meshMessageSentControl(0, "q", "th-1", "a", []string{"b"}, string(agentcomm.MessageKindRequest), nil, "question"),
		meshWaitRegisteredControl("w", "a", agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: "q"}, "", "", 0),
	)
	first := mustOp("reply-1", meshMessageSentControl(0, "r1", "th-1", "b", []string{"a"}, string(agentcomm.MessageKindReply), strp("q"), "one"))
	if len(first.ReadyWaits) != 1 || first.ReadyWaits[0] != "w" {
		t.Fatalf("first reply's ReadyWaits = %v, want [w]", first.ReadyWaits)
	}
	second := mustOp("reply-2", meshMessageSentControl(0, "r2", "th-1", "b", []string{"a"}, string(agentcomm.MessageKindReply), strp("q"), "two"))
	if len(second.ReadyWaits) != 0 {
		t.Fatalf("second reply's ReadyWaits = %v, want none: the wait was already ready", second.ReadyWaits)
	}
	if again := mustOp("reply-1", meshMessageSentControl(0, "r1", "th-1", "b", []string{"a"}, string(agentcomm.MessageKindReply), strp("q"), "one")); len(again.ReadyWaits) != 0 {
		t.Fatalf("an idempotent repeat claims waits again: %v", again.ReadyWaits)
	}
}
