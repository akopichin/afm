package orchestrator

import (
	"context"
	"os"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/concurrency"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
	"github.com/akopichin/afm/pkg/state"
)

// approveStage долговечно переводит impl-стадию awaiting_approval → ready, чтобы краш после approve не
// терял интент.
func TestApproveStage_DurableTransition(t *testing.T) {
	dir := t.TempDir()
	store, err := state.Open(dir, []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	stages := []flow.Stage{
		{ID: "b", Agents: []flow.AgentType{flow.AgentImplementation}},
		{ID: "a", Agents: []flow.AgentType{flow.AgentImplementation}, DependsOn: []string{"b"}},
	}
	cb := bus.NewCriticalBus(16)
	o := &Orchestrator{
		opts:        Options{RunDir: dir, Stages: stages, Store: store},
		graph:       graph.NewRegistry(stages),
		fsm:         bus.NewFSM(store),
		ui:          bus.NewUIBus(),
		critical:    cb,
		concurrency: concurrency.NewWithSemaphores(cb, map[string]concurrency.Semaphore{}, ""),
	}
	// довести "a" до awaiting_approval ("b" остаётся pending, некому её завершить)
	o.Trigger("a", bus.EvStartPlanning, bus.GuardCtx{}, "")
	o.Trigger("a", bus.EvPlanReady, bus.GuardCtx{}, "")

	o.approveStage(context.Background(), "a")

	// перечитываем состояние из лога — оно должно быть ready (долговечно)
	rs, err := state.LoadRunState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if rs.Stages["a"].Status != state.StatusReady {
		t.Fatalf("want ready persisted in log, got %q", rs.Stages["a"].Status)
	}
	store.Close()
}

// noopPlanningRunner пишет валидный plan.md, завершая Revise без реального клиента.
type noopPlanningRunner struct{}

func (noopPlanningRunner) RunPlanning(_ context.Context, _, _, outFile, _ string) error {
	return os.WriteFile(outFile, []byte("## Tasks\n- t\n## Assumptions\n- a\n## Acceptance Criteria\n- c\n"), 0644)
}

func (noopPlanningRunner) RunAgent(_ context.Context, _, _, _, _ string) error { return nil }

func (noopPlanningRunner) RunJSONQuery(_ context.Context, _ string) ([]byte, error) { return nil, nil }

func TestRevise_DurableTransition(t *testing.T) {
	dir := t.TempDir()
	store, err := state.Open(dir, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	stages := []flow.Stage{{ID: "a", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}}}
	cb := bus.NewCriticalBus(16)
	blockSem := concurrency.ChannelSemaphore(make(chan struct{}, 1))
	blockSem <- struct{}{} // занят: следующий acquire() (в SpawnAgent) заблокируется
	o := &Orchestrator{
		// Устанавливаем opts.Runner: иначе runnerFor выберет реальный claude вместо мока.
		opts:        Options{RunDir: dir, Stages: stages, Store: store, Runner: noopPlanningRunner{}},
		graph:       graph.NewRegistry(stages),
		runner:      noopPlanningRunner{},
		fsm:         bus.NewFSM(store),
		ui:          bus.NewUIBus(),
		critical:    cb,
		concurrency: concurrency.NewWithSemaphores(cb, map[string]concurrency.Semaphore{"": blockSem}, ""),
	}
	// подготовить plan.md (revise версионирует его)
	stageDir := dir + "/a"
	_ = os.MkdirAll(stageDir, 0755)
	_ = os.WriteFile(stageDir+"/plan.md", []byte("# plan"), 0644)

	o.Trigger("a", bus.EvStartPlanning, bus.GuardCtx{}, "")
	o.Trigger("a", bus.EvPlanReady, bus.GuardCtx{}, "")

	if _, _, err := o.Revise(context.Background(), "a", "нужны правки"); err != nil {
		t.Fatal(err)
	}
	rs, _ := state.LoadRunState(dir)
	if rs.Stages["a"].Status != state.StatusRevising {
		t.Fatalf("want revising persisted, got %q", rs.Stages["a"].Status)
	}
	<-blockSem // отпускаем фонового агента — теперь он может продолжить (planning → done)
	o.concurrency.WaitAgents()
	store.Close()
}
