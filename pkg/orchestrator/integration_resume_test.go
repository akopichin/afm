package orchestrator_test

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/mcp"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/state"
)

// TestIntegration_ResumeWithDoneFile verifies that on resume, a stage in "running"
// with an existing .done file transitions to "done" without restarting the agent.
func TestIntegration_ResumeWithDoneFile(t *testing.T) {
	stages := []flow.Stage{
		{ID: "s1", Name: "Stage 1", Description: "already done", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
	}

	runDir := t.TempDir()
	stageDir := filepath.Join(runDir, "s1")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# Plan"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, ".done"), []byte("completed work summary"), 0644); err != nil {
		t.Fatal(err)
	}

	store, err := state.Open(runDir, []string{"s1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	_ = store.Apply(state.Transition{StageID: "s1", From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"})
	stateFile := filepath.Join(runDir, "state.json")

	// Use a failing runner — if the agent runs, the test should fail
	runner := mockRunner(t, mockFailScript)

	cfg := config.Default()
	orch := orchestrator.New(orchestrator.Options{
		RunDir:  runDir,
		Stages:  stages,
		Store:   store,
		Config:  cfg,
		Prompts: orchestrator.DefaultPrompts(),
		Runner:  runner,
	})

	if err := orch.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	final := loadStateJSON(t, stateFile)
	if final.Stages["s1"].Status != state.StatusDone {
		t.Errorf("expected done (from .done file), got %v", final.Stages["s1"].Status)
	}
}

