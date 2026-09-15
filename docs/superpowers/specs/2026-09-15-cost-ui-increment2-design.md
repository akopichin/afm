# Design: Cost UI — Increment 2 (dashboard + server read model)

**Date:** 2026-09-15
**Status:** design, revised after four review rounds (`tmp/cost-ui-review.md`)
**Depends on:** `docs/superpowers/plans/2026-09-14-stage-and-run-pricing.md` (Increment 1 — `pkg/accounting`, `usage.jsonl`, `afm check`/`afm report`; landed on branch `pricing`). This spec is Increment 2 (Tasks 7/9/10), refined into a concrete UI + read model.

## Goal

Surface the token/cost accounting already in `usage.jsonl` inside the live dashboard — per stage and total, while a run runs — without dropping to the CLI. **No new accounting arithmetic.** Accounting gains small **presentation helpers** (`Coverage`, `DisplayCost`, `CoverageIssue`) so the CLI, server, and frontend all render one set of finished values — never re-deriving money or coverage.

## Terminology: health vs coverage (two orthogonal axes)

Never conflate these; the whole design turns on keeping them separate.

- **`coverage`** — "can I price the invocations I have?" — `full` / `partial` / `none`, from invocation **counts** (below). A perfectly healthy store routinely has `partial`/`none` coverage (an unpriced Codex model is not a failure).
- **`health`** — "is the accounting *writer* operational and did the ledger load cleanly?" — `ok` / `unavailable`. **`health` makes NO claim of historical completeness** (review round-4 #1): once a live write fails, the `Store` latches `unavailable` for the rest of that process; but a crash between a lost write and a restart cannot be detected — after reopen the valid prefix loads and `health` is `ok` again. So `ok` means "nothing is currently known to be wrong", NOT "provably complete". This limit is documented, not papered over; a durable degradation marker is a possible future addition, out of scope here. `health=unavailable` = "we KNOW writes are failing this session, or the ledger didn't load/parse".

## Presentation model (accounting helpers)

Added to `pkg/accounting`, tested there (the numbers already exist on `Summary`/`Records`):

- **`Coverage` enum — from COUNTS, never from the money value.** Let `priced = Metered - Unpriced` (metered invocations that resolved to a rate), `gaps = Unpriced + Unmetered`:
  - `full` — `priced ≥ 1` AND `gaps == 0`;
  - `partial` — `priced ≥ 1` AND `gaps ≥ 1`;
  - `none` — `priced == 0`.
  An explicit `0` rate is a valid priced result: a single zero-rate record ⇒ `full`, `$0.0000`; a **single** unpriced record ⇒ `none` (not `partial`); zero-rate + unpriced ⇒ `partial`, `$0.0000+`. Never gate on `CostUSD > 0`.
- **`DisplayCost` string** — reuses `FormatUSD`'s existing tiny-value handling for the priced sum (`Summary.CostUSD`), then annotates for coverage. Exact outputs, identical on every surface:
  | coverage | normal | genuine zero | tiny non-zero (`<0.0001`) |
  |---|---|---|---|
  | `full` | `$0.3995` | `$0.0000` | `<$0.0001` |
  | `partial` | `$0.3995+` | `$0.0000+` | `<$0.0001+` |
  | `none` | `—` | `—` | `—` |
  The `+` is produced HERE, never by UI string math. (Round-4 #8: tiny partial is `<$0.0001+`.)
- **`CoverageIssue`** (per gap invocation, extracted from `Records()` as `report.go`'s `renderCoverage` already does): `{kind: "unmetered"|"unpriced", stage, phase, channel, model, reason}`. A `RunSummary`-level `[]CoverageIssue` feeds the coverage line and the header amber marker — naming the specific model (`codex/gpt-5.6-sol`) an aggregate `Models[]` can't for a multi-model stage. **This is only the two gap sub-lists; it does NOT replace the report's `Rates used` (key/source/as-of) or reported-vs-estimated mismatch data** (round-4 #7) — those stay as their own record-walk / rate provenance in `report.go`.

## Chosen UI (from the brainstorm)

Three coordinated surfaces, theme-aware (graphite/goga/novacorps × light/dark), semantic colors only (mint = priced, amber = a coverage gap or unavailable, grey = unmetered; indigo stays the product accent):

1. **Header "Est. cost" tile** — a fifth `RunMetrics` metric. Shows `run_cost.display_cost`. It is **always a `<button>`** (destination = the permanent Cost tab). **Amber marker + tooltip whenever there is something to warn about — `coverage_issues.length > 0` OR `health=unavailable`** (round-4 #6: an all-unpriced healthy run — `coverage=none`, `—` — must NOT look like a no-data run; the amber marker + reason text is what distinguishes them). A plain grey `—` with no marker is reserved for true no-data. The tooltip summarizes `coverage_issues` (or, for hard-unavailable-with-no-issues, explains the storage failure — never "sum" an empty issue list). Tooltip is **hover/focus only** (the tile is itself a nav button — no nested tap-toggle); the touch-accessible explanation is the coverage line inside the permanent Cost tab the tap opens. The tile carries `aria-describedby` pointing at the reason text so a screen reader gets it without hover (round-4 #13).

2. **Rail per-stage cost** — a quiet, right-aligned mono figure in the row's trailing slot (`.stage-actions`), left of the kebab; no chip/background. **One helper decides it, with access to Stage AND accounting availability/presence (round-4 #3):**
   1. `stage.cost != nil` → **always** render `stage.cost.display_cost` (`$X` / `$X+` / `—`) — a running stage with a priced planning/revision record shows `$X`, never overridden by `…`.
   2. `stage.cost == nil` AND accounting is **supported and not hard-unavailable** (a live writer can still produce records) AND status ∈ `COST_PENDING_STATUSES` → `…`.
   3. otherwise → render nothing.
   `COST_PENDING_STATUSES` is an explicit set of statuses where an agent is actively producing output that will yield a record: **`planning`, `running`, `revising`** (autonomous execution runs *under* `running`, so it's covered — there is no `autonomous` status). **`retrying` is excluded** — it's passive backoff, no agent producing tokens right now; a `…` there would be a false promise. Rest-vs-hover uses **color, not opacity** (AA): token `--cost-rail` at rest (muted-but-AA-passing, verified per skin × mode) → `--cost-rail-strong` on row hover/active. `prefers-reduced-motion` respected.

3. **Cost tab** — a **permanent** workspace view (always selectable; not attention; never auto-opens). A live `afm report`: table `Stage · Model · Tokens · Cache R/W · Cost`. **Every value row — stage rows AND the `run overhead` row — is expandable** via a chevron **button** (round-4 #12: the overhead row has a breakdown too; expand state is keyed by a stable id set including the reserved `"__overhead__"` key, not just `Set<stageId>`). Expand → an inline detail: a **token-mix bar** (segment widths = each category ÷ `total_tokens`: cache-read / cache-write / output / uncached, labelled "Token mix" — a cost-mix bar is deferred; **`total_tokens == 0` → a neutral empty track, no percentages, no divide**) + a figure grid (cache read, cache write by TTL, output, uncached input, cache-hit ratio, phases×count). Below the rows: `Total` (`run_cost.display_cost`), then a coverage line from `coverage_issues` (`codex/gpt-5.6-sol — unpriced — add a rate in pricing:`).

Visual reference: the brainstorm artifacts (chosen direction; rail placement) — except the rail rest state uses a color token, not `opacity`.

## Architecture

```
usage.jsonl ─Load→ Ledger ─(presentation helpers)→ read model → CLI / /api/status → use-status → React
                     │  Coverage / DisplayCost / CoverageIssue
              SummaryByStage / RunSummary / Records
```

Cost is available in any mode (host too — `usage.jsonl` is always written), so **no capability gate**.

### Health-state matrix (health ⟂ coverage)

`health=ok, has_data=true` does **not** imply full cost — always render by the cost's own `coverage`.

| health | has_data | Meaning | UI |
|---|---|---|---|
| unsupported (old backend / accounting off) | — | field absent in JSON | tile `—` no marker, rail never `…`, tab "No usage data" |
| `ok` | `false` | run predates feature / no usage yet | tile `—`, rail nothing, tab "No usage data" |
| `ok` | `true` | ledger loaded, writer fine | render each cost by its own `coverage` (may be full/partial/none) |
| `unavailable` | `true` | writes failed after some records this session | last snapshot, rendered by its coverage, + amber "cost may be incomplete" banner (tab) + amber tile marker |
| `unavailable` | `false` | hard `Open` error / corrupt reload, no recoverable snapshot | tile amber `—`, tab "Cost unavailable this run" |

### Server read model (Task 7)

`server.Config` does **not** receive accounting today (it holds `Workspace`, `StageDependsOn`, …; the raw `accounting.Store` is wired only to `orchestrator.Options`). Closing that gap:

- **Provider interface** on `server.Config`, `Accounting CostProvider`. Because `*accounting.Store` already has `Snapshot() *Ledger`, the new method is **`CostSnapshot()`** (no Go overload) returning one struct under a single mutex — `{ stages map[string]*CostView; run, overhead *CostView; issues []CoverageIssue; health; hasData }` — the `Store` copies the ledger + health under its lock, and the presentation helpers compute `CostView`s from the immutable copy **outside** the lock (round-4 #2). Nil provider ⇒ accounting unsupported.
- **`cmd/afm/run.go` owns the initial health.** `accounting.Open` can return `acct==nil, acctErr!=nil`:
  - `acct != nil` → pass it; health tracks `Unavailable()` live.
  - `acct == nil && acctErr != nil` → pass a **static "unavailable" provider** (`health=unavailable, has_data=false`) so a hard failure is reported, distinct from a nil provider's "unsupported".
- **`StageView.Cost *CostView`** (`json:"cost,omitempty"`) — per-stage, nil when the stage has no record yet.
- **`statusResponse` gains** `run_cost`, `run_overhead_cost` (`*CostView`), `coverage_issues []CoverageIssue`, and `accounting {health, has_data}` (omitted entirely when unsupported). The server never fails `/api/status` for accounting reasons.
- **`CostView` DTO** (accounting-owned, JSON-tagged, snake_case): `display_cost`, `coverage`, `estimated_cost_usd`, invocation counts `metered`/`priced_invocations`/`unpriced`/`unmetered` (explicit numbers — the ambiguous `Priced` bool is NOT exposed; if a bool is kept internally it's `all_metered_priced`), `models`, token breakdown (`uncached_input`, `cache_read`, `cache_write_5m`, `cache_write_1h`, `cache_write_other`, `output`, `reasoning_output`, derived `total_tokens`, `cache_write_total`, `cache_hit_ratio`), and `phases` (phase→count). All from accounting methods; the server does no arithmetic.

### CLI consistency — `afm check` / `afm report` adopt the same helpers (Task, in scope)

Increment 1's CLI renders cost with `FormatUSD(sum.CostUSD, sum.Priced)` — a partial run shows `—`, and the presentation lives in a second place. Both move onto `DisplayCost`/`Coverage`/`CoverageIssue`:

- `afm check` `EST. COST`/`TOTAL` → `DisplayCost` (partial → `$X+` not `—`); its `coverageNote` from `CoverageIssue`.
- `afm report` per-stage/overhead/total → `DisplayCost`. Its `Coverage and pricing` section uses `CoverageIssue` **only for the two gap sub-lists (unmetered/unpriced) and KEEPS the existing `Rates used` (key/source/as-of) and reported-vs-estimated mismatch sub-sections** (round-4 #7 — don't impoverish the report).
- **`Load` error is no longer ignored (round-4 #7).** Today `afm check`/`report` swallow `accounting.Load` errors → a corrupt ledger reads as "No usage data". Instead: a corrupt/`ErrCorruptUsage` ledger prints an explicit "cost unavailable/incomplete this run" line (and, for `report`, keeps the rest of the report), with a defined non-zero exit code for `check` so scripts can tell; a *missing* `usage.jsonl` stays "No usage data" (exit 0). Add corrupt-ledger CLI tests + a golden preserving rate provenance/mismatch.
- **Result:** `afm check`, `afm report`, and `/api/status run_cost.display_cost` are byte-identical for the same run. Behavior change (intended): partial CLI output `—` → `$X+`; Increment-1 golden/CLI tests update. Core untouched (token math, pricing, `usage.jsonl`, event log).

### Frontend (Task 9)

`pkg/web/dashboard/src/`:

- **Types** (`types/cost.ts`): `CostSummary`/`TokenBreakdown`/`CoverageIssue`/`Coverage`/`AccountingState`. **Backward-compat is explicit (round-4 #4):** a `Status.accounting?: AccountingState` where **absent ⇒ `supported: false`** (an old backend sends no `accounting`/`coverage_issues`). Rules: unsupported → tile/tab behave as no-data AND the rail never shows an optimistic `…`; `coverage_issues` absent → `[]`; per-stage/run cost absent → `undefined` (not zeroed). `use-status` normalizes this; add a `normalizeStatus` test on a **fully old JSON shape** (no accounting at all), not just absent cost.
- **`RunMetrics`**: add the `Est. cost` tile — always a `<button>` → open the Cost tab; amber marker+`aria-describedby` when `coverageIssues.length > 0 || health==='unavailable'`; grey `—` (no marker) only when unsupported/no-data. Joins the narrow-header overflow popover; **activating Cost from the popover closes the popover first, then switches view** (round-4 #15) — add a small component test.
- **Header 5-metric squeeze (round-4 earlier / responsive):** `RunMetrics` was sized for four; define the inline→popover collapse order (Idle/Backoff first, then Cost, keeping Started/Elapsed longest) and re-tune the breakpoint. Explicit acceptance at 1280/1279px with a long flow name+description and all right-side controls.
- **Workspace** (`use-workspace-view` + `WorkspaceHeader`): add `'cost'` to `WorkspaceView` — permanent, user-selectable, never auto-opens. **The stage-scoped `WorkspaceHeader` is hidden when `view==='cost'` (round-4 #9)** — Cost is a global report; leaving the last stage's name/status above it would imply a nonexistent per-stage filter. App test: select a stage, open Cost, assert no stage context is shown.
- **Attention `returnView` — restricted to the two global views (round-3 #6):** history views are stage-scoped and an auto-open changes `selectedStageId`, so `returnView ∈ {'feed','cost'}` only (entering from a history view records `feed`). On the last attention resolving, restore `returnView`; a manual Feed/Cost click during attention replaces it; a second attention doesn't overwrite an already-set `returnView`. Reducer tests for each.
- **`CostPanel`** (`components/cost-panel/`): table + expandable rows (stage + overhead, keyed set incl. `"__overhead__"`) + token-mix bar (+zero-token empty track) + Total + coverage-from-issues. **Empty-state order is exact (round-4 #5):** (1) `health==='unavailable' && !hasData` → "Cost unavailable this run"; (2) `!hasData` → "No usage data"; (3) data present → snapshot, plus an incomplete banner when `health==='unavailable'`. **Its own vertical scroll (round-4 #10):** the panel body is `flex:1; min-height:0; overflow-y:auto` so expanded rows never push `Total`/coverage out of view (the dashboard forbids page scroll). Expand state is local (`Set<string>`).
  - **A11y (round-4 #13):** the expander is a chevron `<button>` in the Stage cell with `aria-expanded` + `aria-controls`→the detail region id; the detail region carries `role="region"` **inside a `<td colSpan=5>` (not on the `<tr>`, which would break table semantics)** and `aria-labelledby`→the stage/expander label. Enter/Space, visible focus.
- **Rail** (`StagesList.tsx`): render per the precedence helper (which also sees `accounting.supported`/health), colored by `--cost-rail`/`--cost-rail-strong`.

### CSS (round-4 #11 — real source + token)

- **Source is `pkg/web/dashboard/public/skins/`** (the `pkg/web/dashboard/skins/` tree is generated build output). Add `public/skins/base/cost-panel.css` (tokenized) and `@import` it from the **three supported public skins** (`graphite`, `goga`, `novacorps`); then **regenerate the embedded artifacts** so the new CSS lands in the binary (miss this and only the generated copy or nothing changes). Coffee is NOT in acceptance — config normalizes legacy `coffee` → graphite.
- **Tokens use the real names:** cache-read segment = **`--accent-primary`** (not `--accent`), cache-write = a new `--cost-cachewrite` (violet), output = `--success`, uncached = `--text-muted`. New `--cost-rail`/`--cost-rail-strong`/`--cost-cachewrite` defined per skin × light/dark where the other accents live, all AA-checked.
- **Sticky Stage/Cost columns (round-4 #14):** "anchored" needs `position: sticky` first/last cells (`left:0`/`right:0`) with an opaque semantic background + `z-index`, applied to header, body, and `Total` rows, coexisting with the full-width detail rows — not just `overflow-x:auto`. Responsive acceptance verifies Stage and Cost stay visible after horizontal scroll.
- External `skin_dir` (fully replaces the stylesheet): document the new component/token contract; no per-skin work required of it.

### Responsive, mobile & themes (hard requirements)

Verified on desktop wide, the 1280/1279 boundary, ~900px rail-breakpoint, and a ~360–390px phone, every supported skin × light/dark:

- Header tile → overflow popover at narrow widths (with label; opens the tab; closes the popover on activate); no run-name truncation at 1280/1279.
- Rail cost + kebab fit the mobile drawer (<900px) with no overflow; `id`/`name` ellipsize first.
- Cost tab: its own `overflow-x:auto` (sticky Stage/Cost) AND `overflow-y:auto`; detail grid reflows via `auto-fit`/`minmax`; bar full-width; coverage line wraps.
- Every new surface driven only by semantic tokens; mint/amber/grey + `--cost-rail` + `--cost-cachewrite` AA-checked per skin × mode.
- `prefers-reduced-motion: reduce` respected for color/expand transitions.

### Docs (Task 10)

`docs/accounting.md` (token semantics, formula, TTL, reported-vs-estimated, subscription caveat, `pricing:` overrides, `unmetered`/`unpriced`/`partial`, health vs coverage, where cost shows up), linked from `docs/config-reference.md` + `mkdocs.yml`; one README line.

## Error handling

- unsupported / `ok`+`!has_data` → "No usage data", plain `—`, never `$0.00`, rail never optimistic `…`.
- `unavailable`+`has_data` → keep the last snapshot rendered by its coverage + amber incomplete banner; never zero/blank known cost.
- `unavailable`+`!has_data` → "Cost unavailable this run" (distinct from no-data).
- coverage gaps always surfaced (amber marker when issues exist, coverage line in the tab), never hidden.
- Any accounting read error → `unavailable` (logged), never a `/api/status` failure or a run/FSM effect (consistent with Increment 1).

## Testing

- **Accounting**: `Coverage` from counts — **single unpriced → `none`** (not partial), priced+unpriced → `partial`, zero-rate full → `full`/`$0.0000`, zero-rate+unpriced → `partial`/`$0.0000+`; `DisplayCost` exact strings incl. tiny full `<$0.0001` and **tiny partial `<$0.0001+`**; `priced_invocations` correct; `CoverageIssue` names the right stage/phase/channel/model/reason for a multi-model stage.
- **Server**: the `CostProvider`/`CostSnapshot()` seam (single-mutex copy, views computed outside the lock); `StageView.cost` present/omitted; `run_cost`/`run_overhead_cost`/`coverage_issues`/`accounting{health,has_data}`; **health ⟂ coverage** — healthy store + unpriced record → `ok,true,coverage=none|partial` (a *single* unpriced → `none`); the four health×has_data payloads distinct; **`acct==nil && acctErr!=nil` → static `unavailable,has_data=false`** distinct from nil-provider unsupported (field absent); `200` always.
- **CLI**: `afm check`/`report` render `DisplayCost` (partial `$X+`); coverage note/section from `CoverageIssue` while **keeping Rates-used + mismatch subsections**; **corrupt ledger → "cost unavailable/incomplete" + defined exit code, not "No usage data"**; golden preserves rate provenance/mismatch.
- **Cross-surface consistency**: `afm check` total == `afm report` total == `/api/status run_cost.display_cost`, byte-identical, incl. partial, zero-rate, and tiny cases.
- **Frontend**: tile is a button, opens the tab, amber when `issues>0 || unavailable` (incl. all-unpriced `none` with data — NOT plain `—`), plain `—` only for unsupported/no-data, `aria-describedby` present; popover Cost closes popover then opens tab; Cost tab selectable with no data; **empty-state order** — `unavailable&&!hasData` → "Cost unavailable", `!hasData` → "No usage data", data+unavailable → snapshot+banner; `CostPanel` vertical-scrolls (long list + expanded rows keep Total visible), overhead row expands via `"__overhead__"`, chevron `<button>` toggles the region with `role="region"` inside a `<td>` + `aria-controls`/keyboard, token-mix widths sum to total AND `total_tokens===0` → empty track (no NaN); rail follows the precedence helper — **running WITH a priced record → `$X` (not `…`)**, cost-nil+live+supported → `…`, cost-nil+(pending|unsupported|hard-unavailable) → nothing, `retrying` → nothing; **`WorkspaceHeader` hidden in `view==='cost'`**; `returnView` cases (cost→attn→cost; history→attn→feed; manual Cost mid-attn wins; second attn no clobber); `normalizeStatus` on a **fully old JSON shape** → `supported:false`, `issues=[]`, no optimistic rail.
- **Live acceptance**: re-run Claude + GLM + Codex; tile == `afm report` == `afm check`; rail figures match; expand shows the breakdown; unpriced Codex → stage `—` / run `$X+` + amber + coverage line naming `codex/…`; **restart-stable, INCLUDING the degraded case** — successful prefix → forced accounting write failure (live shows `unavailable,true`) → reopen the same run: document that reopen shows `ok,true` (the incomplete warning is live-only, per the health-semantics limit above) and assert the totals/figures over the loaded prefix are stable.
- **Responsive/theme**: 1280/1279 header (5 metrics + long name + all controls, no overflow); rail cost+kebab in the ~375px drawer; Cost tab sticky Stage/Cost after horizontal scroll + own vertical scroll; every surface graphite/goga/novacorps × light/dark at AA; rail dim (by color) at rest, strong on hover.

## Out of scope

- Increment 1's **core** — token normalization/math, pricing resolution, `usage.jsonl` format, event log. In scope: the new accounting presentation helpers + JSON view, and pointing the CLI at them.
- `max_cost` budget guard; cost-mix bar / cost-over-time charts; editing `pricing:` from the UI; a durable cross-restart degradation marker (health stays "writer-operational" semantics).
