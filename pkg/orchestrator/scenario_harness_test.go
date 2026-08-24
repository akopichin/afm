package orchestrator_test

// Сценарийный харнесс для интеграционных тестов оркестратора.
//
// Идея: вместо ручного написания mock-Runner + setup-кода для каждого нового
// теста (см. rateLimitThenSuccessRunner, noDoneRunner в integration_retry_test.go)
// — описать сценарий
// декларативно (Scenario{Stages, Agents, Expect, ...}) и прогнать его через
// единый runScenario. scriptedRunner — единственная реализация executor.Runner,
// конфигурируемая картой AgentSpec на стадию.
//
// Task 1 закрыл неинтерактивный путь. Task 2 добавляет DIALOG seam:
// writeSynthAgent генерирует bash-скрипт для stage.Command интерактивных
// стадий (модель — TestFullDialogCycle/TestIntegration_MisplacedQuestionRelocated/
// TestIntegration_MisprefixedQuestionNormalized из integration_interactive_test.go),
// answerDialogs дожидается awaiting_user_input и отвечает через orch.NotifyAnswer.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/state"
)

// Injection описывает, какую неполадку нужно инжектировать в RunAgent для
// данной стадии (пусто — агент отрабатывает штатно).
type Injection int

const (
	InjectNone Injection = iota
	// InjectRateLimitThenOK: первый вызов RunAgent для стадии возвращает
	// retryable-ошибку (см. isRetryableError/Classify в retry.go/errors.go),
	// все последующие — отрабатывают штатно.
	InjectRateLimitThenOK
	// InjectNoDone: RunAgent "отрабатывает" без ошибки, но не создаёт артефакт
	// завершения — имитирует агента, который вышел, не закончив работу.
	InjectNoDone
)

// QuestionFault описывает неполадку с размещением question.json (Task 2).
type QuestionFault int

const (
	FaultNone QuestionFault = iota
	FaultWrongFolder
	FaultWrongPrefix
)

// QuestionInject описывает интерактивный диалог, который должна инициировать
// стадия (Task 2). В Task 1 не используется ни одним сценарием.
type QuestionInject struct {
	Phase  string
	ID     string
	Answer string // "" → никогда не отвечать (зависание)
	Fault  QuestionFault
}

// AgentSpec конфигурирует поведение scriptedRunner для одной стадии.
type AgentSpec struct {
	Interactive bool
	Question    *QuestionInject
	Inject      Injection
}

// Expectation описывает, что должно быть верно после завершения (или
// остановки на awaiting_user_input/таймауте) прогона сценария.
type Expectation struct {
	Statuses             map[string]state.StageStatus
	FilesPresent         map[string][]string // stageID → относительные пути, которые должны быть
	FilesAbsent          map[string][]string // stageID → пути, которых быть не должно
	ReachesAwaitingInput []string
	RunErrSubstr         string
}

// Scenario — декларативное описание одного end-to-end прогона оркестратора.
type Scenario struct {
	Name   string
	Stages []flow.Stage
	Agents map[string]AgentSpec
	Expect Expectation
	// PreSeedEventsLog, если не nil, записывается в runDir/events.jsonl ДО
	// state.Open (Task 3) — используется для проверки path'а восстановления
	// после порчи лога (corrupt-log-recovery): state.Open должен вернуть
	// ErrCorruptLog, что уже покрывается веткой RunErrSubstr в runScenario.
	PreSeedEventsLog []byte
}

// scriptedRunner — единственный mock executor.Runner для всех сценариев.
// Поведение конфигурируется картой agents (stageID → AgentSpec). Модель
// поведения — rateLimitThenSuccessRunner, noDoneRunner из соседних *_test.go.
type scriptedRunner struct {
	agents map[string]AgentSpec

	mu    sync.Mutex
	calls map[string]int // ключ agentType|logFile → счётчик (для InjectRateLimitThenOK)
}

func newScriptedRunner(agents map[string]AgentSpec) *scriptedRunner {
	return &scriptedRunner{agents: agents, calls: map[string]int{}}
}

func (r *scriptedRunner) RunJSONQuery(_ context.Context, _ string) ([]byte, error) {
	return nil, nil
}

// RunPlanning пишет валидный plan.md (проходит checkPlanCompletionFor: нужны
// секции ## Tasks / ## Assumptions / ## Acceptance Criteria).
func (r *scriptedRunner) RunPlanning(_ context.Context, _, _, outFile, _ string) error {
	plan := "## Tasks\n- [ ] step 1\n## Assumptions\n- none\n## Acceptance Criteria\n- [ ] done\n"
	return os.WriteFile(outFile, []byte(plan), 0644)
}

