# Design: stage.prompt field + stage name in UI

Date: 2026-07-02

## Summary

Two independent additions to afm:

1. **`prompt` field on `Stage`** — optional explicit instruction block delivered to the agent after the description context block, positioned as direct user instruction.
2. **Stage name display in UI** — left panel shows id (large) + name (small), center detail title shows name (or id if name absent). Name is already optional in YAML; this wires it through to the frontend.

---

## Feature 1: `stage.prompt` field

### Motivation

`description` is contextual — background, scope, constraints. `prompt` is directive — the exact instruction the agent should follow. Having them separate lets flow authors write context-heavy descriptions while keeping the actual task instruction explicit and prominent.

### YAML interface

```yaml
stages:
  - id: backend
    description: |
      Контекст: монолит на Go, PostgreSQL, уже есть pkg/auth.
      Требования безопасности: bcrypt для паролей, JWT RS256.
    prompt: |
      Реализуй эндпоинт POST /api/login: принимает email+password,
      возвращает JWT. Следуй существующим паттернам в pkg/auth.
    agents: [planning, implementation]
```

Both fields are optional independently. A stage can have only `description`, only `prompt`, both, or neither (existing flows are unaffected).

### Prompt structure (output of `Build()`)

```
<system_rules>
[template]
[interactive_rules if applicable]
</system_rules>

<context>
[dependency_plans]
[artifacts]
</context>

<stage id="..." name="...">
<description>
[description]
</description>
<skills>...</skills>
</stage>

<prompt>          ← NEW: only if stage.Prompt != ""
[prompt]
</prompt>

[plan]
[previous_plan]
[feedback]
[retry_context]
[example_output]
```

### Code changes

| File | Change |
|------|--------|
| `pkg/flow/flow.go` | Add `Prompt string \`yaml:"prompt"\`` to `Stage` struct |
| `pkg/prompts/builder.go` | Add `Prompt string` to `Inputs`; emit `<prompt>…</prompt>` block after `</stage>`; add tag pair to `tagReplacer` |
| `pkg/orchestrator/orchestrator.go` | Pass `Prompt: stage.Prompt` at all 4 `prompts.Build()` call sites |

Total: ~15 lines changed across 3 files. No new types, no new interfaces.

### Constraints

- `prompt` content is escaped through the same `escapeTags()` as `description` — no XML injection risk.
- No validation: `prompt` can be empty string, multiline, anything. Flow authors control it.
- Existing flows with no `prompt` field produce identical output to today.

---

## Feature 2: Stage name in UI

### Motivation

The left panel currently shows only `id` (e.g. `backend-auth`). When a flow has many stages, descriptive names help orient the user. `name` is already an optional field on `Stage` in the YAML but is never forwarded to the frontend.

### UI behaviour

**Left panel (stage list):**
```
● backend-auth          ← id, font-weight: 600
  Backend Authentication  ← name, smaller + muted, only if name present
```

**Center detail title (`<h2 id="detail-title">`):**
- Shows `name` when present, else falls back to `id`.
- Example: `"Backend Authentication"` or `"backend-auth"`.

Backward compatibility: if `stage_names` is absent (old run resumed from pre-feature state.json), UI shows only id — same as today.

### Data flow

The `RunState` JSON served by `/api/status` gains a new top-level field:

```json
{
  "flow_name": "jwt-auth",
  "started_at": "...",
  "stage_order": ["backend-auth", "frontend-login"],
  "stage_names": {
    "backend-auth": "Backend Authentication",
    "frontend-login": "Frontend Login"
  },
  "stages": { ... }
}
```

`stage_names` is populated once at run creation from the parsed flow and is immutable for the lifetime of the run. Stages with no `name` in YAML get an empty string entry (or are absent from the map) — both degrade gracefully in the UI.

### Code changes

| File | Change |
|------|--------|
| `pkg/state/state.go` | Add `StageNames map[string]string \`json:"stage_names"\`` to `RunState`; update `NewRunState` to accept `[]flow.Stage` instead of `[]string`, populate both `StageOrder` and `StageNames` |
| `pkg/orchestrator/orchestrator.go` | Pass `flow.Stages` (not just IDs) to `state.NewRunState` |
| `pkg/web/app.js` | `renderStages()`: render `<span class="stage-id">` + optional `<span class="stage-name">`; `renderDetail()`: set title to `stage_names[id] \|\| id` |
| `pkg/web/style.css` | Add `.stage-id`, `.stage-name` styles; ensure `.stage-item` stacks them vertically |

`pkg/server/handlers.go` — no changes needed: `handleStatus` already encodes the full `RunState`.

### CSS

```css
.stage-item {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
}
.stage-id {
    font-size: 0.9em;
    font-weight: 600;
}
.stage-name {
    font-size: 0.75em;
    opacity: 0.65;
    line-height: 1.2;
}
```

Exact values to be tuned against existing theme during implementation.

---

## Out of scope

- Making `name` required — stays optional.
- Showing `name` in the event feed right panel — feed badges show `stageID` only, unchanged.
- Showing `name` in CLI `afm check` output — out of scope.
- Any changes to `afm init` YAML generation — it already emits `name:`, no change needed.

---

## Testing

- `pkg/flow/flow.go`: existing tests continue to pass (no new validation).
- `pkg/prompts/builder.go`: add unit test case `Build()` with non-empty `Prompt` → verify `<prompt>` block appears after `</stage>` and before `[plan]`.
- `pkg/state/state.go`: update `NewRunState` call sites in existing tests to pass `[]flow.Stage`.
- UI: manual smoke test — run a flow with `name` set on some stages and absent on others; verify left panel and detail title behave correctly in both cases.
