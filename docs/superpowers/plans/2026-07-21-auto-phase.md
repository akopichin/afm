# Auto-phase (`agents: [auto]`) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Разрешить `agents: [auto]` в YAML стейджа — стадия исполняется автономным агентом напрямую (без supervisor/LLM-решения и без фолбэка).

**Architecture:** `auto` — зарезервированное значение `AgentType` в `pkg/flow`. `Stage.IsAuto()` детектит его; хелперы агентов коротко замыкают auto (не путать с custom-implementation); `ParseFile.validate` fail-fast отвергает конфликты. В оркестраторе auto-стадия активируется в `tryActivatePrePlanned` (пишет `autonomous.flag` + `EvReady`, БЕЗ копирования plan.md), после чего существующая ветка `startReadyStages` (`isAutonomousStage → runAutonomousAgent`) запускает автономный агент. `DetermineStagePhases` для auto не вызывается.

**Tech Stack:** Go. `pkg/flow`, `pkg/orchestrator`. TDD, стандартный `testing`.

## Global Constraints
- НЕ менять версию Go в go.mod.
- Коммиты на русском языке, без Co-Authored-By.
- Линт: `go vet ./...` + `go build ./...`; тесты `go test -count=1 ./pkg/flow/... ./pkg/orchestrator/...`.
- Обратная совместимость: `auto` аддитивен, существующие flow не затронуты.
- НЕ трогать `accept.yaml`.

---

### Task 1: flow-модель — AgentAuto, IsAuto, короткие замыкания, валидация

**Files:**
- Modify: `pkg/flow/flow.go`
- Test: `pkg/flow/auto_test.go` (создать)

**Interfaces:**
- Produces: `const AgentAuto AgentType = "auto"`; `func (s *Stage) IsAuto() bool`; guarded `HasAgent`/`ImplAgent`; validation в `(*Flow).validate`.

- [ ] **Step 1: Написать падающие тесты**

Создать `pkg/flow/auto_test.go`:

```go
package flow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsAuto(t *testing.T) {
	if !(&Stage{Agents: []AgentType{AgentAuto}}).IsAuto() {
		t.Error("[auto] should be IsAuto")
	}
	for _, s := range []*Stage{
		{Agents: []AgentType{AgentPlanning}},
		{Agents: []AgentType{AgentAuto, AgentPlanning}},
		{Agents: nil},
	} {
		if s.IsAuto() {
			t.Errorf("%v should not be IsAuto", s.Agents)
		}
	}
}

func TestAutoStage_NoPlanningNoImplAgent(t *testing.T) {
	s := &Stage{Agents: []AgentType{AgentAuto}}
	if s.NeedsPlanning() {
		t.Error("auto stage must not need planning")
	}
	if s.HasAgent(AgentPlanning) || s.HasAgent(AgentImplementation) || s.HasAgent(AgentReview) {
		t.Error("auto stage must not report having planning/implementation/review agents")
	}
	if s.ImplAgent() == AgentAuto {
		t.Error("ImplAgent must never return the literal auto (would be used as a command)")
	}
}

// writeFlow — хелпер: пишет YAML во временный файл и парсит.
func writeFlow(t *testing.T, yaml string) (*Flow, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "flow.yaml")
	if err := os.WriteFile(p, []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}
	return ParseFile(p)
}

func TestParse_AutoStageValid(t *testing.T) {
	_, err := writeFlow(t, `
name: f
stages:
  - id: a
    agents: [auto]
`)
	if err != nil {
		t.Fatalf("valid [auto] stage rejected: %v", err)
	}
}

func TestParse_AutoMustBeSoleAgent(t *testing.T) {
	_, err := writeFlow(t, `
name: f
stages:
  - id: a
    agents: [auto, planning]
`)
	if err == nil || !strings.Contains(err.Error(), "only agent") {
		t.Fatalf("auto+planning: want 'only agent' error, got %v", err)
	}
}

func TestParse_AutoIncompatibleWithSupervisor(t *testing.T) {
	_, err := writeFlow(t, `
name: f
stages:
  - id: a
    agents: [auto]
    supervisor: true
`)
	if err == nil || !strings.Contains(err.Error(), "supervisor") {
		t.Fatalf("auto+supervisor: want 'supervisor' error, got %v", err)
	}
}
```

- [ ] **Step 2: Прогнать — падает**

Run: `go test ./pkg/flow/ -run 'TestIsAuto|TestAutoStage|TestParse_Auto' -v`
Expected: FAIL — `undefined: AgentAuto` / `IsAuto` (не компилируется).

- [ ] **Step 3: Добавить константу и `IsAuto`**

В `pkg/flow/flow.go` в блок констант агентов добавить `AgentAuto`:

```go
const (
	AgentPlanning       AgentType = "planning"
	AgentImplementation AgentType = "implementation"
	AgentReview         AgentType = "review"
	// AgentAuto — псевдо-агент: стадия исполняется автономным агентом напрямую,
	// без supervisor/LLM-решения и без фолбэка. Должен быть единственным агентом.
	AgentAuto AgentType = "auto"
)
```

Добавить метод (рядом с `HasAgent`/`NeedsPlanning`):

