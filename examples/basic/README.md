# Basic feature flow

A classic three-stage feature, each stage going through the full
planning → implementation → review cycle:

1. **backend** — build the API; produces artifacts (the API contract, DB schema).
2. **frontend** — depends on `backend`, consumes its artifacts as `inputs`.
3. **integration** — ties the two together and runs tests.

It demonstrates the core building blocks: `agents`, `depends_on`, `artifacts`,
and `inputs`.

```bash
afm run examples/basic/flow.yaml
```

Each stage stops at `awaiting_approval` after planning — approve or revise the
plan in the dashboard, and afm carries out the implementation. (While `afm run` is
active it holds an exclusive lock; the CLI `afm approve <stage>` is for headless use,
after the run process stops.)
