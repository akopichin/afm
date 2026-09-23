# Per-Stage Feed + Full-Feed Tab Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Clicking a completed stage always shows that stage's own events (never an empty feed), by capping the feed **per stage** instead of flow-wide, and by splitting the two concerns into two tabs: **Feed** (always the current stage) and a new **Full feed** (last 1000 events of the whole flow).

**Architecture:** Today both `/api/events` (server, `maxReplayEvents = 200`) and `useEventFeed` (client, `MAX_EVENTS = 200`) keep only the last 200 events **flow-wide**, then `FeedWorkspace` filters them by stage. On a long run (many stages, thousands of events) an early completed stage has zero of its events left in that global 200-window, so "This stage" renders "No events for this stage yet". We fix this with two independent history sources: a **per-stage** history endpoint (`/api/events?stage=<id>`, last 200 for that stage, reconstructed regardless of global recency) feeding the **Feed** tab, and the existing **global** history (`/api/events`, raised to last 1000) feeding a new **Full feed** tab plus the WS-refresh trigger. The AI-verify stage badge is moved OFF the event feed onto a durable per-stage field on `/api/status` (Task 7) so it too stops aging out. The `This stage | All` toggle and `useFeedScope` are removed; `FeedWorkspace` becomes a dumb renderer of whatever event list it is handed.

**Tech Stack:** Go 1.x (`pkg/server`), React + TypeScript + Vite (`pkg/web/dashboard`), Vitest, Go `testing`, Chrome DevTools MCP for live verification.

**Spec:** This document is self-contained (no separate spec). Root cause was reproduced live on 2026-09-23: with stages `early`(done) → `noise`(300 lines) → `hold`(paused), `/api/events` returned 200 events (199 `noise` + 1 `hold`, **0 `early`**), so the Feed for `early` was empty. See "Root cause" below.

## Global Constraints

- **Do NOT change the Go version in `go.mod`.**
- **CHANGELOG.md entries are English; commit messages are Russian.** No keepachangelog.com references.
- **Never add `Co-Authored-By` to commits.**
- **Lint must stay green** (`golangci-lint`, `npm run lint`) after every task.
- **Prefer simplicity:** remove code over adding; reuse existing helpers (`dedupeKey`, `mergeHistory`, `reconstructEventHistory`) rather than duplicating.
- **Per-stage cap = 200 events. Full-feed cap = 1000 events.** Both enforced on server AND client (same belt-and-suspenders pattern the code already uses for `maxReplayEvents`/`MAX_EVENTS`).
- **"1000 messages" = 1000 raw events** (one event ≈ one feed row; grouping into bubbles is presentational). This matches the existing event-cap semantics.
- The `Full feed` tab is **always present** (unlike `Cost`, which is gated on `accounting.supported`) and is rendered **last** in the tab row.
- **Presentation/transport only:** no orchestrator/FSM/event-log changes. The durable logs (`events.jsonl`, `<phase>.jsonl`, `notices.jsonl`) are untouched — we only change how much of them is replayed and how the client buckets them.

---

## Root cause (verified 2026-09-23)

- `pkg/server/events_handler.go:21` — `const maxReplayEvents = 200`; `reconstructEventHistory` sorts ALL events by time and returns only the last 200 flow-wide.
- `pkg/web/dashboard/src/hooks/use-event-feed/use-event-feed.ts:12` — `const MAX_EVENTS = 200`; the live stream and `mergeHistory` also slice to the last 200.
- `pkg/web/dashboard/src/components/feed-workspace/FeedWorkspace.tsx:56-61` — `This stage` filters `events` by `stageId`. When all of a stage's events are older than the global last-200, the filtered list is empty.

Live API evidence from the repro (`early` is `done`): `/api/events` returned `total 200 → noise:199, hold:1, early:0`. Even the `All` view started at `noise line 104` — everything earlier never reached the client.

---

## File Structure

