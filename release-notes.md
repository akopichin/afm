# Release Notes

Новые возможности — сверху, дальше вниз по устареванию. Даты — по коммитам в `fix`/`master`.

## 2026-07-22

### GitHub CI + автоматический релиз при push в main
- Тег `v0.5.6` синхронизирован с upstream; дальнейшая разработка ведётся в этом (публичном GitHub) репозитории.
- `.github/workflows/ci.yml`: job `validate` (build+test+lint) на любой push/PR; на push в `main` — job `auto-release-tag` автоматически бампает patch-версию и пушит тег через `RELEASE_TOKEN` (не дефолтный `GITHUB_TOKEN` — иначе тег не триггерит другой workflow, защита GitHub от циклов).
- `.github/workflows/release.yml`: реагирует на любой тег `v*.*.*` (авто или ручной `make release-minor/major`) — мультиарх (`linux/amd64`+`linux/arm64`, с `docker/setup-qemu-action`) docker-образ в Docker Hub, плюс `goreleaser`: кросс-платформенные бинарники, GitHub Release, Homebrew cask.
- `scripts/release.sh` упрощён — больше не собирает docker сам, только бампает версию и пушит тег; вся сборка теперь в `release.yml` (единая точка входа в релиз, независимо от источника тега).
- Новое: `brew install --cask akopichin/afm` (тап `akopichin/homebrew-afm`, `.goreleaser.yml` → `homebrew_casks:`). Post-install хук снимает `com.apple.quarantine` и ad-hoc подписывает бинарник (`codesign -f -s -`) — без обоих шагов macOS убивает скачанный бинарник (`SIGKILL`), одного codesign недостаточно.

## 2026-07-21

### Жёсткий автономный трек: `agents: [auto]`
- В YAML стадии можно статически задать автономный трек — `agents: [auto]`. Стадия сразу исполняется автономным агентом **без LLM-решения супервизора и без фолбэка** на обычные фазы (нет `plan.md`, нет одобрения, доступен диалог, пишет `execution_summary.md`). Для случаев, когда супервизор не всегда правильно определяет автономность.
- Валидация: `auto` — единственный агент стадии; `auto` + `supervisor: true` → ошибка парсинга (противоречивые интенты). См. `docs/superpowers/specs/2026-07-21-auto-phase-design.md`.

### Дашборд: комментарии к вопросам (как к планам)
- В канале диалога теперь можно кликать по строкам вопроса и оставлять построчные комментарии — как в панели плана. Как только есть хотя бы один комментарий, опции и поле свободного ответа скрываются, появляется одна кнопка **«Send feedback»**: по нажатию комментарии (цитата строки + текст) отправляются агенту как ответ.
- **Ctrl/Cmd+Enter** в поле ответа диалога — отправить ответ.
- **Тёмная тема по умолчанию** (раньше бралась из системы; ручной переключатель сохранён).
- **Логи на весь экран** — кнопка разворота как у диалога/плана; строки меньше обрезаются, в развёрнутом виде не обрезаются вовсе.
- **Описание проекта в шапке** — `/api/status` отдаёт `description` из корня флоу; удобно различать несколько запущенных пайплайнов.
- Кнопка «↓ к последнему» переведена на «↓ latest»; окно диалога при раскрытии проматывается в конец.

### Надёжность: целостность `events.jsonl`
- **Оборванная последняя запись без завершающего `\n`** (потерян при crash перевод строки) теперь безопасно усекается, а не расширяет лог нулевым байтом с последующим уходом в карантин.
- **Порча в середине лога** трактуется единообразно на путях `afm run` и `afm check`/поиска run (единый парсер): read-путь больше не отдаёт молча устаревший префикс, а сигналит `ErrCorruptLog`; `FindLatestRunForStage` не уходит тихо в более старый run.

