# Dialog and approval: agent questions and user answers

> Baseline: afm `462bd5d4` · afm-engine `v1.2.1` (`11bddf2c`). Status: draft baseline.

## Scope
- Agent side: `afm stage ask` / `afm stage wait-answer` (`engine:host/stage.go`) over question/answer files.
- Engine side: the question poller, `awaiting_user_input`, answer validation and delivery (answer window and
  hand-off), cancel, non-interactive auto-answer, malformed-question repair, restart.
- Family (prefix `DLG`, disjoint ranges): plans, notes, revise — [dialog-approval](../dialog-approval/spec.md)
  (`R01–R19`); this file `R20+`.
- Out of scope: mesh questions between agents ([agent-mesh-waits](../agent-mesh-waits/spec.md)), interactive
  normalization ([stage-lifecycle-tracks](../stage-lifecycle-tracks/spec.md) STG-R25), lifecycle-hook question
  events ([lifecycle-hooks](../lifecycle-hooks/spec.md)).

## Terms
- **phase** — `planning`, `implementation`, `review`, `autonomous_execution`; files are `<phase>.q<N>.question.json` /
  `.answer.json` in the stage dir, history in `<phase>.dialog.jsonl`.
- **live agent** — a turn whose process will itself read the answer (`answeringAgent`), not merely a running goroutine.
- **continuation** — a new turn started because the answer arrived after the asking turn left.

## Requirements

### DLG-R20 Asking a question
Only a stage agent with `AFM_DIALOG_PHASE` can ask. `afm stage ask` validates the payload, assigns the next
numeric id per phase and creates the question file exclusively with `allow_custom: true`. Repeating the open
question returns its id; a different question while one is open fails `question_pending` with the open id.
Mistakes print one JSON error on stderr with exit 1.

Evidence: engine:host/stage_test.go `TestStageAsk_CreatesQuestionAndPrintsWaitCommand`,
`TestStageAsk_RepeatIsIdempotentAndOtherTextIsPending`, `TestStageAsk_LegacyFlagCannotDisableFreeText`,
`TestStageCommands_NotStageAuthor`, `TestStageAsk_ValidationErrorsAreJSONOnStderr`;
afm:contracts/mcp/ask_test.go `TestAskQuestion_ConcurrentAsksNeverOverwrite`, `TestAskQuestion_OrderIsNumericPastNine`

