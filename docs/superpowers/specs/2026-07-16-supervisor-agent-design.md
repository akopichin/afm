# Supervisor Agent Design

**Date:** 2026-07-16  
**Branch:** supervisor  
**Status:** Approved

---

## 1. Цель

Добавить **Агента-Супервизора** — лёгкую LLM-модель, которая на старте стадии оценивает, достаточно ли прикреплённых скиллов для выполнения задачи «под ключ». Если да — движок схлопывает цепочку фаз до одного шага `autonomous_execution`, пропуская planning, approval и review.

---

## 2. Архитектурные ограничения

1. **Одностороннее сокращение.** Supervisor может только убирать фазы. Стадии с `supervisor: true` обязаны иметь `planning` в `agents` — YAML-валидация не меняется. Supervisor не может навязывать фазы, которых нет в `agents`.
2. **Go-эвристики до LLM-вызова.** Если стадия генерирует артефакты с `inline: true`, planning пропускать нельзя — движок возвращает базовые фазы без обращения к LLM.
3. **Фолбэк при ошибке.** Любая ошибка Supervisor (таймаут, плохой JSON, сеть) → базовые фазы. Стадия идёт стандартным путём.
4. **Персистентность трека.** Выбранный трек фиксируется флаг-файлом `autonomous.flag` до старта агента. Корректный resume при перезапуске.
5. **Контекст для зависимостей.** Автономная стадия пишет `execution_summary.md` вместо `plan.md`. Зависимые стадии получают его через `CollectDependencyPlans`.

---

## 3. Новые поля в YAML и конфиге

### `flow.yaml`

```yaml
name: my-flow
prompt: "Global context..."
supervisor_command: glm51     # команда для supervisor; default = client.command

stages:
  - id: my-stage
    supervisor: true                        # включить Supervisor
    supervisor_prompt: "Extra context"      # опц. дополнительный hint (высший приоритет в шаблоне)
    agents: [planning, implementation]      # ОБЯЗАН содержать planning
    skills: [goga:apply]
```

### `config.yaml`

```yaml
supervisor:
  command: claude    # global default; перекрывается supervisor_command в flow.yaml
```

**Приоритет команды:** `flow.SupervisorCommand` → `config.Supervisor.Command` → `config.Client.Command`.

### Go-структуры

```go
// pkg/flow/flow.go
type Stage struct {
    // ... existing ...
    Supervisor       bool   `yaml:"supervisor"`
    SupervisorPrompt string `yaml:"supervisor_prompt,omitempty"`
}

type Flow struct {
    // ... existing ...
    SupervisorCommand string `yaml:"supervisor_command,omitempty"`
}

// pkg/config/config.go
type SupervisorConfig struct {
    Command string `yaml:"command"`
}

type Config struct {
    // ... existing ...
    Supervisor SupervisorConfig `yaml:"supervisor"`
}
```

---

## 4. Executor: `RunJSONQuery`

Новый метод добавляется в интерфейс `Runner` и реализуется в `Executor`. Запускает supervisor-команду в режиме одного промпта и возвращает сырой JSON ответа. Не использует stream-json парсинг.

```go
// pkg/executor/runner.go
type Runner interface {
    RunPlanning(ctx context.Context, stageName, prompt, outFile, logFile string) error
    RunAgent(ctx context.Context, agentType, stageName, prompt, logFile string) error
    RunJSONQuery(ctx context.Context, prompt string) ([]byte, error)
}

// pkg/executor/executor.go
func (e *Executor) RunJSONQuery(ctx context.Context, prompt string) ([]byte, error) {
    args := []string{"-p", prompt, "--output-format", "json"}
    cmd := exec.CommandContext(ctx, e.command, args...)
    if e.wrapperDir != "" {
        cmd.Env = append(os.Environ(), "PATH="+e.wrapperDir+":"+os.Getenv("PATH"))
    }
    return cmd.Output()
}
```

autoShim работает прозрачно: если `supervisor_command` — generated-агент, `wrapperDir` прокидывается в PATH и враппер резолвится как обычно.

---

## 5. Компонент Supervisor (`pkg/orchestrator/supervisor.go`)

```go
type EvaluationResult struct {
    CanExecuteAutonomously bool     `json:"can_execute_autonomously"`
    Reason                 string   `json:"reason"`
    RecommendedPhases      []string `json:"recommended_phases"`
}

type Supervisor struct {
    runner executor.Runner
}

func NewSupervisor(r executor.Runner) *Supervisor {
    return &Supervisor{runner: r}
}

func (s *Supervisor) EvaluateStage(ctx context.Context, stage flow.Stage, globalPrompt string) (*EvaluationResult, error) {
    prompt := compileSupervisorPrompt(stage, globalPrompt) // text/template
    raw, err := s.runner.RunJSONQuery(ctx, prompt)
    if err != nil {
        return nil, err
    }
    var result EvaluationResult
    if err := json.Unmarshal(raw, &result); err != nil {
        return nil, fmt.Errorf("parse supervisor response: %w", err)
    }
    if err := validateDecision(&result, stage); err != nil {
        return nil, err
    }
    return &result, nil
}
```

