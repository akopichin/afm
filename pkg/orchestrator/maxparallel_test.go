package orchestrator_test

// TestMaxParallelThrottling закрывает пробел покрытия: stage.MaxParallel
// (per-command семафор, см. pkg/orchestrator/concurrency.go semFor и
// New()'s sems-построение в orchestrator.go) ограничивает число ОДНОВРЕМЕННО
// исполняющихся стадий, разделяющих одну команду агента.
//
// Модель: N независимых (без DependsOn) auto-стадий (Agents: [flow.AgentAuto])
// с общей (пустой, т.е. дефолтной) командой и MaxParallel=2 на каждой стадии —
// New берёт САМЫЙ строгий MaxParallel среди всех стадий с данной эффективной
// командой (см. orchestrator.go: сбор limits[cmd] = min(...) перед построением
// семафоров); здесь все стадии задают одно и то же значение, так что тест не
// различает "строже из всех" от прежнего "первая встретившаяся" — про это
// различие см. TestMaxParallelThrottling_NonFirstStageLimitApplies ниже.
// auto-стадии не требуют planning.md
// (runAutonomousAgent, agents.go) — все они становятся Ready в одном проходе
// startPlanningForPending→startReadyStages при старте Run и спавнятся
// параллельно (по одной горутине на стадию), так что тест реально бьётся
// за общий семафор, а не исполняется последовательно по другой причине.
//
// Детерминизм без sleep-as-sync: blockingAutoRunner.RunAgent сигнализирует
// вход через небуферизованный acquired-канал и блокируется на release-канале,
// пока тест явно не отпустит ровно нужное число вызовов. Единственный таймер
// в тесте — короткое окно "убедиться, что НИКТО ТРЕТИЙ не вошёл, пока держим
// первых двух" (защитная проверка верхней границы, а не механизм синхронизации
// прогресса: семафор физически не пускает acquire() сверх ёмкости раньше
// release, поэтому этот вызов detect'ит нарушение, а не ждёт прогресса).
import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/state"
)

// blockingAutoRunner — mock executor.Runner для стадий flow.AgentAuto
// (runAutonomousAgent вызывает RunAgent с agentType "autonomous_execution").
// Каждый вызов RunAgent: увеличивает inFlight, публикует свой stageDir на
// acquired (небуферизованный — приёмник читает ровно по одному входу за раз),
// блокируется на release, затем пишет execution_summary.md (completion-
// артефакт, см. checkAutonomousCompletion) и уменьшает inFlight.
type blockingAutoRunner struct {
	acquired chan string   // сигнал входа (stageDir) — читается тестом
	release  chan struct{} // тест шлёт по одному сигналу на каждый выпускаемый вызов

	inFlight int64 // atomic: текущее число заблокированных в RunAgent вызовов
	maxSeen  int64 // atomic: максимум, когда-либо достигнутый inFlight
}

func newBlockingAutoRunner(nStages int) *blockingAutoRunner {
	return &blockingAutoRunner{
		acquired: make(chan string, nStages),
		release:  make(chan struct{}),
	}
}

func (r *blockingAutoRunner) RunPlanning(_ context.Context, _, _, outFile, _ string) error {
	// auto-стадии не проходят planning — не должно вызываться, но на всякий
	// случай пишем валидный plan.md, чтобы тест не завис на несвязанной причине.
	return os.WriteFile(outFile, []byte("## Tasks\n- x\n"), 0644)
}

func (r *blockingAutoRunner) RunJSONQuery(_ context.Context, _ string) ([]byte, error) {
	return nil, nil
}

func (r *blockingAutoRunner) RunAgent(ctx context.Context, _, _, _, logFile string) error {
	stageDir := filepath.Dir(logFile)

	n := atomic.AddInt64(&r.inFlight, 1)
	for {
		old := atomic.LoadInt64(&r.maxSeen)
		if n <= old || atomic.CompareAndSwapInt64(&r.maxSeen, old, n) {
			break
		}
	}

	r.acquired <- stageDir

	select {
	case <-r.release:
	case <-ctx.Done():
		atomic.AddInt64(&r.inFlight, -1)
		return ctx.Err()
	}

	atomic.AddInt64(&r.inFlight, -1)
	return os.WriteFile(filepath.Join(stageDir, "execution_summary.md"), []byte("## Summary\ndone\n"), 0644)
}

