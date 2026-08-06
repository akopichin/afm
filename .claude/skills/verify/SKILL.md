---
name: verify
description: Build the afm Docker image and drive a real flow through the dashboard to observe orchestrator behavior end-to-end
---

# Verifying afm end-to-end

Build → run a flow.yaml with a mock agent (no real API calls) → drive the
dashboard through the browser or curl. This exercises the real Go
orchestrator + pkg/server + dashboard, not just `go test`.

## Build

```bash
make docker-build   # tags akopichin/afm:latest from Dockerfile.runtime, local single-arch
```

Sanity check: `docker run --rm akopichin/afm:latest --version` (note: the
entrypoint IS `afm`, so pass flags directly — `afm afm --version` is wrong).

## Mock agent instead of real `claude`

Real `claude` needs `CLAUDE_CODE_OAUTH_TOKEN`/`ANTHROPIC_API_KEY` inside
Docker (macOS Keychain isn't reachable from the Linux container) — skip
this entirely for orchestrator-only smoke tests with a bash mock agent that
speaks claude's stream-json protocol:

```bash
jq -nc --arg t "some text" '{type:"assistant",message:{content:[{type:"text",text:$t}]}}'
```

Phase is inferred from context afm itself provides, not from prompt wording:
- **implementation**: the prompt always embeds a literal
  `Stage directory for .done file: <path>` line
  (`pkg/orchestrator/agents.go` `runImplementationAgent`) → write `.done` there.
- **autonomous** (`agents: [auto]`): `AFM_STAGE_DIR` env var is set for this
  phase and the prompt mentions `execution_summary.md` → write it there.
- **interactive dialog** (any phase with `AFM_STAGE_DIR` set, i.e.
  `interactive: true` stages): write `<phase>.<id>.question.json` into
  `$AFM_STAGE_DIR`, poll for `<phase>.<id>.answer.json`, then proceed.
- **default** (non-interactive planning/review): just emit assistant text
  with `## Tasks` / `## Assumptions` / `## Acceptance Criteria` headings —
  `RunPlanning` writes the collected text to `plan.md` itself when no
  `Write` tool_use event matches; `RunAgent` (review) ignores the text
  entirely.

One unified script covers every stage type — see the phase-inference order
above (check implementation's marker first, then autonomous's, then
interactive, then default).

## Project-level `.afm/config.yaml` for a smoke-test project dir

```yaml
client:
  command: mock-agent   # avoids the CheckClaudeDockerAuth gate entirely —
                         # that check fires whenever the DEFAULT client
                         # command is "claude", even if every stage
                         # overrides `command:` itself
docker:
  enabled: true
  image: akopichin/afm:latest   # overrides ~/.afm/config.yaml's pinned tag
server:
  open_browser: false
```

`mock-agent` must be on `PATH` when invoking `afm run` — Docker mode does
`which <cmd>` on the **host** to find and mount non-standard agent binaries
into the container (`command:` in flow.yaml must be a bare name, not an
absolute path).

## Gotcha: don't share a project dir across concurrent runs

Docker mode mounts `$(pwd)` into the container 1:1. If you `rm -rf .afm/runs`
in the same directory while an earlier Docker container from a *different*
test run is still alive (e.g. testing sequentially without waiting for full
exit), you delete the live run's directory out from under it — the running
process logs `snapshot write failed: ... no such file or directory` and the
stage goes `failed`. Not an orchestrator bug — it's the test harness racing
itself. Use a dedicated directory per concurrent run, or fully stop/wait for
the previous container before reusing a directory.

## Known pre-existing (not a regression) dashboard quirk

On a **fresh page load** of a stage already sitting in `awaiting_approval`
(or similar past-planning states) at mount time, `PlanPanel.tsx`'s
`loadPlan()` effect sometimes never fires `GET /api/stages/<id>/plan` —
"No plan yet" shows even though the API endpoint itself returns the correct
content when curled directly. This blocks the Approve/Revise buttons in
that view (which render conditionally on plan content being loaded).
Confirmed present at commit `8996919` (pre-orchestrator-split) too, so it's
not caused by any particular refactor — verified by running the same flow
on a `git worktree` checkout of the earlier commit.

Workaround while driving a flow: approve via
`curl -X POST localhost:<port>/api/stages/<id>/approve` — same endpoint the
button calls. The bug does NOT appear when a stage transitions to
`awaiting_approval` *while the page is already open* (e.g. after answering
an interactive dialog in the same session) — only on a fresh load landing
directly on an already-past-planning stage.

## Driving the dashboard

- `GET /api/status` — stage statuses, polled by the frontend every ~2s.
- `GET /api/stages/<id>/plan` — plan.md text.
- `GET /api/stages/<id>/dialog` — pending question (file-based dialog protocol).
- `POST /api/stages/<id>/approve` — no body.
- `POST /api/stages/<id>/dialog/answer` — `{"phase":"planning","id":"q1","answer":"...","from_options":true}`
  (note: **not** `/answer`, it's `/dialog/answer`).

Chrome DevTools MCP tools work fine against `http://localhost:<port>` once
the container's port is up (`docker ps` to confirm the `-p` mapping).
