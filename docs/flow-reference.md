# The flow.yaml file

!!! tip "Editor autocomplete & validation"
    afm ships a JSON Schema for `flow.yaml`. Add this line at the top of your flow
    and VS Code / JetBrains (via the YAML language server) will autocomplete fields
    and flag typos:

    ```yaml
    # yaml-language-server: $schema=https://raw.githubusercontent.com/akopichin/afm/main/schema/flow.schema.json
    ```

    `afm init` adds it automatically, and every file under `examples/` already has it.

```yaml
name: my-feature
description: "Short task description"

stages:

  - id: backend          # unique stage ID
    name: "Backend API"
    description: |
      What needs to be done — in detail.
      The AI will use this text as guidance during planning and implementation.
    agents: [planning, implementation, review]
    skills:              # optional — Claude skills
      - superpowers:test-driven-development
    command: claude      # optional — custom AI command for this stage
    max_parallel: 2      # optional — parallelism limit for this command
    artifacts:           # files this stage passes on to other stages
      - name: api-contract
        path: docs/api-contract.yaml
        description: "OpenAPI specification"
      - name: db-schema
        path: ./schema.sql        # ./ = relative to the stage directory in the run
        description: "SQL migration"
        inline: false             # pass the path, not the contents

  - id: frontend
    name: "Frontend"
    description: "Implement the UI against the API contract"
    agents: [planning, implementation]
    depends_on: [backend]         # will only start after backend completes
    inputs:                       # artifacts from dependency stages
      - backend.api-contract      # the file's contents will be substituted into the prompt
      - ref: backend.db-schema    # optional — doesn't block if the file is missing
        optional: true

  - id: db-migration
    name: "DB Migration"
    description: "Apply the migration"
    agents: [implementation]
    plan: docs/plans/migration.md   # ready-made plan — the planning agent doesn't run
    verify: "make test"             # gate command: exit != 0 — stage is not marked done
```

## Stage fields

