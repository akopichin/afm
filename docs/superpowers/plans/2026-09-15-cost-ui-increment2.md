# Cost UI — Increment 2 — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Show token/cost accounting in the live dashboard (header total tile + per-stage rail figure + a Cost tab), and unify the CLI onto the same presentation, without touching Increment 1's accounting core.

**Architecture:** `pkg/accounting` gains presentation helpers (`Coverage`, `DisplayCost`, `CoverageIssue`, a `CostView` DTO) + a revision-cached `CostSnapshot()`. The CLI (`afm check`/`report`) and a new server read model both render those. The React dashboard adds a header tile, a rail figure, and a `CostPanel`, driven by `/api/status`. No new token math.

**Tech Stack:** Go (`go.mod` version — do not bump), `gopkg.in/yaml.v3`, Cobra, `go test -race`; React/TS + Vitest; CSS in `public/skins/`.

**Spec:** `docs/superpowers/plans/2026-09-14-stage-and-run-pricing.md` (Increment 1) and **`docs/superpowers/specs/2026-09-15-cost-ui-increment2-design.md`** (this increment — the binding contract; read it, it carries the seven-round detail these tasks reference).

## Global Constraints

- **Do NOT change Increment 1's core:** token normalization/math (`normalize.go`), pricing resolution (`pricing.go`), the `usage.jsonl` format/`store.go` append path, or the event log. Adding presentation helpers, a `CostSnapshot()` method, JSON tags, and a revision counter to `pkg/accounting` is allowed.
- **All money/coverage presentation is produced in `pkg/accounting`** and rendered identically by CLI, server, and frontend. No `+`, `$`, rounding, or coverage logic in `cmd`/`server`/React beyond calling the helpers.
- `pkg/accounting` imports only stdlib. Money is `float64`. `pkg/server`/`cmd` do no token/cost arithmetic.
- **`has_data == len(ledger.Records()) > 0`** — never derived from tokens/cost.
- **`health` (writer-operational: `ok`/`unavailable`) is orthogonal to `coverage` (`full`/`partial`/`none`).** `health=ok` never implies full cost.
- Never render a fabricated `$0.00`; unknown → `—` (or `$X+` for a known partial minimum).
- Theme-aware (graphite/goga/novacorps × light/dark), AA contrast, `prefers-reduced-motion`. CSS source is `pkg/web/dashboard/public/skins/`; regenerate embedded artifacts.
- Commit messages in Russian; no `Co-Authored-By`. `make lint` clean; `go test ./... -race` and the dashboard Vitest suite green.

## File Structure

- `pkg/accounting/present.go` (new) — `Coverage`, `DisplayCost`, `CostView`, `CoverageIssue`, `Attribution`, `BuildCostView`, aggregation + comparator.
- `pkg/accounting/present_test.go` (new).
- `pkg/accounting/store.go` (modify) — `revision`, `CostSnapshot()`, `CostProvider`.
- `pkg/accounting/report.go` (modify) — render via `DisplayCost`/`CoverageIssue`, degraded shapes.
- `cmd/afm/exit.go` (new) — `ExitError` transport; `cmd/afm/main.go` (modify) — handle it.
- `cmd/afm/check.go` / `report.go` (modify) — DisplayCost, corrupt/missing/other matrix.
- `pkg/server/{server.go,stageview.go,handlers.go}` (modify) — provider seam + DTO in `/api/status`.
- `cmd/afm/run.go` (modify) — pass provider (or static-unavailable) to `server.Config`.
- `pkg/web/dashboard/src/types/cost.ts` (new); `hooks/use-status/use-status.ts` (modify).
- `hooks/use-workspace-view/workspace-view.ts` (modify) — `cost` view + `returnView`.
- `components/run-metrics/*` (modify) — tile; `components/stages-list/*` (modify) — rail; `components/cost-panel/*` (new); `app/App.tsx` (modify).
- `public/skins/base/cost-panel.css` (new) + `public/skins/{graphite,goga,novacorps}/index.css` (modify); `docs/accounting.md` (new) + `mkdocs.yml`/`docs/config-reference.md`/README (modify).

---

## Task 1: Coverage + DisplayCost

**Files:** Create `pkg/accounting/present.go`, `pkg/accounting/present_test.go`.

**Interfaces — Produces:** `type Coverage string` (`CoverageFull`/`CoveragePartial`/`CoverageNone`); `func coverageOf(s Summary) Coverage`; `func DisplayCost(s Summary) string`.

