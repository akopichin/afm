# flowManager Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a Go binary + Claude skills that orchestrate multi-stage AI flows from a YAML file, with parallel execution, file-based state/locking, and resumable runs.

> **Note:** Replace `github.com/you/flowmanager` with your actual module path in `go.mod` and all imports before running.

**Architecture:** Go binary (`flowmanager`) manages all state, locking, and process orchestration. It spawns `claude` (or any configured CLI) as subprocesses for planning and implementation agents. Claude skills are thin launchers that invoke the binary and poll `state.json` for approval gates.

**Tech Stack:** Go 1.23, cobra (CLI), gopkg.in/yaml.v3 (YAML), syscall flock (file locking), go:embed (prompts), goreleaser (releases)

---

## File Map

```
flowManager/
  cmd/flowmanager/
    main.go                         ← entry point, cobra root
  pkg/
    config/
      config.go                     ← Config struct, Load(), merge global+project
      config_test.go
    flow/
      flow.go                       ← Flow/Stage structs, ParseFile(), Validate()
      flow_test.go
    state/
      state.go                      ← RunState/StageStatus, Load/Save atomic, SetStatus
      state_test.go
    progress/
      progress.go                   ← Logger: timestamped append-only file + stdout
      flock_unix.go                 ← //go:build !windows — syscall.Flock
      flock_windows.go              ← //go:build windows — LockFileEx
      progress_test.go
    executor/
      executor.go                   ← Executor.RunPlanning(), RunAgent(), idle timeout
      linereader.go                 ← bufio line reader for stream-json
      executor_test.go
    orchestrator/
      graph.go                      ← DAG: Build, TopologicalSort, HasCycle, ReadyStages
      orchestrator.go               ← Orchestrator: PlanningPhase, ImplementationPhase, SummaryPhase
      graph_test.go
      orchestrator_test.go
  assets/
    assets.go                       ← //go:embed directives
    prompts/
      planning.md
      implementation.md
      review.md
      summary.md
    claude/
      skills/
        flowmanager/SKILL.md
        flowmanager-check/SKILL.md
        flowmanager-init/SKILL.md
  go.mod
  go.sum
  Makefile
  .goreleaser.yml
```

---

## Task 1: Project scaffold

**Files:**
- Create: `go.mod`
- Create: `cmd/flowmanager/main.go`

- [ ] **Step 1: Create go.mod**

```
module github.com/you/flowmanager

go 1.23
```

- [ ] **Step 2: Create main.go with cobra root and command stubs**

```go
// cmd/flowmanager/main.go
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "flowmanager",
		Short: "Orchestrate multi-stage AI flows",
	}
	root.AddCommand(
		newRunCmd(),
		newCheckCmd(),
		newApproveCmd(),
		newInitCmd(),
		newListCmd(),
		newResumeCmd(),
	)
	return root
}

func newRunCmd() *cobra.Command {
	return &cobra.Command{Use: "run [flow.yaml]", Short: "Run a flow",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("run: not implemented")
			return nil
		}}
}

func newCheckCmd() *cobra.Command {
	return &cobra.Command{Use: "check", Short: "Show flow status",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("check: not implemented")
			return nil
		}}
}

func newApproveCmd() *cobra.Command {
	return &cobra.Command{Use: "approve [stage-id]", Short: "Approve a stage plan",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("approve: not implemented")
			return nil
		}}
}

func newInitCmd() *cobra.Command {
	return &cobra.Command{Use: "init", Short: "Create a flow.yaml interactively",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("init: not implemented")
			return nil
		}}
}

func newListCmd() *cobra.Command {
	return &cobra.Command{Use: "list", Short: "List flow files",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("list: not implemented")
			return nil
		}}
}

func newResumeCmd() *cobra.Command {
	return &cobra.Command{Use: "resume [run-id]", Short: "Resume an interrupted run",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("resume: not implemented")
			return nil
		}}
}
```

- [ ] **Step 3: Add cobra dependency and tidy**

```bash
cd /Users/alexander.kopichin/work/flowManager
go get github.com/spf13/cobra@v1.8.0
go get gopkg.in/yaml.v3
go mod tidy
```

- [ ] **Step 4: Verify build**

```bash
go build ./cmd/flowmanager
./flowmanager --help
```

Expected: help text with all 6 subcommands listed.

- [ ] **Step 5: Commit**

```bash
git init
git add .
git commit -m "инициализация проекта: scaffold CLI с cobra"
```

---

## Task 2: pkg/flow — YAML parsing and validation

**Files:**
- Create: `pkg/flow/flow.go`
- Create: `pkg/flow/flow_test.go`

- [ ] **Step 1: Write failing tests**

```go
// pkg/flow/flow_test.go
package flow_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/you/flowmanager/pkg/flow"
)

const validYAML = `
name: test-flow
description: "test"
stages:
  - id: backend
    name: "Backend"
    description: "do backend stuff"
    agents: [planning, implementation, review]
  - id: frontend
    name: "Frontend"
    description: "do frontend stuff"
    depends_on: [backend]
    agents: [planning, implementation]
`

const cycleYAML = `
name: cycle-flow
description: "has cycle"
stages:
  - id: a
    name: "A"
    description: "a"
    depends_on: [b]
    agents: [planning]
  - id: b
    name: "B"
    description: "b"
    depends_on: [a]
    agents: [planning]
`

const noPlanningYAML = `
name: bad-flow
description: "bad"
stages:
  - id: stage1
    name: "S1"
    description: "no planning and no plan"
    agents: [implementation]
`

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(content)
	f.Close()
	return f.Name()
}

func TestParseValidFlow(t *testing.T) {
	f, err := flow.ParseFile(writeTemp(t, validYAML))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.Name != "test-flow" {
		t.Errorf("name: got %q want %q", f.Name, "test-flow")
	}
	if len(f.Stages) != 2 {
		t.Errorf("stages: got %d want 2", len(f.Stages))
	}
	if f.Stages[1].DependsOn[0] != "backend" {
		t.Errorf("depends_on: got %v", f.Stages[1].DependsOn)
	}
}

func TestValidateCycle(t *testing.T) {
	path := writeTemp(t, cycleYAML)
	_, err := flow.ParseFile(path)
	if err == nil {
		t.Fatal("expected cycle error, got nil")
	}
}

func TestValidateMissingPlan(t *testing.T) {
	path := writeTemp(t, noPlanningYAML)
	_, err := flow.ParseFile(path)
	if err == nil {
		t.Fatal("expected validation error for missing planning+plan")
	}
}

func TestStageWithReadyPlan(t *testing.T) {
	yaml := `
name: f
description: d
stages:
  - id: s1
    name: S1
    description: d
    agents: [implementation]
    plan: docs/plans/existing.md
`
	f, err := flow.ParseFile(writeTemp(t, yaml))
	if err != nil {
		t.Fatalf("unexpected error for stage with plan path: %v", err)
	}
	if f.Stages[0].Plan != "docs/plans/existing.md" {
		t.Errorf("plan path not set")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./pkg/flow/... 2>&1 | head -5
```

Expected: compilation error (package does not exist).

- [ ] **Step 3: Implement pkg/flow/flow.go**

```go
// pkg/flow/flow.go
package flow

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// AgentType defines which built-in agents a stage uses.
type AgentType string

const (
	AgentPlanning       AgentType = "planning"
	AgentImplementation AgentType = "implementation"
	AgentReview         AgentType = "review"
)

// Stage represents a single stage in a flow.
type Stage struct {
	ID          string      `yaml:"id"`
	Name        string      `yaml:"name"`
	Description string      `yaml:"description"`
	Agents      []AgentType `yaml:"agents"`
	Skills      []string    `yaml:"skills"`
	DependsOn   []string    `yaml:"depends_on"`
	// Plan is an optional path to an existing plan file.
	// If set, the planning agent is skipped.
	Plan string `yaml:"plan"`
}

// HasAgent reports whether the stage uses a specific agent type.
func (s *Stage) HasAgent(a AgentType) bool {
	for _, ag := range s.Agents {
		if ag == a {
			return true
		}
	}
	return false
}

// NeedsPlanning reports whether a planning agent will run for this stage.
func (s *Stage) NeedsPlanning() bool {
	return s.Plan == "" && s.HasAgent(AgentPlanning)
}

// Flow is the top-level structure parsed from a flow YAML file.
type Flow struct {
	Name        string  `yaml:"name"`
	Description string  `yaml:"description"`
	Stages      []Stage `yaml:"stages"`
}

// ParseFile reads and validates a flow YAML file.
func ParseFile(path string) (*Flow, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read flow file: %w", err)
	}
	var f Flow
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	if err := f.validate(); err != nil {
		return nil, err
	}
	return &f, nil
}

func (f *Flow) validate() error {
	ids := make(map[string]bool, len(f.Stages))
	for _, s := range f.Stages {
		if ids[s.ID] {
			return fmt.Errorf("duplicate stage id: %q", s.ID)
		}
		ids[s.ID] = true
	}

	// Check all depends_on reference valid ids
	for _, s := range f.Stages {
		for _, dep := range s.DependsOn {
			if !ids[dep] {
				return fmt.Errorf("stage %q depends_on unknown stage %q", s.ID, dep)
			}
		}
	}

	// Check each stage has either planning agent or a plan path
	for _, s := range f.Stages {
		if s.Plan == "" && !s.HasAgent(AgentPlanning) {
			return fmt.Errorf("stage %q: must have planning agent or a plan path", s.ID)
		}
	}

	// Cycle detection via DFS
	return detectCycles(f.Stages)
}

func detectCycles(stages []Stage) error {
	// Build adjacency: id → deps
	deps := make(map[string][]string, len(stages))
	for _, s := range stages {
		deps[s.ID] = s.DependsOn
	}

	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[string]int, len(stages))

	var visit func(id string) error
	visit = func(id string) error {
		if color[id] == black {
			return nil
		}
		if color[id] == gray {
			return fmt.Errorf("cycle detected involving stage %q", id)
		}
		color[id] = gray
		for _, dep := range deps[id] {
			if err := visit(dep); err != nil {
				return err
			}
		}
		color[id] = black
		return nil
	}

	for _, s := range stages {
		if err := visit(s.ID); err != nil {
			return err
		}
	}
	return nil
}
```

