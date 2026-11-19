# Pre-completion review of a stage result in AFM 3.x

## ADDED

### Requirement: helpers-pre-completion/HLP-R80 — Pre-completion review runs before verify
AFM MUST accept `trigger: pre_completion` on a top-level helper of an execution stage (implementation, review
or auto), declared in `flow.yaml` or attached at runtime, together with the stage field
`pre_completion.max_failures` (integer >= 0, default 1), and MUST run it with an afm-engine that implements the
trigger (v1.2.3 or later). When the author's turn passes its completion check, the stage parks in
`awaiting_pre_completion`; verify and stage completion wait until every required helper of the round passed or was
waived. A required `needs_changes` gives the stage's own agent one corrective turn whose prompt contains the
helper's `summary` and `suggested_change` verbatim, counted against `max_failures`; the next author result is
reviewed by a new round. When a correction is owed and the budget is spent the stage fails. `afm validate` and run
opening MUST reject the trigger on a planning-only or script stage and a negative budget.

Evidence: `engine:docs/pre-completion.md`; `engine:internal/engine/pre_completion_integration_test.go`
`TestPreCompletionRun_CorrectionBeforeVerify`; `afm:contracts/flow/pre_completion.go` `CorrectionLimit`.

#### Scenario: HLP-S130 — The review runs between the author and verify
- GIVEN an auto stage with a verify step and a required `pre_completion` helper
- WHEN the author writes `execution_summary.md` and exits
- THEN the stage is `awaiting_pre_completion`, no verify step starts until the helper passes, and after the pass verify runs and the stage is done

#### Scenario: HLP-S131 — Requested changes go back to the stage agent
- GIVEN `pre_completion.max_failures: 1` and a required helper whose first answer is `needs_changes` with summary S and suggested change C
- WHEN the round is decided
- THEN the author's next prompt contains S and C, a second round reviews the corrected result, and only its pass starts verify

#### Scenario: HLP-S132 — A spent budget fails the stage
- GIVEN `pre_completion.max_failures: 0` and a required helper that answers `needs_changes`
- WHEN the round is decided
- THEN the stage is `failed`, the author is not started again and no verify step ran

#### Scenario: HLP-S133 — Invalid placement is rejected before the run
- GIVEN a flow whose planning-only stage declares a `pre_completion` helper
- WHEN `afm validate` checks the flow
- THEN it exits non-zero with an error that names the stage and the trigger

### Requirement: helpers-pre-completion/HLP-R81 — The stage view reports the review round
The stages of `GET /api/status` and the stage summaries of MCP `afm_run_status` (a separate compact summary built
from the same stage view) MUST report a stage that has a recorded round with `pre_completion` = {`round_id`,
`phase`, `decision`, `correction_count`, `max_failures`, `task_ids`}, where `max_failures` is the stage's effective
budget. They MUST set `pre_completion_blocked: true` exactly while the stage is `awaiting_pre_completion` with
decision `needs_decision`. A stage without a recorded round has neither field.

Evidence: `afm:contracts/state/models.go` `PreCompletionRound`; `afm:pkg/server/stageview.go` `BuildStageViews`;
`afm:pkg/mcpserver/status.go` `stageSummaries`.

#### Scenario: HLP-S134 — Status while helpers review
- GIVEN a stage whose round is open with decision `reviewing` and `max_failures: 1`
- WHEN a client reads `/api/status`
- THEN the stage has status `awaiting_pre_completion`, `pre_completion` with decision `reviewing`, `correction_count` 0, `max_failures` 1 and the round's task IDs, and no `pre_completion_blocked`

#### Scenario: HLP-S135 — An inconclusive review waits for a person
- GIVEN a required `pre_completion` helper that answers `inconclusive`
- WHEN the round is decided
- THEN `/api/status` shows decision `needs_decision` and `pre_completion_blocked: true`

### Requirement: helpers-pre-completion/HLP-R82 — Attaching a pre-completion helper over HTTP
`POST /api/stages/{id}/assistants` MUST create a `pre_completion` helper on an execution stage before its round
opens, and that helper MUST join the next round. While a round is open, while verify of an accepted round is owed,
and after the stage result, it MUST answer 409 with code `invalid_status` and leave the round's task IDs unchanged.

Evidence: `engine:internal/orchestrator/pre_completion_admission_test.go`; `afm:pkg/server/assistants.go`
`handleAttachAssistant`.

