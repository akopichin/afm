# Agent memory

Each stage runs its agent in an isolated context. **Agent memory** carries project
lessons across runs: an API's real behavior, a required build flag, or a rule the
agent had to correct. After a participating stage finishes, afm captures its session
in a background reflection step. At the end of the run, it turns those datasets into
Markdown rules files for future runs. New rules are not passed to downstream stages
within the same run.

Memory is **opt-in**: a non-empty `memory.path` enables it. Reading existing memory
also requires `memory_use: true`; producing new memory requires at least one
non-script stage with writable `reflect`. These switches are independent: you can
read existing shared memory without declaring `reflect`, or collect new rules while
reading is disabled.

## Turning it on

```yaml
name: my-feature
root_dir: .
memory:
  path: docs/memory          # a DIRECTORY (relative to root_dir); a non-empty value ENABLES the feature
  mode: rw                  # lifecycle of the shared memory.md: r / w / rw (default rw)
  memory_use: true          # do stages READ memory at all? default false → you opt in
  max_rules: 25             # max patterns kept per file (default 25)
  commit: false             # git-commit the memory directory at end of run (default false, no push)
stages:
  - id: build
    name: build
    agents: [planning, implementation]
    reflect: { file: build.md, mode: rw }   # reads existing rules; updates them at run end
  - id: test
    name: test
    agents: [planning, implementation]
    depends_on: [build]
    reflect: { file: build.md, mode: r }    # reads rules from earlier runs, not this build's new lessons
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

- a non-script stage participates in capture when its `reflect.mode` includes
  write (`w`/`rw`) — independent of `memory_use`;
- at run end, the available datasets from those stages update their
  **`reflect.file`** targets;
- the same datasets also update the shared **`memory.md`** if `memory.mode`
  includes write (`w`/`rw`). Stages without writable `reflect` do not contribute
  to this shared pass. Without captured datasets, there is nothing to finalize.

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
- **`<reflect.file>`** (e.g. `build.md`) — a rules file updated at run end from the
  stages that name it with write permission. Multiple stages can share one target;
  their updates are combined in stage order. A participating stage is pointed at it
  only when its `reflect.mode` allows reading.

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

The pipeline has **two phases**. Capture runs in the background after each eligible
stage finishes, without blocking downstream stages. Normal run completion waits for
pending captures, then finalizes memory once before afm exits. Finalization can
therefore add time at the end of a run.

The complete chain has five steps, with four agent calls:

1. **reflect (capture)** — a fresh-context agent reads the stage's session logs,
   plans/summaries and direct user input, including dialog answers, pre-notes and
   feedback. It produces `<stageDir>/reflect_dataset.yaml`. User instructions take
   priority; afm dialog/completion mechanics are excluded from project patterns.
2. **aggregate (finalize)** — turns captured datasets into a numbered list of named
   patterns.
3. **prioritize (finalize)** — assigns High / Medium / Low priority, preserving the
   priority of direct user input.
4. **select High (finalize, code only)** — keeps High patterns. Empty datasets or no
   High patterns preserve the existing target content.
5. **update (finalize)** — merges High patterns into a staged copy of the target,
   folds them into existing rules where possible, and enforces `max_rules`.

Finalization runs steps 2–5 for each contributing stage's target and, when enabled,
once for the shared `memory.md` using all eligible non-empty datasets. It reuses the
captured datasets; it does not repeat reflection. All candidate files are prepared
before publication, under a shared memory-directory lock also used by rebuild.
The lock remains held through the optional memory commit.

Live memory is **best-effort**: capture, finalization or commit errors produce a
`reflect_failed` dashboard notice without changing stage status or failing an
otherwise successful run. An interrupted run can leave captured datasets without
updated rules; rebuild can finish this work once the run satisfies its completion
requirements. The four prompts ship as embedded defaults (`reflect.md`,
`aggregate.md`, `prioritize.md`, `update.md`) and can be overridden via `prompts_dir`.

## Practical notes

- **New lessons become available on future runs.** Downstream stages in the current
  run still see the existing rules, even if an earlier stage's capture has finished.
  When building a participating agent's prompt, afm points it at the memory files
  that already exist; it does not inject new reflection datasets.
- **Commit it (or ignore it).** `memory.md` is meant to live in your repo and grow over
  time. If you'd rather not track it, add `<memory.path>/` to `.gitignore`; if you want
  afm to commit it automatically after each run, set `commit: true`.
- **Script stages** are excluded from capture, even if they declare `reflect:` —
  there is no agent session to reflect on.

## `afm memory rebuild` — backfilling memory from a past run

`afm memory rebuild [flow.yaml]` runs the same capture/finalize pipeline **offline**
against a completed run. Use it to enable memory for past sessions, regenerate rules
after changing prompts, or recover from a failed live memory update. All stages of
the selected run must be done; this command does not resume unfinished stage work.

```bash
afm memory rebuild                       # rebuild for the flow in .afm/flows (or the only one present)
afm memory rebuild flow.yaml             # explicit flow file
afm memory rebuild --run my-flow-20260910-153000-ab12   # a specific historical run, not just the latest
afm memory rebuild --worktree my-feature flow.yaml         # rebuild a run in .worktrees/my-feature
afm memory rebuild --dry-run             # run the pipeline and show diffs without publishing changes
afm memory rebuild --force-reflect       # regenerate every reflect_dataset.yaml from raw logs
afm memory rebuild --commit              # force a git commit of changed files, overriding flow.yaml
afm memory rebuild --no-commit           # force NO commit, overriding memory.commit: true in flow.yaml
```

| Flag | Default | Meaning |
|------|---------|---------|
| `--run <id>` | latest completed run | Explicit run directory name (bare basename) to rebuild from, instead of auto-selecting the latest completed one. |
| `--worktree <name>` | unset | Select runs prefixed by `<name>` and resolve relative `memory.path` inside `<project-root>/.worktrees/<name>`. The worktree must already exist. |
| `--dry-run` | `false` | Run agents and print target diffs, writing only attempt artifacts; never publish memory/datasets or commit. |
| `--force-reflect` | `false` | Regenerate datasets from session sources instead of reusing them; replace canonical datasets only during publication. |
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
  analyzing a substantially older run. Without `--worktree`, only runs prefixed by
  the flow name are considered. For a run made in a worktree, pass `--worktree`
  with that worktree's name, even when it matches the flow name, so rebuild also
  uses the worktree as its memory root. Check the run id printed to stdout. An
  explicit `--run <id>` must match the selected run prefix and bypasses the
  completed-run filter (an incomplete run or stage-set mismatch is a hard error).
- **"Current YAML + current prompts, historical logs."** Rebuild always uses your
  current `flow.yaml` and current prompt templates — only the session logs being
  distilled come from the past. That's what makes backfill useful.
- **Dataset reuse.** Each stage's `reflect_dataset.yaml` is the reusable artifact of
  the reflect step. If a valid one exists, rebuild reuses it and skips re-running the
  reflect agent; `--force-reflect` generates a replacement in the attempt workspace.
- **Run state is unchanged.** Rebuild does not change the FSM or `events.jsonl`.
  It writes audit artifacts under `<runDir>/memory-rebuild/<attempt-id>/` and may
  publish regenerated datasets back to the stage directories as well as updating
  the memory files. A run held by a live `afm run` is rejected immediately.
- **Shared writers are serialized.** Capture happens before the memory lock; then
  rebuild waits for exclusive access to the memory directory, rechecks commit
  preconditions, finalizes and optionally commits. Live finalization uses the same
  lock. A dry run still runs agents and creates logs, candidates and `manifest.json`,
  but leaves published memory and canonical datasets unchanged.
- **Failures are reported.** Unlike live reflection, rebuild exits with an error if
  capture, finalization, commit or manifest persistence fails. Candidates are prepared
  before publication, but replacement is atomic per file, not across the entire
  set. The attempt's manifest records published files and its backups preserve old
  memory content if publication stops partway through.
- **Docker mode** is honored exactly like `afm run`: if Docker is enabled it re-execs
  into the container before taking any locks.

> **Note on `memory.path` resolution.** With `--worktree`, rebuild resolves a relative
> `memory.path` inside that worktree, as `afm run` does. Without `--worktree` and with
> no `root_dir` set, `afm run` resolves it against `--dir`/`AFM_DIR`, while rebuild
> resolves it against its own current working directory. To guarantee both target
> the same directory, set an explicit `root_dir`, make `memory.path` absolute, or
> run both from the same directory.
