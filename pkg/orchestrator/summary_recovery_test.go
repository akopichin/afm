package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/mcp"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/state"
)

// summaryHarness — runWithRetry автономной стадии с реальным file-probe summary и счётчиком вызовов автора.
type summaryHarness struct {
	t        *testing.T
	o        *Orchestrator
	runDir   string
	stageDir string
	calls    int
	// interrupted считает вызовы onUserInterrupted; по умолчанию они считаются ошибкой теста.
	interrupted    int
	allowInterrupt bool
	// interactive включает режим стадии с сессией (willResume).
	interactive bool
}

func newSummaryHarness(t *testing.T, maxRetries int) *summaryHarness {
	t.Helper()
	o, runDir := setupHookOrch(t, "s1")
	o.maxRetries = maxRetries
	o.retryBackoff = time.Millisecond
	stageDir := filepath.Join(runDir, "s1")
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return &summaryHarness{t: t, o: o, runDir: runDir, stageDir: stageDir}
}

func (h *summaryHarness) summaryPath() string {
	return filepath.Join(h.stageDir, "execution_summary.md")
}

func (h *summaryHarness) writeSummary(text string) {
	h.t.Helper()
	if err := os.WriteFile(h.summaryPath(), []byte(text), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

// run вызывает runWithRetry; agent получает 1-based номер вызова и сам решает, что писать на диск.
func (h *summaryHarness) run(agent func(call int, retryContext string) error, check func() error) {
	h.t.Helper()
	if check == nil {
		check = func() error { return stagefiles.CheckAutonomousCompletion(h.stageDir) }
	}
	h.o.runWithRetry(context.Background(), flow.Stage{ID: "s1", Interactive: h.interactive}, phaseAutonomous,
		func(retryContext string) error {
			h.calls++
			return agent(h.calls, retryContext)
		},
		check,
		func() {
			h.interrupted++
			if !h.allowInterrupt {
				h.t.Error("onUserInterrupted should not be called")
			}
		},
	)
}

// This harness does not run the event consumer. Count pending completion handoffs; durable
// completion notices are asserted separately after onAgentCompleted commits the fenced transition.
func (h *summaryHarness) completions() int {
	var events []bus.Event
	var n int
	for {
		select {
		case ev := <-h.o.critical.Recv():
			events = append(events, ev)
			if ev.Type == bus.EventAgentCompleted {
				n++
			}
		default:
			for _, ev := range events {
				h.o.critical.TryPublish(ev)
			}
			return n
		}
	}
}

func (h *summaryHarness) noticeCount(eventType string) int {
	h.t.Helper()
	data, err := os.ReadFile(filepath.Join(h.runDir, "notices.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		h.t.Fatal(err)
	}
	n := 0
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.Contains(line, `"type":"`+eventType+`"`) {
			n++
		}
	}
	return n
}

func (h *summaryHarness) wantStatus(want state.StageStatus) {
	h.t.Helper()
	if got := h.o.opts.Store.Get("s1"); got != want {
		h.t.Fatalf("status = %v, want %v", got, want)
	}
}

func TestSummaryRecovery_FirstRecoveryWritesSummary(t *testing.T) {
	h := newSummaryHarness(t, 15)
	h.run(func(call int, _ string) error {
		if call == 2 {
			h.writeSummary("done\n")
		}
		return nil
	}, nil)

	if h.calls != 2 {
		t.Fatalf("author calls = %d, want 2", h.calls)
	}
	if h.completions() != 1 {
		t.Fatalf("completions = %d, want 1", h.completions())
	}
}

func TestSummaryRecovery_SecondRecoveryWritesSummary(t *testing.T) {
	h := newSummaryHarness(t, 15)
	h.run(func(call int, _ string) error {
		if call == 3 {
			h.writeSummary("done\n")
		}
		return nil
	}, nil)

	if h.calls != 3 {
		t.Fatalf("author calls = %d, want 3 (run + two recoveries)", h.calls)
	}
	if h.completions() != 1 {
		t.Fatalf("completions = %d, want 1", h.completions())
	}
}

func TestSummaryRecovery_WhitespaceSummaryIsRecovered(t *testing.T) {
	h := newSummaryHarness(t, 15)
	h.run(func(call int, _ string) error {
		if call == 1 {
			h.writeSummary(" \n\t\n")
		}
		if call == 3 {
			h.writeSummary("done\n")
		}
		return nil
	}, nil)

	if h.calls != 3 || h.completions() != 1 {
		t.Fatalf("calls=%d completions=%d, want 3 and 1", h.calls, h.completions())
	}
}

func TestSummaryRecovery_ExhaustionFailsWithoutFourthCall(t *testing.T) {
	h := newSummaryHarness(t, 15)
	h.run(func(int, string) error { return nil }, nil)

	if h.calls != 3 {
		t.Fatalf("author calls = %d, want exactly 3", h.calls)
	}
	h.wantStatus(state.StatusFailed)
	if h.completions() != 0 {
		t.Fatalf("completions = %d, want 0", h.completions())
	}
}

func TestSummaryRecovery_TransportThenMissingKeepsOwnBudget(t *testing.T) {
	h := newSummaryHarness(t, 15)
	h.run(func(call int, _ string) error {
		switch call {
		case 1:
			return errors.New("overloaded")
		case 3:
			h.writeSummary("done\n")
		default:
		}
		return nil
	}, nil)

	if h.calls != 3 || h.completions() != 1 {
		t.Fatalf("calls=%d completions=%d, want 3 and 1", h.calls, h.completions())
	}
}

func TestSummaryRecovery_WorksWithMaxRetriesZero(t *testing.T) {
	h := newSummaryHarness(t, 0)
	h.run(func(call int, _ string) error {
		if call == 3 {
			h.writeSummary("done\n")
		}
		return nil
	}, nil)

	if h.calls != 3 || h.completions() != 1 {
		t.Fatalf("calls=%d completions=%d, want 3 and 1", h.calls, h.completions())
	}
}

func TestSummaryRecovery_TransportInsideRecoveryKeepsInstruction(t *testing.T) {
	h := newSummaryHarness(t, 15)
	var prompts []string
	h.run(func(call int, retryContext string) error {
		prompts = append(prompts, retryContext)
		switch call {
		case 2:
			return errors.New("overloaded") // транспортный сбой внутри recovery 1/2
		case 3:
			h.writeSummary("done\n")
		default:
		}
		return nil
	}, nil)

	if h.calls != 3 || h.completions() != 1 {
		t.Fatalf("calls=%d completions=%d, want 3 and 1", h.calls, h.completions())
	}
	for _, i := range []int{1, 2} {
		if !strings.Contains(prompts[i], h.summaryPath()) || !strings.Contains(prompts[i], "1/2") {
			t.Errorf("call %d lost the targeted 1/2 instruction: %q", i+1, prompts[i])
		}
	}
}

func TestSummaryRecovery_VerifyRejectThenMissingGetsSummaryBudget(t *testing.T) {
	h := newSummaryHarness(t, 15)
	rejected := false
	check := func() error {
		if err := stagefiles.CheckAutonomousCompletion(h.stageDir); err != nil {
			return err
		}
		if !rejected {
			rejected = true
			return newVerifyRejected(1)
		}
		return nil
	}
	h.run(func(call int, _ string) error {
		switch call {
		case 1:
			h.writeSummary("done\n")
		case 2:
			_ = os.Remove(h.summaryPath()) // verify-коррекция оставила стадию без summary
		case 3:
			h.writeSummary("done again\n")
		default:
		}
		return nil
	}, check)

	if h.calls != 3 || h.completions() != 1 {
		t.Fatalf("calls=%d completions=%d, want 3 and 1", h.calls, h.completions())
	}
}

func TestSummaryRecovery_AlternatingFailuresStayFinite(t *testing.T) {
	const maxRetries = 2
	h := newSummaryHarness(t, maxRetries)
	h.run(func(call int, _ string) error {
		if call%2 == 0 {
			return errors.New("overloaded")
		}
		return nil
	}, nil)

	// Старый потолок non-summary вызовов (maxRetries+1) плюс не более двух summary-продолжений.
	if limit := maxRetries + 1 + 2; h.calls > limit {
		t.Fatalf("author calls = %d, want <= %d", h.calls, limit)
	}
	h.wantStatus(state.StatusFailed)
}

func TestSummaryRecovery_DirectoryInPlaceOfSummaryFailsWithoutRecovery(t *testing.T) {
	h := newSummaryHarness(t, 15)
	if err := os.Mkdir(h.summaryPath(), 0o755); err != nil {
		t.Fatal(err)
	}
	h.run(func(int, string) error { return nil }, nil)

	if h.calls != 1 {
		t.Fatalf("author calls = %d, want 1 (a read error is not a missing summary)", h.calls)
	}
	h.wantStatus(state.StatusFailed)
}

// --- open question, owner fence, restore, prompt, notices, parity ---------------------------------

func (h *summaryHarness) used() int { return h.o.opts.Store.Stage("s1").SummaryRecoveryUsed }

// seedReserved имитирует уже выполненный ранее reserve (до рестарта процесса).
func (h *summaryHarness) seedReserved(used int, reason string) {
	h.t.Helper()
	epoch := h.o.opts.Store.Stage("s1").OwnerEpoch
	_, _, ok := h.o.triggerWithSeq("s1", bus.EvSummaryRecover, bus.GuardCtx{
		OwnerEpoch: &epoch, SummaryRecovery: &state.SummaryRecovery{Used: used, Reason: reason},
	}, "seed")
	if !ok {
		h.t.Fatal("seed reserve failed")
	}
}

func TestSummaryRecovery_OpenQuestionKeepsAwaitingInputAndSpendsNothing(t *testing.T) {
	h := newSummaryHarness(t, 15)
	question := filepath.Join(h.stageDir, "autonomous_execution.q1.question.json")
	if err := os.WriteFile(question, []byte(`{"id":"q1","question":"which db?"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	h.run(func(int, string) error { return nil }, nil)

	if h.calls != 1 || h.used() != 0 {
		t.Fatalf("calls=%d used=%d, want 1 and 0 (no recovery while the user is asked)", h.calls, h.used())
	}
	h.wantStatus(state.StatusAwaitingUserInput)
}

func TestSummaryRecovery_StaleOwnerAfterPauseContinueDoesNotReserve(t *testing.T) {
	h := newSummaryHarness(t, 15)
	h.o.summaryDecisionHook = func(id string) {
		// Pause→Continue: статус снова running, но владелец стадии уже другой (ABA).
		h.o.Trigger(id, bus.EvPause, bus.GuardCtx{}, "")
		h.o.Trigger(id, bus.EvContinue, bus.GuardCtx{PausedFrom: state.StatusRunning}, "")
	}
	h.run(func(int, string) error { return nil }, nil)

	if h.calls != 1 || h.used() != 0 {
		t.Fatalf("calls=%d used=%d, a stale author must neither reserve nor start the CLI", h.calls, h.used())
	}
	h.wantStatus(state.StatusRunning)
	if n := h.noticeCount(string(bus.EventRetryScheduled)); n != 0 {
		t.Fatalf("retry notices = %d, a lost CAS must not announce a recovery", n)
	}
}

func TestSummaryRecovery_PauseBeforeReserveIsNoOp(t *testing.T) {
	h := newSummaryHarness(t, 15)
	h.o.summaryDecisionHook = func(id string) { h.o.Trigger(id, bus.EvPause, bus.GuardCtx{}, "") }
	h.run(func(int, string) error { return nil }, nil)

	if h.calls != 1 || h.used() != 0 {
		t.Fatalf("calls=%d used=%d, want 1 and 0", h.calls, h.used())
	}
	h.wantStatus(state.StatusPaused)
}

func TestSummaryRecovery_ReviseBeforeReserveRestartsViaRegisteredOwner(t *testing.T) {
	h := newSummaryHarness(t, 15)
	h.allowInterrupt = true
	h.o.summaryDecisionHook = func(id string) { h.o.Trigger(id, bus.EvRevise, bus.GuardCtx{}, "feedback") }
	h.run(func(int, string) error { return nil }, nil)

	if h.calls != 1 || h.used() != 0 {
		t.Fatalf("calls=%d used=%d, want 1 and 0", h.calls, h.used())
	}
	if h.interrupted != 1 {
		t.Fatalf("onUserInterrupted calls = %d, want 1: the returned author owns the feedback restart", h.interrupted)
	}
	h.wantStatus(state.StatusRevising)
}

func TestSummaryRecovery_ReviseWithSuccessorLeavesRestartToSuccessor(t *testing.T) {
	h := newSummaryHarness(t, 15)
	h.o.summaryDecisionHook = func(id string) {
		h.o.Trigger(id, bus.EvRevise, bus.GuardCtx{}, "feedback")
		h.o.Trigger(id, bus.EvStartRun, bus.GuardCtx{}, "")
		h.o.interruptChans.Store(id, make(chan struct{}, 1)) // преемник зарегистрировал свой канал
	}
	h.run(func(int, string) error { return nil }, nil)

	if h.calls != 1 || h.used() != 0 || h.interrupted != 0 {
		t.Fatalf("calls=%d used=%d interrupted=%d, want 1/0/0: the successor serves the restart", h.calls, h.used(), h.interrupted)
	}
	h.wantStatus(state.StatusRunning)
}

func TestSummaryRecovery_QuestionAnswerBySameOwnerKeepsRightToReserve(t *testing.T) {
	h := newSummaryHarness(t, 15)
	h.o.summaryDecisionHook = func(id string) {
		h.o.Trigger(id, bus.EvAskUser, bus.GuardCtx{Phase: phaseAutonomous}, "")
		h.o.Trigger(id, bus.EvUserAnswered, bus.GuardCtx{Phase: phaseAutonomous}, "")
		h.o.summaryDecisionHook = nil
	}
	h.run(func(call int, _ string) error {
		if call == 2 {
			h.writeSummary("done\n")
		}
		return nil
	}, nil)

	if h.calls != 2 || h.used() != 1 || h.completions() != 1 {
		t.Fatalf("calls=%d used=%d completions=%d, want 2/1/1", h.calls, h.used(), h.completions())
	}
}

func TestSummaryRecovery_ExhaustedFailIsFenced(t *testing.T) {
	h := newSummaryHarness(t, 15)
	h.seedReserved(2, stagefiles.SummaryMissing)
	h.o.summaryDecisionHook = func(id string) {
		h.o.Trigger(id, bus.EvPause, bus.GuardCtx{}, "")
		h.o.Trigger(id, bus.EvContinue, bus.GuardCtx{PausedFrom: state.StatusRunning}, "")
	}
	h.run(func(int, string) error { return nil }, nil)

	h.wantStatus(state.StatusRunning) // устаревший автор не провалил стадию, которой уже владеет другой
	if h.calls != 1 {
		t.Fatalf("calls = %d, want 1", h.calls)
	}
	if n := h.noticeCount(string(bus.EventRetryExhausted)); n != 0 {
		t.Fatalf("exhausted notices = %d: a fail that lost the owner CAS must not be announced", n)
	}
}

// Успешный fenced-fail по исчерпанному бюджету объявляется ровно один раз.
func TestSummaryRecovery_ExhaustionIsAnnouncedAfterTheFail(t *testing.T) {
	h := newSummaryHarness(t, 15)
	h.run(func(int, string) error { return nil }, nil)

	h.wantStatus(state.StatusFailed)
	if n := h.noticeCount(string(bus.EventRetryExhausted)); n != 1 {
		t.Fatalf("exhausted notices = %d, want 1", n)
	}
}

func TestSummaryRecovery_RestartRestoresReservedInstructionWithoutNewSpend(t *testing.T) {
	h := newSummaryHarness(t, 15)
	h.seedReserved(1, stagefiles.SummaryMissing) // процесс упал после reserve 1/2
	var prompts []string
	h.run(func(call int, retryContext string) error {
		prompts = append(prompts, retryContext)
		if call == 2 {
			h.writeSummary("done\n")
		}
		return nil
	}, nil)

	if !strings.Contains(prompts[0], "(1/2)") {
		t.Errorf("restored run must carry the 1/2 instruction: %q", prompts[0])
	}
	if !strings.Contains(prompts[1], "(2/2)") {
		t.Errorf("the next real recovery must be 2/2: %q", prompts[1])
	}
	if h.calls != 2 || h.used() != 2 || h.completions() != 1 {
		t.Fatalf("calls=%d used=%d completions=%d, want 2/2/1", h.calls, h.used(), h.completions())
	}
}

func TestSummaryRecovery_RestartAfterBudgetSpentFailsAfterOneLastRun(t *testing.T) {
	h := newSummaryHarness(t, 15)
	h.seedReserved(2, stagefiles.SummaryEmpty)
	var retryContext string
	h.run(func(_ int, rc string) error { retryContext = rc; return nil }, nil)

	if h.calls != 1 {
		t.Fatalf("calls = %d, want 1: the reserved 2/2 run is restored, then the stage fails", h.calls)
	}
	if !strings.Contains(retryContext, "(2/2)") {
		t.Errorf("the restored last run must carry the 2/2 instruction: %q", retryContext)
	}
	h.wantStatus(state.StatusFailed)
}

func TestSummaryRecovery_RestartWithReadySummaryUsesNormalPath(t *testing.T) {
	h := newSummaryHarness(t, 15)
	h.seedReserved(1, stagefiles.SummaryMissing)
	h.writeSummary("done\n")
	var prompt string
	h.run(func(_ int, retryContext string) error { prompt = retryContext; return nil }, nil)

	if strings.Contains(prompt, "Execution summary recovery") {
		t.Errorf("a ready summary must not get a recovery instruction: %q", prompt)
	}
	if h.completions() != 1 || h.used() != 1 {
		t.Fatalf("completions=%d used=%d, want 1/1", h.completions(), h.used())
	}
}

func TestSummaryRecovery_TerminalCLIErrorBeatsSummary(t *testing.T) {
	h := newSummaryHarness(t, 15)
	h.run(func(int, string) error { return &executor.TerminalResultError{Message: "boom"} }, nil)

	if h.calls != 1 {
		t.Fatalf("calls = %d, want 1: a CLI failure is not a missing summary", h.calls)
	}
	h.wantStatus(state.StatusFailed)
	if h.used() != 0 {
		t.Fatalf("used = %d, want 0", h.used())
	}
}

func TestSummaryRecovery_DoneFileStagesKeepOldCorrection(t *testing.T) {
	o, runDir := setupHookOrch(t, "s1")
	o.maxRetries = 15
	stageDir := filepath.Join(runDir, "s1")
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	calls := 0
	o.runWithRetry(context.Background(), flow.Stage{ID: "s1"}, phaseImplementation,
		func(string) error { calls++; return nil },
		func() error { return stagefiles.CheckCompletion(stageDir, ".", flow.Stage{ID: "s1"}) },
		func() { t.Error("onUserInterrupted should not be called") },
	)

	if calls != 2 {
		t.Fatalf("calls = %d, want 2: .done keeps the single old correction, no summary budget", calls)
	}
	if got := o.opts.Store.Stage("s1").SummaryRecoveryUsed; got != 0 {
		t.Fatalf("used = %d, want 0", got)
	}
}

func TestSummaryRecovery_AutonomousVerifyRejectionBudgetUnchanged(t *testing.T) {
	h := newSummaryHarness(t, 15)
	h.writeSummary("done\n")
	h.run(func(int, string) error { return nil }, func() error { return newVerifyRejected(1) })

	if h.calls != 2 {
		t.Fatalf("calls = %d, want 2 (default verify budget: one correction)", h.calls)
	}
	h.wantStatus(state.StatusFailed)
	if h.used() != 0 {
		t.Fatalf("a verify rejection must not touch the summary budget, used=%d", h.used())
	}
}

func TestSummaryRecovery_NoticesStartRecoveredAndExhausted(t *testing.T) {
	h := newSummaryHarness(t, 15)
	h.run(func(call int, _ string) error {
		if call == 2 {
			h.writeSummary("done\n")
		}
		return nil
	}, nil)
	if h.noticeCount(string(bus.EventAgentCompleted)) != 0 {
		t.Fatal("completion notice preceded the durable completion transition")
	}
	if !commitQueuedCompletion(t, h.o) {
		t.Fatal("no completion queued")
	}
	if h.noticeCount(string(bus.EventAgentCompleted)) != 1 {
		t.Fatal("accepted completion must publish one durable notice")
	}

	data, _ := os.ReadFile(filepath.Join(h.runDir, "notices.jsonl"))
	for _, want := range []string{"summary recovery 1/2", "missing execution_summary.md", h.summaryPath(), "execution_summary.md recovered"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("notices lack %q:\n%s", want, data)
		}
	}

	ex := newSummaryHarness(t, 15)
	ex.run(func(int, string) error { return nil }, nil)
	if ex.noticeCount(string(bus.EventRetryExhausted)) != 1 {
		t.Fatal("exhaustion must publish one durable notice")
	}
	exData, _ := os.ReadFile(filepath.Join(ex.runDir, "notices.jsonl"))
	if !strings.Contains(string(exData), "after 2 recovery attempts") {
		t.Errorf("exhaustion notice lacks the attempt count:\n%s", exData)
	}
}

func TestSummaryRecovery_ReviseResetsBudgetAtomically(t *testing.T) {
	h := newSummaryHarness(t, 15)
	h.seedReserved(2, stagefiles.SummaryMissing)
	if applied, _, err := h.o.Revise(context.Background(), "s1", "please fix"); err != nil || !applied {
		t.Fatalf("Revise: applied=%v err=%v", applied, err)
	}
	if h.used() != 0 {
		t.Fatalf("used = %d, Revise must start a new cycle", h.used())
	}
}

func TestSummaryRecovery_PromptIsTargetedAndHonest(t *testing.T) {
	stageDir := filepath.Join(t.TempDir(), "stage dir")
	block := summaryRecoveryBlock(stageDir, &summaryRecovery{n: 2, reason: stagefiles.SummaryEmpty})
	path := stagefiles.SummaryPath(stageDir)
	for _, want := range []string{path, "exists but is empty", "(2/2)", "0 recovery run(s) left", "verify it", "Do NOT redo"} {
		if !strings.Contains(block, want) {
			t.Errorf("prompt lacks %q:\n%s", want, block)
		}
	}
	for _, banned := range []string{"## Summary", "## Changes", "## Result", "will be notified", "retry if"} {
		if strings.Contains(block, banned) {
			t.Errorf("prompt must not promise section checks or background delivery (%q):\n%s", banned, block)
		}
	}
	missing := summaryRecoveryBlock(stageDir, &summaryRecovery{n: 1, reason: stagefiles.SummaryMissing})
	if !strings.Contains(missing, "does not exist") || !strings.Contains(missing, "(1/2)") {
		t.Errorf("missing-file prompt wrong:\n%s", missing)
	}
}

func TestSummaryRecovery_FeedbackRecoveryReadsFeedbackInvocationLog(t *testing.T) {
	runner := &p11CapturingRunner{}
	o, runDir := newP11Orch(t, "auto3", state.StatusRevising, runner)
	stageDir := filepath.Join(runDir, "auto3")
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "feedback.md"), []byte("FEEDBACK-MARKER"), 0o644); err != nil {
		t.Fatal(err)
	}
	action := func(file string) string {
		return fmt.Sprintf(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":"%s"}}]}}`+"\n", file)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "autonomous.jsonl"), []byte(action("OLD-INVOCATION.go")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "autonomous-feedback.jsonl"), []byte(action("FEEDBACK-INVOCATION.go")), 0o644); err != nil {
		t.Fatal(err)
	}
	o.maxRetries = 0

	o.runAutonomousWithFeedback(context.Background(), flow.Stage{ID: "auto3", Agents: []flow.AgentType{flow.AgentAuto}})

	calls := runner.snapshot()
	if len(calls) != 3 {
		t.Fatalf("calls = %d, want 3 (feedback run + two recoveries)", len(calls))
	}
	second := calls[1].prompt
	for _, want := range []string{"FEEDBACK-INVOCATION.go", "FEEDBACK-MARKER", "(1/2)", stagefiles.SummaryPath(stageDir)} {
		if !strings.Contains(second, want) {
			t.Errorf("recovery prompt lacks %q", want)
		}
	}
	if strings.Contains(second, "OLD-INVOCATION.go") {
		t.Error("recovery context must come from the feedback invocation, not the earlier run")
	}
	if !strings.Contains(calls[2].prompt, "(2/2)") {
		t.Error("third call must carry the 2/2 instruction")
	}
}

func TestSummaryRecovery_ClassifiesSeparatelyFromIncompleteWork(t *testing.T) {
	err := fmt.Errorf("wrapped: %w", &SummaryError{Path: "p", Reason: stagefiles.SummaryMissing})
	if got := Classify(err); got != ClassSummaryMissing {
		t.Fatalf("Classify = %v, want ClassSummaryMissing", got)
	}
	if Classify(&IncompleteWorkError{Reason: "missing .done file"}) != ClassIncomplete {
		t.Fatal(".done incompleteness must keep its own class")
	}
}

func TestSummaryRecovery_ReserveIsNotALifecycleEvent(t *testing.T) {
	if _, ok := fsmLifecycleEvents[bus.EvSummaryRecover]; ok {
		t.Fatal("the internal reserve must not be published as a lifecycle hook event")
	}
}

func TestSummaryRecovery_StorageFailureOnReserveIsFatalAndStartsNothing(t *testing.T) {
	h := newSummaryHarness(t, 15)
	h.o.summaryDecisionHook = func(string) { _ = h.o.opts.Store.Close() } // журнал недоступен к моменту reserve
	h.run(func(int, string) error { return nil }, nil)

	if h.calls != 1 {
		t.Fatalf("calls = %d, want 1: no CLI after a failed intent", h.calls)
	}
	if h.o.loadFatal() == nil {
		t.Fatal("a storage failure on reserve must be fatal for the run")
	}
}

// seedAnsweredDialog кладёт отвеченный вопрос в историю диалога фазы.
func (h *summaryHarness) seedAnsweredDialog() {
	h.t.Helper()
	path := mcp.DialogPath(h.stageDir, phaseAutonomous)
	if err := mcp.AppendQuestion(path, mcp.Question{ID: "q1", Question: "which database?"}); err != nil {
		h.t.Fatal(err)
	}
	if err := mcp.AppendAnswer(path, mcp.Answer{ID: "q1", Answer: "postgres"}); err != nil {
		h.t.Fatal(err)
	}
}

func TestSummaryRecovery_SessionResumeSkipsDigestOtherwiseRestoresDialog(t *testing.T) {
	tests := []struct {
		name        string
		interactive bool
		session     bool
		wantDigest  bool
	}{
		{"interactive with session resumes", true, true, false},
		{"interactive without session starts fresh", true, false, true},
		{"non-interactive never resumes", false, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newSummaryHarness(t, 15)
			h.interactive = tt.interactive
			h.seedAnsweredDialog()
			if tt.session {
				if _, err := stagefiles.LoadOrCreateSession(h.stageDir, phaseAutonomous); err != nil {
					t.Fatal(err)
				}
			}
			var recoveryPrompt string
			h.run(func(call int, retryContext string) error {
				if call == 2 {
					recoveryPrompt = retryContext
					h.writeSummary("done\n")
				}
				return nil
			}, nil)

			if !strings.Contains(recoveryPrompt, "(1/2)") {
				t.Fatalf("no recovery prompt captured: %q", recoveryPrompt)
			}
			if got := strings.Contains(recoveryPrompt, "which database?") && strings.Contains(recoveryPrompt, "postgres"); got != tt.wantDigest {
				t.Errorf("dialog digest present = %v, want %v:\n%s", got, tt.wantDigest, recoveryPrompt)
			}
		})
	}
}

