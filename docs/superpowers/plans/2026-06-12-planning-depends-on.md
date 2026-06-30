# Гейтинг фазы planning по depends_on — план имплементации

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** По умолчанию planning стейджа запускается только когда все его `depends_on` в статусе `done`; флаг `eager_planning: true` возвращает старое поведение (планировать сразу).

**Architecture:** Гейт в `startPlanningForPending` (recovery.go) оставляет зависимые стейджи в `pending`; новый метод `startPlanningForUnblocked` запускает их планирование из event loop, когда зависимости завершаются. Никаких новых статусов FSM. Спека: `docs/superpowers/specs/2026-06-12-planning-depends-on-design.md`.

**Tech Stack:** Go 1.26 (версию в go.mod не менять), yaml.v3, стандартный testing. Тесты — в стиле существующих интеграционных: мок-раннеры поверх `executor.Runner`, bash-скрипты, `setupOrchestratorWithRunner`, `autoApprove`.

**Контекст для исполнителя без знания кодовой базы:**

- `pkg/flow/flow.go` — структура `Stage` (yaml-описание стейджа), `NeedsPlanning()` = «нет готового плана и есть planning-агент».
- `pkg/orchestrator/orchestrator.go` — event loop (`Run` → `handleEvent`), `depsDone(s)` проверяет что все `depends_on` в `done`, `startReadyStages` запускает implementation, `tryActivatePrePlanned` активирует стейджи с готовым планом.
- `pkg/orchestrator/recovery.go` — `startPlanningForPending` вызывается один раз при старте `Run`; сейчас запускает planning для ВСЕХ pending-стейджей сразу (комментарий «planning runs eagerly before deps are done»).
- `pkg/orchestrator/fsm.go` — FSM-переходы; `EvStartPlanning` разрешён только из `pending`/`retrying`/`revising`, повторный `Trigger` возвращает `ok == false` (безопасный no-op).
- Статусы стейджей хранятся в `state.Store` (`o.opts.Store.Get(id)`), новый стейдж — `state.StatusPending`.
- В мок-раннерах `RunPlanning(ctx, stageName, ...)` и `RunAgent(ctx, agentType, stageName, ...)` параметр `stageName` — это `Stage.Name` (не ID).
- Прогон тестов: `go test ./pkg/... -race` (или `make test` — то же с `-v`). Линт: `make lint`.

---

### Task 1: Поле `eager_planning` в flow.Stage

**Files:**
- Modify: `pkg/flow/flow.go:58` (struct `Stage`)
- Test: `pkg/flow/flow_test.go`

- [x] **Step 1: Написать падающий тест парсинга**

Добавить в `pkg/flow/flow_test.go` (после константы `noPlanningYAML`, рядом с другими yaml-константами):

```go
const eagerPlanningYAML = `
name: eager-flow
description: "eager planning"
stages:
  - id: base
    name: "Base"
    description: "base stage"
    agents: [planning, implementation]
  - id: dependent
    name: "Dependent"
    description: "plans eagerly"
    depends_on: [base]
    eager_planning: true
    agents: [planning, implementation]
`
```

И тест (рядом с `TestParseValidFlow`):

```go
func TestParseEagerPlanning(t *testing.T) {
	f, err := flow.ParseFile(writeTemp(t, eagerPlanningYAML))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !f.Stages[1].EagerPlanning {
		t.Errorf("eager_planning: got false, want true")
	}
	if f.Stages[0].EagerPlanning {
		t.Errorf("eager_planning default: got true, want false")
	}
}
```

- [x] **Step 2: Убедиться что тест падает**

Run: `go test ./pkg/flow/ -run TestParseEagerPlanning -v`
Expected: COMPILE ERROR — `f.Stages[1].EagerPlanning undefined`.

- [x] **Step 3: Добавить поле в Stage**

В `pkg/flow/flow.go`, в структуре `Stage`, сразу после поля `DependsOn []string \`yaml:"depends_on"\``:

