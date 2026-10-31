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

# provider usage limit (also applies during planning and verify):
running → awaiting_quota → running
                         ↘ paused → running (Continue)
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
- `awaiting_quota` — an agent hit a provider usage limit. afm records the wait in
  the event log and releases the agent's execution slot. With a known reset time,
  it resumes after the reset plus the configured margin; without one, it probes
  again after the configured interval. The same wait can occur during planning,
  execution, or an AI verify step. A parked verify step resumes at that step
  without rerunning the author. Other work using the same provider waits for the
  existing hold instead of making another request. See [Usage limits](config-reference.md#usage-limits).
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
  For an autonomous stage, a missing or empty `execution_summary.md` can receive
  one Stop hook reminder in the live session and up to two post-exit author
  continuations. Those continuations have their own durable budget; an unreadable
  file fails with its file error. See [Autonomous track](autonomous-track.md).
  A recognized provider usage limit uses `awaiting_quota` instead: waiting does
  not consume the ordinary retry or verify correction budget.
- `hook_failed` — a [`script_before` hook](script-stages.md) exhausted its retries;
  the stage is blocked until you hit **Retry** or **Skip** on the dashboard (a
  `script_after` failure never uses this status — the stage stays `done`)
- `paused` — waiting for you to hit **Continue** on the dashboard: either gated by
  `auto_run: false` on first activation, or manually paused mid-run with **Pause** in the
  stage header. A usage limit also pauses the stage when the provider reports quota
  exhaustion with no reset, its reset exceeds `usage_limit.max_wait`, or
  consecutive refusals without progress exceed `usage_limit.max_waits`.
  Continue makes one immediate attempt;
  another limit can park or pause it again. See
  [Dashboard → Pausing a stage](dashboard.md#pausing-a-stage-before-it-starts).
- `done` / `failed` — complete

## Exit on stage failure

With [`afm run --exit-on-fail`](config-reference.md#exit-on-stage-failure), the
first stage committed as `failed` stops the run and makes the command exit 1.
Automatic retries do not stop it while the stage is still `retrying`. A failed
required helper that has not been waived also fails its stage after the main
agent finishes; advisory helpers do not. The error names the stage and reason,
and includes the run directory. The event log remains available for resume.

afm cancels active work and waits for its agents to stop, without waiting for
other stages to finish normally or for the dashboard's usual exit grace period.
An active neighboring stage may itself become `failed` during cancellation.
A `script_before` failure in `hook_failed` and a `script_after` failure on an
already `done` stage do not trigger this option, because neither moves the
stage to `failed`.

On the next `afm run`, `auto_recover: true` (the default) resets failed stages
for another attempt in the same run. With `auto_recover: false`, a resumed
`--exit-on-fail` run returns 1 for an already failed stage before starting
agents. A manual stage **Retry** after a failed required-helper gate starts a
new generation of its required helper tasks. Without this option, the existing
run completion and exit behavior remains in effect.

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
- Restores `awaiting_quota` with its recorded wake time; a stage whose wait
  expired while afm was stopped resumes on restart
- Restarts interrupted stages (`planning`, `running`, `revising`, `retrying`)
- Restores autonomous stages (from `execution_summary.md` / `autonomous.flag`)
- Preserves spent autonomous summary reminders and post-exit continuations; an
  interrupted continuation does not gain an extra attempt
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
