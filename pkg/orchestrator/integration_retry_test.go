package orchestrator_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/state"
)

// manualRetryRunner: planning of "First" always fails; implementation of
// "Blocker" blocks until released; everything else delegates.
type manualRetryRunner struct {
	delegate executor.Runner
	release  <-chan struct{}
}

func (r *manualRetryRunner) RunPlanning(ctx context.Context, stageName, prompt, outFile, logFile string) error {
	if stageName == "First" {
		return errors.New("planning exploded")
	}
	return r.delegate.RunPlanning(ctx, stageName, prompt, outFile, logFile)
}

func (r *manualRetryRunner) RunAgent(ctx context.Context, agentType, stageName, prompt, logFile string) error {
	if stageName == "Blocker" {
		select {
		case <-r.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return r.delegate.RunAgent(ctx, agentType, stageName, prompt, logFile)
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

// TestIntegration_RetryOnServerError verifies that 500 errors trigger
// backoff retry, same as rate limit errors.
func TestIntegration_RetryOnServerError(t *testing.T) {
	origBackoff := orchestrator.RetryBackoff
	origMax := orchestrator.MaxRetries
	orchestrator.RetryBackoff = 1 * time.Millisecond
	orchestrator.MaxRetries = 3
	t.Cleanup(func() { orchestrator.RetryBackoff = origBackoff; orchestrator.MaxRetries = origMax })

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

	final := loadStateJSON(t, stateFile)
	if final.Stages["server-err"].Status != state.StatusDone {
		t.Errorf("expected done after 500 retry, got %v", final.Stages["server-err"].Status)
	}
}

// TestIntegration_RetryOnRateLimit verifies that the orchestrator retries
// when the runner returns a rate limit error, and eventually succeeds.
func TestIntegration_RetryOnRateLimit(t *testing.T) {
	// Speed up retries: use minimal backoff durations
	origBackoff := orchestrator.RetryBackoff
	origMax := orchestrator.MaxRetries
	orchestrator.RetryBackoff = 1 * time.Millisecond
	orchestrator.MaxRetries = 3
	t.Cleanup(func() { orchestrator.RetryBackoff = origBackoff; orchestrator.MaxRetries = origMax })

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

	final := loadStateJSON(t, stateFile)
	if final.Stages["retry-stage"].Status != state.StatusDone {
		t.Errorf("expected done after retry, got %v", final.Stages["retry-stage"].Status)
	}
}

// TestIntegration_RetryExhausted verifies that after exhausting all retry
// attempts the stage ends up in failed status.
func TestIntegration_RetryExhausted(t *testing.T) {
	// Speed up retries: use minimal backoff durations
	origBackoff := orchestrator.RetryBackoff
	origMax := orchestrator.MaxRetries
	orchestrator.RetryBackoff = 1 * time.Millisecond
	orchestrator.MaxRetries = 3
	t.Cleanup(func() { orchestrator.RetryBackoff = origBackoff; orchestrator.MaxRetries = origMax })

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

	final := loadStateJSON(t, stateFile)
	if final.Stages["exhaust"].Status != state.StatusFailed {
		t.Errorf("expected failed after retries exhausted, got %v", final.Stages["exhaust"].Status)
	}
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

	final := loadStateJSON(t, stateFile)
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

	final := loadStateJSON(t, stateFile)
	if final.Stages["never-done"].Status != state.StatusFailed {
		t.Errorf("expected failed after incomplete retry exhausted, got %v", final.Stages["never-done"].Status)
	}
}

// verifyCaptureRunner wraps a Runner, always creates .done after RunAgent
// and records every implementation prompt it receives.
type verifyCaptureRunner struct {
	delegate executor.Runner
	mu       sync.Mutex
	prompts  []string
}

func (r *verifyCaptureRunner) RunPlanning(ctx context.Context, stageName, prompt, outFile, logFile string) error {
	return r.delegate.RunPlanning(ctx, stageName, prompt, outFile, logFile)
}

func (r *verifyCaptureRunner) RunAgent(ctx context.Context, agentType, stageName, prompt, logFile string) error {
	r.mu.Lock()
	r.prompts = append(r.prompts, prompt)
	r.mu.Unlock()
	if err := r.delegate.RunAgent(ctx, agentType, stageName, prompt, logFile); err != nil {
		return err
	}
	stageDir := filepath.Dir(logFile)
	return os.WriteFile(filepath.Join(stageDir, ".done"), []byte("agent claims done"), 0644)
}

// TestIntegration_VerifyPass verifies that a stage with a passing verify
// command completes normally.
func TestIntegration_VerifyPass(t *testing.T) {
	stages := []flow.Stage{
		{ID: "verified", Name: "Verified", Description: "verify passes",
			Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation},
			Verify: "true"},
	}

	runner := &verifyCaptureRunner{delegate: mockRunner(t, mockPlanningScript)}
	orch, _, stateFile := setupOrchestratorWithRunner(t, stages, runner)

	cancel := autoApprove(orch)
	defer cancel()

	if err := orch.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	final := loadStateJSON(t, stateFile)
	if final.Stages["verified"].Status != state.StatusDone {
		t.Errorf("expected done when verify passes, got %v", final.Stages["verified"].Status)
	}

	runner.mu.Lock()
	prompts := append([]string{}, runner.prompts...)
	runner.mu.Unlock()
	if len(prompts) == 0 || !strings.Contains(prompts[0], stages[0].Verify) {
		t.Error("implementation prompt should announce the verify command")
	}
}

// TestIntegration_VerifyFail verifies that a failing verify command overrides
// the agent's .done: the stage is retried once with the verify output in the
// prompt, then fails.
func TestIntegration_VerifyFail(t *testing.T) {
	stages := []flow.Stage{
		{ID: "unverified", Name: "Unverified", Description: "verify fails",
			Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation},
			Verify: "echo 'VERIFY-BOOM: 63 tests failed'; exit 1"},
	}

	runner := &verifyCaptureRunner{delegate: mockRunner(t, mockPlanningScript)}
	orch, _, stateFile := setupOrchestratorWithRunner(t, stages, runner)

	cancel := autoApprove(orch)
	defer cancel()

	if err := orch.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	final := loadStateJSON(t, stateFile)
	if final.Stages["unverified"].Status != state.StatusFailed {
		t.Errorf("expected failed when verify fails despite .done, got %v", final.Stages["unverified"].Status)
	}

	runner.mu.Lock()
	prompts := append([]string{}, runner.prompts...)
	runner.mu.Unlock()

	if len(prompts) != 2 {
		t.Fatalf("expected 2 RunAgent calls (initial + retry), got %d", len(prompts))
	}
	if !strings.Contains(prompts[1], "VERIFY-BOOM") {
		t.Errorf("retry prompt should contain verify output, got:\n%s", prompts[1])
	}
}

// TestIntegration_ManualRetryWaitsForDeps verifies that manual retry of a
// failed dependent stage keeps it pending instead of starting planning while
// its dependency is still failed.
func TestIntegration_ManualRetryWaitsForDeps(t *testing.T) {
	stages := []flow.Stage{
		{ID: "first", Name: "First", Description: "fails at planning", Agents: []flow.AgentType{flow.AgentPlanning}},
		{ID: "second", Name: "Second", Description: "depends on first", DependsOn: []string{"first"}, Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
		{ID: "blocker", Name: "Blocker", Description: "keeps the flow alive", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
	}

	release := make(chan struct{})
	rec := &callRecordingRunner{delegate: &manualRetryRunner{
		delegate: &doneCreatingRunner{delegate: mockRunner(t, mockPlanningScript)},
		release:  release,
	}}
	orch, _, stateFile := setupOrchestratorWithRunner(t, stages, rec)

	cancel := autoApprove(orch)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- orch.Run(context.Background()) }()

	// first fails its planning, second is cascade-failed.
	waitForStatus(t, stateFile, "second", state.StatusFailed, 10*time.Second)

	// Manual retry of second: dependency is still failed — the stage must
	// stay pending and must not start planning.
	if err := orch.Retry(context.Background(), "second"); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, stateFile, "second", state.StatusPending, 10*time.Second)

	// Release the blocker; its completion re-fails second (dep still failed)
	// and the flow terminates.
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, c := range rec.callsSnapshot() {
		if c == "planning:Second" {
			t.Errorf("second must not plan after manual retry with failed dep, calls: %v", rec.callsSnapshot())
		}
	}

	final := loadStateJSON(t, stateFile)
	if final.Stages["second"].Status != state.StatusFailed {
		t.Errorf("second: expected failed, got %v", final.Stages["second"].Status)
	}
}
