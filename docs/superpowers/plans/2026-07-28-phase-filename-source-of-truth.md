# pkg/flow как единственный источник правды для имён файлов по фазе — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Убрать дублирование факта «как называется файл для фазы X» — сейчас он независимо реализован на пишущей стороне (`pkg/orchestrator/agents.go`, литералы) и минимум в 6 читающих местах (3 пакета) — заменив всё вызовами двух новых функций `pkg/flow`.

**Architecture:** `pkg/flow/phase.go` получает `PhaseLogFile(p Phase) string` и `PhaseLogFiles(p Phase) []string` — по образцу уже существующих `PhaseJSONL`/`PhaseStreamLogs`. Пишущая сторона (`agents.go`) и все 6 читающих мест (`retry.go`, `recovery.go`, `dialog_poller.go`, `events_handler.go`, `handlers.go`, `check.go`) переводятся на эти функции. Два из шести читающих мест (`handlers.go`, `check.go`) сейчас неполны — рефакторинг расширяет их наблюдаемое поведение (видят больше логов), это осознанное и одобренное расширение охвата, не побочный эффект.

**Tech Stack:** Go 1.26.4, стандартная библиотека — без новых внешних зависимостей.

## Global Constraints

- `PhaseLogFile(p Phase) string` — для `PhaseAutonomous` возвращает `"autonomous.log"` (не `"autonomous_execution.log"`), для остальных — `string(p) + ".log"`. Живёт в `pkg/flow/phase.go`.
- `PhaseLogFiles(p Phase) []string` — канонический файл фазы первым, затем retry/revise-варианты в хронологическом порядке: planning → `{"planning.log", "planning-reprompt.log", "planning-revision.log"}`; implementation → `{"implementation.log", "implementation-feedback.log"}`; review → `{"review.log", "review-feedback.log"}`; autonomous → `{"autonomous.log", "autonomous-feedback.log"}`.
- Пишущая сторона (`agents.go`) переводит ТОЛЬКО 6 мест с каноническими (не вариантными) именами на `flow.PhaseLogFile`. 5 вариантных мест (`-feedback`/`-reprompt`/`-revision`) не трогаются.
- Все 6 читающих мест (`retry.go`, `recovery.go`, `dialog_poller.go`, `events_handler.go`, `handlers.go`, `check.go`) переводятся на `flow.Phases()`/`flow.PhaseJSONL()`/`flow.PhaseLogFile(s)` вместо ручных списков/switch.
- `events_handler.go`: doc-комментарий у `autonomousLabel` обновляется — после рефакторинга эта константа используется ТОЛЬКО для лейбла supervisor-track («standard»/«autonomous»), не для имени фазы.
- `handlers.go` (`handleLog`) и `check.go` (`lastLogAction`) — сознательное расширение поведения (видят больше файлов, включая review/autonomous и feedback-варианты), а не только рефакторинг реализации; `lastLogAction` сохраняет семантику «более поздняя фаза побеждает, если её лог существует» (раньше — implementation бьёт planning; теперь распространяется на review/autonomous).
- Публичные сигнатуры существующих функций (`buildRetryContext`, `detectInterruptedPhase`, `dialogPhases`, `reconstructAgentActions`, `handleLog`, `lastLogAction`) не меняются — только внутренняя реализация.
- Существующие тесты (`TestBuildRetryContext_*`, `TestDialogPhases_*`, `TestHandleLog`, интеграционные тесты recovery) должны продолжать проходить без изменений в самих тестах.

---

### Task 1: `pkg/flow` — `PhaseLogFile` и `PhaseLogFiles` (TDD)

**Files:**
- Modify: `pkg/flow/phase.go` (добавить обе функции после существующего `PhaseStreamLogs`)
- Modify: `pkg/flow/phase_test.go` (добавить `TestPhaseLogFile`, `TestPhaseLogFiles`)

