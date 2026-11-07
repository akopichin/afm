# SDK core mode

Stage 3 of the new-AFM plan. The Go SDK starts and controls `afm run --mode core`.
User behavior: [SDK](../../sdk.md#core-mode), [Core mode](../../core-mode.md).
Builds on [Core mode design](2026-11-07-core-mode-design.md).

## Decisions

- **D1.** `Config.Mode`: empty and `ModeDashboard` pass no `--mode`, so older
  binaries keep working; `ModeCore` adds `--mode core`; `New` rejects unknown modes.
- **D2.** `Start(ModeCore)` returns only when the process is alive, `/api/runtime`
  says `core`, protocol 1, `ready`, `/api/status` has the same `run_id`, and
  `<workDir>/.afm/runs/<run_id>` exists (guards against a foreign core that took
  the port). Any failure stops and reaps the process, frees the slot, keeps `workDir`.
- **D3.** `Connect`/`Attach` read `/api/runtime`; 404 means an older afm (mode empty);
  an unknown mode or protocol is `ErrIncompatibleRuntime`.
- **D4.** `WaitResult` returns the durable result of the run seen after `ready`:
  a failed stage under a running workflow is not final, another `run_id` is
  `*RunChangedError`, a lost API is `ErrResultUnavailable`.
- **D5.** `Shutdown` is for the owned process only: SIGINT, then kill after `ctx`
  (30 s default) with `ErrKilled`; exit 0 is `nil`, otherwise `*exec.ExitError`.
- **D6.** `Wait` and `Cleanup` keep their meaning; on handles that do not own the
  process they match `ErrNotOwned`.
- **D7.** Output is a bounded tail (4 KiB); `cmd.WaitDelay` is 5 s.
- **D8.** `engine.Run.Ready()` also requires the saved `BeginRun` of this process,
  otherwise a resumed run is `ready` while `/api/status` shows the old result.
- **D9.** Timeouts are package variables for unit tests; no public settings.

## Manual Docker check (not run in CI)

Docker is unavailable in the live-run container, so this runs on the host before
merge. With `docker.enabled: true`, start a core through the SDK
(`Config{Mode: ModeCore}`) against a fixture flow. Expect `-p 127.0.0.1:<p>:<p>`
in `docker ps`, `/api/runtime` with `mode: core` and `ready: true`, `GET /`
returning 404, `WaitResult` returning the final status, and `Shutdown` returning
`nil` (exit 0) after the container stops. Also check that a killed process
(`ErrKilled`) leaves the container, which the SDK cannot remove.
