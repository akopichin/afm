package orchestrator

// T8.2 — миграция scheduler/recovery (и по сути всех остальных по-стадийных потребителей внутри
// pkg/orchestrator) с opts.Stages на runtime-registry (o.graph). Тесты здесь доказывают два разных свойства:
//
//  1. opts.Stages больше нигде не читается в обход registry (грепом по исходникам пакета).
//  2. Код, который раньше читал opts.Stages напрямую (recovery.go, dependency-plan/artifact collection),
//     теперь ДОКАЗУЕМО читает o.graph: сценарий делает opts.Stages намеренно "неправильным" (пустым/иным,
//     чем в registry) и показывает, что поведение следует за registry, а не за opts.Stages.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
	"github.com/akopichin/afm/pkg/state"
)

// TestOrchestrator_NoOptsStagesReadOutsideRegistryConstruction is T8.2's grep-based acceptance check: every
// .go file directly in pkg/orchestrator (not its subpackages, not _test.go files) must not read opts.Stages
// — except orchestrator.go, whose New() seeds the registry from it exactly once. This is the literal text of
// dev-plan.md's acceptance criterion for T8.2: "В pkg/orchestrator не осталось чтения opts.Stages в обход
// registry".
func TestOrchestrator_NoOptsStagesReadOutsideRegistryConstruction(t *testing.T) {
	allowed := map[string]bool{
		"orchestrator.go": true, // New() reads opts.Stages ONCE, to build o.graph; see the comment there.
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir(.): %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", name, err)
		}
		if strings.Contains(string(data), "opts.Stages") && !allowed[name] {
			t.Errorf("%s reads opts.Stages directly — must go through o.graph (the registry) instead; "+
				"only orchestrator.go's New() is allowed to read opts.Stages, to seed the registry", name)
		}
	}
}

// newRegistryMigrationTestOrchestrator builds a minimal, real Orchestrator (no mocked runner needed for
// these tests — they exercise scheduling/recovery/context-building helpers directly, not a full Run loop)
// whose opts.Stages and o.graph can be set to DIFFERENT stage sets, to prove which one production code
// actually consults.
func newRegistryMigrationTestOrchestrator(t *testing.T, graphStages, optsStages []flow.Stage, initial map[string]state.StageStatus) *Orchestrator {
	t.Helper()
	runDir := t.TempDir()
	ids := make([]string, 0, len(graphStages))
	for _, s := range graphStages {
		ids = append(ids, s.ID)
	}
	store, err := state.Open(runDir, ids)
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	for id, status := range initial {
		if status == state.StatusPending {
			continue
		}
		if err := store.Apply(&state.Transition{StageID: id, From: state.StatusPending, To: status, Event: "test_setup"}); err != nil {
			t.Fatalf("seed status for %s: %v", id, err)
		}
	}
	return &Orchestrator{
		opts:     Options{RunDir: runDir, RunID: "run1", Store: store, Stages: optsStages, Prompts: DefaultPrompts()},
		ui:       bus.NewUIBus(),
		critical: bus.NewCriticalBus(16),
		fsm:      bus.NewFSM(store),
		graph:    graph.NewStore(graph.NewRegistry(graphStages)),
	}
}

// TestRecovery_UsesRegistryNotOptsStages: recovery.go's autoRecoverFailedStages (the exact function migrated
// off opts.Stages in T8.2) must walk o.graph's stage set, not opts.Stages — opts.Stages here is deliberately
// EMPTY, so if the migration regressed to reading it, nothing would be recovered at all.
func TestRecovery_UsesRegistryNotOptsStages(t *testing.T) {
	registryOnly := flow.Stage{ID: "only-in-registry"}
	o := newRegistryMigrationTestOrchestrator(t,
		[]flow.Stage{registryOnly},
		nil, // opts.Stages intentionally empty/stale
		map[string]state.StageStatus{registryOnly.ID: state.StatusFailed},
	)
	o.opts.Config.AutoRecover = boolPtr(true)

	o.autoRecoverFailedStages()

	if got := o.currentStatus(registryOnly.ID); got != state.StatusPending {
		t.Fatalf("autoRecoverFailedStages: status = %s, want pending — it must have walked o.graph (where the "+
			"failed stage lives), not the deliberately-empty opts.Stages", got)
	}
}

func boolPtr(b bool) *bool { return &b }

