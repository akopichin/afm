# Agent memory v3 (directory store + pattern chain) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace v2's structured YAML store with a directory-based Markdown "project rules" memory distilled by a fixed prompt chain (reflect → aggregate → prioritize → select-high → update), run per reflecting stage and once over the whole run, with per-stage read/write modes and optional git commit.

**Architecture:** `memory.path` is a directory holding `memory.md` (project-wide) plus optional per-stage files. A stage opts in with `reflect: {file, mode}`. After a writing stage completes, a background best-effort chain distills its session into High-priority patterns and merges them into the stage's file; at end of run the same chain runs over all stage datasets into `memory.md`. v2's structured `pkg/memory` (Finding/Store/Reconcile/Evict/Select) and the consolidator are deleted; a smaller `pkg/memory` (path resolution, SelectHigh, Commit) replaces it. The scheduling envelope (maybeRunReflection/reflectMu/pendingReflections/SpawnDetached/no-FSM, the runMemoryAgent seam, per-stage MemoryBlock injection) is kept.

**Tech Stack:** Go (do NOT change go.mod version), `gopkg.in/yaml.v3` (reflect dataset is YAML but afm never parses it — agents do), `pkg/memory` (rewritten), `pkg/orchestrator`, `pkg/prompts`, `pkg/flow`, `assets` embed, `cmd/afm`. Tests: `go test -race`, stub injection via the `runMemoryAgent` seam.

**Spec:** `docs/superpowers/specs/2026-08-28-agent-memory-v3-design.md`

## Global Constraints

- **Do NOT change the go version in `go.mod`.**
- **Lint clean** (`golangci-lint run ./...`, 0 new issues); no deprecated constructs.
- **Commits in Russian, no `Co-Authored-By`.** Pre-commit hook runs the full suite and is slow; `--no-verify` is acceptable when the build is green and you've verified the changed packages — say so in the report.
- **Reflection stays best-effort:** never touches the FSM, never fails a stage/run. Any chain step failing → abort the rest for that scope + `reflect_failed` notice.
- **P1 preserved:** the reflect prompt keeps a hard exclude-list for afm/agent-protocol mechanics so memory stays project-specific.
- **Kept from v2 (do NOT reimplement):** `maybeRunReflection` trigger from `completeStage`; `reflectMu`; `pendingReflections`; `SpawnDetached`; best-effort/no-FSM envelope; `runMemoryAgent`/`execMemoryAgent` seam; the end-of-run hook point in `Run()` (v2 `runFinalReflectionOnce`, guarded by `finalReflectDone`); `reflect_dataset.yaml` intermediate; `prompts.Inputs.MemoryBlock` injection field.
- Defaults: `max_rules=25`; per-stage `mode` default `rw`.
- Memory file format (produced by the `update` agent), English only:
  ```markdown
  # Project rules

  ## [Pattern Name]

  [Pattern description]
  ```
  Priority encoded ONLY by block order (high, then medium, then low); no tier headings/words in the file.

## File map

- `pkg/flow/flow.go` — `MemoryConfig{Path,MaxRules,Commit}`, `Reflect{File,Mode}` + `Stage.Reflect *Reflect`, validation, defaults.
- `pkg/memory/` — DELETE `memory.go`/`io.go`/`reconcile.go`/`evict.go`/`retrieval.go` (+ their tests); ADD `paths.go`, `selecthigh.go`, `commit.go` (+ tests).
- `assets/prompts/` — rewrite `reflect.md`; ADD `aggregate.md`/`prioritize.md`/`update.md`; DELETE `consolidator.md`.
- `pkg/orchestrator/orchestrator.go` — `Prompts{...,Reflect,Aggregate,Prioritize,Update}`; `Options` memory fields → `MemoryDir`.
- `pkg/orchestrator/memory_prompts.go` — kinds + `memoryAgentSpec` + `buildMemoryPrompt` for reflect/aggregate/prioritize/update.
- `pkg/orchestrator/reflection.go` — the new chain (`maybeRunReflection`, `distill`, end-of-run pass, commit); delete v2 `reconcileAndSave`/`mergeStores`/`stripYAMLFence`/`initSessionMemory`.
- `pkg/orchestrator/memory_inject.go` — pointer to `memory.md` (+ stage file by mode).
- `cmd/afm/run.go` — `loadPrompts` names; `Options.MemoryDir` wiring.

---

### Task 1: Flow config v3 (`memory.path` dir + `reflect` object)

**Files:**
- Modify: `pkg/flow/flow.go`
- Test: `pkg/flow/flow_test.go`

