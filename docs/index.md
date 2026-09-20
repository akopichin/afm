# afm

**Orchestrate multi-stage AI tasks.** Describe a task in a YAML file, break it
into stages — afm runs AI agents sequentially or in parallel, waits for your
approval of their plans, and carries out the implementation. Works with `claude`
and any claude-compatible agent (GLM, DeepSeek, Cursor, Codex, …).

<p align="center">
  <img src="assets/afm-demo.gif" alt="afm demo — multi-stage AI orchestration with a live dashboard" width="900">
</p>

## Why afm, when Claude Code already exists

- **Tasks measured in hours, not minutes.** Every run is written to an append-only
  event log; if it's interrupted, `afm run` resumes from the same point — completed
  stages are skipped, interrupted ones retried.
- **Several models in one flow.** Claude plans, a cheaper model (GLM/DeepSeek) does
  the routine work, another reviews — each stage can use a different agent.
- **A human in the loop at the plan level.** Review and comment on the plan
  line-by-line *before* any code is written, like a merge request.
- **An explicit dependency graph and artifacts** between stages, instead of implicit
  shared context.
- **Everything on disk** — logs, events, and (with `--debug`) the exact prompt each
  agent received. You can see what happened after the fact.

## Quick start

```bash
brew install --cask akopichin/afm/afm
afm init   # scaffold a flow into .afm/flows/ interactively
afm run    # run the flow from .afm/flows (or: afm run path/to/flow.yaml)
```

A live dashboard comes up at `http://localhost:9876` (its URL is printed to the log).

Then head to **[Getting started](getting-started.md)** for the full walkthrough,
or the **[flow.yaml reference](flow-reference.md)** for every field.

## Documentation map

| Topic | Page |
|-------|------|
| Install, first flow, approving plans | [Getting started](getting-started.md) |
| Every `flow.yaml` field | [flow.yaml reference](flow-reference.md) |
| `config.yaml`, `--dir`, `--debug` | [Configuration](config-reference.md) |
| Statuses and resume | [Stage lifecycle](stage-lifecycle.md) |
| `agents: [auto]` | [Autonomous track](autonomous-track.md) |
| Independent AI/shell verification gate | [AI-verify](verify.md) |
| `script:` stages and hooks | [Script stages & hooks](script-stages.md) |
| File-based dialog | [Interactive stages](interactive-stages.md) |
| Passing data between stages | [Artifacts & inputs](artifacts.md) |
| Carrying lessons across runs | [Agent memory](agent-memory.md) |
| The web dashboard | [Dashboard](dashboard.md) |
| Running in Docker, non-Claude agents | [Docker mode](docker.md) |
| Driving afm from Go | [Go SDK](sdk.md) |
| Event log, directory layout, prompts | [Internals](internals/directory-layout.md) |