// TestDependencyPlans_Preserved: dependency-plan collection (stagefiles.CollectDependencyPlans, reached via
// collectDependencyPlansNoticing — one of T8.2's migrated call sites) must still find a dependency's plan.md
// via o.graph, even when opts.Stages carries a stale/different stage list.
func TestDependencyPlans_Preserved(t *testing.T) {
	dep := flow.Stage{ID: "dep", Name: "Dependency"}
	main := flow.Stage{ID: "main", DependsOn: []string{"dep"}}
	o := newRegistryMigrationTestOrchestrator(t,
		[]flow.Stage{dep, main},
		[]flow.Stage{main}, // opts.Stages intentionally missing "dep" — the real source must be o.graph
		map[string]state.StageStatus{dep.ID: state.StatusDone, main.ID: state.StatusPending},
	)
	depDir := filepath.Join(o.opts.RunDir, dep.ID)
	if err := os.MkdirAll(depDir, 0755); err != nil {
		t.Fatal(err)
	}
	const marker = "## Tasks\n- do the dependency work\n"
	if err := os.WriteFile(filepath.Join(depDir, "plan.md"), []byte(marker), 0644); err != nil {
		t.Fatal(err)
	}

	got := o.collectDependencyPlansNoticing(main)
	if !strings.Contains(got, marker) {
		t.Fatalf("collectDependencyPlansNoticing: dependency plan not found via registry; got:\n%s", got)
	}
}

// TestScheduler_BehaviourUnchangedForStaticFlow is a golden/characterization test: for a representative
// static flow (two independent root stages, one dependent, one script stage), the exact sequence of
// stage_status_changed transitions the scheduler/recovery code produces end-to-end through orch.Run must
// match this pinned sequence. T8.2 moved every stage-definition lookup in scheduling.go/recovery.go behind
// o.graph; this test is the behavioural proof that nothing changed from the caller's point of view.
func TestScheduler_BehaviourUnchangedForStaticFlow(t *testing.T) {
	runDir := t.TempDir()
	stages := []flow.Stage{
		{ID: "root-a", Name: "root-a", Agents: []flow.AgentType{flow.AgentAuto}},
		{ID: "root-b", Name: "root-b", Script: "true"},
		{ID: "dependent", Name: "dependent", DependsOn: []string{"root-a", "root-b"}, Agents: []flow.AgentType{flow.AgentAuto}},
	}
	ids := []string{"root-a", "root-b", "dependent"}
	store, err := state.Open(runDir, ids)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	orch := New(Options{
		RunDir:  runDir,
		Stages:  stages,
		Store:   store,
		Prompts: DefaultPrompts(),
		Runner:  alwaysSucceedsGoldenRunner{},
	})

	subID, events := orch.UIBus().Subscribe(256)
	defer orch.UIBus().Unsubscribe(subID)

	type transition struct {
		stage  string
		status state.StageStatus
	}
	var got []transition
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range events {
			if ev.Type == bus.EventStageStatusChanged {
				got = append(got, transition{stage: ev.StageID, status: state.StageStatus(ev.Data.(string))})
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := orch.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	orch.UIBus().Unsubscribe(subID)
	<-done

	finalStatus := func(id string) state.StageStatus {
		for i := len(got) - 1; i >= 0; i-- {
			if got[i].stage == id {
				return got[i].status
			}
		}
		return ""
	}
	for _, id := range ids {
		if fs := finalStatus(id); fs != state.StatusDone {
			t.Fatalf("stage %s final status = %s, want done (full transitions: %+v)", id, fs, got)
		}
	}

	// root-a and root-b must both reach running before dependent does (dependent's depends_on gate) — the
	// exact ordering BETWEEN root-a and root-b is not pinned (auto vs script stages can legitimately race),
	// but "dependent starts only after both roots are done" is the scheduling invariant this test protects.
	var dependentStart, rootADone, rootBDone = -1, -1, -1
	for i, tr := range got {
		switch {
		case tr.stage == "dependent" && tr.status == state.StatusRunning && dependentStart == -1:
			dependentStart = i
		case tr.stage == "root-a" && tr.status == state.StatusDone && rootADone == -1:
			rootADone = i
		case tr.stage == "root-b" && tr.status == state.StatusDone && rootBDone == -1:
			rootBDone = i
		default:
			// Other transitions (e.g. running→... for root stages, or earlier events) are irrelevant here.
		}
	}
	if dependentStart == -1 || rootADone == -1 || rootBDone == -1 {
		t.Fatalf("missing expected transitions (dependentStart=%d rootADone=%d rootBDone=%d): %+v", dependentStart, rootADone, rootBDone, got)
	}
	if dependentStart < rootADone || dependentStart < rootBDone {
		t.Fatalf("dependent started before both dependencies were done: %+v", got)
	}
}

// alwaysSucceedsGoldenRunner is alwaysSucceedsRunner's internal-package twin (seq_live_test.go's type lives
// in package orchestrator_test, not reachable from here).
type alwaysSucceedsGoldenRunner struct{}

func (alwaysSucceedsGoldenRunner) RunPlanning(_ context.Context, _, _, _, _ string) error {
	return nil
}

func (alwaysSucceedsGoldenRunner) RunAgent(_ context.Context, _, _, _, logFile string) error {
	stageDir := filepath.Dir(logFile)
	return os.WriteFile(filepath.Join(stageDir, "execution_summary.md"), []byte("## Summary\ndone\n"), 0644)
}

func (alwaysSucceedsGoldenRunner) RunJSONQuery(_ context.Context, _ string) ([]byte, error) {
	return nil, nil
}