```go
// IsAuto сообщает, что стадия жёстко помечена автономной (agents: [auto]).
func (s *Stage) IsAuto() bool {
	return len(s.Agents) == 1 && s.Agents[0] == AgentAuto
}
```

- [ ] **Step 4: Короткие замыкания в `HasAgent` и `ImplAgent`**

`auto` не built-in, поэтому без guard `HasAgent(AgentImplementation)` вернул бы true (ветка custom-агента), а `ImplAgent()` вернул бы `"auto"` как команду. Добавить guard в НАЧАЛО обоих методов:

В `HasAgent`, первой строкой тела:
```go
	if s.IsAuto() {
		return false // auto-стадия не имеет planning/implementation/review-агентов
	}
```
В `ImplAgent`, первой строкой тела:
```go
	if s.IsAuto() {
		return AgentImplementation // defensive: auto не исполняется как implementation-команда
	}
```
(`NeedsPlanning` менять не нужно: `s.Plan=="" && s.HasAgent(AgentPlanning)` → `false` для auto автоматически после guard в `HasAgent`.)

- [ ] **Step 5: Валидация в `(*Flow).validate`**

Правка существующей проверки (строка ~175) — auto-стадия легитимно без planning/plan/interactive: добавить `&& !s.IsAuto()`:
```go
		if s.Plan == "" && !s.HasAgent(AgentPlanning) && !s.Interactive && !s.IsAuto() {
			return fmt.Errorf("stage %q: must have planning agent or a plan path", s.ID)
		}
```

Добавить новый цикл валидации auto (например, сразу после цикла проверки planning-agent, до `detectCycles`):
```go
	for _, s := range f.Stages {
		hasAuto := false
		for _, a := range s.Agents {
			if a == AgentAuto {
				hasAuto = true
				break
			}
		}
		if !hasAuto {
			continue
		}
		if len(s.Agents) != 1 {
			return fmt.Errorf("stage %q: \"auto\" must be the only agent", s.ID)
		}
		if s.Supervisor {
			return fmt.Errorf("stage %q: \"auto\" is incompatible with supervisor: true", s.ID)
		}
	}
```

- [ ] **Step 6: Зелёно**

Run: `go test ./pkg/flow/ -run 'TestIsAuto|TestAutoStage|TestParse_Auto' -v` → PASS
Run: `go build ./... && go vet ./pkg/flow/... && go test -count=1 ./pkg/flow/...` → всё ок.

- [ ] **Step 7: Коммит**

```bash
git add pkg/flow/flow.go pkg/flow/auto_test.go
git commit -m "feat(flow): agents: [auto] — модель и валидация автономной стадии

Константа AgentAuto + Stage.IsAuto(); HasAgent/ImplAgent коротко замыкают auto
(не путать с custom-implementation); ParseFile отвергает auto+другой-агент и
auto+supervisor:true; validate разрешает auto-стадию без planning/plan."
```

---

### Task 2: оркестратор — маршрутизация auto-стадии в автономный трек

**Files:**
- Modify: `pkg/orchestrator/scheduling.go` (`tryActivatePrePlanned`, `startReadyStages`)
- Test: `pkg/orchestrator/integration_supervisor_test.go` (дополнить — там уже есть mock-раннер и `setupSupervisorOrch`)

**Interfaces:**
- Consumes: `flow.Stage.IsAuto()` (Task 1), `isAutonomousStage`, `runAutonomousAgent`, `Trigger`/`EvReady`/`EvFail`, `spawnAgent` — существующие.

- [ ] **Step 1: Написать падающий интеграционный тест**

Добавить в `pkg/orchestrator/integration_supervisor_test.go` (использует существующие `setupSupervisorOrch` и `supervisorTestRunner`, чей `RunAgent` для `autonomous_execution` пишет `execution_summary.md`):

```go
// TestIntegration_AutoStageRunsAutonomousIgnoringSupervisor: стадия agents:[auto]
// исполняется автономно напрямую. Решение супервизора умышленно "НЕ автономно" —
// auto обязан его игнорировать (нет LLM, нет фолбэка на planning).
func TestIntegration_AutoStageRunsAutonomousIgnoringSupervisor(t *testing.T) {
	decision := []byte(`{"can_execute_autonomously":false,"reason":"no","recommended_phases":["planning","implementation"]}`)

	stages := []flow.Stage{
		{
			ID:          "auto-stage",
			Description: "hard autonomous",
			Agents:      []flow.AgentType{flow.AgentAuto},
		},
	}

	orch, runDir := setupSupervisorOrch(t, stages, decision)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := orch.Run(ctx); err != nil && err != context.DeadlineExceeded {
		t.Fatalf("orch.Run: %v", err)
	}

	stageDir := filepath.Join(runDir, "auto-stage")

	if st := orchestrator.StoreFromOrch(orch).Get("auto-stage"); st != state.StatusDone {
		t.Errorf("expected done, got %s", st)
	}
	if _, err := os.Stat(filepath.Join(stageDir, "autonomous.flag")); err != nil {
		t.Errorf("autonomous.flag missing: %v", err)
	}
	// autonomous-агент отработал (мок пишет execution_summary.md для autonomous_execution).
	if _, err := os.Stat(filepath.Join(stageDir, "execution_summary.md")); err != nil {
		t.Errorf("execution_summary.md missing — autonomous agent did not run: %v", err)
	}
	// planning пропущен → plan.md нет.
	if _, err := os.Stat(filepath.Join(stageDir, "plan.md")); err == nil {
		t.Error("plan.md should NOT exist for auto stage")
	}
}
```

