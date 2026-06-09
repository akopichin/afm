# flowManager

CLI-инструмент для оркестрации многостадийных AI-задач. Описываешь задачу в YAML-файле, разбиваешь на стадии — flowManager запускает AI-агентов последовательно или параллельно, ждёт твоего одобрения планов и автоматически выполняет реализацию.

## Как это работает

Каждый запуск проходит три фазы:

```
1. Planning   — AI строит план для каждой стадии → ты просматриваешь и одобряешь
2. Execution  — AI реализует по плану (+ опциональный code review)
3. Summary    — AI пишет итоговый отчёт по всем стадиям
```

Стадии могут запускаться параллельно. Зависимости через `depends_on` гарантируют правильный порядок.

Состояние каждого запуска сохраняется в `.flowManager/runs/` — если прервать, `run` автоматически продолжит с того же места (прерванные стадии перезапускаются, завершённые — пропускаются).

## Установка

**Из исходников:**
```bash
make build        # собрать в bin/flowmanager
make install      # установить через go install
```

**Готовый бинарник + Claude-скиллы:**
```bash
./install.sh
```

Скрипт копирует бинарник в `/usr/local/bin` и устанавливает скиллы для Claude Code (`/flowmanager`, `/flowmanager-check`, `/flowmanager-init`).

## Быстрый старт

### 1. Создать flow

```bash
flowmanager init
```

Интерактивно задаёт вопросы и создаёт `.flowManager/flows/<name>.yaml`.

Или написать вручную — см. пример ниже.

### 2. Запустить

```bash
flowmanager run flow.yaml

# Если flow лежит в .flowManager/flows/ — можно без аргумента:
flowmanager run
```

Автоматически откроется веб-дашборд (по умолчанию `http://localhost:9876`).

### 3. Одобрить планы

После фазы планирования каждая стадия переходит в `awaiting_approval`. Есть два способа:

**Через веб-дашборд** — открой `http://localhost:9876`, выбери стадию, просмотри план с построчным ревью, оставь inline-комментарии к конкретным строкам (как в MR) и нажми «Одобрить» или «Отправить правку».

**Через CLI:**
```bash
# Посмотреть план
cat .flowManager/runs/<run-dir>/<stage-id>/plan.md

# Одобрить
flowmanager approve backend-auth

# Не нравится — попросить переделать
flowmanager revise backend-auth --feedback "Нужно добавить Redis для блеклиста токенов"
```

### 4. Следить за прогрессом

```bash
flowmanager check
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
```

**Поля стадии:**

| Поле | Обязательно | Описание |
|------|-------------|----------|
| `id` | да | Уникальный идентификатор |
| `name` | да | Название для логов |
| `description` | да | Задача для AI |
| `agents` | да | `planning`, `implementation`, `review` |
| `depends_on` | нет | ID стадий, которые должны завершиться раньше |
| `skills` | нет | Claude-скиллы для агента |
| `plan` | нет | Путь к готовому план-файлу (пропускает planning) |
| `command` | нет | AI-команда для этой стадии (переопределяет config) |
| `max_parallel` | нет | Лимит параллельных стадий для этой команды |
| `interactive` | нет | `true` — включает MCP-tool `ask_user` для диалога с пользователем через dashboard |
| `artifacts` | нет | Файлы, которые стадия производит для других стадий |
| `inputs` | нет | Артефакты из зависимых стадий (`stage.artifact`) |

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

Стадия с `interactive: true` получает MCP-tool `ask_user` для диалога с пользователем через dashboard. Агент вызывает `ask_user` с вопросом и вариантами ответа — в dashboard появляется секция «Диалог», где пользователь отвечает. Пока ответа нет, стадия находится в статусе `awaiting_user_input`.

```yaml
stages:
  - id: discovery
    name: "Сбор требований"
    description: |
      Спроси у пользователя preferred language через ask_user (id: q1).
      После ответа запиши итог в ./summary.md.
    agents: [implementation]
    interactive: true
    artifacts:
      - name: summary
        path: ./summary.md
```

Полный пример: `example-flow-interactive.yaml`

## Конфигурация

Создай `.flowManager/config.yaml` в проекте или `~/.flowManager/config.yaml` глобально:

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

# prompts_dir: .flowManager/prompts/  # кастомные шаблоны промптов
```

Приоритет настроек (от высокого к низкому):
1. CLI-флаги (`--max-parallel`, `--port`)
2. `.flowManager/config.yaml` проекта
3. `~/.flowManager/config.yaml` глобальный
4. Значения по умолчанию

## Веб-дашборд

При запуске автоматически открывается дашборд:

- **Левая панель** — список стадий с цветными статус-индикаторами
- **Центральная панель** — план с построчным ревью и inline-комментариями, лог агента (только текстовые сообщения)
- **Правая панель** — лента событий со всех стадий с бейджами источников
- **Прогресс-бар** — внизу, сколько стадий завершено

### Inline-комментарии к плану

Когда стадия в `awaiting_approval`:
1. Кликни на строку плана — откроется форма комментария
2. Напиши замечание — строка подсветится жёлтым
3. Нажми «Отправить правку (N)» — все комментарии отправятся агенту с номерами строк

### Resume при перезапуске

При повторном запуске `flowmanager run` инструмент автоматически:
- Пропускает завершённые стадии (`done`)
- Сохраняет стадии ожидающие одобрения (`awaiting_approval`)
- Перезапускает прерванные стадии (`planning`, `running`, `revising`)

## Структура директорий

```
.flowManager/
  flows/           # flow.yaml файлы
  runs/
    <flow>-<ts>/   # данные одного запуска
      state.json   # текущий статус всех стадий
      <stage-id>/
        plan.md          # план стадии
        planning.log     # лог агента планирования
        planning.jsonl   # raw stream-json
        implementation.log
        review.log
  config.yaml      # конфиг проекта (опционально)
```

## Использование в Claude Code

После `./install.sh` доступны скиллы:

- `/flowmanager` — запускает flow, мониторит и запрашивает одобрения планов прямо в чате
- `/flowmanager-check` — показывает статус текущего запуска
- `/flowmanager-init` — создаёт flow.yaml интерактивно

## Жизненный цикл стадии

```
pending → planning → awaiting_approval → ready → running → done
                                                         ↘ failed
         ↑                                         ↓
         └───────── revising ←────────────────────┘
```

- `pending` — ещё не запущена
- `planning` — AI строит план
- `awaiting_approval` — план готов, ждёт одобрения (через веб или CLI)
- `ready` — план одобрен, ждёт своей очереди (зависимости)
- `running` — AI реализует план
- `done` / `failed` — завершена
- `revising` — отправлены правки, AI переделывает план

## Разработка

```bash
make build    # собрать
make test     # тесты
make lint     # линтер
make clean    # удалить артефакты
```
