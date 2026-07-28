# Один источник правды для plan.v{N}.md — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Убрать дублирование двух независимых алгоритмов, работающих с файловой конвенцией `plan.v{N}.md` (версионирование при Revise и чтение "предыдущего плана" для промпта), заменив их одной экспортируемой функцией `state.LatestPlanVersion`.

**Architecture:** Новая функция `state.LatestPlanVersion(stageDir string) (version int, content string, err error)` в `pkg/state/state.go` сканирует директорию стадии на файлы `plan.v{N}.md`, возвращает максимальный найденный номер версии и содержимое этого файла. `VersionPlan` (там же) и `runPlanningWithFeedback` (`pkg/orchestrator/agents.go`) переводятся на вызов этой функции вместо своих независимых реализаций (stat-петля от 1 и inline-regexp соответственно).

**Tech Stack:** Go 1.26.4, стандартная библиотека (`os`, `strings`, `strconv`) — без внешних зависимостей.

## Global Constraints

- Новая функция называется `LatestPlanVersion`, сигнатура ровно `func LatestPlanVersion(stageDir string) (version int, content string, err error)`, находится в `pkg/state/state.go` — новый файл/пакет не создаётся.
- Реализация без `regexp`: формат имени `plan.v{N}.md` разбирается через `strings.HasPrefix`/`strings.HasSuffix` + `strconv.Atoi` на середине строки. Файлы, не проходящие оба условия ИЛИ с нечисловой серединой, игнорируются (не считаются версией, не валят функцию ошибкой).
- Пустая директория (или директория без единого `plan.v*.md`) → `(0, "", nil)`, НЕ ошибка.
- `VersionPlan` вычисляет следующий номер версии как `latest + 1`, где `latest` — из `LatestPlanVersion`; не use прежний stat-цикл от 1.
- `runPlanningWithFeedback` в `pkg/orchestrator/agents.go` вызывает `state.LatestPlanVersion(stageDir)` вместо своего inline-блока (объявление `regexp.MustCompile`, `os.ReadDir` + цикл `FindStringSubmatch`); ошибка чтения директории (ранее тихо игнорировалась через `_, _ := os.ReadDir(...)`) теперь пробрасывается наружу через `return fmt.Errorf(...)` из retry-closure.
- После удаления regexp-блока импорты `"regexp"` и `"strconv"` в `pkg/orchestrator/agents.go` удаляются как неиспользуемые (`"strings"` остаётся — используется в `rePromptMissingSections`).
- Публичные сигнатуры `VersionPlan(stageDir string) (int, error)` и поведение `runPlanningWithFeedback` снаружи (что кладёт в `prompts.Inputs.PreviousPlan`) не меняются — меняется только внутренняя реализация.
- Существующие тесты `TestVersionPlan` (`pkg/state/state_test.go`) и `TestIntegration_ResumeFromRevising` (`pkg/orchestrator/integration_resume_test.go`) должны продолжать проходить без изменений в самих тестах.

---

### Task 1: `LatestPlanVersion` в pkg/state (TDD)

**Files:**
- Modify: `pkg/state/state.go` (добавить функцию рядом с `VersionPlan`)
- Modify: `pkg/state/state_test.go` (добавить `TestLatestPlanVersion`)

**Interfaces:**
- Consumes: ничего из предыдущих задач (первая задача).
- Produces: `func LatestPlanVersion(stageDir string) (version int, content string, err error)` — используется в Task 2 (`VersionPlan`) и Task 3 (`runPlanningWithFeedback`).

- [ ] **Step 1: Написать падающий тест**

Добавить в `pkg/state/state_test.go` (после существующего `TestVersionPlan`):

