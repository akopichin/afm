# AFM Development Guide

## Working directory: `.afm` and `--dir`

By default afm stores runs, flows, and config under `.afm/` in the working directory. The parent directory is resolved in `PersistentPreRunE` (`cmd/afm/main.go`) with priority **flag > env > `.`**: the `--dir` persistent flag, else the `AFM_DIR` env variable, else the current directory. All subcommands read the effective `.afm` path via `fmDir()` (`filepath.Join(rootDir, ".afm")`); `state.FindLatestRunDir(base, flowName)` takes the runs base as an explicit argument instead of hardcoding the path.

## State persistence & run lifecycle (reliability core)

Событийный лог `.afm/runs/<run_id>/events.jsonl` — **единственный доверенный источник правды**. `state.json` — производный кэш (несёт `last_seq`), пути чтения (`afm check`, поиск run) читают состояние из лога через `state.LoadRunState` (без flock), не доверяя снапшоту.

- **flock между процессами.** `state.Open` берёт эксклюзивный `flock` на `<runDir>/.lock` на всё время жизни `Store`. Живой `afm run` держит его; CLI `approve`/`retry`/`revise` при активном run падают с `state.ErrRunLocked` и понятным сообщением. flock освобождается ОС при завершении процесса — упавший run не оставляет «залипшей» блокировки. `afm check` (read-only, без flock) живым run не блокируется.
- **Недеструктивный replay.** Оборванный хвост (последняя запись без `\n`, crash при append) безопасно усекается. Битая **полная** строка в середине лога (валидные записи после неё) → карантин в `events.jsonl.corrupt-<ts>` + `state.ErrCorruptLog`, оригинал НЕ трогается (никогда не усекаем разрушительно).
- **Долговечный snapshot.** `writeSnapshot` делает `f.Sync()` перед Close и fsync родительской директории после Rename. Ошибка записи снапшота нефатальна (это кэш), но read-пути всё равно берут состояние из лога.
- **Уникальный run-id** — `<flow>-<timestamp>-<rand4hex>` (нет коллизий в одну секунду). `state.FindLatestRunDir` якорит префикс (после `<flow>-` обязана быть цифра — `foo` не матчит `foo-bar`); `state.FindLatestRunForStage` — единая точка поиска run по стадии из лога.
- **Storage-fatal завершает run.** `Trigger` через `errors.As(*StorageError)` различает реальный сбой записи лога (→ `setFatal` + отмена run-ctx → `Run` возвращает ошибку) от доброкачественного `ErrConcurrentChange` (CAS-mismatch, тихий no-op) и `ErrNoRule` (log-and-drop, не валит run).
- **Чистый shutdown.** Все агентские горутины запускаются через `spawnAgent` (семафор + маркер активности + `agentWG`). На выходе `Run`: `cancel()` (LIFO — раньше) → `waitAgents()` (bounded 10s) → потом `store.Close()`. Завершения агентов публикуются под run-ctx (не `context.Background()`) — не блокируются навсегда на мёртвой шине.
- **Долговечный approve/revise/retry.** `Approve`/`Revise`/`Retry` синхронны: durable-переход фиксируется в логе ДО возврата (краш не теряет интент — recovery резюмит `ready`/`revising`/`running`). Headless auto-approve обрабатывается inline (нет блокирующего self-publish в event-loop). **Важно:** HTTP-инициированные approve/revise/retry спавнят агента под **run-scoped ctx** (`runContext`), а не под `r.Context()` — иначе net/http отменяет ctx при возврате хэндлера и агент убивается мгновенно. Спавны, достижимые и из HTTP-горутины, и из event-loop, guard'ятся по CAS-результату `Trigger` (нет двойного запуска).
- **`startReadyStages` чтит `autonomous.flag`.** CAS-guard на `EvStartRun` предотвращает только повторный запуск ОДНОГО И ТОГО ЖЕ агента — но не гарантирует, что выигравший гонку код знает, какой агент запускать. `retryStage` для autonomous-стадии переводит её `Pending → Ready` через `EvReady`, а затем сам берёт `EvStartRun` — в узком окне между этими двумя переходами конкурентный вызов `startReadyStages` из другой ветки event-loop (например, `onAgentCompleted` другой стадии) мог выиграть CAS первым и слепо запустить `runImplementationAgent` (он читает `plan.md`, которого у autonomous-стадии нет → падение "no such file or directory"). Поэтому `startReadyStages` перед спавном проверяет `isAutonomousStage` и для таких стадий запускает `runAutonomousAgent` — симметрично уже существующим проверкам в `recovery.go` и в самом `retryStage`.
- **Жёсткий автономный трек: `agents: [auto]`.** В YAML стадии можно статически задать автономный трек — `agents: [auto]` (тип `flow.AgentAuto`, детект `Stage.IsAuto()`). Такая стадия идёт `runAutonomousAgent` НАПРЯМУЮ, без вызова `DetermineStagePhases` (без LLM-супервизора и без фолбэка). Активация — общий хелпер `activateAutoStage` (пишет `autonomous.flag` + `EvReady`, БЕЗ `plan.md`), вызываемый из ОБОИХ путей активации no-planning-стадии: `tryActivatePrePlanned` (scheduling.go) и `startPlanningForPending` (recovery.go) — иначе на fresh/zero-dep запуске recovery-ветка попыталась бы скопировать несуществующий `plan.md`. `startReadyStages` дополнительно чтит `stage.IsAuto()` (страховка, если флаг не записался). Короткое замыкание в `flow.HasAgent`/`ImplAgent` не даёт трактовать `auto` как custom-implementation-агента. Валидация `ParseFile`: `auto` — единственный агент; `auto`+`supervisor:true` → ошибка. Спек/план: `docs/superpowers/specs/2026-07-21-auto-phase-design.md`.

