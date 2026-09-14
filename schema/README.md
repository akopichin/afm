# JSON Schemas

Machine-readable schemas for afm's YAML files:

- [`flow.schema.json`](flow.schema.json) — the `flow.yaml` format
- [`config.schema.json`](config.schema.json) — the `config.yaml` format

## Editor integration

Add a modeline to the top of your file and VS Code / JetBrains (via the YAML
language server) will provide autocomplete and validation:

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/akopichin/afm/main/schema/flow.schema.json
```

`afm init` adds this line to generated flows automatically, and every file under
[`examples/`](../examples/) already carries it.

## Keeping them in sync

The schemas are **hand-written** (not generated) because several fields use a
custom `UnmarshalYAML` whose YAML shape diverges from the Go type — `inputs`
(string or object), `buttons` (a `label: prompt` map), and `extra_mounts`
(string or object). A generator would describe those incorrectly.

Drift is guarded by a test: `pkg/schemacheck` reflects over `flow.Flow` and
`config.Config` and fails if any struct field is missing from its schema. Run it
with:

```bash
make schema-check      # or: go test ./pkg/schemacheck/
```

It also runs as part of `make test` (and therefore CI). When you add a field to
`flow.yaml`/`config.yaml`, update the matching schema here or the test goes red.

> Follow-up: these schemas can be submitted to [SchemaStore](https://www.schemastore.org/)
> so editors pick them up for `.afm/flows/*.yaml` without the modeline.
