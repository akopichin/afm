# Verify verdicts and execution failures

> Baseline: afm `462bd5d4` · afm-engine `v1.2.1` (`11bddf2c`). Status: draft baseline.

## Scope
- The strict AI verdict contract (`pass` / `needs_changes` / `inconclusive`) and its
  decoding, local repair, format-only repair and native-schema reruns.
- Classification of outcomes: rejection vs. execution failure vs. interruption,
  transient verifier retries, timeouts, usage-limit parking.
- Out of scope: gate placement and isolation → [../verify/spec.md](../verify/spec.md);
  correction budget and stalemate → [../verify-budget/spec.md](../verify-budget/spec.md).
  IDs: this file uses R20–R39, S30–S59, E10–E19, G10–G19.

## Terms
- **Rejection**: AI `needs_changes` or shell non-zero exit; feeds the correction path.
- **Exec failure** (`VerifyExecError`): no trustworthy verdict; terminal
  `EvVerifyFail`, author not rerun.
- **Native mode**: the CLI's own structured output (`--json-schema`, `--output-schema`).

## Requirements

### VER-R20 Strict verdict contract
A verdict needs `schema_version` 1, a known verdict, and a non-blank `summary`.
`pass` must have no blocking finding; `needs_changes` needs at least one.
Text fields accept a string or list of strings (joined); any other shape,
extra JSON data, or the reserved `accepted_stalemate` is rejected.

Evidence: engine:internal/orchestrator/verify/result_test.go `TestDecodeModelResult_HappyPaths`, `TestDecodeModelResult_Rejections`, `TestDecodeModelResult_TextFieldsAcceptLines`, `TestDecodeModelResult_TextFieldsStayStrictOtherwise`; engine:internal/orchestrator/verify/result.go `validateVerdictConsistency`; afm:schema/verify-result.schema.json

#### VER-S30 Pass with a blocker
- GIVEN `{"verdict":"pass", ... "findings":[{"blocking":true,...}]}`
- WHEN decoded
- THEN it is a protocol error, never a pass

### VER-R21 Repair is bounded and never changes the verdict
A malformed answer gets a narrow local repair (e.g. raw newlines inside strings),
then a format-only model call told the exact validation error. Every candidate
passes the same strict checks and must keep the verdict and all findings; an
explicit `null` verdict is never format-repaired. `raw-result.json` keeps the
original; repair spends no correction budget.

Evidence: engine:internal/orchestrator/verify/result_test.go `TestDecodeModelResult_RepairsBareNewline`, `TestDecodeModelResult_RepairDoesNotMaskRejections`, `TestDecodeModelResult_RepairStaysNarrow`; engine:internal/orchestrator/output_recovery_test.go `TestOutputRecovery_PreservationAndBudget`, `TestOutputRecovery_FormatterPromptNamesTheValidationError`; engine:internal/orchestrator/output_recovery_null_verdict_test.go `TestRecoverOutput_ExplicitNullVerdictIsNeverFormatRepaired`

#### VER-S31 Formatter flips the verdict
- GIVEN a raw `needs_changes` answer with a format error
- WHEN the format-only candidate says `pass`
- THEN the candidate is rejected and the step does not pass

### VER-R22 Every AI answer records its provenance
Each AI step writes `output.recovery.json` (source `native`/`prompt`/`repair`/
`format_agent`, fallback reason, hashes, attempts), even for a clean answer.

Evidence: engine:internal/orchestrator/native_schema_test.go `TestOutputRecovery_ProvenanceSourceOfEveryAnswer`, `TestPersistOutputRecovery_WritesEveryAnswer`, `TestVerifyNative_CleanAnswerRecordsItsSource`

### VER-R23 Native mode by command family, one prompt-only rerun
Native schema is requested by family (claude → `--json-schema`; codex → strict
`--output-schema`; openai-agent → schema file). If the native run is rejected by
the provider, or the CLI forced a `pass` or an undecodable answer, the same step
reruns once prompt-only, keeping slot and deadline, without spending budget or a
transient retry. Forced `needs_changes`/`inconclusive` stand. Interrupts,
expired deadlines and usage limits are not rerun.

