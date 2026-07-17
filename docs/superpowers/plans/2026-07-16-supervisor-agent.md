# Supervisor Agent Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Добавить Агента-Супервизора, который на старте стадии оценивает через LLM, можно ли выполнить задачу автономно (одним шагом через скилл), и если да — схлопывает фазы planning/impl/review в `autonomous_execution`.

**Architecture:** Supervisor вызывается внутри горутины запуска стадии (после `EvStartPlanning`-guard), вызывает `RunJSONQuery` на отдельном executor с командой `supervisor_command`, получает JSON-решение. При автономном треке: `EvSupervisorApproved` (planning→ready) → `EvStartRun` → `runAutonomousAgent`. Флаг `autonomous.flag` фиксирует трек на диске для корректного resume.

**Tech Stack:** Go, `text/template`, `encoding/json`, `os/exec`, `embed.FS` (assets)

## Global Constraints

- Не менять версию Go в go.mod
- Все новые FSM-переходы добавлять в `pkg/orchestrator/fsm.go` (map `rules`)
- Supervisor может только сокращать фазы, никогда не добавлять
- Любая ошибка LLM-вызова → безопасный фолбэк на базовые фазы
- `events.jsonl` не трогать — supervisor пишет в отдельный `supervisor.jsonl`
- Коммиты на русском языке, без Co-Authored-By

---

## File Map

| Файл | Действие | Ответственность |
|------|----------|-----------------|
| `pkg/flow/flow.go` | modify | + `Stage.Supervisor`, `Stage.SupervisorPrompt`, `Flow.SupervisorCommand` |
| `pkg/config/config.go` | modify | + `SupervisorConfig`, `Config.Supervisor`, merge в `mergeFile` |
| `pkg/executor/runner.go` | modify | + `RunJSONQuery` в интерфейс |
| `pkg/executor/executor.go` | modify | Реализация `RunJSONQuery` |
| `pkg/orchestrator/supervisor.go` | create | `Supervisor`, `EvaluationResult`, промпт-шаблон, `compileSupervisorPrompt`, `validateDecision` |
| `pkg/orchestrator/fsm.go` | modify | + `EvSupervisorApproved: planning → ready` |
| `pkg/orchestrator/bus.go` | modify | + `EventSupervisorDecision` |
| `pkg/orchestrator/context.go` | modify | `CollectDependencyPlans` — фолбэк на `execution_summary.md` |
| `assets/prompts/autonomous.md` | create | Промпт-шаблон для автономного агента |
| `pkg/prompts/builder.go` | modify | + `AgentAutonomous`, поле `Autonomous` в `Inputs` |
| `pkg/orchestrator/orchestrator.go` | modify | + `supervisor` поле в `Orchestrator`/`Options`, `agentTypesToStrings`, `DetermineStagePhases`, `runAutonomousAgent`, `logSupervisorDecision` |
| `pkg/orchestrator/recovery.go` | modify | `startPlanningForPending`: resume-логика для `StatusRunning` с `autonomous.flag` |
| `cmd/afm/run.go` | modify | Инициализация `SupervisorRunner` из resolved supervisor command, `loadPrompts` + `Autonomous` |

---

### Task 1: Модели данных — flow.go + config.go

**Files:**
- Modify: `pkg/flow/flow.go:52-80`
- Modify: `pkg/config/config.go:195-314`
- Modify: `pkg/flow/flow_test.go`
- Modify: `pkg/config/config_test.go`

**Interfaces:**
- Produces: `flow.Stage.Supervisor bool`, `flow.Stage.SupervisorPrompt string`, `flow.Flow.SupervisorCommand string`, `config.SupervisorConfig{Command string}`, `config.Config.Supervisor SupervisorConfig`

- [ ] **Step 1: Добавить поля в `pkg/flow/flow.go`**

В структуру `Stage` (после поля `Prompt string`):
```go
// Supervisor включает оценку стадии агентом-супервизором перед запуском.
// Стадия обязана содержать AgentPlanning в Agents.
Supervisor       bool   `yaml:"supervisor"`
SupervisorPrompt string `yaml:"supervisor_prompt,omitempty"`
```

В структуру `Flow` (после поля `MaxParallel int`):
```go
// SupervisorCommand задаёт команду для агента-супервизора (как command у стадии).
// Default: значение из config.Supervisor.Command или config.Client.Command.
SupervisorCommand string `yaml:"supervisor_command,omitempty"`
```

- [ ] **Step 2: Написать тест для YAML-парсинга новых полей в `pkg/flow/flow_test.go`**

```go
func TestFlow_SupervisorFields(t *testing.T) {
    yaml := `
name: test
supervisor_command: glm51
stages:
  - id: s1
    description: do something
    supervisor: true
    supervisor_prompt: "extra hint"
    agents: [planning, implementation]
    skills: [goga:apply]
`
    f, err := ParseFile(writeTempYAML(t, yaml))
    if err != nil {
        t.Fatal(err)
    }
    if f.SupervisorCommand != "glm51" {
        t.Errorf("got SupervisorCommand=%q, want glm51", f.SupervisorCommand)
    }
    s := f.Stages[0]
    if !s.Supervisor {
        t.Error("expected Supervisor=true")
    }
    if s.SupervisorPrompt != "extra hint" {
        t.Errorf("got SupervisorPrompt=%q, want 'extra hint'", s.SupervisorPrompt)
    }
}
```

(Хелпер `writeTempYAML` — если не существует в flow_test.go, добавить:)
```go
func writeTempYAML(t *testing.T, content string) string {
    t.Helper()
    f, err := os.CreateTemp(t.TempDir(), "flow*.yaml")
    if err != nil { t.Fatal(err) }
    if _, err := f.WriteString(content); err != nil { t.Fatal(err) }
    f.Close()
    return f.Name()
}
```

- [ ] **Step 3: Запустить тест — убедиться что FAIL**
```bash
cd /Users/alexander.kopichin/work/flowManager
go test ./pkg/flow/... -run TestFlow_SupervisorFields -v
```
Ожидаем: FAIL (поля не существуют).

- [ ] **Step 4: Добавить поля в `pkg/config/config.go`**

После `DockerConfig` struct, добавить:
```go
// SupervisorConfig настраивает агента-супервизора.
type SupervisorConfig struct {
    Command string `yaml:"command"`
}
```

В `Config` struct (после поля `Docker DockerConfig`):
```go
Supervisor SupervisorConfig `yaml:"supervisor"`
```

В функцию `mergeFile` (после блока merge Docker, перед `return nil`):
```go
if overlay.Supervisor.Command != "" {
    dst.Supervisor.Command = overlay.Supervisor.Command
}
```

- [ ] **Step 5: Написать тест для конфига в `pkg/config/config_test.go`**

```go
func TestConfig_SupervisorMerge(t *testing.T) {
    dir := t.TempDir()
    cfgFile := filepath.Join(dir, "config.yaml")
    if err := os.WriteFile(cfgFile, []byte("supervisor:\n  command: glm51\n"), 0644); err != nil {
        t.Fatal(err)
    }
    cfg, err := LoadFrom("", dir)
    if err != nil {
        t.Fatal(err)
    }
    if cfg.Supervisor.Command != "glm51" {
        t.Errorf("got %q, want glm51", cfg.Supervisor.Command)
    }
}
```

- [ ] **Step 6: Запустить тесты — убедиться что PASS**
```bash
go test ./pkg/flow/... ./pkg/config/... -run "TestFlow_SupervisorFields|TestConfig_SupervisorMerge" -v
```
Ожидаем: PASS.

