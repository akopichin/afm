# Flow file contract

> Baseline: afm `462bd5d4` · afm-engine `v1.2.1` (`11bddf2c`). Status: draft baseline.

## Scope
- How `flow.yaml` is parsed and validated (`afm:contracts/flow` `ParseFile`) before the engine opens a run.
- Stage-ID rules for static flows, runtime-added stages (`afm:contracts/graph`) and engine commands (`ValidStageID`).
- Out of scope: `config.yaml`, helper-profile preflight, `afm validate`, JSON Schemas and `jsonstrict` — [../flow-config-settings/spec.md](../flow-config-settings/spec.md) (FLOW-R10..R16, R19, R20); pipeline packages — [../flow-config-pipelines/spec.md](../flow-config-pipelines/spec.md) (FLOW-R40+); budget semantics — [../budgets-accounting/spec.md](../budgets-accounting/spec.md); memory behaviour — [../agent-memory/spec.md](../agent-memory/spec.md).

## Terms
- **Execution stage**: a stage with an implementation/review/auto agent (not planning-only).
- **Runtime-added stage**: a stage inserted or appended during a run through the engine graph (dashboard, graph API, agent proposals).

## Requirements

### FLOW-R01 Flow parsing is lenient at the top and stage level
`ParseFile` decodes YAML with yaml.v3 without `KnownFields`, so unknown keys at the flow root and inside a stage are silently ignored. Removed supervisor keys (`supervisor_command`, `supervisor`, `supervisor_prompt`) are accepted and produce a non-fatal `WARN:` on stderr naming the stage.

Evidence: `afm:contracts/flow/flow_test.go` `TestParseFile_WarnsOnDeprecatedSupervisorKeys`; `afm:contracts/flow/flow.go` `ParseFile`, `warnDeprecatedSupervisorFields`

#### FLOW-S01 Deprecated supervisor key
- GIVEN a stage with `supervisor: true`
- WHEN the flow is parsed
- THEN parsing succeeds and stderr gets `WARN: … stage "s1": "supervisor" is no longer supported and is ignored …`

### FLOW-R02 Nested objects with custom decoders are strict
`repeat`, `collaboration`, `team` members, stage `assistants` items and their `loop`, and `verify` steps reject unknown keys with an error naming the key (and, for team/assistants, the allowed keys). `collaboration.enabled` in a flow is rejected with a hint that the switch lives in `config.yaml`/`AFM_COLLABORATION`.

Evidence: `afm:contracts/flow/flow_test.go` `TestParseFile_RepeatRejectsInvalid`, `TestParseFile_RejectsUnknownCollaborationField`; `afm:contracts/flow/team_test.go` `TestTeam_Rejected`; `afm:contracts/flow/assistants_test.go` `TestStageAssistants_UnknownKeySuggestsProfile`, `TestStageAssistants_LoopRejected`; `afm:contracts/flow/verify_test.go`; `afm:contracts/flow/flow.go` `RepeatConfig.unmarshalMapping`, `FlowCollaboration.UnmarshalYAML`

#### FLOW-S02 Typo inside repeat
- GIVEN `repeat: {max_cycle: 3}`
- WHEN the flow is parsed
- THEN parsing fails with `unknown field "max_cycle"`

### FLOW-R03 Explicit null is rejected where it would read as absent
A bare `team:`, `repeat:`, `skills:`/`skills_dir:` (flow and stage), and `budget:` (flow, stage, team member, assistant) is an error, including a null reached through an alias, tag or `<<` merge; a direct key overrides a merged null.

Evidence: `afm:contracts/flow/budget_test.go` `TestBudget_NullErrorNamesTheKey`, `TestBudget_RejectsNullThroughMerge`, `TestBudget_MergedValueAndOverrides`; `afm:contracts/flow/skills_test.go`; `afm:contracts/flow/flow.go` `rejectNullKey`, `rejectNullSkillKeys`, `rejectNullBudgetKeys`

