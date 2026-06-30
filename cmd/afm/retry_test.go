package main

import (
	"testing"

	"github.com/akopichin/afm/pkg/state"
)

func TestRetryFailedStage(t *testing.T) {
	chdirTemp(t)

	runDir := makeRunState(t, "flow-20260101-120000", cmdInit, state.StatusFailed)

	cmd := newRetryCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{cmdInit})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("retry: %v", err)
	}

	rs := loadRunState(t, runDir)
	if rs.Stages[cmdInit].Status != state.StatusPending {
		t.Errorf("expected pending, got: %v", rs.Stages[cmdInit].Status)
	}
}

func TestRetryNonFailedStage(t *testing.T) {
	chdirTemp(t)

	makeRunState(t, "flow-20260101-120000", cmdInit, state.StatusDone)

	cmd := newRetryCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{cmdInit})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for non-failed stage")
	}
}

func TestRetryNonexistentStage(t *testing.T) {
	chdirTemp(t)

	makeRunState(t, "flow-20260101-120000", cmdInit, state.StatusFailed)

	cmd := newRetryCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"nonexistent"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for nonexistent stage")
	}
}
