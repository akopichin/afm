# AFM engine behavior specifications

Baseline specifications of the **current observable behavior** of afm-engine as
consumed by AFM. They describe what the baseline code does, not what it should do;
new behavior is introduced only through deltas.

> Baseline: afm `462bd5d4625303acf07acd337bec425d6cc73ccf` (branch `spec`) ·
> afm-engine `v1.2.1` = `11bddf2c1fe328c36c0315a89742bedb9daab4ba` (the version pinned
> in `go.mod`) · public contracts module `afm:contracts/` at the same AFM commit.
> Status: **draft baseline**, not yet approved.

## Conventions

- One domain per `specs/<domain>/spec.md`. Large domains are split into subdomains
  `specs/<domain>-<sub>/spec.md` that share one ID prefix with disjoint number ranges.
- Sections: Scope (with out-of-scope pointers) · Terms · Requirements · Errors and
  rejections · Coverage gaps and discrepancies.
- IDs are stable and never renumbered: `<PFX>-Rnn` requirement, `<PFX>-Snn` scenario
  (GIVEN / WHEN / THEN), `<PFX>-Enn` error, `<PFX>-Gnn` gap.
- Evidence is `<repo>:<path>` plus a symbol or test name, without line numbers.
  `engine:` is afm-engine, `afm:` is this repository (including `afm:contracts/`).
- When code, tests and docs disagree, the spec follows the **code** and records the
  discrepancy as a gap (`code≠doc`, `test≠doc`, `untested`, `doc-only`, `ambiguous`).

## Domain map

| Prefix | Domain | File | Responsibility |
|---|---|---|---|
| RUN | Run lifecycle | [run-lifecycle](run-lifecycle/spec.md) | Engine ownership `Open → Start → Wait → Shutdown → Close`, readiness, drain, run context |
| RUN | | [run-lifecycle-resume](run-lifecycle-resume/spec.md) | Run directory selection, resume/new run, frozen graph, offline approve/retry/revise |
| RUN | | [run-lifecycle-cli](run-lifecycle-cli/spec.md) | `afm run` composition, modes, hold after result, exit codes, exit-on-fail, `--task` |
| LOG | Event journal | [event-journal](event-journal/spec.md) | `events.jsonl` source of truth, fsync, lock, read-only replay, corruption, timers, legacy |
| LOG | | [event-journal-commit](event-journal-commit/spec.md) | CAS transitions, fences, durable intent, storage-fatal, `notices.jsonl`, redaction |
| STG | Stage lifecycle | [stage-lifecycle](stage-lifecycle/spec.md) | Status FSM, durable CAS, turn ownership, dependencies, command slots, Pause/Continue, cascade |
| STG | | [stage-lifecycle-recovery](stage-lifecycle-recovery/spec.md) | Automatic and manual retry, restart behavior, exit-on-fail, run end condition |
| STG | | [stage-lifecycle-tracks](stage-lifecycle-tracks/spec.md) | Tracks (auto/pre-planned/script), script hooks, artifacts, runtime-added stages |
| DLG | Dialog and approval | [dialog-approval](dialog-approval/spec.md) | Plan approval, `auto_approve`, conditional approval (revision), Revise, buttons, pre-notes |
| DLG | | [dialog-approval-questions](dialog-approval-questions/spec.md) | `afm stage ask` / `wait-answer`, answer delivery, auto-answer, question restart |
| VER | Verify | [verify](verify/spec.md) | When verify runs, YAML forms, steps, verifier isolation, results and feedback |
| VER | | [verify-verdict](verify-verdict/spec.md) | Verdict JSON contract, repair, inconclusive/timeout/retry/usage-limit outcomes |
| VER | | [verify-budget](verify-budget/spec.md) | `max_failures`, `review_patience` stalemate, `fresh_correction` |
| HLP | Helpers (assistants) | [helpers-lifecycle](helpers-lifecycle/spec.md) | Triggers, modes, creation paths, one_shot/checkpoints/on_completion, three kinds of completion |
| HLP | | [helpers-barrier](helpers-barrier/spec.md) | Required barrier, re-checks, failure policy, Revise generation, waivers, task controls, delegation |
| HLP | | [helpers-on-message](helpers-on-message/spec.md) | on_message requests and turns, closing, declared helper loops (`AssistantLoops`) |
| HLP | | [helpers-results](helpers-results/spec.md) | Atomic result commit, publication order, replies to requesters, `helper-result` reads |
| HLP | | [helpers-staleness](helpers-staleness/spec.md) | Turn ownership fences, discarded outcomes, late results, answer acceptance |
| HLP | | [helpers-recovery](helpers-recovery/spec.md) | Replay of helper state, settle/resume at startup, crash points, owed passes |
| HLP | | [helpers-admission](helpers-admission/spec.md) | Admission order, delegation limits, slots, size limits, budgets, usage-limit parks, run-open rules |
| HLP | | [helpers-pre-completion](helpers-pre-completion/spec.md) | `pre_completion` review round in stage view, attach over HTTP, dashboard, `afm check`, MCP, SDK |
| HLP | | [helpers-context](helpers-context/spec.md) | Helper profile `stage_context`: accepted forms, validation against stage inputs, prompt contents |
| MESH | Agent mesh | [agent-mesh](agent-mesh/spec.md) | Collaboration policy and mode, Assistant toggle, roster, team and run agents, tokens, agent jobs |
| MESH | | [agent-mesh-messaging](agent-mesh-messaging/spec.md) | send/reply threads, human member, inbox, turn prompts, limits, redaction |
| MESH | | [agent-mesh-waits](agent-mesh-waits/spec.md) | Non-blocking `wait`, conditions, target checks, refusals, cycle detection, batches |
| MESH | | [agent-mesh-wait-endings](agent-mesh-wait-endings/spec.md) | Exactly-once wait endings, deadlines, cancel, left-unanswered, `run_closed`, owner API |
| MESH | | [agent-mesh-yield](agent-mesh-yield/spec.md) | Yield/park of stages and tasks, resume, exit fence, park recovery |
| MESH | | [agent-mesh-board](agent-mesh-board/spec.md) | Board facts, typed tasks, discussion |
| MESH | | [agent-mesh-loops](agent-mesh-loops/spec.md) | Human/agent loops: creation, limits, slots, delivery, lifetime |
| EXE | Execution environment | [execution-environment](execution-environment/spec.md) | Agent processes, stdin prompt, process groups, env inheritance, secret filtering and precedence |
| EXE | | [execution-environment-workspace](execution-environment-workspace/spec.md) | afm-root, agent CWD, `AFM_STAGE_DIR`, worktrees, skills, prompt assembly |
| EXE | | [execution-environment-docker](execution-environment-docker/spec.md) | Docker re-exec, bare `-e` secrets, prepared commands, wrappers, skill mounts |
| EXE | | [execution-environment-browser](execution-environment-browser/spec.md) | Managed browser policy, sessions, token transport, read-only guarantees |
| HOOK | Lifecycle hooks | [lifecycle-hooks](lifecycle-hooks/spec.md) | Event catalog, payload, delivery queue, timeouts/retries, failure isolation, flush |
| HOOK | | [lifecycle-hooks-env](lifecycle-hooks-env/spec.md) | Hook layering and validation, secret resolution, Docker transport, env and cwd |
| MEM | Agent memory | [agent-memory](agent-memory/spec.md) | Memory config, `ResolveMemoryDir`, prompt pointers, read scope |
| MEM | | [agent-memory-live](agent-memory-live/spec.md) | Capture after stages, finalize at run end, failure notices, budget, commits |
| MEM | | [agent-memory-pipeline](agent-memory-pipeline/spec.md) | Shared processing: inputs, dataset, rule selection, atomic writes, lock |
| MEM | | [agent-memory-rebuild](agent-memory-rebuild/spec.md) | `afm memory rebuild`: preconditions, locks, publish, dry-run, exit codes |
| BUD | Budgets and accounting | [budgets-accounting](budgets-accounting/spec.md) | Usage extraction, pricing, `usage.jsonl`, display toggles, `afm check/report` |
| BUD | | [budgets-accounting-limits](budgets-accounting-limits/spec.md) | Budget declarations, admission checks, hard/medium/info, holds, notices, replay |
| BUD | | [budgets-accounting-control](budgets-accounting-control/spec.md) | Budget control API, exporter, offline views, held-run outcome |
| FLOW | Flow and config contract | [flow-config](flow-config/spec.md) | `flow.yaml` parsing strictness, stage IDs, dependencies, stage kinds, artifacts |
| FLOW | | [flow-config-settings](flow-config-settings/spec.md) | Config layering, env overrides, helper profiles, `afm validate`, schema drift guard |
| FLOW | | [flow-config-pipelines](flow-config-pipelines/spec.md) | Pipeline packages, snapshots, pinning, run binding |
| API | HTTP API | [http-api](http-api/spec.md) | Modes, port checks, hold, `/api/runtime`, guards and tokens, StageConfig/StageView, buttons |
| API | | [http-api-files](http-api-files/spec.md) | File browser and verify report containment |
| API | | [http-api-live](http-api-live/spec.md) | Durable answers/commands, CAS → 409, run-scoped work, `/ws`, reconnect, SDK contract |
| MCP | MCP and serve | [mcp-serve](mcp-serve/spec.md) | `/mcp` endpoint: catalog, run binding, approve, waits, artifacts, sessions, stdio bridge |
| MCP | | [mcp-serve-host](mcp-serve-host/spec.md) | Host `afm serve`: registry, identity, launch/attach, gateway, allowlist |
| MCP | | [mcp-serve-run](mcp-serve-run/spec.md) | `active-runs` registry record, `--task` sources |
| DASH | Dashboard | [dashboard](dashboard/spec.md) | Stage focus mode: ends only on Exit focus, **F** or **Escape**; views open inside it; **View in Feed** in the Stage failed banner |

