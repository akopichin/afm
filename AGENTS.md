# AFM Development Guide

Условные обозначения к пути кода даются как ориентир — точные имена символов
смотри в коде, а не в этом файле (он отстаёт при рефакторингах).

## Composition and shared policies

- `cmd/afm/run.go`: загрузка конфигурации/preflight и `executeFlow`, владеющий
  ресурсами рана через defer. Подготовка CWD/memory/wrappers и Docker re-exec —
  `run_environment.go`; lifecycle — `run_lifecycle.go`; сервер —
  `run_dashboard.go`; workspace и адаптеры review-заметок — `run_workspace.go`.
  `normalizeInteractiveStages` создаёт копию slice ДО `orchestrator.New`;
  не меняй `Interactive` после создания graph/оркестратора.
- `activatePrePlannedStage` — общая подготовка стадии без planning-агента для
  startup и завершения зависимостей. `startReadyStage` — общий CAS-защищённый
  ready → running → spawn для scheduler и retry. `executionKind` выбирает
  implementation/autonomous с учётом legacy `autonomous.flag`.
- `resumeStageAtStatus` используется startup и Continue; `resumeExecutionStage`
  проверяет артефакты и verify, `recoverPlan` — план и политику approval.
  Fresh-start выполняет before-hook; resumed agent его повторно не запускает.
  Retrying без planning после рестарта по-прежнему идёт через ready/before-hook.
- `publishNotice` (`pkg/orchestrator/notices.go`) связывает UI и `notices.jsonl`
  для best-effort non-FSM уведомлений. FSM и critical completion сохраняют
  собственные durable/CAS/delivery-пути.
- Статический конфиг сервера — `Config.Stages map[string]StageConfig`;
  runtime-представление — `StageView`. Промпты кнопок в StageConfig не передаются.
- `App` использует `useSelectedStage` и `useReviewNoteCount`; режим workspace
  принадлежит `useWorkspaceView`. `useFilePreview` владеет content/diff/Reload;
  каждое переключение файла/вкладки и unmount инвалидирует старые ответы,
  даже если пользователь вернулся к тому же пути.

## Working directory: `.afm` and `--dir`

- afm хранит runs/flows/config под `.afm/` в рабочем каталоге. Родительский
  каталог резолвится в `PersistentPreRunE` (`cmd/afm/main.go`) по приоритету
  **флаг `--dir` > env `AFM_DIR` > `.`**. Подкоманды берут путь через `fmDir()`
  (`filepath.Join(rootDir, ".afm")`); `state.FindLatestRunDir(base, flowName)`
  принимает базу runs аргументом, а не хардкодит путь.
- **Project root для агентов: `flow.root_dir`** (`flow.Flow.RootDir`). afm
  считает, что afm-root (родитель `.afm/`) == корень проекта. Когда это не так
  (Docker: исходники в `/workspace`, `.afm/` отдельно), задай `root_dir` — он
  становится CWD агентов: относительный путь резолвится от afm-root в
  `cmd/afm/agent_environment.go` и протягивается `orchestrator.Options.RootDir` →
  `executor.Config.Dir` → `cmd.Dir`. Пусто → наследуется CWD процесса.
  `AFM_STAGE_DIR` (файлы диалога) всегда анкорится к afm-root, независимо от
  `root_dir`.
- `agent_action`-события получают `stageID` из `OnAction`-замыкания per-stage
  раннера (`runnerFor`, `runner_factory.go`). Инъектированный `o.runner` (тесты,
  пустой stageID) используется только при `opts.Runner != nil` и у стадии нет
  своего `command`; в продакшене у каждой стадии свой раннер с корректным `s.ID`.

## CHANGELOG conventions

- **Записи в `CHANGELOG.md` — на английском** (коммиты остаются русскими). Формат:
  датированная секция `## YYYY-MM-DD` (новые сверху) с подсекциями
  `### Improvement:` / `### Fix:`.
- Никаких ссылок на keepachangelog.com — формат описан во введении без упоминания
  названия.

## State persistence & run lifecycle (reliability core)

`.afm/runs/<run_id>/events.jsonl` — **единственный источник правды**. `state.json`
— производный кэш (несёт `last_seq`); read-пути (`afm check`, поиск рана) читают
состояние из лога через `state.LoadRunState` (без flock), а не из снапшота.

- **flock между процессами.** `state.Open` держит эксклюзивный flock на
  `<runDir>/.lock` на всё время жизни `Store`. Живой `afm run` держит его; CLI
  `approve`/`retry`/`revise` против активного рана падают с `state.ErrRunLocked`.
  flock снимает ОС при выходе процесса. `afm check` (read-only) не блокируется.
- **Non-destructive replay.** Оборванный хвост (последняя запись без `\n`) —
  безопасно усекается. Битая **полная** строка в середине лога (с валидными после
  неё) → карантин в `events.jsonl.corrupt-<ts>` + `state.ErrCorruptLog`, оригинал
  не трогаем.
- **Durable snapshot.** `writeSnapshot` делает `f.Sync()` до Close и fsync
  родительского каталога после Rename. Ошибка записи снапшота не фатальна (кэш).
- **Unique run-id** — `<flow>-<timestamp>-<rand4hex>`. `FindLatestRunDir`
  анкорит префикс (после `<flow>-` должна быть цифра — `foo` не матчит `foo-bar`);
  `FindLatestRunForStage` — единая точка поиска рана по стадии из лога.
- **Storage-fatal завершает ран.** `Trigger` через `errors.As(*StorageError)`
  отличает реальный сбой записи лога (→ `setFatal` + cancel run-ctx) от безобидных
  `ErrConcurrentChange` (CAS-мисс, тихий no-op) и `ErrNoRule` (log-and-drop).
- **Clean shutdown.** Все агент-горутины запускаются через
  `concurrency.Manager.SpawnAgent` (семафор + activity-маркер + `agentWG`). На
  выходе `Run`: `cancel()` → `WaitAgents()` (bounded 10s) → `store.Close()`.
  Завершения агентов публикуются под run-ctx (не `context.Background()`).
- **Durable approve/revise/retry.** `Approve`/`Revise`/`Retry` синхронны: durable
  переход коммитится в лог ДО возврата (падение не теряет намерение — recovery
  возобновляет `ready`/`revising`/`running`). **Важно:** HTTP-инициированные
  approve/revise/retry спавнят агента под **run-scoped ctx** (`runContext`), не под
  `r.Context()` — иначе net/http отменит ctx по возврату хендлера и убьёт агента.
  Двойной запуск закрыт CAS-результатом `Trigger`.
- **Autonomous track: `agents: [auto]`** (тип `flow.AgentAuto`, `Stage.IsAuto()`)
  — стадия идёт в `runAutonomousAgent` НАПРЯМУЮ, без planning/`plan.md`. Активация
  — общий хелпер `activateAutoStage` (пишет `autonomous.flag` + `EvReady`),
  вызывается из обоих путей первой активации: `tryActivatePrePlanned`
  (scheduling.go) и `startPlanningForPending` (recovery.go). `startReadyStages`,
  `retryStage`, `resumeStageAtStatus` дополнительно чекают `isAutonomousStage`,
  чтобы гонка CAS не запустила `runImplementationAgent` (который читает
  несуществующий `plan.md`). Валидация `ParseFile`: `auto` — единственный агент.

### Persistent IDLE/BACKOFF footer metrics

- **Аккумуляторы в `RunState`, не в событиях.** `IdleAccumulatedMs`/
  `BackoffAccumulatedMs` (`int64`, `pkg/state/state.go`) персистятся в снапшот и
  восстанавливаются реплеем лога. `IdleSince()`/`BackoffOpenSince()` не
  персистятся — считаются на чтении из `Stages[].UpdatedAt`.
- **Один хелпер, два call-site.** `accountIdleAndBackoff(rs, stageID, to, t)`
  обновляет оба аккумулятора по состоянию `rs.Stages` ДО применения перехода.
  Вызывается из `Store.Apply` (live) И из `parseEventLog` (replay) — это разные
  функции, `parseEventLog` не идёт через `Apply`. Забыть один call-site → числа
  разъедутся между живым раном и рестартом.
- **Idle — flow-wide**: idle, если любая стадия в `awaiting_user_input`/
  `awaiting_approval`, ЛИБО есть `failed` и нет `running`/`planning`/`revising`
  (`retrying` НЕ считается активной). **Backoff** суммируется параллельно: несколько
  `retrying`-стадий складываются независимо.
- `Store.Apply` берёт `t.Time` один раз (`SetStageStatusAt`) — иначе повторный
  `time.Now()` после fsync искажал точность. `NewRunState` НЕ штампует `UpdatedAt`
  для pending-стадий — иначе на каждом `Store.Open` untouched-стадия доминировала
  бы в `maxUpdatedAt()` и портила Idle после рестарта.
