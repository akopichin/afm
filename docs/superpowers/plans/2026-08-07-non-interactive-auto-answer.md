# Авто-ответ на вопросы в non-interactive стадиях — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** non-interactive стадия (`stage.Interactive != true`, включая `agents: [auto]`), чей агент/скилл написал вопрос через файловый диалоговый протокол, не зависает и не падает — afm сам синтезирует ответ и записывает его так, что агент продолжает работу как будто ответил человек.

**Architecture:** `runner_factory.go` начинает пробрасывать `AFM_STAGE_DIR` для ВСЕХ стадий (не только interactive/autonomous); новая функция `mcp.PickAutoAnswer` выбирает рекомендованный/первый вариант; новая функция `mcp.WriteAnswer` — единая атомарная точка записи `answer.json` + истории `dialog.jsonl` (переиспользуется и HTTP-хендлером, и новым авто-ответчиком); `dialog_poller.go`'s `pollQuestions` для non-interactive стадий пишет ответ сам вместо перевода стадии в `awaiting_user_input`, публикуя новое событие `bus.EventAutoAnswered` без FSM-перехода.

**Tech Stack:** Go (backend, `pkg/mcp`, `pkg/orchestrator`, `pkg/server`), TypeScript/React + Vitest (dashboard, `pkg/web/dashboard`).

## Global Constraints

- Область действия — любая стадия с `stage.Interactive != true` (обычные planning/implementation/review с `interactive: false`/дефолтом, и `agents: [auto]`). Единственное исключение — явный `interactive: true`.
- НЕ трогаем: `pkg/prompts/builder.go`, `pkg/orchestrator/retry.go`'s условие `(s.Interactive || phase == phaseAutonomous) && hasOpenQuestion` (строка ~101), сессии/`--resume`-инфраструктуру.
- Recovery/restart уже работает без доработок (проверено по коду в брейнсторминге) — новых тестов на этот сценарий план не включает.
- Fallback-текст для открытых вопросов без options — **дословно**: `Прими самое релевантное решение автономно или предложи варианты ответов`.
- Маркеры «рекомендованного» варианта (case-insensitive substring), в порядке появления в списке options (не в порядке маркеров): `(recommended)`, `(default)`, `(рекомендую)`, `(рекомендуется)`, `(по умолчанию)`.
- Новое имя события: `bus.EventAutoAnswered = "auto_answered"`.
- Отклонение от исходной design-спеки (сделано в этом плане сознательно, по принципу simplicity из CLAUDE.md — значение проще чем строковый enum из двух состояний): вместо строкового поля `role` используется булево поле `AutoAnswered bool` (json: `auto_answered`) на `mcp.Answer`/`mcp.Entry` — в коде сегодня нет никакого поля `role` вообще (только `Type: "agent_text"` для текстовых сообщений агента), а различать нужно ровно два случая: ответил человек или afm.
- Go — не менять версию в `go.mod`. После каждой правки — `golangci-lint run ./...` (или `go vet ./...`, если golangci-lint недоступен) и `go build ./...` должны быть чистыми.
- Frontend — после правок: `npx vitest run` и `npx tsc --noEmit` в `pkg/web/dashboard` должны быть чистыми.

---

### Task 1: `pkg/mcp/dialog.go` — `PickAutoAnswer`

**Files:**
- Modify: `pkg/mcp/dialog.go`
- Test: `pkg/mcp/dialog_test.go`

**Interfaces:**
- Produces: `func PickAutoAnswer(q QuestionFile) (answer string, fromOptions bool)` — чистая функция, использует существующий тип `mcp.QuestionFile` (поля `Options []string`, `AllowCustom bool` — `AllowCustom` не используется этой функцией, решение о fallback-тексте зависит только от `len(q.Options) == 0`).

- [ ] **Step 1: Write the failing tests**

Добавить в конец `pkg/mcp/dialog_test.go`:

```go
func TestPickAutoAnswer_NoOptions_ReturnsFallbackText(t *testing.T) {
	answer, fromOptions := mcp.PickAutoAnswer(mcp.QuestionFile{Question: "что делать?"})
	want := "Прими самое релевантное решение автономно или предложи варианты ответов"
	if answer != want || fromOptions {
		t.Errorf("got (%q, %v), want (%q, false)", answer, fromOptions, want)
	}
}

func TestPickAutoAnswer_MarkerVariants(t *testing.T) {
	cases := []struct {
		name    string
		options []string
		want    string
	}{
		{"recommended", []string{"Вариант A", "Вариант B (recommended)"}, "Вариант B"},
		{"default", []string{"Вариант A", "Вариант B (default)"}, "Вариант B"},
		{"рекомендую", []string{"Вариант A", "Вариант B (рекомендую)"}, "Вариант B"},
		{"рекомендуется", []string{"Вариант A (рекомендуется)", "Вариант B"}, "Вариант A"},
		{"по умолчанию", []string{"Вариант A", "Вариант B (по умолчанию)"}, "Вариант B"},
		{"marker not in first option", []string{"Вариант A", "Вариант B", "Вариант C (recommended)"}, "Вариант C"},
		{"case-insensitive marker", []string{"Вариант A", "Вариант B (RECOMMENDED)"}, "Вариант B"},
		{"trailing dash before marker trimmed", []string{"Вариант A - (recommended)"}, "Вариант A"},
		{"no marker anywhere: first option wins", []string{"Вариант A", "Вариант B"}, "Вариант A"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			answer, fromOptions := mcp.PickAutoAnswer(mcp.QuestionFile{Options: tc.options})
			if answer != tc.want || !fromOptions {
				t.Errorf("got (%q, %v), want (%q, true)", answer, fromOptions, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/mcp/... -run TestPickAutoAnswer -v`
Expected: FAIL — `undefined: mcp.PickAutoAnswer`

- [ ] **Step 3: Write minimal implementation**

Добавить в конец `pkg/mcp/dialog.go`:

```go
// autoAnswerFallbackText is the answer afm synthesizes for an open question
// (no options, only allow_custom) in a non-interactive stage.
const autoAnswerFallbackText = "Прими самое релевантное решение автономно или предложи варианты ответов"

// autoAnswerMarkers are the case-insensitive substrings that mark an option
// as the recommended default, checked in option order (not marker priority
// order) — the first option carrying ANY of these markers wins.
var autoAnswerMarkers = []string{"(recommended)", "(default)", "(рекомендую)", "(рекомендуется)", "(по умолчанию)"}

// PickAutoAnswer chooses the answer afm synthesizes for a question asked by a
// non-interactive stage's agent: the option explicitly marked recommended
// (see autoAnswerMarkers), the first option if none are marked, or a fixed
// fallback text when the question has no options at all.
func PickAutoAnswer(q QuestionFile) (answer string, fromOptions bool) {
	if len(q.Options) == 0 {
		return autoAnswerFallbackText, false
	}
	for _, opt := range q.Options {
		if cleaned, ok := stripRecommendedMarker(opt); ok {
			return cleaned, true
		}
	}
	return q.Options[0], true
}

// stripRecommendedMarker reports whether opt carries any autoAnswerMarkers
// substring and, if so, returns opt with the marker (and a trailing
// space/dash left over from " - (recommended)"-style authoring) removed.
func stripRecommendedMarker(opt string) (string, bool) {
	lower := strings.ToLower(opt)
	for _, m := range autoAnswerMarkers {
		idx := strings.Index(lower, m)
		if idx < 0 {
			continue
		}
		cleaned := opt[:idx] + opt[idx+len(m):]
		cleaned = strings.TrimSpace(cleaned)
		cleaned = strings.TrimRight(cleaned, "-–— \t")
		return strings.TrimSpace(cleaned), true
	}
	return "", false
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/mcp/... -run TestPickAutoAnswer -v`
Expected: PASS (all subtests)

- [ ] **Step 5: Commit**

```bash
git add pkg/mcp/dialog.go pkg/mcp/dialog_test.go
git commit -m "feat(mcp): выбор авто-ответа на вопрос по маркеру recommended/default"
```

---

### Task 2: `pkg/mcp/dialog.go` — `AutoAnswered` field + `WriteAnswer`

**Files:**
- Modify: `pkg/mcp/dialog.go`
- Test: `pkg/mcp/dialog_test.go`

**Interfaces:**
- Consumes: `Answer`, `Entry`, `AppendAnswer` (existing, this file).
- Produces:
  - `Answer.AutoAnswered bool` (json: `auto_answered,omitempty`)
  - `Entry.AutoAnswered bool`
  - `func WriteAnswer(stageDir, phase, id, answer string, fromOptions, autoAnswered bool) error` — atomically creates `<stageDir>/<phase>.<id>.answer.json` (O_EXCL — returns an error satisfying `os.IsExist` if already answered), then best-effort appends to `<stageDir>/<phase>.dialog.jsonl` (append failure is logged, not returned — see Step 3 comment).

- [ ] **Step 1: Write the failing tests**

Добавить в конец `pkg/mcp/dialog_test.go`:

```go
func TestWriteAnswer_CreatesAnswerFileAndDialogEntry(t *testing.T) {
	dir := t.TempDir()

	if err := mcp.WriteAnswer(dir, "implementation", "q1", "Вариант B", true, true); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "implementation.q1.answer.json"))
	if err != nil {
		t.Fatalf("answer.json not written: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got["id"] != "q1" || got["answer"] != "Вариант B" || got["from_options"] != true {
		t.Errorf("answer.json content mismatch: %v", got)
	}

	entries, err := mcp.ReadDialog(filepath.Join(dir, "implementation.dialog.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Answer == nil || *entries[0].Answer != "Вариант B" || !entries[0].AutoAnswered {
		t.Fatalf("dialog entry mismatch: %+v", entries)
	}
}

func TestWriteAnswer_DuplicateReturnsExistsError(t *testing.T) {
	dir := t.TempDir()

	if err := mcp.WriteAnswer(dir, "planning", "q1", "yes", false, false); err != nil {
		t.Fatal(err)
	}
	err := mcp.WriteAnswer(dir, "planning", "q1", "no", false, false)
	if err == nil || !os.IsExist(err) {
		t.Fatalf("want an os.IsExist error on duplicate write, got %v", err)
	}
}

func TestWriteAnswer_DialogAppendFailureStillWritesAnswerFile(t *testing.T) {
	dir := t.TempDir()
	// Make the dialog.jsonl path a directory so AppendAnswer fails internally.
	if err := os.MkdirAll(filepath.Join(dir, "planning.dialog.jsonl"), 0755); err != nil {
		t.Fatal(err)
	}

	if err := mcp.WriteAnswer(dir, "planning", "q1", "yes", false, false); err != nil {
		t.Fatalf("WriteAnswer must not fail when only the best-effort dialog append fails: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "planning.q1.answer.json")); err != nil {
		t.Errorf("answer.json not written despite dialog append failure: %v", err)
	}
}
```

Убедиться, что `pkg/mcp/dialog_test.go` импортирует `"encoding/json"` и `"os"` и `"path/filepath"` (уже импортированы существующими тестами в этом файле).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/mcp/... -run TestWriteAnswer -v`
Expected: FAIL — `undefined: mcp.WriteAnswer`

- [ ] **Step 3: Write minimal implementation**

В `pkg/mcp/dialog.go` добавить поле `AutoAnswered bool` в структуры `Answer` (после `FromOptions`) и `Entry` (после `FromOptions`):

```go
type Answer struct {
	ID          string `json:"id"`
	TS          string `json:"ts"`
	Answer      string `json:"answer"`
	FromOptions bool   `json:"from_options"`
	AutoAnswered bool  `json:"auto_answered,omitempty"`
}
```

```go
type Entry struct {
	ID          string
	TS          string
	Question    string
	Options     []string
	AllowCustom bool
	Answer      *string
	AnswerTS    string
	FromOptions bool
	AutoAnswered bool
}
```

В `ReadDialog`, в ветке разбора Answer-строки (там, где сейчас `e.Answer = &ans; e.AnswerTS = a.TS; e.FromOptions = a.FromOptions`), добавить перенос нового поля:

```go
			ans := a.Answer
			e.Answer = &ans
			e.AnswerTS = a.TS
			e.FromOptions = a.FromOptions
			e.AutoAnswered = a.AutoAnswered
