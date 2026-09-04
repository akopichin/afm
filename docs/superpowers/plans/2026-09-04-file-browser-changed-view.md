# File browser changed-view Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an extensible tools toolbar to the top of the file browser's left panel with a three-way view switch — All / Unstaged / vs HEAD — where the two changed views list git-changed files.

**Architecture:** Reuse the existing file-browser pipeline: a new secure `workspace.FS.Changes` method (git via the already-verified `findGitDir` plumbing, its own strict git runner) → a new `GET /api/files/changed` case in `routeFiles` → a typed `getChanged` client → a `ChangedFilesList` component wired into `FileBrowserModal` behind a `viewMode` switch.

**Tech Stack:** Go (backend, `pkg/server/workspace` + `pkg/server`), TypeScript/React + Vitest (dashboard, `pkg/web/dashboard`), git CLI, `golangci-lint`.

**Spec:** `docs/superpowers/specs/2026-09-04-file-browser-changed-view-design.md`

## Global Constraints

- All user-facing UI copy is **English** (matches `FILE`/`DIFF`, `Reload`, `Search in …`). Labels: `All` / `Unstaged` / `vs HEAD`; states: `Loading changes…`, `No changes`, `Not a git repository`, `Failed to load changes: <code>`, `Some changes are not shown`.
- Never reuse `pkg/server/workspace/diff.go`'s `runGit`: its contract treats any non-zero git exit as benign "object absent". A new strict runner is used for `diff`/`ls-files`.
- git is always invoked with no shell, verified `--git-dir=<findGitDir result>`, `--work-tree=<root path>`, `--no-pager`, `-c core.fsmonitor=false`, env `GIT_OPTIONAL_LOCKS=0`, per-process 3s timeout; `diff` additionally with `--no-ext-diff --no-textconv --no-renames`. stderr is never returned to the client.
- Every git-emitted path is untrusted: require valid UTF-8, pass `validateRelPath`, and compute `Selectable` from a real `openat` + `fstat` (regular-file-only). Absolute/escape/`.git`/`.afm`/non-UTF-8 paths never reach JSON.
- The response never contains absolute paths or git stderr; error bodies stay `{"error":"<code>"}`.
- Do not edit `pkg/web/dashboard/public/skins/*` by hand — only `pkg/web/dashboard/skins/base/file-browser.css`; `npm run build`/`npm run sync-skins` regenerates `public/skins`.
- Go version in `go.mod` must not change.
- Commits in Russian, no `Co-Authored-By`.

---

## File Structure

**Backend (Go):**
- Create `pkg/server/workspace/changes.go` — `Changes` method + pure parser/aggregation + strict git runner.
- Create `pkg/server/workspace/changes_test.go` (`//go:build linux`) — real-repo integration matrix + pure-function unit tests.
- Modify `pkg/server/workspace/types.go` — `ChangeStatus`, `Change`, `ChangeList`.
- Modify `pkg/server/workspace/fs.go` — `ChangeMode` consts + `Changes` in the `FS` interface.
- Modify `pkg/server/files_handlers.go` — `changed` route case + `filesChanged` handler.
- Modify `pkg/server/files_handlers_test.go` — handler tests.
- Modify `pkg/server/capabilities_test.go` — extend `fakeFS` with `Changes` + `changes`/`changesErr` fields.

**Frontend (TS/React):**
- Modify `pkg/web/dashboard/src/api/files-client.ts` — `ChangeStatus`/`ChangeEntry`/`ChangeList`/`getChanged`/`toChangeEntry`.
- Modify `pkg/web/dashboard/src/api/files-client.test.ts` — `getChanged` tests.
- Create `pkg/web/dashboard/src/components/file-browser/ChangedFilesList.tsx`.
- Create `pkg/web/dashboard/src/components/file-browser/ChangedFilesList.test.tsx`.
- Modify `pkg/web/dashboard/src/components/file-browser/FileBrowserModal.tsx` — toolbar, `viewMode`, Refresh, load effect, hidden-search stop.
- Modify `pkg/web/dashboard/src/components/file-browser/FileBrowserModal.test.tsx` — mode-switch/guard/refresh tests.
- Modify `pkg/web/dashboard/src/components/file-browser/test-support.ts` — mock `/api/files/changed`.
- Modify `pkg/web/dashboard/skins/base/file-browser.css` — toolbar, segmented control, refresh, `.change-badge`.

---

## Task 1: Types + name-status parser (pure)

**Files:**
- Modify: `pkg/server/workspace/types.go`
- Create: `pkg/server/workspace/changes.go`
- Create: `pkg/server/workspace/changes_test.go` (`//go:build linux` — keeps it beside the later real-repo tests; the pure funcs still run there)

**Interfaces:**
- Produces: `ChangeStatus` (`ChangeModified`/`ChangeAdded`/`ChangeDeleted`), `Change{Name,Path string; Status ChangeStatus; Selectable bool}`, `ChangeList{Entries []Change; Truncated bool}`; `parseNameStatusZ(data []byte, truncated bool) ([]nameStatusRec, error)` with `nameStatusRec{status byte; path string}`; `parseUntrackedZ(data []byte) []string`; `mapStatus(s byte) (ChangeStatus, bool)`.

- [ ] **Step 1: Write the failing tests**

Add to `pkg/server/workspace/changes_test.go`:

```go
//go:build linux

package workspace

import (
	"reflect"
	"testing"
)

func TestMapStatus(t *testing.T) {
	cases := map[byte]struct {
		want ChangeStatus
		ok   bool
	}{
		'A': {ChangeAdded, true},
		'D': {ChangeDeleted, true},
		'M': {ChangeModified, true},
		'T': {ChangeModified, true},
		'U': {ChangeModified, true},
		'X': {"", false},
	}
	for in, exp := range cases {
		got, ok := mapStatus(in)
		if got != exp.want || ok != exp.ok {
			t.Errorf("mapStatus(%q) = (%q,%v), want (%q,%v)", in, got, ok, exp.want, exp.ok)
		}
	}
}

func TestParseNameStatusZ_Basic(t *testing.T) {
	// "M<NUL>a.go<NUL>D<NUL>dir/b.go<NUL>" — two complete records.
	data := []byte("M\x00a.go\x00D\x00dir/b.go\x00")
	recs, err := parseNameStatusZ(data, false)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	want := []nameStatusRec{{'M', "a.go"}, {'D', "dir/b.go"}}
	if !reflect.DeepEqual(recs, want) {
		t.Fatalf("got %+v, want %+v", recs, want)
	}
}

func TestParseNameStatusZ_PathWithWhitespace(t *testing.T) {
	// -z keeps spaces/tabs/newlines literally inside the path field.
	data := []byte("M\x00a b\tc\nd.go\x00")
	recs, err := parseNameStatusZ(data, false)
	if err != nil || len(recs) != 1 || recs[0].path != "a b\tc\nd.go" {
		t.Fatalf("got %+v err %v", recs, err)
	}
}

func TestParseNameStatusZ_UnknownStatusIsError(t *testing.T) {
	if _, err := parseNameStatusZ([]byte("R\x00a.go\x00"), false); err == nil {
		t.Fatal("want error for unknown status R (renames are disabled), got nil")
	}
}

func TestParseNameStatusZ_IncompleteTailRejectedUnlessTruncated(t *testing.T) {
	data := []byte("M\x00a.go\x00D") // dangling status, no path
	if _, err := parseNameStatusZ(data, false); err == nil {
		t.Fatal("want error for incomplete tail when not truncated")
	}
	recs, err := parseNameStatusZ(data, true)
	if err != nil || len(recs) != 1 || recs[0].path != "a.go" {
		t.Fatalf("truncated tail should drop the partial record: got %+v err %v", recs, err)
	}
}

func TestParseUntrackedZ(t *testing.T) {
	got := parseUntrackedZ([]byte("new.go\x00dir/other.go\x00"))
	want := []string{"new.go", "dir/other.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/server/workspace/ -run 'TestMapStatus|TestParseNameStatusZ|TestParseUntrackedZ' -v`
Expected: FAIL — undefined `ChangeStatus`, `mapStatus`, `parseNameStatusZ`, `nameStatusRec`, `parseUntrackedZ`.

- [ ] **Step 3: Add the wire types**

Append to `pkg/server/workspace/types.go`:

```go
// ChangeStatus is the git-derived state of a file in a changed-files listing.
// Deliberately a separate vocabulary from Diff.Status (which also has "clean"
// and describes a different object at a different moment).
type ChangeStatus string

const (
	ChangeModified ChangeStatus = "modified"
	ChangeAdded    ChangeStatus = "added"
	ChangeDeleted  ChangeStatus = "deleted"
)

// Change is one file in a changed-files listing. Selectable is true only for a
// path that is currently a regular file (a deleted/vanished/symlink/dir/special
// path is listed but not openable).
type Change struct {
	Name       string       `json:"name"`
	Path       string       `json:"path"` // root-relative, slash-separated
	Status     ChangeStatus `json:"status"`
	Selectable bool         `json:"selectable"`
}

// ChangeList is the flat result of FS.Changes. Truncated is true when any
// entry- or byte-cap was hit; it is never an error.
type ChangeList struct {
	Entries   []Change `json:"entries"`
	Truncated bool     `json:"truncated,omitempty"`
}
```

- [ ] **Step 4: Add the parsers to changes.go**

Create `pkg/server/workspace/changes.go`:

```go
package workspace

import "bytes"

// nameStatusRec is one record of `git diff --name-status -z` (rename/copy
// detection is disabled, so there are never old+new path triples).
type nameStatusRec struct {
	status byte
	path   string
}

// mapStatus maps a git single-letter status to a ChangeStatus. ok=false for any
// status this feature does not expect (with --no-renames, R/C never appear).
func mapStatus(s byte) (ChangeStatus, bool) {
	switch s {
	case 'A':
		return ChangeAdded, true
	case 'D':
		return ChangeDeleted, true
	case 'M', 'T', 'U':
		return ChangeModified, true
	default:
		return "", false
	}
}

// parseNameStatusZ parses `git diff --name-status -z` output: NUL-separated
// fields alternating status,path,status,path,… (NOT a "status\tpath" line).
// A dangling final record (status without a path) is an error unless truncated
// is true (byte-cap fired mid-record), in which case the partial tail is
// dropped. An unknown/unmapped status is an error.
func parseNameStatusZ(data []byte, truncated bool) ([]nameStatusRec, error) {
	fields := splitZ(data)
	recs := make([]nameStatusRec, 0, len(fields)/2)
	for i := 0; i < len(fields); i += 2 {
		if i+1 >= len(fields) {
			if truncated {
				break // partial tail from a byte-capped stream — drop it
			}
			return nil, ErrReadFailed
		}
		st := fields[i]
		if len(st) != 1 {
			return nil, ErrReadFailed
		}
		if _, ok := mapStatus(st[0]); !ok {
			return nil, ErrReadFailed
		}
		recs = append(recs, nameStatusRec{status: st[0], path: string(fields[i+1])})
	}
	return recs, nil
}

// parseUntrackedZ parses `git ls-files -z` output: NUL-separated paths.
func parseUntrackedZ(data []byte) []string {
	fields := splitZ(data)
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		out = append(out, string(f))
	}
	return out
}

// splitZ splits on NUL and drops the trailing empty element that a
// NUL-terminated stream always produces. Empty fields elsewhere are impossible
// (git never emits an empty path/status).
func splitZ(data []byte) [][]byte {
	parts := bytes.Split(data, []byte{0})
	if n := len(parts); n > 0 && len(parts[n-1]) == 0 {
		parts = parts[:n-1]
	}
	return parts
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./pkg/server/workspace/ -run 'TestMapStatus|TestParseNameStatusZ|TestParseUntrackedZ' -v`
Expected: PASS.

- [ ] **Step 6: Lint + commit**

```bash
make lint
git add pkg/server/workspace/types.go pkg/server/workspace/changes.go pkg/server/workspace/changes_test.go
git commit -m "feat(workspace): типы и парсер name-status для списка изменений"
```

Note: `make lint` here runs the repo's full pre-commit (lint+build+test) per project setup; it must be green before committing.

---

## Task 2: Strict git runner + HEAD probe

**Files:**
- Modify: `pkg/server/workspace/changes.go`
- Modify: `pkg/server/workspace/changes_test.go`

**Interfaces:**
- Consumes: `gitTimeout` (`diff.go`).
- Produces: `runGitChanges(ctx context.Context, gitDir, workTree string, args ...string) (out []byte, truncated bool, err error)`; `headExists(ctx context.Context, gitDir, workTree string) (bool, error)`; vars `maxChangesOutputBytes`, `maxChangesEntries`.

- [ ] **Step 1: Write the failing tests**

Append to `pkg/server/workspace/changes_test.go` (reuses `gitInit`/`gitCommitAll`/`gitDir` helpers; note the git-dir is `<repo>/.git`):

```go
func TestRunGitChanges_TrackedModified(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	writeFile(t, dir, "a.go", "package a\n")
	gitCommitAll(t, dir, "init")
	writeFile(t, dir, "a.go", "package a\n// edit\n")

	gitDir := filepath.Join(dir, ".git")
	out, truncated, err := runGitChanges(context.Background(), gitDir, dir,
		"diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--name-status", "-z", "--")
	if err != nil || truncated {
		t.Fatalf("err=%v truncated=%v", err, truncated)
	}
	if !bytes.Contains(out, []byte("a.go")) {
		t.Fatalf("expected a.go in output, got %q", out)
	}
}

func TestRunGitChanges_ByteCapTruncates(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	for i := 0; i < 50; i++ {
		writeFile(t, dir, "f"+strconv.Itoa(i)+".txt", "x")
	}
	old := maxChangesOutputBytes
	maxChangesOutputBytes = 8
	defer func() { maxChangesOutputBytes = old }()

	gitDir := filepath.Join(dir, ".git")
	out, truncated, err := runGitChanges(context.Background(), gitDir, dir,
		"ls-files", "--others", "--exclude-standard", "-z", "--")
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if !truncated || len(out) > 8 {
		t.Fatalf("expected truncated at 8 bytes, got truncated=%v len=%d", truncated, len(out))
	}
}

func TestRunGitChanges_NonZeroExitIsError(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	gitDir := filepath.Join(dir, ".git")
	// A bogus subcommand exits non-zero → strict runner must surface an error,
	// never a silent empty success (contrast with diff.go runGit).
	if _, _, err := runGitChanges(context.Background(), gitDir, dir, "not-a-command"); err == nil {
		t.Fatal("expected error for non-zero git exit")
	}
}

func TestHeadExists(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	gitDir := filepath.Join(dir, ".git")
	if ok, err := headExists(context.Background(), gitDir, dir); err != nil || ok {
		t.Fatalf("unborn repo: ok=%v err=%v, want false,nil", ok, err)
	}
	writeFile(t, dir, "a.go", "package a\n")
	gitCommitAll(t, dir, "init")
	if ok, err := headExists(context.Background(), gitDir, dir); err != nil || !ok {
		t.Fatalf("after commit: ok=%v err=%v, want true,nil", ok, err)
	}
}
```

Add this helper to `changes_test.go` if not already present:

```go
func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
```

Ensure the test imports include `bytes`, `context`, `os`, `path/filepath`, `strconv`, `testing`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/server/workspace/ -run 'TestRunGitChanges|TestHeadExists' -v`
Expected: FAIL — undefined `runGitChanges`, `headExists`, `maxChangesOutputBytes`.

- [ ] **Step 3: Implement the runner + HEAD probe**

Append to `pkg/server/workspace/changes.go`:

```go
import (
	"bytes"
	"context"
	"errors"
	"os/exec"
)

// maxChangesOutputBytes caps stdout captured from one git process; the rest is
// drained and discarded and the result is flagged truncated. A var so tests can
// shrink it. Distinct from an entry cap: exec buffering could otherwise grow
// without bound before any entry cap applies.
var maxChangesOutputBytes = 4 << 20

// maxChangesEntries caps how many Change entries Changes returns; exceeding it
// sets ChangeList.Truncated. A var so tests can shrink it.
var maxChangesEntries = 5000

// cappedBuffer captures up to cap bytes and records whether more arrived. It
// keeps draining (returns len(p), nil) past the cap so the child process is
// never blocked on a full pipe.
type cappedBuffer struct {
	buf      bytes.Buffer
	cap      int
	overflow bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if room := c.cap - c.buf.Len(); room > 0 {
		if len(p) <= room {
			c.buf.Write(p)
		} else {
			c.buf.Write(p[:room])
			c.overflow = true
		}
	} else if len(p) > 0 {
		c.overflow = true
	}
	return len(p), nil
}

