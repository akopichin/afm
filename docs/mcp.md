# MCP: drive a run from an AI client

`afm run --mcp` serves the [Model Context Protocol](https://modelcontextprotocol.io)
on `/mcp` of the run's HTTP API. An MCP client (for example Claude Code) can
read the run, see what needs a person, answer questions and approve plans
without the dashboard.

Whenever the run has an HTTP API, `/mcp` is on and **every permission group is
allowed**. Narrow it with `--mcp-allow`, or turn it off with `--mcp-off`.

!!! warning "Changed default"
    Earlier builds served `/mcp` only with `--mcp`, and `--mcp` alone was read-only.
    Now `/mcp` is on by default with all groups, and `--mcp` alone no longer limits
    anything. Pass `--mcp-allow read` to keep the old read-only behavior, or `--mcp-off`.

```bash
# default: /mcp with all groups next to the API
afm run --mode core --port 9876 flow.yaml

# read-only
afm run --mode core --port 9876 --mcp-allow read flow.yaml

# only answer questions and approve the exact plan the client has read
afm run --mode core --port 9876 --mcp-allow=reply,approve flow.yaml

# no MCP
afm run --mode core --port 9876 --mcp-off flow.yaml
```

To follow all runs of the machine from one client, use [`afm serve`](serve.md).

## Enabling

| Flag | Meaning |
|---|---|
| *(none)* | `/mcp` is mounted on the HTTP API port with all groups. With port `0` there is no API and no MCP. |
| `--mcp` | Same as the default; refused before a run is created when there is no API (`--port 0`). |
| `--mcp-allow=<groups>` | Comma-separated groups: `reply`, `approve`, `stages`, `budget`, or `read` (also an empty value) for read-only. An unknown name is refused before a run is created. There is no `all`: omit the flag for every group. |
| `--mcp-off` | Do not serve `/mcp`. Cannot be combined with `--mcp` or `--mcp-allow`. |

Works in both [launch modes](core-mode.md): the endpoint is on the same port as
the API (`core`) or as the API and Web UI (`dashboard`). With `--mcp-off`,
`/mcp` answers `404 {"error":"mcp_disabled"}` (never the dashboard's HTML).
When the run finishes, core holds the API (and `/mcp`) until interrupted, as
described in [Core mode](core-mode.md#lifetime).

### Permission groups

| Group | Tools it adds |
|---|---|
| *(always on)* | `afm_run_status`, `afm_run_wait`, `afm_events_read`, `afm_artifact_read` |
| `reply` | `afm_interaction_reply` |
| `approve` | `approve` decision of `afm_stage_decide` |
| `stages` | `revise` and `retry` decisions of `afm_stage_decide`, `afm_stage_pause`, `afm_stage_continue` |
| `budget` | `afm_budget_adjust`, `afm_budget_resume` |

A tool whose group is off is not listed (and not available to the run's own agents either). A call that needs a group that is off
fails with `not_allowed`.

## Tools

| Tool | Purpose |
|---|---|
| `afm_run_status` | Run status, `attention_required`, every pending action (question, approval, failure, budget hold) with the allowed actions and resources, stage summaries, artifact ids and a `cursor`. |
| `afm_run_wait` | Block until the attention model changes from a cursor, or until `timeout_seconds` (default 30, at most 60). Returns the fresh status and `changed`; `reset: true` means the cursor belonged to another run. Cancelling the request does not affect the run. |
| `afm_events_read` | Stage transitions of the run journal in pages ordered by `seq`; pass the previous `next_seq`. Default 100, at most 500 per page. |
| `afm_artifact_read` | Read an artifact by the opaque `artifact_id` listed by `afm_run_status`. |
| `afm_interaction_reply` | Answer the question a stage waits on (`phase` and `question_id` come from its pending action). |
| `afm_stage_decide` | `approve` (with `expected_revision`), `revise` (with `feedback`) or `retry`. |
| `afm_stage_pause`, `afm_stage_continue` | Pause or continue a stage. |
| `afm_budget_adjust`, `afm_budget_resume` | Set or remove the limit of a [budget](budgets.md) scope, or continue a scope held by its limit. |

Every control call takes the `run_id` from `afm_run_status`. A call with the id of
another run (for example after a [repeat](flow-reference.md#repeat-run-successful-flows-again)
cycle) fails with `run_changed` and changes nothing. A successful control call
returns `applied` and the stage state after the call.

Costs are available as the `run cost` resource: tokens and coverage always, money
only when `accounting.show_money` is on (see [Cost accounting](accounting.md)).

### Resources

| URI | Content |
|---|---|
| `afm://runs/<run_id>/status` | Same as `afm_run_status`; subscribable. |
| `afm://runs/<run_id>/cost` | Token and cost totals; subscribable. |
| `afm://runs/{run_id}/stages/{stage_id}/plans/{revision}` | Plan at the revision a client reviewed; a revision that is no longer current is not found. |
| `afm://runs/{run_id}/artifacts/{artifact_id}` | Artifact: first text page or an image. |

### Errors

A failed tool call has `isError: true` and a structured body
`{"code", "kind", "message"}`; clients should branch on `code`. Failures that REST
also reports keep the REST code (`not_found`, `conflict`, `invalid`,
`approval_stale`, `approval_run_changed`, `approval_unavailable`, …). Codes that
exist only in MCP:

| Code | Meaning |
|---|---|
| `not_allowed` | The required `--mcp-allow` group is off. |
| `run_changed` | The `run_id` is not the run served now. |
| `mcp_closed` | The service is shutting down. |
| `mcp_session_limit` | Too many open MCP sessions (16). |
| `question_not_pending` | The question was already answered or is no longer asked. |
| `wait_capacity` | Too many `afm_run_wait` calls are parked (8). |

## Approving a plan

Approval is strict: a client may approve only the exact plan revision it read.

1. `afm_run_status` lists an `approval` pending action with the stage and
   `revision`.
2. `afm_artifact_read` of the plan returns `text` and the `revision` of the
   plan's original bytes (also when secrets in the text are masked).
3. `afm_stage_decide` with `decision: approve` and `expected_revision` set to that
   revision. It is applied only if the revision is still current.

If the plan was revised in between, the call fails with `approval_stale` and the
stage keeps waiting: read the new plan and decide again. Two concurrent approvals
of one revision apply once; the second gets `approval_stale`. MCP never falls
back to the legacy approve that accepts any waiting plan.

If a response is lost, call `afm_run_status` before retrying. `afm_interaction_reply`
and `revise` are not idempotent: an applied answer fails the second time with
`question_not_pending`, and a repeated `revise` sends the feedback twice.

The same operation is available over REST and in the [Go SDK](sdk.md#conditional-approval).

## Waiting and notifications

`afm_run_wait` is the reliable way to follow a run: pass the last `cursor` and it
returns when something that needs a person appears or is resolved. A cursor that is
already stale returns at once.

Clients may also subscribe (`resources/subscribe`) to the status and cost
resources and receive `notifications/resources/updated`. Delivery is best effort:
bursts are coalesced and an event may be skipped, so treat a notification as a
hint to re-read, not as a log. There is no `subscriptions/listen`, and a dropped
SSE stream is not resumed; open a new session instead.

## stdio bridge: `afm mcp serve`

Clients that only speak stdio use the bridge. It forwards to a run that is already
running and does not own it:

```bash
afm mcp serve --connect http://127.0.0.1:9876
```

The same bridge connects to an [`afm serve`](serve.md) host (`--connect http://127.0.0.1:8100`).

```json
{
  "mcpServers": {
    "afm": {"command": "afm", "args": ["mcp", "serve", "--connect", "http://127.0.0.1:9876"]}
  }
}
```

- `--connect` must be a loopback `http` URL (`127.0.0.1`, `localhost`, `[::1]`).
  A bare host and port means the `/mcp` path. `--transport` accepts only `stdio`.
- stdout carries protocol messages only; diagnostics go to stderr.
- A run started with `--mcp-off` makes the bridge exit with code 1 and
  `the process serves no MCP endpoint: the run was started with --mcp-off`; stdout stays empty.
- When stdin closes, the bridge deletes its session and exits `0`; the run keeps
  going. `Ctrl+C` also stops only the bridge.
- A session ends with the run, with a [repeat](flow-reference.md#repeat-run-successful-flows-again)
  cycle and with an afm restart. The bridge then exits with an error asking you to
  start it again; the new session sees the new `run_id`.

Over HTTP, a client sends `Accept: application/json, text/event-stream`, keeps the
`Mcp-Session-Id` it was given and sends `MCP-Protocol-Version`.

## Artifacts and secrets

A client never passes a path. It passes an opaque id that resolves only to:
a stage plan, an execution summary, an image the agent published, or a file a
stage declares as an artifact in `flow.yaml` (inside the stage's run directory).
Paths with `..`, absolute paths and other files are refused. Logs and dialog
files are not readable.

- Text is paged: 32 KiB by default, at most 64 KiB per call (`max_bytes`,
  `offset`); `next_offset` is `null` on the last page. Files over 4 MiB are
  `too_large`; binary files are `not_text`.
- Secret values of the run are masked in text. `redacted: true` says the text
  differs from the file; the plan `revision` still describes the original bytes.
- Images: PNG, JPEG and GIF up to 10 MiB and 8192 px per side; small ones come
  inline, others as a resource link. At most 20 images per stage and 50 declared
  artifacts are listed.

## Local access

`/mcp` has **no authentication**. Only these requests are accepted: a loopback
`Host` (`127.0.0.1`, `localhost`, `[::1]`), an absent or same-origin `Origin`, and
the methods `GET`, `POST` and `DELETE`. Other hosts get `403 forbidden_host`, other
origins `403 forbidden_origin`. The check stops browsers and DNS rebinding; it does
not make a permission group a security boundary.

Anyone who can reach the loopback port can use every enabled group. Keep in mind:

- A process on the same machine, including the run's own agents, can call the
  endpoint, and by default every group is on, so an agent can approve its own plan or
  change budgets. Narrow it with `--mcp-allow`, or use `--mcp-off`, if agents must not
  control the run.
- With [Docker](docker.md) the port is published as `127.0.0.1:<port>:<port>`,
  and the container's network can reach it too. Leave out `approve`, `stages`
  and `budget` (or use `--mcp-off`) if agents must not control the run; the default allows them.
- Remote access is not supported; use an SSH tunnel.

## Verified clients

Checked on a real build against: a JSON-RPC client over HTTP, `afm mcp serve`
over stdio, and Claude Code 2.1.304 (it connects and lists all tools over HTTP
and over the bridge). Protocol version `2025-06-18` was negotiated; the server
advertises `resources.subscribe`.

## Limits of this stage

- Plan approval is not conditional on anything other than the revision; there is
  no per-user authentication or token (planned as a separate stage).
- One endpoint serves one run. [`afm serve`](serve.md) forwards these tools for many runs.
- Only status and cost resources are subscribable; plans and artifacts are read
  on demand.
- Sessions idle for 30 minutes expire; at most 16 are open at once; at most 8
  `afm_run_wait` calls run in parallel.
- Existing mutating `/api/stages/*` routes keep their current Origin checks.
