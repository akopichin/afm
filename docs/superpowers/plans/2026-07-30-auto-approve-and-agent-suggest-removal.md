# Auto-Approve Stage Flag + agent_suggest Flag Removal Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** (1) Delete the `agent_suggest` experimental flag/env var and make its gated behavior (revise-while-running) unconditional; (2) add a per-stage `auto_approve: bool` flow.yaml field that, when true, auto-approves a stage's plan the instant it's ready — regardless of dashboard attachment or `--require-approval` — so CI runs can mix human-reviewed and fully automatic stages.

**Architecture:** Task 1 is a pure deletion (config field, server gate, frontend conditional) — no new code paths. Task 2 adds one new `flow.Stage` field, one new orchestrator helper called from the three places that fire `EvPlanReady`, one new static map threaded through the server the same way `stage_interactive` already is, and a frontend badge that replaces the approve/revise buttons when the flag is set.

**Tech Stack:** Go (backend, orchestrator FSM), TypeScript/React + Vitest (dashboard), YAML (flow config).

## Global Constraints

- Commits are in Russian (per project CLAUDE.md) — every commit message in this plan is already written in Russian; keep that when committing.
- Never add `Co-Authored-By` to commits.
- Do not change the Go version in `go.mod`.
- After any Go change, code must pass `make lint` (pre-commit hook runs `make lint && make build && make test` — budget several minutes per commit).
- No placeholders, no TODOs, no speculative validation beyond what the spec calls for.

---

## Task 1: Remove `agent_suggest` — backend (config, server, wiring)

**Files:**
- Modify: `pkg/config/config.go:228-243` (remove `ExperimentalConfig`, `IsAgentSuggestEnabled`), `:246-256` (remove `Experimental` field), `:377-379` (remove overlay merge)
- Modify: `pkg/server/handlers.go:24-33` (remove `AgentSuggestEnabled` from `statusResponse`), `:170-186` (`handleRevise` gate)
- Modify: `pkg/server/server.go:83` (`Server.agentSuggestEnabled`), `:112` (`Config.AgentSuggestEnabled`), `:149` (wiring in `New`)
- Modify: `cmd/afm/run.go:268` (remove `AgentSuggestEnabled: ...`)
- Modify: `pkg/orchestrator/agent_suggest_test.go:117-119` (drop the now-meaningless config setup)
- Test: `pkg/config/config_test.go:730-754` (delete `TestIsAgentSuggestEnabled`)
- Test: `pkg/server/handlers_test.go:799-904` (delete/rename tests)

**Interfaces:**
- Produces: `handleRevise` allows `running`-stage revise unconditionally — no config/flag dependency for any later task.

- [ ] **Step 1: Remove `ExperimentalConfig` and its wiring from `pkg/config/config.go`**

Delete the type, its method, the `Config.Experimental` field, and the overlay-merge branch:

```go
// DELETE (lines 228-243):
// ExperimentalConfig настраивает фичи под флагом, ещё не готовые для дефолтного
// включения. agent_suggest — вброс фразы-поправки агенту на активной стадии
// (не только awaiting_approval, как у обычного Revise) — см.
// docs/superpowers/specs/2026-07-27-agent-suggest-design.md.
type ExperimentalConfig struct {
	AgentSuggest *bool `yaml:"agent_suggest"` // nil = смотрим AFM_EXP_AGENT_SUGGEST
}

// IsAgentSuggestEnabled reports whether the agent_suggest experimental
// feature is enabled — explicit config value takes priority over the env var.
func (e ExperimentalConfig) IsAgentSuggestEnabled() bool {
	if e.AgentSuggest != nil {
		return *e.AgentSuggest
	}
	return envFlag("AFM_EXP_AGENT_SUGGEST")
}
```

In `Config` struct, remove the `Experimental` field:

```go
// BEFORE:
type Config struct {
	Client       ClientConfig       `yaml:"client"`
	Executor     ExecutorConfig     `yaml:"executor"`
	Server       ServerConfig       `yaml:"server"`
	Docker       DockerConfig       `yaml:"docker"`
	Supervisor   SupervisorConfig   `yaml:"supervisor"`
	Experimental ExperimentalConfig `yaml:"experimental"`
	PromptsDir   string             `yaml:"prompts_dir"`
	Theme        string             `yaml:"theme"`
	SkinDir      string             `yaml:"skin_dir"`
}

// AFTER:
type Config struct {
	Client     ClientConfig     `yaml:"client"`
	Executor   ExecutorConfig   `yaml:"executor"`
	Server     ServerConfig     `yaml:"server"`
	Docker     DockerConfig     `yaml:"docker"`
	Supervisor SupervisorConfig `yaml:"supervisor"`
	PromptsDir string           `yaml:"prompts_dir"`
	Theme      string           `yaml:"theme"`
	SkinDir    string           `yaml:"skin_dir"`
}
```

In `mergeFile`, remove the overlay branch:

```go
// DELETE:
	if overlay.Experimental.AgentSuggest != nil {
		dst.Experimental.AgentSuggest = overlay.Experimental.AgentSuggest
	}
```

- [ ] **Step 2: Delete `TestIsAgentSuggestEnabled` from `pkg/config/config_test.go`**

Delete the entire function (lines 730-754, starting `func TestIsAgentSuggestEnabled(t *testing.T) {` through its closing `}`).

- [ ] **Step 3: Run config tests to confirm nothing else references the removed type**

Run: `go test ./pkg/config/... -v`
Expected: PASS, no compile errors about `ExperimentalConfig`/`AgentSuggest`.

- [ ] **Step 4: Remove the flag from `pkg/server` (statusResponse, Config, Server)**

In `pkg/server/handlers.go`:

```go
// BEFORE:
// statusResponse расширяет снапшот двумя per-stage картами для UI:
// stage_interactive (статический конфиг флоу) и stage_autonomous (рантайм,
// по наличию autonomous.flag в директории стадии).
type statusResponse struct {
	state.RunState
	Description         string          `json:"description,omitempty"`
	StageInteractive    map[string]bool `json:"stage_interactive,omitempty"`
	StageAutonomous     map[string]bool `json:"stage_autonomous,omitempty"`
	AgentSuggestEnabled bool            `json:"agent_suggest_enabled,omitempty"`
}

// AFTER:
// statusResponse расширяет снапшот двумя per-stage картами для UI:
// stage_interactive (статический конфиг флоу) и stage_autonomous (рантайм,
// по наличию autonomous.flag в директории стадии).
type statusResponse struct {
	state.RunState
	Description      string          `json:"description,omitempty"`
	StageInteractive map[string]bool `json:"stage_interactive,omitempty"`
	StageAutonomous  map[string]bool `json:"stage_autonomous,omitempty"`
}
```

Update `handleStatus` (remove `AgentSuggestEnabled: s.agentSuggestEnabled` from the `resp := statusResponse{...}` literal):

```go
	resp := statusResponse{
		RunState:         rs,
		Description:      s.Description,
		StageInteractive: s.stageInteractive,
		StageAutonomous:  autonomous,
	}
```

Update `handleRevise`'s gate to be unconditional:

```go
// BEFORE:
	allowed := st.Status == state.StatusAwaitingApproval ||
		(st.Status == state.StatusRunning && s.agentSuggestEnabled)
	if !allowed {
		http.Error(w, fmt.Sprintf("stage is %s, not awaiting_approval (or running, with agent_suggest enabled)", st.Status), http.StatusBadRequest)
		return
	}

// AFTER:
	allowed := st.Status == state.StatusAwaitingApproval || st.Status == state.StatusRunning
	if !allowed {
		http.Error(w, fmt.Sprintf("stage is %s, not awaiting_approval or running", st.Status), http.StatusBadRequest)
		return
	}
```

