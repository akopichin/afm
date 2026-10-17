## The alternative answer form: delegating to children

Instead of the `kind:"result"` form above, you may answer with `kind:"delegate"` to ask the orchestrator to create 1 to 8 child tasks on your behalf. Use this form ONLY when you genuinely want to split the work; otherwise answer with `kind:"result"`.

Rules:
- `schema_version` is always `1`.
- `kind` is always `"delegate"`.
- `summary` is optional: a short note on why you are delegating.
- `children[]` requires 1 to 8 entries, each with `profile_id`, `title`, `instruction`, `mode` (`advisory` or `required`), and `trigger` (`one_shot`, `checkpoints`, or `on_completion`).
- `continuation` is optional: `trigger` (same enum as above) says when you want to be woken up once your children report back, and `note` is a short reminder of what you are waiting for.
- Any field not listed here is rejected outright — do not add your own.

Example:

```json
{
  "schema_version": 1,
  "kind": "delegate",
  "summary": "Splitting this into a review and a security pass.",
  "children": [
    {
      "profile_id": "reviewer",
      "title": "Review the diff",
      "instruction": "Review pkg/foo for correctness.",
      "mode": "advisory",
      "trigger": "one_shot"
    },
    {
      "profile_id": "security",
      "title": "Security pass",
      "instruction": "Check pkg/foo for injection issues.",
      "mode": "required",
      "trigger": "one_shot"
    }
  ],
  "continuation": {
    "trigger": "on_completion",
    "note": "resume once both children report back"
  }
}
```