```go
func TestLatestPlanVersion(t *testing.T) {
	t.Run("empty directory", func(t *testing.T) {
		dir := t.TempDir()
		v, content, err := state.LatestPlanVersion(dir)
		if err != nil {
			t.Fatal(err)
		}
		if v != 0 || content != "" {
			t.Errorf("got (%d, %q), want (0, \"\")", v, content)
		}
	})

	t.Run("single version", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "plan.v1.md"), []byte("v1 content"), 0644); err != nil {
			t.Fatal(err)
		}
		v, content, err := state.LatestPlanVersion(dir)
		if err != nil {
			t.Fatal(err)
		}
		if v != 1 || content != "v1 content" {
			t.Errorf("got (%d, %q), want (1, \"v1 content\")", v, content)
		}
	})

	t.Run("multiple versions with a gap picks the max", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "plan.v1.md"), []byte("v1 content"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "plan.v3.md"), []byte("v3 content"), 0644); err != nil {
			t.Fatal(err)
		}
		v, content, err := state.LatestPlanVersion(dir)
		if err != nil {
			t.Fatal(err)
		}
		if v != 3 || content != "v3 content" {
			t.Errorf("got (%d, %q), want (3, \"v3 content\")", v, content)
		}
	})

	t.Run("garbage names are ignored", func(t *testing.T) {
		dir := t.TempDir()
		for _, name := range []string{"plan.vX.md", "plan.v1.txt", "plan.md", "plan.v.md"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte("should not count"), 0644); err != nil {
				t.Fatal(err)
			}
		}
		v, content, err := state.LatestPlanVersion(dir)
		if err != nil {
			t.Fatal(err)
		}
		if v != 0 || content != "" {
			t.Errorf("got (%d, %q), want (0, \"\") — garbage names must not be counted as versions", v, content)
		}
	})
}
```

- [ ] **Step 2: Запустить тест, убедиться что он падает**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go test ./pkg/state/... -run TestLatestPlanVersion -v`
Expected: FAIL — `undefined: state.LatestPlanVersion` (функция ещё не существует).

- [ ] **Step 3: Реализовать функцию**

В `pkg/state/state.go`, сразу после существующей функции `VersionPlan` (после строки `return n, nil` и закрывающей `}` в конце `VersionPlan`), добавить:

```go
// LatestPlanVersion scans stageDir for plan.v{N}.md files and returns the
// highest N found (0 if none) along with that file's content ("" if none).
// Garbage names (non-numeric middle, wrong extension) are ignored, not errors.
func LatestPlanVersion(stageDir string) (version int, content string, err error) {
	entries, err := os.ReadDir(stageDir)
	if err != nil {
		return 0, "", fmt.Errorf("read stage dir: %w", err)
	}

	best := 0
	var bestName string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "plan.v") || !strings.HasSuffix(name, ".md") {
			continue
		}
		numPart := strings.TrimSuffix(strings.TrimPrefix(name, "plan.v"), ".md")
		n, convErr := strconv.Atoi(numPart)
		if convErr != nil || n <= best {
			continue
		}
		best = n
		bestName = name
	}
	if bestName == "" {
		return 0, "", nil
	}
	data, err := os.ReadFile(filepath.Join(stageDir, bestName))
	if err != nil {
		return 0, "", fmt.Errorf("read %s: %w", bestName, err)
	}
	return best, string(data), nil
}
```

This needs the `strconv` package. Add it to the import block at the top of `pkg/state/state.go` (currently: `"bytes"`, `"encoding/json"`, `"errors"`, `"fmt"`, `"os"`, `"path/filepath"`, `"slices"`, `"strings"`, `"time"`) — insert `"strconv"` alphabetically after `"slices"`:

```go
import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)
```

- [ ] **Step 4: Запустить тест, убедиться что он проходит**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go test ./pkg/state/... -run TestLatestPlanVersion -v`
Expected: PASS — все 4 сабтеста зелёные.

- [ ] **Step 5: Прогнать весь пакет pkg/state, убедиться что ничего не сломано**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go test ./pkg/state/... -v`
Expected: PASS — включая существующий `TestVersionPlan` (он пока не тронут, но должен оставаться зелёным).

- [ ] **Step 6: go vet**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go vet ./pkg/state/...`
Expected: без замечаний.

- [ ] **Step 7: Commit**

```bash
cd /Users/alexander.kopichin/work/personal/afm
git add pkg/state/state.go pkg/state/state_test.go
git commit -m "feat(state): LatestPlanVersion — единый скан plan.v{N}.md"
```

---

### Task 2: `VersionPlan` использует `LatestPlanVersion`

**Files:**
- Modify: `pkg/state/state.go` (тело функции `VersionPlan`)