**Server (`pkg/server/`)**
- `events_handler.go` — MODIFY. `handleEvents` reads a `?stage=<id>` query param. Introduce two caps: `maxReplayEvents = 1000` (global) and `maxStageReplayEvents = 200` (per-stage). Factor the cap/filter tail out of `reconstructEventHistory` so both the global and per-stage responses share the same reconstruction but differ only in filter + cap. **`reconstructNotices` must become stage-aware** — `notices.jsonl` is a single **shared** file (all stages' `script_output`/dialog/auto-answer notices interleaved), and `readLines` currently keeps only the last ~500 lines flow-wide. Filtering by stage AFTER that truncation would re-introduce the exact bug we're fixing (an old stage's notices truncated away by newer other-stage noise). Transitions (`store.History()` from `events.jsonl`) and `reconstructAgentActions` (per-stage `<phase>.jsonl` files) are NOT affected — only the shared `notices.jsonl` needs stage-aware retention.
- `events_handler_test.go` — MODIFY. Add tests for the `?stage=` filter, the per-stage 200 cap, the raised global 1000 cap, AND the blocker regression: an early stage whose notices are buried under >1000 newer other-stage notice lines must still return via `?stage=<id>`.

**Client — hooks (`pkg/web/dashboard/src/hooks/`)**
- `use-event-feed/use-event-feed.ts` — MODIFY. Raise `MAX_EVENTS` 200 → 1000 (global feed). Export the shared `dedupeKey` + a `mergeCapped` helper (currently `mergeHistory`) so the per-stage hook reuses them. No behavioral change beyond the cap.
- `use-event-feed/use-event-feed.test.ts` — MODIFY. Update the cap assertion (200 → 1000).
- `use-stage-events/use-stage-events.ts` — CREATE. `useStageEvents(stageId, globalEvents)` — lazily fetches `/api/events?stage=<id>` on stage change, merges with live `globalEvents` filtered to that stage, dedupes, caps at 200. Returns the per-stage event list for the Feed tab.
- `use-stage-events/use-stage-events.test.ts` — CREATE.
- `use-stage-events/index.ts` — CREATE.
- `use-feed-scope/` — DELETE the whole directory (`use-feed-scope.ts`, `use-feed-scope.test.ts`). The toggle is gone.
- `use-workspace-view/workspace-view.ts` — MODIFY. Add `'full-feed'` to `WorkspaceView`; add the `openFullFeed` action; extend `returnView` to `'feed' | 'cost' | 'full-feed'`; make `reduceSync`'s return-to-global path preserve `full-feed`.
- `use-workspace-view/use-workspace-view.ts` — MODIFY. Expose `openFullFeed`.
- `use-workspace-view/workspace-view.test.ts` — MODIFY. Cover `openFullFeed` + attention return-to-full-feed.

**Client — components (`pkg/web/dashboard/src/components/`)**
- `feed-workspace/FeedWorkspace.tsx` — MODIFY. Remove `useFeedScope`, the `This stage | All` switch, and the internal per-stage filter. `FeedWorkspace` now renders exactly the `events` it is given. Keep grouping, stick-to-bottom, Jump-to-latest, the note composer (`noteTarget`/`onSendNote`), image markers, verify report links, and dialog-click navigation. `stageId` becomes display-only (whether to still show the stage badge in Full feed — see Task 5).
- `feed-workspace/FeedWorkspace.test.tsx` — MODIFY. Drop toggle tests; assert it renders the given events verbatim (no filtering) and that the empty hint text is stage-appropriate.

**Client — app (`pkg/web/dashboard/src/app/`)**
- `App.tsx` — MODIFY. Wire `useStageEvents(workspaceStage?.id ?? null, events)`; feed the **Feed** body with the per-stage list; add the **Full feed** tab (pushed last, after Cost) rendering the global `events`; extend `isGlobalView`, `activeTabId`, `onSelectTab`. Keep WS-refresh on the global `events`. Replace `verifyByStage = useMemo(() => computeVerifyIndicators(events), [events])` with the durable per-stage field from `/api/status` (Task 7).
- `App.test.tsx` — MODIFY. Update tab-list expectations; add a Full-feed-tab test; keep the "WS refresh survives past the cap" test (now 1000).

**Client — tab CSS (`pkg/web/dashboard/skins/base/layout.css`)**
- MODIFY. **The Full feed tab is right-aligned** (flush to the right edge of the tab strip), not merely last in DOM order. Add `.workspace-tab[data-tab-id="full-feed"] { margin-left: auto; }` — `.workspace-tabs` is already `display:flex`, so `margin-left:auto` on the last child pushes it to the far right while every other tab (Feed / detail / beacon / Cost) stays left-packed. `WorkspaceTabs.tsx` already renders `data-tab-id={tab.id}` on each button, so no component change is needed — this is a pure CSS addition. (When the tab strip overflows on a narrow viewport it already scrolls via `overflow-x:auto`; `margin-left:auto` degrades gracefully there.)

**Verify indicator → durable per-stage (Task 7)**
- `pkg/server/stageview.go` — MODIFY. Add `StageView.Verify *VerifyView` (JSON `verify,omitempty`, `{step int, command string, phase string}`), computed per stage from `notices.jsonl` (`verify_started`/`verify_result`) — the durable analogue of the client's `computeVerifyIndicators`.
- `pkg/web/dashboard/src/types/stage.ts` + `hooks/use-status.ts` — MODIFY. Map `verify` into the `Stage` type.
- `pkg/web/dashboard/src/components/stages-list/StagesList.tsx` — MODIFY. Read `stage.verify` instead of the `verifyByStage` prop.
- `pkg/web/dashboard/src/components/feed-workspace/feed-view-model.ts` — `computeVerifyIndicators` + `VerifyIndicator` type become dead once StagesList stops using them; DELETE them and their tests (feed-view-model still maps verify events into feed ROWS via `mapVerifyEvent` — that stays; only the stage-card indicator derivation is removed).

**Docs**
- `CHANGELOG.md`, `pkg/web/dashboard/RELEASE_NOTES.md`, `docs/dashboard.md` — MODIFY (Task 9).

---

## Task 1: Server — per-stage `/api/events?stage=<id>` + raise global cap to 1000

**Files:**
- Modify: `pkg/server/events_handler.go`
- Test: `pkg/server/events_handler_test.go`

**Interfaces:**
- Produces: `GET /api/events` → global, last **1000** events (unchanged shape: `[]feedEvent`). `GET /api/events?stage=<id>` → only that stage's events, last **200**. An empty/absent `stage` param preserves today's global behavior (just with the raised cap).
- Consumes: existing `reconstructEventHistory()`, `feedEvent`.

- [ ] **Step 1: Write the failing tests**

Add to `events_handler_test.go` (follow the existing `httptest` pattern in that file):

```go
func TestHandleEvents_StageFilterReturnsOnlyThatStage(t *testing.T) {
	// Build a run with two stages; assert ?stage=early returns only 'early' events.
	srv := newTestServerWithStages(t, /* early: few events, noise: many */)
	req := httptest.NewRequest("GET", "/api/events?stage=early", nil)
	rr := httptest.NewRecorder()
	srv.handleEvents(rr, req)
	var got []feedEvent
	mustDecode(t, rr, &got)
	for _, e := range got {
		if e.StageID != "early" {
			t.Fatalf("stage filter leaked stage %q", e.StageID)
		}
	}
	if len(got) == 0 {
		t.Fatal("expected 'early' events, got none")
	}
}

func TestHandleEvents_StageFilterCapsAt200(t *testing.T) {
	// 'noise' has > 200 reconstructable events; ?stage=noise must cap at 200.
	srv := newTestServerWithStages(t /* noise: 300 script_output notices */)
	req := httptest.NewRequest("GET", "/api/events?stage=noise", nil)
	rr := httptest.NewRecorder()
	srv.handleEvents(rr, req)
	var got []feedEvent
	mustDecode(t, rr, &got)
	if len(got) != 200 {
		t.Fatalf("per-stage cap: want 200, got %d", len(got))
	}
}

func TestHandleEvents_GlobalCapRaisedTo1000(t *testing.T) {
	// > 1000 events flow-wide; global response caps at 1000 (was 200).
	srv := newTestServerWithStages(t /* > 1000 total events */)
	req := httptest.NewRequest("GET", "/api/events", nil)
	rr := httptest.NewRecorder()
	srv.handleEvents(rr, req)
	var got []feedEvent
	mustDecode(t, rr, &got)
	if len(got) != 1000 {
		t.Fatalf("global cap: want 1000, got %d", len(got))
	}
}

// Blocker regression (codex review): an early stage's notices must survive even
// when buried under thousands of NEWER other-stage notice lines in the shared
// notices.jsonl. This is the real-world reproduction of the original bug.
func TestHandleEvents_StageFilterSurvivesFlowWideNoticeNoise(t *testing.T) {
	runDir := t.TempDir()
	// early: 5 notices written FIRST.
	writeNotices(t, runDir, "early", 5)
	// noise: 2000 notices written AFTER (dominate any flow-wide tail window).
	writeNotices(t, runDir, "noise", 2000)
	srv := newTestServerForRunDir(t, runDir, []string{"early", "noise"})
	req := httptest.NewRequest("GET", "/api/events?stage=early", nil)
	rr := httptest.NewRecorder()
	srv.handleEvents(rr, req)
	var got []feedEvent
	mustDecode(t, rr, &got)
	if len(got) < 5 {
		t.Fatalf("early notices truncated by flow-wide noise: got %d, want >= 5", len(got))
	}
}
```

Reuse whatever fixture builder the existing tests use (`events_handler_test.go` already stands up a `Server` around a temp run dir with `notices.jsonl`/`events.jsonl`). If no reusable builder exists, add a small `writeNotices(t, runDir, stageID, n)` helper that appends `n` `script_output` notice lines — matching how `execScript` writes them (`stagefiles.AppendNotice(runDir, stageID, "script_output", map[string]string{"hook":"","line":...})`).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/server/ -run TestHandleEvents_ -v`
Expected: FAIL — `?stage=` is ignored (stage filter leaks other stages), global cap still 200.

- [ ] **Step 3: Implement the filter + caps**

In `events_handler.go`:

```go
// maxReplayEvents bounds the GLOBAL /api/events history (Full feed + the WS
// status-refresh trigger). Raised from 200 to 1000 so the whole-flow feed is
// deep enough on long runs. (The AI-verify badge no longer reads this — it's a
// durable per-stage field on /api/status, see Task 7.)
const maxReplayEvents = 1000

// maxStageReplayEvents bounds the PER-STAGE /api/events?stage=<id> history
// (the Feed tab). Independent of maxReplayEvents so a completed stage always
// shows its own last 200 events regardless of global recency — the whole point
// of the fix.
const maxStageReplayEvents = 200

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	stage := r.URL.Query().Get("stage")
	events := s.reconstructEventHistory(stage)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(events)
}

// reconstructEventHistory reconstructs the feed. When stageID is non-empty it
// returns only that stage's events, capped at maxStageReplayEvents; otherwise
// the whole flow, capped at maxReplayEvents.
func (s *Server) reconstructEventHistory(stageID string) []feedEvent {
	var out []feedEvent

	history, _ := s.store.History()
	for _, t := range history {
		out = append(out, transitionToFeedEvents(t)...)
	}

	snap := s.store.Snapshot()
	for id := range snap.Stages {
		if stageID != "" && id != stageID {
			continue // per-stage: skip other stages' agent-action reconstruction
		}
		out = append(out, reconstructAgentActions(s.runDir, id)...)
	}

	out = append(out, reconstructNotices(s.runDir, stageID)...) // stage-aware (Step 3b)

	cap := maxReplayEvents
	if stageID != "" {
		out = slices.DeleteFunc(out, func(e feedEvent) bool { return e.StageID != stageID })
		cap = maxStageReplayEvents
	}

	slices.SortFunc(out, func(a, b feedEvent) int { return a.Timestamp.Compare(b.Timestamp) })
	if len(out) > cap {
		out = out[len(out)-cap:]
	}
	return out
}
```

Note: the `handleEvents` signature changes from `(w, _)` to `(w, r)` to read the query param. `transitionToFeedEvents` builds all transitions (from `events.jsonl`, never flow-wide-truncated — cheap); the per-stage path filters them via `slices.DeleteFunc` before the cap. Only `reconstructAgentActions` (the expensive multi-MB log parse) is skipped for non-matching stages.

**Migration:** `reconstructEventHistory()` gains a required `stageID string` param. Grep for all callers (`grep -rn reconstructEventHistory pkg/server`) and update every one — today it's `handleEvents` plus any direct test calls; pass `""` for the existing global behavior.

- [ ] **Step 3b: Make `reconstructNotices` stage-aware (blocker fix)**

`notices.jsonl` is one shared file. `readLines` (a flow-wide last-500 window) truncates old stages' notices before any stage filter runs. Rewrite `reconstructNotices` to scan the WHOLE file with a **stage-aware ring buffer**, so an early stage's notices are never evicted by newer OTHER-stage noise:

```go
// reconstructNotices reads the shared notices.jsonl. When stageID != "" it
// keeps only that stage's notices (last maxStageReplayEvents of them); otherwise
// it keeps the last maxReplayEvents across all stages. Retention is applied
// PER SCOPE while scanning (a ring buffer bounded by the cap), NOT a flow-wide
// line truncation first — otherwise an early stage's notices vanish under newer
// other-stage lines (the exact bug this feature fixes). O(file) time, O(cap)
// memory; only invoked on an /api/events request, not a hot path.
func reconstructNotices(runDir, stageID string) []feedEvent {
	path := filepath.Join(runDir, "notices.jsonl")
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	ringCap := maxReplayEvents
	if stageID != "" {
		ringCap = maxStageReplayEvents
	}
	ring := make([]feedEvent, 0, ringCap)
	seen := map[string]bool{} // dialog_question/dialog_answer content dedup (unchanged intent)

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var e struct {
			Time    time.Time `json:"time"`
			Type    string    `json:"type"`
			StageID string    `json:"stage_id"`
			Data    any       `json:"data,omitempty"`
		}
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		if stageID != "" && e.StageID != stageID {
			continue // per-stage: only this stage's notices enter the ring
		}
		if dialogDedupTypes[e.Type] {
			key := dialogNoticeDedupKey(e.Type, e.StageID, e.Data)
			if seen[key] {
				continue
			}
			seen[key] = true
		}
		ring = append(ring, feedEvent{Type: e.Type, StageID: e.StageID, Data: e.Data, Timestamp: e.Time})
		if len(ring) > ringCap {
			ring = ring[1:]
		}
	}
	return ring
}
```

Call it as `reconstructNotices(s.runDir, stageID)` from `reconstructEventHistory`. (The old `readLines`-based helper is replaced; `readLines` stays for `reconstructAgentActions`, which reads per-stage phase files where flow-wide truncation is not a concern.)

Memory: the ring itself is O(cap), but the dialog dedup `seen` set spans the whole file (was previously bounded by the 500-line window), so total is **O(cap + distinct dialog-notice keys)** (codex round 2). That's a correctness improvement (dialog notices now dedup across the entire run) at bounded, small extra memory; acceptable. Ordering: the ring keeps scan (append) order; `reconstructEventHistory` still sorts the merged result by timestamp afterward, so a rare out-of-order concurrent append is corrected there.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/server/ -run TestHandleEvents_ -v`
Expected: PASS.