**Interfaces:**
- Consumes: ничего из предыдущих задач (первая задача).
- Produces: `func PhaseLogFile(p Phase) string` и `func PhaseLogFiles(p Phase) []string` — используются во ВСЕХ последующих задачах.

- [ ] **Step 1: Написать падающие тесты**

Добавить в `pkg/flow/phase_test.go`, сразу после существующего `TestPhaseStreamLogs`:

```go
func TestPhaseLogFile(t *testing.T) {
	cases := map[Phase]string{
		PhasePlanning:       "planning.log",
		PhaseImplementation: "implementation.log",
		PhaseReview:         "review.log",
		PhaseAutonomous:     "autonomous.log", // НЕ autonomous_execution.log
	}
	for p, want := range cases {
		if got := PhaseLogFile(p); got != want {
			t.Errorf("PhaseLogFile(%q) = %q, want %q", p, got, want)
		}
	}
}

func TestPhaseLogFiles(t *testing.T) {
	cases := map[Phase][]string{
		PhasePlanning:       {"planning.log", "planning-reprompt.log", "planning-revision.log"},
		PhaseImplementation: {"implementation.log", "implementation-feedback.log"},
		PhaseReview:         {"review.log", "review-feedback.log"},
		PhaseAutonomous:     {"autonomous.log", "autonomous-feedback.log"},
	}
	for p, want := range cases {
		if got := PhaseLogFiles(p); !slices.Equal(got, want) {
			t.Errorf("PhaseLogFiles(%q) = %v, want %v", p, got, want)
		}
	}
}
```

(`slices` уже импортирован в этом файле — используется существующим `TestPhaseStreamLogs`.)

- [ ] **Step 2: Убедиться что тесты падают**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go test ./pkg/flow/... -run 'TestPhaseLogFile|TestPhaseLogFiles' -v`
Expected: FAIL — `undefined: PhaseLogFile` / `undefined: PhaseLogFiles`.

- [ ] **Step 3: Реализовать функции**

В `pkg/flow/phase.go`, сразу после `PhaseStreamLogs` (после его закрывающей `}`):

```go

// PhaseLogFile returns the phase's canonical human-readable log file name.
// autonomous logs to autonomous.log (not autonomous_execution.log),
// mirroring PhaseJSONL's naming.
func PhaseLogFile(p Phase) string {
	if p == PhaseAutonomous {
		return "autonomous.log"
	}
	return string(p) + ".log"
}

// PhaseLogFiles returns all human-readable log files a phase may produce,
// in chronological/reading order: the canonical file first, then any
// retry/revise variants. Mirrors PhaseStreamLogs but for *.log, not *.jsonl.
func PhaseLogFiles(p Phase) []string {
	switch p {
	case PhasePlanning:
		return []string{"planning.log", "planning-reprompt.log", "planning-revision.log"}
	case PhaseImplementation:
		return []string{"implementation.log", "implementation-feedback.log"}
	case PhaseReview:
		return []string{"review.log", "review-feedback.log"}
	case PhaseAutonomous:
		return []string{"autonomous.log", "autonomous-feedback.log"}
	default:
		return nil
	}
}
```

- [ ] **Step 4: Убедиться что тесты проходят**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go test ./pkg/flow/... -run 'TestPhaseLogFile|TestPhaseLogFiles' -v`
Expected: PASS.