```go
	// EagerPlanning starts the planning agent at flow start without
	// waiting for depends_on stages to finish (legacy behavior).
	EagerPlanning bool `yaml:"eager_planning"`
```

- [x] **Step 4: Убедиться что тест проходит**

Run: `go test ./pkg/flow/ -race -v`
Expected: PASS (все тесты пакета).

- [x] **Step 5: Commit**

```bash
git add pkg/flow/flow.go pkg/flow/flow_test.go
git commit -m "feat: поле eager_planning в описании стейджа"
```

---

### Task 2: Гейт planning по depends_on + startPlanningForUnblocked

**Files:**
- Modify: `pkg/orchestrator/recovery.go:114-131` (ветка `default` в `startPlanningForPending`) и `pkg/orchestrator/recovery.go:133-142` (хвост функции)
- Modify: `pkg/orchestrator/orchestrator.go` — метод `startPlanningForUnblocked` (после `tryActivatePrePlanned`, ~строка 543), вызовы в `onAgentCompleted` (~строка 328) и `onApproved` (~строка 403)
- Test: `pkg/orchestrator/integration_test.go`

- [x] **Step 1: Написать падающий тест порядка запуска**

Добавить в `pkg/orchestrator/integration_test.go` хелпер (рядом с другими обёртками раннеров, например после `promptCapturingRunner`):

```go
// callRecordingRunner wraps a Runner and records the order of calls
// as "planning:<stageName>" / "agent:<stageName>".
type callRecordingRunner struct {
	delegate executor.Runner
	mu       sync.Mutex
	calls    []string
}

func (r *callRecordingRunner) record(kind, stageName string) {
	r.mu.Lock()
	r.calls = append(r.calls, kind+":"+stageName)
	r.mu.Unlock()
}

func (r *callRecordingRunner) callsSnapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string{}, r.calls...)
}

func (r *callRecordingRunner) RunPlanning(ctx context.Context, stageName, prompt, outFile, logFile string) error {
	r.record("planning", stageName)
	return r.delegate.RunPlanning(ctx, stageName, prompt, outFile, logFile)
}

func (r *callRecordingRunner) RunAgent(ctx context.Context, agentType, stageName, prompt, logFile string) error {
	r.record("agent", stageName)
	return r.delegate.RunAgent(ctx, agentType, stageName, prompt, logFile)
}
```

И тест:

```go
// TestIntegration_PlanningWaitsForDependencies verifies that by default the
// planning of a dependent stage starts only after its dependency is done.
func TestIntegration_PlanningWaitsForDependencies(t *testing.T) {
	stages := []flow.Stage{
		{ID: "first", Name: "First", Description: "runs first", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
		{ID: "second", Name: "Second", Description: "plans after first is done", DependsOn: []string{"first"}, Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
	}

	rec := &callRecordingRunner{delegate: &doneCreatingRunner{delegate: mockRunner(t, mockPlanningScript)}}
	orch, _, stateFile := setupOrchestratorWithRunner(t, stages, rec)

	cancel := autoApprove(orch)
	defer cancel()

	if err := orch.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	final := loadStateJSON(t, stateFile)
	for _, id := range []string{"first", "second"} {
		if final.Stages[id].Status != state.StatusDone {
			t.Errorf("stage %s: expected done, got %v", id, final.Stages[id].Status)
		}
	}

	calls := rec.callsSnapshot()
	idxImplFirst, idxPlanSecond := -1, -1
	for i, c := range calls {
		switch c {
		case "agent:First":
			if idxImplFirst == -1 {
				idxImplFirst = i
			}
		case "planning:Second":
			idxPlanSecond = i
		}
	}
	if idxImplFirst == -1 || idxPlanSecond == -1 {
		t.Fatalf("expected both agent:First and planning:Second in calls, got %v", calls)
	}
	if idxPlanSecond < idxImplFirst {
		t.Errorf("planning of second started before first finished, calls: %v", calls)
	}
}
```

