# Interactive stages

A stage with `interactive: true` gets a file-based protocol for dialog with the
user through the dashboard. The agent receives the `AFM_STAGE_DIR` env variable (the
path to the stage directory). To ask a question, the agent writes a
`<phase>.q<N>.question.json` file (`<phase>` is `planning`/`implementation`/`review`;
`N` increments: q1, q2, …), then waits for `<phase>.q<N>.answer.json` to appear via
a bash loop. A "Dialog" section appears in the dashboard where the user answers.
While there's no answer, the stage sits in `awaiting_user_input` status; once
answered, execution continues.

When launching `claude`, the flags
`--print --output-format stream-json --verbose --dangerously-skip-permissions` are
always added (`--verbose` is required for stream-json in Claude Code 2.1.x). If an
interactive agent mistakenly writes `question.json` outside `$AFM_STAGE_DIR`, the
poller auto-relocates the file into stageDir and creates a symlink for the answer —
the stage moves into `awaiting_user_input` instead of hanging.

```yaml
stages:
  - id: discovery
    name: "Gather Requirements"
    description: |
      Ask the user for their preferred language via the file protocol (id: q1):
      write $AFM_STAGE_DIR/implementation.q1.question.json and wait for
      the answer at $AFM_STAGE_DIR/implementation.q1.answer.json.
      After the answer, write the result to ./summary.md.
    agents: [implementation]
    interactive: true
    artifacts:
      - name: summary
        path: ./summary.md
```

Full example: [`examples/interactive/`](https://github.com/akopichin/afm/tree/main/examples/interactive).

> **Waiting for an answer and idle-timeout.** While a stage waits for an answer,
> the agent is idle and writes nothing to stdout. By default `executor.idle_timeout`
> = 30 min — if you don't answer within that time, the waiting agent may be killed.
> For long waits, raise the timeout: `executor: { idle_timeout: 24h }`.

## Non-interactive stages that ask questions

A stage that is *not* marked `interactive: true` — including `agents: [auto]` stages
— may still use the dialog protocol (a skill it runs might ask a clarifying
question). Rather than hanging forever, afm **answers on its own**: it picks the
option marked `(recommended)`/`(default)` (or makes a best-effort autonomous
decision when there are no options), writes the answer file, and records it in the
dialog history with an "answered automatically" badge. The stage's status is never
changed.
