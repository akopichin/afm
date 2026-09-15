# Design: Cost UI — Increment 2 (dashboard + server read model)

**Date:** 2026-09-15
**Status:** design, pending review
**Depends on:** `docs/superpowers/plans/2026-09-14-stage-and-run-pricing.md` (Increment 1 — `pkg/accounting`, `usage.jsonl`, `afm check`/`afm report`; landed on branch `pricing`). This spec is Increment 2, Tasks 7/9/10 of that plan, refined into a concrete UI after a visual brainstorm.

## Goal

Surface the token/cost accounting that already lands in `usage.jsonl` inside the live web dashboard, so a run's cost is visible while it runs — per stage and in total — without dropping to the CLI. No new accounting math: the frontend and server only read `pkg/accounting` summaries and render them.

## Chosen UI (from the brainstorm)

Three coordinated surfaces, all theme-aware (graphite/goga/novacorps × light/dark), semantic colors only (mint = priced, amber = unpriced/partial, grey = unmetered; indigo stays the product accent):

1. **Header "Est. cost" tile** — a fifth metric in `RunMetrics` (after Started/Elapsed/Idle/Backoff): the live run total. Mint value. **Clickable** → selects the Cost tab in the workspace. On partial coverage (any unpriced/unmetered invocation) it renders the total with a trailing `+` and a small amber marker (e.g. `$0.3995+`), with a tooltip naming the gap (`1 unpriced`). Never `$0.00` for unknown — an all-unpriced run shows `—`.

2. **Rail per-stage cost** — a quiet, right-aligned grey monospace figure in each stage row's existing trailing slot (`.stage-actions`), sitting left of the kebab. **No chip/background/border** — plain secondary text so it never competes with the stage `id` or the kebab. **Semi-transparent at rest (`opacity ~0.5`), full opacity on row hover** (and on the selected/active row); transition respects `prefers-reduced-motion`. The real row is unchanged otherwise (status dot + `id` bold / `name` muted two-line label). States: priced → `$0.2498`; running/not-yet-metered → `…`; unpriced/unmetered → `—`.

3. **Cost tab** — a new workspace tab (a live `afm report`). A table with columns `Stage · Model · Tokens · Cache R/W · Cost`. Each stage row is **expandable** (click → an inline detail block: a horizontal bar splitting input into cache-read / cache-write / output / uncached, plus a grid of the exact figures — cache read, cache write by TTL, output, uncached input, cache-hit ratio, phases×count). Below the stage rows: a `run overhead` row (memory pipeline, `scope=run_overhead`), a `Total` row, and a coverage line (`1 unpriced — codex/gpt-5.6-sol — add a rate in pricing:`).

Visual reference (mockups, graphite): the brainstorm artifacts — chosen direction and rail placement — are the source of truth for spacing/tone.

## Architecture

Data already exists after Increment 1: `accounting.Load(runDir) *Ledger` → `SummaryByStage() map[string]Summary`, `RunSummary() Summary`, and (for the tab detail + overhead) `Ledger.Records() []UsageRecord`. **No capability gate** — unlike the Docker file browser, accounting works in host mode too (`usage.jsonl` is written on every run), so cost is always available when a run has usage; the frontend simply renders empty/`—` when a run predates the feature.

Flow of data:

```
usage.jsonl ──accounting.Load──▶ Ledger ──(server read model)──▶ /api/status JSON ──use-status──▶ React
                                   │
                        SummaryByStage / RunSummary / Records
```

### Server read model (Task 7)

The live server already holds the run's `accounting.Store` (Increment 1 wired `orchestrator.Options.Accounting`); `buildStageViews`/`handleStatus` gain read access to its snapshot. `state.LoadRunState` is NOT changed — the CLI path (`afm check`) already loads accounting independently.

