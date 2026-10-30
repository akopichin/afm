# Autonomous track: `agents: [auto]`

Most stages go through the full planning → approval → implementation cycle. A stage
that doesn't need that ceremony — a simple, well-defined task — can run on the
**autonomous track** instead: set `agents: [auto]` and the stage is executed by a
single autonomous agent in one step, with **no `plan.md`, no approval gate**. The
agent (with its skills) does the work right away and is required to write
`execution_summary.md`, which serves as the artifact for dependent stages in place
of a plan. [Interactive dialog](interactive-stages.md) is still available on an
autonomous stage.

The summary must be a readable Markdown file with non-whitespace content at the
stage directory path given in the agent prompt. afm includes that absolute path
in its completion instructions, even when a project overrides prompt templates
with `prompts_dir`. The agent should describe what it changed, why, and how it
checked the work. afm checks for content, not Markdown structure or the truth of
the agent's claims.

If the file is missing or empty, afm can remind a Claude, GLM, or supported
Codex author once before its live session ends. If it is still missing or empty
after exit, afm gives the author up to two separate continuation attempts. An
unavailable or disabled Stop hook does not prevent these continuations. The
reminder uses temporary session integration and preserves the user's existing
Stop hooks and CLI settings. The attempts survive a run restart and are separate
from agent transport retries and
the [verify correction budget](verify.md#the-correction-budget-verifymax_failures).
An unreadable file fails with its file error. After the file passes the check,
declared verify steps and required helpers still gate completion. Progress and
failure notices remain visible after dashboard reconnect. If the agent CLI reports
an error, afm reports that outcome alongside the file check instead of treating
the missing summary as the only cause.

```yaml
stages:
  - id: sync-manifests
    description: "Sync the CODEMANIFEST files with the code"
    agents: [auto]        # autonomous — one step, no plan, no approval
```

`auto` must be the stage's only agent (`agents: [auto]`, nothing else) — the flow
parser rejects combining it with other agent phases.

> **Note.** Earlier versions had an optional LLM *supervisor* that decided per-stage
> whether to collapse into the autonomous track. It was removed as redundant —
> declare `agents: [auto]` statically instead. The old `supervisor` /
> `supervisor_prompt` / `supervisor_command` keys are ignored (afm prints a
> non-fatal warning if it sees them).