- [x] **Step 2: Убедиться что тест падает**

Run: `go test ./pkg/orchestrator/ -run TestIntegration_PlanningWaitsForDependencies -race -v`
Expected: FAIL — «planning of second started before first finished» (сейчас оба планируются сразу при старте).

- [x] **Step 3: Гейт в startPlanningForPending**

В `pkg/orchestrator/recovery.go`, в ветке `default` второго `switch` (строки ~114-124), заменить:

```go
		default:
			// Pending, planning, or unknown — check if planning already completed
			if s.NeedsPlanning() {
				stageDir := filepath.Join(o.opts.RunDir, s.ID)
				if checkPlanCompletion(stageDir) == nil {
					o.Trigger(s.ID, EvPlanReady, GuardCtx{}, "recovered plan.md")
					continue
				}
			}
			// (Re)start planning (planning runs eagerly before deps are done)
			o.Trigger(s.ID, EvStartPlanning, GuardCtx{}, "")
```

на:

```go
		default:
			// Pending, planning, or unknown — check if planning already completed
			if s.NeedsPlanning() {
				stageDir := filepath.Join(o.opts.RunDir, s.ID)
				if checkPlanCompletion(stageDir) == nil {
					o.Trigger(s.ID, EvPlanReady, GuardCtx{}, "recovered plan.md")
					continue
				}
			}
			// Pending stages wait for depends_on unless eager_planning is set.
			// Interrupted planning (status "planning") always resumes.
			if current == state.StatusPending && !s.EagerPlanning && !o.depsDone(s) {
				continue
			}
			o.Trigger(s.ID, EvStartPlanning, GuardCtx{}, "")
```

(`go func(stage flow.Stage) {...}` после Trigger остаётся без изменений.)

- [x] **Step 4: Метод startPlanningForUnblocked**

В `pkg/orchestrator/orchestrator.go`, после функции `tryActivatePrePlanned` (~строка 543), добавить:

```go
// startPlanningForUnblocked starts planning for pending stages whose
// dependencies are all done. Stages with eager_planning start at flow
// start and are never gated here.
func (o *Orchestrator) startPlanningForUnblocked(ctx context.Context) {
	for _, s := range o.opts.Stages {
		if !s.NeedsPlanning() {
			continue
		}
		if o.opts.Store.Get(s.ID) != state.StatusPending {
			continue
		}
		if !o.depsDone(s) {
			continue
		}
		// Synchronous transition out of pending guards against double
		// start: a second call sees "planning" and skips the stage.
		if _, ok := o.Trigger(s.ID, EvStartPlanning, GuardCtx{Stage: s}, "deps done"); !ok {
			continue
		}
		go func(st flow.Stage) {
			sem := o.semFor(st)
			sem.acquire()
			defer sem.release()
			o.runPlanningAgent(ctx, st)
		}(s)
	}
}
```

- [x] **Step 5: Вызовы в точках завершения стейджей**

Три места:

1. `pkg/orchestrator/orchestrator.go`, `onAgentCompleted`, ветка `case phaseImplementation:` — заменить:

```go
		o.Trigger(ev.StageID, EvComplete, GuardCtx{}, "")
		o.failBlockedStages()
		o.startReadyStages(ctx)
		o.tryActivatePrePlanned(ctx)
```

на:

```go
		o.Trigger(ev.StageID, EvComplete, GuardCtx{}, "")
		o.failBlockedStages()
		o.startPlanningForUnblocked(ctx)
		o.startReadyStages(ctx)
		o.tryActivatePrePlanned(ctx)
```

2. `pkg/orchestrator/orchestrator.go`, `onApproved`, ветка planning-only стейджа — заменить:

```go
	if stage != nil && !stage.HasAgent(flow.AgentImplementation) {
		// Planning-only stage — nothing to implement, mark as done.
		o.Trigger(ev.StageID, EvComplete, GuardCtx{}, "planning-only stage")
		o.startReadyStages(ctx)
		o.tryActivatePrePlanned(ctx)
		return nil
	}
```

