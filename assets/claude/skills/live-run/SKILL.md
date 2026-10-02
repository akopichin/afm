---
description: Exercise a change in the real running app via the managed Chrome DevTools browser (afm_chrome MCP) — a live run, not unit tests.
allowed-tools: [Bash, Read, Write, Edit]
---

# live-run — Drive the real app in a managed browser

**SCOPE**: Run the product for real and confirm a change works end-to-end, using
the managed Chrome DevTools browser exposed as the MCP server `afm_chrome`. This is
NOT unit testing (that is TDD) — this is launching the actual app and clicking
through it.

This skill assumes the managed browser capability is live. If the `afm_chrome` MCP
tools are not available, the capability is off for this stage — report that and
treat the live run as **not performed** (do not claim success).

## Environment (set by afm)

- `$AFM_BROWSER_APP_URL` — the URL to open in the browser. ALWAYS use this.
  Never open `localhost:<app_port>` inside the container, and never
  `host.docker.internal` — only `$AFM_BROWSER_APP_URL`.
- `$AFM_BROWSER_APP_PORT` — the port the app under test MUST listen on. In Docker,
  bind the server to `0.0.0.0:$AFM_BROWSER_APP_PORT` so the host browser can reach
  the published port; natively `127.0.0.1:$AFM_BROWSER_APP_PORT` is enough (and
  avoids exposing the dev app on your LAN). The `<browser_tools>` block in your
  prompt states the correct bind for the current mode — follow it.
- `$AFM_STAGE_DIR/artifacts/` — write screenshots, console logs and network
  captures here so they show up in the run.

## Step 1: Build the app from the CURRENT sources

Rebuild from the working tree — the pinned image binary does NOT reflect new edits.
For afm itself:

```bash
make build   # rebuilds the frontend bundle before the Go embed, then the binary
```

For another project, use its real build command (check the repo / dev-plan).

## Step 2: Start the app on the managed port

Launch the server in the background (bind address per the mode note above — Docker:
`0.0.0.0`; native: `127.0.0.1`), then wait for it to answer before driving the
browser:

```bash
# example — replace with the real start command.
# BIND: Docker → 0.0.0.0, native → 127.0.0.1 (don't expose a native dev server on the LAN)
BIND=0.0.0.0; [ -z "$AFM_IN_DOCKER" ] && BIND=127.0.0.1
./your-app --addr "$BIND:$AFM_BROWSER_APP_PORT" &
until curl -sf "http://127.0.0.1:$AFM_BROWSER_APP_PORT" >/dev/null; do sleep 0.5; done
```

## Step 3: Drive the browser with the `afm_chrome` MCP tools

Open `$AFM_BROWSER_APP_URL` and walk the exact scenario the change affects (from the
plan in `dev-plan.md` / the stage description). Use the MCP tools to navigate, take
a snapshot, click, fill, and evaluate. Assert the expected result is actually on the
page — do not assume.

## Step 4: Capture evidence

Save at least one screenshot plus the console and network logs into
`$AFM_STAGE_DIR/artifacts/`. Reference a screenshot in your narrative with the
image marker so it renders in the feed:

```
[AFM image: live-run.png]
```

## Step 5: Verdict

- Scenario passed and evidence captured → report success with the artifacts.
- Browser unavailable, app failed to start, or the scenario did not produce the
  expected result → this is a **FAILED** check. Say so plainly; do not paper over
  it. Clean up any server you started.