## File-Based Dialog Protocol (Interactive Stages)

The interactive dialog system was refactored from an MCP HTTP server to a file-based protocol starting with the planning-depends-on-ref branch. This enables agents to ask users questions and receive answers through simple file I/O instead of HTTP.

### Architecture

**Agent writes question:**
- Agent writes: `$AFM_STAGE_DIR/<phase>.<id>.question.json`
- Example: `planning.q1.question.json`
- Format: `{"id":"q1", "question":"...", "options":[...], "allow_custom":true/false}`

**Agent polls for answer:**
- Bash loop in agent script: `while [ ! -f "$AFM_STAGE_DIR/<phase>.<id>.answer.json" ]; do sleep 30; done`
- When file appears, agent reads it and continues
- Format: `{"id":"q1", "answer":"...", "from_options":true/false}`

**Orchestrator polls for questions:**
- `startQuestionPoller()` launches goroutine that scans every 1 second
- Detects `*.question.json` files in stage directories
- Publishes `EventAskUser` to transition stage to `awaiting_user_input`
- UI dashboard polls `/api/stages/<id>/dialog` to fetch questions

**HTTP handler processes answer:**
1. Validates phase is one of: `planning`, `implementation`, `review`
2. Validates ID is safe filename component (no path traversal)
3. Checks question.json exists (else 404)
4. Rejects if answer.json already exists (409 Conflict)
5. **Atomically writes** answer.json (O_EXCL exclusive create) — critical path
6. Appends to dialog.jsonl for UI history (best-effort, non-critical)
7. Calls `NotifyAnswer()` to either transition FSM (if agent active) or restart agent (if exited)

### Key Files

