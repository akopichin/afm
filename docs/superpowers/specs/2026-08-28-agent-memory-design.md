# Agent memory (reflect → updater → compressor pipeline)

**Date:** 2026-08-28
**Status:** Approved for implementation

## Problem

Each afm stage runs an agent in isolation. Whatever a stage's agent learns —
that an API behaves a certain way, that the build needs a flag, that a file is
the real entry point, a rule it violated and had to correct — evaporates when
the stage finishes. Later stages (and later runs) start blind and rediscover
the same facts and repeat the same mistakes. afm has no notion of a **project
memory** that accumulates useful findings and feeds them forward.

There is already a shell-based hook mechanism (`script_before`/`script_after`,
`pkg/orchestrator/hooks.go`), but a shell script can't read and distill an
agent session into knowledge — that needs an agent.

## Goal

After a stage completes, optionally run a small **code-driven pipeline of three
specialized agents** that reads the stage's execution session, distills useful
findings, and merges them into project memory files. Subsequent stages are
told where the memory lives and read it themselves. Memory is kept bounded by a
size-triggered compression step. A flow can also run one final reflection over
the **whole flow** at the end.

```yaml
name: my-flow
root_dir: .
memory:
  project_file: docs/PROJECT_MEMORY.md   # relative to root_dir; ENABLES the feature
  max_bytes: 20000                        # per-file size threshold that triggers compression
  compress_retries: 2                     # compression attempts before terminal fallback
  final_reflect: true                     # run one reflection over the whole flow at the end
stages:
  - name: build
    reflect: true                         # opt-in: this stage feeds memory after it completes
  - name: test                            # no `reflect` → default false → does not feed memory
```

## Non-goals

- **No `--resume` of the stage's own session.** The reflect agent runs with a
  **fresh context** and reads the on-disk session artifacts (proven, cheap,
  reliable — the `runJSONFixAgent` pattern). Resume is explicitly out of scope
  for v1.
- **No true LRU eviction.** afm has no per-block recency/usage signal, so the
  terminal size fallback is FIFO-drop-oldest, not LRU. True LRU is future work.
- **No crash-recovery of an in-flight pipeline.** Reflection is a best-effort
  background side effect with no FSM backing; a crash mid-pipeline loses that
  stage's reflection (v1).
- **No prompt customization surface beyond `prompts_dir`.** The three prompts
  ship as embedded defaults, overridable through the existing `prompts_dir`
  mechanism — no per-stage/per-flow prompt fields.

## Background: what already exists (reuse, don't reinvent)

| Mechanism | File | Reuse for |
|---|---|---|
| `maybeRunAfterHook` + `pendingAfterHooks` counter | `pkg/orchestrator/hooks.go`, `orchestrator.go` | Trigger point in `completeStage`, in-flight bookkeeping so the run doesn't exit early |
| `concurrency.SpawnDetached` | `pkg/orchestrator/concurrency/concurrency.go` | Fresh-context, one-shot agent that does NOT take the `max_parallel` semaphore but IS tracked in `agentWG` for clean shutdown |
| `runJSONFixAgent` | `pkg/orchestrator/dialog_poller.go` | Template for an isolated agent (no `--resume`, no `AFM_STAGE_DIR`, its own log file) that reads specific files and rewrites a file in place |
| `assets.ReadPrompt(name, overrideDir)` + `//go:embed prompts` | `assets/assets.go`, `cmd/afm/run.go:loadPrompts` | Ship `reflect.md`/`updater.md`/`compressor.md` as embedded defaults, overridable via `prompts_dir` |
| `Flow.Prompt` → `Options.GlobalPrompt` → every `prompts.Build` | `cmd/afm/run.go`, `pkg/prompts/builder.go` | Inject the memory-pointer block into every subsequent stage's prompt |
| `publishHookNotice` + `notices.jsonl` | `pkg/orchestrator/hooks.go`, `pkg/orchestrator/stagefiles` | Surface a `reflect_failed` notice that survives reload |
| `Stage.AutoRun *bool` gate pattern | `pkg/flow/flow.go` | Model for the per-stage boolean (here a plain `bool` — see §2) |

## The three agents

Prompt files live in `assets/prompts/` (embedded), sourced from the groomed
drafts in `tmp/mem_prompts/`:

1. **reflect** (`reflect.md`) — Session Data Analyst / RL dataset engineer.
   Reads the session, outputs **one YAML document** with two sections,
   `project_level` and `session_level`, each item `{prompt, chosen, rejected}`
   (RL/RLHF shape). Writes files only via the code-supplied output path (§4);
   emits no conversational text.
2. **updater** (`updater.md`) — Memory Management Agent. Input: the reflect YAML
   + current `PROJECT_MEMORY.md` and `SESSION_MEMORY.md`. Consolidates,
   deduplicates, generalizes, and rewrites **both** files in the "Do's /
   Don'ts" (🟩 Best Practices / 🟥 Anti-Patterns) format.