**Interfaces — Produces:**
- `type MemoryConfig struct { Path string \`yaml:"path,omitempty"\`; MaxRules int \`yaml:"max_rules,omitempty"\`; Commit bool \`yaml:"commit,omitempty"\` }`
- `type Reflect struct { File string \`yaml:"file"\`; Mode string \`yaml:"mode,omitempty"\` }`
- `Stage.Reflect *Reflect \`yaml:"reflect,omitempty"\`` (was `bool`)
- `(f *Flow) MemoryEnabled() bool` → `f.Memory.Path != ""`
- Mode constants: `const ReflectModeR="r"; ReflectModeW="w"; ReflectModeRW="rw"`; helpers `(r *Reflect) CanRead() bool` (mode r or rw), `(r *Reflect) CanWrite() bool` (mode w or rw).

- [ ] **Step 1: Update/replace the v2 memory tests** in `flow_test.go`. Remove/replace the v2 field assertions (`ProjectFile`/`MaxFindings`/`RetrievalThreshold`/`CoreConfirmCount`/`FinalReflect`, and stage `reflect: true` bool). Write these:

```go
func TestParseMemoryV3_FieldsAndDefaults(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "flow.yaml")
	os.WriteFile(p, []byte(`
name: f
memory:
  path: .goga/memory
stages:
  - name: build
    agents: [planning, implementation]
    reflect:
      file: build.md
`), 0644)
	f, err := ParseFile(p)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !f.MemoryEnabled() || f.Memory.Path != ".goga/memory" {
		t.Fatalf("path not parsed: %+v", f.Memory)
	}
	if f.Memory.MaxRules != 25 {
		t.Errorf("MaxRules default = %d, want 25", f.Memory.MaxRules)
	}
	r := f.Stages[0].Reflect
	if r == nil || r.File != "build.md" {
		t.Fatalf("reflect not parsed: %+v", r)
	}
	if r.Mode != "rw" {
		t.Errorf("mode default = %q, want rw", r.Mode)
	}
	if !r.CanRead() || !r.CanWrite() {
		t.Error("rw must read and write")
	}
}

func TestParseMemoryV3_ModeGates(t *testing.T) {
	r := Reflect{Mode: "r"}
	if !r.CanRead() || r.CanWrite() {
		t.Error("r: read-only")
	}
	w := Reflect{Mode: "w"}
	if w.CanRead() || !w.CanWrite() {
		t.Error("w: write-only")
	}
}

func TestValidateMemoryV3(t *testing.T) {
	write := func(body string) error {
		dir := t.TempDir()
		p := filepath.Join(dir, "flow.yaml")
		os.WriteFile(p, []byte(body), 0644)
		_, err := ParseFile(p)
		return err
	}
	// reflect without memory.path → error
	if err := write("name: f\nstages:\n  - name: s\n    agents: [planning]\n    reflect:\n      file: s.md\n"); err == nil {
		t.Error("reflect without memory.path must error")
	}
	// empty file → error
	if err := write("name: f\nmemory:\n  path: m\nstages:\n  - name: s\n    agents: [planning]\n    reflect:\n      mode: rw\n"); err == nil {
		t.Error("empty reflect.file must error")
	}
	// bad mode → error
	if err := write("name: f\nmemory:\n  path: m\nstages:\n  - name: s\n    agents: [planning]\n    reflect:\n      file: s.md\n      mode: x\n"); err == nil {
		t.Error("bad mode must error")
	}
	// script stage with reflect parses OK
	if err := write("name: f\nmemory:\n  path: m\nstages:\n  - name: s\n    script: \"echo hi\"\n    reflect:\n      file: s.md\n"); err != nil {
		t.Errorf("script+reflect must parse: %v", err)
	}
}
```

- [ ] **Step 2: Run — expect FAIL** `go test ./pkg/flow/ -run 'MemoryV3' -v`.

- [ ] **Step 3: Implement.** Replace `MemoryConfig` with the v3 fields. Add `Reflect` struct + mode constants + `CanRead`/`CanWrite`. Change `Stage.Reflect` from `bool` to `*Reflect`. Update `MemoryEnabled()` to `Path != ""`. In `ParseFile` defaults block (when `MemoryEnabled()`): `if f.Memory.MaxRules == 0 { f.Memory.MaxRules = 25 }`, and for each stage with non-nil `Reflect` and empty `Mode`, set `Mode = "rw"`. In `validate()`: replace the v2 memory validation with — for each stage with `Reflect != nil`: error if `!MemoryEnabled()` (`stage %q: reflect requires memory.path`), error if `Reflect.File == ""` (`stage %q: reflect.file is required`), error if `Reflect.Mode ∉ {r,w,rw}` (`stage %q: reflect.mode must be r, w, or rw`).

