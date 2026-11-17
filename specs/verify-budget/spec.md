# Verify correction budget, stalemate and fresh corrections

> Baseline: afm `462bd5d4` · afm-engine `v1.2.1` (`11bddf2c`). Status: draft baseline.

## Scope
- `verify.max_failures`: how many rejections the author may correct.
- `verify.review_patience`: stalemate acceptance after unchanged-tree rejections.
- `verify.fresh_correction`: session renewal after a rejection.
- Out of scope: gate and isolation → [../verify/spec.md](../verify/spec.md);
  verdict decoding and exec failures → [../verify-verdict/spec.md](../verify-verdict/spec.md).
  IDs: this file uses R40–R59, S60–S89, E20–E29, G20–G29.

## Terms
- **Correction**: an author rerun caused by a rejection (AI `needs_changes` or
  shell non-zero exit), with `verify/feedback.md` injected.
- **Fingerprint**: hash of HEAD, staged and unstaged diffs, and non-ignored
  untracked files' paths and contents, excluding `.afm/`.

## Requirements

### VER-R40 Resolution order and defaults
`max_failures` resolves stage container > global config > 1; `review_patience`
stage > global > 0; `fresh_correction` stage > project > global > `true`.
Project config overrides global, including an explicit 0. Negative values are
rejected at config load and flow parse; these keys exist only in the container form.

Evidence: engine:internal/orchestrator/verify_maxfailures_test.go `TestVerifyMaxFailures_ResolutionHelper`; engine:internal/orchestrator/verify_stalemate_loop_test.go `TestVerifyReviewPatience_ResolutionHelper`; afm:contracts/config/config_test.go `TestVerifyMaxFailuresMerge_ProjectZeroOverGlobal`, `TestVerifyMaxFailures_NegativeRejected`, `TestVerifyReviewPatience_NegativeRejected`, `TestVerify_ResolvedFreshCorrectionDefault`; afm:contracts/flow/verify_test.go `TestVerifySpec_MaxFailuresWithoutStepsRejected`, `TestVerifyFreshCorrection_RequiresSteps`

#### VER-S60 Stage beats global
- GIVEN global `verify.max_failures: 1` and a stage container `max_failures: 3`
- WHEN the stage is rejected repeatedly
- THEN up to 3 corrections are made before failure

### VER-R41 N corrections, then failure
With budget N the author gets N corrections; the (N+1)-th rejection fails the
stage via `EvVerifyFail` with a diagnosis. N=0 fails on the first rejection. Each
correction emits a retry-scheduled notice.

Evidence: engine:internal/orchestrator/verify_maxfailures_test.go `TestVerifyMaxFailures_DefaultOneCorrection`, `TestVerifyMaxFailures_ZeroStrict`, `TestVerifyMaxFailures_ThreeCorrections`, `TestVerifyMaxFailures_RetryEventsPerCorrection`; engine:internal/orchestrator/verify_retry_test.go `TestRunWithRetry_VerifyRejected_OneFreeRetryThenFailsWithDiagnosis`

#### VER-S61 Default budget
- GIVEN no `max_failures` configured and a verifier that always rejects
- WHEN the stage runs
- THEN the author runs twice (initial + one correction) and the stage is `failed`

#### VER-S62 Strict budget
- GIVEN `max_failures: 0`
- WHEN the first verify rejection arrives
- THEN the stage fails without rerunning the author

### VER-R42 Only rejections spend the budget
Exec failures, transport/rate-limit retries, plan-incomplete retries, repairs,
native reruns and usage-limit parks do not draw on the correction budget, and the
budget does not draw on theirs.

Evidence: engine:internal/orchestrator/verify_maxfailures_test.go `TestVerifyMaxFailures_TransportRetryDoesNotConsumeBudget`, `TestVerifyMaxFailures_PlanIncompleteAndVerifyIndependent`, `TestVerifyMaxFailures_ExecErrorNotRetried`; engine:internal/orchestrator/verify_quota_test.go `TestVerifyQuota_ParkKeepsVerifyCountersAndNeverSpendsTheBudget`

### VER-R43 Effective ceiling is min(max_failures, MaxRetries)
Corrections ride the retry loop (`MaxRetries` = 15); a larger budget is capped
there and the stage still ends terminally `failed`, never stuck active.

Evidence: engine:internal/orchestrator/verify_maxfailures_test.go `TestVerifyMaxFailures_BudgetExceedsMaxRetries`; engine:internal/orchestrator/retry.go `MaxRetries`

### VER-R44 Counters are in-memory
Correction and unchanged-round counters live in the running retry loop. A
usage-limit park keeps them within the process (resetting only the previous
fingerprint); a process restart starts them from zero.

Evidence: engine:internal/orchestrator/verify_quota_test.go `TestVerifyQuota_ResumeKeepsCountersButResetsFingerprint`; engine:internal/orchestrator/retry.go `runWithRetry` (local `correction` state); afm:docs/verify.md

