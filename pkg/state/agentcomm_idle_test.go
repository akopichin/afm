package state

import "testing"

// Agent Mesh Phase 5, T5.1: awaiting_peer is classified as ACTIVITY (a colleague working), never as a
// human-idle trigger by itself — mirroring awaiting_assistants' classification.

func TestIsIdle_AwaitingPeerAloneIsNotIdle(t *testing.T) {
	stages := map[string]StageState{
		"a": {Status: StatusAwaitingPeer},
	}
	if isIdle(rsWithStages(stages)) {
		t.Error("want idle=false when the only stage is awaiting_peer — a colleague working is machine activity, not a human-idle trigger")
	}
}

func TestIsIdle_AwaitingPeerDoesNotMaskAnotherStagesQuestion(t *testing.T) {
	stages := map[string]StageState{
		"a": {Status: StatusAwaitingPeer},
		"b": {Status: StatusAwaitingApproval},
	}
	if !isIdle(rsWithStages(stages)) {
		t.Error("want idle=true: a genuinely idle stage elsewhere must still be reported, regardless of awaiting_peer")
	}
}
