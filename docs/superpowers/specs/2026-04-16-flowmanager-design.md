# flowManager — Design Spec

**Date:** 2026-04-16  
**Status:** Draft

---

## Overview

flowManager — Go-бинарник + набор Claude-скиллов для оркестрации многоступенчатых AI-флоу. Аналог ralphex, но вместо линейного плана — YAML flow-файл со stages, зависимостями и параллельным выполнением.

**Ключевые цели:**
- Декларативное описание многошагового AI-флоу в YAML
- Автоматическое планирование каждого stage отдельным агентом с аппрувалом пользователя
- Параллельное выполнение stage где нет зависимостей
- Возобновляемость: лог + стейт в файлах, рестарт с места прерывания
- Файловые локи — один агент на одну задачу
- Дистрибуция: Homebrew/GitHub Releases + Claude plugin marketplace

---

## Architecture

**Подход:** Go binary (orchestrator + executor) + внешние prompt-шаблоны (`go:embed`) + тонкие Claude-скиллы (launchers).

```
cmd/flowmanager/          ← точка входа, CLI
pkg/
  flow/                   ← парсинг YAML flow-файлов
  orchestrator/           ← граф зависимостей, параллельный запуск stages
  executor/               ← спавн claude CLI, парсинг stream-json
  state/                  ← стейт-машина flow/stage, atomic JSON-обновления
  progress/               ← лог-файлы + flock (Unix/Windows)
assets/
  prompts/
    planning.md           ← промпт для planning-агента
    implementation.md     ← промпт для implementation-агента
    review.md             ← промпт для review-агента
    summary.md            ← промпт финального отчёта
  claude/skills/
    flowmanager/SKILL.md          ← /flowmanager (launcher)
    flowmanager-check/SKILL.md    ← /check flow (статус)
    flowmanager-init/SKILL.md     ← /flowmanager init (создать flow.yaml)
```

Промпты встроены через `go:embed`, но могут быть переопределены пользователем через `~/.flowmanager/prompts/`.

---

## Flow File Format (YAML)

```yaml
name: my-feature
description: "Краткое описание всего flow"

stages:
  - id: backend
    name: "Backend"
    description: "Детальное описание что нужно сделать"
    agents:
      - planning         # сгенерировать plan.md
      - implementation   # выполнить plan.md
      - review           # ревью изменений
    skills:              # дополнительные Claude-скиллы, передаются агентам
      - superpowers:tdd
    plan: null           # null → авто-генерация; путь → использовать готовый

  - id: frontend
    name: "Frontend"
    description: "..."
    depends_on: [backend]   # запуск только после завершения backend
    agents:
      - planning
      - implementation
      - review
    skills:
      - frontend-design

  - id: e2e
    name: "E2E Tests"
    description: "..."
    depends_on: [backend, frontend]
    agents:
      - planning
      - implementation
    plan: docs/plans/existing-plan.md   # готовый план, фаза планирования пропускается
```

**Правила:**
- `depends_on` → направленный граф зависимостей; циклы — ошибка при валидации
- `plan: <path>` → stage сразу переходит в `ready`, минуя `planning`/`awaiting_approval`; если при этом в `agents` указан `planning` — он игнорируется
- `agents` — порядок важен: planning → implementation → review; каждый элемент опционален
- Если `agents` не содержит `planning` и `plan` не задан — ошибка валидации

---

## State Machine

Стейт каждого stage:

```
pending → planning → awaiting_approval → ready → running → done
                                                          ↘ failed
```

- `pending` — ждёт depends_on или не запущен
- `planning` — planning-агент активен прямо сейчас
- `awaiting_approval` — plan.md готов, ждём подтверждения пользователя
- `ready` — план утверждён (или был готовый), готов к implementation
- `running` — implementation-агент активен
- `done` — всё завершено успешно
- `failed` — агент вернул ошибку или был прерван без завершения

Стейт flow:

```
planning_phase → implementation_phase → review_phase → completed | failed
```

---

## Directory Layout

```
.flowManager/
  flows/                            ← дефолтное место для flow YAML (опционально)
  runs/
    {flow-name}-{timestamp}/
      state.json                    ← стейт всего flow + каждого stage
      {stage-id}/
        plan.md                     ← план (сгенерированный или скопированный)
        plan.md.lock                ← flock на период работы агента
        planning.log                ← лог planning-агента (stream-json)
        implementation.log          ← лог implementation-агента
        review.log                  ← лог review-агента
        stage.json                  ← индивидуальный стейт stage
```

`state.json` обновляется атомарно (write-to-temp + rename). Лог-файлы пишутся append-only. При рестарте: `done`-stages пропускаются, `running`-stages перезапускаются (лог сохраняется с сепаратором `--- restarted at ... ---`).

---

## Orchestrator Logic

### Фаза планирования

1. Загрузить/создать run-директорию
2. Построить граф зависимостей
3. Для каждого stage без готового `plan`:
   - Параллельно (goroutines) запустить planning-агентов
   - Каждый: взять flock на `plan.md.lock`, спавнить `claude` с planning-промптом
   - Результат записывается в `plan.md` + `planning.log`
   - Стейт → `awaiting_approval`
4. Показать пользователю список сгенерированных планов
5. Пользователь просматривает/редактирует каждый plan.md → подтверждает
6. Стейт всех утверждённых stages → `ready`

