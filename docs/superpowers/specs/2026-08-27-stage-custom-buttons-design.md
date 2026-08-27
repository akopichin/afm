# Stage custom buttons (predefined kebab-menu prompts)

**Date:** 2026-08-27
**Status:** Approved for implementation

## Problem

A stage's kebab menu offers a fixed set of actions: "Add note for agent"
(free-text Revise), "Pause", "Continue", "Retry", pre-note. There's no way to
predefine, per stage in `flow.yaml`, a reusable canned instruction that the
user can fire with one click — e.g. a "Run linter" or "Rebuild" button whose
prompt is written once in the flow and triggered many times during a run.

## Goal

Let a flow author declare named buttons on a stage. Each button carries a
prompt. Clicking a button in the dashboard sends that prompt to the stage's
**live agent** through the existing Revise mechanism (the exact same path as
the free-text "Add note for agent"), restarting the agent with the prompt as
feedback.

```yaml
stages:
  - name: build
    buttons:
      Run linter: "Запусти golangci-lint и почини все замечания"
      Rebuild:    "Пересобери проект с нуля и убедись что тесты зелёные"
```

## Decisions (locked during brainstorming)

1. **Mechanism = Revise of the live agent.** A click routes the button's
   prompt into `Orchestrator.Revise` — `feedback.md` + `EvRevise` + interrupt
   channel → the agent restarts with the prompt. No new agent-execution
   machinery.
2. **Availability = `running` / `awaiting_approval` only**, and never on a
   `script:` stage (no agent to feed — symmetric to the `!isScript` gate on
   "Add note for agent"). Buttons are hidden on every other status.
3. **Fire immediately, no modal.** The client sends only the **button name**;
   the server/orchestrator resolves the prompt from the flow (never trusts
   client-supplied prompt text).
4. **Name = both label and identity.** The YAML key is the display label and
   the lookup key. Renaming in YAML is a "new" button. Duplicate labels within
   one stage are a parse error.
5. **Repeated clicks allowed** — each click is another Revise (agent restarts
   again), exactly like the free-text note.
6. **Per-stage only.** No flow-level/global buttons (YAGNI).

## Architecture & data flow

```
kebab click «Run linter»
  → POST /api/stages/{id}/button        {"name":"Run linter"}
  → Server.handleStageButton
       gate: stage exists, status ∈ {running, awaiting_approval}, !isScript
  → StageActions.Button(ctx, stageID, "Run linter")
  → Orchestrator.Button:
       stage := o.graph.Stage(stageID)
       prompt := stage.Buttons.Prompt("Run linter")   // "" → unknown button
       return o.Revise(ctx, stageID, prompt)           // existing path, unchanged
  → feedback.md + EvRevise + interrupt → agent restarts with the prompt
```

The orchestrator owns `o.graph`, so it is the natural place to resolve
name→prompt; the HTTP layer never sees the prompt text. The server keeps a
per-stage **labels-only** map (`stageButtons map[string][]string`) purely for
(a) rendering the menu items into `StageView` and (b) rejecting a click whose
name isn't a declared button before bothering the orchestrator.

## Component 1 — `pkg/flow` (schema + validation)

New field on `Stage` (add to the struct near `AutoRun`):

```go
// Buttons are predefined kebab-menu items for this stage: label → prompt.
// Clicking one delivers the prompt to the live agent via Revise (same path
// as a free-text note). Order is preserved from YAML. Illegal on a script
// stage (no agent). See docs/superpowers/specs/2026-08-27-stage-custom-buttons-design.md.
Buttons Buttons `yaml:"buttons,omitempty"`
```

New types + ordered unmarshal (mirrors the existing `Input.UnmarshalYAML`
precedent at `pkg/flow/flow.go:46`):

