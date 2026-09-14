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
- Output from `script`/`script_before`/`script_after` streams to the dashboard's
  event feed and log panel just like an agent's.
