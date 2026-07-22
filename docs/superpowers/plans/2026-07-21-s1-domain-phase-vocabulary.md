# S1 — Domain Phase Vocabulary in pkg/flow Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Единый источник правды для доменного словаря фаз выполнения (`planning`/`implementation`/`review`/`autonomous_execution`), их валидации и отображения на лог-файлы — в листовом пакете `pkg/flow`, откуда его используют `orchestrator`, `server`, `mcp`, `prompts`.

**Architecture:** Ввести тип `flow.Phase` (4 рантайм-фазы) — ОТДЕЛЬНО от существующего `flow.AgentType` (3 агента, объявляемых в YAML; `autonomous_execution` — рантайм-решение супервизора, а не YAML-значение). Мигрировать 4 пакета так, чтобы их локальные константы/литералы/валидации/маппинги re-source'или значения из `flow` (минимальный ripple, компиляторно проверяемо). `flow` — лист графа зависимостей (не импортирует ничего внутреннего), поэтому все могут его импортировать без цикла.

**Tech Stack:** Go, пакет `pkg/flow` как domain core. Стандартный `testing`.

## Global Constraints
- НЕ менять версию Go в go.mod.
- Коммиты на русском языке, без Co-Authored-By.
- Линт: golangci-lint отсутствует → `go vet ./...` + `go build ./...` как проверка.
- Рефакторинг БЕЗ изменения поведения: все существующие тесты обязаны остаться зелёными.
- НЕ трогать `flow.AgentType` (3 значения) — это YAML-агенты; `autonomous_execution` НЕ должен стать допустимым YAML-агентом.

## Вне охвата (перенесено в S2)
Константы имён артефактов (`plan.md` ×11, `autonomous.flag` ×6, `execution_summary.md`, `feedback.md`, `events.jsonl`, `state.json`, `.lock`) — это раскладка run-директории, отдельный концерн; S2 (god-file split) всё равно трогает эти сайты в orchestrator, там их и консолидируем.

---

### Task 1: Ввести `flow.Phase` vocabulary в pkg/flow

**Files:**
- Create: `pkg/flow/phase.go`
- Create: `pkg/flow/phase_test.go`

**Interfaces:**
- Produces:
  - `type Phase string`
  - `const PhasePlanning, PhaseImplementation, PhaseReview, PhaseAutonomous Phase` (значения `"planning"`, `"implementation"`, `"review"`, `"autonomous_execution"`)
  - `func Phases() []Phase`
  - `func IsValidPhase(s string) bool`
  - `func PhaseJSONL(p Phase) string`
  - `func PhaseStreamLogs(p Phase) []string`

- [ ] **Step 1: Написать тесты**

Создать `pkg/flow/phase_test.go`:

```go
package flow

import (
	"slices"
	"testing"
)

func TestIsValidPhase(t *testing.T) {
	for _, ok := range []string{"planning", "implementation", "review", "autonomous_execution"} {
		if !IsValidPhase(ok) {
			t.Errorf("IsValidPhase(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "commit-changes", "Planning", "autonomous", "plan"} {
		if IsValidPhase(bad) {
			t.Errorf("IsValidPhase(%q) = true, want false", bad)
		}
	}
}

func TestPhaseValues(t *testing.T) {
	// Значения фаз — часть контракта имён файлов на диске; зафиксировать.
	if PhasePlanning != "planning" || PhaseImplementation != "implementation" ||
		PhaseReview != "review" || PhaseAutonomous != "autonomous_execution" {
		t.Fatalf("phase constant values changed: %q %q %q %q",
			PhasePlanning, PhaseImplementation, PhaseReview, PhaseAutonomous)
	}
	if got := Phases(); !slices.Equal(got, []Phase{PhasePlanning, PhaseImplementation, PhaseReview, PhaseAutonomous}) {
		t.Fatalf("Phases() = %v", got)
	}
}

func TestPhaseJSONL(t *testing.T) {
	cases := map[Phase]string{
		PhasePlanning:       "planning.jsonl",
		PhaseImplementation: "implementation.jsonl",
		PhaseReview:         "review.jsonl",
		PhaseAutonomous:     "autonomous.jsonl", // НЕ autonomous_execution.jsonl
	}
	for p, want := range cases {
		if got := PhaseJSONL(p); got != want {
			t.Errorf("PhaseJSONL(%q) = %q, want %q", p, got, want)
		}
	}
}

func TestPhaseStreamLogs(t *testing.T) {
	if got := PhaseStreamLogs(PhasePlanning); !slices.Equal(got,
		[]string{"planning.jsonl", "planning-reprompt.jsonl", "planning-revision.jsonl"}) {
		t.Errorf("PhaseStreamLogs(planning) = %v", got)
	}
	if got := PhaseStreamLogs(PhaseAutonomous); !slices.Equal(got, []string{"autonomous.jsonl"}) {
		t.Errorf("PhaseStreamLogs(autonomous) = %v", got)
	}
}

// AgentType (YAML-агенты) НЕ должен включать autonomous_execution.
func TestAgentTypeExcludesAutonomous(t *testing.T) {
	if AgentType("autonomous_execution") == AgentPlanning ||
		AgentType("autonomous_execution") == AgentImplementation ||
		AgentType("autonomous_execution") == AgentReview {
		t.Fatal("autonomous_execution must not equal any YAML AgentType")
	}
}
```