- [ ] **Step 4: Run — expect PASS** `go test ./pkg/flow/ -run 'MemoryV3' -v`. NOTE: `go build ./...` will fail outside `pkg/flow` (orchestrator/cmd still reference v2 fields) — EXPECTED; later tasks fix them. Confirm `go test ./pkg/flow/...` and `go vet ./pkg/flow/...` pass; report which other files reference removed fields.

- [ ] **Step 5: Commit** `git commit -am "feat(memory): конфиг v3 — memory.path (директория) + reflect{file,mode}"`

---

### Task 2: `pkg/memory` rewrite (paths, SelectHigh, Commit)

**Files:**
- Delete: `pkg/memory/{memory,io,reconcile,evict,retrieval}.go` and their `_test.go`.
- Create: `pkg/memory/paths.go`, `pkg/memory/selecthigh.go`, `pkg/memory/commit.go`
- Test: `pkg/memory/paths_test.go`, `pkg/memory/selecthigh_test.go`, `pkg/memory/commit_test.go`

**Interfaces — Produces:**
- `func ProjectFile(dir string) string` → `filepath.Join(dir, "memory.md")`.
- `func StageFile(dir, rel string) string` → `filepath.Join(dir, rel)` (rel may be `sub/f.md`).
- `func AtomicWrite(path string, data []byte) error` — temp+rename in the file's dir; `MkdirAll(filepath.Dir(path))` first.
- `func SelectHigh(prioritized string) string` — from the prioritize step's output (which uses `## High` / `## Medium` / `## Low` section headings), return the text under the `## High` heading up to the next `## ` heading (trimmed). Empty string if no High section.
- `func Commit(dir, message string) (committed bool, err error)` — `git -C <repo> add <dir>` then, only if there are staged changes under dir, `git commit -m message`; returns `committed=false, nil` when nothing to commit; never pushes. Uses `os/exec`. Best-effort caller ignores err but logs.

- [ ] **Step 1: Delete the v2 files.**

```bash
git rm pkg/memory/memory.go pkg/memory/io.go pkg/memory/reconcile.go pkg/memory/evict.go pkg/memory/retrieval.go
git rm pkg/memory/memory_test.go pkg/memory/io_test.go pkg/memory/reconcile_test.go pkg/memory/evict_test.go pkg/memory/retrieval_test.go
```
(If some `_test.go` names differ, `ls pkg/memory` and remove all v2 `.go`/`_test.go` — the package should end up containing only the new files.)

- [ ] **Step 2: Write failing tests.**

`pkg/memory/paths_test.go`:
```go
package memory

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProjectAndStageFile(t *testing.T) {
	if ProjectFile("/m") != filepath.Join("/m", "memory.md") {
		t.Error("ProjectFile")
	}
	if StageFile("/m", "sub/b.md") != filepath.Join("/m", "sub/b.md") {
		t.Error("StageFile")
	}
}

func TestAtomicWrite_CreatesDirsAndFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a/b/c.md")
	if err := AtomicWrite(p, []byte("hi")); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "hi" {
		t.Errorf("content = %q", b)
	}
}
```

`pkg/memory/selecthigh_test.go`:
```go
package memory

import (
	"strings"
	"testing"
)

func TestSelectHigh(t *testing.T) {
	in := "## High\n1. A — desc a\n2. B — desc b\n\n## Medium\n1. C — desc c\n\n## Low\n1. D — desc d\n"
	got := SelectHigh(in)
	if !strings.Contains(got, "A — desc a") || !strings.Contains(got, "B — desc b") {
		t.Errorf("High items missing: %q", got)
	}
	if strings.Contains(got, "desc c") || strings.Contains(got, "desc d") {
		t.Errorf("non-High leaked: %q", got)
	}
}

func TestSelectHigh_NoHigh(t *testing.T) {
	if SelectHigh("## Medium\n1. x\n") != "" {
		t.Error("no High → empty")
	}
}
```

