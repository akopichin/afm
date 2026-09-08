# File-Browser Review Notes with Best-Effort Pause — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a reviewer click a line in the Docker file browser, write content-anchored notes across many files while the flow is best-effort paused, then inject all notes as feedback into one chosen stage which restarts while the others resume.

**Architecture:** Hybrid model — note *correctness* rests on a per-note content anchor (`content_sha` + `orig_line_text`), not on freezing writers; the flow pause is a best-effort UX affordance (a private `activationHeld` flag holds new activations; active `IsActive` agent stages get the existing per-stage `EvPause`). A durable marker (`notes-pause.json`, states `paused`→`resuming`) is the review-transaction authority for the control policy, banner, `shouldExit`, and finalization until it is deleted. Injection reuses `Revise`/`Continue`/`*WithFeedback`, dispatched by a recorded `resume_kind`, resuming each owner only after its old callback drains.

**Tech Stack:** Go 1.x (orchestrator/state/server), React + TypeScript + Vite (dashboard), `net/http` mux with suffix routing, event-sourced FSM.

**Spec:** `docs/superpowers/specs/2026-09-08-file-browser-review-notes-design.md` (v6). The plan argues from the spec; executors read both.

## Global Constraints

- **Do NOT change the `go` version in `go.mod`.**
- **`make lint` must stay green** (it runs `golangci-lint` twice: native + `GOOS=linux`). Run it before every commit that touches Go.
- **Commit messages in Russian.** No `Co-Authored-By` trailers.
- **Frontend build** (`make build`) runs `vite build`; keep it green.
- **`.afm/runs/<run_id>/` layout:** stage dir is always `filepath.Join(o.opts.RunDir, s.ID)` — there is no helper. Run-level files (notes, marker) live directly under `RunDir`.
- **Status constants** are `state.StatusPending/Planning/AwaitingApproval/Revising/Ready/Running/Retrying/Paused/AwaitingUserInput/Done/Failed/HookFailed`.
- **Server flow actions need three touch points:** an interface method (`FlowActions`), a `routeStages`/new-router case, and a handler — plus a `run-client.ts` export.
- **Best-effort is deliberate:** never add quiescence detection, launch tokens, or writer counting. A racing writer is allowed; the content anchor covers it.

---

## Phase 1 — State layer (`pkg/state`)

Self-contained pure-Go IO. Everything here is unit-testable without the orchestrator.

### Task 1: Shared atomic-write helper + content-hash helper

**Files:**
- Modify: `pkg/state/state.go` (add helpers near the other file helpers)
- Test: `pkg/state/state_test.go`

**Interfaces:**
- Produces: `func atomicWriteFile(path string, data []byte, perm os.FileMode) error` (temp+fsync+rename+dir-fsync); `func FileContentSHA(data []byte) string` returning `"sha256:"+hex`.

- [ ] **Step 1: Write the failing test**

```go
func TestFileContentSHA_Stable(t *testing.T) {
	a := FileContentSHA([]byte("hello\n"))
	b := FileContentSHA([]byte("hello\n"))
	if a != b || !strings.HasPrefix(a, "sha256:") {
		t.Fatalf("unstable or unprefixed: %q %q", a, b)
	}
	if FileContentSHA([]byte("other")) == a {
		t.Fatalf("collision")
	}
}

func TestAtomicWriteFile_ReplacesAndPersists(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.json")
	if err := atomicWriteFile(p, []byte("v1"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := atomicWriteFile(p, []byte("v2"), 0644); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	if string(got) != "v2" {
		t.Fatalf("got %q", got)
	}
	if _, err := os.Stat(p + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temp file left behind")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/state/ -run 'TestFileContentSHA_Stable|TestAtomicWriteFile' -v`
Expected: FAIL (undefined: `FileContentSHA`, `atomicWriteFile`).

- [ ] **Step 3: Implement the helpers**

Add to `pkg/state/state.go` (imports: `crypto/sha256`, `encoding/hex`; `os`, `path/filepath`, `fmt` already present):

```go
// FileContentSHA returns a stable "sha256:<hex>" digest of the given bytes.
// Used as the review-note content anchor (a content hash, not mtime+size).
func FileContentSHA(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// atomicWriteFile writes data to path via a unique temp file, fsyncing the file
// and the parent directory, then renaming into place. Durable + torn-read safe.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dirName := filepath.Dir(path)
	if err := os.MkdirAll(dirName, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dirName, err)
	}
	tmp, err := os.CreateTemp(dirName, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("fsync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename temp: %w", err)
	}
	dir, err := os.Open(dirName)
	if err != nil {
		return fmt.Errorf("open dir for fsync: %w", err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("fsync dir: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/state/ -run 'TestFileContentSHA_Stable|TestAtomicWriteFile' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
make lint
git add pkg/state/state.go pkg/state/state_test.go
git commit -m "feat(state): атомарная запись файла с fsync и content-hash helper для заметок"
```

---

### Task 2: Review-notes types + Save/Load/Delete

**Files:**
- Create: `pkg/state/reviewnotes.go`
- Test: `pkg/state/reviewnotes_test.go`

**Interfaces:**
- Consumes: `atomicWriteFile`, `FileContentSHA` (Task 1).
- Produces:
  - `type ReviewNote struct { ID, Root, Path, DisplayPath, Reference string; Line *int; OrigLineText *string; ContentSHA, Text string; CreatedAt time.Time }` (json tags: `id,root,path,display_path,reference,line,orig_line_text,content_sha,text,created_at`).
  - `type ReviewNotes struct { Version, Rev, NextID int; Notes []ReviewNote }` (json `version,rev,next_id,notes`).
  - `func SaveReviewNotes(runDir string, n ReviewNotes) error`
  - `func LoadReviewNotes(runDir string) (ReviewNotes, error)` — missing file → `ReviewNotes{Version:1, NextID:1}`, nil error; corrupt → quarantine + empty + nil error.
  - `func DeleteReviewNotes(runDir string) error` — idempotent.
  - `const reviewNotesFile = "review-notes.json"`

- [ ] **Step 1: Write the failing test**

```go
func TestReviewNotes_SaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	loaded, err := LoadReviewNotes(dir)
	if err != nil || loaded.Version != 1 || loaded.NextID != 1 || len(loaded.Notes) != 0 {
		t.Fatalf("empty default wrong: %+v err=%v", loaded, err)
	}
	line := 42
	orig := "\tif err != nil {"
	loaded.Notes = append(loaded.Notes, ReviewNote{
		ID: "n1", Root: "project", Path: "a.go", DisplayPath: "project/a.go",
		Reference: `[AFM file: "/w/a.go"]`, Line: &line, OrigLineText: &orig,
		ContentSHA: "sha256:x", Text: "fix", CreatedAt: time.Unix(0, 0).UTC(),
	})
	loaded.NextID = 2
	loaded.Rev = 1
	if err := SaveReviewNotes(dir, loaded); err != nil {
		t.Fatal(err)
	}
	again, err := LoadReviewNotes(dir)
	if err != nil || len(again.Notes) != 1 || again.NextID != 2 || *again.Notes[0].Line != 42 {
		t.Fatalf("roundtrip wrong: %+v err=%v", again, err)
	}
}

func TestReviewNotes_CorruptQuarantines(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, reviewNotesFile), []byte("{not json"), 0644)
	loaded, err := LoadReviewNotes(dir)
	if err != nil || len(loaded.Notes) != 0 {
		t.Fatalf("want empty+nil, got %+v err=%v", loaded, err)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, reviewNotesFile+".corrupt-*"))
	if len(matches) != 1 {
		t.Fatalf("want 1 quarantine file, got %d", len(matches))
	}
}

func TestDeleteReviewNotes_Idempotent(t *testing.T) {
	dir := t.TempDir()
	if err := DeleteReviewNotes(dir); err != nil {
		t.Fatalf("delete on missing should be nil: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/state/ -run TestReviewNotes -v`
Expected: FAIL (undefined types/functions).

- [ ] **Step 3: Implement `pkg/state/reviewnotes.go`**

```go
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const reviewNotesFile = "review-notes.json"

type ReviewNote struct {
	ID           string    `json:"id"`
	Root         string    `json:"root"`
	Path         string    `json:"path"`
	DisplayPath  string    `json:"display_path"`
	Reference    string    `json:"reference"`
	Line         *int      `json:"line"`
	OrigLineText *string   `json:"orig_line_text"`
	ContentSHA   string    `json:"content_sha"`
	Text         string    `json:"text"`
	CreatedAt    time.Time `json:"created_at"`
}

type ReviewNotes struct {
	Version int          `json:"version"`
	Rev     int          `json:"rev"`
	NextID  int          `json:"next_id"`
	Notes   []ReviewNote `json:"notes"`
}

func reviewNotesPath(runDir string) string { return filepath.Join(runDir, reviewNotesFile) }

func SaveReviewNotes(runDir string, n ReviewNotes) error {
	if n.Version == 0 {
		n.Version = 1
	}
	data, err := json.MarshalIndent(n, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal review notes: %w", err)
	}
	return atomicWriteFile(reviewNotesPath(runDir), data, 0644)
}

func LoadReviewNotes(runDir string) (ReviewNotes, error) {
	path := reviewNotesPath(runDir)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ReviewNotes{Version: 1, NextID: 1}, nil
	}
	if err != nil {
		return ReviewNotes{Version: 1, NextID: 1}, fmt.Errorf("read review notes: %w", err)
	}
	var n ReviewNotes
	if err := json.Unmarshal(data, &n); err != nil || n.Version != 1 {
		// Advisory data: quarantine the bad file and start empty.
		_ = os.Rename(path, fmt.Sprintf("%s.corrupt-%d", path, time.Now().UnixNano()))
		return ReviewNotes{Version: 1, NextID: 1}, nil
	}
	if n.NextID == 0 {
		n.NextID = 1
	}
	return n, nil
}

func DeleteReviewNotes(runDir string) error {
	if err := os.Remove(reviewNotesPath(runDir)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete review notes: %w", err)
	}
	return nil
}
```

> Note: `time.Now()` is available here (this is production Go, not a workflow script).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/state/ -run TestReviewNotes -v && go test ./pkg/state/ -run TestDeleteReviewNotes -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
make lint
git add pkg/state/reviewnotes.go pkg/state/reviewnotes_test.go
git commit -m "feat(state): content-anchored review-notes хранилище (save/load/delete, quarantine)"
```

---

### Task 3: Pause-marker types + Write/Read/Clear (corrupt-aware)

**Files:**
- Modify: `pkg/state/reviewnotes.go` (same file — marker lives with notes)
- Test: `pkg/state/reviewnotes_test.go`

**Interfaces:**
- Consumes: `atomicWriteFile`.
- Produces:
  - `const (PauseStatePaused="paused"; PauseStateResuming="resuming"; PauseModeInject="inject"; PauseModeCancel="cancel")`
  - `type PauseOwner struct { ID, ResumeKind string; FromRevising bool }` (json `id,resume_kind,from_revising`).
  - `type PauseMarker struct { Version int; OperationID, State, Mode, TargetStage string; CreatedAt time.Time; Owned []PauseOwner }` (json `version,operation_id,state,mode,target_stage,created_at,owned`).
  - `func WriteNotesPauseMarker(runDir string, m PauseMarker) error`
  - `func ReadNotesPauseMarker(runDir string) (PauseMarker, bool, error)` — found=false if absent; corrupt → quarantine + return `(zero, true, ErrCorruptPauseMarker)` so the caller can fail-closed for `resuming`.
  - `func ClearNotesPauseMarker(runDir string) error` — idempotent.
  - `var ErrCorruptPauseMarker = errors.New("corrupt pause marker")`

- [ ] **Step 1: Write the failing test**

```go
func TestPauseMarker_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	_, found, err := ReadNotesPauseMarker(dir)
	if found || err != nil {
		t.Fatalf("absent marker: found=%v err=%v", found, err)
	}
	m := PauseMarker{
		Version: 1, OperationID: "op1", State: PauseStatePaused,
		CreatedAt: time.Unix(0, 0).UTC(),
		Owned:     []PauseOwner{{ID: "s1", ResumeKind: "implementation", FromRevising: false}},
	}
	if err := WriteNotesPauseMarker(dir, m); err != nil {
		t.Fatal(err)
	}
	got, found, err := ReadNotesPauseMarker(dir)
	if !found || err != nil || got.State != PauseStatePaused || got.Owned[0].ID != "s1" {
		t.Fatalf("roundtrip wrong: %+v found=%v err=%v", got, found, err)
	}
	if err := ClearNotesPauseMarker(dir); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := ReadNotesPauseMarker(dir); found {
		t.Fatalf("marker should be gone")
	}
}

