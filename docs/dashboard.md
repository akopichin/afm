# Web dashboard

On startup (if `server.open_browser: true`) the dashboard opens; otherwise its URL
is printed to the log.

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
  *selected stage's own* event history, capped at its last 200 events) plus one
  contextual tab for the selected stage — live/actionable, or completed with a
  plan or Q&A history to reopen — and, when collaboration is enabled, a
  **Discussion** tab. When Agent Mesh is available, a run-wide **Team** tab
  opens the participant roster and conversations (see
  [Team conversations](collaboration.md#team-conversations)). The workspace also has a permanent
  **Full feed** tab pinned to the far right of the tab row, showing the whole
  flow's last 1000 events across every stage. Feed always reflects whichever
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
- **Helpers and Discussion** — attached helper tasks appear beneath their stage.
  Select one to see its result and controls, including a **repaired** badge for an
  answer whose JSON needed repair or the raw answer behind a protocol error when
  available. The **Discussion** tab shows stage
  messages and pending agent proposals; **+ Stage** can add a stage to a live run.
  See [Helpers and collaboration](collaboration.md).

## Side agent — a free-form chat alongside the run

Open the **Side agent** — a large centered modal over the dashboard — to talk
directly to the same agent your flow uses (`client.command`), in the same working
directory and environment as the flow's stages. Use it to ask questions, probe the
project, or run a quick command-style exchange *without* touching the run.

- **Open it** with the **Side agent** button in the header, or press **Cmd/Ctrl+`**
  (the backquote key, recognised physically so keyboard layout doesn't matter).
  Focus moves to the message field when it opens. Opening the window by itself
  does not call the model; **Escape** closes it. The header button carries a
  small indicator — it lights while a reply is in flight and
  flags an unseen result once one arrives while the window is closed.
- **One conversation per run, one request at a time.** The chat keeps a single
  conversation tied to the run; you can't fire a second message while one is still
  working — send it after the current reply lands (or **Stop** the current one).
- **Independent execution.** The side chat has its own journal and its own
  cost accounting; it is *not* part of any stage. It never appears in a stage's Feed,
  never changes a stage's status, and never triggers stage timers or lifecycle hooks.
  An error in the chat cannot fail a stage. When Agent Mesh is available, the Side
  agent also appears in Team.
- **The agent outlives the HTTP request.** Once a message is accepted the agent runs
  under afm's own lifetime, not the browser request — closing the window or reloading
  the page does **not** interrupt it. Only **Stop**, `Ctrl+C`, or afm shutting down
  does. Reopening the window catches you back up on whatever ran while it was closed.
- **afm assembles the context, with an explicit budget.** Each message is a fresh
  agent call; afm rebuilds the conversation itself rather than relying on the agent's
  native resume. The assembled prompt has a hard byte budget — exceeding it is a
  `context_limit` error (your history and draft are preserved), never a silent drop of
  older turns.
- **File comments can be attached from the Docker file browser.** Choose
  **Without pause** while commenting on a file, then **Send to side agent** to open
  this chat with the comments attached. Write a message and send it to deliver both
  the comments and your request. See [File comments without pausing](#file-comments-without-pausing-docker-mode).
- **Mention a helper role.** When collaboration is available, type `@` in the
  message field to choose a configured role. A mention is recorded with the
  message; it does not attach a helper by itself. See
  [Helpers and collaboration](collaboration.md).

The side agent is on by default whenever the dashboard is up. Disable it with a
toggle resolved **flow `side_agent` > env `AFM_SIDEAGENT` > config
`side_agent.enabled` > default ON**:

```yaml
# .afm/config.yaml
side_agent:
  enabled: false        # turn the side agent off for this project
```

```yaml
# flow.yaml (top level) — overrides config and env for this flow
side_agent: true
```

`AFM_SIDEAGENT=0` disables it for a single run (and `=1` re-enables), overriding the
config but not an explicit `side_agent` in `flow.yaml`. When it is off the manager is
never created: the `/api/side-agent*` routes are not registered, the `side_agent`
capability reports `false`, and the header **Side agent** button is hidden.

Because it runs an arbitrary agent command with full project access, its endpoints
require a trusted local client. This loopback boundary does **not** depend on the
toggle: on a native host afm **always** binds the dashboard to `127.0.0.1`, and in
Docker mode the port is **always** published loopback-only (see [Docker](docker.md)) —
the dashboard also exposes unauthenticated mutating stage/flow APIs, so the bind is
unconditional regardless of whether the side agent or file browser is enabled.
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

The plan renders as proper markdown (lists, tables, blockquotes and code stay intact),
and comments anchor to the **sub-element** you clicked — an individual `<li>` or table
row, not just the whole block. Keyboard: the plan body is navigable (↑/↓ move between
commentable elements, Enter opens the form); the `## Assumptions` / `## Acceptance
Criteria` sections are collapsible. The same per-element commenting works on an agent's
**question** in the Communication channel.

## Sending a note to a running stage

While a stage is actively `running`, a messenger-style composer is pinned to the
bottom of its **Feed**. With Agent Mesh available it has **Revise** and **Message**
modes; **Revise** is selected by default and keeps the existing note behavior:

1. Select the `running` stage and open its **Feed** tab. A note input sits at the
   bottom: a growing textarea, an **Attach** button, and a send button. It
   appears only for a `running`, non-script stage.
2. Type your note and send it — click **Apply Revise** when Agent Mesh is
   available (otherwise the send button), or press **Cmd/Ctrl+Enter**
   (plain Enter inserts a newline). **Attach** lets you paste or upload an image,
   and in Docker mode reference a project file.
3. The agent finishes its current step, then receives SIGINT (a graceful interrupt,
   not a kill); the stage moves through `revising` and restarts the same phase
   (planning/implementation/review/autonomous) with your note folded into its
   context, then continues toward `done`.
4. Your note appears in the Feed as a right-side bubble carrying its text, alongside
   the agent's own messages (it is not clickable — it's a record of what you sent).

Choose **Message** to contact a Team participant without revising the stage. Type
`@` and pick someone under **This run** or **This stage**; sending uses an existing
direct conversation or opens one on the first message. **Attach to stage** lists
available configured roles and opens an attach preview before sending. The message
view shows its conversation and delivery status. A failed send leaves the draft
available to retry. See [Team conversations](collaboration.md#team-conversations).

### People panel

With Agent Mesh available, the stage **Feed** has a **People** panel on the right:
everyone this stage can talk to, in three groups — **This stage** (the stage's
agent and its helpers), **Run agents** (run agents and the Side agent) and
**Other stages** (in flow order, each with its helpers). It is the Team roster
with the selected stage on top; conversations stay in the **Team** tab.

- Each row shows the person's `@handle`, what they are doing now and a presence
  dot. A chip next to the name shows the command they run: the stage's command
  for a stage agent, the profile's command for a run agent or helper. A run agent
  or helper also shows its read-only guarantee: **read-only** (an OS sandbox
  enforces it) or **no sandbox** (only the container or the prompt limits it).
  Both come from the server, as recorded when the agent was added.
- Click a person to put their `@handle` into the composer and address them, the
  same as picking them from the `@` menu; the composer switches to **Message**.
  You can do this while the stage is `running` and its composer is shown.
- A finished stage or task stays listed but is disabled (*Finished — can't
  write*). A stage that has not started yet can be addressed: it reads the
  message when it starts.
- **+ Add agent** opens the same dialog as **Add run agent** in Team.

On a narrower window the panel becomes a drawer: open it with the 👥 button at
the right of the stage header.

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
> buttons, **Pause**, and the pre-note item for `pending` stages.

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

## File comments without pausing (Docker mode)

When the Side agent is enabled, clicking a file line while the flow is running
offers **Without pause** alongside **Pause** and **Cancel**. Choose it to open the
line comment editor while the flow keeps running. Save comments on multiple lines
or files; the bar at the bottom of the file browser shows the draft count. Use
**Clear** to discard all drafts, or **Send to side agent** to open the Side agent
with the comments attached above its message field. The handoff clears the file
browser drafts. Type a message and press **Send** to deliver the comments with your
request; an attachment alone cannot be sent. You can remove the attachment with
its ✕ button before sending.

These drafts live only in the browser: closing and reopening the file browser
keeps them for the same run, but reloading the page or switching runs clears them.
They are not saved as review notes or delivered to a flow stage. **Without pause**
is hidden when the Side agent is disabled.

## Review notes — pause the whole flow and comment on files (Docker mode)

Where a Feed note redirects **one running stage** with free-text,
**review notes** let you pause the **entire flow**, walk the project source in the
[file browser](docker.md#project-file-browser), attach comments **anchored to
specific file lines**, and then hand the whole batch to a stage of your choice. It's
Docker-only.

1. Open the file browser and click a line of any file. If the flow is still running,
   a small confirm appears — **"pause the flow to write notes?"**. Confirming puts the
   flow into **review mode**: a banner appears at the top, every currently-active
   agent stage is gracefully paused (SIGINT), and new stages are held from starting.
   Scripts and stages that already finished are left alone.
2. Write a comment on the line — it's saved immediately and the line gets a marker
   dot. Repeat across as many lines and files as you like; the banner keeps a running
   note count. Each note is anchored to `(file, line)` together with the file's
   content hash, so if the file changes before the notes are delivered the agent is
   told the content drifted.
3. Click **Send notes** to open the review modal: it lists every note grouped by file,
   lets you edit/delete individual ones, and asks which **target stage** should receive
   them. Then:
   - **Inject** — the notes are rendered into that stage's `feedback.md` (exactly once,
     crash-safe) and the flow resumes: the target stage restarts with the notes in its
     context, every other paused stage continues where it left off.
   - **Cancel** — discards the notes and resumes the flow unchanged.

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