- **API `/api/status`:** `idle_accumulated_ms`, `idle_since` (omitempty),
  `backoff_accumulated_ms`, `backoff_open_since`.
- **Frontend — anchor + tick, без event-replay.** `useIdleMs`/`useBackoffMs`
  (`pkg/web/dashboard/src/hooks/`) считают `accumulated + (now - since)`, берут
  `connected` — при `false` тикер замирает; на reconnect `useStatus`-поллинг
  подтягивает уже скорректированный сервером anchor.

### Stage order in the dashboard — топологический, не порядок объявления

- `buildStageViews` (`pkg/server/stageview.go`) пересчитывает порядок через
  `topoOrder` — стабильный Кан: очередь ready-нод сидируется и пополняется в
  порядке исходного `StageOrder`, независимые стадии сохраняют взаимный порядок
  объявления, зависимая рендерится сразу после ВСЕХ своих `depends_on`.
- `state.RunState.StageOrder` не трогается (авторитетный порядок для
  state/scheduling); `topoOrder` — чисто display-слой в `pkg/server`.
  `StageConfig.DependsOn` заполняется из `flow.Stage.DependsOn` в
  `dashboardStageConfig` (`cmd/afm/run_dashboard.go`).

### Auto-advancing выбранной стадии в дашборде

- `useSelectedStage` держит `selectedStageId` и автоматически переводит выбор на следующую
  активную стадию, когда отслеживаемая завершилась — но не двигает пользователя,
  если он вручную открыл уже завершённую стадию.
- **`wasLive` — флаг per-selection, не per-tick.** Живёт, пока не сменится
  `selectedStageId`. Пока стадия done и была «живой» при этом выборе, поиск
  следующей активной повторяется на КАЖДОМ поллинге (self-correcting за один цикл)
  — важно для script-стадий, где `running` может пройти между двумя поллами (3s).
  Ручной клик по завершённой стадии `wasLive` не ставит.

## Paused Stage Status

Статус `paused` покрывает: (1) `auto_run: false` в `flow.yaml` — гейт первой
активации (ждёт Continue); (2) ручная пауза через kebab-меню для
`running`/`planning`/`revising`/`retrying`; (3) script-стадии паузятся только через
(1) (mid-script стоп архитектурно не поддержан).

- **`flow.Stage.AutoRun *bool`** (yaml `auto_run,omitempty`) — указатель, чтобы
  отличать «не задано» (`nil`) от `false`. Только явный `auto_run: false` уводит
  стадию сразу в `paused` (`PausedFrom: pending`). Легально на стадии любого типа.
  Хелпер `AutoRunDisabled()`; гейт — `shouldGateAutoRun` (`scheduling.go`) в обеих
  точках первой активации. Срабатывает **один раз** (см. `PausedFrom`).
- **`state.StageState.PausedFrom` — dual-purpose, не очищается на выходе из
  paused.** Хранит статус, из которого ушли в паузу. Пока `paused` — туда и
  возобновлять. После Continue поле остаётся непустым НАВСЕГДА — постоянный маркер
  «стадия уже проходила pause-цикл», который `shouldGateAutoRun` использует, чтобы
  `auto_run:false` срабатывал только на первой активации. `StageView.PausedFrom`
  (JSON `paused_from`) НЕ наследует эту постоянность — заполняется только при
  `Status == StatusPaused`.
- **`Continue` == «сделай вид, что afm перезапустился и нашёл стадию в
  `PausedFrom`».** `Orchestrator.Continue` (`control_api.go`): для
  `PausedFrom == pending` — обычная активация; для остальных статусов —
  `resumeStageAtStatus` (`recovery.go`), тот же диспетчер, что и после краха.
- **Manual pause переиспользует `interruptChans`** (SIGINT + 15s grace, тот же
  механизм, что `Revise`). `Orchestrator.Pause` синхронно коммитит `EvPause` в лог
  до сигнала канала. `runWithRetry` (`retry.go`) на `ErrUserInterrupted` чекает
  `currentStatus == paused`, чтобы отличить Pause от Revise. Для `retrying` в
  backoff-select добавлена ветка `<-interruptCh` (там Revise недостижим).
- **`withBeforeHook` (`hooks.go`) перечекивает статус после `script_before`** — не
  вызывает `mainFn`, если после хука `currentStatus(s.ID) == StatusPaused`
  (`script_before` крутится без interrupt-канала).
- **Гонка «Pause пока в очереди за семафором» закрыта централизованно в
  `concurrency.Manager.SpawnAgent`**: поле `shouldRun func(stageID string) bool`
  чекается СРАЗУ после `sem.acquire()`+`markActive`, до `run(ctx, s)`.
  `orchestrator.New` передаёт `func(id) bool { return Store.Get(id) != StatusPaused }`.
  Единая точка для любого пути запуска агента.
- `pkg/server/stageview.go`'s `ShowPlan` учитывает `paused` (не только `failed`) —
  иначе у autonomous-стадии в паузе не рендерился Continue-button (PlanPanel не
  монтировался). `use-attention.ts`: `paused` входит в
  `ATTENTION_STATUSES`/`AttentionKind`.
- **Continue не должен подвешивать зависимые стадии.** «Recovered» fast-path'ы в
  `resumeStageAtStatus` (найден `execution_summary.md`/`.done`) завершают стадию
  через `completeStage(ctx, id, status, reason)` — общий каскад
  (`failBlockedStages`/`startPlanningForUnblocked`/`startReadyStages`/
  `tryActivatePrePlanned`), а не голый `Trigger(EvComplete)` — иначе зависимые
  зависают в `pending`.

## Pre-note: заметка стадии до её старта

Заметка на ещё не стартовавшую (`pending`) стадию, вклеивается в контекст агента
на его **первом** старте (как часть исходной задачи, не как коррекция уже начатой
работы — в отличие от `Revise`/`feedback.md`).

- **Файл `<stageDir>/prenote.md`, не событие и не FSM.** `pending` остаётся
  `pending`. `state.SavePreNote`/`state.LoadPreNote` (`pkg/state/state.go`) пишут/
  читают напрямую. Сохранение **заменяет** текст; пустое/whitespace → удаление
  файла. Запись атомарна (temp+rename, `MkdirAll` внутри). После старта файл НЕ
  удаляется — 📝-индикатор остаётся.
- **Вклейка — `(*Orchestrator).preNoteBlock(stageDir)`** (`pkg/orchestrator/agents.go`):
  блок `## User note (added before this stage started)`, добавляется в
  `RetryContext` на первом (fresh) старте всех четырёх раннеров
  (`runPlanningAgent`/`runImplementationAgent`/`runReviewAgent`/`runAutonomousAgent`).
  `*WithFeedback`-раннеры pre-note НЕ вклеивают.
- **HTTP `POST /api/stages/{id}/note`** `{"note":"..."}` (`handleStageNote`,
  `pkg/server/handlers.go`): гейт — статус `pending` И не script, иначе `400`.
  Пустой `note` → удаление. Текст возвращается в `pre_note` (omitempty) у
  `StageView`.

## Stage buttons: предзаданные промпты в kebab-меню

Стадия объявляет named one-click действия в `flow.yaml`; клик доставляет промпт
**живому** агенту тем же путём, что free-text заметка (`Revise`).

```yaml
stages:
  - name: build
    buttons:
      Run linter: "Запусти golangci-lint и почини все замечания"
      Rebuild:    "Пересобери проект с нуля и убедись что тесты зелёные"
```

- **Упорядоченный `Buttons` тип, не map.** `flow.Stage.Buttons` = `type Buttons
  []Button{Label, Prompt}` с кастомным `UnmarshalYAML(*yaml.Node)` (обход
  `Content` парами → **порядок меню == порядок YAML**). Хелперы `Prompt(label)`,
  `Labels()`. Дубли ключей ловит `validate()` (не yaml.v3). `Flow.validate()`
  отвергает пустой label/prompt, дубль label в стадии, `buttons` на `script:`.
- **Клик == Revise живого агента; клиент шлёт только *имя*.** `POST
  /api/stages/{id}/button` `{"name":"..."}` (`handleStageButton`) → гейт (статус
  `running`/`awaiting_approval` И не script; неизвестное имя → `400` через
  labels-only `StageConfig.Buttons`) → `StageActions.Button` → `(*Orchestrator).Button`
  (`control_api.go`) резолвит `Buttons.Prompt(name)` и делегирует в `Revise`.
  **Текст промпта клиенту не доверяется** — резолвится на сервере.

## Lifecycle hooks (Phase 1 + Phase 3: секреты и env)

Observer-only уведомления/метрики о жизненном цикле рана и стадий — команды
(`sh -c`) с JSON-payload события в stdin. В отличие от `script_before`/
`script_after` (те блокируют стадию и меняют FSM), lifecycle-хуки **никогда не
трогают FSM** — сбой хука (после retry) только логируется и всплывает live+durable
предупреждением `lifecycle_hook_failed`. Пакет: `pkg/lifecyclehooks/*`.

