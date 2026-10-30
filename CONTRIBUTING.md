# Contributing to afm

Thanks for your interest in improving afm! This guide covers building, testing, and
opening a pull request.

## Prerequisites

- Go (the version pinned in [`go.mod`](go.mod) — do not bump it without discussion)
- Node.js (for building the web dashboard)
- Docker (optional — only needed for Docker-mode features and their tests)

## Build and test

```bash
make build        # build the binary into bin/afm
make test         # run the full test suite with -race
make lint         # golangci-lint (must be clean)
```

Go dependencies are checked into `vendor/`, so Go builds and tests can run
without downloading modules. After changing `go.mod` or `go.sum`, run
`make vendor` and commit the resulting `vendor/` changes with the manifests.
Building the dashboard still requires npm dependencies from its lockfile.

Before committing, run `make lint build test`.

For stable and numbered alpha/beta releases, see
[Releases and prereleases](docs/releases.md). Release script and publication
configuration regression tests are included in `make test` (`go test ./scripts`).

## Maintaining the 1.x release line

`v1.x` maintains the old release line, starting from `v1.2.17`. Develop its fixes
and minor features on branches based on `v1.x`, and target **`v1.x`** when merging
or opening a pull request. `main` remains the current development line.

### Create a feature worktree

From the primary checkout:

```bash
git fetch origin --tags
git worktree add -b fix-v1-example .worktrees/fix-v1-example origin/v1.x
```

Choose a new name for each feature. Edit and commit inside that worktree, following
this branch's instructions and checks. For a pull request, push the feature branch
and select `v1.x` as its base. To merge locally, use a clean checkout of `v1.x`;
`git worktree list` shows where it is checked out. For example, if that checkout
is `.worktrees/v1.x`:

```bash
git -C .worktrees/v1.x pull --ff-only
git -C .worktrees/v1.x merge --ff-only fix-v1-example
```

If the base advanced, integrate the updated `v1.x` into the feature and validate
again before fast-forwarding. If `v1.x` has no checkout yet, create one first;
for a clone without a local `v1.x` branch, use
`git worktree add --track -b v1.x .worktrees/v1.x origin/v1.x`.

### Use dev-pipeline for a 1.x change

Use a current AFM 2.x binary to drive the pipeline; the feature worktree determines
which product source is changed. Create the feature worktree above **before**
starting AFM: `--worktree` names the feature branch, and a newly created AFM
worktree would otherwise start from the project checkout's current `HEAD`.

With [dev-pipeline](https://github.com/akopichin/afm-pipelines/tree/main/pipelines/dev-pipeline)
installed, run from the primary checkout so its pipeline, helper, configuration
and skill library remain available:

```bash
afm --dir . run pipelines/dev-pipeline/flow.yaml \
  --worktree fix-v1-example --port 8013
```

If using this project's older local entry point, replace
`pipelines/dev-pipeline/flow.yaml` with `pipelines/dev-pipeline-v1.yaml`.
These are optional installed/local workflows, not files guaranteed to exist in
a fresh AFM clone. Choose a free port if `8013` is already in use.

Give the planning stage the task and these constraints:

> Implement this change for AFM 1.x. The base branch and final merge target are
> `v1.x`. Set `base_branch` to `v1.x` in `plan/project.json`. Use the 1.x code and
> its contribution rules. Locate the target checkout with `git worktree list`
> (in this setup, `.worktrees/v1.x`).

For an existing plan stored in an ignored directory such as `tmp/`, copy it into
the feature worktree, give the agent its path and include it in the contract's
`input_paths`. Otherwise it is not automatically copied from the primary checkout
or included in the helper's evidence fingerprint.

The merge stage prepares and validates the candidate, requests merge approval,
and fast-forwards the clean `v1.x` checkout after approval. It does **not** push
or release. Reuse the same worktree name to continue an unfinished run; use a new
name for another feature. Existing runs keep their saved stage commands and model
bindings when the pipeline YAML changes.

### Publish a patch or minor release

After merging, push the maintenance branch and wait for its CI checks to pass:

```bash
git push origin v1.x
./scripts/release.sh --dry-run patch --branch v1.x
```

Choose one release command, from any checkout with the updated release tooling:

| Bump | Command | Example from `v1.2.17` |
| --- | --- | --- |
| Patch | `make release-patch v1.x` | `v1.2.18` |
| Minor | `make release-minor v1.x` | `v1.3.0` |

The explicit branch argument fetches and releases the **pushed `origin/v1.x`
commit**, regardless of the caller's checkout or uncommitted changes. A branch
push alone runs CI; the release command pushes the version tag and triggers the
build/publication workflow. Major bumps are rejected for `v1.x`.

Stable 1.x releases update Homebrew `afm@1` (command `afm-1`) and Docker
`akopichin/afm:1`. The regular `afm` cask, Docker `latest`, and GitHub Latest stay
on the current main release line. The separate 1.x cask and Docker tag become
available after the first maintenance release. See
[Releases and prereleases](docs/releases.md) for installation and channel details.

## Documentation

User docs live under [`docs/`](docs/) and are published to
[akopichin.github.io/afm](https://akopichin.github.io/afm/) via MkDocs Material. To
preview locally:

```bash
pip install -r docs/requirements.txt
mkdocs serve            # http://localhost:8000
```

The Pages workflow builds with `mkdocs build --strict`, so broken links or missing
anchors fail CI — check `mkdocs build --strict` locally before pushing doc changes.

Design specs and implementation plans live under `docs/superpowers/` (excluded from
the public site).

## Pull requests

- Keep changes focused; one logical change per PR.
- Add or update tests for behavior changes — see the many `*_test.go` files and the
  integration tests in `pkg/orchestrator/` for patterns.
- Make sure `make lint` and `make test` pass.
- Note which agents/models you tested against (Claude, GLM, DeepSeek, Cursor, …) when
  relevant — behavior can differ between agent CLIs.

## Reporting issues

Include the afm version (`afm --version`), the `flow.yaml` (redacted as needed), and
the relevant `.afm/runs/<run>/` logs — the event log and per-stage `<phase>.log` are
usually enough to reproduce.