// TestIntegration_ResumeFromRetrying verifies that a stage stuck in "retrying"
// status (process killed during backoff) is properly restarted on resume.
func TestIntegration_ResumeFromRetrying(t *testing.T) {
	stages := []flow.Stage{
		{ID: "retry-stuck", Name: "Retry Stuck", Description: "was retrying", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
	}

	runDir := t.TempDir()
	stageDir := filepath.Join(runDir, "retry-stuck")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}

	store, err := state.Open(runDir, []string{"retry-stuck"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	_ = store.Apply(state.Transition{StageID: "retry-stuck", From: state.StatusPending, To: state.StatusRetrying, Event: "test_setup"})
	stateFile := filepath.Join(runDir, "state.json")

	base := mockRunner(t, mockPlanningScript)
	runner := &doneCreatingRunner{delegate: base}

	cfg := config.Default()
	orch := orchestrator.New(orchestrator.Options{
		RunDir:  runDir,
		Stages:  stages,
		Store:   store,
		Config:  cfg,
		Prompts: orchestrator.DefaultPrompts(),
		Runner:  runner,
	})

	cancel := autoApprove(orch)
	defer cancel()

	ctx, ctxCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer ctxCancel()

	if err := orch.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	final := loadStateJSON(t, stateFile)
	if final.Stages["retry-stuck"].Status != state.StatusDone {
		t.Errorf("expected done after resume from retrying, got %v", final.Stages["retry-stuck"].Status)
	}
}

// TestIntegration_ResumeFromPlanningWithExistingPlan verifies that if planning
// was completed (plan.md exists) but the orchestrator crashed before transitioning
// to awaiting_approval, the stage resumes correctly without re-planning.
func TestIntegration_ResumeFromPlanningWithExistingPlan(t *testing.T) {
	stages := []flow.Stage{
		{ID: "planned", Name: "Planned", Description: "already planned", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
	}

	runDir := t.TempDir()
	stageDir := filepath.Join(runDir, "planned")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# Existing Plan\n\nStep 1\n"), 0644); err != nil {
		t.Fatal(err)
	}

	store, err := state.Open(runDir, []string{"planned"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	_ = store.Apply(state.Transition{StageID: "planned", From: state.StatusPending, To: state.StatusPlanning, Event: "test_setup"})
	stateFile := filepath.Join(runDir, "state.json")

	// Use a failing runner for planning — if planning re-runs, the test fails
	base := mockRunner(t, mockPlanningScript)
	runner := &doneCreatingRunner{delegate: base}

	cfg := config.Default()
	orch := orchestrator.New(orchestrator.Options{
		RunDir:  runDir,
		Stages:  stages,
		Store:   store,
		Config:  cfg,
		Prompts: orchestrator.DefaultPrompts(),
		Runner:  runner,
	})

	cancel := autoApprove(orch)
	defer cancel()

	ctx, ctxCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer ctxCancel()

	if err := orch.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	final := loadStateJSON(t, stateFile)
	if final.Stages["planned"].Status != state.StatusDone {
		t.Errorf("expected done, got %v", final.Stages["planned"].Status)
	}

	// Verify the original plan was preserved (not overwritten by re-planning)
	data, _ := os.ReadFile(filepath.Join(stageDir, "plan.md"))
	if !strings.Contains(string(data), "Existing Plan") {
		t.Error("plan.md was overwritten by re-planning, expected 'Existing Plan' content")
	}
}

// TestResumeAfterCrash verifies that when flowManager crashes while a stage
// is in awaiting_user_input, and the user's answer is already persisted in
// dialog.jsonl, the orchestrator on restart detects the status, resumes the
// interactive agent, which replays the sealed Q/A pair via MCP, and the stage
// reaches done.
func TestResumeAfterCrash(t *testing.T) {
	dir := t.TempDir()
	stageDir := filepath.Join(dir, "discovery")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# plan"), 0644); err != nil {
		t.Fatal(err)
	}

	// Pre-populate: agent asked q1 and user answered before crash
	dialogPath := filepath.Join(stageDir, "implementation.dialog.jsonl")
	if err := mcp.AppendQuestion(dialogPath, mcp.Question{ID: "q1", Question: "x?"}); err != nil {
		t.Fatal(err)
	}
	if err := mcp.AppendAnswer(dialogPath, mcp.Answer{ID: "q1", Answer: "after restart"}); err != nil {
		t.Fatal(err)
	}

	// Pre-populate: session.json exists (simulating prior run)
	if err := os.WriteFile(filepath.Join(stageDir, "implementation.session.json"),
		[]byte(`{"session_id":"test-uuid-resume"}`), 0644); err != nil {
		t.Fatal(err)
	}

	// Agent script: calls MCP ask_user (gets replay answer), creates .done, exits
	agentScript := filepath.Join(dir, "mock-resume-agent.sh")
	script := "#!/bin/bash\n" +
		"MCP=\"\"\n" +
		"while [ $# -gt 0 ]; do\n" +
		"  case \"$1\" in\n" +
		"    --mcp-config) MCP=\"$2\"; shift 2;;\n" +
		"    --session-id|--resume) shift 2;;\n" +
		"    *) shift;;\n" +
		"  esac\n" +
		"done\n" +
		"if [ -z \"$MCP\" ]; then echo 'no mcp config' >&2; exit 1; fi\n" +
		"URL=$(python3 -c \"import json,sys;d=json.load(open(sys.argv[1]));print(d['mcpServers']['flowmanager']['url'])\" \"$MCP\" 2>/dev/null)\n" +
		"if [ -z \"$URL\" ]; then URL=$(grep -o '\"url\": *\"[^\"]*\"' \"$MCP\" | head -1 | sed 's/.*\"url\":[[:space:]]*\"[[:space:]]*//' | sed 's/\".*//'); fi\n" +
		"STAGE_DIR=$(dirname \"$MCP\")\n" +
		"curl -sf -X POST \"$URL\" -H 'Content-Type: application/json' -d '{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{}}' >/dev/null\n" +
		"curl -sf -X POST \"$URL\" -H 'Content-Type: application/json' -d '{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"ask_user\",\"arguments\":{\"id\":\"q1\",\"question\":\"x?\"}}}' >/dev/null\n" +
		"echo 'done' > \"$STAGE_DIR/.done\"\n" +
		"echo '{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"resumed\"}]}}'\n" +
		"echo '{\"type\":\"result\",\"subtype\":\"success\"}'\n"
	if err := os.WriteFile(agentScript, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	stages := []flow.Stage{
		{
			ID: "discovery", Name: "Discovery", Description: "ask user",
			Agents:      []flow.AgentType{flow.AgentImplementation},
			Interactive: true,
			Command:     agentScript,
		},
	}

	store, err := state.Open(dir, []string{"discovery"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	_ = store.Apply(state.Transition{StageID: "discovery", From: state.StatusPending, To: state.StatusAwaitingUserInput, Event: "test_setup"})
	stateFile := filepath.Join(dir, "state.json")

	cfg := config.Default()
	orch := orchestrator.New(orchestrator.Options{
		RunDir:  dir,
		Stages:  stages,
		Store:   store,
		Config:  cfg,
		Prompts: orchestrator.DefaultPrompts(),
	})

	mcpSrv := mcp.NewServer(dir, orchestrator.NewMcpNotifier(orch))
	srv := httptest.NewServer(mcpSrv)
	defer srv.Close()
	orch.SetDashboardURL(srv.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	go func() { _ = orch.Run(ctx) }()

	// Stage should go from awaiting_user_input → running → done via resume
	waitForStatus(t, stateFile, "discovery", state.StatusDone, 10*time.Second)

	// Verify dialog was preserved
	entries, err := mcp.ReadDialog(dialogPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 dialog entry, got %d", len(entries))
	}
	if entries[0].Answer == nil || *entries[0].Answer != "after restart" {
		t.Errorf("answer mismatch: %+v", entries[0])
	}
}
