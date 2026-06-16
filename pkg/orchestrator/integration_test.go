package orchestrator_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/state"
)

const bashCommand = "bash"

// bash scripts for mocking the AI client (simulate claude stream-json protocol)

const mockPlanningScript = `echo '{"type":"assistant","message":{"content":[{"type":"text","text":"## Tasks\n\n- [ ] Step 1: implement feature\n- [ ] Step 2: write tests\n\n## Assumptions\n\n- none\n\n## Acceptance Criteria\n\n- [ ] feature works\n"}]}}'
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

	store, err := state.Open(runDir, stageIDs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	cfg := config.Default()

	orch := orchestrator.New(orchestrator.Options{
		RunDir:  runDir,
		Stages:  stages,
		Store:   store,
		Config:  cfg,
		Prompts: orchestrator.DefaultPrompts(),
		Runner:  runner,
	})

	return orch, runDir, filepath.Join(runDir, "state.json")
}

// autoApprove subscribes to the bus and auto-approves any stage reaching awaiting_approval.
// Returns a cancel function to stop the auto-approver.
func autoApprove(orch *orchestrator.Orchestrator) context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		subID, events := orch.UIBus().Subscribe(64)
		defer orch.UIBus().Unsubscribe(subID)
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
						_ = orch.Approve(context.Background(), ev.StageID)
					}
				}
			}
		}
	}()
	return cancel
}

func waitForStatus(t *testing.T, stateFile, stageID string, want state.StageStatus, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		rs, err := tryLoadStateJSON(stateFile)
		if err == nil && rs.Stages[stageID].Status == want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	rs, err := tryLoadStateJSON(stateFile)
	current := "<missing>"
	if err == nil && rs != nil {
		current = string(rs.Stages[stageID].Status)
	}
	t.Fatalf("stage %s did not reach %s within %s (current: %s)", stageID, want, timeout, current)
}

func tryLoadStateJSON(path string) (*state.RunState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rs state.RunState
	if err := json.Unmarshal(data, &rs); err != nil {
		return nil, err
	}
	return &rs, nil
}

func loadStateJSON(t *testing.T, path string) *state.RunState {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read state.json: %v", err)
	}
	var rs state.RunState
	if err := json.Unmarshal(data, &rs); err != nil {
		t.Fatalf("unmarshal state: %v", err)
	}
	return &rs
}

// doneCreatingRunner wraps a Runner and creates .done after successful RunAgent calls.
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

// phaseDispatchRunner uses a different runner for planning vs other phases.
type phaseDispatchRunner struct {
	planning executor.Runner
	other    executor.Runner
}

func (r *phaseDispatchRunner) RunPlanning(ctx context.Context, stageName, prompt, outFile, logFile string) error {
	return r.planning.RunPlanning(ctx, stageName, prompt, outFile, logFile)
}

func (r *phaseDispatchRunner) RunAgent(ctx context.Context, agentType, stageName, prompt, logFile string) error {
	return r.other.RunAgent(ctx, agentType, stageName, prompt, logFile)
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

// callRecordingRunner wraps a Runner and records the order of calls
// as "planning:<stageName>" / "agent:<stageName>".
type callRecordingRunner struct {
	delegate executor.Runner
	mu       sync.Mutex
	calls    []string
}

func (r *callRecordingRunner) record(kind, stageName string) {
	r.mu.Lock()
	r.calls = append(r.calls, kind+":"+stageName)
	r.mu.Unlock()
}

func (r *callRecordingRunner) callsSnapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string{}, r.calls...)
}

func (r *callRecordingRunner) RunPlanning(ctx context.Context, stageName, prompt, outFile, logFile string) error {
	r.record("planning", stageName)
	return r.delegate.RunPlanning(ctx, stageName, prompt, outFile, logFile)
}

func (r *callRecordingRunner) RunAgent(ctx context.Context, agentType, stageName, prompt, logFile string) error {
	r.record("agent", stageName)
	return r.delegate.RunAgent(ctx, agentType, stageName, prompt, logFile)
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
	final := loadStateJSON(t, stateFile)
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
	final := loadStateJSON(t, stateFile)
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

	final := loadStateJSON(t, stateFile)
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
	final := loadStateJSON(t, stateFile)
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

	planningRunner := mockRunner(t, mockPlanningScript)
	reviewRunner := mockRunner(t, mockReviewScript)
	runner := &phaseDispatchRunner{planning: planningRunner, other: reviewRunner}
	doneRunner := &doneCreatingRunner{delegate: runner}
	orch, runDir, _ := setupOrchestratorWithRunner(t, stages, doneRunner)

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
	store, err := state.Open(runDir, stageIDs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	// Mark first stage as done so second starts planning
	_ = store.Apply(state.Transition{StageID: "first", From: state.StatusPending, To: state.StatusDone, Event: "test_setup"})

	cfg := config.Default()
	orch := orchestrator.New(orchestrator.Options{
		RunDir:  runDir,
		Stages:  stages,
		Store:   store,
		Config:  cfg,
		Prompts: orchestrator.DefaultPrompts(),
		Runner:  capturingRunner,
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

	final := loadStateJSON(t, stateFile)
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

// TestIntegration_PlanSavedToCustomFile: описание стадии просит агента сохранить
// план в файл с произвольным именем. Агент пишет план через Write tool, а текстом
// выводит лишь резюме. Оркестратор должен подхватить план из записанного файла,
// а не проваливать стадию на валидации текстового вывода.
func TestIntegration_PlanSavedToCustomFile(t *testing.T) {
	planFile := filepath.Join(t.TempDir(), "my-custom-plan.md")
	planContent := "## Tasks\n\n- [ ] step 1\n\n## Assumptions\n\n- none\n\n## Acceptance Criteria\n\n- [ ] works\n"

	script := fmt.Sprintf(
		`printf '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":"%s","content":"..."}}]}}\n'`+
			"\nprintf '%%b' %q > %q"+
			"\nprintf '{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"plan saved to file\"}]}}\n'"+
			"\nprintf '{\"type\":\"result\",\"subtype\":\"success\"}\n'",
		planFile, planContent, planFile,
	)

	stages := []flow.Stage{
		{ID: "init", Name: "Init", Description: "save plan to my-custom-plan.md",
			Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
	}

	runner := &doneCreatingRunner{delegate: mockRunner(t, script)}
	orch, runDir, stateFile := setupOrchestratorWithRunner(t, stages, runner)
	cancel := autoApprove(orch)
	defer cancel()

	if err := orch.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	final := loadStateJSON(t, stateFile)
	if final.Stages["init"].Status != state.StatusDone {
		t.Errorf("init: expected done, got %v", final.Stages["init"].Status)
	}

	data, err := os.ReadFile(filepath.Join(runDir, "init", "plan.md"))
	if err != nil {
		t.Fatalf("init/plan.md: %v", err)
	}
	if !strings.Contains(string(data), "## Tasks") {
		t.Errorf("init/plan.md should contain adopted plan, got %q", string(data))
	}
}

// eagerProbeRunner blocks planning of stage "First" until planning of
// "Second" is observed, proving that "Second" plans eagerly at startup.
type eagerProbeRunner struct {
	delegate   executor.Runner
	secondSeen chan struct{}
	once       sync.Once
}

func (r *eagerProbeRunner) RunPlanning(ctx context.Context, stageName, prompt, outFile, logFile string) error {
	if stageName == "Second" {
		r.once.Do(func() { close(r.secondSeen) })
	}
	if stageName == "First" {
		select {
		case <-r.secondSeen:
		case <-time.After(10 * time.Second):
			return errors.New("planning of Second did not start eagerly")
		}
	}
	return r.delegate.RunPlanning(ctx, stageName, prompt, outFile, logFile)
}

func (r *eagerProbeRunner) RunAgent(ctx context.Context, agentType, stageName, prompt, logFile string) error {
	return r.delegate.RunAgent(ctx, agentType, stageName, prompt, logFile)
}

// TestIntegration_EagerPlanningStartsImmediately verifies that a stage with
// eager_planning: true plans at flow start, before its dependency is done.
func TestIntegration_EagerPlanningStartsImmediately(t *testing.T) {
	stages := []flow.Stage{
		{ID: "first", Name: "First", Description: "runs first", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
		{ID: "second", Name: "Second", Description: "plans eagerly", DependsOn: []string{"first"}, EagerPlanning: true, Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
	}

	runner := &eagerProbeRunner{
		delegate:   &doneCreatingRunner{delegate: mockRunner(t, mockPlanningScript)},
		secondSeen: make(chan struct{}),
	}
	orch, _, stateFile := setupOrchestratorWithRunner(t, stages, runner)

	cancel := autoApprove(orch)
	defer cancel()

	if err := orch.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	final := loadStateJSON(t, stateFile)
	for _, id := range []string{"first", "second"} {
		if final.Stages[id].Status != state.StatusDone {
			t.Errorf("stage %s: expected done, got %v", id, final.Stages[id].Status)
		}
	}
}

// TestIntegration_PlanningWaitsForDependencies verifies that by default the
// planning of a dependent stage starts only after its dependency is done.
func TestIntegration_PlanningWaitsForDependencies(t *testing.T) {
	stages := []flow.Stage{
		{ID: "first", Name: "First", Description: "runs first", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
		{ID: "second", Name: "Second", Description: "plans after first is done", DependsOn: []string{"first"}, Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
	}

	rec := &callRecordingRunner{delegate: &doneCreatingRunner{delegate: mockRunner(t, mockPlanningScript)}}
	orch, _, stateFile := setupOrchestratorWithRunner(t, stages, rec)

	cancel := autoApprove(orch)
	defer cancel()

	if err := orch.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	final := loadStateJSON(t, stateFile)
	for _, id := range []string{"first", "second"} {
		if final.Stages[id].Status != state.StatusDone {
			t.Errorf("stage %s: expected done, got %v", id, final.Stages[id].Status)
		}
	}

	calls := rec.callsSnapshot()
	idxImplFirst, idxPlanSecond := -1, -1
	for i, c := range calls {
		switch c {
		case "agent:First":
			if idxImplFirst == -1 {
				idxImplFirst = i
			}
		case "planning:Second":
			idxPlanSecond = i
		default:
		}
	}
	if idxImplFirst == -1 || idxPlanSecond == -1 {
		t.Fatalf("expected both agent:First and planning:Second in calls, got %v", calls)
	}
	if idxPlanSecond < idxImplFirst {
		t.Errorf("planning of second started before first finished, calls: %v", calls)
	}
}