`pkg/memory/commit_test.go`:
```go
package memory

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCommit_AddsAndCommitsOnlyOnChange(t *testing.T) {
	repo := t.TempDir()
	for _, args := range [][]string{{"init"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}} {
		c := exec.Command("git", args...)
		c.Dir = repo
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	memDir := filepath.Join(repo, "m")
	os.MkdirAll(memDir, 0755)
	os.WriteFile(filepath.Join(memDir, "memory.md"), []byte("# Project rules\n"), 0644)

	committed, err := Commit(memDir, "chore(memory): update project memory")
	if err != nil || !committed {
		t.Fatalf("first commit: committed=%v err=%v", committed, err)
	}
	// no change → committed=false
	committed, err = Commit(memDir, "chore(memory): update project memory")
	if err != nil || committed {
		t.Fatalf("second commit should be no-op: committed=%v err=%v", committed, err)
	}
}
```

- [ ] **Step 3: Run — expect FAIL** `go test ./pkg/memory/ -v`.

- [ ] **Step 4: Implement** `paths.go` (the three path/write funcs; `AtomicWrite` mirrors the v2 temp+rename idiom), `selecthigh.go` (scan lines: capture between a line equal to `## High` (case-insensitive, trimmed) and the next line starting with `## `), `commit.go` (`exec.Command("git","-C",repo,...)` — resolve repo as the dir itself works for `git add <dir>`; run `git -C <dir> add .` then `git -C <dir> diff --cached --quiet` to detect staged changes (exit code 1 = has changes), then commit; return committed bool).

- [ ] **Step 5: Run — expect PASS** `go test ./pkg/memory/ -race -v`.

- [ ] **Step 6: Commit** `git commit -am "feat(memory): pkg/memory v3 — пути, SelectHigh, git-commit (удалён v2-store)"`

---

### Task 3: Prompts + `Prompts` struct + `loadPrompts`

**Files:**
- Rewrite: `assets/prompts/reflect.md`; Create: `assets/prompts/aggregate.md`, `assets/prompts/prioritize.md`, `assets/prompts/update.md`; Delete: `assets/prompts/consolidator.md`
- Modify: `pkg/orchestrator/orchestrator.go` (`Prompts`)
- Modify: `cmd/afm/run.go` (`loadPrompts`)
- Test: `cmd/afm/run_test.go`

**Interfaces — Produces:** `orchestrator.Prompts` gains `Reflect, Aggregate, Prioritize, Update string` (remove `Consolidator`). `loadPrompts` loads them.

- [ ] **Step 1: Author the prompt files.**

`assets/prompts/reflect.md` (RL dataset + P1 exclude-list):
```markdown
# ROLE AND PURPOSE
You are a specialized Session Data Analyst Agent and Reinforcement Learning (RL/RLHF) Dataset Engineer. Your task is to analyze the log of the current interaction session, extract recorded rules and errors, categorize them into two levels of abstraction, and format the output into a training dataset.

# DATA SOURCE
The object of your analysis is the current session (dialogue context, system logs, user and agent actions).

# HARD EXCLUDE LIST (do NOT record any of these)
Do NOT extract anything about the afm harness or agent-protocol mechanics — every stage rediscovers these and they are noise, not project knowledge. Never record:
- the execution_summary.md format or that one must be written;
- $AFM_STAGE_DIR, stage directories, or dialog/question/answer file naming;
- plan approval, the autonomous / agents:[auto] flow, revise/retry/backoff/idle-timeout behavior;
- "read the memory files" or anything about this memory system itself;
- generic software-engineering platitudes not specific to THIS project (e.g. "write tests", "handle errors").
If a candidate is really just restating one of the above, drop it.

# ANALYSIS INSTRUCTIONS AND OUTPUT STRUCTURE
Analyze the session and generate strictly one YAML document divided into two independent sections:

1. **project_level:** Rules and system errors that affect the entire project, architecture, or global business logic. They are fundamental and repeat regardless of the specific step context.
2. **session_level:** Implementation errors and rules specific only to the current session context, a particular user request, or a momentary step.

Each item in both lists must contain only three keys for RL training:
- `prompt`: Description of the situation, context, or the recorded issue.
- `chosen`: The ideal, reference response or action (compliance with the rule, error correction).
- `rejected`: The negative, erroneous response or action (the recorded error or rule violation).

Use the block literal style (`|`) for text fields to correctly preserve line breaks, quotes, and special characters.

# OUTPUT FORMAT EXAMPLE (FEW-SHOT EXAMPLES)

```yaml
project_level:
  - prompt: |
      Project-level situation: The user requests deletion of a critical infrastructure node without any confirmation.
    chosen: |
      Block the action and request two-factor verification according to the global security policy.
    rejected: |
      Execute the node deletion directly upon the first request.

