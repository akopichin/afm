# Agent memory v2 (structured, afm-owned) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the prose memory store with a structured, afm-owned YAML store: afm (code) owns validation, dedup-metadata, eviction, atomic writes, and per-stage relevance retrieval; LLM agents do only extraction (reflect) and semantic consolidation (consolidator). Retune extraction toward project-specific findings.

**Architecture:** New `pkg/memory` package holds the pure code core (types, IO, validation, metadata reconciliation, eviction, retrieval/render) — heavily unit-tested, no LLM. The orchestrator pipeline becomes reflect → consolidator → afm(validate+reconcile+evict+atomic-write). Memory is injected **per-stage** at prompt-build time via a new `prompts.Inputs.MemoryBlock` (pointer when the store is small, an inlined relevant slice when large), replacing v1's static `GlobalPrompt` pointer. The compressor agent is removed.

**Tech Stack:** Go (do NOT change go.mod version), `gopkg.in/yaml.v3` (already a dep), `pkg/memory` (new), `pkg/orchestrator`, `pkg/prompts`, `pkg/flow`, `assets` embed, `cmd/afm`. Tests: `go test -race`, stub injection via the `runMemoryAgent` seam (cf. `injectFixStub`).

**Spec:** `docs/superpowers/specs/2026-08-28-agent-memory-v2-design.md`

## Global Constraints

- **Do NOT change the go version in `go.mod`.**
- **Lint clean** (`golangci-lint run ./...`, 0 new issues); no deprecated constructs.
- **Commits in Russian, no `Co-Authored-By`.**
- **Reflection stays best-effort:** never touches the FSM, never fails a stage/run. Any pipeline step failing → abort the rest for that stage + `reflect_failed` notice.
- **afm owns every write to the store files** via atomic temp+rename. Agents write ONLY their own candidate/merge output files, never the persisted store.
- **Evidence is mandatory:** a finding with empty `Evidence` is invalid and dropped by afm.
- **Kept from v1 (do NOT reimplement):** `memory:` block + `Stage.Reflect`; `maybeRunReflection` trigger from `completeStage`; `reflectMu`; `pendingReflections`; `SpawnDetached`; `final_reflect`/`finalReflectDone`; `initSessionMemory`; the `runMemoryAgent`/`execMemoryAgent` seam; best-effort/no-FSM envelope.
- Defaults: `max_findings=60`, `retrieval_threshold=25`, `core_confirm_count=3`.
- Run id for metadata stamps = `filepath.Base(o.opts.RunDir)`.

---

### Task 1: `pkg/memory` — types, YAML IO, validation

**Files:**
- Create: `pkg/memory/memory.go`, `pkg/memory/io.go`
- Test: `pkg/memory/memory_test.go`

**Interfaces — Produces:**
- `type Finding struct { ID, Scope, Kind string; Topic []string; Statement, Evidence, FirstSeen, LastSeen string; ConfirmCount int; SourceStage string }` (yaml tags per spec §1).
- `type Store struct { Findings []Finding }` (yaml `findings`).
- `const ScopeProject="project"; ScopeSession="session"`; `const KindFact="fact"; KindBestPractice="best_practice"; KindAntiPattern="anti_pattern"`.
- `func Load(path string) (Store, error)` — missing file → empty `Store{}`, nil error; malformed YAML → error.
- `func Save(path string, s Store) error` — atomic temp+rename in the file's dir.
- `func (f Finding) Valid() bool` — non-empty `ID` (charset `[a-z0-9-]`), valid `Scope`, valid `Kind`, non-empty `Statement`, non-empty `Evidence`.
- `func (s Store) Sanitize() Store` — returns a copy keeping only `Valid()` findings.

- [ ] **Step 1: Write failing tests** (`pkg/memory/memory_test.go`)