- **Конфигурация — 4 слоя, keyed-merge по id.** Global (`~/.afm/config.yaml`) →
  project (`.afm/config.yaml`) → flow (`flow.yaml` верхний уровень) → stage
  (`stages[].hooks`). Global+project мёржит `config.LoadFrom` (`mergeFile`, keyed
  по `Hook.ID`); flow/stage добавляются в `combineRunHooks`
  (`cmd/afm/run_lifecycle.go`) через `lifecyclehooks.Combine`.

  ```yaml
  hooks:
    - id: notify-slack
      events: all
      skip_events: [stage_question_asked]
      command: "curl -sf -X POST $SLACK_WEBHOOK -d @-"
      timeout: 10s
      retries: 2
  ```

  `Hook{ID, Events, SkipEvents, Command, Timeout, Retries}` (`types.go`). `events`
  — scalar-or-list (`EventSelector`): `all` или список; `skip_events` вычитает.
  `timeout` дефолт `DefaultHookTimeout = 30s`; `retries` дефолт `0`
  (`attempts := h.Retries+1`), пауза `hookRetryBackoff = 1s`.
- **Каталог событий — 27 имён** (`AllEvents()`): 5 flow (`flow_started`,
  `flow_resumed`, `flow_finished`, `flow_failed`, `flow_interrupted`) + 13
  stage/FSM (`stage_planning_started`…`stage_finished`/`stage_failed`) + 9
  script/не-FSM. `IsFlowEvent` отмечает первые 5 как недопустимые в stage-скоупе.
