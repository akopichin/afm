# AI-verify: an independent verification gate

`verify` on a stage is a **stage-completion gate**: after the stage's agent finishes
and the usual file-probe passes (`.done`/declared artifacts, or a non-empty
`execution_summary.md` for `agents: [auto]`), afm runs the declared verify steps
before the stage is actually marked done. A step can be a plain shell command (the
long-standing behavior) or a dedicated **AI reviewer** instructed to inspect the
result without editing it and report back structured findings.

It is not a new stage, not a new `agents` phase, and not a rename of `review`.
Verify never publishes `EvComplete` itself and never activates dependent stages —
only the stage's own completion path does that, once every verify step has passed.

```text
stage's agent finishes
    ↓
file-probe (.done / artifacts / execution_summary.md)
    ↓
verify: shell and/or AI, run in order
    ├── everything passes         → normal completion path
    ├── blocking issues found     → report → existing incomplete-retry (author fixes it)
    ├── verify itself couldn't run → diagnosable "verify execution failed"
    └── user paused/revised        → existing pause/revise/cancel path
```

## The YAML forms

### 1. Scalar string (legacy, unchanged)

```yaml
verify: "go test ./..."
```

One shell step, run in the agents' project root (the run's worktree, else
`flow.root_dir`, else the afm process's working directory) after `.done`. A non-zero exit is treated as "not actually done" — the author gets a
corrective retry with the command's output in the prompt, then `failed` once the
correction budget is spent (default 1, configurable via
[`verify.max_failures`](#the-correction-budget-verifymax_failures)).

### 2. Object — one step

A shell step:

```yaml
verify:
  run: "go test ./..."
  timeout: 5m           # optional
```

Or an AI step — a dedicated reviewer instructed to be read-only:

```yaml
verify:
  command: codex-as-claude   # host; see "Choosing a verifier" below
  timeout: 15m
  prompt: |
    Check the result against the stage's acceptance criteria. Block only
    provable defects, contract violations, and regressions. Don't require
    speculative abstractions or scope creep.
```

`run` and `command` are mutually exclusive, and exactly one is required — an object
with neither (or both) is a parse-time error. `prompt` is only valid on an AI step;
if omitted, the verifier still runs with afm's built-in verify instructions and the
stage's own context (description, plan, artifacts, author's summary).

### 3. List — sequential steps, fail-fast

```yaml
verify:
  - run: "go test ./..."
    timeout: 5m
  - command: codex-as-claude
    timeout: 15m
    prompt: |
      Check acceptance criteria, backward compatibility, and error handling.
      Block only provable issues; leave architectural preferences unblocking.
```

Steps run **in order**. The first step that doesn't pass stops the sequence —
remaining steps are recorded as "not run", not "passed". After the author fixes
the issue, the **whole list re-runs from the beginning** on the next attempt; a
step that already passed is not cached across attempts.

### 4. Container — steps plus correction settings

To set a per-stage correction budget (how many `needs_changes` rejections the
author may correct before the stage fails, see
[The correction budget](#the-correction-budget-verifymax_failures) below) or a
per-stage stalemate backstop (see
[Stalemate detection](#stalemate-detection-verifyreview_patience) below), or
[fresh correction sessions](#fresh-correction-sessions-verifyfresh_correction),
wrap the steps in an object with `steps:` and the desired settings:

```yaml
verify:
  steps:
    - run: "go test ./..."
      timeout: 5m
    - command: codex-as-claude
      timeout: 15m
      prompt: |
        Check acceptance criteria and backward compatibility.
  max_failures: 3          # optional; overrides the global verify.max_failures
  review_patience: 2       # optional; overrides the global verify.review_patience
  fresh_correction: false  # optional; reuse the interactive author's session
```

`steps` is the same list of step objects as form 3; all three settings are optional.
Only this container form carries them — the
scalar, single-object, and list forms above use the global `verify.max_failures`
(default 1), `verify.review_patience` (default 0), and `verify.fresh_correction`
(default `true`). No other top-level keys are allowed here: a step field
(`run`/`command`/`prompt`/`timeout`) at the
container's top level is a parse error (it belongs inside a `steps` item).

### Fields

| Field | Where valid | Rule |
|-------|-------------|------|
| `run` | shell step | non-empty shell command; mutually exclusive with `command`/`prompt` |
| `command` | AI step | required — an agent alias, resolved by the **same** command/recipe mechanism as `stage.command`. Text-only recipes are rejected at preflight; the command must support the agent CLI output protocol (see below) |
| `prompt` | AI step only | additional review criteria; optional |
| `timeout` | either | optional step duration; bounds both waiting for a free command slot and the step's own execution. The executor's independent idle-timeout still applies — whichever limit is hit first wins |

A `verify:` field is unset by default — a flow that never mentions it has zero
behavior change: no extra runs, no extra files, no new defaults.

## Choosing a verifier

Any command that runs as an agent can act as a verify adapter — a bare
`claude`, `codex-as-claude`, a `docker.agents` recipe, or a compatible custom binary
on `PATH`. The only agent commands rejected at preflight are **text-only recipes**
(`type: openai`, `type: cursor`): they have no tool loop or guaranteed-JSON
contract. `DecodeModelResult` would fail at the end of an expensive verify pass.
Rejection happens at **preflight**, before the flow even starts, with an error
naming the stage, step index, and recipe type. Other commands must produce the
expected agent output and a valid JSON verdict when run; preflight does not
probe their behavior.

Three working options:

**`codex-as-claude`** — the only verifier with an OS-level read-only guarantee on
the host:

```yaml
verify:
  command: codex-as-claude
```

`codex-as-claude` (`scripts/codex-as-claude.sh`) understands a dedicated
`CODEX_VERIFY=1` mode. On the host, it omits bypass/full-access flags and requests
the CLI's explicit `-s read-only` sandbox without escalation. Inside afm's Docker
container, the same adapter uses full access because the codex sandbox cannot
create its namespace there. It uses a fresh session (never resumes the author's)
and captures the final structured answer (`--output-last-message` when the
installed codex CLI supports it) rather than intermediate tool output.
A `type: codex` recipe alias is also accepted at preflight without Docker
autoShim. Its generated `codex-as-claude` wrapper is available when Docker
autoShim runs; without that wrapper, the alias must resolve to a compatible
installed command, and afm cannot guarantee read-only execution.

**A `docker.agents` recipe** — in Docker autoShim mode, review with a different
account, model, or provider than the stage's own author:

```yaml
docker:
  autoShim: true
  agents:
    reviewer:
      type: claude
      model: claude-sonnet-5
      auth: { from: "file:~/.afm/secrets/reviewer-token", to: "env:CLAUDE_CODE_OAUTH_TOKEN" }
```

```yaml
# flow.yaml
verify:
  command: reviewer
```

**A bare `claude`** — reuses the author's own account:

```yaml
verify:
  command: claude
```

**Trust note.** Without the `codex-as-claude` read-only wrapper on the host,
afm does not enforce filesystem isolation. Other verifiers receive the usual
agent arguments, including `--dangerously-skip-permissions`, and rely on the
verify prompt to stay read-only. afm compares Git working-tree fingerprints
before and after each AI step and warns in the feed if they differ. This check
is best-effort: it needs a readable Git tree and excludes `.afm/`; a warning
does not change the step's verdict. A bare `claude` verifier keeps its Claude
credentials (it needs them to authenticate); a recipe verifier carries its
own `auth` and does not inherit the author's Claude credentials.

## Outcome semantics

The verifier returns a strict JSON verdict — `pass`, `needs_changes`, or
`inconclusive` — never freeform text:

```json
{
  "schema_version": 1,
  "verdict": "needs_changes",
  "summary": "One test asserts the wrong status code.",
  "findings": [
    {
      "blocking": true,
      "title": "Wrong HTTP status on invalid token",
      "path": "internal/auth/middleware.go",
      "line_start": 42,
      "line_end": 49,
      "requirement": "An expired token must return 401, not 500",
      "evidence": "The handler panics on jwt.ErrTokenExpired instead of returning 401.",
      "minimal_fix": "Return 401 explicitly when errors.Is(err, jwt.ErrTokenExpired)."
    }
  ]
}
```

| Outcome | Meaning | What afm does |
|---------|---------|---------------|
| `pass` | no blocking findings | the step passes; the stage moves to the next step (or completes) |
| `needs_changes` (≥1 blocking finding) | the verifier found a real, provable problem | the author gets a corrective attempt via the incomplete-retry mechanism, with the blockers and a link to the full report injected into its prompt. How many corrections are allowed before the stage fails is the **correction budget** (default 1, configurable via [`verify.max_failures`](#the-correction-budget-verifymax_failures)) |
| `inconclusive`, a protocol error, a timeout, a non-zero verifier exit, or a transport failure | verify itself could not produce a trustworthy verdict | a "verify execution failed" error — the stage is marked `failed`; the author is **not** re-run automatically (a broken/unreachable verifier is not evidence the author's work is wrong) |

A recognized provider usage limit is an exception to a non-zero verifier exit:
the stage enters [`awaiting_quota`](stage-lifecycle.md) and resumes at the same
verify step after the wait. It does not spend the verifier's execution retry or
correction budget, and the author is not rerun for that wait.

A shell step's non-zero exit is treated the same as an AI `needs_changes` — it
consumes the correction budget, giving the author a corrective retry, then
`failed` once the budget is spent.

### Transient verifier crashes are retried before failing

Before an AI verifier's execution failure becomes a terminal "verify execution
failed", afm retries the same verify step. The case that qualifies as **transient**
is any AI verifier *process*-execution failure that is **not** a timeout,
interrupt, or recognized provider usage limit — this covers the verifier failing
to launch, its output stream
breaking mid-run, and the verifier running and exiting non-zero (the live-run
scenario: a codex adapter that crashes on the first two attempts, then passes). afm
retries that step up to **2 times** with an **8-second backoff** between attempts;
if any retry succeeds the step passes normally. Only after the retries are exhausted
is the step a terminal exec-failure.

This retry budget is fixed (not configurable in v1) and is entirely separate from
the [correction budget](#the-correction-budget-verifymax_failures) — a crash is not
a verdict, so it never consumes a correction. Everything else is still **fail-fast,
not retried**: a step timeout, the executor's idle-timeout, a protocol error, a
Pause/Revise/cancel interrupt, and a direct executor/storage error (such as the
verify agent hitting its own context deadline) all go straight to their terminal
outcome. The backoff wait is itself interruptible, so pausing or revising during it
does not have to wait out the 8 seconds. When a step does fail for real, its reason
now carries the tail of the verifier process's stderr, so the dashboard and logs
show why the verifier crashed rather than a bare "verify execution failed".

`script:` stages cannot declare `verify` (there's no agent to correct). A
planning-only stage (`agents: [planning]`, no implementation/review/autonomous
execution) cannot declare `verify` either — verify is a gate on the result of doing
the work, not on a plan.

## Fresh correction sessions: `verify.fresh_correction`

After a verify step rejects the work and a correction is allowed, afm starts an
`interactive: true` author's next attempt in a new session by default, without
resuming the rejected attempt. This applies to both AI `needs_changes` and a shell
step's non-zero exit. The correction prompt still includes the verify failure and
previous work context. When a retry attempt starts without resuming, afm also includes
answered questions from that phase's dialog history, so earlier user decisions are
available to the agent. The digest keeps the most recent answers within an 8 KiB
limit. It is omitted when the agent resumes an existing session.

Set `verify.fresh_correction: false` in `.afm/config.yaml` or
`~/.afm/config.yaml` to retain the interactive session across verify corrections:

```yaml
verify:
  fresh_correction: false
```

For one stage, use the [container form](#4-container-steps-plus-correction-settings)
of its `verify:` field. Resolution is **stage `verify.fresh_correction` > project
config > global config > default `true`**. This setting controls session renewal
after verify rejection; it does not change the correction budget. Non-interactive
agents already start without a resumable session.

## The correction budget: `verify.max_failures`

When a verify step returns `needs_changes` (or a shell step exits non-zero), afm
sends the author back for a correction with the blockers injected into its
prompt. `verify.max_failures` is how many such corrections are allowed before the
stage is marked `failed`:

- **`N`** = the number of author corrections. **Default is 1** — the historical
  behavior (author fails once, gets one corrective run, then pass or fail).
- **`0`** is strict: the first rejection fails the stage immediately, with no
  correction.
- **`3`** allows up to three corrections and fails on the fourth rejection.

A negative value is rejected at config load time.

Set it globally with a top-level `verify:` block in `config.yaml`:

```yaml
verify:
  max_failures: 2
```

or per stage with the [container form](#4-container-steps-plus-correction-settings) of
the stage's `verify:` field. Resolution is **stage `verify.max_failures` >
global `verify.max_failures` > default 1**.

Only real verify rejections draw down the budget — an AI `needs_changes` or a
shell step's non-zero exit. Two things it does **not** touch:

- A verifier *execution* failure (`inconclusive`/timeout/non-zero verifier
  exit/transport failure) fails the stage, as described under
  [Outcome semantics](#outcome-semantics) — a broken verifier is not evidence
  the author's work is wrong, so it never consumes a correction. (A transient
  process-execution failure is [retried with backoff](#transient-verifier-crashes-are-retried-before-failing)
  first, but still never draws on the correction budget.)
- Transport/rate-limit retries and plan-incomplete retries are counted
  **separately** and neither draws from the verify budget nor is drawn from by
  it.

The counter is **in-memory** and resets if the run is resumed after a restart
(it is not persisted). Verify corrections also still ride the overall retry
loop, so the effective ceiling is `min(max_failures, MaxRetries)` — with the
built-in `MaxRetries = 15`, a budget above 15 is capped there.

## Stalemate detection: `verify.review_patience`

An AI reviewer runs fresh each round with no memory of the previous ones, so it
can keep finding ever-subtler refinements forever. When the author disagrees and
stops editing in response, the two deadlock: the reviewer keeps returning
`needs_changes`, the author keeps producing the same result, and the stage burns
its correction budget and `fails` over a dispute, not a real defect.

`verify.review_patience` is a backstop for exactly that case. When a verify
rejection is the **N-th consecutive** one that left the git **working tree
unchanged** — the author made no edits in response to the review — afm **accepts
the stage as complete** instead of looping to failure. In effect the implementer
wins the dispute once they have held their ground `review_patience` times in a row.

- **`0`** (the **default**) disables stalemate detection entirely — exactly
  today's behavior. The feature is opt-in.
- **`N` ≥ 1** accepts the stage after the author has left the working tree
  unchanged across `N` consecutive rejections.

A negative value is rejected at config load / flow validation time.

Set it globally with the top-level `verify:` block in `config.yaml`, or per stage
with the [container form](#4-container-steps-plus-correction-settings)
of the stage's `verify:` field. Resolution is **stage `verify.review_patience` >
global `verify.review_patience` > default 0**.

```yaml
verify:
  review_patience: 2
```

Important guarantees and guardrails:

- **It never accepts work that still fails a check.** Only an **AI** step's
  `needs_changes` can be accepted by stalemate, and only when **no shell step runs
  after it** in the sequence. A red `go test` (a shell step) can never be accepted
  this way — put your shell gates first in the list and they stay authoritative.
- **"Unchanged" is measured between rejections.** afm fingerprints the working
  tree at each rejection (HEAD, the staged and unstaged diffs, and each
  **non-ignored** untracked file's path **and** content; afm's own `.afm/`
  directory is excluded). The counter only advances when the fingerprints of two
  consecutive rejections match. Two consequences worth knowing: a change that is
  **visible between snapshots** resets the counter, but an edit that is made and
  then **fully reverted before the next rejection snapshot** looks identical and is
  not detected; and because untracked files are collected with git's standard
  exclude rules, a change confined to **gitignored-only** files is not seen either.
- **It is fail-safe.** If afm cannot prove the tree is unchanged (not a git repo,
  an unborn `HEAD`, any git error, or a file too large to hash), that round does
  **not** count as unchanged and the counter resets — afm never accepts on
  uncertainty.
- **Budget interplay.** For stalemate to engage you need
  `max_failures >= review_patience`; otherwise the stage fails on the correction
  budget before the unchanged-round count is reached, and `review_patience` is a
  silent no-op (the safe side — it never accepts by mistake, it just doesn't
  engage).
- Like the correction budget, the unchanged-round counter is **in-memory** and
  resets if the run is resumed after a restart.

## Reports and feedback files

Every verification pass writes to `<stageDir>/verify/`:

```text
<stageDir>/verify/
  feedback.md                    # active machine feedback, injected into the author's
                                  # next prompt — separate from the human feedback.md
  <verification-id>/
    manifest.json                 # this pass's steps and their outcomes
    report.md                     # full human-readable report (never truncated)
    step-01/
      command.log                 # shell step: full stdout+stderr
      result.json                 # normalized outcome
    step-02/
      agent.log
      agent.jsonl
      raw-result.json             # the verifier's raw final answer, before validation
      result.json                 # accepted, validated outcome
```

`verify/feedback.md` is machine-generated and **separate** from the stage's own
`feedback.md` (which holds human notes from Revise/agent_suggest) — the two never
overwrite each other, and both reach the author on the next retry. `feedback.md` is
capped at 12 KiB (with an explicit truncation marker if a pass has many blockers);
the full report on disk is never truncated. Once the stage passes verify
completely, the active `verify/feedback.md` is cleared — the history under
`verify/<verification-id>/` stays on disk.

## Dashboard

The Feed shows a line for each step as it starts and finishes: `Verify step N ·
<alias>`, then the outcome — pass (success), needs_changes/inconclusive (warning),
or an execution error (danger) — with a **Show full report** link and the step's
cost, alongside the stage's normal pause/retry/revise actions. Nothing about
verify introduces a new stage status or a second progress bar.

If a step hits a [transient verifier crash](#transient-verifier-crashes-are-retried-before-failing),
the Feed shows a `Verify retry N (step M) · <alias>: <reason>` warning line for each
retry before the step finally passes or fails. These lines are durable, so a client
that connects after the retries still replays them.

When [stalemate detection](#stalemate-detection-verifyreview_patience) accepts a
stage, the Feed and the stage rail show a distinct **accepted (stalemate)** verify
badge (its own accent colour, separate from a clean pass or an error) so it's clear
the stage was accepted after unchanged rounds rather than by a passing verdict.

If the Git working-tree fingerprint changes during any AI verify step, the Feed
shows a `Verify step N · <alias> modified the working tree (verifier must be
read-only)` warning line (see the [trust note](#choosing-a-verifier)). This is
purely informational — the step's verdict and the stage outcome are unaffected.

## Limitations (v1)

- **Only the codex adapter has a checkable read-only contract.** Any compatible
  agent command can act as a verify adapter (see [Choosing a verifier](#choosing-a-verifier)),
  but only `codex-as-claude` on the host requests an enforced read-only sandbox.
  In Docker, even the generated codex wrapper uses full access inside the
  container. Other verifiers rely on the prompt; the Git fingerprint warning
  is best-effort, not a sandbox.
- **Shell verify runs where the agents run**: the run's worktree, else
  `flow.root_dir`, else the afm process's working directory. Project artifacts
  (paths without `./`) and a stage's `plan:` resolve from the same root. A shell step and an agent verify step
  therefore see the same project directory.
- **The correction budget is an independent, in-memory counter**
  (`verify.max_failures`, default 1 — see
  [The correction budget](#the-correction-budget-verifymax_failures)), separate
  from the shared retry `attempt` counter. Transport/rate-limit retries and
  plan-incomplete retries no longer eat into it, and vice versa, so the
  configured number of corrective author runs is honored regardless of unrelated
  retries. The counter is **not durable**: it lives only for the current run and
  resets if the run is resumed after a restart. Verify corrections still ride the
  overall retry loop, so the effective ceiling is `min(max_failures, 15)`.
- **Read-only is not a security boundary against local processes.** Even the
  codex sandbox is not a defense against a malicious process running under the
  same OS user.
- **`report.md` is per verification pass, not per step.** In the rare case of a
  multi-step, all-agent-pass config, an earlier step's report link in the feed
  points at the same file as the last step's report (the latest one). Per-step
  report storage is a possible follow-up.
- **(Pre-existing, not verify-specific)** a very fast Pause-then-Continue can still
  double-run an agent in rare timing windows — unaffected by this feature.

## Behavior change: `agents: [auto]` now runs verify

Before this release, a declared `verify` on an `agents: [auto]` (autonomous) stage
**never ran** — autonomous completion only checked for a non-empty
`execution_summary.md`, and the shell/AI verify steps were silently skipped. This
was a gap, not a documented design choice.

**Now a declared `verify` gates autonomous completion, same as any other execution
track.** If you already have `agents: [auto]` stages with a `verify:` field in
production, upgrading can make them start failing where they never used to be
checked at all — this is an intentional fix, not a regression, but it is a real
behavior change. Review your autonomous stages' verify steps before upgrading if
you rely on them never actually running.

Two related compatibility notes:

- **Downgrading is not silent.** If you roll back to an afm binary older than this
  release while your `flow.yaml` still has the new object/list `verify` form, that
  older binary's parser doesn't understand the new shape and **fails to parse the
  flow** — it does not silently ignore the field or fall back to the old
  scalar-only behavior.
- **Report directories survive a rollback.** `<stageDir>/verify/` is plain files on
  disk; rolling back to an older binary does not delete or corrupt any existing
  verification history.

See also: [flow.yaml reference](flow-reference.md#stage-fields),
[Autonomous track](autonomous-track.md), [Docker mode](docker.md) (for the `codex`
recipe type and autoShim).
