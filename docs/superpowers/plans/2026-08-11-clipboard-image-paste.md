# Clipboard image paste (PasteableTextarea) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a user paste a clipboard screenshot (Cmd+V) into any of the four agent-facing textareas in the afm dashboard (AgentNoteModal's note, DialogChannel's custom-answer and line-comment boxes, PlanPanel's line-comment box), uploading it to the stage directory and inserting a `[Screenshot: <path>]` text reference that the agent can read via its own Read tool.

**Architecture:** A new Go endpoint `POST /api/stages/{id}/attachments` persists uploaded image bytes to `<runDir>/<stageID>/attachments/paste-<id>.<ext>` and returns the absolute path. A new `useImagePaste` hook wraps a textarea's paste handler: on an image paste it uploads via this endpoint and splices `[Screenshot: <path>]\n` into the controlled value at the caret. A new `PasteableTextarea` component composes this hook with the existing `useAutoGrowTextarea` hook behind one drop-in `<textarea>` replacement, used at all four call sites. No changes to the wire contract of `revise`/`dialog/answer` — the image reference is just text riding inside those existing plain-text payloads.

**Tech Stack:** Go (`net/http`, stdlib only) for the backend; React + TypeScript + Vitest/@testing-library/react for the dashboard (`pkg/web/dashboard`).

## Global Constraints

- Accepted image types: `image/png`, `image/jpeg`, `image/webp`, `image/gif`. Anything else → `415`.
- Max upload size: 10 MiB (10,485,760 bytes). Larger → `413`.
- No GET endpoint for serving attachments back — previews are client-side only (`URL.createObjectURL`), never persisted server-side for display.
- No change to `reviseStage`/`answerDialog`'s request shape — attachments ride as plain text inside the existing `feedback`/`answer` string fields.
- Commit messages must be in Russian (per repo convention — see recent `git log`). No `Co-Authored-By` trailers.
- After each task, lint must be clean: run `make lint` (Go) and, for frontend tasks, `npm run typecheck` inside `pkg/web/dashboard`.

---

### Task 1: Backend — upload endpoint

**Files:**
- Create: `pkg/server/attachments.go`
- Create: `pkg/server/attachments_test.go`
- Modify: `pkg/server/server.go:265-293` (add a route case to `routeStages`)

**Interfaces:**
- Consumes: `s.runDir` (existing `Server` field, already used identically by `handlePlan`/`handleLog`/`handleDialogGet` in `pkg/server/handlers.go`), `extractStageID`/`isValidStageID` (existing helpers, `pkg/server/handlers.go:518-533`).
- Produces: `POST /api/stages/{stageID}/attachments` — request: `Content-Type: image/png|image/jpeg|image/webp|image/gif`, raw image bytes as body (no JSON wrapper, no multipart). Response: `200 {"path": "<absolute path on disk>"}`; `400` invalid stage id or empty body; `415` unsupported `Content-Type`; `413` body over 10 MiB. This is consumed by Task 2's `uploadAttachment` client function.

- [ ] **Step 1: Write the failing tests**

Create `pkg/server/attachments_test.go`:

```go
package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestHandleUploadAttachment_Success(t *testing.T) {
	srv, runDir := setupTestServer(t)

	body := bytes.Repeat([]byte{0xFF}, 128)
	req := httptest.NewRequest(http.MethodPost, "/api/stages/"+testStageID+"/attachments", bytes.NewReader(body))
	req.Header.Set("Content-Type", "image/png")
	w := httptest.NewRecorder()

	srv.handleUploadAttachment(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200, body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Path == "" {
		t.Fatal("response path is empty")
	}
	wantDir := filepath.Join(runDir, testStageID, "attachments")
	if filepath.Dir(resp.Path) != wantDir {
		t.Errorf("path dir: got %q, want %q", filepath.Dir(resp.Path), wantDir)
	}

	written, err := os.ReadFile(resp.Path)
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if !bytes.Equal(written, body) {
		t.Error("written file content does not match uploaded body")
	}
}

func TestHandleUploadAttachment_RejectsUnsupportedType(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/stages/"+testStageID+"/attachments", bytes.NewReader([]byte("not an image")))
	req.Header.Set("Content-Type", "application/pdf")
	w := httptest.NewRecorder()

	srv.handleUploadAttachment(w, req)

	if w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("status: got %d, want 415", w.Code)
	}
}

func TestHandleUploadAttachment_RejectsOversizedBody(t *testing.T) {
	srv, _ := setupTestServer(t)

	body := bytes.Repeat([]byte{0x00}, maxAttachmentBytes+1)
	req := httptest.NewRequest(http.MethodPost, "/api/stages/"+testStageID+"/attachments", bytes.NewReader(body))
	req.Header.Set("Content-Type", "image/png")
	w := httptest.NewRecorder()

	srv.handleUploadAttachment(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status: got %d, want 413", w.Code)
	}
}

func TestHandleUploadAttachment_RejectsInvalidStageID(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/stages/../escape/attachments", bytes.NewReader([]byte{0x01}))
	req.Header.Set("Content-Type", "image/png")
	w := httptest.NewRecorder()

	srv.handleUploadAttachment(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", w.Code)
	}
}

func TestHandleUploadAttachment_TwoUploadsGetDistinctFilenames(t *testing.T) {
	srv, _ := setupTestServer(t)

	upload := func() string {
		req := httptest.NewRequest(http.MethodPost, "/api/stages/"+testStageID+"/attachments", bytes.NewReader([]byte{0x01, 0x02}))
		req.Header.Set("Content-Type", "image/png")
		w := httptest.NewRecorder()
		srv.handleUploadAttachment(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status: got %d, want 200", w.Code)
		}
		var resp struct {
			Path string `json:"path"`
		}
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return resp.Path
	}

	p1 := upload()
	p2 := upload()
	if p1 == p2 {
		t.Errorf("two uploads got the same path: %q", p1)
	}
}

func TestHandleUploadAttachment_RoutedThroughMux(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/stages/"+testStageID+"/attachments", bytes.NewReader([]byte{0x01}))
	req.Header.Set("Content-Type", "image/png")
	w := httptest.NewRecorder()

	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200, body=%s", w.Code, w.Body.String())
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go test ./pkg/server/... -run TestHandleUploadAttachment -v`
Expected: FAIL — `srv.handleUploadAttachment` and `maxAttachmentBytes` undefined (compile error).

- [ ] **Step 3: Implement the handler**

Create `pkg/server/attachments.go`:

```go
package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// maxAttachmentBytes caps a single pasted-image upload. 10 MiB comfortably
// covers a full-screen macOS/browser screenshot with room to spare, while
// still bounding worst-case disk usage per paste.
const maxAttachmentBytes = 10 << 20

// allowedAttachmentTypes maps an accepted Content-Type to the file extension
// used when persisting the upload. afm only needs to accept clipboard
// screenshots through this endpoint, not arbitrary file types.
var allowedAttachmentTypes = map[string]string{
	"image/png":  ".png",
	"image/jpeg": ".jpg",
	"image/webp": ".webp",
	"image/gif":  ".gif",
}

// handleUploadAttachment saves a pasted clipboard image to
// <runDir>/<stageID>/attachments/paste-<id>.<ext> and returns its absolute
// path as {"path": "..."}. The path is later embedded as plain text
// ("[Screenshot: <path>]") in a revise/dialog-answer/note payload by the
// frontend — this handler has no awareness of where the path ends up, it
// only persists bytes and hands back a location for the agent to Read.
func (s *Server) handleUploadAttachment(w http.ResponseWriter, r *http.Request) {
	stageID := extractStageID(r.URL.Path, "/api/stages/", "/attachments")
	if !isValidStageID(stageID) {
		http.Error(w, "invalid stage id", http.StatusBadRequest)
		return
	}

	ext, ok := allowedAttachmentTypes[r.Header.Get("Content-Type")]
	if !ok {
		http.Error(w, "unsupported image type", http.StatusUnsupportedMediaType)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxAttachmentBytes)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "attachment too large", http.StatusRequestEntityTooLarge)
		return
	}
	if len(data) == 0 {
		http.Error(w, "empty attachment", http.StatusBadRequest)
		return
	}

	attachmentsDir := filepath.Join(s.runDir, stageID, "attachments")
	if err := os.MkdirAll(attachmentsDir, 0755); err != nil {
		http.Error(w, "mkdir attachments: "+err.Error(), http.StatusInternalServerError)
		return
	}

	name := "paste-" + randomAttachmentID() + ext
	path := filepath.Join(attachmentsDir, name)
	if err := os.WriteFile(path, data, 0644); err != nil {
		http.Error(w, "write attachment: "+err.Error(), http.StatusInternalServerError)
		return
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"path": absPath})
}

// randomAttachmentID mirrors newRunID's random-suffix shape (cmd/afm/run.go):
// 8 random bytes as hex, generated fresh per upload so concurrent pastes in
// the same stage never collide on a filename.
func randomAttachmentID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
```

Modify `pkg/server/server.go` — in `routeStages` (currently lines 265-293), add a case right after the `/skip-hook` case (after line 283, before the `/dialog` GET case):

```go
	case strings.HasSuffix(path, "/skip-hook") && r.Method == http.MethodPost:
		s.handleSkipHook(w, r)
	case strings.HasSuffix(path, "/attachments") && r.Method == http.MethodPost:
		s.handleUploadAttachment(w, r)
	case strings.HasSuffix(path, "/dialog") && r.Method == http.MethodGet:
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go test ./pkg/server/... -run TestHandleUploadAttachment -v`
Expected: PASS (all 6 subtests).

- [ ] **Step 5: Lint**

Run: `cd /Users/alexander.kopichin/work/personal/afm && make lint`
Expected: `0 issues.` (G304/G306 are already excluded project-wide in `.golangci.yml`, so no `nolint` comments are needed for the variable-path `os.WriteFile`/`os.MkdirAll` calls above.)

- [ ] **Step 6: Commit**

```bash
cd /Users/alexander.kopichin/work/personal/afm
git add pkg/server/attachments.go pkg/server/attachments_test.go pkg/server/server.go
git commit -m "$(cat <<'EOF'
feat(server): добавляем эндпоинт загрузки вставленных скриншотов

POST /api/stages/{id}/attachments сохраняет байты картинки в
<runDir>/<stageID>/attachments/paste-<id>.<ext> и отдаёт абсолютный путь.
Путь дальше просто текст в revise/dialog-answer — агент сам решает,
читать ли файл.
EOF
)"
```

---

### Task 2: Frontend — API client + `useImagePaste` hook