- [ ] **Step 4: Run tests — expect pass**

```bash
go test ./pkg/flow/... -v
```

Expected: all 4 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/flow/
git commit -m "feat(flow): парсинг и валидация flow YAML"
```

---

## Task 3: pkg/config — configuration loading

**Files:**
- Create: `pkg/config/config.go`
- Create: `pkg/config/config_test.go`

- [ ] **Step 1: Write failing tests**

```go
// pkg/config/config_test.go
package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/you/flowmanager/pkg/config"
)

func writeYAML(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDefaultConfig(t *testing.T) {
	cfg := config.Default()
	if cfg.Client.Command != "claude" {
		t.Errorf("default command: got %q want %q", cfg.Client.Command, "claude")
	}
	if cfg.Executor.IdleTimeout != 30*time.Minute {
		t.Errorf("default idle timeout: got %v", cfg.Executor.IdleTimeout)
	}
	if cfg.Executor.MaxParallel != 0 {
		t.Errorf("default max_parallel: got %d", cfg.Executor.MaxParallel)
	}
}

func TestLoadProjectOverridesGlobal(t *testing.T) {
	globalDir := t.TempDir()
	projectDir := t.TempDir()

	writeYAML(t, globalDir, "config.yaml", `
client:
  command: claude
executor:
  idle_timeout: 10m
`)
	writeYAML(t, projectDir, "config.yaml", `
client:
  command: gemini
`)

	cfg, err := config.LoadFrom(globalDir, projectDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Client.Command != "gemini" {
		t.Errorf("project should override command: got %q", cfg.Client.Command)
	}
	// global idle_timeout should be preserved where not overridden
	if cfg.Executor.IdleTimeout != 10*time.Minute {
		t.Errorf("global idle_timeout should carry over: got %v", cfg.Executor.IdleTimeout)
	}
}

func TestLoadMissingFiles(t *testing.T) {
	cfg, err := config.LoadFrom("/nonexistent", "/also/nonexistent")
	if err != nil {
		t.Fatalf("missing config files should not error: %v", err)
	}
	if cfg.Client.Command != "claude" {
		t.Errorf("should fall back to defaults: got %q", cfg.Client.Command)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./pkg/config/... 2>&1 | head -5
```

Expected: compilation error.

- [ ] **Step 3: Implement pkg/config/config.go**

```go
// pkg/config/config.go
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// ClientConfig configures the AI client command.
type ClientConfig struct {
	Command   string   `yaml:"command"`
	ExtraArgs []string `yaml:"extra_args"`
}

// ExecutorConfig controls agent execution parameters.
type ExecutorConfig struct {
	IdleTimeout time.Duration `yaml:"idle_timeout"`
	MaxParallel int           `yaml:"max_parallel"`
}

// Config is the merged configuration for flowmanager.
type Config struct {
	Client     ClientConfig   `yaml:"client"`
	Executor   ExecutorConfig `yaml:"executor"`
	PromptsDir string         `yaml:"prompts_dir"`
}

// Default returns the built-in default configuration.
func Default() Config {
	return Config{
		Client:   ClientConfig{Command: "claude"},
		Executor: ExecutorConfig{IdleTimeout: 30 * time.Minute, MaxParallel: 0},
	}
}

// Load loads configuration from the standard locations:
// ~/.flowManager/config.yaml (global) and .flowManager/config.yaml (project).
func Load() (Config, error) {
	home, _ := os.UserHomeDir()
	return LoadFrom(
		filepath.Join(home, ".flowManager"),
		".flowManager",
	)
}

// LoadFrom loads and merges config from explicit global and project dirs.
// Missing files are silently ignored; defaults apply.
func LoadFrom(globalDir, projectDir string) (Config, error) {
	cfg := Default()
	if err := mergeFile(&cfg, filepath.Join(globalDir, "config.yaml")); err != nil {
		return cfg, err
	}
	if err := mergeFile(&cfg, filepath.Join(projectDir, "config.yaml")); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func mergeFile(dst *Config, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read config %s: %w", path, err)
	}
	var overlay Config
	if err := yaml.Unmarshal(data, &overlay); err != nil {
		return fmt.Errorf("parse config %s: %w", path, err)
	}
	// Shallow merge: non-zero overlay values win
	if overlay.Client.Command != "" {
		dst.Client.Command = overlay.Client.Command
	}
	if overlay.Client.ExtraArgs != nil {
		dst.Client.ExtraArgs = overlay.Client.ExtraArgs
	}
	if overlay.Executor.IdleTimeout != 0 {
		dst.Executor.IdleTimeout = overlay.Executor.IdleTimeout
	}
	if overlay.Executor.MaxParallel != 0 {
		dst.Executor.MaxParallel = overlay.Executor.MaxParallel
	}
	if overlay.PromptsDir != "" {
		dst.PromptsDir = overlay.PromptsDir
	}
	return nil
}
```

- [ ] **Step 4: Run tests — expect pass**

```bash
go test ./pkg/config/... -v
```

Expected: all 3 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/config/
git commit -m "feat(config): двухуровневая конфигурация"
```

---

## Task 4: pkg/state — state machine

**Files:**
- Create: `pkg/state/state.go`
- Create: `pkg/state/state_test.go`

- [ ] **Step 1: Write failing tests**

```go
// pkg/state/state_test.go
package state_test

import (
	"path/filepath"
	"testing"

	"github.com/you/flowmanager/pkg/state"
)

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	s := state.NewRunState([]string{"backend", "frontend"})
	s.SetStageStatus("backend", state.StatusPlanning)

	if err := s.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := state.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Stages["backend"].Status != state.StatusPlanning {
		t.Errorf("status not persisted: got %v", loaded.Stages["backend"].Status)
	}
	if loaded.Stages["frontend"].Status != state.StatusPending {
		t.Errorf("other stage should be pending: got %v", loaded.Stages["frontend"].Status)
	}
}

func TestAtomicSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	s := state.NewRunState([]string{"a"})
	// Save multiple times — should not corrupt
	for i := 0; i < 10; i++ {
		if err := s.Save(path); err != nil {
			t.Fatalf("Save iteration %d: %v", i, err)
		}
	}
	if _, err := state.Load(path); err != nil {
		t.Fatalf("final Load: %v", err)
	}
}

func TestAllDone(t *testing.T) {
	s := state.NewRunState([]string{"a", "b"})
	if s.AllDone() {
		t.Error("should not be done initially")
	}
	s.SetStageStatus("a", state.StatusDone)
	s.SetStageStatus("b", state.StatusDone)
	if !s.AllDone() {
		t.Error("should be done when all stages done")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./pkg/state/... 2>&1 | head -5
```

Expected: compilation error.

- [ ] **Step 3: Implement pkg/state/state.go**

```go
// pkg/state/state.go
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// StageStatus represents the lifecycle state of a single stage.
type StageStatus string

const (
	StatusPending          StageStatus = "pending"
	StatusPlanning         StageStatus = "planning"
	StatusAwaitingApproval StageStatus = "awaiting_approval"
	StatusReady            StageStatus = "ready"
	StatusRunning          StageStatus = "running"
	StatusDone             StageStatus = "done"
	StatusFailed           StageStatus = "failed"
)

// StageState holds persistent state for a single stage.
type StageState struct {
	Status    StageStatus `json:"status"`
	UpdatedAt time.Time   `json:"updated_at"`
}

// RunState is the top-level state persisted in state.json.
type RunState struct {
	FlowName  string                `json:"flow_name"`
	StartedAt time.Time             `json:"started_at"`
	Stages    map[string]StageState `json:"stages"`
}

// NewRunState creates an initial RunState with all stages pending.
func NewRunState(stageIDs []string) *RunState {
	rs := &RunState{
		StartedAt: time.Now(),
		Stages:    make(map[string]StageState, len(stageIDs)),
	}
	for _, id := range stageIDs {
		rs.Stages[id] = StageState{Status: StatusPending, UpdatedAt: time.Now()}
	}
	return rs
}

// SetStageStatus updates a stage status and its timestamp.
func (rs *RunState) SetStageStatus(stageID string, status StageStatus) {
	rs.Stages[stageID] = StageState{Status: status, UpdatedAt: time.Now()}
}

// AllDone returns true when every stage has StatusDone.
func (rs *RunState) AllDone() bool {
	for _, s := range rs.Stages {
		if s.Status != StatusDone {
			return false
		}
	}
	return true
}

// Save writes state atomically (temp file + rename).
func (rs *RunState) Save(path string) error {
	data, err := json.MarshalIndent(rs, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("write temp state: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename state: %w", err)
	}
	return nil
}

// Load reads state from a JSON file.
func Load(path string) (*RunState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read state: %w", err)
	}
	var rs RunState
	if err := json.Unmarshal(data, &rs); err != nil {
		return nil, fmt.Errorf("unmarshal state: %w", err)
	}
	return &rs, nil
}

// FindLatestRunDir finds the most recent run directory for a given flow name
// under .flowManager/runs/.
func FindLatestRunDir(flowName string) (string, error) {
	base := filepath.Join(".flowManager", "runs")
	entries, err := os.ReadDir(base)
	if err != nil {
		return "", fmt.Errorf("read runs dir: %w", err)
	}
	// Entries are alphabetically sorted; since we use {name}-{timestamp} the
	// last matching entry is the most recent.
	var latest string
	prefix := flowName + "-"
	for _, e := range entries {
		if e.IsDir() && len(e.Name()) > len(prefix) && e.Name()[:len(prefix)] == prefix {
			latest = filepath.Join(base, e.Name())
		}
	}
	if latest == "" {
		return "", fmt.Errorf("no run found for flow %q", flowName)
	}
	return latest, nil
}
```

- [ ] **Step 4: Run tests — expect pass**

```bash
go test ./pkg/state/... -v
```

Expected: all 3 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/state/
git commit -m "feat(state): стейт-машина с атомарным JSON"
```

---

## Task 5: pkg/progress — logging and file locking

**Files:**
- Create: `pkg/progress/progress.go`
- Create: `pkg/progress/flock_unix.go`
- Create: `pkg/progress/flock_windows.go`
- Create: `pkg/progress/progress_test.go`

- [ ] **Step 1: Write failing tests**

```go
// pkg/progress/progress_test.go
package progress_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/you/flowmanager/pkg/progress"
)

func TestLoggerWritesTimestamped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	lg, err := progress.NewLogger(path)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	defer lg.Close()

	lg.Log("hello world")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "hello world") {
		t.Errorf("log content missing message: %q", content)
	}
	// Should have a timestamp prefix like "26-04-16"
	if !strings.Contains(content, "-") {
		t.Errorf("log content missing timestamp: %q", content)
	}
}

func TestLoggerRestartsWithSeparator(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	lg, _ := progress.NewLogger(path)
	lg.Log("first run")
	lg.Close()

	lg2, err := progress.NewLogger(path)
	if err != nil {
		t.Fatalf("second NewLogger: %v", err)
	}
	defer lg2.Close()

	data, _ := os.ReadFile(path)
	content := string(data)
	if !strings.Contains(content, "restarted") {
		t.Errorf("should have restart separator: %q", content)
	}
	if !strings.Contains(content, "first run") {
		t.Errorf("original content should be preserved: %q", content)
	}
}

func TestFileLock(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "test.lock")

	lock1, err := progress.NewLock(lockPath)
	if err != nil {
		t.Fatalf("NewLock: %v", err)
	}
	if err := lock1.Lock(); err != nil {
		t.Fatalf("Lock: %v", err)
	}

	// TryLock from a second instance should fail
	lock2, _ := progress.NewLock(lockPath)
	if lock2.TryLock() == nil {
		t.Error("second TryLock should fail while first holds lock")
	}

	lock1.Unlock()

	// Now it should succeed
	if err := lock2.TryLock(); err != nil {
		t.Errorf("TryLock after release: %v", err)
	}
	lock2.Unlock()
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./pkg/progress/... 2>&1 | head -5
```

Expected: compilation error.

- [ ] **Step 3: Implement progress.go**

```go
// pkg/progress/progress.go
package progress

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Logger writes timestamped messages to a file (append-only) and stdout.
type Logger struct {
	f *os.File
}

// NewLogger opens (or creates) a log file. If the file exists without a
// completion footer, a restart separator is appended.
func NewLogger(path string) (*Logger, error) {
	// Check if file exists and has content but no completion marker
	existing, _ := os.ReadFile(path)
	needsSeparator := len(existing) > 0

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	lg := &Logger{f: f}

	if needsSeparator {
		sep := fmt.Sprintf("\n--- restarted at %s ---\n", time.Now().Format("06-01-02 15:04:05"))
		lg.write(sep)
	}
	return lg, nil
}

// Log writes a timestamped line to the log file and stdout.
func (l *Logger) Log(msg string) {
	line := fmt.Sprintf("%s  %s\n", time.Now().Format("06-01-02 15:04:05"), msg)
	l.write(line)
	fmt.Print(line)
}

func (l *Logger) write(s string) {
	l.f.WriteString(s) //nolint:errcheck
}

// Close closes the underlying file.
func (l *Logger) Close() error {
	return l.f.Close()
}

// AppendRaw appends raw bytes to the log (used for streaming agent output).
func (l *Logger) AppendRaw(data []byte) {
	l.f.Write(data) //nolint:errcheck
}

// Lock is a file-based exclusive lock.
type Lock struct {
	path string
	f    *os.File
}

// NewLock creates a Lock handle for the given path (does not acquire).
func NewLock(path string) (*Lock, error) {
	return &Lock{path: path}, nil
}

// IsLocked returns true if another process holds the lock.
func IsLocked(path string) bool {
	l := &Lock{path: path}
	err := l.TryLock()
	if err != nil {
		return true
	}
	l.Unlock()
	return false
}

// progressFile returns true if the log file contains no completion footer.
// Used to distinguish interrupted vs. complete runs.
func hasCompletion(content []byte) bool {
	return strings.Contains(string(content), "--- completed ---")
}
```

- [ ] **Step 4: Implement flock_unix.go**

```go
// pkg/progress/flock_unix.go
//go:build !windows

package progress

import (
	"fmt"
	"os"
	"syscall"
)

// Lock acquires an exclusive blocking flock.
func (l *Lock) Lock() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return fmt.Errorf("flock: %w", err)
	}
	l.f = f
	return nil
}

