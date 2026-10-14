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

server:
  port: 9876                # web dashboard port
  open_browser: false       # open the browser on startup (default: false)

# theme: graphite           # dashboard theme: graphite | goga | novacorps (default: graphite; legacy "coffee" → graphite)
# prompts_dir: .afm/prompts/  # custom prompt templates
# auto_recover: true        # auto-retry failed stages on run start/resume (default: true)

# side_agent:
#   enabled: false          # dashboard side agent; ON by default. env AFM_SIDEAGENT overrides,
#                           # and flow.yaml side_agent overrides both. See dashboard.md

# worktrees:
#   enabled: true           # per-flow git worktrees; OFF by default. env AFM_WORKTREES
#                           # overrides, and flow.yaml worktrees overrides both

accounting:
  enabled: false            # hide cost display everywhere (dashboard + afm check/report);
                            # ON by default, env AFM_ACCOUNTING overrides. Data is still
                            # collected into usage.jsonl — this hides the display, not the data.
  show_money: false         # also show monetary ($) figures (Est. cost); OFF by default —
                            # tokens are always shown, money is opt-in. env
                            # AFM_ACCOUNTING_SHOW_MONEY overrides.

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

## Reasoning effort

`client.reasoning` sets the reasoning (thinking) effort for the default claude
client. afm maps the level to claude's `--effort` flag for the author (stage) agents,
the dashboard side agent, the interactive JSON-repair helper, and `afm memory
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

See [Docker mode](docker.md) for the full `docker:` section,
[Dashboard → Themes](dashboard.md#themes) for `theme`/`skin_dir`, and
[Cost accounting](accounting.md) for the `pricing:` overrides key (rate
cards, precedence, and what "estimated cost" means) and the `accounting:`
display switches (`AFM_ACCOUNTING`, `AFM_ACCOUNTING_SHOW_MONEY`), and
[AI-verify](verify.md) for the top-level `verify.max_failures` correction budget,
`verify.review_patience` stalemate backstop, and `verify.fresh_correction` session
setting.

## Settings priority

Highest to lowest:

1. CLI flags (`--max-parallel`, `--port`, `--require-approval`)
2. The project's `.afm/config.yaml`
3. The global `~/.afm/config.yaml`
4. Default values

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
