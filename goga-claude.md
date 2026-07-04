# Plan: `goga-claude`

## Purpose

Этот план — **реверс-инжиниринг as-is**: миграция уже работающего кода `afm` на архитектуру клеточек Goga.
Контракты (16 `CODEMANIFEST`) были получены трассировкой **существующего** рабочего кода (`goga-apply`),
а не написаны под greenfield-реализацию. Поэтому план — это **conformance-verification + test-gap-closure**,
а не построение с нуля:

- **Что должно быть после выполнения.** Каждый из 16 пакетов проверен на соответствие своему
  `CODEMANIFEST` (фасад, форма API, поведение из описаний); все 16 клеточек материализованы,
  `goga lint` чист, `goga schema` строит 16 клеточек без циклов, `go build ./...` и `go test ./...` зелёные.
- **Главный зазор между контрактом и кодом.** После Applied Fixes (4 дефекта в `pkg/orchestrator`) —
  зазоров контракт↔код **не найдено**. Единственный test-coverage gap — отсутствие теста в `pkg/web`
  на embed-split (см. Gap Analysis).
- **Стратегия.** Для каждой клеточки — одна задача проверки конформности (verify-or-gap-fix-in-code),
  высокорисковые точки входа (dialog-протокол, proxy ZAI, Docker privilege-drop, web-split) получают
  полный TDD-вес + отдельные integration-задачи с дословными сценариями из дизайн-документа.

