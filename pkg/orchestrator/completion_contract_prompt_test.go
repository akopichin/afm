package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

// contractCapturingRunner — фейковый executor.Runner: записывает промпт, переданный в RunPlanning/RunAgent
// (RunAgent — по agentType), и сам создаёт маркер завершения (.done / execution_summary.md), который в
// реальности пишет агент — так runWithRetry's completion-гейт проходит без настоящего подпроцесса.
type contractCapturingRunner struct {
	mu             sync.Mutex
	planningPrompt string
	prompts        map[string]string
}

func (r *contractCapturingRunner) RunPlanning(_ context.Context, _, prompt, outFile, _ string) error {
	r.mu.Lock()
	r.planningPrompt = prompt
	r.mu.Unlock()
	return os.WriteFile(outFile, []byte("## Tasks\n- t\n\n## Assumptions\n- none\n\n## Acceptance Criteria\n- ok\n"), 0644)
}

func (r *contractCapturingRunner) RunAgent(_ context.Context, agentType, _, prompt, logFile string) error {
	r.mu.Lock()
	if r.prompts == nil {
		r.prompts = map[string]string{}
	}
	r.prompts[agentType] = prompt
	r.mu.Unlock()

	// logFile == <stageDir>/<phase>.log — так построен во всех раннерах в agents.go.
	stageDir := filepath.Dir(logFile)
	switch agentType {
	case phaseImplementation, phaseReview:
		return os.WriteFile(filepath.Join(stageDir, ".done"), []byte("done"), 0644)
	case phaseAutonomous:
		return os.WriteFile(filepath.Join(stageDir, "execution_summary.md"), []byte("summary"), 0644)
	default:
		return nil
	}
}

func (r *contractCapturingRunner) RunJSONQuery(context.Context, string) ([]byte, error) {
	return nil, nil
}

func (r *contractCapturingRunner) promptFor(agentType string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.prompts[agentType]
}

