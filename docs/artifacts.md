# Passing context between stages

Plans (and the `execution_summary.md` of [autonomous stages](autonomous-track.md))
of dependent stages are automatically added to the prompt via `depends_on`. To pass
file artifacts, use `artifacts` + `inputs`:

```yaml
stages:
  - id: backend
    artifacts:
      - name: api-contract
        path: docs/api-contract.yaml
        description: "OpenAPI schema"
      - name: db-schema
        path: ./schema.sql           # ./ = the stage directory in the run
        description: "SQL migration"
        inline: false                 # pass the path, not the content

  - id: frontend
    depends_on: [backend]
    inputs:
      - backend.api-contract          # required artifact
      - ref: backend.db-schema        # optional
        optional: true
```

- `inline: true` (default) — the file's content is inserted into the prompt.
- `inline: false` — the file's path is passed into the prompt instead.
- `optional: true` — if the file isn't found, the stage runs without it (a required
  artifact that's missing fails the consuming stage).

An `inputs` entry references an upstream artifact as `<stage>.<artifact>`. The stage
producing it must be in the consumer's `depends_on`.