- [ ] **Step 5: Remove `agentSuggestEnabled` from `pkg/server/server.go`**

Remove the field from `Server` struct:

```go
// BEFORE:
	dialogAnswerFn      func(stageID, phase, qID, answer string, fromOptions bool) error
	dialogCancelFn      func(stageID string) error
	agentSuggestEnabled bool
	theme               string       // "goga" или "" (default coffee)

// AFTER:
	dialogAnswerFn func(stageID, phase, qID, answer string, fromOptions bool) error
	dialogCancelFn func(stageID string) error
	theme          string // "goga" или "" (default coffee)
```

Remove the field from `Config` struct:

```go
// BEFORE:
	DialogAnswerFn      func(stageID, phase, qID, answer string, fromOptions bool) error
	DialogCancelFn      func(stageID string) error
	AgentSuggestEnabled bool // gate for agent_suggest experimental feature (config.Experimental.IsAgentSuggestEnabled())
	Theme               string

// AFTER:
	DialogAnswerFn func(stageID, phase, qID, answer string, fromOptions bool) error
	DialogCancelFn func(stageID string) error
	Theme          string
```

Remove the assignment in `New`:

```go
// DELETE this line from the `s := &Server{...}` literal:
		agentSuggestEnabled: cfg.AgentSuggestEnabled,
```

(Re-run `gofmt`/rely on `make lint --fix` to re-align the remaining struct literal columns — do not hand-align.)

- [ ] **Step 6: Remove wiring from `cmd/afm/run.go`**

```go
// DELETE this line from the `server.Config{...}` literal at run.go:254:
					AgentSuggestEnabled: cfg.Experimental.IsAgentSuggestEnabled(),
```

- [ ] **Step 7: Fix up `pkg/server/handlers_test.go`**

Delete `TestHandleRevise_RunningRequiresAgentSuggestFlag` entirely (lines 799-837).

Rename `TestHandleRevise_RunningAllowedWithFlag` to `TestHandleRevise_RunningAllowed` and drop the flag:

```go
// BEFORE:
func TestHandleRevise_RunningAllowedWithFlag(t *testing.T) {
	...
	var reviseCalled bool
	srv := New(Config{
		RunDir: runDir,
		Store:  store,
		UIBus:  orchestrator.NewUIBus(),
		ReviseFn: func(_ context.Context, _, _ string) error {
			reviseCalled = true
			return nil
		},
		AgentSuggestEnabled: true,
	})
	...
	if !reviseCalled {
		t.Error("reviseFn should be called when agent_suggest is enabled and stage is running")
	}
}

// AFTER:
func TestHandleRevise_RunningAllowed(t *testing.T) {
	...
	var reviseCalled bool
	srv := New(Config{
		RunDir: runDir,
		Store:  store,
		UIBus:  orchestrator.NewUIBus(),
		ReviseFn: func(_ context.Context, _, _ string) error {
			reviseCalled = true
			return nil
		},
	})
	...
	if !reviseCalled {
		t.Error("reviseFn should be called for a running stage")
	}
}
```

Delete `TestHandleStatus_AgentSuggestEnabledReflectsConfig` entirely (the function that builds `Config{..., AgentSuggestEnabled: true}` and asserts `resp["agent_suggest_enabled"]`).

- [ ] **Step 8: Fix up `pkg/orchestrator/agent_suggest_test.go`**

```go
// BEFORE:
	runner := &blockingThenFeedbackRunner{stageID: "impl"}
	cfg := config.Default()
	trueVal := true
	cfg.Experimental.AgentSuggest = &trueVal

	orch := orchestrator.New(orchestrator.Options{

// AFTER:
	runner := &blockingThenFeedbackRunner{stageID: "impl"}
	cfg := config.Default()

	orch := orchestrator.New(orchestrator.Options{
```

(This config value never actually gated the behavior under test — `orch.Revise()` on a `running` stage has always been unconditional at the FSM level; only the HTTP handler enforced the flag. Confirm this by reading the rest of the test after the edit — it calls `orch.Revise(ctx, "impl", ...)` directly, not through the HTTP layer.)

- [ ] **Step 9: Run full backend test suite**

Run: `go build ./... && go test ./pkg/config/... ./pkg/server/... ./pkg/orchestrator/... ./cmd/... -v`
Expected: PASS. No references to `AgentSuggestEnabled`/`ExperimentalConfig`/`agent_suggest_enabled` remain anywhere except the renamed/kept test names and unrelated doc comments.

Run: `grep -rn "AgentSuggestEnabled\|ExperimentalConfig\|agent_suggest_enabled" --include="*.go" .`
Expected: no output.

- [ ] **Step 10: Commit**

```bash
git add pkg/config/config.go pkg/config/config_test.go pkg/server/handlers.go pkg/server/server.go pkg/server/handlers_test.go cmd/afm/run.go pkg/orchestrator/agent_suggest_test.go
git commit -m "$(cat <<'EOF'
fix(server,config): убираем флаг agent_suggest — revise во время running теперь безусловный
EOF
)"
```

---

## Task 2: Remove `agent_suggest` — frontend

**Files:**
- Modify: `pkg/web/dashboard/src/hooks/use-status/use-status.ts:10-136`
- Modify: `pkg/web/dashboard/src/app/App.tsx:29,190-196`
- Modify: `pkg/web/dashboard/src/components/stages-list/StagesList.tsx:10-181`
- Test: `pkg/web/dashboard/src/app/App.test.tsx:316`
- Test: `pkg/web/dashboard/src/components/stages-list/StagesList.test.tsx` (all 14 `agentSuggestEnabled` usages)

**Interfaces:**
- Consumes: nothing from Task 1 (frontend and backend removal are independent; the frontend simply stops reading a JSON field the backend no longer sends).
- Produces: `<StagesList>` no longer takes an `agentSuggestEnabled` prop — Task 7 (adding `autoApprove` to `Stage`) does not touch this component, so no future task depends on this prop's removal beyond "it's gone."

- [ ] **Step 1: Remove `agentSuggestEnabled` from `use-status.ts`**

```ts
// BEFORE:
export type FlowStatus = {
  flowName: string
  stages: Stage[]
  startedAt: string
  description?: string
  // Гейт экспериментальной фичи agent_suggest (config.Experimental, Task 1..7):
  // из него зависит видимость кебаб-меню «Добавить поправку агенту» в StagesList
  // (см. Task 8). Поле — statusResponse.AgentSuggestEnabled (`agent_suggest_enabled`).
  agentSuggestEnabled: boolean
}

const EMPTY_STATUS: FlowStatus = { flowName: '', stages: [], startedAt: '', agentSuggestEnabled: false }

// AFTER:
export type FlowStatus = {
  flowName: string
  stages: Stage[]
  startedAt: string
  description?: string
}

const EMPTY_STATUS: FlowStatus = { flowName: '', stages: [], startedAt: '' }
```

```ts
// BEFORE (in normalizeStatus):
  const description = typeof obj.description === 'string' ? obj.description : undefined
  const agentSuggestEnabled = obj.agent_suggest_enabled === true

  const stagesObj = isRecord(obj.stages) ? obj.stages : {}
  ...
  return { flowName, stages, startedAt, description, agentSuggestEnabled }

// AFTER:
  const description = typeof obj.description === 'string' ? obj.description : undefined

  const stagesObj = isRecord(obj.stages) ? obj.stages : {}
  ...
  return { flowName, stages, startedAt, description }
```

- [ ] **Step 2: Remove the prop from `App.tsx`**

```tsx
// BEFORE:
  const { flowName, stages, startedAt, description, agentSuggestEnabled, refresh } = useStatus()

// AFTER:
  const { flowName, stages, startedAt, description, refresh } = useStatus()
```