**Семантика TDD-шагов в этом плане (адаптация под реверс-инжиниринг):**
- **STEP 1 CONTRACT TESTS** — проверка доступности фасада + объявленной формы API против `CODEMANIFEST`
  (как правило уже покрыто существующим suite'ом из 36 `_test.go`-файлов; где нет — добавить
  сфокусированный shape-тест, который PASS'ит сразу, т.к. код уже существует);
- **STEP 2 IMPLEMENTATION** — проверить, что существующий код в `location` удовлетворяет контракту;
  если найдено расхождение — править **код** (никогда контракт); после Applied Fixes расхождений нет;
- **STEP 4 LOGIC TESTS** — проверить поведение из описаний контракта (в значительной степени уже
  покрыто — расширять только там, где описанное поведение не протестировано);
- **STEP 5 DEBUGGING** — прогон cell-suite + новых тестов в зелёное; фиксить код, не тесты.

Каждая задача помечает свою природу (`verify/conform` vs `implement/gap`) честно, чтобы ralphex не
тратил циклы на повторную реализацию рабочего кода.

## Context

### Contract Surface

> Каждая клеточка = Go-пакет; фасад = сам пакет (все экспортированные идентификаторы импортируемы).
> `location` — bare-имя файла без пути. Сущности сгруппированы по `location` внутри задач.
> Ниже — компактный реестр «сущность → тип → location» (полные свойства/сигнатуры — в `CODEMANIFEST`,
> который read-only). Достоверные трассировки и алгоритмы переносятся в задачи **дословно** (см. §Code
> Stack Trace / Algorithm Design дизайн-документа).

#### Layer 0 (листья, нет внутренних зависимостей кроме `pkg/web`→`pkg/web/dashboard`)

**`pkg/config`** (`config.go`): `Default` (function), `LoadFrom` (function), `Config`, `ClientConfig`,
`ExecutorConfig`, `ServerConfig`, `ProxyConfig`, `TransformOverrides`, `DockerConfig` (entities).
Imports: нет. Usages: `config_facade.md` (local), `yaml_v3` (project).

**`pkg/flow`** (`flow.go`): `ParseFile` (function), `Flow`, `Stage`, `Artifact`, `Input` (entities).
Imports: нет. Usages: `flow_facade.md` (local), `yaml_v3` (project).

**`pkg/state`** (`state.go` + `store.go`): `FindLatestRunDir`, `SaveFeedback`, `VersionPlan` (functions, `state.go`);
`RunState`, `StageState` (entities, `state.go`); `SetApplyHook`, `Store`, `Transition` (`store.go`).
Imports: нет. Usages: `store_facade.md` (local). **Constraint:** `(*Store).Apply` вне `fsm.go` запрещён
линтером `tools/setstatuslinter`; `cmd/afm` approve/retry/revise — осознанное исключение.

**`pkg/progress`** (`progress.go`): `Lock`, `Logger` (entities). Imports: нет.
Usages: `x_sys_windows` (project, Windows-flock).

**`pkg/mcp`** (`dialog.go`): `AppendQuestion`, `AppendAnswer`, `FindUnansweredQuestions`, `FindEntry`,
`ReadDialog` (functions); `Question`, `Answer`, `Entry`, `QuestionFile` (entities). Imports: нет.
Usages: `dialog_protocol.md` (local). **Высокий риск** — file-based dialog protocol.

**`pkg/proxy`** (`zai.go` + `proxy.go` + `transform.go` + `shim.go`): `BuildTransforms`, `ZAITransform` (`zai.go`);
`Transform` (`transform.go`); `Proxy` (`proxy.go`); `CreateShim` (`shim.go`). Imports: нет.
Usages: `proxy_facade.md` (local). **Высокий риск** — ZAI SSE-реассемблинг.

**`pkg/web`** (`embed.go`): `FS` (function). Imports: `pkg/web/dashboard` (Usages `dashboard_assets`).
Usages: `embed_fs_facade.md` (local). **Высокий риск (тривиальный контракт)** — embed-split после переноса ассетов.

**`pkg/web/dashboard`** (`index.html`): `DashboardAssets` (entity — состав 5 статических ассетов).
Imports: нет. Usages: `dashboard_assets.md` (local).

**`assets`** (`assets.go`): `ReadPrompt` (function); `FS`, `SkillsFS` (functions/entities — embed).
Imports: нет. Usages: `assets_facade.md` (local).

**`tools/setstatuslinter`** (`main.go`): `Analyzer` (entity). Imports: нет. Usages: нет.
Usages: `x_tools_analysis` (project). Независимый `go/analysis`-линтер.

#### Layer 1

**`pkg/prompts`** (`builder.go` + `validator.go`): `Build`, `EscapeTagsForReprompt` (functions, `builder.go`);
`Inputs` (entity, `builder.go`); `ValidatePlan` (function, `validator.go`); `PlanIssues` (entity, `validator.go`).
Imports: `pkg/flow` (`Stage`). Usages: `prompts_facade.md` (local).

**`pkg/docker`** (`launcher.go`): `CheckClaudeDockerAuth`, `ScanCommands`, `SetExecFunc`, `ResetExecFunc`,
`ReExec` (functions); `CommandMount`, `ReExecConfig` (entities). Imports: `pkg/flow` (`Flow`).
Usages: `docker_privilege_drop.md` (local), `x_term` (project). **Высокий риск** — privilege-drop.

**`pkg/executor`** (`executor.go` + `transcript.go` + `runner.go`): `DefaultClaudeArgs`, `ResolveArgs`,
`ParseToolAction`, `WrittenFiles`, `Config` (`executor.go`); `DialogTranscript`, `TranscriptItem`
(`transcript.go`); `Runner` (`runner.go`); mutation `Runner::Executor(cfg Config)` (`executor.go`).
Imports: `pkg/progress` (`Logger`). Usages: `runner_facade.md` (local).

#### Layer 2

**`pkg/orchestrator`** (`errors.go` + `bus.go` + `graph.go` + `context.go` + `fsm.go` + `orchestrator.go`):
`Classify`, `IncompleteWorkError`, `MissingArtifactError`, `MissingSectionsError`, `StorageError` (`errors.go`);
`UIBus`, `CriticalBus`, `Event` (`bus.go`); `Graph` (`graph.go`); `CollectArtifacts`, `CollectDependencyPlans`
(`context.go`); `IsTerminal`, `FSM`, `GuardCtx`, `Rule` (`fsm.go`); `Options`, `Prompts`, `Orchestrator` (`orchestrator.go`).
Imports: `pkg/config` (`Config`), `pkg/executor` (`Runner`), `pkg/flow` (`Artifact`, `Stage`),
`pkg/mcp` (`Question`, `QuestionFile` + Usages `dialog_protocol`), `pkg/prompts` (`Inputs`, `PlanIssues`),
`pkg/state` (`RunState`, `StageState`, `Store`, `Transition`). Usages: `orchestrator_facade.md` (local),
`rapid` (project). **Высокий риск** — FSM + dialog poller/NotifyAnswer.

#### Layer 3

**`pkg/server`** (`server.go` + `handlers.go`): `Config`, `Server` (`server.go`); диалог/HTTP/WS-хендлеры (`handlers.go`).
Imports: `pkg/executor` (`TranscriptItem`), `pkg/mcp` (`Answer`, `Entry`), `pkg/orchestrator` (`Event`, `UIBus`),
`pkg/state` (`Store`), `pkg/web` (`FS`). Usages: `server_facade.md` (local), `gorilla_websocket` (project).
**Высокий риск** — `handleDialogAnswer` O_EXCL.

#### Layer 4

**`cmd/afm`** (`main.go` + `run.go` + `approve.go`/`check.go`/`retry.go`/`revise.go` + `init.go`/`list.go`/`install_skills.go`):
`resolveRootDir`, `fmDir`, `main`, `newRootCmd` (`main.go`); `browserCmd`, `launchHostBrowserOpener`,
`loadPrompts`, `newRunCmd`, `openBrowser`, `resolveFlowPath`, `resolveRun` (`run.go`); `findLatestRunDir`,
`newApproveCmd` (`approve.go`); `lastLogAction`, `newCheckCmd`, `statusColor` (`check.go`); `newRetryCmd` (`retry.go`);
`newReviseCmd` (`revise.go`); `newInitCmd`, `prompt`, `splitComma`, `stageInput` (`init.go`); `newListCmd` (`list.go`);
`installSkills`, `newInstallSkillsCmd`, `resolveSkillsDir` (`install_skills.go`). Imports: `assets`, `pkg/config`,
`pkg/docker` (+ Usages `docker_privilege_drop`), `pkg/flow`, `pkg/orchestrator`, `pkg/proxy`, `pkg/server`, `pkg/state`.
Usages: `cobra` (project). Терминальная вершина графа.

### Re-exports

Явные re-export-блоки (`->Name: {}`) в контрактах отсутствуют — фасад каждой клеточки = сам Go-пакет
(все экспортированные идентификаторы импортируемы по Go-конвенции). `pkg/web` потребляет ассеты
`pkg/web/dashboard` через `Imports.Usages` (`dashboard_assets`), а не re-export.

### Usages Context

Project-level practices (`.goga/usages/cooks/`, 7 файлов):

- **`yaml_v3`** — `gopkg.in/yaml.v3`, YAML-парсинг с опциональностью через `*T`-поле + getter с дефолтом.
  Потребители: `pkg/config` (`LoadFrom`), `pkg/flow` (`ParseFile`, `Input.UnmarshalYAML`).
- **`cobra`** — дерево CLI-команд. Потребитель: `cmd/afm` (`newRootCmd` с persistent `--dir` + `PersistentPreRunE`).
  Как: `&cobra.Command{RunE:…}`, `root.AddCommand(…)`.
- **`gorilla_websocket`** — WebSocket-стриминг. Потребитель: `pkg/server` (`Upgrader` один на пакет,
  `CheckOrigin *`, подписка на `UIBus`). Как: `Upgrader.Upgrade` → `WriteMessage(TextMessage,…)`.
- **`rapid`** — property-based тесты FSM. Потребитель: `pkg/orchestrator` (тестовый контур, НЕ часть контракта).
- **`x_sys_windows`** — файловые блокировки на Windows. Потребитель: `pkg/progress` (`Lock`, `//go:build windows`).
- **`x_term`** — честная TTY-детекция (`term.IsTerminal`). Потребитель: `pkg/docker` (`isTTY`).
- **`x_tools_analysis`** — статический анализатор (`singlechecker.Main`). Потребитель: `tools/setstatuslinter`.

### Imported Usages

- **`dashboard_assets`** ← `pkg/web/dashboard` → потребляется в `pkg/web`. Path: `pkg/web/dashboard/.usages/dashboard_assets.md`.
  Состав 5 статических ассетов (`index.html`, `style.css`, `app.js`, `markdown-it.min.js`, `favicon.svg`) для embed.
- **`dialog_protocol`** ← `pkg/mcp` → потребляется в `pkg/orchestrator`. Path: `pkg/mcp/.usages/dialog_protocol.md`.
  Полный контракт вопрос/ответ; **критический путь O_EXCL в `pkg/server`, а не в `mcp.AppendAnswer`**.
- **`docker_privilege_drop`** ← `pkg/docker` → потребляется в `cmd/afm`. Path: `pkg/docker/.usages/docker_privilege_drop.md`.
  Механизм gosu/HOME-after/uid-drop.

### Local Usages

Все 13 cell-level `.usages/`-файлов созданы стадией `goga-apply` и свёрены с оттрассированными
контрактами — состав API потребителя не менялся (as-is), дополнений не требуется. Создание/обновление
в этом плане **не требуется** (status: current). Ни одна клеточка не ссылается в `CODEMANIFEST Usages`
на собственный `.usages/`. Файлы:

- `pkg/web/dashboard/.usages/dashboard_assets.md`, `pkg/mcp/.usages/dialog_protocol.md`,
  `pkg/docker/.usages/docker_privilege_drop.md` (imported, выше);
- `pkg/config/.usages/config_facade.md`, `pkg/flow/.usages/flow_facade.md`, `pkg/state/.usages/store_facade.md`,
  `pkg/web/.usages/embed_fs_facade.md`, `pkg/executor/.usages/runner_facade.md`, `pkg/prompts/.usages/prompts_facade.md`,
  `pkg/orchestrator/.usages/orchestrator_facade.md`, `pkg/proxy/.usages/proxy_facade.md`,
  `pkg/server/.usages/server_facade.md`, `assets/.usages/assets_facade.md` (facade-документация).

### External Dependencies

- `gopkg.in/yaml.v3`, `github.com/gorilla/websocket`, `github.com/spf13/cobra`, `pgregory.net/rapid`,
  `golang.org/x/sys/windows`, `golang.org/x/term`, `golang.org/x/tools/go/analysis` (см. `go.mod`, `go 1.26`).
- Инструменты: `goga` (`$HOME/.local/bin/goga`), компилятор Go 1.26, линтер `tools/setstatuslinter` (vendored).

## Facts

- **Reverse-engineering / no-behavior-change**: 16 `CODEMANIFEST` получены из работающего кода; реализации
  уже существуют. Любая правка `.go` — только если контракт↔код расходятся (после Applied Fixes расхождений нет).
- **go 1.26** в `go.mod` — версия НЕ меняется (правило global CLAUDE.md).
- `goga lint` → `cells: 16 errors: 0`; `goga schema` → 16 клеточек, 0 циклов (после Applied Fixes).
- `go build ./...` exit 0; `go vet ./...` exit 0.
- **36 `_test.go`-файлов** уже существуют (orchestrator 14, cmd/afm 5, server 3, proxy 3, state 2, prompts 2,
  executor 2, config/flow/progress/mcp/docker по 1; `pkg/web`/`pkg/web/dashboard`/`assets`/`tools/setstatuslinter` — 0).
- Перенос ассетов `pkg/web/*` → `pkg/web/dashboard/*` + `//go:embed dashboard/*` **уже выполнен** (`goga-apply`);
  повторно НЕ издаётся. `go build ./...` подтверждает, что embed резолвится.
- **Applied Fixes** (4 дефекта `pkg/orchestrator`, найдены живым `goga lint`): устранена коллизия `Config`
  (импорт `config.Config` оставлен, `executor.Config AS ExecutorConfig` удалён, ребро сохранено через `Runner`);
  `StageStatus` удалён из импорта (enum-конвенция — значения в annotations), ребро сохранено через
  `Store`/`RunState`/`StageState`/`Transition`; `Agent` удалён из импорта (enum-конвенция), ребро сохранено через
  `Inputs`/`PlanIssues`. После — `goga lint` чист.
- Большинство сценариев дизайн-документа **уже покрыты** существующими тестами
  (`TestReadDialog_AnswerBeforeQuestion`, `TestHandleDialogAnswer_DuplicateAnswer`/`_InvalidID`/`_WritesAnswerFile`,
  `TestZAITransform_ConvertNonStreaming`/`_UpstreamNon200_ForwardedAsIs`, `TestReExec_BuildsDockerArgs`/`_PassthroughEnv`,
  `TestFullDialogCycle`, `TestPollQuestions_DetectsNewQuestion`/`_Idempotent`, и т.д.).
- **Enum-конвенция**: строковые enum'ы (`StageStatus`, `EventType`, `FSMEvent`, `Agent`) документируются как
  значения в annotations потребителя, НЕ как top-level типы и НЕ как `Imports.Types`.
- 3 нюанса CLAUDE.md сохранены в annotations (проверено): file-based dialog protocol (`pkg/mcp`+`pkg/orchestrator`),
  reverse-proxy ZAI transform (`pkg/proxy`+`cmd/afm`), Docker privilege-drop (`pkg/docker`).

## Gap Analysis

- **Missing contract entities**: нет — все 16 клеточек материализованы, `goga schema` строит все.
- **Missing facade exposure**: нет — все экспортированные идентификаторы импортируемы (`go build`/`go vet` зелёные).
- **Incorrect `location` placement**: нет — `WrittenFiles` уже `executor.go` (фикс `brainstorm-review` применён);
  свёрено по реестру выше.
- **API mismatches**: нет — после Applied Fixes сигнатуры совпадают с реальным Go API.
- **Behavioral mismatches**: нет — трассировки дизайн-документа совпали с исходником на всех чекпойнтах.
- **Existing code that can be reused**: весь — это реверс-инжиниринг as-is; suite из 36 файлов переиспользуется.
- **Test coverage gaps**:
  - **`pkg/web` (0 тестов)** — `TestWeb_Embed_ServesOriginalWebPaths_AfterDirMove` (регресс на слом embed после
    переноса ассетов в `pkg/web/dashboard/`) **отсутствует** → задача Task 12 добавляет его (гenuine gap).
  - Остальные 7 сценариев дизайн-документа имеют эквиваленты в существующем suite → задачи их
    верифицируют+документируют (где имя не совпадает дословно — задача сверяет поведение и при желании
    добавляет алиас-тест с каноническим именем).
- **Missing visibility in workspace/git**: нет — все `CODEMANIFEST`/`.usages/` в git (`.afm/runs` вне scope).

---

## Tasks

> **Правило порядка пакетов**: задачи каждой клеточки выполняются до начала следующей; внутри клетки —
> infrastructure/facade → сущности по `location` → integration-тесты. Внутри каждой coding-задачи —
> TDD (contract tests → verify/implementation → verification → logic tests → debugging → re-verification → lint).
> Все задачи — conformance-verification (код существует); STEP 2 = «проверить соответствие, фиксить код при зазоре».
> Только ОДНА задача активна за ralphex-итерацию.

### Task 1: `pkg/config` — conformance (config.go)  [verify/conform]

Проверка конформности клеточки `pkg/config` контракту. Сущности (`config.go`): `Default` (function),
`LoadFrom(globalDir, projectDir) -> (Config, error)` (function), `Config`, `ClientConfig`, `ExecutorConfig`,
`ServerConfig`, `ProxyConfig`, `TransformOverrides`, `DockerConfig` (entities). Imports: нет.
Семантика (из дизайн-дока): `LoadFrom` читает `~/.afm/config.yaml` и `.afm/config.yaml` (`yaml_v3`), отсутствующие
молча игнорируются, мердж; опциональность — указатель + геттер с дефолтом (`ServerConfig.GetPort`→9876,
`IsOpenBrowser`→true, `ProxyConfig.IsEnabled`→true при nil, `DockerConfig.IsDockerEnabled`→по env).

**Usages relevant to this task:**
- `yaml_v3`: `yaml.Unmarshal` в struct с `*T`-полями + getter-метод с дефолтом; стандарт `gopkg.in/yaml.v3`.
- `config_facade.md`: фасад потребителя (как вызывать `LoadFrom`/`Default`).

**CRITICAL: `CODEMANIFEST` files — read-only contract definitions. Do NOT modify them. If implementation does not match the contract, fix the implementation — never fix the contract.**

- [ ] **STEP 0 (DECLARATION)**: объявить работу над Task 1 (`pkg/config` conformance).
- [ ] **STEP 1 (CONTRACT TESTS)**: верифицировать/добавить contract-тесты на доступность фасада (`config.Default`,
  `config.LoadFrom`, типы `Config`/`ClientConfig`/… доступны из `pkg/config`) и форму API (сигнатуры).
  Покрыто `pkg/config/config_test.go` — сверить, дополнить shape-тест только при зазоре (ожидается PASS — код существует).
- [ ] **STEP 2 (IMPLEMENTATION)**: проверить, что `pkg/config/config.go` удовлетворяет контракту (опциональность через
  указатель+геттер, мердж global+project, дефолты). Зазоров после Applied Fixes нет; если найдено — править **код**.
- [ ] **STEP 3 (INTERFACE VERIFICATION)**: `go test ./pkg/config/...` — все тесты зелёные.
- [ ] **STEP 4 (LOGIC TESTS)**: верифицировать поведение: дефолты (`GetPort`→9876 и т.д.), `IsEnabled` nil→true,
  мердж (project переопределяет global). Покрыто существующим suite; расширить только при непокрытом поведении.
- [ ] **STEP 5 (DEBUGGING)**: `go test ./pkg/config/...` — фиксить код (не тесты) до зелёного.
- [ ] **STEP 6 (CONTRACT RE-VERIFICATION)**: фасад, форма API, поведение — соответствуют `CODEMANIFEST`.
- [ ] **STEP 7 (LINT)**: `go vet ./pkg/config/...` — чисто; `goga lint` остаётся `cells: 16 errors: 0`.
- [ ] **STEP 8 (COMPLETION)**: отметить чекбоксы выполненными.

### Task 2: `pkg/flow` — conformance (flow.go)  [verify/conform]

Проверка конформности `pkg/flow`. Сущности (`flow.go`): `ParseFile(path) -> (Flow, error)` (function);
`Flow`, `Stage`, `Artifact`, `Input` (entities). Imports: нет. Семантика: `yaml_v3`-парсинг `flow.yaml` → `Flow{Stages}`;
`Input.UnmarshalYAML` — из строки `"stage.artifact"` или объекта `{ref,optional}`; `Stage.HasAgent`/`ImplAgent`/`NeedsPlanning`
— selection-логика агентов.

**Usages relevant to this task:**
- `yaml_v3`: `yaml.Unmarshal` для `Flow`; кастомный `UnmarshalYAML` на `Input`.
- `flow_facade.md`: фасад потребителя.

**CRITICAL: `CODEMANIFEST` files — read-only contract definitions. Do NOT modify them. If implementation does not match the contract, fix the implementation — never fix the contract.**

- [ ] **STEP 0 (DECLARATION)**: Task 2 (`pkg/flow` conformance).
- [ ] **STEP 1 (CONTRACT TESTS)**: contract-тесты на фасад (`flow.ParseFile`, `Flow`, `Stage`, `Artifact`, `Input`)
  и сигнатуры. Покрыто `pkg/flow/flow_test.go`.
- [ ] **STEP 2 (IMPLEMENTATION)**: проверить `pkg/flow/flow.go` (парсинг, `Input.UnmarshalYAML` две формы,
  `Stage.HasAgent`/`ImplAgent`/`NeedsPlanning`). Зазоров нет; править код при находке.
- [ ] **STEP 3 (INTERFACE VERIFICATION)**: `go test ./pkg/flow/...` — зелёные.
- [ ] **STEP 4 (LOGIC TESTS)**: поведение — парсинг `flow.yaml`, обе формы `Input`, selection-методы `Stage`.
  Покрыто suite; расширить при непокрытом.
- [ ] **STEP 5 (DEBUGGING)**: `go test ./pkg/flow/...` — фиксить код до зелёного.
- [ ] **STEP 6 (CONTRACT RE-VERIFICATION)**: соответствие `CODEMANIFEST`.
- [ ] **STEP 7 (LINT)**: `go vet ./pkg/flow/...`; `goga lint` 16/0.
- [ ] **STEP 8 (COMPLETION)**: отметить чекбоксы.

### Task 3: `pkg/state` — conformance (state.go + store.go)  [verify/conform]

Проверка конформности `pkg/state`. Сущности: `FindLatestRunDir`, `SaveFeedback`, `VersionPlan` (functions, `state.go`);
`RunState`, `StageState` (entities, `state.go`); `SetApplyHook`, `Store`, `Transition` (`store.go`). Imports: нет.
Семантика: `Store.Open` создаёт `state.json`+event log; `Apply(t Transition)` — fsync+перезапись снапшота
(через `SetApplyHook` между fsync и записью — тестовый хук); `Snapshot`→`RunState`. **Constraint:**
`(*Store).Apply` внутри `pkg/orchestrator` — только из `fsm.go` (статически проверяется `tools/setstatuslinter`);
`cmd/afm` approve/retry/revise — осознанное исключение (CLI-мутации без живого Orchestrator).

**Usages relevant to this task:**
- `store_facade.md`: фасад потребителя, включая FSM-only `Apply` constraint.

**CRITICAL: `CODEMANIFEST` files — read-only contract definitions. Do NOT modify them. If implementation does not match the contract, fix the implementation — never fix the contract.**

- [ ] **STEP 0 (DECLARATION)**: Task 3 (`pkg/state` conformance).
- [ ] **STEP 1 (CONTRACT TESTS)**: contract-тесты на фасад (`state.Store`, `state.Transition`, `state.RunState`,
  `state.StageState`, `state.FindLatestRunDir`, `state.SaveFeedback`, `state.VersionPlan`, `state.SetApplyHook`).
  Покрыто `pkg/state/store_test.go` (и др.).
- [ ] **STEP 2 (IMPLEMENTATION)**: проверить `store.go`/`state.go` — fsync+snapshot, event log, `SetApplyHook`
  между fsync и записью, `FindLatestRunDir(base, flowName)`. Зазоров нет; править код при находке.
- [ ] **STEP 3 (INTERFACE VERIFICATION)**: `go test ./pkg/state/...` — зелёные.
- [ ] **STEP 4 (LOGIC TESTS)**: поведение — replay events, crash-after-fsync recovery, reject wrong `from`,
  `FindLatestRunDir` not-found, `VersionPlan`. Покрыто suite (`TestApply_CrashAfterFsync_Recovers`, `TestOpen_ReplaysExistingEvents`, …).
- [ ] **STEP 5 (DEBUGGING)**: `go test ./pkg/state/...` — фиксить код до зелёного.
- [ ] **STEP 6 (CONTRACT RE-VERIFICATION)**: соответствие `CODEMANIFEST` (включая FSM-only `Apply` constraint).
- [ ] **STEP 7 (LINT)**: `go vet ./pkg/state/...`; `goga lint` 16/0.
- [ ] **STEP 8 (COMPLETION)**: отметить чекбоксы.

### Task 4: `pkg/progress` — conformance (progress.go)  [verify/conform]

Проверка конформности `pkg/progress`. Сущности (`progress.go`): `Lock`, `Logger` (entities). Imports: нет.
Семантика: `Lock` — платформенно-специфичный flock (`x_sys_windows` на Windows через `windows.LockFileEx`,
`syscall.Flock` на Unix); `Logger` — append-only лог с метками времени + stdout.

**Usages relevant to this task:**
- `x_sys_windows`: `windows.LockFileEx` на Windows (`//go:build windows`); Unix-ветка — `syscall.Flock` stdlib.

**CRITICAL: `CODEMANIFEST` files — read-only contract definitions. Do NOT modify them. If implementation does not match the contract, fix the implementation — never fix the contract.**

- [ ] **STEP 0 (DECLARATION)**: Task 4 (`pkg/progress` conformance).
- [ ] **STEP 1 (CONTRACT TESTS)**: contract-тесты на фасад (`progress.Lock`, `progress.Logger`). Покрыто `pkg/progress/*_test.go`.
- [ ] **STEP 2 (IMPLEMENTATION)**: проверить `progress.go` — flock (платформенные build-tags), append-only лог.
  Зазоров нет; править код при находке.
- [ ] **STEP 3 (INTERFACE VERIFICATION)**: `go test ./pkg/progress/...` — зелёные.
- [ ] **STEP 4 (LOGIC TESTS)**: поведение — блокировка, логирование. Покрыто suite; расширить при непокрытом.
- [ ] **STEP 5 (DEBUGGING)**: `go test ./pkg/progress/...` — фиксить код до зелёного.
- [ ] **STEP 6 (CONTRACT RE-VERIFICATION)**: соответствие `CODEMANIFEST`.
- [ ] **STEP 7 (LINT)**: `go vet ./pkg/progress/...`; `goga lint` 16/0.
- [ ] **STEP 8 (COMPLETION)**: отметить чекбоксы.

### Task 5: `pkg/mcp` — conformance (dialog.go)  [verify/conform]  ⚠ высокий риск — file-based dialog protocol

Проверка конформности `pkg/mcp` (file-based dialog protocol). Сущности (`dialog.go`): `AppendQuestion`,
`AppendAnswer`, `FindUnansweredQuestions`, `FindEntry`, `ReadDialog` (functions); `Question`, `Answer`, `Entry`,
`QuestionFile` (entities). Imports: нет. Usages: `dialog_protocol.md` (local).

**Дословная трассировка (из дизайн-дока §Code Stack Trace — `pkg/mcp`):**
```
mcp.FindUnansweredQuestions(stageDir) → filepath.Glob(*.question.json) → для каждого: разбор <phase>.<id>,
  фильтр фаз (planning/implementation/review), os.Stat парного .answer.json (пропуск если есть) → QuestionFile{Phase,ID,…}.
  Checkpoint (passed): формат имени и фильтр фаз совпадают с isValidDialogID/валидацией фазы в pkg/server/handlers.go.

mcp.ReadDialog(path) → построчное сканирование JSONL → probe {id, answer}: если answer != nil — Answer,
  иначе Question; группировка по ID в хронологическом порядке первого вопроса. Checkpoint (passed, нюанс):
  обработка «ответ пришёл раньше вопроса» (разные горутины пишут .jsonl) — поля вопроса дозаполняются в
  существующий Entry (dialog.go:112-127); отражено в контракте (Entry с опциональным Answer).

mcp.AppendAnswer → appendLine (O_APPEND|O_CREATE, сериализован appendMu). Checkpoint (passed, критический нюанс):
  AppendAnswer НЕ реализует эксклюзивную (O_EXCL) поставку ответа — это best-effort история для UI
  (<phase>.dialog.jsonl). Атомарная O_EXCL-поставка answer.json (критический путь) реализована отдельно,
  напрямую в pkg/server/handlers.go. Контракт фиксирует это разделение.
```

**Usages relevant to this task:**
- `dialog_protocol.md`: полный контракт вопрос/ответ; **O_EXCL `answer.json` — в `pkg/server`, не в `mcp.AppendAnswer`**.

**CRITICAL: `CODEMANIFEST` files — read-only contract definitions. Do NOT modify them. If implementation does not match the contract, fix the implementation — never fix the contract.**

- [ ] **STEP 0 (DECLARATION)**: Task 5 (`pkg/mcp` conformance, dialog protocol).
- [ ] **STEP 1 (CONTRACT TESTS)**: contract-тесты на фасад (`mcp.AppendQuestion`, `mcp.AppendAnswer`,
  `mcp.FindUnansweredQuestions`, `mcp.FindEntry`, `mcp.ReadDialog`, типы `Question`/`Answer`/`Entry`/`QuestionFile`).
  Покрыто `pkg/mcp/dialog_test.go` (`TestAppendAndRead`, `TestFindUnansweredQuestions`, `TestFindEntry`, `TestReadDialog_AnswerBeforeQuestion`, …).
- [ ] **STEP 2 (IMPLEMENTATION)**: проверить `dialog.go` — `appendLine` (O_APPEND|O_CREATE, `appendMu`),
  формат имени `<phase>.<id>.{question,answer}.json`, фильтр фаз, `ReadDialog` group-by-ID + дозаполнение
  «ответ раньше вопроса» (dialog.go:112-127). **Контракт явно фиксирует, что `AppendAnswer` НЕ делает O_EXCL** —
  не «исправлять» это. Зазоров нет; править код при находке.
- [ ] **STEP 3 (INTERFACE VERIFICATION)**: `go test ./pkg/mcp/...` — зелёные.
- [ ] **STEP 4 (LOGIC TESTS)**: поведение — concurrent append (`appendMu`), answer-before-question back-fill,
  find-unanswered (skip paired answer), >1MB error. Покрыто suite (`TestConcurrentAppend`, `TestReadDialog_AnswerBeforeQuestion`).
- [ ] **STEP 5 (DEBUGGING)**: `go test ./pkg/mcp/...` — фиксить код до зелёного.
- [ ] **STEP 6 (CONTRACT RE-VERIFICATION)**: соответствие `CODEMANIFEST` (включая Algorithm-аннотацию dialog protocol).
- [ ] **STEP 7 (LINT)**: `go vet ./pkg/mcp/...`; `goga lint` 16/0.
- [ ] **STEP 8 (COMPLETION)**: отметить чекбоксы.

### Task 6: `pkg/mcp` — integration tests (dialog protocol)  [integration]

Integration-сценарии dialog-протокола, охватывающие `pkg/mcp`. Переносятся **дословно** из дизайн-дока
§Test Stack Trace. Большинство уже покрыто `pkg/mcp/dialog_test.go` — задача верифицирует эквивалентность
и при желании добавляет алиас с каноническим именем.

**Usages relevant to this task:**
- `dialog_protocol.md`: контракт, по которому сверяется поведение.

**CRITICAL: `CODEMANIFEST` files — read-only contract definitions. Do NOT modify them.**

- [ ] **Edge: `TestDialog_AnswerArrivesBeforeQuestion_InJsonl`** (дословно из дизайн-дока):
  - **Setup**: `dialog.jsonl` содержит строку ответа ДО строки вопроса (разные горутины).
  - **Input**: `mcp.ReadDialog(dialogPath)`.
  - **Trace**: probe первой строки → answer → создаёт `Entry{ID}`; вторая строка → question → дозаполняет поля
    вопроса в существующий Entry (не дропает).
  - **Assertions**: `Entry` содержит и вопрос, и ответ, в хронологическом порядке первого вопроса.
  - **Sufficiency**: регресс на потерю вопроса при гонке записи.
  - Эквивалент в suite: `TestReadDialog_AnswerBeforeQuestion` — сверить поведение; при несовпадении имени добавить алиас.
- [ ] Создать/верифицировать `pkg/web/…` — НЕТ (этот task только `pkg/mcp`); оставить mcp-сценарии в `pkg/mcp/dialog_test.go`.
- [ ] **Run validation**: `go test ./pkg/mcp/... -run 'AnswerBeforeQuestion|AppendAndRead|ConcurrentAppend|FindUnansweredQuestions'` — зелёные.
- [ ] **Cross-check**: сверить, что `AppendAnswer` в коде остаётся best-effort (НЕ O_EXCL) — критический нюанс контракта.

### Task 7: `pkg/proxy` — conformance, ZAI transform (zai.go)  [verify/conform]  ⚠ высокий риск — ZAI SSE-реассемблинг

Проверка конформности `pkg/proxy`, часть A (ZAI transform). Сущности (`zai.go`): `BuildTransforms(upstream, zai) -> []Transform`,
`ZAITransform` (entity). Imports: нет. Usages: `proxy_facade.md` (local).

**Дословная трассировка (из дизайн-дока §Code Stack Trace — `pkg/proxy.ZAITransform.ServeHTTP`):**
```
ServeHTTP: io.ReadAll(r.Body) → если non-JSON или streamRequested → passthroughTo(upstream).
  Иначе bj["stream"]=true → http.NewRequestWithContext на upstream+r.URL.RequestURI() → копирование заголовков
  (без content-length) → http.DefaultClient.Do → io.ReadAll(resp.Body). Если статус ≠ 200 → проброс
  статуса+заголовков+тела. Иначе parseSSE(sseBytes): при apiErr → writeSSEError(529); иначе json.Marshal(msg) →
  200 application/json. Checkpoint (passed): BuildTransforms(upstream, *zai) — nil→автоопределение по api.z.ai,
  true→всегда, false→никогда. Checkpoint (known limitation, не дефект): http.DefaultClient без явного таймаута
  (рассчитывает на context); [DONE]-терминатор ожидает \n (Anthropic).
```

**Usages relevant to this task:**
- `proxy_facade.md`: фасад потребителя (как `BuildTransforms`/`Proxy` собираются в `cmd/afm`).

**CRITICAL: `CODEMANIFEST` files — read-only contract definitions. Do NOT modify them. If implementation does not match the contract, fix the implementation — never fix the contract.**

- [ ] **STEP 0 (DECLARATION)**: Task 7 (`pkg/proxy` ZAI transform conformance).
- [ ] **STEP 1 (CONTRACT TESTS)**: contract-тесты на фасад (`proxy.BuildTransforms`, `proxy.ZAITransform`).
  Покрыто `pkg/proxy/zai_test.go` (`TestBuildTransforms_AutoDetect`, `TestBuildTransforms_Override`, `TestZAITransform_Match`, `TestZAITransform_ConvertNonStreaming`, `TestZAITransform_PassthroughStreaming`, `TestZAITransform_EmptySSE_Returns529`, `TestZAITransform_SSEError_Returns529`, `TestZAITransform_UpstreamNon200_ForwardedAsIs`).
- [ ] **STEP 2 (IMPLEMENTATION)**: проверить `zai.go` — инъекция `stream:true`, `parseSSE` (message_start/content_block_*/message_delta),
    проброс non-200, `writeSSEError(529)`, `BuildTransforms` автоопределение по хосту `api.z.ai`. Зазоров нет; править код при находке.
- [ ] **STEP 3 (INTERFACE VERIFICATION)**: `go test ./pkg/proxy/...` — зелёные.
- [ ] **STEP 4 (LOGIC TESTS)**: поведение — non-streaming→stream reassembly, passthrough streaming, empty-SSE→529,
  SSE-error→529, upstream-non-200 passthrough, `Match` по хосту. Покрыто suite.
- [ ] **STEP 5 (DEBUGGING)**: `go test ./pkg/proxy/...` — фиксить код до зелёного.
- [ ] **STEP 6 (CONTRACT RE-VERIFICATION)**: соответствие `CODEMANIFEST` (Algorithm `pkg/proxy` ZAI).
- [ ] **STEP 7 (LINT)**: `go vet ./pkg/proxy/...`; `goga lint` 16/0.
- [ ] **STEP 8 (COMPLETION)**: отметить чекбоксы.

### Task 8: `pkg/proxy` — conformance, proxy infra + shim (proxy.go, transform.go, shim.go)  [verify/conform]

Проверка конформности `pkg/proxy`, часть B. Сущности: `Proxy(upstream, transforms)` (`proxy.go`);
`Transform(match, serveHTTP)` (`transform.go`); `CreateShim(proxyAddr) -> (shimDir, error)` (`shim.go`).
Imports: нет. Семантика: `Proxy.ServeHTTP` диспетчеризует к первому подходящему `Transform` (по `Match`),
без совпадения — `passthroughTo`. `CreateShim` — temp-dir со скриптом `claude`, выставляющим
`ANTHROPIC_BASE_URL=<proxy>` и exec'ающим реальный `claude` (PATH-превосходство shim'а над обёрткой-враппером).