- [ ] **Step 7: Проверить линт**
```bash
go vet ./pkg/flow/... ./pkg/config/...
```

- [ ] **Step 8: Коммит**
```bash
git add pkg/flow/flow.go pkg/config/config.go pkg/flow/flow_test.go pkg/config/config_test.go
git commit -m "feat(flow,config): поля Supervisor/SupervisorCommand/SupervisorConfig"
```

---

### Task 2: Executor — `RunJSONQuery`

**Files:**
- Modify: `pkg/executor/runner.go`
- Modify: `pkg/executor/executor.go`
- Modify: `pkg/executor/executor_test.go`

**Interfaces:**
- Consumes: `executor.Executor` struct (existing)
- Produces: `Runner.RunJSONQuery(ctx context.Context, prompt string) ([]byte, error)`

- [ ] **Step 1: Написать тест в `pkg/executor/executor_test.go`**

```go
func TestExecutor_RunJSONQuery(t *testing.T) {
    // Мок: команда bash -c 'echo {"ok":true}'
    e := New(Config{
        Command:   "bash",
        ExtraArgs: []string{"-c", `echo '{"ok":true}'`},
    })
    ctx := context.Background()
    got, err := e.RunJSONQuery(ctx, "ignored prompt")
    if err != nil {
        t.Fatal(err)
    }
    var m map[string]bool
    if err := json.Unmarshal(got, &m); err != nil {
        t.Fatalf("unmarshal: %v, raw=%q", err, got)
    }
    if !m["ok"] {
        t.Errorf("expected ok=true, got %v", m)
    }
}

func TestExecutor_RunJSONQuery_Error(t *testing.T) {
    e := New(Config{
        Command:   "bash",
        ExtraArgs: []string{"-c", "exit 1"},
    })
    _, err := e.RunJSONQuery(context.Background(), "prompt")
    if err == nil {
        t.Fatal("expected error, got nil")
    }
}
```

- [ ] **Step 2: Запустить тест — убедиться что FAIL**
```bash
go test ./pkg/executor/... -run "TestExecutor_RunJSONQuery" -v
```
Ожидаем: FAIL (метод не существует).

- [ ] **Step 3: Добавить метод в интерфейс `pkg/executor/runner.go`**

```go
type Runner interface {
    RunPlanning(ctx context.Context, stageName, prompt, outFile, logFile string) error
    RunAgent(ctx context.Context, agentType, stageName, prompt, logFile string) error
    // RunJSONQuery запускает команду с -p prompt --output-format json
    // и возвращает сырые байты stdout. Используется Supervisor.
    RunJSONQuery(ctx context.Context, prompt string) ([]byte, error)
}
```

- [ ] **Step 4: Реализовать в `pkg/executor/executor.go`**

Добавить метод после `RunAgent`:
```go
// RunJSONQuery запускает команду с одним промптом в JSON-режиме.
// Не использует stream-json — просто захватывает stdout.
func (e *Executor) RunJSONQuery(ctx context.Context, prompt string) ([]byte, error) {
    args := make([]string, 0, len(e.extraArgs)+3)
    args = append(args, e.extraArgs...)
    args = append(args, "-p", prompt, "--output-format", "json")
    cmd := exec.CommandContext(ctx, e.command, args...)
    if e.wrapperDir != "" {
        cmd.Env = append(os.Environ(), "PATH="+e.wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
    }
    return cmd.Output()
}
```

Убедиться что импорт `"os/exec"` и `"os"` уже есть в файле (они есть).

- [ ] **Step 5: Запустить тесты — убедиться что PASS**
```bash
go test ./pkg/executor/... -run "TestExecutor_RunJSONQuery" -v
```
Ожидаем: PASS.

- [ ] **Step 6: Убедиться что компилируется (compile-time check Runner уже есть в файле)**
```bash
go build ./pkg/executor/...
```

- [ ] **Step 7: Линт**
```bash
go vet ./pkg/executor/...
```

- [ ] **Step 8: Коммит**
```bash
git add pkg/executor/runner.go pkg/executor/executor.go pkg/executor/executor_test.go
git commit -m "feat(executor): метод RunJSONQuery для однократных LLM-вызовов"
```

---

### Task 3: Компонент Supervisor (`pkg/orchestrator/supervisor.go`)

**Files:**
- Create: `pkg/orchestrator/supervisor.go`
- Create: `pkg/orchestrator/supervisor_test.go`

**Interfaces:**
- Consumes: `executor.Runner` (из Task 2), `flow.Stage` (из Task 1)
- Produces:
  - `EvaluationResult{CanExecuteAutonomously bool, Reason string, RecommendedPhases []string}`
  - `Supervisor{runner executor.Runner}`
  - `NewSupervisor(r executor.Runner) *Supervisor`
  - `(*Supervisor).EvaluateStage(ctx context.Context, stage flow.Stage, globalPrompt string) (*EvaluationResult, error)`

- [ ] **Step 1: Создать `pkg/orchestrator/supervisor_test.go`**

```go
package orchestrator

import (
    "context"
    "errors"
    "testing"

    "github.com/akopichin/afm/pkg/flow"
)

// mockJSONRunner реализует executor.Runner с настраиваемым RunJSONQuery.
type mockJSONRunner struct {
    response []byte
    err      error
}

func (m *mockJSONRunner) RunJSONQuery(_ context.Context, _ string) ([]byte, error) {
    return m.response, m.err
}
func (m *mockJSONRunner) RunPlanning(_ context.Context, _, _, _, _ string) error { return nil }
func (m *mockJSONRunner) RunAgent(_ context.Context, _, _, _, _ string) error    { return nil }

func makeTestStage(supervisor bool, agents []flow.AgentType, skills []string) flow.Stage {
    return flow.Stage{
        ID:          "test-stage",
        Description: "do the thing",
        Supervisor:  supervisor,
        Agents:      agents,
        Skills:      skills,
    }
}

func TestSupervisor_Autonomous(t *testing.T) {
    runner := &mockJSONRunner{
        response: []byte(`{"can_execute_autonomously":true,"reason":"skill handles it","recommended_phases":["autonomous_execution"]}`),
    }
    s := NewSupervisor(runner)
    stage := makeTestStage(true, []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}, []string{"goga:apply"})

    result, err := s.EvaluateStage(context.Background(), stage, "global context")
    if err != nil {
        t.Fatal(err)
    }
    if !result.CanExecuteAutonomously {
        t.Error("expected autonomous=true")
    }
    if len(result.RecommendedPhases) != 1 || result.RecommendedPhases[0] != "autonomous_execution" {
        t.Errorf("unexpected phases: %v", result.RecommendedPhases)
    }
}

func TestSupervisor_Standard(t *testing.T) {
    runner := &mockJSONRunner{
        response: []byte(`{"can_execute_autonomously":false,"reason":"needs planning","recommended_phases":["planning","implementation"]}`),
    }
    s := NewSupervisor(runner)
    stage := makeTestStage(true, []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}, []string{"goga:apply"})

    result, err := s.EvaluateStage(context.Background(), stage, "")
    if err != nil {
        t.Fatal(err)
    }
    if result.CanExecuteAutonomously {
        t.Error("expected autonomous=false")
    }
}

func TestSupervisor_RunnerError_ReturnsError(t *testing.T) {
    runner := &mockJSONRunner{err: errors.New("network timeout")}
    s := NewSupervisor(runner)
    stage := makeTestStage(true, []flow.AgentType{flow.AgentPlanning}, nil)

    _, err := s.EvaluateStage(context.Background(), stage, "")
    if err == nil {
        t.Fatal("expected error, got nil")
    }
}

func TestSupervisor_BadJSON_ReturnsError(t *testing.T) {
    runner := &mockJSONRunner{response: []byte(`not json`)}
    s := NewSupervisor(runner)
    _, err := s.EvaluateStage(context.Background(), makeTestStage(true, nil, nil), "")
    if err == nil {
        t.Fatal("expected error for bad JSON")
    }
}

func TestSupervisor_HallucinatedPhase_ReturnsError(t *testing.T) {
    // Supervisor рекомендует фазу которой нет в stage.Agents — должно быть отклонено
    runner := &mockJSONRunner{
        response: []byte(`{"can_execute_autonomously":false,"reason":"x","recommended_phases":["deploy"]}`),
    }
    s := NewSupervisor(runner)
    stage := makeTestStage(true, []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}, nil)
    _, err := s.EvaluateStage(context.Background(), stage, "")
    if err == nil {
        t.Fatal("expected error for hallucinated phase")
    }
}
```