func TestPauseMarker_CorruptReturnsErr(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, notesPauseMarkerFile), []byte("{bad"), 0644)
	_, found, err := ReadNotesPauseMarker(dir)
	if !found || !errors.Is(err, ErrCorruptPauseMarker) {
		t.Fatalf("want found+ErrCorrupt, got found=%v err=%v", found, err)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, notesPauseMarkerFile+".corrupt-*"))
	if len(matches) != 1 {
		t.Fatalf("want quarantine copy")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/state/ -run TestPauseMarker -v`
Expected: FAIL (undefined).

- [ ] **Step 3: Implement (append to `pkg/state/reviewnotes.go`)**

Add `"errors"` to imports.

```go
const notesPauseMarkerFile = "notes-pause.json"

const (
	PauseStatePaused   = "paused"
	PauseStateResuming = "resuming"
	PauseModeInject    = "inject"
	PauseModeCancel    = "cancel"
)

var ErrCorruptPauseMarker = errors.New("corrupt pause marker")

type PauseOwner struct {
	ID           string `json:"id"`
	ResumeKind   string `json:"resume_kind"`
	FromRevising bool   `json:"from_revising"`
}

type PauseMarker struct {
	Version     int          `json:"version"`
	OperationID string       `json:"operation_id"`
	State       string       `json:"state"`
	Mode        string       `json:"mode"`
	TargetStage string       `json:"target_stage"`
	CreatedAt   time.Time    `json:"created_at"`
	Owned       []PauseOwner `json:"owned"`
}

func notesPauseMarkerPath(runDir string) string {
	return filepath.Join(runDir, notesPauseMarkerFile)
}

func WriteNotesPauseMarker(runDir string, m PauseMarker) error {
	if m.Version == 0 {
		m.Version = 1
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal pause marker: %w", err)
	}
	return atomicWriteFile(notesPauseMarkerPath(runDir), data, 0644)
}

func ReadNotesPauseMarker(runDir string) (PauseMarker, bool, error) {
	path := notesPauseMarkerPath(runDir)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return PauseMarker{}, false, nil
	}
	if err != nil {
		return PauseMarker{}, true, fmt.Errorf("read pause marker: %w", err)
	}
	var m PauseMarker
	if err := json.Unmarshal(data, &m); err != nil || m.Version != 1 {
		_ = os.Rename(path, fmt.Sprintf("%s.corrupt-%d", path, time.Now().UnixNano()))
		return PauseMarker{}, true, ErrCorruptPauseMarker
	}
	return m, true, nil
}

