# AFM Development Guide

Здесь — общие правила разработки и карта документации. Неочевидные ограничения
подсистем находятся в локальных `AGENTS.md`; читай их и при изменении смежного
кода, который использует эту подсистему. Точные символы и текущие поля ищи в коде.

## Рабочий процесс

- Сборка и требования к PR: [CONTRIBUTING.md](CONTRIBUTING.md).
- Go-версию из `go.mod` не повышай без обсуждения. После изменения Go-зависимостей
  выполни `make vendor` и включи изменения `vendor/` в тот же коммит.
- Коммиты — на русском. `CHANGELOG.md` и пользовательская документация — на
  английском. Changelog: новые даты `## YYYY-MM-DD` сверху, записи под
  `### Improvement:` / `### Fix:`; без ссылок на keepachangelog.com.
- Пользовательское поведение документируй в `docs/`; планы и исторические
  решения — в `docs/superpowers/` (не публикуется на сайте).
- Комментарии в коде — 1–2 строки сути: почему так, а не что делает код. Без
  номеров задач/фаз плана и истории решения — им место в коммите или
  `docs/superpowers/`. Проверка диффа: `scripts/check-comments.sh`.
- Добавляй проверки изменённого поведения. Для документации достаточно проверки
  ссылок и сборки сайта; команды остальных проверок выбирай по затронутому коду.

## Сборка и проверки

Источники команд — [Makefile](Makefile) и
[package.json](pkg/web/dashboard/package.json).

| Область | Проверка |
|---|---|
| Бинарник и встроенный dashboard | `make build` (сначала собирает web-assets) |
| Go | `make test` (`-race`), `make lint` (применяет исправления) |
| Dashboard | `make web-test web-typecheck`; Vite build их не заменяет |
| Список статусов | `make generate-check`; TS-файл генерируется из `state.AllStatuses` |
| YAML-конфигурация и схемы | `make schema-check` |
| SDK, отдельный Go-модуль | `make sdk-test sdk-lint`; интеграция — `make sdk-test-integration` |
| Документация | `mkdocs build --strict` после установки `docs/requirements.txt` |

Linux-only код и тесты (например, `pkg/server/workspace`) требуют Linux/Docker/CI.
Успешная проверка на macOS не означает, что они выполнились; cross-build проверяет
компиляцию, но не поведение.

## Composition and shared policies

- Точка сборки рана — `cmd/afm/run.go`. Подготовка окружения, lifecycle-хуки,
  dashboard и workspace выделены в соседние `run_*.go`; сохраняй владение
  ресурсами и cleanup через defer у вызывающего кода.
- Нормализуй конфигурацию стадий до создания оркестратора; например,
  `normalizeInteractiveStages` копирует slice до изменения `Interactive`.
- Runtime-граф читай через registry оркестратора: исходный `flow.Stages` не
  включает добавленные во время рана стадии. Изменение графа должно переживать
  replay и не переписывать пользовательский `flow.yaml`.
- Сервер разделяет конфигурацию стадии (`StageConfig`) и runtime-представление
  (`StageView`). Клиент передаёт имя кнопки, её промпт резолвится на сервере.
- При изменении YAML-контракта обновляй парсер, валидацию, схемы и документацию;
  при изменении API — согласованные серверные и клиентские типы.

## Working directory: `.afm` and `--dir`

- afm-root — родитель `.afm/`, выбирается как `--dir` > `AFM_DIR` > `.`.
  Для конфигурации и runs используй общие path-хелперы CLI, не литералы `.afm`.
- `flow.root_dir` задаёт CWD агентов. Относительный путь считается от afm-root;
  без него агент наследует CWD процесса. Worktree меняет эффективный корень
  проекта — используй существующий резолвер вместо повторного вычисления пути.
- `AFM_STAGE_DIR` всегда абсолютный и указывает внутрь run directory, независимо
  от CWD агента. Пути lifecycle-payload/env тоже должны сохранять смысл после
  пересечения границы процесса или контейнера.

## State persistence & run lifecycle

- `events.jsonl` — источник истины, `state.json` — производный кэш. Read-only
  пути восстанавливают состояние через `state.LoadRunState`, без writer-lock.
