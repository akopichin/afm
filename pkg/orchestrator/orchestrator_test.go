package orchestrator_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/state"
)

func TestPlanningPhaseMarksPlanningStatus(t *testing.T) {
	runDir := t.TempDir()
	stages := []flow.Stage{
		{
			ID: "s1", Name: "Stage 1", Description: "do something",
			Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation},
		},
	}

	rs := state.NewRunState([]string{"s1"})
	statePath := filepath.Join(runDir, "state.json")
	if err := rs.Save(statePath); err != nil {
		t.Fatal(err)
	}

	runner := &doneCreatingRunner{delegate: executor.New(executor.Config{
		Command: bashCommand,
		ExtraArgs: []string{"-c",
			`echo '{"type":"assistant","message":{"content":[{"type":"text","text":"# Plan\n- step 1"}]}}'
echo '{"type":"result","subtype":"success"}'`},
		IdleTimeout: 10 * time.Second,
	})}

	cfg := config.Default()

	orch := orchestrator.New(orchestrator.Options{
		RunDir:    runDir,
		Stages:    stages,
		State:     rs,
		StateFile: statePath,
		Config:    cfg,
		Prompts:   orchestrator.DefaultPrompts(),
		Runner:    runner,
	})

	// Subscribe to auto-approve
	go func() {
		events := orch.Bus().Subscribe()
		defer orch.Bus().Unsubscribe(events)
		for ev := range events {
			if ev.Type == orchestrator.EventStageStatusChanged {
				status, _ := ev.Data.(string)
				if status == string(state.StatusAwaitingApproval) {
					orch.Approve(ev.StageID)
				}
			}
		}
	}()

	if err := orch.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	loaded, _ := state.Load(statePath)
	if loaded.Stages["s1"].Status != state.StatusDone {
		t.Errorf("stage should be done after run: got %v",
			loaded.Stages["s1"].Status)
	}

	planPath := filepath.Join(runDir, "s1", "plan.md")
	if _, err := os.Stat(planPath); err != nil {
		t.Errorf("plan.md should exist: %v", err)
	}
}

func TestCollectDependencyPlans(t *testing.T) {
	runDir := t.TempDir()

	// Create a plan for the dependency stage
	depDir := filepath.Join(runDir, "backend")
	if err := os.MkdirAll(depDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(depDir, "plan.md"), []byte("# Backend Plan\n\nDo stuff"), 0644); err != nil {
		t.Fatal(err)
	}

	stages := []flow.Stage{
		{ID: "backend", Name: "Backend API", Description: "backend"},
		{ID: "frontend", Name: "Frontend", Description: "frontend", DependsOn: []string{"backend"}},
	}

	result := orchestrator.CollectDependencyPlans(runDir, stages[1], stages)
	if result == "" {
		t.Fatal("expected non-empty dependency plans")
	}
	if !strings.Contains(result, "Backend API") {
		t.Error("should contain dependency stage name")
	}
	if !strings.Contains(result, "# Backend Plan") {
		t.Error("should contain dependency plan content")
	}
}

func TestCollectArtifacts_Inline(t *testing.T) {
	projectDir := t.TempDir()

	// Create artifact file in project dir
	if err := os.MkdirAll(filepath.Join(projectDir, "docs"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "docs/api.yaml"), []byte("openapi: 3.0.0"), 0644); err != nil {
		t.Fatal(err)
	}

	allStages := []flow.Stage{
		{
			ID: "backend", Name: "Backend",
			Artifacts: []flow.Artifact{
				{Name: "api-contract", Path: "docs/api.yaml", Description: "OpenAPI schema"},
			},
		},
		{
			ID: "frontend", Name: "Frontend",
			DependsOn: []string{"backend"},
			Inputs:    []flow.Input{{Ref: "backend.api-contract"}},
		},
	}

	result, err := orchestrator.CollectArtifacts(projectDir, "", allStages[1], allStages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "api-contract") {
		t.Error("should contain artifact name")
	}
	if !strings.Contains(result, "openapi: 3.0.0") {
		t.Error("should contain inlined artifact content")
	}
}

func TestCollectArtifacts_NonInline(t *testing.T) {
	runDir := t.TempDir()

	// Create artifact in stage dir (path starts with ./)
	stageDir := filepath.Join(runDir, "backend")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "schema.sql"), []byte("CREATE TABLE t(id int)"), 0644); err != nil {
		t.Fatal(err)
	}

	inlineFalse := false
	allStages := []flow.Stage{
		{
			ID: "backend", Name: "Backend",
			Artifacts: []flow.Artifact{
				{Name: "db-schema", Path: "./schema.sql", Description: "SQL migration", Inline: &inlineFalse},
			},
		},
		{
			ID: "frontend", Name: "Frontend",
			DependsOn: []string{"backend"},
			Inputs:    []flow.Input{{Ref: "backend.db-schema"}},
		},
	}

	result, err := orchestrator.CollectArtifacts("", runDir, allStages[1], allStages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "File path:") {
		t.Error("non-inline artifact should contain file path reference")
	}
	if strings.Contains(result, "CREATE TABLE") {
		t.Error("non-inline artifact should NOT contain file content")
	}
}