Evidence: engine:internal/orchestrator/native_schema_test.go `TestNativeSchemaFor_ByCommandFamily`, `TestVerifyNative_RejectionRerunsPromptOnlyInTheSameStep`, `TestVerifyNative_EnforcedPassRerunsPromptOnly`, `TestVerifyNative_EnforcedFailClosedVerdictIsAccepted`, `TestVerifyNative_ControlFailuresAreNotRerun`, `TestVerifyNative_ExpiredDeadlineIsNotRerun`; engine:internal/executor/verify_native_test.go `TestRunVerifyAgent_ForcedPassIsReportedAsEnforced`

#### VER-S32 Forced pass
- GIVEN the CLI marks the final payload `[structured-output-enforce]` with verdict `pass`
- WHEN the step evaluates it
- THEN the step reruns prompt-only and uses that answer

### VER-R24 Inconclusive and protocol errors are terminal exec failures
`inconclusive`, a protocol error, an empty final message, or a non-zero verifier
exit (even after pass-looking text) is a `VerifyExecError`: the stage fails via
`EvVerifyFail` with a diagnosis; the author is not rerun and no correction is spent.

Evidence: engine:internal/orchestrator/verify_test.go `TestRunVerification_AgentInconclusive_ExecError`, `TestRunVerification_AgentProtocolError_ExecError`, `TestRunVerification_AgentNonZeroExitAfterPassText_NeverPasses`; engine:internal/orchestrator/verify_retry_test.go `TestRunWithRetry_VerifyExecError_NoRetryFailsImmediatelyWithDiagnosis`; engine:internal/executor/verify_agent_test.go `TestRunVerifyAgent_ProtocolErrOnEmptyFinalMessage`

#### VER-S33 Verifier says inconclusive
- GIVEN an AI step returning `inconclusive` with a summary
- WHEN the pass ends
- THEN the stage is `failed` and the author is not started again

### VER-R25 Timeouts are exec failures and mask any verdict
A step `timeout` bounds slot wait and execution. An expired step deadline is an
exec failure even if the verifier returned `pass`, `needs_changes` or
`inconclusive`; a shell timeout is not a rejection.

Evidence: engine:internal/orchestrator/verify_deadline_race_test.go `TestRunVerification_DeadlineRaceMasksPass`, `TestRunVerification_AgentStepDeadlineExpiredMasksNeedsChangesAsTimeout`; engine:internal/orchestrator/verify_timeout_test.go `TestRunVerification_ShellStepTimeoutIsExecErrorNotRejection`, `TestRunVerification_AgentStepTimeoutIsExecError`

### VER-R26 Transient verifier crashes retry twice with 8 s backoff
An AI process failure that is not a timeout, interrupt, usage limit or direct
executor/storage error is retried up to 2 times with an interruptible 8-second
backoff, emitting durable retry notices. Exhaustion is a terminal exec failure
whose reason carries the verifier's stderr tail (not for interrupts).

Evidence: engine:internal/orchestrator/verify_execretry_test.go `TestVerifyExecRetry_TransientThenPass`, `TestVerifyExecRetry_TransientExceedsBudget`, `TestVerifyExecRetry_TimedOutNoRetry`, `TestVerifyExecRetry_GenericErrorNoRetry`, `TestVerifyExecRetry_InterruptDuringBackoff`, `TestVerifyExecRetry_EnrichesReasonFromStderr`

#### VER-S34 Two crashes then pass
- GIVEN a verifier that exits non-zero twice and then passes
- WHEN the step runs
- THEN the step passes after two retry notices

### VER-R27 Shell non-zero exit is a rejection
A shell step's non-zero exit is normalized to a `needs_changes` result with the
command output tail and follows the correction path.