**Files:**
- Modify: `pkg/web/dashboard/src/api/run-client.ts` (add `uploadAttachment`, `AttachmentUploadError`)
- Create: `pkg/web/dashboard/src/hooks/use-image-paste/use-image-paste.ts`
- Create: `pkg/web/dashboard/src/hooks/use-image-paste/use-image-paste.test.ts`
- Create: `pkg/web/dashboard/src/hooks/use-image-paste/index.ts`
- Modify: `pkg/web/dashboard/src/test/setup.ts` (stub `URL.createObjectURL`/`revokeObjectURL` — jsdom doesn't implement them)

**Interfaces:**
- Consumes: Task 1's `POST /api/stages/{id}/attachments` endpoint.
- Produces:
  - `uploadAttachment(stageId: string, file: Blob): Promise<{ path: string }>` and `class AttachmentUploadError extends Error { status: number }`, both exported from `pkg/web/dashboard/src/api/run-client.ts`.
  - `useImagePaste(stageId: string, value: string, onChange: (value: string) => void): { nodeRef: React.MutableRefObject<HTMLTextAreaElement | null>; attachments: PasteAttachment[]; uploadError: string | null; onPaste: (event: React.ClipboardEvent<HTMLTextAreaElement>) => void; removeAttachment: (id: string) => void }` exported from `pkg/web/dashboard/src/hooks/use-image-paste`, where `PasteAttachment = { id: string; previewUrl: string; uploading: boolean }`. This is consumed by Task 3's `PasteableTextarea`.

- [ ] **Step 1: Stub `URL.createObjectURL`/`revokeObjectURL` in the shared test setup**

Modify `pkg/web/dashboard/src/test/setup.ts`, appending at the end of the file:

```ts

// jsdom does not implement URL.createObjectURL/revokeObjectURL (used by
// useImagePaste for an instant local thumbnail of a pasted screenshot before
// the upload resolves) — stub them so paste-related tests don't crash.
if (typeof URL.createObjectURL !== 'function') {
  URL.createObjectURL = (): string => 'blob:mock-url'
}
if (typeof URL.revokeObjectURL !== 'function') {
  URL.revokeObjectURL = (): void => {
    /* no-op */
  }
}
```

- [ ] **Step 2: Add the API client functions (no dedicated test file)**

`run-client.ts`'s five existing functions (`approveStage`, `reviseStage`, `retryStage`, `retryHookStage`, `skipHookStage`, `answerDialog`, `cancelDialog`) have no direct unit tests today — they're exercised indirectly through `DialogChannel.test.tsx`/`PlanPanel.test.tsx`'s mocked-`fetch` assertions. `uploadAttachment` follows the same convention: it gets full coverage from this task's `useImagePaste` tests (which mock it directly) and Task 3's `PasteableTextarea` tests (which exercise it for real against a mocked `fetch`), so no separate `run-client.test.ts` is added here.

Modify `pkg/web/dashboard/src/api/run-client.ts`, appending at the end of the file:

```ts

export class AttachmentUploadError extends Error {
  status: number

  constructor(status: number) {
    super(`upload attachment failed with status ${status}`)
    this.status = status
  }
}

export async function uploadAttachment(stageId: string, file: Blob): Promise<{ path: string }> {
  const url = stageUrl(stageId, 'attachments')
  const response = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': file.type },
    body: file,
  })

  if (!response.ok) {
    throw new AttachmentUploadError(response.status)
  }

  return (await response.json()) as { path: string }
}
```

- [ ] **Step 3: Write the failing hook tests**

Create `pkg/web/dashboard/src/hooks/use-image-paste/use-image-paste.test.ts`:

```ts
import { act, renderHook } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { ClipboardEvent } from 'react'
import { AttachmentUploadError, uploadAttachment } from '../../api/run-client'
import { useImagePaste } from './use-image-paste'

vi.mock('../../api/run-client', async () => {
  const actual = await vi.importActual<typeof import('../../api/run-client')>('../../api/run-client')
  return { ...actual, uploadAttachment: vi.fn() }
})

const mockUpload = uploadAttachment as unknown as ReturnType<typeof vi.fn>

function makeImageItem(type = 'image/png'): DataTransferItem {
  const file = new File([new Uint8Array([1, 2, 3])], 'paste.png', { type })
  return { kind: 'file', type, getAsFile: () => file } as unknown as DataTransferItem
}

function makeTextItem(): DataTransferItem {
  return { kind: 'string', type: 'text/plain', getAsFile: () => null } as unknown as DataTransferItem
}

function makePasteEvent(items: DataTransferItem[], selectionStart = 0): ClipboardEvent<HTMLTextAreaElement> {
  return {
    clipboardData: { items },
    currentTarget: { selectionStart },
    preventDefault: vi.fn(),
  } as unknown as ClipboardEvent<HTMLTextAreaElement>
}

describe('useImagePaste', () => {
  beforeEach(() => {
    mockUpload.mockReset()
    vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:mock-url')
    vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {})
  })

  it('ignores a paste with only text items', () => {
    const onChange = vi.fn()
    const { result } = renderHook(() => useImagePaste('s1', '', onChange))
    const event = makePasteEvent([makeTextItem()])

    act(() => {
      result.current.onPaste(event)
    })

    expect(event.preventDefault).not.toHaveBeenCalled()
    expect(onChange).not.toHaveBeenCalled()
    expect(mockUpload).not.toHaveBeenCalled()
  })

  it('uploads a pasted image and inserts a Screenshot reference at the caret', async () => {
    mockUpload.mockResolvedValue({ path: '/afm/run/s1/attachments/paste-1.png' })
    const onChange = vi.fn()
    const { result } = renderHook(() => useImagePaste('s1', 'hello', onChange))
    const event = makePasteEvent([makeImageItem()], 5)

    await act(async () => {
      await result.current.onPaste(event)
    })

    expect(event.preventDefault).toHaveBeenCalled()
    expect(onChange).toHaveBeenCalledWith('hello[Screenshot: /afm/run/s1/attachments/paste-1.png]\n')
  })

  it('shows a size-specific error and does not call onChange when the upload is too large', async () => {
    mockUpload.mockRejectedValue(new AttachmentUploadError(413))
    const onChange = vi.fn()
    const { result } = renderHook(() => useImagePaste('s1', '', onChange))
    const event = makePasteEvent([makeImageItem()], 0)

    await act(async () => {
      await result.current.onPaste(event)
    })

    expect(onChange).not.toHaveBeenCalled()
    expect(result.current.uploadError).toBe('Image too large (max 10 MB)')
    expect(result.current.attachments).toHaveLength(0)
  })

  it('shows an unsupported-type error for a 415 response', async () => {
    mockUpload.mockRejectedValue(new AttachmentUploadError(415))
    const onChange = vi.fn()
    const { result } = renderHook(() => useImagePaste('s1', '', onChange))
    const event = makePasteEvent([makeImageItem()], 0)

    await act(async () => {
      await result.current.onPaste(event)
    })

    expect(result.current.uploadError).toBe('Unsupported image type')
  })

  it('removeAttachment strips exactly the inserted substring for a resolved attachment', async () => {
    mockUpload.mockResolvedValue({ path: '/x/paste-1.png' })
    let value = ''
    const onChange = vi.fn((next: string) => {
      value = next
    })
    const { result, rerender } = renderHook(({ v }) => useImagePaste('s1', v, onChange), {
      initialProps: { v: value },
    })
    const event = makePasteEvent([makeImageItem()], 0)

    await act(async () => {
      await result.current.onPaste(event)
    })
    rerender({ v: value })

    expect(value).toBe('[Screenshot: /x/paste-1.png]\n')
    expect(result.current.attachments).toHaveLength(1)

    act(() => {
      result.current.removeAttachment(result.current.attachments[0].id)
    })

    expect(value).toBe('')
  })

  it('removing an in-flight attachment prevents its text from being inserted once the upload resolves', async () => {
    let resolveUpload: (value: { path: string }) => void = () => {}
    mockUpload.mockImplementation(
      () =>
        new Promise((resolve) => {
          resolveUpload = resolve
        }),
    )
    const onChange = vi.fn()
    const { result } = renderHook(() => useImagePaste('s1', '', onChange))
    const event = makePasteEvent([makeImageItem()], 0)

    let pastePromise: Promise<void> | undefined
    act(() => {
      pastePromise = result.current.onPaste(event) as unknown as Promise<void>
    })
    expect(result.current.attachments).toHaveLength(1)

    act(() => {
      result.current.removeAttachment(result.current.attachments[0].id)
    })
    expect(result.current.attachments).toHaveLength(0)

    await act(async () => {
      resolveUpload({ path: '/x/paste-1.png' })
      await pastePromise
    })

    expect(onChange).not.toHaveBeenCalled()
  })

  it('inserts two pasted images in order', async () => {
    mockUpload.mockResolvedValueOnce({ path: '/x/paste-1.png' }).mockResolvedValueOnce({ path: '/x/paste-2.png' })
    const onChange = vi.fn()
    const { result } = renderHook(() => useImagePaste('s1', '', onChange))
    const event = makePasteEvent([makeImageItem(), makeImageItem()], 0)

    await act(async () => {
      await result.current.onPaste(event)
    })

    expect(onChange).toHaveBeenLastCalledWith('[Screenshot: /x/paste-1.png]\n[Screenshot: /x/paste-2.png]\n')
  })
})
```

- [ ] **Step 4: Run tests to verify they fail**

Run: `cd /Users/alexander.kopichin/work/personal/afm/pkg/web/dashboard && npx vitest run src/hooks/use-image-paste`
Expected: FAIL — cannot find module `./use-image-paste`.

- [ ] **Step 5: Implement the hook**

Create `pkg/web/dashboard/src/hooks/use-image-paste/use-image-paste.ts`:

```ts
import { useLayoutEffect, useRef, useState, type ClipboardEvent, type MutableRefObject } from 'react'
import { AttachmentUploadError, uploadAttachment } from '../../api/run-client'

export type PasteAttachment = {
  id: string
  previewUrl: string
  uploading: boolean
}

export type UseImagePasteResult = {
  nodeRef: MutableRefObject<HTMLTextAreaElement | null>
  attachments: PasteAttachment[]
  uploadError: string | null
  onPaste: (event: ClipboardEvent<HTMLTextAreaElement>) => void
  removeAttachment: (id: string) => void
}

type AttachmentRecord = PasteAttachment & { insertedText: string | null }

const ERROR_DISPLAY_MS = 4000

// Backs PasteableTextarea's paste handling: uploads a pasted clipboard image
// via uploadAttachment and splices "[Screenshot: <path>]\n" into the
// controlled value at the caret. Multiple images in one paste are uploaded
// sequentially (not in parallel) so each insertion's caret math is computed
// against the previous insertion's already-updated in-memory value, not
// against a possibly-stale `value` prop from before the parent re-rendered —
// see uploadOne's `baseValue`/`caret` threading below.
export function useImagePaste(
  stageId: string,
  value: string,
  onChange: (value: string) => void,
): UseImagePasteResult {
  const nodeRef = useRef<HTMLTextAreaElement | null>(null)
  const valueRef = useRef(value)
  valueRef.current = value
  const onChangeRef = useRef(onChange)
  onChangeRef.current = onChange
  const removedIds = useRef<Set<string>>(new Set())
  const nextId = useRef(0)
  const errorTimer = useRef<number | undefined>(undefined)
  const pendingCaret = useRef<number | null>(null)

  const [attachments, setAttachments] = useState<AttachmentRecord[]>([])
  const [uploadError, setUploadError] = useState<string | null>(null)

  useLayoutEffect(() => {
    if (pendingCaret.current === null) return
    const el = nodeRef.current
    if (el !== null) {
      el.selectionStart = pendingCaret.current
      el.selectionEnd = pendingCaret.current
    }
    pendingCaret.current = null
  }, [value])

  function showError(message: string): void {
    setUploadError(message)
    if (errorTimer.current !== undefined) window.clearTimeout(errorTimer.current)
    errorTimer.current = window.setTimeout(() => setUploadError(null), ERROR_DISPLAY_MS)
  }

  async function uploadOne(
    file: File,
    caret: number,
    baseValue: string,
  ): Promise<{ caret: number; value: string } | null> {
    const id = String(nextId.current)
    nextId.current += 1
    const previewUrl = URL.createObjectURL(file)
    setAttachments((prev) => [...prev, { id, previewUrl, uploading: true, insertedText: null }])

    try {
      const { path } = await uploadAttachment(stageId, file)

      if (removedIds.current.has(id)) {
        URL.revokeObjectURL(previewUrl)
        return null
      }

      const inserted = `[Screenshot: ${path}]\n`
      const before = baseValue.slice(0, caret)
      const after = baseValue.slice(caret)
      const next = before + inserted + after
      pendingCaret.current = before.length + inserted.length
      onChangeRef.current(next)
      setAttachments((prev) =>
        prev.map((a) => (a.id === id ? { ...a, uploading: false, insertedText: inserted } : a)),
      )
      return { caret: before.length + inserted.length, value: next }
    } catch (err) {
      URL.revokeObjectURL(previewUrl)
      setAttachments((prev) => prev.filter((a) => a.id !== id))
      if (!removedIds.current.has(id)) {
        const status = err instanceof AttachmentUploadError ? err.status : 0
        showError(
          status === 413
            ? 'Image too large (max 10 MB)'
            : status === 415
              ? 'Unsupported image type'
              : 'Upload failed',
        )
      }
      return null
    }
  }

  function onPaste(event: ClipboardEvent<HTMLTextAreaElement>): Promise<void> | undefined {
    const items = event.clipboardData?.items
    if (items === undefined || items === null) return undefined

    const files: File[] = []
    for (let i = 0; i < items.length; i++) {
      const item = items[i]
      if (item !== undefined && item.kind === 'file' && item.type.startsWith('image/')) {
        const file = item.getAsFile()
        if (file !== null) files.push(file)
      }
    }
    if (files.length === 0) return undefined

    event.preventDefault()
    const startCaret = event.currentTarget.selectionStart ?? valueRef.current.length

    return (async () => {
      let caret = startCaret
      let runningValue = valueRef.current
      for (const file of files) {
        const result = await uploadOne(file, caret, runningValue)
        if (result !== null) {
          caret = result.caret
          runningValue = result.value
        }
      }
    })()
  }

  function removeAttachment(id: string): void {
    const target = attachments.find((a) => a.id === id)
    if (target === undefined) return

    URL.revokeObjectURL(target.previewUrl)
    if (target.insertedText !== null) {
      const idx = valueRef.current.indexOf(target.insertedText)
      if (idx !== -1) {
        onChangeRef.current(valueRef.current.slice(0, idx) + valueRef.current.slice(idx + target.insertedText.length))
      }
    } else {
      removedIds.current.add(id)
    }
    setAttachments((prev) => prev.filter((a) => a.id !== id))
  }

  return { nodeRef, attachments, uploadError, onPaste: onPaste as (event: ClipboardEvent<HTMLTextAreaElement>) => void, removeAttachment }
}
```

Create `pkg/web/dashboard/src/hooks/use-image-paste/index.ts`:

```ts
export { useImagePaste } from './use-image-paste'
export type { PasteAttachment, UseImagePasteResult } from './use-image-paste'
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `cd /Users/alexander.kopichin/work/personal/afm/pkg/web/dashboard && npx vitest run src/hooks/use-image-paste`
Expected: PASS (7 tests).

- [ ] **Step 7: Typecheck**

Run: `cd /Users/alexander.kopichin/work/personal/afm/pkg/web/dashboard && npm run typecheck`
Expected: no errors. (The test file's `result.current.onPaste(event) as unknown as Promise<void>` cast is what lets a test await the hook's internal promise despite the public type being `(event) => void` — see the hook's return statement, which widens `onPaste`'s declared type but not its runtime behavior.)

- [ ] **Step 8: Commit**

```bash
cd /Users/alexander.kopichin/work/personal/afm
git add pkg/web/dashboard/src/api/run-client.ts pkg/web/dashboard/src/hooks/use-image-paste pkg/web/dashboard/src/test/setup.ts
git commit -m "$(cat <<'EOF'
feat(dashboard): добавляем useImagePaste — загрузка картинки из буфера

Хук перехватывает paste с картинкой, грузит её через новый эндпоинт
attachments и вставляет ссылку [Screenshot: <path>] в текст на месте
курсора. Несколько картинок за один paste грузятся последовательно.
EOF
)"
```

---

### Task 3: `PasteableTextarea` component + CSS

**Files:**
- Create: `pkg/web/dashboard/src/components/pasteable-textarea/PasteableTextarea.tsx`
- Create: `pkg/web/dashboard/src/components/pasteable-textarea/PasteableTextarea.test.tsx`
- Create: `pkg/web/dashboard/src/components/pasteable-textarea/index.ts`
- Create: `pkg/web/dashboard/skins/base/pasteable-textarea.css`
- Modify: `pkg/web/dashboard/skins/coffee/index.css:14` (add `@import`)
- Modify: `pkg/web/dashboard/skins/goga/index.css:17` (add `@import`)
- Modify: `pkg/web/dashboard/skins/novacorps/index.css:14` (add `@import`)

**Interfaces:**
- Consumes: Task 2's `useImagePaste`; existing `useAutoGrowTextarea` (`pkg/web/dashboard/src/hooks/use-auto-grow-textarea`, unchanged).
- Produces: `PasteableTextarea` component, `pkg/web/dashboard/src/components/pasteable-textarea`, props `{ stageId: string; value: string; onChange: (value: string) => void; placeholder?: string; className?: string; autoFocus?: boolean; disabled?: boolean; maxHeight?: number; onKeyDown?: (event: React.KeyboardEvent<HTMLTextAreaElement>) => void }`. Consumed by Tasks 4-6.

- [ ] **Step 1: Write the failing component tests**

Create `pkg/web/dashboard/src/components/pasteable-textarea/PasteableTextarea.test.tsx`:

```tsx
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { PasteableTextarea } from './PasteableTextarea'

