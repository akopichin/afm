# Agent memory (reflect → updater → compressor) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** After a stage completes (opt-in `reflect: true`), run a code-driven pipeline of three fresh-context agents that distills the stage's session into project/session memory files; later stages are pointed at that memory and read it themselves.

**Architecture:** A new `memory:` block on `flow.Flow` enables the feature. Per stage, `maybeRunReflection` (called from `completeStage`, background, best-effort, serialized by a mutex) spawns a detached goroutine that runs three agents in sequence via one injectable seam (`runMemoryAgent`): **reflect** writes a YAML dataset, **updater** rewrites both `.md` memory files, and a code-side size check drives **compressor** loops with a FIFO fallback. A pointer to the two memory files is injected into every stage's prompt through the existing `GlobalPrompt` path. An optional `final_reflect` runs the same pipeline once over the whole run at `Run()` exit.

**Tech Stack:** Go 1.x (do NOT change the go.mod version), `pkg/flow`, `pkg/orchestrator`, `pkg/orchestrator/concurrency`, `pkg/prompts`, `assets` (`//go:embed`), `cmd/afm`. Tests: standard `go test -race`, stub injection (mirrors `injectFixStub`).

**Spec:** `docs/superpowers/specs/2026-08-28-agent-memory-design.md`

## Global Constraints

