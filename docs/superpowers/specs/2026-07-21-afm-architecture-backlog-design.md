# afm — архитектурный анализ и приоритизированный бэклог

**Дата:** 2026-07-21
**Статус:** design (одобрен к реализации, старт с Tier 0)
**Рамка:** лёгкий ports-and-adapters (domain core + закрытие текущих утечек границ), без ceremony, в духе «Always Prefer Simplicity».

## Цель

Полный разбор архитектуры afm: где написано «не очень» и что улучшить. Результат — ранжированный по impact/effort бэклог, служащий картой на будущие раунды. Реализация начинается с Tier 0 (верифицированные баги надёжности).

## Метод

Четыре параллельных агента-исследователя прочитали код по зонам: (1) `pkg/orchestrator`, (2) ядро надёжности `pkg/state` + `recovery.go`, (3) периферия `server`/`executor`/`docker`/`cmd`, (4) сквозные аспекты + граф зависимостей. Находки сведены, дедуплицированы, а две высокоприоритетные correctness-находки ядра **верифицированы вручную чтением кода** (помечены ✅ VERIFIED).

## Связь с предыдущим бэклогом

Предыдущий разбор — `docs/superpowers/specs/backlog-architecture.md` (2026-06-10, P2/P3). Частично устарел (проект переименован flowManager→afm, MCP HTTP-сервер удалён). Что оттуда всё ещё актуально: декомпозиция `orchestrator.go` (P2) — **не сделана, файл вырос 1222→1632**; `RetryPolicy` как интерфейс; property-based тесты FSM. Крупные P3-идеи (actor-per-stage, полный event sourcing, SQLite, concurrent flows, метрики) остаются отложенными — см. раздел «Вне охвата».

---

## Общий вердикт

Архитектура **здоровая**. Граф внутримодульных зависимостей — чистый ацикличный DAG (подтверждено `go list -deps`):

```
Листья:   flow, mcp, progress, web, config, assets
Уровень1: state→progress, executor→progress, prompts→flow, docker→config/flow
Хаб:      orchestrator (config, executor, flow, mcp, prompts, state)
Верх:     server→orchestrator, cmd/afm→всё
```

`orchestrator` — единственный оркестрирующий хаб; `server` строго над ним; низкоуровневые пакеты не тянут высокоуровневые; циклов нет. Ядро надёжности продумано: append-only лог как источник правды, flock между процессами, недеструктивный replay, чистый shutdown, разведение storage-fatal/benign. Тесты плотные (orchestrator: 4757 строк тестов на 2998 исходников).

**Долг структурный, а не архитектурный** — тупиков нет; почти всё лечится извлечением хелперов и единых источников правды. Два сквозных мотива:
1. Нет пакета-носителя доменного словаря → имена фаз/файлов размазаны по 5 пакетам.
2. Несколько перегруженных функций смешивают транспорт + домен + I/O.

## Архитектурная рамка: лёгкий ports-and-adapters

Формальный hexagonal-рефакторинг **отклонён** как избыточный: он не чинит ни один из багов/перегруженных функций, структура уже близка к ports-and-adapters по духу (домен-хаб + I/O по краям: `server`=HTTP-адаптер, `executor`=process-адаптер, `docker`=infra, `state`=persistence; `executor.Runner`/шина уже фактически порты), а ceremony (интерфейсы на каждую границу, DTO-мэппинг, инверсия зависимостей) прямо конфликтует с «убрать > добавить, функция > метод/класс». Big-bang-переписывание рискует стабильным ядром надёжности.

Вместо этого берём **хорошие идеи гексагона точечно, там где окупаются**:
- **Domain core** = словарь фаз/имён/валидаций в листовом пакете (S1). Это «центр гексагона».
- **Закрыть единственную реально текущую границу**: `server` лезет в файловую раскладку run-директории напрямую → читать через методы `state`/`orchestrator` (C1).
- Прочие адаптеры оставить как есть; интерфейсы вводить только где помогает тестам.

---

## Бэклог

