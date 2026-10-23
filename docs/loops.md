# Repeating prompts with loops

A loop delivers the same prompt to one existing agent at a fixed interval during
a run. Use it to ask a run agent to check logs periodically, or to ask a stage
helper for another pass while its stage is open. Collaboration must be enabled.
The agent must already exist and be reachable when you create the loop.

Loops accept prompts. They do not run shell scripts directly, create a new agent
for each tick, or run outside the current afm run. An agent receiving a prompt
uses its usual tools, permissions and concurrency limits.

## Create a loop in the dashboard

Open a non-script stage's **Feed → Message** and enter:

```text
/loop 5m @watcher Check the latest logs and report new errors
```

`@watcher` is an existing run agent or a helper attached to this stage. Type `@`
to choose an eligible participant; the stage's main agent and the Assistant are
not loop targets. The first `@handle` after the interval is the target. Later
mentions belong to the prompt: `/loop 5m @watcher Check reports from @docs`
sends one repeating prompt to watcher, with `@docs` kept in its text.

Typing `/loop` shows the syntax and selectable **5m**, **15m** and **1h** examples.
Choosing an example replaces the interval and returns focus to the input. You
can type another interval, including `10s`, `90s` or `1h30m`. Intervals use Go
duration syntax and must be at least one second; `1d` is not a duration unit.

Submit with **Start loop** or **Cmd/Ctrl+Enter**. The receipt shows the loop's
short ID (`L1`, `L2`, …), target, interval, next tick in the host's time zone with
seconds, original Feed and stop command. Stage IDs are shown when stages have
no display name. Results remain until the next edit or Escape; use `/loop list`
to retrieve them again after switching stages or reloading.

A loop command has its own target and bypasses ordinary message recipients,
reply chips and helper attachment. It does not interrupt or revise the stage.
In **Revise**, a `/loop` draft shows **Slash commands run in Message** and cannot
be submitted. Malformed commands and server failures keep your draft and show
the reason. Text beginning `/loops`, or mentioning `/loop` later in a message,
stays an ordinary message.

## Choose how long it lives

| Target | Default lifetime | Optional earlier end |
|---|---|---|
| Run agent | Continues across stages until the run finishes | Bind it to a stage |
| Stage helper | Ends with the helper's stage | Bind it to another stage that may finish sooner |

To limit a run-agent loop to the stage whose Feed you are using:

```text
/loop 5m --until-stage-end @watcher Check logs until this stage finishes
```

The flag comes between the interval and `@target`. Without it, switching to the
next stage does not stop a run-agent loop. Its scheduled messages stay in the
Feed where it was created; selecting another stage does not move them. Agents
creating loops use the explicit `--until-stage STAGE_ID` flag instead.

A helper's successful one-shot task can run again on a later tick. A resting
watcher can also run again, and an agent waiting for a mesh message can resume
when the tick arrives. afm reuses the helper's task, participant and saved role.
It does not bypass a required helper's unresolved review decision: a helper
whose required gate is open still needs the usual Revise, Retry or waiver.

A failed helper or failed bound stage receives no ticks. If you retry it during
the same run and it becomes eligible again, an unstopped loop can become active
again. Cancellation, a completed stage, a helper superseded by a new stage or
parent attempt, or a closed/full loop conversation ends the relevant loop.

## Inspect and stop

From any non-script stage's **Feed → Message**, including a completed stage
while the run is still open:

```text
/loop list
/loop stop L1
```

Neither command needs a recipient. The list includes active, stopped and ended
loops in creation order, with their creators, targets, intervals, original
Feeds, stage bounds, next ticks or end reasons. You can stop a loop created in
another Feed or by an agent. A stopped loop stays stopped after a page reload
or process restart. Stopping prevents future deliveries; an already delivered
tick or an agent turn already in progress can still finish.

There are at most **16 active loops per run**. A loop suspended by a reversible
failure still occupies a slot, because Retry can bring it back. Stop it to free
that slot. Short IDs are local to the run and are not reused within it.

## Fixed ticks and skipped slots

The durable creation time anchors the schedule. A loop created at 12:00 with a
five-minute interval has slots at 12:05, 12:10, 12:15, and so on. It does not
wait five minutes after the previous agent turn finishes.

At a slot, afm skips delivery if the recipient is busy, queued for a turn,
held or paused, or still has this loop's previous message waiting to be handed
to it. Review pause also suspends delivery. A durable mesh wait is eligible:
the tick can wake that agent through normal message delivery. The same agent
does not get overlapping turns from a loop.