| Field | Required | Description |
|------|-------------|----------|
| `id` | yes | Unique identifier. Must be a safe path component — non-empty, not `.`/`..`, and no `/`, `\`, or NUL (it becomes a directory name on disk) |
| `name` | no | Human-readable name for logs and the dashboard (if empty — `id` is shown) |
| `description` | no | Task description for the AI (background/context). Not enforced by the validator, but strongly recommended — it's the main guidance the agent gets |
| `prompt` | no | Explicit instruction for the agent — a separate `<prompt>` block after the context. Unlike `description`, this is a direct instruction on what to do. It's escaped and cannot inject XML tags |
| `agents` | conditional | Combination of `planning`, `implementation`, `review` — or `[auto]` for the [autonomous track](autonomous-track.md). A stage must have **one of**: an agent list, a `plan`, `interactive: true`, `agents: [auto]`, or `script:` (see the note below the table) |
| `depends_on` | no | IDs of stages that must complete first |
| `eager_planning` | no | `true` — planning starts immediately when the flow runs, without waiting for `depends_on` |
| `skills` | no | Claude skills for the agent |
| `plan` | no | Path to a ready-made plan file (skips planning); `./` looks in dependency stage directories, other relative paths start at the agents' project root |
| `command` | no | AI command for this stage (overrides `client.command` from the config) |
| `max_parallel` | no | Limit on parallel stages for this command |
| `interactive` | no | `true` — enables the [file-based dialog protocol](interactive-stages.md) with the user via the dashboard |
| `auto_approve` | no | `true` — approve this stage's plan automatically the instant it's ready, with no human interaction — regardless of a dashboard being attached or `--require-approval`. Default `false`. Intended for CI (see [Dashboard → Auto-approving a plan](dashboard.md#auto-approving-a-stages-plan)) |
| `auto_run` | no | `false` — pause the stage the instant it's first eligible to start (`depends_on` satisfied), instead of starting immediately; it sits in `paused` until you hit **Continue** on the dashboard. Default `true` (starts on its own). Works on any stage type — regular, `agents: [auto]`, or `script`. Only gates the very first activation, not retries |
| `artifacts` | no | Files the stage produces for other stages (see [Artifacts & inputs](artifacts.md)) |
| `inputs` | no | Artifacts from dependency stages (`stage.artifact`, see [Artifacts & inputs](artifacts.md)) |
| `verify` | no | Stage-completion check(s), run after `.done` (or `execution_summary.md` for `agents: [auto]`): a shell command string (legacy), one step object (`{run: ...}` shell or `{command: ..., prompt: ...}` AI reviewer), an ordered list of steps (fail-fast), or a container object `{steps: [...], max_failures: N, review_patience: M, fresh_correction: false}` for per-stage settings. A failing step gives the author a corrective retry with the report injected into its prompt, then `failed` once the budget is spent (default 1, or the global/per-stage `verify.max_failures`). `review_patience` defaults to 0 (off). `fresh_correction` defaults to `true`: after verify rejection, an interactive author's next attempt starts a new session. See [AI-verify](verify.md) |
| `script` | no | Makes this a [script-only stage](script-stages.md): runs the given shell script (`sh -c`) instead of any AI agent — no planning, no approval. Mutually exclusive with `agents`/`command`/`interactive`/`plan`/`verify` |
| `script_timeout` | no | Hard timeout for `script` (default `5m`) |
| `script_before` | no | Shell script run immediately before this stage's own content (agent, autonomous track, interactive dialog, or another script). Works on any stage type |
| `script_before_timeout` | no | Hard timeout for `script_before` (default `5m`) |
| `script_after` | no | Shell script run right after the stage successfully completes |
| `script_after_timeout` | no | Hard timeout for `script_after` (default `5m`) |
| `buttons` | no | Named one-click prompts for the stage's live agent, shown in the dashboard kebab menu (see [Dashboard](dashboard.md)) |
| `assistants` | no | Helper profiles to attach automatically when this stage starts execution. A profile name, an object, or a list of either; see [Declared helpers](#declared-helpers) |
| `reflect` | no | An object `{ file, mode }` that opts the stage into [agent memory](agent-memory.md): `file` is the stage's own Markdown memory file (relative to `memory.path`), `mode` is `r`/`w`/`rw` (default `rw`). Requires the flow-level `memory:` block |
| `memory_use` | no | Overrides the flow-level `memory.memory_use` for this stage (`true`/`false`; unset = inherit). Controls whether the stage **reads** [memory](agent-memory.md) |

!!! note "What makes a stage valid"
    Every stage needs *something to do*. The validator requires at least one of:
    a `planning` agent, a `plan` path, `interactive: true`, `agents: [auto]`, or
    `script:`. A `script:` stage must **not** also set `agents`/`command`/
    `interactive`/`plan`/`verify` (they're mutually exclusive), and `agents: [auto]`
    must be the stage's only agent.

### Declared helpers

Use `assistants:` to attach configured [helper profiles](collaboration.md) as
soon as a stage enters its execution phase, without a dashboard click. It accepts
one profile name (`assistants: reviewer`), one object, or a list mixing both:

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

Object fields are `profile` (required), `title`, `instruction`, `mode`
(`advisory` or `required`), and `trigger` (`one_shot`, `checkpoints`, or
`on_completion`). Omitted mode and trigger default to `advisory` and `one_shot`.
Profiles must be defined in `collaboration.profiles`; an unknown profile stops
`afm run` at preflight. Each profile can appear only once per stage. The field is
valid on execution stages (including `agents: [auto]`), but not on `script:` or
planning-only stages. See [Helpers and collaboration](collaboration.md) for
completion gates and behavior when collaboration is disabled.

## Flow fields (top level)

`name`, `description`, `prompt` (global instruction for all stages), `max_parallel`,
`root_dir` (project root and, unless worktrees are enabled, agents' working
directory; see below),
`memory` ([agent-memory config](agent-memory.md)), `hooks`
([lifecycle hooks](lifecycle-hooks.md)), `side_agent`, `collaboration`,
`team` (see below), `worktrees`, `stages`.

`side_agent: true|false` overrides the dashboard
[side agent](dashboard.md#side-agent-a-free-form-chat-alongside-the-run) toggle for
this flow, taking precedence over `AFM_SIDEAGENT` and `config.yaml`. Omit it to defer
to those (default ON).

`collaboration: { max_depth: N }` overrides the helper delegation depth for this
flow. It has no `enabled` field; use `collaboration.enabled` in `config.yaml` or
`AFM_COLLABORATION` to switch helpers on or off. See
[Helpers and collaboration](collaboration.md).

### `team` — run agents of the flow

`team:` lists [run agents](collaboration.md#team-conversations) that afm adds
before the first stage starts, in the listed order — the same agents
**Add run agent** creates in the dashboard. Stage agents can write to them from
their first turn:

```yaml
team:
  - id: arch              # declaration key, not the @name
    profile: rev-arch     # a profile from collaboration.profiles
    title: Architecture   # the @name is made from it: @architecture
    instruction: Review changes for simplicity and duplication when asked.