```tsx
// BEFORE:
              <StagesList
                stages={stages}
                selectedStageId={selectedStageId}
                onSelect={setSelectedStageId}
                agentSuggestEnabled={agentSuggestEnabled}
                onAddNote={setNoteModalStageId}
              />

// AFTER:
              <StagesList
                stages={stages}
                selectedStageId={selectedStageId}
                onSelect={setSelectedStageId}
                onAddNote={setNoteModalStageId}
              />
```

- [ ] **Step 3: Remove the prop and simplify the render gate in `StagesList.tsx`**

```tsx
// BEFORE:
type StagesListProps = {
  stages: Stage[]
  selectedStageId: string | null
  onSelect: (stageId: string) => void
  agentSuggestEnabled: boolean
  onAddNote?: (stageId: string) => void // вызывается при клике на пункт меню
}

// AFTER:
type StagesListProps = {
  stages: Stage[]
  selectedStageId: string | null
  onSelect: (stageId: string) => void
  onAddNote?: (stageId: string) => void // вызывается при клике на пункт меню
}
```

```tsx
// BEFORE:
export function StagesList({ stages, selectedStageId, onSelect, agentSuggestEnabled, onAddNote }: StagesListProps): ReactElement {

// AFTER:
export function StagesList({ stages, selectedStageId, onSelect, onAddNote }: StagesListProps): ReactElement {
```

```tsx
// BEFORE:
            {agentSuggestEnabled && KEBAB_STATUSES.has(stage.status) && (

// AFTER:
            {KEBAB_STATUSES.has(stage.status) && (
```

Also update the comment above `KEBAB_STATUSES` (line 18-20) to drop the "gated by agent_suggest" framing:

```ts
// BEFORE:
// Статусы, при которых у стадии доступен кебаб (agent_suggest): агент ещё
// выполняется (running) или ждёт одобрения плана (awaiting_approval) — оба
// случая, когда POST /api/stages/{id}/revise принимает поправку (см. Task 7).

// AFTER:
// Статусы, при которых у стадии доступен кебаб (добавить заметку агенту):
// агент ещё выполняется (running) или ждёт одобрения плана (awaiting_approval) —
// оба случая, когда POST /api/stages/{id}/revise принимает поправку.
```

- [ ] **Step 4: Fix up `App.test.tsx`**

```ts
// BEFORE:
          json: async () => ({
            flow_name: 'demo',
            stage_order: ['s1'],
            stage_names: { s1: 'Propose' },
            stages: { s1: { status: 'running', updated_at: '' } },
            agent_suggest_enabled: true,
          }),

// AFTER:
          json: async () => ({
            flow_name: 'demo',
            stage_order: ['s1'],
            stage_names: { s1: 'Propose' },
            stages: { s1: { status: 'running', updated_at: '' } },
          }),
```

- [ ] **Step 5: Fix up `StagesList.test.tsx`**

Remove the `agentSuggestEnabled={...}` prop from every `<StagesList ... />` render call in the file (14 occurrences: lines 14, 29, 41, 54, 65, 70/77/81 in the kebab test, 89, 106, 119, 132, 136, 144, 146, 149).

Delete the test that specifically asserted the flag gate, and replace it with one asserting the kebab menu is unconditional:

```tsx
// BEFORE:
  test('shows the kebab menu only when agentSuggestEnabled and status is running or awaiting_approval', () => {
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false },
      { id: 'b', name: '', status: 'done', updatedAt: '', interactive: false, autonomous: false },
      { id: 'c', name: '', status: 'awaiting_approval', updatedAt: '', interactive: false, autonomous: false },
    ]
    const { rerender } = render(
      <StagesList stages={stages} selectedStageId={null} onSelect={() => {}} agentSuggestEnabled={true} />,
    )
    expect(screen.getAllByRole('button', { name: /more actions/i })).toHaveLength(2) // a и c, не b

    rerender(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} agentSuggestEnabled={false} />)
    expect(screen.queryAllByRole('button', { name: /more actions/i })).toHaveLength(0)
  })

// AFTER:
  test('shows the kebab menu only when status is running or awaiting_approval', () => {
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false },
      { id: 'b', name: '', status: 'done', updatedAt: '', interactive: false, autonomous: false },
      { id: 'c', name: '', status: 'awaiting_approval', updatedAt: '', interactive: false, autonomous: false },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} />)
    expect(screen.getAllByRole('button', { name: /more actions/i })).toHaveLength(2) // a и c, не b
  })
```

For every other occurrence, apply the same mechanical removal, e.g.:

```tsx
// BEFORE:
    render(<StagesList stages={stages} selectedStageId="s2" onSelect={onSelect} agentSuggestEnabled={false} />)
// AFTER:
    render(<StagesList stages={stages} selectedStageId="s2" onSelect={onSelect} />)
```

- [ ] **Step 6: Run frontend tests and lint**

Run: `cd pkg/web/dashboard && npm run build && npx vitest run`
Expected: PASS. Then:

Run: `grep -rn "agentSuggestEnabled\|agent_suggest" pkg/web/dashboard/src`
Expected: no output.

- [ ] **Step 7: Commit**

```bash
git add pkg/web/dashboard/src
git commit -m "$(cat <<'EOF'
fix(dashboard): убираем agent_suggest — кебаб-меню правки теперь доступно всегда
EOF
)"
```

---

## Task 3: Remove `agent_suggest` — docs

**Files:**
- Modify: `config.example.yaml:63-73`
- Modify: `README.md:426, 477-479, 526-534, 560`

**Interfaces:** none (doc-only task, no code dependents).

- [ ] **Step 1: `config.example.yaml`**

```yaml
# BEFORE (lines 63-73):

# Experimental features — off by default, opt in explicitly.
# experimental:
#   # agent_suggest: let a user add a corrective note to a stage that's
#   # currently `running` (not only at the `awaiting_approval` checkpoint),
#   # via the dashboard's kebab (⋮) menu or POST /api/stages/{id}/revise.
#   # The agent finishes its current step, gets SIGINT, and restarts the
#   # same phase with the note folded into its context.
#   # nil/absent → falls back to $AFM_EXP_AGENT_SUGGEST (1 = on).
#   # Default: false
#   # agent_suggest: true

# AFTER: delete the whole block (lines 63-73), leaving a single blank line
# between the preceding and following sections.
```

- [ ] **Step 2: `README.md` — status list entry (line 426)**

```md
<!-- BEFORE -->
- `revising` — feedback was sent and the AI is reworking: either the plan (from `awaiting_approval`), or — with the experimental `agent_suggest` flag on — a `running` stage that just got a note and a graceful interrupt (see "Suggesting a Note to a Running Stage" below)

<!-- AFTER -->
- `revising` — feedback was sent and the AI is reworking: either the plan (from `awaiting_approval`), or a `running` stage that just got a note and a graceful interrupt (see "Suggesting a Note to a Running Stage" below)
```

- [ ] **Step 3: `README.md` — config example block (lines 474-480)**

```yaml
<!-- BEFORE -->
supervisor:
  command: glm51            # the supervisor agent's command (for stages with supervisor: true)

experimental:
  agent_suggest: false      # true / env AFM_EXP_AGENT_SUGGEST=1 — allow adding a note
                            # to a stage while it's running (see "Suggesting a Note...")

# theme: coffee             # dashboard theme: coffee | goga | novacorps (default: coffee)

<!-- AFTER -->
supervisor:
  command: glm51            # the supervisor agent's command (for stages with supervisor: true)

# theme: coffee             # dashboard theme: coffee | goga | novacorps (default: coffee)
```

- [ ] **Step 4: `README.md` — "Suggesting a Note to a Running Stage" section (lines 526-534)**

