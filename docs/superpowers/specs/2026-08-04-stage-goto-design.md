# goto: условный откат назад по графу стейджей

**Дата:** 2026-08-04
**Статус:** design (одобрен к реализации)

## Цель

Сейчас `flow.Flow` — чистый DAG (`detectCycles` явно запрещает циклы), а FSM (`pkg/orchestrator/fsm.go`) необратим: `Done`/`Failed` терминальны, выйти из них можно только через `EvManualRetry` (ручной retry ОДНОЙ упавшей стадии, без отката других). Нет способа сказать «эта стадия поняла, что более раннюю стадию нужно переделать» — ни агенту, ни пользователю.

Нужны два сценария:
1. Линейная цепочка `planning → implementation → tests → integration`: `integration` на последнем шаге обнаруживает, что реализация не соответствует ожиданиям, и просит откатиться к `implementation` — вместе с ним автоматически передедываются и все стадии между `implementation` и `integration` (`tests`).
2. Цикл ревью: `review → check`. `check` проверяет результат `review` и, если не ок, возвращает выполнение в `review` — с ограничением числа повторов, чтобы не крутиться вечно.

Общий термин: **`Goto`** — управляемый откат стадии `source` к своему предку `target` по графу зависимостей, с каскадным сбросом всего, что лежит между ними.

## Область охвата (что решаем сейчас, что откладываем)

- Источники триггера в первой итерации: **(a)** сам агент через файловый протокол решения, **(b)** ручная команда `afm goto`. LLM-супервизор как третий источник — не в этой итерации (см. «Вне охвата»), но core-механизм (`Orchestrator.Goto`) рассчитан на то, что супервизор сможет вызвать его точно так же.
- Цель отката обязана быть **транзитивным предком** стадии по `depends_on` — прыжки в произвольную стадию флоу не поддерживаются.
- Разрешённые цели откатов объявляются **статически в flow.yaml** (`goto_targets`) — не любой предок молча разрешён, флоу должен явно описать свои циклы.

## 1. flow.yaml: `goto_targets`

```yaml
stages:
  - id: check
    depends_on: [review]
    goto_targets:
      - stage: review
        max_attempts: 3   # опционально, default 3
```

`pkg/flow/flow.go`:

```go
// GotoTarget описывает разрешённую цель обратного перехода для стадии.
type GotoTarget struct {
    Stage       string `yaml:"stage"`
    MaxAttempts int    `yaml:"max_attempts"`
}

type Stage struct {
    ...
    // GotoTargets перечисляет предков, к которым стадия может откатиться
    // через file-based decision-протокол или afm goto. Цель ДОЛЖНА быть
    // транзитивным depends_on-предком (проверяется в validate()).
    GotoTargets []GotoTarget `yaml:"goto_targets"`
}
```

- `defaultGotoMaxAttempts = 3` — применяется в новой `applyGotoTargetDefaults` (по образцу `applyScriptTimeoutDefaults`, вызывается из `ParseFile` рядом с ней) для любого `GotoTarget` с `MaxAttempts == 0`.
- `Stage.GotoTarget(name string) (GotoTarget, bool)` — хелпер поиска записи по имени цели (используется и в `validate()`, и в orchestrator при проверке allowlist).

### Валидация (`validate()`)

Дополняем существующий проход по стадиям:
- каждый `gt.Stage` в `s.GotoTargets` должен существовать в `ids` (как и `depends_on`);
- каждый `gt.Stage` должен быть **транзитивным предком** `s.ID` — считаем через новую функцию `ancestors(stages []Stage) map[string]map[string]bool` (id → множество предков, транзитивное замыкание `depends_on`, тот же обход, что и `detectCycles`, только результат не «есть цикл», а полное множество достижимых по `depends_on` вершин);
- дубликаты `gt.Stage` в пределах одной стадии — ошибка (как дубликаты `Artifact.Name`).

Ошибка формата: `stage %q: goto_targets references %q which is not an ancestor (depends_on) of this stage`.