- [ ] **Step 2: Прогнать — падает (нет реализации)**

Run: `go test ./pkg/flow/ -run 'TestIsValidPhase|TestPhase|TestAgentType' -v`
Expected: FAIL — `undefined: Phase` / `undefined: IsValidPhase` (компиляция теста не проходит).

- [ ] **Step 3: Реализовать pkg/flow/phase.go**

Создать `pkg/flow/phase.go`:

```go
package flow

// Phase — имя фазы выполнения стадии в рантайме. В отличие от AgentType
// (агенты, объявляемые в YAML: planning/implementation/review), множество фаз
// включает autonomous_execution — это рантайм-решение супервизора, а НЕ
// допустимое значение поля agents: в YAML. Единый источник правды для всех
// пакетов (orchestrator/server/mcp/prompts).
type Phase string

const (
	PhasePlanning       Phase = "planning"
	PhaseImplementation Phase = "implementation"
	PhaseReview         Phase = "review"
	PhaseAutonomous     Phase = "autonomous_execution"
)

// Phases возвращает все допустимые рантайм-фазы.
func Phases() []Phase {
	return []Phase{PhasePlanning, PhaseImplementation, PhaseReview, PhaseAutonomous}
}

// IsValidPhase сообщает, является ли s допустимым именем фазы.
func IsValidPhase(s string) bool {
	switch Phase(s) {
	case PhasePlanning, PhaseImplementation, PhaseReview, PhaseAutonomous:
		return true
	default:
		return false
	}
}

// PhaseJSONL возвращает имя основного stream-json лога фазы. autonomous-трек
// логируется в autonomous.jsonl (а не autonomous_execution.jsonl).
func PhaseJSONL(p Phase) string {
	if p == PhaseAutonomous {
		return "autonomous.jsonl"
	}
	return string(p) + ".jsonl"
}

// PhaseStreamLogs возвращает все stream-json логи фазы в хронологическом
// порядке (для отображения истории в UI дашборда).
func PhaseStreamLogs(p Phase) []string {
	switch p {
	case PhasePlanning:
		return []string{"planning.jsonl", "planning-reprompt.jsonl", "planning-revision.jsonl"}
	case PhaseImplementation:
		return []string{"implementation.jsonl"}
	case PhaseReview:
		return []string{"review.jsonl"}
	case PhaseAutonomous:
		return []string{"autonomous.jsonl"}
	default:
		return nil
	}
}
```

- [ ] **Step 4: Прогнать — зелёно**

Run: `go test ./pkg/flow/`
Expected: ok — новые тесты + все существующие тесты пакета flow зелёные.

- [ ] **Step 5: Коммит**

```bash
git add pkg/flow/phase.go pkg/flow/phase_test.go
git commit -m "feat(flow): единый словарь фаз (Phase, IsValidPhase, PhaseJSONL, PhaseStreamLogs)

Domain core для доменной концепции «фаза выполнения». Тип Phase (4 рантайм-фазы,
включая autonomous_execution) отделён от AgentType (3 YAML-агента). Значения,
валидация и маппинг фаза→лог-файл теперь в одном листовом пакете; следующие
задачи мигрируют orchestrator/server/mcp/prompts на него."
```

---

### Task 2: Мигрировать orchestrator на flow-фазы

**Files:**
- Modify: `pkg/orchestrator/orchestrator.go` (константы `phase*` строки 25-35; `jsonlFileForPhase` ~строка 598)

**Interfaces:**
- Consumes: `flow.PhasePlanning/PhaseImplementation/PhaseReview/PhaseAutonomous`, `flow.PhaseJSONL` (Task 1). `orchestrator.go` уже импортирует `pkg/flow`.

- [ ] **Step 1: Re-source констант из flow**

В `pkg/orchestrator/orchestrator.go` заменить блок объявления констант фаз (сейчас — строки 25-35, четыре отдельных `const phaseX = "literal"` с комментариями):

