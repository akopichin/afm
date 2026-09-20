package orchestrator

// Характеризационные тесты P1.1 (Фаза 1 — PhaseExecutor, safety net ДО
// рефакторинга; см. docs/superpowers/plans/2026-09-19-phase1-phaseexecutor.md,
// Задачи 1.1-1.3). Эти тесты НЕ проверяют "правильное" поведение — они пинят
// ТЕКУЩЕЕ, уже существующее поведение 8 агентных entrypoint'ов из agents.go,
// чтобы более поздний рефакторинг (схлопывание в PhaseExecutor) мог быть
// проверен на behavior-identity. Производственный код в этом PR не менялся.
//
// Инвентаризация (Задача 1.3) — для каждого entrypoint и контракта, который
// рефакторинг рискует сломать: существующий тест, который его уже наблюдает,
// ЛИБО новый тест ниже, закрывающий пробел.
//
//   runPlanningAgent (fresh):
//     - RetryContext = retryContext + preNote            → УЖЕ наблюдается:
//       TestIntegration_PreNoteReachesFreshPrompt (integration_prenote_test.go).
//     - MkdirAll + clearStaleAutonomousFlag + EvStartPlanning → ПРОБЕЛ (нет
//       теста на очистку stale autonomous.flag) → закрыт здесь:
//       TestP11_RunPlanningAgent_Fresh_ClearsStaleAutonomousFlag.
//     - НЕ гейтится gateWithVerify → УЖЕ наблюдается:
//       TestIntegration_PlanningOnlyStageNeverInvokesVerify
//       (verify_wiring_integration_test.go), TestGateWithVerify_PlanningPhaseNeverGated
//       (gate_verify_test.go).
//
//   runPlanningWithFeedback: feedback.md + LatestPlanVersion → поля
//     Feedback/PreviousPlan, НЕ RetryContext → УЖЕ наблюдается:
//     TestIntegration_ResumeFromRevising, TestIntegration_ReviseFeedsBackOriginalPlanContent
//     (integration_resume_test.go). Порядок конкатенации в RetryContext здесь
//     тривиален (сам retryContext, без доп. note) — новый тест не нужен.
//
//   runImplementationAgent (fresh):
//     - RetryContext = retryContext + stageDirNote + preNote + verifyNote,
//       порядок побайтово → ПРОБЕЛ (существующие тесты проверяют только
//       присутствие подстрок, не порядок) → закрыт здесь:
//       TestP11_RunImplementationAgent_Fresh_NoteOrder.
//     - Embedded review в ТОЙ ЖЕ попытке, после implementation → частично
//       наблюдается TestIntegration_WithReviewAgent (integration_test.go,
//       только факт существования обоих логов) → усилено здесь:
//       TestP11_RunImplementationAgent_EmbeddedReview_OrderAndNoPreNoteInReviewPrompt,
//       TestP11_RunImplementationAgent_EmbeddedReviewSkippedOnImplementationFailure.
//     - verifyNote читается ДО implementation и тем же значением
//       переиспользуется для inline-review (не перечитывается) → ПРОБЕЛ →
//       закрыт здесь: TestP11_RunImplementationAgent_EmbeddedReview_ReusesCapturedVerifyNote.
//     - depPlans/artCtx собираются ОДИН РАЗ за попытку и переиспользуются для
//       inline-review (не пересобираются — иначе задублировался бы
//       bus.EventContextWarning) → ПРОБЕЛ → закрыт здесь:
//       TestP11_RunImplementationAgent_EmbeddedReview_ReusesCapturedDepPlans
//       (artCtx отдельным тестом не покрыт — тот же код-путь, что depPlans,
//       но требует более тяжёлой YAML-обвязки Inputs/Artifacts; depPlans уже
//       демонстрирует инвариант "захвачено один раз, не пересобирается").
//     - gateWithVerify(phaseImplementation, CheckCompletion) →
//       УЖЕ наблюдается: TestIntegration_VerifyFeedbackInjectedIntoCorrectionPrompt
//       (verify_wiring_integration_test.go).
//     - IMPORTANT: точное имя файла лога (fresh vs feedback vs embedded-review)
//       не наблюдалось НИ ОДНИМ тестом (ни старым, ни новым до этой правки) →
//       закрыто общим TestP11_LogFileNames_PinnedPerVariant (см. ниже, единая
//       табличная проверка по ВСЕМ 8 entrypoint'ам + embedded review).
//
//   runImplementationWithFeedback:
//     - RetryContext = retryContext + stageDirNote + feedbackNote + verifyNote
//       → присутствие уже наблюдается TestRunImplementationWithFeedback_InjectsVerifyFeedbackAlongsideHumanNote
//       (verify_feedback_inject_test.go), порядок — ПРОБЕЛ → закрыт здесь:
//       TestP11_RunImplementationWithFeedback_NoteOrder.
//     - CRITICAL (найдено ревью коллег): embedded-review блок agents.go:516-536
//       — байт-в-байт дубликат agents.go:308-329 (тот самый primary collapse
//       target плана) — был ПОЛНОСТЬЮ ненаблюдаем ни одним тестом (старым или
//       новым). Закрыто здесь тремя тестами, зеркалящими fresh-версию: порядок
//       вызовов + отсутствие note в review-промпте —
//       TestP11_RunImplementationWithFeedback_EmbeddedReview_OrderAndNoNoteInReviewPrompt;
//       review пропускается при падении feedback-implementation —
//       TestP11_RunImplementationWithFeedback_EmbeddedReviewSkippedOnImplementationFailure;
//       verifyNote/depPlans захватываются ДО implementation и переиспользуются
//       для review без повторного чтения/сбора —
//       TestP11_RunImplementationWithFeedback_EmbeddedReview_ReusesCapturedContext.
//
//   runReviewAgent (standalone, fresh):
//     - RetryContext = retryContext + preNote + verifyNote, порядок → ПРОБЕЛ
//       → закрыт здесь: TestP11_RunReviewAgent_Fresh_NoteOrder.
//     - gateWithVerify(phaseReview, CheckCompletion) реально подключён к
//       ЭТОМУ entrypoint'у (не только к обобщённому phaseImplementation в
//       gate_verify_test.go) → ПРОБЕЛ → закрыт здесь:
//       TestP11_RunReviewAgent_Fresh_VerifyGateInvoked.
//
//   runReviewWithFeedback:
//     - RetryContext = retryContext + feedbackNote + verifyNote, порядок →
//       ПРОБЕЛ → закрыт здесь: TestP11_RunReviewWithFeedback_NoteOrder.
//
//   runAutonomousAgent (fresh):
//     - MkdirAll + запись autonomous.flag → ПРОБЕЛ (нет прямого теста записи,
//       только на её последующее использование/очистку) → закрыт здесь:
//       TestP11_RunAutonomousAgent_Fresh_NoteOrder_AndFlagWritten (тот же тест
//       также пинит порядок summaryNote+preNote+verifyNote).
//     - gateWithVerify(phaseAutonomous, CheckAutonomousCompletion) → УЖЕ
//       наблюдается: TestIntegration_AutonomousVerifyRejectionHoldsThenCorrects
//       (verify_wiring_integration_test.go).
//
//   runAutonomousWithFeedback:
//     - RetryContext = retryContext + summaryNote + feedbackNote + verifyNote,
//       порядок; НЕ повторяет MkdirAll/autonomous.flag → порядок — ПРОБЕЛ →
//       закрыт здесь: TestP11_RunAutonomousWithFeedback_NoteOrder.
//
// Общий инвариант "planning никогда не гейтится verify, а impl/review/auto —
// гейтятся" (§5 матрицы плана, I20) закрыт связкой существующих
// TestGateWithVerify_PlanningPhaseNeverGated +
// TestIntegration_PlanningOnlyStageNeverInvokesVerify (planning) и
// TestIntegration_AutonomousVerifyRejectionHoldsThenCorrects +
// TestIntegration_VerifyFeedbackInjectedIntoCorrectionPrompt (impl/auto) +
// TestP11_RunReviewAgent_Fresh_VerifyGateInvoked (review, добавлен здесь).

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/orchestrator/verify"
	"github.com/akopichin/afm/pkg/state"
)

