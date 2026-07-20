package orchestrator_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/state"
)

// supervisorTestRunner — in-process mock executor.Runner для интеграционных тестов
// супервизора. Один и тот же экземпляр используется и как Runner, и как SupervisorRunner.
//
// Контроллер-резолвшн: автономные/стандартные тестовые стадии неинтерактивные
// (Command == ""), поэтому runnerFor возвращает инжектированный o.runner. Реальный
// executor.New выставляет env AFM_STAGE_DIR только когда Config.StageDir != "" и не
// задаёт cmd.Dir — поэтому bash-скрипт из брифа не мог записать артефакты. Вместо этого
// мок пишет файлы напрямую, получая stageDir через filepath.Dir(logFile).
type supervisorTestRunner struct {
	decisionJSON []byte
}

func (m *supervisorTestRunner) RunJSONQuery(_ context.Context, _ string) ([]byte, error) {
	return m.decisionJSON, nil
}

// RunPlanning пишет plan.md, проходящий checkPlanCompletionFor (нужны ## Tasks и т.п.).
func (m *supervisorTestRunner) RunPlanning(_ context.Context, _, _, outFile, _ string) error {
	plan := "## Tasks\n- [ ] step 1\n## Assumptions\n- none\n## Acceptance Criteria\n- [ ] done\n"
	return os.WriteFile(outFile, []byte(plan), 0644)
}

// RunAgent пишет артефакты, имитируя реального агента. stageDir выводится из logFile
// (runAutonomousAgent/runImplementationAgent задают logFile = <stageDir>/<phase>.log).
func (m *supervisorTestRunner) RunAgent(_ context.Context, agentType, _, _, logFile string) error {
	stageDir := filepath.Dir(logFile)
	switch agentType {
	case "autonomous_execution":
		summary := "## Summary\nExecuted autonomously.\n## Changes\n- some_file.go\n## Result\nSuccess.\n"
		return os.WriteFile(filepath.Join(stageDir, "execution_summary.md"), []byte(summary), 0644)
	default: // implementation — .done должен быть непустым (см. checkCompletion)
		return os.WriteFile(filepath.Join(stageDir, ".done"), []byte("done\n"), 0644)
	}
}

// setupSupervisorOrch строит Orchestrator с supervisor-стадией. Один мок используется
// и как Runner, и как SupervisorRunner.
func setupSupervisorOrch(t *testing.T, stages []flow.Stage, decisionJSON []byte) (*orchestrator.Orchestrator, string) {
	t.Helper()
	runDir := t.TempDir()
	ids := make([]string, len(stages))
	for i, s := range stages {
		ids[i] = s.ID
	}
	store, err := state.Open(runDir, ids)
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	mock := &supervisorTestRunner{decisionJSON: decisionJSON}

	orch := orchestrator.New(orchestrator.Options{
		RunDir:           runDir,
		Stages:           stages,
		Store:            store,
		Config:           config.Default(),
		Prompts:          orchestrator.DefaultPrompts(),
		Runner:           mock,
		SupervisorRunner: mock,
	})
	return orch, runDir
}