```go
// Имена фаз выполнения — единый источник в pkg/flow. Локальные строковые
// алиасы оставлены, т.к. по коду orchestrator фаза используется как string;
// значения больше не дублируются литералами.
const (
	phasePlanning       = string(flow.PhasePlanning)
	phaseImplementation = string(flow.PhaseImplementation)
	phaseReview         = string(flow.PhaseReview)
	phaseAutonomous     = string(flow.PhaseAutonomous)
)
```

- [ ] **Step 2: `jsonlFileForPhase` делегирует в flow**

Заменить тело `jsonlFileForPhase` (сейчас спецкейс autonomous + `phase + ".jsonl"`):

```go
// jsonlFileForPhase возвращает имя JSONL-лога для фазы (делегирует flow).
func jsonlFileForPhase(phase string) string {
	return flow.PhaseJSONL(flow.Phase(phase))
}
```

- [ ] **Step 3: Собрать и прогнать пакет**

Run: `go build ./... && go vet ./pkg/orchestrator/...`
Expected: чисто (константы имеют те же строковые значения → все сравнения `phase == phasePlanning` и т.п. работают без изменений).

Run: `go test ./pkg/orchestrator/...`
Expected: ok — поведение не изменилось.

- [ ] **Step 4: Коммит**

```bash
git add pkg/orchestrator/orchestrator.go
git commit -m "refactor(orchestrator): фазы и jsonlFileForPhase из pkg/flow

Локальные phase-константы re-source'ят значения из flow.Phase*, jsonlFileForPhase
делегирует flow.PhaseJSONL — устранено дублирование строковых литералов фаз.
Поведение не изменилось."
```

---

### Task 3: Мигрировать server на flow-фазы

**Files:**
- Modify: `pkg/server/handlers.go` (const-блок `phase*` строки 18-25; `phaseStreamLogs` map + её использование строки 248-275; валидация фазы строка 352)

**Interfaces:**
- Consumes: `flow.PhasePlanning/…`, `flow.PhaseStreamLogs`, `flow.IsValidPhase` (Task 1). Добавить import `github.com/akopichin/afm/pkg/flow` в handlers.go (server ещё НЕ импортирует flow — добавить; flow — лист, цикла нет).

- [ ] **Step 1: Re-source констант + добавить import flow**

Добавить в import-блок `pkg/server/handlers.go`:
```go
	"github.com/akopichin/afm/pkg/flow"
```
Заменить const-блок фаз (строки 18-25) на:
```go
const (
	phasePlanning       = string(flow.PhasePlanning)
	phaseImplementation = string(flow.PhaseImplementation)
	phaseReview         = string(flow.PhaseReview)
	phaseAutonomous     = string(flow.PhaseAutonomous)
)
```

- [ ] **Step 2: Заменить phaseStreamLogs на flow.PhaseStreamLogs**

Удалить `var phaseStreamLogs = map[string][]string{…}` (строки 250-255). В цикле, который его использовал (строки ~265-275), заменить обход `[]string{phasePlanning, phaseImplementation, phaseReview, phaseAutonomous}` + `phaseStreamLogs[phase]` на обход `flow.Phases()` с `flow.PhaseStreamLogs(p)`:

```go
	for _, p := range flow.Phases() {
		for _, logName := range flow.PhaseStreamLogs(p) {
			// ... существующее тело цикла без изменений; там, где раньше
			// использовалась переменная phase (string), использовать string(p).
		}
	}
```
ВАЖНО: сохранить существующее тело внутреннего цикла дословно; если оно ссылается на `phase` как string — использовать `string(p)`. Не менять логику чтения/склейки логов.

- [ ] **Step 3: Валидация фазы через flow.IsValidPhase**

Заменить строку 352:
```go
	if !flow.IsValidPhase(req.Phase) {
		http.Error(w, "invalid phase", http.StatusBadRequest)
		return
	}
```

- [ ] **Step 4: Собрать и прогнать**

Run: `go build ./... && go vet ./pkg/server/...`
Expected: чисто. Если `phasePlanning`/и т.п. остались неиспользуемыми после замены map — оставить только реально используемые; удалить неиспользуемые, чтобы vet/компилятор не ругался (Go не ругается на неиспользуемые package-level const, так что можно оставить — но если линт-хук ругается, убрать лишние).

Run: `go test ./pkg/server/...`
Expected: ok.

- [ ] **Step 5: Коммит**

```bash
git add pkg/server/handlers.go
git commit -m "refactor(server): фазы, phaseStreamLogs и валидация из pkg/flow

Константы фаз re-source'ят flow.Phase*, phaseStreamLogs заменён на
flow.PhaseStreamLogs, валидация — flow.IsValidPhase. Дублирование словаря фаз
между server и flow устранено. Поведение не изменилось."
```

