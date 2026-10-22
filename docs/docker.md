# Docker mode

afm can re-exec itself inside a container, so the agents run in a clean, isolated
environment with all their tooling preinstalled.

## Running in Docker

```bash
docker run --rm -it --init \
  -p 127.0.0.1:9876:9876 \
  -v $(pwd):/project \
  -v ~/.claude:/home/afm/.claude \
  -v ~/.afm:/home/afm/.afm \
  -v ~/.codex:/tmp/host-codex:ro \
  -e AFM_HOST_UID=$(id -u) -e AFM_HOST_GID=$(id -g) \
  -e AFM_IN_DOCKER=1 \
  -e ANTHROPIC_API_KEY \
  akopichin/afm:latest \
  run flow.yaml
```

Notes on the flags:

- Install the skills before the first manual `docker run`, and again after switching
  to a new image: afm syncs its skills automatically only on the host, and the
  container sees them through the mounted `~/.claude` and `~/.afm` (see
  [Skills for Claude Code and Codex](getting-started.md#skills-for-claude-code-and-codex)).
  Run `afm install-skills` on the host, or, without a local afm, let the image do it:

  ```bash
  docker run --rm \
    -v ~/.claude:/home/afm/.claude -v ~/.afm:/home/afm/.afm -v ~/.codex:/home/afm/.codex \
    -e AFM_HOST_UID=$(id -u) -e AFM_HOST_GID=$(id -g) \
    akopichin/afm:latest install-skills
  ```
- `-v ~/.codex:/tmp/host-codex:ro` is needed only for Codex stages: the entrypoint
  copies the Codex login and skill links from there into the container. Skip it if
  you don't use Codex.

- `--init` runs Docker's init as the container's PID 1. It reaps the background
  processes an agent leaves behind once afm stops them at the end of a turn;
  without it they stay as zombies until the container exits. afm adds this flag
  itself when it starts the container.
- `-p 127.0.0.1:9876:9876` maps the dashboard port so `http://localhost:9876` works
  from the host (use the same port as `server.port`).
- `-e AFM_IN_DOCKER=1` tells afm it is already inside a container, so it does **not**
  try to launch another container even if a mounted `~/.afm/config.yaml` has
  `docker.enabled: true`. Since afm now also **auto-detects** a container by its
  marker file (`/.dockerenv` / `/run/.containerenv`), this flag is optional for
  recursion prevention in a manual `docker run` like this — afm won't re-exec either
  way. Keep it anyway when using afm's official image, which sets it for you.
- For a Claude Pro/Max subscription, replace `-e ANTHROPIC_API_KEY` with
  `-e CLAUDE_CODE_OAUTH_TOKEN` (see [Authentication](#authentication-in-docker-mode)
  below).

Automatic Docker mode (`docker.enabled: true`) sets all of this — the port mapping,
`AFM_IN_DOCKER`, and the forwarded token — for you.

When [per-flow git worktrees](flow-reference.md#per-flow-git-worktrees) are enabled
with automatic Docker mode, afm creates or reuses the worktree on the host and
passes its path into the container. The effective project root (`root_dir`, or
the parent of `.afm/`) must be inside the directory mounted from `--dir`.

Or enable automatic Docker mode in the config — then the plain `afm run` command
restarts itself inside the container:

```yaml
# .afm/config.yaml
docker:
  enabled: true             # or set AFM_USE_DOCKER=1
  # image: akopichin/afm:latest
```

The image includes claude CLI, Node 22, Python 3.12, Go 1.26, and git. The container
starts as root, but the entrypoint (`gosu`) immediately drops privileges to your host
uid/gid — files in the mounted volumes belong to you, not root.

## Authentication in Docker mode

The container is Linux — it has no access to the macOS Keychain where claude's OAuth
sessions live. The token has to be passed explicitly via an environment variable;
afm forwards it into the container automatically.

**Claude Pro/Max (claude.ai subscription):**

```bash
# One-time: generate a long-lived token
claude setup-token

# Add to ~/.zshrc / ~/.bashrc
export CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat01-...
```

**Anthropic API key:**

```bash
export ANTHROPIC_API_KEY=sk-ant-api-...
```

`ANTHROPIC_AUTH_TOKEN` and `ANTHROPIC_BASE_URL` are also supported — all of these are
forwarded in bare form (`-e KEY` with no value), so the secret doesn't leak into
`ps`/history.

## Non-Claude agents in Docker (autoShim)

If a stage uses a non-claude command (`command: glm51`, `command: deepseek`, …),
Docker offers two options:

- **Mounting:** afm locates the binary via `which` and mounts it into the container
  (`:ro`). Works if the agent has no external dependencies.
- **autoShim (recommended):** with `docker.autoShim: true`, afm generates a
  claude-compatible wrapper right inside the container from the `docker.agents.<cmd>`
  recipe — without mounting the binary and without passing tokens through files. The
  secret is read on the host and passed in as a transient env var.

```yaml
docker:
  autoShim: true
  agents:
    glm51:
      model: glm-5.1
      url: https://api.z.ai/api/anthropic
      auth: { from: "file:~/.ai-free/claude-glm/token", to: "env:ANTHROPIC_AUTH_TOKEN" }
```

Supported recipe `type`s:

| `type` | For |
|--------|-----|
| `claude` (default) | Anthropic-compatible endpoints (z.ai, DeepSeek's anthropic API, …) |
| `openai` | OpenAI-compatible `/chat/completions` — text only (planning/review) |
| `openai-agent` | OpenAI-compatible providers with a real tool-loop (autonomous/interactive) |
| `cursor` | Cursor Cloud Agents API |
| `codex` | OpenAI Codex CLI — auth via a mounted `~/.codex` OAuth state, no secret in config |

For a `claude` recipe, its explicit `model` is passed as `--model` on every
invocation, including resumed sessions. It takes precedence over Claude Code's
`model` setting, an inherited `ANTHROPIC_MODEL`, and `--model` in
`client.extra_args`. The wrapper also maps the Haiku, Sonnet and Opus aliases to
the recipe's model. A Claude proxy wrapper without a model keeps Claude Code's
normal model selection. These overrides apply to generated Docker wrappers.

Each recipe can also set a per-recipe **`reasoning`** level (`minimal`, `low`,
`medium`, `high`, `xhigh`, `max`):

```yaml
docker:
  agents:
    glm51:
      model: glm-5.1
      reasoning: high
      url: https://api.z.ai/api/anthropic
      auth: { from: "file:~/.ai-free/claude-glm/token", to: "env:ANTHROPIC_AUTH_TOKEN" }
```

afm clamps the level to each family's native control and bakes it into the generated
wrapper: `claude` recipes get `--effort`, `codex` gets `model_reasoning_effort`, and
`openai`/`openai-agent` get `reasoning_effort` in the request body. `cursor` has no
such control and ignores the field. Because the wrappers are generated inside the
container, `reasoning` on a recipe is **Docker-only**. As with the top-level
[`client.reasoning`](config-reference.md#reasoning-effort), an unrecognized value is
not rejected by afm's `afm run` preflight — it is forwarded to the wrapper as-is and
`afm run` prints a startup warning. The provider backend may still reject a value it
doesn't accept and fail that invocation.

Full recipes and per-provider notes are in
[`config.example.yaml`](https://github.com/akopichin/afm/blob/main/config.example.yaml)
and the [`examples/cursor-agent/`](https://github.com/akopichin/afm/tree/main/examples/cursor-agent)
flow.

## Project file browser

Inside a running Docker container the dashboard header can show a folder-icon button
that opens a **read-only** project file browser. It is Docker-only: a plain host run
doesn't have it (`/api/files/*` returns `404` and the button isn't shown).

- **On by default; opt out to disable.** `docker.file_browser.enabled` defaults to
  `true` (an unset value means enabled). Set it to `false` in the config to turn the
  browser off. The env var `AFM_FILE_BROWSER` takes priority over the config in both
  directions (`AFM_FILE_BROWSER=1` force-enables, `AFM_FILE_BROWSER=0` force-disables).
- **What it does:** a lazy-loading source tree of the project mount and any
  `extra_mounts` explicitly opted in with `browse: true`; opens text files with syntax
  highlighting (Go, TypeScript/TSX, JavaScript/JSX, Python); shows a
  `HEAD → working tree` diff per file — or, for a file inside a
  [per-flow git worktree](flow-reference.md#per-flow-git-worktrees), a diff against the
  branch's fork point, so work the agent has already committed on the worktree branch
  still shows; lets you select files and insert
  `[AFM file: "<absolute container path>"]` references into a plan review comment or a
  pending-question comment — the agent reads the file with its own tools, nothing is
  copied into `feedback.md`/the answer.
- **Changed-files view (All / Unstaged / vs HEAD / vs base).** A toolbar switches between
  the full tree and flat lists of changed files, each row showing an **M**/**A**/**D**
  status badge. Available only when the browsed root is a git repository root. The extra
  **vs base** list appears only when the root contains a
  [per-flow git worktree](flow-reference.md#per-flow-git-worktrees); it lists the files
  the worktree branch changed since its fork point (work committed on the branch),
  rendered under `.worktrees/<flow>/`.
- **Review notes.** Clicking a file line while a flow is running lets you pause the
  whole flow and attach line-anchored comments — see
  [Dashboard → Review notes](dashboard.md#review-notes-pause-the-whole-flow-and-comment-on-files-docker-mode).
- **Comments without pausing.** With the Side agent enabled, choose **Without pause**
  at the file-line prompt to collect draft comments and attach them to a Side agent
  message while the flow keeps running — see
  [Dashboard → File comments without pausing](dashboard.md#file-comments-without-pausing-docker-mode).
- **Strictly read-only.** `.git` and `.afm` are always hidden. No create/edit/rename/
  delete. File content is capped at 2 MiB, diffs at 4 MiB; symlinks are listed but not
  opened.
- **`extra_mounts` accept an object form with `browse`** (the legacy plain-string list
  still works, staying `browse: false`):

  ```yaml
  docker:
    enabled: true
    file_browser:
      enabled: true

    extra_mounts:
      - path: ../shared-contracts
        name: contracts       # optional UI label; default = basename(path)
        browse: true           # source mount — VISIBLE in the file browser
      - path: ~/.ai-free
        browse: false           # credential mount — mounted to the agent, NOT browseable
      - ~/.legacy-agent          # legacy scalar form = browse:false
  ```

  This is a deliberate safe default: after upgrading afm, every existing
  `extra_mounts` entry stays private. Only add `browse: true` for a code root you're
  comfortable showing.
- **Security: loopback-only port.** The dashboard port is **always** published as
  `-p 127.0.0.1:<port>:<port>`, not reachable from other hosts on your LAN. The
  dashboard exposes unauthenticated mutating stage/flow APIs (plus, optionally, the
  project file browser and the [side agent](dashboard.md#side-agent-a-free-form-chat-alongside-the-run)),
  so this boundary is unconditional — it does not depend on whether those features are
  enabled. Use an SSH tunnel for remote access rather than widening the publish.
