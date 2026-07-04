# Design Document: `goga-claude`

> Миграция работающего кода `afm` на архитектуру клеточек Goga — реверс-инжиниринг as-is публичного API
> в 16 `CODEMANIFEST`-контрактов. Поведение кода не меняется. Этот документ — архитектурная спецификация
> (что и как), полученная трассировкой уже материализованных контрактов (`goga-apply`) против реального
> исходного кода. Источник задачи: `docs/tasks/goga-claude.md`; архитектурный план: `docs/arch/goga-claude.md`.

---

## Contract Changes

### Changed CODEMANIFEST Files

Все 16 `CODEMANIFEST` — **новые** (merge-base `master` = `3659d40`, в нём ни одного `CODEMANIFEST` не
существовало; `git ls-tree master` подтверждает отсутствие). Это from-scratch набор контрактов, не
инкрементальное изменение.

| # | Клеточка (слой) | Файл | Содержание контракта |
|---|---|---|---|
| 1 | `pkg/config` (0) | `pkg/config/CODEMANIFEST` | `Default`, `LoadFrom`, `Config`, `ClientConfig`, `ExecutorConfig`, `ServerConfig`, `ProxyConfig`, `TransformOverrides`, `DockerConfig` |
| 2 | `pkg/flow` (0) | `pkg/flow/CODEMANIFEST` | `ParseFile`, `Flow`, `Stage`, `Artifact`, `Input` |
| 3 | `pkg/state` (0) | `pkg/state/CODEMANIFEST` | `FindLatestRunDir`, `SaveFeedback`, `VersionPlan`, `SetApplyHook`, `RunState`, `StageState`, `Store`, `Transition` |
| 4 | `pkg/progress` (0) | `pkg/progress/CODEMANIFEST` | `Lock`, `Logger` |
| 5 | `pkg/mcp` (0) | `pkg/mcp/CODEMANIFEST` | `AppendQuestion`, `AppendAnswer`, `FindUnansweredQuestions`, `FindEntry`, `ReadDialog`, `Question`, `Answer`, `Entry`, `QuestionFile` |
| 6 | `pkg/proxy` (0) | `pkg/proxy/CODEMANIFEST` | `Transform`, `BuildTransforms`, `ZAITransform`, `Proxy`, `CreateShim` |
| 7 | `pkg/web` (0) | `pkg/web/CODEMANIFEST` | `FS` |
| 8 | `pkg/web/dashboard` (0) | `pkg/web/dashboard/CODEMANIFEST` | `DashboardAssets` |
| 9 | `assets` (0) | `assets/CODEMANIFEST` | `ReadPrompt`, `FS`, `SkillsFS` |
| 10 | `tools/setstatuslinter` (0) | `tools/setstatuslinter/CODEMANIFEST` | `Analyzer` |
| 11 | `pkg/prompts` (1) | `pkg/prompts/CODEMANIFEST` | `Build`, `EscapeTagsForReprompt`, `Inputs`, `PlanIssues`, `ValidatePlan` |
| 12 | `pkg/docker` (1) | `pkg/docker/CODEMANIFEST` | `CheckClaudeDockerAuth`, `ReExec`, `ScanCommands`, `SetExecFunc`, `ResetExecFunc`, `CommandMount`, `ReExecConfig` |
| 13 | `pkg/executor` (1) | `pkg/executor/CODEMANIFEST` | `DefaultClaudeArgs`, `ResolveArgs`, `ParseToolAction`, `WrittenFiles`, `DialogTranscript`, `TranscriptItem`, `Config`, `Runner`, `Runner::Executor` |
| 14 | `pkg/orchestrator` (2) | `pkg/orchestrator/CODEMANIFEST` | `IsTerminal`, `FSM`, `GuardCtx`, `Rule`, `Classify`, ошибки, `UIBus`, `CriticalBus`, `Event`, `Graph`, `CollectArtifacts`, `CollectDependencyPlans`, `Options`, `Prompts`, `Orchestrator` |
| 15 | `pkg/server` (3) | `pkg/server/CODEMANIFEST` | `Config`, `Server` |
| 16 | `cmd/afm` (4) | `cmd/afm/CODEMANIFEST` | `resolveRootDir`, `fmDir`, `newRootCmd`, `newRunCmd`, …, `main` |

Карта зависимостей из `goga schema` (16 клеточек, 0 циклов) дословно совпадает с диаграммой §3
`docs/arch/goga-claude.md` — см. «Entity Interaction and Data Flow» ниже.

### New Entities

Все перечисленные выше сущности — новые (контракты ранее отсутствовали).

### Changed Entities

Нет — ни одной существующей клеточки для модификации (`goga schema` до миграции возвращал `[]`).

### Deleted Entities

Нет.

### Usages and Annotations Changes

- 7 project-level usage-файлов подключены через заголовок `Usages` потребляющих клеточек (см. §5
  arch-плана): `cobra`→`cmd/afm`, `gorilla_websocket`→`pkg/server`, `rapid`→`pkg/orchestrator`,
  `x_sys_windows`→`pkg/progress`, `x_term`→`pkg/docker`, `x_tools_analysis`→`tools/setstatuslinter`,
  `yaml_v3`→`pkg/config`, `pkg/flow`.
- 3 imported cell-level usages подключены через `Imports.Usages`: `dashboard_assets`
  (`pkg/web`←`pkg/web/dashboard`), `dialog_protocol` (`pkg/orchestrator`←`pkg/mcp`),
  `docker_privilege_drop` (`cmd/afm`←`pkg/docker`).
- 13 cell-level `.usages/`-файлов созданы (см. «`.usages/` Update»).

---

## Applied Fixes

