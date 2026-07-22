package main

import (
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/state"
)

// TestApproveErrRunLockedMessage verifies that `afm approve` surfaces a
// friendly, actionable message (not a raw lock error) when the run
// directory's flock is already held by another process (e.g. a live
// `afm run`), per the state.ErrRunLocked branch in approve.go.
func TestApproveErrRunLockedMessage(t *testing.T) {
	chdirTemp(t)

	runDir := makeRunState(t, "flow-20260101-120000", cmdInit, state.StatusAwaitingApproval)

	store, err := state.Open(runDir, []string{cmdInit})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	defer store.Close()

	cmd := newApproveCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{cmdInit})

	err = cmd.Execute()
	if err == nil {
		t.Fatal("expected error while run is locked by another process")
	}
	if !strings.Contains(err.Error(), "run is active") {
		t.Errorf("expected friendly ErrRunLocked message, got: %v", err)
	}
}

// TestRetryErrRunLockedMessage is the retry.go analogue of
// TestApproveErrRunLockedMessage above.
func TestRetryErrRunLockedMessage(t *testing.T) {
	chdirTemp(t)

	runDir := makeRunState(t, "flow-20260101-120000", cmdInit, state.StatusFailed)

	store, err := state.Open(runDir, []string{cmdInit})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	defer store.Close()

	cmd := newRetryCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{cmdInit})

	err = cmd.Execute()
	if err == nil {
		t.Fatal("expected error while run is locked by another process")
	}
	if !strings.Contains(err.Error(), "run is active") {
		t.Errorf("expected friendly ErrRunLocked message, got: %v", err)
	}
}

// TestReviseErrRunLockedMessage is the revise.go analogue of
// TestApproveErrRunLockedMessage above.
func TestReviseErrRunLockedMessage(t *testing.T) {
	chdirTemp(t)

	runDir := makeRunState(t, "flow-20260101-120000", cmdInit, state.StatusAwaitingApproval)

	store, err := state.Open(runDir, []string{cmdInit})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	defer store.Close()

	cmd := newReviseCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{flagFeedback, "fix it", cmdInit})

	err = cmd.Execute()
	if err == nil {
		t.Fatal("expected error while run is locked by another process")
	}
	if !strings.Contains(err.Error(), "run is active") {
		t.Errorf("expected friendly ErrRunLocked message, got: %v", err)
	}
}
