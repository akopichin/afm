package orchestrator_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/state"
)

// TestAutoApprove_WinsOverRequireApproval verifies that a stage with
// auto_approve: true is approved even when the whole run was started with
// --require-approval (which would normally FailStage a headless run with no
// dashboard attached, per the existing "Headless: нет дашборда" branch in
// onAgentCompleted).
func TestAutoApprove_WinsOverRequireApproval(t *testing.T) {
	stages := []flow.Stage{
		{ID: "ci-stage", Name: "CI Stage", Description: "auto approved in CI",
			Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}, AutoApprove: true},
	}

	runDir := t.TempDir()
	store, err := state.Open(runDir, []string{"ci-stage"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	stateFile := filepath.Join(runDir, "state.json")

	base := mockRunner(t, mockPlanningScript)
	runner := &doneCreatingRunner{delegate: base}

	orch := orchestrator.New(orchestrator.Options{
		RunDir:          runDir,
		Stages:          stages,
		Store:           store,
		Config:          config.Default(),
		Prompts:         orchestrator.DefaultPrompts(),
		Runner:          runner,
		RequireApproval: true,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := orch.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	final := loadStateJSON(t, stateFile)
	if final.Stages["ci-stage"].Status != state.StatusDone {
		t.Errorf("expected done (auto_approve overrides --require-approval), got %v", final.Stages["ci-stage"].Status)
	}
}

// TestAutoApprove_DefaultFalse_RequireApprovalStillFails is the regression
// guard: without auto_approve, --require-approval on a headless run must
// still fail the stage exactly as before this feature existed.
func TestAutoApprove_DefaultFalse_RequireApprovalStillFails(t *testing.T) {
	stages := []flow.Stage{
		{ID: "manual-stage", Name: "Manual Stage", Description: "needs a human",
			Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
	}

	runDir := t.TempDir()
	store, err := state.Open(runDir, []string{"manual-stage"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	stateFile := filepath.Join(runDir, "state.json")

	base := mockRunner(t, mockPlanningScript)
	runner := &doneCreatingRunner{delegate: base}

	orch := orchestrator.New(orchestrator.Options{
		RunDir:          runDir,
		Stages:          stages,
		Store:           store,
		Config:          config.Default(),
		Prompts:         orchestrator.DefaultPrompts(),
		Runner:          runner,
		RequireApproval: true,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := orch.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	final := loadStateJSON(t, stateFile)
	if final.Stages["manual-stage"].Status != state.StatusFailed {
		t.Errorf("expected failed (no auto_approve, --require-approval, no dashboard), got %v", final.Stages["manual-stage"].Status)
	}
}

// TestAutoApprove_WithDashboardAttached_SkipsManualClick verifies auto_approve
// fires even when a dashboard IS attached (SetDashboardURL), where the
// existing headless branch would NOT have auto-approved anything — proving
// auto_approve is independent of the headless/dashboard distinction. The
// sibling "manual" stage (no auto_approve, no simulated dashboard click) is
// used as a control: it must stay stuck at awaiting_approval forever in this
// test, since nothing here ever approves it.
func TestAutoApprove_WithDashboardAttached_SkipsManualClick(t *testing.T) {
	stages := []flow.Stage{
		{ID: "auto", Name: "Auto", Description: "auto approved",
			Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}, AutoApprove: true},
		{ID: "manual", Name: "Manual", Description: "needs a human click",
			Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
	}

	runDir := t.TempDir()
	store, err := state.Open(runDir, []string{"auto", "manual"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	stateFile := filepath.Join(runDir, "state.json")

	base := mockRunner(t, mockPlanningScript)
	runner := &doneCreatingRunner{delegate: base}

	orch := orchestrator.New(orchestrator.Options{
		RunDir:  runDir,
		Stages:  stages,
		Store:   store,
		Config:  config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
		Runner:  runner,
	})
	orch.SetDashboardURL("http://127.0.0.1:9999")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	runOrchestratorAsync(ctx, t, orch, cancel)

	waitForStatus(t, stateFile, "auto", state.StatusDone, 8*time.Second)

	final := loadStateJSON(t, stateFile)
	if final.Stages["manual"].Status != state.StatusAwaitingApproval {
		t.Errorf("expected manual stage stuck at awaiting_approval, got %v", final.Stages["manual"].Status)
	}
}

// TestAutoApprove_RecoveryDefaultCase_NoBusHelperNeeded exercises the
// recovery.go "default:" EvPlanReady site (crash with status=planning and
// plan.md already on disk). Deliberately does NOT register a bus-based
// auto-approver (unlike TestIntegration_ResumeFromPlanningWithExistingPlan) —
// if this test passes, the recovery-path auto_approve check itself did the
// approving, not a simulated dashboard click.
func TestAutoApprove_RecoveryDefaultCase_NoBusHelperNeeded(t *testing.T) {
	stages := []flow.Stage{
		{ID: "planned-ci", Name: "Planned CI", Description: "already planned",
			Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}, AutoApprove: true},
	}

	runDir := t.TempDir()
	stageDir := filepath.Join(runDir, "planned-ci")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# Existing Plan\n"), 0644); err != nil {
		t.Fatal(err)
	}

	store, err := state.Open(runDir, []string{"planned-ci"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	_ = store.Apply(&state.Transition{StageID: "planned-ci", From: state.StatusPending, To: state.StatusPlanning, Event: "test_setup"})
	stateFile := filepath.Join(runDir, "state.json")

	base := mockRunner(t, mockImplementationScript)
	runner := &doneCreatingRunner{delegate: base}

	orch := orchestrator.New(orchestrator.Options{
		RunDir:  runDir,
		Stages:  stages,
		Store:   store,
		Config:  config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
		Runner:  runner,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := orch.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	final := loadStateJSON(t, stateFile)
	if final.Stages["planned-ci"].Status != state.StatusDone {
		t.Errorf("expected done via recovery auto-approve, got %v", final.Stages["planned-ci"].Status)
	}
}

// TestAutoApprove_RecoveryRetryingWithExistingPlan_NoBusHelperNeeded exercises
// the recovery.go "case state.StatusRetrying:" EvPlanReady site (crash while
// Retrying, with plan.md already on disk). Same "no bus helper" proof as
// TestAutoApprove_RecoveryDefaultCase_NoBusHelperNeeded, targeting the sibling
// code path.
func TestAutoApprove_RecoveryRetryingWithExistingPlan_NoBusHelperNeeded(t *testing.T) {
	stages := []flow.Stage{
		{ID: "retry-ci", Name: "Retry CI", Description: "was retrying with a plan already on disk",
			Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}, AutoApprove: true},
	}

	runDir := t.TempDir()
	stageDir := filepath.Join(runDir, "retry-ci")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# Existing Plan\n"), 0644); err != nil {
		t.Fatal(err)
	}

	store, err := state.Open(runDir, []string{"retry-ci"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	_ = store.Apply(&state.Transition{StageID: "retry-ci", From: state.StatusPending, To: state.StatusRetrying, Event: "test_setup"})
	stateFile := filepath.Join(runDir, "state.json")

	base := mockRunner(t, mockImplementationScript)
	runner := &doneCreatingRunner{delegate: base}

	orch := orchestrator.New(orchestrator.Options{
		RunDir:  runDir,
		Stages:  stages,
		Store:   store,
		Config:  config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
		Runner:  runner,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := orch.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	final := loadStateJSON(t, stateFile)
	if final.Stages["retry-ci"].Status != state.StatusDone {
		t.Errorf("expected done via recovery auto-approve, got %v", final.Stages["retry-ci"].Status)
	}
}
