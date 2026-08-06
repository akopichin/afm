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

// approveStage долговечно переводит impl-стадию awaiting_approval → ready
// (запись в Store фиксируется до возврата), чтобы краш после approve не терял интент.
//
// Стадия "a" зависит от "b" (которая осталась pending), поэтому
// startReadyStages внутри approveStage не подхватывает "a" и не продвигает
// её дальше в running — это позволяет наблюдать именно persisted-статус
// ready, не гоняясь за состоянием фонового агента.
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
		graph:       graph.NewGraph(stages),
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

// noopPlanningRunner — минимальный executor.Runner для теста Revise: пишет
// валидный plan.md (с обязательными секциями), чтобы агентская горутина,
// запущенная Revise через spawnAgent, корректно завершилась без реального
// вызова claude (иначе r.RunPlanning на неинициализированном o.runner
// паникует нулевым указателем).
type noopPlanningRunner struct{}

func (noopPlanningRunner) RunPlanning(_ context.Context, _, _, outFile, _ string) error {
	return os.WriteFile(outFile, []byte("## Tasks\n- t\n## Assumptions\n- a\n## Acceptance Criteria\n- c\n"), 0644)
}

func (noopPlanningRunner) RunAgent(_ context.Context, _, _, _, _ string) error { return nil }

func (noopPlanningRunner) RunJSONQuery(_ context.Context, _ string) ([]byte, error) { return nil, nil }

// TestRevise_DurableTransition проверяет, что Revise фиксирует переход
// в state.StatusRevising в логе синхронно (до возврата из вызова) — краш
// сразу после Revise не теряет интент на переплан.
//
// Фоновая горутина, которую Revise запускает через concurrency.Manager.SpawnAgent
// (runPlanningWithFeedback), сама первым делом делает
// Trigger(EvStartPlanning) — а это валидный переход ИЗ revising (см.
// fsm.go: EvStartPlanning{From: ...,StatusRevising}), так что без
// синхронизации она гоняется с последующим LoadRunState в тесте: получаем
// то "revising", то уже "planning" в зависимости от планировщика
// (флейково воспроизводится через `-race -count`). Семафор команды "" (test
// sems map) держит SpawnAgent на sem.acquire() ДО запуска run(), пока тест
// не прочитает состояние и не отпустит его явно — тем самым тест проверяет
// именно гарантию Revise (запись в Store до возврата), а не выигрыш в гонке
// с фоновым агентом.
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
		opts:        Options{RunDir: dir, Stages: stages, Store: store},
		graph:       graph.NewGraph(stages),
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

	if err := o.Revise(context.Background(), "a", "нужны правки"); err != nil {
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
