# Implementation plan — Feed dialog text, click-to-navigate, block markdown

Spec: `docs/superpowers/specs/2026-09-15-feed-dialog-and-markdown-design.md`

## Global Constraints

- Presentation-only: no change to the FSM, `events.jsonl` schema, the dialog file
  protocol, or `/api/stages/<id>/dialog`. The two new feed messages
  (`dialog_question`/`dialog_answer`) ride the existing `notices.jsonl` non-FSM UI
  channel and `bus.Event.Data` (already `any`), mirroring `auto_answered` exactly
  — no `events.jsonl`/FSM change.
- Sourcing rule: feed dialog text/ids come from real dialog records (`mcp.Entry`:
  `ID`/`Question`/`Answer`), never from order-correlating FSM transitions.
- Go: `gofmt`; run `bin/golangci-lint run ./<pkg>/` before committing each Go
  task; no `go.mod` version bump.
- Frontend gate: `npm test` (vitest) + `npm run typecheck` (no lint script).
  After ALL frontend tasks, rebuild the embedded bundle (`npm run build`) and
  commit `pkg/web/dashboard/assets/*` + `index.html`; CSS source is
  `pkg/web/dashboard/skins/` (mirrored to `public/skins/` by `npm run
  sync-skins` — run it if any skin CSS changes).
- Commits in Russian, no `Co-Authored-By`.
- Each task is independently testable; keep the per-line comment/anchor contract
  intact throughout (markdown blocks anchor to their FIRST source line).

## Tasks

> **Anti-duplication + import-cycle fixes (codex plan review).** (1) The shared
> helper/payload lives in **`pkg/mcp`** (imported by BOTH `dialog_poller.go` and
> `handlers.go` — no import cycle; putting it in `pkg/server` and importing from
> the orchestrator WOULD cycle). (2) `EventAskUser` is ALREADY published live and
> replayed; adding `dialog_question` ALONGSIDE it (not replacing) is correct
> ONLY because the client stops rendering `ask_user`/`user_answered` as feed rows
> (T4) — otherwise every question would show twice. Backend keeps emitting
> `ask_user` (status/other consumers) untouched; the feed simply ignores it.
> (3) Live vs. notice dedup is by `type|stageId|JSON(payload)` for seq-less
> events, so the live `Publish` Data and the `AppendNotice` Data MUST be the
> byte-identical map — built by one shared constructor (mirrors how
> `auto_answered` already dedupes).

### T1 — `dialogSnippet` + shared notice payload (Go, `pkg/mcp`)
In `pkg/mcp` (cycle-safe leaf, imported by both sites): `DialogSnippet(text
string) string` — first non-empty line, trimmed, ≤120-rune ellipsized, rune-safe,
`""` for empty. `DialogFeedNotice(phase, id, title string) map[string]any` — the
ONE constructor for the notice/live payload (`{phase, id, title}`), used by BOTH
the live `Publish` Data and the `AppendNotice` Data at every site so their JSON is
identical (dedup depends on it).
**Tests:** snippet first-line/rune-truncation/empty; `DialogFeedNotice` returns
the exact keys.

### T2 — `dialog_question` at BOTH interactive-question sites (Go)
`pkg/orchestrator/dialog_poller.go` publishes `EventAskUser` in TWO places: the
normal path (~line 224) AND `giveUpOnMalformedQuestion` (~line 416). Since T4
makes the client drop `ask_user`, a malformed interactive question would vanish
from the feed unless BOTH emit `dialog_question`. Factor a helper
`(o *Orchestrator) publishDialogQuestion(stageID, phase, id, questionText)` that
does `o.ui.Publish(bus.Event{Type:"dialog_question", StageID, Data:
mcp.DialogFeedNotice(phase,id,mcp.DialogSnippet(questionText))})` +
`stagefiles.AppendNotice(runDir, stageID, "dialog_question", <same map>)` — one
map value passed to both (identical JSON for dedup). Existing `EventAskUser`
publishes and the FSM are untouched.

**Exactly-once invariant (critical) — trace the `processed` lifecycle first.**
`giveUpOnMalformedQuestion` writes a valid STUB question, which the NEXT poll
re-detects via the NORMAL interactive branch; `/api/events` history is NOT
deduplicated, so any double emission survives replay. The implementer must FIRST
read `pollQuestions` + `giveUpOnMalformedQuestion` + the `processed` map lifecycle,
then place a SINGLE `publishDialogQuestion` call at the point that owns the
`processed[key]=true` transition for a freshly-surfaced question — the same gate
`EventAskUser` uses — so the malformed→stub→re-poll sequence yields exactly one
emission. Do NOT blindly call it from both sites: if the malformed fallback marks
`processed`, emit there; if it defers to the normal re-poll, emit only there. The
double-poll test is the guarantee, not the placement prose.
**Tests:** a normal question emits exactly one `dialog_question` (live+notice); a
malformed question through `giveUpOnMalformedQuestion` then re-polled emits EXACTLY
ONE `dialog_question` total (poll twice, assert count==1); identical
`{phase,id,title}` on live and notice.

