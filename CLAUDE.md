# AFM Development Guide

## Working directory: `.afm` and `--dir`

By default afm stores runs, flows, and config under `.afm/` in the working directory. The parent directory is resolved in `PersistentPreRunE` (`cmd/afm/main.go`) with priority **flag > env > `.`**: the `--dir` persistent flag, else the `AFM_DIR` env variable, else the current directory. All subcommands read the effective `.afm` path via `fmDir()` (`filepath.Join(rootDir, ".afm")`); `state.FindLatestRunDir(base, flowName)` takes the runs base as an explicit argument instead of hardcoding the path.

**Корень проекта для агентов: `flow.root_dir`.** afm предполагает, что afm-корень (родитель `.afm/`) == корень проекта. Если это не так (напр. Docker: исходники в `/workspace`, `.afm/` в другом каталоге), агенты, наследуя CWD процесса afm, резолвят относительные пути проекта (`docs/arch/…`) в чужом корне — стадии расходятся: одна пишет файл, другая его не находит. Поле `root_dir` в `flow.yaml` (`flow.Flow.RootDir`) задаёт CWD агентов: относительный путь резолвится от afm-корня в `cmd/afm/run.go`, прокидывается `orchestrator.Options.RootDir` → `executor.Config.Dir` → `cmd.Dir` в `executor.run`. Пусто → прежнее поведение (наследование CWD). `AFM_STAGE_DIR` (файлы диалога) остаётся привязан к afm-корню независимо от `root_dir`.

**Атрибуция `agent_action` к стадии.** События tool-действий агента получают `stageID` из `OnAction`-замыкания per-stage runner'а (`runnerFor`, `runner_factory.go`). Инъектированный `o.runner` (тесты, пустой stageID) используется ТОЛЬКО когда `opts.Runner != nil` и у стадии нет своего `command`; в проде каждая стадия всегда получает per-stage runner с правильным `s.ID` — иначе бейдж стадии в event feed дашборда пропадал (`EventFeedPanel.tsx` рисует бейдж только при непустом `stageId`).

## State persistence & run lifecycle (reliability core)

Событийный лог `.afm/runs/<run_id>/events.jsonl` — **единственный доверенный источник правды**. `state.json` — производный кэш (несёт `last_seq`), пути чтения (`afm check`, поиск run) читают состояние из лога через `state.LoadRunState` (без flock), не доверяя снапшоту.

- **flock между процессами.** `state.Open` берёт эксклюзивный `flock` на `<runDir>/.lock` на всё время жизни `Store`. Живой `afm run` держит его; CLI `approve`/`retry`/`revise` при активном run падают с `state.ErrRunLocked` и понятным сообщением. flock освобождается ОС при завершении процесса — упавший run не оставляет «залипшей» блокировки. `afm check` (read-only, без flock) живым run не блокируется.
- **Недеструктивный replay.** Оборванный хвост (последняя запись без `\n`, crash при append) безопасно усекается. Битая **полная** строка в середине лога (валидные записи после неё) → карантин в `events.jsonl.corrupt-<ts>` + `state.ErrCorruptLog`, оригинал НЕ трогается (никогда не усекаем разрушительно).
- **Долговечный snapshot.** `writeSnapshot` делает `f.Sync()` перед Close и fsync родительской директории после Rename. Ошибка записи снапшота нефатальна (это кэш), но read-пути всё равно берут состояние из лога.
- **Уникальный run-id** — `<flow>-<timestamp>-<rand4hex>` (нет коллизий в одну секунду). `state.FindLatestRunDir` якорит префикс (после `<flow>-` обязана быть цифра — `foo` не матчит `foo-bar`); `state.FindLatestRunForStage` — единая точка поиска run по стадии из лога.
- **Storage-fatal завершает run.** `Trigger` через `errors.As(*StorageError)` различает реальный сбой записи лога (→ `setFatal` + отмена run-ctx → `Run` возвращает ошибку) от доброкачественного `ErrConcurrentChange` (CAS-mismatch, тихий no-op) и `ErrNoRule` (log-and-drop, не валит run).
- **Чистый shutdown.** Все агентские горутины запускаются через `spawnAgent` (семафор + маркер активности + `agentWG`). На выходе `Run`: `cancel()` (LIFO — раньше) → `waitAgents()` (bounded 10s) → потом `store.Close()`. Завершения агентов публикуются под run-ctx (не `context.Background()`) — не блокируются навсегда на мёртвой шине.
- **Долговечный approve/revise/retry.** `Approve`/`Revise`/`Retry` синхронны: durable-переход фиксируется в логе ДО возврата (краш не теряет интент — recovery резюмит `ready`/`revising`/`running`). Headless auto-approve обрабатывается inline (нет блокирующего self-publish в event-loop). **Важно:** HTTP-инициированные approve/revise/retry спавнят агента под **run-scoped ctx** (`runContext`), а не под `r.Context()` — иначе net/http отменяет ctx при возврате хэндлера и агент убивается мгновенно. Спавны, достижимые и из HTTP-горутины, и из event-loop, guard'ятся по CAS-результату `Trigger` (нет двойного запуска).
- **`startReadyStages` чтит `autonomous.flag`.** CAS-guard на `EvStartRun` предотвращает только повторный запуск ОДНОГО И ТОГО ЖЕ агента — но не гарантирует, что выигравший гонку код знает, какой агент запускать. `retryStage` для autonomous-стадии переводит её `Pending → Ready` через `EvReady`, а затем сам берёт `EvStartRun` — в узком окне между этими двумя переходами конкурентный вызов `startReadyStages` из другой ветки event-loop (например, `onAgentCompleted` другой стадии) мог выиграть CAS первым и слепо запустить `runImplementationAgent` (он читает `plan.md`, которого у autonomous-стадии нет → падение "no such file or directory"). Поэтому `startReadyStages` перед спавном проверяет `isAutonomousStage` и для таких стадий запускает `runAutonomousAgent` — симметрично уже существующим проверкам в `recovery.go` и в самом `retryStage`.
- **Жёсткий автономный трек: `agents: [auto]`.** В YAML стадии можно статически задать автономный трек — `agents: [auto]` (тип `flow.AgentAuto`, детект `Stage.IsAuto()`). Такая стадия идёт `runAutonomousAgent` НАПРЯМУЮ, без вызова `DetermineStagePhases` (без LLM-супервизора и без фолбэка). Активация — общий хелпер `activateAutoStage` (пишет `autonomous.flag` + `EvReady`, БЕЗ `plan.md`), вызываемый из ОБОИХ путей активации no-planning-стадии: `tryActivatePrePlanned` (scheduling.go) и `startPlanningForPending` (recovery.go) — иначе на fresh/zero-dep запуске recovery-ветка попыталась бы скопировать несуществующий `plan.md`. `startReadyStages` дополнительно чтит `stage.IsAuto()` (страховка, если флаг не записался). Короткое замыкание в `flow.HasAgent`/`ImplAgent` не даёт трактовать `auto` как custom-implementation-агента. Валидация `ParseFile`: `auto` — единственный агент; `auto`+`supervisor:true` → ошибка. Спек/план: `docs/superpowers/specs/2026-07-21-auto-phase-design.md`.

### Персистентные IDLE/BACKOFF метрики футера

Футер дашборда (`PROGRESS`/`STARTED`/`ELAPSED`/`IDLE`/`BACKOFF`) переживает reload и restart afm с той же точностью, что уже была у `STARTED`/`ELAPSED`, и не тикает, пока WebSocket не в сети.

- **Накопители в `RunState`, не события.** `pkg/state/state.go` хранит `IdleAccumulatedMs`/`BackoffAccumulatedMs` (`int64`, персистятся в снапшот и восстанавливаются replay'ем `events.jsonl`). `IdleSince() *time.Time`/`BackoffOpenSince() []time.Time` — не персистятся, вычисляются на чтении из `Stages[].UpdatedAt` (idle — максимум по всем стадиям; backoff — `UpdatedAt` каждой стадии, у которой `Status == StatusRetrying`).
- **Один хелпер, два места вызова.** `accountIdleAndBackoff(rs, stageID, to, t)` обновляет оба накопителя, используя состояние `rs.Stages` ДО применения перехода. Вызывается из `Store.Apply` (живой путь) И из `parseEventLog` (replay-путь `Store.Open`/`LoadRunState`) — это **разные** функции, `parseEventLog` не проходит через `Apply`. Забыть один из двух call site'ов — значит разойтись в цифрах между live-раном и его restart'ом.
- **Idle — общефлоуовое состояние**, не per-stage: idle, если любая стадия в `awaiting_user_input`/`awaiting_approval`, ИЛИ есть `failed`-стадия и при этом ни одна не `running`/`planning`/`revising` (`retrying` НЕ считается активной — это пассивный backoff, не работа агента).
- **Backoff суммируется параллельно**, не мержится: если несколько стадий одновременно в `retrying`, их интервалы складываются независимо (та же упрощённая модель, что была в старом `useStatusDuration`).
- **`Store.Apply` берёт `t.Time` один раз.** До фикса `SetStageStatus` внутри звал `time.Now()` повторно после fsync — расхождение между залогированным временем перехода и временем, использованным для учёта, портило точность на длительность fsync. `SetStageStatusAt(t.Time)` использует ровно тот timestamp, что попал в лог.
- **`NewRunState` не штампует `UpdatedAt` для pending-стадий.** Иначе при каждом `Store.Open` (в т.ч. на resume, когда "сейчас" — момент рестарта процесса, а не момент реальной последней транзакции) непотронутая pending-стадия получала бы `UpdatedAt = time.Now()` и всегда перекрывала бы по времени реальные исторические переходы в `maxUpdatedAt()` — тихо портя `IdleSince`/недосчитывая Idle после каждого restart.
- **API:** `/api/status` отдаёт `idle_accumulated_ms`, `idle_since` (omitempty), `backoff_accumulated_ms`, `backoff_open_since` (`[]time.Time`, пустой массив — если сейчас нет открытых эпизодов).
- **Фронтенд — anchor + tick, без event-replay.** `useIdleMs`/`useBackoffMs` (`pkg/web/dashboard/src/hooks/`) считают `accumulated + (now - since)`, как уже работавший `useElapsed`, и принимают `connected: boolean` — при `false` тикер замирает на последнем значении; при реконнекте `useStatus`'ный poll подтягивает уже скорректированный сервером якорь, никакой клиентской доверстки не нужно. Заменили `useIdleTime`/`useStatusDuration` (event-replay по `useEventFeed`'ному 200-событийному кэшу — на длинных ранах старые переходы вываливались из кэша и IDLE/BACKOFF тихо недосчитывали после reload).
- Спек/план: `docs/superpowers/specs/2026-08-07-persistent-idle-backoff-design.md`, `docs/superpowers/plans/2026-08-07-persistent-idle-backoff.md`.

