# AI-verify: an independent verification gate

`verify` on a stage is a **stage-completion gate**: after the stage's agent finishes
and the usual file-probe passes (`.done`/declared artifacts, or a non-empty
`execution_summary.md` for `agents: [auto]`), afm runs the declared verify steps
before the stage is actually marked done. A step can be a plain shell command (the
long-standing behavior) or a dedicated **read-only AI reviewer** that inspects the
result and reports back structured findings.

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

Exactly the previous behavior: one shell step, run in the project directory after
`.done`. A non-zero exit is treated as "not actually done" — the author gets a
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

Or an AI step — a dedicated read-only reviewer:

```yaml
verify:
  command: codex-as-claude   # host; see "Configuring the codex verifier" below
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

### 4. Container — steps plus `max_failures`

To set a per-stage correction budget (how many `needs_changes` rejections the
author may correct before the stage fails, see
[The correction budget](#the-correction-budget-verifymax_failures) below), wrap
the steps in an object with `steps:` and `max_failures:`:

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
```

`steps` is the same list of step objects as form 3; `max_failures` is optional.
Only this container form carries `max_failures` — the scalar, single-object, and
list forms above use the global `verify.max_failures` (default 1). No other
top-level keys are allowed here: a step field (`run`/`command`/`prompt`/
`timeout`) at the container's top level is a parse error (it belongs inside a
`steps` item).

### Fields

| Field | Where valid | Rule |
|-------|-------------|------|
| `run` | shell step | non-empty shell command; mutually exclusive with `command`/`prompt` |
| `command` | AI step | required — an agent alias, resolved by the **same** command/recipe mechanism as `stage.command`. Must resolve to a supported verify adapter (v1: codex only, see below) |
| `prompt` | AI step only | additional review criteria; optional |
| `timeout` | either | optional step duration; bounds both waiting for a free command slot and the step's own execution. The executor's independent idle-timeout still applies — whichever limit is hit first wins |

A `verify:` field is unset by default — a flow that never mentions it has zero
behavior change: no extra runs, no extra files, no new defaults.

## Configuring the codex verifier

Only a **codex** adapter is a supported verify adapter in v1 — it's the only one
with a real, enforced read-only mode. Any other alias (`claude`, `openai`,
`cursor`, or a bare `codex` binary) is rejected at **preflight**, before the flow
even starts, with an error naming the stage and step index — afm refuses to pretend
a verifier is safe when it can't actually guarantee read-only.

**On the host** (no Docker), point `command` at the real read-only shim:

```yaml
verify:
  command: codex-as-claude
```

`codex-as-claude` (`scripts/codex-as-claude.sh`) understands a dedicated
`CODEX_VERIFY=1` mode: it never passes `--dangerously-bypass-approvals-and-sandbox`
or any full-access flag, always requests the CLI's explicit `-s read-only` sandbox,
never escalates on error, uses a fresh session (never resumes the author's), and
captures exactly the final structured answer (`--output-last-message` when the
installed codex CLI supports it) rather than a mix of intermediate tool output.

**In Docker with autoShim**, use a `type: codex` recipe alias instead of the bare
name:

```yaml
docker:
  autoShim: true
  agents:
    codex:
      type: codex
      model: gpt-5-codex   # optional
```

```yaml
# flow.yaml
verify:
  command: codex
```

autoShim generates the read-only wrapper for you. A `type: codex` recipe alias is
only accepted as a verify adapter when this run will actually generate that
wrapper (autoShim enabled) — otherwise afm would silently fall back to running a
raw `codex` binary that doesn't understand `CODEX_VERIFY` or read-only mode at all,
and the preflight check rejects it instead.

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

A shell step's non-zero exit is treated the same as an AI `needs_changes` — it
consumes the correction budget, giving the author a corrective retry, then
`failed` once the budget is spent.

`script:` stages cannot declare `verify` (there's no agent to correct). A
planning-only stage (`agents: [planning]`, no implementation/review/autonomous
execution) cannot declare `verify` either — verify is a gate on the result of doing
the work, not on a plan.

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

or per stage with the [container form](#4-container-steps-plus-max_failures) of
the stage's `verify:` field. Resolution is **stage `verify.max_failures` >
global `verify.max_failures` > default 1**.

Only real verify rejections draw down the budget — an AI `needs_changes` or a
shell step's non-zero exit. Two things it does **not** touch:

- A verifier *execution* failure (`inconclusive`/timeout/non-zero verifier
  exit/transport failure) still fails the stage immediately, as described under
  [Outcome semantics](#outcome-semantics) — a broken verifier is not evidence
  the author's work is wrong, so it never consumes a correction.
- Transport/rate-limit retries and plan-incomplete retries are counted
  **separately** and neither draws from the verify budget nor is drawn from by
  it.

The counter is **in-memory** and resets if the run is resumed after a restart
(it is not persisted). Verify corrections also still ride the overall retry
loop, so the effective ceiling is `min(max_failures, MaxRetries)` — with the
built-in `MaxRetries = 15`, a budget above 15 is capped there.

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

## Limitations (v1)

- **Codex-only.** Only the codex adapter is a supported verify agent in v1 — it's
  the only one with a real, checkable read-only contract. Other providers may gain
  verify support later without changing the YAML shape.
- **Shell verify runs with CWD `"."`** (the afm-root), matching the pre-existing
  legacy contract — **not** `flow.root_dir`. In a split deployment (e.g. Docker
  with sources mounted elsewhere), a shell verify step can see a different
  directory than an agent verify step, which correctly uses `root_dir`. Aligning
  shell verify's CWD with `root_dir` is a separate follow-up.
- **The correction budget is an independent, in-memory counter**
  (`verify.max_failures`, default 1 — see
  [The correction budget](#the-correction-budget-verifymax_failures)), separate
  from the shared retry `attempt` counter. Transport/rate-limit retries and
  plan-incomplete retries no longer eat into it, and vice versa, so the
  configured number of corrective author runs is honored regardless of unrelated
  retries. The counter is **not durable**: it lives only for the current run and
  resets if the run is resumed after a restart. Verify corrections still ride the
  overall retry loop, so the effective ceiling is `min(max_failures, 15)`.
- **Read-only is a policy boundary, not a sandbox.** It prevents accidental edits
  and over-broad model actions from the verifier itself; it is not a defense
  against a malicious local process running under the same OS user.
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
