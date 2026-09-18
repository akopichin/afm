# Lifecycle hooks

Lifecycle hooks are **observer** commands that afm runs when flow/stage events
happen — to send notifications, push metrics, or update an external status board.
They are deliberately separate from [`script_before`/`script_after` and script
stages](script-stages.md):

- `script_before`/`script_after` participate in a stage and can block it;
- a lifecycle hook only *observes* — it receives the event and runs alongside;
- **a lifecycle hook error never changes the FSM, the stage result, or the flow
  outcome.** A failed notifier can't break your run.

```yaml
hooks:
  - id: notify
    events: all
    command: "bash ./scripts/notify.sh"
```

## Where hooks are declared

Hooks can be declared at four layers; the effective list is the union across all
of them:

1. global `~/.afm/config.yaml`
2. project `.afm/config.yaml`
3. the flow file root (`flow.yaml`)
4. inside a single stage

Global, project and flow hooks receive events from **all** stages. A **stage**
hook receives only its own stage's events (flow-level events like `flow_finished`
are rejected in a stage hook at parse time).

Each hook has an `id`, used in logs and diagnostics. `id` is unique within a
layer; if the same `id` appears in a more specific layer it **replaces** the
one above (`global < project < flow < stage`). The same `id` in two different
stages is a config error.

## Selecting events

`events` is either the scalar `all` or a list of event names; `skip_events`
subtracts from it (`effective = events − skip_events`):

```yaml
hooks:
  - id: telegram
    events:
      - flow_finished
      - flow_failed
      - stage_failed
      - stage_question_asked
    command: "bash ./scripts/telegram-notify.sh"

  - id: metrics
    events: all
    skip_events: [stage_question_answered, stage_retry_started]
    command: "/opt/afm/metrics.sh"
```

An unknown event name is a config error (so a typo in a rule is never silently
ignored). `events: all` means all public lifecycle events — it does **not**
include high-frequency internal events (`agent_action`, `script_output`, event
loop wakeups).

### Event catalog

**Flow:** `flow_started`, `flow_resumed`, `flow_finished`, `flow_failed`,
`flow_interrupted`.

**Stage:** `stage_planning_started`, `stage_plan_ready`, `stage_approved`,
`stage_revision_started`, `stage_execution_started`, `stage_question_asked`,
`stage_question_answered`, `stage_retry_scheduled`, `stage_retry_started`,
`stage_paused`, `stage_resumed`, `stage_finished`, `stage_failed`.

**Scripts (script stages & `script_before`/`script_after`):**
`stage_script_started`, `stage_script_finished`, `stage_script_failed`,
`stage_script_before_started`, `stage_script_before_finished`,
`stage_script_before_failed`, `stage_script_after_started`,
`stage_script_after_finished`, `stage_script_after_failed`.

## What the hook receives

The full event is delivered as **JSON on stdin**:

```json
{
  "schema_version": 1,
  "event_id": "release-20260918-120000-ab12:transition:42:stage_failed",
  "event": "stage_failed",
  "occurred_at": "2026-09-18T12:04:31.381Z",
  "flow": { "name": "release", "run_id": "…", "resumed": false,
            "run_dir": "…/.afm/runs/…", "root_dir": "/project" },
  "stage": { "id": "deploy", "name": "Deploy production",
             "from": "running", "to": "failed", "phase": "implementation" },
  "reason": "agent exited with status 1"
}
```

The common scalars are also passed as environment variables, so a small script
needs no JSON parser:

```
AFM_HOOK_EVENT      AFM_HOOK_EVENT_ID   AFM_FLOW_NAME   AFM_RUN_ID
AFM_RUN_DIR         AFM_ROOT_DIR        AFM_STAGE_ID    AFM_STAGE_NAME
AFM_STAGE_FROM      AFM_STAGE_TO
```

For flow-level events the stage variables are empty. Use `AFM_HOOK_EVENT_ID` as
an idempotency key if your receiver needs one.

The command is run through `sh -c` with the working directory set to the
effective `flow.root_dir` (the same CWD agents use). Event values are never
interpolated into the command string — pass a static command and read the event
from stdin/env.

## Secrets and the hook environment

A hook often needs a token or webhook URL. Declare it in the hook's `env` map —
the **key** is the variable name the hook process will see, the **value** is a
reference to where afm reads it from:

```yaml
hooks:
  - id: telegram
    events: [flow_finished, flow_failed, stage_failed]
    command: "bash ~/.afm/t-notify.sh"
    env:
      TELEGRAM_BOT_TOKEN: "file:~/.afm/secrets/telegram-bot-token"
      TELEGRAM_CHAT_ID:    "env:TELEGRAM_CHAT_ID"
```