| File | Responsibility |
|------|-----------------|
| `pkg/mcp/dialog.go` | `FindUnansweredQuestions()`, `QuestionFile` type, `appendLine()` for dialog.jsonl |
| `pkg/orchestrator/orchestrator.go` | `startQuestionPoller()`, `NotifyAnswer()`, `pollQuestions()`, active agent tracking |
| `pkg/executor/executor.go` | Passes `AFM_STAGE_DIR` environment variable to agent process |
| `pkg/server/handlers.go` | `handleDialogAnswer()` with atomic write pattern (O_EXCL) |
| `pkg/prompts/builder.go` | Interactive rules instruction in system prompt |

### Implementation Details

**Agent Activity Tracking**
- `Orchestrator.activeAgents` is a `sync.Map` tracking which stages have running agent goroutines
- Goroutine acquires semaphore → calls `markAgentActive(stageID)` → deferred `markAgentDone(stageID)`
- If user answers while agent is active: `NotifyAnswer()` transitions FSM, agent bash loop detects file
- If user answers after agent exited: `NotifyAnswer()` publishes to critical bus → `onUserAnswered()` restarts agent

**Question File Naming**
- Format: `<phase>.<id>.question.json`
- Phase must be: `planning`, `implementation`, or `review`
- ID must pass `isValidDialogID()` check (safe filename, no path traversal)
- Enforces: alphanumeric + underscore only

**Answer Delivery Guarantee**
- Answer.json is written atomically (O_EXCL exclusive create) BEFORE FSM transition
- Agent bash loop will always find the file if still running
- If agent already exited, restart with `--resume` flag to re-read answer

**Dialog History**
- `<phase>.dialog.jsonl` appended for UI (best-effort, NOT critical)
- Stores: `{"timestamp":"...", "phase":"...", "role":"assistant|user", "message":"..."}`
- If append fails, agent continues anyway (answer.json already safe on disk)

### Deleted Components

- `pkg/mcp/server.go` — MCP HTTP server (replaced by polling)
- `pkg/mcp/server_test.go` — MCP server tests
- `pkg/orchestrator/mcp_notifier.go` — MCP event notifier

### Environment Variables

| Variable | Purpose | Set By |
|----------|---------|--------|
| `AFM_STAGE_DIR` | Stage directory for question/answer files | `executor.New()` when `StageDir` configured |
| `AFM_DIR` | Parent directory for `.afm` (used when `--dir` is not set) | CLI flag `--dir` / env, resolved in `PersistentPreRunE` |

### Testing File-Based Dialog Locally

Mock agents must implement their own polling:
```bash
while [ ! -f "$AFM_STAGE_DIR/<phase>.q1.answer.json" ]; do 
  sleep 0.5
done
answer=$(cat "$AFM_STAGE_DIR/<phase>.q1.answer.json" | jq -r '.answer')
```

Write question.json with proper schema:
```json
{
  "id": "q1",
  "question": "What should we do?",
  "options": ["Option A", "Option B"],
  "allow_custom": true
}
```

### Debugging Interactive Stages

Check stage directory for dialog files:
```bash
ls -la .afm/runs/<run_id>/<stage_id>/
# Look for: planning.q1.question.json, planning.q1.answer.json, planning.dialog.jsonl
```