- [ ] **Step 2: Запустить тесты — убедиться что FAIL**
```bash
go test ./pkg/orchestrator/... -run "TestSupervisor_" -v
```
Ожидаем: FAIL (файл не существует).

- [ ] **Step 3: Создать `pkg/orchestrator/supervisor.go`**

```go
package orchestrator

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "text/template"

    "github.com/akopichin/afm/pkg/executor"
    "github.com/akopichin/afm/pkg/flow"
)

// EvaluationResult — ответ супервизора на оценку стадии.
type EvaluationResult struct {
    CanExecuteAutonomously bool     `json:"can_execute_autonomously"`
    Reason                 string   `json:"reason"`
    RecommendedPhases      []string `json:"recommended_phases"`
}

// Supervisor оценивает стадию и решает, можно ли выполнить её автономно.
type Supervisor struct {
    runner executor.Runner
}

// NewSupervisor создаёт Supervisor с заданным runner.
func NewSupervisor(r executor.Runner) *Supervisor {
    return &Supervisor{runner: r}
}

// supervisorPromptTmpl — системный промпт на английском (экономия токенов).
const supervisorPromptTmpl = `You are an AI Supervisor in the afm CLI orchestrator. Determine if a stage can execute autonomously in a single step using its attached skills, or requires the standard multi-phase development cycle.

GLOBAL PROJECT RULES:
<global_prompt>
{{.GlobalPrompt}}
</global_prompt>

CURRENT STAGE:
- ID: {{.Stage.ID}}
- Description: {{.Stage.Description}}
- Attached Skills: {{.SkillsList}}
- Base Phases (configured by user): {{.BasePhases}}
{{if .Stage.SupervisorPrompt}}
EXTRA STAGE-SPECIFIC INSTRUCTIONS (Highest priority):
<local_supervisor_prompt>
{{.Stage.SupervisorPrompt}}
</local_supervisor_prompt>
{{end}}
CONSTRAINTS:
1. "recommended_phases" MUST be exactly one of:
   - ["autonomous_execution"] — skill handles the entire task end-to-end without planning.
   - {{.BasePhases}} — standard cycle required (unsafe, unclear, or needs human approval).
2. NEVER add phases not present in Base Phases.

Respond with ONLY this JSON (no markdown, no preamble):
{
  "can_execute_autonomously": <true|false>,
  "reason": "<concise justification referencing skills and local prompt>",
  "recommended_phases": [<"autonomous_execution" or base phases list>]
}`

type supervisorTmplData struct {
    GlobalPrompt string
    Stage        flow.Stage
    SkillsList   string
    BasePhases   []string
}

var supervisorTmpl = template.Must(template.New("supervisor").Parse(supervisorPromptTmpl))

func compileSupervisorPrompt(stage flow.Stage, globalPrompt string) (string, error) {
    data := supervisorTmplData{
        GlobalPrompt: globalPrompt,
        Stage:        stage,
        SkillsList:   formatSkills(stage.Skills),
        BasePhases:   agentTypesToStrings(stage.Agents),
    }
    var buf bytes.Buffer
    if err := supervisorTmpl.Execute(&buf, data); err != nil {
        return "", fmt.Errorf("compile supervisor prompt: %w", err)
    }
    return buf.String(), nil
}

func formatSkills(skills []string) string {
    if len(skills) == 0 {
        return "(none)"
    }
    result := ""
    for i, s := range skills {
        if i > 0 {
            result += ", "
        }
        result += s
    }
    return result
}

// EvaluateStage вызывает LLM и возвращает решение о треке стадии.
// При любой ошибке вызывающий должен использовать базовые фазы (фолбэк).
func (s *Supervisor) EvaluateStage(ctx context.Context, stage flow.Stage, globalPrompt string) (*EvaluationResult, error) {
    prompt, err := compileSupervisorPrompt(stage, globalPrompt)
    if err != nil {
        return nil, err
    }
    raw, err := s.runner.RunJSONQuery(ctx, prompt)
    if err != nil {
        return nil, fmt.Errorf("supervisor LLM call: %w", err)
    }
    var result EvaluationResult
    if err := json.Unmarshal(bytes.TrimSpace(raw), &result); err != nil {
        return nil, fmt.Errorf("parse supervisor response: %w (raw: %.200s)", err, raw)
    }
    if err := validateDecision(&result, stage); err != nil {
        return nil, err
    }
    return &result, nil
}

// validateDecision проверяет что supervisor не добавил лишних фаз.
func validateDecision(result *EvaluationResult, stage flow.Stage) error {
    if len(result.RecommendedPhases) == 0 {
        return fmt.Errorf("supervisor returned empty recommended_phases")
    }
    if len(result.RecommendedPhases) == 1 && result.RecommendedPhases[0] == "autonomous_execution" {
        return nil
    }
    allowed := make(map[string]bool, len(stage.Agents))
    for _, a := range stage.Agents {
        allowed[string(a)] = true
    }
    for _, p := range result.RecommendedPhases {
        if !allowed[p] {
            return fmt.Errorf("supervisor recommended unknown phase %q (not in stage.Agents)", p)
        }
    }
    return nil
}
```

- [ ] **Step 4: Запустить тесты — убедиться что PASS**
```bash
go test ./pkg/orchestrator/... -run "TestSupervisor_" -v
```
Ожидаем: PASS все 5 тестов.

- [ ] **Step 5: Линт**
```bash
go vet ./pkg/orchestrator/...
```

- [ ] **Step 6: Коммит**
```bash
git add pkg/orchestrator/supervisor.go pkg/orchestrator/supervisor_test.go
git commit -m "feat(orchestrator): компонент Supervisor — EvaluateStage + валидация ответа"
```

---

### Task 4: FSM-переход + UIBus событие

**Files:**
- Modify: `pkg/orchestrator/fsm.go:52-73`
- Modify: `pkg/orchestrator/bus.go:12-23`
- Modify: `pkg/orchestrator/fsm_test.go`

**Interfaces:**
- Produces: `EvSupervisorApproved FSMEvent = "supervisor_approved"` (planning → ready)
- Produces: `EventSupervisorDecision EventType = "supervisor_decision"`

- [ ] **Step 1: Добавить тест FSM-перехода в `pkg/orchestrator/fsm_test.go`**

В `TestFSM_Apply_LegalTransitions`, в slice `cases` добавить:
```go
{"planning->ready(supervisor)", state.StatusPlanning, EvSupervisorApproved, state.StatusReady, true},
```

- [ ] **Step 2: Запустить тест — убедиться что FAIL**
```bash
go test ./pkg/orchestrator/... -run "TestFSM_Apply_LegalTransitions" -v
```
Ожидаем: FAIL (EvSupervisorApproved не существует).