- [ ] **Step 5: Прогнать весь пакет + go vet**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go test ./pkg/flow/... -v && go vet ./pkg/flow/...`
Expected: всё зелёное, включая существующие тесты пакета.

- [ ] **Step 6: Commit**

```bash
cd /Users/alexander.kopichin/work/personal/afm
git add pkg/flow/phase.go pkg/flow/phase_test.go
git commit -m "feat(flow): PhaseLogFile/PhaseLogFiles — единый формат имён .log-файлов по фазе"
```

---

### Task 2: Пишущая сторона — `pkg/orchestrator/agents.go`

**Files:**
- Modify: `pkg/orchestrator/agents.go` (6 мест построения канонических имён `.log`)

**Interfaces:**
- Consumes: `flow.PhaseLogFile(p flow.Phase) string` из Task 1.
- Produces: ничего нового для последующих задач — это конечный потребитель.

- [ ] **Step 1: Заменить 6 канонических мест**

В `pkg/orchestrator/agents.go` (пакет уже импортирует `"github.com/akopichin/afm/pkg/flow"` — новый импорт не нужен):

Строка (ориентировочно 63, внутри `runPlanningAgent`):
```go
		logFile := filepath.Join(stageDir, "planning.log")
```
→
```go
		logFile := filepath.Join(stageDir, flow.PhaseLogFile(flow.PhasePlanning))
```

Строка (ориентировочно 225, внутри `runImplementationAgent`):
```go
		logFile := filepath.Join(stageDir, "implementation.log")
```
→
```go
		logFile := filepath.Join(stageDir, flow.PhaseLogFile(flow.PhaseImplementation))
```

Строка (ориентировочно 243, внутри `runImplementationAgent`, review-подветка):
```go
			reviewLog := filepath.Join(stageDir, "review.log")
```
→
```go
			reviewLog := filepath.Join(stageDir, flow.PhaseLogFile(flow.PhaseReview))
```

Строка (ориентировочно 284, внутри `runReviewAgent`) — та же замена, что и предыдущая (`"review.log"` → `flow.PhaseLogFile(flow.PhaseReview)`).

Строка (ориентировочно 337, внутри `runAutonomousAgent`):
```go
		logFile := filepath.Join(stageDir, "autonomous.log")
```
→
```go
		logFile := filepath.Join(stageDir, flow.PhaseLogFile(flow.PhaseAutonomous))
```

Строка (ориентировочно 430, внутри `runReviewWithFeedback`) — та же замена `"review.log"` → `flow.PhaseLogFile(flow.PhaseReview)`, что и выше.

**Не трогать** (вариантные файлы, специфичны для retry/feedback-флоу): строки со значениями `"planning-reprompt.log"`, `"planning-revision.log"`, `"implementation-feedback.log"`, `"review-feedback.log"`, `"autonomous-feedback.log"`.

Используй Grep/поиск по литералам `"planning.log"`, `"implementation.log"`, `"review.log"`, `"autonomous.log"` в этом файле, чтобы найти все 6 вхождений — если найдётся 7-е или пропущено одно из 6, останови и проверь против списка выше, прежде чем продолжать.

- [ ] **Step 2: Собрать проект**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go build ./...`
Expected: чистая сборка.