### T3 — `dialog_answer` at the human-answer site (Go)
`pkg/server/handlers.go`, `handleDialogAnswer` (has `runDir` + `req.Answer`, and
already appends dialog.jsonl): after the answer is authorized/persisted, emit
`dialog_answer` — `AppendNotice(runDir, stageID, "dialog_answer",
mcp.DialogFeedNotice(phase, id, mcp.DialogSnippet(answer)))` AND publish it live
via `s.uiBus.Publish` (confirmed: `Server.uiBus` is the same `*bus.UIBus` wired to
`/ws` in production — `pkg/server/server.go:82`). Live publish is REQUIRED, not
optional: `/api/events` is fetched only on mount/reconnect, so AppendNotice alone
would leave the answer invisible for the rest of a healthy session. Pass the one
map value to both. Auto-answers untouched (existing `auto_answered`).
**Tests:** `handleDialogAnswer` publishes a live `dialog_answer` AND writes the
notice with identical `{phase,id,title}`; auto-answer path unaffected.

### T4 — Feed ignores old dialog events, renders the new ones (TS only)
- `types/afm-event.ts`: add `dialog_question`/`dialog_answer` to the known types
  (live + notice lists).
- `feed-view-model.ts`: `ask_user`/`user_answered` → produce NO feed row (filter
  them out — prevents the double row alongside `dialog_question`/`dialog_answer`).
  `dialog_question` → `{actor:'agent', kind:'dialog', text:data.title}`;
  `dialog_answer` → `{actor:'user', kind:'message', text:data.title}`; both carry
  `phase`/`id` + `navigable:true`. Grouping keeps navigable items individually
  addressable. No `transitionToFeedEvents`/server change here (backend still
  emits ask_user; the client just ignores it).
**Tests:** ask_user/user_answered yield no feed item; dialog_question/dialog_answer
render title + navigable + phase/id; a live event and its notice replay dedupe to
ONE row (identical payload).

### T4b — Dedupe live feed ingestion in BOTH arrival orders (TS)
`hooks/use-event-feed/use-event-feed.ts`. Today `mergeHistory` drops a live event
when history (from `/api/events`) arrives LATER, but `onmessage` blindly appends a
live event when history already arrived — so a seq-less notice returned by
`/api/events` BEFORE its queued live WS message produces TWO rows (a pre-existing
race that also affects `auto_answered`, now hit reliably by `dialog_*`).

Two traps a naive fix hits (both must be avoided):
- **Shared-seq collision:** one FSM transition emits BOTH `stage_status_changed`
  AND a semantic event (e.g. `retry_scheduled`) with the SAME `seq`. Today
  `dedupeKey` is just `seq:${seq}`, so deduping onmessage by it would drop the
  semantic event live. → Change `dedupeKey` to `seq:${seq}|${type}` (also
  hardens `mergeHistory`; same-seq+same-type still dedupes live-vs-replay).
- **Collapsing legit repeats:** blanket content-dedup of seq-less events would
  merge legitimately-repeated `agent_action`/identical `script_output` lines. →
  Restrict live-ingestion content-dedup to ONLY `dialog_question`/`dialog_answer`
  (a small allowlist); all other seq-less types keep appending as today.
So: `onmessage` skips a `dialog_question`/`dialog_answer` whose content
`dedupeKey` is already present; everything else is unchanged. Reused-id identical
`dialog_*` emissions collapse to one row — acceptable (dialog tab is source of
truth), documented.
**Tests:** live-then-history AND history-then-live both yield ONE row for one
`dialog_*` emission; a `stage_status_changed`+`retry_scheduled` sharing a seq both
survive live; two identical seq-less `agent_action` lines both survive.

### T5 — FeedWorkspace click → onOpenDialog (TS)
`components/feed-workspace/FeedWorkspace.tsx`: a navigable item renders as a
keyboard-focusable `<button>` calling `onOpenDialog(stageId, phase, id)` (new
optional prop); non-navigable items render exactly as today. Visible focus/hover
affordance via existing tokens.
**Tests:** clicking a navigable item fires `onOpenDialog` with the right args;
non-navigable items are not buttons; keyboard activation works.

