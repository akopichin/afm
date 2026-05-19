package orchestrator_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gitlab.ae-rus.net/bx/ai-flow-manager/pkg/config"
	"gitlab.ae-rus.net/bx/ai-flow-manager/pkg/executor"
	"gitlab.ae-rus.net/bx/ai-flow-manager/pkg/flow"
	"gitlab.ae-rus.net/bx/ai-flow-manager/pkg/orchestrator"
	"gitlab.ae-rus.net/bx/ai-flow-manager/pkg/state"
)

const bashCommand = "bash"

// bash scripts for mocking the AI client (simulate claude stream-json protocol)

const mockPlanningScript = `echo '{"type":"assistant","message":{"content":[{"type":"text","text":"# Plan\n\n- [ ] Step 1: implement feature\n- [ ] Step 2: write tests\n"}]}}'
echo '{"type":"result","subtype":"success"}'`

const mockImplementationScript = `echo '{"type":"assistant","message":{"content":[{"type":"text","text":"implementation done"}]}}'
echo '{"type":"result","subtype":"success"}'`

const mockReviewScript = `echo '{"type":"assistant","message":{"content":[{"type":"text","text":"review: looks good"}]}}'
echo '{"type":"result","subtype":"success"}'`

const mockFailScript = `echo '{"type":"error","message":"something went wrong"}' >&2; exit 1`

// mockRunner returns a Runner that uses bash to simulate the AI client.
func mockRunner(t *testing.T, script string) executor.Runner {
	t.Helper()
	return executor.New(executor.Config{
		Command:     bashCommand,
		ExtraArgs:   []string{"-c", script},
		IdleTimeout: 10 * time.Second,
	})
}

// setupOrchestratorWithRunner creates a test environment with a custom Runner.
func setupOrchestratorWithRunner(t *testing.T, stages []flow.Stage, runner executor.Runner) (*orchestrator.Orchestrator, string, string) {
	t.Helper()

	runDir := t.TempDir()
	stageIDs := make([]string, len(stages))
	for i, s := range stages {
		stageIDs[i] = s.ID
	}

	rs := state.NewRunState(stageIDs)
	stateFile := filepath.Join(runDir, "state.json")
	if err := rs.Save(stateFile); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()

	orch := orchestrator.New(orchestrator.Options{
		RunDir:    runDir,
		Stages:    stages,
		State:     rs,
		StateFile: stateFile,
		Config:    cfg,
		Prompts:   orchestrator.DefaultPrompts(),
		Runner:    runner,
	})

	return orch, runDir, stateFile
}

// autoApprove subscribes to the bus and auto-approves any stage reaching awaiting_approval.
// Returns a cancel function to stop the auto-approver.
func autoApprove(orch *orchestrator.Orchestrator) context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		events := orch.Bus().Subscribe()
		defer orch.Bus().Unsubscribe(events)
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-events:
				if !ok {
					return
				}
				if ev.Type == orchestrator.EventStageStatusChanged {
					status, _ := ev.Data.(string)
					if status == string(state.StatusAwaitingApproval) {
						orch.Approve(ev.StageID)
					}
				}
			}
		}
	}()
	return cancel
}

// doneCreatingRunner wraps a Runner and creates .done after successful RunAgent calls.
// The orchestrator requires .done files for implementation stage completion.
// Mock scripts cannot create .done themselves because they don't know stageDir,
// so this wrapper creates the file based on the logFile path.
type doneCreatingRunner struct {
	delegate executor.Runner
}

func (r *doneCreatingRunner) RunPlanning(ctx context.Context, stageName, prompt, outFile, logFile string) error {
	return r.delegate.RunPlanning(ctx, stageName, prompt, outFile, logFile)
}

func (r *doneCreatingRunner) RunAgent(ctx context.Context, agentType, stageName, prompt, logFile string) error {
	err := r.delegate.RunAgent(ctx, agentType, stageName, prompt, logFile)
	if err != nil {
		return err
	}
	// Extract stage dir from logFile path: {runDir}/{stageID}/implementation.log
	stageDir := filepath.Dir(logFile)
	return os.WriteFile(filepath.Join(stageDir, ".done"), []byte("test completion"), 0644)
}