// RunAgent пишет артефакт завершения фазы, поддерживая инъекции неполадок.
// stageID выводится из имени stageDir (filepath.Base(filepath.Dir(logFile))),
// поскольку runAutonomousAgent/runImplementationAgent задают
// logFile = <stageDir>/<phase>.log.
func (r *scriptedRunner) RunAgent(_ context.Context, agentType, _, _, logFile string) error {
	stageDir := filepath.Dir(logFile)
	spec := r.agents[filepath.Base(stageDir)]

	switch spec.Inject {
	case InjectRateLimitThenOK:
		r.mu.Lock()
		k := agentType + "|" + logFile
		r.calls[k]++
		n := r.calls[k]
		r.mu.Unlock()
		if n == 1 {
			return errors.New("rate limit exceeded") // retryable — см. isRetryableError/Classify
		}
	case InjectNoDone:
		return nil // «отработал», но артефакт не создан → incomplete
	default:
		// без инъекции — нормальное завершение (артефакт пишется ниже)
	}

	if agentType == "autonomous_execution" {
		return os.WriteFile(filepath.Join(stageDir, "execution_summary.md"), []byte("## Summary\ndone\n"), 0644)
	}
	return os.WriteFile(filepath.Join(stageDir, ".done"), []byte("done\n"), 0644)
}

// compile-time check that scriptedRunner satisfies executor.Runner.
var _ executor.Runner = (*scriptedRunner)(nil)

// writeSynthAgent генерирует bash-скрипт "синтетического агента" для
// stage.Command интерактивной стадии. Модель — реальные проверенные тесты
// TestFullDialogCycle / TestIntegration_MisplacedQuestionRelocated /
// TestIntegration_MisprefixedQuestionNormalized (integration_interactive_test.go):
// скрипт пишет question.json (по умолчанию в $AFM_STAGE_DIR, либо с
// неполадкой размещения — см. QuestionFault), эмитит stream-json Write
// tool_use событие с этим путём (чтобы поллер узнал о файле через
// executor.WrittenFiles/<phase>.jsonl — как в model-тестах), затем поллит
// answer по СВОЕМУ пути с ограничением по числу итераций (~20с), чтобы
// Answer=="" (hung-dialog) не подвешивал субпроцесс навсегда — сценарий
// должен завершиться через ctx-таймаут харнесса runScenario, а не через
// зависший процесс. После ответа (или сразу, если Question==nil) пишет
// completion-артефакт фазы в $AFM_STAGE_DIR (канонический stageDir, вне
// зависимости от Fault — агент ошибается только в пути question/answer).
func writeSynthAgent(t *testing.T, spec AgentSpec) string {
	t.Helper()
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "synth-agent.sh")

	var b strings.Builder
	b.WriteString("#!/bin/bash\n")

	phase := ""
	if q := spec.Question; q != nil {
		phase = q.Phase
		var qExpr, aExpr string
		switch q.Fault {
		case FaultWrongPrefix:
			// Префикс = имя каталога стадии (basename $AFM_STAGE_DIR) вместо
			// канонической фазы — вычисляется в рантайме, т.к. writeSynthAgent
			// не знает stageID (он известен только оркестратору по RunDir/ID).
			b.WriteString(`STAGE_PREFIX="$(basename "$AFM_STAGE_DIR")"` + "\n")
			qExpr = fmt.Sprintf(`"$AFM_STAGE_DIR/$STAGE_PREFIX.%s.question.json"`, q.ID)
			aExpr = fmt.Sprintf(`"$AFM_STAGE_DIR/$STAGE_PREFIX.%s.answer.json"`, q.ID)
		case FaultWrongFolder:
			// Верхний уровень root_dir (= runDir харнесса, см. Options.RootDir в
			// runScenarioUpToAssert) — как и предписывает дизайн
			// relocateMisplacedQuestions (Task 2): скан root_dir идёт по его
			// ВЕРХНЕМУ уровню без рекурсии, поэтому файл должен оказаться
			// прямо в корне проекта ($AFM_STAGE_DIR/.. резолвится в runDir),
			// а не в произвольной вложенной поддиректории.
			qExpr = fmt.Sprintf(`"$AFM_STAGE_DIR/../%s.%s.question.json"`, q.Phase, q.ID)
			aExpr = fmt.Sprintf(`"$AFM_STAGE_DIR/../%s.%s.answer.json"`, q.Phase, q.ID)
		default: // FaultNone
			qExpr = fmt.Sprintf(`"$AFM_STAGE_DIR/%s.%s.question.json"`, q.Phase, q.ID)
			aExpr = fmt.Sprintf(`"$AFM_STAGE_DIR/%s.%s.answer.json"`, q.Phase, q.ID)
		}

		fmt.Fprintf(&b, "Q=%s\n", qExpr)
		fmt.Fprintf(&b, "A=%s\n", aExpr)
		fmt.Fprintf(&b, `echo '{"id":"%s","question":"q?","options":["a","b"]}' > "$Q"`+"\n", q.ID)
		b.WriteString(`echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":"'"$Q"'","content":"..."}}]}}'` + "\n")

		// Ограниченный поллинг (100 x 0.2с ≈ 20с): Answer=="" в hung-dialog
		// даёт скрипту сдаться и выйти, вместо того чтобы висеть вечно.
		b.WriteString("for i in $(seq 1 100); do\n")
		b.WriteString(`  if [ -f "$A" ]; then break; fi` + "\n")
		b.WriteString("  sleep 0.2\n")
		b.WriteString("done\n")
		b.WriteString(`if [ ! -f "$A" ]; then echo 'no answer received, giving up' >&2; exit 0; fi` + "\n")
	}

	b.WriteString(synthCompletionScript(phase))

	if err := os.WriteFile(scriptPath, []byte(b.String()), 0o755); err != nil {
		t.Fatalf("write synth agent script: %v", err)
	}
	return scriptPath
}

