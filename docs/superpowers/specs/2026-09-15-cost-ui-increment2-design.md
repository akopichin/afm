# Design: Cost UI — Increment 2 (dashboard + server read model)

**Date:** 2026-09-15
**Status:** design, revised after review (`tmp/cost-ui-review.md`)
**Depends on:** `docs/superpowers/plans/2026-09-14-stage-and-run-pricing.md` (Increment 1 — `pkg/accounting`, `usage.jsonl`, `afm check`/`afm report`; landed on branch `pricing`). This spec is Increment 2, Tasks 7/9/10 of that plan, refined into a concrete UI after a visual brainstorm.

## Goal

Surface the token/cost accounting that already lands in `usage.jsonl` inside the live web dashboard, so a run's cost is visible while it runs — per stage and in total — without dropping to the CLI. **No new accounting arithmetic**; but accounting DOES gain small **presentation helpers** (a display-cost/coverage result and a coverage-issue list) so the server and frontend never re-derive money or string-concatenate — they render finished values.

## Presentation model (the review's P1 core)

The naïve "format with `FormatUSD`, add `+` on the frontend" is wrong: a mixed run's `Summary.Priced` is `false`, so `FormatUSD` returns `—`, yielding `—+`. Instead **accounting owns the presentation result**. Add to `pkg/accounting` (small, tested there — the numbers already exist on `Summary`/`Records`):

