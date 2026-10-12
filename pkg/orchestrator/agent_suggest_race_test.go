package orchestrator_test

import (
	"context"
	"errors"
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

// naturalCompletionRaceRunner reproduces the OTHER agent_suggest race: the agent subprocess finishes
// NATURALLY at almost the same instant Revise fires Running -> Revising.
type naturalCompletionRaceRunner struct {
	calls   int
	proceed chan struct{} // test closes this only after Revise() has returned,
	// Block completion until Revise has durably changed status and saved feedback.
	secondCallFeedback string // captured feedback.md content on the reconciliation restart
}

func (r *naturalCompletionRaceRunner) RunPlanning(_ context.Context, _, _, outFile, _ string) error {
	plan := "## Tasks\n\n- [ ] implement feature\n\n## Assumptions\n\n- none\n\n## Acceptance Criteria\n\n- [ ] feature works\n"
	return os.WriteFile(outFile, []byte(plan), 0644)
}

func (r *naturalCompletionRaceRunner) RunAgent(_ context.Context, _, _, _, logFile string) error {
	r.calls++
	stageDir := filepath.Dir(logFile)
	if r.calls == 1 {
		<-r.proceed
		// Natural completion: the agent finished its work on its own, with no idea that Revise had already fire
		// EvRevise and send a signal into interruptChans that nobody will ever read.
		return os.WriteFile(filepath.Join(stageDir, ".done"), []byte("done\n"), 0644)
	}
	// The reconciliation restart must read the saved feedback, proving it was applied.
	feedback, err := os.ReadFile(filepath.Join(stageDir, "feedback.md"))
	if err != nil {
		return errors.New("expected feedback.md to be readable on the reconciliation restart")
	}
	r.secondCallFeedback = string(feedback)
	return os.WriteFile(filepath.Join(stageDir, ".done"), []byte("done\n"), 0644)
}

func (r *naturalCompletionRaceRunner) RunJSONQuery(_ context.Context, _ string) ([]byte, error) {
	return nil, errors.New("not used in this test")
}

var _ executor.Runner = (*naturalCompletionRaceRunner)(nil)

func TestAgentSuggest_NaturalCompletionRaceReconciles(t *testing.T) {
	runDir := t.TempDir()
	stages := []flow.Stage{{
		ID: "impl", Name: "Impl",
		Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation},
	}}

	store, err := state.Open(runDir, []string{"impl"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	stateFile := filepath.Join(runDir, "state.json")

	runner := &naturalCompletionRaceRunner{proceed: make(chan struct{})}
	orch := orchestrator.New(orchestrator.Options{
		RunDir:  runDir,
		Stages:  stages,
		Store:   store,
		Config:  config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
		Runner:  runner,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	runOrchestratorAsync(ctx, t, orch, cancel)

	waitForStatus(t, stateFile, "impl", state.StatusRunning, 10*time.Second)

	// Fires EvRevise and durably saves feedback.md BEFORE this call returns.
	if _, _, err := orch.Revise(ctx, "impl", "please add extra logging"); err != nil {
		t.Fatalf("Revise: %v", err)
	}
	waitForStatus(t, stateFile, "impl", state.StatusRevising, 5*time.Second)

	// Release natural completion only after Revise commits its transition.
	close(runner.proceed)

	waitForStatus(t, stateFile, "impl", state.StatusDone, 15*time.Second)

	if runner.calls != 2 {
		t.Fatalf("expected exactly 2 RunAgent calls (natural completion + reconciliation restart), got %d", runner.calls)
	}
	if runner.secondCallFeedback == "" {
		t.Fatal("reconciliation restart did not read feedback.md content")
	}
	if want := "please add extra logging"; !strings.Contains(runner.secondCallFeedback, want) {
		t.Errorf("reconciliation restart's feedback.md = %q, want it to contain %q", runner.secondCallFeedback, want)
	}
}
