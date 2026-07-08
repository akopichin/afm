# afm

CLI-инструмент для оркестрации многостадийных AI-задач. Описываешь задачу в YAML-файле, разбиваешь на стадии — afm запускает AI-агентов последовательно или параллельно, ждёт твоего одобрения планов и автоматически выполняет реализацию.

## Как это работает

Каждый запуск проходит три фазы:

```
1. Planning   — AI строит план для каждой стадии → ты просматриваешь и одобряешь
2. Execution  — AI реализует по плану (+ опциональный code review)
3. Summary    — AI пишет итоговый отчёт по всем стадиям
```

Стадии могут запускаться параллельно. Зависимости через `depends_on` гарантируют правильный порядок.

Состояние каждого запуска сохраняется в `.afm/runs/` — если прервать, `run` автоматически продолжит с того же места (прерванные стадии перезапускаются, завершённые — пропускаются).

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

Скрипт копирует бинарник в `/usr/local/bin` и устанавливает скиллы для Claude Code (`/afm`, `/afm-check`, `/afm-init`).

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

Образ включает: claude CLI, Node 22, Python 3.12, Go 1.26, git.

#### Аутентификация в Docker-режиме

Docker-контейнер — Linux, у него нет доступа к macOS Keychain, где хранятся OAuth-сессии claude. Поэтому нужно передать токен явно через переменную окружения.

**Claude Pro/Max (подписка claude.ai)**

```bash
# Один раз: сгенерировать долгоживущий токен
claude setup-token

# Добавить в ~/.zshrc / ~/.bashrc
export CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat01-...
```

После этого afm автоматически передаст токен в контейнер — больше ничего настраивать не нужно.

**Anthropic API Key**

```bash
export ANTHROPIC_API_KEY=sk-ant-api-...
```

Также передаётся автоматически.

**Смешанные агенты в одном флоу**

Если один стейдж использует `command: claude`, а другой — кастомного агента (например, `command: glm51`), всё работает само:
- стейджи с `command: claude` → напрямую на `api.anthropic.com` (без proxy)
- остальные стейджи → через настроенный proxy (z.ai и т.п.)

Никакой дополнительной настройки не нужно.

## Быстрый старт

### 1. Создать flow

```bash
afm init
```

Интерактивно задаёт вопросы и создаёт `.afm/flows/<name>.yaml`.

Или написать вручную — см. пример ниже.

### 2. Запустить

```bash
afm run flow.yaml

# Если flow лежит в .afm/flows/ — можно без аргумента:
afm run
```

Автоматически откроется веб-дашборд (по умолчанию `http://localhost:9876`).

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
```

### 4. Следить за прогрессом

```bash
afm check
```

```
Run: jwt-auth-20260416-152543

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

Все команды (`run`, `check`, `approve`, `revise`, `retry`, `init`, `list`) уважают `--dir`.

## Файл flow.yaml

```yaml
name: my-feature
description: "Краткое описание задачи"

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
| `id` | да | Уникальный идентификатор |
| `name` | нет | Человекочитаемое название для логов и дашборда (если пусто — показывается `id`) |
| `description` | да | Задача для AI |
| `prompt` | нет | Явная инструкция агенту — попадает в отдельный блок `<prompt>` после контекста стадии. В отличие от `description` (фон/контекст), это прямое указание что делать. Экранируется и не может внедрять XML-теги |
| `agents` | да | `planning`, `implementation`, `review` |
| `depends_on` | нет | ID стадий, которые должны завершиться раньше |
| `eager_planning` | нет | `true` — planning стартует сразу при запуске flow, не дожидаясь `depends_on`. По умолчанию planning ждёт завершения зависимостей |
| `skills` | нет | Claude-скиллы для агента |
| `plan` | нет | Путь к готовому план-файлу (пропускает planning) |
| `command` | нет | AI-команда для этой стадии (переопределяет config) |
| `max_parallel` | нет | Лимит параллельных стадий для этой команды |
| `interactive` | нет | `true` — включает файловый протокол диалога с пользователем через dashboard. Агенту передаётся env `AFM_STAGE_DIR`, он пишет `<phase>.q<N>.question.json` и ждёт `<phase>.q<N>.answer.json` через bash-цикл |
| `artifacts` | нет | Файлы, которые стадия производит для других стадий |
| `inputs` | нет | Артефакты из зависимых стадий (`stage.artifact`) |
| `verify` | нет | Shell-команда, выполняется в директории проекта после `.done`. Exit-код ≠ 0 — стадия не засчитывается: один ретрай агента с выводом команды в промпте, затем `failed`. Защита от ложного «done» |

### Передача контекста между стадиями

Планы зависимых стадий автоматически добавляются в промпт через `depends_on`. Для передачи файловых артефактов используй `artifacts` + `inputs`:

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

Стадия с `interactive: true` получает файловый протокол для диалога с пользователем через dashboard. Агенту передаётся env-переменная `AFM_STAGE_DIR` (путь к stage-директории). Чтобы задать вопрос, агент пишет файл `<phase>.q<N>.question.json` (где `<phase>` — `planning`/`implementation`/`review`, `N` растёт: q1, q2, …), а затем ждёт появления `<phase>.q<N>.answer.json` через bash-цикл. В dashboard появляется секция «Диалог», где пользователь отвечает. Пока ответа нет, стадия находится в статусе `awaiting_user_input`; после ответа выполнение продолжается.

Для запуска `claude` всегда добавляются флаги `--print --output-format stream-json --verbose --dangerously-skip-permissions` (`--verbose` обязателен для stream-json в Claude Code 2.1.x). Если интерактивный агент по ошибке запишет `question.json` вне `$AFM_STAGE_DIR`, стадия сразу перейдёт в `failed` с причиной `dialog protocol violation` (видно в `events.jsonl`) — это защита от вечного зависания.

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

Полный пример: `example-flow-interactive.yaml`

## Конфигурация

Создай `.afm/config.yaml` в проекте или `~/.afm/config.yaml` глобально:

```yaml
client:
  command: claude           # AI-команда (по умолчанию: claude)
  # extra_args: [--my-flag] # доп. аргументы

