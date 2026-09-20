# AI-verify: shell gate + read-only AI review

A single implementation stage with a two-step `verify:` list, run in order after
the agent's own `.done`:

1. `go test ./...` — a plain shell gate (the long-standing `verify:` behavior).
2. `codex-as-claude` — a dedicated, **read-only** AI reviewer that checks the
   result against the stage's requirements and returns a strict `pass` /
   `needs_changes` / `inconclusive` verdict instead of freeform text.

```bash
afm run examples/verify/flow.yaml
```

If step 2 finds a blocking issue, the implementation agent gets one corrective
retry with the reviewer's findings (and a link to the full report) injected into
its prompt. If step 2 itself can't produce a trustworthy verdict (timeout,
transport error, `inconclusive`), the stage fails with "verify execution
failed" — the author is **not** re-run automatically, since a broken verifier is
not evidence the code is wrong.

## Requirements for the `codex-as-claude` step

**On the host** (no Docker), you need a `codex` CLI on `PATH` — `codex-as-claude`
(`scripts/codex-as-claude.sh`) shells out to it in a dedicated read-only mode
(`CODEX_VERIFY=1`): no bypass/full-access flags, an explicit `-s read-only`
sandbox, a fresh session, and the exact final structured answer.

**In Docker with autoShim**, add a `type: codex` recipe to `config.yaml` and use
`command: codex` in the flow instead of the bare `codex-as-claude` name (see the
commented block at the bottom of `flow.yaml`):

```yaml
docker:
  autoShim: true
  agents:
    codex:
      type: codex
      model: gpt-5-codex   # optional
```

Codex authenticates through the ChatGPT-plan OAuth state at `~/.codex` on the
host — no secret needed in `config.yaml` for `type: codex`.

Only the codex adapter is supported as a verify agent in v1 — any other alias
(`claude`, `openai`, `cursor`, a bare `codex` binary) is rejected before the flow
even starts, because only codex has a real, enforced read-only mode.

See [AI-verify](https://akopichin.github.io/afm/verify/) for the full contract,
including the important behavior change for `agents: [auto]` stages (verify now
actually gates their completion) and v1 limitations.