function jsonResponse(data: unknown, ok = true): Response {
  return { ok, json: async () => data } as Response
}

function makeImageItem(): DataTransferItem {
  const file = new File([new Uint8Array([1, 2, 3])], 'paste.png', { type: 'image/png' })
  return { kind: 'file', type: 'image/png', getAsFile: () => file } as unknown as DataTransferItem
}

describe('PasteableTextarea', () => {
  beforeEach(() => {
    vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:mock-url')
    vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {})
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('renders the current value and calls onChange on typing', () => {
    const onChange = vi.fn()
    render(<PasteableTextarea stageId="s1" value="hi" onChange={onChange} />)

    const textarea = screen.getByRole('textbox') as HTMLTextAreaElement
    expect(textarea.value).toBe('hi')

    fireEvent.change(textarea, { target: { value: 'hi there' } })
    expect(onChange).toHaveBeenCalledWith('hi there')
  })

  it('passes className/placeholder through to the inner textarea', () => {
    render(<PasteableTextarea stageId="s1" value="" onChange={vi.fn()} className="dialog-custom" placeholder="Or type…" />)

    const textarea = screen.getByPlaceholderText('Or type…')
    expect(textarea).toHaveClass('dialog-custom')
  })

  it('grows the textarea to fit its content, same as a plain textarea would', () => {
    const scrollHeightSpy = vi
      .spyOn(window.HTMLTextAreaElement.prototype, 'scrollHeight', 'get')
      .mockReturnValue(180)

    const { rerender } = render(<PasteableTextarea stageId="s1" value="" onChange={vi.fn()} />)
    rerender(<PasteableTextarea stageId="s1" value="a longer note" onChange={vi.fn()} />)

    const textarea = screen.getByRole('textbox') as HTMLTextAreaElement
    expect(textarea.style.height).toBe('180px')

    scrollHeightSpy.mockRestore()
  })

  it('pasting a clipboard image uploads it, shows a thumbnail, and inserts a Screenshot reference', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse({ path: '/afm/run/s1/attachments/paste-1.png' }))
    const onChange = vi.fn()
    render(<PasteableTextarea stageId="s1" value="" onChange={onChange} />)

    fireEvent.paste(screen.getByRole('textbox'), { clipboardData: { items: [makeImageItem()] } })

    await waitFor(() => expect(onChange).toHaveBeenCalledWith('[Screenshot: /afm/run/s1/attachments/paste-1.png]\n'))
    expect(screen.getByAltText('Pasted screenshot')).toBeInTheDocument()
  })

  it('removing an attachment strips its Screenshot reference from the value', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse({ path: '/x/paste-1.png' }))
    let value = ''
    const onChange = vi.fn((next: string) => {
      value = next
    })
    const { rerender } = render(<PasteableTextarea stageId="s1" value={value} onChange={onChange} />)

    fireEvent.paste(screen.getByRole('textbox'), { clipboardData: { items: [makeImageItem()] } })
    await waitFor(() => expect(onChange).toHaveBeenCalled())
    rerender(<PasteableTextarea stageId="s1" value={value} onChange={onChange} />)

    expect(screen.getByAltText('Pasted screenshot')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: /remove pasted image/i }))
    expect(value).toBe('')
  })

  it('pasting plain text does not upload or alter the value', () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch')
    const onChange = vi.fn()
    render(<PasteableTextarea stageId="s1" value="hello" onChange={onChange} />)

    fireEvent.paste(screen.getByRole('textbox'), {
      clipboardData: { items: [{ kind: 'string', type: 'text/plain', getAsFile: () => null }] },
    })

    expect(fetchSpy).not.toHaveBeenCalled()
    expect(onChange).not.toHaveBeenCalled()
  })
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/alexander.kopichin/work/personal/afm/pkg/web/dashboard && npx vitest run src/components/pasteable-textarea`
Expected: FAIL — cannot find module `./PasteableTextarea`.

- [ ] **Step 3: Implement the component**

Create `pkg/web/dashboard/src/components/pasteable-textarea/PasteableTextarea.tsx`:

```tsx
import type { ChangeEvent, KeyboardEvent, ReactElement } from 'react'
import { useAutoGrowTextarea } from '../../hooks/use-auto-grow-textarea'
import { useImagePaste } from '../../hooks/use-image-paste'

