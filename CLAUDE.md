# AFM Development Guide

## Working directory: `.afm` and `--dir`

By default afm stores runs, flows, and config under `.afm/` in the working directory. The parent directory is resolved in `PersistentPreRunE` (`cmd/afm/main.go`) with priority **flag > env > `.`**: the `--dir` persistent flag, else the `AFM_DIR` env variable, else the current directory. All subcommands read the effective `.afm` path via `fmDir()` (`filepath.Join(rootDir, ".afm")`); `state.FindLatestRunDir(base, flowName)` takes the runs base as an explicit argument instead of hardcoding the path.

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
- **Dialog protocol violation:** Stage failed fast with reason `dialog protocol violation` in `events.jsonl` — the interactive agent wrote a `*.question.json` OUTSIDE `$AFM_STAGE_DIR` (detected by `detectDialogViolation` scanning `<phase>.jsonl` Write events in `pkg/orchestrator/orchestrator.go`). On manual retry, `<phase>.session.json` and `<phase>.jsonl` are cleared so detection starts fresh.

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
4. Add integration tests. Note: interactive stages (`stage.Interactive=true`) **ignore** the injected `Runner` — `runnerFor` always builds a real `executor.New(...)` driven by `stage.Command`. So interactive tests run a real bash script via `stage.Command` (see `TestFullDialogCycle`, `TestIntegration_DialogViolationDetected`), not `eagerProbeRunner` (which only applies to non-interactive stages)
5. Verify atomic write pattern (O_EXCL) is preserved in handlers

## Built-in Reverse Proxy

afm can run a built-in reverse proxy that intercepts agent HTTP traffic to Anthropic-compatible gateways and applies transforms. The primary use case is working around `api.z.ai` 529 errors: `ZAITransform` rewrites non-streaming requests to streaming, collects the SSE response, and reassembles it into a single Anthropic JSON `message`.

### Architecture

`run.go` starts the proxy before the orchestrator (random free port, `127.0.0.1` only). The proxy address and a `claude` shim directory are threaded through `orchestrator.Options` → `executor.Config` → the agent process env (`ANTHROPIC_BASE_URL`, `AFM_PROXY_URL`, and `PATH` with the shim dir prepended). `ZAITransform` is auto-detected when the upstream host contains `api.z.ai`.

**Upstream resolution (in `run.go`, before `orchestrator.New`):** `cfg.Proxy.Upstream` (config), else `os.Getenv("ANTHROPIC_BASE_URL")`. If both are empty the proxy is skipped (no-op + info log). A proxy `Start` failure is a **hard error** (`start proxy: %w`); a `CreateShim` failure is a **non-fatal warning** — env-var injection still points the agent at the proxy. Proxy + shim are torn down via `defer` (`p.Shutdown`, `os.RemoveAll(shimDir)`).

### Key Files

| File | Responsibility |
|------|-----------------|
| `pkg/proxy/transform.go` | `Transform` interface (`Match` + `ServeHTTP`) |
| `pkg/proxy/proxy.go` | `Proxy` struct, `New`/`Start`/`Addr`/`Shutdown`, `ServeHTTP` dispatch, `passthroughTo` |
| `pkg/proxy/zai.go` | `ZAITransform`, `BuildTransforms` (auto-detect + `*bool` override), `parseSSE` (SSE→JSON reassembly), `writeSSEError` |
| `pkg/proxy/shim.go` | `CreateShim` — temp dir with a `claude` wrapper that sets `ANTHROPIC_BASE_URL=<proxy>` and execs the real `claude` |
| `pkg/config/config.go` | `ProxyConfig`, `TransformOverrides`, `IsEnabled()` (nil `Enabled` → enabled), merge in `mergeFile` |
| `pkg/executor/executor.go` | `Config.ProxyURL`/`ProxyShimDir` → injects `ANTHROPIC_BASE_URL` + `AFM_PROXY_URL` and prepends shim dir to `PATH` in the agent env (also strips any pre-existing `ANTHROPIC_BASE_URL` when `ProxyURL` is set) |
| `pkg/orchestrator/orchestrator.go` | `Options.ProxyURL`/`ProxyShimDir` forwarded to **all four** `executor.New` call sites (`New`, two in `runnerFor`, `runnerForFallback`) |
| `cmd/afm/run.go` | Starts proxy + shim before the orchestrator; resolves upstream |

### How the ZAI transform works

For requests where `stream` is absent/false (and upstream is `api.z.ai`):
1. Inject `stream: true` into the body, forward to upstream.
2. Read the SSE response; `parseSSE` accumulates `message_start` (id/role/model/usage), `content_block_start` + `content_block_delta` (text/thinking/tool_use/signature deltas) in content-block index order, and `message_delta` (stop_reason + usage merge).
3. Return a single Anthropic JSON `message`.

`stream: true` requests and non-JSON bodies pass through unchanged. Upstream non-200 responses are forwarded transparently (status + headers + body). An upstream SSE `error` event or an empty SSE response yields HTTP 529 with a structured Anthropic-style error.

### Wrapper commands (glm51, etc.) — no patching required

When `client.command` is a wrapper (e.g. `glm51`) that exports `ANTHROPIC_BASE_URL` itself and then `exec`s `claude`, the proxy still works without patching the wrapper:
- afm prepends a shim dir to the agent's `PATH`. The shim is a `claude` script that sets `ANTHROPIC_BASE_URL=<proxy>` and execs the real `claude`.
- The wrapper clobbers `ANTHROPIC_BASE_URL`, but its inner `exec claude` resolves to the **shim** (PATH precedence), which re-sets the proxy address for the real `claude`.
- Requirement: the real `claude` must be in afm's `PATH` (used by `CreateShim`'s `exec.LookPath`).

