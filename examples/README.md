# Example flows

Ready-to-run `flow.yaml` files that show afm's main features. **Copy one into a
scratch project and adapt it** — don't run them inside the afm repository itself.
Agents run against the current working directory, so running an example in place lets
them write into these sources (e.g. the `basic` flow implements a JWT backend/frontend,
`buttons` creates `GREETING.md`).

| Example | What it shows |
|---------|---------------|
| [`basic/`](basic/) | A classic three-stage feature: planning → implementation → review, with `depends_on` and artifacts. |
| [`interactive/`](interactive/) | An interactive stage that asks the user a question through the file-based dialog protocol. |
| [`cursor-agent/`](cursor-agent/) | Using a non-Claude agent (`type: cursor`, the Cursor Cloud Agents API) via Docker autoShim. |
| [`buttons/`](buttons/) | Predefined one-click stage actions in the dashboard kebab menu. |
| [`verify/`](verify/) | AI-verify: a shell test gate followed by a read-only AI code review before a stage is marked done. |

Copy one into a scratch project and run it there:

```bash
mkdir /tmp/afm-demo && cp examples/basic/flow.yaml /tmp/afm-demo/
cd /tmp/afm-demo && afm run flow.yaml
```

See the [full documentation](https://akopichin.github.io/afm/) for every
`flow.yaml` field.