// synthCompletionScript возвращает bash-код, который завершает фазу в
// $AFM_STAGE_DIR и эмитит финальные stream-json строки, как в мок-агентах из
// integration_test.go/integration_interactive_test.go (mockPlanningScript и
// соседние).
//
// Планирование — ОСОБЫЙ случай: executor.RunPlanning сама пишет outFile
// (plan.md) из накопленного текста assistant-сообщений, ЕСЛИ агент не
// сигнализировал Write tool_use на тот же путь (agentWroteOutFile). Если бы
// мы одновременно писали plan.md напрямую (printf > plan.md) И эмитили любой
// текст после этого, RunPlanning перезаписал бы наш валидный файл этим
// текстом (agentWroteOutFile==false, textBuf непуст) — ровно так и
// проявлялся баг при первой попытке реализации. Поэтому для planning мы НЕ
// пишем файл напрямую — передаём валидный план как assistant text, и
// RunPlanning сам сохраняет его в outFile.
func synthCompletionScript(phase string) string {
	switch phase {
	case "planning":
		return `echo '{"type":"assistant","message":{"content":[{"type":"text","text":"## Tasks\n\n- [ ] step 1\n\n## Assumptions\n\n- none\n\n## Acceptance Criteria\n\n- [ ] done\n"}]}}'` + "\n" +
			`echo '{"type":"result","subtype":"success"}'` + "\n"
	case "autonomous_execution", "autonomous":
		return `printf '## Summary\ndone\n' > "$AFM_STAGE_DIR/execution_summary.md"` + "\n" +
			`echo '{"type":"assistant","message":{"content":[{"type":"text","text":"done"}]}}'` + "\n" +
			`echo '{"type":"result","subtype":"success"}'` + "\n"
	default:
		return `echo done > "$AFM_STAGE_DIR/.done"` + "\n" +
			`echo '{"type":"assistant","message":{"content":[{"type":"text","text":"done"}]}}'` + "\n" +
			`echo '{"type":"result","subtype":"success"}'` + "\n"
	}
}

// runScenario строит Store+Orchestrator по Scenario, запускает Run в фоне,
// обрабатывает диалоги (answerDialogs) и проверяет Expect.
func runScenario(t *testing.T, sc Scenario) {
	t.Helper()
	orch, runDir, reached := runScenarioUpToAssert(t, sc)
	if orch == nil {
		return // ожидаемая ошибка открытия лога (corrupt-log-recovery, Task 3) — уже проверена внутри
	}
	assertExpectation(t, orch, runDir, sc.Expect, reached)
}