- [ ] **Step 5: Consider `maxLinesPerLog` for the raised global cap**

`maxLinesPerLog = 500` (`events_handler.go:153`) bounds agent-actions read per phase. With the global cap now 1000, a single busy phase can legitimately contribute more than 500 of the last-1000 events. Raise it to `1200` (comfortably above the 1000 global cap) so the full feed isn't silently under-filled for a dominant stage. Keep the sliding-window read (memory stays bounded). Update the doc comment to reference the 1000 global cap.

**Approximation caveat (codex #2):** the `maxLinesPerLog` window counts RAW lines, not parseable actions — a phase log with many non-`assistant` / unparseable lines yields fewer than 1200 reconstructed events, so the global feed (and, symmetrically, the per-stage 200) can be under-filled for a log that is mostly noise. This is an accepted approximation (agent stream-json logs are overwhelmingly assistant events in practice, and the durable per-stage feed is always ≥ what the old flow-wide-200 gave). Document it in the doc comment; do not add a scan-until-N-valid loop (YAGNI unless a real under-fill is observed).

- [ ] **Step 6: Run the full server package + lint**

Run: `go test ./pkg/server/... && golangci-lint run ./pkg/server/...`
Expected: PASS, no lint findings (the previously-unused `r` param is now used).

- [ ] **Step 7: Commit**

```bash
git add pkg/server/events_handler.go pkg/server/events_handler_test.go
git commit -m "feat(server): пер-стейдж /api/events?stage и глобальный кап 1000"
```

---

## Task 2: Client — raise global feed cap to 1000 + export shared merge helpers

**Files:**
- Modify: `pkg/web/dashboard/src/hooks/use-event-feed/use-event-feed.ts`
- Test: `pkg/web/dashboard/src/hooks/use-event-feed/use-event-feed.test.ts`

**Interfaces:**
- Produces: `MAX_EVENTS = 1000` (global). Exported `dedupeKey(e: AfmEvent): string` and `mergeCapped(history: AfmEvent[], live: AfmEvent[], cap: number): AfmEvent[]` (a generalization of the current `mergeHistory`, which becomes `mergeCapped(history, live, MAX_EVENTS)`). `toEvent(raw): AfmEvent` exported for the per-stage hook's fetch parsing.
- Consumes: nothing new.

- [ ] **Step 1: Write/adjust the failing test**

In `use-event-feed.test.ts`, the existing cap test asserts 200. Change it to 1000 and add a merge-helper unit test:

```ts
it('caps the global feed at 1000 events', async () => {
  // drive 1200 live events through the mocked socket; expect events.length === 1000
  // (mirror the existing 200-cap test, just with the new numbers)
})

it('mergeCapped keeps history as base and appends only live-only, capped', () => {
  const history = [ev('a'), ev('b')]
  const live = [ev('b'), ev('c')] // 'b' dup by dedupeKey
  expect(mergeCapped(history, live, 10).map(k)).toEqual(['a', 'b', 'c'])
  expect(mergeCapped(history, live, 2).map(k)).toEqual(['b', 'c']) // last-N
})
```

- [ ] **Step 2: Run to verify failure**

Run: `cd pkg/web/dashboard && npx vitest run src/hooks/use-event-feed`
Expected: FAIL (cap still 200; `mergeCapped` not exported).

- [ ] **Step 3: Implement**

- Change `const MAX_EVENTS = 200` → `const MAX_EVENTS = 1000` and update the comment (it references app.js's 200 — note the raise and why: Full feed depth).
- Rename `mergeHistory(history, live)` → `export function mergeCapped(history, live, cap)`; its body is the current one with `MAX_EVENTS` replaced by `cap`. Its call site becomes `mergeCapped(history, prev, MAX_EVENTS)`.
- The `onmessage` `.slice(-MAX_EVENTS)` stays (global cap).
- `export { dedupeKey, toEvent }`.

- [ ] **Step 4: Run to verify pass**

Run: `npx vitest run src/hooks/use-event-feed`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/web/dashboard/src/hooks/use-event-feed/
git commit -m "feat(dashboard): глобальный кап ленты 1000 + экспорт mergeCapped/dedupeKey"
```

---

## Task 3: Client — `useStageEvents` (per-stage feed source)

**Files:**
- Create: `pkg/web/dashboard/src/hooks/use-stage-events/use-stage-events.ts`
- Create: `pkg/web/dashboard/src/hooks/use-stage-events/index.ts`
- Test: `pkg/web/dashboard/src/hooks/use-stage-events/use-stage-events.test.ts`

**Interfaces:**
- Produces: `function useStageEvents(stageId: string | null, globalEvents: AfmEvent[]): AfmEvent[]` — the last ≤200 events of `stageId`, live. Returns `[]` when `stageId === null`.
- Consumes: `dedupeKey`, `toEvent`, `mergeCapped` (Task 2); `AfmEvent`.

**Design:** On `stageId` change, fetch `GET /api/events?stage=<id>` once → `history` (already ≤200, sorted). The live tail comes from `globalEvents.filter(e => e.stageId === stageId)`. Return `mergeCapped(history, liveForStage, 200)`. A fetch race is guarded by a generation ref (a stale response for a previous stage must not land). While the fetch is in flight, fall back to the live-filtered slice so the panel isn't a blank flash for a currently-running stage.

- [ ] **Step 1: Write the failing test**

```ts
import { renderHook, waitFor } from '@testing-library/react'
import { useStageEvents } from './use-stage-events'

const ev = (stage: string, seq: number) => ({ type: 'agent_action', stageId: stage, payload: {}, timestamp: new Date(seq).toISOString(), seq })

it('returns the fetched per-stage history merged with live global events', async () => {
  const fetchMock = vi.spyOn(global, 'fetch').mockResolvedValue(
    new Response(JSON.stringify([{ type: 'agent_action', stage_id: 'early', seq: 1, timestamp: '1970-01-01T00:00:00.001Z' }])),
  )
  const live = [ev('early', 2), ev('noise', 9)]
  const { result } = renderHook(() => useStageEvents('early', live))
  await waitFor(() => expect(result.current.length).toBe(2)) // seq1 (history) + seq2 (live), noise excluded
  expect(result.current.every((e) => e.stageId === 'early')).toBe(true)
  expect(fetchMock).toHaveBeenCalledWith('/api/events?stage=early')
})

it('returns [] and does not fetch when stageId is null', () => {
  const fetchMock = vi.spyOn(global, 'fetch')
  const { result } = renderHook(() => useStageEvents(null, []))
  expect(result.current).toEqual([])
  expect(fetchMock).not.toHaveBeenCalled()
})

it('ignores a stale response after the stage changed', async () => {
  // First stage's fetch resolves AFTER a switch to the second stage → must be dropped.
  // Use two deferred Responses keyed by URL; assert the hook never shows stage-A history under stage-B.
})

// Freshness / race coverage (codex #3):
it('appends live global events for the stage AFTER the history fetch resolves', async () => {
  // fetch resolves with [seq1]; then rerender with globalEvents=[seq1(dup), seq2 same stage] →
  // result === [seq1, seq2] (dup collapsed by dedupeKey, chronological).
})

it('shows the live-filtered tail while the fetch is still pending (no blank flash)', async () => {
  // fetch never resolves; globalEvents already has 1 event for the stage → result length 1 immediately.
})

it('dedupes an event present in BOTH fetched history and the live stream', async () => {
  // same seq in history and globalEvents → appears once.
})

it('does not call setHistory after stageId flips to null mid-flight', async () => {
  // gen bumped on the null switch → a late resolve is dropped (no act() warning / stale update).
})
```

- [ ] **Step 2: Run to verify failure**

Run: `npx vitest run src/hooks/use-stage-events`
Expected: FAIL (module missing).

- [ ] **Step 3: Implement**

```ts
import { useEffect, useMemo, useRef, useState } from 'react'
import type { AfmEvent } from '../../types'
import { dedupeKey, mergeCapped, toEvent } from '../use-event-feed'

const MAX_STAGE_EVENTS = 200

// useStageEvents — источник ленты для вкладки Feed: последние ≤200 событий ОДНОЙ
// стадии, независимо от глобального окна в 1000. История тянется один раз на
// смену стадии через /api/events?stage=<id> (там она уже ≤200 и отсортирована),
// а живой хвост берётся из globalEvents, отфильтрованных по стадии, и мёржится с
// историей по dedupeKey (тот же ключ, что и в use-event-feed). Гонка фетча
// закрыта generation-ref: ответ для ПРЕДЫДУЩЕЙ стадии не должен «приехать» в
// текущую (тот же приём, что и в use-image-paste для смены стадии).
export function useStageEvents(stageId: string | null, globalEvents: AfmEvent[]): AfmEvent[] {
  const [history, setHistory] = useState<AfmEvent[]>([])
  const gen = useRef(0)

  useEffect(() => {
    const myGen = ++gen.current // bump ALWAYS (codex minor): a null switch must also invalidate an in-flight fetch
    if (stageId === null) {
      setHistory([])
      return
    }
    setHistory([]) // не показываем историю прошлой стадии, пока грузим новую
    fetch(`/api/events?stage=${encodeURIComponent(stageId)}`)
      .then((r) => (r.ok ? r.json() : []))
      .then((raw: unknown) => {
        if (gen.current !== myGen || !Array.isArray(raw)) return
        setHistory(raw.map(toEvent))
      })
      .catch(() => {
        // Сеть/старый сервер — деградируем к чистому live-хвосту (ниже).
      })
    return () => {
      // Следующая смена стадии (или размонтирование) инвалидирует текущий фетч
      // через gen; отдельного AbortController не нужно — ответ просто дропнется.
    }
  }, [stageId])

  return useMemo(() => {
    if (stageId === null) return []
    const live = globalEvents.filter((e) => e.stageId === stageId)
    return mergeCapped(history, live, MAX_STAGE_EVENTS)
  }, [stageId, history, globalEvents])

  // dedupeKey imported to keep parity with mergeCapped's internal dedup.
  void dedupeKey
}
```

(If `dedupeKey` isn't referenced directly, drop the import and the `void` line — `mergeCapped` already uses it internally. Keep the import list minimal to satisfy lint.)

`index.ts`: `export { useStageEvents } from './use-stage-events'`

- [ ] **Step 4: Run to verify pass**

Run: `npx vitest run src/hooks/use-stage-events`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/web/dashboard/src/hooks/use-stage-events/
git commit -m "feat(dashboard): useStageEvents — пер-стейдж источник ленты (кап 200)"
```

---

## Task 4: Client — `FeedWorkspace` becomes a dumb renderer (remove the toggle)

**Files:**
- Modify: `pkg/web/dashboard/src/components/feed-workspace/FeedWorkspace.tsx`
- Test: `pkg/web/dashboard/src/components/feed-workspace/FeedWorkspace.test.tsx`
- Delete: `pkg/web/dashboard/src/hooks/use-feed-scope/` (both files)

**Interfaces:**
- Produces: `FeedWorkspace({ events, stageId, showStageBadges?, emptyHint?, onOpenDialog?, noteTarget?, onSendNote? })`. It renders `groupFeedItems(toFeedItems(events))` **verbatim** — no internal filtering. `stageId` is now used ONLY to key the note composer and (optionally) decide whether to show stage badges. `showStageBadges` defaults to `false` for the per-stage Feed (redundant — every row is the same stage) and `true` for Full feed.
- Consumes: `useStageEvents` output (Feed) or global `events` (Full feed), decided by the caller (App.tsx, Task 5).

- [ ] **Step 1: Update the test first**

In `FeedWorkspace.test.tsx`: delete any `This stage`/`All` toggle assertions. Add:

```tsx
it('renders exactly the events it is given (no internal filtering)', () => {
  const events = [mkEvent('agent_action', 'early'), mkEvent('agent_action', 'noise')]
  render(<FeedWorkspace events={events} stageId="early" />)
  // both rows present — FeedWorkspace does not filter by stageId anymore
  expect(screen.getByText(/early/)).toBeInTheDocument()
  expect(screen.getByText(/noise/)).toBeInTheDocument()
})

it('shows the empty hint when given no events', () => {
  render(<FeedWorkspace events={[]} stageId="early" emptyHint="No events for this stage yet" />)
  expect(screen.getByText('No events for this stage yet')).toBeInTheDocument()
})
```

- [ ] **Step 2: Run to verify failure**

Run: `npx vitest run src/components/feed-workspace/FeedWorkspace`
Expected: FAIL (toggle still present / filters by stage).

- [ ] **Step 3: Implement**

- Remove the `useFeedScope` import + call, `stageScopeActive`, `showScopeSwitch`, the `feed-controls`/`feed-switch` JSX, and `showStageScope`/`showAllScope`.
- `groups` becomes: `useMemo(() => groupFeedItems(toFeedItems(events)), [events])` — no filter.
- Empty hint: `<div className="empty-hint feed-empty">{emptyHint ?? 'No events yet'}</div>`.
- Add `showStageBadges?: boolean` (default `false`) to `FeedWorkspaceProps`. **Thread it through explicitly (codex #4):** `FeedGroupView` today renders the badge unconditionally (`{group.stageId !== '' && <span className="feed-stage-badge">…}`). Add `showStageBadges: boolean` to `FeedGroupViewProps`, gate the badge as `{showStageBadges && group.stageId !== '' && …}`, and pass `showStageBadges` from `FeedWorkspace` at the single `<FeedGroupView … />` call site. Per-stage Feed passes `false` (every row is the same stage — badge is redundant noise); Full feed passes `true`.
- Test BOTH: a Feed render (`showStageBadges` default false) has no `.feed-stage-badge`; a Full-feed render (`showStageBadges`) shows badges.
- Keep the composer block gated by `noteTarget !== null && onSendNote !== undefined`.

- [ ] **Step 4: Delete the dead hook + its references**

```bash
git rm -r pkg/web/dashboard/src/hooks/use-feed-scope/
grep -rn "use-feed-scope\|useFeedScope\|afm-feed-scope" pkg/web/dashboard/src   # must return nothing
```

- [ ] **Step 5: Run tests + typecheck**

Run: `npx vitest run src/components/feed-workspace && npm run typecheck` (or `tsc --noEmit`)
Expected: PASS; no dangling imports.

- [ ] **Step 6: Commit**

```bash
git add -A pkg/web/dashboard/src/components/feed-workspace/ pkg/web/dashboard/src/hooks/use-feed-scope/
git commit -m "refactor(dashboard): FeedWorkspace без тумблера This stage/All — рендерит переданные события"
```

---

## Task 5: Client — workspace-view reducer gains `full-feed`

**Files:**
- Modify: `pkg/web/dashboard/src/hooks/use-workspace-view/workspace-view.ts`
- Modify: `pkg/web/dashboard/src/hooks/use-workspace-view/use-workspace-view.ts`
- Test: `pkg/web/dashboard/src/hooks/use-workspace-view/workspace-view.test.ts`

**Interfaces:**
- Produces: `WorkspaceView = 'feed' | 'cost' | 'full-feed' | 'attention' | 'plan-history' | 'dialog-history'`; `returnView: 'feed' | 'cost' | 'full-feed'`; new action `{ type: 'openFullFeed' }`; `useWorkspaceView(...)` returns an added `openFullFeed: () => void`.
- Consumes: existing reducer.

- [ ] **Step 1: Write the failing tests**

```ts
it('openFullFeed switches to full-feed and records it as returnView', () => {
  const s = workspaceReducer(initialWorkspaceState, { type: 'openFullFeed' })
  expect(s.view).toBe('full-feed')
  expect(s.returnView).toBe('full-feed')
})

it('attention opened over full-feed returns to full-feed when the queue drains', () => {
  let s = workspaceReducer(initialWorkspaceState, { type: 'openFullFeed' })
  s = workspaceReducer(s, { type: 'sync', items: [item('s1','question')], suppressed: false }) // auto-open attention
  expect(s.view).toBe('attention')
  s = workspaceReducer(s, { type: 'sync', items: [], suppressed: false }) // drains
  expect(s.view).toBe('full-feed')
})
```

- [ ] **Step 2: Run to verify failure**

Run: `npx vitest run src/hooks/use-workspace-view`
Expected: FAIL (`openFullFeed` unknown; returnView type/behavior).

- [ ] **Step 3: Implement**

- `workspace-view.ts`: add `'full-feed'` to `WorkspaceView`; change `returnView` type to `'feed' | 'cost' | 'full-feed'`; add the action; in `workspaceReducer` add `case 'openFullFeed': return { ...state, view: 'full-feed', returnView: 'full-feed' }`; in `reduceSync`'s arrival branch, replace `returnView = view === 'cost' ? 'cost' : 'feed'` with a helper that maps a **global** current view to itself and any non-global (history) view to `'feed'`:

```ts
function globalReturnView(view: WorkspaceView): 'feed' | 'cost' | 'full-feed' {
  return view === 'cost' || view === 'full-feed' ? view : 'feed'
}
// ...
returnView = globalReturnView(view)
```

- `use-workspace-view.ts`: `const openFullFeed = useCallback(() => dispatch({ type: 'openFullFeed' }), [])` and add it to the returned object + the `UseWorkspaceView` type.

- [ ] **Step 4: Run to verify pass**

Run: `npx vitest run src/hooks/use-workspace-view`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/web/dashboard/src/hooks/use-workspace-view/
git commit -m "feat(dashboard): workspace-view — вид full-feed + openFullFeed"
```

---

## Task 6: Client — wire Feed (per-stage) + Full feed tab in `App.tsx`

**Files:**
- Modify: `pkg/web/dashboard/src/app/App.tsx`
- Modify: `pkg/web/dashboard/skins/base/layout.css` (right-align the Full feed tab)
- Test: `pkg/web/dashboard/src/app/App.test.tsx`

**Interfaces:**
- Consumes: `useStageEvents` (Task 3), `openFullFeed` (Task 5), `FeedWorkspace`'s new props (Task 4).

**Do NOT touch the verify indicator here.** `verifyByStage = useMemo(() => computeVerifyIndicators(events), [events])` and its `verifyByStage={verifyByStage}` prop to `StagesList` stay UNCHANGED in this task — they still work (read global `events`). Task 7 removes them and migrates `StagesList` to `stage.verify`. Removing them here would break `StagesList` (still expects the prop) until Task 7 lands. (Controller ruling, preflight scan.)

- [ ] **Step 1: Write the failing tests**

In `App.test.tsx`:

```tsx
it('renders a Full feed tab as the last tab', async () => {
  renderAppWith(/* status with stages, accounting supported */)
  const tabs = screen.getAllByRole('tab')
  expect(tabs[tabs.length - 1]).toHaveTextContent('Full feed')
})

it('Feed tab shows only the selected stage; Full feed shows all', async () => {
  // mock /api/events?stage=early → [early events]; /api/events → [early+noise], global
  renderAppWith(/* ... */)
  // default Feed (early selected) → only early rows
  // click Full feed tab → early + noise rows
})

it('no This stage / All toggle exists', () => {
  renderAppWith(/* ... */)
  expect(screen.queryByRole('button', { name: 'All' })).not.toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'This stage' })).not.toBeInTheDocument()
})
```

Keep/rename the existing "WS refresh survives past the 200-event feed cap" test → cap is now 1000 (the mechanism — last-element identity — is unchanged; only the number in the comment/fixture updates).

- [ ] **Step 2: Run to verify failure**

Run: `npx vitest run src/app/App`
Expected: FAIL (no Full feed tab; Feed still uses global events).

- [ ] **Step 3: Implement the wiring**

- Add `const { ..., openFullFeed } = useWorkspaceView(stages, editing)`.
- Add `const stageEvents = useStageEvents(workspaceStage?.id ?? null, events)`.
- `isGlobalView`: `wsState.view === 'feed' || wsState.view === 'cost' || wsState.view === 'full-feed'`.
- Tab list: after the Cost push, unconditionally push the Full-feed tab **last**:

```ts
if (accounting.supported) {
  tabs.push({ id: 'cost', label: 'Cost' })
}
tabs.push({ id: 'full-feed', label: 'Full feed' })
```

- `activeTabId`: `wsState.view === 'feed' ? 'feed' : wsState.view === 'cost' ? 'cost' : wsState.view === 'full-feed' ? 'full-feed' : 'detail'`.
- `onSelectTab`: add `if (id === 'full-feed') { openFullFeed(); return }`.
- Body rendering (the `wsState.view === 'cost' ? ... : ...` chain around App.tsx:627). **Branch precedence matters (codex #6): the `full-feed` branch MUST be tested BEFORE the `feed || workspaceStage === null` branch**, otherwise Full feed with no stage selected falls into the per-stage empty state. Order the ternary chain exactly: `cost` → `full-feed` → `feed || workspaceStage === null` → `detail`:
  - `wsState.view === 'full-feed'` → `<FeedWorkspace events={events} stageId={null} showStageBadges onOpenDialog={handleOpenDialogFromFeed} emptyHint="No events yet" />` (no note composer — global view, no single live stage).
  - `wsState.view === 'feed' || workspaceStage === null` → **Feed (per-stage)**: `<FeedWorkspace events={stageEvents} stageId={workspaceStage?.id ?? null} emptyHint={workspaceStage === null ? 'Select a stage to see its feed' : 'No events for this stage yet'} onOpenDialog={handleOpenDialogFromFeed} noteTarget={noteTarget} onSendNote={handleSendNote} />`.
- The `WorkspaceHeader` (stage name/status above the body) should render for `feed` but NOT for `full-feed` (like Cost, Full feed is global — no single-stage header). Update the guard: `workspaceStage !== null && wsState.view !== 'cost' && wsState.view !== 'full-feed'`.

- [ ] **Step 3b: Right-align the Full feed tab (CSS)**

Add to `pkg/web/dashboard/skins/base/layout.css`, right after the `.workspace-tab.active` / `.workspace-tab-label` rules:

```css
/* Full feed is pushed to the far RIGHT edge of the tab strip; every other tab
   (Feed / detail / beacon / Cost) stays left-packed. .workspace-tabs is already
   display:flex, so margin-left:auto on this one tab does it — no component change
   (WorkspaceTabs already renders data-tab-id on each button). */