// newP11Orch строит минимальный Orchestrator для прямых вызовов run*Agent
// entrypoint'ов без реального подпроцесса — тот же приём, что уже используют
// setupHookOrch (hooks_test.go) и newGateVerifyTestOrchestrator
// (gate_verify_test.go). maxRetries/retryBackoff остаются нулевыми
// (zero value), поэтому runWithRetry делает РОВНО одну попытку — без сна и
// без сетевых вызовов, тесты быстрые и детерминированные.
func newP11Orch(t *testing.T, stageID string, initialStatus state.StageStatus, runner *p11CapturingRunner) (*Orchestrator, string) {
	t.Helper()
	rootDir := t.TempDir()
	runDir := t.TempDir()
	store, err := state.Open(runDir, []string{stageID})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if initialStatus != state.StatusPending {
		if err := store.Apply(&state.Transition{StageID: stageID, From: state.StatusPending, To: initialStatus, Event: "test_setup"}); err != nil {
			t.Fatal(err)
		}
	}
	stages := []flow.Stage{{ID: stageID}}
	o := &Orchestrator{
		opts:     Options{RootDir: rootDir, RunDir: runDir, Store: store, Stages: stages, Prompts: DefaultPrompts()},
		ui:       bus.NewUIBus(),
		critical: bus.NewCriticalBus(16),
		fsm:      bus.NewFSM(store),
		graph:    graph.NewGraph(stages),
		runner:   runner,
	}
	o.opts.Runner = runner
	return o, runDir
}

// p11Call — одно наблюдённое обращение к RunAgent/RunPlanning: тип агента
// (используется как метка фазы: phaseImplementation/phaseReview/… для
// RunAgent; постоянная "planning" для RunPlanning, где типа нет в сигнатуре),
// итоговый промпт и logFile — имя файла лога пиновать отдельно важно: план
// фиксирует его как byte-identity инвариант (fresh → flow.PhaseLogFile(...),
// feedback → хардкод "*-feedback.log"/"planning-revision.log", embedded
// review → ТОТ ЖЕ flow.PhaseLogFile(flow.PhaseReview), что и standalone
// review — см. TestP11_LogFileNames_PinnedPerVariant).
type p11Call struct {
	agentType string
	prompt    string
	logFile   string
}

// p11CapturingRunner — минимальная реализация executor.Runner (без реального
// подпроцесса), которая записывает каждый RunAgent-вызов и опционально: (а)
// возвращает ошибку для конкретного agentType (failOn) — модель "агент
// упал"; (б) пишет stageDir/.done после конкретного agentType (doneOn) —
// модель "агент успешно завершил работу и прошёл file-пробу"; (в) зовёт
// onCall(agentType) сразу после записи, ДО возврата — точка для теста,
// который хочет что-то изменить на диске МЕЖДУ implementation- и
// embedded-review-вызовом внутри одной попытки.
type p11CapturingRunner struct {
	mu     sync.Mutex
	calls  []p11Call
	failOn string
	doneOn string
	onCall func(agentType string)
}

// planningStub — валидный plan.md (все обязательные секции присутствуют), чтобы
// runPlanningAgent's post-RunPlanning валидация проходила чисто и НЕ уходила
// в rePromptMissingSections (второй RunPlanning-вызов усложнил бы подсчёт
// calls в тестах ниже без всякой пользы).
const planningStub = "## Tasks\n- t\n\n## Assumptions\n- a\n\n## Acceptance Criteria\n- c\n"

// p11PlanningAgentType — синтетическая метка agentType для p11Call-записей,
// рождённых RunPlanning (у RunPlanning в сигнатуре executor.Runner нет
// собственного agentType, в отличие от RunAgent) — используется только там,
// где тест сверяет agentType, а не только logFile.
const p11PlanningAgentType = "planning"

func (r *p11CapturingRunner) RunPlanning(ctx context.Context, stageName, prompt, outFile, logFile string) error {
	r.mu.Lock()
	r.calls = append(r.calls, p11Call{agentType: p11PlanningAgentType, prompt: prompt, logFile: logFile})
	r.mu.Unlock()
	return os.WriteFile(outFile, []byte(planningStub), 0644)
}

