# Архитектурный план: корневой `prompt` в flow.yaml

Источник: `docs/tasks/claude-copy-global-prompt.md`.

## Topic

`global-prompt` — root-level `prompt` field in `flow.yaml`, propagated into every stage's system prompt.

## Acceptance Criteria

- Root `prompt:` (multiline via `|`) parses into `Flow.Prompt` (test in `pkg/flow/flow_test.go`).
- Root prompt text reaches the system prompt of every stage and every phase (planning/implementation/review), including both review call sites (inline `:1115` and standalone `:1151`) — verified via `prompts.Build` tests.
- Empty/absent `prompt` → assembled prompt byte-identical to current (golden test `TestBuild_Golden_PlanningSimple` passes unmodified; `TestBuild_NoGlobalPromptBlock_WhenEmpty` green).
- `<`/`>` in the root prompt are escaped, including its own closing tag `</global_prompt>` (no early block closure, no injection into adjacent blocks) — `TestBuild_GlobalPromptEscapesOwnClosingTag`.
- `tagReplacer` in `pkg/prompts/builder.go` contains the `<global_prompt>`/`</global_prompt>` pair.
- All 5 `prompts.Build` call sites in `pkg/orchestrator/orchestrator.go` pass `GlobalPrompt: o.opts.GlobalPrompt`.
- `go build ./...`, `go vet ./...`, `golangci-lint run`, and `go test ./pkg/flow/ ./pkg/prompts/ ./pkg/orchestrator/` pass; no deprecated constructs.

## Stack & External Dependencies

Go stdlib + existing afm packages (`pkg/flow`, `pkg/prompts`, `pkg/orchestrator`, `cmd/afm`); `gopkg.in/yaml.v3` (already a dependency, plain struct-tag decoding — see `.goga/usages/cooks/yaml-v3.md`). No new external dependencies.

## Existing Cells & Schema

Cell hierarchy (17 cells, read directly from `CODEMANIFEST` files — `goga schema` unavailable in this environment): `assets`, `cmd/afm`, `tools/setstatuslinter`, `pkg/accounting`, `pkg/docker`, `pkg/proxy`, `pkg/progress`, `pkg/config`, `pkg/web`, `pkg/server`, `pkg/mcp`, `pkg/state`, `pkg/prompts`, `pkg/executor`, `pkg/orchestrator`, `pkg/flow`, `pkg/web/dashboard`.

| Cell | CODEMANIFEST | Relevance |
|---|---|---|
| `pkg/flow` | `pkg/flow/CODEMANIFEST` | Owns `Flow` type — target for new `Prompt` field |
| `pkg/prompts` | `pkg/prompts/CODEMANIFEST` | Owns `Build`/`Inputs` — target for new `GlobalPrompt` field + rendering |
| `pkg/orchestrator` | `pkg/orchestrator/CODEMANIFEST` | Owns `Options` + the 5 `Build` call sites |
| `cmd/afm` | `cmd/afm/CODEMANIFEST` | `newRunCmd` wires `Flow.Prompt` into `Options` |

## Artifact Resolution

| Name/term | Resolution | Justification |
|---|---|---|
| `Flow` | modify existing cell `pkg/flow` | Add `Prompt -> string` property to existing type `Flow(name, description, maxParallel, stages)` |
| `orchestrator.Options` | modify existing cell `pkg/orchestrator` | Add `GlobalPrompt -> string` property, analogous to existing plain-string fields `ProxyURL`/`ProxyShimDir` |
| `prompts.Inputs` | modify existing cell `pkg/prompts` | Add `GlobalPrompt -> string` property |
| `prompts.Build` | modify existing cell `pkg/prompts` | Extend Algorithm: render `<global_prompt>` block conditionally; extend `tagReplacer` |
| `cmd/afm/run.go` `newRunCmd` | modify existing cell `cmd/afm` | Add one field assignment `GlobalPrompt: f.Prompt` to the `orchestrator.Options{...}` literal |