// TryLock attempts a non-blocking exclusive flock.
func (l *Lock) TryLock() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return fmt.Errorf("lock busy: %w", err)
	}
	l.f = f
	return nil
}

// Unlock releases the flock and closes the file.
func (l *Lock) Unlock() {
	if l.f != nil {
		syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN) //nolint:errcheck
		l.f.Close()
		l.f = nil
	}
}
```

- [ ] **Step 5: Implement flock_windows.go**

```go
// pkg/progress/flock_windows.go
//go:build windows

package progress

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func (l *Lock) Lock() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return fmt.Errorf("open lock file: %w", err)
	}
	ol := new(windows.Overlapped)
	if err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, ol); err != nil {
		f.Close()
		return fmt.Errorf("LockFileEx: %w", err)
	}
	l.f = f
	return nil
}

func (l *Lock) TryLock() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return fmt.Errorf("open lock file: %w", err)
	}
	ol := new(windows.Overlapped)
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK | windows.LOCKFILE_FAIL_IMMEDIATELY)
	if err := windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, ol); err != nil {
		f.Close()
		return fmt.Errorf("lock busy: %w", err)
	}
	l.f = f
	return nil
}

func (l *Lock) Unlock() {
	if l.f != nil {
		ol := new(windows.Overlapped)
		windows.UnlockFileEx(windows.Handle(l.f.Fd()), 0, 1, 0, ol) //nolint:errcheck
		l.f.Close()
		l.f = nil
	}
}
```

- [ ] **Step 6: Run tests — expect pass**

```bash
go test ./pkg/progress/... -v
```

Expected: all 3 tests PASS.

- [ ] **Step 7: Commit**

```bash
git add pkg/progress/
git commit -m "feat(progress): логгер с временными метками и файловые локи"
```

---

## Task 6: pkg/executor — spawning Claude subprocess

**Files:**
- Create: `pkg/executor/executor.go`
- Create: `pkg/executor/linereader.go`
- Create: `pkg/executor/executor_test.go`

- [ ] **Step 1: Write failing tests**

```go
// pkg/executor/executor_test.go
package executor_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/you/flowmanager/pkg/executor"
)

