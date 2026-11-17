# Config and preflight contract

> Baseline: afm `462bd5d4` · afm-engine `v1.2.1` (`11bddf2c`). Status: draft baseline.

## Scope
- How `config.yaml` is loaded and merged from the global and project layers (`afm:contracts/config` `LoadFrom`), and env/flow overrides.
- Preflight checks of helper profiles (`BuildProfileRegistry`, `CheckStageAssistants`, `CheckTeamProfiles`) and the `afm validate` command.
- The published JSON Schemas and their drift guard (`schema/`, `pkg/schemacheck`, `make schema-check`); strict model-JSON primitives (`contracts/jsonstrict`).
- Out of scope: flow parsing and stage IDs — [../flow-config/spec.md](../flow-config/spec.md) (FLOW-R01..R09, R17, R18); pipeline packages — [../flow-config-pipelines/spec.md](../flow-config-pipelines/spec.md); budget semantics — [../budgets-accounting/spec.md](../budgets-accounting/spec.md).

## Terms
- **afm-root**: parent of `.afm/`; `--dir` > `AFM_DIR` > `.`.
- **Global layer / project layer**: `~/.afm/config.yaml` / `<afm-root>/.afm/config.yaml`.
- **Helper profile**: an entry of `collaboration.profiles` or the builtin default, referenced by stage `assistants:` and flow `team:`.

## Requirements

### FLOW-R10 Config layers merge field by field, project over global
`LoadFrom` starts from `Default()` (client `claude`, idle timeout 30m, port 9876, open_browser false, auto_recover true), then overlays the global and then the project `config.yaml`. A missing file is skipped. Only fields set in a layer override (pointer/non-empty checks), so an explicit `false`/`0` in the project wins and an absent key keeps the lower value.

Evidence: `afm:contracts/config/config_test.go` `TestDefaultConfig`, `TestLoadProjectOverridesGlobal`, `TestLoadMissingFiles`, `TestAutoRecoverMerge_ProjectDisablesGlobalDefault`, `TestAutoRecoverMerge_AbsentKeepsDefault`, `TestVerifyMaxFailuresMerge_ProjectZeroOverGlobal`; `afm:contracts/config/config.go` `LoadFrom`, `mergeFile`

#### FLOW-S05 Project disables a global default
- GIVEN global `auto_recover` absent and project `auto_recover: false`
- WHEN config is loaded
- THEN `IsAutoRecover()` is false

### FLOW-R11 Keyed maps merge per key; whole records are replaced
`docker.agents`, `collaboration.profiles`, `pricing.models`, `pricing.channels.<ch>`, `command_names` merge per key, and the later layer replaces the whole record for that key. `hooks` merge by `id`, each layer validated before merge so a duplicate ID inside one file is not hidden.

Evidence: `afm:contracts/config/config_test.go` `TestDockerAutoShim_MergeLayers`, `TestPricingConfig_MergeProjectOverridesGlobal`, `TestHooksMerge_KeyedByID`; `afm:contracts/config/command_names_test.go` `TestCommandNames_MergesByKeyProjectWins`; `afm:contracts/config/config.go` `mergeFile`

### FLOW-R12 Config validation fails loading
Loading fails on: malformed YAML; null `skills_dir`; invalid command naming; negative `collaboration.max_depth`/`limits.max_tasks` (naming the layer); invalid `collaboration.mode` in any layer, even when a higher layer overrides it; invalid `docker.extra_mounts`; negative pricing rates; invalid hooks; negative `verify.max_failures`/`review_patience`; non-positive `usage_limit` values; invalid browser settings. Recipe validity (`AgentRecipe.Validate`) is checked separately via `DockerConfig.ValidateAgents`.

Evidence: `afm:contracts/config/config_test.go` `TestPricingConfig_NegativeRateRejected`, `TestVerifyMaxFailures_NegativeRejected`, `TestHooksMerge_InvalidEventFails`, `TestExtraMounts_Validate`, `TestDockerAutoShim_ValidationErrors`; `afm:contracts/config/collaboration_test.go` `TestMaxDepth_NegativeRejectedAtLoad_NamesGlobalLayer`, `TestCollaborationMode_InvalidLowerLayerRejectedEvenWhenOverridden`; `afm:contracts/config/config.go` `LoadFrom`, `mergeFile`

### FLOW-R13 Environment switches beat config; flow beats both where it may override
Feature switches read env first: `AFM_COLLABORATION`, `AFM_SIDEAGENT`, `AFM_ADD_STAGE`, `AFM_WORKTREES`, accounting and file-browser env (an empty env value does not override). Flow overrides exist only for `side_agent`, `worktrees`, `collaboration.max_depth` and `collaboration.mode`: side agent is flow > env > config > ON; collaboration enable is env > config > ON with no flow override; max_depth is flow > project > global > default.

Evidence: `afm:contracts/config/collaboration_test.go` `TestCollaboration_EnvZeroOverridesConfigTrue`, `TestCollaboration_EmptyEnvDoesNotOverride`; `afm:contracts/config/config_test.go` `TestSideAgent_EnvOverridesConfig`, `TestWorktrees_EnvOverridesConfig`, `TestAddStage_Resolution`, `TestAccounting_EnvOverridesConfig`; `engine:internal/engine/assistant_test.go` `TestSideAgentEnabled_Precedence`; `engine:internal/engine/collaboration_policy.go` `ResolveCollaboration`, `ResolveCollaborationMode`

### FLOW-R14 Helper profiles must exist when collaboration is on
`BuildProfileRegistry` builds the registry from the builtin default plus `collaboration.profiles` and is the single validation of those profiles. With collaboration on, `CheckStageAssistants` and `CheckTeamProfiles` return the first stage assistant / team member whose profile is missing. With collaboration off, both are skipped and a `warning:` says assistants/team will not be attached. Executor availability is not checked (PATH differs host vs container).