## 2. FSM: обратный переход

`pkg/orchestrator/fsm.go` — новое событие, отдельное от `EvManualRetry`, чтобы в `events.jsonl` откат по цепочке был отличим от «пользователь тыкнул retry на упавшей стадии»:

```go
// EvGotoReset — часть каскадного Goto: переводит стадию (target ИЛИ любую
// стадию из cascade-set, включая source) обратно в Pending, откуда она
// подхватывается штатным depends_on-планировщиком с нуля.
EvGotoReset FSMEvent = "goto_reset"
```

```go
EvGotoReset: {From: []state.StageStatus{
    state.StatusRunning, state.StatusRetrying, state.StatusDone, state.StatusFailed,
}, To: to(state.StatusPending)},
```

(`Running`/`Retrying` — это статус `source` в момент, когда он просит откат, до того как сам получит `EvComplete`/`EvFail`; `Done`/`Failed` — статус стадий из cascade-set, накопленный за предыдущие прогоны.)

## 3. Граф: вычисление cascade-set

`pkg/orchestrator/graph.go` — две новые функции поверх уже существующего `Graph.deps`:

```go
// Ancestors возвращает id (включая сам id) — транзитивное замыкание depends_on.
func (g *Graph) Ancestors(id string) map[string]bool

// Descendants возвращает id (включая сам id) — транзитивное замыкание "кто зависит от".
func (g *Graph) Descendants(id string) map[string]bool

// Between возвращает id стадий, лежащих на каком-либо depends_on-пути от
// fromID к toID включительно: Descendants(fromID) ∩ Ancestors(toID).
// Ветки, зависящие от fromID, но не ведущие к toID, не включаются.
func (g *Graph) Between(fromID, toID string) []string
```

`Between(target, source)` и есть cascade-set. Поскольку `target` уже проверен как предок `source` (валидация flow.yaml + defensive re-check в runtime), `source ∈ Descendants(target)` и `target ∈ Ancestors(source)` гарантированно — пустого результата не бывает.

## 4. Loop guard — счётчик из event-лога

Отдельного in-memory счётчика не заводим (не переживёт resume/crash, конфликтует с философией «events.jsonl — источник правды», см. `state.LoadRunState`). Вместо этого при каждом вызове `Goto(source, target, ...)`:

```go
func gotoAttempts(history []state.Transition, source, target string) int {
    n := 0
    prefix := "goto:" + source + "->"
    for _, t := range history {
        if t.Event == string(EvGotoReset) && t.StageID == target && strings.HasPrefix(t.Reason, prefix) {
            n++
        }
    }
    return n
}
```

`Reason`, записываемый в `Transition` для перехода `target`-стадии, всегда начинается с `"goto:<source>-><target>: "` + человекочитаемая причина — так один `strings.HasPrefix` и различает пары, и остаётся читаемым в UI/логах.

Если `gotoAttempts(...) >= maxAttempts` — не откатываемся; `source` уходит в `Failed` через обычный `EvFail` с причиной `"goto loop limit exceeded: <source>-><target> (N/N)"`.

## 5. Core: `Orchestrator.Goto`

Новый файл `pkg/orchestrator/goto.go`:

```go
// Goto откатывает source к предку target: сбрасывает весь cascade-set
// (target..source включительно) в Pending, снимает debug-снапшот их
// директорий и оставляет target причину отката для следующего прогона.
// source == "manual" источник допускается только через явный вызов с
// bypassAllowlist=true (CLI-команда afm goto — осознанный люк оператора).
func (o *Orchestrator) Goto(ctx context.Context, source, target, reason string, bypassAllowlist bool) error
```