Common patterns:
- **Agent waiting:** `*.question.json` exists, but no corresponding `*.answer.json` yet
- **Answer received:** Both `*.question.json` and `*.answer.json` exist, agent should have exited
- **Dialog history:** Check `*.dialog.jsonl` for full Q&A history (safe to ignore if missing)
- **Agent error / hung:** Agent stdout (tool actions) is in `<phase>.log`; agent **stderr** (claude diagnostics, e.g. `stream-json requires --verbose`) is in `<phase>.stderr.log`. The bash polling loop times out after the executor's idle timeout (30 min default).
- **Misplaced question (auto-relocate/normalize):** `relocateMisplacedQuestions` (`pkg/orchestrator/orchestrator.go`, читает Write-события из `<phase>.jsonl`) чинит два способа «спрятать» файл вопроса от поллера, оба ведут к вечному зависанию стадии: **(1) неверная директория** — `*.question.json` записан ВНЕ `$AFM_STAGE_DIR` (баг GLM-4.7: путь из CWD вместо env); **(2) неверный префикс** — файл внутри stageDir, но назван по id стадии (напр. `commit-changes.q1.question.json`) вместо канонической фазы (`planning.q1.question.json`), а `FindUnansweredQuestions` матчит только `planning`/`implementation`/`review`/`autonomous_execution`. В обоих случаях файл нормализуется к каноническому имени `<phase>.<id>.question.json` (правильная фаза берётся из того, в чьём `<phase>.jsonl` найден Write, а не из неверного префикса) + создаётся dangling-симлинк по пути, который опрашивает агент (его директория + его префикс), → канонический `<stageDir>/<phase>.<id>.answer.json`, чтобы bash-polling-loop нашёл ответ. Стадия уходит в `awaiting_user_input`, а не зависает. Первый слой защиты — сам промпт (`pkg/prompts/builder.go`, `<interactive_rules>`) с адресным constraint-ом «префикс — это фаза, а НЕ id стадии». (Прежнее поведение — fail-fast через `detectDialogViolation` — заменено relocate.) На ручном retry `<phase>.session.json` и `<phase>.jsonl` очищаются.

### Polling Latency

- Orchestrator polls every **1 second** for new questions
- Answer detection is immediate (bash loop checks file existence continuously)
- UI dashboard polls `/api/stages/<id>/dialog` every ~2 seconds
- Total latency: question visible in UI within ~2-3 seconds of agent writing it

### Common Changes

When adding new interactive features:
1. Ensure agent writes `<phase>.<id>.question.json` in correct format
2. Ensure agent polls correctly: `while [ ! -f "$AFM_STAGE_DIR/<phase>.<id>.answer.json" ]; do sleep 30; done`
3. Update handler validation in `pkg/server/handlers.go` if phase names change
4. Add integration tests. Note: interactive stages (`stage.Interactive=true`) **ignore** the injected `Runner` — `runnerFor` always builds a real `executor.New(...)` driven by `stage.Command`. So interactive tests run a real bash script via `stage.Command` (see `TestFullDialogCycle`, `TestIntegration_MisplacedQuestionRelocated`), not `eagerProbeRunner` (which only applies to non-interactive stages)
5. Verify atomic write pattern (O_EXCL) is preserved in handlers

## Docker Mode

afm умеет автоматически перезапускать себя внутри Docker при включённом Docker-режиме.

### Включение

Через конфиг (`.afm/config.yaml` или `~/.afm/config.yaml`):
```yaml
docker:
  enabled: true
  image: akopichin/afm:latest   # опционально, это дефолт
```

Или через переменную окружения:
```bash
AFM_USE_DOCKER=1 afm run flow.yaml
```

### Что монтируется автоматически

| Хост | Контейнер | Назначение |
|------|-----------|------------|
| `$(pwd)` (абсолютный путь) | тот же путь | Проект + `.afm/` (runs, flows, config) |
| `~/.claude/` | `/home/afm/.claude` | Auth, skills, память (= `$HOME/.claude` в контейнере) |
| `~/.afm/` | `/home/afm/.afm` | Глобальный конфиг afm |
| Нестандартные агенты из flow | `/usr/local/bin/<cmd>` (`:ro`) | Кастомные команды |
| `docker.extra_mounts` | `~`-пути → `/home/afm/…`, прочие — тот же путь (`:ro`) | Токены/конфиги кастомных агентов (напр. `~/.ai-free`) |

`~/.claude.json` намеренно **НЕ** монтируется — claude создаёт свежий container-local конфиг (`/home/afm/.claude.json`). Попытка примонтировать его `:ro` приводила к падению (`corrupted: JSON Parse error`), т.к. claude обновляет файл атомарным rename.