```go
package memory

import (
	"os"
	"path/filepath"
	"testing"
)

func validFinding() Finding {
	return Finding{ID: "sqlite-db", Scope: ScopeProject, Kind: KindFact, Topic: []string{"db"},
		Statement: "uses sqlite", Evidence: "config.json:1", FirstSeen: "r1", LastSeen: "r1", ConfirmCount: 1, SourceStage: "step1"}
}

func TestValid_RejectsMissingEvidenceAndBadFields(t *testing.T) {
	if !validFinding().Valid() {
		t.Fatal("baseline should be valid")
	}
	noEv := validFinding(); noEv.Evidence = ""
	if noEv.Valid() {
		t.Error("empty evidence must be invalid")
	}
	badScope := validFinding(); badScope.Scope = "global"
	if badScope.Valid() {
		t.Error("unknown scope must be invalid")
	}
	badKind := validFinding(); badKind.Kind = "rule"
	if badKind.Valid() {
		t.Error("unknown kind must be invalid")
	}
	badID := validFinding(); badID.ID = "Bad ID!"
	if badID.Valid() {
		t.Error("bad id charset must be invalid")
	}
}

func TestSanitize_DropsInvalid(t *testing.T) {
	bad := validFinding(); bad.Evidence = ""
	s := Store{Findings: []Finding{validFinding(), bad}}.Sanitize()
	if len(s.Findings) != 1 {
		t.Fatalf("want 1 valid finding, got %d", len(s.Findings))
	}
}

func TestLoadMissing_IsEmpty(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil || len(s.Findings) != 0 {
		t.Fatalf("missing → empty,nil; got %v err=%v", s, err)
	}
}

func TestSaveLoad_RoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "m.yaml")
	in := Store{Findings: []Finding{validFinding()}}
	if err := Save(p, in); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal("file not written")
	}
	out, err := Load(p)
	if err != nil || len(out.Findings) != 1 || out.Findings[0].ID != "sqlite-db" {
		t.Fatalf("round-trip mismatch: %v err=%v", out, err)
	}
}
```

- [ ] **Step 2: Run — expect FAIL** `go test ./pkg/memory/ -v` (package/symbols undefined).

- [ ] **Step 3: Implement** `pkg/memory/memory.go` (types + `Valid`/`Sanitize`, `regexp` for id charset `^[a-z0-9-]+$`) and `pkg/memory/io.go` (`Load` via `os.ReadFile`+`yaml.Unmarshal`, missing→empty; `Save` via temp+rename using `os.CreateTemp` in `filepath.Dir(path)` then `os.Rename` — mirror the v1 `atomicWriteFile` in `pkg/orchestrator/memory_size.go`).

- [ ] **Step 4: Run — expect PASS** `go test ./pkg/memory/ -v`.

- [ ] **Step 5: Commit** `git add pkg/memory && git commit -m "feat(memory): pkg/memory — типы Finding/Store, YAML IO, валидация"`

---

### Task 2: `pkg/memory` — metadata reconciliation

**Files:**
- Create: `pkg/memory/reconcile.go`
- Test: `pkg/memory/reconcile_test.go`

**Interfaces:**
- Consumes: `Finding`, `Store` (Task 1).
- Produces:
  - `type Status string`; `const StatusNew Status="new"; StatusReinforced="reinforced"; StatusUnchanged="unchanged"`.
  - `type MergedFinding struct { Finding \`yaml:",inline"\`; Status Status \`yaml:"status"\` }`
  - `type MergedStore struct { Findings []MergedFinding \`yaml:"findings"\` }`
  - `func Reconcile(prev Store, merged MergedStore, runID string) Store` — for each merged finding (after `Valid()` check on the embedded Finding; invalid dropped): `StatusNew` → set `FirstSeen=LastSeen=runID`, `ConfirmCount=1`, and if `ID` empty or collides with an already-emitted id, assign a fresh slug (`slugFromStatement`, deduped with a `-2`,`-3` suffix); `StatusReinforced` → carry `FirstSeen` from the matching prev finding (by ID) if present, `LastSeen=runID`, `ConfirmCount = prevCount+1`; `StatusUnchanged` → carry prev finding's metadata verbatim by ID (if no prev match, treat as `StatusNew`). Returns a `Store` (order: stable by input order).
  - `func slugFromStatement(s string) string` — lowercased, non-alphanumeric → `-`, collapse/trim `-`, cap length ~40.

- [ ] **Step 1: Write failing tests**