func (r *p11CapturingRunner) RunAgent(ctx context.Context, agentType, stageName, prompt, logFile string) error {
	r.mu.Lock()
	r.calls = append(r.calls, p11Call{agentType: agentType, prompt: prompt, logFile: logFile})
	r.mu.Unlock()
	if r.onCall != nil {
		r.onCall(agentType)
	}
	if r.failOn != "" && agentType == r.failOn {
		return errors.New("p11: forced failure for " + agentType)
	}
	if r.doneOn != "" && agentType == r.doneOn {
		_ = os.WriteFile(filepath.Join(filepath.Dir(logFile), ".done"), []byte("done"), 0644)
	}
	return nil
}

func (r *p11CapturingRunner) RunJSONQuery(ctx context.Context, prompt string) ([]byte, error) {
	return nil, nil
}

func (r *p11CapturingRunner) snapshot() []p11Call {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]p11Call{}, r.calls...)
}

// assertNoteOrder проверяет, что каждый маркер из markersInOrder найден в
// prompt и что их позиции строго возрастают в заданном порядке — то есть
// пинит порядок конкатенации кусков RetryContext (не просто их присутствие).
func assertNoteOrder(t *testing.T, prompt string, markersInOrder ...string) {
	t.Helper()
	prevIdx, prevMarker := -1, "<start>"
	for _, m := range markersInOrder {
		idx := strings.Index(prompt, m)
		if idx < 0 {
			t.Fatalf("expected marker %q in prompt, not found:\n%s", m, prompt)
		}
		if idx <= prevIdx {
			t.Errorf("note ordering broken: marker %q (at %d) must come AFTER %q (at %d):\n%s", m, idx, prevMarker, prevIdx, prompt)
		}
		prevIdx, prevMarker = idx, m
	}
}

// assertNoMarker проверяет, что маркер НЕ встречается в prompt — используется
// пинить отрицательный контракт (напр. embedded-review не получает preNote).
func assertNoMarker(t *testing.T, prompt, marker string) {
	t.Helper()
	if strings.Contains(prompt, marker) {
		t.Errorf("expected marker %q to be ABSENT from prompt, but found it:\n%s", marker, prompt)
	}
}

// countContextWarnings подписывается на o.ui, выполняет fn и считает, сколько
// раз за время fn был опубликован bus.EventContextWarning — используется,
// чтобы доказать, что depPlans собирается РОВНО ОДИН РАЗ за попытку и
// переиспользуется для embedded review, а не пересобирается: у тестовой
// стадии есть зависимость (DependsOn) БЕЗ plan.md, поэтому каждый вызов
// stagefiles.CollectDependencyPlans публикует ровно одно предупреждение —
// повторный сбор внутри той же попытки удвоил бы счётчик.
func countContextWarnings(o *Orchestrator, fn func()) int {
	subID, events := o.ui.Subscribe(64)
	defer o.ui.Unsubscribe(subID)
	fn()
	n := 0
	for {
		select {
		case ev := <-events:
			if ev.Type == bus.EventContextWarning {
				n++
			}
		default:
			return n
		}
	}
}

// seedActiveVerifyFeedback пишет verify/feedback.md (машинный feedback
// AI-verify) с blocking-finding, чей Title содержит title — тем же приёмом,
// что verify_feedback_inject_test.go, но параметризуемо, чтобы можно было
// отличить "до" и "после" содержимое файла в одном тесте.
func seedActiveVerifyFeedback(t *testing.T, stageDir, verID, title string) {
	t.Helper()
	result := verify.ModelResult{
		SchemaVersion: verify.SupportedSchemaVersion, Verdict: verify.VerdictNeedsChanges, Summary: "s",
		Findings: []verify.Finding{{Blocking: true, Title: title, Requirement: "req", Evidence: "ev", MinimalFix: "fix"}},
	}
	if err := stagefiles.WriteActiveFeedback(stageDir, verID, result, "codex", 1); err != nil {
		t.Fatal(err)
	}
}

// --- runPlanningAgent (fresh) ---------------------------------------------

// TestP11_RunPlanningAgent_Fresh_ClearsStaleAutonomousFlag пинит поведение,
// описанное в комментарии над runPlanningAgent (agents.go): свежий старт
// planning-трека удаляет autonomous.flag, оставшийся от предыдущей неудачной
// autonomous-попытки — иначе /api/status продолжал бы врать stage_autonomous.
func TestP11_RunPlanningAgent_Fresh_ClearsStaleAutonomousFlag(t *testing.T) {
	stageID := "s1"
	runner := &p11CapturingRunner{}
	o, runDir := newP11Orch(t, stageID, state.StatusPending, runner)
	stageDir := filepath.Join(runDir, stageID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	flagPath := filepath.Join(stageDir, "autonomous.flag")
	if err := os.WriteFile(flagPath, nil, 0644); err != nil {
		t.Fatal(err)
	}

	s := flow.Stage{ID: stageID, Agents: []flow.AgentType{flow.AgentPlanning}}
	o.runPlanningAgent(context.Background(), s)

	if _, err := os.Stat(flagPath); !os.IsNotExist(err) {
		t.Errorf("expected fresh planning to clear the stale autonomous.flag, stat err = %v", err)
	}
}

// --- runImplementationAgent (fresh) ---------------------------------------

// TestP11_RunImplementationAgent_Fresh_NoteOrder пинит побайтовый порядок
// RetryContext = retryContext + stageDirNote + preNote + verifyNote
// (agents.go's runImplementationAgent, addendum §11.2) — attempt 0 не несёт
// retryContext (пусто), поэтому первый видимый кусок — stageDirNote.
func TestP11_RunImplementationAgent_Fresh_NoteOrder(t *testing.T) {
	stageID := "impl1"
	runner := &p11CapturingRunner{}
	o, runDir := newP11Orch(t, stageID, state.StatusPending, runner)
	stageDir := filepath.Join(runDir, stageID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# Plan\n- step\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := state.SavePreNote(stageDir, "PRENOTE-MARKER"); err != nil {
		t.Fatal(err)
	}
	seedActiveVerifyFeedback(t, stageDir, "ver1", "verify-marker")

	s := flow.Stage{ID: stageID, Agents: []flow.AgentType{flow.AgentImplementation}}
	o.runImplementationAgent(context.Background(), s)

	calls := runner.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected exactly 1 RunAgent call (no review agent declared), got %d: %+v", len(calls), calls)
	}
	assertNoteOrder(t, calls[0].prompt,
		"Stage directory for .done file",
		"added before this stage started",
		verifyFeedbackHeading,
	)
}

