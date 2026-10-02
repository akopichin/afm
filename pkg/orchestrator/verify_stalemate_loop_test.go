package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/verify"
	"github.com/akopichin/afm/pkg/state"
)

// --- Task 4: resolver ---

// TestVerifyReviewPatience_ResolutionHelper — verifyReviewPatience: stage override
// побеждает глобальный конфиг, тот — дефолт 0 (выключено).
func TestVerifyReviewPatience_ResolutionHelper(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")

	if got := o.verifyReviewPatience(flow.Stage{ID: "s1"}); got != 0 {
		t.Errorf("no config, no stage override: got %d, want default 0", got)
	}

	four := 4
	o.opts.Config.Verify = config.VerifyConfig{ReviewPatience: &four}
	if got := o.verifyReviewPatience(flow.Stage{ID: "s1"}); got != 4 {
		t.Errorf("global config: got %d, want 4", got)
	}

	zero := 0
	if got := o.verifyReviewPatience(flow.Stage{ID: "s1", Verify: flow.VerifySpec{ReviewPatience: &zero}}); got != 0 {
		t.Errorf("stage override 0 must beat global 4: got %d, want 0", got)
	}
}

// --- Task 5: stalemate loop ---

func intPtr(n int) *int { return &n }

// agentVerifyStage собирает стадию с agent-шагом verify и заданными бюджетами.
func agentVerifyStage(patience, maxFailures int, steps ...flow.VerifyStep) flow.Stage {
	return flow.Stage{ID: "s1", Verify: flow.VerifySpec{
		Steps:          steps,
		ReviewPatience: intPtr(patience),
		MaxFailures:    intPtr(maxFailures),
	}}
}

func agentStep() flow.VerifyStep { return flow.VerifyStep{Kind: flow.VerifyAgent, Command: "codex"} }
func shellStep() flow.VerifyStep {
	return flow.VerifyStep{Kind: flow.VerifyShell, Run: "go test ./..."}
}

// countStalemateNotices считает accepted_stalemate verify_result-записи в notices.jsonl.
func countStalemateNotices(t *testing.T, runDir string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(runDir, "notices.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatal(err)
	}
	n := 0
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.Contains(line, string(verify.VerdictAcceptedStalemate)) {
			n++
		}
	}
	return n
}

// drainAgentCompleted читает из critical-канала и сообщает, был ли опубликован
// EventAgentCompleted (success-путь принятия стадии).
func drainAgentCompleted(events <-chan bus.Event) bool {
	for {
		select {
		case ev := <-events:
			if ev.Type == bus.EventAgentCompleted {
				return true
			}
		default:
			return false
		}
	}
}

// TestStalemate_NoEditsAcceptsStage — автор ничего не правит, агентский шаг
// отклоняет каждый раунд → стадия принимается по stalemate на (patience+1)-м
// отклонении, публикуется EventAgentCompleted и accepted_stalemate verify_result.
func TestStalemate_NoEditsAcceptsStage(t *testing.T) {
	o, runDir := setupHookOrch(t, "s1")
	o.maxRetries = 15
	o.fingerprintFn = func(string) (string, bool) { return "frozen", true }
	events := o.critical.Recv()

	stage := agentVerifyStage(2, 6, agentStep())

	var attempts int
	o.runWithRetry(context.Background(), stage, phaseImplementation,
		func(string) error { attempts++; return nil },
		func() error { return newVerifyRejected(1) },
		func() { t.Error("onUserInterrupted should not be called") },
	)

	// patience=2: reject#1 baseline, reject#2 count=1, reject#3 count=2 → accept.
	if attempts != 3 {
		t.Fatalf("patience=2: expected 3 attempts then accept, got %d", attempts)
	}
	// Capture the completion event and drive it through the NORMAL handler
	// (onAgentCompleted) to prove the stage reaches done via the real success
	// path — not merely "not failed" (a still-running stage would pass that).
	var completed bus.Event
	select {
	case completed = <-events:
	default:
		t.Fatal("expected EventAgentCompleted published on stalemate accept")
	}
	if completed.Type != bus.EventAgentCompleted || completed.StageID != "s1" {
		t.Fatalf("got %+v, want EventAgentCompleted for s1", completed)
	}
	if err := o.onAgentCompleted(context.Background(), completed); err != nil {
		t.Fatalf("onAgentCompleted: %v", err)
	}
	if got := o.opts.Store.Get("s1"); got != state.StatusDone {
		t.Fatalf("status = %v, want done (stalemate accept must complete via the normal success path)", got)
	}
	if n := countStalemateNotices(t, runDir); n != 1 {
		t.Errorf("expected exactly 1 accepted_stalemate verify_result notice, got %d", n)
	}
}