// pauseContinue — Pause→Continue поверх работающего автора: статус снова running, владелец стадии уже другой (ABA).
func (h *summaryHarness) pauseContinue() {
	h.o.Trigger("s1", bus.EvPause, bus.GuardCtx{}, "")
	h.o.Trigger("s1", bus.EvContinue, bus.GuardCtx{PausedFrom: state.StatusRunning}, "")
}

// Вопрос появился после решения хода о диалоге, но до reserve/fail: поллер уже отдал стадию пользователю. Решение о
// recovery принимается под тем же замком и видит вопрос — бюджет не тратится и стадия не проваливается.
func TestSummaryRecovery_QuestionAppearingBeforeTheDecisionKeepsTheUserWaiting(t *testing.T) {
	for _, used := range []int{0, 2} {
		t.Run(fmt.Sprintf("used=%d", used), func(t *testing.T) {
			h := newSummaryHarness(t, 15)
			if used > 0 {
				h.seedReserved(used, stagefiles.SummaryMissing)
			}
			h.o.summaryDecisionHook = func(string) {
				h.writeQuestion()
				h.o.surfaceQuestion("s1", phaseAutonomous, "q1")
			}
			h.run(func(int, string) error { return nil }, nil)

			h.wantStatus(state.StatusAwaitingUserInput)
			if h.calls != 1 || h.used() != used {
				t.Fatalf("calls=%d used=%d, want 1 and %d: a question beats reserve and exhaustion", h.calls, h.used(), used)
			}
			if n := h.noticeCount(string(bus.EventRetryExhausted)); n != 0 {
				t.Fatalf("exhausted notices = %d with the user asked", n)
			}
		})
	}
}