### Внутреннее: рефакторинг оркестратора + тест-харнесс
- `pkg/orchestrator/orchestrator.go` разнесён из god-файла (1625→~400 строк) по сфокусированным файлам (concurrency/dialog_poller/agents/scheduling/control_api/supervisor_track/runner_factory) — чистое перемещение, поведение не изменилось. Доменный словарь фаз вынесен в `pkg/flow` (единый источник). Дедуп autonomous-трека (`startWithSupervisor`) и списков фаз (`dialogPhases`).
- Добавлен scenario-driven интеграционный тест-харнесс со «синтетической» моделью (`pkg/orchestrator/scenario_*_test.go`): декларативные сценарии happy-flow и ошибочные (mis-prefixed/misplaced вопрос, зависший диалог, битый лог, неверный трек супервизора) — для ловли регрессий между версиями. Плюс закрыты пробелы покрытия (`afm check`/`list`, ErrRunLocked-CLI, resume-from-revising, MaxParallel).

## 2026-07-20

### Fix: `startReadyStages` мог запустить implementation-агента для autonomous-стадии
- Retry упавшей autonomous-стадии (`retryStage`) переводит её `Pending → Ready` через `EvReady`, а следом сам берёт `EvStartRun`. В узком окне между этими двумя переходами конкурентный вызов `startReadyStages` из другой ветки event-loop (например, `onAgentCompleted` соседней стадии) мог выиграть CAS на `EvStartRun` первым — а `startReadyStages` слепо запускал `runImplementationAgent` для любой Ready-стадии, не проверяя `autonomous.flag`. `runImplementationAgent` читает `plan.md`, которого у autonomous-стадии нет → стадия падала с `open .../plan.md: no such file or directory` вместо повторного автономного прогона.
- **Фикс:** `startReadyStages` перед спавном проверяет `isAutonomousStage` и для таких стадий запускает `runAutonomousAgent` — симметрично уже существующим проверкам в `recovery.go` и в `retryStage`.
- Регрессия воспроизводилась стабильно (5/5) в `TestIntegration_RetryFailedAutonomousStaysAutonomous`; после фикса тест зелёный (5/5 подряд, `-race`).

### goga-accept: синхронизация CODEMANIFEST (второй проход)
- `assets.ReadPrompt`: манифест не отражал третий возврат `fromOverride bool` (источник промпта для логирования).
- `pkg/docker.ScanCommands`: манифест не отражал третий параметр `generated map[string]bool` (команды, покрытые autoShim-враппером).
- `pkg/orchestrator.Orchestrator.Retry`: аннотация не описывала ветку retry для autonomous-стадий и очистку session/jsonl для интерактивных.
- `cmd/afm`: манифест документировал несуществующий локальный хелпер `findLatestRunDir(stageID)` — approve/retry/revise реально вызывают `state.FindLatestRunForStage` (добавлена в `pkg/state/CODEMANIFEST`).
- `pkg/web/dashboard/src/app`: `App()` не документировал исключение `status === 'failed'` для скрытия `showPlan` у autonomous-стадии — кнопка Retry в PlanPanel должна быть доступна и для упавшей autonomous-стадии.

