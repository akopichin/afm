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

// blockingThenFeedbackRunner пишет план, ждёт реальный interruptCh, возвращает ErrUserInterrupted и читает
// feedback на втором вызове.
type blockingThenFeedbackRunner struct {
	calls   int
	orch    *orchestrator.Orchestrator // выставляется тестом после New(), до Run()
	stageID string
}

func (r *blockingThenFeedbackRunner) RunPlanning(_ context.Context, _, _, outFile, _ string) error {
	plan := "## Tasks\n\n- [ ] implement feature\n\n## Assumptions\n\n- none\n\n## Acceptance Criteria\n\n- [ ] feature works\n"
	return os.WriteFile(outFile, []byte(plan), 0644)
}

func (r *blockingThenFeedbackRunner) RunAgent(ctx context.Context, _, _, _, logFile string) error {
	r.calls++
	stageDir := filepath.Dir(logFile)
	if r.calls == 1 {
		// runWithRetry регистрирует interruptCh в реестре ДО вызова agentFn (следовательно, до этого RunAgent),
		// поэтому Load здесь не гонится с Revise()/Store() — канал уже на месте.
		ch, ok := orchestrator.InterruptChanForTest(r.orch, r.stageID)
		if !ok {
			<-ctx.Done()
			return context.Canceled
		}
		select {
		case <-ch:
			return executor.ErrUserInterrupted
		case <-ctx.Done():
			return context.Canceled
		}
	}
	feedback, _ := os.ReadFile(filepath.Join(stageDir, "feedback.md"))
	if len(feedback) == 0 {
		return errors.New("expected feedback.md to be readable on the feedback restart")
	}
	// checkCompletion требует .done, а не execution_summary.md — тот пишет только автономный трек, не
	// задействованный в этом тесте.
	return os.WriteFile(filepath.Join(stageDir, ".done"), []byte("done\n"), 0644)
}

func (r *blockingThenFeedbackRunner) RunJSONQuery(_ context.Context, _ string) ([]byte, error) {
	return nil, errors.New("not used in this test")
}

var _ executor.Runner = (*blockingThenFeedbackRunner)(nil)

func TestAgentSuggest_InterruptRestartsWithFeedback(t *testing.T) {
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

	runner := &blockingThenFeedbackRunner{stageID: "impl"}
	cfg := config.Default()

	orch := orchestrator.New(orchestrator.Options{
		RunDir:  runDir,
		Stages:  stages,
		Store:   store,
		Config:  cfg,
		Prompts: orchestrator.DefaultPrompts(),
		Runner:  runner,
	})
	runner.orch = orch

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	runOrchestratorAsync(ctx, t, orch, cancel)

	waitForStatus(t, stateFile, "impl", state.StatusRunning, 10*time.Second)

	// planning уже должно было пройти (agents: [planning, implementation]),
	// implementation блокируется в blockingThenFeedbackRunner.RunAgent.
	if _, _, err := orch.Revise(ctx, "impl", "please add extra logging"); err != nil {
		t.Fatalf("Revise: %v", err)
	}

	waitForStatus(t, stateFile, "impl", state.StatusDone, 15*time.Second)

	feedbackPath := filepath.Join(runDir, "impl", "feedback.md")
	data, err := os.ReadFile(feedbackPath)
	if err != nil {
		t.Fatalf("feedback.md not found: %v", err)
	}
	// state.SaveFeedback prepends a "--- revision N | timestamp ---" header
	// (see pkg/state/state.go) — the phrase itself must still be present.
	if !strings.Contains(string(data), "please add extra logging") {
		t.Errorf("feedback.md = %q, want it to contain %q", data, "please add extra logging")
	}
	if runner.calls != 2 {
		t.Errorf("expected exactly 2 RunAgent calls (initial + feedback restart), got %d", runner.calls)
	}
}
