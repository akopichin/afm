# Non-Claude agent (Cursor)

afm works with any claude-compatible agent, and with several third-party
providers through **Docker autoShim** — afm generates a claude-compatible
wrapper inside the container from a recipe in the config. This example uses
`type: cursor`: the wrapper runs the `cursor-agent` CLI that the Docker image
ships (version and checksum are pinned in the image build) and translates its
output for afm.

```bash
afm run examples/cursor-agent/flow.yaml
```

autoShim runs inside Docker, so enable Docker mode and add the recipe to your
config:

```yaml
docker:
  enabled: true
  autoShim: true
  agents:
    cursor:
      type: cursor
      model: auto
      auth: { from: "file:~/.cursor/token", to: "env:CURSOR_API_KEY" }
```

`url` is not used by `type: cursor` (older recipes that set it keep working).
The API key comes from `auth` and reaches `cursor-agent` as `CURSOR_API_KEY`.

See [Docker mode](https://akopichin.github.io/afm/docker/) for `openai`,
`openai-agent`, `codex`, and other provider types.
