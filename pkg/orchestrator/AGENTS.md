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
- Любой результат хода, и ошибка агента тоже, действует, только пока ход владеет эпохой: после Pause/Continue/Revise
  он ничего не меняет, кроме hand-off Revise своего хода. Fail и retry хода fenced его эпохой (`failTurn`).
- Исход helper-хода (result, delegate, ошибка ответа) коммитится только под turn-ownership guard в Store lock; Pause→Continue виден через `TaskState.HoldEpoch`.
- Guard исходного helper-хода обязателен и для ошибок процесса, quota/yield; final-pass watcher потребляется только после успешного коммита.
- `result.json` должен быть на диске до видимого commit результата helper; отвергнутый commit восстанавливает прежние байты или отсутствие файла, сохраняя per-turn артефакты.
- `runnerFor` привязывает `OnAction` к stage ID. Инъектированный тестовый runner
  не должен подменять production-runner стадии с собственной командой.
- Новый AI-путь получает `executor.Config.Job` через `jobConfig` (`agent_jobs.go`): ключ — participant ID
  (`meshParticipantForTask`), verify — `verify:<stage>#<шаг>`, format repair — владелец ответа. Инъектированный
  runner jobs не пишет; `Options.Jobs` закрывает вызывающий после возврата `Run`. Исход `yielded` решает то же
  правило, что и runtime (`turnEpisode` автора, `turnYielded` helper) по ожиданиям до старта процесса: ответ на
  ожидание до выхода процесса не делает ход `ok`.
- `AnalyticsMeta.LiveSince` не сбрасывается с концом флоу: Assistant работает и после него. Usage без job
  атрибутируется через метаданные (задача → участник, Assistant), поэтому Assistant в них назван всегда.
- `data.analytics` terminal-хука собирается в `emitLifecycleTransition` после CAS и до `Emit`, всё режется по времени
  перехода с этим seq; usage — только через jobs замороженного журнала (по полям — лишь legacy без `job_id`). Стадия,
  ушедшая дальше, даёт неизвестный timing. Живая работа — partial, без нового FSM-гейта.

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
- Решения диалога (`EvAskUser`/`EvUserAnswered`) и доставка ответа принимаются под `answerMu`, проверка файла на
  диске — внутри него; ход владеет эпохой своей выдачи (`grantTurn`), а не прочитанной позже. «Горутина ещё активна»
  (`IsActive`) не значит, что агент прочтёт ответ: это `liveAgents`.
- Вопрос задан, только если `EvAskUser` закоммичен или стадия уже у пользователя: отвергнутый переход (revising) —
  не всплытие, ключ поллера остаётся для следующего скана.
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
- Запрос, переданный task-агенту, который ушёл в покой без ответа, неотвечаем
  (`leftUnanswered`): ожидания на него закрываются как cancelled с причиной
  проверкой неотвечаемых ожиданий — никогда не ready по результату хода, Board
  done или тексту сообщения. Старт хода (`start_task`, единственный переход
  queued→running) отменяет в том же коммите ожидания на запросы, брошенные этим
  агентом; фоновые проверки после ухода в покой (finish, quota park, resume) нужны
  только для своевременности. Запрос из очереди on_message-помощника (Pending/Active,
  и прочитанный заранее) не брошен: его закрывает только собственный исход.
- on_message-помощник отдыхает в `watching`; в очередь его ставит только CAS
  `wakeOnMessageTask` после коммита, который мог дать работу (сообщение ему, конец его
  запроса, Continue). Ход называет свой запрос, результат и ответ привязаны к нему, а не к
  будящему сообщению; пока запрос не закончен, новые запросы ждут своей очереди и не будят
  помощника как встречный вопрос. Упавший ход заканчивает только свой запрос; инфраструктурный сбой, мешающий
  продолжить (например, процесс не запустился), как у других задач роняет задачу. Запросы, включая
  незаконченный, остаются за ней, и Retry продолжает их по порядку; помощника прежней попытки стадии Retry не
  возвращает — и проверяет это под lock Store, ведь новая попытка может прийти до коммита.