### DLG-R21 Waiting for the answer
`afm stage wait-answer [qN]` (default: the phase's latest question) exits 0 with `{id, answer, from_options}`, or
75 with a retry hint after the timeout: `--timeout`, else `AFM_WAIT_ANSWER_TIMEOUT`, else 100 s, clamped to
1–600. A partial or invalid answer file counts as not yet answered.

Evidence: engine:host/stage_test.go `TestStageWaitAnswer_TimeoutExits75WithRetryHint`,
`TestStageWaitAnswer_WithoutArgWaitsForQ10`, `TestStageWaitAnswer_PartialAnswerKeepsWaiting`, `TestWaitAnswerTimeout`;
afm:contracts/mcp/ask_test.go `TestReadAnswer_PartialInvalidOrMissingMeansNotYet`

### DLG-R22 An interactive question moves the stage to the user
For an interactive stage the poller surfaces only the oldest open question: it commits `ask_user` (from
`planning`, `running`, `retrying`) and publishes one `dialog_question` notice. A question counts as asked only if
that transition committed; a revised stage's leftover question is not handed to the user. An open question at
agent exit keeps the stage in `awaiting_user_input`.

Evidence: engine:internal/orchestrator/dialog_poller_test.go `TestPollQuestions_InteractiveBatch_SurfacesOldestOnly`,
`TestPollQuestions_InteractiveQuestion_EmitsDialogQuestionOnce`, `TestPollQuestions_Regression_BatchOfEight_NoDeadlock`;
engine:internal/orchestrator/poller_revise_test.go `TestPoller_DoesNotHandARevisedStageToTheUser`;
engine:internal/orchestrator/integration_interactive_test.go `TestFullDialogCycle`,
`TestIntegration_InteractiveOpenQuestionHoldsOnAgentExit`; engine:internal/orchestrator/bus/fsm_test.go
`TestFSM_Apply_AskUser_NotFromRevising`

### DLG-R23 Answer validation and the answer file
`Answer(stage, phase, qID, text, fromOptions)` requires a valid phase, safe ids, non-empty text and an existing
question file. Free text is always accepted (even for legacy `allow_custom: false`); `fromOptions=true` must equal
a listed option. Interactive stages must be answered in question order. The answer file is created exclusively
before any FSM change; a second answer conflicts and a rejected answer writes nothing.

Evidence: engine:internal/engine/commands_integration_test.go `TestCommands_AnswerRejectionsOnALiveQuestion`;
engine:internal/engine/dialog_integration_test.go `TestCommands_LegacyRestrictedQuestionAcceptsFeedback`;
afm:pkg/server/command_contract_test.go `TestCommandContract_DialogAnswer_AllowedOption`,
`TestCommandContract_DialogAnswer_InteractiveOrdering`, `TestCommandContract_DialogAnswer_Duplicate`;
engine:internal/engine/dialog.go `StageControl.Answer`

#### DLG-S20 Option claim that does not match
- GIVEN a question with options `Go`, `Python`
- WHEN the client answers `Rust` with `fromOptions=true`
- THEN `KindInvalid`, no answer file, and the question still accepts a valid answer.

### DLG-R24 Answer delivery: one continuation, never stranded
Delivery and every dialog decision of a turn are serialized under one lock. If a live agent will read the
answer, `user_answered` is committed fenced to its epoch and the stage returns to the asking phase; otherwise the
answer starts exactly one continuation. An answer landing between a turn's detection of its question and its
exit is never left in `awaiting_user_input`; an answer while another question is open starts nothing.

Evidence: engine:internal/orchestrator/answer_window_test.go `TestAnswerWindow_AnswerBeforeTheTurnLeavesIsNotStuck`,
`TestAnswerWindow_AnswerRacingTheDialogDecisionNeverStrandsTheStage`, `TestAnswerWindow_ReviseBeforeTheTurnLeavesRunsTheFeedbackTurn`;
engine:internal/orchestrator/answer_handoff_test.go `TestAnswerHandoff_AnswerAfterTheTurnAsksTheUserStartsOneContinuation`,
`TestAnswerHandoff_AnswerWhileAnotherQuestionIsOpenStartsNothing`, `TestAnswerHandoff_ResumeAfterTheAnswersContinuationStartsNoAgent`;
engine:internal/orchestrator/control_api.go `answerToLiveAgent`; engine:internal/orchestrator/bus/fsm_test.go
`TestFSM_PhaseDispatch_UserAnswered`

#### DLG-S21 Answer after the agent exited
- GIVEN an interactive stage parked in `awaiting_user_input` with no live agent
- WHEN the user answers
- THEN one continuation of the asking phase starts with the answer available.

### DLG-R25 Dialog history and feed notices
After an accepted answer the engine appends it to `<phase>.dialog.jsonl` (best effort; the answer file is
authoritative) and publishes a `dialog_answer` notice live and to `notices.jsonl`. If a review pause wins the
race after the file was written, the answer file is removed and `flow_paused` is returned.

Evidence: afm:pkg/server/command_contract_test.go `TestCommandContract_DialogAnswer_Success`,
`TestCommandContract_DialogAnswer_NotifyFailures`; engine:internal/engine/dialog.go `StageControl.Answer`

### DLG-R26 Non-interactive stages are auto-answered
A question from a non-interactive stage (including `agents: [auto]`) is answered by the engine: the first option
carrying a marker such as `(recommended)`/`(default)` (marker stripped, `from_options=true`), else the first
option, else a fixed fallback instruction. The status is not changed by the answer; a stage already parked in
`awaiting_user_input` is resumed. A durable `dialog_question` + `auto_answered` notice pair is published; a
failed write is retried next tick; nothing is answered during a review pause.

Evidence: engine:internal/orchestrator/dialog_poller_test.go `TestPollQuestions_NonInteractiveStageAutoAnswers`,
`TestPollQuestions_AutoStageAutoAnswers`, `TestPollQuestions_ParkedNonInteractiveStageIsUnparked`,
`TestPollQuestions_FailedAutoAnswerWriteIsRetried`, `TestPollQuestions_AutoAnswerPersistsToNotices`;
afm:contracts/mcp/dialog_test.go `TestPickAutoAnswer_MarkerVariants`, `TestPickAutoAnswer_NoOptions_ReturnsFallbackText`;
engine:internal/orchestrator/integration_auto_answer_test.go `TestIntegration_NonInteractiveStageAutoAnswersQuestion`

### DLG-R27 Malformed and misplaced questions
A malformed question file gets one grace tick (a write may still complete), a local repair, then up to 3
isolated format-fix agents. If still invalid, an interactive stage shows the raw text as a free-text question; a
non-interactive stage uses the auto-answer fallback. Misplaced or misnamed question files are relocated into the
stage dir.

Evidence: engine:internal/orchestrator/dialog_poller_test.go `TestPollQuestions_MalformedQuestion_GraceTickHidesFromUser`,
`TestPollQuestions_MalformedQuestion_FixAgentRepairsQuestion`, `TestPollQuestions_MalformedQuestion_FixAgentExhaustionShowsStub`,
`TestPollQuestions_MalformedQuestion_NonInteractiveFixFailsThenAutoAnswerFallback`;
engine:internal/orchestrator/integration_interactive_test.go `TestIntegration_MisplacedQuestionRelocated`;
engine:internal/orchestrator/dialog_poller.go `maxJSONFixAttempts`

### DLG-R28 Answered and reused question ids
An answered question is not reopened. The poller forgets answered keys, so a reused question id after an answer
is asked again.

Evidence: engine:internal/orchestrator/integration_interactive_test.go `TestIntegration_StaleAnsweredQuestionNotReopened`;
engine:internal/orchestrator/dialog_poller_test.go `TestPollQuestions_ReusedIDAfterAnswerAsksAgain`

### DLG-R29 Cancel dialog
`CancelDialog` of a stage in `awaiting_user_input` fails it with reason "cancelled by user" and runs the failure
cascade; other statuses are rejected.

Evidence: afm:pkg/server/command_contract_test.go `TestCommandContract_DialogCancel`;
engine:internal/orchestrator/control_api.go `CancelDialog`

### DLG-R30 Questions survive restart and retry
A stage in `awaiting_user_input` keeps its files across restart; an unanswered question is shown again and an
answer resumes the phase. Manual Retry / auto_recover of an interactive stage clears phase sessions and truncates
phase logs so an old question does not re-fire.

Evidence: engine:internal/orchestrator/orchestrator_test.go `TestResumeInteractiveAgent_PlanningPhase`,
`TestResumeInteractiveAgent_ImplementationPhase`; engine:internal/orchestrator/recovery_auto_test.go
`TestAutoRecover_ClearsStaleInteractiveSessionBeforeRetry`; engine:internal/orchestrator/integration_interactive_test.go
`TestIntegration_InteractiveFailureClearsSession`

## Errors and rejections
| ID | Condition | Observable result | Evidence |
|----|-----------|-------------------|----------|
| DLG-E20 | Invalid phase, unsafe id, empty text | `KindInvalid` | `TestCommands_AnswerRejectionsOnALiveQuestion` |
| DLG-E21 | Question file missing | `KindNotFound` | afm:pkg/server/command_contract_test.go `TestCommandContract_DialogAnswer_Rejections` |
| DLG-E22 | Interactive answer to a non-current question | `KindConflict`, `answer_out_of_order` | `TestCommandContract_DialogAnswer_InteractiveOrdering` |
| DLG-E23 | Question already answered | `KindConflict` | `TestCommandContract_DialogAnswer_Duplicate` |
| DLG-E24 | Unparseable question file at answer time | `KindInternal` | `TestCommandContract_DialogAnswer_MalformedQuestionFile` |
| DLG-E25 | Cancel of a stage not awaiting input | `KindInvalid` | `TestCommandContract_DialogCancel` |
| DLG-E26 | `stage ask`/`wait-answer` misuse | exit 1, `{"error","message"}`: `invalid_question`, `question_pending`, `no_question`, `invalid_arguments`, `not_stage_author` | engine:host/stage_test.go `TestStageCommands_CallMistakesAreOneJSONError` |

## Coverage gaps and discrepancies
- **DLG-G20** (code≠doc): `interactive-stages.md` says non-interactive auto-answer picks the marked option "or
  makes a best-effort autonomous decision when there are no options"; code also picks the **first** option when
  none is marked, and the no-options fallback is a fixed Russian instruction string.
- **DLG-G21** (ambiguous): FSM allows `pause` from `awaiting_user_input` (used by budgets), but the public Pause
  rejects it; see [stage-lifecycle](../stage-lifecycle/spec.md) STG-G01.
- **DLG-G22** (untested): best-effort `dialog.jsonl` append failure leaving history without the answer is untested.
- **DLG-G23** (doc-only): the `executor.idle_timeout` kill of a waiting agent (default 30 min) is documented, not
  tested in this domain.