**Usages relevant to this task:**
- `proxy_facade.md`: как `Proxy`/`CreateShim` собираются в `cmd/afm` (shim в `PATH`, env-инъекция).

**CRITICAL: `CODEMANIFEST` files — read-only contract definitions. Do NOT modify them. If implementation does not match the contract, fix the implementation — never fix the contract.**

- [ ] **STEP 0 (DECLARATION)**: Task 8 (`pkg/proxy` proxy infra + shim conformance).
- [ ] **STEP 1 (CONTRACT TESTS)**: contract-тесты на фасад (`proxy.Proxy`, `proxy.Transform`, `proxy.CreateShim`).
  Покрыто `pkg/proxy/proxy_test.go` (`TestProxy_StartShutdown`), `pkg/proxy/shim_test.go` (`TestCreateShim`, `TestCreateShim_NoClaude`).
- [ ] **STEP 2 (IMPLEMENTATION)**: проверить `proxy.go` (диспетчеризация Transform/passthrough), `transform.go`
  (интерфейс `Transform`), `shim.go` (`CreateShim` через `exec.LookPath("claude")`). Зазоров нет; править код при находке.
- [ ] **STEP 3 (INTERFACE VERIFICATION)**: `go test ./pkg/proxy/...` — зелёные.
- [ ] **STEP 4 (LOGIC TESTS)**: поведение — start/shutdown, transform dispatch, shim создаёт скрипт + выставляет env,
  no-claude error. Покрыто suite.