.workspace-tab[data-tab-id="full-feed"] {
  margin-left: auto;
}
```

No change to `WorkspaceTabs.tsx`. Verify visually in Task 8 (live run): Full feed sits flush right, all other tabs left.

- [ ] **Step 4: Run tests + typecheck + full frontend suite**

Run: `npx vitest run src/app/App && npm run typecheck && npm run lint`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/web/dashboard/src/app/App.tsx pkg/web/dashboard/src/app/App.test.tsx
git commit -m "feat(dashboard): Feed = только текущий стейдж; новая вкладка Full feed (последняя)"
```

---

## Task 7: Server — durable per-stage AI-verify indicator (`StageView.Verify`)

**Files:**
- Modify: `pkg/server/stageview.go` (+ its test `stageview_test.go`)
- Modify: `pkg/web/dashboard/src/types/stage.ts`, `hooks/use-status.ts`, `components/stages-list/StagesList.tsx`, `app/App.tsx`
- Delete: `computeVerifyIndicators` + `VerifyIndicator`/`VerifyIndicatorPhase` from `components/feed-workspace/feed-view-model.ts` and their tests

**Interfaces:**
- Produces: `StageView.Verify *VerifyView` on `/api/status`, where `VerifyView = { Step int json:"step"; Command string json:"command"; Phase string json:"phase" }` and `Phase ∈ {"running","pass","needs_changes","inconclusive","error"}` — the SAME vocabulary as the deleted client `VerifyIndicatorPhase`. `omitempty`/nil when the stage has no verify activity.
- Consumes: `notices.jsonl` — a **dedicated unbounded** last-verify scan (NOT `reconstructNotices`).