- [ ] **Step 3: Добавить переход в `pkg/orchestrator/fsm.go`**

В блок констант после `EvReady`:
```go
EvSupervisorApproved FSMEvent = "supervisor_approved"
```

В `NewFSM`, в map `rules` после записи `EvReady`:
```go
EvSupervisorApproved: {From: []state.StageStatus{state.StatusPlanning}, To: to(state.StatusReady)},
```

- [ ] **Step 4: Добавить EventType в `pkg/orchestrator/bus.go`**

В блок констант (после `EventUserAnswered`):
```go
EventSupervisorDecision EventType = "supervisor_decision"
```

- [ ] **Step 5: Запустить тест — убедиться что PASS**
```bash
go test ./pkg/orchestrator/... -run "TestFSM_Apply_LegalTransitions" -v
```
Ожидаем: PASS.

- [ ] **Step 6: Линт**
```bash
go vet ./pkg/orchestrator/...
```

- [ ] **Step 7: Коммит**
```bash
git add pkg/orchestrator/fsm.go pkg/orchestrator/bus.go pkg/orchestrator/fsm_test.go
git commit -m "feat(fsm,bus): EvSupervisorApproved + EventSupervisorDecision"
```

---

### Task 5: Фолбэк на `execution_summary.md` в `CollectDependencyPlans`

**Files:**
- Modify: `pkg/orchestrator/context.go:14-44`
- Modify: `pkg/orchestrator/plan_source_test.go` (или создать новый test file)

**Interfaces:**
- Consumes: `autonomous.flag` файл в stageDir (presence = автономная стадия)
- Produces: обновлённый `CollectDependencyPlans` читает `execution_summary.md` для автономных стадий

- [ ] **Step 1: Написать тест в `pkg/orchestrator/plan_source_test.go`**

```go
func TestCollectDependencyPlans_AutonomousFallback(t *testing.T) {
    runDir := t.TempDir()

    // Создаём "предыдущую" автономную стадию
    depDir := filepath.Join(runDir, "dep1")
    if err := os.MkdirAll(depDir, 0755); err != nil {
        t.Fatal(err)
    }
    // autonomous.flag сигнализирует что стадия прошла автономный трек
    if err := os.WriteFile(filepath.Join(depDir, "autonomous.flag"), nil, 0644); err != nil {
        t.Fatal(err)
    }
    // execution_summary.md — контекст который будет передан зависимой стадии
    summary := "## Summary\nDid the thing.\n## Changes\n- foo.go\n## Result\nOK\n"
    if err := os.WriteFile(filepath.Join(depDir, "execution_summary.md"), []byte(summary), 0644); err != nil {
        t.Fatal(err)
    }

    stages := []flow.Stage{
        {ID: "dep1", Description: "dep", Agents: []flow.AgentType{flow.AgentPlanning}},
        {ID: "s1", Description: "main", DependsOn: []string{"dep1"}, Agents: []flow.AgentType{flow.AgentPlanning}},
    }
    result := CollectDependencyPlans(runDir, stages[1], stages)
    if !strings.Contains(result, "Did the thing") {
        t.Errorf("expected execution_summary.md content in result, got:\n%s", result)
    }
    if strings.Contains(result, "plan not available") {
        t.Error("should not say 'plan not available' when execution_summary.md exists")
    }
}

func TestCollectDependencyPlans_StandardPlan(t *testing.T) {
    runDir := t.TempDir()
    depDir := filepath.Join(runDir, "dep1")
    if err := os.MkdirAll(depDir, 0755); err != nil {
        t.Fatal(err)
    }
    // Нет autonomous.flag — читаем plan.md как обычно
    if err := os.WriteFile(filepath.Join(depDir, "plan.md"), []byte("## Tasks\n- do it\n"), 0644); err != nil {
        t.Fatal(err)
    }
    stages := []flow.Stage{
        {ID: "dep1", Agents: []flow.AgentType{flow.AgentPlanning}},
        {ID: "s1", DependsOn: []string{"dep1"}, Agents: []flow.AgentType{flow.AgentPlanning}},
    }
    result := CollectDependencyPlans(runDir, stages[1], stages)
    if !strings.Contains(result, "do it") {
        t.Errorf("expected plan.md content, got:\n%s", result)
    }
}
```

Проверить что `strings` уже импортирован в test файле — если нет, добавить в imports.

- [ ] **Step 2: Запустить тесты — убедиться что FAIL**
```bash
go test ./pkg/orchestrator/... -run "TestCollectDependencyPlans_Autonomous|TestCollectDependencyPlans_Standard" -v
```
Ожидаем: FAIL.

- [ ] **Step 3: Обновить `pkg/orchestrator/context.go`**

В функции `CollectDependencyPlans`, заменить тело цикла `for _, depID := range stage.DependsOn`:
```go
for _, depID := range stage.DependsOn {
    stageDir := filepath.Join(runDir, depID)
    name := nameIndex[depID]
    if name == "" {
        name = depID
    }
    fmt.Fprintf(&buf, "\n### Stage: %s (%s)\n\n", name, depID)

    // Если стадия прошла автономный трек — читаем execution_summary.md
    var data []byte
    autonomousFlag := filepath.Join(stageDir, "autonomous.flag")
    if _, err := os.Stat(autonomousFlag); err == nil {
        data, _ = os.ReadFile(filepath.Join(stageDir, "execution_summary.md"))
    } else {
        data, _ = os.ReadFile(filepath.Join(stageDir, "plan.md"))
    }

    if len(data) == 0 {
        buf.WriteString("(plan not available)\n")
        continue
    }
    buf.WriteString(string(data))
    buf.WriteString("\n")
}
```

Убедиться что `"os"` уже импортирован в context.go (он есть).

- [ ] **Step 4: Запустить тесты — убедиться что PASS**
```bash
go test ./pkg/orchestrator/... -run "TestCollectDependencyPlans_" -v
```
Ожидаем: PASS.

- [ ] **Step 5: Линт**
```bash
go vet ./pkg/orchestrator/...
```

- [ ] **Step 6: Коммит**
```bash
git add pkg/orchestrator/context.go pkg/orchestrator/plan_source_test.go
git commit -m "feat(context): фолбэк на execution_summary.md для автономных стадий"
```

---

### Task 6: Промпт-шаблон автономного агента + `prompts.Build`

**Files:**
- Create: `assets/prompts/autonomous.md`
- Modify: `pkg/prompts/builder.go:19-34`
- Modify: `pkg/orchestrator/orchestrator.go:52-57` (Prompts struct)

**Interfaces:**
- Produces: `prompts.AgentAutonomous Agent = "autonomous_execution"`, поле `Autonomous string` в `Inputs`
- Produces: `Prompts.Autonomous string` в orchestrator

- [ ] **Step 1: Создать `assets/prompts/autonomous.md`**

```markdown
# Autonomous Execution Agent

You are executing a task autonomously using attached skills. You have been pre-approved to act without a planning phase — use your attached skills to complete the task end-to-end in one step.

## Rules

- Execute the task described in the `<stage>` block using the skills listed in `<skills>`.
- Do NOT ask for approval, do NOT wait for human input.
- Do NOT produce a plan.md — jump directly to execution.
- When complete, write `execution_summary.md` to `$AFM_STAGE_DIR/execution_summary.md`.

## Output Contract (mandatory)

`execution_summary.md` MUST contain these top-level sections (exact names):
- `## Summary` — what you executed and key decisions made.
- `## Changes` — files created or modified (list paths, one per line).
- `## Result` — final outcome. Mention any failures or partial completions.