```

`id`, `profile` and `title` are required; `instruction` is optional. `id` must
be unique in the team and match `[A-Za-z0-9._-]`. The `@name` is made from
`title`; when it is taken, the agent gets `-2`, `-3` and so on. With
collaboration enabled, an unknown profile stops `afm run` at preflight and fails
`afm validate`, which checks the flow as a fresh run; with collaboration
disabled, both only warn. Team agents get no
first message: each one waits until somebody writes to it. See
[Declare a team in flow.yaml](collaboration.md#declare-a-team-in-flowyaml) for
resume and disabled-collaboration behavior. Full example:
[`examples/agent-team-review/`](https://github.com/akopichin/afm/tree/main/examples/agent-team-review).

### `root_dir` — the project root for agents

Sets the working directory (CWD) in which stage agents run:

```yaml
name: my-feature
root_dir: /workspace      # a relative path is resolved from the afm root (--dir); empty — CWD of the afm process
stages: ...
```

By default the agent inherits the CWD of the `afm` process, and `afm` assumes the
project root matches the afm root (the parent of `.afm/`). If that's not the case —
for example, in a Docker setup where the sources are mounted at `/workspace` but
`.afm/` lives in a different directory — relative project paths (`docs/arch/…`, etc.)
resolve to different roots for different stages: one stage writes a file, another
can't find it. `root_dir` fixes a single root for all stages. Dialog paths
(`AFM_STAGE_DIR`) stay anchored to the afm root regardless of `root_dir`, and are
always passed to the agent as an absolute path — the agent's CWD can differ from
afm's, so a relative path would resolve to the wrong place.
When worktrees are enabled, `root_dir` instead anchors the worktree location;
stages run inside that worktree.

### Per-flow git worktrees

Set `worktrees: true` at the top of a flow to run its stages in a dedicated git
worktree. This is off by default. A flow setting overrides `AFM_WORKTREES` and
`config.yaml`'s `worktrees.enabled` in either direction. Pass
`afm run --worktree <name>` to use a named worktree for this invocation; the flag
enables worktrees even if the flow or config disables them.

```yaml
name: my-feature
worktrees: true
stages: ...
```

The project root is `root_dir` when set (relative paths resolve from `--dir`),
otherwise the parent of `.afm/`. It must be a git repository. afm creates
`<project-root>/.worktrees/my-feature` on branch `my-feature`, starting at the
project root's current `HEAD`. With `--worktree <name>`, that name replaces
`my-feature` in both the worktree path and branch. The effective worktree name
must be a single safe path component and git branch name (letters, digits,
`.`, `_`, `-`; it cannot start with `.` or `-`, end with `.` or `.lock`, or
contain `..`). Existing matching worktrees are
reused, including their uncommitted changes. If the branch is checked out
elsewhere, the target path belongs to another worktree, or the branch exists
without its worktree, afm stops with an error instead of overwriting it.

Agents and script stages use the worktree as their project root. Run state and
dialog files remain under the original `.afm/`; afm adds `.worktrees/` to the
repository's local `.git/info/exclude` when possible. Repeating `afm run` can
resume the latest unfinished run against the same worktree. Run directories use
the effective worktree name as their prefix (`.afm/runs/<name>-…`); the flow's
name in the dashboard and lifecycle events does not change.

To reset a flow's worktree, run `afm run --worktree-clear` to discard uncommitted
changes while keeping its branch and commits. Use
`afm run --worktree-clear-force` to discard the branch and its commits too, then
create it again from the project root's current `HEAD`. Both flags start a fresh
run, require worktrees to be enabled, and cannot be combined. Combine either
flag with `--worktree <name>` to reset that named worktree. If another
`afm run` is already using the same worktree, the new invocation exits with
a lock error; retry after the active run finishes.

With automatic Docker mode, afm prepares the worktree on the host and passes its
path into the container. The project root must be inside the directory mounted
from `--dir`; an external `root_dir` is rejected before Docker starts.