- **Precedence по id — плоская замена.** `Combine` держит единый namespace id:
  хук с тем же id из более позднего слоя заменяет ранний целиком на исходной
  позиции; новый id дописывается. Валидация (`ValidateLayer`, `validate.go`):
  дубль id внутри слоя — ошибка; `id` обязан быть безопасным одиночным компонентом
  пути (без `/`, `\`, NUL, `..`/`.` — иначе `id: ../events.jsonl` дописал бы в
  авторитетный лог); flow-событие в stage-хуке — ошибка. Cross-stage дубль id
  ловит `Flow.validate()`.
- **Payload (stdin, JSON):** `Payload{SchemaVersion:1, EventID, Event, OccurredAt,
  Flow{...}, Stage *StageInfo, Reason, Data}` (`BuildPayload`); `Stage` — `nil` для
  flow-событий. `event_id` (`Dispatcher.eventID`) — 4 схемы: FSM-переходы
  (`Seq>0`) `<run>:transition:<seq>:<event>` (durable); прочие flow
  `<run>:flow:<event>`; события с непустым `Key` `<run>:<stageID>:<key>:<event>`;
  прочие stage `<run>:<stageID>:<event>` (best-effort). Плюс 10 `AFM_*` env
  (`buildEnv`/`afmVars`): `AFM_HOOK_EVENT`, `AFM_HOOK_EVENT_ID`, `AFM_FLOW_NAME`,
  `AFM_RUN_ID`, `AFM_RUN_DIR`, `AFM_ROOT_DIR`, `AFM_STAGE_ID`, `AFM_STAGE_NAME`,
  `AFM_STAGE_FROM`, `AFM_STAGE_TO` (последние 4 пустые для flow-событий).
- **Runner (`runner.go`): `sh -c h.Command`**, stdin = JSON payload, `cmd.Dir` =
  эффективный `root_dir`. Вывод стримится (не буферизуется) в
  `.afm/runs/<run_id>/hooks/<id>.log`. Таймаут-килл — process-group
  (`setProcessGroup`/`killProcessGroup` в `runner_unix.go`/`runner_windows.go`
  через `cmd.Cancel`).
- **Dispatcher (`dispatcher.go`): один воркер на хук** — доставки одному хуку
  последовательны, между хуками параллельны. Очередь `hookQueueSize = 256`; `Emit`
  неблокирующий (полная очередь → drop + `OnError`). `OnError` →
  `orch.PublishLifecycleHookFailure` → `bus.EventLifecycleHookFailed` live +
  durable `notices.jsonl` (тон `warning`). Собственный `context.Background()`
  (не run-ctx) — `flow_interrupted` должен дойти после отмены рана. `Flush(10s)` +
  `Stop()` в `defer` на любом выходе `RunE`; `Emit`/`Stop` под RWMutex.
- **Эмиссия FSM-событий — `triggerWithSeq`** (`orchestrator.go`): эмитит
  `emitLifecycleTransition` только для успешного перехода (после CAS, с реальным
  `seq`). `fsmLifecycleEvents` (`lifecycle_emit.go`) — таблица `bus.FSMEvent →
  EventType`. Flow-терминальные события запоминаются (`setTerminalFlow`,
  классификатор учитывает `hasFailedStage`) и эмитятся в `defer finalizeLifecycle()`
  — по LIFO после `WaitAgents()`. Не-FSM границы — `emitStageEvent`.
- **Гарантии Phase 1 — live best-effort, не durable.** Падение между `Emit` и
  доставкой теряет событие (очередь в памяти). Durable outbox — будущая работа.

### Phase 3: секреты и env lifecycle-хуков

Хук объявляет `env:` — доп. переменные окружения, значения которых берутся из
секретов, а не хранятся в `flow.yaml`. Дефолт окружения — **минимальный**, не
полное наследование afm-процесса.

```yaml
hooks:
  - id: notify-telegram
    events: [flow_finished, flow_failed]
    command: 'curl -sf ... "$TG_TOKEN" ...'
    env:
      TG_TOKEN: "file:~/.afm/secrets/telegram-token"
      TG_CHAT: "env:AFM_NOTIFY_CHAT_ID"
```

- **`Hook.Env map[string]SecretRef`** (`types.go`) — имя переменной → `SecretRef`
  вида `env:NAME` | `file:PATH` (та же семантика, что `auth.from` у docker
  agent-recipes). `Hook.InheritEnv bool` (yaml `inherit_env`, дефолт `false`)
  переключает базовое окружение. `Hook.ResolvedEnv map[string]string`
  (`yaml:"-"`) — только в памяти, не сериализуется.
- **`pkg/secrets` — нейтральный резолвер** (вынесен из `pkg/docker`, оба
  потребителя делят один). `ResolveRef(ref, loaded)` резолвит `env:VAR` (сначала
  `loaded`, затем `os.Getenv`) и `file:PATH` (`~` через `ExpandHome`, содержимое
  `TrimSpace`); `LoadSecrets(paths)` парсит `KEY=VALUE`-файлы (later-wins).
- **Валидация имён (`validate.go`):** имя матчит `[A-Za-z_][A-Za-z0-9_]*`; префикс
  `AFM_` зарезервирован **регистронезависимо** (`strings.ToUpper`); коллизии после
  uppercase внутри хука — ошибка. Существование источника здесь не проверяется.
- **Резолв — один раз при сборке dispatcher'а, fail-fast до `flow_started`.**
  итоговый список собирается `combineRunHooks` в `cmd/afm/run.go` **до** докер-ветки
  (единый источник для host-резолва, `docker.ReExec` и in-container резолва —
  иначе транспортные индексы `hookIdx`/`varIdx` разъехались бы). Host:
  `ResolveHookEnv(hook, secretsMap)`; ошибка называет id хука и имя переменной, но
  **никогда значение**.
- **`secrets.env` — 3 слоя, `project > global > env процесса`.**
  `loadHookSecretLayers(afmRoot)` (`cmd/afm/agent_environment.go`) переиспользует
  `docker.LoadSecretLayers`: `~/.afm/secrets.env` → `<afmRoot>/.afm/secrets.env`
  (later-wins), приоритет над обоими — `os.Getenv`. Ленивая загрузка: читается,
  только если у какого-то хука непустой `Env` (`anyEnv`-гейт).
- **Контракт окружения (IMPORTANT).** `buildEnv` (`runner.go`):
  `InheritEnv==false` → `minimalBaseEnv()` (allowlist: `PATH`/`HOME`/`TMPDIR`/
  `LANG`/`LC_*`/`*_PROXY`/`NO_PROXY`/`SSL_CERT_*`); `InheritEnv==true` →
  `stripTransportVars(os.Environ())` (полное окружение минус префиксы
  `AFM_HOOK_SECRET_`/`AFM_SECRET_`/`AFM_SYSPROMPT_`). Поверх — `afmVars` (10
  `AFM_*`) и последними `ResolvedEnv`.
- **`[REDACTED]`-редакция в логе хука И в ошибке/notice.** `newRedactingWriter`
  (`redact.go`) оборачивает stdout/stderr (и заголовки лога), если у хука есть
  `ResolvedEnv`. Потоковый bounded-редактор с fixed-point заменой; `redactMarker`
  выбирает первый безопасный из `["[REDACTED]", "[[hook-secret-redacted]]",
  "\x00REDACTED\x00"]` (маркер сам не содержит секрет, его суффиксы не префиксы
  секретов, он не подстрока секрета) — иначе fallback пустая строка. Ошибка тоже
  санитизируется (`redactString`) до возврата. Best-effort: скрипт, кодирующий
  секрет посимвольно, не ловится.
- **Docker-транспорт — резолв на хосте, transient bare `-e` по индексам.**
  `pkg/docker/launcher.go`'s `ReExec`: если `UsesLifecycleSecrets(cfg.Hooks)`,
  хост резолвит `Hook.Env` до `docker run` и передаёт значения как
  `-e AFM_HOOK_SECRET_<hookIdx>_<varIdx>` (`hookIdx` — позиция в `combinedHooks`,
  `varIdx` — позиция в `SortedEnvKeys`; индексная схема исключает коллизии id).
  Значение уходит в env самого afm-процесса перед exec (не в `argv` — не видно в
  `ps aux`). Секретные файлы в контейнер не монтируются.
- **Ограничение изоляции:** секретный файл, физически лежащий под смонтированным
  каталогом (`~/.afm`, каталог проекта), всё равно читаем изнутри контейнера любым
  агентом под тем же пользователем — транспорт закрывает только `argv`/`ps aux`.
  Для сильной изоляции держи секреты ВНЕ смонтированных каталогов или используй
  `env:`-источник из окружения хост-процесса.
- **In-container:** с `AFM_IN_DOCKER=1` хуки резолвятся из транспорта
  (`ResolveHookEnvFromTransport(hookIdx, hook)`, `env.go`; отсутствие/пустота —
  fail-fast). Сразу после резолва всех хуков и **до** `Orchestrator.Run` —
  `UnsetTransportVars()` снимает каждую `AFM_HOOK_SECRET_*` из окружения (единая
  точка изоляции, ни один дочерний процесс их не унаследует).

## AI-verify: independent verification gate

`verify` — **stage-completion gate**, не новый FSM-статус, не фаза `agents`, не
переименование `review`: после того как агент стадии закончил и прошёл file-probe
(`.done`/artifacts или `execution_summary.md` для `agents:[auto]`), afm гоняет
объявленные verify-шаги (shell и/или read-only AI-ревьюер) до принятия стадии.

- **`flow.VerifySpec`/`flow.VerifyStep`** (`pkg/flow/verify.go`) нормализует три
  YAML-формы (scalar / один объект / список) в `Steps []VerifyStep`, каждый —
  `Kind: VerifyShell` (`Run`) или `Kind: VerifyAgent` (`Command`+опц. `Prompt`) +
  опц. `Timeout`. `VerifySpec.validate(stageID)` (из `Stage.validate()`) — бизнес-
  правила с 1-based индексами (`verify[%d]: run and command are mutually
  exclusive`). `agent` отвергается как unknown-поле (подсказка использовать
  `command`). `verify` запрещён на `script:` и на planning-only стадии.
  `Stage.VerifyAgentCommands()` — дедуп alias'ов для docker-mount/shim discovery.
- **v1 — только codex.** `config.SupportedVerifyCommand`/`ValidateVerifySpecs`
  (`pkg/config/verify_resolve.go`) чекаются на preflight (`cmd/afm/run.go` до
  старта стадий). Принимается `codex-as-claude` напрямую или `docker.agents`-рецепт
  с `Type == RecipeTypeCodex` И `codexRecipesShimmed`. Голый `codex` (без рецепта)
  и `claude` НЕ поддержаны: у них нет реального read-only режима, `VerifyMode`
  ставит только `CODEX_VERIFY=1`, который claude игнорирует.
- **`(*Orchestrator).RunVerification`** (`pkg/orchestrator/verify.go`) — ОДИН
  fail-fast проход по `s.Verify.Steps`, без внутреннего цикла коррекции (коррекция
  — через существующий incomplete-retry путь вызывающего). Подключён через
  `gateWithVerify`, оборачивающий `completionCheck`
  (`CheckCompletion`/`CheckAutonomousCompletion`) так, что verify стреляет только
  когда file-probe уже прошёл И `phase` ∈ `implementation`/`review`/`autonomous`
  (никогда planning). Прочие вызовы `completionCheck()` (GET/status) платный AI не
  триггерят.
- **Machine result — `pkg/orchestrator/verify` (`result.go`).**
  `DecodeModelResult` — строгий декодер (`ModelResult{SchemaVersion, Verdict,
  Summary, Findings}`): отвергает markdown-fence, дубли ключей на любой глубине,
  unknown-поля, trailing-контент, неподдерживаемый `SchemaVersion` (только
  `SupportedSchemaVersion = 1`), невалидный `Verdict`, verdict/findings-
  несогласованность (`validateVerdictConsistency`). Кап `MaxResultBytes =
  256*1024` чекается на ПОЛНОМ входе до парса. `Finding.Path`/`LineStart`/`LineEnd`
  — указатели (finding может быть не привязан к файлу/строке).
- **File namespace — `pkg/orchestrator/stagefiles/verify.go`.** Под
  `<stageDir>/verify/`: `feedback.md` (active feedback, кап `feedbackBudgetBytes =
  12*1024` через `truncateUTF8`; on-disk `report.md` НЕ усекается) и per-pass
  `<verification-id>/{manifest.json, report.md, step-NN/}`. `NewVerificationID()`
  → `v-<ts>-<hex>` (отличается от retry `attempt`). `SaveRawResult` (до валидации)
  vs `SaveAcceptedResult` (после). HTTP-чтение отчёта — `handleVerifyReport`
  (`pkg/server/verify_report.go`, `GET /api/stages/{id}/verify/{verID}/report`)
  через `os.OpenRoot(runDir)` (client-supplied `verID`/stage id не выйдет за
  runDir через `..`/symlink); `report_path` в событии — только для отображения.
- **Read-only Codex adapter — `scripts/codex-as-claude.sh`.** Режим
  `CODEX_VERIFY=1` (ставит только `executor.RunVerifyAgent`): без
  `--dangerously-bypass-...`, всегда `-s read-only`, без эскалации, финальный ответ
  через `--output-last-message`.
- **`executor.Config.VerifyMode`/`RunVerifyAgent`** (`pkg/executor/executor.go`):
  свежая сессия (без `--resume`), без `AFM_STAGE_DIR`. Окружение фильтруется:
  срезает inherited `CODEX_VERIFY`, `IsCrossAgentTransportSecret`-матчи (секреты
  чужих хуков/агентов) и `isClaudeAuthorCredential` (auth автора), но оставляет
  свой секрет (`isVerifierOwnSecret`). Для shell-шага — тот же фильтр
  `sanitizedShellVerifyEnv()`/`IsCrossAgentTransportSecret` в `verify.go`.
- **Command-slot lease swap — `pkg/orchestrator/concurrency/lease.go`.** Стадия
  держит `*Lease` на слот команды автора (`o.stageLeases`). Каждый AI verify-шаг —
  `Lease.SwapTo(ctx, st.Command)`: тот же command → no-op fast-path (не
  release+reacquire единственный слот на `max_parallel:1` — self-deadlock); другой
  → release-before-acquire. `defer` возвращает lease автору **только** на
  `*VerifyRejectedError` (единственный случай немедленного релонча автора);
  на прочих исходах `run(ctx,s)` и так возвращается. `pauseAwareVerifyCtx` делает
  ожидание слота/подпроцесса прерываемым через Pause/Revise.
- **FSM — `bus.EvVerifyFail`** (`bus/fsm.go`): атомарный брат `EvFail`, но `From`
  ограничен активными статусами (без `StatusPlanning`) — CAS сам отвергает stale-
  переход (закрывает TOCTOU, который был у `EvFail` с `From == nil`).
  `commitVerifyFailure` вызывается только через `isVerifyDrivenError`
  (`errors.go`, `errors.As` против `*VerifyRejectedError`/`*VerifyExecError`);
  прочие терминальные ошибки идут обычным `EvFail`.
- **Typed errors (`errors.go`):** `VerifyRejectedError` встраивает
  `*stagefiles.IncompleteWorkError` (`Unwrap`) → идёт через существующую
  incomplete-retry классификацию (`ClassIncomplete`). `VerifyExecError` —
  отдельная классификация `ClassVerifyFailed`, чекается `errors.As` ДО
  substring-`Classify` (свободный текст summary с «rate limit» не должен принять
  за transport-retryable).
- **Observability — non-FSM, live + durable.** `emitVerifyStarted`/
  `emitVerifyResult` публикуют `bus.EventVerifyStarted`/`EventVerifyResult`
  (`"verify_started"`/`"verify_result"`) через `publishNotice` live + durable в
  `notices.jsonl`. Cost-accounting помечается `verifyExecutionLabel = "verify"`,
  но verify НЕ в `flow.Phases()` (это ортогональный проход, не фаза).
- **Инварианты перед правкой:** (1) verify никогда сам не зовёт `EvComplete`/не
  активирует зависимые — только нормальное завершение после `RunVerification()==nil`;
  (2) протухший step-timeout не должен пройти как pass — `verifyStepDeadlineRace`
  перечекивает `stepCtx.Err()` после любого success; (3) прерывание (Pause/Revise/
  cancel) не должно записаться как `needs_changes` — interrupted/timeout-ветки
  чекаются ДО «non-zero exit ⇒ needs_changes»; (4) storage-сбой при персисте —
  `*VerifyExecError` (fail-closed), `persistVerifyRejection` — единый упорядоченный
  commit (result → report → manifest → active feedback → emit).

## File-Based Dialog Protocol (Interactive Stages)

Агенты задают вопросы и получают ответы через файловый I/O (MCP HTTP-сервер
удалён).

**Протокол:**
- Агент пишет `$AFM_STAGE_DIR/<phase>.<id>.question.json`
  (`{"id", "question", "options", "allow_custom"}`) и поллит
  `<phase>.<id>.answer.json` в bash-цикле.
- Оркестратор поллит вопросы каждую 1s (`startQuestionPoller`/`pollQuestions`,
  `pkg/orchestrator/dialog_poller.go`), публикует `EventAskUser` (→
  `awaiting_user_input`). UI поллит `/api/stages/<id>/dialog` ~2s.
- HTTP-хендлер (`handleDialogAnswer`, `pkg/server/handlers.go`): валидирует phase
  через `flow.IsValidPhase` (`planning`/`implementation`/`review`/
  `autonomous_execution`, `pkg/flow/phase.go`) и id (safe filename, без traversal);
  `answer.json` уже есть → `409`; **атомарно** пишет `answer.json` (O_EXCL) ДО FSM-
  перехода; дописывает `dialog.jsonl` (best-effort); `NotifyAnswer`
  (`pkg/orchestrator/control_api.go`) либо двигает FSM (агент жив), либо
  рестартит агента (`--resume`, если вышел).

**Ключевые файлы:** `pkg/mcp/dialog.go` (`FindUnansweredQuestions`,
`QuestionFile`, `appendLine`); `pkg/orchestrator/dialog_poller.go`
(`startQuestionPoller`/`pollQuestions`/`relocateMisplacedQuestions`);
`pkg/orchestrator/control_api.go` (`NotifyAnswer`);
`pkg/orchestrator/concurrency/concurrency.go` (`Manager.activeAgents` sync.Map,
`markActive`/`markDone`); `pkg/executor/executor.go` (`AFM_STAGE_DIR` из
`Config.StageDir`); `pkg/prompts/builder.go` (`<interactive_rules>`).

**Гарантия доставки:** `answer.json` атомарен (O_EXCL) ДО FSM-перехода — живой
bash-цикл всегда найдёт файл; вышедший агент рестартит с `--resume`.
`AFM_STAGE_DIR` ставится для ВСЕХ стадий (не только autonomous), иначе агенту
некуда писать `question.json`.

**Известные грабли:**
- **Reuse id после ответа:** `pollQuestions`'s `processed[stageID|phase|id]` map
  должна забывать отвеченные ключи — иначе повторный тот же id (агент нарушает
  «не переиспользуй id») невидим навсегда, стадия виснет.
- **Malformed `question.json` (torn-read или реальная поломка):** единый ремонт
  `handleMalformedQuestion` (`dialog_poller.go`) для ЛЮБОГО типа стадии: (1)
  jsonrepair (внутри `FindUnansweredQuestions`); (2) grace-tick (torn-read сам
  дочитается за 1s); (3) свежий изолированный fix-агент `spawnJSONFix`/
  `runJSONFixAgent` (до `maxJSONFixAttempts = 3`, чистый контекст, без
  `AFM_STAGE_DIR`, через `concurrency.SpawnDetached` — не `SpawnAgent`: главный
  агент держит слот семафора); (4) exhausted → interactive показывает stub
  человеку, non-interactive — `autoAnswerMalformed`. `QuestionFile.Malformed`
  помечает «не распарсилось», файл на диске не трогается.
- **Misplaced question:** `relocateMisplacedQuestions` нормализует файл, записанный
  вне `$AFM_STAGE_DIR` или с префиксом stage-id вместо phase, + dangling symlink на
  путь, который поллит агент.
- **Гонка «поллер обогнал агента»:** «готовность стадии» решается в ОДНОЙ
  канонической точке (`runWithRetry`), остальные потребляют её результат (события),
  а не пересчитывают через ФС. `runWithRetry`/`onAgentCompleted` при `err==nil`
  различают stale/live вопрос по текущему FSM-статусу (уже
  `AwaitingUserInput` == stale-хвост). Таблицы `EvComplete`/`EvPlanReady`
  включают `AwaitingUserInput` в `From` — иначе завершение теряется и стадия
  виснет.

### Auto-answering в non-interactive стадиях

- `pollQuestions` ветвится по `stage.Interactive`. Для `!Interactive` (дефолт,
  включая `agents:[auto]`) стадия НЕ идёт в `awaiting_user_input`:
  `mcp.PickAutoAnswer` выбирает ответ (маркер `(recommended)`/`(default)`/
  `(рекомендую)`/`(рекомендуется)`/`(по умолчанию)`, case-insensitive, первый с
  маркером; без опций — фиксированный русский fallback-текст), `mcp.WriteAnswer`
  атомарно (O_EXCL) пишет `answer.json` + best-effort `dialog.jsonl` с
  `AutoAnswered: true`. FSM не трогается.
- **`bus.EventAutoAnswered` — live + `notices.jsonl`.** Не FSM-переход, поэтому не
  в `events.jsonl`; публикуется live И durable через `publishNotice` —
  иначе клиент, подключившийся после авто-ответа, не увидит строку в фиде
  (`/api/events`'s `reconstructNotices` реплеит из `notices.jsonl`). **Забыть
  durable-копию нового non-FSM UI-события — типовой баг.**
- **Dashboard:** панель диалога гейтится реальным наличием истории, не типом
  стадии. `/api/status` отдаёт `has_dialog` (JSON-тег; frontend `stage.hasDialog`)
  — наличие любого `<phase>.dialog.jsonl`. Layout-гейт `buildStageViews`'s
  `showDialog = hasDialog || status == awaiting_user_input` ДОЛЖЕН совпадать с
  `DialogChannel`'s `hasContent` — иначе зарезервированная пустая строка диалога
  рендерится как пустота в центре. `mcp.Answer`/`Entry` несут
  `AutoAnswered bool` (json `auto_answered`) → фронт рисует `⚙`-badge.

## The flake «write events.jsonl: invalid argument» — process-group kill

Долго выглядело как случайная нестабильность окружения. Реальная причина — две
проблемы:

- **Текст ошибки маскировал источник.** `(*os.File)` nil-safe: `Write` на `nil`-
  ресивере не паникует, а возвращает `fs.ErrInvalid` с текстом `"invalid
  argument"` (не отличить от EINVAL). Если `Store.Close()` обнулил `s.eventsLog` ДО
  того как агент-горутина вызвала `Apply` → `s.eventsLog.Write` — выглядит как OS-
  флейк.
- **Причина #1 (тест-инфра):** `go func(){ _ = orch.Run(ctx) }()` без ожидания
  завершения. Тесты ждали СТАТУС стадии, но не возврат `Run()`, а
  `t.Cleanup(store.Close)` срабатывал раньше, чем `WaitAgents()`. Фикс — хелпер
  `runOrchestratorAsync` (`pkg/orchestrator/testrun_helper_test.go`): `cancel()` +
  ждёт `done`-канал. Порядок `t.Cleanup` (LIFO) важен: регистрация ожидания ПОЗЖЕ
  `store.Close()` (оба через `t.Cleanup`, не голый `defer`).
- **Причина #2 (реальный прод-баг, `pkg/executor`):** `cmd.Process.Kill()` убивает
  только прямого потомка. Если `stage.Command` порождает внука без `exec`, тот
  держит stdout-pipe открытым → `lineReader` не видит EOF → `<-done` в `run`
  блокируется на всю жизнь сироты. Фикс — `killProcessGroup(cmd, sig)`
  (`syscall.Kill(-pid, sig)`) + `SysProcAttr{Setpgid: true}` перед `Start()`
  (`pkg/executor/executor_unix.go`), во всех точках kill.

## Docker Mode

afm автоматически перезапускается внутри Docker, если Docker-режим включён.

### Enabling

```yaml
docker:
  enabled: true
  image: akopichin/afm:latest   # опц., дефолт
```

Или `AFM_USE_DOCKER=1 afm run flow.yaml`.

### Что монтируется автоматически

| Host | Container | Назначение |
|------|-----------|-----------|
| `$(pwd)` (абс. путь) | тот же путь | Проект + `.afm/` |
| `~/.claude/` | `/home/afm/.claude` | Auth, skills, memory |
| `~/.afm/` | `/home/afm/.afm` | Global config |
| Нестандартные агенты | `/usr/local/bin/<cmd>` (`:ro`) | Кастомные команды |
| `docker.extra_mounts` | `~`→`/home/afm/…`, иначе тот же путь (`:ro`) | Токены/конфиги |

- `~/.claude.json` **НЕ** монтируется — claude создаёт свежий контейнер-локальный
  (`:ro`-монтирование ломало его атомарным rename → «corrupted»).
- **Auth для `command: claude`:** macOS Keychain недоступен из Linux-контейнера →
  `claude setup-token` и `export CLAUDE_CODE_OAUTH_TOKEN=sk-ant-...` в `~/.zshrc`;
  лаунчер форвардит его (плюс `ANTHROPIC_API_KEY`/`ANTHROPIC_AUTH_TOKEN`).
- **Dashboard:** порт из `server.port` форвардится (`-p <port>:<port>`). Браузер по
  умолчанию НЕ открывается (URL печатается в лог); с `server.open_browser: true`
  открывает host-side helper (afm в Linux-контейнере не может открыть браузер на
  macOS-хосте). Внутри контейнера `openBrowser` пропускается (`config.InContainer()`).
- **Привилегии:** контейнер стартует root'ом, entrypoint (`docker-entrypoint.sh` +
  `gosu`) сразу дропает до host uid/gid (`AFM_HOST_UID/GID`) и ставит
  `HOME=/home/afm` — все записи принадлежат host-пользователю, не root.