Missing any section causes a retry.
```

- [ ] **Step 2: Добавить `AgentAutonomous` и поле `Autonomous` в `pkg/prompts/builder.go`**

В блок констант Agent:
```go
AgentAutonomous Agent = "autonomous_execution"
```

В структуру `Inputs` (после поля `GlobalPrompt string`):
```go
// Autonomous — шаблон для автономного трека (без plan.md, с execution_summary.md).
// Если непустой, используется вместо Template.
Autonomous string
```

В функцию `Build`, в самом начале после `var sb strings.Builder`:
```go
// Если задан автономный шаблон — использовать его вместо основного.
tmpl := in.Template
if in.Autonomous != "" {
    tmpl = in.Autonomous
}
sb.WriteString("<system_rules>\n")
sb.WriteString(tmpl)
```

Заменить первые строки `Build`:
```go
func Build(in Inputs) string {
    var sb strings.Builder

    tmpl := in.Template
    if in.Autonomous != "" {
        tmpl = in.Autonomous
    }
    sb.WriteString("<system_rules>\n")
    sb.WriteString(tmpl)
    // остаток функции без изменений...
```

(Удалить дублирующую строку `sb.WriteString(in.Template)` которая была после `sb.WriteString("<system_rules>\n")`.)

- [ ] **Step 3: Добавить поле `Autonomous` в `Prompts` struct в `pkg/orchestrator/orchestrator.go`**

```go
type Prompts struct {
    Planning       string
    Implementation string
    Review         string
    Summary        string
    Autonomous     string
}
```

- [ ] **Step 4: Проверить что всё компилируется**
```bash
go build ./...
```

- [ ] **Step 5: Линт**
```bash
go vet ./...
```

- [ ] **Step 6: Коммит**
```bash
git add assets/prompts/autonomous.md pkg/prompts/builder.go pkg/orchestrator/orchestrator.go
git commit -m "feat(prompts): шаблон autonomous.md + AgentAutonomous + Prompts.Autonomous"
```

---

### Task 7: Ядро оркестратора — `DetermineStagePhases`, `runAutonomousAgent`, `logSupervisorDecision`

**Files:**
- Modify: `pkg/orchestrator/orchestrator.go`
- Modify: `pkg/orchestrator/completion.go`
- Create: `pkg/orchestrator/supervisor_orchestrator_test.go`

**Interfaces:**
- Consumes: `Supervisor.EvaluateStage` (Task 3), `EvSupervisorApproved` (Task 4), `Prompts.Autonomous` (Task 6)
- Produces:
  - `agentTypesToStrings(agents []flow.AgentType) []string`
  - `isAutonomousStage(stageDir string) bool`
  - `checkAutonomousCompletion(stageDir string) error`
  - `(*Orchestrator).DetermineStagePhases(ctx, stage) []string`
  - `(*Orchestrator).runAutonomousAgent(ctx, stage)`
  - `(*Orchestrator).logSupervisorDecision(stageID, decision, reason string)`

- [ ] **Step 1: Добавить `supervisor` поле в `Options` и `Orchestrator` в `orchestrator.go`**

В `Options` struct (после `RequireApproval bool`):
```go
// SupervisorRunner — runner для вызовов Supervisor.EvaluateStage.
// nil = Supervisor отключён глобально.
SupervisorRunner executor.Runner
```

В `Orchestrator` struct (после `violationCache map[string]violationCacheEntry`):
```go
supervisor *Supervisor
```

В `New()` (после инициализации `violationCache`, перед `return`):
```go
var sup *Supervisor
if opts.SupervisorRunner != nil {
    sup = NewSupervisor(opts.SupervisorRunner)
}
```

В `return &Orchestrator{...}` добавить поле:
```go
supervisor: sup,
```

- [ ] **Step 2: Добавить хелперы в `orchestrator.go`** (в конец файла, после `copyFile`)

```go
// agentTypesToStrings конвертирует []flow.AgentType в []string.
func agentTypesToStrings(agents []flow.AgentType) []string {
    ss := make([]string, len(agents))
    for i, a := range agents {
        ss[i] = string(a)
    }
    return ss
}

// isAutonomousStage возвращает true если stageDir содержит autonomous.flag.
func isAutonomousStage(stageDir string) bool {
    _, err := os.Stat(filepath.Join(stageDir, "autonomous.flag"))
    return err == nil
}

// logSupervisorDecision записывает решение супервизора в <runDir>/supervisor.jsonl.
func (o *Orchestrator) logSupervisorDecision(stageID, decision, reason string) {
    type entry struct {
        Ts       string `json:"ts"`
        StageID  string `json:"stage_id"`
        Decision string `json:"decision"`
        Reason   string `json:"reason"`
    }
    e := entry{
        Ts:       time.Now().UTC().Format(time.RFC3339),
        StageID:  stageID,
        Decision: decision,
        Reason:   reason,
    }
    data, err := json.Marshal(e)
    if err != nil {
        return
    }
    logPath := filepath.Join(o.opts.RunDir, "supervisor.jsonl")
    f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
    if err != nil {
        return
    }
    defer f.Close()
    _, _ = f.Write(append(data, '\n'))
}

// DetermineStagePhases вызывает Supervisor и возвращает выбранные фазы.
// Вызывается внутри горутины (не блокирует event loop).
// При любой ошибке или отключённом supervisor возвращает базовые фазы.
func (o *Orchestrator) DetermineStagePhases(ctx context.Context, s flow.Stage) []string {
    base := agentTypesToStrings(s.Agents)

    if !s.Supervisor || o.supervisor == nil {
        return base
    }
    // Inline-артефакт guard: planning пропускать нельзя
    for _, art := range s.Artifacts {
        if art.IsInline() {
            log.Printf("supervisor: stage %s has inline artifact, skipping evaluation", s.ID)
            return base
        }
    }
    decision, err := o.supervisor.EvaluateStage(ctx, s, o.opts.GlobalPrompt)
    if err != nil {
        log.Printf("supervisor: fallback for stage %s: %v", s.ID, err)
        return base
    }
    if decision.CanExecuteAutonomously {
        o.logSupervisorDecision(s.ID, "autonomous", decision.Reason)
        o.ui.Publish(Event{
            Type:    EventSupervisorDecision,
            StageID: s.ID,
            Data:    decision,
        })
        log.Printf("supervisor: stage %s → autonomous_execution. Reason: %s", s.ID, decision.Reason)
        return []string{"autonomous_execution"}
    }
    o.logSupervisorDecision(s.ID, "standard", decision.Reason)
    return base
}
```

Добавить `"encoding/json"` в imports orchestrator.go — его там НЕТ, нужно добавить явно в блок stdlib imports:
```go
import (
    "context"
    "encoding/json"   // ADD
    "fmt"
    "log"
    ...
)
```

- [ ] **Step 3: Добавить `checkAutonomousCompletion` в `pkg/orchestrator/completion.go`**

```go
// checkAutonomousCompletion проверяет что execution_summary.md существует и не пуст.
// Используется как completion-check для runAutonomousAgent.
func checkAutonomousCompletion(stageDir string) error {
    data, err := os.ReadFile(filepath.Join(stageDir, "execution_summary.md"))
    if err != nil {
        return &IncompleteWorkError{Reason: "missing execution_summary.md"}
    }
    if len(strings.TrimSpace(string(data))) == 0 {
        return &IncompleteWorkError{Reason: "execution_summary.md is empty"}
    }
    return nil
}
```

Убедиться что `"strings"` импортирован в completion.go.

- [ ] **Step 4: Добавить `runAutonomousAgent` в `orchestrator.go`** (после `runImplementationAgent`)

```go
// runAutonomousAgent выполняет стадию в автономном треке — без plan.md и approval.
// Агент использует прикреплённые скиллы и пишет execution_summary.md по завершении.
func (o *Orchestrator) runAutonomousAgent(ctx context.Context, s flow.Stage) {
    stageDir := filepath.Join(o.opts.RunDir, s.ID)

    o.runWithRetry(ctx, s, phaseImplementation, func(retryContext string) error {
        artCtx, artErr := CollectArtifacts(".", o.opts.RunDir, s, o.opts.Stages)
        if artErr != nil {
            log.Printf("WARN: collect artifacts for %s autonomous: %v", s.ID, artErr)
        }
        depCtx := CollectDependencyPlans(o.opts.RunDir, s, o.opts.Stages)

        summaryNote := fmt.Sprintf("\n\nStage directory: %s\nWrite execution_summary.md here when done.", stageDir)
        prompt := prompts.Build(prompts.Inputs{
            Template:        o.opts.Prompts.Implementation, // fallback если Autonomous пустой
            Autonomous:      o.opts.Prompts.Autonomous,
            Stage:           s,
            PhaseAgent:      prompts.AgentAutonomous,
            Artifacts:       artCtx,
            DependencyPlans: depCtx,
            StageDir:        stageDir,
            GlobalPrompt:    o.opts.GlobalPrompt,
            RetryContext:    retryContext + summaryNote,
        })
        logFile := filepath.Join(stageDir, "autonomous.log")
        r := o.runnerFor(s, phaseImplementation)
        return r.RunAgent(ctx, "autonomous_execution", s.Name, prompt, logFile)
    }, func() error {
        return checkAutonomousCompletion(stageDir)
    })
}
```

- [ ] **Step 5: Проверить что проект компилируется**
```bash
go build ./...
```

- [ ] **Step 6: Написать unit-тест DetermineStagePhases в `pkg/orchestrator/supervisor_orchestrator_test.go`**

```go
package orchestrator

import (
    "context"
    "os"
    "path/filepath"
    "testing"

    "github.com/akopichin/afm/pkg/config"
    "github.com/akopichin/afm/pkg/executor"
    "github.com/akopichin/afm/pkg/flow"
    "github.com/akopichin/afm/pkg/state"
)

func newOrchWithSupervisorRunner(t *testing.T, stages []flow.Stage, supervisorRunner executor.Runner) *Orchestrator {
    t.Helper()
    runDir := t.TempDir()
    ids := make([]string, len(stages))
    for i, s := range stages {
        ids[i] = s.ID
    }
    store, err := state.Open(runDir, ids)
    if err != nil {
        t.Fatal(err)
    }
    t.Cleanup(func() { store.Close() })
    return New(Options{
        RunDir:          runDir,
        Stages:          stages,
        Store:           store,
        Config:          config.Default(),
        Prompts:         DefaultPrompts(),
        SupervisorRunner: supervisorRunner,
    })
}

func TestDetermineStagePhases_Disabled(t *testing.T) {
    stage := flow.Stage{
        ID:         "s1",
        Supervisor: false,
        Agents:     []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation},
    }
    orch := newOrchWithSupervisorRunner(t, []flow.Stage{stage}, nil)
    phases := orch.DetermineStagePhases(context.Background(), stage)
    if len(phases) != 2 || phases[0] != "planning" {
        t.Errorf("expected base phases, got %v", phases)
    }
}