---

### Task 4: Мигрировать mcp и prompts на flow-фазы

**Files:**
- Modify: `pkg/mcp/dialog.go` (инлайн-литералы фаз, строка 210)
- Modify: `pkg/prompts/builder.go` (тип `Agent` + константы, строки 11-18)

**Interfaces:**
- Consumes: `flow.IsValidPhase` (mcp), `flow.Phase` + `flow.Phase*` (prompts). Оба пакета: добавить import flow (prompts уже импортирует flow; mcp — добавить, flow лист → цикла нет).

- [ ] **Step 1: mcp/dialog.go — валидация через flow.IsValidPhase**

Добавить import `github.com/akopichin/afm/pkg/flow` в `pkg/mcp/dialog.go`. Заменить строку 210:
```go
		if !flow.IsValidPhase(phase) {
			continue
		}
```

- [ ] **Step 2: prompts — Agent как алиас flow.Phase**

В `pkg/prompts/builder.go` заменить объявление типа и констант (строки 11-18):
```go
// Agent — фаза выполнения (алиас доменного flow.Phase). Значения приходят из
// единого источника pkg/flow; локальные имена сохранены для читаемости
// вызовов prompts.AgentPlanning и т.п.
type Agent = flow.Phase

const (
	AgentPlanning       = flow.PhasePlanning
	AgentImplementation = flow.PhaseImplementation
	AgentReview         = flow.PhaseReview
	AgentAutonomous     = flow.PhaseAutonomous
)
```
(`prompts` уже импортирует `pkg/flow`.) Тип-алиас `=` означает `prompts.Agent` и `flow.Phase` — один тип; существующие `Inputs.PhaseAgent Agent`, `fmt.Fprintf("%s"/"%q", in.PhaseAgent)` и присваивания `PhaseAgent: prompts.AgentPlanning` в orchestrator компилируются без изменений.

- [ ] **Step 3: Собрать и прогнать (вкл. вызывающих prompts)**

Run: `go build ./... && go vet ./pkg/mcp/... ./pkg/prompts/...`
Expected: чисто. `go build ./...` покрывает orchestrator (он присваивает `PhaseAgent: prompts.AgentPlanning` и т.д. — тип-алиас гарантирует совместимость).

Run: `go test ./pkg/mcp/... ./pkg/prompts/... ./pkg/orchestrator/...`
Expected: ok — в т.ч. `builder_test.go` (использует `PhaseAgent: AgentPlanning`) и интеграционные тесты диалога.

- [ ] **Step 4: Коммит**

```bash
git add pkg/mcp/dialog.go pkg/prompts/builder.go
git commit -m "refactor(mcp,prompts): фазы из pkg/flow

mcp/dialog валидирует фазу через flow.IsValidPhase (вместо инлайн-литералов);
prompts.Agent стал алиасом flow.Phase с константами из flow. Доменный словарь
фаз теперь единый источник во всех 4 пакетах."
```

---

## Self-Review

**Spec coverage (S1):**
- Единый источник фаз + IsValidPhase → Task 1 (`flow.Phase`, `IsValidPhase`). ✓
- Правило фаза→jsonl (спецкейс autonomous) + stream-logs → Task 1 (`PhaseJSONL`, `PhaseStreamLogs`), потребляется Task 2/3. ✓
- Миграция orchestrator/server/mcp/prompts на единый источник → Tasks 2/3/4. ✓
- Разделение рантайм-фаз (4) и YAML-агентов (3) → Task 1 (`Phase` vs `AgentType`, тест `TestAgentTypeExcludesAutonomous`, комментарии). ✓
- Артефакт-имена (`plan.md` и т.д.) — явно вне охвата (перенос в S2). ✓

**Placeholder scan:** код приведён полностью в каждом шаге; команды и ожидаемый вывод указаны. Единственное место с прозой — Task 3 Step 2 «сохранить тело внутреннего цикла дословно»: это потому, что тело (чтение/склейка логов) не меняется, меняется только источник итерации; имплементер видит текущий код в файле. ✓

**Type consistency:** `flow.Phase` определён в Task 1; `PhaseJSONL(p Phase)`/`PhaseStreamLogs(p Phase)`/`IsValidPhase(s string) bool` — сигнатуры едины между определением (Task 1) и потреблением (Tasks 2-4). `type Agent = flow.Phase` (алиас, не новый тип) в Task 4 гарантирует совместимость с существующими `PhaseAgent`-присваиваниями. Локальные `const phaseX = string(flow.PhaseX)` — строковые, совместимы с существующими `string`-сравнениями в orchestrator/server. ✓
