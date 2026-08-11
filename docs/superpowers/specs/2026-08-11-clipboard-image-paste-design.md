# Pasting a clipboard screenshot into agent-facing textareas — design

Date: 2026-08-11

## Context

The afm dashboard (`pkg/web/dashboard`) has four `<textarea>` fields where a
user writes text that ends up in front of the agent, all funneling through
plain-text-only endpoints today:

- `AgentNoteModal.tsx` — free-text "поправка агенту" note, sent via
  `reviseStage(stageId, note)`.
- `DialogChannel.tsx` — the free-text answer box (`customText`) and the
  per-line comment box on a pending question (`draft`), both sent via
  `answerDialog(...)`.
- `PlanPanel.tsx` — the per-line comment box on a plan review (`draft`),
  sent via `reviseStage(stageId, feedback)`.

There is currently no way to attach an image. A user who wants to point the
agent at a screenshot (a UI bug, a stack trace in a terminal window, a
diagram) has to describe it in words. The ask is: paste a screenshot
(Cmd+V) directly into any of these four fields and have it reach the agent.

### Why this isn't a real multimodal image block

All three surfaces above hand text to the agent by exactly one mechanism —
`cmd.Stdin = strings.NewReader(prompt)` in `pkg/executor/executor.go:535` —
a single plain string. `--output-format stream-json` (used for the CLI's
*output*) has no bearing on the *input* format, which is always plain text.

Worse, the most common of the three surfaces — answering a pending
question — never goes through that code path at all: an already-running
`claude`/`codex` process is sitting inside its own Bash tool call, polling
for `answer.json` (`while [ ! -f ... ]; do sleep 15; done && cat ...`,
baked into the prompt by `pkg/prompts/builder.go:91-92`). afm only creates
the file; the agent's own already-executing shell command reads it, and
the CLI wraps that stdout as a `tool_result` — a text block, not an image
content block — before sending it to the model. There is no point in this
path where afm's Go code constructs the API request at all.