Subtle points folded in (spec §Presentation model): coverage from COUNTS (`priced = Metered - Unpriced`, `gaps = Unpriced + Unmetered`), never money; explicit zero-rate is priced; DisplayCost reuses `FormatUSD` tiny handling then annotates.

- [ ] **Step 1: failing test** (`present_test.go`)

```go
package accounting

import "testing"

func sum(metered, unpriced, unmetered int, cost float64) Summary {
	return Summary{Metered: metered, Unpriced: unpriced, Unmetered: unmetered, CostUSD: cost}
}

func TestCoverageOf(t *testing.T) {
	cases := []struct{ name string; s Summary; want Coverage }{
		{"all priced", sum(2, 0, 0, 0.1), CoverageFull},
		{"single unpriced", sum(1, 1, 0, 0), CoverageNone},           // metered=1,unpriced=1 → priced=0
		{"priced+unpriced", sum(2, 1, 0, 0.1), CoveragePartial},
		{"priced+unmetered", sum(1, 0, 1, 0.1), CoveragePartial},
		{"zero-rate full", sum(1, 0, 0, 0), CoverageFull},            // priced, $0
		{"zero-rate partial", sum(2, 1, 0, 0), CoveragePartial},
		{"nothing", sum(0, 0, 0, 0), CoverageNone},
	}
	for _, c := range cases {
		if got := coverageOf(c.s); got != c.want {
			t.Errorf("%s: coverageOf = %q want %q", c.name, got, c.want)
		}
	}
}

func TestDisplayCost(t *testing.T) {
	cases := []struct{ name string; s Summary; want string }{
		{"full", sum(1, 0, 0, 0.3995), "$0.3995"},
		{"full zero", sum(1, 0, 0, 0), "$0.0000"},
		{"full tiny", sum(1, 0, 0, 0.00004), "<$0.0001"},
		{"partial", sum(2, 1, 0, 0.3995), "$0.3995+"},
		{"partial zero", sum(2, 1, 0, 0), "$0.0000+"},
		{"partial tiny", sum(2, 1, 0, 0.00004), "<$0.0001+"},
		{"none", sum(1, 1, 0, 0), "—"},
	}
	for _, c := range cases {
		if got := DisplayCost(c.s); got != c.want {
			t.Errorf("%s: DisplayCost = %q want %q", c.name, got, c.want)
		}
	}
}
```

- [ ] **Step 2:** `go test ./pkg/accounting/ -run 'TestCoverageOf|TestDisplayCost' -v` → FAIL (undefined).
- [ ] **Step 3: implement** `present.go`:

```go
package accounting

type Coverage string

const (
	CoverageFull    Coverage = "full"
	CoveragePartial Coverage = "partial"
	CoverageNone    Coverage = "none"
)

func coverageOf(s Summary) Coverage {
	priced := s.Metered - s.Unpriced
	gaps := s.Unpriced + s.Unmetered
	switch {
	case priced >= 1 && gaps == 0:
		return CoverageFull
	case priced >= 1:
		return CoveragePartial
	default:
		return CoverageNone
	}
}

// DisplayCost is the one cost string every surface renders. Full/partial reuse
// FormatUSD's tiny-value handling for the priced sum; partial appends "+"; none
// is the em dash (dashUnknown). Never gates on the money value.
func DisplayCost(s Summary) string {
	switch coverageOf(s) {
	case CoverageFull:
		return FormatUSD(s.CostUSD, true)
	case CoveragePartial:
		return FormatUSD(s.CostUSD, true) + "+"
	default:
		return dashUnknown
	}
}
```

- [ ] **Step 4:** `go test ./pkg/accounting/ -run 'TestCoverageOf|TestDisplayCost' -v` → PASS.
- [ ] **Step 5: commit** — `git commit -am "feat(accounting): Coverage + DisplayCost (counts-based, zero-rate, tiny)"`

---

## Task 2: CoverageIssue — structural attribution + aggregation

**Files:** Modify `pkg/accounting/present.go`, `pkg/accounting/present_test.go`.

**Interfaces — Produces:** `type AttrKind string` (`AttrStage`/`AttrRunOverhead`/`AttrUnknown`); `type Attribution struct{ Kind AttrKind; StageID string }` (JSON `kind`,`stage_id,omitempty`); `type CoverageIssue struct{ Kind string; Attribution Attribution; Phase, Channel, Model, Reason string; Count int }`; `func aggregateIssues(recs []UsageRecord) []CoverageIssue`; `func attributionOf(r UsageRecord) Attribution`; `func (a Attribution) Label() string`.