```go
package memory

import "testing"

func TestReconcile_NewGetsMetadataAndSlug(t *testing.T) {
	merged := MergedStore{Findings: []MergedFinding{
		{Finding: Finding{Scope: ScopeProject, Kind: KindFact, Statement: "Server uses sqlite", Evidence: "config.json:1"}, Status: StatusNew},
	}}
	out := Reconcile(Store{}, merged, "r5")
	if len(out.Findings) != 1 {
		t.Fatalf("want 1, got %d", len(out.Findings))
	}
	f := out.Findings[0]
	if f.ID == "" || f.FirstSeen != "r5" || f.LastSeen != "r5" || f.ConfirmCount != 1 {
		t.Errorf("new finding metadata wrong: %+v", f)
	}
}

func TestReconcile_ReinforcedBumpsCountKeepsFirstSeen(t *testing.T) {
	prev := Store{Findings: []Finding{{ID: "sqlite-db", Scope: ScopeProject, Kind: KindFact, Statement: "uses sqlite", Evidence: "config.json:1", FirstSeen: "r1", LastSeen: "r1", ConfirmCount: 2}}}
	merged := MergedStore{Findings: []MergedFinding{
		{Finding: Finding{ID: "sqlite-db", Scope: ScopeProject, Kind: KindFact, Statement: "uses sqlite (db layer)", Evidence: "config.json:1"}, Status: StatusReinforced},
	}}
	out := Reconcile(prev, merged, "r5")
	f := out.Findings[0]
	if f.FirstSeen != "r1" || f.LastSeen != "r5" || f.ConfirmCount != 3 {
		t.Errorf("reinforced metadata wrong: %+v", f)
	}
}

func TestReconcile_DropsInvalidAndDedupesSlug(t *testing.T) {
	merged := MergedStore{Findings: []MergedFinding{
		{Finding: Finding{Scope: ScopeProject, Kind: KindFact, Statement: "Same topic", Evidence: "a:1"}, Status: StatusNew},
		{Finding: Finding{Scope: ScopeProject, Kind: KindFact, Statement: "Same topic", Evidence: "b:2"}, Status: StatusNew},
		{Finding: Finding{Scope: ScopeProject, Kind: KindFact, Statement: "no evidence"}, Status: StatusNew}, // invalid → dropped
	}}
	out := Reconcile(Store{}, merged, "r1")
	if len(out.Findings) != 2 {
		t.Fatalf("want 2 valid, got %d", len(out.Findings))
	}
	if out.Findings[0].ID == out.Findings[1].ID {
		t.Error("colliding slugs must be deduped")
	}
}
```

- [ ] **Step 2: Run — expect FAIL** `go test ./pkg/memory/ -run TestReconcile -v`.
- [ ] **Step 3: Implement** `pkg/memory/reconcile.go`.
- [ ] **Step 4: Run — expect PASS** `go test ./pkg/memory/ -run TestReconcile -v`.
- [ ] **Step 5: Commit** `git commit -am "feat(memory): реконсиляция метаданных (new/reinforced/unchanged) + слаги"`

---

### Task 3: `pkg/memory` — eviction

**Files:** Create `pkg/memory/evict.go`; Test `pkg/memory/evict_test.go`