func TestMaxParallelThrottling(t *testing.T) {
	const (
		nStages     = 5
		maxParallel = 2
	)

	stages := make([]flow.Stage, nStages)
	for i := range stages {
		stages[i] = flow.Stage{
			ID:          fmt.Sprintf("auto%d", i),
			Name:        fmt.Sprintf("Auto %d", i),
			Agents:      []flow.AgentType{flow.AgentAuto},
			MaxParallel: maxParallel,
		}
	}

	runDir := t.TempDir()
	ids := make([]string, nStages)
	for i, s := range stages {
		ids[i] = s.ID
	}
	store, err := state.Open(runDir, ids)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	runner := newBlockingAutoRunner(nStages)

	orch := orchestrator.New(orchestrator.Options{
		RunDir:  runDir,
		Stages:  stages,
		Store:   store,
		Config:  config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
		Runner:  runner,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- orch.Run(ctx) }()

	// Три волны: 2 + 2 + 1 = 5 стадий, ёмкость семафора = 2.
	remaining := nStages
	for remaining > 0 {
		waveSize := maxParallel
		if remaining < waveSize {
			waveSize = remaining
		}

		for i := 0; i < waveSize; i++ {
			select {
			case <-runner.acquired:
			case <-time.After(10 * time.Second):
				t.Fatalf("timed out waiting for wave entry %d/%d (remaining=%d)", i+1, waveSize, remaining)
			}
		}

		// Защитная проверка верхней границы: пока держим текущую волну
		// заблокированной (никто не released), НИКТО ЕЩЁ не должен был войти
		// сверх ёмкости семафора — короткое окно ожидания лишнего входа, а не
		// синхронизация прогресса (semFor.acquire() физически блокирует
		// сверхкомплектные горутины до release, так что "тишина" здесь —
		// прямое следствие throttling, а не гонка со временем).
		select {
		case extra := <-runner.acquired:
			t.Fatalf("stage %q entered RunAgent while semaphore should have been full (wave size %d)", extra, waveSize)
		case <-time.After(200 * time.Millisecond):
		}

		if got := atomic.LoadInt64(&runner.inFlight); got != int64(waveSize) {
			t.Errorf("wave with %d entries: inFlight = %d, want %d", waveSize, got, waveSize)
		}

		for i := 0; i < waveSize; i++ {
			runner.release <- struct{}{}
		}

		remaining -= waveSize
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("orch.Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("orch.Run did not finish after releasing all stages")
	}

	final := store.Snapshot()
	for _, id := range ids {
		if got := final.Stages[id].Status; got != state.StatusDone {
			t.Errorf("stage %s: status = %s, want %s", id, got, state.StatusDone)
		}
	}

	if maxSeen := atomic.LoadInt64(&runner.maxSeen); maxSeen != int64(maxParallel) {
		t.Errorf("max observed concurrency = %d, want exactly %d (MaxParallel)", maxSeen, maxParallel)
	}
}

// TestMaxParallelThrottling_NonFirstStageLimitApplies воспроизводит найденный
// при аудите баг: New строил per-command семафор из MaxParallel ПЕРВОЙ
// стадии, встретившей эффективную команду ("if _, exists := sems[cmd];
// exists { continue }") — MaxParallel любой последующей стадии с той же
// командой молча игнорировался. Здесь первая стадия ("loose") не задаёт
// MaxParallel вовсе (а глобальный Executor.MaxParallel тоже 0 — безлимит),
// а вторая и третья ("strict1"/"strict2") явно просят MaxParallel: 1. До
// фикса это давало неограниченный параллелизм (первая стадия "выигрывала"
// пустым лимитом); после фикса действует самый строгий лимит среди всех
// стадий команды — не более 1 одновременно, независимо от порядка в YAML.
func TestMaxParallelThrottling_NonFirstStageLimitApplies(t *testing.T) {
	stages := []flow.Stage{
		{ID: "loose", Name: "Loose", Agents: []flow.AgentType{flow.AgentAuto}},
		{ID: "strict1", Name: "Strict 1", Agents: []flow.AgentType{flow.AgentAuto}, MaxParallel: 1},
		{ID: "strict2", Name: "Strict 2", Agents: []flow.AgentType{flow.AgentAuto}, MaxParallel: 1},
	}

	runDir := t.TempDir()
	ids := []string{"loose", "strict1", "strict2"}
	store, err := state.Open(runDir, ids)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	runner := newBlockingAutoRunner(len(stages))

	orch := orchestrator.New(orchestrator.Options{
		RunDir:  runDir,
		Stages:  stages,
		Store:   store,
		Config:  config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
		Runner:  runner,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- orch.Run(ctx) }()

	// Три волны по 1 стадии — если баг вернётся, вторая волна не будет
	// заблокирована первой и придёт больше одного входа сразу.
	for i := 0; i < len(stages); i++ {
		select {
		case <-runner.acquired:
		case <-time.After(10 * time.Second):
			t.Fatalf("timed out waiting for wave entry %d/%d", i+1, len(stages))
		}

		select {
		case extra := <-runner.acquired:
			t.Fatalf("stage %q entered RunAgent while limit=1 semaphore should have been full", extra)
		case <-time.After(200 * time.Millisecond):
		}

		runner.release <- struct{}{}
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("orch.Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("orch.Run did not finish after releasing all stages")
	}

	if maxSeen := atomic.LoadInt64(&runner.maxSeen); maxSeen != 1 {
		t.Errorf("max observed concurrency = %d, want exactly 1 (strict1/strict2's MaxParallel must apply even though \"loose\" is first)", maxSeen)
	}
}
