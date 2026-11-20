# Five-minute checks of delegated work

A long assignment budget is not one long sleep. While any delegated writer,
diagnostician or review helper remains outstanding, schedule a health check at
least every 300 seconds of waiting. This applies to a single request and batches,
including implement, plan consultations, verify corrections and live repairs.
Use durable AFM wait deadlines, not shell polling, sleep loops or another agent.

## Separate the work budget from the wait window

Before dispatch save the logical task, canonical request, assignee/task IDs,
brief revision, ownership, `assigned_at` and absolute `task_deadline` in
team-progress.json. A one-hour assignment still has a one-hour task deadline.
Save `last_health_check_at`, last observed status/activity cursor or timestamp,
evidence paths, and any recovery attempt. Recover these on resume.

Each wait window is `min(300, remaining task budget)` seconds, rounded down to
whole seconds. If the task budget has elapsed, handle that outcome immediately;
never register a zero-second wait, which means no deadline. For a batch use the
earliest outstanding task deadline. Register only the unanswered canonical IDs:

```sh
afm agent wait --condition reply_to_all --message-ids <unanswered-canonical-ids> --deadline-seconds <1-to-300> --note "Health check: inspect outstanding agents, results and recent activity before the next wait."
```

One batch supports at most 16 IDs. Persist its batch ID and window deadline. On
`yield`, end the turn without a completion marker. The deadline wakes the stage
even when a long task's process has stopped producing events. Normal replies and
terminal failures may wake it earlier. A callback does not finish the batch:
answer it, inspect the existing pending window and do not stack duplicate waits.
After a window expires, reconcile outcomes, perform the health check, and register
one NEW window for still-unanswered requests. Reusing those canonical requests
after wait expiry is allowed; do not resend or restart the healthy work. Never
re-register an answered request or renew an already-pending equivalent wait.

Window expiry is a health checkpoint, not a task timeout, failed repair, rejected
review, loss of file ownership or reason to extend the original task deadline.
Do not keep a separate hour-long pending wait beside these windows. Recover any
old long wait before switching: shorten it only through supported own-wait
cancellation and record why; registering another equivalent wait does not change
the original deadline. Never claim five-minute coverage while only that long
wait is pending. If own-wait recovery is unavailable, report the exact blocker.

## Inspect progress before waiting again

On every wake read `afm agent wait-status --wait-id <batch-id>`, reconcile inbox
and final replies, and run `afm agent list`. For each OWN temporary helper read
`afm agent helper-result --task-id <task-id>` for committed status/result. Inspect
the latest granted run logs/activity and stderr for the exact assignee/task/turn;
read a bounded tail or changes since the saved cursor, not whole histories.
Use only logs or authenticated read capabilities granted to this participant.
Record concrete observations and timestamps, not just "still working".

- A completed result is collected and evaluated immediately; an ACK or activity
  spinner is not a final result. Remove accepted requests from the next window.
- A failed/cancelled task, unreachable recipient or confirmed process exit with
  terminal CLI/connection failure is handled immediately, before the original
  task deadline. Do not wait for the rest of an hour-long assignment.
- A reconnect/retry message alone may be recoverable. Check subsequent events,
  process/command outcome and task status before calling it dead. A partial
  stream, old timestamp or five minutes of silence alone does not prove a hang:
  a real compile/test/tool call or model computation can be quiet.
- If progress is uncertain, send ONE ordinary status message in the existing
  conversation, naming the canonical request and asking for current operation,
  last completed check and concrete blocker. Do not create a new work request,
  reply-to wait or automatic final ACK. Recheck at the next five-minute window.
  Persistent lack of progress across two checks plus evidence of a stuck/lost
  process warrants bounded recovery; do not keep yielding unchanged until deadline.

## Recover without duplicate writers

Preserve partial work, logs, failure reason and prior findings. First reconcile
late replies and any pending child commands. A failed agent can leave a compiler,
test or other modifying child alive. Confirm the old writer AND its modifying
commands have stopped; otherwise use an actually supported, granted cancellation
or stop capability for that exact task/owned process, then verify termination.
No name/pattern kills, borrowed identities, operator APIs or journal/state edits.
If safe termination cannot be established with available capabilities, report the
concrete task/process and missing stop capability now rather than silently
waiting for the original deadline or starting a second writer.

Once safe, record release and replacement in the same task ledger. Select a
reachable standing agent or supported replacement with the required role/access
and independent-review constraints. Do not turn a read-only helper into a writer,
skip a mandatory role or activate Fabula just because infrastructure failed.
Release/reassign a typed Board assignment using current versions when applicable.
For a temporary reviewer, spawn a fresh helper with a new saved operation ID;
an uncertain spawn response is reconciled/retried under its original ID instead.

Give the replacement a NEW canonical request with the original task, latest brief,
partial diff/evidence, exact remaining scope, ownership and bounded budget. Link
old/new IDs with `supersedes`, retire only your obsolete waits through supported
own-wait recovery, and save the handoff before waiting. Exclude superseded requests
from future windows. A late old reply is evidence, not authority to resume editing
or acceptance of the replacement's work. Do not reset repair/review counters or
extend the overall task budget implicitly; record any justified new recovery
budget explicitly. Infrastructure failures are not product rejection rounds.
One automatic replacement per logical assignment is allowed; if it also fails,
no suitable replacement exists or a required capability is missing, surface the
concrete blocker immediately. Further retries need a new decision, not a loop.

Finish by reconciling every window, request and helper. Stop monitoring when no
delegated work remains; there is no permanent heartbeat loop to leave behind.
