# Helpers and collaboration

Helpers are agent tasks attached to a stage. They have their own status, result,
and cost records. A helper can give advice, or it can be **required** to pass
before its stage finishes. Enabling collaboration does not start a helper by
itself; attach one from the dashboard. The Side agent can record mentions of
configured roles, but a mention alone does not create a task.

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

## Add a stage during a run

When available, **+ Stage** in the stage rail opens a dialog for a new dynamic
`agents: [auto]` stage. Enter its ID, command, and prompt, then choose **Append**
or **Insert before** an eligible stage. The dialog previews dependencies and
placement before **Add stage** commits the change. A committed stage appears in
the rail on the next status update.
