# Core mode: HTTP API without the dashboard

`afm run --mode core` runs the same engine as the default mode and serves the
same HTTP API, but mounts no Web UI. Use it when another program (for example
the [Go SDK](sdk.md)) drives the run and nobody needs the dashboard.

```bash
afm run --mode core --port 9876 path/to/flow.yaml
```

`--mode` accepts `dashboard` (the default) and `core`. Any other value is
refused before the run starts.

## Port is required

Core exists to expose the API, so it needs a positive port from `--port` or
`server.port`. `--port=0` or `server.port: 0` with `--mode core` fails at startup
with an error and creates no run. If the port is already taken, the process also
exits with an error instead of running without an API.

## What differs from the dashboard

- `GET /` and the other UI paths return `404`; every `/api/*` route and `/ws`
  work as in the dashboard.
- The console prints `api: http://localhost:<port>` instead of the dashboard
  block, and afm never opens a browser tab (`server.open_browser` is ignored).
- Stages that need a human still wait for one: answer questions and approve
  plans through the [API](dashboard.md) (or the SDK) instead of the page.

## Runtime and readiness

`GET /api/runtime` tells a client which mode it is talking to and whether the run
can accept work:

```json
{"protocol_version": 1, "mode": "core", "run_id": "my-flow-20261107-100000-ab12", "ready": true}
```

`ready` turns `true` once the workflow of this process has started (for a resumed
run, once its new start is saved) and stays `true` until shutdown begins, so a
resumed run never shows the previous process's result while `ready` is `true`. A
client should poll until `ready` is `true`; the SDK does this for you with
[`ModeCore`](sdk.md#core-mode). The dashboard mode
reports the same fields with `"mode": "dashboard"`. If the run fails after
`ready` became `true`, the process still exits, so a client must also handle the
connection dropping.

## Lifetime

- After the run reaches its result, core keeps the API up so a client can read
  the final state, and prints `api: holding the final state until interrupted`.
  It stops only on interrupt (`Ctrl+C` / `SIGINT`) and exits with code `0`.
- A run held by a [budget limit](budgets.md) stays open: update the limit and
  continue through `POST /api/budgets/update` with `resume: true`.
- A failed stage does not stop the process: you can inspect it and use **Retry**
  through the API. With `--exit-on-fail` the run stops and the command exits with
  code `1`, as in the dashboard mode.
- With [`repeat`](flow-reference.md#repeat-run-successful-flows-again) the API
  stays on the same port between cycles (a short pause, then the next cycle). The
  last cycle is held until interrupt.
- **Only `SIGINT` is a clean stop.** `SIGTERM` (for example from `docker stop` or
  a process supervisor) does not drain the run. Configure the supervisor to send
  `SIGINT`, or accept that the run is stopped abruptly and resumed on the next
  `afm run`.

Restarting `afm run --mode core` on the same flow resumes the run as usual; see
[Stage lifecycle](stage-lifecycle.md).

## Docker

With `docker.enabled: true` core re-executes into the container like any run, and
the port is published as `-p 127.0.0.1:<port>:<port>`. The host never opens a
tab. See [Docker](docker.md).

## Security

Natively the API listens on `127.0.0.1` only. In Docker it listens on `:<port>`
inside the container, and the port is published only on the host loopback. Mutating browser requests need a matching
`Origin`, and owner and agent routes require a bearer token. Remote access is not
supported; use an SSH tunnel. See [Dashboard](dashboard.md#assistant-a-free-form-chat-alongside-the-run).
