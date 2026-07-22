# Scenario Test Harness (Phase 1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Декларативный scenario-driven интеграционный харнесс со «синтетической» моделью в `package orchestrator_test`: `Scenario`-таблица + драйвер `runScenario`, покрывающий happy-flow и ошибочные сценарии через два сейма (`scriptedRunner` для неинтерактивных стадий, генерируемый bash-`synthAgent` для диалоговых).

**Architecture:** Test-only код в `pkg/orchestrator/*_test.go` (external test package — как существующие интеграционные тесты). Опирается на реальные паттерны из репозитория: `supervisorTestRunner` (`integration_supervisor_test.go:26-80`) для мок-Runner, `TestIntegration_MisprefixedQuestionNormalized`/`TestIntegration_MisplacedQuestionRelocated` (`integration_interactive_test.go`) для bash-агента и диалога, `waitForStatus` (`integration_test.go:104`) для ожидания статусов.

**Tech Stack:** Go `testing`. `orchestrator.New`, `orchestrator.StoreFromOrch`, `(*Orchestrator).NotifyAnswer`, `state.Open`, `executor.Runner`, `flow.Stage`.

## Global Constraints
- НЕ менять версию Go в go.mod. Только тестовый код (`_test.go`), продакшн не трогать.
- Коммиты на русском языке, без Co-Authored-By.
- `go build ./...`, `go vet ./pkg/orchestrator/...`, `go test -count=1 ./pkg/orchestrator/...` — зелёные после каждой задачи.
- НЕ трогать `accept.yaml`. Существующие тесты НЕ удалять.

## Ключевые факты о сеймах (для реализации)
- Неинтерактивная стадия с `Command==""` → использует инжектированный `Options.Runner`. Интерактивная стадия → `runnerFor` строит реальный `executor.New` по `stage.Command` и выставляет `AFM_STAGE_DIR=stageDir` (bash-агент пишет туда question/answer/артефакты). Autonomous-фаза тоже получает `AFM_STAGE_DIR`.
- Валидный `plan.md` для `checkPlanCompletion` содержит `## Tasks`, `## Assumptions`, `## Acceptance Criteria` (см. `supervisorTestRunner.RunPlanning`). Autonomous-завершение = наличие `execution_summary.md`. Implementation-завершение = непустой `.done`.
- Мок-Runner выводит `stageDir` из `logFile` (`filepath.Dir(logFile)`), т.к. реальный executor не задаёт cwd (см. коммент в `supervisorTestRunner`).

---

### Task 1: харнесс-ядро + неинтерактивные сценарии

**Files:**
- Create: `pkg/orchestrator/scenario_harness_test.go` (типы + `scriptedRunner` + `runScenario`)
- Create: `pkg/orchestrator/scenario_test.go` (таблица сценариев + `TestScenarios`)

**Interfaces produced:**
- Типы `Scenario`, `AgentSpec`, `Injection`, `QuestionFault`, `QuestionInject`, `Expectation` (как в спеке).
- `scriptedRunner` (реализует `executor.Runner`).
- `func runScenario(t *testing.T, sc Scenario)`.

- [ ] **Step 1: Определить типы сценария**

В `pkg/orchestrator/scenario_harness_test.go` (package `orchestrator_test`), с импортами `context, os, path/filepath, testing, time` + `pkg/config, pkg/executor, pkg/flow, pkg/orchestrator, pkg/state`:

```go
type Injection int

const (
	InjectNone Injection = iota
	InjectRateLimitThenOK
	InjectNoDone
)

type QuestionFault int

const (
	FaultNone QuestionFault = iota
	FaultWrongFolder
	FaultWrongPrefix
)

type QuestionInject struct {
	Phase  string
	ID     string
	Answer string // "" → никогда не отвечать (зависание)
	Fault  QuestionFault
}

type AgentSpec struct {
	Interactive bool
	Question    *QuestionInject
	Inject      Injection
}

type Expectation struct {
	Statuses             map[string]state.StageStatus
	FilesPresent         map[string][]string // stageID → относительные пути, которые должны быть
	FilesAbsent          map[string][]string // stageID → пути, которых быть не должно
	ReachesAwaitingInput []string
	RunErrSubstr         string
}

type Scenario struct {
	Name       string
	Stages     []flow.Stage
	Supervisor []byte
	Agents     map[string]AgentSpec
	Expect     Expectation
}
```