- [ ] **STEP 5 (DEBUGGING)**: `go test ./pkg/proxy/...` — фиксить код до зелёного.
- [ ] **STEP 6 (CONTRACT RE-VERIFICATION)**: соответствие `CODEMANIFEST`.
- [ ] **STEP 7 (LINT)**: `go vet ./pkg/proxy/...`; `goga lint` 16/0.
- [ ] **STEP 8 (COMPLETION)**: отметить чекбоксы.

### Task 9: `pkg/proxy` — integration tests (ZAI scenarios)  [integration]

Integration-сценарии ZAI transform. Переносятся **дословно** из дизайн-дока §Test Stack Trace. Эквиваленты
уже в `pkg/proxy/zai_test.go` — задача верифицирует эквивалентность и добавляет алиасы с каноническими именами при необходимости.

**Usages relevant to this task:**
- `proxy_facade.md`: контракт transform-поведения.

**CRITICAL: `CODEMANIFEST` files — read-only contract definitions. Do NOT modify them.**

- [ ] **Positive: `TestProxy_ZAI_NonStreamingReassembled`** (дословно):
  - **Setup**: upstream-сервер на `api.z.ai/api/anthropic`, отвечает SSE-потоком (`message_start` → `content_block_delta` text → `message_delta` stop_reason=end_turn).
  - **Input**: POST non-streaming JSON (`{"model":"…","messages":[…]}`, без `stream`).
  - **Trace**: `ZAITransform.ServeHTTP → body non-streaming → inject stream:true → forward → read SSE → parseSSE → message{id,role,model,content:[…],stop_reason:end_turn,usage} → 200 application/json`.
  - **Assertions**: ответ — единый Anthropic JSON `message` с `stop_reason:"end_turn"`, текст собран из deltas; исходящий запрос upstream содержал `"stream":true`.
  - **Sufficiency**: ядро workaround'а 529 — регресс на рассыпание SSE.
  - Эквивалент: `TestZAITransform_ConvertNonStreaming` — сверить; добавить алиас при расхождении.
- [ ] **Negative: `TestProxy_UpstreamNon200_PassedThrough`** (дословно):
  - **Setup**: upstream отвечает 500.
  - **Trace**: `resp.StatusCode != 200` → проброс статуса+заголовков+тела.
  - **Assertions**: клиент получил 500 + тело upstream; `parseSSE` не вызывался.
  - **Sufficiency**: upstream-ошибки не маскируются.
  - Эквивалент: `TestZAITransform_UpstreamNon200_ForwardedAsIs`.
- [ ] **Run validation**: `go test ./pkg/proxy/... -run 'ConvertNonStreaming|UpstreamNon200|PassthroughStreaming|EmptySSE|SSEError'` — зелёные.
- [ ] **Edge verification**: passthrough streaming-запросов и не-z.ai upstream (без изменений) — `TestZAITransform_PassthroughStreaming`.

### Task 10: `pkg/web/dashboard` — facade conformance (index.html)  [verify/conform]

Проверка конформности `pkg/web/dashboard` — leaf-клеточка статических ассетов, родительская `pkg/web`
импортирует её (Usages `dashboard_assets`) через `//go:embed dashboard/*`; обрабатывается ПЕРЕД `pkg/web`
(bottom-up, leaf-before-parent — Phase 7). Сущность (`index.html`):
`DashboardAssets` — состав 5 ассетов (`index.html`, `style.css`, `app.js`, `markdown-it.min.js`, `favicon.svg`).
Imports: нет. Usages: `dashboard_assets.md` (local). Минимальный контракт — верифицируется через embed в `pkg/web` (Task 11/12).

**Usages relevant to this task:**
- `dashboard_assets.md`: состав 5 ассетов (должен совпадать с `embed.go` `dashboard/*`).

**CRITICAL: `CODEMANIFEST` files — read-only contract definitions. Do NOT modify them. If implementation does not match the contract, fix the implementation — never fix the contract.**

- [ ] **STEP 0 (DECLARATION)**: Task 10 (`pkg/web/dashboard` facade conformance).
- [ ] **STEP 1 (CONTRACT TESTS)**: верифицировать, что 5 ассентов присутствуют в `pkg/web/dashboard/`
  (`ls` = `index.html`, `style.css`, `app.js`, `markdown-it.min.js`, `favicon.svg`, `CODEMANIFEST`, `.usages/`).
  Тестов нет (assets-клеточка) — контракт подтверждается embed в Task 12.
- [ ] **STEP 2 (IMPLEMENTATION)**: сверить состав `dashboard_assets.md` с реальными файлами. Зазоров нет.
- [ ] **STEP 3 (INTERFACE VERIFICATION)**: `go build ./pkg/web/...` exit 0 (embed резолвится).
- [ ] **STEP 4 (LOGIC TESTS)**: N/A для assets-клеточки (поведение = доступность файлов, покрыто Task 12).
- [ ] **STEP 5 (DEBUGGING)**: `go test ./pkg/web/...` — зелёные (через Task 12).
- [ ] **STEP 6 (CONTRACT RE-VERIFICATION)**: соответствие `CODEMANIFEST` (`DashboardAssets`, состав 5 файлов).
- [ ] **STEP 7 (LINT)**: `goga lint` 16/0.
- [ ] **STEP 8 (COMPLETION)**: отметить чекбоксы.

### Task 11: `pkg/web` — conformance (embed.go, embed-split)  [verify/conform]  ⚠ высокий риск — embed после переноса ассетов

Проверка конформности `pkg/web`. Сущности (`embed.go`): `FS() -> fs:embed.FS` (function). Imports:
`pkg/web/dashboard` (Usages `dashboard_assets`). Usages: `embed_fs_facade.md` (local).

**Дословная трассировка (из дизайн-дока §Code Stack Trace — `pkg/web` embed-split):**
```
embed.go: //go:embed dashboard/* → var embedded embed.FS → fs.Sub(embedded, "dashboard") → FS.
  Checkpoint (passed): fs.Sub пере-рулит встраивание в корень dashboard/, поэтому относительные веб-пути
  (index.html, style.css, …) не меняются — перенос ассетов в поддиректорию изменил только путь embed-директивы,
  не поведение FS. pkg/server отдаёт корень / через http.FileServer(http.FS(web.FS)).
```
**Важно:** перенос 5 ассетов в `pkg/web/dashboard/` + правка `//go:embed dashboard/*` **уже выполнены**
(`goga-apply`). Эта задача — **только верификация** (НЕ переделывать перенос).

**Usages relevant to this task:**
- `dashboard_assets` (imported, `pkg/web/dashboard/.usages/dashboard_assets.md`): состав 5 ассетов для embed.
- `embed_fs_facade.md`: `fs.Sub(embedded,"dashboard")` — корневые пути без префикса.

**CRITICAL: `CODEMANIFEST` files — read-only contract definitions. Do NOT modify them. If implementation does not match the contract, fix the implementation — never fix the contract.**

- [ ] **STEP 0 (DECLARATION)**: Task 11 (`pkg/web` embed-split conformance).
- [ ] **STEP 1 (CONTRACT TESTS)**: contract-тест на фасад (`web.FS` возвращает `embed.FS`). Тестов в `pkg/web` НЕТ
  (0 файлов) — добавить минимальный shape-тест `web_test.go` (`func TestFS_ReturnsEmbedFS`), PASS'ит (код существует).
- [ ] **STEP 2 (IMPLEMENTATION)**: проверить `pkg/web/embed.go` — `//go:embed dashboard/*`, `fs.Sub(embedded,"dashboard")`.
  Подтвердить, что перенос ассетов на месте (`ls pkg/web/dashboard/` = 5 файлов) и `go build ./...` резолвит embed.
  Зазоров нет; НЕ повторять `git mv`. Править код только при находке.
- [ ] **STEP 3 (INTERFACE VERIFICATION)**: `go test ./pkg/web/...` — зелёные (новый shape-тест).
- [ ] **STEP 4 (LOGIC TESTS)**: поведение — `fs.Sub` даёт корневые пути (без префикса `dashboard/`). Покрытие
  поведения — в Task 12 (integration); здесь достаточно shape-теста.
- [ ] **STEP 5 (DEBUGGING)**: `go test ./pkg/web/...` — фиксить код до зелёного.
- [ ] **STEP 6 (CONTRACT RE-VERIFICATION)**: соответствие `CODEMANIFEST` (`FS`, `fs.Sub`).
- [ ] **STEP 7 (LINT)**: `go vet ./pkg/web/...`; `goga lint` 16/0; `go build ./...` exit 0.
- [ ] **STEP 8 (COMPLETION)**: отметить чекбоксы.

### Task 12: `pkg/web` — integration test (embed serves original paths after dir move)  [integration]  ⚠ genuine gap

**Genuine test gap**: `pkg/web` не имеет тестов. Сценарий `TestWeb_Embed_ServesOriginalWebPaths_AfterDirMove`
**отсутствует** и должен быть **добавлен** (`pkg/web/web_test.go` или `embed_test.go`). Переносится **дословно**
из дизайн-дока §Test Stack Trace (Edge Case).

**Usages relevant to this task:**
- `embed_fs_facade.md`: контракт корневых путей после `fs.Sub`.

**CRITICAL: `CODEMANIFEST` files — read-only contract definitions. Do NOT modify them.**

- [ ] **Edge: `TestWeb_Embed_ServesOriginalWebPaths_AfterDirMove`** (дословно):
  - **Setup**: собранный бинарь (embed `dashboard/*`).
  - **Input**: `http.Get(<server>/style.css)`.
  - **Trace**: `web.FS = fs.Sub(embedded,"dashboard") → http.FileServer отдаёт style.css по корневому пути (без префикса dashboard/)`.
  - **Assertions**: 200 + корректный CSS; `index.html`, `app.js`, `favicon.svg`, `markdown-it.min.js` также доступны по корневым путям.
  - **Sufficiency**: регресс на слом embed после физического переноса ассетов в `pkg/web/dashboard/`.
- [ ] Создать `pkg/web/embed_test.go` с `TestWeb_Embed_ServesOriginalWebPaths_AfterDirMove`: использовать
  `http.FileServer(http.FS(web.FS))` + `httptest.NewRequest`/`httptest.NewRecorder` (или прямой `fs.Stat(web.FS, "style.css")`),
  проверить доступность всех 5 корневых путей. Не запускать реальный HTTP-сервер — достаточно `http.FileServer` + recorder.
- [ ] **Run validation**: `go test ./pkg/web/... -run 'ServesOriginalWebPaths'` — зелёный (новый тест).
- [ ] **Cross-check**: после теста `goga lint` остаётся 16/0, `go build ./...` exit 0.