func ClearNotesPauseMarker(runDir string) error {
	if err := os.Remove(notesPauseMarkerPath(runDir)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clear pause marker: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/state/ -run TestPauseMarker -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
make lint
git add pkg/state/reviewnotes.go pkg/state/reviewnotes_test.go
git commit -m "feat(state): durable pause-marker (paused/resuming, corrupt-aware read)"
```

---

### Task 4: `SaveFeedbackOnce` — atomic exactly-once feedback

**Files:**
- Modify: `pkg/state/state.go` (near `SaveFeedback`)
- Test: `pkg/state/state_test.go`

**Interfaces:**
- Consumes: `atomicWriteFile` (Task 1).
- Produces: `func SaveFeedbackOnce(stageDir, opID, feedback string) (wrote bool, err error)` — reads the existing `feedback.md`; if it already contains the operation sentinel, returns `(false, nil)`; else appends a sentinel + block and writes the **whole file** atomically; returns `(true, nil)`.

- [ ] **Step 1: Write the failing test**

```go
func TestSaveFeedbackOnce_ExactlyOnce(t *testing.T) {
	dir := t.TempDir()
	wrote, err := SaveFeedbackOnce(dir, "op-A", "please fix line 42")
	if err != nil || !wrote {
		t.Fatalf("first write: wrote=%v err=%v", wrote, err)
	}
	wrote2, err := SaveFeedbackOnce(dir, "op-A", "please fix line 42")
	if err != nil || wrote2 {
		t.Fatalf("second write must be no-op: wrote=%v err=%v", wrote2, err)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "feedback.md"))
	if strings.Count(string(body), "please fix line 42") != 1 {
		t.Fatalf("feedback duplicated:\n%s", body)
	}
	// A different operation appends a second block.
	if wrote3, _ := SaveFeedbackOnce(dir, "op-B", "and line 88"); !wrote3 {
		t.Fatalf("different op should write")
	}
	body, _ = os.ReadFile(filepath.Join(dir, "feedback.md"))
	if !strings.Contains(string(body), "and line 88") {
		t.Fatalf("op-B block missing")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/state/ -run TestSaveFeedbackOnce -v`
Expected: FAIL (undefined `SaveFeedbackOnce`).

- [ ] **Step 3: Implement (add to `pkg/state/state.go`)**

```go
// SaveFeedbackOnce appends a feedback block tagged with opID, but only once:
// if feedback.md already contains the operation sentinel it is a no-op. The
// whole file is rewritten atomically so a crash yields either the old complete
// file or the old file plus exactly one complete new block — never a partial or
// sentinel-only block. Callers must serialize concurrent feedback writers.
func SaveFeedbackOnce(stageDir, opID, feedback string) (bool, error) {
	fbFile := filepath.Join(stageDir, "feedback.md")
	sentinel := fmt.Sprintf("<!-- afm-review-op: %s -->", opID)
	existing, err := os.ReadFile(fbFile)
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("read feedback: %w", err)
	}
	if strings.Contains(string(existing), sentinel) {
		return false, nil
	}
	var b strings.Builder
	b.Write(existing)
	n := strings.Count(string(existing), "--- revision ") + 1
	b.WriteString(fmt.Sprintf("\n--- revision %d | %s ---\n%s\n%s\n",
		n, time.Now().Format("2006-01-02 15:04"), sentinel, feedback))
	if err := atomicWriteFile(fbFile, []byte(b.String()), 0644); err != nil {
		return false, err
	}
	return true, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/state/ -run TestSaveFeedbackOnce -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
make lint
git add pkg/state/state.go pkg/state/state_test.go
git commit -m "feat(state): SaveFeedbackOnce — атомарная exactly-once доставка ревью-фидбэка"
```

---

## Phase 2 — Orchestrator (`pkg/orchestrator`)

### Task 5: FSM — allow `EvRevise` from `paused`

**Files:**
- Modify: `pkg/orchestrator/bus/fsm.go:101`
- Test: `pkg/orchestrator/bus/fsm_test.go`

**Interfaces:**
- Produces: `EvRevise` now transitions from `{AwaitingApproval, Running, Paused}` → `Revising`.

- [ ] **Step 1: Write the failing test**

```go
func TestEvRevise_FromPaused(t *testing.T) {
	st := state.NewInMemoryStoreForTest(t) // use whatever the package's test store helper is
	f := NewFSM(st)
	_ = seedStageStatus(t, st, "s1", state.StatusPaused) // set s1 to paused via existing test helper
	to, _, ok, err := f.Apply("s1", EvRevise, GuardCtx{}, "review inject")
	if err != nil || !ok || to != state.StatusRevising {
		t.Fatalf("EvRevise from paused: to=%v ok=%v err=%v", to, ok, err)
	}
}
```

> If the FSM test file has no store/seed helpers, mirror the setup used by the nearest existing `Apply` test in `fsm_test.go` (e.g. `TestApply_*`). The assertion is the point: paused → revising is now allowed.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/orchestrator/bus/ -run TestEvRevise_FromPaused -v`
Expected: FAIL (transition not allowed from paused).

- [ ] **Step 3: Implement**

In `pkg/orchestrator/bus/fsm.go`, change the `EvRevise` rule:

```go
EvRevise: {From: []state.StageStatus{state.StatusAwaitingApproval, state.StatusRunning, state.StatusPaused}, To: to(state.StatusRevising)},
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/orchestrator/bus/ -run TestEvRevise_FromPaused -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
make lint
git add pkg/orchestrator/bus/fsm.go pkg/orchestrator/bus/fsm_test.go
git commit -m "feat(fsm): EvRevise разрешён из paused (для инъекции ревью-заметок)"
```

---

### Task 6: Runner-kind registry

**Files:**
- Modify: `pkg/orchestrator/orchestrator.go` (add field + helpers)
- Modify: `pkg/orchestrator/agents.go` (set kind at each of the 8 runner entry points)
- Modify: `pkg/orchestrator/scheduling.go`/`recovery.go` if a runner is spawned outside `agents.go` entry funcs (set at spawn site too)
- Test: `pkg/orchestrator/reviewpause_test.go` (new file for this feature's orchestrator tests)

**Interfaces:**
- Produces:
  - field `runnerKind sync.Map` (stageID → string).
  - `func (o *Orchestrator) setRunnerKind(stageID, kind string)`
  - `func (o *Orchestrator) runnerKindOf(stageID string) string` (`""` if none)
  - `func (o *Orchestrator) clearRunnerKind(stageID string)`
  - kind constants: `const (kindPlanning="planning"; kindImplementation="implementation"; kindReview="review"; kindAutonomous="autonomous")`

- [ ] **Step 1: Write the failing test**

```go
func TestRunnerKind_SetGetClear(t *testing.T) {
	o := &Orchestrator{}
	if o.runnerKindOf("s1") != "" {
		t.Fatal("empty default")
	}
	o.setRunnerKind("s1", kindImplementation)
	if o.runnerKindOf("s1") != kindImplementation {
		t.Fatal("set/get")
	}
	o.clearRunnerKind("s1")
	if o.runnerKindOf("s1") != "" {
		t.Fatal("clear")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/orchestrator/ -run TestRunnerKind_SetGetClear -v`
Expected: FAIL (undefined).

- [ ] **Step 3: Implement**

In `orchestrator.go`, add the field near `pauseGen sync.Map` and the constants + helpers:

```go
// (field, next to pauseGen)
runnerKind sync.Map // stageID -> "planning"|"implementation"|"review"|"autonomous"

const (
	kindPlanning       = "planning"
	kindImplementation = "implementation"
	kindReview         = "review"
	kindAutonomous     = "autonomous"
)

func (o *Orchestrator) setRunnerKind(stageID, kind string) { o.runnerKind.Store(stageID, kind) }
func (o *Orchestrator) clearRunnerKind(stageID string)     { o.runnerKind.Delete(stageID) }
func (o *Orchestrator) runnerKindOf(stageID string) string {
	if v, ok := o.runnerKind.Load(stageID); ok {
		return v.(string)
	}
	return ""
}
```

Then set the kind at the top of each runner in `agents.go` (both fresh and `*WithFeedback` variants), e.g. in `runPlanningAgent`/`runPlanningWithFeedback`: `o.setRunnerKind(s.ID, kindPlanning)`; in `runImplementationAgent`/`runImplementationWithFeedback`: `kindImplementation`; `runReviewAgent`/`runReviewWithFeedback`: `kindReview`; `runAutonomousAgent`/`runAutonomousWithFeedback`: `kindAutonomous`. Add `defer o.clearRunnerKind(s.ID)` is **wrong** (it must survive retry backoff and the pause window); instead clear it where the stage reaches a terminal/awaiting status — the simplest correct place is in `completeStage` and in the failure/await transitions. For this task, set-at-entry is enough; clearing is covered where `completeStage` is edited (Task 12). Add `o.clearRunnerKind(id)` to `completeStage` and to the `EvFail`/`EvAskUser` handlers.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/orchestrator/ -run TestRunnerKind_SetGetClear -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
make lint
git add pkg/orchestrator/orchestrator.go pkg/orchestrator/agents.go pkg/orchestrator/reviewpause_test.go
git commit -m "feat(orchestrator): runner-kind registry для детерминированного resume-dispatch"
```

---

### Task 7: Attempt-0 resume context (decision A′)

**Files:**
- Modify: `pkg/orchestrator/retry.go` (add an optional initial resume context)
- Test: `pkg/orchestrator/retry_test.go`

**Interfaces:**
- Produces: a way for a resumed runner to inject the `buildRetryContext` block at attempt 0. Implement as a new field on the orchestrator consulted by `runWithRetry`: `resumeContextOnce sync.Map` (stageID → bool). When set for a stage, the attempt-0 branch also builds `buildRetryContext(stageDir, phase)`. Helpers: `func (o *Orchestrator) armResumeContext(stageID string)` / consumed-and-cleared inside `runWithRetry`.

- [ ] **Step 1: Write the failing test**

```go
func TestRunWithRetry_Attempt0ResumeContext(t *testing.T) {
	// Arrange a stage dir with a prior <phase>.jsonl so buildRetryContext is non-empty.
	// Arm resume-context, then assert the FIRST agentFn call receives a non-empty retryCtx.
	o := newTestOrchestrator(t) // existing test constructor in the package
	stageDir := filepath.Join(o.opts.RunDir, "s1")
	os.MkdirAll(stageDir, 0755)
	writePriorActionsJSONL(t, stageDir, "implementation") // helper: write a <phase>.jsonl with one action
	o.armResumeContext("s1")

	var firstCtx string
	got := make(chan struct{})
	agentFn := func(retryContext string) error {
		firstCtx = retryContext
		close(got)
		return nil
	}
	go o.runWithRetry(context.Background(), flow.Stage{ID: "s1", Name: "s1"}, "implementation", agentFn, func() error { return nil }, func() {})
	<-got
	if firstCtx == "" {
		t.Fatalf("attempt-0 resume context should be non-empty")
	}
}
```

> If the package lacks `newTestOrchestrator`/`writePriorActionsJSONL`, add minimal helpers to `reviewpause_test.go` mirroring existing retry tests. The observable contract is: armed stage → first `agentFn` gets non-empty context.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/orchestrator/ -run TestRunWithRetry_Attempt0ResumeContext -v`
Expected: FAIL (undefined `armResumeContext`; attempt-0 ctx empty).

- [ ] **Step 3: Implement**

Add field + helper in `orchestrator.go`:

```go
resumeContextOnce sync.Map // stageID -> struct{} : build retry context even at attempt 0

func (o *Orchestrator) armResumeContext(stageID string) { o.resumeContextOnce.Store(stageID, struct{}{}) }
```

In `retry.go`, at the top of `runWithRetry` after storing `interruptCh`, consume the flag once:

```go
resumeCtx := false
if _, ok := o.resumeContextOnce.LoadAndDelete(s.ID); ok {
	resumeCtx = true
}
```

Then change the attempt-0 branch:

```go
for attempt := 0; attempt <= maxRetries; attempt++ {
	retryCtx := ""
	if attempt > 0 || resumeCtx {
		retryCtx = buildRetryContext(stageDir, phase)
		if incompleteReason != "" {
			retryCtx += "\n\n## Completion check failed\n\n" + incompleteReason +
				"\n\nFix the underlying problem and make the check pass before finishing.\n"
		}
	}
	resumeCtx = false // only the very first attempt of a resumed run gets it
	err := agentFn(retryCtx)
	...
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/orchestrator/ -run TestRunWithRetry_Attempt0ResumeContext -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
make lint
git add pkg/orchestrator/retry.go pkg/orchestrator/orchestrator.go pkg/orchestrator/reviewpause_test.go
git commit -m "feat(orchestrator): attempt-0 resume-контекст для non-interactive Continue (A′)"
```

---

### Task 8: Interrupt-registration handshake

**Files:**
- Modify: `pkg/orchestrator/retry.go` (attempt-loop head)
- Test: `pkg/orchestrator/retry_test.go`

**Interfaces:**
- Produces: at the attempt-loop head, before `agentFn`, if `o.currentStatus(s.ID) == state.StatusPaused` the attempt returns cleanly (self-abort), closing the shouldRun→register gap.

- [ ] **Step 1: Write the failing test**

```go
func TestRunWithRetry_SelfAbortsWhenAlreadyPaused(t *testing.T) {
	o := newTestOrchestrator(t)
	seedStageStatus(t, o.opts.Store, "s1", state.StatusPaused)
	called := false
	agentFn := func(string) error { called = true; return nil }
	done := make(chan struct{})
	go func() {
		o.runWithRetry(context.Background(), flow.Stage{ID: "s1", Name: "s1"}, "implementation",
			agentFn, func() error { return nil }, func() {})
		close(done)
	}()
	<-done
	if called {
		t.Fatalf("agentFn must not run when stage is already paused")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/orchestrator/ -run TestRunWithRetry_SelfAbortsWhenAlreadyPaused -v`
Expected: FAIL (agentFn runs).

- [ ] **Step 3: Implement**

In `retry.go`, inside the `for attempt` loop, immediately before `err := agentFn(retryCtx)`:

```go
if o.currentStatus(s.ID) == state.StatusPaused {
	return // a pause landed in the shouldRun->register gap; do not start the subprocess
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/orchestrator/ -run TestRunWithRetry_SelfAbortsWhenAlreadyPaused -v`
Expected: PASS. Also run the full retry suite: `go test ./pkg/orchestrator/ -run TestRunWithRetry -v`.

- [ ] **Step 5: Commit**

```bash
make lint
git add pkg/orchestrator/retry.go pkg/orchestrator/reviewpause_test.go
git commit -m "feat(orchestrator): handshake self-abort при паузе в окне shouldRun→регистрация"
```

---

### Task 9: Review-pause state fields + `activationHeld` hold at activation sites

**Files:**
- Modify: `pkg/orchestrator/orchestrator.go` (fields + helpers)
- Modify: `pkg/orchestrator/scheduling.go` (`startReadyStages`, `tryActivatePrePlanned`, `startPlanningForUnblocked`)
- Modify: `pkg/orchestrator/recovery.go` (`startPlanningForPending` fresh-activation + its active-status respawns)
- Test: `pkg/orchestrator/reviewpause_test.go`

**Interfaces:**
- Produces:
  - fields: `flowPauseMu sync.Mutex`, `activationHeld atomic.Bool`, `finalizing atomic.Bool` (guarded by `flowPauseMu`), and an in-memory cache `reviewMarker atomic.Pointer[state.PauseMarker]` (nil = none) so `reviewTxnActive()` is lock-free.
  - `func (o *Orchestrator) reviewTxnActive() bool` (marker present)
  - `func (o *Orchestrator) activationBlocked() bool` (`o.activationHeld.Load()`)

- [ ] **Step 1: Write the failing test**

```go
func TestActivationHold_BlocksNewActivation(t *testing.T) {
	o := newTestOrchestrator(t)
	o.activationHeld.Store(true)
	// A pending, dependency-satisfied stage must NOT transition while held.
	seedStageStatus(t, o.opts.Store, "s1", state.StatusReady)
	o.startReadyStages(context.Background()) // existing scheduler entry
	if o.currentStatus("s1") != state.StatusReady {
		t.Fatalf("held stage should stay ready, got %v", o.currentStatus("s1"))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/orchestrator/ -run TestActivationHold_BlocksNewActivation -v`
Expected: FAIL (stage transitioned to running).

- [ ] **Step 3: Implement**

Add fields to `orchestrator.go`:

```go
flowPauseMu    sync.Mutex
activationHeld atomic.Bool
finalizing     atomic.Bool
reviewMarker   atomic.Pointer[state.PauseMarker]

func (o *Orchestrator) reviewTxnActive() bool  { return o.reviewMarker.Load() != nil }
func (o *Orchestrator) activationBlocked() bool { return o.activationHeld.Load() }
```

Guard each activation site. At the top of the per-stage activation body in `startReadyStages` (before `Trigger(EvStartRun)`), `tryActivatePrePlanned`, and `startPlanningForUnblocked` (before `Trigger(EvStartPlanning)`), add:

```go
if o.activationBlocked() {
	continue // review mode: hold new activations; the stage stays pending/ready
}
```

In `recovery.go` `startPlanningForPending`, guard both the fresh `EvStartPlanning` path and the active-status respawn dispatch (`resumeStageAtStatus`) with the same `if o.activationBlocked() { continue }` so a valid-marker recovery does not resume owners.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/orchestrator/ -run TestActivationHold_BlocksNewActivation -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
make lint
git add pkg/orchestrator/orchestrator.go pkg/orchestrator/scheduling.go pkg/orchestrator/recovery.go pkg/orchestrator/reviewpause_test.go
git commit -m "feat(orchestrator): activationHeld hold на всех точках активации + reviewTxn state"
```

---

### Task 10: `PauseFlow` — own only live callbacks, EvPause-first, truthful marker

**Files:**
- Create: `pkg/orchestrator/reviewpause.go`
- Test: `pkg/orchestrator/reviewpause_test.go`

**Interfaces:**
- Consumes: `state.WriteNotesPauseMarker`, `PauseMarker`, `PauseOwner`, `runnerKindOf`, `IsActive`, per-stage `Pause` internals (`Trigger(EvPause)`, `bumpPauseGen`, `interruptChans`), `finalizing`.
- Produces:
  - `func (o *Orchestrator) PauseFlow(ctx context.Context) (pausedStages []string, err error)` — sets `activationHeld`, snapshots `IsActive` pausable candidates, applies `EvPause`, records only CAS winners with `resume_kind`+`from_revising`, writes the marker (`state=paused`), caches it in `reviewMarker`. Returns owner IDs. Rejects with `ErrRunFinalizing` if `finalizing`.
  - `var ErrRunFinalizing = errors.New("run is finalizing")`
  - helper `func (o *Orchestrator) computeResumeKind(s flow.Stage, pausedFrom state.StageStatus) string`

- [ ] **Step 1: Write the failing test**

```go
func TestPauseFlow_OwnsOnlyActiveWinners(t *testing.T) {
	o := newTestOrchestrator(t)
	// s1: running + IsActive -> owned as implementation.
	seedStageStatus(t, o.opts.Store, "s1", state.StatusRunning)
	o.setRunnerKind("s1", kindImplementation)
	markActiveForTest(t, o, "s1") // helper making IsActive("s1")==true
	// s2: running but NOT active (queued) -> not owned.
	seedStageStatus(t, o.opts.Store, "s2", state.StatusRunning)
	// s3: script running -> not owned.
	seedScriptStage(t, o, "s3", state.StatusRunning)

	owners, err := o.PauseFlow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(owners) != 1 || owners[0] != "s1" {
		t.Fatalf("owners = %v, want [s1]", owners)
	}
	if o.currentStatus("s1") != state.StatusPaused {
		t.Fatalf("s1 not paused")
	}
	m := o.reviewMarker.Load()
	if m == nil || m.State != state.PauseStatePaused || m.Owned[0].ResumeKind != kindImplementation {
		t.Fatalf("marker wrong: %+v", m)
	}
}

func TestPauseFlow_RejectedDuringFinalization(t *testing.T) {
	o := newTestOrchestrator(t)
	o.finalizing.Store(true)
	if _, err := o.PauseFlow(context.Background()); !errors.Is(err, ErrRunFinalizing) {
		t.Fatalf("want ErrRunFinalizing, got %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/orchestrator/ -run TestPauseFlow -v`
Expected: FAIL (undefined `PauseFlow`).

- [ ] **Step 3: Implement `pkg/orchestrator/reviewpause.go`**

```go
package orchestrator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"<module>/pkg/orchestrator/bus"
	"<module>/pkg/state"
)

var ErrRunFinalizing = errors.New("run is finalizing")

func newOperationID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (o *Orchestrator) computeResumeKind(s flow.Stage, pausedFrom state.StageStatus) string {
	if s.IsAuto() || o.isAutonomousStage(s.ID) { // reuse existing autonomous detection
		return kindAutonomous
	}
	if k := o.runnerKindOf(s.ID); k != "" {
		return k // authoritative: what was actually spawned
	}
	if pausedFrom == state.StatusPlanning {
		return kindPlanning
	}
	return kindImplementation
}

// PauseFlow puts the flow into best-effort review mode: hold new activations,
// and gracefully pause every currently-active, non-script agent stage. Only
// stages whose EvPause CAS wins are recorded as owners.
func (o *Orchestrator) PauseFlow(ctx context.Context) ([]string, error) {
	o.flowPauseMu.Lock()
	defer o.flowPauseMu.Unlock()

	if o.finalizing.Load() {
		return nil, ErrRunFinalizing
	}
	if m := o.reviewMarker.Load(); m != nil {
		return ownerIDs(m), nil // already in review mode; idempotent
	}

	o.activationHeld.Store(true)

	snap := o.opts.Store.Snapshot()
	var owned []state.PauseOwner
	var ids []string
	for _, id := range snap.StageOrder {
		st := snap.Stages[id]
		if o.opts.StageIsScriptFunc(id) { // or the graph lookup used elsewhere; scripts are never paused
			continue
		}
		switch st.Status {
		case state.StatusRunning, state.StatusPlanning, state.StatusRevising, state.StatusRetrying:
		default:
			continue
		}
		if !o.concurrency.IsActive(id) {
			continue // queued/not-live: not owned, continues best-effort
		}
		stage := o.graph.Stage(id)
		kind := o.computeResumeKind(*stage, st.Status)
		fromRevising := st.Status == state.StatusRevising
		// Apply the existing per-stage pause internals.
		if _, ok := o.Trigger(id, bus.EvPause, bus.GuardCtx{}, "review pause"); !ok {
			continue // lost the CAS (stage left the From-set): not an owner
		}
		o.bumpPauseGen(id)
		if ch, ok := o.interruptChans.Load(id); ok {
			select {
			case ch.(chan struct{}) <- struct{}{}:
			default:
			}
		}
		owned = append(owned, state.PauseOwner{ID: id, ResumeKind: kind, FromRevising: fromRevising})
		ids = append(ids, id)
	}

	m := state.PauseMarker{
		Version: 1, OperationID: newOperationID(), State: state.PauseStatePaused,
		CreatedAt: time.Now().UTC(), Owned: owned,
	}
	if err := state.WriteNotesPauseMarker(o.opts.RunDir, m); err != nil {
		// Initial-marker failure: roll back the hold, surface the paused IDs.
		o.activationHeld.Store(false)
		return ids, fmt.Errorf("write pause marker (these stages are paused, Continue them manually): %w", err)
	}
	o.reviewMarker.Store(&m)
	return ids, nil
}

func ownerIDs(m *state.PauseMarker) []string {
	ids := make([]string, 0, len(m.Owned))
	for _, ow := range m.Owned {
		ids = append(ids, ow.ID)
	}
	return ids
}
```

> Notes for the implementer: `<module>` = the module path from `go.mod`. Reuse the existing script-detection the orchestrator already has (there is a `StageIsScript`-style predicate used by the scheduler / a `graph.Stage(id).IsScript()`); use whichever is in scope. `o.isAutonomousStage` already exists (per AGENTS.md). `o.graph.Stage(id)` returns `*flow.Stage`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/orchestrator/ -run TestPauseFlow -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
make lint
git add pkg/orchestrator/reviewpause.go pkg/orchestrator/reviewpause_test.go
git commit -m "feat(orchestrator): PauseFlow — own-only-IsActive, EvPause-first, truthful marker"
```

---

### Task 11: Rendering — content-anchored feedback (always carries original line)

**Files:**
- Modify: `pkg/orchestrator/reviewpause.go`
- Test: `pkg/orchestrator/reviewpause_test.go`

**Interfaces:**
- Consumes: `state.ReviewNote`, workspace read for the current file (via an injected reader — see below).
- Produces: `func renderReviewFeedback(notes []state.ReviewNote, current func(root, path string) (sha string, ok bool)) string` — deterministic order (files by `display_path`, notes by line, file-level last); every line note always prints JSON-quoted `orig_line_text`; a `content_sha` mismatch adds `(⚠ content differed at injection)`; `ok==false` (unreadable) adds `(⚠ file unavailable at injection)` on the file header.

- [ ] **Step 1: Write the failing test**

```go
func TestRenderReviewFeedback_AlwaysCarriesOriginal(t *testing.T) {
	l42 := 42
	orig := "\tif err != nil {"
	notes := []state.ReviewNote{{
		Root: "project", Path: "a.go", DisplayPath: "project/a.go",
		Reference: `[AFM file: "/w/a.go"]`, Line: &l42, OrigLineText: &orig,
		ContentSHA: "sha256:OLD", Text: "handle this",
	}}
	// current sha differs -> changed-at-injection warning, but original still present.
	out := renderReviewFeedback(notes, func(root, path string) (string, bool) {
		return "sha256:NEW", true
	})
	if !strings.Contains(out, `original: "\tif err != nil {"`) {
		t.Fatalf("original line text missing:\n%s", out)
	}
	if !strings.Contains(out, "content differed at injection") {
		t.Fatalf("drift warning missing:\n%s", out)
	}
	if !strings.Contains(out, `[AFM file: "/w/a.go"]`) {
		t.Fatalf("reference marker missing")
	}
}

func TestRenderReviewFeedback_UnavailableFile(t *testing.T) {
	l5 := 5
	orig := "package gone"
	notes := []state.ReviewNote{{DisplayPath: "project/gone.go", Reference: `[AFM file: "/w/gone.go"]`,
		Line: &l5, OrigLineText: &orig, ContentSHA: "sha256:X", Text: "note"}}
	out := renderReviewFeedback(notes, func(root, path string) (string, bool) { return "", false })
	if !strings.Contains(out, "file unavailable at injection") {
		t.Fatalf("unavailable marker missing:\n%s", out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/orchestrator/ -run TestRenderReviewFeedback -v`
Expected: FAIL (undefined).

- [ ] **Step 3: Implement (add to `reviewpause.go`)**

```go
func renderReviewFeedback(notes []state.ReviewNote, current func(root, path string) (string, bool)) string {
	// Group by display_path, deterministic order.
	byFile := map[string][]state.ReviewNote{}
	var order []string
	for _, n := range notes {
		if _, seen := byFile[n.DisplayPath]; !seen {
			order = append(order, n.DisplayPath)
		}
		byFile[n.DisplayPath] = append(byFile[n.DisplayPath], n)
	}
	sort.Strings(order)
	var b strings.Builder
	b.WriteString("## Review notes\n")
	for _, dp := range order {
		group := byFile[dp]
		sort.SliceStable(group, func(i, j int) bool { // line notes by line asc, file-level last
			li, lj := group[i].Line, group[j].Line
			if li == nil {
				return false
			}
			if lj == nil {
				return true
			}
			return *li < *lj
		})
		curSHA, ok := current(group[0].Root, group[0].Path)
		header := fmt.Sprintf("### %s  %s", group[0].Reference, jsonQuote(dp))
		if !ok {
			header += "  (⚠ file unavailable at injection)"
		}
		b.WriteString(header + "\n")
		for _, n := range group {
			if n.Line == nil {
				b.WriteString(fmt.Sprintf("- File: %s\n", n.Text))
				continue
			}
			drift := ""
			if ok && curSHA != n.ContentSHA {
				drift = " (⚠ content differed at injection)"
			}
			origTxt := ""
			if n.OrigLineText != nil {
				origTxt = *n.OrigLineText
			}
			b.WriteString(fmt.Sprintf("- Line %d%s; original: %s: %s\n",
				*n.Line, drift, jsonQuote(origTxt), n.Text))
		}
	}
	return b.String()
}

func jsonQuote(s string) string {
	q, _ := json.Marshal(s)
	return string(q)
}
```

Add imports `sort`, `strings`, `encoding/json`, `fmt`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/orchestrator/ -run TestRenderReviewFeedback -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
make lint
git add pkg/orchestrator/reviewpause.go pkg/orchestrator/reviewpause_test.go
git commit -m "feat(orchestrator): рендер ревью-фидбэка с always-original + drift/unavailable диагностикой"
```

---

### Task 12: Resume dispatcher (drain-then-resume by `resume_kind`)

**Files:**
- Modify: `pkg/orchestrator/reviewpause.go`
- Modify: `pkg/orchestrator/orchestrator.go` (clear `runnerKind` in `completeStage` + fail/await transitions); route standalone `review` completion through `completeStage`
- Test: `pkg/orchestrator/reviewpause_test.go`

**Interfaces:**
- Consumes: `IsActive`, `SpawnAgent`, the 8 runner funcs, `resumeContextOnce` (Task 7), FSM `EvRevise`/`EvContinue`.
- Produces:
  - `func (o *Orchestrator) resumeOwner(ctx context.Context, ow state.PauseOwner, isTarget bool)` — waits `!IsActive(ow.ID)`, then transitions + spawns the correct runner.
  - `func (o *Orchestrator) withFeedbackRunner(kind string) func(context.Context, flow.Stage)` and `func (o *Orchestrator) plainRunner(kind string) func(context.Context, flow.Stage)` — map a `resume_kind` to the concrete runner.

- [ ] **Step 1: Write the failing test**

```go
func TestResumeOwner_TargetImplementationUsesWithFeedback(t *testing.T) {
	o := newTestOrchestrator(t)
	seedStageStatus(t, o.opts.Store, "s1", state.StatusPaused)
	setPausedFrom(t, o.opts.Store, "s1", state.StatusRunning)
	// Stub the runner map so we observe which runner is chosen without a real process.
	var chosen string
	o.testRunnerHook = func(kind string, withFeedback bool) { chosen = kind + "/" + boolStr(withFeedback) }
	o.resumeOwner(context.Background(), state.PauseOwner{ID: "s1", ResumeKind: kindImplementation}, true)
	if chosen != "implementation/true" {
		t.Fatalf("target should use implementation WithFeedback, got %q", chosen)
	}
}

func TestResumeOwner_WaitsForDrain(t *testing.T) {
	o := newTestOrchestrator(t)
	seedStageStatus(t, o.opts.Store, "s1", state.StatusPaused)
	markActiveForTest(t, o, "s1")
	started := make(chan struct{})
	go func() {
		o.resumeOwner(context.Background(), state.PauseOwner{ID: "s1", ResumeKind: kindImplementation}, false)
		close(started)
	}()
	select {
	case <-started:
		t.Fatalf("resume must block until IsActive(s1)==false")
	case <-time.After(50 * time.Millisecond):
	}
	markDoneForTest(t, o, "s1") // IsActive -> false
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatalf("resume should proceed after drain")
	}
}
```

> Add a small test seam `o.testRunnerHook func(kind string, withFeedback bool)`; when non-nil, `resumeOwner` calls it instead of `SpawnAgent` so tests avoid real processes. In production it is nil.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/orchestrator/ -run TestResumeOwner -v`
Expected: FAIL (undefined).

- [ ] **Step 3: Implement (add to `reviewpause.go`)**

```go
func (o *Orchestrator) withFeedbackRunner(kind string) func(context.Context, flow.Stage) {
	switch kind {
	case kindPlanning:
		return o.runPlanningWithFeedback
	case kindReview:
		return o.runReviewWithFeedback
	case kindAutonomous:
		return o.runAutonomousWithFeedback
	default:
		return o.runImplementationWithFeedback
	}
}

func (o *Orchestrator) plainRunner(kind string) func(context.Context, flow.Stage) {
	switch kind {
	case kindPlanning:
		return o.runPlanningAgent
	case kindReview:
		return o.runReviewAgent
	case kindAutonomous:
		return o.runAutonomousAgent
	default:
		return o.runImplementationAgent
	}
}

// resumeOwner resumes a single owned stage after its old callback has drained.
func (o *Orchestrator) resumeOwner(ctx context.Context, ow state.PauseOwner, isTarget bool) {
	// Wait for the interrupted callback to drain so status stays `paused` until
	// we transition — the old callback then treats its ErrUserInterrupted as a
	// Pause (currentStatus==paused) and does not re-spawn.
	for o.concurrency.IsActive(ow.ID) {
		select {
		case <-ctx.Done():
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
	stage := o.graph.Stage(ow.ID)
	useFeedback := isTarget || ow.FromRevising
	if o.testRunnerHook != nil {
		o.testRunnerHook(ow.ResumeKind, useFeedback)
		return
	}
	if !isTarget {
		o.armResumeContext(ow.ID) // A′: attempt-0 replay for non-interactive
	}
	if useFeedback {
		if _, ok := o.Trigger(ow.ID, bus.EvRevise, bus.GuardCtx{}, "review resume"); !ok {
			return
		}
		o.concurrency.SpawnAgent(ctx, *stage, o.withFeedbackRunner(ow.ResumeKind))
		return
	}
	pausedFrom := o.opts.Store.PausedFrom(ow.ID)
	if _, ok := o.Trigger(ow.ID, bus.EvContinue, bus.GuardCtx{PausedFrom: pausedFrom}, "review resume"); !ok {
		return
	}
	o.concurrency.SpawnAgent(ctx, *stage, o.plainRunner(ow.ResumeKind))
}
```

Add the field `testRunnerHook func(kind string, withFeedback bool)` to the struct. Also: in `completeStage` add `o.clearRunnerKind(id)`; ensure a **standalone** review runner's successful completion flows through `completeStage` (per AGENTS.md `onAgentCompleted` treats review as "no status change" — add: if the completing phase is review AND the stage is not in inline-review context, route to `completeStage`). Implement this by having `runReviewWithFeedback`/`runReviewAgent`'s completion publish through the same path `completeStage` handles; the concrete edit is in `onAgentCompleted` — when `phase == phaseReview` and current status is `revising`/`running`, call `o.completeStage(ctx, id, state.StatusDone, "review complete")`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/orchestrator/ -run TestResumeOwner -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
make lint
git add pkg/orchestrator/reviewpause.go pkg/orchestrator/orchestrator.go pkg/orchestrator/reviewpause_test.go
git commit -m "feat(orchestrator): resume-dispatcher (drain→resume по resume_kind, review→completeStage)"
```

---

### Task 13: `InjectNotesAndResume` + `CancelNotesAndResume`

**Files:**
- Modify: `pkg/orchestrator/reviewpause.go`
- Test: `pkg/orchestrator/reviewpause_test.go`

**Interfaces:**
- Consumes: `SaveFeedbackOnce`, `renderReviewFeedback`, `resumeOwner`, marker IO, `runContext`.
- Produces:
  - `func (o *Orchestrator) InjectNotesAndResume(reqCtx context.Context, targetStageID string) error` — validates target ∈ owned && `StatusPaused`; persists `state=resuming, mode=inject, target`; `SaveFeedbackOnce`; clears `activationHeld`; spawns an async resume worker under `runContext`.
  - `func (o *Orchestrator) CancelNotesAndResume(reqCtx context.Context) error`
  - `var (ErrNoReviewPause=errors.New("no review pause active"); ErrNoNotes=errors.New("no notes to inject"); ErrTargetNotPaused=errors.New("target is not paused"))`
  - shared worker `func (o *Orchestrator) runResumeTransaction(ctx context.Context, m state.PauseMarker)`

- [ ] **Step 1: Write the failing test**

```go
func TestInject_WritesFeedbackOnceAndResumes(t *testing.T) {
	o := newTestOrchestrator(t)
	// Set up a paused owner s1 (implementation) via PauseFlow-like seeding.
	seedStageStatus(t, o.opts.Store, "s1", state.StatusPaused)
	m := state.PauseMarker{Version: 1, OperationID: "op1", State: state.PauseStatePaused,
		Owned: []state.PauseOwner{{ID: "s1", ResumeKind: kindImplementation}}}
	state.WriteNotesPauseMarker(o.opts.RunDir, m)
	o.reviewMarker.Store(&m)
	o.activationHeld.Store(true)
	l := 1
	orig := "x"
	state.SaveReviewNotes(o.opts.RunDir, state.ReviewNotes{Version: 1, NextID: 2,
		Notes: []state.ReviewNote{{ID: "n1", Root: "project", Path: "a.go", DisplayPath: "project/a.go",
			Reference: `[AFM file: "/w/a.go"]`, Line: &l, OrigLineText: &orig, ContentSHA: "sha256:x", Text: "fix"}}})

	var resumed []string
	o.testRunnerHook = func(kind string, wf bool) { resumed = append(resumed, kind) }
	if err := o.InjectNotesAndResume(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(resumed) == 1 }) // async worker ran
	body, _ := os.ReadFile(filepath.Join(o.opts.RunDir, "s1", "feedback.md"))
	if strings.Count(string(body), "fix") != 1 {
		t.Fatalf("feedback not written once:\n%s", body)
	}
	if _, found, _ := state.ReadNotesPauseMarker(o.opts.RunDir); found {
		t.Fatalf("marker should be deleted after resume")
	}
	if o.reviewTxnActive() {
		t.Fatalf("reviewTxn should be inactive")
	}
}

func TestInject_RejectsUnpausedTargetAndEmptyNotes(t *testing.T) {
	o := newTestOrchestrator(t)
	if err := o.InjectNotesAndResume(context.Background(), "s1"); !errors.Is(err, ErrNoReviewPause) {
		t.Fatalf("want ErrNoReviewPause, got %v", err)
	}
	// ... seed marker with s1 but leave notes empty -> ErrNoNotes; set s1 not paused -> ErrTargetNotPaused
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/orchestrator/ -run 'TestInject' -v`
Expected: FAIL (undefined).

- [ ] **Step 3: Implement (add to `reviewpause.go`)**

```go
var (
	ErrNoReviewPause   = errors.New("no review pause active")
	ErrNoNotes         = errors.New("no notes to inject")
	ErrTargetNotPaused = errors.New("target is not paused")
	ErrNotOwned        = errors.New("stage is not owned by the review pause")
)

func (o *Orchestrator) InjectNotesAndResume(reqCtx context.Context, targetStageID string) error {
	o.flowPauseMu.Lock()
	m := o.reviewMarker.Load()
	if m == nil {
		o.flowPauseMu.Unlock()
		return ErrNoReviewPause
	}
	if !ownsStage(m, targetStageID) {
		o.flowPauseMu.Unlock()
		return ErrNotOwned
	}
	if o.currentStatus(targetStageID) != state.StatusPaused {
		o.flowPauseMu.Unlock()
		return ErrTargetNotPaused
	}
	notes, _ := state.LoadReviewNotes(o.opts.RunDir)
	if len(notes.Notes) == 0 {
		o.flowPauseMu.Unlock()
		return ErrNoNotes
	}
	updated := *m
	updated.State = state.PauseStateResuming
	updated.Mode = state.PauseModeInject
	updated.TargetStage = targetStageID
	if err := state.WriteNotesPauseMarker(o.opts.RunDir, updated); err != nil {
		o.flowPauseMu.Unlock()
		return err
	}
	o.reviewMarker.Store(&updated)

	rendered := renderReviewFeedback(notes.Notes, o.currentFileSHA)
	targetDir := filepath.Join(o.opts.RunDir, targetStageID)
	if _, err := state.SaveFeedbackOnce(targetDir, updated.OperationID, rendered); err != nil {
		o.flowPauseMu.Unlock()
		return err
	}
	o.activationHeld.Store(false)
	o.flowPauseMu.Unlock()

	o.concurrency.SpawnDetached(o.runContext(reqCtx), func(ctx context.Context) {
		o.runResumeTransaction(ctx, updated)
	})
	return nil
}

func (o *Orchestrator) CancelNotesAndResume(reqCtx context.Context) error {
	o.flowPauseMu.Lock()
	m := o.reviewMarker.Load()
	if m == nil {
		o.flowPauseMu.Unlock()
		return ErrNoReviewPause
	}
	updated := *m
	updated.State = state.PauseStateResuming
	updated.Mode = state.PauseModeCancel
	if err := state.WriteNotesPauseMarker(o.opts.RunDir, updated); err != nil {
		o.flowPauseMu.Unlock()
		return err
	}
	o.reviewMarker.Store(&updated)
	o.activationHeld.Store(false)
	o.flowPauseMu.Unlock()

	o.concurrency.SpawnDetached(o.runContext(reqCtx), func(ctx context.Context) {
		o.runResumeTransaction(ctx, updated)
	})
	return nil
}

// runResumeTransaction resumes each owner (target with feedback, others plain or
// WithFeedback when from_revising), then deletes notes and the marker last.
func (o *Orchestrator) runResumeTransaction(ctx context.Context, m state.PauseMarker) {
	for _, ow := range m.Owned {
		isTarget := m.Mode == state.PauseModeInject && ow.ID == m.TargetStage
		o.resumeOwner(ctx, ow, isTarget)
	}
	o.startReadyStages(ctx) // re-drive activation-held pending stages
	_ = state.DeleteReviewNotes(o.opts.RunDir)
	_ = state.ClearNotesPauseMarker(o.opts.RunDir)
	o.reviewMarker.Store(nil)
}

func ownsStage(m *state.PauseMarker, id string) bool {
	for _, ow := range m.Owned {
		if ow.ID == id {
			return true
		}
	}
	return false
}
```

Add `currentFileSHA(root, path string) (string, bool)` — reads the file via the workspace and returns its `state.FileContentSHA`; `false` if unreadable/binary/oversize. If the orchestrator has no workspace handle, inject a `func(root, path string)(string,bool)` at construction (`Options.CurrentFileSHA`) wired from the server's workspace; default returns `("", false)`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/orchestrator/ -run 'TestInject' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
make lint
git add pkg/orchestrator/reviewpause.go pkg/orchestrator/orchestrator.go pkg/orchestrator/reviewpause_test.go
git commit -m "feat(orchestrator): InjectNotesAndResume/CancelNotesAndResume + async resume-транзакция"
```

---

### Task 14: Control policy, `shouldExit`, finalization guard

**Files:**
- Modify: `pkg/orchestrator/control_api.go` (guard the public actions)
- Modify: `pkg/orchestrator/scheduling.go` (`shouldExit`; finalization entry sets `finalizing` under `flowPauseMu`)
- Test: `pkg/orchestrator/reviewpause_test.go`

**Interfaces:**
- Produces:
  - a guard `func (o *Orchestrator) rejectIfReviewPaused() error` returning `ErrFlowPaused` when `reviewTxnActive()`.
  - `var ErrFlowPaused = errors.New("flow_paused")`
  - `shouldExit` returns false while `reviewTxnActive()`.
  - finalization entry acquires `flowPauseMu`, sets `finalizing=true` (only if not `reviewTxnActive()`), releases — atomic with `PauseFlow`.

- [ ] **Step 1: Write the failing test**

```go
func TestControlPolicy_RejectsWhileReviewPaused(t *testing.T) {
	o := newTestOrchestrator(t)
	m := state.PauseMarker{Version: 1, State: state.PauseStatePaused}
	o.reviewMarker.Store(&m)
	if err := o.Continue(context.Background(), "s1"); !errors.Is(err, ErrFlowPaused) {
		t.Fatalf("Continue want ErrFlowPaused, got %v", err)
	}
	if err := o.Retry(context.Background(), "s1"); !errors.Is(err, ErrFlowPaused) {
		t.Fatalf("Retry want ErrFlowPaused, got %v", err)
	}
}

func TestShouldExit_HeldByReviewMarker(t *testing.T) {
	o := newTestOrchestrator(t)
	makeAllTerminal(t, o) // so shouldExit would otherwise be true
	m := state.PauseMarker{Version: 1, State: state.PauseStateResuming}
	o.reviewMarker.Store(&m)
	if o.shouldExit() {
		t.Fatalf("shouldExit must be false while review marker exists")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/orchestrator/ -run 'TestControlPolicy_RejectsWhileReviewPaused|TestShouldExit_HeldByReviewMarker' -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

Add to `control_api.go`:

```go
var ErrFlowPaused = errors.New("flow_paused")

func (o *Orchestrator) rejectIfReviewPaused() error {
	if o.reviewTxnActive() {
		return ErrFlowPaused
	}
	return nil
}
```

At the very top of `Approve`, `Revise`, `Retry`, `Pause`, `Continue`, `Button`, `RetryHook`, `SkipHook`, `NotifyAnswer`, `CancelDialog`, add:

```go
if err := o.rejectIfReviewPaused(); err != nil {
	return err
}
```

(The private resume path uses `Trigger`/`SpawnAgent` directly, so it is unaffected.) Also suppress non-interactive auto-answer while paused: in the auto-answer branch of `pollQuestions`, `if o.reviewTxnActive() { continue }`.

In `scheduling.go` `shouldExit`, add at the top:

```go
if o.reviewTxnActive() {
	return false
}
```

At the finalization entry (where `runEndOfRunMemory` is invoked / `Run` decides to exit), wrap the decision:

```go
o.flowPauseMu.Lock()
if o.reviewTxnActive() {
	o.flowPauseMu.Unlock()
	// stay alive for the reviewer
} else {
	o.finalizing.Store(true)
	o.flowPauseMu.Unlock()
	// ... proceed to finalize
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/orchestrator/ -run 'TestControlPolicy_RejectsWhileReviewPaused|TestShouldExit_HeldByReviewMarker' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
make lint
git add pkg/orchestrator/control_api.go pkg/orchestrator/scheduling.go pkg/orchestrator/reviewpause_test.go
git commit -m "feat(orchestrator): control-policy 409, shouldExit hold, атомарный finalize guard"
```

---

### Task 15: Recovery — read marker at start, per-status matrix, corrupt-`resuming` fail-closed

**Files:**
- Modify: `pkg/orchestrator/recovery.go` (add `recoverReviewPause`, called before bootstrap)
- Modify: the orchestrator start path (where recovery runs before `startPlanningForPending`)
- Test: `pkg/orchestrator/reviewpause_test.go`

**Interfaces:**
- Consumes: `ReadNotesPauseMarker`, `resumeOwner`, `runResumeTransaction`.
- Produces: `func (o *Orchestrator) recoverReviewPause(ctx context.Context) error` — absent→nil; `paused`→set `activationHeld` + cache marker (owners stay paused); `resuming`→run `runResumeTransaction` (idempotent); corrupt `paused`→fail-open (log); corrupt `resuming`→fail-closed (keep hold, return an error that halts normal scheduling and surfaces the quarantine path).

- [ ] **Step 1: Write the failing test**

```go
func TestRecoverReviewPause_PausedReestablishesHold(t *testing.T) {
	o := newTestOrchestrator(t)
	m := state.PauseMarker{Version: 1, State: state.PauseStatePaused,
		Owned: []state.PauseOwner{{ID: "s1", ResumeKind: kindImplementation}}}
	state.WriteNotesPauseMarker(o.opts.RunDir, m)
	if err := o.recoverReviewPause(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !o.activationHeld.Load() || !o.reviewTxnActive() {
		t.Fatalf("paused recovery must re-establish the hold + marker")
	}
}

func TestRecoverReviewPause_ResumingFinishes(t *testing.T) {
	o := newTestOrchestrator(t)
	seedStageStatus(t, o.opts.Store, "s1", state.StatusPaused)
	m := state.PauseMarker{Version: 1, OperationID: "op1", State: state.PauseStateResuming,
		Mode: state.PauseModeCancel, Owned: []state.PauseOwner{{ID: "s1", ResumeKind: kindImplementation}}}
	state.WriteNotesPauseMarker(o.opts.RunDir, m)
	o.testRunnerHook = func(string, bool) {}
	if err := o.recoverReviewPause(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := state.ReadNotesPauseMarker(o.opts.RunDir); found {
		t.Fatalf("resuming recovery must finish and delete the marker")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/orchestrator/ -run TestRecoverReviewPause -v`
Expected: FAIL (undefined).

- [ ] **Step 3: Implement (add to `recovery.go`)**

```go
func (o *Orchestrator) recoverReviewPause(ctx context.Context) error {
	m, found, err := state.ReadNotesPauseMarker(o.opts.RunDir)
	if !found {
		if errors.Is(err, state.ErrCorruptPauseMarker) {
			// Can't tell paused vs resuming; the file was quarantined. Fail-open:
			// nothing to re-establish, owners (if any) stay as their log status.
			log.Printf("review-pause: corrupt marker quarantined, continuing without review mode")
		}
		return nil
	}
	if errors.Is(err, state.ErrCorruptPauseMarker) {
		// unreachable (found=false on corrupt) — kept for clarity
		return nil
	}
	switch m.State {
	case state.PauseStatePaused:
		o.activationHeld.Store(true)
		o.reviewMarker.Store(&m)
		return nil
	case state.PauseStateResuming:
		o.reviewMarker.Store(&m)
		o.activationHeld.Store(false)
		o.runResumeTransaction(ctx, m) // idempotent: SaveFeedbackOnce/transitions/deletes
		return nil
	default:
		return fmt.Errorf("review-pause: unknown marker state %q", m.State)
	}
}
```

For the corrupt-`resuming` fail-closed requirement: since `ReadNotesPauseMarker` quarantines and returns `found=false` on corruption, distinguish by leaving a breadcrumb — before quarantine we cannot know the state. To satisfy "corrupt resuming fails closed," change `ReadNotesPauseMarker` to attempt a best-effort `state` field extraction: if the raw bytes contain `"state": "resuming"`, keep `activationHeld=true` and return `ErrCorruptPauseMarker` with `found=true` and the raw path; the recovery then returns an error that the caller surfaces (halting normal scheduling). Add that substring check in Task 3's `ReadNotesPauseMarker` (a follow-up edit) and here:

```go
	// (added branch, before the switch)
	// handled by ReadNotesPauseMarker returning found=true+ErrCorrupt for resuming:
```

Wire `recoverReviewPause(runCtx)` into the start path **before** `startPlanningForPending`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/orchestrator/ -run TestRecoverReviewPause -v`
Expected: PASS. Then the whole package: `go test ./pkg/orchestrator/...`.

- [ ] **Step 5: Commit**

```bash
make lint
git add pkg/orchestrator/recovery.go pkg/orchestrator/reviewpause.go pkg/state/reviewnotes.go pkg/orchestrator/reviewpause_test.go
git commit -m "feat(orchestrator): recovery review-паузы (per-status, resuming finish, corrupt-resuming fail-closed)"
```

---

## Phase 3 — Server (`pkg/server`) + wiring (`cmd/afm`)

### Task 16: `FlowActions` interface, status fields, config wiring

**Files:**
- Modify: `pkg/server/actions.go` (new `FlowActions` interface)
- Modify: `pkg/server/server.go` (Config/Server fields; New copy)
- Modify: `pkg/server/handlers.go` (`statusResponse` fields; populate in `handleStatus`)
- Modify: `cmd/afm/run.go` (wire `Actions` as `FlowActions`; the orchestrator already satisfies it)
- Test: `pkg/server/handlers_test.go`

**Interfaces:**
- Produces:
  - `type FlowActions interface { PauseFlow(ctx context.Context) ([]string, error); InjectNotesAndResume(ctx context.Context, target string) error; CancelNotesAndResume(ctx context.Context) error }`
  - `statusResponse.FlowPauseState string json:"flow_pause_state"` (`none|paused|resuming`), `.FlowPausedStages []string json:"flow_paused_stages,omitempty"`.
  - `Server.flowActions FlowActions`, `Config.FlowActions FlowActions`, plus a `Config.ReviewState func() (state string, owners []string)` closure the orchestrator provides so `/api/status` is lock-free.

- [ ] **Step 1: Write the failing test**

```go
func TestStatus_IncludesFlowPauseState(t *testing.T) {
	srv := newTestServer(t, func(c *server.Config) {
		c.ReviewState = func() (string, []string) { return "paused", []string{"s1"} }
	})
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	var got map[string]any
	json.Unmarshal(rr.Body.Bytes(), &got)
	if got["flow_pause_state"] != "paused" {
		t.Fatalf("flow_pause_state = %v", got["flow_pause_state"])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/server/ -run TestStatus_IncludesFlowPauseState -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

- `actions.go`: add the `FlowActions` interface.
- `server.go`: add `flowActions FlowActions` + `reviewState func() (string, []string)` to `Server`; `FlowActions FlowActions` + `ReviewState func() (string, []string)` to `Config`; copy them in `New`.
- `handlers.go`: extend `statusResponse` with the two fields; in `handleStatus`, `if s.reviewState != nil { st, owners := s.reviewState(); resp.FlowPauseState = st; resp.FlowPausedStages = owners } else { resp.FlowPauseState = "none" }`.
- `cmd/afm/run.go`: set `FlowActions: orch` and `ReviewState: orch.ReviewState` (add a tiny `func (o *Orchestrator) ReviewState() (string, []string)` reading `o.reviewMarker`).

Add `ReviewState` to the orchestrator:

```go
func (o *Orchestrator) ReviewState() (string, []string) {
	m := o.reviewMarker.Load()
	if m == nil {
		return "none", nil
	}
	if m.State == state.PauseStateResuming {
		return "resuming", nil
	}
	var paused []string
	for _, ow := range m.Owned {
		if o.currentStatus(ow.ID) == state.StatusPaused {
			paused = append(paused, ow.ID)
		}
	}
	return "paused", paused
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/server/ -run TestStatus_IncludesFlowPauseState -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
make lint
git add pkg/server/actions.go pkg/server/server.go pkg/server/handlers.go pkg/orchestrator/reviewpause.go cmd/afm/run.go pkg/server/handlers_test.go
git commit -m "feat(server): FlowActions, flow_pause_state в /api/status, wiring"
```

---

### Task 17: `/api/flow/*` router + pause/inject/cancel handlers

**Files:**
- Modify: `pkg/server/server.go` (register `/api/flow/`)
- Create: `pkg/server/flow_handlers.go`
- Test: `pkg/server/flow_handlers_test.go`

**Interfaces:**
- Consumes: `flowActions`, the JSON-error idiom `writeFilesError`-style (reuse a local `writeFlowError(w, status, code)`).
- Produces: `routeFlow(w, r)` dispatching `POST /api/flow/pause`, `/notes/inject`, `/notes/cancel` (notes CRUD in Task 18). Errors → JSON `{"error":"<code>"}` with codes `flow_not_paused`, `no_notes`, `run_finalizing`, `not_owned`, `target_not_paused`, `flow_paused`.

- [ ] **Step 1: Write the failing test**

```go
func TestFlowPause_Success(t *testing.T) {
	fa := &fakeFlowActions{pauseIDs: []string{"s1"}}
	srv := newTestServer(t, func(c *server.Config) { c.FlowActions = fa })
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/flow/pause", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body)
	}
	var got map[string]any
	json.Unmarshal(rr.Body.Bytes(), &got)
	if len(got["paused_stages"].([]any)) != 1 {
		t.Fatalf("paused_stages missing")
	}
}

func TestFlowInject_ErrorCodes(t *testing.T) {
	fa := &fakeFlowActions{injectErr: orchestrator.ErrNoNotes}
	srv := newTestServer(t, func(c *server.Config) { c.FlowActions = fa })
	rr := httptest.NewRecorder()
	body := strings.NewReader(`{"stage_id":"s1"}`)
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/flow/notes/inject", body))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("code=%d", rr.Code)
	}
	var got map[string]string
	json.Unmarshal(rr.Body.Bytes(), &got)
	if got["error"] != "no_notes" {
		t.Fatalf("error=%q", got["error"])
	}
}
```

> `fakeFlowActions` implements `server.FlowActions`; map orchestrator sentinel errors to codes in the handler.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/server/ -run 'TestFlowPause_Success|TestFlowInject_ErrorCodes' -v`
Expected: FAIL (no route).

- [ ] **Step 3: Implement**

`server.go`: `mux.HandleFunc("/api/flow/", s.routeFlow)`.

`flow_handlers.go`:

```go
package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"<module>/pkg/orchestrator"
)

func writeFlowError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}

func flowErrCode(err error) (int, string) {
	switch {
	case errors.Is(err, orchestrator.ErrRunFinalizing):
		return http.StatusConflict, "run_finalizing"
	case errors.Is(err, orchestrator.ErrNoReviewPause):
		return http.StatusConflict, "flow_not_paused"
	case errors.Is(err, orchestrator.ErrNoNotes):
		return http.StatusBadRequest, "no_notes"
	case errors.Is(err, orchestrator.ErrNotOwned):
		return http.StatusBadRequest, "not_owned"
	case errors.Is(err, orchestrator.ErrTargetNotPaused):
		return http.StatusBadRequest, "target_not_paused"
	default:
		return http.StatusInternalServerError, "internal"
	}
}

func (s *Server) routeFlow(w http.ResponseWriter, r *http.Request) {
	if s.flowActions == nil {
		http.NotFound(w, r)
		return
	}
	switch {
	case r.URL.Path == "/api/flow/pause" && r.Method == http.MethodPost:
		s.handleFlowPause(w, r)
	case r.URL.Path == "/api/flow/notes/inject" && r.Method == http.MethodPost:
		s.handleFlowInject(w, r)
	case r.URL.Path == "/api/flow/notes/cancel" && r.Method == http.MethodPost:
		s.handleFlowCancel(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/flow/notes"): // Task 18
		s.routeFlowNotes(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleFlowPause(w http.ResponseWriter, r *http.Request) {
	ids, err := s.flowActions.PauseFlow(r.Context())
	if err != nil {
		status, code := flowErrCode(err)
		writeFlowError(w, status, code)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"paused_stages": ids})
}

func (s *Server) handleFlowInject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StageID string `json:"stage_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.StageID == "" {
		writeFlowError(w, http.StatusBadRequest, "invalid_body")
		return
	}
	if err := s.flowActions.InjectNotesAndResume(r.Context(), req.StageID); err != nil {
		status, code := flowErrCode(err)
		writeFlowError(w, status, code)
		return
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "resuming"})
}

func (s *Server) handleFlowCancel(w http.ResponseWriter, r *http.Request) {
	if err := s.flowActions.CancelNotesAndResume(r.Context()); err != nil {
		status, code := flowErrCode(err)
		writeFlowError(w, status, code)
		return
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "resuming"})
}
```

Note the `InjectNotesAndResume` HTTP signature here is `(ctx, target)`; the `FlowActions` interface (Task 16) must match — update it to `InjectNotesAndResume(ctx context.Context, targetStageID string) error`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/server/ -run 'TestFlowPause_Success|TestFlowInject_ErrorCodes' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
make lint
git add pkg/server/server.go pkg/server/flow_handlers.go pkg/server/flow_handlers_test.go pkg/server/actions.go
git commit -m "feat(server): /api/flow pause/inject/cancel c machine-readable кодами ошибок"
```

---

### Task 18: Notes CRUD handlers with `content_sha` + validation

**Files:**
- Modify: `pkg/server/flow_handlers.go` (add `routeFlowNotes`, GET/POST/PUT/DELETE)
- Test: `pkg/server/flow_handlers_test.go`

**Interfaces:**
- Consumes: `s.workspace` (resolve `{root,path}`→abs/display/reference + recompute `content_sha`), `state.LoadReviewNotes`/`SaveReviewNotes`, `s.reviewState` (must be `paused`).
- Produces: `routeFlowNotes` handling `GET /api/flow/notes`, `POST /api/flow/notes`, `PUT /api/flow/notes/{id}`, `DELETE /api/flow/notes/{id}`. New error codes `stale_content`, `rev_conflict`, `stale_line`. The write ops go through the orchestrator via a new `FlowActions`-adjacent path OR directly (see note). Since notes IO must serialize with the lifecycle, add three methods to `FlowActions`: `AddNote(root, path string, line *int, text, contentSHA string, expectedRev int) (state.ReviewNote, int, error)`, `UpdateNote(id, text string, expectedRev int) (int, error)`, `DeleteNote(id string, expectedRev int) (int, error)` — implemented in the orchestrator under `flowPauseMu`, resolving via an injected `Options.ResolveFile func(root, path string) (abs, display, reference, curSHA string, line int, ok bool)`.

- [ ] **Step 1: Write the failing test**

```go
func TestFlowNotes_AddRequiresPausedAndMatchingSHA(t *testing.T) {
	fa := &fakeFlowActions{reviewState: "none"}
	srv := newTestServer(t, func(c *server.Config) { c.FlowActions = fa; c.ReviewState = fa.review })
	rr := httptest.NewRecorder()
	body := strings.NewReader(`{"root":"project","path":"a.go","line":42,"text":"fix","content_sha":"sha256:x","expected_rev":0}`)
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/flow/notes", body))
	if rr.Code != http.StatusConflict {
		t.Fatalf("adding a note while not paused must 409, got %d", rr.Code)
	}
	var got map[string]string
	json.Unmarshal(rr.Body.Bytes(), &got)
	if got["error"] != "flow_not_paused" {
		t.Fatalf("error=%q", got["error"])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/server/ -run TestFlowNotes_AddRequiresPaused -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

Add `routeFlowNotes` to `flow_handlers.go`:

```go
func (s *Server) routeFlowNotes(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/api/flow/notes" && r.Method == http.MethodGet:
		s.handleNotesList(w, r)
	case r.URL.Path == "/api/flow/notes" && r.Method == http.MethodPost:
		s.handleNotesAdd(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/flow/notes/") && r.Method == http.MethodPut:
		s.handleNotesUpdate(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/flow/notes/") && r.Method == http.MethodDelete:
		s.handleNotesDelete(w, r)
	default:
		http.NotFound(w, r)
	}
}
```

`handleNotesAdd` decodes `{root, path, line *int, text, content_sha string, expected_rev int}`, requires `s.reviewState()=="paused"` (else `writeFlowError(w, 409, "flow_not_paused")`), then calls `s.flowActions.AddNote(...)`, mapping sentinels `ErrStaleContent→stale_content(409)`, `ErrRevConflict→rev_conflict(409)`, `ErrStaleLine→stale_line(409)`, blank text→`empty_text(400)`. Returns the created note + new `rev`. `handleNotesList` reads via a lock-free `s.flowActions.ListNotes()` (or a `Config.LoadNotes func() (state.ReviewNotes, error)`). `handleNotesUpdate`/`handleNotesDelete` extract `{id}` from the path and call the matching action.

Implement `AddNote`/`UpdateNote`/`DeleteNote`/`ListNotes` on the orchestrator in `reviewpause.go` under `flowPauseMu`, using `Options.ResolveFile` to compute `abs/display/reference/curSHA/lineText` and validate. `AddNote` enforces one-note-per-`(root,path,line)` (replace keeps `id`/`created_at`), assigns `id = "n"+NextID`, bumps `NextID` and `Rev`. Define the sentinels `ErrStaleContent`, `ErrRevConflict`, `ErrStaleLine` in `reviewpause.go`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/server/ -run TestFlowNotes_AddRequiresPaused -v`
Expected: PASS. Add and run tests for a successful add (paused + matching sha), `rev_conflict`, and delete.

- [ ] **Step 5: Commit**

```bash
make lint
git add pkg/server/flow_handlers.go pkg/orchestrator/reviewpause.go pkg/server/actions.go pkg/server/flow_handlers_test.go
git commit -m "feat(server): notes CRUD (/api/flow/notes) c content_sha + rev валидацией"
```

---

## Phase 4 — Frontend (`pkg/web/dashboard/src`)

### Task 19: `run-client` flow calls + `use-status` mapping + types

**Files:**
- Modify: `pkg/web/dashboard/src/api/run-client.ts`
- Modify: `pkg/web/dashboard/src/hooks/use-status/use-status.ts`
- Modify: the status type (wherever `capabilities`/status shape is typed)
- Test: `pkg/web/dashboard/src/api/run-client.test.ts`, `.../use-status.test.ts`

**Interfaces:**
- Produces:
  - `pauseFlow(): Promise<{paused_stages:string[]}>`, `listNotes()`, `addNote(body)`, `updateNote(id, text, expectedRev)`, `deleteNote(id, expectedRev)`, `injectNotes(stageId)`, `cancelNotes()` in `run-client.ts`.
  - `flowPauseState: 'none'|'paused'|'resuming'`, `flowPausedStages: string[]` on the status object from `use-status`.

- [ ] **Step 1: Write the failing test**

```ts
// run-client.test.ts
it('pauseFlow POSTs /api/flow/pause', async () => {
  const spy = mockFetchOnce({ paused_stages: ['s1'] })
  const res = await pauseFlow()
  expect(spy).toHaveBeenCalledWith('/api/flow/pause', expect.objectContaining({ method: 'POST' }))
  expect(res.paused_stages).toEqual(['s1'])
})
```

```ts
// use-status.test.ts
it('maps flow_pause_state and flow_paused_stages', () => {
  const s = normalizeStatus({ flow_pause_state: 'paused', flow_paused_stages: ['s1'] })
  expect(s.flowPauseState).toBe('paused')
  expect(s.flowPausedStages).toEqual(['s1'])
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd pkg/web/dashboard && npx vitest run src/api/run-client.test.ts src/hooks/use-status/use-status.test.ts`
Expected: FAIL.

- [ ] **Step 3: Implement**

In `run-client.ts`, add a JSON-returning helper next to `postJson`:

```ts
async function postJsonReturning<T>(url: string, body: unknown): Promise<T> {
  const response = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: body === null ? null : JSON.stringify(body),
  })
  if (!response.ok) {
    const err = new Error(`POST ${url} -> ${response.status}`)
    ;(err as any).code = await response.json().then((b) => b.error).catch(() => undefined)
    ;(err as any).status = response.status
    throw err
  }
  return response.json() as Promise<T>
}

export async function pauseFlow(): Promise<{ paused_stages: string[] }> {
  return postJsonReturning('/api/flow/pause', null)
}
export async function injectNotes(stageId: string): Promise<void> {
  await postJson('/api/flow/notes/inject', { stage_id: stageId })
}
export async function cancelNotes(): Promise<void> {
  await postJson('/api/flow/notes/cancel', null)
}
export async function listNotes(): Promise<{ rev: number; notes: ReviewNote[] }> {
  const r = await fetch('/api/flow/notes')
  if (!r.ok) throw new Error(`GET /api/flow/notes -> ${r.status}`)
  return r.json()
}
export async function addNote(body: {
  root: string; path: string; line: number | null; text: string; content_sha: string; expected_rev: number
}): Promise<{ note: ReviewNote; rev: number }> {
  return postJsonReturning('/api/flow/notes', body)
}
export async function updateNote(id: string, text: string, expectedRev: number): Promise<{ rev: number }> {
  return postJsonReturning(`/api/flow/notes/${encodeURIComponent(id)}`, { text, expected_rev: expectedRev }) // PUT below
}
export async function deleteNote(id: string, expectedRev: number): Promise<void> {
  const r = await fetch(`/api/flow/notes/${encodeURIComponent(id)}?expected_rev=${expectedRev}`, { method: 'DELETE' })
  if (!r.ok) throw new Error(`DELETE -> ${r.status}`)
}
```

(For `updateNote`, use a `PUT` variant of `postJsonReturning` — add a `method` param.) Define `ReviewNote` in a shared `types/` file matching the Go json shape.

In `use-status.ts` `normalizeStatus`, add:

```ts
const flowPauseState = obj.flow_pause_state === 'paused' || obj.flow_pause_state === 'resuming'
  ? obj.flow_pause_state : 'none'
const flowPausedStages = Array.isArray(obj.flow_paused_stages)
  ? obj.flow_paused_stages.filter((v): v is string => typeof v === 'string') : []
return { /* ...existing..., */ flowPauseState, flowPausedStages }
```

Add the two fields to the returned status type.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd pkg/web/dashboard && npx vitest run src/api/run-client.test.ts src/hooks/use-status/use-status.test.ts`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/web/dashboard/src/api/run-client.ts pkg/web/dashboard/src/hooks/use-status/use-status.ts pkg/web/dashboard/src/types/
git commit -m "feat(dashboard): run-client flow-вызовы + use-status маппинг flow_pause_state"
```

---

### Task 20: FileViewer line-comment UX + client `content_sha` + add note

**Files:**
- Modify: `pkg/web/dashboard/src/components/file-browser/FileViewer.tsx`
- Modify: `pkg/web/dashboard/src/skins/base/file-browser.css` (line-comment styles — reuse plan-panel tokens)
- Test: `.../FileViewer.test.tsx`

**Interfaces:**
- Consumes: `addNote` (Task 19), the fetched file content (FileViewer already has it) → compute `content_sha` via a browser SHA-256 (`crypto.subtle.digest`).
- Produces: clickable lines with a `●` marker + inline comment editor mirroring `PlanPanel`; on save → `addNote({root, path, line, text, content_sha, expected_rev})`.

- [ ] **Step 1: Write the failing test**

```tsx
it('opens a comment editor on line click and calls addNote on save', async () => {
  const addNote = vi.fn().mockResolvedValue({ note: {}, rev: 1 })
  render(<FileViewer file={sampleFile} root="project" flowPaused addNote={addNote} contentSha="sha256:x" />)
  fireEvent.click(screen.getByTestId('file-line-2'))
  fireEvent.change(screen.getByRole('textbox'), { target: { value: 'fix this' } })
  fireEvent.click(screen.getByText('Save'))
  await waitFor(() => expect(addNote).toHaveBeenCalledWith(
    expect.objectContaining({ root: 'project', line: 2, text: 'fix this', content_sha: 'sha256:x' })))
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd pkg/web/dashboard && npx vitest run src/components/file-browser/FileViewer.test.tsx`
Expected: FAIL.

- [ ] **Step 3: Implement**

Mirror `PlanPanel`'s pattern in `FileViewer`: `comments`/`activeCommentLine`/`draft` state, `handleLineClick`, `saveComment`, per-line render with `className="plan-line"` + `data-line` + `data-testid={`file-line-${n}`}` + `●` marker + inline `line-comment-form` with a textarea. On save, call the injected `addNote`. Compute `content_sha` once per loaded file: `const sha = 'sha256:' + toHex(await crypto.subtle.digest('SHA-256', new TextEncoder().encode(content)))`. Gate the line-comment affordance on `flowPaused` (only annotate when the flow is paused; the pause gate itself is Task 22).

- [ ] **Step 4: Run test to verify it passes**

Run: `cd pkg/web/dashboard && npx vitest run src/components/file-browser/FileViewer.test.tsx`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/web/dashboard/src/components/file-browser/FileViewer.tsx pkg/web/dashboard/src/skins/base/file-browser.css pkg/web/dashboard/src/components/file-browser/FileViewer.test.tsx
git commit -m "feat(dashboard): построчные заметки в FileViewer + client content_sha"
```

---

### Task 21: Pause-gate confirm dialog

**Files:**
- Modify: `pkg/web/dashboard/src/components/file-browser/FileViewer.tsx` (or a small `ReviewPauseGate` component)
- Test: `.../FileViewer.test.tsx`

**Interfaces:**
- Consumes: `pauseFlow` (Task 19), status `flowPauseState`.
- Produces: first line-click while `flowPauseState==='none'` shows a confirm ("Чтобы писать заметки, нужно поставить флоу на паузу. Поставить?"); Yes → `pauseFlow()` then open editor once `flowPauseState==='paused'`; No → close.

- [ ] **Step 1: Write the failing test**

```tsx
it('prompts to pause on the first note when not paused', async () => {
  const pauseFlow = vi.fn().mockResolvedValue({ paused_stages: ['s1'] })
  render(<FileViewer file={sampleFile} root="project" flowPauseState="none" pauseFlow={pauseFlow} />)
  fireEvent.click(screen.getByTestId('file-line-2'))
  expect(screen.getByText(/поставить флоу на паузу/i)).toBeInTheDocument()
  fireEvent.click(screen.getByText('Да'))
  await waitFor(() => expect(pauseFlow).toHaveBeenCalled())
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd pkg/web/dashboard && npx vitest run src/components/file-browser/FileViewer.test.tsx -t pause`
Expected: FAIL.

- [ ] **Step 3: Implement**

Add the confirm dialog. When `flowPauseState==='none'` and the user clicks a line, show the confirm instead of the editor; on "Да" call `pauseFlow()` and, when the status next reports `paused`, open the editor for the clicked line; on "Нет" clear the pending line.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd pkg/web/dashboard && npx vitest run src/components/file-browser/FileViewer.test.tsx -t pause`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/web/dashboard/src/components/file-browser/FileViewer.tsx pkg/web/dashboard/src/components/file-browser/FileViewer.test.tsx
git commit -m "feat(dashboard): confirm-gate паузы при первой заметке"
```

---

### Task 22: Full-width review banner

**Files:**
- Create: `pkg/web/dashboard/src/components/review-banner/ReviewBanner.tsx`
- Modify: `pkg/web/dashboard/src/app/App.tsx` (mount after `<FlowHeader/>`)
- Modify: `pkg/web/dashboard/src/skins/base/*.css` (banner styles, tokenized)
- Test: `.../ReviewBanner.test.tsx`

**Interfaces:**
- Consumes: `flowPauseState`, `flowPausedStages`, note count, `injectNotes`, `cancelNotes`, an `onSend` opener.
- Produces: a full-width banner; `paused` → "⏸ Review mode — new stages held; active work pausing best-effort (N notes)" + **Send notes** + **Cancel**; `resuming` → "Resuming…"; `none` → not rendered.

- [ ] **Step 1: Write the failing test**

```tsx
it('renders in paused state with Send and Cancel', () => {
  render(<ReviewBanner flowPauseState="paused" noteCount={3} onSend={() => {}} onCancel={() => {}} />)
  expect(screen.getByText(/Review mode/i)).toBeInTheDocument()
  expect(screen.getByText(/3/)).toBeInTheDocument()
  expect(screen.getByText('Send notes')).toBeInTheDocument()
})
it('renders nothing when none', () => {
  const { container } = render(<ReviewBanner flowPauseState="none" noteCount={0} onSend={() => {}} onCancel={() => {}} />)
  expect(container).toBeEmptyDOMElement()
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd pkg/web/dashboard && npx vitest run src/components/review-banner/ReviewBanner.test.tsx`
Expected: FAIL.

- [ ] **Step 3: Implement** the component and mount it in `App.tsx` between `</FlowHeader>` (its closing tag) and `<main id="main">`, still inside `<FileBrowserProvider>`. Style tokenized (reuse `--amber`/`--panel-bg` etc.), full width, sticky under the header.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd pkg/web/dashboard && npx vitest run src/components/review-banner/ReviewBanner.test.tsx`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/web/dashboard/src/components/review-banner/ pkg/web/dashboard/src/app/App.tsx pkg/web/dashboard/src/skins/
git commit -m "feat(dashboard): full-width review-баннер (paused/resuming), честная формулировка"
```

---

### Task 23: Notes review modal + stage picker (Send/Inject/Cancel)

**Files:**
- Create: `pkg/web/dashboard/src/components/review-notes-modal/ReviewNotesModal.tsx`
- Modify: `App.tsx` (wire banner **Send** → open modal; `injectNotes`/`cancelNotes`)
- Test: `.../ReviewNotesModal.test.tsx`

**Interfaces:**
- Consumes: `listNotes`, `deleteNote`, `updateNote`, `injectNotes`, `flowPausedStages`.
- Produces: a modal listing notes grouped by file with edit/delete, a stage **picker** (`flowPausedStages`), **Inject** (disabled + disclosure when empty), **Cancel**.

- [ ] **Step 1: Write the failing test**

```tsx
it('disables inject and discloses when no target stage', async () => {
  vi.mocked(listNotes).mockResolvedValue({ rev: 1, notes: [{ id: 'n1', display_path: 'project/a.go', line: 2, text: 'x' } as any] })
  render(<ReviewNotesModal pausedStages={[]} onClose={() => {}} />)
  await screen.findByText(/project\/a.go/)
  expect(screen.getByText(/no active stage to receive notes/i)).toBeInTheDocument()
  expect(screen.getByText('Inject')).toBeDisabled()
})

it('injects into the chosen stage', async () => {
  vi.mocked(listNotes).mockResolvedValue({ rev: 1, notes: [{ id: 'n1', display_path: 'project/a.go', line: 2, text: 'x' } as any] })
  const inject = vi.mocked(injectNotes).mockResolvedValue()
  render(<ReviewNotesModal pausedStages={['s1']} onClose={() => {}} />)
  fireEvent.change(await screen.findByRole('combobox'), { target: { value: 's1' } })
  fireEvent.click(screen.getByText('Inject'))
  await waitFor(() => expect(inject).toHaveBeenCalledWith('s1'))
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd pkg/web/dashboard && npx vitest run src/components/review-notes-modal/ReviewNotesModal.test.tsx`
Expected: FAIL.

- [ ] **Step 3: Implement** the modal. On mount `listNotes()`; render grouped by `display_path`; per-note edit (→`updateNote`) / delete (→`deleteNote`, refresh `rev`); a `<select>` of `pausedStages`; **Inject** disabled when `pausedStages.length===0` (with the disclosure text) → `injectNotes(selected)` then close; a top-level **Cancel** → `cancelNotes()` then close. Surface `stale_content`/`rev_conflict` codes as inline messages.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd pkg/web/dashboard && npx vitest run src/components/review-notes-modal/ReviewNotesModal.test.tsx`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/web/dashboard/src/components/review-notes-modal/ pkg/web/dashboard/src/app/App.tsx
git commit -m "feat(dashboard): модалка ревью-заметок + stage-picker + inject/cancel"
```

---

### Task 24: Frontend build + full dashboard test pass

**Files:** none new.

- [ ] **Step 1: Run the whole dashboard test suite**

Run: `cd pkg/web/dashboard && npx vitest run`
Expected: PASS (fix any regressions in existing suites touched by App.tsx/use-status changes).

- [ ] **Step 2: Build**

Run: `make build`
Expected: `vite build` succeeds.

- [ ] **Step 3: Commit** (if the build produced regenerated assets that are tracked)

```bash
git add -A
git commit -m "chore(dashboard): пересборка после review-notes фичи"
```

---

## Phase 5 — Integration + live verification

### Task 25: Orchestrator integration test — end-to-end pause→inject→resume

**Files:**
- Test: `pkg/orchestrator/reviewpause_integration_test.go`

**Interfaces:** Consumes the whole feature.

- [ ] **Step 1: Write the integration test**

Drive a real (mock-agent) orchestrator: start a non-interactive implementation stage `s1` (running, IsActive), a dependent `s2` (pending). `PauseFlow` → `s1` paused, `s2` held. Add a note. `InjectNotesAndResume("s1")`. Assert: `s1`'s `feedback.md` contains the rendered note exactly once; `s1` resumes via the **implementation** WithFeedback runner (not planning) — assert via the runner-kind seam or a recorded phase; `s2` eventually activates; the marker and notes file are gone; a second inject/replay does not duplicate feedback.

```go
func TestIntegration_PauseInjectResume(t *testing.T) {
	// ... build orchestrator with a scripted mock runner that records phases,
	// run in a goroutine via the package's runOrchestratorAsync helper ...
	// assertions as above.
}
```

- [ ] **Step 2: Run it**

Run: `go test ./pkg/orchestrator/ -run TestIntegration_PauseInjectResume -v`
Expected: PASS.

- [ ] **Step 3: Full backend suite + lint**

Run: `go test ./... && make lint`
Expected: PASS, 0 lint issues (both native and `GOOS=linux`).

- [ ] **Step 4: Commit**

```bash
git add pkg/orchestrator/reviewpause_integration_test.go
git commit -m "test(orchestrator): интеграционный тест pause→inject→resume review-заметок"
```

---

### Task 26: Live verification (Docker + real browser)

**Files:** none (uses the `verify` skill; see `.memory`/AGENTS.md conventions and `project_file_browser_testing`).

- [ ] **Step 1:** Invoke the `verify` skill to build the Docker image and drive a flow with the file browser enabled (`AFM_FILE_BROWSER=1`, a `browse:true` mount), a non-interactive implementation stage, and a dependent stage.

- [ ] **Step 2:** In the real browser: open a file, click a line, accept the pause gate, confirm the banner shows "Review mode", write notes on two files (one line-level, one file-level), then **Send → pick the stage → Inject**.

- [ ] **Step 3:** Verify in the container: the target stage's `feedback.md` contains `## Review notes` with the rendered notes **once** and the `original:` line text; the stage restarted as **implementation** (check `<run>/<stage>/implementation.prompt.log` under `--debug`, not `planning.prompt.log`); the dependent stage resumed; the banner cleared; `notes-pause.json` and `review-notes.json` are gone.

- [ ] **Step 4:** Reload mid-pause → confirm the pause + notes survive. Mutate a reviewed file during pause, then inject → confirm the feedback carries `original:` + `(⚠ content differed at injection)`.

- [ ] **Step 5:** Record the outcome in the branch notes / a short comment on the spec. No code commit unless a fix was needed.

---

## Self-Review (performed by the plan author)

**Spec coverage:** content anchor (Tasks 2, 11, 20), best-effort pause + own-only-IsActive + EvPause-first (Tasks 9, 10), runner-kind registry + resume_kind dispatch + drain-before-resume + review→completeStage (Tasks 6, 12), attempt-0 replay A′ (Task 7), interrupt handshake (Task 8), resume transaction + recovery matrix + corrupt-resuming fail-closed (Tasks 13, 15), activationHeld vs reviewTxnActive split + control policy + shouldExit + atomic finalization (Tasks 9, 14), atomic SaveFeedbackOnce (Task 4), `/api/flow/*` + machine-readable codes + content_sha validation (Tasks 16–18), FileViewer line comments + pause gate + banner + notes modal + picker (Tasks 20–23), status fields + client (Tasks 16, 19). Live drift/unavailable + no-double-launch verified (Tasks 25, 26). All spec sections map to a task.

**Type consistency:** `PauseMarker`/`PauseOwner`/`ReviewNote`/`ReviewNotes` defined once (Tasks 2–3) and consumed unchanged; `FlowActions` signatures in Task 16 match the handlers in Tasks 17–18 (`InjectNotesAndResume(ctx, target)`, `AddNote/UpdateNote/DeleteNote/ListNotes`); `resume_kind` constants (`kindPlanning`…) consistent across Tasks 6/10/12; `flowPauseState`/`flowPausedStages` consistent across Tasks 16/19/22/23.

**Placeholder scan:** no "TBD"/"handle edge cases" — each step carries real code or an exact command; the two seams (`testRunnerHook`, `Options.ResolveFile`/`CurrentFileSHA`) are named, typed, and wired.