- [ ] **Step 2: Прогнать — падает**

Run: `go test ./pkg/orchestrator/ -run TestIntegration_AutoStageRunsAutonomousIgnoringSupervisor -v`
Expected: FAIL — сейчас auto-стадия попадает в `tryActivatePrePlanned`, где `copyFile(resolvePlanSource(...), plan.md)` падает (плана нет) → `EvFail "copy plan failed"`, стадия не done / нет execution_summary.md.

- [ ] **Step 3: auto-branch в `tryActivatePrePlanned`**

В `pkg/orchestrator/scheduling.go`, в `tryActivatePrePlanned`, сразу ПОСЛЕ проверки `if !o.depsDone(s) { continue }` и ДО блока `stageDir := ...; MkdirAll; copyFile(plan.md)` вставить:

```go
		// auto-стадия: жёсткий автономный трек, без plan.md и supervisor. Пишем
		// autonomous.flag и переводим в Ready — дальше startReadyStages увидит флаг
		// и запустит runAutonomousAgent (как для supervisor-автономных стадий).
		if s.IsAuto() {
			autoDir := filepath.Join(o.opts.RunDir, s.ID)
			if err := os.MkdirAll(autoDir, 0755); err != nil {
				o.Trigger(s.ID, EvFail, GuardCtx{}, "mkdir failed")
				continue
			}
			_ = os.WriteFile(filepath.Join(autoDir, "autonomous.flag"), nil, 0644)
			o.Trigger(s.ID, EvReady, GuardCtx{}, "auto stage")
			continue
		}
```

- [ ] **Step 4: страховочное условие в `startReadyStages`**

В `startReadyStages` расширить ветку автономного запуска, чтобы auto-стадия попадала на автономный агент даже без флага (defensive). Заменить
```go
		if isAutonomousStage(filepath.Join(o.opts.RunDir, id)) {
			o.spawnAgent(ctx, *stage, o.runAutonomousAgent)
			continue
		}
```
на
```go
		if isAutonomousStage(filepath.Join(o.opts.RunDir, id)) || stage.IsAuto() {
			o.spawnAgent(ctx, *stage, o.runAutonomousAgent)
			continue
		}
```

- [ ] **Step 5: Зелёно + регрессия**

Run: `go test ./pkg/orchestrator/ -run TestIntegration_AutoStageRunsAutonomousIgnoringSupervisor -v` → PASS
Run: `go build ./... && go vet ./pkg/orchestrator/... && go test -count=1 ./pkg/orchestrator/...` → всё ок (существующие supervisor/autonomous/retry-тесты не задеты — auto-ветки срабатывают только при `IsAuto()`).

- [ ] **Step 6: Коммит**

```bash
git add pkg/orchestrator/scheduling.go pkg/orchestrator/integration_supervisor_test.go
git commit -m "feat(orchestrator): agents: [auto] запускает автономный трек напрямую

tryActivatePrePlanned для auto-стадии пишет autonomous.flag + EvReady (без
копирования plan.md), startReadyStages запускает runAutonomousAgent. Supervisor
и DetermineStagePhases не вызываются — жёсткий автономный трек, без фолбэков."
```

---

## Self-Review

**Spec coverage:**
- `agents: [auto]` синтаксис + `AgentAuto`/`IsAuto` → Task 1 Step 3. ✓
- Короткие замыкания (auto ≠ custom implementation) → Task 1 Step 4 (`HasAgent`/`ImplAgent`), `NeedsPlanning` автоматически. ✓
- Валидация fail-fast (auto-sole-agent, auto+supervisor) + фикс существующей проверки → Task 1 Step 5. ✓
- Маршрутизация в автономный трек через `tryActivatePrePlanned` + `startReadyStages`, без `DetermineStagePhases` → Task 2 Step 3-4. ✓
- Тесты (parse/model + интеграция: флаг есть, execution_summary есть, plan.md нет, супервизор проигнорирован) → Task 1/2 Step 1. ✓

**Placeholder scan:** код полный в каждом шаге; команды и ожидаемый вывод указаны; правки «вставить после строки X» сопровождаются точным кодом. ✓

**Type consistency:** `AgentAuto AgentType` (Task 1) используется в тестах и в `startReadyStages`/`tryActivatePrePlanned` через `stage.IsAuto()` (Task 2). `IsAuto()` определён в Task 1, потребляется в Task 2. Интеграционный тест использует уже существующие `setupSupervisorOrch`/`supervisorTestRunner`/`orchestrator.StoreFromOrch` (сигнатуры из integration_supervisor_test.go). ✓

**Порядок:** Task 1 (модель) → Task 2 (оркестратор, зависит от `IsAuto`). Последовательно.