type PasteableTextareaProps = {
  stageId: string
  value: string
  onChange: (value: string) => void
  placeholder?: string
  className?: string
  autoFocus?: boolean
  disabled?: boolean
  maxHeight?: number
  onKeyDown?: (event: KeyboardEvent<HTMLTextAreaElement>) => void
}

// Drop-in replacement for a plain <textarea>, used everywhere a user writes
// text that ends up in front of the agent (AgentNoteModal, DialogChannel's
// custom-answer and line-comment boxes, PlanPanel's line-comment box).
// Composes the existing auto-grow behavior with clipboard-image paste — Cmd+V
// with a screenshot on the clipboard uploads it and inserts a
// "[Screenshot: <path>]" reference at the caret; the agent reads the file
// itself when it decides to (see
// docs/superpowers/specs/2026-08-11-clipboard-image-paste-design.md).
export function PasteableTextarea({
  stageId,
  value,
  onChange,
  placeholder,
  className,
  autoFocus,
  disabled,
  maxHeight = 400,
  onKeyDown,
}: PasteableTextareaProps): ReactElement {
  const autoGrowRef = useAutoGrowTextarea(value, maxHeight)
  const { nodeRef, attachments, uploadError, onPaste, removeAttachment } = useImagePaste(stageId, value, onChange)

  function setRefs(node: HTMLTextAreaElement | null): void {
    autoGrowRef(node)
    nodeRef.current = node
  }

  function handleChange(event: ChangeEvent<HTMLTextAreaElement>): void {
    onChange(event.target.value)
  }

  const showStrip = attachments.length > 0 || uploadError !== null

  return (
    <div className="pasteable-textarea-wrap">
      {showStrip && (
        <div className="pasteable-attachments">
          {attachments.map((attachment) => (
            <div key={attachment.id} className={`pasteable-attachment${attachment.uploading ? ' uploading' : ''}`}>
              <img src={attachment.previewUrl} alt="Pasted screenshot" />
              <button
                type="button"
                className="pasteable-attachment-remove"
                aria-label="Remove pasted image"
                onClick={() => removeAttachment(attachment.id)}
              >
                ✕
              </button>
            </div>
          ))}
          {uploadError !== null && <span className="pasteable-attachment-error">{uploadError}</span>}
        </div>
      )}
      <textarea
        ref={setRefs}
        className={className}
        value={value}
        placeholder={placeholder}
        autoFocus={autoFocus}
        disabled={disabled}
        onChange={handleChange}
        onPaste={onPaste}
        onKeyDown={onKeyDown}
      />
    </div>
  )
}
```

Create `pkg/web/dashboard/src/components/pasteable-textarea/index.ts`:

```ts
export { PasteableTextarea } from './PasteableTextarea'
```

Create `pkg/web/dashboard/skins/base/pasteable-textarea.css`:

```css
/* pkg/web/dashboard/public/skins/base/pasteable-textarea.css
   PasteableTextarea: drop-in textarea wrapper used by AgentNoteModal,
   DialogChannel (custom answer + line comments) and PlanPanel (line
   comments) to support pasting a clipboard screenshot (Cmd+V). This file
   owns layout only (width/stacking) — colors come from the same tokens each
   call site's own textarea rule already uses (className is passed through
   unchanged), so this file needs no per-skin override. */