// TestIntegration_SupervisorAutonomous проверяет полный автономный трек:
// supervisor решает can_execute_autonomously=true → planning пропускается,
// автономный агент пишет execution_summary.md, стадия переходит в done.
func TestIntegration_SupervisorAutonomous(t *testing.T) {
	decision := []byte(`{"can_execute_autonomously":true,"reason":"skill handles it","recommended_phases":["autonomous_execution"]}`)

	stages := []flow.Stage{
		{
			ID:          "auto-stage",
			Description: "run goga:apply autonomously",
			Supervisor:  true,
			Agents:      []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation},
			Skills:      []string{"goga:apply"},
		},
	}

	orch, runDir := setupSupervisorOrch(t, stages, decision)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := orch.Run(ctx); err != nil && err != context.DeadlineExceeded {
		t.Fatalf("orch.Run: %v", err)
	}

	stageDir := filepath.Join(runDir, "auto-stage")

	// 1. Стадия завершена.
	if st := orchestrator.StoreFromOrch(orch).Get("auto-stage"); st != state.StatusDone {
		t.Errorf("expected stage status done, got %s", st)
	}

	// 2. autonomous.flag существует.
	if _, err := os.Stat(filepath.Join(stageDir, "autonomous.flag")); err != nil {
		t.Errorf("autonomous.flag missing: %v", err)
	}

	// 3. execution_summary.md существует и содержит ожидаемый текст.
	data, err := os.ReadFile(filepath.Join(stageDir, "execution_summary.md"))
	if err != nil {
		t.Fatalf("execution_summary.md missing: %v", err)
	}
	if !strings.Contains(string(data), "Executed autonomously") {
		t.Errorf("execution_summary.md: expected to contain %q, got %s", "Executed autonomously", data)
	}

	// 4. plan.md НЕ существует (planning пропущен).
	if _, err := os.Stat(filepath.Join(stageDir, "plan.md")); err == nil {
		t.Error("plan.md should NOT exist for autonomous stage")
	} else if !os.IsNotExist(err) {
		t.Errorf("unexpected error checking plan.md: %v", err)
	}

	// 5. supervisor.jsonl существует и содержит запись об автономном решении.
	logData, err := os.ReadFile(filepath.Join(runDir, "supervisor.jsonl"))
	if err != nil {
		t.Fatalf("supervisor.jsonl missing: %v", err)
	}
	if !strings.Contains(string(logData), "autonomous") {
		t.Errorf("supervisor.jsonl should mention autonomous, got: %s", logData)
	}
}

// TestIntegration_SupervisorStandard проверяет стандартный трек:
// supervisor решает can_execute_autonomously=false → обычный planning + implementation,
// autonomous.flag не создаётся, plan.md появляется.
func TestIntegration_SupervisorStandard(t *testing.T) {
	decision := []byte(`{"can_execute_autonomously":false,"reason":"needs planning","recommended_phases":["planning","implementation"]}`)

	stages := []flow.Stage{
		{
			ID:          "std-stage",
			Description: "standard flow",
			Supervisor:  true,
			Agents:      []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation},
		},
	}

	orch, runDir := setupSupervisorOrch(t, stages, decision)

	// Стандартному треку нужно подтверждение планирования.
	cancelApprove := autoApprove(orch)
	defer cancelApprove()

	ctx, cancelCtx := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelCtx()

	if err := orch.Run(ctx); err != nil && err != context.DeadlineExceeded {
		t.Fatalf("orch.Run: %v", err)
	}

	stageDir := filepath.Join(runDir, "std-stage")

	// 1. plan.md существует (planning не пропущен).
	if _, err := os.Stat(filepath.Join(stageDir, "plan.md")); err != nil {
		t.Errorf("plan.md should exist for standard stage: %v", err)
	}

	// 2. autonomous.flag НЕ существует.
	if _, err := os.Stat(filepath.Join(stageDir, "autonomous.flag")); err == nil {
		t.Error("autonomous.flag should NOT exist for standard stage")
	} else if !os.IsNotExist(err) {
		t.Errorf("unexpected error checking autonomous.flag: %v", err)
	}

	// 3. Стадия должна дойти до done.
	if st := orchestrator.StoreFromOrch(orch).Get("std-stage"); st != state.StatusDone {
		t.Errorf("expected stage status done, got %s", st)
	}
}

