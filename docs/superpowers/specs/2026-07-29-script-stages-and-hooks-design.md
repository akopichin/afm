# Script-стейджи и script_before/script_after хуки

**Дата:** 2026-07-29
**Статус:** design (одобрен к реализации)

## Цель

Дать два новых способа выполнить произвольный shell-скрипт как часть flow, без участия LLM-агента:

1. **`script`-стейдж** — полноценный тип стейджа (свой ID, зависимости, участвует в DAG наравне с агентскими стейджами), у которого вместо агента выполняется shell-скрипт. Никакого planning/supervisor/approve — как только зависимости выполнены, скрипт запускается сразу.
2. **`script_before`/`script_after`** — необязательные хуки на ЛЮБОМ стейдже (агентском, script, interactive, `auto`), которые выполняются непосредственно до/после основного содержимого стейджа.

Вывод обоих видов скриптов логируется в человекочитаемый лог стейджа (виден в `LogPanel`) и в event-фид (виден в `EventFeedPanel`), сам факт запуска/успеха/провала — тоже событие.

## Синтаксис YAML

```yaml
stages:
  - id: notify
    script: |
      echo "deploy started"
      curl -s https://hooks.example/notify
    script_timeout: 120s          # опционально, дефолт 300s

  - id: build
    agents: [implementation]
    script_before: |
      echo "setting up workspace"
    script_before_timeout: 60s    # опционально, дефолт 300s
    script_after: |
      echo "cleaning up"
    script_after_timeout: 60s     # опционально, дефолт 300s
```

- `script`-стейдж — **новый, взаимоисключающий тип стейджа**. У стейджа со `script` не может быть НИЧЕГО, кроме `script`/`script_timeout` (плюс общие поля стейджа — `id`, `name`, `depends_on`, `artifacts`, `inputs`, `max_parallel`): `agents`, `command`, `interactive`, `plan`, `verify`, `supervisor` — запрещены. `script` полностью заменяет и агента, и `verify` (ненулевой exit-код скрипта — это и есть провал стейджа).
- `script_before`/`script_after` — ортогональные хук-поля, разрешены на ЛЮБОМ типе стейджа (агентском с `agents`/`command`, `interactive: true`, `agents: [auto]`) вместе со всеми остальными полями этого стейджа — они не заменяют, а оборачивают основное содержимое.
- Таймауты — отдельные поля на каждый скрипт (`script_timeout`, `script_before_timeout`, `script_after_timeout`), Go duration string (`"120s"`, `"5m"`). Не заданы → дефолт 300s.
- Все скрипты выполняются как `sh -c "<script>"` — тот же механизм, что уже используется для `verify:` (`pkg/orchestrator/completion.go:98`).

## Валидация (`pkg/flow`, `ParseFile`/`validate()`)

- `script` несовместим с `agents`, `command`, `interactive`, `plan`, `verify`, `supervisor` — явная ошибка `stage %q: "script" cannot be combined with agents/command/interactive/plan/verify/supervisor`.
- Стейдж должен иметь ровно один способ реально что-то сделать: `agents` (включая `auto`), `command`, `interactive`, ИЛИ `script`. Существующая проверка «у каждого стейджа должен быть planning-агент/plan/interactive/auto» (`flow.go:195-197`) расширяется веткой `IsScript()`.
- `script_timeout`/`script_before_timeout`/`script_after_timeout` — невалидная duration-строка → ошибка парсинга с указанием стейджа и поля.
- Пустая строка в `script_before`/`script_after` трактуется как отсутствие хука (no-op), не ошибка.

## Модель (`pkg/flow/flow.go`)

Новые поля `Stage`: `Script string`, `ScriptTimeout time.Duration`, `ScriptBefore string`, `ScriptBeforeTimeout time.Duration`, `ScriptAfter string`, `ScriptAfterTimeout time.Duration`.

`Stage.IsScript() bool` → `s.Script != ""` — аналогично `IsAuto()`. Как и `auto`, `IsScript()` коротко замыкает `NeedsPlanning()` (→ false), `HasAgent()` (→ false для всех built-in фаз), `ImplAgent()` (не применяется).

## Исполнение

### Новый примитив в `pkg/executor`

`(*Executor).RunScript(ctx, cfg, script string, timeout time.Duration) error` — рядом с `RunAgent`/`RunPlanning`. Переиспользует существующий `run()` (`executor.go:466-583`): `exec.CommandContext("sh", "-c", script)`, `cmd.Dir = cfg.Dir` (= `root_dir` стейджа), `AFM_STAGE_DIR` инжектится как обычно. В отличие от агентских ран, здесь не idle-timeout (сбрасываемый по строке вывода), а жёсткий общий `timeout` на весь скрипт — таймер не сбрасывается новыми строками вывода. stdout+stderr построчно проходят через тот же `progress.Logger.LogAction("text", line)`, которым уже пользуется `RunAgent` — стейдж получает лог бесплатно.