- [ ] **Step 2: Реализовать `scriptedRunner`**

Мок `executor.Runner`, конфигурируемый картой `agents map[string]AgentSpec` и `supervisor []byte`. Логика (модель — `supervisorTestRunner`, `rateLimitThenSuccessRunner`, `noDoneRunner`):

```go
type scriptedRunner struct {
	agents     map[string]AgentSpec
	supervisor []byte
	mu         sync.Mutex
	calls      map[string]int // ключ agentType|logFile → счётчик (для RateLimitThenOK)
}

func newScriptedRunner(agents map[string]AgentSpec, supervisor []byte) *scriptedRunner {
	return &scriptedRunner{agents: agents, supervisor: supervisor, calls: map[string]int{}}
}

func (r *scriptedRunner) RunJSONQuery(_ context.Context, _ string) ([]byte, error) {
	return r.supervisor, nil
}

// RunPlanning пишет валидный plan.md.
func (r *scriptedRunner) RunPlanning(_ context.Context, _, _, outFile, _ string) error {
	plan := "## Tasks\n- [ ] step 1\n## Assumptions\n- none\n## Acceptance Criteria\n- [ ] done\n"
	return os.WriteFile(outFile, []byte(plan), 0644)
}

// RunAgent пишет артефакт по фазе; поддерживает инъекции. stageID берётся из
// имени stageDir (filepath.Base(filepath.Dir(logFile))).
func (r *scriptedRunner) RunAgent(_ context.Context, agentType, _, _, logFile string) error {
	stageDir := filepath.Dir(logFile)
	spec := r.agents[filepath.Base(stageDir)]
	switch spec.Inject {
	case InjectRateLimitThenOK:
		r.mu.Lock()
		k := agentType + "|" + logFile
		r.calls[k]++
		n := r.calls[k]
		r.mu.Unlock()
		if n == 1 {
			return fmt.Errorf("rate limit exceeded") // retryable — см. isRetryableError
		}
	case InjectNoDone:
		return nil // «отработал», но артефакт не создан → incomplete
	}
	if agentType == "autonomous_execution" {
		return os.WriteFile(filepath.Join(stageDir, "execution_summary.md"), []byte("## Summary\ndone\n"), 0644)
	}
	return os.WriteFile(filepath.Join(stageDir, ".done"), []byte("done\n"), 0644)
}
```
(Добавить import `fmt`, `sync`. Проверить точную формулировку retryable-ошибки по `isRetryableError` в `pkg/orchestrator/retry.go` — использовать строку, которую та распознаёт как rate-limit; если она матчит подстроку `"rate limit"` — оставить как выше, иначе подогнать.)

- [ ] **Step 3: Реализовать `runScenario` (неинтерактивный путь)**