### T6 — App wires navigation to the dialog view (TS)
`app/App.tsx`: implement `onOpenDialog(stageId, phase, id)` → `setSelectedStageId`
+ open the stage's dialog view (live question if `status ===
'awaiting_user_input'`, else `openHistory('dialog-history')`); thread a
`scrollToDialogTarget = {phase,id}` down to `DialogChannel`.

`DialogChannel.tsx`: **the `/dialog` fetch is async, so the target element does
not exist on mount** — a one-shot scroll effect would systematically fail for
answered-item navigation. Instead: RETAIN the `{phase,id}` target in state and
re-attempt the scroll whenever `entries`/`pending` change; on a successful scroll
to `qa-<phase>-<id>` (or the pending question) clear the target; also clear it
when the selected stage/target changes (stale-guard). A target whose anchor never
appears is a harmless no-op, but the common case (anchor appears after the fetch)
MUST scroll.
**Tests (App):** clicking a feed question for a waiting stage selects it + shows
the live question; for a finished stage shows dialog history. **DialogChannel
delayed-fetch test:** with the target set before entries load, `scrollIntoView`
is called AFTER the matching `qa-<phase>-<id>` renders (not only "no throw when
absent").

### T7 — Block markdown via markdown-it token `.map`, shared by BOTH consumers (TS)
`components/plan-panel/markdown.ts`. Two consumers segment blocks today:
`parseLineBlocks` (DialogChannel question) AND `PlanPanel.tsx`'s `parseReviewPlan`
(its own loop calling `nextLineBlock(lines, i)` at line ~431, interleaving
`isSpecialSection`/`isHeading2` wrapping). Both must switch together or the build
breaks / plan markdown stays broken.

- Add `blockSpans(text): LineBlock[]` — `md.parse(text, {})`, iterate tokens
  selecting only `token.level === 0 && token.map !== null` (top-level only, so
  nested list/blockquote CHILDREN are not double-rendered); for each, slice
  `lines[map[0]..map[1]]`, `md.render` the slice, emit `LineBlock{ line: map[0]+1,
  html }`. Correctly segments CommonMark (lazy continuations, loose lists,
  `para`⏎`- item` split vs `para`⏎`2. item` one paragraph) — prefix scanning
  cannot. Lines not covered by any top-level block token (blank separators) are
  skipped.
- `parseLineBlocks` returns `blockSpans(text)` (same `LineBlock[]` signature →
  DialogChannel unchanged).
- Refactor `PlanPanel.parseReviewPlan` to iterate `blockSpans(text)` instead of
  the manual `nextLineBlock`+index loop, keeping its special-section wrapping by
  testing each span's FIRST source line with `isSpecialSection`/`isHeading2`.
  Remove `nextLineBlock` (only these two used it) or keep it as a thin wrapper —
  no other callers.
- Anchor stays the block's first source line, so per-line comments (PlanPanel)
  and feedback quoting (DialogChannel) keep working.
**Tests:** `markdown.test.ts` — ordered/bulleted/nested/LOOSE list, lazy
continuation (`- a`⏎`b`), `para`⏎`- item` (two blocks) vs `para`⏎`2. item` (one),
blockquote+continuation, blockquote→paragraph, wrapped paragraph, code fence +
table still one block, heading/blank single lines — assert HTML AND `block.line`
== first source line. PLUS a PlanPanel component-level review test (special
sections + a list render correctly and stay commentable), not only pure
`parseLineBlocks` tests.

### T9 — Remove the redundant header attention dot (TS)
User request: the amber dot right of the connection status ("Offline"/"Agent
online") is redundant — the beacon tab, favicon pulse, title flash, desktop
notification, and stage badges already signal attention. `GlobalHeader.tsx:132`
`{attention && <span className="attention-dot" aria-label="Action needed" />}` —
remove it. Then remove the now-dead `attention` prop from `GlobalHeaderProps` and
its pass-through in `App.tsx` IF it becomes unused there (App's title-flash /
favicon / desktop-notification logic lives in separate hooks — do NOT touch
those). Drop the `.attention-dot` CSS (source `skins/…`, then `sync-skins`) and
any test asserting the dot's presence.
**Tests:** GlobalHeader no longer renders `.attention-dot` / `aria-label="Action
needed"`; existing header tests still pass; typecheck clean (no unused prop).

### T8 — Rebuild embedded bundle + full gate
After all frontend tasks (T4, T4b, T5–T7, T9): `npm test` + `npm run typecheck`
green; `npm run build`; commit
`pkg/web/dashboard/assets/*` + `index.html` (+ `sync-skins` if CSS changed).
Then `make lint` (0, darwin+linux) and `go test ./... -race`.

## Review + verification (after tasks)

- codex review of the whole branch diff; fix findings; scoped re-review.
- Live run (Docker disabled, mock interactive agent that asks a question and
  reads the answer): verify (1) user answer text in the feed, (2) agent question
  title in the feed + click opens the dialog tab scrolled to it (both waiting and
  answered states), (3) a plan/question with ordered list + nested list +
  blockquote + wrapped paragraph renders correctly and is still commentable per
  block.
