# afm

CLI-инструмент для оркестрации многостадийных AI-задач. Описываешь задачу в YAML-файле, разбиваешь на стадии — afm запускает AI-агентов последовательно или параллельно, ждёт твоего одобрения планов и автоматически выполняет реализацию. Работает с `claude` и с любыми claude-совместимыми агентами (GLM, DeepSeek, Cursor и т.п.).

## Как это работает

Каждая стадия по умолчанию проходит через фазы:

```
1. Planning   — AI строит план стадии → ты просматриваешь и одобряешь (или правишь)
2. Execution  — AI реализует по одобренному плану (+ опциональный code review)
```

Стадии могут запускаться параллельно; зависимости через `depends_on` гарантируют правильный порядок. Планы и артефакты зависимых стадий автоматически подставляются в промпт.

**Автономный трек (опционально).** Если для стадии включён супервизор, agent-супервизор (LLM) сам оценивает, нужен ли полный цикл. Для простых стадий он схлопывает planning/implementation/review в один шаг `autonomous_execution` — агент со скиллами делает работу сразу и пишет `execution_summary.md`, без плана и без одобрения. При любой ошибке LLM — безопасный откат на обычные фазы. См. [Супервизор и автономный трек](#супервизор-и-автономный-трек).

**Надёжность.** Состояние каждого запуска пишется в событийный лог `.afm/runs/<run>/events.jsonl` (append + fsync) — это единственный источник правды. Если запуск прервать, `afm run` автоматически продолжит с того же места: завершённые стадии пропускаются, прерванные перезапускаются. Пока `afm run` активен, он держит эксклюзивную блокировку run-директории (`.lock`) — параллельный `afm approve/retry/revise` из другого процесса не сможет повредить живой лог.

## Установка

**Из исходников:**
```bash
make build        # собрать в bin/afm
make install      # установить через go install
```

**Готовый бинарник + Claude-скиллы:**
```bash
./install.sh
```

Скрипт копирует бинарник в `/usr/local/bin` и устанавливает скиллы для Claude Code (`/afm`, `/afm-check`, `/afm-init`, `/afm-retry`, `/afm-review`).

### Запуск в Docker (без локальной установки)

```bash
docker run --rm -it \
  -v $(pwd):/project \
  -v ~/.claude:/home/afm/.claude \
  -v ~/.afm:/home/afm/.afm \
  -e AFM_HOST_UID=$(id -u) -e AFM_HOST_GID=$(id -g) \
  -e ANTHROPIC_API_KEY \
  akopichin/afm:latest \
  run flow.yaml
```

Или включить автоматический Docker-режим в конфиге — тогда обычная команда `afm run` сама перезапустится в контейнере:

```yaml
# .afm/config.yaml
docker:
  enabled: true
```

Образ включает: claude CLI, Node 22, Python 3.12, Go 1.26, git. Контейнер стартует под root, но entrypoint (`gosu`) сразу дропает привилегии до твоего хостового uid/gid — файлы в томах принадлежат тебе, а не root.

#### Аутентификация в Docker-режиме

Docker-контейнер — Linux, у него нет доступа к macOS Keychain, где хранятся OAuth-сессии claude. Поэтому нужно передать токен явно через переменную окружения — afm пробросит его в контейнер автоматически.

**Claude Pro/Max (подписка claude.ai)**

```bash
# Один раз: сгенерировать долгоживущий токен
claude setup-token

# Добавить в ~/.zshrc / ~/.bashrc
export CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat01-...
```

**Anthropic API Key**

```bash
export ANTHROPIC_API_KEY=sk-ant-api-...
```

Также поддерживаются `ANTHROPIC_AUTH_TOKEN` и `ANTHROPIC_BASE_URL` — все пробрасываются в bare-форме (`-e KEY` без значения), чтобы секрет не светился в `ps`/history.

#### Нестандартные агенты в Docker (autoShim)

Если стадия использует не-claude команду (`command: glm51`, `command: deepseek` и т.п.), в Docker есть два варианта:

- **Монтирование:** afm находит бинарник через `which` и монтирует его в контейнер (`:ro`). Подходит, если у агента нет внешних зависимостей.
- **autoShim (рекомендуется):** по `docker.autoShim: true` afm генерирует claude-совместимую обёртку прямо в контейнере из рецепта `docker.agents.<cmd>` — без монтирования бинарника и без проброса токенов файлами. Секрет читается на хосте и передаётся transient-env. Поддерживаются типы `claude` (по умолчанию), `openai` (DeepSeek/OpenAI-совместимые) и `cursor` (Cursor Cloud Agents API).

```yaml
docker:
  autoShim: true
  agents:
    glm51:
      model: glm-5.1
      url: https://api.z.ai/api/anthropic
      auth: { from: "file:~/.ai-free/claude-glm/token", to: "env:ANTHROPIC_AUTH_TOKEN" }
```

Подробности и примеры — в `config.example.yaml`, `example-flow-cursor.yaml` и `CLAUDE.md` (раздел Docker Mode).

## Быстрый старт

### 1. Создать flow

```bash
afm init
```

Интерактивно задаёт вопросы и создаёт `.afm/flows/<name>.yaml`. Или написать вручную — см. пример ниже.

### 2. Запустить

```bash
afm run flow.yaml

# Если flow лежит в .afm/flows/ — можно без аргумента:
afm run
```

По умолчанию поднимается веб-дашборд (`http://localhost:9876`); в лог печатается его URL.

### 3. Одобрить планы

После фазы планирования каждая стадия переходит в `awaiting_approval`. Есть два способа:

**Через веб-дашборд** — открой `http://localhost:9876`, выбери стадию, просмотри план с построчным ревью, оставь inline-комментарии к конкретным строкам (как в MR) и нажми «Одобрить» или «Отправить правку».

**Через CLI:**
```bash
# Посмотреть план
cat .afm/runs/<run-dir>/<stage-id>/plan.md

# Одобрить
afm approve backend-auth

# Не нравится — попросить переделать
afm revise backend-auth --feedback "Нужно добавить Redis для блеклиста токенов"

# Перезапустить упавшую стадию
afm retry backend-auth
```

> CLI-мутации (`approve`/`revise`/`retry`) работают, когда `afm run` НЕ запущен (headless-сценарий). При активном `afm run` одобряй через дашборд — иначе команда сообщит, что run заблокирован.

### 4. Следить за прогрессом

```bash
afm check
```

```
Run: jwt-auth-20260416-152543-a3f9

STAGE                 STATUS                 UPDATED
-----                 ------                 -------
backend-auth          done                   15:31:02
frontend-login        running                15:31:45
integration-tests     pending                15:31:02
```

Или в реальном времени через веб-дашборд — стадии, прогресс-бар, лента событий, логи.

## Указание рабочей директории

По умолчанию `.afm/` создаётся в текущей папке. Чтобы вынести её в другое место:

```bash
# Флаг (разовый запуск)
afm --dir ~/my-flows run

# Переменная окружения (постоянно)
export AFM_DIR=~/my-flows
afm run
```

Все команды (`run`, `check`, `approve`, `revise`, `retry`, `init`, `list`) уважают `--dir`. Приоритет: флаг `--dir` > env `AFM_DIR` > текущая директория.

## Файл flow.yaml

```yaml
name: my-feature
description: "Краткое описание задачи"
# supervisor_command: glm51    # опц. — команда агента-супервизора для всего флоу

stages:

  - id: backend          # уникальный ID стадии
    name: "Backend API"
    description: |
      Что нужно сделать — подробно.
      AI будет ориентироваться на этот текст при планировании и реализации.
    agents: [planning, implementation, review]
    skills:              # опционально — скиллы Claude
      - superpowers:test-driven-development
    command: claude      # опционально — своя AI-команда для этой стадии
    max_parallel: 2      # опционально — лимит параллельности для этой команды
    artifacts:           # файлы, которые стадия передаёт дальше
      - name: api-contract
        path: docs/api-contract.yaml
        description: "OpenAPI спецификация"
      - name: db-schema
        path: ./schema.sql        # ./ = относительно stage-директории в run
        description: "SQL миграция"
        inline: false             # передать путь, не содержимое

  - id: frontend
    name: "Frontend"
    description: "Реализовать UI по API-контракту"
    agents: [planning, implementation]
    depends_on: [backend]         # запустится только после завершения backend
    inputs:                       # артефакты из зависимых стадий
      - backend.api-contract      # содержимое файла подставится в промпт
      - ref: backend.db-schema    # опциональный — не блокирует если файла нет
        optional: true

  - id: db-migration
    name: "DB Migration"
    description: "Применить миграцию"
    agents: [implementation]
    plan: docs/plans/migration.md   # готовый план — planning agent не запускается
    verify: "make test"             # команда-гейт: exit != 0 — стадия не done
```

**Поля стадии:**

| Поле | Обязательно | Описание |
|------|-------------|----------|
| `id` | да | Уникальный идентификатор (алфавит/цифры/`_`/`-`) |
| `name` | нет | Человекочитаемое название для логов и дашборда (если пусто — показывается `id`) |
| `description` | да | Задача для AI (фон/контекст) |
| `prompt` | нет | Явная инструкция агенту — отдельный блок `<prompt>` после контекста. В отличие от `description`, это прямое указание что делать. Экранируется, не может внедрять XML-теги |
| `agents` | да | Комбинация из `planning`, `implementation`, `review` |
| `depends_on` | нет | ID стадий, которые должны завершиться раньше |
| `eager_planning` | нет | `true` — planning стартует сразу при запуске flow, не дожидаясь `depends_on` |
| `skills` | нет | Claude-скиллы для агента |
| `plan` | нет | Путь к готовому план-файлу (пропускает planning) |
| `command` | нет | AI-команда для этой стадии (переопределяет `client.command` из конфига) |
| `max_parallel` | нет | Лимит параллельных стадий для этой команды |
| `interactive` | нет | `true` — включает файловый протокол диалога с пользователем через dashboard (см. ниже) |
| `supervisor` | нет | `true` — разрешить супервизору оценить стадию и, возможно, перевести её на автономный трек (нужен `supervisor_command`) |
| `supervisor_prompt` | нет | Доп. контекст для супервизора при оценке этой стадии |
| `artifacts` | нет | Файлы, которые стадия производит для других стадий |
| `inputs` | нет | Артефакты из зависимых стадий (`stage.artifact`) |
| `verify` | нет | Shell-команда после `.done`. Exit ≠ 0 — стадия не засчитывается: один ретрай с выводом команды в промпте, затем `failed`. Защита от ложного «done» |

**Поля флоу (верхний уровень):** `name`, `description`, `prompt` (глобальная инструкция во все стадии), `max_parallel`, `supervisor_command` (команда агента-супервизора), `stages`.

### Передача контекста между стадиями

Планы (и `execution_summary.md` автономных стадий) зависимых стадий автоматически добавляются в промпт через `depends_on`. Для передачи файловых артефактов — `artifacts` + `inputs`:

```yaml
stages:
  - id: backend
    artifacts:
      - name: api-contract
        path: docs/api-contract.yaml
        description: "OpenAPI schema"
      - name: db-schema
        path: ./schema.sql           # ./ = stage-директория в run
        description: "SQL миграция"
        inline: false                 # передать путь, не содержимое

  - id: frontend
    depends_on: [backend]
    inputs:
      - backend.api-contract          # обязательный артефакт
      - ref: backend.db-schema        # опциональный
        optional: true
```

- `inline: true` (по умолчанию) — содержимое файла вставляется в промпт
- `inline: false` — в промпт передаётся путь к файлу
- `optional: true` — если файл не найден, стадия запускается без него

### Интерактивные стадии

Стадия с `interactive: true` получает файловый протокол для диалога с пользователем через dashboard. Агенту передаётся env-переменная `AFM_STAGE_DIR` (путь к stage-директории). Чтобы задать вопрос, агент пишет файл `<phase>.q<N>.question.json` (`<phase>` — `planning`/`implementation`/`review`; `N` растёт: q1, q2, …), а затем ждёт появления `<phase>.q<N>.answer.json` через bash-цикл. В dashboard появляется секция «Диалог», где пользователь отвечает. Пока ответа нет, стадия в статусе `awaiting_user_input`; после ответа выполнение продолжается.

Для запуска `claude` всегда добавляются флаги `--print --output-format stream-json --verbose --dangerously-skip-permissions` (`--verbose` обязателен для stream-json в Claude Code 2.1.x). Если интерактивный агент по ошибке запишет `question.json` вне `$AFM_STAGE_DIR` (баг GLM-4.7: путь из CWD вместо env), poller авто-релокейтит файл внутрь stageDir и создаёт симлинк для ответа — стадия уходит в `awaiting_user_input`, а не зависает.

```yaml
stages:
  - id: discovery
    name: "Сбор требований"
    description: |
      Спроси у пользователя preferred language через файловый протокол (id: q1):
      запиши $AFM_STAGE_DIR/implementation.q1.question.json и дождись
      ответа $AFM_STAGE_DIR/implementation.q1.answer.json.
      После ответа запиши итог в ./summary.md.
    agents: [implementation]
    interactive: true
    artifacts:
      - name: summary
        path: ./summary.md
```

Полный пример: `example-flow-interactive.yaml`.

> **Ожидание ответа и idle-timeout.** Пока стадия ждёт ответа, агент простаивает и не пишет в stdout. По умолчанию `executor.idle_timeout` = 30 мин — если не ответить за это время, ждущий агент может быть убит. Для долгих ожиданий подними таймаут: `executor: { idle_timeout: 24h }`.

## Супервизор и автономный трек

Супервизор — это отдельный LLM-агент, который перед запуском стадии решает, нужен ли ей полный цикл planning→approval→implementation, или её можно выполнить автономно за один шаг.

Включается для стадии, когда:
1. в конфиге/флоу задана команда супервизора (`supervisor.command` в config или `supervisor_command` во флоу), и
2. у стадии стоит `supervisor: true`.

Если супервизор решает `can_execute_autonomously`, стадия переводится на трек `autonomous_execution`: агент со скиллами делает работу сразу (без `plan.md` и без одобрения) и обязан написать `execution_summary.md` — он служит артефактом для зависимых стадий вместо плана. Иначе стадия идёт обычным циклом.

- Решение супервизора публикуется в дашборд и пишется в `.afm/runs/<run>/supervisor.jsonl` (аудит).
- Любая ошибка LLM/парсинга → безопасный фолбэк на базовые фазы (флоу не падает).
- Стадия с inline-артефактом всегда идёт обычным циклом (агенту нужен контекст артефакта в плане).

```yaml
# config.yaml
supervisor:
  command: glm51        # команда агента-супервизора

# flow.yaml
stages:
  - id: rename-var
    description: "Переименовать переменную foo → bar во всём модуле"
    agents: [planning, implementation]
    supervisor: true     # разрешить супервизору схлопнуть в автономный шаг
```

## Конфигурация

Создай `.afm/config.yaml` в проекте или `~/.afm/config.yaml` глобально (полный пример — `config.example.yaml`):

```yaml
client:
  command: claude           # AI-команда (по умолчанию: claude)
  # extra_args: [--my-flag] # доп. аргументы
  # claude_bare: false      # true → добавлять --bare в генерируемые обёртки (меньше нагрузка,
                            #        но отключает auto-discovery скиллов). Default: false

executor:
  idle_timeout: 30m         # таймаут простоя агента
  max_parallel: 4           # макс. параллельных стадий (0 = без ограничений)

server:
  port: 9876                # порт веб-дашборда
  open_browser: false       # открывать браузер при старте (default: false)

supervisor:
  command: glm51            # команда агента-супервизора (для stages с supervisor: true)

# theme: goga               # тема дашборда: goga | novacorps (default: novacorps)
# prompts_dir: .afm/prompts/  # кастомные шаблоны промптов

docker:
  enabled: false            # true / env AFM_USE_DOCKER=1 — перезапуск в контейнере
  # image: akopichin/afm:latest
  # autoShim: true          # генерировать claude-обёртки для agents.<cmd> в контейнере
  # extra_mounts: [~/.ai-free]  # доп. хост-пути в контейнер (:ro)
  # agents:                 # рецепты для autoShim (см. config.example.yaml)
  #   glm51: { model: glm-5.1, url: https://api.z.ai/api/anthropic,
  #            auth: { from: "file:~/.ai-free/claude-glm/token", to: "env:ANTHROPIC_AUTH_TOKEN" } }
```

Приоритет настроек (от высокого к низкому):
1. CLI-флаги (`--max-parallel`, `--port`, `--require-approval`)
2. `.afm/config.yaml` проекта
3. `~/.afm/config.yaml` глобальный
4. Значения по умолчанию

## Веб-дашборд

При запуске (если `server.open_browser: true`) открывается дашборд; иначе его URL печатается в лог.

- **Левая панель** — список стадий с цветными статус-индикаторами; под `id` показывается `name` стадии (если задано). В заголовке центральной панели — тоже `name`, иначе `id`
- **Центральная панель** — план с построчным ревью и inline-комментариями, лог агента (markdown), секция «Диалог» для интерактивных стадий
- **Правая панель** — лента событий со всех стадий с бейджами источников (включая решения супервизора)
- **Прогресс-бар** — внизу, сколько стадий завершено

### Inline-комментарии к плану

Когда стадия в `awaiting_approval`:
1. Кликни на строку плана — откроется форма комментария
2. Напиши замечание — строка подсветится жёлтым
3. Нажми «Отправить правку (N)» — все комментарии отправятся агенту с номерами строк

### Resume при перезапуске

При повторном запуске `afm run` инструмент автоматически:
- Пропускает завершённые стадии (`done`)
- Сохраняет стадии, ожидающие одобрения (`awaiting_approval`)
- Перезапускает прерванные стадии (`planning`, `running`, `revising`, `retrying`)
- Восстанавливает автономные стадии (по `execution_summary.md` / `autonomous.flag`)
- Сохраняет стадии в `awaiting_user_input`: файлы вопросов/ответов переживают перезапуск, незакрытый вопрос снова показывается в dashboard, после ответа стадия продолжается

Одобрение/правка/ретрай фиксируются в логе долговечно (fsync) до того, как управление вернётся — краш сразу после одобрения не теряет интент, recovery продолжит с корректного состояния.

## Структура директорий

```
.afm/
  flows/           # flow.yaml файлы
  runs/
    <flow>-<ts>-<rand>/    # данные одного запуска (rand — чтобы не было коллизий)
      events.jsonl   # событийный лог переходов — ИСТОЧНИК ПРАВДЫ (append + fsync)
      state.json     # производный снапшот статусов (кэш; читатели берут правду из лога)
      .lock          # flock активного afm run
      supervisor.jsonl       # решения супервизора (если включён)
      <stage-id>/
        plan.md          # план стадии
        planning.log     # лог агента планирования (stdout: tool actions)
        planning.jsonl   # raw stream-json
        planning.stderr.log  # stderr агента (диагностика claude)
        implementation.log
        review.log
        .done                # маркер завершения реализации
        # автономный трек (если супервизор перевёл стадию):
        autonomous.flag      # маркер автономной стадии
        autonomous.log
        execution_summary.md # итог автономной работы (артефакт для зависимых)
        # файлы интерактивного диалога (interactive: true):
        <phase>.q<N>.question.json   # вопрос агента
        <phase>.q<N>.answer.json     # ответ пользователя
        <phase>.dialog.jsonl         # история диалога для UI
  config.yaml      # конфиг проекта (опционально)
```

## Использование в Claude Code

После `./install.sh` доступны скиллы:

- `/afm` — запускает flow, мониторит и запрашивает одобрения планов прямо в чате
- `/afm-check` — показывает статус текущего запуска
- `/afm-init` — создаёт flow.yaml интерактивно
- `/afm-retry` — перезапускает упавшую стадию
- `/afm-review` — просмотр плана стадии с фидбэком/одобрением

## Жизненный цикл стадии

```
pending → planning → awaiting_approval → ready → running → done
                ↓                                     ↓        ↘ failed
                └────→ awaiting_user_input ←──────────┘
         ↑                                         ↓
         └───────── revising ←────────────────────┘

# автономный трек (супервизор):
pending → (supervisor) → running(autonomous_execution) → done
```

- `pending` — ещё не запущена; planning стартует после завершения всех `depends_on` (если нет `eager_planning: true`)
- `planning` — AI строит план (или супервизор оценивает стадию)
- `awaiting_approval` — план готов, ждёт одобрения (веб или CLI)
- `ready` — план одобрен, ждёт своей очереди
- `running` — AI реализует план (или выполняет автономный трек)
- `awaiting_user_input` — интерактивная стадия ждёт ответа пользователя; после ответа возвращается в фазу, где был задан вопрос
- `revising` — отправлены правки, AI переделывает план
- `retrying` — временная ошибка (rate limit / 5xx), автоповтор с бэкоффом
- `done` / `failed` — завершена

## Разработка

```bash
make build        # собрать (bin/afm)
make test         # тесты (с -race)
make lint         # линтер
make install      # go install
make install-skills   # установить /afm-* скиллы в ~/.claude
make docker-build     # собрать Docker-образ
make clean        # удалить артефакты
```

Версионированный релиз образа: `make release-patch` / `release-minor` / `release-major` (авто-бамп SemVer-тега, пуш `:vX.Y.Z` + `:latest`).