func TestRunPlanningCapturesOutput(t *testing.T) {
	// Use echo as a mock command — it prints the input and exits.
	// We fake stream-json by using a simple echo command that outputs a result line.
	dir := t.TempDir()
	outFile := filepath.Join(dir, "plan.md")
	logFile := filepath.Join(dir, "planning.log")

	ex := executor.New(executor.Config{
		Command: "bash",
		ExtraArgs: []string{"-c",
			`echo '{"type":"assistant","message":{"content":[{"type":"text","text":"# Plan\n\nstep 1"}]}}'
echo '{"type":"result","subtype":"success"}'`},
		IdleTimeout: 5 * time.Second,
	})

	err := ex.RunPlanning(context.Background(), "generate a plan", outFile, logFile)
	if err != nil {
		t.Fatalf("RunPlanning: %v", err)
	}

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read outFile: %v", err)
	}
	if !strings.Contains(string(data), "Plan") {
		t.Errorf("plan output missing: %q", string(data))
	}
}

func TestRunAgentLogsOutput(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "impl.log")

	ex := executor.New(executor.Config{
		Command: "bash",
		ExtraArgs: []string{"-c",
			`echo '{"type":"result","subtype":"success"}'`},
		IdleTimeout: 5 * time.Second,
	})

	err := ex.RunAgent(context.Background(), "implement the plan", logFile)
	if err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	data, _ := os.ReadFile(logFile)
	if len(data) == 0 {
		t.Error("log file should not be empty")
	}
}

func TestIdleTimeout(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "impl.log")

	ex := executor.New(executor.Config{
		Command:     "bash",
		ExtraArgs:   []string{"-c", "sleep 10"},
		IdleTimeout: 100 * time.Millisecond,
	})

	ctx := context.Background()
	err := ex.RunAgent(ctx, "do work", logFile)
	if err == nil {
		t.Error("expected timeout error, got nil")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./pkg/executor/... 2>&1 | head -5
```

Expected: compilation error.

- [ ] **Step 3: Implement linereader.go**

```go
// pkg/executor/linereader.go
package executor

import (
	"bufio"
	"io"
)

// lineReader reads lines from r, calling fn for each line.
// Returns when r is exhausted or fn returns false.
func lineReader(r io.Reader, fn func(line string) bool) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB per line
	for scanner.Scan() {
		if !fn(scanner.Text()) {
			return nil
		}
	}
	return scanner.Err()
}
```

- [ ] **Step 4: Implement executor.go**

```go
// pkg/executor/executor.go
package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/you/flowmanager/pkg/progress"
)

// Config configures the executor.
type Config struct {
	Command     string
	ExtraArgs   []string
	IdleTimeout time.Duration
}

// Executor spawns AI client subprocesses.
type Executor struct {
	cfg Config
}

// New creates an Executor.
func New(cfg Config) *Executor {
	if cfg.Command == "" {
		cfg.Command = "claude"
	}
	if cfg.IdleTimeout == 0 {
		cfg.IdleTimeout = 30 * time.Minute
	}
	return &Executor{cfg: cfg}
}

// streamEvent is a minimal representation of a claude stream-json event.
type streamEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Message *struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"`
}

// RunPlanning runs the AI client with prompt via stdin, collects text output
// into outFile, and appends raw lines to logFile.
func (e *Executor) RunPlanning(ctx context.Context, prompt, outFile, logFile string) error {
	lg, err := progress.NewLogger(logFile)
	if err != nil {
		return err
	}
	defer lg.Close()

	var textBuf strings.Builder
	err = e.run(ctx, prompt, func(line string) {
		lg.AppendRaw([]byte(line + "\n"))
		var ev streamEvent
		if json.Unmarshal([]byte(line), &ev) != nil {
			return
		}
		if ev.Type == "assistant" && ev.Message != nil {
			for _, c := range ev.Message.Content {
				if c.Type == "text" {
					textBuf.WriteString(c.Text)
				}
			}
		}
	})
	if err != nil {
		return err
	}

	return os.WriteFile(outFile, []byte(textBuf.String()), 0644)
}

// RunAgent runs the AI client with prompt via stdin, logging all output.
func (e *Executor) RunAgent(ctx context.Context, prompt, logFile string) error {
	lg, err := progress.NewLogger(logFile)
	if err != nil {
		return err
	}
	defer lg.Close()

	return e.run(ctx, prompt, func(line string) {
		lg.AppendRaw([]byte(line + "\n"))
	})
}

// run spawns the AI client subprocess, feeds prompt via stdin, and calls
// lineCallback for each stdout line. Respects idle timeout.
func (e *Executor) run(ctx context.Context, prompt string, lineCallback func(string)) error {
	args := append([]string{"--print", "--output-format", "stream-json",
		"--dangerously-skip-permissions"}, e.cfg.ExtraArgs...)

	cmd := exec.CommandContext(ctx, e.cfg.Command, args...)
	cmd.Stdin = strings.NewReader(prompt)

	// Strip CLAUDECODE to allow nested sessions
	env := os.Environ()
	filtered := env[:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, "CLAUDECODE=") && !strings.HasPrefix(kv, "ANTHROPIC_API_KEY=") {
			filtered = append(filtered, kv)
		}
	}
	cmd.Env = filtered

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", e.cfg.Command, err)
	}

	done := make(chan error, 1)
	go func() {
		done <- lineReader(stdout, func(line string) bool {
			lineCallback(line)
			return true
		})
	}()

	idleTimer := time.NewTimer(e.cfg.IdleTimeout)
	defer idleTimer.Stop()

	select {
	case err := <-done:
		_ = cmd.Wait()
		return err
	case <-idleTimer.C:
		cmd.Process.Kill() //nolint:errcheck
		return fmt.Errorf("idle timeout after %v", e.cfg.IdleTimeout)
	case <-ctx.Done():
		cmd.Process.Kill() //nolint:errcheck
		return ctx.Err()
	}
}
```

- [ ] **Step 5: Run tests — expect pass**

```bash
go test ./pkg/executor/... -v
```

Expected: all 3 tests PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/executor/
git commit -m "feat(executor): спавн AI-клиента с парсингом stream-json"
```

---

## Task 7: pkg/orchestrator/graph.go — dependency graph

**Files:**
- Create: `pkg/orchestrator/graph.go`
- Create: `pkg/orchestrator/graph_test.go`

- [ ] **Step 1: Write failing tests**

```go
// pkg/orchestrator/graph_test.go
package orchestrator_test

import (
	"testing"

	"github.com/you/flowmanager/pkg/flow"
	"github.com/you/flowmanager/pkg/orchestrator"
	"github.com/you/flowmanager/pkg/state"
)

func makeStages(specs []struct{ id string; deps []string }) []flow.Stage {
	stages := make([]flow.Stage, len(specs))
	for i, s := range specs {
		stages[i] = flow.Stage{ID: s.id, DependsOn: s.deps, Agents: []flow.AgentType{flow.AgentPlanning}}
	}
	return stages
}

func TestReadyStagesNoDeps(t *testing.T) {
	stages := makeStages([]struct{ id string; deps []string }{
		{"a", nil}, {"b", nil}, {"c", nil},
	})
	statuses := map[string]state.StageStatus{
		"a": state.StatusReady,
		"b": state.StatusReady,
		"c": state.StatusDone,
	}
	g := orchestrator.NewGraph(stages)
	ready := g.ReadyStages(statuses)
	if len(ready) != 2 {
		t.Errorf("expected 2 ready stages, got %d: %v", len(ready), ready)
	}
}

func TestReadyStagesBlockedByDep(t *testing.T) {
	stages := makeStages([]struct{ id string; deps []string }{
		{"a", nil},
		{"b", []string{"a"}},
	})
	statuses := map[string]state.StageStatus{
		"a": state.StatusRunning,
		"b": state.StatusReady,
	}
	g := orchestrator.NewGraph(stages)
	ready := g.ReadyStages(statuses)
	if len(ready) != 0 {
		t.Errorf("b should be blocked: got %v", ready)
	}
}

func TestReadyStagesDepDone(t *testing.T) {
	stages := makeStages([]struct{ id string; deps []string }{
		{"a", nil},
		{"b", []string{"a"}},
	})
	statuses := map[string]state.StageStatus{
		"a": state.StatusDone,
		"b": state.StatusReady,
	}
	g := orchestrator.NewGraph(stages)
	ready := g.ReadyStages(statuses)
	if len(ready) != 1 || ready[0] != "b" {
		t.Errorf("b should be ready when a is done: got %v", ready)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./pkg/orchestrator/... 2>&1 | head -5
```

Expected: compilation error.

- [ ] **Step 3: Implement graph.go**