.pasteable-textarea-wrap {
  display: block;
  width: 100%;
}

.pasteable-textarea-wrap textarea {
  display: block;
  width: 100%;
  box-sizing: border-box;
}

.pasteable-attachments {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
  margin-bottom: 6px;
}

.pasteable-attachment {
  position: relative;
  width: 48px;
  height: 48px;
  border: 1px solid var(--mint-soft);
  border-radius: 4px;
  overflow: hidden;
  flex-shrink: 0;
}

.pasteable-attachment img {
  width: 100%;
  height: 100%;
  object-fit: cover;
  display: block;
}

.pasteable-attachment.uploading img {
  opacity: 0.5;
}

.pasteable-attachment-remove {
  position: absolute;
  top: 1px;
  right: 1px;
  background: rgba(var(--overlay-scrim-rgb), 0.6);
  border: none;
  color: var(--ink-hi);
  cursor: pointer;
  font-size: 9px;
  line-height: 1;
  padding: 1px 4px;
  border-radius: 2px;
}
.pasteable-attachment-remove:hover {
  color: var(--coral);
}

.pasteable-attachment-error {
  color: var(--coral);
  font-size: 11px;
}
```

Modify `pkg/web/dashboard/skins/coffee/index.css` — add right after the existing `@import url("../base/agent-note-modal.css");` line (line 14):

```css
@import url("../base/pasteable-textarea.css");
```

Modify `pkg/web/dashboard/skins/goga/index.css` — same addition, right after its `@import url("../base/agent-note-modal.css");` line (line 17).

Modify `pkg/web/dashboard/skins/novacorps/index.css` — same addition, right after its `@import url("../base/agent-note-modal.css");` line (line 14).

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/alexander.kopichin/work/personal/afm/pkg/web/dashboard && npx vitest run src/components/pasteable-textarea`
Expected: PASS (6 tests).

- [ ] **Step 5: Typecheck**

Run: `cd /Users/alexander.kopichin/work/personal/afm/pkg/web/dashboard && npm run typecheck`
Expected: no errors.

- [ ] **Step 6: Commit**

```bash
cd /Users/alexander.kopichin/work/personal/afm
git add pkg/web/dashboard/src/components/pasteable-textarea pkg/web/dashboard/skins/base/pasteable-textarea.css pkg/web/dashboard/skins/coffee/index.css pkg/web/dashboard/skins/goga/index.css pkg/web/dashboard/skins/novacorps/index.css
git commit -m "$(cat <<'EOF'
feat(dashboard): добавляем PasteableTextarea — drop-in textarea со вставкой картинок

Оборачивает useAutoGrowTextarea и useImagePaste за одним компонентом —
дальше просто замена <textarea> на <PasteableTextarea> в четырёх местах.
EOF
)"
```

---

### Task 4: Wire into `AgentNoteModal`

**Files:**
- Modify: `pkg/web/dashboard/src/components/agent-note-modal/AgentNoteModal.tsx`

**Interfaces:**
- Consumes: Task 3's `PasteableTextarea`.
- Produces: nothing new — this task only swaps an existing textarea for the drop-in component. `AgentNoteModal`'s own props (`stageId`, `onSubmit`, `onCancel`) are unchanged.

