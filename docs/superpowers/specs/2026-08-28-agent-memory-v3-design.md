# Agent memory v3 — directory store + pattern-extraction chain

**Date:** 2026-08-28
**Status:** Approved for implementation
**Supersedes:** `docs/superpowers/specs/2026-08-28-agent-memory-v2-design.md` (v2, structured YAML store). v3 **replaces** the v2 storage model and pipeline entirely; it keeps only v2's scheduling/lifecycle envelope (see "Kept").

## Why v3 (over v2)

v2 stored structured YAML findings with per-finding metadata (`evidence`, `confirm_count`, `last_seen`) and a `reflect → consolidator` pipeline. v3 switches to a **pattern-extraction** model whose durable output is a human-readable Markdown rules file (`# Project rules` / `## Pattern`), priority encoded only by order, capped at `max_rules`. Memory becomes a **directory** (not a single file): one project-wide `memory.md` plus optional per-stage files, each stage declaring its own file and read/write mode. The distillation is a fixed chain of prompts (reflect → aggregate → prioritize → select-high → update) run per reflecting stage and once more over the whole run.

## Goal

- `memory:` points at a **directory**; the project-wide rules live in `<path>/memory.md`, accumulating across runs.
- A stage opts in with a `reflect:` **object** (`file`, `mode`) selecting a per-stage memory file and whether the stage reads it, writes it, or both.
- After a writing stage completes, a code-driven chain distills its session into **High-priority project patterns** and merges them into that stage's file.
- At end of run, the same chain runs over the whole run's datasets and merges project-wide High patterns into `memory.md`.
- Preserve v2's P1 win: the reflect step excludes afm/agent-protocol mechanics so memory stays project-specific, not framework boilerplate.
- Optionally `git commit` the memory directory at end of run.

## Kept from v2 (do NOT reimplement)

Only the scheduling/lifecycle envelope survives — the storage model and pipeline are replaced:

- `maybeRunReflection` triggered from `completeStage` after the unblock cascade; background via `concurrency.SpawnDetached`; serialized by `reflectMu`; tracked by `pendingReflections` so `shouldExit` waits; **never touches the FSM**; `reflect_failed` notice on failure (`ui.Publish` + `stagefiles.AppendNotice`).
- The `runMemoryAgent(ctx, memoryAgentSpec) error` seam and its fresh-context executor (`execMemoryAgent`), extended with the new agent kinds.
- The end-of-run pass hook point in `Run()` (v2's `runFinalReflectionOnce` becomes the memory.md pass; guard flag kept).
- The `reflect_dataset.yaml` intermediate artifact under each stage dir.
- `flow.MemoryConfig` block + per-stage opt-in (shape changes — see §1).

## Removed from v2

- The whole structured-store core: `pkg/memory` `Finding`/`Store`/`Reconcile`/`Evict`/`Select`/`Render`/`Load`/`Save` metadata model (evidence/confirm_count/first_seen/last_seen). v3 introduces a smaller `pkg/memory` (see §5).
- The `consolidator` agent + `consolidator.md`.
- `SESSION_MEMORY.yaml` (per-run session store) — replaced by per-stage files.
- Per-stage relevance slicing (`memoryBlockForStage` Select/Render, `retrieval_threshold`, `core_confirm_count`) — memory files are small Markdown, injected as a pointer whole.
- Config `project_file`, `max_findings`, `retrieval_threshold`, `core_confirm_count`, `final_reflect`.

## Non-goals

- No relevance/embedding retrieval — files are small (≤ `max_rules` patterns); inject a pointer, the agent reads them.
- No per-finding metadata (confirm_count/evidence/last_seen). Priority is High/Medium/Low, used only internally for ordering/discard; the file encodes it by order only.
- No crash-recovery of an in-flight chain (best-effort, same as v2).
- No `git push` — `commit` only commits locally.

## Architecture

### 1. Config surface & validation

`flow.MemoryConfig` (replaces v2 fields):

| Field | Type | Default | Meaning |
|---|---|---|---|
| `path` | `string` | `""` | **Directory** (rel. `root_dir`) for memory. **Non-empty enables the feature.** Project-wide file is `<path>/memory.md` (fixed name). |
| `max_rules` | `int` | `25` | Max patterns kept per file (project and per-stage). |
| `commit` | `bool` | `false` | If true, git-commit `<path>` at end of run. |

Per-stage `flow.Stage.Reflect` becomes an **object** (was a `bool` in v2):

```go
type Reflect struct {
    File string `yaml:"file"` // rel <memory.path>; may be "dir/file.md"
    Mode string `yaml:"mode"` // "r" | "w" | "rw"
}
Reflect *Reflect `yaml:"reflect,omitempty"` // nil = no per-stage reflection
```

- `reflect` present ⇒ `File` required (non-empty), `Mode` ∈ {`r`,`w`,`rw`} (default `rw` if omitted).
- **Validation (`Flow.validate`):** any stage with `reflect` while `memory.path` is empty → parse error; invalid `mode` → parse error; empty `file` → parse error. A `script:` stage may declare `reflect` but its **write** chain is silently skipped at runtime (no agent session); its `r` injection still applies.
- Defaults applied in `ParseFile` when memory enabled: `max_rules=25`.

### 2. Storage layout

Under `<root_dir>/<memory.path>/`:
- `memory.md` — project-wide rules, cross-run, Markdown `# Project rules` / `## Pattern`. Created on first end-of-run pass if absent.
- `<reflect.file>` per declaring stage (e.g. `file.md`, `sub/dir/file.md`) — that stage's own High patterns, same Markdown format. Path is resolved relative to `<memory.path>`; parent dirs created as needed.

The file format (produced by the `update` prompt):
```markdown
# Project rules

## [Pattern Name]

[Pattern description]
```
Repeated per pattern. **Priority is encoded ONLY by order** (high blocks first, then medium, then low) — no tier headings, no tier words in names/descriptions. English only.

### 3. The pattern-extraction chain

A stage reflects (runs the write chain) when: `memory.path != ""` AND `stage.Reflect != nil` AND `mode ∈ {w, rw}` AND the stage is not a script stage. Background, best-effort, serialized (`reflectMu`), same envelope as v2. Four LLM steps + code selection:

1. **reflect** (`reflect.md`) — reads the stage session (`<phase>.log` + summary/plan; may read `<phase>.jsonl`) and writes the RL-style dataset to `<stageDir>/reflect_dataset.yaml` — one YAML doc with `project_level` and `session_level`, each item `{prompt, chosen, rejected}` (block-literal style). **P1: a hard EXCLUDE list** for afm/agent-protocol mechanics (execution_summary format, `$AFM_STAGE_DIR`, dialog/question file naming, plan-approval/autonomous flow, retry/backoff, "read the memory files") is added to this prompt so the dataset stays project-specific.
2. **aggregate** (`aggregate.md`) — reads the dataset(s) and extracts **project-level patterns** with no specific citations, mutually exclusive, as a numbered list `name + description`. Writes `<stageDir>/patterns.md`.
3. **prioritize** (`prioritize.md`) — assigns every pattern to exactly one of High/Medium/Low; all three tiers must be used. Writes `<stageDir>/prioritized.md`.
4. **code (select High)** — afm parses the prioritized output, keeps only the High tier. (Deterministic; no LLM.)
5. **update** (`update.md`) — merges the High patterns into the existing target file (`<path>/<reflect.file>` per-stage, or `<path>/memory.md` end-of-run), preferring to merge into existing patterns, else create new; then internally re-tiers, caps at `max_rules` (drop low first, then medium, preserve high), and rewrites the file in the Markdown format above (priority by order, English). The `15` literal in the prompt is parameterized with `max_rules`.

**Per-stage:** steps 1-5 run on that stage's dataset → its `<reflect.file>`.
**End-of-run:** once when memory enabled and ≥1 dataset exists — steps 2-5 run over **all** stages' `<stageDir>/reflect_dataset.yaml` (aggregate across the run) → merge into `memory.md`. (Step 1/reflect already produced the per-stage datasets during the run; the end-of-run pass reuses them.)

All steps go through the `runMemoryAgent` seam. Agents are fresh-context, no `AFM_STAGE_DIR`, separate log files (`reflect.log`/`aggregate.log`/`prioritize.log`/`update.log`) under the stage dir (end-of-run: under the run dir). Intermediate `.md`/`.yaml` byproducts stay on disk (auditable).

afm owns the final file write only in the sense that the `update` agent writes the target file directly (Markdown), matching v2's realization that agents write their own output; afm code owns select-High, path resolution, and the commit.

### 4. Injection (read side)

Pointer-based, per stage, at prompt build (via `prompts.Inputs.MemoryBlock`, kept from v2):
- If `<path>/memory.md` exists → a pointer to it is injected into **every** stage's prompt (project memory, always).
- If the stage has `reflect` with `mode ∈ {r, rw}` → a pointer to `<path>/<reflect.file>` is also injected into **that** stage's prompt.
- Files are small Markdown; the pointer names the absolute path(s) and instructs the agent to read them. No inlining, no slicing.
- `mode: w` → the stage's file is written but NOT injected into that stage.

`memoryBlockForStage` (kept, rewritten): builds this pointer block from the memory dir + the stage's reflect mode. Empty when memory disabled and neither file applies.

### 5. `pkg/memory` (rewritten, smaller)

Pure code core, no LLM:
- Path resolution: `MemoryDir(root, path)`, `ProjectFile(dir)` = `<dir>/memory.md`, `StageFile(dir, reflect.File)`.
- `SelectHigh(prioritized string) []Pattern` (or the raw High section) — parse the prioritize step's output, return the High-tier items. Deterministic.
- `max_rules` cap enforcement lives in the `update` prompt (parameterized). v3 adds **no** code guard/trim on top (YAGNI); if the agent overshoots, the next `update` self-corrects. No `CountPatterns` helper in v3.
- `Commit(dir, message) error` — `git add <dir>` + `git commit` (best-effort, only if changes, no push), used when `commit: true`.
- Atomic writes for any file afm writes directly (temp+rename).

The heavy v2 types (Finding/Store/Reconcile/Evict/Select-by-metadata) are deleted.

### 6. commit

When `memory.commit == true`: after the end-of-run `memory.md` pass, afm runs `git add <memory.path>` + `git commit -m "<standard message>"` scoped to the memory dir, best-effort (log a notice on failure, never fail the run), only if there are staged changes, no push. Runs on whatever branch the flow is on.

### 7. Prompts

`orchestrator.Prompts`: keep `Reflect`; **replace `Consolidator` with `Aggregate`, `Prioritize`, `Update`**. Embedded files `assets/prompts/{reflect,aggregate,prioritize,update}.md`; `loadPrompts` names updated; delete `consolidator.md` (and any leftover v1 updater/compressor). `reflect.md` = the RL-dataset prompt + the P1 exclude list. `aggregate.md`/`prioritize.md`/`update.md` = the user-supplied prompts (steps 4/5/7), with `update.md`'s cap parameterized by `max_rules`. `buildMemoryPrompt(Prompts, memoryAgentSpec)` gains `aggregate`/`prioritize`/`update` kinds and drops `consolidator`.

## Testing

- **`pkg/flow`**: parse `memory.{path,max_rules,commit}` and `reflect{file,mode}` object; defaults (`max_rules=25`, `mode` default `rw`); validation (reflect requires path; bad mode; empty file; script-stage reflect parses).
- **`pkg/memory`** (rewritten, unit): path resolution; `SelectHigh` parsing (High-only, tolerant of the prioritize format); `Commit` (temp git repo — adds+commits only on change, no push); atomic write.
- **Pipeline** (stub `runMemoryAgent`): per-stage runs reflect→aggregate→prioritize→update in order and writes the stage file; end-of-run aggregates all datasets → memory.md; `mode` gates (w writes-not-injects, r injects-not-writes, rw both); script stage skips write; disabled → no-op; `pendingReflections`/`reflectMu`/no-FSM invariants hold.
- **Injection** (`pkg/prompts` + orchestrator): `memory.md` pointer in every stage; stage file pointer only for r/rw; none when w-only or disabled.
- **commit**: `commit:true` triggers a git commit of the memory dir at end of run (integration with a temp repo); `false` does nothing.
- **Integration**: a `reflect:{file,mode:rw}` stage produces `reflect_dataset.yaml` + writes its file with `# Project rules`; end-of-run writes `memory.md`; a later stage's prompt carries the memory pointer.

## Open items fixed as defaults

- `max_rules` default 25; per-stage `mode` default `rw`.
- End-of-run memory.md pass always runs when memory enabled and ≥1 dataset exists (no separate flag).
- Chain is the full 4-LLM-step chain per reflecting stage + once at end (user chose to keep 4 steps; not collapsed).
- `commit` message: a fixed standard string (e.g. `chore(memory): update project memory`); no push.