**Валидация ответа (`validateDecision`):** `RecommendedPhases` должен быть либо `["autonomous_execution"]`, либо подмножеством `stage.Agents`. Ответ с неизвестными фазами отклоняется → фолбэк.

**Промпт-шаблон** — английский, через `text/template`. Включает секции:

- `<global_prompt>` — `flow.Prompt`
- Stage ID, Description, Skills, BasePhases
- `<local_supervisor_prompt>` — `stage.SupervisorPrompt` (высший приоритет)
- Constraint: вернуть строго JSON с полями `can_execute_autonomously`, `reason`, `recommended_phases`

---

## 6. Интеграция в оркестратор

### `Options` и `Orchestrator`

```go
type Options struct {
    // ... existing ...
    SupervisorRunner executor.Runner // nil = supervisor отключён
}

type Orchestrator struct {
    // ... existing ...
    supervisor *Supervisor // nil если SupervisorRunner == nil
}
```

В `New()`: если `opts.SupervisorRunner != nil`, создаём `NewSupervisor(opts.SupervisorRunner)`.

### `DetermineStagePhases`

Вызывается **внутри горутины** — не блокирует event loop:

```go
func (o *Orchestrator) DetermineStagePhases(ctx context.Context, s flow.Stage) []string {
    base := agentTypesToStrings(s.Agents)

    if !s.Supervisor || o.supervisor == nil {
        return base
    }
    for _, art := range s.Artifacts {
        if art.IsInline() {
            return base // inline-артефакт guard
        }
    }
    decision, err := o.supervisor.EvaluateStage(ctx, s, o.opts.GlobalPrompt)
    if err != nil {
        log.Printf("supervisor fallback for stage %s: %v", s.ID, err)
        return base
    }
    if decision.CanExecuteAutonomously {
        o.logSupervisorDecision(s.ID, decision.Reason)
        o.ui.Publish(Event{Type: EventSupervisorDecision, StageID: s.ID, Data: decision})
        return []string{"autonomous_execution"}
    }
    return base
}
```

### Изменения в `startPlanningForUnblocked` / `startPlanningForPending`

`EvStartPlanning` срабатывает синхронно как mutex (как сейчас). Внутри горутины — вызов `DetermineStagePhases`, затем ветвление:

```go
if _, ok := o.Trigger(s.ID, EvStartPlanning, GuardCtx{Stage: s}, "deps done"); !ok {
    continue
}
go func(st flow.Stage) {
    sem := o.semFor(st)
    sem.acquire()
    o.markAgentActive(st.ID)
    defer func() { o.markAgentDone(st.ID); sem.release() }()

    phases := o.DetermineStagePhases(ctx, st)
    if len(phases) == 1 && phases[0] == "autonomous_execution" {
        stageDir := filepath.Join(o.opts.RunDir, st.ID)
        _ = os.MkdirAll(stageDir, 0755)
        _ = os.WriteFile(filepath.Join(stageDir, "autonomous.flag"), nil, 0644)
        o.Trigger(st.ID, EvSupervisorApproved, GuardCtx{}, "")  // planning → ready
        o.Trigger(st.ID, EvStartRun, GuardCtx{}, "")             // ready → running
        o.runAutonomousAgent(ctx, st)
    } else {
        o.runPlanningAgent(ctx, st)
    }
}(s)
```

### Новый FSM-переход

```go
// pkg/orchestrator/fsm.go
{From: StatusPlanning, Event: EvSupervisorApproved, To: StatusReady}
```

---

## 7. `runAutonomousAgent`

Новая функция в `orchestrator.go`. Аналог `runImplementationAgent`, но без чтения `plan.md` — агент планирует и выполняет сам через прикреплённые скиллы.

```go
func (o *Orchestrator) runAutonomousAgent(ctx context.Context, s flow.Stage) {
    stageDir := filepath.Join(o.opts.RunDir, s.ID)

    o.runWithRetry(ctx, s, phaseImplementation, func(retryContext string) error {
        artCtx, _ := CollectArtifacts(".", o.opts.RunDir, s, o.opts.Stages)
        depCtx := CollectDependencyPlans(o.opts.RunDir, s, o.opts.Stages)

        prompt := prompts.Build(prompts.Inputs{
            Template:        o.opts.Prompts.Autonomous, // новый шаблон
            Stage:           s,
            PhaseAgent:      prompts.AgentAutonomous,
            Artifacts:       artCtx,
            DependencyPlans: depCtx,
            StageDir:        stageDir,
            GlobalPrompt:    o.opts.GlobalPrompt,
            RetryContext:    retryContext,
            // Plan намеренно пустой
        })
        logFile := filepath.Join(stageDir, "autonomous.log")
        r := o.runnerFor(s, phaseImplementation)
        return r.RunAgent(ctx, "autonomous_execution", s.Name, prompt, logFile)
    }, func() error {
        summaryPath := filepath.Join(stageDir, "execution_summary.md")
        if _, err := os.Stat(summaryPath); err != nil {
            return &IncompleteWorkError{Reason: "missing execution_summary.md"}
        }
        return nil
    })
}
```

