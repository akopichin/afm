# Scenario-driven integration-test harness (синтетическая модель) — Phase 1

**Дата:** 2026-07-21
**Статус:** design (одобрен к реализации)

## Цель

Систематизировать интеграционное тестирование afm в декларативный scenario-driven харнесс с «синтетической» (скриптованной) моделью: захардкоженные ответы агента, покрывающие happy-flow И ошибочные сценарии (вопрос в чужой папке, mis-prefixed вопрос, зависший диалог, неверный трек супервизора, битый лог). Цель — покрыть функционал afm и ловить регрессии/деградацию между версиями. Сегодня такие тесты существуют, но разрознены (~10 ad-hoc mock-раннеров + разовые bash-скрипты); харнесс делает добавление сценариев декларативным.

**Scope Phase 1:** in-process харнесс (Go-тесты: `orchestrator.New` + скриптуемый агент) + набор из 9 seed-сценариев. **Phase 2** (отдельный спек позже): прогон тех же `Scenario` через собранный бинарь (`afm run`), ассерты по `.afm/runs`.

## Контекст: два сейма синтетической модели

1. **Инжектируемый `executor.Runner`** (интерфейс: `RunPlanning`, `RunAgent`, `RunJSONQuery`) — используется для НЕинтерактивных стадий. Быстрый in-process мок. Прецеденты: `supervisorTestRunner`, `rateLimitThenSuccessRunner`, `noDoneRunner`, `doneCreatingRunner` и др.
2. **`stage.Command` bash-скрипт** — интерактивные стадии (`stage.Interactive=true`) ИГНОРИРУЮТ инжектируемый Runner (`runnerFor` всегда строит реальный `executor.New` по `stage.Command`). Это ЕДИНСТВЕННЫЙ путь, покрывающий файловый dialog-протокол (question/answer файлы, поллер, relocate, mis-prefix, зависание, парсинг stream-json). Прецеденты: `TestFullDialogCycle`, `TestIntegration_MisplacedQuestionRelocated`, `TestIntegration_MisprefixedQuestionNormalized`.

Харнесс объединяет оба сейма за одним декларативным `AgentSpec`.

## Архитектура (`package orchestrator_test`)

Новый файл(ы) в `pkg/orchestrator` (external test package — уже там живут интеграционные тесты, есть доступ к `orchestrator.New`, `StoreFromOrch`, `NotifyAnswer`).

### Типы сценария

```go
type Scenario struct {
	Name       string
	Stages     []flow.Stage         // флоу (строится в тесте)
	Supervisor []byte               // scripted decision JSON для RunJSONQuery (опц.; nil = супервизор не задействован)
	Agents     map[string]AgentSpec // поведение синтетического агента по stageID
	Expect     Expectation
}

type Injection int
const (
	InjectNone Injection = iota
	InjectRateLimitThenOK   // первый вызов → retryable rate-limit, второй → успех
	InjectNoDone            // агент «отработал», но не создал .done → incomplete
	InjectMissingArtifact   // не создаёт заявленный artifact
)

type QuestionFault int
const (
	FaultNone      QuestionFault = iota
	FaultWrongFolder             // question.json записан ВНЕ $AFM_STAGE_DIR (баг relocate)
	FaultWrongPrefix             // <stageID>.qN вместо <phase>.qN (баг normalize)
)

type QuestionInject struct {
	Phase  string // planning/implementation/review/autonomous_execution
	ID     string // q1, q2, …
	Answer string // ответ, который харнесс доставит; "" → НИКОГДА не отвечать (зависание)
	Fault  QuestionFault
}

type AgentSpec struct {
	Interactive bool             // true → синтетический bash-агент (stage.Command); false → scriptedRunner
	Question    *QuestionInject  // interactive: задать вопрос перед завершением; nil = без вопроса
	Inject      Injection        // инъекция ошибки (для неинтерактивного/раннер-сейма)
}

type Expectation struct {
	Statuses             map[string]state.StageStatus // финальный статус по stageID (проверяется через StoreFromOrch)
	FilesPresent         []string // пути относительно stageDir, которые ДОЛЖНЫ существовать (напр. "autonomous.flag", "execution_summary.md")
	FilesAbsent          []string // пути, которых НЕ должно быть (напр. "plan.md")
	ReachesAwaitingInput []string // stageID, которые обязаны достичь awaiting_user_input (диалог/зависание)
	RunErrSubstr         string   // подстрока в ошибке orch.Run (storage-fatal сценарии); "" = ошибки нет
}
```

### Компоненты