- [ ] **Step 3: Прогнать пакет orchestrator + go vet**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go test ./pkg/orchestrator/... && go vet ./pkg/orchestrator/...`
Expected: PASS, без замечаний vet.

- [ ] **Step 4: Commit**

```bash
cd /Users/alexander.kopichin/work/personal/afm
git add pkg/orchestrator/agents.go
git commit -m "refactor(orchestrator): agents.go пишет канонические .log-имена через flow.PhaseLogFile"
```

---

### Task 3: Читающая сторона внутри pkg/orchestrator — `retry.go`, `recovery.go`, `dialog_poller.go`

**Files:**
- Modify: `pkg/orchestrator/retry.go` (`buildRetryContext`)
- Modify: `pkg/orchestrator/recovery.go` (`detectInterruptedPhase`)
- Modify: `pkg/orchestrator/dialog_poller.go` (`dialogPhases`)

**Interfaces:**
- Consumes: `flow.PhaseJSONL(p flow.Phase) string`, `flow.Phases() []flow.Phase` (уже существуют, не из Task 1/2).
- Produces: ничего нового для последующих задач.

Все три файла уже импортируют `"github.com/akopichin/afm/pkg/flow"` — новых импортов не требуется.

- [ ] **Step 1: `retry.go` — заменить switch на `flow.PhaseJSONL`**

Текущее тело `buildRetryContext`:

```go
func buildRetryContext(stageDir, phase string) string {
	var jsonlName string
	switch phase {
	case phasePlanning:
		jsonlName = "planning.jsonl"
	case phaseReview:
		jsonlName = "review.jsonl"
	case phaseAutonomous:
		jsonlName = "autonomous.jsonl"
	default:
		jsonlName = "implementation.jsonl"
	}

	lines := executor.RenderActions(filepath.Join(stageDir, jsonlName))
```

Заменить на:

```go
func buildRetryContext(stageDir, phase string) string {
	jsonlName := flow.PhaseJSONL(flow.Phase(phase))

	lines := executor.RenderActions(filepath.Join(stageDir, jsonlName))
```

(Остальное тело функции ниже не меняется.)

- [ ] **Step 2: `recovery.go` — заменить список на `flow.Phases()`**

Текущее тело `detectInterruptedPhase`:

```go
func (o *Orchestrator) detectInterruptedPhase(stageDir string) string {
	var latestPhase string
	var latestMtime time.Time
	for _, phase := range []string{phasePlanning, phaseImplementation, phaseReview, phaseAutonomous} {
		fi, err := os.Stat(sessionFile(stageDir, phase))
		if err != nil {
			continue
		}
		if fi.ModTime().After(latestMtime) {
			latestMtime = fi.ModTime()
			latestPhase = phase
		}
	}
	return latestPhase
}
```

Заменить на:

```go
func (o *Orchestrator) detectInterruptedPhase(stageDir string) string {
	var latestPhase string
	var latestMtime time.Time
	for _, p := range flow.Phases() {
		phase := string(p)
		fi, err := os.Stat(sessionFile(stageDir, phase))
		if err != nil {
			continue
		}
		if fi.ModTime().After(latestMtime) {
			latestMtime = fi.ModTime()
			latestPhase = phase
		}
	}
	return latestPhase
}
```

- [ ] **Step 3: `dialog_poller.go` — заменить список на фильтрованный `flow.Phases()`**

Текущее тело `dialogPhases`:

```go
func dialogPhases(stageDir string) []string {
	phases := []string{phasePlanning, phaseImplementation, phaseReview}
	if isAutonomousStage(stageDir) {
		phases = append(phases, phaseAutonomous)
	}
	return phases
}
```

Заменить на:

```go
func dialogPhases(stageDir string) []string {
	var phases []string
	for _, p := range flow.Phases() {
		if p == flow.PhaseAutonomous && !isAutonomousStage(stageDir) {
			continue
		}
		phases = append(phases, string(p))
	}
	return phases
}
```

- [ ] **Step 4: Прогнать существующие целевые тесты**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go test ./pkg/orchestrator/... -run 'TestBuildRetryContext|TestDialogPhases' -v`
Expected: PASS — `TestBuildRetryContext_FullActionNotTruncated`, `TestBuildRetryContext_MissingLogReturnsEmpty`, `TestDialogPhases_BaseWithoutAutonomousFlag`, `TestDialogPhases_IncludesAutonomousWhenFlagPresent` все зелёные без изменений в самих тестах.

- [ ] **Step 5: Собрать проект + прогнать весь пакет orchestrator + go vet**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go build ./... && go test ./pkg/orchestrator/... && go vet ./pkg/orchestrator/...`
Expected: всё зелёное (включая интеграционные тесты recovery, которые используют `detectInterruptedPhase` косвенно).

- [ ] **Step 6: Commit**

```bash
cd /Users/alexander.kopichin/work/personal/afm
git add pkg/orchestrator/retry.go pkg/orchestrator/recovery.go pkg/orchestrator/dialog_poller.go
git commit -m "refactor(orchestrator): retry/recovery/dialog_poller читают список фаз из flow.Phases()"
```

---

### Task 4: `pkg/server/events_handler.go` — `reconstructAgentActions`

**Files:**
- Modify: `pkg/server/events_handler.go`
- Modify: `pkg/server/events_handler_test.go` (создать файл, если его нет — новый тест `reconstructAgentActions`)

**Interfaces:**
- Consumes: `flow.Phases() []flow.Phase`, `flow.PhaseJSONL(p flow.Phase) string`.
- Produces: ничего нового для последующих задач.

- [ ] **Step 1: Добавить импорт `flow`**

В начале `pkg/server/events_handler.go` текущий блок импорта:

```go
import (
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/state"
)
```

Заменить на (добавлена строка `"github.com/akopichin/afm/pkg/flow"`, алфавитный порядок между `executor` и `state`):

```go
import (
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)
```

- [ ] **Step 2: Заменить хардкод-список фаз в `reconstructAgentActions`**

Текущий цикл:

```go
	for _, phase := range []string{"planning", "implementation", "review", autonomousLabel} {
		path := filepath.Join(stageDir, phase+".jsonl")
```

Заменить на:

```go
	for _, p := range flow.Phases() {
		path := filepath.Join(stageDir, flow.PhaseJSONL(p))
```

(Тело цикла ниже — `os.Stat`, `readLines`, интерполяция времени, построение `feedEvent` — не меняется; переменная `phase` больше нигде в теле цикла не используется, кроме построения `path`, так что удаление её как самостоятельной переменной безопасно.)

- [ ] **Step 3: Обновить doc-комментарий `autonomousLabel`**

Текущий комментарий:

```go
// autonomousLabel — имя фазы (reconstructAgentActions) и значение
// supervisor-решения (Task 3, logSupervisorDecision track="autonomous")
// текстуально совпадают, поэтому используем одну общую константу вместо
// двух одинаковых строковых литералов (goconst).
const autonomousLabel = "autonomous"
```

Заменить на:

```go
// autonomousLabel — значение supervisor-решения (Task 3,
// logSupervisorDecision track="autonomous") в can_execute_autonomously.
// Не связано с flow.PhaseAutonomous ("autonomous_execution") — это
// отдельный, случайно совпадающий по подстроке "autonomous" словарь
// (supervisor-track "standard"/"autonomous", а не имя фазы).
const autonomousLabel = "autonomous"
```

- [ ] **Step 4: Написать тест для `reconstructAgentActions`**

Файл `pkg/server/events_handler_test.go` уже существует (содержит `TestHandleEvents_ReplaysTransitionsAndNotices`, `TestHandleEvents_CapsAt200`, импортирует `os`/`path/filepath`/`testing` — все три уже нужны новому тесту, доп. импортов не требуется) — добавить новую функцию в конец файла:

```go
func TestReconstructAgentActions_CoversAllPhasesIncludingAutonomous(t *testing.T) {
	stageDir := t.TempDir()

	planningLine := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"echo hi"}}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(stageDir, "planning.jsonl"), []byte(planningLine), 0644); err != nil {
		t.Fatal(err)
	}
	// autonomous — граничный случай: файл называется autonomous.jsonl,
	// НЕ autonomous_execution.jsonl (flow.PhaseJSONL спецкейс).
	autonomousLine := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":"plan.md"}}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(stageDir, "autonomous.jsonl"), []byte(autonomousLine), 0644); err != nil {
		t.Fatal(err)
	}

	out := reconstructAgentActions(filepath.Dir(stageDir), filepath.Base(stageDir))

	if len(out) != 2 {
		t.Fatalf("want 2 agent_action events (one per phase file), got %d: %+v", len(out), out)
	}
	tools := map[string]bool{}
	for _, e := range out {
		data, ok := e.Data.(map[string]string)
		if !ok {
			t.Fatalf("unexpected Data type: %#v", e.Data)
		}
		tools[data["tool"]] = true
	}
	if !tools["Bash"] || !tools["Write"] {
		t.Errorf("expected actions from both planning.jsonl (Bash) and autonomous.jsonl (Write), got tools: %v", tools)
	}
}
```

(Сигнатура `reconstructAgentActions(runDir, stageID string) []feedEvent` — вызывается с `runDir=filepath.Dir(stageDir)`, `stageID=filepath.Base(stageDir)`, чтобы итоговый `filepath.Join(runDir, stageID)` внутри функции снова дал `stageDir`.)

- [ ] **Step 5: Запустить новый тест**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go test ./pkg/server/... -run TestReconstructAgentActions_CoversAllPhasesIncludingAutonomous -v`
Expected: PASS.

