package orchestrator

import (
	"testing"

	"gitlab.ae-rus.net/bx/ai-flow-manager/pkg/state"
)

func TestFSM_ValidTransitions(t *testing.T) {
	cases := []struct {
		from state.StageStatus
		to   state.StageStatus
	}{
		{state.StatusPending, state.StatusPlanning},
		{state.StatusPlanning, state.StatusAwaitingApproval},
		{state.StatusPlanning, state.StatusFailed},
		{state.StatusAwaitingApproval, state.StatusReady},
		{state.StatusAwaitingApproval, state.StatusRevising},
		{state.StatusRevising, state.StatusPlanning},
		{state.StatusReady, state.StatusRunning},
		{state.StatusRunning, state.StatusDone},
		{state.StatusRunning, state.StatusFailed},
		{state.StatusRunning, state.StatusRetrying},
		{state.StatusPlanning, state.StatusRetrying},
		{state.StatusRetrying, state.StatusRunning},
		{state.StatusRetrying, state.StatusPlanning},
		{state.StatusRetrying, state.StatusFailed},
		{state.StatusRetrying, state.StatusDone},
		{state.StatusRetrying, state.StatusAwaitingApproval},
		{state.StatusPending, state.StatusReady},
		{state.StatusPending, state.StatusFailed},
		{state.StatusFailed, state.StatusPending},
	}
	for _, c := range cases {
		if !ValidTransition(c.from, c.to) {
			t.Errorf("expected valid: %s -> %s", c.from, c.to)
		}
	}
}

func TestFSM_InvalidTransitions(t *testing.T) {
	cases := []struct {
		from state.StageStatus
		to   state.StageStatus
	}{
		{state.StatusPending, state.StatusDone},
		{state.StatusDone, state.StatusRunning},
		{state.StatusReady, state.StatusPlanning},
		{state.StatusRunning, state.StatusReady},
		{state.StatusAwaitingApproval, state.StatusRunning},
		{state.StatusFailed, state.StatusRunning},
	}
	for _, c := range cases {
		if ValidTransition(c.from, c.to) {
			t.Errorf("expected invalid: %s -> %s", c.from, c.to)
		}
	}
}

func TestFSM_IsTerminal(t *testing.T) {
	if !IsTerminal(state.StatusDone) {
		t.Error("done should be terminal")
	}
	if !IsTerminal(state.StatusFailed) {
		t.Error("failed should be terminal")
	}
	if IsTerminal(state.StatusRunning) {
		t.Error("running should not be terminal")
	}
	if IsTerminal(state.StatusRetrying) {
		t.Error("retrying should not be terminal")
	}
}
