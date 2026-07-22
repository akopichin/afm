# S3 — Dedup autonomous-track & phase-slice helpers Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Устранить дублирование в `pkg/orchestrator`: (1) три ручных сборки списка «диалоговых фаз» → один хелпер `dialogPhases`; (2) байт-идентичный autonomous-track блок в scheduling+recovery → хелпер `startWithSupervisor`; (3) попутно починить молчаливый баг — при manual retry интерактивной autonomous-стадии её session/jsonl не очищались (список фаз в retryStage не включал autonomous, а очистка использовала `ph+".jsonl"` вместо `jsonlFileForPhase`).

**Architecture:** Три небольших извлечения хелперов в пакете `orchestrator`. Task 1 и 2 — поведение-сохраняющие дедупы (гарантируются существующими тестами + сборкой). Task 3 — целевой фикс поведения с прямым unit-тестом изолированного хелпера. TDD.

**Tech Stack:** Go, пакет `pkg/orchestrator`. Стандартный `testing`.

## Global Constraints
- НЕ менять версию Go в go.mod.
- Коммиты на русском языке, без Co-Authored-By.
- Линт: `go vet ./pkg/orchestrator/...` + `go build ./...`; тесты `go test -count=1 ./pkg/orchestrator/...`.
- Task 1 и 2 — БЕЗ изменения поведения (все существующие тесты зелёные). Task 3 — целевое изменение поведения, покрытое новым тестом.
- НЕ трогать `accept.yaml` (несвязанная незакоммиченная правка).

---

### Task 1: хелпер `dialogPhases` + дедуп двух сайтов в dialog_poller.go

**Files:**
- Modify: `pkg/orchestrator/dialog_poller.go` (добавить `dialogPhases`; заменить 2 ручные сборки на строках ~114-116 и ~163-165)
- Test: `pkg/orchestrator/dialog_poller_test.go` (существует — дополнить; если функция теста конфликтует, создать `phase_helpers_test.go`)

**Interfaces:**
- Consumes: `isAutonomousStage(stageDir string) bool`, консты `phasePlanning/phaseImplementation/phaseReview/phaseAutonomous` — существующие.
- Produces: `func dialogPhases(stageDir string) []string` — базовые planning/implementation/review + autonomous, если в stageDir есть autonomous.flag.

- [ ] **Step 1: Написать падающий тест**

Создать `pkg/orchestrator/phase_helpers_test.go`:

```go
package orchestrator

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestDialogPhases_BaseWithoutAutonomousFlag(t *testing.T) {
	dir := t.TempDir()
	got := dialogPhases(dir)
	want := []string{phasePlanning, phaseImplementation, phaseReview}
	if !slices.Equal(got, want) {
		t.Fatalf("dialogPhases (no flag) = %v, want %v", got, want)
	}
}

func TestDialogPhases_IncludesAutonomousWhenFlagPresent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "autonomous.flag"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	got := dialogPhases(dir)
	want := []string{phasePlanning, phaseImplementation, phaseReview, phaseAutonomous}
	if !slices.Equal(got, want) {
		t.Fatalf("dialogPhases (with flag) = %v, want %v", got, want)
	}
}
```

- [ ] **Step 2: Прогнать — падает**

Run: `go test ./pkg/orchestrator/ -run TestDialogPhases -v`
Expected: FAIL — `undefined: dialogPhases` (не компилируется).

- [ ] **Step 3: Реализовать `dialogPhases` в dialog_poller.go**

Добавить в `pkg/orchestrator/dialog_poller.go` (рядом с `jsonlFileForPhase`):

```go
// dialogPhases возвращает фазы, чьи диалоговые артефакты (session/jsonl/вопросы)
// относятся к стадии: базовые planning/implementation/review плюс autonomous,
// если стадия исполняется в автономном треке (в stageDir есть autonomous.flag).
// Единый источник для сканов, ранее собиравшихся вручную в нескольких местах.
func dialogPhases(stageDir string) []string {
	phases := []string{phasePlanning, phaseImplementation, phaseReview}
	if isAutonomousStage(stageDir) {
		phases = append(phases, phaseAutonomous)
	}
	return phases
}
```

- [ ] **Step 4: Заменить два ручных сайта на вызов хелпера**

