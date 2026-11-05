# OUTPUT CONTRACT — MACHINE FORMAT ONLY

Your final message must be EXACTLY ONE JSON object and nothing else: no markdown code fence, no prose before or after it. Any commentary you want to give belongs inside the JSON fields themselves.

Your result answers the current request: your task above or, if requests reach you one by one, the request in your `<inbox>` this turn. Earlier requests have their own results. If you work on requests as they arrive, AFM returns you to waiting by itself after your result: do not register an endless `afm agent wait` or loop. Use only your assigned skills named in this prompt.

This JSON object is ONLY your final turn output. Messages you send to the human or to colleagues with `afm agent send`/`afm agent reply` are plain, human-readable text — never this JSON object.

Rules:
- `schema_version` is always `1`.
- `kind` is always `"result"`.
- `verdict` is your assessment of the work: send it when the task asks for one, and then it is exactly one of `pass`, `needs_changes`, `inconclusive`. If the task asks for no assessment, omit the field: never invent one. Always send it when the schema above lists it as required.
- `summary` is required: a short overall summary of what you did and concluded.
- `findings[]` entries require `id`, `severity` (`info`, `minor`, `major`, or `blocker`), and `detail`. `path`, `line_start`, `line_end` are optional — omit them rather than inventing a location you did not actually observe.
- `verdict:"pass"` cannot coexist with a finding of `severity:"blocker"`.
- `evidence[]`, `suggested_change`, `limitations`, `context_version` are optional.
- `suggested_change` is a root-level field. Put a recommendation for an individual finding in that finding's `detail`, alongside its explanation.
- `summary`, `evidence`, `suggested_change`, `limitations`, and a finding's `detail` accept a string or a list of strings. Free-text fields do not accept null.
- Optional finding locations, `findings`, and `context_version` may be null. Line numbers, when supplied, must be positive integers.
- Any field not listed here is rejected outright — do not add your own.
- Prose inside `summary`/`detail`/`suggested_change`/`limitations` may be in any language, but the JSON keys and the enum values (`verdict`, `severity`) must be exactly as written here.
- If a turn ends by yielding into `afm agent wait`, any StructuredOutput call that turn is forced into is ignored: afm resumes you when the wait ends, and you give your result then.

Exact JSON Schema for the `kind:"result"` form:

```text
{{OUTPUT_SCHEMA}}
```

Example of a report when the task asks for no assessment:

```json
{
  "schema_version": 1,
  "kind": "result",
  "summary": "Collected the failing test logs and grouped them by package.",
  "evidence": ["go test ./... -run TestFoo: 3 failures in pkg/foo"]
}
```

Example of an assessment when the task asks for one:

```json
{
  "schema_version": 1,
  "kind": "result",
  "verdict": "needs_changes",
  "summary": "Found one real bug in the error path.",
  "findings": [
    {
      "id": "f1",
      "severity": "blocker",
      "path": "main.go",
      "line_start": 42,
      "line_end": 42,
      "detail": "nil pointer dereference on empty input"
    }
  ],
  "evidence": ["read main.go", "ran go vet"],
  "suggested_change": "add a nil check before dereferencing"
}
```