**Auth для `command: claude` в Docker:** macOS хранит OAuth-токены в Keychain (`Claude Safe Storage`), который недоступен из Linux-контейнера. Поэтому `claude` внутри Docker пишет `not logged in`. Решение — передать токен через env var:
1. Сгенерировать долгоживущий токен: `claude setup-token` → сохранить в `~/.zshrc` как `export CLAUDE_CODE_OAUTH_TOKEN=sk-ant-si-...`
2. Launcher автоматически прокинет `CLAUDE_CODE_OAUTH_TOKEN` в контейнер (если задан в env).
   Также поддерживается `ANTHROPIC_API_KEY` (API-ключ) и `ANTHROPIC_AUTH_TOKEN`.

**Dashboard:** порт из `server.port` пробрасывается на хост через `-p <port>:<port>`, иначе UI недоступен снаружи контейнера. **Браузер:** по умолчанию (`server.open_browser` отсутствует/`false`) НЕ открывается — в лог печатается URL дашборда с подсказкой `→ open this URL in your browser to follow the run`. При `server.open_browser: true` браузер открывает хост-side opener: afm внутри Linux-контейнера сам открыть браузер на macOS-хосте не может (`runtime.GOOS=linux` → `xdg-open` без display), поэтому отдельный процесс-помощник запускается на хосте ДО re-exec, опрашивает проброшенный порт и зовёт `open`/`xdg-open`. Внутри контейнера вызов `openBrowser` пропускается (`AFM_IN_DOCKER=1`).

**Привилегии (важно):** контейнер стартует под root, но entrypoint (`docker-entrypoint.sh` + `gosu`) сразу дропает привилегии до хостового uid/gid (`AFM_HOST_UID/GID`, передаются из `os.Getuid/Getgid`) и выставляет `HOME=/home/afm`. Поэтому afm и агенты работают под тем же пользователем, что и на хосте — все записи в `~/.claude`, `~/.afm`, каталог проекта и `extra_mounts` принадлежат пользователю хоста, а не root (нет root-owned файлов и конфликтов с правами у хостового claude). Под non-root claude разрешает `--dangerously-skip-permissions` без `IS_SANDBOX`.

### Environment Variables

| Переменная | Назначение |
|-----------|------------|
| `AFM_USE_DOCKER=1` | Включить Docker mode без правки конфига |
| `AFM_IN_DOCKER=1` | Выставляется внутри контейнера — предотвращает рекурсию (не трогать) |
| `AFM_HOST_UID` / `AFM_HOST_GID` | Передаются внутрь; entrypoint дропает root до этого uid/gid (`gosu`), чтобы записи в тома принадлежали пользователю хоста |
| `AFM_DOCKER_IMAGE` | Переопределить образ (например, для локальной сборки) |
| `ANTHROPIC_API_KEY` | Пробрасывается в bare-форме `-e KEY` (без значения — не светится в `ps aux`/history) |
| `ANTHROPIC_AUTH_TOKEN` | То же самое |
| `ANTHROPIC_BASE_URL` | То же самое |
| `CLAUDE_CODE_OAUTH_TOKEN` | Долгоживущий OAuth-токен для `command: claude` (генерируется через `claude setup-token`) |

### Публикация нового образа

Версионированный релиз (SemVer, авто-бамп) — пушит иммутабельный `akopichin/afm:vX.Y.Z` и rolling `:latest`:

```bash
make release-patch   # v1.2.3 → v1.2.4  (bugfix)
make release-minor   # v1.2.3 → v1.3.0  (новая фича, обратная совместимость)
make release-major   # v1.2.3 → v2.0.0  (breaking change)
```

`scripts/release.sh` читает последний SemVer git-тег, бампит уровень, собирает имидж с двумя тегами и пушит оба; после успешного пуша образа создаёт локальный git-тег **и сразу пушит его в remote** (`git push origin vX.Y.Z`). Сбой пуша тега (нет сети/auth) не валит релиз — образ уже опубликован, тег пушится вручную. Версия вшита в бинарник: `docker run akopichin/afm:vX.Y.Z afm --version` покажет тег.