В `pkg/orchestrator/dialog_poller.go` в `detectDialogViolation` (около строки 114) и `relocateMisplacedQuestions` (около строки 163) заменить блок
```go
	phases := []string{phasePlanning, phaseImplementation, phaseReview}
	if isAutonomousStage(stageDir) {
		phases = append(phases, phaseAutonomous)
	}
```
на
```go
	phases := dialogPhases(stageDir)
```
(в обоих местах — семантика идентична исходной).

- [ ] **Step 5: Зелёно + сборка**

Run: `go test ./pkg/orchestrator/ -run TestDialogPhases -v` → PASS
Run: `go build ./... && go vet ./pkg/orchestrator/... && go test -count=1 ./pkg/orchestrator/...` → всё ок (поведение сайтов не изменилось).

- [ ] **Step 6: Коммит**

```bash
git add pkg/orchestrator/dialog_poller.go pkg/orchestrator/phase_helpers_test.go
git commit -m "refactor(orchestrator): хелпер dialogPhases вместо ручных списков фаз

detectDialogViolation и relocateMisplacedQuestions собирали список
диалоговых фаз (base + autonomous-if-flag) вручную; вынесено в dialogPhases.
Поведение не изменилось."
```

---

### Task 2: хелпер `startWithSupervisor` + дедуп scheduling/recovery

**Files:**
- Modify: `pkg/orchestrator/supervisor_track.go` (добавить `startWithSupervisor`)
- Modify: `pkg/orchestrator/scheduling.go` (`startPlanningForUnblocked`, ~строки 76-91)
- Modify: `pkg/orchestrator/recovery.go` (~строки 120-135)

**Interfaces:**
- Consumes: `DetermineStagePhases`, `phaseAutonomous`, `Trigger`, `EvFail`/`EvSupervisorApproved`/`EvStartRun`, `runAutonomousAgent`, `runPlanningAgent`, `spawnAgent` — существующие.
- Produces: `func (o *Orchestrator) startWithSupervisor(ctx context.Context, s flow.Stage)` — тело = текущий anonymous-closure autonomous-track блок (байт-в-байт).

- [ ] **Step 1: Реализовать `startWithSupervisor`**

Добавить в `pkg/orchestrator/supervisor_track.go`:

```go
// startWithSupervisor — общая точка запуска стадии после решения супервизора:
// автономный трек (пишет autonomous.flag + durable-переходы + runAutonomousAgent)
// либо обычное планирование. Идентичный блок раньше дублировался в
// startPlanningForUnblocked (scheduling.go) и в recovery.go; извлечён сюда.
// Вызывается как agent-функция через spawnAgent.
func (o *Orchestrator) startWithSupervisor(ctx context.Context, s flow.Stage) {
	phases := o.DetermineStagePhases(ctx, s)
	if len(phases) == 1 && phases[0] == phaseAutonomous {
		stageDir := filepath.Join(o.opts.RunDir, s.ID)
		if err := os.MkdirAll(stageDir, 0755); err != nil {
			o.Trigger(s.ID, EvFail, GuardCtx{}, "mkdir failed")
			return
		}
		_ = os.WriteFile(filepath.Join(stageDir, "autonomous.flag"), nil, 0644)
		o.Trigger(s.ID, EvSupervisorApproved, GuardCtx{}, "supervisor: autonomous")
		o.Trigger(s.ID, EvStartRun, GuardCtx{}, "")
		o.runAutonomousAgent(ctx, s)
	} else {
		o.runPlanningAgent(ctx, s)
	}
}
```
(Проверить, что supervisor_track.go импортирует `os`, `path/filepath`, `context`, `flow` — добавить недостающее; компилятор подскажет.)

- [ ] **Step 2: Заменить closure в scheduling.go**

В `startPlanningForUnblocked` заменить вызов `o.spawnAgent(ctx, s, func(ctx context.Context, st flow.Stage) { … весь блок … })` (строки ~76-91) на:
```go
		o.spawnAgent(ctx, s, o.startWithSupervisor)
```

- [ ] **Step 3: Заменить closure в recovery.go**

В recovery.go заменить `o.spawnAgent(ctx, s, func(ctx context.Context, stage flow.Stage) { … тот же блок … })` (строки ~120-135) на:
```go
		o.spawnAgent(ctx, s, o.startWithSupervisor)
```

- [ ] **Step 4: Сборка + тесты (поведение сохранено)**