So a genuine `{"type": "image", ...}` content block is only even
theoretically reachable for the first two surfaces (initial prompt / a
revise-triggered stage restart), and only by switching the executor to
`--input-format stream-json` and hand-assembling JSON messages — a
non-trivial change to `pkg/executor/executor.go` — while the third surface
(dialog answers, the most-used one) could never carry a real image block no
matter what. Splitting the delivery mechanism by surface would mean two
different behaviors ("the agent sees the picture directly here, but only if
it decides to open the file over there") for the same paste gesture, which
is confusing and not worth the executor rework for two out of three cases.

## Requirements

1. Cmd+V with an image on the clipboard, in any of the four textareas
   listed above, attaches the image — no separate "attach" step.
2. The agent must actually be able to see it. Since text is the only wire
   format available in every surface, the image is saved to disk and its
   path is referenced in the text; the agent reads the file itself with its
   own Read tool when it decides to.
3. The user sees an immediate visual confirmation of what was pasted
   (thumbnail), and can remove it before sending if it's the wrong image.
4. Multiple screenshots per message are supported.
5. No change to the wire contract of `revise` / `dialog/answer` — both stay
   plain-text endpoints; the image reference is just text within them.

## Rejected approaches

- **Real image content-block via `--input-format stream-json`:** covered
  above — only reachable for 2 of 3 surfaces, requires reworking
  `Executor.run`'s input transport, and produces inconsistent behavior
  between surfaces for what looks like the same user action. Rejected in
  favor of one uniform mechanism.
- **Base64 data URI inlined into the text:** works everywhere in principle
  (it's still just text), but bloats `events.jsonl`/`notices.jsonl`/
  `feedback.md` by tens of KB per screenshot, and CLI agents don't parse
  inline data URIs out of plain text any better than they parse a file
  path — the agent still has to notice and act on it, so the extra encoding
  buys nothing. Rejected for cost with no upside over a plain path.
- **Server-rendered thumbnail (GET endpoint serving the saved file back to
  the browser):** would let a reloaded page still show a thumbnail, but the
  answer/feedback history already renders as plain text (not through
  `MarkdownRenderer`), so a rendered thumbnail would only ever show during
  the current compose session anyway. A client-side `URL.createObjectURL`
  on the pasted blob gives the exact same compose-time preview with zero
  new backend surface. Rejected as unneeded for what's actually visible.

## Design

### Data flow

1. User pastes into a `PasteableTextarea` (see below). `onPaste` inspects
   `event.clipboardData.items`; if none are `kind === 'file'` with an
   `image/*` type, the handler does nothing and lets the normal text-paste
   proceed untouched.
2. If an image item is found: `event.preventDefault()`, then for each image
   item:
   - `URL.createObjectURL(blob)` gives an immediate local thumbnail — no
     network round-trip needed to show the user what they pasted.
   - `fetch('/api/stages/{id}/attachments', { method: 'POST', headers: {
     'Content-Type': blob.type }, body: blob })` uploads the raw bytes.
3. Backend (`handleUploadAttachment`, new, in `pkg/server/handlers.go`):
   - Validates `stageID` with the existing `isValidStageID`.
   - Validates `Content-Type` against an allow-list: `image/png`,
     `image/jpeg`, `image/webp`, `image/gif`. Anything else → `415`.
   - Wraps `r.Body` in `http.MaxBytesReader` at 10 MiB → oversized body
     surfaces as a read error → `413`.
   - Generates the filename itself — `paste-<uuid>.<ext>`, same
     `crypto/rand`-based id shape as `newUUID()` in
     `pkg/orchestrator/stagefiles/session.go` — the client never supplies a
     name, so path traversal isn't reachable by construction.
   - `MkdirAll`s `<runDir>/<stageID>/attachments/` and writes the file
     there — a sibling of the existing `attachments`-free stage directory
     that already holds `question.json`/`answer.json`/`dialog.jsonl`, so it
     inherits the same host/Docker visibility guarantees documented for
     `AFM_STAGE_DIR` (anchored to the afm-root regardless of `root_dir`).
   - Responds `{"path": "<absolute path>"}` (or the appropriate 4xx).
4. On a successful response, the frontend inserts `[Screenshot: <path>]\n`
   into the textarea's value at the caret position and marks that
   attachment "resolved" (tied to the exact substring inserted, so removal
   can find and strip it later). On failure, the attachment is dropped from
   the list, its object URL revoked, and a small transient inline message
   appears (same transient-state shape as the existing `flash`/`clicked`
   patterns in these components) — not a silent `console.error`, since
   paste is an implicit gesture the user needs feedback on.
5. From here on it's just text. `[Screenshot: /abs/path/to/paste-<uuid>.png]`
   rides inside the same string that already goes to `reviseStage` /
   `answerDialog`. The agent notices the path in its context (initial
   prompt, a resumed session's next turn, or a Bash `tool_result` it just
   read via `cat`) and, at its own discretion, reads the file with its Read
   tool.

### Components

**`PasteableTextarea`** (new, `pkg/web/dashboard/src/components/pasteable-textarea/`) —
a drop-in replacement for a plain `<textarea>`, used at all four call sites:

```tsx
type PasteableTextareaProps = {
  stageId: string
  value: string
  onChange: (value: string) => void
  placeholder?: string
  className?: string
  autoFocus?: boolean
  disabled?: boolean
  maxHeight?: number // default 400 — matches every existing call site today
  onKeyDown?: (e: KeyboardEvent<HTMLTextAreaElement>) => void
}
```

Internally it composes two things that today are wired by hand at each call
site: the existing `useAutoGrowTextarea(value, maxHeight)` hook for the ref,
and a new `useImagePaste(stageId)` hook for attachments. It renders a
`<PasteAttachments>` thumbnail strip (small images with a `✕` remove
button, plus the transient error message) above the `<textarea>` itself.
Callers just swap their current `<textarea ref={...} value={...}
onChange={...} .../>` for `<PasteableTextarea stageId={...} value={...}
onChange={...} .../>` — no other changes to the four components' own
state management (`note`, `customText`, `draft` stay exactly as they are,
since `onChange` still just receives the new string value).

**`useImagePaste(stageId)`** (new hook, internal to `PasteableTextarea`) —
owns the attachment list (`{id, previewUrl, path, uploading, error}[]`),
the paste handler (steps 1-4 above), and `removeAttachment(id)` (revokes the
object URL, strips the `[Screenshot: ...]` substring it had inserted from
the current value, drops it from the list).

### Backend

- New route in `routeStages` (`pkg/server/server.go`): `HasSuffix(path,
  "/attachments") && r.Method == http.MethodPost` → `handleUploadAttachment`.
- No GET/serving route — nothing on the server needs to read the file back
  for the browser; the compose-time preview is entirely client-side.
- No cleanup of orphaned files if a draft is discarded before sending —
  consistent with the existing best-effort treatment of other non-critical
  stage files (e.g. `dialog.jsonl` appends); not part of the durable event
  log, not worth a cleanup pass for an MVP.

## Testing

- Go (`pkg/server/handlers_test.go`): successful upload writes the expected
  file under `<runDir>/<stageID>/attachments/` and returns its path;
  rejected MIME type → 415; oversized body → 413; response path is
  actually readable at the returned location.
- Frontend (`use-image-paste.test.ts` / `PasteableTextarea.test.tsx`):
  pasting a fabricated `ClipboardEvent` with an `image/png` item calls
  `preventDefault`, uploads, and appends `[Screenshot: ...]` to the value
  via `onChange`; pasting an event with only `text/plain` items does not
  call `preventDefault` and leaves `onChange` untouched; removing an
  attachment strips exactly its own substring and revokes its object URL;
  a mocked 415/413 response drops the attachment and does not touch the
  textarea value.
- Manual verification in a real browser: paste a real screenshot into each
  of the four fields, confirm the thumbnail appears immediately, confirm
  the inserted path is correct and Cmd+Enter-submits still work where they
  already do (`DialogChannel`'s custom-answer and comment boxes); paste a
  non-image (plain text) and confirm normal text paste still works
  unaffected; paste an oversized/corrupt image and confirm the inline error
  appears and the field is left usable.