на:

```go
	if stage != nil && !stage.HasAgent(flow.AgentImplementation) {
		// Planning-only stage — nothing to implement, mark as done.
		o.Trigger(ev.StageID, EvComplete, GuardCtx{}, "planning-only stage")
		o.startPlanningForUnblocked(ctx)
		o.startReadyStages(ctx)
		o.tryActivatePrePlanned(ctx)
		return nil
	}
```

3. `pkg/orchestrator/recovery.go`, хвост `startPlanningForPending` — заменить:

```go
	// Cascade failures to stages blocked by failed dependencies.
	o.failBlockedStages()

	// Start implementation for stages that are ready.
	o.startReadyStages(ctx)
```

на:

```go
	// Cascade failures to stages blocked by failed dependencies.
	o.failBlockedStages()

	// Start planning for stages whose dependencies are already done
	// (covers recovery where a dependency was recovered as done above).
	o.startPlanningForUnblocked(ctx)

	// Start implementation for stages that are ready.
	o.startReadyStages(ctx)
```

- [x] **Step 6: Убедиться что новый тест проходит**

Run: `go test ./pkg/orchestrator/ -run TestIntegration_PlanningWaitsForDependencies -race -v`
Expected: PASS

- [x] **Step 7: Прогнать все тесты оркестратора и flow**

Run: `go test ./pkg/orchestrator/ ./pkg/flow/ -race`
Expected: PASS. Особое внимание: `TestIntegration_SequentialDependencies`, `TestIntegration_FailedDependencyCascade`, `TestIntegration_PlanningPromptIncludesDependencyPlan`, resume/interactive тесты — они должны пройти без правок. Если что-то упало — разбираться, не подгонять тесты.

- [x] **Step 8: Commit**

```bash
git add pkg/orchestrator/orchestrator.go pkg/orchestrator/recovery.go pkg/orchestrator/integration_test.go
git commit -m "feat: planning ждёт завершения depends_on по умолчанию"
```

---

### Task 3: Регрессионный тест eager_planning

Флаг уже реализован в Task 2 (условие `s.EagerPlanning ||` в гейте). Тест фиксирует поведение: стейдж с `eager_planning: true` планируется сразу при старте, не дожидаясь зависимостей.

**Files:**
- Test: `pkg/orchestrator/integration_test.go`

- [x] **Step 1: Написать тест с блокирующим раннером**

Добавить в `pkg/orchestrator/integration_test.go`:

```go
// eagerProbeRunner blocks planning of stage "First" until planning of
// "Second" is observed, proving that "Second" plans eagerly at startup.
type eagerProbeRunner struct {
	delegate   executor.Runner
	secondSeen chan struct{}
	once       sync.Once
}

func (r *eagerProbeRunner) RunPlanning(ctx context.Context, stageName, prompt, outFile, logFile string) error {
	if stageName == "Second" {
		r.once.Do(func() { close(r.secondSeen) })
	}
	if stageName == "First" {
		select {
		case <-r.secondSeen:
		case <-time.After(10 * time.Second):
			return fmt.Errorf("planning of Second did not start eagerly")
		}
	}
	return r.delegate.RunPlanning(ctx, stageName, prompt, outFile, logFile)
}

func (r *eagerProbeRunner) RunAgent(ctx context.Context, agentType, stageName, prompt, logFile string) error {
	return r.delegate.RunAgent(ctx, agentType, stageName, prompt, logFile)
}

// TestIntegration_EagerPlanningStartsImmediately verifies that a stage with
// eager_planning: true plans at flow start, before its dependency is done.
func TestIntegration_EagerPlanningStartsImmediately(t *testing.T) {
	stages := []flow.Stage{
		{ID: "first", Name: "First", Description: "runs first", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
		{ID: "second", Name: "Second", Description: "plans eagerly", DependsOn: []string{"first"}, EagerPlanning: true, Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
	}

	runner := &eagerProbeRunner{
		delegate:   &doneCreatingRunner{delegate: mockRunner(t, mockPlanningScript)},
		secondSeen: make(chan struct{}),
	}
	orch, _, stateFile := setupOrchestratorWithRunner(t, stages, runner)

	cancel := autoApprove(orch)
	defer cancel()

	if err := orch.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	final := loadStateJSON(t, stateFile)
	for _, id := range []string{"first", "second"} {
		if final.Stages[id].Status != state.StatusDone {
			t.Errorf("stage %s: expected done, got %v", id, final.Stages[id].Status)
		}
	}
}
```