// TestStalemate_ShellFirstThenAgentAccepts — безопасный порядок [shell, agent]:
// agent (step 2) отклоняет, shell (step 1) уже прошёл fail-fast → stalemate
// принимает.
func TestStalemate_ShellFirstThenAgentAccepts(t *testing.T) {
	o, runDir := setupHookOrch(t, "s1")
	o.maxRetries = 15
	o.fingerprintFn = func(string) (string, bool) { return "frozen", true }
	events := o.critical.Recv()

	stage := agentVerifyStage(1, 6, shellStep(), agentStep())

	var attempts int
	o.runWithRetry(context.Background(), stage, phaseImplementation,
		func(string) error { attempts++; return nil },
		func() error { return newVerifyRejected(2) }, // agent step rejects
		func() { t.Error("onUserInterrupted should not be called") },
	)

	// patience=1: reject#1 baseline, reject#2 count=1 → accept.
	if attempts != 2 {
		t.Fatalf("expected 2 attempts then accept, got %d", attempts)
	}
	if !drainAgentCompleted(events) {
		t.Error("expected EventAgentCompleted on stalemate accept (shell before agent is safe)")
	}
	if n := countStalemateNotices(t, runDir); n != 1 {
		t.Errorf("expected 1 accepted_stalemate notice, got %d", n)
	}
}

// TestStalemate_AgentFirstUnrunShellNeverAccepts — опасный порядок [agent, shell]:
// agent (step 1) отклоняет, а shell (step 2) в этом проходе НЕ запускался
// (fail-fast) → stalemate НЕ вправе принять; стадия падает на max_failures.
func TestStalemate_AgentFirstUnrunShellNeverAccepts(t *testing.T) {
	o, runDir := setupHookOrch(t, "s1")
	o.maxRetries = 15
	o.fingerprintFn = func(string) (string, bool) { return "frozen", true }
	events := o.critical.Recv()

	stage := agentVerifyStage(1, 2, agentStep(), shellStep())

	var attempts int
	o.runWithRetry(context.Background(), stage, phaseImplementation,
		func(string) error { attempts++; return nil },
		func() error { return newVerifyRejected(1) }, // agent step rejects; shell step 2 never ran
		func() { t.Error("onUserInterrupted should not be called") },
	)

	// max_failures=2 → 2 corrections then fail; stalemate must never fire.
	if attempts != 3 {
		t.Fatalf("expected 3 attempts then fail (no stalemate), got %d", attempts)
	}
	if drainAgentCompleted(events) {
		t.Error("EventAgentCompleted must NOT be published — unrun shell step after the agent reject")
	}
	if got := o.opts.Store.Get("s1"); got != state.StatusFailed {
		t.Errorf("status = %v, want failed", got)
	}
	if n := countStalemateNotices(t, runDir); n != 0 {
		t.Errorf("expected 0 accepted_stalemate notices, got %d", n)
	}
}

// TestStalemate_ShellOnlyNeverAccepts — shell-only verify: красный shell-шаг
// отклоняет каждый раунд → НИКОГДА не принимается, падает на max_failures,
// независимо от review_patience.
func TestStalemate_ShellOnlyNeverAccepts(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	o.maxRetries = 15
	o.fingerprintFn = func(string) (string, bool) { return "frozen", true }
	events := o.critical.Recv()

	stage := agentVerifyStage(1, 2, shellStep())

	var attempts int
	o.runWithRetry(context.Background(), stage, phaseImplementation,
		func(string) error { attempts++; return nil },
		func() error { return newVerifyRejected(1) }, // shell step rejects
		func() { t.Error("onUserInterrupted should not be called") },
	)

	if attempts != 3 {
		t.Fatalf("expected 3 attempts then fail, got %d", attempts)
	}
	if drainAgentCompleted(events) {
		t.Error("EventAgentCompleted must NOT be published for a red shell step")
	}
	if got := o.opts.Store.Get("s1"); got != state.StatusFailed {
		t.Errorf("status = %v, want failed", got)
	}
}

