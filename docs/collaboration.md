# Helpers and collaboration

Helpers are agent tasks attached to a stage. They have their own status, result,
and cost records. A helper can give advice, or it can be **required** to pass
before its stage finishes. Attach helpers from the dashboard or declare them in
`flow.yaml` to attach automatically when a stage starts execution. The Side agent
can record mentions of configured roles, but a mention alone does not create a
task.

## Configure roles

Add roles to global `~/.afm/config.yaml` or project `.afm/config.yaml`:

```yaml
collaboration:
  enabled: true                 # default; AFM_COLLABORATION=0 disables it
  max_depth: 3                 # default; 0 prevents helper creation
  limits:
    max_tasks: 20              # optional lifetime limit for this run
  profiles:
    reviewer:
      title: Code reviewer
      command: codex-as-claude
      instructions: Review the stage's changes and report concrete findings.
      delegate: false          # opt in to child tasks with true
```

Each configured profile needs its own `command`; it does not inherit
`client.command`. Profiles with the same ID in project config replace those in
global config. Only available profiles can run. On a native host, afm accepts
the Codex read-only adapter for helpers; inside a container, afm relies on the
container boundary and allows configured commands without an OS read-only
guarantee. Unavailable roles cannot be attached; the profiles API reports why
each one is unavailable.

`AFM_COLLABORATION` takes precedence over `collaboration.enabled`; collaboration
is on by default. Delegation depth follows `flow.yaml` > project config > global
config > default (3). A flow can override **only** the depth:

```yaml
collaboration:
  max_depth: 2
```

Depth 1 is a helper attached directly to a stage; deeper tasks are its children.
`delegate: true` permits a role to create children, subject to the depth and
whole-run `limits.max_tasks` limits. `max_tasks` counts every task of the run:
helpers and run agents you attach as well as their children. A flow cannot re-enable collaboration when
config or the environment disables it.

## Where helpers work

