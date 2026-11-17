package state

import (
	"encoding/json"
	"testing"
)

func TestPreCompletionSnapshot(t *testing.T) {
	for _, tc := range []struct {
		decision PreCompletionDecision
		idle     bool
	}{
		{PreCompletionReviewing, false}, {PreCompletionCorrect, false}, {PreCompletionNeedsDecision, true},
	} {
		rs := RunState{Stages: map[string]StageState{"build": {Status: StatusAwaitingPreCompletion, PreCompletion: &PreCompletionRound{ID: "round-2", Decision: tc.decision, CorrectionCount: 1}}}}
		b, err := json.Marshal(rs)
		if err != nil {
			t.Fatal(err)
		}
		var snapshot RunState
		if err := json.Unmarshal(b, &snapshot); err != nil {
			t.Fatal(err)
		}
		round := snapshot.Stages["build"].PreCompletion
		if round == nil || round.ID != "round-2" || round.CorrectionCount != 1 || round.Decision != tc.decision || isIdle(&snapshot) != tc.idle {
			t.Fatalf("unexpected snapshot for %s: %s", tc.decision, b)
		}
	}
}