### Фаза имплементации

1. Worker pool (по умолчанию неограниченный — все stages без нарушенных зависимостей запускаются одновременно; ограничение через `--max-parallel`)
2. Воркер берёт следующий `ready` stage, у которого все `depends_on` в статусе `done`
3. Берёт flock, спавнит implementation-агент с планом
4. По завершении → если есть `review` агент → запустить review-агент
5. Стейт → `done`; разблокировать зависимые stages → они переходят в очередь

### Фаза финального ревью

После всех stages: запустить summary-агент, который читает все `review.log` и `implementation.log`, пишет итоговый отчёт пользователю.

---

## Executor (spawning Claude)

Аналогично ralphex:
- Команда для запуска AI-клиента берётся из конфига (`client.command`, дефолт: `claude`); можно заменить на любой совместимый CLI
- `{command} --print --output-format stream-json --dangerously-skip-permissions`
- Промпт передаётся через stdin (избегаем лимит командной строки)
- Из environment удаляются `CLAUDECODE` и `ANTHROPIC_API_KEY` перед спавном дочернего процесса
- Process group для корректного kill всего поддерева
- Парсинг stream-json для progress-репортинга в лог
- Idle timeout — kill сессии после 30 минут без активности (настраивается через `--idle-timeout`)

---

## Configuration

flowManager использует двухуровневую конфигурацию (мерж с приоритетом более локального):

| Уровень | Путь | Применяется к |
|---------|------|---------------|
| Глобальный | `~/.flowManager/config.yaml` | Всем проектам |
| Проектный | `.flowManager/config.yaml` | Текущей директории/репозиторию |

Ключи проектного конфига перекрывают одноимённые ключи глобального (shallow merge по ключам верхнего уровня).

### Формат config.yaml

```yaml
client:
  command: claude          # команда для запуска AI-клиента (дефолт: claude)
  # command: gemini        # пример: переключиться на другой совместимый CLI
  extra_args: []           # дополнительные аргументы к каждому вызову

executor:
  idle_timeout: 30m        # kill агента если нет активности (дефолт: 30m)
  max_parallel: 0          # макс. параллельных stages; 0 = без ограничений

prompts_dir: ""            # путь к директории с промптами (пусто = встроенные go:embed)
```

### Порядок приоритетов

1. `~/.flowManager/config.yaml` — глобальные дефолты
2. `.flowManager/config.yaml` — перекрывает глобальные
3. CLI-флаги (`--idle-timeout`, `--max-parallel`) — перекрывают конфиг

---

## CLI Commands

```bash
flowmanager run [flow.yaml]       # запустить/продолжить flow
flowmanager check                 # статус последнего/текущего flow (таблица stages)
flowmanager approve [stage-id]    # подтвердить план stage (вызывается из скилла)
flowmanager init                  # интерактивное создание flow.yaml
flowmanager list                  # список flow-файлов в .flowManager/flows/
flowmanager resume [run-id]       # явно возобновить конкретный run
```

---

## Claude Skills (thin launchers)

### `/flowmanager`

1. Проверить `which flowmanager`; если нет — подсказать установку
2. `flowmanager list` → предложить flow через AskUserQuestion
3. `flowmanager run {flow}` в фоне (`run_in_background: true`)
4. Мониторить `state.json` через polling (`tail -f` или периодический read); при появлении stage в статусе `awaiting_approval` — прочитать `plan.md`, показать пользователю, дождаться аппрувала через AskUserQuestion, затем вызвать `flowmanager approve {stage-id}`
5. Вызвать `flowmanager check` после завершения

### `/check flow`

Запустить `flowmanager check`, отобразить таблицу стейджей и их статусов. STOP после отчёта.

### `/flowmanager init`

Запустить `flowmanager init`, мониторить интерактивный вывод, помочь пользователю заполнить flow YAML.

---

## File Locking

- Unix: `flock(2)` эксклюзивный лок на `*.lock`-файл
- Windows: `LockFileEx`
- Лок берётся до начала работы агента, освобождается при закрытии (defer)
- Lock registry предотвращает double-lock внутри одного процесса
- При crash: OS освобождает flock автоматически → безопасное возобновление

---

## Resumption

При рестарте `flowmanager run`:
1. Найти последний run с этим flow-именем в `.flowManager/runs/`
2. Прочитать `state.json`
3. `done` stages — пропустить
4. `running` stages с живым lock-файлом — значит агент всё ещё работает, подключиться к логу
5. `running` без lock — агент упал; перезапустить, лог сохранить с `--- restarted ---`
6. `awaiting_approval` — снова показать план пользователю

---

## Distribution

- **GitHub Releases + goreleaser:** бинарники для darwin/linux/windows amd64/arm64
- **Homebrew tap:** `brew install <owner>/tap/flowmanager` (конкретное имя определяется при создании репозитория)
- **Claude plugin marketplace:** пакет с `assets/claude/skills/` + манифест плагина
  - Скиллы регистрируются как `/flowmanager`, `/check flow`, `/flowmanager init`
  - Бинарник отдельно — скилл подсказывает установку если нет

---

## Out of Scope (v1)

- Web dashboard (как у ralphex)
- Условные переходы между stages (if/else в YAML)
- Кастомные агенты (не встроенные)
- Интеграция с git (ветки per-stage)
- Retry policy для отдельных агентов