### Task 13: `assets` — conformance (assets.go)  [verify/conform]

Проверка конформности `assets`. Сущности (`assets.go`): `ReadPrompt(name, overrideDir) -> (prompt, error)` (function);
`FS`, `SkillsFS` (functions → `embed.FS`). Imports: нет. Usages: `assets_facade.md` (local).
Семантика: `ReadPrompt` читает встроенный system-промпт (с override-директорией); `FS`/`SkillsFS` — embed встроенных промптов/скиллов.

**Usages relevant to this task:**
- `assets_facade.md`: как `ReadPrompt`/`FS`/`SkillsFS` вызываются из `cmd/afm` (`loadPrompts`, `installSkills`).

**CRITICAL: `CODEMANIFEST` files — read-only contract definitions. Do NOT modify them. If implementation does not match the contract, fix the implementation — never fix the contract.**

- [ ] **STEP 0 (DECLARATION)**: Task 13 (`assets` conformance).
- [ ] **STEP 1 (CONTRACT TESTS)**: contract-тесты на фасад (`assets.ReadPrompt`, `assets.FS`, `assets.SkillsFS`).
  Тестов в `assets` нет — добавить минимальный shape-тест (`TestReadPrompt_ReturnsPrompt`, `TestFS_NonEmpty`),
  PASS'ит (код/embed существует). Если embed-зависимый тест тяжёлый — ограничиться `go doc`/`go vet` facade-check.
- [ ] **STEP 2 (IMPLEMENTATION)**: проверить `assets.go` — embed-директивы, `ReadPrompt` (override), `FS`/`SkillsFS`.
  Зазоров нет; править код при находке.
- [ ] **STEP 3 (INTERFACE VERIFICATION)**: `go test ./assets/...` (если добавлен тест) либо `go build ./assets/...` exit 0.
- [ ] **STEP 4 (LOGIC TESTS)**: поведение — `ReadPrompt` возвращает промпт; override приоритетнее embed. При добавлении теста — покрыть.
- [ ] **STEP 5 (DEBUGGING)**: зелёные.
- [ ] **STEP 6 (CONTRACT RE-VERIFICATION)**: соответствие `CODEMANIFEST`.
- [ ] **STEP 7 (LINT)**: `go vet ./assets/...`; `goga lint` 16/0.
- [ ] **STEP 8 (COMPLETION)**: отметить чекбоксы.

### Task 14: `tools/setstatuslinter` — conformance (main.go)  [verify/conform]

Проверка конформности `tools/setstatuslinter`. Сущность (`main.go`): `Analyzer` (entity). Imports: нет.
Usages: `x_tools_analysis` (project). Независимый `go/analysis`-линтер (`singlechecker.Main`):
запрещает `(*state.Store).Apply` вне `pkg/orchestrator/fsm.go` (область `./pkg/...`, `_test.go` исключены).

**Usages relevant to this task:**
- `x_tools_analysis`: `go/analysis`-анализатор, `singlechecker.Main`; запрет `Store.Apply` вне `fsm.go`.

**CRITICAL: `CODEMANIFEST` files — read-only contract definitions. Do NOT modify them. If implementation does not match the contract, fix the implementation — never fix the contract.**

- [ ] **STEP 0 (DECLARATION)**: Task 14 (`tools/setstatuslinter` conformance).
- [ ] **STEP 1 (CONTRACT TESTS)**: contract-тест на фасад (`setstatuslinter.Analyzer`). Тестов нет — добавить
  минимальный анализатор-тест (`TestAnalyzer_RejectsApplyOutsideFsmGo` / `TestAnalyzer_AllowsApplyInFsmGo`)
  через `analysistest.Run` с тестовым пакетом. PASS'ит (анализатор существует).
- [ ] **STEP 2 (IMPLEMENTATION)**: проверить `main.go` — `Analyzer` (`*analysis.Analyzer`), правило запрета `Store.Apply`
  вне `fsm.go`, исключение `_test.go`. Зазоров нет; править код при находке.
- [ ] **STEP 3 (INTERFACE VERIFICATION)**: `go test ./tools/setstatuslinter/...` (если добавлен) либо `go build ./tools/setstatuslinter/...` exit 0.
- [ ] **STEP 4 (LOGIC TESTS)**: поведение — анализатор флагает `Store.Apply` вне `fsm.go`, пропускает в `fsm.go` и `_test.go`.
- [ ] **STEP 5 (DEBUGGING)**: зелёные.
- [ ] **STEP 6 (CONTRACT RE-VERIFICATION)**: соответствие `CODEMANIFEST` (`Analyzer`).
- [ ] **STEP 7 (LINT)**: `go vet ./tools/setstatuslinter/...`; `goga lint` 16/0.
- [ ] **STEP 8 (COMPLETION)**: отметить чекбоксы.

### Task 15: `pkg/prompts` — conformance (builder.go + validator.go)  [verify/conform]

Проверка конформности `pkg/prompts`. Сущности: `Build(in Inputs) -> prompt` (function, `builder.go`);
`EscapeTagsForReprompt(s) -> escaped` (function, `builder.go`); `Inputs` (entity, `builder.go`);
`ValidatePlan(md, required) -> PlanIssues` (function, `validator.go`); `PlanIssues` (entity, `validator.go`).
Imports: `pkg/flow` (`Stage`). Usages: `prompts_facade.md` (local). Семантика: `Build` собирает промпт по `Inputs`
(шаблон, стадия, артефакты, диалог, контекст); `ValidatePlan(md,required)`→`PlanIssues{MissingSections}`, `IsClean()`.

**Usages relevant to this task:**
- `prompts_facade.md`: как `Build`/`ValidatePlan` вызываются из `pkg/orchestrator`.

**CRITICAL: `CODEMANIFEST` files — read-only contract definitions. Do NOT modify them. If implementation does not match the contract, fix the implementation — never fix the contract.**

- [ ] **STEP 0 (DECLARATION)**: Task 15 (`pkg/prompts` conformance).
- [ ] **STEP 1 (CONTRACT TESTS)**: contract-тесты на фасад (`prompts.Build`, `prompts.ValidatePlan`, `prompts.PlanIssues`,
  `prompts.Inputs`, `prompts.EscapeTagsForReprompt`). Покрыто `pkg/prompts/*_test.go`.
- [ ] **STEP 2 (IMPLEMENTATION)**: проверить `builder.go`/`validator.go` — сборка промпта, `ValidatePlan`→`MissingSections`,
  `IsClean()`, `EscapeTagsForReprompt`. Зазоров нет; править код при находке.
- [ ] **STEP 3 (INTERFACE VERIFICATION)**: `go test ./pkg/prompts/...` — зелёные.
- [ ] **STEP 4 (LOGIC TESTS)**: поведение — `ValidatePlan` находит отсутствующие секции, `IsClean()`, prompt-injection
  (`EscapeTagsForReprompt`). Покрыто suite (`TestPromptInjection_DescriptionWithMaliciousTags` в orchestrator).
- [ ] **STEP 5 (DEBUGGING)**: `go test ./pkg/prompts/...` — фиксить код до зелёного.
- [ ] **STEP 6 (CONTRACT RE-VERIFICATION)**: соответствие `CODEMANIFEST`.
- [ ] **STEP 7 (LINT)**: `go vet ./pkg/prompts/...`; `goga lint` 16/0.
- [ ] **STEP 8 (COMPLETION)**: отметить чекбоксы.

### Task 16: `pkg/docker` — conformance (launcher.go, ReExec + privilege-drop)  [verify/conform]  ⚠ высокий риск — privilege-drop

Проверка конформности `pkg/docker`. Сущности (`launcher.go`): `CheckClaudeDockerAuth`, `ScanCommands`,
`SetExecFunc`, `ResetExecFunc`, `ReExec` (functions); `CommandMount`, `ReExecConfig` (entities). Imports: `pkg/flow` (`Flow`).
Usages: `docker_privilege_drop.md` (local), `x_term` (project).

**Дословная трассировка (из дизайн-дока §Code Stack Trace — `pkg/docker.ReExec`):**
```
ReExec(cfg): exec.LookPath("docker") (фатально если нет) → os.UserHomeDir (фатально если пусто) →
  args := docker run --rm (+ -it если isTTY()=term.IsTerminal) → -p при DashboardPort>0 → монтирования:
  проект same-path, ~/.claude+~/.afm→containerHome, НАМЕРЕННО НЕ ~/.claude.json (corruption при атомарном rename :ro),
  команды :ro, extra-mounts (~→containerHome) → env: AFM_IN_DOCKER=1, AFM_HOST_UID/GID (entrypoint дропает gosu
  до них), секреты в bare-форме -e KEY (не светятся в argv/ps) → execFunc(dockerBin,args,os.Environ())
  (syscall.Exec, не возвращает). Checkpoint (passed): привилегии дропаются до хостового uid/gid через
  entrypoint+gosu; HOME выставляется ПОСЛЕ gosu (gosu сбрасывает HOME для uid без записи в /etc/passwd);
  совпадает с Algorithm pkg/docker и CLAUDE.md «Docker Mode». isTTY — честная проверка через term.IsTerminal
  (os.ModeCharDevice ложно срабатывал на /dev/null).
```

**Usages relevant to this task:**
- `x_term`: `term.IsTerminal` для `isTTY()` (решение по `-it`); НЕ `os.ModeCharDevice` (ложно с `/dev/null`).
- `docker_privilege_drop.md`: gosu/HOME-after/uid-drop — потребляется в `cmd/afm`.

**CRITICAL: `CODEMANIFEST` files — read-only contract definitions. Do NOT modify them. If implementation does not match the contract, fix the implementation — never fix the contract.**

- [ ] **STEP 0 (DECLARATION)**: Task 16 (`pkg/docker` ReExec + privilege-drop conformance).
- [ ] **STEP 1 (CONTRACT TESTS)**: contract-тесты на фасад (`docker.ReExec`, `docker.ScanCommands`,
  `docker.CheckClaudeDockerAuth`, `docker.SetExecFunc`, `docker.ResetExecFunc`, `docker.CommandMount`, `docker.ReExecConfig`).
  Покрыто `pkg/docker/launcher_test.go` (`TestReExec_BuildsDockerArgs`, `TestReExec_DockerNotFound`, `TestReExec_PassthroughEnv`, `TestScanCommands_*`).
- [ ] **STEP 2 (IMPLEMENTATION)**: проверить `launcher.go` — монтирования, `-it` по `isTTY` (`term.IsTerminal`),
  bare `-e KEY`, **отсутствие** `~/.claude.json` mount, `AFM_HOST_UID/GID`, `syscall.Exec` через `execFunc`.
  Зазоров нет; править код при находке.
- [ ] **STEP 3 (INTERFACE VERIFICATION)**: `go test ./pkg/docker/...` — зелёные.
- [ ] **STEP 4 (LOGIC TESTS)**: поведение — args-сборка, docker-not-found, passthrough env, scan-commands
  (dedup, skip claude, skip missing). Покрыто suite.
- [ ] **STEP 5 (DEBUGGING)**: `go test ./pkg/docker/...` — фиксить код до зелёного.
- [ ] **STEP 6 (CONTRACT RE-VERIFICATION)**: соответствие `CODEMANIFEST` (Algorithm `pkg/docker` privilege-drop).
- [ ] **STEP 7 (LINT)**: `go vet ./pkg/docker/...`; `goga lint` 16/0.
- [ ] **STEP 8 (COMPLETION)**: отметить чекбоксы.

### Task 17: `pkg/docker` — integration test (HomeAfterGosu scenario)  [integration]

Integration-сценарий Docker privilege-drop. Переносится **дословно** из дизайн-дока §Test Stack Trace (Edge Case).
Эквиваленты (`TestReExec_BuildsDockerArgs`, `TestReExec_PassthroughEnv`) уже в suite — задача верифицирует
эквивалентность и добавляет алиас с каноническим именем.

**Usages relevant to this task:**
- `docker_privilege_drop.md`: контракт gosu/HOME/uid-drop.
- `x_term`: `isTTY` через `term.IsTerminal`.

**CRITICAL: `CODEMANIFEST` files — read-only contract definitions. Do NOT modify them.**

- [ ] **Edge: `TestDocker_HomeNotDroppable_HomeAfterGosu`** (дословно):
  - **Setup**: `SetExecFunc` перехватывает; мок `/etc/passwd` без записи для uid.
  - **Input**: `ReExec(ReExecConfig{…})` с `AFM_HOST_UID`/`AFM_HOST_GID` из `os.Getuid/Gid`.
  - **Trace**: аргументы `docker run` содержат `-e AFM_HOST_UID=<uid> -e AFM_HOST_GID=<gid>`, `AFM_IN_DOCKER=1`;
    entrypoint (скрипт) дропает gosu → выставляет `HOME=/home/afm` ПОСЛЕ gosu; `~/.claude.json` НЕ в `-v`.
  - **Assertions**: перехваченные args НЕ содержат `:ro`-маунта `.claude.json`; `AFM_HOST_UID`/`GID` присутствуют;
    секреты — bare `-e KEY` (без значения в argv).
  - **Sufficiency**: регресс на «токен/файлы агента ищутся в `/`» и «corrupted .claude.json» (см. CLAUDE.md).
  - Эквиваленты: `TestReExec_BuildsDockerArgs`, `TestReExec_PassthroughEnv` — сверить assertions; добавить алиас при расхождении.
