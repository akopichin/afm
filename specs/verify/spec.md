# Verify gate

> Baseline: afm `462bd5d4` · afm-engine `v1.2.1` (`11bddf2c`). Status: draft baseline.

## Scope
- When the stage verify gate runs, the YAML forms of `verify:`, step ordering.
- Where steps run (cwd, `AFM_STAGE_DIR`) and what the AI verifier is denied
  (session, credentials, browser, edits).
- Persisted reports and active machine feedback.
- Out of scope: verdict decoding, repair, exec failures and retries →
  [../verify-verdict/spec.md](../verify-verdict/spec.md); correction budget,
  stalemate, fresh sessions → [../verify-budget/spec.md](../verify-budget/spec.md).
  IDs: this file uses R01–R19, S01–S29, E01–E09, G01–G09.

## Terms
- **Step**: one shell (`run`) or AI (`command`) check. **Pass**: one run of all
  steps under a new verification ID (`<stageDir>/verify/<id>/`).
- **File-probe**: the pre-verify completion check (`.done`/artifacts, or non-empty
  `execution_summary.md` for auto stages).

## Requirements

### VER-R01 Gate runs only after a passing file-probe, outside planning
Verify runs only when the base completion check passes, the stage declares a
non-empty `verify`, and the phase is implementation, review or autonomous. A
planning phase and an empty spec never run verification.

Evidence: engine:internal/orchestrator/gate_verify_test.go `TestGateWithVerify_ProbeFailsSkipsVerify`, `TestGateWithVerify_EmptyVerifyNeverRunsVerification`, `TestGateWithVerify_PlanningPhaseNeverGated`; engine:internal/orchestrator/verify.go `gateWithVerify`

#### VER-S01 Probe fails
- GIVEN a stage with `verify: "go test ./..."` and no `.done`
- WHEN the completion gate is evaluated
- THEN the probe error is returned and no verify step starts

#### VER-S02 Autonomous stage is gated
- GIVEN an `agents: [auto]` stage with a non-empty `execution_summary.md` and a verify step
- WHEN the gate runs
- THEN verification runs before completion

### VER-R02 Verify never completes the stage itself
Verify does not publish `EvComplete` or activate dependents; on pass the
normal completion path completes the stage. A Pause arriving after a verify pass
but before completion keeps the stage paused.

Evidence: engine:internal/orchestrator/verify_pause_integration_test.go `TestIntegration_PauseAfterVerifyPassBeforeCompletion_StaysPaused`; engine:internal/orchestrator/AGENTS.md (AI-verify); afm:docs/verify.md

### VER-R03 Resume re-verifies unfinished work, never done stages
On resume, a running/retrying stage whose probe already passes runs a fresh
verify gate; a `done` stage is never verified again. An extra turn or an answered
question after a passed verify is verified again.

Evidence: engine:internal/orchestrator/verify_recovery_test.go `TestIntegration_ResumeRunningWithDoneFile_RunsFreshVerifyGate`, `TestIntegration_ResumeDoneStage_NeverReVerifies`; engine:internal/orchestrator/budget_r7_verify_test.go `TestBudgetR7_ExtraTurnAfterPassedVerifyIsVerifiedAgain`

#### VER-S03 Done stage on restart
- GIVEN a stage already `done` in the journal
- WHEN the run resumes
- THEN no verification pass is created for it

### VER-R04 YAML forms and parse rules
`verify` is a scalar (one shell step), a single step object, a list of steps, or
a container `{steps, max_failures, review_patience, fresh_correction}`. A step
needs exactly one of `run`/`command`; `prompt` is AI-only; `name` must be a
non-blank string; timeouts and budgets must be non-negative; empty objects/lists,
nested lists, unknown keys and step fields at container top level are rejected.

Evidence: afm:contracts/flow/verify_test.go `TestVerifySpec_Scalar`, `TestVerifySpec_RunAndCommandMutuallyExclusive`, `TestVerifySpec_ContainerForm`, `TestVerifySpec_ContainerStepsMixedWithRunRejected`, `TestVerifySpec_StepNameRejected`, `TestVerifySpec_NegativeTimeoutRejected`; afm:schema/flow.schema.json

#### VER-S04 Both run and command
- GIVEN a list item with both `run` and `command`
- WHEN the flow is parsed
- THEN parsing fails naming the 1-based list index

### VER-R05 Verify is rejected on script and planning-only stages
A `script:` stage or a planning-only stage cannot declare `verify`.