```go
func runScenario(t *testing.T, sc Scenario) {
	t.Helper()
	runDir := t.TempDir()
	ids := make([]string, len(sc.Stages))
	for i, s := range sc.Stages {
		ids[i] = s.ID
	}
	store, err := state.Open(runDir, ids)
	if err != nil {
		if sc.Expect.RunErrSubstr != "" && strings.Contains(err.Error(), sc.Expect.RunErrSubstr) {
			return // ожидаемая ошибка открытия (corrupt-log сценарий, Task 3)
		}
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	runner := newScriptedRunner(sc.Agents, sc.Supervisor)
	orch := orchestrator.New(orchestrator.Options{
		RunDir:           runDir,
		Stages:           sc.Stages,
		Store:            store,
		Config:           config.Default(),
		Prompts:          orchestrator.DefaultPrompts(),
		Runner:           runner,
		SupervisorRunner: runner,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- orch.Run(ctx) }()

	// Авто-ответ на диалоги (Task 2 наполнит; здесь no-op, если нет интерактивных).
	answerDialogs(t, orch, runDir, sc)

	select {
	case err := <-done:
		if sc.Expect.RunErrSubstr != "" {
			if err == nil || !strings.Contains(err.Error(), sc.Expect.RunErrSubstr) {
				t.Fatalf("expected Run error containing %q, got %v", sc.Expect.RunErrSubstr, err)
			}
		} else if err != nil && err != context.DeadlineExceeded {
			t.Fatalf("orch.Run: %v", err)
		}
	case <-time.After(35 * time.Second):
		t.Fatal("scenario timed out")
	}

	assertExpectation(t, orch, runDir, sc.Expect)
}
```
Добавить хелперы:
```go
// answerDialogs ждёт awaiting_user_input по стадиям с Question.Answer!="" и отвечает.
// Заглушка на Task 1 (интерактивных стадий нет) — наполняется в Task 2.
func answerDialogs(t *testing.T, orch *orchestrator.Orchestrator, runDir string, sc Scenario) {}

func assertExpectation(t *testing.T, orch *orchestrator.Orchestrator, runDir string, e Expectation) {
	t.Helper()
	store := orchestrator.StoreFromOrch(orch)
	for id, want := range e.Statuses {
		if got := store.Get(id); got != want {
			t.Errorf("stage %s: status = %s, want %s", id, got, want)
		}
	}
	for id, files := range e.FilesPresent {
		for _, f := range files {
			if _, err := os.Stat(filepath.Join(runDir, id, f)); err != nil {
				t.Errorf("stage %s: expected file %q present: %v", id, f, err)
			}
		}
	}
	for id, files := range e.FilesAbsent {
		for _, f := range files {
			if _, err := os.Stat(filepath.Join(runDir, id, f)); err == nil {
				t.Errorf("stage %s: file %q should be absent", id, f)
			}
		}
	}
}
```
(Добавить import `strings`. `assertExpectation` пока не проверяет `ReachesAwaitingInput` — это Task 2.)

- [ ] **Step 4: Написать 4 неинтерактивных сценария**

В `pkg/orchestrator/scenario_test.go`:

```go
func TestScenarios(t *testing.T) {
	scenarios := []Scenario{
		{
			Name: "happy-multistage",
			Stages: []flow.Stage{
				{ID: "plan-stage", Agents: []flow.AgentType{flow.AgentPlanning}, Plan: ""},
				{ID: "impl-stage", Agents: []flow.AgentType{flow.AgentImplementation}, Plan: "plan-stage-plan", DependsOn: []string{"plan-stage"}},
			},
			Agents: map[string]AgentSpec{
				"plan-stage": {}, "impl-stage": {},
			},
			Expect: Expectation{Statuses: map[string]state.StageStatus{
				"plan-stage": state.StatusDone, "impl-stage": state.StatusDone,
			}},
		},
		{
			Name: "supervisor-autonomous",
			Stages: []flow.Stage{
				{ID: "auto1", Supervisor: true, Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
			},
			Supervisor: []byte(`{"can_execute_autonomously":true,"reason":"x","recommended_phases":["autonomous_execution"]}`),
			Agents:     map[string]AgentSpec{"auto1": {}},
			Expect: Expectation{
				Statuses:     map[string]state.StageStatus{"auto1": state.StatusDone},
				FilesPresent: map[string][]string{"auto1": {"autonomous.flag", "execution_summary.md"}},
				FilesAbsent:  map[string][]string{"auto1": {"plan.md"}},
			},
		},
		{
			Name:   "auto-phase",
			Stages: []flow.Stage{{ID: "hard", Agents: []flow.AgentType{flow.AgentAuto}}},
			Agents: map[string]AgentSpec{"hard": {}},
			Expect: Expectation{
				Statuses:     map[string]state.StageStatus{"hard": state.StatusDone},
				FilesPresent: map[string][]string{"hard": {"autonomous.flag", "execution_summary.md"}},
				FilesAbsent:  map[string][]string{"hard": {"plan.md"}},
			},
		},
		{
			Name:   "retry-after-transient",
			Stages: []flow.Stage{{ID: "flaky", Agents: []flow.AgentType{flow.AgentImplementation}, Plan: "flaky-plan"}},
			Agents: map[string]AgentSpec{"flaky": {Inject: InjectRateLimitThenOK}},
			Expect: Expectation{Statuses: map[string]state.StageStatus{"flaky": state.StatusDone}},
		},
	}
	for _, sc := range scenarios {
		t.Run(sc.Name, func(t *testing.T) { runScenario(t, sc) })
	}
}
```
ПРИМЕЧАНИЕ по non-planning стадиям с `Plan`: стадия без planning-агента активируется как pre-planned и копирует `plan.md` из `resolvePlanSource(runDir, s)`. Проверить, как тесты задают источник плана для non-planning стадий (см. `doneCreatingRunner`/pre-planned тесты в `integration_test.go`); при необходимости задать `Plan` путём к существующему файлу или сделать `impl-stage` через planning+implementation, чтобы не завязываться на внешний план. Если pre-planned источник неудобен — заменить `impl-stage`/`flaky` на стадию с `Agents: [planning, implementation]` (planning напишет plan.md через scriptedRunner.RunPlanning, дальше implementation). Реализатор выбирает рабочий вариант, сверяясь с существующими pre-planned тестами; инвариант сценария (happy → done; flaky → done после ретрая) сохранить.