// TestP11_RunImplementationAgent_EmbeddedReview_OrderAndNoPreNoteInReviewPrompt
// пинит два факта из addendum §11.2/§11.5: (1) когда стадия объявляет
// AgentReview, implementation-попытка делает ВТОРОЙ RunAgent-вызов (embedded
// review) СРАЗУ после первого, в той же попытке; (2) RetryContext ревью —
// это ГОЛЫЙ verifyNote (agents.go:322), БЕЗ preNote — в отличие от
// standalone runReviewAgent, где preNote есть.
func TestP11_RunImplementationAgent_EmbeddedReview_OrderAndNoPreNoteInReviewPrompt(t *testing.T) {
	stageID := "implrev"
	runner := &p11CapturingRunner{}
	o, runDir := newP11Orch(t, stageID, state.StatusPending, runner)
	stageDir := filepath.Join(runDir, stageID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# Plan\n- step\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := state.SavePreNote(stageDir, "PRENOTE-MARKER"); err != nil {
		t.Fatal(err)
	}
	seedActiveVerifyFeedback(t, stageDir, "ver1", "verify-marker")

	s := flow.Stage{ID: stageID, Agents: []flow.AgentType{flow.AgentImplementation, flow.AgentReview}}
	o.runImplementationAgent(context.Background(), s)

	calls := runner.snapshot()
	if len(calls) != 2 {
		t.Fatalf("expected implementation + embedded review (2 calls), got %d: %+v", len(calls), calls)
	}
	if calls[0].agentType != phaseImplementation {
		t.Errorf("expected call 0 to be implementation, got %q", calls[0].agentType)
	}
	if calls[1].agentType != phaseReview {
		t.Errorf("expected call 1 (embedded review) to be phaseReview, got %q", calls[1].agentType)
	}
	if !strings.Contains(calls[1].prompt, verifyFeedbackHeading) {
		t.Errorf("expected embedded review prompt to carry verifyNote, got:\n%s", calls[1].prompt)
	}
	assertNoMarker(t, calls[1].prompt, "added before this stage started")
}

// TestP11_RunImplementationAgent_EmbeddedReview_ReusesCapturedVerifyNote
// пинит addendum §11.5: verifyNote читается ОДИН РАЗ до запуска
// implementation и тем же значением переиспользуется для inline-review — НЕ
// перечитывается заново после implementation. Runner переписывает
// verify/feedback.md ВНУТРИ implementation-вызова (onCall) — если бы review
// перечитывал файл, он увидел бы "AFTER-marker" вместо захваченного
// "BEFORE-marker".
func TestP11_RunImplementationAgent_EmbeddedReview_ReusesCapturedVerifyNote(t *testing.T) {
	stageID := "implreuse"
	var stageDir string
	runner := &p11CapturingRunner{
		onCall: func(agentType string) {
			if agentType == phaseImplementation {
				seedActiveVerifyFeedback(t, stageDir, "ver2", "AFTER-marker")
			}
		},
	}
	o, runDir := newP11Orch(t, stageID, state.StatusPending, runner)
	stageDir = filepath.Join(runDir, stageID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# Plan\n- step\n"), 0644); err != nil {
		t.Fatal(err)
	}
	seedActiveVerifyFeedback(t, stageDir, "ver1", "BEFORE-marker")

	s := flow.Stage{ID: stageID, Agents: []flow.AgentType{flow.AgentImplementation, flow.AgentReview}}
	o.runImplementationAgent(context.Background(), s)

	calls := runner.snapshot()
	if len(calls) != 2 {
		t.Fatalf("expected implementation + embedded review (2 calls), got %d: %+v", len(calls), calls)
	}
	reviewPrompt := calls[1].prompt
	if !strings.Contains(reviewPrompt, "BEFORE-marker") {
		t.Errorf("expected embedded review to reuse the verifyNote captured BEFORE implementation ran (BEFORE-marker), got:\n%s", reviewPrompt)
	}
	if strings.Contains(reviewPrompt, "AFTER-marker") {
		t.Errorf("embedded review must NOT re-read verify/feedback.md after implementation — it reused a stale AFTER-marker:\n%s", reviewPrompt)
	}
}

// TestP11_RunImplementationAgent_EmbeddedReview_ReusesCapturedDepPlans пинит
// то же самое (addendum §11.5: "depPlans/artCtx переиспользуются, не
// пересобираются после implementation"), но для depPlans, а не verifyNote:
// stage зависит от dep1, у которого НЕТ plan.md — stagefiles.CollectDependencyPlans
// публикует bus.EventContextWarning РОВНО ОДИН РАЗ за сбор. Если бы embedded
// review пересобирал depPlans самостоятельно, тот же missing-dependency warning
// вышел бы ещё раз — итого 2 вместо 1.
func TestP11_RunImplementationAgent_EmbeddedReview_ReusesCapturedDepPlans(t *testing.T) {
	stageID := "implreusedeps"
	runner := &p11CapturingRunner{}
	o, runDir := newP11Orch(t, stageID, state.StatusPending, runner)
	stageDir := filepath.Join(runDir, stageID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# Plan\n- step\n"), 0644); err != nil {
		t.Fatal(err)
	}

	s := flow.Stage{ID: stageID, Agents: []flow.AgentType{flow.AgentImplementation, flow.AgentReview}, DependsOn: []string{"dep1"}}
	o.opts.Stages = append(o.opts.Stages, flow.Stage{ID: "dep1", Name: "Dep1"})

	warnings := countContextWarnings(o, func() {
		o.runImplementationAgent(context.Background(), s)
	})
	if warnings != 1 {
		t.Errorf("expected exactly 1 EventContextWarning (depPlans collected ONCE and reused for embedded review), got %d — embedded review likely re-collected depPlans", warnings)
	}

	calls := runner.snapshot()
	if len(calls) != 2 {
		t.Fatalf("expected implementation + embedded review (2 calls), got %d: %+v", len(calls), calls)
	}
}

