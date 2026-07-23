# Release Notes

Newest features at the top, older ones further down. Dates follow commits to `fix`/`master`.

## 2026-07-23

### Fix: empty stage badge in dashboard event feed
- For a stage without its own `command` (uses the default client), `agent_action` events (Bash/Read/Skill/text) went out with an empty `stageId` — the dashboard didn't render the stage badge, even though status-change rows had one. Cause: `runnerFor` returned a shared runner whose `OnAction` was bound to an empty stageID.
- **Fix:** each stage now gets a per-stage runner with the correct stageID (the injected runner stays test-only). This also fixes attribution for parallel stages.

### New flow field `root_dir` — project root for agents
- `flow.yaml` can now set `root_dir` — the working directory (CWD) agents run in. Needed when the `afm` process's CWD doesn't match the project root (e.g. Docker setup: sources in `/workspace`, `.afm/` elsewhere). Without it, relative project paths (`docs/arch/…`) resolved against different roots for different stages → one stage writes a file, another can't find it.
- A relative `root_dir` resolves from the afm root (`--dir`); empty keeps prior behavior. `AFM_STAGE_DIR` (dialog files) stays tied to the afm root regardless. See README, "The flow.yaml file" section.

## 2026-07-22

### GitHub CI + automatic release on push to main
- Tag `v0.5.6` synced with upstream; further development happens in this (public GitHub) repo.
- `.github/workflows/ci.yml`: `validate` job (build+test+lint) on every push/PR; on push to `main`, the `auto-release-tag` job automatically bumps the patch version and pushes a tag using `RELEASE_TOKEN` (not the default `GITHUB_TOKEN` — otherwise the tag wouldn't trigger another workflow, a GitHub safeguard against loops).
- `.github/workflows/release.yml`: triggered by any `v*.*.*` tag (auto or manual `make release-minor/major`) — builds a multi-arch (`linux/amd64`+`linux/arm64`, via `docker/setup-qemu-action`) docker image to Docker Hub, plus `goreleaser`: cross-platform binaries, GitHub Release, Homebrew cask.
- `scripts/release.sh` simplified — no longer builds docker itself, only bumps the version and pushes the tag; all building now lives in `release.yml` (a single release entry point regardless of tag source).
- New: `brew install --cask akopichin/afm` (tap `akopichin/homebrew-afm`, `.goreleaser.yml` → `homebrew_casks:`). The post-install hook strips `com.apple.quarantine` and ad-hoc signs the binary (`codesign -f -s -`) — without both steps macOS kills the downloaded binary (`SIGKILL`); codesign alone isn't enough.

## 2026-07-21

### Hard autonomous track: `agents: [auto]`
- A stage's YAML can now statically declare the autonomous track — `agents: [auto]`. The stage runs directly via the autonomous agent, **skipping the supervisor's LLM decision and the fallback** to normal phases (no `plan.md`, no approval, dialog still available, writes `execution_summary.md`). For cases where the supervisor doesn't always classify autonomy correctly.
- Validation: `auto` must be the stage's only agent; `auto` + `supervisor: true` → parse error (conflicting intents). See `docs/superpowers/specs/2026-07-21-auto-phase-design.md`.

### Dashboard: comments on questions (like on plans)
- The dialog channel now supports clicking question lines and leaving per-line comments — same as the plan panel. Once at least one comment exists, the options and free-text answer field are hidden and a single **"Send feedback"** button appears: clicking it sends the comments (line quote + text) to the agent as the answer.
- **Ctrl/Cmd+Enter** in the dialog answer field submits the answer.
- **Dark theme by default** (previously taken from the system; the manual toggle is kept).
- **Full-screen logs** — an expand button like dialog/plan have; lines are truncated less, and not truncated at all in expanded view.
- **Project description in the header** — `/api/status` returns `description` from the flow root; useful for telling multiple running pipelines apart.
- The "↓ to latest" button is now labeled "↓ latest"; the dialog window scrolls to the end when expanded.

### Reliability: `events.jsonl` integrity
- **A truncated last record missing the trailing `\n`** (newline lost on crash) is now safely truncated instead of extending the log with a null byte and then quarantining it.
- **Corruption in the middle of the log** is now handled consistently on the `afm run` and `afm check`/run-lookup paths (single parser): the read path no longer silently falls back to a stale prefix — it signals `ErrCorruptLog`; `FindLatestRunForStage` no longer silently falls back to an older run.

### Internal: orchestrator refactor + test harness
- `pkg/orchestrator/orchestrator.go` was split from a god-file (1625→~400 lines) into focused files (concurrency/dialog_poller/agents/scheduling/control_api/supervisor_track/runner_factory) — a pure move, no behavior change. The phase domain vocabulary moved into `pkg/flow` (single source of truth). Deduplicated the autonomous-track helper (`startWithSupervisor`) and phase lists (`dialogPhases`).
- Added a scenario-driven integration test harness with a "synthetic" model (`pkg/orchestrator/scenario_*_test.go`): declarative happy-path and failure scenarios (mis-prefixed/misplaced question, stuck dialog, corrupt log, wrong supervisor track) to catch regressions across versions. Also closed coverage gaps (`afm check`/`list`, ErrRunLocked CLI, resume-from-revising, MaxParallel).

## 2026-07-20

### Fix: `startReadyStages` could launch the implementation agent for an autonomous stage
- Retrying a failed autonomous stage (`retryStage`) transitions it `Pending → Ready` via `EvReady`, then itself takes `EvStartRun`. In the narrow window between these two transitions, a concurrent call to `startReadyStages` from another event-loop branch (e.g. `onAgentCompleted` for a sibling stage) could win the `EvStartRun` CAS first — and `startReadyStages` blindly launched `runImplementationAgent` for any Ready stage, without checking `autonomous.flag`. `runImplementationAgent` reads `plan.md`, which an autonomous stage doesn't have → the stage failed with `open .../plan.md: no such file or directory` instead of re-running autonomously.
- **Fix:** `startReadyStages` now checks `isAutonomousStage` before spawning and launches `runAutonomousAgent` for such stages — symmetric with the existing checks in `recovery.go` and in `retryStage`.
- The regression reproduced reliably (5/5) in `TestIntegration_RetryFailedAutonomousStaysAutonomous`; after the fix the test is green (5/5 in a row, `-race`).

### goga-accept: CODEMANIFEST sync (second pass)
- `assets.ReadPrompt`: the manifest didn't reflect the third return value `fromOverride bool` (prompt source, for logging).
- `pkg/docker.ScanCommands`: the manifest didn't reflect the third parameter `generated map[string]bool` (commands covered by the autoShim wrapper).
- `pkg/orchestrator.Orchestrator.Retry`: the annotation didn't describe the retry branch for autonomous stages or the session/jsonl cleanup for interactive ones.
- `cmd/afm`: the manifest documented a nonexistent local helper `findLatestRunDir(stageID)` — approve/retry/revise actually call `state.FindLatestRunForStage` (added to `pkg/state/CODEMANIFEST`).
- `pkg/web/dashboard/src/app`: `App()` didn't document the `status === 'failed'` exception for hiding `showPlan` on an autonomous stage — the Retry button in PlanPanel must stay available for a failed autonomous stage too.

### UI: theme toggle moved from footer to header
- The dark/light button (`useThemeMode`, ☾/☀ icon) moved from `Footer.tsx` to `FlowHeader.tsx` — now in the header's top-right corner, next to the WS connection indicator, instead of the footer at the bottom. Behavior and appearance unchanged (same `.icon-btn`, same `aria-label`, same `localStorage` persistence), only the render location changed.
- `skins/base/header.css` (source `public/skins/base/header.css`, copied to root by `vite build`): added a column to `#header { grid-template-columns }` and spacing `#header .icon-btn { margin-left: 4px }` for the new button.
- `Footer.test.tsx`/`FlowHeader.test.tsx`: the theme-toggle test moved along with the button.
- CODEMANIFEST `footer`/`flow-header` synced with the code (`goga lint`/`goga contract` confirm no drift).

### Data race on package-level globals (websocket keepalive + retry)
- **`pkg/server` (websocket):** `wsPongWait`/`wsPingPeriod`/`wsWriteWait` were package-level `var`s that tests mutated between connections. Server goroutines (`PongHandler` in `readPump`, the ticker in `writePump`) read them on every iteration → data race against test writes (goroutines from a previous connection were still alive). **Fix:** snapshot the values into locals at the start of `readPump`/`writePump` (keepalive is fixed for the connection's lifetime — no need to re-read globals). `TestWebSocket_ClosesSilentClient` is now green under `-race`.
- **`pkg/orchestrator` (retry):** `RetryBackoff`/`MaxRetries` had the same problem. Agent goroutines (`runWithRetry`) outlive `Run()`'s return (it doesn't wait for them), and tests restored the globals in `t.Cleanup` → a race in `TestIntegration_RetryExhausted` under `-race`. **Fix:** `RetryBackoff`/`MaxRetries` are snapshotted per-instance in `New()`/`NewSupervisor()` (`Orchestrator.maxRetries`/`retryBackoff`, `Supervisor.maxRetries`/`retryBackoff`); `runWithRetry` and `EvaluateStage` read the instance's immutable fields instead of the globals. The package-level `RetryBackoff`/`MaxRetries` remain as defaults, including for tests.

### Lint: goconst + identical-switch-branches
- `supervisor.go`: the literal `"autonomous_execution"` replaced with the constant `phaseAutonomous` (goconst).
- `orchestrator.go`: identical `case phaseImplementation` and `case phaseAutonomous` branches (both did running→done) merged (revive `identical-switch-branches`).
- `golangci-lint run ./...` = 0 issues; `setstatuslinter` clean.

### Test: stale `TestIntegration_DialogViolationDetected` → relocate
- The test checked **old** behavior (fail-fast via `detectDialogViolation` when `question.json` was written outside stageDir), replaced by auto-relocate in commit `2a759dd`. Because of that, the mock (emitting a Write event with no real file) made the stage hang → 15s timeout. **Fix:** rewritten as `TestIntegration_MisplacedQuestionRelocated` — the mock creates the file in the wrong directory, and the test checks that the poller relocates it into stageDir and the stage moves to `awaiting_user_input`. `CLAUDE.md` docs updated to match the current relocate behavior.

### goga-accept: CODEMANIFEST sync
- `pkg/state/CODEMANIFEST`: `SetApplyHook(h TransitionCallback)` → `SetApplyHook(h)` — the `TransitionCallback` type doesn't exist in the code (a dangling reference; the real signature is `func SetApplyHook(h func(Transition))`); aligned with the sibling convention `SetExecFunc(f)`.

## 2026-07-16

### Supervisor agent — auto-assessing stage autonomy
- A stage with `supervisor: true` is assessed at start by an LLM supervisor: can it run **autonomously** (a single step through the attached skill — writes `execution_summary.md`, skipping planning/approval/review) or does it need the standard multi-phase cycle. The decision is written to `<runDir>/supervisor.jsonl` (`events.jsonl` is untouched).
- **Fallback safety:** any LLM-call error (timeout, bad JSON) → safe fallback to base phases. 529/502/503/504 are survived via retry (see below). The supervisor can **only shorten** phases, never extend them. Inline artifacts → always the standard track.
- **Config:** `flow.supervisor_command` (e.g. `glm47`) > `config.supervisor.command` > `config.client.command`; `stage.supervisor: true` + optional `stage.supervisor_prompt`. Prompt template: `assets/prompts/autonomous.md`.
- **FSM:** new transition `EvSupervisorApproved` (planning→ready), bus event `EventSupervisorDecision`. Resuming autonomous stages (`autonomous.flag` + `execution_summary.md`) handled in `recovery.go`. `CollectDependencyPlans` reads `execution_summary.md` instead of `plan.md` for autonomous dependencies.
- **Robust parsing:** `Supervisor.parseDecision` extracts the decision JSON from: raw JSON / claude envelope `{"type":"result","result":"…"}` / claude event array `[…]` / ```json fences. Covers both `claude --output-format json` shapes (container — single envelope, host — array).
- Spec/plan: `docs/superpowers/specs/2026-07-16-supervisor-agent-design.md`, `docs/superpowers/plans/2026-07-16-supervisor-agent.md`.

### Fix: supervisor LLM call + claude wrappers for `--output-format json`
- **RunJSONQuery** (`pkg/executor`) inherited `e.cfg.ExtraArgs`, which `executor.New` defaults to `DefaultClaudeArgs` (`--print --output-format stream-json --verbose --dangerously-skip-permissions`). That `stream-json` conflicted with the supervisor's `--output-format json` → claude exited 1. **Fix:** a clean invocation `-p <prompt> --output-format json` without ExtraArgs, plus stderr captured into the error (diagnostics).
- **docker wrapper** (`pkg/docker/wrapper.go`): `--include-partial-messages` is now added **only when `output-format=stream-json`** (previously always — which gave `requires --output-format=stream-json` for the json call). The same fix applied to host wrappers `glm47/51/52` (ai-free): partial only with stream-json, not with `-p`.
- Live validation: `afm run` in docker mode with `feature.yaml` (`supervisor: true`, `supervisor_command: glm47`) — `supervisor.jsonl` now contains a real decision (`decision=standard`, reasoning); before the fix it silently fell back on every stage.

### Supervisor: UI visibility + retries + autonomous dialog
- **Supervisor now retries transient errors** (529/502/503/504) with the same `RetryBackoff`×`MaxRetries` as stage agents (`EvaluateStage`), instead of falling back immediately. On non-retryable errors it still fails straight to fallback.
- **`validateDecision` is strict only for autonomous** (`== ["autonomous_execution"]`); for the standard phase it's advisory (`DetermineStagePhases` returns base phases anyway). Removed a false fallback that triggered when the LLM wrote base phases as a single string `"planning implementation"` (an artifact of Go-slice rendering) — a valid decision no longer gets hidden from the log/UI. `BasePhases` now renders as JSON `["planning","implementation"]`.
- **Supervisor decision is now published to the UI for both tracks** (`EventSupervisorDecision` + `supervisor.jsonl`); previously the standard track wasn't published — the UI never saw the resolution. In the dashboard, `supervisor_decision` gets its own highlight (`.feed-entry.supervisor`) in the Event feed.
- **Autonomous-agent logs now visible in the middle "Log" panel**: `handleLog` (`/api/stages/<id>/log`) and `buildDialogEntries` now read `autonomous.log`/`autonomous.jsonl` (previously only planning/implementation/review → the panel was empty for autonomous stages).
- **The autonomous phase supports dialog**: `phaseAutonomous = "autonomous_execution"` — a single phase with no planning/impl/review (the skill does everything itself, writes `execution_summary.md`), BUT the skill can still ask the user via the same file-based dialog protocol (questions `autonomous_execution.q<N>.*`, a valid phase, resume via `onUserAnswered`). The runner gets `AFM_STAGE_DIR`, the prompt includes `<interactive_rules>`.
- **Persistent supervisor-decision badge** in the stage header in the dashboard (the decision stays visible even after the event scrolls out of the feed).
- **Fix for host-mode `supervisor_command`**: the wrapper is now generated even if no stage uses the command as an agent; the secret is resolved in the host branch too (`UsedRecipes`).

## 2026-07-15

### Retry on 529/502/503/504 + removal of proxy and accounting
- `orchestrator.Classify` now classifies `API Error: 529/502/503/504` (raw text from glm wrappers) as `ClassRetryable` (previously `ClassFatal` → the stage failed). 500 stays fatal.
- The built-in reverse proxy (`pkg/proxy`) is fully removed: the ZAI transform is redundant after adding retry, and routing isn't needed (autoShim wrappers bake in the direct upstream URL). Removed the associated threading infra in `run.go`/`orchestrator`/`executor`/`docker`.
- Token accounting (`pkg/accounting`) is fully removed: it lost its data source without the proxy. Removed `/api/usage`, the dashboard `ConsumptionPanel`, and config `proxy`/`pricing`/`accounting`.
- **Backward compat:** `yaml.Unmarshal` is lenient → old configs with `proxy`/`pricing`/`accounting` still parse (the sections are silently ignored). `autoShim:false` is neutral (glm wrappers already went direct). Usage accounting is deferred.

### claude wrappers: bounded retry + stream-json + `--bare` (config `claude_bare`)
- **Bounded retry loop** (`pkg/orchestrator/retry.go`): fixed `RetryBackoff=5s` × `MaxRetries=15` (as in ralphex), replacing the previous exponential `[5s,10s,30s]` (4 attempts). z.ai 529 is transient; this survives the overload window. Confirmed: claude sends `stream:true` itself (via `--output-format stream-json`), so force-streaming isn't needed.
- **`--output-format stream-json` + `--include-partial-messages`** added to generated claude wrappers (`pkg/docker/wrapper.go`): covers non-interactive stages (which the executor doesn't pass ExtraArgs to) and gives partial deltas. `--output-format` is deduplicated (interactive stages already get it via the executor).
- **`--bare` + config `client.claude_bare`**: `--bare` = claude Code minimal mode (skips CLAUDE.md/hooks/skills/memory), body ~4 KB instead of ~127 KB (lower load on z.ai). **BUT `--bare` breaks the Skill tool** — goga-* skills stop resolving (the agent has to imitate them itself). So **default is `claude_bare: false`** (skills matter more). `claude_bare: true` is for flows WITHOUT skills.

### `type: cursor` — Cursor Cloud Agents API
- The Cursor Cloud API (`api.cursor.com`) **has no** synchronous OpenAI `/v1/chat/completions` (returns 404) — it's a **Cloud Agents API**: an async, run-based API where "chat" means starting a cloud code agent. So `type: openai` (which hits `${BASE_URL}/chat/completions`) **never worked and cannot work** with Cursor. The historical note about "Cursor via `api2.cursor.sh`" under `type: openai` was wrong — removed.
- New recipe type `type: cursor` → a wrapper with `CURSOR_*` env (`CURSOR_API_KEY`/`CURSOR_BASE_URL`/`CURSOR_MODEL`) and `exec /usr/local/bin/cursor-as-claude`. The adapter: reads the prompt from stdin → `POST /v1/agents` (no-repo, `mode:"agent"`) → polls `GET /agents/{id}/runs/{runId}` until a terminal status (`FINISHED`/`ERROR`/`CANCELLED`/`EXPIRED`) → emits claude stream-json (an `assistant` envelope with the `result` text + a `result` event) → archives the agent (best-effort, to avoid leaving clutter). `model: auto` (or empty) → the `model` field is omitted, Cursor uses its default.
- `auth.to` for cursor is any `env:VAR` (conventionally `CURSOR_API_KEY`); `url` is required (`https://api.cursor.com/v1`). Doesn't require `claude` in PATH (unlike openai). Requires `jq`+`curl` in the image. Tests: `TestAgentRecipe_CursorType`, `TestCreateWrappers_CursorTemplate`/`_CursorNoClaudeRequired`.
- **Note:** the first response takes ~30–90s (cloud-VM startup when the agent is created); the run itself is fast afterward (`durationMs` in seconds). Tolerable for interactive dialog, but not instant.

## 2026-07-14

### Docker `autoShim` — генерируемые врапперы без монтирования
- По флагу `docker.autoShim: true` afm **генерирует claude-совместимые врапперы** для recipe-агентов (`docker.agents.<cmd>`) прямо в контейнере — без `-v` монтирования хост-бинарника и без `extra_mounts` для токенов. Реальные обёртки (`glm47`/`glm51`/`glm52`/`deepseek-v4`) — это «model+url+auth+sysprompt → `exec claude`», поэтому описываются recipe и регенерируются.
- **Recipe:** `model` (обязателен → `ANTHROPIC_DEFAULT_{HAIKU,SONNET,OPUS}_MODEL`, один на все 3 тира), `url` (gateway), `system_prompt` (`file:<path>` → `--append-system-prompt-file`), `auth.from` (`env:VAR` | `file:<path>` — где afm читает секрет на хосте) + `auth.to` (`env:<VAR>` ∈ {`CLAUDE_CODE_OAUTH_TOKEN`,`ANTHROPIC_API_KEY`,`ANTHROPIC_AUTH_TOKEN`}).
- **Data flow:** хост читает секрет и контент sysprompt из host-only файлов → transient env `AFM_SECRET_<CMD>`/`AFM_SYSPROMPT_<CMD>` (bare-form `-e`, значение не попадает в argv `docker run`); `url`/`model`/`auth.to` контейнер берёт из смонтированного `config.yaml`. Враппер bake'ит `ANTHROPIC_BASE_URL` (по host-match с `proxy.upstream` — z.ai через прокси ради 529-защиты, deepseek напрямую), подставляет секрет из transient env, `unset`'ит его и `exec`'ит абсолютный `claude`.
- **Единый wrapper-dir** (`docker.CreateWrappers`) = claude proxy-shim + generated-врапперы; `proxy.CreateShim` удалён. `orchestrator.proxyForCmd` стал generated-aware (`generated` → self-route через baked `BASE_URL`, wrapper-dir на PATH). `docker.ScanCommands` пропускает generated (не монтируются); `docker.UsedRecipes` — секреты резолвятся только для recipe, реально используемых в flow (нет false fail-fast / утечки секретов неиспользуемых агентов). Нет секрета → fail-fast с именем агента. `afm-init` добавляет `.afm/secrets.env` в `.gitignore`.
- **Bonus:** recipe может описать docker-only агента, бинарника которого нет на хосте (напр. `deepseek-v4`) — `autoShim` сгенерирует его в контейнере.
- Спек: `docs/superpowers/specs/2026-07-14-docker-autoshim-design.md`.

### `type: openai` — OpenAI-совместимые провайдеры
- Recipe с `type: openai` → враппер с `OPENAI_*` env (`OPENAI_API_KEY`/`OPENAI_BASE_URL`/`OPENAI_MODEL`) и `exec /usr/local/bin/openai-as-claude` — bash-транслятор: читает промпт из stdin, вызывает `${OPENAI_BASE_URL}/chat/completions` (stream=true), транслирует SSE в claude stream-json. Поддержка Cursor (`api2.cursor.sh`), DeepSeek, локальных LLM и любых OpenAI-совместимых эндпоинтов.
- `auth.to` для openai — любой `env:VAR` (НЕ ограничен ClaudeAuthEnvVars); `url` обязателен. Требует `jq`+`curl` в образе (добавлены в `Dockerfile.runtime`).
- Backward compat: пустой `type` (или `"claude"`) = прежнее claude-поведение; неизвестное значение `type` → ошибка валидации.

### Fix: generated-враппер не находился (executor LookPath)
- `exec.Command` резолвил bare-команду (`glm47`) через `LookPath` по PATH родительского процесса (afm), а wrapper-dir (`ProxyShimDir`) добавлялся только в env ребёнка → `start glm47: executable file not found`. Executor теперь резолвит команду в `ProxyShimDir/<cmd>` (абсолютный путь); для mounted-бинарей fallback на bare name. Регрессионный тест `TestRunAgentResolvesWrapperCommand`. Без этого фикса autoShim не работал end-to-end.

## 2026-07-13

### Дашборд на React
- Веб-дашборд переписан с vanilla JS (`app.js` + `markdown-it.min.js`) на **React 18 + Vite + TypeScript** (`pkg/web/dashboard/src`); markdown-it упакован внутрь бандла, отдельного файла больше нет.
- **`go:embed` ограничен** только раздаваемой статикой (`index.html`, `assets/`, стили, иконки). Раньше `dashboard/*` утягивал в бинарь `node_modules` (~96 МБ) — бинарник весил 163 МБ; теперь **14 МБ**.
- **Сборка фронтенда в `make`**: цель `web` (`npm run build`) стала пререквизитом `build`/`install`/`docker-build` — веб всегда пересобирается и вкомпилируется в бинарь.
- **`Dockerfile.runtime` multi-stage**: node-стадия собирает React, go-стадия встраивает его через embed. `make release-*` теперь тоже собирает веб (релизный образ всегда с актуальным дашбордом из исходников).
- `.dockerignore`: `**/node_modules/` исключён из docker-контекста.

### WebSocket keepalive
- Сервер пингует соединения (gorilla `PingMessage` + `SetReadDeadline` 60 c + `PongHandler`) и рвёт «мёртвые» клиента; app-level `{"type":"heartbeat"}` каждые 30 c (`pkg/server/websocket.go`, single-writer через `select`).
- Клиент (`use-event-feed`): автореконнект с backoff (был) + **watchdog** (тишина >75 c → принудительный реконнект); heartbeat обновляет liveness, но в ленту событий не попадает.

### Resizable-лейаут и maximize
- Панели на **`react-resizable-panels`**: 3 колонки (`stages | central | feed`) и вертикальные сплиты `plan/dialog/log` внутри central; размеры сохраняются в `localStorage`. Дефолт 15/60/25 (колонки), 30/45/25 (строки).
- **Maximize** (иконка ⛶) панелей plan/dialog/feed на весь экран через React-портал; внутреннее состояние (скролл, ввод) сохраняется, `Esc`/✕ — свернуть.

### Сигнал «ждёт пользователя»
- Для статусов `awaiting_user_input`/`awaiting_approval`: пульс элемента стадии в сайдбаре + точка в шапке + свечение панели + мигание `document.title` в фоновой вкладке + автоскролл центральной колонки к ожидающей панели.

### Auto-scroll диалога и фида
- Диалог и лента событий прижаты к низу при появлении контента, пока пользователь сам не уехал вверх (кнопка «↓ к последнему»); при наличии ждущего ответа вопроса диалог проматывается к нему (и при загрузке, и при новом вопросе).

### Диалог: только Q/A, без «мыслей» агента
- В секцию диалога больше не попадают `text`-блоки агента из stream-json лога (для GLM это рассуждения вслух, дублировавшие панель log) — только вопросы/ответы. Контекст рассуждений остаётся в `LogPanel`. Кнопки вариантов ответа подсвечивают выбор (`selected`).

### Тема goga после React-миграции
- `style-goga.css` пересобран как `@import "style.css"` + goga design-tokens (прежде отдельный 1100-строчный файл под vanilla-DOM — сломался после миграции на React). Теперь обе темы разделяют структуру из `style.css`, goga отличается палитрой + оверрайдами; темы больше не расходятся.
- goga-оверрайды: лого «goga» (teal), чистый фон без novacorps-клетки и `.ray`, панели на `--bg-elev`.
- `pkg/server/server.go`: подмена CSS под `href="./style.css"` (Vite `base: './'`) — фикс переключения стиля для goga.

### Тесты сервера под React
- `TestServerServesMarkdownIt` → `TestServerServesReactBundle` (markdown-it в бандле); `TestServer_IndexDefaultTheme`/`_IndexGogaTheme` актуализированы под собранный React `index.html` (`./style.css`, `#root`, theme-class).

## 2026-07-09

### Тема дашборда `goga`
- Вторая тема веб-дашборда, включаемая флажком `theme: goga` в `~/.afm/config.yaml` (top-level). Визуально по мотивам qarium.ru/goga: тёмно-синий фон `#0A0E1A`, teal-акцент `#20D4BF`, sans-serif шрифт, скруглённые углы, без неон-декора. Дефолтная тема — `novacorps` (прежняя hi-tech мятная). Неизвестное значение → warning + `novacorps`.
- Самодостаточный `pkg/web/dashboard/style-goga.css` (стиль с нуля; `style.css`/`index.html` для дефолта не тронуты). Доставка темы — server-side replace `style.css`→`style-goga.css` и класс `<body>` при отдаче `/` (без FOUC, без `/api/config`).
- Лого quarium + заголовок «Goga» в goga-теме (CSS: скрыт Nova-гексагон, `background quarium-logo.png`, `h1`→«Goga» teal через `::before`).
- Палитра графика потребления (`app.js USAGE_COLORS`) читается из CSS-токенов с fallback на mint — график teal в goga, не меняется в novacorps.
- Интерфейс переведён на английский для обеих тем (`index.html`, `app.js`, CSS `content`).

### `open_browser` по умолчанию `false`
- `server.open_browser` (в `~/.afm/config.yaml`) теперь по умолчанию `false`: браузер НЕ открывается автоматически при старте дашборда — в лог печатается URL с подсказкой `→ open this URL in your browser to follow the run`. `server.open_browser: true` возвращает прежнее авто-открытие. Работает для локального запуска и Docker (хост-side opener).
- Примечание: «косяки с подписанием бинарника» на macOS 26 (SIGKILL неподписанного бинаря) НЕ связаны с открытием браузера — лечатся `make install` (ad-hoc codesign), а не этим флажком.

## 2026-07-08

### Глобальный `prompt` (root-level)
- **Корневое поле `prompt:`** в `flow.yaml` — общая инструкция, попадающая в системный промпт **каждой стадии и каждой фазы** (planning/implementation/review): рендерится как блок `<global_prompt>…</global_prompt>` сразу после `</system_rules>`.
- Не путать с `stage.prompt` (2026-07-02) — тот адресует конкретную стадию после `</stage>`; корневой общий для всего прогона.
- Необязательное: пустое/отсутствующее → блок не пишется, вывод байт-идентичен прежнему (обратная совместимость). Содержимое экранируется (`escapeTags`) — нельзя внедрить XML-теги.
- Проброс: `flow.Flow.Prompt` → `orchestrator.Options.GlobalPrompt` → `prompts.Inputs.GlobalPrompt` → `Build` (5 точек вызова в orchestrator).

### Reverse-proxy: тихий usage для non-200
- `captureUsage` больше не логирует warning для ответов без usage-поля — non-200 (ошибки, 429/529 rate-limit) пропускаются молча (`pkg/proxy/proxy.go`). Раньше каждый неуспешный ответ прокси засорял лог предупреждением о невалидном usage.

### Версионирование Docker-имиджа (SemVer + авто-бамп)
- `make release-{patch,minor,major}` — версионированный релиз: пушит иммутабельный `akopichin/afm:vX.Y.Z` и rolling `:latest`. Тег авто-бампится от последнего git-тега (`scripts/release.sh`); git-тег создаётся локально после успешного пуша.
- Версия вшита в бинарник: `afm --version` (в т.ч. `docker run … afm --version`).
- `make docker-push` остался dev-only `:latest`. Секцию `dockers` из `.goreleaser.yml` убрали (docker теперь дело Makefile).

## 2026-07-07

### Учёт потребления по стейджам (Consumption / Accounting)
- afm считает потребление агентов (токены / стоимость / КБ) и атрибутирует его по стадиям прогона. Новый пакет `pkg/accounting`: окна выполнения стадий (`StageWindow`/`LoadStageWindows`), чтение `usage.jsonl` и терминальных result-событий, агрегация по метрикам и временным бакетам, дерайв стоимости из токенов (`DeriveCost`), фасад запроса `Accountant.Query`.
- Источник данных — reverse-proxy: равномерно захватывает usage проксированных ответов (`UsageRecord`/`ParseUsage` → `usage.jsonl`), `proxy.New` принимает `usageLogPath`. Правило «без двойного счёта»: стадия с proxy-записью не получает result-usage-фолбэк.
- **Config**: `pricing.models.<model>` (`input_per_mtok`/`output_per_mtok`/`cache_per_mtok`, USD за миллион токенов; nil/empty → стоимость скрыта, точное совпадение по имени модели без fuzzy); `accounting.bucket_minutes` (ширина бакета агрегации, по умолчанию 5).
- **HTTP**: `GET /api/usage?metric=tokens|cost|kb&stage=<id>` (`UsageHandler` от `Config.Accountant`).
- **Dashboard**: панель потребления (Consumption) в `pkg/web/dashboard`.

## 2026-07-05

### fix(dialog): интерактивная стадия при ожидании ответа
- Интерактивная стадия больше не падает, пока ждёт ответа пользователя: агент может завершиться до ответа, но стадия остаётся в `awaiting_user_input` (а не `failed`) — `NotifyAnswer` перезапускает агента после ответа.

## 2026-07-02

### Поле стадии `prompt`
- **`stage.prompt`** — необязательное поле: явная инструкция агенту, попадает в отдельный блок `<prompt>…</prompt>` сразу после контекста стадии (`</stage>`).
- В отличие от `description` (фон/контекст задачи), это прямое указание что делать. Содержимое экранируется (`escapeTags`) — нельзя внедрить XML-теги (`</stage>`, `</prompt>`, `<plan>`).
- Builder читает `Stage.Prompt` напрямую (как `description`/`skills`) — без отдельного поля `prompts.Inputs.Prompt` и проброса через вызовы `Build()` в orchestrator.

### Имя стадии (`name`) в дашборде
- **`RunState.stage_names`** (id→name, `omitempty`) пробрасывается через существующий `/api/status`; заполняется из файла flow в `run.go` (работает и для новых, и для возобновляемых прогонов). `SetStageNames`/`Snapshot()` копируют карту (`maps.Clone`) — поздние мутации вызывающего кода не портят состояние стора.
- **UI**: левая панель показывает `id` (крупно, uppercase) + `name` под ним мелко; заголовок центральной панели — `name`, иначе `id`. Стадии без `name` выглядят как раньше.
- **README**: поле `name` исправлено на необязательное (валидация его не требует); добавлены описания `prompt` и отображения `name` в дашборде.

## 2026-07-01

### Embedded skills в бинаре
- **`afm install-skills`** — Claude-скилы (`/afm`, `/afm-check`, `/afm-init`, `/afm-retry`, `/afm-review`) встроены в бинарник через `assets.SkillsFS`.
- Установка одной командой: `afm install-skills [--skills-dir <path>] [--force]`. Идемпотентно — без `--force` существующие файлы пропускаются, с `--force` перезаписываются.
- `install.sh` делегирует установку скилов бинарнику с интерактивным запросом `[Y/n]` (дефолт — установить).
- `install.sh` UX: явная ошибка + подсказка `make build`, если `bin/afm` не собран; блок «Готово!» — только при установке skills.

### Docker-mode: стабилизация интерактивных flow
- Запуск под **host-uid** (gosu entrypoint): нет root-записей, файлы в томах принадлежат пользователю хоста; claude разрешает `--dangerously-skip-permissions`.
- `isatty`-проверка (`golang.org/x/term`) — корректный `-it` только в настоящем TTY.
- Проброс порта dashboard; браузер открывается на хосте (host-side opener); `IS_SANDBOX=1`; `extra_mounts` для токенов кастомных агентов; HOME выставляется после gosu.
- Безопасность: секреты не в argv (`-e KEY` без значения); абсолютный `--dir` в контейнере; `.dockerignore`.

## 2026-06-30

### Docker mode
- **afm автоматически перезапускает себя внутри Docker** (`docker.enabled` в конфиге или `AFM_USE_DOCKER=1`).
- `Dockerfile.runtime` (ubuntu 24.04 + node 22 + python 3.12 + go 1.26 + gosu); `make docker-build/push/run`; goreleaser docker.
- Автомонтирование: проект + `.afm/`, `~/.claude/`, `~/.afm/`, нестандартные агенты (`command: glm51` → монтируется бинарь `:ro`); `extra_mounts` для конфигов/токенов.
- Dashboard доступен с хоста (проброс порта `-p`).

### `--dir` и переименование в afm
- Флаг **`--dir`** (`AFM_DIR`) — кастомная директория для `.afm` (прогоны, flows, config); приоритет флаг > env > текущая директория.
- Переименование **flowmanager → afm**: бинарник, команда, env `AFM_*`, навыки `/afm*`. (Каталог репо и git-имя не менялись; module-path тот же.)

## 2026-06-29

### Встроенный reverse-proxy
- **Встроенный прокси** перехватывает HTTP-трафик агентов к Anthropic-совместимым шлюзам и применяет трансформации.
- **`ZAITransform`** — обход `api.z.ai` 529: переписывает non-streaming запрос в streaming, собирает SSE и пересобирает в единый Anthropic JSON-ответ.
- **`CreateShim`** — поддержка wrapper-команд (`glm51` и др.): shim оборачивает `claude`, прокси-адрес доходит до реального клиента даже если wrapper перезаписывает `ANTHROPIC_BASE_URL`.
- `ProxyConfig` в конфиге: `proxy.enabled/upstream/port/transforms.zai` (nil/absent → включён, авто-детект `api.z.ai` по хосту).