// Каждый исход CLI устаревшего автора (Pause→Continue поверх него) отсекается: стадию нового владельца он не валит,
// не переводит в retrying и не перезапускает.
func TestSummaryRecovery_StaleAuthorCLIOutcomesDoNotTouchTheNewOwner(t *testing.T) {
	cases := []struct {
		name       string
		maxRetries int
		agentErr   error
	}{
		{"terminal result error", 15, &executor.TerminalResultError{Message: "boom"}},
		{"retryable error", 15, errors.New("overloaded")},
		{"retryable error with no retries left", 0, errors.New("overloaded")},
		{"interrupted by the pause that Continue undid", 15, executor.ErrUserInterrupted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newSummaryHarness(t, tc.maxRetries)
			h.run(func(int, string) error {
				h.pauseContinue()
				return tc.agentErr
			}, nil)

			h.wantStatus(state.StatusRunning)
			if h.calls != 1 {
				t.Fatalf("calls = %d, want 1: the stale author must not restart", h.calls)
			}
			if n := h.noticeCount(string(bus.EventRetryExhausted)); n != 0 {
				t.Fatalf("exhausted notices = %d", n)
			}
		})
	}
}

// Ошибка чтения summary после Pause→Continue поверх автора — тоже не его fail.
func TestSummaryRecovery_StaleAuthorReadErrorDoesNotFailTheNewOwner(t *testing.T) {
	h := newSummaryHarness(t, 15)
	h.run(func(int, string) error { return nil }, func() error {
		h.pauseContinue()
		return &fs.PathError{Op: "read", Path: h.summaryPath(), Err: fs.ErrPermission}
	})

	h.wantStatus(state.StatusRunning)
	if h.calls != 1 {
		t.Fatalf("calls = %d, want 1", h.calls)
	}
}

