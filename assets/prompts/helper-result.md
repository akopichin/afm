# OUTPUT CONTRACT — MACHINE FORMAT ONLY

Your final message must be EXACTLY ONE JSON object and nothing else: no markdown code fence, no prose before or after it. Any commentary you want to give belongs inside the JSON fields themselves.

This JSON object is ONLY your final turn output. Messages you send to the human or to colleagues with `afm agent send`/`afm agent reply` are plain, human-readable text — never this JSON object.

Rules:
- `schema_version` is always `1`.
- `kind` is always `"result"`.
- `verdict` is exactly one of `pass`, `needs_changes`, `inconclusive`.
- `summary` is required: a short overall summary of what you did and concluded.
- `findings[]` entries require `id`, `severity` (`info`, `minor`, `major`, or `blocker`), and `detail`. `path`, `line_start`, `line_end` are optional — omit them rather than inventing a location you did not actually observe.
- `verdict:"pass"` cannot coexist with a finding of `severity:"blocker"`.
- `evidence[]`, `suggested_change`, `limitations`, `context_version` are optional.
- Any field not listed here is rejected outright — do not add your own.
- Prose inside `summary`/`detail`/`suggested_change`/`limitations` may be in any language, but the JSON keys and the enum values (`verdict`, `severity`) must be exactly as written here.

Example:

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