### FLOW-R04 Stage IDs are unique, path-safe and case-distinct
Every stage ID matches `[A-Za-z0-9._-]+` and is not `.` or `..` (`StageIDSafe`); IDs are unique, and two IDs equal after lowercasing are rejected because they would share a session/Chrome profile on a case-insensitive filesystem. Dependencies stay case-sensitive.

Evidence: `afm:contracts/flow/flow_test.go` `TestValidateStageIDCharset`, `TestValidateStageIDCaseInsensitiveCollision`; `afm:contracts/flow/validate_pathsafety_test.go` `TestValidate_StageIDPathSafety`; `afm:contracts/flow/flow.go` `StageIDSafe`, `Flow.validate`

#### FLOW-S03 Case collision
- GIVEN stages `Build` and `build`
- WHEN the flow is parsed
- THEN parsing fails with `stage ids "Build" and "build" collide case-insensitively …`

### FLOW-R05 Dependencies must resolve and be acyclic
Each `depends_on` entry names an existing stage; the dependency graph is checked by DFS and any cycle fails with `cycle detected involving stage "<id>"`.

Evidence: `afm:contracts/flow/flow_test.go` `TestValidateCycle`; `afm:cmd/afm/validate_test.go` `TestValidateFlowFile_CycleReturnsError`, `TestValidateFlowFile_UnknownDependsOnReturnsError`; `afm:contracts/flow/flow.go` `detectCycles`

### FLOW-R06 Every stage has a way to produce work, and kinds do not mix
A stage needs a planning agent, a `plan` path, `interactive`, `auto` or `script`. `auto` must be the only agent. `script` cannot be combined with agents, `command`, `interactive`, `plan`, `verify`, `buttons` or `assistants`. `verify` and `assistants` require an execution stage. Buttons need non-empty unique labels and non-empty prompts.

Evidence: `afm:contracts/flow/flow_test.go` `TestValidateMissingPlan`, `TestValidateScriptCannotCombineWithAgents`, `TestValidateScriptCannotCombineWithVerify`, `TestValidateButtons_*`; `afm:contracts/flow/assistants_test.go` `TestStageAssistants_ScriptStageRejected`, `TestStageAssistants_PlanningOnlyStageRejected`, `TestStageAssistants_DuplicateProfileWithinStage`; `afm:contracts/flow/flow.go` `Flow.validate`

#### FLOW-S04 Verify on a planning-only stage
- GIVEN a stage with only `agents: [planning]` and a `verify` block
- WHEN the flow is parsed
- THEN parsing fails with `verify requires an execution stage …`

### FLOW-R07 Artifact inputs reference declared upstream artifacts
Artifact names are unique per stage. An input ref has the form `stage.artifact`; the stage must exist, be listed in `depends_on`, and declare that artifact.

Evidence: `afm:contracts/flow/flow_test.go` `TestValidateDuplicateArtifactName`, `TestValidateInputRefUnknownStage`, `TestValidateInputRefUnknownArtifact`, `TestValidateInputNotInDependsOn`; `afm:contracts/flow/flow.go` `Flow.validate`

### FLOW-R08 Memory and reflect settings are validated and defaulted
`memory.mode` and `reflect.mode` accept only `r`, `w`, `rw`; `memory.max_rules` must be ≥ 0. `reflect` needs `memory.path`, a non-empty `reflect.file` that is a local relative path and is not `memory.md`. With `memory.path` set, `max_rules` 0 becomes 25 and empty modes become `rw`. `memory.*` keys without `memory.path` only warn.

Evidence: `afm:contracts/flow/flow_test.go` `TestParseMemory_FieldsAndDefaults`, `TestValidateMemory_ReflectRequiresPath`, `TestValidateMemoryV3`; `afm:contracts/flow/validate_pathsafety_test.go` `TestValidate_ReflectFilePathSafety`; `afm:contracts/flow/memory_v3_flags_test.go`; `afm:contracts/flow/flow.go` `ParseFile`, `warnMemorySettingsWithoutPath`