### VER-R45 Stalemate accepts after N unchanged consecutive rejections
With `review_patience` N ≥ 1, when an AI rejection is the N-th consecutive one
whose fingerprint equals the previous rejection's, the stage is accepted as
complete with an `accepted_stalemate` verify result instead of a correction. A
visible change between rejections resets the count. 0 disables the feature.

Evidence: engine:internal/orchestrator/verify_stalemate_loop_test.go `TestStalemate_NoEditsAcceptsStage`, `TestStalemate_ChangingTreeNeverAccepts`, `TestStalemate_ChangeThenFreezeAccepts`; engine:internal/orchestrator/verify_names_test.go `TestVerifyNotices_StalemateAcceptCarriesStepName`; engine:internal/orchestrator/verify_stalemate.go

#### VER-S63 Author holds ground
- GIVEN `review_patience: 2`, `max_failures: 3`, and an author that never edits
- WHEN the AI verifier keeps returning `needs_changes`
- THEN the stage completes with an accepted-stalemate badge, not `failed`

#### VER-S64 Author keeps editing
- GIVEN `review_patience: 1` and an author that changes a tracked file each round
- WHEN the verifier keeps rejecting
- THEN stalemate never fires and the budget decides

### VER-R46 Stalemate never overrides a shell gate
Only an AI step's rejection can be accepted, and only when no shell step follows
it in the sequence. A shell rejection, or an AI rejection followed by an unrun
shell step, is never accepted.

Evidence: engine:internal/orchestrator/verify_stalemate_loop_test.go `TestStalemate_ShellFirstThenAgentAccepts`, `TestStalemate_AgentFirstUnrunShellNeverAccepts`, `TestStalemate_ShellOnlyNeverAccepts`

### VER-R47 Fingerprint is fail-safe
A non-git dir, unborn HEAD, git error, timeout, or oversized diff/untracked file
yields "unknown", which resets the count and never accepts. `.afm/` churn is
excluded; staged/unstaged cancellation and commits are detected.

Evidence: engine:internal/orchestrator/verify_stalemate_test.go `TestWorkingTreeFingerprint_NonGitDir`, `TestWorkingTreeFingerprint_OversizedUntracked`, `TestWorkingTreeFingerprint_OversizedDiffNotOk`, `TestWorkingTreeFingerprint_AfmChurnExcluded`, `TestWorkingTreeFingerprint_StagedUnstagedCancellationDiffers`, `TestWorkingTreeFingerprint_CommittedChangeDiffers`; engine:internal/orchestrator/verify_stalemate_loop_test.go `TestStalemate_NotOkFingerprintNeverCounts`

### VER-R48 Pause/Revise during fingerprinting wins
A Pause or Revise that lands while the stalemate fingerprint is computed is not
overwritten by acceptance or a correction.

Evidence: engine:internal/orchestrator/verify_stalemate_loop_test.go `TestStalemate_ReviseDuringFingerprintNotClobbered`, `TestStalemate_PauseDuringFingerprintNotClobbered`

### VER-R49 Fresh correction drops the author session
With `fresh_correction` true, a rejection (AI or shell) removes the phase's
session file so the interactive author's next attempt starts fresh; with false the
session is kept. Non-verify incomplete retries keep the session. A fresh attempt
gets a digest of answered dialog questions (≤ 8 KiB); a resumed one does not.

Evidence: engine:internal/orchestrator/verify_freshcorrection_test.go `TestVerifyReject_DropsSessionWhenFreshCorrectionOn`, `TestVerifyReject_KeepsSessionWhenFreshCorrectionOff`, `TestIncompleteNonVerify_KeepsSession`; engine:internal/orchestrator/dialog_digest_wiring_test.go `TestRetryContext_DigestWhenSessionDropped`, `TestRetryContext_NoDigestWhenResuming`

#### VER-S65 Fresh session after rejection
- GIVEN an `interactive: true` stage with default `fresh_correction`
- WHEN verify rejects and a correction is allowed
- THEN the next author attempt runs without `--resume` and with verify feedback

## Errors and rejections
| ID | Condition | Observable result | Evidence |
|----|-----------|-------------------|----------|
| VER-E20 | Negative `max_failures`/`review_patience` | config load / flow parse error | afm:contracts/config/config_test.go `TestVerifyMaxFailures_NegativeRejected`; afm:contracts/flow/verify_test.go `TestVerifySpec_ContainerNegativeReviewPatienceRejected` |
| VER-E21 | Budget key outside container form | flow parse error | afm:contracts/flow/verify_test.go `TestVerifySpec_ReviewPatienceWithoutStepsRejected` |
| VER-E22 | Budget exhausted | stage `failed` via `EvVerifyFail` | engine:internal/orchestrator/verify_retry_test.go `TestRunWithRetry_VerifyRejected_OneFreeRetryThenFailsWithDiagnosis` |

## Coverage gaps and discrepancies
- **VER-G20** (untested): reset of correction/stalemate counters across a real
  process restart is asserted only by docs and code structure.
- **VER-G21** (doc-only): `review_patience > max_failures` being a silent no-op
  follows from loop order; no dedicated test.
