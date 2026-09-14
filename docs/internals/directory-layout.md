# Directory layout

```
.afm/
  flows/           # flow.yaml files
  runs/
    <flow>-<ts>-<rand>/    # data for a single run (rand — avoids collisions)
      events.jsonl   # event log of transitions — SOURCE OF TRUTH (append + fsync)
      state.json     # derived status snapshot (cache; readers take the truth from the log)
      .lock          # flock of the active afm run
      <stage-id>/
        plan.md          # stage plan
        feedback.md      # revision notes (plan revise, or a note added to a running stage)
        planning.log     # planning agent log (stdout: tool actions)
        planning.jsonl   # raw stream-json
        planning.stderr.log  # agent stderr (claude diagnostics)
        implementation.log
        review.log
        .done                # implementation-completion marker
        # autonomous track (agents: [auto]):
        autonomous.flag      # autonomous-stage marker
        autonomous.log
        execution_summary.md # summary of the autonomous work (artifact for dependents)
        # interactive dialog files (interactive: true):
        <phase>.q<N>.question.json   # agent's question
        <phase>.q<N>.answer.json     # user's answer
        <phase>.dialog.jsonl         # dialog history for the UI
  config.yaml      # project config (optional)
```

See [the event log](event-log.md) for the guarantees behind `events.jsonl`.