// TestP11_RunImplementationAgent_EmbeddedReviewSkippedOnImplementationFailure
// пинит "review остаётся частью этой же попытки" (§1.2 таблицы) с обратной
// стороны: если сама implementation вернула ошибку, embedded review вообще
// не запускается — ошибка распространяется в обрамляющий runWithRetry
// implementation'а как есть (agents.go:304-306).
func TestP11_RunImplementationAgent_EmbeddedReviewSkippedOnImplementationFailure(t *testing.T) {
	stageID := "implfail"
	runner := &p11CapturingRunner{failOn: phaseImplementation}
	o, runDir := newP11Orch(t, stageID, state.StatusPending, runner)
	stageDir := filepath.Join(runDir, stageID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# Plan\n- step\n"), 0644); err != nil {
		t.Fatal(err)
	}

	s := flow.Stage{ID: stageID, Agents: []flow.AgentType{flow.AgentImplementation, flow.AgentReview}}
	o.runImplementationAgent(context.Background(), s)

	calls := runner.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected embedded review to be SKIPPED when implementation itself fails, got %d calls: %+v", len(calls), calls)
	}
	if calls[0].agentType != phaseImplementation {
		t.Errorf("expected the single recorded call to be the failed implementation attempt, got %q", calls[0].agentType)
	}
}

// --- runImplementationWithFeedback ----------------------------------------

// TestP11_RunImplementationWithFeedback_NoteOrder пинит порядок RetryContext
// = retryContext + stageDirNote + feedbackNote + verifyNote (agents.go:505).
func TestP11_RunImplementationWithFeedback_NoteOrder(t *testing.T) {
	stageID := "impl2"
	runner := &p11CapturingRunner{}
	o, runDir := newP11Orch(t, stageID, state.StatusRevising, runner)
	stageDir := filepath.Join(runDir, stageID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# Plan\n- step\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "feedback.md"), []byte("FEEDBACK-MARKER"), 0644); err != nil {
		t.Fatal(err)
	}
	seedActiveVerifyFeedback(t, stageDir, "ver1", "verify-marker")

	s := flow.Stage{ID: stageID, Agents: []flow.AgentType{flow.AgentImplementation}}
	o.runImplementationWithFeedback(context.Background(), s)

	calls := runner.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected exactly 1 RunAgent call (no review agent declared), got %d: %+v", len(calls), calls)
	}
	assertNoteOrder(t, calls[0].prompt,
		"Stage directory for .done file",
		"added while this stage was running",
		verifyFeedbackHeading,
	)
	assertNoMarker(t, calls[0].prompt, "added before this stage started")
}

// TestP11_RunImplementationWithFeedback_EmbeddedReview_OrderAndNoNoteInReviewPrompt
// — CRITICAL gap closed: agents.go:516-536 (runImplementationWithFeedback's
// inline-review block) is a byte-for-byte duplicate of agents.go:308-329
// (runImplementationAgent's), explicitly named by the plan as the primary
// collapse target — yet no test (old or new) ever exercised it before this
// addition. Mirrors TestP11_RunImplementationAgent_EmbeddedReview_OrderAndNoPreNoteInReviewPrompt:
// with AgentReview present, the feedback-implementation attempt makes a
// SECOND RunAgent call (embedded review), right after the first, in the same
// attempt; its RetryContext is the bare verifyNote (agents.go:529) — NEITHER
// preNote NOR feedbackNote leak into it.
func TestP11_RunImplementationWithFeedback_EmbeddedReview_OrderAndNoNoteInReviewPrompt(t *testing.T) {
	stageID := "implrevfb"
	runner := &p11CapturingRunner{}
	o, runDir := newP11Orch(t, stageID, state.StatusRevising, runner)
	stageDir := filepath.Join(runDir, stageID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# Plan\n- step\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "feedback.md"), []byte("FEEDBACK-MARKER"), 0644); err != nil {
		t.Fatal(err)
	}
	seedActiveVerifyFeedback(t, stageDir, "ver1", "verify-marker")

	s := flow.Stage{ID: stageID, Agents: []flow.AgentType{flow.AgentImplementation, flow.AgentReview}}
	o.runImplementationWithFeedback(context.Background(), s)

	calls := runner.snapshot()
	if len(calls) != 2 {
		t.Fatalf("expected feedback-implementation + embedded review (2 calls), got %d: %+v", len(calls), calls)
	}
	if calls[0].agentType != phaseImplementation {
		t.Errorf("expected call 0 to be implementation, got %q", calls[0].agentType)
	}
	if calls[1].agentType != phaseReview {
		t.Errorf("expected call 1 (embedded review) to be phaseReview, got %q", calls[1].agentType)
	}
	if !strings.Contains(calls[1].prompt, verifyFeedbackHeading) {
		t.Errorf("expected embedded review prompt to carry verifyNote, got:\n%s", calls[1].prompt)
	}
	assertNoMarker(t, calls[1].prompt, "added before this stage started")
	assertNoMarker(t, calls[1].prompt, "added while this stage was running")
}

// TestP11_RunImplementationWithFeedback_EmbeddedReviewSkippedOnImplementationFailure
// mirrors TestP11_RunImplementationAgent_EmbeddedReviewSkippedOnImplementationFailure
// for the feedback-restart path: if the feedback-implementation call itself
// fails, the embedded review is never reached (agents.go:512-514's early
// `return err`, BEFORE the `if s.HasAgent(flow.AgentReview)` block).
func TestP11_RunImplementationWithFeedback_EmbeddedReviewSkippedOnImplementationFailure(t *testing.T) {
	stageID := "implfailfb"
	runner := &p11CapturingRunner{failOn: phaseImplementation}
	o, runDir := newP11Orch(t, stageID, state.StatusRevising, runner)
	stageDir := filepath.Join(runDir, stageID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# Plan\n- step\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "feedback.md"), []byte("FEEDBACK-MARKER"), 0644); err != nil {
		t.Fatal(err)
	}

	s := flow.Stage{ID: stageID, Agents: []flow.AgentType{flow.AgentImplementation, flow.AgentReview}}
	o.runImplementationWithFeedback(context.Background(), s)

	calls := runner.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected embedded review to be SKIPPED when feedback-implementation itself fails, got %d calls: %+v", len(calls), calls)
	}
	if calls[0].agentType != phaseImplementation {
		t.Errorf("expected the single recorded call to be the failed implementation attempt, got %q", calls[0].agentType)
	}
}

