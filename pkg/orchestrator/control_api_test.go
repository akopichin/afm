package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/concurrency"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
	"github.com/akopichin/afm/pkg/state"
)

func TestCancelDialog_FailsStage(t *testing.T) {
	dir := t.TempDir()
	store, err := state.Open(dir, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	stages := []flow.Stage{{ID: "a", Agents: []flow.AgentType{flow.AgentImplementation}}}
	cb := bus.NewCriticalBus(16)
	o := &Orchestrator{
		opts:        Options{RunDir: dir, Stages: stages, Store: store},
		graph:       graph.NewGraph(stages),
		fsm:         bus.NewFSM(store),
		ui:          bus.NewUIBus(),
		critical:    cb,
		concurrency: concurrency.NewWithSemaphores(cb, map[string]concurrency.Semaphore{}, ""),
	}
	o.Trigger("a", bus.EvStartPlanning, bus.GuardCtx{}, "")
	o.Trigger("a", bus.EvAskUser, bus.GuardCtx{}, "")

	if err := o.CancelDialog("a"); err != nil {
		t.Fatalf("CancelDialog returned error: %v", err)
	}

	rs, err := state.LoadRunState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if rs.Stages["a"].Status != state.StatusFailed {
		t.Fatalf("status = %q, want failed", rs.Stages["a"].Status)
	}
}

func TestRetryHook_NoWaiterReturnsError(t *testing.T) {
	o := &Orchestrator{}
	if err := o.RetryHook("nonexistent"); err == nil {
		t.Error("expected error when no stage is waiting on a hook decision")
	}
}

func TestSkipHook_DeliversDecision(t *testing.T) {
	o := &Orchestrator{}
	done := make(chan hookDecision, 1)
	go func() {
		d, _ := o.waitForHookDecision(context.Background(), "s1")
		done <- d
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := o.SkipHook("s1"); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case d := <-done:
		if d != hookDecisionSkip {
			t.Errorf("decision = %v, want skip", d)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out")
	}
}