- **`Coverage` enum**: `full` (≥1 metered, all metered priced), `partial` (some priced, some unpriced/unmetered), `none` (nothing priced). Derived from the group's `Metered`/`Unpriced`/`Unmetered` + whether `CostUSD` came from ≥1 priced record.
- **`DisplayCost` string**: `full` → `$0.3995`; `partial` → `$0.3995+` (the `+` = "at least this — some invocations unpriced/unmetered"; the number is the known priced sum, which `Summary.CostUSD` already holds); `none` → `—`. The `+` is produced HERE, never by string math in the UI.
- **`CoverageIssue`** (per problem invocation, extracted from `Records()` exactly as `report.go`'s `renderCoverage` already does): `{kind: "unmetered"|"unpriced", stage, phase, channel, model, reason}`. A `RunSummary`-level `[]CoverageIssue` feeds the tab's coverage line — so it can name the specific model (`codex/gpt-5.6-sol`), which the aggregate `Models[]` cannot for a multi-model stage.

The UI reads `display_cost`/`coverage`/`coverage_issues`; it never applies its own rounding, `+`, or `$0.00`.

## Chosen UI (from the brainstorm)

Three coordinated surfaces, all theme-aware (graphite/goga/novacorps × light/dark), semantic colors only (mint = priced, amber = partial/unpriced, grey = unmetered; indigo stays the product accent):

1. **Header "Est. cost" tile** — a fifth metric in `RunMetrics` (after Started/Elapsed/Idle/Backoff): the live run total, showing `run_cost.display_cost`. Mint when `coverage=full`; amber marker + tooltip (naming the gap from `coverage_issues`) when `partial`; `—` when `none`. It is a **shortcut** to the Cost tab (click → select it), NOT the only way in — the tab is always present (see below). At narrow header widths it joins the existing `⋯` overflow popover (still opens the tab from there).

2. **Rail per-stage cost** — a quiet, right-aligned monospace figure in each stage row's existing trailing slot (`.stage-actions`), left of the kebab. **No chip/background/border.** Shows `stage.cost.display_cost`; states: priced → `$0.2498`; running/not-yet-metered → `…`; unpriced/unmetered → `—`. **Rest vs hover is done with COLOR, not opacity** (review P2/AA): a dedicated semantic token `--cost-rail` at rest (a muted-but-AA-passing tone, verified per skin × mode) intensifies to `--cost-rail-strong` (or `--text-primary`) on row hover / active row. No `opacity` on text. Transition respects `prefers-reduced-motion`. The real row is otherwise unchanged (status dot + `id` bold / `name` muted).

3. **Cost tab** — a **permanent** workspace tab (always selectable, independent of whether data exists; empty state renders "No usage data"). A live `afm report`: table `Stage · Model · Tokens · Cache R/W · Cost`. Each stage row is **expandable** via a chevron **button** in the Stage cell (see accessibility below) → an inline detail block: a **token-mix bar** (widths computed from `total_tokens`: cache-read / cache-write / output / uncached — labelled "Token mix", NOT "where the money goes"; a cost-mix bar is deferred, it needs per-category cost the DTO doesn't carry) plus a grid of exact figures (cache read, cache write by TTL, output, uncached input, cache-hit ratio, phases×count). Below the stage rows: a `run overhead` row (`scope=run_overhead`), a `Total` row (`run_cost.display_cost`), and a coverage line built from `coverage_issues` (`codex/gpt-5.6-sol — unpriced — add a rate in pricing:`).

Visual reference: the brainstorm artifacts (chosen direction; rail placement) are the source of truth for spacing/tone — except the rail's rest state now uses a color token, not `opacity`.

## Architecture

Data exists after Increment 1: `accounting.Load(runDir) *Ledger` → `SummaryByStage()`, `RunSummary()`, `Records()`. Cost is **always available in any mode** (host too — `usage.jsonl` is written on every run), so **no capability gate**. The frontend renders empty/`—`/"No usage data" for a run that predates the feature.

```
usage.jsonl ─accounting.Load→ Ledger ─(presentation helpers)→ server read model → /api/status JSON → use-status → React
                                 │  DisplayCost / Coverage / CoverageIssue
                        SummaryByStage / RunSummary / Records
```

### Accounting health (review P1: `unavailable` ≠ "no data")

Three distinct states, never collapsed:

| Health | has_data | Meaning | UI |
|---|---|---|---|
| `ok` | `false` | run predates the feature / no usage yet | tile `—`, rail nothing, tab "No usage data" |
| `ok` | `true` | normal | full cost everywhere |
| `unavailable` | `true` | append/fsync failed *after* some records landed | **keep showing the last valid snapshot** + an amber "cost may be incomplete — accounting stopped" banner in the tab and an amber marker on the tile |
| `unavailable` | `false` | store failed before any record | tile amber `—`, tab "cost unavailable this run" |

`accounting.Store` already tracks `Unavailable()` (Increment 1). The read model reports `health` + `has_data` from the live store; `afm report`/reload paths derive them from whether `usage.jsonl` loaded and parsed. A failed store never drops already-known cost.

### Server read model (Task 7)

The live server holds the run's `accounting.Store` (Increment 1 wired `orchestrator.Options.Accounting`); `buildStageViews`/`handleStatus` gain read access. `state.LoadRunState` is NOT changed.

- **`accounting` presentation view** (owned by `pkg/accounting`, JSON-tagged; consumed by `pkg/server`): a `CostView` carrying `display_cost` (string), `coverage` (`full|partial|none`), `estimated_cost_usd` (number, for tooltips/tests), `priced`, coverage counts `metered`/`unmetered`/`unpriced`, `models` ([]string), and the token breakdown `uncached_input`, `cache_read`, `cache_write_5m`, `cache_write_1h`, `cache_write_other`, `output`, `reasoning_output`, plus derived `total_tokens`, `cache_write_total`, `cache_hit_ratio`, and `phases` (phase→invocation-count, for the expand detail). All numbers/strings come from accounting methods — the server does no arithmetic.
- **`StageView.Cost *CostView`** (`json:"cost,omitempty"`) — per-stage, nil when the stage has no usage record yet. From `SummaryByStage()[id]`.
- **`statusResponse` gains:** `run_cost *CostView`, `run_overhead_cost *CostView`, `coverage_issues []CoverageIssue`, and `accounting {health, has_data}`. `run_cost` drives the tile; `run_overhead_cost` the tab's overhead row; `coverage_issues` the coverage line.
- The server never fails `/api/status` because of accounting; a read error → `health=unavailable`, last snapshot preserved where possible.

### Frontend (Task 9)

`pkg/web/dashboard/src/`:

- **Types** (`types/cost.ts`): `CostSummary`/`TokenBreakdown`/`CoverageIssue`/`Coverage`/`AccountingHealth` mirroring the DTO; `Stage.cost?: CostSummary`; `Status.runCost?`, `Status.runOverheadCost?`, `Status.coverageIssues`, `Status.accounting`. `use-status` maps snake→camel; **absent cost → `undefined`** (not zeroed) so no-data is distinguishable from `$0`.
- **`RunMetrics`**: add the `Est. cost` tile — always a `<button>` (it always has a destination: the permanent Cost tab), showing `runCost?.displayCost ?? '—'`; amber marker when `coverage==='partial'` or `accounting.health==='unavailable'`, with a tooltip summarizing `coverageIssues`. Joins the narrow-header overflow popover.
- **Workspace tab** (`use-workspace-view` reducer + `WorkspaceHeader`): add `'cost'` to `WorkspaceView` as a **permanent, user-selectable** view (like Feed — not attention, never auto-opens). **Attention interaction (review P2 #8):** when attention auto-opens while the user is on Cost, record the prior view; on attention resolve, return to the **recorded** view (`cost`) instead of always Feed. Add a `returnView` field to the reducer state (default `feed`); document that auto-open is a temporary interruption that restores the user's last non-attention view. New component `components/cost-panel/CostPanel.tsx`.
- **`CostPanel`**: table + expandable rows + token-mix bar + overhead/total/coverage, from `Status.stages[].cost` / `runCost` / `runOverheadCost` / `coverageIssues`. Empty state ("No usage data") when `!accounting.hasData`; incomplete banner when `health==='unavailable'`. Row expand is local state (`Set<stageId>`).
  - **Accessibility (review P2 #7):** the expander is a chevron `<button>` inside the Stage cell with `aria-expanded` + `aria-controls` (pointing at the detail row's id), operable by Enter/Space, with a visible focus ring; the detail row is the announced region. The amber-gap tooltip (tile + coverage) opens on **focus and hover** and is reachable/toggle-able on touch (tap), not hover-only.
- **Rail** (`StagesList.tsx`): render `stage.cost?.displayCost` as a quiet mono span in `.stage-actions` before the kebab, colored by `--cost-rail`/`--cost-rail-strong` (color, not opacity). New tokenized CSS.
- **CSS**: new `skins/base/cost-panel.css` (tokenized), `@import`ed by every skin. New tokens: `--cost-rail`, `--cost-rail-strong`, and a violet `--cost-cachewrite` for the bar's cache-write segment — defined per skin × light/dark where the other accents live, all AA-checked. Bar segments: cache-read `--accent`, cache-write `--cost-cachewrite`, output `--success`, uncached `--text-muted`. Figures `font-variant-numeric: tabular-nums`.

### Responsive, mobile & themes (hard requirements)

Acceptance criteria, verified at implementation on real viewports (desktop wide, ~900px rail-breakpoint, small phone ~360–390px), every skin × light/dark:

- **Header tile at narrow widths** joins the `⋯` overflow popover (with its label), never truncating the run name; still opens the Cost tab from the popover.
- **Rail on mobile** (drawer <900px): cost figure + kebab fit the drawer width; `id`/`name` ellipsize first; the trailing slot never overflows.
- **Cost tab on small screens**: table in an `overflow-x: auto` container (its own scroll, no page-level horizontal scroll); `Stage` and `Cost` anchored, middle columns scroll; the expand detail grid reflows via `auto-fit`/`minmax`, bar stays full-width; coverage line wraps.
- **Themes**: every new surface driven only by semantic tokens; mint/amber/grey keep AA on each ground; the rail rest color and violet cache-write segment are AA-checked per skin.
- **Motion**: opacity/expand/color transitions respect `prefers-reduced-motion: reduce`.

### Docs (Task 10)

- `docs/accounting.md`: token semantics per schema, cost formula, cache TTL, reported-vs-estimated, subscription caveat, `pricing:` override syntax, `unmetered`/`unpriced`/`partial`, and where cost shows up (CLI + dashboard). Linked from `docs/config-reference.md` and `mkdocs.yml` nav.
- README: one line + link.

## Data-flow / states

| State | Header tile | Rail | Cost tab |
|---|---|---|---|
| priced stage | contributes (mint) | `$0.2498` | row; expands to breakdown |
| unpriced (known model, no rate) | total → `$X+` amber | `—` | row, `unpriced` pill, `—`, named in coverage line |
| unmetered (no usage captured) | `$X+` amber | `—` | row, `unmetered` note + coverage line |
| stage still running | total so far | `…` | row partial/empty until record lands |
| run predates feature (`ok`,`has_data=false`) | `—` | nothing | "No usage data" |
| store failed mid-run (`unavailable`,`has_data=true`) | last known + amber | last known | last snapshot + "cost may be incomplete" banner |

Live updates ride the existing `/api/status` poll — each stage's rail figure and tab row fill in as its record lands; the header total climbs.

## Error handling

- `health=ok`+`has_data=false` → "No usage data"; never `$0.00`.
- `health=unavailable` → keep the last valid snapshot, mark it incomplete (amber), never silently zero or blank already-known cost.
- Partial coverage is always surfaced (`+` on the tile, coverage line in the tab), never hidden.
- The server treats any accounting read error as `unavailable` (logged), never a `/api/status` failure — consistent with Increment 1's "usage failure never affects the run".

## Testing

- **Accounting** (`pkg/accounting`): `Coverage`/`DisplayCost` — `full`→`$X`, `partial`→`$X+` (known priced sum, never `—+`), `none`→`—`; `CoverageIssue` extraction names the right stage/phase/channel/model/reason for a multi-model stage (reuse `report.go`'s record walk).
- **Server** (`pkg/server`): `StageView.cost` present with a record / omitted without; `run_cost`/`run_overhead_cost`/`coverage_issues`/`accounting{health,has_data}` in `/api/status`; the three health states produce distinct payloads; `unavailable`+`has_data=true` still carries the last snapshot; `200` in every case. JSON-shape test (snake_case, `display_cost` present).
- **Frontend** (Vitest): tile shows `displayCost`, is a button, opens the Cost tab, amber on `partial`/`unavailable`, `—` on `none`; **Cost tab selectable with no data → "No usage data"**; `CostPanel` renders rows/overhead/total/coverage-from-issues, chevron button toggles the detail with `aria-expanded`/keyboard, token-mix bar widths sum to total; tooltip opens on focus + tap; `StagesList` rail cost uses the color token and the `…`/`—` states; **attention over Cost returns to Cost, not Feed**; `use-status` maps absent cost → undefined (old-backend safe); `unavailable`+snapshot keeps showing the last known total.
- **Live acceptance** (pricing spec's "Критерий готовности"): re-run Claude + GLM + Codex; dashboard tile total == `afm report` total; per-stage rail figures match; expand shows the right breakdown; an unpriced Codex stage → `—` + a coverage line naming `codex/…`; restart-stable.
- **Responsive/theme live-check** (browser, real run): tile → overflow popover on a narrow header; rail cost + kebab inside the mobile drawer at ~375px with no overflow; Cost tab table scrolls in its own container (no page-level horizontal scroll), detail grid reflows; every surface graphite/goga/novacorps × light/dark at AA; rail cost dim (by color) at rest, strong on hover.

## Out of scope

- `max_cost` budget guard (separate FSM change).
- Cost-mix bar / cost-over-time charts (token-mix bar only; cost-mix needs per-category cost from records — a later accounting addition).
- Editing `pricing:` from the UI (config stays file-driven).
- Any change to Increment 1 beyond the accounting **presentation helpers** (`Coverage`/`DisplayCost`/`CoverageIssue`) and JSON-tagged view used by the read model — no change to token math, `usage.jsonl`, or the CLI.
