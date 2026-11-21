# Explicit stage inputs for helpers in AFM

## ADDED

### Requirement: helpers-context/HLP-R85 — Helper profiles accept an optional stage_context block
AFM MUST accept `collaboration.profiles.<id>.stage_context` with a required `inputs` list of `stage.artifact`
references, where an empty list is valid, in the global and project `config.yaml`, in a pipeline package's
`agents.yaml` and in `--pipeline-options`. Omitting the block and giving an empty list MUST stay distinct,
including through the encoded options layer that a Docker re-exec reads back. Config loading, and with it
`afm validate` and `afm run`, MUST reject `null`, unknown keys of the block, a missing or non-list `inputs`,
non-string items and references not in `stage.artifact` form. The published `schema/config.schema.json` MUST
accept and reject the same forms. AFM MUST run with `github.com/akopichin/afm/contracts` v0.4.1 and
`github.com/akopichin/afm-engine` v1.2.4 or later.

Evidence: `afm:contracts/config/collaboration.go` `ProfileConfig`; `afm:pkg/pipeline/layer.go` `ParseLayer`,
`EncodeLayer`; `afm:pkg/schemacheck/schemacheck_test.go`; `afm:cmd/afm/validate_test.go`.

#### Scenario: HLP-S145 — A valid block passes validation
- GIVEN a project config whose profile `picky` has `stage_context: {inputs: [prep.rules]}` and is declared on stage `work` with inputs `[prep.rules, prep.big]`, and whose profile `bare` has `stage_context: {inputs: []}` and backs a `team:` member
- WHEN the user runs `afm validate flow.yaml`
- THEN it exits 0 and prints `OK:` with the flow name

#### Scenario: HLP-S146 — A malformed block stops config loading
- GIVEN a project config whose helper profile has `stage_context: null`, or a block whose `inputs` holds a reference without a dot such as `rules`
- WHEN the user runs `afm validate flow.yaml`
- THEN it exits non-zero with an error that names `stage_context` and no run is started

#### Scenario: HLP-S147 — Package and options profiles keep the block through the encoded layer
- GIVEN a pipeline options layer where profile `a` has `stage_context: {inputs: []}` and profile `b` has no block
- WHEN the layer is parsed, encoded with `EncodeLayer` and parsed again
- THEN `a` still has an empty, non-nil list, `b` still has no block, and a layer with `stage_context: null` or an unknown key inside the block is rejected with the profile's key path

#### Scenario: HLP-S148 — The published schema agrees with the loader
- GIVEN the forms without a block, with `inputs: []`, with valid references, and the forms `null`, `{}`, `inputs: null`, a scalar `inputs`, a number item, a reference without a dot, an unknown key and a list instead of a mapping
- WHEN each is checked against `schema/config.schema.json` and loaded with `config.LoadFrom`
- THEN both accept exactly the first three forms and reject all the others

### Requirement: helpers-context/HLP-R86 — Selected references are checked before a run starts
With collaboration enabled, the helper-profile preflight shared by `afm validate`, `afm pipelines inspect` and
`afm run --pipeline`, and the run opening of a plain `afm run`, MUST reject a declared stage helper whose
profile selects a reference that is not one of that stage's `inputs`, and a `team:` member whose profile selects
any reference, because a run agent has no root stage. The error MUST name the stage or the team member, the
profile and the reference. A team member with an empty list and a profile without the block are accepted.
`afm run` MUST fail before any stage starts.

Evidence: `afm:cmd/afm/validate.go` `checkHelperProfiles`; `afm:cmd/afm/run_pipeline_preflight.go`
`pipelinePreflight`; `engine:internal/engine/collaboration_policy.go` `CheckStageAssistants`, `CheckTeamProfiles`.

#### Scenario: HLP-S149 — An unknown reference of a declared helper stops validation and the run
- GIVEN stage `work` with inputs `[prep.rules, prep.big]` that declares a helper of profile `bad` with `stage_context: {inputs: [prep.missing]}`
- WHEN the user runs `afm validate flow.yaml` and then `afm run` on the same flow
- THEN both exit non-zero with an error naming `work`, `bad` and `prep.missing`, and neither `prep` nor `work` starts