- [ ] **Step 6: Прогнать весь пакет server + go vet + go build**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go build ./... && go test ./pkg/server/... -v && go vet ./pkg/server/...`
Expected: всё зелёное, включая существующий `TestHandleLog` (эта задача его не трогает — см. Task 5).

- [ ] **Step 7: Commit**

```bash
cd /Users/alexander.kopichin/work/personal/afm
git add pkg/server/events_handler.go pkg/server/events_handler_test.go
git commit -m "refactor(server): reconstructAgentActions читает фазы из flow.Phases(), тест на autonomous-спецкейс"
```

---

### Task 5: Расширение покрытия — `handlers.go` (`handleLog`) и `check.go` (`lastLogAction`)

**Files:**
- Modify: `pkg/server/handlers.go` (`handleLog`)
- Modify: `pkg/server/handlers_test.go` (расширить существующий `TestHandleLog` или добавить новый тест)
- Modify: `cmd/afm/check.go` (`lastLogAction`)
- Modify: `cmd/afm/check_test.go` (создать файл, если его нет — новый тест `lastLogAction`)

**Interfaces:**
- Consumes: `flow.Phases() []flow.Phase`, `flow.PhaseLogFiles(p flow.Phase) []string` из Task 1.
- Produces: ничего для последующих задач (последняя задача плана).

`pkg/server/handlers.go` уже импортирует `flow` — новый импорт не нужен. `cmd/afm/check.go` — импорт `flow` нужно добавить.

- [ ] **Step 1: `handlers.go` — заменить список на `flow.Phases()`+`flow.PhaseLogFiles`**

Текущее тело (внутри `handleLog`):

```go
	var logContent string
	for _, name := range []string{"planning.log", "planning-revision.log", "implementation.log", "review.log", "autonomous.log"} {
		data, err := os.ReadFile(filepath.Join(stageDir, name))
		if err == nil {
			logContent += string(data)
		}
	}