Механика: если eager работает, оба планирования стартуют при запуске — Second закрывает канал, First продолжает, flow завершается. Если eager сломан, Second застрянет в pending, First отвалится по таймауту и финальные статусы будут failed.

`config.Default()` имеет `MaxParallel: 0` (без лимита) — параллельные планирования не сериализуются семафором, дедлока нет.

- [x] **Step 2: Прогнать тест**

Run: `go test ./pkg/orchestrator/ -run TestIntegration_EagerPlanningStartsImmediately -race -v`
Expected: PASS (флаг реализован в Task 2). Если FAIL — чинить условие гейта в `startPlanningForPending`, а не тест.

- [x] **Step 3: Commit**

```bash
git add pkg/orchestrator/integration_test.go
git commit -m "test: eager_planning планирует сразу при старте"
```

---

### Task 4: Регрессионный тест — обречённый стейдж не планируется

Фиксирует выигрыш от гейта: если зависимость упала, зависимый стейдж фейлится без запуска планирования (раньше planning запускался зря).

**Files:**
- Test: `pkg/orchestrator/integration_failure_test.go`

- [x] **Step 1: Написать тест**

Добавить в `pkg/orchestrator/integration_failure_test.go`:

```go
// TestIntegration_NoPlanningForDoomedStage verifies that when a dependency
// fails, the dependent stage is failed without ever starting its planning.
func TestIntegration_NoPlanningForDoomedStage(t *testing.T) {
	stages := []flow.Stage{
		{ID: "first", Name: "First", Description: "implementation fails", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
		{ID: "second", Name: "Second", Description: "never plans", DependsOn: []string{"first"}, Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
	}

	rec := &callRecordingRunner{delegate: &phaseDispatchRunner{
		planning: mockRunner(t, mockPlanningScript),
		other:    mockRunner(t, mockFailScript),
	}}
	orch, _, stateFile := setupOrchestratorWithRunner(t, stages, rec)

	cancel := autoApprove(orch)
	defer cancel()

	if err := orch.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	final := loadStateJSON(t, stateFile)
	for _, id := range []string{"first", "second"} {
		if final.Stages[id].Status != state.StatusFailed {
			t.Errorf("stage %s: expected failed, got %v", id, final.Stages[id].Status)
		}
	}

	for _, c := range rec.callsSnapshot() {
		if c == "planning:Second" {
			t.Errorf("second must never plan when first failed, calls: %v", rec.callsSnapshot())
		}
	}
}
```

В тесте нужны импорты `context`, `testing`, `flow`, `state` — они в файле уже есть. `callRecordingRunner`, `phaseDispatchRunner`, `mockRunner`, скрипты — из `integration_test.go` (тот же пакет `orchestrator_test`).

- [x] **Step 2: Прогнать тест**

Run: `go test ./pkg/orchestrator/ -run TestIntegration_NoPlanningForDoomedStage -race -v`
Expected: PASS (поведение обеспечено Task 2: second остаётся pending, `failBlockedStages` фейлит его каскадом).

- [x] **Step 3: Commit**

```bash
git add pkg/orchestrator/integration_failure_test.go
git commit -m "test: зависимый стейдж не планируется при упавшей зависимости"
```

---

### Task 5: Гейт в onManualRetry

