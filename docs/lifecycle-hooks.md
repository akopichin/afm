# Lifecycle hooks

Lifecycle hooks are **observer** commands that afm runs when flow/stage events
happen — to send notifications, push metrics, or update an external status board.
They are deliberately separate from [`script_before`/`script_after` and script
stages](script-stages.md):

The autonomous author's [Stop hook reminder](autonomous-track.md) is a separate
CLI integration for `execution_summary.md`; it does not add a hook to this list.

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

**Flow:** `flow_started`, `flow_resumed`, `flow_repeated`, `flow_finished`,
`flow_failed`, `flow_interrupted`.

For a [repeating flow](flow-reference.md#repeat-run-successful-flows-again), the first
run emits `flow_started`. Each later cycle emits `flow_repeated` instead of
`flow_started`; a resumed run still emits `flow_resumed`. Successful cycles emit
`flow_finished` before the next run starts. `events: all` includes
`flow_repeated`.

**Stage:** `stage_planning_started`, `stage_plan_ready`, `stage_approved`,
`stage_revision_started`, `stage_execution_started`, `stage_question_asked`,
`stage_question_answered`, `stage_retry_scheduled`, `stage_retry_started`,
`stage_paused`, `stage_resumed`, `stage_finished`, `stage_failed`.

Usage-limit waits add `stage_awaiting_quota` when a stage is parked and
`stage_quota_reset` when the wait ends and work resumes. They are included in
`events: all`. Their `reason` names the provider and, when known, the limit
window and wait time. Times in these reasons are explicitly **UTC**. An
unwaitable limit also emits `stage_paused` with its reason; a recognized
usage-limit wait does not emit the ordinary retry events.

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

`stage_finished` and `stage_failed` also carry the stage's time and tokens in
`data.analytics`; see [Stage analytics](#stage-analytics).

The common scalars are also passed as environment variables, so a small script
needs no JSON parser:

```
AFM_HOOK_EVENT      AFM_HOOK_EVENT_ID   AFM_FLOW_NAME   AFM_RUN_ID
AFM_RUN_DIR         AFM_ROOT_DIR        AFM_STAGE_ID    AFM_STAGE_NAME
AFM_STAGE_FROM      AFM_STAGE_TO
```

In repeat mode, the `flow` object of every hook payload also has `cycle`
(starting at 1) and `max_cycles` (`0` means unlimited). Cycles after the first
also have `previous_run_id`. These fields are absent outside repeat mode.
`AFM_FLOW_CYCLE` gives the same cycle number to hook commands in repeat mode;
it is absent otherwise. Each cycle has its own `run_id` and `run_dir`.

For flow-level events the stage variables are empty. Use `AFM_HOOK_EVENT_ID` as
an idempotency key if your receiver needs one.

`AFM_RUN_DIR` and `AFM_ROOT_DIR` are absolute paths when set, even if afm was
started with relative paths. An unset `AFM_ROOT_DIR` stays empty.

The command is run through `sh -c` with the working directory set to the
effective `flow.root_dir` (the same CWD agents use). Event values are never
interpolated into the command string — pass a static command and read the event
from stdin/env.

## Stage analytics in `stage_finished` and `stage_failed` { #stage-analytics }

The two terminal stage events add a `data.analytics` object, so a notifier can
report how long a stage took and how many tokens it used without reading run
files. Other events have no `data`. The object has its own `version` (currently
`1`); the envelope's `schema_version` is unchanged, and a hook that ignores `data`
keeps working.

```json
{
  "schema_version": 1,
  "event": "stage_finished",
  "stage": { "id": "build", "name": "Build", "from": "running", "to": "done" },
  "data": {
    "analytics": {
      "version": 1,
      "as_of": "2026-10-31T14:31:07.412Z",
      "stage_event_seq": 37,
      "usage_revision": 12,
      "jobs_revision": 24,
      "scope": "stage_cumulative",
      "timing": {
        "total_ms": 103772, "work_ms": 99096, "waiting_ms": 4676, "paused_ms": 0,
        "approval_ms": 0, "helpers_ms": 0, "backoff_ms": 4676, "scheduling_ms": 0,
        "complete": true
      },
      "agent_jobs": {
        "count": 4, "active": 1, "measured": 3, "unknown_time": 0,
        "duration_ms": 102107, "complete": false, "reason": "active_jobs"
      },
      "usage": {
        "health": "ok", "coverage": "partial",
        "metered_jobs": 3, "unmetered_jobs": 0, "pending_jobs": 1,
        "tokens": {
          "uncached_input": 5200, "cache_read": 14100, "cache_write_5m": 800,
          "cache_write_1h": 0, "cache_write_other": 0, "output": 3480, "total": 23580
        },
        "reason": "pending_usage"
      }
    }
  }
}
```

(Other envelope fields are omitted from the example.)

- **Cumulative, not a delta.** `scope: "stage_cumulative"` means every attempt of
  the stage and its helpers up to this event. After a **Retry**, the next
  terminal event counts the earlier attempts as well, so subtract the previous
  event's values if you need the increment. Its `event_id` is new; a redelivery
  of the same event carries the same bytes.
- **Time.** `timing.total_ms` is working plus waiting plus paused time, as in
  [Stage time](dashboard.md#stage-time). `waiting_ms` is the sum of `approval_ms`,
  `helpers_ms`, `backoff_ms` and `scheduling_ms`.
- **Agent jobs.** `agent_jobs.duration_ms` adds the durations of the stage's
  [agent jobs](accounting.md#agent-jobs-and-agent-time). Parallel helpers make it
  larger than `work_ms`. A helper that is still running when the stage ends counts
  in `active`.
- **Tokens.** `usage.tokens` covers the jobs that had finished when the stage
  ended, including helpers of the stage, and never includes money.
- **Unknown is `null`, not `0`.** A figure afm cannot determine is `null`, and
  `complete` is `false` (or `coverage` is `partial` or `none`) with a short
  `reason`. A real zero stays `0`.

| `reason` | Meaning |
|---|---|
| `active_jobs` | a job of the stage was still running at the event |
| `pending_usage` | a running job has not reported its usage yet |
| `pending_start` | a helper was starting; its job was not recorded yet |
| `unmetered_job` | some jobs reported no usage |
| `stage_advanced_before_snapshot` | the stage was retried before the snapshot was built: the numeric fields of `timing` are `null`, `timing.complete` is `false` and `timing.reason` is this value |
| `started_before_recording`, `legacy_journal` | the stage's start is not covered by the record: its start cannot be placed in the event log (`timing`), or the agent-jobs journal began later or does not exist (jobs and tokens), see below |
| `jobs_store_unavailable`, `usage_store_unavailable` | a journal could not be read |

A **script-only** stage reports `agent_jobs.count: 0` and `usage.tokens.total: 0`
with `coverage: "full"`, which is a proven zero. On a run recorded before afm
kept the agent-jobs journal, the same stage reports `null` and a `reason`
instead (`legacy_journal`, or `started_before_recording` when the journal was
created on a run that was already going).

The snapshot is built right after the transition succeeds, and no hook delays
the stage. The tiny gap has one consequence: a job that starts in the
microseconds before the transition and is recorded after the snapshot may be
missing from it. `pending_start` covers most of those cases for helpers.
A failed hook never changes the stage.

Collection does not depend on the display settings. `accounting.enabled: false`
and `show_money: false` hide figures in the dashboard, but the payload still has
the tokens; it has no money fields in any mode.

Read it from the JSON on stdin:

```bash
payload=$(cat)
echo "$payload" | jq '.data.analytics.timing.total_ms'
echo "$payload" | jq '.data.analytics.usage.tokens.total'
```

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

References are resolved before the first run starts, and again before each new
run in native repeat mode. In Docker repeat mode, hook secrets are transported
once for the process, so changing the identity, order, or `env` references of
hooks with `env` requires restarting `afm run`.
A missing variable, missing file, or empty value is a config error (naming the
hook and the variable, never the secret value) — the run fails fast rather than
starting a hook whose secret can't be resolved.

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