// runScenarioUpToAssert выполняет весь сценарий (сборка Store+Orchestrator,
// фоновый Run, обработка диалогов, ожидание завершения, проверка
// RunErrSubstr) БЕЗ финальной проверки Expect через assertExpectation —
// оставляя её вызывающему коду.
//
// Вынесено из runScenario отдельной функцией ради
// TestScenarioHarness_FailsOnWrongExpectation (Task 3, scenario_test.go):
// негативному само-тесту харнесса нужен реально прошедший orch/runDir/reached
// от штатного прогона, чтобы затем самостоятельно вызвать assertExpectation
// с заведомо неверным Expectation и recordingT (а не настоящим *testing.T,
// Errorf которого безусловно валит тестовый процесс) — так само-тест
// доказывает, что assertExpectation реально детектирует расхождение, а не
// является no-op.
//
// Возвращает (nil, "", nil), если state.Open вернул ошибку, ожидаемую через
// sc.Expect.RunErrSubstr (corrupt-log сценарий) — дальше проверять нечего.
func runScenarioUpToAssert(t *testing.T, sc Scenario) (*orchestrator.Orchestrator, string, map[string]bool) {
	t.Helper()
	runDir := t.TempDir()

	// Предзасев events.jsonl ДО state.Open (Task 3): позволяет сценарию
	// смоделировать восстановление с уже существующим (в т.ч. битым) логом,
	// не трогая сам runScenario/scriptedRunner.
	if sc.PreSeedEventsLog != nil {
		if err := os.WriteFile(filepath.Join(runDir, "events.jsonl"), sc.PreSeedEventsLog, 0644); err != nil {
			t.Fatalf("pre-seed events.jsonl: %v", err)
		}
	}

	// Копируем Stages: интерактивные стадии (AgentSpec.Interactive) получают
	// stage.Interactive=true и stage.Command = сгенерированный synth-агент.
	stages := make([]flow.Stage, len(sc.Stages))
	copy(stages, sc.Stages)
	for i, s := range stages {
		if spec, ok := sc.Agents[s.ID]; ok && spec.Interactive {
			stages[i].Interactive = true
			stages[i].Command = writeSynthAgent(t, spec)
		}
	}

	ids := make([]string, len(stages))
	for i, s := range stages {
		ids[i] = s.ID
	}
	store, err := state.Open(runDir, ids)
	if err != nil {
		if sc.Expect.RunErrSubstr != "" && strings.Contains(err.Error(), sc.Expect.RunErrSubstr) {
			return nil, "", nil // ожидаемая ошибка открытия (corrupt-log сценарий, Task 3)
		}
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	runner := newScriptedRunner(sc.Agents)
	orch := orchestrator.New(orchestrator.Options{
		RunDir:  runDir,
		Stages:  stages,
		Store:   store,
		Config:  config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
		Runner:  runner,
		// RootDir — верхний уровень "root_dir" для FaultWrongFolder (Task 2):
		// relocateMisplacedQuestions сканирует top-level root_dir только когда
		// он задан, поэтому харнесс использует runDir как root_dir, соответствуя
		// реальному использованию (root_dir — родитель stageDir).
		RootDir: runDir,
	})

	// config.Default() задаёт 30-минутный idle-timeout — намного больше, чем
	// нам нужно для теста, поэтому ограничителем служит ctx (30с), как и
	// раньше: он же гарантирует, что hung-dialog не подвесит тест навсегда.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- orch.Run(ctx) }()

	// Авто-ответ на диалоги: ждёт awaiting_user_input и отвечает (или просто
	// фиксирует факт для hung-dialog, где Answer=="").
	reached := answerDialogs(t, orch, runDir, sc)

	select {
	case err := <-done:
		if sc.Expect.RunErrSubstr != "" {
			if err == nil || !strings.Contains(err.Error(), sc.Expect.RunErrSubstr) {
				t.Fatalf("expected Run error containing %q, got %v", sc.Expect.RunErrSubstr, err)
			}
		} else if err != nil && err != context.DeadlineExceeded {
			t.Fatalf("orch.Run: %v", err)
		}
	case <-time.After(35 * time.Second):
		t.Fatal("scenario timed out")
	}

	return orch, runDir, reached
}

// answerDialogs ждёт awaiting_user_input по каждой стадии с Question!=nil и
// отвечает (Answer!=""), либо — для hung-dialog (Answer=="") — только
// дожидается awaiting_user_input и фиксирует факт достижения (для
// assertExpectation/ReachesAwaitingInput), не отвечая никогда.
//
// Ответ пишется по КАНОНИЧЕСКОМУ пути ($AFM_STAGE_DIR/<phase>.<id>.answer.json),
// даже если вопрос был размещён с неполадкой (FaultWrongPrefix/FaultWrongFolder):
// именно туда указывает dangling-симлинк, который relocateMisplacedQuestions
// создаёт по (неверному) пути, который поллит агентский bash-скрипт — см.
// dialog_poller.go. Канонической фазой при неполадке размещения тоже всегда
// является q.Phase ("planning"), т.к. поллер публикует и триггерит FSM именно
// по исправленной фазе (see correctPhaseForState).
func answerDialogs(t *testing.T, orch *orchestrator.Orchestrator, runDir string, sc Scenario) map[string]bool {
	t.Helper()
	store := orchestrator.StoreFromOrch(orch)
	reached := make(map[string]bool)

	for _, s := range sc.Stages {
		spec, ok := sc.Agents[s.ID]
		if !ok || spec.Question == nil {
			continue
		}
		q := spec.Question

		if !waitAwaitingUserInput(store, s.ID, 15*time.Second) {
			continue // не дождались — assertExpectation отрапортует по ReachesAwaitingInput
		}
		reached[s.ID] = true

		if q.Answer == "" {
			continue // hung-dialog: пользователь никогда не отвечает
		}

		writeCanonicalAnswer(t, runDir, s.ID, q.Phase, q.ID, q.Answer)
		if err := orch.NotifyAnswer(s.ID, q.Phase, q.ID, q.Answer, false); err != nil {
			t.Fatalf("NotifyAnswer(%s): %v", s.ID, err)
		}
	}
	return reached
}

// waitAwaitingUserInput поллит store.Get(stageID) до state.StatusAwaitingUserInput
// с дедлайном timeout. Возвращает false, если стадия не дошла до этого статуса
// вовремя (не fatal — вызывающий код и assertExpectation репортуют отдельно).
func waitAwaitingUserInput(store *state.Store, stageID string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if store.Get(stageID) == state.StatusAwaitingUserInput {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return store.Get(stageID) == state.StatusAwaitingUserInput
}

// writeCanonicalAnswer атомарно (tmp+rename) пишет answer.json по каноническому
// пути стадии — так же, как это делает реальный HTTP-обработчик
// (handleDialogAnswer, см. CLAUDE.md) и существующие интеграционные тесты.
func writeCanonicalAnswer(t *testing.T, runDir, stageID, phase, id, answer string) {
	t.Helper()
	stageDir := filepath.Join(runDir, stageID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatalf("mkdir stageDir %s: %v", stageDir, err)
	}
	answerPath := filepath.Join(stageDir, phase+"."+id+".answer.json")
	payload, err := json.Marshal(map[string]any{"id": id, "answer": answer, "from_options": false})
	if err != nil {
		t.Fatalf("marshal answer: %v", err)
	}
	tmp := answerPath + ".tmp"
	if err := os.WriteFile(tmp, payload, 0644); err != nil {
		t.Fatalf("write answer tmp: %v", err)
	}
	if err := os.Rename(tmp, answerPath); err != nil {
		t.Fatalf("rename answer: %v", err)
	}
}

// expectationReporter — минимальный интерфейс, которого достаточно
// assertExpectation. Реализуется *testing.T (штатный путь через runScenario)
// и recordingT (TestScenarioHarness_FailsOnWrongExpectation, scenario_test.go):
// в отличие от настоящего *testing.T, чей Errorf безусловно помечает
// вызывающий тест проваленным, recordingT лишь собирает сообщения — это
// позволяет само-тесту вызвать assertExpectation с заведомо неверным
// Expectation и убедиться в наличии зафиксированных провалов, не заваливая
// собственный тестовый процесс.
type expectationReporter interface {
	Helper()
	Errorf(format string, args ...any)
}

func assertExpectation(t expectationReporter, orch *orchestrator.Orchestrator, runDir string, e Expectation, reached map[string]bool) {
	t.Helper()
	store := orchestrator.StoreFromOrch(orch)
	for id, want := range e.Statuses {
		if got := store.Get(id); got != want {
			t.Errorf("stage %s: status = %s, want %s", id, got, want)
		}
	}
	for id, files := range e.FilesPresent {
		for _, f := range files {
			if _, err := os.Stat(filepath.Join(runDir, id, f)); err != nil {
				t.Errorf("stage %s: expected file %q present: %v", id, f, err)
			}
		}
	}
	for id, files := range e.FilesAbsent {
		for _, f := range files {
			if _, err := os.Stat(filepath.Join(runDir, id, f)); err == nil {
				t.Errorf("stage %s: file %q should be absent", id, f)
			}
		}
	}
	for _, id := range e.ReachesAwaitingInput {
		if !reached[id] {
			t.Errorf("stage %s: expected to reach awaiting_user_input, but it did not", id)
		}
	}
}