- [ ] **Step 5: Прогнать**

Run: `go build ./... && go vet ./pkg/orchestrator/... && go test -count=1 ./pkg/orchestrator/ -run TestScenarios -v`
Expected: 4 подтеста PASS. Если happy/retry падают из-за pre-planned источника плана — переключить те стадии на planning+implementation (см. примечание Step 4) до зелёного.

- [ ] **Step 6: Коммит**

```bash
git add pkg/orchestrator/scenario_harness_test.go pkg/orchestrator/scenario_test.go
git commit -m "test(orchestrator): scenario-харнесс + неинтерактивные сценарии (happy/supervisor/auto/retry)"
```

---

### Task 2: synthAgent (bash) + диалоговые сценарии

**Files:**
- Modify: `pkg/orchestrator/scenario_harness_test.go` (добавить `writeSynthAgent`, нарастить `answerDialogs`, wiring интерактивных стадий в `runScenario`, `ReachesAwaitingInput` в `assertExpectation`)
- Modify: `pkg/orchestrator/scenario_test.go` (добавить 4 диалоговых сценария)

**Interfaces:** `func writeSynthAgent(t *testing.T, spec AgentSpec) string` — путь к bash-скрипту для `stage.Command`.

- [ ] **Step 1: `writeSynthAgent` (bash-генератор)**

Модель — скрипты в `TestIntegration_MisprefixedQuestionNormalized`/`TestIntegration_MisplacedQuestionRelocated`. Скрипт для `AgentSpec` с `Question`:
- вычисляет путь вопроса: по умолчанию `$AFM_STAGE_DIR/<phase>.<id>.question.json`; при `FaultWrongPrefix` — `$AFM_STAGE_DIR/<stageID>.<id>.question.json` (префикс = имя каталога стадии, `basename $AFM_STAGE_DIR`); при `FaultWrongFolder` — `<временная чужая папка>/<phase>.<id>.question.json` (напр. `$AFM_STAGE_DIR/../wrong-<id>/`);
- пишет question.json (`{"id":"<id>","question":"q?","options":["a","b"]}`);
- эмитит stream-json Write-событие с этим путём в stdout (поллер читает `WrittenFiles`): `echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":"<путь>","content":"..."}}]}}'`;
- поллит answer по СВОЕМУ пути `<phase|stageID>.<id>.answer.json` в своей папке (соответствует тому, куда relocate/normalize кладёт симлинк): `while [ ! -f "<answerPath>" ]; do sleep 0.2; done` — с ограничением по числу итераций, чтобы при `Answer==""` не висеть вечно (напр. 100 итераций ≈ 20с);
- после ответа (или если `Question==nil`) пишет completion-артефакт по фазе: planning → `$AFM_STAGE_DIR/plan.md` (валидный контракт), autonomous → `execution_summary.md`, иначе `.done`.

