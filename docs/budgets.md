# Spending budgets

A budget caps what a run, a stage, a helper or a run agent may spend, in tokens,
in dollars or both. afm warns at 80%, acts at 100%, and lets you raise the limit
and continue from the dashboard, the HTTP API, the Go SDK or the flow file.

Budgets are about **limits**. For the estimated cost of a run see
[Cost accounting](accounting.md); budgets work with the dashboard's cost display
switched off.

## Declaring a limit

A limit is a mapping with `tokens`, `money` or both, and an optional
`stop_policy`:

```yaml
name: release
budget: { tokens: 5m, money: 40$ }        # the whole run

stages:
  - id: implement
    prompt: Implement the change.
    budget: { tokens: 800k, stop_policy: medium }
    assistants:
      - profile: reviewer
        budget: { tokens: 150k }          # this helper only
  - id: docs
    prompt: Update the docs.
    budget: { tokens: 200k, stop_policy: info }

team:
  - id: arch
    profile: rev-arch
    title: Architecture
    budget: { tokens: 300k }              # this run agent only
```

| Field | Meaning |
|---|---|
| `tokens` | A number with a `k` or `m` suffix: `500k`, `1.5m` (up to three digits after the point for `k`, six for `m`). Counts input, output, cache read and cache write tokens. |
| `money` | A positive USD amount with a `$` suffix: `10$`, `0.50$`. Needs explicit rates, see [Money limits](#money-limits). |
| `stop_policy` | `hard` (default), `medium` or `info`; see [Policies](#policies). |

A budget needs `tokens` or `money`; unknown keys and a bare `budget:` are
rejected by `afm validate`. A limit can be declared at the top of the flow (the
run), on a stage, on a declared helper (`assistants:` entry), and on a `team`
member. A helper profile may carry a default in
`collaboration.profiles.<id>.budget`; a `budget` on the `assistants:` entry
replaces that default as a whole.

## Scopes and nesting

Every counter belongs to one scope:

| Scope | Counts |
|---|---|
| `run` | Everything in the run. |
| `stage:<id>` | The stage's agent, its verify and repair turns and every helper attached to it. |
| `task:<id>` | One helper or run agent. |

Scopes nest: `run` → `stage` → helper, and `run` → run agent (a run agent
belongs to no stage, so a stage limit does not apply to it). Every usage record
counts in its own scope and in all of its parents, and work starts only if every
scope on the path allows it. A retry, another turn or a loop tick uses the same
counter; a new stage, helper or run agent gets its own.

A limit on a child never lifts a limit on a parent: a stage with a generous
budget still stops when the run budget is used up. The Dashboard
[Assistant](dashboard.md#assistant-a-free-form-chat-alongside-the-run) is
outside budgets.

## Policies

| `stop_policy` | At 100% of the limit |
|---|---|
| `hard` (default) | Nothing new starts. Running agents in the scope and below are interrupted, scripts are terminated (Linux) and the scope is held. |
| `medium` | Work that already started may finish, including its verification, repairs and child helpers. New stages, messages and loop ticks do not start. |
| `info` | Warnings only; nothing stops. |

Reaching a limit is not a failure: no stage becomes `failed`, no retry is spent,
and sibling stages outside the limited scope keep running. A held stage that
has started is paused; a stage that has not started keeps its pending or ready
status and is simply not admitted, so `auto_run: false` and the other start
conditions still apply. afm cannot reserve tokens, so the call that crosses the limit finishes
and may overshoot it.

When both `tokens` and `money` are set, reaching either one counts.

## Money limits

A `money` limit needs a complete **explicit** rate card — `input`, `output`,
`cache_read` and `cache_write` — for the command and model of every agent whose
spend counts toward it. Built-in prices and partial cards are not enough. Give
the rates in [`pricing:`](accounting.md#pricing-overrides):

```yaml
pricing:
  models:
    my-model: { input: 3.00, output: 15.00, cache_read: 0.30, cache_write: 5.00 }
```

`afm validate` refuses a flow whose money budget covers a model without the four
rates and names the scope, the command, the model and the missing rates. At
runtime a missing card holds the scope with *rates missing*; past records are
never repriced. Without a money limit nothing changes. Money is counted in the
run directory's own USD estimate, so it is tied to the run, not the project.

## Warnings and the limit notice

| When | What you see |
|---|---|
| 80% of a limit | `Budget 80% · Stage alpha: 850 / 1k tokens` (one per scope, metric and limit episode, so reconnects and restarts do not repeat it). |
| 100% | `Budget limit reached · Stage alpha: 1.2k / 1k tokens — stopped` for `hard`; `— finishing active work, nothing new starts` for `medium`; `— limit exceeded, work continues` for `info`. |
| A limit changed | `Budget updated · …` |
| A hold released | `Budget resumed · …` |

Notices are stored in `notices.jsonl`, so they survive a reload and a restart.
With both limits set, the row shows both figures (`8k / 10k tokens · $0.08 /
$0.10`), whatever the dashboard's money display is set to.

## In the dashboard

- **Feed.** Notices of a run or stage appear in the feed of the stage you have
  selected; notices of a run agent appear in **Team → Activity**. Warning and
  limit rows have an **Edit** button that opens the budget window.
- **Stage list.** A stage at 80% or more of its limit shows a `Budget N%` badge,
  and `Stopped` while held. Clicking it opens the window.
- **Banner.** A stage stopped by its budget shows **Stopped by budget** in place
  of the generic paused text, and its **Continue** button opens the budget
  window instead of resuming blindly.
- **Header.** A **Budget** tile shows the run's worst metric. It appears only
  when the run has a limit or is held; you can still set a limit for any scope
  from **Cost → Budgets**.
- **Cost → Budgets.** Lists the run, stages, helpers and run agents, including
  those with *No limit*, each with **Edit**.

### The budget window

The window shows what was spent, the limit and policy, what is left, the state,
the declared (original) limit with its source, and the limits of every parent.
Choose one of:

- **Use declared limit** — drop the runtime change.
- **Custom limit** — tokens, USD and **When the limit is reached** (the three
  policies).
- **No limit for this stage** — remove this scope's own limit; parent limits
  still apply.

Buttons:

- **Save** stores the limit and does not start work.
- **Save and continue** stores the limit and continues the held scope in one
  step. If the limit was saved but the scope is still limited (or something else
  blocks it), the window stays open with *Limit saved. Work is still stopped:
  …*; the next **Save** works as usual.
- **Continue** appears when the scope is held and the window has no unsaved
  edits.

When a parent holds the scope (for example the run budget is used up), the
window names the blocker and disables the continue buttons: raise that limit
instead.

If the budget changes under you (another tab, the API), **Save** answers with a
revision conflict; your typed values stay, the current ones are shown, and a
second **Save** applies yours. The window keeps your draft through background
updates and new questions.

### Saving to the flow file

**Also save to `flow.yaml`** writes a *custom* limit into the declaration the
scope came from, so the next run starts with it. The run limit is saved first;
if writing the file fails, the window says *Run limit saved, flow.yaml was not
changed: …* with **Retry saving**, and continuing (if requested) is skipped.

afm edits only the text of the `budget` block, keeping comments and layout
elsewhere, and checks that the result still validates. It refuses when:

- the scope is not declared in the file (a stage added during the run);
- the file is a symbolic link or not writable;
- the budget of this declaration changed in the file since it was read (edits
  to other parts of the file are fine);
- the declaration is not unique, or uses anchors, aliases or merge keys;
- the stage or team member is declared inline (a one-line `budget: {…}` value
  is supported and replaced), the lines of the block being replaced contain
  comments (a comment on the `budget:` key line is kept), or the helper is
  declared by profile name only.

Only a custom limit can be saved; **No limit** and **Use declared limit** are
runtime-only.

!!! warning "A runtime change after an export is replaced on restart"
    A restarted run takes the declared limit from the file again. After an
    export the file holds the exported value, and the run does not carry the
    *original* declaration over. A later runtime change that is not saved to the
    file is therefore replaced by the file's value on restart. The window shows
    a note when the file and the run's declared limit differ.

## From the command line

- `afm check` and `afm report` print a **Budgets** section: scope, spent / limit,
  policy and state (`ok`, `80% used`, `reached`, `draining`, or
  `held: limit reached`, `parent limit reached`, `rates missing`,
  `accounting unavailable`). The section is read from the journal, so it works on
  a stopped run and with accounting display off. For a running run the numbers
  are a snapshot.
- `afm validate` checks the declared limits and the money rates.
- `afm run` without a dashboard (`--port 0`) cannot be raised by anyone, so a
  run held by a hard or medium limit ends with **exit code 75**, lists the held
  scopes and tells you to raise the limit. Run again to continue; the same run
  resumes and reads the flow file again. A held run never starts another
  [`repeat`](flow-reference.md#repeat-run-successful-flows-again) cycle:
  `afm: repeat stopped: budget limit reached`.
- With a dashboard or `--mode core`, a held run stays open so you can update and
  continue; Ctrl+C ends it with the usual exit code 130.

## Restart and resume

Limits, overrides, holds and notices are journaled; spend is rebuilt from
`usage.jsonl`. After `afm run` is restarted on the same run:

- an unchanged declared limit keeps the runtime override;
- a changed declared limit (you edited the flow file) replaces the override of
  that scope and releases the hold if the new limit allows work;
- the figures and holds are the same as before the restart.

## HTTP API

Served when the run has budgets (`capabilities.budgets` in `/api/status`;
`capabilities.budget_export` when saving to the flow file is possible). Without
them the routes answer 404. Types are those of `contracts/budget`.

| Route | Purpose |
|---|---|
| `GET /api/budgets` | `{"budgets": [view, …]}` for every declared scope. Never starts work. |
| `GET /api/budgets/view?scope=<id>` | `{"view": …, "yaml"?: {available, reason?, file, declared}}`; `yaml` appears when the scope can be saved to the flow file. |
| `POST /api/budgets/update` | Body: `scope`, `expected_revision` (required; `0` is valid), `override` (`{"kind": "limit"\|"unlimited"\|"none", "spec"?}`; `spec` is `{tokens, money_usd, stop_policy}`), optional `save_to_yaml`, `resume`. Body up to 16 KiB, unknown fields rejected. |
| `POST /api/budgets/resume` | Body: `scope`, `expected_revision`. Answers `{"view": …}`. |

A view carries the engine's decision (effective and declared limit, spend,
level per metric, `hold`, `draining`, parent limits and the `actions` that make
sense now); clients display it and never recompute admission. POST requests
must be same-origin.

`update` answers `200` with the new view and, when `save_to_yaml` was asked, an
`export` result (`saved`, `failed` or `unavailable`). If the limit was saved
but `resume` was refused, the answer is still `200` with
`resume_error: {code, message}`. Failures are `{"error": code, "message": …}`;
branch on `error`:

| `error` | Status | Meaning |
|---|---|---|
| `budget_invalid`, `budget_rates_missing` | 400 | Bad scope, body or limit; money without explicit rates. |
| `budget_unknown_scope` | 404 | The scope is not in the run. |
| `budget_revision_conflict` | 409 | `expected_revision` is stale. |
| `budget_still_limited` | 409 | `resume` of a scope that is still limited. |
| `budget_closed` | 503 | The run is shutting down. |
| `forbidden_origin`, `request_too_large` | 403, 413 | Foreign Origin; body above 16 KiB. |

A dropped connection does not cancel an update that was already accepted.

The [Go SDK](sdk.md#budgets) wraps the same routes.

## Limitations

- A helper started with `afm agent spawn` gets its profile's default budget
  only; the command takes no budget.
- No token reservation and no price forecast: a call may finish past its limit.
- One hold is shown per scope.
- Budgets of the Dashboard Assistant are not supported.
- The buttons for editing a limit appear on the feed, stage list, header and
  **Cost** tab; helper and run-agent rows in Team show the status text only.
