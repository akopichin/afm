# Stage lifecycle

```
pending → planning → awaiting_approval → ready → running → done
                                                  ↓
                                          awaiting_assistants → done
                ↓                                     ↓        ↘ failed
                └────→ awaiting_user_input ←──────────┘
         ↑                                         ↓
         └───────── revising ←────────────────────┘

# autonomous track (agents: [auto]):
pending → running(autonomous_execution) → done

# agent-to-agent wait:
running → awaiting_peer → running
```

- `pending` — not started yet; planning starts once all `depends_on` are complete
  (unless `eager_planning: true`)
- `planning` — the AI builds a plan
- `awaiting_approval` — the plan is ready, awaiting approval (web or CLI)
- `ready` — the plan is approved, waiting its turn
- `running` — the AI implements the plan (or runs the [autonomous track](autonomous-track.md))
- `awaiting_assistants` — the stage's own work is finished, but a
  [required helper](collaboration.md) has not passed or been waived; the stage
  completes when the required helper gate closes
- `awaiting_peer` — the stage agent yielded while waiting for an Agent Mesh reply;
  it resumes when that wait resolves (see [Team conversations](collaboration.md#team-conversations))
- `awaiting_user_input` — an [interactive stage](interactive-stages.md) is waiting for
  a user answer; once answered, it returns to the phase where the question was asked
- `revising` — feedback was sent and the AI is reworking: either the plan (from
  `awaiting_approval`), or a `running` stage that just got a note and a graceful
  interrupt (see [Dashboard → Revise](dashboard.md#revise))
- `retrying` — an agent process failed and an automatic retry is scheduled. Known
  rate-limit, server, and network errors get up to 15 retries; an unrecognized
  process failure or idle timeout gets up to 3. A process that could not start
  fails immediately. Delays start at 5 seconds, double up to a 60-second cap,
  and are reduced by up to 20% of jitter. The retry notice shows the actual delay.
  Completion-check corrections and `verify` retries have separate rules.
- `hook_failed` — a [`script_before` hook](script-stages.md) exhausted its retries;
  the stage is blocked until you hit **Retry** or **Skip** on the dashboard (a
  `script_after` failure never uses this status — the stage stays `done`)
- `paused` — waiting for you to hit **Continue** on the dashboard: either gated by
  `auto_run: false` on first activation, or manually paused mid-run via the kebab (⋮)
  menu (see [Dashboard → Pausing a stage](dashboard.md#pausing-a-stage-before-it-starts))
- `done` / `failed` — complete

## Resume on restart

When you invoke `afm run` again, the tool automatically:

- Skips completed stages (`done`)
- Preserves stages awaiting approval (`awaiting_approval`)
- Rechecks the required-helper gate for stages in `awaiting_assistants`, without
  rerunning their completed main agent
- Restores pending Agent Mesh waits for stages in `awaiting_peer` and resumes the
  stage when its wait resolves; a `running` stage whose turn had already
  yielded on waits (`afm agent wait` answered `yield`) is parked on them
  instead of restarted
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

## Repeating a flow

With [flow-level `repeat`](flow-reference.md#repeat-run-successful-flows-again), a
successful pass finishes its run, then afm starts the flow again in a new run
directory. Stages begin from `pending` in the new run; their previous `done`
states are not reset in place. A failed stage, interruption, or storage failure
stops repetition. A finite `max_cycles` counts passes including the first;
restarting the `afm run` process starts that count over.

Between runs, the dashboard shows the completed run for about five seconds,
then restarts on the same port without waiting for an open tab to disconnect.
There is no configured pause between cycles. On final exit, the normal
dashboard drain applies. An open tab follows the new run automatically; see
[Web dashboard](dashboard.md#following-a-repeated-flow).
