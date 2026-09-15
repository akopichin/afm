# Design: Cost UI — Increment 2 (dashboard + server read model)

**Date:** 2026-09-15
**Status:** design, revised after review (`tmp/cost-ui-review.md`)
**Depends on:** `docs/superpowers/plans/2026-09-14-stage-and-run-pricing.md` (Increment 1 — `pkg/accounting`, `usage.jsonl`, `afm check`/`afm report`; landed on branch `pricing`). This spec is Increment 2, Tasks 7/9/10 of that plan, refined into a concrete UI after a visual brainstorm.

## Goal

Surface the token/cost accounting that already lands in `usage.jsonl` inside the live web dashboard, so a run's cost is visible while it runs — per stage and in total — without dropping to the CLI. **No new accounting arithmetic**; but accounting DOES gain small **presentation helpers** (a display-cost/coverage result and a coverage-issue list) so the server and frontend never re-derive money or string-concatenate — they render finished values.

## Presentation model (the review's P1 core)

The naïve "format with `FormatUSD`, add `+` on the frontend" is wrong: a mixed run's `Summary.Priced` is `false`, so `FormatUSD` returns `—`, yielding `—+`. Instead **accounting owns the presentation result**. Add to `pkg/accounting` (small, tested there — the numbers already exist on `Summary`/`Records`):

- **`Coverage` enum**, computed from **invocation COUNTS only, never from the money value** (an explicit `0` rate is a valid priced result). Let `priced = Metered - Unpriced` (metered invocations that resolved to a rate), `gaps = Unpriced + Unmetered`:
  - `full` — `priced ≥ 1` AND `gaps == 0`;
  - `partial` — `priced ≥ 1` AND `gaps ≥ 1`;
  - `none` — `priced == 0` (everything unpriced/unmetered, or no invocations).
  A single explicit-zero-rate record ⇒ `full`, `$0.0000`; that record plus one unpriced ⇒ `partial`, `$0.0000+`. Never gate coverage on `CostUSD > 0`.
