# Release Notes

Новые возможности — сверху, дальше вниз по устареванию. Даты — по коммитам в `fix`/`master`.

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
