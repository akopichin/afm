# Pipeline packages

A pipeline package is a folder with a flow, optional agent recipes, skills and
scripts. `afm run --pipeline` runs it straight from a local folder or a Git
repository. Nothing is installed: your `.afm/config.yaml` and the global
`~/.afm/config.yaml` are never edited, and no profile is written anywhere.

```bash
# Look first: roles, effective models, what this machine lacks. Starts no agent.
afm pipelines inspect ./my-pipeline --pipeline-options ./my-options.yaml

# Run it in your project
afm run --pipeline ./my-pipeline --pipeline-options ./my-options.yaml
```

A runnable example is in
[`examples/pipeline-package`](https://github.com/akopichin/afm/tree/main/examples/pipeline-package).

## Package format (manifest v2)

The package root holds `pipeline.yaml`:

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/akopichin/afm/main/schema/pipeline.schema.json
schema_version: 2
name: demo-review            # ^[a-z][a-z0-9-]*[a-z0-9]$
version: 1.0.0
description: "Drafts release notes with one agent, then checks them with a package script"
entrypoint: flow.yaml        # the flow
agents: agents.yaml          # optional: agent recipes and helper profiles
resources:                   # copied into the snapshot, relative to the package root
  - skills
  - scripts
roles:
  writer:
    command: demo-review-writer
    description: "Drafts the release notes"
  reviewer:
    command: demo-review-reviewer
    profile: demo-review-reviewer
requirements:
  docker_auto_shim: true
  collaboration: true
  tools: [bash]
```

| Key | Meaning |
|---|---|
| `schema_version` | Must be `2`. |
| `name`, `version` | Required. Recipe and profile names of the package start with `<name>-`. |
| `entrypoint` | The flow file. Always part of the snapshot. |
| `agents` | Optional file with `docker.agents` and `collaboration.profiles`. Always part of the snapshot. |
| `resources` | Files and directories copied into the snapshot. Paths stay inside the package; symlinks and special files are rejected. |
| `roles` | What the pipeline calls its agents: the `command` it runs and an optional helper `profile`. `inspect` and the start banner show their effective settings. |
| `requirements` | What the package needs from your machine: `docker_auto_shim`, `collaboration`, `browser`, `tools`. `inspect` reports what is missing and exits 0; `afm run` stops at preflight, before creating a run. |

`pipeline.yaml` is strict: an unknown key is an error with its path. A manifest
with `schema_version: 1` is the catalog installer format; `afm run --pipeline`
refuses it with a message that says so. Run that package's `flow.yaml` directly
(`afm run flow.yaml`) as before — nothing migrates automatically.

## Agent layers

The agent configuration of a run is assembled in memory from three layers, lowest
first:

1. **base** — your global and project `config.yaml`;
2. **package** — the package's `agents` file;
3. **options** — the file passed with `--pipeline-options`.

Each layer may contain only `docker.agents` (recipes, see
[Docker mode](docker.md#non-claude-agents-in-docker-autoshim)) and
`collaboration.profiles` (helper profiles, see
[Helpers and collaboration](collaboration.md)). Any other key is rejected with
its path, so a package cannot change your image, mounts, browser or limits.

A record is **replaced as a whole** by the same name from a later layer — fields
are not merged. If an options recipe replaces the package's writer, repeat every
field you still want, including `auth`. A claude-type recipe needs an `auth`
block even to override only `model`; codex recipes use the mounted `~/.codex`
login instead.

Extra limits for the package layer:

- recipe and profile names start with `<package name>-`;
- `auth.from` reads the host environment only (`env:NAME`); a `file:` secret is
  refused, and a secret is passed to the container by name, never by value;
- `system_prompt: file:...` must be a relative path inside the package; it becomes
  an absolute path into the snapshot.

The options file has no prefix rule, so it can override a package recipe or point
a role at a command you already have. `afm run` prints a warning when a package
recipe shadows one from your config, or sends a secret to a custom `url`.

## Options

```yaml
# my-options.yaml — your choices for this package
docker:
  agents:
    demo-review-writer:        # run the writer on Codex instead of Claude
      type: codex
      model: gpt-5.5
      reasoning: high

collaboration:
  profiles:
    demo-review-reviewer:      # replaces the package profile entirely: keep `command`
      title: Release notes reviewer
      command: claude
```

Relative `file:` paths in options are resolved against the options file's
directory. A role's `command` is a recipe name (from `docker.agents`) or a command
such as `claude`; it is not a profile ID, so putting a profile name there selects
nothing. Options may only set `docker.agents` and `collaboration.profiles`: to
change a package recipe or helper profile, replace it by name as above, and to
use a helper profile you already have in your config, reference it as `profile:`
on a flow helper item (see [Collaboration](collaboration.md)), which needs a flow
that can be edited. `afm pipelines inspect` shows which layer defined each
effective record.

## Inspect

```bash
afm pipelines inspect <source> [--pipeline-options <file>] [--json]
```

It prints the source, the Git commit (for Git sources), the content hash and
snapshot folder, the options file, each role with its client, model, reasoning,
`url`, `auth.from` reference and defining layer, and the requirements with what
this machine lacks. It starts no agent and writes no config or run state; the
snapshot is cached exactly as a run would cache it. The exit status is 0 for a valid
package even when requirements are missing, and 1 for an invalid one. `--json`
prints the same report for scripts. Secret values never appear, only the
`auth.from` reference.

## Sources

| Source | Example |
|---|---|
| Local folder | `./pipelines/refactor`, `/abs/path` |
| Git | `<repo>[//<subdir>][@<ref>]` |

The repository can be `host/path` (`https://` is assumed), `https://`, `http://`,
`ssh://`, `git@host:path` or `file://`. The optional `//<subdir>` is the package
folder inside the repository, and `@<ref>` is a branch, tag or commit:

```bash
afm run --pipeline github.com/akopichin/afm-pipelines//pipelines/refactor@v1.2.0
```

In AFM Desktop, **Settings → Library → GitHub repository** also accepts the repository
root. Desktop discovers nested packages and lists each workflow separately in
**New run**. Use `afm pipelines catalog <git-source>` to list the package directories
and the inspected commit as JSON. An explicit package directory selects only that
package. A branch or tag suffix applies to the whole collection.


A URL with embedded credentials is refused; use your git credential helper or ssh
key. Prefer a tag or a full commit over a branch: the run is reproducible and a
full commit already in the cache needs no network.

AFM keeps its cache outside every project, in `~/.afm/cache/pipelines/`: a bare
clone per repository (`git/`) and the immutable snapshots (`snapshots/<sha256>`).
A branch or tag always fetches, so a stale resolution is never reused. The ref is
pinned to a full commit before anything starts, and the commit and content hash are
shown in the start banner.

## The snapshot and `AFM_PIPELINE_DIR`

Before the run starts, the declared files are copied into a read-only snapshot
named by its content hash. The run, its container and its scripts use the snapshot,
so editing or deleting the source folder, or moving a branch, does not change a run
in progress. The snapshot protects against accidental changes and mid-run edits;
it is not tamper-proof against a process that can write to your home directory.

Stages see the snapshot as `AFM_PIPELINE_DIR` (always absolute). Use it for package
scripts and data:

```yaml
stages:
  - id: check
    script: |
      "$AFM_PIPELINE_DIR/scripts/check-notes.sh" RELEASE_NOTES.md
```

Path rules: `flow.root_dir` and other project paths keep meaning *your project*
(relative to the afm root), while package files are found through
`AFM_PIPELINE_DIR`. The package's own `skills_dir` is an absolute snapshot path
once loaded. Limits: 50 000 files and 256 MiB per snapshot.

## Continuing a run

The run is bound to its snapshot in `.afm/pipelines/runs/<run-id>.json` (the
source, commit, content hash, options file path and hash, and the executor type of
each command; never secrets). To continue after Ctrl+C or a failure, run **the same
command** again:

```bash
afm run --pipeline ./my-pipeline --pipeline-options ./my-options.yaml
# afm: pipeline demo-review 1.0.0 (continuing run demo-review-20261114-...)
```

- A local folder or branch that changed since the start does **not** change the run:
  it continues on the recorded snapshot and prints a notice. A Git run restores
  its snapshot from the recorded commit in the local Git cache without fetching; if
  that commit is no longer in the cache, the start fails and says so.
- **Options are applied fresh** on every start. Between Ctrl+C and the next start
  you can edit the options file — change a model, switch a recipe, replace a secret
  reference — and the new values take effect. Continuing without the
  options file prints a notice and the package values over your base config apply.
  Passing a different file applies that file's settings and prints a notice naming
  the file it now uses.
- Pass the same `--worktree` as before: the run name is `--worktree` or the flow
  name, and only the newest unfinished run of that name can be continued. A command
  for another source or another `--worktree` never takes over a bound run; if an
  unfinished run of that name belongs elsewhere, the start stops with advice to use
  `--worktree <new name>` or `--worktree-clear`.
- `--worktree-clear` and `--worktree-clear-force` always start a new run.
- With `repeat`, every cycle is a separate run with its own binding and snapshot.

### Changing the client mid-run

Changing a recipe is allowed, but saved conversations do not always follow:

- an unfinished **interactive** stage gets a notice when its command changes
  client type. Codex to Claude is the risky direction: Claude receives the saved
  Codex session ID with `--resume` and may refuse it. Claude to Codex starts a new
  session. To continue reliably, keep the original client for that stage or start a
  new run;
- a **team agent or helper** keeps the executor identity it was created with. The
  identity covers the recipe's type, model, reasoning, URL and `auth` selectors, so
  changing any of them makes its next turn fail with `executor_changed` instead of
  silently continuing on a different model or account (see
  [Which executor a helper runs](collaboration.md#which-executor-a-helper-runs));
- changing only the value a secret reference points to, with the same selector,
  changes neither the notice nor the identity.

## Docker

With Docker enabled, the snapshot is mounted read-only at the same absolute path
and the container receives a launch record rather than fetching the package again.
The container's afm must be the same version as the host's, because the launch
record is versioned: use an image built from this release. See
[Docker mode](docker.md#running-a-pipeline-package-in-docker).

## Compatibility

- `afm run flow.yaml` is unchanged. `--pipeline` cannot be combined with a flow
  file, and `--pipeline-options` requires `--pipeline`; both are rejected before
  anything is created.
- A project's own `pipeline.yaml` is not read unless you pass `--pipeline`.
- The `/afm-pipelines` skill uses these commands; see
  [Getting started](getting-started.md#usage-in-claude-code).