executor:
  idle_timeout: 30m         # таймаут простоя агента
  max_parallel: 4           # макс. параллельных стадий (0 = без ограничений)

server:
  port: 9876                # порт веб-дашборда
  open_browser: true        # открывать браузер при старте

# prompts_dir: .afm/prompts/  # кастомные шаблоны промптов
```

Приоритет настроек (от высокого к низкому):
1. CLI-флаги (`--max-parallel`, `--port`)
2. `.afm/config.yaml` проекта
3. `~/.afm/config.yaml` глобальный
4. Значения по умолчанию

## Встроенный прокси

afm умеет запускать встроенный reverse-прокси, который перехватывает HTTP-трафик AI-агентов к Anthropic-совместимым шлюзам и применяет трансформации. Главная цель — обходить ошибки `529` от `api.z.ai`: прокси превращает non-streaming-запрос в streaming, получает SSE-ответ и собирает его обратно в обычный JSON-ответ.

### Когда включается

Прокси включён по умолчанию. Он стартует, только если удалось определить **upstream** (адрес реального шлюза) — в таком порядке:

1. `proxy.upstream` в конфиге, иначе
2. переменная окружения `ANTHROPIC_BASE_URL`.

Если оба пусты — прокси пропускается. В логе старта это видно: `proxy: http://127.0.0.1:PORT → <upstream>` (запущен) или `proxy: skipped …` (пропущен). Прокси слушает только `127.0.0.1`.

### Конфигурация

```yaml
proxy:
  upstream: https://api.z.ai/api/anthropic   # адрес шлюза; если не задан — берётся из $ANTHROPIC_BASE_URL
  # port: 0          # порт прокси (0 = случайный свободный, по умолчанию)
  # enabled: false   # полностью отключить прокси
  # transforms:
  #   zai: true      # принудительно включить ZAI-трансформ
  #   zai: false     # принудительно выключить (даже когда upstream — api.z.ai)
```

ZAI-трансформ **авто-включается**, когда upstream содержит `api.z.ai` (`transforms.zai` не задан → авто-детект).

### Работа с обёртками над claude (например glm51)

Если `client.command` — обёртка (типа `glm51`), которая сама выставляет `ANTHROPIC_BASE_URL` и зовёт `claude`, **патчить обёртку не нужно**. afm создаёт временный shim — скрипт с именем `claude`, который выставляет адрес прокси и зовёт настоящий `claude`. Этот shim оказывается первым в `PATH` агента и перехватывает внутренний `exec claude` из обёртки — поэтому адрес прокси «переживает» перезатирание переменной внутри обёртки.