func TestCollectArtifacts_MissingRequired(t *testing.T) {
	allStages := []flow.Stage{
		{
			ID: "backend", Name: "Backend",
			Artifacts: []flow.Artifact{
				{Name: "missing", Path: "nonexistent.txt", Description: "gone"},
			},
		},
		{
			ID: "frontend", Name: "Frontend",
			DependsOn: []string{"backend"},
			Inputs:    []flow.Input{{Ref: "backend.missing"}},
		},
	}

	_, err := orchestrator.CollectArtifacts("/tmp", "", allStages[1], allStages)
	if err == nil {
		t.Fatal("expected error for missing required artifact")
	}
}

func TestCollectArtifacts_MissingOptional(t *testing.T) {
	allStages := []flow.Stage{
		{
			ID: "backend", Name: "Backend",
			Artifacts: []flow.Artifact{
				{Name: "missing", Path: "nonexistent.txt", Description: "gone"},
			},
		},
		{
			ID: "frontend", Name: "Frontend",
			DependsOn: []string{"backend"},
			Inputs:    []flow.Input{{Ref: "backend.missing", Optional: true}},
		},
	}

	result, err := orchestrator.CollectArtifacts("/tmp", "", allStages[1], allStages)
	if err != nil {
		t.Fatalf("optional artifact should not cause error: %v", err)
	}
	if result != "" {
		t.Errorf("expected empty result for missing optional artifact, got: %q", result)
	}
}

func TestResumeInteractiveAgent_PlanningPhase(t *testing.T) {
	dir := t.TempDir()
	stageDir := filepath.Join(dir, "s1")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "planning.session.json"), []byte(`{"session_id":"x"}`), 0644); err != nil {
		t.Fatal(err)
	}

	stages := []flow.Stage{
		{ID: "s1", Name: "Stage 1", Description: "test resume", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
	}

	rs := state.NewRunState([]string{"s1"})
	rs.SetStageStatus("s1", state.StatusAwaitingUserInput)
	stateFile := filepath.Join(dir, "state.json")
	if err := rs.Save(stateFile); err != nil {
		t.Fatal(err)
	}

	base := executor.New(executor.Config{
		Command:     bashCommand,
		ExtraArgs:   []string{"-c", mockPlanningScript},
		IdleTimeout: 10 * time.Second,
	})
	runner := &doneCreatingRunner{delegate: base}

	cfg := config.Default()

	orch := orchestrator.New(orchestrator.Options{
		RunDir:    dir,
		Stages:    stages,
		State:     rs,
		StateFile: stateFile,
		Config:    cfg,
		Prompts:   orchestrator.DefaultPrompts(),
		Runner:    runner,
	})

	cancel := autoApprove(orch)
	defer cancel()

	ctx, ctxCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer ctxCancel()

	if err := orch.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	final, _ := state.Load(stateFile)
	if final.Stages["s1"].Status != state.StatusDone {
		t.Errorf("expected done, got %v", final.Stages["s1"].Status)
	}
}

func TestResumeInteractiveAgent_ImplementationPhase(t *testing.T) {
	dir := t.TempDir()
	stageDir := filepath.Join(dir, "s1")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Implementation session — detected as the interrupted phase
	if err := os.WriteFile(filepath.Join(stageDir, "implementation.session.json"), []byte(`{"session_id":"y"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# Plan\n\n- step 1\n"), 0644); err != nil {
		t.Fatal(err)
	}

	stages := []flow.Stage{
		{ID: "s1", Name: "Stage 1", Description: "test resume", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
	}

	rs := state.NewRunState([]string{"s1"})
	rs.SetStageStatus("s1", state.StatusAwaitingUserInput)
	stateFile := filepath.Join(dir, "state.json")
	if err := rs.Save(stateFile); err != nil {
		t.Fatal(err)
	}

	base := executor.New(executor.Config{
		Command:     bashCommand,
		ExtraArgs:   []string{"-c", mockImplementationScript},
		IdleTimeout: 10 * time.Second,
	})
	runner := &doneCreatingRunner{delegate: base}

	cfg := config.Default()

	orch := orchestrator.New(orchestrator.Options{
		RunDir:    dir,
		Stages:    stages,
		State:     rs,
		StateFile: stateFile,
		Config:    cfg,
		Prompts:   orchestrator.DefaultPrompts(),
		Runner:    runner,
	})

	ctx, ctxCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer ctxCancel()

	if err := orch.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	final, _ := state.Load(stateFile)
	if final.Stages["s1"].Status != state.StatusDone {
		t.Errorf("expected done, got %v", final.Stages["s1"].Status)
	}
}
