# Release Notes

Новые возможности — сверху, дальше вниз по устареванию. Даты — по коммитам в `fix`/`master`.

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