**Релиз всегда мультиарх (`linux/amd64` + `linux/arm64`).** `release.sh` собирает и пушит через `docker buildx build --platform linux/amd64,linux/arm64 --push` одним шагом (раздельный `docker push` для манифест-листа не годится — образы не грузятся в локальный daemon). Обязателен билдер с драйвером `docker-container` (драйвер `docker` не умеет манифест-листы) — скрипт создаёт именованный `afm-multiarch` идемпотентно, если его нет. amd64-ветка на arm64-хосте собирается через QEMU-эмуляцию (медленно, но корректно). **Зачем:** обычный `docker build` на Mac (arm64) даёт single-arch образ → у тех, кто делает `FROM akopichin/afm` на amd64, сборка падает `no match for platform in manifest: not found`.

`make docker-push` — dev-only, пушит только `:latest` **single-arch** (быстрая итерация без релиза, без эмуляции). Для раздачи вовне всегда используй `make release-*` (мультиарх) и тег `:vX.Y.Z`.

### Отладка

```bash
# Посмотреть что именно будет запущено
AFM_USE_DOCKER=1 AFM_DOCKER_IMAGE=local/afm:dev afm run flow.yaml

# Войти в контейнер вручную (привилегии дропаются до твоего uid через entrypoint)
docker run --rm -it \
  -v $(pwd):/project \
  -v ~/.claude:/home/afm/.claude \
  -v ~/.afm:/home/afm/.afm \
  -e AFM_HOST_UID=$(id -u) -e AFM_HOST_GID=$(id -g) \
  akopichin/afm:latest bash
```

### Нестандартные агенты (не claude)

Если в flow прописан `command: glm51` (или другой не-claude бинарник), afm автоматически:
1. Находит бинарник через `which glm51`
2. Монтирует его в контейнер: `-v /path/to/glm51:/usr/local/bin/glm51:ro`

Бинарники, не найденные в PATH на хосте, молча пропускаются.

Ограничения:
- В контейнер монтируется только сам файл бинарника/скрипта агента (`:ro`). Если скрипт-обёртка вызывает сторонние зависимости (node/python/скрипты-сиблинги/файлы вроде `~/.glmrc`), они не перенесутся — используйте агентов, чьи зависимости уже есть в образе.
- `command` в flow должен быть именем из `PATH` (базовым именем), а не абсолютным путём: монтируется только `filepath.Base(cmd)`, и внутри контейнера искался бы абсолютный путь хоста.
- Если скрипт-агент читает свои токены/конфиги из дома (напр. GLM-обёртки `glm51`/`glm52`/`ai-free.claude-glm` — из `~/.ai-free/claude-glm/`), добавьте эту директорию в `docker.extra_mounts`, иначе агент упадёт с "файл не найден".

### autoShim: генерируемые врапперы без монтирования

По `docker.autoShim: true` afm генерирует claude-совместимые врапперы для агентов,
описанных в `docker.agents.<cmd>` (recipe: `model`/`url`/`system_prompt`/`auth`),
прямо в контейнере — без `-v` монтирования хост-бинарника и без `extra_mounts`
для токенов. Секрет и контент system_prompt читаются на хосте и передаются в
контейнер как transient env (`AFM_SECRET_<CMD>`, `AFM_SYSPROMPT_<CMD>`); `url`/`model`
контейнер берёт из смонтированного `config.yaml`.

```yaml
docker:
  autoShim: true
  agents:
    glm51:
      model: glm-5.1
      url: https://api.z.ai/api/anthropic
      auth: { from: "file:~/.ai-free/claude-glm/token", to: "env:ANTHROPIC_AUTH_TOKEN" }
```

