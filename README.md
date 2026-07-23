# afm

A CLI tool for orchestrating multi-stage AI tasks. Describe the task in a YAML file, break it into stages — afm runs AI agents sequentially or in parallel, waits for your approval of plans, and automatically carries out the implementation. Works with `claude` and with any claude-compatible agents (GLM, DeepSeek, Cursor, etc.).

## How It Works

Each stage goes through phases by default:

```
1. Planning   — AI builds a stage plan → you review and approve (or revise)
2. Execution  — AI implements the approved plan (+ optional code review)
```

Stages can run in parallel; dependencies via `depends_on` guarantee the correct order. Plans and artifacts of dependent stages are automatically substituted into the prompt.

**Autonomous track (optional).** If a supervisor is enabled for a stage, an agent-supervisor (LLM) decides for itself whether the full cycle is needed. For simple stages it collapses planning/implementation/review into a single `autonomous_execution` step — an agent with skills does the work right away and writes `execution_summary.md`, without a plan and without approval. On any LLM error, there's a safe fallback to the regular phases. The autonomous track can also be forced without a supervisor — `agents: [auto]`. See [Supervisor and Autonomous Track](#supervisor-and-autonomous-track).

**Reliability.** The state of every run is written to an event log `.afm/runs/<run>/events.jsonl` (append + fsync) — this is the single source of truth. If a run is interrupted, `afm run` automatically resumes from the same point: completed stages are skipped, interrupted ones are retried. While `afm run` is active, it holds an exclusive lock on the run directory (`.lock`) — a concurrent `afm approve/retry/revise` from another process can't corrupt the live log.

## Installation

**Via Homebrew (recommended):**
```bash
brew install --cask akopichin/afm
afm install-skills   # optional: /afm, /afm-check, etc. in Claude Code
```

The binary is updated via `brew upgrade --cask afm`; skills don't need to be
reinstalled on update, but you can re-run `afm install-skills` if new ones
have appeared.

**From source:**
```bash
make build        # build into bin/afm
make install      # install via go install
```

**Prebuilt binary + Claude skills:**
```bash
./install.sh
```

The script copies the binary to `/usr/local/bin` and installs skills for Claude Code (`/afm`, `/afm-check`, `/afm-init`, `/afm-retry`, `/afm-review`).

### Running in Docker (without a local install)

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

Or enable automatic Docker mode in the config — then the plain `afm run` command will restart itself inside the container:

```yaml
# .afm/config.yaml
docker:
  enabled: true
```

The image includes: claude CLI, Node 22, Python 3.12, Go 1.26, git. The container starts as root, but the entrypoint (`gosu`) immediately drops privileges to your host uid/gid — files in the mounted volumes belong to you, not root.

#### Authentication in Docker Mode

The Docker container is Linux — it has no access to the macOS Keychain where claude's OAuth sessions are stored. So the token needs to be passed explicitly via an environment variable — afm forwards it into the container automatically.

**Claude Pro/Max (claude.ai subscription)**

```bash
# One-time: generate a long-lived token
claude setup-token

# Add to ~/.zshrc / ~/.bashrc
export CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat01-...
```

**Anthropic API Key**

```bash
export ANTHROPIC_API_KEY=sk-ant-api-...
```

`ANTHROPIC_AUTH_TOKEN` and `ANTHROPIC_BASE_URL` are also supported — all of these are forwarded in bare form (`-e KEY` with no value), so the secret doesn't leak into `ps`/history.

#### Non-Standard Agents in Docker (autoShim)

If a stage uses a non-claude command (`command: glm51`, `command: deepseek`, etc.), Docker offers two options:

- **Mounting:** afm locates the binary via `which` and mounts it into the container (`:ro`). Works if the agent has no external dependencies.
- **autoShim (recommended):** with `docker.autoShim: true`, afm generates a claude-compatible wrapper right inside the container from the `docker.agents.<cmd>` recipe — without mounting the binary and without passing tokens through files. The secret is read on the host and passed in as a transient env var. Supported types are `claude` (default), `openai` (DeepSeek/OpenAI-compatible), and `cursor` (Cursor Cloud Agents API).