// TestStalemate_ChangingTreeNeverAccepts — автор правит дерево каждый раунд
// (отпечаток всё время разный) → stalemate никогда не срабатывает, падает на
// max_failures.
func TestStalemate_ChangingTreeNeverAccepts(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	o.maxRetries = 15
	var fpN int
	o.fingerprintFn = func(string) (string, bool) {
		fpN++
		return fmt.Sprintf("fp-%d", fpN), true
	}
	events := o.critical.Recv()

	stage := agentVerifyStage(2, 2, agentStep())

	var attempts int
	o.runWithRetry(context.Background(), stage, phaseImplementation,
		func(string) error { attempts++; return nil },
		func() error { return newVerifyRejected(1) },
		func() { t.Error("onUserInterrupted should not be called") },
	)

	if attempts != 3 {
		t.Fatalf("expected 3 attempts then fail (tree changes every round), got %d", attempts)
	}
	if drainAgentCompleted(events) {
		t.Error("EventAgentCompleted must NOT be published when the tree changes every round")
	}
	if got := o.opts.Store.Get("s1"); got != state.StatusFailed {
		t.Errorf("status = %v, want failed", got)
	}
}

// TestStalemate_NotOkFingerprintNeverCounts — отпечаток !ok (не git / слишком
// большой файл) никогда не засчитывается как «без изменений» → stalemate не
// срабатывает, падает на max_failures.
func TestStalemate_NotOkFingerprintNeverCounts(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	o.maxRetries = 15
	o.fingerprintFn = func(string) (string, bool) { return "", false }
	events := o.critical.Recv()

	stage := agentVerifyStage(1, 2, agentStep())

	var attempts int
	o.runWithRetry(context.Background(), stage, phaseImplementation,
		func(string) error { attempts++; return nil },
		func() error { return newVerifyRejected(1) },
		func() { t.Error("onUserInterrupted should not be called") },
	)

	if attempts != 3 {
		t.Fatalf("expected 3 attempts then fail (!ok never counts), got %d", attempts)
	}
	if drainAgentCompleted(events) {
		t.Error("EventAgentCompleted must NOT be published when fingerprint is !ok")
	}
	if got := o.opts.Store.Get("s1"); got != state.StatusFailed {
		t.Errorf("status = %v, want failed", got)
	}
}

// TestStalemate_ChangeThenFreezeAccepts — A/B композиция: автор правит дерево
// первые раунды (отпечаток меняется, счётчик сбрасывается), затем замирает →
// B принимает, отсчитывая от ПОСЛЕДНЕГО изменения, а не от первого отклонения.
// Доказывает, что базовый снимок переустанавливается на каждом изменении.
func TestStalemate_ChangeThenFreezeAccepts(t *testing.T) {
	o, runDir := setupHookOrch(t, "s1")
	o.maxRetries = 15
	var fpN int
	o.fingerprintFn = func(string) (string, bool) {
		fpN++
		switch fpN {
		case 1:
			return "fp-1", true
		case 2:
			return "fp-2", true
		default:
			return "frozen", true // заморозка начиная с 3-го отклонения
		}
	}
	events := o.critical.Recv()

	stage := agentVerifyStage(2, 6, agentStep())

	var attempts int
	o.runWithRetry(context.Background(), stage, phaseImplementation,
		func(string) error { attempts++; return nil },
		func() error { return newVerifyRejected(1) },
		func() { t.Error("onUserInterrupted should not be called") },
	)

	// reject#1 fp-1 baseline; reject#2 fp-2 (change, reset); reject#3 frozen
	// (change vs fp-2, new baseline); reject#4 frozen count=1; reject#5 frozen
	// count=2 → accept. Итого 5 вызовов агента — счёт идёт от последнего
	// изменения (reject#3), не от reject#1.
	if attempts != 5 {
		t.Fatalf("expected 5 attempts (2 changing + 3 frozen), got %d", attempts)
	}
	if !drainAgentCompleted(events) {
		t.Error("expected EventAgentCompleted after the tree freezes")
	}
	if got := o.opts.Store.Get("s1"); got == state.StatusFailed {
		t.Error("stage must NOT be failed — it froze and was accepted")
	}
	if n := countStalemateNotices(t, runDir); n != 1 {
		t.Errorf("expected 1 accepted_stalemate notice, got %d", n)
	}
}