**Interfaces:**
- Consumes: `LatestPlanVersion(stageDir string) (int, string, error)` из Task 1 (тот же файл/пакет, без импорта).
- Produces: `VersionPlan(stageDir string) (int, error)` — сигнатура НЕ меняется, используется в `cmd/afm/revise.go:59` и `pkg/orchestrator/control_api.go:117` (эти вызывающие места не трогаются).

- [ ] **Step 1: Заменить тело VersionPlan**

Текущее тело (в `pkg/state/state.go`):

```go
// VersionPlan renames plan.md to plan.v{N}.md and returns N.
func VersionPlan(stageDir string) (int, error) {
	planFile := filepath.Join(stageDir, "plan.md")
	if _, err := os.Stat(planFile); err != nil {
		return 0, fmt.Errorf("plan.md not found: %w", err)
	}

	n := 1
	for {
		versionedPath := filepath.Join(stageDir, fmt.Sprintf("plan.v%d.md", n))
		if _, err := os.Stat(versionedPath); os.IsNotExist(err) {
			break
		}
		n++
	}

	dst := filepath.Join(stageDir, fmt.Sprintf("plan.v%d.md", n))
	if err := os.Rename(planFile, dst); err != nil {
		return 0, fmt.Errorf("rename plan: %w", err)
	}
	return n, nil
}
```

Заменить на:

```go
// VersionPlan renames plan.md to plan.v{N}.md and returns N.
func VersionPlan(stageDir string) (int, error) {
	planFile := filepath.Join(stageDir, "plan.md")
	if _, err := os.Stat(planFile); err != nil {
		return 0, fmt.Errorf("plan.md not found: %w", err)
	}

	latest, _, err := LatestPlanVersion(stageDir)
	if err != nil {
		return 0, fmt.Errorf("scan plan versions: %w", err)
	}
	n := latest + 1

	dst := filepath.Join(stageDir, fmt.Sprintf("plan.v%d.md", n))
	if err := os.Rename(planFile, dst); err != nil {
		return 0, fmt.Errorf("rename plan: %w", err)
	}
	return n, nil
}
```

- [ ] **Step 2: Прогнать существующий тест VersionPlan, убедиться что он всё ещё проходит**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go test ./pkg/state/... -run TestVersionPlan -v`
Expected: PASS — `TestVersionPlan` не изменялся, но теперь идёт через `LatestPlanVersion`; должен по-прежнему возвращать `n == 1` на пустой директории (`LatestPlanVersion` вернёт `(0, "", nil)`, `n = 0+1 = 1`).

- [ ] **Step 3: Прогнать весь пакет + go vet**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go test ./pkg/state/... -v && go vet ./pkg/state/...`
Expected: PASS, без замечаний vet.

- [ ] **Step 4: Commit**

```bash
cd /Users/alexander.kopichin/work/personal/afm
git add pkg/state/state.go
git commit -m "refactor(state): VersionPlan берёт latest+1 из LatestPlanVersion вместо stat-петли"
```

---

### Task 3: `runPlanningWithFeedback` использует `LatestPlanVersion`

**Files:**
- Modify: `pkg/orchestrator/agents.go`

**Interfaces:**
- Consumes: `state.LatestPlanVersion(stageDir string) (int, string, error)` из Task 1 (новый импорт `"github.com/akopichin/afm/pkg/state"` в `agents.go`).
- Produces: ничего для последующих задач (Task 4 — только тестирование, без новых интерфейсов).

- [ ] **Step 1: Заменить inline-блок сканирования версий**

Текущий фрагмент в `pkg/orchestrator/agents.go`, внутри `runPlanningWithFeedback` (строки ориентировочно 118-135, внутри retry-closure):

```go
	o.runWithRetry(ctx, s, phasePlanning, func(retryContext string) error {
		feedbackData, _ := os.ReadFile(filepath.Join(stageDir, "feedback.md"))
		var prevPlan string
		planVersionRe := regexp.MustCompile(`^plan\.v(\d+)\.md$`)
		var bestVer int
		entries, _ := os.ReadDir(stageDir)
		for _, e := range entries {
			m := planVersionRe.FindStringSubmatch(e.Name())
			if m == nil {
				continue
			}
			v, _ := strconv.Atoi(m[1])
			if v > bestVer {
				bestVer = v
				data, _ := os.ReadFile(filepath.Join(stageDir, e.Name()))
				prevPlan = string(data)
			}
		}

		depPlans := CollectDependencyPlans(o.opts.RunDir, s, o.opts.Stages, func(depID, msg string) {
```