session_level:
  - prompt: |
      Current session context: In step #4, the user passed a string instead of a number in the 'age' field.
    chosen: |
      Return a validation error: 'The age field must be an integer.'
    rejected: |
      Fail with a 500 Internal Server Error due to a lack of type handling in the parse_input function.
```

# EXECUTION
Analyze the current session right now and output the resulting dataset in YAML format according to the specified structure. Do not add any conversational text or explanations before or after the YAML block.
```

`assets/prompts/aggregate.md`:
```markdown
Aggregate these datasets and extract project-level patterns without citing or referencing specific details. The identified patterns must be mutually exclusive with no conceptual overlaps. Output the result as a numbered list where each item includes a clear pattern name and its corresponding description.
```

`assets/prompts/prioritize.md`:
```markdown
Prioritize the patterns into High, Medium, and Low tiers. Every pattern must be assigned to exactly one tier. Crucially, all three tiers (High, Medium, and Low) MUST be utilized, meaning no tier can be left empty.

Output exactly three Markdown sections, in this order, using these exact headings and nothing else before the first heading:

## High
<numbered list of High patterns: "N. Name — description">

## Medium
<numbered list of Medium patterns>

## Low
<numbered list of Low patterns>
```

`assets/prompts/update.md` (the `<MAX_RULES>` and `<FILEPATH>` placeholders are substituted by afm in `buildMemoryPrompt`):
```markdown
Now, take the patterns saved in "<FILEPATH>" and merge them with the current high-priority patterns provided to you. Prefer merging them into existing patterns if their meanings/concepts group well together. If this is not possible, create a new pattern.

Then, follow this exact algorithm:
1. Assign every resulting pattern to exactly one priority tier: high, medium, or low.
   This classification is an internal decision used only for section ordering and for
   the discard rule below — it must never appear in the output file.
2. Keep a maximum of <MAX_RULES> patterns in total. If you need to discard patterns to meet this
   limit, drop 'low' first, then 'medium', to preserve 'high'.
3. Update the file with the finalized result. The entire content of the file must be
   written in English.

You must strictly use the following Markdown format for the file content:

  # Project rules

  ## [Pattern Name]

  [Pattern description]

(Repeat the ## block for each pattern. Encode priority ONLY through the order of the
## blocks: high first, then medium, then low. Do NOT add any priority indication to the
file: no tier headings like ## High, no tier prefix or suffix in the pattern name like
High: or [High], no tier words inside the descriptions.)
```

Then delete the old prompt: `git rm assets/prompts/consolidator.md`.

- [ ] **Step 2: Failing test** — update `TestLoadPrompts_IncludesMemoryPrompts` in `cmd/afm/run_test.go` to assert `p.Reflect != "" && p.Aggregate != "" && p.Prioritize != "" && p.Update != ""` (remove Consolidator).

- [ ] **Step 3: Run — expect FAIL** `go test ./cmd/afm/ -run TestLoadPrompts -v` (Aggregate/Prioritize/Update undefined).

- [ ] **Step 4: Implement.** In `orchestrator.go` `Prompts`: replace `Consolidator string` with `Aggregate string`, `Prioritize string`, `Update string` (keep `Reflect`). In `cmd/afm/run.go` `loadPrompts`: `names` = the 5 base + `"reflect.md","aggregate.md","prioritize.md","update.md"`; map `texts[5]→Reflect, texts[6]→Aggregate, texts[7]→Prioritize, texts[8]→Update`.

- [ ] **Step 5: Run** `go test ./cmd/afm/ -run TestLoadPrompts -v`. NOTE: `go build ./...` still fails in pkg/orchestrator (`memory_prompts.go`/`reflection.go`/`memory_inject.go` reference removed symbols) — EXPECTED until Task 4. Confirm the failures are only those files.

- [ ] **Step 6: Commit** `git add -A && git commit -m "feat(memory): промпты v3 reflect/aggregate/prioritize/update; удалён consolidator"`

---

### Task 4: Orchestrator core — chain, injection, wiring (makes the build green again)

**Files:**
- Modify: `pkg/orchestrator/orchestrator.go` (`Options` memory fields), `pkg/orchestrator/memory_prompts.go`, `pkg/orchestrator/reflection.go`, `pkg/orchestrator/memory_inject.go`, `cmd/afm/run.go`
- Test: `pkg/orchestrator/reflection_test.go`, `pkg/orchestrator/memory_inject_test.go`, `pkg/orchestrator/memory_prompts_test.go`