**Design:** Mirror `computeVerifyIndicators` exactly, server-side and durable: scan `notices.jsonl` for this stage's `verify_started`/`verify_result` notices and keep only the LAST one — `verify_started` → `phase:"running"`; `verify_result` → its `verdict` (`pass`/`needs_changes`/`inconclusive`, else `error`). A new `verify_started` after a completed pass re-enters `running` (current state, not last-ever outcome — identical semantics to the client version).

**IMPORTANT (codex round 2 blocker): do NOT reuse `reconstructNotices(runDir, stageID)` here** — that is capped at `maxStageReplayEvents = 200`, so a `verify_result` followed by 200+ later same-stage notices would age out and the badge would vanish, re-introducing exactly the eviction this task exists to kill. Instead add a dedicated `latestVerifyForStage(runDir, stageID string) *VerifyView` that scans the WHOLE `notices.jsonl` once and retains ONLY the single latest verify notice (O(1) memory, unbounded lookback). Select the latest by **timestamp** (not raw scan order), with file-order as a deterministic tie-break — `AppendNotice` stamps `time.Now()` before writing, so concurrent writers can land slightly out of timestamp order; picking max-by-timestamp keeps parity with `reconstructEventHistory`'s own timestamp sort.

- [ ] **Step 1: Write the failing test** (`stageview_test.go`)

