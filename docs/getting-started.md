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
afm install-skills   # optional: /afm-run, /afm-check, etc. in Claude Code and Codex
```

The binary is updated via `brew upgrade --cask afm`; `afm run` refreshes the skills
on its own, see [Skills for Claude Code and Codex](#skills-for-claude-code-and-codex).

Alpha and beta channels are available as separate casks after their first
release: `brew install --cask akopichin/afm/afm-alpha` or
`brew install --cask akopichin/afm/afm-beta`. Run them with `afm-alpha` or
`afm-beta`; both can coexist with stable `afm`. See
[Releases and prereleases](releases.md) for updates and publishing commands.

**On Linux without Homebrew (prebuilt binary from GitHub Releases):**

```bash
curl -fsSL https://raw.githubusercontent.com/akopichin/afm/main/install-linux.sh | bash
# or pin a specific version (see the Releases page for available tags):
AFM_VERSION=vX.Y.Z bash -c "$(curl -fsSL https://raw.githubusercontent.com/akopichin/afm/main/install-linux.sh)"
```

The script detects the architecture (amd64/arm64), downloads the matching
`afm_linux_<arch>.tar.gz`, verifies its sha256 checksum, and installs the binary
into `/usr/local/bin` (or `~/.local/bin` if `/usr/local` is not writable). Then
optionally run `afm install-skills`.

**From source:**

```bash
make build        # build into bin/afm
make install      # install via `go install` (into your Go bin)
```

To also install the skills, run `./install.sh` after `make build` — it copies the
freshly built `bin/afm` into `$HOME/homebrew/bin` and runs `afm install-skills --force`
(see [Skills for Claude Code and Codex](#skills-for-claude-code-and-codex)).
`make install-skills` installs the skills straight from the source tree.

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
validated before the wizard reports success and written to `.afm/flows/<name>.yaml`
(the wizard prints the exact path). Or write `flow.yaml` by hand — see the
[flow.yaml reference](flow-reference.md).

```bash
afm validate flow.yaml
```

Checks a flow.yaml for structural errors (dependency cycles, unknown
`depends_on`/`inputs` references, …) without running any agents. The wizard runs
this automatically after generating a file; run it yourself after hand-editing a
flow.

### 2. Run

```bash
# A flow created by `afm init` lives in .afm/flows/ — just run:
afm run

# Or point at a flow file explicitly (e.g. one written by hand):
afm run path/to/flow.yaml
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

Once a run has recorded token usage, `afm check` also shows a per-stage token/cost
summary and `afm report` renders a full markdown cost breakdown — see
[Cost accounting](accounting.md) (and how to turn the display off).

## Skills for Claude Code and Codex

afm ships its skills inside the binary. `afm install-skills` writes them to
`~/.afm/skills` and links each one into the agents' skill folders:

- Claude Code: `~/.claude/skills/<name>`
- Codex: `$CODEX_HOME/skills/<name>`, or `~/.codex/skills/<name>` when `CODEX_HOME`
  is not set (it must be an absolute path)

The links are relative (`../../.afm/skills/<name>`). With the standard layout
(`~/.claude/skills` and `~/.codex/skills`, no custom `CODEX_HOME`) they also resolve
inside afm's Docker container, where `~/.claude` and `~/.afm` are mounted and `~/.codex` is copied
under `/home/afm`; a custom `CODEX_HOME` works on the host only. `afm run` repeats the
same sync on the host before every run, so the skills follow the installed afm; inside
a container it does nothing. A failed sync never stops the run — it prints a
one-line warning, and `afm install-skills` shows the details.
`~/.afm/skills` belongs to afm — don't put your own skills there.

There is one store per machine: if you keep several afm builds (for example stable
and alpha), the one that synced last decides which skill versions all agents see.

afm never touches entries it did not create: your own skills, symlinks that point
elsewhere (for example goga's), files, and skills under the old names (`afm`,
`live-run`, `mesh-driven-development`). A real folder with an afm skill name — a
copy installed by an older afm, or one you edited — is also left alone with a
warning; `afm install-skills --force` replaces it with a link and moves the folder to
a `.afm-skills-backup-*` directory next to the skills folder (the path is printed).
Run it once after upgrading from a version that copied the skills.

On Windows, creating symlinks needs Developer Mode (or administrator rights);
without it `afm install-skills` reports an error instead of copying the files. The
skill folders must also be on the same volume as `~/.afm` (a relative link cannot
cross volumes), so keep a custom `CODEX_HOME` on the same drive.

### Usage in Claude Code

After `./install.sh` (or `afm install-skills`) the following skills are available
in Claude Code and Codex:

- `/afm-run` — runs a flow, monitors it, and requests plan approvals right in the chat
- `/afm-check` — shows the status of the current run
- `/afm-init` — creates flow.yaml interactively
- `/afm-pipelines` — finds and installs reusable workflows from the
  [AFM Pipelines catalog](https://github.com/akopichin/afm-pipelines), with
  project-specific model profiles, settings and optional skills
- `/afm-retry` — retries a failed stage
- `/afm-review` — view a stage plan with feedback/approval
- `/afm-live-run` — exercise a change in the real running app via the managed Chrome browser
- `/afm-mesh-driven-development` — execute a plan inside an afm run by delegating tasks to named team/mesh agents

For example, ask the agent to "install the refactor pipeline in this project".
The pipeline skill reads the catalog's manifests, offers role/model choices,
and uses its installation scripts to prepare a concrete change plan, apply it
with backups, and validate the flow. Packages are versioned independently of
AFM. Installing this skill does not download pipelines or start paid agent runs.
The first installer supports Claude/Codex Docker recipes; it reports provider
login and runtime checks separately from configuration validation.