Subtle points (spec §CoverageIssue): `kind` single schema (no `attribution_kind`); structural identity (a stage literally named `run_overhead`/`—` stays distinct); group by full key; `Count` = invocations; TOTAL-order comparator over every field; label separate from key.

- [ ] **Step 1: failing test** — assert: overhead unpriced record → `Attribution{Kind:AttrRunOverhead}`; a stage id `run_overhead` and a real overhead record → two distinct groups; a stage id `—` and an empty-owner record → two distinct groups; two identical unpriced records → one issue `Count==2`; ordering byte-stable across two `aggregateIssues` calls on shuffled input.

```go
func TestAttributionIdentityDistinct(t *testing.T) {
	recs := []UsageRecord{
		{Metered: true, Priced: false, StageID: "run_overhead", Phase: "planning", Model: "m"},   // a STAGE named run_overhead
		{Metered: true, Priced: false, Scope: ScopeRunOverhead, Phase: "planning", Model: "m"},    // real overhead
	}
	got := aggregateIssues(recs)
	if len(got) != 2 {
		t.Fatalf("want 2 distinct groups, got %d: %+v", len(got), got)
	}
}

func TestAggregateCountsAndStableOrder(t *testing.T) {
	r := UsageRecord{Metered: true, Priced: false, StageID: "s", Phase: "impl", Model: "codex"}
	a := aggregateIssues([]UsageRecord{r, r, r})
	if len(a) != 1 || a[0].Count != 3 {
		t.Fatalf("want 1 issue count=3, got %+v", a)
	}
	// stable order: same slice regardless of input order
	x := aggregateIssues([]UsageRecord{{Metered: true, StageID: "b", Phase: "p"}, {Priced: false, Metered: true, StageID: "a", Phase: "p", Model: "m"}})
	y := aggregateIssues([]UsageRecord{{Priced: false, Metered: true, StageID: "a", Phase: "p", Model: "m"}, {Metered: true, StageID: "b", Phase: "p"}})
	// only unpriced/unmetered are issues; assert equal ordering of whatever groups exist
	if len(x) != len(y) {
		t.Fatalf("len differ %d vs %d", len(x), len(y))
	}
	for i := range x {
		if x[i] != y[i] {
			t.Fatalf("order not stable at %d: %+v vs %+v", i, x[i], y[i])
		}
	}
}
```

(Adjust `UsageRecord` field access to its real shape from Increment 1 — read `pkg/accounting/ledger.go`; a record is an "issue" when `!Priced && Metered` (unpriced) or `!Metered` (unmetered).)

