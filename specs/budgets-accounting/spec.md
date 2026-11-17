# Budgets and accounting: usage accounting

> Baseline: afm `462bd5d4` · afm-engine `v1.2.1` (`11bddf2c`). Status: draft baseline.

## Scope
- Collecting token usage of agent invocations into `usage.jsonl` and pricing it (`afm:contracts/accounting`).
- Coverage and health verdicts; `pricing:` overrides.
- Display switches `accounting.enabled` / `accounting.show_money`; `afm check` / `afm report` cost output.
- Out of scope: limits and enforcement (`../budgets-accounting-limits/spec.md`), budget control API,
  exporter and run outcome (`../budgets-accounting-control/spec.md`), config parsing (`../flow-config/spec.md`).

## Terms
- **Record**: one line of `.afm/runs/<run>/usage.jsonl`, one per agent invocation.
- **Unmetered**: invocation with no usage. **Unpriced**: usage known, no complete rate.
- **Explicit rates**: a full card (input, output, cache read, cache write) from config `pricing:`.

## Requirements

### BUD-R01 Usage comes only from the terminal result
The collector reads usage only from the terminal `result` envelope of an agent stream; repeated
intermediate `assistant` messages are never summed. An interrupted process or missing terminal line
yields an unmetered record; an unknown envelope contract version is unsupported, not zero.

Evidence: `afm:contracts/accounting/collector_test.go` `TestCollectorClaudeTerminalResult`,
`TestCollectorRepeatedAssistantNotSummed`, `TestCollectorInterruptedIsUnmetered`,
`TestCollectorUnknownContractVersionIsUnsupported`; `afm:contracts/accounting/collector.go`; `afm:docs/accounting.md`

#### BUD-S01 Streaming turn counted once
- GIVEN a claude stream with three `assistant` messages carrying usage and one terminal `result`
- WHEN the collector finishes
- THEN exactly one record with the terminal usage is produced

### BUD-R02 Provider usage is normalized into one token shape
Anthropic input is cache-exclusive; OpenAI/Codex input is cache-inclusive and uncached input is
`max(input - cached - cache_write, 0)`; an underflow clamps to zero and records a warning. Reasoning
tokens are a subset of output and never added to totals.

Evidence: `afm:contracts/accounting/normalize_test.go` `TestNormalizeAnthropic`, `TestNormalizeOpenAIInclusive`,
`TestNormalizeOpenAIUnderflowClamps`; `afm:contracts/accounting/normalize.go`

#### BUD-S02 Inconsistent inclusive counts
- GIVEN a Codex usage with `cached_input_tokens` greater than `input_tokens`
- WHEN it is normalized
- THEN uncached input is 0 and the record carries an `inclusive input underflow` warning

### BUD-R03 Pricing precedence and per-category completeness
Rates resolve from `pricing.channels.<ch>.<model>` > `pricing.models.<model>` > built-in channel/model >
built-in model > unpriced. A config override merges per category onto a matching built-in card.
An invocation is priced only if every used category has a rate; explicit `0` is a priced free category.

Evidence: `afm:contracts/accounting/pricing_test.go` `TestResolvePrecedenceAndCost`, `TestResolveChannelOverride`,
`TestResolvePartialOverrideMergesOntoBuiltin`, `TestResolveUnknownModelPartialOverrideMissingCategory`;
`afm:contracts/accounting/ledger_test.go` `TestBuildRecordUnknownModelUnpriced`; `afm:contracts/accounting/pricing.go`

#### BUD-S03 Partial override of unknown model
- GIVEN `pricing.models.x: {output: 15}` and no built-in `x`
- WHEN an invocation of `x` uses input tokens
- THEN the record is unpriced and its tokens are still shown

### BUD-R04 Cache-write TTL rates fall back to the generic rate
`cache_write_5m` / `cache_write_1h` use their own rate or fall back to `cache_write`; TTL-less
cache writes use `cache_write`. A record stores whether its rates were explicit; old lines read as not explicit.

Evidence: `afm:contracts/accounting/explicit_rates_test.go` `TestBuildRecord_ConfigCardCoversTTLViaGenericCacheWrite`,
`TestBuildRecord_ConfigTTLBeatsGenericCacheWrite`, `TestRateSnapshot_OldLineReadsAsNotExplicit`,
`TestBuildRecord_PartialConfigCardIsNotExplicit`; `afm:contracts/accounting/ledger.go`

### BUD-R05 Vendor-reported cost is diagnostic only
`reported_cost_usd` is stored but never used as the estimate; a difference above one cent tags the
record `reported_cost_differs_from_estimate`.

Evidence: `afm:contracts/accounting/ledger_test.go` `TestBuildRecordReportedCostDiffersBeyondTolerance`,
`TestBuildRecordReportedCostWithinTolerance`

### BUD-R06 The usage store is append-only with a health latch
`usage.jsonl` is the single source for check, report, dashboard and budget spend. A torn last line is
tolerated; a corrupt interior line is an error. After a write failure the store latches `unavailable`
and stops writing for that process; reopening reports `ok` again (no historical completeness claim).

Evidence: `afm:contracts/accounting/store_test.go` `TestLoadTornTailTolerated`, `TestLoadCorruptInteriorLineErrors`,
`TestStoreUnavailableAfterWriteFailureStopsWrites`, `TestOpenPreExistingCorruptFileReturnsUsableUnavailableStore`;
`engine:internal/orchestrator/admission_budget_test.go` `TestRecordUsage_ReportsAfterDurableAppend`

