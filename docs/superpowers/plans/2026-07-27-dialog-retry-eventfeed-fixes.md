# Три фикса надёжности: dialog misplacement, cascade retry, event feed persistence — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Починить три независимых бага в afm: (1) агент прячет `question.json` от поллера, если пишет его не инструментом `Write`; (2) каскадно упавшие стадии (`blocked_by_dep`) не восстанавливаются ручным retry; (3) лента событий (event feed) в дашборде теряется при рефреше страницы и рестарте `afm run`.

**Architecture:** Backend на Go (`pkg/orchestrator`, `pkg/server`), frontend на React/TypeScript (`pkg/web/dashboard`). Все три фикса независимы друг от друга по коду, но задача 2 (mkdir в `runAutonomousAgent`) — предпосылка корректной работы задачи 1 (без `autonomous.flag` `dialogPhases()` не видит фазу `autonomous_execution`), поэтому задача 2 выполняется первой.

**Tech Stack:** Go 1.x (backend, без изменений go.mod — см. глобальное ограничение), React + TypeScript + Vite (dashboard), стандартная `testing` для Go, Vitest для фронта (см. `use-event-feed.test.ts`).

## Global Constraints

- Не менять версию Go в `go.mod` без предупреждения пользователя.
- После каждой правки — `make lint` должен быть чист (0 issues).
- Не добавлять устаревшие конструкции; код должен быть максимально понятен человеку.
- Коммиты — на русском языке, без `Co-Authored-By`.
- Всегда предпочитать простое сложному (см. `CLAUDE.md`: убрать код ← добавить, модифицировать существующее ← новое, константа ← переменная).
- Спек: `docs/superpowers/specs/2026-07-27-dialog-retry-eventfeed-fixes-design.md` — источник истины по архитектурным решениям, сверяться при любой неясности.

---

### Task 1: `runAutonomousAgent` создаёт свой `stageDir` и `autonomous.flag`

**Файлы:**
- Modify: `pkg/orchestrator/agents.go:307-309` (начало `runAutonomousAgent`)
- Test: `pkg/orchestrator/retry_cascade_test.go` (новый файл)

**Interfaces:**
- Consumes: `filepath.Join`, `os.MkdirAll`, `os.WriteFile` (stdlib); `o.opts.RunDir` (`Options.RunDir string`, уже существует); `o.Trigger(stageID string, ev FSMEvent, ctx GuardCtx, reason string) (state.StageStatus, bool)` (существующий метод `Orchestrator`, `pkg/orchestrator/orchestrator.go:209`); `EvFail FSMEvent` (существующая константа, `pkg/orchestrator/fsm.go`).
- Produces: после фикса `runAutonomousAgent` гарантирует, что `<RunDir>/<stageID>/` и `<RunDir>/<stageID>/autonomous.flag` существуют на диске до первого обращения к `<RunDir>/<stageID>/autonomous.log` — это предпосылка для Task 2 (`activeDialogPhase`/`dialogPhases` смотрят на `autonomous.flag`).

Текущий код (`pkg/orchestrator/agents.go:307-309`):
```go
func (o *Orchestrator) runAutonomousAgent(ctx context.Context, s flow.Stage) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)

	o.runWithRetry(ctx, s, phaseAutonomous, func(retryContext string) error {
```

- [ ] **Step 1: Написать падающий тест**

Создать `pkg/orchestrator/retry_cascade_test.go`:

```go
package orchestrator_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/state"
)

// cascadeRetryRunner: RunAgent для стадии "a" возвращает не-retryable ошибку
// на первом вызове (имитирует реальный сбой), затем успешно пишет
// execution_summary.md на всех последующих вызовах (успех после ручного retry).
// Стадия "b" (depends_on: ["a"]) при первом прогоне никогда не активируется —
// её каскадно валит failBlockedStages ДО того, как для неё создаётся stageDir.
type cascadeRetryRunner struct {
	mu       sync.Mutex
	aCalls   int
}

func (r *cascadeRetryRunner) RunPlanning(_ context.Context, _, _, _, _ string) error {
	return errors.New("not used in this test")
}

func (r *cascadeRetryRunner) RunAgent(_ context.Context, _, stageName, _, logFile string) error {
	stageDir := filepath.Dir(logFile)
	if stageName == "a" {
		r.mu.Lock()
		r.aCalls++
		n := r.aCalls
		r.mu.Unlock()
		if n == 1 {
			return errors.New("boom: first attempt always fails")
		}
	}
	return os.WriteFile(filepath.Join(stageDir, "execution_summary.md"), []byte("## Summary\ndone\n"), 0644)
}

func (r *cascadeRetryRunner) RunJSONQuery(_ context.Context, _ string) ([]byte, error) {
	return nil, errors.New("not used in this test")
}

var _ executor.Runner = (*cascadeRetryRunner)(nil)

// TestIntegration_CascadeFailedStageRetrySucceeds воспроизводит баг из
// прод-лога GogaFeature-20260726-133106-3c1c: стадия "a" падает, стадия "b"
// (depends_on: ["a"]) каскадно валится через blocked_by_dep, никогда не
// получив свой stageDir. Ручной retry "a" должен пройти успешно, а затем
// ручной retry "b" должен СОЗДАТЬ ей stageDir и autonomous.flag и успешно
// завершиться — а не падать вечно с "open log file: ... no such file or
// directory" (баг: retryStage спавнил runAutonomousAgent без MkdirAll).
func TestIntegration_CascadeFailedStageRetrySucceeds(t *testing.T) {
	dir := t.TempDir()
	stages := []flow.Stage{
		{ID: "a", Name: "a", Agents: []flow.AgentType{flow.AgentAuto}},
		{ID: "b", Name: "b", Agents: []flow.AgentType{flow.AgentAuto}, DependsOn: []string{"a"}},
	}

	store, err := state.Open(dir, []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	stateFile := filepath.Join(dir, "state.json")

	runner := &cascadeRetryRunner{}
	orch := orchestrator.New(orchestrator.Options{
		RunDir: dir,
		Stages: stages,
		Store:  store,
		Config: config.Default(),
		// DashboardURL непустой — Run() не должен завершиться сразу после
		// того, как обе стадии стали terminal (failed): нужен живой run-ctx
		// для ручных Retry() ниже, как в реальном прогоне с открытым дашбордом.
		DashboardURL: "http://test.invalid",
		Prompts:      orchestrator.DefaultPrompts(),
		Runner:       runner,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go func() { _ = orch.Run(ctx) }()

	waitForStatus(t, stateFile, "a", state.StatusFailed, 10*time.Second)
	waitForStatus(t, stateFile, "b", state.StatusFailed, 10*time.Second)

	// "b" никогда не активировалась — её директории на диске не существует.
	bDir := filepath.Join(dir, "b")
	if _, err := os.Stat(bDir); err == nil {
		t.Fatalf("expected %s to not exist before any activation, but it does", bDir)
	}

	if err := orch.Retry(ctx, "a"); err != nil {
		t.Fatalf("Retry(a): %v", err)
	}
	waitForStatus(t, stateFile, "a", state.StatusDone, 10*time.Second)

	if err := orch.Retry(ctx, "b"); err != nil {
		t.Fatalf("Retry(b): %v", err)
	}
	waitForStatus(t, stateFile, "b", state.StatusDone, 10*time.Second)

	if _, err := os.Stat(bDir); err != nil {
		t.Errorf("stageDir for b was not created by retry: %v", err)
	}
	if _, err := os.Stat(filepath.Join(bDir, "autonomous.flag")); err != nil {
		t.Errorf("autonomous.flag for b was not created by retry: %v", err)
	}
}
```

