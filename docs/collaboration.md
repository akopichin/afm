# Helpers and collaboration

Helpers are agent tasks attached to a stage. They have their own status, result,
and cost records. A helper can give advice, or it can be **required** to pass
before its stage finishes. Attach helpers from the dashboard or declare them in
`flow.yaml` to attach automatically when a stage starts execution. The Side agent
can record mentions of configured roles, but a mention alone does not create a
task.

## Configure roles

Add roles to global `~/.afm/config.yaml` or project `.afm/config.yaml`:

```yaml
collaboration:
  enabled: true                 # default; AFM_COLLABORATION=0 disables it
  max_depth: 3                 # default; 0 prevents helper creation
  limits:
    max_tasks: 20              # optional lifetime limit for this run
  profiles:
    reviewer:
      title: Code reviewer
      command: codex-as-claude
      instructions: Review the stage's changes and report concrete findings.
      delegate: false          # opt in to child tasks with true
```

Each configured profile needs its own `command`; it does not inherit
`client.command`. Profiles with the same ID in project config replace those in
global config. Only available profiles can run. On a native host, afm accepts
the Codex read-only adapter for helpers; inside a container, afm relies on the
container boundary and allows configured commands without an OS read-only
guarantee. Unavailable roles cannot be attached; the profiles API reports why
each one is unavailable.

`AFM_COLLABORATION` takes precedence over `collaboration.enabled`; collaboration
is on by default. Delegation depth follows `flow.yaml` > project config > global
config > default (3). A flow can override **only** the depth:

```yaml
collaboration:
  max_depth: 2
```

Depth 1 is a helper attached directly to a stage; deeper tasks are its children.
`delegate: true` permits a role to create children, subject to the depth and
whole-run `limits.max_tasks` limits. A flow cannot re-enable collaboration when
config or the environment disables it.

## Attach helpers from flow.yaml

Add `assistants:` to an execution stage to attach configured roles when that
stage starts its implementation, review, or autonomous phase:

```yaml
stages:
  - id: build
    agents: [auto]
    assistants:
      - reviewer
      - profile: qa
        title: QA gate
        instruction: Check test coverage.
        mode: required
        trigger: on_completion
```

A single name (`assistants: reviewer`) or a single object also works. List order
is preserved. The object accepts `profile` plus optional `title`, `instruction`,
`mode`, and `trigger`. Defaults are `advisory` and `one_shot`; the mode and trigger
choices have the same meaning as in the dashboard below. For stages with planning,
helpers attach when execution begins, after plan approval. Retries and crash
recovery do not duplicate an attachment; a new revision can attach a fresh task.

Profiles must exist in `collaboration.profiles` when the run starts. An unknown
profile stops startup; a configured profile that is unavailable at attach time
is logged and skipped without stopping the stage. `assistants:` cannot be used on
`script:` or planning-only stages, and a stage cannot list the same profile twice.
When collaboration is disabled, afm warns about declared helpers and runs the
stage without attaching them. This also lets `AFM_COLLABORATION=0` temporarily
disable a flow's helpers.

## Use helpers in the dashboard

Open a stage's **⋯ → Attach helper…** menu, select an available role, and give
it an instruction. Choose **Advisory** for a result that does not block the
stage, or **Required** for a result that must pass before the stage finishes.
Attach helpers after the stage starts. A helper attached while its stage is
`pending` stays in `waiting_parent` and does not start when the stage starts;
a required helper in that state can prevent the stage from finishing.
Choose when it runs:

- **One shot** runs once.
- **At checkpoints** observes the stage agent's output and can run again when
  a checkpoint is reached.
- **On stage completion** can run earlier if enough stage output reaches its
  collector. If it is still active when the stage result is ready, it receives
  a final turn.

Attached helpers appear as child rows in the stage rail. Select one to see its
status, verdict, findings, delegation breadcrumb, and, for watchers, pass count.
The task view offers **Pause**, **Continue**, and **Cancel** while applicable.
If a required helper cannot pass, **Waive…** lets a human unblock the stage by
recording a reason. Required tasks with `needs_changes` or `inconclusive` do not
pass the gate; a `pass` verdict does.

The **Discussion** tab shows the stage's discussion and lets you post a human
message. Posting does not change the stage's status. Pending agent proposals
also appear there; **Accept** confirms a supported proposal before its change
is applied, while **Dismiss** discards it. In the Side agent, type `@` to choose a
configured helper role from the suggestion menu. Mentions name roles, not
existing task instances.

## Helper answers and protocol errors

Helpers must return one JSON object with `schema_version: 1` and either
`kind: "result"` (with a verdict and summary) or, for a role with `delegate: true`,
`kind: "delegate"` (with child tasks). afm includes this output contract at the
end of each helper's task context, including after any follow-up instruction.

If strict decoding fails, afm tries one mechanical repair of a single JSON
object, such as extracting an answer from a markdown fence or surrounding prose.
The repaired object still has to pass the same schema and verdict checks; repair
cannot change its verdict. An accepted result shows a **repaired** badge in the
helper view and a warning in the stage feed. If the answer still fails, the
helper gets a `protocol_error`. Open **Show the assistant's raw answer** in the
helper view to inspect its final text, when available.

## Add a stage during a run

When available, **+ Stage** in the stage rail opens a dialog for a new dynamic
`agents: [auto]` stage. Enter its ID, command, and prompt, then choose **Append**
or **Insert before** an eligible stage. The dialog previews dependencies and
placement before **Add stage** commits the change. A committed stage appears in
the rail on the next status update.
