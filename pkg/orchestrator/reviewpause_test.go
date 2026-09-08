package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/concurrency"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
	"github.com/akopichin/afm/pkg/state"
)

// TestRunnerKind_SetGetClear проверяет базовый контракт реестра runnerKind
// (Task 6 фичи "review notes"): пусто по умолчанию, set/get, clear возвращает
// в пустое состояние. o := &Orchestrator{} без New() — runnerKind это голое
// sync.Map с нулевым значением, отдельной инициализации не требует.
func TestRunnerKind_SetGetClear(t *testing.T) {
	o := &Orchestrator{}
	if o.runnerKindOf("s1") != "" {
		t.Fatal("empty default")
	}
	o.setRunnerKind("s1", kindImplementation)
	if o.runnerKindOf("s1") != kindImplementation {
		t.Fatal("set/get")
	}
	o.clearRunnerKind("s1")
	if o.runnerKindOf("s1") != "" {
		t.Fatal("clear")
	}
}

// TestRunWithRetry_Attempt0ResumeContext закрывает Task 7 фичи "review notes"
// (decision A′): резюмируемый non-interactive стадии агент должен получить
// replay-контекст "previously completed actions" уже на ПЕРВОЙ попытке, а не
// только начиная со второй (attempt > 0) — иначе Continue после паузы
// перезапускает агента с чистого листа, будто предыдущей работы не было.
// armResumeContext — однократный флаг (stageID -> взведён), consumed внутри
// runWithRetry через LoadAndDelete: используется агентом отладки этого
// вызова, ко второму вызову той же стадии без повторного arm флаг уже снят.
//
// Переиспользует существующий тест-конструктор setupHookOrch (hooks_test.go)
// вместо изобретения параллельного newTestOrchestrator — тот уже собирает
// Orchestrator с реальными RunDir/Store/critical bus, которых достаточно,
// чтобы runWithRetry дошёл до success-ветки (agentFn возвращает nil,
// completionCheck nil → сразу EventAgentCompleted, без обращения к FSM).
func TestRunWithRetry_Attempt0ResumeContext(t *testing.T) {
	o, runDir := setupHookOrch(t, "s1")
	stageDir := filepath.Join(runDir, "s1")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Минимальная строка в форме реального stream-json лога агента (см.
	// buildRetryContext/executor.RenderActions и аналогичный
	// TestBuildRetryContext_FullActionNotTruncated в retry_test.go) — иначе
	// buildRetryContext вернёт "" для любого attempt и тест не отличит
	// "context пуст потому что не собирается" от "собирается, но пуст".
	line := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"echo prior work"}}]}}`
	if err := os.WriteFile(filepath.Join(stageDir, "implementation.jsonl"), []byte(line+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Baseline: без arm первая (и единственная — agentFn успешен) попытка НЕ
	// получает retry-контекст.
	var unarmedCtx string
	o.runWithRetry(context.Background(), flow.Stage{ID: "s1"}, phaseImplementation,
		func(retryContext string) error { unarmedCtx = retryContext; return nil },
		nil, func() {})
	if unarmedCtx != "" {
		t.Fatalf("unarmed attempt-0 context should be empty, got %q", unarmedCtx)
	}

	o.armResumeContext("s1")

	var armedCtx string
	o.runWithRetry(context.Background(), flow.Stage{ID: "s1"}, phaseImplementation,
		func(retryContext string) error { armedCtx = retryContext; return nil },
		nil, func() {})
	if armedCtx == "" {
		t.Fatal("armed attempt-0 context should be non-empty")
	}

	// Флаг однократный: следующий вызов той же стадии без повторного arm
	// снова не должен получить контекст на attempt 0.
	var secondCtx string
	o.runWithRetry(context.Background(), flow.Stage{ID: "s1"}, phaseImplementation,
		func(retryContext string) error { secondCtx = retryContext; return nil },
		nil, func() {})
	if secondCtx != "" {
		t.Fatalf("resume-context flag must be consumed after first use, got %q on next call", secondCtx)
	}
}

// TestRunWithRetry_SelfAbortsWhenAlreadyPaused закрывает Task 8 фичи "review
// notes": окно между проверкой shouldRun в concurrency.Manager.SpawnAgent и
// регистрацией interruptCh в начале runWithRetry — если Pause() успевает
// закоммитить транзишн в этом окне, интерраптор ещё не зарегистрирован и
// сигнал некому доставить. Симулируем это, заранее переведя стадию в
// state.StatusPaused (как будто Pause() уже случился ДО входа в
// runWithRetry) и проверяя, что agentFn вообще не вызывается — attempt-loop
// должен сам себя прервать на самом первом обращении к currentStatus, до
// запуска подпроцесса.
func TestRunWithRetry_SelfAbortsWhenAlreadyPaused(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	if err := o.opts.Store.Apply(&state.Transition{StageID: "s1", From: state.StatusRunning, To: state.StatusPaused, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}

	called := false
	agentFn := func(string) error { called = true; return nil }
	done := make(chan struct{})
	go func() {
		o.runWithRetry(context.Background(), flow.Stage{ID: "s1", Name: "s1"}, phaseImplementation,
			agentFn, func() error { return nil }, func() {})
		close(done)
	}()
	<-done

	if called {
		t.Fatal("agentFn must not run when stage is already paused")
	}
}

// TestActivationHold_BlocksNewActivation закрывает Task 9 фичи "review notes":
// пока activationHeld взведён (ревью-режим), startReadyStages не должен
// переводить уже готовую (Ready) стадию в Running — она обязана остаться на
// месте (already-running стадии здесь ни при чём, только НОВЫЕ активации).
// Собран напрямую (по образцу TestCancelDialog_FailsStage в
// control_api_test.go), а не через setupHookOrch: нужен реальный
// concurrency.Manager, иначе незагаженный путь (до реализации guard'а)
// упадёт в nil-панике на o.concurrency.SpawnAgent вместо честного assert-fail.
func TestActivationHold_BlocksNewActivation(t *testing.T) {
	dir := t.TempDir()
	stages := []flow.Stage{{ID: "s1", Agents: []flow.AgentType{flow.AgentImplementation}}}
	store, err := state.Open(dir, []string{"s1"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	// Стадия без зависимостей, уже в Ready — единственное, что мешает
	// startReadyStages её забрать, должен быть activationHeld.
	if err := store.Apply(&state.Transition{StageID: "s1", From: state.StatusPending, To: state.StatusReady, Event: "test_setup"}); err != nil {
		t.Fatal(err)
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
	o.activationHeld.Store(true)

	o.startReadyStages(context.Background())

	if got := o.currentStatus("s1"); got != state.StatusReady {
		t.Fatalf("held stage should stay ready, got %v", got)
	}
}

// newPauseFlowTestOrch строит *Orchestrator поверх реального *state.Store с
// НЕСКОЛЬКИМИ стадиями через New() — в отличие от newTestOrchestrator
// (memory_agent_test.go), которая жёстко собирает Store с единственной
// стадией "s1" и не годится тестам PauseFlow, которым нужно независимо
// сидировать статусы s1/s2/s3. stages сами решают, какая из них script
// (flow.Stage.Script непусто) — граф собирается один раз внутри New() и
// после этого неизменен, так что "is this a script stage" нельзя пришить
// постфактум, только объявить при конструировании.
func newPauseFlowTestOrch(t *testing.T, stages []flow.Stage) *Orchestrator {
	t.Helper()
	runDir := t.TempDir()
	ids := make([]string, len(stages))
	for i, s := range stages {
		ids[i] = s.ID
	}
	store, err := state.Open(runDir, ids)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return New(Options{RunDir: runDir, Stages: stages, Store: store, Config: config.Default()})
}

// seedStageStatus переводит стадию id из state.StatusPending (начальное
// состояние любой свежесозданной Store) прямо в status одной транзакцией.
// Store.Apply проверяет только совпадение From с текущим статусом (FSM-guard
// тут ни при чём — это сырая подмена состояния для теста), поэтому это
// работает для любого целевого статуса ровно так же, как уже делают
// setupHookOrch/TestActivationHold_BlocksNewActivation в этом пакете.
func seedStageStatus(t *testing.T, store *state.Store, id string, status state.StageStatus) {
	t.Helper()
	if err := store.Apply(&state.Transition{StageID: id, From: state.StatusPending, To: status, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
}

// seedScriptStage — то же самое, что seedStageStatus, но для стадии,
// объявленной в графе как script (Script непусто в её flow.Stage) —
// именование отдельным хелпером в тесте документирует намерение "эта стадия
// должна быть проигнорирована PauseFlow как скриптовая", а не просто
// переиспользует seedStageStatus напрямую.
func seedScriptStage(t *testing.T, o *Orchestrator, id string, status state.StageStatus) {
	t.Helper()
	stage := o.graph.Stage(id)
	if stage == nil || !stage.IsScript() {
		t.Fatalf("seedScriptStage: %q is not declared as a script stage in the graph", id)
	}
	seedStageStatus(t, o.opts.Store, id, status)
}

// markActiveForTest делает concurrency.Manager.IsActive(stageID) истинным по-
// настоящему, через реальный SpawnAgent (markActive/markDone — приватные
// методы concurrency.Manager, недоступные этому пакету напрямую — тест
// использует единственную существующую производственную точку входа, а не
// заводит теневой способ выставить активность). run блокируется на release,
// закрываемом в t.Cleanup, так что стадия остаётся "активной" на всё время
// теста; close(ready) гарантирует, что markActive внутри SpawnAgent уже
// отработал к моменту, когда <-ready возвращается здесь.
func markActiveForTest(t *testing.T, o *Orchestrator, stageID string) {
	t.Helper()
	ready := make(chan struct{})
	release := make(chan struct{})
	o.concurrency.SpawnAgent(context.Background(), flow.Stage{ID: stageID}, func(context.Context, flow.Stage) {
		close(ready)
		<-release
	})
	t.Cleanup(func() { close(release) })
	<-ready
}

// TestPauseFlow_OwnsOnlyActiveWinners закрывает Task 10 фичи "review notes":
// PauseFlow не должен пытаться поставить на паузу всё подряд, что похоже на
// "бегущую" стадию — только те, что действительно живы прямо сейчас
// (concurrency.IsActive), и никогда скриптовые стадии (у них нет точки
// прерывания, см. Pause в control_api.go).
func TestPauseFlow_OwnsOnlyActiveWinners(t *testing.T) {
	stages := []flow.Stage{
		{ID: "s1", Agents: []flow.AgentType{flow.AgentImplementation}},
		{ID: "s2", Agents: []flow.AgentType{flow.AgentImplementation}},
		{ID: "s3", Script: "echo hi"},
	}
	o := newPauseFlowTestOrch(t, stages)

	// s1: running + IsActive -> owned as implementation.
	seedStageStatus(t, o.opts.Store, "s1", state.StatusRunning)
	o.setRunnerKind("s1", kindImplementation)
	markActiveForTest(t, o, "s1") // helper making IsActive("s1")==true

	// s2: running but NOT active (queued) -> not owned.
	seedStageStatus(t, o.opts.Store, "s2", state.StatusRunning)

	// s3: script running -> not owned.
	seedScriptStage(t, o, "s3", state.StatusRunning)

	owners, err := o.PauseFlow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(owners) != 1 || owners[0] != "s1" {
		t.Fatalf("owners = %v, want [s1]", owners)
	}
	if o.currentStatus("s1") != state.StatusPaused {
		t.Fatal("s1 not paused")
	}
	if o.currentStatus("s2") != state.StatusRunning {
		t.Fatalf("s2 must be left running (not owned), got %v", o.currentStatus("s2"))
	}
	if o.currentStatus("s3") != state.StatusRunning {
		t.Fatalf("s3 (script) must be left running, got %v", o.currentStatus("s3"))
	}

	m := o.reviewMarker.Load()
	if m == nil || m.State != state.PauseStatePaused || len(m.Owned) != 1 || m.Owned[0].ResumeKind != kindImplementation {
		t.Fatalf("marker wrong: %+v", m)
	}
	if !o.activationHeld.Load() {
		t.Fatal("activationHeld must be set")
	}
}

// TestPauseFlow_RejectedDuringFinalization закрывает Task 10: PauseFlow не
// должен запускать новый ревью-раунд поверх ещё финализирующегося.
func TestPauseFlow_RejectedDuringFinalization(t *testing.T) {
	o := newPauseFlowTestOrch(t, []flow.Stage{{ID: "s1", Agents: []flow.AgentType{flow.AgentImplementation}}})
	o.finalizing.Store(true)
	if _, err := o.PauseFlow(context.Background()); !errors.Is(err, ErrRunFinalizing) {
		t.Fatalf("want ErrRunFinalizing, got %v", err)
	}
}

// TestRenderReviewFeedback_AlwaysCarriesOriginal закрывает Task 11 фичи
// "review notes": рендер ревью-фидбэка ВСЕГДА выводит JSON-quoted исходный
// текст строки, а при расхождении контентной ХЭШ'и добавляет диагностику
// "content differed at injection".
func TestRenderReviewFeedback_AlwaysCarriesOriginal(t *testing.T) {
	l42 := 42
	orig := "\tif err != nil {"
	notes := []state.ReviewNote{{
		Root: "project", Path: "a.go", DisplayPath: "project/a.go",
		Reference: `[AFM file: "/w/a.go"]`, Line: &l42, OrigLineText: &orig,
		ContentSHA: "sha256:OLD", Text: "handle this",
	}}
	// current sha differs -> changed-at-injection warning, but original still present.
	out := renderReviewFeedback(notes, func(root, path string) (string, bool) {
		return "sha256:NEW", true
	})
	if !strings.Contains(out, `original: "\tif err != nil {"`) {
		t.Fatalf("original line text missing:\n%s", out)
	}
	if !strings.Contains(out, "content differed at injection") {
		t.Fatalf("drift warning missing:\n%s", out)
	}
	if !strings.Contains(out, `[AFM file: "/w/a.go"]`) {
		t.Fatal("reference marker missing")
	}
}

// TestRenderReviewFeedback_UnavailableFile закрывает Task 11: файл
// недоступен при injection-времени (current() возвращает ok=false) ->
// добавляется диагностика на заголовке файла.
func TestRenderReviewFeedback_UnavailableFile(t *testing.T) {
	l5 := 5
	orig := "package gone"
	notes := []state.ReviewNote{{DisplayPath: "project/gone.go", Reference: `[AFM file: "/w/gone.go"]`,
		Line: &l5, OrigLineText: &orig, ContentSHA: "sha256:X", Text: "note"}}
	out := renderReviewFeedback(notes, func(root, path string) (string, bool) { return "", false })
	if !strings.Contains(out, "file unavailable at injection") {
		t.Fatalf("unavailable marker missing:\n%s", out)
	}
}

// TestRenderReviewFeedback_TwoFileLevelNotesStableOrder закрывает Fix round 1:
// регрессионная проверка strict-weak-ordering контракта SortStableFunc.
// renderReviewFeedback должна корректно обрабатывать несколько file-level
// notes (Line==nil) на том же файле, что и line notes, и выдать
// детерминированный порядок (line notes первыми, потом file-level notes) без
// паники и переупорядочивания.
func TestRenderReviewFeedback_TwoFileLevelNotesStableOrder(t *testing.T) {
	l10 := 10
	lineOrig := "var x int"

	// Три замечания: одно на строке 10, два на файле (Line==nil)
	notes := []state.ReviewNote{
		{
			Root: "proj", Path: "a.go", DisplayPath: "proj/a.go",
			Reference: `[AFM file: "/w/a.go"]`, Line: &l10, OrigLineText: &lineOrig,
			ContentSHA: "sha256:ABC", Text: "add type annotation",
		},
		{
			Root: "proj", Path: "a.go", DisplayPath: "proj/a.go",
			Reference: `[AFM file: "/w/a.go"]`, Line: nil, OrigLineText: nil,
			ContentSHA: "sha256:ABC", Text: "file-level comment 1",
		},
		{
			Root: "proj", Path: "a.go", DisplayPath: "proj/a.go",
			Reference: `[AFM file: "/w/a.go"]`, Line: nil, OrigLineText: nil,
			ContentSHA: "sha256:ABC", Text: "file-level comment 2",
		},
	}
	out := renderReviewFeedback(notes, func(root, path string) (string, bool) {
		return "sha256:ABC", true
	})

	// Проверяем, что выход содержит оба file-level замечания.
	if !strings.Contains(out, "file-level comment 1") {
		t.Fatalf("file-level comment 1 missing:\n%s", out)
	}
	if !strings.Contains(out, "file-level comment 2") {
		t.Fatalf("file-level comment 2 missing:\n%s", out)
	}

	// Проверяем порядок: line note (Line 10) должен быть ПЕРЕД file-level нотами.
	pos1 := strings.Index(out, "Line 10")
	pos2 := strings.Index(out, "file-level comment 1")
	pos3 := strings.Index(out, "file-level comment 2")

	if pos1 < 0 || pos2 < 0 || pos3 < 0 {
		t.Fatalf("missing required text in output:\n%s", out)
	}
	if pos1 >= pos2 || pos1 >= pos3 {
		t.Fatalf("line note must come before file-level notes, got positions: line=%d, file1=%d, file2=%d", pos1, pos2, pos3)
	}
}