Run: `go build ./... && go vet ./pkg/orchestrator/... && go test -count=1 ./pkg/orchestrator/...`
Expected: всё ок. Тело `startWithSupervisor` идентично исходным closure — существующие тесты (в т.ч. integration_supervisor_test, integration_resume_test) — зелёные. Удалить импорты, ставшие неиспользуемыми в scheduling.go/recovery.go (если `os`/`filepath` больше нигде в файле не нужны — компилятор скажет).

- [ ] **Step 5: Коммит**

```bash
git add pkg/orchestrator/supervisor_track.go pkg/orchestrator/scheduling.go pkg/orchestrator/recovery.go
git commit -m "refactor(orchestrator): дедуп autonomous-track блока в startWithSupervisor

Идентичный блок «DetermineStagePhases → autonomous.flag + переходы +
runAutonomousAgent, иначе runPlanningAgent» дублировался в
startPlanningForUnblocked и recovery; извлечён в startWithSupervisor.
Поведение не изменилось."
```

---

### Task 3: `clearInteractiveSessions` + фикс очистки autonomous при retry

**Files:**
- Modify: `pkg/orchestrator/scheduling.go` (добавить `clearInteractiveSessions`; `retryStage` использует его вместо inline-цикла, ~строки 152-158)
- Test: `pkg/orchestrator/phase_helpers_test.go` (дополнить)

**Interfaces:**
- Consumes: `dialogPhases` (Task 1), `sessionFile(stageDir, phase string) string`, `jsonlFileForPhase(phase string) string` — существующие.
- Produces: `func clearInteractiveSessions(stageDir string)` — удаляет `<phase>.session.json` и обнуляет основной jsonl (`jsonlFileForPhase(phase)`) для каждой фазы из `dialogPhases(stageDir)`.

**Баг, который чинится:** inline-цикл в retryStage перебирал только `[]string{planning, implementation, review}` и обнулял `ph+".jsonl"`. Для интерактивной стадии, ушедшей в autonomous-трек, это (а) НЕ чистило `autonomous_execution.session.json` и (б) даже при добавлении autonomous обнулило бы несуществующий `autonomous_execution.jsonl` вместо реального `autonomous.jsonl`. Хелпер использует `dialogPhases` (добавляет autonomous по флагу) и `jsonlFileForPhase` (даёт `autonomous.jsonl`).

- [ ] **Step 1: Написать падающий тест**

Дополнить `pkg/orchestrator/phase_helpers_test.go`:

