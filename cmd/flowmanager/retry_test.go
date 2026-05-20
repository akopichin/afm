package main

import (
	"path/filepath"
	"testing"

	"gitlab.ae-rus.net/bx/ai-flow-manager/pkg/state"
)

func TestRetryFailedStage(t *testing.T) {
	chdirTemp(t)

	runDir := makeRunState(t, "flow-20260101-120000", "init", state.StatusFailed)

	cmd := newRetryCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"init"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("retry: %v", err)
	}

	sf := filepath.Join(runDir, "state.json")
	rs, err := state.Load(sf)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if rs.Stages["init"].Status != state.StatusPending {
		t.Errorf("expected pending, got: %v", rs.Stages["init"].Status)
	}
}

func TestRetryNonFailedStage(t *testing.T) {
	chdirTemp(t)

	makeRunState(t, "flow-20260101-120000", "init", state.StatusDone)

	cmd := newRetryCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"init"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for non-failed stage")
	}
}

func TestRetryNonexistentStage(t *testing.T) {
	chdirTemp(t)

	makeRunState(t, "flow-20260101-120000", "init", state.StatusFailed)

	cmd := newRetryCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"nonexistent"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for nonexistent stage")
	}
}
