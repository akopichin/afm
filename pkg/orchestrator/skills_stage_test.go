package orchestrator_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/skills"
	"github.com/akopichin/afm/pkg/state"
)

// agentPromptRunner records every prompt a stage agent receives, planning and agent turns alike.
type agentPromptRunner struct {
	promptCapturingRunner
	mu      sync.Mutex
	prompts []string
}

func (r *agentPromptRunner) record(prompt string) {
	r.mu.Lock()
	r.prompts = append(r.prompts, prompt)
	r.mu.Unlock()
}

func (r *agentPromptRunner) RunPlanning(ctx context.Context, stageName, prompt, outFile, logFile string) error {
	r.record(prompt)
	return r.delegate.RunPlanning(ctx, stageName, prompt, outFile, logFile)
}

func (r *agentPromptRunner) RunAgent(ctx context.Context, agentType, stageName, prompt, logFile string) error {
	r.record(prompt)
	return r.delegate.RunAgent(ctx, agentType, stageName, prompt, logFile)
}

func writeLibrary(t *testing.T) string {
	t.Helper()
	lib := t.TempDir()
	dir := filepath.Join(lib, "tdd")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: tdd\ndescription: Test first\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return lib
}

// Every phase prompt of a stage — planning, implementation, embedded review and the autonomous track — carries
// the stage's skills and catalog.
func TestIntegration_StagePromptsCarrySkills(t *testing.T) {
	lib := writeLibrary(t)
	stages := []flow.Stage{
		{ID: "std", Name: "Std", Description: "do", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation, flow.AgentReview},
			Skills: skills.Names{"tdd"}, SkillsDir: skills.Dir(lib)},
		{ID: "auto", Name: "Auto", Description: "do", Agents: []flow.AgentType{flow.AgentAuto},
			Skills: skills.Names{"tdd"}, SkillsDir: skills.Dir(lib)},
	}
	runner := &agentPromptRunner{promptCapturingRunner: promptCapturingRunner{delegate: mockRunner(t, mockPlanningScript)}}
	orch, _, _ := setupOrchestratorWithRunner(t, stages, runner)
	cancel := autoApprove(orch)
	defer cancel()
	// The mock never writes the completion marker, so the run ends in failure; the prompts were built by then.
	ctx, cancelRun := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelRun()
	_ = orch.Run(ctx)

	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.prompts) < 4 {
		t.Fatalf("captured %d prompts, want planning, implementation, review and autonomous", len(runner.prompts))
	}
	for i, p := range runner.prompts {
		if !strings.Contains(p, "Required skills, in this order: tdd.") || !strings.Contains(p, "- tdd: Test first ("+filepath.Join(lib, "tdd", "SKILL.md")+")") {
			t.Errorf("prompt %d lacks the skills block:\n%s", i+1, p)
		}
	}
}

func TestIntegration_BrokenSkillsDirFailsTheStageBeforeTheAgent(t *testing.T) {
	stages := []flow.Stage{
		{ID: "auto", Name: "Auto", Description: "do", Agents: []flow.AgentType{flow.AgentAuto}, SkillsDir: skills.Dir(filepath.Join(t.TempDir(), "missing"))},
	}
	runner := &agentPromptRunner{promptCapturingRunner: promptCapturingRunner{delegate: mockRunner(t, mockImplementationScript)}}
	orch, _, stateFile := setupOrchestratorWithRunner(t, stages, runner)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = orch.Run(ctx)
	if ctx.Err() != nil {
		t.Fatal("the run was still retrying an unreadable skills_dir when the timeout hit")
	}

	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.prompts) != 0 {
		t.Fatalf("the agent ran %d times despite an unreadable skills_dir", len(runner.prompts))
	}
	if got := loadStateJSON(t, stateFile).Stages["auto"].Status; got != state.StatusFailed {
		t.Fatalf("stage status = %v, want failed at once, without retries", got)
	}
}
