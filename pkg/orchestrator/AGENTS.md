# Orchestrator

Поведение: [stage lifecycle](../../docs/stage-lifecycle.md),
[verify](../../docs/verify.md), [collaboration](../../docs/collaboration.md).
При изменении subprocess-путей читай [executor/AGENTS.md](../executor/AGENTS.md).

## Scheduling and recovery

- Общие пути: `activatePrePlannedStage` готовит стадию без planning;
  `startReadyStage` делает CAS ready → running → spawn; `executionKind` выбирает
  трек с учётом legacy `autonomous.flag`. Auto-стадии не требуют `plan.md`.
- Агента стадии запускай через `spawnKind`/`spawnAgentLeased`: прямой
  `SpawnAgent` обходит регистрацию lease и ломает учёт verify в `max_parallel`.
  Helpers используют owner-keyed `SpawnOwned`; служебные memory/JSON-fix задачи —
  `SpawnDetached`, с учётом shutdown, без повторного захвата слота автора.
- Startup и Continue используют общий `resumeStageAtStatus`. Fresh-start
  выполняет before-hook, resumed agent — нет; retry без planning после рестарта
  проходит через ready/before-hook.
- Завершай через общий completion-путь с проверкой required helpers и каскадом
  зависимостей. Голый `EvComplete` оставляет зависимые стадии в pending.
- `runnerFor` привязывает `OnAction` к stage ID. Инъектированный тестовый runner
  не должен подменять production-runner стадии с собственной командой.

## Paused Stage Status

- `auto_run: false` действует только на первой активации; не очищай `PausedFrom`.
  Script-стадии поддерживают этот gate, но не остановку посреди скрипта.
- Pause сначала durable-коммитит переход, затем сигналит interrupt-канал.
  После before-hook и после ожидания семафора снова проверяй разрешение запуска.
  В backoff ожидание тоже должно прерываться.
- Continue и startup связывают чтение статуса, CAS и `continuedThisProcess`
  через `continueMu`, чтобы не запустить одну стадию дважды.

## Dialog and user input

- Ответ записывается атомарно (O_EXCL) ДО изменения FSM; дубли дают conflict.
  `AFM_STAGE_DIR` нужен всем стадийным агентам. Протокол и имена фаз общие с `pkg/mcp`.
- `runWithRetry` — каноническое решение о completion; поллер не должен заново
  выводить готовность из файлов и терять завершение из `awaiting_user_input`.
- Поллер забывает отвеченные ключи, чтобы reused question ID не завис навсегда.
  Для malformed JSON сохраняй grace-tick перед изолированным repair-агентом.
- Non-interactive auto-answer не меняет FSM; его UI-notice должен быть durable.
  Pre-note вклеивается в fresh prompt, feedback-пути не вклеивают его повторно.

## AI-verify

- Verify запускается после успешного file-probe, вне planning. GET/status не
  должен запускать платную проверку. Verify сам не завершает стадию.
- Сохраняй общий retry/correction-путь и typed errors: rejection, execution
  failure и interruption различаются до классификации по свободному тексту.
- Истёкший step-timeout не может стать pass; Pause/Revise/cancel не превращаются
  в `needs_changes`. Терминальные verify-ошибки используют CAS `EvVerifyFail`.
- Персист результата/отчёта/manifest/feedback предшествует публикации события;
  storage-сбой проверки — fail-closed. Raw result сохраняется до валидации.
- Lease меняет команду release-before-acquire; та же команда — no-op, иначе
  возможен self-deadlock при `max_parallel: 1`. Ожидание прерывается Pause/Revise.
- `verify_tree_modified` — наблюдение, не основание менять verdict. Строгий JSON
  result-контракт и лимиты проверяй общим декодером, не ad-hoc парсингом.

## Durable completion barrier

`awaiting_assistants` означает, что основной исполнитель уже закончил. Recovery
и Continue восстанавливают ожидание helper-gate, не перезапускают автора и verify.
Required-гейт использует durable verdict/waiver текущего поколения; advisory
задачи не блокируют root. См. также [state/AGENTS.md](../state/AGENTS.md).

- Состояние задач Board никогда не выводится из свободного текста; actor и
  assignee — это durable ID участника, а не имя или текст сообщения.

## Memory and tests

- Live reflection только собирает датасеты; финализация памяти выполняется в
  конце рана после pending reflections. Общий движок и lock-контракт —
  [memorypipeline/AGENTS.md](../memorypipeline/AGENTS.md).
- В тестах используй `runOrchestratorAsync`: cancel + ожидание Run регистрируются
  ПОСЛЕ cleanup Store (LIFO), чтобы агенты не писали в уже закрытый журнал.