Сейчас ручной retry упавшего стейджа сразу запускает его планирование, даже если зависимости не выполнены — это противоречит новому поведению. После правки: retry переводит стейдж в `pending`, планирование стартует автоматически через `startPlanningForUnblocked`, когда зависимости завершатся.

**Files:**
- Modify: `pkg/orchestrator/orchestrator.go` — `onManualRetry`, ветка `else` (~строка 488)
- Test: `pkg/orchestrator/integration_retry_test.go`

- [x] **Step 1: Написать падающий тест**

Добавить в `pkg/orchestrator/integration_retry_test.go`:

```go
// manualRetryRunner: planning of "First" always fails; implementation of
// "Blocker" blocks until released; everything else delegates.
type manualRetryRunner struct {
	delegate executor.Runner
	release  <-chan struct{}
}

func (r *manualRetryRunner) RunPlanning(ctx context.Context, stageName, prompt, outFile, logFile string) error {
	if stageName == "First" {
		return fmt.Errorf("planning exploded")
	}
	return r.delegate.RunPlanning(ctx, stageName, prompt, outFile, logFile)
}

func (r *manualRetryRunner) RunAgent(ctx context.Context, agentType, stageName, prompt, logFile string) error {
	if stageName == "Blocker" {
		select {
		case <-r.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return r.delegate.RunAgent(ctx, agentType, stageName, prompt, logFile)
}

// TestIntegration_ManualRetryWaitsForDeps verifies that manual retry of a
// failed dependent stage keeps it pending instead of starting planning while
// its dependency is still failed.
func TestIntegration_ManualRetryWaitsForDeps(t *testing.T) {
	stages := []flow.Stage{
		{ID: "first", Name: "First", Description: "fails at planning", Agents: []flow.AgentType{flow.AgentPlanning}},
		{ID: "second", Name: "Second", Description: "depends on first", DependsOn: []string{"first"}, Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
		{ID: "blocker", Name: "Blocker", Description: "keeps the flow alive", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
	}

	release := make(chan struct{})
	rec := &callRecordingRunner{delegate: &manualRetryRunner{
		delegate: &doneCreatingRunner{delegate: mockRunner(t, mockPlanningScript)},
		release:  release,
	}}
	orch, _, stateFile := setupOrchestratorWithRunner(t, stages, rec)

	cancel := autoApprove(orch)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- orch.Run(context.Background()) }()

	// first fails its planning, second is cascade-failed.
	waitForStatus(t, stateFile, "second", state.StatusFailed, 10*time.Second)

	// Manual retry of second: dependency is still failed — the stage must
	// stay pending and must not start planning.
	if err := orch.Retry(context.Background(), "second"); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, stateFile, "second", state.StatusPending, 10*time.Second)

	// Release the blocker; its completion re-fails second (dep still failed)
	// and the flow terminates.
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, c := range rec.callsSnapshot() {
		if c == "planning:Second" {
			t.Errorf("second must not plan after manual retry with failed dep, calls: %v", rec.callsSnapshot())
		}
	}

	final := loadStateJSON(t, stateFile)
	if final.Stages["second"].Status != state.StatusFailed {
		t.Errorf("second: expected failed, got %v", final.Stages["second"].Status)
	}
}
```

Механика: `blocker` держит event loop живым (его implementation висит на канале), пока тест делает `Retry("second")`. После `close(release)` blocker завершается, `onAgentCompleted` вызывает `failBlockedStages` — second из `pending` снова уходит в `failed` (зависимость first так и осталась failed), всё терминально, `Run` возвращается.

- [x] **Step 2: Убедиться что тест падает**

Run: `go test ./pkg/orchestrator/ -run TestIntegration_ManualRetryWaitsForDeps -race -v`
Expected: FAIL — в calls есть `planning:Second` (текущий код запускает планирование сразу после retry).

- [x] **Step 3: Добавить гейт в onManualRetry**

