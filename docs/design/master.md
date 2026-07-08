# Design Document: `master` — Stage-Level Consumption Accounting (tokens / cost / KB)

Source task: `docs/tasks/master.md`. Source architecture plan: `docs/arch/master.md`. This document
elaborates the 7-cell contract change (already materialized into CODEMANIFEST/`.usages/` files by the
Apply Architecture stage) into a complete, implementation-ready specification.

## Contract Changes

### Changed CODEMANIFEST Files

- `pkg/state/CODEMANIFEST` — additive method `Store.History()` (replay-derived transition history), no
  other change.
- `pkg/proxy/CODEMANIFEST` — `Proxy.New`/`Proxy(...)` gains a third `usageLogPath` argument;
  `Proxy.ServeHTTP` gains a uniform usage-capture algorithm (tee-write + `ParseUsage` +
  `AppendUsageRecord`) applied regardless of which `Transform` (or none) handled the request; three new
  entities (`UsageRecord`, `ParseUsage`, `AppendUsageRecord`).
- `pkg/config/CODEMANIFEST` — `Config` gains a `Pricing PricingConfig` field; two new entities
  (`PricingConfig`, `ModelPricing`).
- `pkg/accounting/CODEMANIFEST` — **new cell**, 10 entities: `Accountant`, `DeriveCost`, `StageWindow`,
  `LoadStageWindows`, `LoadUsageRecords`, `AttributeStage`, `ReadResultUsage`, `ResultUsage`,
  `Aggregate`, `UsageAggregate`.
- `pkg/server/CODEMANIFEST` — `Config` gains an `Accountant Accountant` field; `Server` gains the
  `/api/usage` route requirement; one new entity (`UsageHandler`).
- `pkg/web/dashboard/CODEMANIFEST` — one new Requirements bullet on `DashboardAssets()` describing the
  consumption panel (slide-out, metric switch, stage filter, hand-rolled SVG chart).
- `cmd/afm/CODEMANIFEST` — one new `Imports` entry (`Accountant`/`query_usage` from `pkg/accounting`);
  one new algorithm step (`2b.`) on `newRunCmd()` describing `Accountant` construction and threading
  into `server.Config`.

### New Entities

- `pkg/proxy.UsageRecord` — per-request usage/byte-count record (`usage.go`).
- `pkg/proxy.ParseUsage` — parses a proxied response body (SSE or JSON) into a `UsageRecord` (`usage.go`).
- `pkg/proxy.AppendUsageRecord` — appends one `UsageRecord` as a JSON line to a run's usage log (`usage.go`).
- `pkg/config.PricingConfig` / `pkg/config.ModelPricing` — optional per-model $/Mtok pricing table (`config.go`).
- `pkg/config.AccountingConfig` — configurable time-bucket width for consumption aggregation, added
  during this design-review stage (`config.go`, see Applied Fixes).
- `pkg/accounting.Accountant` — per-run consumption query facade (`accountant.go`).
- `pkg/accounting.DeriveCost` — tokens → USD conversion (`cost.go`).
- `pkg/accounting.StageWindow` / `LoadStageWindows` — stage execution time windows from `Store.History` (`attribution.go`).
- `pkg/accounting.LoadUsageRecords` — reads `usage.jsonl` (`reader.go`).
- `pkg/accounting.AttributeStage` — maps a `UsageRecord` timestamp to a stage window (`attribution.go`).
- `pkg/accounting.ReadResultUsage` / `ResultUsage` — reads the authoritative `result` event from `<phase>.jsonl` (`reader.go`).
- `pkg/accounting.Aggregate` / `UsageAggregate` — builds stage × time-bucket × metric aggregates (`aggregate.go`).
- `pkg/server.UsageHandler` — `/api/usage` HTTP handler factory (`usage_handler.go`).

### Changed Entities

- `pkg/state.Store` — new method `History() -> transitions:[]Transition, err:error`.
- `pkg/proxy.Proxy` — `New`/constructor signature gains `usageLogPath string`; `ServeHTTP` annotation
  gains the tee-capture Algorithm.
- `pkg/config.Config` — gains `Pricing PricingConfig` property; gains `Accounting AccountingConfig`
  property (added during this design-review stage, see Applied Fixes).
- `pkg/accounting.Accountant`/`NewAccountant` — gains a `bucketMinutes int` constructor parameter
  (added during this design-review stage, see Applied Fixes).
- `pkg/accounting.Aggregate` — gains a `bucketMinutes int` parameter and an explicit `TimeBucket`
  derivation step (added during this design-review stage, see Applied Fixes).
- `pkg/server.Config` — gains `Accountant Accountant` property.
- `pkg/server.Server` — gains the `/api/usage` Requirements bullet.
- `pkg/web/dashboard.DashboardAssets` — gains one Requirements bullet.
- `cmd/afm.newRunCmd` — gains Imports entry + Algorithm step `2b.`.

### Deleted Entities

None.

### Usages and Annotations Changes

- `pkg/proxy/.usages/usage_log.md` (**new**) — how `pkg/accounting` consumes `usage.jsonl` via `LoadUsageRecords`.
- `pkg/proxy/.usages/proxy_facade.md` (**modified**) — `proxy.New` call site updated to the 3-arg form.
- `pkg/config/.usages/config_facade.md` (**modified**) — added `cfg.Pricing.GetModelPricing(...)` snippet.
- `pkg/accounting/.usages/query_usage.md` (**new**) — how `pkg/server`/`cmd/afm` construct and query an `Accountant`.
- `pkg/server/.usages/server_facade.md` (**modified**) — added `Accountant: accounting.NewAccountant(...)` to the `server.New` example.

## Applied Fixes

### Fixed CODEMANIFEST Defects