```go
func TestBuildStageViews_VerifyIndicatorFromNotices(t *testing.T) {
	runDir := t.TempDir()
	appendVerifyNotice(t, runDir, "build", "verify_started", map[string]any{"step": 1, "command": "codex"})
	appendVerifyNotice(t, runDir, "build", "verify_result", map[string]any{"step": 1, "command": "codex", "verdict": "needs_changes"})
	views := buildStageViews(/* cfg with stage "build", runDir */)
	v := findStage(views, "build").Verify
	if v == nil || v.Phase != "needs_changes" || v.Step != 1 || v.Command != "codex" {
		t.Fatalf("verify view = %+v, want {1 codex needs_changes}", v)
	}
}

func TestBuildStageViews_VerifyReRunReturnsRunning(t *testing.T) {
	// started → result(pass) → started(step2) ⇒ phase "running"
}

func TestBuildStageViews_NoVerifyNotices_NilVerify(t *testing.T) {
	// a stage with no verify notices ⇒ Verify == nil (omitempty)
}
```

- [ ] **Step 2: Run to verify failure** — `go test ./pkg/server/ -run TestBuildStageViews_Verify -v` → FAIL (no `Verify` field).

- [ ] **Step 3: Implement `VerifyView` + `latestVerifyForStage`** in `stageview.go`; populate `StageView.Verify` in `buildStageViews` via `latestVerifyForStage(runDir, stage.ID)` (the dedicated unbounded scan described above — NOT `reconstructNotices`). Keep the started/result → phase reduction identical to the client's old `computeVerifyIndicators` logic so behavior is unchanged.