- [ ] **Setup note**: `SetExecFunc`/`ResetExecFunc` подменяют `syscall.Exec` (перехват args без реального `docker run`);
  `t.Parallel` НЕ использовать (изменяемое состояние пакета).
- [ ] **Run validation**: `go test ./pkg/docker/... -run 'ReExec|ScanCommands'` — зелёные.
- [ ] **Cross-check**: после теста `goga lint` 16/0.

### Task 18: `pkg/executor` — conformance (executor.go + transcript.go + runner.go)  [verify/conform]

Проверка конформности `pkg/executor`. Сущности: `DefaultClaudeArgs`, `ResolveArgs`, `ParseToolAction`,
`WrittenFiles`, `Config` (`executor.go`); `DialogTranscript`, `TranscriptItem` (`transcript.go`); `Runner` (`runner.go`);
mutation `Runner::Executor(cfg Config)` (`executor.go`). Imports: `pkg/progress` (`Logger`). Usages: `runner_facade.md` (local).
Семантика: `DefaultClaudeArgs` (`--print --output-format=stream-json --verbose` — `--verbose` обязателен для Claude 2.1.x);
`ResolveArgs` дедуплицирует; `Runner.RunAgent`/`RunPlanning` порождают процесс, стримят stdout/stderr, пишут лог через
`progress.NewLogger`; `Config.ProxyURL`/`ProxyShimDir` инжекят `ANTHROPIC_BASE_URL`/`AFM_PROXY_URL` и prepends shim в `PATH`
(вычищая существующий `ANTHROPIC_BASE_URL`); `DialogTranscript`/`WrittenFiles` парсят stream-json.
**Cross-cutting note (`proxyForCmd`):** `claude` НЕ получает proxy (OAuth→api.anthropic.com; z.ai не принимает OAuth) —
внутренняя маршрутизация `pkg/orchestrator`, в контракте не выражена.

**Usages relevant to this task:**
- `runner_facade.md`: как `Runner`/`Config` собираются в `pkg/orchestrator` (`Options.Runner`).

**CRITICAL: `CODEMANIFEST` files — read-only contract definitions. Do NOT modify them. If implementation does not match the contract, fix the implementation — never fix the contract.**

- [ ] **STEP 0 (DECLARATION)**: Task 18 (`pkg/executor` conformance).
- [ ] **STEP 1 (CONTRACT TESTS)**: contract-тесты на фасад (`executor.Config`, `executor.Runner`, `executor.DefaultClaudeArgs`,
  `executor.ResolveArgs`, `executor.ParseToolAction`, `executor.WrittenFiles`, `executor.DialogTranscript`, `executor.TranscriptItem`).
  Покрыто `pkg/executor/*_test.go`.
- [ ] **STEP 2 (IMPLEMENTATION)**: проверить `executor.go`/`transcript.go`/`runner.go` — `DefaultClaudeArgs` (с `--verbose`),
  `ResolveArgs` дедуп, proxy/shim-инъекция env + PATH, парсинг stream-json. Mutation `Runner::Executor(cfg)`.
  Зазоров нет; править код при находке.
- [ ] **STEP 3 (INTERFACE VERIFICATION)**: `go test ./pkg/executor/...` — зелёные.
- [ ] **STEP 4 (LOGIC TESTS)**: поведение — `DefaultClaudeArgs` состав, `ResolveArgs` дедуп, `ParseToolAction`,
  `WrittenFiles`/`DialogTranscript` парсинг. Покрыто suite; расширить при непокрытом.
- [ ] **STEP 5 (DEBUGGING)**: `go test ./pkg/executor/...` — фиксить код до зелёного.
- [ ] **STEP 6 (CONTRACT RE-VERIFICATION)**: соответствие `CODEMANIFEST` (mutation `Runner::Executor`).
- [ ] **STEP 7 (LINT)**: `go vet ./pkg/executor/...`; `goga lint` 16/0.
- [ ] **STEP 8 (COMPLETION)**: отметить чекбоксы.

### Task 19: `pkg/orchestrator` — conformance, errors + buses + graph + context  [verify/conform]

Проверка конформности `pkg/orchestrator`, часть A. Сущности: `Classify`, `IncompleteWorkError`,
`MissingArtifactError`, `MissingSectionsError`, `StorageError` (`errors.go`); `UIBus`, `CriticalBus`, `Event` (`bus.go`);
`Graph` (`graph.go`); `CollectArtifacts`, `CollectDependencyPlans` (`context.go`). Imports: `pkg/flow` (`Artifact`, `Stage`),
`pkg/prompts` (`Inputs`, `PlanIssues`), `pkg/state` (`RunState`, `StageState`, `Store`, `Transition`). Usages: `orchestrator_facade.md`.
Семантика: `Classify(err)`→`ClassNone/Retryable/Incomplete/MissingArtifact/MissingSections/Fatal/StorageFatal`;
`MissingSectionsError.Missing`←`PlanIssues.MissingSections`; `UIBus` — pub/sub для `pkg/server` (`Subscribe`→канал,
`SubscriberDroppedCount`); `CriticalBus` — гарантированная доставка; `Graph.ReadyStages` — стадии в ready, все зависимости которых done.

**Usages relevant to this task:**
- `orchestrator_facade.md`: фасад потребителя (FSM, buses, graph).

**CRITICAL: `CODEMANIFEST` files — read-only contract definitions. Do NOT modify them. If implementation does not match the contract, fix the implementation — never fix the contract.**

- [ ] **STEP 0 (DECLARATION)**: Task 19 (`pkg/orchestrator` errors/buses/graph/context conformance).
- [ ] **STEP 1 (CONTRACT TESTS)**: contract-тесты на фасад (`orchestrator.Classify`, `orchestrator.UIBus`,
  `orchestrator.CriticalBus`, `orchestrator.Event`, `orchestrator.Graph`, `orchestrator.CollectArtifacts`,
  `orchestrator.CollectDependencyPlans`, типы ошибок). Покрыто `pkg/orchestrator/*_test.go` (`TestClassify`,
  `TestUIBus_FanOutAndDrop`, `TestCriticalBus_Blocking`, `TestReadyStages*`, `TestCollectArtifacts_*`, `TestCollectDependencyPlans`).
- [ ] **STEP 2 (IMPLEMENTATION)**: проверить `errors.go`/`bus.go`/`graph.go`/`context.go` — `Classify` mapping,
  pub/sub buses, `ReadyStages` graph logic, `Collect*`. **Enum-конвенция:** `EventType`/`Classification` — значения
  в annotations, НЕ top-level типы (после Applied Fixes). Зазоров нет; править код при находке.
- [ ] **STEP 3 (INTERFACE VERIFICATION)**: `go test ./pkg/orchestrator/...` — зелёные.
- [ ] **STEP 4 (LOGIC TESTS)**: поведение — classify mapping, bus fan-out/drop, critical blocking, ready-stages deps, collect-artifacts/dep-plans. Покрыто suite.
- [ ] **STEP 5 (DEBUGGING)**: `go test ./pkg/orchestrator/...` — фиксить код до зелёного.
- [ ] **STEP 6 (CONTRACT RE-VERIFICATION)**: соответствие `CODEMANIFEST` (Applied Fixes учтены).
- [ ] **STEP 7 (LINT)**: `go vet ./pkg/orchestrator/...`; `goga lint` 16/0.
- [ ] **STEP 8 (COMPLETION)**: отметить чекбоксы.

### Task 20: `pkg/orchestrator` — conformance, FSM (fsm.go)  [verify/conform]  ⚠ высокий риск

Проверка конформности `pkg/orchestrator`, часть B (FSM). Сущности (`fsm.go`): `IsTerminal`, `FSM`, `GuardCtx`, `Rule`.
Imports (часть): `pkg/state` (`Store`, `Transition`). Usages: `orchestrator_facade.md`.
Семантика (из дизайн-дока §Algorithm Design — `pkg/orchestrator`): `FSM.Apply(stageID,ev,ctx,reason)` → `ruleAllowsFrom(From, текущий статус)`
→ если переход разрешён → `Store.Apply(Transition{from,to,ev,reason})` → вернуть `(to,applied,nil)`; `EvUserAnswered` → `phaseDispatch` (по `GuardCtx.Phase`).
**Constraint:** `(*Store).Apply` только из `fsm.go` (линтер `tools/setstatuslinter`). **FSM events (enum-конвенция):**
`EvAskUser From:[planning,running,retrying,revising]→awaiting_user_input`; `EvUserAnswered From:[awaiting_user_input]`.

**Usages relevant to this task:**
- `rapid`: property-based тесты FSM (тестовый контур, НЕ часть контракта).
- `orchestrator_facade.md`: контракт FSM-переходов.

**CRITICAL: `CODEMANIFEST` files — read-only contract definitions. Do NOT modify them. If implementation does not match the contract, fix the implementation — never fix the contract.**

- [ ] **STEP 0 (DECLARATION)**: Task 20 (`pkg/orchestrator` FSM conformance).
- [ ] **STEP 1 (CONTRACT TESTS)**: contract-тесты на фасад (`orchestrator.FSM`, `orchestrator.Rule`, `orchestrator.GuardCtx`,
  `orchestrator.IsTerminal`). Покрыто `pkg/orchestrator/fsm_test.go` (`TestFSM_Apply_LegalTransitions`,
  `TestFSM_Apply_IllegalReturnsApplyFalse`, `TestFSM_Apply_AskUser`, `TestFSM_Apply_AskUser_FromRetryAndRevising`,
  `TestFSM_PhaseDispatch_UserAnswered`, `TestFSM_PhaseDispatch_ResumeAfterRetry`, `TestFSM_Property_LivenessTerminates`).
- [ ] **STEP 2 (IMPLEMENTATION)**: проверить `fsm.go` — `ruleAllowsFrom`, `Apply`→`Store.Apply` (только отсюда),
  `EvAskUser`/`EvUserAnswered` From-списки, `phaseDispatch`. **Enum-конвенция:** `FSMEvent`/`StageStatus` — значения
  в annotations. Зазоров нет; править код при находке.
- [ ] **STEP 3 (INTERFACE VERIFICATION)**: `go test ./pkg/orchestrator/...` — зелёные.
- [ ] **STEP 4 (LOGIC TESTS)**: поведение — legal/illegal transitions, ask-user from retry/revising, phase-dispatch,
  liveness (rapid). Покрыто suite.
- [ ] **STEP 5 (DEBUGGING)**: `go test ./pkg/orchestrator/...` — фиксить код до зелёного.
- [ ] **STEP 6 (CONTRACT RE-VERIFICATION)**: соответствие `CODEMANIFEST` (FSM-переходы).
- [ ] **STEP 7 (LINT)**: `go vet ./pkg/orchestrator/...`; `goga lint` 16/0.
- [ ] **STEP 8 (COMPLETION)**: отметить чекбоксы.

### Task 21: `pkg/orchestrator` — conformance, Orchestrator + dialog poller/NotifyAnswer (orchestrator.go)  [verify/conform]  ⚠ высокий риск — dialog protocol

Проверка конформности `pkg/orchestrator`, часть C. Сущности (`orchestrator.go`): `Options`, `Prompts`, `Orchestrator`.
Imports: `pkg/config` (`Config`), `pkg/executor` (`Runner`), `pkg/mcp` (`Question`, `QuestionFile` + Usages `dialog_protocol`),
`pkg/prompts` (`Inputs`, `PlanIssues`), `pkg/state` (`RunState`, `StageState`, `Store`, `Transition`). Usages: `orchestrator_facade.md`.

**Дословная трассировка (из дизайн-дока §Code Stack Trace — `pkg/orchestrator` dialog poller + NotifyAnswer):**
```
Orchestrator.startQuestionPoller → горутина с time.Ticker(1s) → pollQuestions(processed).
  pollQuestions: Store.Snapshot → для стадий в активных статусах (planning/running/revising/retrying/awaiting_user_input)
  → mcp.FindUnansweredQuestions → дедуп processed["stageID|phase|id"] → mcp.AppendQuestion (идемпотентно через FindEntry)
  → ui.Publish(EventAskUser) + Trigger(EvAskUser). Checkpoint (passed): при отсутствии открытых вопросов у
  интерактивной стадии — detectDialogViolation (fail-fast, если агент написал *.question.json вне stageDir).

Orchestrator.NotifyAnswer(stageID,phase,qID,answer,fromOptions): если isAgentActive(stageID) → Trigger(EvUserAnswered)
  + ui.Publish; ИНАЧЕ → critical.Publish (для onUserAnswered, перезапуск с --resume). Checkpoint (passed): две ветви
  совпадают с Algorithm контракта шаг 4; activeAgents — sync.Map (Concurrency).
```

