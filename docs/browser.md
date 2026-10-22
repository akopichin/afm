# Managed browser (Chrome DevTools)

afm can drive a real Chrome on your host so a stage agent can run the app it just
built **for real** — click through it, read the console and network, take a
screenshot — using the [Chrome DevTools MCP](https://github.com/ChromeDevTools/chrome-devtools-mcp).
This is the "живой прогон" (live run) a human would otherwise do by hand.

It works both when afm runs natively and when it re-executes into Docker: the agent
and the MCP stay on the same side as the agent, and afm owns a small host-side
helper that launches and proxies to Chrome. You never start Chrome, enable Remote
Login, or open a CDP port yourself.

## Enabling

Add a `browser:` block to `config.yaml`:

```yaml
browser:
  enabled: true       # omitted → auto; true → on; false → fully off
  headless: false     # default: a visible window
  app_port: 18080     # the port your app is served on (must differ from server.port)
  # executable_path: "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
```

`enabled` is a tri-state:

| Value | Meaning |
|-------|---------|
| *(omitted)* | **auto** — offer the browser tools when the host supports them (Chrome present, and on native runs the pinned MCP installed); degrade silently otherwise so CLI/API-only flows are unaffected. |
| `true` | **on** — the host preflight must succeed or the run fails (no silent skip). |
| `false` | **off** — the capability is disabled entirely. |

The env var `AFM_BROWSER=1` / `AFM_BROWSER=0` overrides the config for a one-off run.

A single stage can opt out with `browser: false` in the flow:

```yaml
stages:
  - id: live-run
    browser: false   # this stage gets no afm_chrome tools
```

Chrome launches **lazily** — only on the agent's first actual browser tool call, so
just offering the tools never opens a window. Its lifetime ends with the run.

## Which agents can use it

The managed browser is wired for agents that speak the MCP tool-loop: the
**claude** family (bare `claude` or a `docker.agents` recipe of type claude) and the
**codex** adapter. Text-only / single-bash-tool adapters (openai, cursor) are not
MCP clients and don't receive it.

## How a stage uses it

When the capability is active, a normal agent invocation gets an MCP server named
**`afm_chrome`** plus two environment variables:

```text
AFM_BROWSER_APP_PORT=18080
AFM_BROWSER_APP_URL=http://127.0.0.1:<assigned-host-port>
```

Open `$AFM_BROWSER_APP_URL` in Chrome — not the container's own `localhost:18080`.
In **Docker**, serve the app on `0.0.0.0:$AFM_BROWSER_APP_PORT` so the published
host port can reach it; afm publishes that port and hands you the resulting URL. In
a **native** run, bind `127.0.0.1:$AFM_BROWSER_APP_PORT` (the app and Chrome share
the host — no need to expose the dev server on the LAN). Screenshots and other
artifacts the MCP writes land under the stage directory. The bundled `afm-live-run`
skill automates this flow (build from source → wait for health → walk the scenario
→ capture evidence).

## Supported hosts

The first release targets **macOS + Docker Desktop + Chrome**, and native macOS
runs. Linux-Docker and Windows need their own verification and are not claimed yet.

For a native run, the pinned `chrome-devtools-mcp` must already be installed on
`PATH` (the Docker image ships it). The version must match exactly — afm never runs
`@latest` and never installs it for you.

## Isolation and limits

- **Verify and helper agents never get the browser.** Read-only AI verify, JSON-fix,
  and the memory pipeline do not receive `afm_chrome` or its transport token — a
  read-only *filesystem* would not stop an MCP from mutating an external app.
- **Each stage gets its own Chrome and profile.** Two stages running in parallel
  never share a browser; a retry/resume of the same stage reconnects to its own.
- **The transport token stays in `0600` files only.** It is passed to the container
  via a bare `-e` (no value in `docker run` argv) and written only to per-call
  `0600` files — the afm descriptor and the private Chrome-DevTools MCP config
  (whose `wsHeaders` carries the bearer header the WebSocket needs). It is stripped
  from afm's own environment once consumed, and never appears in argv, the agent's
  environment, the claude `.mcp.json`, or logs.
- **The container is the trust boundary, not the process.** afm keeps the token out
  of argv/`ps`, out of child environments, and out of the claude config — but a
  process running as the *same user inside the same container* is not isolated from
  it (it could read another stage's `0600` descriptor, or the token still visible in
  PID 1's initial `/proc/1/environ`). That is afm's deliberate model: stages and
  verify share one container and are mutually trusted. If you need a stage that must
  not be able to reach the managed browser at all, isolate it at the container level.
- **Cleanup on hard kill is best-effort.** A normal exit, error, or Ctrl-C closes
  Chrome and removes its profile. A `SIGKILL` of afm can leave a profile behind; the
  next start sweeps profiles whose owning process is provably gone.
- **One app port.** The first release publishes a single app port and hands you one
  URL; arbitrary multi-service port maps are a later addition.