Заменить на:

```go
	o.runWithRetry(ctx, s, phasePlanning, func(retryContext string) error {
		feedbackData, _ := os.ReadFile(filepath.Join(stageDir, "feedback.md"))
		_, prevPlan, err := state.LatestPlanVersion(stageDir)
		if err != nil {
			return fmt.Errorf("read previous plan: %w", err)
		}

		depPlans := CollectDependencyPlans(o.opts.RunDir, s, o.opts.Stages, func(depID, msg string) {
```

(Только этот блок меняется — остальное тело `runPlanningWithFeedback` ниже, начиная с `depPlans := CollectDependencyPlans(...)`, остаётся как есть без изменений.)

- [ ] **Step 2: Обновить импорты**

В начале `pkg/orchestrator/agents.go` текущий блок импорта:

```go
import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/prompts"
)
```

Заменить на (удалены `"regexp"`, `"strconv"`; добавлен `"github.com/akopichin/afm/pkg/state"`):

```go
import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/prompts"
	"github.com/akopichin/afm/pkg/state"
)
```

- [ ] **Step 3: Собрать проект**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go build ./...`
Expected: чистая сборка (подтверждает, что `regexp`/`strconv` больше нигде в файле не используются и убраны верно, а `state` действительно нужен и подключён).

- [ ] **Step 4: Прогнать интеграционный тест resume-from-revising**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go test ./pkg/orchestrator/... -run TestIntegration_ResumeFromRevising -v`
Expected: PASS — этот тест раскладывает `plan.v1.md` вручную и проверяет, что фидбек попадает в промпт; должен продолжать проходить с новой реализацией чтения `prevPlan`.

- [ ] **Step 5: Прогнать весь пакет orchestrator + go vet**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go test ./pkg/orchestrator/... && go vet ./pkg/orchestrator/...`
Expected: PASS, без замечаний vet.

- [ ] **Step 6: Commit**

```bash
cd /Users/alexander.kopichin/work/personal/afm
git add pkg/orchestrator/agents.go
git commit -m "refactor(orchestrator): runPlanningWithFeedback читает prevPlan через state.LatestPlanVersion"
```

---

### Task 4: Полный e2e-тест живого пути Revise + прогон всего сьюта

**Files:**
- Modify: `pkg/orchestrator/integration_resume_test.go` (новый тест, использует уже существующие в этом файле хелперы `capturingPlanningRunner`, `doneCreatingRunner`, `mockPlanningScript`, `setupOrchestratorWithRunner`, `waitForStatus` из `integration_test.go` того же пакета)

**Interfaces:**
- Consumes: публичное поведение `orchestrator.New`, `orch.Run`, `orch.Revise` (не меняется этим планом) + `capturingPlanningRunner`/`mockPlanningScript`/`waitForStatus`/`setupOrchestratorWithRunner` (уже существуют в пакете `orchestrator_test`).
- Produces: ничего для последующих задач (финальная задача плана).

Существующие тесты покрывают либо `VersionPlan` в изоляции (без реального запуска `runPlanningWithFeedback` — `TestRevise_DurableTransition` в `approve_test.go` блокирует спавн семафором), либо восстановление после краша с ВРУЧНУЮ разложенными файлами (`TestIntegration_ResumeFromRevising` — `plan.v1.md` создан тестом напрямую, не через живой `VersionPlan`). Ни один существующий тест не проверяет ПОЛНУЮ живую цепочку: реальный `plan.md` → живой `Revise` → `VersionPlan` переименовывает → `runPlanningWithFeedback` читает через `LatestPlanVersion` → содержимое ОРИГИНАЛЬНОГО плана попадает в новый промпт. Эта задача добавляет именно такой тест.

- [ ] **Step 1: Добавить e2e-тест живого Revise**

Добавить в `pkg/orchestrator/integration_resume_test.go`, сразу после `TestIntegration_ResumeFromRevising` (после закрывающей `}` этой функции, перед комментарием `// TestResume_RevisingAutonomousStageUsesAutonomousFeedback`):