A helper or run agent runs in the project root, the same working directory as
the stage agent (`root_dir`, or the run's worktree). Its task context names
that directory, a **base revision** for `git diff <base>` (in a worktree, the
commit the branch forked from; otherwise `HEAD`), and its own scratch directory.
The scratch directory is also in `$AFM_SCRATCH_DIR`; it is meant for the
helper's notes and temporary files.

What stops a helper from changing project files depends on where it runs, not
on its prompt:

| Where the helper runs | Guarantee | Can it write to the project? |
|---|---|---|
| Host, `codex-as-claude` adapter | OS read-only sandbox (`-s read-only`) | No |
| Inside afm's Docker container | Only the container boundary | Yes |
| Host, any other command | None (afm does not admit these commands) | — |

afm compares the working tree before and after each helper turn. If it
changed, the stage feed shows *"The working tree changed during <agent>'s
turn"*. This is an observation: the stage agent works in the same tree, so it
does not say who made the change, and it never changes the helper's verdict.

## Attach helpers from flow.yaml

Add `assistants:` to an execution stage to attach configured roles when that
stage starts its implementation, review, or autonomous phase:

```yaml
stages:
  - id: build
    agents: [auto]
    assistants:
      - reviewer
      - profile: qa
        title: QA gate
        instruction: Check test coverage.
        mode: required
        trigger: on_completion
```

A single name (`assistants: reviewer`) or a single object also works. List order
is preserved. The object accepts `profile` plus optional `title`, `instruction`,
`mode`, and `trigger`. Defaults are `advisory` and `one_shot`; the mode and trigger
choices have the same meaning as in the dashboard below. For stages with planning,
helpers attach when execution begins, after plan approval. Retries and crash
recovery do not duplicate an attachment; a new revision can attach a fresh task.

Profiles must exist in `collaboration.profiles` when the run starts. An unknown
profile stops startup; a configured profile that is unavailable at attach time
is logged and skipped without stopping the stage. `assistants:` cannot be used on
`script:` or planning-only stages, and a stage cannot list the same profile twice.
When collaboration is disabled, afm warns about declared helpers and runs the
stage without attaching them. This also lets `AFM_COLLABORATION=0` temporarily
disable a flow's helpers.

## Use helpers in the dashboard

Open a stage's **⋯ → Attach helper…** menu, select an available role, and give
it an instruction. Choose **Advisory** for a result that does not block the
stage, or **Required** for a result that must pass before the stage finishes.
Attach helpers after the stage starts. A helper attached while its stage is
`pending` stays in `waiting_parent` and does not start when the stage starts;
a required helper in that state can prevent the stage from finishing.
Choose when it runs:

- **One shot** runs once.
- **At checkpoints** observes the stage agent's output and can run again when
  a checkpoint is reached.
- **On stage completion** can run earlier if enough stage output reaches its
  collector. If it is still active when the stage result is ready, it receives
  a final turn.

Attached helpers appear as child rows in the stage rail. Select one to see its
status, verdict, findings, delegation breadcrumb, and, for watchers, pass count.
The task view offers **Pause**, **Continue**, and **Cancel** while applicable.
If a required helper cannot pass, **Waive…** lets a human unblock the stage by
recording a reason. Required tasks with `needs_changes` or `inconclusive` do not
pass the gate; a `pass` verdict does.

The **Discussion** tab shows the stage's discussion and lets you post a human
message; replies appear there too, and authors are named as in the Team roster.
Posting does not change the stage's status. A post normally waits until the
stage agent next reads its inbox or ends its turn. Agents are asked to answer
every message from you, even with a short status update, but this is not
guaranteed: if a stage finishes without replying, the post is marked **Not
answered** and the feed gets a notice. A post to a stage that has already
finished does not reach its agent, though it still reaches anyone you mention.
While the stage is running, you can turn on **Interrupt agent** before you send.
If the agent is working, it stops its current turn, answers your post as a
question with `afm agent reply`, and then continues. It does not treat the post
as a revision of its plan. If the agent has not started yet, for example during
a `script_before` hook, the question is in its first prompt instead. Until the
agent replies, the question is repeated in each new prompt, also after a
restart. For any other status the switch is not shown. Type `@` to bring other
agents into a post, such as run agents, helpers or other stages' agents. Agents
that have finished are not offered. They receive the post together with the
stage agent, the post shows who it went to (`→ QA, DevOps`), and their replies
to it appear in this Discussion under their own names.
Pending agent proposals also appear there; **Accept** confirms a supported
proposal before its change is applied, while **Dismiss** discards it. In the Side agent, type `@` to choose a
configured helper role from the suggestion menu. Mentions name roles, not
existing task instances.

## Team conversations

The run-wide **Team** tab appears when Agent Mesh is available. Its roster lists
run agents and the Side agent under **This run**, stage agents in flow order under
**Stages** (with their live status and their helpers nested below), and existing
conversations under **Conversations**. Each row shows the `@name` used to address
that participant and what it is doing: a stage's status, or for a run agent or
helper whether it is **Working**, **Waiting** (for a colleague), **Idle** (between
turns), **Paused**, or **Done**; the presence dot follows the same state.

Select a person to read or send a direct message; the first send opens a
conversation, and selecting that person again returns to it. The **+** button in
the conversation header creates a group conversation after you enter a topic and
choose participants; stages and helpers that have already finished are not
offered. In a conversation, type `@` to address only some of its members; without
a mention the message goes to everyone in it who can still answer. Members that
have already finished are named above the composer and left out; when nobody can
answer any more, sending is turned off. When two agents share a name, the `@`
menu inserts a handle that addresses only the one you picked. The **Messages**,
**Board** and **Activity** tabs work with the arrow keys. A conversation opens at its latest
message, and a reply quotes the message it answers. On narrow screens the roster
opens from the **☰** button.

**Add run agent** starts a standing agent from an available configured profile.
Choose a role, enter an instruction, and optionally give it a title and a first
message. You can add several agents with the same role; each one is addressed by
an `@name` made from its title (or the role's title when there is none) — unique
in the run, so a second "QA" becomes `@qa-2` — and they work side by side. A run agent is a standing role: every message addressed to it
starts its next turn, and between turns it is idle — an idle agent never keeps the
run open, while one with an unread message does. An agent added without a first
message waits until somebody writes to it. Messages to a stage or helper that has
already finished are refused with a clear error instead of going unanswered. This
agent belongs to the run rather than one stage.

The **Board** tab (with a count of its tasks) shows keys agents have published
with their status, author, version, and artifact references. Use **Post task** to
put a task on the board: choose under **Notify** which working agents get a
message about it (all of them by default, at most 16 per task), so even an idle run
agent picks it up.
Each one gets its own message, recorded together with the task, so the run does
not finish before the agents read it. A recipient that has finished, or whose
inbox or conversation is full, is skipped; the task is posted either way.
Agents report on a task by publishing their own `<task key>/<name>` entry, shown
under the task; only a key's author can update that key.
**Activity** shows Agent Mesh events such as participants joining, messages,
waits, and board updates. Agent messages become available at the next turn or
when an agent reads its inbox; they are not injected into an in-progress response.
One turn's prompt carries up to 64 KiB of new messages and says how many more are
waiting; an agent can hold at most 200 messages it has not been handed yet. Your
own inbox is not capped that way; it lists the newest 1000 messages sent to you,
and older ones stay readable in their threads.

A stage agent does not finish while a message to it is still unread. If a
teammate writes while the agent is working and the agent ends its turn without
reading it, the stage does not complete: the agent gets one more turn with the
new messages in its prompt and a note to answer them, then finish. A stage gets
at most 5 such extra turns. A stage that finishes anyway with unread messages
(the limit was reached, or the stage failed) shows a notice listing them; those
messages are marked undeliverable, and a sender waiting for an answer to one of
them (`reply_to` that message, or `addressed_message` on its thread or on any
thread) has that wait cancelled and is resumed, so it is not left waiting for an
answer that cannot come and the run can finish. The same holds after a restart:
a stage whose agent had already finished its turn still gets the extra turn, and
a waiter whose wait was cancelled before it was woken is resumed.

Agents talk to each other with the `afm agent` CLI. `afm agent send --to docs
--body "..."` continues the sender's open conversation with exactly those
recipients or opens a new one, and prints its `thread_id`; recipients can be
`@name`s or participant ids. `afm agent reply --in-reply-to <message-id>` answers
in that message's thread. `afm agent inbox` lists only messages the agent has not
been handed yet (`--all` shows the whole history), so an agent never answers the
same question twice. `afm agent wait --condition addressed_message` without
`--thread-id` waits for the next message anyone sends the agent. A `task_result`
wait on a run agent resolves on the first result after the wait was registered,
and a `stage_status` wait resolves as soon as the stage is at that status. A wait
on an unknown message, task or stage, on a closed thread, on a stage that already
finished in another status, one only the waiter itself could satisfy, one for an
answer nobody can send any more (every recipient of the message, or every other
member of the thread, has finished or gave the message up as undeliverable), or
one that would close a cycle of agents waiting on each other is refused. A wait whose
producer is not known yet (a board key nobody has published, a thread not opened
yet) needs `--deadline-seconds`. When a run is interrupted (Ctrl+C), pending waits
stay pending and their deadlines keep running after `afm run` resumes it; only a
run that finishes closes them. A rejected call reports why
(for example an unknown recipient) instead of a generic server error.

Secret values are replaced before they are stored or shown. This covers message
bodies and artifact references, board statuses and artifact references,
discussion posts, a helper's title and instruction, every text field of its
result (`result.json` and the journal), and what the helper process writes:
`agent.log`, `agent.jsonl`, `agent.stderr.log` and the raw answer. The values
replaced are every value of the global and project `secrets.env`
and of `docker.secrets_file`, the values recipe and lifecycle-hook references
resolve to (including from the environment), provider credentials in afm's
environment, and the agents' mesh tokens. Values shorter than 4 characters are
left as they are: they cannot be meaningful credentials, and replacing them would
garble ordinary text.

A running stage's [Feed composer](dashboard.md#sending-a-note-to-a-running-stage)
can send a **Message** to an existing participant or attach a configured helper
through a preview. Its **Revise** mode continues to send stage feedback through
the existing revision path. In the Side agent, an `@` mention records a role with
the chat message; it still does not attach a helper by itself.

## Helper answers and protocol errors

Helpers must return one JSON object with `schema_version: 1` and either
`kind: "result"` (with a verdict and summary) or, for a role with `delegate: true`,
`kind: "delegate"` (with child tasks). afm includes this output contract at the
end of each helper's task context, including after any follow-up instruction.

If strict decoding fails, afm tries one mechanical repair of a single JSON
object, such as extracting an answer from a markdown fence or surrounding prose.
The repaired object still has to pass the same schema and verdict checks; repair
cannot change its verdict. An accepted result shows a **repaired** badge in the
helper view and a warning in the stage feed. If the answer still fails, the
helper gets a `protocol_error`. Open **Show the assistant's raw answer** in the
helper view to inspect its final text, when available.

A turn that ends by waiting for a colleague (`afm agent wait` answered
`yield`) needs no answer: the task waits in `waiting_peer` and continues in a
new turn when the reply arrives, even if it arrives while the agent process is
still exiting.

## Add a stage during a run

When available, **+ Stage** in the stage rail opens a dialog for a new dynamic
`agents: [auto]` stage. Enter its ID, command, and prompt, then choose **Append**
or **Insert before** an eligible stage. The dialog previews dependencies and
placement before **Add stage** commits the change. A committed stage appears in
the rail on the next status update.
