package orchestrator_test

// Task 13: полный Phase-1 контур end-to-end — настоящие процессы sh,
// настоящий dispatcher (не тестовый RunFunc-стаб), хук дописывает каждое
// событие в файл через `cat >> capture`. Проверка идёт по фактическому
// содержимому файла на диске, а не по in-memory рекордеру
// (flowEventRecorder из lifecycle_flow_test.go) — этот тест страхует
// именно ПРОДАКШЕН-путь (реальный Dispatcher.Start/Emit/Flush/Stop,
// реальный runCommand), который run.go собирает из слоёв config+flow+stage
// (см. cmd/afm/run.go, Task 13). Хелперы взяты из соседних интеграционных
// тестов пакета (integration_test.go/integration_hooks_test.go): flow.Stage
// строится Go-литералом, store через state.Open, orchestrator.New с
// Hooks:disp, ожидание завершения через waitForStatus + ручной cancel/<-runDone
// (тот же паттерн, что integration_hooks_test.go, а не отдельный внешний
// хелпер newRunStoreForTest/newOrchestratorForFlow — в пакете он не
// существовал, а заводить его специально под один тест избыточно).

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/lifecyclehooks"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/state"
)

func TestIntegration_LifecycleHooks_EndToEnd(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "captured.events")

	stages := []flow.Stage{{
		ID:     "s1",
		Script: "true",
		Hooks: []lifecyclehooks.Hook{{
			ID:      "capture-stage",
			Events:  lifecyclehooks.EventSelector{Events: []lifecyclehooks.EventType{lifecyclehooks.EventStageScriptStarted, lifecyclehooks.EventStageScriptFinished}},
			Command: "sh -c 'cat >> " + capture + "'",
		}},
	}}
	flowHooks := []lifecyclehooks.Hook{{
		ID:      "capture-flow",
		Events:  lifecyclehooks.EventSelector{Events: []lifecyclehooks.EventType{lifecyclehooks.EventFlowStarted, lifecyclehooks.EventFlowFinished, lifecyclehooks.EventStageFinished}},
		Command: "sh -c 'cat >> " + capture + "'",
	}}

	runDir := t.TempDir()
	store, err := state.Open(runDir, []string{"s1"})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	disp := lifecyclehooks.New(lifecyclehooks.DispatcherOptions{
		Config: lifecyclehooks.DispatcherConfig{
			FlowName: "hooks-e2e", RunID: "hooks-e2e-run", RunDir: runDir, RootDir: dir,
		},
		Hooks: lifecyclehooks.Combine(
			lifecyclehooks.Layer{Hooks: flowHooks},
			lifecyclehooks.Layer{StageID: "s1", Hooks: stages[0].Hooks},
		),
		LogDir: filepath.Join(runDir, "hooks"),
	})
	disp.Start()
	t.Cleanup(disp.Stop)

	orch := orchestrator.New(orchestrator.Options{
		RunDir:   runDir,
		RootDir:  dir,
		Stages:   stages,
		Store:    store,
		Config:   config.Default(),
		Prompts:  orchestrator.DefaultPrompts(),
		Hooks:    disp,
		FlowName: "hooks-e2e",
		RunID:    "hooks-e2e-run",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- orch.Run(ctx) }()

	stateFile := filepath.Join(runDir, "state.json")
	waitForStatus(t, stateFile, "s1", state.StatusDone, 20*time.Second)
	cancel()
	<-runDone

	// bounded flush, тот же контракт, что unified defer в cmd/afm/run.go.
	if !disp.Flush(lifecyclehooks.FlushTimeout) {
		t.Fatal("dispatcher flush timed out")
	}

	raw, err := os.ReadFile(capture)
	if err != nil {
		t.Fatalf("capture file: %v", err)
	}
	body := string(raw)
	for _, want := range []string{
		`"event":"flow_started"`,
		`"schema_version":1`,
		`"event":"stage_script_started"`,
		`"event":"stage_script_finished"`,
		`"event":"stage_finished"`,
		`"event":"flow_finished"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("capture missing %s\nbody:\n%s", want, body)
		}
	}
	if strings.Contains(body, `"event":"stage_script_started"`) && !strings.Contains(body, `"stage":{"id":"s1"`) {
		t.Errorf("stage-scoped hook must carry stage block\n%s", body)
	}

	// логи хуков на месте
	for _, id := range []string{"capture-flow", "capture-stage"} {
		if _, err := os.Stat(filepath.Join(runDir, "hooks", id+".log")); err != nil {
			t.Errorf("hook log %s: %v", id, err)
		}
	}
}