// TestIntegration_StaleAutonomousFlagClearedOnPlanning воспроизводит баг: стадия
// на предыдущей попытке ушла в autonomous-трек (создан autonomous.flag), autonomous-
// агент не завершил её, и на retry/повторе стадия пошла по стандартному planning-треку
// и дошла до awaiting_approval с реальным plan.md. Флаг обязан быть снят: иначе
// stage_autonomous в /api/status оставался бы true, и дашборд прятал бы plan-панель
// (approve-кнопку не видно — стадия «висит» на awaiting_approval без UI).
func TestIntegration_StaleAutonomousFlagClearedOnPlanning(t *testing.T) {
	stages := []flow.Stage{
		{ID: "s1", Name: "Stage 1", Description: "planning track after stale autonomous attempt", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
	}

	// Стандартный planning-раннер (пишет plan.md), без supervisor и без autoApprove —
	// хотим остановиться ровно на awaiting_approval и проверить состояние.
	runner := mockRunner(t, mockPlanningScript)
	orch, runDir, stateFile := setupOrchestratorWithRunner(t, stages, runner)

	// Непустой DashboardURL → headless auto-approve не срабатывает, стадия стабильно
	// стоит на awaiting_approval (как при реально запущенном дашборде).
	orch.SetDashboardURL("http://127.0.0.1:0")

	// Пред-сеем устаревший autonomous.flag от «прошлой» автономной попытки.
	stageDir := filepath.Join(runDir, "s1")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "autonomous.flag"), nil, 0644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	go func() { _ = orch.Run(ctx) }()

	// Стадия проходит planning и встаёт на awaiting_approval (approve никто не жмёт).
	waitForStatus(t, stateFile, "s1", state.StatusAwaitingApproval, 10*time.Second)

	// plan.md появился — стадия реально шла по стандартному треку.
	if _, err := os.Stat(filepath.Join(stageDir, "plan.md")); err != nil {
		t.Errorf("plan.md should exist after standard planning: %v", err)
	}

	// Устаревший autonomous.flag снят — stage_autonomous станет false, plan-панель
	// с approve-кнопкой видна.
	if _, err := os.Stat(filepath.Join(stageDir, "autonomous.flag")); err == nil {
		t.Error("stale autonomous.flag should have been cleared by the planning track")
	} else if !os.IsNotExist(err) {
		t.Errorf("unexpected error checking autonomous.flag: %v", err)
	}
}

// TestIntegration_RetryFailedAutonomousStaysAutonomous: упавшая autonomous-стадия
// (на диске autonomous.flag) при manual retry перезапускается в автономном треке,
// а НЕ сваливается в planning. Раньше retryStage игнорировал флаг и всегда уводил
// planning-способную стадию в planning (баг: пользователь видел неожиданный
// planning + awaiting_approval вместо повторного автономного запуска).
func TestIntegration_RetryFailedAutonomousStaysAutonomous(t *testing.T) {
	decision := []byte(`{"can_execute_autonomously":true,"reason":"skill handles it","recommended_phases":["autonomous_execution"]}`)
	stages := []flow.Stage{
		{
			ID:          "s1",
			Name:        "S1",
			Description: "autonomous stage that failed and is retried",
			Supervisor:  true,
			Agents:      []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation},
			Skills:      []string{"goga:apply"},
		},
	}

	orch, runDir := setupSupervisorOrch(t, stages, decision)
	// DashboardURL непустой → Run не выходит на failed-стадии (терминальной), а ждёт
	// AllDone; это даёт время вызвать Retry (и совпадает с реальным сценарием — дашборд запущен).
	orch.SetDashboardURL("http://127.0.0.1:9876")

	// Пред-состояние: стадия уже ушла в autonomous-трек (flag на диске) и упала
	// (напр. autonomous-агент был прерван рестартом → context canceled → failed).
	stageDir := filepath.Join(runDir, "s1")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "autonomous.flag"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	store := orchestrator.StoreFromOrch(orch)
	if err := store.Apply(state.Transition{StageID: "s1", From: state.StatusPending, To: state.StatusFailed, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(runDir, "state.json")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	go func() { _ = orch.Run(ctx) }()

	// Retry упавшей autonomous-стадии.
	if err := orch.Retry(context.Background(), "s1"); err != nil {
		t.Fatalf("Retry: %v", err)
	}

	// Успешно завершилась через autonomous-трек (а не застряла на awaiting_approval,
	// куда привёл бы ошибочный planning-fallback).
	waitForStatus(t, stateFile, "s1", state.StatusDone, 10*time.Second)

	// execution_summary.md написан → шёл автономный агент.
	if _, err := os.Stat(filepath.Join(stageDir, "execution_summary.md")); err != nil {
		t.Errorf("execution_summary.md missing — retry did NOT re-run the autonomous track: %v", err)
	}
	// plan.md отсутствует → в planning не сваливались.
	if _, err := os.Stat(filepath.Join(stageDir, "plan.md")); err == nil {
		t.Error("plan.md exists — retry wrongly fell back to the planning track")
	}
	// autonomous.flag сохранён → стадия осталась автономной.
	if _, err := os.Stat(filepath.Join(stageDir, "autonomous.flag")); err != nil {
		t.Errorf("autonomous.flag should be preserved on autonomous retry: %v", err)
	}
}

// compile-time check that supervisorTestRunner satisfies executor.Runner.
var _ executor.Runner = (*supervisorTestRunner)(nil)