**Interfaces:**
- Consumes: `pkg/memory.{ProjectFile,StageFile,SelectHigh,AtomicWrite,Commit}` (Task 2); `flow.MemoryConfig`/`flow.Reflect`/`Stage.Reflect`/`CanRead`/`CanWrite` (Task 1); `Prompts.{Reflect,Aggregate,Prioritize,Update}` (Task 3); the `runMemoryAgent` seam.
- Produces:
  - `Options`: replace `MemoryProjectPath`/`MemorySessionPath` with `MemoryDir string` (abs dir; `""` = disabled). Keep `Memory flow.MemoryConfig`.
  - `memoryAgentSpec` fields (see Step 3).
  - `memoryKind{Reflect,Aggregate,Prioritize,Update}`.

This is one task because `pkg/orchestrator` will not compile until all of these align (each currently references deleted v2 symbols). Do all edits, then run the package tests once.

- [ ] **Step 1: Options + spec + kinds.**
  - `orchestrator.go` `Options`: remove `MemoryProjectPath`, `MemorySessionPath`; add `MemoryDir string`.
  - `memory_prompts.go`: `memoryKindReflect="reflect"`, `memoryKindAggregate="aggregate"`, `memoryKindPrioritize="prioritize"`, `memoryKindUpdate="update"`. `memoryAgentSpec` fields: `kind, stageName, command, logFile string` (common); `sources []string, datasetOut string` (reflect); `inPaths []string, out string` (aggregate: inPaths = dataset files, out = patterns.md); `in, out string` reused for prioritize (in=patterns.md, out=prioritized.md); `highPath, targetFile string, maxRules int` (update: highPath = high.md, targetFile = memory file to rewrite). Keep fields minimal; it's fine to reuse `in`/`out` across aggregate/prioritize.

- [ ] **Step 2: `buildMemoryPrompt` cases** (memory_prompts.go): 
  - `reflect` → `p.Reflect` + "read these sources: <sources>; write the YAML dataset to <datasetOut>; write nothing else."
  - `aggregate` → `p.Aggregate` + "read these dataset files: <inPaths>; write the numbered pattern list to <out>."
  - `prioritize` → `p.Prioritize` + "read the patterns from <in>; write the prioritized High/Medium/Low sections to <out>."
  - `update` → `p.Update` with `<FILEPATH>`→`targetFile` and `<MAX_RULES>`→`maxRules` substituted; + "the current high-priority patterns are in <highPath>; read it and the existing <targetFile> (may not exist — treat as empty); rewrite <targetFile> in place with the merged result; write nothing else."
  Update `memory_prompts_test.go`: replace the consolidator test with `TestBuildMemoryPrompt_{Aggregate,Prioritize,Update}` (each asserts base text + its paths; Update asserts `<MAX_RULES>`/`<FILEPATH>` substituted and highPath/targetFile present) and keep `TestBuildMemoryPrompt_Reflect`.

- [ ] **Step 3: reflection.go — the chain.** Delete `reconcileAndSave`, `mergeStores`, `stripYAMLFence`, `initSessionMemory` and the `o.initSessionMemory()` call in `Run()` (grep `orchestrator.go` for it and remove). Implement:
  - `func (o *Orchestrator) maybeRunReflection(ctx, stageID string)`: no-op unless `o.opts.MemoryDir != ""`; `stage := o.graph.Stage(stageID)`; unless `stage != nil && stage.Reflect != nil && stage.Reflect.CanWrite() && !stage.IsScript()` → return. `stageDir := filepath.Join(o.opts.RunDir, stageID)`; `targetFile := memory.StageFile(o.opts.MemoryDir, stage.Reflect.File)`. Increment `pendingReflections`; `SpawnDetached` a goroutine (defer decrement + WakeEventLoop) that runs: `dataset := filepath.Join(stageDir, "reflect_dataset.yaml")`; run reflect (spec kind reflect, sources=[stageDir], datasetOut=dataset); then `o.distill(ctx, stage.Name, []string{dataset}, stageDir, targetFile)`.
  - `func (o *Orchestrator) distill(ctx, stageName string, datasets []string, logDir, targetFile string)` (serialized by `reflectMu` — Lock at top, defer Unlock): 
    1. aggregate → `filepath.Join(logDir,"patterns.md")` (inPaths=datasets). On err → reflectFailed, return.
    2. prioritize → `filepath.Join(logDir,"prioritized.md")` (in=patterns.md). On err → reflectFailed, return.
    3. code: read prioritized.md; `high := memory.SelectHigh(string(data))`; if `high == ""` → reflectNotice "no high-priority patterns", return; `highPath := filepath.Join(logDir,"high.md")`; `memory.AtomicWrite(highPath, []byte(high))`.
    4. update → spec kind update, highPath, targetFile, maxRules=o.opts.Memory.MaxRules. On err → reflectFailed.
  - Rename/repurpose `runFinalReflectionOnce` → `runEndOfRunMemory(ctx)`: guard `finalReflectDone`; no-op if `MemoryDir==""`; gather all `<RunDir>/*/reflect_dataset.yaml` that exist; if none → return; `o.distill(ctx, "flow-memory", datasets, o.opts.RunDir, memory.ProjectFile(o.opts.MemoryDir))`; then if `o.opts.Memory.Commit` → `committed, err := memory.Commit(o.opts.MemoryDir, "chore(memory): update project memory")`; on err → reflectNotice. Keep the call site in `Run()` (where `runFinalReflectionOnce` was called), renamed.
  - Keep `reflectFailed`/`reflectNotice` as-is.
  - Note the reflect step is per-stage only; the end-of-run pass reuses the per-stage datasets (no reflect call).