### Порядок стадий в дашборде — топологический, не порядок объявления

Список стадий слева в дашборде рендерится в порядке, который отдаёт `GET /api/status` (`stages []StageView`) — раньше это было ровно `state.RunState.StageOrder`, т.е. порядок объявления в `flow.yaml`. Стадия, объявленная в YAML раньше своей же зависимости (ради читаемости флоу), рисовалась выше неё, хотя реально стартует только когда зависимость завершится — список не отражал граф выполнения.

- **`buildStageViews` (`pkg/server/stageview.go`) пересчитывает порядок через `topoOrder`** — устойчивый (stable) вариант алгоритма Кана: очередь готовых узлов заводится и пополняется в порядке исходного `StageOrder`, поэтому независимые стадии без связи между собой сохраняют взаимный порядок объявления (не тасуются итерацией по map), а зависимая стадия рендерится сразу после ВСЕХ своих `depends_on`, а не перед несвязанными соседями просто потому что была объявлена раньше.
- **Пример:** `stage1 (deps:[stage2]), stage2, stage3, stage4, stage5, stage6 (deps:[stage2,3,4,5])` в объявлении → рендерится как `stage2, stage3, stage4, stage5, stage1, stage6`.
- **`state.RunState.StageOrder` не трогается** — это авторитетный порядок для `state`/`scheduling` (реплей лога, CAS-переходы и т.д.); `topoOrder` — чисто display-слой поверх него, живёт только в `pkg/server`.
- Новый `Server.stageDependsOn`/`Config.StageDependsOn` (`pkg/server/server.go`) заполняется из `flow.Stage.DependsOn` в `cmd/afm/run.go`, рядом с уже существующими `stageInteractive`/`stageAutoApprove`.
- Защитный фолбэк в `topoOrder`: если результат не покрыл все id (цикл или ссылка на несуществующую стадию), возвращает исходный порядок как есть — на практике недостижимо, `flow.ParseFile`'s `detectCycles` уже отвергает такие флоу на этапе парсинга.

### Автопродвижение выбранной стадии в дашборде — ретрай на каждом опросе, не одноразовая проверка

`App.tsx` держит выбор пользователя (`selectedStageId`) и автоматически продвигает его к следующей активной стадии, когда стадия, за которой сейчас следят, завершается — но не перекидывает пользователя, если он сам вручную открыл уже завершённую стадию (посмотреть план/лог/диалог).

- **`wasLive` — per-selection флаг, не per-tick.** Раньше продвижение проверялось РОВНО в тот тик, когда выбранная стадия переходила `!done → done` (сравнение с предыдущим статусом). На скриптовых стейджах (`Stage.IsScript()`, `pkg/flow/flow.go`) `running` может длиться доли секунды — несколько стадий подряд успевают пройти `running → done` МЕЖДУ двумя опросами `/api/status` (раз в 3с). К моменту, когда фронтенд наконец видит «стадия1 стала done», стадия2 уже тоже done — среди `ACTIVE_STAGE_STATUSES` искать нечего, и старая проверка сдавалась НАВСЕГДА (следующий опрос её уже не перезапускал, раз статус уже done) — выбор залипал на стадии1, хотя реально работает стадия3/4.
- **Фикс:** `wasLive.current` живёт, пока текущий `selectedStageId` не поменяется (сбрасывается только при смене выбора, не при каждом опросе). Пока стадия done и была замечена «живой» под этим выбором — поиск следующей активной стадии повторяется на КАЖДОМ опросе, а не один раз. Самокорректируется в пределах одного цикла опроса вместо необратимого залипания. Ручной клик по уже завершённой стадии никогда не выставляет `wasLive` → её выбор не трогается (сохранено прежнее поведение).

## Paused Stage Status

Новый статус стадии `paused` — три сценария: (1) `auto_run: false` в `flow.yaml` гейтит первую активацию стадии (стадия не стартует сама, ждёт Continue); (2) ручная пауза через пункт "Pause" в кебаб-меню стадии, доступна для `running`/`planning`/`revising`/`retrying`; (3) скриптовые стадии (`script:`) паузятся только через (1) — mid-script graceful stop архитектурно не поддержан (`RunScript` не принимает interrupt-канал).