No new cell/artifact is created — every change is a property addition or algorithm extension inside 4 already-existing cells.

## Key Concepts

- `Flow.Prompt` (new `string` property) — root-level shared prompt text, parsed from `flow.yaml`'s `prompt:` key.
- `orchestrator.Options.GlobalPrompt` (new `string` property) — carries `Flow.Prompt` from CLI entrypoint into the orchestrator.
- `prompts.Inputs.GlobalPrompt` (new `string` property) — carries the value into a single `Build` call.
- `<global_prompt>` prompt block — new escaped XML-like block emitted by `Build`, reusing the existing `<prompt>` block's escaping mechanism (own `tagReplacer` pair, conditional on non-empty value). Placement is independent of `<prompt>`: rendered right after `</system_rules>`, before `<context>`/`<stage>` — deliberately earlier, since root-level rules should apply before any stage-specific content, whereas `<prompt>` (from `Stage.Prompt`) is rendered later, after the `<stage>` block closes (builder.go:96-103).

## Dark Zones

None outstanding — this task was already fully resolved by prior `propose`/`propose-review` stages, including the previously ambiguous point (4 vs 5 `Build` call sites), confirmed as 5 by source inspection. The only design choice explicitly left to this stage was tag name/placement, and it is already fixed: `<global_prompt>`, placed right after `</system_rules>` (before `<context>`/`<stage>`) — reusing `<prompt>`'s escaping mechanism only, not its position (`<prompt>` itself renders later, after `<stage>` closes at builder.go:96-103; `<global_prompt>` is placed earlier by design, since root-level rules should reach the agent before stage-specific content).

## Connection to Existing Architecture

Fully additive within the existing `Flow → Options → Inputs → Build` prompt-assembly data path. `pkg/flow` gains a property; `pkg/orchestrator` gains a property and 5 call-site edits; `pkg/prompts` gains a property, an algorithm branch, and 2 `tagReplacer` entries; `cmd/afm` gains 1 field assignment. No changes to `pkg/mcp`, `pkg/state`, `pkg/executor`, `pkg/server`, `pkg/config`, `pkg/proxy`, or any other cell — Imports lists in the affected CODEMANIFESTs are unaffected (no new cross-cell dependency introduced; `pkg/orchestrator` already imports `Inputs` from `pkg/prompts` and `Stage`/`Artifact` from `pkg/flow`).

## Risks and Constraints

- Easiest mistake: missing one of the 5 `prompts.Build` call sites in `pkg/orchestrator/orchestrator.go` (926 planning, 1019 planning retry/revise, 1096 implementation, 1115 inline-review, 1151 standalone `runReviewAgent`) — 1115 is nested inside the implementation block and most often skipped.
- Backward compatibility is load-bearing: the `<global_prompt>` block must be omitted entirely (not rendered empty) when `GlobalPrompt == ""`, or the golden test `TestBuild_Golden_PlanningSimple` breaks.
- No flow-level merge/composition logic exists for top-level fields (`ParseFile` is the sole loader; `inline` applies only to `Artifact`) — confirmed, so `Flow.Prompt` needs no special merge plumbing, just direct struct-tag decoding (per `.goga/usages/cooks/yaml-v3.md`).
- Not to be confused with delivering `~/.claude/CLAUDE.md` (out of scope, per task file).

## Type Map