- [ ] **Step 4: memory_inject.go — pointer.** Rewrite `memoryBlockForStage(s flow.Stage) string`: if `o.opts.MemoryDir == ""` → "". Build a list of readable absolute paths: always include `memory.ProjectFile(o.opts.MemoryDir)` **if it exists on disk**; if `s.Reflect != nil && s.Reflect.CanRead()` include `memory.StageFile(o.opts.MemoryDir, s.Reflect.File)` **if it exists**. If the list is empty → "". Else return a `<project_memory>`-free pointer block (the wrapping tag is added by `prompts.Build`) via a rewritten `memoryPointerBlock(paths []string) string` that names each path and instructs: "Project memory lives in these files (Markdown '# Project rules' with '## Pattern' blocks). Read them before you start and follow the rules." Remove the old two-arg `memoryPointerBlock(projectPath, sessionPath)`.

- [ ] **Step 5: run.go wiring.** In `cmd/afm/run.go`: replace the `memProjectPath`/`memSessionPath` block with: `var memDir string; if f.MemoryEnabled() { memDir = f.Memory.Path; if !filepath.IsAbs(memDir) { base := agentRootDir; if base == "" { base = rootDir }; memDir = filepath.Join(base, memDir) } }`. In the `Options{...}` literal: remove `MemoryProjectPath`/`MemorySessionPath`, add `MemoryDir: memDir` (keep `Memory: f.Memory`).

- [ ] **Step 6: Update `reflection_test.go`.** Rewrite `pipelineHarness` and tests for the v3 chain: stub `runMemoryAgent` keyed by kind — reflect writes `datasetOut` ("project_level: []\nsession_level: []\n"), aggregate writes `out` ("1. P — desc"), prioritize writes `out` ("## High\n1. P — desc\n\n## Medium\n1. m\n\n## Low\n1. l\n"), update writes `targetFile` ("# Project rules\n\n## P\n\ndesc\n"). Tests:
  - `TestMaybeRunReflection_NoOpWhenDisabled` (MemoryDir=="" → stub never called).
  - `TestMaybeRunReflection_WritesStageFile`: stage with `Reflect{File:"s.md",Mode:"rw"}`, MemoryDir set; after `maybeRunReflection`+`WaitAgents`, `memory.StageFile(dir,"s.md")` exists with "# Project rules"; order was reflect,aggregate,prioritize,update.
  - `TestMaybeRunReflection_ModeR_DoesNotWrite`: `Mode:"r"` → no write chain (stub not called for reflect/update).
  - `TestMaybeRunReflection_ScriptSkipped`.
  - `TestEndOfRunMemory_WritesProjectFile`: two stage datasets present → `runEndOfRunMemory` writes `memory.ProjectFile(dir)`; `finalReflectDone` prevents a second run.
  - `TestEndOfRunMemory_CommitsWhenEnabled` (Memory.Commit=true, MemoryDir is a temp git repo) → after the pass, `git log` has a commit. (Or assert via `memory.Commit` side effect; keep light.)
  Create stage dirs you pass as logDir via `os.MkdirAll` (production dirs exist; tests must create them).

- [ ] **Step 7: memory_inject_test.go.** Rewrite: disabled → ""; with a written `memory.md` → block names it; stage `Mode:"r"` with an existing stage file → block names both; `Mode:"w"` → stage file NOT in block; nonexistent files → not named.

