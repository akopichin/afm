# Release Notes

Новые возможности — сверху, дальше вниз по устареванию. Даты — по коммитам в `fix`/`master`.

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