```go
// pkg/orchestrator/graph.go
package orchestrator

import (
	"github.com/you/flowmanager/pkg/flow"
	"github.com/you/flowmanager/pkg/state"
)

// Graph is a dependency graph of stages.
type Graph struct {
	stages map[string]*flow.Stage
	deps   map[string][]string // id → depends_on
}

// NewGraph builds a graph from a slice of stages.
func NewGraph(stages []flow.Stage) *Graph {
	g := &Graph{
		stages: make(map[string]*flow.Stage, len(stages)),
		deps:   make(map[string][]string, len(stages)),
	}
	for i := range stages {
		s := &stages[i]
		g.stages[s.ID] = s
		g.deps[s.ID] = s.DependsOn
	}
	return g
}

// ReadyStages returns the IDs of stages that are in StatusReady and whose
// all dependencies are in StatusDone.
func (g *Graph) ReadyStages(statuses map[string]state.StageStatus) []string {
	var ready []string
	for id, deps := range g.deps {
		if statuses[id] != state.StatusReady {
			continue
		}
		allDone := true
		for _, dep := range deps {
			if statuses[dep] != state.StatusDone {
				allDone = false
				break
			}
		}
		if allDone {
			ready = append(ready, id)
		}
	}
	return ready
}

// Stage returns the Stage for a given ID.
func (g *Graph) Stage(id string) *flow.Stage {
	return g.stages[id]
}

// AllIDs returns all stage IDs.
func (g *Graph) AllIDs() []string {
	ids := make([]string, 0, len(g.stages))
	for id := range g.stages {
		ids = append(ids, id)
	}
	return ids
}
```

- [ ] **Step 4: Run tests — expect pass**

```bash
go test ./pkg/orchestrator/... -v
```

Expected: all 3 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/orchestrator/graph.go pkg/orchestrator/graph_test.go
git commit -m "feat(orchestrator): граф зависимостей"
```

---

## Task 8: pkg/orchestrator/orchestrator.go — main loop

**Files:**
- Create: `pkg/orchestrator/orchestrator.go`
- Create: `pkg/orchestrator/orchestrator_test.go`

- [ ] **Step 1: Write failing tests**

```go
// pkg/orchestrator/orchestrator_test.go
package orchestrator_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/you/flowmanager/pkg/config"
	"github.com/you/flowmanager/pkg/flow"
	"github.com/you/flowmanager/pkg/orchestrator"
	"github.com/you/flowmanager/pkg/state"
)

func TestPlanningPhaseMarksPlanningStatus(t *testing.T) {
	runDir := t.TempDir()
	stages := []flow.Stage{
		{
			ID: "s1", Name: "Stage 1", Description: "do something",
			Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation},
		},
	}

	rs := state.NewRunState([]string{"s1"})
	statePath := filepath.Join(runDir, "state.json")
	rs.Save(statePath)

	cfg := config.Default()
	// Use echo as mock AI client that outputs minimal stream-json
	cfg.Client.Command = "bash"
	cfg.Client.ExtraArgs = []string{"-c",
		`echo '{"type":"assistant","message":{"content":[{"type":"text","text":"# Plan\n- step 1"}]}}'
echo '{"type":"result","subtype":"success"}'`}

	orch := orchestrator.New(orchestrator.Options{
		RunDir:   runDir,
		Stages:   stages,
		State:    rs,
		StateFile: statePath,
		Config:   cfg,
		Prompts:  orchestrator.DefaultPrompts(),
	})

	if err := orch.PlanningPhase(context.Background()); err != nil {
		t.Fatalf("PlanningPhase: %v", err)
	}

	// Load refreshed state
	loaded, _ := state.Load(statePath)
	if loaded.Stages["s1"].Status != state.StatusAwaitingApproval {
		t.Errorf("stage should be awaiting_approval after planning: got %v",
			loaded.Stages["s1"].Status)
	}

	// plan.md should exist
	planPath := filepath.Join(runDir, "s1", "plan.md")
	if _, err := os.Stat(planPath); err != nil {
		t.Errorf("plan.md should exist: %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./pkg/orchestrator/... -run TestPlanningPhase 2>&1 | head -10
```

Expected: compilation error.

- [ ] **Step 3: Implement orchestrator.go**

```go
// pkg/orchestrator/orchestrator.go
package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/you/flowmanager/pkg/config"
	"github.com/you/flowmanager/pkg/executor"
	"github.com/you/flowmanager/pkg/flow"
	"github.com/you/flowmanager/pkg/progress"
	"github.com/you/flowmanager/pkg/state"
)

// Prompts holds the prompt templates for each agent type.
type Prompts struct {
	Planning       string
	Implementation string
	Review         string
	Summary        string
}

// DefaultPrompts returns empty prompts (will be set from assets).
func DefaultPrompts() Prompts { return Prompts{} }

// Options configures an Orchestrator.
type Options struct {
	RunDir    string
	Stages    []flow.Stage
	State     *state.RunState
	StateFile string
	Config    config.Config
	Prompts   Prompts
}

// Orchestrator manages the full lifecycle of a flow run.
type Orchestrator struct {
	opts  Options
	graph *Graph
	exec  *executor.Executor
	mu    sync.Mutex
}

// New creates an Orchestrator.
func New(opts Options) *Orchestrator {
	return &Orchestrator{
		opts:  opts,
		graph: NewGraph(opts.Stages),
		exec: executor.New(executor.Config{
			Command:     opts.Config.Client.Command,
			ExtraArgs:   opts.Config.Client.ExtraArgs,
			IdleTimeout: opts.Config.Executor.IdleTimeout,
		}),
	}
}

// PlanningPhase runs planning agents for all stages that need a plan.
// After completion each stage is in StatusAwaitingApproval (or StatusReady if plan was pre-set).
func (o *Orchestrator) PlanningPhase(ctx context.Context) error {
	var wg sync.WaitGroup
	errs := make(chan error, len(o.opts.Stages))

	for _, s := range o.opts.Stages {
		s := s
		if !s.NeedsPlanning() {
			// Pre-existing plan: copy to run dir and mark ready
			stageDir := filepath.Join(o.opts.RunDir, s.ID)
			os.MkdirAll(stageDir, 0755) //nolint:errcheck
			dst := filepath.Join(stageDir, "plan.md")
			if err := copyFile(s.Plan, dst); err != nil {
				return fmt.Errorf("copy plan for stage %s: %w", s.ID, err)
			}
			o.setStatus(s.ID, state.StatusReady)
			continue
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := o.runPlanningAgent(ctx, s); err != nil {
				errs <- fmt.Errorf("stage %s planning: %w", s.ID, err)
			}
		}()
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func (o *Orchestrator) runPlanningAgent(ctx context.Context, s flow.Stage) error {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		return err
	}

	lock, _ := progress.NewLock(filepath.Join(stageDir, "plan.md.lock"))
	if err := lock.Lock(); err != nil {
		return fmt.Errorf("acquire lock: %w", err)
	}
	defer lock.Unlock()

	o.setStatus(s.ID, state.StatusPlanning)

	prompt := buildPlanningPrompt(o.opts.Prompts.Planning, s)
	outFile := filepath.Join(stageDir, "plan.md")
	logFile := filepath.Join(stageDir, "planning.log")

	if err := o.exec.RunPlanning(ctx, prompt, outFile, logFile); err != nil {
		o.setStatus(s.ID, state.StatusFailed)
		return err
	}

	o.setStatus(s.ID, state.StatusAwaitingApproval)
	return nil
}

// WaitForApprovals polls state.json until all awaiting_approval stages become ready.
// The skill calls `flowmanager approve` which updates the state file.
func (o *Orchestrator) WaitForApprovals(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}

		loaded, err := state.Load(o.opts.StateFile)
		if err != nil {
			continue
		}

		stillWaiting := false
		for _, st := range loaded.Stages {
			if st.Status == state.StatusAwaitingApproval {
				stillWaiting = true
				break
			}
		}
		if !stillWaiting {
			// Sync in-memory state with approved states
			o.mu.Lock()
			for id, st := range loaded.Stages {
				if st.Status == state.StatusReady {
					o.opts.State.SetStageStatus(id, state.StatusReady)
				}
			}
			o.mu.Unlock()
			return nil
		}
	}
}