- Намерение пользователя сохраняется durable до возврата API; только выигравший
  CAS запускает работу. Storage-fatal завершает ран; CAS-мисс — обычный no-op.
- Работа, запущенная через HTTP, живёт под run-scoped context. `r.Context()`
  отменится при возврате хендлера и не должен быть родителем агента.
- Clean shutdown: отменить run context → дождаться агентов → закрыть Store.
  Это относится и к тестам: используй cleanup с ожиданием `Run`, а не только
  ожидание статуса стадии. Иначе возможен `write events.jsonl: invalid argument`.
- Новые пути запуска/завершения используй через общие хелперы оркестратора:
  сохраняй lease командного семафора, проверки Pause и каскад зависимых стадий.
- Non-FSM UI-уведомления, которые должны переживать reconnect, публикуй live и в
  `notices.jsonl` через общий путь. Критические операции сохраняют свой durable
  путь; best-effort notice не заменяет запись в журнал операций.

## Окружение и границы доступа

- Секретные значения не должны попадать в argv, логи и notices. Для передачи
  Docker используй существующий bare `-e NAME` транспорт, для browser — дескриптор.
  Сохраняй фильтрацию транспортных секретов во всех дочерних процессах.
- Для `env:`-секретов приоритет: project `secrets.env` > global `secrets.env` >
  окружение процесса. Общий резолвер — `pkg/secrets`.
- `InContainer()` определяет любой контейнер; `ReExecedIntoContainer()` — только
  собственный Docker re-exec с afm-транспортом. Эти проверки не взаимозаменяемы.
- Read-only промпт не даёт ОС-изоляции. Сохраняй различие гарантий host/container
  и не выдавай verify/helper-агентам managed browser.
- Пины инструментов в `Dockerfile.runtime` меняй после ручной проверки с afm.
  Версия Chrome DevTools MCP должна совпадать с host-пином в `pkg/browser`.

## Где читать подробности

| Задача | Правила разработки | Поведение и формат |
|---|---|---|
| Журнал, replay, таймеры | [state](pkg/state/AGENTS.md) | [Event log](docs/internals/event-log.md), [Collaboration journal](docs/collaboration-journal.md) |
| Тестируемое время (`clock.Clock` seam) | [clock](pkg/clock/AGENTS.md) | — |
| Scheduling, Pause/Continue, диалог, verify, helpers | [orchestrator](pkg/orchestrator/AGENTS.md) | [Lifecycle](docs/stage-lifecycle.md), [Interactive](docs/interactive-stages.md), [Verify](docs/verify.md), [Collaboration](docs/collaboration.md) |
| Процессы и изоляция агентов | [executor](pkg/executor/AGENTS.md) | [Prompts](docs/internals/prompts.md) |
| HTTP и файловый браузер | [server](pkg/server/AGENTS.md) | [Dashboard](docs/dashboard.md), [Docker](docs/docker.md) |
| React dashboard | [dashboard](pkg/web/dashboard/AGENTS.md) | [Dashboard](docs/dashboard.md) |
| Docker re-exec и wrappers | [docker](pkg/docker/AGENTS.md) | [Docker](docs/docker.md), [Configuration](docs/config-reference.md) |
| Lifecycle-хуки | [lifecyclehooks](pkg/lifecyclehooks/AGENTS.md) | [Lifecycle hooks](docs/lifecycle-hooks.md) |
| Memory: live и rebuild | [memorypipeline](pkg/memorypipeline/AGENTS.md) | [Agent memory](docs/agent-memory.md) |
| Managed Chrome | [browser](pkg/browser/AGENTS.md) | [Browser](docs/browser.md) |

Формат флоу — [flow-reference](docs/flow-reference.md), автономный трек —
[autonomous-track](docs/autonomous-track.md), script-стадии —
[script-stages](docs/script-stages.md), артефакты — [artifacts](docs/artifacts.md).

## Как поддерживать этот файл

Добавляй сюда только общие неочевидные правила: что нельзя нарушить и почему.
Локальные ограничения держи в ближайшем `AGENTS.md`, пользовательские примеры —
в `docs/`. Не дублируй каталоги полей, событий, версий и функций: они устаревают
быстрее правил. Из исправленного бага сохраняй инвариант и регрессионную проверку,
а историю расследования оставляй в коммите или design-документе.
