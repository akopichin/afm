# Run lifecycle: afm run composition, modes and exit codes

> Baseline: afm `462bd5d4` · afm-engine `v1.2.1` (`11bddf2c`). Status: draft baseline.

## Scope
- How `afm run` (`afm:cmd/afm/run.go`) validates its command line before anything is created, composes the
  engine run with transports, holds after the result per mode (`dashboard`/`core`), and maps outcomes to exit
  codes (`afm:cmd/afm/exit.go`).
- Part of the run-lifecycle family: engine ownership — [run-lifecycle](../run-lifecycle/spec.md); resume and
  offline control — [run-lifecycle-resume](../run-lifecycle-resume/spec.md).
- Out of scope: HTTP routes and readiness endpoint ([http-api](../http-api/spec.md)), MCP
  ([mcp-serve](../mcp-serve/spec.md)), Docker re-exec ([execution-environment-docker](../execution-environment-docker/spec.md)),
  `repeat` and flow format ([flow-config](../flow-config/spec.md)).

## Terms
- **Hold** — keeping the HTTP server up after `Wait` returned so clients can read the final state.
- **Silent exit** — `ExitError{Silent: true}`: the process exits with the code without a usage dump.

## Requirements

### RUN-R40 Refusals happen before anything is created
`afm run` rejects, before any `.afm/runs`, worktree, container, run or agent exists: contradictory pipeline
flags, an invalid `AFM_EXIT_ON_FAIL` value (without echoing the value), an unknown `--mode`, `--mode core`
without a positive port, invalid MCP flags, and a refused `--task` (empty, over 1 MiB, URL with credentials,
unreadable file). Invalid `--worktree` names are rejected before worktree resolution.

Evidence: `afm:cmd/afm/run_mode_test.go` `TestRunMain_RefusedModeCreatesNothing`;
`afm:cmd/afm/run_exit_on_fail_test.go` `TestExitOnFailInvalidEnvPreflight`; `afm:cmd/afm/run_task_test.go`
`TestRunTask_RefusalsCreateNothing`; `afm:cmd/afm/run_mcp_test.go` `TestRunMCPRefusedFlagsCreateNothing`;
`afm:cmd/afm/run.go` `newRunCmdWith`; `afm:docs/core-mode.md`.

#### RUN-S40 Core without API
- GIVEN `server.port: 0` in config
- WHEN `afm run --mode core flow.yaml` runs
- THEN it exits non-zero with `--mode core needs the HTTP API`, no `.afm/runs` exists and no stage ran.

### RUN-R41 Launch modes are explicit
`--mode` accepts `dashboard` (default, also for empty) and `core`; core serves the HTTP API without the Web UI.
afm derives the engine policies `HumanControl` and `AssistantAvailable` from a positive API port, never from the
mode; the mode is reported to clients as is.

Evidence: `afm:cmd/afm/run_mode_test.go` `TestParseLaunchMode`, `TestLaunchModeValidate`;
`afm:cmd/afm/run_core_lifetime_test.go` `TestDockerReExec_CarriesModeAndPort`; `afm:cmd/afm/run_mode.go`;
`afm:cmd/afm/run.go` `executeFlow`.

### RUN-R42 Composition order around the engine
For each pass `executeFlow` opens the workspace, calls `engine.Open`, starts the HTTP server, calls `Start`,
waits with a non-cancelling ctx, holds, then `Shutdown` (while the server is up), server shutdown, and `Close`
last. A `Start` failure or `Open` failure still shuts down/closes what was opened. Interrupt via the signal ctx
passed to `Start` cancels the run; `Wait` still reports the interrupted result.

Evidence: `afm:cmd/afm/run_core_lifetime_test.go` `TestCoreChild_InterruptDrainsActiveAssistantTurn`;
`afm:cmd/afm/run.go` `executeFlow`; `afm:AGENTS.md` "Composition and shared policies".

### RUN-R43 Hold after the result depends on the mode
After a non-failed result: core prints `api: holding the final state until interrupted` and holds until SIGINT,
then exits 0; dashboard holds at least 5 s and while UI clients stay connected, at most 2 min; between `repeat`
cycles both hold the short inter-cycle grace and reuse the port. A failure under exit-on-fail, an error result
or a budget-held run without API does not hold.

Evidence: `afm:cmd/afm/run_core_lifetime_test.go` `TestExecuteFlow_CoreHoldsUntilContextCancel`,
`TestCoreChild_InterruptInFinalHoldExitsZero`, `TestCoreChild_RepeatTwoCyclesThenFinalHold`,
`TestExecuteFlow_CoreExitOnFailDoesNotHold`, `TestCoreChild_ExitOnFailExitsOneWithoutHold`;
`afm:cmd/afm/run_dashboard_drain_test.go` `TestWaitForDashboardDrain_NoClients_ReturnsAfterMinGrace`,
`TestWaitForDashboardDrain_StopsAtMaxGraceIfClientNeverDisconnects`;
`afm:cmd/afm/run_exit_on_fail_test.go` `TestExitOnFailDashboardDrain`; `afm:cmd/afm/run.go` `holdAfterResult`.