**Usages relevant to this task:**
- `dialog_protocol` (imported, `pkg/mcp/.usages/dialog_protocol.md`): контракт, по которому работает poller/NotifyAnswer.
- `orchestrator_facade.md`: фасад Orchestrator.

**CRITICAL: `CODEMANIFEST` files — read-only contract definitions. Do NOT modify them. If implementation does not match the contract, fix the implementation — never fix the contract.**

- [ ] **STEP 0 (DECLARATION)**: Task 21 (`pkg/orchestrator` Orchestrator + dialog conformance).
- [ ] **STEP 1 (CONTRACT TESTS)**: contract-тесты на фасад (`orchestrator.Options`, `orchestrator.Prompts`,
  `orchestrator.Orchestrator`). Диалог-поведение покрыто `TestPollQuestions_DetectsNewQuestion`/`_Idempotent`,
  `TestDetectDialogViolation`, `TestFullDialogCycle`, `TestResumeInteractiveAgent_*`, `TestIntegration_PlanningWithOpenQuestionWaits`.
- [ ] **STEP 2 (IMPLEMENTATION)**: проверить `orchestrator.go` — `startQuestionPoller` (1 Гц), `pollQuestions`
  (snapshot + активные статусы + дедуп + AppendQuestion idempotent + EventAskUser/Trigger(EvAskUser)),
  `NotifyAnswer` (две ветви: active→Trigger(EvUserAnswered); иначе→critical.Publish), `detectDialogViolation`,
  `activeAgents` (`sync.Map`). **Applied Fixes учтены:** `Config` из `pkg/config` (не дублируется), `StageStatus`/`Agent`
  НЕ в Imports (enum-конвенция), ребра сохранены. Зазоров нет; править код при находке.
- [ ] **STEP 3 (INTERFACE VERIFICATION)**: `go test ./pkg/orchestrator/...` — зелёные.
- [ ] **STEP 4 (LOGIC TESTS)**: поведение — poller detects new question, idempotent, dialog violation fail-fast,
  NotifyAnswer active/inactive ветви, resume with --resume. Покрыто suite.
- [ ] **STEP 5 (DEBUGGING)**: `go test ./pkg/orchestrator/...` — фиксить код до зелёного.
- [ ] **STEP 6 (CONTRACT RE-VERIFICATION)**: соответствие `CODEMANIFEST` (Algorithm dialog).
- [ ] **STEP 7 (LINT)**: `go vet ./pkg/orchestrator/...`; `goga lint` 16/0.
- [ ] **STEP 8 (COMPLETION)**: отметить чекбоксы.

### Task 22: `pkg/orchestrator` — integration tests (dialog protocol scenarios)  [integration]

Integration-сценарии dialog-протокола (orchestrator-сторона). Переносятся **дословно** из дизайн-дока
§Test Stack Trace. Эквиваленты уже в `pkg/orchestrator/*_test.go` — задача верифицирует эквивалентность
и добавляет алиас с каноническим именем при необходимости.

**Usages relevant to this task:**
- `dialog_protocol`: контракт (критический путь O_EXCL в `pkg/server`, доставка через bash-loop/`--resume`).

**CRITICAL: `CODEMANIFEST` files — read-only contract definitions. Do NOT modify them.**

- [ ] **Positive: `TestDialog_AnswerDeliveredToAgent_BashLoop`** (дословно):
  - **Setup**: стадия `implementation` в `awaiting_user_input` — опросчик `startQuestionPoller` (1 Гц) уже перевёл
    её по `EvAskUser` (`From:[planning,running,retrying,revising]→awaiting_user_input`, `fsm.go:65`) сразу после
    обнаружения `implementation.q1.question.json`. Горутина агента ещё активна (`isAgentActive=true`, bash-цикл поллит `answer.json`).
    `$AFM_STAGE_DIR/<runDir>/<stage>` содержит `implementation.q1.question.json`.
    ⚠ **Почему `awaiting_user_input`, а не `running`:** `EvUserAnswered` разрешён только `From:[awaiting_user_input]`
    (`fsm.go:66`). У стадии с зарегистрированным вопросом и активным опросчиком статус уже `awaiting_user_input`,
    а не `running`. Если бы стадия действительно была в `running`, `Trigger(EvUserAnswered)` был бы молча отклонён
    (`ruleAllowsFrom`→`applied=false`, `fsm.go:89-90`), и `answer.json` остался бы на диске, а стадия зависла.
  - **Input**: HTTP POST `/api/stages/<stage>/dialog/answer` body `{"id":"q1","phase":"implementation","answer":"A","from_options":true}`.
  - **Trace**: `handleDialogAnswer → isValidDialogID("q1")=true → os.Stat(question.json) exists → allow_custom=true skip Options-validation → os.OpenFile(answer.json, O_EXCL) created → Write+Sync+Close → re-Stat(question.json) still exists → mcp.AppendAnswer(dialog.jsonl) best-effort → dialogAnswerFn=NotifyAnswer(stage,"implementation","q1","A",true) → isAgentActive=true → Trigger(EvUserAnswered) → FSM.Apply EvUserAnswered From:[awaiting_user_input] (fsm.go:66) → phaseDispatch (phase=implementation≠planning→StatusRunning) → Store.Apply(Transition) applied → ui.Publish(EventUserAnswered) → bash-цикл агента находит answer.json, читает "A", продолжается`.
  - **Assertions**: HTTP 200; `answer.json` существует с `{"id":"q1","answer":"A","from_options":true}`; `dialog.jsonl` содержит ответ;
    FSM перевёл стадию из `awaiting_user_input` → `running` (до ответа статус уже был `awaiting_user_input`, не `running`).
  - **Sufficiency**: гарантирует, что ответ доходит до работающего агента через критический путь O_EXCL — регресс на «агент висит, хотя пользователь ответил».
  - Эквиваленты: `TestFullDialogCycle`, `TestResumeInteractiveAgent_ImplementationPhase` — сверить end-to-end; добавить алиас при расхождении.
- [ ] **General Setup**: мок-агент — bash-скрипт, пишущий `*.question.json` в `$AFM_STAGE_DIR`; интерактивные стадии
  запускают реальный `executor.New` (не injected Runner).
- [ ] **Run validation**: `go test ./pkg/orchestrator/... -run 'FullDialogCycle|InteractiveAgent|PollQuestions|DetectDialogViolation'` — зелёные.
- [ ] **Cross-check**: `goga schema --depends-on pkg/mcp` подтверждает ребро `pkg/orchestrator`→`pkg/mcp`; `goga lint` 16/0.

### Task 23: `pkg/server` — conformance (server.go)  [verify/conform]

Проверка конформности `pkg/server`, часть A. Сущности (`server.go`): `Config`, `Server`. Imports: `pkg/executor`
(`TranscriptItem`), `pkg/mcp` (`Answer`, `Entry`), `pkg/orchestrator` (`Event`, `UIBus`), `pkg/state` (`Store`), `pkg/web` (`FS`).
Usages: `server_facade.md` (local), `gorilla_websocket` (project). Семантика (вне диалога): `Start` слушает порт;
корень `/` — статика из `web.FS`; approve/revise/retry/dialog-cancel гейтятся `StageStatus` через `Store.Snapshot`;
WebSocket — `UIBus.Subscribe`.

**Usages relevant to this task:**
- `gorilla_websocket`: `Upgrader` (один на пакет, `CheckOrigin *`), подписка на `UIBus`, `WriteMessage(TextMessage,…)`.
- `server_facade.md`: фасад `Server`/`Config`.

**CRITICAL: `CODEMANIFEST` files — read-only contract definitions. Do NOT modify them. If implementation does not match the contract, fix the implementation — never fix the contract.**

- [ ] **STEP 0 (DECLARATION)**: Task 23 (`pkg/server` server.go conformance).
- [ ] **STEP 1 (CONTRACT TESTS)**: contract-тесты на фасад (`server.Config`, `server.Server`). Покрыто
  `pkg/server/*_test.go` (`TestServerRouteStages`, `TestServerServesMarkdownIt`, `TestWebSocket_ReceivesEvents`,
  `TestHandleApprove`/`_Retry`/`_Revise`/`_Status`, `TestDialogGet*`, `TestDialogCancel*`).
- [ ] **STEP 2 (IMPLEMENTATION)**: проверить `server.go` — `Start`, статика из `web.FS`, approve/revise/retry/dialog-cancel
  гейтятся `StageStatus` через `Store.Snapshot`, WebSocket (`UIBus.Subscribe`). Зазоров нет; править код при находке.
- [ ] **STEP 3 (INTERFACE VERIFICATION)**: `go test ./pkg/server/...` — зелёные.
- [ ] **STEP 4 (LOGIC TESTS)**: поведение — маршруты, статика, WS-стриминг, status-gates. Покрыто suite.
- [ ] **STEP 5 (DEBUGGING)**: `go test ./pkg/server/...` — фиксить код до зелёного.
- [ ] **STEP 6 (CONTRACT RE-VERIFICATION)**: соответствие `CODEMANIFEST`.
- [ ] **STEP 7 (LINT)**: `go vet ./pkg/server/...`; `goga lint` 16/0.
- [ ] **STEP 8 (COMPLETION)**: отметить чекбоксы.

### Task 24: `pkg/server` — conformance, handleDialogAnswer O_EXCL (handlers.go)  [verify/conform]  ⚠ высокий риск — критический путь O_EXCL

Проверка конформности `pkg/server`, часть B (dialog answer handler). `handleDialogAnswer` в `handlers.go`.
Imports: `pkg/executor` (`TranscriptItem`), `pkg/mcp` (`Answer`, `Entry`). Usages: `server_facade.md`, `gorilla_websocket`.

**Дословная трассировка (из дизайн-дока §Code Stack Trace — `pkg/server.handleDialogAnswer`):**
```
handleDialogAnswer: extractStageID → isValidStageID (400) → декодинг тела → проверка id/phase/answer (400) →
  фаза ∈ {planning,implementation,review} (400) → isValidDialogID(req.ID) (path-traversal guard, 400) →
  os.Stat(question.json) (404 если нет) → чтение/парсинг question.json → валидация ответа против Options при
  allow_custom=false (400) → АТОМАРНАЯ ЗАПИСЬ answer.json (os.OpenFile(…,O_WRONLY|O_CREATE|O_EXCL,0644) → 409 Conflict
  если уже есть) → Write+Sync+Close (с _ = os.Remove при сбое) → re-check question.json всё ещё есть (TOCTOU, 400 + remove)
  → mcp.AppendAnswer в dialog.jsonl (best-effort, НЕ критический) → dialogAnswerFn (notify). Checkpoint (passed):
  порядок «сначала answer.json (O_EXCL), потом история» гарантирует, что bash-цикл агента всегда найдёт ответ,
  даже если запись истории упадёт; совпадает с Algorithm pkg/server.
```

**Usages relevant to this task:**
- `server_facade.md`: контракт dialog-answer handler (O_EXCL, TOCTOU, 400/404/409).

**CRITICAL: `CODEMANIFEST` files — read-only contract definitions. Do NOT modify them. If implementation does not match the contract, fix the implementation — never fix the contract.**

- [ ] **STEP 0 (DECLARATION)**: Task 24 (`pkg/server` handleDialogAnswer conformance).
- [ ] **STEP 1 (CONTRACT TESTS)**: contract/behaviour покрыто `pkg/server/handlers_test.go`
  (`TestHandleDialogAnswer_WritesAnswerFile`, `TestHandleDialogAnswer_DuplicateAnswer`,
  `TestHandleDialogAnswer_InvalidID`, `TestHandleDialogAnswer_QuestionNotFound`,
  `TestHandleDialogAnswer_AppendAnswerFailureStillNotifies`, `TestDialogAnswer`).
- [ ] **STEP 2 (IMPLEMENTATION)**: проверить `handlers.go` — `handleDialogAnswer`: валидация (stageID/body/phase/
  dialogID path-traversal), `Stat` question.json (404), Options-валидация при `!allow_custom`, `OpenFile(O_EXCL)` (409),
  `Write+Sync+Close`, re-`Stat` question.json (TOCTOU), `mcp.AppendAnswer` (best-effort), `dialogAnswerFn` (notify).
  **Контракт: критический путь O_EXCL — ЗДЕСЬ, не в `pkg/mcp`.** Зазоров нет; править код при находке.
- [ ] **STEP 3 (INTERFACE VERIFICATION)**: `go test ./pkg/server/...` — зелёные.
- [ ] **STEP 4 (LOGIC TESTS)**: поведение — пишет answer.json, 409 на дубль, 400 на invalid/path-traversal, 404 на
  нет question, AppendAnswer-failure всё равно notify (TOCTOU). Покрыто suite.