// Автор потерял владение, пока шли probe/verify (Pause→Continue): валидный summary не завершает стадию нового владельца
// и не запускает преемников.
func TestSummaryRecovery_StaleAuthorDoesNotCompleteAfterTheCheck(t *testing.T) {
	h := newSummaryHarness(t, 15)
	h.run(func(int, string) error { return nil }, func() error {
		h.pauseContinue()
		return nil
	})

	h.wantStatus(state.StatusRunning)
	if h.completions() != 0 {
		t.Fatalf("completions = %d, want 0: the new owner decides completion", h.completions())
	}
}

// Резерв списан, затем (до запуска CLI) Pause→Continue: устаревший автор не запускает второй CLI. Ловит ранний отсев.
func TestSummaryRecovery_StaleAuthorDoesNotLaunchAfterReserve(t *testing.T) {
	h := newSummaryHarness(t, 15)
	h.o.beforeLaunchHook = func(string) {
		if h.used() == 1 { // резерв предыдущей итерации уже зафиксирован
			h.o.beforeLaunchHook = nil
			h.pauseContinue()
		}
	}
	h.run(func(int, string) error { return nil }, nil)

	if h.calls != 1 {
		t.Fatalf("calls = %d, want 1: the reserved continuation of a stale author must not start", h.calls)
	}
	h.wantStatus(state.StatusRunning)
}