- [ ] **Step 3b: Update the JSON-shape lock test.** `TestStageView_JSONShape_NoVerifyField` (`stageview_test.go:319`) asserts `verify` is ABSENT from the key set — it now must permit `verify` (omitempty: absent when nil, present when set). Rename/adjust it to lock the new shape (a stage with no verify notices still omits the key; a stage with verify includes `{step,command,phase}`).

- [ ] **Step 4: Run to verify pass** — `go test ./pkg/server/... && golangci-lint run ./pkg/server/...` → PASS.

- [ ] **Step 5: Frontend — read `stage.verify`, delete the client derivation**

- `types/stage.ts`: export `type StageVerify = { step: number; command: string; phase: 'running'|'pass'|'needs_changes'|'inconclusive'|'error' }` and add `verify?: StageVerify` to `Stage`. This becomes the single shared type (replacing the deleted `VerifyIndicator`).
- `use-status.ts`: add a `toVerifyView(raw: unknown): StageVerify | undefined` normalizer (validate `phase` against the enum, coerce `step`/`command` defensively like the other field mappers) and map `obj.verify` → `stage.verify`. Do NOT assume the server shape blindly.
- `StagesList.tsx`: currently imports `VerifyIndicator` (line 6), types `verifyPhaseLabel(phase: VerifyIndicator['phase'])` (line 47), declares the `verifyByStage?: Record<string, VerifyIndicator>` prop (line 41), destructures it (line 138), and reads `verifyByStage[stage.id]` (lines 312-316). Migrate ALL of these: import `StageVerify` from `../../types` instead; `verifyPhaseLabel(phase: StageVerify['phase'])`; DROP the `verifyByStage` prop entirely; render from `stage.verify` (`{stage.verify !== undefined && (<span data-phase={stage.verify.phase} title={...stage.verify.step...stage.verify.command...} />)}`). Update `StagesList.test.tsx` cases that pass `verifyByStage` → set `verify` on the stage fixtures instead.
- `App.tsx`: delete `const verifyByStage = useMemo(() => computeVerifyIndicators(events), [events])` (line 166) and the `verifyByStage={verifyByStage}` prop passed to `StagesList` (line 605); also drop `computeVerifyIndicators` from the `feed-workspace` import (line 9).
- `feed-view-model.ts`: delete `computeVerifyIndicators`, `VerifyIndicator`, `VerifyIndicatorPhase`, their barrel exports in `feed-workspace/index.ts`, and their tests in `feed-view-model.test.ts`. **Keep** `mapVerifyEvent` (verify events still render as feed ROWS). `grep -rn "computeVerifyIndicators\|VerifyIndicator\|verifyByStage" pkg/web/dashboard/src` must return nothing.

- [ ] **Step 6: Run frontend suite + typecheck + lint**

Run: `cd pkg/web/dashboard && npx vitest run && npm run typecheck && npm run lint`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add pkg/server/stageview.go pkg/server/stageview_test.go pkg/web/dashboard/src/
git commit -m "feat: durable пер-стейдж AI-verify индикатор в /api/status (не из ленты)"
```

---

## Task 8: Build the bundle + live browser verification

**Files:**
- Modify: `pkg/web/dashboard/index.html`, `pkg/web/dashboard/assets/index-*.js` (Vite build output — regenerated, committed as the served bundle).

**Interfaces:** none (verification task).

- [ ] **Step 1: Build the dashboard bundle**

Run: `cd pkg/web/dashboard && npm run build`
Expected: fresh `assets/index-<hash>.js` + updated `index.html` (the server serves this committed bundle; see how commit `59df822` committed the rebuilt bundle).

- [ ] **Step 2: Build afm + run the repro flow**

Reuse the repro from root-cause analysis:

```bash
go build -o /tmp/afm-repro/afm ./cmd/afm
mkdir -p /tmp/afm-repro/work/.afm && cd /tmp/afm-repro/work
# flow.yaml: early(5 lines) -> noise(300 lines) -> hold(auto_run:false)
# .afm/config.yaml: docker.enabled:false, server.port:8791, open_browser:false
AFM_USE_DOCKER=0 /tmp/afm-repro/afm run flow.yaml   # (background)
```

- [ ] **Step 3: Drive the browser (Chrome DevTools MCP)**

- Navigate to `http://localhost:8791/`.
- Click the **`early`** stage (done). Assert the **Feed** tab body now shows `early line 1..5` (NOT "No events for this stage yet"). ← this is the fix.
- Confirm there is **no** `This stage | All` toggle.
- Click the **Full feed** tab (last tab). Assert it shows interleaved events including both `early` and `noise` lines, up to 1000.
- Verify the API directly: `curl -s 'localhost:8791/api/events?stage=early' | python3 -c "import json,sys;print(len(json.load(sys.stdin)))"` → 6-ish (>0); `curl -s localhost:8791/api/events | ...` → capped at 1000.
- **Verify-badge durability (Task 7):** on a flow with a `verify:` stage, run long enough (or synthesize >1000 later events) that the verify stage's `verify_result` is older than the global 1000-window; confirm the stage-list AI-verify badge is STILL shown (it now comes from `/api/status`, not the feed). `curl -s localhost:PORT/api/status | python3 -c "import json,sys;print([s.get('verify') for s in json.load(sys.stdin)['stages']])"` shows the durable verdict.

