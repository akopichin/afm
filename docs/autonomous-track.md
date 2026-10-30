# Autonomous track: `agents: [auto]`

Most stages go through the full planning → approval → implementation cycle. A stage
that doesn't need that ceremony — a simple, well-defined task — can run on the
**autonomous track** instead: set `agents: [auto]` and the stage is executed by a
single autonomous agent in one step, with **no `plan.md`, no approval gate**. The
agent (with its skills) does the work right away and is required to write
`execution_summary.md`, which serves as the artifact for dependent stages in place
of a plan. [Interactive dialog](interactive-stages.md) is still available on an
autonomous stage.

## Completion and recovery

The file must exist in the stage directory and contain non-whitespace text. The
suggested `Summary`, `Changes`, and `Result` sections help readers, but AFM does not
check for those headings. A valid summary proceeds through the usual completion
checks, including `verify:` when configured; dependent stages start only after
those checks pass.

If the agent exits successfully without the file, or writes only whitespace, AFM
gives the same agent up to two focused continuations (`1/2`, then `2/2`). Each asks
it to inspect the work and actual check results, finish anything still needed, and
write the summary. Already completed work and external actions with side effects
should not be repeated. A genuine agent or provider error follows the normal error
path instead; a file read error fails with its file error rather than using a
summary continuation. If the file is still missing or empty after the second
continuation, the stage fails.

These continuations have a separate limit from transport retries and verification
corrections. **Revise** and a manual **Retry** start a new summary-recovery cycle;
**Pause**, **Continue**, and a process restart preserve the used count. AFM records
each continuation before launching it. If a restart occurs after that record but
before the agent runs, the reserved continuation remains spent; when the summary
is still missing or empty, AFM resumes the same numbered instruction without
charging another continuation. The event feed reports scheduled recovery as a
`retry: summary recovery 1/2` or `2/2` message and preserves notices for clients
that reconnect.

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