Шаги:
1. **Resolve & allowlist.** `stage := o.graph.Stage(source)`; если `!bypassAllowlist`, ищем `target` в `stage.GotoTargets` (`Stage.GotoTarget`) — нет записи → `fmt.Errorf("stage %q has no goto_targets entry for %q", source, target)`. `bypassAllowlist` (ручной CLI) всё равно проверяет, что `target` — предок (`o.graph.Ancestors(source)[target]`), просто не требует записи в YAML.
2. **Loop guard** (см. §4) — `maxAttempts` берём из `GotoTarget.MaxAttempts`, если запись есть; для `bypassAllowlist` без записи — дефолт 3 (тот же `flow.DefaultGotoMaxAttempts`, экспортированная константа).
3. **Cascade-set** = `o.graph.Between(target, source)` (§3).
4. **Debug-снапшот** каждой стадии cascade-set (§6) — до сброса, пока директория ещё содержит прошлый прогон.
5. **Причина → target**: `state.SaveGotoReason(targetDir, source, reason)` (§7).
6. **Сброс** каждой стадии cascade-set через `o.Trigger(id, EvGotoReset, GuardCtx{}, gotoReasonPrefix(source, target)+reason)` — порядок не важен (см. `autoRecoverFailedStages`: сброс — это просто смена статуса, дальше штатный `depsDone()`-гейт в `startReadyStages`/`startPlanningForPending` сам восстановит правильную последовательность).
7. **Событие для аудита/дашборда**: `o.ui.Publish(Event{Type: EventGotoTriggered, StageID: source, Data: fmt.Sprintf("%s -> %s: %s", source, target, reason)})` — новая константа в `bus.go`, рядом с `EventRetryScheduled`.
8. Если стадия из cascade-set интерактивна — `clearInteractiveSessions` (уже существующий helper из `scheduling.go`), как в `retryStage`/`autoRecoverFailedStages`, иначе протухший `<phase>.session.json` ломает следующий прогон.

Два публичных входа в `control_api.go` (разница — только `bypassAllowlist`):

```go
// GotoStage — вход для file-based decision-протокола (агент сам решил
// откатиться). target ОБЯЗАН быть в статическом Stage.GotoTargets.
func (o *Orchestrator) GotoStage(ctx context.Context, source, target, reason string) error {
    return o.Goto(o.runContext(ctx), source, target, reason, false)
}

// GotoStageManual — вход для afm goto (оператор руками). target достаточно
// быть предком по depends_on, запись в GotoTargets не обязательна —
// осознанный аварийный люк, минуя декларацию флоу.
func (o *Orchestrator) GotoStageManual(ctx context.Context, source, target, reason string) error {
    return o.Goto(o.runContext(ctx), source, target, reason, true)
}
```

(`runContext` — тот же паттерн, что у `Retry`/`Approve`/`Revise`: под run-scoped ctx, не под `ctx` HTTP-хэндлера, который истечёт сразу после ответа.)

## 6. Debug-снапшот директории стадии

`pkg/state/state.go` — по образцу `VersionPlan`/`LatestPlanVersion`, только для целой директории, не только `plan.md`:

```go
// SnapshotStageDir копирует <runDir>/<stageID>/ в <runDir>/<stageID>.attempt-N/
// для дебага (N — следующий свободный номер, см. LatestAttemptSnapshot).
// Оригинальная директория НЕ трогается — следующий прогон агента перезапишет
// в ней то, что нужно (как сейчас работает Revise).
func SnapshotStageDir(runDir, stageID string) (int, error)

// LatestAttemptSnapshot сканирует runDir на "<stageID>.attempt-N" и возвращает
// максимальный N (0, если снапшотов нет).
func LatestAttemptSnapshot(runDir, stageID string) (int, error)
```

Копирование — рекурсивное (`filepath.WalkDir` + `os.MkdirAll`/`io.Copy`), в проекте такого хелпера ещё нет — добавляется как приватная `copyDirRecursive`.

## 7. Причина отката → следующий прогон target

`pkg/state/state.go`:

