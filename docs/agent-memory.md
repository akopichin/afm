# Agent memory

Each stage runs its agent in an isolated context — whatever it learns (an API's real
behavior, a required build flag, a rule it broke and had to correct) is lost when the
stage finishes, and the next stage starts blind. **Agent memory** carries those
lessons forward: after a stage that opts in completes, afm runs a small background
pipeline that distills the stage's session into a handful of durable **project
patterns** and merges them into a plain Markdown rules file that later stages — and
later runs — are told to read.

Memory is entirely **opt-in** and off by default. Nothing runs and nothing is written
unless you add a `memory:` block to the flow and mark at least one stage with
`reflect:`.

## Turning it on

```yaml
name: my-feature
root_dir: .
memory:
  path: docs/memory        # a DIRECTORY (relative to root_dir); a non-empty value ENABLES the feature
  mode: rw                  # lifecycle of the shared memory.md: r / w / rw (default rw)
  memory_use: true          # do stages READ memory at all? default false → you opt in
  max_rules: 25             # max patterns kept per file (default 25)
  commit: false             # git-commit the memory directory at end of run (default false, no push)
stages:
  - id: build
    name: build
    agents: [planning, implementation]
    reflect: { file: build.md, mode: rw }   # this stage's own file: writes AND reads it
  - id: test
    name: test
    agents: [planning, implementation]
    depends_on: [build]
    reflect: { file: build.md, mode: r }    # reads build's file, writes nothing of its own
  - id: docs
    name: docs
    agents: [planning, implementation]
    depends_on: [build]
    memory_use: false                        # opt THIS stage out of reading memory entirely
```

**Flow-level `memory:` fields:**

| Field | Default | Meaning |
|-------|---------|---------|
| `path` | — | Directory (relative to `root_dir`) where memory files live. **A non-empty value is what enables the whole feature.** |
| `mode` | `rw` | Lifecycle of the **shared `memory.md`**: `r` = read-only (injected, never rewritten), `w` = write-only (updated at end of run, never injected), `rw` = both. |
| `memory_use` | `false` | Master switch for **reading** memory into stage prompts. Off by default. Does not affect writing. |
| `max_rules` | `25` | Maximum number of `## Pattern` blocks kept in each file. When the distiller would exceed it, it drops Low-priority patterns first, then Medium, preserving High. |
| `commit` | `false` | When `true`, afm runs `git add`/`git commit` **scoped to the memory directory** at the end of the run (only if something changed; never pushes). |

**Per-stage fields:**

| Field | Default | Meaning |
|-------|---------|---------|
| `reflect` | — | `{ file, mode }` — the stage's own memory file (relative to `memory.path`) and its `mode` (`r`/`w`/`rw`, default `rw`). Controls **this stage's own file** only. |
| `memory_use` | inherit | Overrides the flow-level `memory_use` for this stage. Unset = inherit. Controls **reading** only. |

## How reading and writing are decided

Reading and writing are two **independent** axes.