#### BUD-S04 Write failure mid-run
- GIVEN an accounting store whose append fails once
- WHEN later records arrive in the same process
- THEN they are not written and health stays `unavailable`

### BUD-R07 Coverage is computed from counts, separately from health
Coverage is `full`, `partial` (priced plus at least one unmetered/unpriced) or `none`, from invocation
counts; `partial` totals display with a `+` suffix and `none` as `—`. Health and coverage are independent.

Evidence: `afm:contracts/accounting/present_test.go` `TestCoverageOf`, `TestDisplayCost`;
`afm:pkg/server/accounting_status_test.go` `TestHandleStatus_HealthOrthogonalToCoverage`

### BUD-R08 Display switches never stop collection
`accounting.enabled` (default on, env `AFM_ACCOUNTING` wins) hides all token/money output;
`accounting.show_money` (default off, env `AFM_ACCOUNTING_SHOW_MONEY` wins) hides money only. Both merge
project over global config. Recording to `usage.jsonl` and budget enforcement continue regardless; the
server omits hidden fields instead of masking them.

Evidence: `afm:contracts/config/config_test.go` `TestAccounting_EnvOverridesConfig`, `TestAccounting_ShowMoneyDefaultsFalse`,
`TestAccounting_ShowMoneyEnvOverridesConfig`, `TestLoadFrom_AccountingShowMoneyMergesAcrossLayers`;
`afm:pkg/server/accounting_status_test.go` `TestHandleStatus_ShowMoney`; `engine:docs/budgets.md` (Money)

#### BUD-S05 Money hidden by default
- GIVEN no `show_money` and no env override
- WHEN `afm check` runs on a run with usage
- THEN `TOKENS`/`CACHE` columns are shown and `EST. COST` is absent

### BUD-R09 `afm check` and `afm report` read the same ledger
`afm check` shows per-stage tokens/cost and a total; `afm report [run] [--format markdown|json] [--stage id]`
is read-only and lock-free, in modes `full`/`no_money`/`time_only`. Both totals are byte-identical with the
dashboard Cost tab for the same run.

Evidence: `afm:cmd/afm/check_test.go` `TestCheckShowsCostColumnsAndTotal`, `TestCheck_ShowMoneyOffKeepsTokensDropsCost`,
`TestCheck_AccountingDisabledHidesCost`; `afm:cmd/afm/report_test.go` `TestReportFormatAndStageFlags`,
`TestReportModesHideTheirData`, `TestReport_AccountingDisabledPrintsTimeOnlyReportExit0`;
`afm:cmd/afm/report_parity_test.go` `TestReportParityWithDashboardCost`

#### BUD-S06 Corrupt ledger
- GIVEN a run whose `usage.jsonl` has a corrupt interior line
- WHEN `afm report` runs in `full` mode
- THEN the report is printed with diagnostics and the exit code is 3

### BUD-R10 Budget spend is derived from usage records
Each record counts once in its own scope and every ancestor scope; retries and loop ticks share a scope;
same-profile helper instances count separately. Tokens are input+output+cache read+cache write
(no reasoning double count). Money is summed in nano-USD per record; unknown money is counted, not summed.

Evidence: `afm:contracts/accounting/budget_spend_test.go` `TestSpendByScope_NestingCountsEachRecordOnce`,
`TestSpendByScope_RetryAndLoopShareScope`, `TestSpendByScope_SameProfileInstancesAreSeparate`,
`TestSpendByScope_TokensDoNotDoubleCountCacheOrReasoning`, `TestSpendByScope_UnknownMoneyIsCountedNotSummed`,
`TestSpendByScope_TenDimesReachOneDollar`

## Errors and rejections
| ID | Condition | Observable result | Evidence |
|----|-----------|-------------------|----------|
| BUD-E01 | Negative or unparseable rate in `pricing:` | Config load fails | `afm:contracts/config/config_test.go` `TestPricingConfig_NegativeRateRejected`, `TestPricingConfig_UnparseableRateRejected` |
| BUD-E02 | Ledger or agent-jobs journal unusable (`full`/`no_money`) | `afm report` prints diagnostics, exit 3, reason on stderr | `afm:cmd/afm/report_test.go` `TestReportExitCodeMatrix` |
| BUD-E03 | Unknown `--format` or `--stage` | `afm report` prints nothing, error | `afm:cmd/afm/report_test.go` `TestReportFormatAndStageFlags` |
| BUD-E05 | Corrupt usage ledger in `time_only` mode | Report printed, exit 0 | `afm:cmd/afm/report_test.go` `TestReportExitCodeMatrix` |
| BUD-E04 | Corrupt `events.jsonl` | `afm report` errors even with accounting display off | `afm:cmd/afm/report_test.go` `TestReport_AccountingDisabledStillErrorsOnBadRun` |

## Coverage gaps and discrepancies
- **BUD-G01** (code≠doc): `afm:docs/accounting.md` names `pkg/accounting` and `pkg/accounting/pricing.go`;
  the package is `afm:contracts/accounting`.
- **BUD-G02** (untested): the dashboard Est. tokens/Est. cost tiles and rail figures are only covered by
  frontend tests not reviewed here; check `pkg/web/dashboard`.
