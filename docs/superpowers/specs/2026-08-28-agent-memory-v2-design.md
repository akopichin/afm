# Agent memory v2 — structured, afm-owned store

**Date:** 2026-08-28
**Status:** Approved for implementation
**Supersedes:** the storage/pipeline layer of `docs/superpowers/specs/2026-08-28-agent-memory-design.md` (v1). The v1 enablement model (`memory:` flow block, per-stage `reflect: true`, background/best-effort/serialized pipeline, `final_reflect`) is kept; the file format, the agent pipeline, and the growth-control mechanism are replaced.

## Problem (what v1 got wrong)

v1 shipped and works, but a live 3-stage run exposed four weaknesses:

1. **Signal quality (P1).** The captured memory was dominated by **generic afm/agent-protocol mechanics** ("write execution_summary with three sections", "read `$AFM_STAGE_DIR`", "don't wait for plan approval on an autonomous stage") — noise that every stage rediscovers — rather than durable, project-specific findings (an API's real behavior, a required build flag, the true entry point). The reflect prompt's RL/RLHF "rules/errors" framing biases toward process boilerplate.
2. **No retrieval (P2).** Every stage is pointed at the *entire* memory. As it grows, later agents read an ever-larger file each time — cost plus distraction. There is no relevance scoping.
3. **Unstructured store (P3).** Prose "Do's/Don'ts" markdown can't be reliably parsed, deduplicated by key, or evicted by value. Growth control is lossy LLM compression + a blunt FIFO drop, with no notion of confidence/recency.
4. **Robustness (P4).** The updater/compressor **agents rewrite the `.md` files in place with their own Write tool**, so a killed agent can leave a partially written file; afm can't enforce structure or size deterministically.

## Goal

Replace the prose store with a **structured, afm-owned store** so that afm (code) controls dedup-metadata, eviction, and atomic writes, while LLM agents do only what needs judgment (extracting findings, semantic consolidation). Retune extraction toward project-specific facts, and inject only the relevant slice into each stage.

## Non-goals (unchanged from v1 or explicitly out)

- **No crash recovery of an in-flight pipeline.** Still best-effort; an afm crash mid-pipeline loses that stage's reflection.
- **No `--resume` of the stage's own session.** reflect stays fresh-context.
- **No embeddings / vector retrieval.** P2 retrieval is deterministic tag/keyword matching in code (no new infra).
- **No migration from v1 `.md` files.** The format changes (`.md` → `.yaml`); a pre-existing `.md` is simply not read, the structured store starts empty. The feature is new with few users.
- **No `.md` human-render layer.** Agents read the structured YAML directly (LLMs handle YAML); a pretty markdown render is deferred (YAGNI).

## What is KEPT from v1 (do not reimplement)

The entire enablement + scheduling layer is unchanged — only the format/pipeline/growth-control inside it change:

- `memory:` block on `flow.Flow` + per-stage `Stage.Reflect bool` (opt-in, default false; script stages skipped). Validation that `reflect`/`final_reflect` require `memory.project_file`.
- `maybeRunReflection` triggered from `completeStage` after the unblock cascade; background via `concurrency.SpawnDetached`; serialized by `reflectMu`; tracked by `pendingReflections` so `shouldExit` waits; **never touches the FSM**; `reflect_failed` notice on failure.
- Per-run `SESSION_MEMORY` reset at `Run()` start (`initSessionMemory`).
- `final_reflect` runs the same pipeline once over the whole run at `Run()` exit, guarded by `finalReflectDone`.
- The `runMemoryAgent(ctx, memoryAgentSpec)` seam and its fresh-context executor (`execMemoryAgent`) — extended with the new agent kinds, not replaced.
- `atomicWriteFile` (`memory_size.go`) — reused as the write primitive.

## Architecture

### 1. The structured store

Two files, same scopes/lifetimes as v1 (PROJECT cross-run in repo at `memory.project_file`; SESSION per-run at `.afm/runs/<run>/SESSION_MEMORY.yaml`), but **YAML, owned by afm**. `memory.project_file` now names a `.yaml`.

Go type (`pkg/state` or a new `pkg/memory` package — see §6):

```go
type Finding struct {
	ID           string   `yaml:"id"`            // stable short slug; afm keeps it stable across runs
	Scope        string   `yaml:"scope"`         // "project" | "session"
	Kind         string   `yaml:"kind"`          // "fact" | "best_practice" | "anti_pattern"
	Topic        []string `yaml:"topic"`         // tags, for retrieval matching (P2)
	Statement    string   `yaml:"statement"`     // the finding itself
	Evidence     string   `yaml:"evidence"`      // REQUIRED: file:line / command / observation
	FirstSeen    string   `yaml:"first_seen"`    // run id
	LastSeen     string   `yaml:"last_seen"`     // run id
	ConfirmCount int      `yaml:"confirm_count"` // ++ on re-confirmation → "core" when high
	SourceStage  string   `yaml:"source_stage"`
}

type Store struct {
	Findings []Finding `yaml:"findings"`
}
```

**afm owns all metadata** (`id` stability, `first_seen`/`last_seen`/`confirm_count`). Agents never set metadata; afm assigns/reconciles it (see §2 step 3). A finding with an empty `Evidence` is rejected by afm at validation time (P1 — evidence is mandatory).

### 2. The pipeline (per `reflect: true` stage; also the final pass)

Same background/best-effort/serialized envelope as v1. **The compressor agent is removed** (consolidation + code eviction replace it), so the pipeline is **two agents**, down from three:

1. **reflect** (LLM, fresh context) — reads the stage session (`<phase>.log` + `execution_summary.md`/`plan.md`; may read `<phase>.jsonl`) and writes **candidate findings** as a `Store` YAML to `<stageDir>/reflect_dataset.yaml`. Retuned prompt (P1):
   - capture only **project-specific, durable** facts/practices/anti-patterns;
   - **explicitly exclude** afm/agent-protocol mechanics (execution_summary format, `$AFM_STAGE_DIR`, dialog file naming, approval/autonomous flow, retry behavior) — a hard "do NOT record" list;
   - every finding MUST carry concrete `evidence`; if there is nothing durable and evidenced, emit an empty `findings: []`;
   - assign `scope`, `kind`, `topic` tags per finding (no metadata fields).

2. **consolidator** (LLM, fresh context) — input: the candidate `Store` + the current persisted `Store` (both scopes). Returns the **merged** `Store` where each finding carries a transient `status: new | reinforced | unchanged` field, and:
   - semantically merges/generalizes duplicates (the v1 updater strength, kept);
   - **verification gate (P1):** drops candidates that are not durable, not project-specific, or lack real evidence — the reason a third "verify" agent isn't needed;
   - preserves the `id` of any existing finding it merges into (so afm can bump its metadata); marks genuinely new findings `status: new`.

3. **afm (code)** — the write owner:
   - **validate** every returned finding against the schema (required fields, known `scope`/`kind`, non-empty `evidence`, valid `id` charset); drop invalid ones with a logged warning (never abort);
   - **reconcile metadata** by `status`: `new` → `first_seen = last_seen = <run-id>`, `confirm_count = 1`, afm assigns a slug `id` if missing/colliding; `reinforced` → `last_seen = <run-id>`, `confirm_count++`; `unchanged` → untouched;
   - **write atomically** (temp+rename via `atomicWriteFile`) to the scope's `.yaml`. The agents never write the store file — only `reflect_dataset.yaml` (their own candidate output).

4. **eviction (code, P3)** — after the write, if a scope's finding count exceeds `max_findings`, drop the lowest-value findings until it fits. Value score: primarily `confirm_count` (higher = keep), tie-broken by `last_seen` recency. Deterministic; no LLM, no FIFO. `reflect_dataset.yaml` stays on disk as a per-stage byproduct.

### 3. Retrieval into the prompt (P2, code)

At prompt-build time (`cmd/afm/run.go` / the pointer path), the injected memory depends on store size:

- **Small store** (total findings ≤ `retrieval_threshold`): behave like v1 — inject a **pointer** to the store file(s); the agent reads all.
- **Large store** (> `retrieval_threshold`): inject an **inlined, bounded slice** of relevant findings, plus a pointer to the full file ("more in `<path>`"). The slice =
  - **core**: every finding with `confirm_count ≥ core_confirm_count` (always included), PLUS
  - **relevant**: findings whose `topic` tags or `statement` keywords overlap the stage's `id`/`name`/`description` tokens (case-insensitive token intersection).

  Both memory files contribute; session findings are always small (per-run) so are inlined whole. Rendering is a compact deterministic text block built in code from the `Finding`s (no agent, no per-stage file).

### 4. Config

```yaml
memory:
  project_file: docs/PROJECT_MEMORY.yaml   # structured YAML now; non-empty still ENABLES the feature
  max_findings: 60          # per-scope eviction threshold (P3)
  retrieval_threshold: 25   # ≤ → inject all (pointer); > → inlined relevant slice (P2)
  core_confirm_count: 3     # confirm_count at/above which a finding is always injected (P2)
  final_reflect: false
```

- **Removed:** `max_bytes`, `compress_retries` (the compressor agent is gone).
- **Defaults:** `max_findings=60`, `retrieval_threshold=25`, `core_confirm_count=3`. Applied in `ParseFile` when memory is enabled (same place v1 defaulted its knobs).
- `SESSION_MEMORY.yaml` remains auto-located and non-configurable.
- Validation unchanged except the field set; `project_file` non-empty still gates everything.

### 5. Prompts

`orchestrator.Prompts` keeps `Reflect` but **replaces `Updater`/`Compressor` with `Consolidator`** (a net −1 embedded prompt). Embedded files: `assets/prompts/reflect.md` (rewritten for P1 + structured YAML output) and `assets/prompts/consolidator.md` (new; merge + verify + structured output with `status`). `loadPrompts` updates its `names` list accordingly. `updater.md`/`compressor.md` are deleted.

`buildMemoryPrompt(Prompts, memoryAgentSpec)` gains the `consolidator` kind (input paths: candidate dataset + current store paths; output: merged store to a path afm then reads) and drops `updater`/`compressor`. The reflect kind now instructs structured YAML output to `reflect_dataset.yaml`.

## Testing

**`pkg/flow`** — parse `memory.{max_findings,retrieval_threshold,core_confirm_count}`; defaults; validation unchanged.

**Code core (heavily unit-tested, no LLM):**
- schema validation: rejects missing/empty `evidence`, unknown `scope`/`kind`, bad `id`; keeps valid.
- metadata reconciliation: `new`/`reinforced`/`unchanged` produce the right `first_seen`/`last_seen`/`confirm_count`; afm-assigned slug ids are stable and collision-free.
- eviction: over `max_findings` drops lowest `confirm_count` (then oldest `last_seen`) until it fits; never drops a higher-value finding before a lower one.
- retrieval selection: ≤ threshold → "inject all" signal; > threshold → core (by `core_confirm_count`) ∪ tag/keyword matches against a stage's tokens; deterministic ordering.
- atomic store write (temp+rename); a torn write never corrupts the previous store.

**Pipeline (agents stubbed via the `runMemoryAgent` seam, cf. `injectFixStub`):**
- reflect → consolidator order; consolidator's `status` drives metadata; rejected candidates don't land; a running-empty result is a no-op.
- eviction fires after a write that overflows.
- `pendingReflections`/`reflectMu`/best-effort/no-FSM invariants still hold (regression-guard the kept envelope).

**Integration:** a `reflect: true` stage produces `reflect_dataset.yaml`; the store `.yaml` gains a validated finding with correct metadata; a later stage's built prompt contains the relevant slice (small store → pointer; forced large store → inlined core/relevant).

## Open items intentionally fixed as defaults

- Eviction value function = `confirm_count` desc, then `last_seen` recency desc.
- Retrieval relevance = case-insensitive token intersection of finding `topic`+`statement` with stage `id`+`name`+`description`.
- Metadata `run-id` (not wall-clock) is the `first_seen`/`last_seen` stamp — stable, testable, and already threaded through the orchestrator.