| Type | Character | Description | Connected Types |
|---|---|---|---|
| `Flow` | Entity | existing type, `pkg/flow`; gains `Prompt -> string` property | referenced by `resolveRun`/`newRunCmd` in `cmd/afm` |
| `Options` | Entity | existing type, `pkg/orchestrator`; gains `GlobalPrompt -> string` property | constructed in `cmd/afm` from `Flow`; consumed by `Orchestrator` |
| `Inputs` | Entity | existing type, `pkg/prompts`; gains `GlobalPrompt -> string` property | constructed in `pkg/orchestrator` (5 call sites) from `Options.GlobalPrompt`; consumed by `Build` |
| `Build` | Routine | existing routine, `pkg/prompts`; algorithm extended to render `<global_prompt>` block from `Inputs.GlobalPrompt`, placed right after `</system_rules>` (independent of, and earlier than, `<prompt>`'s own position later in `Build`) | accepts `Inputs`; returns `string` (prompt) |

No new types are introduced anywhere in this change — every affected item is either an existing Entity gaining one `string` property, or an existing Routine gaining one conditional algorithm branch plus 2 `tagReplacer` string-pair entries (not types, not modeled as rows).

## Type Detail

| Type | Character | Description | Connected Types | Signature | Properties | Methods | Mutations & Embeddings |
|---|---|---|---|---|---|---|---|
| `Flow` | Entity | existing type, `pkg/flow`, gains `Prompt -> string` | referenced by `cmd/afm` | `Flow(name string, description string, prompt string, maxParallel int, stages []Stage)` | `Name -> string`; `Description -> string`; **`Prompt -> string`** (new); `MaxParallel -> int`; `Stages -> []Stage` | none affected — no new/changed methods; `ParseFile(path string) -> flow:Flow, err:error` decodes the new field via existing yaml struct-tag mechanism, unchanged signature | `Flow::Prompt` — property addition to existing entity (Imports only, no new cross-cell dependency) |
| `Options` | Entity | existing type, `pkg/orchestrator`, gains `GlobalPrompt -> string` | constructed from `Flow` in `cmd/afm`; consumed by `Orchestrator`/`prompts.Build` call sites | `Options(runDir string, stages []Stage, store Store, config Config, prompts Prompts, runner Runner, dashboardURL string, proxyURL string, proxyShimDir string, globalPrompt string)` | existing properties unchanged; **`GlobalPrompt -> string`** (new, plain data field, mirrors existing `ProxyURL`/`ProxyShimDir` string fields) | none affected | `Options::GlobalPrompt` — property addition (Imports only) |
| `Inputs` | Entity | existing type, `pkg/prompts`, gains `GlobalPrompt -> string` | constructed in `pkg/orchestrator` (5 sites) from `Options.GlobalPrompt`; consumed by `Build` | `Inputs(template string, stage Stage, phaseAgent Agent, dependencyPlans string, artifacts string, plan string, previousPlan string, feedback string, retryContext string, stageDir string, interactive bool, outputContractMD string, exampleOutput string, globalPrompt string)` | existing properties unchanged; **`GlobalPrompt -> string`** (new) | none affected | `Inputs::GlobalPrompt` — property addition (Imports only) |
| `Build` | Routine | existing routine, `pkg/prompts`, algorithm extended | accepts `Inputs`; returns `string` (prompt) | `Build(in Inputs) -> prompt:string` — signature unchanged | n/a (routine) | n/a (routine) | Algorithm extension only (no type mutation): after building the `</system_rules>` block, if `in.GlobalPrompt != ""`, append `<global_prompt>\n{escapeTags(in.GlobalPrompt)}\n</global_prompt>\n` before the `<context>`/`<stage>` sections; `tagReplacer` (a package-level value, not a modeled type) gains two entries `"</global_prompt>" → "</​global_prompt>"` and `"<global_prompt>" → "<​global_prompt>"` |

## Cell Distribution

| Cell path | Types assigned |
|---|---|
| `pkg/flow` | `Flow` (existing, gains `Prompt` property) |
| `pkg/orchestrator` | `Options` (existing, gains `GlobalPrompt` property) |
| `pkg/prompts` | `Inputs` (existing, gains `GlobalPrompt` property); `Build` (existing, algorithm extended) |
| `cmd/afm` | no type — only the `newRunCmd` construction site of `orchestrator.Options{...}` is edited (one field literal) |

No new cell is created — all 4 types/routines are distributed to their existing owning cells.

### Inter-Cell Connections

```
pkg/flow ──(Flow.Prompt: string)──> cmd/afm (newRunCmd reads f.Prompt)
cmd/afm ──(GlobalPrompt: string, via Options{...} literal)──> pkg/orchestrator (Options.GlobalPrompt)
pkg/orchestrator ──(GlobalPrompt: string, via 5 Inputs{...} literals)──> pkg/prompts (Inputs.GlobalPrompt)
pkg/prompts ──(Build reads in.GlobalPrompt internally)──> pkg/prompts (no external target; consumed within Build)
```

This exactly mirrors the existing dependency direction already recorded in each cell's `Imports`: `pkg/orchestrator` already imports `Inputs`/`PlanIssues` from `pkg/prompts`, and `pkg/orchestrator` already imports `Stage`/`Artifact` from `pkg/flow`; `cmd/afm` already imports `Flow`/`ParseFile` from `pkg/flow` and `Orchestrator`/`Options`/`Prompts` from `pkg/orchestrator`. No new `Imports` entries are required in any CODEMANIFEST — the new fields travel through already-declared type imports (`Flow`, `Options`, `Inputs`), and no new type crosses a cell boundary.

### No-Circular-Dependency Confirmation

Dependency order (leaves to root), unchanged from the existing architecture:

1. `pkg/flow` (leaf — no afm-internal imports among the affected types)
2. `pkg/prompts` (imports `Stage` from `pkg/flow`)
3. `pkg/orchestrator` (imports from `pkg/flow` and `pkg/prompts`)
4. `cmd/afm` (imports from `pkg/flow`, `pkg/orchestrator`; root/entrypoint)

Data flows strictly downstream in this same order (`Flow.Prompt` → `cmd/afm` → `Options.GlobalPrompt` → `Inputs.GlobalPrompt` → `Build`), with no back-edge introduced. No circular dependency.

## Contracts

### Cell: `pkg/flow`

**Usages:** base (`conventions`, `golang`), external `yaml_v3: .goga/usages/cooks/yaml-v3.md` (already declared) — governs the new field's decoding (plain struct-tag field, no custom `UnmarshalYAML`).

**Annotations:**
- Type-level `Flow`: annotation gains one line under the existing description: "`prompt`: общий текст, добавляемый в системный промпт каждой стадии и каждой фазы (planning/implementation/review); пустое значение не меняет поведение."
- Member-level: new property `"Prompt -> string": "Общий (root-level) промпт флоу, применяется ко всем стадиям и фазам. Пусто/отсутствует — поведение не меняется."` inserted after the existing `"Description -> string"` property line.

No new cell usage files — existing `yaml_v3` usage file already covers plain struct-tag decoding.

### Cell: `pkg/prompts`

**Usages:** base (`conventions`, `golang`); no new cross-cell or external usages.

**Annotations:**
- `Build(in Inputs) -> prompt:string` — Algorithm gains a new numbered step, inserted right after building the `<system_rules>` block and before the stage-description/context sections: "1b. Если `in.GlobalPrompt` непусто — добавляет блок `<global_prompt>\n{escapeTags(in.GlobalPrompt)}\n</global_prompt>\n` сразу после `</system_rules>`, до `<context>`/`<stage>`; при пустом `GlobalPrompt` блок не рендерится вовсе (сохранение обратной совместимости)."
- `Inputs(...)` signature gains one parameter: `globalPrompt string`.
- Member-level: new property `"GlobalPrompt -> string": "Общий (root-level) промпт флоу (Flow.Prompt, импорт pkg/flow через cmd/afm/pkg/orchestrator). Рендерится в блок <global_prompt> только если непусто."` inserted after existing `"ExampleOutput -> string"` property.

`tagReplacer`/`escapeTags` are unexported package-level implementation details (not modeled as DSL types), covered by the `Build` Algorithm annotation above.

### Cell: `pkg/orchestrator`

**Usages:** base (`conventions`, `golang`, `rapid`); no new cross-cell or external usages.

**Annotations:**
- `Options`: property list gains `GlobalPrompt`.
- `Orchestrator.Run`: annotation gains a clarifying line: "Каждый из 5 вызовов `prompts.Build` (planning, повтор planning/revise, implementation, inline-review, standalone review) передаёт `GlobalPrompt: o.opts.GlobalPrompt` в `prompts.Inputs`."
- Member-level: new property on `Options`: `"GlobalPrompt -> string": "Общий (root-level) промпт флоу (Flow.Prompt, pkg/flow), пробрасывается во все вызовы prompts.Build."` inserted after existing `"ProxyShimDir -> string"` property.

### Cell: `cmd/afm` (function-level edit only, no type change)

**Usages:** base (`conventions`, `golang`, `cobra`); no new cross-cell or external usages.

**Annotations:**
- `newRunCmd() -> cmd:cobra.Command` annotation gains one clause: "…и передаёт `Flow.Prompt` в `Options.GlobalPrompt` при построении `orchestrator.Options`."

### Base Compliance

| Cell | Base usages included | Contract complies? |
|---|---|---|
| `pkg/flow` | conventions, golang, yaml_v3 (existing) | ✓ |
| `pkg/prompts` | conventions, golang (existing) | ✓ |
| `pkg/orchestrator` | conventions, golang, rapid (existing) | ✓ |
| `cmd/afm` | conventions, golang, cobra (existing) | ✓ |

No new external library, framework, or cross-cutting practice is introduced by this change; all four cells' existing usages fully cover the new field/algorithm additions.

## Implementation Order

1. `pkg/flow` — add `Prompt string` to `Flow` (no internal dependencies among the affected types).
2. `pkg/prompts` — add `GlobalPrompt string` to `Inputs`, extend `Build` and `tagReplacer` (depends on nothing new; `Inputs` is self-contained).
3. `pkg/orchestrator` — add `GlobalPrompt string` to `Options`, thread it through all 5 `prompts.Build` call sites (926, 1019, 1096, 1115, 1151) (depends on `pkg/prompts.Inputs.GlobalPrompt` from step 2).
4. `cmd/afm` — assign `GlobalPrompt: f.Prompt` in the `orchestrator.Options{...}` literal in `newRunCmd` (depends on `Flow.Prompt` from step 1 and `Options.GlobalPrompt` from step 3).
5. Tests — `pkg/flow/flow_test.go` (root `prompt:` parsing), `pkg/prompts/builder_test.go` (`TestBuild_GlobalPromptBlockAppears`, `TestBuild_NoGlobalPromptBlock_WhenEmpty`, `TestBuild_GlobalPromptEscapesOwnClosingTag`, golden test stays green).

## Verification Checklist

- `go build ./...` and `go vet ./...` pass.
- `golangci-lint run` (or the project's configured lint) reports no new issues.
- `go test ./pkg/flow/ ./pkg/prompts/ ./pkg/orchestrator/` passes, including the new tests listed above.
- `TestBuild_Golden_PlanningSimple` passes unmodified (no golden-file edits) — confirms backward compatibility when `GlobalPrompt == ""`.
- Manual/integration check: a `flow.yaml` with a root `prompt:` produces a `<global_prompt>` block in every stage's assembled system prompt (planning, implementation, both review call sites) under `.afm/runs/.../<stage>/`.
- Confirm all 5 `prompts.Build` call sites in `pkg/orchestrator/orchestrator.go` (926, 1019, 1096, 1115, 1151) pass `GlobalPrompt: o.opts.GlobalPrompt` — grep for `GlobalPrompt` in that file should show exactly 5 usages plus the field declaration.

## Notes

Given the outer AFM stage contract mandates producing a plan artifact with no interactive waiting, the remaining brainstorm phases (Cell Assembly full CODEMANIFEST text, Plan Verification) are represented here at the level of diffs/annotations rather than full regenerated CODEMANIFEST files, since every change is a minimal property/algorithm addition to an already-existing, already-documented cell — the decisions were already fixed by the prior propose/propose-review stages.