This is why the shim wraps `claude` (the actual HTTP client), **not** `client.command` — wrapping the wrapper would be clobbered by the wrapper's own `export`. The env-var injection alone is insufficient for wrappers because they overwrite `ANTHROPIC_BASE_URL`; the shim is what actually delivers the proxy address to `claude`.

### Environment Variables

| Variable | Purpose | Set By |
|----------|---------|--------|
| `ANTHROPIC_BASE_URL` | Upstream source (fallback in `run.go`) / injected proxy address (to the agent) | `run.go` reads; executor injects proxy address when `ProxyURL` set |
| `AFM_PROXY_URL` | Proxy address (informational, mirrors `ANTHROPIC_BASE_URL`) | executor when `ProxyURL` set |
| `PATH` | Prepended with the shim dir so the wrapper's `claude` call resolves to the shim | executor when `ProxyShimDir` set |

### Config

```yaml
proxy:
  enabled: true                          # nil/absent → enabled by default
  upstream: https://api.z.ai/api/anthropic   # else read from $ANTHROPIC_BASE_URL
  port: 0                                # 0 = random free port
  transforms:
    zai: true                            # nil = auto-detect by host; true = force on; false = force off
```

### Debugging

- **`proxy: skipped (no upstream) …`** — no upstream found. Set `proxy.upstream` in config or `export ANTHROPIC_BASE_URL`.
- **`proxy: http://127.0.0.1:PORT → <upstream>`** — proxy started; agents route through it.
- **`warning: proxy shim: …`** — shim creation failed (usually `claude` not in `PATH`); non-fatal — env-var injection still applies, but wrapper commands that clobber `ANTHROPIC_BASE_URL` will bypass the proxy.
- **Proxy not taking effect with a wrapper command** — confirm `claude` is in `PATH` (shim requires it) and the wrapper ultimately execs a binary named `claude`.

### Common Changes

- **Add a transform:** implement `proxy.Transform` (`Match` + `ServeHTTP`), append it in `BuildTransforms`. `Proxy.ServeHTTP` dispatches to the first matching transform; requests with no match pass through unchanged via `passthroughTo`.
- **Change upstream resolution:** edit the block in `run.go` before `orchestrator.New`.
- **Keep all four `executor.New` sites in sync** when touching proxy plumbing in the orchestrator (`New`, two in `runnerFor`, `runnerForFallback`) — missing one leaves an agent path without proxy settings.

### Known limitations (tracked follow-ups)

- `pkg/proxy/zai.go` uses `http.DefaultClient` (no explicit timeout) — relies on the request context for cancellation.
- The SSE `[DONE]` terminator detection assumes `\n` line endings (Anthropic uses `\n`); `\r\n` would need a `TrimRight`.
- The `stop_sequence` field is currently never populated (always null).
- `passthroughTo` produces a double slash in the path if the upstream has a trailing slash.

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

`~/.claude.json` намеренно **НЕ** монтируется — claude создаёт свежий container-local конфиг (`/home/afm/.claude.json`). Auth кастомных агентов идёт через `ANTHROPIC_AUTH_TOKEN` из env, не через этот файл; попытка примонтировать его `:ro` приводила к падению (`corrupted: JSON Parse error`).

**Dashboard:** порт из `server.port` пробрасывается на хост через `-p <port>:<port>`, иначе UI недоступен снаружи контейнера. **Браузер** открывает хост-side opener: afm внутри Linux-контейнера сам открыть браузер на macOS-хосте не может (`runtime.GOOS=linux` → `xdg-open` без display), поэтому отдельный процесс-помощник запускается на хосте ДО re-exec, опрашивает проброшенный порт и зовёт `open`/`xdg-open`. Внутри контейнера вызов `openBrowser` пропускается (`AFM_IN_DOCKER=1`).

**Привилегии (важно):** контейнер стартует под root, но entrypoint (`docker-entrypoint.sh` + `gosu`) сразу дропает привилегии до хостового uid/gid (`AFM_HOST_UID/GID`, передаются из `os.Getuid/Getgid`) и выставляет `HOME=/home/afm`. Поэтому afm и агенты работают под тем же пользователем, что и на хосте — все записи в `~/.claude`, `~/.afm`, каталог проекта и `extra_mounts` принадлежат пользователю хоста, а не root (нет root-owned файлов и конфликтов с правами у хостового claude). Под non-root claude разрешает `--dangerously-skip-permissions` без `IS_SANDBOX`.

### Environment Variables

| Переменная | Назначение |
|-----------|------------|
| `AFM_USE_DOCKER=1` | Включить Docker mode без правки конфига |
| `AFM_IN_DOCKER=1` | Выставляется внутри контейнера — предотвращает рекурсию (не трогать) |
| `AFM_HOST_UID` / `AFM_HOST_GID` | Передаются внутрь; entrypoint дропает root до этого uid/gid (`gosu`), чтобы записи в тома принадлежали пользователю хоста |
| `AFM_DOCKER_IMAGE` | Переопределить образ (например, для локальной сборки) |
| `ANTHROPIC_API_KEY` | Пробрасывается в bare-форме `-e KEY` (без значения — не светится в `ps aux`/history) |
| `ANTHROPIC_BASE_URL` | То же самое |

### Публикация нового образа

```bash
make docker-push   # собирает Dockerfile.runtime и пушит в akopichin/afm:latest
```

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