Стадия design запускается по уже материализованным (lint-clean по заявлению `goga-apply`) контрактам.
Однако живой прогон `goga lint` (бинарь установлен в этой сессии) выявил **4 дефекта** в
`pkg/orchestrator/CODEMANIFEST`, которые выборочные ревизии `brainstorm-review` (без реального `goga`)
упустили. Все 4 — следствие конфликта между enum-конвенцией goga («строковые enum'ы документируются как
значения в annotations, не как top-level типы») и линтер-правилами (`import_type_exists`,
`import_is_used`, `import_has_not_duplicate`). Исправлено автономно (стадия non-interactive),
`goga lint` перепроверен: **cells: 16, errors: 0**.

### Fixed CODEMANIFEST Defects

- **`pkg/orchestrator`**: `[import_has_not_duplicate]` — тип `Config` импортирован из двух клеточек
  (`pkg/config` и `pkg/executor AS ExecutorConfig`). Линтер проверяет **исходное имя** типа
  (`item.type_name`), алиас `AS` не размыкает коллизию (dsl.md:145 описывает `AS` для разрешения
  конфликтов, но правило `import_has_not_duplicate` жёстче — см. исходник правила). **До:** импорт
  `Config` + `Config AS ExecutorConfig`. **После:** импорт `config.Config` оставлен (поле
  `Options.Config` требует его в сигнатуре); формальный импорт `executor.Config` удалён, клеточечная
  зависимость `pkg/executor` сохранена через `Runner` (поле `Options.Runner`). Конструктор
  `executor.New(executor.Config{...})` — внутренняя деталь сборки Runner, документирован в клеточке
  `pkg/executor`. (Причина: корректность по линтеру; поведение/Go-зависимость не изменились.)
- **`pkg/orchestrator`**: `[import_is_used]` + `[import_type_exists]` — `StageStatus` импортирован из
  `pkg/state`, но не используется формальной backtick-ссылкой и не объявлен в `pkg/state` (по
  enum-конвенции это перечисление значений в annotations, не top-level тип). **До:** `StageStatus` в
  `Imports` из `pkg/state`. **После:** удалён из импорта; значения (pending/planning/…/failed)
  остаются в prose-аннотациях; клеточечное ребро `orchestrator→state` сохранено через
  `Store`/`RunState`/`StageState`/`Transition`.
- **`pkg/orchestrator`**: `[import_type_exists]` — `Agent` импортирован из `pkg/prompts`, но не
  объявлен там (enum-конвенция). **До:** `Agent` в `Imports` из `pkg/prompts`. **После:** удалён;
  backtick `` `Agent` `` в аннотации `Run` заменён на prose `Agent (AgentPlanning/AgentImplementation/
  AgentReview)`; ребро `orchestrator→prompts` сохранено через `Inputs`/`PlanIssues`.

Никакие другие дефекты при живом `goga lint`/`goga schema` не обнаружены. `goga schema` строит все 16
клеточек без ошибок парсинга.

---

## Entity Interaction and Data Flow

### Interaction Diagram

```
                    cmd/afm (4)  ──entrypoint──►  process
                   ╱   │  │  │  ╲╲
        ┌─────────┘   │  │  │   ╲╲
        ▼             ▼  │  │    ▼
   pkg/proxy    pkg/server(3)   assets ──(embedded FS: prompts/skills)
   (ZAI shim)   ╱   │  │  ╲╲
              ╱     │  │   ╲╲
        pkg/orchestrator(2)   pkg/web ──Usages──► pkg/web/dashboard
        ╱  │  │  │  ╲╲
       ╱   │  │  │   ╲╲
   pkg/   pkg/ pkg/ pkg/ pkg/
   config executor flow mcp prompts ──► pkg/flow
     │      │              │
     │      ▼              │ file-based dialog protocol
     │   pkg/progress      │ (question.json/answer.json)
     │                      ▼
     └─────────────►  pkg/state (Store/FSM-via-fsm.go)

   pkg/docker(1)──►pkg/flow     tools/setstatuslinter(0) — независимый линтер (AST)
```

Рёбра соответствуют выводу `goga schema` 1:1 (см. «Contract Changes»). Слои строго bottom-up:
клеточка слоя N импортирует только клеточки слоя < N. Циклов нет.

### Data Flows

**Поток A — запуск флоу (`cmd/afm.newRunCmd` → Orchestrator):**
`resolveFlowPath` → `config.LoadFrom` (`Config`) → проверка Docker (`DockerConfig.IsDockerEnabled` →
при включении `CheckClaudeDockerAuth` + `ScanCommands` + `ReExec`/`syscall.Exec`, процесс замещается) →
[если не Docker] `resolveRun` (`Store`) → старт `Proxy` (`ProxyConfig.IsEnabled` → `BuildTransforms` +
`CreateShim`) → `loadPrompts` (`assets.ReadPrompt`) → `orchestrator.New(Options)` → `server.New` →
`openBrowser`. Участвующие сущности: `cmd/afm`, `pkg/config`, `pkg/docker`, `pkg/proxy`, `assets`,
`pkg/state`, `pkg/orchestrator`, `pkg/server`.

**Поток B — file-based dialog protocol (агент ↔ пользователь):**
Агент пишет `planning.qN.question.json` в `$AFM_STAGE_DIR` (исполнитель = `pkg/executor`, env
`AFM_STAGE_DIR`) → `Orchestrator.startQuestionPoller` (1 Гц) → `mcp.FindUnansweredQuestions` →
`mcp.AppendQuestion` в `dialog.jsonl` (best-effort история) → `Orchestrator` публикует `EventAskUser` +
`Trigger(EvAskUser)` → стадия в `awaiting_user_input`. UI показывает вопрос (`/api/stages/<id>/dialog`).
Пользователь отвечает → `pkg/server.handleDialogAnswer` → атомарная запись `answer.json` (O_EXCL) →
`Orchestrator.NotifyAnswer`: если агент активен — `Trigger(EvUserAnswered)` (bash-цикл агента сам найдёт
файл); иначе публикация в `CriticalBus` → `onUserAnswered` перезапускает агент с `--resume`.