For example, if a turn started at 12:05 is still running at 12:10, that slot is
skipped. If the agent is ready at 12:15, it receives the next tick then. Skipped
slots do not queue up. A late scheduler attempts at most the latest due slot;
after an afm restart it starts at the next future slot, without catching up
ticks missed while it was offline.

**Next tick** is the next scheduled opportunity, not a guarantee of delivery or
an estimate of when the agent will finish. There is no per-slot skip history.
Completed or failed flows with no nonterminal stages deliver no ticks, and a
loop never keeps a finished flow open.

## Conversations and limits

Each loop gets its own conversation on its first delivered tick. Messages use
the creator's identity and a visible header such as `⟳ Loop L1 · every 5m`,
followed by the prompt. Replies link to the tick that triggered them. Ordinary
new conversations do not reuse a loop's thread.

A browser-created loop appears in its original stage Feed. A helper-created
loop appears in that helper's stage Feed. A run agent's own loop has no stage
origin and is available in the run's conversation view. A self-loop does not
generate an automatic reply to its own sender. A helper that creates a loop for
a run agent can finish while that loop continues; ticks no longer generate
automatic replies to a creator that cannot receive them.

The existing conversation budget is **200 messages**, including replies. afm
reserves room for a tick and its reply before delivering. When the thread is
full or closed, `/loop list` reports the end reason. The loop does not rotate to
another thread: create a new loop if you need to continue.

Prompts may be at most **15,872 bytes** (the message limit minus header room).
afm redacts known secrets before persisting the prompt. For agent commands,
use a file or stdin when the text should stay out of command-line arguments.

## Agent CLI

An agent inside an afm invocation can create a loop for itself:

```sh
afm agent loop add --every 5m --prompt "Check the latest logs"
```

End the current turn after creating a self-loop. The next eligible tick starts
another turn; you do not need to register `afm agent wait` for it. To target a
colleague and optionally bind the schedule to a stage:

```sh
afm agent loop add --every 90s --to @watcher \
  --until-stage build --prompt "Check build logs"
afm agent loop add --every 5m --prompt-file ./periodic-check.txt
afm agent loop add --every 5m --prompt-file - < ./private-check.txt
afm agent loop list
afm agent loop stop --loop-id L1
```

Supply exactly one of `--prompt` and `--prompt-file`. `--to` accepts an alias
(with or without `@`) or participant ID; omitting it targets the authenticated
agent. Agents can list and stop only loops they created. The human can inspect
and stop every loop. The server derives caller identity from the invocation's
mesh token; it does not trust an actor in a request body.

Create and stop commands return JSON and accept `--request-id` for retries.
Reuse the same request ID and arguments after an uncertain response to avoid
creating a second schedule. A different intent needs a new ID. Alias resolution
is saved at creation, so later alias changes do not redirect the target.

## HTTP API and recovery

The agent endpoints are `GET /api/agent-mesh/loops` and
`POST /api/agent-mesh/loop`, authenticated with the invocation's bearer token.
The dashboard uses the corresponding `/api/mesh/loops` and `/api/mesh/loop`
endpoints with the usual same-origin mutation checks.

An add request has this shape:

```json
{
  "request_id": "periodic-log-check-1",
  "action": "add",
  "target": "@watcher",
  "every": "5m",
  "prompt": "Check the latest logs",
  "until_stage": "build"
}
```

`target` is optional for agents and required for the human. `until_stage` is
optional. The dashboard also supplies `origin_stage`, the Feed it uses; an
agent's origin is derived server-side. Stop uses
`{"request_id":"stop-log-check-1","action":"stop","loop_id":"L1"}`.
List returns `{"loops":[...]}`. Each loop has `id`, `creator_id`, `target_id`,
`every` and `status`, optional `origin_stage`/`until_stage`, and either
`next_tick_at` while active or `end_reason` when stopped/ended.

Creation, delivered ticks and explicit stops are saved in `events.jsonl`.
A tick's message and slot cursor commit together; replay cannot recover one
without the other or deliver the same committed slot twice. Implicit end
reasons are derived from current run, stage, target and conversation state.
See [journal compatibility](collaboration-journal.md#loop-records).

Whole-flow [repeat](flow-reference.md#repeat-run-successful-flows-again) creates
a fresh run after a successful flow. Loops belong to the old run and do not
carry into the next cycle. Configure the new run's agents to create any loops
that cycle needs.