**Interfaces:**
- Produces: `func Evict(s Store, max int) Store` — if `len(s.Findings) <= max` return unchanged; else keep the `max` highest-value findings. Value order: `ConfirmCount` desc, then `LastSeen` desc (string compare on run-id is not chronological — see note), then stable by original index. Returns findings in their ORIGINAL order (evict, don't reorder the survivors).
  - **Note on `LastSeen` recency:** run-ids are `<flow>-<timestamp>-<rand>`; the timestamp segment sorts chronologically as a string. Compare `LastSeen` lexically as the recency proxy (documented approximation — good enough for tie-breaking within a flow).

- [ ] **Step 1: Failing tests**

```go
package memory

import "testing"

func TestEvict_UnderMaxUnchanged(t *testing.T) {
	s := Store{Findings: []Finding{{ID: "a", ConfirmCount: 1}}}
	if len(Evict(s, 5).Findings) != 1 {
		t.Error("under max must be unchanged")
	}
}

func TestEvict_DropsLowestConfirmCount(t *testing.T) {
	s := Store{Findings: []Finding{
		{ID: "keep-hi", ConfirmCount: 5, LastSeen: "r1"},
		{ID: "drop-lo", ConfirmCount: 1, LastSeen: "r1"},
		{ID: "keep-mid", ConfirmCount: 3, LastSeen: "r1"},
	}}
	out := Evict(s, 2)
	if len(out.Findings) != 2 {
		t.Fatalf("want 2, got %d", len(out.Findings))
	}
	for _, f := range out.Findings {
		if f.ID == "drop-lo" {
			t.Error("lowest confirm_count should have been evicted")
		}
	}
}

func TestEvict_TieBrokenByRecency(t *testing.T) {
	s := Store{Findings: []Finding{
		{ID: "old", ConfirmCount: 1, LastSeen: "flow-20260101-000000-aaaa"},
		{ID: "new", ConfirmCount: 1, LastSeen: "flow-20260828-000000-bbbb"},
	}}
	out := Evict(s, 1)
	if len(out.Findings) != 1 || out.Findings[0].ID != "new" {
		t.Errorf("recency tie-break failed: %+v", out.Findings)
	}
}
```

- [ ] **Step 2: FAIL** → **Step 3: Implement** (sort indices by value into a keep-set, then filter preserving original order) → **Step 4: PASS** `go test ./pkg/memory/ -run TestEvict -v`.
- [ ] **Step 5: Commit** `git commit -am "feat(memory): вытеснение по confirm_count/recency"`

---

### Task 4: `pkg/memory` — retrieval + render

**Files:** Create `pkg/memory/retrieval.go`; Test `pkg/memory/retrieval_test.go`

**Interfaces:**
- Produces:
  - `type RetrievalConfig struct { Threshold, CoreConfirmCount int }`
  - `func Tokenize(s string) []string` — lowercase, split on non-alphanumeric, drop tokens < 3 chars; returns a set-like slice.
  - `func Select(project, session Store, stageTokens []string, cfg RetrievalConfig) (findings []Finding, injectAll bool)` — total = len(project)+len(session). If `total <= cfg.Threshold` → return `(all, true)` (caller uses a pointer). Else `injectAll=false` and `findings` = session (all) ∪ project findings that are **core** (`ConfirmCount >= cfg.CoreConfirmCount`) OR **relevant** (Tokenize(Topic joined + Statement) ∩ stageTokens non-empty). Deterministic order: session first, then project in original order; no duplicates.
  - `func Render(findings []Finding) string` — compact deterministic text block: a header line, then one line per finding like `- [project/fact] <statement> (topic: a,b) — evidence: <ev>`. Empty input → "".

- [ ] **Step 1: Failing tests**

```go
package memory

import (
	"strings"
	"testing"
)

func mk(id string, scope string, cc int, topic, stmt string) Finding {
	return Finding{ID: id, Scope: scope, Kind: KindFact, Topic: []string{topic}, Statement: stmt, Evidence: "e:1", ConfirmCount: cc, LastSeen: "r1"}
}

func TestSelect_SmallStoreInjectsAll(t *testing.T) {
	proj := Store{Findings: []Finding{mk("a", ScopeProject, 1, "db", "uses sqlite")}}
	_, all := Select(proj, Store{}, []string{"anything"}, RetrievalConfig{Threshold: 25, CoreConfirmCount: 3})
	if !all {
		t.Error("small store must signal injectAll")
	}
}

func TestSelect_LargeStorePicksCoreAndRelevant(t *testing.T) {
	proj := Store{}
	for i := 0; i < 30; i++ {
		proj.Findings = append(proj.Findings, mk("f"+string(rune('a'+i)), ScopeProject, 1, "misc", "irrelevant thing"))
	}
	proj.Findings = append(proj.Findings,
		mk("core1", ScopeProject, 5, "misc", "core high-confidence rule"), // core by confirm_count
		mk("rel1", ScopeProject, 1, "database", "relevant to build stage database schema"), // relevant by token
	)
	sess := Store{Findings: []Finding{mk("s1", ScopeSession, 1, "x", "session ctx")}}
	got, all := Select(proj, sess, Tokenize("build database migration"), RetrievalConfig{Threshold: 25, CoreConfirmCount: 3})
	if all {
		t.Fatal("large store must not injectAll")
	}
	ids := map[string]bool{}
	for _, f := range got {
		ids[f.ID] = true
	}
	if !ids["core1"] || !ids["rel1"] || !ids["s1"] {
		t.Errorf("must include core, relevant, and all session; got %v", ids)
	}
	if ids["fa"] {
		t.Error("irrelevant low-confidence finding must be excluded")
	}
}

func TestRender_CompactAndDeterministic(t *testing.T) {
	out := Render([]Finding{mk("a", ScopeProject, 1, "db", "uses sqlite")})
	if !strings.Contains(out, "uses sqlite") || !strings.Contains(out, "e:1") {
		t.Errorf("render missing content: %q", out)
	}
	if Render(nil) != "" {
		t.Error("empty render must be empty string")
	}
}
```

- [ ] **Step 2: FAIL → Step 3: Implement → Step 4: PASS** `go test ./pkg/memory/ -run 'TestSelect|TestRender|TestTokenize' -v`.
- [ ] **Step 5: Commit** `git commit -am "feat(memory): извлечение релевантного среза (порог+core+теги) и рендер"`

---

### Task 5: flow config for v2

**Files:** Modify `pkg/flow/flow.go` (`MemoryConfig`), `ParseFile` defaults; Test `pkg/flow/flow_test.go`.

**Interfaces:**
- `MemoryConfig` fields become: `ProjectFile string \`yaml:"project_file,omitempty"\``, `MaxFindings int \`yaml:"max_findings,omitempty"\``, `RetrievalThreshold int \`yaml:"retrieval_threshold,omitempty"\``, `CoreConfirmCount int \`yaml:"core_confirm_count,omitempty"\``, `FinalReflect bool \`yaml:"final_reflect,omitempty"\``. **Remove** `MaxBytes`, `CompressRetries`.
- `ParseFile`: when `MemoryEnabled()`, default `MaxFindings=60`, `RetrievalThreshold=25`, `CoreConfirmCount=3` (only when 0). `MemoryEnabled()` unchanged.

- [ ] **Step 1: Update the v1 memory tests** in `flow_test.go` that assert `MaxBytes==20000`/`CompressRetries==2` → assert the new defaults (`MaxFindings==60`, `RetrievalThreshold==25`, `CoreConfirmCount==3`). Keep the validation tests (reflect/final_reflect require project_file, script stage ok) as-is. Add a parse test for the three new fields set explicitly.
- [ ] **Step 2: Run — expect FAIL** `go test ./pkg/flow/ -run 'Memory' -v` (old field names gone / new defaults).
- [ ] **Step 3: Implement** the struct + default changes.
- [ ] **Step 4: Run — expect PASS**.
- [ ] **Step 5: Commit** `git commit -am "feat(memory): конфиг v2 — max_findings/retrieval_threshold/core_confirm_count"`

---

### Task 6: prompts — reflect rewrite + consolidator, remove updater/compressor

**Files:** Create `assets/prompts/consolidator.md`; rewrite `assets/prompts/reflect.md`; delete `assets/prompts/updater.md`, `assets/prompts/compressor.md`; modify `pkg/orchestrator/orchestrator.go` (`Prompts`), `cmd/afm/run.go` (`loadPrompts`); Test `cmd/afm/run_test.go`.

**Interfaces:**
- `Prompts`: keep `Reflect`; **replace** `Updater`, `Compressor` with `Consolidator`. (Planning/Implementation/Review/Summary/Autonomous unchanged.)
- `loadPrompts` `names`: `[...existing 5..., "reflect.md", "consolidator.md"]`; map `texts[5]→Reflect`, `texts[6]→Consolidator`.

- [ ] **Step 1: Author the prompts.**
  - `reflect.md` (rewrite): role = extract **project-specific, durable** knowledge from the session; output **only** a YAML document `findings: [...]` where each item has `scope` (project|session), `kind` (fact|best_practice|anti_pattern), `topic` (tags), `statement`, `evidence` (REQUIRED: file:line / command / observation). Hard **EXCLUDE** list: afm/agent-protocol mechanics — execution_summary format, `$AFM_STAGE_DIR`, dialog/question file naming, plan-approval/autonomous flow, retry/backoff behavior, "read the memory files". If nothing durable+evidenced, output `findings: []`. No prose outside the YAML.
  - `consolidator.md` (new): role = merge candidate findings into the existing store. Inputs described as files (paths supplied by afm). Output **only** a YAML `findings: [...]` where each item additionally has `status: new|reinforced|unchanged`, preserves the existing `id` when merging into an existing finding, generalizes/merges duplicates, and **drops** candidates that are not durable, not project-specific, or lack real evidence (verification). Do not invent metadata (first_seen/last_seen/confirm_count) — afm owns those; omit them.
- [ ] **Step 2: Failing test** — update `TestLoadPrompts_IncludesMemoryPrompts` to assert `p.Reflect != "" && p.Consolidator != ""` (and remove Updater/Compressor references).
- [ ] **Step 3: Run — expect FAIL** (Consolidator field undefined).
- [ ] **Step 4: Implement** struct + loadPrompts changes; delete the two old prompt files.
- [ ] **Step 5: Run — expect PASS** `go test ./cmd/afm/ -run TestLoadPrompts -v && go build ./...`.
- [ ] **Step 6: Commit** `git add -A && git commit -m "feat(memory): промпты v2 — reflect (структурный) + consolidator; удалены updater/compressor"`

---

### Task 7: `memory_prompts.go` — spec + builders for reflect/consolidator

**Files:** Modify `pkg/orchestrator/memory_prompts.go`; Test `pkg/orchestrator/memory_prompts_test.go`.

**Interfaces:**
- `memoryAgentSpec`: keep `kind, stageName, command, logFile, sources, datasetOut`; for consolidator add `datasetPath, projectPath, sessionPath, outPath string`. **Remove** compressor fields (`targetFile`, `lineLimit`) and the `updater` usage of projectPath/sessionPath is now the consolidator's INPUT (read-only) with `outPath` the merged output.
- `memoryKind` consts: `memoryKindReflect="reflect"`, `memoryKindConsolidator="consolidator"` (remove updater/compressor).
- `buildMemoryPrompt`: `reflect` case → base `p.Reflect` + I/O block (read `sources`; write YAML `findings` to `datasetOut`). `consolidator` case → base `p.Consolidator` + I/O block (read candidate `datasetPath`, current `projectPath`+`sessionPath`; write merged YAML with `status` to `outPath`; do not write anything else).

- [ ] **Step 1: Update tests** — replace `TestBuildMemoryPrompt_Updater`/`_CompressorPlain`/`_CompressorExtreme` with `TestBuildMemoryPrompt_Consolidator` (asserts base text + all four paths appear + "status" instruction) and keep `TestBuildMemoryPrompt_Reflect` (assert sources + datasetOut + a "findings" / YAML instruction).
- [ ] **Step 2: FAIL → Step 3: Implement → Step 4: PASS** `go test ./pkg/orchestrator/ -run TestBuildMemoryPrompt -v`.
- [ ] **Step 5: Commit** `git commit -am "feat(memory): билдеры промптов reflect/consolidator + spec без compressor"`

---

### Task 8: reflection.go — pipeline rewrite

**Files:** Modify `pkg/orchestrator/reflection.go`; Modify `pkg/orchestrator/memory_size.go` (remove now-unused helpers, keep nothing if all unused — see Task 10); Test `pkg/orchestrator/reflection_test.go`.

**Interfaces:**
- Consumes: `memory.{Load,Save,Reconcile,Evict,MergedStore,Store}` (Tasks 1-3), the `runMemoryAgent` seam, `o.opts.{Memory,MemoryProjectPath,MemorySessionPath,RunDir}`.
- `runReflectionPipeline(ctx, stageName, sources, logDir)` new body:
  1. reflect spec → writes `<logDir>/reflect_dataset.yaml` (candidate `Store`). On error: `reflectFailed`, return.
  2. consolidator spec: `datasetPath=<logDir>/reflect_dataset.yaml`, `projectPath=o.opts.MemoryProjectPath`, `sessionPath=o.opts.MemorySessionPath`, `outPath=<logDir>/consolidated.yaml`. On error: `reflectFailed`, return.
  3. **afm code:** `runID := filepath.Base(o.opts.RunDir)`. Read `consolidated.yaml` into `memory.MergedStore` (parse error → `reflectFailed`, return). `prevProject,_ := memory.Load(projectPath)`, `prevSession,_ := memory.Load(sessionPath)`. `reconciled := memory.Reconcile(mergeStores(prevProject, prevSession), merged, runID)`. Split reconciled by `Scope`; for project: `memory.Save(projectPath, memory.Evict(projectStore, o.opts.Memory.MaxFindings))`; same for session. Save errors → `reflectNotice` (best-effort), do not abort further.
- Remove `compressIfNeeded` entirely.
- `initSessionMemory` now writes an empty store: `memory.Save(o.opts.MemorySessionPath, memory.Store{})` (creates a valid empty `findings: []` YAML) instead of the `# SESSION MEMORY\n` markdown stub. Update its test accordingly.

- [ ] **Step 1: Update the pipeline tests** in `reflection_test.go`. The v1 `pipelineHarness` stub simulated reflect/updater/compressor file effects — rewrite it: the stub, keyed by `spec.kind`, writes `reflect_dataset.yaml` (a candidate `memory.Store`) for `reflect`, and `consolidated.yaml` (a `memory.MergedStore` with `status`) for `consolidator`. New assertions:
  - after `runReflectionPipeline`, the project store `.yaml` loads via `memory.Load` and contains the expected finding with `ConfirmCount==1`, `FirstSeen==LastSeen==runID`.
  - order is reflect → consolidator (assert on the recorded `order` slice; no `compressor`).
  - a candidate with empty evidence in `consolidated.yaml` does NOT land (validation).
  - eviction: seed a prev store at `MaxFindings` and a consolidated output that pushes over → store length stays `<= MaxFindings`.
  Update `TestInitSessionMemory_ResetsEachRun` to assert the session file parses as an empty `memory.Store` (no "STALE").
  Keep the `TestMaybeRunReflection_NoOpWhenDisabled` and serialization tests (envelope unchanged).
- [ ] **Step 2: Run — expect FAIL** `go test ./pkg/orchestrator/ -run 'Reflection|MaybeRunReflection|InitSessionMemory' -v`.
- [ ] **Step 3: Implement** the new pipeline + `mergeStores` helper + `initSessionMemory` change; delete `compressIfNeeded`.
- [ ] **Step 4: Run — expect PASS** with `-race`.
- [ ] **Step 5: Commit** `git commit -am "feat(memory): конвейер v2 reflect→consolidator→afm(reconcile/evict/atomic write)"`

---

### Task 9: per-stage memory injection

**Files:** Modify `pkg/prompts/builder.go` (`Inputs.MemoryBlock` + render), `pkg/orchestrator/agents.go` (all runners), create helper in `pkg/orchestrator/reflection.go` (or a new `memory_inject.go`), Modify `cmd/afm/run.go` (drop static pointer); Test `pkg/prompts/builder_test.go`, `pkg/orchestrator/*`.

**Interfaces:**
- `prompts.Inputs` gains `MemoryBlock string`. `Build` emits it, when non-empty, as its own block after `</global_prompt>` (e.g. wrapped in `<project_memory>…</project_memory>`), escaped via `escapeTags`.
- New `func (o *Orchestrator) memoryBlockForStage(s flow.Stage) string`:
  - if `o.opts.MemoryProjectPath == ""` → "".
  - `proj,_ := memory.Load(o.opts.MemoryProjectPath)`, `sess,_ := memory.Load(o.opts.MemorySessionPath)`.
  - `tokens := memory.Tokenize(s.ID + " " + s.Name + " " + s.Description)`.
  - `sel, all := memory.Select(proj, sess, tokens, memory.RetrievalConfig{Threshold: o.opts.Memory.RetrievalThreshold, CoreConfirmCount: o.opts.Memory.CoreConfirmCount})`.
  - if `all` → return a **pointer** block naming the two absolute paths (reuse the v1 pointer wording, moved here). else → return `memory.Render(sel)` + a trailing "More project memory in `<projectPath>` — read it if relevant." line.
- Wire `MemoryBlock: o.memoryBlockForStage(s)` into every `prompts.Build(...)` call in `agents.go` (planning, implementation, review, autonomous, and the `*WithFeedback` variants — all of them).
- `cmd/afm/run.go`: set `GlobalPrompt: f.Prompt` (drop the appended memory pointer); KEEP computing `memProjectPath`/`memSessionPath` and passing `Memory`/`MemoryProjectPath`/`MemorySessionPath` to Options. `buildMemoryPointer` is removed here (logic moves into `memoryBlockForStage`) — see Task 10.

- [ ] **Step 1: Failing tests.**
  - `builder_test.go`: `Build(Inputs{MemoryBlock: "MEM-XYZ"})` output contains `MEM-XYZ` inside a `<project_memory>` block; empty `MemoryBlock` → no such block.
  - `pkg/orchestrator` (internal): `TestMemoryBlockForStage_*` — disabled → ""; small store → pointer block with both paths; large store (seed > threshold) → inlined render containing a core/relevant finding's statement, not an irrelevant one. Build via the existing `newTestOrchestrator` + set `o.opts.Memory*` and write store files with `memory.Save`.
- [ ] **Step 2: FAIL → Step 3: Implement → Step 4: PASS** `go test ./pkg/prompts/ ./pkg/orchestrator/ -run 'MemoryBlock|Build' -race -v`.
- [ ] **Step 5: Commit** `git commit -am "feat(memory): по-стадийная инъекция среза памяти (MemoryBlock) вместо статического указателя"`

---

### Task 10: remove v1 remnants

**Files:** `pkg/orchestrator/memory_size.go`, `cmd/afm/memory_pointer.go` (+ test), any dangling refs.

**Interfaces:** none new — deletion only.

- [ ] **Step 1:** Delete unused v1 helpers now that Task 8/9 removed their callers: in `memory_size.go` remove `fileExceeds`, `lineLimitForBytes`, `fifoDropOldestBlocks`. If `atomicWriteFile` is no longer referenced in `pkg/orchestrator` (Task 8 uses `memory.Save`), delete `memory_size.go` and its test entirely; otherwise keep only `atomicWriteFile`. Delete `cmd/afm/memory_pointer.go` and `cmd/afm/memory_pointer_test.go` (pointer logic moved to `memoryBlockForStage`).
- [ ] **Step 2: Run** `go build ./... && go vet ./...` — resolve any dangling references (grep for `fileExceeds`, `fifoDropOldestBlocks`, `lineLimitForBytes`, `buildMemoryPointer`, `memoryKindUpdater`, `memoryKindCompressor`, `MaxBytes`, `CompressRetries` and remove/replace each).
- [ ] **Step 3: Run** full `go test ./... -race` + `golangci-lint run ./...` → clean (0 new).
- [ ] **Step 4: Commit** `git commit -am "chore(memory): удаление остатков v1 (compressor/size/pointer)"`

---

### Task 11: integration test + docs

**Files:** Create `pkg/orchestrator/memory_integration_test.go` (replace/extend v1's); Modify `AGENTS.md`, `release-notes.md`, `README.md`.

- [ ] **Step 1: Integration test** (internal package, stub `runMemoryAgent`): a `reflect: true` stage runs the pipeline; assert (a) `reflect_dataset.yaml` + `consolidated.yaml` exist in the stage dir; (b) `memory.Load(projectPath)` returns a validated finding with `ConfirmCount==1` and `FirstSeen==LastSeen==runID`; (c) a later stage's built prompt (via `o.memoryBlockForStage` + `prompts.Build`) contains the finding's statement (force large store) or the pointer (small store). Run `-race`.
- [ ] **Step 2:** Rewrite the `## Agent memory` section of `AGENTS.md` for v2 (structured store, `pkg/memory`, reflect→consolidator, afm-owned atomic writes, metadata/eviction, per-stage retrieval via `MemoryBlock`, compressor removed). Point to the v2 spec + this plan.
- [ ] **Step 3:** Add a `release-notes.md` top entry `## 2026-08-28` "Agent memory v2" summarizing the change (structured YAML store, project-focused extraction, per-stage relevance retrieval, afm-owned atomic writes, 2 agents not 3) — include the live-run result (filled in after the live run).
- [ ] **Step 4:** Update the README `### Agent Memory` section (file is `.yaml`; new knobs `max_findings`/`retrieval_threshold`/`core_confirm_count`; two agents; structured findings with metadata).
- [ ] **Step 5: Commit** `git commit -am "test(memory): интеграционный тест v2 + docs (AGENTS/README/release-notes)"`

---

## Self-Review

**Spec coverage:**
- P1 signal quality → Task 6 (reflect prompt rewrite + exclude-list + evidence-required) + Task 2/8 (afm drops invalid) + consolidator verification (Task 6). ✓
- P2 retrieval → Task 4 (Select/Render) + Task 9 (per-stage MemoryBlock, pointer-vs-inline). ✓
- P3 structured + metadata + eviction → Tasks 1,2,3 + Task 8 (wired). ✓
- P4 afm-owned atomic writes → Task 1 (Save) + Task 8 (afm writes, agents don't). Compressor removed → Task 6/8/10. ✓
- Config change → Task 5. Kept-envelope regression → Tasks 8,9 tests. Cleanup → Task 10.

**Placeholder scan:** none — every task has concrete code or precise change instructions with exact symbol names.

**Type consistency:** `Finding`/`Store` (T1) used verbatim in T2/3/4/8/9. `MergedStore`/`Status` (T2) consumed in T8. `Select`/`RetrievalConfig`/`Render`/`Tokenize` (T4) consumed in T9. `memoryAgentSpec` consolidator fields (T7) match reflection.go usage (T8). `Prompts.Consolidator` (T6) used in T7. `MemoryConfig` v2 fields (T5) used in T8/T9. `Inputs.MemoryBlock` (T9) emitted by builder + set in agents.go.

**Cross-task ordering note:** Tasks 1-4 (pkg/memory) are independent and could batch; 5-7 independent-ish; 8 depends on 1-3,7; 9 depends on 4,8; 10 after 8-9; 11 last.