- **`DisplayCost` string**: `full` → `$0.3995` (or `$0.0000` for a genuine zero); `partial` → `$0.3995+` (the `+` = "at least this — some invocations unpriced/unmetered"; the number is the known priced sum in `Summary.CostUSD`, which is `$0.0000` for zero-rate); `none` → `—`. The `+` is produced HERE, never by string math in the UI.
- **`CoverageIssue`** (per problem invocation, extracted from `Records()` exactly as `report.go`'s `renderCoverage` already does): `{kind: "unmetered"|"unpriced", stage, phase, channel, model, reason}`. A `RunSummary`-level `[]CoverageIssue` feeds the tab's coverage line — so it can name the specific model (`codex/gpt-5.6-sol`), which the aggregate `Models[]` cannot for a multi-model stage.

The UI reads `display_cost`/`coverage`/`coverage_issues`; it never applies its own rounding, `+`, or `$0.00`.

## Chosen UI (from the brainstorm)

Three coordinated surfaces, all theme-aware (graphite/goga/novacorps × light/dark), semantic colors only (mint = priced, amber = partial/unpriced, grey = unmetered; indigo stays the product accent):

1. **Header "Est. cost" tile** — a fifth metric in `RunMetrics` (after Started/Elapsed/Idle/Backoff): the live run total, showing `run_cost.display_cost`. Mint when `coverage=full`; amber marker + tooltip (naming the gap from `coverage_issues`) when `partial`; `—` when `none`. It is a **shortcut** to the Cost tab (click → select it), NOT the only way in — the tab is always present (see below). At narrow header widths it joins the existing `⋯` overflow popover (still opens the tab from there).

2. **Rail per-stage cost** — a quiet, right-aligned monospace figure in each stage row's existing trailing slot (`.stage-actions`), left of the kebab. **No chip/background/border.** **One precedence rule drives it (review P1 #3) — `stage.cost` presence wins, status is only the fallback**:
   1. `stage.cost != nil` → **always** render `stage.cost.display_cost` (`$X` / `$X+` / `—`). A running stage with priced planning/revision invocations shows `$X`; a mixed stage shows `$X+`. Never overridden by `…`.
   2. `stage.cost == nil` AND status is live agent work (`planning`/`running`/`revising`/`retrying`/`autonomous`) → `…` (metered soon).
   3. `stage.cost == nil` otherwise (pending, script-only, or a pre-feature run) → render nothing.
   This is the single contract; the states table below is illustrative, not an override of it. **Rest vs hover uses COLOR, not opacity** (review P2/AA): token `--cost-rail` at rest (muted-but-AA-passing, verified per skin × mode) intensifies to `--cost-rail-strong` on row hover / active row. No `opacity` on text; transition respects `prefers-reduced-motion`. The real row is otherwise unchanged (status dot + `id` bold / `name` muted).

3. **Cost tab** — a **permanent** workspace tab (always selectable, independent of whether data exists; empty state renders "No usage data"). A live `afm report`: table `Stage · Model · Tokens · Cache R/W · Cost`. Each stage row is **expandable** via a chevron **button** in the Stage cell (see accessibility below) → an inline detail block: a **token-mix bar** (segment widths = each category ÷ `total_tokens`: cache-read / cache-write / output / uncached — labelled "Token mix", NOT "where the money goes"; a cost-mix bar is deferred, it needs per-category cost the DTO doesn't carry) plus a grid of exact figures (cache read, cache write by TTL, output, uncached input, cache-hit ratio, phases×count). **Zero-token guard (review #8):** when `total_tokens == 0` (an unmetered invocation, or a metered reply with no tokens) the bar renders a neutral empty track — no percentages, no division — and the figure grid shows `0`; never a divide-by-zero or a full-width mystery segment. Below the stage rows: a `run overhead` row (`scope=run_overhead`), a `Total` row (`run_cost.display_cost`), and a coverage line built from `coverage_issues` (`codex/gpt-5.6-sol — unpriced — add a rate in pricing:`).

Visual reference: the brainstorm artifacts (chosen direction; rail placement) are the source of truth for spacing/tone — except the rail's rest state now uses a color token, not `opacity`.

## Architecture

Data exists after Increment 1: `accounting.Load(runDir) *Ledger` → `SummaryByStage()`, `RunSummary()`, `Records()`. Cost is **always available in any mode** (host too — `usage.jsonl` is written on every run), so **no capability gate**. The frontend renders empty/`—`/"No usage data" for a run that predates the feature.

```
usage.jsonl ─accounting.Load→ Ledger ─(presentation helpers)→ server read model → /api/status JSON → use-status → React
                                 │  DisplayCost / Coverage / CoverageIssue
                        SummaryByStage / RunSummary / Records
```

### Accounting health vs coverage — two orthogonal axes (review P1 #1)

Do NOT conflate them:

- **`health` = "can I trust the ledger is complete up to now?"** — `ok` (every usage record that should exist does) vs `unavailable` (writing stopped or the ledger couldn't be opened/parsed, so records may be missing).
- **`coverage` = "can I price the invocations I DO have?"** — `full`/`partial`/`none`, per invocation counts (above). A perfectly healthy store routinely has `partial`/`none` coverage (an unpriced Codex model is not a store failure).

So `health=ok, has_data=true` does **not** imply full cost — the UI always renders per `run_cost.coverage` / `stage.cost.coverage`, whatever the health.

| Health | has_data | Meaning | UI |
|---|---|---|---|
| `ok` | `false` | run predates the feature / no usage yet | tile `—`, rail nothing, tab "No usage data" |
| `ok` | `true` | ledger complete | render each cost by its own `coverage` (may be full/partial/none) |
| `unavailable` | `true` | writing failed *after* some records landed | **keep the last valid snapshot**, render by its coverage + an amber "cost may be incomplete — accounting stopped" banner (tab) and amber marker (tile) |
| `unavailable` | `false` | no recoverable snapshot (hard `Open` error / corrupt reload before any record) | tile amber `—`, tab "cost unavailable this run" |

### Server seam for accounting (review P1 #2 — not wired today)

`server.Config` does **not** currently receive accounting (it holds `Workspace`, `StageDependsOn`, … — the raw `accounting.Store` is wired only to `orchestrator.Options`). This is a real gap the plan must close:

- Add `Config.Accounting AccountingSnapshotProvider` — a small read-only interface the server owns: `Snapshot() (stages map[string]*CostView, run *CostView, overhead *CostView, issues []CoverageIssue)` + `Health() (health, hasData)`. `pkg/accounting`'s live `Store` implements it (reading its in-memory ledger under its mutex); a nil provider ⇒ accounting simply off (host runs without the feature, old servers).
- **`cmd/afm/run.go` owns the initial health.** `accounting.Open` can return `acct==nil, acctErr!=nil` (hard open failure). The wiring must map:
  - `acct!=nil` → pass it as the provider; health tracks its `Unavailable()` live.
  - `acct==nil && acctErr!=nil` → pass a **static "unavailable" provider** (`health=unavailable, has_data=false`) so the server reports the hard failure instead of the nil being indistinguishable from "feature off / old run".
- **"Last snapshot preserved" is precise:** a live `Store` that fails on append/fsync keeps the prefix it already applied to its in-memory ledger (`has_data=true`). A hard `Open` error or a corrupt reload has **no** recoverable snapshot (`has_data=false`). The read model reflects exactly which case it is.
- `afm report`/reload (non-live) paths derive `health`/`has_data` from whether `usage.jsonl` loaded and parsed (missing → `ok,false`; `ErrCorruptUsage` → `unavailable,false`; loaded → `ok,true`).

### Server read model (Task 7)

The live server holds the run's `accounting.Store` (Increment 1 wired `orchestrator.Options.Accounting`); `buildStageViews`/`handleStatus` gain read access. `state.LoadRunState` is NOT changed.

- **`accounting` presentation view** (owned by `pkg/accounting`, JSON-tagged; consumed by `pkg/server`): a `CostView` carrying `display_cost` (string), `coverage` (`full|partial|none`), `estimated_cost_usd` (number, for tooltips/tests), invocation counts `metered`/`priced_invocations`/`unpriced`/`unmetered` (an explicit number set, not a bare bool — the review's P2 #5; `priced_invocations = metered - unpriced`), `models` ([]string), and the token breakdown `uncached_input`, `cache_read`, `cache_write_5m`, `cache_write_1h`, `cache_write_other`, `output`, `reasoning_output`, plus derived `total_tokens`, `cache_write_total`, `cache_hit_ratio`, and `phases` (phase→invocation-count, for the expand detail). The old `Summary.Priced` bool ("≥1 metered AND all metered priced", ignoring unmetered) is NOT exposed in the DTO — `coverage` supersedes it; if a boolean is still convenient internally, name it `all_metered_priced` so it isn't misread as "fully covered". All numbers/strings come from accounting methods — the server does no arithmetic.
- **`StageView.Cost *CostView`** (`json:"cost,omitempty"`) — per-stage, nil when the stage has no usage record yet. From `SummaryByStage()[id]`.
- **`statusResponse` gains:** `run_cost *CostView`, `run_overhead_cost *CostView`, `coverage_issues []CoverageIssue`, and `accounting {health, has_data}`. `run_cost` drives the tile; `run_overhead_cost` the tab's overhead row; `coverage_issues` the coverage line.
- The server never fails `/api/status` because of accounting; a read error → `health=unavailable`, last snapshot preserved where possible.

### Frontend (Task 9)

`pkg/web/dashboard/src/`:

- **Types** (`types/cost.ts`): `CostSummary`/`TokenBreakdown`/`CoverageIssue`/`Coverage`/`AccountingHealth` mirroring the DTO; `Stage.cost?: CostSummary`; `Status.runCost?`, `Status.runOverheadCost?`, `Status.coverageIssues`, `Status.accounting`. `use-status` maps snake→camel; **absent cost → `undefined`** (not zeroed) so no-data is distinguishable from `$0`.
- **`RunMetrics`**: add the `Est. cost` tile — always a `<button>` (it always has a destination: the permanent Cost tab), showing `runCost?.displayCost ?? '—'`; amber marker when `coverage==='partial'` or `accounting.health==='unavailable'`, with a tooltip summarizing `coverageIssues`. Joins the narrow-header overflow popover.
- **Workspace tab** (`use-workspace-view` reducer + `WorkspaceHeader`): add `'cost'` to `WorkspaceView` as a **permanent, user-selectable** view (like Feed — not attention, never auto-opens). New component `components/cost-panel/CostPanel.tsx`.
- **Attention interaction — `returnView` restricted to the two global views (review #6).** History views (`plan-history`/`dialog-history`) are stage-scoped: an attention auto-open changes `selectedStageId`, so "restore plan-history" would show the *wrong* stage's history. Therefore `returnView ∈ {'feed','cost'}` only. Contract:
  - Entering attention from `feed` or `cost` records that as `returnView`; from a history view records `feed` (preserving today's history→Feed behavior).
  - On the last attention resolving, restore `returnView`.
  - A **manual** Feed/Cost click *during* attention replaces `returnView` with that choice; subsequent attention arrivals do **not** overwrite an already-set `returnView`.
  - Add reducer tests for: cost→attention→resolve→cost; history→attention→resolve→feed; manual Cost click mid-attention → later resolve lands on cost; a second attention mid-first doesn't clobber `returnView`.
- **`CostPanel`**: table + expandable rows + token-mix bar + overhead/total/coverage, from `Status.stages[].cost` / `runCost` / `runOverheadCost` / `coverageIssues`. Empty state ("No usage data") when `!accounting.hasData`; incomplete banner when `health==='unavailable'`. Row expand is local state (`Set<stageId>`).
  - **Accessibility (review #7):** the expander is a chevron `<button>` inside the Stage cell with `aria-expanded` + `aria-controls` (pointing at the detail row's id), operable by Enter/Space, with a visible focus ring; the detail row is the announced region.
  - **Tooltip vs the tile-button conflict (review #7).** The header tile is itself a navigation `<button>` (tap → opens the Cost tab) — so its amber-gap marker's tooltip is **hover/focus only**, NOT tap-toggled (you can't nest an interactive info-toggle in a button, and a tap already navigates). Touch users get the full, always-accessible explanation as the **coverage line inside the permanent Cost tab** the tap takes them to — that is the canonical, non-hover surface for "why the `+`". The tab's own coverage line and any `unpriced`/`unmetered` pills are plain text (no hover dependency).
- **Rail** (`StagesList.tsx`): render `stage.cost?.displayCost` as a quiet mono span in `.stage-actions` before the kebab, colored by `--cost-rail`/`--cost-rail-strong` (color, not opacity). New tokenized CSS.
- **CSS**: new `skins/base/cost-panel.css` (tokenized), `@import`ed by every skin. New tokens: `--cost-rail`, `--cost-rail-strong`, and a violet `--cost-cachewrite` for the bar's cache-write segment — defined per skin × light/dark where the other accents live, all AA-checked. Bar segments: cache-read `--accent`, cache-write `--cost-cachewrite`, output `--success`, uncached `--text-muted`. Figures `font-variant-numeric: tabular-nums`.

### Responsive, mobile & themes (hard requirements)

Acceptance criteria, verified at implementation on real viewports (desktop wide, ~900px rail-breakpoint, small phone ~360–390px), every skin × light/dark:

- **Header tile at narrow widths & the 5-metric squeeze (review #9).** `RunMetrics` today shows all metrics inline from ~1280px up, sized for **four**; Cost makes **five**, competing with a long flow name/description and the right-side controls (Files/theme/notifications/connection). The collapse scheme must be re-tuned for five, not assume the old threshold holds: define the inline→popover collapse order (e.g. Idle/Backoff collapse first, then Cost, keeping Started/Elapsed longest) and raise the breakpoint if needed so nothing overflows or truncates the run name at the boundary. Cost stays reachable (and still opens the Cost tab) from the `⋯` popover with its label. **Acceptance includes an explicit 1280/1279px case** with a long flow name + description and all right-side controls present — the exact boundary the old two-point "wide vs narrow" check misses.
- **Rail on mobile** (drawer <900px): cost figure + kebab fit the drawer width; `id`/`name` ellipsize first; the trailing slot never overflows.
- **Cost tab on small screens**: table in an `overflow-x: auto` container (its own scroll, no page-level horizontal scroll); `Stage` and `Cost` anchored, middle columns scroll; the expand detail grid reflows via `auto-fit`/`minmax`, bar stays full-width; coverage line wraps.
- **Themes**: every new surface driven only by semantic tokens; mint/amber/grey keep AA on each ground; the rail rest color and violet cache-write segment are AA-checked per skin.
- **Motion**: opacity/expand/color transitions respect `prefers-reduced-motion: reduce`.

### CLI consistency — `afm check` / `afm report` adopt the same helper

Increment 1's CLI renders cost with `FormatUSD(sum.CostUSD, sum.Priced)` (`cmd/afm/check.go`, `pkg/accounting/report.go`) plus its own coverage note/section. That means a **partial run shows `—`** (because `Priced=false`), and — more importantly — cost presentation would now live in **two places** (CLI vs the new dashboard helper), guaranteed to diverge on the next edit. Since Increment 2 introduces the shared `DisplayCost`/`Coverage`/`CoverageIssue` presentation helpers in `pkg/accounting`, the CLI **moves onto them too**:

- `afm check`'s `EST. COST` column and `TOTAL` render `DisplayCost` (partial → `$X+` instead of `—`); its `coverageNote` is rebuilt from `CoverageIssue` (or kept if already equivalent).
- `afm report`'s per-stage/overhead/total cost render `DisplayCost`; its `Coverage and pricing` section is built from `CoverageIssue` (it already walks records — this just shares the extracted type instead of re-deriving).
- **Result: `afm check`, `afm report`, and the dashboard produce identical cost figures by construction** — one formatter, one coverage source.
- **Behavior change (intended, not a regression):** partial CLI output changes `—` → `$X+`; the Increment-1 golden/CLI tests update to match. This is a presentation-only refactor — token math, pricing, `usage.jsonl`, and the event log are untouched.

### Docs (Task 10)

- `docs/accounting.md`: token semantics per schema, cost formula, cache TTL, reported-vs-estimated, subscription caveat, `pricing:` override syntax, `unmetered`/`unpriced`/`partial`, and where cost shows up (CLI + dashboard). Linked from `docs/config-reference.md` and `mkdocs.yml` nav.
- README: one line + link.

## Data-flow / states

The **Rail** column follows the precedence rule in "Chosen UI" #2 (cost presence wins; status is only the fallback), not the row label.

| State | Header tile | Rail | Cost tab |
|---|---|---|---|
| stage, all priced | contributes (mint) | `$0.2498` | row; expands to breakdown |
| stage, mixed (some priced, some not) | total → `$X+` amber | `$X+` | row, `unpriced`/`unmetered` note, named in coverage line |
| stage, nothing priced (unpriced model / unmetered) | total → `$X+` amber | `—` | row, `—`, named in coverage line |
| stage running, no record yet | total so far | `…` | row empty until first record lands |
| stage running, already has priced record(s) | total so far | `$X`/`$X+` | row fills in live |
| run predates feature (`ok`,`has_data=false`) | `—` | nothing | "No usage data" |
| store failed mid-run (`unavailable`,`has_data=true`) | last known + amber | last known | last snapshot + "cost may be incomplete" banner |
| hard open failure (`unavailable`,`has_data=false`) | amber `—` | nothing | "cost unavailable this run" |

Live updates ride the existing `/api/status` poll — each stage's rail figure and tab row fill in as its record lands; the header total climbs.

## Error handling

- `health=ok`+`has_data=false` → "No usage data"; never `$0.00`.
- `health=unavailable` → keep the last valid snapshot, mark it incomplete (amber), never silently zero or blank already-known cost.
- Partial coverage is always surfaced (`+` on the tile, coverage line in the tab), never hidden.
- The server treats any accounting read error as `unavailable` (logged), never a `/api/status` failure — consistent with Increment 1's "usage failure never affects the run".

## Testing

- **Accounting** (`pkg/accounting`): `Coverage`/`DisplayCost` computed from counts — `full`→`$X`, `partial`→`$X+` (known priced sum, never `—+`), `none`→`—`; **explicit-zero-rate cases**: one zero-rate record → `full`,`$0.0000`; zero-rate + unpriced → `partial`,`$0.0000+` (coverage never gated on `CostUSD>0`); `priced_invocations = metered - unpriced` is exposed and correct; `CoverageIssue` extraction names the right stage/phase/channel/model/reason for a multi-model stage (reuse `report.go`'s record walk).
- **Server** (`pkg/server`): the new `Config.Accounting` provider seam; `StageView.cost` present with a record / omitted without; `run_cost`/`run_overhead_cost`/`coverage_issues`/`accounting{health,has_data}` in `/api/status`; **health ≠ coverage** — a healthy store with an unpriced record yields `health=ok, has_data=true, coverage=partial` (not "full"); the four health×has_data states produce distinct payloads; **`acct==nil && acctErr!=nil` → `unavailable, has_data=false`** (static provider), distinct from nil-provider "feature off"; `unavailable`+`has_data=true` still carries the last snapshot; `200` in every case. JSON-shape test (snake_case, `display_cost`/`priced_invocations` present).
- **Frontend** (Vitest): tile shows `displayCost`, is a button, opens the Cost tab, amber on `coverage==='partial'` OR `health==='unavailable'`, `—` on `none`; **Cost tab selectable with no data → "No usage data"**; `CostPanel` renders rows/overhead/total/coverage-from-issues, chevron button toggles the detail with `aria-expanded`/keyboard, token-mix bar widths sum to total AND **the `total_tokens===0` row renders the empty track with no NaN/percentages**; the tile tooltip opens on hover/focus (not tap-toggled) and the tab coverage line is the touch explanation; `StagesList` rail follows the precedence rule — **a running stage WITH a priced record shows `$X` (not `…`)**, cost-nil+live→`…`, cost-nil+pending→nothing — using the color token; **attention over Cost returns to Cost; over history returns to Feed; manual Cost click mid-attention wins; a second attention doesn't clobber `returnView`**; `use-status` maps absent cost → undefined (old-backend safe); `unavailable`+snapshot keeps showing the last known total.
- **CLI** (`cmd/afm`, `pkg/accounting`): `afm check`/`afm report` render `DisplayCost` — a partial run now shows `$X+` (was `—`); coverage note/section built from `CoverageIssue`; Increment-1 golden/CLI tests updated for the `—`→`$X+` change.
- **Cross-surface consistency** (the whole point of the shared helper): a table/golden test asserts `afm check` total, `afm report` total, and the `/api/status` `run_cost.display_cost` are **byte-identical** for the same run (same formatter, same coverage source) — including the partial and zero-rate cases.
- **Live acceptance** (pricing spec's "Критерий готовности"): re-run Claude + GLM + Codex; dashboard tile total == `afm report` total == `afm check` total; per-stage rail figures match; expand shows the right breakdown; an unpriced Codex stage → `—` (stage) / `$X+` (run) + a coverage line naming `codex/…`; restart-stable.
- **Responsive/theme live-check** (browser, real run): tile → overflow popover on a narrow header; rail cost + kebab inside the mobile drawer at ~375px with no overflow; Cost tab table scrolls in its own container (no page-level horizontal scroll), detail grid reflows; every surface graphite/goga/novacorps × light/dark at AA; rail cost dim (by color) at rest, strong on hover.

## Out of scope

- `max_cost` budget guard (separate FSM change).
- Cost-mix bar / cost-over-time charts (token-mix bar only; cost-mix needs per-category cost from records — a later accounting addition).
- Editing `pricing:` from the UI (config stays file-driven).
- Any change to Increment 1's **core** — token normalization/math, pricing resolution, the `usage.jsonl` format, or the event log. In scope: the new accounting **presentation helpers** (`Coverage`/`DisplayCost`/`CoverageIssue`) + JSON-tagged view, and pointing the existing CLI (`afm check`/`afm report`) at those same helpers for consistency (a presentation-only refactor — see "CLI consistency" above).