// ImplementationPhase runs implementation (and optionally review) agents for
// all ready stages, respecting depends_on.
func (o *Orchestrator) ImplementationPhase(ctx context.Context) error {
	maxParallel := o.opts.Config.Executor.MaxParallel
	if maxParallel <= 0 {
		maxParallel = len(o.opts.Stages)
	}
	sem := make(chan struct{}, maxParallel)

	var wg sync.WaitGroup
	errs := make(chan error, len(o.opts.Stages))

	for {
		o.mu.Lock()
		statuses := make(map[string]state.StageStatus, len(o.opts.State.Stages))
		for id, s := range o.opts.State.Stages {
			statuses[id] = s.Status
		}
		o.mu.Unlock()

		if o.opts.State.AllDone() {
			break
		}

		ready := o.graph.ReadyStages(statuses)
		for _, id := range ready {
			id := id
			stage := o.graph.Stage(id)
			sem <- struct{}{}
			wg.Add(1)
			o.setStatus(id, state.StatusRunning)
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				if err := o.runImplementationAgent(ctx, *stage); err != nil {
					errs <- fmt.Errorf("stage %s implementation: %w", id, err)
					o.setStatus(id, state.StatusFailed)
					return
				}
				o.setStatus(id, state.StatusDone)
			}()
		}

		// If no progress possible, wait a bit
		if len(ready) == 0 {
			time.Sleep(500 * time.Millisecond)
		}
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func (o *Orchestrator) runImplementationAgent(ctx context.Context, s flow.Stage) error {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)

	lock, _ := progress.NewLock(filepath.Join(stageDir, "plan.md.lock"))
	if err := lock.Lock(); err != nil {
		return fmt.Errorf("acquire lock: %w", err)
	}
	defer lock.Unlock()

	planData, err := os.ReadFile(filepath.Join(stageDir, "plan.md"))
	if err != nil {
		return fmt.Errorf("read plan: %w", err)
	}

	prompt := buildImplementationPrompt(o.opts.Prompts.Implementation, s, string(planData))
	logFile := filepath.Join(stageDir, "implementation.log")

	if err := o.exec.RunAgent(ctx, prompt, logFile); err != nil {
		return err
	}

	// Run review agent if configured
	if s.HasAgent(flow.AgentReview) {
		reviewPrompt := buildReviewPrompt(o.opts.Prompts.Review, s)
		reviewLog := filepath.Join(stageDir, "review.log")
		if err := o.exec.RunAgent(ctx, reviewPrompt, reviewLog); err != nil {
			return fmt.Errorf("review: %w", err)
		}
	}
	return nil
}

// SummaryPhase runs the summary agent after all stages complete.
func (o *Orchestrator) SummaryPhase(ctx context.Context) error {
	prompt := buildSummaryPrompt(o.opts.Prompts.Summary, o.opts.RunDir, o.opts.Stages)
	logFile := filepath.Join(o.opts.RunDir, "summary.log")
	return o.exec.RunAgent(ctx, prompt, logFile)
}

func (o *Orchestrator) setStatus(id string, status state.StageStatus) {
	o.mu.Lock()
	o.opts.State.SetStageStatus(id, status)
	o.opts.State.Save(o.opts.StateFile) //nolint:errcheck
	o.mu.Unlock()
}

// buildPlanningPrompt assembles the prompt for a planning agent.
func buildPlanningPrompt(template string, s flow.Stage) string {
	extra := ""
	if len(s.Skills) > 0 {
		extra = "\n\nUse these skills: " + joinStrings(s.Skills)
	}
	return fmt.Sprintf("%s\n\n## Stage: %s\n\n%s%s", template, s.Name, s.Description, extra)
}

func buildImplementationPrompt(template string, s flow.Stage, plan string) string {
	extra := ""
	if len(s.Skills) > 0 {
		extra = "\n\nUse these skills: " + joinStrings(s.Skills)
	}
	return fmt.Sprintf("%s\n\n## Stage: %s\n\n## Plan\n\n%s%s", template, s.Name, plan, extra)
}

func buildReviewPrompt(template string, s flow.Stage) string {
	return fmt.Sprintf("%s\n\n## Stage: %s\n\n%s", template, s.Name, s.Description)
}

func buildSummaryPrompt(template, runDir string, stages []flow.Stage) string {
	names := make([]string, len(stages))
	for i, s := range stages {
		names[i] = s.Name
	}
	return fmt.Sprintf("%s\n\nRun directory: %s\nStages: %s", template, runDir, joinStrings(names))
}

func joinStrings(ss []string) string {
	result := ""
	for i, s := range ss {
		if i > 0 {
			result += ", "
		}
		result += s
	}
	return result
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0644)
}
```

- [ ] **Step 4: Run tests — expect pass**

```bash
go test ./pkg/orchestrator/... -v
```

Expected: all 4 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/orchestrator/orchestrator.go pkg/orchestrator/orchestrator_test.go
git commit -m "feat(orchestrator): основной цикл оркестрации"
```

---

## Task 9: assets — embedded prompts

**Files:**
- Create: `assets/assets.go`
- Create: `assets/prompts/planning.md`
- Create: `assets/prompts/implementation.md`
- Create: `assets/prompts/review.md`
- Create: `assets/prompts/summary.md`

- [ ] **Step 1: Create assets/assets.go**

```go
// assets/assets.go
package assets

import "embed"

//go:embed prompts
var FS embed.FS

// ReadPrompt returns the content of a prompt file by name (e.g. "planning.md").
// If PromptsDir is set in config, reads from disk instead.
func ReadPrompt(name, overrideDir string) (string, error) {
	if overrideDir != "" {
		import "os"
		import "path/filepath"
		data, err := os.ReadFile(filepath.Join(overrideDir, name))
		return string(data), err
	}
	data, err := FS.ReadFile("prompts/" + name)
	return string(data), err
}
```

Wait — you can't have conditional imports in Go. Fix:

```go
// assets/assets.go
package assets

import (
	"embed"
	"os"
	"path/filepath"
)

//go:embed prompts
var FS embed.FS

// ReadPrompt returns a prompt by filename. If overrideDir is non-empty,
// reads from that directory instead of the embedded files.
func ReadPrompt(name, overrideDir string) (string, error) {
	if overrideDir != "" {
		data, err := os.ReadFile(filepath.Join(overrideDir, name))
		return string(data), err
	}
	data, err := FS.ReadFile("prompts/" + name)
	return string(data), err
}
```

- [ ] **Step 2: Create assets/prompts/planning.md**

```markdown
# Planning Agent

You are a planning agent. Your task is to create a detailed implementation plan for the stage described below.

Write a plan in markdown format. The plan should contain:
- An overview of what needs to be done
- Numbered tasks with specific checkboxes
- Each task should be concrete and actionable
- Include file paths where relevant

Output ONLY the plan markdown — no preamble, no explanation.
```

- [ ] **Step 3: Create assets/prompts/implementation.md**

```markdown
# Implementation Agent

You are an implementation agent. Your task is to execute the implementation plan provided below.

Work through the plan task by task:
1. Read each task carefully
2. Implement the code changes described
3. Run tests after each task
4. Mark tasks complete as you go

Follow TDD practices: write tests first, then implementation.
Commit after each completed task.
```

- [ ] **Step 4: Create assets/prompts/review.md**

```markdown
# Review Agent

You are a code review agent. Your task is to review the changes made during the implementation stage.

Review for:
- Correctness: does the implementation match the plan?
- Code quality: is the code clean, readable, and well-structured?
- Test coverage: are there adequate tests?
- Edge cases: are error conditions handled?

Write a concise review summary. If there are issues, describe them clearly.
```

- [ ] **Step 5: Create assets/prompts/summary.md**

```markdown
# Summary Agent

You are a summary agent. Your task is to produce a final report for the completed flow run.

Read the implementation and review logs from each stage in the run directory.
Write a concise summary covering:
- What was accomplished in each stage
- Any issues or concerns from the review phase
- Overall assessment of the completed work
```

- [ ] **Step 6: Verify embed compiles**

```bash
go build ./...
```

Expected: no errors.

- [ ] **Step 7: Commit**

```bash
git add assets/
git commit -m "feat(assets): встроенные промпты через go:embed"
```

---

## Task 10: CLI commands — full implementation

**Files:**
- Modify: `cmd/flowmanager/main.go` (split into command files)
- Create: `cmd/flowmanager/run.go`
- Create: `cmd/flowmanager/check.go`
- Create: `cmd/flowmanager/approve.go`
- Create: `cmd/flowmanager/init.go`
- Create: `cmd/flowmanager/list.go`

- [ ] **Step 1: Create cmd/flowmanager/run.go**

