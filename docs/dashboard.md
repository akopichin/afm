# Web dashboard

On startup (if `server.open_browser: true`) the dashboard opens; otherwise its URL
is printed to the log.

The layout is a **stage rail** on the left plus one **tabbed workspace** on the
right (there is no three-column layout or bottom progress bar).

- **Stage rail** — a vertical timeline of the run: numbered status nodes, each
  stage's `name` (or `id`), a human-readable status line, and, in the rail head, the
  run progress (`done / total`). On narrow viewports the rail collapses into a
  slide-over drawer (open with the ☰ toggle next to the tabs). Run metrics (Started /
  Elapsed / Idle / Backoff) live in the header, center.
- **Workspace** — a permanent **Feed** tab (a messenger-style view of the real event
  log across all stages) plus one contextual tab for the selected stage. When a stage
  needs action the contextual tab is labelled by kind (**Approval / Question / Paused
  / Failed / Hook failed**) with a count and a glow, and the workspace auto-opens it —
  one active view at a time, filling the whole area. When several stages await action,
  prev/next navigation steps through the queue.
- **Approval / Question views** — the plan with line-by-line review and inline
  comments, or the agent's question with answer options and a free-text reply, each
  with a fixed bottom action bar.
- **Folder icon (Docker mode only)** — opens the [project file browser](docker.md#project-file-browser).
- **Est. cost tile & Cost tab** — the header's fifth metric shows the run's estimated
  cost and opens a **Cost** tab with a per-stage/overhead/total breakdown; the stage
  rail shows a quiet per-stage cost. All of it can be switched off — see
  [Cost tracking](#cost-tracking) below.

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

1. Click a plan line — a comment form opens.
2. Write a remark — the line highlights yellow.
3. Click "Send revision (N)" — all comments are sent to the agent with line numbers.

## Sending a note to a running stage

While a stage is actively `running`, a messenger-style composer is pinned to the
bottom of its **Feed** — the same input you'd expect in a chat app:

1. Select the `running` stage and open its **Feed** tab. A note input sits at the
   bottom: a growing textarea, an **Attach** button, and a round send button. It
   appears only for a `running`, non-script stage.
2. Type your note and send it — click the send button or press **Cmd/Ctrl+Enter**
   (plain Enter inserts a newline). **Attach** lets you paste or upload an image,
   and in Docker mode reference a project file.
3. The agent finishes its current step, then receives SIGINT (a graceful interrupt,
   not a kill); the stage moves through `revising` and restarts the same phase
   (planning/implementation/review/autonomous) with your note folded into its
   context, then continues toward `done`.
4. Your note appears in the Feed as a right-side bubble carrying its text, alongside
   the agent's own messages (it is not clickable — it's a record of what you sent).

At the `awaiting_approval` checkpoint you redirect the stage a different way — with
line comments and "Send revision" (see [Inline plan comments](#inline-plan-comments) above).

> An earlier version offered "Add a note for the agent" in the stage's kebab (⋮)
> menu; that item was replaced by this Feed composer. The kebab still hosts stage
> buttons, **Pause**, and the pre-note item for `pending` stages.

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