// TestIntegration_FullSingleStage verifies the full lifecycle of one stage:
// planning -> awaiting_approval -> (auto-approve) -> ready -> running -> done.
func TestIntegration_FullSingleStage(t *testing.T) {
	stages := []flow.Stage{
		{ID: "backend", Name: "Backend", Description: "implement backend", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
	}

	base := mockRunner(t, mockPlanningScript)
	runner := &doneCreatingRunner{delegate: base}
	orch, runDir, stateFile := setupOrchestratorWithRunner(t, stages, runner)

	cancel := autoApprove(orch)
	defer cancel()

	if err := orch.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Verify: plan.md was created
	planPath := filepath.Join(runDir, "backend", "plan.md")
	data, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("plan.md not found: %v", err)
	}
	if !strings.Contains(string(data), "Step 1") {
		t.Errorf("plan.md content unexpected: %q", string(data))
	}

	// Verify: final status is done
	final, _ := state.Load(stateFile)
	if final.Stages["backend"].Status != state.StatusDone {
		t.Errorf("expected done, got %v", final.Stages["backend"].Status)
	}
}

// TestIntegration_TwoParallelStages verifies that two independent stages
// both complete.
func TestIntegration_TwoParallelStages(t *testing.T) {
	stages := []flow.Stage{
		{ID: "alpha", Name: "Alpha", Description: "first stage", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
		{ID: "beta", Name: "Beta", Description: "second stage", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
	}

	base := mockRunner(t, mockPlanningScript)
	runner := &doneCreatingRunner{delegate: base}
	orch, runDir, stateFile := setupOrchestratorWithRunner(t, stages, runner)

	cancel := autoApprove(orch)
	defer cancel()

	if err := orch.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Both plan.md files should exist
	for _, id := range []string{"alpha", "beta"} {
		p := filepath.Join(runDir, id, "plan.md")
		if _, err := os.Stat(p); err != nil {
			t.Errorf("plan.md for %s not found: %v", id, err)
		}
	}

	// Verify both stages are done
	final, _ := state.Load(stateFile)
	for _, id := range []string{"alpha", "beta"} {
		if final.Stages[id].Status != state.StatusDone {
			t.Errorf("stage %s: expected done, got %v", id, final.Stages[id].Status)
		}
	}
}

// TestIntegration_SequentialDependencies verifies that stage B
// runs only after A completes (depends_on).
func TestIntegration_SequentialDependencies(t *testing.T) {
	stages := []flow.Stage{
		{ID: "first", Name: "First", Description: "runs first", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
		{ID: "second", Name: "Second", Description: "runs after first", DependsOn: []string{"first"}, Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
	}

	base := mockRunner(t, mockPlanningScript)
	runner := &doneCreatingRunner{delegate: base}
	orch, _, stateFile := setupOrchestratorWithRunner(t, stages, runner)

	cancel := autoApprove(orch)
	defer cancel()

	if err := orch.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	final, _ := state.Load(stateFile)
	if final.Stages["first"].Status != state.StatusDone {
		t.Errorf("first: expected done, got %v", final.Stages["first"].Status)
	}
	if final.Stages["second"].Status != state.StatusDone {
		t.Errorf("second: expected done, got %v", final.Stages["second"].Status)
	}
}

// TestIntegration_PreExistingPlan verifies that a stage with a ready plan
// skips the planning agent and goes straight to implementation.
func TestIntegration_PreExistingPlan(t *testing.T) {
	// Create a pre-existing plan file
	planDir := t.TempDir()
	planFile := filepath.Join(planDir, "existing-plan.md")
	planContent := "# Pre-existing Plan\n\n- [ ] Do the thing\n"
	if err := os.WriteFile(planFile, []byte(planContent), 0644); err != nil {
		t.Fatal(err)
	}

	stages := []flow.Stage{
		{ID: "ready", Name: "Ready Stage", Description: "has a plan already", Plan: planFile, Agents: []flow.AgentType{flow.AgentImplementation}},
	}

	// Pre-existing plan stage does not need planning, just implementation
	base := mockRunner(t, mockImplementationScript)
	runner := &doneCreatingRunner{delegate: base}
	orch, runDir, stateFile := setupOrchestratorWithRunner(t, stages, runner)

	if err := orch.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Verify: plan was copied
	copiedPlan := filepath.Join(runDir, "ready", "plan.md")
	data, err := os.ReadFile(copiedPlan)
	if err != nil {
		t.Fatalf("copied plan not found: %v", err)
	}
	if !strings.Contains(string(data), "Pre-existing Plan") {
		t.Errorf("plan content unexpected: %q", string(data))
	}

	// Verify: final status is done
	final, _ := state.Load(stateFile)
	if final.Stages["ready"].Status != state.StatusDone {
		t.Errorf("expected done, got %v", final.Stages["ready"].Status)
	}
}

// TestIntegration_WithReviewAgent verifies that the review agent
// runs after implementation and creates review.log.
func TestIntegration_WithReviewAgent(t *testing.T) {
	stages := []flow.Stage{
		{ID: "reviewed", Name: "Reviewed Stage", Description: "needs review", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation, flow.AgentReview}},
	}

	base := mockRunner(t, mockReviewScript)
	runner := &doneCreatingRunner{delegate: base}
	orch, runDir, _ := setupOrchestratorWithRunner(t, stages, runner)

	cancel := autoApprove(orch)
	defer cancel()

	if err := orch.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Verify: implementation.log and review.log exist
	implLog := filepath.Join(runDir, "reviewed", "implementation.log")
	if _, err := os.Stat(implLog); err != nil {
		t.Errorf("implementation.log not found: %v", err)
	}
	reviewLog := filepath.Join(runDir, "reviewed", "review.log")
	if _, err := os.Stat(reviewLog); err != nil {
		t.Errorf("review.log not found: %v", err)
	}
}

// TestIntegration_FailedStage verifies that when the AI client fails,
// the stage ends up in failed status.
func TestIntegration_FailedStage(t *testing.T) {
	stages := []flow.Stage{
		{ID: "fail", Name: "Failing Stage", Description: "will fail", Agents: []flow.AgentType{flow.AgentPlanning}},
	}

	runner := mockRunner(t, mockFailScript)
	orch, _, stateFile := setupOrchestratorWithRunner(t, stages, runner)

	if err := orch.Run(context.Background()); err != nil {
		t.Fatalf("Run should not return error for failed stage: %v", err)
	}

	// Verify: status failed
	final, _ := state.Load(stateFile)
	if final.Stages["fail"].Status != state.StatusFailed {
		t.Errorf("expected failed, got %v", final.Stages["fail"].Status)
	}
}

// promptCapturingRunner wraps a Runner and captures the prompt passed to RunPlanning.
type promptCapturingRunner struct {
	delegate   executor.Runner
	onPlanning func(prompt string)
}

func (r *promptCapturingRunner) RunPlanning(ctx context.Context, stageName, prompt, outFile, logFile string) error {
	if r.onPlanning != nil {
		r.onPlanning(prompt)
	}
	return r.delegate.RunPlanning(ctx, stageName, prompt, outFile, logFile)
}

func (r *promptCapturingRunner) RunAgent(ctx context.Context, agentType, stageName, prompt, logFile string) error {
	return r.delegate.RunAgent(ctx, agentType, stageName, prompt, logFile)
}

// TestIntegration_PlanningPromptIncludesDependencyPlan verifies that when a stage
// with dependencies starts planning, the prompt includes the dependency stage's plan.
func TestIntegration_PlanningPromptIncludesDependencyPlan(t *testing.T) {
	runDir := t.TempDir()

	// Create dependency plan
	depDir := filepath.Join(runDir, "first")
	if err := os.MkdirAll(depDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(depDir, "plan.md"), []byte("# First Plan"), 0644); err != nil {
		t.Fatal(err)
	}

	stages := []flow.Stage{
		{ID: "first", Name: "First", Description: "runs first", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
		{ID: "second", Name: "Second", Description: "runs after", DependsOn: []string{"first"}, Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
	}

	// Capture the prompt passed to the runner
	var capturedPrompt string
	capturingRunner := &promptCapturingRunner{
		delegate: &doneCreatingRunner{delegate: mockRunner(t, mockPlanningScript)},
		onPlanning: func(prompt string) {
			capturedPrompt = prompt
		},
	}

	stageIDs := []string{"first", "second"}
	rs := state.NewRunState(stageIDs)
	// Mark first stage as done so second starts planning
	rs.SetStageStatus("first", state.StatusDone)
	stateFile := filepath.Join(runDir, "state.json")
	if err := rs.Save(stateFile); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	orch := orchestrator.New(orchestrator.Options{
		RunDir:    runDir,
		Stages:    stages,
		State:     rs,
		StateFile: stateFile,
		Config:    cfg,
		Prompts:   orchestrator.DefaultPrompts(),
		Runner:    capturingRunner,
	})

	cancel := autoApprove(orch)
	defer cancel()

	if err := orch.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !strings.Contains(capturedPrompt, "# First Plan") {
		t.Errorf("planning prompt should contain dependency plan, got:\n%s", capturedPrompt)
	}
}

// rateLimitThenSuccessRunner wraps a Runner and returns a rate limit error
// on the first N calls, then delegates to the underlying runner.
type rateLimitThenSuccessRunner struct {
	delegate  executor.Runner
	failCount int    // number of calls to fail before succeeding
	failMsg   string // error message for failures
	mu        sync.Mutex
	callCount int
}

func (r *rateLimitThenSuccessRunner) RunPlanning(ctx context.Context, stageName, prompt, outFile, logFile string) error {
	r.mu.Lock()
	r.callCount++
	count := r.callCount
	r.mu.Unlock()

	if count <= r.failCount {
		return fmt.Errorf("%s", r.failMsg)
	}
	return r.delegate.RunPlanning(ctx, stageName, prompt, outFile, logFile)
}

func (r *rateLimitThenSuccessRunner) RunAgent(ctx context.Context, agentType, stageName, prompt, logFile string) error {
	r.mu.Lock()
	r.callCount++
	count := r.callCount
	r.mu.Unlock()

	if count <= r.failCount {
		return fmt.Errorf("%s", r.failMsg)
	}
	return r.delegate.RunAgent(ctx, agentType, stageName, prompt, logFile)
}

// TestIntegration_RetryOnServerError verifies that 500 errors trigger
// backoff retry, same as rate limit errors.
func TestIntegration_RetryOnServerError(t *testing.T) {
	origBackoff := orchestrator.RetryBackoff
	orchestrator.RetryBackoff = []time.Duration{1 * time.Millisecond, 2 * time.Millisecond, 5 * time.Millisecond}
	t.Cleanup(func() { orchestrator.RetryBackoff = origBackoff })

	stages := []flow.Stage{
		{ID: "server-err", Name: "Server Error", Description: "test 500 retry", Agents: []flow.AgentType{flow.AgentPlanning}},
	}

	delegate := mockRunner(t, mockPlanningScript)
	rlRunner := &rateLimitThenSuccessRunner{
		delegate:  delegate,
		failCount: 1,
		failMsg:   "500 Internal Server Error",
	}
	runner := &doneCreatingRunner{delegate: rlRunner}
	orch, _, stateFile := setupOrchestratorWithRunner(t, stages, runner)

	cancel := autoApprove(orch)
	defer cancel()

	if err := orch.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	rlRunner.mu.Lock()
	calls := rlRunner.callCount
	rlRunner.mu.Unlock()
	if calls < 2 {
		t.Errorf("expected at least 2 calls (1 fail + 1 success), got %d", calls)
	}

	final, _ := state.Load(stateFile)
	if final.Stages["server-err"].Status != state.StatusDone {
		t.Errorf("expected done after 500 retry, got %v", final.Stages["server-err"].Status)
	}
}

// TestIntegration_RetryOnRateLimit verifies that the orchestrator retries
// when the runner returns a rate limit error, and eventually succeeds.
func TestIntegration_RetryOnRateLimit(t *testing.T) {
	// Speed up retries: use minimal backoff durations
	origBackoff := orchestrator.RetryBackoff
	orchestrator.RetryBackoff = []time.Duration{1 * time.Millisecond, 2 * time.Millisecond, 5 * time.Millisecond}
	t.Cleanup(func() { orchestrator.RetryBackoff = origBackoff })

	stages := []flow.Stage{
		{ID: "retry-stage", Name: "Retry Stage", Description: "test retry", Agents: []flow.AgentType{flow.AgentPlanning}},
	}

	// Fail once with rate limit, then succeed
	delegate := mockRunner(t, mockPlanningScript)
	rlRunner := &rateLimitThenSuccessRunner{
		delegate:  delegate,
		failCount: 1,
		failMsg:   "You've hit your limit · resets 3pm",
	}
	runner := &doneCreatingRunner{delegate: rlRunner}

	orch, _, stateFile := setupOrchestratorWithRunner(t, stages, runner)

	// autoApprove so the stage completes fully after the retry succeeds
	cancel := autoApprove(orch)
	defer cancel()

	if err := orch.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Verify runner was called more than once (retry happened)
	rlRunner.mu.Lock()
	calls := rlRunner.callCount
	rlRunner.mu.Unlock()
	if calls < 2 {
		t.Errorf("expected at least 2 runner calls (1 fail + 1 success), got %d", calls)
	}

	final, _ := state.Load(stateFile)
	if final.Stages["retry-stage"].Status != state.StatusDone {
		t.Errorf("expected done after retry, got %v", final.Stages["retry-stage"].Status)
	}
}

// TestIntegration_RetryExhausted verifies that after exhausting all retry
// attempts the stage ends up in failed status.
func TestIntegration_RetryExhausted(t *testing.T) {
	// Speed up retries: use minimal backoff durations
	origBackoff := orchestrator.RetryBackoff
	orchestrator.RetryBackoff = []time.Duration{1 * time.Millisecond, 2 * time.Millisecond, 5 * time.Millisecond}
	t.Cleanup(func() { orchestrator.RetryBackoff = origBackoff })

	stages := []flow.Stage{
		{ID: "exhaust", Name: "Exhaust Stage", Description: "test retry exhausted", Agents: []flow.AgentType{flow.AgentPlanning}},
	}

	// Always fail with rate limit
	delegate := mockRunner(t, mockFailScript)
	runner := &rateLimitThenSuccessRunner{
		delegate:  delegate,
		failCount: 99,
		failMsg:   "You've hit your limit · resets 3pm",
	}

	orch, _, stateFile := setupOrchestratorWithRunner(t, stages, runner)

	if err := orch.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	final, _ := state.Load(stateFile)
	if final.Stages["exhaust"].Status != state.StatusFailed {
		t.Errorf("expected failed after retries exhausted, got %v", final.Stages["exhaust"].Status)
	}
}

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

	rs := state.NewRunState([]string{"s1"})
	rs.SetStageStatus("s1", state.StatusRunning)
	stateFile := filepath.Join(runDir, "state.json")
	if err := rs.Save(stateFile); err != nil {
		t.Fatal(err)
	}

	// Use a failing runner — if the agent runs, the test should fail
	runner := mockRunner(t, mockFailScript)

	cfg := config.Default()
	orch := orchestrator.New(orchestrator.Options{
		RunDir:    runDir,
		Stages:    stages,
		State:     rs,
		StateFile: stateFile,
		Config:    cfg,
		Prompts:   orchestrator.DefaultPrompts(),
		Runner:    runner,
	})

	if err := orch.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	final, _ := state.Load(stateFile)
	if final.Stages["s1"].Status != state.StatusDone {
		t.Errorf("expected done (from .done file), got %v", final.Stages["s1"].Status)
	}
}

// TestIntegration_PrePlannedStageWaitsForDeps verifies that a stage with plan:
// and depends_on does NOT fail at startup when the plan file doesn't exist yet.
// Instead it waits for its dependency to complete, then activates.
func TestIntegration_PrePlannedStageWaitsForDeps(t *testing.T) {
	// Create a plan file that the "implement" stage references.
	// Initially it does NOT exist — "init" will "create" it.
	planFile := filepath.Join(t.TempDir(), "plan.md")

	stages := []flow.Stage{
		{ID: "init", Name: "Init", Description: "create plan",
			Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
		{ID: "implement", Name: "Implement", Plan: planFile,
			Agents:    []flow.AgentType{flow.AgentImplementation},
			DependsOn: []string{"init"}},
	}

	runner := &planCreatingDoneRunner{
		delegate:  mockRunner(t, mockPlanningScript),
		planFile:  planFile,
		planAfter: 1, // create plan after first RunPlanning (for "init")
	}

	orch, runDir, stateFile := setupOrchestratorWithRunner(t, stages, runner)
	cancel := autoApprove(orch)
	defer cancel()

	if err := orch.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	final, _ := state.Load(stateFile)
	if final.Stages["init"].Status != state.StatusDone {
		t.Errorf("init: expected done, got %v", final.Stages["init"].Status)
	}
	if final.Stages["implement"].Status != state.StatusDone {
		t.Errorf("implement: expected done, got %v", final.Stages["implement"].Status)
	}

	// Verify the plan file was copied into implement's stage dir.
	copiedPlan := filepath.Join(runDir, "implement", "plan.md")
	if _, err := os.Stat(copiedPlan); err != nil {
		t.Errorf("implement/plan.md should exist: %v", err)
	}
}

// planCreatingDoneRunner wraps a Runner and creates a plan file + .done.
type planCreatingDoneRunner struct {
	delegate  executor.Runner
	planFile  string
	planAfter int // create plan after this many RunPlanning calls
	mu        sync.Mutex
	calls     int
}

func (r *planCreatingDoneRunner) RunPlanning(ctx context.Context, stageName, prompt, outFile, logFile string) error {
	err := r.delegate.RunPlanning(ctx, stageName, prompt, outFile, logFile)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.calls++
	shouldCreate := r.calls == r.planAfter
	r.mu.Unlock()
	if shouldCreate {
		_ = os.WriteFile(r.planFile, []byte("# Plan\n- step 1"), 0644)
	}
	return nil
}

func (r *planCreatingDoneRunner) RunAgent(ctx context.Context, agentType, stageName, prompt, logFile string) error {
	err := r.delegate.RunAgent(ctx, agentType, stageName, prompt, logFile)
	if err != nil {
		return err
	}
	stageDir := filepath.Dir(logFile)
	return os.WriteFile(filepath.Join(stageDir, ".done"), []byte("test completion"), 0644)
}

// noDoneRunner wraps a Runner and does NOT create .done, simulating an agent
// that exits successfully without completing work.
// After retryAfter calls it starts creating .done.
type noDoneRunner struct {
	delegate   executor.Runner
	retryAfter int // create .done after this many RunAgent calls
	mu         sync.Mutex
	agentCalls int
}

func (r *noDoneRunner) RunPlanning(ctx context.Context, stageName, prompt, outFile, logFile string) error {
	return r.delegate.RunPlanning(ctx, stageName, prompt, outFile, logFile)
}

func (r *noDoneRunner) RunAgent(ctx context.Context, agentType, stageName, prompt, logFile string) error {
	err := r.delegate.RunAgent(ctx, agentType, stageName, prompt, logFile)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.agentCalls++
	calls := r.agentCalls
	r.mu.Unlock()

	if calls > r.retryAfter {
		stageDir := filepath.Dir(logFile)
		_ = os.WriteFile(filepath.Join(stageDir, ".done"), []byte("done after retry"), 0644)
	}
	return nil
}

// TestIntegration_IncompleteRetry verifies that when an agent exits without
// creating .done, the orchestrator retries once, and the retry succeeds.
func TestIntegration_IncompleteRetry(t *testing.T) {
	stages := []flow.Stage{
		{ID: "incomplete", Name: "Incomplete", Description: "test incomplete retry", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
	}

	base := mockRunner(t, mockPlanningScript)
	runner := &noDoneRunner{
		delegate:   base,
		retryAfter: 1, // first RunAgent (implementation) — no .done; second (retry) — creates .done
	}
	orch, _, stateFile := setupOrchestratorWithRunner(t, stages, runner)

	cancel := autoApprove(orch)
	defer cancel()

	if err := orch.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	runner.mu.Lock()
	calls := runner.agentCalls
	runner.mu.Unlock()

	if calls < 2 {
		t.Errorf("expected at least 2 RunAgent calls (1 incomplete + 1 retry), got %d", calls)
	}

	final, _ := state.Load(stateFile)
	if final.Stages["incomplete"].Status != state.StatusDone {
		t.Errorf("expected done after incomplete retry, got %v", final.Stages["incomplete"].Status)
	}
}

// TestIntegration_IncompleteRetryExhausted verifies that when an agent never
// creates .done, the stage fails after one retry attempt.
func TestIntegration_IncompleteRetryExhausted(t *testing.T) {
	stages := []flow.Stage{
		{ID: "never-done", Name: "Never Done", Description: "test incomplete exhausted", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
	}

	base := mockRunner(t, mockPlanningScript)
	runner := &noDoneRunner{
		delegate:   base,
		retryAfter: 999, // never create .done
	}
	orch, _, stateFile := setupOrchestratorWithRunner(t, stages, runner)

	cancel := autoApprove(orch)
	defer cancel()

	if err := orch.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	final, _ := state.Load(stateFile)
	if final.Stages["never-done"].Status != state.StatusFailed {
		t.Errorf("expected failed after incomplete retry exhausted, got %v", final.Stages["never-done"].Status)
	}
}

// TestIntegration_FailedDependencyCascade verifies that when a stage fails,
// all pending stages that depend on it are also marked as failed,
// so the flow terminates instead of hanging forever.
func TestIntegration_FailedDependencyCascade(t *testing.T) {
	stages := []flow.Stage{
		{ID: "a", Name: "A", Description: "will fail", Agents: []flow.AgentType{flow.AgentPlanning}},
		{ID: "b", Name: "B", Description: "depends on A", DependsOn: []string{"a"}, Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
		{ID: "c", Name: "C", Description: "depends on B", DependsOn: []string{"b"}, Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
	}

	runner := mockRunner(t, mockFailScript)
	orch, _, stateFile := setupOrchestratorWithRunner(t, stages, runner)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := orch.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	final, _ := state.Load(stateFile)
	for _, id := range []string{"a", "b", "c"} {
		if final.Stages[id].Status != state.StatusFailed {
			t.Errorf("stage %s: expected failed, got %v", id, final.Stages[id].Status)
		}
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

	rs := state.NewRunState([]string{"retry-stuck"})
	rs.SetStageStatus("retry-stuck", state.StatusRetrying)
	stateFile := filepath.Join(runDir, "state.json")
	if err := rs.Save(stateFile); err != nil {
		t.Fatal(err)
	}

	base := mockRunner(t, mockPlanningScript)
	runner := &doneCreatingRunner{delegate: base}

	cfg := config.Default()
	orch := orchestrator.New(orchestrator.Options{
		RunDir:    runDir,
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

	rs := state.NewRunState([]string{"planned"})
	rs.SetStageStatus("planned", state.StatusPlanning)
	stateFile := filepath.Join(runDir, "state.json")
	if err := rs.Save(stateFile); err != nil {
		t.Fatal(err)
	}

	// Use a failing runner for planning — if planning re-runs, the test fails
	base := mockRunner(t, mockPlanningScript)
	runner := &doneCreatingRunner{delegate: base}

	cfg := config.Default()
	orch := orchestrator.New(orchestrator.Options{
		RunDir:    runDir,
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
	if final.Stages["planned"].Status != state.StatusDone {
		t.Errorf("expected done, got %v", final.Stages["planned"].Status)
	}

	// Verify the original plan was preserved (not overwritten by re-planning)
	data, _ := os.ReadFile(filepath.Join(stageDir, "plan.md"))
	if !strings.Contains(string(data), "Existing Plan") {
		t.Error("plan.md was overwritten by re-planning, expected 'Existing Plan' content")
	}
}
