# Getting started

## How it works

Each stage goes through phases by default:

```
1. Planning   — AI builds a stage plan → you review and approve (or revise)
2. Execution  — AI implements the approved plan (+ optional code review)
```

Stages can run in parallel; dependencies via `depends_on` guarantee the correct
order. Plans and artifacts of dependent stages are automatically substituted into
the prompt.

**Autonomous track (optional).** A stage marked `agents: [auto]` skips
planning/approval entirely: an agent with skills does the work in a single
`autonomous_execution` step and writes `execution_summary.md` (the artifact
dependent stages read instead of a plan). See the
[Autonomous track](autonomous-track.md).

**Reliability.** The state of every run is written to an event log
`.afm/runs/<run>/events.jsonl` (append + fsync) — this is the single source of
truth. If a run is interrupted, `afm run` automatically resumes from the same
point: completed stages are skipped, interrupted ones are retried. While `afm run`
is active, it holds an exclusive lock on the run directory (`.lock`) — a concurrent
`afm approve/retry/revise` from another process can't corrupt the live log. See
[the event log](internals/event-log.md).

## Installation

**Via Homebrew (recommended):**

```bash
brew install --cask akopichin/afm/afm
afm install-skills   # optional: /afm, /afm-check, etc. in Claude Code
```

The binary is updated via `brew upgrade --cask afm`; skills don't need to be
reinstalled on update, but you can re-run `afm install-skills` if new ones have
appeared.

**On Linux without Homebrew (prebuilt binary from GitHub Releases):**

```bash
curl -fsSL https://raw.githubusercontent.com/akopichin/afm/main/install-linux.sh | bash
# or a specific version:
AFM_VERSION=v0.5.70 bash -c "$(curl -fsSL https://raw.githubusercontent.com/akopichin/afm/main/install-linux.sh)"
```

The script detects the architecture (amd64/arm64), downloads the matching
`afm_linux_<arch>.tar.gz`, verifies its sha256 checksum, and installs the binary
into `/usr/local/bin` (or `~/.local/bin` if `/usr/local` is not writable). Then
optionally run `afm install-skills`.

**From source:**

```bash
make build        # build into bin/afm
make install      # install via go install
```

**Prebuilt binary + Claude skills:**

```bash
./install.sh
```

The script copies the binary to `/usr/local/bin` and installs skills for Claude
Code (`/afm`, `/afm-check`, `/afm-init`, `/afm-retry`, `/afm-review`).

To run afm without a local install, see [Docker mode](docker.md).

## Quick start

### 1. Create a flow

```bash
afm init
```

Walks you through one of four archetypes — a single change (planning →
implementation → review), a build + verify loop, parallel tracks merging into an
integration stage, or fully custom stage-by-stage — then asks per-stage questions
(agent mode, plan vs. planning agent, which phases to run, and optional
artifacts/inputs/verify/interactive/custom-command settings). The result is
validated before the wizard reports success. Or write `flow.yaml` by hand — see
the [flow.yaml reference](flow-reference.md).

```bash
afm validate flow.yaml
```

Checks a flow.yaml for structural errors (dependency cycles, unknown
`depends_on`/`inputs` references, …) without running any agents. The wizard runs
this automatically after generating a file; run it yourself after hand-editing a
flow.

### 2. Run

```bash
afm run flow.yaml

# If the flow lives in .afm/flows/ — you can omit the argument:
afm run
```

By default a web dashboard comes up (`http://localhost:9876`); its URL is printed
to the log.

### 3. Approve plans

After the planning phase, each stage transitions to `awaiting_approval`. There are
two ways to approve:

**Via the web dashboard** — open `http://localhost:9876`, select a stage, review
the plan line by line, leave inline comments on specific lines (like in an MR), and
click "Approve" or "Send revision".

**Via the CLI:**

```bash
# View the plan
cat .afm/runs/<run-dir>/<stage-id>/plan.md

# Approve
afm approve backend-auth

# Not happy with it — ask for a redo
afm revise backend-auth --feedback "Need to add Redis for the token blacklist"

# Retry a failed stage
afm retry backend-auth
```

> CLI mutations (`approve`/`revise`/`retry`) work when `afm run` is NOT running
> (headless scenario). While `afm run` is active, approve through the dashboard —
> otherwise the command will report that the run is locked.

### 4. Follow progress

```bash
afm check
```

```
Run: jwt-auth-20260416-152543-a3f9

STAGE                 STATUS                 UPDATED
-----                 ------                 -------
backend-auth          done                   15:31:02
frontend-login        running                15:31:45
integration-tests     pending                15:31:02
```

Or in real time via the [web dashboard](dashboard.md) — stages, progress bar,
event feed, logs.

## Usage in Claude Code

After `./install.sh` (or `afm install-skills`) the following skills are available:

- `/afm` — runs a flow, monitors it, and requests plan approvals right in the chat
- `/afm-check` — shows the status of the current run
- `/afm-init` — creates flow.yaml interactively
- `/afm-retry` — retries a failed stage
- `/afm-review` — view a stage plan with feedback/approval