#### Scenario: HLP-S136 — Attach before the round joins it
- GIVEN an execution stage whose author is still running and has no round yet
- WHEN the dashboard Attach dialog submits trigger Pre-completion review
- THEN the response is 200 with a task ID, and the task ID is in `task_ids` of the round that opens after the author finishes

#### Scenario: HLP-S137 — Attach during a round is refused
- GIVEN a stage in `awaiting_pre_completion`
- WHEN a client attaches another `pre_completion` helper to it
- THEN the response is 409 with code `invalid_status` and the round's `task_ids` are unchanged

### Requirement: helpers-pre-completion/HLP-R83 — Dashboard presentation and controls of the review
The dashboard MUST show `awaiting_pre_completion` as "Pre-completion review" in the stage rail and the workspace
header, show the used corrections and the budget in the header, and treat only a `needs_decision` round as
attention (blocked badge and attention queue); a reviewing round is background work. It MUST offer Pause and
Restart with note in this status, offer "Pre-completion review" in the Attach dialog with a hint that it runs
after the stage agent's turn and before verify, and render `pre_completion_round` and `pre_completion_decision`
notices as readable feed rows that remain after a reload.

Evidence: `afm:pkg/web/dashboard/src/types/stage.ts` `STAGE_STATUS_LABELS`;
`afm:pkg/web/dashboard/src/components/feed-workspace/feed-view-model.ts` `mapEvent`.

#### Scenario: HLP-S138 — A reviewing round is shown as background work
- GIVEN a stage with an open round in decision `reviewing` and budget 1
- WHEN the user views the run in the dashboard
- THEN the rail and header show Pre-completion review, the header shows 0 of 1 corrections, and the stage has no attention badge

#### Scenario: HLP-S139 — A round that needs a decision draws attention and Waive resolves it
- GIVEN a round in decision `needs_decision` because a required helper answered `inconclusive`
- WHEN the user opens the helper task and waives it with a reason
- THEN before the waiver the rail showed the blocked badge, and after it the round is decided again, verify runs and the stage is done

#### Scenario: HLP-S140 — Pause and Continue keep the round
- GIVEN a stage in `awaiting_pre_completion` with decision `reviewing`
- WHEN the user presses Pause in the header and then Continue
- THEN the stage is `paused` and then `awaiting_pre_completion` again with the same `round_id`, and no extra author turn started

#### Scenario: HLP-S141 — Review notices remain in the feed after a reload
- GIVEN a stage whose first round requested changes and whose second round passed
- WHEN the user reloads the dashboard
- THEN the stage feed shows readable rows for the opened rounds, the requested changes with the correction count and the accepted round, in that order

### Requirement: helpers-pre-completion/HLP-R84 — CLI, MCP and SDK report the review
`afm check` MUST show a stage in `awaiting_pre_completion` with the attention color and a LAST ACTION naming the
round ID, its decision and the number of corrections, keeping the table columns aligned. MCP `afm_run_status` MUST
list a pending action of type `assistants_blocked` with reason `pre_completion` exactly for a round in
`needs_decision`. The Go SDK MUST expose `StageAwaitingPreCompletion` and decode `pre_completion` and
`pre_completion_blocked` of a stage view.

Evidence: `afm:cmd/afm/check.go` `statusColor`; `afm:pkg/mcpserver/attention.go` `stageAction`;
`afm:pkg/mcpserver/status.go` `stageSummary`; `afm:sdk/status.go`.

#### Scenario: HLP-S142 — afm check names the round
- GIVEN a run whose stage is in `awaiting_pre_completion` with decision `reviewing`
- WHEN the user runs `afm check`
- THEN the stage row shows `awaiting_pre_completion` and its LAST ACTION names the round ID, `reviewing` and 0 corrections

#### Scenario: HLP-S143 — MCP reports the rounds and lists a blocked review as a pending action
- GIVEN one stage whose round is in `needs_decision` and another whose round is `reviewing`
- WHEN an MCP client calls `afm_run_status`
- THEN both stage summaries carry `pre_completion` with their round ID and decision, only the blocked stage has `pre_completion_blocked: true`, and exactly the blocked stage has a pending action of type `assistants_blocked` with reason `pre_completion`

#### Scenario: HLP-S144 — The SDK decodes the review fields
- GIVEN a `/api/status` response with a stage in `awaiting_pre_completion` carrying `pre_completion` and `pre_completion_blocked`
- WHEN the Go SDK decodes it
- THEN the stage status equals `StageAwaitingPreCompletion` and the round fields and the blocked flag are populated

## MODIFIED

## REMOVED