- [ ] **Step 4: Commit the bundle**

```bash
git add pkg/web/dashboard/index.html pkg/web/dashboard/assets/
git commit -m "build(dashboard): пересборка бандла — пер-стейдж Feed + Full feed"
```

---

## Task 9: Docs + changelog

**Files:**
- Modify: `CHANGELOG.md`, `pkg/web/dashboard/RELEASE_NOTES.md`, `docs/dashboard.md`

- [ ] **Step 1: CHANGELOG.md (English)** — a dated `## YYYY-MM-DD` section, newest at top, with a `### Fix:` describing: the per-stage feed cap (a completed stage always shows its own last-200 events), the removal of the `This stage/All` toggle, and the new `Full feed` tab (last 1000 events flow-wide).

- [ ] **Step 2: RELEASE_NOTES.md** — user-facing one-liner mirroring the changelog.

- [ ] **Step 3: docs/dashboard.md** — update the Feed section: Feed = current stage only; Full feed = whole flow, last 1000; caps (200 per stage / 1000 global).

- [ ] **Step 4: Commit**

```bash
git add CHANGELOG.md pkg/web/dashboard/RELEASE_NOTES.md docs/dashboard.md
git commit -m "docs: пер-стейдж Feed + вкладка Full feed"
```

---

## Self-Review

**Spec coverage:**
- "200 per-stage limits" → Task 1 (`maxStageReplayEvents = 200`, server) + Task 3 (`MAX_STAGE_EVENTS = 200`, client). ✓
- "Remove This stage / All toggle" → Task 4 (delete `use-feed-scope`, strip the switch). ✓
- "Full feed tab at the far-right end, last 1000 of whole flow" → Task 1 (global cap 1000) + Task 5 (`full-feed` view) + Task 6 (tab pushed last, renders global `events`). ✓
- "Feed tab shows only current stage, always" → Task 3 + Task 6 (Feed body fed by `useStageEvents`). ✓
- "Verify badge must not age out either" (user decision) → Task 7 (durable `StageView.Verify` from `notices.jsonl`; client derivation deleted). ✓

**Type consistency:** `maxReplayEvents`(1000)/`maxStageReplayEvents`(200) server; `MAX_EVENTS`(1000)/`MAX_STAGE_EVENTS`(200) client; `mergeCapped(history, live, cap)` used by both `use-event-feed` and `use-stage-events`; `WorkspaceView` includes `'full-feed'` everywhere it is switched (`isGlobalView`, `activeTabId`, `onSelectTab`, `reduceSync`, WorkspaceHeader guard). `useStageEvents(stageId, globalEvents)` signature matches its App.tsx call site.

**Open decisions (defaults chosen, flagged for review):**
1. **1000 = raw events**, not grouped bubbles (matches existing cap semantics).
2. **Feed with no stage selected** → empty hint "Select a stage to see its feed" (rare; auto-select normally fills it). Alternative would be to fall back to Full feed — rejected as surprising.
3. **Full feed excludes the note composer** (it's a global view with no single live stage) but keeps dialog-click navigation.
4. **Per-stage transport = `GET /api/events?stage=<id>`** (reuse `handleEvents`), not a new route — minimal new surface, mirrors the removed `/api/stages/{id}/log`.

**Codex review (2026-09-23) — resolutions:**
- **[blocker] stage-aware notices** — FIXED in Task 1 Step 3b (`reconstructNotices` scans the whole shared `notices.jsonl` with a stage-aware ring buffer) + regression test `TestHandleEvents_StageFilterSurvivesFlowWideNoticeNoise`.
- **[important] `maxLinesPerLog` raw-line approximation** — documented as accepted (Task 1 Step 5 caveat); no scan-until-N loop (YAGNI).
- **[important] `useStageEvents` race tests** — ADDED (Task 3: live-after-fetch, live-while-pending, cross-source dup, null-mid-flight).
- **[important] `showStageBadges` threading** — made explicit in Task 4 (`FeedGroupViewProps` gains the flag; both render paths tested).
- **[minor] `gen` on null** — FIXED (bump generation before the null early-return).
- **[minor] full-feed branch precedence** — made explicit in Task 6 (`cost → full-feed → feed → detail`).
- **[minor] `reconstructEventHistory` caller migration** — added to Task 1 as an explicit grep-and-update step.
- **[important] global consumers (`computeVerifyIndicators` / WS-refresh) still read the 1000-global array** — **RESOLVED per user decision (2026-09-23): fix it per-stage too.** WS-refresh stays as-is (it only needs the newest event's identity to trigger a `/api/status` poll; the 3s poll is a backstop). The AI-verify badge is moved OFF the event feed and computed **server-side, durably, per stage** from `notices.jsonl` (where `verify_started`/`verify_result` are already persisted via `stagefiles.AppendNotice`) into a new `StageView.Verify` field on `/api/status`. See **Task 7**. This closes the eviction class for the badge entirely.

**Codex review round 2 (2026-09-23, on the revised plan) — resolutions:**
- **[blocker] Task 7 badge could still age out** if it reused the 200-capped `reconstructNotices` — FIXED: Task 7 now specifies a dedicated **unbounded** `latestVerifyForStage` scan (keeps only the single latest verify notice, O(1) memory, selected by timestamp).
- **[important] Task 1 Step 3/3b signature mismatch** — FIXED: the Step 3 sample now calls `reconstructNotices(s.runDir, stageID)`.
- **[important] Task 7 conflicts with `TestStageView_JSONShape_NoVerifyField`** — added Task 7 Step 3b to update that lock test.
- **[important] Task 7 TS type migration** (`StagesList` imports `VerifyIndicator`/`verifyPhaseLabel`, `verifyByStage` prop) — made explicit with exact line refs; introduced the shared `StageVerify` type + `toVerifyView` normalizer.
- **[minor] ring memory claim / append-order** — corrected to `O(cap + distinct dialog keys)`; timestamp sort in `reconstructEventHistory` handles rare out-of-order appends.
- **[consistency] stale "verify indicators" references** in Architecture + the `maxReplayEvents` comment — removed. Task numbering 1–9 confirmed unique (build=8, docs=9).
