# Явная фаза `auto` — статический автономный трек стадии

**Дата:** 2026-07-21
**Статус:** design (одобрен к реализации)

## Цель

Дать возможность в YAML стейджа жёстко указать автономный трек — `agents: [auto]` — чтобы стадия исполнялась автономным агентом **напрямую**: без supervisor/LLM-решения и без фолбэка на базовые фазы. Причина: supervisor не всегда правильно определяет, что стадию можно исполнить автономно; пользователю нужно захардкодить это решение per-stage.

## Контекст (как автономный трек работает сейчас)

Сегодня автономный трек достижим ТОЛЬКО через `startWithSupervisor` → `DetermineStagePhases(ctx, s)`, который возвращает `["autonomous_execution"]` лишь когда `stage.Supervisor=true` и живой LLM-супервизор решил `CanExecuteAutonomously=true`. При любой ошибке LLM/парсинга — фолбэк на базовые фазы. Признак «стадия в автономном треке» в рантайме — файл `autonomous.flag` в stage-директории; на него завязаны `startReadyStages`, `retryStage`, recovery, `dialogPhases`, `clearInteractiveSessions`.

`auto` делает то же решение **статическим** (из YAML), в обход LLM и фолбэка.

## Синтаксис YAML

```yaml
stages:
  - id: sync-manifests
    name: "sync goga manifests"
    agents: [auto]        # ← стадия исполняется автономно, без supervisor/LLM
    interactive: true     # опционально — autonomous-трек поддерживает dialog protocol
    depends_on: [build]
```

`auto` — зарезервированное значение в списке `agents`. Должно быть ЕДИНСТВЕННЫМ агентом стадии.

## Изменения в модели (`pkg/flow`)

1. **Константа:** `const AgentAuto AgentType = "auto"`.
2. **`Stage.IsAuto()`** → `len(s.Agents) == 1 && s.Agents[0] == AgentAuto`.
3. **Короткое замыкание `auto` в хелперах агентов** — критично: `auto` НЕ должен приниматься за кастомный implementation-агент (сейчас `HasAgent(AgentImplementation)` и `ImplAgent()` считают любой не-built-in агент реализацией):
   - `NeedsPlanning()` → `false` для auto-стадии (нет planning-фазы).
   - `HasAgent(a)` → для auto-стадии возвращает `false` для всех built-in фаз И не трактует `auto` как implementation (явная проверка `IsAuto()` перед custom-agent логикой).
   - `ImplAgent()` → не применяется к auto-стадии (её реализует автономный агент, не implementation-команда); если вызвана — не должна вернуть `"auto"` как команду.
4. **Валидация в `ParseFile` (fail-fast, понятные сообщения):**
   - Если `auto` присутствует в `agents`, но не единственный → ошибка `stage %q: "auto" must be the only agent`.
   - Если `IsAuto()` и `Supervisor=true` → ошибка `stage %q: "auto" is incompatible with supervisor: true` (противоречивые интенты: auto = без LLM, supervisor = решает LLM).

## Изменения в оркестраторе (`pkg/orchestrator`)

1. **Предикат «автономна ли стадия»** становится двусоставным: статический признак из определения (`stage.IsAuto()`) ЛИБО рантайм-флаг (`isAutonomousStage(stageDir)`). Определение авторитетно; флаг остаётся механизмом непрерывности для retry/recovery/dialog-путей.
2. **Маршрутизация auto-стадии:** auto-стадия пропускает planning и supervisor. Когда её зависимости выполнены и она активируется, она идёт СРАЗУ в автономный трек:
   - записывается `autonomous.flag` (чтобы все существующие flag-зависимые пути — `startReadyStages`, `retryStage`, recovery, `dialogPhases`, `clearInteractiveSessions` — работали без изменений),
   - durable-переходы в running,
   - `runAutonomousAgent`.
   - `DetermineStagePhases` для auto-стадии **НЕ вызывается** → нет LLM, нет фолбэка.
3. **Точка активации — `tryActivatePrePlanned` (+ страховка в `startReadyStages`).** Поскольку `NeedsPlanning()=false`, auto-стадия активируется как «pre-planned»: pending-стадию без планирования обрабатывает `tryActivatePrePlanned` (переводит Pending→Ready, затем `startReadyStages` запускает её). НО текущий `tryActivatePrePlanned` для не-планирующих стадий копирует `plan.md` из `stage.Plan` — у auto-стадии плана нет, и копирование упало бы «copy plan failed» ДО активации. Решение:
   - В `tryActivatePrePlanned` добавить ранний auto-branch: если `s.IsAuto()` → `MkdirAll(stageDir)` + `WriteFile(autonomous.flag)` + `Trigger(EvReady)`, `continue` (БЕЗ копирования plan.md).
   - `startReadyStages` уже содержит `if isAutonomousStage(stageDir) → runAutonomousAgent`; после записи флага в шаге выше эта ветка срабатывает без изменений. Для страховки (если флаг не записался) расширить условие до `if isAutonomousStage(stageDir) || stage.IsAuto()`.
   - Recovery резюмит auto-стадию через тот же `tryActivatePrePlanned` (вызывается из `startPlanningForPending`) + флаг на диске.

   Инвариант: auto-стадия всегда попадает на `runAutonomousAgent`, никогда на `runImplementationAgent`, plan.md для неё не копируется/не создаётся, и `DetermineStagePhases` для неё не зовётся.
4. **Recovery/retry:** auto-стадия резюмится/ретраится по существующему autonomous-пути (флаг на диске + `stage.IsAuto()` как страховка, если флаг потерян).

## Что auto наследует от текущего автономного трека (без изменений)

Нет `plan.md`, нет approval-гейта, доступен file-based dialog protocol (`Interactive:true` в `runAutonomousAgent`), completion-check по `execution_summary.md`, `verify:` выполняется после завершения.

## Тестирование

- **flow-parse:** валидный `agents: [auto]` парсится; `agents: [auto, planning]` → ошибка; `agents: [auto]` + `supervisor: true` → ошибка.
- **Модель:** `IsAuto()` (true только для `[auto]`), `NeedsPlanning()==false` для auto, `HasAgent`/`ImplAgent` не трактуют `auto` как implementation.
- **Интеграция (orchestrator):** стадия `agents: [auto]` (мок-agent через `stage.Command`, как в существующих autonomous/interactive тестах) — запускается автономный агент, `autonomous.flag` создан, `plan.md` НЕ создаётся, supervisor НЕ опрашивается (проверяемо: без `supervisor:true` DetermineStagePhases и так не зовёт LLM — тест утверждает выход на autonomous-трек и отсутствие planning-артефактов).

## Вне охвата

Изменение поведения supervisor-решаемого автономного трека; новые автономные возможности; миграция существующих flow (обратная совместимость полная — `auto` аддитивен).
