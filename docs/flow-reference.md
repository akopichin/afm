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
| `plan` | no | Path to a ready-made plan file (skips planning) |
| `command` | no | AI command for this stage (overrides `client.command` from the config) |
| `max_parallel` | no | Limit on parallel stages for this command |
| `interactive` | no | `true` — enables the [file-based dialog protocol](interactive-stages.md) with the user via the dashboard |
| `auto_approve` | no | `true` — approve this stage's plan automatically the instant it's ready, with no human interaction — regardless of a dashboard being attached or `--require-approval`. Default `false`. Intended for CI (see [Dashboard → Auto-approving a plan](dashboard.md#auto-approving-a-stages-plan)) |
| `auto_run` | no | `false` — pause the stage the instant it's first eligible to start (`depends_on` satisfied), instead of starting immediately; it sits in `paused` until you hit **Continue** on the dashboard. Default `true` (starts on its own). Works on any stage type — regular, `agents: [auto]`, or `script`. Only gates the very first activation, not retries |
| `artifacts` | no | Files the stage produces for other stages (see [Artifacts & inputs](artifacts.md)) |
| `inputs` | no | Artifacts from dependency stages (`stage.artifact`, see [Artifacts & inputs](artifacts.md)) |
| `verify` | no | Stage-completion check(s), run after `.done` (or `execution_summary.md` for `agents: [auto]`): a shell command string (legacy), one step object (`{run: ...}` shell or `{command: ..., prompt: ...}` AI reviewer), an ordered list of steps (fail-fast), or a container object `{steps: [...], max_failures: N}` that also sets this stage's correction budget. A failing step gives the author a corrective retry with the report injected into its prompt, then `failed` once the budget is spent (default 1, or the global/per-stage `verify.max_failures`). See [AI-verify](verify.md) |
| `script` | no | Makes this a [script-only stage](script-stages.md): runs the given shell script (`sh -c`) instead of any AI agent — no planning, no approval. Mutually exclusive with `agents`/`command`/`interactive`/`plan`/`verify` |
| `script_timeout` | no | Hard timeout for `script` (default `5m`) |
| `script_before` | no | Shell script run immediately before this stage's own content (agent, autonomous track, interactive dialog, or another script). Works on any stage type |
| `script_before_timeout` | no | Hard timeout for `script_before` (default `5m`) |
| `script_after` | no | Shell script run right after the stage successfully completes |
| `script_after_timeout` | no | Hard timeout for `script_after` (default `5m`) |
| `buttons` | no | Named one-click prompts for the stage's live agent, shown in the dashboard kebab menu (see [Dashboard](dashboard.md)) |
| `reflect` | no | An object `{ file, mode }` that opts the stage into [agent memory](agent-memory.md): `file` is the stage's own Markdown memory file (relative to `memory.path`), `mode` is `r`/`w`/`rw` (default `rw`). Requires the flow-level `memory:` block |
| `memory_use` | no | Overrides the flow-level `memory.memory_use` for this stage (`true`/`false`; unset = inherit). Controls whether the stage **reads** [memory](agent-memory.md) |

!!! note "What makes a stage valid"
    Every stage needs *something to do*. The validator requires at least one of:
    a `planning` agent, a `plan` path, `interactive: true`, `agents: [auto]`, or
    `script:`. A `script:` stage must **not** also set `agents`/`command`/
    `interactive`/`plan`/`verify` (they're mutually exclusive), and `agents: [auto]`
    must be the stage's only agent.

## Flow fields (top level)

`name`, `description`, `prompt` (global instruction for all stages), `max_parallel`,
`root_dir` (project root = agents' working directory, see below),
`memory` ([agent-memory config](agent-memory.md)), `stages`.

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
(`AFM_STAGE_DIR`) stay anchored to the afm root regardless of `root_dir`.