// TestP11_RunImplementationWithFeedback_EmbeddedReview_ReusesCapturedContext
// mirrors TestP11_RunImplementationAgent_EmbeddedReview_ReusesCapturedVerifyNote
// + TestP11_RunImplementationAgent_EmbeddedReview_ReusesCapturedDepPlans for
// the feedback-restart path, combined: (1) verifyNote is captured BEFORE the
// feedback-implementation call and reused unmodified for the embedded review
// (the runner mutates verify/feedback.md mid-attempt via onCall — a
// re-reading review would see "AFTER-marker" instead of the captured
// "BEFORE-marker"); (2) depPlans is collected exactly ONCE per attempt (a
// missing dep1/plan.md makes stagefiles.CollectDependencyPlans warn exactly
// once per collection — re-collecting for the embedded review would double
// the bus.EventContextWarning count).
func TestP11_RunImplementationWithFeedback_EmbeddedReview_ReusesCapturedContext(t *testing.T) {
	stageID := "implreusefb"
	var stageDir string
	runner := &p11CapturingRunner{
		onCall: func(agentType string) {
			if agentType == phaseImplementation {
				seedActiveVerifyFeedback(t, stageDir, "ver2", "AFTER-marker")
			}
		},
	}
	o, runDir := newP11Orch(t, stageID, state.StatusRevising, runner)
	stageDir = filepath.Join(runDir, stageID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# Plan\n- step\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "feedback.md"), []byte("FEEDBACK-MARKER"), 0644); err != nil {
		t.Fatal(err)
	}
	seedActiveVerifyFeedback(t, stageDir, "ver1", "BEFORE-marker")

	s := flow.Stage{ID: stageID, Agents: []flow.AgentType{flow.AgentImplementation, flow.AgentReview}, DependsOn: []string{"dep1"}}
	o.opts.Stages = append(o.opts.Stages, flow.Stage{ID: "dep1", Name: "Dep1"})

	warnings := countContextWarnings(o, func() {
		o.runImplementationWithFeedback(context.Background(), s)
	})
	if warnings != 1 {
		t.Errorf("expected exactly 1 EventContextWarning (depPlans collected ONCE and reused for embedded review), got %d — embedded review likely re-collected depPlans", warnings)
	}

	calls := runner.snapshot()
	if len(calls) != 2 {
		t.Fatalf("expected feedback-implementation + embedded review (2 calls), got %d: %+v", len(calls), calls)
	}
	reviewPrompt := calls[1].prompt
	if !strings.Contains(reviewPrompt, "BEFORE-marker") {
		t.Errorf("expected embedded review to reuse the verifyNote captured BEFORE the feedback-implementation attempt ran (BEFORE-marker), got:\n%s", reviewPrompt)
	}
	if strings.Contains(reviewPrompt, "AFTER-marker") {
		t.Errorf("embedded review (feedback variant) must NOT re-read verify/feedback.md after implementation — it reused a stale AFTER-marker:\n%s", reviewPrompt)
	}
}

// --- runReviewAgent (standalone, fresh) -----------------------------------

// TestP11_RunReviewAgent_Fresh_NoteOrder пинит порядок RetryContext =
// retryContext + preNote + verifyNote (agents.go:365) — обратите внимание:
// в отличие от embedded review внутри implementation, standalone review
// ДЕЙСТВИТЕЛЬНО получает preNote.
func TestP11_RunReviewAgent_Fresh_NoteOrder(t *testing.T) {
	stageID := "rev1"
	runner := &p11CapturingRunner{}
	o, runDir := newP11Orch(t, stageID, state.StatusPending, runner)
	stageDir := filepath.Join(runDir, stageID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := state.SavePreNote(stageDir, "PRENOTE-MARKER"); err != nil {
		t.Fatal(err)
	}
	seedActiveVerifyFeedback(t, stageDir, "ver1", "verify-marker")

	s := flow.Stage{ID: stageID, Agents: []flow.AgentType{flow.AgentReview}}
	o.runReviewAgent(context.Background(), s)

	calls := runner.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected exactly 1 RunAgent call, got %d: %+v", len(calls), calls)
	}
	assertNoteOrder(t, calls[0].prompt, "added before this stage started", verifyFeedbackHeading)
}

// TestP11_RunReviewAgent_Fresh_VerifyGateInvoked пинит, что runReviewAgent
// реально оборачивает свой completionCheck в gateWithVerify(phaseReview, …) —
// gate_verify_test.go проверяет ТОЛЬКО обобщённый gateWithVerify-хелпер, а
// verify_wiring_integration_test.go покрывает planning/autonomous/impl, но не
// standalone review. Раннер пишет .done СРАЗУ после RunAgent (probe
// проходит), после чего RunVerification обязан реально вызваться.
func TestP11_RunReviewAgent_Fresh_VerifyGateInvoked(t *testing.T) {
	stageID := "revv"
	runner := &p11CapturingRunner{doneOn: phaseReview}
	o, runDir := newP11Orch(t, stageID, state.StatusPending, runner)
	stageDir := filepath.Join(runDir, stageID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}

	called := false
	o.runVerifyAgent = func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
		called = true
		return verify.RunOutcome{ProcessOK: true, Result: &verify.ModelResult{
			SchemaVersion: verify.SupportedSchemaVersion, Verdict: verify.VerdictPass, Summary: "ok",
		}}, nil
	}

	s := flow.Stage{ID: stageID, Agents: []flow.AgentType{flow.AgentReview},
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "codex"}}}}
	o.runReviewAgent(context.Background(), s)

	if !called {
		t.Error("expected runReviewAgent's completionCheck to be gateWithVerify-wrapped and invoke RunVerification once the .done probe passed")
	}
}

// --- runReviewWithFeedback -------------------------------------------------