В `pkg/orchestrator/orchestrator.go`, в `onManualRetry`, заменить ветку `else` (запуск планирования при отсутствии plan.md):

```go
	} else {
		go func(st flow.Stage) {
			sem := o.semFor(st)
			sem.acquire()
			defer sem.release()
			o.runPlanningAgent(ctx, st)
		}(*stage)
	}
```

на:

```go
	} else {
		// Deps not done — stay pending; planning starts automatically
		// via startPlanningForUnblocked once dependencies complete.
		if !stage.EagerPlanning && !o.depsDone(*stage) {
			return nil
		}
		go func(st flow.Stage) {
			sem := o.semFor(st)
			sem.acquire()
			defer sem.release()
			o.runPlanningAgent(ctx, st)
		}(*stage)
	}
```

- [x] **Step 4: Убедиться что тест проходит**

Run: `go test ./pkg/orchestrator/ -run TestIntegration_ManualRetryWaitsForDeps -race -v`
Expected: PASS

- [x] **Step 5: Прогнать все тесты пакета**

Run: `go test ./pkg/orchestrator/ -race`
Expected: PASS (в т.ч. существующие retry-тесты).

- [x] **Step 6: Commit**

```bash
git add pkg/orchestrator/orchestrator.go pkg/orchestrator/integration_retry_test.go
git commit -m "fix: ручной retry не запускает planning при невыполненных depends_on"
```

---

### Task 6: Документация, полный прогон, линт

**Files:**
- Modify: `README.md` (таблица полей стадии ~строка 145; описание `pending` в разделе «Жизненный цикл стадии» ~строка 288)
- Modify: `example-flow.yaml` (комментарий у `depends_on`)

- [x] **Step 1: README — строка таблицы полей**

В таблицу «Поля стадии», после строки `| depends_on | ... |`, добавить:

```markdown
| `eager_planning` | нет | `true` — planning стартует сразу при запуске flow, не дожидаясь `depends_on`. По умолчанию planning ждёт завершения зависимостей |
```

- [x] **Step 2: README — жизненный цикл**

В разделе «Жизненный цикл стадии» заменить строку:

```markdown
- `pending` — ещё не запущена
```

на:

```markdown
- `pending` — ещё не запущена; planning стартует только после завершения всех `depends_on` (если не задан `eager_planning: true`)
```

- [x] **Step 3: example-flow.yaml — комментарий**

В `example-flow.yaml` найти первую строку с `depends_on: [backend-auth]` (строка ~46) и дополнить комментарием на следующей строке (с тем же отступом, что и `depends_on`):

```yaml
    depends_on: [backend-auth]
    # planning этой стадии стартует после завершения backend-auth;
    # eager_planning: true — планировать сразу, не дожидаясь
```

- [x] **Step 4: Полный прогон тестов**

Run: `go test ./... -race`
Expected: PASS по всем пакетам.

- [x] **Step 5: Линт**

Run: `make lint`
Expected: без ошибок (golangci-lint v2.11.4 + setstatuslinter). Замечания починить.

- [x] **Step 6: Commit**

```bash
git add README.md example-flow.yaml
git commit -m "docs: описание гейтинга planning по depends_on и флага eager_planning"
```

- [x] **Step 7: Обновить бинарь**

Бинарь живёт в `~/homebrew/bin/flowmanager` (не в GOPATH — `go install` его не обновляет):

```bash
go build -o ~/homebrew/bin/flowmanager ./cmd/flowmanager
```

Проверка: `ls -la ~/homebrew/bin/flowmanager` — дата должна быть текущей.

- [x] **Step 8: Финальный коммит бинаря (если bin/ версионируется)**

В репозитории есть `bin/` и коммиты вида «new bin». Собрать локальный бинарь и закоммитить, только если `git status` показывает изменения в отслеживаемых файлах `bin/`:

```bash
make build
git status --short bin/
```

Если есть изменения: `git add bin/ && git commit -m "bin up"`. Если `bin/` в .gitignore — пропустить шаг.
