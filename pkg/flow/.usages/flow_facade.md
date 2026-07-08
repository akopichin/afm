Domain: parsing and querying a flow.yaml definition. Audience: `pkg/prompts`, `pkg/docker`,
`pkg/orchestrator`, `cmd/afm`.

## Parsing a flow file

```go
f, err := flow.ParseFile(path)
if err != nil {
    return err
}
```

## Iterating stages and their dependency-relevant fields

```go
for _, stage := range f.Stages {
    if stage.HasAgent("planning") {
        // stage.NeedsPlanning() tells you whether a planning agent will actually run
    }
    implAgent := stage.ImplAgent() // "implementation", or the custom command's own type
}
```

## Reading artifacts and inputs

```go
for _, art := range stage.Artifacts {
    inline := art.IsInline() // true unless explicitly set to false
}
for _, in := range stage.Inputs {
    if !in.Optional {
        // in.Ref is "stageID.artifactName"
    }
}
```

`Input` unmarshals from either a bare string (`"stage.artifact"`) or an object (`{ref, optional}`) —
callers do not need to special-case either form; `UnmarshalYAML` normalizes both.

## Reading the root prompt

`f.Prompt` holds the optional root-level `prompt:` directive from `flow.yaml`. It is
forwarded verbatim into `orchestrator.Options.GlobalPrompt` by `cmd/afm`; an empty
value (`""` — YAML key absent) means "no root prompt", not an error.