Evidence: afm:contracts/flow/flow_test.go `TestValidateScriptCannotCombineWithVerify`, case "planning-only stage with verify rejected"; afm:contracts/flow/flow.go; engine:internal/orchestrator/verify_wiring_integration_test.go `TestIntegration_PlanningOnlyStageNeverInvokesVerify`

### VER-R06 Text-only and cursor recipes rejected at preflight
An AI step whose command resolves to a `type: openai` (text-only) or
`type: cursor` recipe is rejected before the flow starts, naming stage, step and
recipe type. Shell steps are not checked; other commands are not probed.

Evidence: afm:contracts/config/verify_resolve_test.go `TestVerifyCommandRejection_TextOnlyRecipes`, `TestVerifyCommandRejection_CursorReasonIsAnUnverifiedVerdict`, `TestValidateVerifySpecs_IgnoresShellSteps`, `TestValidateVerifySpecs_CodexRecipeWithoutShim`

### VER-R07 Steps run in order, fail-fast, no cross-attempt caching
Steps run sequentially; the first non-passing step stops the pass and later
steps are recorded as not run. Each new attempt re-runs all steps from the first.

Evidence: engine:internal/orchestrator/verify_test.go `TestRunVerification_ShellPassThenAgentNeedsChanges`, `TestRunVerification_StaleAcceptedResultNotReused`; afm:docs/verify.md

### VER-R08 Steps run in the agents' project root
Shell and AI steps use the effective project root (worktree, else
`flow.root_dir`, else the process CWD). A shell step gets `AFM_STAGE_DIR` set to
the verified stage's absolute directory, replacing any inherited value.

Evidence: engine:internal/orchestrator/verify_shell_dir_test.go `TestRunVerification_ShellStepRunsInProjectRoot`, `TestRunVerification_RealShellStepSeesAbsoluteStageDir`; engine:internal/orchestrator/verify.go `sanitizedShellVerifyEnv`; engine:internal/orchestrator/runner_factory.go `verifyExecutorConfig` (`Dir: RootDir`)

#### VER-S05 Shell step sees stage dir
- GIVEN a shell step `test -f "$AFM_STAGE_DIR/plan.md"`
- WHEN it runs
- THEN `AFM_STAGE_DIR` is the absolute stage directory of that stage

### VER-R09 AI verifier is isolated from the author
The AI verifier runs a fresh session (no `--resume`/`--session-id`), with empty
`AFM_STAGE_DIR`, no dialog phase, no managed browser, and `CODEX_VERIFY=1`. Its
raw actions are not streamed to the feed. Cross-agent and browser transport
secrets are stripped; Claude author credentials are stripped unless the verifier
itself is Claude; a recipe keeps only its own secret.

Evidence: engine:internal/executor/verify_agent_test.go `TestRunVerifyAgent_FreshSessionNoResumeNoStageDir`, `TestRunVerifyAgent_StripsClaudeAuthorCredentials`, `TestVerifyEnv_ClaudeVerifierKeepsClaudeCredentials`, `TestRunVerifyAgent_KeepsOwnRecipeSecretButStripsOthers`; engine:internal/executor/dialog_phase_env_test.go `TestRunVerifyAgent_NoDialogPhase`; engine:internal/orchestrator/browser_policy_test.go `TestRunnerForVerify_NoBrowser`; engine:internal/orchestrator/verify_quiet_test.go `TestVerifyExecutorConfig_PublishesNoRawActions`

#### VER-S06 Verifier gets no browser
- GIVEN a run with a managed browser descriptor
- WHEN an AI verify runner is built
- THEN it carries no browser descriptor

### VER-R10 Shell step environment is sanitized and cancellable
A shell step runs via the shell in its own process group, without cross-agent or
browser transport secrets; cancel/Pause kills the group; a pre-cancelled context
or already-paused stage never starts it.

Evidence: engine:internal/orchestrator/verify_shell_security_test.go `TestRunVerifyShellCommand_StripsCrossAgentTransportSecrets`, `TestRunVerifyShellCommand_PreCancelledCtxNeverStarts`; engine:internal/orchestrator/browser_policy_test.go `TestSanitizedShellVerifyEnv_StripsBrowserTransport`; engine:internal/orchestrator/verify_shell_procgroup_test.go `TestRunVerifyShellCommand_KillsProcessGroupOnCancel`

### VER-R11 Read-only is prompt-level; tree changes only warn
The verify prompt instructs the reviewer to be read-only. The engine fingerprints
the Git tree (excluding `.afm/`) before and after each AI step and, if it
changed, emits a durable `verify_tree_modified` warning; the verdict is unchanged.
No warning is emitted when the fingerprint is unavailable.

