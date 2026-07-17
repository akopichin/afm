package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

// ctxCapturingRunner is a fake executor.Runner that records the ctx passed to
// RunAgent and signals gotCtx. It also drops a .done file next to logFile so
// runWithRetry's completion check passes on the first attempt (no retry loop,
// no leaked goroutine trying to resend on the buffered channel).
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

// TestApprove_SpawnsAgentUnderRunCtx_NotRequestCtx is the regression lock-in
// for the CRITICAL bug: HTTP-initiated Approve must spawn the implementation
// agent under the long-lived orchestrator run ctx, NOT the HTTP request ctx.
// net/http cancels the request ctx as soon as the handler returns its
// response — if spawnAgent inherited that ctx, exec.CommandContext would kill
// the just-started agent process immediately (before it can do any work).
//
// Simulates Run() having already set runCtx (as it does right after
// context.WithCancel in Run), then calls Approve with a separate, cancelable
// request ctx and cancels it right after Approve returns — mirroring a
// handler returning its HTTP response. The fake runner's RunAgent must still
// observe a live (non-Done) ctx.
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

	o := &Orchestrator{
		opts:     Options{RunDir: dir, Stages: stages, Store: store, Runner: runner},
		graph:    NewGraph(stages),
		runner:   runner,
		fsm:      NewFSM(store),
		ui:       NewUIBus(),
		critical: NewCriticalBus(16),
		sems: map[string]interface {
			acquire()
			release()
		}{},
		runCtx: runCtx,
	}

	stageDir := filepath.Join(dir, "a")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# plan"), 0644); err != nil {
		t.Fatal(err)
	}

	// Drive stage "a" to awaiting_approval, same as the dashboard would see it.
	o.Trigger("a", EvStartPlanning, GuardCtx{}, "")
	o.Trigger("a", EvPlanReady, GuardCtx{}, "")

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

	o.waitAgents()
}
