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

Before your first commit, enable the pre-commit hook — it runs lint + build + test on
every commit:

```bash
git config core.hooksPath .githooks
```

`core.hooksPath` is local git config, so it must be set once per clone. To skip the
hook for a single commit: `git commit --no-verify`.

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