- [ ] **Step 1: Swap the textarea**

Modify `pkg/web/dashboard/src/components/agent-note-modal/AgentNoteModal.tsx`. Replace the whole file content with:

```tsx
import { useState, type ReactElement } from 'react'
import { PasteableTextarea } from '../pasteable-textarea'

type AgentNoteModalProps = {
  stageId: string
  onSubmit: (note: string) => void
  onCancel: () => void
}

// Модалка «Добавить поправку агенту» (agent_suggest): открывается из кебаб-
// меню StagesList. Предупреждает, что агент доведёт текущее действие до
// конца перед перезапуском с этой фразой в контексте.
export function AgentNoteModal({ stageId, onSubmit, onCancel }: AgentNoteModalProps): ReactElement {
  const [note, setNote] = useState('')

  return (
    <div className="modal-overlay" role="dialog" aria-modal="true" aria-label={`Add a note for stage ${stageId}`}>
      <div className="modal-content agent-note-modal">
        <p className="agent-note-warning">
          The agent will finish its current action, then restart with this note in context.
        </p>
        <PasteableTextarea
          stageId={stageId}
          className="agent-note-textarea"
          value={note}
          onChange={setNote}
          placeholder="What should the agent take into account?"
          autoFocus
        />
        <div className="modal-actions">
          <button type="button" className="btn btn-cancel" onClick={onCancel}>
            Cancel
          </button>
          <button
            type="button"
            className="btn btn-send"
            disabled={note.trim() === ''}
            onClick={() => onSubmit(note.trim())}
          >
            Send
          </button>
        </div>
      </div>
    </div>
  )
}
```

- [ ] **Step 2: Run the existing tests unmodified**

Run: `cd /Users/alexander.kopichin/work/personal/afm/pkg/web/dashboard && npx vitest run src/components/agent-note-modal`
Expected: PASS — all 4 existing tests in `AgentNoteModal.test.tsx` (including the `scrollHeight`-based auto-grow assertion) pass with zero edits to the test file, since `PasteableTextarea` renders the same `className="agent-note-textarea"` textarea with the same `role="textbox"`, wired through the same `useAutoGrowTextarea` hook.

- [ ] **Step 3: Typecheck**

Run: `cd /Users/alexander.kopichin/work/personal/afm/pkg/web/dashboard && npm run typecheck`
Expected: no errors.

- [ ] **Step 4: Commit**

```bash
cd /Users/alexander.kopichin/work/personal/afm
git add pkg/web/dashboard/src/components/agent-note-modal/AgentNoteModal.tsx
git commit -m "$(cat <<'EOF'
feat(dashboard): AgentNoteModal — вставка скриншота в поле поправки

Заменяем textarea на PasteableTextarea, поведение не меняется, кроме
новой возможности Cmd+V со скриншотом.
EOF
)"
```

---

### Task 5: Wire into `DialogChannel`

**Files:**
- Modify: `pkg/web/dashboard/src/components/dialog-channel/DialogChannel.tsx`

**Interfaces:**
- Consumes: Task 3's `PasteableTextarea`.
- Produces: nothing new — swaps the custom-answer textarea (`dialog-custom`) and the per-line comment textarea for the drop-in component. `DialogChannel`'s own props/behavior are unchanged.

- [ ] **Step 1: Remove the now-unused auto-grow import and hook calls**

In `pkg/web/dashboard/src/components/dialog-channel/DialogChannel.tsx`, remove this import line:

```ts
import { useAutoGrowTextarea } from '../../hooks/use-auto-grow-textarea'
```

and add, in its place:

```ts
import { PasteableTextarea } from '../pasteable-textarea'
```

Remove these two lines from inside the component body:

```ts
  const commentTextareaRef = useAutoGrowTextarea(draft, 400)
  const customTextareaRef = useAutoGrowTextarea(customText, 400)
```

- [ ] **Step 2: Add a `stageId` constant**

Right after the function signature line

```ts
export function DialogChannel({ stage, attention = false }: DialogChannelProps): ReactElement {
```

add:

```ts
  const stageId = stage?.id ?? ''
```

- [ ] **Step 3: Swap the custom-answer textarea**

Replace:

```tsx
                    <textarea
                      ref={customTextareaRef}
                      className="dialog-custom"
                      placeholder="Or type your own answer…"
                      value={customText}
                      disabled={pending.allow_custom !== true}
                      onChange={(event) => onCustomInput(event.target.value)}
                      onKeyDown={(e) => {
                        if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') {
                          e.preventDefault()
                          void sendAnswer()
                        }
                      }}
                    />
```

with:

```tsx
                    <PasteableTextarea
                      stageId={stageId}
                      className="dialog-custom"
                      placeholder="Or type your own answer…"
                      value={customText}
                      disabled={pending.allow_custom !== true}
                      onChange={onCustomInput}
                      onKeyDown={(e) => {
                        if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') {
                          e.preventDefault()
                          void sendAnswer()
                        }
                      }}
                    />
```

- [ ] **Step 4: Swap the per-line comment textarea**

Replace:

```tsx
            <textarea
              ref={commentTextareaRef}
              placeholder={`Comment on line ${item.line}...`}
              value={draft}
              onChange={(event) => setDraft(event.target.value)}
              onKeyDown={(e) => {
                if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') {
                  e.preventDefault()
                  saveComment(item.line)
                }
              }}
            />
```

with:

```tsx
            <PasteableTextarea
              stageId={stageId}
              placeholder={`Comment on line ${item.line}...`}
              value={draft}
              onChange={setDraft}
              onKeyDown={(e) => {
                if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') {
                  e.preventDefault()
                  saveComment(item.line)
                }
              }}
            />
```

- [ ] **Step 5: Run the existing tests unmodified**

Run: `cd /Users/alexander.kopichin/work/personal/afm/pkg/web/dashboard && npx vitest run src/components/dialog-channel`
Expected: PASS — every existing selector in `DialogChannel.test.tsx` targets `container.querySelector('textarea.dialog-custom')` or `container.querySelector('.line-comment-form textarea')`, both descendant selectors that still match through `PasteableTextarea`'s wrapper `<div>`.

- [ ] **Step 6: Typecheck**

Run: `cd /Users/alexander.kopichin/work/personal/afm/pkg/web/dashboard && npm run typecheck`
Expected: no errors.

- [ ] **Step 7: Commit**

```bash
cd /Users/alexander.kopichin/work/personal/afm
git add pkg/web/dashboard/src/components/dialog-channel/DialogChannel.tsx
git commit -m "$(cat <<'EOF'
feat(dashboard): DialogChannel — вставка скриншота в ответ и комментарий

Заменяем оба textarea (свободный ответ и комментарий к строке вопроса)
на PasteableTextarea, поведение не меняется, кроме новой возможности
Cmd+V со скриншотом.
EOF
)"
```