- **Do NOT change the go version in `go.mod`.**
- **Lint must pass** (`make lint` / `golangci-lint run`); no deprecated constructs.
- **Commits in Russian, no `Co-Authored-By` trailer.** Prefer scoped messages (`feat(memory): …`, `docs: …`).
- **Simplicity first:** plain `bool` over `*bool` when the default equals the zero value; values over pointers; delete-over-add where possible.
- **Reflection is best-effort:** it MUST NEVER touch the FSM and MUST NEVER fail a stage or the run.
- **Data flows through files, not stdout capture:** every agent reads/writes files at absolute paths given in its prompt.
- **Memory files are written atomically by afm** (temp + rename) — never rely on the agent for atomicity. (Agents write via their own tools; afm's compressor/fallback code paths that touch files directly use temp+rename.)
- Default `max_bytes = 20000`, `compress_retries = 2`.
- The three prompt drafts live in `tmp/mem_prompts/{reflect,updater,compressor}.md` — copy them verbatim into `assets/prompts/`.

---

### Task 1: Flow config, `Stage.Reflect`, validation, defaults

**Files:**
- Modify: `pkg/flow/flow.go` (add `MemoryConfig`, `Flow.Memory`, `Stage.Reflect`; extend `validate()` and `ParseFile` defaults)
- Test: `pkg/flow/flow_test.go`

**Interfaces:**
- Produces:
  - `type MemoryConfig struct { ProjectFile string; MaxBytes int; CompressRetries int; FinalReflect bool }`
  - `Flow.Memory MemoryConfig` (`yaml:"memory,omitempty"`)
  - `Stage.Reflect bool` (`yaml:"reflect,omitempty"`)
  - `(f *Flow) MemoryEnabled() bool` → `f.Memory.ProjectFile != ""`

- [ ] **Step 1: Write the failing tests**

Add to `pkg/flow/flow_test.go`:

```go
func TestParseMemory_FieldsAndDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "flow.yaml")
	os.WriteFile(path, []byte(`
name: f
memory:
  project_file: docs/PROJECT_MEMORY.md
stages:
  - name: build
    reflect: true
    agents: [planning, implementation]
`), 0644)

	f, err := ParseFile(path)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !f.MemoryEnabled() {
		t.Fatal("expected memory enabled")
	}
	if f.Memory.MaxBytes != 20000 {
		t.Errorf("MaxBytes default = %d, want 20000", f.Memory.MaxBytes)
	}
	if f.Memory.CompressRetries != 2 {
		t.Errorf("CompressRetries default = %d, want 2", f.Memory.CompressRetries)
	}
	if !f.Stages[0].Reflect {
		t.Error("expected stage.Reflect true")
	}
}

func TestParseMemory_ReflectDefaultsFalse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "flow.yaml")
	os.WriteFile(path, []byte(`
name: f
memory:
  project_file: docs/PROJECT_MEMORY.md
stages:
  - name: build
    agents: [planning, implementation]
`), 0644)
	f, err := ParseFile(path)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if f.Stages[0].Reflect {
		t.Error("Reflect must default to false")
	}
}

func TestValidateMemory_ReflectRequiresProjectFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "flow.yaml")
	os.WriteFile(path, []byte(`
name: f
stages:
  - name: build
    reflect: true
    agents: [planning, implementation]
`), 0644)
	if _, err := ParseFile(path); err == nil {
		t.Fatal("expected error: reflect without memory.project_file")
	}
}

func TestValidateMemory_FinalReflectRequiresProjectFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "flow.yaml")
	os.WriteFile(path, []byte(`
name: f
memory:
  final_reflect: true
stages:
  - name: build
    agents: [planning, implementation]
`), 0644)
	if _, err := ParseFile(path); err == nil {
		t.Fatal("expected error: final_reflect without memory.project_file")
	}
}

func TestValidateMemory_ScriptStageReflectAllowed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "flow.yaml")
	os.WriteFile(path, []byte(`
name: f
memory:
  project_file: docs/PROJECT_MEMORY.md
stages:
  - name: gen
    reflect: true
    script: "echo hi"
`), 0644)
	if _, err := ParseFile(path); err != nil {
		t.Fatalf("script stage with reflect:true must parse: %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/flow/ -run TestParseMemory -run TestValidateMemory -v`
Expected: FAIL (undefined `MemoryConfig`/`Memory`/`Reflect`/`MemoryEnabled`).

- [ ] **Step 3: Add the struct fields**

In `pkg/flow/flow.go`, add after the `Flow` struct:

```go
// MemoryConfig — настройки agent-памяти флоу. ProjectFile непустой включает
// всю фичу (см. docs/superpowers/specs/2026-08-28-agent-memory-design.md).
type MemoryConfig struct {
	// ProjectFile — путь к PROJECT_MEMORY.md относительно root_dir; репозиторный
	// файл, накапливается между ранами. Непусто = фича включена.
	ProjectFile string `yaml:"project_file,omitempty"`
	// MaxBytes — порог размера на КАЖДЫЙ файл памяти, выше которого запускается
	// компрессор. 0 → дефолт 20000 (ставится в ParseFile).
	MaxBytes int `yaml:"max_bytes,omitempty"`
	// CompressRetries — попыток компрессии до терминального FIFO-fallback.
	// 0 → дефолт 2.
	CompressRetries int `yaml:"compress_retries,omitempty"`
	// FinalReflect — один прогон конвейера по ВСЕЙ сессии флоу в конце Run().
	FinalReflect bool `yaml:"final_reflect,omitempty"`
}
```

Add `Memory MemoryConfig \`yaml:"memory,omitempty"\`` to the `Flow` struct (next to `RootDir`), and `Reflect bool \`yaml:"reflect,omitempty"\`` to the `Stage` struct (next to `Buttons`, with a doc comment):

```go
	// Reflect (opt-in, дефолт false): после успешного завершения этой стадии
	// запустить конвейер agent-памяти (reflect→updater→compressor) по её
	// сессии. Требует непустой memory.project_file на уровне флоу. На script-
	// стадии допустимо, но во время выполнения тихо пропускается (нет
	// агентской сессии). Обычный bool — дефолт совпадает с нулевым значением.
	Reflect bool `yaml:"reflect,omitempty"`
```

Add the helper:

```go
// MemoryEnabled сообщает, включена ли agent-память для этого флоу.
func (f *Flow) MemoryEnabled() bool { return f.Memory.ProjectFile != "" }
```

- [ ] **Step 4: Add validation**

In `(f *Flow) validate()`, append a new block (after the existing loops):

```go
	if !f.MemoryEnabled() {
		if f.Memory.FinalReflect {
			return fmt.Errorf("memory.final_reflect requires memory.project_file")
		}
		for _, s := range f.Stages {
			if s.Reflect {
				return fmt.Errorf("stage %q: reflect requires memory.project_file", s.ID)
			}
		}
	}
```

- [ ] **Step 5: Add defaults in ParseFile**

Find the `ParseFile` block that applies `defaultScriptTimeout` (around `pkg/flow/flow.go:304-326`). After that loop (still inside `ParseFile`, before `return`), add:

```go
	if f.MemoryEnabled() {
		if f.Memory.MaxBytes == 0 {
			f.Memory.MaxBytes = 20000
		}
		if f.Memory.CompressRetries == 0 {
			f.Memory.CompressRetries = 2
		}
	}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./pkg/flow/ -run 'TestParseMemory|TestValidateMemory' -v`
Expected: PASS (all 5).

- [ ] **Step 7: Commit**

```bash
git add pkg/flow/flow.go pkg/flow/flow_test.go
git commit -m "feat(memory): конфиг memory + stage.reflect в flow.yaml с валидацией"
```

---

### Task 2: Embed the three prompts, extend `Prompts` and `loadPrompts`

**Files:**
- Create: `assets/prompts/reflect.md`, `assets/prompts/updater.md`, `assets/prompts/compressor.md` (copy from `tmp/mem_prompts/`)
- Modify: `pkg/orchestrator/orchestrator.go` (`Prompts` struct)
- Modify: `cmd/afm/run.go` (`loadPrompts`)
- Test: `cmd/afm/run_test.go` (or wherever `loadPrompts`/prompt loading is tested)

**Interfaces:**
- Produces: `orchestrator.Prompts` gains `Reflect`, `Updater`, `Compressor string` fields, populated by `loadPrompts`.

- [ ] **Step 1: Copy the prompt files**

```bash
cp tmp/mem_prompts/reflect.md    assets/prompts/reflect.md
cp tmp/mem_prompts/updater.md    assets/prompts/updater.md
cp tmp/mem_prompts/compressor.md assets/prompts/compressor.md
```

(They are picked up by the existing `//go:embed prompts` in `assets/assets.go` — no embed directive change needed.)

- [ ] **Step 2: Write the failing test**

Add to the file that tests prompt loading (`cmd/afm/run_test.go`):

```go
func TestLoadPrompts_IncludesMemoryPrompts(t *testing.T) {
	p, err := loadPrompts("") // "" = embedded defaults
	if err != nil {
		t.Fatalf("loadPrompts: %v", err)
	}
	if p.Reflect == "" || p.Updater == "" || p.Compressor == "" {
		t.Fatalf("memory prompts empty: reflect=%d updater=%d compressor=%d",
			len(p.Reflect), len(p.Updater), len(p.Compressor))
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./cmd/afm/ -run TestLoadPrompts_IncludesMemoryPrompts -v`
Expected: FAIL (`p.Reflect` undefined).

- [ ] **Step 4: Extend the `Prompts` struct**

In `pkg/orchestrator/orchestrator.go`:

```go
type Prompts struct {
	Planning       string
	Implementation string
	Review         string
	Summary        string
	Autonomous     string
	Reflect        string
	Updater        string
	Compressor     string
}
```

- [ ] **Step 5: Extend `loadPrompts`**

In `cmd/afm/run.go`, extend the `names` slice and the returned struct:

```go
	names := []string{"planning.md", "implementation.md", "review.md", "summary.md", "autonomous.md",
		"reflect.md", "updater.md", "compressor.md"}
```

```go
	return orchestrator.Prompts{
		Planning:       texts[0],
		Implementation: texts[1],
		Review:         texts[2],
		Summary:        texts[3],
		Autonomous:     texts[4],
		Reflect:        texts[5],
		Updater:        texts[6],
		Compressor:     texts[7],
	}, nil
```

- [ ] **Step 6: Run test to verify it passes**

Run: `go test ./cmd/afm/ -run TestLoadPrompts_IncludesMemoryPrompts -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add assets/prompts/reflect.md assets/prompts/updater.md assets/prompts/compressor.md pkg/orchestrator/orchestrator.go cmd/afm/run.go cmd/afm/run_test.go
git commit -m "feat(memory): embed промптов reflect/updater/compressor + loadPrompts"
```

---

### Task 3: Thread memory config into `Options`; inject the memory pointer into `GlobalPrompt`

**Files:**
- Modify: `pkg/orchestrator/orchestrator.go` (`Options` struct)
- Modify: `cmd/afm/run.go` (compute abs paths, build pointer block, extend `orchestrator.New(Options{…})`)
- Create: `cmd/afm/memory_pointer.go` (pure helper `buildMemoryPointer`)
- Test: `cmd/afm/memory_pointer_test.go`

**Interfaces:**
- Consumes: `flow.MemoryConfig`, `flow.Flow.MemoryEnabled()` (Task 1).
- Produces:
  - `Options` gains `Memory flow.MemoryConfig`, `MemoryProjectPath string` (abs; `""` = disabled), `MemorySessionPath string` (abs).
  - `func buildMemoryPointer(projectPath, sessionPath string) string` — the `<global_prompt>`-appended pointer block (empty string when `projectPath == ""`).

- [ ] **Step 1: Write the failing test**

Create `cmd/afm/memory_pointer_test.go`:

```go
package main

import (
	"strings"
	"testing"
)

func TestBuildMemoryPointer_MentionsBothFiles(t *testing.T) {
	got := buildMemoryPointer("/proj/docs/PROJECT_MEMORY.md", "/runs/r1/SESSION_MEMORY.md")
	if !strings.Contains(got, "/proj/docs/PROJECT_MEMORY.md") {
		t.Error("missing project path")
	}
	if !strings.Contains(got, "/runs/r1/SESSION_MEMORY.md") {
		t.Error("missing session path")
	}
}

func TestBuildMemoryPointer_EmptyWhenDisabled(t *testing.T) {
	if buildMemoryPointer("", "/runs/r1/SESSION_MEMORY.md") != "" {
		t.Error("expected empty pointer when project path is empty")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/afm/ -run TestBuildMemoryPointer -v`
Expected: FAIL (`buildMemoryPointer` undefined).

- [ ] **Step 3: Implement the pointer helper**

Create `cmd/afm/memory_pointer.go`:

```go
package main

import "fmt"

// buildMemoryPointer возвращает текст, дописываемый к GlobalPrompt, который
// СООБЩАЕТ агенту путь к файлам памяти (содержимое агент читает сам своим
// Read — так промпт не растёт вместе с памятью). Пусто, если память выключена.
func buildMemoryPointer(projectPath, sessionPath string) string {
	if projectPath == "" {
		return ""
	}
	return fmt.Sprintf(`Project memory — accumulated findings from earlier stages and runs — lives at:
  %s
Session memory — this run's short-term context — lives at:
  %s
Before you start, read both files and take their Best Practices (🟩) and Anti-Patterns (🟥) into account. They may not exist yet on the first stage; that is fine.`, projectPath, sessionPath)
}
```

- [ ] **Step 4: Extend `Options`**

In `pkg/orchestrator/orchestrator.go`, add to `Options`:

```go
	Memory            flow.MemoryConfig // agent-память: включена, если MemoryProjectPath != ""
	MemoryProjectPath string            // abs путь к PROJECT_MEMORY.md ("" = выключено)
	MemorySessionPath string            // abs путь к SESSION_MEMORY.md в run-папке
```

- [ ] **Step 5: Wire in run.go**

In `cmd/afm/run.go`, just before the `orchestrator.New(orchestrator.Options{…})` call (where `agentRootDir` is already resolved), add:

```go
			var memProjectPath, memSessionPath string
			if f.MemoryEnabled() {
				base := agentRootDir
				if base == "" {
					base = rootDir
				}
				memProjectPath = f.Memory.ProjectFile
				if !filepath.IsAbs(memProjectPath) {
					memProjectPath = filepath.Join(base, memProjectPath)
				}
				memSessionPath = filepath.Join(runDir, "SESSION_MEMORY.md")
			}
			globalPrompt := f.Prompt
			if ptr := buildMemoryPointer(memProjectPath, memSessionPath); ptr != "" {
				if globalPrompt != "" {
					globalPrompt += "\n\n"
				}
				globalPrompt += ptr
			}
```

Then change the `Options` literal:

```go
				GlobalPrompt:      globalPrompt,
				Memory:            f.Memory,
				MemoryProjectPath: memProjectPath,
				MemorySessionPath: memSessionPath,
```

(Replace the existing `GlobalPrompt: f.Prompt,` line.)

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./cmd/afm/ -run TestBuildMemoryPointer -v && go build ./...`
Expected: PASS + clean build.

- [ ] **Step 7: Commit**

```bash
git add cmd/afm/memory_pointer.go cmd/afm/memory_pointer_test.go cmd/afm/run.go pkg/orchestrator/orchestrator.go
git commit -m "feat(memory): указатель на файлы памяти в GlobalPrompt + проброс конфига в Options"
```

---

### Task 4: Prompt builders for the three agents

**Files:**
- Create: `pkg/orchestrator/memory_prompts.go`
- Test: `pkg/orchestrator/memory_prompts_test.go`

**Interfaces:**
- Consumes: `orchestrator.Prompts.{Reflect,Updater,Compressor}` (Task 2) — passed in as the base template string.
- Produces:
  - `type memoryAgentSpec struct { kind, stageName, command, logFile string; sources []string; datasetOut, datasetPath, projectPath, sessionPath, targetFile string; lineLimit int }`
  - `func buildMemoryPrompt(p Prompts, spec memoryAgentSpec) string` — dispatches on `spec.kind` (`"reflect"|"updater"|"compressor"`), wrapping the embedded template with concrete absolute paths and file-I/O instructions.

- [ ] **Step 1: Write the failing tests**

Create `pkg/orchestrator/memory_prompts_test.go`:

```go
package orchestrator

import (
	"strings"
	"testing"
)

func testPrompts() Prompts {
	return Prompts{Reflect: "REFLECT-BASE", Updater: "UPDATER-BASE", Compressor: "COMPRESS-BASE"}
}

func TestBuildMemoryPrompt_Reflect(t *testing.T) {
	got := buildMemoryPrompt(testPrompts(), memoryAgentSpec{
		kind:       "reflect",
		sources:    []string{"/run/s1/autonomous.log", "/run/s1/execution_summary.md"},
		datasetOut: "/run/s1/reflect_dataset.yaml",
	})
	for _, want := range []string{"REFLECT-BASE", "/run/s1/autonomous.log", "/run/s1/execution_summary.md", "/run/s1/reflect_dataset.yaml"} {
		if !strings.Contains(got, want) {
			t.Errorf("reflect prompt missing %q", want)
		}
	}
}

func TestBuildMemoryPrompt_Updater(t *testing.T) {
	got := buildMemoryPrompt(testPrompts(), memoryAgentSpec{
		kind:        "updater",
		datasetPath: "/run/s1/reflect_dataset.yaml",
		projectPath: "/proj/PROJECT_MEMORY.md",
		sessionPath: "/run/SESSION_MEMORY.md",
	})
	for _, want := range []string{"UPDATER-BASE", "/run/s1/reflect_dataset.yaml", "/proj/PROJECT_MEMORY.md", "/run/SESSION_MEMORY.md"} {
		if !strings.Contains(got, want) {
			t.Errorf("updater prompt missing %q", want)
		}
	}
}

func TestBuildMemoryPrompt_CompressorPlain(t *testing.T) {
	got := buildMemoryPrompt(testPrompts(), memoryAgentSpec{
		kind:       "compressor",
		targetFile: "/proj/PROJECT_MEMORY.md",
	})
	if !strings.Contains(got, "COMPRESS-BASE") || !strings.Contains(got, "/proj/PROJECT_MEMORY.md") {
		t.Error("compressor prompt missing base or path")
	}
	if strings.Contains(got, "CRITICAL: reduce") {
		t.Error("plain compressor must not contain the line-limit tail")
	}
}

func TestBuildMemoryPrompt_CompressorExtreme(t *testing.T) {
	got := buildMemoryPrompt(testPrompts(), memoryAgentSpec{
		kind:       "compressor",
		targetFile: "/proj/PROJECT_MEMORY.md",
		lineLimit:  40,
	})
	if !strings.Contains(got, "CRITICAL: reduce") || !strings.Contains(got, "40") {
		t.Error("extreme compressor must contain the dynamic line-limit tail with N=40")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/orchestrator/ -run TestBuildMemoryPrompt -v`
Expected: FAIL (`buildMemoryPrompt`/`memoryAgentSpec` undefined).

- [ ] **Step 3: Implement the builders**

Create `pkg/orchestrator/memory_prompts.go`:

```go
package orchestrator

import (
	"fmt"
	"strings"
)

// memoryAgentSpec — единый параметр для запуска одного агента конвейера памяти.
// Заполняются только поля, релевантные kind. Один seam (o.runMemoryAgent)
// принимает этот spec — так тесты подменяют реальный запуск процесса.
type memoryAgentSpec struct {
	kind      string // "reflect" | "updater" | "compressor"
	stageName string // для лога/имени
	command   string // разрешённая команда агента (пусто → дефолтный клиент)
	logFile   string // абс. путь к логу этого агента

	// reflect:
	sources    []string // абс. пути (файлы или директории) для чтения
	datasetOut string   // абс. путь, куда записать YAML-датасет

	// updater:
	datasetPath string // reflect_dataset.yaml
	projectPath string // PROJECT_MEMORY.md
	sessionPath string // SESSION_MEMORY.md

	// compressor:
	targetFile string // единственный файл для сжатия
	lineLimit  int    // >0 → добавить динамический «reduce to under N lines»
}

// buildMemoryPrompt оборачивает вкомпиленный шаблон (p.Reflect/Updater/
// Compressor) конкретными абсолютными путями и инструкциями файлового I/O.
func buildMemoryPrompt(p Prompts, spec memoryAgentSpec) string {
	switch spec.kind {
	case "reflect":
		var b strings.Builder
		b.WriteString(p.Reflect)
		b.WriteString("\n\n# AFM FILE I/O (added by afm)\n")
		b.WriteString("Read ONLY these sources (if a path is a directory, read every *.log file under it):\n")
		for _, s := range spec.sources {
			fmt.Fprintf(&b, "  - %s\n", s)
		}
		fmt.Fprintf(&b, "Write the resulting YAML document to this EXACT file, and write nothing else:\n  %s\n", spec.datasetOut)
		b.WriteString("Do not modify any other file. Do not ask questions.\n")
		return b.String()
	case "updater":
		var b strings.Builder
		b.WriteString(p.Updater)
		b.WriteString("\n\n# AFM FILE I/O (added by afm)\n")
		fmt.Fprintf(&b, "Input YAML dataset: %s\n", spec.datasetPath)
		fmt.Fprintf(&b, "PROJECT_MEMORY.md path: %s\n", spec.projectPath)
		fmt.Fprintf(&b, "SESSION_MEMORY.md path: %s\n", spec.sessionPath)
		b.WriteString("Read the dataset and both memory files (they may not exist yet — treat a missing file as empty). ")
		b.WriteString("Rewrite BOTH memory files IN PLACE at the exact paths above with the consolidated content. ")
		b.WriteString("Do not create any other file. Do not ask questions.\n")
		return b.String()
	case "compressor":
		var b strings.Builder
		b.WriteString(p.Compressor)
		b.WriteString("\n\n# AFM FILE I/O (added by afm)\n")
		fmt.Fprintf(&b, "Compress this file IN PLACE (overwrite the same path): %s\n", spec.targetFile)
		b.WriteString("Do not modify any other file. Do not ask questions.\n")
		if spec.lineLimit > 0 {
			fmt.Fprintf(&b, "\nCRITICAL: reduce the total line count of this file to under %d lines while preserving all core safety principles and architectural invariants.\n", spec.lineLimit)
		}
		return b.String()
	default:
		return ""
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/orchestrator/ -run TestBuildMemoryPrompt -v`
Expected: PASS (all 4).

- [ ] **Step 5: Commit**

```bash
git add pkg/orchestrator/memory_prompts.go pkg/orchestrator/memory_prompts_test.go
git commit -m "feat(memory): билдеры промптов reflect/updater/compressor + memoryAgentSpec"
```

---

### Task 5: Size-check and FIFO-fallback helpers

**Files:**
- Create: `pkg/orchestrator/memory_size.go`
- Test: `pkg/orchestrator/memory_size_test.go`

**Interfaces:**
- Produces:
  - `func fileExceeds(path string, maxBytes int) bool` — true iff the file exists and its size > maxBytes (missing file → false).
  - `func fifoDropOldestBlocks(path string, maxBytes int) error` — atomically (temp+rename) trims the file to fit `maxBytes` by dropping the OLDEST `##`-delimited blocks from the top while keeping the level-1 header; best-effort, keeps at least the header.
  - `func lineLimitForBytes(maxBytes int) int` — derives a hard line cap for the extreme compressor pass (≈ maxBytes/80, min 10).

- [ ] **Step 1: Write the failing tests**

Create `pkg/orchestrator/memory_size_test.go`:

```go
package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileExceeds(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "m.md")
	os.WriteFile(p, []byte(strings.Repeat("x", 100)), 0644)
	if !fileExceeds(p, 50) {
		t.Error("100 > 50 should exceed")
	}
	if fileExceeds(p, 200) {
		t.Error("100 < 200 should not exceed")
	}
	if fileExceeds(filepath.Join(dir, "missing.md"), 0) {
		t.Error("missing file must not exceed")
	}
}

func TestFifoDropOldestBlocks(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "m.md")
	content := "# PROJECT MEMORY\n\n## Block A\n" + strings.Repeat("a", 200) +
		"\n## Block B\n" + strings.Repeat("b", 200) + "\n## Block C\n" + strings.Repeat("c", 50) + "\n"
	os.WriteFile(p, []byte(content), 0644)

	if err := fifoDropOldestBlocks(p, 300); err != nil {
		t.Fatalf("drop: %v", err)
	}
	out, _ := os.ReadFile(p)
	s := string(out)
	if !strings.HasPrefix(s, "# PROJECT MEMORY") {
		t.Error("header must be preserved")
	}
	if strings.Contains(s, "Block A") {
		t.Error("oldest block A should have been dropped")
	}
	if !strings.Contains(s, "Block C") {
		t.Error("newest block C must survive")
	}
	if len(out) > 300 {
		t.Errorf("size %d still over 300", len(out))
	}
}

func TestLineLimitForBytes(t *testing.T) {
	if lineLimitForBytes(20000) < 10 {
		t.Error("line limit too small")
	}
	if lineLimitForBytes(0) < 10 {
		t.Error("line limit must have a floor of 10")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/orchestrator/ -run 'TestFileExceeds|TestFifoDropOldestBlocks|TestLineLimitForBytes' -v`
Expected: FAIL (undefined helpers).

- [ ] **Step 3: Implement the helpers**

Create `pkg/orchestrator/memory_size.go`:

```go
package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
)

// fileExceeds — true, если файл существует и его размер строго больше maxBytes.
func fileExceeds(path string, maxBytes int) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	return fi.Size() > int64(maxBytes)
}

// lineLimitForBytes выводит жёсткий лимит строк для «экстремального» прохода
// компрессора из байтового порога (≈80 байт/строка), пол — 10.
func lineLimitForBytes(maxBytes int) int {
	n := maxBytes / 80
	if n < 10 {
		return 10
	}
	return n
}

// fifoDropOldestBlocks — терминальный fallback: если компрессор-агент не смог
// уложиться в maxBytes, детерминированно удаляем самые СТАРЫЕ (верхние) блоки
// «## …» файла, сохраняя level-1 заголовок (первая строка «# …»), пока размер
// не уложится в порог. НЕ true-LRU (сигнала recency нет) — FIFO по позиции,
// т.к. updater дописывает новое ниже. Атомарно (temp+rename). Никогда не
// удаляет заголовок целиком.
func fifoDropOldestBlocks(path string, maxBytes int) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")

	// Заголовок = ведущие строки до первого «## ».
	header := []string{}
	i := 0
	for ; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") {
			break
		}
		header = append(header, lines[i])
	}
	// Блоки: каждый начинается со строки «## » и идёт до следующей «## ».
	type block struct{ lines []string }
	var blocks []block
	var cur *block
	for ; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") {
			blocks = append(blocks, block{})
			cur = &blocks[len(blocks)-1]
		}
		if cur != nil {
			cur.lines = append(cur.lines, lines[i])
		}
	}

	rendered := func() string {
		var b strings.Builder
		b.WriteString(strings.Join(header, "\n"))
		for _, bl := range blocks {
			b.WriteString("\n")
			b.WriteString(strings.Join(bl.lines, "\n"))
		}
		return b.String()
	}

	// Дропаем самые старые блоки, пока не уложимся или блоки не кончатся.
	for len(blocks) > 0 && len(rendered()) > maxBytes {
		blocks = blocks[1:]
	}

	return atomicWriteFile(path, []byte(rendered()))
}

// atomicWriteFile пишет data в path через temp+rename в той же директории.
func atomicWriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".mem-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/orchestrator/ -run 'TestFileExceeds|TestFifoDropOldestBlocks|TestLineLimitForBytes' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/orchestrator/memory_size.go pkg/orchestrator/memory_size_test.go
git commit -m "feat(memory): size-check + FIFO-fallback + атомарная запись"
```

---

### Task 6: `runMemoryAgent` seam + default executor implementation

**Files:**
- Create: `pkg/orchestrator/memory_agent.go`
- Modify: `pkg/orchestrator/orchestrator.go` (add the seam field + default it in `New`)
- Test: `pkg/orchestrator/memory_agent_test.go`

**Interfaces:**
- Consumes: `buildMemoryPrompt` (Task 4), `memoryAgentSpec` (Task 4), `executor.Config`/`executor.New`/`RunAgent`, `o.opts.{Config,RootDir,WrapperDir,GeneratedAgents,Debug,RunDir}`.
- Produces:
  - Field on `Orchestrator`: `runMemoryAgent func(ctx context.Context, spec memoryAgentSpec) error` (defaulted to `o.execMemoryAgent` in `New`; tests override it).
  - `func (o *Orchestrator) execMemoryAgent(ctx context.Context, spec memoryAgentSpec) error` — resolves the command, builds the prompt via `buildMemoryPrompt(o.opts.Prompts, spec)`, runs a fresh-context agent (mirrors `runJSONFixAgent`: no `--resume`, no `StageDir`).
  - `func (o *Orchestrator) resolveMemoryCommand(specCommand string) (cmd string, extra []string)` — `specCommand` or the default client command + its `ExtraArgs`.

- [ ] **Step 1: Write the failing test**

Create `pkg/orchestrator/memory_agent_test.go`:

```go
package orchestrator

import (
	"context"
	"testing"
)

// Verifies the seam field exists and is defaulted (non-nil) by New via the
// package's existing test constructor. If your package has a newTestOrch
// helper, reuse it; otherwise construct Options minimally.
func TestRunMemoryAgent_SeamDefaulted(t *testing.T) {
	o := newTestOrchestrator(t) // existing test helper in this package
	if o.runMemoryAgent == nil {
		t.Fatal("runMemoryAgent must be defaulted by New")
	}
	// Override with a stub and confirm it is invoked (no real process).
	called := false
	o.runMemoryAgent = func(ctx context.Context, spec memoryAgentSpec) error {
		called = true
		return nil
	}
	_ = o.runMemoryAgent(context.Background(), memoryAgentSpec{kind: "reflect"})
	if !called {
		t.Fatal("stub not invoked")
	}
}
```

> Note: use the package's existing test-orchestrator constructor. Search the package for how other tests build an `*Orchestrator` (e.g. a `newTestOrchestrator`/`newOrch` helper) and reuse it. If none exists, construct via `New(Options{Store: …, Prompts: …})` with a temp store as other `_test.go` files do.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/orchestrator/ -run TestRunMemoryAgent_SeamDefaulted -v`
Expected: FAIL (`runMemoryAgent` field undefined).

- [ ] **Step 3: Add the seam field and default it**

In `pkg/orchestrator/orchestrator.go`, next to the `spawnJSONFix` field, add:

```go
	// runMemoryAgent запускает один агент конвейера памяти (reflect/updater/
	// compressor). Реальная реализация — execMemoryAgent; тесты подменяют.
	runMemoryAgent func(ctx context.Context, spec memoryAgentSpec) error
```

In `New`, next to `o.spawnJSONFix = o.runJSONFixAgent`, add:

```go
	o.runMemoryAgent = o.execMemoryAgent
```

- [ ] **Step 4: Implement `execMemoryAgent`**

Create `pkg/orchestrator/memory_agent.go`:

```go
package orchestrator

import (
	"context"
	"log"

	"github.com/akopichin/afm/pkg/executor"
)

// resolveMemoryCommand выбирает команду агента: явную из spec или дефолтный
// клиент из конфига (с его ExtraArgs).
func (o *Orchestrator) resolveMemoryCommand(specCommand string) (string, []string) {
	if specCommand != "" {
		return specCommand, nil
	}
	return o.opts.Config.Client.Command, o.opts.Config.Client.ExtraArgs
}

// execMemoryAgent — реальная реализация seam runMemoryAgent: свежий
// изолированный агент (без --resume, без StageDir), CWD=root_dir, читает/пишет
// файлы по абсолютным путям из промпта. Зеркалит runJSONFixAgent, но
// синхронный: конвейер уже крутится в отдельной (SpawnDetached) горутине и
// вызывает шаги последовательно.
func (o *Orchestrator) execMemoryAgent(ctx context.Context, spec memoryAgentSpec) error {
	cmd, extra := o.resolveMemoryCommand(spec.command)
	cfg := executor.Config{
		Command:     cmd,
		ExtraArgs:   executor.ResolveArgs(extra),
		IdleTimeout: o.opts.Config.Executor.IdleTimeout,
		WrapperDir:  wrapperDirFor(cmd, o.opts.WrapperDir, o.opts.GeneratedAgents),
		Dir:         o.opts.RootDir,
		Debug:       o.opts.Debug,
		RunDir:      o.opts.RunDir,
	}
	ex := executor.New(cfg)
	prompt := buildMemoryPrompt(o.opts.Prompts, spec)
	if err := ex.RunAgent(ctx, "memory-"+spec.kind, spec.stageName, prompt, spec.logFile); err != nil {
		log.Printf("WARN: memory %s agent (%s): %v", spec.kind, spec.stageName, err)
		return err
	}
	return nil
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./pkg/orchestrator/ -run TestRunMemoryAgent_SeamDefaulted -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/orchestrator/memory_agent.go pkg/orchestrator/orchestrator.go pkg/orchestrator/memory_agent_test.go
git commit -m "feat(memory): seam runMemoryAgent + реальный execMemoryAgent (свежий агент)"
```

---

### Task 7: The reflection pipeline — `maybeRunReflection`, serialization, shutdown bookkeeping, wiring into `completeStage`/`shouldExit`

**Files:**
- Create: `pkg/orchestrator/reflection.go`
- Modify: `pkg/orchestrator/orchestrator.go` (add `reflectMu sync.Mutex`, `pendingReflections atomic.Int32`; call `maybeRunReflection` from `completeStage`)
- Modify: `pkg/orchestrator/scheduling.go` (`shouldExit` accounts for `pendingReflections`)
- Test: `pkg/orchestrator/reflection_test.go`

**Interfaces:**
- Consumes: `o.runMemoryAgent` (Task 6), `fileExceeds`/`fifoDropOldestBlocks`/`lineLimitForBytes` (Task 5), `memoryAgentSpec` (Task 4), `o.graph.Stage(id)`, `o.opts.{Memory,MemoryProjectPath,MemorySessionPath,RunDir}`, `o.concurrency.{SpawnDetached,WakeEventLoop}`.
- Produces:
  - `func (o *Orchestrator) maybeRunReflection(ctx context.Context, stageID string)` — no-op unless memory enabled, `stage.Reflect`, not a script stage.
  - `func (o *Orchestrator) runReflectionPipeline(ctx context.Context, stageName string, sources []string, logDir string)` — the reflect→updater→compress-loop sequence (also reused by Task 9). Serialized by `reflectMu`.

- [ ] **Step 1: Write the failing tests**

Create `pkg/orchestrator/reflection_test.go`:

```go
package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// pipelineHarness wires an orchestrator with memory enabled and a scripted
// runMemoryAgent stub that simulates each agent's file effects.
func pipelineHarness(t *testing.T, maxBytes, retries int) (*Orchestrator, string, string, *[]string) {
	t.Helper()
	o := newTestOrchestrator(t)
	runDir := t.TempDir()
	proj := filepath.Join(t.TempDir(), "PROJECT_MEMORY.md")
	sess := filepath.Join(runDir, "SESSION_MEMORY.md")
	o.opts.RunDir = runDir
	o.opts.Memory = flowMemory(proj, maxBytes, retries) // helper below
	o.opts.MemoryProjectPath = proj
	o.opts.MemorySessionPath = sess

	var order []string
	o.runMemoryAgent = func(ctx context.Context, spec memoryAgentSpec) error {
		order = append(order, spec.kind)
		switch spec.kind {
		case "reflect":
			os.WriteFile(spec.datasetOut, []byte("project_level: []\nsession_level: []\n"), 0644)
		case "updater":
			// Simulate an oversized PROJECT file on first write.
			os.WriteFile(spec.projectPath, []byte("# PROJECT MEMORY\n\n## A\n"+strings.Repeat("x", maxBytes+500)+"\n"), 0644)
			os.WriteFile(spec.sessionPath, []byte("# SESSION MEMORY\n"), 0644)
		case "compressor":
			// Simulate a compressor that shrinks under the limit.
			os.WriteFile(spec.targetFile, []byte("# PROJECT MEMORY\n\n## A\nsmall\n"), 0644)
		}
		return nil
	}
	return o, proj, sess, &order
}

func TestReflectionPipeline_ReflectThenUpdaterThenCompress(t *testing.T) {
	o, proj, _, order := pipelineHarness(t, 1000, 2)
	o.runReflectionPipeline(context.Background(), "s1", []string{filepath.Join(o.opts.RunDir, "s1", "autonomous.log")}, filepath.Join(o.opts.RunDir, "s1"))
	got := strings.Join(*order, ",")
	if !strings.HasPrefix(got, "reflect,updater,compressor") {
		t.Errorf("order = %q, want reflect,updater,compressor…", got)
	}
	if fileExceeds(proj, 1000) {
		t.Error("project memory should be within limit after compression")
	}
}

func TestMaybeRunReflection_NoOpWhenDisabled(t *testing.T) {
	o := newTestOrchestrator(t)
	o.opts.MemoryProjectPath = "" // disabled
	called := false
	o.runMemoryAgent = func(ctx context.Context, spec memoryAgentSpec) error { called = true; return nil }
	o.maybeRunReflection(context.Background(), "s1")
	o.concurrency.WaitAgents()
	if called {
		t.Error("must not run any memory agent when memory disabled")
	}
}

func TestReflectionPipeline_Serialized(t *testing.T) {
	o, _, _, _ := pipelineHarness(t, 100000, 2)
	var mu sync.Mutex
	concurrent, maxConcurrent := 0, 0
	o.runMemoryAgent = func(ctx context.Context, spec memoryAgentSpec) error {
		mu.Lock()
		concurrent++
		if concurrent > maxConcurrent {
			maxConcurrent = concurrent
		}
		mu.Unlock()
		// small yield to expose overlap if not serialized
		mu.Lock()
		concurrent--
		mu.Unlock()
		return nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); o.runReflectionPipeline(context.Background(), "s", nil, t.TempDir()) }()
	}
	wg.Wait()
	if maxConcurrent > 1 {
		t.Errorf("pipelines overlapped: maxConcurrent=%d (reflectMu not held)", maxConcurrent)
	}
}
```

Add a small helper at the bottom of the test file:

```go
func flowMemory(proj string, maxBytes, retries int) flowMemoryConfig { // alias type below
	return flowMemoryConfig{ProjectFile: proj, MaxBytes: maxBytes, CompressRetries: retries}
}
```

> Replace `flowMemoryConfig` with the real `flow.MemoryConfig` and import `pkg/flow` in the test — the alias is only shown to keep the snippet short. Use: `flow.MemoryConfig{ProjectFile: proj, MaxBytes: maxBytes, CompressRetries: retries}`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/orchestrator/ -run 'TestReflectionPipeline|TestMaybeRunReflection' -v`
Expected: FAIL (undefined `maybeRunReflection`/`runReflectionPipeline`/`reflectMu`).

- [ ] **Step 3: Add orchestrator fields**

In `pkg/orchestrator/orchestrator.go`, near `pendingAfterHooks atomic.Int32`, add:

```go
	// pendingReflections — счётчик живых конвейеров памяти (инкремент в
	// maybeRunReflection ДО SpawnDetached, декремент в обёртке), чтобы
	// shouldExit не завершил ран, пока конвейер в полёте. Зеркалит
	// pendingAfterHooks.
	pendingReflections atomic.Int32
	// reflectMu сериализует запись в общие файлы памяти: одновременно бежит
	// максимум один конвейер (best-effort/фон, латентность очереди неважна).
	reflectMu sync.Mutex
```

(Ensure `sync` is imported.)

- [ ] **Step 4: Implement the pipeline**

Create `pkg/orchestrator/reflection.go`:

```go
package orchestrator

import (
	"context"
	"path/filepath"

	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
)

// maybeRunReflection запускает конвейер памяти в фоне после завершения стадии.
// No-op, если память выключена, стадия без reflect, или это script-стадия
// (нет агентской сессии). Best-effort: НИКОГДА не трогает FSM.
func (o *Orchestrator) maybeRunReflection(ctx context.Context, stageID string) {
	if o.opts.MemoryProjectPath == "" {
		return
	}
	stage := o.graph.Stage(stageID)
	if stage == nil || !stage.Reflect || stage.IsScript() {
		return
	}
	stageDir := filepath.Join(o.opts.RunDir, stageID)
	// Источники: компактные логи фаз стадии + summary/plan. Агент читает сам.
	sources := []string{stageDir} // директория; reflect читает *.log под ней
	o.pendingReflections.Add(1)
	o.concurrency.SpawnDetached(ctx, func(ctx context.Context) {
		defer func() {
			o.pendingReflections.Add(-1)
			o.concurrency.WakeEventLoop()
		}()
		o.runReflectionPipeline(ctx, stage.Name, sources, stageDir)
	})
}

// runReflectionPipeline — reflect → updater → size-check/compress-loop.
// Сериализован reflectMu. logDir — куда класть reflect.log/updater.log/…
// и reflect_dataset.yaml. Best-effort: любая ошибка шага прерывает конвейер
// и логируется нотисом, но не трогает FSM и не валит ран.
func (o *Orchestrator) runReflectionPipeline(ctx context.Context, stageName string, sources []string, logDir string) {
	o.reflectMu.Lock()
	defer o.reflectMu.Unlock()

	datasetOut := filepath.Join(logDir, "reflect_dataset.yaml")

	if err := o.runMemoryAgent(ctx, memoryAgentSpec{
		kind:       "reflect",
		stageName:  stageName,
		sources:    sources,
		datasetOut: datasetOut,
		logFile:    filepath.Join(logDir, "reflect.log"),
	}); err != nil {
		o.reflectFailed(stageName, "reflect", err)
		return
	}

	if err := o.runMemoryAgent(ctx, memoryAgentSpec{
		kind:        "updater",
		stageName:   stageName,
		datasetPath: datasetOut,
		projectPath: o.opts.MemoryProjectPath,
		sessionPath: o.opts.MemorySessionPath,
		logFile:     filepath.Join(logDir, "updater.log"),
	}); err != nil {
		o.reflectFailed(stageName, "updater", err)
		return
	}

	for _, target := range []string{o.opts.MemoryProjectPath, o.opts.MemorySessionPath} {
		o.compressIfNeeded(ctx, stageName, target, logDir)
	}
}

// compressIfNeeded гоняет компрессор до compress_retries раз; на последней
// попытке добавляет динамический line-limit; если всё ещё превышен —
// FIFO-выброс старых блоков + warning-нотис.
func (o *Orchestrator) compressIfNeeded(ctx context.Context, stageName, target, logDir string) {
	max := o.opts.Memory.MaxBytes
	if !fileExceeds(target, max) {
		return
	}
	base := filepath.Base(target)
	for attempt := 0; attempt < o.opts.Memory.CompressRetries; attempt++ {
		limit := 0
		if attempt == o.opts.Memory.CompressRetries-1 {
			limit = lineLimitForBytes(max) // экстремальный проход на последней попытке
		}
		if err := o.runMemoryAgent(ctx, memoryAgentSpec{
			kind:       "compressor",
			stageName:  stageName,
			targetFile: target,
			lineLimit:  limit,
			logFile:    filepath.Join(logDir, "compressor-"+base+".log"),
		}); err != nil {
			o.reflectFailed(stageName, "compressor", err)
			break
		}
		if !fileExceeds(target, max) {
			return
		}
	}
	if fileExceeds(target, max) {
		_ = fifoDropOldestBlocks(target, max)
		o.reflectNotice(stageName, "memory file "+base+" still over limit after compression; dropped oldest blocks")
	}
}

// reflectFailed / reflectNotice — best-effort UI-нотисы (live + notices.jsonl),
// НЕ FSM. Зеркалит publishHookNotice/AppendNotice.
func (o *Orchestrator) reflectFailed(stageName, step string, err error) {
	o.reflectNotice(stageName, "reflection "+step+" failed: "+err.Error())
}

func (o *Orchestrator) reflectNotice(stageName, msg string) {
	data := map[string]string{"stage": stageName, "message": msg}
	o.ui.Publish(bus.Event{Type: bus.EventReflectFailed, Data: data})
	stagefiles.AppendNotice(o.opts.RunDir, "", string(bus.EventReflectFailed), data)
}
```

> If `o.graph.Stage(id)` returns a value type (not pointer), adapt the nil-check accordingly (mirror how `maybeRunAfterHook` accesses `o.graph.Stage`). If `o.ui`/`o.concurrency` field names differ, match the existing usages in `hooks.go`.

- [ ] **Step 5: Add the `EventReflectFailed` bus event**

In `pkg/orchestrator/bus` (where `EventHookFailed`/`EventAutoAnswered` are declared), add:

```go
	EventReflectFailed EventType = "reflect_failed"
```

- [ ] **Step 6: Wire into `completeStage` and `shouldExit`**

In `pkg/orchestrator/orchestrator.go`, in `completeStage`, after `o.maybeRunAfterHook(ctx, stageID)`:

```go
	o.maybeRunReflection(ctx, stageID)
```

In `pkg/orchestrator/scheduling.go`, in `shouldExit`, extend the first guard:

```go
	if o.pendingAfterHooks.Load() > 0 || o.pendingReflections.Load() > 0 {
		return false
	}
```

(Replace the existing `if o.pendingAfterHooks.Load() > 0 {` block.)

- [ ] **Step 7: Run tests to verify they pass**

Run: `go test ./pkg/orchestrator/ -run 'TestReflectionPipeline|TestMaybeRunReflection' -race -v`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add pkg/orchestrator/reflection.go pkg/orchestrator/orchestrator.go pkg/orchestrator/scheduling.go pkg/orchestrator/bus pkg/orchestrator/reflection_test.go
git commit -m "feat(memory): конвейер reflect→updater→compressor + сериализация + учёт в shouldExit"
```

---

### Task 8: Initialize `SESSION_MEMORY.md` at run start

**Files:**
- Modify: `pkg/orchestrator/orchestrator.go` (`Run`, at the top — reset the session file)
- Test: `pkg/orchestrator/reflection_test.go` (add a case)

**Interfaces:**
- Consumes: `o.opts.MemorySessionPath`, `atomicWriteFile` (Task 5).
- Produces: `func (o *Orchestrator) initSessionMemory()` — writes a fresh `# SESSION MEMORY\n` stub when memory is enabled; overwrites any leftover.

- [ ] **Step 1: Write the failing test**

Add to `pkg/orchestrator/reflection_test.go`:

```go
func TestInitSessionMemory_ResetsEachRun(t *testing.T) {
	o := newTestOrchestrator(t)
	runDir := t.TempDir()
	sess := filepath.Join(runDir, "SESSION_MEMORY.md")
	os.WriteFile(sess, []byte("STALE CONTENT FROM A PREVIOUS RUN"), 0644)
	o.opts.MemoryProjectPath = filepath.Join(t.TempDir(), "P.md")
	o.opts.MemorySessionPath = sess

	o.initSessionMemory()

	data, _ := os.ReadFile(sess)
	if strings.Contains(string(data), "STALE") {
		t.Error("session memory must be reset at run start")
	}
	if !strings.Contains(string(data), "SESSION MEMORY") {
		t.Error("expected header stub")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/orchestrator/ -run TestInitSessionMemory_ResetsEachRun -v`
Expected: FAIL (`initSessionMemory` undefined).

- [ ] **Step 3: Implement + call it**

Add to `pkg/orchestrator/reflection.go`:

```go
// initSessionMemory сбрасывает SESSION_MEMORY.md в свежий stub на старте рана
// (пер-ран скоуп: предыдущий ран не переносится). No-op, если память выключена.
func (o *Orchestrator) initSessionMemory() {
	if o.opts.MemoryProjectPath == "" || o.opts.MemorySessionPath == "" {
		return
	}
	_ = atomicWriteFile(o.opts.MemorySessionPath, []byte("# SESSION MEMORY\n"))
}
```

In `Run` (`pkg/orchestrator/orchestrator.go`), right after the `runCtx` is set and before `o.startPlanningForPending(ctx)`:

```go
	o.initSessionMemory()
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/orchestrator/ -run TestInitSessionMemory_ResetsEachRun -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/orchestrator/reflection.go pkg/orchestrator/orchestrator.go pkg/orchestrator/reflection_test.go
git commit -m "feat(memory): сброс SESSION_MEMORY.md на старте рана"
```

---

### Task 9: Flow-final reflection at `Run()` exit

**Files:**
- Modify: `pkg/orchestrator/orchestrator.go` (`Run` loop; add `finalReflectDone bool`)
- Create: helper in `pkg/orchestrator/reflection.go` (`runFinalReflectionOnce`)
- Test: `pkg/orchestrator/reflection_test.go`

**Interfaces:**
- Consumes: `o.opts.Memory.FinalReflect`, `o.opts.{MemoryProjectPath,RunDir}`, `runReflectionPipeline` (Task 7).
- Produces: `func (o *Orchestrator) runFinalReflectionOnce(ctx context.Context)` — runs the pipeline once over the whole run (reflect reads all stage logs under `RunDir`), guarded by `finalReflectDone`. Synchronous.

- [ ] **Step 1: Write the failing test**

Add to `pkg/orchestrator/reflection_test.go`:

```go
func TestFinalReflection_RunsOnceWhenEnabled(t *testing.T) {
	o, _, _, order := pipelineHarness(t, 100000, 2)
	o.opts.Memory.FinalReflect = true

	o.runFinalReflectionOnce(context.Background())
	first := len(*order)
	if first == 0 {
		t.Fatal("final reflection did not run")
	}
	// A second call must be a no-op (finalReflectDone).
	o.runFinalReflectionOnce(context.Background())
	if len(*order) != first {
		t.Error("final reflection ran twice; finalReflectDone not honored")
	}
}

func TestFinalReflection_NoOpWhenDisabled(t *testing.T) {
	o, _, _, order := pipelineHarness(t, 100000, 2)
	o.opts.Memory.FinalReflect = false
	o.runFinalReflectionOnce(context.Background())
	if len(*order) != 0 {
		t.Error("final reflection must not run when disabled")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/orchestrator/ -run TestFinalReflection -v`
Expected: FAIL (`runFinalReflectionOnce`/`finalReflectDone` undefined).

- [ ] **Step 3: Implement**

Add field `finalReflectDone bool` to the `Orchestrator` struct (near `reflectMu`).

Add to `pkg/orchestrator/reflection.go`:

```go
// runFinalReflectionOnce прогоняет конвейер памяти ОДИН раз по ВСЕЙ сессии
// флоу в конце Run(). reflect читает логи всех стадий рана (директория RunDir).
// Синхронный — Run() дожидается его перед завершением. Идемпотентен.
func (o *Orchestrator) runFinalReflectionOnce(ctx context.Context) {
	if o.finalReflectDone {
		return
	}
	if o.opts.MemoryProjectPath == "" || !o.opts.Memory.FinalReflect {
		return
	}
	o.finalReflectDone = true
	o.runReflectionPipeline(ctx, "flow-final", []string{o.opts.RunDir}, o.opts.RunDir)
}
```

In `Run` (`pkg/orchestrator/orchestrator.go`), change the exit branch:

```go
			if o.shouldExit() {
				o.runFinalReflectionOnce(ctx)
				return nil
			}
```

> `finalReflectDone` is only read/written on the single `Run` goroutine, so a plain `bool` is race-free here.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/orchestrator/ -run TestFinalReflection -race -v`
Expected: PASS.

- [ ] **Step 5: Full package test + lint**

Run: `go test ./pkg/orchestrator/ ./pkg/flow/ ./cmd/afm/ -race && golangci-lint run ./...`
Expected: PASS, 0 issues.

- [ ] **Step 6: Commit**

```bash
git add pkg/orchestrator/reflection.go pkg/orchestrator/orchestrator.go pkg/orchestrator/reflection_test.go
git commit -m "feat(memory): флоу-финальный reflect по всей сессии в конце Run"
```

---

### Task 10: Integration test + docs

**Files:**
- Create: `pkg/orchestrator/memory_integration_test.go`
- Modify: `AGENTS.md` (a new section documenting the memory feature)

**Interfaces:**
- Consumes: everything above, via the `runMemoryAgent` stub.

- [ ] **Step 1: Write the integration test**

Create `pkg/orchestrator/memory_integration_test.go` — drive one `reflect:true` stage to completion with a stubbed `runMemoryAgent` and assert the pipeline touched both memory files and the pointer reached a subsequent stage's built prompt. Reuse the package's existing integration harness (search for `TestIntegration_` in this package for the established scaffolding — orchestrator + real `state.Store` + stubbed runner). Concretely assert:

```go
// After a reflect:true stage completes:
//   - reflect_dataset.yaml exists in the stage dir
//   - PROJECT_MEMORY.md and SESSION_MEMORY.md exist and are non-empty
//   - a later stage's prompts.Build output contains the memory pointer
//     (both absolute paths) — assert via prompts.Build(Inputs{GlobalPrompt: o.opts.GlobalPrompt})
```

Model the stub effects on `pipelineHarness` (Task 7). Keep it in the same style as `TestIntegration_PreNoteReachesFreshPrompt`.

- [ ] **Step 2: Run the integration test**

Run: `go test ./pkg/orchestrator/ -run TestIntegration_Memory -race -v`
Expected: PASS.

- [ ] **Step 3: Document in AGENTS.md**

Add a section `## Agent memory (reflect → updater → compressor)` to `AGENTS.md` summarizing: the `memory:` flow block + `reflect:` stage opt-in; the two-file scope (PROJECT cross-run in repo, SESSION per-run in run dir); the code-driven pipeline and its background/best-effort nature; the pointer injection; serialization + `pendingReflections`; `final_reflect`; and the three overridable prompts. Point to the spec and this plan. Match the density and tone of the neighboring sections.

- [ ] **Step 4: Commit**

```bash
git add pkg/orchestrator/memory_integration_test.go AGENTS.md
git commit -m "test(memory): интеграционный тест конвейера + docs в AGENTS.md"
```

---

## Self-Review

**1. Spec coverage:**
- Two-file scope (PROJECT cross-run / SESSION per-run) → Tasks 3 (paths), 8 (session reset). ✓
- Config surface + validation → Task 1. ✓
- Three embedded prompts + `prompts_dir` override → Task 2 (reuses `assets.ReadPrompt`). ✓
- Pointer injection into every stage → Task 3. ✓
- Pipeline reflect→updater→size-check→compressor (file-based) → Tasks 4,5,6,7. ✓
- Dynamic line-limit "extreme" pass → Task 4 (`lineLimit`) + Task 7 (last attempt). ✓
- FIFO terminal fallback → Task 5 + Task 7. ✓
- Serialization (`reflectMu`) + `pendingReflections` + `shouldExit` → Task 7. ✓
- Best-effort / never touches FSM / `reflect_failed` notice → Task 7. ✓
- Flow-final reflect → Task 9. ✓
- Atomic writes → Task 5 (`atomicWriteFile`, used by fallback + session init). ✓
- Per-stage opt-in default false, script stage skipped → Tasks 1 (validation allows), 7 (runtime skip). ✓

**2. Placeholder scan:** No "TBD"/"handle edge cases"/"similar to Task N". Every code step has real code. The only soft references are "reuse the package's existing test-orchestrator/integration harness" — deliberate, because the exact helper name must be read from the package; the expected symbol shape is given.

**3. Type consistency:**
- `memoryAgentSpec` fields identical across Tasks 4, 6, 7. ✓
- `runMemoryAgent func(ctx, spec memoryAgentSpec) error` identical in Tasks 6, 7, tests. ✓
- `buildMemoryPrompt(Prompts, memoryAgentSpec) string` — Tasks 4, 6. ✓
- `fileExceeds`/`fifoDropOldestBlocks`/`lineLimitForBytes`/`atomicWriteFile` — defined Task 5, used Tasks 7, 8. ✓
- `flow.MemoryConfig`/`Flow.Memory`/`Stage.Reflect`/`MemoryEnabled` — defined Task 1, used Tasks 3, 7, 9. ✓
- `Options.{Memory,MemoryProjectPath,MemorySessionPath}` — defined Task 3, used Tasks 7, 8, 9. ✓
- `EventReflectFailed` — defined Task 7, used Task 7. ✓
