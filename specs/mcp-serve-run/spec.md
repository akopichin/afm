# `afm run` side of serve: registry record and `--task`

> Baseline: afm `462bd5d4` · afm-engine `v1.2.1` (`11bddf2c`). Status: draft baseline.

## Scope
- The run registry `~/.afm/active-runs` written by `afm run` (`afm:pkg/runregistry`, `afm:cmd/afm/run_discovery.go`).
- The `--task` flag of `afm run` (`afm:cmd/afm/run_task.go`), used directly, by the SDK and by `afm serve`.
- Out of scope: the host that reads the registry and launches runs (`../mcp-serve-host/spec.md`); `/mcp`
  (`../mcp-serve/spec.md`). Part of the `MCP` family; IDs continue the shared numbering.

## Terms
- **instance_id**: per-process id, also reported by `/api/runtime`; names the registry record.

## Requirements

### MCP-R20 `afm run` with an API publishes a secret-free registry record
A run serving an API writes `~/.afm/active-runs/<instance_id>.json` (dir 0700, file 0600) with pid, start marker,
mode, project, API URL, current `run_id` and trimmed earlier repeat ids; an early record has no endpoint; the record is
updated with the run, heartbeated, and removed on exit (only its own). A process inside the container does not register;
the Docker host registers the published endpoint. Refused flags create no record.

Evidence: `afm:pkg/runregistry/store_test.go` `TestPublishRoundTripAndPermissions`, `TestPublishTrimsPreviousRunIDs`,
`TestRemoveOnlyOwnRecord`, `TestListSkipsGarbage`; `afm:cmd/afm/run_discovery_test.go` `TestRunRegistrar_EarlyRecordHasNoEndpoint`,
`TestRunRegistry_NativeRunPublishesAndRemovesItsRecord`, `TestRunRegistry_RepeatKeepsHistory`, `TestRunRegistry_ContainerDoesNotRegister`,
`TestRunRegistry_DockerHostRegistersPublishedEndpoint`, `TestRunRegistry_RefusedFlagsCreateNothing`

### MCP-R33 `--task` adds a task to a new run, validated before anything is created
`--task` takes text, `@file`, `@-` or an `http(s)` URL answering 200 `text/*`. Empty/blank, missing file, > 1 MiB, invalid UTF-8,
URL with userinfo, non-text, HTTP error or redirect to a file are refused before a run, worktree or container exists; fetch errors
mask the query. The task always starts a new run, reaches every agent prompt, is saved as `task.md` (0600) and kept by resume and
repeat cycles.

Evidence: `afm:cmd/afm/run_task_test.go` `TestLoadTask_Sources`, `TestLoadTask_Refusals`, `TestLoadTask_URLErrorsMaskQuery`,
`TestRunTask_RefusalsCreateNothing`, `TestRunTask_ReachesAgentAndRunDir`, `TestRunTask_ResumeKeepsTask`, `TestRunTask_RepeatCarriesTask`;
`TestServe` ("start with a task, duplicates and admission")

#### MCP-S23 resume keeps the task
- GIVEN a run started with `--task @task.md`
- WHEN `afm run` is invoked again on the flow without `--task`
- THEN the resumed run keeps the task from `task.md`

## Errors and rejections
| ID | Condition | Observable result | Evidence |
|----|-----------|-------------------|----------|
| MCP-E27 | invalid `--task` | error before run creation | `TestRunTask_RefusalsCreateNothing` |
| MCP-E28 | invalid `instance_id` on publish | publish refused | `TestPublishRejectsInvalidInstanceID` |

## Coverage gaps and discrepancies
- **MCP-G22** (ambiguous): inline `--task` text is visible in the `afm run` argv; only SDK (stdin) and host (file) avoid it.