Обозначения: Impact/Effort ∈ {High, Medium, Low}. ✅ VERIFIED — подтверждено чтением кода.

### Tier 0 — Корректность (чинить первым)

| ID | Проблема | file:line | I/E |
|----|----------|-----------|-----|
| **B1** ✅ | `afm check` и `afm run` расходятся на порче лога в середине | `state.go:102` vs `store.go:186` | High / Med |
| **B2** ✅ | Потерянный финальный `\n` → лог необратимо портится | `store.go:174,98` | High / Low |

**B1.** `replayEvents` (путь `run`) при битой строке в середине лога → карантин + `ErrCorruptLog`. `LoadRunState` (путь `check`, поиск run) делает `break` на любой ошибке unmarshal и **молча возвращает устаревший префикс**, игнорируя валидные записи после места порчи — без ошибки и предупреждения. Нарушает инвариант «лог — единственная правда»: `check` покажет `running`, а `run` того же лога откажется стартовать. **Фикс:** единый парсер лога (классификация torn-tail / mid-corruption) для обоих путей; `LoadRunState` возвращает тот же `ErrCorruptLog` (или сигнал «неполный префикс»).

**B2.** Для валидной последней строки без `\n` цикл делает `offset += len(line) + 1` безусловно (`store.go:174`) → `goodOffset = len(data)+1`; затем `f.Truncate(lastGoodOffset)` вызывается **безусловно** (`store.go:98`) и **расширяет** файл нулевым байтом → следующий `Apply` пишет после `\0` → на следующем `Open` строка не парсится → карантин. Безопасно-усекаемый torn-write превращается в permanent corruption. Не покрыто тестами (оба torn-tail-теста используют невалидный JSON). **Фикс:** прибавлять `+1` только если строка реально кончалась `\n` (или `min(goodOffset, len(data))`); тест «валидный JSON без финального `\n`».

**Отложено — B3 (`NotifyAnswer` публикует в `context.Background()`, `orchestrator.go:343`).** Правдоподобная (Med/Low) находка: на завершающемся run `Publish(context.Background(), …)` может заблокироваться навсегда при полном буфере — класс «навсегда на мёртвой шине». **Не верифицирована**, поэтому вынесена из первого захода Tier 0. Вернуться к ней отдельно после закрытия B1/B2 (сначала подтвердить репро, потом фикс с run-scoped ctx).

### Tier 1 — Максимальный рычаг, низкий риск

| ID | Проблема | Места | I/E |
|----|----------|-------|-----|
| **S1** | Единый источник правды для доменного словаря (фазы/имена файлов/валидация) | 5 пакетов, 9 файлов | High / Low |
| **S2** | Разнести god-file `orchestrator.go` (1632 строки, 63 функции) | `pkg/orchestrator` | High / Low |
| **S3** | Устранить дублирование autonomous-трека + ручных списков фаз | `orchestrator.go:1096` ≈ `recovery.go:120` | High / Low |

**S1 (консенсус всех 4 агентов).** Имена фаз `planning/implementation/review/autonomous_execution` продублированы: `flow.AgentType` (3, без autonomous), `prompts.Agent` (4), `orchestrator.phase*` (4), `server.phase*` (4), инлайн-литералы `mcp/dialog.go:210`. Валидация «фаза ∈ {четыре}» повторена в `handlers.go:352` и `dialog.go:210`. Правило «фаза → jsonl-файл» (спецкейс `autonomous→autonomous.jsonl`) в 2 местах. Имена артефактов литералами: `plan.md` ×11, `autonomous.flag` ×6, `execution_summary.md`, `feedback.md`, `state.json`, `events.jsonl`, `.lock`. **Фикс:** вынести в листовой `pkg/flow` (уже в основании графа, импортируется без риска цикла): `Phases()`, `IsValidPhase()`, `PhaseLogFile()`, константы `File*`. `prompts.Agent` → алиас `flow.AgentType`. **Важно:** чётко разделить «фазы рантайма» (4) и «агенты в YAML» (3) комментарием — autonomous не должен стать валидным YAML-агентом (`flow.go:14`). Это domain core из арх-рамки. Закрывает ~5 находок.