// TestStalemate_ReviseDuringFingerprintNotClobbered — Revise, закоммиченный в
// окне отпечатка (ровно перед ownership re-check), не затирается accept'ом:
// EventAgentCompleted НЕ публикуется, onUserInterrupted вызывается (ветка
// revising у verifyOutcomeStillOwned), стадия остаётся revising.
func TestStalemate_ReviseDuringFingerprintNotClobbered(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	o.maxRetries = 15
	events := o.critical.Recv()

	var fpN int
	o.fingerprintFn = func(string) (string, bool) {
		fpN++
		if fpN == 2 {
			// Второй вызов — на нём счётчик достигнет patience=1 и код пойдёт
			// на accept. Симулируем Revise, долговечно закоммиченный в окне
			// отпечатка, ДО ownership re-check.
			if err := o.opts.Store.Apply(&state.Transition{StageID: "s1", From: state.StatusRunning, To: state.StatusRevising, Event: "test_revise"}); err != nil {
				t.Error(err)
			}
		}
		return "frozen", true
	}

	var interrupted bool
	stage := agentVerifyStage(1, 6, agentStep())

	o.runWithRetry(context.Background(), stage, phaseImplementation,
		func(string) error { return nil },
		func() error { return newVerifyRejected(1) },
		func() { interrupted = true },
	)

	if !interrupted {
		t.Error("onUserInterrupted must be called for a Revise (revising branch)")
	}
	if drainAgentCompleted(events) {
		t.Error("EventAgentCompleted must NOT be published — a Revise landed in the fingerprint window")
	}
	if got := o.opts.Store.Get("s1"); got != state.StatusRevising {
		t.Errorf("status = %v, want revising (Revise must survive the stalemate window)", got)
	}
}

// TestStalemate_PauseDuringFingerprintNotClobbered — Pause, закоммиченный в окне
// вычисления отпечатка (ровно перед ownership re-check), не затирается
// accept-публикацией: EventAgentCompleted НЕ публикуется, стадия остаётся paused.
func TestStalemate_PauseDuringFingerprintNotClobbered(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	o.maxRetries = 15
	events := o.critical.Recv()

	var fpN int
	o.fingerprintFn = func(string) (string, bool) {
		fpN++
		if fpN == 2 {
			// Второй вызов — тот, на котором счётчик достигнет patience и
			// код пойдёт на accept. Симулируем Pause, долговечно
			// закоммиченный в окне отпечатка, ДО ownership re-check.
			if err := o.opts.Store.Apply(&state.Transition{StageID: "s1", From: state.StatusRunning, To: state.StatusPaused, Event: "test_pause"}); err != nil {
				t.Error(err)
			}
		}
		return "frozen", true
	}

	stage := agentVerifyStage(1, 6, agentStep())

	o.runWithRetry(context.Background(), stage, phaseImplementation,
		func(string) error { return nil },
		func() error { return newVerifyRejected(1) },
		func() { t.Error("onUserInterrupted should not be called for a Pause (paused branch)") },
	)

	if drainAgentCompleted(events) {
		t.Error("EventAgentCompleted must NOT be published — a Pause landed in the fingerprint window")
	}
	if got := o.opts.Store.Get("s1"); got != state.StatusPaused {
		t.Errorf("status = %v, want paused (Pause must survive the stalemate window)", got)
	}
}