// gitCmd builds a hardened git *exec.Cmd: no shell, verified --git-dir,
// allowlisted --work-tree, no pager, no repo-configured fsmonitor, optional
// index locks disabled (read-only endpoint must never mutate the index).
func gitCmd(ctx context.Context, gitDir, workTree string, args ...string) *exec.Cmd {
	full := append([]string{
		"--no-pager",
		"-c", "core.fsmonitor=false",
		"--git-dir=" + gitDir,
		"--work-tree=" + workTree,
	}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Env = append(cmd.Environ(), "GIT_OPTIONAL_LOCKS=0")
	return cmd
}

// runGitChanges runs a git command that MUST succeed (exit 0). Any non-zero
// exit, timeout, or exec failure is ErrReadFailed — unlike diff.go's runGit, it
// never collapses a failure into an empty success. stdout is byte-capped;
// stderr is discarded (never returned to the client).
func runGitChanges(ctx context.Context, gitDir, workTree string, args ...string) ([]byte, bool, error) {
	cctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := gitCmd(cctx, gitDir, workTree, args...)
	out := &cappedBuffer{cap: maxChangesOutputBytes}
	cmd.Stdout = out
	cmd.Stderr = nil // discard git diagnostics
	if err := cmd.Run(); err != nil {
		return nil, false, ErrReadFailed
	}
	return out.buf.Bytes(), out.overflow, nil
}

// headExists reports whether the repo has a HEAD commit. exit 0 → yes; exit 1 →
// unborn (no commits yet, not an error); anything else → ErrReadFailed.
func headExists(ctx context.Context, gitDir, workTree string) (bool, error) {
	cctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := gitCmd(cctx, gitDir, workTree, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	if cctx.Err() != nil {
		return false, ErrReadFailed
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil // unborn repo
	}
	return false, ErrReadFailed
}
```

Merge the new `import (...)` block with Task 1's `import "bytes"` (single import block; keep only `bytes` once).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/server/workspace/ -run 'TestRunGitChanges|TestHeadExists' -v`
Expected: PASS.

- [ ] **Step 5: Lint + commit**

```bash
make lint
git add pkg/server/workspace/changes.go pkg/server/workspace/changes_test.go
git commit -m "feat(workspace): строгий git-раннер и проверка HEAD для списка изменений"
```

---

## Task 3: Aggregation (dedup + collapse + sort + cap, pure)

**Files:**
- Modify: `pkg/server/workspace/changes.go`
- Modify: `pkg/server/workspace/changes_test.go`

**Interfaces:**
- Consumes: `Change`, `ChangeStatus`, `maxChangesEntries`.
- Produces: `aggregateChanges(tracked, untracked []Change) (entries []Change, truncated bool)` — seeds with tracked, then merges untracked: an untracked path colliding with a tracked `deleted` collapses to one `modified` (Selectable taken from the untracked/current entry); any other collision keeps the tracked entry; result is full-`Path` case-insensitive sorted and capped to `maxChangesEntries`.

- [ ] **Step 1: Write the failing tests**

Append to `pkg/server/workspace/changes_test.go`:

```go
func TestAggregateChanges_SortAndDedup(t *testing.T) {
	tracked := []Change{
		{Name: "b.go", Path: "z/b.go", Status: ChangeModified, Selectable: true},
		{Name: "a.go", Path: "a/a.go", Status: ChangeModified, Selectable: true},
	}
	got, truncated := aggregateChanges(tracked, nil)
	if truncated {
		t.Fatal("unexpected truncation")
	}
	// Full-path case-insensitive sort: a/a.go before z/b.go.
	if got[0].Path != "a/a.go" || got[1].Path != "z/b.go" {
		t.Fatalf("bad order: %+v", got)
	}
}

func TestAggregateChanges_DeletedPlusUntrackedCollapsesToModified(t *testing.T) {
	tracked := []Change{{Name: "x.go", Path: "x.go", Status: ChangeDeleted, Selectable: false}}
	untracked := []Change{{Name: "x.go", Path: "x.go", Status: ChangeAdded, Selectable: true}}
	got, _ := aggregateChanges(tracked, untracked)
	if len(got) != 1 {
		t.Fatalf("want 1 entry, got %+v", got)
	}
	if got[0].Status != ChangeModified || !got[0].Selectable {
		t.Fatalf("want modified+selectable, got %+v", got[0])
	}
}

func TestAggregateChanges_TrackedWinsOtherCollisions(t *testing.T) {
	tracked := []Change{{Name: "x.go", Path: "x.go", Status: ChangeModified, Selectable: true}}
	untracked := []Change{{Name: "x.go", Path: "x.go", Status: ChangeAdded, Selectable: true}}
	got, _ := aggregateChanges(tracked, untracked)
	if len(got) != 1 || got[0].Status != ChangeModified {
		t.Fatalf("tracked should win, got %+v", got)
	}
}

func TestAggregateChanges_EntryCapTruncates(t *testing.T) {
	old := maxChangesEntries
	maxChangesEntries = 2
	defer func() { maxChangesEntries = old }()
	tracked := []Change{
		{Path: "a", Name: "a", Status: ChangeModified},
		{Path: "b", Name: "b", Status: ChangeModified},
		{Path: "c", Name: "c", Status: ChangeModified},
	}
	got, truncated := aggregateChanges(tracked, nil)
	if !truncated || len(got) != 2 {
		t.Fatalf("want 2 entries + truncated, got len=%d truncated=%v", len(got), truncated)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/server/workspace/ -run TestAggregateChanges -v`
Expected: FAIL — undefined `aggregateChanges`.

- [ ] **Step 3: Implement aggregation**

Append to `pkg/server/workspace/changes.go` (add `cmp`, `slices`, `strings` to the import block):

```go
// aggregateChanges merges tracked and untracked entries into the final listing.
// Tracked entries seed the map. An untracked path that collides with a tracked
// "deleted" collapses to a single "modified" (the file was re-created after a
// staged deletion) with Selectable from the current (untracked) entry; any
// other collision keeps the tracked entry (tracked wins). The result is sorted
// by full Path (case-insensitive, original Path as tie-break) and capped to
// maxChangesEntries — hitting the cap sets truncated.
func aggregateChanges(tracked, untracked []Change) (entries []Change, truncated bool) {
	byPath := make(map[string]Change, len(tracked)+len(untracked))
	for _, c := range tracked {
		byPath[c.Path] = c
	}
	for _, u := range untracked {
		existing, ok := byPath[u.Path]
		switch {
		case !ok:
			byPath[u.Path] = u
		case existing.Status == ChangeDeleted:
			byPath[u.Path] = Change{Name: u.Name, Path: u.Path, Status: ChangeModified, Selectable: u.Selectable}
		default:
			// tracked wins — keep existing
		}
	}

	entries = make([]Change, 0, len(byPath))
	for _, c := range byPath {
		entries = append(entries, c)
	}
	slices.SortFunc(entries, func(a, b Change) int {
		if c := cmp.Compare(strings.ToLower(a.Path), strings.ToLower(b.Path)); c != 0 {
			return c
		}
		return cmp.Compare(a.Path, b.Path)
	})
	if len(entries) > maxChangesEntries {
		entries = entries[:maxChangesEntries]
		truncated = true
	}
	return entries, truncated
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/server/workspace/ -run TestAggregateChanges -v`
Expected: PASS.

- [ ] **Step 5: Lint + commit**

```bash
make lint
git add pkg/server/workspace/changes.go pkg/server/workspace/changes_test.go
git commit -m "feat(workspace): агрегация списка изменений (dedup, collapse, sort, cap)"
```

---

## Task 4: `FS.Changes` method + interface + fakeFS

**Files:**
- Modify: `pkg/server/workspace/fs.go`
- Modify: `pkg/server/workspace/changes.go`
- Modify: `pkg/server/workspace/changes_test.go`
- Modify: `pkg/server/capabilities_test.go`

**Interfaces:**
- Consumes: `resolve`, `findGitDir` (`diff.go`), `openFileReadNonblock` (`access_linux.go`/`access_other.go`), `runGitChanges`, `headExists`, `parseNameStatusZ`, `parseUntrackedZ`, `mapStatus`, `aggregateChanges`.
- Produces: `ChangeMode` (`ChangeModeIndex`/`ChangeModeHead`); `FS.Changes(ctx context.Context, rootID string, mode ChangeMode) (ChangeList, error)`; `fsImpl.Changes` implementation. `fakeFS` gains `changes workspace.ChangeList` + `changesErr error` and a `Changes` method returning them.

- [ ] **Step 1: Write the failing integration test**

Append to `pkg/server/workspace/changes_test.go`:

```go
func TestChanges_Matrix(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	writeFile(t, dir, "tracked.go", "package a\n")
	writeFile(t, dir, "deleteme.go", "package a\n")
	gitCommitAll(t, dir, "init")

	writeFile(t, dir, "tracked.go", "package a\n// edit\n") // modified
	os.Remove(filepath.Join(dir, "deleteme.go"))            // deleted
	writeFile(t, dir, "brand-new.go", "package a\n")        // untracked → added
	writeFile(t, dir, "ignored.log", "x")
	writeFile(t, dir, ".gitignore", "*.log\n")

	fs := newFS(t, Root{ID: "r", Label: "root", Path: dir})
	defer fs.Close()

	for _, mode := range []ChangeMode{ChangeModeIndex, ChangeModeHead} {
		got, err := fs.Changes(context.Background(), "r", mode)
		if err != nil {
			t.Fatalf("mode %s: %v", mode, err)
		}
		byPath := map[string]Change{}
		for _, c := range got.Entries {
			byPath[c.Path] = c
		}
		if c := byPath["tracked.go"]; c.Status != ChangeModified || !c.Selectable {
			t.Errorf("mode %s tracked.go = %+v", mode, c)
		}
		if c := byPath["deleteme.go"]; c.Status != ChangeDeleted || c.Selectable {
			t.Errorf("mode %s deleteme.go = %+v", mode, c)
		}
		if c := byPath["brand-new.go"]; c.Status != ChangeAdded || !c.Selectable {
			t.Errorf("mode %s brand-new.go = %+v", mode, c)
		}
		if _, ok := byPath["ignored.log"]; ok {
			t.Errorf("mode %s: ignored file must not appear", mode)
		}
		if _, ok := byPath[".gitignore"]; !ok {
			t.Errorf("mode %s: new .gitignore should appear as added", mode)
		}
	}
}

func TestChanges_UnbornRepoHeadMode(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir) // no commit → unborn HEAD
	writeFile(t, dir, "a.go", "package a\n")

	fs := newFS(t, Root{ID: "r", Label: "root", Path: dir})
	defer fs.Close()

	got, err := fs.Changes(context.Background(), "r", ChangeModeHead)
	if err != nil {
		t.Fatalf("unborn head: %v", err)
	}
	if len(got.Entries) != 1 || got.Entries[0].Path != "a.go" || got.Entries[0].Status != ChangeAdded {
		t.Fatalf("unborn head should list a.go as added, got %+v", got.Entries)
	}
}

func TestChanges_Errors(t *testing.T) {
	fs := newFS(t, Root{ID: "r", Label: "root", Path: t.TempDir()}) // no .git
	defer fs.Close()

	if _, err := fs.Changes(context.Background(), "r", ChangeModeIndex); !errors.Is(err, ErrDiffUnavailable) {
		t.Errorf("non-repo want ErrDiffUnavailable, got %v", err)
	}
	if _, err := fs.Changes(context.Background(), "r", "bogus"); !errors.Is(err, ErrInvalidRootOrPath) {
		t.Errorf("bad mode want ErrInvalidRootOrPath, got %v", err)
	}
	if _, err := fs.Changes(context.Background(), "nope", ChangeModeIndex); !errors.Is(err, ErrInvalidRootOrPath) {
		t.Errorf("bad root want ErrInvalidRootOrPath, got %v", err)
	}
}
```

Add `"errors"` to the test imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/server/workspace/ -run TestChanges_ -v`
Expected: FAIL — `fs.Changes` undefined (and `ChangeModeIndex`/`ChangeModeHead`).

- [ ] **Step 3: Add the mode type + interface method**

In `pkg/server/workspace/fs.go`, add above the `FS` interface:

```go
// ChangeMode selects the baseline for a changed-files listing.
type ChangeMode string

const (
	ChangeModeIndex ChangeMode = "index" // worktree vs index (+ untracked)
	ChangeModeHead  ChangeMode = "head"  // worktree vs HEAD (+ untracked)
)
```

Add to the `FS` interface (after `Diff`):

```go
	Changes(ctx context.Context, rootID string, mode ChangeMode) (ChangeList, error)
```

- [ ] **Step 4: Implement `fsImpl.Changes`**

Append to `pkg/server/workspace/changes.go` (add `context`, `os`, `path`, `unicode/utf8` to imports as needed):

```go
// Changes lists files that differ from the chosen baseline in rootID, plus
// untracked files (as "added"). It works only when rootID is itself a git repo
// root (findGitDir does not walk above the root). git output is untrusted:
// every path is UTF-8- and validateRelPath-checked, and Selectable is decided
// by a real openat+fstat. See the spec for the security rationale.
func (fs *fsImpl) Changes(ctx context.Context, rootID string, mode ChangeMode) (ChangeList, error) {
	if err := ctx.Err(); err != nil {
		return ChangeList{}, err
	}
	if mode != ChangeModeIndex && mode != ChangeModeHead {
		return ChangeList{}, ErrInvalidRootOrPath
	}
	rh, _, err := fs.resolve(rootID, ".")
	if err != nil {
		return ChangeList{}, err
	}
	gitDir, _, ok := findGitDir(rh, ".")
	if !ok {
		return ChangeList{}, ErrDiffUnavailable
	}
	workTree := rh.root.Path

	var (
		tracked  []Change
		byteTrun bool
	)
	untrackedArgs := []string{"ls-files", "--others", "--exclude-standard", "-z", "--"}

	useDiff := mode == ChangeModeIndex
	if mode == ChangeModeHead {
		has, herr := headExists(ctx, gitDir, workTree)
		if herr != nil {
			return ChangeList{}, herr
		}
		useDiff = has
		if !has {
			// Unborn repo: no HEAD to diff against. Everything currently on
			// disk (cached + untracked) is "added" vs the empty baseline;
			// cached paths already deleted from the worktree are simply absent.
			untrackedArgs = []string{"ls-files", "--cached", "--others", "--exclude-standard", "-z", "--"}
		}
	}

	if useDiff {
		diffArgs := []string{"diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--name-status", "-z"}
		if mode == ChangeModeHead {
			diffArgs = append(diffArgs, "HEAD")
		}
		diffArgs = append(diffArgs, "--")
		out, trun, derr := runGitChanges(ctx, gitDir, workTree, diffArgs...)
		if derr != nil {
			return ChangeList{}, derr
		}
		byteTrun = byteTrun || trun
		recs, perr := parseNameStatusZ(out, trun)
		if perr != nil {
			return ChangeList{}, perr
		}
		tracked = fs.recsToChanges(rh, recs)
	}

	uout, utrun, uerr := runGitChanges(ctx, gitDir, workTree, untrackedArgs...)
	if uerr != nil {
		return ChangeList{}, uerr
	}
	byteTrun = byteTrun || utrun
	untracked := fs.pathsToChanges(rh, parseUntrackedZ(uout))

	entries, capTrun := aggregateChanges(tracked, untracked)
	return ChangeList{Entries: entries, Truncated: byteTrun || capTrun}, nil
}

// recsToChanges validates each diff record's path and builds a Change with a
// disk-verified Selectable. A deleted path is not opened (it is gone); any path
// that fails validation is dropped (git output is untrusted).
func (fs *fsImpl) recsToChanges(rh *rootHandle, recs []nameStatusRec) []Change {
	out := make([]Change, 0, len(recs))
	for _, r := range recs {
		status, ok := mapStatus(r.status)
		if !ok {
			continue
		}
		clean, ok := fs.validGitPath(r.path)
		if !ok {
			continue
		}
		selectable := status != ChangeDeleted && fs.isSelectable(rh, clean)
		out = append(out, Change{Name: path.Base(clean), Path: clean, Status: status, Selectable: selectable})
	}
	return out
}

// pathsToChanges builds "added" Changes for untracked paths, dropping any path
// that fails validation.
func (fs *fsImpl) pathsToChanges(rh *rootHandle, paths []string) []Change {
	out := make([]Change, 0, len(paths))
	for _, p := range paths {
		clean, ok := fs.validGitPath(p)
		if !ok {
			continue
		}
		out = append(out, Change{Name: path.Base(clean), Path: clean, Status: ChangeAdded, Selectable: fs.isSelectable(rh, clean)})
	}
	return out
}

// validGitPath requires valid UTF-8 and passes the path through validateRelPath
// (rejecting absolute/.. /NUL and hidden .git/.afm). It returns the cleaned
// path, or ok=false to drop it.
func (fs *fsImpl) validGitPath(p string) (string, bool) {
	if !utf8.ValidString(p) {
		return "", false
	}
	clean, err := validateRelPath(p)
	if err != nil || clean == "." {
		return "", false
	}
	return clean, true
}

// isSelectable reports whether clean is currently a regular file, decided by a
// real secure open (openFileReadNonblock never blocks on a FIFO) + fstat — not
// by trusting git's name. A symlink/dir/special/vanished path is not selectable.
func (fs *fsImpl) isSelectable(rh *rootHandle, clean string) bool {
	fd, err := rh.openat(clean, openFileReadNonblock)
	if err != nil {
		return false
	}
	f := os.NewFile(uintptr(fd), clean)
	st, serr := f.Stat()
	_ = f.Close()
	return serr == nil && st.Mode().IsRegular()
}
```

- [ ] **Step 5: Update the fakeFS test double**

In `pkg/server/capabilities_test.go`, add fields to `fakeFS`:

```go
type fakeFS struct {
	roots      int
	files      map[string]workspace.File
	search     workspace.SearchResult
	changes    workspace.ChangeList
	changesErr error
}
```

And add the method (near `Diff`):

```go
func (f fakeFS) Changes(context.Context, string, workspace.ChangeMode) (workspace.ChangeList, error) {
	return f.changes, f.changesErr
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./pkg/server/workspace/ -run TestChanges_ -v && go build ./pkg/server/...`
Expected: PASS; build succeeds (interface satisfied by `fsImpl` and `fakeFS`).

- [ ] **Step 7: Cross-platform build check**

Run: `GOOS=linux go build ./pkg/server/workspace/ ./pkg/server/ && go build ./...`
Expected: both succeed.

- [ ] **Step 8: Lint + commit**

```bash
make lint
git add pkg/server/workspace/fs.go pkg/server/workspace/changes.go pkg/server/workspace/changes_test.go pkg/server/capabilities_test.go
git commit -m "feat(workspace): метод Changes — список изменённых файлов по git"
```

---

## Task 5: HTTP endpoint `GET /api/files/changed`

**Files:**
- Modify: `pkg/server/files_handlers.go`
- Modify: `pkg/server/files_handlers_test.go`

**Interfaces:**
- Consumes: `s.workspace.Changes`, `writeFilesError`, `filesErrStatus`, `workspace.ChangeMode`.
- Produces: route case `"changed"` → `s.filesChanged`; JSON body is `workspace.ChangeList`; success sets `Cache-Control: no-store`.

- [ ] **Step 1: Write the failing tests**

Append to `pkg/server/files_handlers_test.go` (follow the existing table/`newTestServer` style; adapt imports/helpers to that file's conventions):

```go
func TestFilesChanged_Success(t *testing.T) {
	fs := fakeFS{roots: 1, changes: workspace.ChangeList{
		Entries: []workspace.Change{{Name: "a.go", Path: "a.go", Status: workspace.ChangeModified, Selectable: true}},
	}}
	srv := newTestServer(t, Config{Workspace: fs})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/files/changed?root=r&mode=head", nil)
	srv.routeFiles(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	var body workspace.ChangeList
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Entries) != 1 || body.Entries[0].Path != "a.go" {
		t.Fatalf("body = %+v", body)
	}
}

func TestFilesChanged_InvalidMode(t *testing.T) {
	fs := fakeFS{roots: 1, changesErr: workspace.ErrInvalidRootOrPath}
	srv := newTestServer(t, Config{Workspace: fs})
	rec := httptest.NewRecorder()
	srv.routeFiles(rec, httptest.NewRequest(http.MethodGet, "/api/files/changed?root=r&mode=bogus", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestFilesChanged_NotRepo(t *testing.T) {
	fs := fakeFS{roots: 1, changesErr: workspace.ErrDiffUnavailable}
	srv := newTestServer(t, Config{Workspace: fs})
	rec := httptest.NewRecorder()
	srv.routeFiles(rec, httptest.NewRequest(http.MethodGet, "/api/files/changed?root=r&mode=index", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

func TestFilesChanged_NonGET(t *testing.T) {
	srv := newTestServer(t, Config{Workspace: fakeFS{roots: 1}})
	rec := httptest.NewRecorder()
	srv.routeFiles(rec, httptest.NewRequest(http.MethodPost, "/api/files/changed?root=r&mode=index", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestFilesChanged_WorkspaceDisabled(t *testing.T) {
	srv := newTestServer(t, Config{Workspace: fakeFS{roots: 0}})
	rec := httptest.NewRecorder()
	srv.routeFiles(rec, httptest.NewRequest(http.MethodGet, "/api/files/changed?root=r&mode=index", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}
```

Ensure the test file imports `net/http`, `net/http/httptest`, `encoding/json`, and `github.com/akopichin/afm/pkg/server/workspace`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/server/ -run TestFilesChanged -v`
Expected: FAIL — `/api/files/changed` routes to the `default` 404 (no handler yet).

- [ ] **Step 3: Add the route case + handler**

In `pkg/server/files_handlers.go`, add to the `switch` in `routeFiles` (after `diff`):

```go
	case "changed":
		s.filesChanged(w, r)
```

Add the handler:

```go
func (s *Server) filesChanged(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	list, err := s.workspace.Changes(r.Context(), q.Get("root"), workspace.ChangeMode(q.Get("mode")))
	if err != nil {
		status, code := filesErrStatus(err)
		writeFilesError(w, status, code)
		return
	}
	// Dynamic listing: Refresh must never get a browser-cached body.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(list)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/server/ -run TestFilesChanged -v`
Expected: PASS.

- [ ] **Step 5: Lint + commit**

```bash
make lint
git add pkg/server/files_handlers.go pkg/server/files_handlers_test.go
git commit -m "feat(server): эндпоинт GET /api/files/changed"
```

---

## Task 6: Frontend client `getChanged`

**Files:**
- Modify: `pkg/web/dashboard/src/api/files-client.ts`
- Modify: `pkg/web/dashboard/src/api/files-client.test.ts`
- Modify: `pkg/web/dashboard/src/components/file-browser/test-support.ts`

**Interfaces:**
- Consumes: `fetchOk`, `query`, `isRecord`, `TreeEntry` (all in `files-client.ts`).
- Produces: `ChangeStatus = 'modified' | 'added' | 'deleted'`; `ChangeEntry = TreeEntry & { status: ChangeStatus }`; `ChangeList = { entries: ChangeEntry[]; truncated: boolean }`; `getChanged(root, mode: 'index' | 'head', signal?: AbortSignal): Promise<ChangeList>`. Test-support gains `setChanged(root, mode, entries, opts)` / `setChangedError(root, mode, code, status)` and a `/api/files/changed` route.

- [ ] **Step 1: Write the failing client tests**

Append to `pkg/web/dashboard/src/api/files-client.test.ts` (match the file's existing fetch-mock style; if it uses `FilesApiMock`, prefer that):

```ts
import { getChanged, FilesApiError } from './files-client'

describe('getChanged', () => {
  it('parses entries and synthesizes kind=file', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({
        entries: [
          { name: 'a.go', path: 'a.go', status: 'modified', selectable: true },
          { name: 'b.go', path: 'dir/b.go', status: 'deleted', selectable: false },
        ],
        truncated: true,
      }),
      headers: { get: () => null },
    } as unknown as Response)

    const res = await getChanged('r', 'head')
    expect(res.truncated).toBe(true)
    expect(res.entries).toHaveLength(2)
    expect(res.entries[0]).toMatchObject({ path: 'a.go', status: 'modified', selectable: true, kind: 'file' })
    expect(res.entries[1]).toMatchObject({ path: 'dir/b.go', status: 'deleted', selectable: false })
  })

  it('drops malformed entries and defaults truncated to false', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ entries: [{ name: 'x' }, { name: 'ok', path: 'ok.go', status: 'added' }] }),
      headers: { get: () => null },
    } as unknown as Response)

    const res = await getChanged('r', 'index')
    expect(res.truncated).toBe(false)
    expect(res.entries).toHaveLength(1)
    expect(res.entries[0].path).toBe('ok.go')
  })

  it('throws FilesApiError on 409', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: false,
      status: 409,
      json: async () => ({ error: 'diff_unavailable' }),
      headers: { get: () => null },
    } as unknown as Response)

    await expect(getChanged('r', 'index')).rejects.toMatchObject({ code: 'diff_unavailable', status: 409 })
    expect(FilesApiError).toBeDefined()
  })

  it('forwards the abort signal and mode', async () => {
    const spy = vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true, status: 200, json: async () => ({ entries: [] }), headers: { get: () => null },
    } as unknown as Response)
    const ctrl = new AbortController()
    await getChanged('r', 'head', ctrl.signal)
    const url = spy.mock.calls[0][0] as string
    expect(url).toContain('/api/files/changed')
    expect(url).toContain('mode=head')
    expect(spy.mock.calls[0][1]).toMatchObject({ signal: ctrl.signal })
  })
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run (from `pkg/web/dashboard`): `npm test -- files-client`
Expected: FAIL — `getChanged` is not exported.

- [ ] **Step 3: Implement the client**

Append to `pkg/web/dashboard/src/api/files-client.ts`:

```ts
export type ChangeStatus = 'modified' | 'added' | 'deleted'
export type ChangeEntry = TreeEntry & { status: ChangeStatus }
export type ChangeList = { entries: ChangeEntry[]; truncated: boolean }

function isChangeStatus(value: unknown): value is ChangeStatus {
  return value === 'modified' || value === 'added' || value === 'deleted'
}

// toChangeEntry требует строковые name/path и известный status; иначе строка
// отбрасывается (не выдумываем 'modified'). kind синтезируется как 'file' —
// changed-entry структурно совместим с TreeEntry, чтобы openFile/onToggleSelect
// принимали его без адаптеров.
function toChangeEntry(raw: unknown): ChangeEntry | null {
  if (!isRecord(raw) || typeof raw.name !== 'string' || typeof raw.path !== 'string' || !isChangeStatus(raw.status)) {
    return null
  }
  return {
    name: raw.name,
    path: raw.path,
    kind: 'file',
    status: raw.status,
    selectable: raw.selectable === true,
  }
}

export async function getChanged(root: string, mode: 'index' | 'head', signal?: AbortSignal): Promise<ChangeList> {
  const response = await fetchOk(`/api/files/changed${query({ root, mode })}`, signal ? { signal } : undefined)
  const data = (await response.json()) as { entries?: unknown; truncated?: unknown }
  const entries = Array.isArray(data.entries)
    ? data.entries.map(toChangeEntry).filter((e): e is ChangeEntry => e !== null)
    : []
  return { entries, truncated: data.truncated === true }
}
```

- [ ] **Step 4: Add the mock route to test-support.ts**

In `pkg/web/dashboard/src/components/file-browser/test-support.ts`, add a field, setters, and a route (mirroring `setSearch`/the `/api/files/search` block):

```ts
  private changes = new Map<string, { entries: (TreeEntryInput & { status: string })[]; truncated?: boolean } | { errorCode: string; status: number }>()

  setChanged(root: string, mode: string, entries: (TreeEntryInput & { status: string })[], opts: { truncated?: boolean } = {}): void {
    this.changes.set(`${root}|${mode}`, { entries, truncated: opts.truncated })
  }

  setChangedError(root: string, mode: string, code: string, status: number): void {
    this.changes.set(`${root}|${mode}`, { errorCode: code, status })
  }
```

And inside `install()`'s `mockImplementation`, before the final `return errorResponse('not_found', 404)`:

```ts
      if (url.pathname === '/api/files/changed') {
        const mode = url.searchParams.get('mode') ?? ''
        const hit = this.changes.get(`${root}|${mode}`)
        if (hit === undefined) return errorResponse('not_found', 404)
        if ('errorCode' in hit) return errorResponse(hit.errorCode, hit.status)
        return jsonResponse({
          entries: hit.entries.map((e) => ({
            name: e.name,
            path: e.path,
            status: e.status,
            selectable: e.selectable ?? true,
          })),
          truncated: hit.truncated ?? false,
        })
      }
```

- [ ] **Step 5: Run tests + typecheck**

Run (from `pkg/web/dashboard`): `npm test -- files-client && npm run typecheck`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/web/dashboard/src/api/files-client.ts pkg/web/dashboard/src/api/files-client.test.ts pkg/web/dashboard/src/components/file-browser/test-support.ts
git commit -m "feat(dashboard): клиент getChanged для /api/files/changed"
```

---

## Task 7: `ChangedFilesList` component + badge styles

**Files:**
- Create: `pkg/web/dashboard/src/components/file-browser/ChangedFilesList.tsx`
- Create: `pkg/web/dashboard/src/components/file-browser/ChangedFilesList.test.tsx`
- Modify: `pkg/web/dashboard/skins/base/file-browser.css`

**Interfaces:**
- Consumes: `ChangeEntry`, `TreeEntry` (`files-client.ts`).
- Produces: `ChangedFilesList` React component with props `{ result: ChangeList | null; loading: boolean; error: string | null; onOpenFile: (entry: TreeEntry) => void; onToggleSelect: (entry: TreeEntry) => void; isSelected: (path: string) => boolean; activePath: string | null }`.

- [ ] **Step 1: Write the failing component tests**

Create `pkg/web/dashboard/src/components/file-browser/ChangedFilesList.test.tsx`:

```tsx
import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import { ChangedFilesList } from './ChangedFilesList'
import type { ChangeList } from '../../api/files-client'

const list: ChangeList = {
  entries: [
    { name: 'a.go', path: 'a.go', kind: 'file', status: 'modified', selectable: true },
    { name: 'gone.go', path: 'dir/gone.go', kind: 'file', status: 'deleted', selectable: false },
  ],
  truncated: false,
}

describe('ChangedFilesList', () => {
  it('renders status badges and the directory prefix', () => {
    render(<ChangedFilesList result={list} loading={false} error={null} onOpenFile={vi.fn()} onToggleSelect={vi.fn()} isSelected={() => false} activePath={null} />)
    expect(screen.getByText('M')).toBeInTheDocument()
    expect(screen.getByText('D')).toBeInTheDocument()
    expect(screen.getByText('dir/')).toBeInTheDocument()
  })

  it('opens a selectable file on click', () => {
    const onOpen = vi.fn()
    render(<ChangedFilesList result={list} loading={false} error={null} onOpenFile={onOpen} onToggleSelect={vi.fn()} isSelected={() => false} activePath={null} />)
    fireEvent.click(screen.getByRole('button', { name: /a\.go/ }))
    expect(onOpen).toHaveBeenCalledWith(expect.objectContaining({ path: 'a.go' }))
  })

  it('does not render an open button or checkbox for a deleted (non-selectable) row', () => {
    render(<ChangedFilesList result={list} loading={false} error={null} onOpenFile={vi.fn()} onToggleSelect={vi.fn()} isSelected={() => false} activePath={null} />)
    expect(screen.queryByRole('button', { name: /gone\.go/ })).toBeNull()
    expect(screen.queryByRole('checkbox', { name: /gone\.go/ })).toBeNull()
    expect(screen.getByText(/Deleted or unavailable/i)).toBeInTheDocument()
  })

  it('shows loading, empty, error and truncation states', () => {
    const { rerender } = render(<ChangedFilesList result={null} loading error={null} onOpenFile={vi.fn()} onToggleSelect={vi.fn()} isSelected={() => false} activePath={null} />)
    expect(screen.getByText('Loading changes…')).toBeInTheDocument()

    rerender(<ChangedFilesList result={{ entries: [], truncated: false }} loading={false} error={null} onOpenFile={vi.fn()} onToggleSelect={vi.fn()} isSelected={() => false} activePath={null} />)
    expect(screen.getByText('No changes')).toBeInTheDocument()

    rerender(<ChangedFilesList result={null} loading={false} error="Failed to load changes: read_failed" onOpenFile={vi.fn()} onToggleSelect={vi.fn()} isSelected={() => false} activePath={null} />)
    expect(screen.getByText(/Failed to load changes/)).toBeInTheDocument()

    rerender(<ChangedFilesList result={{ entries: list.entries, truncated: true }} loading={false} error={null} onOpenFile={vi.fn()} onToggleSelect={vi.fn()} isSelected={() => false} activePath={null} />)
    expect(screen.getByText('Some changes are not shown')).toBeInTheDocument()
  })
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run (from `pkg/web/dashboard`): `npm test -- ChangedFilesList`
Expected: FAIL — module not found.

- [ ] **Step 3: Implement the component**

Create `pkg/web/dashboard/src/components/file-browser/ChangedFilesList.tsx` (modeled on `FileSearchResults.tsx`):

```tsx
import { type ReactElement } from 'react'
import type { ChangeList, ChangeEntry, ChangeStatus, TreeEntry } from '../../api/files-client'

type ChangedFilesListProps = {
  result: ChangeList | null
  loading: boolean
  error: string | null
  onOpenFile: (entry: TreeEntry) => void
  onToggleSelect: (entry: TreeEntry) => void
  isSelected: (path: string) => boolean
  activePath: string | null
}

const BADGE: Record<ChangeStatus, string> = { modified: 'M', added: 'A', deleted: 'D' }

function dirPrefix(path: string): string {
  const slash = path.lastIndexOf('/')
  return slash === -1 ? '' : path.slice(0, slash + 1)
}

// Плоский список изменённых файлов. Цвет badge — не единственный сигнал: буква
// статуса (M/A/D) всегда видна. Selectable-строка открывается настоящей кнопкой
// и имеет чекбокс; неселектируемая (deleted/vanished) — приглушена и несёт
// доступный текст причины.
export function ChangedFilesList({ result, loading, error, onOpenFile, onToggleSelect, isSelected, activePath }: ChangedFilesListProps): ReactElement {
  if (error !== null) return <div className="file-tree-hint file-tree-error">{error}</div>
  if (loading && result === null) return <div className="file-tree-hint">Loading changes…</div>
  if (result === null) return <div className="file-tree-hint">Loading changes…</div>
  if (result.entries.length === 0) return <div className="file-tree-hint">No changes</div>

  return (
    <div className="file-search-results" role="list" aria-label="Changed files">
      {result.entries.map((entry: ChangeEntry) => {
        const prefix = dirPrefix(entry.path)
        const badge = <span className={`change-badge change-badge-${entry.status}`} aria-hidden="true">{BADGE[entry.status]}</span>
        if (!entry.selectable) {
          return (
            <div key={entry.path} role="listitem" data-kind="file" className="file-tree-row file-search-row file-change-row-disabled">
              {badge}
              <span className="file-search-name">
                {prefix !== '' && <span className="file-search-dir">{prefix}</span>}
                <span className="file-tree-name">{entry.name}</span>
              </span>
              <span className="file-change-reason">Deleted or unavailable in the working tree</span>
            </div>
          )
        }
        return (
          <div
            key={entry.path}
            role="listitem"
            data-kind="file"
            className={`file-tree-row file-search-row${activePath === entry.path ? ' active' : ''}`}
          >
            <input type="checkbox" aria-label={`Select ${entry.path}`} checked={isSelected(entry.path)} onChange={() => onToggleSelect(entry)} />
            <button type="button" className="file-search-open" onClick={() => onOpenFile(entry)}>
              {badge}
              <span className="file-search-name">
                {prefix !== '' && <span className="file-search-dir">{prefix}</span>}
                <span className="file-tree-name">{entry.name}</span>
              </span>
            </button>
          </div>
        )
      })}
      {result.truncated && <div className="file-tree-hint">Some changes are not shown</div>}
    </div>
  )
}
```

- [ ] **Step 4: Add badge styles**

Append to `pkg/web/dashboard/skins/base/file-browser.css` (tokens only — every skin defines them):

```css
.change-badge {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 1.15rem;
  min-width: 1.15rem;
  height: 1.15rem;
  border-radius: 3px;
  font-size: 0.7rem;
  font-weight: 700;
  margin-right: 0.4rem;
  color: var(--bg-elev);
}
.change-badge-modified { background: var(--amber); }
.change-badge-added { background: var(--mint); }
.change-badge-deleted { background: var(--coral); }

.file-change-row-disabled {
  opacity: 0.55;
  cursor: default;
}
.file-change-reason {
  margin-left: auto;
  font-size: 0.72rem;
  color: var(--ink-soft, var(--ink));
}
```

If `--ink-soft` is not defined in the skins, use the plain muted token already used by `.file-tree-hint`; check `file-browser.css` for the existing muted-text token and reuse it verbatim rather than inventing one.

- [ ] **Step 5: Run tests + typecheck**

Run (from `pkg/web/dashboard`): `npm test -- ChangedFilesList && npm run typecheck`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/web/dashboard/src/components/file-browser/ChangedFilesList.tsx pkg/web/dashboard/src/components/file-browser/ChangedFilesList.test.tsx pkg/web/dashboard/skins/base/file-browser.css
git commit -m "feat(dashboard): компонент ChangedFilesList и стили бейджей"
```

---

## Task 8: Toolbar + view switch in FileBrowserModal

**Files:**
- Modify: `pkg/web/dashboard/src/components/file-browser/FileBrowserModal.tsx`
- Modify: `pkg/web/dashboard/src/components/file-browser/FileBrowserModal.test.tsx`
- Modify: `pkg/web/dashboard/skins/base/file-browser.css`

**Interfaces:**
- Consumes: `getChanged`, `ChangeList` (`files-client.ts`); `ChangedFilesList` (Task 7); existing `openFile`/`onToggleSelect`/`selection`/`FileTree`/`FileSearchResults`.
- Produces: internal `viewMode: 'all' | 'index' | 'head'` + `changesRevision` state; toolbar UI; changed-files load effect.

- [ ] **Step 1: Write the failing modal tests**

Append to `pkg/web/dashboard/src/components/file-browser/FileBrowserModal.test.tsx` (reuse the file's existing `FilesApiMock` setup/render helpers):

```tsx
it('switches to Unstaged view and lists changed files', async () => {
  const api = new FilesApiMock()
  api.setRoots([{ id: 'r', label: 'root' }])
  api.setTree('r', '.', [{ name: 'a.go', path: 'a.go', kind: 'file' }])
  api.setChanged('r', 'index', [{ name: 'c.go', path: 'c.go', kind: 'file', status: 'modified' }])
  api.install()

  renderModal() // the helper this test file already uses to mount FileBrowserModal
  fireEvent.click(await screen.findByRole('button', { name: 'Unstaged' }))

  expect(await screen.findByLabelText('Changed files')).toBeInTheDocument()
  expect(await screen.findByText('c.go')).toBeInTheDocument()
})

it('hides the search box outside the All view', async () => {
  const api = new FilesApiMock()
  api.setRoots([{ id: 'r', label: 'root' }])
  api.setTree('r', '.', [])
  api.setChanged('r', 'head', [])
  api.install()

  renderModal()
  expect(await screen.findByLabelText(/Search in/)).toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: 'vs HEAD' }))
  await screen.findByLabelText('Changed files')
  expect(screen.queryByLabelText(/Search in/)).toBeNull()
})

it('shows "Not a git repository" on 409', async () => {
  const api = new FilesApiMock()
  api.setRoots([{ id: 'r', label: 'root' }])
  api.setTree('r', '.', [])
  api.setChangedError('r', 'index', 'diff_unavailable', 409)
  api.install()

  renderModal()
  fireEvent.click(await screen.findByRole('button', { name: 'Unstaged' }))
  expect(await screen.findByText('Not a git repository')).toBeInTheDocument()
})
```

If the existing file has no `renderModal`/`renderBrowser` helper, follow whatever mount pattern its other tests use (they already render `FileBrowserModal` with the provider or props); do not invent a new harness.

- [ ] **Step 2: Run tests to verify they fail**

Run (from `pkg/web/dashboard`): `npm test -- FileBrowserModal`
Expected: FAIL — no `Unstaged`/`vs HEAD` buttons; `Changed files` list never appears.

- [ ] **Step 3: Add view state + the changed-files load effect**

In `FileBrowserModal.tsx`, add the import and state near the other `useState` hooks:

```tsx
import { ChangedFilesList } from './ChangedFilesList'
import { getChanged, type ChangeList /* keep existing imports on the same line */ } from '../../api/files-client'
```

```tsx
  type ViewMode = 'all' | 'index' | 'head'
  const [viewMode, setViewMode] = useState<ViewMode>('all')
  const [changesRevision, setChangesRevision] = useState(0)
  const [changes, setChanges] = useState<ChangeList | null>(null)
  const [changesLoading, setChangesLoading] = useState(false)
  const [changesError, setChangesError] = useState<string | null>(null)
  const changesGenRef = useRef(0)
```

Add the load effect (mirror the search effect's generation-guard + abort pattern):

```tsx
  // Загрузка списка изменений для index/head. Как и поиск: generation растёт на
  // КАЖДОМ запуске (включая ветки all/null-root), поздний ответ устаревшего
  // режима/root'а отбрасывается по generation, старый список чистится сразу.
  useEffect(() => {
    const generation = ++changesGenRef.current
    if (viewMode === 'all' || selectedRoot === null) {
      setChanges(null)
      setChangesError(null)
      setChangesLoading(false)
      return
    }
    const controller = new AbortController()
    setChanges(null)
    setChangesError(null)
    setChangesLoading(true)
    void getChanged(selectedRoot, viewMode, controller.signal)
      .then((r) => {
        if (generation !== changesGenRef.current) return
        setChangesLoading(false)
        setChanges(r)
      })
      .catch((e: unknown) => {
        if (controller.signal.aborted || generation !== changesGenRef.current) return
        setChangesLoading(false)
        setChangesError(
          e !== null && typeof e === 'object' && 'code' in e && (e as { code?: unknown }).code === 'diff_unavailable'
            ? 'Not a git repository'
            : `Failed to load changes: ${e instanceof Error ? e.message : 'read_failed'}`,
        )
      })
    return () => controller.abort()
  }, [viewMode, selectedRoot, changesRevision])
```

- [ ] **Step 4: Stop the hidden search when leaving All**

Add `viewMode` to the search effect's dependency array and short-circuit non-`all` modes. In the existing search `useEffect`, change the guard block so that when `viewMode !== 'all'` it bumps the generation, clears the result, and returns (so a hidden in-flight search can't overwrite the panel):

```tsx
    const generation = ++searchGenRef.current
    if (selectedRoot === null || viewMode !== 'all') {
      setSearchResult(null)
      setSearchError(null)
      setSearchLoading(false)
      return
    }
```

And append `viewMode` to that effect's dependency array: `[query, selectedRoot, viewMode]`.

- [ ] **Step 5: Render the toolbar + switch the left panel body**

In the `<aside className="file-browser-roots">`, add the toolbar as the FIRST child (before `rootsError`):

```tsx
            <div className="file-browser-toolbar">
              <div className="file-browser-viewswitch" role="group" aria-label="File panel view">
                <button type="button" aria-pressed={viewMode === 'all'} className={`file-browser-viewbtn${viewMode === 'all' ? ' active' : ''}`} onClick={() => setViewMode('all')}>
                  All
                </button>
                <button type="button" aria-pressed={viewMode === 'index'} title="Working tree vs index; includes untracked files" className={`file-browser-viewbtn${viewMode === 'index' ? ' active' : ''}`} onClick={() => setViewMode('index')}>
                  Unstaged
                </button>
                <button type="button" aria-pressed={viewMode === 'head'} title="Working tree vs last commit; includes untracked files" className={`file-browser-viewbtn${viewMode === 'head' ? ' active' : ''}`} onClick={() => setViewMode('head')}>
                  vs HEAD
                </button>
              </div>
              {viewMode !== 'all' && (
                <button type="button" className="file-browser-refresh icon-btn" aria-label="Refresh changed files" disabled={changesLoading} onClick={() => setChangesRevision((n) => n + 1)}>
                  ↻
                </button>
              )}
            </div>
```

Guard the search box so it renders only in `all` mode. Wrap the existing `<div className="file-search-box">…</div>` and the `query.trim() !== '' ? <FileSearchResults…/> : null` block in `{viewMode === 'all' && ( … )}`.

Change the tree wrapper so the changed list replaces the tree outside `all`, while keeping `FileTree` mounted-but-hidden (preserves expanded folders):

```tsx
                {viewMode !== 'all' ? (
                  <ChangedFilesList
                    result={changes}
                    loading={changesLoading}
                    error={changesError}
                    onOpenFile={(entry) => openFile(selectedRoot, entry)}
                    onToggleSelect={(entry) => onToggleSelect(selectedRoot, entry)}
                    isSelected={(path) => selection.some((f) => f.root === selectedRoot && f.path === path)}
                    activePath={activeFile?.root === selectedRoot ? activeFile.entry.path : null}
                  />
                ) : null}
                <div className="file-browser-tree-wrap" hidden={viewMode !== 'all' || query.trim() !== ''}>
                  <FileTree
                    root={selectedRoot}
                    onOpenFile={(entry) => openFile(selectedRoot, entry)}
                    onToggleSelect={(entry) => onToggleSelect(selectedRoot, entry)}
                    isSelected={(path) => selection.some((f) => f.root === selectedRoot && f.path === path)}
                    activePath={activeFile?.root === selectedRoot ? activeFile.entry.path : null}
                  />
                </div>
```

(`ChangedFilesList` and `FileSearchResults` share the `.file-search-results` styling, so `openFile`/`onToggleSelect` accept a `ChangeEntry` unchanged — it is structurally a `TreeEntry`.)

- [ ] **Step 6: Add toolbar styles**

Append to `pkg/web/dashboard/skins/base/file-browser.css`:

```css
.file-browser-toolbar {
  display: flex;
  align-items: center;
  gap: 0.4rem;
  margin-bottom: 0.5rem;
}
.file-browser-viewswitch {
  display: inline-flex;
  border: 1px solid var(--ink-line, var(--panel-bg));
  border-radius: 5px;
  overflow: hidden;
}
.file-browser-viewbtn {
  padding: 0.25rem 0.55rem;
  font-size: 0.75rem;
  background: var(--bg-elev);
  color: var(--ink);
  border: none;
  cursor: pointer;
}
.file-browser-viewbtn + .file-browser-viewbtn {
  border-left: 1px solid var(--ink-line, var(--panel-bg));
}
.file-browser-viewbtn.active {
  background: var(--amber);
  color: var(--bg-elev);
}
.file-browser-refresh {
  margin-left: auto;
}
```

If `--ink-line` is not a token in the skins, reuse whatever border token the existing `.file-browser-*` rules use (check the file) instead of `--ink-line`.

- [ ] **Step 7: Run tests + typecheck + build**

Run (from `pkg/web/dashboard`): `npm test -- FileBrowserModal && npm run typecheck && npm run build`
Expected: PASS; build succeeds and syncs `skins/` → `public/skins/`.

- [ ] **Step 8: Full frontend + backend regression**

Run (from `pkg/web/dashboard`): `npm test`
Run (repo root): `go test ./pkg/server/... && make lint`
Expected: all green.

- [ ] **Step 9: Commit**

```bash
git add pkg/web/dashboard/src/components/file-browser/FileBrowserModal.tsx pkg/web/dashboard/src/components/file-browser/FileBrowserModal.test.tsx pkg/web/dashboard/skins/base/file-browser.css pkg/web/dashboard/public/skins
git commit -m "feat(dashboard): тулбар переключения вида все/изменённые в file browser"
```

---

## Task 9: Docker E2E manual verification

The Linux-tagged workspace tests do not run on a macOS dev host and the browser is Docker-only. Verify the real path once end-to-end.

**Files:** none (manual).

- [ ] **Step 1: Enable the browser + build a local image**

Per `AGENTS.md` Docker section: set `docker.file_browser.enabled: true` (or `AFM_FILE_BROWSER=1`) and `make docker-build`; run a flow with `AFM_USE_DOCKER=1 AFM_DOCKER_IMAGE=<local tag>` in a git repo working tree.

- [ ] **Step 2: Verify in a real browser (Chrome DevTools)**

- Toolbar shows `All / Unstaged / vs HEAD`; `All` is the default tree.
- Modify a tracked file, delete another, create a new file: `Unstaged` and `vs HEAD` both list modified (`M`), deleted (`D`, non-clickable), and the new file (`A`).
- Clicking a listed file opens it; `DIFF` tab still shows `HEAD → current file`.
- `Refresh` re-fetches; search box hidden outside `All`.
- Point at a non-repo root (an `extra_mounts` browse dir that is not a git repo) → `Not a git repository`.

- [ ] **Step 3: Confirm the Linux-tagged tests pass in-container**

Run inside a `golang` container over the repo: `go test ./pkg/server/workspace/ -run TestChanges_ -v` (and the parser/runner tests). Expected: PASS.

---

## Self-Review

**Spec coverage:**
- Modes `index`/`head` + untracked in both → Tasks 4 (`Changes`), 1 (mapping). ✓
- `--no-renames` / `--no-ext-diff` / `--no-textconv` / `--no-pager` / `GIT_OPTIONAL_LOCKS=0` / `core.fsmonitor=false` → Task 2 (`gitCmd`), Task 4 (diff args). ✓
- Strict runner ≠ diff.go `runGit` → Task 2. ✓
- Unborn repo handling for `head` (`ls-files --cached --others`) → Task 4 + test `TestChanges_UnbornRepoHeadMode`. ✓
- deleted+untracked collapse → modified → Task 3 + `TestAggregateChanges_DeletedPlusUntrackedCollapsesToModified`. ✓
- Full-path case-insensitive sort → Task 3. ✓
- byte-cap + entry-cap → Truncated → Tasks 2/3. ✓
- Untrusted paths: UTF-8 + validateRelPath + openat/fstat Selectable → Task 4 (`validGitPath`/`isSelectable`). ✓
- Typed `ChangeMode`, all FS impls/doubles updated (`fakeFS`) → Task 4. ✓
- Endpoint + `Cache-Control: no-store` + error mapping → Task 5. ✓
- Client `ChangeEntry = TreeEntry & {status}`, malformed-entry drop → Task 6. ✓
- Toolbar (`role="group"`, `aria-pressed` buttons), Refresh, tooltips → Task 8. ✓
- viewMode persists across root; selection/activeFile preserved; tree stays mounted-hidden → Task 8. ✓
- generation-guard + abort for changed load; hidden-search stop → Task 8. ✓
- States (Loading/No changes/Not a git repository/Failed/Some changes not shown) → Tasks 7/8. ✓
- CSS only in `skins/base`, tokens only, `public/skins` via build → Tasks 7/8. ✓
- Frontend/backend test suites → Tasks 1–8; Docker E2E → Task 9. ✓

**Placeholder scan:** No TBD/TODO; every code step has concrete content. The two conditional token notes (`--ink-soft`/`--ink-line`) instruct reuse of an existing token if the named one is absent — an explicit instruction, not a placeholder.

**Type consistency:** `ChangeMode`/`ChangeModeIndex`/`ChangeModeHead`, `ChangeStatus`/`ChangeModified`/`ChangeAdded`/`ChangeDeleted`, `Change{Name,Path,Status,Selectable}`, `ChangeList{Entries,Truncated}`, `getChanged(root, 'index'|'head', signal?)`, `ChangedFilesList` prop names — used consistently across backend Tasks 1/3/4/5 and frontend Tasks 6/7/8. `aggregateChanges(tracked, untracked)`, `runGitChanges(ctx, gitDir, workTree, args...)`, `headExists(ctx, gitDir, workTree)`, `isSelectable`/`validGitPath` names match every call site.
