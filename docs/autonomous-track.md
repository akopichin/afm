# Autonomous track: `agents: [auto]`

Most stages go through the full planning → approval → implementation cycle. A stage
that doesn't need that ceremony — a simple, well-defined task — can run on the
**autonomous track** instead: set `agents: [auto]` and the stage is executed by a
single autonomous agent in one step, with **no `plan.md`, no approval gate**. The
agent (with its skills) does the work right away and is required to write
`execution_summary.md`, which serves as the artifact for dependent stages in place
of a plan. [Interactive dialog](interactive-stages.md) is still available on an
autonomous stage.

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
