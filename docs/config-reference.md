# Configuration

!!! tip "Editor autocomplete & validation"
    afm ships a JSON Schema for `config.yaml`. Add this line at the top of your config
    for autocomplete and validation in VS Code / JetBrains:

    ```yaml
    # yaml-language-server: $schema=https://raw.githubusercontent.com/akopichin/afm/main/schema/config.schema.json
    ```

Create `.afm/config.yaml` in the project or `~/.afm/config.yaml` globally. The full
annotated example is [`config.example.yaml`](https://github.com/akopichin/afm/blob/main/config.example.yaml).

```yaml
client:
  command: claude           # the AI command (default: claude)
  # extra_args: [--my-flag] # extra arguments
  # claude_bare: false      # true → add --bare to generated wrappers (lighter load,
                            #        but disables skill auto-discovery). Default: false
  # reasoning: medium       # reasoning effort for the default claude client, mapped to
                            #        --effort. Levels: minimal, low, medium, high, xhigh, max
                            #        (clamped to claude's set). Empty → not passed. See below

executor:
  idle_timeout: 30m         # agent idle timeout
  max_parallel: 4           # max parallel stages (0 = unlimited)
  truncate_output: 0        # max chars for logged agent text/Bash commands (0 = no limit, default)

# usage_limit:
#   resume_margin: 60s       # wait after a known provider reset (default: 60s)
#   probe_interval: 15m      # retry when reset time is unknown (default: 15m)
#   max_wait: 12h            # pause if the reset is farther away (default: 12h)
#   max_waits: 4             # pause after more than 4 consecutive refusals (default: 4)

server:
  port: 9876                # web dashboard port
  open_browser: false       # open the browser on startup (default: false)

# theme: graphite           # dashboard theme: graphite | goga | novacorps (default: graphite; legacy "coffee" → graphite)
# prompts_dir: .afm/prompts/  # custom prompt templates
# skills_dir: /opt/team-skills  # default extra local skill library for runs
# auto_recover: true        # auto-retry failed stages on run start/resume (default: true)

# command_names:            # readable dashboard names for agent commands; see "Executor names"
#   "./scripts/dev-agent.sh": Development Agent
#   codex-as-claude: Codex Reviewer
# unknown_command_name: Unknown  # label prefix when the server sent no name (default: Unknown)

# side_agent:
#   enabled: false          # dashboard Assistant; ON by default. env AFM_SIDEAGENT overrides,
#                           # and flow.yaml side_agent overrides both. See dashboard.md

# collaboration:
#   enabled: true           # stage helpers; ON by default. env AFM_COLLABORATION overrides
#   mode: team              # dashboard: team (default) | assistants; flow.yaml may override
#   hide_attach_helpers: true # hide the manual Attach helper buttons; OFF by default.
#                           # env AFM_HIDE_ATTACH_HELPERS overrides; no flow.yaml override
#   runtime_context: isolated # helper process environment: isolated (default) | wrapper
#   max_depth: 3            # helper delegation depth; flow.yaml may override it
#   limits: { max_tasks: 20 } # optional whole-run task limit
#   profiles:               # locally configured roles; see collaboration.md
#     reviewer: { title: Code reviewer, instructions: Review the stage }  # runs the executor it is attached to
#     codex-reviewer: { command: codex-as-claude, runtime_context: wrapper,
#                       instructions: Review the stage, delegate: false }

# worktrees:
#   enabled: true           # per-flow git worktrees; OFF by default. env AFM_WORKTREES
#                           # overrides, and flow.yaml worktrees overrides both

# add_stage:
#   enabled: false          # allow adding stages during a run; ON by default.
#                           # env AFM_ADD_STAGE overrides; no flow.yaml override

accounting:
  enabled: false            # hide cost display everywhere (dashboard + afm check/report);
                            # ON by default, env AFM_ACCOUNTING overrides. Data is still
                            # collected into usage.jsonl — this hides the display, not the data.
  show_money: false         # also show monetary ($) figures (Est. cost); OFF by default —
                            # tokens are always shown, money is opt-in. env
                            # AFM_ACCOUNTING_SHOW_MONEY overrides.
                            # Both switches also pick what Models & agents shows (tokens,
                            # money); agent jobs and their time are always recorded. The
                            # data.analytics hook block ignores both and never has money.

verify:
  max_failures: 1           # how many verify rejections (needs_changes) the author may
                            # correct before the stage fails (default 1; 0 = strict, no
                            # correction). Per-stage override: the container form of a
                            # stage's verify: field. See verify.md
  review_patience: 0        # how many CONSECUTIVE rejections that left the working tree
                            # unchanged are tolerated before the stage is accepted as
                            # complete (reviewer/author stalemate). Default 0 = disabled.
                            # Needs max_failures >= review_patience to engage. Per-stage
                            # override: the container form of a stage's verify: field
  fresh_correction: true    # start a new interactive author session after a verify
                            # rejection (default true); false reuses the session.
                            # Per-stage override: the container form of verify:

docker:
  enabled: false            # true / env AFM_USE_DOCKER=1 — restart inside a container
  # image: akopichin/afm:latest
  # autoShim: true          # generate claude wrappers for agents.<cmd> inside the container
  # file_browser:
  #   enabled: false         # dashboard file browser; ON by default, env AFM_FILE_BROWSER overrides
  # extra_mounts: [~/.ai-free]  # extra host paths into the container (:ro); each entry can
  #   # also be {path, name, browse} — browse:true exposes it in the file browser
  # agents:                 # recipes for autoShim (see config.example.yaml)
  #   glm51: { model: glm-5.1, url: https://api.z.ai/api/anthropic,
  #            auth: { from: "file:~/.ai-free/claude-glm/token", to: "env:ANTHROPIC_AUTH_TOKEN" } }
```

A recipe of `type: cursor` runs the local `cursor-agent` CLI from the Docker image: it
needs no `url` (a value is ignored), its `auth.to` is `env:CURSOR_API_KEY` and `model`
defaults to `auto`. See [Docker mode](docker.md#non-claude-agents-in-docker-autoshim).

`collaboration.mode` selects the dashboard presentation: `team` (the default for
existing configs) or `assistants`. The value in `flow.yaml` takes precedence over
project `.afm/config.yaml`, then global `~/.afm/config.yaml`. The mode does not
enable or disable collaboration; `AFM_COLLABORATION` and `collaboration.enabled`
still control that separately. See [Collaboration modes](collaboration.md#collaboration-modes).

`collaboration.hide_attach_helpers: true` hides the dashboard buttons that attach a
helper to a stage by hand: **Attach helper to stage…** in People and **Attach helper…**
in the stage header. It is `false` by default. Precedence is the non-empty
`AFM_HIDE_ATTACH_HELPERS` environment variable, then project `.afm/config.yaml`, then
global `~/.afm/config.yaml`, then `false`; `flow.yaml` cannot set it. In the
environment, exactly `1` or `true` hides the buttons and any other non-empty value
(such as `0`, `false` or `yes`) shows them; an empty or unset variable leaves the
config value in effect.

```bash
AFM_HIDE_ATTACH_HELPERS=1 afm run flow.yaml   # hide the buttons for this run
AFM_HIDE_ATTACH_HELPERS=0 afm run flow.yaml   # show them even if config says true
```

The setting is read when `afm run` starts or resumes a run, so changing it needs a
restart. It only hides the two buttons: collaboration stays enabled, and
**Attach to stage** in the Feed `@` menu, `afm agent spawn`, the API and `assistants:`
in YAML keep working. To switch helpers off entirely use `collaboration.enabled` or
`AFM_COLLABORATION=0` instead. See
[Web dashboard](dashboard.md#people-panel) and
[Collaboration](collaboration.md#hide-the-attach-helper-buttons).

A profile defines a role; its `command` is optional. Without it a helper runs the
executor of its stage (or `client.command`), and a built-in `default` profile always
exists. `collaboration.runtime_context` (`isolated` or `wrapper`) and
`profiles.<id>.runtime_context` choose the environment of a helper's process; a
profile value wins, and an empty, `null` or unknown value stops config loading.
See [Which executor a helper runs](collaboration.md#which-executor-a-helper-runs)
and [Runtime context](collaboration.md#runtime-context).

## Exit on stage failure

`afm run --exit-on-fail` makes a committed `failed` stage stop the run and return
exit code 1. It is off by default. Set `AFM_EXIT_ON_FAIL=1` for the same behavior
without a flag. An explicit CLI flag takes precedence in either direction:
`--exit-on-fail=false` disables it even when the environment enables it.

The environment value is case-insensitive and ignores surrounding whitespace.
Accepted true values are `1`, `true`, `yes`, and `on`; false values are `0`,
`false`, `no`, and `off`. An empty value acts as unset. Any other value fails
before afm creates a run or worktree. This is a `run` option, not a
`config.yaml` or `flow.yaml` field; its value stays fixed for that invocation,
including repeated cycles. See [stage failure and resume](stage-lifecycle.md#exit-on-stage-failure).

## Usage limits

`usage_limit` controls how long afm waits when an agent reports that its
provider's subscription or quota is exhausted. Set it in project or global
`.afm/config.yaml`; project values override global values for each field. All
four settings must be positive:

| Setting | Default | Effect |
|---------|---------|--------|
| `resume_margin` | `60s` | Extra time after a reported reset before work resumes; wake times get a small spread around this margin. |
| `probe_interval` | `15m` | Time until the next attempt when no reset time is known. |
| `max_wait` | `12h` | Pause instead of waiting when a known reset is farther away. |
| `max_waits` | `4` | Pause after more than this many consecutive usage-limit refusals without progress. |

The wait and its wake time survive a restart. Work using the same provider
shares the hold, so it waits without consuming another refusal. A normal
temporary rate-limit or transport error still follows the [retry policy](stage-lifecycle.md).

## Reasoning effort

`client.reasoning` sets the reasoning (thinking) effort for the default claude
client. afm maps the level to claude's `--effort` flag for the author (stage) agents,
the dashboard Assistant, the interactive JSON-repair helper, and `afm memory
rebuild`. The independent AI `verify:` reviewer does **not** receive it — verifier
agents run without the effort flag. It works both on the host and in Docker (the
default claude is exec'd directly in both).

The level is one of `minimal`, `low`, `medium`, `high`, `xhigh`, `max`. Each is
clamped to claude's actual set before it reaches the flag — claude has no `minimal`,
so `minimal` becomes `low`; every other level passes through unchanged. Leave
`reasoning` empty (the default) to pass no flag and let claude use its own default.

The field is **not** an enum: a value afm doesn't recognize is not rejected by afm's
own `afm run` preflight — it is forwarded to `--effort` verbatim and `afm run` prints
a one-line `warning:` at startup. afm itself never turns an unknown level into a hard
error, but the claude backend may reject an `--effort` value it doesn't accept and
fail that invocation. (The preflight warning is emitted by `afm run` only; `afm
memory rebuild` forwards the value the same way but does not print the warning.)

Non-claude agents configured as Docker recipes take their own per-recipe
`reasoning` knob instead — see [Docker mode → autoShim recipes](docker.md#non-claude-agents-in-docker-autoshim).

## Per-flow git worktrees

Set `worktrees.enabled: true` in `.afm/config.yaml` (or globally in
`~/.afm/config.yaml`) to opt in. `AFM_WORKTREES=1` enables and
`AFM_WORKTREES=0` disables it for a run. A top-level `worktrees: true|false` in
`flow.yaml` takes priority over both; `afm run --worktree <name>` enables a named
worktree regardless of those settings. The default is off. See
[Per-flow git worktrees](flow-reference.md#per-flow-git-worktrees) for paths,
reuse, and the `--worktree-clear` flags.

## Adding stages during a run

Set `add_stage.enabled: false` in the project's `.afm/config.yaml` or global
`~/.afm/config.yaml` to prevent new stages from being added during a run. The
default is `true`. Priority is `AFM_ADD_STAGE` (when nonempty) > project config >
global config > default. The environment variable enables the feature only for
the exact values `1` or `true`; any other nonempty value disables it. An empty
value is treated as unset. The setting is resolved when afm starts or resumes a
run, including in Docker mode. There is no `flow.yaml` override.

When disabled, the dashboard hides **+ Stage**, `/api/status` reports
`capabilities.add_stage: false`, and the graph preview and commit API routes
return HTTP 404. Agent proposals to add a stage cannot be accepted. Stages
already added to a run remain in its graph.

See [Docker mode](docker.md) for the full `docker:` section,
[Dashboard → Themes](dashboard.md#themes) for `theme`/`skin_dir`, and
[Cost accounting](accounting.md) for the `pricing:` overrides key (rate
cards, precedence, and what "estimated cost" means) and the `accounting:`
display switches (`AFM_ACCOUNTING`, `AFM_ACCOUNTING_SHOW_MONEY`), which also
decide what [Models & agents](dashboard.md#models-and-agents) shows (the
`data.analytics` [hook block](lifecycle-hooks.md#stage-analytics) ignores them and
never has money), and
[AI-verify](verify.md) for the top-level `verify.max_failures` correction budget,
`verify.review_patience` stalemate backstop, and `verify.fresh_correction` session
setting.

## Executor names

The dashboard shows a readable name for the command an agent runs instead of the
raw binary, recipe or wrapper script. `command_names` maps a command to the name
you want to see:

```yaml
command_names:
  "./scripts/dev-agent.sh": Development Agent
  codex-as-claude: Codex Reviewer
```

- **The key is the exact command string**, as written in `client.command`, a
  stage's `command`, a collaboration profile or a `verify` step. It is compared
  as is: `./scripts/dev-agent.sh` and `scripts/dev-agent.sh` are different keys.
  A key that is empty or only spaces is an error.
- **The value is a non-empty string.** Surrounding spaces are trimmed. A number,
  boolean, `null`, map or list is an error, as is an empty value. Two commands may
  share one name.
- **Project and global files are merged by key.** A key in the project's
  `.afm/config.yaml` overrides the same key in `~/.afm/config.yaml`; keys that
  only one file has are kept.

The name for a command is the first match of:

1. an exact `command_names` key (project, then global);
2. a built-in name: `claude` → `Claude Code`, `codex-as-claude` → `Codex`,
   `cursor-agent-as-claude` → `Cursor Agent`, `openai-as-claude` → `OpenAI`,
   `openai-agent-as-claude` → `OpenAI Agent`. The directory and a trailing `.sh`
   are ignored, so `/opt/bin/codex-as-claude.sh` also reads `Codex`;
3. the file name of the command, such as `agent.sh` for `./tools/x/agent.sh`.

afm never guesses a provider from part of an unknown file name and never reads
the file system to name a command.

`unknown_command_name` sets the prefix of the emergency label that the dashboard
shows only when the server sent no name for a command, for example a newer
dashboard talking to an older server. Such commands read `<prefix>-1`,
`<prefix>-2`, … (one number per command), and the raw command stays in the
tooltip. The default prefix is `Unknown`; project overrides global, spaces are
trimmed, and an empty value is an error.

Names are presentation only. They do not change which command runs, the
executor identity saved with a helper task (renaming a command never produces
`executor_changed`), participant IDs or `@` handles, or the event journal. Names are read when `afm run` starts and there is no hot
reload: after you edit `command_names`, restart the run, and the new names apply
to its earlier history as well, because journals keep the raw command. In Docker
mode the container reads the same mounted global and project config files, so
the names match the host. The dashboard layer is described in
[Executor names](dashboard.md#executor-names) and
[Verify step names](verify.md#naming-a-step).

## Settings priority

Highest to lowest:

1. CLI flags (`--max-parallel`, `--port`, `--require-approval`)
2. The project's `.afm/config.yaml`
3. The global `~/.afm/config.yaml`
4. Default values

`skills_dir` follows the normal project-over-global config merge, then
`flow.yaml` can override it at the flow, stage, or team-member level. The
nearest setting selects one library; values from different levels are not
combined. A relative path is resolved from the run's effective project root,
not from the config file. See [Agent skills](flow-reference.md#agent-skills) for
the path rules, library format, and loading behavior.

## Working directory

By default `.afm/` is created in the current folder. To move it elsewhere:

```bash
# Flag (one-off run)
afm --dir ~/my-flows run

# Environment variable (persistent)
export AFM_DIR=~/my-flows
afm run
```

All commands (`run`, `check`, `approve`, `revise`, `retry`, `init`, `list`) respect
`--dir`. Priority: `--dir` flag > `AFM_DIR` env var > current directory.

## Debugging: `--debug`

Run with `--debug` (or `AFM_DEBUG=1`) to log the **exact prompt sent to each agent**
(stdin), with timestamps and stage/phase tags:

- `.afm/runs/<run>/debug.log` — one chronological log across all stages/phases;
- `.afm/runs/<run>/<stage>/<phase>.prompt.log` — per-stage/phase (appends across
  retries).

Off by default. The logs contain full project context passed to the agent (not
secrets/env) — they live under `.afm/runs/` and aren't committed. Only the input is
logged; agent output is already in `<phase>.jsonl`/`.log`. In Docker mode,
`--debug`/`AFM_DEBUG` on the host is passed through into the container automatically.