- on_message-помощника закрывает только система и только отдыхающего без запросов (`settleOnMessageTask`):
  у `done` стадии — с причиной, без результата и verdict; у `awaiting_assistants` — required, чей последний
  запрос дал pass, с этим pass (гейт закрывает статус `completed`). Закрытие — CAS на `watching` с проверкой
  под lock Store, поэтому принятый тем временем запрос оставляет помощника. Вызывай его везде, где помощник
  может стать закрываемым: переход стадии, конец запроса, Retry задачи, рестарт (и при выключенной коллаборации:
  закрытие не вызывает модель). Пока стадия на паузе, ход не стартует (`onMessageGated`): запись старта хода —
  последний коммит перед процессом и отказывает под lock Store, а ход возвращается в очередь; выход из паузы будит
  помощников. Failed стадия ничего не отменяет и не закрывает; конец запроса будит цикл Run, ведь отдыхающий
  помощник больше не держит ран.
- Ожидание стадийного агента решается по токену его вызова (`WithMeshInvocation`) под lock Store: запрос
  завершившегося вызова или прежней попытки (после Pause/Continue/Revise) ничего не регистрирует. Эпоха токена —
  эпоха живого хода (`liveTurns`), не Store. Revoke удаляет токен: Wait получает только токены, прошедшие
  аутентификацию сервера, поэтому токен стадии, которого нет среди живых, — токен завершившегося вызова.
- Граф admission ожиданий включает невыполненные `depends_on` ещё не стартовавших стадий; список стадий строится
  внутри commit (registry плюс `DynamicStages` журнала): ожидание зависимости на её зависимую стадию — `wait_cycle`.
  Ожидание `done` стадии требует и её невыполненных `depends_on` при любом её статусе (eager planning стадия уже
  планирует и отвечает на сообщения); исключение — eager planning-only стадия, её завершает approve плана.
- `on_completion`-помощник стартует только после результата своей стадии, поэтому граф admission даёт ещё не запущенному помощнику ребро
  к стадии и её `depends_on` (`completionPassPending`): ожидание автора на него — `wait_cycle`; запущенный помощник ребра не имеет.
  Такой ещё не начатый помощник стадии не отменяет и не заменяет callback-исключение её помощников (`stageOwnHelper`:
  one-shot, которых агент стадии запустил сам, и любые on_message текущей попытки стадии).
- Провал стадии отменяет по тому же `completionPassPending` её ещё не начатые проходы (`cancelled`, префикс `completionPassFailedReason`)
  на всех путях — переход, рестарт, `maybeScheduleTask` и каждый возврат хода в queued (слот, пауза, quota park): провал мог
  прийти, пока ход был running. Retry до старта новой попытки возвращает в `watching` только их.
- Привязку watcher'а после коммита меняет только `reconcileWatcherBinding`: чтение задачи и смена привязки — один шаг под
  `watchReconcileMu`, взятым до lock Store и `watchMu` (проверка loop tick берёт `watchMu` под lock Store).
- Recovery решает по намерению из журнала, а не по текущему статусу: bump Revise доделывается для любой стадии,
  стадия с pending-ожиданием не завершается по маркеру, а паркуется (`parkInterruptedTurn`, из running и retrying).
- Завершение autonomous-стадии по файлам решает один `executionProbe` (summary, не `.done`) на всех путях recovery;
  ход, стартовавший в retrying (Continue, рестарт), сам переводит стадию в running в начале `runRetryLoop`. Признак
  `Summary.Issued` снимает любой переход, кроме записи-самоперехода внутри живого хода.
- Revise из awaiting_assistants бампит поколение под ключом seq своего перехода (`reviseBumpOpID`); Revise из
  других статусов не бампит.

## Memory and tests

- Live reflection только собирает датасеты; финализация памяти выполняется в
  конце рана после pending reflections. Общий движок и lock-контракт —
  [memorypipeline/AGENTS.md](../memorypipeline/AGENTS.md).
- В тестах используй `runOrchestratorAsync`: cancel + ожидание Run регистрируются
  ПОСЛЕ cleanup Store (LIFO), чтобы агенты не писали в уже закрытый журнал.