- `pkg/accounting/CODEMANIFEST`, `Aggregate` annotation: the `Algorithm` filtered/grouped records but
  never specified how the per-record numeric value is derived for any of the three metrics, and the
  declared `DeriveCost` routine was never referenced anywhere in the document (dead entity — an
  Annotations↔Entity consistency defect: a declared contract entity with no annotation binding it into
  any other entity's behavior). **Before**: step 4 only resolved `ModelPricing` and step 5 said
  "sum the selected metric's values" without saying what those values are. **After**: step 4 now
  explicitly derives the per-record value per metric — `tokens` = sum of the four token fields, `cost` =
  `DeriveCost(InputTokens, OutputTokens, CacheCreationTokens+CacheReadTokens, ModelPricing)`, `kb` =
  `(RequestBytes+ResponseBytes)/1024` — and step 5 sums those derived values into `UsageAggregate.Value`.
  Re-validated: `goga lint` → `cells: 17 errors: 0` (one intermediate lint failure during the fix, caused
  by an invalid dotted backtick reference `` `UsageAggregate.Value` ``, corrected to a plain-text field
  mention plus a bare `` `UsageAggregate` `` type reference).

- `pkg/accounting/CODEMANIFEST`, `Aggregate`/`UsageAggregate` — found during this design-review stage
  (Remark 1): the "time bucket" that `Aggregate` groups by and that `UsageAggregate.TimeBucket` stores
  was never defined anywhere — neither the bucket width nor the function mapping a `Timestamp` to a
  bucket boundary. This is an insufficient-requirements CODEMANIFEST defect: it blocked implementation,
  and this design's own test trace for `test_aggregate_cost_metric_uses_derive_cost` could not state a
  concrete expected `TimeBucket` value (used the placeholder `<bucket containing 10:01>`). **Fix**
  (user-selected, see stage dialog): 5-minute UTC wall-clock buckets, with the width made configurable.
  Added `pkg/config.AccountingConfig{BucketMinutes int}` (nil/0 → default 5, `GetModelPricing`-style
  getter `GetBucketMinutes`), threaded it through `Accountant`/`NewAccountant` (new `bucketMinutes int`
  parameter) and `Aggregate` (new `bucketMinutes int` parameter, new algorithm step 5: floor `Timestamp`
  to the nearest `bucketMinutes`-minute UTC boundary; for fallback `ResultUsage` rows, which carry no
  `Timestamp`, use the attributed `StageWindow.Start` instead). Updated `cmd/afm/CODEMANIFEST` step
  `2b.` and both `pkg/accounting/.usages/query_usage.md` and `pkg/server/.usages/server_facade.md` to
  pass `cfg.Accounting.GetBucketMinutes()` into `NewAccountant`. Re-validated: `goga lint` →
  `cells: 17 errors: 0` (two intermediate failures from invalid dotted/out-of-scope backtick references
  — `` `config.AccountingConfig.GetBucketMinutes` `` and a `` `bucketMinutes` `` reference inside
  `Query`'s own annotation where it is not a signature parameter — both corrected to plain text).

## Entity Interaction and Data Flow

### Interaction Diagram

```
                         (real-time, per-request, only while an agent's HTTP call is in flight)
Agent process ──HTTP──▶ Proxy.ServeHTTP ──tee-write──▶ client (unblocked)
                              │
                              ├─▶ Transform.ServeHTTP or passthroughTo (existing dispatch, unchanged)
                              │
                              └─▶ ParseUsage(contentType, buffered body) ──▶ UsageRecord
                                        │
                                        └─▶ AppendUsageRecord(usageLogPath, record) ──▶ usage.jsonl

                         (on-demand, triggered by a dashboard GET)
Dashboard ──GET /api/usage?metric=&stage=──▶ UsageHandler ──▶ Accountant.Query(metric, stage)
                                                                     │
                              ┌──────────────────────────────────────┼───────────────────────────────┐
                              ▼                                      ▼                                ▼
                    LoadUsageRecords(usage.jsonl)          LoadStageWindows(store)          per-stageID ReadResultUsage
                    (missing file → empty, not error)      (store.History() → []Transition)  (<phase>.jsonl per stage)
                              │                                      │                                │
                              └──────────────┬───────────────────────┴────────────────┬───────────────┘
                                             ▼                                        ▼
                                     AttributeStage(record, windows)          Aggregate(records, windows,
                                     (per record → stageID or ok=false)        resultUsages, pricing,
                                                                                metric, stage)
                                                                                       │
                                                                                       ▼
                                                                            []UsageAggregate ──▶ JSON ──▶ dashboard

                         (startup wiring, once per run)
cmd/afm newRunCmd ──▶ proxy.New(upstream, transforms, runDir/usage.jsonl)
                  ──▶ accounting.NewAccountant(runDir, store, cfg.Pricing, cfg.Accounting.GetBucketMinutes())
                  ──▶ server.Config.Accountant
```

### Data Flows

**1. Real-time capture (proxy active).** Every proxied HTTP response — whether handled by a `Transform`
(e.g. `ZAITransform`) or passed straight through — is captured uniformly: `Proxy.ServeHTTP` wraps the
`http.ResponseWriter` it received in a tee-writer before dispatching, so bytes still stream to the agent
immediately while a copy accumulates in memory. After the handler returns, `ParseUsage` turns the
buffered body + `Content-Type` into a `UsageRecord`, and `AppendUsageRecord` appends it to
`<runDir>/usage.jsonl`. Errors at either step are logged and swallowed — the client-facing response was
already sent and is never affected.

**2. On-demand query (dashboard).** The dashboard's consumption panel calls `GET
/api/usage?metric=<m>&stage=<s>`. `UsageHandler` validates `metric` and calls
`Accountant.Query(metric, stage)`, which combines three independent sources — the proxy's `usage.jsonl`
(via `LoadUsageRecords`), the stage-transition history (via `LoadStageWindows`, itself built from
`Store.History`), and per-stage authoritative jsonl `result` events (via `ReadResultUsage`, one call per
`stageID` in `store.Snapshot().StageOrder`, over each of `planning.jsonl`/`implementation.jsonl`/`review.jsonl`)
— into `[]UsageAggregate` via `Aggregate`.

**3. jsonl fallback (proxy inactive this run).** When `usage.jsonl` is absent or empty (no proxy started
this run — no `ANTHROPIC_BASE_URL` resolved), `Aggregate` falls back to `resultUsages` for `metric=tokens`
only: `ResultUsage` carries no `Model` field, so `pricing.GetModelPricing` cannot be resolved for it, and
such a stage is excluded from `metric=cost` aggregates entirely (never using `ResultUsage.TotalCostUsd` as
a displayed value — cost is single-sourced via `PricingConfig`/`DeriveCost` per the task's explicit
decision). There is no KB fallback (KB is proxy-only; no other source has byte counts).

**4. Startup wiring (`cmd/afm`).** `newRunCmd` resolves the proxy upstream, builds `Proxy` with the new
`usageLogPath` argument (`filepath.Join(runDir, "usage.jsonl")`), starts it, then constructs `Accountant`
via `accounting.NewAccountant(runDir, store, cfg.Pricing, cfg.Accounting.GetBucketMinutes())` and
threads it into `server.Config.Accountant`
— this is algorithm step `2b.`, positioned after the existing `CreateShim` handling (step 2) and before
the deferred `Shutdown` (step 3).

### Entity Dependencies

Initialization/design order (leaves → root, matches `docs/arch/master.md`'s Implementation Order and the
live `goga schema` graph):

1. `pkg/state` (`Store.History`) — no dependencies.
2. `pkg/proxy` (`UsageRecord`/`ParseUsage`/`AppendUsageRecord`/`Proxy`) — no dependencies, independent of (1).
3. `pkg/config` (`PricingConfig`/`ModelPricing`) — no dependencies, independent of (1)/(2).
4. `pkg/accounting` — depends on (1) `Store`/`Transition`, (2) `UsageRecord`, (3) `PricingConfig`/`ModelPricing`.
5. `pkg/server` — depends on (4) `Accountant`.
6. `pkg/web/dashboard` — depends on (5) only via the HTTP `/api/usage` contract, zero Go imports.
7. `cmd/afm` — depends on (2) `Proxy`, (4) `Accountant`, (5) `Server`; constructs (4) and (5) at runtime,
   in that order, inside `newRunCmd`.

Runtime construction order inside a single `afm run`: `state.Open` → `proxy.New`/`Start` →
`accounting.NewAccountant` → `server.New` (holding the `Accountant`). `Accountant.Query` is stateless
between calls — it re-reads `usage.jsonl` and re-derives `store.History()`/windows on every call (see
Cross-cutting Concerns → Caching).

## Code Stack Trace

### Trace: `Proxy.ServeHTTP` (usage-capture path)

#### Chain
1. **Input**: an inbound HTTP request from an agent process (e.g. `claude`), reaching the already-started
   `Proxy` on `127.0.0.1:<port>`.
2. **Wrap**: `ServeHTTP` wraps the real `http.ResponseWriter` in an unexported tee-writer — every `Write`
   call forwards immediately to the real writer (no buffering added to the client-visible stream) and
   also appends to an internal `[]byte` buffer. → checkpoint: streaming latency unaffected (Requirement
   in `Proxy` annotation); type is still `http.ResponseWriter`, satisfying `Transform.ServeHTTP`'s
   parameter type — **passed**.
3. **Dispatch**: existing logic unchanged — first `Transform` whose `Match(upstream)` is true handles the
   request via its `ServeHTTP(wrappedWriter, r, upstream)`; otherwise `passthroughTo` forwards
   unmodified. → checkpoint: both paths receive the *same* wrapped writer type, so capture is uniform
   regardless of dispatch branch — **passed** (this is the fix for the task's "passthrough тоже
   считается" acceptance criterion).
4. **Post-dispatch**: once the handler returns, `ServeHTTP` calls `ParseUsage(contentType, buf)` where
   `contentType` is the real response's `Content-Type` header and `buf` is the tee-writer's accumulated
   bytes. → checkpoint: `ParseUsage`'s declared input types (`string, string`) match what's available at
   this call site — **passed**.
5. **Record assembly**: on success, `ServeHTTP` fills in `RequestBytes`/`ResponseBytes` (sizes it can
   measure directly) onto the `UsageRecord` returned by `ParseUsage` (which only fills `Model`+token
   fields, per `ParseUsage`'s own Requirements) and calls `AppendUsageRecord(usageLogPath, record)`.
   → checkpoint: `UsageRecord` is a single shared type between `ParseUsage`'s output and
   `AppendUsageRecord`'s input — **passed**.
6. **Output**: none returned to the caller of `ServeHTTP` (side effect only — the client already received
   its response in step 2/3). Errors from `ParseUsage`/`AppendUsageRecord` are logged and discarded, per
   the `Proxy` annotation's explicit Requirement.

#### Checkpoint Summary
- Tee-write does not block client streaming: **passed** (Requirement text explicit on this).
- Uniform capture across Transform/passthrough: **passed**.
- Type flow `ParseUsage` → `AppendUsageRecord` via `UsageRecord`: **passed**.
- Error isolation (capture failure never touches the already-sent response): **passed**.

### Trace: `ParseUsage`

#### Chain
1. **Input**: `contentType string` (response `Content-Type` header), `body string` (accumulated response bytes).
2. **Branch on content type**: if `contentType` contains `text/event-stream`, parse `body` as an SSE
   stream — accumulate `usage` from `message_start`/`message_delta` events, generalizing the existing
   `parseSSE` logic in `zai.go`. → checkpoint: this reuses an already-verified parsing algorithm
   (`ZAITransform`'s `Algorithm` step 4) — **passed**, no new parsing logic invented.
3. **Else**: parse `body` as a single JSON object, extract `model` and `usage` fields directly — this
   covers both plain non-streaming Anthropic responses and `ZAITransform`'s already-reassembled single
   JSON message (which is itself the direct-JSON shape). → checkpoint: `ZAITransform`'s output shape
   (single Anthropic `message` JSON) is exactly the shape this branch expects — **passed**, no adapter
   needed.
4. **Validation**: a non-200 status or a response with no resolvable `usage` field returns `err` — the
   function must not synthesize a zero-valued `UsageRecord` in that case (Constraint).
5. **Output**: `record UsageRecord` (`Model` + four token fields populated; `RequestBytes`/`ResponseBytes`
   left to the caller) and `err error`.

#### Checkpoint Summary
- SSE branch reuses `zai.go`'s `parseSSE` pattern: **passed**.
- JSON branch also matches `ZAITransform`'s reassembled-message shape: **passed**.
- Non-200/missing-usage → error, no partial record: **passed** (explicit Constraint).

### Trace: `AppendUsageRecord`

#### Chain
1. **Input**: `path string` (`usage.jsonl`'s full path), `record UsageRecord`.
2. **Open**: `os.OpenFile(path, O_CREATE|O_APPEND|O_WRONLY, 0644)` — same pattern as `pkg/state.SaveFeedback`
   (verified in `pkg/state/state.go:106`, the precedent this Requirement cites).
3. **Serialize + write**: marshal `record` to JSON, write one line (`json + "\n"`), single `Write` call.
4. **Close**: `defer f.Close()`.
5. **Output**: `err error` — non-nil if open/write fails.

#### Checkpoint Summary
- Open-write-close per call, no held handle across calls: **passed** (matches `SaveFeedback` precedent
  exactly — single `os.OpenFile` + single write + `defer Close`, relying on OS-level atomicity of a
  single `O_APPEND` write for concurrency safety, same as the existing convention).
- Single-line JSON write matches `usage_log.md`'s documented format: **passed**.

### Trace: `config.PricingConfig.GetModelPricing`

#### Chain
1. **Input**: `model string` (exact model name, e.g. `"claude-sonnet-5"`).
2. **Lookup**: exact-match lookup into `PricingConfig.Models map[string]ModelPricing` — no fuzzy/prefix
   matching (Constraint).
3. **Output**: `pricing ModelPricing, ok bool` — `ok=false` when `Models` is nil/empty or the key is
   absent.

#### Checkpoint Summary
- Nil/empty map → `ok=false`, never a panic on nil-map read (Go map reads on nil maps are safe,
  language-guaranteed): **passed**.
- Exact-match only, per Constraint: **passed**.
- Consumed correctly downstream by `Aggregate` step 4 (`pricing.GetModelPricing(record.Model)`) and by
  `config_facade.md`'s documented snippet: **passed** — same two-value signature at both call sites.

### Trace: `state.Store.History`

#### Chain
1. **Input**: none (method on an already-`Open`ed `Store`).
2. **Source**: the in-memory transition log accumulated during `Open`'s replay of `events.jsonl` (no file
   re-read — Read-only per annotation).
3. **Output**: `transitions []Transition, err error`, ordered by ascending `Seq` (which the annotation's
   Requirement guarantees is append-order, hence non-decreasing `Time`).

#### Checkpoint Summary
- Read-only, no double file I/O: **passed** — consistent with `Store`'s existing `Snapshot()` method
  which is also documented as reading in-memory state only.
- Ordering guarantee (`Seq` ascending ⇒ `Time` non-decreasing) is exactly what `LoadStageWindows` step 1
  requires ("отсортированную по возрастанию поля Time") — checkpoint: **passed**, the two annotations
  agree on ordering semantics without `LoadStageWindows` needing to re-sort.

### Trace: `accounting.NewAccountant` (construction, `cmd/afm` step 2b)

#### Chain
1. **Input**: `runDir string`, `store Store` (the same `*state.Store` instance `cmd/afm` already opened
   and later passes to `server.Config.Store`), `pricing PricingConfig` (`cfg.Pricing`, zero-value valid).
2. **Construct**: stores all three as unexported internal state (no exported properties — same shape as
   `Proxy`/`Executor`, per annotation).
3. **Output**: `accountant Accountant`.
4. **Downstream**: passed into `server.Config.Accountant` (per `server_facade.md`'s updated example) —
   this is the same call site pattern `cmd/afm`'s CODEMANIFEST step `2b.` describes.

#### Checkpoint Summary
- `store` is the *same instance* threaded to both `Accountant` and `server.Config.Store` — no duplicate
  `Store.Open` call, no divergent in-memory state between the two consumers: **passed** (verified via
  `cmd/afm` CODEMANIFEST's existing single `Open` call in its Algorithm, unchanged by this plan).
- `pricing` zero-value (`Models` nil) is explicitly valid per `PricingConfig`'s own annotation and
  `query_usage.md`'s documented example: **passed**.

### Trace: `Accountant.Query`

#### Chain
1. **Input**: `metric string` (`tokens|cost|kb`), `stage string` (`""` = all stages).
2. **Step**: `LoadUsageRecords(filepath.Join(a.runDir, "usage.jsonl"))` → `[]UsageRecord`; missing file →
   empty slice, `nil` error (not a failure — proxy may not have been active this run).
3. **Step**: `LoadStageWindows(a.store)` → `[]StageWindow`, internally calling `a.store.History()`.
4. **Step**: for each `stageID` in `a.store.Snapshot().StageOrder`, `ReadResultUsage` against each of that
   stage's `planning.jsonl`/`implementation.jsonl`/`review.jsonl` (whichever exist) → `[]ResultUsage`.
   → checkpoint: `RunState.StageOrder` (from `Store.Snapshot()`) is exactly the `[]string` type
   `ReadResultUsage`'s caller needs to enumerate stage directories — **passed** (verified against
   `pkg/state/CODEMANIFEST`'s `RunState.StageOrder -> []string` property).
5. **Step**: `Aggregate(records, windows, resultUsages, a.pricing, metric, stage, a.bucketMinutes)` →
   `[]UsageAggregate`.
6. **Output**: `aggregates []UsageAggregate, err error`. `err` is non-nil only if `LoadUsageRecords` or
   `LoadStageWindows` hard-fails (e.g. `usage.jsonl` exists but is unreadable, or `store.History()`
   errors) — `metric=cost` with empty `pricing.Models` is *not* an error path (Requirement).

#### Checkpoint Summary
- Every sub-call's output type feeds the next/`Aggregate`'s corresponding parameter type exactly
  (`[]UsageRecord`, `[]StageWindow`, `[]ResultUsage`, `PricingConfig`, `string`, `string`): **passed**.
- `Query` never mutates `Store`/`events.jsonl` (Constraint) — every sub-call used (`History`, `Snapshot`)
  is documented read-only: **passed**.

### Trace: `DeriveCost`

#### Chain
1. **Input**: `inputTokens int, outputTokens int, cacheTokens int, pricing ModelPricing` — caller (only
   `Aggregate`, per the Applied Fix above) is responsible for having already resolved `pricing` via
   `GetModelPricing`; `DeriveCost` itself does not check for a missing price.
2. **Step**: `inputTokens * pricing.InputPerMtok / 1_000_000`.
3. **Step**: `outputTokens * pricing.OutputPerMtok / 1_000_000`.
4. **Step**: `cacheTokens * pricing.CachePerMtok / 1_000_000` (note: `cacheTokens` is the caller-summed
   `CacheCreationTokens+CacheReadTokens` — `ModelPricing` has one undifferentiated cache rate).
5. **Output**: `costUsd float64`, the sum of the three.

#### Checkpoint Summary
- Caller contract (pricing must already be resolved) matches `Aggregate`'s step 4, which only calls
  `DeriveCost` after a successful `GetModelPricing`: **passed**, post the Applied Fix.
- Rate unit (`$/Mtok`) consistently divided by `1_000_000` in all three terms: **passed**.

### Trace: `LoadStageWindows`

#### Chain
1. **Input**: `store Store`.
2. **Step**: `store.History()` → `[]Transition` ordered by ascending `Time` (per `Store.History`'s
   Requirement).
3. **Step**: per `stageID`, pair the first transition into `running` with the next later terminal
   transition (`done` or `failed`) of the same stage → `StageWindow{Start, End}`.
4. **Edge**: a stage with a `running` transition but no subsequent terminal transition (still executing,
   or the run was interrupted) → `End=""`.
5. **Output**: `windows []StageWindow, err error` (`err` only from `store.History()` itself failing).

#### Checkpoint Summary
- Depends on `Store.History`'s ordering guarantee, not a private re-sort: **passed**, checkpoint already
  covered in the `Store.History` trace above.
- `Transition.StageID`/`From`/`To`/`Time` fields (all declared on `pkg/state.Transition`) are exactly
  what step 3 consumes: **passed**.

### Trace: `LoadUsageRecords`

#### Chain
1. **Input**: `path string` (`usage.jsonl`'s path).
2. **Step**: read the file line by line, unmarshal each line as `UsageRecord` (JSON shape documented in
   `pkg/proxy/.usages/usage_log.md`, which `pkg/accounting` imports/reads per Phase 1's contract).
3. **Edge**: missing file → `records: [], err: nil` (Constraint — not a failure, proxy may be inactive).
4. **Output**: `records []UsageRecord, err error`.

#### Checkpoint Summary
- Consumer (`Accountant.Query`) treats a missing file identically to an empty result: **passed**, no
  special-casing needed at the call site beyond what `LoadUsageRecords` already guarantees.
- Field-for-field match against `UsageRecord`'s declared properties (`Timestamp`/`Model`/`InputTokens`/
  `OutputTokens`/`CacheCreationTokens`/`CacheReadTokens`/`RequestBytes`/`ResponseBytes`): **passed**.

### Trace: `AttributeStage`

#### Chain
1. **Input**: `record UsageRecord`, `windows []StageWindow`.
2. **Step**: find every window whose `[Start, End)` interval contains `record.Timestamp` (`End=""` treated
   as open-ended, i.e. still running).
3. **Branch**: exactly one match → `stageID` = that window's `StageID`, `ok=true`. Zero or more-than-one
   matches (overlapping windows — concurrent top-level stages) → `ok=false` (Constraint — the ambiguous
   case is intentionally unresolved here, deferred per `docs/tasks/master.md`'s Risks).
4. **Output**: `stageID string, ok bool`.

#### Checkpoint Summary
- `record.Timestamp` and `window.Start`/`window.End` are both `string` (RFC3339) — direct lexicographic
  comparison of RFC3339 timestamps is order-preserving (fixed-width, zero-padded, UTC), so this is
  implementable without a parse step if desired, though parsing to `time.Time` for the comparison is
  equally valid and clearer: **passed**, no type mismatch.
- Ambiguous-overlap Constraint is consistent with the task doc's explicitly-accepted Risk ("параллельные
  top-level стейджи" — unresolved by design, not a defect): **passed**, not re-litigated here.

### Trace: `ReadResultUsage` / `ResultUsage`

#### Chain
1. **Input**: `jsonlPath string` (one stage-phase's `<phase>.jsonl`, e.g. `implementation.jsonl`).
2. **Step**: read line by line, find the *last* line whose `type` field is `"result"` (there is at most
   one per phase in practice, but "last" is the defensive rule).
3. **Step**: parse that line's `total_cost_usd`, `usage.input_tokens`, `usage.output_tokens`,
   `usage.cache_creation_input_tokens`, `usage.cache_read_input_tokens`, `session_id`, `duration_ms` into
   `ResultUsage`'s matching fields (`TotalCostUsd`, `InputTokens`, `OutputTokens`, `CacheCreationTokens`,
   `CacheReadTokens`, `SessionID`, `DurationMs`) plus the caller-supplied `StageID`.
   → verified against a real observed `result` event from this exact run's own failed attempt (see this
   stage's prior execution log): `{"type":"result","total_cost_usd":0.006087,"usage":{"input_tokens":0,
   "output_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0},"session_id":"...",
   "duration_ms":210681,...}` — field names match `ResultUsage`'s properties exactly.
4. **Edge**: file absent, unreadable, or no `"result"`-typed line → `ok=false` (Constraint, same shape as
   `pkg/executor.WrittenFiles`'s "missing log → empty" precedent).
5. **Output**: `usage ResultUsage, ok bool`.

#### Checkpoint Summary
- Field names verified against a real, observed `result` event (not assumed): **passed**.
- `ok=false` convention matches the precedented `WrittenFiles` pattern cited in the annotation: **passed**.
- `ResultUsage` deliberately has no `Model` field — `Aggregate` step 4 (`metric=cost` via fallback)
  correctly cannot resolve `GetModelPricing` for it and excludes such stages from cost aggregates: this
  is a documented, intentional gap, not a defect — **passed**.

### Trace: `Aggregate` / `UsageAggregate` (post Applied Fix)

#### Chain
1. **Input**: `records []UsageRecord`, `windows []StageWindow`, `resultUsages []ResultUsage`,
   `pricing PricingConfig`, `metric string`, `stage string`.
2. **Step**: attribute each `record` via `AttributeStage`; drop unattributed (`ok=false`) records.
3. **Step**: filter by `stage` (empty = all).
4. **Step**: for stages with zero proxy records this run, substitute the matching `resultUsages` as a
   token-only fallback (per the Data Flows §3 rule above — `metric=cost` excludes these).
5. **Step** (post-fix): compute each surviving record's per-metric value —
   `tokens` = `InputTokens+OutputTokens+CacheCreationTokens+CacheReadTokens`;
   `cost` = `DeriveCost(...)` after a successful `pricing.GetModelPricing(record.Model)` (skip the record
   on `ok=false`); `kb` = `(RequestBytes+ResponseBytes)/1024`.
6. **Step**: group by `(stageID, timeBucket)`, sum the step-5 values into `UsageAggregate.Value`.
7. **Output**: `aggregates []UsageAggregate` — one row per `(stageID, timeBucket)` pair for the requested
   `metric`.

#### Checkpoint Summary
- `DeriveCost` is now reachable from `Aggregate`'s own algorithm text (Applied Fix above resolved the
  dead-entity defect): **passed**.
- Every field `Aggregate` reads (`record.Model`, `record.InputTokens`, …, `window.Start/End`,
  `resultUsage.InputTokens`, …) resolves against a declared property on the corresponding imported/local
  type: **passed**.
- `TotalCostUsd` is never summed into `UsageAggregate.Value` (Constraint, unchanged by the fix): **passed**.

### Trace: `server.UsageHandler`

#### Chain
1. **Input**: an HTTP `GET /api/usage?metric=<m>&stage=<s>` request; the handler closure holds an
   `Accountant` captured at construction time (`UsageHandler(cfg.Accountant)`, registered once in
   `Server.New`'s route setup, per the `Server` annotation's Requirements bullet).
2. **Step**: parse `metric` (default `"tokens"`) and `stage` (default `""`) from the query string.
3. **Step**: validate `metric ∈ {tokens, cost, kb}`; otherwise respond `400`.
4. **Step**: call `accountant.Query(metric, stage)`.
5. **Branch**: `err != nil` → `500`; else → `200` with a JSON array of `UsageAggregate`.

#### Checkpoint Summary
- `Accountant` passed into `UsageHandler` is the exact same instance held by `Server.Config.Accountant`
  (no separate construction) — verified against `server_facade.md`'s single `accounting.NewAccountant`
  call feeding `server.Config`: **passed**.
- Empty-pricing → empty-array (not an error) reaches the HTTP layer as `200 []`, matching the `Server`
  annotation's explicit Requirement ("не ошибка... хендлер отвечает 200 с пустым списком"): **passed**.

## Algorithm Design

### `Proxy` (changed)

**Responsibility**: reverse-proxy dispatch (unchanged) plus uniform response-usage capture (new).

**Algorithm:**
```
1. Wrap w in a tee-writer — every Write forwards to the real w and buffers a copy
   → wrapped writer, same http.ResponseWriter interface
2. Dispatch: first matching Transform.ServeHTTP(wrapped, r, upstream), else passthroughTo(wrapped, r)
   → response bytes already delivered to the client via the tee-writer's forwarding half
3. ParseUsage(responseContentType, teeBuffer) → record, err
   IF err != nil:
     - log and return (no further action — response already sent)
4. Fill record.RequestBytes / record.ResponseBytes from measured sizes
5. AppendUsageRecord(usageLogPath, record) → err
   IF err != nil:
     - log and return
```

**Errors:**
- `ParseUsage` error (non-200, no `usage` field) → logged, capture skipped for this request, response unaffected.
- `AppendUsageRecord` error (disk full, permission) → logged, capture skipped, response unaffected.

**Edge Cases:**
- `usageLogPath == ""` → capture is a no-op by convention (documented in `Proxy.New`'s annotation — used
  in tests); `ServeHTTP` should short-circuit before calling `AppendUsageRecord` in that case rather than
  attempt-and-fail on every request.
- Concurrent requests (parallel subagents) each get their own tee-writer instance (per-request, not
  shared) — no cross-request buffer aliasing.

### `pkg/proxy.ParseUsage`

**Responsibility**: normalize any proxied response body (SSE or JSON, any upstream) into one `UsageRecord`.

**Algorithm:**
```
1. IF contentType contains "text/event-stream":
     parse body as SSE (reuse zai.go's parseSSE pattern) → accumulate model + usage from
     message_start / message_delta
   ELSE:
     parse body as one JSON object → read .model and .usage directly
2. IF response was non-200 OR no usage field resolved:
     return err (no zero-valued record)
3. Return UsageRecord{Model, InputTokens, OutputTokens, CacheCreationTokens, CacheReadTokens}
   (RequestBytes/ResponseBytes left zero — caller fills them)
```

**Errors:**
- Malformed JSON / unparseable SSE → `err`, no record.
- Non-200 upstream status → `err`, no record (Constraint).

**Edge Cases:**
- `stream:true` request where the SSE stream ends without a `message_start` (empty/aborted stream) → no
  usage resolved → `err` (same as the missing-usage case).

### `pkg/proxy.AppendUsageRecord`

**Responsibility**: durably append one `UsageRecord` line to a run's `usage.jsonl`.

**Algorithm:**
```
1. os.OpenFile(path, O_CREATE|O_APPEND|O_WRONLY, 0644) → f, err
   IF err != nil: return err
2. defer f.Close()
3. json.Marshal(record) → line, err
4. f.Write(line + "\n")
```

**Errors:**
- Open failure (permissions, missing parent dir) → `err`, propagated to `Proxy.ServeHTTP` which logs and swallows it.

**Edge Cases:**
- High-concurrency parallel subagents each calling `AppendUsageRecord` on the same file: relies on the
  same OS-level single-`write()`-call atomicity as the precedented `pkg/state.SaveFeedback` — no explicit
  locking is introduced, consistent with the existing project convention for this pattern.

### `config.PricingConfig` / `ModelPricing` (new, data-holding + one method)

**Responsibility**: optional per-model pricing table; all-or-nothing visibility of the cost metric.

**Algorithm** (`GetModelPricing`):
```
1. Look up model in Models map (exact key match)
2. IF found: return pricing, true
   ELSE: return zero-value ModelPricing, false
```

**Errors:** none (returns `ok bool`, never an error).

**Edge Cases:**
- `Models` is `nil` (section absent from YAML) → every lookup returns `ok=false`, consistent with
  "money metric fully hidden" (annotation Requirement — no partial display).

### `state.Store.History` (new method on existing entity)

**Responsibility**: expose the already-replayed transition log for read-only consumers outside `pkg/orchestrator`.

**Algorithm:**
```
1. Return the in-memory []Transition slice accumulated during Open's events.jsonl replay
   (no file re-read, no re-sort — already Seq-ascending from append-only writes)
```

**Errors:** `err` reserved for a future failure mode (e.g. detecting log corruption after `Open`); no
current code path in `Open`'s documented Algorithm produces a post-open error here, so `err` is expected
`nil` in practice today.

**Edge Cases:**
- A run with zero transitions (opened but no stage ever left `pending`) → empty slice, not an error.

### `accounting.Accountant`

**Responsibility**: single per-run facade combining three usage sources into queryable aggregates.

**Algorithm** (`Query`):
```
1. LoadUsageRecords(runDir/usage.jsonl) → records (empty if file absent)
2. LoadStageWindows(store) → windows
3. FOR each stageID IN store.Snapshot().StageOrder:
     ReadResultUsage on each existing phase.jsonl for stageID → resultUsages (append if ok)
4. Aggregate(records, windows, resultUsages, pricing, metric, stage, bucketMinutes) → aggregates
5. Return aggregates, nil
   (LoadUsageRecords/LoadStageWindows failures propagate as err; nothing else can fail)
```

**Errors:**
- `LoadUsageRecords`/`LoadStageWindows` hard failure (not "file missing") → propagated as `Query`'s `err`.

**Edge Cases:**
- `metric=cost` with empty `pricing.Models` → `Aggregate` naturally produces `[]` (every
  `GetModelPricing` call returns `ok=false`) — `Query` returns `(nil-or-empty, nil)`, not an error
  (Requirement).
- No stages yet started (`StageOrder` populated but all `pending`) → `windows` and `resultUsages` both
  empty; `records` may still be non-empty if the proxy captured requests before any stage transition was
  recorded (unlikely in practice, since capture only happens around agent HTTP calls which occur during a
  running stage) — such unattributable records are dropped by `AttributeStage` returning `ok=false`.

### `accounting.DeriveCost`

**Responsibility**: pure tokens→USD conversion for one already-resolved `ModelPricing`.

**Algorithm:**
```
1. cost = inputTokens * pricing.InputPerMtok / 1_000_000
         + outputTokens * pricing.OutputPerMtok / 1_000_000
         + cacheTokens * pricing.CachePerMtok / 1_000_000
2. Return cost
```

**Errors:** none — pure function.

**Edge Cases:**
- All-zero token counts (e.g. an `is_error` result with zero usage, as observed in this stage's own
  failed prior attempt) → `costUsd = 0`, not an error.
- Zero pricing rates (all fields `0`) → `costUsd = 0` regardless of token counts — a legitimately
  configured free-tier/self-hosted model.

### `accounting.LoadStageWindows`

**Responsibility**: derive stage execution windows from the transition log.

**Algorithm:**
```
1. transitions = store.History()  (ascending Time)
2. FOR each transition INTO status "running":
     find the next later transition of the same stageID INTO "done" or "failed"
     → StageWindow{StageID, Start: running.Time, End: terminal.Time or ""}
```

**Errors:** propagates `store.History()`'s error, if any.

**Edge Cases:**
- Stage retried (transitions to `running` more than once) → produces one window per `running`→terminal
  pairing; a stage retried after `failed` naturally yields two windows, each independently attributable.
- Stage still `running` at query time → `End=""`, treated as open-ended by `AttributeStage`.

### `accounting.LoadUsageRecords`

**Responsibility**: read the per-run usage log written by the proxy.

**Algorithm:**
```
1. IF file at path does not exist: return [], nil
2. Read line by line, json.Unmarshal each line into UsageRecord
3. Return records, nil (or err on a genuine read/parse failure of an existing file)
```

**Errors:** propagates real I/O/parse errors on an *existing* file; a missing file is never an error (Constraint).

**Edge Cases:**
- Empty file (proxy started but zero requests captured, e.g. an idle stage) → `[]`, not an error.
- Truncated last line (process killed mid-write) — not explicitly covered by the CODEMANIFEST; the most
  conservative behavior consistent with `pkg/state.Open`'s own precedent (which detects and truncates a
  corrupted tail in `events.jsonl`) is to skip an unparseable trailing line rather than fail the whole
  read; this is an implementation-level choice within the "genuine read/parse failure" boundary and does
  not require a contract change since `err` is already available for hard failures on other malformed
  content.

### `accounting.AttributeStage`

**Responsibility**: map one usage record's timestamp to the stage that produced it.

**Algorithm:**
```
1. matches = [w for w in windows if w.Start <= record.Timestamp < (w.End or +inf)]
2. IF len(matches) == 1: return matches[0].StageID, true
   ELSE: return "", false
```

**Errors:** none — `ok bool` return, never an error.

**Edge Cases:**
- Record timestamp exactly equal to a window's `Start` → included (half-open interval, inclusive start).
- Two overlapping windows (parallel top-level stages) both matching → `ok=false` (Constraint, accepted risk).

### `accounting.ReadResultUsage`

**Responsibility**: extract authoritative tokens/cost/duration from one phase's terminal `result` event.

**Algorithm:**
```
1. IF file at jsonlPath does not exist or is unreadable: return zero-value, false
2. Read line by line; track the last line where json field "type" == "result"
3. IF no such line found: return zero-value, false
4. Parse total_cost_usd, usage.{input,output,cache_creation_input,cache_read_input}_tokens,
   session_id, duration_ms into ResultUsage (StageID supplied by the caller)
5. Return usage, true
```

**Errors:** none — `ok bool` return, mirroring `pkg/executor.WrittenFiles`'s precedent.

**Edge Cases:**
- `result` event with `"is_error":true` (e.g. an API 529, as literally occurred in this stage's own prior
  run attempt) still has a valid `usage`/`total_cost_usd` shape (observed: all-zero tokens, non-zero
  `total_cost_usd`) — `ReadResultUsage` returns it as-is; `ok=true` is about *parseability*, not about
  whether the underlying agent invocation succeeded.

### `accounting.Aggregate`

Covered in full in the Code Stack Trace section above (post Applied Fix); Responsibility: combine
attributed records + fallback result-usages into `[]UsageAggregate` per the requested `metric`/`stage`.

**Errors:** none — a pure function over already-loaded slices.

**Edge Cases:**
- `records` empty and `resultUsages` empty (no proxy activity, no completed phases yet) → `[]`.
- `stage` filters to a stage with zero attributable records/result-usages → `[]` for that stage (not an
  error — the handler still responds `200 []`).

### `server.UsageHandler`

Covered in full in the Code Stack Trace section above; Responsibility: HTTP adapter over `Accountant.Query`.

**Errors:**
- Invalid `metric` → `400`.
- `accountant.Query` error → `500`.

**Edge Cases:**
- `stage` omitted → all stages (default `""`, matches `Query`'s own default semantics — no handler-level
  translation needed).

## Cross-cutting Concerns

- **Error handling**: two distinct policies coexist by design. (1) *Capture-path* (`Proxy.ServeHTTP`'s use
  of `ParseUsage`/`AppendUsageRecord`) is fire-and-forget — errors are logged and never surface to the
  agent's HTTP response, because the response was already streamed before capture runs. (2) *Query-path*
  (`Accountant.Query`, `UsageHandler`) surfaces real errors normally (`err`/`500`) but treats "no data yet"
  (missing `usage.jsonl`, empty `pricing.Models`, no attributable records) as a valid empty/zero result,
  never an error — this distinction is load-bearing for the "pricing optional" and "proxy optional"
  acceptance criteria.
- **Validation**: `UsageHandler` validates `metric ∈ {tokens, cost, kb}` (400 on violation); `stage` is
  unvalidated (an unknown `stage` value simply yields zero attributable records, not an error — consistent
  with `Aggregate`'s filter semantics, which does not distinguish "unknown stage" from "known stage with no
  activity").
- **Logging**: capture-path failures (`ParseUsage`/`AppendUsageRecord` errors) are logged at a
  warning/info level analogous to the existing `warning: proxy shim: …` pattern in `cmd/afm` — non-fatal,
  informational, not surfaced to the end user beyond the log.
- **Caching**: none. Per `docs/tasks/master.md`'s explicit Out-of-Scope decision ("агрегация считается
  по требованию (on-demand)... schema RunState/StageState не меняется"), `Accountant.Query` recomputes
  everything from `usage.jsonl` + `store.History()` + `<phase>.jsonl` files on every call — no persisted
  aggregate cache, no `state.json` schema change. This resolves the task doc's open design question (б)
  explicitly in favor of "no cache."
- **Concurrency**: `AppendUsageRecord` relies on the same OS-level single-write atomicity as the
  precedented `pkg/state.SaveFeedback` (open-append-write-close per call, no shared file handle, no
  explicit mutex) — accepted as sufficient given the existing project convention and the small,
  single-line JSON record size. `Accountant.Query` is read-only and safe for concurrent dashboard polling
  (no shared mutable state between calls — each call re-reads from disk/`Store`). The task doc's
  "usage-log growth on long runs with many parallel subagents" risk remains an accepted, documented
  limitation (no rotation implemented in this plan — out of scope per `docs/tasks/master.md`).

## Usages Analysis

### `store_facade` (imported by `pkg/accounting` from `pkg/state`)
- **What it provides**: documented read patterns for `Store` (`Get`/`Snapshot`, and now implicitly
  `History`, per this plan's addition).
- **Where used**: `Accountant.Query` (via `store.Snapshot().StageOrder`), `LoadStageWindows` (via
  `store.History()`).
- **Why chosen**: `pkg/accounting` must consume `Store` without duplicating `pkg/state`'s file-I/O
  conventions — routing through the facade avoids reimplementing replay/read logic.
- **How exactly**: `store.History()` for the full transition log, `store.Snapshot().StageOrder` for stage
  enumeration — both read-only, no `Apply` calls (Constraint honored).

### `usage_log` (new, `pkg/proxy/.usages/usage_log.md`, consumed by `pkg/accounting`)
- **What it provides**: the exact `usage.jsonl` line format and the "absence = no active proxy" semantic.
- **Where used**: `LoadUsageRecords`. Formally connected during this design-review stage (Remark 2): the
  `pkg/proxy` import entry in `pkg/accounting/CODEMANIFEST` was missing `Usages: [usage_log]` — the
  contract only imported the `UsageRecord` type, so this consumption relationship existed in the design
  document but not in the actual contract. Fixed by adding `Usages: [usage_log]` to the import entry and
  a `` `usage_log` `` backtick reference in `LoadUsageRecords`'s own annotation. Re-validated: `goga lint`
  → `cells: 17 errors: 0`.
- **Why chosen**: `pkg/accounting` must not guess `usage.jsonl`'s shape — it's owned by `pkg/proxy`
  (`AppendUsageRecord`'s writer), so the consuming cell documents its read pattern via this practice file
  rather than duplicating format knowledge inline in CODEMANIFEST annotations.
- **How exactly**: `proxy.LoadUsageRecords(filepath.Join(runDir, "usage.jsonl"))` — note this practice
  file's example calls the function as `proxy.LoadUsageRecords`, while the actual entity lives in
  `pkg/accounting` per the CODEMANIFEST (`LoadUsageRecords` is declared in `pkg/accounting/CODEMANIFEST`,
  not `pkg/proxy/CODEMANIFEST`) — this is a naming inconsistency in the practice file's example code
  worth flagging for the implementation stage (the practice's *domain description* is correct — it
  documents `pkg/proxy`'s output format for `pkg/accounting`'s consumption — but the example snippet's
  package qualifier should read `accounting.LoadUsageRecords`, not `proxy.LoadUsageRecords`). This is a
  `.usages/` wording defect, not a CODEMANIFEST contract defect (practices carry no contractual weight per
  the DSL), so it is noted here for the implementation stage to correct rather than fixed via a CODEMANIFEST edit.

### `query_usage` (new, `pkg/accounting/.usages/query_usage.md`, consumed by `pkg/server`/`cmd/afm`)
- **What it provides**: `Accountant` construction and querying patterns, including the "pricing not
  configured" handling idiom.
- **Where used**: `server.Config.Accountant` construction (`server_facade.md`'s updated example),
  `cmd/afm`'s step `2b.` wiring.
- **Why chosen**: both consumers need the identical construction call
  (`accounting.NewAccountant(runDir, store, cfg.Pricing, cfg.Accounting.GetBucketMinutes())`) —
  documenting it once in the provider cell's
  `.usages/` avoids drift between `cmd/afm` and any future consumer.
- **How exactly**: construct once per run in `cmd/afm`, pass the single instance into `server.Config`;
  `pkg/server`'s `UsageHandler` calls `.Query(metric, stage)` per HTTP request on that same instance.

### `conventions` / `golang` (all 7 cells)
- **What it provides**: project-wide code style and Go implementation conventions.
- **Where used**: global `Annotations` in every touched/created cell.
- **Why chosen**: pre-existing project-level practice, unchanged by this plan.
- **How exactly**: applied uniformly, no cell-specific deviation introduced.

## `.usages/` Update

### Cell: `pkg/state`
- No `.usages/` change — `store_facade.md` (if present) or the absence of one is unaffected; `History`
  is a same-domain read-only addition alongside `Get`/`Snapshot`, per the architecture plan's explicit
  decision to defer any example update as optional/non-blocking.

### Cell: `pkg/proxy`
#### Existing Files — Consistency
- **`proxy_facade.md`** → `pkg/proxy/.usages/proxy_facade.md`
  - Status: current (already updated to the 3-arg `proxy.New` call by the Apply stage).
  - Additions needed: none.
  - Updates needed: none.
#### New Files
- **`usage_log`** → `pkg/proxy/.usages/usage_log.md`
  - Reason: new functional domain (consuming the usage log), distinct from "starting the proxy"
    (`proxy_facade.md`'s domain).
  - Related entities: `UsageRecord`, `ParseUsage`, `AppendUsageRecord` (documents `pkg/proxy`'s output
    for `pkg/accounting`'s `LoadUsageRecords`).
  - Note: example snippet's package qualifier (`proxy.LoadUsageRecords`) should be corrected to
    `accounting.LoadUsageRecords` at implementation time (see Usages Analysis above) — wording only, no
    contract impact.

### Cell: `pkg/config`
#### Existing Files — Consistency
- **`config_facade.md`** → `pkg/config/.usages/config_facade.md`
  - Status: current (pricing-lookup snippet already appended by the Apply stage, correctly placed under
    "Reading optional fields safely" alongside the other pointer-field getters).
  - Additions needed: none.
  - Updates needed: none.

### Cell: `pkg/accounting`
#### New Files
- **`query_usage`** → `pkg/accounting/.usages/query_usage.md`
  - Reason: first and only functional domain for this new cell (querying consumption aggregates).
  - Related entities: `Accountant`, `NewAccountant`, `Query`.
  - Status: current, self-contained, no cross-references to other practices (consistent with the
    cookbook's "practices must not reference other practices" rule).

### Cell: `pkg/server`
#### Existing Files — Consistency
- **`server_facade.md`** → `pkg/server/.usages/server_facade.md`
  - Status: updated during this review — the `server.New` example's `Accountant:
    accounting.NewAccountant(runDir, store, cfg.Pricing)` call was missing the new `bucketMinutes`
    argument added by this review's Remark 1 fix; corrected to
    `accounting.NewAccountant(runDir, store, cfg.Pricing, cfg.Accounting.GetBucketMinutes())`. The
    callback-coupling note correctly remains unchanged since `Accountant` is a data dependency, not a
    callback.
  - Additions needed: none.
  - Updates needed: none (applied above).

### Cell: `pkg/web/dashboard`
- No `.usages/` change — the panel is additions to existing bundled files (`app.js`), not new files; the
  existing `dashboard_assets.md` (if present) already documents "new files in the directory are
  automatically part of the embedded bundle" per the architecture plan, which covers this case.

### Cell: `cmd/afm`
- No `.usages/` change — `cmd/afm` is a consumer-only cell (no `.usages/` directory of its own to update);
  its consumption of `query_usage` and `usage_log`-adjacent practices is already reflected in its
  CODEMANIFEST `Imports`/Annotations.

## Test Stack Trace

### General Setup

Tests live alongside each entity's Go file (`*_test.go` in the same package, per project convention). No
new external test dependency — `pgregory.net/rapid` is already available project-wide for
property-based edge-case tests where useful (e.g. `AttributeStage`'s interval logic), but table-driven
`testing` tests are the default per `conventions`/`testing` practices.

### Source File Registry

- `pkg/proxy/usage.go` — `UsageRecord`, `ParseUsage`, `AppendUsageRecord`.
- `pkg/proxy/proxy.go` — `Proxy.ServeHTTP` capture wiring (existing file, modified).
- `pkg/config/config.go` — `PricingConfig`, `ModelPricing` (existing file, modified).
- `pkg/state/store.go` — `Store.History` (existing file, modified).
- `pkg/accounting/accountant.go` — `Accountant`, `NewAccountant`, `Query`.
- `pkg/accounting/cost.go` — `DeriveCost`.
- `pkg/accounting/attribution.go` — `StageWindow`, `LoadStageWindows`, `AttributeStage`.
- `pkg/accounting/reader.go` — `LoadUsageRecords`, `ReadResultUsage`, `ResultUsage`.
- `pkg/accounting/aggregate.go` — `Aggregate`, `UsageAggregate`.
- `pkg/server/usage_handler.go` — `UsageHandler`.

---

### Positive Tests

#### `test_derive_cost_computes_weighted_sum`

**Setup**: `pricing := ModelPricing{InputPerMtok: 3.0, OutputPerMtok: 15.0, CachePerMtok: 0.3}`.

**Input**: `inputTokens=1_000_000, outputTokens=200_000, cacheTokens=500_000`.

**Trace**:
```
test_derive_cost_computes_weighted_sum(1_000_000, 200_000, 500_000, pricing)
  → DeriveCost(1_000_000, 200_000, 500_000, pricing)
      inputCost  = 1_000_000 * 3.0  / 1_000_000 = 3.0
      outputCost = 200_000   * 15.0 / 1_000_000 = 3.0
      cacheCost  = 500_000   * 0.3  / 1_000_000 = 0.15
    returns costUsd = 6.15
  → assert costUsd == 6.15
```

**Assertions**: `costUsd == 6.15` (float comparison with a small epsilon, e.g. `1e-9`).

**Sufficiency**: pins the exact per-Mtok arithmetic and the three-term sum so a future refactor cannot
silently drop one token category or divide by the wrong constant.

---

#### `test_load_stage_windows_pairs_running_with_terminal`

**Setup**: an in-memory `Store` (or a fake satisfying the same read-only surface) whose `History()`
returns:
```
[
  {Seq:1, Time:"2026-07-07T10:00:00Z", StageID:"design", From:"pending", To:"running"},
  {Seq:2, Time:"2026-07-07T10:05:00Z", StageID:"design", From:"running", To:"done"},
]
```

**Input**: the `store` above.

**Trace**:
```
test_load_stage_windows_pairs_running_with_terminal(store)
  → LoadStageWindows(store)
      → store.History() returns the 2 transitions above
      pairs seq1 (running) with seq2 (done, same stageID)
    returns [StageWindow{StageID:"design", Start:"2026-07-07T10:00:00Z", End:"2026-07-07T10:05:00Z"}], nil
  → assert len(windows) == 1 and windows[0] matches
```

**Assertions**: `windows == [StageWindow{"design","2026-07-07T10:00:00Z","2026-07-07T10:05:00Z"}]`, `err == nil`.

**Sufficiency**: verifies the core running→terminal pairing logic that all downstream attribution depends on.

---

#### `test_attribute_stage_matches_single_window`

**Setup**: `windows := []StageWindow{{StageID:"design", Start:"2026-07-07T10:00:00Z", End:"2026-07-07T10:05:00Z"}}`.

**Input**: `record := UsageRecord{Timestamp:"2026-07-07T10:02:00Z", Model:"claude-sonnet-5", ...}`.

**Trace**:
```
test_attribute_stage_matches_single_window(record, windows)
  → AttributeStage(record, windows)
      matches = [windows[0]]  (10:02 is within [10:00, 10:05))
    returns "design", true
  → assert stageID == "design" and ok == true
```

**Assertions**: `stageID == "design"`, `ok == true`.

**Sufficiency**: the baseline non-overlapping-window case every other `AttributeStage` test builds on.

---

#### `test_aggregate_cost_metric_uses_derive_cost`

**Setup**: `pricing := PricingConfig{Models: map[string]ModelPricing{"claude-sonnet-5": {InputPerMtok:3.0, OutputPerMtok:15.0, CachePerMtok:0.3}}}`;
`windows := []StageWindow{{StageID:"design", Start:"2026-07-07T10:00:00Z", End:""}}`.

**Input**: `records := []UsageRecord{{Timestamp:"2026-07-07T10:01:00Z", Model:"claude-sonnet-5", InputTokens:1000, OutputTokens:200, CacheCreationTokens:0, CacheReadTokens:0}}`, `resultUsages := []`, `metric := "cost"`, `stage := ""`, `bucketMinutes := 5`.

**Trace**:
```
test_aggregate_cost_metric_uses_derive_cost(records, windows, [], pricing, "cost", "", 5)
  → Aggregate(...)
      AttributeStage(records[0], windows) → "design", true
      pricing.GetModelPricing("claude-sonnet-5") → {3.0,15.0,0.3}, true
      DeriveCost(1000, 200, 0, {3.0,15.0,0.3}) → 0.003 + 0.003 + 0 = 0.006
      TimeBucket(10:01:00Z, bucketMinutes=5) → 10:01 floored to the nearest 5-minute UTC
      wall-clock boundary → "2026-07-07T10:00:00Z"
      group by (design, "2026-07-07T10:00:00Z") → sum = 0.006
    returns [UsageAggregate{StageID:"design", TimeBucket:"2026-07-07T10:00:00Z", Metric:"cost", Value:0.006}]
  → assert len == 1 and Value == 0.006 and TimeBucket == "2026-07-07T10:00:00Z"
```

**Assertions**: one `UsageAggregate` row, `Value == 0.006` (epsilon-compared), `TimeBucket ==
"2026-07-07T10:00:00Z"`.

**Sufficiency**: directly exercises the Applied Fix — confirms `Aggregate` actually invokes `DeriveCost`
for the cost metric rather than leaving `Value` undefined, closing the gap found during Phase 3/4 tracing.

---

#### `test_accountant_query_combines_all_three_sources`

**Setup**: a fixture `runDir` containing:
- `usage.jsonl` with one line: `{"timestamp":"2026-07-07T10:01:00Z","model":"claude-sonnet-5","inputTokens":1000,"outputTokens":200,"cacheCreationTokens":0,"cacheReadTokens":0,"requestBytes":500,"responseBytes":1500}`
- `implementation.jsonl` with a terminal `result` line for a *different*, already-completed phase:
  `{"type":"result","total_cost_usd":0.05,"usage":{"input_tokens":2000,"output_tokens":400,"cache_creation_input_tokens":0,"cache_read_input_tokens":0},"session_id":"xyz","duration_ms":9000}`

A `Store` (or fake satisfying its read-only surface) whose `History()` returns two transitions putting
stage `design` into `running` at `2026-07-07T10:00:00Z` then `done` at `2026-07-07T10:10:00Z`, and whose
`Snapshot().StageOrder` is `["design"]`. `pricing := PricingConfig{}` (zero-value — `metric=tokens`
requested, so pricing is irrelevant here). `bucketMinutes := 5`.

**Input**: `accountant := NewAccountant(runDir, store, pricing, 5)`; call `accountant.Query("tokens", "")`.

**Trace**:
```
test_accountant_query_combines_all_three_sources(runDir, store, pricing)
  → Query("tokens", "")
      LoadUsageRecords(runDir/usage.jsonl) → [UsageRecord{Timestamp:"2026-07-07T10:01:00Z", InputTokens:1000, OutputTokens:200, ...}], nil
      LoadStageWindows(store) → [StageWindow{StageID:"design", Start:"2026-07-07T10:00:00Z", End:"2026-07-07T10:10:00Z"}], nil
      FOR stageID "design": ReadResultUsage(implementation.jsonl) → ResultUsage{StageID:"design", InputTokens:2000, OutputTokens:400, ...}, true
      Aggregate(records, windows, resultUsages, pricing, "tokens", "", 5)
        AttributeStage(record, windows) → "design", true — the usage.jsonl record IS attributed
        stage "design" has a non-zero proxy record this run, so the resultUsages fallback for
        "design" is NOT substituted (fallback only applies when a stage has zero proxy records)
        value = 1000+200+0+0 = 1200; TimeBucket("2026-07-07T10:01:00Z", 5) = "2026-07-07T10:00:00Z"
      returns [UsageAggregate{StageID:"design", TimeBucket:"2026-07-07T10:00:00Z", Metric:"tokens", Value:1200}]
    returns aggregates, nil
  → assert len(aggregates) == 1 and aggregates[0].Value == 1200
```

**Assertions**: `err == nil`; `len(aggregates) == 1`; `aggregates[0] == UsageAggregate{StageID:"design",
TimeBucket:"2026-07-07T10:00:00Z", Metric:"tokens", Value:1200}` — critically, `Value` is `1200` (from
the proxy record), **not** `1200+2600` (it must not double-count the `ResultUsage` fallback for a stage
that already has an attributed proxy record).

**Sufficiency**: this is the only test in the whole suite that exercises `Accountant.Query`'s actual
orchestration — that it wires `LoadUsageRecords`/`LoadStageWindows`/`ReadResultUsage`/`Aggregate`
together with the right arguments (including the `bucketMinutes` threaded through from construction) and
that the "proxy record present → no fallback double-count" rule (`Aggregate` step 3) holds end-to-end,
not just in each function's isolated unit test.

---

#### `test_usage_handler_returns_aggregates_as_json`

**Setup**: a stub `Accountant` (or the real one over a fixture `runDir`) whose `Query("tokens", "")`
returns `[]UsageAggregate{{StageID:"design", TimeBucket:"2026-07-07T10:00:00Z", Metric:"tokens", Value:1200}}, nil`.

**Input**: `GET /api/usage?metric=tokens&stage=`.

**Trace**:
```
test_usage_handler_returns_aggregates_as_json(request)
  → UsageHandler(stubAccountant).ServeHTTP(w, request)
      parse metric="tokens", stage=""
      validate metric ∈ {tokens,cost,kb} → ok
      stubAccountant.Query("tokens", "") → aggregates, nil
    writes 200 + json.Marshal(aggregates)
  → assert response.StatusCode == 200
  → assert response.Body decodes to the one UsageAggregate above
```

**Assertions**: `StatusCode == 200`; decoded body `== [{StageID:"design", TimeBucket:"2026-07-07T10:00:00Z", Metric:"tokens", Value:1200}]`.

**Sufficiency**: confirms the HTTP contract end-to-end for the happy path the dashboard relies on.

---

#### `test_pricing_config_get_model_pricing_found`

**Setup**: `cfg := PricingConfig{Models: map[string]ModelPricing{"claude-sonnet-5": {InputPerMtok:3.0, OutputPerMtok:15.0, CachePerMtok:0.3}}}`.

**Input**: `model := "claude-sonnet-5"`.

**Trace**:
```
test_pricing_config_get_model_pricing_found(cfg, "claude-sonnet-5")
  → cfg.GetModelPricing("claude-sonnet-5")
      map lookup hits
    returns {3.0,15.0,0.3}, true
  → assert pricing == {3.0,15.0,0.3} and ok == true
```

**Assertions**: `pricing.InputPerMtok == 3.0`, `ok == true`.

**Sufficiency**: baseline for the pricing lookup every cost computation depends on.

---

#### `test_store_history_returns_ordered_transitions`

**Setup**: a `Store` opened over a fixture `runDir` whose `events.jsonl` contains 3 append-order lines
for stages `a` (pending→running→done).

**Input**: none (method call on the opened store).

**Trace**:
```
test_store_history_returns_ordered_transitions(store)
  → store.History()
      returns the 3 replayed Transition values in Seq-ascending order
  → assert len == 3 and Seq values are 1,2,3 in order
```

**Assertions**: `transitions[i].Seq < transitions[i+1].Seq` for all `i`; `err == nil`.

**Sufficiency**: pins the ordering guarantee `LoadStageWindows` depends on without re-sorting.

---

#### `test_load_usage_records_reads_existing_records`

**Setup**: `<tmpdir>/usage.jsonl` written with 2 lines:
```
{"timestamp":"2026-07-07T10:00:00Z","model":"claude-sonnet-5","inputTokens":1200,"outputTokens":340,"cacheCreationTokens":0,"cacheReadTokens":800,"requestBytes":2048,"responseBytes":6144}
{"timestamp":"2026-07-07T10:01:00Z","model":"claude-sonnet-5","inputTokens":500,"outputTokens":100,"cacheCreationTokens":0,"cacheReadTokens":0,"requestBytes":1024,"responseBytes":2048}
```

**Input**: the path to that file.

**Trace**:
```
test_load_usage_records_reads_existing_records(path)
  → LoadUsageRecords(path)
      reads 2 lines, json.Unmarshal each into UsageRecord
    returns [record1, record2], nil
  → assert len(records) == 2 and records[0].InputTokens == 1200
```

**Assertions**: `len(records) == 2`; `records[0] == UsageRecord{Timestamp:"2026-07-07T10:00:00Z", Model:"claude-sonnet-5", InputTokens:1200, OutputTokens:340, CacheReadTokens:800, RequestBytes:2048, ResponseBytes:6144}`; `err == nil`.

**Sufficiency**: confirms the documented `usage_log.md` format (exact field names/shape) round-trips correctly through `LoadUsageRecords` — the format `pkg/proxy` writes and `pkg/accounting` reads must agree byte-for-byte on JSON key names.

---

#### `test_read_result_usage_parses_terminal_result_event`

**Setup**: `<tmpdir>/implementation.jsonl` written with an assistant line followed by a real observed
`result` line shape:
```
{"type":"assistant","message":{"content":[{"type":"text","text":"working"}]}}
{"type":"result","subtype":"success","total_cost_usd":0.42,"usage":{"input_tokens":5000,"output_tokens":800,"cache_creation_input_tokens":100,"cache_read_input_tokens":2000},"session_id":"abc-123","duration_ms":45000}
```

**Input**: `jsonlPath` above, caller-supplied `stageID := "implementation"`.

**Trace**:
```
test_read_result_usage_parses_terminal_result_event(jsonlPath, "implementation")
  → ReadResultUsage(jsonlPath)
      scans lines, finds the last type=="result" line
      parses total_cost_usd, usage.*, session_id, duration_ms
    returns ResultUsage{StageID:"implementation", InputTokens:5000, OutputTokens:800,
      CacheCreationTokens:100, CacheReadTokens:2000, TotalCostUsd:0.42, DurationMs:45000,
      SessionID:"abc-123"}, true
  → assert ok == true and usage.InputTokens == 5000
```

**Assertions**: `ok == true`; every `ResultUsage` field matches the source JSON exactly (no field
dropped/mistranslated).

**Sufficiency**: confirms the jsonl-`result` field mapping this design traced against a real observed
event (see the `ReadResultUsage` Code Stack Trace) is implemented faithfully, not just assumed.

---

#### `test_parse_usage_parses_non_streaming_json_response`

**Setup**: none beyond the input.

**Input**: `contentType := "application/json"`, `body := '{"model":"claude-sonnet-5","usage":{"input_tokens":1200,"output_tokens":340,"cache_creation_input_tokens":0,"cache_read_input_tokens":800}}'`.

**Trace**:
```
test_parse_usage_parses_non_streaming_json_response(contentType, body)
  → ParseUsage(contentType, body)
      contentType does not contain "text/event-stream" → JSON branch
      parses body as one JSON object, reads .model and .usage
    returns UsageRecord{Model:"claude-sonnet-5", InputTokens:1200, OutputTokens:340,
      CacheCreationTokens:0, CacheReadTokens:800}, nil
  → assert err == nil and record.Model == "claude-sonnet-5"
```

**Assertions**: `err == nil`; `record.InputTokens == 1200`, `record.CacheReadTokens == 800`
(`RequestBytes`/`ResponseBytes` are zero — left to the caller, per Requirement).

**Sufficiency**: exercises the non-SSE branch of `ParseUsage`, which is the branch that makes "passthrough
тоже считается" (a stated acceptance criterion) possible — most passthrough responses to `stream:true`
requests are plain JSON, not SSE.

---

#### `test_parse_usage_parses_sse_stream_response`

**Setup**: none beyond the input.

**Input**: `contentType := "text/event-stream; charset=utf-8"`, `body :=` the following SSE event
sequence (mirrors the shape `zai.go`'s `parseSSE` already handles, generalized by `ParseUsage`):
```
event: message_start
data: {"type":"message_start","message":{"id":"msg_1","role":"assistant","model":"claude-sonnet-5","usage":{"input_tokens":1200,"cache_creation_input_tokens":0,"cache_read_input_tokens":800,"output_tokens":0}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":340}}

event: message_stop
data: {"type":"message_stop"}
```

**Trace**:
```
test_parse_usage_parses_sse_stream_response(contentType, body)
  → ParseUsage(contentType, body)
      contentType contains "text/event-stream" → SSE branch
      parse SSE events in order:
        message_start → model="claude-sonnet-5", InputTokens=1200, CacheCreationTokens=0, CacheReadTokens=800
        content_block_delta → text delta, no usage field, ignored for usage purposes
        message_delta → merge usage.output_tokens=340 into the accumulated record
    returns UsageRecord{Model:"claude-sonnet-5", InputTokens:1200, OutputTokens:340,
      CacheCreationTokens:0, CacheReadTokens:800}, nil
  → assert err == nil and record.Model == "claude-sonnet-5" and record.OutputTokens == 340
```

**Assertions**: `err == nil`; `record.InputTokens == 1200`, `record.OutputTokens == 340`,
`record.CacheReadTokens == 800` — confirms the `message_start` and `message_delta` events' usage fields
are both correctly accumulated into one `UsageRecord` (not just one or the other).

**Sufficiency**: exercises `ParseUsage`'s SSE branch, the *generalized* new logic this design introduces
(as opposed to the JSON branch, which only mirrors `ZAITransform`'s already-reassembled shape) — without
this test, a regression that breaks SSE parsing for a non-`api.z.ai` upstream (the primary reason the
tee-capture/`ParseUsage` mechanism exists at all) would not be caught by any test in this suite.

---

#### `test_append_usage_record_writes_one_json_line`

**Setup**: `<tmpdir>/usage.jsonl` does not exist yet.

**Input**: `record := UsageRecord{Timestamp:"2026-07-07T10:00:00Z", Model:"claude-sonnet-5", InputTokens:1200, OutputTokens:340, RequestBytes:2048, ResponseBytes:6144}`.

**Trace**:
```
test_append_usage_record_writes_one_json_line(path, record)
  → AppendUsageRecord(path, record)
      os.OpenFile creates the file (O_CREATE), marshals record, writes one line, closes
    returns nil
  → assert file now contains exactly 1 line, which round-trips to `record` via json.Unmarshal
```

**Assertions**: `err == nil`; reading the file back yields exactly one line whose parsed `UsageRecord`
equals the input `record` field-for-field.

**Sufficiency**: confirms the writer half of the round-trip validated by
`test_load_usage_records_reads_existing_records` — together the two tests pin the full write→read
contract for `usage.jsonl` without relying on a live proxy.

---

### Negative Tests

#### `test_load_stage_windows_propagates_history_error`

**Setup**: a fake `Store` whose `History()` is stubbed to return `nil, errors.New("events.jsonl: corrupt")`.

**Input**: the fake `store`.

**Trace**:
```
test_load_stage_windows_propagates_history_error(store)
  → LoadStageWindows(store)
      store.History() returns nil, err
    returns nil, err (propagated unchanged)
  → assert err != nil and windows == nil
```

**Assertions**: `err != nil`; `windows == nil`.

**Sufficiency**: confirms `LoadStageWindows` does not swallow a genuine `Store.History` failure into a
misleading empty-but-successful result — `Accountant.Query` depends on this propagation to surface real
failures as `err` rather than silently returning `[]`.

---

#### `test_accountant_query_propagates_load_stage_windows_error`

**Setup**: a fixture `runDir` with an empty/absent `usage.jsonl` (irrelevant to this test); a fake
`Store` whose `History()` is stubbed to return `nil, errors.New("events.jsonl: corrupt")` (same fake as
`test_load_stage_windows_propagates_history_error`, reused here one layer up); a spy wrapper around
`Aggregate` (or an equivalent call-count hook) to assert it is never invoked.

**Input**: `accountant := NewAccountant(runDir, store, PricingConfig{}, 5)`; call
`accountant.Query("tokens", "")`.

**Trace**:
```
test_accountant_query_propagates_load_stage_windows_error(runDir, store)
  → Query("tokens", "")
      LoadUsageRecords(runDir/usage.jsonl) → [], nil
      LoadStageWindows(store)
        store.History() returns nil, err
      returns nil, err (propagated unchanged)
    returns nil, err — Aggregate is never called
  → assert err != nil and aggregates == nil and Aggregate call count == 0
```

**Assertions**: `err != nil`; `aggregates == nil`; `Aggregate`'s call count is `0` (verified via the spy).

**Sufficiency**: closes the gap left by leaf-level tests alone — confirms `Query` itself (not just
`LoadStageWindows` in isolation) actually surfaces a hard `LoadStageWindows` failure as its own `err`
rather than swallowing it and calling `Aggregate` with a `nil`/empty `windows` slice, which would
silently misattribute or drop every record instead of failing loudly.

---

#### `test_parse_usage_returns_error_on_non_200`

**Setup**: none beyond the input.

**Input**: `contentType := "application/json"`, `body := ` a JSON error body with no `usage` field, simulating a non-200 upstream response.

**Trace**:
```
test_parse_usage_returns_error_on_non_200(contentType, body)
  → ParseUsage(contentType, body)
      JSON branch: parses body, finds no usage field
    returns UsageRecord{}, err (non-nil)
  → assert err != nil
  → assert record is the zero value (not partially populated)
```

**Assertions**: `err != nil`; `record == UsageRecord{}`.

**Sufficiency**: enforces the Constraint that a failed parse never yields a fabricated zero-usage record
that could silently pollute aggregates.

---

#### `test_append_usage_record_returns_error_on_unwritable_path`

**Setup**: `path := "/nonexistent-dir/usage.jsonl"` (parent directory does not exist).

**Input**: `record := UsageRecord{Model:"claude-sonnet-5", InputTokens:10}`.

**Trace**:
```
test_append_usage_record_returns_error_on_unwritable_path(path, record)
  → AppendUsageRecord(path, record)
      os.OpenFile fails: no such directory
    returns err (non-nil)
  → assert err != nil
```

**Assertions**: `err != nil`, wraps the underlying `os.OpenFile` error.

**Sufficiency**: confirms the capture-path's error return contract that `Proxy.ServeHTTP` relies on to
decide "log and swallow."

---

#### `test_pricing_config_get_model_pricing_unknown_model`

**Setup**: `cfg := PricingConfig{Models: map[string]ModelPricing{"claude-sonnet-5": {...}}}`.

**Input**: `model := "claude-opus-4-8"` (configured for a different model only).

**Trace**:
```
test_pricing_config_get_model_pricing_unknown_model(cfg, "claude-opus-4-8")
  → cfg.GetModelPricing("claude-opus-4-8")
      map lookup misses, no fuzzy fallback attempted
    returns ModelPricing{}, false
  → assert ok == false
```

**Assertions**: `ok == false`; `pricing == ModelPricing{}`.

**Sufficiency**: enforces the explicit "no fuzzy/prefix matching" Constraint — a near-miss model name
must not silently borrow another model's rate.

---

#### `test_read_result_usage_missing_file_returns_not_ok`

**Setup**: `jsonlPath := "<tmpdir>/implementation.jsonl"` — file never created.

**Input**: the path above.

**Trace**:
```
test_read_result_usage_missing_file_returns_not_ok(jsonlPath)
  → ReadResultUsage(jsonlPath)
      file does not exist
    returns ResultUsage{}, false
  → assert ok == false
```

**Assertions**: `ok == false`; no panic/error propagated (per the `WrittenFiles`-precedented "missing → empty" convention).

**Sufficiency**: confirms the jsonl-fallback path degrades gracefully for stages whose phase never ran (e.g. `review` skipped).

---

#### `test_usage_handler_rejects_invalid_metric`

**Setup**: any `Accountant` (never reached).

**Input**: `GET /api/usage?metric=dollars` (not one of `tokens|cost|kb`).

**Trace**:
```
test_usage_handler_rejects_invalid_metric(request)
  → UsageHandler(accountant).ServeHTTP(w, request)
      parse metric="dollars"
      validate metric ∈ {tokens,cost,kb} → fails
    writes 400, accountant.Query is never called
  → assert response.StatusCode == 400
```

**Assertions**: `StatusCode == 400`; `accountant.Query` call count `== 0` (verified via a spy `Accountant`).

**Sufficiency**: confirms input validation happens before any query work, and that an invalid metric
cannot silently fall through to a default.

---

### Edge Case Tests

#### `test_pricing_config_get_model_pricing_nil_models_map`

**Setup**: `cfg := PricingConfig{}` (zero value — `Models` is `nil`, i.e. the `pricing:` YAML section is
absent entirely).

**Input**: `model := "claude-sonnet-5"`.

**Trace**:
```
test_pricing_config_get_model_pricing_nil_models_map(cfg, "claude-sonnet-5")
  → cfg.GetModelPricing("claude-sonnet-5")
      read on a nil map is a safe no-op in Go — lookup misses
    returns ModelPricing{}, false
  → assert ok == false (no panic)
```

**Assertions**: `ok == false`; no panic on the nil-map read.

**Sufficiency**: this is the exact zero-config path the "pricing optional" acceptance criterion depends
on — a project with no `pricing:` section at all must degrade to `ok=false` for every lookup, never crash.

---

#### `test_store_history_empty_run_returns_empty_slice`

**Setup**: a `Store` opened over a freshly created `runDir` (no prior `events.jsonl`, every stage still `pending`).

**Input**: none.

**Trace**:
```
test_store_history_empty_run_returns_empty_slice(store)
  → store.History()
      no transitions were ever replayed (every stage is pending, no Apply calls happened)
    returns [], nil
  → assert len(transitions) == 0 and err == nil
```

**Assertions**: `transitions == []`; `err == nil`.

**Sufficiency**: confirms a brand-new run (no stage has started yet, e.g. the dashboard is opened
immediately after `afm run` starts) does not error out of `LoadStageWindows`/`Accountant.Query` — it must
produce empty aggregates, not a failure.

---

#### `test_usage_handler_defaults_stage_to_all_and_surfaces_query_error`

**Setup**: a stub `Accountant` whose `Query("tokens", "")` is stubbed to return `nil, errors.New("usage.jsonl: permission denied")`.

**Input**: `GET /api/usage` (no `stage` query param at all, exercising the `stage` default).

**Trace**:
```
test_usage_handler_defaults_stage_to_all_and_surfaces_query_error(request)
  → UsageHandler(stubAccountant).ServeHTTP(w, request)
      parse metric="tokens" (default), stage="" (default — absent param)
      validate metric → ok
      stubAccountant.Query("tokens", "") → nil, err
    writes 500
  → assert response.StatusCode == 500
  → assert stubAccountant received stage == "" (not some other sentinel for "unset")
```

**Assertions**: `StatusCode == 500`; the recorded call to `Query` used `stage == ""`, confirming the
handler's default matches `Query`'s own "empty = all stages" semantics exactly (no handler-side
translation like `"*"` or `"all"`).

**Sufficiency**: covers both the default-parameter edge case and the `Query`-error → `500` branch in one
test, since both are edge conditions on the same handler and neither is exercised by the two tests above.

---

#### `test_load_usage_records_missing_file_returns_empty_not_error`

**Setup**: `path := "<tmpdir>/usage.jsonl"` — file never created (proxy inactive this run).

**Input**: the path above.

**Trace**:
```
test_load_usage_records_missing_file_returns_empty_not_error(path)
  → LoadUsageRecords(path)
      os.Stat/Open reports "does not exist"
    returns [], nil
  → assert records == [] and err == nil
```

**Assertions**: `len(records) == 0`; `err == nil` (explicitly not an error, per Constraint).

**Sufficiency**: this is the exact behavior the "pricing/proxy optional" acceptance criteria depend on —
regresses loudly (a spurious error) if ever miscoded as a hard failure.

---

#### `test_load_usage_records_skips_truncated_trailing_line`

**Setup**: `<tmpdir>/usage.jsonl` written with 2 well-formed lines followed by one truncated/malformed
3rd line (simulating a process killed mid-write):
```
{"timestamp":"2026-07-07T10:00:00Z","model":"claude-sonnet-5","inputTokens":1200,"outputTokens":340,"cacheCreationTokens":0,"cacheReadTokens":800,"requestBytes":2048,"responseBytes":6144}
{"timestamp":"2026-07-07T10:01:00Z","model":"claude-sonnet-5","inputTokens":500,"outputTokens":100,"cacheCreationTokens":0,"cacheReadTokens":0,"requestBytes":1024,"responseBytes":2048}
{"timestamp":"2026-07
```

**Input**: the path to that file.

**Trace**:
```
test_load_usage_records_skips_truncated_trailing_line(path)
  → LoadUsageRecords(path)
      reads line 1, json.Unmarshal succeeds → record1
      reads line 2, json.Unmarshal succeeds → record2
      reads line 3, json.Unmarshal fails (truncated JSON) — skipped, not a hard failure
      (mirrors pkg/state.Open's own precedent for a corrupted event-log tail)
    returns [record1, record2], nil
  → assert len(records) == 2 and err == nil
```

**Assertions**: `len(records) == 2`; `records[0].InputTokens == 1200` and `records[1].InputTokens == 500`
(both well-formed lines preserved, in order); `err == nil` (a malformed trailing line degrades gracefully,
it does not hard-fail the whole read).

**Sufficiency**: pins the Algorithm Design section's explicit "skip an unparseable trailing line" decision
(previously asserted only in prose, citing `pkg/state.Open`'s corrupted-tail precedent) as an actual
test — without this, an implementer could equally validly hard-fail the entire read on any malformed
line without violating any other written test or CODEMANIFEST Constraint.

---

#### `test_attribute_stage_ambiguous_overlap_returns_not_ok`

**Setup**: `windows := []StageWindow{{StageID:"a", Start:"2026-07-07T10:00:00Z", End:"2026-07-07T10:10:00Z"}, {StageID:"b", Start:"2026-07-07T10:00:00Z", End:"2026-07-07T10:10:00Z"}}` (two parallel top-level stages).

**Input**: `record := UsageRecord{Timestamp:"2026-07-07T10:05:00Z", ...}` (falls inside both windows).

**Trace**:
```
test_attribute_stage_ambiguous_overlap_returns_not_ok(record, windows)
  → AttributeStage(record, windows)
      matches = [windows[0], windows[1]]  (both contain 10:05)
    returns "", false
  → assert ok == false
```

**Assertions**: `ok == false`; `stageID == ""`.

**Sufficiency**: pins the intentionally-conservative ambiguous-overlap behavior documented as an accepted
risk in `docs/tasks/master.md`, preventing a future "just pick the first match" regression that would
silently misattribute cost/tokens between concurrent stages.

---

#### `test_aggregate_cost_metric_skips_records_with_unknown_model_pricing`

**Setup**: `pricing := PricingConfig{Models: map[string]ModelPricing{"claude-sonnet-5": {...}}}` (no entry for `"claude-opus-4-8"`).

**Input**: `records := []UsageRecord{{Timestamp:"...", Model:"claude-opus-4-8", InputTokens:500, OutputTokens:100}}`, `windows` attributing it to stage `"design"`, `metric := "cost"`.

**Trace**:
```
test_aggregate_cost_metric_skips_records_with_unknown_model_pricing(records, windows, [], pricing, "cost", "")
  → Aggregate(...)
      AttributeStage(records[0], windows) → "design", true
      pricing.GetModelPricing("claude-opus-4-8") → ModelPricing{}, false
      record skipped (not included in any aggregate, per Aggregate step 4/5)
    returns [], nil
  → assert aggregates == []
```

**Assertions**: `len(aggregates) == 0`.

**Sufficiency**: confirms unpriced models silently drop out of cost aggregates rather than contributing a
wrong (zero-rate) cost value — directly guards the Applied Fix's new per-record derivation logic.

---

#### `test_derive_cost_zero_tokens_returns_zero`

**Setup**: `pricing := ModelPricing{InputPerMtok:3.0, OutputPerMtok:15.0, CachePerMtok:0.3}` — mirrors the
real `is_error:true` result event observed during this stage's own prior failed attempt
(`total_cost_usd:0.006087` but all-zero `usage` fields, since the error occurred before any tokens were
consumed server-side).

**Input**: `inputTokens=0, outputTokens=0, cacheTokens=0`.

**Trace**:
```
test_derive_cost_zero_tokens_returns_zero(0, 0, 0, pricing)
  → DeriveCost(0, 0, 0, pricing)
      all three terms are 0
    returns 0.0
  → assert costUsd == 0.0
```

**Assertions**: `costUsd == 0.0`.

**Sufficiency**: an all-zero-usage `result` event is a real, observed shape (API errors), not a
hypothetical — this test guards against a division-by-zero or NaN regression in the per-Mtok arithmetic.

---

#### `test_load_stage_windows_open_ended_for_still_running_stage`

**Setup**: `store.History()` returns a single transition `{Seq:1, Time:"2026-07-07T10:00:00Z", StageID:"implementation", From:"pending", To:"running"}` with no subsequent terminal transition.

**Input**: the `store` above.

**Trace**:
```
test_load_stage_windows_open_ended_for_still_running_stage(store)
  → LoadStageWindows(store)
      seq1 (running) has no later done/failed transition for "implementation"
    returns [StageWindow{StageID:"implementation", Start:"2026-07-07T10:00:00Z", End:""}], nil
  → assert windows[0].End == ""
```

**Assertions**: `windows[0].End == ""`.

**Sufficiency**: confirms the currently-executing-stage case (the most common real-time query scenario —
a user watching the dashboard while a stage is still running) produces a usable open-ended window rather
than an error or a bogus `End`.

---

## Additional Instructions for the Implementation Agent

- Follow `goga-cell-go`'s allowed-type list strictly for every new signature in `pkg/accounting`,
  `pkg/proxy`'s new entities, and `pkg/config`'s new entities — all are already primitive/`[]T`/`map[string]T`/
  custom-struct-typed per the CODEMANIFEST as verified in this design; do not introduce pointers or `interface{}`.
- Correct the `pkg/proxy/.usages/usage_log.md` example snippet's package qualifier from
  `proxy.LoadUsageRecords` to `accounting.LoadUsageRecords` while implementing `pkg/accounting/reader.go`
  (documentation wording fix, noted under Usages Analysis — no CODEMANIFEST change required).
- Implement `AppendUsageRecord` and `Store.History` using the exact precedented patterns cited in this
  document (`pkg/state.SaveFeedback` for open-append-write-close; the existing in-memory replay state for
  `History`) rather than inventing new I/O strategies.
- `Aggregate`'s per-metric value derivation (tokens/cost/kb) added by this design's Applied Fix is now
  part of the CODEMANIFEST contract — implement exactly the three branches described, and make sure
  `DeriveCost` is actually called from `Aggregate`'s cost branch (this was the defect found and fixed
  during this design stage; a test — `test_aggregate_cost_metric_uses_derive_cost` above — guards it).
- Do not implement `usage.jsonl` rotation or a `state.json`-persisted aggregate cache — both are
  explicitly out of scope per `docs/tasks/master.md` and this design's Cross-cutting Concerns → Caching.
- Run `goga lint` (expect `cells: 17 errors: 0`) and `goga schema --depends-on pkg/orchestrator` (expect
  `pkg/accounting` absent from the result) before considering the implementation stage complete, matching
  this design stage's own verification.
