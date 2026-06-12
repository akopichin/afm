package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/state"
)

const flagFeedback = "--feedback"

// chdirTemp switches the working directory to a temp dir and restores it after the test.
func chdirTemp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(orig) }) //nolint:errcheck
	return dir
}

// makeRunState creates .flowManager/runs/{runName}/state.json with the given stage.
func makeRunState(t *testing.T, runName, stageID string, status state.StageStatus) string {
	t.Helper()
	runDir := filepath.Join(".flowManager", "runs", runName)
	if err := os.MkdirAll(runDir, 0755); err != nil {
		t.Fatal(err)
	}
	rs := state.NewRunState([]string{stageID})
	rs.FlowName = runName
	rs.SetStageStatus(stageID, status)
	sf := filepath.Join(runDir, "state.json")
	data, err := json.MarshalIndent(rs, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sf, data, 0644); err != nil {
		t.Fatal(err)
	}
	return runDir
}

// loadRunState reads state.json from a run directory.
func loadRunState(t *testing.T, runDir string) *state.RunState {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(runDir, "state.json"))
	if err != nil {
		t.Fatalf("read state.json: %v", err)
	}
	var rs state.RunState
	if err := json.Unmarshal(data, &rs); err != nil {
		t.Fatalf("unmarshal state: %v", err)
	}
	return &rs
}

// TestReviseSavesFeedback verifies that the revise command saves feedback
// to the stage directory and sets the status to revising.
func TestReviseSavesFeedback(t *testing.T) {
	chdirTemp(t)

	runDir := makeRunState(t, "flow-20260101-120000", cmdInit, state.StatusAwaitingApproval)

	// Create stage directory with a plan
	stageDir := filepath.Join(runDir, cmdInit)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# Plan\n\nstep 1"), 0644); err != nil {
		t.Fatal(err)
	}

	cmd := newReviseCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{flagFeedback, "add rollback section", cmdInit})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("revise: %v", err)
	}

	// Check feedback file was created
	data, err := os.ReadFile(filepath.Join(stageDir, "feedback.md"))
	if err != nil {
		t.Fatalf("read feedback: %v", err)
	}
	if !strings.Contains(string(data), "add rollback section") {
		t.Errorf("feedback file should contain feedback text, got: %s", string(data))
	}

	// Check status changed to revising
	rs := loadRunState(t, runDir)
	if rs.Stages[cmdInit].Status != state.StatusRevising {
		t.Errorf("expected status revising, got: %v", rs.Stages[cmdInit].Status)
	}
}

// TestReviseRequiresAwaitingApproval verifies that revise fails
// if the stage is not in awaiting_approval status.
func TestReviseRequiresAwaitingApproval(t *testing.T) {
	chdirTemp(t)

	makeRunState(t, "flow-20260101-120000", cmdInit, state.StatusDone)

	cmd := newReviseCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{flagFeedback, "fix it", cmdInit})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for non-awaiting_approval stage")
	}
	if !strings.Contains(err.Error(), "not awaiting_approval") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestReviseEmptyFeedbackReturnsError verifies that revise fails
// when no feedback is provided.
func TestReviseEmptyFeedbackReturnsError(t *testing.T) {
	chdirTemp(t)

	makeRunState(t, "flow-20260101-120000", cmdInit, state.StatusAwaitingApproval)

	cmd := newReviseCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{cmdInit})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for empty feedback")
	}
	if !strings.Contains(err.Error(), "feedback is required") {
		t.Errorf("unexpected error: %v", err)
	}
}