#### Scenario: HLP-S150 — A team member may only select nothing
- GIVEN a `team:` member whose profile has `stage_context: {inputs: [prep.rules]}`
- WHEN the user runs `afm validate flow.yaml`
- THEN it exits non-zero with an error naming the member, its profile and `prep.rules`

### Requirement: helpers-context/HLP-R87 — A helper in a run receives only the selected stage inputs
In a run, a helper task whose profile has `stage_context` MUST get in its context only the selected inputs of its
root stage, collected under the stage's `inline` and `optional` rules, and no root-stage plan. An empty list adds
no stage inputs while the task instruction, the output contract and the agent communication block remain. A
profile without the block MUST keep the plan and all inputs. The stage agent's own prompt MUST keep all of its
inputs. This applies to helpers declared in `assistants:`, including the request turns of `on_message` helpers,
and to helpers created with `afm agent spawn`. With the model, container, task and instructions unchanged,
selecting fewer inline inputs MUST reduce the helper's first model request by their size.

Evidence: `engine:internal/orchestrator/assistants_stage_context.go` `stagePlanAndArtifacts`;
`engine:internal/orchestrator/assistants_context.go` `buildTaskContext`; live acceptance of this change.

#### Scenario: HLP-S151 — A declared helper gets only the selected input while the stage keeps all
- GIVEN stage `work` with a plan and inline inputs `prep.rules` and a large `prep.big`, and a required `on_completion` helper whose profile selects `[prep.rules]`
- WHEN the run executes `work` and the helper's turn starts
- THEN the stage agent's prompt contains both inputs, and the helper's `context.md` contains `prep.rules` but neither `prep.big` nor the plan

#### Scenario: HLP-S152 — Without the block the helper keeps the full stage context
- GIVEN the same run with the helper's profile lacking `stage_context`
- WHEN the helper's turn starts
- THEN its `context.md` contains the plan and the contents of both `prep.rules` and `prep.big`

#### Scenario: HLP-S153 — A spawned helper gets only the selected input
- GIVEN stage `work` with inline inputs `prep.rules` and `prep.big` and a profile `picky-stub` that selects `[prep.rules]`
- WHEN the stage agent runs `afm agent spawn --profile picky-stub`
- THEN the spawned helper's `context.md` contains `prep.rules` and not `prep.big`

#### Scenario: HLP-S154 — An on_message request turn gets only the selected input
- GIVEN a declared `on_message` helper on `work` whose profile selects `[prep.rules]`
- WHEN the stage agent sends it a request with `afm agent send --kind request`
- THEN the `context.md` of the turn that answers the request contains `prep.rules` and not `prep.big`

#### Scenario: HLP-S155 — An empty selection keeps AFM's own blocks
- GIVEN a declared helper on `work` whose profile has `stage_context: {inputs: []}`
- WHEN its turn starts
- THEN its `context.md` contains no content of `prep.rules` or `prep.big` and still contains the task instruction, the output contract and the agent communication block

#### Scenario: HLP-S156 — Selecting inputs shrinks the helper's first model request
- GIVEN runs A and B with the same model, container, task and instructions that differ only in the helper profile, where A has no block and B selects `[prep.rules]`, and `prep.big` is about 120 KB inline
- WHEN each helper makes its first model request
- THEN B's `context.md` is smaller than A's by at least the size of `prep.big`, B's first-request input including cache reads and writes is lower than A's by at least 10000 tokens, and the stage prompts of A and B differ only in run paths

#### Scenario: HLP-S157 — A dev-spec implement reviewer works with an empty selection
- GIVEN a stage with the dev-spec 2.2.1 `implement` inputs passed by path, a candidate with a seeded boundary defect, and the frozen `dev-spec-default-verifier` profile with `stage_context: {inputs: []}` spawned with a brief that names the criteria, scenarios, file paths and review skills
- WHEN the reviewer runs next to a control reviewer whose profile has no block
- THEN the reviewer's `context.md` has no stage-input section while the control lists the input paths, and both reviewers commit a result that reports the seeded defect

## MODIFIED

## REMOVED