```go
// SaveGotoReason перезаписывает <stageDir>/goto_reason.md — причину, по
// которой стадию откатили. В отличие от feedback.md (SaveFeedback,
// append-версионируется по ревизиям Revise) здесь нужна только САМАЯ
// СВЕЖАЯ причина к следующему прогону — перезапись, без истории.
func SaveGotoReason(stageDir, source, reason string) error
```

`pkg/prompts/builder.go`:
- новое поле `Inputs.GotoReason string` (рядом с `Feedback`);
- рендер — новый блок, симметричный `<feedback>`:
  ```go
  if in.GotoReason != "" {
      sb.WriteString("\n<goto_reason>\n")
      sb.WriteString(escapeTags(in.GotoReason))
      sb.WriteString("\n</goto_reason>\n")
  }
  ```
- `<goto_reason>`/`</goto_reason>` добавляются в `tagReplacer` (zero-width-space escaping, как остальные теги).

**Потребление — один раз, при первом входе в стадию после сброса.** Новый хелпер в `pkg/orchestrator`:

```go
// consumeGotoReason читает и удаляет goto_reason.md, если он есть — причина
// должна попасть в промпт только для того самого прогона, что случился
// сразу после сброса, а не для каждого последующего запуска этой стадии.
func consumeGotoReason(stageDir string) string
```

Вызывается в начале трёх «первый вход в стадию» функций (`agents.go`), передаётся как `prompts.Inputs.GotoReason`:
- `runPlanningAgent` (стадии с планированием — самый частый случай, покрывает оба сценария из «Цель»);
- `runImplementationAgent`, если `!s.NeedsPlanning()` (готовый `Plan`, планирования нет);
- `runReviewAgent`, если review — единственный агент стадии и планирования нет (тот же случай, но для стадии типа `check` из сценария 2, если она устроена как самостоятельный review без своего planning);
- `runAutonomousAgent` (auto-стадии, `agents: [auto]`).

`*WithFeedback`-варианты (`runPlanningWithFeedback` и т.д.) не читают `goto_reason.md` — они обслуживают Revise-цикл (Running/AwaitingApproval → Revising), а после `Goto` стадия всегда идёт через Pending → полный свежий старт, то есть через «первый вход», не через `WithFeedback`.

## 8. Источник (a): агент сам решает — decision-протокол

Расширение файлового протокола диалога (`AFM_STAGE_DIR`) новым файлом:

```
$AFM_STAGE_DIR/<phase>.decision.json
{"verdict": "goto", "target": "review", "reason": "тесты не бьются с ревью-планом: ..."}
```

`pkg/prompts/builder.go` — новый блок `<goto_rules>`, добавляется в `Build()` когда `len(in.Stage.GotoTargets) > 0` (по образцу секции `<interactive_rules>`, гейтящейся на `in.Interactive`): объясняет агенту доступные `target` (из `in.Stage.GotoTargets`), формат файла и что писать его нужно ВМЕСТО обычного сигнала завершения (`.done`/`execution_summary.md`).

**Точка перехвата** — `pkg/orchestrator/retry.go`, `runWithRetry`, ветка `err == nil` (агент завершился успешно), ПЕРЕД вызовом `completionCheck()`:

```go
if decision, ok := readDecision(stageDir, phase); ok && decision.Verdict == "goto" {
    if err := o.GotoStage(ctx, s.ID, decision.Target, decision.Reason); err != nil {
        o.Trigger(s.ID, EvFail, GuardCtx{}, "goto: "+err.Error())
        o.failBlockedStages()
    }
    return
}
```

