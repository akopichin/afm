package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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

// testActiveRelease хранит per-stageID idempotent release-функцию,
// установленную markActiveForTest — markDoneForTest использует её, чтобы
// освободить стадию ПРЯМО ВНУТРИ теста (не только на t.Cleanup), когда нужно
// проверить логику, реагирующую на переход IsActive true->false (см.
// resumeOwner's drain-wait в TestResumeOwner_WaitsForDrain).
var testActiveRelease sync.Map // stageID -> func() (закрывает release-канал не более раза)

// markActiveForTest делает concurrency.Manager.IsActive(stageID) истинным по-
// настоящему, через реальный SpawnAgent (markActive/markDone — приватные
// методы concurrency.Manager, недоступные этому пакету напрямую — тест
// использует единственную существующую производственную точку входа, а не
// заводит теневой способ выставить активность). run блокируется на release;
// releaseFn (sync.Once-обёрнутая) регистрируется и в testActiveRelease (для
// markDoneForTest), и в t.Cleanup (страховка, если тест сам не вызвал
// markDoneForTest) — идемпотентность гарантирует, что оба пути закрытия не
// паникуют на двойном close. close(ready) гарантирует, что markActive внутри
// SpawnAgent уже отработал к моменту, когда <-ready возвращается здесь.
func markActiveForTest(t *testing.T, o *Orchestrator, stageID string) {
	t.Helper()
	ready := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	releaseFn := func() { once.Do(func() { close(release) }) }
	testActiveRelease.Store(stageID, releaseFn)
	o.concurrency.SpawnAgent(context.Background(), flow.Stage{ID: stageID}, func(context.Context, flow.Stage) {
		close(ready)
		<-release
	})
	t.Cleanup(func() {
		releaseFn()
		testActiveRelease.Delete(stageID)
	})
	<-ready
}

// markDoneForTest releases a stage marked active by markActiveForTest so
// concurrency.Manager.IsActive(stageID) flips to false DURING the test
// (rather than only at t.Cleanup) — needed to test drain-wait logic such as
// resumeOwner's polling loop. Blocks until the effect is actually observable
// (the SpawnAgent goroutine's deferred markDone has run), so the caller never
// races its own subsequent IsActive check against that goroutine.
func markDoneForTest(t *testing.T, o *Orchestrator, stageID string) {
	t.Helper()
	v, ok := testActiveRelease.Load(stageID)
	if !ok {
		t.Fatalf("markDoneForTest(%q): not marked active (call markActiveForTest first)", stageID)
	}
	v.(func())()
	for o.concurrency.IsActive(stageID) {
		time.Sleep(time.Millisecond)
	}
}