### Container auto-detection

Два предиката в `pkg/config/config.go`:
- **`InContainer()`** — «в любом контейнере?»: `AFM_IN_DOCKER=1` ИЛИ маркер-файл
  `/.dockerenv` / `/run/.containerenv`. Управляет recursion-guard
  (`IsDockerEnabled()` → `false` в любом контейнере) и skip'ом открытия браузера.
  Auto-detect побеждает даже явный `docker.enabled: true` — afm в чужом контейнере
  запустится нативно, а не через nested docker.
- **`ReExecedIntoContainer()`** — строго `AFM_IN_DOCKER=1` и ничего больше. Только
  транспорт-потребители (autoShim wrapper-gen, lifecycle-hook транспорт-секреты,
  `AFM_DOCKER_FILE_ROOTS`, verify autoShim preflight) должны на него завязываться
  — у чужого контейнера есть маркер, но нет этого транспорта.

Marker-детект через инъектируемый seam `containerMarkerPresent`/
`containerMarkerPaths` (тесты не читают реальный `/.dockerenv`).

### Environment Variables

| Переменная | Назначение |
|-----------|-----------|
| `AFM_USE_DOCKER=1` | Включить Docker без правки конфига (игнорируется внутри контейнера) |
| `AFM_IN_DOCKER=1` | Ставит собственный re-exec afm — recursion-guard + маркер доступности транспорта (не трогать) |
| `AFM_HOST_UID`/`AFM_HOST_GID` | Entrypoint дропает root до этого uid/gid |
| `AFM_DOCKER_IMAGE` | Override образа |
| `AFM_DEBUG`/`AFM_ACCOUNTING` | Форвардятся по значению (`-e VAR=…`, не секрет) |
| `ANTHROPIC_API_KEY`/`ANTHROPIC_AUTH_TOKEN`/`ANTHROPIC_BASE_URL` | Bare `-e KEY` (без значения — не видно в `ps aux`) |
| `CLAUDE_CODE_OAUTH_TOKEN` | Long-lived OAuth для `command: claude` |