```go
type Button struct {
	Label  string
	Prompt string
}

// Buttons preserves YAML declaration order (a plain map[string]string would
// iterate randomly, shuffling the menu). Decoded from a mapping node by
// walking value.Content in pairs.
type Buttons []Button

func (b *Buttons) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.MappingNode {
		return fmt.Errorf("buttons must be a mapping of label: prompt")
	}
	out := make(Buttons, 0, len(value.Content)/2)
	for i := 0; i+1 < len(value.Content); i += 2 {
		out = append(out, Button{
			Label:  value.Content[i].Value,
			Prompt: value.Content[i+1].Value,
		})
	}
	*b = out
	return nil
}

// Prompt returns the prompt for label, or "" if no such button.
func (b Buttons) Prompt(label string) string {
	for _, btn := range b {
		if btn.Label == label {
			return btn.Prompt
		}
	}
	return ""
}

// Labels returns the button labels in declaration order.
func (b Buttons) Labels() []string {
	out := make([]string, len(b))
	for i, btn := range b {
		out[i] = btn.Label
	}
	return out
}
```

Validation in `Flow.validate()` (new loop over `f.Stages`), errors matching the
existing `stage %q: ...` style:

- empty label → `stage %q: button label cannot be empty`
- empty prompt → `stage %q: button %q has empty prompt`
- duplicate label within a stage → `stage %q: duplicate button label %q`
- buttons on a script stage → `stage %q: "buttons" cannot be combined with script`

`buttons` omitted → `nil` → previous behavior; full backward compatibility.

**Tests (`pkg/flow`):** parse label→prompt; **declaration order preserved**
(e.g. `Zebra` before `Apple` stays `Zebra, Apple`); each validation error;
script+buttons rejected; a flow with no buttons parses unchanged.

## Component 2 — `pkg/orchestrator` (`Button` action)

Add to `pkg/orchestrator/control_api.go`, next to `Revise`:

```go
// Button resolves the named button's prompt from the flow and delivers it to
// the live agent via Revise. Unknown name or a stage with no such button is a
// no-op (returns nil). The status gate (running/awaiting_approval) lives in
// Revise itself, so Button doesn't re-check it.
func (o *Orchestrator) Button(ctx context.Context, stageID, name string) error {
	stage := o.graph.Stage(stageID)
	if stage == nil {
		return nil
	}
	prompt := stage.Buttons.Prompt(name)
	if prompt == "" {
		return nil
	}
	return o.Revise(ctx, stageID, prompt)
}
```

Add to the `StageActions` interface (`pkg/server/actions.go`):

```go
Button(ctx context.Context, stageID, name string) error
```

The compile-time assertion `_ server.StageActions = (*orchestrator.Orchestrator)(nil)`
(`cmd/afm/run.go:33`) then forces the method to exist.

**Tests (`pkg/orchestrator`):** integration test by the shape of the existing
revise test — a running stage with a declared button; call `orch.Button(ctx,
id, "Run linter")`; assert `feedback.md` contains the button's prompt and the
stage went through `EvRevise`. Also: unknown name is a no-op (no feedback
written, no error).

## Component 3 — `pkg/server` (endpoint + StageView)

**Server struct / Config** (`pkg/server/server.go`): add
`stageButtons map[string][]string` (labels only), wired from a new
`Config.StageButtons` field, exactly like `stageIsScript`/`stageDependsOn`.

**Route** (`pkg/server/server.go`, next to `/revise`, `/note`):

```go
case strings.HasSuffix(path, "/button") && r.Method == http.MethodPost:
	s.handleStageButton(w, r)
```

**Handler** (`pkg/server/handlers.go`, modeled on `handleRevise`):