Evidence: `afm:cmd/afm/validate_test.go` `TestValidateFlowFile_StageAssistantUnknownProfile`, `TestValidateFlowFile_TeamUnknownProfile`, `TestValidateFlowFile_BuiltinDefaultProfile`, `TestValidateFlowFile_StageAssistantWithDisabledCollaborationWarns`, `TestValidateFlowFile_TeamWithDisabledCollaborationWarns`; `engine:internal/engine/collaboration_policy.go` `BuildProfileRegistry`, `CheckStageAssistants`, `CheckTeamProfiles`, `CheckDeclaredAssistants`

#### FLOW-S06 Unknown helper profile
- GIVEN collaboration enabled and a stage `assistants: [qa]` with no `qa` profile
- WHEN `afm validate` or `afm run` preflight runs
- THEN it fails with `stage "<id>": unknown assistant profile "qa" (not in collaboration.profiles)`

### FLOW-R15 A malformed profile registry: run degrades, validate fails
At run start a registry build error disables collaboration with `warning: collaboration disabled: …` instead of failing the run. `afm validate` reports the same error as invalid, suffixed `(afm run would turn collaboration off)`. On resume, team members already created in the run journal need no profile; `afm validate` reads no journal and checks all members.

Evidence: `afm:cmd/afm/validate_test.go` `TestValidateFlowFile_TeamWithMalformedProfileRegistry`; `engine:internal/engine/collaboration_policy.go` `startCollaborationWithStderr`, `CheckFlowTeam`; `afm:cmd/afm/validate.go` `checkHelperProfiles`, `teamToCreate`

### FLOW-R16 afm validate mirrors fresh-run preflight
`afm validate <flow.yaml>` runs `ParseFile`, the helper-profile check (loading config only when helpers are declared) and the money-budget explicit-rate check (as engine `Open` does), prints `OK: "<name>" — N stage(s), valid`, and wraps failures as `invalid flow.yaml: …`. It starts no agent.

Evidence: `afm:cmd/afm/validate_test.go` `TestValidateFlowFile_ValidFlowReturnsOK`, `TestValidateFlowFile_MoneyRatesMatchEngineOpen`, `TestValidateFlowFile_MissingRatesNameScopeCommandModelAndCategories`; `afm:cmd/afm/validate.go` `validateFlowFile`

### FLOW-R19 Published JSON Schemas are kept in sync with the parsers
Canonical schemas live in `contracts/schema/`; `schema/` holds byte-identical published copies. `make schema-check` fails on copy drift (`scripts/sync-schemas.py --check`), on a Go field missing from the schema at its exact path or a schema property absent in Go, on a schema that does not compile as draft-7, on schema/parser disagreement for listed edge cases, and on repository examples that do not validate.

Evidence: `afm:pkg/schemacheck/schemacheck_test.go` `TestFlowSchemaCoversStructAtCorrectPaths`, `TestConfigSchemaCoversStructAtCorrectPaths`, `TestSchemasCompileAsDraft7`, `TestFlowSchemaMatchesParserEdgeCases`, `TestFlowSchemaMatchesParserBudget`, `TestRepositoryYAMLExamplesMatchSchemas`; `afm:Makefile` `schema-check`; `afm:schema/README.md`

#### FLOW-S07 New Go field without schema
- GIVEN a field added to `flow.Stage` but not to `flow.schema.json`
- WHEN `make schema-check` runs
- THEN `TestFlowSchemaCoversStructAtCorrectPaths` fails

### FLOW-R20 Strict decoding of model JSON answers
`contracts/jsonstrict` is used for model-produced JSON (verify results, assistant results, MCP dialog), not for `flow.yaml`/`config.yaml`. It enforces a byte cap before parsing, rejects a markdown fence, unknown fields, trailing content and duplicate keys at any depth, and repairs only raw control bytes inside string literals.

Evidence: `afm:contracts/jsonstrict/jsonstrict_test.go` `TestRejectFence`, `TestEnforceMaxBytes`, `TestDecodeDisallowUnknown_RejectsUnknownField`, `TestDecodeDisallowUnknown_RejectsTrailingContent`, `TestDecodeNoDuplicates_NestedSecondLevel`, `TestRepairRawNewlines_OnlyInsideStrings`; `afm:contracts/jsonstrict/jsonstrict.go`

## Errors and rejections
| ID | Condition | Observable result | Evidence |
|----|-----------|-------------------|----------|
| FLOW-E08 | Config layer invalid | `parse <global\|project> config <path>: …` or field-path error; run does not start | `afm:contracts/config/config.go` `mergeFile` |
| FLOW-E09 | Unknown helper profile (collab on) | `unknown assistant profile "<p>" (not in collaboration.profiles)` | `TestValidateFlowFile_TeamUnknownProfile` |
| FLOW-E10 | Money budget without explicit rates | `money budget of <scope> needs explicit rates: command …, model …, missing …` | `TestValidateFlowFile_MissingRatesNameScopeCommandModelAndCategories` |

## Coverage gaps and discrepancies
- **FLOW-G04** (untested): `ResolveCollaboration` precedence (env > config, no flow override; depth flow > project > global) is tested only indirectly; failure to record the policy disabling collaboration has no direct test.
- **FLOW-G05** (ambiguous): `LoadFrom` does not validate `docker.agents` recipes; `DockerConfig.ValidateAgents` runs only in the engine Docker projection (`engine:internal/engine/docker_projection.go`) and `afm memory rebuild`. `afm validate` therefore does not report an invalid recipe, and a host (non-Docker) run may never validate it.
