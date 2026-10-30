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
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/state"
)

// summaryRecoveryRunner пишет summary автономной стадии только на writeOnCall-й вызов этой стадии.
type summaryRecoveryRunner struct {
	mu          sync.Mutex
	calls       map[string]int
	prompts     map[string][]string
	writeOnCall map[string]int
}

func (r *summaryRecoveryRunner) RunPlanning(context.Context, string, string, string, string) error {
	return nil
}

func (r *summaryRecoveryRunner) RunJSONQuery(context.Context, string) ([]byte, error) {
	return nil, nil
}

func (r *summaryRecoveryRunner) RunAgent(_ context.Context, _, _, prompt, logFile string) error {
	stageDir := filepath.Dir(logFile)
	id := filepath.Base(stageDir)
	r.mu.Lock()
	r.calls[id]++
	n := r.calls[id]
	r.prompts[id] = append(r.prompts[id], prompt)
	r.mu.Unlock()
	if n == r.writeOnCall[id] {
		return os.WriteFile(filepath.Join(stageDir, "execution_summary.md"), []byte("done\n"), 0o644)
	}
	return nil
}

// TestSummaryRecovery_RunCompletesWithMaxParallelOne — полный Run при max_parallel:1: recovery идёт в том же
// слоте автора (без дедлока и утечки слота), зависимая стадия стартует один раз и только после summary.
func TestSummaryRecovery_RunCompletesWithMaxParallelOne(t *testing.T) {
	stages := []flow.Stage{
		{ID: "author", Name: "Author", Agents: []flow.AgentType{flow.AgentAuto}, MaxParallel: 1},
		{ID: "next", Name: "Next", Agents: []flow.AgentType{flow.AgentAuto}, MaxParallel: 1, DependsOn: []string{"author"}},
	}
	runDir := t.TempDir()
	store, err := state.Open(runDir, []string{"author", "next"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	runner := &summaryRecoveryRunner{
		calls: map[string]int{}, prompts: map[string][]string{},
		writeOnCall: map[string]int{"author": 2, "next": 1}, // author: summary только на recovery 1/2
	}
	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir, Stages: stages, Store: store, Config: config.Default(),
		Prompts: orchestrator.DefaultPrompts(), Runner: runner,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var runErr error
	done := make(chan struct{})
	go func() { runErr = orch.Run(ctx); close(done) }()
	t.Cleanup(func() { // LIFO: сначала дождаться Run, потом закрыть Store
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("orch.Run did not return within 10s of cancellation")
		}
	})

	select {
	case <-done:
		if runErr != nil {
			t.Fatalf("Run: %v", runErr)
		}
	case <-time.After(25 * time.Second):
		t.Fatal("Run did not finish: recovery deadlocked or the successor never started")
	}

	if got := runner.calls["author"]; got != 2 {
		t.Errorf("author calls = %d, want 2", got)
	}
	if got := runner.calls["next"]; got != 1 {
		t.Errorf("successor calls = %d, want exactly 1", got)
	}
	if !strings.Contains(runner.prompts["author"][1], "(1/2)") || strings.Contains(runner.prompts["author"][0], "Execution summary recovery") {
		t.Error("only the second author call carries the 1/2 recovery instruction")
	}
	for _, id := range []string{"author", "next"} {
		if got := store.Get(id); got != state.StatusDone {
			t.Errorf("stage %s = %s, want done", id, got)
		}
	}
	if got := store.Stage("author").SummaryRecoveryUsed; got != 1 {
		t.Errorf("author used = %d, want 1", got)
	}
}