// newContractTestOrchestrator builds a full Orchestrator (via New, same as production wiring) around a
// single stage backed by runner. Status is left at the store's default "pending" — the run*Agent methods
// under test (except planning, which triggers its own start_planning) don't require "running" to proceed;
// only paused/revising short-circuit them, and the stage starts in neither.
func newContractTestOrchestrator(t *testing.T, stage flow.Stage, runner executor.Runner, prompts Prompts) (*Orchestrator, string) {
	t.Helper()
	runDir := t.TempDir()
	store, err := state.Open(runDir, []string{stage.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	o := New(Options{
		RunDir:  runDir,
		Stages:  []flow.Stage{stage},
		Store:   store,
		Config:  config.Default(),
		Prompts: prompts,
		Runner:  runner,
	})
	return o, runDir
}

func TestImplementationPromptCarriesContract(t *testing.T) {
	stage := flow.Stage{ID: "s1", Name: "S1", Agents: []flow.AgentType{flow.AgentImplementation}}
	runner := &contractCapturingRunner{}
	o, runDir := newContractTestOrchestrator(t, stage, runner, DefaultPrompts())

	stageDir := filepath.Join(runDir, stage.ID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("a plan"), 0644); err != nil {
		t.Fatal(err)
	}

	o.runImplementationAgent(context.Background(), stage)

	got := runner.promptFor(phaseImplementation)
	if got == "" {
		t.Fatal("expected a captured implementation prompt")
	}
	donePath := filepath.Join(o.stageDir(stage.ID), ".done")
	if !strings.Contains(got, donePath) {
		t.Fatalf("expected the implementation prompt to carry the completion contract (%q), got:\n%s", donePath, got)
	}
}

func TestReviewPromptCarriesContract(t *testing.T) {
	// KEY REGRESSION (D6): standalone review is gated on .done (stagefiles.CheckCompletion) but, before T8,
	// its prompt never mentioned .done or even the stage directory.
	stage := flow.Stage{ID: "s1", Name: "S1", Agents: []flow.AgentType{flow.AgentReview}}
	runner := &contractCapturingRunner{}
	o, _ := newContractTestOrchestrator(t, stage, runner, DefaultPrompts())

	o.runReviewAgent(context.Background(), stage)

	got := runner.promptFor(phaseReview)
	if got == "" {
		t.Fatal("expected a captured review prompt")
	}
	donePath := filepath.Join(o.stageDir(stage.ID), ".done")
	if !strings.Contains(got, donePath) {
		t.Fatalf("review is gated on .done but its prompt never mentions it; expected %q in:\n%s", donePath, got)
	}
}

func TestEmbeddedReviewPromptCarriesContract(t *testing.T) {
	// KEY REGRESSION (D6, found in review): runEmbeddedReview — the review step inlined into an
	// implementation attempt for a stage declaring `agents: [implementation, review]` — is a SEPARATE call
	// site from the standalone runReviewAgent covered by TestReviewPromptCarriesContract above. Both are
	// gated on the same .done check, but nothing before this test asserted the EMBEDDED path's prompt
	// actually carries the contract too — a future refactor could drop it from runEmbeddedReview alone and
	// every other T8 test would stay green while agents:[implementation, review] stages silently regressed.
	stage := flow.Stage{ID: "s1", Name: "S1", Agents: []flow.AgentType{flow.AgentImplementation, flow.AgentReview}}
	runner := &contractCapturingRunner{}
	o, runDir := newContractTestOrchestrator(t, stage, runner, DefaultPrompts())

	stageDir := filepath.Join(runDir, stage.ID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("a plan"), 0644); err != nil {
		t.Fatal(err)
	}

	o.runImplementationAgent(context.Background(), stage)

	got := runner.promptFor(phaseReview)
	if got == "" {
		t.Fatal("expected a captured embedded-review prompt")
	}
	donePath := filepath.Join(o.stageDir(stage.ID), ".done")
	if !strings.Contains(got, donePath) {
		t.Fatalf("embedded review is gated on .done but its prompt never mentions it; expected %q in:\n%s", donePath, got)
	}
}

func TestAutonomousPromptCarriesContract(t *testing.T) {
	stage := flow.Stage{ID: "s1", Name: "S1", Agents: []flow.AgentType{flow.AgentAuto}}
	runner := &contractCapturingRunner{}
	o, _ := newContractTestOrchestrator(t, stage, runner, DefaultPrompts())

	o.runAutonomousAgent(context.Background(), stage)

	got := runner.promptFor(phaseAutonomous)
	if got == "" {
		t.Fatal("expected a captured autonomous prompt")
	}
	summaryPath := filepath.Join(o.stageDir(stage.ID), "execution_summary.md")
	if !strings.Contains(got, summaryPath) {
		t.Fatalf("expected the autonomous prompt to carry the completion contract (%q), got:\n%s", summaryPath, got)
	}
}

func TestPlanningPromptHasNoDoneContract(t *testing.T) {
	stage := flow.Stage{ID: "s1", Name: "S1", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}}
	runner := &contractCapturingRunner{}
	o, _ := newContractTestOrchestrator(t, stage, runner, DefaultPrompts())

	o.runPlanningAgent(context.Background(), stage)

	got := runner.planningPrompt
	if got == "" {
		t.Fatal("expected a captured planning prompt")
	}
	if strings.Contains(got, ".done") {
		t.Fatalf("planning must not receive the implementation/review completion contract, got:\n%s", got)
	}
	if strings.Contains(got, "## Completion contract (mandatory)") {
		t.Fatalf("planning must not receive the code-generated completion-contract block (it has its own), got:\n%s", got)
	}
}

func TestContractSurvivesPromptOverride(t *testing.T) {
	// D5: prompts_dir can replace implementation.md wholesale — the mandatory block must still be present
	// because it's appended by code, not by the template.
	stage := flow.Stage{ID: "s1", Name: "S1", Agents: []flow.AgentType{flow.AgentImplementation}}
	runner := &contractCapturingRunner{}
	overridden := Prompts{Implementation: "Totally custom instructions that never mention the completion marker file."}
	o, runDir := newContractTestOrchestrator(t, stage, runner, overridden)

	stageDir := filepath.Join(runDir, stage.ID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("a plan"), 0644); err != nil {
		t.Fatal(err)
	}

	o.runImplementationAgent(context.Background(), stage)

	got := runner.promptFor(phaseImplementation)
	if !strings.Contains(got, "Totally custom instructions") {
		t.Fatalf("expected the overridden template text in the prompt, got:\n%s", got)
	}
	donePath := filepath.Join(o.stageDir(stage.ID), ".done")
	if !strings.Contains(got, donePath) {
		t.Fatalf("mandatory completion block must survive a prompts_dir override; expected %q in:\n%s", donePath, got)
	}
}