// setPausedFrom seeds a stage straight into StatusPaused with PausedFrom set
// to a specific prior status, via two real Store.Apply transitions
// (Pending -> from -> Paused) — the same derivation production code uses
// (RunState.SetStageStatusAt stamps PausedFrom from the status immediately
// preceding a transition INTO Paused), rather than hand-poking the field.
// Requires the stage to still be Pending (true for any freshly-opened Store,
// e.g. newTestOrchestrator's), and from != state.StatusPaused.
func setPausedFrom(t *testing.T, store *state.Store, id string, from state.StageStatus) {
	t.Helper()
	if from != state.StatusPending {
		if err := store.Apply(&state.Transition{StageID: id, From: state.StatusPending, To: from, Event: "test_setup"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Apply(&state.Transition{StageID: id, From: from, To: state.StatusPaused, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
}

// boolStr renders a bool the same way the test-only observer closure in
// TestResumeOwner_TargetImplementationUsesWithFeedback does, so assertions
// read as a single comparable string ("implementation/true") instead of two
// separate fields.
func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
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

// TestResumeOwner_TargetImplementationUsesWithFeedback закрывает Task 12
// фичи "review notes": стадия-цель (isTarget=true, review notes ЕЙ
// адресованы) резюмируется через withFeedbackRunner независимо от
// FromRevising — она должна увидеть feedback.md с ревью-заметками.
// o.testRunnerHook — тест-сейм: подменяет реальный Trigger+SpawnAgent
// наблюдением "какой kind, с фидбеком или без", без запуска процесса.
func TestResumeOwner_TargetImplementationUsesWithFeedback(t *testing.T) {
	o := newTestOrchestrator(t)
	setPausedFrom(t, o.opts.Store, "s1", state.StatusRunning)

	var chosen string
	o.testRunnerHook = func(kind string, withFeedback bool) { chosen = kind + "/" + boolStr(withFeedback) }

	o.resumeOwner(context.Background(), state.PauseOwner{ID: "s1", ResumeKind: kindImplementation}, true)

	if chosen != "implementation/true" {
		t.Fatalf("target should use implementation WithFeedback, got %q", chosen)
	}
}

// TestResumeOwner_WaitsForDrain закрывает Task 12: resumeOwner не должен
// транзишнить/спавнить раннер, пока concurrency.IsActive(ow.ID) истинно —
// это сигнал, что старый (прерванный PauseFlow'ом) callback стадии ещё не
// вернулся. Резюмировать раньше значит рисковать гонкой: старый callback,
// вернувшись позже с ErrUserInterrupted, увидит статус уже не paused (мы его
// сменили) и не распознает свой возврат как обычный Pause — см. комментарий
// у resumeOwner.
//
// markActiveForTest seeds the stage to Running (not Paused) BEFORE spawning:
// the real concurrency.Manager built by New() carries the production
// shouldRun gate (opts.Store.Get(id) != Paused, see orchestrator.New) checked
// right after the semaphore is acquired — seeding Paused first would make
// that gate reject the spawned run() before it ever reaches `close(ready)`,
// hanging markActiveForTest itself instead of exercising the drain wait. The
// stage is moved Running->Paused only AFTER the goroutine is confirmed live,
// reproducing the real sequence: PauseFlow's EvPause fires while an agent
// goroutine started earlier (when the stage was still Running) is still in
// flight — IsActive stays true for a stage that is already Paused.
//
// testRunnerHook is stubbed (no-op) here too, even though this test isn't
// about which runner gets picked: without it, resumeOwner's real path spawns
// a genuine runImplementationAgent, which fails fast (no plan.md on disk) and
// calls Trigger(EvFail) on a goroutine this test never waits for — a leftover
// write racing t.Cleanup's store.Close() right after the test returns
// (exactly the "storage failure: write events.jsonl: invalid argument" flake
// documented in AGENTS.md: nil *os.File after Close returns fs.ErrInvalid,
// not a real OS error). The stub keeps resumeOwner's observable side effect
// bounded to the drain wait this test actually exercises.
func TestResumeOwner_WaitsForDrain(t *testing.T) {
	o := newTestOrchestrator(t)
	o.testRunnerHook = func(string, bool) {}
	seedStageStatus(t, o.opts.Store, "s1", state.StatusRunning)
	markActiveForTest(t, o, "s1")
	if err := o.opts.Store.Apply(&state.Transition{StageID: "s1", From: state.StatusRunning, To: state.StatusPaused, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	go func() {
		o.resumeOwner(context.Background(), state.PauseOwner{ID: "s1", ResumeKind: kindImplementation}, false)
		close(started)
	}()

	select {
	case <-started:
		t.Fatal("resume must block until IsActive(s1)==false")
	case <-time.After(50 * time.Millisecond):
	}

	markDoneForTest(t, o, "s1") // IsActive -> false

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("resume should proceed after drain")
	}
}

// waitFor polls cond every 10ms until it returns true or a 2s deadline
// passes (t.Fatal on timeout) — a small generic helper for asserting on the
// effect of an async worker (SpawnDetached) that this package's other
// wait-helpers (waitForStatus, waitForAgentAction) don't cover: they poll a
// specific status/event, this one polls an arbitrary caller-supplied
// condition.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !cond() {
		t.Fatal("waitFor: condition not met within timeout")
	}
}

// TestInject_WritesFeedbackOnceAndResumes закрывает Task 13 фичи "review
// notes": InjectNotesAndResume должен (1) синхронно, до возврата, зафиксировать
// маркер State=resuming/Mode=inject и записать feedback.md РОВНО ОДИН раз
// (SaveFeedbackOnce's op-id sentinel), (2) снять activationHeld, (3)
// асинхронно (под runContext, через SpawnDetached — testRunnerHook подменяет
// реальный Trigger+SpawnAgent внутри resumeOwner) резюмировать owner'а и
// только ПОСЛЕ этого удалить notes+marker (reviewTxnActive() -> false).
func TestInject_WritesFeedbackOnceAndResumes(t *testing.T) {
	o := newTestOrchestrator(t)
	seedStageStatus(t, o.opts.Store, "s1", state.StatusPaused)
	m := state.PauseMarker{Version: 1, OperationID: "op1", State: state.PauseStatePaused,
		Owned: []state.PauseOwner{{ID: "s1", ResumeKind: kindImplementation}}}
	if err := state.WriteNotesPauseMarker(o.opts.RunDir, m); err != nil {
		t.Fatal(err)
	}
	o.reviewMarker.Store(&m)
	o.activationHeld.Store(true)

	l := 1
	orig := "x"
	if err := state.SaveReviewNotes(o.opts.RunDir, state.ReviewNotes{Version: 1, NextID: 2,
		Notes: []state.ReviewNote{{ID: "n1", Root: "project", Path: "a.go", DisplayPath: "project/a.go",
			Reference: `[AFM file: "/w/a.go"]`, Line: &l, OrigLineText: &orig, ContentSHA: "sha256:x", Text: "fix"}}}); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var resumed []string
	o.testRunnerHook = func(kind string, _ bool) {
		mu.Lock()
		resumed = append(resumed, kind)
		mu.Unlock()
	}

	if err := o.InjectNotesAndResume(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}

	// Synchronous effects: assertable immediately, before the async worker runs.
	if o.activationHeld.Load() {
		t.Fatal("activationHeld should be cleared synchronously")
	}
	body, err := os.ReadFile(filepath.Join(o.opts.RunDir, "s1", "feedback.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(body), "fix") != 1 {
		t.Fatalf("feedback not written once:\n%s", body)
	}

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(resumed) == 1
	}) // async worker ran
	mu.Lock()
	if resumed[0] != kindImplementation {
		t.Fatalf("resumed kind = %q, want %q", resumed[0], kindImplementation)
	}
	mu.Unlock()

	waitFor(t, func() bool { return !o.reviewTxnActive() })
	if _, found, _ := state.ReadNotesPauseMarker(o.opts.RunDir); found {
		t.Fatal("marker should be deleted after resume")
	}
	if notes, err := state.LoadReviewNotes(o.opts.RunDir); err != nil || len(notes.Notes) != 0 {
		t.Fatalf("notes should be deleted after resume, got %+v (err=%v)", notes, err)
	}
}

// TestInject_RejectsUnpausedTargetAndEmptyNotes закрывает Task 13: три пути
// отказа InjectNotesAndResume — нет активного ревью-раунда вовсе, целевая
// стадия не paused, заметок нет — не должны трогать диск (никакого
// feedback.md, маркер/статус остаются как были).
func TestInject_RejectsUnpausedTargetAndEmptyNotes(t *testing.T) {
	o := newTestOrchestrator(t)

	// No review-pause marker at all.
	if err := o.InjectNotesAndResume(context.Background(), "s1"); !errors.Is(err, ErrNoReviewPause) {
		t.Fatalf("want ErrNoReviewPause, got %v", err)
	}

	// Marker present, s1 owned, but target not (yet) paused -> ErrTargetNotPaused.
	m := state.PauseMarker{Version: 1, OperationID: "op1", State: state.PauseStatePaused,
		Owned: []state.PauseOwner{{ID: "s1", ResumeKind: kindImplementation}}}
	o.reviewMarker.Store(&m)
	if err := o.InjectNotesAndResume(context.Background(), "s1"); !errors.Is(err, ErrTargetNotPaused) {
		t.Fatalf("want ErrTargetNotPaused, got %v", err)
	}

	// Target not owned by the marker at all -> ErrNotOwned.
	seedStageStatus(t, o.opts.Store, "s1", state.StatusPaused)
	unowned := state.PauseMarker{Version: 1, OperationID: "op1", State: state.PauseStatePaused}
	o.reviewMarker.Store(&unowned)
	if err := o.InjectNotesAndResume(context.Background(), "s1"); !errors.Is(err, ErrNotOwned) {
		t.Fatalf("want ErrNotOwned, got %v", err)
	}

	// Owned and paused, but no review notes on disk -> ErrNoNotes.
	o.reviewMarker.Store(&m)
	if err := o.InjectNotesAndResume(context.Background(), "s1"); !errors.Is(err, ErrNoNotes) {
		t.Fatalf("want ErrNoNotes, got %v", err)
	}

	// None of the rejected paths should have written a feedback.md.
	if _, err := os.Stat(filepath.Join(o.opts.RunDir, "s1", "feedback.md")); !os.IsNotExist(err) {
		t.Fatalf("feedback.md should not exist after rejected injections, stat err=%v", err)
	}
}

// makeAllTerminal moves every stage of o's flow directly to StatusDone
// (bypassing the FSM, same technique as seedStageStatus) so that allTerminal()
// — and therefore shouldExit(), absent any other gate — would report true.
// Used to isolate the reviewTxnActive() gate from shouldExit's other
// conditions in TestShouldExit_HeldByReviewMarker.
func makeAllTerminal(t *testing.T, o *Orchestrator) {
	t.Helper()
	for _, s := range o.opts.Stages {
		seedStageStatus(t, o.opts.Store, s.ID, state.StatusDone)
	}
}

// TestControlPolicy_RejectsWhileReviewPaused закрывает Task 14: пока активен
// маркер ревью-паузы (reviewTxnActive()), любое внешнее forward-driving
// control-действие обязано вернуть ErrFlowPaused ПЕРВЫМ делом — до чтения
// или изменения состояния стадии. Continue и Retry выбраны как
// представители двух разных путей (Continue гейтится по текущему статусу,
// Retry — нет), оба должны отклоняться одинаково.
func TestControlPolicy_RejectsWhileReviewPaused(t *testing.T) {
	o := newTestOrchestrator(t)
	m := state.PauseMarker{Version: 1, State: state.PauseStatePaused}
	o.reviewMarker.Store(&m)
	if err := o.Continue(context.Background(), "s1"); !errors.Is(err, ErrFlowPaused) {
		t.Fatalf("Continue want ErrFlowPaused, got %v", err)
	}
	if err := o.Retry(context.Background(), "s1"); !errors.Is(err, ErrFlowPaused) {
		t.Fatalf("Retry want ErrFlowPaused, got %v", err)
	}
}

// TestShouldExit_HeldByReviewMarker закрывает Task 14: shouldExit() должен
// вернуть false, пока существует маркер ревью-паузы, даже если все прочие
// условия для выхода (allTerminal, DashboardURL=="") уже выполнены — иначе
// Run() финализировал бы ран прямо под ревьюером.
func TestShouldExit_HeldByReviewMarker(t *testing.T) {
	o := newTestOrchestrator(t)
	makeAllTerminal(t, o) // so shouldExit would otherwise be true
	m := state.PauseMarker{Version: 1, State: state.PauseStateResuming}
	o.reviewMarker.Store(&m)
	if o.shouldExit() {
		t.Fatal("shouldExit must be false while review marker exists")
	}
}

// TestRecoverReviewPause_PausedReestablishesHold закрывает Task 15 фичи
// "review notes": на старте afm (перед обычным bootstrap'ом) durable-маркер
// state=paused должен ре-установить и activationHeld, и in-memory кэш
// маркера (reviewTxnActive()) — ровно то, что PauseFlow сам установил бы,
// не будь afm перезапущен между PauseFlow и решением оператора.
func TestRecoverReviewPause_PausedReestablishesHold(t *testing.T) {
	o := newTestOrchestrator(t)
	m := state.PauseMarker{Version: 1, State: state.PauseStatePaused,
		Owned: []state.PauseOwner{{ID: "s1", ResumeKind: kindImplementation}}}
	if err := state.WriteNotesPauseMarker(o.opts.RunDir, m); err != nil {
		t.Fatal(err)
	}
	if err := o.recoverReviewPause(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !o.activationHeld.Load() || !o.reviewTxnActive() {
		t.Fatal("paused recovery must re-establish the hold + marker")
	}
}

// TestRecoverReviewPause_ResumingFinishes закрывает Task 15: durable-маркер
// state=resuming (интерраптед resume-транзакция) должен быть ДОВЕДЁН до
// конца прямо на старте (runResumeTransaction идемпотентна) — маркер и
// review notes должны исчезнуть с диска, а не просто быть закэшированы.
// o.testRunnerHook — тот же тест-сейм, что и в TestInject_WritesFeedback…/
// TestResumeOwner_…, подменяет реальный Trigger+SpawnAgent внутри
// resumeOwner, чтобы не спавнить настоящий процесс агента.
func TestRecoverReviewPause_ResumingFinishes(t *testing.T) {
	o := newTestOrchestrator(t)
	seedStageStatus(t, o.opts.Store, "s1", state.StatusPaused)
	m := state.PauseMarker{Version: 1, OperationID: "op1", State: state.PauseStateResuming,
		Mode: state.PauseModeCancel, Owned: []state.PauseOwner{{ID: "s1", ResumeKind: kindImplementation}}}
	if err := state.WriteNotesPauseMarker(o.opts.RunDir, m); err != nil {
		t.Fatal(err)
	}
	o.testRunnerHook = func(string, bool) {}
	if err := o.recoverReviewPause(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := state.ReadNotesPauseMarker(o.opts.RunDir); found {
		t.Fatal("resuming recovery must finish and delete the marker")
	}
}

// TestRecoverReviewPause_Absent закрывает Task 15: без маркера на диске
// recoverReviewPause не должен ничего трогать — ни activationHeld, ни
// reviewMarker — и должен вернуть nil.
func TestRecoverReviewPause_Absent(t *testing.T) {
	o := newTestOrchestrator(t)
	if err := o.recoverReviewPause(context.Background()); err != nil {
		t.Fatal(err)
	}
	if o.activationHeld.Load() || o.reviewTxnActive() {
		t.Fatal("no marker on disk: recovery must be a no-op")
	}
}

// TestRecoverReviewPause_CorruptResumingFailsClosed закрывает Task 15: если
// на диске лежит НЕПАРСИМЫЙ маркер, но его сырые байты содержат "resuming"
// (best-effort State detection в state.ReadNotesPauseMarker), recoverReviewPause
// обязан вернуть ошибку и оставить activationHeld взведённым — мы не можем
// понять по повреждённому файлу, какие owner'ы уже резюмированы, поэтому
// fail-closed: держим hold, не пропускаем обычный bootstrap на резюм.
func TestRecoverReviewPause_CorruptResumingFailsClosed(t *testing.T) {
	o := newTestOrchestrator(t)
	path := filepath.Join(o.opts.RunDir, "notes-pause.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"state":"resuming",`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := o.recoverReviewPause(context.Background()); err == nil {
		t.Fatal("corrupt resuming marker must fail closed with a non-nil error")
	}
	if !o.activationHeld.Load() {
		t.Fatal("corrupt resuming marker must keep activationHeld set")
	}
}

// TestRecoverReviewPause_CorruptPausedFailsOpen закрывает Task 15: маркер
// корраптед, но лучший угадываемый State не "resuming" (по умолчанию
// paused) — recoverReviewPause должен fail-open (вернуть nil, ничего не
// взводить): owned-стадии уже durable-paused в event-логе независимо от
// маркера, ждут ручного Continue.
func TestRecoverReviewPause_CorruptPausedFailsOpen(t *testing.T) {
	o := newTestOrchestrator(t)
	path := filepath.Join(o.opts.RunDir, "notes-pause.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"state":"paused",`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := o.recoverReviewPause(context.Background()); err != nil {
		t.Fatalf("corrupt paused marker must fail open (nil error), got %v", err)
	}
	if o.activationHeld.Load() || o.reviewTxnActive() {
		t.Fatal("corrupt paused marker must not set activationHeld/reviewMarker")
	}
}

// stubResolveFile returns an Options.ResolveFile stub that always resolves
// successfully with a fixed content_sha/line text/in-range verdict — the
// AddNote tests below (Task 18) drive staleness purely by varying the
// clientSHA/line the CALLER passes in, not by changing what the "workspace"
// reports.
func stubResolveFile(sha, lineText string, inRange bool) func(root, path string, line *int) (ResolvedFile, bool) {
	return func(root, path string, line *int) (ResolvedFile, bool) {
		return ResolvedFile{
			Abs:         "/w/" + path,
			DisplayPath: root + "/" + path,
			Reference:   `[AFM file: "/w/` + path + `"]`,
			ContentSHA:  sha,
			LineText:    lineText,
			InRange:     inRange,
		}, true
	}
}

// pausedNotesTestOrch builds a single-stage orchestrator with a live
// PauseStatePaused review marker (the precondition AddNote/UpdateNote/
// DeleteNote all require) and the given ResolveFile stub wired in.
func pausedNotesTestOrch(t *testing.T, resolve func(root, path string, line *int) (ResolvedFile, bool)) *Orchestrator {
	t.Helper()
	o := newPauseFlowTestOrch(t, []flow.Stage{{ID: "s1", Agents: []flow.AgentType{flow.AgentImplementation}}})
	o.reviewMarker.Store(&state.PauseMarker{Version: 1, State: state.PauseStatePaused})
	o.opts.ResolveFile = resolve
	return o
}

// TestAddNote_RejectedWhenNotPaused закрывает Task 18: без активного
// review-pause маркера (или маркер есть, но State != paused) AddNote должен
// вернуть ErrNoReviewPause и не трогать review-notes.json.
func TestAddNote_RejectedWhenNotPaused(t *testing.T) {
	o := newPauseFlowTestOrch(t, []flow.Stage{{ID: "s1", Agents: []flow.AgentType{flow.AgentImplementation}}})
	o.opts.ResolveFile = stubResolveFile("sha256:x", "orig", true)
	if _, _, err := o.AddNote("project", "a.go", nil, "fix", "sha256:x", 0); !errors.Is(err, ErrNoReviewPause) {
		t.Fatalf("err=%v, want ErrNoReviewPause", err)
	}
}

// TestAddNote_RevMismatch закрывает Task 18: expectedRev != текущий Rev
// (0 при первой ноте) должен вернуть ErrRevConflict.
func TestAddNote_RevMismatch(t *testing.T) {
	o := pausedNotesTestOrch(t, stubResolveFile("sha256:x", "orig", true))
	if _, _, err := o.AddNote("project", "a.go", nil, "fix", "sha256:x", 7); !errors.Is(err, ErrRevConflict) {
		t.Fatalf("err=%v, want ErrRevConflict", err)
	}
}

// TestAddNote_EmptyText закрывает Task 18: пустой/whitespace-only text
// должен вернуть ErrEmptyText.
func TestAddNote_EmptyText(t *testing.T) {
	o := pausedNotesTestOrch(t, stubResolveFile("sha256:x", "orig", true))
	if _, _, err := o.AddNote("project", "a.go", nil, "   ", "sha256:x", 0); !errors.Is(err, ErrEmptyText) {
		t.Fatalf("err=%v, want ErrEmptyText", err)
	}
}

// TestAddNote_StaleContent закрывает Task 18: клиентский content_sha не
// совпадает с тем, что резолвит ResolveFile сейчас — ErrStaleContent.
func TestAddNote_StaleContent(t *testing.T) {
	o := pausedNotesTestOrch(t, stubResolveFile("sha256:current", "orig", true))
	if _, _, err := o.AddNote("project", "a.go", nil, "fix", "sha256:stale", 0); !errors.Is(err, ErrStaleContent) {
		t.Fatalf("err=%v, want ErrStaleContent", err)
	}
}

// TestAddNote_UnresolvableFileIsStaleContent закрывает Task 18: если
// ResolveFile не смог резолвить файл вообще (ok=false — например, файл
// удалили), AddNote должен вернуть ErrStaleContent, а не панику/500.
func TestAddNote_UnresolvableFileIsStaleContent(t *testing.T) {
	o := pausedNotesTestOrch(t, func(root, path string, line *int) (ResolvedFile, bool) { return ResolvedFile{}, false })
	if _, _, err := o.AddNote("project", "a.go", nil, "fix", "sha256:x", 0); !errors.Is(err, ErrStaleContent) {
		t.Fatalf("err=%v, want ErrStaleContent", err)
	}
}

// TestAddNote_StaleLine закрывает Task 18: построчная нота с InRange=false
// (номер строки за пределами текущего файла) должна вернуть ErrStaleLine.
func TestAddNote_StaleLine(t *testing.T) {
	o := pausedNotesTestOrch(t, stubResolveFile("sha256:x", "orig", false))
	line := 999
	if _, _, err := o.AddNote("project", "a.go", &line, "fix", "sha256:x", 0); !errors.Is(err, ErrStaleLine) {
		t.Fatalf("err=%v, want ErrStaleLine", err)
	}
}

// TestAddNote_HappyPath закрывает Task 18: первая нота получает id "n1",
// Rev поднимается до 1, и записанное на диск review-notes.json совпадает с
// возвращённым значением.
func TestAddNote_HappyPath(t *testing.T) {
	o := pausedNotesTestOrch(t, stubResolveFile("sha256:x", "original line", true))
	line := 42
	note, rev, err := o.AddNote("project", "a.go", &line, "fix this", "sha256:x", 0)
	if err != nil {
		t.Fatalf("AddNote: %v", err)
	}
	if note.ID != "n1" {
		t.Fatalf("id=%q, want n1", note.ID)
	}
	if rev != 1 {
		t.Fatalf("rev=%d, want 1", rev)
	}
	if note.DisplayPath != "project/a.go" || note.ContentSHA != "sha256:x" {
		t.Fatalf("note=%+v", note)
	}
	if note.OrigLineText == nil || *note.OrigLineText != "original line" {
		t.Fatalf("orig_line_text=%v", note.OrigLineText)
	}

	onDisk, err := state.LoadReviewNotes(o.opts.RunDir)
	if err != nil {
		t.Fatal(err)
	}
	if onDisk.Rev != 1 || len(onDisk.Notes) != 1 || onDisk.Notes[0].ID != "n1" {
		t.Fatalf("on disk: %+v", onDisk)
	}
}

// TestAddNote_DedupReplacesSameKey закрывает Task 18: две ноты на один и тот
// же (root,path,line) — вторая ЗАМЕНЯЕТ первую, сохраняя id/created_at, а не
// плодит дубликат; Rev поднимается на каждый вызов, NextID — только на
// НОВУЮ ноту.
func TestAddNote_DedupReplacesSameKey(t *testing.T) {
	o := pausedNotesTestOrch(t, stubResolveFile("sha256:x", "original line", true))
	line := 42
	first, rev1, err := o.AddNote("project", "a.go", &line, "first text", "sha256:x", 0)
	if err != nil {
		t.Fatalf("first AddNote: %v", err)
	}
	if first.ID != "n1" || rev1 != 1 {
		t.Fatalf("first=%+v rev=%d", first, rev1)
	}

	second, rev2, err := o.AddNote("project", "a.go", &line, "second text", "sha256:x", rev1)
	if err != nil {
		t.Fatalf("second AddNote: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("dedup must keep the same id: first=%q second=%q", first.ID, second.ID)
	}
	if !second.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("dedup must keep created_at: first=%v second=%v", first.CreatedAt, second.CreatedAt)
	}
	if second.Text != "second text" {
		t.Fatalf("dedup must refresh text: %q", second.Text)
	}
	if rev2 != 2 {
		t.Fatalf("rev2=%d, want 2", rev2)
	}

	onDisk, err := state.LoadReviewNotes(o.opts.RunDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(onDisk.Notes) != 1 {
		t.Fatalf("dedup must not create a second note: %+v", onDisk.Notes)
	}
	if onDisk.NextID != 2 {
		t.Fatalf("NextID must only bump for a genuinely new note: %d", onDisk.NextID)
	}
}

// TestUpdateNote_RevConflictAndSuccess закрывает Task 18: UpdateNote
// отклоняет мисматч ревизии и, при успехе, меняет только text.
func TestUpdateNote_RevConflictAndSuccess(t *testing.T) {
	o := pausedNotesTestOrch(t, stubResolveFile("sha256:x", "orig", true))
	note, rev, err := o.AddNote("project", "a.go", nil, "first", "sha256:x", 0)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := o.UpdateNote(note.ID, "updated", rev+1); !errors.Is(err, ErrRevConflict) {
		t.Fatalf("err=%v, want ErrRevConflict", err)
	}

	newRev, err := o.UpdateNote(note.ID, "updated", rev)
	if err != nil {
		t.Fatalf("UpdateNote: %v", err)
	}
	if newRev != rev+1 {
		t.Fatalf("newRev=%d, want %d", newRev, rev+1)
	}

	onDisk, err := state.LoadReviewNotes(o.opts.RunDir)
	if err != nil {
		t.Fatal(err)
	}
	if onDisk.Notes[0].Text != "updated" {
		t.Fatalf("text not updated: %+v", onDisk.Notes[0])
	}
}

// TestUpdateNote_NotFound закрывает Task 18: обновление несуществующего id
// возвращает ErrNoteNotFound, а не тихий no-op.
func TestUpdateNote_NotFound(t *testing.T) {
	o := pausedNotesTestOrch(t, stubResolveFile("sha256:x", "orig", true))
	if _, err := o.UpdateNote("n404", "updated", 0); !errors.Is(err, ErrNoteNotFound) {
		t.Fatalf("err=%v, want ErrNoteNotFound", err)
	}
}

// TestDeleteNote_Success закрывает Task 18: DeleteNote убирает ноту и
// поднимает Rev; повторное удаление того же id — ErrNoteNotFound.
func TestDeleteNote_Success(t *testing.T) {
	o := pausedNotesTestOrch(t, stubResolveFile("sha256:x", "orig", true))
	note, rev, err := o.AddNote("project", "a.go", nil, "first", "sha256:x", 0)
	if err != nil {
		t.Fatal(err)
	}

	newRev, err := o.DeleteNote(note.ID, rev)
	if err != nil {
		t.Fatalf("DeleteNote: %v", err)
	}
	if newRev != rev+1 {
		t.Fatalf("newRev=%d, want %d", newRev, rev+1)
	}

	onDisk, err := state.LoadReviewNotes(o.opts.RunDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(onDisk.Notes) != 0 {
		t.Fatalf("note not deleted: %+v", onDisk.Notes)
	}

	if _, err := o.DeleteNote(note.ID, newRev); !errors.Is(err, ErrNoteNotFound) {
		t.Fatalf("second delete err=%v, want ErrNoteNotFound", err)
	}
}

// TestListNotes_NoPauseGate закрывает Task 18: ListNotes читает
// review-notes.json без проверки паузы — доступен даже когда review-pause
// вообще не активен.
func TestListNotes_NoPauseGate(t *testing.T) {
	o := newPauseFlowTestOrch(t, []flow.Stage{{ID: "s1", Agents: []flow.AgentType{flow.AgentImplementation}}})
	notes, err := o.ListNotes()
	if err != nil {
		t.Fatalf("ListNotes: %v", err)
	}
	if notes.Rev != 0 || len(notes.Notes) != 0 {
		t.Fatalf("expected empty notes, got %+v", notes)
	}
}