```go
type stageButtonRequest struct {
	Name string `json:"name"`
}

func (s *Server) handleStageButton(w http.ResponseWriter, r *http.Request) {
	stageID := extractStageID(r.URL.Path, "/api/stages/", "/button")
	if !isValidStageID(stageID) { /* 400 invalid stage id */ }
	rs := s.store.Snapshot()
	st, ok := rs.Stages[stageID]
	if !ok { /* 404 stage not found */ }
	allowed := st.Status == state.StatusAwaitingApproval || st.Status == state.StatusRunning
	if !allowed { /* 400 "stage is %s, not awaiting_approval or running" */ }
	if s.stageIsScript[stageID] { /* 400 "script stage has no agent" */ }
	var req stageButtonRequest
	// decode; 400 on error
	if req.Name == "" { /* 400 "button name is required" */ }
	// reject unknown button before touching the orchestrator:
	if !slices.Contains(s.stageButtons[stageID], req.Name) { /* 400 "unknown button" */ }
	if err := s.actions.Button(r.Context(), stageID, req.Name); err != nil { /* 500 */ }
	// 200 {"status":"button","stage_id":stageID}
}
```

**StageView** (`pkg/server/stageview.go`): add

```go
// Buttons — labels of predefined kebab-menu buttons for this stage (static
// flow config). Empty if none. The frontend renders one menu item per label
// and POSTs the label to /button on click.
Buttons []string `json:"buttons,omitempty"`
```

Thread `stageButtons` through `buildStageViews` (new param) and set
`view.Buttons = stageButtons[id]`. Update the call site in `handlers.go:45`
and the existing `buildStageViews` tests (extra nil arg).

**Tests (`pkg/server`):** `handleStageButton` — success (200, calls
`actions.Button` with the name); unknown name (400, orchestrator not called);
wrong status (400); script stage (400); missing name (400); nonexistent stage
(404). Add `Button` to the `fakeStageActions` test double. `buildStageViews`
includes `Buttons` from the map.

## Component 4 — `cmd/afm/run.go` (wiring)

In the per-stage map-building loop (~`run.go:237`), add:

```go
stageButtons := make(map[string][]string, len(f.Stages))
// inside the loop:
stageButtons[st.ID] = st.Buttons.Labels()
```

Pass `StageButtons: stageButtons` into `server.Config{...}` (~`run.go:250`).

## Component 5 — frontend (`pkg/web/dashboard`)

- **`api/run-client.ts`:** `triggerStageButton(stageId, name)` →
  `POST /api/stages/{id}/button` `{name}` (mirror `reviseStage`).
- **`hooks/use-status/use-status.ts`:** map `buttons: Array.isArray(obj.buttons)
  ? obj.buttons.filter(x => typeof x === 'string') : []` into the `Stage`
  type; add `buttons: string[]` to the type.
- **`components/stages-list/StagesList.tsx`:**
  - New prop `onButton?: (stageId: string, name: string) => void`.
  - Render one menu item per `stage.buttons` entry, in a sub-block below
    "Add note for agent" separated by a divider. Same status gate as the note
    item (`running`/`awaiting_approval`); items hidden when `buttons` is empty.
    Reuse the existing menu-item click pattern (close menu, then call handler).
  - `hasKebab(stage)` already returns true for `running`/`awaiting_approval`
    (in `KEBAB_STATUSES`), so no change needed there; buttons only ever show on
    those statuses.
- **`app/App.tsx`:** `handleButton(stageId, name)` → `triggerStageButton`;
  pass `onButton={handleButton}` to `StagesList`. No modal (fire-immediately).

**Tests (frontend):** `use-status.test.ts` parses `buttons`;
`StagesList.test.tsx` renders one item per button in declared order, gated by
status, calls `onButton` with the label on click, hides the sub-block when
empty; App handler wiring.

## Non-goals

- No flow-level/global buttons.
- No confirmation modal / prefill-and-edit (fire immediately).
- No button availability on `pending`/`done`/`failed` (no live agent).
- No per-button icons, styling, or conditional visibility beyond the status gate.
- No new agent-execution path — strictly reuses Revise.

## Testing summary

TDD, one failing test first per unit, across four layers:
`pkg/flow` (schema/order/validation) → `pkg/orchestrator` (`Button`→Revise) →
`pkg/server` (endpoint + StageView) → frontend (client/hook/list/app). Plus a
final live browser run on a real flow to confirm a click restarts the agent
with the button's prompt.