Evidence: engine:internal/orchestrator/verify_test.go `TestRunVerification_ShellRejectedCarriesLegacyTail`; engine:internal/orchestrator/verify.go `shellRejectionResult`

### VER-R28 Interruptions are neither pass nor rejection
Pause/Revise/cancel during verify propagates as a user interrupt; it never becomes
`needs_changes`. A late Pause/Revise after an exec failure or rejection keeps the
stage paused/revising instead of being overwritten by fail/retry.

Evidence: engine:internal/orchestrator/verify_test.go `TestRunVerification_Interrupted_PropagatesUserInterrupted`; engine:internal/orchestrator/verify_outcome_race_test.go `TestRunWithRetry_VerifyExecError_LateRevise_DoesNotOverrideRevising`, `TestRunWithRetry_NeedsChanges_LatePause_DoesNotOverridePaused`; engine:internal/orchestrator/verify_pause_integration_test.go `TestIntegration_PauseDuringVerifier_StopsSubprocessAndStaysPaused`

#### VER-S35 Pause during verifier
- GIVEN an AI verifier subprocess running
- WHEN the user pauses the stage
- THEN the subprocess stops and the stage stays `paused`

### VER-R29 Provider usage limit parks the stage at the same step
A recognized usage limit (or provider hold at verify admission) moves the stage to
`awaiting_quota`; after the wait the same verify step reruns without the author,
spending neither the exec retry nor the correction budget. A limit with an
expired step deadline is a timeout. A corrupt manifest on resume fails closed.

Evidence: engine:internal/orchestrator/verify_quota_test.go `TestVerifyQuota_LimitParksVerifyAndAuthorIsNotCalledAgain`, `TestVerifyQuota_ParkKeepsVerifyCountersAndNeverSpendsTheBudget`, `TestVerifyQuota_ReopenWaitsRemainderAndRepeatsOnlyTheStep`, `TestVerifyQuota_LimitAtExpiredStepDeadlineIsTimeoutNotPark`, `TestVerifyQuota_CorruptManifestFailsClosedAsVerifyStorageError`; engine:internal/executor/usage_limit_test.go `TestRunVerifyAgent_UsageLimit`

### VER-R30 Verifier usage is recorded once
Each AI verify invocation records usage once, also when it errors; shell steps
record no agent usage.

Evidence: engine:internal/orchestrator/verify_usage_test.go `TestIntegration_VerifyAgentInvocationRecordsUsageOnce`, `TestIntegration_VerifyAgentErroredInvocationStillCountedOnce`, `TestIntegration_VerifyShellStepProducesNoAgentUsage`

## Errors and rejections
| ID | Condition | Observable result | Evidence |
|----|-----------|-------------------|----------|
| VER-E10 | Verdict fails strict schema after repair | exec failure, stage `failed` | engine:internal/orchestrator/verify_test.go `TestRunVerification_AgentProtocolError_ExecError` |
| VER-E11 | Step deadline expires | exec failure (timeout), never pass | engine:internal/orchestrator/verify_deadline_race_test.go `TestRunVerification_DeadlineRaceMasksPass` |
| VER-E12 | Transient crashes exceed 2 retries | exec failure with stderr tail | engine:internal/orchestrator/verify_execretry_test.go `TestVerifyExecRetry_TransientExceedsBudget` |
| VER-E13 | Corrupt manifest at quota resume | verify storage error, fail closed | engine:internal/orchestrator/verify_quota_test.go `TestVerifyQuota_CorruptManifestFailsClosedAsVerifyStorageError` |

## Coverage gaps and discrepancies
- **VER-G10** (doc-only): openai-agent adapter's in-adapter schema validation
  "up to twice" is documented; the engine only passes the schema file
  (`TestNativeSchema_OpenAIAgentGetsTheOriginalSchemaFile`); the retry count is adapter-side, untested here.
- **VER-G11** (ambiguous): docs table lists "transport failure" as an exec
  failure without retry, while the transient-retry section retries stream breaks;
  code retries any non-timeout/interrupt/limit process failure.