### Новый stage-runner в `pkg/orchestrator`

`runScriptStage(ctx, s)` — рядом с `runAutonomousAgent`. Активируется тем же путём, что и `auto`-стейдж сегодня (`tryActivatePrePlanned` / `startPlanningForPending` в `recovery.go`, страховка в `startReadyStages`): т.к. `NeedsPlanning()==false`, стейдж уходит pending→ready без planning/supervisor и сразу запускается. `runScriptStage` вызывает `executor.RunScript` с `s.Script`/`s.ScriptTimeout`, ретраит по хук-политике (см. ниже), пишет лог в `<stage>.script.log`.

### Централизованный hook-wrapper

Решение о том, ЧТО и КОГДА запускать, в оркестраторе принимается минимум в 6-7 независимых местах (`startReadyStages`, `retryStage`, `recovery.go`, `Revise`, `onAgentCompleted`, `onUserAnswered`) — единой точки диспатча-решения нет. Но почти все они сходятся к единому низкоуровневому механизму запуска — `spawnAgent` (`pkg/orchestrator/concurrency.go:46`), который принимает саму run-функцию (`runPlanningAgent`/`runImplementationAgent`/`runAutonomousAgent`/новую `runScriptStage`) как параметр и стартует горутину. Поэтому hook-wrapper встраивается ОДИН РАЗ — внутрь `spawnAgent`, оборачивая переданную `fn` до/после её вызова:

`runStageWithHooks(ctx, s, mainFn func() error) error` — вызывается из тела `spawnAgent` вместо прямого вызова `fn`:

1. Если `s.ScriptBefore != ""` — выполнить его (retry ×3, backoff 1s→2s→3s, лог в `<stage>.before.log`). При исчерпании ретраев → стейдж уходит в статус `hook_failed` (см. ниже), `mainFn` НЕ вызывается, пока пользователь не решит retry/skip.
2. Вызвать `mainFn()` — существующий диспатч (агент/interactive/auto/script), без изменений.
3. Если `mainFn()` завершился успехом И `s.ScriptAfter != ""` — выполнить его (та же retry-политика, лог в `<stage>.after.log`). При исчерпании ретраев — публикуется `hook_failed{hook:"after"}`, но статус самого стейджа остаётся `done` (см. «Обработка ошибок»).

Хук-ретраи — **отдельная, фиксированная политика** (3 попытки, backoff 1s/2s/3s), НЕ переиспользует `runWithRetry`/`o.maxRetries`/`o.retryBackoff` (`pkg/orchestrator/retry.go:56-63`, 15 попыток по 5s — рассчитана на LLM rate-limit окна, для детерминированного shell-скрипта избыточна и не подходит по смыслу). Провал самого `script`-стейджа (не хука, а основного скрипта) — та же 3×/1-2-3s политика, что и у хуков (это тоже детерминированная быстрая операция), после исчерпания — обычный `failed`, доступен `afm retry` как сегодня.

**Два прямых вызова, минующих `spawnAgent`.** `resumeInteractiveAgent` (`recovery.go:171,179`) вызывает `runPlanningAgent`/`runImplementationAgent` напрямую и синхронно (уже внутри своей горутины) — эти два места должны явно обернуть вызов в `runStageWithHooks`, т.к. они не проходят через `spawnAgent` и иначе останутся без хуков.

### Working directory и окружение

`cmd.Dir` = `root_dir` стейджа (та же директория, где выполняется агент этого стейджа), `AFM_STAGE_DIR` = `.afm/runs/<run_id>/<stage_id>/` (та же stage-директория). Никакой специальной изоляции — script/script_before/script_after стейджа X видят то же окружение, что и агент стейджа X.

## Обработка ошибок: `hook_failed`

Новый нетерминальный статус в `pkg/state/state.go` (рядом с `pending, planning, awaiting_approval, revising, ready, running, retrying, awaiting_user_input, done, failed`). Не переиспользует `awaiting_user_input` — тот жёстко завязан на file-based dialog protocol (`dialog_poller.go` ждёт `*.question.json`), конфликтующая семантика.

FSM (`pkg/orchestrator/fsm.go`): новое событие `EvHookFailed` (переход в `hook_failed` из `running`/`ready`), `EvHookRetried`/`EvHookSkipped` (переход из `hook_failed` обратно в рабочий поток). `IsTerminal()` — `hook_failed` не терминален.

Данные при входе в `hook_failed`: какой хук (`before`/`after`), какого стейджа, текст последней ошибки/exit-код.

**`script_before` исчерпал ретраи** → блокирует прогресс: стейдж висит в `hook_failed`, пока пользователь не нажмёт Retry (перезапуск всего цикла 3×/1-2-3s) или Skip (продолжить без before, сразу к `mainFn`).

