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
helper's notes and temporary files. A helper in the host's read-only sandbox
(the `codex-as-claude` adapter) cannot write it — nor anything else — so its
prompt says so and asks for notes in its answer instead. This follows the
environment each turn runs in: a task admitted on the host and resumed in a
container is told it can write, and a finished task's badge shows the
guarantee its last turn ran under.

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

## Declare a team in flow.yaml

Add `team:` at the top level of `flow.yaml` to start run agents with the run,
without clicking **Add run agent**:

```yaml
team:
  - id: arch
    profile: rev-arch
    title: Architecture
    instruction: Review changes for simplicity and duplication when asked.
  - id: qa
    profile: qa
    title: QA
```

afm adds them in the listed order before any stage starts, so the order decides
who gets `@qa` and who gets `@qa-2` when titles collide. They are ordinary run
agents (see [Team conversations](#team-conversations)): idle until someone
writes to them, and they never keep the run open on their own. `id` names the
declaration, not the agent: it is how a resumed run recognizes an agent it has
already added.

On resume, afm reuses every agent the run already added and adds only members
with a new `id`, so stopping with Ctrl+C and resuming never adds an agent twice.
A member whose `title`, `instruction` or `profile` changed since keeps its saved
version; afm logs a warning and writes a `team_warning` notice: *"changed in
flow.yaml; using the saved version, the new one applies to a new run"*. A member
removed from `flow.yaml` keeps its agent until the run ends.

With collaboration enabled, every member the run has not added yet needs a
profile in `collaboration.profiles`: an unknown one stops `afm run` at preflight.
An agent the run already added keeps the profile it was created with, so a
resume still works after that profile is removed from config. `afm validate`
checks the flow as a fresh run: it reads no run journal, so it requires a
configured profile for every member. A profile that is unavailable when the run starts is logged, reported as
a `team_warning` notice and skipped, and the stages run anyway. If the run's
journal cannot be read or written while the team is being added, the run stops
before any stage starts. When collaboration is disabled, `afm run` and
`afm validate` only warn that the team will not be added, and the flow runs
without it, as with `assistants:`. A `team_warning` notice shows in the
dashboard's **Full feed** as a warning line naming the member and the reason,
also after a reload.

Full example: [`examples/agent-team-review/`](https://github.com/akopichin/afm/tree/main/examples/agent-team-review)
— a stage agent sends its change to two reviewer agents from `team:`, waits for
both answers with `afm agent wait`, fixes the valid findings, and then passes a
`verify:` gate.

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
A paused task with work waiting — a message it has not read yet or a turn it was
in — keeps the run open until you continue or cancel it.
If a required helper cannot pass, **Waive…** lets a human unblock the stage by
recording a reason. Required tasks with `needs_changes` or `inconclusive` do not
pass the gate; a `pass` verdict does.

Your conversation with a stage's agent lives in the stage's **Feed**: write a
**Message** to the stage agent from its composer (see
[Message](dashboard.md#message)); replies appear in the same feed, and authors are
named as in the Team roster. Posting does not change the stage's status. A post normally waits until the
stage agent next reads its inbox or ends its turn. Agents are asked to answer
every message from you, even with a short status update, but this is not
guaranteed: if a stage finishes without replying, the post is marked **Not
answered** and the feed gets a notice. A post to a stage that has already
finished does not reach its agent, though it still reaches anyone you mention.
A post that mentions someone who can no longer read it — a stage or helper that
has finished — is refused with the reason instead, and nothing is sent.
While the stage is running, you can turn on **Interrupt agent** before you send
a new message to the stage agent.
If the agent is working, it stops its current turn, answers your post as a
question with `afm agent reply`, and then continues. It does not treat the post
as a revision of its plan. If the agent has not started yet, for example during
a `script_before` hook, the question is in its first prompt instead. Until the
agent replies, the question is repeated in each new prompt, also after a
restart. For any other status — and for a reply, which stays in the
conversation it answers — the switch is not shown. Name other agents too (`@`,
or a pick in the People panel), such as run agents, helpers or other stages'
agents: they receive the post together with the stage agent, in the same
conversation; the post shows who it went to (`→ QA, DevOps`), and their replies
to it appear under their own names. Other agents that have finished cannot be
sent to. The stage's own agent, once the stage has finished, can still be named
on purpose: the post is recorded in its conversation and reaches the others
named, but the stage's agent does not read it. The dashboard has no separate Discussion tab and no proposal cards; agent
proposals are still recorded by the run. In the Side agent, type `@` to choose a
configured helper role from the suggestion menu. Mentions name roles, not
existing task instances.

## Team conversations

The run-wide **Team** tab appears after the Agent Mesh probe succeeds, even if
stage collaboration is disabled. A first probe that returns 404 hides Team;
the stage Feed's People panel has its own collaboration gate. Team opens on
**Messages → Conversations**. Switch the list to **People** to find run agents
and the Side agent under **This run**, or stage agents in flow order under the
collapsible **Stages** group, with helpers beneath their stage agent. Working
run and Side agents appear first; the remaining run agents keep their roster
order. People rows show each `@name`, status, and presence dot. Search filters
the current list by conversation topic, member, handle, or message preview, or
by name and handle in People. The list switch also works with the arrow keys.

Conversations are ordered by the latest saved message sequence, with empty
conversations after those with messages. Each row shows its topic (or member
names), members, the latest message's author and plain-text preview, and its
relative time. A successful send moves its conversation to the top immediately;
the saved, redacted preview replaces the temporary **Message sent** label when
it arrives. Incoming messages reorder rows without changing the selected
conversation. **Board** and **Activity** are run-wide tabs above the conversation
header, so neither depends on the selected conversation.

To ask the Side agent about the run, select it under **This run** and send a
message such as `Summarize the latest test failure`. Its handle is `@side`; a
stage agent or helper can address the same participant with
`afm agent send --to side --kind request --body "Summarize the latest test failure"`.
You can also include the Side agent in a stage Feed message by selecting it in
the **People** panel. Each message it receives joins its single conversation,
one turn at a time. The answer appears as a linked reply in the original Team
or Feed conversation. While it works, the Side agent modal names the sender;
**Stop** there applies only to messages started from that modal. A failed turn
shows **Could not answer** in the source conversation; if that conversation
cannot accept a reply, its delivery is marked undeliverable. See
[Side agent](dashboard.md#side-agent-a-free-form-chat-alongside-the-run) for
its conversation and availability settings.

Drag the divider at the roster's right edge to make more room for the roster or
conversation. You can also focus the divider and use **Left** or **Right** to
resize in 16 px steps; double-click it to restore the 260 px default. The roster
can be 200–560 px wide while leaving at least 320 px for the conversation. Its
width is saved in the browser separately from the stage Feed's People panel, and
survives reloads and tab changes. If the Team area shrinks, the displayed width
is limited to the space available; your saved width returns when it grows again.
If browser storage is unavailable, resizing still works, but the width is not
retained.

Select a person to read or send an untitled direct message; the first send opens
it, and selecting that person again returns to the newest open untitled direct
conversation. A titled conversation with the same person remains separate.
Use **New conversation** above Conversations to choose one or more participants
and, optionally, a topic. Stages and helpers that have already finished are not
offered. An untitled one-person conversation reuses the existing direct message;
a titled conversation stays distinct. **Edit topic** on a selected conversation
can rename or clear its topic, including after the conversation is closed. A
concurrent edit shows a conflict so you can review the latest topic and retry;
clearing falls back to member names. Topic changes survive reload without moving
the conversation in the list. The titled task conversation created by **Add run
agent** remains in Conversations; selecting that agent in People opens its
separate untitled direct message.

In a conversation, type `@` to address only some of its members; without a
mention the message goes to everyone in it who can still answer. Members that
have already finished are named above the composer and left out; when nobody can
answer any more, sending is turned off. When two agents share a name, the `@`
menu inserts a handle that addresses only the one you picked. The **Messages**,
**Board** and **Activity** tabs work with the arrow keys. A conversation opens at
its latest message, and a reply quotes the message it answers.

**New to you** shows the number of conversations with new messages from someone
else. Turn it on to show only those conversations; an empty filter says **You're
all caught up**. A marked row stays marked while you read older messages, and
clears when its incoming messages are rendered at the bottom of the visible
Messages view. Your own sends do not clear an earlier marker. These markers are
local to this dashboard page: its first successful roster snapshot sets the
baseline for the run, switching away from Team keeps that baseline, and a page
reload starts a fresh one. They do not acknowledge messages to Agent Mesh.

On narrow screens (760 px or less, or when Team has less than 560 px), Team shows
the list first, then the selected conversation. Use **Back** to return to the
list; the resize divider is hidden. Wider layouts show both panes and retain the
keyboard-resizable divider.

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

When a run agent's turn ends with a result, afm posts the result's summary as a
reply to the message that woke that turn — the newest message it was handed — in
that message's conversation. The reply is plain text: the summary, cut to fit a
message, then the verdict, the number of findings and the task and turn whose
full result it is. Team and the stage feed mark it with a **turn result** tag. A turn result does not wake its recipient: it
waits in the inbox and is handed over at the recipient's next turn, so it does
not start an idle run agent, give a stage an extra turn, resume an agent that
waits for a message (`addressed_message`, in one conversation or any), or keep
the run open. An agent that waits for an answer to that very message
(`reply_to`) is resumed by it. Turn results waiting in an inbox have their own
limit of 200, so they never take the room an ordinary message needs. A turn's
prompt carries up to 64 KiB of messages: first whatever an interrupted turn was
handed, then the messages that wake the agent, and only then turn results, so a
pile of results never delays the message that starts the turn. What does not
fit waits for the next turn or `afm agent inbox`. A stage
that finishes without reading one marks it undeliverable without listing it as
unanswered. afm skips the reply when the agent already answered the waking
message itself during the turn; a question to someone else or a reply to a
different message does not count. A turn with no waking message and no awaited
request left open by an earlier turn posts nothing.
If the reply cannot be delivered — the conversation is closed or full, the
recipient's inbox is full, or the recipient has finished — the result is still
recorded, the agent goes idle as usual, and the feed shows *"turn result of task
… not delivered: <reason>"* — the feed of the addressed stage, else of the
agent's own stage, else of the stage its conversation was opened from. The same holds for a delegated child's result
mirrored to its parent, which is a turn result too. When the waking message was
your post to a stage agent that named the agent too, the reply appears as its
answer, so the post is not shown as **Not answered**. Run
agents are told this in their prompt; to continue a conversation they write
explicitly with `afm agent send` or `afm agent reply`.

The human is a member of every conversation (except some opened before this
rule): one an agent starts with `afm agent send`, the message telling an agent
about a board task, and the thread where a delegated child's result reaches its
parent. You read along and can answer in the conversation; you are not addressed
by its messages and your inbox does not fill with them. The human does not count
toward the limit of 8 agents per conversation. An agent that writes to the same
colleagues again continues their conversation, also one opened before you
joined every conversation, so resuming an older run does not split it. Your
conversation with a stage's agent is never picked for this: what agents say
among themselves stays out of it. A conversation you start from a stage's feed
is that feed's own: two feeds writing to the same agent at the same time get a
conversation each, and an older conversation of those agents you are not a
member of is not reused for your message.

Each stage's **Feed** shows its conversations: those opened from that stage (by
its agent or one of its helpers, from its feed's composer, or by adding a run
agent from its People panel), those its agent or one of its helpers takes part
in, and your conversation with its agent. See [the stage feed](dashboard.md#conversations-in-the-stage-feed).

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
same question twice. A `reply_to` wait resolves on a reply from a recipient of
that message: you take part in every conversation, but your reply to a message
addressed to a reviewer does not count as the reviewer's answer (your reply to a
message addressed to you does). `afm agent wait --condition addressed_message` without
`--thread-id` waits for the next message anyone sends the agent. A `task_result`
wait on a run agent resolves on the first result after the wait was registered,
and a `stage_status` wait resolves as soon as the stage is at that status. A wait
on an unknown message, task or stage, on a closed thread, on a stage that already
finished in another status, one only the waiter itself could satisfy, one for an
answer nobody can send any more (every recipient of the message, or every other
member of the thread, has finished or gave the message up as undeliverable), or
one that would close a cycle of agents waiting on each other is refused. A wait
registered while someone could still answer ends the same way once they can no
longer do so: when every recipient of the message (or every other member of the
thread) has finished — a helper or run agent failed or was cancelled, a stage
finished without answering — the wait is cancelled with a reason naming them,
and the waiter continues; a stage parked in `awaiting_peer` gets its next turn
as "wait ended". A restart checks this again. A wait whose
producer is not known yet (a board key nobody has published, a thread not opened
yet) needs `--deadline-seconds`. When a run is interrupted (Ctrl+C), pending waits
stay pending and their deadlines keep running after `afm run` resumes it; only a
run that finishes closes them. A rejected call reports why
(for example an unknown recipient) instead of a generic server error.

Each helper or run-agent turn is numbered in the run's journal together with
the messages it was handed, before its process starts. A turn cut short by
Pause, Ctrl+C or a crash, before its outcome was recorded, is retried with the
same messages; a turn whose outcome was recorded does not get them again.
`turn-inbox.json` in the task directory is only a copy for inspection, removed
when the turn's outcome (a result, a wait, a delegation) is recorded. Do not
resume a run with an older afm: it reads that copy instead of the journal, and
if afm stopped between recording a turn's outcome and removing the copy, the
older binary would hand the turn's messages over again.

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

A stage's [Feed composer](dashboard.md#writing-to-a-stage-from-its-feed) sends a
**Message** — one message to one or several participants, the stage's agent by
default — or attaches a configured helper through a preview. Its **Revise** mode
continues to send stage feedback through the existing revision path. In the Side agent, an `@` mention records a role with
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
helper view to inspect its final text, when available. The free-text fields
`summary`, `evidence`, `suggested_change` and `limitations` may be either a
string or an array of strings; anything else in them is still a protocol error.

A run agent is a standing role, so a turn with an invalid answer does not end
it: the agent goes back to idle (its status says the last turn failed and why,
with secrets masked and the text cut to 2 KiB), and the message that woke the
turn gets a reply such as *"QA could not produce a valid result: invalid result
— …"*, which also answers a colleague waiting for that reply. Write to the
agent again to retry. A turn's reply, whether a result or such a failure, also
answers each request an earlier turn ended waiting on (`afm agent wait`, or a
delegation) while its sender still waits for that reply (`reply_to`): a turn
woken only by a colleague's passive result still answers the request it was
working on. If a reply cannot be delivered (the
conversation or an inbox is full), the requester's `reply_to` wait for it is
cancelled with the reason, unless another recipient can still answer. A stage helper fails as before
and can be retried from its view. In the People list and in a conversation with
it, a finished agent shows how it ended, for example *"task … failed: invalid
result — …"* or *"task … was cancelled by user"*.

A turn that ends by waiting for a colleague (`afm agent wait` answered
`yield`) needs no answer: the task waits in `waiting_peer` and continues in a
new turn when the reply arrives, even if it arrives while the agent process is
still exiting. A stage agent works the same way: when a wait its turn yielded
on is answered, or ends without an answer, before its process exits, the stage
continues in a new turn instead of finishing or being retried as incomplete.
An answer the agent already read in that turn (with `afm agent inbox`) is not a
reason for another turn. Every turn's prompt lists the requests the stage agent
sent in earlier turns: each with its answers, its open wait, or the status and
reason of a wait that ended without an answer (cancelled, expired). Requests
still waiting come first, then the newest others; the list keeps at most 20
requests and about 24 KiB of text, shows the start of each request and answer
(`afm agent inbox --all` has the full text) and says how many requests and
answers it left out. A continuation that is not a resumed agent session also
gets the earlier turns' actions, also when afm restarted before its agent
started, so the agent goes on where it stopped instead of asking its
colleagues again.
The park remembers which waits the turn ended for, so after a restart the stage
continues on the same rule, also when one of them was answered by a turn
result and another is still open. If you pause, continue or revise the stage
while that process is exiting, your action wins: the old process neither parks
nor completes the stage, a revise of its turn starts exactly one feedback turn,
and a continuation decided for an earlier park never resumes a later one. A
parked stage (`awaiting_peer`) can be paused too: it keeps the waits it parked
for, an answer that arrives while it is paused waits, and **Continue** resumes
it if an answer is there (or keeps it parked). If afm stops after `afm agent
wait` answered `yield` but before the stage was parked, the next `afm run` parks
it on those waits instead of rerunning the agent or finishing the stage from a
completion marker the turn left behind.

## Add a stage during a run

When available, **+ Stage** in the stage rail opens a dialog for a new dynamic
`agents: [auto]` stage. Enter its ID, command, and prompt, then choose **Append**
or **Insert before** an eligible stage. The dialog previews dependencies and
placement before **Add stage** commits the change. A committed stage appears in
the rail on the next status update.