// TestP11_RunReviewWithFeedback_NoteOrder пинит порядок RetryContext =
// retryContext + feedbackNote + verifyNote (agents.go:574).
func TestP11_RunReviewWithFeedback_NoteOrder(t *testing.T) {
	stageID := "rev2"
	runner := &p11CapturingRunner{}
	o, runDir := newP11Orch(t, stageID, state.StatusRevising, runner)
	stageDir := filepath.Join(runDir, stageID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "feedback.md"), []byte("FEEDBACK-MARKER"), 0644); err != nil {
		t.Fatal(err)
	}
	seedActiveVerifyFeedback(t, stageDir, "ver1", "verify-marker")

	s := flow.Stage{ID: stageID, Agents: []flow.AgentType{flow.AgentReview}}
	o.runReviewWithFeedback(context.Background(), s)

	calls := runner.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected exactly 1 RunAgent call, got %d: %+v", len(calls), calls)
	}
	assertNoteOrder(t, calls[0].prompt, "added while this stage was running", verifyFeedbackHeading)
	assertNoMarker(t, calls[0].prompt, "added before this stage started")
}

// --- runAutonomousAgent (fresh) --------------------------------------------

// TestP11_RunAutonomousAgent_Fresh_NoteOrder_AndFlagWritten пинит: (1)
// MkdirAll + запись autonomous.flag на свежем старте (agents.go:402-406); (2)
// порядок RetryContext = retryContext + summaryNote + preNote + verifyNote
// (agents.go:432).
func TestP11_RunAutonomousAgent_Fresh_NoteOrder_AndFlagWritten(t *testing.T) {
	stageID := "auto1"
	runner := &p11CapturingRunner{}
	o, runDir := newP11Orch(t, stageID, state.StatusPending, runner)
	stageDir := filepath.Join(runDir, stageID)
	if err := state.SavePreNote(stageDir, "PRENOTE-MARKER"); err != nil {
		t.Fatal(err)
	}
	seedActiveVerifyFeedback(t, stageDir, "ver1", "verify-marker")

	s := flow.Stage{ID: stageID, Agents: []flow.AgentType{flow.AgentAuto}}
	o.runAutonomousAgent(context.Background(), s)

	if _, err := os.Stat(filepath.Join(stageDir, "autonomous.flag")); err != nil {
		t.Errorf("expected fresh autonomous start to write autonomous.flag, stat err = %v", err)
	}

	calls := runner.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected exactly 1 RunAgent call, got %d: %+v", len(calls), calls)
	}
	assertNoteOrder(t, calls[0].prompt,
		"Write execution_summary.md here when done.",
		"added before this stage started",
		verifyFeedbackHeading,
	)
}

// --- runAutonomousWithFeedback ----------------------------------------------

// TestP11_RunAutonomousWithFeedback_NoteOrder пинит порядок RetryContext =
// retryContext + summaryNote + feedbackNote + verifyNote (agents.go:625) и то,
// что фидбек-раннер НЕ пишет autonomous.flag заново (стадия уже была
// активирована исходным runAutonomousAgent до прерывания — agents.go's doc
// comment над runAutonomousWithFeedback).
func TestP11_RunAutonomousWithFeedback_NoteOrder(t *testing.T) {
	stageID := "auto2"
	runner := &p11CapturingRunner{}
	o, runDir := newP11Orch(t, stageID, state.StatusRevising, runner)
	stageDir := filepath.Join(runDir, stageID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "feedback.md"), []byte("FEEDBACK-MARKER"), 0644); err != nil {
		t.Fatal(err)
	}
	seedActiveVerifyFeedback(t, stageDir, "ver1", "verify-marker")

	s := flow.Stage{ID: stageID, Agents: []flow.AgentType{flow.AgentAuto}}
	o.runAutonomousWithFeedback(context.Background(), s)

	calls := runner.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected exactly 1 RunAgent call, got %d: %+v", len(calls), calls)
	}
	assertNoteOrder(t, calls[0].prompt,
		"Write execution_summary.md here when done.",
		"added while this stage was running",
		verifyFeedbackHeading,
	)
	assertNoMarker(t, calls[0].prompt, "added before this stage started")

	if _, err := os.Stat(filepath.Join(stageDir, "autonomous.flag")); !os.IsNotExist(err) {
		t.Errorf("expected runAutonomousWithFeedback NOT to write autonomous.flag itself, stat err = %v", err)
	}
}

// --- Log filename identity (IMPORTANT gap) ---------------------------------