**Reading (what memory the stage's agent is pointed at):**

1. First a participation gate — `memory_use`, resolved as *stage `memory_use` if set,
   else the flow-level `memory_use` (default `false`)*. If it resolves to `false`, the
   stage gets **no memory at all**.
2. If it participates, it is pointed at:
   - the shared **`memory.md`** — only if `memory.mode` includes read (`r`/`rw`) and
     the file exists;
   - its **own `reflect.file`** — only if it has `reflect` with `mode` `r`/`rw` and the
     file exists.

**Writing (what gets distilled after the run):**

- a stage's **own `reflect.file`** is written if its `reflect.mode` includes write
  (`w`/`rw`) — independent of `memory_use`;
- the shared **`memory.md`** is (re)written by the end-of-run pass only if
  `memory.mode` includes write (`w`/`rw`).

So, for example: `memory.mode: r` + stages with `reflect: {mode: rw}` gives you a
**read-only shared memory** (curated by hand, never overwritten) while each stage
still reads and writes its own file. And with the default `memory_use: false`, reflect
stages still *write* their files but nothing is *read* until you flip
`memory_use: true`.

## What ends up on disk

Two tiers of files, both plain Markdown, all under `<memory.path>/`:

- **`memory.md`** — the project-wide rules file. Accumulates **across runs** (keep it
  in git). Injected into a stage's prompt when the stage participates (`memory_use`)
  and `memory.mode` allows reading.
- **`<reflect.file>`** (e.g. `build.md`) — a per-stage file, rewritten by that stage's
  write chain, injected only into a participating stage that names it with `mode`
  `r`/`rw`.

Both files have the same shape — a flat list of named patterns, highest-priority first
(priority is encoded **only** by block order, never written into the file):

```markdown
# Project rules

## Single Source of Truth Propagation

Treat the project's canonical config file as authoritative and propagate its exact values into every derived output.

## Exact Path Fidelity

Reproduce target paths precisely and verify the written file resolves to the intended location before declaring success.
```

afm reads the memory content by **pointing the agent at the file paths** (the agent
reads them itself with its normal tools); it does not paste the file contents into the
prompt, so prompts don't grow as memory grows.

## How a stage's session becomes patterns

The distill chain runs **once per reflecting stage** (writing that stage's own file)
and **once more at the end of the run** (aggregating every stage's session into
`memory.md`). Four steps:

1. **reflect** — a fresh-context agent reads the stage's session log and produces a
   raw RL-style dataset (`reflect_dataset.yaml`). Its prompt carries a hard **exclude
   list** for afm/agent-protocol mechanics so memory stays about **your project**, not
   the framework.
2. **aggregate** — turns the dataset into a mutually-exclusive numbered list of named
   patterns.
3. **prioritize** — buckets every pattern into High / Medium / Low.
4. afm code keeps only the **High** patterns, then **update** — merges them into the
   target file (preferring to fold into existing patterns), caps at `max_rules`, and
   rewrites the file.

The pipeline is **background and best-effort**: it never blocks downstream stages,
never touches a stage's status, and never fails a stage or the run — if a step errors
you get a `reflect_failed` notice in the dashboard and nothing else. The four prompts
ship as embedded defaults (`reflect.md` / `aggregate.md` / `prioritize.md` /
`update.md`) and can be overridden per project via the `prompts_dir` config.

## Practical notes

- **Cross-run is the main payoff.** Because reflection runs in the background after a
  stage finishes, a *fast* downstream stage in the **same** run may start before the
  previous stage's memory has been written — so in-run forward carry isn't guaranteed.
  What *is* reliable is accumulation **across runs**: `memory.md` and the per-stage
  files are read at the start of every run.
- **Commit it (or ignore it).** `memory.md` is meant to live in your repo and grow over
  time. If you'd rather not track it, add `<memory.path>/` to `.gitignore`; if you want
  afm to commit it automatically after each run, set `commit: true`.
- **Script stages** may declare `reflect:` for reading, but their write chain is always
  skipped — there is no agent session to reflect on.

## `afm memory rebuild` — backfilling memory from a past run

The normal write chain only runs live, right after each stage finishes during
`afm run`. `afm memory rebuild [flow.yaml]` runs the same distill pipeline **offline**,
against the session logs of a run that already finished — for turning on
`memory`/`reflect` on a flow you'd already been running, for regenerating memory after
editing the prompts, or for recovering from a failed/aborted live reflection.

```bash
afm memory rebuild                       # rebuild for the flow in .afm/flows (or the only one present)
afm memory rebuild flow.yaml             # explicit flow file
afm memory rebuild --run my-flow-20260910-153000-ab12   # a specific historical run, not just the latest
afm memory rebuild --dry-run             # show what would change, write nothing
afm memory rebuild --force-reflect       # regenerate every reflect_dataset.yaml from raw logs
afm memory rebuild --commit              # force a git commit of changed files, overriding flow.yaml
afm memory rebuild --no-commit           # force NO commit, overriding memory.commit: true in flow.yaml
```

| Flag | Default | Meaning |
|------|---------|---------|
| `--run <id>` | latest completed run | Explicit run directory name (bare basename) to rebuild from, instead of auto-selecting the latest completed one. |
| `--dry-run` | `false` | Compute and print the diff for every target that would change; write nothing and never commit. |
| `--force-reflect` | `false` | Ignore any existing `reflect_dataset.yaml` and regenerate it from the stage's raw session logs. |
| `--commit` | — | Commit changed memory files at the end, regardless of `memory.commit`. Mutually exclusive with `--no-commit`. |
| `--no-commit` | — | Never commit, even if `memory.commit: true`. Mutually exclusive with `--commit`. |

Key behaviors:

- **Effective commit decision:** an explicit `--commit`/`--no-commit` always wins;
  otherwise it falls back to the flow's `memory.commit` (default `false`); `--dry-run`
  always disables committing.
- **Run selection: completed-only, exact stage-set match, newest wins.** Without
  `--run`, afm scans runs newest-first and picks the first one that is fully done AND
  whose stage set exactly matches the current flow's stage IDs. A newer completed run
  whose topology has since drifted is silently skipped — so the command may end up
  analyzing a substantially older run. Check the run id printed to stdout. An explicit
  `--run <id>` bypasses this filter (a stage-set mismatch there is a hard error).
- **"Current YAML + current prompts, historical logs."** Rebuild always uses your
  current `flow.yaml` and current prompt templates — only the session logs being
  distilled come from the past. That's what makes backfill useful.
- **Dataset reuse.** Each stage's `reflect_dataset.yaml` is the reusable artifact of
  the reflect step. If a valid one exists, rebuild reuses it and skips re-running the
  reflect agent; `--force-reflect` discards it and regenerates from raw logs.
- **Never mutates the historical run.** Rebuild reads `events.jsonl`/stage directories
  read-only; only files under the memory directory (and the attempt's own audit
  workspace under `<runDir>/memory-rebuild/<attempt-id>/`) are written. A run held by a
  live `afm run` is rejected immediately.
- **Docker mode** is honored exactly like `afm run`: if Docker is enabled it re-execs
  into the container before taking any locks.

> **Note on `memory.path` resolution.** With no `root_dir` set, `afm run` resolves a
> relative `memory.path` against `--dir`/`AFM_DIR`, while `afm memory rebuild` resolves
> it against its own current working directory. To guarantee both target the same
> directory, set an explicit `root_dir`, make `memory.path` absolute, or run both from
> the same directory.