```go
// cmd/flowmanager/run.go
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/you/flowmanager/assets"
	"github.com/you/flowmanager/pkg/config"
	"github.com/you/flowmanager/pkg/flow"
	"github.com/you/flowmanager/pkg/orchestrator"
	"github.com/you/flowmanager/pkg/state"
)

func newRunCmd() *cobra.Command {
	var maxParallel int
	var idleTimeout time.Duration

	cmd := &cobra.Command{
		Use:   "run [flow.yaml]",
		Short: "Run a flow (or resume the latest run)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if maxParallel > 0 {
				cfg.Executor.MaxParallel = maxParallel
			}
			if idleTimeout > 0 {
				cfg.Executor.IdleTimeout = idleTimeout
			}

			// Resolve flow file
			flowPath, err := resolveFlowPath(args)
			if err != nil {
				return err
			}

			f, err := flow.ParseFile(flowPath)
			if err != nil {
				return fmt.Errorf("parse flow: %w", err)
			}

			// Load prompts
			prompts, err := loadPrompts(cfg.PromptsDir)
			if err != nil {
				return err
			}

			// Create or resume run directory
			runDir, rs, stateFile, err := resolveRun(f)
			if err != nil {
				return err
			}

			fmt.Printf("flowmanager: running %q\n", f.Name)
			fmt.Printf("  run dir: %s\n", runDir)

			orch := orchestrator.New(orchestrator.Options{
				RunDir:    runDir,
				Stages:    f.Stages,
				State:     rs,
				StateFile: stateFile,
				Config:    cfg,
				Prompts:   prompts,
			})

			ctx := context.Background()

			// Planning phase
			fmt.Println("flowmanager: planning phase...")
			if err := orch.PlanningPhase(ctx); err != nil {
				return fmt.Errorf("planning phase: %w", err)
			}

			// Wait for approvals (skill calls `flowmanager approve` for each stage)
			fmt.Println("flowmanager: waiting for plan approvals...")
			if err := orch.WaitForApprovals(ctx); err != nil {
				return fmt.Errorf("wait approvals: %w", err)
			}

			// Implementation phase
			fmt.Println("flowmanager: implementation phase...")
			if err := orch.ImplementationPhase(ctx); err != nil {
				return fmt.Errorf("implementation phase: %w", err)
			}

			// Summary
			fmt.Println("flowmanager: summary phase...")
			if err := orch.SummaryPhase(ctx); err != nil {
				return fmt.Errorf("summary phase: %w", err)
			}

			fmt.Printf("flowmanager: flow %q completed\n", f.Name)
			return nil
		},
	}

	cmd.Flags().IntVar(&maxParallel, "max-parallel", 0, "max parallel stages (0=unlimited)")
	cmd.Flags().DurationVar(&idleTimeout, "idle-timeout", 0, "agent idle timeout")
	return cmd
}

func resolveFlowPath(args []string) (string, error) {
	if len(args) > 0 {
		return args[0], nil
	}
	// Default: look in .flowManager/flows/
	entries, err := os.ReadDir(filepath.Join(".flowManager", "flows"))
	if err != nil {
		return "", fmt.Errorf("no flow file provided and .flowManager/flows/ not found")
	}
	var yamls []string
	for _, e := range entries {
		if !e.IsDir() && (filepath.Ext(e.Name()) == ".yaml" || filepath.Ext(e.Name()) == ".yml") {
			yamls = append(yamls, filepath.Join(".flowManager", "flows", e.Name()))
		}
	}
	if len(yamls) == 0 {
		return "", fmt.Errorf("no flow YAML files found in .flowManager/flows/")
	}
	if len(yamls) == 1 {
		return yamls[0], nil
	}
	return "", fmt.Errorf("multiple flow files found; specify one: %v", yamls)
}

func resolveRun(f *flow.Flow) (runDir string, rs *state.RunState, stateFile string, err error) {
	base := filepath.Join(".flowManager", "runs")

	// Check for existing incomplete run
	existing, lookErr := state.FindLatestRunDir(f.Name)
	if lookErr == nil {
		stateFile = filepath.Join(existing, "state.json")
		rs, err = state.Load(stateFile)
		if err == nil && !rs.AllDone() {
			fmt.Printf("flowmanager: resuming run %s\n", filepath.Base(existing))
			return existing, rs, stateFile, nil
		}
	}

	// New run
	ts := time.Now().Format("20060102-150405")
	runDir = filepath.Join(base, f.Name+"-"+ts)
	if err = os.MkdirAll(runDir, 0755); err != nil {
		return
	}
	stageIDs := make([]string, len(f.Stages))
	for i, s := range f.Stages {
		stageIDs[i] = s.ID
	}
	rs = state.NewRunState(stageIDs)
	rs.FlowName = f.Name
	stateFile = filepath.Join(runDir, "state.json")
	err = rs.Save(stateFile)
	return
}

func loadPrompts(overrideDir string) (orchestrator.Prompts, error) {
	names := []string{"planning.md", "implementation.md", "review.md", "summary.md"}
	texts := make([]string, len(names))
	for i, name := range names {
		text, err := assets.ReadPrompt(name, overrideDir)
		if err != nil {
			return orchestrator.Prompts{}, fmt.Errorf("read prompt %s: %w", name, err)
		}
		texts[i] = text
	}
	return orchestrator.Prompts{
		Planning:       texts[0],
		Implementation: texts[1],
		Review:         texts[2],
		Summary:        texts[3],
	}, nil
}
```

- [ ] **Step 2: Create cmd/flowmanager/check.go**

```go
// cmd/flowmanager/check.go
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"
	"github.com/you/flowmanager/pkg/state"
)

func newCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check",
		Short: "Show status of the latest flow run",
		RunE: func(cmd *cobra.Command, args []string) error {
			base := filepath.Join(".flowManager", "runs")
			entries, err := os.ReadDir(base)
			if err != nil {
				return fmt.Errorf("no runs found in %s", base)
			}

			// Find most recent run dir
			var dirs []string
			for _, e := range entries {
				if e.IsDir() {
					dirs = append(dirs, filepath.Join(base, e.Name()))
				}
			}
			if len(dirs) == 0 {
				return fmt.Errorf("no runs found")
			}
			sort.Strings(dirs)
			latest := dirs[len(dirs)-1]

			rs, err := state.Load(filepath.Join(latest, "state.json"))
			if err != nil {
				return fmt.Errorf("load state: %w", err)
			}

			fmt.Printf("Run: %s\n\n", filepath.Base(latest))
			fmt.Printf("%-20s  %-22s  %s\n", "STAGE", "STATUS", "UPDATED")
			fmt.Printf("%-20s  %-22s  %s\n", "-----", "------", "-------")

			// Sort stages by name for stable output
			type row struct{ id, status, updated string }
			var rows []row
			for id, s := range rs.Stages {
				rows = append(rows, row{id, string(s.Status), s.UpdatedAt.Format("15:04:05")})
			}
			sort.Slice(rows, func(i, j int) bool { return rows[i].id < rows[j].id })
			for _, r := range rows {
				fmt.Printf("%-20s  %-22s  %s\n", r.id, r.status, r.updated)
			}
			return nil
		},
	}
}
```

- [ ] **Step 3: Create cmd/flowmanager/approve.go**

```go
// cmd/flowmanager/approve.go
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"
	"github.com/you/flowmanager/pkg/state"
)

func newApproveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "approve [stage-id]",
		Short: "Approve a stage plan (transitions awaiting_approval → ready)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			stageID := args[0]
			stateFile, err := findLatestStateFile()
			if err != nil {
				return err
			}
			rs, err := state.Load(stateFile)
			if err != nil {
				return fmt.Errorf("load state: %w", err)
			}
			st, ok := rs.Stages[stageID]
			if !ok {
				return fmt.Errorf("stage %q not found", stageID)
			}
			if st.Status != state.StatusAwaitingApproval {
				return fmt.Errorf("stage %q is %v, not awaiting_approval", stageID, st.Status)
			}
			rs.SetStageStatus(stageID, state.StatusReady)
			if err := rs.Save(stateFile); err != nil {
				return fmt.Errorf("save state: %w", err)
			}
			fmt.Printf("approved stage %q: ready to run\n", stageID)
			return nil
		},
	}
}

func findLatestStateFile() (string, error) {
	base := filepath.Join(".flowManager", "runs")
	entries, err := os.ReadDir(base)
	if err != nil {
		return "", fmt.Errorf("no runs dir: %w", err)
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, filepath.Join(base, e.Name()))
		}
	}
	if len(dirs) == 0 {
		return "", fmt.Errorf("no runs found")
	}
	sort.Strings(dirs)
	return filepath.Join(dirs[len(dirs)-1], "state.json"), nil
}
```

- [ ] **Step 4: Create cmd/flowmanager/init.go**

```go
// cmd/flowmanager/init.go
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Create a flow.yaml interactively",
		RunE: func(cmd *cobra.Command, args []string) error {
			scanner := bufio.NewScanner(os.Stdin)

			name := prompt(scanner, "Flow name (e.g. my-feature): ")
			description := prompt(scanner, "Flow description: ")

			var stages []stageInput
			for {
				fmt.Println("\nAdd a stage (leave ID empty to finish):")
				id := prompt(scanner, "  Stage ID: ")
				if id == "" {
					break
				}
				stageName := prompt(scanner, "  Stage name: ")
				stageDesc := prompt(scanner, "  Stage description: ")
				depsRaw := prompt(scanner, "  Depends on (comma-separated IDs, or empty): ")
				agentsRaw := prompt(scanner, "  Agents [planning,implementation,review]: ")
				if agentsRaw == "" {
					agentsRaw = "planning,implementation,review"
				}
				stages = append(stages, stageInput{
					id: id, name: stageName, desc: stageDesc,
					deps: splitComma(depsRaw), agents: splitComma(agentsRaw),
				})
			}

			outPath := ".flowManager/flows/" + name + ".yaml"
			os.MkdirAll(".flowManager/flows", 0755) //nolint:errcheck

			var sb strings.Builder
			sb.WriteString("name: " + name + "\n")
			sb.WriteString("description: \"" + description + "\"\n\n")
			sb.WriteString("stages:\n")
			for _, s := range stages {
				sb.WriteString("  - id: " + s.id + "\n")
				sb.WriteString("    name: \"" + s.name + "\"\n")
				sb.WriteString("    description: \"" + s.desc + "\"\n")
				sb.WriteString("    agents: [" + strings.Join(s.agents, ", ") + "]\n")
				if len(s.deps) > 0 {
					sb.WriteString("    depends_on: [" + strings.Join(s.deps, ", ") + "]\n")
				}
				sb.WriteString("\n")
			}

			if err := os.WriteFile(outPath, []byte(sb.String()), 0644); err != nil {
				return fmt.Errorf("write flow file: %w", err)
			}
			fmt.Printf("\nCreated: %s\n", outPath)
			return nil
		},
	}
}

type stageInput struct {
	id, name, desc string
	deps, agents   []string
}

func prompt(scanner *bufio.Scanner, label string) string {
	fmt.Print(label)
	scanner.Scan()
	return strings.TrimSpace(scanner.Text())
}

func splitComma(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	result := parts[:0]
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			result = append(result, t)
		}
	}
	return result
}
```