```yaml
docker:
  autoShim: true
  agents:
    glm51:
      model: glm-5.1
      url: https://api.z.ai/api/anthropic
      auth: { from: "file:~/.ai-free/claude-glm/token", to: "env:ANTHROPIC_AUTH_TOKEN" }
```

Details and examples are in `config.example.yaml`, `example-flow-cursor.yaml`, and `CLAUDE.md` (Docker Mode section).

## Quick Start

### 1. Create a flow

```bash
afm init
```

Interactively asks questions and creates `.afm/flows/<name>.yaml`. Or write it by hand — see the example below.

### 2. Run

```bash
afm run flow.yaml

# If the flow lives in .afm/flows/ — you can omit the argument:
afm run
```

By default a web dashboard comes up (`http://localhost:9876`); its URL is printed to the log.

### 3. Approve plans

After the planning phase, each stage transitions to `awaiting_approval`. There are two ways to do this:

**Via the web dashboard** — open `http://localhost:9876`, select a stage, review the plan line by line, leave inline comments on specific lines (like in an MR), and click "Approve" or "Send revision".

**Via the CLI:**
```bash
# View the plan
cat .afm/runs/<run-dir>/<stage-id>/plan.md

# Approve
afm approve backend-auth

# Not happy with it — ask for a redo
afm revise backend-auth --feedback "Need to add Redis for the token blacklist"

# Retry a failed stage
afm retry backend-auth
```

> CLI mutations (`approve`/`revise`/`retry`) work when `afm run` is NOT running (headless scenario). While `afm run` is active, approve through the dashboard — otherwise the command will report that the run is locked.

### 4. Follow progress

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

Or in real time via the web dashboard — stages, progress bar, event feed, logs.

## Specifying the working directory

By default `.afm/` is created in the current folder. To move it elsewhere:

```bash
# Flag (one-off run)
afm --dir ~/my-flows run

# Environment variable (persistent)
export AFM_DIR=~/my-flows
afm run
```

All commands (`run`, `check`, `approve`, `revise`, `retry`, `init`, `list`) respect `--dir`. Priority: `--dir` flag > `AFM_DIR` env var > current directory.

## The flow.yaml file

```yaml
name: my-feature
description: "Short task description"
# supervisor_command: glm51    # optional — supervisor agent command for the whole flow

stages:

  - id: backend          # unique stage ID
    name: "Backend API"
    description: |
      What needs to be done — in detail.
      The AI will use this text as guidance during planning and implementation.
    agents: [planning, implementation, review]
    skills:              # optional — Claude skills
      - superpowers:test-driven-development
    command: claude      # optional — custom AI command for this stage
    max_parallel: 2      # optional — parallelism limit for this command
    artifacts:           # files this stage passes on to other stages
      - name: api-contract
        path: docs/api-contract.yaml
        description: "OpenAPI specification"
      - name: db-schema
        path: ./schema.sql        # ./ = relative to the stage directory in the run
        description: "SQL migration"
        inline: false             # pass the path, not the contents

  - id: frontend
    name: "Frontend"
    description: "Implement the UI against the API contract"
    agents: [planning, implementation]
    depends_on: [backend]         # will only start after backend completes
    inputs:                       # artifacts from dependency stages
      - backend.api-contract      # the file's contents will be substituted into the prompt
      - ref: backend.db-schema    # optional — doesn't block if the file is missing
        optional: true

  - id: db-migration
    name: "DB Migration"
    description: "Apply the migration"
    agents: [implementation]
    plan: docs/plans/migration.md   # ready-made plan — the planning agent doesn't run
    verify: "make test"             # gate command: exit != 0 — stage is not marked done
```

**Stage fields:**