**S2.** Механическое перемещение функций по файлам одного пакета (компилятор гарантирует корректность, поведение не меняется): `dialog_poller.go` (поллер, relocate, detectViolation), `agents.go` (5 runner'ов), `scheduling.go` (startReadyStages, tryActivatePrePlanned, depsDone, failBlockedStages), `api.go` (Approve/Revise/Retry/NotifyAnswer), `spawn.go`+`sem.go` (spawnAgent, waitAgents, семафор), `runner_factory.go`, `supervisor_track.go`. В `orchestrator.go` остаётся ядро (struct/Options/New/Run/handleEvent/Trigger). _(Уточняет P2 из старого бэклога с учётом новых ответственностей — dialog poller, supervisor track — которых старый план не предвидел.)_

**S3.** Идентичный блок «`DetermineStagePhases` → write `autonomous.flag` → триггеры → `runAutonomousAgent`, иначе `runPlanningAgent`» в `orchestrator.go:1096` и `recovery.go:120`, уже разошёлся (литерал `"autonomous_execution"` vs константа `phaseAutonomous`). **Фикс:** хелпер `startWithSupervisor`. Плюс `dialogPhases(stageDir)` вместо трёх ручных сборок списка фаз (в `retryStage:949` autonomous уже забыт — молчаливый баг).

### Tier 2 — Перегруженные функции

| ID | Функция | file:line | Строк | I/E |
|----|---------|-----------|-------|-----|
| **F1** | `newRunCmd.RunE` — конфиг+docker+wrappers+autoShim+сервер+браузер+запуск | `run.go:39` | ~270 | High / Med |
| **F2** | `retryStage` (4 сценария) + `startPlanningForPending` (два каскадных switch) | `orchestrator.go:925`, `recovery.go:17` | 105+134 | High / Med |
| **F3** | `handleDialogAnswer` — транспорт+домен+I/O | `handlers.go:337` | 145 | Med / Med |
| **F4** | `ReExec` — 6 концернов сборки docker-args | `launcher.go:203` | ~110 | Med / Med |
| **F5** | `runWithRetry` — 4 стратегии завершения в одном цикле | `retry.go:78` | 100 | Med / Med |
| **F6** | Boilerplate 5 agent-runner'ов + близнецы `RunPlanning`/`RunAgent` | orchestrator, `executor.go:225,339` | — | Med / Med |

**Фиксы:** F1 → линейный оркестровщик из хелперов (`maybeReExecDocker`, `buildWrappers`, `startDashboard`, `runFlow`). F2 → разбить по целевому статусу (`retryAutonomous/Implementation/Planning`, `resumeRetrying/Running/Planning`); общая тройка `ready→startRun→spawn` в хелпер. F3 → вынести `mcp.WriteAnswer()` (валидация+атомарная запись), хэндлер = декод + маппинг sentinel-ошибок в коды. F4 → чистые билдеры `[]string` (`baseMounts`, `commandMounts`, `extraMounts`, `envArgs`, `autoShimEnv`), юнит-тестируемые без docker. F5 → `classifyOutcome()` → enum, цикл-диспетчер. F6 → `buildStageContext()` + `runLogged()` с колбэками.

### Tier 3 — Границы и консистентность

| ID | Проблема | Места | I/E |
|----|----------|-------|-----|
| **C1** | `server` знает файловую раскладку run-директории (текущая граница ports-and-adapters) | `handlers.go:40,78,102` | Med / Med |
| **C2** | CLI approve/revise/retry — тройной scaffold (lock-семантика ×3) | `cmd/afm/{approve,revise,retry}.go` | Med / Low |
| **C3** | HTTP: boilerplate `extractStageID` в 11 хэндлерах + ручной suffix-роутинг без 405 | `handlers.go`, `server.go:263` | Med / Low-Med |
| **C4** | interactive-rules промпт склеен в Go (остальные — файлы); `tagReplacer` вручную синхронится с тегами `Build()` | `builder.go:62,164` | Med / Med |
| **C5** | Kill процесса, а не группы (утечка детей по таймауту) | `executor.go:527` | Med / Med |
| **C6** | Устаревшие имена: `pkg/mcp` (MCP-сервер удалён), `pkg/progress` мешает Logger+flock | — | Low / Low-Med |

**Фиксы:** C1 → `state.StageIsAutonomous()`, `state.LatestSupervisorDecision()`, единая таблица `phase→logfile` (закрывает и utечку границы из арх-рамки). C2 → хелпер `withStageStore(stageID, fn)` (глагол approve/revise/retry — параметр). C3 → Go 1.22 `ServeMux` (`POST /api/stages/{id}/approve`, `r.PathValue`), даёт корректный 405, убирает ручной suffix-switch и `extractStageID`. C4 → `assets/prompts/interactive_rules.md` через `text/template`; `tagReplacer` — генерировать из единого списка тегов или regex `</?\w+>`. C5 → `Setpgid: true` + kill группы (SIGTERM→grace→SIGKILL). C6 → как минимум исправить doc-комментарий `mcp`; опц. переименовать в `pkg/dialog`, выделить `pkg/lock`.

### Tier 4 — Мелкие упрощения (Low / Low)

- Именованный тип `semaphore interface` вместо анонимного ×3 (`orchestrator.go:104,193,431`).
- Чистые функции оформлены методами `*Orchestrator` — сделать свободными (`correctPhaseForState`, `depsDone`, `hasOpenQuestion`) для точечных тестов.
- `History()` возвращает всегда-nil error → убрать (`store.go:232`, YAGNI).
- `Apply`: инкремент `lastSeq` после успешного `Sync` (commit-after-durable, `store.go:287`).
- Тест-хук `applyHook` — package-global mutex в hot-path → поле Store или убрать (`store.go:265`).
- Избыточный двойной fsync снапшота-кэша на каждый `Apply` → rename без fsync / реже (`store.go:322`).
- `ErrRunLocked` маскирует не-lock ошибки открытия `.lock` → различать EWOULDBLOCK (`store.go:55`).
- `isErrorLine` слишком широкий substring-fallback → ложные ошибки на доброкачественном тексте (`executor.go:144`).
- Docker execFunc-глобал + секреты через `os.Setenv` → поле конфига / локальный env-срез (`launcher.go:114,291`).
- `Revise`/`Retry` возвращают nil при no-op → sentinel `ErrNotApprovable` для корректного 409 (`orchestrator.go:738`).
- Дублирование PATH/WrapperDir-инъекции в executor (разошёлся разделитель PATH) → `resolveCmd()`/`agentEnv()` (`executor.go:405,456`).
- Дублирование autoShim recipe-резолва host vs container → `selectRecipes()` (`run.go:82,174`).
- Непоследовательный формат таймстампа (`progress.go:28,43,51`) и канал логирования (`log`/`os.Stderr`/`fmt.Print`) по проекту → единые константа/конвенция.

---

## Рекомендуемый порядок

1. **Tier 0 (B1, B2)** — по TDD, сначала падающий тест. Бьют по заявленным гарантиям надёжности. Старт здесь. (B3 отложен до подтверждения репро — см. выше.)
2. **S1 (domain core)** — низкий риск, закрывает ~5 находок, готовит почву для остального.
3. **S2 (god-file split)** — механически, снижает когнитивную нагрузку для всех дальнейших правок.
4. **S3** — устранить дубли до того, как они разойдутся дальше.
5. **Tier 2** — инкрементально, под защитой существующих тестов (F2 — осторожно, recovery-путь).
6. **Tier 3/4** — по мере касания соответствующего кода («улучшаем то, в чём работаем»).

## Вне охвата (отложено, см. backlog-architecture.md)

Actor-per-stage, полный event sourcing (time-travel/compaction), SQLite вместо JSON, concurrent flows в одном проекте, Prometheus-метрики, формальный hexagonal. Это крупные направления с пререквизитами; берутся при реальной потребности, не сейчас.
