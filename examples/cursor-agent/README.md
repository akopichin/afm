# Non-Claude agent (Cursor)

afm works with any claude-compatible agent, and with several third-party
providers through **Docker autoShim** — afm generates a claude-compatible
wrapper inside the container from a recipe in the config. This example uses
`type: cursor` (the Cursor Cloud Agents API).

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
      url: https://api.cursor.com/v1
      auth: { from: "file:~/.cursor/token", to: "env:CURSOR_API_KEY" }
```

See [Docker mode](https://akopichin.github.io/afm/docker/) for `openai`,
`openai-agent`, `codex`, and other provider types.