- `auth.to` ∈ {`env:CLAUDE_CODE_OAUTH_TOKEN`, `env:ANTHROPIC_API_KEY`, `env:ANTHROPIC_AUTH_TOKEN`}.
- Без recipe (при `autoShim: true`) команда монтируется `:ro` как раньше.
- `url` bake'ится в враппер как `ANTHROPIC_BASE_URL` (z.ai, deepseek — напрямую, без прокси).
- См. спек `docs/superpowers/specs/2026-07-14-docker-autoshim-design.md`.

#### Тип `openai`: OpenAI-совместимые провайдеры

Для провайдеров с **настоящим** API, совместимым с OpenAI (`v1/chat/completions`), укажи `type: openai`.
Сгенерированный враппер использует `/usr/local/bin/openai-as-claude` вместо claude:

```yaml
docker:
  autoShim: true
  agents:
    deepseek:
      type: openai
      model: deepseek-chat
      url: https://api.deepseek.com/v1
      auth:
        from: env:DEEPSEEK_KEY        # секрет на хосте
        to: env:OPENAI_API_KEY        # не ограничен ClaudeAuthEnvVars
```

Поддерживаемые провайдеры: DeepSeek (`api.deepseek.com`), OpenAI, локальные Ollama/любые
эндпоинты с `POST /v1/chat/completions`. **Важно:** Cursor сюда НЕ относится — см. ниже `type: cursor`.

Требования в образе: `jq`, `curl` (оба присутствуют в `Dockerfile.runtime`).

#### Тип `cursor`: Cursor Cloud Agents API

Cursor Cloud API (`api.cursor.com`) **не имеет** синхронного `v1/chat/completions` (ответ 404) —
это **Cloud Agents API**: асинхронный run-based API, где чат = запуск облачного код-агента.
Поэтому для Cursor используется отдельный тип и адаптер `cursor-as-claude`:

```yaml
docker:
  autoShim: true
  agents:
    cursor:
      type: cursor
      model: auto                    # auto/пусто → Cursor default; иначе model.id из GET /v1/models
      url: https://api.cursor.com/v1
      auth:
        from: "file:~/.ai-free/claude-glm/token-cursor"   # секрет на хосте (CRSR_…)
        to: env:CURSOR_API_KEY         # любой env:VAR; CURSOR_API_KEY по конвенции
```

Адаптер `cursor-as-claude`: создаёт no-repo Cloud Agent (`POST /v1/agents`, `mode:"agent"`),
опрашивает run до терминального статуса, эмитит claude stream-json с `result`-текстом и
архивирует агента (чтобы не плодить мусор). `system_prompt` для cursor **не используется**
(адаптер его не передаёт).

Особенность: первый ответ ~30–90с (старт cloud-VM при создании агента); далее run быстрый.
Токен — user API key из Cursor Dashboard → API Keys (префикс `crsr_`). Требования в образе: `jq`, `curl`.

### Известные грабли (Docker-mode)

- **gosu сбрасывает HOME для uid без записи в `/etc/passwd`** → ставит `HOME=/`. Поэтому в `docker-entrypoint.sh` HOME задаётся **после** gosu (`gosu uid:gid env HOME=/home/afm afm …`), а не до. Иначе агенты ищут `~/`-файлы в `/` (баг: токен искался в `//.ai-free/…`).
- **`:ro` single-file bind-mount + атомарный rename = corruption.** Приложения, переписывающие конфиг через temp+rename (claude и `~/.claude.json`), не могут обновить `:ro`-маунт и квартитят его как corrupted. Не монтируй `:ro` то, что приложение пишет — пусть создаст свежий container-local файл.
- **`os.ModeCharDevice` ≢ TTY.** `/dev/null` — тоже char device, поэтому эвристика `Stdin.Stat().Mode()&ModeCharDevice` ложно добавляла `-it` в не-TTY → `docker run` падал "the input device is not a TTY". Честная проверка — `golang.org/x/term.IsTerminal`.