Cross-domain hot spots: helper waits live in `agent-mesh-waits` (`MESH-R36`) and
`helpers-barrier` (`HLP-R08`); budget holds of helpers in `helpers-admission` and
`budgets-accounting-limits`; offline controls in `run-lifecycle-resume`.

## Out of the baseline

Behavior absent from the baseline commits is not specified here and must arrive as a
delta. The `pre_completion` helper trigger is specified in
[helpers-pre-completion](helpers-pre-completion/spec.md), except HLP-R80 (review order,
correction budget and placement), which is still unpublished. `docs/superpowers/` plans
are not evidence of current behavior.

## Maintaining specs

1. **Size limit.** One `spec.md` must not exceed **350 lines** (~12 KB).
2. **Split on overflow.** If applying a `delta.md` pushes a file over the limit, split it
   in the same iteration into two or more coherent subdomains
   (`specs/<domain>-<sub>/spec.md`), keeping existing IDs.
3. **No lossy compression.** Never delete requirements or GIVEN / WHEN / THEN scenarios to
   fit the limit. Only splitting, or removing obsolete items explicitly listed under
   `## REMOVED`, reduces size.
4. **Index.** After any split or new domain, update the table above (one line of
   responsibility per file) so a planner can find a domain without reading every spec.
5. **Atomic commit.** Spec updates, new subdomains and this README change together in one
   commit at the `Docs & Merge` stage.
6. Gaps are resolved by adding a test (then cite it), fixing the code through a delta, or
   correcting the docs; update the gap entry accordingly instead of silently deleting it.
