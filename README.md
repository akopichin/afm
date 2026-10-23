# afm

**Reliable, resumable workflows for AI agents.** Define stages, dependencies,
approvals, and checks. afm runs your agents through the flow and records progress
on disk. If a run is interrupted, start it again: completed stages stay completed,
interrupted stages are retried, and approvals, team agents, conversations, and
pending waits are restored from their saved state.

Start with a single agent or bring a team. Use Claude, Codex, GLM, DeepSeek,
Cursor, or other compatible agents in the same flow, with a live dashboard to
review plans, answer questions, and steer the work.

[![CI](https://github.com/akopichin/afm/actions/workflows/ci.yml/badge.svg)](https://github.com/akopichin/afm/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/akopichin/afm?sort=semver)](https://github.com/akopichin/afm/releases)
[![Go Report Card](https://goreportcard.com/badge/github.com/akopichin/afm)](https://goreportcard.com/report/github.com/akopichin/afm)
[![License: MIT](https://img.shields.io/github/license/akopichin/afm)](LICENSE)
[![Docs](https://img.shields.io/badge/docs-akopichin.github.io%2Fafm-blue)](https://akopichin.github.io/afm/)

<p align="center">
  <img src="docs/assets/afm-team.gif" alt="afm demo — agents fix checkout with a required helper review, live conversations, recurring Kafka checks, and automatic resume after usage limits" width="900">
</p>

## Why afm

- **A flow that survives interruptions.** Restart `afm run` after a crash,
  Ctrl+C, or a stopped container to recover the run's recorded progress.
  Completed stages are skipped; interrupted stages are retried. Pending approvals
  and questions are preserved, and the team's agents, tasks, conversations, and
  waits are restored. Agents can also wait for provider usage limits to reset
  without spending their ordinary retry budget. See
  [Resume on restart](docs/stage-lifecycle.md#resume-on-restart).

  <p align="center">
    <img src="docs/assets/afm-restart.gif" alt="afm restarts mid-run and the team's conversations and waits are restored" width="720">
  </p>

- **An explicit path to a checked result.** Run stages sequentially or in
  parallel, declare their dependencies, and pass artifacts between them. Add
  [test commands and independent AI review](docs/verify.md) as completion checks.
  Stage agents can also exercise the app in a
  [managed Chrome browser](docs/browser.md) on supported macOS hosts.
- **Human control throughout the flow.** Review plans line by line before
  implementation, answer agents' questions, pause work, send revisions, or add
  a stage as the task evolves. Follow the flow in the [dashboard](docs/dashboard.md),
  or answer questions and approve plans through [Telegram](docs/telegram-bot.md).
- **One agent or a coordinating team.** Choose different agents and models for
  different stages. Add specialists who exchange findings, track assignments on
  a shared board, and delegate helper tasks. Stages can wait for colleagues'
  answers before continuing; you can join their conversations. See
  [Helpers and collaboration](docs/collaboration.md).
- **Flows you can run again.** [Repeat successful flows](docs/flow-reference.md#repeat-run-successful-flows-again)
  in fresh runs, use [`/loop`](docs/loops.md) to send recurring prompts to existing
  agents during a run, and opt into [memory](docs/agent-memory.md) to carry project
  lessons into future runs.
- **A history you can inspect.** Logs, events, artifacts, and (with `--debug`)
  the exact prompts agents received stay on disk. See what happened, who did the
  work, and which decisions moved the flow forward.

## Quick start

```bash
brew install --cask akopichin/afm/afm   # or see the install guide for Linux/Docker/source
afm init                                # scaffold a flow into .afm/flows/ interactively
afm run                                 # run the flow from .afm/flows (or: afm run path/to/flow.yaml)
```

A live dashboard comes up at `http://localhost:9876` (its URL is printed to the log).

After the planning phase each stage waits at `awaiting_approval` — review and approve
the plan in the dashboard, and afm implements it. Follow along with `afm check` or the
dashboard. (The CLI `afm approve`/`revise`/`retry` commands are for headless use — a
live `afm run` holds an exclusive lock, so approve through the dashboard while it's
running.)

## A minimal flow.yaml

```yaml
name: my-feature
description: "Add JWT auth"

stages:
  - id: backend
    name: "Backend API"
    description: "Implement the login/logout endpoints and JWT middleware."
    agents: [planning, implementation, review]
    verify: "make test"
    artifacts:
      - name: api-contract
        path: docs/api-contract.yaml
        description: "OpenAPI specification"

  - id: frontend
    name: "Frontend"
    description: "Build the login UI against the API contract."
    agents: [planning, implementation]
    depends_on: [backend]
    inputs:
      - backend.api-contract
    verify: "make test"
```

Replace `make test` with your project's test command. The frontend starts after
the backend and its verification pass, and receives the declared API contract.
A failed check sends the stage back for correction within its configured budget.

For collaborating agents and independent AI review, see the
[team review example](examples/agent-team-review/). More flows live in
[`examples/`](examples/), and every field is described in the
[flow.yaml reference](https://akopichin.github.io/afm/flow-reference/).

To give a flow its own branch and working directory, set `worktrees: true` at the
top of `flow.yaml`. afm runs its stages in `<project-root>/.worktrees/<flow-name>`
and keeps run state in the original `.afm/`. Use `afm run --worktree <name>` to
choose a different branch and worktree for a run. See
[per-flow git worktrees](https://akopichin.github.io/afm/flow-reference/#per-flow-git-worktrees)
for configuration and reset options.

## Lifecycle hooks

Connect flow and stage events to notifications, metrics, or external status
updates with [lifecycle hooks](docs/lifecycle-hooks.md). Hooks receive JSON on
stdin and `AFM_*` environment variables, with secrets supplied by reference.
They observe the flow; a hook failure does not change its result. See the guide
for configuration and examples.

## Documentation

Full documentation lives at **[akopichin.github.io/afm](https://akopichin.github.io/afm/)**:

| Topic | |
|-------|--|
| Install, first flow, approving plans | [Getting started](https://akopichin.github.io/afm/getting-started/) |
| Every `flow.yaml` field | [flow.yaml reference](https://akopichin.github.io/afm/flow-reference/) |
| `config.yaml`, `--dir`, `--debug` | [Configuration](https://akopichin.github.io/afm/config-reference/) |
| Statuses and resume | [Stage lifecycle](https://akopichin.github.io/afm/stage-lifecycle/) |
| `agents: [auto]` | [Autonomous track](https://akopichin.github.io/afm/autonomous-track/) |
| Test commands and independent AI review | [Verification](https://akopichin.github.io/afm/verify/) |
| File-based dialog | [Interactive stages](https://akopichin.github.io/afm/interactive-stages/) |
| Event notifications, secrets in hooks | [Lifecycle hooks](https://akopichin.github.io/afm/lifecycle-hooks/) |
| Carrying lessons across runs | [Agent memory](https://akopichin.github.io/afm/agent-memory/) |
| Token/cost tracking, `pricing:` overrides | [Cost accounting](https://akopichin.github.io/afm/accounting/) |
| The web dashboard | [Dashboard](https://akopichin.github.io/afm/dashboard/) |
| AFM from a private Telegram chat | [Telegram bot](https://akopichin.github.io/afm/telegram-bot/) |
| Agent teams: run agents, conversations, board, helpers | [Helpers and collaboration](https://akopichin.github.io/afm/collaboration/) |
| Recurring prompts during a run | [Repeating prompts](https://akopichin.github.io/afm/loops/) |
| Exercising an app in Chrome | [Managed browser](https://akopichin.github.io/afm/browser/) |
| Running in Docker, non-Claude agents | [Docker mode](https://akopichin.github.io/afm/docker/) |
| Driving afm from Go | [Go SDK](https://akopichin.github.io/afm/sdk/) |
| Event log, directory layout, prompts | [Internals](https://akopichin.github.io/afm/internals/directory-layout/) |

## Development

```bash
make build        # build (bin/afm)
make test         # tests (with -race)
make lint         # linter
make install      # go install
make docker-build # build the Docker image
```

Versioned release: `make release-patch` / `release-minor` / `release-major` bumps the
SemVer tag and pushes it; the actual build (Docker image + binaries + GitHub Release +
Homebrew cask) happens in GitHub Actions once the tag is pushed. Add `-alpha` or
`-beta` to any release target for numbered prereleases, for example
`make release-major-alpha` creates `v2.0.0-alpha.1` from stable `v1.x.y` and
increments the alpha counter on subsequent calls. Install the separate Homebrew
casks with `brew install --cask akopichin/afm/afm-alpha` or `afm-beta`; they provide
commands `afm-alpha` and `afm-beta` alongside stable `afm`. Pushes to `main` run
CI checks; releases require an explicit version tag, created with `make release-*`.
See [Releases and prereleases](docs/releases.md) for details, the
[CHANGELOG](CHANGELOG.md) for release history, and
[CONTRIBUTING](CONTRIBUTING.md) to get started.

## License

[MIT](LICENSE).