Evidence: engine:internal/orchestrator/verify_tree_modified_test.go `TestVerifyAgentStep_WarnsWhenVerifierModifiedTree`, `TestVerifyAgentStep_NoWarnWhenFingerprintUnavailable`, `TestVerifyTreeModified_IsDurable`; afm:contracts/assets/prompts/verify.md

#### VER-S07 Verifier edits a file
- GIVEN an AI verifier that writes a tracked file and returns `pass`
- WHEN the step finishes
- THEN the step passes and a tree-modified warning is published

### VER-R12 Command lease swaps to the verifier's command
Before an AI step the stage's command lease moves to the step command
(release-before-acquire); the same command is a no-op. After the pass the lease
returns to the author. Waiting for a slot is aborted by Pause/Revise, and the
verifier then never starts.

Evidence: engine:internal/orchestrator/verify_lease_test.go `TestRunVerification_SwapsLeaseToVerifierCommandAndBackToAuthor`, `TestRunVerification_SameCommandAsAuthorNeverReleasesLease`; engine:internal/orchestrator/verify_pause_test.go `TestRunVerification_LeaseWaitAbortsOnPause_VerifierNeverStarts`

### VER-R13 Persist before publish; storage failure fails closed
Raw result, accepted result, manifest, report and feedback are written before the
verify event is published. A storage failure is a verify error, never a pass.

Evidence: engine:internal/orchestrator/verify_test.go `TestRunVerification_StorageFailureFailsClosed`; engine:internal/orchestrator/verify_pass_storage_test.go `TestRunVerification_PassStorageOutcomes`

### VER-R14 Active feedback reaches the author and clears on pass
A rejection writes `verify/feedback.md` (capped at 12 KiB with a truncation
marker), separate from the human `feedback.md`; both reach the next author
attempt, including after a crash. A full pass clears it and keeps history.

Evidence: engine:internal/orchestrator/stagefiles/verify_test.go `TestWriteActiveFeedback_TruncatesToBudget`, `TestClearActiveFeedback_RemovesFeedbackKeepsArchive`; engine:internal/orchestrator/verify_feedback_inject_test.go `TestRunImplementationWithFeedback_InjectsVerifyFeedbackAlongsideHumanNote`; engine:internal/orchestrator/verify_recovery_test.go `TestIntegration_ResumeAfterCrashDuringVerifyCorrection_InjectsActiveFeedback`

### VER-R15 Step notices carry the optional step name
Started, result, retry, rejection, tree-modified, exec-failure and stalemate
notices carry the step `name`, scoped to its stage. Names do not change what runs.

Evidence: engine:internal/orchestrator/verify_names_test.go `TestVerifyNotices_StartedAndResultCarryStepName`, `TestVerifyNotices_StepNamesAreScopedToTheirStage`

### VER-R16 Live open question blocks verify
While the stage has a live open question, verify is not invoked; a stale open
question does not swallow a rejection.

Evidence: engine:internal/orchestrator/verify_openquestion_test.go `TestRunWithRetry_VerifyGatedLiveOpenQuestion_NeverInvokesVerify`, `TestRunWithRetry_VerifyGatedStaleOpenQuestion_RejectionNotSwallowed`

## Errors and rejections
| ID | Condition | Observable result | Evidence |
|----|-----------|-------------------|----------|
| VER-E01 | Malformed `verify:` shape | flow parse error | afm:contracts/flow/verify_test.go `TestVerifySpec_EmptyObjectRejected` |
| VER-E02 | `verify` on script / planning-only stage | flow validation error | afm:contracts/flow/flow_test.go `TestValidateScriptCannotCombineWithVerify` |
| VER-E03 | AI step on openai/cursor recipe | preflight error before flow start | afm:contracts/config/verify_resolve_test.go `TestValidateVerifySpecs_RejectsTextOnlyRecipe` |
| VER-E04 | Verify storage write fails | verify error, stage not passed | engine:internal/orchestrator/verify_test.go `TestRunVerification_StorageFailureFailsClosed` |

## Coverage gaps and discrepancies
- **VER-G01** (code≠doc): docs say `AFM_STAGE_DIR` lets "a step" check stage
  artifacts; the AI verifier gets an empty `AFM_STAGE_DIR`
  (`TestRunVerifyAgent_FreshSessionNoResumeNoStageDir`); only shell steps get it.
- **VER-G02** (doc-only): "remaining steps recorded as not run" in the manifest
  has no direct test asserting the `not run` marker.
- **VER-G03** (doc-only): `report.md` per pass (not per step) link behavior is
  documented as a limitation, untested.