#### RUN-S41 Interrupt during the final core hold
- GIVEN a core run that reached its result and is holding
- WHEN SIGINT arrives
- THEN the process drains and exits 0.

### RUN-R44 Exit codes
`afm run` exits 0 on a completed run; 1 (silent, one stderr line naming stage, reason and run dir, joined
persistence errors on extra lines) when a committed stage failure stopped the run — a committed failure wins
over a concurrent SIGINT; 130 with `interrupted — run afm run again to resume` on SIGINT cancellation outside the
final hold; 75 when a run without API is held by a budget (lists held scopes and how to continue); 1 with usage
for other errors. Under Docker the container's exit code is propagated.

Evidence: `afm:cmd/afm/run_exit_on_fail_test.go` `TestExitOnFailExitError`, `TestExitOnFailSignal`,
`TestExitOnFailJoinedError`, `TestExitOnFailSingleLineReason`; `afm:cmd/afm/exit_test.go`
`TestRunExitError_InterruptIsNotAUsageError`, `TestRunMain_ExitErrorSilentSuppressesPrinting`;
`afm:cmd/afm/run_core_lifetime_test.go` `TestCoreChild_InterruptBetweenCyclesExits130`;
`afm:cmd/afm/budget_integration_test.go` `TestBudgetIntegration_HeldRunWithoutAPIExits75`;
`afm:cmd/afm/exit.go` `runExitError`; `afm:cmd/afm/main.go` `runMain`; `afm:cmd/afm/run.go` `budgetHeldExit`.

#### RUN-S42 Exit on fail
- GIVEN `AFM_EXIT_ON_FAIL=1` and a script stage that exits 7
- WHEN `afm run --port=0` runs
- THEN the exit code is 1, stderr has exactly one `afm: stage` line with `exit status 7` and the run dir, no
  usage, and the journal records `flow_started`, `flow_failed` and the stage `failed`.

### RUN-R45 Exit-on-fail resolution and engine behaviour
`--exit-on-fail` overrides `AFM_EXIT_ON_FAIL` (`1/true/yes/on`, `0/false/no/off`, empty = off). With it the first
stage committed as `failed` cancels the run and waits for agents; `retrying`, `hook_failed` and a CAS-miss do not
stop it; a resumed run with an already failed stage and `auto_recover: false` stops before starting agents.

Evidence: `afm:cmd/afm/run_exit_on_fail_test.go` `TestExitOnFailResolver`, `TestExitOnFailDefault`,
`TestExitOnFailNonTerminalBoundaries`, `TestExitOnFailRepeat`; `engine:internal/orchestrator/exit_on_fail_test.go`
`TestExitOnFailCommittedTransition`, `TestExitOnFailRetry`, `TestExitOnFailStartupBeforeAgentStarts`,
`TestExitOnFailStartupAutoRecover`; `engine:internal/orchestrator/exit_on_fail_internal_test.go`
`TestExitOnFailCASMissIsNotAFailure`; `afm:docs/stage-lifecycle.md` "Exit on stage failure".

### RUN-R46 New run versus resume from the CLI
`afm run` resumes by default. `--task`, `--worktree-clear` and `--worktree-clear-force` force a new run; a task is
saved as `task.md` (0600) in the run directory and reused on resume and in every repeat cycle.

Evidence: `afm:cmd/afm/run_task_test.go` `TestRunTask_ReachesAgentAndRunDir`, `TestRunTask_ResumeKeepsTask`,
`TestRunTask_RepeatCarriesTask`; `afm:cmd/afm/run.go` (`forceNewRun`); `afm:docs/core-mode.md` "Initial task".

## Errors and rejections
| ID | Condition | Observable result | Evidence |
|----|-----------|-------------------|----------|
| RUN-E40 | `--mode` not dashboard/core | `--mode "x": want dashboard or core`, nothing created | `TestRunMain_RefusedModeCreatesNothing` |
| RUN-E41 | core with port ≤ 0 | `--mode core needs the HTTP API…` | same |
| RUN-E42 | invalid `AFM_EXIT_ON_FAIL` | error naming the variable, value not echoed | `TestExitOnFailInvalidEnvPreflight` |
| RUN-E43 | `--listen-any-port` with `--port` | error before run | `afm:cmd/afm/run.go` (untested) |
| RUN-E44 | stage failure under exit-on-fail | exit 1, silent | `TestExitOnFailExitError` |
| RUN-E45 | budget hold, no API | exit 75 | `TestBudgetIntegration_HeldRunWithoutAPIExits75` |

## Coverage gaps and discrepancies
- **RUN-G40** (doc-only): only SIGINT is a clean stop; SIGTERM does not drain (`afm:docs/core-mode.md`). Code
  registers only `os.Interrupt` (`signal.NotifyContext`), no test asserts SIGTERM behaviour.
- **RUN-G41** (untested): the exact composition order of `executeFlow` (Shutdown before server shutdown, Close
  last) is code-only; tests observe effects (drained Assistant turn) but not ordering.
- **RUN-G42** (untested): `--listen-any-port` combined with `--port` refusal.
