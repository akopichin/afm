# Interactive stage

A stage marked `interactive: true` can ask the user questions mid-run through
the [file-based dialog protocol](https://akopichin.github.io/afm/interactive-stages/).
The agent writes a `question.json`, the dashboard shows a "Dialog" panel, and
the stage waits in `awaiting_user_input` until you answer.

```bash
afm run examples/interactive/flow.yaml
```

Open the dashboard, answer the question when it appears, and the stage continues.

> While a stage waits for an answer the agent is idle. The default
> `executor.idle_timeout` is 30 min — raise it (`executor: { idle_timeout: 24h }`)
> if you expect long waits.
