# Stage buttons

A stage can declare named one-click actions in `flow.yaml`. Each button carries
a canned prompt; clicking it in the dashboard stage header delivers that prompt
to the stage's live agent (the same path as a Feed note).

```yaml
stages:
  - name: build
    buttons:
      Run linter: "Run golangci-lint and fix every finding"
      Rebuild:    "Rebuild the project from scratch and make sure tests pass"
```

```bash
afm run examples/buttons/flow.yaml
```

Button order matches declaration order. See
[Dashboard](https://akopichin.github.io/afm/dashboard/) for details.