```go
// TestIntegration_ReviseFeedsBackOriginalPlanContent — полный живой путь
// Revise (не восстановление после краша): реальный plan.md, реальный вызов
// orch.Revise, реальная версионация через VersionPlan, реальное чтение через
// LatestPlanVersion. Закрывает пробел, который не закрывают ни
// TestRevise_DurableTransition (approve_test.go — блокирует спавн
// семафором, runPlanningWithFeedback не успевает выполниться), ни
// TestIntegration_ResumeFromRevising (plan.v1.md разложен тестом вручную,
// VersionPlan вообще не вызывается) — здесь же проверяется, что байты
// ОРИГИНАЛЬНОГО plan.md, написанного первым проходом planning, доходят до
// промпта ревизии в неизменном виде через полную цепочку VersionPlan →
// LatestPlanVersion.
func TestIntegration_ReviseFeedsBackOriginalPlanContent(t *testing.T) {
	stages := []flow.Stage{{
		ID: "a", Name: "A", Description: "revise round-trip",
		Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation},
	}}

	capture := &capturingPlanningRunner{delegate: mockRunner(t, mockPlanningScript)}
	runner := &doneCreatingRunner{delegate: capture}

	orch, runDir, stateFile := setupOrchestratorWithRunner(t, stages, runner)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _ = orch.Run(ctx) }()

	// Первый проход planning: стадия доходит до awaiting_approval с реальным
	// plan.md, написанным mockPlanningScript (никто его вручную не раскладывает).
	waitForStatus(t, stateFile, "a", state.StatusAwaitingApproval, 10*time.Second)

	if err := orch.Revise(context.Background(), "a", "please add error handling for edge case X"); err != nil {
		t.Fatalf("Revise: %v", err)
	}

	// После Revise planning перезапускается с фидбеком и снова доходит до
	// awaiting_approval — к этому моменту runPlanningWithFeedback уже
	// отработал и capture успел записать второй промпт.
	waitForStatus(t, stateFile, "a", state.StatusAwaitingApproval, 10*time.Second)

	stageDir := filepath.Join(runDir, "a")
	if _, err := os.Stat(filepath.Join(stageDir, "plan.v1.md")); err != nil {
		t.Fatalf("expected plan.v1.md to exist after Revise (VersionPlan should have archived it): %v", err)
	}

	capture.mu.Lock()
	prompts := append([]string{}, capture.prompts...)
	capture.mu.Unlock()
	if len(prompts) < 2 {
		t.Fatalf("expected at least 2 RunPlanning calls (initial + revise), got %d: %v", len(prompts), prompts)
	}

	revisePrompt := prompts[1]
	if !strings.Contains(revisePrompt, "Step 1: implement feature") {
		t.Errorf("expected revise prompt to include the ORIGINAL plan.md content (via VersionPlan -> LatestPlanVersion round-trip), got: %s", revisePrompt)
	}
	if !strings.Contains(revisePrompt, "please add error handling for edge case X") {
		t.Errorf("expected revise prompt to include the feedback note, got: %s", revisePrompt)
	}
}
```

- [ ] **Step 2: Запустить новый тест**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go test ./pkg/orchestrator/... -run TestIntegration_ReviseFeedsBackOriginalPlanContent -v`
Expected: PASS.

- [ ] **Step 3: Полный прогон Go-сьюта с race detector**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go build ./... && go vet ./... && go test ./... -race`
Expected: всё зелёное — включая `pkg/state`, `pkg/orchestrator`, и все остальные пакеты (это изменение не должно затрагивать их, но полный прогон подтверждает отсутствие незамеченных побочных эффектов).

- [ ] **Step 4: Полный прогон фронтенд-сьюта (для полноты — эта задача не трогает pkg/web/dashboard, но пользователь просил полный e2e-прогон)**

Run: `cd /Users/alexander.kopichin/work/personal/afm/pkg/web/dashboard && npm test && npm run typecheck`
Expected: всё зелёное, без изменений относительно текущего состояния (baseline).

- [ ] **Step 5: Commit**

```bash
cd /Users/alexander.kopichin/work/personal/afm
git add pkg/orchestrator/integration_resume_test.go
git commit -m "test(orchestrator): e2e-тест живого Revise — plan.md переживает VersionPlan+LatestPlanVersion без искажений"
```