- [ ] **Step 2:** run → FAIL.
- [ ] **Step 3: implement** `attributionOf` (`Scope==ScopeRunOverhead` → `AttrRunOverhead`; `StageID!=""` → `AttrStage`; else `AttrUnknown`), `(Attribution).Label()` (mirrors `report.go`'s `recordLabel`), and `aggregateIssues`: walk records, keep only unpriced/unmetered, group by a struct key `{kind, attrKind, stageID, phase, channel, model, reason}` in a map, accumulate `Count`, then sort by a comparator ordering **attrKind, stageID, phase, kind, channel, model, reason** for a total order.
- [ ] **Step 4:** run → PASS; `go test ./pkg/accounting/ -race`.
- [ ] **Step 5: commit** — `git commit -am "feat(accounting): CoverageIssue со структурной attribution + агрегация с count"`

---

## Task 3: CostView DTO + builder

**Files:** Modify `pkg/accounting/present.go`, `pkg/accounting/present_test.go`.

**Interfaces — Produces:** `type CostView struct{...}` (JSON snake_case: `display_cost`, `coverage`, `estimated_cost_usd`, `metered`, `priced_invocations`, `unpriced`, `unmetered`, `models`, token fields + `total_tokens`/`cache_write_total`, `cache_hit_ratio *float64`, `phases map[string]int`); `func BuildCostView(s Summary, phases map[string]int) *CostView`. Add a nullable cache-hit helper `func cacheHitRatioPtr(t Tokens) *float64` (nil when `InputTotal()==0`).

- [ ] **Step 1: failing test** — `BuildCostView`: partial summary → `display_cost` `$X+`, `coverage:"partial"`, `priced_invocations == metered-unpriced`; `cache_hit_ratio` **nil** when `InputTotal()==0`, `0` (pointer to 0) for uncached-only, non-zero for cached input; JSON of `CostView` uses snake_case incl. `display_cost`/`priced_invocations` and `cache_hit_ratio` serializes to `null` when nil.
- [ ] **Step 2:** run → FAIL.
- [ ] **Step 3: implement** the struct + tags + builder (fills every field from `Summary`/`Tokens` methods + `DisplayCost`/`coverageOf`; `cache_hit_ratio` from `cacheHitRatioPtr`). No arithmetic beyond calling existing methods.
- [ ] **Step 4:** run → PASS (incl. a `json.Marshal` assertion for `"cache_hit_ratio":null` and `"display_cost"`).
- [ ] **Step 5: commit** — `git commit -am "feat(accounting): CostView DTO (nullable cache_hit_ratio, priced_invocations)"`

---

## Task 4: CostProvider + revision-cached CostSnapshot

**Files:** Modify `pkg/accounting/store.go`, `pkg/accounting/store_test.go`. New tiny file `pkg/accounting/provider.go` if cleaner.

**Interfaces:**
- Consumes: `Ledger.SummaryByStage`/`RunSummary`/`Records` (Task-1/2/3 helpers).
- Produces: `type Health string` (`HealthOK`/`HealthUnavailable`); `type CostBundle struct{ Stages map[string]*CostView; Run, Overhead *CostView; Issues []CoverageIssue; Health Health; HasData bool }`; `type CostProvider interface{ CostSnapshot() CostBundle }`; `func (s *Store) CostSnapshot() CostBundle`; `func StaticUnavailable() CostProvider` (for the nil-open case). Store gains an internal `revision uint64` bumped on successful `Append` and on the unavailable-latch, plus a cached `*CostBundle` + the revision it was built at.

Subtle points (spec §Server read model / round-7 #7): revision bump only on append/health-change; unchanged poll returns cached bundle with **no ledger copy / no re-aggregation**; build reads records under the lock briefly, computes views outside; returned maps/slices immutable to callers; `has_data = len(Records())>0`; `Run`/stage cost non-nil whenever a record exists (even all-unmetered → coverage none).

- [ ] **Step 1: failing test** (`store_test.go`):

```go
func TestCostSnapshot_RevisionCacheReusesBuild(t *testing.T) {
	s := openTestStore(t) // helper: Open a temp store with a resolver
	s.Append(obsGLM(), "backend", "implementation", "")
	b1 := s.CostSnapshot()
	b2 := s.CostSnapshot() // no Append between
	if !sameBundleIdentity(b1, b2) { // helper: same cached pointer / revision
		t.Fatal("expected cached bundle reuse without rebuild")
	}
	s.Append(obsGLM(), "backend", "review", "")
	b3 := s.CostSnapshot()
	if sameBundleIdentity(b2, b3) {
		t.Fatal("append must invalidate the cache")
	}
}

func TestCostSnapshot_AllUnmeteredIsData(t *testing.T) {
	s := openTestStore(t)
	s.Append(unmeteredObs(), "backend", "implementation", "") // metered:false
	b := s.CostSnapshot()
	if !b.HasData { t.Fatal("one record ⇒ has_data") }
	if b.Run == nil || b.Run.Coverage != string(CoverageNone) { t.Fatalf("run=%+v", b.Run) }
	if b.Stages["backend"] == nil { t.Fatal("stage-attributed record ⇒ non-nil stage cost") }
}
```

(Provide `openTestStore`, `sameBundleIdentity` (compare a build-generation counter exposed for tests, or pointer equality of the cached bundle), `unmeteredObs`.)

- [ ] **Step 2:** run → FAIL.
- [ ] **Step 3: implement** the revision + cache in `Store` (bump in `Append` after successful apply, and where it latches `unavailable`); `CostSnapshot()` returns the cache when `revision == cachedAt`, else rebuilds; `HasData` from record count; `Health` from `Unavailable()`; build the per-stage/run/overhead `CostView`s via `BuildCostView` and `aggregateIssues`. Add `StaticUnavailable()`.
- [ ] **Step 4:** run → PASS; `go test ./pkg/accounting/ -race`.
- [ ] **Step 5: commit** — `git commit -am "feat(accounting): CostProvider + revision-кэшированный CostSnapshot"`

---

## Task 5: CLI — check/report onto DisplayCost + ExitError transport

**Files:** Create `cmd/afm/exit.go`; modify `cmd/afm/main.go`, `cmd/afm/check.go`, `cmd/afm/report.go`, `pkg/accounting/report.go`, and their tests.

**Interfaces — Produces:** `type ExitError struct{ Code int; Silent bool }` implementing `error`; `main` maps it to `os.Exit(Code)` (alongside, not replacing, `docker.SubprocessExitError`). `RenderMarkdown` gains a degraded path (title + FSM stage rows + a self-contained note; distinguishes missing vs unavailable).

Subtle points (spec §CLI consistency, round-5/6/7): partial → `$X+`; `coverageNote` totals `sum(count)`; keep `Rates used` + mismatch subsections; matrix — missing (stdout `No usage data`, exit 0) / corrupt (`ErrCorruptUsage` → stderr `corrupt`, **exit 3**, stdout self-contained note, no invented totals) / **other read error** (stderr `cannot read … <cause>`, exit 3) / normal; report stdout self-contained (reason in markdown, survives redirect); tests assert stdout/stderr/exit separately + a **subprocess/main-level** exit-3 test; use injected loader / dir-fixture, not chmod.

- [ ] **Step 1: failing tests** — `cmd/afm/exit_test.go`: `main` translates `ExitError{Code:3}` to process exit 3 (subprocess test invoking the built binary or a `TestMain`-style re-exec). `check_test.go`/`report_test.go`: missing/corrupt/other/normal matrix with separate stdout/stderr/exit assertions; `coverageNote` shows `2 unpriced` for two identical unpriced records.
- [ ] **Step 2:** run → FAIL.
- [ ] **Step 3: implement** `ExitError` + `main` handling; refactor `check.go`/`report.go` to use `DisplayCost`/`aggregateIssues` and `errors.Is(err, ErrCorruptUsage)` vs other Load errors → the matrix; `RenderMarkdown` degraded shape (stop the bare-`No usage data` early return; render title + FSM rows + note). Keep `renderCoverage`'s `Rates used`/mismatch record walk.
- [ ] **Step 4:** run → PASS; `go test ./cmd/afm/ ./pkg/accounting/ -race`; `make lint`.
- [ ] **Step 5: commit** — `git commit -am "feat(cli): check/report на DisplayCost/CoverageIssue + ExitError транспорт (exit 3)"`

---

## Task 6: Server read model

**Files:** Modify `pkg/server/server.go`, `pkg/server/stageview.go`, `pkg/server/handlers.go` (statusResponse), `cmd/afm/run.go`, and server tests.

**Interfaces:**
- Consumes: `accounting.CostProvider`/`CostBundle`/`CostView`/`CoverageIssue` (Tasks 3–4).
- Produces: `server.Config.Accounting accounting.CostProvider`; `StageView.Cost *accounting.CostView` (`json:"cost,omitempty"`); `statusResponse` fields `run_cost`, `run_overhead_cost` (`*CostView`), `coverage_issues []CoverageIssue`, `accounting *accountingHealth` (`{health, has_data}`, omitted when provider nil).

Subtle points: provider nil ⇒ accounting fields omitted (unsupported, distinct from a present `unavailable`); `cmd/afm/run.go` passes the live `*Store` when `acct!=nil`, else `accounting.StaticUnavailable()` when `acctErr!=nil` (nil vs static-unavailable distinct); server never fails `/api/status` for accounting; one `CostSnapshot()` per request feeds both stage views and the run fields.

- [ ] **Step 1: failing tests** (`stageview_test.go`/`server_test.go`): `buildStageViews` sets `Cost` from the bundle when a stage has a record, omits it otherwise; `/api/status` carries `run_cost`/`run_overhead_cost`/`coverage_issues`/`accounting`; nil provider → all omitted; static-unavailable provider → `accounting:{health:"unavailable",has_data:false}`; **health ⟂ coverage** — a healthy provider with a single unpriced record → `accounting.health:"ok"`, `has_data:true`, `run_cost.coverage:"none"`; `attribution` JSON literal for the three variants.
- [ ] **Step 2:** run → FAIL.
- [ ] **Step 3: implement** the `Config` field + thread it into the `Server`; call `CostSnapshot()` once in `handleStatus`; populate `StageView.Cost` in `buildStageViews` (pass the bundle in); add the statusResponse fields; wire `cmd/afm/run.go` (static-unavailable branch).
- [ ] **Step 4:** run → PASS; `go test ./pkg/server/ ./cmd/afm/ -race`.
- [ ] **Step 5: commit** — `git commit -am "feat(server): cost read model в /api/status (provider seam, health⟂coverage)"`

---

## Task 7: Frontend types + use-status normalization

**Files:** Create `pkg/web/dashboard/src/types/cost.ts`; modify `hooks/use-status/use-status.ts` + its test.

**Interfaces — Produces:** TS `CostSummary`/`TokenBreakdown`/`CoverageIssue`/`Attribution`/`Coverage`/`AccountingState`; `FlowStatus.runCost?`, `runOverheadCost?`, `coverageIssues: CoverageIssue[]`, `accounting?: AccountingState` where **absent JSON ⇒ `{ supported: false }`**; `Stage.cost?: CostSummary`.

Subtle points (round-6 #4 wire schema, round-4 #4 backward-compat): map snake→camel; `attribution.kind`; absent `accounting` ⇒ `supported:false` (NOT `ok,false` — must not look like a healthy new writer so the rail shows no optimistic `…`); absent `coverage_issues` ⇒ `[]`; absent per-stage/run cost ⇒ `undefined`; `cache_hit_ratio` null → `null`.

- [ ] **Step 1: failing test** (`use-status.test.ts`): `normalizeStatus` on a **fully old JSON shape** (no `accounting`, no `cost`, no `coverage_issues`) → `accounting` is `{supported:false}` (or a sentinel the rail treats as "no optimistic …"), `coverageIssues===[]`, stage `.cost===undefined`; and on a new shape maps `run_cost.display_cost`, `attribution.kind`, `cache_hit_ratio:null`.
- [ ] **Step 2:** run → FAIL.
- [ ] **Step 3: implement** the types + mapping.
- [ ] **Step 4:** run → PASS (`npm test` for the file).
- [ ] **Step 5: commit** — `git commit -am "feat(dashboard): типы cost + нормализация use-status (supported:false для старого backend)"`

---

## Task 8: Workspace-view reducer — cost view + returnView

**Files:** Modify `hooks/use-workspace-view/workspace-view.ts` + test.

**Interfaces:** `WorkspaceView` gains `'cost'`; `WorkspaceState` gains `returnView: 'feed' | 'cost'`; actions gain `{type:'openCost'}` (and existing `openFeed`). Attention entry records `returnView` (from `feed`/`cost`; from a history view records `feed`); resolving the last attention restores `returnView`; a manual `openFeed`/`openCost` during attention replaces `returnView`; a second attention doesn't overwrite an already-set `returnView`.

- [ ] **Step 1: failing tests** — cost→attention→resolve→cost; history→attention→resolve→feed; manual openCost mid-attention → resolve lands cost; second attention mid-first doesn't clobber `returnView`; `openCost`/`openFeed` set the view.
- [ ] **Step 2:** run → FAIL.
- [ ] **Step 3: implement** the view + `returnView` field + transitions (extend `applyAttention`/`workspaceReducer`; `returnView` restricted to the two globals).
- [ ] **Step 4:** run → PASS.
- [ ] **Step 5: commit** — `git commit -am "feat(dashboard): cost-вид в workspace reducer + returnView контракт"`

---

## Task 9: RunMetrics — Est. cost tile

**Files:** Modify `components/run-metrics/RunMetrics.tsx` + `run-metrics.css` + test.

**Interfaces — Consumes:** `FlowStatus.runCost`, `coverageIssues`, `accounting`; a new prop `onOpenCost: () => void`. Produces a fifth metric.

Subtle points: always a `<button>` → `onOpenCost`; amber marker when `coverageIssues.length>0 || accounting?.health==='unavailable'`; plain `—` no marker for unsupported/no-data; **reason text composes both axes** (storage sentence if unavailable; coverage summary if issues; both if both); **twin render** (inline + popover) — no duplicate DOM ids, each `aria-describedby` resolves to one node (shared hidden node or `useId` per instance); **popover activation closes the popover then moves focus** to a stable Cost target; **5-metric collapse order** (Idle/Backoff first, then Cost) re-tuned; shared **bounded summary builder** (first 3 groups + `and K more groups (M invocations)`).

- [ ] **Step 1: failing tests** (`RunMetrics.test.tsx`): tile is a button, calls `onOpenCost`; amber+non-empty accessible reason for `ok+issues`, `unavailable+no issues` (both hasData), `unavailable+issues` (both sentences); plain `—` no marker for unsupported; all DOM ids unique across inline+popover; keyboard: open popover → activate Cost → popover closed, `onOpenCost` called, focus moved; summary builder shows exactly 3 groups + correct K/M.
- [ ] **Step 2:** run → FAIL.
- [ ] **Step 3: implement** the tile, reason builder, focus handoff, id strategy, collapse order.
- [ ] **Step 4:** run → PASS.
- [ ] **Step 5: commit** — `git commit -am "feat(dashboard): тайл Est. cost (reason по двум осям, focus/id при twin-render, cap)"`

---

## Task 10: Rail per-stage cost

**Files:** Modify `components/stages-list/StagesList.tsx` + `public/skins/base/stages-list.css` + test.

**Interfaces — Consumes:** `stage.cost`, `stage.isScript`, `accounting`. A pure helper `railCost(stage, accounting): {text?:string, a11y?:string}` encoding the precedence.

Subtle points: precedence — `stage.cost` present → `display_cost` always; else `!isScript && accounting.supported && health==='ok' && status ∈ {planning,running,revising}` → `…`; else nothing (pending/script/unsupported/any unavailable/retrying). Color token `--cost-rail`→`--cost-rail-strong` on row hover/active (no opacity). Tie to the stage button's accessible name via `aria-describedby`/visually-hidden; accessible text spells state (`Estimated cost $X, partial pricing coverage` / `unavailable` / `pending`); empty rail → no description.

- [ ] **Step 1: failing tests** (`StagesList.test.tsx`): running WITH a priced `stage.cost` → `$X` (not `…`); cost-nil + running + non-script + `ok` → `…`; cost-nil + running **script** → nothing; cost-nil + `unavailable` → nothing; `retrying` → nothing; accessible description present per state and absent when empty.
- [ ] **Step 2:** run → FAIL.
- [ ] **Step 3: implement** the helper + render into `.stage-actions` (before the kebab) + CSS token + a11y wiring.
- [ ] **Step 4:** run → PASS.
- [ ] **Step 5: commit** — `git commit -am "feat(dashboard): цена в стойке стадий (precedence, !script, health-гейт, a11y)"`

---

## Task 11: CostPanel + App wiring

**Files:** Create `components/cost-panel/CostPanel.tsx` (+ test, + index); modify `app/App.tsx`, `components/workspace/*` (tab), `public/skins/base/cost-panel.css`.

**Interfaces — Consumes:** `FlowStatus.stages[].cost`, `runCost`, `runOverheadCost`, `coverageIssues`, `accounting`. Rendered when `wsState.view==='cost'`.

Subtle points: **permanent tab**; empty-state order — `unavailable&&!hasData` → "Cost unavailable this run", `!hasData` → "No usage data", data+unavailable → snapshot + incomplete banner; expandable rows keyed by discriminated `{kind:'stage'|'overhead', id}` (overhead row expandable too); token-mix bar widths from `total_tokens`, **`total_tokens===0` → neutral empty track**; `cache_hit_ratio===null` → `—`; coverage line + `×count` rows + shared bounded summary; **own vertical scroll** (`flex:1;min-height:0;overflow-y:auto`) + **sticky Stage/Cost columns** in `overflow-x:auto`; a11y — chevron `<button>` `aria-expanded`+`aria-controls`→`role="region"` inside a `<td colSpan=5>` with `useId` ids; **`WorkspaceHeader` hidden when `view==='cost'`** (App).

- [ ] **Step 1: failing tests** (`CostPanel.test.tsx` + an `App` test): empty-state order (all three); expand a stage row and the overhead row independently (test with stage ids `__overhead__` and `a stage`), `aria-controls` resolves to one region; token-mix widths sum to total; `total_tokens===0` → empty track no NaN; `cache_hit_ratio null → —`; `×count` shown; App: select a stage, open Cost → no `WorkspaceHeader` stage context; long list + expanded rows keep `Total` reachable (scroll container present).
- [ ] **Step 2:** run → FAIL.
- [ ] **Step 3: implement** the panel + CSS (vertical scroll, sticky cols, bar) + App wiring (route `view==='cost'`→`CostPanel`, hide stage header, pass `onOpenCost`/`openCost` through) + workspace tab entry.
- [ ] **Step 4:** run → PASS.
- [ ] **Step 5: commit** — `git commit -am "feat(dashboard): CostPanel (empty-state, expand, sticky, a11y) + App wiring"`

---

## Task 12: CSS tokens across skins, docs, live acceptance

**Files:** Modify `public/skins/{graphite,goga,novacorps}/index.css` (+ `base/tokens.css` if the semantic layer lives there); regenerate embedded artifacts; create `docs/accounting.md`; modify `mkdocs.yml`, `docs/config-reference.md`, `README.md`.

Subtle points: new tokens `--cost-rail`, `--cost-rail-strong`, `--cost-cachewrite` (violet) per skin × light/dark, AA-checked; cache-read bar segment uses **`--accent-primary`** (not `--accent`); `cost-panel.css` `@import`ed by the three skins; **regenerate embedded** (the `skins/` build output) so the binary carries the CSS; Coffee excluded (normalizes to graphite).

- [ ] **Step 1:** add the tokens to each supported skin (dark+light), `@import "../base/cost-panel.css"` in each; run the dashboard build / skin-sync so embedded artifacts regenerate; `make build`.
- [ ] **Step 2:** write `docs/accounting.md` (token semantics, formula, TTL, reported-vs-estimated, subscription caveat, `pricing:` overrides, unmetered/unpriced/partial, health vs coverage, CLI + dashboard surfaces); add to `mkdocs.yml` nav + a link from `docs/config-reference.md` + one README line; `mkdocs build --strict` clean.
- [ ] **Step 3: full test gate** — `go test ./... -race`, `make lint`, dashboard `npm run lint`/`test`/`build`, `make generate-check`.
- [ ] **Step 4: live acceptance** (browser + real run, Docker off via project `.afm/config.yaml`): Claude + GLM + Codex run; assert **tile total == `afm report` total == `afm check` total**; rail figures match; expand shows breakdown; unpriced Codex → stage `—` / run `$X+` + amber + coverage line naming `codex/…`; restart-stable (incl. the degraded prefix→forced-write-failure→reopen case: live shows `unavailable,true`, reopen shows `ok,true` per the documented health limit, totals over the prefix stable); **responsive** — 1280/1279 header (5 metrics + long name + all controls, no overflow), rail cost+kebab in the ~375px drawer, Cost tab sticky Stage/Cost after horizontal scroll + own vertical scroll; **themes** — every surface graphite/goga/novacorps × light/dark at AA; rail dim-by-color at rest, strong on hover.
- [ ] **Step 5: commit** — `git commit -am "feat(dashboard): cost CSS-токены по скинам + docs/accounting.md + live/responsive"`

---

## Self-Review

- **Spec coverage:** presentation model → T1–T3; provider/revision-cache → T4; CLI+ExitError → T5; server read model → T6; types/use-status → T7; reducer/returnView → T8; tile → T9; rail → T10; CostPanel/App → T11; CSS/docs/live → T12. All spec sections map to a task.
- **Subtle points folded in:** counts-based coverage + zero-rate + tiny (T1); structural attribution + count + total order (T2); nullable cache_hit_ratio + priced_invocations (T3); revision cache + has_data=records + all-unmetered-is-data (T4); ExitError transport + corrupt/other/missing matrix + self-contained report stdout + sum(count) (T5); provider-nil-vs-static-unavailable + health⟂coverage + attribution JSON (T6); supported:false backward-compat (T7); returnView∈{feed,cost} (T8); composed reason + twin-render ids + popover focus + 5-metric collapse + bounded summary (T9); rail precedence + !isScript + health gate + color-not-opacity + a11y (T10); empty-state order + discriminated expand key + zero-token bar + sticky cols + vertical scroll + region-in-td + hide WorkspaceHeader (T11); `--accent-primary` + per-skin tokens + regenerate embedded + Coffee-excluded (T12).
- **Type consistency:** `Coverage`, `DisplayCost`, `Attribution{Kind,StageID}`, `CoverageIssue{Count}`, `CostView`, `CostBundle`, `CostProvider.CostSnapshot()`, `ExitError`, `StageView.Cost`, `FlowStatus.accounting/runCost/coverageIssues`, `WorkspaceView 'cost'`/`returnView`, `railCost` used consistently across tasks.