```

Заменить на:

```go
	var logContent string
	for _, p := range flow.Phases() {
		for _, name := range flow.PhaseLogFiles(p) {
			data, err := os.ReadFile(filepath.Join(stageDir, name))
			if err == nil {
				logContent += string(data)
			}
		}
	}
```

- [ ] **Step 2: Прогнать существующий TestHandleLog**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go test ./pkg/server/... -run TestHandleLog -v`
Expected: PASS без изменений в самом тесте (planning.log остаётся первым в порядке обхода).

- [ ] **Step 3: Добавить тест на расширенное покрытие в `handlers_test.go`**

Добавить рядом с существующим `TestHandleLog`:

```go
func TestHandleLog_IncludesReviewAndAutonomousPhases(t *testing.T) {
	srv, runDir := setupTestServer(t)
	stageDir := filepath.Join(runDir, testStageID)
	if err := os.WriteFile(filepath.Join(stageDir, "review.log"), []byte("review phase output"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "autonomous.log"), []byte("autonomous phase output"), 0644); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/api/stages/"+testStageID+"/log", nil)
	w := httptest.NewRecorder()
	srv.handleLog(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "review phase output") {
		t.Errorf("expected review.log content in response, got: %s", body)
	}
	if !strings.Contains(body, "autonomous phase output") {
		t.Errorf("expected autonomous.log content in response, got: %s", body)
	}
}
```

