# Serve: one MCP host for every run

`afm serve` is a persistent process on `127.0.0.1` that serves the
[Model Context Protocol](mcp.md) for **all** afm runs on this machine. An AI client
connects once and can list runs, start new ones, attach runs started elsewhere and
control any of them by `run_id`.

```bash
afm serve                      # MCP on http://127.0.0.1:8100/mcp, all groups
afm serve --port 0             # pick a free port; it is written to ~/.afm/serve/host.json
afm serve --mcp-allow read     # list and read only
```

```json
{
  "mcpServers": {
    "afm": {"type": "http", "url": "http://127.0.0.1:8100/mcp"}
  }
}
```

Clients that only speak stdio use `afm mcp serve --connect http://127.0.0.1:8100`
(the [bridge](mcp.md#stdio-bridge-afm-mcp-serve) accepts the host as well as a single run).

## Flags

| Flag | Meaning |
|---|---|
| `--port` | Loopback port of the MCP endpoint. Default `8100`; `0` picks a free one. |
| `--mcp-allow` | Permissions of the host: `reply`, `stages`, `approve`, `budget`, `runs`, or `read` for read-only. Default: all groups. |
| `--config` | Serve config file. Default `~/.afm/serve.yaml`. |

Only one host runs per user. A second `afm serve` exits with code 1 and names the pid and
endpoint of the first.

## How the host finds runs

Every `afm run` that serves an API registers itself in `~/.afm/active-runs/<instance_id>.json`
(directory `0700`, file `0600`) when it starts and removes the file when it exits. The record
holds the pid, process start marker, mode, project, API URL, current `run_id` and the ids of earlier
repeat cycles. It never holds secrets or workflow state. The host reads these records, then asks
each endpoint's `/api/runtime` and accepts a run only when `instance_id` and `run_id` match.

`afm_runs_list` reports, per run:

| Field | Values |
|---|---|
| `ownership` | `managed`: started by the host. `attached`: attached explicitly, or a manually started run found in the registry. `discovered`: not verified. |
| `liveness` | `active`, `active_orphaned` (Docker container outlived its process), `unreachable`, `stale`, `exited`, `identity_mismatch` (the record does not match what the process reports). |
| launch `state` | `accepted`, `starting`, `ready`, `failed`, `uncertain`, `lost`, `exited`. |

A run with a forged or reused-port record is shown as `identity_mismatch` and is never
controlled. Runs of a restarted host are found again from the registry and the host's own state.

### Connect a run in AFM Desktop

On Home, choose **Connect run** next to **Active runs**, or press **Command+Shift+C**
on macOS (**Ctrl+Shift+C** on Linux). Enter the run's local HTTP URL, such as
`http://localhost:63439/`, and select **Connect**. This also works for AFM in
Docker when its port is published on the host. When the dialog says **Connected**,
choose **Done** and select the run under **Active runs** to open its dashboard.
Runs without a local project folder open under **External runs**. Core runs have
an API and appear in the list with an API-only status.

## Tools

The tools of a single run's [MCP endpoint](mcp.md#tools) are available on the host with an
extra required `run_id` argument from `afm_runs_list`. The host adds:

| Tool | Group | Purpose |
|---|---|---|
| `afm_runs_list` | always | Runs with identity, liveness, project, endpoint, launch state, run status and open actions. |
| `afm_run_report` | always | [Report](accounting.md) of a managed or attached run, read from its files even after the process exits. Pass `instance_id` from the run listing to select a specific instance. AFM Desktop uses this report for finished run tabs whose dashboard has closed. |
| `afm_run_start` | `runs` | Start a run from an allowlisted project and flow or pipeline. Returns at once with a launch in state `starting`; follow it with `afm_runs_list`. |
| `afm_run_attach` | `runs` | Attach a run started elsewhere by its loopback API URL. The host controls it through MCP but never signals it. |
| `afm_run_cancel` | `runs` | Stop a managed run whose workflow is active. |
| `afm_run_release` | `runs` | End a managed run whose workflow finished but whose process still holds the API. Its files stay. |

Forwarded control tools follow the same groups as in [MCP](mcp.md#permission-groups); a
group the host does not allow is not listed. A forwarded call takes the `run_id` it was
made for: when the run served by that process changed (a [repeat](flow-reference.md#repeat-run-successful-flows-again)
cycle, restart) it fails with `run_changed` and changes nothing.

The list can include an `outcome` for the current run cycle. A recorded workflow result is `finished`, `failed`, or `interrupted`. For a managed launch, `process_exited` means the process ended while the journal still said `running`, `launch_failed` identifies a startup failure, and `unknown` means the host could not establish the result. The host saves its observation when it detects exit. The field can be absent during execution and on older records. Use the list after a live wait returns `run_unavailable`. A failed stage can still be waiting for retry in a running workflow.

For example, after a forced process exit the list can contain this row fragment:

```json
{
  "launch": { "state": "exited" },
  "run_status": "running",
  "outcome": "process_exited"
}
```

Here the host observed the process exit before the workflow recorded a final result. An external client waiting for completion can stop polling that process and report the interruption.

`cancel_requested: true` means the host recorded a cancel request for this run cycle before attempting to signal it. Check the outcome and liveness for completion. A Docker stop can return `stop_pending` while the container is still active.

### Starting a run

`afm_run_start` takes `request_id` (your idempotency key), `project`, `mode` (`core` or
`dashboard`) and either `flow` (a file in the project's `.afm/flows`) or `pipeline`. Optional:
`worktree`, `no_worktree` (run in the project checkout even if the flow or config enables worktrees; not with `worktree`), `mcp_allow` (permissions of the run's own MCP, default what the host allows) and
`task` (see [`--task`](core-mode.md#initial-task)).

- Repeating a `request_id` with the same parameters returns the same launch and never starts a
  second run; with other parameters it fails with `request_conflict`. A failed start also spends
  its `request_id`.
- The host starts `afm run` as its own child in a new session, with stdin closed, a filtered
  environment and the task passed as a file, never in argv.
- A second launch of the same flow in the same checkout is refused with `conflict` until the
  first ends; give the second its own `worktree`.
- A launch whose fate is unknown is `uncertain`, later `lost`. The host never restarts a child blindly.

### Cancel and release

- `afm_run_cancel` interrupts a managed run and kills it after a grace period. A Docker run is only
  interrupted and may report `stop_pending`.
- `afm_run_release` ends a finished run that [core mode](core-mode.md#lifetime) keeps alive. It also
  ends the whole [repeat](flow-reference.md#repeat-run-successful-flows-again) series.
- Neither works on an attached run: a failed call says `not_managed`.

## `serve.yaml`

The host starts only what `~/.afm/serve.yaml` lists, so a request is never an arbitrary command.
The file is described by [`schema/serve.schema.json`](https://github.com/akopichin/afm/blob/main/schema/serve.schema.json).

```yaml
projects:
  - id: app
    path: /home/me/work/app          # absolute path of an existing directory
pipelines:
  - id: dev
    source: /home/me/pipelines/dev   # anything `afm run --pipeline` accepts
    options: /home/me/dev-options.yaml
limits:
  max_running: 4                     # runs the host keeps running (default 4)
  max_starting: 2                    # runs allowed to start at once (default 2)
retention:
  release_after: 24h                 # release a finished run after this time; off by default
```

A missing file is an empty config: nothing can be started, but listing and attaching work.
Ids are lowercase letters, digits and dashes. Over a limit the call fails with `limit_running`
or `limit_starting`.

## Lifetime

Stopping the host (`SIGINT`, `SIGTERM`) leaves every run running. A restarted host finds them again,
including the runs it started. Host state lives in `~/.afm/serve`; the host never deletes your
projects or run directories.

## Limits

- No authentication: the host answers only loopback `Host` and `Origin`, like [MCP](mcp.md#local-access).
  Anyone who can reach the port can use every enabled group, including `runs`. Remote access is a
  later stage; use an SSH tunnel.
- Docker runs are stopped gracefully only (`SIGINT`), never killed by the host.
- Native macOS integration tests also cover cancel, release and forced process exit. The Docker `stop_pending` case has controller-test coverage.
- Budgets cannot be set at start; use `afm_budget_adjust` afterwards.
- A run whose process exited keeps its files; read it with `afm_run_report` if it was started by the host or explicitly attached with `afm_run_attach`. A manual run that was only discovered is forgotten when its record disappears, so attach it before it ends.