// Pause→Continue и преемник успевают между отсевом и запуском CLI (max_parallel>1, второй слот свободен): раннер
// устаревшего автора слушает канал самого автора, где Pause оставил сигнал, а не канал преемника — subprocess не стартует.
func TestSummaryRecovery_StaleAuthorCLINeverStartsOverTheSuccessor(t *testing.T) {
	h := newSummaryHarness(t, 15)
	marker := filepath.Join(h.runDir, "second-cli-started")
	script := filepath.Join(h.runDir, "agent.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	stage := flow.Stage{ID: "s1", Command: script}
	authorCh := make(chan struct{}, 1)
	var runErr error
	h.o.runWithRetryOpts(context.Background(), stage, phaseAutonomous, retryOptions{interruptCh: authorCh},
		func(string) error {
			h.calls++
			if h.calls == 1 {
				return nil // summary нет: резерв и повторный запуск
			}
			// Барьер после отсева: Pause (сигнал в канал автора) → Continue → преемник регистрирует свой канал.
			h.o.Trigger("s1", bus.EvPause, bus.GuardCtx{}, "")
			authorCh <- struct{}{}
			h.o.Trigger("s1", bus.EvContinue, bus.GuardCtx{PausedFrom: state.StatusRunning}, "")
			h.o.interruptChans.Store("s1", make(chan struct{}, 1))
			runErr = h.o.runnerForTurn(stage, phaseAutonomous, authorCh).
				RunAgent(context.Background(), phaseAutonomous, "s1", "prompt", filepath.Join(h.stageDir, "a.log"))
			return runErr
		},
		func() error { return stagefiles.CheckAutonomousCompletion(h.stageDir) },
		func() { t.Error("onUserInterrupted should not be called") },
	)

	if !errors.Is(runErr, executor.ErrUserInterrupted) {
		t.Fatalf("RunAgent err = %v, want ErrUserInterrupted before the subprocess starts", runErr)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the stale author started a second CLI")
	}
	h.wantStatus(state.StatusRunning)
	if h.calls != 2 || h.completions() != 0 {
		t.Fatalf("calls=%d completions=%d, want 2 and 0", h.calls, h.completions())
	}
}

// Боевой путь: runAutonomousAttempt отдаёт раннеру канал СВОЕГО хода. Pause→Continue и преемник успевают до сборки
// раннера — автор слушает канал, где Pause оставил сигнал, поэтому subprocess не стартует поверх преемника.
func TestRunAutonomousAttempt_StaleAuthorListensToItsOwnInterruptChannel(t *testing.T) {
	h := newSummaryHarness(t, 15)
	marker := filepath.Join(h.runDir, "cli-started")
	script := filepath.Join(h.runDir, "agent.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	h.o.beforeLaunchHook = func(id string) {
		h.o.beforeLaunchHook = nil
		h.o.Trigger(id, bus.EvPause, bus.GuardCtx{}, "")
		if ch, ok := h.o.interruptChans.Load(id); ok { // Pause сигналит канал, зарегистрированный на этот момент
			ch.(chan struct{}) <- struct{}{}
		}
		h.o.Trigger(id, bus.EvContinue, bus.GuardCtx{PausedFrom: state.StatusRunning}, "")
		h.o.interruptChans.Store(id, make(chan struct{}, 1)) // преемник
	}

	h.o.runAutonomousAttempt(context.Background(), flow.Stage{ID: "s1", Command: script}, h.stageDir, "",
		"autonomous.log", "autonomous")

	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the stale author started its CLI over the successor")
	}
	h.wantStatus(state.StatusRunning)
}
