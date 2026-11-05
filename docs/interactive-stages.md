# Interactive stages

A stage with `interactive: true` can ask the user questions through the dashboard.
The agent uses two commands, `afm stage ask` and `afm stage wait-answer`. A "Dialog"
section appears in the dashboard where the user answers. While there's no answer,
the stage sits in `awaiting_user_input` status; once answered, execution continues.

With `collaboration.mode: assistants`, the stage agent's mesh instructions explicitly
route questions, clarifications, choices and approvals from the human through this
dialog. An instruction such as "ask the user" means `afm stage ask` followed by
`afm stage wait-answer`, rather than a mesh message or a wait for an `@mention`.
The agent can still reply to incoming chat messages through the mesh.

```sh
afm stage ask --option "Go" --option "Python" <<'EOF'
## Full context here

Which language should the service use?
EOF
# {"id":"q1","wait":"afm stage wait-answer q1"}

afm stage wait-answer q1
# {"id":"q1","answer":"Go","from_options":true}
```

- **`afm stage ask`** reads the question text (markdown) from stdin. Each
  `--option` adds an answer option; `--allow-custom=false` requires the user to pick
  one of them (a custom answer is allowed by default). afm picks the next question
  id itself (`q1`, `q2`, …), checks the question and creates the question file, so
  the agent does not write dialog files by hand. Asking the same question again
  returns the same id, so repeating the command is safe. While an earlier question
  is still open, a different question fails with `question_pending` and the id of
  the open one.
- **`afm stage wait-answer [qN]`** waits for the answer to `qN` (by default the
  phase's latest question) and stops after `--timeout` seconds. The default is
  `AFM_WAIT_ANSWER_TIMEOUT`, else 100 seconds, always limited to 1–600. The agent's
  own command timeout must be longer than this wait, so the prompt tells agents to
  allow at least 120 seconds.

| Exit code | Output | Meaning |
|-----------|--------|---------|
| 0 | stdout `{"id","answer","from_options"}` (`ask`: `{"id","wait"}`) | done |
| 75 | stdout `{"status":"waiting","id","retry"}` | no answer yet: run the same command again |
| 1 | stderr `{"error","message"}` | a mistake in the call; the message names the field to fix |

Error codes include `invalid_question` (an empty, oversized or otherwise invalid
question or option), `question_pending`, `no_question`, `invalid_arguments` and
`not_stage_author`.

Only the stage's own agent can ask. afm gives it `AFM_STAGE_DIR` (the absolute path
to the stage directory, even when `flow.root_dir` changes its working directory) and
`AFM_DIALOG_PHASE` (`planning`, `implementation`, `review`, or
`autonomous_execution`). Verify and helper agents do not get `AFM_DIALOG_PHASE`, so
the commands fail with `not_stage_author` there. The commands are hidden from
`afm --help`: they are meant for the agent, not for a person. afm puts a directory
with a link to the running `afm` binary first in the `PATH` of every agent it starts,
so agents call the same version that runs the flow even when another `afm` is
installed; the directory is removed when the run ends.

Under the hood the commands use the same files as before:
`<phase>.q<N>.question.json` and `<phase>.q<N>.answer.json` in the stage directory.
The prompt keeps a short fallback for an agent whose `afm` command is unavailable:
it can write these files by hand, and afm reads them as usual.

When launching `claude`, the flags
`--print --output-format stream-json --verbose --dangerously-skip-permissions` are
always added (`--verbose` is required for stream-json in Claude Code 2.1.x). If an
interactive agent mistakenly writes `question.json` outside `$AFM_STAGE_DIR`, the
poller auto-relocates the file into stageDir and creates a symlink for the answer —
the stage moves into `awaiting_user_input` instead of hanging.

If `question.json` contains malformed JSON, afm tries a local repair and then
limited format-only attempts. It accepts only a question that passes the usual
validation without changing its meaning. After three unsuccessful attempts, an
interactive stage shows the raw text as a free-text question so the user can
answer; a non-interactive stage uses its automatic-answer fallback. A question
created by `afm stage ask` is already validated, so it skips this repair.

```yaml
stages:
  - id: discovery
    name: "Gather Requirements"
    description: |
      Ask the user for their preferred language: run `afm stage ask` with
      the options, then `afm stage wait-answer` (run it again after exit 75).
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
