---
name: afm-pipelines
description: Inspect, configure and run reusable AFM pipeline packages from a local folder or a Git source. Use when the user wants a ready-made AFM workflow or an architecture/refactoring workshop, including per-pipeline models, profiles and skills.
---

# AFM pipeline packages

A pipeline package is a folder with `pipeline.yaml` (manifest `schema_version: 2`), a
flow, optional agent recipes, skills and scripts. AFM runs it directly from a local
path or a Git source; nothing is installed and `.afm/config.yaml` is never edited.
Do not assume the user has a checkout, that a package suits their project, or that
all roles should use the same models. Running a package needs the user's go-ahead.

## Find the source

Use the source the user names: an absolute or relative folder, or a Git source
`<repo>[//<subdir>][@<ref>]`, for example
`github.com/akopichin/afm-pipelines//pipelines/refactor@v1.2.0`. Repositories may be
`host/path`, `https://`, `ssh://`, `git@host:path` or `file://`. URLs with embedded
credentials are rejected; rely on the user's git credential helper or ssh key. Do not
clone the repository by hand. AFM fetches it into its own cache, pins the ref to a
full commit and shows the commit it used. Prefer a tag or commit over a branch so
the version is reproducible, and tell the user which one you chose.

## Inspect first

```sh
afm pipelines inspect <source> [--pipeline-options /absolute/options.yaml] [--json]
```

It starts no agents and writes nothing to the project. Read the source and commit,
content hash, package version, each role's effective client, model, reasoning and
the layer that defined it (`package`, `options` or `base`), the requirements
(Docker autoShim, collaboration, browser, tools) and what is missing. Use `--json`
when you need to process the result. A non-zero exit means the package or options
are invalid; show the message, which names the offending key.

A package whose manifest has `schema_version: 1` is in the old catalog-installer
format. `inspect` says so and `afm run --pipeline` refuses it. For those packages
follow the catalog's own README and `docs/installer.md`, and say that they use the
older mechanism.

## Choose settings

For each role offer the package defaults, a different client or model, or an
existing profile of the user. Reuse choices the user already gave. Put the choices
in an options file kept outside the project's committed files:

```yaml
docker:
  agents:
    <package>-writer:        # replaces the package recipe of this name as a whole
      type: codex
      model: gpt-5.5
      reasoning: high
collaboration:
  profiles:
    <package>-reviewer:      # replaces the package profile as a whole: keep `command`
      title: Reviewer
      command: claude
```

Only `docker.agents` and `collaboration.profiles` are accepted; any other key is
rejected with its path. A record replaces the one with the same name entirely, so
repeat every field you want to keep; nothing is inherited, including `auth`, `url`
and `model` when the `type` changes. A `claude`-type recipe needs an `auth` block even
when you only override the model; `codex` recipes do not. Package recipes may read
secrets only from `env:` references. Reasoning levels and models are the user's
choice; package recommendations are not mandatory.

Provider login, Docker/client installation and paid model probes are separate steps.
Offer concrete commands for the user's environment and never print credentials or
put them in files you create. `inspect` checks configuration, not provider access.

## Run and continue

```sh
afm run --pipeline <source> [--pipeline-options /absolute/options.yaml] [--worktree NAME]
```

`--pipeline` replaces the positional flow file; passing both, or
`--pipeline-options` alone, is an error. Skill libraries and `file:` prompts inside
the package resolve inside an immutable snapshot, and scripts see its path in
`AFM_PIPELINE_DIR`; the project keeps its usual root and worktree rules. In Docker
the snapshot is mounted read-only and the image must contain this afm version.

To continue an interrupted run, repeat the same command with the same `--worktree`
and source. AFM resumes on the recorded snapshot without fetching, so the version
does not change under the run. A different source for an unfinished run stops
before anything starts, with a hint. Options may change between stop and continue:
an ordinary stage then runs on the new recipe, with its results kept. Helpers and
team agents report `executor_changed` as before, and an interactive stage's saved
session does not carry over to a client of another type. If a run used options and
the continuation omits them, AFM warns and uses the package values; pass the file
again to keep your choices.

Report the source and commit, the roles with their models, the checks you ran, any
missing setup and the exact run command. Existing runs and their snapshots are
preserved; AFM does not clean its package cache.
