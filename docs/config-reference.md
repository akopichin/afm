# Configuration

Create `.afm/config.yaml` in the project or `~/.afm/config.yaml` globally. The full
annotated example is [`config.example.yaml`](https://github.com/akopichin/afm/blob/main/config.example.yaml).

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

# theme: graphite           # dashboard theme: graphite | goga | novacorps (default: graphite; legacy "coffee" → graphite)
# prompts_dir: .afm/prompts/  # custom prompt templates
# auto_recover: true        # auto-retry failed stages on run start/resume (default: true)

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

See [Docker mode](docker.md) for the full `docker:` section, and
[Dashboard → Themes](dashboard.md#themes) for `theme`/`skin_dir`.

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