// TestP11_LogFileNames_PinnedPerVariant pins the exact log filename each
// entrypoint passes to RunAgent/RunPlanning — a byte-identity invariant the
// plan calls out explicitly (addendum §11.2's "универсальная дельта
// fresh↔feedback"): fresh runners use flow.PhaseLogFile(...) (the canonical
// per-phase name), feedback/*WithFeedback runners use a HARDCODED
// "*-feedback.log"/"planning-revision.log" name, and the embedded review log
// (both impl variants) is the SAME flow.PhaseLogFile(flow.PhaseReview) as
// standalone runReviewAgent's own log — a deliberate, documented collision
// (agents.go's runImplementationAgent doc comment: "Совпадение имени лога
// review внутри impl с standalone-review логом сохранить как есть"). A
// refactor that genericizes log-file naming (e.g. derives it purely from
// "fresh vs feedback" without per-adapter overrides) must fail at least one
// of these cases.
func TestP11_LogFileNames_PinnedPerVariant(t *testing.T) {
	cases := []struct {
		name     string
		stageID  string
		status   state.StageStatus
		agents   []flow.AgentType
		setup    func(t *testing.T, stageDir string)
		call     func(o *Orchestrator, s flow.Stage)
		wantLogs []string // expected p11Call.logFile per call, in order
	}{
		{
			name:    "planning fresh",
			stageID: "logs-plan-fresh",
			status:  state.StatusPending,
			agents:  []flow.AgentType{flow.AgentPlanning},
			setup:   func(t *testing.T, stageDir string) {},
			call:    func(o *Orchestrator, s flow.Stage) { o.runPlanningAgent(context.Background(), s) },
			wantLogs: []string{
				flow.PhaseLogFile(flow.PhasePlanning), // "planning.log"
			},
		},
		{
			name:    "planning feedback (revision)",
			stageID: "logs-plan-fb",
			status:  state.StatusRevising,
			agents:  []flow.AgentType{flow.AgentPlanning},
			setup: func(t *testing.T, stageDir string) {
				if err := os.MkdirAll(stageDir, 0755); err != nil {
					t.Fatal(err)
				}
			},
			call:     func(o *Orchestrator, s flow.Stage) { o.runPlanningWithFeedback(context.Background(), s) },
			wantLogs: []string{"planning-revision.log"},
		},
		{
			name:    "implementation fresh (no review)",
			stageID: "logs-impl-fresh",
			status:  state.StatusPending,
			agents:  []flow.AgentType{flow.AgentImplementation},
			setup: func(t *testing.T, stageDir string) {
				if err := os.MkdirAll(stageDir, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# Plan\n- step\n"), 0644); err != nil {
					t.Fatal(err)
				}
			},
			call: func(o *Orchestrator, s flow.Stage) { o.runImplementationAgent(context.Background(), s) },
			wantLogs: []string{
				flow.PhaseLogFile(flow.PhaseImplementation), // "implementation.log"
			},
		},
		{
			name:    "implementation feedback (no review)",
			stageID: "logs-impl-fb",
			status:  state.StatusRevising,
			agents:  []flow.AgentType{flow.AgentImplementation},
			setup: func(t *testing.T, stageDir string) {
				if err := os.MkdirAll(stageDir, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# Plan\n- step\n"), 0644); err != nil {
					t.Fatal(err)
				}
			},
			call:     func(o *Orchestrator, s flow.Stage) { o.runImplementationWithFeedback(context.Background(), s) },
			wantLogs: []string{"implementation-feedback.log"},
		},
		{
			// Embedded review, fresh implementation: the review log is the
			// SAME name as standalone runReviewAgent's own log — see below.
			name:    "implementation fresh + embedded review",
			stageID: "logs-impl-fresh-rev",
			status:  state.StatusPending,
			agents:  []flow.AgentType{flow.AgentImplementation, flow.AgentReview},
			setup: func(t *testing.T, stageDir string) {
				if err := os.MkdirAll(stageDir, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# Plan\n- step\n"), 0644); err != nil {
					t.Fatal(err)
				}
			},
			call: func(o *Orchestrator, s flow.Stage) { o.runImplementationAgent(context.Background(), s) },
			wantLogs: []string{
				flow.PhaseLogFile(flow.PhaseImplementation),
				flow.PhaseLogFile(flow.PhaseReview), // "review.log" — SAME as standalone
			},
		},
		{
			// Embedded review, feedback-restart implementation: the outer
			// log is the "-feedback" variant, but the INNER embedded review
			// log is still the plain flow.PhaseLogFile(flow.PhaseReview) —
			// NOT "review-feedback.log" (that name belongs only to
			// standalone runReviewWithFeedback, a different entrypoint).
			name:    "implementation feedback + embedded review",
			stageID: "logs-impl-fb-rev",
			status:  state.StatusRevising,
			agents:  []flow.AgentType{flow.AgentImplementation, flow.AgentReview},
			setup: func(t *testing.T, stageDir string) {
				if err := os.MkdirAll(stageDir, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# Plan\n- step\n"), 0644); err != nil {
					t.Fatal(err)
				}
			},
			call: func(o *Orchestrator, s flow.Stage) { o.runImplementationWithFeedback(context.Background(), s) },
			wantLogs: []string{
				"implementation-feedback.log",
				flow.PhaseLogFile(flow.PhaseReview), // "review.log" — NOT "review-feedback.log"
			},
		},
		{
			name:     "review fresh (standalone)",
			stageID:  "logs-rev-fresh",
			status:   state.StatusPending,
			agents:   []flow.AgentType{flow.AgentReview},
			setup:    func(t *testing.T, stageDir string) {},
			call:     func(o *Orchestrator, s flow.Stage) { o.runReviewAgent(context.Background(), s) },
			wantLogs: []string{flow.PhaseLogFile(flow.PhaseReview)}, // "review.log"
		},
		{
			name:    "review feedback (standalone)",
			stageID: "logs-rev-fb",
			status:  state.StatusRevising,
			agents:  []flow.AgentType{flow.AgentReview},
			setup: func(t *testing.T, stageDir string) {
				if err := os.MkdirAll(stageDir, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(stageDir, "feedback.md"), []byte("FEEDBACK-MARKER"), 0644); err != nil {
					t.Fatal(err)
				}
			},
			call:     func(o *Orchestrator, s flow.Stage) { o.runReviewWithFeedback(context.Background(), s) },
			wantLogs: []string{"review-feedback.log"},
		},
		{
			name:     "autonomous fresh",
			stageID:  "logs-auto-fresh",
			status:   state.StatusPending,
			agents:   []flow.AgentType{flow.AgentAuto},
			setup:    func(t *testing.T, stageDir string) {},
			call:     func(o *Orchestrator, s flow.Stage) { o.runAutonomousAgent(context.Background(), s) },
			wantLogs: []string{flow.PhaseLogFile(flow.PhaseAutonomous)}, // "autonomous.log"
		},
		{
			name:    "autonomous feedback",
			stageID: "logs-auto-fb",
			status:  state.StatusRevising,
			agents:  []flow.AgentType{flow.AgentAuto},
			setup: func(t *testing.T, stageDir string) {
				if err := os.MkdirAll(stageDir, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(stageDir, "feedback.md"), []byte("FEEDBACK-MARKER"), 0644); err != nil {
					t.Fatal(err)
				}
			},
			call:     func(o *Orchestrator, s flow.Stage) { o.runAutonomousWithFeedback(context.Background(), s) },
			wantLogs: []string{"autonomous-feedback.log"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := &p11CapturingRunner{}
			o, runDir := newP11Orch(t, tc.stageID, tc.status, runner)
			stageDir := filepath.Join(runDir, tc.stageID)
			tc.setup(t, stageDir)

			s := flow.Stage{ID: tc.stageID, Agents: tc.agents}
			tc.call(o, s)

			calls := runner.snapshot()
			if len(calls) != len(tc.wantLogs) {
				t.Fatalf("expected %d call(s), got %d: %+v", len(tc.wantLogs), len(calls), calls)
			}
			for i, want := range tc.wantLogs {
				gotBase := filepath.Base(calls[i].logFile)
				if gotBase != want {
					t.Errorf("call %d: logFile base = %q, want %q (full: %q)", i, gotBase, want, calls[i].logFile)
				}
			}
		})
	}
}
