# afm

A CLI tool for orchestrating multi-stage AI tasks. Describe the task in a YAML file, break it into stages — afm runs AI agents sequentially or in parallel, waits for your approval of plans, and automatically carries out the implementation. Works with `claude` and with any claude-compatible agents (GLM, DeepSeek, Cursor, etc.).

## Contents

- [How It Works](#how-it-works)
- [Installation](#installation)
- [Quick Start](#quick-start)
- [Usage in Claude Code](#usage-in-claude-code)
- [The flow.yaml File](#the-flowyaml-file)
- [Autonomous Track](#autonomous-track-agents-auto)
- [Script Stages and Hooks](#script-stages-and-hooks)
- [Stage Lifecycle](#stage-lifecycle)
- [Configuration](#configuration)
  - [Debugging: `--debug`](#debugging---debug)
- [Web Dashboard](#web-dashboard)
- [Go SDK](#go-sdk)
- [Directory Structure](#directory-structure)
- [Development](#development)

## How It Works

Each stage goes through phases by default:

```
1. Planning   — AI builds a stage plan → you review and approve (or revise)
2. Execution  — AI implements the approved plan (+ optional code review)
```

Stages can run in parallel; dependencies via `depends_on` guarantee the correct order. Plans and artifacts of dependent stages are automatically substituted into the prompt.

**Autonomous track (optional).** A stage marked `agents: [auto]` skips planning/approval entirely: an agent with skills does the work in a single `autonomous_execution` step and writes `execution_summary.md` (the artifact dependent stages read instead of a plan). See [Autonomous Track](#autonomous-track-agents-auto).

**Reliability.** The state of every run is written to an event log `.afm/runs/<run>/events.jsonl` (append + fsync) — this is the single source of truth. If a run is interrupted, `afm run` automatically resumes from the same point: completed stages are skipped, interrupted ones are retried. While `afm run` is active, it holds an exclusive lock on the run directory (`.lock`) — a concurrent `afm approve/retry/revise` from another process can't corrupt the live log.

## Installation

**Via Homebrew (recommended):**
```bash
brew install --cask akopichin/afm/afm
afm install-skills   # optional: /afm, /afm-check, etc. in Claude Code
```

The binary is updated via `brew upgrade --cask afm`; skills don't need to be
reinstalled on update, but you can re-run `afm install-skills` if new ones
have appeared.

**From source:**
```bash
make build        # build into bin/afm
make install      # install via go install
```

**Prebuilt binary + Claude skills:**
```bash
./install.sh
```

The script copies the binary to `/usr/local/bin` and installs skills for Claude Code (`/afm`, `/afm-check`, `/afm-init`, `/afm-retry`, `/afm-review`).

### Running in Docker (without a local install)

```bash
docker run --rm -it \
  -v $(pwd):/project \
  -v ~/.claude:/home/afm/.claude \
  -v ~/.afm:/home/afm/.afm \
  -e AFM_HOST_UID=$(id -u) -e AFM_HOST_GID=$(id -g) \
  -e ANTHROPIC_API_KEY \
  akopichin/afm:latest \
  run flow.yaml
```

Or enable automatic Docker mode in the config — then the plain `afm run` command will restart itself inside the container:

```yaml
# .afm/config.yaml
docker:
  enabled: true
```

The image includes: claude CLI, Node 22, Python 3.12, Go 1.26, git. The container starts as root, but the entrypoint (`gosu`) immediately drops privileges to your host uid/gid — files in the mounted volumes belong to you, not root.

#### Authentication in Docker Mode

The Docker container is Linux — it has no access to the macOS Keychain where claude's OAuth sessions are stored. So the token needs to be passed explicitly via an environment variable — afm forwards it into the container automatically.

**Claude Pro/Max (claude.ai subscription)**

```bash
# One-time: generate a long-lived token
claude setup-token

# Add to ~/.zshrc / ~/.bashrc
export CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat01-...
```

**Anthropic API Key**

```bash
export ANTHROPIC_API_KEY=sk-ant-api-...
```

`ANTHROPIC_AUTH_TOKEN` and `ANTHROPIC_BASE_URL` are also supported — all of these are forwarded in bare form (`-e KEY` with no value), so the secret doesn't leak into `ps`/history.

#### Non-Standard Agents in Docker (autoShim)

If a stage uses a non-claude command (`command: glm51`, `command: deepseek`, etc.), Docker offers two options:

- **Mounting:** afm locates the binary via `which` and mounts it into the container (`:ro`). Works if the agent has no external dependencies.
- **autoShim (recommended):** with `docker.autoShim: true`, afm generates a claude-compatible wrapper right inside the container from the `docker.agents.<cmd>` recipe — without mounting the binary and without passing tokens through files. The secret is read on the host and passed in as a transient env var. Supported types are `claude` (default), `openai` (DeepSeek/OpenAI-compatible), `cursor` (Cursor Cloud Agents API), and `codex` (OpenAI Codex CLI — auth via a mounted `~/.codex` OAuth state instead of a secret in config).

```yaml
docker:
  autoShim: true
  agents:
    glm51:
      model: glm-5.1
      url: https://api.z.ai/api/anthropic
      auth: { from: "file:~/.ai-free/claude-glm/token", to: "env:ANTHROPIC_AUTH_TOKEN" }
```

Details and examples are in `config.example.yaml`, `example-flow-cursor.yaml`, and `CLAUDE.md` (Docker Mode section).

#### Project File Browser (Docker mode)

Inside a running Docker container the dashboard header shows a folder-icon button that opens a project file browser. This is Docker-only — it needs the host launcher's manifest of container mount paths (`AFM_IN_DOCKER=1` plus that manifest); a plain host run without Docker doesn't have it at all: `/api/files/*` returns `404` and the button isn't shown.

- **Enabled by default.** `docker.file_browser.enabled` (default `true` inside Docker mode) — set `false` to disable it.
- **What it does:** a lazy-loading source tree of the project mount and any `extra_mounts` explicitly opted in with `browse: true` (see below); opens text files with syntax highlighting for Go, TypeScript/TSX, JavaScript/JSX, and Python; shows a `HEAD → working tree` diff per file; lets you select one or more files and insert `[AFM file: "<absolute container path>"]` references into a plan review comment or a pending-question comment — the agent reads the file itself with its own tools, nothing is copied into `feedback.md`/the answer.
- **Strictly read-only.** `.git` and `.afm` are always hidden from the tree, even under an opted-in root. There's no create/edit/rename/delete from the dashboard.
- **Limits.** File content is capped at 2 MiB (a larger file shows an inline "file too large" message but can still be selected/referenced); diffs are capped at 4 MiB (truncated with a banner). Symlinks are listed but can't be opened. On a Linux host with an old kernel (< 5.6, no `openat2`) the feature degrades off automatically instead of falling back to a less-safe path check.
- **`extra_mounts` now accept an object form with `browse`** (the old plain-string list keeps working, unchanged):

  ```yaml
  docker:
    enabled: true
    file_browser:
      enabled: true          # optional; default true in Docker mode

    extra_mounts:
      - path: ../shared-contracts
        name: contracts       # optional UI label; default = basename(path)
        browse: true           # source mount — VISIBLE in the file browser

      - path: ~/.ai-free
        browse: false           # credential mount — mounted to the agent, NOT browseable

      - ~/.legacy-agent          # legacy scalar form still works too = browse:false
  ```

  This is a deliberate safe default: after upgrading afm, every existing `extra_mounts` entry (scalar, or object without `browse: true`) stays private — nothing new is exposed to the browser. Only add `browse: true` for a code root you're comfortable showing in the dashboard, never for a credentials/token directory.
- **Security: loopback-only port when the browser is on.** With the file browser enabled, the Docker dashboard port is published as `-p 127.0.0.1:<port>:<port>` instead of `-p <port>:<port>` — the dashboard (and everything the browser can read) stops being reachable from other hosts on your LAN. Docker runs with the file browser disabled keep the previous `0.0.0.0` publish behavior. If you relied on LAN access to the Docker dashboard, either set `file_browser.enabled: false` or plan for loopback-only access (e.g. an SSH tunnel).

## Quick Start

### 1. Create a flow

```bash
afm init
```

Walks you through one of four archetypes — a single change
(planning → implementation → review), a build + verify loop, parallel
tracks merging into an integration stage, or fully custom
stage-by-stage — then asks per-stage questions (agent mode, plan vs.
planning agent, which phases to run, and optional artifacts/inputs/
verify/interactive/custom-command settings). The result is validated
before the wizard reports success. Or write `flow.yaml` by hand — see
the example below.

```bash
afm validate flow.yaml
```

Checks a flow.yaml for structural errors (dependency cycles, unknown
`depends_on`/`inputs` references, …) without running any agents. The
wizard runs this automatically after generating a file; run it yourself
after hand-editing a flow.

### 2. Run

```bash
afm run flow.yaml

# If the flow lives in .afm/flows/ — you can omit the argument:
afm run
```

By default a web dashboard comes up (`http://localhost:9876`); its URL is printed to the log.

### 3. Approve plans

After the planning phase, each stage transitions to `awaiting_approval`. There are two ways to do this:

**Via the web dashboard** — open `http://localhost:9876`, select a stage, review the plan line by line, leave inline comments on specific lines (like in an MR), and click "Approve" or "Send revision".

**Via the CLI:**
```bash
# View the plan
cat .afm/runs/<run-dir>/<stage-id>/plan.md

# Approve
afm approve backend-auth

# Not happy with it — ask for a redo
afm revise backend-auth --feedback "Need to add Redis for the token blacklist"

# Retry a failed stage
afm retry backend-auth
```

> CLI mutations (`approve`/`revise`/`retry`) work when `afm run` is NOT running (headless scenario). While `afm run` is active, approve through the dashboard — otherwise the command will report that the run is locked.

### 4. Follow progress

```bash
afm check
```

```
Run: jwt-auth-20260416-152543-a3f9

STAGE                 STATUS                 UPDATED
-----                 ------                 -------
backend-auth          done                   15:31:02
frontend-login        running                15:31:45
integration-tests     pending                15:31:02
```

Or in real time via the web dashboard — stages, progress bar, event feed, logs.

## Usage in Claude Code

After `./install.sh` the following skills are available:

- `/afm` — runs a flow, monitors it, and requests plan approvals right in the chat
- `/afm-check` — shows the status of the current run
- `/afm-init` — creates flow.yaml interactively
- `/afm-retry` — retries a failed stage
- `/afm-review` — view a stage plan with feedback/approval

## The flow.yaml File

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

**Stage fields:**

| Field | Required | Description |
|------|-------------|----------|
| `id` | yes | Unique identifier (letters/digits/`_`/`-`) |
| `name` | no | Human-readable name for logs and the dashboard (if empty — `id` is shown) |
| `description` | yes | Task description for the AI (background/context) |
| `prompt` | no | Explicit instruction for the agent — a separate `<prompt>` block after the context. Unlike `description`, this is a direct instruction on what to do. It's escaped and cannot inject XML tags |
| `agents` | yes | Combination of `planning`, `implementation`, `review` |
| `depends_on` | no | IDs of stages that must complete first |
| `eager_planning` | no | `true` — planning starts immediately when the flow runs, without waiting for `depends_on` |
| `skills` | no | Claude skills for the agent |
| `plan` | no | Path to a ready-made plan file (skips planning) |
| `command` | no | AI command for this stage (overrides `client.command` from the config) |
| `max_parallel` | no | Limit on parallel stages for this command |
| `interactive` | no | `true` — enables the file-based dialog protocol with the user via the dashboard (see below) |
| `auto_approve` | no | `true` — approve this stage's plan automatically the instant it's ready, with no human interaction — regardless of a dashboard being attached or `--require-approval`. Default `false`. Intended for CI (see "Auto-Approving a Stage's Plan" below) |
| `auto_run` | no | `false` — pause the stage the instant it's first eligible to start (`depends_on` satisfied), instead of starting immediately; it sits in `paused` until you hit **Continue** on the dashboard. Default `true` (starts on its own). Works on any stage type — regular, `agents: [auto]`, or `script`. Only gates the very first activation, not retries (see "Pausing a Stage Before It Starts" below) |
| `artifacts` | no | Files the stage produces for other stages |
| `inputs` | no | Artifacts from dependency stages (`stage.artifact`) |
| `verify` | no | Shell command run after `.done`. Exit ≠ 0 — the stage is not counted as complete: one retry with the command's output in the prompt, then `failed`. Guards against a false "done" |
| `script` | no | Makes this a script-only stage: runs the given shell script (`sh -c`) instead of any AI agent — no planning, no approval. Mutually exclusive with `agents`/`command`/`interactive`/`plan`/`verify` |
| `script_timeout` | no | Hard timeout for `script` (default `5m`) |
| `script_before` | no | Shell script run immediately before this stage's own content (agent, autonomous track, interactive dialog, or another script). Works on any stage type |
| `script_before_timeout` | no | Hard timeout for `script_before` (default `5m`) |
| `script_after` | no | Shell script run right after the stage successfully completes |
| `script_after_timeout` | no | Hard timeout for `script_after` (default `5m`) |
| `reflect` | no | An object `{ file, mode }` that opts the stage into agent memory: `file` is the stage's own Markdown memory file (relative to `memory.path`), `mode` is `r`/`w`/`rw` (read / write / both, default `rw`). Requires the flow-level `memory:` block. See "Agent Memory" below |
| `memory_use` | no | Overrides the flow-level `memory.memory_use` for this stage (`true`/`false`; unset = inherit). Controls whether the stage **reads** memory. See "Agent Memory" below |

**Flow fields (top level):** `name`, `description`, `prompt` (global instruction for all stages), `max_parallel`, `root_dir` (project root = agents' working directory, see below), `memory` (agent-memory config, see "Agent Memory" below), `stages`.

**`root_dir` — the project root for agents.** Sets the working directory (CWD) in which stage agents run:

```yaml
name: my-feature
root_dir: /workspace      # a relative path is resolved from the afm root (--dir); empty — CWD of the afm process
stages: ...
```

By default the agent inherits the CWD of the `afm` process, and `afm` assumes the project root matches the afm root (the parent of `.afm/`). If that's not the case — for example, in a Docker setup where the sources are mounted at `/workspace` but `.afm/` lives in a different directory — relative project paths (`docs/arch/…`, etc.) resolve to different roots for different stages: one stage writes a file, another can't find it. `root_dir` fixes a single root for all stages. Dialog paths (`AFM_STAGE_DIR`) stay anchored to the afm root regardless of `root_dir`.

### Agent Memory

Each stage runs its agent in an isolated context — whatever it learns (an API's real behavior, a required build flag, a rule it broke and had to correct) is lost when the stage finishes, and the next stage starts blind. **Agent memory** carries those lessons forward: after a stage that opts in completes, afm runs a small background pipeline that distills the stage's session into a handful of durable **project patterns** and merges them into a plain Markdown rules file that later stages — and later runs — are told to read.

Memory is entirely **opt-in** and off by default. Nothing runs and nothing is written unless you add a `memory:` block to the flow and mark at least one stage with `reflect:`.

#### Turning it on

```yaml
name: my-feature
root_dir: .
memory:
  path: docs/memory        # a DIRECTORY (relative to root_dir); a non-empty value ENABLES the feature
  mode: rw                  # lifecycle of the shared memory.md: r / w / rw (default rw)
  memory_use: true          # do stages READ memory at all? default false → you opt in
  max_rules: 25             # max patterns kept per file (default 25)
  commit: false             # git-commit the memory directory at end of run (default false, no push)
stages:
  - id: build
    name: build
    agents: [planning, implementation]
    reflect: { file: build.md, mode: rw }   # this stage's own file: writes AND reads it
  - id: test
    name: test
    agents: [implementation]
    depends_on: [build]
    reflect: { file: build.md, mode: r }    # reads build's file, writes nothing of its own
  - id: docs
    name: docs
    agents: [implementation]
    depends_on: [build]
    memory_use: false                        # opt THIS stage out of reading memory entirely
```

**Flow-level `memory:` fields:**

| Field | Default | Meaning |
|-------|---------|---------|
| `path` | — | Directory (relative to `root_dir`) where memory files live. **A non-empty value is what enables the whole feature.** |
| `mode` | `rw` | Lifecycle of the **shared `memory.md`**: `r` = read-only (injected, never rewritten), `w` = write-only (updated at end of run, never injected), `rw` = both. |
| `memory_use` | `false` | Master switch for **reading** memory into stage prompts. Off by default — you opt in globally (or per stage). Does not affect writing. |
| `max_rules` | `25` | Maximum number of `## Pattern` blocks kept in each file. When the distiller would exceed it, it drops Low-priority patterns first, then Medium, preserving High. |
| `commit` | `false` | When `true`, afm runs `git add`/`git commit` **scoped to the memory directory** at the end of the run (only if something changed; never pushes). |

**Per-stage fields:**

| Field | Default | Meaning |
|-------|---------|---------|
| `reflect` | — | `{ file, mode }` — the stage's own memory file (relative to `memory.path`) and its `mode` (`r`/`w`/`rw`, default `rw`). Controls **this stage's own file** only. |
| `memory_use` | inherit | Overrides the flow-level `memory_use` for this stage: `true`/`false`. Unset = inherit the global value. Controls **reading** only. |

#### How reading and writing are decided

Reading and writing are two **independent** axes.

**Reading (what memory the stage's agent is pointed at):**
1. First a participation gate — `memory_use`, resolved as *stage `memory_use` if set, else the flow-level `memory_use` (default `false`)*. If it resolves to `false`, the stage gets **no memory at all**.
2. If it participates, it is pointed at:
   - the shared **`memory.md`** — only if `memory.mode` includes read (`r`/`rw`) and the file exists;
   - its **own `reflect.file`** — only if it has `reflect` with `mode` `r`/`rw` and the file exists.

**Writing (what gets distilled after the run):**
- a stage's **own `reflect.file`** is written if its `reflect.mode` includes write (`w`/`rw`) — independent of `memory_use`;
- the shared **`memory.md`** is (re)written by the end-of-run pass only if `memory.mode` includes write (`w`/`rw`).

So, for example: `memory.mode: r` + stages with `reflect: {mode: rw}` gives you a **read-only shared memory** (curated by hand, never overwritten) while each stage still reads and writes its own file. And with the default `memory_use: false`, reflect stages still *write* their files but nothing is *read* until you flip `memory_use: true`.

#### What ends up on disk

Two tiers of files, both plain Markdown, all under `<memory.path>/`:

- **`memory.md`** — the project-wide rules file. Accumulates **across runs** (keep it in git). It is injected into a stage's prompt when the stage participates (`memory_use`) and `memory.mode` allows reading — see "How reading and writing are decided" above.
- **`<reflect.file>`** (e.g. `build.md`) — a per-stage file, rewritten by that stage's write chain, injected only into a participating stage that names it with `mode` `r`/`rw`.

Both files have the same shape — a flat list of named patterns, highest-priority first (priority is encoded **only** by block order, never written into the file):

```markdown
# Project rules

## Single Source of Truth Propagation

Treat the project's canonical config file as authoritative and propagate its exact values into every derived output.

## Exact Path Fidelity

Reproduce target paths precisely and verify the written file resolves to the intended location before declaring success.
```

afm reads the memory content by **pointing the agent at the file paths** (the agent reads them itself with its normal tools); it does not paste the file contents into the prompt, so prompts don't grow as memory grows.

#### How a stage's session becomes patterns

The distill chain runs **once per reflecting stage** (writing that stage's own file) and **once more at the end of the run** (aggregating every stage's session into `memory.md`). Four steps:

1. **reflect** — a fresh-context agent reads the stage's session log and produces a raw RL-style dataset (`reflect_dataset.yaml`). Its prompt carries a hard **exclude list** for afm/agent-protocol mechanics (execution-summary format, stage directories, approval/retry flow, "read the memory files", generic SWE platitudes) so memory stays about **your project**, not the framework.
2. **aggregate** — turns the dataset into a mutually-exclusive numbered list of named patterns.
3. **prioritize** — buckets every pattern into High / Medium / Low.
4. afm code keeps only the **High** patterns, then **update** — merges them into the target file (preferring to fold into existing patterns), caps at `max_rules`, and rewrites the file.

The pipeline is **background and best-effort**: it never blocks downstream stages, never touches a stage's status, and never fails a stage or the run — if a step errors you get a `reflect_failed` notice in the dashboard and nothing else. The four prompts ship as embedded defaults (`reflect.md` / `aggregate.md` / `prioritize.md` / `update.md`) and can be overridden per project via the `prompts_dir` config.

#### Practical notes

- **Cross-run is the main payoff.** Because reflection runs in the background after a stage finishes, a *fast* downstream stage in the **same** run may start before the previous stage's memory has been written — so in-run forward carry isn't guaranteed. What *is* reliable is accumulation **across runs**: `memory.md` and the per-stage files are read at the start of every run, so each run stands on the previous run's lessons.
- **Commit it (or ignore it).** `memory.md` is meant to live in your repo and grow over time. If you'd rather not track it, add `<memory.path>/` to `.gitignore`; if you want afm to commit it automatically after each run, set `commit: true`.
- **Script stages** may declare `reflect:` for reading, but their write chain is always skipped — there is no agent session to reflect on.

### Passing Context Between Stages

Plans (and the `execution_summary.md` of autonomous stages) of dependent stages are automatically added to the prompt via `depends_on`. To pass file artifacts, use `artifacts` + `inputs`:

```yaml
stages:
  - id: backend
    artifacts:
      - name: api-contract
        path: docs/api-contract.yaml
        description: "OpenAPI schema"
      - name: db-schema
        path: ./schema.sql           # ./ = the stage directory in the run
        description: "SQL migration"
        inline: false                 # pass the path, not the content

  - id: frontend
    depends_on: [backend]
    inputs:
      - backend.api-contract          # required artifact
      - ref: backend.db-schema        # optional
        optional: true
```

- `inline: true` (default) — the file's content is inserted into the prompt
- `inline: false` — the file's path is passed into the prompt instead
- `optional: true` — if the file isn't found, the stage runs without it

### Interactive Stages

A stage with `interactive: true` gets a file-based protocol for dialog with the user through the dashboard. The agent receives the `AFM_STAGE_DIR` env variable (the path to the stage directory). To ask a question, the agent writes a `<phase>.q<N>.question.json` file (`<phase>` is `planning`/`implementation`/`review`; `N` increments: q1, q2, …), then waits for `<phase>.q<N>.answer.json` to appear via a bash loop. A "Dialog" section appears in the dashboard where the user answers. While there's no answer, the stage sits in `awaiting_user_input` status; once answered, execution continues.

When launching `claude`, the flags `--print --output-format stream-json --verbose --dangerously-skip-permissions` are always added (`--verbose` is required for stream-json in Claude Code 2.1.x). If an interactive agent mistakenly writes `question.json` outside `$AFM_STAGE_DIR` (a GLM-4.7 bug: path taken from CWD instead of the env var), the poller auto-relocates the file into stageDir and creates a symlink for the answer — the stage moves into `awaiting_user_input` instead of hanging.

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

Full example: `example-flow-interactive.yaml`.

> **Waiting for an answer and idle-timeout.** While a stage waits for an answer, the agent is idle and writes nothing to stdout. By default `executor.idle_timeout` = 30 min — if you don't answer within that time, the waiting agent may be killed. For long waits, raise the timeout: `executor: { idle_timeout: 24h }`.

## Autonomous Track: `agents: [auto]`

Most stages go through the full planning → approval → implementation cycle. A stage that doesn't need that ceremony — a simple, well-defined task — can run on the **autonomous track** instead: set `agents: [auto]` and the stage is executed by a single autonomous agent in one step, with **no `plan.md`, no approval gate**. The agent (with its skills) does the work right away and is required to write `execution_summary.md`, which serves as the artifact for dependent stages in place of a plan. Interactive dialog is still available on an autonomous stage.

```yaml
stages:
  - id: sync-manifests
    description: "Sync the CODEMANIFEST files with the code"
    agents: [auto]        # autonomous — one step, no plan, no approval
```

`auto` must be the stage's only agent (`agents: [auto]`, nothing else) — the flow parser rejects combining it with other agent phases.

> **Note.** Earlier versions had an optional LLM *supervisor* that decided per-stage whether to collapse into the autonomous track. It was removed as redundant — declare `agents: [auto]` statically instead. The old `supervisor` / `supervisor_prompt` / `supervisor_command` keys are ignored (afm prints a non-fatal warning if it sees them).

## Script Stages and Hooks

A stage can run a plain shell script instead of an AI agent — useful for glue steps (notifications, deploy commands, a linter run) that don't need an LLM:

```yaml
stages:
  - id: notify
    script: |
      curl -s -X POST https://hooks.example/notify -d '{"status":"started"}'
```

A `script` stage skips planning/approval entirely: as soon as its `depends_on` are done, the script runs, and the stage moves straight to `done`/`failed` based on the exit code.

**`script_before` / `script_after`** are hooks that run immediately before/after *any* stage's own content — orthogonal to the stage type, so they combine freely with `agents`/`interactive`/etc.:

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
- If `script_before` still fails after retries, the stage blocks in `hook_failed` — resolve it from the dashboard with **Retry** (re-run the hook) or **Skip** (proceed to the stage's own content anyway).
- If `script_after` still fails, it does **not** revert the stage — it's already `done`. You get the same Retry/Skip notice, but the stage's status is unaffected either way.
- Output from `script`/`script_before`/`script_after` streams to the dashboard's event feed and log panel just like an agent's.

## Stage Lifecycle

```
pending → planning → awaiting_approval → ready → running → done
                ↓                                     ↓        ↘ failed
                └────→ awaiting_user_input ←──────────┘
         ↑                                         ↓
         └───────── revising ←────────────────────┘

# autonomous track (agents: [auto]):
pending → running(autonomous_execution) → done
```

- `pending` — not started yet; planning starts once all `depends_on` are complete (unless `eager_planning: true`)
- `planning` — the AI builds a plan
- `awaiting_approval` — the plan is ready, awaiting approval (web or CLI)
- `ready` — the plan is approved, waiting its turn
- `running` — the AI implements the plan (or runs the autonomous track)
- `awaiting_user_input` — an interactive stage is waiting for a user answer; once answered, it returns to the phase where the question was asked
- `revising` — feedback was sent and the AI is reworking: either the plan (from `awaiting_approval`), or a `running` stage that just got a note and a graceful interrupt (see "Suggesting a Note to a Running Stage" below)
- `retrying` — a transient error (rate limit / 5xx), auto-retry with backoff
- `hook_failed` — a `script_before` hook exhausted its retries; the stage is blocked until you hit **Retry** or **Skip** on the dashboard (a `script_after` failure never uses this status — the stage stays `done`)
- `paused` — waiting for you to hit **Continue** on the dashboard: either gated by `auto_run: false` on first activation, or manually paused mid-run via the kebab (⋮) menu (see "Pausing a Stage Before It Starts" below)
- `done` / `failed` — complete

## Configuration

### Working Directory

By default `.afm/` is created in the current folder. To move it elsewhere:

```bash
# Flag (one-off run)
afm --dir ~/my-flows run

# Environment variable (persistent)
export AFM_DIR=~/my-flows
afm run
```

All commands (`run`, `check`, `approve`, `revise`, `retry`, `init`, `list`) respect `--dir`. Priority: `--dir` flag > `AFM_DIR` env var > current directory.

### Debugging: `--debug`

Run with `--debug` (or `AFM_DEBUG=1`) to log the **exact prompt sent to each agent** (stdin), with timestamps and stage/phase tags:
- `.afm/runs/<run>/debug.log` — one chronological log across all stages/phases;
- `.afm/runs/<run>/<stage>/<phase>.prompt.log` — per-stage/phase (appends across retries).

Off by default. The logs contain full project context passed to the agent (not secrets/env) — they live under `.afm/runs/` and aren't committed. Only the input is logged; agent output is already in `<phase>.jsonl`/`.log`. In Docker mode, `--debug`/`AFM_DEBUG` on the host is passed through into the container automatically.

Create `.afm/config.yaml` in the project or `~/.afm/config.yaml` globally (full example — `config.example.yaml`):

```yaml
client:
  command: claude           # the AI command (default: claude)
  # extra_args: [--my-flag] # extra arguments
  # claude_bare: false      # true → add --bare to generated wrappers (lighter load,
                            #        but disables skill auto-discovery). Default: false

executor:
  idle_timeout: 30m         # agent idle timeout
  max_parallel: 4           # max parallel stages (0 = unlimited)
  truncate_output: 0        # max chars for logged agent text/Bash commands (0 = no limit, default)

server:
  port: 9876                # web dashboard port
  open_browser: false       # open the browser on startup (default: false)

# theme: coffee             # dashboard theme: coffee | goga | novacorps (default: coffee)
# prompts_dir: .afm/prompts/  # custom prompt templates
# auto_recover: true        # auto-retry failed stages on run start/resume (default: true)

docker:
  enabled: false            # true / env AFM_USE_DOCKER=1 — restart inside a container
  # image: akopichin/afm:latest
  # autoShim: true          # generate claude wrappers for agents.<cmd> inside the container
  # file_browser:
  #   enabled: true          # dashboard file browser; default true in Docker mode
  # extra_mounts: [~/.ai-free]  # extra host paths into the container (:ro); each entry can
  #   # also be {path, name, browse} — browse:true exposes it in the file browser (see
  #   # "Project File Browser (Docker mode)" above); scalar entries stay browse:false
  # agents:                 # recipes for autoShim (see config.example.yaml)
  #   glm51: { model: glm-5.1, url: https://api.z.ai/api/anthropic,
  #            auth: { from: "file:~/.ai-free/claude-glm/token", to: "env:ANTHROPIC_AUTH_TOKEN" } }
```

Settings priority (highest to lowest):
1. CLI flags (`--max-parallel`, `--port`, `--require-approval`)
2. The project's `.afm/config.yaml`
3. The global `~/.afm/config.yaml`
4. Default values

## Web Dashboard

On startup (if `server.open_browser: true`) the dashboard opens; otherwise its URL is printed to the log.

- **Left panel** — list of stages with colored status indicators; the stage's `name` is shown under `id` (if set). The center panel's header also shows `name`, otherwise `id`
- **Center panel** — the plan with line-by-line review and inline comments, the agent log (markdown), a "Dialog" section for interactive stages
- **Right panel** — an event feed from all stages with source badges
- **Progress bar** — at the bottom, showing how many stages are complete
- **Folder icon (Docker mode only)** — opens the project file browser: browse the source tree, view files with syntax highlighting, check a per-file `HEAD → working tree` diff, and insert file references into plan/question comments. See [Project File Browser (Docker mode)](#project-file-browser-docker-mode).

### Themes

The dashboard ships with three built-in themes; choose one with `theme:` in `.afm/config.yaml`:

- **`coffee`** (default) — warm coffee palette: a "valve-glow" amber dark mode and a cream "latte" light mode, with a matcha accent for user-dialog states.
- **`goga`** — flat dark tech theme (teal accent, sans-serif, Goga wordmark).
- **`novacorps`** — the previous hi-tech theme (mint accent, monospace, scanline/neon decor).

Empty or unknown values fall back to `coffee` (an unknown value logs a warning to stderr). Light vs. dark mode is toggled inside the dashboard itself and is independent of the theme choice. A fully custom skin can be supplied via the top-level `skin_dir:` config option (a directory containing `index.css`), which overrides the built-in theme.

### Inline Plan Comments

When a stage is in `awaiting_approval`:
1. Click a plan line — a comment form opens
2. Write a remark — the line highlights yellow
3. Click "Send revision (N)" — all comments are sent to the agent with line numbers

### Suggesting a Note to a Running Stage

Normally you can only redirect a stage at the `awaiting_approval` checkpoint (see "Inline Plan Comments" above). You can also do it while a stage is actively `running`:

1. Click the kebab (⋮) menu on a `running` (or `awaiting_approval`) stage row and choose "Add a note for the agent".
2. Type the note and send — the agent finishes its current step, then receives SIGINT (a graceful interrupt, not a kill).
3. The stage moves through `revising` and restarts the same phase (planning/implementation/review/autonomous) with your note folded into its context, then continues toward `done`.

### Auto-Approving a Stage's Plan

Set `auto_approve: true` on a stage to skip the human approval checkpoint entirely — useful for CI runs where some stages need review and others don't:

```yaml
stages:
  - id: lint
    agents: [planning, implementation]
    auto_approve: true    # no human ever needs to click Approve for this stage
  - id: deploy
    agents: [planning, implementation]
    depends_on: [lint]    # still requires a human Approve — auto_approve not set
```

The plan is approved the instant it's ready, whether or not a dashboard is attached and regardless of `--require-approval` (which normally fails a headless run with no dashboard). If the dashboard is open, the stage's plan is still shown, with an "Auto-approved" badge in place of the Approve/Revise buttons.

### Pausing a Stage Before It Starts

Set `auto_run: false` on a stage to hold it at `paused` the instant it becomes eligible to start (once `depends_on` are satisfied), instead of running unattended — useful for a checkpoint you want a human to explicitly kick off (e.g. a stage that commits/deploys):

```yaml
stages:
  - id: build
    agents: [planning, implementation]
  - id: deploy
    agents: [planning, implementation]
    depends_on: [build]
    auto_run: false    # waits for a human to click Continue before it starts
```

The dashboard shows a Continue button instead of the stage running on its own. This only gates the stage's very first activation — once it's been through a pause/Continue cycle, later retries (e.g. after a `failed`) run normally without pausing again.

You can also pause any `running`/`planning`/`revising`/`retrying` stage mid-flight from the kebab (⋮) menu — the same "Pause" action, just triggered manually instead of by `auto_run: false`. The agent gets a graceful interrupt (SIGINT), and Continue resumes it exactly where recovery-on-restart would (`--resume` for agents that support it). Script-only stages (`script:`) can only be paused via `auto_run: false`, before they start — a script already running can't be gracefully interrupted mid-way.

### Resume on Restart

On a repeated `afm run`, the tool automatically:
- Skips completed stages (`done`)
- Preserves stages awaiting approval (`awaiting_approval`)
- Restarts interrupted stages (`planning`, `running`, `revising`, `retrying`)
- Restores autonomous stages (from `execution_summary.md` / `autonomous.flag`)
- Preserves stages in `awaiting_user_input`: question/answer files survive the restart, an unanswered question is shown again in the dashboard, and once answered the stage continues
- **Auto-retries `failed` stages** (`auto_recover`, default `true`): if a run was interrupted hard enough that a stage landed in `failed` (e.g. the process or Docker container was killed), the next `afm run` resets every failed stage back to `pending` before doing anything else — no manual `afm retry <id>` needed. All failed stages are reset regardless of why they failed; dependency order (`depends_on`) is preserved automatically, since a reset stage just re-enters the normal pending flow. Set `auto_recover: false` in `.afm/config.yaml` to go back to requiring manual `afm retry` for each failed stage.

Approve/revise/retry are durably recorded in the log (fsync) before control returns — a crash right after approval doesn't lose the intent; recovery continues from the correct state.

## Go SDK

Need to drive afm from a Go service instead of the CLI — start a flow as a subprocess, poll its progress, and call approve/retry/revise while it's running, e.g. to expose your own HTTP endpoints for watching progress in a browser? See [`sdk/README.md`](sdk/README.md) for the `afmsdk` Go module.

## Directory Structure

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

## Development

Once after cloning — enable the pre-commit hook (lint + build + test before every commit):

```bash
git config core.hooksPath .githooks
```

The hook lives in `.githooks/pre-commit` and is versioned with the repository, but the
`core.hooksPath` setting itself is local git configuration, so it must be applied separately
in each clone. To skip it once: `git commit --no-verify`.

```bash
make build        # build (bin/afm)
make test         # tests (with -race)
make lint         # linter
make install      # go install
make install-skills   # install the /afm-* skills into ~/.claude
make docker-build     # build the Docker image
make clean        # remove build artifacts
```

Versioned release: `make release-patch` / `release-minor` / `release-major` bumps the SemVer tag and pushes it; the actual build (docker image `:vX.Y.Z` + `:latest`, binaries, GitHub Release, Homebrew cask) happens in GitHub Actions (`.github/workflows/release.yml`) once the tag is pushed. A push to `main` releases a patch version automatically — running `make release-patch` by hand is rarely needed.