Единственное условие: настоящий `claude` должен быть в `PATH` (он нужен для создания shim'а). Если `claude` не найден — shim не создаётся (non-fatal warning), и прокси работает только для команд, которые сами читают `ANTHROPIC_BASE_URL` из окружения.

## Веб-дашборд

При запуске автоматически открывается дашборд:

- **Левая панель** — список стадий с цветными статус-индикаторами; под `id` показывается `name` стадии (если задано в flow). В заголовке центральной панели тоже отображается `name`, иначе — `id`
- **Центральная панель** — план с построчным ревью и inline-комментариями, лог агента (сообщения с markdown-форматированием)
- **Правая панель** — лента событий со всех стадий с бейджами источников
- **Прогресс-бар** — внизу, сколько стадий завершено

### Inline-комментарии к плану

Когда стадия в `awaiting_approval`:
1. Кликни на строку плана — откроется форма комментария
2. Напиши замечание — строка подсветится жёлтым
3. Нажми «Отправить правку (N)» — все комментарии отправятся агенту с номерами строк

### Resume при перезапуске

При повторном запуске `afm run` инструмент автоматически:
- Пропускает завершённые стадии (`done`)
- Сохраняет стадии ожидающие одобрения (`awaiting_approval`)
- Перезапускает прерванные стадии (`planning`, `running`, `revising`)
- Сохраняет стадии в `awaiting_user_input`: файлы вопросов/ответов переживают перезапуск, незакрытый вопрос снова показывается в dashboard, после ответа стадия продолжается

## Учёт потребления (токены / стоимость / трафик)

afm считает расход каждого стейджа: встроенный прокси перехватывает ответы агента, достаёт из них `usage` (input/output/cache токены + байты запроса/ответа) и пишет по одной записи в `.afm/runs/<run>/usage.jsonl`. Дальше записи агрегируются по стейджам и временным бакетам и отдаются через API и панель дашборда.

### Как посмотреть

- **API:** `GET /api/usage?metric=tokens|cost|kb&stage=<id>`
  - `metric` — `tokens` (input+output+cache), `cost` (USD), `kb` (запрос+ответ в KB)
  - `stage` опционален — без него по всем стейджам
  - ответ: `[{"stageId","timeBucket","metric","value"}]`
- **Дашборд:** выезжающая панель потребления (кнопка-стрелка) — переключатель метрик, фильтр по стейджу и график-таймсерия на SVG. Опция `cost` скрывается автоматически, если цены не настроены (массив cost пустой).

Принадлежность стейджу определяется по времени: запись относится к стейджу, в чьём окне выполнения лежит её timestamp. Окно открывается, когда стейдж уходит из `pending` в работу, и закрывается на `done`/`failed`.

### Цены (метрика cost)

`cost` считается только если в конфиге задан раздел `pricing` — цены за 1 млн токенов, по моделям:

```yaml
pricing:
  models:
    glm-5.2:           { input_per_mtok: 0.6,  output_per_mtok: 2.2,  cache_per_mtok: 0.11 }
    claude-sonnet-4-5: { input_per_mtok: 3.0,  output_per_mtok: 15.0, cache_per_mtok: 0.3 }
    # полный набор — в config.example.yaml
```

Ориентировочные list-price для общеизвестных моделей (Claude, GPT, GLM) уже лежат в `config.example.yaml` — скопируй оттуда. Цены примерные, проверяй актуальные у провайдера.

#### Как определяется модель (ключ цены)

Ключ в `pricing.models` должен **строго совпадать** со значением поля `model` из ответа upstream-API — именно его прокси кладёт в `usage.jsonl`. Сопоставления по префиксу или алиасу нет:

- модели `glm-*` (напр. `glm-5.2`) совпадают как есть;
- Claude API часто отдаёт `model` с датой (`claude-sonnet-4-5-20250929`) — тогда добавь в конфиг именно эту строку.

Точную строку для своего агента смотри в поле `model` файла `.afm/runs/<run>/usage.jsonl`.

## Структура директорий

```
.afm/
  flows/           # flow.yaml файлы
  runs/
    <flow>-<ts>/   # данные одного запуска
      state.json   # текущий статус всех стадий
      <stage-id>/
        plan.md          # план стадии
        planning.log     # лог агента планирования (stdout: tool actions)
        planning.jsonl   # raw stream-json
        planning.stderr.log  # stderr агента (диагностика, напр. ошибки claude)
        implementation.log
        review.log
        # файлы интерактивного диалога (только для interactive: true):
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

## Жизненный цикл стадии

```
pending → planning → awaiting_approval → ready → running → done
                ↓                                     ↓        ↘ failed
                └────→ awaiting_user_input ←──────────┘
         ↑                                         ↓
         └───────── revising ←────────────────────┘
```

- `pending` — ещё не запущена; planning стартует только после завершения всех `depends_on` (если не задан `eager_planning: true`)
- `planning` — AI строит план
- `awaiting_approval` — план готов, ждёт одобрения (через веб или CLI)
- `ready` — план одобрен, ждёт своей очереди (зависимости)
- `running` — AI реализует план
- `awaiting_user_input` — интерактивная стадия приостановлена: агент задал вопрос через файловый протокол и ждёт ответа пользователя; после ответа возвращается в `planning` или `running` (в ту фазу, где был задан вопрос)
- `done` / `failed` — завершена
- `revising` — отправлены правки, AI переделывает план

## Разработка.

```bash
make build    # собрать
make test     # тесты
make lint     # линтер
make clean    # удалить артефакты
```