- [ ] **Step 8: Build + test.** `go build ./...` (now GREEN), `go test ./pkg/orchestrator/ ./pkg/flow/ ./pkg/memory/ ./pkg/prompts/ ./cmd/afm/ -race`, `golangci-lint run ./...` (0 new). Fix your own failures. Remember tests that run `runReflectionPipeline`/`distill` must `MkdirAll` the logDir.

- [ ] **Step 9: Commit** `git add -A && git commit -m "feat(memory): конвейер v3 (reflect→aggregate→prioritize→high→update) + инъекция + commit + wiring"` (--no-verify ok if hook slow; build green).

---

### Task 5: Integration test + docs

**Files:**
- Create: `pkg/orchestrator/memory_integration_test.go` (replace the v2 one)
- Modify: `AGENTS.md`, `README.md`, `release-notes.md`

- [ ] **Step 1: Integration test** (internal package, stub `runMemoryAgent`): a stage with `reflect:{file:"build.md",mode:"rw"}` and `memory.path` set drives `maybeRunReflection`; assert `reflect_dataset.yaml`, `patterns.md`, `prioritized.md`, `high.md` exist in the stage dir and `<memdir>/build.md` is written with `# Project rules`; then `runEndOfRunMemory` writes `<memdir>/memory.md`; and a later stage's `memoryBlockForStage`+`prompts.Build` output points at `memory.md`. Name `TestIntegration_MemoryV3*`. Run `-race`.

- [ ] **Step 2: AGENTS.md** — replace the `## Agent memory` section with v3: directory store (`memory.path`, `memory.md` + per-stage files), `reflect:{file,mode}` object with r/w/rw, the reflect→aggregate→prioritize→select-high→update chain (per-stage + end-of-run over all datasets), P1 exclude-list kept, Markdown rules format + `max_rules` cap in the update prompt, pointer injection by mode, `commit`, and what was removed from v2. Reference the v3 spec + this plan.

- [ ] **Step 3: README.md** — replace the `### Agent Memory` section: `memory.path` directory, `max_rules`, `commit`; per-stage `reflect:{file,mode}`; two-tier files (project `memory.md` + per-stage); the pattern chain in brief.

- [ ] **Step 4: release-notes.md** — add a top `## 2026-08-28` "Agent memory v3" entry summarizing the redesign. Leave `LIVE-RUN: <to be filled after live verification>`.

- [ ] **Step 5: Commit** `git add -A && git commit -m "test(memory): интеграционный тест v3 + docs (AGENTS/README/release-notes)"`

---

## Self-Review

**Spec coverage:**
- Config `path`/`max_rules`/`commit` + `reflect{file,mode}` → Task 1. ✓
- Directory layout + `memory.md`/stage files + path resolution → Tasks 2 (paths), 4 (StageFile/ProjectFile use), 5. ✓
- 4-step chain per-stage + end-of-run over all datasets → Task 4 (`distill`, `maybeRunReflection`, `runEndOfRunMemory`). ✓
- P1 exclude-list → Task 3 (`reflect.md`). ✓
- Injection by mode + memory.md always → Task 4 (memory_inject). ✓
- SelectHigh code step → Task 2 + used in Task 4. ✓
- commit → Task 2 (`Commit`) + Task 4 (wired in end-of-run). ✓
- Remove v2 store/consolidator/session/relevance-config → Tasks 1 (config), 2 (delete files), 3 (prompt), 4 (reflection/inject rewrite). ✓
- Docs → Task 5.

**Placeholder scan:** none — every code step has concrete code or precise instructions; prompt files are given verbatim; `<FILEPATH>`/`<MAX_RULES>` are intentional substitution tokens defined in Task 4 Step 2.

**Type consistency:** `MemoryConfig{Path,MaxRules,Commit}` + `Reflect{File,Mode}`/`CanRead`/`CanWrite` (T1) used in T4. `memory.{ProjectFile,StageFile,SelectHigh,AtomicWrite,Commit}` (T2) consumed in T4. `Prompts.{Reflect,Aggregate,Prioritize,Update}` (T3) used in T4 buildMemoryPrompt. `Options.MemoryDir` (T4) set in run.go (T4 Step 5) and read by reflection/inject. `memoryKind*` + `memoryAgentSpec` (T4) internal to orchestrator. Chain order reflect→aggregate→prioritize→update consistent across T3/T4/T5.

**Build-break sequencing (intentional):** T1/T3 leave `pkg/orchestrator` non-compiling; T4 restores green. T2 deletes v2 `pkg/memory` (consumers break until T4). This is called out in each task; only T4's Step 8 requires a fully green `go build ./...`.