3. **compressor** (`compressor.md`) — Knowledge Engineer / distiller. Input: one
   oversized memory file. Performs lossy distillation (30–50% reduction)
   preserving semantics; rewrites the file. Accepts a dynamic tail instruction
   for extreme compression (§4, step 4).

`orchestrator.Prompts` gains three fields (`Reflect`, `Updater`, `Compressor`);
`loadPrompts` gains three names (`reflect.md`, `updater.md`, `compressor.md`).

## Architecture

### 1. Memory scope & lifecycle

- **`PROJECT_MEMORY.md`** — path from `memory.project_file`, relative to
  `root_dir`, lives **in the repo**, accumulates **across runs**. Long-term.
- **`SESSION_MEMORY.md`** — auto-located at `.afm/runs/<run>/SESSION_MEMORY.md`,
  **recreated empty each run** (a header stub is fine). Scoped to the current
  run. Not configurable.
- Both are pointed at from later-stage prompts (see §3); the final reflect (§4b)
  distills the run's SESSION knowledge into cross-run PROJECT memory.

### 2. Config surface & validation

Flow-level nested block `memory` on `flow.Flow`:

| Field | Type | Default | Meaning |
|---|---|---|---|
| `project_file` | `string` | `""` | Path (rel. `root_dir`) to `PROJECT_MEMORY.md`. **Non-empty enables the feature.** |
| `max_bytes` | `int` | `20000` | Per-file byte threshold that triggers compression |
| `compress_retries` | `int` | `2` | Compression attempts before the terminal FIFO fallback |
| `final_reflect` | `bool` | `false` | Run one reflection over the whole flow at completion |

Per-stage on `flow.Stage`:

| Field | Type | Default | Meaning |
|---|---|---|---|
| `reflect` | `bool` (`yaml:"reflect,omitempty"`) | `false` | **Opt-in**: after this stage completes, run the per-stage pipeline. Plain `bool` — the default equals the zero value, so no `*bool` is needed. |

**Validation (`Flow.validate`):**
- Any stage with `reflect: true`, **or** `memory.final_reflect: true`, while
  `memory.project_file` is empty → parse error (`stage %q: reflect requires
  memory.project_file` / `memory.final_reflect requires memory.project_file`).
- A `script:` stage with `reflect: true` → **not** an error; reflection is
  silently skipped at runtime (a script stage has no agent session). Rationale:
  a script stage can legitimately sit inside a memory-enabled flow.

### 3. Memory delivery to later stages (pointer injection)

When `memory.project_file` is non-empty, afm appends a **pointer block** to the
injected `GlobalPrompt` (which already reaches every `prompts.Build` call), for
**every** stage regardless of its own `reflect` value:

> Project memory (accumulated findings from earlier stages) lives at
> `<abs path to PROJECT_MEMORY.md>`. Session memory (this run's context) lives
> at `<abs path to SESSION_MEMORY.md>`. Read both before you start and take
> their Best Practices / Anti-Patterns into account.

afm does **not** read or inline the memory content — the agent reads the files
itself with its `Read` tool. This keeps prompt size constant as memory grows.
Implementation: one branch in `cmd/afm/run.go` where `GlobalPrompt` is built
(`f.Prompt`), appending the pointer block when `project_file` is set. Paths are
made absolute against `root_dir` (mirrors the interactive `AFM_STAGE_DIR`
absolute-path treatment in `builder.go`).

### 4. Per-stage pipeline (`maybeRunReflection`)

Triggered from `completeStage` (`orchestrator.go`), adjacent to
`maybeRunAfterHook`, **after** the unblock cascade has run (background — same
ordering guarantee as `script_after`, chosen deliberately: reflection must NOT
block downstream stages). No-op unless `s.Reflect && project_file != "" &&
!s.IsScript()`.

The pipeline is **code-driven**; each step is a `SpawnDetached` fresh-context
agent with its own log file, CWD = `root_dir`. Data flows through **files**, not
stdout capture (agent stdout is stream-json; extracting clean YAML from it is
unreliable — afm always exchanges via files):

```
1. reflect   reads  <stageDir>/<phase>.log  (+ execution_summary.md / plan.md;
                    may read <phase>.jsonl for detail)
             writes <stageDir>/reflect_dataset.yaml   (project_level / session_level)
                    → kept on disk: free RLHF-dataset byproduct

2. updater   reads  <stageDir>/reflect_dataset.yaml + PROJECT_MEMORY.md + SESSION_MEMORY.md
             rewrites both .md in place (Do's/Don'ts, dedup/consolidate)

3. size-check (CODE):  for each of the two .md: os.Stat().Size() > max_bytes ?

4. if over → compressor rewrites that specific .md in place;
             re-run size-check; repeat up to compress_retries times.
             On the LAST attempt, append a dynamic tail to the compressor prompt:
                "CRITICAL: reduce the total line count of this file to under
                 <N> lines while preserving all core safety principles."
             (<N> derived from max_bytes; the "extreme compression" lifehack.)

5. terminal fallback (CODE): if still over after compress_retries →
             FIFO-drop the oldest blocks from the file (NOT true LRU — no recency
             signal exists) + emit a warning notice. Never silently destroy the
             whole file.
```

**Atomic writes (afm's own direct touches only):** afm's own direct writes —
`initSessionMemory` (session stub) and `fifoDropOldestBlocks` (terminal FIFO
fallback) — go via temp+rename. The updater and compressor **agents** rewrite
`PROJECT_MEMORY.md`/`SESSION_MEMORY.md` in place with their own Write tool, so
a killed agent can leave a partially written memory file. This is accepted:
memory is best-effort and outside crash-recovery — the next run's updater
re-consolidates.

**Agent isolation** (per `runJSONFixAgent`): fresh session (no `--resume`), no
`AFM_STAGE_DIR` (these agents are not dialog participants — they read/write
absolute paths given in the prompt), separate logs `reflect.log` /
`updater.log` / `compressor.log` under `<stageDir>` so their tool actions never
land in the stage's `<phase>.jsonl` (event feed / `WrittenFiles`).

### 4b. Flow-final reflection

When `memory.final_reflect && project_file != ""`: in `Run()`, when
`o.shouldExit()` first becomes true and **before** `return nil`, run the SAME
reflect → updater → size-check → compressor pipeline **once** (guarded by a
`finalReflectDone` flag), where the **reflect** agent reads the logs of **all**
stages of the run (`<run>/*/<phase>.log`) to distill the whole flow's
experience. This runs **synchronously** — `Run()` awaits it before reporting the
run done. By the time `shouldExit()` is true, all per-stage pipelines have
drained (`pendingReflections == 0` is part of `shouldExit`), so memory is
already fully updated and the final pass sees a consistent state and the
complete run.

### 5. Concurrency & lifecycle

- **Serialized writes:** a single orchestrator-level `reflectMu` (a size-1 slot)
  is held for the duration of each pipeline run so that, with `max_parallel >
  1`, pipelines from different stages never write the shared `.md` files
  concurrently. Pipelines queue; they are background/best-effort so queueing
  latency is acceptable.
- **Shutdown bookkeeping:** a new `pendingReflections atomic.Int64` mirrors
  `pendingAfterHooks` exactly — incremented synchronously in
  `maybeRunReflection` before `SpawnDetached`, decremented in the wrapper on
  completion; `shouldExit`/`allTerminal` account for it so the run cannot finish
  while a pipeline is in flight. A long pipeline is bounded by `waitAgents`
  (10s) at shutdown — acceptable for a best-effort side effect.
- **Failure of any step:** best-effort — the FSM is never touched (mirrors
  `runAfterHook`). The pipeline aborts; memory may be left mid-consolidation
  (the updater/compressor agents write the `.md` files in place, not
  atomically — see §4) but never destroyed. A `reflect_failed` notice is surfaced via
  `publishHookNotice`/`notices.jsonl` (live + replayable), with no
  retry/skip blocking UI.
- **Recovery:** after an afm crash, an interrupted pipeline is **not** resumed
  (no FSM transition, no trace in `events.jsonl`). v1 loses that stage's
  reflection.

### 6. SESSION_MEMORY at run start

Created empty (or with a `# SESSION MEMORY` header stub) at run start when
`project_file` is set; the previous run's session memory is never carried into a
new run.

## Testing

**`pkg/flow`**
- Parse `memory.{project_file,max_bytes,compress_retries,final_reflect}` and
  stage `reflect`.
- Defaults: absent `memory` → feature off; absent `reflect` → false.
- Validation: `reflect:true` (or `final_reflect:true`) with empty
  `project_file` → error; `script:` stage with `reflect:true` → parses OK.

**`pkg/prompts` / `cmd/afm` run wiring**
- Pointer block present in `GlobalPrompt` (for both files) when `project_file`
  set, absent otherwise; paths absolute.

**`pkg/orchestrator`** (agents injected via a test stub, cf. `injectFixStub`)
- `maybeRunReflection` is a no-op when `project_file` empty / `reflect:false` /
  script stage; spawns the pipeline otherwise.
- Pipeline order reflect → updater → compressor; compressor runs **only** when a
  file exceeds `max_bytes`.
- `compress_retries` honored; the dynamic line-limit tail appears on the last
  attempt; terminal FIFO fallback fires when still over and emits a warning.
- `pendingReflections` keeps `shouldExit` false while a pipeline is in flight.
- Serialization: two stages completing simultaneously → their pipelines do not
  interleave writes to the shared files (assert via `reflectMu` contention or
  ordered writes).
- Flow-final reflect: runs once at `shouldExit`, reads all stage logs, awaited
  before `Run()` returns; `finalReflectDone` prevents a second run.

**Integration**
- Stage with `reflect:true` writes a `<phase>.log` → mock reflect writes YAML →
  mock updater appends to both `.md` → oversize triggers mock compressor →
  a subsequent stage's prompt contains the memory pointer.

## Open items intentionally deferred to v1 defaults

- Terminal size fallback = FIFO-drop-oldest (not true LRU).
- `max_bytes` default 20000; `compress_retries` default 2.
- Config shape = nested `memory:` block (chosen over flat fields for grouping).