| Field | Required | Description |
|------|-------------|----------|
| `id` | yes | Unique identifier (letters/digits/`_`/`-`) |
| `name` | no | Human-readable name for logs and the dashboard (if empty — `id` is shown) |
| `description` | yes | Task description for the AI (background/context) |
| `prompt` | no | Explicit instruction for the agent — a separate `<prompt>` block after the context. Unlike `description`, this is a direct instruction on what to do. It's escaped and cannot inject XML tags |
| `agents` | yes | Combination of `planning`, `implementation`, `review` |
| `depends_on` | no | IDs of stages that must complete first |
| `eager_planning` | no | `true` — planning starts immediately when the flow runs, without waiting for `depends_on` |
| `skills` | no | Claude skills for the agent |
| `plan` | no | Path to a ready-made plan file (skips planning) |
| `command` | no | AI command for this stage (overrides `client.command` from the config) |
| `max_parallel` | no | Limit on parallel stages for this command |
| `interactive` | no | `true` — enables the file-based dialog protocol with the user via the dashboard (see below) |
| `supervisor` | no | `true` — allow the supervisor to evaluate the stage and possibly move it to the autonomous track (requires `supervisor_command`) |
| `supervisor_prompt` | no | Extra context for the supervisor when evaluating this stage |
| `artifacts` | no | Files the stage produces for other stages |
| `inputs` | no | Artifacts from dependency stages (`stage.artifact`) |
| `verify` | no | Shell command run after `.done`. Exit ≠ 0 — the stage is not counted as complete: one retry with the command's output in the prompt, then `failed`. Guards against a false "done" |

**Flow fields (top level):** `name`, `description`, `prompt` (global instruction for all stages), `max_parallel`, `supervisor_command` (supervisor agent command), `root_dir` (project root = agents' working directory, see below), `stages`.

**`root_dir` — the project root for agents.** Sets the working directory (CWD) in which stage agents run:

```yaml
name: my-feature
root_dir: /workspace      # a relative path is resolved from the afm root (--dir); empty — CWD of the afm process
stages: ...
```

By default the agent inherits the CWD of the `afm` process, and `afm` assumes the project root matches the afm root (the parent of `.afm/`). If that's not the case — for example, in a Docker setup where the sources are mounted at `/workspace` but `.afm/` lives in a different directory — relative project paths (`docs/arch/…`, etc.) resolve to different roots for different stages: one stage writes a file, another can't find it. `root_dir` fixes a single root for all stages. Dialog paths (`AFM_STAGE_DIR`) stay anchored to the afm root regardless of `root_dir`.

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

### Жёсткий автономный трек: `agents: [auto]`

Если ты заранее знаешь, что стадия должна идти автономным треком (супервизор не всегда угадывает), укажи `agents: [auto]` — стадия сразу исполняется автономным агентом, **без LLM-решения супервизора и без фолбэка** на обычные фазы. Ведёт себя как supervisor-автономная стадия (нет `plan.md`, нет одобрения, доступен диалог, пишет `execution_summary.md`), только решение статическое — из YAML.

```yaml
stages:
  - id: sync-manifests
    description: "Засинкать CODEMANIFEST-ы с кодом"
    agents: [auto]        # жёстко автономно, без супервизора
```

`auto` должен быть единственным агентом стадии; `auto` + `supervisor: true` — ошибка конфигурации (противоречивые интенты, ловится при парсинге флоу).

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

Один раз после клонирования — включить pre-commit хук (lint + build + test перед каждым коммитом):

```bash
git config core.hooksPath .githooks
```

Хук лежит в `.githooks/pre-commit` и версионируется вместе с репозиторием, но сама настройка
`core.hooksPath` — локальная git-конфигурация, поэтому её нужно применить в каждом клоне отдельно.
Пропустить разово: `git commit --no-verify`.

```bash
make build        # собрать (bin/afm)
make test         # тесты (с -race)
make lint         # линтер
make install      # go install
make install-skills   # установить /afm-* скиллы в ~/.claude
make docker-build     # собрать Docker-образ
make clean        # удалить артефакты
```

Версионированный релиз: `make release-patch` / `release-minor` / `release-major` бампает SemVer-тег и пушит его; сама сборка (docker-образ `:vX.Y.Z` + `:latest`, бинарники, GitHub Release, Homebrew cask) происходит в GitHub Actions (`.github/workflows/release.yml`) по факту пуша тега. На push в `main` patch-версия релизится автоматически — `make release-patch` вручную нужен редко.