```

Добавить в конец файла:

```go
// WriteAnswer atomically creates <stageDir>/<phase>.<id>.answer.json (O_EXCL
// — a question may only be answered once; returns an error satisfying
// os.IsExist if it already was) and best-effort appends the answer to
// <phase>.dialog.jsonl for UI history. autoAnswered marks answers afm
// synthesized itself (non-interactive auto-answer), as opposed to a real
// user reply — see Answer.AutoAnswered / Entry.AutoAnswered.
//
// A dialog.jsonl append failure is logged and swallowed: answer.json is
// already safely on disk (the critical path for the agent's polling loop),
// so failing the caller here would incorrectly signal the answer was lost.
func WriteAnswer(stageDir, phase, id, answer string, fromOptions, autoAnswered bool) error {
	answerPath := filepath.Join(stageDir, phase+"."+id+".answer.json")
	payload, err := json.Marshal(map[string]any{
		"id": id, "answer": answer, "from_options": fromOptions,
	})
	if err != nil {
		return fmt.Errorf("marshal answer: %w", err)
	}

	// O_EXCL atomically creates-and-checks-existence in one step, preventing a
	// TOCTOU race where two concurrent writers both see the file missing and
	// both try to create it.
	f, err := os.OpenFile(answerPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	if _, err := f.Write(payload); err != nil {
		f.Close()
		_ = os.Remove(answerPath)
		return fmt.Errorf("write answer: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		_ = os.Remove(answerPath)
		return fmt.Errorf("sync answer: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(answerPath)
		return fmt.Errorf("close answer: %w", err)
	}

	dialogPath := filepath.Join(stageDir, phase+".dialog.jsonl")
	if err := AppendAnswer(dialogPath, Answer{
		ID: id, Answer: answer, FromOptions: fromOptions, AutoAnswered: autoAnswered,
	}); err != nil {
		log.Printf("WARN: persist dialog answer for %s/%s.%s: %v (answer.json already written)", stageDir, phase, id, err) //nolint:gosec // G706: phase/id are validated safe filename components by callers
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/mcp/... -v`
Expected: PASS (all tests in the package, including pre-existing ones — confirms the new struct fields don't break `TestAppendAndRead` etc.)

- [ ] **Step 5: Commit**

```bash
git add pkg/mcp/dialog.go pkg/mcp/dialog_test.go
git commit -m "feat(mcp): WriteAnswer — общая атомарная запись answer.json + dialog.jsonl с меткой auto_answered"
```

---

### Task 3: `pkg/server/handlers.go` — переиспользовать `WriteAnswer`, отдавать `auto_answered` в GET /dialog

**Files:**
- Modify: `pkg/server/handlers.go`
- Test: `pkg/server/handlers_test.go`

**Interfaces:**
- Consumes: `mcp.WriteAnswer` (Task 2).
- Produces: `dialogUIEntry.AutoAnswered bool` (json: `auto_answered,omitempty`).

Это в основном рефакторинг: существующие тесты (`TestDialogAnswer`, `TestHandleDialogAnswer_WritesAnswerFile`, `TestHandleDialogAnswer_AppendAnswerFailureStillNotifies`, `TestHandleDialogAnswer_DuplicateAnswer`, `TestHandleDialogAnswer_QuestionNotFound`, `TestHandleDialogAnswer_InvalidID`) — регрессионная сеть на поведение `handleDialogAnswer`, которое не должно измениться. Новый тест нужен только для нового поля в GET-ответе.

- [ ] **Step 1: Run existing dialog-answer tests to confirm they currently pass (baseline)**

Run: `go test ./pkg/server/... -run 'TestDialogAnswer|TestHandleDialogAnswer' -v`
Expected: PASS (все, до рефакторинга — это характеризационный baseline)

- [ ] **Step 2: Write the failing test for the new field**

Добавить в `pkg/server/handlers_test.go`:

```go
func TestHandleDialogGet_SurfacesAutoAnsweredFlag(t *testing.T) {
	srv, runDir := setupTestServer(t)
	stageDir := filepath.Join(runDir, testStageID)

	dialogPath := filepath.Join(stageDir, "implementation.dialog.jsonl")
	if err := mcp.AppendQuestion(dialogPath, mcp.Question{ID: "q1", Question: "which?", Options: []string{"A", "B"}}); err != nil {
		t.Fatal(err)
	}
	if err := mcp.AppendAnswer(dialogPath, mcp.Answer{ID: "q1", Answer: "B", FromOptions: true, AutoAnswered: true}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/api/stages/"+testStageID+"/dialog", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}

	var got []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0]["auto_answered"] != true {
		t.Fatalf("want auto_answered:true surfaced in GET /dialog, got %v", got)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./pkg/server/... -run TestHandleDialogGet_SurfacesAutoAnsweredFlag -v`
Expected: FAIL — `auto_answered` missing from the JSON response (field doesn't exist on `dialogUIEntry` yet)

- [ ] **Step 4: Implement — add the field, wire it, refactor the write path**

В `pkg/server/handlers.go`, добавить поле в `dialogUIEntry` (после `FromOptions`):

```go
	FromOptions bool     `json:"from_options,omitempty"`
	AutoAnswered bool    `json:"auto_answered,omitempty"`
}
```

В `questionUIEntry`, прокинуть поле:

```go
func questionUIEntry(phase string, e mcp.Entry) dialogUIEntry {
	return dialogUIEntry{
		ID: e.ID, Phase: phase, TS: e.TS, Question: e.Question,
		Options: e.Options, AllowCustom: e.AllowCustom,
		Answer: e.Answer, AnswerTS: e.AnswerTS, FromOptions: e.FromOptions,
		AutoAnswered: e.AutoAnswered,
	}
}
```

Заменить блок атомарной записи в `handleDialogAnswer` (от комментария `// Atomically write answer.json FIRST...` до конца блока `mcp.AppendAnswer(...)` с его `log.Printf` — переменная `answerPath` уже объявлена выше в функции и используется в recheck-блоке, оставить как есть) на:

```go
	// Atomically write answer.json FIRST (mcp.WriteAnswer) so the agent's bash
	// loop can pick it up, then persist to dialog.jsonl for UI history
	// (best-effort inside WriteAnswer). This is the critical path: dialog
	// history must never be persisted before the agent's answer exists on disk.
	if err := mcp.WriteAnswer(stageDir, req.Phase, req.ID, req.Answer, req.FromOptions, false); err != nil {
		if os.IsExist(err) {
			http.Error(w, "question already answered", http.StatusConflict)
			return
		}
		http.Error(w, "write answer: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Re-check that the question file still exists to ensure we maintain the
	// invariant that answer.json only exists when its question.json exists.
	// Another goroutine could have deleted the question between our initial
	// check and now.
	if _, err := os.Stat(questionPath); err != nil {
		_ = os.Remove(answerPath)
		http.Error(w, "question disappeared during write", http.StatusBadRequest)
		return
	}
```

Удалить теперь неиспользуемый импорт `"log"` из `pkg/server/handlers.go` (единственный вызов `log.Printf` в этом файле был в удалённом блоке).

- [ ] **Step 5: Run tests to verify everything passes**

Run: `go test ./pkg/server/... -run 'TestDialogAnswer|TestHandleDialogAnswer|TestHandleDialogGet' -v`
Expected: PASS — включая все baseline-тесты из Step 1 (поведение не изменилось) и новый тест из Step 2

Run: `go build ./... && go vet ./...`
Expected: чисто (в частности, отсутствие неиспользуемого импорта `log`)

- [ ] **Step 6: Commit**

```bash
git add pkg/server/handlers.go pkg/server/handlers_test.go
git commit -m "refactor(server): handleDialogAnswer переиспользует mcp.WriteAnswer, GET /dialog отдаёт auto_answered"
```

---

### Task 4: `pkg/orchestrator/runner_factory.go` — `AFM_STAGE_DIR` для всех стадий

**Files:**
- Modify: `pkg/orchestrator/runner_factory.go`
- Test: `pkg/orchestrator/runner_factory_test.go`

**Interfaces:**
- Не меняет публичные сигнатуры — только поведение `runnerFor` для `!s.Interactive`: `executor.Config.StageDir` теперь ВСЕГДА `filepath.Join(o.opts.RunDir, s.ID)`, а не только когда `phase == phaseAutonomous`.

- [ ] **Step 1: Write the failing test**

Добавить в `pkg/orchestrator/runner_factory_test.go` (добавить импорты `"fmt"` и `"os"` к существующим):

```go
// TestRunnerForSetsStageDirForNonInteractiveStage закрывает главный пробел
// этой фичи: без AFM_STAGE_DIR агенту non-interactive стадии физически
// некуда писать question.json (см. docs/superpowers/specs/2026-08-07-non-interactive-auto-answer-design.md).
func TestRunnerForSetsStageDirForNonInteractiveStage(t *testing.T) {
	runDir := t.TempDir()
	stage := flow.Stage{ID: "s1", Name: "Backend"} // Interactive: false (default)

	store, err := state.Open(runDir, []string{stage.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	marker := filepath.Join(runDir, "stagedir-seen.txt")
	script := fmt.Sprintf(`printf '%%s' "$AFM_STAGE_DIR" > %q
printf '{"type":"result","subtype":"success"}\n'`, marker)
	cfg := config.Default()
	cfg.Client.Command = "sh"
	cfg.Client.ExtraArgs = []string{"-c", script}
	cfg.Executor.IdleTimeout = 5 * time.Second

	o := New(Options{
		RunDir: runDir,
		Stages: []flow.Stage{stage},
		Store:  store,
		Config: cfg,
	})

	runner := o.runnerFor(stage, phaseImplementation)
	logFile := filepath.Join(runDir, "impl.log")
	if err := runner.RunAgent(context.Background(), phaseImplementation, stage.Name, "do work", logFile); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("AFM_STAGE_DIR not set for non-interactive stage: %v", err)
	}
	if want := filepath.Join(runDir, stage.ID); string(got) != want {
		t.Errorf("AFM_STAGE_DIR = %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/orchestrator/... -run TestRunnerForSetsStageDirForNonInteractiveStage -v`
Expected: FAIL — marker file empty/missing (`AFM_STAGE_DIR` unset)

- [ ] **Step 3: Write minimal implementation**

В `pkg/orchestrator/runner_factory.go`, в `runnerFor`, заменить:

```go
		cfg := executor.Config{
			Command:        cmd,
			ExtraArgs:      extraArgs,
			IdleTimeout:    o.opts.Config.Executor.IdleTimeout,
			TruncateOutput: o.opts.Config.Executor.TruncateOutput,
			OnAction:       uiActionPublisher(o.ui, s.ID),
			WrapperDir:     wrapperDirFor(cmd, o.opts.WrapperDir, o.opts.GeneratedAgents),
			Dir:            o.opts.RootDir,
			Debug:          o.opts.Debug,
			RunDir:         o.opts.RunDir,
			StageID:        s.ID,
		}
		// Autonomous-фаза диалоговая: агенту нужен AFM_STAGE_DIR, чтобы писать
		// question.json и писать execution_summary.md в каталог стадии.
		if phase == phaseAutonomous {
			cfg.StageDir = filepath.Join(o.opts.RunDir, s.ID)
		}
```

на:

```go
		cfg := executor.Config{
			Command:        cmd,
			ExtraArgs:      extraArgs,
			IdleTimeout:    o.opts.Config.Executor.IdleTimeout,
			TruncateOutput: o.opts.Config.Executor.TruncateOutput,
			OnAction:       uiActionPublisher(o.ui, s.ID),
			WrapperDir:     wrapperDirFor(cmd, o.opts.WrapperDir, o.opts.GeneratedAgents),
			Dir:            o.opts.RootDir,
			Debug:          o.opts.Debug,
			RunDir:         o.opts.RunDir,
			StageID:        s.ID,
			// Every non-interactive stage gets AFM_STAGE_DIR too, so an agent or
			// skill that uses the file-based dialog protocol always has somewhere
			// to write question.json — the poller auto-answers it (dialog_poller.go).
			StageDir: filepath.Join(o.opts.RunDir, s.ID),
		}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/orchestrator/... -run TestRunnerFor -v`
Expected: PASS (включая уже существующий `TestRunnerForAttributesStageIDForDefaultCommand`)

Run: `go test ./pkg/orchestrator/... 2>&1 | tail -30`
Expected: весь пакет по-прежнему зелёный (проверка, что always-on `StageDir` не сломала interactive-специфичные тесты)

- [ ] **Step 5: Commit**

```bash
git add pkg/orchestrator/runner_factory.go pkg/orchestrator/runner_factory_test.go
git commit -m "feat(orchestrator): AFM_STAGE_DIR пробрасывается для всех стадий, не только interactive/autonomous"
```

---

### Task 5: `pkg/orchestrator/bus/bus.go` — `EventAutoAnswered`

**Files:**
- Modify: `pkg/orchestrator/bus/bus.go`

**Interfaces:**
- Produces: `bus.EventAutoAnswered EventType = "auto_answered"`

Тривиальная константа без собственного поведения — тестируется опосредованно в Task 6 (проверка, что публикуется именно это событие). Отдельного failing-теста для одной строки константы не пишем (TDD применяется к поведению, не к декларациям).

- [ ] **Step 1: Add the constant**

В `pkg/orchestrator/bus/bus.go`, в блоке `const ( ... )`, добавить после `EventUserAnswered`:

```go
	EventUserAnswered       EventType = "user_answered"
	// EventAutoAnswered fires when a non-interactive stage's open question was
	// answered by afm itself (see pkg/mcp.PickAutoAnswer), not by a real user.
	// Never triggers an FSM transition — the stage's status is unaffected.
	EventAutoAnswered EventType = "auto_answered"
```

- [ ] **Step 2: Build**

Run: `go build ./pkg/orchestrator/...`
Expected: чисто

- [ ] **Step 3: Commit**

```bash
git add pkg/orchestrator/bus/bus.go
git commit -m "feat(bus): событие auto_answered для авто-ответов afm на вопросы non-interactive стадий"
```

---

### Task 6: `pkg/orchestrator/dialog_poller.go` — ветка авто-ответа (unit-тест на `pollQuestions`)

**Files:**
- Modify: `pkg/orchestrator/dialog_poller.go`
- Test: `pkg/orchestrator/dialog_poller_test.go` (новый файл, `package orchestrator` — white-box, как `runner_factory_test.go`)

**Interfaces:**
- Consumes: `mcp.PickAutoAnswer`, `mcp.WriteAnswer` (Task 1, 2), `bus.EventAutoAnswered` (Task 5), `o.graph.Stage(stageID) *flow.Stage` (existing).
- Produces: изменённое поведение `pollQuestions` — не меняет сигнатуру.

- [ ] **Step 1: Write the failing tests**

Создать `pkg/orchestrator/dialog_poller_test.go`:

```go
package orchestrator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/mcp"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

func writeQuestionFile(t *testing.T, stageDir, phase, id string, options []string) {
	t.Helper()
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{
		"id": id, "question": "which?", "options": options, "allow_custom": true,
	})
	path := filepath.Join(stageDir, phase+"."+id+".question.json")
	if err := os.WriteFile(path, payload, 0644); err != nil {
		t.Fatal(err)
	}
}

// TestPollQuestions_NonInteractiveStageAutoAnswers покрывает ядро фичи:
// non-interactive стадия с открытым вопросом получает ответ от afm, не
// переходя в awaiting_user_input.
func TestPollQuestions_NonInteractiveStageAutoAnswers(t *testing.T) {
	runDir := t.TempDir()
	stage := flow.Stage{ID: "s1", Name: "Backend", Agents: []flow.AgentType{flow.AgentImplementation}}

	store, err := state.Open(runDir, []string{stage.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Apply(&state.Transition{StageID: stage.ID, From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}

	stageDir := filepath.Join(runDir, stage.ID)
	writeQuestionFile(t, stageDir, "implementation", "q1", []string{"Вариант A", "Вариант B (recommended)"})

	o := New(Options{RunDir: runDir, Stages: []flow.Stage{stage}, Store: store, Config: config.Default()})

	subID, events := o.ui.Subscribe(8)
	defer o.ui.Unsubscribe(subID)

	o.pollQuestions(map[string]bool{})

	data, err := os.ReadFile(filepath.Join(stageDir, "implementation.q1.answer.json"))
	if err != nil {
		t.Fatalf("answer.json not written: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got["answer"] != "Вариант B" {
		t.Errorf("auto-answer = %v, want %q", got["answer"], "Вариант B")
	}

	select {
	case ev := <-events:
		if ev.Type != bus.EventAutoAnswered {
			t.Errorf("got event %s, want %s", ev.Type, bus.EventAutoAnswered)
		}
	default:
		t.Fatal("expected EventAutoAnswered to be published")
	}

	if got := store.Snapshot().Stages[stage.ID].Status; got != state.StatusRunning {
		t.Errorf("stage status = %s, want unchanged %s (no FSM transition on auto-answer)", got, state.StatusRunning)
	}

	entries, err := mcp.ReadDialog(filepath.Join(stageDir, "implementation.dialog.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Answer == nil || !entries[0].AutoAnswered {
		t.Fatalf("dialog entry missing auto_answered marker: %+v", entries)
	}
}

// TestPollQuestions_AutoStageAutoAnswers покрывает "agents: [auto]" стадию —
// та же non-interactive обработка (Interactive: false у auto-стадий и так
// по умолчанию, специальной проверки на IsAuto() коду не требуется).
func TestPollQuestions_AutoStageAutoAnswers(t *testing.T) {
	runDir := t.TempDir()
	stage := flow.Stage{ID: "s1", Name: "Autonomous", Agents: []flow.AgentType{flow.AgentAuto}}

	store, err := state.Open(runDir, []string{stage.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Apply(&state.Transition{StageID: stage.ID, From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}

	stageDir := filepath.Join(runDir, stage.ID)
	writeQuestionFile(t, stageDir, "autonomous_execution", "q1", []string{"Вариант A"})

	o := New(Options{RunDir: runDir, Stages: []flow.Stage{stage}, Store: store, Config: config.Default()})
	o.pollQuestions(map[string]bool{})

	if _, err := os.Stat(filepath.Join(stageDir, "autonomous_execution.q1.answer.json")); err != nil {
		t.Errorf("auto-стадия должна получить авто-ответ так же, как обычная non-interactive: %v", err)
	}
	if got := store.Snapshot().Stages[stage.ID].Status; got != state.StatusRunning {
		t.Errorf("stage status = %s, want unchanged %s", got, state.StatusRunning)
	}
}

// TestPollQuestions_InteractiveStageStillAsksUser — регрессионная гарантия:
// interactive-стадия НЕ получает авто-ответ, поведение (EvAskUser →
// awaiting_user_input) не меняется этой фичей.
func TestPollQuestions_InteractiveStageStillAsksUser(t *testing.T) {
	runDir := t.TempDir()
	stage := flow.Stage{ID: "s1", Name: "Interactive", Agents: []flow.AgentType{flow.AgentImplementation}, Interactive: true}

	store, err := state.Open(runDir, []string{stage.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Apply(&state.Transition{StageID: stage.ID, From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}

	stageDir := filepath.Join(runDir, stage.ID)
	writeQuestionFile(t, stageDir, "implementation", "q1", []string{"Вариант A (recommended)"})

	o := New(Options{RunDir: runDir, Stages: []flow.Stage{stage}, Store: store, Config: config.Default()})
	o.pollQuestions(map[string]bool{})

	if _, err := os.Stat(filepath.Join(stageDir, "implementation.q1.answer.json")); err == nil {
		t.Error("interactive-стадия не должна получать авто-ответ")
	}
	if got := store.Snapshot().Stages[stage.ID].Status; got != state.StatusAwaitingUserInput {
		t.Errorf("stage status = %s, want %s", got, state.StatusAwaitingUserInput)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/orchestrator/... -run TestPollQuestions -v`
Expected: FAIL для `TestPollQuestions_NonInteractiveStageAutoAnswers` и `TestPollQuestions_AutoStageAutoAnswers` (answer.json не создаётся — стадия вместо этого уходит в `awaiting_user_input`, `TestPollQuestions_InteractiveStageStillAsksUser` уже PASS на текущем коде — это ожидаемо, регрессионный тест на неизменную ветку)

- [ ] **Step 3: Write minimal implementation**

В `pkg/orchestrator/dialog_poller.go`, в `pollQuestions`, заменить тело цикла `for _, q := range questions { ... }`:

```go
		for _, q := range questions {
			key := stageID + "|" + q.Phase + "|" + q.ID
			if processed[key] {
				continue
			}
			processed[key] = true
			// Write to dialog.jsonl for history (idempotent via FindEntry check).
			dialogPath := filepath.Join(stageDir, q.Phase+".dialog.jsonl")
			if e, _ := mcp.FindEntry(dialogPath, q.ID); e == nil {
				_ = mcp.AppendQuestion(dialogPath, mcp.Question{
					ID:          q.ID,
					Question:    q.Question,
					Options:     q.Options,
					AllowCustom: q.AllowCustom,
				})
			}

			// Non-interactive stage (default, or agents:[auto]): afm answers the
			// question itself instead of surfacing it to a human — the stage's
			// FSM status is left untouched (no EvAskUser transition).
			if stage := o.graph.Stage(stageID); stage != nil && !stage.Interactive {
				answer, fromOptions := mcp.PickAutoAnswer(q)
				if err := mcp.WriteAnswer(stageDir, q.Phase, q.ID, answer, fromOptions, true); err != nil {
					log.Printf("WARN: auto-answer %s/%s.%s: %v", stageID, q.Phase, q.ID, err)
					continue
				}
				o.ui.Publish(bus.Event{
					Type:    bus.EventAutoAnswered,
					StageID: stageID,
					Data: map[string]any{
						keyID: q.ID, keyPhase: q.Phase, "answer": answer, "from_options": fromOptions,
					},
				})
				continue
			}

			// Сохраняем реальную фазу ДО перехода в awaiting_user_input.
			// Фаза из имени файла (q.Phase) может быть неправильной (агент написал
			// "review" вместо "planning") — при EvUserAnswered используем сохранённую
			// фазу, а не ту что в файле вопроса.
			o.preAskPhase.Store(stageID, o.correctPhaseForState(o.currentStatus(stageID), q.Phase))
			// Триггерим ПЕРЕД публикацией ask_user, чтобы приложить к событию
			// реальный seq этой transition — фронт дедуплицирует по нему live-
			// событие с историческим двойником из /api/events.
			_, seq, _ := o.triggerWithSeq(stageID, bus.EvAskUser, bus.GuardCtx{Phase: q.Phase}, "")
			o.ui.Publish(bus.Event{
				Type:    bus.EventAskUser,
				StageID: stageID,
				Data: map[string]any{
					keyID: q.ID, keyPhase: q.Phase, "question": q.Question,
					"options": q.Options, "allow_custom": q.AllowCustom,
				},
				Seq: seq,
			})
		}
```

`"log"` в этом файле уже импортирован (используется в `normalizeMisplacedQuestion`) — новый импорт не требуется.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/orchestrator/... -run TestPollQuestions -v`
Expected: PASS (все три теста)

Run: `go test ./pkg/orchestrator/... 2>&1 | tail -40`
Expected: весь пакет по-прежнему зелёный, включая `integration_interactive_test.go` (interactive-путь не тронут)

- [ ] **Step 5: Commit**

```bash
git add pkg/orchestrator/dialog_poller.go pkg/orchestrator/dialog_poller_test.go
git commit -m "feat(orchestrator): pollQuestions авто-отвечает на вопросы non-interactive стадий вместо ask_user"
```

---

### Task 7: End-to-end интеграционный тест (реальный bash-агент)

**Files:**
- Test: `pkg/orchestrator/integration_auto_answer_test.go` (новый файл, `package orchestrator_test`)

**Interfaces:**
- Consumes: `orchestrator.New`, `orch.UIBus()`, `waitForStatus`, `loadStateJSON` (все — существующие тестовые хелперы из `integration_test.go`, тот же пакет).

Этот тест — черновой end-to-end прогон настоящего под-процесса (как `TestFullDialogCycle`), а не мок. Он должен УЖЕ проходить после Task 4 и Task 6 — новых продакшн-изменений в этой задаче нет, только фиксация сценария целиком.

- [ ] **Step 1: Write the test**

Создать `pkg/orchestrator/integration_auto_answer_test.go`:

```go
package orchestrator_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/mcp"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// TestIntegration_NonInteractiveStageAutoAnswersQuestion — полный e2e
// сценарий: non-interactive стадия (interactive не указан — дефолт false)
// запускает агента, который использует файловый диалоговый протокол (пишет
// question.json и ждёт answer.json в цикле — так же, как настоящий
// интерактивный агент). Стадия не должна ни разу побывать в
// awaiting_user_input, а должна дойти до done, получив ответ от afm.
func TestIntegration_NonInteractiveStageAutoAnswersQuestion(t *testing.T) {
	dir := t.TempDir()

	agentScript := filepath.Join(dir, "mock-agent.sh")
	script := "#!/bin/bash\n" +
		"STAGE_DIR=\"$AFM_STAGE_DIR\"\n" +
		"if [ -z \"$STAGE_DIR\" ]; then echo 'no AFM_STAGE_DIR' >&2; exit 1; fi\n" +
		`printf '{"id":"q1","question":"which option?","options":["Вариант A","Вариант B (recommended)"],"allow_custom":true}' > "$STAGE_DIR/implementation.q1.question.json"` + "\n" +
		"for i in $(seq 1 20); do\n" +
		"  if [ -f \"$STAGE_DIR/implementation.q1.answer.json\" ]; then break; fi\n" +
		"  sleep 0.5\n" +
		"done\n" +
		"if [ ! -f \"$STAGE_DIR/implementation.q1.answer.json\" ]; then echo 'timeout' >&2; exit 1; fi\n" +
		"echo done > \"$STAGE_DIR/.done\"\n" +
		"echo '{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"done\"}]}}'\n" +
		"echo '{\"type\":\"result\",\"subtype\":\"success\"}'\n"
	if err := os.WriteFile(agentScript, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	stages := []flow.Stage{{
		ID: "discovery", Name: "Discovery", Description: "non-interactive stage whose agent still asks a question",
		Agents:  []flow.AgentType{flow.AgentImplementation},
		Command: agentScript,
		// Interactive left unset (false) — exactly the scope this feature covers.
	}}

	stageDir := filepath.Join(dir, "discovery")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# plan"), 0644); err != nil {
		t.Fatal(err)
	}

	store, err := state.Open(dir, []string{"discovery"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	_ = store.Apply(&state.Transition{StageID: "discovery", From: state.StatusPending, To: state.StatusReady, Event: "test_setup"})
	stateFile := filepath.Join(dir, "state.json")

	orch := orchestrator.New(orchestrator.Options{
		RunDir:  dir,
		Stages:  stages,
		Store:   store,
		Config:  config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
	})

	subID, events := orch.UIBus().Subscribe(64)

	var mu sync.Mutex
	var autoAnsweredCount int
	sawAwaitingUserInput := false
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		for ev := range events {
			switch {
			case ev.Type == bus.EventAutoAnswered:
				mu.Lock()
				autoAnsweredCount++
				mu.Unlock()
			case ev.Type == bus.EventStageStatusChanged:
				if status, _ := ev.Data.(string); status == string(state.StatusAwaitingUserInput) {
					sawAwaitingUserInput = true
				}
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go func() { _ = orch.Run(ctx) }()

	waitForStatus(t, stateFile, "discovery", state.StatusDone, 15*time.Second)
	orch.UIBus().Unsubscribe(subID)
	<-watchDone

	if sawAwaitingUserInput {
		t.Error("non-interactive stage transitioned to awaiting_user_input; question should have been auto-answered")
	}
	mu.Lock()
	got := autoAnsweredCount
	mu.Unlock()
	if got != 1 {
		t.Fatalf("want 1 EventAutoAnswered, got %d", got)
	}

	dialogPath := filepath.Join(stageDir, "implementation.dialog.jsonl")
	entries, err := mcp.ReadDialog(dialogPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Answer == nil || *entries[0].Answer != "Вариант B" || !entries[0].AutoAnswered {
		t.Fatalf("dialog entry mismatch: %+v", entries)
	}
}
```

- [ ] **Step 2: Run the test**

Run: `go test ./pkg/orchestrator/... -run TestIntegration_NonInteractiveStageAutoAnswersQuestion -v`
Expected: PASS (Task 4 + Task 6 уже реализованы — это подтверждение сквозного сценария, а не новый прод-код)

Если тест падает — искать причину в Task 4/6 (напр. `AFM_STAGE_DIR` не пробросился в исполняемый скрипт), не в этом тесте.

- [ ] **Step 3: Run the full orchestrator test suite**

Run: `go test ./pkg/orchestrator/... -v 2>&1 | tail -80`
Expected: весь пакет зелёный

- [ ] **Step 4: Commit**

```bash
git add pkg/orchestrator/integration_auto_answer_test.go
git commit -m "test(orchestrator): e2e сценарий авто-ответа для non-interactive стадии с реальным bash-агентом"
```

---

### Task 8: Frontend — событие `auto_answered` в event feed

**Files:**
- Modify: `pkg/web/dashboard/src/types/afm-event.ts`
- Modify: `pkg/web/dashboard/src/components/event-feed/EventFeedPanel.tsx`
- Test: `pkg/web/dashboard/src/types/afm-event.test.ts`
- Test: `pkg/web/dashboard/src/components/event-feed/EventFeedPanel.test.tsx`

**Interfaces:**
- Produces: `'auto_answered'` добавлен в `AFM_EVENT_TYPES` (НЕ в `SIGNIFICANT_EVENT_TYPES` — это не значимая FSM-транзиция, статус стадии не меняется).

- [ ] **Step 1: Write the failing tests**

В `pkg/web/dashboard/src/types/afm-event.test.ts`, в тесте `AFM_EVENT_TYPES` (блок `expect(AFM_EVENT_TYPES).toEqual([...])`), добавить `'auto_answered'` в конец массива:

```ts
    expect(AFM_EVENT_TYPES).toEqual([
      'stage_status_changed',
      'approved',
      'revised',
      'retry_scheduled',
      'retry_exhausted',
      'manual_retry',
      'ask_user',
      'user_answered',
      'agent_action',
      'agent_completed',
      'supervisor_decision',
      'script_output',
      'hook_failed',
      'hook_resolved',
      'auto_answered',
    ])
```

В `pkg/web/dashboard/src/components/event-feed/EventFeedPanel.test.tsx`, добавить новый тест после `'renders script_output, hook_failed, and hook_resolved lines'`:

```ts
  test('renders auto_answered lines with the synthesized answer', () => {
    const events: AfmEvent[] = [
      { type: 'auto_answered', payload: { id: 'q1', phase: 'implementation', answer: 'Вариант B', from_options: true }, stageId: 's1', timestamp: '2026-08-07T10:00:00Z' },
    ]

    const { container } = render(<EventFeedPanel events={events} />)
    const entries = container.querySelectorAll('.feed-entry')
    expect(entries).toHaveLength(1)
    expect(entries[0]?.textContent).toContain('auto-answered')
    expect(entries[0]?.textContent).toContain('q1')
    expect(entries[0]?.textContent).toContain('Вариант B')
  })
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd pkg/web/dashboard && npx vitest run src/types/afm-event.test.ts src/components/event-feed/EventFeedPanel.test.tsx`
Expected: FAIL — `AFM_EVENT_TYPES` array mismatch; `auto_answered` line falls back to default `msg = event.type` (contains `auto_answered` but not the human-readable `auto-answered`/answer text expected by the new assertions)

- [ ] **Step 3: Write minimal implementation**

В `pkg/web/dashboard/src/types/afm-event.ts`, добавить `'auto_answered'` в конец `AFM_EVENT_TYPES` (НЕ добавлять в `SIGNIFICANT_EVENT_TYPES`):

```ts
export const AFM_EVENT_TYPES = [
  'stage_status_changed',
  'approved',
  'revised',
  'retry_scheduled',
  'retry_exhausted',
  'manual_retry',
  'ask_user',
  'user_answered',
  'agent_action',
  'agent_completed',
  'supervisor_decision',
  'script_output',
  'hook_failed',
  'hook_resolved',
  'auto_answered',
] as const
```

В `pkg/web/dashboard/src/components/event-feed/EventFeedPanel.tsx`, в `toFeedLine`'s `switch`, добавить case (после `case 'user_answered':`):

```ts
    case 'auto_answered': {
      const obj = isRecord(data) ? data : {}
      const id = typeof obj.id === 'string' ? obj.id : ''
      const answer = typeof obj.answer === 'string' ? obj.answer : ''
      msg = `auto-answered ${id}: ${answer}`
      msgClass = 'feed-msg action'
      break
    }
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd pkg/web/dashboard && npx vitest run src/types/afm-event.test.ts src/components/event-feed/EventFeedPanel.test.tsx`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/web/dashboard/src/types/afm-event.ts pkg/web/dashboard/src/types/afm-event.test.ts pkg/web/dashboard/src/components/event-feed/EventFeedPanel.tsx pkg/web/dashboard/src/components/event-feed/EventFeedPanel.test.tsx
git commit -m "feat(dashboard): событие auto_answered в event feed"
```

---

### Task 9: Frontend — визуальная пометка в `DialogChannel`

**Files:**
- Modify: `pkg/web/dashboard/src/components/dialog-channel/DialogChannel.tsx`
- Test: `pkg/web/dashboard/src/components/dialog-channel/DialogChannel.test.tsx`

**Interfaces:**
- Consumes: `auto_answered` field теперь присутствует в ответе `GET /api/stages/:id/dialog` (Task 3).
- Produces: не меняет публичные пропсы компонента — только рендер истории.

- [ ] **Step 1: Write the failing test**

Добавить в `pkg/web/dashboard/src/components/dialog-channel/DialogChannel.test.tsx` (после существующих тестов истории — искать по паттерну `RawDialogEntry` в файле):

```ts
  test('answered entry with auto_answered:true is marked distinctly from a real user answer', async () => {
    const history: RawDialogEntry & { auto_answered?: boolean } = {
      id: 'q1',
      phase: 'implementation',
      question: 'which option?',
      answer: 'Вариант B',
      auto_answered: true,
    }
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse([history]))

    const { container } = render(<DialogChannel stage={makeStage({ status: 'running' })} />)

    await waitFor(() => expect(container.querySelector('.qa')).not.toBeNull())
    expect(container.querySelector('.qa')).toHaveClass('qa-auto')
    expect(container.querySelector('.qa')?.textContent).toContain('Вариант B')
  })
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd pkg/web/dashboard && npx vitest run src/components/dialog-channel/DialogChannel.test.tsx -t "auto_answered"`
Expected: FAIL — `.qa` element lacks `qa-auto` class (field is parsed by nothing, `DialogEntry` type doesn't even carry it)

- [ ] **Step 3: Write minimal implementation**

В `pkg/web/dashboard/src/components/dialog-channel/DialogChannel.tsx`, добавить поле в тип `DialogEntry`:

```ts
type DialogEntry = {
  type?: string
  phase?: string
  question?: string
  answer?: string | null
  id?: string
  allow_custom?: boolean
  options?: string[]
  text?: string
  auto_answered?: boolean
}
```

В `renderHistory`, заменить блок рендера отвеченной записи:

```tsx
    if (entry.answer !== null && entry.answer !== undefined) {
      nodes.push(
        <div className={`qa${entry.auto_answered === true ? ' qa-auto' : ''}`} key={`qa-${index}`}>
          <div className="q">
            <MarkdownRenderer source={entry.question ?? ''} />
          </div>
          <div className="a">
            {entry.auto_answered === true && (
              <span className="auto-answered-badge" title="Answered automatically by afm">⚙</span>
            )}
            {`→ ${entry.answer}`}
          </div>
        </div>,
      )
    }
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd pkg/web/dashboard && npx vitest run src/components/dialog-channel/DialogChannel.test.tsx`
Expected: PASS (весь файл — новый тест + все существующие)

- [ ] **Step 5: Full frontend verification**

Run: `cd pkg/web/dashboard && npx vitest run && npx tsc --noEmit`
Expected: все тесты зелёные, тайпчек чист

- [ ] **Step 6: Commit**

```bash
git add pkg/web/dashboard/src/components/dialog-channel/DialogChannel.tsx pkg/web/dashboard/src/components/dialog-channel/DialogChannel.test.tsx
git commit -m "feat(dashboard): визуальная пометка auto-answered записей в диалоговой истории"
```

---

### Task 10: Финальная проверка всего стека

**Files:** нет новых/изменённых файлов — только верификация.

- [ ] **Step 1: Full Go suite**

Run: `go build ./... && go vet ./... && go test ./... 2>&1 | tail -100`
Expected: build чист, все пакеты зелёные

- [ ] **Step 2: Lint**

Run: `golangci-lint run ./...` (если недоступен — `go vet ./...` уже покрыт Step 1)
Expected: без замечаний

- [ ] **Step 3: Full frontend suite**

Run: `cd pkg/web/dashboard && npx vitest run && npx tsc --noEmit`
Expected: все тесты зелёные (210+ существующих + новые из Task 8/9), тайпчек чист

- [ ] **Step 4: Ручная проверка в браузере**

Собрать тестовый flow (по образцу `verify-project/flow.yaml` из ручной верификации Stream 3) с non-interactive стадией, чей mock-agent пишет `question.json` с одним вариантом, помеченным `(recommended)`, и ждёт `answer.json` в цикле. Прогнать `afm run`, открыть дашборд, убедиться:
- стадия не мелькает в `awaiting_user_input`;
- в панели диалога виден auto-answered маркер (⚙) у ответа;
- в event feed виден `auto-answered q1: ...`;
- стадия доходит до `done`.

- [ ] **Step 5: Обновить CLAUDE.md и docs/superpowers**

Добавить в корневой `CLAUDE.md` (в раздел рядом с "File-Based Dialog Protocol") краткое описание фичи авто-ответа, аналогично тому, как в этой сессии уже документировались persistent IDLE/BACKOFF — со ссылкой на `docs/superpowers/specs/2026-08-07-non-interactive-auto-answer-design.md` и этот план. Добавить запись в `pkg/web/dashboard/RELEASE_NOTES.md` про новую визуальную пометку auto-answered.

- [ ] **Step 6: Commit**

```bash
git add CLAUDE.md pkg/web/dashboard/RELEASE_NOTES.md
git commit -m "docs: авто-ответ на вопросы non-interactive стадий"
```