- [ ] **STEP 5 (DEBUGGING)**: `go test ./pkg/server/...` — фиксить код до зелёного.
- [ ] **STEP 6 (CONTRACT RE-VERIFICATION)**: соответствие `CODEMANIFEST` (Algorithm `pkg/server` dialog).
- [ ] **STEP 7 (LINT)**: `go vet ./pkg/server/...`; `goga lint` 16/0.
- [ ] **STEP 8 (COMPLETION)**: отметить чекбоксы.

### Task 25: `pkg/server` — integration tests (dialog answer scenarios)  [integration]

Integration-сценарии dialog-answer (server-сторона). Переносятся **дословно** из дизайн-дока §Test Stack Trace.
Эквиваленты уже в `pkg/server/handlers_test.go` — задача верифицирует эквивалентность и добавляет алиасы при необходимости.

**Usages relevant to this task:**
- `server_facade.md`: контракт ответов (400/404/409/500).

**CRITICAL: `CODEMANIFEST` files — read-only contract definitions. Do NOT modify them.**

- [ ] **Negative: `TestDialog_AnswerTwice_Conflict409`** (дословно):
  - **Setup**: `answer.json` уже существует.
  - **Input**: повторный POST `dialog/answer` с тем же `id`.
  - **Trace**: `os.OpenFile(O_EXCL)` → `os.IsExist` → HTTP 409 «question already answered».
  - **Assertions**: HTTP 409; второй ответ не перезаписал `answer.json`; `dialogAnswerFn` не вызван.
  - **Sufficiency**: гарантия единственной поставки ответа (idempotency против двойного клика/ретрая UI).
  - Эквивалент: `TestHandleDialogAnswer_DuplicateAnswer`.
- [ ] **Negative: `TestDialog_PathTraversal_Rejected400`** (дословно):
  - **Input**: `id="../../etc/passwd"`.
  - **Trace**: `isValidDialogID` → false → HTTP 400 «invalid question id».
  - **Assertions**: HTTP 400; файл за пределами `stageDir` не создан.
  - **Sufficiency**: защита от записи произвольного пути через crafted id.
  - Эквивалент: `TestHandleDialogAnswer_InvalidID`.
- [ ] **Run validation**: `go test ./pkg/server/... -run 'DialogAnswer|DuplicateAnswer|InvalidID|QuestionNotFound'` — зелёные.
- [ ] **Cross-check**: `goga schema --depends-on pkg/mcp` показывает `pkg/server`→`pkg/mcp`; `goga lint` 16/0.

### Task 26: `cmd/afm` — conformance, entrypoint + Поток A (main.go, run.go)  [verify/conform]

Проверка конформности `cmd/afm`, часть A (entrypoint + запуск флоу). Сущности: `resolveRootDir`, `fmDir`, `main`,
`newRootCmd` (`main.go`); `browserCmd`, `launchHostBrowserOpener`, `loadPrompts`, `newRunCmd`, `openBrowser`,
`resolveFlowPath`, `resolveRun` (`run.go`). Imports: `assets`, `pkg/config`, `pkg/docker` (+ Usages `docker_privilege_drop`),
`pkg/flow`, `pkg/orchestrator`, `pkg/proxy`, `pkg/server`, `pkg/state`. Usages: `cobra` (project).
Семантика: `resolveRootDir` (флаг>env>.); `newRunCmd` — **Поток A**: `resolveFlowPath` → `config.LoadFrom` → проверка
Docker (`DockerConfig.IsDockerEnabled` → `CheckClaudeDockerAuth`+`ScanCommands`+`ReExec`/`syscall.Exec`) → [если не Docker]
`resolveRun` (`Store`) → старт `Proxy` (`ProxyConfig.IsEnabled`→`BuildTransforms`+`CreateShim`) → `loadPrompts`
(`assets.ReadPrompt`) → `orchestrator.New(Options)` → `server.New` → `openBrowser`. **Docker нюанс:** браузер открывает
host-side opener (`launchHostBrowserOpener`); внутри контейнера (`AFM_IN_DOCKER=1`) `openBrowser` пропускается.
**proxy нюанс:** shim/`ANTHROPIC_BASE_URL` инжекция (`cmd/afm` → `pkg/proxy`).

**Usages relevant to this task:**
- `cobra`: `newRootCmd` с persistent `--dir` + `PersistentPreRunE` (приоритет флаг>env>.); `&cobra.Command{RunE:…}`, `root.AddCommand(…)`.
- `docker_privilege_drop` (imported, `pkg/docker/.usages/docker_privilege_drop.md`): `ReExec` (gosu/HOME/uid-drop), `AFM_IN_DOCKER`/`AFM_HOST_UID/GID`.

**CRITICAL: `CODEMANIFEST` files — read-only contract definitions. Do NOT modify them. If implementation does not match the contract, fix the implementation — never fix the contract.**

- [ ] **STEP 0 (DECLARATION)**: Task 26 (`cmd/afm` entrypoint + Поток A conformance).
- [ ] **STEP 1 (CONTRACT TESTS)**: contract-тесты на фасад (`afm`-пакет: `resolveRootDir`, `fmDir`, `newRootCmd`,
  `newRunCmd`, `resolveFlowPath`, `resolveRun`, `loadPrompts`). Покрыто `cmd/afm/*_test.go`.
- [ ] **STEP 2 (IMPLEMENTATION)**: проверить `main.go`/`run.go` — `resolveRootDir` (флаг>env>.), `PersistentPreRunE`,
  `newRunCmd` (Поток A: config → docker check → store → proxy → prompts → orchestrator → server → browser),
  `launchHostBrowserOpener`/`openBrowser` (Docker-skip). Зазоров нет; править код при находке.
- [ ] **STEP 3 (INTERFACE VERIFICATION)**: `go test ./cmd/afm/...` — зелёные.
- [ ] **STEP 4 (LOGIC TESTS)**: поведение — `resolveRootDir` приоритет, `newRunCmd` wiring. Покрыто suite; расширить при непокрытом.
- [ ] **STEP 5 (DEBUGGING)**: `go test ./cmd/afm/...` — фиксить код до зелёного.
- [ ] **STEP 6 (CONTRACT RE-VERIFICATION)**: соответствие `CODEMANIFEST` (Algorithm `cmd/afm` newRunCmd — proxy/docker нюансы).
- [ ] **STEP 7 (LINT)**: `go vet ./cmd/afm/...`; `goga lint` 16/0.
- [ ] **STEP 8 (COMPLETION)**: отметить чекбоксы.

### Task 27: `cmd/afm` — conformance, CLI-команды (approve/check/retry/revise/init/list/install_skills)  [verify/conform]

Проверка конформности `cmd/afm`, часть B (CLI-команды). Сущности: `findLatestRunDir`, `newApproveCmd` (`approve.go`);
`lastLogAction`, `newCheckCmd`, `statusColor` (`check.go`); `newRetryCmd` (`retry.go`); `newReviseCmd` (`revise.go`);
`newInitCmd`, `prompt`, `splitComma`, `stageInput` (`init.go`); `newListCmd` (`list.go`); `installSkills`,
`newInstallSkillsCmd`, `resolveSkillsDir` (`install_skills.go`). Imports: `pkg/state` (`Store`, `Transition`, `RunState`),
`assets` (`SkillsFS`). Usages: `cobra`. Семантика: approve/retry/revise — `store.Apply(Transition{…})` (осознанное
исключение FSM-only `Apply` constraint — CLI-мутации без живого Orchestrator); check/approve читают `RunState`;
`newInstallSkillsCmd` — `SkillsFS`→копирование (без `--force` не перезаписывает существующее).

**Usages relevant to this task:**
- `cobra`: `newApproveCmd`/`newCheckCmd`/`newRetryCmd`/`newReviseCmd`/`newInitCmd`/`newListCmd`/`newInstallSkillsCmd` — `&cobra.Command{RunE:…}`.
- `store_facade.md`: `Store.Apply`/`Transition` (CLI-мутации — осознанное исключение линтера).

**CRITICAL: `CODEMANIFEST` files — read-only contract definitions. Do NOT modify them. If implementation does not match the contract, fix the implementation — never fix the contract.**

- [ ] **STEP 0 (DECLARATION)**: Task 27 (`cmd/afm` CLI-команды conformance).
- [ ] **STEP 1 (CONTRACT TESTS)**: contract-тесты на фасад (`newApproveCmd`, `newCheckCmd`, `newRetryCmd`,
  `newReviseCmd`, `newInitCmd`, `newListCmd`, `newInstallSkillsCmd`, `installSkills`, `resolveSkillsDir`, `findLatestRunDir`).
  Покрыто `cmd/afm/*_test.go`.
- [ ] **STEP 2 (IMPLEMENTATION)**: проверить `approve.go`/`check.go`/`retry.go`/`revise.go`/`init.go`/`list.go`/`install_skills.go` —
  approve/retry/revise → `store.Apply(Transition{…})`, check → `RunState`, install_skills → `SkillsFS` копирование (no-overwrite).
  **Constraint:** CLI-мутации `Store.Apply` — осознанное исключение `tools/setstatuslinter` (без живого Orchestrator).
  Зазоров нет; править код при находке.
- [ ] **STEP 3 (INTERFACE VERIFICATION)**: `go test ./cmd/afm/...` — зелёные.
- [ ] **STEP 4 (LOGIC TESTS)**: поведение — approve/retry/revise transitions, check reads state, install_skills no-overwrite.
  Покрыто suite; расширить при непокрытом.
- [ ] **STEP 5 (DEBUGGING)**: `go test ./cmd/afm/...` — фиксить код до зелёного.
- [ ] **STEP 6 (CONTRACT RE-VERIFICATION)**: соответствие `CODEMANIFEST`.
- [ ] **STEP 7 (LINT)**: `go vet ./cmd/afm/...`; `goga lint` 16/0.
- [ ] **STEP 8 (COMPLETION)**: отметить чекбоксы.

---

## Validation Commands

- `export PATH="$HOME/.local/bin:$PATH" && goga lint`: проверка структуры всех 16 клеточек — ожидается `cells: 16 errors: 0`.
- `export PATH="$HOME/.local/bin:$PATH" && goga schema`: иерархия 16 клеточек, 0 циклов — сверить с диаграммой дизайн-дока §3.
- `go build ./...`: весь проект компилируется (embed `dashboard/*` резолвится) — exit 0.
- `go vet ./...`: статический анализ — exit 0.
- `go test ./...`: весь тест-suite зелёный (включая новые тесты `pkg/web`).
- `go doc <pkg>` / `go doc -all <pkg>`: проверка доступности фасада каждой клеточки (все экспортированные идентификаторы импортируемы).
- Per-cell: `go test ./<cell>/...` + `go vet ./<cell>/...` — cell-scoped верификация в каждой задаче (STEP 3/5/7).

---

## Completion Criteria

- [ ] Каждая контракт-сущность из `CODEMANIFEST` 16 клеточек покрыта задачей (верифицирована в правильном `location`).
- [ ] Каждая клеточка доступна с фасада (`go build ./...` + `go doc`).
- [ ] Сигнатуры/поведение соответствуют контракту (после Applied Fixes расхождений нет).
- [ ] Контракт-зависимости (`Imports`) удовлетворены; рёбра `goga schema` совпадают с дизайн-доком §3.
- [ ] Все 7 project-level usages и 3 imported usages упомянуты минимум в одной задаче (§Usages Analysis).
- [ ] 3 нюанса CLAUDE.md сохранены в annotations соответствующих клеточек (dialog, proxy ZAI, docker privilege-drop).
- [ ] Каждая coding-задача прошла TDD (contract tests → verify/implementation → verification → logic tests → debugging → re-verification → lint).
- [ ] Все 8 сценариев дизайн-дока §Test Stack Trace назначены владеющей клеточке и верифицированы
  (`TestDialog_AnswerDeliveredToAgent_BashLoop`, `TestProxy_ZAI_NonStreamingReassembled`,
  `TestDialog_AnswerTwice_Conflict409`, `TestDialog_PathTraversal_Rejected400`,
  `TestProxy_UpstreamNon200_PassedThrough`, `TestDocker_HomeNotDroppable_HomeAfterGosu`,
  `TestWeb_Embed_ServesOriginalWebPaths_AfterDirMove` — genuine gap, добавлен Task 12,
  `TestDialog_AnswerArrivesBeforeQuestion_InJsonl`).
- [ ] Никакая граница пакета не расширена; ни одна клеточка не создана заново; `cells/`-каркас не введён.
- [ ] `CODEMANIFEST`-файлы не модифицированы (контракт read-only) — каждая задача несёт CRITICAL-баннер.
- [ ] Перенос `pkg/web/dashboard` НЕ повторяется (уже выполнен `goga-apply`); `go build ./...` подтверждает embed.
- [ ] Версия Go в `go.mod` не изменена (`go 1.26`).
- [ ] Все Validation Commands проходят: `goga lint` 16/0, `goga schema` 16 клеток/0 циклов, `go build ./...`, `go vet ./...`, `go test ./...` — зелёные.
