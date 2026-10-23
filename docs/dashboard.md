# Web dashboard

On startup (if `server.open_browser: true`) the dashboard opens; otherwise its URL
is printed to the log.

## Following a repeated flow

When [repeat](flow-reference.md#repeat-run-successful-flows-again) starts the next
cycle, the dashboard closes briefly and starts again on the same port. An open
tab reconnects and switches to the new run automatically, without a manual
reload. Its Feed and stage histories show the new run's events; conversations
and unsent stage drafts from the previous run are cleared. With
`server.open_browser: true`, afm opens a tab only for the first cycle.

The layout is a **stage rail** on the left plus one **tabbed workspace** on the
right (there is no three-column layout or bottom progress bar).

- **Stage rail** — a vertical timeline of the run: numbered status nodes, each
  stage's `name` (or `id`), a human-readable status line, and, in the rail head, the
  run progress (`done / total`). Attached helper tasks appear beneath their stage;
  **+ Stage** is available when a live stage can be added. A stage using
  [AI-verify](verify.md) shows a small
  verify-status badge next to its status line; it's a durable field computed
  server-side from the stage's own notices, so — unlike the feed it used to be
  derived from — a completed verify outcome stays visible for as long as the stage
  exists, regardless of how much other event activity has happened since. (One
  exception: a stale `running` verify badge is hidden once the stage itself is
  terminal — `done`/`failed` — so it can't pulse beside a finished dot.) On narrow
  viewports the rail
  collapses into a slide-over drawer (open with the ☰ toggle next to the tabs).
  Run metrics (Started / Elapsed / Idle / Backoff) live in the header, center.
- **Workspace** — a permanent **Feed** tab (a messenger-style view of the
  *selected stage's own* event history, capped at its last 2,000 events) plus one
  contextual tab for the selected stage — live/actionable, or completed with a
  plan or Q&A history to reopen. In `team` mode, when Agent Mesh is available,
  run-wide **Team** and **Board** tabs appear beside each other. Team opens the
  participant roster and conversations; Board groups tasks by status (see
  [Board tasks](collaboration.md#board-tasks)). The workspace also has a permanent
  **Full feed** tab pinned to the far right of the tab row, showing the whole
  flow's last 10,000 events across every stage. Feed always reflects whichever
  stage is selected — including an old, already-completed one, which used to
  show up empty once enough later activity had pushed it out of a single
  flow-wide window. Clicking a `pending` or `done` stage stays on Feed; read-only
  history is not opened automatically. A completed stage that has a plan or an
  answered-question history still exposes that contextual detail tab, so its
  **Plan | Dialog** history is always one click away — you just have to click the
  tab, it never opens on its own. When a stage needs action the contextual
  tab is labelled by kind (**Approval / Question / Paused / Failed / Hook
  failed**) with a count and a glow, and the workspace auto-opens it — one
  active view at a time, filling the whole area. When several stages await
  action, prev/next navigation steps through the queue. If an action arrives
  while you're mid-task — typing in a note field, with an overlay open, or while
  the **Hold the agent while you type** pause is in flight — the workspace does
  **not** steal focus: the contextual tab appears and glows as a beacon, and the action opens
  automatically the moment you finish (blur the field, close the overlay), as long
  as it's still current. It is never silently dropped. Merely focusing a checkbox
  (such as the **Hold the agent while you type** toggle) does not count as typing,
  so a question arriving then opens right away.
- **Approval / Question views** — the plan with line-by-line review and inline
  comments, or the agent's question with answer options and a free-text reply, each
  with a fixed bottom action bar.
- **Dialog history** — while a question is active, answered history is collapsed by
  default so the current question stays in view. Expand it with **EXPAND HISTORY**;
  when there is no active question, the full history is shown automatically.
- **Folder icon (Docker mode only)** — opens the [project file browser](docker.md#project-file-browser).
- **Est. cost tile & Cost tab** — the header's fifth metric shows the run's estimated
  cost and opens a **Cost** tab with a per-stage/overhead/total breakdown; the stage
  rail shows a quiet per-stage cost. All of it can be switched off — see
  [Cost tracking](#cost-tracking) below.
- **Helpers** — attached helper tasks appear beneath their stage.
  Select one to see its result and controls, including a **repaired** badge for an
  answer whose JSON needed repair or the raw answer behind a protocol error when
  available. With collaboration on, your conversation with a stage's agent lives
  in its **Feed** (see [Message](#message) and
  [Conversations in the stage feed](#conversations-in-the-stage-feed)); **+ Stage**
  can add a stage to a live run. See [Helpers and collaboration](collaboration.md).

The **+ Stage** button is hidden when adding stages is disabled by
[`add_stage.enabled` or `AFM_ADD_STAGE`](config-reference.md#adding-stages-during-a-run);
it is not shown as a disabled button. In that state `/api/status` reports
`capabilities.add_stage: false`, and `/api/run/graph/preview` and
`/api/run/graph/commit` return HTTP 404. The setting takes effect when afm
starts or resumes the run.

## Team workspace

In `team` mode, Team appears when the dashboard's first Agent Mesh roster probe
succeeds; a 404 hides it. This gate is separate from stage collaboration. Under
**Messages**, the left pane opens on **Conversations**; switch to **People** to
find a participant.
Search applies to the visible list. The action above it is **New conversation**
or **Add run agent**, respectively. Conversation rows show the latest saved
message preview and relative time, and sort by message sequence rather than wall
clock time. On wide screens, the newest conversation is selected on first load
unless you choose something else. **Board** and **Activity** cover the whole run
and have no conversation heading.

A conversation can have an optional topic. Select a person for an untitled
direct message, or create a titled discussion with the same person without
merging them. **Edit topic** can save or clear the selected conversation's topic;
changes persist after reload and do not change message order. **New to you**
counts conversations with new incoming messages in this dashboard page, and
filters the list when pressed. Reading at the bottom of a visible conversation
clears its marker; reading while scrolled up does not. The first roster snapshot
sets the baseline, and reloading the page resets it. Leaving Team for the Feed and coming back keeps the
selected conversation, its unsent draft and its reading position. On narrow screens, select
from the list and use **Back** from the conversation; on wide screens, drag or
keyboard-resize the divider. See [Team conversations](collaboration.md#team-conversations)
for messaging and participant details.

Answers to agent requests are linked the same way as in
[Run conversations](#run-conversations): the latest unanswered request in the
selected conversation is preselected as the reply target, so the agent's
`reply_to` wait resumes. Cancel the target to send a plain message, or choose
**Reply** on another message.

## Run conversations

In `assistants` mode, the **Run conversations** button in the workspace tabs
opens a right-hand panel for conversations that include you and have no stage
origin or stage participant. This includes conversations started by run agents;
conversations involving a stage agent or stage helper stay in the stage Feed.
The panel starts closed and never opens itself. It is available across Feed,
Plan, Cost, Full feed, and Assistant views when Agent Mesh is available; changing
views keeps the selected conversation and draft. There are no People, agent
creation, or board task controls in this panel.

On wide screens the panel shares the workspace area; drag its left edge to
resize it. On screens 1180 px wide or less, it opens over the workspace as a
drawer. **Back** returns from a conversation to the list. Close the panel with
its button or **Escape**; the narrow drawer also closes when you click outside
it. Keyboard focus returns to the opener.

Only an explicit message from an agent addressed to you raises the new-message
signal. The closed opener pulses and shows a count; the conversation row and a
newly viewed message briefly brighten. Your own sends, agent-to-agent messages,
and passive turn results do not raise it. The signal survives a reload and
clears only when the addressed message is visible in the selected conversation.
If it is below the visible history, **New message below** jumps to it. With
reduced motion, the accent remains briefly without animation.

When an agent's latest unanswered request is selected, its reply target appears
above the composer. Sending your answer links it to that request so the agent's
`reply_to` wait can resume. You can cancel the target to send a plain message or
choose **Reply** on another message. A newer request does not replace the target
while you are typing.

See [Collaboration modes](collaboration.md#collaboration-modes) for configuration
and the controls available in each mode.

## Board tab

In `team` mode, the run-wide **Board** tab shows tasks in **Todo**, **In progress**,
and **Done** columns. Task cards are gray, blue, and green, respectively. Search matches a
task's key and text; the assignee filter can show one agent's tasks or unassigned
tasks. The header shows completed tasks out of all tasks. **New task** opens the
posting form, and selecting a card opens its text, assignees, and current reports.
New tasks start in Todo.

In a run with [typed tasks](collaboration.md#typed-tasks) (every run started by
this afm version), the form's **Assignee** picker can assign the task to one
agent at once, or leave it with **Nobody yet**; the agent is told about it
immediately. The picker, like the **Assign to** list in task details, offers
every reachable run agent, idle or paused ones included, and the agent of each
reachable stage that is working (running, planning, revising or retrying) or
waiting for a colleague (`awaiting_peer`). The Assistant and helpers are not
offered. A card shows each assignee with its own state —
**Assigned**, **In progress** or **Done** — so an assigned agent is visible
before it starts, and search also matches the latest progress or result. Task
details show the title, description and latest note, every active assignment
with its result (or its progress when there is no result yet) and artifacts,
and a **History** of released and
reopened assignments. As the user you can **Assign** a task that is not Done,
**Release** an active assignment that is not done, and **Reopen** the task,
with an optional **Reason**; the column itself still follows the agents'
assignments. If the task changed meanwhile, details reload it (**Task changed,
reloaded**). When the answer to a change was lost (a network error or a server
error), clicking the same action again — the same action on the same task,
assignment and recipient — resends the original change with its request id,
even if the task changed in between; a different action or recipient is a new
change. Likewise, posting a new task again resends the lost create, assignee
included, as long as the title still gives the same task key; a title with a
different key is a new task. A successful post clears only the text that was
actually sent. Closing the form or the details forgets the pending retry.

In a run started by an older afm, statuses come from agent reports, the form
picks agents to **Notify**, and there is no control for a person to move a task
between columns.

Team keeps its own **Board** subtab. It shows the same color-coded task cards in
one mixed grid alongside non-task board entries, without status columns. Both
views update from the shared board. While it loads, the Board tab says so; if
loading fails, **Retry** is available. If a later refresh fails, the last loaded
cards stay visible with an out-of-date warning.

## Message times

Event rows in the stage **Feed**, **Full feed**, and Assistant history, and message
bubbles in **Team**, Run conversations, and stage Feed conversations, show the message time as `HH:MM`
in the host's 24-hour clock. Hover over the time, or focus its label or enclosing
interactive row with a keyboard, to see the elapsed gap (`+7s`, `+2m`, etc.). The
first item in a feed or displayed conversation block shows `+0s`. The time label
has a larger hover area, and switching labels does not move the row. Its native
tooltip and accessible name include the full host-local date and time, down to
seconds, and the gap. With touch alone, `HH:MM` remains visible; the gap needs a
pointer or keyboard focus, and the native tooltip depends on browser hover/focus
support.

The clock uses the time zone of the OS running afm, including its `TZ` override,
even when the browser is in another zone. In automatic Docker mode, afm passes
the launching host's zone to the container. When an IANA zone name is available,
historical daylight saving time follows that zone. If the name is unavailable or
the browser cannot use it, the display falls back to the host's signed UTC offset
reported by `/api/status`; this fixed offset cannot reconstruct historical
daylight saving changes.

## Assistant — a free-form chat alongside the run

Open the **Assistant** — a large centered modal over the dashboard — to talk
directly to the same agent your flow uses (`client.command`), in the same working
directory and environment as the flow's stages. Use it to ask questions, probe the
project, or run a quick command-style exchange *without* touching the run.

- **Open it** with the **Assistant** button in the header, or press **Cmd/Ctrl+`**
  (the backquote key, recognised physically so keyboard layout doesn't matter).
  Focus moves to the message field when it opens. Opening the window by itself
  does not call the model; **Escape** closes it. The header button carries a
  small indicator — it lights while a reply is in flight and
  flags an unseen result once one arrives while the window is closed.
- **One conversation per run, one request at a time.** The chat keeps a single
  conversation tied to the run. Messages sent to `@assistant` through Agent Mesh join
  this history too. Only one turn runs at a time, so you can send a new modal
  message after the current turn ends. Mesh messages wait for their turn.
- **Independent execution.** The Assistant chat has its own journal and its own
  cost accounting; it is *not* part of any stage. Its turns do not change stage
  status or trigger stage timers or lifecycle hooks, and an error in a turn cannot
  fail a stage. Mesh messages to and from the Assistant appear in their original
  Team or stage Feed conversations. When Agent Mesh is available, the Assistant
  appears in Team as `@assistant` in newly registered runs. Older runs keep their
  saved `@side` handle.
- **Messages from colleagues appear here too.** A message addressed to `@assistant`
  from Team, a stage Feed, or an agent starts a turn in this conversation. The
  incoming message is labelled **From <sender>**; while it runs, the header says
  **Replying to <sender>…**. The agent receives the sender and original Mesh
  conversation in its prompt, so it can answer there with `afm agent reply`.
  If it does, and writes no separate final answer, its linked reply is also shown
  in this modal. Otherwise, a successful final answer is posted back to the
  original conversation unless the agent already replied explicitly. A failed
  turn or unavailable conversation settles with **Could not answer** or an
  undeliverable receipt in the source conversation. Automatic turn results do
  not trigger an automatic answer back to their sender.
- **The agent outlives the HTTP request.** Once a message is accepted the agent runs
  under afm's own lifetime, not the browser request — closing the window or reloading
  the page does **not** interrupt it. **Stop** is available only for a turn started
  from this modal; it cannot stop a colleague's turn. `Ctrl+C` or afm shutting
  down also ends the work. Reopening the window catches you back up on whatever
  ran while it was closed.
- **afm assembles the context, with an explicit budget.** Each message is a fresh
  agent call; afm rebuilds the conversation itself rather than relying on the agent's
  native resume. The assembled prompt has a hard byte budget — exceeding it is a
  `context_limit` error (your history and draft are preserved), never a silent drop of
  older turns.
- **File comments can be sent to the Assistant from the Docker file browser.**
  Use **Send** on one comment or **Send all (N)**, select Assistant, and optionally
  add a message. The accepted request appears in Assistant history. See
  [File comments](#file-comments-docker-mode).
- **Mention a helper role.** When collaboration is available, type `@` in the
  message field to choose a configured role. A mention is recorded with the
  message; it does not attach a helper by itself. See
  [Helpers and collaboration](collaboration.md).

The Assistant is on by default whenever the dashboard is up. Disable it with a
toggle resolved **flow `side_agent` > env `AFM_SIDEAGENT` > config
`side_agent.enabled` > default ON**:

```yaml
# .afm/config.yaml
side_agent:
  enabled: false        # turn the Assistant off for this project
```

```yaml
# flow.yaml (top level) — overrides config and env for this flow
side_agent: true
```

`AFM_SIDEAGENT=0` disables it for a single run (and `=1` re-enables), overriding the
config but not an explicit `side_agent` in `flow.yaml`. When it is off the manager is
never created: the `/api/side-agent*` routes are not registered, the `side_agent`
capability reports `false`, and the header **Assistant** button is hidden.

Because it runs an arbitrary agent command with full project access, its endpoints
require a trusted local client. This loopback boundary does **not** depend on the
toggle: on a native host afm **always** binds the dashboard to `127.0.0.1`, and in
Docker mode the port is **always** published loopback-only (see [Docker](docker.md)) —
the dashboard also exposes unauthenticated mutating stage/flow APIs, so the bind is
unconditional regardless of whether the Assistant or file browser is enabled.
Remote/authenticated access is not supported in this version.

## Themes

The dashboard ships with three built-in themes; choose one with `theme:` in
`.afm/config.yaml`:

- **`graphite`** (default) — the product skin: material hierarchy on a cool graphite
  canvas, indigo interaction accent, mint success and amber attention, in both dark
  and light modes.
- **`goga`** — flat dark tech theme (teal accent, sans-serif, Goga wordmark).
- **`novacorps`** — a hi-tech theme (mint accent, monospace, scanline/neon decor).

Empty or unknown values fall back to `graphite` (an unknown value logs a warning to
stderr). The legacy `theme: coffee` value is still accepted as a compatibility alias
that normalizes to `graphite`. Light vs. dark mode is toggled inside the dashboard
itself and is independent of the theme choice. A fully custom skin can be supplied
via the top-level `skin_dir:` config option (a directory containing `index.css`),
which overrides the built-in theme.

The dashboard uses entrance animations as content appears. The reply-to-thought
quote chip and the line-comment quote mark glow in, taking their accent color from
the active theme. The newest feed message slides and fades in over 420 ms with a
brief accent glow that disappears by the end of the animation. All of them honor
`prefers-reduced-motion`: if your system requests reduced motion, they collapse to
effectively instant.

## Cost tracking

The dashboard surfaces the run's **estimated** cost in three places:

- an **Est. cost** tile in the header (the run total; an amber marker flags a coverage
  gap or that the accounting writer went unavailable),
- a **Cost** tab — the same per-stage / run-overhead / total table as `afm report`,
  each row expandable into a token-mix breakdown,
- a quiet per-stage figure in the stage rail once a stage has a priced record — while
  an active stage's first estimate is still pending, a small spinner sits in its place.

All three are a **list-price estimate**, not an invoice. To hide them entirely, set
`accounting: { enabled: false }` (or `AFM_ACCOUNTING=0`) — cost is still collected
into `usage.jsonl`, just not displayed. See [Cost accounting](accounting.md) for the
full model (token semantics, `pricing:` overrides, coverage, and the display switch).

## Inline plan comments

When a stage is in `awaiting_approval`:

1. Hover the plan — each commentable element (a list item, a table row, a heading,
   a paragraph, a code block) highlights. Click it — a comment form opens right there.
2. Write a remark — the target stays highlighted and shows a saved-comment indicator.
3. Click "Send revision (N)" — all comments are sent to the agent, each tagged with
   its source line (`Line N: …`).

The editor saves with **Add** (or **Update** for an existing comment) or
**Cmd/Ctrl+Enter**. **Escape** or ✕ folds it without discarding nonempty text;
unsaved edits show **Draft** when folded. Plan and question
comments survive switching tabs or stages within the run, but not a page reload or
new run. **Approve** stays disabled while the plan has unsent comments. Comments on
an older plan version can be shown, copied or dismissed; they are not sent with a
new version. Question answers and free-text drafts are likewise retained until sent.

The plan renders as proper markdown (lists, tables, blockquotes and code stay intact),
and comments anchor to the **sub-element** you clicked — an individual `<li>` or table
row, not just the whole block. Keyboard: the plan body is navigable (↑/↓ move between
commentable elements, Enter opens the form); the `## Assumptions` / `## Acceptance
Criteria` sections are collapsible. The same per-element commenting works on an agent's
**question** in the Communication channel.

## Writing to a stage from its Feed

A messenger-style composer is pinned to the bottom of a stage's **Feed**.

With collaboration off (`AFM_COLLABORATION=0`) it is the plain note field, without
modes, shown only while the stage is `running` (and not a script stage); it sends a
Revise. With Agent Mesh and collaboration on it has two modes:

- **Message** (selected by default) — write to people in the run without
  restarting the stage. Available for any non-script stage while the run is going,
  whatever the stage's status.
- **Revise** — the note that restarts the stage's agent with your text. The tab is
  there only while the stage takes a Revise: `running`, `awaiting_approval` or
  `awaiting_assistants`, and not during a review pause.

Each mode keeps its own draft when you switch between them. Drafts — both modes'
text, the chosen mode and the people picked as recipients — are kept per stage:
switching to another stage, the Feed following the run to the next stage, or
opening another tab and coming back restores them. They are not kept across a
page reload. When Agent Mesh becomes available only after the composer appeared,
it switches to **Message** unless you already chose a mode or are writing a
Revise note.

### Revise

1. Select the stage and open its **Feed** tab (with modes, pick **Revise**). The
   input is a growing textarea, an **Attach** button and a send button.
2. Type your note and send it — click **Apply Revise** when the composer has
   modes (otherwise the send button), or press **Cmd/Ctrl+Enter**
   (plain Enter inserts a newline). **Attach** lets you paste or upload an image,
   and in Docker mode reference a project file.
3. The agent finishes its current step, then receives SIGINT (a graceful interrupt,
   not a kill); the stage moves through `revising` and restarts the same phase
   (planning/implementation/review/autonomous) with your note folded into its
   context, then continues toward `done`. An agent waiting for its helpers
   (`awaiting_assistants`) has finished its turn; a Revise starts it again.
4. Your note appears in the Feed as a right-side bubble carrying its text, alongside
   the agent's own messages (it is not clickable — it's a record of what you sent).

Click **↩** on one of the agent's thoughts to answer it: the composer switches to
**Revise** and shows the quoted thought above the input (**↩ In reply to**).

### Message

The **To** row above the input lists who the message goes to:

- People you click in the [People panel](#people-panel) — each click adds a
  recipient chip; your text is left as it is.
- `@handles` you type in the text. Type `@` (or click **+** in the **To** row) to
  pick someone under **This run**, **This stage** or **Other stages**; their `@handle` goes into
  the text and shows as a chip. A handle typed by hand counts too.
- Nobody named — the stage's own agent, shown as a dashed **stage agent** chip.
  If it cannot read a message (the stage has finished, or it waits for its
  helpers and reads a note only as a Revise), the row says why and you must pick
  someone; **Use Revise** switches modes when Revise is available.

**×** on a chip removes that recipient everywhere it came from: the pick and
every mention of them in the text (a full id such as `@stage:docs` included). A
name nobody in the run has, an alias several people share, someone who can no
longer receive messages, or a picked person who has left the run is marked on
its chip and explained under the input; nothing is sent until you fix it. afm
never sends to someone else instead.

Sending posts **one** message to all recipients:

- A new message whose recipients include **the stage's own agent** is a post in
  your conversation with that agent; anyone else you named joins that same
  conversation. This is the path that can interrupt the agent and that marks a
  post the stage finished without answering (**Not answered**). While the stage
  is `running`, an **Interrupt agent** switch appears in the **To** row: switched
  on, the agent stops its current turn, answers your message, then continues its
  work; off, it reads the message at its next inbox check. You choose it for each
  send.
- Any other message — without the stage agent, or a **↩ Reply in conversation** —
  goes to exactly its recipients: into your open conversation with those people
  that this stage's feed shows (a reply stays in the conversation it answers), or
  a new one there, opened in the same step.
- Once the stage has finished, its agent is no longer the default recipient, but
  you can still name it (`@` or the People panel): the chip says *finished — post
  recorded, not read*. The post is kept in your conversation with that stage and
  reaches the other agents you named; the stage's agent does not read it.

**Cmd/Ctrl+Enter** sends; plain **Enter** adds a line. While there is text, a
hint under the input says so, and it is highlighted when you press plain Enter
on a message that could be sent.

**Interrupt agent** is reset after each successful send and kept after a failed
one, so a retry interrupts too.

A **↩ Reply in conversation** has its own draft: the text and `@mentions` you
were writing to others are neither its text nor its recipients, and they come
back when the reply ends.

A failed send keeps the draft and shows the
server's reason; sending it again is the same message, never a duplicate. Under
the input each recipient gets its own delivery state: *Accepted · waiting for an
inbox check* until afm actually hands the message to that agent's turn or inbox
read, then *Delivered*; *Paused · waiting for Continue*, *Unavailable*, and
*Answered* once that recipient replies.

**Attach to stage** in the `@` list lists available configured roles: picking one
adds an *Attach helper* chip, and Send opens the attach preview with your text as
the helper's assignment (it must be the only recipient). See
[Team conversations](collaboration.md#team-conversations).

### People panel

In `team` mode, with Agent Mesh and collaboration on, the stage **Feed** has a **People** panel on the right:
everyone this stage can talk to, in three groups — **This stage** (the stage's
agent and its helpers), **Run agents** (run agents and the Assistant) and
**Other stages** (in flow order, each with its helpers). It is the Team roster
with the selected stage on top; conversations stay in the **Team** tab.

To give the Feed more room, drag the divider at the left edge of the right-hand
People panel to the right; drag it left to widen People. Focus the divider and
press **Left** or **Right** to resize in 16 px steps. Double-click it to restore
the 280 px default. The panel can be 200–560 px wide, provided at least 320 px
remains for the Feed. Its width is saved in the browser separately from the Team
roster's width, so it survives reloads and tab changes. If the available space
shrinks, the panel becomes narrower without losing your saved preference; it
returns to that width when space grows again. If browser storage is unavailable,
resizing still works, but the width is not retained.

- Each row shows the person's `@handle`, what they are doing now and a presence
  dot. A chip next to the name shows the command they run: the stage's command
  for a stage agent, the profile's command for a run agent or helper. A run agent
  or helper also shows its read-only guarantee: **read-only** (an OS sandbox
  enforces it) or **no sandbox** (only the container or the prompt limits it).
  Both come from the server. For a live agent the guarantee is the one the
  current environment gives its command; if the agent was added under a
  different one (for example, a host run resumed in Docker), the badge's tooltip
  says so. A finished agent shows the guarantee it ran under.
- Click a person to add them to the composer's recipients: the composer switches
  to **Message** and the person gets a chip in the **To** row; click more people
  to write to several at once. You can do this while the stage's composer is
  shown (any non-script stage while the run is going).
- A finished stage or task stays listed but is disabled (*Finished — can't
  write*). A script stage is listed but disabled too: no agent reads its
  messages. A stage that has not started yet can be addressed: it reads the
  message when it starts. An agent you just added shows under its `@handle` and
  can be picked right away.
- Two buttons at the bottom add someone new: **Add run agent…** opens the same
  dialog as **Add run agent** in Team (an agent of the whole run, which can talk
  to every stage); **Attach helper to stage…** opens the stage's **Attach
  helper** dialog (a helper that works for this stage's agent). The second is
  shown only while the stage can take a helper.

At viewport widths of 1180 px or less, the panel becomes a right-side drawer:
open it with the 👥 button at the right of the stage header. The resize divider
is hidden; if it had keyboard focus when the layout changed, focus moves to the
👥 button. The open drawer takes the keyboard focus and keeps **Tab** inside it;
**Escape**, a click beside it or a pick closes it and returns the focus. A long
command is shortened with an ellipsis; hover it for the full value.

### Conversations in the stage feed

With Agent Mesh and collaboration on, a stage's **Feed** also shows its conversations,
between the stage's own events in time order: conversations the stage opened
(its agent or helpers wrote first, you started one from this feed, or you added a
run agent from this stage's People panel), conversations its agent or one of its
helpers is in, and your conversation with the stage's agent. Each message
appears once, as its bubble, with Markdown rendered. Each message shows its
author, the command they run, **→** whom it is addressed to, the time, a quote of
the message it answers (click the quote to jump to that message) and a **turn
result** tag on a turn result. A post of yours that the stage finished without
anyone answering is marked **Not answered** (the mark goes once a reply arrives).

Runs recorded before conversations lived in the feed keep their stage
conversation log: its posts — including helper results with the evidence they
cited, which reached no inbox — show as read-only bubbles in journal order, once
each, next to the newer messages. Such a bubble shows its author's role and the
cited ids as tags; an agent's answer to your post counts as addressed to you, so
**Author's work** keeps it. Until that log is read, such a post shows as a
line such as *"reviewer posted in the stage conversation"*.

- Filter the feed with **All**, **Conversations** (only the messages) or
  **Author's work** (only the stage's own events). Each chip shows how many
  rows that filter shows. Messages addressed to you stay visible under every
  filter.
- **↩ Reply in conversation** answers that message in its conversation: the
  composer switches to **Message**, the author becomes a recipient and a
  **↩ Reply in conversation** chip quotes the message, which is highlighted in
  the feed. The reply goes to that conversation's people only: people you picked
  before wait until the reply ends, and anyone you add during it is dropped with
  it. Naming someone who is not in that conversation, or the finished stage's
  agent, is an error on their chip and Send stays off — a conversation never
  gains members. One message is answered at a time — choosing another replaces it; **✕**
  cancels. It is told apart from answering a thought with Revise by its colour.
  It is offered while the composer is shown, on open conversations you are a
  member of, and while the author can still read it; if the conversation closes
  meanwhile, the reply ends on the next poll.
- A line such as *@qa is working* at the bottom shows who among the people in
  this stage's conversations, and its helpers, is in a turn right now.

The feed reads conversations by itself every few seconds, so a message appears
even when the live connection drops. It keeps the newest 2,000 conversation
messages of a stage; Team shows every conversation in full.

### Hold the agent while you type

A running agent can finish (or move on) before you're done typing a note. The
composer has a **Hold the agent while you type** toggle above the note input,
aligned to the right, for this:

- With it **on**, clicking into the note field pauses the running stage — the agent
  is put on hold and a banner shows *"Stage paused — it'll resume when you send."*
  Take your time; the agent won't advance past where it was.
- **Send** resumes the stage and delivers your note in one step. **Resume now** (or
  leaving the field empty and clicking away) un-pauses without sending.
- With it **off**, the composer behaves as above (Send interrupts the live agent, no
  pause).

The toggle is remembered across sessions. It reuses the ordinary pause and
note-delivery paths, so on resume the agent restarts the phase once with your note
in context. Only the stage you're writing to is paused — other running stages are
untouched.

At the `awaiting_approval` checkpoint you redirect the stage a different way — with
line comments and "Send revision" (see [Inline plan comments](#inline-plan-comments) above).

> An earlier version offered "Add a note for the agent" in the stage's kebab (⋮)
> menu; that item was replaced by this Feed composer. The kebab still hosts stage
> buttons, **Pause**, and the pre-note item for `pending` stages. **Pause** is
> offered while a stage is `planning`, `running` (not for a running script),
> `revising`, `retrying` or `awaiting_peer` (parked waiting for a colleague's
> reply); **Continue** returns a paused `awaiting_peer` stage to its wait, and it
> goes on by itself if the wait was answered meanwhile.

A message whose post the server refuses — for example because someone you named
can no longer read it — keeps its draft and shows the server's reason under the
input (*"@qa cannot read this post (…) — remove the mention and send again"*).

## Images in the feed (agent → you)

An agent can show you an image it produced — a chart, a rendered diagram, a
screenshot, a downloaded picture — right in the **Feed**. It publishes the file as
a stage artifact and references it by name; afm renders it inline.

1. The agent writes the image **atomically** to `$AFM_STAGE_DIR/artifacts/<name>`
   (a temp file, then `rename`), where `<name>` matches `[A-Za-z0-9._-]+` and ends
   in `.png` / `.jpg` / `.jpeg` / `.gif`. This contract is part of the agent's
   system prompt, so any agent can do it without extra configuration.
2. It then prints the marker on its own line in its narrative: `[AFM image: <name>]`
   (one marker per line; several images → several lines).
3. The Feed renders each marker as an inline image; the surrounding text renders as
   normal markdown.

The image is served read-only from that stage's artifact directory over
`GET /api/stages/<id>/artifacts/<name>` — addressed by stage + opaque name (never a
client-supplied path), format-checked (PNG/JPEG/GIF only), size- and
dimension-capped, and served with an `ETag` (revalidated, so an overwritten image
is never shown stale). External markdown images in agent text
(`![](https://…)`) are **not** loaded — a Content-Security-Policy and a renderer
rule keep the feed from fetching off-origin URLs.

## Stage buttons

A stage can declare named one-click actions in `flow.yaml`. Each button carries a
canned prompt; clicking it in the kebab (⋮) menu delivers that prompt to the stage's
live agent — the same graceful-revise path as a Feed note, but reusable with a single
click.

```yaml
stages:
  - name: build
    buttons:
      Run linter: "Run golangci-lint and fix every finding"
      Rebuild:    "Rebuild the project from scratch and make sure tests pass"
```

Menu order matches declaration order. Available while the stage is
`running`/`awaiting_approval` (not on `script:` stages).

## Adding a note before a stage starts (pre-note)

You can attach a note to a `pending` stage that hasn't started yet. It's spliced into
the agent's context on its **first** start, as part of the original task (unlike a
note to a running stage, which restarts an already-running agent). Use "Add note
(before start)" in the kebab menu of a pending stage; a 📝 indicator marks a stage
that has one.

## File comments (Docker mode)

In **Files**, click a line number in the **FILE** view to write a local comment.
**Save** or **Cmd/Ctrl+Enter** saves and folds it; **Escape** or ✕ folds nonempty
text without saving it, and the card then shows **Draft**. **Delete** removes a
comment. Clicking a line does not pause the flow, including when an external
review pause is already active. These comments are separate from review notes.

Use **Files / Comments (N)** to see the full text, file and line for every local
file comment and open its source. On narrow screens, **Comments (N)** opens this
list in a drawer. The file preview also keeps comments under
their lines, and files with comments show **● N** in the file tree, search results
and changed-files list. Comments survive switching files and **FILE / DIFF** tabs.
Leaving a file — another file, **DIFF** or closing Files — folds its open editor
into a card; unsaved text shows **Draft**. Closing and
reopening Files also restores the selected file, tab and scroll position in this run.
Reloading the page or starting a new run clears the local comments.

Use **Send** on a comment card or **Send all (N)** at the bottom of Files. The send
dialog previews the comments and lets you add an optional message and choose
**one** recipient. Available choices are the Assistant and reachable stage,
helper or run agents; when Assistant is the only choice it is selected
automatically. **Send all** sends one message to the chosen agent, regardless of
recipients selected on individual cards. The Assistant route works without Agent
Mesh. Sending does not pause or revise a stage.

Before sending, Files checks the source again. If a line changed or disappeared,
confirm the old quote or choose its current line; if a file is gone, confirm
sending without a current quote or delete its comment. A read error blocks Send
while keeping the text. The dialog also blocks packets over the 16 KiB Mesh or
64 KiB Assistant message limit. After the server accepts a request, only the
submitted versions are removed and **Accepted: N comments → Agent (not read yet)**
appears; this means acceptance, not that the agent has read or answered. A failed
request keeps the comments. **Retry** resends the same packet, while edits made
during the request remain as new drafts.

## Review notes — pause the whole flow and comment on files (Docker mode)

Where a Feed note redirects **one running stage** with free-text, the
flow-wide review pause holds the **entire flow** and can deliver stored,
line-anchored review notes to a stage. This path requires the Docker file browser.

The dashboard no longer starts a flow-wide review pause by clicking a file line.
An existing review pause started through the flow API remains supported: it shows
a banner, holds new stages, and gracefully pauses active agent stages. Review
notes stored through that API remain distinct from the local file comments above.
They are labelled **Review note · sent with Continue** in the dashboard. The
**Review notes** modal can edit and delete those stored notes.

When a review pause and its notes already exist, click **Send notes** to open the
modal. It groups notes by file, lets you edit or delete them, and asks which
**target stage** should receive them. **Inject** renders the notes into that
stage's `feedback.md` (exactly once, crash-safe) and resumes the flow: the target
stage restarts with the notes in its context, and other paused stages continue
where they left off. **Cancel** discards the notes and resumes without them.

The pause/notes/resume cycle is durable: it survives an `afm` restart mid-review, a
resume interrupted by a crash finishes on the next start, and while a review round is
open the ordinary controls (approve/retry/continue/dialog-answer) return
`409 flow_paused` so nothing races the review.

## Auto-approving a stage's plan

Set `auto_approve: true` on a stage to skip the human approval checkpoint entirely —
useful for CI runs where some stages need review and others don't:

```yaml
stages:
  - id: lint
    agents: [planning, implementation]
    auto_approve: true    # no human ever needs to click Approve for this stage
  - id: deploy
    agents: [planning, implementation]
    depends_on: [lint]    # still requires a human Approve — auto_approve not set
```

The plan is approved the instant it's ready, whether or not a dashboard is attached
and regardless of `--require-approval` (which normally fails a headless run with no
dashboard). If the dashboard is open, the stage's plan is still shown, with an
"Auto-approved" badge in place of the Approve/Revise buttons.

## Pausing a stage before it starts

Set `auto_run: false` on a stage to hold it at `paused` the instant it becomes
eligible to start (once `depends_on` are satisfied), instead of running unattended —
useful for a checkpoint you want a human to explicitly kick off (e.g. a stage that
commits/deploys):

```yaml
stages:
  - id: build
    agents: [planning, implementation]
  - id: deploy
    agents: [planning, implementation]
    depends_on: [build]
    auto_run: false    # waits for a human to click Continue before it starts
```

The dashboard shows a Continue button instead of the stage running on its own. This
only gates the stage's very first activation — once it's been through a pause/Continue
cycle, later retries (e.g. after a `failed`) run normally without pausing again.

You can also pause any `running`/`planning`/`revising`/`retrying` stage mid-flight
from the kebab (⋮) menu — the same "Pause" action, just triggered manually. The agent
gets a graceful interrupt (SIGINT), and Continue resumes it exactly where
recovery-on-restart would (`--resume` for agents that support it). Script-only stages
(`script:`) can only be paused via `auto_run: false`, before they start.