func TestDetermineStagePhases_InlineArtifactGuard(t *testing.T) {
    inlineTrue := true
    stage := flow.Stage{
        ID:         "s1",
        Supervisor: true,
        Agents:     []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation},
        Artifacts:  []flow.Artifact{{Name: "spec", Path: "./spec.md", Inline: &inlineTrue}},
    }
    // Runner вернул бы автономное решение, но guard должен его блокировать
    runner := &mockJSONRunner{
        response: []byte(`{"can_execute_autonomously":true,"reason":"x","recommended_phases":["autonomous_execution"]}`),
    }
    orch := newOrchWithSupervisorRunner(t, []flow.Stage{stage}, runner)
    phases := orch.DetermineStagePhases(context.Background(), stage)
    if len(phases) != 2 || phases[0] != "planning" {
        t.Errorf("inline guard failed: expected base phases, got %v", phases)
    }
}

func TestDetermineStagePhases_Autonomous(t *testing.T) {
    stage := flow.Stage{
        ID:         "s1",
        Supervisor: true,
        Agents:     []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation},
        Skills:     []string{"goga:apply"},
    }
    runner := &mockJSONRunner{
        response: []byte(`{"can_execute_autonomously":true,"reason":"skill handles it","recommended_phases":["autonomous_execution"]}`),
    }
    orch := newOrchWithSupervisorRunner(t, []flow.Stage{stage}, runner)
    phases := orch.DetermineStagePhases(context.Background(), stage)
    if len(phases) != 1 || phases[0] != "autonomous_execution" {
        t.Errorf("expected autonomous, got %v", phases)
    }
    // supervisor.jsonl должен появиться
    logPath := filepath.Join(orch.opts.RunDir, "supervisor.jsonl")
    if _, err := os.Stat(logPath); err != nil {
        t.Errorf("supervisor.jsonl not written: %v", err)
    }
}

