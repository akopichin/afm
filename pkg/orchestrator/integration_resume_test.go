package orchestrator_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
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
	_ = store.Apply(&state.Transition{StageID: "s1", From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"})
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
	_ = store.Apply(&state.Transition{StageID: "retry-stuck", From: state.StatusPending, To: state.StatusRetrying, Event: "test_setup"})
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
	_ = store.Apply(&state.Transition{StageID: "planned", From: state.StatusPending, To: state.StatusPlanning, Event: "test_setup"})
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

// capturingPlanningRunner wraps a Runner and records every prompt passed to
// RunPlanning — used to verify that runPlanningWithFeedback (recovery.go,
// StatusRevising branch) actually reads feedback.md and forwards its content
// into the prompt, as opposed to a "fresh" planning restart which would not
// carry any feedback text.
type capturingPlanningRunner struct {
	delegate executor.Runner
	mu       sync.Mutex
	prompts  []string
}

func (r *capturingPlanningRunner) RunPlanning(ctx context.Context, stageName, prompt, outFile, logFile string) error {
	r.mu.Lock()
	r.prompts = append(r.prompts, prompt)
	r.mu.Unlock()
	return r.delegate.RunPlanning(ctx, stageName, prompt, outFile, logFile)
}

func (r *capturingPlanningRunner) RunAgent(ctx context.Context, agentType, stageName, prompt, logFile string) error {
	return r.delegate.RunAgent(ctx, agentType, stageName, prompt, logFile)
}

func (r *capturingPlanningRunner) RunJSONQuery(ctx context.Context, prompt string) ([]byte, error) {
	return r.delegate.RunJSONQuery(ctx, prompt)
}

// TestIntegration_ResumeFromRevising verifies that a stage stuck in "revising"
// status (process killed while the agent was re-planning after review
// feedback) resumes via runPlanningWithFeedback (recovery.go, StatusRevising
// branch) rather than restarting planning from scratch: the pre-existing
// feedback.md must be read and its content forwarded into the new planning
// prompt, and the stage must progress past awaiting_approval (auto-approved)
// all the way to done.
func TestIntegration_ResumeFromRevising(t *testing.T) {
	stages := []flow.Stage{
		{ID: "revise-stuck", Name: "Revise Stuck", Description: "was revising", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
	}

	runDir := t.TempDir()
	stageDir := filepath.Join(runDir, "revise-stuck")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Seed the artifacts a real Revise leaves behind: a previously rejected
	// plan version and the reviewer's feedback.
	if err := os.WriteFile(filepath.Join(stageDir, "plan.v1.md"), []byte("# Plan v1\n\nold content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "feedback.md"), []byte("please add error handling for edge case X"), 0644); err != nil {
		t.Fatal(err)
	}

	store, err := state.Open(runDir, []string{"revise-stuck"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	_ = store.Apply(&state.Transition{StageID: "revise-stuck", From: state.StatusPending, To: state.StatusRevising, Event: "test_setup"})
	stateFile := filepath.Join(runDir, "state.json")

	capture := &capturingPlanningRunner{delegate: mockRunner(t, mockPlanningScript)}
	runner := &doneCreatingRunner{delegate: capture}

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
	if final.Stages["revise-stuck"].Status != state.StatusDone {
		t.Errorf("expected done after resume from revising, got %v", final.Stages["revise-stuck"].Status)
	}

	capture.mu.Lock()
	prompts := append([]string{}, capture.prompts...)
	capture.mu.Unlock()
	if len(prompts) == 0 {
		t.Fatal("expected at least one RunPlanning call via runPlanningWithFeedback")
	}
	if !strings.Contains(prompts[0], "please add error handling for edge case X") {
		t.Errorf("expected planning prompt to include feedback.md content (proves runPlanningWithFeedback resume path), got: %s", prompts[0])
	}
}

// TestResumeAfterCrash verifies that when afm crashes while a stage is
// in awaiting_user_input, and both question.json and answer.json already exist
// on disk, the orchestrator on restart resumes the interactive agent, which
// reads the pre-existing answer.json via its bash loop, and the stage reaches done.
func TestResumeAfterCrash(t *testing.T) {
	dir := t.TempDir()
	stageDir := filepath.Join(dir, "discovery")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# plan"), 0644); err != nil {
		t.Fatal(err)
	}

	// Pre-populate: agent had already asked q1 and user answered before crash.
	qPath := filepath.Join(stageDir, "implementation.q1.question.json")
	if err := os.WriteFile(qPath, []byte(`{"id":"q1","question":"x?"}`), 0644); err != nil {
		t.Fatal(err)
	}
	aPath := filepath.Join(stageDir, "implementation.q1.answer.json")
	if err := os.WriteFile(aPath, []byte(`{"id":"q1","answer":"after restart","from_options":false}`), 0644); err != nil {
		t.Fatal(err)
	}
	// Also populate dialog.jsonl for history.
	dialogPath := filepath.Join(stageDir, "implementation.dialog.jsonl")
	if err := mcp.AppendQuestion(dialogPath, mcp.Question{ID: "q1", Question: "x?"}); err != nil {
		t.Fatal(err)
	}
	if err := mcp.AppendAnswer(dialogPath, mcp.Answer{ID: "q1", Answer: "after restart"}); err != nil {
		t.Fatal(err)
	}
	// Pre-populate: session.json to simulate prior run.
	if err := os.WriteFile(filepath.Join(stageDir, "implementation.session.json"),
		[]byte(`{"session_id":"test-uuid-resume"}`), 0644); err != nil {
		t.Fatal(err)
	}

	// Agent script: checks AFM_STAGE_DIR, reads answer.json (already exists),
	// creates .done, exits.
	agentScript := filepath.Join(dir, "mock-resume-agent.sh")
	script := "#!/bin/bash\n" +
		"STAGE_DIR=\"$AFM_STAGE_DIR\"\n" +
		"if [ -z \"$STAGE_DIR\" ]; then echo 'no AFM_STAGE_DIR' >&2; exit 1; fi\n" +
		"# answer.json should already exist from before the crash\n" +
		"if [ ! -f \"$STAGE_DIR/implementation.q1.answer.json\" ]; then echo 'answer missing' >&2; exit 1; fi\n" +
		"echo 'done' > \"$STAGE_DIR/.done\"\n" +
		"echo '{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"resumed\"}]}}'\n" +
		"echo '{\"type\":\"result\",\"subtype\":\"success\"}'\n"
	if err := os.WriteFile(agentScript, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	stages := []flow.Stage{{
		ID: "discovery", Name: "Discovery", Description: "ask user",
		Agents:      []flow.AgentType{flow.AgentImplementation},
		Interactive: true,
		Command:     agentScript,
	}}

	store, err := state.Open(dir, []string{"discovery"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	_ = store.Apply(&state.Transition{StageID: "discovery", From: state.StatusPending, To: state.StatusAwaitingUserInput, Event: "test_setup"})
	stateFile := filepath.Join(dir, "state.json")

	cfg := config.Default()
	orch := orchestrator.New(orchestrator.Options{
		RunDir:  dir,
		Stages:  stages,
		Store:   store,
		Config:  cfg,
		Prompts: orchestrator.DefaultPrompts(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	go func() { _ = orch.Run(ctx) }()

	// Stage goes from awaiting_user_input → running → done via file-based resume.
	waitForStatus(t, stateFile, "discovery", state.StatusDone, 10*time.Second)

	// Verify dialog was preserved.
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