**`script_after` исчерпал ретраи** → стейдж уже успешно завершён (`done`), это НЕ блокирует и не переводит стейдж в `failed`. Показывается такая же плашка Retry/Skip, но статус стейджа остаётся `done` в любом случае — Retry просто пере-пытается выполнить `script_after`, Skip просто снимает плашку.

### Новые HTTP-эндпоинты (`pkg/server/handlers.go`)

По образцу существующего `/api/stages/{id}/retry` (`handleRetry`, `handlers.go:195`):

- `POST /api/stages/{id}/retry-hook` — требует `status == hook_failed`, иначе 400. Перезапускает провалившийся хук (снова 3×/1-2-3s).
- `POST /api/stages/{id}/skip-hook` — требует `status == hook_failed`, иначе 400. Пропускает хук и продолжает (before → сразу `mainFn`; after → просто снимает плашку, стейдж уже `done`).

### Recovery (`pkg/orchestrator/recovery.go`)

Краш во время `hook_failed` → при `afm run` restart стейдж резюмится обратно в `hook_failed` (явный `case` в switch по статусам), а не тихо ретраится и не теряет ожидающее решение пользователя.

## Логи и события

**Человекочитаемый лог** (виден в `LogPanel` через `GET /api/stages/{id}/log`): каждый скрипт пишет в свой файл — `<stage>.script.log`, `<stage>.before.log`, `<stage>.after.log` — тем же `progress.Logger.LogAction("text", line)`, которым уже пользуется `RunAgent`. Хендлер лога стейджа конкатенирует файлы в порядке `before → script/agent → after`, как сегодня конкатенирует фазы.

**Event-фид** (`pkg/orchestrator/bus.go`, `EventFeedPanel.tsx`) — три новых типа события:

- `EventScriptOutput` — по одному на строку вывода, `Data: {hook: "main"|"before"|"after", line}`. Новый `case` в `toFeedLine` рендерит `[before|script|after] <line>`, аналогично тому как сегодня рендерится `agent_action`.
- `EventHookFailed` — хук исчерпал ретраи, `Data: {hook, error}`. Ведёт к переходу в `hook_failed`.
- `EventHookResolved` — пользователь нажал Retry (успех) или Skip, `Data: {hook, resolution: "retried"|"skipped"}`.

Существующий `EventStageStatusChanged` уже покрывает грубый жизненный цикл стейджа (`running`→`done`/`failed`) — отдельного события «script-стейдж стартовал/завершился» не нужно, это уже общее для всех типов стейджей.

Все новые события идут в тот же `events.jsonl` (единственный источник правды), реплеятся при resume без изменений в механизме — просто новые типы поверх существующего лога.

## UI (dashboard)

- Стейдж-панель: `script`-стейдж помечается меткой "script" вместо обычного имени фазы.
- `hook_failed`: две кнопки — Retry и Skip (тот же визуальный паттерн, что у существующих approve/revise), с подписью какой хук (`before`/`after`) провалился и текстом последней ошибки.
- Event-фид: новый `case` для `EventScriptOutput`.
- `LogPanel`: без нового компонента — хендлер лога стейджа конкатенирует новые файлы логов хуков в существующем месте.

## Тестирование

- **`pkg/flow`**: парсинг/валидация — `script` конфликтует с `agents`/`command`/`interactive`/`plan`/`verify`/`supervisor`; `script_before`/`script_after` принимаются вместе с любым типом стейджа; невалидные duration-строки отклоняются с понятной ошибкой.
- **`pkg/executor`**: `RunScript` — exit-код прокидывается наружу, stdout/stderr построчно попадают в лог, жёсткий timeout убивает процесс (SIGTERM → SIGKILL grace period, как у существующего interrupt-handling).
- **`pkg/orchestrator`** (интеграционные):
  - `script`-стейдж, happy path: ready → running → done, лог и события на месте.
  - `script_before` проваливается 3 раза (backoff 1-2-3s наблюдаем) → `hook_failed` → `retry-hook` успешен → основной стейдж выполняется.
  - `script_before` → `hook_failed` → `skip-hook` → основной стейдж выполняется без before.
  - `script_after` проваливается 3 раза → `hook_failed{after}`, статус стейджа остаётся `done`.
  - Краш во время `hook_failed` → restart резюмит `hook_failed`, не теряет решение.
- **`pkg/server`**: тесты хендлеров `retry-hook`/`skip-hook` — гейт по статусу `hook_failed`, 400 в остальных случаях (по образцу существующих тестов `handleRetry`).

## Вне охвата

- Параллельное выполнение хуков (script_before/after выполняются строго последовательно с основным содержимым, никакого fan-out).
- Настраиваемая per-stage retry-политика для хуков (фиксированные 3×/1-2-3s, не конфигурируются в YAML) — если понадобится, отдельная фича.
- File-based dialog protocol внутри script/script_before/script_after — это простые shell-скрипты без Q&A.
- Изменение поведения существующего поля `verify:` на агентских стейджах — оно продолжает работать как есть, независимо от новых хуков.