func TestDetermineStagePhases_SupervisorError_Fallback(t *testing.T) {
    stage := flow.Stage{
        ID:         "s1",
        Supervisor: true,
        Agents:     []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation},
    }
    runner := &mockJSONRunner{err: os.ErrNotExist}
    orch := newOrchWithSupervisorRunner(t, []flow.Stage{stage}, runner)
    phases := orch.DetermineStagePhases(context.Background(), stage)
    if len(phases) != 2 || phases[0] != "planning" {
        t.Errorf("expected fallback to base phases, got %v", phases)
    }
}
```

- [ ] **Step 7: Запустить тесты**
```bash
go test ./pkg/orchestrator/... -run "TestDetermineStagePhases_" -v
```
Ожидаем: PASS.

- [ ] **Step 8: Линт**
```bash
go vet ./pkg/orchestrator/...
```

- [ ] **Step 9: Коммит**
```bash
git add pkg/orchestrator/orchestrator.go pkg/orchestrator/completion.go pkg/orchestrator/supervisor_orchestrator_test.go
git commit -m "feat(orchestrator): DetermineStagePhases, runAutonomousAgent, logSupervisorDecision"
```

---

### Task 8: Интеграция в `startPlanningForUnblocked/Pending` + resume для автономных стадий

**Files:**
- Modify: `pkg/orchestrator/orchestrator.go` (функция `startPlanningForUnblocked`)
- Modify: `pkg/orchestrator/recovery.go` (функция `startPlanningForPending`, case `StatusRunning`)

**Interfaces:**
- Consumes: `DetermineStagePhases` (Task 7), `runAutonomousAgent` (Task 7), `EvSupervisorApproved` (Task 4), `isAutonomousStage` (Task 7)

- [ ] **Step 1: Обновить `startPlanningForUnblocked` в `orchestrator.go`**

Найти текущий цикл в `startPlanningForUnblocked`. Заменить тело горутины:

**Было:**
```go
go func(st flow.Stage) {
    sem := o.semFor(st)
    sem.acquire()
    o.markAgentActive(st.ID)
    defer func() {
        o.markAgentDone(st.ID)
        sem.release()
    }()
    o.runPlanningAgent(ctx, st)
}(s)
```

**Стало:**
```go
go func(st flow.Stage) {
    sem := o.semFor(st)
    sem.acquire()
    o.markAgentActive(st.ID)
    defer func() {
        o.markAgentDone(st.ID)
        sem.release()
    }()

    phases := o.DetermineStagePhases(ctx, st)
    if len(phases) == 1 && phases[0] == "autonomous_execution" {
        stageDir := filepath.Join(o.opts.RunDir, st.ID)
        if err := os.MkdirAll(stageDir, 0755); err != nil {
            o.Trigger(st.ID, EvFail, GuardCtx{}, "mkdir failed")
            return
        }
        _ = os.WriteFile(filepath.Join(stageDir, "autonomous.flag"), nil, 0644)
        o.Trigger(st.ID, EvSupervisorApproved, GuardCtx{}, "supervisor: autonomous")
        o.Trigger(st.ID, EvStartRun, GuardCtx{}, "")
        o.runAutonomousAgent(ctx, st)
    } else {
        o.runPlanningAgent(ctx, st)
    }
}(s)
```

- [ ] **Step 2: Обновить `startPlanningForPending` в `recovery.go` — аналогичная горутина**

В функции `startPlanningForPending`, найти блок `default:` в конце (примерно строки 122–147):

```go
o.Trigger(s.ID, EvStartPlanning, GuardCtx{}, "")
go func(stage flow.Stage) {
    sem := o.semFor(stage)
    sem.acquire()
    o.markAgentActive(stage.ID)
    defer func() {
        o.markAgentDone(stage.ID)
        sem.release()
    }()
    o.runPlanningAgent(ctx, stage)
}(s)
```

Заменить тело горутины аналогично Task 8 Step 1 (DetermineStagePhases → ветвление).

- [ ] **Step 3: Добавить resume для `StatusRunning` автономных стадий в `recovery.go`**

В case `state.StatusRunning` (примерно строки 104–121), в начало блока вставить:

```go
case state.StatusRunning:
    stageDir := filepath.Join(o.opts.RunDir, s.ID)
    // Автономный трек: проверяем autonomous.flag и execution_summary.md
    if isAutonomousStage(stageDir) {
        if checkAutonomousCompletion(stageDir) == nil {
            o.Trigger(s.ID, EvComplete, GuardCtx{}, "recovered execution_summary.md")
            continue
        }
        go func(st flow.Stage) {
            sem := o.semFor(st)
            sem.acquire()
            o.markAgentActive(st.ID)
            defer func() {
                o.markAgentDone(st.ID)
                sem.release()
            }()
            o.runAutonomousAgent(ctx, st)
        }(s)
        continue
    }
    // Стандартный трек (существующий код без изменений)
    if err := checkCompletion(stageDir, ".", s); err == nil {
        // ... (оставить как есть)
```

- [ ] **Step 4: Проверить компиляцию**
```bash
go build ./...
```

- [ ] **Step 5: Запустить все тесты оркестратора**
```bash
go test ./pkg/orchestrator/... -timeout 60s -v 2>&1 | tail -30
```
Ожидаем: все существующие тесты PASS, новые тесты PASS.

- [ ] **Step 6: Линт**
```bash
go vet ./pkg/orchestrator/...
```

- [ ] **Step 7: Коммит**
```bash
git add pkg/orchestrator/orchestrator.go pkg/orchestrator/recovery.go
git commit -m "feat(orchestrator): интеграция DetermineStagePhases в startPlanning + resume автономных стадий"
```

---

### Task 9: Wiring в `cmd/afm/run.go`

**Files:**
- Modify: `cmd/afm/run.go:175-185` (orchestrator.New call), `cmd/afm/run.go:388-404` (loadPrompts)

**Interfaces:**
- Consumes: `flow.Flow.SupervisorCommand` (Task 1), `config.Config.Supervisor` (Task 1), `executor.New` (Task 2), `orchestrator.Options.SupervisorRunner` (Task 7)

- [ ] **Step 1: Обновить `loadPrompts` в `run.go`**

Добавить `"autonomous.md"` в список имён:
```go
func loadPrompts(overrideDir string) (orchestrator.Prompts, error) {
    names := []string{"planning.md", "implementation.md", "review.md", "summary.md", "autonomous.md"}
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
        Autonomous:     texts[4],
    }, nil
}
```

- [ ] **Step 2: Добавить инициализацию `SupervisorRunner` перед `orchestrator.New`**

После блока с `wrapperDir` и перед `orch := orchestrator.New(...)`:

```go
// Resolve supervisor command: flow.yaml > config.supervisor.command > config.client.command
supervisorCmd := f.SupervisorCommand
if supervisorCmd == "" {
    supervisorCmd = cfg.Supervisor.Command
}
if supervisorCmd == "" {
    supervisorCmd = cfg.Client.Command
}
supervisorWrapperDir := ""
if generatedAgents[supervisorCmd] {
    supervisorWrapperDir = wrapperDir
}
supervisorRunner := executor.New(executor.Config{
    Command:    supervisorCmd,
    WrapperDir: supervisorWrapperDir,
})
```

- [ ] **Step 3: Добавить `SupervisorRunner` в `orchestrator.New` вызов**

```go
orch := orchestrator.New(orchestrator.Options{
    RunDir:           runDir,
    Stages:           f.Stages,
    Store:            store,
    Config:           cfg,
    Prompts:          prompts,
    WrapperDir:       wrapperDir,
    GeneratedAgents:  generatedAgents,
    GlobalPrompt:     f.Prompt,
    RequireApproval:  requireApproval,
    SupervisorRunner: supervisorRunner,  // NEW
})
```

Добавить импорт `executor` если ещё не импортирован (проверить — он уже должен быть через `"github.com/akopichin/afm/pkg/executor"`).

- [ ] **Step 4: Проверить компиляцию всего проекта**
```bash
go build ./...
```

- [ ] **Step 5: Запустить все тесты**
```bash
go test ./... -timeout 120s 2>&1 | tail -20
```
Ожидаем: PASS.

- [ ] **Step 6: Линт**
```bash
go vet ./...
```

- [ ] **Step 7: Коммит**
```bash
git add cmd/afm/run.go
git commit -m "feat(run): инициализация SupervisorRunner + загрузка промпта autonomous.md"
```

---

### Task 10: Интеграционный тест автономного флоу

**Files:**
- Create: `pkg/orchestrator/integration_supervisor_test.go`

**Interfaces:**
- Consumes: всё из Tasks 1–9

- [ ] **Step 1: Создать `pkg/orchestrator/integration_supervisor_test.go`**

```go
package orchestrator_test

import (
    "context"
    "os"
    "path/filepath"
    "strings"
    "testing"
    "time"

    "github.com/akopichin/afm/pkg/config"
    "github.com/akopichin/afm/pkg/executor"
    "github.com/akopichin/afm/pkg/flow"
    "github.com/akopichin/afm/pkg/orchestrator"
    "github.com/akopichin/afm/pkg/state"
)

// mockSupervisorRunner реализует executor.Runner.
// RunJSONQuery возвращает настраиваемый JSON; RunAgent пишет execution_summary.md.
type mockSupervisorRunner struct {
    supervisorJSON []byte
    agentScript    string
}

func (m *mockSupervisorRunner) RunJSONQuery(_ context.Context, _ string) ([]byte, error) {
    return m.supervisorJSON, nil
}
func (m *mockSupervisorRunner) RunPlanning(_ context.Context, _, _, _, _ string) error { return nil }
func (m *mockSupervisorRunner) RunAgent(_ context.Context, _, _, _, _ string) error    { return nil }