- **`scriptedRunner`** (`executor.Runner`) — конфигурируемый мок для неинтерактивных стадий: по `(stageID, phase)` возвращает `plan.md` (валидный для `checkPlanCompletion`), пишет `.done`/`execution_summary.md`, отдаёт `Supervisor` decision из `RunJSONQuery`, либо реализует `Injection` (rate-limit-then-ok, no-done, missing-artifact). Выводит `stageDir` из `logFile` (как существующий `supervisorTestRunner`). Заменяет разрозненные ad-hoc раннеры единым.
- **`writeSynthAgent(t, id string, spec AgentSpec) string`** — генерирует bash-скрипт (в temp), возвращает путь для `stage.Command`. Скрипт: (1) если есть `Question` — пишет `question.json` в `$AFM_STAGE_DIR` (или ВНЕ его при `FaultWrongFolder`, или с префиксом stageID при `FaultWrongPrefix`) + эмитит stream-json `Write`-событие с этим путём (поллер читает `WrittenFiles`); (2) поллит answer-файл (или спит до таймаута при `Answer==""`); (3) пишет `plan.md`/`execution_summary.md`/`.done` по фазе. Модель — существующие интерактивные тесты.
- **`runScenario(t *testing.T, sc Scenario)`** — драйвер: `t.TempDir()` → `state.Open` → `orchestrator.New` (с `scriptedRunner`; интерактивным стадиям выставляется `Command = writeSynthAgent(...)`) → `orch.Run` в горутине с таймаутом (ctx). Для сценариев с `Answer!=""`: ждёт `awaiting_user_input` по нужным стадиям, затем `orch.NotifyAnswer(...)`. По завершении/таймауту ассертит `Expectation` (статусы через `StoreFromOrch`, наличие/отсутствие файлов в stageDir, достижение `awaiting_user_input`, подстрока ошибки).

## Seed-сценарии (первый деливерабл)

Таблица `[]Scenario`, каждый гоняется через `runScenario`:

1. **happy-multistage** — `propose`→`plan`→`implement` (depends_on), все агенты завершаются → все стадии `done`.
2. **supervisor-autonomous** — `supervisor:true`, decision `can_execute_autonomously:true` → autonomous-трек; `autonomous.flag` есть, `execution_summary.md` есть, `plan.md` нет.
3. **auto-phase** — `agents:[auto]` → autonomous без супервизора; `autonomous.flag` есть, `plan.md` нет.
4. **interactive-dialog-happy** — интерактивная стадия задаёт корректный вопрос, харнесс отвечает → стадия проходит `awaiting_user_input` → `done`.
5. **misprefixed-question** — агент пишет `<stageID>.q1.question.json` (`FaultWrongPrefix`) → нормализуется → `awaiting_user_input` → ответ доставлен → `done`.
6. **wrong-folder-question** — агент пишет вопрос вне `$AFM_STAGE_DIR` (`FaultWrongFolder`) → relocate → `awaiting_user_input`.
7. **hung-dialog** — вопрос задан, `Answer==""` → стадия остаётся в `awaiting_user_input` (не падает, не done) к моменту таймаута.
8. **retry-after-transient** — `InjectRateLimitThenOK`: первый вызов агента → retryable rate-limit, автоматический ретрай (`runWithRetry`) → второй вызов успех → `done`. (Тестирует авто-ретрай без mid-run экшена; ручной `Retry` failed→ready — позже, когда в харнесс добавим hook действий.)
9. **corrupt-log-recovery** — предзаписанный `events.jsonl` с битой строкой в середине → `state.Open`/`LoadRunState` возвращает `ErrCorruptLog` (state-layer сценарий; не требует агента).

Каждый seed-сценарий — регрессионный якорь на реальный функционал/баг этой сессии.

## Границы и переиспользование

- Харнесс — только тестовый код (`_test.go`), продакшн не трогает.
- Существующие ad-hoc интеграционные тесты НЕ удаляются массово; мигрируются на харнесс инкрементально (вне Phase 1). Дубли допустимы временно.
- `.afm/runs/*` — источник реалистичных фикстур (реальные `events.jsonl`, stream-json, question/answer) для сценариев recovery и dialog; в Phase 1 используются как образец форматов, не как загружаемые данные.

## Тестирование харнесса

Харнесс сам себе тест: 9 seed-сценариев — это и есть тесты, которые обязаны проходить. Дополнительно — короткий негативный само-тест: сценарий с заведомо неверным `Expect` должен падать (проверка, что ассерты реально срабатывают, а не no-op).

## Вне охвата (Phase 2 и далее)

Прогон `Scenario` через собранный бинарь (`afm run` + synthAgent как команда, ассерты по `.afm/runs`). Полная миграция всех существующих ad-hoc тестов. Загрузка сценариев из внешних файлов. Docker-режим.