(симметрично уже существующей проверке `hasOpenQuestion` в той же ветке — обе проверяют файл в `stageDir` до того, как код решит, что стадия обычным образом Done). `readDecision` валидирует `Verdict ∈ {"goto"}` (зарезервировано под будущие verdict'ы) и что `Target` не пустой; если файл повреждён/не парсится — считается, что решения нет, идёт обычный `completionCheck`.

## 9. Источник (b): ручной CLI

`cmd/afm/goto.go`, по образцу `cmd/afm/retry.go`:

```
afm goto <stage-id> <target-id> [--reason "..."]
```

В отличие от `afm retry` (у которого есть офлайн-путь — прямой `store.Apply` в остановленный run, см. `cmd/afm/retry.go`), `afm goto` **требует живого `afm run`**: сброс cascade-set в Pending бесполезен, если некому его подхватить — `startReadyStages` реагирует на новый `Pending` только внутри работающего event-loop'а. Поэтому `afm goto` реализуется как клиент к новому HTTP-эндпоинту `POST /api/stages/{source}/goto` (по образцу существующих `/api/stages/{id}/approve` в `pkg/server/handlers.go`), который вызывает `Orchestrator.GotoStageManual`. Если `afm run` не запущен — понятная ошибка «run is not active — start `afm run` first» (не «файл заблокирован», как у `retry`).

## 10. Наблюдаемость

- `EventGotoTriggered EventType = "goto_triggered"` в `bus.go` — публикуется один раз на успешный `Goto` (см. §5, шаг 7); дашборд рисует его в event feed как и остальные `Event*` типы.
- `events.jsonl` — каждая стадия cascade-set получает свою запись `EvGotoReset` с `Reason`, начинающимся `"goto:<source>-><target>: "` — по логу однозначно видно, кто, когда и почему инициировал откат, без отдельного хранилища аудита.

## Тестирование

- **`pkg/flow`**: валидация `goto_targets` — цель-не-предок → ошибка; цель-предок (прямая и транзитивная) → ок; дубликат цели → ошибка; `max_attempts: 0` → дефолтится в 3.
- **`pkg/orchestrator/graph_test.go`**: `Ancestors`/`Descendants`/`Between` на графе с разветвлением (проверить, что сторонняя ветка, зависящая от `target`, но не ведущая к `source`, НЕ попадает в `Between`).
- **`pkg/orchestrator` (интеграционные, `goto_test.go`)**:
  - Сценарий 1 (линейная цепочка `implementation → tests → integration`): decision.json от `integration` с `target: implementation` → `implementation` и `tests` сбрасываются в `Pending` и перезапускаются с нуля, `goto_reason.md` появляется в `implementation`, попадает в промпт следующего прогона планирования, затем стирается.
  - Сценарий 2 (`review → check` цикл): `check` трижды откатывает в `review` (в пределах `max_attempts: 3`) → на четвёртый — `check` падает `Failed` с "goto loop limit exceeded", `review` остаётся в последнем полученном статусе.
  - Ручной `afm goto` (через `GotoStageManual`) на предка вне статического `goto_targets` — работает (bypass), на не-предка — ошибка. Через `GotoStage` (decision-протокол) — цель вне `goto_targets` даёт ошибку без bypass.
  - Debug-снапшоты: после двух откатов на диске есть `<stage>.attempt-1/` и `<stage>.attempt-2/`, оригинальная директория цели не пуста и содержит файлы предыдущего прогона (не удалена).
  - Интерактивная стадия в cascade-set — протухший `<phase>.session.json` подчищается (симметрично `autoRecoverFailedStages`).

## Вне охвата

- LLM-супервизор как третий источник триггера (`DetermineStagePhases` решает откатиться сама) — API `Orchestrator.Goto` уже рассчитан на этот вызов, но провода к supervisor-логике в этой итерации нет.
- Garbage collection старых `<stage>.attempt-N/` снапшотов — копятся на диске без лимита; если это станет проблемой, отдельная задача.
- Откат в стадию, которая НЕ предок (произвольный прыжок по графу) — принципиально не поддерживается, слишком легко создать логически противоречивый флоу.
- Инвалидация «соседних» веток, которые тоже зависят от `target`, но не лежат на пути к `source` — по этому дизайну они НЕ сбрасываются, даже если формально их вход (артефакт `target`) уже устарел. Если это окажется реальной проблемой — расширение `Between` до полного `Descendants(target)` тривиально, но осознанно не делается сейчас (см. вопрос про cascade в брейнсторминге).