---

### Task 6: Wire into `PlanPanel`

**Files:**
- Modify: `pkg/web/dashboard/src/components/plan-panel/PlanPanel.tsx`

**Interfaces:**
- Consumes: Task 3's `PasteableTextarea`.
- Produces: nothing new — swaps the per-line comment textarea for the drop-in component. `PlanPanel`'s own props/behavior are unchanged.

- [ ] **Step 1: Remove the now-unused auto-grow import and hook call**

In `pkg/web/dashboard/src/components/plan-panel/PlanPanel.tsx`, remove:

```ts
import { useAutoGrowTextarea } from '../../hooks/use-auto-grow-textarea'
```

and add, in its place:

```ts
import { PasteableTextarea } from '../pasteable-textarea'
```

Remove this line from inside the component body:

```ts
  const commentTextareaRef = useAutoGrowTextarea(draft, 400)
```

- [ ] **Step 2: Add a `stageId` constant**

Right after the function signature line

```ts
export function PlanPanel({ stage, attention = false }: PlanPanelProps): ReactElement {
```

add:

```ts
  const stageId = stage?.id ?? ''
```

- [ ] **Step 3: Swap the comment textarea**

Replace:

```tsx
            <textarea
              ref={commentTextareaRef}
              placeholder={`Comment on line ${item.line}...`}
              value={draft}
              onChange={(event) => setDraft(event.target.value)}
            />
```

with:

```tsx
            <PasteableTextarea
              stageId={stageId}
              placeholder={`Comment on line ${item.line}...`}
              value={draft}
              onChange={setDraft}
            />
```

- [ ] **Step 4: Run the existing tests unmodified**

Run: `cd /Users/alexander.kopichin/work/personal/afm/pkg/web/dashboard && npx vitest run src/components/plan-panel`
Expected: PASS — every existing selector in `PlanPanel.test.tsx` targets `container.querySelector('.line-comment-form textarea')`, a descendant selector that still matches through `PasteableTextarea`'s wrapper `<div>`.

- [ ] **Step 5: Typecheck**

Run: `cd /Users/alexander.kopichin/work/personal/afm/pkg/web/dashboard && npm run typecheck`
Expected: no errors.

- [ ] **Step 6: Commit**

```bash
cd /Users/alexander.kopichin/work/personal/afm
git add pkg/web/dashboard/src/components/plan-panel/PlanPanel.tsx
git commit -m "$(cat <<'EOF'
feat(dashboard): PlanPanel — вставка скриншота в комментарий к плану

Заменяем textarea комментария на PasteableTextarea, поведение не
меняется, кроме новой возможности Cmd+V со скриншотом.
EOF
)"
```

---

### Task 7: Full-stack verification (lint, build, full test suite, manual browser check)

**Files:** none (verification only).

**Interfaces:** none — this task only runs and observes.

- [ ] **Step 1: Full Go verification**

Run: `cd /Users/alexander.kopichin/work/personal/afm && make lint && make build && make test`
Expected: `make lint` → `0 issues.`; `make build` → builds `bin/afm` (this also runs the frontend build via the Makefile's existing `npm run build` step, so a broken frontend build fails this step too); `make test` → all Go tests pass, including the new `pkg/server/attachments_test.go` cases.

- [ ] **Step 2: Full frontend verification**

Run: `cd /Users/alexander.kopichin/work/personal/afm/pkg/web/dashboard && npm run typecheck && npm test`
Expected: no type errors; every test file passes, including the new `use-image-paste.test.ts` and `PasteableTextarea.test.tsx`, and the untouched `AgentNoteModal.test.tsx`/`DialogChannel.test.tsx`/`PlanPanel.test.tsx`.

- [ ] **Step 3: Manual browser verification of the real end-to-end path**

This exercises the actual built binary and a real browser — not mocks — to confirm bytes really land on disk and the UI really reflects it.

1. In a scratch directory (e.g. `/private/tmp/claude-*/scratchpad/img-paste-verify`), copy `example-flow-interactive.yaml` from the repo root and run the built binary in the background:
   ```bash
   mkdir -p /tmp/img-paste-verify && cd /tmp/img-paste-verify
   cp /Users/alexander.kopichin/work/personal/afm/example-flow-interactive.yaml .
   /Users/alexander.kopichin/work/personal/afm/bin/afm run example-flow-interactive.yaml
   ```
   Wait for the log to print the dashboard URL (`http://localhost:<port>`) and for the `discovery` stage to reach `awaiting_user_input` (it asks a question via the file-based dialog protocol — see `example-flow-interactive.yaml`'s own header comment).
2. Using the `mcp__chrome-devtools__*` tools: `new_page` to the printed dashboard URL, `take_snapshot` to locate the dialog's custom-answer textarea (`textarea.dialog-custom`) once the pending question is visible.
3. Synthesize a real in-browser clipboard paste via `evaluate_script` against that textarea — construct a small PNG `Blob`, wrap it in a `DataTransfer`, and dispatch a real `ClipboardEvent('paste', { clipboardData })` on the textarea node (this runs in actual Chromium, not jsdom, so it exercises the real `fetch`, the real Go handler, and real disk I/O under a real `.afm/runs/.../attachments/` directory).
4. `take_screenshot` to visually confirm the thumbnail renders above the textarea, and confirm the textarea's value now contains `[Screenshot: ...]` pointing at a real path.
5. `list_console_messages` to confirm no JavaScript errors were logged during the paste.
6. Inspect the filesystem directly: `find /tmp/img-paste-verify/.afm/runs -path '*/attachments/*'` should show the uploaded file, and its byte size should be non-zero and match what was pasted.
7. Repeat steps 2-4 once against the plan-comment textarea (open a stage in `awaiting_approval`, click a plan line to open its comment form) and once against the `AgentNoteModal` note field (stage kebab menu → "Add a note for the agent") to confirm all three surfaces work, not just the dialog one.
8. Stop the running `afm` process and remove `/tmp/img-paste-verify`.

If any of these manual checks fail, treat it as a real bug: fix it, re-run the affected task's automated tests, and repeat this manual check before considering the plan done.

- [ ] **Step 4: Report**

Summarize what was verified (all automated suites green, manual paste confirmed in a real browser across all three surfaces, uploaded bytes found on disk) — no commit needed for this task unless Step 3 surfaced a bug that required a fix, in which case that fix gets its own small commit with a description of what was wrong.