func setupSupervisorOrch(t *testing.T, stages []flow.Stage, supJSON []byte, agentRunner executor.Runner) (*orchestrator.Orchestrator, string) {
    t.Helper()
    runDir := t.TempDir()
    ids := make([]string, len(stages))
    for i, s := range stages {
        ids[i] = s.ID
    }
    store, err := state.Open(runDir, ids)
    if err != nil {
        t.Fatal(err)
    }
    t.Cleanup(func() { store.Close() })

    supRunner := &mockSupervisorRunner{supervisorJSON: supJSON}

    orch := orchestrator.New(orchestrator.Options{
        RunDir:           runDir,
        Stages:           stages,
        Store:            store,
        Config:           config.Default(),
        Prompts:          orchestrator.DefaultPrompts(),
        Runner:           agentRunner,
        SupervisorRunner: supRunner,
    })
    return orch, runDir
}

// TestIntegration_SupervisorAutonomous проверяет что стадия с supervisor:true
// и автономным решением пропускает planning/approval и завершается через
// execution_summary.md.
func TestIntegration_SupervisorAutonomous(t *testing.T) {
    supJSON := []byte(`{"can_execute_autonomously":true,"reason":"skill handles it","recommended_phases":["autonomous_execution"]}`)

    // Агент пишет execution_summary.md при запуске
    autonomousScript := `
set -e
mkdir -p "$AFM_STAGE_DIR"
cat > "$AFM_STAGE_DIR/execution_summary.md" << 'EOF'
## Summary
Executed autonomously.
## Changes
- some_file.go
## Result
Success.
EOF
echo '{"type":"result","subtype":"success"}'
`
    agentRunner := executor.New(executor.Config{
        Command:     "bash",
        ExtraArgs:   []string{"-c", autonomousScript},
        IdleTimeout: 15 * time.Second,
    })

    stages := []flow.Stage{
        {
            ID:          "auto-stage",
            Description: "run goga:apply autonomously",
            Supervisor:  true,
            Agents:      []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation},
            Skills:      []string{"goga:apply"},
        },
    }

    orch, runDir := setupSupervisorOrch(t, stages, supJSON, agentRunner)

    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()

    if err := orch.Run(ctx); err != nil && err != context.DeadlineExceeded {
        t.Fatalf("Run: %v", err)
    }

    // 1. Стадия должна быть в статусе done
    store := orchestrator.StoreFromOrch(orch) // хелпер для тестов (см. ниже)
    if st := store.Get("auto-stage"); st != state.StatusDone {
        t.Errorf("expected done, got %s", st)
    }

    // 2. autonomous.flag должен существовать
    stageDir := filepath.Join(runDir, "auto-stage")
    if _, err := os.Stat(filepath.Join(stageDir, "autonomous.flag")); err != nil {
        t.Errorf("autonomous.flag missing: %v", err)
    }

    // 3. execution_summary.md должен существовать
    data, err := os.ReadFile(filepath.Join(stageDir, "execution_summary.md"))
    if err != nil {
        t.Fatalf("execution_summary.md missing: %v", err)
    }
    if !strings.Contains(string(data), "Executed autonomously") {
        t.Errorf("unexpected summary content: %s", data)
    }

    // 4. plan.md НЕ должен существовать (planning пропущен)
    if _, err := os.Stat(filepath.Join(stageDir, "plan.md")); err == nil {
        t.Error("plan.md should not exist for autonomous stage")
    }

    // 5. supervisor.jsonl должен содержать запись
    logData, err := os.ReadFile(filepath.Join(runDir, "supervisor.jsonl"))
    if err != nil {
        t.Fatalf("supervisor.jsonl missing: %v", err)
    }
    if !strings.Contains(string(logData), "autonomous") {
        t.Errorf("supervisor.jsonl should contain decision, got: %s", logData)
    }
}

// TestIntegration_SupervisorStandard проверяет что при стандартном решении
// стадия идёт обычным путём: autonomous.flag не создаётся, plan.md появляется.
// Тест использует короткий таймаут — нас интересует только что planning запустился.
func TestIntegration_SupervisorStandard(t *testing.T) {
    supJSON := []byte(`{"can_execute_autonomously":false,"reason":"needs planning","recommended_phases":["planning","implementation"]}`)

    // Скрипт пишет plan.md (planning) и .done (implementation) — полный цикл
    const planAndImplScript = `
set -e
mkdir -p "$AFM_STAGE_DIR"
# Если вызван как planning (outFile передаётся как $1 через RunPlanning):
# RunPlanning запускает с аргументом outFile, RunAgent — без него.
# Определяем фазу по наличию аргумента.
if [ -n "$1" ]; then
  # planning: пишем plan.md в $1
  cat > "$1" << 'EOF'
## Tasks
- [ ] step 1
## Assumptions
- none
## Acceptance Criteria
- [ ] done
EOF
else
  # implementation: пишем .done
  touch "$AFM_STAGE_DIR/.done"
fi
echo '{"type":"result","subtype":"success"}'
`
    agentRunner := executor.New(executor.Config{
        Command:     "bash",
        ExtraArgs:   []string{"-c", planAndImplScript},
        IdleTimeout: 15 * time.Second,
    })

    stages := []flow.Stage{
        {
            ID:          "std-stage",
            Description: "standard flow",
            Supervisor:  true,
            Agents:      []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation},
        },
    }

    orch, runDir := setupSupervisorOrch(t, stages, supJSON, agentRunner)
    cancel := autoApprove(orch)
    defer cancel()

    ctx, cancelCtx := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancelCtx()

    if err := orch.Run(ctx); err != nil && err != context.DeadlineExceeded {
        t.Fatalf("Run: %v", err)
    }

    stageDir := filepath.Join(runDir, "std-stage")

    // plan.md должен существовать (planning не пропущен)
    if _, err := os.Stat(filepath.Join(stageDir, "plan.md")); err != nil {
        t.Errorf("plan.md should exist for standard stage: %v", err)
    }
    // autonomous.flag НЕ должен существовать
    if _, err := os.Stat(filepath.Join(stageDir, "autonomous.flag")); err == nil {
        t.Error("autonomous.flag should not exist for standard stage")
    }
}
```

**Примечание:** `orchestrator.StoreFromOrch` — нужен экспортируемый хелпер для тестов. Добавить в `orchestrator.go` или `orchestrator_test.go` пакета orchestrator (package-internal тесты видят `o.opts.Store`). Для integration_supervisor_test.go который в `package orchestrator_test`, добавить в `orchestrator.go`:

```go
// StoreFromOrch возвращает Store оркестратора. Только для тестов.
func StoreFromOrch(o *Orchestrator) *state.Store { return o.opts.Store }
```

Добавить этот хелпер в конец `pkg/orchestrator/orchestrator.go` (Task 9 модифицирует этот же файл — можно добавить там). Он нужен потому что `integration_supervisor_test.go` находится в `package orchestrator_test` (внешний пакет) и не имеет прямого доступа к `o.opts.Store`.

- [ ] **Step 2: Запустить интеграционный тест**
```bash
go test ./pkg/orchestrator/... -run "TestIntegration_Supervisor" -v -timeout 60s
```
Ожидаем: PASS.

- [ ] **Step 3: Запустить все тесты**
```bash
go test ./... -timeout 120s 2>&1 | grep -E "FAIL|ok|---"
```
Ожидаем: все PASS.

- [ ] **Step 4: Финальный линт**
```bash
go vet ./...
```

- [ ] **Step 5: Финальный коммит**
```bash
git add pkg/orchestrator/integration_supervisor_test.go pkg/orchestrator/orchestrator.go
git commit -m "test(orchestrator): интеграционный тест автономного флоу через Supervisor"
```