- **`accounting.Summary` gets JSON tags + a serialized view.** Today `Summary` has no json tags (would serialize as `Tokens`/`CostUSD`/…). Add a small serialization DTO owned by `pkg/server` (or json tags + helper methods on the accounting side) that carries what the UI needs, computed in `pkg/accounting` (no arithmetic in the server):
  - `estimated_cost_usd` (number, raw) and `estimated_cost` (string, from `FormatUSD` — the single formatter),
  - `priced` (bool), coverage counts `metered`/`unmetered`/`unpriced`,
  - `models` ([]string),
  - token breakdown: `uncached_input`, `cache_read`, `cache_write_5m`, `cache_write_1h`, `cache_write_other`, `output`, `reasoning_output`, plus derived `total_tokens`, `cache_write_total`, `cache_hit_ratio` (all from `accounting` methods),
  - `phases` (map or list of phase→invocation-count, for the expand detail) — derived in accounting from `Records()`.
- **`StageView` gains `Cost *CostView`** (`json:"cost,omitempty"`) — the per-stage summary (nil when the stage has no usage record yet). Populated in `buildStageViews` from `SummaryByStage()[id]`.
- **`statusResponse` gains `run_cost` and `run_overhead_cost`** (both `*CostView`) — from `RunSummary()` and the run-overhead-scoped subset. `run_cost` is the header tile's source; `run_overhead_cost` is the tab's overhead row.
- Accounting-unavailable (store marked `unavailable`, or a run with no `usage.jsonl`): all cost fields are nil/omitted; a small `accounting_ok bool` (or reuse nil) lets the UI distinguish "no data" from "$0". The server never fails `/api/status` because of accounting.

### Frontend (Task 9)

`pkg/web/dashboard/src/`:

- **Types** (`types/stage.ts` + a new `types/cost.ts`): `CostSummary` / `TokenBreakdown` mirroring the server DTO; `Stage.cost?: CostSummary`; `Status.runCost?`, `Status.runOverheadCost?`. `use-status/use-status.ts` maps the snake_case API → camelCase, mapping an absent cost to `undefined` (not a zeroed object) so "no data" is distinguishable.
- **`RunMetrics`** (`components/run-metrics/`): add the `Est. cost` metric (fifth tile). It's a `<button>` when cost data exists (click → `onOpenCostTab`), a plain metric otherwise. Value via a shared `formatCost(summary)` helper that trusts the server's `estimated_cost` string and appends `+`/amber marker on partial coverage. Participates in the existing narrow-header overflow popover.
- **Workspace tab** (`use-workspace-view` reducer + `WorkspaceHeader`): add a `cost` view/tab alongside `feed`/`attention`/history. The header tile and (optionally) a tab entry select it. New component `components/cost-panel/CostPanel.tsx` renders the table + expandable rows + bar + overhead/total/coverage, reading `Status.stages[].cost`, `runCost`, `runOverheadCost`. Row expand is local component state (a `Set<stageId>`); the bar is plain React nodes (no chart lib), styled via tokens.
- **Rail** (`components/stages-list/StagesList.tsx`): render `stage.cost` as a quiet grey mono span inside `.stage-actions`, before the kebab. New CSS in `skins/base/stages-list.css` (tokenized). Grid stays valid (the trailing slot already holds badges+kebab).
- **CSS**: new `skins/base/cost-panel.css` (tokenized — `--success`/`--attention`/`--text-muted`/`--surface-*`/`--radius-*`), `@import`ed by every skin like the other base sheets. Bar segment colors: cache-read = `--accent`, cache-write = a violet token, output = `--success`, uncached = `--text-muted`. All figures `font-variant-numeric: tabular-nums`.

### Responsive, mobile & themes (hard requirements)

These are acceptance criteria, verified at implementation on real viewports (desktop wide, ~900px rail-breakpoint, and a small phone ~360–390px), in every skin × light/dark:

- **Header tile at narrow widths.** `RunMetrics` already collapses secondary metrics into a `⋯` overflow popover below ~1000/1280px. The `Est. cost` tile joins that mechanism — on a small header it lives in the popover (with its label), never truncating the run name or wrapping the header. The tile stays reachable (and still opens the Cost tab) from the popover.
- **Rail on mobile.** Below 900px the rail is a slide-over drawer. The per-stage cost figure must fit the drawer width without pushing the kebab off-screen or wrapping the row; if space is tight the cost stays on its row (grey mono) and the `id`/`name` ellipsize first (they already do). Verify the trailing slot (cost + kebab) never overflows the drawer.
- **Cost tab on small screens.** The table must not force a horizontal page scroll: wrap it in an `overflow-x: auto` container (its own scroll), keep `Stage` and `Cost` as the anchored/most-important columns, and let the middle columns scroll. The expandable detail block (bar + figure grid) reflows to fewer columns on narrow widths (grid `auto-fit`/`minmax`), the bar stays full-width. Coverage line wraps, not truncates.
- **Themes.** Every new surface (tile, rail figure, tab table, expand bar/detail, coverage line) is driven only by semantic tokens and verified in graphite/goga/novacorps × light/dark — no hardcoded colors. Semantic mint/amber/grey must keep AA contrast on each ground; the bar's violet cache-write segment needs a token defined per skin (add it where the other accents live).
- **Motion.** Opacity/expand transitions respect `prefers-reduced-motion: reduce`.

### Docs (Task 10)

- `docs/accounting.md`: token semantics per schema, the cost formula, cache TTL, reported-vs-estimated, subscription caveat, `pricing:` override syntax, `unmetered`/`unpriced`, and where cost shows up (CLI `check`/`report` + dashboard). Linked from `docs/config-reference.md` and the docs nav (`mkdocs.yml`).
- README: one line + link.

## Data flow / states

| State | Header tile | Rail | Cost tab |
|---|---|---|---|
| priced stage | contributes to total (mint) | `$0.2498` grey | row with cost; expands to breakdown |
| unpriced (known model, no rate) | total gets `+`/amber | `—` | row, `unpriced` pill, `—` cost, coverage line |
| unmetered (no usage captured) | `+`/amber | `—` | row, `unmetered` note |
| stage still running | total = sum so far | `…` | row shows partial/empty until its record lands |
| run predates feature / accounting unavailable | tile shows `—` (or hidden) | no cost | tab shows "No usage data" |

Live updates: the existing `/api/status` poll refreshes cost the same way it refreshes status — as each stage's usage record lands, its rail figure and tab row fill in and the header total climbs.

## Error handling

- Accounting store `unavailable` or `usage.jsonl` absent → cost fields omitted; UI shows "No usage data" in the tab, `—`/nothing in rail, `—` in tile. Never a fabricated `$0.00`.
- The server treats any accounting read error as "no data" (logged), never a `/api/status` failure — consistent with Increment 1's "usage failure never affects the run".
- Partial coverage is surfaced, never hidden: `+` on the tile, coverage line in the tab.

## Testing

- **Server** (`pkg/server`): `buildStageViews` includes `cost` when a usage record exists and omits it otherwise; `handleStatus` returns `run_cost`/`run_overhead_cost`; accounting-unavailable path yields nil cost + `200`. Golden/JSON-shape test for the `CostView` DTO (snake_case, `estimated_cost` string present).
- **Frontend** (Vitest): `RunMetrics` shows the tile, is a button when data exists, fires `onOpenCostTab`, renders `+`/amber on partial coverage, `—` on all-unpriced; `CostPanel` renders rows/overhead/total/coverage, expands a row into the breakdown+bar, handles the no-usage empty state; `StagesList` renders the quiet rail cost and the `…`/`—` states; `use-status` maps absent cost → undefined (backward-compatible with an old backend).
- **Live acceptance** (from the pricing spec's "Критерий готовности"): re-run Claude + GLM + Codex; assert the dashboard tile total == `afm report` total, per-stage rail figures match, expand shows the right breakdown, an unpriced Codex stage shows `—` + coverage, and the numbers are restart-stable.
- **Responsive/theme live-check** (browser, real run): verify the header tile → overflow popover on a narrow header; the rail cost + kebab inside the mobile drawer at ~375px with no overflow; the Cost tab table scrolls inside its own container (no page-level horizontal scroll) and the detail grid reflows; and every surface in graphite/goga/novacorps × light/dark with AA contrast. Rail cost is dim at rest, full on hover.

## Out of scope

- `max_cost` budget guard (separate FSM change).
- Historical time-series / cost-over-time charts.
- Editing `pricing:` from the UI (config stays file-driven).
- Any change to Increment 1 (`pkg/accounting`, `usage.jsonl`, CLI) beyond adding JSON tags / a serialization view and small derived helpers used by the read model.
