# Agent memory: configuration and injection

> Baseline: afm `462bd5d4` · afm-engine `v1.2.1` (`11bddf2c`). Status: draft baseline.

## Scope
- The `memory:` flow block and per-stage `reflect` / `memory_use` fields: defaults, validation, memory-dir resolution (`ResolveMemoryDir`).
- Reading: which memory files a stage agent is pointed at, and when new rules become visible.
- The agent-memory domain is split into: configuration and injection (`../agent-memory/spec.md`), live run path (`../agent-memory-live/spec.md`), shared pipeline (`../agent-memory-pipeline/spec.md`) and offline rebuild (`../agent-memory-rebuild/spec.md`). Prefix `MEM` is shared; IDs are unique across them.
- Out of scope: general flow parsing (`../flow-config/spec.md`); budget rules (`../budgets-accounting/spec.md`).

## Terms
- **memory dir** — resolved directory of `memory.path`; holds `memory.md` (project file) and
  stage `reflect.file` targets.
- **dataset** — `<runDir>/<stageID>/reflect_dataset.yaml`, YAML output of the reflect agent.
- **capture** — reflect step producing a dataset. **finalize** — aggregate → prioritize →
  select High → update → publish.
- **target** — a rules file being finalized: a `reflect.file` group or `memory.md`.
- **memory lock** — interprocess lock keyed by canonical memory dir.

## Requirements

### MEM-R01 Opt-in and defaults
Agent memory is enabled only by a non-empty `memory.path`. When enabled, parsing defaults
`memory.mode` to `rw`, `memory.max_rules` to `25` and every declared `reflect.mode` to `rw`;
`memory_use` defaults to `false`. Memory settings present without `memory.path` produce a stderr
`WARN` and are ignored.

Evidence: afm:contracts/flow/memory_v3_flags_test.go `TestParseMemoryV3_ModeAndUseDefaults`,
`TestValidateMemoryV3_ZeroMaxRulesDefaults`, `TestParseMemoryV3_SettingsWithoutPathWarn`,
`TestParseMemoryV3_NoSpuriousWarnWhenPathSet`; afm:contracts/flow/flow.go `ParseFile`,
`MemoryEnabled`, `warnMemorySettingsWithoutPath`; afm:docs/agent-memory.md

#### MEM-S01 Settings without path
- GIVEN a flow with `memory: {memory_use: true}` and no `memory.path`
- WHEN the flow is parsed
- THEN a `WARN ... memory.memory_use set without memory.path` line goes to stderr and memory stays disabled

### MEM-R02 Memory validation
Validation rejects an unknown `memory.mode` or `reflect.mode` (allowed: `r`, `w`, `rw`), a
negative `max_rules`, `reflect` without `memory.path`, an empty `reflect.file`, a non-local
`reflect.file` (absolute or escaping with `..`), and a `reflect.file` that cleans to `memory.md`.

Evidence: afm:contracts/flow/memory_v3_flags_test.go `TestValidateMemoryV3_BadGlobalMode`,
`TestValidateMemoryV3_NegativeMaxRules`; afm:contracts/flow/flow_test.go
`TestValidateMemory_ReflectRequiresPath`; afm:contracts/flow/validate_pathsafety_test.go
`TestValidate_ReflectFilePathSafety`; afm:contracts/flow/flow.go `validate`

#### MEM-S02 Reflect file collides with project file
- GIVEN a stage with `reflect: {file: ./memory.md}`
- WHEN the flow is validated
- THEN validation fails with `reflect.file must not be memory.md`

### MEM-R03 Memory dir resolution
`ResolveMemoryDir` returns `""` when memory is disabled; an absolute `memory.path` is used as is;
a relative one is joined to the agent root (`root_dir` resolved against afm-root), or to afm-root
when the agent root is empty. `afm run` and rebuild share this resolver.

Evidence: engine:internal/engine/agent_environment_test.go `TestResolveMemoryDir_MatchesRunInlineLogic`;
engine:internal/engine/agent_environment.go `ResolveMemoryDir`; engine:internal/engine/environment.go

### MEM-R04 Read participation gate
A stage reads memory only when its effective `memory_use` is true: the stage's own `memory_use`
if set, else flow-level `memory.memory_use`. Writing (capture) does not depend on `memory_use`.

Evidence: afm:contracts/flow/memory_v3_flags_test.go `TestMemoryConfig_UseFor`,
`TestParseMemoryV3_StageMemoryUseOverride`; engine:internal/orchestrator/memory_inject_test.go
`TestMemoryBlockForStage_ParticipationOff`, `TestMemoryBlockForStage_StageOverrideOptsIn`;
engine:internal/orchestrator/memory_inject.go `memoryBlockForStage`

#### MEM-S03 Stage opts in against flow default
- GIVEN `memory.memory_use: false` and a stage with `memory_use: true` and an existing `memory.md`
- WHEN the stage prompt is built
- THEN the prompt contains the memory pointer block naming `memory.md`

### MEM-R05 Pointer injection, not content
For a participating stage the prompt gets a pointer block listing existing files only:
`memory.md` when `memory.mode` allows read, and the stage's `reflect.file` when its `reflect.mode`
allows read. File contents are never pasted. With no qualifying existing file the block is empty.

Evidence: engine:internal/orchestrator/memory_inject_test.go
`TestMemoryBlockForStage_ProjectFileNamedForEveryStage`, `TestMemoryBlockForStage_ReadableStageFileNamed`,
`TestMemoryBlockForStage_WriteOnlyModeDoesNotNameStageFile`, `TestMemoryBlockForStage_NonexistentStageFileNotNamed`,
`TestMemoryBlockForStage_GlobalModeWriteOnlyHidesProject`, `TestMemoryBlockForStage_NoFilesReturnsEmpty`;
engine:internal/orchestrator/memory_inject.go `memoryPointerBlock`

#### MEM-S04 Write-only project file hidden
- GIVEN `memory.mode: w` and an existing `memory.md`
- WHEN a participating stage prompt is built
- THEN `memory.md` is not named

### MEM-R06 No same-run propagation
Rules produced in a run become visible only after end-of-run finalize; a dataset captured earlier
in the same run is never injected into downstream prompts.

Evidence: engine:internal/orchestrator/memory_integration_test.go
`TestIntegration_MemoryV3_PointerReachesLaterStagePrompt`; engine:internal/memorypipeline/AGENTS.md

## Errors and rejections
| ID | Condition | Observable result | Evidence |
|----|-----------|-------------------|----------|
| MEM-E01 | `reflect` without `memory.path` | validation error `stage "<id>": reflect requires memory.path` | afm:contracts/flow/flow_test.go `TestValidateMemory_ReflectRequiresPath` |
| MEM-E02 | `reflect.file` absolute/`..`/`memory.md` | validation error | afm:contracts/flow/validate_pathsafety_test.go `TestValidate_ReflectFilePathSafety` |
| MEM-E03 | bad `memory.mode` / negative `max_rules` | `memory.mode must be r, w, or rw` / `memory.max_rules must be >= 0` | afm:contracts/flow/memory_v3_flags_test.go `TestValidateMemoryV3_BadGlobalMode`, `TestValidateMemoryV3_NegativeMaxRules` |

## Coverage gaps and discrepancies
- None beyond the sibling files; the `memory.path` resolution difference between `afm run` and rebuild is recorded as MEM-G30 in `../agent-memory-rebuild/spec.md`.