```go
func TestClearInteractiveSessions_ClearsAutonomousArtifacts(t *testing.T) {
	dir := t.TempDir()
	// Стадия в автономном треке.
	if err := os.WriteFile(filepath.Join(dir, "autonomous.flag"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	// Артефакты autonomous-фазы: session по имени фазы, лог — autonomous.jsonl.
	autoSession := filepath.Join(dir, phaseAutonomous+".session.json") // autonomous_execution.session.json
	autoJSONL := filepath.Join(dir, "autonomous.jsonl")
	if err := os.WriteFile(autoSession, []byte(`{"session":"x"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(autoJSONL, []byte("line\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// Плюс обычная planning-сессия, чтобы проверить базовый путь.
	planSession := filepath.Join(dir, phasePlanning+".session.json")
	if err := os.WriteFile(planSession, []byte(`{"session":"p"}`), 0644); err != nil {
		t.Fatal(err)
	}

	clearInteractiveSessions(dir)

	if _, err := os.Stat(autoSession); !os.IsNotExist(err) {
		t.Errorf("autonomous session должен быть удалён, err=%v", err)
	}
	if _, err := os.Stat(planSession); !os.IsNotExist(err) {
		t.Errorf("planning session должен быть удалён, err=%v", err)
	}
	if fi, err := os.Stat(autoJSONL); err != nil || fi.Size() != 0 {
		t.Errorf("autonomous.jsonl должен быть усечён до 0, size/err = %v/%v", func() int64 { if fi != nil { return fi.Size() }; return -1 }(), err)
	}
}
```

- [ ] **Step 2: Прогнать — падает**

Run: `go test ./pkg/orchestrator/ -run TestClearInteractiveSessions -v`
Expected: FAIL — `undefined: clearInteractiveSessions`.

- [ ] **Step 3: Реализовать `clearInteractiveSessions` в scheduling.go**

Добавить в `pkg/orchestrator/scheduling.go`:

```go
// clearInteractiveSessions удаляет claude-сессии и обнуляет stream-json логи всех
// диалоговых фаз стадии — вызывается при manual retry интерактивной стадии, чтобы
// не тянуть phantom-сессию и не перезапускать старый *.question.json. Использует
// dialogPhases (учитывает autonomous.flag) и jsonlFileForPhase (autonomous-фаза
// логируется в autonomous.jsonl, а не autonomous_execution.jsonl).
func clearInteractiveSessions(stageDir string) {
	for _, ph := range dialogPhases(stageDir) {
		_ = os.Remove(sessionFile(stageDir, ph))
		_ = os.Truncate(filepath.Join(stageDir, jsonlFileForPhase(ph)), 0)
	}
}
```

- [ ] **Step 4: retryStage вызывает хелпер**

В `retryStage` заменить inline-блок (строки ~152-158):
```go
	if stage.Interactive {
		stageDir := filepath.Join(o.opts.RunDir, stageID)
		for _, ph := range []string{phasePlanning, phaseImplementation, phaseReview} {
			_ = os.Remove(sessionFile(stageDir, ph))
			_ = os.Truncate(filepath.Join(stageDir, ph+".jsonl"), 0)
		}
	}
```
на:
```go
	if stage.Interactive {
		clearInteractiveSessions(filepath.Join(o.opts.RunDir, stageID))
	}
```

- [ ] **Step 5: Зелёно + сборка + регрессия retry**

Run: `go test ./pkg/orchestrator/ -run TestClearInteractiveSessions -v` → PASS
Run: `go build ./... && go vet ./pkg/orchestrator/... && go test -count=1 ./pkg/orchestrator/...` → всё ок (в т.ч. integration_retry_test — базовый путь planning/implementation/review сохранён: `jsonlFileForPhase(planning)="planning.jsonl"` совпадает со старым `ph+".jsonl"`).

- [ ] **Step 6: Коммит**

```bash
git add pkg/orchestrator/scheduling.go pkg/orchestrator/phase_helpers_test.go
git commit -m "fix(orchestrator): manual retry чистит и autonomous session/jsonl

retryStage перебирал только planning/implementation/review и обнулял
ph+.jsonl, из-за чего при retry интерактивной autonomous-стадии её
autonomous_execution.session.json и autonomous.jsonl не очищались (phantom-
сессия/зависший вопрос). Вынесено в clearInteractiveSessions на dialogPhases +
jsonlFileForPhase (autonomous.jsonl). Базовый путь без изменений."
```

---

## Self-Review

**Spec coverage (S3 = autonomous-track dedup + phase-slice helpers):**
- Дедуп ручных списков фаз → Task 1 (`dialogPhases`, 2 сайта). ✓
- Дедуп autonomous-track блока → Task 2 (`startWithSupervisor`, scheduling+recovery). ✓
- «retryStage забыл autonomous» (молчаливый баг) → Task 3 (`clearInteractiveSessions` + фикс jsonl-имени), с тестом. ✓
- detectInterruptedPhase (recovery.go:178) НАМЕРЕННО оставлен на base-3: он ищет прерванную *интерактивную* фазу для resumeInteractiveAgent (autonomous резюмится отдельной веткой recovery), autonomous туда не входит — не наш случай. (Зафиксировано, чтобы не «дедупнуть» по ошибке.)

**Placeholder scan:** код каждого шага полный; команды и ожидаемый вывод указаны. Прозаические указания («заменить блок X на Y») сопровождаются точным до/после. ✓

**Type consistency:** `dialogPhases(stageDir string) []string` (Task 1) потребляется в `clearInteractiveSessions` (Task 3). `startWithSupervisor(ctx, flow.Stage)` (Task 2) имеет сигнатуру agent-функции `func(context.Context, flow.Stage)`, совместимую со вторым аргументом `spawnAgent`. `clearInteractiveSessions(stageDir string)` — free-функция (не метод), использует только free-функции (`dialogPhases`/`sessionFile`/`jsonlFileForPhase`), поэтому напрямую unit-тестируема без Orchestrator. ✓

**Порядок:** Task 1 вводит `dialogPhases` (нужен Task 3). Task 2 независим. Выполнять 1→2→3 последовательно; каждая самостоятельно компилируется/тестируется.