- [ ] **Step 2: Запустить тест и убедиться, что он падает**

Run: `go test ./pkg/orchestrator/... -run TestIntegration_CascadeFailedStageRetrySucceeds -v -timeout 60s`
Expected: FAIL — стадия "b" после `Retry(ctx, "b")` не доходит до `StatusDone` за 10с (падает с `open log file: ... no such file or directory` внутри `progress.NewLogger`, см. `pkg/progress/progress.go:21-24`), тест зависает на `waitForStatus` и завершается по её собственному `t.Fatalf`.

- [ ] **Step 3: Реализовать минимальный фикс**

В `pkg/orchestrator/agents.go` заменить:
```go
func (o *Orchestrator) runAutonomousAgent(ctx context.Context, s flow.Stage) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)

	o.runWithRetry(ctx, s, phaseAutonomous, func(retryContext string) error {
```
на:
```go
// runAutonomousAgent выполняет стадию в автономном треке — без plan.md и approval.
// Агент использует прикреплённые скиллы и обязан написать execution_summary.md
// по завершении (проверяется completion-check'ом checkAutonomousCompletion).
//
// Трек отличается от runImplementationAgent: нет чтения plan.md, нет .done,
// фаза — "autonomous_execution", используется Autonomous-шаблон промпта.
//
// MkdirAll + запись autonomous.flag в начале — тот же защитный паттерн, что
// уже есть в runPlanningAgent/runReviewAgent (см. их начало): стадия могла
// быть каскадно помечена failed через failBlockedStages/blocked_by_dep, так и
// не получив stageDir на диске (директория создаётся только при реальной
// активации). Без этого ручной retry такой стадии падал с
// "open log file: ... no such file or directory" — фикс в единой точке
// покрывает retry, resume-после-рестарта (recovery.go) и любой будущий caller.
func (o *Orchestrator) runAutonomousAgent(ctx context.Context, s flow.Stage) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		o.Trigger(s.ID, EvFail, GuardCtx{}, "mkdir failed")
		return
	}
	_ = os.WriteFile(filepath.Join(stageDir, "autonomous.flag"), nil, 0644)

	o.runWithRetry(ctx, s, phaseAutonomous, func(retryContext string) error {
```

(Существующий doc-комментарий над функцией — строки 301-306 — заменяется расширенной версией выше; тело функции после `runWithRetry(...)` не меняется.)

- [ ] **Step 4: Запустить тест и убедиться, что он проходит**

Run: `go test ./pkg/orchestrator/... -run TestIntegration_CascadeFailedStageRetrySucceeds -v -timeout 60s`
Expected: PASS

- [ ] **Step 5: Прогнать весь пакет orchestrator, чтобы не сломать существующее**

Run: `go test ./pkg/orchestrator/... -timeout 120s`
Expected: PASS (все существующие тесты, включая `TestIntegration_IncompleteRetry`, `integration_supervisor_test.go`, `scenario_test.go`)

- [ ] **Step 6: Линт и коммит**

Run: `make lint`
Expected: `0 issues`

```bash
git add pkg/orchestrator/agents.go pkg/orchestrator/retry_cascade_test.go
git commit -m "$(cat <<'EOF'
fix(orchestrator): runAutonomousAgent создаёт свой stageDir/autonomous.flag

Каскадно упавшие через blocked_by_dep стадии никогда не получали
директорию на диске (она создаётся только при реальной активации).
Ручной retry такой стадии падал с "open log file: ... no such file
or directory" — подтверждено прод-логом. runPlanningAgent/
runReviewAgent уже защищались от этого сами; теперь и
runAutonomousAgent делает то же самое.
EOF
)"
```

---

### Task 2: `relocateMisplacedQuestions` — скан файловой системы вместо парсинга `Write`-вызовов