Two reference kinds:

- `file:PATH` — read the value from a file (trimmed).
- `env:NAME` — take `NAME` from `secrets.env`, then from afm's own process env.

For `env:NAME`, `secrets.env` files are searched **project `.afm/secrets.env` >
global `~/.afm/secrets.env` > process env** (more specific wins). Files are the
usual `KEY=VALUE` format. The reference source must be `env:` or `file:` —
plain literals are rejected. Target names must match `[A-Za-z_][A-Za-z0-9_]*`
and cannot start with the reserved `AFM_` prefix.

References are resolved **once, before the flow starts**. A missing variable,
missing file, or empty value is a config error (naming the hook and the
variable, never the secret value) — the run fails fast rather than starting a
hook whose secret can't be resolved.

### The hook process environment

By default a hook process gets a **minimal** environment — `PATH`, `HOME`,
locale, temp dir, proxy/cert settings — plus the `AFM_*` event variables and the
resolved `env`. This keeps unrelated variables (and other secrets) in afm's
environment out of the hook. To give the hook the full inherited environment,
set `inherit_env: true`:

```yaml
    env: { WEBHOOK: "file:~/.afm/secrets/webhook" }
    inherit_env: true   # full os environment + AFM_* + resolved env
```

### Redaction

Resolved secret values are replaced with `[REDACTED]` in the hook's saved
stdout/stderr and in any surfaced error, as a safety net against an accidental
`set -x` or `echo "$TOKEN"`. This is best-effort — a script that transforms or
re-encodes a secret can still leak it, so hook authors should still never print
secrets. Secrets are never written to the JSON payload, the event log, or the
command arguments.

## Execution and errors

A typical hook:

```yaml
    timeout: 30s   # default 30s
    retries: 2     # default 0 (a single attempt)
```

If a hook's command ultimately fails:

- stage and flow statuses are unchanged;
- the error is written to the hook log;
- a warning appears in the dashboard;
- the flow continues.

Calls to one hook run sequentially (a single consumer sees events in order);
different hooks run in parallel. On a clean flow exit afm waits a bounded time
for the hook queue to drain, and the terminal `flow_finished`/`flow_failed`
event is emitted before that flush.

Each hook's stdout/stderr is saved to `.afm/runs/<run-id>/hooks/<hook-id>.log`.

### Delivery guarantee

Delivery is **live, best-effort**: while afm is running, a matching event is
delivered to each hook (with `timeout`/`retries`). If afm crashes before an
event is delivered, that event is not replayed after restart. (Durable
at-least-once redelivery is planned but not yet available.)

## Docker mode

In [Docker mode](docker.md) hook secrets are resolved on the **host** before
`docker run` and passed into the container through transient environment
variables — you do **not** need to mount secret files, and the value never
appears in `docker run` arguments or `ps`. Inside the container those transient
variables are consumed and then removed, so they aren't inherited by agents or
other processes.

!!! note "Isolation boundary"
    afm's Docker mode already bind-mounts `~/.afm` and the project directory into
    the container. A secret **file** placed under one of those mounted
    directories (e.g. `file:~/.afm/secrets/…`) is therefore readable by any
    process running in the container under the same user — lifecycle hooks do not
    promise absolute isolation from same-user code. If you need stronger
    isolation, keep secret files outside the mounted directories, or use `env:`
    sources resolved from the host process environment.

## Example: Telegram notifications on every stage

```yaml
name: release
hooks:
  - id: telegram
    events: all
    command: "bash ~/.afm/t-notify.sh"
    env:
      TELEGRAM_BOT_TOKEN: "file:~/.afm/secrets/telegram-bot-token"
      TELEGRAM_CHAT_ID:    "file:~/.afm/secrets/telegram-chat-id"
stages:
  - id: build
    script: "make build"
  - id: deploy
    agents: [planning, implementation]
```

A minimal `t-notify.sh` builds the message from the `AFM_*` variables and posts
it to Telegram:

```bash
#!/usr/bin/env bash
set -euo pipefail
: "${TELEGRAM_BOT_TOKEN:?}"; : "${TELEGRAM_CHAT_ID:?}"
msg="afm: ${AFM_HOOK_EVENT} — ${AFM_FLOW_NAME}"
[[ -n "${AFM_STAGE_ID:-}" ]] && msg+=" / ${AFM_STAGE_NAME} (${AFM_STAGE_FROM}→${AFM_STAGE_TO})"
curl -fsS --data-urlencode "chat_id=${TELEGRAM_CHAT_ID}" \
  --data-urlencode "text=${msg}" \
  "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/sendMessage" >/dev/null
```
