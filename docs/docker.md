# Docker mode

afm can re-exec itself inside a container, so the agents run in a clean, isolated
environment with all their tooling preinstalled.

## Running in Docker

```bash
docker run --rm -it \
  -p 127.0.0.1:9876:9876 \
  -v $(pwd):/project \
  -v ~/.claude:/home/afm/.claude \
  -v ~/.afm:/home/afm/.afm \
  -e AFM_HOST_UID=$(id -u) -e AFM_HOST_GID=$(id -g) \
  -e AFM_IN_DOCKER=1 \
  -e ANTHROPIC_API_KEY \
  akopichin/afm:latest \
  run flow.yaml
```

Notes on the flags:

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
  `HEAD → working tree` diff per file; lets you select files and insert
  `[AFM file: "<absolute container path>"]` references into a plan review comment or a
  pending-question comment — the agent reads the file with its own tools, nothing is
  copied into `feedback.md`/the answer.
- **Changed-files view (All / Unstaged / vs HEAD).** A toolbar switches between the
  full tree and two flat lists of changed files, each row showing an **M**/**A**/**D**
  status badge. Available only when the browsed root is a git repository root.
- **Review notes.** Clicking a file line while a flow is running lets you pause the
  whole flow and attach line-anchored comments — see
  [Dashboard → Review notes](dashboard.md#review-notes-pause-the-whole-flow-and-comment-on-files-docker-mode).
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
- **Security: loopback-only port when the browser is on.** With the browser enabled
  (the default), the dashboard port is published as `-p 127.0.0.1:<port>:<port>` — not
  reachable from other hosts on your LAN. Disable the browser
  (`file_browser: {enabled: false}` or `AFM_FILE_BROWSER=0`) to restore the LAN-reachable
  `0.0.0.0` publish, or keep it on and use an SSH tunnel for remote access.