Точные детали путей относительно stageDir и формат stream-json СВЕРИТЬ с двумя model-тестами построчно; сохранить их проверенный паттерн (там это уже работает). Скрипт писать через `os.WriteFile(path, []byte(script), 0o755)` в `t.TempDir()`.

- [ ] **Step 2: Wiring интерактивных стадий в `runScenario`**

Перед `orchestrator.New`: для каждой стадии, чей `AgentSpec.Interactive==true`, выставить `stage.Interactive=true` и `stage.Command = writeSynthAgent(t, spec)` (модифицируя копию `sc.Stages`, передаваемую в Options и в Store ids). Убедиться, что `config.Default()` даёт короткий idle-timeout или использовать ctx-таймаут (30с) как ограничитель.

- [ ] **Step 3: Нарастить `answerDialogs` и `ReachesAwaitingInput`**

`answerDialogs`: для каждой стадии с `Question != nil && Question.Answer != ""` — дождаться `awaiting_user_input` (поллить `orchestrator.StoreFromOrch(orch).Get(id)` до `state.StatusAwaitingUserInput` с дедлайном ~15с), затем `orch.NotifyAnswer(id, correctPhase, Question.ID, Question.Answer, false)`. Для стадий из `Expect.ReachesAwaitingInput` без ответа (`Answer==""`) — только дождаться `awaiting_user_input` и запомнить факт (для ассерта).
Расширить `assertExpectation`: для каждого id в `ReachesAwaitingInput` подтвердить, что стадия достигала `awaiting_user_input` (проверить текущий статус или зафиксировать флаг во время ожидания). Для нормализованного/relocated вопроса phase в `NotifyAnswer` — каноническая (planning), т.к. поллер публикует исправленную фазу.

- [ ] **Step 4: 4 диалоговых сценария**

Добавить в таблицу `TestScenarios`:
- `interactive-dialog-happy`: стадия `Interactive`, `Question{Phase:"planning", ID:"q1", Answer:"да", Fault:FaultNone}` → `Statuses: done`.
- `misprefixed-question`: `Question{Phase:"planning", ID:"q1", Answer:"да", Fault:FaultWrongPrefix}` → `ReachesAwaitingInput:[stage]`, финально `done`.
- `wrong-folder-question`: `Fault:FaultWrongFolder`, `Answer:"да"` → `ReachesAwaitingInput:[stage]`, финально `done`.
- `hung-dialog`: `Question{..., Answer:""}` → `ReachesAwaitingInput:[stage]`, статус остаётся `awaiting_user_input` (в `Statuses`).

- [ ] **Step 5: Прогнать**

Run: `go test -count=1 ./pkg/orchestrator/ -run TestScenarios -v` → все 8 подтестов PASS.
Run: `go build ./... && go vet ./pkg/orchestrator/...` → чисто.

- [ ] **Step 6: Коммит**

```bash
git add pkg/orchestrator/scenario_harness_test.go pkg/orchestrator/scenario_test.go
git commit -m "test(orchestrator): synthAgent + диалоговые сценарии (happy/misprefixed/wrong-folder/hung)"
```

---

### Task 3: corrupt-log сценарий + негативный само-тест