(`setupTestServer(t)` returns `(*Server, runDir string)` — confirmed from its current implementation in `handlers_test.go`; `runDir` is the temp run directory, NOT the stage directory. `stageDir` must be derived as `filepath.Join(runDir, testStageID)`, matching what `setupTestServer` itself does internally.)

- [ ] **Step 4: Запустить оба теста handlers**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go test ./pkg/server/... -run 'TestHandleLog' -v`
Expected: PASS — оба теста.

- [ ] **Step 5: `check.go` — добавить импорт и переписать `lastLogAction`**

В начале `cmd/afm/check.go` текущий блок импорта:

```go
import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/akopichin/afm/pkg/state"
)
```

Заменить на (добавлена строка с `flow`, алфавитный порядок):

```go
import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)
```

Текущее тело `lastLogAction`:

```go
func lastLogAction(stageDir string) string {
	for _, name := range []string{"implementation.log", "planning.log"} {
		data, err := os.ReadFile(filepath.Join(stageDir, name))
		if err != nil {
			continue
		}
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		if len(lines) > 0 {
			last := lines[len(lines)-1]
			if len(last) > 60 {
				last = last[:60] + "..."
			}
			return last
		}
	}
	return ""
}
```

Заменить на:

```go
func lastLogAction(stageDir string) string {
	var last string
	for _, p := range flow.Phases() {
		for _, name := range flow.PhaseLogFiles(p) {
			data, err := os.ReadFile(filepath.Join(stageDir, name))
			if err != nil || len(data) == 0 {
				continue
			}
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			if len(lines) > 0 && lines[len(lines)-1] != "" {
				last = lines[len(lines)-1]
			}
		}
	}
	if len(last) > 60 {
		last = last[:60] + "..."
	}
	return last
}
```

- [ ] **Step 6: Написать тесты для `lastLogAction`**

Файл `cmd/afm/check_test.go` уже существует (содержит `TestCheckReadsStatusFromLogNotSnapshot` и другие, импортирует `os`/`path/filepath`/`testing` — все три уже нужны новым тестам, доп. импортов не требуется) — добавить три функции в конец файла:

```go
func TestLastLogAction_OnlyPlanning(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "planning.log"), []byte("line one\nplanning last line"), 0644); err != nil {
		t.Fatal(err)
	}
	got := lastLogAction(dir)
	if got != "planning last line" {
		t.Errorf("got %q, want %q", got, "planning last line")
	}
}

func TestLastLogAction_ImplementationBeatsPlanning(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "planning.log"), []byte("planning last line"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "implementation.log"), []byte("implementation last line"), 0644); err != nil {
		t.Fatal(err)
	}
	got := lastLogAction(dir)
	if got != "implementation last line" {
		t.Errorf("got %q, want %q — later phase must win", got, "implementation last line")
	}
}

func TestLastLogAction_ReviewOnly(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "review.log"), []byte("review last line"), 0644); err != nil {
		t.Fatal(err)
	}
	got := lastLogAction(dir)
	if got != "review last line" {
		t.Errorf("got %q, want %q — review.log was not covered before this fix", got, "review last line")
	}
}
```

- [ ] **Step 7: Запустить новые тесты**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go test ./cmd/afm/... -run TestLastLogAction -v`
Expected: PASS — все 3 теста.

- [ ] **Step 8: Полный прогон + go vet + go build**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go build ./... && go vet ./... && go test ./... -race`
Expected: всё зелёное, все пакеты.

- [ ] **Step 9: Commit**

```bash
cd /Users/alexander.kopichin/work/personal/afm
git add pkg/server/handlers.go pkg/server/handlers_test.go cmd/afm/check.go cmd/afm/check_test.go
git commit -m "fix(server,cmd): handleLog/lastLogAction видят все фазы через flow.Phases()+PhaseLogFiles"
```