```md
<!-- BEFORE -->
### Suggesting a Note to a Running Stage (experimental)

Normally you can only redirect a stage at the `awaiting_approval` checkpoint (see "Inline Plan Comments" above). With the experimental `agent_suggest` flag enabled (`experimental.agent_suggest: true` in config, or `AFM_EXP_AGENT_SUGGEST=1`), you can also do it while a stage is actively `running`:

1. Click the kebab (⋮) menu on a `running` (or `awaiting_approval`) stage row and choose "Add a note for the agent".
2. Type the note and send — the agent finishes its current step, then receives SIGINT (a graceful interrupt, not a kill).
3. The stage moves through `revising` and restarts the same phase (planning/implementation/review/autonomous) with your note folded into its context, then continues toward `done`.

Without the flag (the default), the kebab menu isn't shown and the equivalent `POST /api/stages/{id}/revise` call on a `running` stage is rejected with 400 — behavior is unchanged from before this feature existed.

<!-- AFTER -->
### Suggesting a Note to a Running Stage

Normally you can only redirect a stage at the `awaiting_approval` checkpoint (see "Inline Plan Comments" above). You can also do it while a stage is actively `running`:

1. Click the kebab (⋮) menu on a `running` (or `awaiting_approval`) stage row and choose "Add a note for the agent".
2. Type the note and send — the agent finishes its current step, then receives SIGINT (a graceful interrupt, not a kill).
3. The stage moves through `revising` and restarts the same phase (planning/implementation/review/autonomous) with your note folded into its context, then continues toward `done`.
```

- [ ] **Step 5: `README.md` — directory structure comment (line 560)**

```md
<!-- BEFORE -->
        feedback.md      # revision notes (plan revise, or agent_suggest on a running stage)

<!-- AFTER -->
        feedback.md      # revision notes (plan revise, or a note added to a running stage)
```

- [ ] **Step 6: Verify no stray mentions remain**

Run: `grep -rn "agent_suggest" README.md config.example.yaml`
Expected: no output.

- [ ] **Step 7: Commit**

```bash
git add README.md config.example.yaml
git commit -m "$(cat <<'EOF'
docs: убираем упоминания флага agent_suggest — фича безусловна
EOF
)"
```

---

## Task 4: `auto_approve` — flow.Stage field + validation

**Files:**
- Modify: `pkg/flow/flow.go:56-101` (add field)
- Test: `pkg/flow/auto_approve_test.go` (new)

**Interfaces:**
- Produces: `flow.Stage.AutoApprove bool` (yaml tag `auto_approve`) — consumed by Task 5 (orchestrator) and Task 6 (server wiring via `cmd/afm/run.go`).

- [ ] **Step 1: Add the field**

```go
// In pkg/flow/flow.go, Stage struct — add after ScriptAfterTimeout (end of struct):
	ScriptAfter         string        `yaml:"script_after"`
	ScriptAfterTimeout  time.Duration `yaml:"script_after_timeout"`
	// AutoApprove, if true, approves this stage's plan automatically the
	// instant it's ready (awaiting_approval), with no human interaction —
	// regardless of whether a dashboard is attached and regardless of
	// --require-approval. Default false. Intended for CI runs where some
	// stages need human review and others don't.
	AutoApprove bool `yaml:"auto_approve"`
}
```

No validation is added: on stage types that never reach `awaiting_approval` (`agents: [auto]`, preset `plan:` path, `interactive: true`), the field is a harmless no-op, consistent with how those stage types already ignore unrelated fields.

- [ ] **Step 2: Write the failing test**

Create `pkg/flow/auto_approve_test.go`:

```go
package flow

import "testing"

func TestParse_AutoApproveDefaultsFalse(t *testing.T) {
	f, err := writeFlow(t, `
name: f
stages:
  - id: a
    agents: [planning, implementation]
`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if f.Stages[0].AutoApprove {
		t.Error("AutoApprove should default to false when auto_approve is absent from YAML")
	}
}

func TestParse_AutoApproveTrue(t *testing.T) {
	f, err := writeFlow(t, `
name: f
stages:
  - id: a
    agents: [planning, implementation]
    auto_approve: true
`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !f.Stages[0].AutoApprove {
		t.Error("AutoApprove should be true when auto_approve: true is set in YAML")
	}
}

func TestParse_AutoApproveOnAutoStage_NoErrorNoOp(t *testing.T) {
	// auto_approve is a documented no-op on agents:[auto] stages (they skip
	// the approval gate entirely) — parsing must not reject the combination.
	f, err := writeFlow(t, `
name: f
stages:
  - id: a
    agents: [auto]
    auto_approve: true
`)
	if err != nil {
		t.Fatalf("auto_approve + agents:[auto] should parse without error: %v", err)
	}
	if !f.Stages[0].AutoApprove {
		t.Error("AutoApprove field should still be true even though it has no effect on an auto stage")
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./pkg/flow/... -run TestParse_AutoApprove -v`
Expected: FAIL with `f.Stages[0].AutoApprove undefined (type Stage has no field or method AutoApprove)`.

- [ ] **Step 4: Confirm the field from Step 1 makes it pass**

Run: `go test ./pkg/flow/... -run TestParse_AutoApprove -v`
Expected: PASS (3/3).

- [ ] **Step 5: Run the full flow package test suite**

Run: `go test ./pkg/flow/... -v`
Expected: PASS, no regressions in existing `TestParse_*`/`TestIsAuto`/etc.

- [ ] **Step 6: Commit**

```bash
git add pkg/flow/flow.go pkg/flow/auto_approve_test.go
git commit -m "$(cat <<'EOF'
feat(flow): добавляем стейдж-флаг auto_approve
EOF
)"
```

---

## Task 5: `auto_approve` — orchestrator wiring

**Files:**
- Modify: `pkg/orchestrator/control_api.go` (add `autoApproveIfConfigured` helper, near `approveStage`)
- Modify: `pkg/orchestrator/orchestrator.go:358-377` (`onAgentCompleted`, `phasePlanning` case)
- Modify: `pkg/orchestrator/recovery.go:104-108` (Retrying case), `:151-158` (default case)
- Test: `pkg/orchestrator/auto_approve_test.go` (new)

**Interfaces:**
- Consumes: `flow.Stage.AutoApprove` (Task 4).
- Produces: `(*Orchestrator).autoApproveIfConfigured(ctx context.Context, stage flow.Stage) bool` — not consumed by any later task, but keep the exact name/signature since Task 6/7 tests reference stage behavior it produces (auto-approval happening without a dashboard click).

- [ ] **Step 1: Write the failing tests first**

Create `pkg/orchestrator/auto_approve_test.go`:

```go
package orchestrator_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/state"
)

// TestAutoApprove_WinsOverRequireApproval verifies that a stage with
// auto_approve: true is approved even when the whole run was started with
// --require-approval (which would normally FailStage a headless run with no
// dashboard attached, per the existing "Headless: нет дашборда" branch in
// onAgentCompleted).
func TestAutoApprove_WinsOverRequireApproval(t *testing.T) {
	stages := []flow.Stage{
		{ID: "ci-stage", Name: "CI Stage", Description: "auto approved in CI",
			Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}, AutoApprove: true},
	}

	runDir := t.TempDir()
	store, err := state.Open(runDir, []string{"ci-stage"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	stateFile := filepath.Join(runDir, "state.json")

	base := mockRunner(t, mockPlanningScript)
	runner := &doneCreatingRunner{delegate: base}

	orch := orchestrator.New(orchestrator.Options{
		RunDir:          runDir,
		Stages:          stages,
		Store:           store,
		Config:          config.Default(),
		Prompts:         orchestrator.DefaultPrompts(),
		Runner:          runner,
		RequireApproval: true,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := orch.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	final := loadStateJSON(t, stateFile)
	if final.Stages["ci-stage"].Status != state.StatusDone {
		t.Errorf("expected done (auto_approve overrides --require-approval), got %v", final.Stages["ci-stage"].Status)
	}
}

// TestAutoApprove_DefaultFalse_RequireApprovalStillFails is the regression
// guard: without auto_approve, --require-approval on a headless run must
// still fail the stage exactly as before this feature existed.
func TestAutoApprove_DefaultFalse_RequireApprovalStillFails(t *testing.T) {
	stages := []flow.Stage{
		{ID: "manual-stage", Name: "Manual Stage", Description: "needs a human",
			Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
	}

	runDir := t.TempDir()
	store, err := state.Open(runDir, []string{"manual-stage"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	stateFile := filepath.Join(runDir, "state.json")

	base := mockRunner(t, mockPlanningScript)
	runner := &doneCreatingRunner{delegate: base}

	orch := orchestrator.New(orchestrator.Options{
		RunDir:          runDir,
		Stages:          stages,
		Store:           store,
		Config:          config.Default(),
		Prompts:         orchestrator.DefaultPrompts(),
		Runner:          runner,
		RequireApproval: true,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := orch.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	final := loadStateJSON(t, stateFile)
	if final.Stages["manual-stage"].Status != state.StatusFailed {
		t.Errorf("expected failed (no auto_approve, --require-approval, no dashboard), got %v", final.Stages["manual-stage"].Status)
	}
}

// TestAutoApprove_WithDashboardAttached_SkipsManualClick verifies auto_approve
// fires even when a dashboard IS attached (SetDashboardURL), where the
// existing headless branch would NOT have auto-approved anything — proving
// auto_approve is independent of the headless/dashboard distinction. The
// sibling "manual" stage (no auto_approve, no simulated dashboard click) is
// used as a control: it must stay stuck at awaiting_approval forever in this
// test, since nothing here ever approves it.
func TestAutoApprove_WithDashboardAttached_SkipsManualClick(t *testing.T) {
	stages := []flow.Stage{
		{ID: "auto", Name: "Auto", Description: "auto approved",
			Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}, AutoApprove: true},
		{ID: "manual", Name: "Manual", Description: "needs a human click",
			Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
	}

	runDir := t.TempDir()
	store, err := state.Open(runDir, []string{"auto", "manual"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	stateFile := filepath.Join(runDir, "state.json")

	base := mockRunner(t, mockPlanningScript)
	runner := &doneCreatingRunner{delegate: base}

	orch := orchestrator.New(orchestrator.Options{
		RunDir:  runDir,
		Stages:  stages,
		Store:   store,
		Config:  config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
		Runner:  runner,
	})
	orch.SetDashboardURL("http://127.0.0.1:9999")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _ = orch.Run(ctx) }()

	waitForStatus(t, stateFile, "auto", state.StatusDone, 8*time.Second)

	final := loadStateJSON(t, stateFile)
	if final.Stages["manual"].Status != state.StatusAwaitingApproval {
		t.Errorf("expected manual stage stuck at awaiting_approval, got %v", final.Stages["manual"].Status)
	}
}

// TestAutoApprove_RecoveryDefaultCase_NoBusHelperNeeded exercises the
// recovery.go "default:" EvPlanReady site (crash with status=planning and
// plan.md already on disk). Deliberately does NOT register a bus-based
// auto-approver (unlike TestIntegration_ResumeFromPlanningWithExistingPlan) —
// if this test passes, the recovery-path auto_approve check itself did the
// approving, not a simulated dashboard click.
func TestAutoApprove_RecoveryDefaultCase_NoBusHelperNeeded(t *testing.T) {
	stages := []flow.Stage{
		{ID: "planned-ci", Name: "Planned CI", Description: "already planned",
			Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}, AutoApprove: true},
	}

	runDir := t.TempDir()
	stageDir := filepath.Join(runDir, "planned-ci")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# Existing Plan\n"), 0644); err != nil {
		t.Fatal(err)
	}

	store, err := state.Open(runDir, []string{"planned-ci"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	_ = store.Apply(&state.Transition{StageID: "planned-ci", From: state.StatusPending, To: state.StatusPlanning, Event: "test_setup"})
	stateFile := filepath.Join(runDir, "state.json")

	base := mockRunner(t, mockImplementationScript)
	runner := &doneCreatingRunner{delegate: base}

	orch := orchestrator.New(orchestrator.Options{
		RunDir:  runDir,
		Stages:  stages,
		Store:   store,
		Config:  config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
		Runner:  runner,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := orch.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	final := loadStateJSON(t, stateFile)
	if final.Stages["planned-ci"].Status != state.StatusDone {
		t.Errorf("expected done via recovery auto-approve, got %v", final.Stages["planned-ci"].Status)
	}
}

// TestAutoApprove_RecoveryRetryingWithExistingPlan_NoBusHelperNeeded exercises
// the recovery.go "case state.StatusRetrying:" EvPlanReady site (crash while
// Retrying, with plan.md already on disk). Same "no bus helper" proof as
// TestAutoApprove_RecoveryDefaultCase_NoBusHelperNeeded, targeting the sibling
// code path.
func TestAutoApprove_RecoveryRetryingWithExistingPlan_NoBusHelperNeeded(t *testing.T) {
	stages := []flow.Stage{
		{ID: "retry-ci", Name: "Retry CI", Description: "was retrying with a plan already on disk",
			Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}, AutoApprove: true},
	}

	runDir := t.TempDir()
	stageDir := filepath.Join(runDir, "retry-ci")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# Existing Plan\n"), 0644); err != nil {
		t.Fatal(err)
	}

	store, err := state.Open(runDir, []string{"retry-ci"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	_ = store.Apply(&state.Transition{StageID: "retry-ci", From: state.StatusPending, To: state.StatusRetrying, Event: "test_setup"})
	stateFile := filepath.Join(runDir, "state.json")

	base := mockRunner(t, mockImplementationScript)
	runner := &doneCreatingRunner{delegate: base}

	orch := orchestrator.New(orchestrator.Options{
		RunDir:  runDir,
		Stages:  stages,
		Store:   store,
		Config:  config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
		Runner:  runner,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := orch.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	final := loadStateJSON(t, stateFile)
	if final.Stages["retry-ci"].Status != state.StatusDone {
		t.Errorf("expected done via recovery auto-approve, got %v", final.Stages["retry-ci"].Status)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail (or hang) without the implementation**

Run: `go test ./pkg/orchestrator/... -run TestAutoApprove -v -timeout 60s`
Expected: `TestAutoApprove_WinsOverRequireApproval` FAILs with status `failed` instead of `done` (RequireApproval fires before any auto_approve check exists); the two recovery tests and the dashboard test time out or fail similarly (`AutoApprove` field exists from Task 4, but nothing reads it yet). `TestAutoApprove_DefaultFalse_RequireApprovalStillFails` already passes (it describes today's behavior).

- [ ] **Step 3: Add the `autoApproveIfConfigured` helper to `pkg/orchestrator/control_api.go`**

```go
// Add "log" to the existing import block:
import (
	"context"
	"fmt"
	"log"
	"path/filepath"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)
```

```go
// Add right after approveStage's doc comment / before runContext:

// autoApproveIfConfigured immediately approves a stage right after its plan
// becomes ready (EvPlanReady) if flow.yaml sets auto_approve: true on it —
// independent of whether a dashboard is attached and independent of
// --require-approval, both of which only govern the *default* (no
// auto_approve) headless behavior elsewhere in this package. Returns whether
// it auto-approved (callers use this to skip their own headless branch).
func (o *Orchestrator) autoApproveIfConfigured(ctx context.Context, stage flow.Stage) bool {
	if !stage.AutoApprove {
		return false
	}
	log.Printf("auto_approve: auto-approving plan for stage %q", stage.ID)
	o.approveStage(ctx, stage.ID)
	return true
}
```

- [ ] **Step 4: Wire it into `onAgentCompleted` (`pkg/orchestrator/orchestrator.go`)**

```go
// BEFORE:
		o.Trigger(ev.StageID, EvPlanReady, GuardCtx{}, "")
		// Headless: нет дашборда → никто не нажмёт Approve.
		// RequireApproval=true → fail-fast с понятным сообщением.
		// RequireApproval=false (дефолт) → авто-апрув, flow идёт дальше.
		if o.opts.DashboardURL == "" {
			if o.opts.RequireApproval {
				o.FailStage(ev.StageID, "approval required but no dashboard running (use --port or server.port in config)")
				return nil
			}
			log.Printf("headless: auto-approving plan for stage %q", ev.StageID)
			o.approveStage(ctx, ev.StageID)
			return nil
		}
		o.tryActivatePrePlanned(ctx)

// AFTER:
		o.Trigger(ev.StageID, EvPlanReady, GuardCtx{}, "")
		// auto_approve: true on the stage config wins regardless of
		// dashboard/--require-approval — checked before the headless branch.
		if stage := o.graph.Stage(ev.StageID); stage != nil && o.autoApproveIfConfigured(ctx, *stage) {
			return nil
		}
		// Headless: нет дашборда → никто не нажмёт Approve.
		// RequireApproval=true → fail-fast с понятным сообщением.
		// RequireApproval=false (дефолт) → авто-апрув, flow идёт дальше.
		if o.opts.DashboardURL == "" {
			if o.opts.RequireApproval {
				o.FailStage(ev.StageID, "approval required but no dashboard running (use --port or server.port in config)")
				return nil
			}
			log.Printf("headless: auto-approving plan for stage %q", ev.StageID)
			o.approveStage(ctx, ev.StageID)
			return nil
		}
		o.tryActivatePrePlanned(ctx)
```

- [ ] **Step 5: Wire it into `recovery.go`'s two `EvPlanReady` sites**

```go
// BEFORE (Retrying case, ~line 104-108):
			if checkPlanCompletion(stageDir) == nil && s.NeedsPlanning() {
				o.Trigger(s.ID, EvPlanReady, GuardCtx{}, "recovered plan.md")
				continue
			}

// AFTER:
			if checkPlanCompletion(stageDir) == nil && s.NeedsPlanning() {
				o.Trigger(s.ID, EvPlanReady, GuardCtx{}, "recovered plan.md")
				o.autoApproveIfConfigured(ctx, s)
				continue
			}
```

```go
// BEFORE (default case, ~line 151-158):
			if s.NeedsPlanning() {
				stageDir := filepath.Join(o.opts.RunDir, s.ID)
				if checkPlanCompletion(stageDir) == nil {
					o.Trigger(s.ID, EvPlanReady, GuardCtx{}, "recovered plan.md")
					continue
				}
			}

// AFTER:
			if s.NeedsPlanning() {
				stageDir := filepath.Join(o.opts.RunDir, s.ID)
				if checkPlanCompletion(stageDir) == nil {
					o.Trigger(s.ID, EvPlanReady, GuardCtx{}, "recovered plan.md")
					o.autoApproveIfConfigured(ctx, s)
					continue
				}
			}