**Files:**
- Modify: `pkg/orchestrator/scenario_harness_test.go` (поле `PreSeedEventsLog []byte` в `Scenario` + ветка в `runScenario`)
- Modify: `pkg/orchestrator/scenario_test.go` (сценарий #9 + негативный само-тест)

- [ ] **Step 1: Поле предзасева лога + ветка в runScenario**

Добавить в `Scenario`: `PreSeedEventsLog []byte`. В `runScenario`, СРАЗУ после `runDir := t.TempDir()` и до `state.Open`: если `sc.PreSeedEventsLog != nil` — записать его в `filepath.Join(runDir, "events.jsonl")`. Существующая ветка обработки `state.Open`-ошибки (Step 3 Task 1) уже проверяет `RunErrSubstr` при сбое Open → corrupt-log сценарий отработает через неё.

- [ ] **Step 2: Сценарий corrupt-log + негативный само-тест**

В `scenario_test.go` добавить сценарий:
```go
{
	Name:   "corrupt-log-recovery",
	Stages: []flow.Stage{{ID: "a", Agents: []flow.AgentType{flow.AgentPlanning}}},
	PreSeedEventsLog: []byte(
		`{"seq":1,"stage_id":"a","from":"pending","to":"planning","event":"x"}` + "\n" +
			`NOT JSON AT ALL` + "\n" +
			`{"seq":2,"stage_id":"a","from":"planning","to":"done","event":"y"}` + "\n"),
	Expect: Expectation{RunErrSubstr: "corrupted"},
},
```
(`state.Open` на mid-corruption вернёт `ErrCorruptLog` — `events.jsonl is corrupted mid-log` — подстрока `"corrupted"` матчит.)

Отдельный негативный само-тест (в новом `func TestScenarioHarness_FailsOnWrongExpectation`): гонять минимальный сценарий с ЗАВЕДОМО неверным `Expect.Statuses` через вложенный `testing.T`-подтест и подтвердить, что харнесс сообщает провал (ассерты не no-op). Реализация: запустить happy-сценарий, но ожидать `StatusFailed` вместо `StatusDone`, обернув в под-`t.Run` и проверив, что он зафейлился. Простейший надёжный вариант — вынести проверочную логику так, чтобы её можно было позвать с фиктивным `*testing.T` и увидеть `Failed()==true`; если это громоздко — задокументировать и сделать через `t.Run` + проверку, что подтест упал (стандартный приём: не тривиален — реализатор выбирает надёжный способ, но само-тест ОБЯЗАН реально проверять срабатывание ассертов, а не быть пустым).

- [ ] **Step 3: Прогнать всё**

Run: `go test -count=1 ./pkg/orchestrator/ -run 'TestScenarios|TestScenarioHarness' -v` → все 9 сценариев + само-тест PASS.
Run: `go build ./... && go vet ./... && go test -count=1 ./pkg/orchestrator/...` → всё зелёное (существующие тесты не задеты — харнесс только добавляет файлы).

- [ ] **Step 4: Коммит**

```bash
git add pkg/orchestrator/scenario_harness_test.go pkg/orchestrator/scenario_test.go
git commit -m "test(orchestrator): corrupt-log сценарий + негативный само-тест харнесса"
```

---

## Self-Review

**Spec coverage:** два сейма (scriptedRunner Task 1 + synthAgent Task 2); `Scenario`/`AgentSpec`/`Expectation` типы (Task 1 Step 1); инъекции ошибок (RateLimit/NoDone — Task 1; WrongPrefix/WrongFolder/hung — Task 2; corrupt-log — Task 3); 9 seed-сценариев (4+4+1); негативный само-тест (Task 3). ✓

**Placeholder scan:** типы, scriptedRunner, runScenario, assertExpectation, сценарные таблицы приведены кодом. Два места делегируют выбор реализатору с явным инвариантом и ссылкой на конкретные model-тесты: (a) источник plan.md для non-planning стадий (Task 1 Step 4) — потому что зависит от того, как pre-planned стадии берут план в существующих тестах; (b) точный bash `writeSynthAgent` (Task 2 Step 1) — построчно моделируется по двум реальным проверенным тестам, дублировать их целиком в план нецелесообразно, паттерн задан. Это не placeholder-заглушки, а привязка к существующему рабочему коду. ✓

**Type consistency:** `Scenario`/`AgentSpec`/`Expectation`/`QuestionInject` определены в Task 1, потребляются в Task 2/3; `PreSeedEventsLog` добавлен в Task 3 к тому же `Scenario`. `runScenario`/`answerDialogs`/`assertExpectation`/`writeSynthAgent`/`scriptedRunner` — согласованы между задачами. Сигнатуры `orchestrator.New`/`StoreFromOrch`/`NotifyAnswer(id,phase,qID,answer,fromOptions)`/`state.Open`/`executor.Runner` — реальные (сверены с репозиторием). ✓

**Порядок:** Task 1 (ядро + Runner-сейм) → Task 2 (bash-сейм + диалог, зависит от драйвера) → Task 3 (corrupt-log + само-тест). Последовательно.