**Файлы:**
- Modify: `pkg/orchestrator/dialog_poller.go` (функции `pollQuestions`, `relocateMisplacedQuestions`; удаляется `detectDialogViolation`'s зависимость не трогаем — она отдельная, неиспользуемая в проде функция, вне скоупа)
- Modify: `pkg/orchestrator/orchestrator.go` (добавить поле `lastRootScan map[string]time.Time` в `Orchestrator`, инициализация в `New`)
- Modify: `pkg/orchestrator/integration_interactive_test.go:291-410` (адаптировать `TestIntegration_MisplacedQuestionRelocated`/`TestIntegration_MisprefixedQuestionNormalized` под новый механизм — старый полагался на эмиссию stream-json `Write` tool_use, которую убираем)
- Test: правки в существующих двух тестах + новый тест на bare-имя без префикса и safety-guard при ≥2 активных interactive-стадиях (в том же файле)

**Interfaces:**
- Consumes: `o.opts.RootDir string` (существует, `Options.RootDir`); `dialogPhases(stageDir string) []string` (существует, `pkg/orchestrator/dialog_poller.go:147-153`); `jsonlFileForPhase(phase string) string` (существует); `pathInside(file, dir string) bool` (существует, `pkg/orchestrator/dialog_poller.go:225-238`).
- Produces: `relocateMisplacedQuestions(stageID, stageDir string, allowRootScan bool)` — новая сигнатура (было `relocateMisplacedQuestions(stageDir string)`); `activeDialogPhase(stageDir string) string` — новая функция, возвращает `""`, если ни один `<phase>.jsonl` ещё не существует.

Текущий код (`pkg/orchestrator/dialog_poller.go`, актуальные строки — см. `pollQuestions` целиком и `relocateMisplacedQuestions`/`detectDialogViolation` уже прочитаны в контексте сессии):

- [ ] **Step 1: Написать падающие тесты**

Заменить в `pkg/orchestrator/integration_interactive_test.go` тело `TestIntegration_MisplacedQuestionRelocated` (строки 291-357) на версию без эмиссии `Write` tool_use (новый механизм её не читает — находит файл сканом `root_dir`):

```go
func TestIntegration_MisplacedQuestionRelocated(t *testing.T) {
	dir := t.TempDir()
	rootDir := filepath.Join(dir, "project")
	if err := os.MkdirAll(rootDir, 0755); err != nil {
		t.Fatal(err)
	}

	// «Неправильная» директория: агент по CWD-багу пишет вопрос прямо в
	// верхний уровень root_dir (см. дизайн: bare-relative-write паттерн),
	// а не в $AFM_STAGE_DIR.
	wrongQuestion := filepath.Join(rootDir, "planning.q1.question.json")
	wrongAnswer := filepath.Join(rootDir, "planning.q1.answer.json")

	scriptPath := filepath.Join(dir, "misplacedagent.sh")
	script := "#!/bin/bash\n" +
		fmt.Sprintf(`echo '{"id":"q1","question":"where?","options":["a","b"]}' > %q`+"\n", wrongQuestion) +
		"sleep 30\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	stages := []flow.Stage{{
		ID:          "propose",
		Name:        "Propose",
		Description: "interactive planning that writes question outside stageDir",
		Agents:      []flow.AgentType{flow.AgentPlanning},
		Interactive: true,
		Command:     scriptPath,
	}}

	store, err := state.Open(dir, []string{"propose"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	stateFile := filepath.Join(dir, "state.json")

	orch := orchestrator.New(orchestrator.Options{
		RunDir:  dir,
		Stages:  stages,
		Store:   store,
		Config:  config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
		RootDir: rootDir,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go func() { _ = orch.Run(ctx) }()

	waitForStatus(t, stateFile, "propose", state.StatusAwaitingUserInput, 15*time.Second)

	stageDir := filepath.Join(dir, "propose")

	if _, err := os.Stat(filepath.Join(stageDir, "planning.q1.question.json")); err != nil {
		t.Errorf("relocated question missing in stageDir: %v", err)
	}
	link, err := os.Readlink(wrongAnswer)
	if err != nil {
		t.Errorf("expected answer symlink at %s: %v", wrongAnswer, err)
	} else if link != filepath.Join(stageDir, "planning.q1.answer.json") {
		t.Errorf("answer symlink points to %q, want %q", link, filepath.Join(stageDir, "planning.q1.answer.json"))
	}
}
```

Аналогично убрать эмиссию `Write` tool_use из скрипта в `TestIntegration_MisprefixedQuestionNormalized` (строки 366-410ish) — этот тест кладёт файл ВНУТРЬ `$AFM_STAGE_DIR` с неверным префиксом, что новый механизм ловит без изменений (сканирует `stageDir` напрямую), поэтому единственная правка — удалить строку `echo '{"type":"assistant",...tool_use...Write...}'` из скрипта (она больше не нужна и не читается).

Добавить в тот же файл новый тест на bare-имя без префикса:

```go
// TestIntegration_BareQuestionFilenameNormalized: агент пишет вопрос вообще
// без префикса ("q1.question.json" вместо "planning.q1.question.json") прямо
// в root_dir. relocateMisplacedQuestions должен считать всё имя целиком id и
// подставить активную фазу (planning — единственный <phase>.jsonl, который
// успел появиться на диске к этому моменту).
func TestIntegration_BareQuestionFilenameNormalized(t *testing.T) {
	dir := t.TempDir()
	rootDir := filepath.Join(dir, "project")
	if err := os.MkdirAll(rootDir, 0755); err != nil {
		t.Fatal(err)
	}

	wrongQuestion := filepath.Join(rootDir, "q1.question.json")
	wrongAnswer := filepath.Join(rootDir, "q1.answer.json")

	scriptPath := filepath.Join(dir, "bareagent.sh")
	script := "#!/bin/bash\n" +
		fmt.Sprintf(`echo '{"id":"q1","question":"where?","options":["a","b"]}' > %q`+"\n", wrongQuestion) +
		"sleep 30\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	stages := []flow.Stage{{
		ID:          "propose",
		Name:        "Propose",
		Description: "interactive planning that writes a bare-named question file",
		Agents:      []flow.AgentType{flow.AgentPlanning},
		Interactive: true,
		Command:     scriptPath,
	}}

	store, err := state.Open(dir, []string{"propose"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	stateFile := filepath.Join(dir, "state.json")

	orch := orchestrator.New(orchestrator.Options{
		RunDir:  dir,
		Stages:  stages,
		Store:   store,
		Config:  config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
		RootDir: rootDir,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go func() { _ = orch.Run(ctx) }()

	waitForStatus(t, stateFile, "propose", state.StatusAwaitingUserInput, 15*time.Second)

	stageDir := filepath.Join(dir, "propose")
	if _, err := os.Stat(filepath.Join(stageDir, "planning.q1.question.json")); err != nil {
		t.Errorf("bare-named question was not normalized into stageDir: %v", err)
	}
	if _, err := os.Readlink(wrongAnswer); err != nil {
		t.Errorf("expected answer symlink at %s: %v", wrongAnswer, err)
	}
}
```

- [ ] **Step 2: Запустить тесты и убедиться, что они падают**

Run: `go test ./pkg/orchestrator/... -run 'TestIntegration_MisplacedQuestionRelocated|TestIntegration_BareQuestionFilenameNormalized' -v -timeout 60s`
Expected: FAIL — `TestIntegration_MisplacedQuestionRelocated` не находит `planning.q1.question.json` в `stageDir` (старый механизм читал `Write`-эмиссию, которой мы больше не пишем; `RootDir` в `Options` ещё никто не сканирует). `TestIntegration_BareQuestionFilenameNormalized` падает по той же причине.

- [ ] **Step 3: Реализовать `activeDialogPhase` и переписать `relocateMisplacedQuestions`**

В `pkg/orchestrator/dialog_poller.go` заменить старую реализацию `relocateMisplacedQuestions` (весь блок, включая doc-комментарий, между `dialogPhases` и `pathInside`) на:

```go
// relocateScanInterval — throttle для скана root_dir внутри
// relocateMisplacedQuestions: это запасная сеть, а не основной путь, лишняя
// частота (раз в секунду, как основной poll-тик) не нужна.
const relocateScanInterval = 5 * time.Second

// activeDialogPhase возвращает фазу, чей <phase>.jsonl изменялся последним
// среди dialogPhases(stageDir) — это и есть фаза, в которой сейчас реально
// работает агент (одновременно активна только одна). Пустая строка, если
// агент ещё не начал писать ни в одну фазу (ни один <phase>.jsonl не создан).
func activeDialogPhase(stageDir string) string {
	var latest string
	var latestMod time.Time
	for _, phase := range dialogPhases(stageDir) {
		info, err := os.Stat(filepath.Join(stageDir, jsonlFileForPhase(phase)))
		if err != nil {
			continue
		}
		if latest == "" || info.ModTime().After(latestMod) {
			latest = phase
			latestMod = info.ModTime()
		}
	}
	return latest
}

// collectQuestionFiles добавляет в into абсолютные пути всех *.question.json
// в верхнем уровне dir (без рекурсии в поддиректории).
func collectQuestionFiles(dir string, into map[string]bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".question.json") {
			continue
		}
		into[filepath.Join(dir, e.Name())] = true
	}
}

// relocateMisplacedQuestions чинит два способа, которыми агент может «спрятать»
// файл вопроса от поллера, и оба ведут к вечному зависанию стадии:
//
//  1. Неверная ДИРЕКТОРИЯ: question.json записан вне stageDir (агент строит
//     путь из CWD вместо $AFM_STAGE_DIR).
//  2. Неверный ПРЕФИКС: файл лежит внутри stageDir, но назван не по
//     канонической фазе (напр. "commit-changes.q1.question.json" или вообще
//     без префикса "q1.question.json").
//
// В отличие от прежней реализации, НЕ парсит stream-json лог агента в поисках
// вызова инструмента Write — сканирует файловую систему напрямую, поэтому не
// зависит от того, каким инструментом (Write, Bash echo/heredoc, кастомный
// скилл-скрипт) агент создал файл.
//
// Скан stageDir (случай 2) выполняется всегда — дёшево, там всего несколько
// файлов. Скан root_dir (случай 1, allowRootScan) — throttled раз в
// relocateScanInterval на стадию и ТОЛЬКО когда allowRootScan=true: если
// параллельно активно ≥2 interactive-стадий без открытого вопроса, у файла в
// root_dir нет однозначного адресата (в имени только phase+id, без stageID) —
// безопаснее оставить стадию висеть ещё один тик поллера, чем угадать неверно
// (см. дизайн-документ, "Безопасность при нескольких параллельных
// interactive-стадиях").
func (o *Orchestrator) relocateMisplacedQuestions(stageID, stageDir string, allowRootScan bool) {
	phase := activeDialogPhase(stageDir)
	if phase == "" {
		return // агент ещё не начал писать ни в одну фазу — сканировать нечего
	}

	candidates := map[string]bool{}
	collectQuestionFiles(stageDir, candidates)

	if allowRootScan && o.opts.RootDir != "" && !pathInside(o.opts.RootDir, stageDir) {
		last, seen := o.lastRootScan[stageID]
		if !seen || time.Since(last) >= relocateScanInterval {
			o.lastRootScan[stageID] = time.Now()
			collectQuestionFiles(o.opts.RootDir, candidates)
		}
	}

	for f := range candidates {
		o.normalizeMisplacedQuestion(f, stageDir, phase)
	}
}

// normalizeMisplacedQuestion нормализует один найденный файл в канонический
// путь "<phase>.<id>.question.json" внутри stageDir и создаёт dangling-симлинк
// на будущий answer.json по (неверному) пути, который опрашивает агент.
//
// Разбор имени: "<prefix>.<id>.question.json" → id (префикс отбрасывается,
// неважно, стадии он или фазы). Если в имени нет точки-разделителя вообще
// (агент написал голый "q1.question.json") — id это всё имя целиком.
func (o *Orchestrator) normalizeMisplacedQuestion(f, stageDir, phase string) {
	base := filepath.Base(f)
	trimmed := strings.TrimSuffix(base, ".question.json")
	id := trimmed
	if dot := strings.Index(trimmed, "."); dot >= 0 {
		id = trimmed[dot+1:]
	}
	if id == "" {
		return
	}
	dstBase := phase + "." + id + ".question.json"
	dst := filepath.Join(stageDir, dstBase)

	if _, err := os.Stat(dst); err == nil {
		return // уже на каноническом месте (либо f==dst, либо нормализовано ранее)
	}
	data, err := os.ReadFile(f)
	if err != nil {
		log.Printf("WARN: normalize question %s: read: %v", f, err)
		return
	}
	if err := os.WriteFile(dst, data, 0644); err != nil {
		log.Printf("WARN: normalize question %s → %s: write: %v", f, dst, err)
		return
	}
	wrongAnswer := filepath.Join(filepath.Dir(f), trimmed+".answer.json")
	rightAnswer := filepath.Join(stageDir, phase+"."+id+".answer.json")
	if _, err := os.Lstat(wrongAnswer); err != nil {
		_ = os.MkdirAll(filepath.Dir(wrongAnswer), 0755)
		_ = os.Symlink(rightAnswer, wrongAnswer)
	}
	log.Printf("INFO: normalized misplaced question %s → %s (symlink answer)", f, dst)
}
```

Удалить старую функцию `detectDialogViolation`? **Нет** — она больше не вызывается из `pollQuestions`, но остаётся из-за существующего теста `dialog_violation_test.go` (не в скоупе этой задачи, трогать не нужно — она уже была помечена в CLAUDE.md как "прежнее поведение, заменено relocate", т.е. неиспользуемый в проде путь и до этой задачи).

Обновить вызов в `pollQuestions` — найти блок:
```go
		if len(questions) == 0 {
			if stage := o.graph.Stage(stageID); stage != nil && stage.Interactive {
				o.relocateMisplacedQuestions(stageDir)
			}
		}
```
и заменить на:
```go
		if len(questions) == 0 {
			if stage := o.graph.Stage(stageID); stage != nil && stage.Interactive {
				o.relocateMisplacedQuestions(stageID, stageDir, activeInteractiveCount <= 1)
			}
		}
```

Добавить вычисление `activeInteractiveCount` в начало `pollQuestions` (перед циклом `for stageID, st := range snap.Stages`):
```go
	activeInteractiveCount := 0
	for id, st := range snap.Stages {
		switch st.Status {
		case state.StatusPlanning, state.StatusRunning, state.StatusRevising,
			state.StatusRetrying, state.StatusAwaitingUserInput:
		default:
			continue
		}
		if stage := o.graph.Stage(id); stage != nil && stage.Interactive {
			activeInteractiveCount++
		}
	}
```

Добавить поле в `Orchestrator` (`pkg/orchestrator/orchestrator.go`, рядом с `violationCache`):
```go
	lastRootScan map[string]time.Time // stageID → время последнего скана root_dir (throttle в relocateMisplacedQuestions)
```
и инициализацию в `New(...)` рядом с инициализацией `violationCache`:
```go
		lastRootScan: make(map[string]time.Time),
```

- [ ] **Step 4: Запустить тесты и убедиться, что они проходят**

Run: `go test ./pkg/orchestrator/... -run 'TestIntegration_MisplacedQuestionRelocated|TestIntegration_MisprefixedQuestionNormalized|TestIntegration_BareQuestionFilenameNormalized' -v -timeout 60s`
Expected: PASS (все три)

- [ ] **Step 5: Прогнать весь пакет**

Run: `go test ./pkg/orchestrator/... -timeout 120s`
Expected: PASS

- [ ] **Step 6: Линт и коммит**

Run: `make lint`
Expected: `0 issues`

```bash
git add pkg/orchestrator/dialog_poller.go pkg/orchestrator/orchestrator.go pkg/orchestrator/integration_interactive_test.go
git commit -m "$(cat <<'EOF'
fix(orchestrator): relocateMisplacedQuestions сканирует ФС вместо парсинга Write-вызовов

Старый механизм находил "спрятанный" question.json, только если агент
создал его инструментом Write (парсинг <phase>.jsonl). Если агент писал
файл через Bash (heredoc/echo) — что и предписывает bash-poll-loop
протокол в промптах — обнаружение молчало, вопрос никогда не попадал
в UI, стадия зависала навсегда (колеге пришлось создавать answer.json
руками). Теперь ищем осиротевшие *.question.json прямым сканом
файловой системы (stageDir + верхний уровень root_dir, throttled),
независимо от того, каким инструментом агент создал файл. Активная
фаза определяется по самому свежему <phase>.jsonl. Добавлена защита
от неоднозначности при нескольких параллельных interactive-стадиях.
EOF
)"
```

---

### Task 3: `notices.jsonl` — сайдкар для `agent_completed`/`context_warning`

**Файлы:**
- Create: `pkg/orchestrator/notices.go`
- Modify: `pkg/orchestrator/retry.go` (2 call site: строки с `EventAgentCompleted`)
- Modify: `pkg/orchestrator/agents.go` (5 call sites с `EventContextWarning`)
- Test: `pkg/orchestrator/notices_test.go` (новый файл)

**Interfaces:**
- Produces: `appendNotice(runDir, stageID, eventType string, data any)` — новая функция, дописывает строку в `<runDir>/notices.jsonl`. Потребляется Task 4 (backend reconstruction endpoint читает этот файл).

- [ ] **Step 1: Написать падающий тест**

Создать `pkg/orchestrator/notices_test.go`:

```go
package orchestrator

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAppendNotice(t *testing.T) {
	runDir := t.TempDir()

	appendNotice(runDir, "stage-a", "agent_completed", "planning")
	appendNotice(runDir, "stage-b", "context_warning", "dep-x: context too large")

	f, err := os.Open(filepath.Join(runDir, "notices.jsonl"))
	if err != nil {
		t.Fatalf("notices.jsonl not created: %v", err)
	}
	defer f.Close()

	var lines []noticeEntry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e noticeEntry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("unmarshal line %q: %v", sc.Text(), err)
		}
		lines = append(lines, e)
	}
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	if lines[0].StageID != "stage-a" || lines[0].Type != "agent_completed" {
		t.Errorf("line 0 = %+v, want stage-a/agent_completed", lines[0])
	}
	if lines[1].StageID != "stage-b" || lines[1].Type != "context_warning" {
		t.Errorf("line 1 = %+v, want stage-b/context_warning", lines[1])
	}
	if lines[0].Time.IsZero() {
		t.Error("line 0 Time is zero, want a real timestamp")
	}
}
```

- [ ] **Step 2: Запустить тест и убедиться, что он падает**

Run: `go test ./pkg/orchestrator/... -run TestAppendNotice -v -timeout 30s`
Expected: FAIL с `undefined: appendNotice` / `undefined: noticeEntry` (compile error — функция ещё не существует)

- [ ] **Step 3: Реализовать `appendNotice`**

Создать `pkg/orchestrator/notices.go`:

```go
package orchestrator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// noticeEntry — одна строка side-car notices.jsonl: UI-уведомления без
// FSM-перехода (agent_completed для отдельной фазы стадии, context_warning).
// events.jsonl намеренно не трогаем — это единственный источник правды с
// CAS-инвариантом (Store.Apply: строгая проверка current != t.From), а эти
// два типа уведомлений не соответствуют реальному переходу статуса стадии.
// Файл run-level (как уже существующий supervisor.jsonl), не per-stage.
type noticeEntry struct {
	Time    time.Time `json:"time"`
	Type    string    `json:"type"`
	StageID string    `json:"stage_id"`
	Data    any       `json:"data,omitempty"`
}

// appendNotice дописывает одну строку в <runDir>/notices.jsonl. Ошибка
// нефатальна (это вспомогательный файл для истории event feed, не источник
// правды) — молча игнорируется, как и dialog.jsonl в файловом протоколе.
func appendNotice(runDir, stageID, eventType string, data any) {
	entry := noticeEntry{Time: time.Now(), Type: eventType, StageID: stageID, Data: data}
	line, err := json.Marshal(entry)
	if err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(runDir, "notices.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}
```

- [ ] **Step 4: Запустить тест и убедиться, что он проходит**

Run: `go test ./pkg/orchestrator/... -run TestAppendNotice -v -timeout 30s`
Expected: PASS

- [ ] **Step 5: Подключить `appendNotice` в местах публикации `EventAgentCompleted`/`EventContextWarning`**

В `pkg/orchestrator/retry.go` (2 места) — заменить каждое из:
```go
				_ = o.critical.Publish(ctx, Event{Type: EventAgentCompleted, StageID: s.ID, Data: phase})
```
на:
```go
				appendNotice(o.opts.RunDir, s.ID, string(EventAgentCompleted), phase)
				_ = o.critical.Publish(ctx, Event{Type: EventAgentCompleted, StageID: s.ID, Data: phase})
```
(оба вхождения идентичны, `phase` — параметр функции `runWithRetry`, уже в скоупе в обеих точках).

В `pkg/orchestrator/agents.go` (5 мест) — заменить каждое из:
```go
			o.ui.Publish(Event{Type: EventContextWarning, StageID: s.ID, Data: fmt.Sprintf("%s: %s", depID, msg)})
```
на:
```go
			appendNotice(o.opts.RunDir, s.ID, string(EventContextWarning), fmt.Sprintf("%s: %s", depID, msg))
			o.ui.Publish(Event{Type: EventContextWarning, StageID: s.ID, Data: fmt.Sprintf("%s: %s", depID, msg)})
```
(все 5 вхождений — идентичные замыкания `func(depID, msg string) { ... }`, передаваемые в `CollectDependencyPlans`; `s.ID` уже в скоупе во всех пяти).

- [ ] **Step 6: Прогнать весь пакет**

Run: `go test ./pkg/orchestrator/... -timeout 120s`
Expected: PASS

- [ ] **Step 7: Линт и коммит**

Run: `make lint`
Expected: `0 issues`

```bash
git add pkg/orchestrator/notices.go pkg/orchestrator/notices_test.go pkg/orchestrator/retry.go pkg/orchestrator/agents.go
git commit -m "$(cat <<'EOF'
feat(orchestrator): notices.jsonl — сайдкар для agent_completed/context_warning

agent_completed и context_warning публиковались только в UIBus/critical
bus (чистый in-memory fan-out, без истории) — не переживали ни рефреш
дашборда, ни рестарт afm run. Остальные типы событий ленты уже
переживают оба случая (events.jsonl/<phase>.jsonl/supervisor.jsonl).
Трогать схему events.jsonl ради этих двух типов — плохой размен (CAS-
инвариант Store.Apply не допускает "фейковых" transition с From==To),
поэтому заводим отдельный run-level файл по аналогии с уже
существующим supervisor.jsonl. Предпосылка для Task 4.
EOF
)"
```

---

### Task 4: Backend `GET /api/events` — реплей истории ленты событий

**Файлы:**
- Create: `pkg/server/events_handler.go`
- Modify: `pkg/server/server.go` (регистрация роута `/api/events`)
- Test: `pkg/server/events_handler_test.go` (новый файл)

**Interfaces:**
- Consumes: `s.store.History() ([]state.Transition, error)` (существует, `pkg/state/store.go:206`); `s.store.Snapshot() state.RunState` (существует); `s.runDir string` (существующее поле `Server`); `executor.ParseToolAction(line string, limit int) (toolName, detail string, ok bool)` (существует, `pkg/executor/executor.go`, экспортирована).
- Produces: `GET /api/events` → JSON-массив `feedEvent` (см. ниже), отсортированный по времени, обрезанный до последних 200. `feedEvent{Type, StageID, Data, Timestamp, Seq}` — новый экспортируемый (для JSON) тип, потребляется Task 5 (frontend).

- [ ] **Step 1: Написать падающий тест**

Создать `pkg/server/events_handler_test.go`:

```go
package server

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/akopichin/afm/pkg/state"
)

func TestHandleEvents_ReplaysTransitionsAndNotices(t *testing.T) {
	srv, runDir := setupTestServer(t)

	// events.jsonl уже содержит одну transition из setupTestServerWithWS
	// (StatusPending → StatusAwaitingApproval, Event: "test_setup") — плюс
	// добавим ask_user, чтобы проверить, что она реплеится как отдельный тип.
	if err := srv.store.Apply(state.Transition{
		StageID: testStageID, From: state.StatusAwaitingApproval, To: state.StatusAwaitingUserInput,
		Event: "ask_user",
	}); err != nil {
		t.Fatal(err)
	}

	// notices.jsonl (Task 3 sidecar) — одна строка agent_completed.
	noticesLine := `{"time":"2026-07-27T10:00:00Z","type":"agent_completed","stage_id":"s1","data":"planning"}` + "\n"
	if err := os.WriteFile(filepath.Join(runDir, "notices.jsonl"), []byte(noticesLine), 0644); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/api/events", nil)
	w := httptest.NewRecorder()
	srv.handleEvents(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var events []feedEvent
	if err := json.Unmarshal(w.Body.Bytes(), &events); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	var sawStatusChanged, sawAskUser, sawAgentCompleted bool
	for _, e := range events {
		switch e.Type {
		case "stage_status_changed":
			sawStatusChanged = true
		case "ask_user":
			sawAskUser = true
		case "agent_completed":
			sawAgentCompleted = true
			if e.Data != "planning" {
				t.Errorf("agent_completed Data = %v, want %q", e.Data, "planning")
			}
		}
	}
	if !sawStatusChanged {
		t.Error("expected at least one stage_status_changed event")
	}
	if !sawAskUser {
		t.Error("expected an ask_user event derived from the ask_user transition")
	}
	if !sawAgentCompleted {
		t.Error("expected an agent_completed event from notices.jsonl")
	}
}

func TestHandleEvents_CapsAt200(t *testing.T) {
	srv, _ := setupTestServer(t)
	for i := 0; i < 250; i++ {
		from := state.StatusAwaitingApproval
		if i > 0 {
			from = state.StatusRunning
		}
		to := state.StatusRunning
		if i == 249 {
			to = state.StatusDone
		}
		if err := srv.store.Apply(state.Transition{StageID: testStageID, From: from, To: to, Event: "noop"}); err != nil {
			// CAS может не совпасть при таком синтетическом чередовании —
			// тесту важно только итоговое количество записей в логе, не
			// валидность каждого перехода, поэтому игнорируем ошибку CAS.
			continue
		}
	}

	req := httptest.NewRequest("GET", "/api/events", nil)
	w := httptest.NewRecorder()
	srv.handleEvents(w, req)

	var events []feedEvent
	if err := json.Unmarshal(w.Body.Bytes(), &events); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(events) > 200 {
		t.Errorf("got %d events, want <= 200", len(events))
	}
}
```

- [ ] **Step 2: Запустить тесты и убедиться, что они падают**

Run: `go test ./pkg/server/... -run 'TestHandleEvents' -v -timeout 30s`
Expected: FAIL с `undefined: feedEvent` / `srv.handleEvents undefined` (compile error)

- [ ] **Step 3: Реализовать `handleEvents`**

Создать `pkg/server/events_handler.go`:

```go
package server

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/state"
)

// maxReplayEvents ограничивает историю, отдаваемую /api/events, последними
// 200 записями — совпадает с MAX_EVENTS во фронте (use-event-feed.ts):
// отдавать больше бессмысленно, клиент всё равно обрежет.
const maxReplayEvents = 200

// feedEvent — одна запись реплея истории ленты событий. Seq заполняется
// только для событий, производных от реальной FSM-transition (events.jsonl) —
// это стабильный ключ дедупликации на фронте при слиянии с live-потоком
// WebSocket. Для остальных типов (agent_action/supervisor_decision/notices)
// Seq остаётся нулевым.
type feedEvent struct {
	Type      string    `json:"type"`
	StageID   string    `json:"stage_id"`
	Data      any       `json:"data,omitempty"`
	Timestamp time.Time `json:"timestamp"`
	Seq       uint64    `json:"seq,omitempty"`
}

func (s *Server) handleEvents(w http.ResponseWriter, _ *http.Request) {
	events := s.reconstructEventHistory()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(events)
}

func (s *Server) reconstructEventHistory() []feedEvent {
	var out []feedEvent

	history, _ := s.store.History()
	for _, t := range history {
		out = append(out, transitionToFeedEvents(t)...)
	}

	snap := s.store.Snapshot()
	for stageID := range snap.Stages {
		out = append(out, reconstructAgentActions(s.runDir, stageID)...)
	}

	out = append(out, reconstructSupervisorDecisions(s.runDir)...)
	out = append(out, reconstructNotices(s.runDir)...)

	sort.Slice(out, func(i, j int) bool { return out[i].Timestamp.Before(out[j].Timestamp) })
	if len(out) > maxReplayEvents {
		out = out[len(out)-maxReplayEvents:]
	}
	return out
}

// transitionToFeedEvents мапит одну FSM-transition в one-or-two feedEvent —
// повторяя ровно то, что публикует live-система: Trigger (orchestrator.go)
// ВСЕГДА публикует stage_status_changed при любом переходе; ask_user/
// user_answered/retry_scheduled/retry_exhausted публикуются ДОПОЛНИТЕЛЬНО из
// своих конкретных call site (dialog_poller.go/retry.go) — узнаваемы по
// Event/Reason самой transition, других живых типов (approved/revised/
// manual_retry) в проде сегодня никто не публикует, поэтому и здесь не
// реконструируем.
func transitionToFeedEvents(t state.Transition) []feedEvent {
	out := []feedEvent{{
		Type: "stage_status_changed", StageID: t.StageID, Data: string(t.To),
		Timestamp: t.Time, Seq: t.Seq,
	}}
	switch {
	case t.Event == "ask_user":
		out = append(out, feedEvent{Type: "ask_user", StageID: t.StageID, Timestamp: t.Time, Seq: t.Seq})
	case t.Event == "user_answered":
		out = append(out, feedEvent{Type: "user_answered", StageID: t.StageID, Timestamp: t.Time, Seq: t.Seq})
	case t.Event == "schedule_retry":
		out = append(out, feedEvent{Type: "retry_scheduled", StageID: t.StageID, Data: t.Reason, Timestamp: t.Time, Seq: t.Seq})
	case t.Event == "fail" && t.Reason == "retries exhausted":
		out = append(out, feedEvent{Type: "retry_exhausted", StageID: t.StageID, Timestamp: t.Time, Seq: t.Seq})
	}
	return out
}

// reconstructAgentActions парсит <phase>.jsonl каждой фазы стадии тем же
// парсером, что и живой лог (executor.ParseToolAction), и распределяет
// найденные действия равномерно во времени между началом и концом фазы
// (границы — из events.jsonl). Stream-json не содержит per-строчного
// timestamp — точный порядок внутри фазы сохраняется (индекс в файле), но
// точное время интерполируется, а не измеряется.
func reconstructAgentActions(runDir, stageID string) []feedEvent {
	stageDir := filepath.Join(runDir, stageID)
	var out []feedEvent
	for _, phase := range []string{"planning", "implementation", "review", "autonomous"} {
		jsonlName := phase + ".jsonl"
		if phase == "autonomous" {
			jsonlName = "autonomous.jsonl"
		}
		path := filepath.Join(stageDir, jsonlName)
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		lines := readLines(path)
		if len(lines) == 0 {
			continue
		}
		// Окно интерполяции: [mtime - len(lines)*shortStep, mtime]. Абсолютная
		// точность не нужна — важно, чтобы записи одной фазы шли по порядку и
		// попадали в примерно правильный участок общей ленты.
		end := info.ModTime()
		step := time.Second
		start := end.Add(-time.Duration(len(lines)) * step)
		for i, line := range lines {
			toolName, detail, ok := executor.ParseToolAction(line, 200)
			if !ok {
				continue
			}
			out = append(out, feedEvent{
				Type:    "agent_action",
				StageID: stageID,
				Data:    map[string]string{"tool": toolName, "detail": detail},
				Timestamp: start.Add(time.Duration(i) * step),
			})
		}
	}
	return out
}

func readLines(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines
}

// reconstructSupervisorDecisions читает run-level supervisor.jsonl
// (пишется logSupervisorDecision, pkg/orchestrator/supervisor_track.go).
func reconstructSupervisorDecisions(runDir string) []feedEvent {
	path := filepath.Join(runDir, "supervisor.jsonl")
	var out []feedEvent
	for _, line := range readLines(path) {
		var e struct {
			Ts       string `json:"ts"`
			StageID  string `json:"stage_id"`
			Decision string `json:"decision"`
			Reason   string `json:"reason"`
		}
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		ts, err := time.Parse(time.RFC3339, e.Ts)
		if err != nil {
			continue
		}
		out = append(out, feedEvent{
			Type: "supervisor_decision", StageID: e.StageID,
			Data:      map[string]any{"can_execute_autonomously": e.Decision == "autonomous", "reason": e.Reason},
			Timestamp: ts,
		})
	}
	return out
}

// reconstructNotices читает run-level notices.jsonl (Task 3, appendNotice).
func reconstructNotices(runDir string) []feedEvent {
	path := filepath.Join(runDir, "notices.jsonl")
	var out []feedEvent
	for _, line := range readLines(path) {
		var e struct {
			Time    time.Time `json:"time"`
			Type    string    `json:"type"`
			StageID string    `json:"stage_id"`
			Data    any       `json:"data,omitempty"`
		}
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		out = append(out, feedEvent{Type: e.Type, StageID: e.StageID, Data: e.Data, Timestamp: e.Time})
	}
	return out
}
```

Зарегистрировать роут в `pkg/server/server.go` рядом с существующими (`mux.HandleFunc("/api/status", s.handleStatus)`):
```go
	mux.HandleFunc("/api/events", s.handleEvents)
```

- [ ] **Step 4: Запустить тесты и убедиться, что они проходят**

Run: `go test ./pkg/server/... -run 'TestHandleEvents' -v -timeout 30s`
Expected: PASS

- [ ] **Step 5: Прогнать весь пакет**

Run: `go test ./pkg/server/... -timeout 60s`
Expected: PASS

- [ ] **Step 6: Линт и коммит**

Run: `make lint`
Expected: `0 issues`

```bash
git add pkg/server/events_handler.go pkg/server/server.go pkg/server/events_handler_test.go
git commit -m "$(cat <<'EOF'
feat(server): GET /api/events — реплей истории ленты событий

UIBus — чистый in-memory fan-out без истории, лента в дашборде
терялась при любом рефреше страницы и полностью — при рестарте afm
run. Новый эндпоинт восстанавливает историю из уже существующих
durable-источников (events.jsonl, <phase>.jsonl, supervisor.jsonl,
notices.jsonl из Task 3) — без нового формата для FSM-переходов,
agent_action и supervisor_decision. Обрезка до последних 200 записей.
EOF
)"
```

---

### Task 5: Frontend — `useEventFeed` фетчит историю и переживает рефреш

**Файлы:**
- Modify: `pkg/web/dashboard/src/hooks/use-event-feed/use-event-feed.ts`
- Modify: `pkg/web/dashboard/src/types/afm-event.ts` (добавить опциональные `seq`/уточнить комментарий про timestamp)
- Modify: `pkg/web/dashboard/src/hooks/use-event-feed/use-event-feed.test.ts`

**Interfaces:**
- Consumes: `GET /api/events` (Task 4) — возвращает `feedEvent[]` (JSON: `type`, `stage_id`, `data`, `timestamp` (ISO string), `seq?`).
- Produces: `AfmEvent` получает новое опциональное поле `seq?: number`; `useEventFeed(url: string): { events: AfmEvent[]; connected: boolean }` — сигнатура не меняется, но теперь на маунте дополнительно фетчит `/api/events`.

- [ ] **Step 1: Написать падающий тест**

Прочитать существующий `pkg/web/dashboard/src/hooks/use-event-feed/use-event-feed.test.ts` целиком перед правкой (там уже есть моки WebSocket — переиспользовать тот же паттерн). Добавить в конец файла (внутри существующего `describe('useEventFeed', ...)`):

```ts
  it('seeds history from /api/events on mount and merges without duplicating live events by seq', async () => {
    const historyPayload = [
      { type: 'stage_status_changed', stage_id: 's1', data: 'running', timestamp: '2026-07-27T10:00:00.000Z', seq: 1 },
      { type: 'stage_status_changed', stage_id: 's1', data: 'done', timestamp: '2026-07-27T10:05:00.000Z', seq: 2 },
    ]
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({
        ok: true,
        json: () => Promise.resolve(historyPayload),
      }),
    )

    const { result } = renderHook(() => useEventFeed('ws://test'))

    // Живое сообщение с тем же seq=2 приходит по WS ДО того, как REST-фетч
    // резолвится (гонка) — после merge не должно быть дубля.
    const socket = getLastMockSocket()
    socket.onopen?.(new Event('open'))
    socket.onmessage?.({ data: JSON.stringify({ type: 'stage_status_changed', stage_id: 's1', data: 'done', seq: 2 }) } as MessageEvent)

    await waitFor(() => {
      expect(result.current.events.filter((e) => e.seq === 2)).toHaveLength(1)
    })
    // История (seq=1) должна присутствовать — не потеряна.
    expect(result.current.events.some((e) => e.seq === 1)).toBe(true)
    // Timestamp из истории — реальный, не "сейчас".
    const historic = result.current.events.find((e) => e.seq === 1)
    expect(historic?.timestamp).toBe('2026-07-27T10:00:00.000Z')

    vi.unstubAllGlobals()
  })
```

(Если существующий файл использует другой мок-хелпер для WebSocket вместо `getLastMockSocket`/прямого доступа к `socket.onopen` — адаптировать под реально существующий паттерн из верхней части файла; сохранить точную СЕМАНТИКУ теста: REST-история + гонка с live-сообщением того же seq → итог без дубля, история не потеряна, timestamp у исторической записи не перезаписан текущим временем.)

- [ ] **Step 2: Запустить тест и убедиться, что он падает**

Run: `cd pkg/web/dashboard && npm test -- use-event-feed`
Expected: FAIL — `fetch` не вызывается вообще (хук ещё не фетчит `/api/events`), `result.current.events` содержит только live-сообщение без seq=1 из истории.

- [ ] **Step 3: Реализовать фетч истории + merge-by-seq + сохранение серверного timestamp**

В `pkg/web/dashboard/src/types/afm-event.ts` — обновить тип и комментарий:
```ts
// Событие, приходящее через WebSocket /ws или из истории /api/events.
//   payload   — произвольные данные события (соответствует полю data сервера; тип зависит от type);
//   stageId   — стадия, к которой относится событие (поле stage_id сервера);
//   timestamp — реальное время события в ISO 8601, если его прислал сервер
//               (только реплей истории из /api/events — Task 4); иначе время
//               приёма на клиенте (live WS-сообщения timestamp не несут).
//   seq       — стабильный ключ дедупликации для событий, производных от
//               реальной FSM-transition (только история из /api/events).
export type AfmEvent = {
  type: string
  payload: unknown
  stageId: string
  timestamp: string
  seq?: number
}
```

В `pkg/web/dashboard/src/hooks/use-event-feed/use-event-feed.ts` — изменить `toEvent`, чтобы принимать необязательный `timestamp`/`seq` из payload, и добавить фетч истории с merge:

Заменить:
```ts
function toEvent(raw: unknown): AfmEvent {
  const obj = isRecord(raw) ? raw : {}

  return {
    type: typeof obj.type === 'string' ? obj.type : '',
    payload: obj.data,
    stageId: typeof obj.stage_id === 'string' ? obj.stage_id : '',
    timestamp: new Date().toISOString(),
  }
}
```
на:
```ts
function toEvent(raw: unknown): AfmEvent {
  const obj = isRecord(raw) ? raw : {}

  // timestamp — из payload, если сервер его прислал (реплей истории из
  // /api/events, Task 4); live WS-сообщения его не несут — падаем на время
  // приёма, как и раньше.
  const timestamp = typeof obj.timestamp === 'string' ? obj.timestamp : new Date().toISOString()
  const seq = typeof obj.seq === 'number' ? obj.seq : undefined

  return {
    type: typeof obj.type === 'string' ? obj.type : '',
    payload: obj.data,
    stageId: typeof obj.stage_id === 'string' ? obj.stage_id : '',
    timestamp,
    seq,
  }
}
```

Добавить функцию слияния (в том же файле, рядом с `isSameStatusEvent`):
```ts
// mergeHistory сливает историю из /api/events (history) с уже накопленными
// live-событиями (live), дедуплицируя по seq: если live-событие с тем же seq
// уже пришло по WS (могло случиться в гонке между открытием сокета и
// резолвом REST-фетча), историческая запись с тем же seq отбрасывается.
// События без seq (agent_action и т.п.) не дедуплицируются этим механизмом —
// см. дизайн-документ, принятый компромисс для узкого окна гонки.
function mergeHistory(history: AfmEvent[], live: AfmEvent[]): AfmEvent[] {
  const seenSeq = new Set(live.map((e) => e.seq).filter((s): s is number => s !== undefined))
  const deduped = history.filter((e) => e.seq === undefined || !seenSeq.has(e.seq))
  return [...deduped, ...live].slice(-MAX_EVENTS)
}
```

Изменить `useEventFeed`, чтобы фетчить историю на маунте и мержить её в `events`. Заменить:
```ts
export function useEventFeed(url: string): { events: AfmEvent[]; connected: boolean } {
  const [events, setEvents] = useState<AfmEvent[]>([])
  const [connected, setConnected] = useState(false)

  useEffect(() => {
    let socket: WebSocket | null = null
```
на:
```ts
export function useEventFeed(url: string): { events: AfmEvent[]; connected: boolean } {
  const [events, setEvents] = useState<AfmEvent[]>([])
  const [connected, setConnected] = useState(false)

  useEffect(() => {
    let cancelledFetch = false
    // Открываем WebSocket СРАЗУ (см. ниже, connect() вызывается как и раньше)
    // — live-события накапливаются в events через обычный путь setEvents.
    // Историю фетчим ПАРАЛЛЕЛЬНО и, когда она придёт, мержим в уже
    // накопленные live-события по seq (mergeHistory) — так гарантированно
    // нет ни дыры (WS слушает с самого начала), ни устойчивого дубля
    // (дедуп по seq для FSM-событий).
    fetch('/api/events')
      .then((r) => (r.ok ? r.json() : []))
      .then((raw: unknown) => {
        if (cancelledFetch || !Array.isArray(raw)) return
        const history = raw.map(toEvent)
        setEvents((prev) => mergeHistory(history, prev))
      })
      .catch(() => {
        // /api/events недоступен (старая сборка сервера, сетевая ошибка) —
        // деградируем к чистому live-потоку, как было до этой правки.
      })

    let socket: WebSocket | null = null
```

И в `return`-функции эффекта (cleanup) добавить `cancelledFetch = true` первой строкой:
```ts
    return () => {
      cancelledFetch = true
      cancelled = true
      if (reconnectTimer !== undefined) clearTimeout(reconnectTimer)
      if (watchdogTimer !== undefined) clearInterval(watchdogTimer)
      socket?.close()
    }
```

- [ ] **Step 4: Запустить тест и убедиться, что он проходит**

Run: `cd pkg/web/dashboard && npm test -- use-event-feed`
Expected: PASS

- [ ] **Step 5: Прогнать весь фронтенд-набор тестов**

Run: `cd pkg/web/dashboard && npm test`
Expected: PASS (включая `EventFeedPanel.test.tsx` — компонент не менялся, но убедиться, что рендер новых полей `seq` ничего не ломает)

- [ ] **Step 6: Собрать дашборд и прогнать общий `make build`/`make lint`**

Run: `make lint && make build`
Expected: `0 issues`, сборка проходит без предупреждений о новых типах

- [ ] **Step 7: Коммит**

```bash
git add pkg/web/dashboard/src/hooks/use-event-feed/use-event-feed.ts pkg/web/dashboard/src/hooks/use-event-feed/use-event-feed.test.ts pkg/web/dashboard/src/types/afm-event.ts
git commit -m "$(cat <<'EOF'
feat(dashboard): useEventFeed фетчит /api/events и переживает рефреш

Лента событий стартовала с пустого массива при каждом монтировании —
WebSocket-подписка не имеет истории. Теперь на маунте параллельно с
открытием WS фетчится /api/events (Task 4) и мержится в уже
накопленные live-события по seq — без дыры (WS слушает с начала) и без
устойчивого дубля (дедуп по seq). Заодно исправлен toEvent: раньше он
всегда проставлял timestamp = время приёма на клиенте, игнорируя
серверное время — для реплея истории это ломало relative-time
("сейчас" для событий недельной давности).
EOF
)"
```

---

## Self-Review (проведён при написании плана)

**Spec coverage:** все три пункта спека покрыты — Task 1+2 (спек §2 и §1, порядок инвертирован намеренно, см. Architecture), Task 3+4+5 (спек §3, backend reconstruction + notices.jsonl sidecar + frontend merge/timestamp fix).

**Type consistency:** `relocateMisplacedQuestions(stageID, stageDir string, allowRootScan bool)` — сигнатура одинакова в Task 2 (реализация) и единственном call site (`pollQuestions`, тот же Task). `feedEvent` (Task 4) и `AfmEvent`/`toEvent` (Task 5) согласованы по полям (`type`/`stage_id`/`data`/`timestamp`/`seq`). `appendNotice`/`noticeEntry` (Task 3) — сигнатура и JSON-теги совпадают между реализацией и потребителем в Task 4 (`reconstructNotices`).

**Placeholder scan:** чисто — весь код в шагах конкретен и компилируем как есть, без TODO/TBD и условных развилок для исполнителя.
