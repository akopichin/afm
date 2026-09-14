# Example flows

Ready-to-run `flow.yaml` files that show afm's main features. Copy one into your
project (or run it in place) and adapt it.

| Example | What it shows |
|---------|---------------|
| [`basic/`](basic/) | A classic three-stage feature: planning → implementation → review, with `depends_on` and artifacts. |
| [`interactive/`](interactive/) | An interactive stage that asks the user a question through the file-based dialog protocol. |
| [`cursor-agent/`](cursor-agent/) | Using a non-Claude agent (`type: cursor`, the Cursor Cloud Agents API) via Docker autoShim. |
| [`buttons/`](buttons/) | Predefined one-click stage actions in the dashboard kebab menu. |

Run any of them with:

```bash
afm run examples/basic/flow.yaml
```

See the [full documentation](https://akopichin.github.io/afm/) for every
`flow.yaml` field.