### UI: переключатель темы перенесён из футера в шапку
- Кнопка dark/light (`useThemeMode`, иконка ☾/☀) переехала из `Footer.tsx` в `FlowHeader.tsx` — теперь она в правом верхнем углу шапки, рядом с индикатором WS-соединения, а не в футере снизу. Поведение и внешний вид не изменились (тот же `.icon-btn`, тот же `aria-label`, то же сохранение в `localStorage`), поменялось только место рендера.
- `skins/base/header.css` (источник — `public/skins/base/header.css`, копируется в корень `vite build`'ом): добавлена колонка в `#header { grid-template-columns }` и отступ `#header .icon-btn { margin-left: 4px }` под новую кнопку.
- `Footer.test.tsx`/`FlowHeader.test.tsx`: тест переключения темы перенесён вместе с кнопкой.
- CODEMANIFEST `footer`/`flow-header` синхронизированы с кодом (`goga lint`/`goga contract` подтверждают отсутствие дрейфа).

### Data race на package-level globals (websocket keepalive + retry)
- **`pkg/server` (websocket):** `wsPongWait`/`wsPingPeriod`/`wsWriteWait` — package-level `var`, которые тесты мутировали между соединениями. Серверные горутины (`PongHandler` в `readPump`, тикер в `writePump`) читали их на каждой итерации → data race с записью в тестах (даже горутины от прошлого соединения ещё были живы). **Фикс:** снапшот значений в локалы на старте `readPump`/`writePump` (keepalive фиксирован на время жизни соединения — перечитывать globals не нужно). `TestWebSocket_ClosesSilentClient` под `-race` теперь зелёный.
- **`pkg/orchestrator` (retry):** `RetryBackoff`/`MaxRetries` — те же грабли. Агентские горутины (`runWithRetry`) переживают возврат `Run()` (он не дожидается их), а тесты восстанавливали globals в `t.Cleanup` → race в `TestIntegration_RetryExhausted` под `-race`. **Фикс:** `RetryBackoff`/`MaxRetries` снимаются в снапшот на инстанс в `New()`/`NewSupervisor()` (`Orchestrator.maxRetries`/`retryBackoff`, `Supervisor.maxRetries`/`retryBackoff`); `runWithRetry` и `EvaluateStage` читают immutable-поля инстанса, а не globals. Package-level `RetryBackoff`/`MaxRetries` остаются как дефолты и для тестов.

### Lint: goconst + identical-switch-branches
- `supervisor.go`: литерал `"autonomous_execution"` заменён на константу `phaseAutonomous` (goconst).
- `orchestrator.go`: идентичные ветки `case phaseImplementation` и `case phaseAutonomous` (обе делали running→done) объединены (revive `identical-switch-branches`).
- `golangci-lint run ./...` = 0 issues; `setstatuslinter` чист.

### Test: устаревший `TestIntegration_DialogViolationDetected` → relocate
- Тест проверял **старое** поведение (fail-fast через `detectDialogViolation` при записи `question.json` вне stageDir), заменённое на авто-релокейт в коммите `2a759dd`. Из-за этого мок (эмитил Write-событие без реального файла) заставлял стадию висеть → таймаут 15с. **Фикс:** тест переписан как `TestIntegration_MisplacedQuestionRelocated` — мок создаёт файл в неверной директории, проверяется, что poller релокейтит его в stageDir и стадия уходит в `awaiting_user_input`. Дока в `CLAUDE.md` обновлена под актуальное поведение relocate.

### goga-accept: синхронизация CODEMANIFEST
- `pkg/state/CODEMANIFEST`: `SetApplyHook(h TransitionCallback)` → `SetApplyHook(h)` — тип `TransitionCallback` отсутствует в коде (висячая ссылка, реальная сигнатура `func SetApplyHook(h func(Transition))`); выравнено с конвенцией sibling `SetExecFunc(f)`.

## 2026-07-16

### Агент-Супервизор — автооценка автономности стадии
- Stage с `supervisor: true` на старте оценивается LLM-супервизором: может ли он выполниться **автономно** (один шаг через прикреплённый skill — пишет `execution_summary.md`, пропуская planning/approval/review) или требует стандартного многофазного цикла. Решение пишется в `<runDir>/supervisor.jsonl` (`events.jsonl` не трогается).
- **Fallback-safety:** любая ошибка LLM-вызова (таймаут, плохой JSON) → безопасный фолбэк на базовые фазы. 529/502/503/504 переживаются ретраем (см. ниже). Супервизор **только сокращает** фазы. Inline-артефакты → всегда стандартный трек.
- **Конфиг:** `flow.supervisor_command` (прим. `glm47`) > `config.supervisor.command` > `config.client.command`; `stage.supervisor: true` + опц. `stage.supervisor_prompt`. Промпт-шаблон `assets/prompts/autonomous.md`.
- **FSM:** новый переход `EvSupervisorApproved` (planning→ready), событие шины `EventSupervisorDecision`. Resume автономных стадий (`autonomous.flag` + `execution_summary.md`) в `recovery.go`. `CollectDependencyPlans` для автономных зависимостей читает `execution_summary.md` вместо `plan.md`.
- **Robust parsing:** `Supervisor.parseDecision` извлекает decision-JSON из: сырого JSON / claude-конверта `{"type":"result","result":"…"}` / claude-массива событий `[…]` / ```json-фенсов. Покрывает и `claude --output-format json` (container — single envelope, host — массив).
- Спек/план: `docs/superpowers/specs/2026-07-16-supervisor-agent-design.md`, `docs/superpowers/plans/2026-07-16-supervisor-agent.md`.

### Fix: supervisor LLM-вызов + claude-врапперы для `--output-format json`
- **RunJSONQuery** (`pkg/executor`) наследовал `e.cfg.ExtraArgs`, которые `executor.New` дефолтит в `DefaultClaudeArgs` (`--print --output-format stream-json --verbose --dangerously-skip-permissions`). Этот `stream-json` конфликтовал с `--output-format json` супервизора → claude exit 1. **Фикс:** чистая инвокация `-p <prompt> --output-format json` без ExtraArgs + захват stderr в ошибку (диагностика).
- **docker-враппер** (`pkg/docker/wrapper.go`): `--include-partial-messages` добавляется **только при `output-format=stream-json`** (раньше всегда — для json-вызова это давало `requires --output-format=stream-json`). Аналогичный фикс применён к host-врапперам `glm47/51/52` (ai-free): partial по stream-json, не по `-p`.
- Валидация live: `afm run` docker-режима с `feature.yaml` (`supervisor: true`, `supervisor_command: glm47`) — `supervisor.jsonl` содержит реальное решение (`decision=standard`, обоснование), до фикса был молчаливый fallback на каждой стадии.

### Supervisor: видимость в UI + ретраи + autonomous-диалог
- **Supervisor ретраит transient-ошибки** (529/502/503/504) тем же `RetryBackoff`×`MaxRetries`, что и stage-агенты (`EvaluateStage`), а не сразу фолбэчит. На non-retryable — сразу ошибка → фолбэк.
- **`validateDecision` строг только для autonomous** (`== ["autonomous_execution"]`); для standard фазы advisory (`DetermineStagePhases` и так возвращает base). Убран ложный fallback, когда LLM писал base phases одной строкой `"planning implementation"` (артефакт рендера Go-слайса) — валидное решение больше не пряталось из лога/UI. `BasePhases` рендерится как JSON `["planning","implementation"]`.
- **Решение супервизора публикуется в UI для обоих треков** (`EventSupervisorDecision` + `supervisor.jsonl`); раньше standard не публиковался — UI не видел резолюцию. В дашборде `supervisor_decision` выделен отдельной подсветкой (`.feed-entry.supervisor`) в Event feed.
- **Логи autonomous-агента видны в средней "Log" панели**: `handleLog` (`/api/stages/<id>/log`) и `buildDialogEntries` читают `autonomous.log`/`autonomous.jsonl` (раньше только planning/implementation/review → панель была пуста для autonomous-стадий).
- **Autonomous-фаза диалоговая**: `phaseAutonomous = "autonomous_execution"` — единая фаза без planning/impl/review (скилл всё делает сам, пишет `execution_summary.md`), НО скилл может спрашивать пользователя через тот же file-based dialog protocol (вопросы `autonomous_execution.q<N>.*`, валидная фаза, resume через `onUserAnswered`). Раннер получает `AFM_STAGE_DIR`, промпт включает `<interactive_rules>`.
- **Персистентный supervisor-decision badge** в хедере стадии в дашборде (решение видно и после ухода события из фида).
- **Fix host-режима `supervisor_command`**: wrapper генерируется, даже если ни одна стадия не использует команду как агента; секрет резолвится и в host-ветке (`UsedRecipes`).

## 2026-07-15

### Ретрай на 529/502/503/504 + удаление proxy и accounting
- `orchestrator.Classify` теперь классифицирует `API Error: 529/502/503/504` (raw-текст из glm-обёрток) как `ClassRetryable` (раньше `ClassFatal` → stage падал). 500 остаётся fatal.
- Полностью удалён built-in reverse proxy (`pkg/proxy`): ZAI-transform избыточен после ретрая, маршрутизация не нужна (autoShim-врапперы bake'ят прямой upstream-URL). Убрана threading-инфра в `run.go`/`orchestrator`/`executor`/`docker`.
- Полностью удалён accounting/подсчёт токенов (`pkg/accounting`): терял источник данных без прокси. Убраны `/api/usage`, dashboard `ConsumptionPanel`, config `proxy`/`pricing`/`accounting`.
- **Backward compat:** `yaml.Unmarshal` lenient → старые конфиги с `proxy`/`pricing`/`accounting` продолжают парситься (секции молча игнорируются). `autoShim:false` нейтрален (glm-обёртки уже шли напрямую). Учёт потребления отложен.

### claude-врапперы: bounded retry + stream-json + `--bare` (config `claude_bare`)
- **Bounded retry-loop** (`pkg/orchestrator/retry.go`): фикс `RetryBackoff=5s` × `MaxRetries=15` (как в ralphex) вместо прежнего exponential `[5s,10s,30s]` (4 попытки). z.ai 529 — transient; переживает окно overload. Подтверждено: claude шлёт `stream:true` сам (через `--output-format stream-json`), force-streaming не нужен.
- **`--output-format stream-json` + `--include-partial-messages`** добавлены в генерируемые claude-врапперы (`pkg/docker/wrapper.go`): покрывает non-interactive stages (которым executor не передаёт ExtraArgs) + даёт partial deltas. `--output-format` с дедупом (interactive уже получает его через executor).
- **`--bare` + config `client.claude_bare`**: `--bare` = minimal mode claude Code (skip CLAUDE.md/hooks/skills/memory), body ~4 KB вместо ~127 KB (ниже нагрузка на z.ai). **НО `--bare` ломает Skill-tool** — goga-* skills перестают резолвиться (агент имитирует их сам). Поэтому **default `claude_bare: false`** (skills важнее). `claude_bare: true` — для flows БЕЗ skills.

### `type: cursor` — Cursor Cloud Agents API
- Cursor Cloud API (`api.cursor.com`) **не имеет** синхронного OpenAI `/v1/chat/completions` (ответ 404) — это **Cloud Agents API**: асинхронный run-based API, где чат = запуск облачного код-агента. Поэтому `type: openai` (который дёргает `${BASE_URL}/chat/completions`) с Cursor **не работал и не может работать**. Историческая заметка про «Cursor через `api2.cursor.sh`» в `type: openai` была ошибочной — убрана.
- Новый тип recipe `type: cursor` → враппер с `CURSOR_*` env (`CURSOR_API_KEY`/`CURSOR_BASE_URL`/`CURSOR_MODEL`) и `exec /usr/local/bin/cursor-as-claude`. Адаптер: читает промпт из stdin → `POST /v1/agents` (no-repo, `mode:"agent"`) → опрашивает `GET /agents/{id}/runs/{runId}` до терминального статуса (`FINISHED`/`ERROR`/`CANCELLED`/`EXPIRED`) → эмитит claude stream-json (`assistant`-конверт с `result`-текстом + `result` event) → архивирует агента (best-effort, чтобы не плодить мусор). `model: auto` (или пусто) → поле `model` опускается, Cursor использует default.
- `auth.to` для cursor — любой `env:VAR` (по конвенции `CURSOR_API_KEY`); `url` обязателен (`https://api.cursor.com/v1`). Не требует `claude` в PATH (как openai). Требует `jq`+`curl` в образе. Тесты: `TestAgentRecipe_CursorType`, `TestCreateWrappers_CursorTemplate`/`_CursorNoClaudeRequired`.
- **Особенность:** первый ответ занимает ~30–90с (старт cloud-VM при создании агента); сам run дальше быстрый (`durationMs` секунды). Для интерактивного диалога — терпимо, но не мгновенно.

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
