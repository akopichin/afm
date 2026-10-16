package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/concurrency"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
	"github.com/akopichin/afm/pkg/state"
)

// ctxCapturingRunner is a fake executor.Runner that records the ctx passed to RunAgent and signals gotCtx.
type ctxCapturingRunner struct {
	gotCtx chan context.Context
}

func (r *ctxCapturingRunner) RunAgent(ctx context.Context, _, _, _, logFile string) error {
	_ = os.WriteFile(filepath.Join(filepath.Dir(logFile), ".done"), []byte("ok"), 0644)
	r.gotCtx <- ctx
	return nil
}

func (r *ctxCapturingRunner) RunPlanning(_ context.Context, _, _, outFile, _ string) error {
	return os.WriteFile(outFile, []byte("## Tasks\n- t\n## Assumptions\n- a\n## Acceptance Criteria\n- c\n"), 0644)
}

func (r *ctxCapturingRunner) RunJSONQuery(_ context.Context, _ string) ([]byte, error) {
	return nil, nil
}

func TestApprove_SpawnsAgentUnderRunCtx_NotRequestCtx(t *testing.T) {
	dir := t.TempDir()
	store, err := state.Open(dir, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	stages := []flow.Stage{{ID: "a", Agents: []flow.AgentType{flow.AgentImplementation}}}
	runner := &ctxCapturingRunner{gotCtx: make(chan context.Context, 1)}

	// runCtx simulates the long-lived ctx that Run() stashes right after
	// context.WithCancel(ctx) — independent from any per-request ctx.
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()

	cb := bus.NewCriticalBus(16)
	o := &Orchestrator{
		opts:        Options{RunDir: dir, Stages: stages, Store: store, Runner: runner},
		graph:       graph.NewRegistry(stages),
		runner:      runner,
		fsm:         bus.NewFSM(store),
		ui:          bus.NewUIBus(),
		critical:    cb,
		concurrency: concurrency.NewWithSemaphores(cb, map[string]concurrency.Semaphore{}, ""),
		runCtx:      runCtx,
	}

	stageDir := filepath.Join(dir, "a")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# plan"), 0644); err != nil {
		t.Fatal(err)
	}

	// Drive stage "a" to awaiting_approval, same as the dashboard would see it.
	o.Trigger("a", bus.EvStartPlanning, bus.GuardCtx{}, "")
	o.Trigger("a", bus.EvPlanReady, bus.GuardCtx{}, "")

	// reqCtx simulates the HTTP request ctx handleApprove passes as r.Context().
	reqCtx, reqCancel := context.WithCancel(context.Background())
	if err := o.Approve(reqCtx, "a"); err != nil {
		t.Fatal(err)
	}
	// Simulate the handler returning its response: net/http cancels r.Context()
	// right away.
	reqCancel()

	select {
	case gotCtx := <-runner.gotCtx:
		if gotCtx.Err() != nil {
			t.Fatalf("RunAgent ctx was canceled after request ctx canceled: %v — "+
				"agent was spawned under the request ctx instead of the run ctx", gotCtx.Err())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for RunAgent to be invoked")
	}

	o.concurrency.WaitAgents()
}