**Промпт-шаблон `Autonomous`** инструктирует агента:
- Выполни задачу используя прикреплённые скиллы (`<skills>` тег уже в prompts.Build)
- Не жди approval — действуй автономно
- Запиши `execution_summary.md` в `$AFM_STAGE_DIR` по завершении (аналог `.done`)

---

## 8. `CollectDependencyPlans` — фолбэк на `execution_summary.md`

```go
// pkg/orchestrator/context.go
for _, depID := range stage.DependsOn {
    stageDir := filepath.Join(runDir, depID)
    name := nameIndex[depID]

    var data []byte
    autonomousFlag := filepath.Join(stageDir, "autonomous.flag")
    if _, err := os.Stat(autonomousFlag); err == nil {
        data, _ = os.ReadFile(filepath.Join(stageDir, "execution_summary.md"))
    } else {
        data, _ = os.ReadFile(filepath.Join(stageDir, "plan.md"))
    }

    fmt.Fprintf(&buf, "\n### Stage: %s (%s)\n\n", name, depID)
    if len(data) == 0 {
        buf.WriteString("(plan not available)\n")
        continue
    }
    buf.WriteString(string(data))
    buf.WriteString("\n")
}
```

---

## 9. Логирование и UI

### `supervisor.jsonl`

Файл `<runDir>/supervisor.jsonl` — append-only, по одной JSON-строке на решение:

```json
{"ts":"2026-07-16T10:00:00Z","stage_id":"my-stage","decision":"autonomous","reason":"Skill goga:apply handles this task end-to-end."}
{"ts":"2026-07-16T10:01:00Z","stage_id":"other-stage","decision":"standard","reason":"Stage produces inline artifact; planning required."}
```

### UIBus событие

```go
// pkg/orchestrator/bus.go
EventSupervisorDecision EventType = "supervisor_decision"
```

Payload: `EvaluationResult`. Дашборд получает по WebSocket и отображает в логах стадии (например, badge «⚡ autonomous» или «🤖 supervisor: standard»).

---

## 10. Affected files

| Файл | Изменение |
|------|-----------|
| `pkg/flow/flow.go` | + `Stage.Supervisor`, `Stage.SupervisorPrompt`, `Flow.SupervisorCommand` |
| `pkg/config/config.go` | + `SupervisorConfig`, `Config.Supervisor`; merge в `mergeFile` |
| `pkg/executor/runner.go` | + `RunJSONQuery` в интерфейс |
| `pkg/executor/executor.go` | Реализация `RunJSONQuery` |
| `pkg/orchestrator/supervisor.go` | Новый файл: `Supervisor`, `EvaluationResult`, промпт-шаблон |
| `pkg/orchestrator/orchestrator.go` | + `supervisor` поле, `DetermineStagePhases`, `agentTypesToStrings` helper, изменения в `startPlanningForUnblocked/Pending`, `runAutonomousAgent`, `logSupervisorDecision` |
| `pkg/orchestrator/fsm.go` | + `EvSupervisorApproved` переход `planning → ready` |
| `pkg/orchestrator/bus.go` | + `EventSupervisorDecision` |
| `pkg/orchestrator/context.go` | `CollectDependencyPlans` — фолбэк на `execution_summary.md` |
| `pkg/prompts/builder.go` | + `AgentAutonomous Agent = "autonomous_execution"`, поле `Autonomous string` в `Inputs`; передача в `<autonomous_execution>` тег |
| `pkg/orchestrator/orchestrator.go` (`Prompts` struct) | + поле `Autonomous string` (рядом с `Planning`, `Implementation`, `Review`, `Summary`) |
| `assets/` | + `autonomous.md` промпт-шаблон |
| `cmd/afm/run.go` | Инициализация `SupervisorRunner` из resolved supervisor command |

---

## 11. Тестирование

- **Unit:** `Supervisor.EvaluateStage` — мок `RunJSONQuery` возвращает разные JSON-ответы; проверяем фолбэк на ошибку, на невалидные фазы, на inline-артефакт guard.
- **Unit:** `DetermineStagePhases` — аналогично мок-раннером.
- **Integration:** новый тест типа `integration_test.go` — flow с `supervisor: true` стадией, мок-раннер возвращает `autonomous_execution`, проверяем что `autonomous.flag` создан, `execution_summary.md` ожидается completion-чеком, зависимая стадия получает правильный контекст.
- **FSM:** тест перехода `planning → ready` через `EvSupervisorApproved`.