### FLOW-R09 Parse-time defaults and hook validation
A script, `script_before` or `script_after` without a timeout gets 5 minutes. Flow- and stage-level lifecycle hooks pass `lifecyclehooks.ValidateLayer`, and a hook ID may appear in at most one stage.

Evidence: `afm:contracts/flow/flow_test.go` `TestScriptTimeoutDefaultsWhenUnset`, `TestScriptBeforeAfterTimeoutsDefaultWhenUnset`, `TestParseHooks_Errors`, `TestParseHooks_DifferentIDsAcrossLayersOK`; `afm:contracts/flow/flow.go` `applyScriptTimeoutDefaults`

### FLOW-R17 Runtime-added stages reuse the same ID rule and report coded errors
A stage added during a run is validated by the engine graph with `flow.StageIDSafe`, uniqueness, a pre-prepared command, an existing target and known dependencies; failures are `graph.ValidationError` with codes `invalid_stage_id`, `duplicate_stage_id`, `command_not_prepared`, `target_not_found`, `unknown_dependency`. A `StageDraft` with explicit `"Budget": null` is rejected.

Evidence: `engine:internal/orchestrator/graph/dynamic_test.go` `TestValidateDraftShape_RejectsUnsafeOrDuplicateID`, `TestDynamic_CommandMustBePrepared`, `TestAppend_UnknownExplicitDependencyRejected`; `engine:internal/orchestrator/graph/dynamic.go`; `afm:contracts/graph/models.go` `StageDraft.UnmarshalJSON`

### FLOW-R18 Engine commands use a stricter ID check
Engine stage commands (approve, dialog, stage control) accept a stage/question ID only if it matches `^[a-zA-Z0-9._-]+$` and contains no `..` (`ValidStageID`); otherwise they return an invalid-kind error.

Evidence: `engine:internal/engine/stage_control.go` `ValidStageID`; `engine:internal/engine/approval.go`, `engine:internal/engine/dialog.go`; `afm:pkg/server/handlers.go` `ValidStageID`

## Errors and rejections
| ID | Condition | Observable result | Evidence |
|----|-----------|-------------------|----------|
| FLOW-E01 | Flow file unreadable / invalid YAML | `read flow file: …` / `parse yaml: …` | `afm:contracts/flow/flow.go` `ParseFile` |
| FLOW-E02 | Duplicate stage ID | `duplicate stage id: "<id>"` | `afm:contracts/flow/flow.go` `Flow.validate` |
| FLOW-E03 | Unsafe stage ID | `stage "<id>": id must match [A-Za-z0-9._-] …` | `TestValidateStageIDCharset` |
| FLOW-E04 | Unknown dependency | `stage "<a>" depends_on unknown stage "<b>"` | `TestValidateFlowFile_UnknownDependsOnReturnsError` |
| FLOW-E05 | Cycle | `cycle detected involving stage "<id>"` | `TestValidateCycle` |
| FLOW-E06 | No work source | `stage "<id>": must have planning agent, a plan path, or script` | `TestValidateMissingPlan` |
| FLOW-E07 | Null budget | error naming the `budget` key | `TestBudget_NullErrorNamesTheKey` |
| FLOW-E11 | Invalid runtime-added stage | `graph.ValidationError` `<code>: <reason>` | `TestValidateDraftShape_RejectsUnsafeOrDuplicateID` |

## Coverage gaps and discrepancies
- **FLOW-G01** (code≠doc): `flow.schema.json`/`config.schema.json` use `additionalProperties: false` and docs say the schema flags typos, but `ParseFile`/`mergeFile` ignore unknown top-level and stage keys. A typo passes `afm validate` and `afm run`; only the editor schema catches it. No test asserts the lenient parser behaviour explicitly.
- **FLOW-G02** (ambiguous): `flow.StageIDSafe` accepts IDs containing `..` (e.g. `a..b`), while engine `ValidStageID` rejects them; such a stage parses but its engine commands (approve/dialog/control) are refused. No test covers the mismatch.
- **FLOW-G03** (untested): `ValidStageID` has no direct unit test in the engine.
