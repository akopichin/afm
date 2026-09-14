# Stage lifecycle

```
pending → planning → awaiting_approval → ready → running → done
                ↓                                     ↓        ↘ failed
                └────→ awaiting_user_input ←──────────┘
         ↑                                         ↓
         └───────── revising ←────────────────────┘

# autonomous track (agents: [auto]):
pending → running(autonomous_execution) → done
```

- `pending` — not started yet; planning starts once all `depends_on` are complete
  (unless `eager_planning: true`)
- `planning` — the AI builds a plan
- `awaiting_approval` — the plan is ready, awaiting approval (web or CLI)
- `ready` — the plan is approved, waiting its turn
- `running` — the AI implements the plan (or runs the [autonomous track](autonomous-track.md))
- `awaiting_user_input` — an [interactive stage](interactive-stages.md) is waiting for
  a user answer; once answered, it returns to the phase where the question was asked
- `revising` — feedback was sent and the AI is reworking: either the plan (from
  `awaiting_approval`), or a `running` stage that just got a note and a graceful
  interrupt (see [Dashboard → Suggesting a note](dashboard.md#suggesting-a-note-to-a-running-stage))
- `retrying` — a transient error (rate limit / 5xx), auto-retry with backoff
- `hook_failed` — a [`script_before` hook](script-stages.md) exhausted its retries;
  the stage is blocked until you hit **Retry** or **Skip** on the dashboard (a
  `script_after` failure never uses this status — the stage stays `done`)
- `paused` — waiting for you to hit **Continue** on the dashboard: either gated by
  `auto_run: false` on first activation, or manually paused mid-run via the kebab (⋮)
  menu (see [Dashboard → Pausing a stage](dashboard.md#pausing-a-stage-before-it-starts))
- `done` / `failed` — complete

## Resume on restart

On a repeated `afm run`, the tool automatically:

- Skips completed stages (`done`)
- Preserves stages awaiting approval (`awaiting_approval`)
- Restarts interrupted stages (`planning`, `running`, `revising`, `retrying`)
- Restores autonomous stages (from `execution_summary.md` / `autonomous.flag`)
- Preserves stages in `awaiting_user_input`: question/answer files survive the
  restart, an unanswered question is shown again in the dashboard, and once answered
  the stage continues
- **Auto-retries `failed` stages** (`auto_recover`, default `true`): if a run was
  interrupted hard enough that a stage landed in `failed` (e.g. the process or Docker
  container was killed), the next `afm run` resets every failed stage back to
  `pending` before doing anything else — no manual `afm retry <id>` needed. All
  failed stages are reset regardless of why they failed; dependency order
  (`depends_on`) is preserved automatically, since a reset stage just re-enters the
  normal pending flow. Set `auto_recover: false` in `.afm/config.yaml` to go back to
  requiring manual `afm retry` for each failed stage.

Approve/revise/retry are durably recorded in the log (fsync) before control
returns — a crash right after approval doesn't lose the intent; recovery continues
from the correct state. See [the event log](internals/event-log.md).