### Publishing a new image

```bash
make release-patch   # bugfix
make release-minor   # feature
make release-major   # breaking
```

`scripts/release.sh` только создаёт+пушит git-тег `vX.Y.Z`; сборку (docker-образ,
бинарники, GitHub Release, Homebrew) делает `.github/workflows/release.yml`. Любой
push в `main` авто-бампит patch-тег (`ci.yml`'s `auto-release-tag`). Релиз всегда
multi-arch (`linux/amd64` + `linux/arm64`) через `docker buildx --platform ... --push`
+ QEMU; версия в бинарник — `--build-arg AFM_VERSION`. `make docker-build`/
`docker-push` — dev-only single-arch.

### Пины версий в образе — бампить руками

`Dockerfile.runtime`: `ARG CLAUDE_CODE_VERSION` и `ARG UV_VERSION` пинят версии
`@anthropic-ai/claude-code` и `uv` (uv копируется из `ghcr.io/astral-sh/uv`).
Пины — чтобы rebuild не тянул молча новую версию (разное поведение agent-loop).
**Правило:** не бампить `CLAUDE_CODE_VERSION` на каждый npm-релиз — только после
ручного теста вместе с готовящимся afm-релизом. Текущие версии:
`npm view @anthropic-ai/claude-code version`, `github.com/astral-sh/uv/releases`.

### Non-standard agents (не claude)

`command: glm51` → afm находит бинарь через `which` и монтирует
`-v /path/glm51:/usr/local/bin/glm51:ro`. Не найден в PATH → тихо пропускается.

Ограничения: монтируется только сам бинарь/скрипт (`:ro`) — сторонние зависимости
(node/python/сиблинг-скрипты/`~/.glmrc`) не переносятся; `command` должен быть
именем из PATH (`filepath.Base`), не абс. путём; если агент читает токены из home
— добавь каталог в `docker.extra_mounts`.

### autoShim: генерируемые wrapper'ы без монтирования

С `docker.autoShim: true` afm генерирует claude-совместимые wrapper'ы для агентов
из `docker.agents.<cmd>` внутри контейнера — без `-v` бинаря и `extra_mounts` для
токенов. Секрет и system_prompt читаются на хосте и передаются transient env
(`AFM_SECRET_<CMD>`, `AFM_SYSPROMPT_<CMD>`); `url`/`model` — из смонтированного
`config.yaml`.

```yaml
docker:
  autoShim: true
  agents:
    glm51:
      model: glm-5.1
      url: https://api.z.ai/api/anthropic
      auth: { from: "file:~/.ai-free/claude-glm/token", to: "env:ANTHROPIC_AUTH_TOKEN" }
```

- `auth.to` ∈ {`env:CLAUDE_CODE_OAUTH_TOKEN`, `env:ANTHROPIC_API_KEY`,
  `env:ANTHROPIC_AUTH_TOKEN`}. Без рецепта команда монтируется `:ro`. `url`
  запекается как `ANTHROPIC_BASE_URL`.

**Типы рецептов (`RecipeType`)** — четыре не-claude адаптера в `/usr/local/bin/`:

- **`type: openai`** (`openai-as-claude`): провайдеры с настоящим
  OpenAI-совместимым `v1/chat/completions` (DeepSeek, OpenAI, локальные Ollama).
  `auth.to` не ограничен `ClaudeAuthEnvVars`. Только текст (planning/review), без
  tool-loop. Multimodal `[Screenshot: <path>]` через начальный промпт.
- **`type: openai-agent`** (`openai-agent-as-claude`): провайдеры с genuine
  function-calling (`tools`/`tool_choice`, streaming `tool_calls`). Для
  `agents:[auto]`/`interactive`. Модели даётся один инструмент `bash` (чтение/
  запись файлов, скрипты, поллинг диалога — всё сама). Каждый tool-call печатается
  как claude `Bash` tool_use (live-фид + сброс idle-таймаута). `max_turns` (дефолт
  40); сбой API-запроса → `exit 1` (стадия падает). Multimodal через оба пути
  (начальный промпт и читаемый агентом ответ диалога).
- **`type: cursor`** (`cursor-as-claude`): Cursor Cloud Agents API
  (`api.cursor.com`, асинхронный run-based, не `chat/completions`). Создаёт no-repo
  Cloud Agent, поллит до терминального статуса, архивирует. Первый ответ ~30–90s
  (старт VM). Токен `crsr_…`. `system_prompt` не используется.
- **`type: codex`** (`codex-as-claude`): OpenAI Codex CLI, ChatGPT-plan OAuth,
  **без** `AFM_SECRET_<CMD>` — `auth` опционален (единственный тип, где отсутствие
  Auth не ошибка в `Validate()`). Авторизация через смонтированный `~/.codex`
  (`docker.ReExec` монтирует `:ro` только если флоу реально использует codex —
  `docker.UsesCodex`; entrypoint копирует в `$HOME/.codex`). Wrapper резолвит абс.
  путь реального `codex` ДО попадания wrapper-каталога в PATH (иначе рекурсия) →
  `CODEX_BIN`. Адаптер гоняет `codex exec --json --dangerously-bypass-...`
  (контейнер и так изолирован), аккумулирует `agent_message`. Требует `jq`.
  `command: codex-as-claude` работает и напрямую в стадии.

Image требует `jq`, `curl` (есть в `Dockerfile.runtime`).

### Known gotchas (Docker mode)

- **gosu сбрасывает HOME для uid не из `/etc/passwd`** → `HOME=/`. Поэтому в
  entrypoint HOME ставится ПОСЛЕ gosu (`gosu uid:gid env HOME=/home/afm afm …`).
- **`:ro` single-file bind + атомарный rename = corruption.** Не монтируй `:ro`
  то, что приложение перезаписывает (claude + `~/.claude.json`).
- **`os.ModeCharDevice` ≢ TTY** (`/dev/null` тоже char device) — используй
  `golang.org/x/term.IsTerminal`, иначе `docker run` падает «input device is not a
  TTY».

## Docker Project File Browser

Docker-only, строго **read-only** файл-браузер в дашборде: просмотр монтирования
проекта + разрешённых extra-mount'ов, подсветка, per-file `HEAD → worktree` diff,
вставка `[AFM file: "<container-path>"]` в комментарии. На host-ране полностью
инертен (нет манифеста → `capabilities.file_browser=false` → `/api/files/*` → 404).

- **`docker.file_browser.enabled` (`*bool`, дефолт `true`).** `IsEnabled()`:
  **env `AFM_FILE_BROWSER` > config > default true**. Читается host-side в
  `cmd/afm/run_environment.go` (решает, форвардить ли манифест). `mergeFile` обязан копировать
  `Docker.FileBrowser.Enabled`, иначе project-слой теряется.
- **`docker.extra_mounts` — scalar-or-object** (`config.ExtraMount{Path, Name,
  Browse}`): legacy-строка → `browse:false` (приватный), только `browse:true`
  browseable (безопасный дефолт — апгрейд не раскроет credential-mount).
- **Манифест разрешённых корней строит хост-лаунчер.** `BuildFileRootManifest`
  (`pkg/docker/manifest.go`) — versioned `FileRootManifest` (project root всегда +
  `extra-N` на каждый `browse:true`, CONTAINER-путь; **project root
  `MountReadOnly:false`, extra-N — `true`**). Передаётся base64 в
  `-e AFM_DOCKER_FILE_ROOTS` (`FileRootsEnvVar`) только при `FileBrowserEnabled &&
  len(Roots)>0`. In-container декодится (gated `AFM_IN_DOCKER=1`) → `workspace.New`.
- **`resolveMountPath` — единый резолвер путей**, переиспользуемый `-v`-петлёй И
  манифестом (`containerPathFor`): `~`→`expandHome`, relative→join с projectRoot,
  absolute→as-is.
- **Loopback bind при включённом браузере:** `-p 127.0.0.1:<port>:<port>` (не
  `0.0.0.0`) — arbitrary-file GET API не должно быть LAN-доступно. Т.к. браузер
  по дефолту ON, это дефолт для Docker-ранов.

### `pkg/server/workspace` — безопасная ФС (security boundary)

Самодостаточный пакет. `validateRelPath` (`roots.go`) — единственный gate ввода:
чекает сырой путь на `..` ДО `path.Clean`; отвергает absolute/NUL; `.git`/`.afm` →
`ErrNotFound`. Каждый метод идёт через `resolve(rootID, relPath)`.

- **Linux `openat2`, без check-then-open** (`access_linux.go`): каждый корень —
  `O_DIRECTORY|O_PATH` fd; дети через `Openat2` с
  `RESOLVE_BENEATH|RESOLVE_NO_MAGICLINKS|RESOLVE_NO_SYMLINKS`. `New` пробит
  `openat2` один раз; на `ENOSYS` (kernel < 5.6) или не-Linux → **ноль корней**
  (capability off), не слабый fallback. Ядро обеспечивает containment.
- **List/Read/Reference/Diff:** `Read`/`Reference` открывают `O_RDONLY|O_NONBLOCK`
  и требуют `Mode().IsRegular()` (FIFO иначе заблокировал бы горутину); `Read`
  кап `maxContentBytes = 2<<20` (2 MiB) → `ErrTooLarge`, NUL/`!utf8.Valid` →
  `ErrBinary`, ставит `ETag`. `Diff` (`diff.go`) читает git-объекты только из репо
  ВНУТРИ корня (`--git-dir` верифицируется, gitfile `gitdir:` обязан жить внутри
  корня), `Read` идёт ПЕРВЫМ (валидирует путь), oversize-baseline (`cat-file -s`)
  пропускает `udiff.Unified` (без OOM), 3s timeout, HEAD blob.
- **Linux-tagged тесты workspace гоняются только в Docker/CI**, не на macOS —
  проверяй `GOOS=linux go build/vet/test -c ./pkg/server/workspace/`.

### HTTP `/api/files/*` (GET-only, JSON-ошибки)

`routeFiles` (`files_handlers.go`, `mux.HandleFunc("/api/files/")`): не-GET или
nil/zero-root workspace → `404`; `X-Content-Type-Options: nosniff`; диспатчит
`roots`/`tree`/`reference`/`content`/`diff`. Ошибки через `writeFilesError` →
`{"error":<code>}` (единственное отклонение от plain-text-конвенции сервера);
`filesErrStatus` мапит sentinel'ы (400/404/409/413/415/422/500). Тела ошибок
никогда не несут абс. путь / raw git stderr; `filesRoots` отдаёт `RootView` без
`Path`. `content` поддерживает `If-None-Match` → `304`.

### Dashboard file-browser

`components/file-browser/*` + `FileBrowserProvider` (`openBrowser`/`pickFiles`).
`FileViewer` — единственный `dangerouslySetInnerHTML`, кормится только `highlight()`
(`highlight.js/lib/core`, `escapeHtml` для plain) — исходник не идёт в Markdown
(no XSS). Capability гейтит весь UI централизованно (`enabled`); host-ран не делает
`/api/files/*`-вызовов. Вставка `[AFM file: "<абс. container path>"]` едет по
существующим `revise`/`dialog-answer` цепочкам → `feedback.md`/`answer.json`
(агент читает путь своими тулами; абс. путь — резолвится независимо от `root_dir`).
Тема-агностично: `skins/base/file-browser.css` токенизирован.

## Dashboard shell — UX redesign (single-workspace + graphite)

Дашборд переработан из трёхколоночного в **stage-rail + один workspace с табами**.
Поведение (хуки, API, FSM) не менялось — presentation-only. Shipped v1.0.0.

- **Дефолтная тема `graphite`** (`config.ThemeGraphite`, заменила `coffee`;
  `theme: coffee` — legacy-алиас с deprecation-warning). Дефолт закодирован в трёх
  связанных местах, которые меняются вместе: HTML-литерал (`index.html`/
  `index.dev.html`), `config.ThemeGraphite`, replace-anchor
  (`pkg/server/server.go`).
- **Семантический token-слой** (`skins/base/tokens.css`, импортится первым):
  `--surface-*`/`--text-*`/`--accent-primary`/`--attention`/`--success`/`--danger`
  и т.д. Новые компоненты используют ТОЛЬКО их (тема-агностика бесплатно).
- **Композиция (`App.tsx`):** `GlobalHeader` (бренд + имя флоу; `RunMetrics` —
  Started/Elapsed/Idle/Backoff **+ Est. cost**; Files/theme/notifications/
  connection) → `ReviewBanner` → `DashboardShell` (`StagesList` rail +
  `.workspace`). Top-level — flex column (`#root` flex column, `#main` flex:1),
  чтобы баннер поглощался без page-scroll.
- **Rail resizable** (`--rail-width`, clamp `[190,560]`, `localStorage`
  `afm.railWidth`); ниже 900px — slide-over drawer (`inert` когда закрыт, focus-
  management).
- **`useWorkspaceView` reducer — source of truth** (`hooks/use-workspace-view/`).
  `WorkspaceView` = `feed | cost | full-feed | attention | plan-history |
  dialog-history`. `App` зовёт его с `suppressed`-сигналом (editable/overlay
  открыт → новое поступление только светится, не крадёт фокус). `AttentionItem`
  несёт `{stageId, kind, episode=updatedAt}` — повторный attention той же
  стадии/kind авто-открывается.
- **Табы:** Feed + контекстный `detail`-таб (отражает реальное тело workspace) +
  отдельный glowing `beacon` (неоткрытый attention). `WorkspaceHeader`'s
  `attentionNav` prev/next; Plan|Dialog-переключатель в history-view. Title-flash/
  favicon/header-dot трекают ГЛОБАЛЬНЫЙ attention.
- **Feed (`components/feed-workspace/`):** messenger-вид реального лога событий
  (`feed-view-model.ts` мапит event → `FeedItem`). `agent_action tool="text"` —
  проза агента; прочие тулы — compact mono-строки; `groupFeedItems` мёржит
  подряд идущие. Scope «This stage | All» — отдельный `full-feed` workspace-view.
  Старый Feed/Log-toggle и raw-log удалены (`useStageLog`/`LogEntry`/
  `GET /api/stages/{id}/log`/`log-panel.css`/`event-feed.css` — нет).
- **Input + вложения — общий `PasteableTextarea`** (line-комментарии plan/question,
  custom dialog answer, `AgentNoteModal`): `Cmd/Ctrl+Enter` submit; paperclip-меню
  «Choose project file…» (gated на file-browser capability) / «Upload image…»
  (всегда, upload работает и в host-режиме). `useImagePaste` защищает от
  stage-change гонки через `stageId`-generation.

## Agent memory (directory store + pattern-extraction chain) — v3

Каждая стадия — изолированный контекст; что агент узнал, испаряется на завершении.
Agent memory (opt-in) после завершения стадии дистиллирует сессию в набор
**project patterns** (Markdown-правила). Пайплайн вынесен в `pkg/memorypipeline`,
общий для live-пути и офлайн-команды `afm memory rebuild`.

- **Включение: `memory.path` — КАТАЛОГ, не файл.** `flow.MemoryConfig{Path, Mode,
  MemoryUse, MaxRules, Commit}` (`pkg/flow/flow.go`): `path` (относительно
  `root_dir`, непустой = включает фичу); `mode` (`r`/`w`/`rw`, дефолт `rw`);
  `memory_use` (глобальный, **дефолт false** — participation-gate чтения);
  `max_rules` (дефолт 25); `commit` (дефолт false). `cmd/afm/run.go` резолвит абс.
  `MemoryDir` → `Options.MemoryDir` (`""` = выключено). Общий файл всегда
  `<MemoryDir>/memory.md` (`memory.ProjectFile`).
- **Per-stage opt-in — `Stage.Reflect *Reflect{File, Mode}`.** `File` резолвится
  относительно `MemoryDir` (`memory.StageFile`); `Mode` (`r`/`w`/`rw`, дефолт
  `rw`), `CanRead()`/`CanWrite()`. `Flow.validate()` отвергает `reflect` при пустом
  `memory.path`, пустом `file`, невалидном `mode`. Script-стадия: write-цепочка
  тихо пропускается, `r`-инъекция работает.
- **Пять шагов дистилляции** (`memorypipeline.Pipeline.DistillTarget`, `distill.go`):
  1. **reflect** — свежий агент читает сессию стадии (`<phase>.log`+summary/plan
     ПЛЮС прямой user-input: `*.dialog.jsonl`-ответы, `prenote.md`/`feedback.md`) и
     пишет RL-датасет `<stageDir>/reflect_dataset.yaml` (`project_level`/
     `session_level`, items `{prompt, chosen, rejected, source}`). **User-input
     приоритетнее логов** (тег `source: user`); provenance едет через цепочку
     (`aggregate` префиксит `[user]`, `prioritize` шлёт `[user]` в High). Hard
     EXCLUDE в промпте: формат `execution_summary.md`, `$AFM_STAGE_DIR`, механика
     dialog/approval/retry — чтобы память была project-specific, не framework-шум.
  2. **aggregate** → нумерованный `name — description` в `<logDir>/patterns.md`.
  3. **prioritize** → High/Medium/Low в `<logDir>/prioritized.md` (сильный биас:
     `[user]` → High), снимает `[user]`-маркер.
  4. **select-High (код, без LLM)** — `memory.SelectHigh(prioritized)`
     (`pkg/memory/selecthigh.go`) → `<logDir>/high.md`. Пусто → цепочка стоп.
  5. **update** — свежий агент мёржит High в целевой файл, ре-тирит, кап
     `max_rules` (дропает Low, затем Medium), формат `# Project rules` / `##
     [Pattern Name]` (приоритет — только порядком блоков).
- **Двухфазный движок.** `Pipeline.CaptureStage`/`CaptureAll` — фаза 1 (только
  шаг reflect, БЕЗ memory-lock, параллелится). `Pipeline.Finalize` — фаза 2 (шаги
  2–5 + промоушен), **вызывающий обязан держать memory-lock**. Агент-раннер —
  `memorypipeline.AgentRunner` (тип в `types.go`), прод — `NewExecRunner`
  (`executor.go`), инъекция через `WithRunner`; kind-константы `KindReflect`/
  `KindAggregate`/`KindPrioritize`/`KindUpdate`; промпты —
  `memorypipeline.BuildPrompt(Prompts, AgentSpec)` (`prompts.go`). Изоляция агента:
  свежая сессия, без `AFM_STAGE_DIR`, через `SpawnDetached` (вне `max_parallel`
  семафора).
- **End-of-run строит шаги 2–5** (`(*Orchestrator).runEndOfRunMemory`,
  `reflection.go`; `o.mem *memorypipeline.Pipeline`): (1) per-stage reflect-файлы
  (gated `reflect.mode`), (2) общий `memory.md` (если `memory.CanWriteProject()`).
  Шаг reflect тут НЕ повторяется (датасеты собраны per-stage во время рана).
  **Следствие:** память строится один раз в конце — reflect ранней стадии доходит
  до поздней только на СЛЕДУЮЩЕМ ране (инъекцией), не внутри текущего. При
  `commit: true` — `memory.Commit(MemoryDir, msg)` (`commit.go`, add + `-- .`-
  scoped commit, без push).
- **Инъекция (read side) — opt-in, два гейта.** `memoryBlockForStage`
  (`memory_inject.go`, wired как `prompts.Inputs.MemoryBlock`): participation-gate
  `Memory.UseFor(s.MemoryUse)` (per-stage override, иначе глобальный `memory_use`,
  дефолт false) → `""`; иначе называет `memory.md` (если `CanReadProject()` +
  файл есть) и/или stage-файл (если `Reflect.CanRead()`). Две оси mode:
  `memory.mode` для общего файла, `reflect.mode` для своего; `memory_use` — мастер-
  свитч чтения (запись независима).
- **`pkg/memory`** (маленький, без LLM): `paths.go` (`ProjectFile`/`StageFile`/
  `AtomicWrite` temp+rename), `selecthigh.go` (`SelectHigh`), `commit.go`
  (`Commit`). v2-типы (`Finding`/`Store`/`Reconcile`/`Evict`) удалены.
- **Best-effort, не трогает FSM.** Сбой шага прерывает цепочку для стадии,
  `reflect_failed` notice live+durable; статус стадии не меняется.

### `afm memory rebuild` — офлайн backfill

`afm memory rebuild` (`cmd/afm/memory_rebuild.go`) пересобирает память из логов
завершённого рана без нового рана и без правки его FSM/лога. Использует тот же
`pkg/memorypipeline`.

- **Attempt-workspace + manifest + staged promotion — офлайн-транзакция.** Каждый
  вызов — свежий каталог `<runDir>/memory-rebuild/<attempt-id>/`
  (`NewUniqueAttemptDir`) с staged-датасетами, артефактами дистилляции и
  `manifest.json` (`Manifest`/`OperationMeta`: run id, SHA флоу/промптов, исход).
  Статус идёт по фикс-lifecycle; `FinalizeManifestOnExit` (defer) закрывает
  застрявший в `failed`/`cancelled`. Промоушен (запись реальных целевых файлов)
  только после успешной дистилляции ВСЕХ таргетов; `--dry-run` гоняет всё, но не
  пишет.
- **Общий memory-lock — обязателен для любого писателя.**
  `memorypipeline.AcquireMemoryLock(ctx, memoryDir)` (`lock.go`) — flock на
  ФИКС-пути от контента каталога: `MemoryLockPath` =
  `~/.afm/locks/memory-<sha256 канонич. абс. пути>.lock` (ВНЕ memory-каталога —
  он может ещё не существовать). `canonicalizeExistingAncestor` резолвит symlink'и
  до глубочайшего существующего предка (два спелллинга одного каталога → один
  lock). **Порядок везде: run-lock → memory-lock** (никогда наоборот).
  `AcquireMemoryLock` поллит 100ms и БЛОКИРУЕТСЯ на contention
  (`progress.ErrLockBusy`) — ожидание нормально.
- **CLI-окно lock'а:** `rebuildHandler` зовёт `CaptureAll` (фаза 1) ДО lock'а,
  затем `AcquireMemoryLock`, ре-preflight (`commitPreflight` — git-состояние могло
  измениться), `Finalize`, `maybeCommit`; lock освобождается defer'ом. Окно
  эксклюзива = finalize+commit, не capture.
- **Strict (CLI) vs best-effort (live) — сознательная асимметрия.** Live
  end-of-run best-effort: успех стадии не должен зависеть от записи памяти. `afm
  memory rebuild` — сама операция пользователя, падает ГРОМКО (capture/finalize/
  commit/manifest-ошибка → non-zero exit, manifest в `failed`). Не «чинить»
  rebuild-падение, делая его best-effort — это скроет ровно тот класс проблем,
  ради которого команда существует.