- **`state.StageState.PausedFrom` — двойного назначения поле, не очищается при выходе из paused.** Хранит статус, из которого стадия ушла в паузу (`running`/`planning`/`revising`/`retrying`/`pending`). Пока стадия в `paused` — это то, куда резюмиться. После Continue (когда статус уже НЕ `paused`) поле остаётся непустым НАВСЕГДА — это постоянная метка "стадия уже проходила цикл паузы хотя бы раз", которую использует `shouldGateAutoRun` (`scheduling.go`), чтобы `auto_run:false` срабатывал только при самой первой активации, а не при каждом повторном заходе в `pending` после `failed`→retry. `pkg/server/stageview.go`'s `StageView.PausedFrom` (JSON `paused_from`) НЕ наследует эту перманентность — заполняется только когда `Status == StatusPaused`, иначе пустая строка, чтобы не светить в API устаревшее значение стадии, давно вышедшей из паузы.
- **`Continue` == "притвориться, что afm только что перезапустился и нашёл эту стадию в статусе `PausedFrom`".** `Orchestrator.Continue` (`control_api.go`) для `PausedFrom == pending` заново прогоняет обычную активацию (`tryActivatePrePlanned`+`startPlanningForUnblocked`, гейт уже сам себя не пустит повторно благодаря непустому `PausedFrom`); для остальных четырёх статусов — `resumeStageAtStatus` (`recovery.go`), ТОТ ЖЕ диспетчер, которым `startPlanningForPending` уже резюмирует стадию после краша afm. Ручная пауза и "процесс упал, afm перезапустили" — с точки зрения планировщика одна и та же ситуация: "процесс, подразумеваемый этим статусом, сейчас не бежит — запусти его".
- **Ручная пауза переиспользует `interruptChans`** — тот же канал/механизм (SIGINT + 15s grace), которым `Revise` уже пользуется для agent_suggest на `running`-стадии. `Orchestrator.Pause` (`control_api.go`) синхронно фиксирует `EvPause` в логе (до сигнала в канал) — durable-переход раньше, чем асинхронный эффект, тот же паттерн, что и `Approve`/`Revise`/`Retry`. `runWithRetry` (`retry.go`) при `ErrUserInterrupted` проверяет `currentStatus == paused`, чтобы отличить "это Pause" (ничего не резюмить, переход уже случился) от "это Revise" (перезапустить с фидбеком) — оба используют один и тот же канал. Для статуса `retrying` (пассивный бэкофф-таймер, живого процесса нет) в `runWithRetry`'s backoff-select добавлена ветка `case <-interruptCh:` — `Revise` туда доехать не может (её precondition — только `awaiting_approval`/`running`), так что сигнал в этом select однозначно означает `Pause`.
- **`withBeforeHook` (`hooks.go`) перепроверяет статус после `script_before`.** `script_before` выполняется голым shell-скриптом БЕЗ interrupt-канала (тот регистрируется только внутри `runWithRetry`, для основного агента, уже ПОСЛЕ хука) — `Pause()` мог успешно увести стадию в `paused`, пока хук ещё крутился в фоне, и по его завершении `mainFn` запускался бы поверх уже поставленной на паузу стадии (плюс второй раз — если пользователь успел нажать Continue раньше). Фикс: `withBeforeHook` не вызывает `mainFn`, если `currentStatus(s.ID) == StatusPaused` после хука.
- **Гонка "Pause во время очереди за семафором" закрыта централизованно в `concurrency.Manager.SpawnAgent`, не патчами по вызывающему коду.** Изначально (найдено отдельным аудитом "нет ли протечек в FSM") эта гонка чинилась точечной проверкой `currentStatus == paused` в начале `runWithRetry` — рабочий, но размазанный по двум местам фикс (второй — `withBeforeHook`). На вопрос "почему дыр для протечек несколько, нельзя сделать одно место управления?" логика была перенесена на уровень ниже: `Manager` (`pkg/orchestrator/concurrency/concurrency.go`) получил поле `shouldRun func(stageID string) bool`, которое `SpawnAgent` проверяет СРАЗУ ПОСЛЕ `sem.acquire()`+`markActive`, ДО вызова `run(ctx, s)` — то есть максимально близко к месту, где горутина реально просыпается после (возможно долгого) ожидания слота в `max_parallel`-семафоре. `orchestrator.New` передаёт замыкание `func(id string) bool { return opts.Store.Get(id) != state.StatusPaused }` в `concurrency.New` (единственный продакшн call site, `pkg/orchestrator/orchestrator.go`). Это единая точка для ЛЮБОГО пути запуска агента (planning/implementation/review/autonomous, fresh или `*WithFeedback` резюм, через `startReadyStages`/`retryStage`/`resumeStageAtStatus`/HTTP-хендлеры) — раз все они уже обязаны идти через `SpawnAgent` (это и есть его исходное назначение, см. "Чистый shutdown" выше). Проверка в `runWithRetry` стала избыточной и удалена вместе со своим тестом; проверка в `withBeforeHook` **осталась** — она защищает структурно другое окно (время выполнения самого `script_before`-хука, которое лежит ВНУТРИ `run(ctx,s)`, уже после единственной SpawnAgent-проверки, и `shouldRun` в неё заглянуть не может). Регрессионный тест на саму гонку — `TestSpawnAgent_SkipsRunWhenPausedWhileQueuedBehindSemaphore` (`pkg/orchestrator/concurrency/concurrency_test.go`), воспроизводит реальный сценарий через `ChannelSemaphore` (та же техника, что и уже существующий `TestSpawnAgent_BlocksOnFullSemaphore`): горутина стоит в очереди на занятый семафор, `shouldRun` переключается в false, семафор освобождается — `run` не должен вызваться.
- **`resumeStageAtStatus`'s `StatusRetrying`-ветка проверяет autonomous/`agents:[auto]`**, симметрично уже существующей проверке в соседней `StatusRunning`-ветке — без этого Continue после ручной паузы автономной стадии во время retry-бэкоффа уводил её в `EvStartPlanning`+planning-агента, хотя у автономных стадий никогда нет `plan.md` и планирования вообще (баг, специфичный именно для нового пути Continue — раньше эта ветка `resumeStageAtStatus` вызывалась только из `startPlanningForPending`, куда автономная стадия не попадает: её перехватывает первый switch этой же функции).
- **`pkg/server/stageview.go`'s `ShowPlan` учитывает `paused`, не только `failed`** (`showPlan := !autonomous || failed || paused`) — иначе autonomous-стадия (`agents:[auto]`), особенно с `interactive: true`, на паузе рендерила только `DialogChannel` без единой видимой кнопки Continue (PlanPanel, где она живёт, вообще не монтировался). Найдено на реальном флоу пользователя — правило для `showPlan` было написано в мире, где autonomous-стадия может дойти до `failed`, но не до `paused`.
- **`use-attention.ts`**: `paused` — часть `ATTENTION_STATUSES`/`AttentionKind` (заголовочная точка "Action needed", favicon pulse, title flash, desktop-notification с заголовком "Stage paused" в `use-desktop-notifications.ts`'s `Record<AttentionKind, string>` — TS не даст забыть добавить новый kind туда, exhaustiveness-check самого типа).
- **Найдено живым ручным прогоном (реальный glm47-агент, реальный браузер): `Continue()` мог навсегда подвесить зависимые стадии.** `resumeStageAtStatus`'s "уже завершено, восстановлено с диска" fast-path'и (autonomous `execution_summary.md` / script `.done`, обе ветки `StatusRetrying` и `StatusRunning`) финализировали саму стадию голым `Trigger(EvComplete)` + `maybeRunAfterHook`, минуя каскад `failBlockedStages`/`startPlanningForUnblocked`/`startReadyStages`/`tryActivatePrePlanned`, которым нормальное завершение агента (`onAgentCompleted` → `completeStage`) всегда сопровождается. Из bootstrap-пути (`startPlanningForPending`) это было безобидно — тот вызывает весь каскад ОДИН РАЗ после цикла по ВСЕМ стадиям флоу, так что пропуск каскада внутри одной итерации ничего не портил. Но `Continue()` резюмит ровно одну стадию и сразу возвращается — если она попадала в fast-path (типичный сценарий: агент уже дописал `execution_summary.md`, а `Pause()` сработал на долю секунды позже), никто и никогда не переоценивал стадии, ждущие её как зависимость — они зависали в `pending` навсегда, пока не перезапустишь весь процесс afm вручную. Фикс: обе "recovered" fast-path-ветки в `resumeStageAtStatus` теперь зовут тот же `completeStage(ctx, s.ID, status, reason)` (получил параметр `reason`, чтобы не терять текст вроде "recovered execution_summary.md" в логе), а не дублируют его тело вручную. Аналогичная ветка внутри `startPlanningForPending`'s собственного цикла НЕ тронута — она провably безопасна за счёт каскада после цикла, трогать её значило бы дважды звать `startReadyStages` и т.п. без driving-теста на реальную проблему. Регрессия: `TestContinue_RecoveredCompletion_UnblocksDependent` (`pause_continue_test.go`) — гоняет `orch.Continue` на стадии с уже написанным `execution_summary.md` и проверяет, что ЗАВИСИМАЯ стадия тоже доходит до `done`, не только резюмированная; тест изначально плавал (проходил по неверной причине из-за гонки между `go func(){ orch.Run(ctx) }()` и немедленным `orch.Continue()`) — стабилизирован явным `time.Sleep` после старта `Run`, чтобы гарантированно бить по пути `Continue()`, а не по каскаду bootstrap-цикла.
- Спек/план: `docs/superpowers/specs/2026-08-17-paused-stage-status-design.md`, `docs/superpowers/plans/2026-08-17-paused-stage-status.md`.

## File-Based Dialog Protocol (Interactive Stages)

The interactive dialog system was refactored from an MCP HTTP server to a file-based protocol starting with the planning-depends-on-ref branch. This enables agents to ask users questions and receive answers through simple file I/O instead of HTTP.

### Architecture

**Agent writes question:**
- Agent writes: `$AFM_STAGE_DIR/<phase>.<id>.question.json`
- Example: `planning.q1.question.json`
- Format: `{"id":"q1", "question":"...", "options":[...], "allow_custom":true/false}`

**Agent polls for answer:**
- Bash loop in agent script: `while [ ! -f "$AFM_STAGE_DIR/<phase>.<id>.answer.json" ]; do sleep 30; done`
- When file appears, agent reads it and continues
- Format: `{"id":"q1", "answer":"...", "from_options":true/false}`

**Orchestrator polls for questions:**
- `startQuestionPoller()` launches goroutine that scans every 1 second
- Detects `*.question.json` files in stage directories
- Publishes `EventAskUser` to transition stage to `awaiting_user_input`
- UI dashboard polls `/api/stages/<id>/dialog` to fetch questions

**HTTP handler processes answer:**
1. Validates phase is one of: `planning`, `implementation`, `review`
2. Validates ID is safe filename component (no path traversal)
3. Checks question.json exists (else 404)
4. Rejects if answer.json already exists (409 Conflict)
5. **Atomically writes** answer.json (O_EXCL exclusive create) — critical path
6. Appends to dialog.jsonl for UI history (best-effort, non-critical)
7. Calls `NotifyAnswer()` to either transition FSM (if agent active) or restart agent (if exited)

### Key Files

| File | Responsibility |
|------|-----------------|
| `pkg/mcp/dialog.go` | `FindUnansweredQuestions()`, `QuestionFile` type, `appendLine()` for dialog.jsonl |
| `pkg/orchestrator/orchestrator.go` | `startQuestionPoller()`, `NotifyAnswer()`, `pollQuestions()`, active agent tracking |
| `pkg/executor/executor.go` | Passes `AFM_STAGE_DIR` environment variable to agent process |
| `pkg/server/handlers.go` | `handleDialogAnswer()` with atomic write pattern (O_EXCL) |
| `pkg/prompts/builder.go` | Interactive rules instruction in system prompt |

### Implementation Details

**Agent Activity Tracking**
- `Orchestrator.activeAgents` is a `sync.Map` tracking which stages have running agent goroutines
- Goroutine acquires semaphore → calls `markAgentActive(stageID)` → deferred `markAgentDone(stageID)`
- If user answers while agent is active: `NotifyAnswer()` transitions FSM, agent bash loop detects file
- If user answers after agent exited: `NotifyAnswer()` publishes to critical bus → `onUserAnswered()` restarts agent

**Question File Naming**
- Format: `<phase>.<id>.question.json`
- Phase must be: `planning`, `implementation`, or `review`
- ID must pass `isValidDialogID()` check (safe filename, no path traversal)
- Enforces: alphanumeric + underscore only

**Answer Delivery Guarantee**
- Answer.json is written atomically (O_EXCL exclusive create) BEFORE FSM transition
- Agent bash loop will always find the file if still running
- If agent already exited, restart with `--resume` flag to re-read answer

**Dialog History**
- `<phase>.dialog.jsonl` appended for UI (best-effort, NOT critical)
- Stores: `{"timestamp":"...", "phase":"...", "role":"assistant|user", "message":"..."}`
- If append fails, agent continues anyway (answer.json already safe on disk)

### Deleted Components

- `pkg/mcp/server.go` — MCP HTTP server (replaced by polling)
- `pkg/mcp/server_test.go` — MCP server tests
- `pkg/orchestrator/mcp_notifier.go` — MCP event notifier

### Environment Variables

| Variable | Purpose | Set By |
|----------|---------|--------|
| `AFM_STAGE_DIR` | Stage directory for question/answer files | `executor.New()` when `StageDir` configured |
| `AFM_DIR` | Parent directory for `.afm` (used when `--dir` is not set) | CLI flag `--dir` / env, resolved in `PersistentPreRunE` |
| `AFM_DEBUG` | Log the exact agent input (prompt) to `<run>/debug.log` + per-stage `<run>/<stage>/<phase>.prompt.log`. Off by default. | CLI flag `--debug` / env (flag > env), resolved in `PersistentPreRunE`; wired via `Options.Debug` → `executor.Config.Debug`/`RunDir`/`StageID` |

### Testing File-Based Dialog Locally

Mock agents must implement their own polling:
```bash
while [ ! -f "$AFM_STAGE_DIR/<phase>.q1.answer.json" ]; do 
  sleep 0.5
done
answer=$(cat "$AFM_STAGE_DIR/<phase>.q1.answer.json" | jq -r '.answer')
```

Write question.json with proper schema:
```json
{
  "id": "q1",
  "question": "What should we do?",
  "options": ["Option A", "Option B"],
  "allow_custom": true
}
```

### Debugging Interactive Stages

Check stage directory for dialog files:
```bash
ls -la .afm/runs/<run_id>/<stage_id>/
# Look for: planning.q1.question.json, planning.q1.answer.json, planning.dialog.jsonl
```

Common patterns:
- **Agent waiting:** `*.question.json` exists, but no corresponding `*.answer.json` yet
- **Answer received:** Both `*.question.json` and `*.answer.json` exist, agent should have exited
- **Dialog history:** Check `*.dialog.jsonl` for full Q&A history (safe to ignore if missing)
- **Agent error / hung:** Agent stdout (tool actions) is in `<phase>.log`; agent **stderr** (claude diagnostics, e.g. `stream-json requires --verbose`) is in `<phase>.stderr.log`. The bash polling loop times out after the executor's idle timeout (30 min default).
- **Misplaced question (auto-relocate/normalize):** `relocateMisplacedQuestions` (`pkg/orchestrator/orchestrator.go`, читает Write-события из `<phase>.jsonl`) чинит два способа «спрятать» файл вопроса от поллера, оба ведут к вечному зависанию стадии: **(1) неверная директория** — `*.question.json` записан ВНЕ `$AFM_STAGE_DIR` (баг GLM-4.7: путь из CWD вместо env); **(2) неверный префикс** — файл внутри stageDir, но назван по id стадии (напр. `commit-changes.q1.question.json`) вместо канонической фазы (`planning.q1.question.json`), а `FindUnansweredQuestions` матчит только `planning`/`implementation`/`review`/`autonomous_execution`. В обоих случаях файл нормализуется к каноническому имени `<phase>.<id>.question.json` (правильная фаза берётся из того, в чьём `<phase>.jsonl` найден Write, а не из неверного префикса) + создаётся dangling-симлинк по пути, который опрашивает агент (его директория + его префикс), → канонический `<stageDir>/<phase>.<id>.answer.json`, чтобы bash-polling-loop нашёл ответ. Стадия уходит в `awaiting_user_input`, а не зависает. Первый слой защиты — сам промпт (`pkg/prompts/builder.go`, `<interactive_rules>`) с адресным constraint-ом «префикс — это фаза, а НЕ id стадии». (Прежнее поведение — fail-fast через `detectDialogViolation` — заменено relocate.) На ручном retry `<phase>.session.json` и `<phase>.jsonl` очищаются.

- **Реюз id вопроса после ответа — `pollQuestions`'s `processed`-карта must forget answered keys.** Найдено разбором реальной production-стадии (`agents:[auto]`, ревизионный цикл `goga-brainstorm`): промпт требует "never reuse an ID within a phase", но реальный агент всё равно переиспользовал тот же id (`q2`) для ВТОРОГО, содержательно другого вопроса после того, как первый `q2` был отвечен и агент возобновил работу с фидбеком пользователя (подтверждено байт-в-байт по `autonomous.log`: `Write q2.question.json` → `cat q2.answer.json` → правки по фидбеку → `Write q2.question.json` СНОВА → `rm -f q2.answer.json; while ...`). `processed[stageID|phase|id]` — карта в памяти процесса, живущая всё время поллинг-горутины — раз выставленная в `true`, никогда не сбрасывалась, поэтому второе, реально неотвеченное появление `q2` было НЕВИДИМО поллеру НАВСЕГДА: ни `EvAskUser`, ни записи в `dialog.jsonl`, стадия зависает в `running` с настоящим неотвеченным `question.json` на диске. Перезагрузка страницы не лечит — баг серверный, in-memory, а не во фронтенде; спасал только полный рестарт `afm run` (карта пересоздаётся с нуля). Фикс: `pollQuestions` перед обработкой очередного тика удаляет из `processed` любой ключ этой стадии, которого больше нет в текущем списке неотвеченных (`mcp.FindUnansweredQuestions`) — т.е. как только вопрос реально получил ответ и исчез из неотвеченных, его ключ забывается, и повторное появление ТОГО ЖЕ id как неотвеченного снова триггерит `EvAskUser`. Тест-регрессия: `TestPollQuestions_ReusedIDAfterAnswerAsksAgain`.

- **Malformed `question.json` — torn-read race, не (только) агентская ошибка; 3-слойный retry-автомат вместо мгновенного fallback-стаба.** Разбор второго реального лога: `FindUnansweredQuestions` не смогла распарсить `question.json` даже через `jsonrepair` и показала fallback-стаб пользователю НАВСЕГДА. Байт-в-байт сравнение показало: захваченный в `dialog.jsonl` "битый" preview — точный префикс ПОЗЖЕ валидного, полного содержимого того же файла, ни разу не изменившегося по факту — т.е. поллер прочитал файл, пока `Write`-тул агента ещё не долетел до диска (torn read), а не то что агент реально сломал JSON. Старое поведение усугубляло гонку: `FindUnansweredQuestions` мгновенно ПЕРЕЗАПИСЫВАЛА `qPath` стабом при первом же failed-parse — вторая гонка поверх первой с ещё пишущим агентским процессом.
  - **`QuestionFile.Malformed bool`** (`pkg/mcp/dialog.go`) — `FindUnansweredQuestions` для "не распарсилось даже после repair" теперь НЕ трогает файл на диске вообще (только для этой ветки; "repair сработал" ветка по-прежнему персистит исправленный JSON — не менялось), просто помечает `Malformed: true`. Решение "это гонка или реальная поломка" переехало вызывающему коду.
  - **3-слойный автомат в `pollQuestions`** (`pkg/orchestrator/dialog_poller.go`, только для `interactive`-стадий — non-interactive и так толерантны через `PickAutoAnswer`'s no-options fallback): (1) **grace tick** — первое появление битых байт просто запоминается (`malformedQuestionState.lastRaw`), никаких действий; если это была гонка — на следующем тике (1с) файл уже дописан и парсится нормально, агент вообще не вовлекается; (2) **stable + всё ещё broken** (те же байты на ВТОРОМ тике подряд — запись точно завершена) → `mcp.WriteInternalAnswer` пишет агенту (через тот же файл `<phase>.<id>.answer.json`, что уже читает его bash-loop — новый протокол не нужен) просьбу перечитать и переписать файл корректным JSON под ТЕМ ЖЕ id, с номером попытки; до `maxMalformedRetries` (3) раз; (3) **exhausted** — `giveUpOnMalformedQuestion` персистит НАСТОЯЩИЙ валидный стаб (`Options: nil`, `AllowCustom: true`, сырой текст файла как объяснение) и только теперь запускает обычный `EvAskUser`-путь. Фронтенд уже поддерживает опции-less вопрос без правок (`DialogChannel.tsx`: `(pending.options ?? []).map(...)` на пустом массиве не рисует кнопок, textarea включена по `allow_custom`).
  - **`unblockRewrittenMalformedQuestions`** решает следующую проблему: после nudge-а `WriteInternalAnswer` создаёт `answer.json` — с точки зрения `FindUnansweredQuestions` этот id теперь "отвечен" НАВСЕГДА, даже когда агент честно переписывает ТОТ ЖЕ id новым (возможно, всё ещё битым) содержимым — та же ловушка, что и реюз-id баг выше, но здесь это НЕ ошибка агента (id намеренно тот же — это правка, не новый вопрос), а следствие самого механизма nudge-а. Перед каждым сканом `FindUnansweredQuestions` для interactive-стадии эта функция проверяет: для каждого `retries > 0` ключа изменилось ли содержимое `question.json` с последнего раза — если да, значит агент отреагировал, и устаревший `answer.json` от afm удаляется, снова открывая id для обнаружения в ЭТОМ ЖЕ тике.
  - **Найдено живым ручным прогоном в браузере (не инспекцией кода): починка ломала следующий, совершенно не связанный ответ.** Первая версия `unblockRewrittenMalformedQuestions` удаляла устаревший `answer.json` и НИЧЕГО больше не делала с `malformed[key]` — запись оставалась в карте с исходным `lastRaw` (старые битые байты) НАВСЕГДА, даже после того как агент успешно исправился. На следующем тике та же проверка "текущее содержимое != lastRaw" снова срабатывала (валидный контент, разумеется, навсегда отличается от старых битых байт) и удаляла `answer.json` СНОВА — но к этому моменту по этому пути уже мог появиться СОВЕРШЕННО НЕСВЯЗАННЫЙ настоящий ответ human'а на нормальный, уже починенный вопрос, и он удалялся раньше, чем bash-loop агента успевал его прочитать, вешая стадию. В логах юнит-тестов это не проявлялось (моки не воспроизводили правильную последовательность тиков), обнаружено только реальным прогоном через браузер: пользователь жал "Yes, continue" на восстановленный вопрос, ответ пропадал с диска. Фикс: `mcp.CanParseQuestion(raw)` (новая экспортированная обёртка над той же parse+repair логикой, что и `FindUnansweredQuestions`, без побочной записи на диск) — если переписанный контент теперь валиден, `unblockRewrittenMalformedQuestions` полностью удаляет ключ из `malformed` (дальше id живёт как обычный вопрос, никогда больше не трогается этим механизмом); если всё ещё бит — просто обновляет `lastRaw` на новые байты (не трогая `retries`), давая этой попытке свой grace tick, ровно как и первому наблюдению. Регрессия: `TestPollQuestions_MalformedQuestion_RealAnswerSurvivesAfterRecovery`.
  - **`MalformedNudgeTimeout` — второй бесплатный вывод из починки существующего интеграционного теста `TestIntegration_UnrepairableQuestionFallsBackToStub`.** Первая версия `unblockRewrittenMalformedQuestions` разблокировала id ТОЛЬКО когда видела, что агент реально переписал файл (сравнение байт). Агент, который вообще не отвечает на нудж (упал, проигнорировал, завис) никогда не меняет содержимое — ключ навсегда застревал в состоянии "жду ответа", `maxMalformedRetries` никогда не достигался, и стадия висела в `planning`/`running` вечно — ровно тот же класс бага, который вся эта фича должна была устранить, просто с другим триггером. `MalformedNudgeTimeout` (package var, тот же паттерн переопределения в тестах, что `RetryBackoff`/`MaxRetries` в retry.go — снапшотится в `Orchestrator.malformedNudgeTimeout` при `New()`) добавляет вторую причину разблокировки: `time.Since(st.nudgedAt) >= timeout`, независимо от того, поменялось ли содержимое. Найдено НЕ в браузере, а при обновлении старого теста под новое поведение — его мок-скрипт (`printf ...; sleep 30`) пишет вопрос ровно один раз и ничего больше не делает, что оказалось точным воспроизведением этого класса агента. Живой прогон в браузере (`silent-agent.sh`, третий сценарий, отдельный от `recovery`/`exhausted`) подтвердил то же самое с реальным `afm run` и настоящим таймером (дефолт 10s): все 3 попытки видны в event feed с интервалом ~10с, затем `awaiting_user_input`. Регрессия: `TestPollQuestions_MalformedQuestion_UnresponsiveAgentEventuallyExhausts`.
  - **`mcp.WriteInternalAnswer`** — новая функция (выделена рефакторингом `WriteAnswer` → общий `writeAnswerFile` + два тонких публичных врапера), пишет `answer.json` БЕЗ побочного эффекта в `dialog.jsonl`. Nudge — обмен между afm и агентом, не человеком; попадание в `dialog.jsonl` дало бы во время real-time наблюдаемого interactive-диалога болтающийся `.qa`-пузырь "→ <nudge-текст>" без вопроса (`renderHistory` в `DialogChannel.tsx` рендерит ЛЮБУЮ запись с `answer != null`, даже без текста вопроса) — ложный сигнал прямо во время попытки НЕ трогать пользователя.
  - Тесты: `TestFindUnansweredQuestions_UnrepairableJSON_MarkedMalformed` (файл на диске не тронут), `TestPollQuestions_MalformedQuestion_{GraceTickHidesFromUser,ResolvesSilentlyIfWriteCompletes,StableSendsNudge,AgentRewriteUnblocksRetry}`, `TestHandleMalformedQuestion_ExhaustedShowsRawTextNoOptions`, `TestDialogGet_SkipsMalformedPendingQuestion` (guarantee-visibility fallback в `handlers.go` тоже фильтрует `Malformed`, иначе тот же путь слил бы недорешённое состояние в обход автомата), `TestWriteInternalAnswer_WritesFileWithoutDialogEntry`.

- **Разбор третьего реального лога вскрыл архитектурный паттерн, а не единичный баг: "завершение стадии" решалось в НЕСКОЛЬКИХ независимых местах вместо одного.** Стадия `brainstorm` в проде дошла до 15/15 вопросов, агент честно записал `execution_summary.md` и корректно завершил процесс (`exit 0`) — но стадия НАВСЕГДА зависла в `awaiting_user_input`, потому что где-то РАНЬШЕ в её жизни (реюз id, см. выше) остался один-единственный брошенный, никогда не отвечаемый `question.json`. `hasOpenQuestion()` этого не различает: "вопрос ещё актуален" и "вопрос — многочасовой хвост, на который никто уже не ответит" для него неотличимы.
  - **`runWithRetry`'s гейт открытого вопроса** (retry.go) при `err == nil` от агента различает "протухший" вопрос от "живого" по ТЕКУЩЕМУ статусу FSM в момент возврата агента: если стадия ещё НЕ в `AwaitingUserInput` (вопрос только что создан этим же вызовом, вживую), держим стадию через `EvAskUser` как раньше — completion не рассматривается вообще, чтобы не гонять реально ожидающего ответа пользователя. Если стадия УЖЕ в `AwaitingUserInput` (независимый поллер вопросов успел перевести её туда, пока агент ещё выполнялся — обогнал его собственный выход), вопрос считается протухшим хвостом, и решает `completionCheck()`: удовлетворён — публикуем `EventAgentCompleted`, нет — просто возвращаемся, ничего не публикуя. Регрессия: `TestRunWithRetry_CompletionMarkerOverridesStaleOpenQuestion` (явно переводит стадию в `AwaitingUserInput` через `EvAskUser` до вызова `runWithRetry`, воспроизводя гонку по-настоящему, а не только по наличию файлов на диске).
  - **`onAgentCompleted` (orchestrator.go) имеет СВОЮ, БЕЗ гейта на `s.Interactive`/`phase`, проверку `hasOpenQuestion`** — первая версия этой чистки посчитала её чистым дубликатом проверки в `runWithRetry` и удалила целиком; последующий разбор регрессии `TestIntegration_PlanningWithOpenQuestionWaits` (неинтерактивная planning-стадия с фейковым первым открытым вопросом внезапно доезжала сразу до `done`, минуя `awaiting_user_input`) показал, что это НЕ дубликат — у проверки в `runWithRetry` есть гейт `(s.Interactive || phase == phaseAutonomous)`, специально чтобы не трогать FSM для неинтерактивных стадий (это работа автоответчика, см. "Авто-ответ на вопросы" ниже); у проверки в `onAgentCompleted` такого гейта никогда не было — она ловит фазы/стадии, которые гейт `runWithRetry` пропускает мимо (напр. неинтерактивную planning-стадию, чей агент синхронно пишет вопрос и сразу же завершается, не дожидаясь поллера). Восстановлена с ТОЙ ЖЕ логикой "протух/жив", что и в `runWithRetry`: пропускается (не держит стадию), если текущий статус УЖЕ `AwaitingUserInput` — та же гонка "поллер обогнал агента", тот же протухший хвост.
  - **`completeStage`'s precondition и FSM-правила `EvComplete`/`EvPlanReady` не признавали `AwaitingUserInput` валидным источником перехода.** Даже после исправления двух гейтов выше, если поллер вопросов УСПЕВАЕТ независимо перевести стадию в `AwaitingUserInput` (из-за того же брошенного вопроса) РАНЬШЕ, чем агентский процесс успевает вернуть `nil` — то есть DURING один и тот же вызов `runWithRetry`, конкурентно с поллинг-горутиной — то к моменту, когда `runWithRetry` публикует `EventAgentCompleted`, текущий статус УЖЕ `AwaitingUserInput`, а не `Running`. `completeStage` и `EvComplete`/`EvPlanReady` признавали источником перехода только `Running`/`Planning`/`Retrying` — молча отбрасывали переход, стадия зависала НАВСЕГДА (агентского процесса уже нет, чтобы попробовать снова). Оба правила расширены до `AwaitingUserInput`. Найдено и подтверждено живым браузерным тестом (`stale-question-verify` флоу) и двумя регрессионными интеграционными тестами: `TestIntegration_PollerRaceDoesNotStrandCompletedStage` (autonomous/implementation) и `TestIntegration_PollerRaceDoesNotStrandPlanningStage` (planning — тот же паттерн, найден аудитом по аналогии, не отдельным живым инцидентом).
  - **Общий вывод для будущих похожих гонок**: если какой-то facts about "готова ли стадия" вычисляется через файловую систему (`FindUnansweredQuestions`, `Check*Completion`), а РЕШЕНИЕ на основе этого factsа принимается больше чем в одном месте — эти места ГАРАНТИРОВАННО разойдутся при следующей правке одного из них. Единственная надёжная защита — один canonical decision point (здесь: `runWithRetry`), все остальные потребители его РЕЗУЛЬТАТА (события), а не пересчитывают тот же факт заново. Плюс: FSM-таблица переходов должна перечислять ВСЕ статусы, до которых конкурентный поллер реально может успеть добежать, а не только "нормальный", последовательный путь — `EvComplete`/`EvPlanReady`'s `From`-списки моделировали синхронный мир, где поллер не мог обогнать текущий агентский вызов.

### Polling Latency

- Orchestrator polls every **1 second** for new questions
- Answer detection is immediate (bash loop checks file existence continuously)
- UI dashboard polls `/api/stages/<id>/dialog` every ~2 seconds
- Total latency: question visible in UI within ~2-3 seconds of agent writing it

### Common Changes

When adding new interactive features:
1. Ensure agent writes `<phase>.<id>.question.json` in correct format
2. Ensure agent polls correctly: `while [ ! -f "$AFM_STAGE_DIR/<phase>.<id>.answer.json" ]; do sleep 30; done`
3. Update handler validation in `pkg/server/handlers.go` if phase names change
4. Add integration tests. Note: interactive stages (`stage.Interactive=true`) **ignore** the injected `Runner` — `runnerFor` always builds a real `executor.New(...)` driven by `stage.Command`. So interactive tests run a real bash script via `stage.Command` (see `TestFullDialogCycle`, `TestIntegration_MisplacedQuestionRelocated`), not `eagerProbeRunner` (which only applies to non-interactive stages)
5. Verify atomic write pattern (O_EXCL) is preserved in handlers

### Авто-ответ на вопросы в non-interactive стадиях

Скилл/агент может использовать файловый диалоговый протокол независимо от того, помечена ли стадия `interactive: true` — просто потому что скилл сам всегда так делает, когда ему нужно уточнение. Раньше это вешало non-interactive стадию навечно (вопрос никто не отвечал) или ронял её («missing artifact or incomplete»). Теперь afm отвечает сам.

- **`AFM_STAGE_DIR` — для всех стадий.** `runner_factory.go`'s `runnerFor` больше не гейтит `StageDir` условием `phase == phaseAutonomous` — он выставляется для ЛЮБОЙ non-interactive стадии, иначе агенту физически некуда писать `question.json`.
- **`pollQuestions` ветвится по `stage.Interactive`.** Для `!stage.Interactive` (default, включая `agents: [auto]` — единственное исключение — явный `interactive: true`) стадия НЕ уходит в `awaiting_user_input`: `mcp.PickAutoAnswer` выбирает ответ (маркер `(recommended)`/`(default)`/`(рекомендую)`/`(рекомендуется)`/`(по умолчанию)`, case-insensitive substring, first-option-with-any-marker wins — маркер ищется в порядке options, не в порядке маркеров; без options — фиксированный текст «Прими самое релевантное решение автономно или предложи варианты ответов»), `mcp.WriteAnswer` атомарно (O_EXCL) пишет `answer.json` + best-effort дописывает `dialog.jsonl` с меткой `AutoAnswered: true` (единая точка записи, переиспользуется и HTTP-хендлером `handleDialogAnswer` с `autoAnswered=false`, и поллером — не дублируется). FSM стадии не трогается вообще (никакого `EvAskUser`/`Trigger`).
- **`bus.EventAutoAnswered` — живой ПЛЮС notices.jsonl.** Это не FSM-переход, поэтому в `events.jsonl` (durable-лог) его нет. Публикуется живьём через `o.ui.Publish` — но, как и `EventAgentCompleted`/`EventContextWarning`/`EventScriptOutput`, ОБЯЗАН дублироваться через `stagefiles.AppendNotice(runDir, stageID, ..., data)` в run-level `notices.jsonl`: иначе клиент, подключившийся или перезагрузивший страницу ПОСЛЕ авто-ответа, никогда не увидит эту строку в event feed (`/api/events`'s `reconstructNotices` реплеит именно из `notices.jsonl`, а не из `events.jsonl`). Забыть про `AppendNotice` для нового non-FSM UI-события — легко воспроизводимый баг, есть готовый regression-тест на этот класс ошибки: `TestExecScript_PersistsOutputToNotices`.
- **Дашборд: панель диалога гейтится по факту наличия истории, не только по типу стадии.** `App.tsx`'s `showDialog` раньше был `interactive || autonomous` — стадия могла иметь настоящую diалоговую историю (авто-отвеченный вопрос) и всё равно не показывать панель `DialogChannel`, потому что панель для НЕЁ вообще не монтировалась. Добавлен третий сигнал `stage_has_dialog` (`/api/status`, тот же файл-presence паттерн, что и `stage_autonomous`/`autonomous.flag` — проверка существования любого `<phase>.dialog.jsonl` в директории стадии), `showDialog = interactive || autonomous || hasDialog`. `DialogChannel.tsx`'s собственный внутренний гейт `hasContent` тоже учитывает `stage.hasDialog` (не только `entries.length > 0`) — иначе тестовый мок с пустым `/dialog`-фетчем маскирует баг, а в реальности есть окно, где `/api/status` уже знает про диалог, а собственный поллинг панели ещё не подтянул `entries`.
- **`AutoAnswered bool`** — новое поле на `mcp.Answer`/`mcp.Entry` (json: `auto_answered`), НЕ строковый `role` — сценарий бинарный (человек/afm), enum из двух значений избыточен. Прокинуто через `dialogUIEntry`/`questionUIEntry` (`pkg/server/handlers.go`) в GET `/dialog`. Фронтенд (`DialogChannel.tsx`) рисует отдельным классом `qa-auto` + бейдж `⚙` (`title="Answered automatically by afm"`) на отвеченных записях истории с этим флагом.
- Спек/план: `docs/superpowers/specs/2026-08-07-non-interactive-auto-answer-design.md`, `docs/superpowers/plans/2026-08-07-non-interactive-auto-answer.md`.

## Флейк "storage failure: write events.jsonl: invalid argument" — process-group kill, не OS-квирк

Долго выглядел как случайная нестабильность окружения при `go test ./pkg/orchestrator/...` — падал на РАЗНЫХ, не связанных друг с другом тестах, с ошибкой `write events.jsonl: invalid argument`, похожей на настоящий OS-level EINVAL. Настоящая причина — комбинация двух багов, один в тестовой инфраструктуре, один в проде.

- **Текст ошибки маскировал источник.** `(*os.File)` в Go nil-safe: вызов метода (`Write`) на `nil`-получателе не паникует, а тихо возвращает `fs.ErrInvalid`, чей `.Error()` — буквально `"invalid argument"`, неотличимо на глаз от настоящего `syscall.EINVAL`. `state.Store.Apply` вызывает `s.eventsLog.Write(data)`; если `Store.Close()` успел обнулить `s.eventsLog` РАНЬШЕ, чем агентская горутина, всё ещё пишущая в стор, вызвала `Apply` — ошибка выглядит как загадочный OS-флейк, а не как «мы закрыли файл раньше времени».
- **Первопричина №1 (тестовая инфраструктура, ~35 мест в 11 файлах пакета `pkg/orchestrator`): `go func() { _ = orch.Run(ctx) }()` без ожидания завершения.** Тесты запускали `Run` в горутине fire-and-forget, дожидались нужного СТАТУСА стадии (`waitForStatus`/подписка на события), затем возвращались — а `t.Cleanup(func(){ store.Close() })` срабатывал, не дожидаясь, пока сама горутина `Run` реально вернётся. `Run`'s собственная гарантия чистого шатдауна (`defer o.concurrency.WaitAgents()` — см. "Чистый shutdown" выше) НЕ была задействована, потому что тест никогда не ждал возврата `Run()`, только терминального вида статуса — а статус может выглядеть терминальным ДО того, как агентская горутина, продолжающая писать в стор (напр. `EvFail` на отмену контекста), реально закончила. Фикс: единый тестовый хелпер `runOrchestratorAsync` (`pkg/orchestrator/testrun_helper_test.go`) — стартует `Run` в горутине и регистрирует `t.Cleanup`, который `cancel()`ит контекст и ЖДЁТ канал `done` (закрывается после возврата `Run`), с честным `t.Error` через 10с вместо молчаливой сдачи. Порядок важен: `t.Cleanup`-колбэки выполняются LIFO, так что регистрация ожидания ПОЗЖЕ регистрации `store.Close()` (которая тоже обязана быть через `t.Cleanup`, не голый `defer` — голый `defer` сработал бы ДО любых `t.Cleanup`, сведя фикс на нет) гарантирует, что ожидание отработает раньше закрытия стора.
- **Первопричина №2 (настоящий прод-баг, `pkg/executor/executor.go`): `cmd.Process.Kill()` убивает только прямого потомка, не всю группу процессов.** Если `stage.Command`-скрипт порождает grandchild-процесс, не заменяя себя им через `exec` (например, скрипт из нескольких строк, последняя из которых — `sleep 30`), тот grandchild наследует stdout-pipe, созданный `cmd.StdoutPipe()`. При отмене контекста/idle-таймауте/просроченном grace-периоде SIGINT executor убивал только скрипт-обёртку (прямого потомка) — осиротевший grandchild продолжал жить и держать pipe открытым, так что `lineReader`, читающий из этого pipe, никогда не видел EOF, и `<-done` в `executor.run` блокировался на весь ОСТАТОК жизни сироты (наблюдалось как задержки 11–31с — ровно длина `sleep N` в тестовых скриптах). Фикс: `killProcessGroup(cmd, sig)` — `syscall.Kill(-cmd.Process.Pid, sig)` (отрицательный PID = вся группа процессов), плюс `cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}` перед `Start()`, чтобы скрипт стал лидером СВОЕЙ группы (иначе `-pid` резолвился бы в группу самого afm, что задело бы посторонние процессы). Применено во всех точках убийства процесса в `run()`: idle-таймаут, отмена контекста, оба шага SIGINT-прерывания (мягкий сигнал и принудительный fallback).
- Это реальный, не только тестовый баг: при отмене `afm run` (Ctrl+C, `Pause()`, обычный shutdown) с `stage.Command`-скриптом, порождающим неexec'нутых потомков, "Чистый shutdown"'s `waitAgents()` (bounded 10s, см. выше) мог упереться в свой таймаут, оставляя такую горутину живой ДОЛЬШЕ заявленных 10 секунд — сам `waitAgents()` от этого не завис бы (у него свой bound), но агентская горутина технически продолжала бы работать в фоне после того, как `Run()` уже вернулся.
- Найдено НЕ по логам прод-инцидента, а систематическим расследованием собственного флейка сессии (см. `superpowers:systematic-debugging`) — первая гипотеза (OS/sandbox-квирк) была отвергнута изоляцией через `git stash` (флейк воспроизводился и на чистом дереве) и стресс-тестом с искусственной параллельной нагрузкой (частота флейка росла вместе с нагрузкой на машину — типичный признак настоящей гонки, не случайного EINVAL).

## Docker Mode

afm умеет автоматически перезапускать себя внутри Docker при включённом Docker-режиме.

### Включение

Через конфиг (`.afm/config.yaml` или `~/.afm/config.yaml`):
```yaml
docker:
  enabled: true
  image: akopichin/afm:latest   # опционально, это дефолт
```

Или через переменную окружения:
```bash
AFM_USE_DOCKER=1 afm run flow.yaml
```

### Что монтируется автоматически

| Хост | Контейнер | Назначение |
|------|-----------|------------|
| `$(pwd)` (абсолютный путь) | тот же путь | Проект + `.afm/` (runs, flows, config) |
| `~/.claude/` | `/home/afm/.claude` | Auth, skills, память (= `$HOME/.claude` в контейнере) |
| `~/.afm/` | `/home/afm/.afm` | Глобальный конфиг afm |
| Нестандартные агенты из flow | `/usr/local/bin/<cmd>` (`:ro`) | Кастомные команды |
| `docker.extra_mounts` | `~`-пути → `/home/afm/…`, прочие — тот же путь (`:ro`) | Токены/конфиги кастомных агентов (напр. `~/.ai-free`) |

`~/.claude.json` намеренно **НЕ** монтируется — claude создаёт свежий container-local конфиг (`/home/afm/.claude.json`). Попытка примонтировать его `:ro` приводила к падению (`corrupted: JSON Parse error`), т.к. claude обновляет файл атомарным rename.

**Auth для `command: claude` в Docker:** macOS хранит OAuth-токены в Keychain (`Claude Safe Storage`), который недоступен из Linux-контейнера. Поэтому `claude` внутри Docker пишет `not logged in`. Решение — передать токен через env var:
1. Сгенерировать долгоживущий токен: `claude setup-token` → сохранить в `~/.zshrc` как `export CLAUDE_CODE_OAUTH_TOKEN=sk-ant-si-...`
2. Launcher автоматически прокинет `CLAUDE_CODE_OAUTH_TOKEN` в контейнер (если задан в env).
   Также поддерживается `ANTHROPIC_API_KEY` (API-ключ) и `ANTHROPIC_AUTH_TOKEN`.

**Dashboard:** порт из `server.port` пробрасывается на хост через `-p <port>:<port>`, иначе UI недоступен снаружи контейнера. **Браузер:** по умолчанию (`server.open_browser` отсутствует/`false`) НЕ открывается — в лог печатается URL дашборда с подсказкой `→ open this URL in your browser to follow the run`. При `server.open_browser: true` браузер открывает хост-side opener: afm внутри Linux-контейнера сам открыть браузер на macOS-хосте не может (`runtime.GOOS=linux` → `xdg-open` без display), поэтому отдельный процесс-помощник запускается на хосте ДО re-exec, опрашивает проброшенный порт и зовёт `open`/`xdg-open`. Внутри контейнера вызов `openBrowser` пропускается (`AFM_IN_DOCKER=1`).

**Привилегии (важно):** контейнер стартует под root, но entrypoint (`docker-entrypoint.sh` + `gosu`) сразу дропает привилегии до хостового uid/gid (`AFM_HOST_UID/GID`, передаются из `os.Getuid/Getgid`) и выставляет `HOME=/home/afm`. Поэтому afm и агенты работают под тем же пользователем, что и на хосте — все записи в `~/.claude`, `~/.afm`, каталог проекта и `extra_mounts` принадлежат пользователю хоста, а не root (нет root-owned файлов и конфликтов с правами у хостового claude). Под non-root claude разрешает `--dangerously-skip-permissions` без `IS_SANDBOX`.

### Environment Variables

| Переменная | Назначение |
|-----------|------------|
| `AFM_USE_DOCKER=1` | Включить Docker mode без правки конфига |
| `AFM_IN_DOCKER=1` | Выставляется внутри контейнера — предотвращает рекурсию (не трогать) |
| `AFM_HOST_UID` / `AFM_HOST_GID` | Передаются внутрь; entrypoint дропает root до этого uid/gid (`gosu`), чтобы записи в тома принадлежали пользователю хоста |
| `AFM_DOCKER_IMAGE` | Переопределить образ (например, для локальной сборки) |
| `AFM_DEBUG` | Пробрасывается в контейнер значением (`-e AFM_DEBUG=…`, не секрет), чтобы re-exec внутри тоже логировал вход агента; на хосте выставляется флагом `--debug` в `PersistentPreRunE` |
| `ANTHROPIC_API_KEY` | Пробрасывается в bare-форме `-e KEY` (без значения — не светится в `ps aux`/history) |
| `ANTHROPIC_AUTH_TOKEN` | То же самое |
| `ANTHROPIC_BASE_URL` | То же самое |
| `CLAUDE_CODE_OAUTH_TOKEN` | Долгоживущий OAuth-токен для `command: claude` (генерируется через `claude setup-token`) |

### Публикация нового образа

Версионированный релиз (SemVer, авто-бамп) — пушит иммутабельный `akopichin/afm:vX.Y.Z` и rolling `:latest`:

```bash
make release-patch   # v1.2.3 → v1.2.4  (bugfix)
make release-minor   # v1.2.3 → v1.3.0  (новая фича, обратная совместимость)
make release-major   # v1.2.3 → v2.0.0  (breaking change)
```

`scripts/release.sh` читает последний SemVer git-тег, бампит уровень и
**только** создаёт+пушит новый git-тег (`git push origin vX.Y.Z`) — сама
сборка (docker-образ, бинарники, GitHub Release, Homebrew cask) в скрипте
больше не происходит. Пуш тега триггерит `.github/workflows/release.yml`,
который и делает всю фактическую работу. Дополнительно: любой push в
`main` автоматически бампает и пушит следующий patch-тег через
`.github/workflows/ci.yml` (`auto-release-tag` job) — `make release-patch`
вручную нужен редко, в основном для minor/major.

**Релиз всегда мультиарх (`linux/amd64` + `linux/arm64`).**
`release.yml` собирает и пушит через `docker buildx build --platform
linux/amd64,linux/arm64 --push` одним шагом (раздельный `docker push` для
манифест-листа не годится — образы не грузятся в локальный daemon), с
предварительной регистрацией QEMU (`docker/setup-qemu-action`) для
эмуляции arm64 на amd64-раннере GitHub Actions. Версия вшита в бинарник
через `--build-arg AFM_VERSION`: `docker run akopichin/afm:vX.Y.Z afm
--version` покажет тег.

`make docker-build`/`docker-push` — dev-only, локальная сборка **single-arch**
(быстрая итерация без релиза). Для реального релиза (мультиарх + бинарники +
Homebrew) достаточно запушить тег `vX.Y.Z` (вручную через `make release-*`
или автоматически при push в `main`) — остальное берёт на себя CI.

### Версия claude-code CLI в образе — запинена, бампать вручную после теста

`Dockerfile.runtime`'s `ARG CLAUDE_CODE_VERSION` фиксирует версию
`@anthropic-ai/claude-code`, устанавливаемую внутри образа. Раньше строка
была `npm install -g @anthropic-ai/claude-code` без версии — каждая
пересборка молча подтягивала актуальный на тот момент npm-релиз, и разные
собранные теги (`v0.5.x`, собранные в разные дни) могли гонять флоу на
разных версиях CLI с разным поведением агентского цикла (например,
модель решает не продолжать ход без явного tool-call иначе, чем раньше) —
разница необнаружима по коду afm, только по факту разного рантайм-поведения
одного и того же флоу между релизами.

**Правило бампа:** НЕ обновлять `CLAUDE_CODE_VERSION` вслепую на каждый
npm-релиз claude-code. Обновлять только когда новая версия уже
протестирована вручную (`AFM_USE_DOCKER=1 AFM_DOCKER_IMAGE=...` или
`make docker-build` + ручной прогон флоу) в связке с готовящимся к тегу
релизом afm — и только тогда включать бамп в тот же релизный коммит.
Актуальная опубликованная версия: `npm view @anthropic-ai/claude-code version`.

### Отладка

```bash
# Посмотреть что именно будет запущено
AFM_USE_DOCKER=1 AFM_DOCKER_IMAGE=local/afm:dev afm run flow.yaml

# Войти в контейнер вручную (привилегии дропаются до твоего uid через entrypoint)
docker run --rm -it \
  -v $(pwd):/project \
  -v ~/.claude:/home/afm/.claude \
  -v ~/.afm:/home/afm/.afm \
  -e AFM_HOST_UID=$(id -u) -e AFM_HOST_GID=$(id -g) \
  akopichin/afm:latest bash
```

### Нестандартные агенты (не claude)

Если в flow прописан `command: glm51` (или другой не-claude бинарник), afm автоматически:
1. Находит бинарник через `which glm51`
2. Монтирует его в контейнер: `-v /path/to/glm51:/usr/local/bin/glm51:ro`

Бинарники, не найденные в PATH на хосте, молча пропускаются.

Ограничения:
- В контейнер монтируется только сам файл бинарника/скрипта агента (`:ro`). Если скрипт-обёртка вызывает сторонние зависимости (node/python/скрипты-сиблинги/файлы вроде `~/.glmrc`), они не перенесутся — используйте агентов, чьи зависимости уже есть в образе.
- `command` в flow должен быть именем из `PATH` (базовым именем), а не абсолютным путём: монтируется только `filepath.Base(cmd)`, и внутри контейнера искался бы абсолютный путь хоста.
- Если скрипт-агент читает свои токены/конфиги из дома (напр. GLM-обёртки `glm51`/`glm52`/`ai-free.claude-glm` — из `~/.ai-free/claude-glm/`), добавьте эту директорию в `docker.extra_mounts`, иначе агент упадёт с "файл не найден".

### autoShim: генерируемые врапперы без монтирования

По `docker.autoShim: true` afm генерирует claude-совместимые врапперы для агентов,
описанных в `docker.agents.<cmd>` (recipe: `model`/`url`/`system_prompt`/`auth`),
прямо в контейнере — без `-v` монтирования хост-бинарника и без `extra_mounts`
для токенов. Секрет и контент system_prompt читаются на хосте и передаются в
контейнер как transient env (`AFM_SECRET_<CMD>`, `AFM_SYSPROMPT_<CMD>`); `url`/`model`
контейнер берёт из смонтированного `config.yaml`.

```yaml
docker:
  autoShim: true
  agents:
    glm51:
      model: glm-5.1
      url: https://api.z.ai/api/anthropic
      auth: { from: "file:~/.ai-free/claude-glm/token", to: "env:ANTHROPIC_AUTH_TOKEN" }
```

- `auth.to` ∈ {`env:CLAUDE_CODE_OAUTH_TOKEN`, `env:ANTHROPIC_API_KEY`, `env:ANTHROPIC_AUTH_TOKEN`}.
- Без recipe (при `autoShim: true`) команда монтируется `:ro` как раньше.
- `url` bake'ится в враппер как `ANTHROPIC_BASE_URL` (z.ai, deepseek — напрямую, без прокси).
- См. спек `docs/superpowers/specs/2026-07-14-docker-autoshim-design.md`.

#### Тип `openai`: OpenAI-совместимые провайдеры

Для провайдеров с **настоящим** API, совместимым с OpenAI (`v1/chat/completions`), укажи `type: openai`.
Сгенерированный враппер использует `/usr/local/bin/openai-as-claude` вместо claude:

```yaml
docker:
  autoShim: true
  agents:
    deepseek:
      type: openai
      model: deepseek-chat
      url: https://api.deepseek.com/v1
      auth:
        from: env:DEEPSEEK_KEY        # секрет на хосте
        to: env:OPENAI_API_KEY        # не ограничен ClaudeAuthEnvVars
```

Поддерживаемые провайдеры: DeepSeek (`api.deepseek.com`), OpenAI, локальные Ollama/любые
эндпоинты с `POST /v1/chat/completions` (в т.ч. SSE-стриминг). **Важно:** Cursor сюда
НЕ относится — см. ниже `type: cursor`; IdeaLab тоже НЕ относится — этому провайдеру
нужен реальный tool-loop, см. ниже `type: openai-agent`.

Поддерживает мультимодальные `[Screenshot: <path>]`-вставки из дашборда так
же, как `openai-agent` (см. ниже) — единственный доступный здесь путь
доставки маркера: сам начальный prompt, скрипт не крутит цикл и не читает
диалоговые ответы сам.

Требования в образе: `jq`, `curl` (оба присутствуют в `Dockerfile.runtime`).

#### Тип `openai-agent`: OpenAI-совместимые провайдеры с реальным tool-loop

`type: openai` (выше) даёт модели только текст — годится для planning/review
стадий, но не годится для `agents: [auto]`/`interactive: true` стадий, которым
нужно реально писать файлы, гонять скрипты и отвечать на диалоговые вопросы.
`type: openai-agent` — для провайдеров, у которых `/chat/completions`
поддерживает настоящий OpenAI-style function calling (`tools`/`tool_choice`,
включая потоковые `tool_calls` со стандартной index-адресацией фрагментов).
Сгенерированный враппер использует `/usr/local/bin/openai-agent-as-claude`:

```yaml
docker:
  autoShim: true
  agents:
    idealab:
      type: openai-agent
      model: qwen3-max
      url: https://idealab.alibaba-inc.com/api/openai/v1
      max_turns: 40          # опционально; дефолт скрипта — 40
      auth:
        from: "file:~/.ai-free/claude-glm/token-idealab"
        to: "env:OPENAI_API_KEY"
    balian:
      # Balian/DashScope (Alibaba Cloud "百炼" Model Studio) — тот же
      # compatible-mode /chat/completions, тот же streaming tool_calls формат.
      # model: доступность моделей зависит от ключа — на проверенном ключе
      # работают только qwen-plus и qwen3.5/3.6/3.7-plus; qwen3.8-max/qwen3-max/
      # qwen-max/qwen-turbo/qwen3-coder-* дают Model.AccessDenied. qwen3.7-plus
      # думает по умолчанию (300+ reasoning-токенов даже на тривиальный ответ,
      # adapter reasoning_content не читает — просто лишние токены/задержка);
      # qwen-plus того же провайдера отвечает без thinking-режима.
      type: openai-agent
      model: qwen3.7-plus
      url: https://dashscope.aliyuncs.com/compatible-mode/v1
      auth:
        from: "file:~/.ai-free/claude-glm/token-balian"
        to: "env:OPENAI_API_KEY"
```

Модели даётся ровно один инструмент — `bash` (команда → stdout+stderr+exit
code). Никаких отдельных read/write/skill-инструментов: чтение и запись
файлов, запуск `./scripts/*.sh`, поллинг диалоговых файлов
(`<phase>.<id>.answer.json`) — всё это модель делает сама через `bash`,
ровно как обычный shell-скрипт делал бы. Skill-конвенция (`<skills>name</skills>`
в промпте, см. раздел "File-Based Dialog Protocol" выше) не поддержана нативно
у стороннего провайдера — системный промпт адаптера явно учит модель при
упоминании skill'а самой прочитать `.claude/skills/<name>/SKILL.md` через `bash`.

Каждый tool-вызов сразу печатается в stdout как
`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"..."}}]}}`
— та же форма, что и настоящий Claude `Bash`-tool_use, поэтому дашборд
показывает живой action feed, а не тишину до самого конца стадии; это же
сбрасывает 30-минутный `idle_timeout` между ходами. `max_turns` (дефолт 40)
ограничивает число обращений к API за одну стадию; при достижении лимита
скрипт завершается штатно (exit 0) с пометкой в тексте — afm обрабатывает
это как обычный незавершённый autonomous-прогон (нет `execution_summary.md` →
retry), а не как отдельную ошибку. Сбой самого запроса к API (сеть, не-2xx) —
это `exit 1`, стадия падает сразу, в отличие от `openai-as-claude.sh`
(который на сбое `curl` проглатывает ошибку в пустой success — там это
безопасно для одноразового текста, здесь тихий "успех" замаскировал бы
реально незавершённый tool-loop).

Известное (не новое) ограничение: если модель зависает на диалоговом поллинге
дольше 30 минут (человек долго не отвечает), сработает тот же `idle_timeout`,
что уже документирован для файлового диалогового протокола выше — это
свойство самого механизма, не специфика этого типа.

**Мультимодальные скриншоты (`[Screenshot: <path>]`).** Вставленный в дашборде
скриншот (см. "paste a clipboard screenshot" в release notes) доходит до
`openai`/`openai-agent` не как путь-который-надо-прочитать самому, а как
настоящая картинка: адаптер сам находит маркер, base64-кодирует файл и
подставляет `image_url`-блок вместо/вместе с текстом — работает, только если
сконфигурированная модель реально мультимодальна (отдельного `vision:`-флага
в рецепте нет, шим просто всегда пытается встроить найденную картинку). Для
`type: openai-agent` это покрывает оба пути, которыми маркер может дойти до
агента: и начальный prompt (revise/заметка), и текст, который модель сама
вычитывает через `bash cat` ответа на диалоговый вопрос внутри цикла — второй
случай подставляется отдельным user-сообщением сразу за tool-результатом, а
не в сам tool-результат (мультимодальный `tool`-content не гарантированно
поддержан всеми провайдерами).

Требования в образе: `jq`, `curl` (оба уже есть в `Dockerfile.runtime`).

#### Тип `cursor`: Cursor Cloud Agents API

Cursor Cloud API (`api.cursor.com`) **не имеет** синхронного `v1/chat/completions` (ответ 404) —
это **Cloud Agents API**: асинхронный run-based API, где чат = запуск облачного код-агента.
Поэтому для Cursor используется отдельный тип и адаптер `cursor-as-claude`:

```yaml
docker:
  autoShim: true
  agents:
    cursor:
      type: cursor
      model: auto                    # auto/пусто → Cursor default; иначе model.id из GET /v1/models
      url: https://api.cursor.com/v1
      auth:
        from: "file:~/.ai-free/claude-glm/token-cursor"   # секрет на хосте (CRSR_…)
        to: env:CURSOR_API_KEY         # любой env:VAR; CURSOR_API_KEY по конвенции
```

Адаптер `cursor-as-claude`: создаёт no-repo Cloud Agent (`POST /v1/agents`, `mode:"agent"`),
опрашивает run до терминального статуса, эмитит claude stream-json с `result`-текстом и
архивирует агента (чтобы не плодить мусор). `system_prompt` для cursor **не используется**
(адаптер его не передаёт).

Особенность: первый ответ ~30–90с (старт cloud-VM при создании агента); далее run быстрый.
Токен — user API key из Cursor Dashboard → API Keys (префикс `crsr_`). Требования в образе: `jq`, `curl`.

#### Тип `codex`: OpenAI Codex CLI (ChatGPT-plan OAuth, без секрета в конфиге)

В отличие от `claude`/`openai`/`cursor`, у `codex` **нет** `AFM_SECRET_<CMD>`-модели авторизации:
`auth` в рецепте необязателен (`AgentRecipe.Validate()` — единственное исключение из трёх типов,
где отсутствие `Auth` не ошибка). Авторизация идёт через ChatGPT-plan OAuth-состояние `~/.codex`
на хосте: `docker.ReExec` монтирует его `:ro` во временный путь контейнера **только если** флоу
реально использует codex (`docker.UsesCodex` — команда `codex-as-claude` напрямую или recipe
`type: codex` где-то в `docker.agents`), а `docker-entrypoint.sh`, ещё под root до `gosu`,
копирует смонтированное в `$HOME/.codex` (уже writable) — codex может обновлять `auth.json`
(refresh token) внутри контейнера, не трогая хостовый файл; контейнер эфемерный, апдейт токена
не переживает пересоздание.

```yaml
docker:
  autoShim: true
  agents:
    codex:
      type: codex
      model: gpt-5-codex          # опционально; "" / "default" → CODEX_MODEL не выставляется,
                                   # решает сам codex / ~/.codex/config.toml
      # auth: не указывается — авторизация через смонтированный ~/.codex
```

Сгенерированный враппер резолвит абсолютный путь к реальному бинарнику `codex` **до** того как
директория враппера (где лежит одноимённый файл `codex`) попадёт в `PATH` — иначе адаптер
`codex-as-claude` (сам вызывающий голый `codex`) поймал бы через PATH самого себя и ушёл
в рекурсию; резолвленный путь передаётся адаптеру через `CODEX_BIN`. Адаптер (`scripts/codex-as-claude.sh`)
запускает `codex exec --json --dangerously-bypass-approvals-and-sandbox` (сэндбокс контейнера и
так изолирован), накапливает `agent_message`-события в один `assistant`-конверт claude
stream-json (`CODEX_VERBOSE=1` — включить в накопление ещё и вывод команд). `system_prompt`
рецепта для codex не используется. Требование в образе: `jq`.

`command: codex-as-claude` также можно использовать напрямую в стадии flow (без autoShim-рецепта) —
`docker.UsesCodex` детектит и этот путь для гейтинга монтирования `~/.codex`.

### Известные грабли (Docker-mode)

- **gosu сбрасывает HOME для uid без записи в `/etc/passwd`** → ставит `HOME=/`. Поэтому в `docker-entrypoint.sh` HOME задаётся **после** gosu (`gosu uid:gid env HOME=/home/afm afm …`), а не до. Иначе агенты ищут `~/`-файлы в `/` (баг: токен искался в `//.ai-free/…`).
- **`:ro` single-file bind-mount + атомарный rename = corruption.** Приложения, переписывающие конфиг через temp+rename (claude и `~/.claude.json`), не могут обновить `:ro`-маунт и квартитят его как corrupted. Не монтируй `:ro` то, что приложение пишет — пусть создаст свежий container-local файл.
- **`os.ModeCharDevice` ≢ TTY.** `/dev/null` — тоже char device, поэтому эвристика `Stdin.Stat().Mode()&ModeCharDevice` ложно добавляла `-it` в не-TTY → `docker run` падал "the input device is not a TTY". Честная проверка — `golang.org/x/term.IsTerminal`.
