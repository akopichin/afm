# Core mode: `afm run --mode core`

Stage 2 of the new-AFM plan. The same binary runs the engine and the HTTP API
without the Web UI. User behavior: [Core mode](../../core-mode.md). Out of scope:
SDK core mode (stage 3).

## Decisions

- **D1.** `--mode dashboard|core` is validated before the run is created; core
  without a positive port is an error.
- **D2.** Web UI serving moves to `pkg/web` (`web.NewHandler`); `server.Config`
  gets an optional `UI`. `pkg/server` does not import `pkg/web`.
- **D3.** `GET /api/runtime`: `protocol_version`, `mode`, `run_id`, `ready`.
- **D4.** `engine.Run.Ready()` backs `ready`.
- **D5.** Execution policies are derived from the presence of the API, not the mode.
- **D6.** Core prints `api: <url>` instead of the dashboard block; no tab, no host opener.
- **D7.** Core holds the API until SIGINT. Only SIGINT is a clean stop; SIGTERM does not drain.
  Stop sequence: SIGINT cancels the run context and releases the final hold; the
  final result is already persisted. `run.Shutdown(ctx)` is called with that
  cancelled context on purpose (no `context.WithoutCancel`), so an active
  Assistant turn is cut after the bounded emergency drain (5 seconds). The
  deferred `Shutdown(context.Background())` then drains Assistant and bridges
  before the HTTP server and the run are closed.
- **D8.** In repeat, only an explicitly final core hold turns the owner's interrupt into exit 0.
- **D9.** Docker re-exec needs no new transport logic; the mode is passed through.

## Manual Docker check (not run in CI)

Docker is unavailable in the live-run container, so this runs on the host before
merge: `afm --dir <fixture> run --mode core --port <p> <flow>` with
`docker.enabled: true`. Expect `-p 127.0.0.1:<p>:<p>` in `docker ps`,
`/api/runtime` with `mode: core` and `ready: true`, `GET /` returning 404, no tab
opened, and SIGINT stopping the container with exit code 0.