- [ ] **Step 5: Create cmd/flowmanager/list.go**

```go
// cmd/flowmanager/list.go
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

func newListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List available flow files in .flowManager/flows/",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := filepath.Join(".flowManager", "flows")
			entries, err := os.ReadDir(dir)
			if err != nil {
				fmt.Println("No flows found (create one with `flowmanager init`)")
				return nil
			}
			fmt.Println("Available flows:")
			for _, e := range entries {
				if !e.IsDir() && (filepath.Ext(e.Name()) == ".yaml" || filepath.Ext(e.Name()) == ".yml") {
					fmt.Printf("  %s\n", filepath.Join(dir, e.Name()))
				}
			}
			return nil
		},
	}
}

func newResumeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "resume [run-id]",
		Short: "Resume an interrupted run by run directory name",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// The run command already auto-resumes; this is explicit.
			runDir := filepath.Join(".flowManager", "runs", args[0])
			if _, err := os.Stat(runDir); err != nil {
				return fmt.Errorf("run dir not found: %s", runDir)
			}
			fmt.Printf("flowmanager: resuming run %s\n", args[0])
			fmt.Println("(run `flowmanager run` with the original flow file to actually resume)")
			return nil
		},
	}
}
```

- [ ] **Step 6: Update main.go to use the new command files**

```go
// cmd/flowmanager/main.go
package main

import (
	"os"

	"github.com/spf13/cobra"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "flowmanager",
		Short: "Orchestrate multi-stage AI flows",
	}
	root.AddCommand(
		newRunCmd(),
		newCheckCmd(),
		newApproveCmd(),
		newInitCmd(),
		newListCmd(),
		newResumeCmd(),
	)
	return root
}
```

- [ ] **Step 7: Build and smoke test**

```bash
go build ./cmd/flowmanager
./flowmanager --help
./flowmanager run --help
./flowmanager check --help
./flowmanager approve --help
```

Expected: all commands show help text with correct flags.

- [ ] **Step 8: Commit**

```bash
git add cmd/flowmanager/
git commit -m "feat(cmd): полная реализация CLI команд"
```

---

## Task 11: Claude skills

**Files:**
- Create: `assets/claude/skills/flowmanager/SKILL.md`
- Create: `assets/claude/skills/flowmanager-check/SKILL.md`
- Create: `assets/claude/skills/flowmanager-init/SKILL.md`

- [ ] **Step 1: Create flowmanager/SKILL.md**

````markdown
---
description: Run a flowManager flow with stage plan approval
allowed-tools: [Bash, Read, AskUserQuestion, TaskOutput]
---

# flowmanager — Run a Flow

**SCOPE**: Launch, monitor, and handle plan approvals for a flowManager run.

## Step 0: Verify Installation

```bash
which flowmanager
```

If not found:
- macOS: `brew install <owner>/tap/flowmanager`
- Any platform: `go install github.com/you/flowmanager/cmd/flowmanager@latest`

## Step 1: Select Flow

```bash
flowmanager list
```

Show available flows via AskUserQuestion. If argument provided, use it directly.

## Step 2: Launch in Background

```bash
flowmanager run {selected-flow}
```

Run with `run_in_background: true`. Save task_id.

**Progress file:** `.flowManager/runs/` — find the newest directory after launch.

## Step 3: Monitor and Handle Approvals

Poll the latest `state.json` every 3 seconds:

```bash
cat .flowManager/runs/$(ls -t .flowManager/runs/ | head -1)/state.json
```

When a stage shows `"status": "awaiting_approval"`:
1. Read its plan: `cat .flowManager/runs/{run}/{stage-id}/plan.md`
2. Show plan to user via AskUserQuestion: "Stage '{name}' plan is ready. Approve?"
3. On approval: `flowmanager approve {stage-id}`
4. Continue polling

## Step 4: Report Completion

When all stages are `done`, run `flowmanager check` and display the result.

After reporting: **STOP**.

## Constraints

- Do NOT modify any code yourself
- Do NOT take any actions on the codebase outside of `flowmanager` commands
- After check report: stop
````

- [ ] **Step 2: Create flowmanager-check/SKILL.md**

````markdown
---
description: Show status of the current flowManager run
allowed-tools: [Bash]
---

# check flow — Flow Status

Run:

```bash
flowmanager check
```

Display the output as-is. **STOP immediately after displaying.**
````

- [ ] **Step 3: Create flowmanager-init/SKILL.md**

````markdown
---
description: Create a new flow.yaml interactively
allowed-tools: [Bash, AskUserQuestion, Read]
---

# flowmanager init — Create a Flow

## Step 0: Verify Installation

```bash
which flowmanager
```

## Step 1: Gather Information

Use AskUserQuestion to collect:
1. Flow name (kebab-case)
2. Flow description
3. For each stage: id, name, description, depends_on, agents

## Step 2: Run Init

Pass the answers interactively to:

```bash
flowmanager init
```

Or construct the YAML manually and save to `.flowManager/flows/{name}.yaml`.

## Step 3: Confirm

Show the created file path and contents. Ask if user wants to run it now.
````

- [ ] **Step 4: Verify files**

```bash
find assets/claude -name "SKILL.md" | sort
```

Expected: 3 SKILL.md files listed.

- [ ] **Step 5: Commit**

```bash
git add assets/claude/
git commit -m "feat(skills): Claude-скиллы launcher и check flow"
```

---

## Task 12: Makefile and goreleaser

**Files:**
- Create: `Makefile`
- Create: `.goreleaser.yml`
- Create: `.golangci.yml`

- [ ] **Step 1: Create Makefile**

```makefile
.PHONY: build test lint clean

build:
	go build -o bin/flowmanager ./cmd/flowmanager

test:
	go test ./... -v -race

lint:
	golangci-lint run ./...

clean:
	rm -rf bin/

install:
	go install ./cmd/flowmanager
```

- [ ] **Step 2: Create .goreleaser.yml**

```yaml
version: 2

project_name: flowmanager

before:
  hooks:
    - go mod tidy

builds:
  - main: ./cmd/flowmanager
    binary: flowmanager
    env:
      - CGO_ENABLED=0
    goos:
      - linux
      - darwin
      - windows
    goarch:
      - amd64
      - arm64

archives:
  - format: tar.gz
    format_overrides:
      - goos: windows
        format: zip
    name_template: "{{ .ProjectName }}_{{ .Os }}_{{ .Arch }}"

checksum:
  name_template: "checksums.txt"

release:
  github:
    owner: "{{ env \"GITHUB_REPOSITORY_OWNER\" }}"
    name: flowmanager
```

- [ ] **Step 3: Create .golangci.yml**

```yaml
linters:
  enable:
    - errcheck
    - govet
    - ineffassign
    - staticcheck
    - unused

linters-settings:
  errcheck:
    exclude-functions:
      - (*os.File).Close
      - (*os.File).Write
```

- [ ] **Step 4: Run full test suite**

```bash
make test
```

Expected: all tests PASS, no race conditions.

- [ ] **Step 5: Build binary**

```bash
make build
./bin/flowmanager --help
```

Expected: binary runs with help text listing all 6 subcommands.

- [ ] **Step 6: Commit**

```bash
git add Makefile .goreleaser.yml .golangci.yml
git commit -m "feat(build): Makefile, goreleaser, golangci"
```

---

## Verification (End-to-End)

- [ ] Create a test flow file:

```bash
mkdir -p .flowManager/flows
cat > .flowManager/flows/test.yaml << 'EOF'
name: test-flow
description: "Smoke test flow"
stages:
  - id: step1
    name: "Step 1"
    description: "Do step 1"
    agents: [planning, implementation]
EOF
```

- [ ] Verify parse and list:

```bash
./bin/flowmanager list
```

Expected: lists `test.yaml`.

- [ ] Run full test suite one final time:

```bash
make test 2>&1 | tail -5
```

Expected: `ok` for all packages, no failures.

- [ ] Verify all packages build with no lint errors:

```bash
make build
```

Expected: binary produced in `bin/`.