```

- [ ] **Step 6: Run the new tests to verify they pass**

Run: `go test ./pkg/orchestrator/... -run TestAutoApprove -v -timeout 60s`
Expected: PASS (5/5).

- [ ] **Step 7: Run the full orchestrator test suite for regressions**

Run: `go test ./pkg/orchestrator/... -v -race`
Expected: PASS — in particular `TestIntegration_ResumeFromPlanningWithExistingPlan`, `TestIntegration_ResumeFromRetrying`, `TestAgentSuggest_InterruptRestartsWithFeedback`, and every other existing test must still pass unchanged (they don't set `AutoApprove`, so `autoApproveIfConfigured` is a no-op for them).

- [ ] **Step 8: Commit**

```bash
git add pkg/orchestrator/control_api.go pkg/orchestrator/orchestrator.go pkg/orchestrator/recovery.go pkg/orchestrator/auto_approve_test.go
git commit -m "$(cat <<'EOF'
feat(orchestrator): auto_approve мгновенно апрувит план независимо от дашборда и --require-approval
EOF
)"
```

---

## Task 6: `auto_approve` — server API + wiring

**Files:**
- Modify: `pkg/server/handlers.go:24-33,43-52` (`statusResponse`, `handleStatus`)
- Modify: `pkg/server/server.go:69-119,136-155` (`Server`, `Config`, `New`)
- Modify: `cmd/afm/run.go:250-258` (build the map from flow stages)
- Test: `pkg/server/handlers_test.go` (new test, mirroring `TestHandleStatus_IncludesInteractiveAndAutonomous`)

**Interfaces:**
- Consumes: `flow.Stage.AutoApprove` (Task 4).
- Produces: JSON field `stage_auto_approve` on `GET /api/status` — consumed by Task 7 (`use-status.ts` parsing).

- [ ] **Step 1: Write the failing test**

Add to `pkg/server/handlers_test.go`, right after `TestHandleStatus_IncludesInteractiveAndAutonomous`:

```go
func TestHandleStatus_IncludesAutoApprove(t *testing.T) {
	srv, _ := setupTestServer(t)
	srv.stageAutoApprove = map[string]bool{testStageID: true}

	req := httptest.NewRequest("GET", "/api/status", nil)
	w := httptest.NewRecorder()
	srv.handleStatus(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}
	var resp statusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.StageAutoApprove[testStageID] {
		t.Errorf("stage_auto_approve[%q] = false, want true", testStageID)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/server/... -run TestHandleStatus_IncludesAutoApprove -v`
Expected: FAIL with `srv.stageAutoApprove undefined (type Server has no field or method stageAutoApprove)`.

- [ ] **Step 3: Add `StageAutoApprove` to `statusResponse` and `handleStatus` (`pkg/server/handlers.go`)**

```go
// BEFORE:
type statusResponse struct {
	state.RunState
	Description      string          `json:"description,omitempty"`
	StageInteractive map[string]bool `json:"stage_interactive,omitempty"`
	StageAutonomous  map[string]bool `json:"stage_autonomous,omitempty"`
}

// AFTER:
type statusResponse struct {
	state.RunState
	Description      string          `json:"description,omitempty"`
	StageInteractive map[string]bool `json:"stage_interactive,omitempty"`
	StageAutonomous  map[string]bool `json:"stage_autonomous,omitempty"`
	StageAutoApprove map[string]bool `json:"stage_auto_approve,omitempty"`
}
```

```go
// BEFORE (handleStatus):
	resp := statusResponse{
		RunState:         rs,
		Description:      s.Description,
		StageInteractive: s.stageInteractive,
		StageAutonomous:  autonomous,
	}

// AFTER:
	resp := statusResponse{
		RunState:         rs,
		Description:      s.Description,
		StageInteractive: s.stageInteractive,
		StageAutonomous:  autonomous,
		StageAutoApprove: s.stageAutoApprove,
	}
```

- [ ] **Step 4: Add `stageAutoApprove`/`StageAutoApprove` to `Server`/`Config` (`pkg/server/server.go`)**

```go
// BEFORE (Server struct):
	runDir              string
	Description         string          // корневой description флоу (для хедера дашборда)
	stageInteractive    map[string]bool // id стадии → interactive (статический конфиг флоу)
	store               *state.Store

// AFTER:
	runDir              string
	Description         string          // корневой description флоу (для хедера дашборда)
	stageInteractive    map[string]bool // id стадии → interactive (статический конфиг флоу)
	stageAutoApprove    map[string]bool // id стадии → auto_approve (статический конфиг флоу)
	store               *state.Store
```

```go
// BEFORE (Config struct):
	Port                int
	RunDir              string
	Description         string // корневой description флоу (для хедера дашборда)
	StageInteractive    map[string]bool
	Store               *state.Store

// AFTER:
	Port                int
	RunDir              string
	Description         string // корневой description флоу (для хедера дашборда)
	StageInteractive    map[string]bool
	StageAutoApprove    map[string]bool
	Store               *state.Store
```

```go
// BEFORE (New()):
	s := &Server{
		runDir:              cfg.RunDir,
		Description:         cfg.Description,
		stageInteractive:    cfg.StageInteractive,
		store:               cfg.Store,

// AFTER:
	s := &Server{
		runDir:              cfg.RunDir,
		Description:         cfg.Description,
		stageInteractive:    cfg.StageInteractive,
		stageAutoApprove:    cfg.StageAutoApprove,
		store:               cfg.Store,
```

(Let `make lint --fix`/`gofmt` re-align struct literal columns; don't hand-align.)

- [ ] **Step 5: Wire it from `cmd/afm/run.go`**

```go
// BEFORE:
				stageInteractive := make(map[string]bool, len(f.Stages))
				for _, st := range f.Stages {
					stageInteractive[st.ID] = st.Interactive
				}
				srv := server.New(server.Config{
					Port:                cfg.Server.GetPort(),
					RunDir:              runDir,
					Description:         f.Description,
					StageInteractive:    stageInteractive,
					Store:               store,

// AFTER:
				stageInteractive := make(map[string]bool, len(f.Stages))
				stageAutoApprove := make(map[string]bool, len(f.Stages))
				for _, st := range f.Stages {
					stageInteractive[st.ID] = st.Interactive
					stageAutoApprove[st.ID] = st.AutoApprove
				}
				srv := server.New(server.Config{
					Port:                cfg.Server.GetPort(),
					RunDir:              runDir,
					Description:         f.Description,
					StageInteractive:    stageInteractive,
					StageAutoApprove:    stageAutoApprove,
					Store:               store,
```

- [ ] **Step 6: Run the test to verify it passes**

Run: `go test ./pkg/server/... -run TestHandleStatus_IncludesAutoApprove -v`
Expected: PASS.

- [ ] **Step 7: Run the full server and cmd test suites**

Run: `go build ./... && go test ./pkg/server/... ./cmd/... -v`
Expected: PASS, no regressions.

- [ ] **Step 8: Commit**

```bash
git add pkg/server/handlers.go pkg/server/server.go pkg/server/handlers_test.go cmd/afm/run.go
git commit -m "$(cat <<'EOF'
feat(server): отдаём stage_auto_approve в GET /api/status
EOF
)"
```

---

## Task 7: `auto_approve` — frontend (types, parsing, PlanPanel badge)

**Files:**
- Modify: `pkg/web/dashboard/src/types/stage.ts:21-28`
- Modify: `pkg/web/dashboard/src/hooks/use-status/use-status.ts` (`normalizeStatus`/`toStage`)
- Modify: `pkg/web/dashboard/src/components/plan-panel/PlanPanel.tsx`
- Modify: `pkg/web/dashboard/skins/base/plan-panel.css` (badge style)
- Test: `pkg/web/dashboard/src/components/plan-panel/PlanPanel.test.tsx`
- Test: fix up `Stage` object literals across `Footer.test.tsx`, `DialogChannel.test.tsx`, `StagesList.test.tsx` (adding the now-required `autoApprove` field)

**Interfaces:**
- Consumes: `stage_auto_approve` JSON field (Task 6).
- Produces: `Stage.autoApprove: boolean` — a required field on the `Stage` type, so every existing `Stage` object literal in tests must be updated (mechanical, see Step 6).

- [ ] **Step 1: Write the failing PlanPanel tests**

In `pkg/web/dashboard/src/components/plan-panel/PlanPanel.test.tsx`, update the `makeStage` helper and add two tests:

```tsx
// BEFORE:
function makeStage(overrides: Partial<Stage> = {}): Stage {
  return { id: 's1', name: 'Stage', status: 'running', updatedAt: '', interactive: false, autonomous: false, ...overrides }
}

// AFTER:
function makeStage(overrides: Partial<Stage> = {}): Stage {
  return {
    id: 's1', name: 'Stage', status: 'running', updatedAt: '',
    interactive: false, autonomous: false, autoApprove: false, ...overrides,
  }
}
```

Add these two tests at the end of the `describe('PlanPanel', ...)` block, right before the closing `})`:

```tsx
  test('autoApprove: true hides Approve/Revise and shows an Auto-approved badge instead', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse('# Plan\n\nSome content'))

    const { container } = render(<PlanPanel stage={makeStage({ status: 'awaiting_approval', autoApprove: true })} />)

    await waitFor(() => expect(container.querySelector('.auto-approved-badge')).not.toBeNull())
    expect(container.querySelector('#actions-section')).toBeNull()
    expect(screen.queryByRole('button', { name: 'Approve' })).not.toBeInTheDocument()
  })

  test('autoApprove: false keeps the normal Approve/Revise actions (no badge)', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse('# Plan'))

    const { container } = render(<PlanPanel stage={makeStage({ status: 'awaiting_approval', autoApprove: false })} />)

    await waitFor(() => expect(container.querySelector('#actions-section')).not.toBeNull())
    expect(container.querySelector('.auto-approved-badge')).toBeNull()
  })
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd pkg/web/dashboard && npx vitest run src/components/plan-panel/PlanPanel.test.tsx`
Expected: FAIL — TypeScript error (`autoApprove` does not exist on type `Stage`) and/or the new assertions fail since nothing renders `.auto-approved-badge` yet.

- [ ] **Step 3: Add `autoApprove` to the `Stage` type**

```ts
// BEFORE (pkg/web/dashboard/src/types/stage.ts):
export type Stage = {
  id: string
  name: string
  status: StageStatus
  updatedAt: string
  interactive: boolean
  autonomous: boolean
}

// AFTER:
export type Stage = {
  id: string
  name: string
  status: StageStatus
  updatedAt: string
  interactive: boolean
  autonomous: boolean
  autoApprove: boolean
}
```

- [ ] **Step 4: Parse `stage_auto_approve` in `use-status.ts`**

```ts
// BEFORE (normalizeStatus):
  const stagesObj = isRecord(obj.stages) ? obj.stages : {}
  const namesObj = isRecord(obj.stage_names) ? obj.stage_names : {}
  const interactiveObj = isRecord(obj.stage_interactive) ? obj.stage_interactive : {}
  const autonomousObj = isRecord(obj.stage_autonomous) ? obj.stage_autonomous : {}

  const order = resolveOrder(obj.stage_order, stagesObj)

  const stages: Stage[] = order.map((id) =>
    toStage(id, stagesObj[id], namesObj[id], interactiveObj[id] === true, autonomousObj[id] === true),
  )

// AFTER:
  const stagesObj = isRecord(obj.stages) ? obj.stages : {}
  const namesObj = isRecord(obj.stage_names) ? obj.stage_names : {}
  const interactiveObj = isRecord(obj.stage_interactive) ? obj.stage_interactive : {}
  const autonomousObj = isRecord(obj.stage_autonomous) ? obj.stage_autonomous : {}
  const autoApproveObj = isRecord(obj.stage_auto_approve) ? obj.stage_auto_approve : {}

  const order = resolveOrder(obj.stage_order, stagesObj)

  const stages: Stage[] = order.map((id) =>
    toStage(id, stagesObj[id], namesObj[id], interactiveObj[id] === true, autonomousObj[id] === true, autoApproveObj[id] === true),
  )
```

```ts
// BEFORE (toStage):
function toStage(
  id: string,
  raw: unknown,
  nameRaw: unknown,
  interactive: boolean,
  autonomous: boolean,
): Stage {
  const obj = isRecord(raw) ? raw : {}

  const status: StageStatus = isStageStatus(obj.status) ? obj.status : 'pending'
  const updatedAt = typeof obj.updated_at === 'string' ? obj.updated_at : ''
  const name = typeof nameRaw === 'string' ? nameRaw : ''

  return { id, name, status, updatedAt, interactive, autonomous }
}

// AFTER:
function toStage(
  id: string,
  raw: unknown,
  nameRaw: unknown,
  interactive: boolean,
  autonomous: boolean,
  autoApprove: boolean,
): Stage {
  const obj = isRecord(raw) ? raw : {}

  const status: StageStatus = isStageStatus(obj.status) ? obj.status : 'pending'
  const updatedAt = typeof obj.updated_at === 'string' ? obj.updated_at : ''
  const name = typeof nameRaw === 'string' ? nameRaw : ''

  return { id, name, status, updatedAt, interactive, autonomous, autoApprove }
}
```

- [ ] **Step 5: Update `PlanPanel.tsx` to gate actions and render the badge**

```tsx
// BEFORE:
  const isReview = stage.status === 'awaiting_approval'
  const showActions = isReview
  const showRetry = stage.status === 'failed'
  const showHookFailed = stage.status === 'hook_failed'

// AFTER:
  const isReview = stage.status === 'awaiting_approval'
  const showActions = isReview && !stage.autoApprove
  const showAutoApprovedBadge = stage.autoApprove && planMarkdown.trim() !== ''
  const showRetry = stage.status === 'failed'
  const showHookFailed = stage.status === 'hook_failed'
```

Add the badge markup right after the `{showActions && (...)}` block (before `{showRetry && (...)}`):

```tsx
        {showAutoApprovedBadge && (
          <div id="auto-approved-section" className="section">
            <span className="auto-approved-badge">Auto-approved</span>
          </div>
        )}

        {showRetry && (
```

- [ ] **Step 6: Add the badge style to `plan-panel.css`**

```css
/* Add near .actions-row: */
.auto-approved-badge {
  display: inline-block;
  padding: 4px 10px;
  border-radius: 999px;
  font-size: 12px;
  font-weight: 600;
  color: var(--c-done);
  border: 1px solid var(--c-done);
}
```

- [ ] **Step 7: Run PlanPanel tests to verify they pass**

Run: `cd pkg/web/dashboard && npx vitest run src/components/plan-panel/PlanPanel.test.tsx`
Expected: PASS (all tests in the file, including the two new ones).

- [ ] **Step 8: Fix up the other three test files whose `Stage` literals now need `autoApprove`**

In `pkg/web/dashboard/src/components/footer/Footer.test.tsx` (2 occurrences):

```tsx
// BEFORE:
      { id: 's1', name: '', status: 'done', updatedAt: '', interactive: false, autonomous: false },
      { id: 's2', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false },

// AFTER:
      { id: 's1', name: '', status: 'done', updatedAt: '', interactive: false, autonomous: false, autoApprove: false },
      { id: 's2', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false },
```

In `pkg/web/dashboard/src/components/dialog-channel/DialogChannel.test.tsx` (1 occurrence, a helper factory):

```tsx
// BEFORE:
  return { id: 's1', name: 'Stage', status: 'awaiting_user_input', updatedAt: '', interactive: true, autonomous: false, ...overrides }

// AFTER:
  return { id: 's1', name: 'Stage', status: 'awaiting_user_input', updatedAt: '', interactive: true, autonomous: false, autoApprove: false, ...overrides }
```

In `pkg/web/dashboard/src/components/stages-list/StagesList.test.tsx` (14 occurrences, all of the form `interactive: <bool>, autonomous: <bool> }`): append `, autoApprove: false` right before the closing `}` on every `Stage` literal. Since every occurrence in this file ends in `autonomous: false }` or `autonomous: true }`, this is a mechanical find/replace:

```
autonomous: false }   →   autonomous: false, autoApprove: false }
autonomous: true }    →   autonomous: true, autoApprove: false }
```

- [ ] **Step 9: Run the full frontend test suite**

Run: `cd pkg/web/dashboard && npm run build && npx vitest run`
Expected: PASS across all files — `Footer.test.tsx`, `DialogChannel.test.tsx`, `StagesList.test.tsx`, `PlanPanel.test.tsx`, `App.test.tsx`, and any others touching `Stage` literals.

Run: `npx tsc --noEmit`
Expected: no errors (confirms every `Stage` literal across the codebase — not just the four files above — got the new required field; if this catches an additional file, fix it the same mechanical way before proceeding).

- [ ] **Step 10: Commit**

```bash
git add pkg/web/dashboard/src pkg/web/dashboard/skins/base/plan-panel.css
git commit -m "$(cat <<'EOF'
feat(dashboard): бейдж Auto-approved вместо кнопок для auto_approve-стадий
EOF
)"
```

---

## Task 8: `auto_approve` — docs

**Files:**
- Modify: `README.md` (stage fields table, new subsection)

**Interfaces:** none (doc-only, no dependents).

- [ ] **Step 1: Add `auto_approve` to the stage fields table**

```md
<!-- BEFORE (README.md, in the stage fields table, right after the `interactive` row): -->
| `interactive` | no | `true` — enables the file-based dialog protocol with the user via the dashboard (see below) |
| `supervisor` | no | `true` — allow the supervisor to evaluate the stage and possibly move it to the autonomous track (requires `supervisor_command`) |

