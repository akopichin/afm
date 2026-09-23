# Script stages and hooks

A stage can run a plain shell script instead of an AI agent — useful for glue steps
(notifications, deploy commands, a linter run) that don't need an LLM:

```yaml
stages:
  - id: notify
    script: |
      curl -s -X POST https://hooks.example/notify -d '{"status":"started"}'
```

A `script` stage skips planning/approval entirely: as soon as its `depends_on` are
done, the script runs, and the stage moves straight to `done`/`failed` based on the
exit code.

## `script_before` / `script_after` hooks

These hooks run immediately before/after *any* stage's own content — orthogonal to
the stage type, so they combine freely with `agents`/`interactive`/etc.:

```yaml
stages:
  - id: deploy
    agents: [planning, implementation]
    script_before: |
      echo "starting deploy at $(date)"
    script_after: |
      curl -s -X POST https://hooks.example/notify -d '{"status":"done"}'
```

- Both hooks retry automatically on failure: 3 attempts with 1s/2s/3s backoff.
- If `script_before` still fails after retries, the stage blocks in `hook_failed` —
  resolve it from the dashboard with **Retry** (re-run the hook) or **Skip** (proceed
  to the stage's own content anyway).
- If `script_after` still fails, it does **not** revert the stage — it's already
  `done`. You get the same Retry/Skip notice, but the stage's status is unaffected
  either way.

## Output & failure visibility

- Output from `script`/`script_before`/`script_after` streams live to the
  dashboard's event feed, just like an agent's — **both stdout and stderr**.
  stderr lines are rendered distinctly from stdout: each one shows up as a
  warning-toned `[hook:stderr] <line>` row, so you can tell noisy-but-harmless
  stderr chatter (progress output, warnings) apart from stdout without losing
  it from the feed entirely.
- **When a `script:` stage fails**, the feed shows a red
  `script failed: <reason>` row (e.g. `script failed: exit status 1`) with a
  fenced tail of its stderr attached — the reason is visible right in the feed,
  no need to open the raw log to see what went wrong.
- **When `script_before`/`script_after` fails** after exhausting its 3
  retries, the existing `hook_failed` notice now carries the same kind of
  stderr tail alongside the error.
- The tail shown in the feed is bounded (last 20 lines / 4 KiB) so a runaway
  script can't flood the feed. The **full** stderr always lives on disk at
  `<stage>/<phase>.stderr.log` (`<phase>` is `script`, `before`, or `after`),
  regardless of how much of it made it into the tail.
