package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// startupCountRunner holds runners until startup dispatch has finished. Releasing them and draining the
// Manager makes duplicate-start assertions deterministic, including executors still queued at dispatch.
type startupCountRunner struct {
	mu       sync.Mutex
	planning map[string]int
	agent    map[string]int
	release  chan struct{}
	once     sync.Once
}

func newStartupCountRunner() *startupCountRunner {
	return &startupCountRunner{planning: map[string]int{}, agent: map[string]int{}, release: make(chan struct{})}
}

func (r *startupCountRunner) RunPlanning(ctx context.Context, stageName, _, outFile, _ string) error {
	r.mu.Lock()
	r.planning[stageName]++
	r.mu.Unlock()
	select {
	case <-r.release:
		return os.WriteFile(outFile, []byte("## Tasks\nDo work\n## Assumptions\nNone\n## Acceptance Criteria\nDone\n"), 0o644)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *startupCountRunner) RunAgent(ctx context.Context, _, stageName, _, logFile string) error {
	r.mu.Lock()
	r.agent[stageName]++
	r.mu.Unlock()
	select {
	case <-r.release:
		for _, name := range []string{"execution_summary.md", ".done"} {
			if err := os.WriteFile(filepath.Join(filepath.Dir(logFile), name), []byte("done\n"), 0o644); err != nil {
				return err
			}
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *startupCountRunner) RunJSONQuery(context.Context, string) ([]byte, error) { return nil, nil }

func (r *startupCountRunner) count(m map[string]int, name string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return m[name]
}

func (r *startupCountRunner) drain(o *Orchestrator) {
	r.once.Do(func() { close(r.release) })
	o.concurrency.WaitAgents()
}

// startupBarrierOrch открывает Store с заданными стартовыми статусами и собирает оркестратор с runner.
func startupBarrierOrch(t *testing.T, stages []flow.Stage, statuses map[string]state.StageStatus, runner *startupCountRunner) (*Orchestrator, string) {
	t.Helper()
	runDir := t.TempDir()
	ids := make([]string, 0, len(stages))
	for _, s := range stages {
		ids = append(ids, s.ID)
	}
	store, err := state.Open(runDir, ids)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	for id, to := range statuses {
		if err := store.Apply(&state.Transition{StageID: id, From: state.StatusPending, To: to, Event: "test_setup"}); err != nil {
			t.Fatal(err)
		}
	}
	o := New(Options{
		RunDir: runDir, Stages: stages, Store: store, Config: config.Default(),
		Prompts: DefaultPrompts(), Runner: runner,
	})
	t.Cleanup(func() { runner.drain(o) })
	return o, runDir
}

func writeSummaryFile(t *testing.T, runDir, stageID string) {
	t.Helper()
	dir := filepath.Join(runDir, stageID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "execution_summary.md"), []byte("done\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// stopAgents отменяет контекст и ждёт агентские горутины раньше, чем t.Cleanup закроет Store.
func stopAgents(t *testing.T, o *Orchestrator, cancel context.CancelFunc) {
	t.Helper()
	t.Cleanup(func() {
		cancel()
		o.concurrency.WaitAgents()
	})
}

// TestRecovery_StartupCascadePlanningSuccessorStartsOnce — каскад завершения work переводит зависимую стадию
// с планированием в planning; тот же проход цикла не должен запускать planning-агента второй раз.
func TestRecovery_StartupCascadePlanningSuccessorStartsOnce(t *testing.T) {
	stages := []flow.Stage{
		{ID: "work", Name: "Work", Agents: []flow.AgentType{flow.AgentAuto}},
		{ID: "after", Name: "After", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}, DependsOn: []string{"work"}},
	}
	runner := newStartupCountRunner()
	o, runDir := startupBarrierOrch(t, stages, map[string]state.StageStatus{"work": state.StatusRunning}, runner)
	writeSummaryFile(t, runDir, "work")

	ctx, cancel := context.WithCancel(context.Background())
	stopAgents(t, o, cancel)
	o.startPlanningForPending(ctx)

	runner.drain(o)
	if got := runner.count(runner.planning, "After"); got != 1 {
		t.Errorf("successor planning runs = %d, want exactly 1", got)
	}
}

// TestRecovery_StartupReviseBetweenSnapshotAndLoopStillResumes — стадия b была running на момент снимка, а
// пользователь успел сделать Revise (HTTP-сервер поднят до Run) до того, как цикл дошёл до неё. Статус изменился
// не каскадом, поэтому feedback-агент b обязан стартовать из свежего статуса revising ровно один раз.
func TestRecovery_StartupReviseBetweenSnapshotAndLoopStillResumes(t *testing.T) {
	stages := []flow.Stage{
		{ID: "a", Name: "A", Agents: []flow.AgentType{flow.AgentAuto}},
		{ID: "b", Name: "B", Agents: []flow.AgentType{flow.AgentAuto}},
	}
	runner := newStartupCountRunner()
	o, runDir := startupBarrierOrch(t, stages, map[string]state.StageStatus{
		"a": state.StatusRunning, "b": state.StatusRunning,
	}, runner)
	writeSummaryFile(t, runDir, "a")
	// Последняя сессия b — автономная, значит feedback-рестарт идёт через runAutonomousWithFeedback.
	if err := os.MkdirAll(filepath.Join(runDir, "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "b", phaseAutonomous+".session.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Хук срабатывает внутри завершения a — уже после снимка статусов, но до того, как цикл дойдёт до b.
	fired := false
	o.beforeCompleteHook = func(stageID string) {
		if stageID == "a" {
			fired = true
			if _, ok := o.Trigger("b", bus.EvRevise, bus.GuardCtx{}, "user revise"); !ok {
				t.Error("revise of b was rejected")
			}
			if got := o.currentStatus("b"); got != state.StatusRevising {
				t.Errorf("b before dispatch = %s, want revising", got)
			}
			if got := runner.count(runner.agent, "B"); got != 0 {
				t.Errorf("b ran before Revise: %d starts", got)
			}
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	stopAgents(t, o, cancel)
	o.startPlanningForPending(ctx)

	if !fired {
		t.Fatal("completion hook did not fire: Revise scenario was not exercised")
	}
	runner.drain(o)
	if got := runner.count(runner.agent, "B"); got != 1 {
		t.Errorf("revising stage feedback runs = %d, want exactly 1", got)
	}
}

func TestRecovery_StartupCascadeReadyAndAutoRecoveredSuccessorsStartOnce(t *testing.T) {
	for _, initial := range []state.StageStatus{state.StatusReady, state.StatusFailed} {
		t.Run(string(initial), func(t *testing.T) {
			stages := []flow.Stage{
				{ID: "work", Name: "Work", Agents: []flow.AgentType{flow.AgentAuto}},
				{ID: "after", Name: "After", Agents: []flow.AgentType{flow.AgentAuto}, DependsOn: []string{"work"}},
			}
			runner := newStartupCountRunner()
			o, runDir := startupBarrierOrch(t, stages, map[string]state.StageStatus{
				"work": state.StatusRunning, "after": initial,
			}, runner)
			writeSummaryFile(t, runDir, "work")
			ctx, cancel := context.WithCancel(context.Background())
			stopAgents(t, o, cancel)
			o.startPlanningForPending(ctx)
			runner.drain(o)
			if got := runner.count(runner.agent, "After"); got != 1 {
				t.Errorf("successor runs = %d, want exactly 1", got)
			}
			if got := runner.count(runner.agent, "Work"); got != 0 {
				t.Errorf("recovered predecessor runs = %d, want 0", got)
			}
		})
	}
}

// A new process must still start executors absent from its registry, including failed stages that
// auto_recover resets to pending and interrupted running stages without completion artifacts.
func TestRecovery_StartupUnownedStageStartsOnce(t *testing.T) {
	for _, initial := range []state.StageStatus{state.StatusFailed, state.StatusRunning} {
		t.Run(string(initial), func(t *testing.T) {
			stage := flow.Stage{ID: "work", Name: "Work", Agents: []flow.AgentType{flow.AgentAuto}}
			runner := newStartupCountRunner()
			o, _ := startupBarrierOrch(t, []flow.Stage{stage}, map[string]state.StageStatus{"work": initial}, runner)
			ctx, cancel := context.WithCancel(context.Background())
			stopAgents(t, o, cancel)
			o.startPlanningForPending(ctx)
			runner.drain(o)
			if got := runner.count(runner.agent, "Work"); got != 1 {
				t.Errorf("unowned %s stage runs = %d, want exactly 1", initial, got)
			}
		})
	}
}