<!-- AFTER: -->
| `interactive` | no | `true` — enables the file-based dialog protocol with the user via the dashboard (see below) |
| `auto_approve` | no | `true` — approve this stage's plan automatically the instant it's ready, with no human interaction — regardless of a dashboard being attached or `--require-approval`. Default `false`. Intended for CI (see "Auto-Approving a Stage's Plan" below) |
| `supervisor` | no | `true` — allow the supervisor to evaluate the stage and possibly move it to the autonomous track (requires `supervisor_command`) |
```

- [ ] **Step 2: Add a new subsection after "Suggesting a Note to a Running Stage"**

```md
<!-- Insert after the "Suggesting a Note to a Running Stage" section (right before "### Resume on Restart"): -->

### Auto-Approving a Stage's Plan

Set `auto_approve: true` on a stage to skip the human approval checkpoint entirely — useful for CI runs where some stages need review and others don't:

```yaml
stages:
  - id: lint
    agents: [planning, implementation]
    auto_approve: true    # no human ever needs to click Approve for this stage
  - id: deploy
    agents: [planning, implementation]
    depends_on: [lint]    # still requires a human Approve — auto_approve not set
```

The plan is approved the instant it's ready, whether or not a dashboard is attached and regardless of `--require-approval` (which normally fails a headless run with no dashboard). If the dashboard is open, the stage's plan is still shown, with an "Auto-approved" badge in place of the Approve/Revise buttons.
```

- [ ] **Step 3: Verify the README renders sanely**

Run: `grep -n "auto_approve" README.md`
Expected: three hits — the stage fields table row, the YAML example, and the new subsection body.

- [ ] **Step 4: Commit**

```bash
git add README.md
git commit -m "$(cat <<'EOF'
docs: документируем стейдж-флаг auto_approve
EOF
)"
```
