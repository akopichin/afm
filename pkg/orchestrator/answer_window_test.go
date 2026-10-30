package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/mcp"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
	"github.com/akopichin/afm/pkg/state"
)

// Окно между тем, как ход автора увидел открытый вопрос, и тем, как он оставил стадию пользователю: ответ, Revise
// или Pause→Continue, попавшие в это окно. dialogHook вызывается ровно в нём; поллер вопросов в этих тестах не
// запущен (harness не вызывает Run).

func (h *summaryHarness) questionPath() string {
	return filepath.Join(h.stageDir, "autonomous_execution.q1.question.json")
}

// writeQuestion — агент оставляет открытый вопрос q1 рядом со своей работой.
func (h *summaryHarness) writeQuestion() {
	h.t.Helper()
	if err := os.WriteFile(h.questionPath(), []byte(`{"id":"q1","question":"which one?"}`), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

// answer — то, что делает дашборд: сначала файл ответа, затем уведомление.
func (h *summaryHarness) answer() {
	h.t.Helper()
	if err := mcp.WriteAnswerFile(h.stageDir, phaseAutonomous, "q1", "the first", false); err != nil {
		h.t.Errorf("answer file: %v", err)
		return
	}
	if err := h.o.NotifyAnswer("s1", phaseAutonomous, "q1", "the first", false); err != nil {
		h.t.Errorf("NotifyAnswer: %v", err)
	}
}

// criticalOfType забирает из critical-шины уже опубликованные события типа typ.
func criticalOfType(o *Orchestrator, typ bus.EventType) int {
	var other []bus.Event
	defer func() {
		for _, ev := range other {
			o.critical.TryPublish(ev)
		}
	}()
	n := 0
	for {
		select {
		case ev := <-o.critical.Recv():
			if ev.Type == typ {
				n++
			} else {
				other = append(other, ev)
			}
		default:
			return n
		}
	}
}

// Ответ записан после того, как ход увидел вопрос, и до того, как отдал стадию пользователю: ход видит ответ и идёт
// дальше сам — стадия не остаётся в awaiting_user_input с уже отвеченным вопросом.
func TestAnswerWindow_AnswerBeforeTheTurnLeavesIsNotStuck(t *testing.T) {
	h := newSummaryHarness(t, 15)
	h.o.dialogHook = func(_ string, left bool) {
		if !left {
			h.answer()
		}
	}
	h.run(func(int, string) error {
		h.writeQuestion()
		h.writeSummary("done\n")
		return nil
	}, nil)

	if h.calls != 1 || h.completions() != 1 {
		t.Fatalf("calls = %d, completions = %d; want 1 and 1: the turn that saw the answer completes itself", h.calls, h.completions())
	}
	h.wantStatus(state.StatusRunning)
	if n := criticalOfType(h.o, bus.EventUserAnswered); n != 0 {
		t.Fatalf("%d continuations queued: the answer belonged to the live turn", n)
	}
}

// Вопрос остался открытым, ответа нет: ход оставляет стадию пользователю и не завершает её.
func TestAnswerWindow_OpenQuestionLeavesTheStageWithTheUser(t *testing.T) {
	h := newSummaryHarness(t, 15)
	var left int
	h.o.dialogHook = func(_ string, isLeft bool) {
		if isLeft {
			left++
		}
	}
	h.run(func(int, string) error {
		h.writeQuestion()
		h.writeSummary("done\n")
		return nil
	}, nil)

	h.wantStatus(state.StatusAwaitingUserInput)
	if left != 1 || h.completions() != 0 {
		t.Fatalf("left = %d, completions = %d; want 1 and 0", left, h.completions())
	}
	if _, live := h.o.liveAgents.Load("s1"); live {
		t.Fatal("the turn has left, yet the stage is still marked as having a live agent")
	}
}

// Ответ после того, как ход оставил стадию: агент его не прочтёт, ответ обязан запустить ровно одно продолжение
// (critical-событие для onUserAnswered), а не применяться к ушедшему ходу.
func TestAnswerWindow_AnswerAfterTheTurnLeftQueuesOneContinuation(t *testing.T) {
	h := newSummaryHarness(t, 15)
	h.o.dialogHook = func(_ string, left bool) {
		if left {
			h.answer()
		}
	}
	h.run(func(int, string) error {
		h.writeQuestion()
		h.writeSummary("done\n")
		return nil
	}, nil)

	h.wantStatus(state.StatusAwaitingUserInput) // продолжение применит ответ в onUserAnswered
	if n := criticalOfType(h.o, bus.EventUserAnswered); n != 1 {
		t.Fatalf("%d continuations queued, want 1", n)
	}
}

// Revise попадает в то же окно: стадия принадлежит feedback-запуску Revise, а не пользователю.
func TestAnswerWindow_ReviseBeforeTheTurnLeavesKeepsTheStageForItsFeedbackRun(t *testing.T) {
	h := newSummaryHarness(t, 15)
	h.allowInterrupt = true
	h.o.dialogHook = func(stage string, left bool) {
		if !left {
			if _, ok := h.o.Trigger(stage, bus.EvRevise, bus.GuardCtx{}, "feedback"); !ok {
				t.Error("Revise was not applied")
			}
		}
	}
	h.run(func(int, string) error {
		h.writeQuestion()
		h.writeSummary("done\n")
		return nil
	}, nil)

	h.wantStatus(state.StatusRevising)
	if h.interrupted != 1 {
		t.Fatalf("onUserInterrupted called %d times, want 1: the old turn must hand the Revise to its feedback run", h.interrupted)
	}
	if h.completions() != 0 {
		t.Fatalf("completions = %d, want 0", h.completions())
	}
}

// Pause и Continue попадают в то же окно: прежний ход не отдаёт пользователю стадию, которую уже ведёт продолжение.
func TestAnswerWindow_PauseContinueBeforeTheTurnLeavesKeepsTheContinuationRunning(t *testing.T) {
	h := newSummaryHarness(t, 15)
	h.o.dialogHook = func(stage string, left bool) {
		if left {
			return
		}
		if _, ok := h.o.Trigger(stage, bus.EvPause, bus.GuardCtx{}, ""); !ok {
			t.Error("Pause was not applied")
		}
		if _, ok := h.o.Trigger(stage, bus.EvContinue, bus.GuardCtx{PausedFrom: state.StatusRunning}, ""); !ok {
			t.Error("Continue was not applied")
		}
	}
	h.run(func(int, string) error {
		h.writeQuestion()
		h.writeSummary("done\n")
		return nil
	}, nil)

	h.wantStatus(state.StatusRunning)
}

// Ответ гоняется с самим решением хода (с момента до взятия answerMu): кто бы ни выиграл, стадия не остаётся
// в awaiting_user_input с отвеченным вопросом без поставленного продолжения.
func TestAnswerWindow_AnswerRacingTheDialogDecisionNeverStrandsTheStage(t *testing.T) {
	for i := 0; i < 40; i++ {
		h := newSummaryHarness(t, 15)
		var wg sync.WaitGroup
		h.o.dialogHook = func(_ string, left bool) {
			if !left {
				wg.Go(h.answer)
			}
		}
		h.run(func(int, string) error {
			h.writeQuestion()
			h.writeSummary("done\n")
			return nil
		}, nil)
		wg.Wait()

		continuations := criticalOfType(h.o, bus.EventUserAnswered)
		if h.o.opts.Store.Get("s1") == state.StatusAwaitingUserInput {
			if continuations != 1 {
				t.Fatalf("iteration %d: awaiting_user_input with an answered question and %d continuations queued", i, continuations)
			}
		} else if h.completions() != 1 {
			t.Fatalf("iteration %d: status %s, completions %d: the turn that saw the answer must complete", i, h.o.opts.Store.Get("s1"), h.completions())
		}
	}
}

// Связка вопроса и summary-recovery (1/2): ответ пришёл до решения хода, а execution_summary.md ещё нет. Recovery не
// перехватывает ответ и не теряет его: ход идёт дальше, резерв берётся один раз, стадия завершается.
func TestAnswerWindow_AnswerBeforeLeaveThenSummaryRecoveryCompletes(t *testing.T) {
	h := newSummaryHarness(t, 15)
	var answerOnce sync.Once // решение хода повторяется после recovery-продолжения
	h.o.dialogHook = func(_ string, left bool) {
		if !left {
			answerOnce.Do(h.answer)
		}
	}
	h.run(func(call int, retryContext string) error {
		if call == 1 {
			h.writeQuestion() // вопрос есть, summary нет
			return nil
		}
		if !strings.Contains(retryContext, "(1/2)") {
			t.Errorf("call %d lacks the summary recovery instruction", call)
		}
		h.writeSummary("done\n")
		return nil
	}, nil)

	if h.calls != 2 || h.completions() != 1 {
		t.Fatalf("calls = %d, completions = %d; want 2 and 1", h.calls, h.completions())
	}
	if used := h.o.opts.Store.Stage("s1").SummaryRecoveryUsed; used != 1 {
		t.Fatalf("SummaryRecoveryUsed = %d, want 1", used)
	}
	raw, err := os.ReadFile(filepath.Join(h.stageDir, "autonomous_execution.q1.answer.json"))
	if err != nil || !strings.Contains(string(raw), `"answer":"the first"`) {
		t.Fatalf("answer.json = %q, %v: the user's answer was lost", raw, err)
	}
}

// Связка вопроса и summary-recovery (2/2): вопрос открыт, summary нет, ответа нет. Ход оставляет стадию
// пользователю и не тратит резерв; ответ запускает ровно одно продолжение, и только оно уходит в recovery.
func TestAnswerWindow_OpenQuestionWithMissingSummaryDoesNotSpendRecovery(t *testing.T) {
	h := newSummaryHarness(t, 15)
	h.run(func(int, string) error {
		h.writeQuestion()
		return nil
	}, nil)

	h.wantStatus(state.StatusAwaitingUserInput)
	if used := h.o.opts.Store.Stage("s1").SummaryRecoveryUsed; used != 0 || h.calls != 1 {
		t.Fatalf("used = %d, calls = %d; want 0 and 1: recovery must not intercept a question left for the user", used, h.calls)
	}

	h.answer() // не живой агент → одно продолжение
	if n := criticalOfType(h.o, bus.EventUserAnswered); n != 1 {
		t.Fatalf("%d continuations queued by the answer, want 1", n)
	}
	// Продолжение, как его запускает onUserAnswered: стадия возвращается в running и агент стартует заново.
	if _, ok := h.o.Trigger("s1", bus.EvUserAnswered, bus.GuardCtx{Phase: phaseAutonomous}, ""); !ok {
		t.Fatal("EvUserAnswered was not applied")
	}
	h.run(func(call int, retryContext string) error {
		if call == 3 {
			if !strings.Contains(retryContext, "(1/2)") {
				t.Error("the continuation's recovery turn lacks the instruction")
			}
			h.writeSummary("done\n")
		}
		return nil
	}, nil)

	if h.calls != 3 || h.completions() != 1 {
		t.Fatalf("calls = %d, completions = %d; want 3 and 1", h.calls, h.completions())
	}
	if used := h.o.opts.Store.Stage("s1").SummaryRecoveryUsed; used != 1 {
		t.Fatalf("SummaryRecoveryUsed = %d, want 1", used)
	}
}

// Ответ идёт живому агенту, только пока он отмечен в liveAgents; IsActive (слот занят, агент ещё не стартовал или уже
// решил выход) для этого недостаточно.
func TestAnswerDelivery_LiveAgentReadsTheAnswerItself(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	o.Trigger("s1", bus.EvAskUser, bus.GuardCtx{Phase: phaseAutonomous}, "")
	o.liveAgents.Store("s1", &attemptOwner{epoch: o.opts.Store.Stage("s1").OwnerEpoch})

	if err := o.resumeAfterAnswer("s1", phaseAutonomous, "q1", "a", false); err != nil {
		t.Fatal(err)
	}
	if got := o.currentStatus("s1"); got != state.StatusRunning {
		t.Fatalf("status = %s, want running: the live agent reads the answer", got)
	}
	if n := criticalOfType(o, bus.EventUserAnswered); n != 0 {
		t.Fatalf("%d continuations queued for a live agent", n)
	}
}

func TestAnswerDelivery_NoLiveAgentQueuesTheContinuation(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	o.Trigger("s1", bus.EvAskUser, bus.GuardCtx{Phase: phaseAutonomous}, "")

	if err := o.resumeAfterAnswer("s1", phaseAutonomous, "q1", "a", false); err != nil {
		t.Fatal(err)
	}
	if got := o.currentStatus("s1"); got != state.StatusAwaitingUserInput {
		t.Fatalf("status = %s, want awaiting_user_input until onUserAnswered resumes it", got)
	}
	if n := criticalOfType(o, bus.EventUserAnswered); n != 1 {
		t.Fatalf("%d continuations queued, want 1", n)
	}
}

// Событие ответа, дошедшее до event loop уже после старта нового живого хода, ничего не запускает.
func TestAnswerDelivery_OnUserAnsweredSkipsALiveAgent(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	o.Trigger("s1", bus.EvAskUser, bus.GuardCtx{Phase: phaseAutonomous}, "")
	o.liveAgents.Store("s1", &attemptOwner{epoch: o.opts.Store.Stage("s1").OwnerEpoch})

	err := o.onUserAnswered(context.Background(), bus.Event{
		Type: bus.EventUserAnswered, StageID: "s1", Data: map[string]any{keyPhase: phaseAutonomous},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := o.currentStatus("s1"); got != state.StatusAwaitingUserInput {
		t.Fatalf("status = %s: onUserAnswered resumed a stage whose live agent reads the answer itself", got)
	}
}

// Два хода одной стадии перекрываются (Pause→Continue при max_parallel>1): выход старого автора не снимает отметку
// нового, и ответ уходит новому живому автору без лишнего продолжения.
func TestAnswerDelivery_OldTurnExitKeepsTheNewTurnsMark(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	oldTurn, newTurn := &attemptOwner{epoch: o.opts.Store.Stage("s1").OwnerEpoch}, &attemptOwner{epoch: o.opts.Store.Stage("s1").OwnerEpoch}
	o.agentAnswers("s1", oldTurn)
	o.agentAnswers("s1", newTurn)

	o.stopAnswering("s1", oldTurn)
	if exit := o.leaveWithUser("s1", phaseAutonomous, oldTurn, true); exit != dialogNone {
		t.Fatalf("leaveWithUser = %v, want dialogNone", exit)
	}
	if mark, live := o.liveAgents.Load("s1"); !live || mark != newTurn {
		t.Fatalf("liveAgents = %v, %v: the old turn dropped the new turn's mark", mark, live)
	}

	o.Trigger("s1", bus.EvAskUser, bus.GuardCtx{Phase: phaseAutonomous}, "")
	if err := o.resumeAfterAnswer("s1", phaseAutonomous, "q1", "a", false); err != nil {
		t.Fatal(err)
	}
	if got := o.currentStatus("s1"); got != state.StatusRunning {
		t.Fatalf("status = %s, want running: the live new author reads the answer", got)
	}
	if n := criticalOfType(o, bus.EventUserAnswered); n != 0 {
		t.Fatalf("%d extra continuations queued for a live author", n)
	}

	o.stopAnswering("s1", newTurn)
	if _, live := o.liveAgents.Load("s1"); live {
		t.Fatal("a turn must remove its own mark")
	}
}

// pollerOrch — оркестратор с interactive-стадией для прогона pollQuestions без Run.
func pollerOrch(t *testing.T) (*Orchestrator, string) {
	t.Helper()
	o, runDir := setupHookOrch(t, "s1")
	stages := []flow.Stage{{ID: "s1", Interactive: true}}
	o.graph = graph.NewGraph(stages)
	o.opts.Stages = stages
	if err := os.MkdirAll(filepath.Join(runDir, "s1"), 0o755); err != nil {
		t.Fatal(err)
	}
	return o, runDir
}

func writeOpenQuestion(t *testing.T, runDir string) {
	t.Helper()
	path := filepath.Join(runDir, "s1", "autonomous_execution.q1.question.json")
	if err := os.WriteFile(path, []byte(`{"id":"q1","question":"which one?"}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Вопрос прерванного Revise-хода поллер откладывает: стадия остаётся revising, ключ не помечается processed, и
// следующий скан спросит пользователя, когда feedback-запуск вернул стадию в running.
func TestSurfaceQuestion_DefersWhileReviseOwnsTheStage(t *testing.T) {
	o, runDir := pollerOrch(t)
	writeOpenQuestion(t, runDir)
	if _, ok := o.Trigger("s1", bus.EvRevise, bus.GuardCtx{}, "feedback"); !ok {
		t.Fatal("Revise was not applied")
	}
	processed := map[string]bool{}
	malformed := map[string]*malformedQuestionState{}

	o.pollQuestions(processed, malformed)
	if got := o.currentStatus("s1"); got != state.StatusRevising {
		t.Fatalf("status = %s, want revising: the poller took the stage from its feedback run", got)
	}
	if len(processed) != 0 {
		t.Fatalf("processed = %v: a deferred question must be retried by the next scan", processed)
	}

	if _, ok := o.Trigger("s1", bus.EvStartRun, bus.GuardCtx{}, ""); !ok {
		t.Fatal("feedback run did not claim the Revise")
	}
	o.pollQuestions(processed, malformed)
	if got := o.currentStatus("s1"); got != state.StatusAwaitingUserInput {
		t.Fatalf("status = %s, want awaiting_user_input once the stage can be the user's", got)
	}
	if !processed["s1|autonomous_execution|q1"] {
		t.Fatalf("processed = %v, want the question marked", processed)
	}
}

// Ответ, записанный между сканом и переходом, виден surfaceQuestion: никого не спрашиваем.
func TestSurfaceQuestion_AnsweredSinceTheScanAsksNobody(t *testing.T) {
	o, runDir := pollerOrch(t)
	writeOpenQuestion(t, runDir)
	if err := mcp.WriteAnswerFile(filepath.Join(runDir, "s1"), phaseAutonomous, "q1", "a", false); err != nil {
		t.Fatal(err)
	}

	if _, move := o.surfaceQuestion("s1", phaseAutonomous, "q1"); move != questionAnswered {
		t.Fatalf("move = %v, want questionAnswered", move)
	}
	if got := o.currentStatus("s1"); got != state.StatusRunning {
		t.Fatalf("status = %s, want running: an answered question must not park the stage", got)
	}
}

func TestSurfaceQuestion_AsksTheUserFromARunningStage(t *testing.T) {
	o, runDir := pollerOrch(t)
	writeOpenQuestion(t, runDir)

	if _, move := o.surfaceQuestion("s1", phaseAutonomous, "q1"); move != questionAsked {
		t.Fatalf("move = %v, want questionAsked", move)
	}
	if got := o.currentStatus("s1"); got != state.StatusAwaitingUserInput {
		t.Fatalf("status = %s, want awaiting_user_input", got)
	}
}

// Полный Run: ход оставил вопрос без summary, пользователь ответил, продолжение (запущенное ответом) summary не
// написало и уходит в recovery, второе продолжение его пишет. Ответ не теряется, recovery берёт ровно один резерв.
type questionThenSummaryRunner struct {
	mu      sync.Mutex
	calls   int
	prompts []string
}

func (r *questionThenSummaryRunner) RunPlanning(context.Context, string, string, string, string) error {
	return nil
}

func (r *questionThenSummaryRunner) RunJSONQuery(context.Context, string) ([]byte, error) {
	return nil, nil
}

func (r *questionThenSummaryRunner) RunAgent(_ context.Context, _, _, prompt, logFile string) error {
	stageDir := filepath.Dir(logFile)
	r.mu.Lock()
	r.calls++
	n := r.calls
	r.prompts = append(r.prompts, prompt)
	r.mu.Unlock()
	switch n {
	case 1:
		return os.WriteFile(filepath.Join(stageDir, "autonomous_execution.q1.question.json"),
			[]byte(`{"id":"q1","question":"which one?"}`), 0o644)
	case 3:
		return os.WriteFile(filepath.Join(stageDir, "execution_summary.md"), []byte("done\n"), 0o644)
	}
	return nil
}

func TestAnswerHandoff_RunAnswerStartsOneContinuationThenSummaryRecovery(t *testing.T) {
	runDir := t.TempDir()
	stages := []flow.Stage{{ID: "s1", Name: "S1", Agents: []flow.AgentType{flow.AgentAuto}}}
	store, err := state.Open(runDir, []string{"s1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	runner := &questionThenSummaryRunner{}
	o := New(Options{
		RunDir: runDir, Stages: stages, Store: store, Config: config.Default(),
		Prompts: DefaultPrompts(), Runner: runner,
	})
	answered := make(chan struct{})
	var once sync.Once
	o.dialogHook = func(_ string, left bool) {
		if !left {
			return
		}
		once.Do(func() {
			// Ход оставил стадию пользователю (поллер ещё не тикал): отвечаем, как дашборд.
			if err := mcp.WriteAnswerFile(filepath.Join(runDir, "s1"), phaseAutonomous, "q1", "the first", false); err != nil {
				t.Errorf("answer file: %v", err)
			}
			if err := o.NotifyAnswer("s1", phaseAutonomous, "q1", "the first", false); err != nil {
				t.Errorf("NotifyAnswer: %v", err)
			}
			close(answered)
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	done := make(chan struct{})
	go func() { _ = o.Run(ctx); close(done) }()
	t.Cleanup(func() { // LIFO: сначала дождаться Run, потом закрыть Store
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("Run did not return within 10s of cancellation")
		}
	})

	select {
	case <-answered:
	case <-time.After(10 * time.Second):
		t.Fatal("the turn never left the stage with the user")
	}
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatalf("Run did not finish; stage = %s", store.Get("s1"))
	}

	if got := store.Get("s1"); got != state.StatusDone {
		t.Fatalf("stage = %s, want done", got)
	}
	runner.mu.Lock()
	calls, prompts := runner.calls, runner.prompts
	runner.mu.Unlock()
	if calls != 3 {
		t.Fatalf("author calls = %d, want 3: the turn, one continuation after the answer, one summary recovery", calls)
	}
	if strings.Contains(prompts[1], "Execution summary recovery") || !strings.Contains(prompts[2], "(1/2)") {
		t.Fatalf("only the third call carries the recovery instruction; prompts[1]=%q", prompts[1])
	}
	if used := store.Stage("s1").SummaryRecoveryUsed; used != 1 {
		t.Fatalf("SummaryRecoveryUsed = %d, want 1", used)
	}
}