**Поток C — proxy ZAI (агентский HTTP-трафик):**
Агент (`pkg/executor`, обёрнутый shim'ом `pkg/proxy.CreateShim`) → HTTP-запрос на
`ANTHROPIC_BASE_URL`=<proxy> → `Proxy.ServeHTTP` → первый подходящий `Transform` (`ZAITransform.Match`
по хосту `api.z.ai`) → для non-streaming: инъекция `stream:true` → upstream → `parseSSE` собирает
`message_start`/`content_block_*`/`message_delta` → единый JSON `message` агенту. Streaming и не-z.ai
upstream — passthrough.

**Поток D — WebSocket-стриминг событий:**
`pkg/server` держит `*orchestrator.UIBus` → `Subscribe` → каждое `Event` пишется как JSON-текст в
WebSocket (`gorilla_websocket`) → UI dashboard (`pkg/web/dashboard/app.js`) рендерит изменения статусов.

### Entity Dependencies

Порядок инициализации (bottom-up, совпадает с порядком материализации `goga-apply`): Layer 0 (10
листьев) → Layer 1 (`prompts`←`flow`, `docker`←`flow`, `executor`←`progress`) → Layer 2
(`orchestrator`←`config`/`executor`/`flow`/`mcp`/`prompts`/`state`) → Layer 3 (`server`←`executor`/
`mcp`/`orchestrator`/`state`/`web`) → Layer 4 (`cmd/afm` — терминальная вершина графа, никем не
импортируется). Единственное физическое перемещение файлов (предварительный шаг `goga-apply`):
не-`.go`-ассеты `pkg/web/*` → `pkg/web/dashboard/*` + правка `//go:embed dashboard/*` (выполнено, `go
build ./...` проходит).

---

## Code Stack Trace

Трассировка выполнена по реальному исходному коду (не по предположению). Для каждой клеточки —
контрактные точки входа; глубина пропорциональна риску. Чекпойнты «passed» = тип/логика сверены с
исходником; «defect» не зафиксировано ни на одном чекпойнте после Applied Fixes.

### Trace: `pkg/mcp` — file-based dialog protocol (высокий риск)

`mcp.FindUnansweredQuestions(stageDir)` → `filepath.Glob(*.question.json)` → для каждого: разбор
`<phase>.<id>`, фильтр фаз (planning/implementation/review), `os.Stat` парного `.answer.json`
(пропуск если есть) → `QuestionFile{Phase,ID,…}`. **Checkpoint (passed):** формат имени и фильтр фаз
совпадают с `isValidDialogID`/валидацией фазы в `pkg/server/handlers.go`.

`mcp.ReadDialog(path)` → построчное сканирование JSONL → probe `{id, answer}`: если `answer != nil` —
`Answer`, иначе `Question`; группировка по ID в хронологическом порядке первого вопроса. **Checkpoint
(passed, нюанс):** обработка случая «ответ пришёл раньше вопроса» (разные горутины пишут `.jsonl`) —
поля вопроса дозаполняются в существующий `Entry` (dialog.go:112-127); это отражено в контракте
(`Entry` с опциональным `Answer`).

`mcp.AppendAnswer` → `appendLine` (O_APPEND|O_CREATE, сериализован `appendMu`). **Checkpoint (passed,
критический нюанс):** `AppendAnswer` **НЕ** реализует эксклюзивную (O_EXCL) поставку ответа — это
best-effort история для UI (`<phase>.dialog.jsonl`). Атомарная O_EXCL-поставкa `answer.json`
(критический путь) реализована отдельно, напрямую в `pkg/server/handlers.go`. Контракт явно фиксирует
это разделение (Algorithm в `pkg/mcp`).

### Trace: `pkg/orchestrator` — dialog poller + NotifyAnswer (высокий риск)

`Orchestrator.startQuestionPoller` → горутина с `time.Ticker(1s)` → `pollQuestions(processed)`.
`pollQuestions`: `Store.Snapshot` → для стадий в активных статусах (planning/running/revising/retrying/
awaiting_user_input) → `mcp.FindUnansweredQuestions` → дедуп `processed["stageID|phase|id"]` →
`mcp.AppendQuestion` (идемпотентно через `FindEntry`) → `ui.Publish(EventAskUser)` +
`Trigger(EvAskUser)`. **Checkpoint (passed):** при отсутствии открытых вопросов у интерактивной стадии
— `detectDialogViolation` (fail-fast, если агент написал `*.question.json` вне stageDir).

`Orchestrator.NotifyAnswer(stageID,phase,qID,answer,fromOptions)`: если `isAgentActive(stageID)` →
`Trigger(EvUserAnswered)` + `ui.Publish`; **иначе** → `critical.Publish` (для `onUserAnswered`,
перезапуск с `--resume`). **Checkpoint (passed):** две ветви совпадают с Algorithm контракта шаг 4;
`activeAgents` — `sync.Map` (Concurrency, см. ниже).

### Trace: `pkg/server.handleDialogAnswer` (высокий риск, критический путь O_EXCL)

`handleDialogAnswer`: `extractStageID` → `isValidStageID` (400) → декодинг тела → проверка
id/phase/answer (400) → фаза ∈ {planning,implementation,review} (400) → `isValidDialogID(req.ID)`
(path-traversal guard, 400) → `os.Stat(question.json)` (404 если нет) → чтение/парсинг question.json →
валидация ответа против `Options` при `allow_custom=false` (400) → **атомарная запись**
`answer.json` (`os.OpenFile(…,O_WRONLY|O_CREATE|O_EXCL,0644)` → 409 Conflict если уже есть) →
`Write`+`Sync`+`Close` (с `_ = os.Remove` при сбое) → re-check `question.json` всё ещё есть (TOCTOU,
400 + remove) → `mcp.AppendAnswer` в `dialog.jsonl` (best-effort, НЕ критический) → `dialogAnswerFn`
(notify). **Checkpoint (passed):** порядок «сначала answer.json (O_EXCL), потом история» гарантирует,
что bash-цикл агента всегда найдёт ответ, даже если запись истории упадёт; совпадает с Algorithm
`pkg/server`.

### Trace: `pkg/proxy.ZAITransform.ServeHTTP` (высокий риск)

`ServeHTTP`: `io.ReadAll(r.Body)` → если non-JSON или `streamRequested` → `passthroughTo(upstream)`.
Иначе `bj["stream"]=true` → `http.NewRequestWithContext` на `upstream+r.URL.RequestURI()` → копирование
заголовков (без content-length) → `http.DefaultClient.Do` → `io.ReadAll(resp.Body)`. Если статус ≠ 200
→ проброс статуса+заголовков+тела. Иначе `parseSSE(sseBytes)`: при `apiErr` → `writeSSEError(529)`;
иначе `json.Marshal(msg)` → `200 application/json`. **Checkpoint (passed):** `BuildTransforms(upstream,
*zai)` — `nil`→автоопределение по `api.z.ai`, `true`→всегда, `false`→никогда; совпадает с контрактом.
**Checkpoint (known limitation, не дефект):** `http.DefaultClient` без явного таймаута (рассчитывает на
context); `[DONE]`-терминатор ожидает `\n` (Anthropic).

### Trace: `pkg/docker.ReExec` (высокий риск, privilege-drop)

`ReExec(cfg)`: `exec.LookPath("docker")` (фатально если нет) → `os.UserHomeDir` (фатально если пусто) →
`args := docker run --rm` (+ `-it` если `isTTY()`=`term.IsTerminal`) → `-p` при `DashboardPort>0` →
монтирования: проект same-path, `~/.claude`+`~/.afm`→`containerHome`, **намеренно НЕ `~/.claude.json`**
(corruption при атомарном rename `:ro`), команды `:ro`, extra-mounts (`~`→containerHome) → env:
`AFM_IN_DOCKER=1`, `AFM_HOST_UID/GID` (entrypoint дропает gosu до них), секреты в bare-форме `-e KEY`
(не светятся в argv/ps) → `execFunc(dockerBin,args,os.Environ())` (`syscall.Exec`, не возвращает).
**Checkpoint (passed):** привилегии дропаются до хостового uid/gid через entrypoint+gosu; `HOME`
выставляется ПОСЛЕ gosu (gosu сбрасывает HOME для uid без записи в `/etc/passwd`); совпадает с
Algorithm `pkg/docker` и CLAUDE.md «Docker Mode». `isTTY` — честная проверка через `term.IsTerminal`
(`os.ModeCharDevice` ложно срабатывал на `/dev/null`).

### Trace: `pkg/web` embed-split (высокий риск, но тривиальный контракт)

`embed.go`: `//go:embed dashboard/*` → `var embedded embed.FS` → `fs.Sub(embedded, "dashboard")` →
`FS`. **Checkpoint (passed):** `fs.Sub` пере-рулит встраивание в корень `dashboard/`, поэтому
относительные веб-пути (`index.html`, `style.css`, …) не меняются — перенос ассетов в поддиректорию
изменил только путь embed-директивы, не поведение `FS`. `pkg/server` отдаёт корень `/` через
`http.FileServer(http.FS(web.FS))`.

### Trace: остальные клеточки (умеренный/низкий риск, сверено с исходником)

- **`pkg/config.LoadFrom`**: читает `~/.afm/config.yaml` и `.afm/config.yaml` (`yaml_v3`), отсутствующие
  молча игнорируются, мердж; опциональность — указатель + геттер с дефолтом (`ServerConfig.GetPort`→9876,
  `IsOpenBrowser`→true, `ProxyConfig.IsEnabled`→true при nil, `DockerConfig.IsDockerEnabled`→по env).
  **Passed.**
- **`pkg/flow.ParseFile`**: `yaml_v3`-парсинг `flow.yaml` → `Flow{Stages}`; `Input.UnmarshalYAML` — из
  строки `"stage.artifact"` или объекта `{ref,optional}`. `Stage.HasAgent`/`ImplAgent`/`NeedsPlanning` —
  selection-логика агентов. **Passed.**
- **`pkg/state.Store`**: `Open` создаёт `state.json`+event log; `Apply(t Transition)` — fsync+перезапись
  снапшота (через `SetApplyHook` между fsync и записью — тестовый хук); `Snapshot`→`RunState`.
  **Constraint (passed):** `(*Store).Apply` внутри `pkg/orchestrator` — только из `fsm.go`
  (статически проверяется `tools/setstatuslinter`); `cmd/afm` approve/retry/revise — осознанное
  исключение (CLI-мутации без живого Orchestrator). **Passed.**
- **`pkg/progress`**: `Lock` — платформенно-специфичный flock (`x_sys_windows` на Windows, `syscall.Flock`
  на Unix); `Logger` — append-only лог с метками времени + stdout. **Passed.**
- **`pkg/executor`**: `DefaultClaudeArgs` (`--print --output-format=stream-json --verbose` — `--verbose`
  обязателен для Claude 2.1.x и включает tool_use-события); `ResolveArgs` дедуплицирует; `Runner.RunAgent`/
  `RunPlanning` порождают процесс, стримят stdout/stderr, пишут лог через `progress.NewLogger`;
  `Config.ProxyURL`/`ProxyShimDir` инжекят `ANTHROPIC_BASE_URL`/`AFM_PROXY_URL` и prepends shim в `PATH`
  (вычищая существующий `ANTHROPIC_BASE_URL`). `DialogTranscript`/`WrittenFiles` парсят stream-json.
  **Checkpoint (passed, нюанс `proxyForCmd`):** `Orchestrator.proxyForCmd` — команда `claude` НЕ получает
  proxy (OAuth→api.anthropic.com; z.ai не принимает OAuth), только не-claude врапперы. Это внутренняя
  маршрутизация, в контракте не выражена (деталь реализации Orchestrator) — зафиксировано здесь как
  cross-cutting note.
- **`pkg/prompts.Build`**: собирает промпт по `Inputs` (шаблон, стадия, артефакты, диалог, контекст);
  `ValidatePlan(md,required)`→`PlanIssues{MissingSections}`, `IsClean()`. **Passed.**
- **`pkg/server` (вне диалога)**: `Start` слушает порт; корень `/` — статика из `web.FS`; approve/revise/
  retry/dialog-cancel гейтятся `StageStatus` через `Store.Snapshot`; WebSocket — `UIBus.Subscribe`.
  **Passed.**
- **`cmd/afm`**: `resolveRootDir` (флаг>env>.); `newRunCmd` — Поток A; approve/retry/revise — `store.Apply(
  Transition{…})`, check/approve читают `RunState`; `newInstallSkillsCmd` — `SkillsFS`→копирование
  (без `--force` не перезаписывает существующее). **Passed.**
- **`assets`**: `ReadPrompt(name,overrideDir)` + `FS`/`SkillsFS` (embed). **Passed.**
- **`tools/setstatuslinter.Analyzer`**: `go/analysis` (`x_tools_analysis` singlechecker) — запрещает
  `(*state.Store).Apply` вне `pkg/orchestrator/fsm.go` (область `./pkg/...`, `_test.go` исключены).
  **Passed.**

---

## Algorithm Design

### `pkg/mcp` (file-based dialog protocol)
**Ответственность:** читать/писать JSON-записи вопрос/ответ и собирать историю диалога.
```
1. AppendQuestion/AppendAnswer → appendLine(path, v):
   marshal → если >1MB, ошибка → Lock(appendMu) → OpenFile(O_APPEND|O_CREATE|O_WRONLY,0644) → Write+\n
   → (serialize concurrent writers of one file; НЕ эксклюзивное создание)
2. FindUnansweredQuestions(stageDir): Glob *.question.json → разбор <phase>.<id> → фильтр фаз →
   пропуск при наличии парного *.answer.json → QuestionFile
3. ReadDialog(path): построчно → probe answer/question → group-by-ID в порядке первого вопроса
   → обработка «ответ раньше вопроса» дозаполнением полей
```
**Errors:** отсутствующий файл → (`ReadDialog`) `nil,nil`; (`appendLine`) wrap `open append`/`write`.
**Edge cases:** запись >1MB → ошибка; конкурентная запись двух горутин → `appendMu`; ответ пришёл
раньше вопроса → дозаполнение.

### `pkg/orchestrator` (FSM + buses + graph + dialog)
**Ответственность:** жизненный цикл рана через событийный цикл.
```
FSM (fsm.go): Apply(stageID,ev,ctx,reason) → ruleAllowsFrom(From, текущий статус) →
   если переход разрешён → Store.Apply(Transition{from,to,ev,reason}) → вернуть (to,applied,nil)
   EvUserAnswered → phaseDispatch (по GuardCtx.Phase)
Buses: UIBus — pub/sub для pkg/server (Subscribe→канал, SubscriberDroppedCount при переполнении);
   CriticalBus — гарантированная доставка для onUserAnswered
Graph: ReadyStages(statuses) — стадии в ready, все зависимости которых done
Dialog: startQuestionPoller(1Гц) → pollQuestions (см. Code Stack Trace) → EventAskUser/Trigger(EvAskUser);
   NotifyAnswer → (активен) Trigger(EvUserAnswered) | (не активен) critical.Publish → onUserAnswered resume
```
**Errors:** `Classify(err)`→`ClassNone/Retryable/Incomplete/MissingArtifact/MissingSections/Fatal/
StorageFatal`; `MissingSectionsError.Missing`←`PlanIssues.MissingSections` (после `!IsClean()`).
**Edge cases:** активный агент vs завершённый (две ветви NotifyAnswer); misplaced question →
`detectDialogViolation` fail-fast; переполнение буфера подписчика → `SubscriberDroppedCount`.

### `pkg/proxy` (ZAI transform)
**Ответственность:** реверс-прокси с точкой расширения `Transform`.
```
ServeHTTP (ZAI): read body → if non-JSON|streamRequested → passthrough; else inject stream:true →
   forward → if status≠200 → проброс; else parseSSE → if apiErr → 529; else JSON message
parseSSE: message_start (id/role/model/usage) + content_block_start/delta (text/thinking/tool_use/
   signature по индексу) + message_delta (stop_reason + merge usage) → единый message
```
**Errors:** upstream-non-200 → проброс как есть; SSE `error`/пустой SSE → HTTP 529 (Anthropic-style).
**Edge cases:** streaming-запросы и не-z.ai upstream → passthrough без изменений.

### `pkg/docker` (ReExec + privilege-drop)
**Ответственность:** перезапуск afm в Docker-контейнере с корректными томами/привилегиями.
```
ReExec: LookPath(docker) → собрать args (монтирования, env, секреты bare -e) → execFunc(syscall.Exec)
   Привилегии: контейнер под root → entrypoint gosu дропает до AFM_HOST_UID/GID → HOME=/home/afm ПОСЛЕ gosu
   ~/.claude.json НЕ монтируется (corruption); auth claude — через CLAUDE_CODE_OAUTH_TOKEN/ANTHROPIC_API_KEY env
```
**Errors:** docker не в PATH / home не определён → return err; при успехе управление не возвращается.
**Edge cases:** `isTTY`=`term.IsTerminal` (не ModeCharDevice); секреты не светятся в argv (`-e KEY` без
значения); нестандартные агенты `:ro`.

### `pkg/server` (HTTP + WebSocket + dialog answer)
**Ответственность:** дашборд, API стадий, стриминг событий, приём ответов диалога.
```
handleDialogAnswer: validate (stageID/body/phase/dialogID path-traversal) → Stat question.json (404) →
   validate answer vs Options (400 if !allow_custom) → OpenFile(answer.json, O_EXCL) (409 if exists) →
   Write+Sync+Close → re-Stat question.json (TOCTOU) → AppendAnswer (best-effort) → dialogAnswerFn (notify)
WebSocket: Upgrader(CheckOrigin *) → UIBus.Subscribe → write each Event as JSON-text
approve/revise/retry/dialog-cancel: gate by StageStatus via Store.Snapshot
```
**Errors:** 400 (невалидный ввод/path-traversal), 404 (нет question), 409 (уже отвечено), 500 (I/O).
**Edge cases:** TOCTOU (re-check question после записи); диалог НЕ гейтится StageStatus (только файлом).

### `pkg/executor`, `pkg/config`, `pkg/flow`, `pkg/state`, `pkg/progress`, `pkg/prompts`
(Кратко — см. Code Stack Trace; алгоритмы прямые: executor — spawn/stream/parse; config — merge+defaults;
flow — yaml-парсинг; state — fsync-snapshot+eventlog; progress — flock+log; prompts — шаблон+валидация.)

### `pkg/web`/`pkg/web/dashboard`/`assets`/`tools/setstatuslinter`/`cmd/afm`
(Минимальные/фасадные контракты — см. Code Stack Trace.)

---

## Cross-cutting Concerns

- **Error handling**: ошибки оборачиваются (`fmt.Errorf("…: %w", err)`); на уровне стадии —
  `Classify`→`Classification` решает retry/fatal; CLI-команды возвращают `error` в cobra; HTTP-хендлеры
  отвечают кодами (400/404/409/500) с текстом. Стратегия консистентна с сигнатурами (везде `err:error`).
- **Validation**: `isValidStageID`/`isValidDialogID` (path-traversal), валидация фаз диалога,
  `StageStatus`-гейты хендлеров, `ValidatePlan` (обязательные секции), `allow_custom`/Options-проверка
  ответа. Поведение на невалидных данных — ранний возврат с кодом/ошибкой.
- **Logging**: `progress.Logger` — человекочитаемый лог стадии (`<phase>.log`) + stdout; сырой
  stream-json — `<phase>.jsonl`; event log переходов — в `Store`. Уровней нет (всё info-level).
- **Caching**: нет (file-based состояние, без in-memory кеша).
- **Concurrency**: `Orchestrator.activeAgents` — `sync.Map` (активные горутины агентов); `mcp.appendMu` —
  `sync.Mutex` (сериализация писателей одного `.jsonl`); `UIBus`/`CriticalBus` — внутренне
  потокобезопасные pub/sub; `docker.execFunc` — изменяемое состояние уровня пакета (только
  последовательные тесты, НЕ `t.Parallel`). Проверено против сигнатур — консистентно.
- **Cross-cutting note (`proxyForCmd`)**: команда `claude` осознанно обходит встроенный proxy
  (OAuth→api.anthropic.com напрямую; z.ai не принимает OAuth-токены) — внутренняя маршрутизация в
  `pkg/orchestrator`, в DSL-контракте не выражена (деталь реализации, не часть публичного API).

---

## Usages Analysis

### Project-level practices (`.goga/usages/cooks/`)

- **`yaml_v3`** — YAML-парсинг с опциональностью через указатель+геттер. **Где:** `pkg/config`
  (`LoadFrom`), `pkg/flow` (`ParseFile`, `Input.UnmarshalYAML`). **Почему:** стандарт `gopkg.in/yaml.v3`
  для конфигов/моделей. **Как:** `yaml.Unmarshal` в struct с `*T`-полями + getter-метод с дефолтом.
- **`cobra`** — дерево CLI-команд. **Где:** `cmd/afm` (`newRootCmd` с persistent `--dir` +
  `PersistentPreRunE`). **Как:** `&cobra.Command{RunE:…}`, `root.AddCommand(…)`.
- **`gorilla_websocket`** — WebSocket-стриминг. **Где:** `pkg/server` (`Upgrader` один на пакет,
  `CheckOrigin *`, подписка на `UIBus`). **Как:** `Upgrader.Upgrade` → `WriteMessage(TextMessage,…)`.
- **`rapid`** — property-based тесты FSM. **Где:** `pkg/orchestrator` (тестовый контур `fsm_test.go`,
  не часть контракта). **Как:** документируется annotation-ссылкой, не тестовым файлом в контракте.
- **`x_sys_windows`** — файловые блокировки на Windows. **Где:** `pkg/progress` (`Lock`,
  `//go:build windows`). **Как:** `windows.LockFileEx`; Unix-ветка — `syscall.Flock` stdlib.
- **`x_term`** — честная TTY-детекция. **Где:** `pkg/docker` (`isTTY`→`term.IsTerminal` для решения
  по `-it`). **Почему:** `os.ModeCharDevice` ложно срабатывал на `/dev/null`.
- **`x_tools_analysis`** — статический анализатор. **Где:** `tools/setstatuslinter` (`singlechecker.Main`).
  **Как:** `go/analysis`-анализатор, запрет `Store.Apply` вне `fsm.go`.

### Imported Usages (`Imports.Usages`)

- **`dashboard_assets`** из `pkg/web/dashboard` — `pkg/web` (состав статических ассетов для embed).
  Path: `pkg/web/dashboard/.usages/dashboard_assets.md`.
- **`dialog_protocol`** из `pkg/mcp` — `pkg/orchestrator` (полный контракт вопрос/ответ, критический
  путь O_EXCL в `pkg/server`, а не в `mcp.AppendAnswer`). Path: `pkg/mcp/.usages/dialog_protocol.md`.
- **`docker_privilege_drop`** из `pkg/docker` — `cmd/afm` (механизм gosu/HOME/uid-drop).
  Path: `pkg/docker/.usages/docker_privilege_drop.md`.

---

## `.usages/` Update

Все 13 cell-level `.usages/`-файлов созданы стадией `goga-apply` (перечислены в §6.17 arch-плана).
Свёрка с оттрассированными контрактами: каждый описывает фасад потребителя («как вызывать клеточку»),
API совпадают с `CODEMANIFEST`. Дополнений/обновлений не требуется — состав API потребителя не менялся
(реверс-инжиниринг as-is). **CODEMANIFEST `Usages`-ссылок на собственные `.usages/` не добавлено**
(`.usages/` — документация для потребителя, не источник контракта; проверено: ни одна клеточка не
ссылается на собственный `.usages/`).

| Клеточка | Файл | Статус |
|---|---|---|
| `pkg/web/dashboard` | `dashboard_assets.md` | current — состав 5 ассетов совпадает с `embed.go` |
| `pkg/mcp` | `dialog_protocol.md` | current — протокол вопрос/ответ, O_EXCL в server — совпадает |
| `pkg/docker` | `docker_privilege_drop.md` | current — gosu/HOME/uid-drop — совпадает с `ReExec` |
| `pkg/config` | `config_facade.md` | current |
| `pkg/flow` | `flow_facade.md` | current |
| `pkg/state` | `store_facade.md` | current (включая FSM-only `Apply` constraint) |
| `pkg/web` | `embed_fs_facade.md` | current (`fs.Sub(embedded,"dashboard")`) |
| `pkg/executor` | `runner_facade.md` | current |
| `pkg/prompts` | `prompts_facade.md` | current |
| `pkg/orchestrator` | `orchestrator_facade.md` | current |
| `pkg/proxy` | `proxy_facade.md` | current |
| `pkg/server` | `server_facade.md` | current |
| `assets` | `assets_facade.md` | current |

`tools/setstatuslinter` не имеет `.usages/` (нет внутренних потребителей — проверено, §6.17 arch-плана).

---

## Test Stack Trace

Сценарии scoped на 4 наиболее рискованные точки входа (диалог, proxy, Docker privilege-drop, web-split),
как требует Acceptance Criteria (полное покрытие каждой экспортированной функции отнесено на downstream
`goga-plan`/implementation).

### General Setup
- Рантайм: Go 1.26, бинарь `afm`, временная директория рана (`runDir`) с `state.json` + директориями
  стадий. Прокси на `127.0.0.1:0`. Мок-агент — bash-скрипт, пишущий `*.question.json` в `$AFM_STAGE_DIR`.
- `pkg/docker`: `SetExecFunc` подменяет `syscall.Exec` (перехват аргументов без реального `docker run`);
  `ResetExecFunc` после. `t.Parallel` НЕ используется (изменяемое состояние пакета).

### Source File Registry
`pkg/mcp/dialog.go`, `pkg/server/handlers.go`, `pkg/orchestrator/orchestrator.go`, `pkg/proxy/zai.go`,
`pkg/docker/launcher.go`, `pkg/web/embed.go`, `pkg/state/store.go`.

---

### Positive Tests

#### `TestDialog_AnswerDeliveredToAgent_BashLoop`
**Setup**: стадия `implementation` в `awaiting_user_input` — опросчик `startQuestionPoller`
(1 Гц) уже перевёл её по `EvAskUser` (`From:[planning,running,retrying,revising]→
awaiting_user_input`, `fsm.go:65`) сразу после обнаружения `implementation.q1.question.json`.
Горутина агента ещё активна (`isAgentActive=true`, bash-цикл поллит `answer.json`).
`$AFM_STAGE_DIR/<runDir>/<stage>` содержит `implementation.q1.question.json`
(`{"id":"q1","question":"…","options":["A","B"],"allow_custom":true}`).

> ⚠ **Почему именно `awaiting_user_input`, а не `running`:** `EvUserAnswered` разрешён только
> `From:[awaiting_user_input]` (`fsm.go:66`). У стадии с зарегистрированным вопросом и активным
> опросчиком статус уже `awaiting_user_input`, а не `running`. Если бы стадия действительно была в
> `running`, `Trigger(EvUserAnswered)` был бы молча отклонён (`ruleAllowsFrom`→`applied=false`,
> `fsm.go:89-90`), и `answer.json` остался бы на диске, а стадия зависла бы в ожидании —
> утверждение ниже ложно.

**Input**: HTTP POST `/api/stages/<stage>/dialog/answer` body `{"id":"q1","phase":"implementation",
"answer":"A","from_options":true}`.
**Trace**:
```
handleDialogAnswer(req)
  → isValidDialogID("q1") = true
  → os.Stat(question.json) → exists (200 path)
  → allow_custom=true → skip Options-validation
  → os.OpenFile(answer.json, O_EXCL) → created (no conflict)
  → Write+Sync+Close → answer.json persisted
  → re-Stat(question.json) → still exists (TOCTOU ok)
  → mcp.AppendAnswer(dialog.jsonl) → best-effort ok
  → dialogAnswerFn = NotifyAnswer(stage,"implementation","q1","A",true)
    → isAgentActive(stage)=true → Trigger(EvUserAnswered)
      → FSM.Apply: EvUserAnswered From:[awaiting_user_input] (fsm.go:66) → phaseDispatch
        (phase=implementation ≠ planning → StatusRunning) → Store.Apply(Transition) — applied
      → ui.Publish(EventUserAnswered)
  → bash-цикл агента находит answer.json, читает "A", продолжается
```
**Assertions**: HTTP 200; `answer.json` существует с `{"id":"q1","answer":"A","from_options":true}`;
`dialog.jsonl` содержит ответ; FSM перевёл стадию из `awaiting_user_input` → `running`
(`EvUserAnswered` `From:[awaiting_user_input]`, `phaseDispatch` для implementation; **до** ответа
статус уже был `awaiting_user_input`, не `running`).
**Sufficiency**: гарантирует, что ответ доходит до работающего агента через критический путь O_EXCL —
регресс на «агент висит, хотя пользователь ответил».

#### `TestProxy_ZAI_NonStreamingReassembled`
**Setup**: upstream-сервер на `api.z.ai/api/anthropic`, отвечает SSE-потоком (`message_start` →
`content_block_delta` text → `message_delta` stop_reason=end_turn).
**Input**: POST non-streaming JSON (`{"model":"…","messages":[…]}`, без `stream`).
**Trace**:
```
ZAITransform.ServeHTTP → body non-streaming → inject stream:true → forward → read SSE →
  parseSSE → message{id,role,model,content:[…],stop_reason:end_turn,usage} → 200 application/json
```
**Assertions**: ответ — единый Anthropic JSON `message` с `stop_reason:"end_turn"`, текст собран из
deltas; исходящий запрос upstream содержал `"stream":true`.
**Sufficiency**: ядро workaround'а 529 — регресс на рассыпание SSE.

---

### Negative Tests

#### `TestDialog_AnswerTwice_Conflict409`
**Setup**: `answer.json` уже существует.
**Input**: повторный POST `dialog/answer` с тем же `id`.
**Trace**: `os.OpenFile(O_EXCL)` → `os.IsExist` → HTTP 409 «question already answered».
**Assertions**: HTTP 409; второй ответ не перезаписал `answer.json`; `dialogAnswerFn` не вызван.
**Sufficiency**: гарантия единственной поставки ответа (idempotency против двойного клика/ретрая UI).

#### `TestDialog_PathTraversal_Rejected400`
**Input**: `id="../../etc/passwd"`.
**Trace**: `isValidDialogID` → false → HTTP 400 «invalid question id».
**Assertions**: HTTP 400; файл за пределами `stageDir` не создан.
**Sufficiency**: защита от записи произвольного пути через crafted id.

#### `TestProxy_UpstreamNon200_PassedThrough`
**Setup**: upstream отвечает 500.
**Trace**: `resp.StatusCode != 200` → проброс статуса+заголовков+тела.
**Assertions**: клиент получил 500 + тело upstream; `parseSSE` не вызывался.
**Sufficiency**: upstream-ошибки не маскируются.

---

### Edge Case Tests

#### `TestDocker_HomeNotDroppable_HomeAfterGosu`
**Setup**: `SetExecFunc` перехватывает; мок `/etc/passwd` без записи для uid.
**Input**: `ReExec(ReExecConfig{…})` с `AFM_HOST_UID`/`AFM_HOST_GID` из `os.Getuid/Gid`.
**Trace**: аргументы `docker run` содержат `-e AFM_HOST_UID=<uid> -e AFM_HOST_GID=<gid>`, `AFM_IN_DOCKER=1`;
entrypoint (скрипт) дропает gosu → выставляет `HOME=/home/afm` ПОСЛЕ gosu; `~/.claude.json` НЕ в `-v`.
**Assertions**: перехваченные args НЕ содержат `:ro`-маунта `.claude.json`; `AFM_HOST_UID`/`GID`
присутствуют; секреты — bare `-e KEY` (без значения в argv).
**Sufficiency**: регресс на «токен/файлы агента ищутся в `/`» и «corrupted .claude.json» (см. CLAUDE.md).

#### `TestWeb_Embed_ServesOriginalWebPaths_AfterDirMove`
**Setup**: собранный бинарь (embed `dashboard/*`).
**Input**: `http.Get(<server>/style.css)`.
**Trace**: `web.FS` = `fs.Sub(embedded,"dashboard")` → `http.FileServer` отдаёт `style.css` по корневому
пути (без префикса `dashboard/`).
**Assertions**: 200 + корректный CSS; `index.html`, `app.js`, `favicon.svg` также доступны по корневым
путям.
**Sufficiency**: регресс на слом embed после физического переноса ассетов в `pkg/web/dashboard/`.

#### `TestDialog_AnswerArrivesBeforeQuestion_InJsonl`
**Setup**: `dialog.jsonl` содержит строку ответа ДО строки вопроса (разные горутины).
**Input**: `mcp.ReadDialog(dialogPath)`.
**Trace**: probe первой строки → answer → создаёт `Entry{ID}`; вторая строка → question → дозаполняет
поля вопроса в существующий Entry (не дропает).
**Assertions**: `Entry` содержит и вопрос, и ответ, в хронологическом порядке первого вопроса.
**Sufficiency**: регресс на потерю вопроса при гонке записи.

---

## Additional Instructions for the Implementation Agent

- **Поведение не меняется.** Это реверс-инжиниринг as-is: реализации уже существуют и работают. Любая
  правка `.go`-кода в downstream-стадии — только если контракт vs код расходятся (после Applied Fixes
  расхождений не найдено); предпочтение — правка контракта, не кода.
- **Не переносить `.go`-файлы** между директориями (Out of Scope задачи). Единственный физический перенос
  (не-`.go`-ассеты `pkg/web` → `pkg/web/dashboard`) уже выполнен стадией `goga-apply`.
- **Не создавать каркас `cells/`** — клеточки лежат поверх существующих директорий пакетов.
- **Enum-конвенция**: строковые enum'ы (`StageStatus`, `EventType`, `FSMEvent`, `Agent`) документируются
  как значения в annotations потребителя, НЕ как top-level типы и НЕ как `Imports.Types` (это причина
  3 из 4 исправленных дефектов — см. Applied Fixes).
- **Коллизия имён `Config`** (`pkg/config` и `pkg/executor`): DSL запрещает импорт двух типов с одним
  исходным именем даже через `AS`. Потребитель импортирует доминирующий (`config.Config`) формально;
  зависимость от второго (`executor.Config`) выражается prose + сохранением клеточечного ребра через
  другой тип (`Runner`). При будущих правках — не пытаться «исправить» это импортом `Config AS …`.
- **3 нюанса CLAUDE.md сохранены в annotations** (проверено): file-based dialog protocol (`pkg/mcp` +
  `pkg/orchestrator`), reverse-proxy ZAI transform (`pkg/proxy` + `cmd/afm`), Docker privilege-drop
  (`pkg/docker`). Не удалять эти Algorithm-блоки.
- **Тесты**: полный per-function охват экспортированного API отнесён на downstream `goga-plan`; здесь
  зафиксированы только сценарии 4 высокорисковых точек входа (dialog, proxy, docker, web-split).
- **`goga lint` чист** (cells: 16, errors: 0), `goga schema` строит 16 клеточек без циклов — состояние
  после Applied Fixes.
